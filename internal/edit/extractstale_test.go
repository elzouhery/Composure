package edit

// Story 9.6 — the two-file write joins the staleness contract.
//
// THE TRAP THIS FILE EXISTS TO AVOID: a staleness test whose two files change
// TOGETHER cannot tell which half the check noticed, so it would pass against
// an implementation that only ever looks at one of them. Every test below moves
// EXACTLY ONE of the two files and asserts the other is byte-identical.
//
// The second trap is the convergence one. DECISIONS.md 25 step 3 leaves a
// designed-inert residue — a `.env` carrying a variable nothing references —
// and the re-run that fixes it finds the `.env` changed since the preview. A
// staleness check that does not know this turns the recovery into a refusal.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// expectOf is the compose half's assertion, recorded from a preview exactly as
// a client records one: the range and the bytes the preview reported.
func expectOf(t *testing.T, res *ExtractResult) *Expect {
	t.Helper()
	if res.Compose == nil || len(res.Compose.Ops) == 0 {
		t.Fatal("the preview reported no operation to assert against")
	}
	op := res.Compose.Ops[0]
	return &Expect{Start: op.Range.Start, End: op.Range.End, Text: op.Before}
}

func TestTheComposeHalfGoingStaleRefusesAndWritesNeitherFile(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")
	req := Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"}

	preview, err := PreviewExtract(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Expect = expectOf(t, preview)
	req.ExpectEnv = preview.EnvExpect

	// ONLY the compose file moves. A line added at the top shifts every offset
	// below it, which is exactly what a colleague's edit does.
	moved := "# somebody else got here first\n" + src
	if err := os.WriteFile(compose, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = ApplyExtract(req)
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("error is %v, want ErrStaleRange", err)
	}
	if Reason(err) != "stale-range" {
		t.Errorf("the slug is %q", Reason(err))
	}
	// Symmetry with the `.env` half's refusal below, which names its own file
	// and says "the compose file is not the half that moved". This one said
	// `services.db.environment.POSTGRES_PASSWORD now reads ...` — a config
	// path, no file at all — and said nothing about the second file. A reader
	// holding a refusal from a TWO-FILE operation cannot tell from that whether
	// the `.env` was already written, which is the one thing they need to know
	// before they look. Both halves of that are asserted.
	if !strings.Contains(err.Error(), "compose.yaml") {
		t.Errorf("the refusal does not name which of the two files moved: %v", err)
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("the refusal does not say what became of the .env half: %v", err)
	}
	// Neither file. The compose file still holds the colleague's version, and
	// the `.env` was never created.
	if got := readAt(t, compose); got != moved {
		t.Error("the compose file was written over")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Error("a .env was created by a refused apply")
	}
}

func TestTheEnvHalfGoingStaleRefusesAsStalenessRatherThanConflict(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})
	compose := filepath.Join(dir, "compose.yaml")
	envPath := filepath.Join(dir, ".env")
	req := Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"}

	preview, err := PreviewExtract(req)
	if err != nil {
		t.Fatal(err)
	}
	if preview.EnvExpect == nil || preview.EnvExpect.Defined {
		t.Fatalf("the preview reported %+v; the name is not in that .env", preview.EnvExpect)
	}
	req.Expect = expectOf(t, preview)
	req.ExpectEnv = preview.EnvExpect

	// ONLY the `.env` moves, and it gains the name with a DIFFERENT value —
	// somebody else's decision, not this operation's residue.
	changed := env + "POSTGRES_PASSWORD=somebody-elses\n"
	if err := os.WriteFile(envPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = ApplyExtract(req)
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("error is %v, want ErrStaleRange", err)
	}
	// The reader's next move is to look again, not to choose another name, so
	// this must NOT come back as a conflict.
	if errors.Is(err, ErrVarConflict) {
		t.Error("it refused as a naming conflict rather than as staleness")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("the refusal does not name which of the two files moved: %v", err)
	}
	if got := readAt(t, compose); got != src {
		t.Error("the compose file was written")
	}
	if got := readAt(t, envPath); got != changed {
		t.Error("the .env was written over")
	}
}

// The one criterion that catches a naive check. The `.env` DID change since the
// preview — that is what step 3's window does — and the re-run must still
// converge rather than refuse.
func TestTheResidualWindowStillConverges(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")
	envPath := filepath.Join(dir, ".env")
	req := Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"}

	preview, err := PreviewExtract(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Expect = expectOf(t, preview)
	req.ExpectEnv = preview.EnvExpect // "undefined when I previewed"

	// Step 3's residue, frozen as a fixture: the `.env` rename landed and the
	// compose one did not.
	residue := edgeFixture(t, "e54-residue.env")
	if err := os.WriteFile(envPath, []byte(residue), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyExtract(req)
	if err != nil {
		t.Fatalf("the re-run that fixes the window was refused: %v", err)
	}
	if !res.EnvUnchanged {
		t.Error("the .env half was not a no-op")
	}
	if got := readAt(t, envPath); got != residue {
		t.Error("the .env was rewritten by the converging re-run")
	}
	want := strings.Replace(src, "POSTGRES_PASSWORD: hunter2", "POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}", 1)
	if got := readAt(t, compose); got != want {
		t.Errorf("the compose half did not land.\n got: %q\nwant: %q", got, want)
	}
}

// The unversioned caller, decided explicitly rather than inherited: no
// expectation means the edit is applied against the files as they stand, which
// is what the extension shipped against and what a CLI invocation needs.
func TestNoExpectationAtAllAppliesAgainstTheFileAsItStands(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	compose := filepath.Join(dir, "compose.yaml")

	moved := "# somebody else got here first\n" + src
	if err := os.WriteFile(compose, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyExtract(Extract{File: compose, At: "services.db.environment.POSTGRES_PASSWORD"})
	if err != nil {
		t.Fatalf("a request with no expectation was refused: %v", err)
	}
	if !res.Written {
		t.Error("it did not write")
	}
}

func TestThePreviewReportsTheEnvExpectationToSendBack(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})
	compose := filepath.Join(dir, "compose.yaml")

	// API_TOKEN IS defined in that .env, with exactly this value, so the
	// expectation is "defined, as t0ken" — the other half of the assertion.
	res, err := PreviewExtract(Extract{File: compose, At: "services.api.environment[0]"})
	if err != nil {
		t.Fatal(err)
	}
	if res.EnvExpect == nil || !res.EnvExpect.Defined || res.EnvExpect.Value != "t0ken" {
		t.Fatalf("the preview reported %+v, want defined as t0ken", res.EnvExpect)
	}
	// And sending it straight back is accepted — an assertion that cannot be
	// satisfied by the state it was taken from is not an assertion.
	if _, err := ApplyExtract(Extract{File: compose, At: "services.api.environment[0]",
		ExpectEnv: res.EnvExpect}); err != nil {
		t.Fatalf("the expectation the preview reported was rejected: %v", err)
	}
}

func TestAnEnvValueThatChangedUnderAMatchingNameIsStale(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	env := edgeFixture(t, "e46-existing.env")
	dir := extractDir(t, map[string]string{"compose.yaml": src, ".env": env})
	compose := filepath.Join(dir, "compose.yaml")
	envPath := filepath.Join(dir, ".env")

	// Defined at preview, and STILL defined at apply — with another value.
	// Offsets say nothing about this; only the variable does.
	changed := env + "\nAPI_TOKEN=rotated\n"
	if err := os.WriteFile(envPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyExtract(Extract{File: compose, At: "services.api.environment[0]",
		ExpectEnv: &ExpectVar{Defined: true, Value: "t0ken"}})
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("error is %v, want ErrStaleRange", err)
	}
	if got := readAt(t, compose); got != src {
		t.Error("the compose file was written")
	}
	if got := readAt(t, envPath); got != changed {
		t.Error("the .env was written")
	}
}
