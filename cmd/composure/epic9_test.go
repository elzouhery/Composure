package main

// Epic 9 at the headless door. CLI before UI, always: none of these three
// capabilities has a webview yet, and every one of them is exercised, refused
// and asserted here first.
//
// Assertions are on the BYTES the files hold afterwards, never on the exit code
// alone — the same rule story 7.3's CLI tests were written under.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func epic9Dir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func epic9Fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileAt(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ------------------------------------------------------- 9.1, comments ---

func TestCLIWritesAndRemovesAComment(t *testing.T) {
	src := epic9Fixture(t, "e44-comments-everywhere.yml")
	dir := epic9Dir(t, map[string]string{"compose.yaml": src})

	if res := runCLI(t, dir, "apply", "-op", "set_comment", "-at", "services.db",
		"-where", "above", "-value", "the database", "compose.yaml"); res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}
	want := strings.Replace(src, "  db:\n", "  # the database\n  db:\n", 1)
	if got := fileAt(t, dir, "compose.yaml"); got != want {
		t.Fatalf("bytes differ.\n got: %q\nwant: %q", got, want)
	}

	if res := runCLI(t, dir, "apply", "-op", "delete_comment", "-at", "services.db",
		"-where", "above", "compose.yaml"); res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}
	if got := fileAt(t, dir, "compose.yaml"); got != src {
		t.Errorf("the round trip did not return the file to its own bytes.\n got: %q", got)
	}
}

// Exit 3 is "the engine declined", and a CI job tells it from "went wrong"
// without reading stderr. A comment refusal has to reach that exit code.
func TestCLIACommentRefusalExitsThree(t *testing.T) {
	src := "services:\n  web:\n    command: |\n      echo hi\n"
	dir := epic9Dir(t, map[string]string{"compose.yaml": src})
	res := runCLI(t, dir, "apply", "-json", "-op", "set_comment", "-at", "services.web.command",
		"-where", "trailing", "-value", "nope", "compose.yaml")
	if res.code != 3 {
		t.Fatalf("exited %d, want 3\n%s%s", res.code, res.stdout, res.stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
	}
	if env["reason"] != "comment-target" || env["refused"] != true {
		t.Errorf("reason=%v refused=%v", env["reason"], env["refused"])
	}
	if got := fileAt(t, dir, "compose.yaml"); got != src {
		t.Error("a refusal wrote to the file")
	}
}

// ----------------------------------------------------- 9.2, list entries ---

func TestCLIEditsOneEntryOfAList(t *testing.T) {
	src := epic9Fixture(t, "e43-repeated-list-entries.yml")
	dir := epic9Dir(t, map[string]string{"compose.yaml": src})

	res := runCLI(t, dir, "apply", "-json", "-op", "replace_scalar",
		"-at", "services.web.healthcheck.test[1]", "-value", "curl", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["changed_lines"] != float64(2) {
		t.Errorf("changed_lines is %v, want 2", doc["changed_lines"])
	}
	want := strings.Replace(src, `"CMD", "wget"`, `"CMD", "curl"`, 1)
	if got := fileAt(t, dir, "compose.yaml"); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestCLIAnEntryTheListDoesNotHaveExitsThree(t *testing.T) {
	src := epic9Fixture(t, "e43-repeated-list-entries.yml")
	dir := epic9Dir(t, map[string]string{"compose.yaml": src})
	res := runCLI(t, dir, "apply", "-json", "-op", "replace_scalar",
		"-at", "services.web.ports[9]", "-value", "x", "compose.yaml")
	if res.code != 3 {
		t.Fatalf("exited %d, want 3\n%s%s", res.code, res.stdout, res.stderr)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env["reason"] != "entry-index" {
		t.Errorf("reason=%v", env["reason"])
	}
	if got := fileAt(t, dir, "compose.yaml"); got != src {
		t.Error("a refusal wrote to the file")
	}
}

// `composure editable` is the read side, and it has to say the same thing.
func TestCLIEditableSaysWhyAnEntryIsNotThere(t *testing.T) {
	dir := epic9Dir(t, map[string]string{"compose.yaml": epic9Fixture(t, "e43-repeated-list-entries.yml")})
	res := runCLI(t, dir, "editable", "-json", "-at", "services.web.ports[9]", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}
	var a map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &a); err != nil {
		t.Fatal(err)
	}
	if a["reason"] != "entry-index" {
		t.Errorf("reason=%v", a["reason"])
	}
	if _, has := a["plan"]; has {
		t.Errorf("a plan was offered for an entry that cannot be added: %v", a["plan"])
	}
}

// -------------------------------------------------------- 9.3, extract ---

func TestCLIExtractPreviewsBothFilesAndWritesNeither(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	dir := epic9Dir(t, map[string]string{"compose.yaml": src})

	res := runCLI(t, dir, "extract", "-at", "services.db.environment.POSTGRES_PASSWORD", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}
	for _, want := range []string{"${POSTGRES_PASSWORD}", "POSTGRES_PASSWORD=hunter2", ".env"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("the preview does not mention %q:\n%s", want, res.stdout)
		}
	}
	if got := fileAt(t, dir, "compose.yaml"); got != src {
		t.Error("the preview wrote the compose file")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("the preview created the .env")
	}
}

func TestCLIExtractWritesBothFiles(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	env := epic9Fixture(t, "e46-existing.env")
	dir := epic9Dir(t, map[string]string{"compose.yaml": src, ".env": env})

	res := runCLI(t, dir, "extract", "-json", "-write",
		"-at", "services.db.environment.POSTGRES_PASSWORD", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
	}
	if doc["written"] != true || doc["name"] != "POSTGRES_PASSWORD" || doc["value"] != "hunter2" {
		t.Errorf("written=%v name=%v value=%v", doc["written"], doc["name"], doc["value"])
	}
	// The compose file: byte-identical apart from the one value.
	want := strings.Replace(src, "POSTGRES_PASSWORD: hunter2", "POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}", 1)
	if got := fileAt(t, dir, "compose.yaml"); got != want {
		t.Errorf("compose bytes differ.\n got: %q\nwant: %q", got, want)
	}
	// The .env: byte-identical plus one line.
	if got := fileAt(t, dir, ".env"); got != env+"POSTGRES_PASSWORD=hunter2\n" {
		t.Errorf(".env bytes differ.\n got: %q", got)
	}
}

func TestCLIExtractRefusalExitsThree(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	env := epic9Fixture(t, "e46-existing.env")
	for _, c := range []struct {
		args []string
		slug string
	}{
		{[]string{"-at", "services.db.environment.ALREADY"}, "already-interpolated"},
		{[]string{"-at", "services.db.environment.POSTGRES_PASSWORD", "-name", "9lives"}, "var-name"},
		{[]string{"-at", "services.db.environment.POSTGRES_PASSWORD", "-name", "COMPOSE_PROJECT_NAME"}, "var-conflict"},
	} {
		dir := epic9Dir(t, map[string]string{"compose.yaml": src, ".env": env})
		args := append([]string{"extract", "-json", "-write"}, c.args...)
		res := runCLI(t, dir, append(args, "compose.yaml")...)
		if res.code != 3 {
			t.Errorf("%v: exited %d, want 3\n%s%s", c.args, res.code, res.stdout, res.stderr)
			continue
		}
		var envDoc map[string]any
		if err := json.Unmarshal([]byte(res.stdout), &envDoc); err != nil {
			t.Errorf("%v: stdout is not JSON: %v", c.args, err)
			continue
		}
		if envDoc["reason"] != c.slug {
			t.Errorf("%v: reason=%v, want %q", c.args, envDoc["reason"], c.slug)
		}
		if got := fileAt(t, dir, "compose.yaml"); got != src {
			t.Errorf("%v: the compose file was written", c.args)
		}
		if got := fileAt(t, dir, ".env"); got != env {
			t.Errorf("%v: the .env was written", c.args)
		}
	}
}

// The HEADLINE does not print the secret.
//
// It read `MOVED hunter2 into ${POSTGRES_PASSWORD}` — the value, bare, with no
// context, on the one line most likely to be pasted into a chat window or
// scrolled past in a CI log. The credential rule goes to lengths never to print
// a value and this undid it for the command that exists to FIX a credential.
//
// The diffs below it still show the value, and that is deliberate rather than
// an oversight: a diff is what the reader approves, and DECISIONS.md 25 requires
// both halves of a two-file write to be shown. What is removed is the line that
// carried the secret and nothing else.
func TestCLIExtractHeadlineDoesNotPrintTheValue(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	dir := epic9Dir(t, map[string]string{"compose.yaml": src})
	res := runCLI(t, dir, "extract", "-at", "services.db.environment.POSTGRES_PASSWORD", "compose.yaml")
	if res.code != 0 {
		t.Fatalf("exited %d\n%s", res.code, res.stderr)
	}

	var head string
	for _, line := range strings.Split(res.stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			head = line
			break
		}
	}
	if head == "" {
		t.Fatalf("no output at all:\n%s", res.stdout)
	}
	if strings.Contains(head, "hunter2") {
		t.Errorf("the headline prints the secret: %q", head)
	}
	// And it still says what happened, to what, and under which name — the
	// headline is not simply deleted.
	for _, want := range []string{"services.db.environment.POSTGRES_PASSWORD", "${POSTGRES_PASSWORD}"} {
		if !strings.Contains(head, want) {
			t.Errorf("the headline %q does not mention %q", head, want)
		}
	}
}

// A Dockerfile no longer gets the "and it is not built" sentence — story 9.4
// built it. The subcommand dispatches on the FILE's grammar, so `-at` on a
// Dockerfile is a USAGE error (2) naming the flag that grammar takes, and NOT
// a refusal (3): the operation exists, the request just addressed it the way
// the other grammar does.
func TestCLIExtractOnADockerfileAsksForAnInstruction(t *testing.T) {
	dir := epic9Dir(t, map[string]string{"Dockerfile": "FROM alpine\nENV TOKEN=t0ken\n"})
	res := runCLI(t, dir, "extract", "-write", "-at", "x", "Dockerfile")
	if res.code != 2 {
		t.Fatalf("exited %d, want 2\n%s%s", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "-instruction") {
		t.Errorf("stderr does not name the flag this grammar takes:\n%s", res.stderr)
	}
	// And nothing was written by a request that never ran.
	if got := fileAt(t, dir, "Dockerfile"); got != "FROM alpine\nENV TOKEN=t0ken\n" {
		t.Errorf("the Dockerfile was touched: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("a .env was created beside a Dockerfile")
	}
}

// The `-at` flag is required, and its absence is a USAGE error (2), not a
// refusal (3) and not a fault (1).
func TestCLIExtractNeedsAt(t *testing.T) {
	dir := epic9Dir(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: nginx\n"})
	if res := runCLI(t, dir, "extract", "compose.yaml"); res.code != 2 {
		t.Errorf("exited %d, want 2\n%s", res.code, res.stderr)
	}
}

// ------------------------------------------------ Epic 9 over the wire ---
//
// Every capability answers over BOTH doors, from one function, the way
// `stack/schema` and `composure schema` do. These drive the real server over real
// pipes and assert the bytes on disk, not the response alone.

func TestServeSetsACommentThroughTheOrdinaryOps(t *testing.T) {
	// `where` rides on edit.Op, so the comment operations arrive over the
	// existing stack/preview and stack/apply with no new method and no
	// protocol revision. This is the check that says so.
	path := writeFixture(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")
	s := start(t)
	s.handshake(1)

	if resp := s.call(2, "stack/apply", map[string]any{
		"file": path,
		"ops": []map[string]any{{
			"operation": "set_comment", "at": "services.web.image",
			"where": "trailing", "value": "pinned",
		}},
	}); resp.Error != nil {
		t.Fatalf("stack/apply failed: %+v", resp.Error)
	}
	if got := fileAt(t, path); got != "services:\n  web:\n    image: nginx # pinned\n" {
		t.Errorf("got %q", got)
	}
}

func TestServeExtractPreviewsBothFilesAndWritesNeither(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	path := writeFixture(t, "compose.yaml", src)
	s := start(t)
	s.handshake(1)

	resp := s.call(2, "stack/extract", map[string]any{
		"file": path, "at": "services.db.environment.POSTGRES_PASSWORD",
	})
	if resp.Error != nil {
		t.Fatalf("stack/extract failed: %+v", resp.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatal(err)
	}
	if out["written"] != false || out["name"] != "POSTGRES_PASSWORD" {
		t.Errorf("written=%v name=%v", out["written"], out["name"])
	}
	if out["env_diff"] == nil || !strings.Contains(out["env_diff"].(string), "POSTGRES_PASSWORD=hunter2") {
		t.Errorf("no .env diff in the response: %v", out["env_diff"])
	}
	if got := fileAt(t, path); got != src {
		t.Error("stack/extract wrote the compose file")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".env")); err == nil {
		t.Error("stack/extract created the .env")
	}
}

func TestServeExtractApplyWritesBothAndRefusesByCode(t *testing.T) {
	src := epic9Fixture(t, "e45-plaintext-credential.yml")
	path := writeFixture(t, "compose.yaml", src)
	dir := filepath.Dir(path)
	s := start(t)
	s.handshake(1)

	if resp := s.call(2, "stack/extract-apply", map[string]any{
		"file": path, "at": "services.db.environment.POSTGRES_PASSWORD",
	}); resp.Error != nil {
		t.Fatalf("stack/extract-apply failed: %+v", resp.Error)
	}
	if got := fileAt(t, dir, ".env"); got != "POSTGRES_PASSWORD=hunter2\n" {
		t.Errorf(".env is %q", got)
	}

	// And a refusal comes back as a refusal, with its slug, so the client
	// reverts the field rather than reporting that the tool broke.
	resp := s.call(3, "stack/extract-apply", map[string]any{
		"file": path, "at": "services.db.environment.ALREADY",
	})
	if resp.Error == nil {
		t.Fatal("an already-interpolated value was accepted")
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("error data: %v", err)
	}
	if data["reason"] != "already-interpolated" {
		t.Errorf("reason=%v", data["reason"])
	}
}
