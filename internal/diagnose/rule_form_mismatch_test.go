package diagnose

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

// The merge-form-mismatch rule.
//
// `environment` as a list in one file and a mapping in the other drops the base
// file's entries — deliberately, because the model keeps the shape each file
// wrote. Before this rule the only place that said so was `composure resolve
// -json`; `composure diagnose` was silent, and so, through the same report, was
// the extension's problems panel.

// crossForm resolves a two-file project whose `environment` disagrees about
// its shape, and runs every registered rule over it.
func crossForm(t *testing.T, base, override string) *Report {
	t.Helper()
	return diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml":          base,
		"compose.override.yaml": override,
	}).rep
}

func onlyFormMismatch(t *testing.T, rep *Report) Finding {
	t.Helper()
	var out []Finding
	for _, f := range rep.Findings {
		if f.Rule == "merge-form-mismatch" {
			out = append(out, f)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d merge-form-mismatch findings, want 1: %s", len(out), summarise(rep.Findings))
	}
	return out[0]
}

// SURFACE 3: diagnose. What was dropped, from which file, and why.
func TestDiagnoseReportsConfigurationTheMergeDropped(t *testing.T) {
	rep := crossForm(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      - BASE_A=1\n      - SHARED=base\n",
		"services:\n  web:\n    environment:\n      SHARED: over\n")
	f := onlyFormMismatch(t, rep)

	if f.Severity != SeverityWarning {
		t.Errorf("severity = %s, want warning: the stack runs, with less configuration than was written", f.Severity)
	}
	// WHAT.
	if !strings.Contains(f.Message, "BASE_A") {
		t.Errorf("the message does not name what was lost: %s", f.Message)
	}
	// SHARED is set by both, so it is not a loss and must not be reported as one.
	if strings.Contains(f.Message, "SHARED") {
		t.Errorf("an entry the override re-declares was reported as dropped: %s", f.Message)
	}
	// FROM WHICH FILE.
	if !strings.Contains(f.Message, "compose.yaml") {
		t.Errorf("the message does not say which file lost its entries: %s", f.Message)
	}
	// WHY.
	if !strings.Contains(f.Message, "re-emitting the collection") {
		t.Errorf("the message does not explain the design: %s", f.Message)
	}

	// Two anchors: where the surviving shape is, and where the lost entries are
	// still written. The second is the one a reader has to go to.
	if len(f.Anchors) != 2 {
		t.Fatalf("anchors = %+v, want the kept declaration and the displaced one", f.Anchors)
	}
	if filepath.Base(f.Anchors[1].Origin.File) != "compose.yaml" || f.Anchors[1].Origin.Line != 5 {
		t.Errorf("the displaced anchor is %s, want compose.yaml:5:x", f.Anchors[1].Origin)
	}

	// No fix, and the reason is the design rather than a shrug.
	if f.Fix != nil {
		t.Fatalf("a fix was offered for a loss only the author can reconcile: %+v", f.Fix)
	}
	if !strings.Contains(f.NoFix, "never re-emits a collection") {
		t.Errorf("NoFix does not give the real reason: %q", f.NoFix)
	}
}

// The other direction — mapping in the base, list in the override — loses the
// base's entries just the same, and must report just the same. The defect was
// verified in BOTH directions, so it is pinned in both.
func TestDiagnoseReportsTheDropInEitherDirection(t *testing.T) {
	rep := crossForm(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      BASE_A: \"1\"\n      SHARED: base\n",
		"services:\n  web:\n    environment:\n      - SHARED=over\n")
	f := onlyFormMismatch(t, rep)
	if !strings.Contains(f.Message, "BASE_A") {
		t.Errorf("mapping-then-list did not name the lost entry: %s", f.Message)
	}
}

// depends_on is the same class and the one where silence costs most: a dropped
// dependency changes start order.
func TestDiagnoseReportsADroppedDependency(t *testing.T) {
	rep := crossForm(t,
		"services:\n  web:\n    image: nginx\n    depends_on:\n      - db\n      - cache\n"+
			"  db:\n    image: postgres\n  cache:\n    image: redis\n",
		"services:\n  web:\n    depends_on:\n      db:\n        condition: service_healthy\n")
	f := onlyFormMismatch(t, rep)
	if !strings.Contains(f.Message, "cache") {
		t.Errorf("the dropped dependency is not named: %s", f.Message)
	}
}

// Two files that agree on the form raise nothing. A rule that fires on ordinary
// merges is noise, and noise is how a real finding gets ignored.
//
// THE FIXTURE CARRIES A RESOLVER FINDING OF ANOTHER KIND ON PURPOSE.
//
// It used to be a plain same-form merge, which produced ZERO resolver findings
// of any kind — so it could not tell "the rule filters by Kind" from "the rule
// emits for every resolver finding there is". Deleting the kind filter at the
// top of apply() left this green. Under that mutant a single-file project
// containing `image: nginx:${MISSING_TAG}` reports `merge-form-mismatch:
// services.web.image is written in two different shapes across the merge` — a
// mismatch across files that do not exist.
//
// `${MISSING_TAG}` raises a FindingUndefinedVar. The forms still agree, so the
// only correct number of merge-form-mismatch findings is nought, and the kind
// filter is now the only thing producing it.
func TestDiagnoseIsSilentWhenTheFormsAgree(t *testing.T) {
	proj := diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml":          "services:\n  web:\n    image: nginx:${MISSING_TAG}\n    environment:\n      A: 1\n",
		"compose.override.yaml": "services:\n  web:\n    environment:\n      B: 2\n",
	})

	// The premise, asserted rather than assumed. Without a resolver finding of
	// SOME other kind this test cannot distinguish a filter from no filter, and
	// it goes back to being a check that cannot fail.
	var other int
	for _, f := range proj.proj.Findings() {
		if f.Kind != resolve.FindingFormMismatch {
			other++
		}
	}
	if other == 0 {
		t.Fatal("the fixture produces no resolver finding of any other kind, so the kind filter " +
			"is not exercised and this test would pass with the filter deleted")
	}

	for _, f := range proj.rep.Findings {
		if f.Rule == "merge-form-mismatch" {
			t.Errorf("fired on a same-form merge: %s", f.Message)
		}
	}
}

// The rule is registered. A rule that exists and is not in the registry runs
// nowhere, and its absence from a report is indistinguishable from silence.
func TestFormMismatchRuleIsRegistered(t *testing.T) {
	for _, r := range Rules() {
		if r.ID == "merge-form-mismatch" {
			return
		}
	}
	t.Fatal("merge-form-mismatch is not in the registry, so it never runs")
}
