package diagnose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 9.5. The plaintext-credential rule offers `composure extract` as its own
// remedy, and DECISIONS.md 26 records why the field it needed did not exist.
//
// The trap this file is written against is named in the story: a fixture whose
// ONLY finding is the credential cannot distinguish a rule that offers the
// remedy for credentials from one that offers it for everything. So the fixture
// carries four findings from three rules, and the assertions are about which
// ones do NOT carry a remedy as much as which one does.

// reportFor runs the shipped rule set over a fixture on disk, the way Analyze
// does, so Sources is the real bytes and the fixture is a permanent regression
// file rather than a heredoc.
func reportFor(t *testing.T, fixture string) (*Report, string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "edge", fixture)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	project, err := resolve.BytesWith(path, src, resolve.Options{Env: map[string]string{}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	all, err := topology.Build(project, declaredProfiles(project))
	if err != nil {
		t.Fatalf("topology (all): %v", err)
	}
	rep, err := Run(Input{
		Path:        path,
		Project:     project,
		Graph:       graph,
		AllProfiles: all,
		Sources:     map[string][]byte{path: src},
		Env:         map[string]string{},
		EnvKnown:    true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep, path
}

// findingOn returns the one finding of rule whose first anchor path ends in
// suffix. It fails rather than returning a zero value: a test that silently
// asserts nothing about a finding it could not find is the shape of check this
// repository keeps shipping.
func findingOn(t *testing.T, rep *Report, rule, suffix string) Finding {
	t.Helper()
	var hits []Finding
	for _, f := range rep.Findings {
		if f.Rule != rule {
			continue
		}
		if strings.HasSuffix(f.Anchors[0].Path.String(), suffix) {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%s findings anchored at *%s: %d, want 1", rule, suffix, len(hits))
	}
	return hits[0]
}

func TestRemedyIsOfferedOnAMappingFormCredential(t *testing.T) {
	rep, path := reportFor(t, "e48-credential-remedy.yml")
	f := findingOn(t, rep, credRule, "environment.POSTGRES_PASSWORD")

	if f.Fix == nil {
		t.Fatalf("no fix at all: %s", f.NoFix)
	}
	r := f.Fix.Remedy
	if r == nil {
		t.Fatalf("the credential fix carries no remedy; the product knows how to finish this and does not say so")
	}
	if r.Operation != RemedyExtract {
		t.Errorf("remedy operation %q, want %q", r.Operation, RemedyExtract)
	}
	if r.Name != "POSTGRES_PASSWORD" {
		t.Errorf("remedy names variable %q, want POSTGRES_PASSWORD — the finding and the remedy have to be one address", r.Name)
	}
	if want := filepath.Join(filepath.Dir(path), ".env"); r.File != want {
		t.Errorf("remedy writes %q, want %q — the .env beside the compose file is the only one Compose interpolates from", r.File, want)
	}
	if r.At != f.Fix.Path.String() {
		t.Errorf("remedy is at %q and the fix is at %q; they have to be the same address", r.At, f.Fix.Path)
	}
	// The command has to be the one that actually performs it, `-name` included,
	// so the variable the finding advertises and the variable extract derives
	// cannot drift apart.
	for _, want := range []string{"composure extract", "-at " + r.At, "-name POSTGRES_PASSWORD", "-write"} {
		if !strings.Contains(r.Command, want) {
			t.Errorf("remedy command %q does not contain %q", r.Command, want)
		}
	}
	// DECISIONS.md 17: offering stages nothing. A remedy that named a byte range
	// would be claiming an edit nobody has checked; it has no field for one.
	if !strings.Contains(r.Describe, "Nothing is written until you run it") {
		t.Errorf("the remedy does not say that nothing is written yet: %q", r.Describe)
	}
}

func TestRemedyOnTheListFormNamesTheKeyNotTheIndex(t *testing.T) {
	rep, _ := reportFor(t, "e48-credential-remedy.yml")
	f := findingOn(t, rep, credRule, "environment[0]")
	if f.Fix == nil || f.Fix.Remedy == nil {
		t.Fatalf("the list-form credential carries no remedy (fix=%v, noFix=%q)", f.Fix, f.NoFix)
	}
	if f.Fix.Remedy.Name != "API_TOKEN" {
		t.Fatalf("remedy names %q, want API_TOKEN — the name lives inside the scalar, not in the index that addresses it", f.Fix.Remedy.Name)
	}
	// And it must agree with the fix's own replacement, which keeps the key.
	if !strings.Contains(f.Fix.Value, "API_TOKEN=${API_TOKEN}") {
		t.Fatalf("fix writes %q; `- ${API_TOKEN}` is an entry with no `=`, which Compose reads as pass-through-by-name", f.Fix.Value)
	}
}

// The trap, asserted directly: every other finding in the same report carries
// no remedy. A rule that offers it for everything passes every check above and
// fails this one.
func TestNoOtherRuleOffersTheRemedy(t *testing.T) {
	rep, _ := reportFor(t, "e48-credential-remedy.yml")
	others := 0
	for _, f := range rep.Findings {
		if f.Rule == credRule {
			continue
		}
		others++
		if f.Fix != nil && f.Fix.Remedy != nil {
			t.Errorf("rule %q offers the extract remedy: %s", f.Rule, f.Message)
		}
	}
	if others < 2 {
		t.Fatalf("the fixture produced %d non-credential findings; with fewer than two this test cannot fail "+
			"and is one of the checks that could not fail", others)
	}
}

// A value `composure extract` would refuse is not offered. The finding and its
// replace_scalar fix are untouched: the remedy is an addition, never a
// replacement for the advice that was already there.
func TestNoRemedyForAValueTheEnvCannotHold(t *testing.T) {
	rep, _ := reportFor(t, "e48-credential-remedy.yml")
	f := findingOn(t, rep, credRule, "environment.SERVICE_SECRET")
	if f.Fix == nil {
		t.Fatalf("the awkward credential lost its fix entirely: %s", f.NoFix)
	}
	if f.Fix.Operation != FixReplaceScalar || f.Fix.Value == "" {
		t.Errorf("the replace_scalar fix was disturbed: %+v", f.Fix)
	}
	if f.Fix.Remedy != nil {
		t.Fatalf("a remedy was offered for a value that cannot be written as one .env line; "+
			"`composure extract` would refuse it, and an offer that refuses is worse than no offer: %+v", f.Fix.Remedy)
	}
}

// unusableRemedy's enumeration, tested directly.
//
// This is the PREDICATE and nothing else. It was called TestRunDropsAMalformedRemedy
// and it never called Run, so the two acceptance criteria that are about Run —
// that the guard is enforced there by construction rather than trusted per
// rule, and that the remedy is dropped ALONE — had no subject at all. Both
// mutations survived it. They are covered by the test below; this one keeps its
// real job, which is the shape-by-shape enumeration.
func TestUnusableRemedyRejectsEveryMalformedShape(t *testing.T) {
	base := func() *Fix {
		return &Fix{
			Operation: FixReplaceScalar,
			Path:      resolve.Path{"services", "a", "environment", "X"},
			Value:     "${X}",
			Remedy:    &Remedy{Operation: RemedyExtract, Name: "X", File: ".env", At: "services.a.environment.X", Command: "composure extract"},
		}
	}
	cases := []struct {
		name string
		on   func(*Fix)
	}{
		{"no name", func(f *Fix) { f.Remedy.Name = "" }},
		{"no file", func(f *Fix) { f.Remedy.File = "" }},
		{"no address", func(f *Fix) { f.Remedy.At = "" }},
		{"no command", func(f *Fix) { f.Remedy.Command = "" }},
		{"unknown operation", func(f *Fix) { f.Remedy.Operation = "rm -rf" }},
		{"not a replace_scalar", func(f *Fix) { f.Operation = FixDeleteKey }},
		{"a different address from the fix", func(f *Fix) { f.Remedy.At = "services.b.image" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.on(f)
			if why := unusableRemedy(f); why == "" {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
	if why := unusableRemedy(base()); why != "" {
		t.Fatalf("a well-formed remedy was rejected: %s", why)
	}
}

// ...and now the two criteria that are about Run rather than about the
// predicate, driven THROUGH Run on a real project.
//
// No shipped rule can produce a malformed remedy — the credential rule is the
// only one that offers any, and extractRemedy self-censors — so the only way to
// reach this guard is a rule that must never ship, which is what guards_test.go's
// fakeRule and withOnlyRule exist for. Every real rule self-censoring is exactly
// why this went untested: the guard works because nobody has needed it yet, and
// the next rule is the one that finds out.
//
// The two mutations this is written to kill, both of which the suite survived:
//
//	gating the call site on a rule id that never matches — the remedy ships
//	malformed, so `Remedy != nil` after Run has to be the assertion;
//
//	`f.Fix.Remedy, f.NoRemedy = nil, why` written as `f.Fix, f.NoFix = nil, why`
//	— the correct replace_scalar goes with it, so `Fix != nil` AND its operation
//	and value have to be asserted, not just "a fix survived".
func TestRunDropsAMalformedRemedy(t *testing.T) {
	// Bytes 22-27 are on line 3 of runRulesSrc, which is what unownedFix
	// checks: this fix has to get PAST the two earlier guards or the test would
	// pass for the wrong reason.
	base := func() *Fix {
		return &Fix{
			Operation: FixReplaceScalar,
			File:      "compose.yaml",
			Path:      resolve.Path{"services", "web", "image"},
			Range:     ByteRange{Start: 22, End: 27, Line: 3},
			Value:     "nginx:1.27",
			Describe:  "pin the image",
			Remedy: &Remedy{
				Operation: RemedyExtract,
				Name:      "WEB_IMAGE",
				File:      ".env",
				At:        "services.web.image",
				Command:   "composure extract -at services.web.image -name WEB_IMAGE -write compose.yaml",
			},
		}
	}
	run := func(t *testing.T, f *Fix) Finding {
		t.Helper()
		withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
			{Message: "carries a remedy", Anchors: anchored(), Fix: f},
		}})
		rep := runRules(t)
		if len(rep.Findings) != 1 {
			t.Fatalf("%d findings, want 1", len(rep.Findings))
		}
		return rep.Findings[0]
	}

	// The control first: a well-formed remedy on a well-formed fix survives Run
	// untouched. Without it, an implementation that drops EVERY remedy passes
	// every case below.
	t.Run("a well-formed remedy survives Run", func(t *testing.T) {
		got := run(t, base())
		if got.Fix == nil {
			t.Fatalf("the fix was dropped: %s", got.NoFix)
		}
		if got.Fix.Remedy == nil {
			t.Fatalf("a well-formed remedy was dropped by Run: %s", got.NoRemedy)
		}
		if got.NoRemedy != "" {
			t.Errorf("NoRemedy was set on a remedy that was kept: %q", got.NoRemedy)
		}
	})

	for _, tc := range []struct {
		name string
		on   func(*Fix)
	}{
		{"no name", func(f *Fix) { f.Remedy.Name = "" }},
		{"no file", func(f *Fix) { f.Remedy.File = "" }},
		{"no address", func(f *Fix) { f.Remedy.At = "" }},
		{"no command", func(f *Fix) { f.Remedy.Command = "" }},
		{"unknown operation", func(f *Fix) { f.Remedy.Operation = "rm -rf" }},
		{"a different address from the fix", func(f *Fix) { f.Remedy.At = "services.b.image" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.on(f)
			got := run(t, f)

			// Criterion one: enforced in Run, by construction. A rule got this
			// past its own author and Run took it away regardless.
			if got.Fix != nil && got.Fix.Remedy != nil {
				t.Fatalf("Run shipped a malformed remedy: %+v", got.Fix.Remedy)
			}
			// Criterion two: dropped ALONE. The splice half is still correct
			// advice and taking it away because its companion was malformed
			// would make the report worse than before the field existed.
			if got.Fix == nil {
				t.Fatalf("the correct replace_scalar went with the remedy; NoFix says %q", got.NoFix)
			}
			if got.Fix.Operation != FixReplaceScalar || got.Fix.Value != "nginx:1.27" {
				t.Errorf("the surviving fix was disturbed: %+v", got.Fix)
			}
			if got.NoFix != "" {
				t.Errorf("a fix that survived was given a NoFix reason: %q", got.NoFix)
			}
			// ...and the drop says why. Rule 6: a guard that silently discards
			// is a guard nobody can debug.
			if got.NoRemedy == "" {
				t.Error("the remedy vanished with no reason on the finding")
			}
			if !strings.Contains(got.NoRemedy, "no remedy is described") {
				t.Errorf("NoRemedy is not the documented reason: %q", got.NoRemedy)
			}
		})
	}

	// The `not a replace_scalar` shape needs its own fix entirely — a
	// delete_key over a range it actually owns — because the guard's subject
	// there is the FIX's operation, not the remedy's fields. Here the remedy is
	// dropped and the delete_key survives, which is the same criterion two.
	t.Run("not a replace_scalar", func(t *testing.T) {
		got := run(t, &Fix{
			Operation: FixDeleteKey,
			File:      "compose.yaml",
			Path:      resolve.Path{"services", "web"},
			Range:     ByteRange{Start: 10, End: len(runRulesSrc), Line: 2},
			Describe:  "delete the service",
			Remedy: &Remedy{
				Operation: RemedyExtract, Name: "WEB_IMAGE", File: ".env",
				At: "services.web", Command: "composure extract",
			},
		})
		if got.Fix == nil {
			t.Fatalf("the delete_key went with the remedy: %s", got.NoFix)
		}
		if got.Fix.Remedy != nil {
			t.Fatalf("a remedy was offered on a %s: %+v", got.Fix.Operation, got.Fix.Remedy)
		}
		if !strings.Contains(got.NoRemedy, "replace_scalar") {
			t.Errorf("NoRemedy does not explain itself: %q", got.NoRemedy)
		}
	})
}

// The wire shape. A field nothing serialises is a field no client can read.
func TestRemedyIsOnTheWire(t *testing.T) {
	rep, _ := reportFor(t, "e48-credential-remedy.yml")
	f := findingOn(t, rep, credRule, "environment.POSTGRES_PASSWORD")
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back struct {
		Fix struct {
			Remedy *Remedy `json:"remedy"`
		} `json:"fix"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Fix.Remedy == nil || back.Fix.Remedy.Name != "POSTGRES_PASSWORD" {
		t.Fatalf("the remedy did not survive the wire: %s", raw)
	}
	// ...and a fix with no remedy must not emit an empty object, which a client
	// would render as an offer with nothing in it.
	g := findingOn(t, rep, credRule, "environment.SERVICE_SECRET")
	raw, _ = json.Marshal(g)
	// `"remedy":`, with the colon: the fixture's own FILE NAME contains the word
	// and appears in every fix's `file`, so a bare substring check here passes
	// whatever the code does. That is the twenty-second check that could not
	// fail, caught on the way in.
	if strings.Contains(string(raw), `"remedy":`) {
		t.Fatalf("a fix with no remedy serialised one: %s", raw)
	}
}
