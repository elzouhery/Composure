package edit

// Story 6.5: a position mismatch is a REFUSAL, not a fault.
//
// `strategy.ErrPositionMismatch` is what the splice engines raise when the
// parser's reported position and the file's own bytes disagree. It is the guard
// that turns a plausible-and-wrong range into a safe no-op, so under CLAUDE.md
// rule 6 it is a declined operation: nothing was written and the reader can act
// on it. Classified as a failure it reads as "the tool broke", which is the one
// thing it is not — `cmd/composure/serve.go` sends codeEditFailed (-32004) for
// anything `Refused` says no to, and the panel then shows the engine's own
// sentence instead of prose.
//
// It has TWO producers, and the classification and the wording have to hold for
// both:
//
//   - `internal/strategy/strategy.go` — the lexeme assertion at the foot of
//     scalarSpan: the bytes at the computed offset are not the lexeme the
//     parser read. The rune-vs-byte column skew reached it
//     (`testdata/edge/e17-multibyte-flow.yml`) until story 6.4 made offsetOf
//     rune-aware.
//   - `internal/strategy/structural.go` — the line-count assertion in locate,
//     which caught goccy counting a COMMENT line in a CRLF document twice.
//     Nothing to do with runes; closed at the cause by parseView.
//
// Both causes are fixed and both guards are deliberately kept — they cost one
// comparison and they catch the failure this engine cannot detect any other
// way. So the errors below are BUILT in the exact shape each call site produces
// rather than provoked through a fixture: no input reaches either guard today,
// and a test that waited for one would assert nothing. What is asserted is the
// only thing that can be: an error carrying this sentinel, however it is
// wrapped and whichever site raised it, is classified as a refusal and carries
// the slug the panel has an arm for.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/strategy"
)

func TestAPositionMismatchIsARefusalWithASlug(t *testing.T) {
	for _, tc := range []struct {
		site string
		err  error
	}{
		{
			// internal/strategy/strategy.go, scalarSpan's lexeme assertion,
			// verbatim in shape: `%w: %s is reported at byte %d, where the file
			// reads %q and not %q`.
			site: "scalarSpan lexeme assertion",
			err: fmt.Errorf("%w: %s is reported at byte %d, where the file reads %q and not %q",
				strategy.ErrPositionMismatch, "services.web.labels.team", 1204, ` "x`, `"x"`),
		},
		{
			// internal/strategy/structural.go, locate's line-count assertion:
			// `%w: the parser puts this key on line %d and the file has %d
			// lines`. Unrelated to runes — this is the CRLF producer, and a
			// classification that only covered the first would be wrong here.
			site: "locate line-count assertion",
			err: fmt.Errorf("%w: the parser puts this key on line %d and the file has %d lines",
				strategy.ErrPositionMismatch, 40, 12),
		},
		{
			// Bare, and wrapped a second time by a caller. Refused and Reason
			// both go through errors.Is, and this is what says so rather than
			// assuming it.
			site: "the sentinel itself",
			err:  strategy.ErrPositionMismatch,
		},
		{
			site: "wrapped twice",
			err: fmt.Errorf("applying operation 2: %w",
				fmt.Errorf("%w: the parser puts this key on line 40 and the file has 12 lines",
					strategy.ErrPositionMismatch)),
		},
	} {
		if !errors.Is(tc.err, strategy.ErrPositionMismatch) {
			t.Fatalf("%s: the fixture error does not carry the sentinel", tc.site)
		}
		if !Refused(tc.err) {
			t.Errorf("%s: Refused is false, so serve.go sends codeEditFailed (-32004) and the "+
				"reader is told the tool broke when the tool declined", tc.site)
		}
		if got := Reason(tc.err); got != "position-mismatch" {
			t.Errorf("%s: Reason is %q, want %q — without a slug the panel has no arm and "+
				"falls through to the engine's own sentence", tc.site, got, "position-mismatch")
		}
	}
}

// The guards this classification exists for are still in the engine.
//
// Without this, story 6.4 or a later parser swap could delete either assertion
// and the test above would keep passing on errors nobody can raise — a slug for
// a refusal that no longer exists, and no signal that the safety net went with
// it. It reads the source because that is the only place the guard is visible
// from here: internal/edit cannot reach either call site with an input.
func TestBothPositionMismatchGuardsAreStillInTheEngine(t *testing.T) {
	// Matched on the sentinel appearing as an ARGUMENT — `ErrPositionMismatch,`
	// — which is what a `fmt.Errorf("%w: …", ErrPositionMismatch, …)` looks like
	// and what neither the `var` declaration nor a prose mention of it does. The
	// wording of either message is free to change without touching this.
	for _, file := range []string{
		filepath.Join("..", "strategy", "strategy.go"),
		filepath.Join("..", "strategy", "structural.go"),
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "ErrPositionMismatch,") {
			t.Errorf("%s no longer raises ErrPositionMismatch: the guard that makes a "+
				"position disagreement a refusal instead of a splice at a guessed offset "+
				"is gone, and story 6.5's slug now names nothing", file)
		}
	}
}
