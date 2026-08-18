package diagnose

import (
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// The guards in Run, tested directly.
//
// Every one of these was unenforced by the suite: each rule happens to
// self-censor, so deleting the guard in Run changed no test. A guard that only
// works because nobody has needed it yet is not a guard, it is a comment — and
// the next rule is the one that finds out.
//
// These drive Run with a rule that deliberately misbehaves, which is the only
// way to reach the guard at all.

// fakeRule emits exactly the findings it is given.
type fakeRule struct {
	ruleID   string
	name     string
	findings []Finding
}

func (r fakeRule) id() string    { return r.ruleID }
func (r fakeRule) title() string { return r.name }
func (r fakeRule) apply(*context) []Finding {
	out := make([]Finding, len(r.findings))
	copy(out, r.findings)
	return out
}

// withOnlyRule replaces the registry for one test and restores it after. The
// registry is a package variable by design (registry.go), so this is the
// supported way to run a rule that must never ship.
func withOnlyRule(t *testing.T, r rule) {
	t.Helper()
	saved := rules
	rules = []rule{r}
	t.Cleanup(func() { rules = saved })
}

// runRulesSrc is the file every guard test's fixture ranges address. It is a
// named constant because the range guard now checks those ranges against it:
// a fixture with a made-up range is refused, which is the point.
const runRulesSrc = "services:\n  web:\n    image: nginx\n"

// runRules drives Run over a trivial project with whatever registry is in place.
func runRules(t *testing.T) *Report {
	t.Helper()
	const name = "compose.yaml"
	const src = runRulesSrc
	project, err := resolve.BytesWith(name, []byte(src), resolve.Options{Env: map[string]string{}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	rep, err := Run(Input{
		Path: name, Project: project, Graph: graph,
		Sources: map[string][]byte{name: []byte(src)},
		Env:     map[string]string{}, EnvKnown: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return rep
}

func anchored() []Anchor {
	return []Anchor{{
		Label:  "here",
		Path:   resolve.Path{"services", "web"},
		Origin: resolve.Origin{File: "compose.yaml", Line: 2, Column: 3},
	}}
}

// AD-7: a finding with no anchor is dropped by Run, not shipped positionless.
// Every real rule declines to emit one, so this is the only test that reaches
// the guard.
func TestRunDropsAnchorlessFindings(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{Message: "no anchor at all"},
		{Message: "properly anchored", Anchors: anchored()},
	}})
	rep := runRules(t)
	if len(rep.Findings) != 1 {
		t.Fatalf("%d findings survived, want only the anchored one: %s", len(rep.Findings), summarise(rep.Findings))
	}
	if rep.Findings[0].Message != "properly anchored" {
		t.Errorf("the wrong finding survived: %s", rep.Findings[0].Message)
	}
}

// An anchorless finding must not reach the sort either. sortFindings reads
// Anchors[0] for its position tiebreak; before firstOrigin it dereferenced that
// unguarded, so the first rule to emit an anchorless finding would panic the
// whole report rather than lose one line of it.
func TestSortFindingsSurvivesAnchorlessFindings(t *testing.T) {
	// Same severity AND same rule on the anchorless pair, deliberately: the
	// comparator returns on the first two keys, so a fixture whose findings
	// differ by rule never reads an Origin at all and an unguarded Anchors[0]
	// survives it. These two force the position comparison.
	fs := []Finding{
		{Rule: "same", Severity: SeverityWarning, Message: "second"},
		{Rule: "same", Severity: SeverityWarning, Message: "first"},
		{Rule: "other", Severity: SeverityError, Message: "worst", Anchors: anchored()},
	}
	sortFindings(fs) // must not panic
	if fs[0].Message != "worst" {
		t.Errorf("worst-first is broken: order is %s,%s,%s", fs[0].Message, fs[1].Message, fs[2].Message)
	}
	// With no position to separate them, the message tiebreak still orders them.
	if fs[1].Message != "first" || fs[2].Message != "second" {
		t.Errorf("anchorless findings did not fall through to the message tiebreak: %s,%s", fs[1].Message, fs[2].Message)
	}
}

// Story 3.8: a finding without a fix says why. The default is Run's, and no rule
// relies on it — all of them set NoFix themselves — so only a fake rule reaches
// it.
func TestRunSuppliesTheDefaultNoFixReason(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{Message: "silent about its fix", Anchors: anchored()},
	}})
	rep := runRules(t)
	got := rep.Findings[0]
	if got.Fix != nil {
		t.Fatal("a fix appeared from nowhere")
	}
	if got.NoFix == "" {
		t.Fatal("a finding shipped with neither a fix nor a reason; silence is not one of the options")
	}
	if !strings.Contains(got.NoFix, "no mechanical fix") {
		t.Errorf("the default reason is not the documented one: %q", got.NoFix)
	}
}

// A rule's own NoFix is never overwritten by the default.
func TestRunKeepsARulesOwnNoFixReason(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{Message: "explains itself", Anchors: anchored(), NoFix: "because the author has to decide"},
	}})
	if got := runRules(t).Findings[0].NoFix; got != "because the author has to decide" {
		t.Errorf("NoFix was rewritten to %q", got)
	}
}

// Run fills in the rule id and title a rule left blank, so a finding is always
// attributable.
func TestRunFillsInRuleIdentity(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake-id", name: "Fake Title", findings: []Finding{
		{Message: "anonymous", Anchors: anchored()},
	}})
	got := runRules(t).Findings[0]
	if got.Rule != "fake-id" || got.Title != "Fake Title" {
		t.Errorf("finding is %q/%q, want the rule's own id and title", got.Rule, got.Title)
	}
}

// A value-writing fix that carries no value and does not declare that one is
// needed is refused. Applied verbatim it would write an empty scalar and report
// success: `- ""` in a ports list is valid YAML and invalid compose.
func TestRunRefusesAValueFixWithNoValue(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "silently empty",
			Anchors: anchored(),
			Fix: &Fix{
				Operation: FixReplaceScalar,
				File:      "compose.yaml",
				Path:      resolve.Path{"services", "web", "image"},
				Range:     ByteRange{Start: 22, End: 27, Line: 3},
				Describe:  "replace it with nothing at all",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix != nil {
		t.Fatalf("a fix that would write an empty value was offered: %+v", got.Fix)
	}
	if !strings.Contains(got.NoFix, "carries no value") {
		t.Errorf("the refusal does not explain itself: %q", got.NoFix)
	}
}

// ...but the same fix declaring NeedsValue is offered, because the distinction
// is now expressible. That is the whole point of the field.
func TestRunKeepsAPlaceholderFixThatDeclaresItself(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "honestly empty",
			Anchors: anchored(),
			Fix: &Fix{
				Operation:  FixReplaceScalar,
				File:       "compose.yaml",
				Path:       resolve.Path{"services", "web", "image"},
				Range:      ByteRange{Start: 22, End: 27, Line: 3},
				NeedsValue: true,
				Describe:   "you choose",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix == nil {
		t.Fatalf("a declared placeholder fix was dropped: %s", got.NoFix)
	}
	if !got.Fix.NeedsValue {
		t.Error("NeedsValue was lost")
	}
}

// A delete_key needs no value, so the value guard must not touch it.
//
// The range was 10-40 when this was written, against a 34-byte file. Nothing
// objected, because the only guard in Run was the value one — which is finding
// 3 in miniature, sitting in the suite that was supposed to be pinning the
// guards. It names the real subtree now: `  web:` through the end of the file.
func TestRunLeavesValuelessOperationsAlone(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "a delete",
			Anchors: anchored(),
			Fix: &Fix{
				Operation: FixDeleteKey,
				File:      "compose.yaml",
				Path:      resolve.Path{"services", "web"},
				Range:     ByteRange{Start: 10, End: len(runRulesSrc), Line: 2},
				Describe:  "delete it",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix == nil {
		t.Errorf("a delete_key fix was refused: %s", got.NoFix)
	}
}

// ---------------------------------------------------------------------------
// The range guard, in Run.
//
// disowned() used to live only inside the locateFix helper, so a rule that
// built a Fix literal — which nothing forbids, and which `&Fix{` in fix.go is
// the only current instance of — shipped a range nobody checked. Both of these
// survived Run untouched before the guard moved.

// A fix whose bytes are on a line other than the one it reports is refused.
func TestRunRefusesAFixWhoseRangeIsOnAnotherLine(t *testing.T) {
	// Bytes 10-16 are `  web:` on line 2. The fix claims line 3.
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "points somewhere else",
			Anchors: anchored(),
			Fix: &Fix{
				Operation: FixReplaceScalar,
				File:      "compose.yaml",
				Path:      resolve.Path{"services", "web", "image"},
				Range:     ByteRange{Start: 10, End: 16, Line: 3},
				Value:     "nginx:1.27",
				Describe:  "replace the image",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix != nil {
		t.Fatalf("a fix owning bytes on line 2 while claiming line 3 was offered: %+v", got.Fix)
	}
	if !strings.Contains(got.NoFix, "does not own the bytes") {
		t.Errorf("the refusal does not explain itself: %q", got.NoFix)
	}
}

// A fix whose range is not in the file at all is refused. This is the shape
// that would have someone splice at byte 9000 of a 34-byte file.
func TestRunRefusesAFixWhoseRangeIsPastTheEndOfTheFile(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "off the end",
			Anchors: anchored(),
			Fix: &Fix{
				Operation: FixReplaceScalar,
				File:      "compose.yaml",
				Path:      resolve.Path{"services", "web", "image"},
				Range:     ByteRange{Start: 9000, End: 9999, Line: 1},
				Value:     "nginx:1.27",
				Describe:  "replace the image",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix != nil {
		t.Fatalf("a fix over bytes 9000-9999 of a %d byte file was offered: %+v", len(runRulesSrc), got.Fix)
	}
	if !strings.Contains(got.NoFix, "not inside a") {
		t.Errorf("the refusal does not explain itself: %q", got.NoFix)
	}
}

// A fix naming a file Run cannot read is refused rather than trusted. An
// unverifiable range is exactly what the guard exists to stop shipping.
func TestRunRefusesAFixAgainstAFileItCannotRead(t *testing.T) {
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "another file entirely",
			Anchors: anchored(),
			Fix: &Fix{
				Operation: FixReplaceScalar,
				File:      "somewhere-else.yaml",
				Path:      resolve.Path{"services", "web", "image"},
				Range:     ByteRange{Start: 0, End: 3, Line: 1},
				Value:     "nginx:1.27",
				Describe:  "replace the image",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix != nil {
		t.Fatalf("a fix was offered against bytes nobody has seen: %+v", got.Fix)
	}
	if !strings.Contains(got.NoFix, "could not be read") {
		t.Errorf("the refusal does not explain itself: %q", got.NoFix)
	}
}

// ...and a fix that does own its bytes still ships. The guard has to refuse the
// liars, not everything.
func TestRunKeepsAFixThatOwnsItsBytes(t *testing.T) {
	// `nginx` on line 3.
	start := strings.Index(runRulesSrc, "nginx")
	withOnlyRule(t, fakeRule{ruleID: "fake", name: "Fake", findings: []Finding{
		{
			Message: "honest",
			Anchors: anchored(),
			Fix: &Fix{
				Operation: FixReplaceScalar,
				File:      "compose.yaml",
				Path:      resolve.Path{"services", "web", "image"},
				Range:     ByteRange{Start: start, End: start + len("nginx"), Line: 3},
				Value:     "nginx:1.27",
				Describe:  "replace the image",
			},
		},
	}})
	got := runRules(t).Findings[0]
	if got.Fix == nil {
		t.Fatalf("a fix that owns its bytes was refused: %s", got.NoFix)
	}
}

// ---------------------------------------------------------------------------
// sortFindings, which TestDeterministic could not pin: comparing two runs is
// satisfied by any stable order, including no sort at all.

func TestSortFindingsIsWorstFirstThenRuleThenPosition(t *testing.T) {
	at := func(file string, line, col int) []Anchor {
		return []Anchor{{Path: resolve.Path{"x"}, Origin: resolve.Origin{File: file, Line: line, Column: col}}}
	}
	// Every message is chosen to CONTRADICT the key above it in the order, so
	// that dropping any one comparison changes the result. A fixture whose
	// message order agrees with its position order cannot tell the column
	// tiebreak from the message tiebreak — which is how the column comparison
	// went unpinned the first time this test was written.
	fs := []Finding{
		{Rule: "z-rule", Severity: SeverityHint, Anchors: at("a.yaml", 1, 1), Message: "hint"},
		// Line 1 in the LATER file: without the file comparison this would sort
		// above every a.yaml finding, so the file key is pinned rather than
		// coincidentally agreeing with the line key.
		{Rule: "b-rule", Severity: SeverityError, Anchors: at("b.yaml", 1, 1), Message: "aaa second file"},
		{Rule: "b-rule", Severity: SeverityError, Anchors: at("a.yaml", 5, 1), Message: "later line"},
		{Rule: "b-rule", Severity: SeverityError, Anchors: at("a.yaml", 5, 1), Message: "aaa same position"},
		{Rule: "a-rule", Severity: SeverityError, Anchors: at("z.yaml", 1, 1), Message: "zzz sorts first by rule"},
		{Rule: "b-rule", Severity: SeverityError, Anchors: at("a.yaml", 2, 7), Message: "aaa later column"},
		{Rule: "b-rule", Severity: SeverityError, Anchors: at("a.yaml", 2, 3), Message: "zzz earlier column"},
		{Rule: "m-rule", Severity: SeverityWarning, Anchors: at("a.yaml", 1, 1), Message: "warning"},
	}
	sortFindings(fs)

	want := []string{
		"zzz sorts first by rule", // error, a-rule — beats b-rule despite z.yaml and a "zzz" message
		"zzz earlier column",      // error, b-rule, a.yaml:2:3 — column beats the message tiebreak
		"aaa later column",        // error, b-rule, a.yaml:2:7
		"aaa same position",       // error, b-rule, a.yaml:5:1 — message tiebreak decides
		"later line",              // error, b-rule, a.yaml:5:1
		"aaa second file",         // error, b-rule, b.yaml — file beats the message tiebreak
		"warning",                 // warning
		"hint",                    // hint
	}
	for i, w := range want {
		if fs[i].Message != w {
			var got []string
			for _, f := range fs {
				got = append(got, f.Message)
			}
			t.Fatalf("order is %v, want %v", got, want)
		}
	}
}

// Severity dominates every other key: a hint in the first file still sorts below
// an error in the last one.
func TestSortFindingsPutsSeverityAboveposition(t *testing.T) {
	fs := []Finding{
		{Rule: "a", Severity: SeverityHint, Anchors: []Anchor{{Origin: resolve.Origin{File: "a.yaml", Line: 1, Column: 1}}}},
		{Rule: "z", Severity: SeverityError, Anchors: []Anchor{{Origin: resolve.Origin{File: "z.yaml", Line: 99, Column: 1}}}},
	}
	sortFindings(fs)
	if fs[0].Severity != SeverityError {
		t.Error("a hint sorted above an error")
	}
}
