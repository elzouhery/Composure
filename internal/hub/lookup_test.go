package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// NOTHING IN THIS FILE OPENS A SOCKET TO DOCKER HUB.
//
// Every test points the client's three endpoint fields at an httptest.Server on
// loopback, and the offline test asserts on a COUNTER that the transport was
// never entered at all. That last one is the point: "it did not reach the
// network" asserted as an absence is the twenty-second check that could not
// fail, and this repository has already shipped twenty-one of those.

// countingTags is a TagLister that records how many times it was asked. A fake
// rather than a server, so a test can assert the count is zero.
type countingTags struct {
	calls  atomic.Int64
	tags   []Tag
	err    error
	filter string
}

func (c *countingTags) Tags(_ context.Context, _ string, _ int, filter string) ([]Tag, RateLimit, error) {
	c.calls.Add(1)
	c.filter = filter
	return c.tags, RateLimit{Limit: "180", Remaining: "179"}, c.err
}

func pushed(daysAgo int) time.Time {
	return time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
}

// The corpus of tags used by most cases: a postgres-shaped repository with a
// nightly, a date stamp, a release candidate and three real releases.
func postgresTags() []Tag {
	return []Tag{
		{Name: "17-alpine", FullSize: 240 << 20, LastPushed: pushed(3)},
		{Name: "16.4-alpine", FullSize: 250 << 20, LastPushed: pushed(20)},
		{Name: "16-alpine", FullSize: 280 << 20, LastPushed: pushed(420)},
		{Name: "18-alpine", FullSize: 200 << 20, LastPushed: pushed(1)}, // rc below
		{Name: "18rc1-alpine", FullSize: 200 << 20, LastPushed: pushed(1)},
		{Name: "edge", FullSize: 100 << 20, LastPushed: pushed(0)},
		{Name: "20260805", FullSize: 100 << 20, LastPushed: pushed(0)},
		{Name: "17", FullSize: 400 << 20, LastPushed: pushed(3)}, // another family
		// A HIGHER version in another family. Without it, deleting the family
		// filter from `pickCandidates` changed nothing and the test still
		// passed — the mutation survived, which is the twenty-second check that
		// could not fail, caught before it shipped. `19` beats `18-alpine` on
		// version alone, so it can only lose on family.
		{Name: "19", FullSize: 500 << 20, LastPushed: pushed(1)},
	}
}

func TestLookPicksTheHighestStableTagInTheFamily(t *testing.T) {
	lister := &countingTags{tags: postgresTags()}
	r := Look(context.Background(), lister, "postgres:16-alpine")

	if r.State != StateOK {
		t.Fatalf("state = %q (%s), want ok", r.State, r.Message)
	}
	if r.Repository != "library/postgres" || r.Tag != "16-alpine" {
		t.Fatalf("repo/tag = %q/%q", r.Repository, r.Tag)
	}
	if r.Candidate == nil {
		t.Fatal("no candidate")
	}
	// 18-alpine is the highest stable one. `edge` and `20260805` and
	// `18rc1-alpine` must not win, and `17` is a different family.
	if r.Candidate.Tag != "18-alpine" {
		t.Errorf("candidate = %q, want 18-alpine", r.Candidate.Tag)
	}
	if r.Candidate.Reference != "postgres:18-alpine" {
		t.Errorf("reference = %q, want postgres:18-alpine", r.Candidate.Reference)
	}
	if r.Candidate.Kind != UpgradeMajor {
		t.Errorf("kind = %q, want major", r.Candidate.Kind)
	}
	// R6.4: the request filtered by family rather than trusting one page.
	if lister.filter != "alpine" {
		t.Errorf("filter = %q, want alpine", lister.filter)
	}
	// The alternatives carry the rest of the family, and never a nightly and
	// never another family — `19` exists and is higher, and must not be here.
	for _, alt := range r.Alternatives {
		if IsUnstable(alt.Tag) || IsDateTag(alt.Tag) {
			t.Errorf("alternative %q is unstable or a date stamp", alt.Tag)
		}
		if FamilySuffix(alt.Tag) != "-alpine" {
			t.Errorf("alternative %q is not in the -alpine family", alt.Tag)
		}
	}
}

// FOUND BY RUNNING THE TOOL AGAINST THE REAL API, not by unit test — the same
// way the date-stamp rule was found.
//
// `composure image lookup postgres:16-alpine` against live Docker Hub offered
// `postgres:18.4-alpine3.23`. Every rule held: same family, stable, not a date
// stamp, strictly higher version. It is still the wrong answer. The reader
// pinned to `16-alpine` — one version component, the plain `-alpine` variant —
// and was offered a tag that pins the PostgreSQL patch AND the Alpine minor,
// which is a different pinning policy, not an upgrade of theirs. The mockup's
// own pill is `node:22-alpine` beside `node:18-alpine`: same shape.
//
// So a candidate must also match the SHAPE of the tag it replaces: the same
// number of version components and the same text after them.
func TestCandidateKeepsTheReadersPinningPolicy(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "16-alpine", FullSize: 111 << 20, LastPushed: pushed(36)},
		{Name: "18-alpine", FullSize: 114 << 20, LastPushed: pushed(2)},
		{Name: "18.4-alpine3.23", FullSize: 114 << 20, LastPushed: pushed(1)},
		{Name: "18.4-alpine", FullSize: 114 << 20, LastPushed: pushed(1)},
		{Name: "17.10-alpine3.24", FullSize: 112 << 20, LastPushed: pushed(4)},
	}}
	r := Look(context.Background(), lister, "postgres:16-alpine")
	if r.Candidate == nil {
		t.Fatal("no candidate")
	}
	if r.Candidate.Tag != "18-alpine" {
		t.Fatalf("candidate = %q, want 18-alpine — the reader pinned one version "+
			"component and the plain -alpine variant, and that is the pin being upgraded",
			r.Candidate.Tag)
	}
	// The alternatives are the reader's own shape too. Offering
	// `18.4-alpine3.23` in the list is offering a different pinning policy
	// under the heading "also available".
	for _, alt := range r.Alternatives {
		if strings.Contains(alt.Tag, "alpine3.") {
			t.Errorf("alternative %q pins the Alpine minor; the current tag does not", alt.Tag)
		}
	}
}

// The shape rule must not become a reason to say "nothing is newer". When no
// candidate shares the reader's shape, the family rule still answers.
func TestShapeIsAPreferenceNotARequirement(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "16-alpine", FullSize: 111 << 20, LastPushed: pushed(400)},
		{Name: "17.2-alpine3.21", FullSize: 114 << 20, LastPushed: pushed(2)},
	}}
	r := Look(context.Background(), lister, "postgres:16-alpine")
	if r.Candidate == nil || r.Candidate.Tag != "17.2-alpine3.21" {
		t.Fatalf("candidate = %+v; with no same-shape tag the family rule still answers", r.Candidate)
	}
}

// The pill is the mockup's own string, composed once in Go so the CLI table,
// the JSON and the pane cannot word one fact three ways.
func TestPillReadsLikeTheMockup(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "18-alpine", FullSize: 60 << 20, LastPushed: pushed(2)},
		{Name: "22-alpine", FullSize: 20 << 20, LastPushed: pushed(1)},
	}}
	r := Look(context.Background(), lister, "node:18-alpine")
	if r.Pill != "node:22-alpine · major · 40MB smaller" {
		t.Fatalf("pill = %q", r.Pill)
	}
}

func TestPillSaysLargerWhenItIsLarger(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "1.0", FullSize: 10 << 20, LastPushed: pushed(2)},
		{Name: "1.1", FullSize: 22 << 20, LastPushed: pushed(1)},
	}}
	r := Look(context.Background(), lister, "acme/app:1.0")
	if !strings.HasSuffix(r.Pill, "· 12MB larger") {
		t.Fatalf("pill = %q, want a `larger` clause", r.Pill)
	}
}

// A pill that only ever says `smaller` is a pill nobody believes twice; a pill
// that guesses a size when there is none is worse. Both sizes unknown means no
// size clause at all.
func TestPillOmitsTheSizeClauseWhenThereIsNothingToCompare(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "1.0", LastPushed: pushed(2)},
		{Name: "1.1", LastPushed: pushed(1)},
	}}
	r := Look(context.Background(), lister, "acme/app:1.0")
	if r.Pill != "acme/app:1.1 · minor" {
		t.Fatalf("pill = %q", r.Pill)
	}
}

func TestAgeReadsInTheReadersWords(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "16-alpine", FullSize: 1 << 20, LastPushed: pushed(425)},
	}}
	r := Look(context.Background(), lister, "postgres:16-alpine")
	if r.Age != "14 months old" {
		t.Fatalf("age = %q, want `14 months old`", r.Age)
	}
	if r.AgeDays < 424 || r.AgeDays > 426 {
		t.Fatalf("age days = %d", r.AgeDays)
	}
}

// FOUND BY RUNNING THE FINISHED CLI against a real Dockerfile, like the two
// rules before it.
//
// `composure image stale` on `node:18-alpine` reported the upgrade and NO AGE at
// all — and the age is the headline of the whole feature, the mockup's own
// "base image 14 months old". The cause: the family page is the hundred most
// recently pushed `*alpine*` tags, and a tag old enough to be worth replacing
// is exactly the tag that is not on it. So the pin the reader actually has is
// the one thing the first page cannot describe.
//
// A second, exact-name request fixes it, and it is only made when the first
// page did not carry the tag.
type twoPageTags struct {
	page  []Tag
	exact map[string]Tag
	asked []string
}

// The API's `name` filter is a SUBSTRING match and the answer is ordered by
// last-updated, so an exact-name request answers with every tag containing that
// string, newest first — and the exact one is usually not the newest. This fake
// reproduces that, and it HONOURS pageSize, which is the whole point: the first
// version of this code asked for one result and got `1.27-alpine3.21-perl`.
func (t *twoPageTags) Tags(_ context.Context, _ string, pageSize int, filter string) ([]Tag, RateLimit, error) {
	t.asked = append(t.asked, filter)
	var out []Tag
	if tag, ok := t.exact[filter]; ok {
		// The decoys Docker Hub really returns, ahead of the exact match.
		out = []Tag{
			{Name: filter + "3.21-perl", LastPushed: pushed(1)},
			{Name: filter + "3.21", LastPushed: pushed(1)},
			{Name: filter + "-perl", LastPushed: pushed(1)},
			tag,
		}
	} else {
		out = t.page
	}
	if pageSize > 0 && len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, RateLimit{}, nil
}

func TestTheCurrentTagIsFetchedWhenTheFamilyPageDoesNotCarryIt(t *testing.T) {
	lister := &twoPageTags{
		// The newest hundred. `18-alpine` is not among them, which is the whole
		// point: it is two years old.
		page: []Tag{
			{Name: "26-alpine", FullSize: 60 << 20, LastPushed: pushed(2)},
			{Name: "24-alpine", FullSize: 62 << 20, LastPushed: pushed(30)},
		},
		exact: map[string]Tag{
			"18-alpine": {Name: "18-alpine", FullSize: 110 << 20, LastPushed: pushed(700)},
		},
	}
	r := Look(context.Background(), lister, "node:18-alpine")
	if r.Age != "23 months old" {
		t.Fatalf("age = %q, want the age of the tag the file actually pins", r.Age)
	}
	if r.CurrentSize != 110<<20 {
		t.Errorf("current size = %d", r.CurrentSize)
	}
	// …and the size delta is now real rather than absent.
	if r.Candidate == nil || !r.Candidate.HasSize {
		t.Fatalf("candidate = %+v; the size comparison needs the current tag's size", r.Candidate)
	}
	if !strings.Contains(r.Pill, "smaller") {
		t.Errorf("pill = %q", r.Pill)
	}
	if len(lister.asked) != 2 {
		t.Errorf("asked %v; the second request is the exact tag", lister.asked)
	}
}

// The second request is only made when it is needed. It costs a share of a rate
// limit shared by an office.
func TestNoSecondRequestWhenTheFamilyPageAlreadyCarriesTheTag(t *testing.T) {
	lister := &twoPageTags{
		page: []Tag{
			{Name: "18-alpine", FullSize: 110 << 20, LastPushed: pushed(700)},
			{Name: "26-alpine", FullSize: 60 << 20, LastPushed: pushed(2)},
		},
		exact: map[string]Tag{},
	}
	r := Look(context.Background(), lister, "node:18-alpine")
	if r.Age == "" {
		t.Fatal("no age")
	}
	if len(lister.asked) != 1 {
		t.Errorf("asked %v; the page already had the tag", lister.asked)
	}
}

func TestNoNewerTagIsItsOwnState(t *testing.T) {
	lister := &countingTags{tags: []Tag{
		{Name: "17-alpine", FullSize: 1 << 20, LastPushed: pushed(2)},
		{Name: "edge", LastPushed: pushed(0)},
	}}
	r := Look(context.Background(), lister, "postgres:17-alpine")
	if r.State != StateCurrent {
		t.Fatalf("state = %q, want current", r.State)
	}
	if r.Candidate != nil {
		t.Fatalf("candidate = %+v, want none", r.Candidate)
	}
	if r.Message == "" {
		t.Fatal("current state carries no sentence")
	}
}

// Rate limiting is a first-class state with its own sentence — never an error
// string, and never the same words as offline. The limit is per IP, so the
// reader who hits it is most likely behind a NAT and has done nothing wrong.
func TestRateLimitedIsAStateWithItsOwnSentence(t *testing.T) {
	lister := &countingTags{err: ErrRateLimited}
	r := Look(context.Background(), lister, "postgres:16-alpine")
	if r.State != StateRateLimited {
		t.Fatalf("state = %q, want rate-limited", r.State)
	}
	if !strings.Contains(r.Message, "rate") {
		t.Fatalf("message = %q", r.Message)
	}
	offline := Look(context.Background(), &countingTags{err: ErrOffline}, "postgres:16-alpine")
	if offline.Message == r.Message {
		t.Fatal("offline and rate-limited say the same thing; they are different facts")
	}
	// Nothing a reader sees carries a transport word.
	for _, banned := range []string{"ECONNREFUSED", "dial tcp", "429", "error"} {
		if strings.Contains(offline.Message, banned) || strings.Contains(r.Message, banned) {
			t.Errorf("a reader-facing sentence contains %q", banned)
		}
	}
}

func TestOfflineIsAStateWithItsOwnSentence(t *testing.T) {
	r := Look(context.Background(), &countingTags{err: ErrOffline}, "postgres:16-alpine")
	if r.State != StateOffline {
		t.Fatalf("state = %q, want offline", r.State)
	}
	if r.Message == "" {
		t.Fatal("offline carries no sentence")
	}
}

func TestNotFoundIsAStateWithItsOwnSentence(t *testing.T) {
	r := Look(context.Background(), &countingTags{err: ErrNotFound}, "acme/nope:1")
	if r.State != StateNotFound {
		t.Fatalf("state = %q, want not-found", r.State)
	}
	if !strings.Contains(r.Message, "acme/nope") {
		t.Fatalf("message does not name the repository: %q", r.Message)
	}
}

// Cross-registry search is out of scope (requirements §3). A ghcr.io reference
// gets a sentence saying so — never an empty answer, which reads as "there is
// nothing newer" in the place a reader decides whether to upgrade.
func TestAnotherRegistryIsRefusedByName(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/foo/bar:1.2",
		"quay.io/prometheus/node-exporter:v1.8.2",
		"localhost:5000/app:dev",
		"registry.example.com:5000/team/app:1",
	} {
		lister := &countingTags{tags: postgresTags()}
		r := Look(context.Background(), lister, ref)
		if r.State != StateOtherRegistry {
			t.Errorf("%s: state = %q, want other-registry", ref, r.State)
		}
		if lister.calls.Load() != 0 {
			t.Errorf("%s: asked Docker Hub about another registry's image", ref)
		}
		if !strings.Contains(r.Message, "Docker Hub") {
			t.Errorf("%s: message = %q", ref, r.Message)
		}
	}
}

// docker.io and index.docker.io ARE Docker Hub, spelled out. Refusing them
// would refuse the very images the feature is for.
func TestExplicitDockerHubHostsAreDockerHub(t *testing.T) {
	for ref, want := range map[string]string{
		"docker.io/library/postgres:16-alpine": "library/postgres",
		"index.docker.io/bitnami/redis:7":      "bitnami/redis",
		"postgres:16-alpine":                   "library/postgres",
		"bitnami/redis:7":                      "bitnami/redis",
	} {
		lister := &countingTags{}
		r := Look(context.Background(), lister, ref)
		if r.State == StateOtherRegistry {
			t.Errorf("%s: refused as another registry", ref)
		}
		if r.Repository != want {
			t.Errorf("%s: repository = %q, want %q", ref, r.Repository, want)
		}
	}
}

func TestNotComparableNamesWhichOfTheThreeItIs(t *testing.T) {
	cases := map[string]string{
		"${REGISTRY}/app:${TAG}": "variable",
		"scratch":                "scratch",
		"postgres@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": "digest",
	}
	for ref, word := range cases {
		lister := &countingTags{tags: postgresTags()}
		r := Look(context.Background(), lister, ref)
		if r.State != StateNotComparable {
			t.Errorf("%s: state = %q, want not-comparable", ref, r.State)
		}
		if lister.calls.Load() != 0 {
			t.Errorf("%s: looked up something with no tag to compare", ref)
		}
		if !strings.Contains(strings.ToLower(r.Message), word) {
			t.Errorf("%s: message %q does not say which of the three it is", ref, r.Message)
		}
	}
}

// The switch a reader can throw, and the thing that makes "no socket was
// opened" a MEASURED claim rather than an absence.
func TestOfflineSwitchOpensNoSocket(t *testing.T) {
	t.Setenv("COMPOSURE_OFFLINE", "1")
	lister := &countingTags{tags: postgresTags()}
	r := Look(context.Background(), Guarded(lister), "postgres:16-alpine")
	if r.State != StateDisabled {
		t.Fatalf("state = %q, want disabled", r.State)
	}
	if lister.calls.Load() != 0 {
		t.Fatalf("the transport was entered %d time(s) with COMPOSURE_OFFLINE=1", lister.calls.Load())
	}
	if !strings.Contains(r.Message, "off") {
		t.Fatalf("message = %q; it must not look like being offline", r.Message)
	}
}

// A cancelled context is not an error state to render; it is a question nobody
// is waiting for the answer to any more.
func TestCancelledLookupSaysSo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Look(ctx, &countingTags{err: context.Canceled}, "postgres:16-alpine")
	if r.State != StateCancelled {
		t.Fatalf("state = %q, want cancelled", r.State)
	}
}

/* ---- the transport, against a loopback server and nothing else ---------- */

func TestClientMapsStatusCodesToSentinels(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusTooManyRequests: ErrRateLimited,
		http.StatusNotFound:        ErrNotFound,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		c := New()
		c.TagsURL = srv.URL + "/v2/repositories/%s/tags/"
		_, _, err := c.TagsContext(context.Background(), "library/postgres", 10, "")
		if !errors.Is(err, want) {
			t.Errorf("status %d gave %v, want %v", status, err, want)
		}
		srv.Close()
	}
}

func TestClientMapsATransportFailureToOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now
	c := New()
	c.TagsURL = url + "/v2/repositories/%s/tags/"
	_, _, err := c.TagsContext(context.Background(), "library/postgres", 10, "")
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("err = %v, want ErrOffline", err)
	}
}

// A CANCELLED request is not an offline machine, and the distinction lives in
// the transport rather than in Look — so it has to be exercised there.
//
// The first version of this suite only fed `context.Canceled` to a fake, which
// tested `describeFailure` and left `getJSON`'s branch untested: replacing it
// with the offline path was a mutation that SURVIVED. Telling a reader they are
// offline because they clicked another service is a confident wrong answer
// about their machine.
func TestACancelledRequestIsNotReportedAsOffline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(release); srv.Close() }()

	c := New()
	c.TagsURL = srv.URL + "/v2/repositories/%s/tags/"
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, _, err := c.TagsContext(ctx, "library/postgres", 10, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrOffline) {
		t.Fatal("a cancelled request was reported as an unreachable Docker Hub")
	}
}

func TestTagsCarryTheRateLimitHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-ratelimit-limit", "180")
		w.Header().Set("x-ratelimit-remaining", "12")
		_ = json.NewEncoder(w).Encode(tagsResponse{Results: []Tag{{Name: "1.0"}}})
	}))
	defer srv.Close()
	c := New()
	c.TagsURL = srv.URL + "/v2/repositories/%s/tags/"
	ts, rl, err := c.TagsContext(context.Background(), "library/postgres", 10, "")
	if err != nil || len(ts) != 1 {
		t.Fatalf("tags = %v, err = %v", ts, err)
	}
	if rl.Limit != "180" || rl.Remaining != "12" {
		t.Fatalf("rate limit = %+v", rl)
	}
}

// R6.8: cache aggressively. Two lookups of the same repository are one request.
func TestCacheAnswersTheSecondAskWithoutAsking(t *testing.T) {
	lister := &countingTags{tags: postgresTags()}
	cache := NewCache(lister, time.Minute)
	for i := 0; i < 3; i++ {
		if r := Look(context.Background(), cache, "postgres:16-alpine"); r.State != StateOK {
			t.Fatalf("state = %q", r.State)
		}
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("the cache made %d requests for three identical questions", got)
	}
}

// A failure must not be cached: the next question is asked again, because the
// reader's network came back and the pane should notice.
func TestCacheDoesNotRememberAFailure(t *testing.T) {
	lister := &countingTags{err: ErrOffline}
	cache := NewCache(lister, time.Minute)
	Look(context.Background(), cache, "postgres:16-alpine")
	Look(context.Background(), cache, "postgres:16-alpine")
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("a failed lookup was cached: %d call(s) for two questions", got)
	}
}

func TestCacheExpires(t *testing.T) {
	lister := &countingTags{tags: postgresTags()}
	cache := NewCache(lister, -time.Second) // already stale on arrival
	Look(context.Background(), cache, "postgres:16-alpine")
	Look(context.Background(), cache, "postgres:16-alpine")
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("a stale entry was served: %d call(s)", got)
	}
}

// R6.7, asserted rather than remembered. The public API carries no vulnerability
// data, so nothing in this package may imply that it does.
func TestNothingPromisesAVulnerabilityFacet(t *testing.T) {
	r := Look(context.Background(), &countingTags{tags: postgresTags()}, "postgres:16-alpine")
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"cve", "vulnerab", "security", "scout"} {
		if strings.Contains(strings.ToLower(string(blob)), word) {
			t.Errorf("the report mentions %q; R6.7 forbids a facet this API cannot deliver", word)
		}
	}
}
