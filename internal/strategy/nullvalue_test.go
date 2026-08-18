package strategy

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Editing a NULL value. This was the worst defect this engine has had: it did
// not fail, it DESTROYED the following line.
//
//	-    healthcheck:
//	-    restart: always
//	+    healthcheck:x restart: always
//
// Every assertion below is on EXACT BYTES. "It still parses" would have passed
// against the corrupting version — `healthcheck:x restart: always` is a
// perfectly valid YAML scalar — which is precisely the weak test this project
// has been burned by. The characteristic failure here is not a crash but a
// confident wrong answer, so the tests compare buffers, not verdicts.

// TestEditNullValueMidFileLeavesFollowingLineIntact is the corruption
// regression. It asserts the whole document byte for byte, so any change to any
// line other than the edited one fails it.
func TestEditNullValueMidFileLeavesFollowingLineIntact(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n    healthcheck:\n    restart: always\n")
	want := "services:\n  web:\n    image: nginx\n    healthcheck: x\n    restart: always\n"

	got, err := Splice{}.Edit(src, []string{"services", "web", "healthcheck"}, "x")
	if err != nil {
		t.Fatalf("Edit over a null value: %v", err)
	}
	if string(got) != want {
		t.Fatalf("null edit changed bytes it does not own\n got: %q\nwant: %q", got, want)
	}
}

// The following line is the thing that was destroyed, so it gets its own
// assertion in the terms the defect was reported in: byte-identical, including
// its leading indentation and its newline.
func TestEditNullValueFollowingLineIsByteIdentical(t *testing.T) {
	src := []byte("services:\n  web:\n    healthcheck:\n    restart: always\n    ports:\n      - \"8080:80\"\n")
	got, err := Splice{}.Edit(src, []string{"services", "web", "healthcheck"}, "x")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	srcLines := strings.Split(string(src), "\n")
	gotLines := strings.Split(string(got), "\n")
	if len(srcLines) != len(gotLines) {
		t.Fatalf("line count moved: %d -> %d\n%q", len(srcLines), len(gotLines), got)
	}
	for i := range srcLines {
		if i == 2 {
			continue // the line the edit owns
		}
		if srcLines[i] != gotLines[i] {
			t.Errorf("line %d changed and the edit does not own it\n got: %q\nwant: %q",
				i+1, gotLines[i], srcLines[i])
		}
	}
	if gotLines[2] != "    healthcheck: x" {
		t.Errorf("edited line is %q, want %q", gotLines[2], "    healthcheck: x")
	}
}

// At end-of-file the same operation errored with "computed range exceeds source
// length" rather than corrupting. Refusing a performable edit is a confident
// wrong answer too, and it is the second half of the same root cause.
func TestEditNullValueAtEndOfFile(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{
			name: "trailing newline",
			src:  "services:\n  web:\n    image: nginx\n    healthcheck:\n",
			want: "services:\n  web:\n    image: nginx\n    healthcheck: x\n",
		},
		{
			name: "no trailing newline",
			src:  "services:\n  web:\n    image: nginx\n    healthcheck:",
			want: "services:\n  web:\n    image: nginx\n    healthcheck: x",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Splice{}.Edit([]byte(tc.src), []string{"services", "web", "healthcheck"}, "x")
			if err != nil {
				t.Fatalf("Edit: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// A trailing comment on the null's line is trivia the edit must step over, not
// through. The value goes between the colon and the comment.
func TestEditNullValueKeepsTrailingComment(t *testing.T) {
	src := []byte("services:\n  web:\n    healthcheck:  # not configured yet\n    restart: always\n")
	want := "services:\n  web:\n    healthcheck: x  # not configured yet\n    restart: always\n"
	got, err := Splice{}.Edit(src, []string{"services", "web", "healthcheck"}, "x")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// CRLF is one of the four bugs the benchmarks caught before, so a null edit is
// checked for it explicitly: the \r belongs to the line ending, not the value.
func TestEditNullValueKeepsCRLF(t *testing.T) {
	src := []byte("services:\r\n  web:\r\n    healthcheck:\r\n    restart: always\r\n")
	want := "services:\r\n  web:\r\n    healthcheck: x\r\n    restart: always\r\n"
	got, err := Splice{}.Edit(src, []string{"services", "web", "healthcheck"}, "x")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// ScalarRange and Edit must agree about a null exactly as they do about a real
// scalar (AD-14). The reported range is empty and splicing the replacement into
// it has to reproduce Edit byte for byte, or a preview is describing an edit
// the write does not perform.
func TestScalarRangeAgreesWithEditOverNull(t *testing.T) {
	src := []byte("services:\n  web:\n    healthcheck:\n    restart: always\n")
	path := []string{"services", "web", "healthcheck"}
	start, end, err := ScalarRange(src, path)
	if err != nil {
		t.Fatalf("ScalarRange: %v", err)
	}
	if start != end {
		t.Errorf("range over a null is [%d,%d); a value with no bytes has an empty range", start, end)
	}
	if got := string(src[start:end]); got != "" {
		t.Errorf("range covers %q, want nothing", got)
	}
	edited, err := Splice{}.Edit(src, path, "x")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	spliced := string(src[:start]) + " x" + string(src[end:])
	if spliced != string(edited) {
		t.Errorf("the reported range does not reproduce Edit\n got: %q\nwant: %q", spliced, edited)
	}
}

// Setting a null to nothing writes NOTHING — not a lone trailing space. The
// caller gets identical bytes and internal/edit turns that into ErrNoChange,
// which is the honest answer to "set this empty thing to empty".
func TestEditNullValueToEmptyWritesNoBytes(t *testing.T) {
	src := []byte("services:\n  web:\n    healthcheck:\n    restart: always\n")
	got, err := Splice{}.Edit(src, []string{"services", "web", "healthcheck"}, "")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if string(got) != string(src) {
		t.Fatalf("empty value wrote bytes\n got: %q\nwant: %q", got, src)
	}
}

// The permanent regression fixtures, exercised as files. CLAUDE.md's definition
// of done: any new failure mode found becomes a file under testdata/, and the
// file is only a regression test if something reads it.
func TestNullValueFixtures(t *testing.T) {
	for _, tc := range []struct {
		file, path string
	}{
		{"../../testdata/edge/e11-null-value-mid-file.yml", "healthcheck"},
		{"../../testdata/edge/e12-null-value-eof.yml", "healthcheck"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			src, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			path := []string{"services", "web", tc.path}
			got, err := Splice{}.Edit(src, path, "CMD-SHELL true")
			if err != nil {
				t.Fatalf("Edit: %v", err)
			}

			srcLines := strings.Split(string(src), "\n")
			gotLines := strings.Split(string(got), "\n")
			if len(srcLines) != len(gotLines) {
				t.Fatalf("line count moved %d -> %d", len(srcLines), len(gotLines))
			}
			changed := 0
			for i := range srcLines {
				if srcLines[i] != gotLines[i] {
					changed++
					if want := srcLines[i] + " CMD-SHELL true"; gotLines[i] != want {
						t.Errorf("line %d is %q, want %q", i+1, gotLines[i], want)
					}
				}
			}
			if changed != 1 {
				t.Errorf("%d lines changed, want exactly 1", changed)
			}
			if _, err := (Splice{}).Identity(got); err != nil {
				t.Errorf("result does not parse: %v", err)
			}
		})
	}
}

// A null that is not the value of a mapping key has no `key:` to attach a value
// to. There is no safe answer, so the engine refuses with a typed sentinel the
// way ErrFlowStyle does — it never guesses an offset and writes there.
func TestEditNullSequenceEntryRefuses(t *testing.T) {
	src := []byte("services:\n  web:\n    ports:\n      - \"8080:80\"\n      -\n")
	_, err := Splice{}.Edit(src, []string{"services", "web", "ports", "1"}, "9090:90")
	if err == nil {
		t.Fatal("editing a null sequence entry was accepted; it must be refused")
	}
	if !errors.Is(err, ErrNullValue) {
		t.Errorf("error is %v, want ErrNullValue", err)
	}
}
