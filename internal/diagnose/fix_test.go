package diagnose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// The range guard, pinned on its own.
//
// The guard and the merged-path remap protect against the same defect from two
// sides, which means each masks the other: with the remap working the guard
// never fires, and with the guard in place a broken remap only loses fixes
// instead of destroying files. Mutation testing showed exactly that — removing
// either one alone left the suite green.
//
// So the remap is pinned by requiring a correct fix in a multi-file project
// (multifile_test.go), and the guard is pinned here, by handing locateFix a
// locator that deliberately points somewhere else. A future rule that computes a
// range against the wrong node looks exactly like this.

const guardSrc = "services:\n  web:\n    image: nginx\n    environment:\n      - A=1\n      - SECRET=hunter2\n"

func guardContext() *context {
	return &context{Input: Input{
		Sources: map[string][]byte{"compose.yaml": []byte(guardSrc)},
		Env:     map[string]string{},
	}}
}

// A locator that names bytes on a different line than the finding is anchored at
// must be refused, whatever it claims.
//
// The refusal happens in Run now, not in locateFix — see the header of fix.go.
// locateFix is a helper and a helper is optional, so the check moved to the one
// place every finding passes through. This drives it where it lives: locateFix
// builds the wrong fix, unownedFix throws it out.
func TestLocateFixRefusesARangeOnAnotherLine(t *testing.T) {
	c := guardContext()
	path := resolve.Path{"services", "web", "environment", "1"}
	origin := resolve.Origin{File: "compose.yaml", Line: 6, Column: 9} // SECRET=hunter2

	// Line 5 is `- A=1`. Its bytes are real, inside the file, and wrong.
	wrong := strings.Index(guardSrc, "A=1")
	fix, why := c.locateFix(FixReplaceScalar, path, origin,
		func([]byte, []string) (int, int, error) { return wrong, wrong + 3, nil })
	if fix != nil {
		why = c.unownedFix(fix)
	}

	if why == "" {
		t.Fatalf("a fix was described over bytes %d-%d on another line than its anchor",
			fix.Range.Start, fix.Range.End)
	}
	if !strings.Contains(why, "does not own the bytes") {
		t.Errorf("the refusal does not explain itself: %q", why)
	}
}

// The same locator pointing at the anchored line is accepted, so the guard is
// refusing the mismatch rather than refusing everything.
func TestLocateFixAcceptsARangeOnItsOwnLine(t *testing.T) {
	c := guardContext()
	path := resolve.Path{"services", "web", "environment", "1"}
	origin := resolve.Origin{File: "compose.yaml", Line: 6, Column: 9}

	right := strings.Index(guardSrc, "SECRET=hunter2")
	fix, why := c.locateFix(FixReplaceScalar, path, origin,
		func([]byte, []string) (int, int, error) { return right, right + len("SECRET=hunter2"), nil })

	if fix == nil {
		t.Fatalf("a correct range was refused: %s", why)
	}
	if fix.Range.Line != 6 {
		t.Errorf("fix reports line %d, want 6", fix.Range.Line)
	}
	if got := c.unownedFix(fix); got != "" {
		t.Errorf("Run's guard refused a correct fix: %s", got)
	}
}

// The out-of-bounds arm of disowned, one condition at a time.
//
// It was covered by NOTHING. TestDisownedPerOperation has a "past the end of
// the file" case and a "running backwards" case, but both are anchored at line
// 6 while their start byte is on another line, so the per-operation arm refuses
// them first and deleting the bounds check entirely left the suite green. Each
// case below is constructed so that the operation's own relation HOLDS and the
// bounds check is the only thing left to refuse it.
func TestDisownedRefusesRangesOutsideTheFile(t *testing.T) {
	src := []byte(guardSrc)
	secret := strings.Index(guardSrc, "SECRET=hunter2")
	at6 := resolve.Origin{File: "compose.yaml", Line: 6, Column: 9}
	// lineAt clamps a negative offset to line 1, so anchoring at line 1 makes
	// the replace_scalar relation agree and leaves `start < 0` alone to fire.
	at1 := resolve.Origin{File: "compose.yaml", Line: 1, Column: 1}

	cases := []struct {
		name       string
		start, end int
		origin     resolve.Origin
	}{
		{"start before the beginning of the file", -5, 3, at1},
		{"end before start", secret, secret - 1, at6},
		{"end past the last byte", secret, len(guardSrc) + 1, at6},
	}
	for _, c := range cases {
		// The premise: without the bounds check, nothing else would object.
		if got := lineAt(src, c.start); got != c.origin.Line {
			t.Fatalf("%s: fixture is wrong — start is on line %d, anchored at %d, so the "+
				"operation's own relation would refuse this and the bounds check would go untested",
				c.name, got, c.origin.Line)
		}
		if got := disowned(src, FixReplaceScalar, c.origin, c.start, c.end); got == "" {
			t.Errorf("%s: bytes %d-%d accepted against a %d byte file", c.name, c.start, c.end, len(src))
		}
	}
}

// disowned itself, per operation. These are the relations the guard encodes, and
// each is a different sentence about which bytes the operation owns.
func TestDisownedPerOperation(t *testing.T) {
	src := []byte(guardSrc)
	// Line 6 is `      - SECRET=hunter2`.
	line6 := strings.Index(guardSrc, "      - SECRET")
	secret := strings.Index(guardSrc, "SECRET=hunter2")
	line5 := strings.Index(guardSrc, "      - A=1")
	at6 := resolve.Origin{File: "compose.yaml", Line: 6, Column: 9}

	cases := []struct {
		name        string
		op          string
		start, end  int
		origin      resolve.Origin
		wantRefusal bool
	}{
		{"replace on its own line", FixReplaceScalar, secret, secret + 14, at6, false},
		{"replace one line above", FixReplaceScalar, line5, line5 + 11, at6, true},
		{"delete spanning the anchored line", FixDeleteKey, line5, len(guardSrc), at6, false},
		{"delete ending above the anchor", FixDeleteKey, line5, line6, at6, true},
		{"insert at or below the target", FixInsertKey, len(guardSrc) - 1, len(guardSrc) - 1, at6, false},
		{"insert above the target", FixInsertKey, line5, line5, at6, true},
		{"range past the end of the file", FixReplaceScalar, 0, len(guardSrc) + 10, at6, true},
		{"range running backwards", FixReplaceScalar, 20, 10, at6, true},
	}
	for _, c := range cases {
		got := disowned(src, c.op, c.origin, c.start, c.end)
		if c.wantRefusal && got == "" {
			t.Errorf("%s: accepted, want a refusal", c.name)
		}
		if !c.wantRefusal && got != "" {
			t.Errorf("%s: refused with %q, want acceptance", c.name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// localPath, pinned directly.

// The merged index is discarded and the file's own index is re-derived from the
// Origin. This is the whole remap in one assertion.
func TestLocalPathReDerivesTheFileIndex(t *testing.T) {
	c := guardContext()
	src := []byte(guardSrc)

	// Pretend the merge put SECRET at index 4 — another file contributed three
	// entries above it. In compose.yaml it is index 1.
	merged := resolve.Path{"services", "web", "environment", "4"}
	origin := resolve.Origin{File: "compose.yaml", Line: 6, Column: 9}

	local, err := c.localPath(src, merged, origin)
	if err != nil {
		t.Fatalf("localPath refused a resolvable path: %v", err)
	}
	if local.String() != "services.web.environment[1]" {
		t.Errorf("localPath returned %s, want services.web.environment[1]", local)
	}
}

// A path whose mapping segments this file does not write is refused rather than
// approximated: the value came from another file or through an extends.
func TestLocalPathRefusesAPathThisFileDoesNotWrite(t *testing.T) {
	c := guardContext()
	_, err := c.localPath([]byte(guardSrc),
		resolve.Path{"services", "api", "environment", "0"},
		resolve.Origin{File: "compose.yaml", Line: 6, Column: 9})
	if err == nil {
		t.Fatal("localPath resolved a service this file does not declare")
	}
}

// An Origin naming a line with nothing at that path is refused. This is the
// alias case: the value is in the model but not at that path in the file.
func TestLocalPathRefusesWhenNothingIsAtThatLine(t *testing.T) {
	c := guardContext()
	_, err := c.localPath([]byte(guardSrc),
		resolve.Path{"services", "web", "environment", "0"},
		resolve.Origin{File: "compose.yaml", Line: 3, Column: 5}) // the image line
	if err == nil {
		t.Fatal("localPath matched an entry on a line that holds no entry")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the refusal does not name the position it could not match: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The ambiguity refusal, and why it is the only defence.
//
// localPath refuses when more than one candidate answers to the Origin. That
// arm had no test, and deleting it — `return hits[0], nil` — left the whole
// suite green while reproducing the F3.1 data loss on a flow-style sequence.
//
// disowned() CANNOT catch this, and the reason is structural rather than an
// oversight: its oracle compares a LINE NUMBER (Fix.Range.Line, taken from the
// Origin) against a BYTE OFFSET (Fix.Range.Start, from strategy). Two
// candidates on the SAME LINE satisfy that relation identically, so the guard
// accepts the wrong one as readily as the right one. Flow style is exactly what
// puts two entries on one line:
//
//	environment: [A=1, SECRET=hunter2]
//
// Under the mutant, the credential finding anchored at SECRET ships a
// replace_scalar over `A=1` — destroying an unrelated variable, leaving the
// credential in the file, and returning success.
//
// The fixture is testdata/edge/e16-flow-env-credential.yml, read rather than
// inlined so the shape is a permanent regression file.

func flowFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", "e16-flow-env-credential.yml"))
	if err != nil {
		t.Skipf("regression file unavailable: %v", err)
	}
	return string(b)
}

// End to end: whatever diagnose decides to do about a credential inside a
// flow-style sequence, it must never describe an edit over bytes belonging to a
// DIFFERENT entry.
//
// The assertion is deliberately not "the message says ambiguous". A refusal
// with different wording is still correct; a fix over the wrong bytes is the
// data loss. So this asserts the damage, not the prose.
func TestNoFixIsDescribedOverAnotherEntryOnTheSameLine(t *testing.T) {
	src := flowFixture(t)
	rep := diagnoseProject(t, "compose.yaml", nil, map[string]string{"compose.yaml": src}).rep

	var found bool
	for _, f := range rep.Findings {
		if f.Rule != "plaintext-credential" {
			continue
		}
		found = true
		if f.Fix == nil {
			if f.NoFix == "" {
				t.Error("no fix and no reason: CLAUDE.md rule 6 asks for a refusal that says why")
			}
			continue
		}
		got := src[f.Fix.Range.Start:f.Fix.Range.End]
		if !strings.Contains(got, "hunter2") {
			t.Errorf("a fix was described over bytes %d-%d, which read %q — that is a different "+
				"environment entry on the same line. Applying it destroys %q and leaves the "+
				"credential in the file, and reports success",
				f.Fix.Range.Start, f.Fix.Range.End, got, got)
		}
	}
	if !found {
		t.Fatal("the credential in the flow sequence raised nothing at all; this test would pass vacuously")
	}
}

// The refusal at the unit, so the arm is pinned where it lives as well as
// through the rule. Two candidates on one line, and the answer is an error.
func TestLocalPathRefusesTwoCandidatesOnOneLine(t *testing.T) {
	text := flowFixture(t)
	src := []byte(text)
	envAt := strings.Index(text, "environment:")
	line := 1 + strings.Count(text[:envAt], "\n")

	// The premise, asserted rather than assumed: BOTH entries locate to this one
	// line. Without that this test could not distinguish the refusal from any
	// other failure, which is the degenerate shape it exists to avoid.
	for _, idx := range []string{"0", "1"} {
		p := resolve.Path{"services", "web", "environment", idx}
		at, _, err := strategy.Locate(src, p)
		if err != nil || at+1 != line {
			t.Fatalf("fixture regressed: %s is at line %d (%v), want %d — the two entries "+
				"no longer share a line and this asserts nothing", p, at+1, err, line)
		}
	}

	c := &context{Input: Input{Sources: map[string][]byte{"compose.yaml": src}, Env: map[string]string{}}}
	_, err := c.localPath(src, resolve.Path{"services", "web", "environment", "1"},
		resolve.Origin{File: "compose.yaml", Line: line})
	if err == nil {
		t.Fatal("localPath picked one of two indistinguishable candidates instead of refusing; " +
			"the one it picks is the wrong one half the time and there is nothing downstream that can tell")
	}

	// And disowned, the guard that is supposed to be the backstop, cannot see
	// it. This is the reason the refusal has to stay: it is not redundant with
	// anything. Both entries are on the anchored line, so the wrong range passes.
	wrong := envAt + strings.Index(text[envAt:], "A=1")
	if got := disowned(src, FixReplaceScalar,
		resolve.Origin{File: "compose.yaml", Line: line}, wrong, wrong+3); got != "" {
		t.Errorf("disowned refused the wrong-entry range (%q) — if it can now see this, the "+
			"comment in localpath.go explaining why it cannot is stale and should be corrected", got)
	}
}

// A path with no sequence index at all still has to land on its own line — the
// remap is the guard's first half for every fix, not only sequence ones.
func TestLocalPathHandlesMappingOnlyPaths(t *testing.T) {
	c := guardContext()
	local, err := c.localPath([]byte(guardSrc),
		resolve.Path{"services", "web", "image"},
		resolve.Origin{File: "compose.yaml", Line: 3, Column: 12})
	if err != nil {
		t.Fatalf("localPath refused a plain mapping path: %v", err)
	}
	if local.String() != "services.web.image" {
		t.Errorf("localPath returned %s", local)
	}
}
