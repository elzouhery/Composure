package main

// Story 1.10 across both surfaces. The CLI and the RPC must answer the same
// question with the same bytes: an editor popover that disagreed with the
// command line about which file set a value would make both untrustworthy.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

func explainProject(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("compose.yaml", "services:\n  db:\n    image: postgres:15\n    restart: always\n")
	must("compose.override.yaml", "services:\n  db:\n    image: postgres:16\n")
	return dir
}

func TestExplainWireCarriesOriginAndHistory(t *testing.T) {
	dir := explainProject(t)
	p, err := resolve.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := p.Explain(resolve.ParsePath("services.db.image"))
	if err != nil {
		t.Fatal(err)
	}
	w := explanationWire(p, e)

	if w.Path != "services.db.image" || w.Value != "postgres:16" {
		t.Errorf("wire = %+v", w)
	}
	if filepath.Base(w.File) != "compose.override.yaml" || w.Line != 3 || w.Step != 1 {
		t.Errorf("origin = %s:%d step %d", w.File, w.Line, w.Step)
	}
	if len(w.Overrides) != 1 || w.Overrides[0].Value != "postgres:15" {
		t.Fatalf("overrides = %+v", w.Overrides)
	}
	// The ordered file list travels with the answer, so a consumer can turn a
	// step number into a path without a second call.
	if len(w.Files) != 2 {
		t.Errorf("files = %+v, want both source files", w.Files)
	}
	// Present and empty, never absent: a value set once must be expressible.
	one, err := p.Explain(resolve.ParsePath("services.db.restart"))
	if err != nil {
		t.Fatal(err)
	}
	if got := explanationWire(p, one).Overrides; got == nil || len(got) != 0 {
		t.Errorf("overrides = %v, want present and empty", got)
	}
	raw, err := json.Marshal(explanationWire(p, one))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["overrides"]; !present {
		t.Error("the JSON drops an empty override history; absence and emptiness must not look alike")
	}
}

// stack/explain is `composure explain -json` reached a second way, and the two
// build the same struct.
func TestServeExplainMatchesTheCLIStruct(t *testing.T) {
	dir := explainProject(t)
	path := filepath.Join(dir, "compose.yaml")

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"stack/explain","params":{"path":%q,"files":[%q,%q],"at":"services.db.image"}}`,
		path, path, filepath.Join(dir, "compose.override.yaml")))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/explain returned an error: %+v", resp.Error)
	}

	p, err := resolve.Files(path, filepath.Join(dir, "compose.override.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := p.Explain(resolve.ParsePath("services.db.image"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(explanationWire(p, e))
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, resp.Result, want) {
		t.Errorf("RPC payload differs from the CLI struct\n rpc: %s\n cli: %s", resp.Result, want)
	}
}

// A path that addresses nothing is an ERROR carrying the closest paths, never
// an empty result — an empty result reads as "this value has no provenance",
// which is the one thing this model makes impossible.
func TestServeExplainMissingPathCarriesSuggestions(t *testing.T) {
	dir := explainProject(t)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"stack/explain","params":{"path":%q,"at":"services.db.imag"}}`,
		filepath.Join(dir, "compose.yaml")))
	resp := s.read()
	if resp.Error == nil {
		t.Fatal("a path that does not exist returned a result")
	}
	if resp.Error.Code != codeNoSuchPath {
		t.Errorf("code = %d, want codeNoSuchPath", resp.Error.Code)
	}
	var data struct {
		Closest []string `json:"closest"`
	}
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("decode error data: %v", err)
	}
	if len(data.Closest) == 0 || data.Closest[0] != "services.db.image" {
		t.Errorf("closest = %v, want services.db.image first", data.Closest)
	}
}

// `at` is required. "Explain the whole project" is what stack/resolve already
// answers, and a silently-empty `at` would return the document root as though
// that were the question.
func TestServeExplainRequiresAConfigPath(t *testing.T) {
	dir := explainProject(t)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/explain","params":{"path":%q}}`,
		filepath.Join(dir, "compose.yaml")))
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("error = %+v, want invalid params", resp.Error)
	}
}

// The `files` parameter is the RPC form of `-f`, and it must disable the
// automatic override pickup exactly as the flag does — or the editor and the
// command line would resolve two different projects from the same request.
func TestServeFilesParameterDisablesTheOverridePickup(t *testing.T) {
	dir := explainProject(t)
	path := filepath.Join(dir, "compose.yaml")

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"stack/explain","params":{"path":%q,"files":[%q],"at":"services.db.image"}}`,
		path, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/explain returned an error: %+v", resp.Error)
	}
	var got explainJSON
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Value != "postgres:15" {
		t.Errorf("value = %q; an explicit file list must not pick up the override file", got.Value)
	}
	if len(got.Files) != 1 {
		t.Errorf("files = %+v, want only the named file", got.Files)
	}
}

// Naming the DIRECTORY picks the override file up, which is what Compose does.
func TestServeDirectoryPathPicksUpTheOverrideFile(t *testing.T) {
	dir := explainProject(t)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/explain","params":{"path":%q,"at":"services.db.image"}}`, dir))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/explain returned an error: %+v", resp.Error)
	}
	var got explainJSON
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Value != "postgres:16" {
		t.Errorf("value = %q; a directory must be resolved with its override file", got.Value)
	}
}
