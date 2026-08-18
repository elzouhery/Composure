package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

const topologyFixture = `services:
  gateway:
    image: nginx
    ports: ["8080:80"]
    depends_on:
      api:
        condition: service_healthy
    networks: [front]
  api:
    image: api
    depends_on: [db]
    networks:
      front:
        aliases: [api-internal]
    volumes: ["data:/var/lib"]
  db:
    image: postgres
    volumes: ["data:/var/lib/postgresql/data"]
  tools:
    image: busybox
    profiles: [debug]
    depends_on: [api]
networks:
  front: {internal: true}
volumes:
  data: {}
`

// topologyJSONViaLibrary is the call the CLI makes, marshalled the same way.
func topologyJSONViaLibrary(t *testing.T, path string, profiles []string) []byte {
	t.Helper()
	project, err := resolve.File(path)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	g, err := topology.Build(project, profiles)
	if err != nil {
		t.Fatalf("build topology: %v", err)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	return b
}

func TestServeTopologyMatchesLibraryOutput(t *testing.T) {
	path := writeFixture(t, "compose.yaml", topologyFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/topology","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/topology returned an error: %+v", resp.Error)
	}
	if !jsonEqual(t, resp.Result, topologyJSONViaLibrary(t, path, nil)) {
		t.Errorf("RPC payload differs from the library marshaller\n rpc: %s", resp.Result)
	}
}

// The literal wire keys a client navigates by. None of these names appears in
// a Go identifier, so renaming a struct tag breaks no Go build — it just
// silently empties the panel.
func TestServeTopologyWireKeys(t *testing.T) {
	path := writeFixture(t, "compose.yaml", topologyFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/topology","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/topology returned an error: %+v", resp.Error)
	}

	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, k := range []string{"profiles", "nodes", "edges", "cycles", "dangling", "max_layer"} {
		if _, ok := payload[k]; !ok {
			t.Errorf("payload has no %q key", k)
		}
	}

	nodes, ok := payload["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		t.Fatalf("nodes = %v", payload["nodes"])
	}
	node := nodes[0].(map[string]any)
	for _, k := range []string{"id", "kind", "name", "origin", "declared", "external", "profiles", "layer"} {
		if _, ok := node[k]; !ok {
			t.Errorf("node has no %q key: %v", k, node)
		}
	}
	origin := node["origin"].(map[string]any)
	for _, k := range []string{"file", "line", "column", "step"} {
		if _, ok := origin[k]; !ok {
			t.Errorf("origin has no %q key", k)
		}
	}

	edges, ok := payload["edges"].([]any)
	if !ok || len(edges) == 0 {
		t.Fatalf("edges = %v", payload["edges"])
	}
	for _, raw := range edges {
		e := raw.(map[string]any)
		for _, k := range []string{"kind", "from", "to", "origin"} {
			if _, ok := e[k]; !ok {
				t.Errorf("edge has no %q key: %v", k, e)
			}
		}
		if e["kind"] == "depends_on" {
			d, ok := e["depends_on"].(map[string]any)
			if !ok || d["condition"] == "" {
				t.Errorf("depends_on edge carries no condition: %v", e)
			}
		}
	}
}

// Every node id must parse back into the path it came from. AD-14: one
// canonical string form for display and one parser back, or the join key does
// not survive the wire.
func TestServeTopologyIDsRoundTrip(t *testing.T) {
	path := writeFixture(t, "compose.yaml", topologyFixture)

	project, err := resolve.File(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := topology.Build(project, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes() {
		if got := resolve.ParsePath(n.Path.String()); !got.Equal(n.Path) {
			t.Errorf("id %q parsed back to %v, want %v", n.Path.String(), got, n.Path)
		}
	}
}

func TestServeTopologyProfilesChangeTheGraph(t *testing.T) {
	path := writeFixture(t, "compose.yaml", topologyFixture)

	s := start(t)
	s.handshake(1)

	count := func(id int, params string) int {
		s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"stack/topology","params":%s}`, id, params))
		resp := s.read()
		if resp.Error != nil {
			t.Fatalf("stack/topology returned an error: %+v", resp.Error)
		}
		var payload struct {
			Nodes []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"nodes"`
			Dangling []struct {
				Ref string `json:"ref"`
			} `json:"dangling"`
		}
		if err := json.Unmarshal(resp.Result, &payload); err != nil {
			t.Fatal(err)
		}
		var services int
		for _, n := range payload.Nodes {
			if n.Kind == "service" {
				services++
			}
			if n.ID == "services.tools" && params == fmt.Sprintf(`{"path":%q}`, path) {
				t.Error("a profiled service leaked into the default graph")
			}
		}
		return services
	}

	bare := count(2, fmt.Sprintf(`{"path":%q}`, path))
	withDebug := count(3, fmt.Sprintf(`{"path":%q,"profiles":["debug"]}`, path))
	if bare != 3 {
		t.Errorf("default graph has %d services, want 3", bare)
	}
	if withDebug != 4 {
		t.Errorf("debug graph has %d services, want 4", withDebug)
	}
}

func TestServeTopologyInvalidParams(t *testing.T) {
	for _, tc := range []struct{ name, params string }{
		{"missing path", `{}`},
		{"empty path", `{"path":"  "}`},
		{"params not an object", `"compose.yaml"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t)
			s.handshake(1)
			s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/topology","params":%s}`, tc.params))
			resp := s.read()
			if resp.Error == nil {
				t.Fatalf("expected an error, got result %s", resp.Result)
			}
			if resp.Error.Code != codeInvalidParams {
				t.Errorf("code = %d, want %d", resp.Error.Code, codeInvalidParams)
			}
		})
	}
}

func TestServeTopologyUnresolvableFileCarriesPosition(t *testing.T) {
	path := writeFixture(t, "compose.yaml", "services:\n  web:\n    ports: [\"8080:80\"\n")

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/topology","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error == nil {
		t.Fatal("a malformed file produced no error")
	}
	if resp.Error.Code != codeResolveFailed {
		t.Errorf("code = %d, want %d", resp.Error.Code, codeResolveFailed)
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("error data = %s: %v", resp.Error.Data, err)
	}
	if data["file"] != path {
		t.Errorf("data.file = %v, want %q", data["file"], path)
	}
	if _, ok := data["line"]; !ok {
		t.Error("a parse failure reached the client with no position")
	}
}

// profileList is what turns `-profile a,b -profile c` into an active set. A
// comma read as part of a name filters the whole stack away silently.
func TestProfileListFlag(t *testing.T) {
	var p profileList
	for _, arg := range []string{"dev,prod", " staging ", "", "a,,b"} {
		if err := p.Set(arg); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.String(); got != "dev,prod,staging,a,b" {
		t.Errorf("profileList = %q", got)
	}
}
