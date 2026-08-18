package edit

// Story 9.3 — the first operation in this product that writes two files.
//
// The traps this file exists to spring, named because twenty-one checks that
// could not fail have shipped here:
//
//	A test that asserts the compose file "now contains ${POSTGRES_PASSWORD}"
//	is not a test of this operation. The operation's claim is that the compose
//	file is BYTE-IDENTICAL apart from that one value, and only a whole-buffer
//	comparison says so.
//
//	A test that asserts "the .env now has the variable" says nothing about the
//	comment, the blank line and the inline comment that were in it already.
//
//	A test of the failure path that only checks for an error says nothing about
//	whether half of it landed.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

func extractDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readAt(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ------------------------------------------------------- the happy paths ---

func TestMovingAValueIntoANewEnvFile(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")

	res, err := ApplyExtract(Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "POSTGRES_PASSWORD" {
		t.Errorf("name derived as %q", res.Name)
	}
	if !res.EnvCreated {
		t.Error("the .env was not reported as created")
	}

	// The compose file: byte-identical apart from the one value. This is the
	// criterion, not a Contains check.
	want := strings.Replace(src, "POSTGRES_PASSWORD: hunter2", "POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}", 1)
	if got := readAt(t, compose); got != want {
		t.Errorf("compose bytes differ.\n got: %q\nwant: %q", got, want)
	}
	if got := readAt(t, filepath.Join(dir, ".env")); got != "POSTGRES_PASSWORD=hunter2\n" {
		t.Errorf(".env is %q", got)
	}
}

// The list form. Replacing the whole `NAME=value` scalar with `${VAR}` leaves
// `- ${VAR}`, which Compose reads as pass-through-by-name — the variable's own
// name has to be kept. Same reasoning as the credential rule's, reused.
func TestMovingAListFormEnvironmentEntry(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")

	res, err := ApplyExtract(Extract{File: compose, At: "services.api.environment[0]"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "API_TOKEN" || res.Value != "t0ken" {
		t.Fatalf("name=%q value=%q", res.Name, res.Value)
	}
	want := strings.Replace(src, "- API_TOKEN=t0ken", "- API_TOKEN=${API_TOKEN}", 1)
	if got := readAt(t, compose); got != want {
		t.Errorf("compose bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

// An existing .env keeps every byte it had. The comment, the blank line and
// the inline comment on a value are the ones that matter.
func TestAddingToAnExistingEnvFile(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})

	if _, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err != nil {
		t.Fatal(err)
	}
	got := readAt(t, filepath.Join(dir, ".env"))
	if want := env + "POSTGRES_PASSWORD=hunter2\n"; got != want {
		t.Errorf(".env bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

// Idempotence. The same name with the same value is not a conflict: the compose
// half is written and the .env is left BYTE-IDENTICAL. That is also what makes
// the residual window between the two renames converge on a re-run.
func TestTheSameVariableWithTheSameValueIsNotAConflict(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})

	res, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.api.environment[0]", // API_TOKEN=t0ken, already in the .env
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.EnvUnchanged {
		t.Error("the .env was rewritten for a variable it already defines identically")
	}
	if got := readAt(t, filepath.Join(dir, ".env")); got != env {
		t.Errorf(".env changed.\n got: %q\nwant: %q", got, env)
	}
	if !strings.Contains(readAt(t, filepath.Join(dir, "compose.yaml")), "- API_TOKEN=${API_TOKEN}") {
		t.Error("the compose half was not written")
	}
}

// A value the .env parser would not read back as itself is quoted so that it
// is. The check is the READBACK, through the one reader of that format in the
// codebase — not a character blocklist.
func TestValuesAreQuotedSoTheyReadBackExactly(t *testing.T) {
	for _, value := range []string{
		"plain",
		"has spaces",
		" leading space",
		"trailing space ",
		"a value # with a hash",
		`quotes "inside" it`,
		"single 'inside' it",
		"1.1.1.1#5054",
		"",
	} {
		src := "services:\n  db:\n    environment:\n      SECRET: \"" + strings.ReplaceAll(value, `"`, `\"`) + "\"\n"
		dir := extractDir(t, map[string]string{"compose.yaml": src})
		res, err := ApplyExtract(Extract{
			File: filepath.Join(dir, "compose.yaml"),
			At:   "services.db.environment.SECRET",
			Name: "SECRET",
		})
		if err != nil {
			if value == "" {
				continue // an empty literal has nothing to move; refused below
			}
			t.Errorf("%q: %v", value, err)
			continue
		}
		back := resolve.ParseDotEnv([]byte(readAt(t, filepath.Join(dir, ".env"))))
		if back["SECRET"] != res.Value {
			t.Errorf("%q was written as a line that reads back as %q", res.Value, back["SECRET"])
		}
	}
}

// Step 1's OUTER readback, made falsifiable — and it is a different property
// from the one above, which is why deleting it passed the whole suite.
//
// `renderEnvValue` reads its candidate back on its OWN LINE, in isolation. That
// is enough for every well-formed `.env`, and every fixture in this file is
// well-formed, so the outer whole-file check in `runExtract` had no test that
// could see it go.
//
// The interaction it is the only thing that catches: a `.env` already holding
// an UNTERMINATED quote, plus a value carrying a `"`. The new line's quote
// closes the old one, `ParseDotEnv` reads the earlier name as everything up to
// it, and the name the compose file now references is never defined at all —
// it resolves to the empty string. Measured with the check removed: both files
// written, `${POSTGRES_PASSWORD}` defined nowhere.
func TestAWholeFileReadbackFailureRefusesAndWritesNeitherFile(t *testing.T) {
	src := edgeFixture(t, "e49-quote-in-value.yml")
	env := edgeFixture(t, "e49-unterminated-quote.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})
	compose := filepath.Join(dir, "compose.yaml")

	// The premise, asserted rather than assumed: the per-LINE readback that
	// `renderEnvValue` performs is happy with this value. If this ever stops
	// being true the test below still passes while testing nothing, which is
	// the shape this whole file exists to avoid.
	if _, err := EnvLine("POSTGRES_PASSWORD", `pa"ss`); err != nil {
		t.Fatalf("the per-line readback already refuses this value, so the whole-file "+
			"check is not what is under test any more: %v", err)
	}

	_, err := ApplyExtract(Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"})
	if !errors.Is(err, ErrVarValue) {
		t.Fatalf("error is %v, want ErrVarValue", err)
	}
	if !Refused(err) {
		t.Error("not classified as a refusal")
	}
	if got := readAt(t, compose); got != src {
		t.Errorf("the compose file was written.\n got: %q\nwant: %q", got, src)
	}
	if got := readAt(t, filepath.Join(dir, ".env")); got != env {
		t.Errorf("the .env was written.\n got: %q\nwant: %q", got, env)
	}
}

// And the reason it is a whole-FILE property, stated as a test of its own: the
// same value into a well-formed `.env` is written without complaint. Without
// this, the test above could be passing because the value is bad rather than
// because the file it is going into is.
func TestTheSameValueIsFineInAWellFormedEnvFile(t *testing.T) {
	src := edgeFixture(t, "e49-quote-in-value.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": "A=closed\nB=2\n"})
	if _, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err != nil {
		t.Fatalf("refused a value that reads back perfectly well here: %v", err)
	}
	back := resolve.ParseDotEnv([]byte(readAt(t, filepath.Join(dir, ".env"))))
	if back["POSTGRES_PASSWORD"] != `pa"ss` {
		t.Errorf("reads back as %q", back["POSTGRES_PASSWORD"])
	}
}

// ---------------------------------------------------------- the refusals ---

func TestExtractRefusals(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")

	cases := []struct {
		name string
		ex   Extract
		is   error
		slug string
	}{
		{"already interpolated", Extract{At: "services.db.environment.ALREADY"}, ErrAlreadyInterpolated, "already-interpolated"},
		{"bad variable name", Extract{At: "services.db.environment.POSTGRES_PASSWORD", Name: "9lives"}, ErrVarName, "var-name"},
		{"name with a dash", Extract{At: "services.db.environment.POSTGRES_PASSWORD", Name: "a-b"}, ErrVarName, "var-name"},
		{"conflicting value", Extract{At: "services.db.environment.POSTGRES_PASSWORD", Name: "COMPOSE_PROJECT_NAME"}, ErrVarConflict, "var-conflict"},
		{"not a scalar", Extract{At: "services.db.environment"}, ErrNoLiteral, "no-literal"},
		{"no such path", Extract{At: "services.db.environment.NOPE"}, ErrNoLiteral, "no-literal"},
	}
	for _, c := range cases {
		dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})
		c.ex.File = filepath.Join(dir, "compose.yaml")
		_, err := ApplyExtract(c.ex)
		if !errors.Is(err, c.is) {
			t.Errorf("%s: error is %v, want %v", c.name, err, c.is)
			continue
		}
		if !Refused(err) {
			t.Errorf("%s: not classified as a refusal", c.name)
		}
		if got := Reason(err); got != c.slug {
			t.Errorf("%s: Reason is %q, want %q", c.name, got, c.slug)
		}
		if got := readAt(t, c.ex.File); got != src {
			t.Errorf("%s: the compose file was written", c.name)
		}
		if got := readAt(t, filepath.Join(dir, ".env")); got != env {
			t.Errorf("%s: the .env was written", c.name)
		}
	}
}

// The conflict sentence has to carry both values and the line, because the
// reader's next move is to decide which one is right.
func TestTheConflictRefusalNamesBothValues(t *testing.T) {
	dir := extractDir(t, map[string]string{
		"compose.yaml": edgeFixture(t, "e45-plaintext-credential.yml"),
		".env":         edgeFixture(t, "e46-existing.env"),
	})
	_, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
		Name: "COMPOSE_PROJECT_NAME",
	})
	for _, want := range []string{"demo", "hunter2", ".env"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A Dockerfile is refused by NAME, with the reason — the COMPOSE operation still
// refuses one, and now it names the operation that does own it. The equivalent
// is ARG, which is build-time, stage-scoped and unreachable from a .env, and it
// is a separate operation (ApplyExtractArg) rather than a mode of this one.
func TestADockerfileIsRefusedWithTheReason(t *testing.T) {
	dir := extractDir(t, map[string]string{"Dockerfile": "FROM alpine\nENV TOKEN=t0ken\n"})
	path := filepath.Join(dir, "Dockerfile")
	_, err := ApplyExtract(Extract{File: path, At: "x"})
	if !errors.Is(err, ErrWrongGrammar) {
		t.Fatalf("error is %v, want ErrWrongGrammar", err)
	}
	if !strings.Contains(err.Error(), "ARG") || !strings.Contains(err.Error(), "-instruction") {
		t.Errorf("the refusal does not say what the Dockerfile equivalent is, or where it is: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("a .env was created for a refused Dockerfile")
	}
}

// ------------------------------------------------------------ atomicity ---

func TestPreviewOfAnExtractWritesNeitherFile(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")

	res, err := PreviewExtract(Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Written {
		t.Error("a preview reported itself written")
	}
	// BOTH diffs. A two-file preview that showed one diff is a lie about the
	// half the reader cannot see.
	if !strings.Contains(res.Compose.Diff, "${POSTGRES_PASSWORD}") {
		t.Errorf("no compose diff:\n%s", res.Compose.Diff)
	}
	if !strings.Contains(res.EnvDiff, "POSTGRES_PASSWORD=hunter2") {
		t.Errorf("no .env diff:\n%s", res.EnvDiff)
	}
	if got := readAt(t, compose); got != src {
		t.Error("preview wrote the compose file")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("preview created the .env")
	}
}

// requireUnwritableDirs is the root guard for the two proofs of step 2, and it
// is LOUD.
//
// A read-only directory is not read-only to root, so those two tests cannot run
// in a root container — which is what CI images and most devcontainers are. They
// called t.Skip, silently, and a silent skip on the only proof that an
// unwritable `.env` leaves the compose file untouched means that step has NO
// TEST AT ALL in exactly the runs most likely to be trusted.
//
// Same shape as extension/host/realcore.test.ts: a banner on stderr whatever the
// reporter is doing, and in the runs that are supposed to cover it — CI, or
// COMPOSURE_REQUIRE_UNWRITABLE=1 — a FAILURE rather than a skip. A run that did not
// exercise the boundary must not be able to look like one that did.
func requireUnwritableDirs(t *testing.T) {
	t.Helper()
	switch unwritableDirsVerdict(os.Geteuid(), os.Getenv("CI"), os.Getenv("COMPOSURE_REQUIRE_UNWRITABLE")) {
	case verdictRun:
		return
	case verdictFail:
		fmt.Fprintf(os.Stderr, unwritableBanner, whyUnwritable)
		t.Fatalf("%s\nCOMPOSURE_REQUIRE_UNWRITABLE (or CI) is set, so this run is one that must exercise it.", whyUnwritable)
	default:
		fmt.Fprintf(os.Stderr, unwritableBanner, whyUnwritable)
		t.Skip(whyUnwritable)
	}
}

const whyUnwritable = "running as root: a read-only directory is not read-only, so step 2 of the " +
	"two-file write (both temps staged before either rename) cannot be proven in this run"

const unwritableBanner = "\n!!! STEP 2 OF THE TWO-FILE WRITE NOT EXERCISED: %s\n" +
	"!!! Run as a non-root user, or set COMPOSURE_REQUIRE_UNWRITABLE=1 to make this a failure.\n\n"

const (
	verdictRun  = "run"
	verdictSkip = "skip"
	verdictFail = "fail"
)

// unwritableDirsVerdict is the decision, split out from the guard so it can be
// tested WITHOUT being root. Left inside requireUnwritableDirs it was a branch
// no run on a developer machine could reach — which is the same shape as the
// silent skip it replaces.
func unwritableDirsVerdict(euid int, ci, require string) string {
	if euid != 0 {
		return verdictRun
	}
	if require == "1" || ci == "true" || ci == "1" {
		return verdictFail
	}
	return verdictSkip
}

func TestTheRootGuardFailsRatherThanSkipsWhereItMatters(t *testing.T) {
	for _, c := range []struct {
		name        string
		euid        int
		ci, require string
		want        string
	}{
		{"an ordinary user runs the proof", 501, "", "", verdictRun},
		{"an ordinary user in CI runs it too", 501, "true", "", verdictRun},
		{"root on a developer machine skips, loudly", 0, "", "", verdictSkip},
		{"root in CI fails", 0, "true", "", verdictFail},
		{"root in CI, the numeric spelling", 0, "1", "", verdictFail},
		{"root with the opt-in fails", 0, "", "1", verdictFail},
	} {
		if got := unwritableDirsVerdict(c.euid, c.ci, c.require); got != c.want {
			t.Errorf("%s: verdict %q, want %q", c.name, got, c.want)
		}
	}
}

// The .env cannot be written. Neither file changes — and the compose file in
// particular is asserted on its BYTES, not on the absence of an error.
func TestAnUnwritableEnvLeavesTheComposeFileAlone(t *testing.T) {
	requireUnwritableDirs(t)
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src, "locked/keep": "x"})
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	compose := filepath.Join(dir, "compose.yaml")
	_, err := ApplyExtract(Extract{
		File:    compose,
		At:      "services.db.environment.POSTGRES_PASSWORD",
		EnvFile: filepath.Join(locked, ".env"),
	})
	if err == nil {
		t.Fatal("no error from an unwritable .env")
	}
	if got := readAt(t, compose); got != src {
		t.Errorf("the compose file was written even though the .env could not be.\n%q", got)
	}
}

// Step 2, and the check that says the ORDER of the two temps matters: if the
// compose file cannot even be staged, the .env must not have been renamed into
// place. An implementation that renames the .env and then discovers it cannot
// write the compose file leaves the exact half-done state this design forbids.
func TestAnUnwritableComposeFileLeavesNoEnvBehind(t *testing.T) {
	requireUnwritableDirs(t)
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"locked/compose.yaml": src})
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	env := filepath.Join(dir, ".env")
	_, err := ApplyExtract(Extract{
		File:    filepath.Join(locked, "compose.yaml"),
		At:      "services.db.environment.POSTGRES_PASSWORD",
		EnvFile: env,
	})
	if err == nil {
		t.Fatal("no error from an unwritable compose file")
	}
	if _, statErr := os.Stat(env); statErr == nil {
		t.Errorf("the .env was written even though the compose file could not be: %q", readAt(t, env))
	}
	if got := readAt(t, filepath.Join(locked, "compose.yaml")); got != src {
		t.Error("the compose file changed")
	}
}

// The remaining window: the .env rename succeeds and the compose rename does
// not. The .env is rolled back — removed, because this operation created it.
func TestAFailedComposeRenameRollsTheEnvBack(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")

	real := renameFile
	renameFile = func(from, to string) error {
		if filepath.Base(to) == "compose.yaml" {
			return errors.New("simulated rename failure")
		}
		return real(from, to)
	}
	t.Cleanup(func() { renameFile = real })

	_, err := ApplyExtract(Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"})
	if err == nil {
		t.Fatal("no error")
	}
	if got := readAt(t, compose); got != src {
		t.Error("the compose file changed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error("the .env this operation created was not rolled back")
	}
}

// THE ORDER OF THE TWO RENAMES, MADE FALSIFIABLE. This is the check that was
// missing, and its absence is the defect rather than the code being wrong.
//
// Every other test of the window fails the rename whose target is
// `compose.yaml`, and BOTH orders produce the same end state under that
// failure — so swapping the two renames in `writeBoth` passed the whole of
// `internal/edit`. Under the swap, failing the `.env` rename leaves exactly
// what DECISIONS.md 25 forbids: measured, the compose file reads
// `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` with no `.env` beside it, which
// `docker compose` resolves to the EMPTY STRING — a database with a blank
// password.
//
// So the failure has to be aimed at the OTHER rename. With the `.env` first,
// its failure happens before anything has been swapped and the correct end
// state is: neither file touched, both temps removed.
func TestAFailedEnvRenameLeavesTheComposeFileUntouched(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")

	real := renameFile
	renameFile = func(from, to string) error {
		if filepath.Base(to) == ".env" {
			return errors.New("simulated .env rename failure")
		}
		return real(from, to)
	}
	t.Cleanup(func() { renameFile = real })

	if _, err := ApplyExtract(Extract{
		File: compose,
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err == nil {
		t.Fatal("no error from a failed .env rename")
	}

	// The criterion, on the BYTES. A compose file referencing a variable no
	// file defines is the half-done state this whole design exists to prevent,
	// and "there was an error" does not say whether it happened.
	if got := readAt(t, compose); got != src {
		t.Errorf("the compose file was written even though the .env could not be renamed into place.\n"+
			"It now reads:\n%s\nwhich resolves ${POSTGRES_PASSWORD} to the empty string.", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Errorf(".env exists after its own rename failed: %q", readAt(t, filepath.Join(dir, ".env")))
	}

	// And nothing else is left in the directory either: step 2 stages BOTH
	// temps before either rename, so a failure here has two of them to remove.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "compose.yaml" {
			t.Errorf("left behind in the directory: %q", e.Name())
		}
	}
}

// The same window against an EXISTING .env: rolled back to its own bytes
// rather than removed.
func TestAFailedComposeRenameRestoresAnExistingEnv(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})

	real := renameFile
	renameFile = func(from, to string) error {
		if filepath.Base(to) == "compose.yaml" {
			return errors.New("simulated rename failure")
		}
		return real(from, to)
	}
	t.Cleanup(func() { renameFile = real })

	if _, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err == nil {
		t.Fatal("no error")
	}
	if got := readAt(t, filepath.Join(dir, ".env")); got != env {
		t.Errorf(".env was not restored.\n got: %q\nwant: %q", got, env)
	}
	if got := readAt(t, filepath.Join(dir, "compose.yaml")); got != src {
		t.Error("the compose file changed")
	}
}

// Which .env: the one beside the compose file being edited, NEVER an env_file.
// Compose does not consult env_file for interpolation, so a value written there
// produces a ${VAR} that resolves to EMPTY under `docker compose`.
func TestTheEnvIsTheOneBesideTheComposeFile(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"stack/compose.yaml": src, ".env": "OTHER=1\n"})
	if _, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "stack", "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err != nil {
		t.Fatal(err)
	}
	if got := readAt(t, filepath.Join(dir, ".env")); got != "OTHER=1\n" {
		t.Errorf("the .env in the PARENT directory was written: %q", got)
	}
	if got := readAt(t, filepath.Join(dir, "stack", ".env")); got != "POSTGRES_PASSWORD=hunter2\n" {
		t.Errorf("stack/.env is %q", got)
	}
}

// A SYMLINKED `.env` is written THROUGH, not replaced.
//
// `proj/.env -> ../shared.env` is an ordinary setup — one secrets file shared
// by several stacks — and it is more ordinary for a `.env` than for anything
// else this product writes. Before this, `stageFile` staged a temp beside the
// LINK and the rename replaced it: `proj/.env` became a regular file holding
// the new content, `shared.env` still read `EXISTING=1`, the link was gone, and
// the exit code was 0 with no warning. Every other stack pointing at the shared
// file never saw the variable.
func TestASymlinkedEnvIsWrittenThrough(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{
		"proj/compose.yaml": src,
		"shared.env":        "EXISTING=1\n",
	})
	link := filepath.Join(dir, "proj", ".env")
	if err := os.Symlink(filepath.Join("..", "shared.env"), link); err != nil {
		t.Skipf("this filesystem does not do symlinks: %v", err)
	}

	res, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "proj", "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The shared file is what changed, and it kept every byte it had.
	shared := filepath.Join(dir, "shared.env")
	if got, want := readAt(t, shared), "EXISTING=1\nPOSTGRES_PASSWORD=hunter2\n"; got != want {
		t.Errorf("the shared file the link points at.\n got: %q\nwant: %q", got, want)
	}
	// The link is still a link. A reader who set one up did so on purpose, and
	// silently turning it into a regular file is the damage that surfaces later
	// in somebody else's terminal.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is no longer a symlink; it was replaced by a regular file holding %q",
			link, readAt(t, link))
	}
	// And the result names the file that actually changed, not the link. A
	// two-file operation that reports the wrong second file is a lie about the
	// half the reader cannot see.
	if res.EnvFile != shared {
		t.Errorf("EnvFile is %q, want the file that was written, %q", res.EnvFile, shared)
	}
	// Not created: the target existed. Getting this wrong makes the rollback in
	// step 3 DELETE somebody's shared secrets file.
	if res.EnvCreated {
		t.Error("an existing shared file was reported as created; a rollback would delete it")
	}
}

// The same link, and the compose rename fails. The rollback must put the SHARED
// file back — to its own bytes, not remove it, and not through the link.
func TestAFailedComposeRenameRestoresASymlinkedEnv(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{
		"proj/compose.yaml": src,
		"shared.env":        "EXISTING=1\n",
	})
	link := filepath.Join(dir, "proj", ".env")
	if err := os.Symlink(filepath.Join("..", "shared.env"), link); err != nil {
		t.Skipf("this filesystem does not do symlinks: %v", err)
	}

	real := renameFile
	renameFile = func(from, to string) error {
		if filepath.Base(to) == "compose.yaml" {
			return errors.New("simulated rename failure")
		}
		return real(from, to)
	}
	t.Cleanup(func() { renameFile = real })

	if _, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "proj", "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err == nil {
		t.Fatal("no error")
	}
	if got := readAt(t, filepath.Join(dir, "shared.env")); got != "EXISTING=1\n" {
		t.Errorf("the shared file was not restored: %q", got)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("the link is gone: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the rollback left a regular file where the link was")
	}
}

// A .env with CRLF endings and no trailing newline keeps both properties.
func TestTheEnvKeepsItsOwnEndingAndTrailingNewline(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": "A=1\r\nB=2"})
	if _, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
	}); err != nil {
		t.Fatal(err)
	}
	got := readAt(t, filepath.Join(dir, ".env"))
	if got != "A=1\r\nB=2\r\nPOSTGRES_PASSWORD=hunter2" {
		t.Errorf(".env is %q", got)
	}
	for i := range got {
		if got[i] == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Fatalf("byte %d is an LF with no CR before it in a CRLF file: %q", i, got)
		}
	}
}

// The conflict refusal names the line that is IN EFFECT.
//
// `resolve.ParseDotEnv` takes the LAST definition of a repeated name — each line
// overwrites the one before, which is what `docker compose` does — while
// `envLineOf` returned the FIRST. So a doubly-defined name was reported with the
// value from one line and the number of another, and the reader's next move,
// which is to go and settle the two by hand, started at the line that is not the
// one in effect.
func TestTheConflictRefusalNamesTheLineThatIsInEffect(t *testing.T) {
	env := edgeFixture(t, "e50-doubly-defined.env")

	// The premise: the two lines disagree, and the parser takes the second.
	if got := resolve.ParseDotEnv([]byte(env))["DUPLICATE"]; got != "second" {
		t.Fatalf("the fixture no longer exercises this: DUPLICATE reads back as %q", got)
	}
	// Line 13 is `DUPLICATE=first`, line 14 is `DUPLICATE=second`, and line 15
	// is a COMMENTED-OUT third definition that must not be picked — no branch
	// skips it, the `#` simply stays in the key, and this asserts that.
	if got := envLineOf([]byte(env), "DUPLICATE"); got != 14 {
		t.Errorf("envLineOf reports line %d, want 14 — the definition ParseDotEnv actually uses", got)
	}

	// And end to end, in the sentence the reader sees.
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})
	_, err := ApplyExtract(Extract{
		File: filepath.Join(dir, "compose.yaml"),
		At:   "services.db.environment.POSTGRES_PASSWORD",
		Name: "DUPLICATE",
	})
	if !errors.Is(err, ErrVarConflict) {
		t.Fatalf("error is %v, want ErrVarConflict", err)
	}
	if !strings.Contains(err.Error(), ".env:14") {
		t.Errorf("the refusal does not point at line 14, the one in effect: %v", err)
	}
	if !strings.Contains(err.Error(), `"second"`) {
		t.Errorf("the refusal does not quote the value in effect: %v", err)
	}
}

// The ARG sentence is for DOCKERFILES, not for everything that is not a compose
// file.
//
// The refusal tested `strategy.RootIsMapping` alone, so any YAML whose root is
// not a mapping — a sequence, a bare scalar, an empty file — was told "the
// Dockerfile equivalent of this is an `ARG`". `composure extract -at a list.yaml`
// answering with a paragraph about build arguments is a confident wrong answer
// about which FILE the reader is holding.
func TestOnlyADockerfileGetsTheARGSentence(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
	}{
		{"a sequence at the root", "- a\n- b\n"},
		{"a bare scalar", "just a string\n"},
		{"an empty file", ""},
	} {
		dir := extractDir(t, map[string]string{"list.yaml": c.body})
		path := filepath.Join(dir, "list.yaml")
		_, err := ApplyExtract(Extract{File: path, At: "a"})
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if !Refused(err) {
			t.Errorf("%s: not classified as a refusal: %v", c.name, err)
		}
		if strings.Contains(err.Error(), "ARG") {
			t.Errorf("%s: told the reader about build arguments: %v", c.name, err)
		}
		if !strings.Contains(err.Error(), "list.yaml") {
			t.Errorf("%s: the refusal does not name the file: %v", c.name, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr == nil {
			t.Errorf("%s: a .env was created for a refused file", c.name)
		}
	}

	// And the Dockerfile still gets it — the sentence is not simply deleted.
	dir := extractDir(t, map[string]string{"Dockerfile": "FROM alpine\nENV TOKEN=t0ken\n"})
	_, err := ApplyExtract(Extract{File: filepath.Join(dir, "Dockerfile"), At: "x"})
	if !errors.Is(err, ErrWrongGrammar) || !strings.Contains(err.Error(), "ARG") {
		t.Errorf("a real Dockerfile lost the ARG sentence: %v", err)
	}
}
