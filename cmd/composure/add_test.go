package main

// CLI before UI — stories 7.3 and 7.4 at the headless door.
//
// `composure add` previews by default and writes with -write, the same one-boolean
// shape `preview` and `apply` have, and it is the SAME planner the extension
// reaches over `stack/add`. A capability that only works from the webview is
// built wrong (requirements §9, AD-9).
//
// Every assertion is on the bytes the file holds afterwards, never on the exit
// code alone.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const addFixture = `# a stack
services:
  web:
    image: nginx:1.25   # pinned deliberately
    ports:
      - "8080:80"

  db:
    image: 'postgres:16'

networks:
  frontend:
`

func addFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(addFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readCompose(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCLIAddServiceWritesOneBlockAndNothingElse(t *testing.T) {
	dir := addFixtureDir(t)

	res := runCLI(t, dir, "add", "-json", "-write", "-kind", "service",
		"-name", "cache", "-value", "redis:7", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("add exited %d\nstderr: %s", res.code, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
	}
	if doc["written"] != true {
		t.Errorf("add reported written=%v", doc["written"])
	}
	// ONE edit: two operations, one diff.
	ops, _ := doc["ops"].([]any)
	if len(ops) != 2 {
		t.Errorf("the request carried %d operations, want 2", len(ops))
	}
	if doc["added"] != float64(2) || doc["removed"] != float64(0) {
		t.Errorf("diff is +%v -%v, want +2 -0", doc["added"], doc["removed"])
	}
	// The bytes: the block lands after the LAST service, before `networks:`,
	// and every other byte of the fixture is where it was.
	want := strings.Replace(addFixture,
		"    image: 'postgres:16'\n",
		"    image: 'postgres:16'\n  cache:\n    image: redis:7\n", 1)
	if got := readCompose(t, dir); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

func TestCLIAddPreviewsWithoutWriting(t *testing.T) {
	dir := addFixtureDir(t)

	res := runCLI(t, dir, "add", "-kind", "service", "-name", "cache", "-value", "redis:7", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("add exited %d\nstderr: %s", res.code, res.stderr)
	}
	if got := readCompose(t, dir); got != addFixture {
		t.Fatalf("a preview wrote to the file:\n got: %q\nwant: %q", got, addFixture)
	}
	if !strings.Contains(res.stdout, "+  cache:") || !strings.Contains(res.stdout, "+    image: redis:7") {
		t.Errorf("the preview does not show the block it would add:\n%s", res.stdout)
	}
	if strings.Contains(res.stdout, "WROTE") {
		t.Errorf("a preview says it wrote:\n%s", res.stdout)
	}
}

func TestCLIDeclareEachResourceKind(t *testing.T) {
	for _, tc := range []struct{ kind, block string }{
		{"network", "networks"},
		{"volume", "volumes"},
		{"config", "configs"},
		{"secret", "secrets"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dir := addFixtureDir(t)
			res := runCLI(t, dir, "add", "-write", "-kind", tc.kind, "-name", "backend", "compose.yaml")
			if res.code != 0 {
				t.Fatalf("add exited %d\nstderr: %s", res.code, res.stderr)
			}
			got := readCompose(t, dir)
			// `networks:` exists in the fixture and the other three do not, so
			// this run covers both the one-operation and the two-operation
			// shapes without a second fixture.
			if !strings.Contains(got, "\n"+tc.block+":") {
				t.Fatalf("no %s block:\n%s", tc.block, got)
			}
			if !strings.Contains(got, "  backend:\n") {
				t.Fatalf("the entry did not land:\n%s", got)
			}
			if strings.Contains(got, "backend: ") || strings.Contains(got, " \n") {
				t.Errorf("a byte the reader did not ask for was written: %q", got)
			}
			if !strings.HasPrefix(got, addFixture[:strings.Index(addFixture, "networks:")]) {
				t.Errorf("everything above the insert moved:\n%s", got)
			}
		})
	}
}

// Rule 6 from a shell: a refusal exits 3, names what could not be done, carries
// a stable slug, and writes nothing.
func TestCLIAddRefusalsExitThreeAndWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		what, reason string
		args         []string
	}{
		{"a duplicate name", "duplicate-name", []string{"-kind", "service", "-name", "db", "-value", "redis:7"}},
		{"a value YAML would round", "needs-quoting", []string{"-kind", "service", "-name", "cache", "-value", "3.10"}},
		{"a service with no image", "no-image", []string{"-kind", "service", "-name", "cache"}},
		{"no name at all", "no-name", []string{"-kind", "service", "-name", "", "-value", "redis:7"}},
		{"a duplicate network", "duplicate-name", []string{"-kind", "network", "-name", "frontend"}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			dir := addFixtureDir(t)
			args := append([]string{"add", "-json", "-write"}, tc.args...)
			res := runCLI(t, dir, append(args, "compose.yaml")...)
			if res.code != 3 {
				t.Fatalf("%s exited %d, want 3\nstdout: %s\nstderr: %s", tc.what, res.code, res.stdout, res.stderr)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
			}
			if doc["reason"] != tc.reason {
				t.Errorf("reason is %v, want %q", doc["reason"], tc.reason)
			}
			if doc["refused"] != true {
				t.Errorf("refused is %v; the reader would be told the tool broke", doc["refused"])
			}
			if doc["written"] != false {
				t.Errorf("written is %v", doc["written"])
			}
			if got := readCompose(t, dir); got != addFixture {
				t.Errorf("a refusal wrote to the file:\n%s", got)
			}
		})
	}
}

// The flow-style refusal reached through the new surface rather than routed
// around it.
func TestCLIAddIntoAFlowServicesMappingIsRefused(t *testing.T) {
	dir := t.TempDir()
	const src = "services: {web: {image: nginx}}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, dir, "add", "-json", "-write", "-kind", "service",
		"-name", "cache", "-value", "redis:7", "compose.yaml")
	if res.code != 3 {
		t.Fatalf("exited %d, want 3\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if doc["reason"] != "flow-style" {
		t.Errorf("reason is %v, want flow-style", doc["reason"])
	}
	if got := readCompose(t, dir); got != src {
		t.Errorf("a refusal wrote to the file: %q", got)
	}
}

// The empty-stack case end to end, from a shell: a stack with nothing in it is
// a starting point.
func TestCLIAddServiceToAnEmptyStack(t *testing.T) {
	dir := t.TempDir()
	const src = "name: demo\nservices:\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, dir, "add", "-write", "-kind", "service", "-name", "web", "-value", "nginx", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("add exited %d\nstderr: %s", res.code, res.stderr)
	}
	const want = "name: demo\nservices:\n  web:\n    image: nginx\n"
	if got := readCompose(t, dir); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

// A name declared in ANOTHER file of the chain: the CLI resolves the project it
// was pointed at, so the collision is caught where last-one-wins happens.
func TestCLIAddCatchesADuplicateFromTheOverrideFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"),
		[]byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.override.yaml"),
		[]byte("services:\n  cache:\n    image: redis:7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No positional path: the directory gets the Compose candidate order plus
	// its override file, which is what a reader in a project directory has.
	// Naming a file explicitly disables the override pickup, as it does for
	// every other subcommand.
	res := runCLI(t, dir, "add", "-write", "-kind", "service", "-name", "cache", "-value", "redis:8")
	if res.code != 3 {
		t.Fatalf("exited %d, want 3\nstderr: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "compose.override.yaml:2") {
		t.Errorf("the refusal does not name the file and line that declares it:\n%s", res.stderr)
	}
	if got := readCompose(t, dir); got != "services:\n  web:\n    image: nginx\n" {
		t.Errorf("a refusal wrote to the file: %q", got)
	}
}

// The usage text is the only documentation a headless user has.
func TestCLIUsageDocumentsAdd(t *testing.T) {
	res := runCLI(t, t.TempDir(), "help")
	text := res.stdout + res.stderr
	for _, want := range []string{"composure add", "-kind", "service", "network", "volume", "config", "secret"} {
		if !strings.Contains(text, want) {
			t.Errorf("the usage text never mentions %q", want)
		}
	}
}
