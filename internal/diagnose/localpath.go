package diagnose

import (
	"fmt"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// Merged paths and file paths are not the same paths.
//
// A rule reasons about the MERGED model and carries a merged resolve.Path. A
// fix has to name a byte range in ONE file, and strategy locates by path against
// that one file's bytes. Handing a merged path to a single file's locator is
// only safe for the segments the merge preserves.
//
// Mapping keys are preserved: `services.web.image` addresses the same thing in
// the merged model and in whichever file wrote it. SEQUENCE INDICES ARE NOT.
// The Compose merge rules append sequences, so `environment[1]` in the merged
// model is `environment[0]` of the second file — or an entirely different entry
// of the first. The same shift happens inside a single file whenever `extends:`
// or an `include:` chain contributes a sequence, so "one file" is not a
// sufficient condition either.
//
// This was reproduced end to end before the fix. Given a compose.yaml that
// includes a db.yaml, both declaring `services.app.environment`, a
// plaintext-credential finding correctly anchored at compose.yaml:7:9
// (`- DB_PASSWORD=hunter2`) described a replace_scalar over bytes 108-111 —
// which are `B=2`, two lines below. Applying it would have destroyed an
// unrelated variable, left the credential in the file, and returned success.
//
// Three options were weighed.
//
//   - Locate against the merged document. There is no merged document: the
//     merged model has no bytes and never will, because splicing the original
//     buffer is the entire product (CLAUDE.md's one rule).
//   - Refuse every fix whose path holds a sequence index when the project has
//     more than one source file. Safe, and it was the fallback — but it drops
//     the commonest fix in the corpus for any project with an override file, and
//     it misses the single-file `extends:` case entirely, which is the same bug
//     with a different cause.
//   - Resolve the merged path back to the path the value occupies in its own
//     file. Implemented here, with refusal as its failure mode.
//
// The resolution deliberately uses strategy's OWN locator rather than a second
// parse of the file. Two reasons, and the second is the one that matters.
//
// First, correctness of the answer: the candidate chosen is the one whose
// located position matches the Origin, so the path handed to ScalarRange or
// DeleteRange has already been proved by the same code that will compute the
// range. AD-14 asks for exactly this — the range a finding reports has to be the
// range the splice engine edits, and the only way to guarantee that is to derive
// both from the same functions.
//
// Second, an attempt to do this by re-resolving the file on its own is wrong in
// a way that is not obvious and that was caught here only by the reproduction
// itself: resolve.BytesWith EXPANDS `include:`. Handed compose.yaml's bytes it
// goes back to disk, pulls db.yaml in again, and hands back the same merged
// sequence the caller was trying to escape — the "single file" model is the
// merged model with a different name, the remap silently agrees with the bug,
// and the fix still names `B=2`.

// localPath maps a path in the merged model to the path the same node occupies
// in the single source file its Origin names.
//
// It returns an error rather than a guess. Mapping segments are taken literally,
// because the merge preserves them. Every sequence index is re-derived: the
// candidates are enumerated against the file and the one whose located line is
// the line the Origin names is the answer. No candidate, or more than one, is a
// refusal — an alias expanded at three sites genuinely has three merged paths
// and one file position, and picking one of them would be the confident wrong
// answer this engine specialises in.
//
// THE AMBIGUITY REFUSAL IS THE ONLY DEFENCE, AND IT IS NOT REDUNDANT.
//
// The instinct is that disowned() (fix.go) backstops this: a fix that names
// bytes it does not own is thrown out in Run, whatever a rule computed. It does
// not backstop this one, and the reason is structural rather than a gap to
// close. disowned's oracle compares a LINE NUMBER — Fix.Range.Line, taken from
// the Origin — against a BYTE OFFSET, Fix.Range.Start. Two candidates on the
// SAME LINE satisfy that relation identically, so the guard accepts the wrong
// one exactly as readily as the right one. Flow style is what puts them there:
//
//	environment: [A=1, SECRET=hunter2]
//
// Replacing this arm with `return hits[0], nil` reproduces the F3.1 data loss
// in full — a replace_scalar over `A=1`, the credential left in the file, and
// success returned. TestNoFixIsDescribedOverAnotherEntryOnTheSameLine and
// TestLocalPathRefusesTwoCandidatesOnOneLine pin both halves.
//
// WHY THE ORACLE WAS NOT STRENGTHENED. The obvious tie-break is the column:
// resolve.Origin carries one, and strategy.Locate returns one. Measured, they
// agree for a SEQUENCE ENTRY (both name the entry's own token) and disagree for
// a MAPPING KEY, where the Origin names the VALUE's column and Locate names the
// KEY's — `services.web.image` is Origin column 12 and Locate column 5. A
// tie-break on a coordinate that means two different things depending on node
// kind is the confident wrong answer wearing a guard's clothes: it would turn a
// safe refusal into a probably-right edit, on the one code path where nothing
// downstream can check the result. Carrying a column into Fix.Range instead
// would be a wire change across cmd/ and extension/ for the same doubtful gain.
//
// So the refusal stays as the single defence. It costs a fix nobody can compute
// safely anyway, which is CLAUDE.md rule 6 working as designed.
func (c *context) localPath(src []byte, merged resolve.Path, origin resolve.Origin) (resolve.Path, error) {
	if origin.Line < 1 {
		return nil, fmt.Errorf("the finding's origin names no line")
	}
	var hits []resolve.Path
	findLocal(src, nil, merged, origin, &hits)
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no node of %s at line %d answers to it", origin.File, origin.Line)
	default:
		return nil, fmt.Errorf("%d nodes of %s answer to it at line %d", len(hits), origin.File, origin.Line)
	}
}

// findLocal walks the merged path against one file's bytes.
//
// A mapping segment is taken as written; the merge keeps keys. An index segment
// is discarded and every index the file actually has is tried in its place,
// because the merged index means nothing here. A complete candidate is a hit
// only when strategy locates it on the line the Origin names.
func findLocal(src []byte, prefix, want resolve.Path, target resolve.Origin, hits *[]resolve.Path) {
	if len(want) == 0 {
		if line, _, err := strategy.Locate(src, prefix); err == nil && line+1 == target.Line {
			*hits = append(*hits, prefix)
		}
		return
	}
	seg := want[0]
	if !isIndex(seg) {
		// A key. If the file does not have it, this branch is dead — the value
		// came from another file, or through an extends the file does not spell.
		p := prefix.Child(seg)
		if _, _, err := strategy.Locate(src, p); err != nil {
			return
		}
		findLocal(src, p, want[1:], target, hits)
		return
	}
	// A sequence index. Indices are contiguous, so enumerating until one fails
	// to locate walks exactly the entries this file wrote.
	for i := 0; ; i++ {
		p := prefix.Index(i)
		if _, _, err := strategy.Locate(src, p); err != nil {
			return
		}
		findLocal(src, p, want[1:], target, hits)
		if i > maxSequenceProbe {
			return
		}
	}
}

// maxSequenceProbe bounds the enumeration. A compose sequence longer than this
// is not a thing anyone hand-wrote, and an unbounded loop over a file the
// locator is unexpectedly permissive about would hang the diagnostic rather than
// fail it.
const maxSequenceProbe = 4096
