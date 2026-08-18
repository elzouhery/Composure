package resolve

// The override history's ORDER, and its presence on the wire.
//
// Story 1.10's answer is "which file set this value, and what did it replace" —
// an ordered list, oldest first. Every existing test uses a TWO-file merge, and
// a one-entry list has no observable order: reversing it, or emitting only the
// most recent entry, is invisible. Three files make the order falsifiable.
//
// The assertions run over the MARSHALLED value as well as the accessor, because
// the list is consumed as JSON. Tagging `Override.Origin` as `json:"-"` deletes
// the file, line and column of every overridden value from every JSON output in
// the product — R1.8, the thing the wedge rests on — and leaves every
// accessor-level assertion in this package green.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mergeChain loads an explicit N-file chain, named a.yaml, b.yaml, … so that
// Origin.Step and the file name agree with position.
func mergeChain(t *testing.T, sources ...string) *Project {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, len(sources))
	for i, src := range sources {
		name := string(rune('a'+i)) + ".yaml"
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[i] = p
	}
	p, err := Load(Options{Files: paths, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

func TestOverrideHistoryIsOldestFirstAcrossThreeFiles(t *testing.T) {
	p := mergeChain(t,
		"services:\n  web:\n    image: nginx:1.25\n",
		"services:\n  web:\n    image: nginx:1.26\n",
		"services:\n  web:\n    image: nginx:1.27\n",
	)
	v, ok := p.At(ParsePath("services.web.image"))
	if !ok {
		t.Fatal("services.web.image did not resolve")
	}
	if v.Scalar() != "nginx:1.27" {
		t.Fatalf("effective value = %q, want the last file's", v.Scalar())
	}

	hist := v.Overrides()
	if len(hist) != 2 {
		t.Fatalf("override history = %+v, want two entries — the fixture replaces the value twice", hist)
	}
	want := []struct {
		value string
		file  string
		step  int
	}{
		{"nginx:1.25", "a.yaml", 0},
		{"nginx:1.26", "b.yaml", 1},
	}
	for i, w := range want {
		if hist[i].Value != w.value {
			t.Errorf("override %d = %q, want %q — the list is oldest first", i, hist[i].Value, w.value)
		}
		if filepath.Base(hist[i].Origin.File) != w.file {
			t.Errorf("override %d came from %s, want %s", i, hist[i].Origin.File, w.file)
		}
		if hist[i].Origin.Step != w.step {
			t.Errorf("override %d has step %d, want %d", i, hist[i].Origin.Step, w.step)
		}
		if hist[i].Origin.IsZero() {
			t.Errorf("override %d carries no position: %v", i, hist[i].Origin)
		}
	}

	// The same list as a consumer receives it. A history whose origins are
	// dropped by a struct tag is a history that answers "what was replaced" and
	// refuses to answer "from where", which is the half of the question the
	// product exists for.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Overrides []struct {
			Value  string  `json:"value"`
			Origin *Origin `json:"origin"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Overrides) != 2 {
		t.Fatalf("the wire carries %d overrides, want 2: %s", len(wire.Overrides), raw)
	}
	for i, w := range want {
		got := wire.Overrides[i]
		if got.Value != w.value {
			t.Errorf("wire override %d = %q, want %q", i, got.Value, w.value)
		}
		if got.Origin == nil {
			t.Fatalf("wire override %d carries no origin at all: %s", i, raw)
		}
		if got.Origin.IsZero() || filepath.Base(got.Origin.File) != w.file || got.Origin.Step != w.step {
			t.Errorf("wire override %d origin = %v, want %s at step %d", i, got.Origin, w.file, w.step)
		}
	}

	// And Explain answers with the same list, since that is what `composure
	// explain` renders.
	e, err := p.Explain(ParsePath("services.web.image"))
	if err != nil {
		t.Fatal(err)
	}
	ex := e.Overrides()
	if len(ex) != 2 || ex[0].Value != want[0].value || ex[1].Value != want[1].value {
		t.Errorf("Explain's history = %+v, want the same two entries oldest first", ex)
	}
	if e.Origin().IsZero() || filepath.Base(e.Origin().File) != "c.yaml" {
		t.Errorf("Explain's origin = %v, want the last file", e.Origin())
	}
}
