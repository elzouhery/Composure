package main

// stack/impact is the one thing the canvas's focus mode dims by, and the whole
// point of it living in Go is that the extension does not compute a transitive
// closure of its own. These assert the wire shape a client navigates by — none
// of those names appears in a Go identifier, so renaming a struct tag breaks no
// build and silently dims the wrong half of a stack.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

const impactFixture = `services:
  gateway:
    image: nginx
    depends_on: [api]
  api:
    image: api
    depends_on: [db]
  db:
    image: postgres
  lonely:
    image: busybox
    networks: [front]
networks:
  front: {}
`

func TestImpactOfMatchesTheLibrary(t *testing.T) {
	path := writeFixture(t, "compose.yaml", impactFixture)
	project, err := resolve.File(path)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	g, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("build topology: %v", err)
	}

	wire, ok := impactOf(g, path, "services.api")
	if !ok {
		t.Fatal("services.api is not a node")
	}
	if wire.Subject != "services.api" {
		t.Errorf("subject = %q", wire.Subject)
	}
	// depends_on only: gateway breaks, db is needed. The shared network does
	// not make `lonely` part of anything.
	if got := fmt.Sprint(wire.Dependents); got != "[services.gateway]" {
		t.Errorf("dependents = %v", wire.Dependents)
	}
	if got := fmt.Sprint(wire.Dependencies); got != "[services.db]" {
		t.Errorf("dependencies = %v", wire.Dependencies)
	}

	// An empty answer is `[]`, never null: a client that has to tell "nothing
	// depends on this" from "the field did not arrive" has been given a puzzle
	// instead of an answer.
	lonely, ok := impactOf(g, path, "services.lonely")
	if !ok {
		t.Fatal("services.lonely is not a node")
	}
	b, err := json.Marshal(lonely)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"path", "subject", "dependents", "dependencies"} {
		if _, present := raw[key]; !present {
			t.Errorf("wire has no %q key: %s", key, b)
		}
	}
	if string(raw["dependents"]) != "[]" || string(raw["dependencies"]) != "[]" {
		t.Errorf("an isolated service should carry empty lists, got %s", b)
	}

	if _, ok := impactOf(g, path, "services.nope"); ok {
		t.Error("a path that is not a node reported an impact")
	}
}

func TestServeImpact(t *testing.T) {
	path := writeFixture(t, "compose.yaml", impactFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"stack/impact","params":{"path":%q,"at":"services.api"}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/impact returned an error: %+v", resp.Error)
	}
	var got impactWire
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if got.Subject != "services.api" {
		t.Errorf("subject = %q", got.Subject)
	}
	if len(got.Dependents) != 1 || got.Dependents[0] != "services.gateway" {
		t.Errorf("dependents = %v", got.Dependents)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0] != "services.db" {
		t.Errorf("dependencies = %v", got.Dependencies)
	}
}

func TestServeImpactUnknownPathIsAnError(t *testing.T) {
	path := writeFixture(t, "compose.yaml", impactFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"stack/impact","params":{"path":%q,"at":"services.nope"}}`, path))
	resp := s.read()
	if resp.Error == nil {
		t.Fatal("a path that is not a node answered with a result")
	}
	if resp.Error.Code != codeNoSuchPath {
		t.Errorf("code = %d, want %d", resp.Error.Code, codeNoSuchPath)
	}
}

func TestServeImpactRequiresAt(t *testing.T) {
	path := writeFixture(t, "compose.yaml", impactFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/impact","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("missing \"at\" should be invalid params, got %+v", resp.Error)
	}
}

// The profile set travels on the request, so the blast radius is computed
// against the same filtered graph the canvas is drawing. A service the active
// profiles removed is not a node, and saying "no such node" is the honest
// answer — dimming a set derived from a different graph is not.
func TestServeImpactHonoursProfiles(t *testing.T) {
	path := writeFixture(t, "compose.yaml", `services:
  api:
    image: api
  tools:
    image: busybox
    profiles: [debug]
    depends_on: [api]
`)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"stack/impact","params":{"path":%q,"at":"services.api"}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/impact returned an error: %+v", resp.Error)
	}
	var got impactWire
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Dependents) != 0 {
		t.Errorf("without the debug profile nothing depends on api, got %v", got.Dependents)
	}

	s.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":3,"method":"stack/impact","params":{"path":%q,"at":"services.api","profiles":["debug"]}}`,
		path))
	resp = s.read()
	if resp.Error != nil {
		t.Fatalf("stack/impact with a profile returned an error: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Dependents) != 1 || got.Dependents[0] != "services.tools" {
		t.Errorf("with the debug profile tools depends on api, got %v", got.Dependents)
	}
}
