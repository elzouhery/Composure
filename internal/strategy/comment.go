package strategy

// Comments, as things that can be authored — story 9.1.
//
// Comments are the whole thesis of this engine: the splice exists so that
// changing a port does not move the sentence above it. They have therefore been
// preserved and untouchable, which is a strange place to leave the one artefact
// the product is built around.
//
// Two decisions carry the whole file, and both are reuse rather than invention:
//
//	A comment is addressed by (config path, position), and position is one of
//	exactly two values. `above` is the contiguous run of comment lines directly
//	above the key at the key's own indent — which is attachedCommentStart's
//	definition, the one DeleteKey has always used to decide which comments
//	travel with a deleted node. `trailing` is the `#…` after the value on the
//	key's own line. There is no third position and no free-floating comment: a
//	comment that belongs to nothing has no path to be addressed by, and "line
//	14" is an address that moves the instant anything above it is edited.
//
//	A run of comment lines is ONE comment. Same reason: attachedCommentStart
//	already treats it as one thing and DeleteKey already removes it as one, so a
//	per-line address would be a second model of the same bytes and the two would
//	disagree the day somebody edited between them.
//
// POSITIONS COME FROM THE BUFFER, NOT FROM GOCCY. This repository has measured
// goccy miscounting a comment line in a CRLF document — the defect that produced
// ErrPositionMismatch — so nothing here reads a goccy comment token. The run is
// found by scanLines; the trailing comment is found by scanning the key's own
// line FORWARD FROM THE END OF THE VALUE, where the value's end comes from
// scalarSpan, which already asserts that the bytes at the computed offset are
// the lexeme the parser read.
//
// Where it could NOT be buffer-derived, said plainly: the key's own anchor line
// still comes from `locate`, which is goccy. It is guarded by that same
// ErrPositionMismatch assertion and by nothing else.

import (
	"fmt"
	"strings"

	goccyparser "github.com/goccy/go-yaml/parser"
)

// The two positions a comment can occupy relative to a key.
const (
	// CommentAbove is the contiguous run of comment lines directly above the
	// key, at the key's own indent. A blank line breaks the run.
	CommentAbove = "above"
	// CommentTrailing is the comment after the value on the key's own line.
	CommentTrailing = "trailing"
)

// ErrNoComment is returned when a delete is asked for at a position that has no
// comment.
//
// It is a refusal rather than a no-op, and rather than ErrNoChange, because
// "there was nothing to delete" and "the delete did nothing" are different
// sentences and the reader acts differently on them. A silent success here
// would tell someone their comment is gone when it is still in the file, three
// lines up, attached to something else.
var ErrNoComment = fmt.Errorf("there is no comment at that position; refusing rather than reporting a delete that removed nothing")

// ErrCommentText is returned when the offered text would not be written as one
// comment holding exactly that text.
//
// Same property test the rest of this engine applies to new content — entryText
// for a sequence entry, edit.bare for a value: the text is checked in the
// position it will occupy rather than against a character blocklist.
var ErrCommentText = fmt.Errorf("that text would not be written as a comment; refusing rather than writing a line the reader did not ask for")

// ErrCommentTarget is returned when the engine cannot say where a line's value
// ends, so it cannot say where a trailing comment would begin.
//
// A block scalar, a value that is an alias, a multi-line plain scalar, an
// unknown position name. In each of them the only way to place a `#` is to
// guess, and a guess writes a comment marker into somebody's value. Flow
// collections get ErrFlowStyle, which is the sentinel this engine already
// carries for "that shape cannot take block content".
var ErrCommentTarget = fmt.Errorf("that line cannot carry a trailing comment safely; refusing rather than guessing where its value ends")

// commentSite is everything both the write and the range functions need about
// one (path, position). One computation, two exports — AD-14, the same shape
// Range and DeleteRange already have.
type commentSite struct {
	// found reports whether a comment is there now.
	found bool
	// start and end are the span a SET replaces: the comment's own bytes,
	// leaving the whitespace in front of a trailing one alone. Empty and equal
	// at the insertion point when found is false.
	start, end int
	// delStart is where a DELETE begins. For a trailing comment it is the end
	// of the value, so the gap goes with the comment and no trailing whitespace
	// is left on the line.
	delStart int
	// prefix is the marker and its spacing, copied from the comment that is
	// there — `#` or `# ` or `#\t` — so replacing one does not restyle it.
	prefix string
	// indent and ending are what a new `above` line needs.
	indent int
	ending string
}

// CommentRange is the half-open byte range SetComment would overwrite at
// (path, where), and whether a comment is there now. An empty range means there
// is nothing there and the new comment is inserted at that offset.
//
// It is derived through the same function SetComment writes through, refusals
// included, so a preview cannot report a range — or an acceptance — the write
// disagrees with.
func CommentRange(src []byte, path []string, where string) (start, end int, found bool, err error) {
	site, err := commentAt(src, path, where)
	if err != nil {
		return 0, 0, false, err
	}
	return site.start, site.end, site.found, nil
}

// CommentDeleteRange is the half-open byte range DeleteComment would remove.
// It refuses with ErrNoComment when there is nothing at that position, exactly
// as the write does.
func CommentDeleteRange(src []byte, path []string, where string) (start, end int, err error) {
	site, err := commentAt(src, path, where)
	if err != nil {
		return 0, 0, err
	}
	if !site.found {
		return 0, 0, fmt.Errorf("%w: %s of %s", ErrNoComment, where, strings.Join(path, "."))
	}
	return site.delStart, site.end, nil
}

// SetComment writes text as the comment at (path, where), replacing whatever is
// there or inserting one where there is none.
func (Splice) SetComment(src []byte, path []string, where, text string) ([]byte, error) {
	site, err := commentAt(src, path, where)
	if err != nil {
		return nil, err
	}
	body, err := commentBody(text, where, site.prefix)
	if err != nil {
		return nil, err
	}
	return spliceRange(src, site.start, site.end, site.render(body, where)), nil
}

// DeleteComment removes the comment at (path, where).
func (Splice) DeleteComment(src []byte, path []string, where string) ([]byte, error) {
	site, err := commentAt(src, path, where)
	if err != nil {
		return nil, err
	}
	if !site.found {
		return nil, fmt.Errorf("%w: %s of %s", ErrNoComment, where, strings.Join(path, "."))
	}
	return spliceRange(src, site.delStart, site.end, ""), nil
}

// render is the bytes that go into the span.
//
// The lines arrive from commentBody with their marker already on them — the
// marker is part of deciding what the line SAYS (`## section` is the reader's
// own doubled one and `# ` is this engine's), so it is decided in one place
// rather than half here and half there. All render adds is the indent and the
// file's own line ending.
func (s commentSite) render(body []string, where string) string {
	if where == CommentTrailing {
		// One line by construction — commentBody has already refused more.
		if s.found {
			return body[0]
		}
		// A NEW trailing comment gets exactly one space before the `#`: the
		// minimum YAML requires for a comment to be a comment, and no
		// formatting beyond it. Two spaces is a house style and this engine
		// does not have one.
		return " " + body[0]
	}
	var b strings.Builder
	pad := strings.Repeat(" ", s.indent)
	for _, line := range body {
		b.WriteString(pad)
		b.WriteString(line)
		b.WriteString(s.ending)
	}
	return b.String()
}

func spliceRange(src []byte, start, end int, text string) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(text))
	out = append(out, src[:start]...)
	out = append(out, text...)
	out = append(out, src[end:]...)
	return out
}

// commentBody turns the reader's text into the comment lines that go into the
// file — marker included, indent excluded.
//
// prefix is the marker this site writes: the one copied off the comment that is
// already there (`#`, `# `, `#\t`) or `# ` for a new one.
//
// Three rules, and the second and third are the ones a review found missing:
//
//   - The reader's own `#` is theirs and is not doubled. One leading `#` and
//     one space after it are stripped, then the site's marker is written.
//     `#one` goes in as `# one` and not as `# #one`.
//   - A marker the reader DOUBLED is a different marker, and it is written
//     exactly as they wrote it. `## mine` is a section header in real files,
//     and stripping one of the two while writing one back meant `## mine` came
//     out as `# # mine` — so a `##` comment could not survive being read and
//     written back unchanged, which is what the pane does when somebody opens
//     a comment and presses Enter. extension/host/edit.ts `commentText` keeps
//     a doubled marker for this reason; the two halves are one rule.
//   - A BLANK line inside a run is a bare marker. `# ` would leave a trailing
//     space on a line nobody typed one on, and trailing whitespace is a change
//     to a line this operation was not asked to change.
//
// Everything else is written verbatim — a comment is the one place in this file
// format where the bytes mean nothing to a parser and everything to a person.
func commentBody(text, where, prefix string) ([]string, error) {
	if strings.Contains(text, "\r") {
		return nil, fmt.Errorf("%w: the text carries a carriage return of its own, and a line ending is the FILE's to choose, not the comment's", ErrCommentText)
	}
	if where == CommentTrailing && strings.Contains(text, "\n") {
		return nil, fmt.Errorf("%w: a trailing comment is one line, and a second line would be written into the mapping as a comment attached to the next key. Put it above the key instead", ErrCommentText)
	}
	bare := strings.TrimRight(prefix, " \t")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	empty := true
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		switch {
		case strings.TrimSpace(l) == "" || strings.TrimSpace(l) == "#":
			// Nothing on this line. It stays a line — a blank inside a run is
			// the reader's paragraph break — and it carries the marker alone.
			out = append(out, bare)
			continue
		case strings.HasPrefix(l, "##"):
			out = append(out, l)
		default:
			body := strings.TrimPrefix(l, "#")
			if body != l {
				body = strings.TrimPrefix(body, " ")
			}
			out = append(out, prefix+body)
		}
		empty = false
	}
	if empty {
		return nil, fmt.Errorf("%w: there is nothing to write. Removing a comment is `delete_comment`, which says so", ErrCommentText)
	}
	return out, nil
}

// commentAt answers everything about one (path, position).
func commentAt(src []byte, path []string, where string) (commentSite, error) {
	lines := scanLines(src)
	idx, col, err := locate(src, path)
	if err != nil {
		return commentSite{}, err
	}
	switch where {
	case CommentAbove:
		indent, err := runIndent(lines, idx, col, path)
		if err != nil {
			return commentSite{}, err
		}
		return aboveSite(src, lines, idx, indent), nil
	case CommentTrailing:
		return trailingSite(src, lines, path, idx)
	}
	return commentSite{}, fmt.Errorf("%w: %q is not a comment position; it is %q or %q",
		ErrCommentTarget, where, CommentAbove, CommentTrailing)
}

// runIndent is the indent the comment run above this node sits at — and it is
// read off the BUFFER, which is the rest of this file's rule applied to the one
// number that was still coming out of goccy.
//
// `locate` answers with the COLUMN OF THE TOKEN, and for an entry of a block
// sequence that token is the VALUE, not the `-`:
//
//	ports:
//	  # the public edge
//	  - "8080:80"
//
// The `-` is at column 2 and `"8080:80"` at column 4, so attachedCommentStart
// looked for comment lines at indent 4, found none, and `delete_comment above
// ports[0]` answered `no-comment` with the comment plainly there. `set_comment`
// then wrote a SECOND one two columns deeper, attached to nothing. The pane
// reaches both: inspector.ts hangs the `#` control off every sequence entry.
//
// The line's own content indent is the right answer for a key too — a key that
// starts its line begins exactly at its indent — so this is one rule, not a
// special case for sequences.
//
// It also draws the line D3 found missing. Everything before the token on its
// line must be spacing or a sequence indicator; anything else means the node is
// INSIDE a flow collection — an entry of `test: ["CMD", …]` has no line of its
// own — and there is nowhere above it for a comment to go. Writing one above
// the enclosing line at the entry's column produces a comment owned by nothing,
// which `trailing` already refuses at the same target with ErrFlowStyle. The
// two positions now agree; a refusal at one and a write at the other is two
// answers about one target, which is AD-14 in the small.
func runIndent(lines []lineInfo, idx, col int, path []string) (int, error) {
	if idx < 0 || idx >= len(lines) {
		return 0, fmt.Errorf("%w: the parser puts %s on line %d and the file has %d lines",
			ErrPositionMismatch, strings.Join(path, "."), idx+1, len(lines))
	}
	text := lines[idx].text
	if col < 0 || col > len(text) {
		return 0, fmt.Errorf("%w: the parser puts %s at column %d of a line %d bytes long",
			ErrPositionMismatch, strings.Join(path, "."), col+1, len(text))
	}
	for i := 0; i < col; i++ {
		switch text[i] {
		case ' ', '\t', '-':
		default:
			if isFlowMapping(lines, idx) {
				return 0, ErrFlowStyle
			}
			return 0, fmt.Errorf("%w: %s does not begin its own line, so there is no line above it that belongs to it",
				ErrCommentTarget, strings.Join(path, "."))
		}
	}
	return lines[idx].indent, nil
}

// aboveSite is the run of comment lines directly above the key.
//
// The span is WHOLE LINES — from the start of the first comment line to the
// start of the key's line — so a replacement carries its own line endings and a
// delete leaves no fragment behind. afterBOM is applied for the same reason
// Range and DeleteRange apply it: the mark at the head of line 0 belongs to the
// file and not to whatever is written on that line.
func aboveSite(src []byte, lines []lineInfo, idx, indent int) commentSite {
	first := attachedCommentStart(lines, idx, indent)
	site := commentSite{
		indent: indent,
		ending: endingFor(lines, idx),
		prefix: "# ",
	}
	if first == idx {
		// Nothing there: the insertion point is the head of the key's line.
		site.start = afterBOM(src, lineStart(lines, idx))
		site.end, site.delStart = site.start, site.start
		return site
	}
	site.found = true
	site.start = afterBOM(src, lineStart(lines, first))
	site.end = lineStart(lines, idx)
	site.delStart = site.start
	site.prefix = markerOf(lines[first].text)
	return site
}

// markerOf copies the `#` and the spacing after it from an existing comment
// line, so replacing `#no space` does not restyle it as `# no space`. Nothing
// is normalised — the rule this engine is built on, applied to the one artefact
// it was built to protect.
func markerOf(text string) string {
	t := strings.TrimLeft(text, " ")
	if !strings.HasPrefix(t, "#") {
		return "# "
	}
	rest := t[1:]
	gap := rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]
	if strings.TrimSpace(rest) == "" {
		// `#` alone on the line. A replacement needs a separator or the text
		// welds onto the marker.
		return "# "
	}
	return "#" + gap
}

// trailingSite is the comment after the value on the key's own line.
//
// The scan starts at the END OF THE VALUE, never at the first `#` on the line.
// `note: "a value with a # inside it"   # and a real one` is the case that
// decides it: a first-hash scan writes into somebody's string and the file
// still parses, which is this engine's characteristic failure exactly.
func trailingSite(src []byte, lines []lineInfo, path []string, idx int) (commentSite, error) {
	if isFlowMapping(lines, idx) {
		// `ports: ["80:80"]`. Finding where a flow collection ends means
		// tracking quoting and nesting across the line, and the sentinel for
		// "that shape cannot take block content" already exists.
		return commentSite{}, ErrFlowStyle
	}
	after, err := valueEnd(src, path, lines, idx)
	if err != nil {
		return commentSite{}, err
	}
	lineEnd := contentEnd(lines, idx)
	if after > lineEnd {
		return commentSite{}, fmt.Errorf("%w: the value at %s does not end on its own line",
			ErrCommentTarget, strings.Join(path, "."))
	}

	site := commentSite{prefix: "# ", ending: endingFor(lines, idx)}
	hash := -1
	for i := after; i < lineEnd; i++ {
		switch src[i] {
		case ' ', '\t':
		case '#':
			hash = i
		default:
			// Something the engine did not account for sits between the value
			// and the end of the line. Refusing costs one comment; guessing
			// costs a `#` written into a value.
			return commentSite{}, fmt.Errorf("%w: %q sits between the value at %s and the end of its line",
				ErrCommentTarget, string(src[after:lineEnd]), strings.Join(path, "."))
		}
		if hash >= 0 {
			break
		}
	}
	if hash < 0 {
		site.start, site.end, site.delStart = lineEnd, lineEnd, lineEnd
		return site, nil
	}
	site.found = true
	site.start, site.end = hash, lineEnd
	site.delStart = after
	site.prefix = markerOf(string(src[hash:lineEnd]))
	return site, nil
}

// valueEnd is the byte offset one past the last byte of the value written on
// the key's line, which is where a trailing comment may begin.
//
// Three roads to it, in the order they are trustworthy:
//
//  1. scalarSpan, for a key whose value is a scalar. It already asserts that
//     the bytes at the computed offset ARE the lexeme the parser read, so an
//     offset that survives it is one the buffer agrees with.
//  2. keyColonEnd, for a key whose value is a mapping, a block sequence or
//     nothing at all — `services:` — where the colon is the last byte on the
//     line that belongs to the key.
//  3. refusal, for everything else.
func valueEnd(src []byte, path []string, lines []lineInfo, idx int) (int, error) {
	lineFrom := lineStart(lines, idx)
	lineTo := contentEnd(lines, idx)

	// A block scalar indicator or an alias is read off the buffer rather than
	// off the parser, because in both cases the parser's answer is about
	// something that is not on this line: the block's body, or the anchor's
	// value somewhere else in the file.
	if text := lines[idx].text; true {
		if i := strings.Index(text, ":"); i >= 0 {
			rest := strings.TrimLeft(text[i+1:], " \t")
			switch {
			case strings.HasPrefix(rest, "|"), strings.HasPrefix(rest, ">"):
				return 0, fmt.Errorf("%w: the value at %s is a block scalar, and its body is the lines below",
					ErrCommentTarget, strings.Join(path, "."))
			case strings.HasPrefix(rest, "*"), strings.HasPrefix(rest, "&"):
				return 0, fmt.Errorf("%w: the value at %s carries an anchor or an alias, and this engine does not splice around one",
					ErrCommentTarget, strings.Join(path, "."))
			}
		}
	}

	// Two independent answers, and the ORDER they are trusted in is the whole
	// of this function.
	//
	// The colon is computed first because it is the one that is always right
	// about the LINE: `services:` has a mapping under it, and scalarSpan
	// happily returns that mapping's first key — which is on the line below and
	// is not this key's value at all. The scalar end is preferred only when it
	// is on this line and at or past the colon, which is exactly the case where
	// the key really does carry its value inline.
	colon, colonErr := colonEnd(src, path)
	colonOK := colonErr == nil && colon >= lineFrom && colon <= lineTo

	start, end, _, afterKey, serr := scalarSpan(src, path)
	if serr == nil && afterKey && start >= lineFrom && start <= lineTo {
		// An implicit null — `healthcheck:` with nothing after it. The range is
		// the insertion point just past the colon, which is where the line's
		// own content ends.
		return start, nil
	}
	if serr == nil && !afterKey && end >= lineFrom && end <= lineTo && (!colonOK || end >= colon) {
		return end, nil
	}
	if colonOK {
		return colon, nil
	}
	if serr != nil && colonErr != nil {
		return 0, fmt.Errorf("%w: the engine cannot say where the value at %s ends (%v)",
			ErrCommentTarget, strings.Join(path, "."), colonErr)
	}
	return 0, fmt.Errorf("%w: the value at %s does not begin and end on the line its key is written on",
		ErrCommentTarget, strings.Join(path, "."))
}

// colonEnd is the byte offset one past the `:` that separates the key at path
// from its value, in the ORIGINAL buffer's coordinates.
//
// It is keyColonEnd with the BOM shift applied — the same function the implicit
// null path uses, so "where does the key end" has one implementation here too.
func colonEnd(src []byte, path []string) (int, error) {
	parse, body, shift := parseView(src)
	f, perr := goccyparser.ParseBytes(parse, goccyparser.ParseComments)
	if perr != nil {
		return 0, perr
	}
	if len(f.Docs) == 0 {
		return 0, fmt.Errorf("empty document")
	}
	off, cerr := keyColonEnd(body, f.Docs[0].Body, path)
	if cerr != nil {
		return 0, cerr
	}
	return off + shift, nil
}
