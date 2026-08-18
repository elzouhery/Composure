package dockerfile

// Splice operations for Dockerfiles.
//
// Same principle as the YAML engine: patch the original bytes, never re-emit.
// Identity is therefore free — a parse that changes nothing writes nothing.

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMultiLine is returned when an edit would have to reflow a continuation
// block. Deciding where to break lines is a formatting opinion, and imposing
// one silently rewrites lines the reader did not ask about — R7.4 says refuse
// instead.
//
// It is a sentinel rather than a bare error so the surfaces above can tell a
// safe refusal, which reverts a field and explains itself, from an operational
// failure, which is a fault.
var ErrMultiLine = errors.New("instruction spans several lines; a multi-line rewrite requires an explicit line-break policy")

// ErrNoInsertionPoint is returned when there is nowhere in the file it is safe
// to put a new line: a file with no stage to add to, and a stage whose last
// instruction opens a heredoc that is never closed — where "the end of the
// stage" is in the middle of a shell script.
//
// InsertAfter's bounds check is not this. An index outside the file is a caller
// bug and stays a plain error; this is the file itself saying no, which is what
// CLAUDE.md rule 6 asks an operation to be able to do (story 7.6).
var ErrNoInsertionPoint = errors.New("there is nowhere in this file it is safe to insert an instruction")

// ErrInsertText is text that cannot be written as one instruction line: empty,
// carrying its own newline, or ending in the escape character — which would
// make the following line a continuation of it.
//
// Breaking the reader's text into lines for them is choosing where their lines
// break, which is the formatting opinion R7.4 refuses and ReplaceArgs already
// refuses for the same reason.
var ErrInsertText = errors.New("that text cannot be written as a single instruction")

// ErrStageName is a stage name that another stage already uses, or one that is
// not a legal stage name. Two stages with one name is a build that resolves to
// whichever BuildKit picks.
var ErrStageName = errors.New("that stage name cannot be used")

// Bytes returns the source unchanged. Identity is trivially byte-perfect
// because the engine has no serialiser to be imperfect with.
func (f *File) Bytes() []byte {
	return append([]byte(nil), f.Source...)
}

// SetBaseImage replaces the image reference of the FROM at stageIdx (zero-based
// over FROM instructions only), preserving --platform, the AS clause, casing of
// the FROM keyword, and any trailing comment.
//
// This is the operation that connects the editor to image search: "your base is
// 14 months old, here are the candidates, applying one is a one-line diff."
func (f *File) SetBaseImage(stageIdx int, newRef string) ([]byte, error) {
	stages := f.Stages()
	if stageIdx < 0 || stageIdx >= len(stages) {
		return nil, fmt.Errorf("stage %d out of range (%d FROM instructions)", stageIdx, len(stages))
	}
	in := f.Instructions[stages[stageIdx]]
	if in.ImageRef == "" {
		return nil, fmt.Errorf("stage %d: could not locate image reference", stageIdx)
	}
	out := make([]byte, 0, len(f.Source)-len(in.ImageRef)+len(newRef))
	out = append(out, f.Source[:in.ImageStart]...)
	out = append(out, newRef...)
	out = append(out, f.Source[in.ImageEnd:]...)
	return out, nil
}

// InsertAfter inserts a new instruction line after the instruction at idx.
//
// It deliberately does NOT try to be clever about placement. Dockerfile
// semantics are order-dependent — moving an ENV above a RUN changes what the
// RUN sees — so the caller decides position and the engine executes it exactly.
// The new line carries the ending of the line it is inserted after (story 7.1).
// This used to split the source on "\n", append text as a bare element and
// rejoin on "\n": the existing lines kept their "\r" inside their text, so the
// old bytes survived and only the NEW line came out with an LF in a CRLF file.
// It is now a splice at one offset, which also means a byte order mark on the
// first line is preserved by construction rather than by the rebuild happening
// to copy it.
//
// The same fix exists in internal/strategy and shares no code with this. The two
// engines are separate grammars (CLAUDE.md) and unifying them for six lines of
// arithmetic would be the wrong trade; what is shared is the fixture set under
// testdata/edge that pins both.
func (f *File) InsertAfter(idx int, text string) ([]byte, error) {
	if idx < 0 || idx >= len(f.Instructions) {
		return nil, fmt.Errorf("instruction index %d out of range (%d instructions)", idx, len(f.Instructions))
	}
	in := f.Instructions[idx]
	at := f.contentEnd(in.EndLine)
	ins := f.endingFor(in.EndLine) + text
	// Choosing WHERE — which stage, which end — is insert.go's, and the offsets
	// it reports to a preview are this same contentEnd. What is here is the
	// splice alone.

	out := make([]byte, 0, len(f.Source)+len(ins))
	out = append(out, f.Source[:at]...)
	out = append(out, ins...)
	out = append(out, f.Source[at:]...)
	return out, nil
}

// contentEnd is the offset one past the last CONTENT byte of a line — before its
// "\r" on a CRLF file, not after it. Splicing at the newline instead would land
// between a CR and its LF and split the ending in two.
func (f *File) contentEnd(line int) int {
	end := f.lineEnd(line)
	if end > 0 && end <= len(f.Source) && end-1 >= 0 && f.Source[end-1] == '\r' {
		return end - 1
	}
	return end
}

// endingFor is the ending a line inserted after `line` must carry: that line's
// own. A file with mixed endings — and real ones have them — keeps its mixture;
// the only defensible local answer is the neighbour's, never a file-wide
// majority that would rewrite a line nobody asked about. A last line with no
// ending of its own borrows the nearest preceding one.
func (f *File) endingFor(line int) string {
	for i := line; i >= 0; i-- {
		if i+1 >= len(f.lineStart) {
			continue // the file's last line is unterminated; look further up
		}
		if f.lineStart[i+1] >= 2 && f.Source[f.lineStart[i+1]-2] == '\r' {
			return "\r\n"
		}
		return "\n"
	}
	return "\n"
}

// Delete removes the instruction at idx together with its continuation lines,
// heredoc body, and any comment block attached directly above it.
func (f *File) Delete(idx int) ([]byte, error) {
	if idx < 0 || idx >= len(f.Instructions) {
		return nil, fmt.Errorf("instruction index %d out of range (%d instructions)", idx, len(f.Instructions))
	}
	in := f.Instructions[idx]
	lines := strings.Split(string(f.Source), "\n")

	start := in.StartLine
	// Absorb a contiguous run of comment lines immediately above — they
	// document this instruction and orphaning them is its own kind of damage.
	for j := idx - 1; j >= 0; j-- {
		p := f.Instructions[j]
		if p.Kind == KindComment && p.EndLine == start-1 {
			start = p.StartLine
			continue
		}
		break
	}
	end := in.EndLine

	// Collapse a doubled blank line, as the YAML engine does.
	if start > 0 && strings.TrimSpace(lines[start-1]) == "" &&
		end+1 < len(lines) && strings.TrimSpace(lines[end+1]) == "" {
		end++
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	out = append(out, lines[end+1:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// ReplaceArgs rewrites the body of an instruction, keeping its name, flags and
// position. Returns an error for multi-line instructions: rewriting a
// continuation block means deciding where to break lines, which is a formatting
// opinion the engine should not impose silently.
func (f *File) ReplaceArgs(idx int, newArgs string) ([]byte, error) {
	if idx < 0 || idx >= len(f.Instructions) {
		return nil, fmt.Errorf("instruction index %d out of range", idx)
	}
	in := f.Instructions[idx]
	if in.Kind != KindInstruction {
		return nil, fmt.Errorf("instruction %d is not an instruction", idx)
	}
	if in.EndLine != in.StartLine {
		return nil, fmt.Errorf("%w: instruction %d spans %d lines",
			ErrMultiLine, idx, in.EndLine-in.StartLine+1)
	}
	lines := strings.Split(string(f.Source), "\n")
	line := lines[in.StartLine]

	// Preserve a trailing comment if there is one.
	comment := ""
	if h := strings.Index(line, "#"); h >= 0 {
		comment = line[h:]
	}

	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	rebuilt := indent + in.NameRaw
	for _, fl := range in.Flags {
		rebuilt += " " + fl
	}
	if newArgs != "" {
		rebuilt += " " + newArgs
	}
	if comment != "" {
		rebuilt += " " + comment
	}
	lines[in.StartLine] = rebuilt
	return []byte(strings.Join(lines, "\n")), nil
}
