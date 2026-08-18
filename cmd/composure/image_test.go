package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elzouhery/composure/internal/hub"
)

// NOT ONE OF THESE TESTS OPENS A SOCKET TO DOCKER HUB.
//
// `imageClient` is a package variable holding the three endpoints, and every
// test here points it at an httptest.Server on loopback. The offline test goes
// further and asserts a HANDLER COUNTER is zero, so "no request was made" is
// measured rather than assumed.

// hubStub is a Docker Hub shaped enough for the two endpoints this command
// uses, plus a counter so a test can assert it was never asked.
type hubStub struct {
	server *httptest.Server
	calls  int
	status int
}

func newHubStub(t *testing.T, tags []hub.Tag, repos int) *hubStub {
	t.Helper()
	s := &hubStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		w.Header().Set("x-ratelimit-limit", "180")
		w.Header().Set("x-ratelimit-remaining", "177")
		if s.status != 0 {
			w.WriteHeader(s.status)
			return
		}
		if strings.Contains(r.URL.Path, "/search/") {
			out := map[string]any{"total": repos, "results": []map[string]any{}}
			list := []map[string]any{}
			for i := 0; i < repos; i++ {
				list = append(list, map[string]any{
					"id":                "library/postgres",
					"name":              "postgres",
					"short_description": "The PostgreSQL object-relational database system",
					"badge":             "official",
					"star_count":        14000,
					"pull_count":        "1B+",
					"architectures":     []map[string]string{{"name": "amd64"}, {"name": "arm64"}},
				})
			}
			out["results"] = list
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": tags})
	}))
	t.Cleanup(s.server.Close)

	prev, prevCache := imageClient, imageCache
	t.Cleanup(func() { imageClient, imageCache = prev, prevCache })
	// A fresh cache per test. The cache is process-wide by design (R6.8), so
	// without this the second test in the file is answered from the first
	// test's tag list and never reaches the stub at all.
	imageCache = hub.NewCache(clientLister{}, imageCacheTTL)
	c := hub.New()
	c.SearchURL = s.server.URL + "/api/search/v4/"
	c.LegacySearchURL = s.server.URL + "/v1/search"
	c.TagsURL = s.server.URL + "/v2/repositories/%s/tags/"
	imageClient = c
	return s
}

// runCLI drives the subcommand IN PROCESS, so a test can point the client at a
// loopback server. `cli_test.go`'s runCLI spawns the built binary, which is the
// right shape for argument dispatch and the wrong one here: a subprocess cannot
// be told which endpoint to talk to without an environment variable that would
// then exist in production for no other reason.
//
// Dispatch is still covered — `TestImageSubcommandIsReachableFromMain` runs the
// real binary, because deleting `case "image"` from main.go would otherwise
// leave every test in this file green.
func runImageCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = runImage(args[1:]) })
	return out, code
}

func ago(days int) time.Time { return time.Now().Add(-time.Duration(days) * 24 * time.Hour) }

func fixtureTags() []hub.Tag {
	return []hub.Tag{
		{Name: "16-alpine", FullSize: 280 << 20, LastPushed: ago(425)},
		{Name: "17-alpine", FullSize: 240 << 20, LastPushed: ago(3)},
		{Name: "edge", LastPushed: ago(0)},
	}
}

func TestImageLookupJSONCarriesTheWholeReport(t *testing.T) {
	newHubStub(t, fixtureTags(), 0)
	out, code := runImageCLI(t, "image", "lookup", "-json", "postgres:16-alpine")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var r hub.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if r.State != hub.StateOK {
		t.Fatalf("state = %q (%s)", r.State, r.Message)
	}
	if r.Candidate == nil || r.Candidate.Tag != "17-alpine" {
		t.Fatalf("candidate = %+v", r.Candidate)
	}
	if r.Pill != "postgres:17-alpine · major · 40MB smaller" {
		t.Fatalf("pill = %q", r.Pill)
	}
	if r.Age != "14 months old" {
		t.Fatalf("age = %q", r.Age)
	}
}

// The table is not a lesser answer. A capability visible only as JSON is one the
// reader checking by hand cannot see.
func TestImageLookupTableNamesTheSameFacts(t *testing.T) {
	newHubStub(t, fixtureTags(), 0)
	out, code := runImageCLI(t, "image", "lookup", "postgres:16-alpine")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"postgres:17-alpine", "major", "14 months old", "smaller"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table does not say %q:\n%s", want, out)
		}
	}
}

// Being offline is not a script failure. A CI job that runs this must not go red
// because a runner has no egress.
func TestImageLookupExitsZeroWhenTheStateIsNotOK(t *testing.T) {
	s := newHubStub(t, nil, 0)
	s.status = http.StatusTooManyRequests
	out, code := runImageCLI(t, "image", "lookup", "-json", "postgres:16-alpine")
	if code != 0 {
		t.Fatalf("exit %d, want 0: %s", code, out)
	}
	var r hub.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != hub.StateRateLimited {
		t.Fatalf("state = %q", r.State)
	}
	if strings.Contains(r.Message, "429") {
		t.Errorf("a reader-facing sentence carries a status code: %q", r.Message)
	}
}

// COMPOSURE_OFFLINE=1 is the switch, and this is the check that proves the code
// path opened nothing: a counter on the handler, asserted at zero.
func TestImageLookupOffMakesNoRequestAtAll(t *testing.T) {
	s := newHubStub(t, fixtureTags(), 0)
	t.Setenv("COMPOSURE_OFFLINE", "1")
	out, code := runImageCLI(t, "image", "lookup", "-json", "postgres:16-alpine")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var r hub.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != hub.StateDisabled {
		t.Fatalf("state = %q, want disabled", r.State)
	}
	if s.calls != 0 {
		t.Fatalf("COMPOSURE_OFFLINE=1 still made %d request(s)", s.calls)
	}
}

func TestImageLookupRefusesAnotherRegistryWithoutAsking(t *testing.T) {
	s := newHubStub(t, fixtureTags(), 0)
	out, _ := runImageCLI(t, "image", "lookup", "-json", "ghcr.io/foo/bar:1.2")
	var r hub.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != hub.StateOtherRegistry {
		t.Fatalf("state = %q", r.State)
	}
	if s.calls != 0 {
		t.Fatalf("asked Docker Hub about a ghcr.io image %d time(s)", s.calls)
	}
}

func TestImageSearchOverBothShapes(t *testing.T) {
	newHubStub(t, nil, 3)
	out, code := runImageCLI(t, "image", "search", "-json", "postgres")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var res imageSearchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if res.State != hub.StateOK || len(res.Results) != 3 {
		t.Fatalf("state = %q, %d result(s)", res.State, len(res.Results))
	}
	// R6.1's fields, all of them, by name.
	first := res.Results[0]
	if first.Name != "postgres" || first.Badge != "official" || first.Stars == 0 ||
		first.PullsDisplay == "" || first.Description == "" || len(first.Architectures) == 0 {
		t.Fatalf("a result is missing one of R6.1's fields: %+v", first)
	}

	table, code := runImageCLI(t, "image", "search", "postgres")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, table)
	}
	if !strings.Contains(table, "postgres") || !strings.Contains(table, "official") {
		t.Errorf("the table does not name the result:\n%s", table)
	}
}

func TestImageSearchRateLimitedIsAStateNotAFailure(t *testing.T) {
	s := newHubStub(t, nil, 0)
	s.status = http.StatusTooManyRequests
	out, code := runImageCLI(t, "image", "search", "-json", "postgres")
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	var res imageSearchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.State != hub.StateRateLimited {
		t.Fatalf("state = %q", res.State)
	}
	if res.Message == "" {
		t.Fatal("no sentence")
	}
}

// R6.5, and the mockup's header: the file's age is the OLDEST stage's base, not
// the first stage's.
func TestImageStaleReportsEveryStageAndTheOldestBase(t *testing.T) {
	newHubStub(t, []hub.Tag{
		{Name: "16-alpine", FullSize: 280 << 20, LastPushed: ago(425)},
		{Name: "17-alpine", FullSize: 240 << 20, LastPushed: ago(3)},
		{Name: "1.27-alpine", FullSize: 50 << 20, LastPushed: ago(9)},
	}, 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	// Stage 0's base is three days old; stage 1's is 425 days old. The header
	// has to report the OLDER of the two, which is not the first.
	body := "FROM nginx:1.27-alpine AS serve\nRUN true\n\nFROM postgres:16-alpine\nRUN true\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runImageCLI(t, "image", "stale", "-json", path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var res imageStaleResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(res.Stages) != 2 {
		t.Fatalf("%d stage(s)", len(res.Stages))
	}
	if res.Stages[0].Name != "serve" {
		t.Errorf("stage 0 name = %q", res.Stages[0].Name)
	}
	if res.OldestBase != "14 months old" {
		t.Errorf("oldest base = %q, want the OLDER of the two stages", res.OldestBase)
	}
	if res.Stages[1].Report.Candidate == nil {
		t.Errorf("stage 1 has no candidate")
	}
}

func TestImageStaleSkipsWhatCannotBeCompared(t *testing.T) {
	s := newHubStub(t, fixtureTags(), 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	body := "ARG BASE=alpine\nFROM ${BASE}:3.20 AS a\nFROM scratch\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := runImageCLI(t, "image", "stale", "-json", path)
	var res imageStaleResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	for _, st := range res.Stages {
		if st.Report.State != hub.StateNotComparable {
			t.Errorf("stage %d state = %q, want not-comparable", st.Index, st.Report.State)
		}
	}
	if s.calls != 0 {
		t.Fatalf("looked up something with no tag to compare: %d request(s)", s.calls)
	}
}

// R6.7, asserted at the CLI boundary too: this surface must never grow a
// vulnerability facet, because the public API cannot deliver one.
func TestNoSurfacePromisesAVulnerabilityFacet(t *testing.T) {
	newHubStub(t, fixtureTags(), 2)
	for _, args := range [][]string{
		{"image", "lookup", "postgres:16-alpine"},
		{"image", "search", "postgres"},
	} {
		out, _ := runImageCLI(t, args...)
		for _, word := range []string{"cve", "vulnerab", "security"} {
			if strings.Contains(strings.ToLower(out), word) {
				t.Errorf("%v mentions %q", args, word)
			}
		}
	}
	if !strings.Contains(usageText, "image lookup") {
		t.Error("`composure image` is not in the usage text; a capability nobody can find is not shipped")
	}
}

// The dispatch, through the real binary. Deleting `case "image"` from main.go
// removes the subcommand from the product and leaves every other test in this
// file green, because they all call runImage directly.
//
// It runs with COMPOSURE_OFFLINE=1 so it cannot reach Docker Hub: the assertion is
// that the subcommand EXISTS and answers, which the `disabled` state proves as
// well as any other.
func TestImageSubcommandIsReachableFromMain(t *testing.T) {
	t.Setenv("COMPOSURE_OFFLINE", "1")
	cmd := exec.Command(composureBin, "image", "lookup", "-json", "postgres:16-alpine")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "COMPOSURE_OFFLINE=1"}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("`composure image lookup` failed: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	var r hub.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("`composure image lookup` did not print a report: %v\n%s%s", err, out.String(), errOut.String())
	}
	if r.State != hub.StateDisabled {
		t.Fatalf("state = %q", r.State)
	}
}
