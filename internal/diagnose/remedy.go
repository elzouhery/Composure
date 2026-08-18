package diagnose

// Story 9.5 — the plaintext-credential rule offers the move as its own remedy.
//
// DECISIONS.md 25 recorded the meeting point and why it could not be taken:
// `composure extract` accepts the same config path the credential finding anchors,
// so the finding and the remedy are ONE ADDRESS — and `Fix` had no field for
// "and write this to the `.env`". DECISIONS.md 26 is the field.
//
// Two things are worth stating about the shape of this file, because both are
// the kind of thing a later reader would "simplify" away.
//
// IT ASKS internal/edit RATHER THAN REIMPLEMENTING IT. Whether a value can be
// written as one `.env` line is decided by READBACK through
// `resolve.ParseDotEnv` — three candidate spellings, the first that returns the
// value unchanged wins — and that property lives in `internal/edit/dotenv.go`
// because that is where the line gets written. Asking it costs one import;
// copying it costs a second answer to a format question, which
// `internal/edit/dotenv.go`'s own header says is how a `.env` reader and a
// `.env` writer come to disagree. The import is a leaf and cycles nothing:
// `internal/edit` depends on dockerfile, resolve and strategy, and on nothing
// in this package. Checked with `go list -deps`, not assumed.
//
// AND IT STILL WRITES NOTHING. `edit.EnvLine` is pure — it renders a line and
// returns it or a typed refusal — and what this file produces is a Remedy,
// which is data. Phase 1 has no write path and this does not give it one:
// offering a fix stages nothing (DECISIONS.md 17), and the reader chooses.

import (
	"fmt"
	"path/filepath"

	"github.com/elzouhery/composure/internal/edit"
)

// extractRemedy is the `composure extract` half of a credential fix, or nil.
//
// nil in three cases, and each is a refusal rather than a gap:
//
//   - there is no fix to hang it off, so there is nothing to complete;
//   - the variable name is not one Compose would interpolate — which cannot
//     happen while envVarName produces it, and is checked rather than assumed
//     because "cannot happen" is how the four corpus bugs were described;
//   - the value cannot be written as one `.env` line that reads back as itself.
//     `composure extract` refuses exactly this, and an offer the operation would
//     refuse is worse than no offer: it costs the reader a command, a failure
//     and the belief that the tool knows what it is talking about.
//
// What it deliberately does NOT do is look at the `.env` on disk. A `.env`
// already defining that name with a different value makes `composure extract`
// refuse, and it is right to — but a diagnostic that reads files outside the
// project it was handed is a different thing from a diagnostic, and the
// operation re-derives everything anyway. The offer is an offer.
func extractRemedy(fix *Fix, name, value string) *Remedy {
	if fix == nil || fix.Operation != FixReplaceScalar {
		return nil
	}
	// The readback, through the one implementation of it (see the head of this
	// file). The line itself is discarded: what is wanted is the ANSWER.
	if _, err := edit.EnvLine(name, value); err != nil {
		return nil
	}
	at := fix.Path.String()
	if at == "" || fix.File == "" {
		return nil
	}
	// The `.env` beside the file the fix edits. Never an `env_file`: Compose
	// does not consult one for interpolation, so a value written there produces
	// a `${VAR}` that resolves to empty (DECISIONS.md 25).
	envPath := filepath.Join(filepath.Dir(fix.File), ".env")
	return &Remedy{
		Operation: RemedyExtract,
		At:        at,
		Name:      name,
		File:      envPath,
		// -name is passed explicitly rather than left to be derived. The rule's
		// envVarName and edit's deriveVarName are different functions, and a
		// remedy that advertises one variable while the operation writes another
		// is precisely the drift this field exists to close.
		Command: fmt.Sprintf("composure extract -at %s -name %s -write %s", at, name, fix.File),
		Describe: fmt.Sprintf("move the literal into ${%s} and write %s=… to %s. Nothing is written until you run it",
			name, name, filepath.Base(envPath)),
	}
}
