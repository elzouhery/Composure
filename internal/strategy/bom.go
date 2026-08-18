package strategy

import "bytes"

// Two things goccy gets wrong about a file's own bytes, and the one place they
// are corrected.
//
// This engine uses the parser to LOCATE bytes and nothing else, so a parser
// position that does not describe the file is not a cosmetic problem — it is
// the confident wrong answer this repository's whole test strategy is built
// around. Both defects below made real, legal files unaddressable or, worse,
// addressable at the wrong offset.
//
// 1. THE UTF-8 BYTE ORDER MARK.
//
// A BOM is legal at the start of a YAML stream and editors on Windows write
// one. yaml.v3 skips it, which is why internal/resolve reads a BOM'd compose
// file perfectly — `composure resolve` and `composure explain` always worked on one.
// goccy folds it into the first token, so `services` becomes "<BOM>services"
// and EVERY path in the file misses, not merely the first: every path starts at
// a top-level key on the first line. `services.web.image` came back "path not
// found", or, where the first line is a comment, as a parse error at 2:1.
//
// 2. CRLF LINE ENDINGS UNDER COMMENT LINES.
//
// goccy counts a comment line in a CRLF document as TWO lines. A file with
// three leading comments reports every position below them three lines too far
// down. This one is not a refusal: on a short file the derived offset runs off
// the end and errors, and on a long enough file it lands somewhere real and
// wrong. `strategy.Range` indexed straight past the end of its line table and
// PANICKED on a four-line fixture. It is entirely independent of the BOM — a
// plain CRLF file with two comment lines and no mark reproduces it — and it
// predates this change.
//
// THE CORRECTION, and why it is only three lines of arithmetic.
//
// The mark is a fixed three-byte prefix introducing no newline; CRLF differs
// from LF only in a byte at the END of each line. So:
//
//   - LINE numbers and COLUMNS within a line are the same in the normalised
//     buffer and the real one. That is what makes this safe: nothing has to be
//     mapped, only offset.
//   - The parser is handed the normalised buffer, so it counts lines the way
//     the file does.
//   - Byte offsets are then derived from those line/column positions against
//     the REAL buffer (offsetOf walks the real bytes), and shifted past the
//     mark. No offset is ever taken from the normalised copy.
//
// Nothing about the splice changes. The mark and every "\r" stay in the buffer,
// no operation rewrites them, and scalarSpan asserts that the bytes at the
// derived offset ARE the lexeme the parser read — so a position that still
// disagrees with the file is refused rather than spliced (ErrPositionMismatch).
//
// The corpus contains no BOM'd file and no CRLF file at all, so none of this
// moves the 98.63% identity figure in either direction. It was verified by
// running the gate before and after.
var bomPrefix = []byte{0xEF, 0xBB, 0xBF}

// parseView splits src into the three things every locator needs.
//
//	addr  — src with any byte order mark removed. ALL byte offsets are derived
//	        against this buffer, because it is the file's real bytes.
//	parse — addr with CRLF normalised to LF. ONLY the parser sees this. No
//	        offset may be taken from it; its line and column numbers are what it
//	        is for.
//	shift — what to add to an offset in addr to get an offset in src.
//
// Returning three values rather than quietly re-basing is deliberate: a caller
// has to say which coordinate system it is in, and one that confuses them gets
// an offset that is right on every file the corpus contains and wrong on the
// Windows ones — which is precisely the failure this engine specialises in.
func parseView(src []byte) (parse, addr []byte, shift int) {
	addr, shift = trimBOM(src)
	parse = addr
	if bytes.Contains(addr, []byte("\r\n")) {
		parse = bytes.ReplaceAll(addr, []byte("\r\n"), []byte("\n"))
	}
	return parse, addr, shift
}

// trimBOM returns src without a leading byte order mark, and the number of
// bytes removed.
func trimBOM(src []byte) (body []byte, shift int) {
	if bytes.HasPrefix(src, bomPrefix) {
		return src[len(bomPrefix):], len(bomPrefix)
	}
	return src, 0
}

// bomLen is the length of the mark at the start of src, or zero.
func bomLen(src []byte) int {
	_, n := trimBOM(src)
	return n
}

// afterBOM moves a range start past the mark.
//
// It exists for the line-oriented ranges, whose first line begins at byte 0 —
// the mark is on that line, and it belongs to the FILE rather than to the key
// written on it. A delete that took it would silently change the file's
// encoding declaration, which is a byte nobody asked to change.
func afterBOM(src []byte, start int) int {
	if n := bomLen(src); start < n {
		return n
	}
	return start
}
