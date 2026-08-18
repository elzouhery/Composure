package edit

// Story 9.4 — moving a Dockerfile literal into a build argument.
//
// The traps this file exists to spring, named because a check that could not
// fail is the failure mode this repository has shipped twenty-one of:
//
//	A FIXTURE WITH ONE STAGE cannot distinguish "above the first FROM" from
//	"above the FROM being changed", which is the whole placement rule. Every
//	placement assertion below runs against a multi-stage file, and the
//	assertion is a WHOLE-BUFFER comparison — "the file now contains
//	${NODE_VERSION}" is true of a file with the ARG in the wrong stage, in the
//	wrong order, or at the end where it is declared after its own use.
//
//	A test that asserts an error says nothing about whether the file was
//	touched. Every refusal below re-reads the file and compares bytes.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/dockerfile"
)

// argFile copies an edge fixture into a temp dir and returns its path and its
// original bytes.
func argFile(t *testing.T, name string) (string, string) {
	t.Helper()
	src := edgeFixture(t, name)
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, src
}

// argInstruction is the instruction index whose line begins with prefix. Typed
// indices go stale the moment a fixture gains a line, and a stale index
// addresses something else without saying so.
func argInstruction(t *testing.T, src, prefix string) int {
	t.Helper()
	f := dockerfile.Parse([]byte(src))
	lines := strings.Split(src, "\n")
	for i, in := range f.Instructions {
		if in.Kind != dockerfile.KindInstruction {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(lines[in.StartLine]), prefix) {
			return i
		}
	}
	t.Fatalf("no instruction line starting %q", prefix)
	return -1
}

// ---------------------------------------------------------- the happy paths ---

func TestMovingAFromTagIntoAGlobalBuildArgument(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	// The SECOND FROM. Its declaration must still land above the FIRST one:
	// a FROM can only use an ARG declared before the first FROM, and that is
	// not a preference.
	idx := argInstruction(t, src, "FROM node:18")

	res, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx, Part: "tag", Name: "NODE_VERSION"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != "18" {
		t.Errorf("the literal moved was %q", res.Value)
	}
	if res.Scope != ScopeGlobal {
		t.Errorf("scope is %q, want %q", res.Scope, ScopeGlobal)
	}
	if !res.Declared || res.Redeclared || res.AlreadyDeclared {
		t.Errorf("declared=%v redeclared=%v already=%v", res.Declared, res.Redeclared, res.AlreadyDeclared)
	}

	want := strings.Replace(src,
		"# The build stage pulls the toolchain in.\nFROM golang:1.24-alpine AS build",
		"ARG NODE_VERSION=18\n# The build stage pulls the toolchain in.\nFROM golang:1.24-alpine AS build", 1)
	want = strings.Replace(want, "FROM node:18 AS runtime", "FROM node:${NODE_VERSION} AS runtime", 1)
	if got := readAt(t, path); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestTheNameIsDerivedFromTheImageWhenNoneIsGiven(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	idx := argInstruction(t, src, "FROM node:18")

	res, err := PreviewExtractArg(ExtractArg{File: path, Instruction: idx})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "NODE_VERSION" {
		t.Errorf("derived name is %q, want NODE_VERSION", res.Name)
	}
	// A preview writes nothing. Bytes, not a flag.
	if got := readAt(t, path); got != src {
		t.Error("the preview wrote to the file")
	}
}

func TestAValueInsideAStageIsDeclaredInsideThatStage(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	idx := argInstruction(t, src, "ENV CGO_ENABLED=0")

	res, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope != "stage 0" {
		t.Errorf("scope is %q, want %q", res.Scope, "stage 0")
	}
	// Directly above the instruction, above its comment block, INSIDE stage 0 —
	// not before the first FROM, where the stage could not see it without a
	// re-declaration, and not at the end of the stage, where it would be
	// declared after its own use.
	want := strings.Replace(src,
		"# Cgo is off so the binary is static.\nENV CGO_ENABLED=0",
		"ARG CGO_ENABLED=0\n# Cgo is off so the binary is static.\nENV CGO_ENABLED=${CGO_ENABLED}", 1)
	if got := readAt(t, path); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestAGlobalArgIsPulledIntoScopeWithABareRedeclaration(t *testing.T) {
	path, src := argFile(t, "e52-global-arg.Dockerfile")
	idx := argInstruction(t, src, "ENV NODE_VERSION=18")

	res, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Redeclared {
		t.Error("it did not report a re-declaration")
	}
	if res.ArgLine != "ARG NODE_VERSION" {
		t.Errorf("the declaration written is %q; a default here would shadow the global one", res.ArgLine)
	}
	// ABOVE the comment block, not between the comment and the ENV it
	// documents — the same placement rule the global case follows.
	want := strings.Replace(src,
		"# The global ARG above is NOT in scope in here",
		"ARG NODE_VERSION\n# The global ARG above is NOT in scope in here", 1)
	want = strings.Replace(want, "ENV NODE_VERSION=18", "ENV NODE_VERSION=${NODE_VERSION}", 1)
	if got := readAt(t, path); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

func TestADeclarationAlreadyThereWithTheSameDefaultIsNotWrittenTwice(t *testing.T) {
	path, src := argFile(t, "e52-global-arg.Dockerfile")
	idx := argInstruction(t, src, "ENV APP_ENV=production")

	res, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyDeclared || res.Declared {
		t.Errorf("already=%v declared=%v", res.AlreadyDeclared, res.Declared)
	}
	// The substitution alone. No second ARG anywhere.
	want := strings.Replace(src, "ENV APP_ENV=production", "ENV APP_ENV=${APP_ENV}", 1)
	if got := readAt(t, path); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
	if strings.Count(readAt(t, path), "ARG APP_ENV") != 1 {
		t.Error("a second ARG APP_ENV was written")
	}
}

func TestTheResultSaysBuildArgsIsNotWired(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	res, err := PreviewExtractArg(ExtractArg{File: path, Instruction: argInstruction(t, src, "FROM node:18")})
	if err != nil {
		t.Fatal(err)
	}
	// DECISIONS.md 27: the absence is a sentence the reader gets, not a gap.
	if !strings.Contains(res.ComposeNote, "build.args") || !strings.Contains(res.ComposeNote, "NODE_VERSION") {
		t.Errorf("the compose note does not say what to write: %q", res.ComposeNote)
	}
}

// ------------------------------------------------------------- the refusals ---

// refusedAndUntouched asserts the sentinel AND that the file still holds its
// original bytes. An error alone says nothing about what landed.
func refusedAndUntouched(t *testing.T, path, src string, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error is %v, want %v", err, want)
	}
	if !Refused(err) {
		t.Errorf("%v is not classified as a refusal", err)
	}
	if got := readAt(t, path); got != src {
		t.Errorf("the file was touched by a refusal.\n got: %q\nwant: %q", got, src)
	}
}

// Two conflicts, two arms, and they are separate tests on purpose: one is with
// the GLOBAL declaration and one is with the declaration in the scope being
// written to. A test of only the first leaves the second unexercised, which is
// the shape of the twenty-one checks that could not fail.
func TestAConflictingGlobalDeclarationIsRefused(t *testing.T) {
	path, src := argFile(t, "e52-global-arg.Dockerfile")
	// Stage 1's literal is 20; the global ARG says 18. A bare re-declaration
	// would pull in 18 and a local default would shadow it — two answers that
	// disagree, so neither is written.
	idx := argInstruction(t, src, "ENV NODE_VERSION=20")

	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	refusedAndUntouched(t, path, src, err, ErrArgConflict)
	if !strings.Contains(err.Error(), "18") || !strings.Contains(err.Error(), "20") {
		t.Errorf("the refusal names neither value: %v", err)
	}
}

func TestAConflictingDeclarationInTheTargetScopeIsRefused(t *testing.T) {
	path, src := argFile(t, "e52-global-arg.Dockerfile")
	// Stage 1 declares LOG_LEVEL=debug itself; the literal is info.
	idx := argInstruction(t, src, "ENV LOG_LEVEL=info")

	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	refusedAndUntouched(t, path, src, err, ErrArgConflict)
	if !strings.Contains(err.Error(), "debug") || !strings.Contains(err.Error(), "info") {
		t.Errorf("the refusal names neither value: %v", err)
	}
	if !strings.Contains(err.Error(), "stage 1") {
		t.Errorf("the refusal does not name the scope it is about: %v", err)
	}
}

func TestAValueThatIsAlreadyAReferenceIsRefused(t *testing.T) {
	path, src := argFile(t, "e52-global-arg.Dockerfile")
	idx := argInstruction(t, src, "FROM node:${NODE_VERSION} AS build")

	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx, Part: "tag"})
	refusedAndUntouched(t, path, src, err, ErrAlreadyInterpolated)
}

func TestAValueThatCannotBeWrittenAsABareDefaultIsRefused(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	idx := argInstruction(t, src, `ENV APP_GREETING="hello world"`)

	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	refusedAndUntouched(t, path, src, err, ErrArgValue)
}

func TestAnInstructionWithNoKeyValueShapeIsRefused(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	idx := argInstruction(t, src, "RUN go build")

	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	refusedAndUntouched(t, path, src, err, ErrNoLiteral)
}

func TestADigestPinnedFromAndAnUntaggedOneAreRefused(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	for _, prefix := range []string{"FROM alpine@sha256:", "FROM busybox"} {
		idx := argInstruction(t, src, prefix)
		_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx, Part: "tag", Name: "V"})
		refusedAndUntouched(t, path, src, err, ErrNoTag)
	}
}

func TestAComposeFileIsRefusedByName(t *testing.T) {
	src := edgeFixture(t, "e45-plaintext-credential.yml")
	dir := extractDir(t, map[string]string{"compose.yaml": src})
	path := filepath.Join(dir, "compose.yaml")

	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: 0})
	refusedAndUntouched(t, path, src, err, ErrWrongGrammar)
	if !strings.Contains(err.Error(), "compose") {
		t.Errorf("the refusal does not name the grammar: %v", err)
	}
}

func TestAnInstructionIndexTheFileDoesNotHaveIsRefused(t *testing.T) {
	path, src := argFile(t, "e51-two-stage-arg.Dockerfile")
	_, err := ApplyExtractArg(ExtractArg{File: path, Instruction: 9999})
	if err == nil {
		t.Fatal("an index past the end was accepted")
	}
	if got := readAt(t, path); got != src {
		t.Error("the file was touched")
	}
}

// ---------------------------------------------------------- the readback ---

// The engine does not crash; it returns a confident wrong answer. So the result
// is read back through the parser before it is written, and this exercises the
// guard against a buffer that is exactly the failure it exists to catch — a
// declaration BELOW its own use, which builds and produces something else.
func TestTheReadbackRefusesADeclarationBelowItsUse(t *testing.T) {
	bad := []byte("FROM node:${NODE_VERSION}\nARG NODE_VERSION=18\nRUN true\n")
	err := argReadback(bad, "NODE_VERSION", "18", 0, true)
	if !errors.Is(err, ErrWouldCorrupt) {
		t.Fatalf("error is %v, want ErrWouldCorrupt", err)
	}

	good := []byte("ARG NODE_VERSION=18\nFROM node:${NODE_VERSION}\nRUN true\n")
	if err := argReadback(good, "NODE_VERSION", "18", 1, true); err != nil {
		t.Errorf("the correct buffer was refused: %v", err)
	}
}

// The THIRD arm, which had no test at all: requireBeforeFirstFrom.
//
// `if false {` around that arm left the suite green. The test above passes
// useIdx=0, so its loop over `i < useIdx` runs zero times, it exits at the
// `!found` arm, and it never reaches the FROM check it was named for —
// which made DECISIONS.md 27's claim that the guard "has been seen to fail"
// true of two arms out of three.
//
// Property 2 is the one arm nothing else can catch: this buffer PARSES, the
// declaration IS above its own use, and the value in scope IS the literal. It
// is still wrong, because a FROM can only use an ARG declared before the FIRST
// FROM, and `ARG V=1` sitting inside stage 0 expands to the empty string in
// stage 1's FROM with no error at all.
func TestTheReadbackRefusesAFromArgDeclaredBelowTheFirstFrom(t *testing.T) {
	bad := []byte("FROM a\nARG V=1\nFROM b:${V}\n")

	err := argReadback(bad, "V", "1", 2, true)
	if !errors.Is(err, ErrWouldCorrupt) {
		t.Fatalf("error is %v, want ErrWouldCorrupt", err)
	}
	if !strings.Contains(err.Error(), "first FROM") {
		t.Errorf("the refusal is not the FROM-scope one, so an earlier arm did the refusing: %v", err)
	}

	// ...and this is what proves it was the THIRD arm and not one of the two
	// above it. The SAME buffer, with the FROM requirement lifted, is ACCEPTED:
	// arms one and two are satisfied by it, so only arm three can be what
	// refused it a moment ago. Without this line the test passes against an
	// implementation where requireBeforeFirstFrom does nothing.
	if err := argReadback(bad, "V", "1", 2, false); err != nil {
		t.Errorf("arms one and two rejected the buffer, so the assertion above proves nothing: %v", err)
	}
}

func TestTheReadbackRefusesADeclarationCarryingTheWrongLiteral(t *testing.T) {
	bad := []byte("ARG NODE_VERSION=20\nFROM node:${NODE_VERSION}\nRUN true\n")
	if err := argReadback(bad, "NODE_VERSION", "18", 1, true); !errors.Is(err, ErrWouldCorrupt) {
		t.Fatalf("error is %v, want ErrWouldCorrupt", err)
	}
}

// ------------------------------------------- scope is POSITIONAL, not stage ---

// A declaration LOWER DOWN in the target's own stage is not in scope at the
// target, and counting it made 9.4 refuse an operation it had itself made
// impossible.
//
// `ARG NAME` is positional: an instruction sees the declarations ABOVE it and
// no others. argsInScope collected the whole stage, so a same-named ARG further
// down with the same value made decideDeclaration report `alreadyDeclared`, no
// declaration was written, and the readback — which correctly only looks above
// the use — refused with `nothing declares APP_VERSION above instruction 1`.
//
// The reader saw a refusal whose stated reason was produced by the planning
// step that decided not to write the thing it then complained was missing.
//
// The assertions are on the WHOLE BUFFER, because "the file contains an ARG"
// is true of the file that already did.
func TestADeclarationBelowTheTargetIsNotInScopeAtIt(t *testing.T) {
	path, src := argFile(t, "e56-later-arg-same-stage.Dockerfile")
	idx := argInstruction(t, src, "ENV APP_VERSION=")

	res, err := ApplyExtractArg(ExtractArg{File: path, Instruction: idx})
	if err != nil {
		t.Fatalf("the move was refused: %v", err)
	}
	if res.AlreadyDeclared {
		t.Fatal("the later declaration was counted as being in scope at the target")
	}
	if !res.Declared {
		t.Errorf("declared=%v redeclared=%v — a declaration carrying the default is the only correct answer here",
			res.Declared, res.Redeclared)
	}
	if res.Value != "1.2.3" {
		t.Errorf("the literal moved was %q", res.Value)
	}

	// The declaration goes directly above its own use, inside the stage, and
	// the ARG that was already further down is left exactly where it was.
	want := strings.Replace(src,
		"ENV APP_VERSION=1.2.3\n",
		"ARG APP_VERSION=1.2.3\nENV APP_VERSION=${APP_VERSION}\n", 1)
	if got := readAt(t, path); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

// The other side of the same rule: a declaration ABOVE the target with the same
// value IS in scope, and is still the idempotent no-second-declaration case.
// Without this, "ignore everything at or below the target" and "ignore every
// declaration in the stage" are the same test.
func TestADeclarationAboveTheTargetIsStillInScopeAtIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	src := "FROM alpine:3.20\nARG APP_VERSION=1.2.3\nENV APP_VERSION=1.2.3\nRUN true\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyExtractArg(ExtractArg{File: path, Instruction: 2})
	if err != nil {
		t.Fatalf("the move was refused: %v", err)
	}
	if !res.AlreadyDeclared {
		t.Errorf("a declaration above the target with the same value was not treated as already in scope: %+v", res)
	}
	want := strings.Replace(src, "ENV APP_VERSION=1.2.3\n", "ENV APP_VERSION=${APP_VERSION}\n", 1)
	if got := readAt(t, path); got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}
