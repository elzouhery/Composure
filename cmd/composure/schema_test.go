package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const schemaFixture = `version: "3.8"
services:
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: hunter2
`

// The literal wire keys the TypeScript inspector reads.
//
// The extension navigates this payload by name. None of those names appears in
// a Go identifier, so nothing in the Go build fails when one changes — the
// inspector simply renders an empty pane, which looks exactly like a stack with
// nothing set. The assertions are made against a decoded map rather than a
// struct so that a struct tag cannot supply the name it is meant to be checked
// against.
func TestServeSchemaWireKeys(t *testing.T) {
	path := writeFixture(t, "compose.yaml", schemaFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/schema","params":{"path":%q,"at":"services.db"}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/schema returned an error: %+v", resp.Error)
	}

	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, key := range []string{
		"path", "schema_commit", "compose_version", "compose_version_known", "files", "profiles", "node",
	} {
		if _, ok := payload[key]; !ok {
			t.Errorf("the answer has no %q", key)
		}
	}
	// AD-20: the obsolete field is reported, never used to choose a schema.
	if payload["version_field"] != "3.8" {
		t.Errorf("version_field = %v, want it reported", payload["version_field"])
	}

	node, ok := payload["node"].(map[string]any)
	if !ok {
		t.Fatalf("node is %T", payload["node"])
	}
	if node["path"] != "services.db" {
		t.Errorf("node path = %v", node["path"])
	}
	fields, ok := node["fields"].([]any)
	if !ok || len(fields) < 60 {
		t.Fatalf("%d fields; the service schema names about ninety", len(fields))
	}

	var declared, available int
	sawValue := false
	for _, raw := range fields {
		f := raw.(map[string]any)
		for _, key := range []string{"key", "declared", "path", "support"} {
			if _, ok := f[key]; !ok {
				t.Fatalf("field %v has no %q", f["key"], key)
			}
		}
		if f["declared"] == true {
			declared++
			if f["key"] == "image" {
				value := f["value"].(map[string]any)
				if value["text"] != "postgres:16-alpine" {
					t.Errorf("image value = %v, want the text as written", value["text"])
				}
				origin := value["origin"].(map[string]any)
				if origin["line"].(float64) != 4 {
					t.Errorf("image origin line = %v, want 4", origin["line"])
				}
				sawValue = true
			}
		} else {
			available++
		}
	}
	if !sawValue {
		t.Error("no declared field carried its value; a key without its value is the incumbent's failure")
	}
	if declared != 2 || available < 60 {
		t.Errorf("%d declared and %d available, want 2 declared and the rest of the schema", declared, available)
	}
}

// The RPC and the CLI must not be two implementations.
func TestServeSchemaMatchesTheLibrary(t *testing.T) {
	path := writeFixture(t, "compose.yaml", schemaFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/schema","params":{"path":%q,"at":"services.db"}}`, path))
	first := s.read()
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"stack/schema","params":{"path":%q,"at":"services.db"}}`, path))
	second := s.read()
	if !jsonEqual(t, first.Result, second.Result) {
		t.Error("two identical requests produced different answers; the list must be deterministic")
	}
}

func TestServeSchemaRequiresAPath(t *testing.T) {
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"stack/schema","params":{}}`)
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params, got %+v / %s", resp.Error, resp.Result)
	}
}

// A config path in neither the file nor the schema is the client's mistake and
// is named as one, rather than answered with an empty field list that reads as
// "this service has nothing set".
func TestServeSchemaUnknownPathIsRefused(t *testing.T) {
	path := writeFixture(t, "compose.yaml", schemaFixture)
	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/schema","params":{"path":%q,"at":"services.db.nope.nope"}}`, path))
	resp := s.read()
	if resp.Error == nil {
		t.Fatalf("expected a refusal, got %s", resp.Result)
	}
}

// The stack itself, which is what the inspector shows when nothing is
// selected. Never an empty pane.
func TestServeSchemaStackLevel(t *testing.T) {
	path := writeFixture(t, "compose.yaml", schemaFixture)
	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/schema","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/schema returned an error: %+v", resp.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	node := payload["node"].(map[string]any)
	if node["path"] != "" || node["schema"] != "compose" {
		t.Errorf("the stack node is %v", node)
	}
	if len(node["fields"].([]any)) == 0 {
		t.Error("the stack node has no fields; the pane would be empty")
	}
	files := payload["files"].([]any)
	if len(files) != 1 {
		t.Errorf("%d source files listed", len(files))
	}
}

// ---------------------------------------------------------------------------
// Story 7.9 — the values a key accepts, over both doors.

const allowedFixture = `services:
  web:
    image: nginx
    restart: unless-stopped
`

// The CLI door. CLAUDE.md's "CLI before UI, always": the picker in the
// extension is built on this answer, so this answer is asserted before it.
//
// Asserted on the CONTENTS of the list, in order, over three different keys
// with three different sources. A count would pass on the right length and the
// wrong words, and one key with one value cannot tell a list read out of the
// specification from a list typed into a Go file.
func TestSchemaCLICarriesAllowedValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(allowedFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, dir, "schema", "-json", "-at", "services.web", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("composure schema exited %d: %s", res.code, res.stderr)
	}
	var payload struct {
		Node struct {
			Fields []struct {
				Key           string   `json:"key"`
				Allowed       []string `json:"allowed"`
				AllowedSource string   `json:"allowed_source"`
			} `json:"fields"`
		} `json:"node"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, res.stdout)
	}
	got := map[string][]string{}
	source := map[string]string{}
	for _, f := range payload.Node.Fields {
		got[f.Key] = f.Allowed
		source[f.Key] = f.AllowedSource
	}
	want := map[string][]string{
		// Declared in the fixture: the picker has to reach a SET key too, not
		// only the `available, not set` half.
		"restart":        {"no", "always", "on-failure", "unless-stopped"},
		"pull_policy":    {"always", "never", "build", "if_not_present", "missing", "refresh", "daily", "weekly"},
		"cgroup":         {"host", "private"},
		"network_mode":   {"bridge", "host", "none"},
		"image":          nil,
		"container_name": nil,
		// The fourth source, and the one the human table used to lie about:
		// `gpus` is `"all"` OR a list of GPU device objects, so the enum on the
		// string arm is not a bound on the key.
		"gpus": {"all"},
	}
	wantSource := map[string]string{
		"restart": "description", "pull_policy": "pattern", "cgroup": "schema",
		"network_mode": "description", "image": "", "container_name": "",
		"gpus": "schema-branch",
	}
	for key, values := range want {
		if strings.Join(got[key], ",") != strings.Join(values, ",") {
			t.Errorf("%s allowed = %v, want %v", key, got[key], values)
		}
		if source[key] != wantSource[key] {
			t.Errorf("%s allowed_source = %q, want %q", key, source[key], wantSource[key])
		}
	}
}

// The RPC door, which is the one the extension actually uses. Both are checked
// because a field can reach one and not the other — `stack/dockerfile`'s
// vocabulary reached both and neither was asserted until story 7.8.
func TestServeSchemaCarriesAllowedValues(t *testing.T) {
	path := writeFixture(t, "compose.yaml", allowedFixture)
	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/schema","params":{"path":%q,"at":"services.web"}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/schema returned an error: %+v", resp.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	fields := payload["node"].(map[string]any)["fields"].([]any)
	for _, raw := range fields {
		f := raw.(map[string]any)
		if f["key"] != "restart" {
			continue
		}
		list, ok := f["allowed"].([]any)
		if !ok {
			t.Fatalf("restart carries no \"allowed\" over the RPC: %v", f)
		}
		var got []string
		for _, v := range list {
			got = append(got, v.(string))
		}
		if strings.Join(got, ",") != "no,always,on-failure,unless-stopped" {
			t.Errorf("restart allowed over RPC = %v", got)
		}
		if f["allowed_source"] != "description" {
			t.Errorf("restart allowed_source over RPC = %v", f["allowed_source"])
		}
		return
	}
	t.Fatal("no restart field in the answer")
}

// The human table says it too. A capability visible only as JSON is a
// capability the reader checking the tool by hand cannot see.
func TestSchemaTableNamesTheAllowedValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(allowedFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, dir, "schema", "-at", "services.web", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("composure schema exited %d: %s", res.code, res.stderr)
	}
	for _, want := range []string{
		"one of no · always · on-failure · unless-stopped",
		"one of host · private",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("the table does not say %q\n%s", want, res.stdout)
		}
	}
	// And the closedness note is on the right lines. `cgroup` is a plain enum
	// and closed; `gpus` is an enum on one arm of a `oneOf` whose other arm is
	// a list of GPU device objects, and the table used to print
	//
	//     one of all (the specification allows nothing else)
	//
	// which is the one false sentence this command could print about the whole
	// vendored document.
	for _, line := range strings.Split(res.stdout, "\n") {
		if !strings.Contains(line, "one of ") {
			continue
		}
		closed := strings.Contains(line, "allows nothing else")
		switch {
		case strings.Contains(line, "one of host · private") && !closed:
			t.Errorf("a plain enum is no longer said to be closed: %q", line)
		case strings.Contains(line, "one of all") && closed:
			t.Errorf("gpus is printed as a closed set: %q", line)
		}
	}
	if !strings.Contains(res.stdout, "one of all") {
		t.Errorf("gpus offers nothing at all now; `all` is a real value\n%s", res.stdout)
	}
}
