package dockerfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Story 7.1, Dockerfile half.
//
// The same defect as the YAML engine's and NOT the same code: InsertAfter split
// the source on "\n", appended the new text as a bare element, and rejoined on
// "\n", so the inserted instruction carried no line ending of its own and a CRLF
// file came back with one LF line in it. CLAUDE.md says the two engines stay
// separate — different grammar, no unification — so the fix is made twice, each
// in its own package, and what is shared between them is this fixture
// requirement rather than a helper.
//
// dockerbench cannot see any of it: its singleBlockDiff splits on "\n", so a
// line missing its "\r" still scores as one clean inserted block, and
// dockerbench.insert_after_clean_pct read 100 with the defect present. Exactly
// 1 of 152 corpus Dockerfiles is CRLF.

func edgeDockerfile(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatalf("regression file %s unavailable: %v", name, err)
	}
	return src
}

// fixtureDockerfile reads one of the committed Dockerfile fixtures — the
// grammar's documented ways to corrupt a file, kept as real files rather than
// as string literals so every engine test meets the same bytes.
func fixtureDockerfile(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "dockerfiles", name))
	if err != nil {
		t.Fatalf("fixture %s unavailable: %v", name, err)
	}
	return src
}

// assertOneInsertedLine asserts on BUFFERS: the result must be the source with
// want spliced in at some single offset, and nothing else changed. "InsertAfter
// returned no error" is worth nothing here.
func assertOneInsertedLine(t *testing.T, src, got []byte, want string) {
	t.Helper()
	for i := 0; i <= len(src); i++ {
		cand := make([]byte, 0, len(got))
		cand = append(cand, src[:i]...)
		cand = append(cand, want...)
		cand = append(cand, src[i:]...)
		if bytes.Equal(cand, got) {
			return
		}
	}
	t.Errorf("the result is not the source with %q inserted at one offset\n got: %q", want, got)
}

func assertNoLoneLF(t *testing.T, got []byte) {
	t.Helper()
	for i, b := range got {
		if b == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Errorf("byte %d is an LF not preceded by CR; the file is CRLF and this line is not", i)
		}
	}
}

// An instruction inserted into a CRLF Dockerfile ends CRLF, and lands after the
// instruction the caller named rather than at the end of the file.
func TestInsertAfterCRLFGetsCRLFLine(t *testing.T) {
	src := edgeDockerfile(t, "e21-crlf.Dockerfile")
	f := Parse(src)
	idx := f.Find("RUN")
	if idx < 0 {
		t.Fatalf("fixture has no RUN")
	}
	got, err := f.InsertAfter(idx, "USER app")
	if err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	assertNoLoneLF(t, got)
	assertOneInsertedLine(t, src, got, "\r\nUSER app")
	if !bytes.Contains(got, []byte("RUN apk add --no-cache git\r\nUSER app\r\n\r\nFROM alpine:3.20\r\n")) {
		t.Errorf("the new instruction did not land after the RUN of stage 1:\n%q", got)
	}
}

// Mixed endings: the new line copies the instruction it is inserted after, and
// the file's mixture is left as it was.
func TestInsertAfterMixedEndingsCopiesTheNeighbour(t *testing.T) {
	src := edgeDockerfile(t, "e22-mixed-endings.Dockerfile")
	f := Parse(src)

	// Instruction 1 (`RUN echo one`) ends CRLF.
	crlf, err := f.InsertAfter(1, "USER app")
	if err != nil {
		t.Fatalf("InsertAfter 1: %v", err)
	}
	assertOneInsertedLine(t, src, crlf, "\r\nUSER app")

	// Instruction 2 (`RUN echo two`) ends LF.
	lf, err := f.InsertAfter(2, "USER app")
	if err != nil {
		t.Fatalf("InsertAfter 2: %v", err)
	}
	assertOneInsertedLine(t, src, lf, "\nUSER app")
	if bytes.Count(lf, []byte("\r\n")) != bytes.Count(src, []byte("\r\n")) {
		t.Errorf("the file's mixture of endings changed: %d CRLF before, %d after",
			bytes.Count(src, []byte("\r\n")), bytes.Count(lf, []byte("\r\n")))
	}
}

// BOM, CRLF and no trailing newline at once. Appending after the last
// instruction adds exactly one separator, of the file's own kind, invents no
// trailing newline, and leaves the mark alone.
func TestInsertAfterBOMCRLFNoFinalNewline(t *testing.T) {
	src := edgeDockerfile(t, "e23-bom-crlf-no-final-nl.Dockerfile")
	f := Parse(src)
	last := len(f.Instructions) - 1
	got, err := f.InsertAfter(last, "USER app")
	if err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	if !bytes.HasPrefix(got, []byte(bom)) {
		t.Errorf("the byte order mark did not survive the insert")
	}
	assertNoLoneLF(t, got)
	assertOneInsertedLine(t, src, got, "\r\nUSER app")
	if bytes.HasSuffix(got, []byte("\n")) {
		t.Errorf("a file that ended without a newline now ends with one:\n%q", got)
	}
}

// LF Dockerfiles are unchanged by the fix — every corpus file but one is LF, so
// a behaviour change here would move dockerbench.
func TestInsertAfterLFUnchanged(t *testing.T) {
	src := []byte("FROM alpine:3.20\nRUN echo hi\nEXPOSE 80\n")
	got, err := Parse(src).InsertAfter(1, "USER app")
	if err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	const want = "FROM alpine:3.20\nRUN echo hi\nUSER app\nEXPOSE 80\n"
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// A multi-line instruction: the new line goes after the whole continuation
// block, not into the middle of it, and the block's own endings are untouched.
func TestInsertAfterContinuationBlockCRLF(t *testing.T) {
	src := []byte("FROM alpine:3.20\r\nRUN apk add \\\r\n    git \\\r\n    curl\r\nEXPOSE 80\r\n")
	f := Parse(src)
	got, err := f.InsertAfter(1, "USER app")
	if err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	assertNoLoneLF(t, got)
	assertOneInsertedLine(t, src, got, "\r\nUSER app")
	if !bytes.Contains(got, []byte("    curl\r\nUSER app\r\nEXPOSE 80\r\n")) {
		t.Errorf("the new line did not land after the continuation block:\n%q", got)
	}
}
