package dockerfile

// Stories 7.6 and 7.7 — adding an instruction to a stage, and adding a stage.
//
// InsertAfter has been gated at 100% on the corpus since Epic 6 with no caller.
// What these tests cover is everything the caller has to decide and the engine
// therefore has to answer: WHICH instruction a stage's new line goes after, what
// casing it is written in, and what happens when there is nowhere safe to put
// it. Every placement assertion compares BUFFERS — the result with the one
// inserted line excised has to be the source, byte for byte. "It returned no
// error" would pass on a file the engine had quietly rewritten.

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

/* -------------------------------------------------------------------------
 * Story 7.6: an instruction lands in the stage the reader chose.
 * ---------------------------------------------------------------------- */

// The trap this fixture exists to avoid: a single-stage file makes "insert into
// stage k" and "insert into stage 0" indistinguishable. e21 has two stages, and
// the assertion is that the line lands in the FIRST one — which is also where
// the file's last instruction is NOT.
func TestInsertInstructionLandsInTheStageNamed(t *testing.T) {
	src := edgeDockerfile(t, "e21-crlf.Dockerfile")
	f := Parse(src)
	if len(f.Stages()) != 2 {
		t.Fatalf("fixture has %d stages; this test is worthless with one", len(f.Stages()))
	}

	got, err := f.InsertInstruction(0, "USER app")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	assertNoLoneLF(t, got)
	assertOneInsertedLine(t, src, got, "\r\nUSER app")
	if !bytes.Contains(got, []byte("RUN apk add --no-cache git\r\nUSER app\r\n\r\nFROM alpine:3.20\r\n")) {
		t.Errorf("the instruction did not land at the end of stage 0:\n%q", got)
	}

	// And stage 1's insert lands somewhere else entirely.
	second, err := f.InsertInstruction(1, "USER app")
	if err != nil {
		t.Fatalf("InsertInstruction(1): %v", err)
	}
	if bytes.Equal(second, got) {
		t.Errorf("stage 0 and stage 1 produced the same file; the stage index is being ignored")
	}
	if !bytes.HasSuffix(second, []byte("CMD [\"/out/app\"]\r\nUSER app\r\n")) {
		t.Errorf("stage 1's instruction did not land after its last instruction:\n%q", second)
	}
}

// The last instruction of a stage may be a heredoc or a continuation block, and
// both span lines the caller cannot see from an index. Landing inside one
// produces a file that still builds and does something else entirely.
func TestInsertInstructionClearsAHeredocBody(t *testing.T) {
	src := fixtureDockerfile(t, "Dockerfile.heredoc")
	f := Parse(src)
	got, err := f.InsertInstruction(0, "USER app")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	assertOneInsertedLine(t, src, got, "\nUSER app")
	// Stage 0 ends with `COPY <<-"CONF" …` whose body ends at the CONF line.
	if !bytes.Contains(got, []byte("CONF\nUSER app\nFROM alpine:3.20\n")) {
		t.Errorf("the instruction did not land after the whole heredoc:\n%q", got)
	}
	if bytes.Contains(got, []byte("key = value\nUSER app")) {
		t.Errorf("the instruction landed INSIDE the heredoc body")
	}
}

func TestInsertInstructionClearsAContinuationBlock(t *testing.T) {
	src := fixtureDockerfile(t, "Dockerfile.continuation")
	f := Parse(src)
	got, err := f.InsertInstruction(0, "USER app")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	assertOneInsertedLine(t, src, got, "\nUSER app")
	if !bytes.HasSuffix(got, []byte("EXPOSE 8080\nUSER app\n")) {
		t.Errorf("the instruction did not land after the stage's last instruction:\n%q", got)
	}
}

// A stage whose last line is a comment. The rule, written down rather than
// inherited by accident: a trailing comment belongs to whatever FOLLOWS it —
// which is what Delete already assumes when it absorbs the comment block above
// an instruction — so the new line goes ABOVE the comment, after the last real
// instruction of the stage.
func TestInsertInstructionGoesAboveATrailingComment(t *testing.T) {
	src := edgeDockerfile(t, "e32-stage-trailing-comment.Dockerfile")
	f := Parse(src)
	got, err := f.InsertInstruction(0, "USER app")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	assertOneInsertedLine(t, src, got, "\nUSER app")
	if !bytes.Contains(got, []byte("RUN go build ./...\nUSER app\n\n# the runtime image\nFROM alpine:3.20\n")) {
		t.Errorf("the instruction did not land above the comment that heads the next stage:\n%q", got)
	}
}

// The escape directive and the BOM are each a documented way to corrupt this
// grammar, and both are invisible in the text.
func TestInsertInstructionKeepsTheMarksThatAreInvisible(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want string
	}{
		{"custom escape character", "Dockerfile.escape", "\nUSER app"},
		{"byte order mark", "Dockerfile.bom", "\nUSER app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fixtureDockerfile(t, tc.file)
			f := Parse(src)
			got, err := f.InsertInstruction(0, "USER app")
			if err != nil {
				t.Fatalf("InsertInstruction: %v", err)
			}
			assertOneInsertedLine(t, src, got, tc.want)
			if bytes.HasPrefix(src, []byte(bom)) && !bytes.HasPrefix(got, []byte(bom)) {
				t.Errorf("the byte order mark did not survive")
			}
		})
	}
}

// Casing comes from the file. Writing `RUN` into a file of `run` lines is a
// formatting opinion in a file the reader did not ask us to tidy.
func TestInsertInstructionTakesTheFilesOwnCasing(t *testing.T) {
	lower := Parse(fixtureDockerfile(t, "Dockerfile.lowercase"))
	got, err := lower.InsertInstruction(0, "USER app")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	if !bytes.Contains(got, []byte("\nuser app")) {
		t.Errorf("a lower-cased file was given an upper-cased instruction:\n%q", got)
	}
	if bytes.Contains(got, []byte("USER")) {
		t.Errorf("the upper-cased keyword survived into a lower-cased file:\n%q", got)
	}

	upper := Parse([]byte("FROM alpine:3.20\nRUN echo hi\n"))
	got, err = upper.InsertInstruction(0, "user app")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	if !bytes.Contains(got, []byte("\nUSER app\n")) {
		t.Errorf("an upper-cased file was given a lower-cased instruction:\n%q", got)
	}
}

// An instruction name the vocabulary does not know is written exactly as typed.
// Recasing it would be an opinion about a word this engine has already said it
// does not recognise.
func TestInsertInstructionLeavesAnUnknownNameAlone(t *testing.T) {
	f := Parse([]byte("from alpine:3.20\nrun echo hi\n"))
	got, err := f.InsertInstruction(0, "RUNX something")
	if err != nil {
		t.Fatalf("InsertInstruction: %v", err)
	}
	if !bytes.Contains(got, []byte("\nRUNX something")) {
		t.Errorf("an unrecognised name was recased:\n%q", got)
	}
}

/* -------------------------------------------------------------------------
 * Story 7.6: refusals. CLAUDE.md rule 6 — refuse rather than corrupt.
 * ---------------------------------------------------------------------- */

func TestInsertInstructionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		stage int
		text  string
		want  error
	}{
		{
			name: "a file with no stage to add to",
			src:  "# just a comment\n",
			text: "USER app",
			want: ErrNoInsertionPoint,
		},
		{
			name: "an empty file",
			src:  "",
			text: "USER app",
			want: ErrNoInsertionPoint,
		},
		{
			// An unterminated heredoc swallows the rest of the file, so the end
			// of the stage IS inside a shell script.
			name: "an unterminated heredoc at the end of the stage",
			src:  "FROM alpine:3.20\nRUN <<EOT\napk add curl\n",
			text: "USER app",
			want: ErrNoInsertionPoint,
		},
		{
			name: "text carrying its own newline",
			src:  "FROM alpine:3.20\nRUN echo hi\n",
			text: "USER app\nWORKDIR /app",
			want: ErrInsertText,
		},
		{
			name: "text ending in the escape character",
			src:  "FROM alpine:3.20\nRUN echo hi\n",
			text: `RUN echo one \`,
			want: ErrInsertText,
		},
		{
			name: "text ending in the file's OWN escape character",
			src:  "# escape=`\nFROM alpine:3.20\nRUN echo hi\n",
			text: "RUN echo one `",
			want: ErrInsertText,
		},
		{
			name: "nothing typed at all",
			src:  "FROM alpine:3.20\nRUN echo hi\n",
			text: "   ",
			want: ErrInsertText,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			f := Parse(src)
			got, err := f.InsertInstruction(tc.stage, tc.text)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got != nil {
				t.Errorf("a refusal returned %d bytes; it must touch nothing", len(got))
			}
			if !bytes.Equal(f.Source, src) {
				t.Errorf("the source buffer was modified by a refused insert")
			}
		})
	}
}

// A stage index outside the file is a caller bug, not a refusal: nothing about
// the file made it unsafe.
func TestInsertInstructionOutOfRangeIsNotARefusal(t *testing.T) {
	f := Parse([]byte("FROM alpine:3.20\nRUN echo hi\n"))
	_, err := f.InsertInstruction(4, "USER app")
	if err == nil {
		t.Fatal("stage 4 of a one-stage file succeeded")
	}
	if errors.Is(err, ErrNoInsertionPoint) || errors.Is(err, ErrInsertText) {
		t.Errorf("an out-of-range index is reported as a refusal: %v", err)
	}
}

/* -------------------------------------------------------------------------
 * Story 7.7: a new stage.
 * ---------------------------------------------------------------------- */

func TestInsertStageAppendsAtTheEndOfTheFile(t *testing.T) {
	src := edgeDockerfile(t, "e21-crlf.Dockerfile")
	f := Parse(src)
	got, err := f.InsertStage("alpine:3.20", "runtime")
	if err != nil {
		t.Fatalf("InsertStage: %v", err)
	}
	assertNoLoneLF(t, got)
	assertOneInsertedLine(t, src, got, "\r\nFROM alpine:3.20 AS runtime")
	if !bytes.HasSuffix(got, []byte("CMD [\"/out/app\"]\r\nFROM alpine:3.20 AS runtime\r\n")) {
		t.Errorf("the new stage did not land at the end of the file:\n%q", got)
	}
	if len(Parse(got).Stages()) != 3 {
		t.Errorf("the new FROM did not parse as a stage")
	}
}

// No name means no AS clause. Nothing is invented.
func TestInsertStageWithoutANameWritesNoAsClause(t *testing.T) {
	src := []byte("FROM alpine:3.20\nRUN echo hi\n")
	got, err := Parse(src).InsertStage("golang:1.24", "")
	if err != nil {
		t.Fatalf("InsertStage: %v", err)
	}
	if string(got) != "FROM alpine:3.20\nRUN echo hi\nFROM golang:1.24\n" {
		t.Fatalf("\n got: %q", got)
	}
}

// An ARG preamble decides what the existing stages build FROM. It is never
// moved, copied, or written past.
func TestInsertStageLeavesThePreambleAlone(t *testing.T) {
	src := fixtureDockerfile(t, "Dockerfile.tricky")
	f := Parse(src)
	got, err := f.InsertStage("alpine:3.20", "extra")
	if err != nil {
		t.Fatalf("InsertStage: %v", err)
	}
	assertOneInsertedLine(t, src, got, "\nFROM alpine:3.20 AS extra")
	if !bytes.HasPrefix(got, []byte("ARG NODE_VERSION=22\nFROM node:")) {
		t.Errorf("the preamble moved:\n%q", got)
	}
	if bytes.Count(got, []byte("ARG NODE_VERSION=22")) != 1 {
		t.Errorf("the preamble was duplicated")
	}
}

// A file that is nothing but directives: the FROM goes AFTER them. Writing at
// offset zero would land above `# syntax=` and change how the file is parsed.
func TestInsertStageGoesBelowTheDirectives(t *testing.T) {
	src := edgeDockerfile(t, "e34-directives-only.Dockerfile")
	f := Parse(src)
	if len(f.Stages()) != 0 {
		t.Fatalf("fixture declares %d stages; it must declare none", len(f.Stages()))
	}
	got, err := f.InsertStage("alpine:3.20", "")
	if err != nil {
		t.Fatalf("InsertStage: %v", err)
	}
	assertOneInsertedLine(t, src, got, "\nFROM alpine:3.20")
	if !bytes.HasPrefix(got, []byte("# syntax=docker/dockerfile:1\n")) {
		t.Errorf("the new FROM landed above the syntax directive:\n%q", got)
	}
	if Parse(got).EscapeChar != '`' {
		t.Errorf("the escape directive stopped being honoured: the new line broke the leading block")
	}
}

func TestInsertStageTakesTheFilesOwnCasing(t *testing.T) {
	src := fixtureDockerfile(t, "Dockerfile.lowercase")
	got, err := Parse(src).InsertStage("alpine:3.20", "runtime")
	if err != nil {
		t.Fatalf("InsertStage: %v", err)
	}
	if !bytes.Contains(got, []byte("\nfrom alpine:3.20 as runtime")) {
		t.Errorf("the new stage was written in a casing the file does not use:\n%q", got)
	}
}

func TestInsertStageRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		image string
		stage string
		want  error
	}{
		{
			name:  "a name another stage already uses",
			src:   "FROM alpine:3.20 AS build\nRUN echo hi\n",
			image: "alpine:3.20",
			stage: "build",
			want:  ErrStageName,
		},
		{
			name:  "the same name in another casing — BuildKit matches case-insensitively",
			src:   "FROM alpine:3.20 AS build\n",
			image: "alpine:3.20",
			stage: "BUILD",
			want:  ErrStageName,
		},
		{
			name:  "a name that is not a legal stage name",
			src:   "FROM alpine:3.20\n",
			image: "alpine:3.20",
			stage: "my stage!",
			want:  ErrStageName,
		},
		{
			name:  "no image at all",
			src:   "FROM alpine:3.20\n",
			image: "  ",
			want:  ErrInsertText,
		},
		{
			name:  "an image reference carrying a space",
			src:   "FROM alpine:3.20\n",
			image: "alpine:3.20 AS sneaky",
			want:  ErrInsertText,
		},
		{
			name:  "a file with nothing in it at all",
			src:   "",
			image: "alpine:3.20",
			want:  ErrNoInsertionPoint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			got, err := Parse(src).InsertStage(tc.image, tc.stage)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got != nil {
				t.Errorf("a refusal returned %d bytes", len(got))
			}
		})
	}
}

// A duplicate name is refused NAMING THE LINE that already uses it — the reader
// has to be able to go and look at it.
func TestInsertStageDuplicateNamesTheLine(t *testing.T) {
	src := []byte("FROM alpine:3.20\n\nFROM golang:1.24 AS build\n")
	_, err := Parse(src).InsertStage("alpine:3.20", "build")
	if err == nil {
		t.Fatal("a duplicate stage name was accepted")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the refusal does not name the line that already declares it: %v", err)
	}
}

/* -------------------------------------------------------------------------
 * AD-14: the range a preview reports is the range the write touches.
 * ---------------------------------------------------------------------- */

func TestInsertionPointsAgreeWithTheWrite(t *testing.T) {
	for _, name := range []string{
		"e21-crlf.Dockerfile",
		"e32-stage-trailing-comment.Dockerfile",
	} {
		t.Run(name, func(t *testing.T) {
			src := edgeDockerfile(t, name)
			f := Parse(src)

			at, err := f.InstructionInsertionPoint(0)
			if err != nil {
				t.Fatalf("InstructionInsertionPoint: %v", err)
			}
			got, err := f.InsertInstruction(0, "USER app")
			if err != nil {
				t.Fatalf("InsertInstruction: %v", err)
			}
			if !bytes.Equal(got[:at], src[:at]) {
				t.Errorf("the write changed bytes before the offset it reported")
			}
			if !bytes.Equal(got[len(got)-(len(src)-at):], src[at:]) {
				t.Errorf("the write changed bytes after the offset it reported")
			}

			sat, err := f.StageInsertionPoint()
			if err != nil {
				t.Fatalf("StageInsertionPoint: %v", err)
			}
			stage, err := f.InsertStage("alpine:3.20", "extra")
			if err != nil {
				t.Fatalf("InsertStage: %v", err)
			}
			if !bytes.Equal(stage[:sat], src[:sat]) {
				t.Errorf("the stage write changed bytes before the offset it reported")
			}
		})
	}
}

// The membership the form displays and the membership the insert uses are one
// function. Two would eventually disagree about which stage an instruction is
// in, and the reader's HEALTHCHECK would land in the builder.
func TestStageInstructionsMatchesTheForm(t *testing.T) {
	src := fixtureDockerfile(t, "Dockerfile.heredoc")
	f := Parse(src)
	form := BuildForm("Dockerfile", src)
	for k, stage := range form.Stages {
		members := f.StageInstructions(k)
		if len(members) == 0 || members[0] != f.Stages()[k] {
			t.Fatalf("stage %d membership does not begin with its FROM: %v", k, members)
		}
		if got, want := len(members)-1, len(stage.Instructions); got != want {
			t.Errorf("stage %d: the insert path sees %d instructions, the form draws %d", k, got, want)
		}
	}
}
