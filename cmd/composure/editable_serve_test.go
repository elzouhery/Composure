package main

// `stack/editable` over the wire, and the refusal the pane must never provoke.
//
// The defect this closes was found from the extension, so it is asserted from
// the extension's own transport: the reader selected a service, changed a
// combobox, and the write path answered `path services.web.restart not found`
// — a message about the caller's mistake for a value the pane itself had just
// rendered from the anchor it came from.

import (
	"os"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/edit"
)

// The same shape as testdata/edge/e42: one service that inherits `restart` and
// one that declares it, so a check cannot pass by treating them alike.
const mergedServeFixture = `x-defaults: &defaults
  restart: unless-stopped

services:
  inherits:
    <<: *defaults
    image: nginx:1.27
  declares:
    <<: *defaults
    restart: always
    image: nginx:1.27
`

func TestServeRefusesAReplaceOnAnInheritedValue(t *testing.T) {
	path := writeFixture(t, "compose.yaml", mergedServeFixture)
	before, _ := os.ReadFile(path)
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/preview", map[string]any{
		"file": path,
		"ops": []map[string]any{
			{"operation": "replace_scalar", "at": "services.inherits.restart", "value": "always"},
		},
	})
	if resp.Error == nil {
		t.Fatal("the engine accepted an edit against a path with no bytes behind it")
	}
	if resp.Error.Code != codeEditRefused {
		t.Fatalf("code = %d, want %d — a refusal, not a fault", resp.Error.Code, codeEditRefused)
	}
	if !strings.Contains(resp.Error.Message, "*defaults") {
		t.Fatalf("the refusal does not name the anchor the value came from: %q", resp.Error.Message)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("the file was written")
	}
}

func TestServeEditableAnswersEveryPathAsked(t *testing.T) {
	path := writeFixture(t, "compose.yaml", mergedServeFixture)
	s := start(t)
	s.handshake(1)

	type response struct {
		File   string              `json:"file"`
		Fields []edit.Availability `json:"fields"`
	}
	// Order is the join, so the assertion is by position.
	got := decodeResult[response](t, s.call(2, "stack/editable", map[string]any{
		"file": path,
		"paths": []string{
			"services.inherits.restart",
			"services.declares.restart",
			"services.inherits.image",
		},
	}))
	if len(got.Fields) != 3 {
		t.Fatalf("got %d answers for 3 paths", len(got.Fields))
	}
	if got.Fields[0].Editable || got.Fields[0].Reason != edit.ReasonInherited || got.Fields[0].Plan != "insert_key" {
		t.Fatalf("the inherited one: %+v", got.Fields[0])
	}
	if !got.Fields[1].Editable {
		t.Fatalf("the service that DECLARES restart is editable in place: %+v", got.Fields[1])
	}
	if !got.Fields[2].Editable {
		t.Fatalf("an ordinary scalar: %+v", got.Fields[2])
	}
}

// The override the refusal points at, end to end: staged as an insert, applied
// over the wire, and the anchor left alone.
func TestServeAppliesTheOverrideTheRefusalNames(t *testing.T) {
	path := writeFixture(t, "compose.yaml", mergedServeFixture)
	s := start(t)
	s.handshake(1)

	res := decodeResult[edit.Result](t, s.call(2, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{
			{"operation": "insert_key", "at": "services.inherits", "key": "restart", "value": "always"},
		},
	}))
	if !res.Written || res.Added != 1 || res.Removed != 0 {
		t.Fatalf("written=%v added=%d removed=%d, want one line added", res.Written, res.Added, res.Removed)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "restart: unless-stopped") {
		t.Fatal("the anchor was rewritten; one service's edit changed every service")
	}
	got := decodeResult[struct {
		Fields []edit.Availability `json:"fields"`
	}](t, s.call(3, "stack/editable", map[string]any{"file": path, "at": "services.inherits.restart"}))
	if !got.Fields[0].Editable {
		t.Fatalf("after the override the value has bytes at its own path: %+v", got.Fields[0])
	}
}
