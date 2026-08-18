package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/hub"
)

// The other door. `image/lookup` and `image/search` must be the SAME two
// functions the CLI calls — the way `stack/schema` and `composure schema` are one
// `schema.Inspect` — or the pane and the terminal grow two answers about one
// image.
//
// Nothing here opens a socket either: `newHubStub` points the client at
// loopback, and the disabled case asserts the handler counter is zero.

func TestServeImageLookupIsTheCLIsAnswer(t *testing.T) {
	newHubStub(t, fixtureTags(), 0)

	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{"ref":"postgres:16-alpine"}}`)
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("image/lookup returned an error: %+v", resp.Error)
	}

	// Decoded as a MAP, so a struct tag cannot supply the name it is meant to
	// be checked against. These are the keys the TypeScript reads.
	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reference", "repository", "display", "tag", "state", "message", "pill", "age", "candidate"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("the answer has no %q", key)
		}
	}
	if payload["state"] != string(hub.StateOK) {
		t.Fatalf("state = %v", payload["state"])
	}
	if payload["pill"] != "postgres:17-alpine · major · 40MB smaller" {
		t.Errorf("pill = %v", payload["pill"])
	}
	cand, ok := payload["candidate"].(map[string]any)
	if !ok {
		t.Fatalf("candidate is %T", payload["candidate"])
	}
	for _, key := range []string{"reference", "tag", "kind"} {
		if _, ok := cand[key]; !ok {
			t.Errorf("the candidate has no %q", key)
		}
	}
}

func TestServeImageLookupNeedsARef(t *testing.T) {
	newHubStub(t, fixtureTags(), 0)
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{}}`)
	resp := s.read()
	if resp.Error == nil {
		t.Fatal("a lookup with no reference was accepted")
	}
	if resp.Error.Code != codeInvalidParams {
		t.Errorf("code = %d, want invalid params", resp.Error.Code)
	}
}

// A network outcome is a RESULT, never a JSON-RPC error. The pane branches on
// the state and renders a sentence; an error would put a failure banner over a
// panel whose every other fact is still true.
func TestServeImageLookupOfflineIsAResultNotAnError(t *testing.T) {
	s := newHubStub(t, fixtureTags(), 0)
	t.Setenv("COMPOSURE_OFFLINE", "1")

	srv := start(t)
	srv.handshake(1)
	srv.send(`{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{"ref":"postgres:16-alpine"}}`)
	resp := srv.read()
	if resp.Error != nil {
		t.Fatalf("a switched-off lookup was reported as a protocol error: %+v", resp.Error)
	}
	var r hub.Report
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.State != hub.StateDisabled {
		t.Fatalf("state = %q", r.State)
	}
	if r.Message == "" {
		t.Fatal("no sentence for the reader")
	}
	if s.calls != 0 {
		t.Fatalf("a switched-off session made %d request(s)", s.calls)
	}
}

func TestServeImageSearch(t *testing.T) {
	newHubStub(t, nil, 2)
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/search","params":{"query":"postgres","limit":5}}`)
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("image/search returned an error: %+v", resp.Error)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"query", "state", "results"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("the answer has no %q", key)
		}
	}
	list, ok := payload["results"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("results = %v", payload["results"])
	}
	first, _ := list[0].(map[string]any)
	// R6.1's fields, by their WIRE names.
	for _, key := range []string{"name", "description", "stars", "pulls_display", "badge", "architectures"} {
		if _, ok := first[key]; !ok {
			t.Errorf("a result has no %q", key)
		}
	}
}

// An empty query is a client mistake and must not become a Docker Hub request.
func TestServeImageSearchRefusesAnEmptyQuery(t *testing.T) {
	stub := newHubStub(t, nil, 2)
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"image/search","params":{"query":"   "}}`)
	resp := s.read()
	if resp.Error == nil {
		t.Fatal("an empty query was accepted")
	}
	if stub.calls != 0 {
		t.Fatalf("an empty query reached Docker Hub %d time(s)", stub.calls)
	}
}

// The handshake is where a version skew has to be caught. A client with a search
// box talking to a core with no method fails at the gesture — in front of the
// reader — instead of at the handshake, which is the reason the five bumps
// before this one happened.
func TestProtocolRevisionCoversTheImageMethods(t *testing.T) {
	// The image methods ARRIVED at revision 6 and every revision after it still
	// carries them, so this is a floor rather than an equality. An equality
	// would have to be edited by every later bump, which turns a check about
	// the image methods into a line somebody updates without reading.
	if protocolRevision < 6 {
		t.Fatalf("protocolRevision = %d; image/lookup and image/search arrived at revision 6",
			protocolRevision)
	}
	for _, method := range []string{"image/lookup", "image/search"} {
		if !strings.Contains(usageText, method) {
			t.Errorf("usageText does not document %q", method)
		}
	}
}

// R6.7 at the RPC boundary as well. Three surfaces, one prohibition.
func TestServeImageAnswersPromiseNoVulnerabilityFacet(t *testing.T) {
	newHubStub(t, fixtureTags(), 2)
	s := start(t)
	s.handshake(1)
	for id, req := range map[int]string{
		2: `{"jsonrpc":"2.0","id":2,"method":"image/lookup","params":{"ref":"postgres:16-alpine"}}`,
		3: `{"jsonrpc":"2.0","id":3,"method":"image/search","params":{"query":"postgres"}}`,
	} {
		s.send(req)
		resp := s.read()
		if resp.Error != nil {
			t.Fatalf("request %d: %+v", id, resp.Error)
		}
		body := strings.ToLower(string(resp.Result))
		for _, word := range []string{"cve", "vulnerab", "security", "scout"} {
			if strings.Contains(body, word) {
				t.Errorf("request %d mentions %q", id, word)
			}
		}
	}
	_ = fmt.Sprint()
}
