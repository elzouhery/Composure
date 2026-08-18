package strategy

// Locate returns the zero-based line index and indentation of the key
// addressed by path, or an error if the path does not resolve.
//
// It is a thin export of the existing internal locate so that callers outside
// this package — the resolver, the topology layer, and eventually the
// extension — can find the same byte ranges the splice engine edits, using the
// same path type they use everywhere else.
//
// path is []string rather than a named type on purpose: a named slice type
// with this underlying type is assignable to it, so resolve.Path passes
// straight through and this package stays a leaf with no imports from the
// resolver. Keeping it a leaf is what lets the corpus harness exercise the
// engine at 5,000-file scale without dragging the resolver in.
func Locate(src []byte, path []string) (lineIdx, indent int, err error) {
	return locate(src, path)
}

// Range returns the half-open byte range [start, end) that the node at path
// occupies: its own line through the last line of its subtree, excluding the
// newline that terminates the last line.
//
// It exists so that a diagnostic can report the byte range a fix would touch
// without computing extents of its own. Two implementations of "where does
// this node end" is exactly the divergence AD-14 forbids: the range reported
// by a finding has to be the range the splice engine later edits, and the only
// way to guarantee that is to derive both from the same functions.
func Range(src []byte, path []string) (start, end int, err error) {
	idx, indent, err := locate(src, path)
	if err != nil {
		return 0, 0, err
	}
	lines := scanLines(src)
	last := subtreeEnd(lines, idx, indent)
	// afterBOM: the first line of a BOM'd file begins with three bytes that
	// belong to the file rather than to the key on it. See bom.go.
	return afterBOM(src, lineStart(lines, idx)), lineStart(lines, last) + len(lines[last].text), nil
}

// DeleteRange returns the half-open byte range DeleteKey would remove: the
// node's subtree, the comment lines attached above it, and — when the node sits
// between two blank lines — one of those blanks.
//
// It mirrors DeleteKey line for line rather than approximating it, and
// TestDeleteRangeMatchesDeleteKey asserts that splicing this range out of the
// source produces exactly what DeleteKey produces. A finding that reported a
// range phase 2 would not touch is worse than one that reported none.
func DeleteRange(src []byte, path []string) (start, end int, err error) {
	idx, indent, err := locate(src, path)
	if err != nil {
		return 0, 0, err
	}
	lines := scanLines(src)
	first := attachedCommentStart(lines, idx, indent)
	last := subtreeEnd(lines, idx, indent)
	if first > 0 && lines[first-1].blank &&
		last+1 < len(lines) && lines[last+1].blank {
		last++
	}

	// afterBOM, for the same reason Range applies it: a delete of the file's
	// first key must not take the byte order mark with it. Dropping it would
	// change the file's encoding declaration, which is a byte nobody asked to
	// change and the kind of silent rewrite this engine exists not to do.
	start = afterBOM(src, lineStart(lines, first))
	if last+1 >= len(lines) {
		// Deleting through the final line takes the newline that separated it
		// from the line above with it — otherwise the result would end in a
		// stray blank line the source did not have.
		//
		// bomLen, not 0: on a BOM'd file whose whole body is the node being
		// deleted, start is already sitting just past the mark and there is no
		// preceding newline to absorb. Decrementing from there would end the
		// splice in the MIDDLE of the three-byte mark and leave two orphan bytes
		// at the head of the file — a worse encoding rewrite than dropping it.
		if start > bomLen(src) {
			start--
		}
		return start, sourceLength(lines), nil
	}
	return start, lineStart(lines, last+1), nil
}

// InsertionPoint returns the byte offset at which InsertKey would add a child
// of the mapping at path, and the indentation it would use. The range a fix
// describes is empty at that offset: an insert touches no existing byte.
// An EMPTY path is the document root (story 7.2). It is answered by
// rootInsertAnchor — the same function InsertKey writes through, so the offset
// the preview shows is the offset the write uses (AD-14), including its refusals.
func InsertionPoint(src []byte, path []string) (offset, indent int, err error) {
	lines := scanLines(src)
	if len(path) == 0 {
		after, ci, err := rootInsertAnchor(src, lines)
		if err != nil {
			return 0, 0, err
		}
		return contentEnd(lines, after), ci, nil
	}
	idx, parentIndent, err := locate(src, path)
	if err != nil {
		return 0, 0, err
	}
	if isFlowMapping(lines, idx) {
		return 0, 0, ErrFlowStyle
	}
	last := subtreeEnd(lines, idx, parentIndent)
	// contentEnd, not lineStart+len(text): on a CRLF file the "\r" is part of
	// the line's text and the insertion point sits BEFORE it. Reporting the
	// offset after the CR would describe a splice that lands between a CR and
	// its LF — which is not where InsertKey writes, and AD-14 exists to stop the
	// preview and the write disagreeing about that.
	return contentEnd(lines, last), childIndent(lines, idx, parentIndent), nil
}

// lineStart is the byte offset of line i in the source scanLines was given.
// scanLines splits on "\n" and joinLines rejoins on "\n", so a line's length
// plus one is exactly its span — a CRLF file keeps its "\r" inside text and the
// arithmetic still holds.
func lineStart(lines []lineInfo, i int) int {
	off := 0
	for k := 0; k < i && k < len(lines); k++ {
		off += len(lines[k].text) + 1
	}
	return off
}

func sourceLength(lines []lineInfo) int {
	if len(lines) == 0 {
		return 0
	}
	return lineStart(lines, len(lines)-1) + len(lines[len(lines)-1].text)
}

// SequenceInsertionPoint returns the byte offset at which InsertSequenceEntry
// would add an entry to the sequence at path, and the column its `-` would sit
// at. The range a fix describes is empty at that offset: an insert touches no
// existing byte.
//
// It is derived through sequenceAnchor, the same function the write goes
// through, so the range a preview shows is the range the write touches (AD-14)
// — refusals included.
func SequenceInsertionPoint(src []byte, path []string) (offset, indent int, err error) {
	lines := scanLines(src)
	after, indent, _, err := sequenceAnchor(src, lines, path)
	if err != nil {
		return 0, 0, err
	}
	return contentEnd(lines, after), indent, nil
}
