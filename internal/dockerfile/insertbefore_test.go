package dockerfile

// Story 9.4's anchor: the ONE position an `ARG` can legally take.
//
// 7.6's InstructionInsertionPoint appends after a stage's last instruction,
// which for an ARG is the one position guaranteed to be wrong — an ARG used
// before it is declared expands to the empty string with no error. So this is
// a second anchor over the same splice arithmetic, and these tests exist to
// hold three properties that a "did it error" test cannot see:
//
//	The comment block above the target moves WITH the target. A comment
//	documenting a RUN keeps documenting it; the ARG goes above the block.
//
//	The offset the preview reports is the offset the write splices at (AD-14).
//	Asserted by reconstructing the buffer from the offset and comparing bytes.
//
//	A UTF-8 BOM stays the first bytes of the file. Inserting above the first
//	line is the only insert in this engine that can put bytes in front of it,
//	and a BOM in the middle of a file makes every stage vanish silently.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// instructionStartingWith is the instruction index whose source line starts with
// prefix. Written here rather than assumed, because an index typed as a literal
// is an index that silently addresses something else the day the fixture gains
// a line.
func instructionStartingWith(t *testing.T, f *File, prefix string) int {
	t.Helper()
	lines := strings.Split(string(f.Source), "\n")
	for i, in := range f.Instructions {
		if in.Kind != KindInstruction {
			continue
		}
		// The BOM is stripped here for the same reason parse.go strips it: it is
		// not whitespace, so a fixture that carries one has a first line
		// beginning "<BOM>FROM" and matches nothing.
		if strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(lines[in.StartLine]), bom), prefix) {
			return i
		}
	}
	t.Fatalf("no instruction starting %q", prefix)
	return -1
}

func TestInsertBeforeLandsAboveTheAttachedCommentBlock(t *testing.T) {
	src := fixtureBytes(t, "e51-two-stage-arg.Dockerfile")
	f := Parse(src)
	idx := instructionStartingWith(t, f, "ENV CGO_ENABLED")

	out, err := f.InsertBefore(idx, "ARG CGO_ENABLED=0")
	if err != nil {
		t.Fatal(err)
	}
	// The whole buffer, not a Contains. The comment must still sit directly
	// above the ENV it documents, with the ARG above the pair.
	want := strings.Replace(string(src),
		"# Cgo is off so the binary is static.\nENV CGO_ENABLED=0",
		"ARG CGO_ENABLED=0\n# Cgo is off so the binary is static.\nENV CGO_ENABLED=0", 1)
	if string(out) != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", out, want)
	}
}

func TestInstructionStartPointIsTheOffsetTheWriteSplicesAt(t *testing.T) {
	src := fixtureBytes(t, "e51-two-stage-arg.Dockerfile")
	f := Parse(src)
	idx := instructionStartingWith(t, f, "ENV APP_PORT")

	at, err := f.InstructionStartPoint(idx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.InsertBefore(idx, "ARG APP_PORT=8080")
	if err != nil {
		t.Fatal(err)
	}
	// AD-14: one anchor, two readers. Reconstructing the write from the
	// reported offset is the only assertion that catches the two drifting.
	rebuilt := string(src[:at]) + "ARG APP_PORT=8080\n" + string(src[at:])
	if string(out) != rebuilt {
		t.Errorf("the reported offset %d does not produce the written bytes.\n got: %q\nwant: %q", at, out, rebuilt)
	}
}

func TestInsertBeforeTheFirstLineKeepsTheBomFirstAndTheCrlf(t *testing.T) {
	src := fixtureBytes(t, "e53-bom-crlf-arg.Dockerfile")
	f := Parse(src)
	idx := instructionStartingWith(t, f, "FROM")

	out, err := f.InsertBefore(idx, "ARG NODE_VERSION=18")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "\ufeffARG NODE_VERSION=18\r\nFROM node:18\r\n") {
		t.Errorf("the BOM or the CRLF did not survive: %q", out)
	}
	if strings.Count(string(out), "\ufeff") != 1 {
		t.Errorf("the BOM appears %d times: %q", strings.Count(string(out), "\ufeff"), out)
	}
	// And the rest of the file is untouched, byte for byte.
	if got, want := string(out), "\ufeffARG NODE_VERSION=18\r\n"+strings.TrimPrefix(string(src), "\ufeff"); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestInsertBeforeRefusesAnIndexOutsideTheFile(t *testing.T) {
	f := Parse(fixtureBytes(t, "e51-two-stage-arg.Dockerfile"))
	if _, err := f.InsertBefore(len(f.Instructions), "ARG X=1"); err == nil {
		t.Error("an index past the end was accepted")
	}
	if _, err := f.InstructionStartPoint(-1); err == nil {
		t.Error("a negative index was accepted")
	}
}

func TestInsertBeforeRefusesTextItCannotWriteAsOneLine(t *testing.T) {
	f := Parse(fixtureBytes(t, "e51-two-stage-arg.Dockerfile"))
	idx := instructionStartingWith(t, f, "ENV APP_PORT")
	for _, text := range []string{"", "ARG A=1\nARG B=2", "ARG A=1 \\"} {
		if _, err := f.InsertBefore(idx, text); err == nil {
			t.Errorf("%q was accepted as one instruction line", text)
		}
	}
}
