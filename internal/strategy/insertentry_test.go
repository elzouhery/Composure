package strategy

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Story 7.5: insert an entry into a sequence.
//
// R4.2 names five operations and this engine had three of them. locate could
// already ADDRESS a sequence entry — a digit-only segment indexes one — so this
// adds writing where reading already worked.
//
// The assertions are buffer comparisons. An entry written at the wrong indent
// reparents into the previous entry or out of the sequence entirely, and both
// still parse: `- "80:80"` under a key is a valid document whatever column it
// lands in, which is precisely why "no error" proves nothing here.

// insertEntry runs the operation against the named service's ports in the
// shared fixture and returns the result.
func entryCase(t *testing.T, src []byte, path []string, value string) []byte {
	t.Helper()
	got, err := Splice{}.InsertSequenceEntry(src, path, value)
	if err != nil {
		t.Fatalf("InsertSequenceEntry(%s): %v", strings.Join(path, "."), err)
	}
	assertSingleInsertion(t, src, got)
	return got
}

// Entries at the KEY's own indent stay at the key's own indent. childIndent's
// answer would be one step in, which is a different sequence.
func TestInsertEntryCopiesSiblingIndentAtKeyLevel(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got := entryCase(t, src, []string{"services", "flush", "ports"}, `"9090:90"`)
	assertInsertion(t, src, got, "\n    - \"9090:90\"")
	if !bytes.Contains(got, []byte("    - \"8443:443\"\n    - \"9090:90\"\n  indented:")) {
		t.Errorf("the entry did not land after the last sibling at the sibling's indent:\n%s", got)
	}
}

// Entries one step in stay one step in.
func TestInsertEntryCopiesSiblingIndentIndented(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got := entryCase(t, src, []string{"services", "indented", "ports"}, `"9091:90"`)
	assertInsertion(t, src, got, "\n      - \"9091:90\"")
}

// The marker's spacing copies the siblings too: a file writing `-   "80:80"`
// keeps its spacing.
func TestInsertEntryCopiesMarkerSpacing(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got := entryCase(t, src, []string{"services", "wide", "ports"}, `"7071:70"`)
	assertInsertion(t, src, got, "\n    -   \"7071:70\"")
}

// No sibling to imitate: the fallback is dominantStep applied to the key's own
// indent. This is the case that has to be decided rather than inferred, so the
// fixture pins it.
func TestInsertEntryIntoEmptySequenceUsesDominantStep(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got := entryCase(t, src, []string{"services", "empty", "ports"}, `"4040:40"`)
	assertInsertion(t, src, got, "\n      - \"4040:40\"")
}

// A sequence of mapping entries: a new scalar entry copies the marker column of
// the mapping entry, and lands after the whole of it rather than inside it.
func TestInsertEntryAfterMappingEntry(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got := entryCase(t, src, []string{"services", "mappings", "ports"}, `"3030:30"`)
	if !bytes.Contains(got, []byte("        published: \"8080\"\n      - \"3030:30\"\n  commented:")) {
		t.Errorf("the entry landed inside the mapping entry or after the wrong line:\n%s", got)
	}
}

// A trailing comment under the last entry keeps its place: the new entry lands
// before it, which is what InsertKey does for a mapping. One rule, pinned.
func TestInsertEntryBeforeTrailingComment(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got := entryCase(t, src, []string{"services", "commented", "ports"}, `"6061:60"`)
	if !bytes.Contains(got, []byte("      - \"6060:60\"\n      - \"6061:60\"\n      # A trailing comment")) {
		t.Errorf("the entry did not land after the last entry and before its trailing comment:\n%s", got)
	}
}

// A flow sequence is refused with ErrFlowStyle. Appending a block entry under
// `ports: ["80:80"]` produces a document that is not valid YAML.
func TestInsertEntryIntoFlowSequenceIsRefused(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	got, err := Splice{}.InsertSequenceEntry(src, []string{"services", "flow", "ports"}, `"5051:50"`)
	if !errors.Is(err, ErrFlowStyle) {
		t.Fatalf("error is %v, want ErrFlowStyle", err)
	}
	if got != nil {
		t.Errorf("a refusal returned %d bytes; it must touch nothing", len(got))
	}
}

// A target that is not a sequence is refused rather than written to. Appending
// `- x` under a mapping would produce a key whose value is both a mapping and a
// sequence, which is not a document anyone can read back.
func TestInsertEntryIntoNonSequenceIsRefused(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n    environment:\n      FOO: bar\n")
	for _, path := range [][]string{
		{"services", "web", "environment"},
		{"services", "web", "image"},
		{"services"},
	} {
		got, err := Splice{}.InsertSequenceEntry(src, path, "x")
		if !errors.Is(err, ErrNotASequence) {
			t.Errorf("%s: error is %v, want ErrNotASequence", strings.Join(path, "."), err)
		}
		if got != nil {
			t.Errorf("%s: a refusal returned %d bytes", strings.Join(path, "."), len(got))
		}
	}
}

// An entry with no value is refused rather than written as a bare `- `, which
// leaves the reader a null sequence entry — a shape with nowhere safe to write
// afterwards. Adding a MAPPING entry is that same refusal: this operation
// writes one scalar entry, and a long-form port is a multi-operation insert a
// later story owns.
func TestInsertEntryWithNoValueIsRefused(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	for _, value := range []string{"", "   "} {
		got, err := Splice{}.InsertSequenceEntry(src, []string{"services", "indented", "ports"}, value)
		if !errors.Is(err, ErrNullEntry) {
			t.Errorf("value %q: error is %v, want ErrNullEntry", value, err)
		}
		if got != nil {
			t.Errorf("value %q: a refusal returned %d bytes", value, len(got))
		}
	}
}

// The null check was `TrimSpace(value) == ""` and nothing else, and two texts
// walked straight past it into a damaged file that reported success.
//
// `# nope` is written as `- # nope` — a dash and a comment, which is the null
// entry ErrNullEntry exists to prevent, reached by a different road. A text
// holding a newline is written as TWO lines, and the second one joined the
// PARENT MAPPING: `- x\n    restart: always` gave the service a `restart` key
// nobody asked for. That one still parses, which is why the write path's
// re-parse never saw it, and it breaks story 7.5's own criterion that excising
// the ONE inserted line restores the source byte for byte.
//
// The accepted half of the table is as load-bearing as the refused half: this
// operation writes ports, and refusing `8080:80` to be rid of a defect would be
// the cure killing the patient.
func TestInsertEntryTextThatWouldNotSurviveIsRefused(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	path := []string{"services", "indented", "ports"}

	nullish := map[string]string{
		"a comment marker":  "# nope",
		"a tilde":           "~",
		"the word null":     "null",
		"a comment and gap": "  # nope",
		"a bare anchor":     "&x",
	}
	for what, value := range nullish {
		got, err := Splice{}.InsertSequenceEntry(src, path, value)
		if !errors.Is(err, ErrNullEntry) {
			t.Errorf("%s (%q): error is %v, want ErrNullEntry", what, value, err)
		}
		if got != nil {
			t.Errorf("%s: a refusal returned %d bytes", what, len(got))
		}
	}

	refused := map[string]string{
		"a line break":         "x\n    restart: always",
		"a bare line break":    "a\nb",
		"a carriage return":    "a\r    restart: always",
		"a tab":                "a\tb",
		"a mapping":            "a: b",
		"a flow mapping":       "{a: b}",
		"a flow sequence":      "[a, b]",
		"a nested dash":        "- x",
		"a trailing space":     "8080:80 ",
		"a leading space":      " 8080:80",
		"a comment after text": "8080:80 # published",
		"an alias":             "*defaults",
		"a folded block":       ">",
	}
	for what, value := range refused {
		got, err := Splice{}.InsertSequenceEntry(src, path, value)
		if !errors.Is(err, ErrEntryText) {
			t.Errorf("%s (%q): error is %v, want ErrEntryText", what, value, err)
		}
		if got != nil {
			t.Errorf("%s: a refusal returned %d bytes", what, len(got))
		}
		// The refusal has to name what stopped it. A generic "not something YAML
		// reads back" for a line break tells the reader nothing they can act on,
		// and the parser's own message for these is worse than useless.
		if strings.Contains(value, "\n") || strings.Contains(value, "\r") {
			if !strings.Contains(err.Error(), "line break") {
				t.Errorf("%s: the refusal does not name the line break: %v", what, err)
			}
		}
		if strings.Contains(value, "\t") && !strings.Contains(err.Error(), "tab") {
			t.Errorf("%s: the refusal does not name the tab: %v", what, err)
		}
	}

	accepted := map[string]string{
		"a published port":   "8080:80",
		"a quoted port":      `"8080:80"`,
		"a single-quoted":    `'8080:80'`,
		"a bare number":      "8080",
		"a network name":     "frontend",
		"a dotted name":      "api.gateway",
		"an interpolation":   "${PORT}:80",
		"a long-form-ish":    "127.0.0.1:8080:80/udp",
		"a capability":       "SYS_ADMIN",
		"an env assignment":  "FOO=bar",
		"a path binding":     "./data:/var/lib/data:ro",
		"a quoted line char": `"a: b"`,
	}
	for what, value := range accepted {
		got, err := Splice{}.InsertSequenceEntry(src, path, value)
		if err != nil {
			t.Errorf("%s (%q) was refused: %v", what, value, err)
			continue
		}
		assertSingleInsertion(t, src, got)
		if _, err := (Splice{}).Identity(got); err != nil {
			t.Errorf("%s: the result does not parse: %v", what, err)
		}
	}
}

// Four-space file: the entry lands at the siblings' column, not at a guessed 2.
func TestInsertEntryFourSpaceFile(t *testing.T) {
	src := edgeSrc(t, "e3-four-space.yml")
	got := entryCase(t, src, []string{"services", "web", "ports"}, `"81:81"`)
	assertInsertion(t, src, got, "\n            - \"81:81\"")
}

// CRLF file: the new entry carries the file's own ending (story 7.1).
func TestInsertEntryCRLF(t *testing.T) {
	src := edgeSrc(t, "e18-crlf-four-space.yml")
	got := entryCase(t, src, []string{"services", "web", "ports"}, `"81:81"`)
	assertNoLoneLF(t, got)
	assertInsertion(t, src, got, "\r\n            - \"81:81\"")
}

// AD-14: the offset a preview reports is the offset the write uses, derived
// through the same function.
func TestSequenceInsertionPoint(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	off, indent, err := SequenceInsertionPoint(src, []string{"services", "flush", "ports"})
	if err != nil {
		t.Fatalf("SequenceInsertionPoint: %v", err)
	}
	if indent != 4 {
		t.Errorf("indent %d, want 4 — the column the existing entries' markers sit at", indent)
	}
	got := entryCase(t, src, []string{"services", "flush", "ports"}, `"9090:90"`)
	if !bytes.Equal(got[:off], src[:off]) || !bytes.HasPrefix(got[off:], []byte("\n    - \"9090:90\"")) {
		t.Errorf("the write did not happen at the offset the preview reported (%d)", off)
	}
	if _, _, err := SequenceInsertionPoint(src, []string{"services", "flow", "ports"}); !errors.Is(err, ErrFlowStyle) {
		t.Errorf("SequenceInsertionPoint error is %v, want ErrFlowStyle", err)
	}
}

// Everything the operation writes must survive a re-parse, and the entry must
// be readable back AT THE INDEX it was written to. This is the strongest
// end-to-end statement that the indentation was right: an entry written at the
// wrong column reparents — out of the sequence, or into the previous entry's
// subtree — and then addresses at a different path or not at all, while still
// parsing perfectly.
func TestInsertEntryIsReadableBackAtItsIndex(t *testing.T) {
	src := edgeSrc(t, "e31-sequences.yml")
	// service -> the index the new entry must occupy, from the fixture's own
	// entry counts.
	want := map[string]string{
		"flush":     "2",
		"indented":  "1",
		"wide":      "1",
		"mappings":  "1",
		"commented": "1",
		"empty":     "0",
	}
	for svc, idx := range want {
		got := entryCase(t, src, []string{"services", svc, "ports"}, `"9999:99"`)
		if _, err := (Splice{}).Identity(got); err != nil {
			t.Fatalf("%s: the result does not parse: %v", svc, err)
		}
		assertAddressable(t, got, []string{"services", svc, "ports", idx}, `"9999:99"`)
	}
}
