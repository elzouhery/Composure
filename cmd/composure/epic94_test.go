package main

// Stories 9.4 and 9.6 at the two headless doors.
//
// CLI before UI, always: the corpus harness can only exercise headless code,
// and nothing in the editor may write these files by a route these two doors
// cannot. Every assertion below is on the BYTES on disk, never on the absence
// of an error.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ------------------------------------------------------ 9.4, the CLI ---

func TestCLIMovesAFromTagIntoABuildArgument(t *testing.T) {
	src := epic9Fixture(t, "e51-two-stage-arg.Dockerfile")
	dir := epic9Dir(t, map[string]string{"Dockerfile": src})

	// Instruction 16 is the SECOND FROM. The declaration must still land above
	// the FIRST one, which a single-stage fixture could not tell apart.
	res := runCLI(t, dir, "extract", "-write", "-instruction", "16", "-part", "tag",
		"-name", "NODE_VERSION", "Dockerfile")
	if res.code != 0 {
		t.Fatalf("exited %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	want := strings.Replace(src,
		"# The build stage pulls the toolchain in.\nFROM golang:1.24-alpine AS build",
		"ARG NODE_VERSION=18\n# The build stage pulls the toolchain in.\nFROM golang:1.24-alpine AS build", 1)
	want = strings.Replace(want, "FROM node:18 AS runtime", "FROM node:${NODE_VERSION} AS runtime", 1)
	if got := fileAt(t, dir, "Dockerfile"); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
	// One file. A `.env` here would be inert — compose feeds build arguments
	// only through build.args — and writing one is the confident wrong answer
	// this story exists to refuse.
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("a .env was written beside a Dockerfile")
	}
	if !strings.Contains(res.stdout, "build.args") {
		t.Errorf("the reader is not told that build.args is not wired:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "global") {
		t.Errorf("the reader is not told which scope it landed in:\n%s", res.stdout)
	}
}

func TestCLIRefusesAValueThatCannotBeABareArgDefault(t *testing.T) {
	src := epic9Fixture(t, "e51-two-stage-arg.Dockerfile")
	dir := epic9Dir(t, map[string]string{"Dockerfile": src})

	// Instruction 19 is `ENV APP_GREETING="hello world"`.
	res := runCLI(t, dir, "extract", "-json", "-write", "-instruction", "19", "Dockerfile")
	if res.code != 3 {
		t.Fatalf("exited %d, want 3 (refused)\n%s%s", res.code, res.stdout, res.stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out["reason"] != "arg-value" || out["refused"] != true {
		t.Errorf("reason=%v refused=%v", out["reason"], out["refused"])
	}
	if got := fileAt(t, dir, "Dockerfile"); got != src {
		t.Error("a refused move touched the file")
	}
}

// ------------------------------------------------------ 9.4, the wire ---

func TestServeExtractArgPreviewsAndApplies(t *testing.T) {
	src := epic9Fixture(t, "e51-two-stage-arg.Dockerfile")
	path := writeFixture(t, "Dockerfile", src)
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/extract-arg", map[string]any{
		"file": path, "instruction": 16, "part": "tag", "name": "NODE_VERSION",
	})
	if resp.Error != nil {
		t.Fatalf("stack/extract-arg failed: %+v", resp.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatal(err)
	}
	if out["written"] != false || out["scope"] != "global" || out["arg_line"] != "ARG NODE_VERSION=18" {
		t.Errorf("written=%v scope=%v arg_line=%v", out["written"], out["scope"], out["arg_line"])
	}
	if got := fileAt(t, path); got != src {
		t.Error("the preview wrote the file")
	}

	if resp := s.call(3, "stack/extract-arg-apply", map[string]any{
		"file": path, "instruction": 16, "part": "tag", "name": "NODE_VERSION",
	}); resp.Error != nil {
		t.Fatalf("stack/extract-arg-apply failed: %+v", resp.Error)
	}
	if got := fileAt(t, path); !strings.Contains(got, "ARG NODE_VERSION=18\n# The build stage") {
		t.Errorf("the apply did not land the declaration above the first FROM:\n%s", got)
	}
}

func TestServeExtractArgRefusesByCodeAndSlug(t *testing.T) {
	path := writeFixture(t, "Dockerfile", epic9Fixture(t, "e52-global-arg.Dockerfile"))
	s := start(t)
	s.handshake(1)

	// Instruction 20 is `ENV NODE_VERSION=20` in stage 1; the global ARG says
	// 18. Neither answer is writable, so it is a refusal rather than a fault.
	resp := s.call(2, "stack/extract-arg-apply", map[string]any{"file": path, "instruction": 20})
	if resp.Error == nil {
		t.Fatal("a conflicting declaration was accepted")
	}
	if resp.Error.Code != -32002 {
		t.Errorf("code is %d, want -32002 (edit refused)", resp.Error.Code)
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["reason"] != "arg-conflict" {
		t.Errorf("reason is %v, want arg-conflict", data["reason"])
	}
}

// ------------------------------------------------------ 9.6, the wire ---

// The branch that could not fire. `errors.Is(err, edit.ErrStaleRange)` on
// stack/extract-apply was inert until this story: ApplyExtract had no code path
// that returned it. This is the test that says it fires.
func TestServeExtractApplyRefusesAStaleComposeRange(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	path := writeFixture(t, "compose.yaml", src)
	dir := filepath.Dir(path)
	s := start(t)
	s.handshake(1)

	params := map[string]any{"file": path, "at": "services.db.environment.POSTGRES_PASSWORD"}
	resp := s.call(2, "stack/extract", params)
	if resp.Error != nil {
		t.Fatalf("stack/extract failed: %+v", resp.Error)
	}
	var preview struct {
		Compose struct {
			Ops []struct {
				Range  struct{ Start, End int }
				Before string
			}
		}
		EnvExpect *struct {
			Defined bool
			Value   string
		} `json:"env_expect"`
	}
	if err := json.Unmarshal(resp.Result, &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Compose.Ops) == 0 {
		t.Fatal("the preview reported no operation to assert against")
	}
	if preview.EnvExpect == nil || preview.EnvExpect.Defined {
		t.Fatalf("env_expect is %+v; the name is not in any .env here", preview.EnvExpect)
	}
	op := preview.Compose.Ops[0]

	// ONLY the compose file moves. If both moved, a check that looked at just
	// one of them would still pass and this test would say nothing.
	moved := "# somebody else got here first\n" + src
	if err := os.WriteFile(path, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}

	params["expect"] = map[string]any{"start": op.Range.Start, "end": op.Range.End, "text": op.Before}
	params["expect_env"] = map[string]any{"defined": false}
	stale := s.call(3, "stack/extract-apply", params)
	if stale.Error == nil {
		t.Fatal("a stale two-file write was accepted")
	}
	if stale.Error.Code != -32003 {
		t.Errorf("code is %d, want -32003 (stale range)", stale.Error.Code)
	}
	var data map[string]any
	if err := json.Unmarshal(stale.Error.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["reason"] != "stale-range" {
		t.Errorf("reason is %v, want stale-range", data["reason"])
	}
	if got := fileAt(t, path); got != moved {
		t.Error("the compose file was written over by a refused apply")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("a .env was created by a refused apply")
	}
}

// The unversioned caller, over the wire: no expectation fields at all is the
// behaviour that shipped, and it still is.
func TestServeExtractApplyWithNoExpectationStillWrites(t *testing.T) {
	path := writeFixture(t, "compose.yaml", epic9Fixture(t, "e45-plaintext-credential.yml"))
	s := start(t)
	s.handshake(1)

	if resp := s.call(2, "stack/extract-apply", map[string]any{
		"file": path, "at": "services.db.environment.POSTGRES_PASSWORD",
	}); resp.Error != nil {
		t.Fatalf("a request with no expectation was refused: %+v", resp.Error)
	}
	if got := fileAt(t, filepath.Dir(path), ".env"); got != "POSTGRES_PASSWORD=hunter2\n" {
		t.Errorf(".env is %q", got)
	}
}

// The revision is asserted rather than assumed. Story 9.6 changed a shipped
// wire — `expect` and `expect_env` on the request, `env_expect` on the response
// — and the handshake is the only place a client finds out that the core it is
// talking to cannot protect the one write path with a blast radius larger than
// the file the reader is looking at.
func TestTheProtocolRevisionCarriesTheStalenessContract(t *testing.T) {
	if protocolRevision < 9 {
		t.Fatalf("protocolRevision = %d; the two-file staleness contract arrived at revision 9",
			protocolRevision)
	}
}
