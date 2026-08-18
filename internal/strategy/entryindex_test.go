package strategy

// Story 9.2. An index that addresses a position the list does not have.
//
// The engine's answer used to be `path segment "9": sequence has 3 entries`
// from locate and `path services.web.ports.9 not found` from scalarSpan — two
// different sentences for one question, neither of them a sentinel, so
// edit.Refused answered false and the reader was told the tool broke.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// The fixture repeats entries on purpose; this asserts that it does, so the
// checks below cannot quietly become checks against a list of distinct values.
func TestListFixtureRepeatsEntries(t *testing.T) {
	src := string(fixture(t, "e43-repeated-list-entries.yml"))
	if strings.Count(src, "- sh\n") < 3 {
		t.Fatal("e43 no longer repeats `- sh`; an off-by-one would be invisible against it")
	}
	if strings.Count(src, `- "8080:80"`) < 2 {
		t.Fatal("e43 no longer repeats a port; an off-by-one would be invisible against it")
	}
}

func TestOutOfRangeIndexIsATypedRefusal(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	for _, path := range []string{
		"services.web.ports[9]",
		"services.web.command[99]",
		"services.web.healthcheck.test[4]",
	} {
		if _, _, err := ScalarRange(src, parse(path)); !errors.Is(err, ErrEntryIndex) {
			t.Errorf("ScalarRange(%s): error is %v, want ErrEntryIndex", path, err)
		}
		if _, _, err := Locate(src, parse(path)); !errors.Is(err, ErrEntryIndex) {
			t.Errorf("Locate(%s): error is %v, want ErrEntryIndex", path, err)
		}
	}
}

// The count has to be in the sentence. "That entry is not there" leaves the
// reader guessing at which entries ARE; the list has three.
func TestEntryIndexRefusalNamesTheLength(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	_, _, err := ScalarRange(src, parse("services.web.ports[9]"))
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("the refusal does not say how many entries the list has: %v", err)
	}
}

// The middle of a repeated list. Editing index 0 of a list of distinct values
// is the check that cannot fail; this is the one that can.
func TestEditingTheMiddleOfARepeatedList(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	out, err := (Splice{}).Edit(src, parse("services.web.command[2]"), "bash")
	if err != nil {
		t.Fatal(err)
	}
	// Buffer comparison, not "the result contains bash": substituting the old
	// lexeme back at the same offset must reproduce the source exactly.
	start, end, err := ScalarRange(src, parse("services.web.command[2]"))
	if err != nil {
		t.Fatal(err)
	}
	if string(src[start:end]) != "sh" {
		t.Fatalf("index 2 reads %q, want the THIRD entry `sh` — the fixture or the addressing moved", src[start:end])
	}
	back := string(out[:start]) + "sh" + string(out[start+len("bash"):])
	if back != string(src) {
		t.Errorf("the edit touched bytes outside the entry.\n got: %q\nwant: %q", back, string(src))
	}
	// And the OTHER two `sh` entries are still there.
	if n := strings.Count(string(out), "- sh\n"); n != 2 {
		t.Errorf("%d `- sh` entries survive, want 2 — the edit hit the wrong repeat", n)
	}
}

// A numeric MAPPING key is a key, not an index. resolve.Path renders it as
// [8080] and that is a display ambiguity; it must not become a resolution one.
func TestNumericMappingKeyIsStillAKey(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	start, end, err := ScalarRange(src, parse("services.web.environment[8080]"))
	if err != nil {
		t.Fatalf("a numeric mapping key is not reachable: %v", err)
	}
	if got := string(src[start:end]); got != `"a numeric mapping key, not a sequence index"` {
		t.Errorf("range reads %q", got)
	}
}

// parse is resolve.ParsePath's arithmetic, inlined so this package stays a leaf.
func parse(s string) []string {
	var out []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.':
			flush()
		case '[':
			flush()
			j := strings.IndexByte(s[i:], ']')
			out = append(out, s[i+1:i+j])
			i += j
		default:
			cur.WriteByte(s[i])
		}
	}
	flush()
	return out
}
