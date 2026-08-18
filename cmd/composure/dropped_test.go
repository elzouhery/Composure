package main

// The form-mismatch loss, on every surface a human reads.
//
// `environment` written as a LIST in one file and a MAPPING in the other drops
// the base file's entries. That drop is deliberate — the resolved model keeps
// the shape each file wrote, because the splice engine edits those bytes — but
// it was reported only in `composure resolve -json`. For anyone reading a table,
// configuration disappeared with no signal at all, which is the silent failure
// CLAUDE.md rule 6 forbids.
//
// These tests drive the PRINTERS rather than the wire structs on purpose. The
// wire has carried the finding all along; what was missing was any surface that
// rendered it, so a test that stops at the struct would pass with the printing
// deleted again.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

// crossFormProject is a base that writes `environment` as a list and an
// override that writes it as a mapping. BASE_A exists only in the base, so it
// is exactly what the merge loses; `docker compose config` keeps it.
func crossFormProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", "services:\n  web:\n    image: nginx\n    environment:\n      - BASE_A=1\n      - SHARED=base\n")
	write("compose.override.yaml", "services:\n  web:\n    environment:\n      SHARED: over\n")
	return dir
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// The printers write to os.Stdout directly, so this is the only way to hold
// them to what they say.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	func() {
		defer func() {
			os.Stdout = saved
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// SURFACE 1: the `resolve` human table.
func TestResolveTableNamesWhatTheMergeDropped(t *testing.T) {
	dir := crossFormProject(t)
	p, err := resolve.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { runResolve(p, dir, false) })

	if !strings.Contains(out, "DROPPED IN MERGE") {
		t.Fatalf("the resolve table says nothing about the dropped entry:\n%s", out)
	}
	// WHAT was dropped.
	if !strings.Contains(out, "BASE_A") {
		t.Errorf("the table does not name the lost entry:\n%s", out)
	}
	// FROM WHICH FILE.
	if !strings.Contains(out, "compose.yaml:5") {
		t.Errorf("the table does not say which declaration lost its entries:\n%s", out)
	}
	// WHY.
	if !strings.Contains(out, "re-emitting the collection") {
		t.Errorf("the table does not say why the entry was dropped:\n%s", out)
	}
}

// A project whose files agree on the form prints no such section. A warning
// that is always on is a warning nobody reads.
func TestResolveTableIsSilentWhenNothingWasDropped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("services:\n  web:\n    image: nginx\n    environment:\n      A: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := resolve.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, func() { runResolve(p, dir, false) }); strings.Contains(out, "DROPPED IN MERGE") {
		t.Errorf("a clean merge reported a loss:\n%s", out)
	}
}

// SURFACE 2: `explain`, which answered "a collection — see the file" and
// nothing else. Explaining the SERVICE, not the collection: that is how the
// question is actually asked, and the finding has to reach up the path.
func TestExplainTableNamesWhatTheMergeDropped(t *testing.T) {
	dir := crossFormProject(t)
	p, err := resolve.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { runExplain(p, dir, "services.web", false) })

	if !strings.Contains(out, "DROPPED IN MERGE") {
		t.Fatalf("explain says nothing about the dropped entry:\n%s", out)
	}
	if !strings.Contains(out, "BASE_A") {
		t.Errorf("explain does not name the lost entry:\n%s", out)
	}
	if !strings.Contains(out, "compose.yaml:5") {
		t.Errorf("explain does not say which declaration lost its entries:\n%s", out)
	}
	if !strings.Contains(out, "re-emitting the collection") {
		t.Errorf("explain does not say why:\n%s", out)
	}
}

// The wire carries it too, present and empty when there is nothing to say —
// the same contract Overrides has.
func TestExplainWireCarriesTheDrop(t *testing.T) {
	dir := crossFormProject(t)
	p, err := resolve.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := p.Explain(resolve.ParsePath("services.web.environment"))
	if err != nil {
		t.Fatal(err)
	}
	w := explanationWire(p, e)
	if len(w.Dropped) != 1 {
		t.Fatalf("dropped = %+v, want the one form mismatch", w.Dropped)
	}
	if len(w.Dropped[0].Dropped) != 1 || w.Dropped[0].Dropped[0] != "BASE_A" {
		t.Errorf("the finding does not name what was lost: %+v", w.Dropped[0])
	}
	if filepath.Base(w.Dropped[0].Displaced.File) != "compose.yaml" {
		t.Errorf("the finding does not name the file that lost entries: %+v", w.Dropped[0].Displaced)
	}

	// A path with nothing under it reports present-and-empty, not null.
	clean, err := p.Explain(resolve.ParsePath("services.web.image"))
	if err != nil {
		t.Fatal(err)
	}
	if got := explanationWire(p, clean).Dropped; got == nil || len(got) != 0 {
		t.Errorf("dropped = %v, want present and empty", got)
	}
}
