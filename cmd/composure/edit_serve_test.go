package main

// The write path over the wire — Epic 6's RPC surface.
//
// These drive the real server over real pipes, like the rest of serve_test.go.
// What they assert is the pair of claims the extension's Save button rests on:
// `stack/preview` writes nothing, and `stack/apply` writes exactly what the
// preview showed. The refusal codes are asserted too, because the client
// branches on them: a refusal reverts a field, a stale range DISCARDS a stage,
// and confusing the two either loses work or writes over someone else's.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/edit"
)

// call sends one request and returns the response.
func (s *session) call(id int, method string, params any) rawResponse {
	s.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		s.t.Fatal(err)
	}
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, raw))
	return s.read()
}

func decodeResult[T any](t *testing.T, resp rawResponse) T {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("request failed: %+v", resp.Error)
	}
	var out T
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

const editFixture = `# a stack
services:
  web:
    image: nginx:1.27   # pinned
    ports:
      - "8080:80"
`

func TestServePreviewReturnsTheDiffAndWritesNothing(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)

	res := decodeResult[edit.Result](t, s.call(2, "stack/preview", map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "replace_scalar",
			"at":        "services.web.image",
			"value":     "nginx:1.28",
		}},
	}))
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("preview diff is %d removed / %d added, want 1 and 1:\n%s", res.Removed, res.Added, res.Diff)
	}
	if res.Written {
		t.Error("preview reported a write")
	}
	if !strings.Contains(res.Diff, "--- a/compose.yaml") {
		t.Errorf("the diff does not name the file:\n%s", res.Diff)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != editFixture {
		t.Error("stack/preview changed the file")
	}
}

func TestServeApplyWritesExactlyWhatThePreviewShowed(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)
	params := map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "replace_scalar",
			"at":        "services.web.image",
			"value":     "nginx:1.28",
		}},
	}

	preview := decodeResult[edit.Result](t, s.call(2, "stack/preview", params))
	applied := decodeResult[edit.Result](t, s.call(3, "stack/apply", params))

	if preview.Diff != applied.Diff {
		t.Errorf("the diff moved between preview and apply:\n%s\n---\n%s", preview.Diff, applied.Diff)
	}
	if !applied.Written {
		t.Error("apply did not report a write")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "image: nginx:1.28   # pinned") {
		t.Errorf("the trailing comment did not survive:\n%s", got)
	}
	if !strings.Contains(string(got), `- "8080:80"`) {
		t.Error("an untouched line moved")
	}
}

// AD-19 over the wire. The client must be able to tell a stale range from every
// other failure, because its response is to DISCARD the stage rather than
// retry it.
func TestServeApplyRefusesAStaleRange(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)

	preview := decodeResult[edit.Result](t, s.call(2, "stack/preview", map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "replace_scalar", "at": "services.web.image", "value": "nginx:1.28",
		}},
	}))
	rng := preview.Ops[0].Range

	moved := "# someone else edited this\n" + editFixture
	if err := os.WriteFile(path, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := s.call(3, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "replace_scalar", "at": "services.web.image", "value": "nginx:1.28",
			"expect": map[string]any{"start": rng.Start, "end": rng.End, "text": preview.Ops[0].Before},
		}},
	})
	if resp.Error == nil {
		t.Fatal("a stale range was applied")
	}
	if resp.Error.Code != codeStaleRange {
		t.Errorf("code = %d, want codeStaleRange (%d)", resp.Error.Code, codeStaleRange)
	}
	var data editFailureData
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("decode error data: %v", err)
	}
	if data.Reason != "stale-range" || data.Written {
		t.Errorf("error data = %+v", data)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != moved {
		t.Error("a refused apply wrote to the file")
	}
}

// AD-8 over the wire. A flow-style collection is its own code, so the client
// reverts the field and says what could not be done rather than showing a fault.
func TestServeApplyRefusesAFlowStyleInsert(t *testing.T) {
	const flow = "services:\n  web: {image: nginx}\n"
	path := writeFixture(t, "compose.yaml", flow)
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "insert_key", "at": "services.web", "key": "restart", "value": "always",
		}},
	})
	if resp.Error == nil {
		t.Fatal("a block insert into a flow mapping was accepted")
	}
	if resp.Error.Code != codeEditRefused {
		t.Errorf("code = %d, want codeEditRefused (%d)", resp.Error.Code, codeEditRefused)
	}
	var data editFailureData
	_ = json.Unmarshal(resp.Error.Data, &data)
	if data.Reason != "flow-style" {
		t.Errorf("reason = %q, want flow-style", data.Reason)
	}
	got, _ := os.ReadFile(path)
	if string(got) != flow {
		t.Error("a refused insert wrote to the file")
	}
}

func TestServeEditRejectsAMalformedRequest(t *testing.T) {
	s := start(t)
	s.handshake(1)
	for _, params := range []map[string]any{
		{"ops": []map[string]any{{"operation": "replace_scalar"}}}, // no file
		{"file": "/nowhere/compose.yaml"},                          // no ops
	} {
		resp := s.call(2, "stack/preview", params)
		if resp.Error == nil || resp.Error.Code != codeInvalidParams {
			t.Errorf("params %v: got %+v, want invalid params", params, resp.Error)
		}
	}
}

// ------------------------------------------------------------ dockerfile ---

const dockerfileFixture = "FROM golang:1.22-alpine AS build\n" +
	"WORKDIR /src\n" +
	"RUN go build ./...\n" +
	"\n" +
	"FROM alpine:3.19\n" +
	"COPY --from=build /src/app /app\n"

func TestServeDockerfileReturnsTheStageForm(t *testing.T) {
	path := writeFixture(t, "Dockerfile", dockerfileFixture)
	s := start(t)
	s.handshake(1)

	var form struct {
		Path    string `json:"path"`
		Missing bool   `json:"missing"`
		Stages  []struct {
			Index    int    `json:"index"`
			Label    string `json:"label"`
			ImageRef string `json:"image_ref"`
		} `json:"stages"`
	}
	resp := s.call(2, "stack/dockerfile", map[string]any{"path": path})
	if resp.Error != nil {
		t.Fatalf("stack/dockerfile failed: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &form); err != nil {
		t.Fatal(err)
	}
	if form.Missing || len(form.Stages) != 2 {
		t.Fatalf("form = %+v", form)
	}
	if form.Stages[0].Label != "build" || form.Stages[1].ImageRef != "alpine:3.19" {
		t.Errorf("stages = %+v", form.Stages)
	}
}

// Story 6.3: the Dockerfile is reached from the service that builds it, and it
// resolves relative to the build context.
func TestServeDockerfileResolvesThroughABuildSection(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte(
		"services:\n  api:\n    build:\n      context: ./api\n      dockerfile: Dockerfile.dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "api", "Dockerfile.dev")
	if err := os.WriteFile(target, []byte(dockerfileFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	s := start(t)
	s.handshake(1)
	var form struct {
		Path    string `json:"path"`
		Missing bool   `json:"missing"`
		Stages  []struct {
			Label string `json:"label"`
		} `json:"stages"`
	}
	resp := s.call(2, "stack/dockerfile", map[string]any{"path": compose, "at": "services.api.build"})
	if resp.Error != nil {
		t.Fatalf("stack/dockerfile failed: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &form); err != nil {
		t.Fatal(err)
	}
	if form.Path != target {
		t.Errorf("resolved to %q, want %q", form.Path, target)
	}
	if form.Missing || len(form.Stages) != 2 {
		t.Errorf("form = %+v", form)
	}
}

// A build naming a Dockerfile that is not there is a RESULT, not an error. The
// node renders as missing; it does not vanish and it does not raise a banner.
func TestServeDockerfileReportsAMissingFileAsAResult(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte(
		"services:\n  api:\n    build:\n      context: ./api\n      dockerfile: Dockerfile.dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := start(t)
	s.handshake(1)
	var form struct {
		Path       string `json:"path"`
		Missing    bool   `json:"missing"`
		Dockerfile string `json:"dockerfile"`
	}
	resp := s.call(2, "stack/dockerfile", map[string]any{"path": compose, "at": "services.api.build"})
	if resp.Error != nil {
		t.Fatalf("a missing Dockerfile was reported as an error: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &form); err != nil {
		t.Fatal(err)
	}
	if !form.Missing {
		t.Error("the form does not say the file is missing")
	}
	if !strings.HasSuffix(form.Path, filepath.Join("api", "Dockerfile.dev")) {
		t.Errorf("the form does not name the file that is not there: %q", form.Path)
	}
}

// Story 6.2's write path over the wire, with R7.2's preservation asserted on
// the bytes rather than on the absence of an error.
func TestServeApplySetBaseImage(t *testing.T) {
	src := "FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build  # pinned\nRUN go build ./...\n"
	path := writeFixture(t, "Dockerfile", src)
	s := start(t)
	s.handshake(1)

	res := decodeResult[edit.Result](t, s.call(2, "stack/apply", map[string]any{
		"file": path,
		"ops":  []map[string]any{{"operation": "set_base_image", "stage": 0, "value": "golang:1.24-alpine"}},
	}))
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("diff is %d removed / %d added, want 1 and 1:\n%s", res.Removed, res.Added, res.Diff)
	}
	got, _ := os.ReadFile(path)
	want := "FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build  # pinned\n"
	if !strings.Contains(string(got), want) {
		t.Errorf("the FROM line was not rebuilt as written:\n%s", got)
	}
}

// The round trip: apply an edit, re-resolve, and assert the model reflects it
// and nothing else moved.
//
// The byte-level tests above prove the file is right. This proves the MODEL is
// right, which is a different claim: a splice could land the correct bytes and
// still shift a provenance line, re-order a mapping, or drop an override
// history, and the panel would then be drawing something the file does not say.
// Comparing the whole resolved JSON before and after — with only the edited
// value textually swapped back — is the strongest form of "nothing else moved"
// available at this layer.
func TestApplyRoundTripsThroughTheModel(t *testing.T) {
	path := writeFixture(t, "compose.yaml", editFixture)
	s := start(t)
	s.handshake(1)

	before := decodeResult[json.RawMessage](t, s.call(2, "stack/resolve", map[string]any{"path": path}))

	decodeResult[edit.Result](t, s.call(3, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "replace_scalar", "at": "services.web.image", "value": "nginx:1.28",
		}},
	}))

	after := decodeResult[json.RawMessage](t, s.call(4, "stack/resolve", map[string]any{"path": path}))

	if strings.Contains(string(after), `nginx:1.27`) {
		t.Error("the re-resolved model still holds the old value")
	}
	if !strings.Contains(string(after), `nginx:1.28`) {
		t.Error("the re-resolved model does not hold the new value")
	}
	// Swap the new value back and the two models must be byte-identical: every
	// origin, every line number, every key order, every override history.
	normalised := strings.ReplaceAll(string(after), "nginx:1.28", "nginx:1.27")
	if normalised != string(before) {
		t.Errorf("the model moved beyond the edited value.\nbefore: %s\nafter:  %s", before, normalised)
	}
}
