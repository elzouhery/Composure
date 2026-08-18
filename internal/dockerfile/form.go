package dockerfile

// The stage form — story 6.2's model.
//
// A Dockerfile is a linear list of instructions grouped by FROM. That is what
// the reader gets: one group per build stage, instructions in order. R5.6 says
// no canvas here, and it is right — a graph over a list adds a layout to
// something that already has one, and the dependency it would draw (stage B
// COPYs --from=A) is a single arrow that a `--from` label already states.
//
// This shape is derived, never a second parse. Every offset below comes from
// the instruction the parser already recorded, so the range the form reports is
// the range internal/edit splices. Nothing here re-reads the source text except
// to slice out the exact bytes of a line for display.
//
// What the form deliberately does NOT do is normalise. `from`, `FROM` and
// `From` render as they are written, flags render in their original order, and
// a trailing comment stays on its line. The reader is looking at their file.

import (
	"path/filepath"
	"strings"
)

// InstructionView is one instruction as the form shows it.
type InstructionView struct {
	// Index is the position in File.Instructions — the handle an edit uses.
	Index int `json:"index"`
	// Kind is "instruction", "comment", "blank" or "directive".
	Kind string `json:"kind"`
	// Name is the upper-cased instruction name, empty for anything that is not
	// an instruction. NameRaw is exactly what the file writes.
	Name    string `json:"name,omitempty"`
	NameRaw string `json:"name_raw,omitempty"`
	// Flags are the instruction's --flags, in the order written.
	Flags []string `json:"flags,omitempty"`
	// Args is the body with continuations joined — what Docker executes.
	Args string `json:"args,omitempty"`
	// Text is the instruction's source bytes, verbatim, continuations and
	// heredoc body included. This is what a reader compares against their file.
	Text string `json:"text"`
	// StartLine and EndLine are 1-based and inclusive.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	// Editable is false for anything the engine will not rewrite in place: a
	// multi-line instruction (R7.4), a comment, a blank. A form that offers a
	// field it will then refuse to save is worse than one that does not.
	Editable bool `json:"editable"`
	// NotEditable says why, in one sentence, when Editable is false.
	NotEditable string `json:"not_editable,omitempty"`
	// Heredoc marks an instruction carrying a heredoc body.
	Heredoc bool `json:"heredoc,omitempty"`
	// Known reports whether the grammar vocabulary recognises Name. Stated
	// rather than silently assumed true: an engine that showed `RUNX` as a
	// normal instruction would be telling the reader their typo is valid.
	Known bool `json:"known"`
	// Summary is the vocabulary's one-line description of the instruction,
	// empty for a comment, a blank, or a name the vocabulary does not know.
	Summary string `json:"summary,omitempty"`

	// FROM only.
	ImageRef string `json:"image_ref,omitempty"`
	// ImageStart and ImageEnd are the byte range SetBaseImage overwrites — the
	// reference alone, never the --platform flag, the AS clause or the comment.
	ImageStart int    `json:"image_start,omitempty"`
	ImageEnd   int    `json:"image_end,omitempty"`
	StageName  string `json:"stage_name,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

// StageView is one build stage: its FROM and everything up to the next one.
type StageView struct {
	// Index is the zero-based index over FROM instructions — the handle
	// SetBaseImage takes.
	Index int `json:"index"`
	// Name is the AS clause, or the empty string for an unnamed stage.
	Name string `json:"name"`
	// Label is what the form heads the group with: the AS name if there is
	// one, otherwise the image reference. Never an invented "stage 0".
	Label        string            `json:"label"`
	ImageRef     string            `json:"image_ref"`
	Platform     string            `json:"platform,omitempty"`
	From         InstructionView   `json:"from"`
	Instructions []InstructionView `json:"instructions"`
	// Vocabulary is what this stage uses and, therefore, what it does not —
	// the Dockerfile answer to compose's `available, not set`. It is scoped to
	// the stage because that is the scope a Dockerfile instruction has: a USER
	// set in the builder stage says nothing about the runtime stage.
	Vocabulary VocabularyView `json:"vocabulary"`
}

// Form is the whole answer for one Dockerfile.
type Form struct {
	// Path is the file as the caller named it.
	Path string `json:"path"`
	// Missing is true when the file the caller asked for is not there. It is
	// not an error: story 6.3 says a build that names a Dockerfile which does
	// not exist renders as missing and raises a finding, never vanishes.
	Missing bool `json:"missing"`
	// Context and Dockerfile echo the build section this file was reached
	// through, empty when the reader opened the file directly.
	Context    string `json:"context,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	// EscapeChar is the continuation character in force: "\\", or "`" when a
	// `# escape=` directive says so.
	EscapeChar string `json:"escape_char"`
	// CRLF and BOM are stated rather than silently handled, because both are
	// ways this engine has produced a confident wrong answer before.
	CRLF bool `json:"crlf"`
	BOM  bool `json:"bom"`
	// Directives are the parser directives in the leading comment block.
	Directives []InstructionView `json:"directives"`
	Stages     []StageView       `json:"stages"`
	// Preamble is anything before the first FROM that is not a directive —
	// ARGs used in the first FROM live here, and dropping them would hide the
	// declaration that decides the base image.
	Preamble []InstructionView `json:"preamble"`
	// Vocabulary is the whole file's split: every instruction the grammar
	// permits, which of them the file uses, and therefore which it does not.
	// Same shape as stack/schema's node, so one renderer draws both.
	Vocabulary VocabularyView `json:"vocabulary"`
}

// BuildForm reads a Dockerfile into the stage form.
func BuildForm(filePath string, src []byte) *Form {
	f := Parse(src)
	form := &Form{
		Path:       filePath,
		EscapeChar: string(f.EscapeChar),
		CRLF:       hasCRLF(src),
		BOM:        strings.HasPrefix(string(src), bom),
		Directives: []InstructionView{},
		Stages:     []StageView{},
		Preamble:   []InstructionView{},
	}

	lines := strings.Split(string(src), "\n")
	view := func(i int) InstructionView {
		return viewOf(f, i, lines)
	}

	var current *StageView
	stageIdx := 0
	for i, in := range f.Instructions {
		v := view(i)
		switch {
		case in.Kind == KindDirective:
			form.Directives = append(form.Directives, v)
		case in.Name == "FROM":
			form.Stages = append(form.Stages, StageView{
				Index:        stageIdx,
				Name:         in.StageName,
				Label:        stageLabel(in),
				ImageRef:     in.ImageRef,
				Platform:     in.PlatformFlag,
				From:         v,
				Instructions: []InstructionView{},
			})
			current = &form.Stages[len(form.Stages)-1]
			stageIdx++
		case current == nil:
			form.Preamble = append(form.Preamble, v)
		default:
			current.Instructions = append(current.Instructions, v)
		}
	}

	all := make([]int, len(f.Instructions))
	for i := range f.Instructions {
		all[i] = i
	}
	form.Vocabulary = vocabularyFor(f, "file", all)
	// Per-stage membership comes from the parser's own answer rather than being
	// collected again in the loop above, so the vocabulary counts what the form
	// displays and the insert appends where the vocabulary said the stage ends
	// — one function, three readers (story 7.6).
	for k := range form.Stages {
		form.Stages[k].Vocabulary = vocabularyFor(f, "stage", f.StageInstructions(k))
	}
	return form
}

// MissingForm is the answer for a build that names a Dockerfile which is not
// there. It carries the path so the reader is told WHICH file is missing.
func MissingForm(filePath, context, dockerfilePath string) *Form {
	return &Form{
		Path:       filePath,
		Missing:    true,
		Context:    context,
		Dockerfile: dockerfilePath,
		EscapeChar: `\`,
		Directives: []InstructionView{},
		Stages:     []StageView{},
		Preamble:   []InstructionView{},
		// A file that is not there declares nothing, so every instruction the
		// grammar permits is available. Populated rather than left null: the
		// field's absence would read to a client as "no vocabulary", which is
		// a different claim from "this file uses none of it".
		Vocabulary: vocabularyFor(&File{}, "file", nil),
	}
}

func viewOf(f *File, i int, lines []string) InstructionView {
	in := f.Instructions[i]
	v := InstructionView{
		Index:     i,
		Kind:      kindName(in.Kind),
		Name:      in.Name,
		NameRaw:   in.NameRaw,
		Flags:     in.Flags,
		Args:      in.Args,
		StartLine: in.StartLine + 1,
		EndLine:   in.EndLine + 1,
		Heredoc:   in.HasHeredoc,
		Known:     in.Known,
	}
	if spec, ok := Lookup(in.Name); ok {
		v.Summary = spec.Summary
	}
	if in.StartLine < len(lines) && in.EndLine < len(lines) {
		v.Text = strings.Join(lines[in.StartLine:in.EndLine+1], "\n")
	}
	if in.Name == "FROM" {
		v.ImageRef = in.ImageRef
		v.ImageStart = in.ImageStart
		v.ImageEnd = in.ImageEnd
		v.StageName = in.StageName
		v.Platform = in.PlatformFlag
	}

	switch {
	case in.Kind != KindInstruction:
		v.Editable = false
	case in.EndLine != in.StartLine:
		// R7.4, said in the form rather than discovered at save time.
		v.Editable = false
		v.NotEditable = "this instruction spans several lines; rewriting it would need a line-break policy, so Composure will not do it"
	default:
		v.Editable = true
	}
	return v
}

func kindName(k Kind) string {
	switch k {
	case KindComment:
		return "comment"
	case KindBlank:
		return "blank"
	case KindDirective:
		return "directive"
	}
	return "instruction"
}

func stageLabel(in Instruction) string {
	if in.StageName != "" {
		return in.StageName
	}
	return in.ImageRef
}

func hasCRLF(src []byte) bool {
	for i := 0; i+1 < len(src); i++ {
		if src[i] == '\r' && src[i+1] == '\n' {
			return true
		}
	}
	return false
}

// ResolveBuild joins a compose `build:` section into the Dockerfile path it
// names, relative to the directory holding the compose file.
//
// It is path arithmetic and touches no filesystem, so it can be used by the
// diagnostic that checks existence and by the RPC that opens the file, and the
// two cannot disagree about which file was meant.
//
// ok is false when the answer would be a guess: a git or URL context, or a
// context or filename still holding an uninterpolated `${VAR}`. Reporting "this
// file does not exist" for `${BUILD_CONTEXT}/api` would be a confident wrong
// answer about a path that was never meant to be read literally.
func ResolveBuild(baseDir, context, dockerfilePath string) (resolved string, ok bool) {
	if strings.Contains(context, "${") || strings.Contains(dockerfilePath, "${") {
		return "", false
	}
	if strings.Contains(context, "://") || strings.HasPrefix(context, "git@") {
		return "", false
	}
	name := dockerfilePath
	if name == "" {
		name = "Dockerfile"
	}
	// An absolute or home-relative filename is already the whole answer;
	// joining a context onto it would invent a path the file does not contain.
	if filepath.IsAbs(name) || strings.HasPrefix(name, "~") {
		return name, true
	}
	dir := baseDir
	if context != "" {
		if filepath.IsAbs(context) || strings.HasPrefix(context, "~") {
			dir = context
		} else {
			dir = filepath.Join(baseDir, filepath.FromSlash(context))
		}
	}
	if dir == "" {
		return filepath.FromSlash(name), true
	}
	return filepath.Join(dir, filepath.FromSlash(name)), true
}
