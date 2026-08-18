package diagnose

import (
	"fmt"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// Story 3.8 lives here.
//
// A described fix is three things: the operation, the target Path, and the byte
// range in the file it would touch. The range is derived through strategy's
// locate path from the same resolve.Path the finding already carries (AD-14),
// so the bytes reported now are the bytes story 6.1 splices later. Nothing is
// written: this computes and describes.
//
// Every fix is built by locateFix below, and it does three things in order that
// no rule may skip:
//
//  1. It translates the MERGED path the rule carries into the path the same node
//     occupies in the one file the Origin names (see localpath.go). A merged
//     sequence index does not address the same element in a source file, and
//     handing one to the locator is how a described fix comes to own bytes it
//     has never seen.
//  2. It locates the range through strategy, so the bytes reported are the bytes
//     the splice engine would edit.
// The third thing — that the located bytes fall where the finding says it is —
// is NOT done here. It used to be, and being here was the defect: locateFix is
// a helper, and a helper is something a rule can decline to call. A rule that
// built a Fix literal shipped a range nobody checked, and Run enforced only
// that a value-writing fix carried a value. Both proven: a fix on the wrong
// line, and one running 9000-9999 bytes past the end of a 35-byte file,
// survived Run untouched. No shipped rule does either, which made it latent
// rather than live — and latent is what this repository's characteristic bug
// looks like the day before it ships.
//
// The check now lives in Run (unownedFix, below), which every finding passes
// through by construction rather than by convention.
//
// The functions below all fail the same way — they return nil and a reason. A
// fix that cannot be located is not offered, because a described edit against a
// range the engine would not actually touch is worse than no description at
// all. The commonest cause is honest and worth surfacing: a value that reached
// the model through a `<<: *anchor` does not exist at that path in the file,
// so the locator correctly refuses to find it.

// locator is the shape of a strategy range function.
type locator func(src []byte, path []string) (start, end int, err error)

// locateFix is the one path every described fix goes through.
//
// path is the merged path; the Fix returned carries the FILE path, because a
// Fix is a request against one file and its three fields — File, Path, Range —
// have to be consistent with each other or phase 2 would splice at a path that
// does not produce the range described here.
func (c *context) locateFix(op string, path resolve.Path, origin resolve.Origin, find locator) (*Fix, string) {
	src, ok := c.Sources[origin.File]
	if !ok {
		return nil, sourceUnavailable(origin.File)
	}
	local, err := c.localPath(src, path, origin)
	if err != nil {
		return nil, notThisFile(path, origin, err)
	}
	start, end, err := find(src, local)
	if err != nil {
		return nil, locateFailed(path, err)
	}
	line := origin.Line
	if op == FixInsertKey {
		// An insertion point is the end of the target's block, not the position
		// the finding is anchored at, so the line it reports is the line it
		// actually lands on.
		line = lineAt(src, start)
	}
	return &Fix{
		Operation: op,
		Path:      local,
		File:      origin.File,
		Range:     ByteRange{Start: start, End: end, Line: line},
	}, ""
}

// unownedFix is disowned() enforced where a rule cannot get past it: Run calls
// it for EVERY finding that carries a fix, whether or not the fix came through
// locateFix.
//
// The Origin it checks against is rebuilt from the Fix's own two claims — the
// file it names and the line it reports. That is not circular: Range.Start and
// Range.End are byte offsets, Range.Line is a line number, and the file on disk
// decides whether they describe the same place. A fix that names bytes outside
// the file, or bytes on a line other than the one it claims, is refused.
//
// A fix whose file is not in Sources is refused too, and that is deliberate: an
// unverifiable range is exactly the thing this guard exists to stop shipping.
// It costs nothing real — locateFix cannot produce a fix for a file it could
// not read either.
func (c *context) unownedFix(f *Fix) string {
	if f == nil {
		return ""
	}
	src, ok := c.Sources[f.File]
	if !ok {
		return sourceUnavailable(f.File)
	}
	origin := resolve.Origin{File: f.File, Line: f.Range.Line}
	return disowned(src, f.Operation, origin, f.Range.Start, f.Range.End)
}

// disowned is the invariant no rule can opt out of: a fix must never name a
// range it does not own.
//
// Fix.Range.Line is taken from the Origin the rule anchored at; Start and End
// come from strategy's locator. They are derived independently, so requiring
// them to agree costs nothing and catches the whole class of defect where a fix
// is computed against the wrong node — a merged sequence index applied to one
// file, an `extends:` target, an alias expansion. Across 87 single-file corpus
// fixes it fires zero times; on both reproduced data-loss cases it fires.
//
// WHAT IT CANNOT SEE, stated here so the next reader does not assume it is a
// complete defence: the oracle is a LINE NUMBER against a BYTE OFFSET, so two
// candidate nodes on the SAME LINE are indistinguishable to it by construction.
// Flow style puts them there — `environment: [A=1, SECRET=hunter2]`. That case
// is refused one layer earlier, by localPath's ambiguity arm, and localpath.go
// records why the column cannot be used to close the gap here.
//
// The relation differs by operation, because the operations own different bytes:
//
//   - replace_scalar owns the lexeme, which is on the anchored line exactly.
//   - delete_key owns the key's subtree plus the comments attached above it, so
//     the anchored line must fall inside the range rather than start it.
//   - insert_key owns nothing; it names a point at the end of the target's
//     block, which is at or below the line the target was declared on.
func disowned(src []byte, op string, origin resolve.Origin, start, end int) string {
	if start < 0 || end < start || end > len(src) {
		return fmt.Sprintf("no fix is described: the located range %d-%d is not inside a %d byte file", start, end, len(src))
	}
	first := lineAt(src, start)
	switch op {
	case FixReplaceScalar:
		if first != origin.Line {
			return rangeDisagrees(op, origin, first)
		}
	case FixDeleteKey:
		last := lineAt(src, end)
		if end > start {
			last = lineAt(src, end-1)
		}
		if origin.Line < first || origin.Line > last {
			return rangeDisagrees(op, origin, first)
		}
	case FixInsertKey:
		if first < origin.Line {
			return rangeDisagrees(op, origin, first)
		}
	}
	return ""
}

func rangeDisagrees(op string, origin resolve.Origin, got int) string {
	return fmt.Sprintf("no fix is described: the %s range located in %s begins on line %d but the finding is anchored at line %d, "+
		"so the fix does not own the bytes it would name and describing it would be worse than describing none",
		op, origin.File, got, origin.Line)
}

// replaceScalarFix describes overwriting the scalar at path with value.
//
// value may be empty only when the author has to supply it; say so with
// replaceScalarPlaceholderFix rather than by leaving Value empty, which reads as
// "set it to the empty string" and is applied as one.
func (c *context) replaceScalarFix(path resolve.Path, origin resolve.Origin, value, describe string) (*Fix, string) {
	// ScalarRange, not Range: the range must be the lexeme alone. A range that
	// ran to the end of the line would describe an edit that swallows a
	// trailing comment, and "hunter2      # plaintext credential" is exactly
	// the shape of line this rule fires on.
	fix, why := c.locateFix(FixReplaceScalar, path, origin, strategy.ScalarRange)
	if fix == nil {
		return nil, why
	}
	fix.Value, fix.Describe = value, describe
	return fix, ""
}

// replaceScalarPlaceholderFix describes overwriting the scalar at path with a
// value only the author can choose. Value stays empty and NeedsValue is set, so
// a consumer cannot mistake "you pick" for "set it to nothing".
func (c *context) replaceScalarPlaceholderFix(path resolve.Path, origin resolve.Origin, describe string) (*Fix, string) {
	fix, why := c.replaceScalarFix(path, origin, "", describe)
	if fix != nil {
		fix.NeedsValue = true
	}
	return fix, why
}

// deleteKeyFix describes removing the key at path, with the exact range
// strategy.DeleteKey would remove — subtree, attached comments and all.
func (c *context) deleteKeyFix(path resolve.Path, origin resolve.Origin, describe string) (*Fix, string) {
	fix, why := c.locateFix(FixDeleteKey, path, origin, strategy.DeleteRange)
	if fix == nil {
		return nil, why
	}
	fix.Describe = describe
	return fix, ""
}

// insertKeyFix describes adding `key: value` as a child of the mapping at path.
// The range is empty at the insertion point: an insert touches no existing
// byte, and reporting a non-empty range would misdescribe what phase 2 does.
//
// needsValue marks an insert whose body the author has to write. Applying it
// verbatim would produce `key:` with a null value, which is valid YAML and
// invalid compose — exactly the failure the flag exists to make visible.
func (c *context) insertKeyFix(path resolve.Path, origin resolve.Origin, key, value string, needsValue bool, describe string) (*Fix, string) {
	insertion := func(src []byte, p []string) (int, int, error) {
		offset, _, err := strategy.InsertionPoint(src, p)
		return offset, offset, err
	}
	fix, why := c.locateFix(FixInsertKey, path, origin, insertion)
	if fix == nil {
		return nil, why
	}
	fix.Key, fix.Value, fix.NeedsValue, fix.Describe = key, value, needsValue, describe
	return fix, ""
}

func sourceUnavailable(file string) string {
	return fmt.Sprintf("no fix is described: %s could not be read, so the byte range a fix would touch is unknown", file)
}

func notThisFile(path resolve.Path, origin resolve.Origin, err error) string {
	return fmt.Sprintf("no fix is described: %s could not be located in %s as the file itself writes it (%s). %s addresses the MERGED "+
		"model, where a sequence index counts entries this file did not write; describing an edit against the wrong element is how a "+
		"fix destroys an unrelated value",
		path, origin.File, err, path)
}

func locateFailed(path resolve.Path, err error) string {
	return fmt.Sprintf("no fix is described: the splice engine could not locate %s (%v), and describing an edit against a range it would not touch is worse than describing none", path, err)
}

func lineAt(src []byte, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	line := 1
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}
