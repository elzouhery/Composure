// Package dockerfile implements a fidelity-preserving parser and editor for
// Dockerfiles.
//
// This is a SEPARATE engine from the YAML one. The splice principle carries
// over — locate byte ranges, patch in place, never re-emit — but the grammar
// shares nothing with YAML and each of its quirks is a way to corrupt a file:
//
//	Line continuations   a trailing backslash joins lines, so one logical
//	                     instruction can span twenty physical lines.
//	Custom escape char   `# escape=`` ` swaps the continuation character to a
//	                     backtick. Miss the directive and every continuation in
//	                     a Windows-targeted file is misread.
//	Heredocs             `RUN <<EOF` swallows following lines until the
//	                     delimiter. A parser that doesn't know this will read
//	                     shell script as Dockerfile instructions.
//	Interior comments    comment lines are legal *inside* a continuation block
//	                     and Docker strips them before execution.
//	Free casing          `FROM`, `from` and `From` are all valid and appear in
//	                     the wild. Normalising casing is a diff nobody asked for.
//
// The parser records byte offsets for every instruction and, for FROM, for the
// image reference specifically — because swapping a base image is the single
// most common Dockerfile edit and the one that connects to image search.
package dockerfile

import (
	"strings"
)

// bom is the UTF-8 byte order mark. Editors on Windows write it, BuildKit
// tolerates it, and a parser that does not will read the first instruction as
// "\ufeffFROM" — which matches nothing, so the file silently appears to have no
// stages and every base-image operation fails without an error.
const bom = "\ufeff"

// Kind distinguishes the line types that matter for editing.
type Kind int

const (
	KindInstruction Kind = iota
	KindComment
	KindBlank
	KindDirective // a parser directive: # syntax=, # escape=
)

// Instruction is one logical Dockerfile instruction, which may span several
// physical lines via continuations or a heredoc body.
type Instruction struct {
	Kind      Kind
	Name      string // upper-cased instruction name, e.g. "FROM". Empty for comments/blanks.
	NameRaw   string // exactly as written, e.g. "from"
	StartLine int    // zero-based, inclusive
	EndLine   int    // zero-based, inclusive — covers continuations and heredoc bodies
	StartByte int
	EndByte   int // exclusive

	// Flags are instruction flags such as --platform, --from, --chown, --mount.
	Flags []string
	// Args is the instruction body with continuations joined and interior
	// comments removed — what Docker actually executes.
	Args string

	// Known reports whether Name is an instruction the grammar vocabulary
	// recognises. False is REPORTED, not corrected: an unrecognised name
	// parses exactly as any other instruction — byte ranges, continuations and
	// heredocs are unaffected — and the reader is told this engine does not
	// know it rather than being shown a file with a line quietly missing.
	Known bool

	// FROM-specific. Zero values when Name != "FROM".
	ImageRef     string // e.g. "golang:1.24-alpine"
	ImageStart   int    // byte offset of ImageRef in the source
	ImageEnd     int
	StageName    string // the AS clause, if present
	PlatformFlag string
	HasHeredoc   bool
	// HeredocOpen marks a heredoc whose delimiter never arrives. The body then
	// runs to the end of the file, so EndLine is the last line and "after this
	// instruction" is inside a shell script. Recorded rather than inferred: the
	// insert path refuses such an anchor (ErrNoInsertionPoint) and there is no
	// other way to tell an open heredoc from a closed one after the fact.
	HeredocOpen bool
}

// File is a parsed Dockerfile.
type File struct {
	Source       []byte
	Instructions []Instruction
	EscapeChar   byte // '\\' by default, '`' if a # escape= directive says so
	lineStart    []int
}

// Stages returns the indices of FROM instructions, in order.
func (f *File) Stages() []int {
	var out []int
	for i, in := range f.Instructions {
		if in.Name == "FROM" {
			out = append(out, i)
		}
	}
	return out
}

// StageInstructions returns the File.Instructions indices belonging to stage
// stageIdx, its FROM first.
//
// One answer to "which stage is this instruction in", read by the form that
// draws the stage, by the vocabulary that counts what it declares, and by the
// insert that appends to it. Two answers would eventually disagree and the
// reader's HEALTHCHECK would land in the builder.
//
// Parser directives belong to no stage: they are only honoured in the leading
// comment block, and the form lists them separately for the same reason.
func (f *File) StageInstructions(stageIdx int) []int {
	all := f.stageMembership()
	if stageIdx < 0 || stageIdx >= len(all) {
		return nil
	}
	return all[stageIdx]
}

func (f *File) stageMembership() [][]int {
	var out [][]int
	for i, in := range f.Instructions {
		switch {
		case in.Kind == KindDirective:
			continue
		case in.Name == "FROM":
			out = append(out, []int{i})
		case len(out) == 0:
			continue // the preamble: before any stage exists
		default:
			out[len(out)-1] = append(out[len(out)-1], i)
		}
	}
	return out
}

// Find returns the index of the first instruction with the given name, or -1.
func (f *File) Find(name string) int {
	for i, in := range f.Instructions {
		if in.Name == strings.ToUpper(name) {
			return i
		}
	}
	return -1
}

// Parse reads a Dockerfile without modifying it.
func Parse(src []byte) *File {
	f := &File{Source: src, EscapeChar: '\\'}
	lines := strings.Split(string(src), "\n")

	// Byte offset of the start of each line.
	f.lineStart = make([]int, len(lines))
	off := 0
	for i, l := range lines {
		f.lineStart[i] = off
		off += len(l) + 1 // +1 for the newline we split on
	}

	// Parser directives are only honoured in the leading comment block, before
	// any instruction or blank line. Scanning the whole file for them would
	// pick up an `# escape=` written in prose halfway down.
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			break
		}
		if !strings.HasPrefix(t, "#") {
			break
		}
		d := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "#")))
		if strings.HasPrefix(d, "escape=") {
			v := strings.TrimSpace(strings.TrimPrefix(d, "escape="))
			if len(v) > 0 && (v[0] == '`' || v[0] == '\\') {
				f.EscapeChar = v[0]
			}
		}
	}

	i := 0
	for i < len(lines) {
		raw := lines[i]
		trimmed := strings.TrimPrefix(strings.TrimSpace(raw), bom)

		switch {
		case trimmed == "":
			f.Instructions = append(f.Instructions, f.simple(KindBlank, i, i))
			i++
			continue
		case strings.HasPrefix(trimmed, "#"):
			kind := KindComment
			low := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "#")))
			if strings.HasPrefix(low, "syntax=") || strings.HasPrefix(low, "escape=") ||
				strings.HasPrefix(low, "check=") {
				kind = KindDirective
			}
			f.Instructions = append(f.Instructions, f.simple(kind, i, i))
			i++
			continue
		}

		// An instruction. Collect continuation lines first.
		start := i
		var bodyParts []string
		cur := raw
		for {
			// Strip a trailing continuation char, remembering whether we saw one.
			stripped := strings.TrimRight(cur, " \t\r")
			cont := len(stripped) > 0 && stripped[len(stripped)-1] == f.EscapeChar
			if cont {
				stripped = stripped[:len(stripped)-1]
			}
			bodyParts = append(bodyParts, stripped)
			if !cont || i+1 >= len(lines) {
				break
			}
			i++
			// Comment lines inside a continuation block are stripped by Docker
			// and must not terminate the instruction.
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
				i++
			}
			if i >= len(lines) {
				i = len(lines) - 1
				break
			}
			cur = lines[i]
		}

		joined := strings.Join(bodyParts, " ")
		in := f.simple(KindInstruction, start, i)
		in.NameRaw, in.Flags, in.Args = splitInstruction(joined)
		in.Name = strings.ToUpper(in.NameRaw)

		// Heredoc bodies belong to the instruction and must be consumed, or the
		// shell script inside them gets parsed as Dockerfile instructions.
		if delims := heredocDelims(joined, f.EscapeChar); len(delims) > 0 {
			in.HasHeredoc = true
			j := i + 1
			remaining := map[string]bool{}
			for _, d := range delims {
				remaining[d] = true
			}
			for j < len(lines) && len(remaining) > 0 {
				t := strings.TrimSpace(lines[j])
				if remaining[t] {
					delete(remaining, t)
				}
				j++
			}
			i = j - 1
			in.EndLine = i
			in.EndByte = f.lineEnd(i)
			in.HeredocOpen = len(remaining) > 0
		}

		// The vocabulary table decides this, not a literal "FROM" here. That is
		// what keeps the table load-bearing: an entry that loses its Stage flag
		// stops the image reference being located and byte-ranged, and the
		// parse tests fail. A decorative list would drift in silence.
		spec, known := Lookup(in.Name)
		in.Known = known
		if known && spec.Stage {
			f.annotateFrom(&in, lines)
		}

		f.Instructions = append(f.Instructions, in)
		i++
	}
	return f
}

func (f *File) simple(k Kind, start, end int) Instruction {
	return Instruction{
		Kind:      k,
		StartLine: start,
		EndLine:   end,
		StartByte: f.lineStart[start],
		EndByte:   f.lineEnd(end),
	}
}

func (f *File) lineEnd(line int) int {
	if line+1 < len(f.lineStart) {
		return f.lineStart[line+1] - 1
	}
	return len(f.Source)
}

// splitInstruction separates the instruction name, its --flags, and the rest.
func splitInstruction(s string) (name string, flags []string, args string) {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimLeft(s, " \t"), bom))
	if len(fields) == 0 {
		return "", nil, ""
	}
	name = fields[0]
	rest := fields[1:]
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
		flags = append(flags, rest[0])
		rest = rest[1:]
	}
	return name, flags, strings.Join(rest, " ")
}

// heredocDelims finds heredoc delimiters (<<EOF, <<-EOF, <<"EOF") in an
// instruction body.
func heredocDelims(s string, escape byte) []string {
	var out []string
	for i := 0; i+2 < len(s); i++ {
		if s[i] != '<' || s[i+1] != '<' {
			continue
		}
		j := i + 2
		if j < len(s) && s[j] == '-' {
			j++
		}
		quote := byte(0)
		if j < len(s) && (s[j] == '"' || s[j] == '\'') {
			quote = s[j]
			j++
		}
		k := j
		for k < len(s) && (isWordByte(s[k])) {
			k++
		}
		if k > j {
			out = append(out, s[j:k])
		}
		if quote != 0 && k < len(s) && s[k] == quote {
			k++
		}
		i = k - 1
	}
	return out
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// annotateFrom locates the image reference within a FROM instruction and
// records its exact byte range, so it can be replaced without touching the
// --platform flag, the AS clause, or a trailing comment.
func (f *File) annotateFrom(in *Instruction, lines []string) {
	// FROM is effectively never continued across lines in practice, so the
	// image reference is on the instruction's first line.
	line := lines[in.StartLine]
	lineOff := f.lineStart[in.StartLine]

	// Ignore any trailing comment when locating tokens.
	code := line
	if h := strings.Index(code, "#"); h >= 0 {
		code = code[:h]
	}
	// CRLF files: the carriage return is the last byte of the line and would
	// otherwise be captured as part of the final token. Replacing an image
	// reference that ends in \r silently rewrites that line's ending from CRLF
	// to LF — one line of real damage in every Windows repository, and the kind
	// that shows up as a whole-file diff once core.autocrlf gets involved.
	code = strings.TrimRight(code, "\r")

	for _, fl := range in.Flags {
		if strings.HasPrefix(fl, "--platform=") {
			in.PlatformFlag = fl
		}
	}

	// Walk tokens with their offsets: FROM [--flags] <image> [AS name]
	type tok struct {
		s     string
		start int
	}
	var toks []tok
	i := 0
	for i < len(code) {
		for i < len(code) && (code[i] == ' ' || code[i] == '\t' || code[i] == '\r') {
			i++
		}
		if i >= len(code) {
			break
		}
		j := i
		for j < len(code) && code[j] != ' ' && code[j] != '\t' && code[j] != '\r' {
			j++
		}
		toks = append(toks, tok{code[i:j], i})
		i = j
	}
	if len(toks) < 2 {
		return
	}
	idx := 1
	for idx < len(toks) && strings.HasPrefix(toks[idx].s, "--") {
		idx++
	}
	if idx >= len(toks) {
		return
	}
	in.ImageRef = toks[idx].s
	in.ImageStart = lineOff + toks[idx].start
	in.ImageEnd = in.ImageStart + len(toks[idx].s)

	if idx+2 < len(toks) && strings.EqualFold(toks[idx+1].s, "AS") {
		in.StageName = toks[idx+2].s
	}
}
