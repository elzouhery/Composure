// Package hub is a Docker Hub search and tag-metadata client.
//
// Measured against the live API in August 2026:
//
//	Authentication  none required for search or tag listing
//	Rate limit      x-ratelimit-limit: 180 per minute, per IP
//	Search returns  repo name, description, star count, pull count, official flag
//	Tags return     compressed size, last-pushed timestamp, per-architecture digests
//
// 180 requests per minute against a desktop application used by one human is
// enormous headroom, so v1 needs NO server component. That matters beyond
// convenience: a purely local tool has no accounts, no hosting bill, no privacy
// question about which images a user searched for, and nothing to keep running.
//
// The limit is per-IP, so the one case that breaks is many users behind one
// corporate NAT. Handle it by caching aggressively and degrading politely, not
// by building a backend before anyone has hit the ceiling.
//
// Note what is NOT available here: CVE counts. Those need Docker Scout (paid
// beyond one repository) or a local scan with Trivy or Grype. Do not promise a
// vulnerability facet that this API cannot deliver.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Endpoint selection matters more than it looks.
//
// hub.docker.com/v2/search/repositories/ — the obvious choice — sits behind
// Cloudflare bot protection. It answers curl with 200 and a Go client with 403
// and a "Just a moment..." interstitial, from the same IP, regardless of
// User-Agent. The rejection is on the TLS fingerprint, so no header will fix it
// and no amount of local testing with curl will reveal it.
//
// Two endpoints do answer a Go client:
//
//	api/search/v4          richer: badges, architectures, OS list, publisher
//	index.docker.io v1     legacy but stable, used here as a fallback
//
// Neither is a documented, contractual API. That, not the rate limit, is the
// real argument for eventually putting a thin service in front of search: it
// lets you change endpoints once instead of shipping a desktop release to every
// user the next time Cloudflare tightens. Build behind the interface now, add
// the service only when it earns its place.
const (
	searchV4URL     = "https://hub.docker.com/api/search/v4/"
	searchLegacyURL = "https://index.docker.io/v1/search"
	tagsURL         = "https://hub.docker.com/v2/repositories/%s/tags/"
)

// The failures a caller has to be able to tell apart. They are sentinels rather
// than strings because the difference between them is a different SENTENCE on
// screen, and branching on the wording of an error is how that goes wrong.
//
// `ErrRateLimited` in particular is not a fault: the 180/minute limit is per IP,
// so the reader who hits it is most likely behind a corporate NAT and has done
// nothing at all.
var (
	// ErrOffline is a transport that never reached Docker Hub — no route, DNS,
	// a proxy, a laptop on a train.
	ErrOffline = errors.New("docker hub could not be reached")
	// ErrRateLimited is a 429.
	ErrRateLimited = errors.New("docker hub is rate limiting this address")
	// ErrNotFound is a repository or tag Docker Hub does not have.
	ErrNotFound = errors.New("docker hub has no such repository")
	// ErrDisabled is the reader's own switch: COMPOSURE_OFFLINE, or the
	// extension's `composure.dockerHub: off`. It is NOT the same as being offline
	// and must never be reported as if it were.
	ErrDisabled = errors.New("docker hub lookup is switched off")
)

// Client talks to Docker Hub's public API.
//
// The three endpoints are FIELDS rather than constants (R6.6). None of them is
// a documented contract, `api/search/v4` is shaped for Docker's own UI, and the
// day Cloudflare tightens again the fix has to be one struct rather than a
// release to every user. It is also what lets every test in this package point
// at an httptest.Server and open no socket to Docker Hub at all.
type Client struct {
	HTTP *http.Client

	SearchURL       string
	LegacySearchURL string
	// TagsURL is a format string taking the normalised repository.
	TagsURL string
}

func New() *Client {
	return &Client{
		HTTP:            &http.Client{Timeout: 15 * time.Second},
		SearchURL:       searchV4URL,
		LegacySearchURL: searchLegacyURL,
		TagsURL:         tagsURL,
	}
}

// Repo is one search result, normalised across the two search endpoints.
// The json tags are the wire the CLI prints and the extension reads. They are
// snake_case to match every other payload in this product, and they are read by
// NAME in TypeScript — nothing in either type system spans that gap, so a
// rename here draws an empty popup and compiles clean on both sides.
type Repo struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Stars         int      `json:"stars"`
	PullsDisplay  string   `json:"pulls_display,omitempty"` // "1B+" — v4 returns a display string, not a count
	Official      bool     `json:"official"`
	Badge         string   `json:"badge,omitempty"` // "official", "verified_publisher", "open_source", "dhi"
	Architectures []string `json:"architectures,omitempty"`
}

// v4SearchResponse maps the api/search/v4 shape. Note pull_count arrives as a
// display string ("1B+"), not a number — a reminder that this is an internal
// endpoint shaped for Docker's own UI, not a contract.
type v4SearchResponse struct {
	Total   int `json:"total"`
	Results []struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		ShortDescription string `json:"short_description"`
		Badge            string `json:"badge"`
		StarCount        int    `json:"star_count"`
		PullCount        string `json:"pull_count"`
		Architectures    []struct {
			Name string `json:"name"`
		} `json:"architectures"`
	} `json:"results"`
}

type legacySearchResponse struct {
	Results []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		StarCount   int    `json:"star_count"`
		IsOfficial  bool   `json:"is_official"`
	} `json:"results"`
}

// RateLimit carries what the API reported about the current quota.
type RateLimit struct {
	Limit     string `json:"limit,omitempty"`
	Remaining string `json:"remaining,omitempty"`
}

// Search queries Docker Hub, preferring the richer v4 endpoint and falling back
// to the legacy index endpoint if it fails. Both are undocumented; treating
// either as guaranteed would be a mistake.
func (c *Client) Search(q string, pageSize int) ([]Repo, RateLimit, error) {
	return c.SearchContext(context.Background(), q, pageSize)
}

// SearchContext is Search, cancellable. Every call this package makes is
// cancellable: a reader who selects another service has stopped waiting for the
// answer to the previous question, and a socket that outlives the question is
// the thing that turns a pane into a hang.
func (c *Client) SearchContext(ctx context.Context, q string, pageSize int) ([]Repo, RateLimit, error) {
	if pageSize <= 0 {
		pageSize = 25
	}
	u := c.searchURL() + "?" + url.Values{
		"query": {q},
		"size":  {fmt.Sprint(pageSize)},
	}.Encode()

	var v4 v4SearchResponse
	rl, err := c.getJSON(ctx, u, &v4)
	if err == nil {
		out := make([]Repo, 0, len(v4.Results))
		for _, r := range v4.Results {
			name := r.Name
			if r.ID != "" {
				name = strings.TrimPrefix(r.ID, "library/")
			}
			var archs []string
			for _, a := range r.Architectures {
				if a.Name != "unknown" {
					archs = append(archs, a.Name)
				}
			}
			out = append(out, Repo{
				Name:          name,
				Description:   r.ShortDescription,
				Stars:         r.StarCount,
				PullsDisplay:  r.PullCount,
				Official:      r.Badge == "official",
				Badge:         r.Badge,
				Architectures: archs,
			})
		}
		return out, rl, nil
	}

	lu := c.legacySearchURL() + "?" + url.Values{
		"q": {q},
		"n": {fmt.Sprint(pageSize)},
	}.Encode()
	var lg legacySearchResponse
	rl2, err2 := c.getJSON(ctx, lu, &lg)
	if err2 != nil {
		// The sentinel survives the join, so a caller can still tell "rate
		// limited" from "offline" after both doors have been tried. Wrapping
		// the second is deliberate: it is the one that ran last, and if the
		// first was a 429 the second almost certainly is too.
		return nil, rl2, fmt.Errorf("v4 search failed (%v); legacy search failed: %w", err, err2)
	}
	out := make([]Repo, 0, len(lg.Results))
	for _, r := range lg.Results {
		out = append(out, Repo{
			Name:        r.Name,
			Description: r.Description,
			Stars:       r.StarCount,
			Official:    r.IsOfficial,
		})
	}
	return out, rl2, nil
}

// Tag is one tag with the metadata needed to compare candidates.
type Tag struct {
	Name       string    `json:"name"`
	FullSize   int64     `json:"full_size"`
	LastPushed time.Time `json:"tag_last_pushed"`
	Images     []struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
		Digest       string `json:"digest"`
	} `json:"images"`
}

// Architectures returns the distinct os/arch pairs a tag supports, skipping the
// "unknown/unknown" entries Docker Hub emits for attestation manifests.
func (t Tag) Architectures() []string {
	seen := map[string]bool{}
	var out []string
	for _, im := range t.Images {
		if im.OS == "unknown" || im.Architecture == "unknown" {
			continue
		}
		s := im.OS + "/" + im.Architecture
		if im.Variant != "" {
			s += "/" + im.Variant
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

type tagsResponse struct {
	Results []Tag `json:"results"`
}

// Tags lists tags for a repository, newest first. When nameFilter is non-empty
// the API returns only tags containing that substring.
//
// The filter is not a nicety. Ordering `golang` by last-updated returns 100
// `tip-*` nightlies before a single stable release appears, so an unfiltered
// first page finds no upgrade candidate at all and the tool reports "no newer
// tag" for an image that is three minor versions behind. Filter by family, or
// paginate until you have stable tags — never trust one page.
func (c *Client) Tags(repo string, pageSize int, nameFilter string) ([]Tag, RateLimit, error) {
	return c.TagsContext(context.Background(), repo, pageSize, nameFilter)
}

// TagsContext is Tags, cancellable, and is the TagLister every caller in this
// package is written against.
func (c *Client) TagsContext(ctx context.Context, repo string, pageSize int, nameFilter string) ([]Tag, RateLimit, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	q := url.Values{
		"page_size": {fmt.Sprint(pageSize)},
		"ordering":  {"last_updated"},
	}
	if nameFilter != "" {
		q.Set("name", nameFilter)
	}
	u := fmt.Sprintf(c.tagsURL(), NormalizeRepo(repo)) + "?" + q.Encode()

	var out tagsResponse
	rl, err := c.getJSON(ctx, u, &out)
	return out.Results, rl, err
}

func (c *Client) searchURL() string {
	if c.SearchURL != "" {
		return c.SearchURL
	}
	return searchV4URL
}

func (c *Client) legacySearchURL() string {
	if c.LegacySearchURL != "" {
		return c.LegacySearchURL
	}
	return searchLegacyURL
}

func (c *Client) tagsURL() string {
	if c.TagsURL != "" {
		return c.TagsURL
	}
	return tagsURL
}

func (c *Client) getJSON(ctx context.Context, u string, v any) (RateLimit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return RateLimit{}, err
	}
	req.Header.Set("Accept", "application/json")
	// Docker Hub rejects Go's default user agent with 403. Identify properly.
	req.Header.Set("User-Agent", "composure/0.1 (+https://github.com/elzouhery/composure)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// A cancellation is not a network failure and must not be reported as
		// one: nobody is waiting for this answer, and telling the reader they
		// are offline because they clicked another service would be a
		// confident wrong answer about their machine.
		if ctx.Err() != nil {
			return RateLimit{}, ctx.Err()
		}
		return RateLimit{}, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	defer resp.Body.Close()

	rl := RateLimit{
		Limit:     resp.Header.Get("x-ratelimit-limit"),
		Remaining: resp.Header.Get("x-ratelimit-remaining"),
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return rl, fmt.Errorf("%w (retry after %s)", ErrRateLimited, resp.Header.Get("retry-after"))
	case resp.StatusCode == http.StatusNotFound:
		return rl, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return rl, fmt.Errorf("docker hub returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return rl, fmt.Errorf("docker hub returned something that is not JSON: %w", err)
	}
	return rl, nil
}

// NormalizeRepo expands a short official-image name into the library/ form the
// API requires: "nginx" becomes "library/nginx", "bitnami/redis" is unchanged.
// An explicit Docker Hub host is stripped first: `docker.io/library/postgres`
// is the same repository as `postgres`, and leaving the host on produces
// `/v2/repositories/docker.io/library/postgres/tags/`, which is a 404 that
// reads as "this image does not exist".
func NormalizeRepo(ref string) string {
	ref = StripTag(ref)
	for _, host := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, host)
	}
	if !strings.Contains(ref, "/") {
		return "library/" + ref
	}
	return ref
}

// StripTag removes a :tag or @digest suffix from an image reference, leaving
// the repository. It is registry-host aware: the colon in "host:5000/img" is a
// port, not a tag.
func StripTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		ref = ref[:i]
	}
	return ref
}

// TagOf returns the tag portion of a reference, or "latest" when implicit.
func TagOf(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[i+1:]
	}
	return "latest"
}

// ---------------------------------------------------------------------------
// Tag semantics
// ---------------------------------------------------------------------------
//
// The hard part of image discovery is not the API, it is deciding what counts
// as an upgrade. A naive "most recently pushed tag in the same family" picks
// `alpine:edge` over `alpine:3.19` and `golang:tip-alpine3.24` over
// `golang:1.23-alpine` — rolling and nightly builds that are strictly worse
// suggestions than the pin they replace. Recommend one of those once and the
// user never trusts the feature again.

var unstableMarkers = []string{
	"edge", "tip", "nightly", "dev", "devel", "rc", "alpha", "beta",
	"canary", "unstable", "experimental", "test", "snapshot", "preview",
}

// IsUnstable reports whether a tag names a rolling, prerelease or nightly
// build that should never be offered as an upgrade to a pinned tag.
func IsUnstable(tag string) bool {
	l := strings.ToLower(tag)
	if l == "latest" {
		return true // not unstable, but never a pin — offering it is a downgrade in determinism
	}
	for _, m := range unstableMarkers {
		if l == m || strings.HasPrefix(l, m+"-") || strings.HasPrefix(l, m+".") ||
			strings.Contains(l, "-"+m) || strings.Contains(l, "."+m) {
			return true
		}
		// Markers also appear with no separator at all, wedged between digits:
		// "1.27rc2-alpine3.24" is a release candidate and slipped past the
		// separator checks above. Require a digit before the marker so "arch"
		// and similar innocent substrings are not caught.
		if idx := strings.Index(l, m); idx > 0 && isDigit(l[idx-1]) {
			after := idx + len(m)
			if after >= len(l) || isDigit(l[after]) || l[after] == '-' || l[after] == '.' {
				return true
			}
		}
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Version extracts the leading dotted numeric version from a tag:
// "3.20.1-alpine" gives [3 20 1], "1.24-alpine" gives [1 24].
// Returns nil when the tag carries no comparable version.
func Version(tag string) []int {
	i := 0
	for i < len(tag) && (tag[i] >= '0' && tag[i] <= '9' || tag[i] == '.') {
		i++
	}
	head := strings.Trim(tag[:i], ".")
	if head == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(head, ".") {
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				return out
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out
}

// CompareVersions returns -1, 0 or 1. A shorter version that is otherwise equal
// sorts lower, so 3.20 < 3.20.1.
func CompareVersions(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// IsDateTag reports whether a tag is a date stamp such as "20260805" or
// "2026-08-05". These sort numerically above every real version — 20260805 is
// "greater than" 3.19 — so without this check an alpine:3.19 pin gets offered a
// date-stamped snapshot as its upgrade. Found by running the tool against a
// real Dockerfile, not by unit test.
func IsDateTag(tag string) bool {
	digits := 0
	for _, c := range tag {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '-' || c == '.' || c == '_':
		default:
			return false // contains letters: not a bare date stamp
		}
	}
	return digits >= 6
}

// UpgradeKind classifies the step from one version to another.
type UpgradeKind string

const (
	UpgradePatch UpgradeKind = "patch"
	UpgradeMinor UpgradeKind = "minor"
	UpgradeMajor UpgradeKind = "major"
	UpgradeNone  UpgradeKind = "none"
)

// Classify describes the jump from cur to next. Major upgrades are surfaced
// rather than hidden: they are legitimate, they are also the ones that break
// builds, and the user is entitled to know which kind they are accepting.
func Classify(cur, next []int) UpgradeKind {
	if CompareVersions(next, cur) <= 0 {
		return UpgradeNone
	}
	if len(cur) == 0 || len(next) == 0 {
		return UpgradeMajor
	}
	if cur[0] != next[0] {
		return UpgradeMajor
	}
	if len(cur) > 1 && len(next) > 1 && cur[1] != next[1] {
		return UpgradeMinor
	}
	return UpgradePatch
}
