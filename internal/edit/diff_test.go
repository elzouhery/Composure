package edit

import (
	"strings"
	"testing"
)

func TestUnifiedOfIdenticalInputIsEmpty(t *testing.T) {
	d := Unified("compose.yaml", []byte("a\nb\nc\n"), []byte("a\nb\nc\n"))
	if !d.Empty() || d.Text != "" {
		t.Errorf("identical input produced a diff: %+v", d)
	}
}

// The claim R4.1 is written in, at the level of the diff itself.
func TestUnifiedOfOneChangedLineIsTwoBodyLines(t *testing.T) {
	a := []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	b := []byte("one\ntwo\nthree\nFOUR\nfive\nsix\nseven\neight\n")
	d := Unified("f", a, b)
	if d.Removed != 1 || d.Added != 1 {
		t.Fatalf("Removed=%d Added=%d, want 1 and 1:\n%s", d.Removed, d.Added, d.Text)
	}
	if !strings.Contains(d.Text, "-four\n") || !strings.Contains(d.Text, "+FOUR\n") {
		t.Errorf("wrong lines changed:\n%s", d.Text)
	}
	// Three lines of context on each side, git's own default, so the diff
	// reads the same as the one in the reader's terminal.
	if !strings.Contains(d.Text, "@@ -1,7 +1,7 @@") {
		t.Errorf("hunk header is not the three-context one:\n%s", d.Text)
	}
}

// A CRLF file diffed against itself is empty. A comparison that normalised
// endings first would report "no change" for a file whose endings had in fact
// been rewritten, which is the failure this whole engine exists to prevent.
func TestUnifiedTreatsCRLFAsPartOfTheLine(t *testing.T) {
	crlf := []byte("a\r\nb\r\n")
	lf := []byte("a\nb\n")
	if !Unified("f", crlf, crlf).Empty() {
		t.Error("a CRLF file differs from itself")
	}
	d := Unified("f", crlf, lf)
	if d.Empty() {
		t.Error("rewriting CRLF to LF was reported as no change")
	}
}

// A dropped final newline is a real difference and is marked the way git marks
// it. Silently tolerating it would let a splice eat the last byte of a file and
// pass review.
func TestUnifiedMarksAMissingFinalNewline(t *testing.T) {
	d := Unified("f", []byte("a\nb\n"), []byte("a\nb"))
	if d.Empty() {
		t.Fatal("losing the final newline was reported as no change")
	}
	if !strings.Contains(d.Text, "\\ No newline at end of file") {
		t.Errorf("the missing newline is not marked:\n%s", d.Text)
	}
}

func TestUnifiedSeparateChangesGetSeparateHunks(t *testing.T) {
	var a, b []string
	for i := 0; i < 40; i++ {
		a = append(a, "line")
		b = append(b, "line")
	}
	b[2], b[35] = "X", "Y"
	d := Unified("f", []byte(strings.Join(a, "\n")+"\n"), []byte(strings.Join(b, "\n")+"\n"))
	if n := strings.Count(d.Text, "@@ "); n != 2 {
		t.Errorf("%d hunks, want 2:\n%s", n, d.Text)
	}
	if d.Added != 2 || d.Removed != 2 {
		t.Errorf("Added=%d Removed=%d, want 2 and 2", d.Added, d.Removed)
	}
}

func TestUnifiedNearbyChangesShareOneHunk(t *testing.T) {
	a := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	b := []byte("1\n2\nX\n4\n5\nY\n7\n8\n9\n")
	d := Unified("f", a, b)
	if n := strings.Count(d.Text, "@@ "); n != 1 {
		t.Errorf("%d hunks, want 1 — overlapping context should merge:\n%s", n, d.Text)
	}
}

func TestUnifiedOnAnInsertion(t *testing.T) {
	d := Unified("f", []byte("a\nb\n"), []byte("a\nnew\nb\n"))
	if d.Added != 1 || d.Removed != 0 {
		t.Errorf("Added=%d Removed=%d, want 1 and 0:\n%s", d.Added, d.Removed, d.Text)
	}
}

func TestUnifiedOnAnEmptyFile(t *testing.T) {
	d := Unified("f", nil, []byte("a\n"))
	if d.Added != 1 || d.Removed != 0 {
		t.Errorf("Added=%d Removed=%d, want 1 and 0:\n%s", d.Added, d.Removed, d.Text)
	}
	if Unified("f", nil, nil).Empty() != true {
		t.Error("two empty files differ")
	}
}

// A change too large for the LCS table still produces a correct, if coarse,
// diff rather than an allocation nobody bounded.
func TestUnifiedFallsBackOnAVeryLargeChange(t *testing.T) {
	var a, b []string
	for i := 0; i < 3000; i++ {
		a = append(a, "a")
		b = append(b, "b")
	}
	d := Unified("f", []byte(strings.Join(a, "\n")+"\n"), []byte(strings.Join(b, "\n")+"\n"))
	if d.Removed != 3000 || d.Added != 3000 {
		t.Errorf("Removed=%d Added=%d, want 3000 each", d.Removed, d.Added)
	}
}
