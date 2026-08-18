package diagnose

import (
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

const varRule = "undefined-variable"

// Every test here is written against the shape story 3.6 asked for. The source
// of the references is now story 1.3's interpolation pass — the rule reads
// resolve.Finding and scans no text of its own — and these tests are what kept
// the grouping, the exemptions and the anchoring fixed while that source was
// swapped underneath them.

func TestUndefinedVariable(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    environment:
      SESSION_SECRET: ${SESSION_SECRET}
`)
	got := wantCount(t, rep, varRule, 1)[0]
	if !strings.Contains(got.Message, "SESSION_SECRET") {
		t.Errorf("message does not name the variable: %s", got.Message)
	}
	if got.Anchors[0].Origin.Line != 6 {
		t.Errorf("anchored at line %d, want 6", got.Anchors[0].Origin.Line)
	}
}

// One finding per variable, carrying every site. Five findings for one missing
// variable is five times the noise for the same information.
func TestUndefinedVariableGroupsItsReferences(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: ${TAG}
    environment:
      A: ${TAG}
  b:
    image: ${TAG}
    environment:
      B: ${TAG}
      C: ${TAG}
`)
	got := wantCount(t, rep, varRule, 1)[0]
	if len(got.Anchors) != 5 {
		t.Fatalf("%d anchors, want one per reference site", len(got.Anchors))
	}
	lines := map[int]bool{}
	for _, a := range got.Anchors {
		lines[a.Origin.Line] = true
	}
	if len(lines) != 5 {
		t.Errorf("the five anchors point at %d distinct lines", len(lines))
	}
}

// An alias expanded into several services is the case that puts many references
// on ONE line, and it is the case dedupeReferences exists for. Nothing in this
// file reached it: every other fixture gives each reference its own position, so
// the whole function was indistinguishable from returning its input.
//
// What survives is one anchor per PATH, because each service genuinely holds
// that variable and a reader fixing service "b" needs to know it is affected.
// What collapses is the same path reported twice at the same position. The
// message's count and the anchor list must agree either way — a count that
// disagrees with the list it summarises is the defect this pins.
func TestUndefinedVariableThroughAnAliasCountsPlacesNotExpansions(t *testing.T) {
	rep := run(t, `
x-common: &common
  environment:
    SHARED: ${MISSING_THING}

services:
  a:
    image: x
    <<: *common
  b:
    image: x
    <<: *common
  c:
    image: x
    <<: *common
`)
	got := wantCount(t, rep, varRule, 1)[0]

	// Every anchor is on the one line the reference is written on.
	for _, a := range got.Anchors {
		if a.Origin.Line != 4 {
			t.Errorf("anchor at line %d, want line 4 where the reference is written", a.Origin.Line)
		}
	}
	// One per distinct path, and no path twice.
	seen := map[string]bool{}
	for _, a := range got.Anchors {
		if seen[a.Path.String()] {
			t.Errorf("the same path is anchored twice: %s", a.Path)
		}
		seen[a.Path.String()] = true
	}
	// The count in the sentence is the number of anchors, whatever that is.
	if !strings.Contains(got.Message, plural(len(got.Anchors), "place", "places")) {
		t.Errorf("the message's count disagrees with its %d anchors: %s", len(got.Anchors), got.Message)
	}
}

// dedupeReferences collapses on POSITION AND PATH together, not on position
// alone: two different paths that happen to share a position are two references.
func TestDedupeReferencesKeepsDistinctPositions(t *testing.T) {
	at := func(line, col int, path ...string) resolve.Finding {
		return resolve.Finding{
			Kind:     resolve.FindingUndefinedVariable,
			Variable: "V",
			Path:     resolve.Path(path),
			Origin:   resolve.Origin{File: "compose.yaml", Line: line, Column: col},
		}
	}
	in := []resolve.Finding{
		at(3, 5, "services", "a", "image"),
		at(3, 5, "services", "a", "image"), // exact duplicate: collapses
		at(3, 5, "services", "b", "image"), // same position, other path: kept
		at(4, 5, "services", "a", "image"), // same path, other line: kept
	}
	got := dedupeReferences(in)
	if len(got) != 3 {
		t.Fatalf("dedupeReferences returned %d of %d, want 3", len(got), len(in))
	}
	if got[0].Path.String() != "services.a.image" || got[1].Path.String() != "services.b.image" {
		t.Errorf("the wrong references survived: %s, %s", got[0].Path, got[1].Path)
	}
	// Order is the input's: the report is diffed in CI and must not shuffle.
	if got[2].Origin.Line != 4 {
		t.Errorf("third survivor is at line %d, want 4", got[2].Origin.Line)
	}
}

// The reference count in the message. Nothing checked the number or its
// grammar, so "referenced in 1 places" would have shipped.
func TestUndefinedVariableCountsItsReferences(t *testing.T) {
	one := wantCount(t, run(t, `
services:
  a:
    image: ${SOLO}
`), varRule, 1)[0]
	if !strings.Contains(one.Message, "referenced in one place") {
		t.Errorf("a single reference is not phrased as one place: %s", one.Message)
	}

	three := wantCount(t, run(t, `
services:
  a:
    image: ${TRIO}
    environment:
      A: ${TRIO}
      B: ${TRIO}
`), varRule, 1)[0]
	if !strings.Contains(three.Message, "referenced in 3 places") {
		t.Errorf("three references are not counted as 3 places: %s", three.Message)
	}
	if len(three.Anchors) != 3 {
		t.Errorf("%d anchors for 3 references", len(three.Anchors))
	}
}

// plural is the grammar itself, pinned directly: the boundary between "one" and
// a number is where this kind of string goes wrong.
func TestPluralGrammar(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{
		{1, "in one place"},
		{2, "in 2 places"},
		{0, "in 0 places"},
	} {
		if got := plural(c.n, "place", "places"); got != c.want {
			t.Errorf("plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// The negatives.

// A default is a definition — the story's own words.
func TestVariableWithDefaultIsNotReported(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx:${TAG:-latest}
    environment:
      A: ${A-fallback}
      B: ${B:+set}
      C: ${C+set}
`)
	wantNone(t, rep, varRule)
}

func TestVariableDefinedInTheEnvironmentIsNotReported(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx:${TAG}
`, withEnv(map[string]string{"TAG": "1.27"}))
	wantNone(t, rep, varRule)
}

// `$$` is Compose's escape for a literal dollar. Reporting it would flag every
// shell command in the corpus that uses awk.
func TestEscapedDollarIsNotAReference(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    command: sh -c 'echo $$HOME && awk "{print $$1}"'
`)
	wantNone(t, rep, varRule)
}

// The `?` forms declare the variable required, and story 1.3 makes that mean
// what it says: the resolve FAILS with the author's message, so there is no
// model for this rule — or any other — to run against. The finding this rule
// used to raise was the weaker answer, and it is gone on purpose.
func TestRequiredVariableFormFailsTheResolve(t *testing.T) {
	_, err := resolve.BytesWith("compose.yaml", []byte(`
services:
  web:
    image: nginx
    environment:
      A: ${MUST_BE_SET:?set this in .env}
`), resolve.Options{IgnoreHostEnv: true})
	if err == nil {
		t.Fatal("${VAR:?msg} with VAR unset resolved successfully; that form exists to fail")
	}
	if !strings.Contains(err.Error(), "set this in .env") {
		t.Errorf("the author's message is not in the error: %v", err)
	}
}

// The escape hatch that used to stand here — "if no environment could be
// established, say nothing" — is gone with the text scan. The resolver always
// has an environment, even an empty one, and its findings are the record of
// what it could not satisfy. Silence is no longer a thing this rule chooses.

// The rule is no longer provisional: its input IS story 1.3's resolver pass.
// A consumer reading Provisional to decide whether to trust silence must see
// the change.
func TestUndefinedVariableRuleIsNoLongerProvisional(t *testing.T) {
	for _, r := range Rules() {
		if r.ID == varRule {
			if r.Provisional {
				t.Error("the rule is still marked provisional, but it now reads the resolver's findings")
			}
			return
		}
	}
	t.Fatal("the rule is not registered")
}

func TestUndefinedVariableOffersNoFix(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx:${TAG}
`)
	got := wantCount(t, rep, varRule, 1)[0]
	if got.Fix != nil {
		t.Error("a fix was described; the definition belongs outside this document")
	}
	if !strings.Contains(got.NoFix, ".env") {
		t.Errorf("the refusal does not say where the definition belongs: %s", got.NoFix)
	}
}

// The grammar itself is the resolver's and is tested there, exhaustively,
// against all six operator forms. What this rule owns is which of those forms
// reach a reader: a default is a definition and must never produce a finding.
func TestDefaultFormsAreNeverReported(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx:${TAG:-latest}
    environment:
      A: ${LEVEL-info}
      B: ${SET:+on}
      C: literal
`)
	wantNone(t, rep, varRule)
}
