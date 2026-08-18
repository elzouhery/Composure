package hub

// Looking one image reference up, and saying what came back in a form a reader
// can act on.
//
// THIS IS THE FIRST CAPABILITY IN COMPOSURE THAT IS NOT A PURE FUNCTION OF FILES
// ON DISK. Every rule in this file follows from that (DECISIONS.md 22):
//
//   - There is no error return. `Look` always produces a Report, and the Report
//     always carries a State and a sentence. A caller that had to decide how to
//     word a network failure would word it differently in the CLI and in the
//     pane, and one of the two would end up printing `dial tcp` at a human.
//   - Offline, rate-limited, switched-off, another registry, not-comparable and
//     "already current" are SIX states, not six spellings of failure. Docker
//     Hub's limit is 180 a minute per IP: the reader who trips it is behind a
//     corporate NAT and has done nothing wrong, and telling them "the request
//     failed" is both useless and untrue.
//   - Every call takes a context. The question is asked on behalf of a
//     selection, and a selection can be replaced a keystroke later.
//   - R6.7: there is no CVE facet here and there must never be one. The public
//     API does not carry that data. `TestNothingPromisesAVulnerabilityFacet`
//     marshals a Report and fails on the word.
//
// The candidate SELECTION lives here rather than in a command, which is where
// it used to be: `cmd/hubsearch/main.go` is a `main` package, so the rules that
// decide what may be offered as an upgrade could not be called by the CLI, the
// RPC or the extension. Three copies of "is `alpine:edge` an upgrade" is three
// answers.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// State is what came back, as a slug a caller branches on. Closed set.
type State string

const (
	// StateOK is a lookup that found a newer stable tag in the same family.
	StateOK State = "ok"
	// StateCurrent is a lookup that ran and found nothing newer. It is a real
	// answer and a good one; it is not an absence.
	StateCurrent State = "current"
	// StateOffline is Docker Hub unreachable.
	StateOffline State = "offline"
	// StateRateLimited is the 180-per-IP-per-minute ceiling.
	StateRateLimited State = "rate-limited"
	// StateNotFound is a repository Docker Hub does not have.
	StateNotFound State = "not-found"
	// StateOtherRegistry is a reference belonging to somebody else's registry.
	// Requirements §3: cross-registry search is out of scope, permanently.
	StateOtherRegistry State = "other-registry"
	// StateNotComparable is a reference with no single tag to compare — an
	// interpolated one, `scratch`, or a digest pin.
	StateNotComparable State = "not-comparable"
	// StateDisabled is the reader's own switch. Deliberately not StateOffline:
	// one of them is a fact about their network and the other is a fact about
	// their settings.
	StateDisabled State = "disabled"
	// StateCancelled is a question nobody is waiting for the answer to.
	StateCancelled State = "cancelled"
)

// Candidate is one tag offered in place of the current one.
type Candidate struct {
	// Reference is what would go in the file — `postgres:18-alpine`, spelled
	// the way the file spells the repository.
	Reference string      `json:"reference"`
	Tag       string      `json:"tag"`
	Kind      UpgradeKind `json:"kind"`
	Pushed    time.Time   `json:"pushed,omitzero"`
	Size      int64       `json:"size,omitempty"`
	// SizeDelta is this tag's size minus the current one's, in bytes. Zero
	// when either is unknown, which is why `HasSize` exists rather than a
	// reader inferring "same size" from a zero.
	SizeDelta int64 `json:"size_delta,omitempty"`
	HasSize   bool  `json:"has_size,omitempty"`
}

// Report is the whole answer about one reference. It is always complete: a
// State, a sentence, and whatever else could be established.
type Report struct {
	// Reference is what the file says, verbatim.
	Reference string `json:"reference"`
	// Repository is the normalised Docker Hub repository, `library/postgres`.
	Repository string `json:"repository"`
	// Display is the repository as the FILE spells it, so a pill reads
	// `postgres:18-alpine` and not `library/postgres:18-alpine`.
	Display string `json:"display"`
	Tag     string `json:"tag"`

	State State `json:"state"`
	// Message is the sentence a reader sees. Never an error string, never a
	// transport word, never empty.
	Message string `json:"message"`

	// `omitzero`, not `omitempty`: a zero time.Time is not empty, and the field
	// serialised as `"0001-01-01T00:00:00Z"` — a client reading it as a date
	// would report an image pushed in the year one.
	CurrentPushed time.Time `json:"current_pushed,omitzero"`
	CurrentSize   int64     `json:"current_size,omitempty"`
	// Age is how old the current tag is, in the reader's words: `14 months old`.
	Age     string `json:"age,omitempty"`
	AgeDays int    `json:"age_days,omitempty"`

	Candidate    *Candidate  `json:"candidate,omitempty"`
	Alternatives []Candidate `json:"alternatives,omitempty"`

	// Pill is the mockup's own string — `node:22-alpine · minor · 40MB smaller`
	// (directions-3.html:576) — composed HERE so the CLI table, the JSON and
	// the pane cannot word one fact three ways.
	Pill string `json:"pill,omitempty"`

	RateLimit RateLimit `json:"rate_limit,omitzero"`
}

// TagLister is the one thing a lookup needs. An interface rather than *Client
// because the cache, the offline guard and every test are all implementations
// of it, and because it is the seam R6.6 asks for.
type TagLister interface {
	Tags(ctx context.Context, repo string, pageSize int, nameFilter string) ([]Tag, RateLimit, error)
}

// tagsPage is how many tags one lookup asks for. A hundred filtered by family
// is far past any real repository's stable line; the filter is what makes that
// true (R6.4), not the number.
const tagsPage = 100

// Look answers "what is this image, and is there a newer one" without ever
// returning an error. See the file comment for why.
func Look(ctx context.Context, tags TagLister, ref string) Report {
	ref = strings.TrimSpace(ref)
	r := Report{Reference: ref}

	// Everything below this line is decided WITHOUT a request, so a reference
	// nothing can be said about costs nothing and cannot hang.
	switch {
	case ref == "":
		r.State = StateNotComparable
		r.Message = "There is no image reference here to look up."
		return r
	case strings.Contains(ref, "${") || strings.Contains(ref, "$"):
		r.State = StateNotComparable
		r.Message = "This reference is built from a variable, so there is no single tag to " +
			"compare. Resolve the variable and the lookup will work."
		return r
	case ref == "scratch":
		r.State = StateNotComparable
		r.Message = "`scratch` is the empty base image. It has no tags and never changes."
		return r
	case strings.Contains(ref, "@"):
		r.State = StateNotComparable
		r.Repository = NormalizeRepo(ref)
		r.Message = "This image is pinned by digest, which names exact bytes rather than a tag. " +
			"Nothing here can say whether a newer one exists without changing what the pin means."
		return r
	}

	if host, ok := foreignRegistry(ref); ok {
		r.State = StateOtherRegistry
		r.Message = fmt.Sprintf(
			"Composure looks images up on Docker Hub. This one is on %s, and searching across "+
				"registries is deliberately not built — so nothing is claimed about it either way.",
			host)
		return r
	}

	r.Repository = NormalizeRepo(ref)
	r.Display = displayRepo(ref)
	r.Tag = TagOf(ref)

	// R6.4. Filtering by the tag's family is not a nicety: ordering `golang` by
	// last-updated returns a hundred `tip-*` nightlies before one stable
	// release, so an unfiltered first page finds no candidate at all and the
	// tool reports "no newer tag" for an image three versions behind.
	family := FamilySuffix(r.Tag)
	list, rl, err := tags.Tags(ctx, r.Repository, tagsPage, strings.TrimPrefix(family, "-"))
	r.RateLimit = rl
	if err != nil {
		describeFailure(&r, err)
		return r
	}

	current := findTag(list, r.Tag)
	if current == nil {
		// The family page is the hundred most recently pushed tags in the
		// family, and A TAG OLD ENOUGH TO BE WORTH REPLACING IS EXACTLY THE TAG
		// THAT IS NOT ON IT. Found by running the finished CLI against a real
		// Dockerfile: `node:18-alpine` reported the upgrade and no age at all,
		// and the age — the mockup's own "base image 14 months old" — is the
		// headline of the whole feature.
		//
		// One extra request, by exact name, and only when the first page did
		// not carry it. The limit is shared by everyone behind one address, so
		// a request that is not needed is one a colleague then cannot make.
		// A FULL page, not one result. The API's `name` filter is a SUBSTRING
		// match ordered by last-updated, so asking for `1.27-alpine` answers
		// with `1.27-alpine3.21-perl` first and the exact tag several rows
		// down. A page size of one found the decoy every time — measured
		// against the live API on nginx, with every unit test green, because
		// the fake ignored the page size.
		if exact, _, err := tags.Tags(ctx, r.Repository, tagsPage, r.Tag); err == nil {
			current = findTag(exact, r.Tag)
		}
	}
	if current != nil {
		r.CurrentPushed = current.LastPushed
		r.CurrentSize = current.FullSize
	}
	if !r.CurrentPushed.IsZero() {
		r.AgeDays = int(time.Since(r.CurrentPushed).Hours() / 24)
		r.Age = ageWords(r.CurrentPushed)
	}

	best, alternatives := pickCandidates(list, r.Display, r.Tag, family, r.CurrentSize)
	r.Alternatives = alternatives
	if best == nil {
		r.State = StateCurrent
		r.Message = "This is the newest stable tag Docker Hub lists in its family."
		if r.Age != "" {
			r.Message = fmt.Sprintf("This is the newest stable tag Docker Hub lists in its "+
				"family. It was pushed %s.", agePhrase(r.CurrentPushed))
		}
		return r
	}
	r.State = StateOK
	r.Candidate = best
	r.Pill = pill(*best)
	r.Message = fmt.Sprintf("%s is a %s upgrade in the same family.", best.Reference, best.Kind)
	return r
}

// describeFailure turns a transport outcome into a state and a SENTENCE.
func describeFailure(r *Report, err error) {
	r.State, r.Message = Describe(err, r.Repository)
}

// Describe is the ONLY place in this package that decides how a failure is
// worded. Lookup and search both go through it, so a reader cannot be told one
// thing about a 429 in the pane and a different thing in the search popup — and
// neither surface can grow its own copy without deleting this one.
//
// `subject` is what the question was about, for the states that can name it. It
// may be empty; the sentence works either way.
func Describe(err error, subject string) (State, string) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StateCancelled, "The lookup was cancelled."
	case errors.Is(err, ErrDisabled):
		return StateDisabled, "Docker Hub lookup is switched off, so nothing here reaches the " +
			"network. Everything else on this pane is read from your files."
	case errors.Is(err, ErrRateLimited):
		return StateRateLimited, "Docker Hub is rate limiting this address. The limit is a " +
			"hundred and eighty requests a minute and it is shared by everyone behind your " +
			"address, so this is usually a busy office rather than anything you did. It " +
			"passes on its own."
	case errors.Is(err, ErrNotFound):
		if subject == "" {
			return StateNotFound, "Docker Hub has nothing to answer this with."
		}
		return StateNotFound, fmt.Sprintf("Docker Hub has no repository called %s.", subject)
	case errors.Is(err, ErrOffline):
		return StateOffline, "Docker Hub could not be reached, so there is nothing here about " +
			"newer tags. Everything else on this pane is read from your files."
	default:
		// A shape Docker Hub has never returned before. Say that, rather than
		// pretending it is one of the five above.
		return StateOffline, "Docker Hub answered with something this version does not " +
			"understand, so there is nothing here about newer tags. Everything else on this " +
			"pane is read from your files."
	}
}

// findTag returns the tag with this exact name, or nil. The API's `name` filter
// is a SUBSTRING match, so a request for `18-alpine` can answer with
// `18-alpine3.21` as well — the exact name still has to be picked out.
func findTag(list []Tag, name string) *Tag {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// pickCandidates applies R6.3's rules, which are tested in hub_test.go and are
// NOT re-derived anywhere else. A candidate must be:
//
//	the same family     `3.20-alpine` competes with `3.21-alpine`, not `3.21`
//	stable              never a nightly, a rolling branch or a prerelease
//	not a date stamp    `20260805` sorts above every real version
//	strictly HIGHER     not merely pushed more recently
//
// Recommend `alpine:edge` over `alpine:3.19` once and the reader never trusts
// the feature again.
//
// AND — found by running the tool against the live API, which is how the
// date-stamp rule was found too — it must keep the reader's own PINNING POLICY.
// `postgres:16-alpine` was offered `postgres:18.4-alpine3.23`: same family,
// stable, not a date stamp, strictly higher, and still wrong. The reader pinned
// one version component and the plain `-alpine` variant; that tag pins the
// PostgreSQL patch and the Alpine minor as well, which is a different decision
// about how tightly to pin rather than a newer version of theirs. Same-shape
// candidates win when there are any; the family rule still answers when there
// are none, because a shape rule that could produce "nothing is newer" would be
// worse than the defect it fixes.
func pickCandidates(list []Tag, display, cur, family string, curSize int64) (*Candidate, []Candidate) {
	curVer := Version(cur)
	curShape := tagRemainder(cur)
	var out []Candidate
	var sameShape []Candidate
	for i := range list {
		t := &list[i]
		if t.Name == cur || IsUnstable(t.Name) || IsDateTag(t.Name) {
			continue
		}
		if FamilySuffix(t.Name) != family {
			continue
		}
		v := Version(t.Name)
		if v == nil {
			continue
		}
		if curVer != nil && CompareVersions(v, curVer) <= 0 {
			continue
		}
		c := Candidate{
			Reference: display + ":" + t.Name,
			Tag:       t.Name,
			Kind:      Classify(curVer, v),
			Pushed:    t.LastPushed,
			Size:      t.FullSize,
		}
		if curSize > 0 && t.FullSize > 0 {
			c.SizeDelta = t.FullSize - curSize
			c.HasSize = true
		}
		out = append(out, c)
		if len(v) == len(curVer) && tagRemainder(t.Name) == curShape {
			sameShape = append(sameShape, c)
		}
	}
	// The reader's own shape when there is one, the whole family when there is
	// not. A preference, never a requirement.
	if len(sameShape) > 0 {
		out = sameShape
	}
	if len(out) == 0 {
		return nil, nil
	}
	// Highest version first, so `best` is out[0] and the alternatives read in
	// the order a reader would consider them.
	sort.SliceStable(out, func(i, j int) bool {
		return CompareVersions(Version(out[i].Tag), Version(out[j].Tag)) > 0
	})
	best := out[0]
	return &best, out[1:]
}

// tagRemainder is everything after a tag's leading dotted version:
// `16-alpine` gives `-alpine`, `18.4-alpine3.23` gives `-alpine3.23`, `3.21`
// gives the empty string.
//
// Together with the NUMBER of version components it is the tag's shape, which
// is the reader's pinning policy written down. Two tags with the same shape
// differ only in version, which is the one axis an upgrade is allowed to move
// along.
func tagRemainder(tag string) string {
	i := 0
	for i < len(tag) && (tag[i] >= '0' && tag[i] <= '9' || tag[i] == '.') {
		i++
	}
	return tag[i:]
}

// pill is the mockup's string: `node:22-alpine · minor · 40MB smaller`.
//
// The size clause is OMITTED rather than guessed when either size is unknown or
// the two are equal, and it reads `larger` when it is larger. A pill that only
// ever says `smaller` is a pill nobody believes twice.
func pill(c Candidate) string {
	parts := []string{c.Reference, string(c.Kind)}
	if c.HasSize && c.SizeDelta != 0 {
		d, word := c.SizeDelta, "larger"
		if d < 0 {
			d, word = -d, "smaller"
		}
		parts = append(parts, fmt.Sprintf("%s %s", HumanBytes(d), word))
	}
	return strings.Join(parts, " · ")
}

// ageWords is how old a tag is, in the words the mockup's header uses:
// `14 months old`. Days below two months, because "63 days old" is a fact and
// "2 months old" is the same fact a reader can act on.
func ageWords(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	days := int(time.Since(t).Hours() / 24)
	switch {
	case days < 1:
		return "pushed today"
	case days == 1:
		return "1 day old"
	case days < 60:
		return fmt.Sprintf("%d days old", days)
	case days < 730:
		return fmt.Sprintf("%d months old", days/30)
	}
	years := days / 365
	if years == 1 {
		return "1 year old"
	}
	return fmt.Sprintf("%d years old", years)
}

// agePhrase is the same fact in a sentence rather than as a label.
func agePhrase(t time.Time) string {
	w := ageWords(t)
	switch {
	case w == "":
		return "at some point Docker Hub did not report"
	case w == "pushed today":
		return "today"
	default:
		return strings.TrimSuffix(w, " old") + " ago"
	}
}

// HumanBytes is a compressed size a reader can compare at a glance. Exported
// because the CLI table and the pill both need exactly this rounding, and two
// roundings of one number is two numbers.
func HumanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for n/div >= u && exp < 3 {
		div *= u
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 10 || v == float64(int64(v)) {
		return fmt.Sprintf("%.0f%cB", v, "KMGT"[exp])
	}
	return fmt.Sprintf("%.1f%cB", v, "KMGT"[exp])
}

// FamilySuffix classifies a tag by its variant so like is compared with like:
// `3.20-alpine` and `3.21-alpine` share a family; `3.21` does not.
//
// It moved here from `cmd/hubsearch/main.go`, which is a `main` package — so
// the rule that decides which tags may be compared could not be called by the
// CLI, the RPC or the extension, and each of them would have grown its own.
func FamilySuffix(tag string) string {
	for _, s := range []string{"-alpine", "-slim", "-bookworm", "-bullseye", "-trixie",
		"-jammy", "-noble", "-windowsservercore", "-nanoserver", "-distroless"} {
		if strings.Contains(tag, s) {
			return s
		}
	}
	return ""
}

// foreignRegistry reports the registry host when a reference does not belong to
// Docker Hub.
//
// Docker's own rule: the first path segment is a registry host if it contains a
// dot or a colon, or is exactly `localhost`. `docker.io` and `index.docker.io`
// contain a dot and ARE Docker Hub, so they are named explicitly — refusing
// them would refuse the very images this feature is for.
func foreignRegistry(ref string) (string, bool) {
	head, _, ok := strings.Cut(ref, "/")
	if !ok {
		return "", false // `postgres:16` — an official image
	}
	if !strings.ContainsAny(head, ".:") && head != "localhost" {
		return "", false // `bitnami/redis` — a Hub namespace
	}
	if head == "docker.io" || head == "index.docker.io" || head == "registry-1.docker.io" {
		return "", false
	}
	return head, true
}

// displayRepo is the repository as the FILE spells it, minus any explicit
// Docker Hub host: `postgres`, `bitnami/redis`. A pill reading
// `library/postgres:18-alpine` names a reference nobody writes.
func displayRepo(ref string) string {
	r := StripTag(ref)
	for _, host := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		r = strings.TrimPrefix(r, host)
	}
	return strings.TrimPrefix(r, "library/")
}

/* ---- the reader's own switch, and the cache (R6.8) ---------------------- */

// Guarded wraps a TagLister so that `COMPOSURE_OFFLINE=1` refuses BEFORE the
// transport is entered.
//
// It is a real product control — a tool that has been local for its whole life
// does not silently start making requests — and it is also what makes "this
// code path opened no socket" a measured claim: the test counts calls on the
// fake underneath and asserts zero.
func Guarded(inner TagLister) TagLister {
	return guard{inner: inner}
}

type guard struct{ inner TagLister }

func (g guard) Tags(ctx context.Context, repo string, pageSize int, filter string) ([]Tag, RateLimit, error) {
	if Off() {
		return nil, RateLimit{}, ErrDisabled
	}
	return g.inner.Tags(ctx, repo, pageSize, filter)
}

// Off reports whether the reader has switched network lookups off.
func Off() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COMPOSURE_OFFLINE"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Cache is R6.8's "cache aggressively". In-process, bounded, TTL'd.
//
// A FAILURE IS NEVER CACHED. The reader's train came out of the tunnel and the
// next pane should notice; remembering "offline" for five minutes would turn a
// thirty-second outage into a feature that looks broken.
type Cache struct {
	inner TagLister
	ttl   time.Duration

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	tags []Tag
	rl   RateLimit
	at   time.Time
}

// maxCacheEntries bounds the map. A reader cannot look at more repositories
// than this in one session; a pathological project must not turn a cache into a
// leak.
const maxCacheEntries = 256

func NewCache(inner TagLister, ttl time.Duration) *Cache {
	return &Cache{inner: inner, ttl: ttl, entries: map[string]cacheEntry{}}
}

func (c *Cache) Tags(ctx context.Context, repo string, pageSize int, filter string) ([]Tag, RateLimit, error) {
	key := repo + "\x00" + filter
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && time.Since(e.at) < c.ttl {
		c.mu.Unlock()
		return e.tags, e.rl, nil
	}
	c.mu.Unlock()

	tags, rl, err := c.inner.Tags(ctx, repo, pageSize, filter)
	if err != nil {
		return tags, rl, err
	}
	c.mu.Lock()
	if len(c.entries) >= maxCacheEntries {
		// Cheapest bound that cannot leak: start again. An LRU here would be
		// more code than the thing it protects.
		c.entries = map[string]cacheEntry{}
	}
	c.entries[key] = cacheEntry{tags: tags, rl: rl, at: time.Now()}
	c.mu.Unlock()
	return tags, rl, nil
}
