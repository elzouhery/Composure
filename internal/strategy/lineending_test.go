package strategy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Story 7.1, YAML half: new content carries the file's own line ending.
//
// The defect: InsertKey rebuilt the document by splitting on "\n" and rejoining
// on "\n", and the new line was appended with no ending of its own. A CRLF
// file's existing lines keep their "\r" INSIDE the line text, which is why the
// old bytes survived and only the new one came out wrong —
// `...image: nginx\r\n  cache:\n`, an LF line in a CRLF file.
//
// Why the tests here are byte comparisons and not diffs. Both gates compare
// with a line-oriented singleBlockDiff that splits on "\n", so a line missing
// its "\r" still scores as one clean inserted block: structbench and dockerbench
// read 100% with the defect present. The corpus cannot see it either — 0 of 146
// corpus compose files use CRLF. The fixtures ARE the test.

func edgeSrc(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatalf("regression file %s unavailable: %v", name, err)
	}
	return src
}

// assertSingleInsertion is the epic-7 constraint as an assertion: the result
// with the inserted block excised must equal the source BYTE FOR BYTE. It
// returns the block so a caller can pin its contents.
//
// Comparing buffers rather than verdicts is the whole point. "InsertKey
// returned no error" is worth nothing against an engine whose characteristic
// failure is a confident wrong answer.
func assertSingleInsertion(t *testing.T, src, got []byte) string {
	t.Helper()
	if len(got) <= len(src) {
		t.Fatalf("result is not longer than the source: %d vs %d bytes", len(got), len(src))
	}
	n := 0
	for n < len(src) && src[n] == got[n] {
		n++
	}
	m := 0
	for m < len(src)-n && src[len(src)-1-m] == got[len(got)-1-m] {
		m++
	}
	rebuilt := append(append([]byte{}, got[:n]...), got[len(got)-m:]...)
	if !bytes.Equal(rebuilt, src) {
		t.Fatalf("excising the inserted block does not restore the source\n got: %q\nwant: %q", rebuilt, src)
	}
	return string(got[n : len(got)-m])
}

// assertInsertion pins WHAT was inserted without depending on how a prefix
// match attributes it: it asserts there is some offset at which splicing want
// into the source produces exactly the result. Inserting "\n  x" before a
// newline and "  x\n" after it are the same buffer, and a test that insisted on
// one spelling would fail for a reason that is not about the bytes.
func assertInsertion(t *testing.T, src, got []byte, want string) {
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
	t.Errorf("the result is not the source with %q inserted anywhere in it\n got: %q", want, got)
}

// assertNoLoneLF is the criterion stated in its own terms: the file contains no
// LF that is not preceded by a CR. It is the check a line-oriented diff cannot
// perform, and its absence is what let this ship.
func assertNoLoneLF(t *testing.T, got []byte) {
	t.Helper()
	for i, b := range got {
		if b == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Errorf("byte %d is an LF not preceded by CR; the file is CRLF and this line is not", i)
		}
	}
}

// A key inserted into a CRLF file ends CRLF. The fixture is four-space indented
// so that the assertion also fails an insert that assumed the file's step was 2.
func TestInsertKeyCRLFFileGetsCRLFLine(t *testing.T) {
	src := edgeSrc(t, "e18-crlf-four-space.yml")
	got, err := Splice{}.InsertKey(src, []string{"services", "web"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	assertNoLoneLF(t, got)
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\r\n        restart: always")
	// Said again as placement, because a line with the right ending in the wrong
	// place is still wrong.
	if !bytes.Contains(got, []byte("            - \"8080:80\"\r\n        restart: always\r\n    cache:\r\n")) {
		t.Errorf("the new line did not land after web's last child with CRLF around it:\n%q", got)
	}
}

// Mixed endings: the new line copies the line it is inserted AFTER, and the
// file's mixture is left exactly as it was. Normalising a mixed file to one
// ending rewrites every line the reader did not ask about.
func TestInsertKeyMixedEndingsCopiesTheNeighbour(t *testing.T) {
	src := edgeSrc(t, "e19-mixed-endings.yml")

	crlf, err := Splice{}.InsertKey(src, []string{"services", "web"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey web: %v", err)
	}
	assertSingleInsertion(t, src, crlf)
	assertInsertion(t, src, crlf, "\r\n    restart: always")

	lf, err := Splice{}.InsertKey(src, []string{"services", "api"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey api: %v", err)
	}
	assertSingleInsertion(t, src, lf)
	assertInsertion(t, src, lf, "\n    restart: always")
	if bytes.Count(lf, []byte("\r\n")) != bytes.Count(src, []byte("\r\n")) {
		t.Errorf("the file's mixture of endings changed: %d CRLF before, %d after",
			bytes.Count(src, []byte("\r\n")), bytes.Count(lf, []byte("\r\n")))
	}
}

// A file with no trailing newline stays that way: exactly one separator is
// added, of the file's own kind, and no trailing one is invented.
func TestInsertKeyNoFinalNewlineAddsOneSeparatorOfTheFilesKind(t *testing.T) {
	src := edgeSrc(t, "e20-crlf-no-final-nl.yml")
	got, err := Splice{}.InsertKey(src, []string{"services", "cache"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	assertNoLoneLF(t, got)
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\r\n    restart: always")
	if bytes.HasSuffix(got, []byte("\n")) {
		t.Errorf("a file that ended without a newline now ends with one:\n%q", got)
	}
}

// BOM and CRLF together. The mark belongs to the file, not to the key written
// on its first line, and an insert must not disturb it.
func TestInsertKeyBOMCRLFKeepsMarkAndEnding(t *testing.T) {
	src := edgeSrc(t, "e10-bom-crlf.yml")
	got, err := Splice{}.InsertKey(src, []string{"services", "web"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	if !bytes.HasPrefix(got, bomPrefix) {
		t.Errorf("the byte order mark did not survive the insert")
	}
	assertNoLoneLF(t, got)
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\r\n    restart: always")
}

// A BOM'd file whose FIRST KEY is the insertion target. The mark sits on the
// same line as the key being inserted under, which is where a rebuild that
// works from line text drops it.
func TestInsertKeyBOMFirstKeyTarget(t *testing.T) {
	src := edgeSrc(t, "e15-bom-first-key.yml")
	got, err := Splice{}.InsertKey(src, []string{"services"}, "queue", "")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	if !bytes.HasPrefix(got, bomPrefix) {
		t.Errorf("the byte order mark did not survive the insert")
	}
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\n  queue:")
}

// InsertionPoint is what a preview reports, and AD-14 requires it to be the
// offset the write uses. On a CRLF file the offset must sit BEFORE the "\r":
// an insert between a CR and its LF splits the ending in two.
func TestInsertionPointOnCRLFSitsBeforeTheCR(t *testing.T) {
	src := edgeSrc(t, "e18-crlf-four-space.yml")
	off, indent, err := InsertionPoint(src, []string{"services", "web"})
	if err != nil {
		t.Fatalf("InsertionPoint: %v", err)
	}
	if indent != 8 {
		t.Errorf("indent %d, want 8 — the indent web's existing children use in a four-space file", indent)
	}
	if off <= 0 || off >= len(src) {
		t.Fatalf("offset %d out of range", off)
	}
	if src[off] != '\r' {
		t.Errorf("offset %d points at %q; the insertion point must be the end of the line's CONTENT, before its CR",
			off, string(src[off]))
	}
	// And it is the offset the write actually used.
	got, err := Splice{}.InsertKey(src, []string{"services", "web"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	if !bytes.Equal(got[:off], src[:off]) {
		t.Errorf("the write changed bytes before the offset the preview reported")
	}
}

// LF files are unchanged by the fix. This is the guard on the gate: every
// corpus file is LF, so a change in behaviour here would move structbench.
func TestInsertKeyLFUnchanged(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n")
	got, err := Splice{}.InsertKey(src, []string{"services", "web"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    restart: always\n  db:\n    image: postgres\n"
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(string(got), "\r") {
		t.Errorf("a CR appeared in an LF file")
	}
}
