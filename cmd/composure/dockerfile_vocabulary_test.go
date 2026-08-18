package main

// The instruction vocabulary has to arrive over BOTH doors — `composure
// dockerfile` and `stack/dockerfile` — in the same shape stack/schema uses for
// compose, because the extension renders `available, not set` from one
// component and the whole point of AD-20 is that neither side holds a list.
//
// The wire assertions read the JSON as JSON rather than through the Go structs.
// A test that unmarshalled into internal/dockerfile's own types would pass if
// every json tag were renamed at once, which is exactly the skew the extension
// would then hit.

import (
	"encoding/json"
	"testing"
)

func TestServeDockerfileCarriesTheInstructionVocabulary(t *testing.T) {
	path := writeFixture(t, "Dockerfile", dockerfileFixture)
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/dockerfile", map[string]any{"path": path})
	if resp.Error != nil {
		t.Fatalf("stack/dockerfile failed: %+v", resp.Error)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		t.Fatal(err)
	}
	vocabRaw, ok := raw["vocabulary"]
	if !ok {
		t.Fatal(`the form carries no "vocabulary": the stage form still has no "available, not set" block`)
	}

	var file vocabView
	if err := json.Unmarshal(vocabRaw, &file); err != nil {
		t.Fatal(err)
	}

	// The same three field names stack/schema uses for compose: declared_count,
	// available_count, and a per-item `declared`.
	if file.Scope != "file" {
		t.Errorf("scope = %q, want %q", file.Scope, "file")
	}
	if file.DeclaredCount == 0 || file.AvailableCount == 0 {
		t.Fatalf("split is %d used / %d available; one half is empty",
			file.DeclaredCount, file.AvailableCount)
	}
	if got := file.DeclaredCount + file.AvailableCount; got != len(file.Instructions) {
		t.Errorf("%d + %d != %d instructions", file.DeclaredCount, file.AvailableCount, got)
	}

	byName := map[string]vocabEntry{}
	for _, e := range file.Instructions {
		byName[e.Name] = e
	}
	// The fixture is FROM / WORKDIR / RUN / FROM / COPY.
	for _, name := range []string{"FROM", "WORKDIR", "RUN", "COPY"} {
		if !byName[name].Declared {
			t.Errorf("%s is used by the fixture and the wire says it is not", name)
		}
	}
	// And what it does not use — the half that is the product's differentiator.
	for _, name := range []string{"HEALTHCHECK", "USER", "ENTRYPOINT", "VOLUME"} {
		e, ok := byName[name]
		if !ok {
			t.Errorf("%s is not offered at all; the grammar permits it", name)
			continue
		}
		if e.Declared {
			t.Errorf("%s is not in the fixture and the wire says it is", name)
		}
		if e.Summary == "" {
			t.Errorf("%s is offered with no description; the reader is shown a bare name", name)
		}
	}
	if len(byName["COPY"].Flags) == 0 {
		t.Error("COPY is offered with no flags; --from is the flag a multi-stage reader needs")
	}

	// Per stage, too. Instruction scope in a Dockerfile is the stage.
	var stages struct {
		Stages []struct {
			Label      string    `json:"label"`
			Vocabulary vocabView `json:"vocabulary"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(resp.Result, &stages); err != nil {
		t.Fatal(err)
	}
	if len(stages.Stages) != 2 {
		t.Fatalf("%d stages, want 2", len(stages.Stages))
	}
	for _, st := range stages.Stages {
		if st.Vocabulary.Scope != "stage" {
			t.Errorf("stage %s: scope = %q, want %q", st.Label, st.Vocabulary.Scope, "stage")
		}
		if st.Vocabulary.DeclaredCount == 0 {
			t.Errorf("stage %s: no instructions reported as used", st.Label)
		}
	}
	// The two stages must NOT report the same used set — a pooled answer would.
	a, b := usedNames(stages.Stages[0].Vocabulary.Instructions), usedNames(stages.Stages[1].Vocabulary.Instructions)
	if equalSets(a, b) {
		t.Errorf("both stages report the same instructions (%v); the split is not stage-scoped", a)
	}
}

// vocabEntry and vocabView are the wire shape, spelled out here by hand. They
// deliberately do NOT reuse internal/dockerfile's types: a test that shared the
// structs would pass if every json tag were renamed at once, which is exactly
// the skew that reaches the extension as a field that silently stopped
// arriving.
type vocabEntry struct {
	Name     string   `json:"name"`
	Summary  string   `json:"summary"`
	Declared bool     `json:"declared"`
	Uses     int      `json:"uses"`
	Flags    []string `json:"flags"`
}

type vocabView struct {
	Scope          string       `json:"scope"`
	DeclaredCount  int          `json:"declared_count"`
	AvailableCount int          `json:"available_count"`
	Instructions   []vocabEntry `json:"instructions"`
}

func usedNames(entries []vocabEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.Declared {
			out[e.Name] = true
		}
	}
	return out
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
