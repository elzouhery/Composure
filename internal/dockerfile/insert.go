package dockerfile

// Adding something that is not there yet — stories 7.6 and 7.7.
//
// The splice is InsertAfter's, unchanged and still the only place bytes move.
// What lives here is the two questions InsertAfter deliberately refuses to
// answer, because it was built to execute a position rather than choose one:
//
//	WHERE does it go   after the last instruction of the stage the reader chose
//	                   (7.6), or after the last instruction of the file (7.7).
//	                   Order is semantic in this grammar, so the choice is made
//	                   once, here, and every surface reads the same answer.
//	IS IT SAFE         a file with no stage, an unterminated heredoc, text that
//	                   would become a continuation, a stage name another stage
//	                   already owns. Each returns an exported sentinel and
//	                   touches nothing (CLAUDE.md rule 6).
//
// The byte offsets these functions report are the offsets the write splices at,
// because both come from the same anchor function (AD-14). A preview that
// computed its own position would be a second answer to "where does this go".

import (
	"fmt"
	"strings"
)

// InstructionInsertionPoint is the byte offset a new instruction for stage
// stageIdx would be spliced at: the end of the content of the stage's last
// instruction line. The new line's ending is added at that offset, so the
// offset itself sits BEFORE the existing line's ending and no CRLF is split.
func (f *File) InstructionInsertionPoint(stageIdx int) (int, error) {
	idx, err := f.instructionAnchor(stageIdx)
	if err != nil {
		return 0, err
	}
	return f.contentEnd(f.Instructions[idx].EndLine), nil
}

// InstructionStartPoint is the byte offset a new instruction placed ABOVE the
// instruction at idx would be spliced at — story 9.4's anchor.
//
// It is a second anchor rather than a flag on the first, and the reason is the
// whole of story 9.4: InstructionInsertionPoint appends after a stage's LAST
// instruction, and for an `ARG` that is the one position guaranteed to be
// wrong. An ARG used before it is declared expands to the empty string with no
// error, so "at the end of the stage" is a declaration after its own use — a
// build that succeeds and produces something else.
//
// "Above" means above the target's ATTACHED COMMENT BLOCK, the same run of
// comment lines Delete absorbs when it removes an instruction. A comment
// documenting a RUN documents it wherever the RUN goes, so putting the ARG
// between the two would separate a sentence from the thing it is about.
//
// The BOM. This is the only anchor in this engine that can return offset 0, and
// splicing there would put the new line IN FRONT of a UTF-8 byte order mark —
// which does not fail, it makes the file report zero stages while every
// operation afterwards succeeds at nothing. That defect has already shipped
// here once (see parse.go's `bom`), so the offset steps over it.
//
// There is deliberately no safeToFollow equivalent. An unterminated heredoc
// swallows every following line INTO the instruction that opened it, so a line
// inside one is never an instruction index a caller can pass here; a check for
// it would be a check that cannot fail, and this repository has shipped
// twenty-one of those.
func (f *File) InstructionStartPoint(idx int) (int, error) {
	if idx < 0 || idx >= len(f.Instructions) {
		return 0, fmt.Errorf("instruction index %d out of range (%d instructions)", idx, len(f.Instructions))
	}
	start := f.Instructions[idx].StartLine
	for j := idx - 1; j >= 0; j-- {
		p := f.Instructions[j]
		if p.Kind == KindComment && p.EndLine == start-1 {
			start = p.StartLine
			continue
		}
		break
	}
	at := f.lineStart[start]
	if at == 0 && strings.HasPrefix(string(f.Source), bom) {
		at = len(bom)
	}
	return at, nil
}

// InsertBefore inserts a new instruction line above the instruction at idx,
// above its attached comment block.
//
// It shares everything with InsertAfter except which line it lands on: the same
// line-ending rule (the neighbour's own, never a file-wide majority), the same
// checkText refusals, one splice at one offset. The offset is
// InstructionStartPoint's, so the range a preview reports is the range this
// writes (AD-14).
func (f *File) InsertBefore(idx int, text string) ([]byte, error) {
	at, err := f.InstructionStartPoint(idx)
	if err != nil {
		return nil, err
	}
	line, err := f.instructionLine(text)
	if err != nil {
		return nil, err
	}
	ins := line + f.endingFor(f.Instructions[idx].StartLine)

	out := make([]byte, 0, len(f.Source)+len(ins))
	out = append(out, f.Source[:at]...)
	out = append(out, ins...)
	out = append(out, f.Source[at:]...)
	return out, nil
}

// StageInsertionPoint is the byte offset a new stage's FROM would be spliced
// at.
func (f *File) StageInsertionPoint() (int, error) {
	idx, err := f.stageAnchor()
	if err != nil {
		return 0, err
	}
	return f.contentEnd(f.Instructions[idx].EndLine), nil
}

// InsertInstruction adds one instruction to the end of a build stage.
//
// "The end of the stage" is after its last real instruction, not after its last
// LINE: a trailing comment block belongs to whatever follows it — which is what
// Delete already assumes when it absorbs the comment block above an instruction
// — so a comment that heads the next stage keeps heading it, and a blank line
// separating two stages stays a separator.
func (f *File) InsertInstruction(stageIdx int, text string) ([]byte, error) {
	idx, err := f.instructionAnchor(stageIdx)
	if err != nil {
		return nil, err
	}
	line, err := f.instructionLine(text)
	if err != nil {
		return nil, err
	}
	return f.InsertAfter(idx, line)
}

// InsertStage appends a new build stage: `FROM <image>` with an optional
// `AS <name>`, after the last instruction of the file.
//
// At the end, because a stage's instructions are the ones that follow its FROM.
// Inserting one in the middle would silently move every following instruction
// into the new stage, which is a build that still succeeds and produces
// something else.
func (f *File) InsertStage(image, name string) ([]byte, error) {
	image = strings.TrimSpace(image)
	if image == "" || strings.ContainsAny(image, " \t") {
		return nil, fmt.Errorf("%w: %q is not a single image reference", ErrInsertText, image)
	}
	if err := f.checkStageName(name); err != nil {
		return nil, err
	}
	idx, err := f.stageAnchor()
	if err != nil {
		return nil, err
	}
	line := f.Keyword("FROM") + " " + image
	if name != "" {
		line += " " + f.Keyword("AS") + " " + name
	}
	if _, err := f.checkText(line); err != nil {
		return nil, err
	}
	return f.InsertAfter(idx, line)
}

// Keyword renders a Dockerfile keyword in the casing the file already uses.
//
// This engine never normalises casing — NameRaw exists so it does not — and
// writing `RUN` into a file of `run` lines is a formatting opinion in a file the
// reader did not ask us to tidy. A file with no instructions, or one that mixes
// the two, gets the canonical upper case: there is no house style to copy.
func (f *File) Keyword(canonical string) string {
	lower, upper := 0, 0
	for _, in := range f.Instructions {
		if in.Kind != KindInstruction || in.NameRaw == "" {
			continue
		}
		switch in.NameRaw {
		case strings.ToLower(in.NameRaw):
			lower++
		case strings.ToUpper(in.NameRaw):
			upper++
		}
	}
	if lower > upper {
		return strings.ToLower(canonical)
	}
	return strings.ToUpper(canonical)
}

// instructionLine is the text as it will be written: verbatim, except that a
// keyword the vocabulary recognises takes the file's own casing. A name the
// vocabulary does NOT recognise is left exactly as typed — recasing it would be
// an opinion about a word this engine has already reported it does not know.
func (f *File) instructionLine(text string) (string, error) {
	trimmed, err := f.checkText(text)
	if err != nil {
		return "", err
	}
	name, rest, _ := strings.Cut(trimmed, " ")
	if _, known := Lookup(name); !known {
		return trimmed, nil
	}
	out := f.Keyword(name)
	if rest != "" {
		out += " " + rest
	}
	return out, nil
}

// checkText is R7.4 applied to text that does not exist yet.
func (f *File) checkText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", fmt.Errorf("%w: there is nothing to write", ErrInsertText)
	}
	if strings.ContainsAny(text, "\n\r") {
		return "", fmt.Errorf("%w: it carries its own line break, and choosing where a line breaks is not this tool's decision", ErrInsertText)
	}
	if trimmed[len(trimmed)-1] == f.EscapeChar {
		return "", fmt.Errorf("%w: it ends in %q, which would make the next line a continuation of it",
			ErrInsertText, string(f.EscapeChar))
	}
	return trimmed, nil
}

// checkStageName refuses a name another stage already owns, and one BuildKit
// would not accept. Stage names are matched case-insensitively, so `BUILD` and
// `build` are the same name and one of the two stages would be unreachable.
func (f *File) checkStageName(name string) error {
	if name == "" {
		return nil
	}
	if !legalStageName(name) {
		return fmt.Errorf("%w: %q is not a legal stage name — a letter or digit first, then letters, digits, %q, %q or %q",
			ErrStageName, name, "_", ".", "-")
	}
	for _, i := range f.Stages() {
		in := f.Instructions[i]
		if strings.EqualFold(in.StageName, name) {
			return fmt.Errorf("%w: line %d already declares a stage called %q",
				ErrStageName, in.StartLine+1, in.StageName)
		}
	}
	return nil
}

func legalStageName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '.' || c == '-'):
		default:
			return false
		}
	}
	return name != ""
}

// instructionAnchor is the instruction a new line for stage stageIdx goes
// after. It is the ONE answer to "where does the stage end", read by the write
// and by the offset the preview reports.
func (f *File) instructionAnchor(stageIdx int) (int, error) {
	stages := f.Stages()
	if len(stages) == 0 {
		return 0, fmt.Errorf("%w: the file declares no build stage to add to", ErrNoInsertionPoint)
	}
	if stageIdx < 0 || stageIdx >= len(stages) {
		return 0, fmt.Errorf("stage %d out of range (%d FROM instructions)", stageIdx, len(stages))
	}
	members := f.StageInstructions(stageIdx)
	idx := -1
	for _, i := range members {
		if f.Instructions[i].Kind == KindInstruction {
			idx = i
		}
	}
	if idx < 0 {
		// Unreachable while a stage begins with its own FROM, and asserted
		// rather than assumed: a stage with no instruction has no end to append
		// to, and guessing one is how a line lands in the previous stage.
		return 0, fmt.Errorf("%w: stage %d holds no instruction to insert after", ErrNoInsertionPoint, stageIdx)
	}
	return idx, f.safeToFollow(idx)
}

// stageAnchor is the instruction a new stage's FROM goes after: the file's last
// real instruction, or — in a file that holds none — its last directive, so the
// FROM never lands above a `# syntax=` line and changes how the file is parsed.
func (f *File) stageAnchor() (int, error) {
	idx := -1
	for i, in := range f.Instructions {
		if in.Kind == KindInstruction {
			idx = i
		}
	}
	if idx < 0 {
		for i, in := range f.Instructions {
			if in.Kind == KindDirective {
				idx = i
			}
		}
	}
	if idx < 0 {
		return 0, fmt.Errorf("%w: the file holds no instruction or directive to insert after", ErrNoInsertionPoint)
	}
	return idx, f.safeToFollow(idx)
}

// safeToFollow refuses an anchor whose own extent this engine cannot see the
// end of. An unterminated heredoc swallows every following line to the end of
// the file, so "after this instruction" is inside a shell script, and the
// inserted line would be script rather than an instruction.
func (f *File) safeToFollow(idx int) error {
	if f.Instructions[idx].HeredocOpen {
		return fmt.Errorf("%w: line %d opens a heredoc that is never closed, so the end of it is inside its body",
			ErrNoInsertionPoint, f.Instructions[idx].StartLine+1)
	}
	return nil
}
