package main

// `stack/add` over the wire — the second door onto stories 7.3 and 7.4.
//
// The method PLANS and returns operations; it never writes and never previews.
// That shape is deliberate: the extension holds staged edits and writes them
// with one press, so a declaration has to arrive as operations it can hold
// alongside the reader's other work and send as ONE `stack/apply`. A method
// that wrote would be a second write path and a second undo.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/edit"
)

type addResult struct {
	Ops []edit.Op `json:"ops"`
}

func TestServeAddPlansAServiceAndWritesNothing(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := start(t)
	s.handshake(1)

	res := decodeResult[addResult](t, s.call(2, "stack/add", map[string]any{
		"file":  path,
		"kind":  "service",
		"name":  "cache",
		"value": "redis:7",
	}))
	if len(res.Ops) != 2 {
		t.Fatalf("got %d operations, want 2: %+v", len(res.Ops), res.Ops)
	}
	if res.Ops[0].At != "services" || res.Ops[0].Key != "cache" || res.Ops[0].Value != "" {
		t.Errorf("operation 0 is %+v", res.Ops[0])
	}
	if res.Ops[1].At != "services.cache" || res.Ops[1].Key != "image" || res.Ops[1].Value != "redis:7" {
		t.Errorf("operation 1 is %+v", res.Ops[1])
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("stack/add wrote to the file")
	}

	// And those operations, handed straight back over stack/apply, are the
	// edit — one request, one diff.
	//
	// This is the CLI's path, NOT the extension's: nothing here records an
	// `expect`, so the staleness comparison never runs. The extension's path is
	// TestServeAddStagedTheWayTheExtensionStagesIt below, and the gap between
	// the two is exactly where the "path segment not found" bug lived.
	ops := make([]any, 0, len(res.Ops))
	for _, op := range res.Ops {
		ops = append(ops, map[string]any{
			"operation": string(op.Operation),
			"at":        op.At,
			"key":       op.Key,
			"value":     op.Value,
		})
	}
	applied := decodeResult[edit.Result](t, s.call(3, "stack/apply", map[string]any{"file": path, "ops": ops}))
	if applied.Added != 2 || applied.Removed != 0 {
		t.Errorf("diff is +%d -%d, want +2 -0", applied.Added, applied.Removed)
	}
	want := string(before) + "  cache:\n    image: redis:7\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

// The owner's report, over the wire, in the order the panel makes the calls.
//
// `stack/add` plans, `stack/preview` reports where each operation lands,
// `panel.ts stageAll` writes those ranges back onto the operations as `expect`,
// `refreshPending` previews the staged set again, and `Save` applies it. Four
// calls, and the bug was in the last two: the staleness check located every
// operation against the file on disk, where `services.PolicyServer` does not
// exist because operation 0 is what creates it.
//
// Before the fix this failed at the refresh with
// `edit: operation 1: path segment "PolicyServer" not found` — the reader saw it
// the instant they pressed Enter in the composer, with nothing staged and no
// way forward.
func TestServeAddStagedTheWayTheExtensionStagesIt(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := start(t)
	s.handshake(1)

	// 1. The composer: the reader types a name and an image.
	planned := decodeResult[addResult](t, s.call(2, "stack/add", map[string]any{
		"path":  path,
		"file":  path,
		"kind":  "service",
		"name":  "PolicyServer",
		"value": "nginx",
	}))
	if len(planned.Ops) != 2 {
		t.Fatalf("got %d operations, want 2: %+v", len(planned.Ops), planned.Ops)
	}

	wire := func(ops []edit.Op) []any {
		out := make([]any, 0, len(ops))
		for _, op := range ops {
			entry := map[string]any{
				"operation": string(op.Operation),
				"at":        op.At,
				"key":       op.Key,
				"value":     op.Value,
			}
			if op.Expect != nil {
				entry["expect"] = map[string]any{
					"start": op.Expect.Start,
					"end":   op.Expect.End,
					"text":  op.Expect.Text,
				}
			}
			out = append(out, entry)
		}
		return out
	}

	// 2. stageAll's preview, which is what produces the ranges.
	staging := decodeResult[edit.Result](t, s.call(3, "stack/preview", map[string]any{
		"file": path,
		"ops":  wire(planned.Ops),
	}))
	if len(staging.Ops) != len(planned.Ops) {
		t.Fatalf("preview reported %d operations for %d", len(staging.Ops), len(planned.Ops))
	}

	// 3. stageAll's record: AD-19's expect, per operation, exactly as panel.ts
	// writes it back — `{start, end, text}` from that operation's own result.
	staged := make([]edit.Op, len(planned.Ops))
	for i, op := range planned.Ops {
		staged[i] = op
		staged[i].Expect = &edit.Expect{
			Start: staging.Ops[i].Range.Start,
			End:   staging.Ops[i].Range.End,
			Text:  staging.Ops[i].Before,
		}
	}

	// 4. refreshPending: the same set, previewed again, now carrying expects.
	// This is the call the reader's failure came out of.
	if resp := s.call(4, "stack/preview", map[string]any{"file": path, "ops": wire(staged)}); resp.Error != nil {
		t.Fatalf("refreshing the pending diff refused a staged add: %+v", resp.Error)
	}

	// 5. Save.
	applied := decodeResult[edit.Result](t, s.call(5, "stack/apply", map[string]any{
		"file": path,
		"ops":  wire(staged),
	}))
	if applied.Added != 2 || applied.Removed != 0 {
		t.Errorf("diff is +%d -%d, want +2 -0", applied.Added, applied.Removed)
	}
	want := string(before) + "  PolicyServer:\n    image: nginx\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

func TestServeAddPlansEveryResourceKind(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)

	id := 2
	for _, kind := range []string{"network", "volume", "config", "secret"} {
		res := decodeResult[addResult](t, s.call(id, "stack/add", map[string]any{
			"file": path,
			"kind": kind,
			"name": "thing",
		}))
		id++
		if len(res.Ops) != 2 {
			t.Fatalf("%s: got %d operations, want the block and the entry: %+v", kind, len(res.Ops), res.Ops)
		}
		if res.Ops[0].At != "" || res.Ops[0].Key != kind+"s" {
			t.Errorf("%s: the block operation is %+v", kind, res.Ops[0])
		}
		if res.Ops[1].At != kind+"s" || res.Ops[1].Key != "thing" || res.Ops[1].Value != "" {
			t.Errorf("%s: the entry operation is %+v", kind, res.Ops[1])
		}
	}
}

// Rule 6 over the wire: a refusal is codeEditRefused with a stable slug, never
// codeEditFailed. Story 6.5 is the standing evidence — a refusal reported as a
// failure tells the reader the tool broke when the tool declined.
func TestServeAddRefusalsAreRefusalsNotFaults(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)

	cases := []struct {
		what, reason string
		params       map[string]any
	}{
		{"a duplicate name", "duplicate-name", map[string]any{"kind": "service", "name": "web", "value": "redis:7"}},
		{"a rounded float", "needs-quoting", map[string]any{"kind": "service", "name": "cache", "value": "3.10"}},
		{"a boolean", "needs-quoting", map[string]any{"kind": "service", "name": "cache", "value": "yes"}},
		{"no image", "no-image", map[string]any{"kind": "service", "name": "cache"}},
		{"no name", "no-name", map[string]any{"kind": "service", "name": "", "value": "redis:7"}},
	}
	for i, tc := range cases {
		params := map[string]any{"file": path}
		for k, v := range tc.params {
			params[k] = v
		}
		resp := s.call(10+i, "stack/add", params)
		if resp.Error == nil {
			t.Fatalf("%s: succeeded", tc.what)
		}
		if resp.Error.Code != codeEditRefused {
			t.Errorf("%s: code %d, want %d (refused)", tc.what, resp.Error.Code, codeEditRefused)
		}
		var data editFailureData
		if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
			t.Fatalf("%s: error data: %v", tc.what, err)
		}
		if data.Reason != tc.reason {
			t.Errorf("%s: reason %q, want %q", tc.what, data.Reason, tc.reason)
		}
		if data.Written {
			t.Errorf("%s: reports written", tc.what)
		}
	}
}

// The merged duplicate check over the wire: `path` names the project, `file`
// names what is written. A name declared in the override is refused, naming it.
func TestServeAddCatchesADuplicateInAnotherFile(t *testing.T) {
	base := writeFixture(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")
	dir := strings.TrimSuffix(base, "compose.yaml")
	if err := os.WriteFile(dir+"compose.override.yaml",
		[]byte("services:\n  cache:\n    image: redis:7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/add", map[string]any{
		"path":  dir,
		"file":  base,
		"kind":  "service",
		"name":  "cache",
		"value": "redis:8",
	})
	if resp.Error == nil {
		t.Fatal("a name the override declares was accepted")
	}
	if !strings.Contains(resp.Error.Message, "compose.override.yaml:2") {
		t.Errorf("the refusal does not name the file and line: %s", resp.Error.Message)
	}
}

// An empty stack does not resolve — `validate` requires services — and that
// must not stop a service being added to it. EXPERIENCE.md's starting point.
func TestServeAddWorksOnAStackThatDoesNotResolve(t *testing.T) {
	path := writeFixture(t, "compose.yaml", "name: demo\nservices:\n")
	s := start(t)
	s.handshake(1)

	res := decodeResult[addResult](t, s.call(2, "stack/add", map[string]any{
		"path":  path,
		"file":  path,
		"kind":  "service",
		"name":  "web",
		"value": "nginx",
	}))
	if len(res.Ops) != 2 {
		t.Fatalf("got %d operations, want 2: %+v", len(res.Ops), res.Ops)
	}
}

func TestServeAddNeedsAFileAndAKind(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)

	if resp := s.call(2, "stack/add", map[string]any{"kind": "service", "name": "x", "value": "y"}); resp.Error == nil {
		t.Error("a request with no file succeeded")
	}
	resp := s.call(3, "stack/add", map[string]any{"file": path, "kind": "deployment", "name": "x"})
	if resp.Error == nil {
		t.Fatal("an unknown kind succeeded")
	}
	if resp.Error.Code == codeEditRefused {
		t.Error("an unknown kind is a client bug, not a refusal the reader can act on")
	}
}
