package strategy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ways the parser's idea of a position can disagree with the file.
//
// All of them are about ADDRESSING, not about writing, and both produced the failure
// this engine is most prone to: not a crash, a confident wrong answer. They are
// pinned here at the locator rather than only through internal/edit, because
// the locator is where every consumer — the resolver's fix ranges, the
// diagnostics, the extension — gets its byte offsets from.

func edgeFile(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Skipf("regression file unavailable: %v", err)
	}
	return src
}

// assertAddressable is the assertion that matters: not that a call succeeds,
// but that the bytes it names are the bytes the path names. An offset that is
// three bytes out still succeeds.
func assertAddressable(t *testing.T, src []byte, path []string, want string) {
	t.Helper()
	start, end, err := ScalarRange(src, path)
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(path, "."), err)
	}
	if got := string(src[start:end]); got != want {
		t.Errorf("%s spans bytes %d-%d, which read %q, want %q",
			strings.Join(path, "."), start, end, got, want)
	}
}

// A UTF-8 byte order mark made every path in the file unaddressable, because
// goccy folds it into the first token and every path begins at a top-level key
// on the first line.
func TestBOMDoesNotHideTheWholeDocument(t *testing.T) {
	src := edgeFile(t, "e13-bom-read.yml")
	assertAddressable(t, src, []string{"name"}, "bom-read")
	assertAddressable(t, src, []string{"services", "web", "image"}, "nginx:1.25")
	assertAddressable(t, src, []string{"services", "web", "ports", "0"}, `"8080:80"`)
}

// The mark belongs to the FILE, not to the first key written on it.
//
// This assertion used to live on e13-bom-read.yml, where seven comment lines
// push `name:` to line 8 — so the range for "the first key" started hundreds of
// bytes past the mark and `start < 3` could not fire whatever the code did.
// That is the degenerate-fixture shape: a check unable to fail. Deleting
// afterBOM's body left it green, and the operation that actually writes bytes,
// DeleteKey, ate the mark on every such file.
//
// e15-bom-first-key.yml puts the first key on line 1, which is the only place
// afterBOM has anything to do.
func TestTheMarkSurvivesAnEditToTheFirstKey(t *testing.T) {
	src := edgeFile(t, "e15-bom-first-key.yml")
	if !hasBOM(src) {
		t.Fatalf("the fixture lost its byte order mark; it tests nothing")
	}
	// The premise, asserted rather than assumed: the first key is on line 1, so
	// its line-oriented range begins inside the mark unless something moves it.
	if line, _, err := Locate(src, []string{"services"}); err != nil || line != 0 {
		t.Fatalf("fixture regressed: `services` is at line index %d (%v), want 0 — "+
			"a comment above it would make every assertion below unable to fail", line, err)
	}

	start, _, err := Range(src, []string{"services"})
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if start < len(bomPrefix) {
		t.Errorf("the range for the first key starts at byte %d, inside the mark", start)
	}

	dstart, dend, err := DeleteRange(src, []string{"services"})
	if err != nil {
		t.Fatalf("delete range: %v", err)
	}
	if dstart < len(bomPrefix) {
		t.Errorf("the reported delete range takes the byte order mark with it (start %d)", dstart)
	}

	// The one that matters: the bytes DeleteKey actually writes. The reported
	// range and the performed edit are the same thing (AD-14), so check both.
	out, err := (Splice{}).DeleteKey(src, []string{"services"})
	if err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if !hasBOM(out) {
		t.Errorf("DeleteKey stripped the byte order mark: the result begins %q. "+
			"Nothing asked for the file's encoding declaration to change", firstBytes(out))
	}
	spliced := string(src[:dstart]) + string(src[dend:])
	if string(out) != spliced {
		t.Errorf("DeleteKey and DeleteRange disagree about the same file (AD-14)\n"+
			" DeleteKey:   %q\n DeleteRange: %q", out, spliced)
	}
}

// The whole body of a BOM'd file is the node being deleted, and there is no
// newline above it to absorb. Decrementing blindly ends the splice inside the
// three-byte mark and leaves two orphan bytes — a file that is neither BOM'd
// nor clean.
func TestDeletingTheOnlyKeyDoesNotCutTheMarkInHalf(t *testing.T) {
	src := append(append([]byte{}, bomPrefix...), []byte("services:\n  web:\n    image: nginx")...)
	out, err := (Splice{}).DeleteKey(src, []string{"services"})
	if err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if len(out) != 0 && !hasBOM(out) {
		t.Errorf("the result is %q — a fragment of the mark, or bytes from inside it", out)
	}
}

// A THIRD way the parser's position can disagree with the file, and the guard
// that refuses it.
//
// goccy reports a token's Column in RUNES; offsetOf walks BYTES. Any multi-byte
// character to the LEFT of a token on its own line shifts the derived offset by
// the extra bytes it costs. Block style hides this — each value is on its own
// line — so it takes flow style to put an accent and a scalar on one line.
//
// The assertion at the foot of scalarSpan is the only thing standing between
// that and a splice one byte out. Without it the range comes back plausible and
// WRONG: ` "x` where the file holds `"x"`, and writing into it damages the
// neighbouring value. That is this engine's characteristic failure exactly, and
// nothing exercised the assertion until this fixture.
//
// The refusal was the correct behaviour under CLAUDE.md rule 6, and it was NOT
// the fix. Story 6.4 made offsetOf rune-aware, so the two independently derived
// values now agree and every path in the file is editable again —
// multibyte_test.go is where that is pinned, path by path, on a fresh copy of
// the file each time.
//
// What is asserted here is the guard itself, which stays. It has no live
// producer on the scalar path any more: swept 2026-08-13, nothing in
// testdata/edge, testdata/adversarial or the 146-file corpus makes goccy's
// scalar position disagree with the buffer. So it is exercised by MUTATION
// rather than by fixture — reverting offsetOf's rune walk to `off + col - 1`
// makes exactly the two paths below refuse again with ErrPositionMismatch,
// reporting ` "x` at byte 1204 and ` PLAIN=2` at 1236. Recorded here because a
// guard nobody can make fire is a guard nobody knows still works.
//
// The property below is that guard's condition, checked from outside: every
// range this engine hands out must read as the lexeme the path names. If
// offsetOf ever goes wrong again, either this fails or the guard refuses — and
// both are the answer this engine owes, unlike a plausible range over the
// wrong bytes.
func TestEveryRangeInAMultibyteFileReadsAsItsOwnLexeme(t *testing.T) {
	src := edgeFile(t, "e17-multibyte-flow.yml")

	for _, c := range []struct {
		path []string
		want string
	}{
		// To the RIGHT of the accent: refused before story 6.4.
		{[]string{"services", "web", "labels", "team"}, `"x"`},
		{[]string{"services", "web", "environment", "1"}, "PLAIN=2"},
		// To the LEFT of it, and block style: these always worked, and a fix
		// that made an accented file unaddressable would be a different defect.
		{[]string{"services", "web", "labels", "owner"}, `"café"`},
		{[]string{"services", "web", "environment", "0"}, "CAFÉ=1"},
		{[]string{"services", "web", "image"}, "nginx:1.25"},
		{[]string{"services", "worker", "command"}, "echo café"},
	} {
		assertAddressable(t, src, c.path, c.want)
	}
}

// The guard's other producer, which has nothing to do with runes and is the
// reason the sentinel outlives story 6.4: locate refuses when the parser puts a
// key on a line the file does not have. It is checked here against a source
// built to disagree, because that is the one half of ErrPositionMismatch a test
// can still make fire.
func TestALineNumberPastTheEndOfTheFileIsRefused(t *testing.T) {
	// Two lines in the buffer, and a parser position on line 9: exactly the
	// shape goccy's CRLF comment miscount produced before parseView corrected
	// it. Asserted through offsetOf, which is where the arithmetic lives.
	if _, err := offsetOf([]byte("services:\n  web: nginx\n"), 9, 1); err == nil {
		t.Error("a position past the end of the file produced an offset instead of an error")
	}
	// And the structural guard's own message, on the same principle.
	if _, _, err := Locate([]byte("services:\n  web: nginx\n"), []string{"services", "web"}); err != nil {
		t.Errorf("a well-formed document was refused: %v", err)
	}
}

func hasBOM(src []byte) bool { return bomLen(src) == len(bomPrefix) }

func firstBytes(src []byte) []byte {
	if len(src) > 8 {
		return src[:8]
	}
	return src
}

// CRLF plus comment lines: goccy counted each comment line twice, so every
// position below them was wrong. Independent of the mark — this file has none.
func TestCRLFCommentLinesDoNotShiftEveryPosition(t *testing.T) {
	src := edgeFile(t, "e14-crlf-comments.yml")
	assertAddressable(t, src, []string{"services", "web", "image"}, "nginx:1.25")
	assertAddressable(t, src, []string{"services", "web", "ports", "0"}, `"8080:80"`)

	// This is the call that panicked: locate returned a line index past the end
	// of the line table and Range indexed straight into it.
	if _, _, err := Range(src, []string{"services", "web"}); err != nil {
		t.Errorf("range over a CRLF document with comments: %v", err)
	}
}

// parseView's contract, stated directly: the parser's buffer is normalised, the
// ADDRESSING buffer is not, and confusing the two is what would put an offset
// three bytes — or one per preceding line — out on a Windows file.
func TestParseViewKeepsAddressingBytesIntact(t *testing.T) {
	src := append(append([]byte{}, bomPrefix...), []byte("a: 1\r\nb: 2\r\n")...)
	parse, addr, shift := parseView(src)

	if shift != len(bomPrefix) {
		t.Errorf("shift = %d, want the length of the mark", shift)
	}
	if string(addr) != "a: 1\r\nb: 2\r\n" {
		t.Errorf("the addressing buffer was normalised: %q — every offset derived from it would be wrong", addr)
	}
	if string(parse) != "a: 1\nb: 2\n" {
		t.Errorf("the parse buffer = %q, want mark removed and endings normalised", parse)
	}
	// The property the whole correction rests on: same number of lines, so a
	// line number from the parser addresses the same line in the real bytes.
	if strings.Count(string(parse), "\n") != strings.Count(string(addr), "\n") {
		t.Error("normalisation changed the line count; line numbers would no longer transfer")
	}

	// A file with neither is handed straight through.
	plain := []byte("a: 1\n")
	p2, a2, s2 := parseView(plain)
	if s2 != 0 || string(p2) != string(plain) || string(a2) != string(plain) {
		t.Errorf("an ordinary file was rewritten: parse=%q addr=%q shift=%d", p2, a2, s2)
	}
}
