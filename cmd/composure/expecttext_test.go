package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `-expect-text` on its own is a staleness guard, and it used to be ignored.
//
// hasExpect was set only from the two integer offsets, so a caller who asked
// "write this only if the target still reads X" was answered as though they had
// asked for no guard at all: the edit went through against text that had
// changed underneath them. The flag is documented as an equal of the other two,
// which is what made it worse than an absent feature — the caller believed they
// were protected. A staleness check that silently does nothing is the failure
// shape this engine exists to refuse.
//
// Both directions are asserted. A guard that always refuses is as broken as one
// that never fires, and only the pair tells a working check from either.
func TestExpectTextAloneIsAStalenessGuard(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n"

	write := func(t *testing.T) (dir, path string) {
		t.Helper()
		dir = t.TempDir()
		path = filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, path
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("refuses when the text has changed", func(t *testing.T) {
		dir, path := write(t)
		res := runCLI(t, dir, "apply", "-op", "replace_scalar",
			"-at", "services.web.image", "-value", "redis",
			"-expect-text", "something-else", "compose.yaml")
		if res.code == 0 {
			t.Fatalf("written despite a stale expectation:\n%s%s", res.stdout, res.stderr)
		}
		if got := read(t, path); got != src {
			t.Errorf("the file was written anyway:\n%s", got)
		}
	})

	t.Run("writes when the text still matches", func(t *testing.T) {
		dir, path := write(t)
		res := runCLI(t, dir, "apply", "-op", "replace_scalar",
			"-at", "services.web.image", "-value", "redis",
			"-expect-text", "nginx", "compose.yaml")
		if res.code != 0 {
			t.Fatalf("a matching expectation was refused (exit %d):\n%s%s", res.code, res.stdout, res.stderr)
		}
		if got := read(t, path); !strings.Contains(got, "image: redis") {
			t.Errorf("the edit did not land:\n%s", got)
		}
	})
}

// ...and the same flag on `extract`, which is the worse of the two places.
//
// `apply` writes one file. `extract` writes TWO — the compose file and the
// `.env` beside it — so a guard that silently does nothing here does not just
// let a stale edit through, it lets a stale edit through into a second file the
// caller never named on the command line. The wiring was a separate call site
// from `apply`'s and was missed when `apply`'s was fixed.
//
// Neutering the whole clause with `if false {` left `go test ./cmd/composure/`
// green before this existed: nothing drove `-expect-text` through `extract` at
// all.
//
// Both directions again, and the refusal asserts BOTH files: "it did not write
// the compose file" is satisfied by an implementation that wrote the `.env`
// first and then refused, which is the exact failure the two-file write has and
// the single-file one does not.
func TestExtractTakesExpectTextOnItsOwn(t *testing.T) {
	const src = "services:\n  db:\n    environment:\n      POSTGRES_PASSWORD: hunter2\n"
	const at = "services.db.environment.POSTGRES_PASSWORD"

	write := func(t *testing.T) (dir, compose, env string) {
		t.Helper()
		dir = t.TempDir()
		compose = filepath.Join(dir, "compose.yaml")
		env = filepath.Join(dir, ".env")
		if err := os.WriteFile(compose, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, compose, env
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("refuses when the text has changed, and writes NEITHER file", func(t *testing.T) {
		dir, compose, env := write(t)
		res := runCLI(t, dir, "extract", "-at", at,
			"-expect-text", "something-else", "-write", "compose.yaml")
		if res.code == 0 {
			t.Fatalf("the move went through against a stale expectation:\n%s%s", res.stdout, res.stderr)
		}
		if got := read(t, compose); got != src {
			t.Errorf("the compose file was written anyway:\n%s", got)
		}
		// The half nobody named on the command line. A refusal that has already
		// created this is the two-file failure mode.
		if _, err := os.Stat(env); !os.IsNotExist(err) {
			t.Errorf("a .env was created by a refused extract: %v, %s", err, read(t, env))
		}
	})

	t.Run("writes both when the text still matches", func(t *testing.T) {
		dir, compose, env := write(t)
		res := runCLI(t, dir, "extract", "-at", at,
			"-expect-text", "hunter2", "-write", "compose.yaml")
		if res.code != 0 {
			t.Fatalf("a matching expectation was refused (exit %d):\n%s%s", res.code, res.stdout, res.stderr)
		}
		if got := read(t, compose); !strings.Contains(got, "POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}") {
			t.Errorf("the compose half did not land:\n%s", got)
		}
		if got := read(t, env); !strings.Contains(got, "POSTGRES_PASSWORD=hunter2") {
			t.Errorf("the .env half did not land:\n%s", got)
		}
	})
}
