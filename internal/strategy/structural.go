package strategy

// Structural edits — insert and delete.
//
// Scalar replacement is the easy case: the byte range of the old value is
// known, so you overwrite it. Insertion and deletion are materially harder
// because they change the shape of the document, which means you have to
// answer two questions the parser will not answer for you:
//
//	1. Where does a node's subtree END? Parsers report where tokens start.
//	   Nothing reports "this service occupies lines 12 through 27".
//	2. What indentation should new content use? YAML has no canonical answer.
//	   The only correct choice is whatever the surrounding file already does,
//	   which must be inferred from siblings — and files are inconsistent.
//
// Getting either wrong corrupts the document silently: a delete that stops one
// line short orphans a fragment, and an insert at the wrong indent silently
// reparents a key into the previous service.
//
// The approach here is line-oriented. The parser locates the anchor line, then
// indentation structure determines the extent. This is the same information a
// human uses when reading YAML, and it survives constructs a strict AST walk
// would need special cases for.

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	goccyast "github.com/goccy/go-yaml/ast"
	goccyparser "github.com/goccy/go-yaml/parser"
	goccytoken "github.com/goccy/go-yaml/token"
	"gopkg.in/yaml.v3"
)

// Structural is implemented by engines that can add and remove nodes, not just
// change scalar values.
type Structural interface {
	// InsertKey adds `key: value` into the mapping at path, appended after the
	// mapping's existing children.
	InsertKey(src []byte, path []string, key, value string) ([]byte, error)
	// DeleteKey removes the key at path, together with its entire subtree and
	// any comment lines attached directly above it.
	DeleteKey(src []byte, path []string) ([]byte, error)
	// InsertSequenceEntry appends `- value` to the block sequence at path,
	// after the sequence's existing entries.
	InsertSequenceEntry(src []byte, path []string, value string) ([]byte, error)
}

// ---------------------------------------------------------------------------
// Line model
// ---------------------------------------------------------------------------

type lineInfo struct {
	text    string
	indent  int
	blank   bool
	comment bool // a line consisting only of a comment
}

func scanLines(src []byte) []lineInfo {
	raw := strings.Split(string(src), "\n")
	out := make([]lineInfo, len(raw))
	for i, l := range raw {
		trimmed := strings.TrimLeft(l, " ")
		info := lineInfo{
			text:   l,
			indent: len(l) - len(trimmed),
			blank:  strings.TrimSpace(l) == "",
		}
		info.comment = !info.blank && strings.HasPrefix(strings.TrimSpace(l), "#")
		out[i] = info
	}
	return out
}

// subtreeEnd returns the index of the last line belonging to the node that
// starts at startIdx with the given indent.
//
// The node ends at the last line before the next line that is neither blank
// nor a comment and whose indent is less than or equal to the node's own.
// Trailing blank lines are excluded — they belong to the gap between siblings,
// not to the node, and swallowing them is how a delete eats a blank line it
// should have left alone.
func subtreeEnd(lines []lineInfo, startIdx, indent int) int {
	last := startIdx
	for i := startIdx + 1; i < len(lines); i++ {
		l := lines[i]
		if l.blank || l.comment {
			continue // undecided: could be interior, could be the gap after
		}
		if l.indent <= indent {
			break
		}
		last = i
	}
	return last
}

// attachedCommentStart walks upward from startIdx over a contiguous run of
// comment lines at the same indent and returns the first one. Those comments
// document the node and must travel with it on delete.
func attachedCommentStart(lines []lineInfo, startIdx, indent int) int {
	first := startIdx
	for i := startIdx - 1; i >= 0; i-- {
		if lines[i].comment && lines[i].indent == indent {
			first = i
			continue
		}
		break
	}
	return first
}

// locate walks path and returns the zero-based line index of the final key and
// its indent.
//
// A segment of all digits addresses an element of a sequence. That case exists
// because a diagnostic has to be able to point at one entry of a `ports:` list
// — `services.web.ports[0]` — and report the byte range phase 2 would splice.
// A mapping key and a sequence element are anchored the same way afterwards:
// the token's line, and the column it starts at.
// The parser sees a normalised view of src — no byte order mark, LF endings —
// so that its line numbers are the file's own. Neither correction changes a
// line number or an in-line column, which is what makes the returned line index
// and indent usable against the ORIGINAL buffer's scanLines. See bom.go.
func locate(src []byte, path []string) (lineIdx, indent int, err error) {
	body, _, _ := parseView(src)
	f, perr := goccyparser.ParseBytes(body, goccyparser.ParseComments)
	if perr != nil {
		return 0, 0, perr
	}
	if len(f.Docs) == 0 {
		return 0, 0, fmt.Errorf("empty document")
	}
	node := f.Docs[0].Body
	var keyTok *struct{ line, col int }

	for _, seg := range path {
		if s, ok := node.(*goccyast.SequenceNode); ok {
			i, numeric, shaped := EntryIndex(seg)
			if !numeric {
				if shaped {
					// `-1`, `+1`, `1 `, or a number too large for an int. The
					// caller named a position; the list does not have it.
					return 0, 0, entryIndexError(seg, len(s.Values))
				}
				return 0, 0, fmt.Errorf("path segment %q: parent is a sequence and the segment is not an index", seg)
			}
			if i < 0 || i >= len(s.Values) {
				return 0, 0, entryIndexError(seg, len(s.Values))
			}
			t := s.Values[i].GetToken()
			if t == nil {
				return 0, 0, fmt.Errorf("path segment %q: sequence entry has no token", seg)
			}
			keyTok = &struct{ line, col int }{t.Position.Line, t.Position.Column}
			node = s.Values[i]
			continue
		}
		m, ok := node.(*goccyast.MappingNode)
		if !ok {
			return 0, 0, fmt.Errorf("path segment %q: parent is %T, not a mapping", seg, node)
		}
		found := false
		for _, kv := range m.Values {
			if kv.Key.GetToken().Value == seg {
				t := kv.Key.GetToken()
				keyTok = &struct{ line, col int }{t.Position.Line, t.Position.Column}
				node = kv.Value
				found = true
				break
			}
		}
		if !found {
			return 0, 0, fmt.Errorf("path segment %q not found", seg)
		}
	}
	if keyTok == nil {
		return 0, 0, fmt.Errorf("empty path")
	}
	// The parser's line number must address a line this file actually has.
	//
	// parseView is what makes that true rather than hoped for, and this is the
	// assertion that says so. It used to be false: goccy counted a COMMENT line
	// in a CRLF document as two lines, so Range and DeleteRange indexed past the
	// end of scanLines and PANICKED, or — on a longer file — returned a
	// plausible range over the wrong bytes, which is worse than a panic.
	//
	// Kept as a guard rather than deleted along with the cause: it costs one
	// comparison, and the failure it catches is the one this engine cannot
	// detect any other way.
	if lines := bytes.Count(src, []byte("\n")) + 1; keyTok.line > lines {
		return 0, 0, fmt.Errorf("%w: the parser puts this key on line %d and the file has %d lines",
			ErrPositionMismatch, keyTok.line, lines)
	}
	// The SECOND rune column in this engine, and the same substitution that
	// produced the scalar defect (story 6.4). The indent returned here is
	// compared against indents scanLines counts in BYTES, and the parser reports
	// this column in RUNES. In block style the two agree, because the only thing
	// to a key's left is spaces — which is why `keyTok.col - 1` never produced
	// wrong bytes and why Range, being line-granular, still returns whole lines.
	// A token inside a FLOW collection has real text to its left and they do not
	// agree: measured on e38, `tier:` inside `{city: "東京", tier: ...}` is
	// reported at column 26 and begins 29 bytes into its line.
	//
	// So it is converted through the same function the scalar path uses, rather
	// than left as a benign coincidence of block style. One conversion, one unit,
	// no second place for a rune to be mistaken for a byte.
	off, oerr := offsetOf(body, keyTok.line, keyTok.col)
	if oerr != nil {
		return 0, 0, oerr
	}
	lineOff, oerr := offsetOf(body, keyTok.line, 1)
	if oerr != nil {
		return 0, 0, oerr
	}
	return keyTok.line - 1, off - lineOff, nil
}

// ErrPositionMismatch is returned when the parser's reported position does not
// describe the file's own bytes. It is a refusal in the sense of CLAUDE.md rule
// 6: an operation that cannot be performed safely returns a typed error and
// touches nothing, because a splice at a position the parser and the buffer
// disagree about writes into a line the operation does not own.
var ErrPositionMismatch = errors.New("the parser's position does not match the file's bytes; refusing rather than splicing at a guessed offset")

// ErrEntryIndex is returned when a path addresses an entry of a sequence by an
// index the sequence does not have — story 9.2.
//
// It is a sentinel rather than a bare error because of what the engine used to
// say. `locate` answered `path segment "9": sequence has 3 entries` and
// `scalarSpan` answered `path services.web.ports.9 not found`: two sentences
// for one question, neither of them classifiable, so `edit.Refused` returned
// false and the reader was told the tool had broken. The classifier above it
// was worse — it read the missing index as an ABSENT KEY and offered to add it
// with `insert_key`, which on a sequence adds nothing at all.
//
// Index addressing is deliberate (DECISIONS.md 24) and its one cost is that an
// index moves when the list does. This is the refusal that makes that cost
// visible instead of silent; the staleness half is AD-19's `Expect`.
var ErrEntryIndex = errors.New("that list has no entry at that position")

func entryIndexError(seg string, length int) error {
	if length == 0 {
		return fmt.Errorf("%w: entry %s was asked for and the list is empty", ErrEntryIndex, seg)
	}
	return fmt.Errorf("%w: entry %s was asked for and the list has %d %s (0 through %d)",
		ErrEntryIndex, seg, length, plural(length, "entry", "entries"), length-1)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// entryIndexFailure re-walks path against body and reports ErrEntryIndex when
// the walk fails because a sequence index is out of range, or nil when it fails
// for any other reason.
//
// It exists because findGoccy answers `nil` for every kind of failure, and the
// caller that turns that nil into a sentence — scalarSpan — cannot tell "no
// such key" from "no such entry". Rather than teach findGoccy a second return
// value and change every caller's shape, the diagnosis is a second walk over
// the same traversal, taken only on the failure path where its cost is nothing.
func entryIndexFailure(n goccyast.Node, path []string) error {
	for i, seg := range path {
		s, ok := n.(*goccyast.SequenceNode)
		if !ok {
			next := findGoccy(n, path[i:i+1])
			if next == nil {
				return nil
			}
			n = next
			continue
		}
		idx, numeric, shaped := EntryIndex(seg)
		if !numeric {
			if shaped {
				return entryIndexError(seg, len(s.Values))
			}
			return nil
		}
		if idx < 0 || idx >= len(s.Values) {
			return entryIndexError(seg, len(s.Values))
		}
		n = s.Values[idx]
	}
	return nil
}

// EntryIndex is the ONE reading of a path segment that addresses an entry of a
// sequence — story 9.2's address, and AD-14 applied to it.
//
// It answers two things, because the callers need two:
//
//	ok      the segment names a position. idx is it.
//	shaped  the segment is plainly MEANT as a position even though it does not
//	        name one: a sign, a stray space, or a number no int can hold. Those
//	        get ErrEntryIndex — "that list has no entry there" — rather than the
//	        bare `path services.web.ports.-1 not found` they used to get, which
//	        `edit.Refused` could not classify, so the reader was told the tool
//	        had broken.
//
// The arithmetic is strconv's and strconv's alone. It used to be a hand-rolled
// `n = n*10 + d` here and a `strconv.Atoi` in internal/edit — two
// implementations of one address, disagreeing exactly where it cost: measured
// through the real binary on a two-entry list, `ports[18446744073709551616]`
// OVERFLOWED to 0 and WROTE ENTRY 0 while reporting success, and `[...617]`
// wrote entry 1. Atoi returns ErrRange for both. After the wrap the index IS in
// range, so ErrEntryIndex — the whole point of story 9.2 — was bypassed, and
// Classify meanwhile answered `absent` + `plan: insert_key`, the confident
// wrong answer with an invitation attached.
//
// Deliberately NOT resolved, though shaped: `+1`, `-1`, `1 `. A leading `+` and
// a stray space are two spellings of an address this engine writes one way, and
// a negative index is Python's idea rather than YAML's — accepting either would
// be a second address for one entry. They are refused by name instead.
func EntryIndex(seg string) (idx int, ok, shaped bool) {
	t := strings.TrimSpace(seg)
	digits := t
	if digits != "" && (digits[0] == '+' || digits[0] == '-') {
		digits = digits[1:]
	}
	if digits == "" {
		return 0, false, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false, false
		}
	}
	// From here it is a number, so it is an address the caller meant as one.
	if t != seg || t != digits {
		return 0, false, true
	}
	n, err := strconv.Atoi(t)
	if err != nil {
		// Out of range. It is shaped like a position and it is not one, which
		// is exactly what ErrEntryIndex says.
		return 0, false, true
	}
	return n, true, true
}

// sequenceIndex is EntryIndex for the callers with nowhere to put a refusal —
// findGoccy answers nil for every kind of failure, and entryIndexFailure
// re-walks the same path to say which kind it was.
func sequenceIndex(s string) (int, bool) {
	idx, ok, _ := EntryIndex(s)
	return idx, ok
}

// childIndent infers the indentation new children of the mapping at parentIdx
// should use: the indent of its existing first child, or the parent's indent
// plus the file's dominant step if the mapping is empty.
func childIndent(lines []lineInfo, parentIdx, parentIndent int) int {
	for i := parentIdx + 1; i < len(lines); i++ {
		l := lines[i]
		if l.blank || l.comment {
			continue
		}
		if l.indent <= parentIndent {
			break // no children
		}
		return l.indent
	}
	return parentIndent + dominantStep(lines)
}

// dominantStep infers the file's indentation unit by taking the smallest
// positive increase between consecutive non-blank lines. Compose files are
// almost always 2, but honouring 4 where a file uses 4 is the difference
// between an edit that looks native and one that looks like a robot did it.
func dominantStep(lines []lineInfo) int {
	best := 0
	prev := -1
	for _, l := range lines {
		if l.blank || l.comment {
			continue
		}
		if prev >= 0 && l.indent > prev {
			if d := l.indent - prev; best == 0 || d < best {
				best = d
			}
		}
		prev = l.indent
	}
	if best == 0 {
		return 2
	}
	return best
}

// ---------------------------------------------------------------------------
// Line endings (story 7.1)
// ---------------------------------------------------------------------------
//
// scanLines splits on "\n" only, so a CRLF file's "\r" stays INSIDE the line
// text. That is what made existing bytes survive an insert and the new line
// come out wrong: a line appended as a lineInfo has no ending of its own and
// inherited the "\n" that joinLines writes between elements. Measured on a CRLF
// compose file, the shipped binary produced `...image: nginx\r\n  cache:\n`.
//
// The three functions below are that arithmetic made explicit, and they are
// used by both the write (InsertKey) and the range a preview reports
// (InsertionPoint) so the two cannot diverge (AD-14).
//
// Note what is NOT here: nothing shared with internal/dockerfile. CLAUDE.md
// says the two engines are separate grammars and must not be unified; story 7.1
// asks for the same FIX in each, in each engine's own code, not for one helper
// spanning both. What crosses between them is the fixture requirement, not a
// function.

// lineTerminator is the bytes that end line i, or "" when line i is the file's
// last and therefore unterminated.
func lineTerminator(lines []lineInfo, i int) string {
	if i < 0 || i >= len(lines)-1 {
		return ""
	}
	if strings.HasSuffix(lines[i].text, "\r") {
		return "\r\n"
	}
	return "\n"
}

// endingFor is the line ending a new line inserted after line i must carry: the
// ending of the line it is inserted AFTER.
//
// That is the only locally defensible answer for a file with mixed endings, and
// real files have them. Picking a file-wide majority would rewrite the ending of
// a line the reader did not ask about the moment the majority disagreed with the
// neighbour.
//
// When line i is the file's last and carries no ending — a file with no trailing
// newline — there is nothing local to copy, so the nearest preceding ending is
// used, and "\n" only when the file has no line break at all.
func endingFor(lines []lineInfo, i int) string {
	if t := lineTerminator(lines, i); t != "" {
		return t
	}
	for k := i - 1; k >= 0; k-- {
		if t := lineTerminator(lines, k); t != "" {
			return t
		}
	}
	return "\n"
}

// contentEnd is the byte offset one past the last CONTENT byte of line i — before
// its "\r" on a CRLF file, not after it.
//
// Splicing at this offset rather than at the newline is what keeps a CRLF
// ending in one piece. Inserting between the CR and the LF would split the
// ending in two and give the new line a stray "\n" of its own while leaving the
// line above ending in a bare "\r".
func contentEnd(lines []lineInfo, i int) int {
	off := lineStart(lines, i) + len(lines[i].text)
	if i < len(lines)-1 && strings.HasSuffix(lines[i].text, "\r") {
		off--
	}
	return off
}

// ---------------------------------------------------------------------------
// Splice implementation
// ---------------------------------------------------------------------------

// ErrFlowStyle is returned when a structural edit targets a flow-style
// collection (`web: {image: nginx}`). A block child cannot be appended to a
// flow mapping — the result is not valid YAML — so the edit is refused rather
// than performed incorrectly.
//
// Refusing is the whole point. An engine that silently emits an unparseable
// file is worse than one that declines, because the damage surfaces later, in
// someone else's terminal.
var ErrFlowStyle = fmt.Errorf("target is a flow-style collection; block insertion would produce invalid YAML")

// ErrNoRootMapping is returned when a top-level insert is asked for on a
// document that has no root mapping to append to: an empty file, a file that is
// only comments, a null document, a sequence or a bare scalar at the root.
//
// It is a new exported sibling of ErrNullValue rather than ErrNullValue itself.
// Both mean "there is nowhere safe to write", but they are different questions
// for the reader: ErrNullValue says a value has no bytes to replace, this says
// the file has no document to add to. Appending `networks:` to an empty file
// would produce a valid YAML document that is not a compose file, and inventing
// a document shape is the scaffolding DECISIONS.md 20 keeps in phase 5.
var ErrNoRootMapping = fmt.Errorf("the document has no top-level mapping to add a key to; refusing rather than inventing one")

// isFlowMapping reports whether the value on the anchor line opens a flow
// collection, i.e. the text after the first `:` begins with `{` or `[`.
func isFlowMapping(lines []lineInfo, idx int) bool {
	if idx < 0 || idx >= len(lines) {
		return false
	}
	text := lines[idx].text
	if i := strings.Index(text, ":"); i >= 0 {
		rest := strings.TrimSpace(text[i+1:])
		return strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "[")
	}
	return false
}

// rootInsertAnchor answers, for the DOCUMENT ROOT, the two questions every
// insert has to answer: after which line does the new key go, and at what
// indent.
//
// One rule for every mapping including the root. InsertKey's contract is
// "appended after the mapping's existing children", so a top-level key is
// appended after the root's existing children — not sorted into place, not put
// after `services:` because the specification lists it there, and not at the
// top. Two rules is how the root becomes the special case that behaves
// differently from everything the reader has already learned.
//
// Two answers it gives that had to be decided rather than inferred, both pinned
// by fixtures under testdata/edge:
//
//   - A trailing comment block at column 0 keeps its place at the foot of the
//     file: the new key lands AFTER it. subtreeEnd treats a trailing comment as
//     undecided, so without this the key would slot in above comments that
//     document the end of the file.
//   - A multi-document file targets the FIRST document, before its separator,
//     as f.Docs[0] does everywhere else in this engine. Appending at the end of
//     the file would add the key to a different document than the one every
//     other operation reads.
func rootInsertAnchor(src []byte, lines []lineInfo) (after, indent int, err error) {
	parse, _, _ := parseView(src)
	f, perr := goccyparser.ParseBytes(parse, goccyparser.ParseComments)
	if perr != nil {
		return 0, 0, perr
	}
	if len(f.Docs) == 0 || f.Docs[0].Body == nil {
		return 0, 0, ErrNoRootMapping
	}

	// The root has to BE a mapping. A sequence, a scalar, a null body — an empty
	// file and a comments-only file both parse as one — have no key to append a
	// sibling to, and writing one anyway would invent a document shape.
	var first *goccyast.MappingValueNode
	switch m := f.Docs[0].Body.(type) {
	case *goccyast.MappingNode:
		if m.IsFlowStyle {
			return 0, 0, ErrFlowStyle
		}
		if len(m.Values) == 0 {
			return 0, 0, ErrNoRootMapping
		}
		first = m.Values[0]
	case *goccyast.MappingValueNode:
		// A root mapping of exactly one entry parses as a bare MappingValueNode.
		first = m
	default:
		return 0, 0, ErrNoRootMapping
	}
	if first == nil || first.Key.GetToken() == nil {
		return 0, 0, ErrNoRootMapping
	}
	// A single-entry flow root — `{name: demo}` — comes back as a
	// MappingValueNode with no IsFlowStyle to read, so it is caught from the
	// bytes: the line the first key sits on opens with "{".
	indent = first.Key.GetToken().Position.Column - 1
	keyLine := first.Key.GetToken().Position.Line - 1
	if keyLine >= 0 && keyLine < len(lines) &&
		strings.HasPrefix(strings.TrimSpace(lines[keyLine].text), "{") {
		return 0, 0, ErrFlowStyle
	}

	end := firstDocumentEnd(lines)
	for i := end - 1; i >= 0; i-- {
		if !lines[i].blank {
			return i, indent, nil
		}
	}
	return 0, 0, ErrNoRootMapping
}

// firstDocumentEnd is the index one past the last line of document 0.
//
// A `---` before any content opens document 0 rather than closing it, which is
// why this tracks whether content has been seen instead of taking the first
// separator it finds. `...` ends the document wherever it appears.
func firstDocumentEnd(lines []lineInfo) int {
	content := false
	for i, l := range lines {
		text := strings.TrimRight(l.text, "\r")
		switch {
		case text == "..." || strings.HasPrefix(text, "... "):
			return i
		case text == "---" || strings.HasPrefix(text, "--- "):
			if content {
				return i
			}
		case l.blank || l.comment || strings.HasPrefix(text, "%"):
			// directives and trivia decide nothing
		default:
			content = true
		}
	}
	return len(lines)
}

// InsertKey appends `key: value` to the mapping at path. An EMPTY path is the
// document root (story 7.2): the same operation, one level up.
func (Splice) InsertKey(src []byte, path []string, key, value string) ([]byte, error) {
	lines := scanLines(src)
	var ci, end int
	if len(path) == 0 {
		var err error
		end, ci, err = rootInsertAnchor(src, lines)
		if err != nil {
			return nil, err
		}
		return spliceLineAfter(src, lines, end, keyLine(ci, key, value)), nil
	}
	idx, indent, err := locate(src, path)
	if err != nil {
		return nil, err
	}
	if isFlowMapping(lines, idx) {
		return nil, ErrFlowStyle
	}
	ci = childIndent(lines, idx, indent)
	end = subtreeEnd(lines, idx, indent)

	return spliceLineAfter(src, lines, end, keyLine(ci, key, value)), nil
}

// keyLine renders `key: value`, or `key:` alone when there is no value.
//
// The separating space belongs to the value, not to the key. Emitting `key: `
// for an empty value leaves a trailing space — invisible in a diff viewer,
// caught by every whitespace linter, and a byte the reader did not ask for. A
// key with no value is written exactly as a human writes it.
func keyLine(indent int, key, value string) string {
	text := strings.Repeat(" ", indent) + key + ":"
	if value != "" {
		text += " " + value
	}
	return text
}

// spliceLineAfter adds text as a new line after line `after`, by patching the
// original buffer at one offset.
//
// It replaced a rebuild-from-lines that split the document on "\n" and rejoined
// it on "\n". The rebuild was byte-identical on an LF file and quietly wrong on
// every other kind: it dropped a CRLF file's "\r" from the new line, and — since
// a lineInfo holds only a line's text — it would have dropped a byte order mark
// the way DeleteKey once did, had the mark ever shared a line with an insert.
// Splicing keeps both by construction rather than by care, which is the rule
// this engine is built on.
func spliceLineAfter(src []byte, lines []lineInfo, after int, text string) []byte {
	at := contentEnd(lines, after)
	ins := endingFor(lines, after) + text
	out := make([]byte, 0, len(src)+len(ins))
	out = append(out, src[:at]...)
	out = append(out, ins...)
	out = append(out, src[at:]...)
	return out
}

// ---------------------------------------------------------------------------
// Sequence entries (story 7.5)
// ---------------------------------------------------------------------------

// ErrNotASequence is returned when a sequence-entry insert targets something
// that is not a block sequence — a mapping, a scalar, a key whose value is
// neither. Appending `- x` under a mapping produces a key that is a mapping and
// a sequence at once, which nothing can read back.
var ErrNotASequence = fmt.Errorf("target is not a block sequence; refusing rather than writing an entry into something that is not a list")

// ErrNullEntry is returned when a sequence-entry insert is asked for with no
// value. Writing `- ` leaves a null entry: a shape with no bytes to edit
// afterwards and nowhere safe to attach one, which is what ErrNullValue names
// on the scalar path.
//
// It is a separate sentinel rather than ErrNullValue for a reason that is about
// classification, not taxonomy. edit.Refused must answer true for this so the
// reader is told the tool declined; adding ErrNullValue to that set would also
// reclassify the SCALAR path's null-value error, and internal/edit's corpus
// sweeps skip refusals — a check that currently fails on a null-value error
// would start passing silently instead.
var ErrNullEntry = fmt.Errorf("a sequence entry with no value would be written as a bare `-`; refusing rather than leaving a null entry")

// ErrEntryText is returned when the text offered for a sequence entry would not
// be written as ONE plain entry holding exactly that text.
//
// It is the sibling of ErrNullEntry for the cases that are not null. The null
// check on its own — `TrimSpace(value) == ""` — passed two texts that damaged
// the file and reported success:
//
//	`# nope`               written as `- # nope`, which is a comment after a
//	                       dash: the null entry ErrNullEntry exists to prevent,
//	                       arrived at by a different road.
//
//	`x\n    restart: y`    written as TWO lines, the second of them a key that
//	                       joined the parent mapping. The file still parsed —
//	                       which is why validate did not catch it — and the
//	                       service silently gained a `restart` nobody asked for.
//
// The second is the one that matters. It breaks story 7.5's own criterion that
// "excising the one inserted line yields the source byte for byte", and an edit
// that adds a key the reader did not type is the confident wrong answer this
// engine specialises in.
var ErrEntryText = fmt.Errorf("that text would not be written as one plain sequence entry; refusing rather than writing a line the reader did not ask for")

// entryText decides whether value can be written after a `- ` and read back as
// itself.
//
// It is the same PROPERTY internal/edit's `bare` tests for a service name and an
// image — parse the text in the position it will occupy, accept it only when
// YAML reads back the bytes the reader typed — applied in the position a
// sequence entry occupies. Story 7.3 built that property test and story 7.5 did
// not reuse it; these are the defects that followed.
//
// It lives HERE rather than in internal/edit because CLAUDE.md rule 6 puts the
// refusal in the engine: every caller of InsertSequenceEntry gets it, including
// the corpus harness and any client the CLI has not been written for yet.
//
// What it deliberately does NOT test is what `bare` calls tag opinion: `- 8080`
// reads back as the integer 8080 and is a legitimate port entry, where `image:
// 8080` is a question about what the reader meant. The property here is byte
// readback, not type.
func entryText(value string) error {
	if strings.TrimSpace(value) == "" {
		return ErrNullEntry
	}
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%w: the entry contains a line break, so it would be written as more than one line and everything after the first would land in the enclosing mapping. Write it on one line", ErrEntryText)
	}
	if strings.Contains(value, "\t") {
		return fmt.Errorf("%w: the entry contains a tab, which YAML does not allow in indentation and reads unpredictably here", ErrEntryText)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("- "+value+"\n"), &doc); err != nil {
		return fmt.Errorf("%w: `%s` is not something YAML reads back as an entry (%v). Quote it yourself and it goes in exactly as typed", ErrEntryText, value, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode || len(doc.Content[0].Content) != 1 {
		return fmt.Errorf("%w: `%s` is not one entry", ErrEntryText, value)
	}
	node := doc.Content[0].Content[0]
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: `%s` is a collection, not one entry. Add the entry and then add what goes under it", ErrEntryText, value)
	}
	if node.Tag == "!!null" {
		// `- # nope`, `- ~`, `- null`. The written line carries no value, which
		// is exactly the shape ErrNullEntry names.
		return fmt.Errorf("%w: `%s` reads back as nothing at all", ErrNullEntry, value)
	}
	if node.Style&yaml.DoubleQuotedStyle != 0 || node.Style&yaml.SingleQuotedStyle != 0 {
		return nil // the reader quoted it themselves; their bytes, unchanged
	}
	if node.Style != 0 {
		return fmt.Errorf("%w: `%s` is a block scalar, which spans lines", ErrEntryText, value)
	}
	if node.Value != value {
		return fmt.Errorf("%w: written bare, `%s` would be read back as `%s`. Quote it yourself and the engine writes every byte of it", ErrEntryText, value, node.Value)
	}
	return nil
}

// RootIsMapping reports whether src is a YAML document whose root is a mapping —
// which every compose file is, and no Dockerfile is.
//
// It exists for internal/edit's grammar check: `insert_stage` on a compose file
// appended `FROM alpine` to it and exited 0, because the request's grammar was
// taken from the operation rather than from the file. The question "is this the
// other engine's file" is answered by the parser this engine already uses, not
// by a filename.
func RootIsMapping(src []byte) bool {
	body, _, _ := parseView(src)
	f, err := goccyparser.ParseBytes(body, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 || f.Docs[0].Body == nil {
		return false
	}
	switch f.Docs[0].Body.(type) {
	case *goccyast.MappingNode, *goccyast.MappingValueNode:
		return true
	}
	return false
}

// InsertSequenceEntry appends `- value` to the block sequence at path.
//
// R4.2 names insert list item and this engine had no writer for it, though
// locate could already address an entry. It is the same splice as InsertKey with
// a different answer to "what indent", and that answer is the whole difficulty:
// a sequence under a key may be written at the key's own indent or one step in,
// and both are valid YAML. childIndent's answer — one step in — is right for a
// mapping and wrong for half the sequences in the wild, so this copies the
// siblings and falls back to dominantStep only when there are none.
func (Splice) InsertSequenceEntry(src []byte, path []string, value string) ([]byte, error) {
	if err := entryText(value); err != nil {
		return nil, err
	}
	lines := scanLines(src)
	after, indent, marker, err := sequenceAnchor(src, lines, path)
	if err != nil {
		return nil, err
	}
	return spliceLineAfter(src, lines, after, strings.Repeat(" ", indent)+"-"+marker+value), nil
}

// sequenceAnchor answers, for the block sequence at path: after which line the
// new entry goes, at what column its `-` sits, and what spacing follows the
// marker.
//
// The marker's spacing is copied too. A file that writes `-   "80:80"` is
// making a formatting choice, and normalising it to `- ` would rewrite a line
// nobody asked about — the same opinion R7.4 refuses in the Dockerfile grammar.
func sequenceAnchor(src []byte, lines []lineInfo, path []string) (after, indent int, marker string, err error) {
	if len(path) == 0 {
		return 0, 0, "", ErrNotASequence
	}
	idx, keyIndent, err := locate(src, path)
	if err != nil {
		return 0, 0, "", err
	}
	// Flow first, before anything is computed, as InsertKey does: `ports:
	// ["80:80"]` cannot take a block entry, and the refusal has to come before
	// the arithmetic rather than after it.
	if isFlowMapping(lines, idx) {
		return 0, 0, "", ErrFlowStyle
	}
	if err := assertBlockSequence(src, path); err != nil {
		return 0, 0, "", err
	}

	entryIndent := -1
	marker = " "
	after = idx
	for i := idx + 1; i < len(lines); i++ {
		l := lines[i]
		if l.blank || l.comment {
			continue // undecided: could be interior, could be the gap after
		}
		dash, spacing := dashEntry(l)
		if entryIndent < 0 {
			if !dash || l.indent < keyIndent {
				break // nothing to imitate
			}
			entryIndent, marker, after = l.indent, spacing, i
			continue
		}
		switch {
		case l.indent > entryIndent:
			after = i // part of the entry above: a mapping entry's own keys
		case l.indent == entryIndent && dash:
			after = i
		default:
			i = len(lines) // the sequence has ended
		}
	}
	if entryIndent < 0 {
		// No sibling to copy. This is the one case that has to be decided
		// rather than inferred, so it is decided here and pinned by a fixture:
		// the file's own indentation step, applied to the key's indent.
		entryIndent = keyIndent + dominantStep(lines)
	}
	return after, entryIndent, marker, nil
}

// dashEntry reports whether a line opens a block sequence entry, and the
// spacing between its `-` and the entry's value.
func dashEntry(l lineInfo) (bool, string) {
	rest := strings.TrimRight(l.text[l.indent:], "\r")
	if rest == "-" {
		return true, " "
	}
	if !strings.HasPrefix(rest, "-") {
		return false, ""
	}
	body := rest[1:]
	if body[0] != ' ' && body[0] != '\t' {
		return false, "" // `-name`, part of a scalar, not a marker
	}
	return true, body[:len(body)-len(strings.TrimLeft(body, " \t"))]
}

// assertBlockSequence refuses everything that is not a block sequence or an
// empty one. The line scan above cannot tell a mapping from an empty sequence —
// both look like "a key with nothing that starts with a dash under it" — so the
// parser decides, and a wrong answer here would write an entry into a mapping.
func assertBlockSequence(src []byte, path []string) error {
	parse, _, _ := parseView(src)
	f, perr := goccyparser.ParseBytes(parse, goccyparser.ParseComments)
	if perr != nil {
		return perr
	}
	if len(f.Docs) == 0 {
		return fmt.Errorf("empty document")
	}
	node := findGoccy(f.Docs[0].Body, path)
	if node == nil {
		return fmt.Errorf("path %s not found", strings.Join(path, "."))
	}
	switch n := node.(type) {
	case *goccyast.SequenceNode:
		if n.IsFlowStyle {
			return ErrFlowStyle
		}
		return nil
	case *goccyast.NullNode:
		// `ports:` with nothing under it. An empty sequence and an unwritten
		// key are the same bytes, and adding the first entry is the operation
		// that tells them apart.
		return nil
	}
	if t := node.GetToken(); t != nil && t.Type == goccytoken.ImplicitNullType {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotASequence, strings.Join(path, "."))
}

// DeleteKey removes the node at path along with its subtree and attached
// comments.
//
// It is a splice of the range DeleteRange reports rather than a second
// implementation of "where does this node end". AD-14 asks for exactly that —
// the range a finding reports has to be the range the engine edits — and the
// only way to guarantee it is to derive both from one function.
//
// The divergence this closes was real and silent. This used to rebuild the
// document from lineInfo objects, and a lineInfo holds only the text of a line.
// The byte order mark lives at the front of line 0, so deleting a file's FIRST
// key rebuilt the document without it: the mark belongs to the file, not to the
// key written on it, and dropping it rewrites the file's encoding declaration.
// DeleteRange applies afterBOM and kept it. Two operations, one file, two
// answers — and the one that wrote bytes was the wrong one.
func (Splice) DeleteKey(src []byte, path []string) ([]byte, error) {
	start, end, err := DeleteRange(src, path)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(src)-(end-start))
	out = append(out, src[:start]...)
	out = append(out, src[end:]...)
	return out, nil
}

// ---------------------------------------------------------------------------
// Re-emit implementations, for comparison
// ---------------------------------------------------------------------------

func (s ReEmitYAMLv3) InsertKey(src []byte, path []string, key, value string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented: re-emit rewrites the document wholesale")
}

func (s ReEmitYAMLv3) DeleteKey(src []byte, path []string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented: re-emit rewrites the document wholesale")
}

func (s ReEmitYAMLv3) InsertSequenceEntry(src []byte, path []string, value string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented: re-emit rewrites the document wholesale")
}
