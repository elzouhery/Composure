// Package strategy implements and compares candidate round-trip engines.
//
// The question this package exists to answer: can we read a real-world
// docker-compose file, apply a semantic edit, and write it back so that the
// resulting diff touches ONLY the bytes the user actually changed?
//
// Three candidates are measured:
//
//	ReEmitYAMLv3 — parse to gopkg.in/yaml.v3 Node, re-encode. This is the
//	  best a "careful" conventional implementation does. It preserves comments
//	  and anchors (unlike js-yaml, which cannot), but it still regenerates the
//	  whole document from the tree.
//
//	ReEmitGoccy  — parse to goccy/go-yaml AST with comment mode, re-render.
//
//	Splice       — never re-emit. Parse only to locate byte ranges, then patch
//	  the original bytes in place. Unchanged bytes are unchanged by construction,
//	  so identity is guaranteed rather than hoped for.
//
// The expected finding is that both re-emit strategies lose fidelity on real
// files and Splice does not. If that holds, the architecture follows from it.
package strategy

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	goccyast "github.com/goccy/go-yaml/ast"
	goccyparser "github.com/goccy/go-yaml/parser"
	goccytoken "github.com/goccy/go-yaml/token"
	yaml "gopkg.in/yaml.v3"
)

// Strategy round-trips source, optionally applying an edit, and returns the result.
type Strategy interface {
	Name() string
	// Identity parses and re-serialises with no semantic change.
	Identity(src []byte) ([]byte, error)
	// Edit applies a scalar change at the given path (e.g. services.web.image)
	// and returns the new document.
	Edit(src []byte, path []string, newValue string) ([]byte, error)
}

// ---------------------------------------------------------------- yaml.v3 ---

type ReEmitYAMLv3 struct{ Indent int }

func (ReEmitYAMLv3) Name() string { return "reemit-yaml.v3" }

func (s ReEmitYAMLv3) encode(node *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	indent := s.Indent
	if indent == 0 {
		indent = 2
	}
	enc.SetIndent(indent)
	if err := enc.Encode(node); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s ReEmitYAMLv3) Identity(src []byte) ([]byte, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(src, &node); err != nil {
		return nil, err
	}
	// Unmarshal wraps in a DocumentNode; encode the document to keep markers sane.
	return s.encode(&node)
}

func (s ReEmitYAMLv3) Edit(src []byte, path []string, newValue string) ([]byte, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(src, &node); err != nil {
		return nil, err
	}
	target := findV3(&node, path)
	if target == nil {
		return nil, fmt.Errorf("path %s not found", strings.Join(path, "."))
	}
	target.Value = newValue
	return s.encode(&node)
}

func findV3(n *yaml.Node, path []string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return findV3(n.Content[0], path)
	}
	if len(path) == 0 {
		return n
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == path[0] {
			return findV3(n.Content[i+1], path[1:])
		}
	}
	return nil
}

// ------------------------------------------------------------------ goccy ---

type ReEmitGoccy struct{}

func (ReEmitGoccy) Name() string { return "reemit-goccy" }

func (ReEmitGoccy) Identity(src []byte) ([]byte, error) {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil {
		return nil, err
	}
	return []byte(f.String()), nil
}

func (g ReEmitGoccy) Edit(src []byte, path []string, newValue string) ([]byte, error) {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil {
		return nil, err
	}
	if len(f.Docs) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	node := findGoccy(f.Docs[0].Body, path)
	if node == nil {
		return nil, fmt.Errorf("path %s not found", strings.Join(path, "."))
	}
	if sv, ok := node.(*goccyast.StringNode); ok {
		sv.Value = newValue
	} else {
		return nil, fmt.Errorf("target at %s is %T, not a string scalar", strings.Join(path, "."), node)
	}
	return []byte(f.String()), nil
}

func findGoccy(n goccyast.Node, path []string) goccyast.Node {
	if len(path) == 0 {
		return n
	}
	// A segment of all digits addresses a sequence element. Compose puts real
	// values in sequences — a `ports:` entry is one — and a diagnostic has to
	// be able to name one of them.
	if s, ok := n.(*goccyast.SequenceNode); ok {
		i, numeric := sequenceIndex(path[0])
		if !numeric || i < 0 || i >= len(s.Values) {
			return nil
		}
		return findGoccy(s.Values[i], path[1:])
	}
	m, ok := n.(*goccyast.MappingNode)
	if !ok {
		return nil
	}
	for _, v := range m.Values {
		if v.Key.GetToken().Value == path[0] {
			return findGoccy(v.Value, path[1:])
		}
	}
	return nil
}

// ----------------------------------------------------------------- splice ---

// Splice never re-serialises. It uses the parser purely to locate the byte
// range of the value being changed, then patches the original buffer.
//
// Identity is trivially byte-perfect because no edit means no write.
type Splice struct{}

func (Splice) Name() string { return "splice" }

func (Splice) Identity(src []byte) ([]byte, error) {
	// Parse to prove the document is understood, then return the source
	// untouched — including any byte order mark, which is a byte of the file
	// and not of the document.
	body, _ := trimBOM(src)
	if _, err := goccyparser.ParseBytes(body, goccyparser.ParseComments); err != nil {
		return nil, err
	}
	return append([]byte(nil), src...), nil
}

func (Splice) Edit(src []byte, path []string, newValue string) ([]byte, error) {
	start, end, orig, afterKey, err := scalarSpan(src, path)
	if err != nil {
		return nil, err
	}

	// Preserve the original quoting style when substituting.
	replacement := newValue
	switch {
	case afterKey:
		// An implicit null owns no bytes, so there is nothing to overwrite and
		// [start, end) is empty. The value is inserted immediately after the
		// `key:` with the separating space YAML requires — anything less would
		// weld the value onto the key (`healthcheck:x`, which is one scalar,
		// not a mapping) and anything more would touch a byte this operation
		// does not own.
		//
		// An empty new value writes nothing at all rather than a lone space:
		// a trailing space is a change to a line for no semantic gain, and the
		// caller gets ErrNoChange, which is the honest answer.
		if newValue == "" {
			replacement = ""
		} else {
			replacement = " " + newValue
		}
	case len(orig) >= 2:
		switch orig[0] {
		case '"':
			replacement = `"` + newValue + `"`
		case '\'':
			replacement = `'` + newValue + `'`
		}
	}

	out := make([]byte, 0, len(src)-(end-start)+len(replacement))
	out = append(out, src[:start]...)
	out = append(out, replacement...)
	out = append(out, src[end:]...)
	return out, nil
}

// ErrNullValue is returned when a scalar edit targets a value that exists in
// the tree but occupies no bytes in the file, AND the engine cannot find the
// `key:` those bytes would have followed.
//
// The normal implicit-null case is handled, not refused — see keyColonEnd. This
// sentinel covers what is left: a null that is not the value of a mapping key
// (a bare `-` sequence entry, a null document body), where there is no
// syntactically safe place to put a value without rewriting structure. Refusing
// is the only correct answer there, for the same reason ErrFlowStyle refuses.
var ErrNullValue = fmt.Errorf("target has no value bytes and no `key:` to attach one to; refusing rather than guessing where to write")

// scalarSpan is the byte range of the value at path, the lexeme exactly as
// written, and whether that range is an insertion point after a `key:` rather
// than an existing lexeme. It is the whole of Splice.Edit's addressing,
// factored out so that a diagnostic can report the range an edit would touch
// and be certain it is the range the edit does touch (AD-14). One
// implementation, two callers.
//
// afterKey is true exactly when the value is an IMPLICIT null — `healthcheck:`
// with nothing after the colon. Such a value has no bytes, so start == end and
// orig is empty, and the only sane edit is an insertion immediately after the
// colon.
//
// This case was a file-corrupting defect. goccy synthesises a null token whose
// Origin is the four characters "null" and whose position is one past the
// colon — bytes that are not in the file. Deriving end from that Origin ran the
// range past the end of the key's line and swallowed the newline, so replacing
// a null mid-file welded the following line onto it and destroyed it, and at
// end-of-file the same arithmetic ran off the end of the buffer. Neither the
// synthesised Origin nor the synthesised position may be trusted for a null;
// the colon token of the enclosing mapping entry is real and is used instead.
func scalarSpan(src []byte, path []string) (start, end int, orig string, afterKey bool, err error) {
	// Three buffers, and the distinction is load-bearing (bom.go): `parse` is
	// the normalised view the PARSER reads, `body` is the file's real bytes
	// minus any byte order mark and is what every OFFSET below is computed
	// against, and `shift` converts an offset in body back into one in src.
	// Taking an offset from `parse` would be right on every LF file and three
	// bytes — or one per preceding line — out on a Windows one.
	parse, body, shift := parseView(src)
	f, perr := goccyparser.ParseBytes(parse, goccyparser.ParseComments)
	if perr != nil {
		return 0, 0, "", false, perr
	}
	if len(f.Docs) == 0 {
		return 0, 0, "", false, fmt.Errorf("empty document")
	}
	node := findGoccy(f.Docs[0].Body, path)
	if node == nil {
		// Why the walk failed, not just that it did. An index the list does not
		// have is a refusal with a sentence (ErrEntryIndex, story 9.2); anything
		// else is the ordinary "no such path".
		if err := entryIndexFailure(f.Docs[0].Body, path); err != nil {
			return 0, 0, "", false, err
		}
		return 0, 0, "", false, fmt.Errorf("path %s not found", strings.Join(path, "."))
	}
	tok := node.GetToken()
	if tok == nil {
		return 0, 0, "", false, fmt.Errorf("no token for %s", strings.Join(path, "."))
	}

	if tok.Type == goccytoken.ImplicitNullType {
		off, cerr := keyColonEnd(body, f.Docs[0].Body, path)
		if cerr != nil {
			return 0, 0, "", false, cerr
		}
		return off + shift, off + shift, "", true, nil
	}

	// goccy reports 1-indexed line/column. Convert to a byte offset in body.
	start, err = offsetOf(body, tok.Position.Line, tok.Position.Column)
	if err != nil {
		return 0, 0, "", false, err
	}
	// The original lexeme exactly as written, including any quotes.
	//
	// goccy's Origin carries surrounding trivia — leading indentation and the
	// trailing newline — so it must be trimmed on BOTH sides. Trimming only the
	// left overshoots the end offset and swallows the newline, which silently
	// joins the following line (and any comment on it) onto this one.
	//
	// TrimSpace is safe for quoted scalars because the quote characters are the
	// lexeme boundary: whitespace inside "foo " is protected by the quotes.
	orig = strings.TrimSpace(tok.Origin)
	if orig == "" {
		// A token that is present but spans no bytes is not something to splice
		// against: the offset came from a position the engine cannot verify.
		// Refuse rather than write at a guessed offset.
		return 0, 0, "", false, fmt.Errorf("%w: %s", ErrNullValue, strings.Join(path, "."))
	}
	end = start + len(orig)
	if end > len(body) {
		return 0, 0, "", false, fmt.Errorf("computed range %d:%d exceeds source length %d", start, end, len(body))
	}
	// The bytes at the computed offset must BE the lexeme the parser read.
	//
	// Same contract as keyColonEnd's colon assertion, and the same reason: the
	// offset and the lexeme are derived independently — one from the token's
	// line and column, the other from its Origin — so requiring them to agree
	// costs one comparison and catches every case where the parser's idea of a
	// position and the buffer's disagree. goccy's CRLF comment-line miscount is
	// one such case, and without this it would return a plausible range over the
	// wrong bytes on a long enough file rather than the out-of-range error a
	// short one happens to produce. A confident wrong answer is this engine's
	// characteristic failure; this is the cheapest place to refuse one.
	if string(body[start:end]) != orig {
		return 0, 0, "", false, fmt.Errorf("%w: %s is reported at byte %d, where the file reads %q and not %q",
			ErrPositionMismatch, strings.Join(path, "."), start+shift, string(body[start:end]), orig)
	}
	return start + shift, end + shift, orig, false, nil
}

// keyColonEnd is the byte offset one past the `:` that separates the key at
// path from its (absent) value — the only safe insertion point for a value that
// occupies no bytes.
//
// It reads the colon from the mapping entry's own Start token, which is a real
// token over real bytes, and then ASSERTS that the byte it names is in fact a
// `:`. The assertion is the point: an offset that is not a colon means the
// parser and this function disagree about the file, and writing at it could
// change a line this operation does not own. Disagreement is refused.
//
// src here is the buffer the parser was given — the source WITHOUT any byte
// order mark — and the offset returned is in its coordinates. Handing it the
// original buffer would put the colon assertion three bytes out on a BOM'd file
// and refuse every implicit null in it.
func keyColonEnd(src []byte, body goccyast.Node, path []string) (int, error) {
	mv := findMappingValue(body, path)
	if mv == nil || mv.Start == nil {
		return 0, fmt.Errorf("%w: %s", ErrNullValue, strings.Join(path, "."))
	}
	off, err := offsetOf(src, mv.Start.Position.Line, mv.Start.Position.Column)
	if err != nil {
		return 0, err
	}
	if off < 0 || off >= len(src) || src[off] != ':' {
		return 0, fmt.Errorf("%w: %s (no `:` at byte %d)", ErrNullValue, strings.Join(path, "."), off)
	}
	return off + 1, nil
}

// findMappingValue walks path and returns the mapping ENTRY — key, colon and
// value together — that the last segment names, or nil if the last segment is
// not a mapping key (a sequence index, say). It mirrors findGoccy's traversal
// exactly, and differs only in what it returns for the final segment.
func findMappingValue(n goccyast.Node, path []string) *goccyast.MappingValueNode {
	if len(path) == 0 || n == nil {
		return nil
	}
	if s, ok := n.(*goccyast.SequenceNode); ok {
		i, numeric := sequenceIndex(path[0])
		if !numeric || i < 0 || i >= len(s.Values) {
			return nil
		}
		return findMappingValue(s.Values[i], path[1:])
	}
	// A mapping of exactly one entry parses as a bare MappingValueNode rather
	// than a MappingNode, so both shapes have to be walked.
	var entries []*goccyast.MappingValueNode
	switch m := n.(type) {
	case *goccyast.MappingNode:
		entries = m.Values
	case *goccyast.MappingValueNode:
		entries = []*goccyast.MappingValueNode{m}
	default:
		return nil
	}
	for _, kv := range entries {
		t := kv.Key.GetToken()
		if t == nil || t.Value != path[0] {
			continue
		}
		if len(path) == 1 {
			return kv
		}
		return findMappingValue(kv.Value, path[1:])
	}
	return nil
}

// ScalarRange is the half-open byte range Splice.Edit would overwrite for the
// scalar at path — the lexeme alone, quotes included, with no trailing comment
// and no newline. A described fix that reported the whole line would be
// describing an edit that welds a comment onto a value, which is a defect this
// engine has already shipped once and will not ship again.
func ScalarRange(src []byte, path []string) (start, end int, err error) {
	start, end, _, _, err = scalarSpan(src, path)
	return start, end, err
}

// offsetOf converts a parser position into a byte offset in src.
//
// The line is walked in bytes and the COLUMN IS WALKED IN RUNES, because that is
// the unit the parser reports it in. Measured 2026-08-13 against goccy: a
// two-byte é, a three-byte 東 and a four-byte 🎉 each advance the reported column
// by exactly one — so an emoji is one column and not the two UTF-16 units some
// parsers count — and a combining mark advances it by one of its own. Adding
// col-1 BYTES, as this used to, put the offset out by exactly the extra bytes
// every multi-byte character to the token's LEFT costs. Block style hid it
// (nothing but indentation is ever to a token's left); flow style exposed it,
// and the position assertion in scalarSpan turned it into a refusal rather than
// a splice into the neighbouring value. See testdata/edge/e17, e38, e39.
//
// On an ASCII line this is byte-for-byte what the byte walk returned, including
// for a column past the end of the line — the remainder is added as bytes once
// the line's runes are exhausted. That equality is not a nicety: every one of
// the 146 corpus files is ASCII on every addressed line, so the gate can only
// prove this function did not CHANGE there, and it can only prove that if the
// arithmetic is identical rather than merely similar.
func offsetOf(src []byte, line, col int) (int, error) {
	if line < 1 || col < 1 {
		return 0, fmt.Errorf("bad position %d:%d", line, col)
	}
	off, cur := 0, 1
	for cur < line {
		idx := bytes.IndexByte(src[off:], '\n')
		if idx < 0 {
			return 0, fmt.Errorf("line %d beyond end of file", line)
		}
		off += idx + 1
		cur++
	}
	return off + byteColumn(src[off:], col), nil
}

// byteColumn is the byte offset, within the line that begins at the start of
// rest, of the 1-indexed RUNE column col.
//
// A column past the end of the line is left as byte arithmetic. Nothing in the
// engine addresses one deliberately, but the previous implementation permitted
// it and every caller's guard — the colon assertion in keyColonEnd, the lexeme
// assertion in scalarSpan — is written against an offset that may be wrong
// rather than against one that cannot be. Turning it into an error here would
// be a behaviour change on ASCII files, which is the one thing this may not do.
func byteColumn(rest []byte, col int) int {
	if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
		rest = rest[:idx]
	}
	i, runes := 0, 0
	for runes < col-1 && i < len(rest) {
		_, size := utf8.DecodeRune(rest[i:])
		i += size
		runes++
	}
	return i + (col - 1 - runes)
}

// All returns the strategies under test, in reporting order.
func All() []Strategy {
	return []Strategy{ReEmitYAMLv3{Indent: 2}, ReEmitGoccy{}, Splice{}}
}
