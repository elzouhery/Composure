package strategy

import (
	"strings"
	"testing"
)

// InsertKey with an EMPTY value wrote `key: ` — a trailing space, confirmed
// with od -c. A key with no value is written `key:` and nothing after it.
//
// The assertion is on the exact bytes of the emitted line rather than on the
// result parsing, because `key: ` parses perfectly. So does `key:`. A test that
// only re-parsed would have passed against the defect, which is exactly how six
// checks that could not fail shipped in this codebase.
func TestInsertKeyEmptyValueEmitsNoTrailingSpace(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	got, err := Splice{}.InsertKey(src, []string{"services", "web"}, "healthcheck", "")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    healthcheck:\n"
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}

	// Said again in the terms the defect was reported in, so a future reader
	// sees the byte sequence that was wrong: `key` `:` SPACE `\n`.
	for i, line := range strings.Split(string(got), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %d ends in a space: %q", i+1, line)
		}
	}
}

// The non-empty case keeps its single separating space. Fixing a trailing space
// by dropping the separator would be a different corruption.
func TestInsertKeyNonEmptyValueKeepsOneSpace(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	got, err := Splice{}.InsertKey(src, []string{"services", "web"}, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    restart: always\n"
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// A key inserted with no value IS a null value, so the two fixes meet: inserting
// `healthcheck:` and then setting it must produce `healthcheck: test`, on one
// line, with every other line untouched. Before the fixes this sequence emitted
// `healthcheck: ` and then destroyed whatever followed it.
func TestInsertKeyThenSetItRoundTrips(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n")
	sp := Splice{}
	inserted, err := sp.InsertKey(src, []string{"services", "web"}, "restart", "")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	if want := "services:\n  web:\n    image: nginx\n    restart:\n  db:\n    image: postgres\n"; string(inserted) != want {
		t.Fatalf("after insert\n got: %q\nwant: %q", inserted, want)
	}
	set, err := sp.Edit(inserted, []string{"services", "web", "restart"}, "always")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if want := "services:\n  web:\n    image: nginx\n    restart: always\n  db:\n    image: postgres\n"; string(set) != want {
		t.Fatalf("after set\n got: %q\nwant: %q", set, want)
	}
}
