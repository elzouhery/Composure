package main

// CLI before UI — stories 7.6 and 7.7 at the headless door.
//
// The rule is an architecture invariant, not a preference: the corpus harness
// can only exercise headless code, and a capability that only works from the
// webview is built wrong (requirements §9, AD-9). So both new operations are
// driven here, through the real binary and over the real wire, BEFORE any
// TypeScript binds to them — and the assertion is the bytes on disk, because
// this engine returns confident wrong answers rather than crashing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/edit"
)

// Two stages, so an insert into stage 0 cannot be mistaken for an append to the
// file. The trailing comment pins which side of a comment a new line lands on.
const insertFixture = `FROM golang:1.24 AS builder
WORKDIR /src
RUN go build ./...

# the runtime image
FROM alpine:3.20
COPY --from=builder /src/app /app
`

func dockerfileFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(insertFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readDockerfile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCLIInsertInstructionIntoAStage(t *testing.T) {
	dir := dockerfileFixtureDir(t)

	res := runCLI(t, dir, "apply", "-json", "-op", "insert_instruction",
		"-stage", "0", "-value", "USER app", "Dockerfile")
	if res.code != 0 {
		t.Fatalf("apply exited %d\nstderr: %s", res.code, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
	}
	if doc["written"] != true {
		t.Errorf("apply reported written=%v", doc["written"])
	}
	want := strings.Replace(insertFixture, "RUN go build ./...\n", "RUN go build ./...\nUSER app\n", 1)
	if got := readDockerfile(t, dir); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if doc["added"] != float64(1) || doc["removed"] != float64(0) {
		t.Errorf("diff is +%v -%v, want +1 -0", doc["added"], doc["removed"])
	}
}

func TestCLIPreviewInstructionWritesNothing(t *testing.T) {
	dir := dockerfileFixtureDir(t)

	res := runCLI(t, dir, "preview", "-json", "-op", "insert_instruction",
		"-stage", "1", "-value", "CMD [\"/app\"]", "Dockerfile")
	if res.code != 0 {
		t.Fatalf("preview exited %d\nstderr: %s", res.code, res.stderr)
	}
	if got := readDockerfile(t, dir); got != insertFixture {
		t.Fatalf("preview changed the file:\n%q", got)
	}
	if !strings.Contains(res.stdout, "+CMD") {
		t.Errorf("the preview diff does not show the added line:\n%s", res.stdout)
	}
}

func TestCLIInsertStage(t *testing.T) {
	dir := dockerfileFixtureDir(t)

	res := runCLI(t, dir, "apply", "-json", "-op", "insert_stage",
		"-value", "nginx:1.27", "-key", "serve", "Dockerfile")
	if res.code != 0 {
		t.Fatalf("apply exited %d\nstderr: %s", res.code, res.stderr)
	}
	if got, want := readDockerfile(t, dir), insertFixture+"FROM nginx:1.27 AS serve\n"; got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

// A refusal exits 3, carries the stable slug, and writes nothing. A CI job can
// tell "cannot be done" from "went wrong" without reading a sentence.
func TestCLIDockerfileInsertRefusalsExitThree(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		slug string
	}{
		{
			name: "a stage name another stage already uses",
			args: []string{"-op", "insert_stage", "-value", "alpine:3.20", "-key", "builder"},
			slug: "stage-name",
		},
		{
			name: "instruction text that would become a continuation",
			args: []string{"-op", "insert_instruction", "-stage", "0", "-value", `RUN echo hi \`},
			slug: "insert-text",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := dockerfileFixtureDir(t)
			res := runCLI(t, dir, append(append([]string{"apply", "-json"}, tc.args...), "Dockerfile")...)
			if res.code != 3 {
				t.Fatalf("exit %d, want 3 (a refusal)\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
			}
			if doc["reason"] != tc.slug {
				t.Errorf("reason is %v, want %q", doc["reason"], tc.slug)
			}
			if doc["refused"] != true {
				t.Errorf("refused is %v; a refusal reported as a fault tells the reader the tool broke", doc["refused"])
			}
			if got := readDockerfile(t, dir); got != insertFixture {
				t.Errorf("a refusal wrote to the file")
			}
		})
	}
}

// The `-op` help has to name every operation the closed set holds. An operation
// nobody can discover from `composure -h` is an operation the CLI does not really
// have (N5).
func TestUsageNamesEveryOperation(t *testing.T) {
	res := runCLI(t, t.TempDir(), "-h")
	help := res.stdout + res.stderr
	// The `-op` flag's OWN description, not merely a mention somewhere in the
	// prose: `composure preview -h` prints the flag list and nothing else, so an
	// operation missing from this line is one nobody can discover from the
	// command they are running.
	var flagLine string
	for _, line := range strings.Split(runCLI(t, t.TempDir(), "preview", "-h").stderr, "\n") {
		if strings.Contains(line, "replace_scalar") {
			flagLine = line
		}
	}
	if flagLine == "" {
		t.Error("`composure preview -h` does not describe -op at all")
	}
	for _, op := range []edit.Operation{
		edit.OpReplaceScalar,
		edit.OpInsertKey,
		edit.OpInsertSequenceEntry,
		edit.OpDeleteKey,
		edit.OpSetBaseImage,
		edit.OpReplaceArgs,
		edit.OpInsertInstruction,
		edit.OpInsertStage,
	} {
		if !strings.Contains(help, string(op)) {
			t.Errorf("`composure -h` never mentions -op %s", op)
		}
		if flagLine != "" && !strings.Contains(flagLine, string(op)) {
			t.Errorf("the -op flag description does not offer %s:\n%s", op, flagLine)
		}
	}
}

/* -------------------------------------------------------------------------
 * The second door: the RPC the extension speaks.
 * ---------------------------------------------------------------------- */

func TestServeInsertsAnInstructionAndAStage(t *testing.T) {
	path := writeFixture(t, "Dockerfile", insertFixture)
	s := start(t)
	s.handshake(1)

	// One request, two operations: the stage, then an instruction INTO the
	// stage that did not exist when the request was built.
	res := decodeResult[edit.Result](t, s.call(2, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{
			{"operation": "insert_stage", "value": "nginx:1.27", "key": "serve"},
			{"operation": "insert_instruction", "stage": 2, "value": "CMD [\"nginx\"]"},
		},
	}))
	if !res.Written {
		t.Error("stack/apply reported no write")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := insertFixture + "FROM nginx:1.27 AS serve\nCMD [\"nginx\"]\n"
	if string(got) != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 2 || res.Removed != 0 {
		t.Errorf("diff is +%d -%d, want +2 -0", res.Added, res.Removed)
	}
}

// The refusal reaches the client as codeEditRefused with its slug, not as a
// generic failure — the whole of story 6.5's obligation, at the wire.
func TestServeDockerfileInsertRefusalCarriesItsSlug(t *testing.T) {
	path := writeFixture(t, "Dockerfile", insertFixture)
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{
			{"operation": "insert_instruction", "stage": 0, "value": "RUN one\nRUN two"},
		},
	})
	if resp.Error == nil {
		t.Fatal("an instruction carrying a newline was accepted")
	}
	if resp.Error.Code != codeEditRefused {
		t.Errorf("code %d, want codeEditRefused (%d)", resp.Error.Code, codeEditRefused)
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("error data is not an object: %v", err)
	}
	if data["reason"] != "insert-text" {
		t.Errorf("reason is %v, want %q", data["reason"], "insert-text")
	}
	if data["written"] != false {
		t.Errorf("written is %v", data["written"])
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != insertFixture {
		t.Error("a refused edit wrote to the file")
	}
}
