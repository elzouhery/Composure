package strategy

import (
	"strings"
	"testing"
)

// The ranges exported for diagnostics have exactly one job: to be the same
// bytes the splice engine would touch. AD-14 is the rule; these are the proof.
// A range that merely looks right is the failure mode this engine is prone to —
// it does not crash, it returns a confident wrong answer.

const rangeFixture = `# top comment
services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
      - "9090:90"
    environment:
      PASSWORD: hunter2      # trailing comment

  # documents the cache
  cache:
    image: redis:7

volumes:
  pgdata:
  scratch:
`

func TestDeleteRangeMatchesDeleteKey(t *testing.T) {
	paths := [][]string{
		{"services", "web", "ports"},
		{"services", "cache"},
		{"services", "web"},
		{"volumes", "scratch"},
		{"volumes", "pgdata"},
		{"services", "web", "environment", "PASSWORD"},
	}
	src := []byte(rangeFixture)
	for _, p := range paths {
		t.Run(strings.Join(p, "."), func(t *testing.T) {
			want, err := Splice{}.DeleteKey(src, p)
			if err != nil {
				t.Fatalf("DeleteKey: %v", err)
			}
			start, end, err := DeleteRange(src, p)
			if err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			got := append(append([]byte{}, src[:start]...), src[end:]...)
			if string(got) != string(want) {
				t.Errorf("splicing out [%d,%d) does not reproduce DeleteKey\n got: %q\nwant: %q",
					start, end, got, want)
			}
		})
	}
}

// A described fix for a scalar must name the lexeme and nothing else. The
// engine has already shipped a bug where a range ran past the value and welded
// the following comment onto it; this is the regression test for the reported
// range, which is the same defect one layer earlier.
func TestScalarRangeExcludesTrailingComment(t *testing.T) {
	src := []byte(rangeFixture)
	start, end, err := ScalarRange(src, []string{"services", "web", "environment", "PASSWORD"})
	if err != nil {
		t.Fatalf("ScalarRange: %v", err)
	}
	if got := string(src[start:end]); got != "hunter2" {
		t.Errorf("range covers %q, want exactly %q", got, "hunter2")
	}
}

// A sequence entry has to be addressable: a port collision names one entry of a
// `ports:` list, and a fix that could not point at it would have no range.
func TestScalarRangeAddressesSequenceEntry(t *testing.T) {
	src := []byte(rangeFixture)
	start, end, err := ScalarRange(src, []string{"services", "web", "ports", "1"})
	if err != nil {
		t.Fatalf("ScalarRange: %v", err)
	}
	if got := string(src[start:end]); got != `"9090:90"` {
		t.Errorf("range covers %q, want %q", got, `"9090:90"`)
	}
}

// Editing through the same path must produce the range the reader was shown.
func TestScalarRangeAgreesWithEdit(t *testing.T) {
	src := []byte(rangeFixture)
	path := []string{"services", "web", "image"}
	start, end, err := ScalarRange(src, path)
	if err != nil {
		t.Fatalf("ScalarRange: %v", err)
	}
	edited, err := Splice{}.Edit(src, path, "nginx:1.28")
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	spliced := string(src[:start]) + "nginx:1.28" + string(src[end:])
	if spliced != string(edited) {
		t.Errorf("the reported range does not reproduce Edit\n got: %q\nwant: %q", spliced, edited)
	}
}

// InsertionPoint has to land where InsertKey lands, or a described "add a
// healthcheck" fix points at the wrong service.
func TestInsertionPointMatchesInsertKey(t *testing.T) {
	src := []byte(rangeFixture)
	path := []string{"services", "cache"}
	offset, indent, err := InsertionPoint(src, path)
	if err != nil {
		t.Fatalf("InsertionPoint: %v", err)
	}
	want, err := Splice{}.InsertKey(src, path, "restart", "always")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	got := string(src[:offset]) + "\n" + strings.Repeat(" ", indent) + "restart: always" + string(src[offset:])
	if got != string(want) {
		t.Errorf("insertion at %d does not reproduce InsertKey\n got: %q\nwant: %q", offset, got, want)
	}
}

// A path that does not resolve gets no range. Refusing is the contract (AD-8):
// a fix described against a range the engine would not touch is worse than no
// fix at all.
func TestRangeRefusesUnknownPath(t *testing.T) {
	src := []byte(rangeFixture)
	for _, p := range [][]string{
		{"services", "nope"},
		{"services", "web", "ports", "9"},
		{"services", "web", "image", "deeper"},
	} {
		if _, _, err := Range(src, p); err == nil {
			t.Errorf("Range(%v) returned no error", p)
		}
		if _, _, err := DeleteRange(src, p); err == nil {
			t.Errorf("DeleteRange(%v) returned no error", p)
		}
	}
}
