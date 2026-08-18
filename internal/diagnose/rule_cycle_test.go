package diagnose

import (
	"strings"
	"testing"
)

const cycleRuleID = "depends-on-cycle"

func TestCycleOfTwo(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [a]
`)
	got := wantCount(t, rep, cycleRuleID, 1)[0]
	if got.Severity != SeverityError {
		t.Errorf("severity is %s, want error", got.Severity)
	}
	if !strings.Contains(got.Message, "a -> b -> a") {
		t.Errorf("message does not name the ring in order: %s", got.Message)
	}
	if len(got.Subjects) != 2 {
		t.Errorf("%d subjects, want both members", len(got.Subjects))
	}
	// One anchor per declaring depends_on.
	if len(got.Anchors) != 2 {
		t.Fatalf("%d anchors, want one per link", len(got.Anchors))
	}
}

// The ring order must be the RING, not the member set in sorted order.
//
// The fixture is adversarial on purpose. Graph.Cycles returns members sorted, so
// a→b→c→a is the one cycle whose ring order equals its sorted order — under that
// fixture the whole sixty-line orderCycle walk is indistinguishable from
// `return members`, and this test passed with the walk replaced by exactly that.
// a→c→b→a separates them: sorted is [a b c], the ring is a -> c -> b -> a.
func TestCycleOfThreeKeepsRingOrder(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [c]
  c:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [a]
`)
	got := wantCount(t, rep, cycleRuleID, 1)[0]
	if !strings.Contains(got.Message, "a -> c -> b -> a") {
		t.Errorf("ring is not in dependency order: %s", got.Message)
	}
	// The sorted member order, which is what a walk that did nothing would emit.
	if strings.Contains(got.Message, "a -> b -> c -> a") {
		t.Errorf("the ring is in sorted member order, not dependency order: %s", got.Message)
	}
	if len(got.Anchors) != 3 {
		t.Errorf("%d anchors, want one per link", len(got.Anchors))
	}
	// Subjects follow the ring too, so a consumer joining on them sees the ring.
	var names []string
	for _, s := range got.Subjects {
		names = append(names, serviceName(s))
	}
	if strings.Join(names, ",") != "a,c,b" {
		t.Errorf("subjects are %v, want the ring order a,c,b", names)
	}
}

// The anchors have to land ON the declaring `depends_on` entry, not merely be
// present in the right number.
//
// AC 3.4: the finding "anchors at each declaring `depends_on`". Counting anchors
// cannot see that — `Origin{File: …, Line: 1, Column: 1}` on every anchor keeps
// the count and destroys the property, and it survived the whole suite. This
// asserts the position, keyed by label so a reordering cannot hide a wrong one.
func anchorPositions(t *testing.T, got Finding) map[string][2]int {
	t.Helper()
	at := map[string][2]int{}
	for _, a := range got.Anchors {
		if a.Origin.File != "compose.yaml" {
			t.Errorf("anchor %q points at file %q, want compose.yaml", a.Label, a.Origin.File)
		}
		if _, dup := at[a.Label]; dup {
			t.Errorf("two anchors labelled %q", a.Label)
		}
		at[a.Label] = [2]int{a.Origin.Line, a.Origin.Column}
	}
	return at
}

func wantAnchorsAt(t *testing.T, got Finding, want map[string][2]int) {
	t.Helper()
	at := anchorPositions(t, got)
	if len(at) != len(want) {
		t.Fatalf("%d distinct anchors, want %d: %v", len(at), len(want), at)
	}
	for label, pos := range want {
		gotPos, ok := at[label]
		if !ok {
			t.Errorf("no anchor labelled %q; got %v", label, at)
			continue
		}
		if gotPos != pos {
			t.Errorf("anchor %q at line %d column %d, want line %d column %d",
				label, gotPos[0], gotPos[1], pos[0], pos[1])
		}
	}
}

// Line 5 is `    depends_on: [b]` under `a:`; line 8 is the same under `b:`.
// Column 18 is the entry itself inside the flow sequence, not the key.
func TestCycleOfTwoAnchorsAtEachDeclaringDependsOn(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [a]
`)
	got := wantCount(t, rep, cycleRuleID, 1)[0]
	wantAnchorsAt(t, got, map[string][2]int{
		"a depends on b": {5, 18},
		"b depends on a": {8, 18},
	})
}

// The three-service ring, where the declaration order and the ring order differ:
// a→c is declared first (line 5), c→b second (line 8), b→a last (line 11). An
// anchor that carried the wrong link's origin would still be a plausible line
// number in this file, so the label-to-position pairing is what is checked.
func TestCycleOfThreeAnchorsAtEachDeclaringDependsOn(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [c]
  c:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [a]
`)
	got := wantCount(t, rep, cycleRuleID, 1)[0]
	wantAnchorsAt(t, got, map[string][2]int{
		"a depends on c": {5, 18},
		"c depends on b": {8, 18},
		"b depends on a": {11, 18},
	})
}

// A self-dependency is a cycle of one, and it is invisible to a size test.
func TestCycleOfOne(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [a]
`)
	got := wantCount(t, rep, cycleRuleID, 1)[0]
	if !strings.Contains(got.Message, "depends on itself") {
		t.Errorf("message does not say it is a self-dependency: %s", got.Message)
	}
}

// Two independent cycles are two findings. Merging them would suggest that
// breaking one link fixes both, which is false.
func TestTwoIndependentCyclesAreTwoFindings(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [a]
  y:
    image: x
    depends_on: [z]
  z:
    image: x
    depends_on: [y]
`)
	got := wantCount(t, rep, cycleRuleID, 2)
	if got[0].Message == got[1].Message {
		t.Error("the two findings describe the same cycle")
	}
}

// The negatives.

func TestDiamondIsNotACycle(t *testing.T) {
	rep := run(t, `
services:
  top:
    image: x
    depends_on: [left, right]
  left:
    image: x
    depends_on: [base]
  right:
    image: x
    depends_on: [base]
  base:
    image: x
`)
	wantNone(t, rep, cycleRuleID)
}

func TestLongChainIsNotACycle(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [c]
  c:
    image: x
    depends_on: [d]
  d:
    image: x
`)
	wantNone(t, rep, cycleRuleID)
}

// Story 3.8's judgement-call clause, as a test: this rule must decline a fix
// and say why, rather than say nothing.
func TestCycleOffersNoFixAndSaysSo(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [a]
`)
	got := wantCount(t, rep, cycleRuleID, 1)[0]
	if got.Fix != nil {
		t.Fatal("a fix was offered for a cycle; which link to break is not a tool's decision")
	}
	if !strings.Contains(got.NoFix, "judgement") {
		t.Errorf("the refusal does not explain itself: %s", got.NoFix)
	}
}

// The topology built successfully despite the cycle — the rule reads the cycle
// the layering pass found, and layering must not fail on cyclic input or
// diagnostics could never run on the stack that most needs them.
func TestCycleDoesNotBreakTheRestOfTheReport(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    depends_on: [b]
    environment:
      APP_SECRET: hunter2
  b:
    image: x
    depends_on: [a]
`)
	wantCount(t, rep, cycleRuleID, 1)
	wantCount(t, rep, credRule, 1)
}
