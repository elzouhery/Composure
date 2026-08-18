package strategy

// Story 9.2's address, spelled every way a caller can spell it.
//
// D4, the one that wrote bytes. `sequenceIndex` parsed an index with a
// hand-rolled `n = n*10 + d` and no bound, so `[18446744073709551616]`
// OVERFLOWED to 0 and `[18446744073709551617]` to 1. Measured through the real
// binary on a two-entry list:
//
//	PREVIEW services.web.ports[18446744073709551616] -> ok, range before "8080:80"
//	APPLY   the same path                            -> wrote entry 0
//
// ErrEntryIndex — the whole point of story 9.2 — was bypassed, because after
// the wrap the index IS in range. There were two implementations of one
// address (AD-14): this package's loop and `internal/edit`'s `strconv.Atoi`,
// which returns ErrRange. There is now one, and it is strconv's.
//
// D5, the same address spelled with a sign or a stray space. `[-1]`, `[+1]`
// and `[1 ]` fell through to `path services.web.ports.-1 not found` —
// unclassifiable, so `edit.Refused` answered false and the reader was told the
// tool had broken. The pane cannot produce those today; the core is a library,
// a CLI and an MCP server, so "the caller is trusted" is not the contract.

import (
	"errors"
	"strings"
	"testing"
)

// The spellings that are not a position the list has. Every one of them is
// ErrEntryIndex — the sentinel that says "that list has no entry there" — and
// none of them resolves to an entry.
func TestIndexSpellingsThatAreNotAPosition(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	for _, seg := range []string{
		// The overflow that wrapped to entry 0 and entry 1.
		"18446744073709551616",
		"18446744073709551617",
		"9223372036854775808", // one past MaxInt64
		// A sign, and a space, and a sign with a space.
		"-1",
		"+1",
		"1 ",
		" 1",
		"-0",
	} {
		path := []string{"services", "web", "ports", seg}
		if _, _, err := ScalarRange(src, path); !errors.Is(err, ErrEntryIndex) {
			t.Errorf("ScalarRange ports[%s]: error is %v, want ErrEntryIndex", seg, err)
		}
		if _, _, err := Locate(src, path); !errors.Is(err, ErrEntryIndex) {
			t.Errorf("Locate ports[%s]: error is %v, want ErrEntryIndex", seg, err)
		}
		if _, _, _, err := CommentRange(src, path, CommentAbove); !errors.Is(err, ErrEntryIndex) {
			t.Errorf("CommentRange ports[%s]: error is %v, want ErrEntryIndex", seg, err)
		}
		out, err := (Splice{}).Edit(src, path, "9999:9999")
		if !errors.Is(err, ErrEntryIndex) {
			t.Errorf("Edit ports[%s]: error is %v, want ErrEntryIndex", seg, err)
		}
		if out != nil {
			t.Errorf("Edit ports[%s] returned %d bytes; a refusal writes nothing", seg, len(out))
		}
	}
}

// The half that says the refusal is not simply "every index is refused now":
// the positions the list HAS still resolve, and they resolve to the right
// entries. e43 repeats `"8080:80"` at 0 and 2 on purpose.
func TestTheSpellingsThatAreAPositionStillResolve(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	for seg, want := range map[string]string{
		"0": `"8080:80"`,
		"1": `"8443:443"`,
		"2": `"8080:80"`,
	} {
		start, end, err := ScalarRange(src, []string{"services", "web", "ports", seg})
		if err != nil {
			t.Fatalf("ports[%s]: %v", seg, err)
		}
		if got := string(src[start:end]); got != want {
			t.Errorf("ports[%s] reads %q, want %q", seg, got, want)
		}
	}
}

// The wrap, asserted as bytes rather than as an error. This is the check that
// would have caught it: the old engine returned no error at all and wrote a
// real entry, which no "want an error" test can see if the error is nil for
// the wrong reason.
func TestAnOverflowingIndexDoesNotWriteADifferentEntry(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	for _, seg := range []string{"18446744073709551616", "18446744073709551617"} {
		out, err := (Splice{}).Edit(src, []string{"services", "web", "ports", seg}, "9999:9999")
		if err == nil {
			t.Fatalf("ports[%s]: no error, and the file now reads:\n%s", seg, out)
		}
		if out != nil && strings.Contains(string(out), "9999:9999") {
			t.Fatalf("ports[%s] wrote an entry:\n%s", seg, out)
		}
	}
}

// A segment that is not index-SHAPED at all keeps its own sentence: `ports.web`
// is a caller asking for a key of a sequence, which is a different mistake from
// asking for an entry that is not there, and answering both with "that list has
// no entry there" would hide it.
func TestASegmentThatIsNotIndexShapedIsNotAnEntryIndexRefusal(t *testing.T) {
	src := fixture(t, "e43-repeated-list-entries.yml")
	_, _, err := Locate(src, []string{"services", "web", "ports", "web"})
	if err == nil {
		t.Fatal("no error")
	}
	if errors.Is(err, ErrEntryIndex) {
		t.Errorf("`ports.web` is reported as a missing entry: %v", err)
	}
	if !strings.Contains(err.Error(), "not an index") {
		t.Errorf("the sentence does not say what is wrong with it: %v", err)
	}
}
