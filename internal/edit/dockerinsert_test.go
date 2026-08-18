package edit

// Stories 7.6 and 7.7 through the write path: adding an instruction to a stage,
// and adding a stage.
//
// The engine has been gated at 100% on the corpus since Epic 6 with no caller
// (DECISIONS.md 20). What is new is everything above it, and what these tests
// hold it to is the same rule the rest of this package is held to: the file on
// disk, compared byte for byte, and a refusal that comes back CLASSIFIED as a
// refusal — because a refusal reported as a fault tells the reader the tool
// broke when the tool declined (story 6.5).

import (
	"errors"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/dockerfile"
)

// Two stages, so "into stage 1" and "into stage 0" cannot pass for one another.
const twoStages = `FROM golang:1.24 AS builder
WORKDIR /src
RUN go build ./...

FROM alpine:3.20
COPY --from=builder /src/app /app
CMD ["/app"]
`

func TestApplyInsertInstructionIntoTheStageNamed(t *testing.T) {
	path := write(t, "Dockerfile", twoStages)

	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertInstruction, Stage: 0, Value: "USER app"},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := strings.Replace(twoStages, "RUN go build ./...\n", "RUN go build ./...\nUSER app\n", 1)
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +1 -0", res.Added, res.Removed)
	}
	if res.Ops[0].Range.Start != res.Ops[0].Range.End {
		t.Errorf("an insert must report an empty range, got %+v", res.Ops[0].Range)
	}
	// AD-14: the range the preview reported is where the write landed.
	if got := want[:res.Ops[0].Range.Start]; got != strings.Split(twoStages, "\n\n")[0] {
		t.Errorf("the reported offset %d is not the end of stage 0 (%q)", res.Ops[0].Range.Start, got)
	}
	if !strings.Contains(res.Ops[0].Describe, "stage 0") {
		t.Errorf("describe %q does not name the stage", res.Ops[0].Describe)
	}
}

func TestApplyInsertInstructionIntoTheSecondStage(t *testing.T) {
	path := write(t, "Dockerfile", twoStages)

	if _, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertInstruction, Stage: 1, Value: "USER app"},
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := read(t, path), twoStages+"USER app\n"; got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

// AD-14, for every stage rather than for the one that happens to be first.
//
// THE MUTATION THIS CLOSES: `locate` calling InstructionInsertionPoint(0)
// instead of (op.Stage). The write still lands in the right stage — `perform`
// has its own copy of the index — so every byte assertion above passes, while
// the range the preview reports, the range the pending strip highlights and the
// range AD-19's staleness check compares are all the FIRST stage's. A stale
// check against the wrong span is a check that cannot fire.
func TestReportedRangeIsWhereTheLineLandsInEveryStage(t *testing.T) {
	for _, stage := range []int{0, 1} {
		path := write(t, "Dockerfile", twoStages)
		res, err := Apply(Request{File: path, Ops: []Op{
			{Operation: OpInsertInstruction, Stage: stage, Value: "USER app"},
		}})
		if err != nil {
			t.Fatalf("stage %d: Apply: %v", stage, err)
		}
		at := res.Ops[0].Range.Start
		got := read(t, path)
		// The file is the source with one block spliced in at the offset the
		// operation reported, and nothing else moved.
		want := twoStages[:at] + got[at:len(got)-(len(twoStages)-at)] + twoStages[at:]
		if got != want {
			t.Errorf("stage %d: the write did not land at the reported offset %d\n got: %q\nwant: %q",
				stage, at, got, want)
		}
		if !strings.Contains(got[at:at+len("\nUSER app")], "USER app") {
			t.Errorf("stage %d: the reported offset %d does not point at the inserted line: %q",
				stage, at, got[at:])
		}
	}
}

// Preview and apply are one boolean apart, and preview writes nothing.
func TestPreviewInstructionInsertWritesNothing(t *testing.T) {
	path := write(t, "Dockerfile", twoStages)
	res, err := Preview(Request{File: path, Ops: []Op{
		{Operation: OpInsertInstruction, Stage: 0, Value: "USER app"},
	}})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if res.Written {
		t.Error("a preview reported that it wrote")
	}
	if got := read(t, path); got != twoStages {
		t.Fatalf("a preview changed the file:\n%q", got)
	}
	if !strings.Contains(res.Diff, "+USER app") {
		t.Errorf("the diff does not show the added line:\n%s", res.Diff)
	}
}

func TestApplyInsertStageAppendsAtTheEnd(t *testing.T) {
	path := write(t, "Dockerfile", twoStages)

	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertStage, Value: "nginx:1.27", Key: "serve"},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := read(t, path), twoStages+"FROM nginx:1.27 AS serve\n"; got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +1 -0", res.Added, res.Removed)
	}
	if !strings.Contains(res.Ops[0].Describe, "serve") {
		t.Errorf("describe %q does not name the stage being added", res.Ops[0].Describe)
	}
}

// A stage and its first instruction, as ONE request: the second operation is
// located against the buffer the first produced, so it can address a stage that
// did not exist when the request was built.
func TestStageAndItsFirstInstructionApplyAsOneEdit(t *testing.T) {
	path := write(t, "Dockerfile", twoStages)

	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertStage, Value: "nginx:1.27", Key: "serve"},
		{Operation: OpInsertInstruction, Stage: 2, Value: "CMD [\"nginx\"]"},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := twoStages + "FROM nginx:1.27 AS serve\nCMD [\"nginx\"]\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if len(res.Ops) != 2 {
		t.Fatalf("%d operation results, want 2", len(res.Ops))
	}
	if res.Added != 2 {
		t.Errorf("diff adds %d lines, want 2", res.Added)
	}
}

// Rule 6, and the three-part obligation story 6.5 sets out: the sentinel comes
// back, Refused answers true, and Reason gives a stable slug the extension can
// branch on.
func TestDockerfileInsertRefusalsAreClassifiedAsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		op   Op
		want error
		slug string
	}{
		{
			name: "a file with no stage to add an instruction to",
			src:  "# a note and nothing else\n",
			op:   Op{Operation: OpInsertInstruction, Stage: 0, Value: "USER app"},
			want: dockerfile.ErrNoInsertionPoint,
			slug: "no-insertion-point",
		},
		{
			name: "a file with nothing at all to append a stage after",
			src:  "",
			op:   Op{Operation: OpInsertStage, Value: "alpine:3.20"},
			want: dockerfile.ErrNoInsertionPoint,
			slug: "no-insertion-point",
		},
		{
			name: "instruction text carrying its own line break",
			src:  twoStages,
			op:   Op{Operation: OpInsertInstruction, Stage: 0, Value: "RUN one\nRUN two"},
			want: dockerfile.ErrInsertText,
			slug: "insert-text",
		},
		{
			name: "instruction text ending in the escape character",
			src:  twoStages,
			op:   Op{Operation: OpInsertInstruction, Stage: 0, Value: `RUN echo one \`},
			want: dockerfile.ErrInsertText,
			slug: "insert-text",
		},
		{
			name: "no instruction typed at all",
			src:  twoStages,
			op:   Op{Operation: OpInsertInstruction, Stage: 0, Value: "  "},
			want: dockerfile.ErrInsertText,
			slug: "insert-text",
		},
		{
			name: "a stage name another stage already uses",
			src:  twoStages,
			op:   Op{Operation: OpInsertStage, Value: "alpine:3.20", Key: "builder"},
			want: dockerfile.ErrStageName,
			slug: "stage-name",
		},
		{
			name: "a stage name the grammar would not accept",
			src:  twoStages,
			op:   Op{Operation: OpInsertStage, Value: "alpine:3.20", Key: "not a name"},
			want: dockerfile.ErrStageName,
			slug: "stage-name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, "Dockerfile", tc.src)
			_, err := Apply(Request{File: path, Ops: []Op{tc.op}})
			if err == nil {
				t.Fatal("the operation succeeded")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error is %v, want %v", err, tc.want)
			}
			if !Refused(err) {
				t.Errorf("Refused(%v) is false; the reader would be shown a fault", err)
			}
			if got := Reason(err); got != tc.slug {
				t.Errorf("Reason is %q, want %q", got, tc.slug)
			}
			if got := read(t, path); got != tc.src {
				t.Errorf("a refusal wrote to the file:\n got: %q\nwant: %q", got, tc.src)
			}
		})
	}
}

// The staleness check reaches the new operations too: an insertion point that
// has moved is discarded rather than rebased (AD-19).
func TestStaleInstructionInsertIsRefused(t *testing.T) {
	path := write(t, "Dockerfile", twoStages)
	res, err := Preview(Request{File: path, Ops: []Op{
		{Operation: OpInsertInstruction, Stage: 0, Value: "USER app"},
	}})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	at := res.Ops[0].Range.Start

	// The file grows above the insertion point.
	moved := strings.Replace(twoStages, "WORKDIR /src\n", "WORKDIR /src\nENV LANG=C\n", 1)
	if err := writeFile(path, []byte(moved)); err != nil {
		t.Fatal(err)
	}

	_, err = Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertInstruction, Stage: 0, Value: "USER app", Expect: &Expect{Start: at, End: at}},
	}})
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("error is %v, want ErrStaleRange", err)
	}
	if got := read(t, path); got != moved {
		t.Errorf("a stale insert wrote to the file")
	}
}

// CRLF and the BOM survive, and the new line takes the file's own ending
// (story 7.1). The assertion is on bytes: a diff cannot see a missing "\r".
func TestDockerfileInsertKeepsEndingsAndMark(t *testing.T) {
	const src = "\ufeffFROM alpine:3.20\r\nRUN echo hi\r\n"
	path := write(t, "Dockerfile", src)

	if _, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertInstruction, Stage: 0, Value: "USER app"},
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := read(t, path)
	if got != src+"USER app\r\n" {
		t.Fatalf("\n got: %q\nwant: %q", got, src+"USER app\r\n")
	}
	for i := 0; i < len(got); i++ {
		if got[i] == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Fatalf("byte %d is an LF in a CRLF file: %q", i, got)
		}
	}
}

// The operations belong to the Dockerfile engine, so the result is validated as
// a Dockerfile rather than parsed as YAML.
func TestDockerfileInsertOperationsDeclareTheirGrammar(t *testing.T) {
	for _, op := range []Operation{OpInsertInstruction, OpInsertStage} {
		if got := op.Grammar(); got != "dockerfile" {
			t.Errorf("%s.Grammar() = %q, want %q", op, got, "dockerfile")
		}
	}
}
