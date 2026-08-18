package diagnose

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// run diagnoses a compose document held in memory.
//
// The environment is empty and EnvKnown is true, so the undefined-variable rule
// is live in every test rather than accidentally silenced by whatever happens
// to be exported in the shell that runs the suite. A test that needs a defined
// variable passes one in.
type testOpts struct {
	profiles []string
	env      map[string]string
	envKnown bool
}

func withProfiles(names ...string) func(*testOpts) {
	return func(o *testOpts) { o.profiles = names }
}

func withEnv(kv map[string]string) func(*testOpts) {
	return func(o *testOpts) { o.env = kv }
}

// withoutEnv models the case where the environment could not be established.
func withoutEnv() func(*testOpts) {
	return func(o *testOpts) { o.envKnown = false }
}

func run(t *testing.T, src string, options ...func(*testOpts)) *Report {
	t.Helper()
	const name = "compose.yaml"
	o := testOpts{env: map[string]string{}, envKnown: true}
	for _, apply := range options {
		apply(&o)
	}
	// Hermetic by construction: the resolver interpolates at load (story 1.3),
	// so the environment has to be stated HERE or the suite would pass or fail
	// on whatever the developer happens to have exported.
	project, err := resolve.BytesWith(name, []byte(src), resolve.Options{Env: o.env, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	graph, err := topology.Build(project, o.profiles)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	all, err := topology.Build(project, declaredProfiles(project))
	if err != nil {
		t.Fatalf("topology (all profiles): %v", err)
	}
	rep, err := Run(Input{
		Path:        name,
		Project:     project,
		Graph:       graph,
		AllProfiles: all,
		Sources:     map[string][]byte{name: []byte(src)},
		Env:         o.env,
		EnvKnown:    o.envKnown,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep
}

// findingsFor returns the findings a single rule produced.
func findingsFor(rep *Report, rule string) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// wantNone is the negative half of every rule's test, and it is the half that
// decides whether the tool survives contact with a real repository. A
// diagnostic that cries wolf gets switched off in a week.
func wantNone(t *testing.T, rep *Report, rule string) {
	t.Helper()
	if got := findingsFor(rep, rule); len(got) != 0 {
		for _, f := range got {
			t.Errorf("unwanted %s finding: %s", rule, f.Message)
		}
	}
}

func wantCount(t *testing.T, rep *Report, rule string, n int) []Finding {
	t.Helper()
	got := findingsFor(rep, rule)
	if len(got) != n {
		t.Fatalf("%s produced %d findings, want %d: %s", rule, len(got), n, summarise(got))
	}
	return got
}

func summarise(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("\n  - " + f.Message)
	}
	if b.Len() == 0 {
		return " (none)"
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Story 3.1's framing criteria, which are about the shape of the run rather
// than any one rule.

func TestCleanStackIsNotAnError(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    ports:
      - "8080:80"
  api:
    image: api
`)
	if len(rep.Findings) != 0 {
		t.Fatalf("a clean stack produced findings: %s", summarise(rep.Findings))
	}
	if _, any := rep.Highest(); any {
		t.Error("Highest reports a severity on an empty report")
	}
}

// AD-7, checked over every rule at once rather than trusted rule by rule.
func TestEveryFindingIsAnchored(t *testing.T) {
	rep := run(t, adversarial)
	if len(rep.Findings) == 0 {
		t.Fatal("the adversarial fixture produced no findings; it is meant to trip most rules")
	}
	for _, f := range rep.Findings {
		if len(f.Anchors) == 0 {
			t.Errorf("%s: finding with no anchor: %s", f.Rule, f.Message)
			continue
		}
		for _, a := range f.Anchors {
			if a.Origin.IsZero() {
				t.Errorf("%s: anchor %q has no position", f.Rule, a.Label)
			}
			if len(a.Path) == 0 {
				t.Errorf("%s: anchor %q has no path", f.Rule, a.Label)
			}
		}
	}
}

// Story 3.8: a finding either carries a fix or says why it does not. Silence is
// not one of the options.
func TestEveryFindingExplainsItsFix(t *testing.T) {
	rep := run(t, adversarial)
	for _, f := range rep.Findings {
		if f.Fix == nil && strings.TrimSpace(f.NoFix) == "" {
			t.Errorf("%s: no fix and no reason: %s", f.Rule, f.Message)
		}
		if f.Fix != nil && f.NoFix != "" {
			t.Errorf("%s: carries both a fix and a no-fix reason", f.Rule)
		}
		if f.Fix == nil {
			continue
		}
		if f.Fix.File == "" {
			t.Errorf("%s: fix names no file", f.Rule)
		}
		if f.Fix.Range.End < f.Fix.Range.Start {
			t.Errorf("%s: fix range %d-%d runs backwards", f.Rule, f.Fix.Range.Start, f.Fix.Range.End)
		}
		if f.Fix.Operation != FixInsertKey && f.Fix.Range.End == f.Fix.Range.Start {
			t.Errorf("%s: %s fix has an empty range", f.Rule, f.Fix.Operation)
		}
	}
}

// Two runs over the same document must produce identical JSON, or the report
// cannot be diffed in CI.
func TestDeterministic(t *testing.T) {
	a, err := json.Marshal(run(t, adversarial))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(run(t, adversarial))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("two runs produced different JSON")
	}
}

// Findings are never errors (AD-13). Run refuses only when handed nothing.
func TestRunRefusesNothing(t *testing.T) {
	if _, err := Run(Input{}); err != ErrNoProject {
		t.Errorf("Run with no project returned %v, want ErrNoProject", err)
	}
	project, err := resolve.Bytes("compose.yaml", []byte("services:\n  web:\n    image: nginx\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Input{Project: project}); err != ErrNoGraph {
		t.Errorf("Run with no graph returned %v, want ErrNoGraph", err)
	}
}

func TestSeverityOrderAndJSON(t *testing.T) {
	if !(SeverityHint < SeverityWarning && SeverityWarning < SeverityError) {
		t.Fatal("severities do not order hint < warning < error")
	}
	b, err := json.Marshal(SeverityWarning)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"warning"` {
		t.Errorf("severity marshalled as %s, want \"warning\"", b)
	}
	var s Severity
	if err := json.Unmarshal([]byte(`"error"`), &s); err != nil || s != SeverityError {
		t.Errorf("round trip gave %v, %v", s, err)
	}
	if err := json.Unmarshal([]byte(`"nope"`), &s); err == nil {
		t.Error("an unknown severity name was accepted")
	}
}

func TestReportHighestIsTheWorst(t *testing.T) {
	rep := &Report{Findings: []Finding{
		{Severity: SeverityHint}, {Severity: SeverityError}, {Severity: SeverityWarning},
	}}
	if worst, any := rep.Highest(); !any || worst != SeverityError {
		t.Errorf("Highest = %v, %v", worst, any)
	}
}

// Every registered rule must be discoverable and uniquely named: the id is what
// a suppression list keys on.
func TestRuleIDsAreUniqueAndStable(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules() {
		if r.ID == "" || r.Title == "" {
			t.Errorf("rule %+v is missing an id or title", r)
		}
		if seen[r.ID] {
			t.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
	}
	// The seven of epic 3, plus story 5.2's obsolete-version-field hint, story
	// 6.3's build-dockerfile-missing warning, and merge-form-mismatch. The
	// count is asserted rather than the set so that a rule deleted by accident
	// — the failure mode where the report simply goes quiet — is caught.
	if len(seen) != 10 {
		t.Errorf("%d rules registered, want the 7 of epic 3 plus obsolete-version-field, build-dockerfile-missing and merge-form-mismatch", len(seen))
	}
	for _, id := range []string{"obsolete-version-field", "build-dockerfile-missing", "merge-form-mismatch"} {
		if !seen[id] {
			t.Errorf("the %s rule is not registered", id)
		}
	}
}

// adversarial trips most of the rules at once. It is the fixture the
// cross-cutting tests above run on, so a rule that emits an unanchored finding
// is caught even if its own test forgot to check.
const adversarial = `
services:
  gateway:
    image: nginx
    ports:
      - "8080:80"
    networks: [front]
    depends_on:
      api:
        condition: service_healthy
    environment:
      ADMIN_TOKEN: letmein
      SESSION: ${UNDEFINED_THING}

  api:
    image: api
    networks: [front]
    ports:
      - "8080:3000"

  loop-a:
    image: busybox
    networks: [front]
    depends_on: [loop-b]

  loop-b:
    image: busybox
    networks: [front]
    depends_on: [loop-a]

  hermit:
    image: busybox
    networks: [lonely]

networks:
  front:
  lonely:
  unused-net:

volumes:
  orphan:
`
