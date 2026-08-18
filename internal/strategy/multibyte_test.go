package strategy

import (
	"strings"
	"testing"
)

// A value that shares its line with non-ASCII text (story 6.4).
//
// goccy reports a token's Column in RUNES. offsetOf converts a line and column
// into a byte offset, and used to do it by adding col-1 BYTES to the start of
// the line. Every multi-byte character to the LEFT of a token on the same line
// therefore shifted the derived offset by the extra bytes that character costs
// — one for é, two for 東, three for 🎉 — and the position assertion at the foot
// of scalarSpan refused the edit rather than splicing one byte out.
//
// The refusal was correct and is kept. What these tests pin is that it no
// longer FIRES on a file whose only sin is an accent: the conversion is now
// rune-aware, so the two offsets it compares agree, and the whole file is
// editable again.

// editFresh applies one edit to a FRESH COPY of src and returns the result.
//
// The fresh copy is the point, and it is not defensive style. Measured
// 2026-08-13 against unfixed code: applying every path in a multi-byte fixture
// in sequence to ONE buffer makes all of them succeed, because the first edit
// replaces "café" with ASCII and the second replaces CAFÉ=1, so the rune/byte
// skew is gone from the line before the later paths are ever tried. A
// single-buffer test of this defect passes today, against the defect, and
// proves nothing. Every path below therefore starts from the file's own bytes.
func editFresh(t *testing.T, src []byte, path []string, newValue string) []byte {
	t.Helper()
	fresh := append([]byte(nil), src...)
	out, err := (Splice{}).Edit(fresh, path, newValue)
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(path, "."), err)
	}
	return out
}

// assertTwoLineDiff is the engine's own acceptance criterion for a scalar edit:
// exactly one line differs between the source and the result. A splice at a
// skewed offset that happened to land inside a neighbouring value would still
// produce one changed line, so the lexeme check in assertAddressable is the
// other half and both are asserted for every path.
func assertTwoLineDiff(t *testing.T, before, after []byte, path []string) {
	t.Helper()
	b := strings.Split(string(before), "\n")
	a := strings.Split(string(after), "\n")
	if len(b) != len(a) {
		t.Fatalf("%s: the edit changed the line COUNT, %d to %d", strings.Join(path, "."), len(b), len(a))
	}
	changed := 0
	for i := range b {
		if b[i] != a[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("%s: %d lines changed, want exactly 1", strings.Join(path, "."), changed)
	}
}

// The six paths of e17, which is the table in story 6.4. Four of them worked
// before the fix; two — the ones with an accent to their LEFT — were refused.
func TestEveryPathInAMultibyteFlowFileIsEditable(t *testing.T) {
	src := edgeFile(t, "e17-multibyte-flow.yml")

	cases := []struct {
		path  []string
		want  string // the lexeme, exactly as written, quotes included
		value string
	}{
		{[]string{"services", "web", "image"}, "nginx:1.25", "nginx:1.26"},
		{[]string{"services", "worker", "command"}, "echo café", "echo tea"},
		{[]string{"services", "web", "labels", "owner"}, `"café"`, "thé"},
		{[]string{"services", "web", "labels", "team"}, `"x"`, "z"},
		{[]string{"services", "web", "environment", "0"}, "CAFÉ=1", "CAFÉ=9"},
		{[]string{"services", "web", "environment", "1"}, "PLAIN=2", "PLAIN=9"},
	}
	for _, c := range cases {
		// A fresh copy per path — see editFresh.
		assertAddressable(t, append([]byte(nil), src...), c.path, c.want)
		out := editFresh(t, src, c.path, c.value)
		assertTwoLineDiff(t, src, out, c.path)
		if !strings.Contains(string(out), c.value) {
			t.Errorf("%s: the new value %q is not in the result", strings.Join(c.path, "."), c.value)
		}
	}
}

// Two, three and four byte characters, combining marks, quoted and unquoted
// scalars, flow mappings and flow sequences, and non-ASCII inside a key.
//
// The widths matter because the skew is exactly the extra bytes the character
// costs: a fix measured only against é is a fix for "+1" and this is what says
// it is a fix for the conversion.
func TestEveryWidthOfMultibyteCharacterConvertsCorrectly(t *testing.T) {
	src := edgeFile(t, "e38-multibyte-widths.yml")

	cases := []struct {
		path []string
		want string
	}{
		// The token to the LEFT is addressable too — a guard that refused every
		// accented file, or that only fixed the right-hand token, is a different
		// defect.
		{[]string{"services", "two-byte", "labels", "city"}, `"café"`},
		{[]string{"services", "two-byte", "labels", "tier"}, `"gold"`},
		{[]string{"services", "two-byte", "environment", "1"}, "PLAIN=2"},

		{[]string{"services", "three-byte", "labels", "tier"}, `"silver"`},
		{[]string{"services", "three-byte", "environment", "1"}, "PLAIN=2"},

		// Four bytes, and two UTF-16 code units. Measured: goccy counts this as
		// ONE column, so the unit is runes and not UTF-16.
		{[]string{"services", "four-byte", "labels", "tier"}, `"bronze"`},
		{[]string{"services", "four-byte", "environment", "1"}, "PLAIN=2"},

		// e + U+0301: two runes, three bytes, one glyph on screen. The parser's
		// unit is the one that has to be honoured, not the reader's.
		{[]string{"services", "combining", "labels", "tier"}, `"plain"`},

		// Unquoted scalars, where the lexeme has no quote characters to bound it.
		{[]string{"services", "unquoted", "labels", "tier"}, "gold"},

		// A key with an accent in it: a key is a token with a position too, and
		// both the value it introduces and the one after it are addressed past it.
		{[]string{"services", "accented-key", "labels", "café"}, `"x"`},
		{[]string{"services", "accented-key", "labels", "plain"}, `"y"`},

		// Three different widths accumulating along one line.
		{[]string{"services", "mixed", "labels", "b"}, `"東"`},
		{[]string{"services", "mixed", "labels", "c"}, `"🎉"`},
		{[]string{"services", "mixed", "labels", "d"}, `"tail"`},
	}
	for _, c := range cases {
		assertAddressable(t, append([]byte(nil), src...), c.path, c.want)
		out := editFresh(t, src, c.path, "REPLACED")
		assertTwoLineDiff(t, src, out, c.path)
	}
}

// A byte order mark and an accent on the same addressed line. Both are
// position-to-offset corrections and they compose; either one applied without
// the other lands three bytes, or one byte, out.
func TestAMarkAndAnAccentInTheSameFile(t *testing.T) {
	src := edgeFile(t, "e39-bom-multibyte.yml")
	if !hasBOM(src) {
		t.Fatalf("the fixture lost its byte order mark; it tests nothing")
	}
	for _, c := range []struct {
		path []string
		want string
	}{
		{[]string{"services", "web", "image"}, "nginx:1.25"},
		{[]string{"services", "web", "labels", "owner"}, `"café"`},
		{[]string{"services", "web", "labels", "team"}, `"x"`},
		{[]string{"services", "web", "environment", "0"}, "CAFÉ=1"},
		{[]string{"services", "web", "environment", "1"}, "PLAIN=2"},
	} {
		assertAddressable(t, append([]byte(nil), src...), c.path, c.want)
		out := editFresh(t, src, c.path, "REPLACED")
		assertTwoLineDiff(t, src, out, c.path)
		if !hasBOM(out) {
			t.Errorf("%s: the edit dropped the byte order mark", strings.Join(c.path, "."))
		}
	}
}

// offsetOf's contract, stated directly rather than through the engine.
//
// The ASCII rows are the ones that guard the corpus: 146 files, exactly one
// byte >= 0x80 anywhere in them, so a green gate says only that these rows
// still hold. They must be byte-for-byte what the old byte-walking code
// returned — including a column past the end of a line, which is the one place
// the two implementations could have diverged on ASCII.
func TestOffsetOfCountsRunesNotBytes(t *testing.T) {
	src := []byte("ab: cd\ne: {x: \"café\", y: \"tail\"}\n東: 1\n")
	cases := []struct {
		line, col int
		want      int
		why       string
	}{
		{1, 1, 0, "first byte of the file"},
		{1, 5, 4, "ASCII, mid-line"},
		{2, 1, 7, "start of line 2"},
		{2, 8, 14, "ASCII prefix of line 2, before the accent"},
		{2, 19, 26, "the quote opening \"tail\", 7 runes and 8 bytes past the accent"},
		{3, 1, 34, "start of line 3"},
		{3, 2, 37, "past a three-byte character"},
		// A column past the end of a line is left as byte arithmetic, exactly as
		// before: there are no runes left to walk, and inventing an error here
		// would change behaviour on ASCII files, which is the one thing this
		// change may not do.
		{1, 9, 8, "past the end of an ASCII line"},
	}
	for _, c := range cases {
		got, err := offsetOf(src, c.line, c.col)
		if err != nil {
			t.Errorf("offsetOf(%d,%d): %v", c.line, c.col, err)
			continue
		}
		if got != c.want {
			t.Errorf("offsetOf(%d,%d) = %d, want %d (%s)", c.line, c.col, got, c.want, c.why)
		}
	}
	if _, err := offsetOf(src, 9, 1); err == nil {
		t.Error("a line past the end of the file was accepted")
	}
}

// The rune walk stops at the END OF ITS LINE, and a column past it is finished
// in bytes.
//
// Separate from the table above because the two implementations agree on
// everything else and disagree only here: a rune walk that runs on past the
// newline counts the NEXT line's characters, so a short line followed by a
// multi-byte one comes back with an offset that is neither the old answer nor
// any defensible new one. Measured: without the clamp this returns 10 rather
// than 8. Nothing in the engine asks for such a column on purpose, which is
// exactly why it must not quietly change meaning — the guards downstream are
// written against the old arithmetic.
func TestAColumnPastTheEndOfALineDoesNotWalkIntoTheNext(t *testing.T) {
	src := []byte("ab: cd\n東: 1\n")
	got, err := offsetOf(src, 1, 9)
	if err != nil {
		t.Fatalf("offsetOf: %v", err)
	}
	if got != 8 {
		t.Errorf("offsetOf(1,9) = %d, want 8 — the walk ran past the end of line 1 and "+
			"counted line 2's characters as if they were on it", got)
	}
}

// The structural path's second rune column (story 6.4's audit criterion).
//
// locate returns the key token's column as an "indent", and that number is
// compared against indents scanLines counts in BYTES. In block style the only
// thing to a key's left is spaces, so the two units agree; a token inside a
// flow collection has real text to its left and they do not. Range is
// line-granular, so this produced no wrong bytes — but two units meeting in one
// comparison is the same substitution that produced the scalar defect, so the
// conversion is done in one place and this pins the result.
func TestTheStructuralIndentIsCountedInBytes(t *testing.T) {
	src := edgeFile(t, "e38-multibyte-widths.yml")
	lines := scanLines(src)

	// The last segment is what locate anchors on: a key name, or a sequence
	// index whose entry is the token. `lexeme` is that token as it is written,
	// and its byte index in the line is what an indent compared against
	// scanLines has to be.
	for _, c := range []struct {
		path   []string
		lexeme string
	}{
		{[]string{"services", "two-byte", "labels", "tier"}, "tier:"},
		{[]string{"services", "three-byte", "labels", "tier"}, "tier:"},
		{[]string{"services", "four-byte", "labels", "tier"}, "tier:"},
		{[]string{"services", "mixed", "labels", "d"}, "d:"},
		{[]string{"services", "accented-key", "labels", "plain"}, "plain:"},
		{[]string{"services", "two-byte", "environment", "1"}, "PLAIN=2"},
		{[]string{"services", "three-byte", "environment", "1"}, "PLAIN=2"},
		{[]string{"services", "four-byte", "environment", "1"}, "PLAIN=2"},
		// Block style, where the two units agree and always did. It is here so
		// that a "fix" which shifted every block indent would be caught.
		{[]string{"services", "two-byte", "labels"}, "labels:"},
	} {
		idx, indent, err := Locate(src, c.path)
		if err != nil {
			t.Fatalf("%s: %v", strings.Join(c.path, "."), err)
		}
		text := lines[idx].text
		want := strings.Index(text, c.lexeme)
		if want < 0 {
			t.Fatalf("fixture regressed: %q is not on line %d (%q)", c.lexeme, idx+1, text)
		}
		if indent != want {
			t.Errorf("%s: locate reports indent %d, %q begins %d bytes into its line (%q) — "+
				"a rune column is being used as a byte index against scanLines",
				strings.Join(c.path, "."), indent, c.lexeme, want, text)
		}
	}
}
