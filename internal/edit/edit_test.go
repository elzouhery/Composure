package edit

// The tests that decide whether Epic 6 shipped.
//
// Unit tests are the smaller half. The half that matters is the corpus sweep at
// the foot of this file: it takes real compose files nobody wrote for this test,
// applies a scalar edit through the SAME surface the extension's Save button
// uses, and asserts the diff is exactly two lines and the result still parses.
// That is editbench's own criterion reached through the write path, and it is
// the only way to know the write path did not quietly acquire a second
// implementation.
//
// The engine's characteristic failure is a confident wrong answer, not a crash.
// Every assertion below is therefore about BYTES, never about the absence of an
// error.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
	goccyast "github.com/goccy/go-yaml/ast"
	goccyparser "github.com/goccy/go-yaml/parser"
)

const corpusRoot = "../../corpus-repos"

// write puts src in a temp file and returns its path.
func write(t *testing.T, name, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const sample = `# a stack
services:
  web:
    image: nginx:1.25   # pinned deliberately
    ports:
      - "8080:80"

  db:
    image: 'postgres:16'
    environment:
      POSTGRES_PASSWORD: hunter2
`

// ---------------------------------------------------------------- preview ---

// The headline claim of the whole epic. A staged scalar change is one line
// removed and one line added, and everything else in the file is untouched.
func TestPreviewOfAScalarIsATwoLineDiff(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	res, err := Preview(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("diff is %d removed / %d added, want 1 and 1:\n%s", res.Removed, res.Added, res.Diff)
	}
	if !strings.Contains(res.Diff, "-    image: nginx:1.25   # pinned deliberately") {
		t.Errorf("the removed line is not the one that was there:\n%s", res.Diff)
	}
	if !strings.Contains(res.Diff, "+    image: nginx:1.27   # pinned deliberately") {
		t.Errorf("the trailing comment did not survive:\n%s", res.Diff)
	}
	if !strings.Contains(res.Diff, "--- a/compose.yaml") {
		t.Errorf("the diff does not name the file it touches:\n%s", res.Diff)
	}
}

// Preview writes nothing. This is the acceptance criterion the reader is being
// asked to trust, so it is asserted on the bytes and on the mtime.
func TestPreviewWritesNothing(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}}); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := read(t, path); got != sample {
		t.Error("preview changed the file")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("preview touched the file's mtime")
	}
}

// Preview and Apply are one boolean apart and must produce the same diff. A
// preview the write does not honour is the one failure that would make every
// other guarantee here unverifiable.
func TestPreviewAndApplyProduceTheSameDiff(t *testing.T) {
	op := Op{Operation: OpReplaceScalar, At: "services.db.image", Value: "postgres:17"}

	a := write(t, "compose.yaml", sample)
	preview, err := Preview(Request{File: a, Ops: []Op{op}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	b := write(t, "compose.yaml", sample)
	applied, err := Apply(Request{File: b, Ops: []Op{op}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if preview.Diff != applied.Diff {
		t.Errorf("preview and apply disagree:\npreview:\n%s\napply:\n%s", preview.Diff, applied.Diff)
	}
	if !applied.Written {
		t.Error("apply did not report a write")
	}
	if string(preview.Bytes) != read(t, b) {
		t.Error("the bytes preview computed are not the bytes apply wrote")
	}
	// Quoting style is a property of the source, not of the engine.
	if !strings.Contains(read(t, b), "image: 'postgres:17'") {
		t.Errorf("single quotes did not survive the edit:\n%s", read(t, b))
	}
}

// ------------------------------------------------------------ refusals -----

// AD-19. A stage held against a range that has moved is refused; nothing is
// written and the message says which target moved and where to.
func TestStaleRangeIsRefusedRatherThanRebased(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	staged, err := Preview(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	rng := staged.Ops[0].Range

	// Someone else edits the file: a line added above moves every later byte.
	moved := "# added by another editor\n" + sample
	if err := os.WriteFile(path, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar,
		At:        "services.web.image",
		Value:     "nginx:1.27",
		Expect:    &Expect{Start: rng.Start, End: rng.End, Text: staged.Ops[0].Before},
	}}})
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("apply against a moved range returned %v, want ErrStaleRange", err)
	}
	if Reason(err) != "stale-range" {
		t.Errorf("Reason = %q, want stale-range", Reason(err))
	}
	if got := read(t, path); got != moved {
		t.Error("a refused apply wrote to the file")
	}
}

// The same-offset case: the range did not move but what is in it did. Offsets
// alone would have passed this and written over someone else's change.
func TestStaleRangeCatchesASameLengthChangeAtTheSameOffset(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	staged, _ := Preview(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}})
	rng := staged.Ops[0].Range

	// "nginx:1.25" -> "nginx:9.99": identical length, identical offsets.
	if err := os.WriteFile(path, []byte(strings.Replace(sample, "nginx:1.25", "nginx:9.99", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
		Expect: &Expect{Start: rng.Start, End: rng.End, Text: "nginx:1.25"},
	}}})
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("apply over a changed value returned %v, want ErrStaleRange", err)
	}
}

// A stage whose range has NOT moved still applies. The staleness check must not
// be so strict that any change to the file anywhere discards every stage — that
// would be a check nobody could work with.
func TestAnUnmovedRangeStillApplies(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	staged, _ := Preview(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}})
	rng := staged.Ops[0].Range

	// A change BELOW the target moves nothing above it.
	if err := os.WriteFile(path, []byte(sample+"\n# a trailing note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
		Expect: &Expect{Start: rng.Start, End: rng.End, Text: "nginx:1.25"},
	}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("diff is %d/%d, want 1 removed and 1 added", res.Removed, res.Added)
	}
}

// AD-8. A flow-style collection cannot take a block child, so the insert is
// refused and the file is untouched. Refusing is the whole point: an engine
// that emitted `web: {image: nginx}\n  restart: always` would produce a file
// that fails in someone else's terminal.
func TestFlowStyleInsertIsRefused(t *testing.T) {
	const flow = "services:\n  web: {image: nginx}\n"
	path := write(t, "compose.yaml", flow)
	_, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpInsertKey, At: "services.web", Key: "restart", Value: "always",
	}}})
	if !errors.Is(err, strategy.ErrFlowStyle) {
		t.Fatalf("insert into a flow mapping returned %v, want ErrFlowStyle", err)
	}
	if !Refused(err) {
		t.Error("ErrFlowStyle is not reported as a refusal, so a client would show it as a fault")
	}
	if Reason(err) != "flow-style" {
		t.Errorf("Reason = %q, want flow-style", Reason(err))
	}
	if got := read(t, path); got != flow {
		t.Error("a refused insert wrote to the file")
	}
}

// An edit that changes nothing is refused rather than written. Rewriting a file
// with its own contents dirties an editor buffer and moves an mtime for
// nothing, and the reader would reasonably read that as "Composure edited my file".
func TestAnEditThatChangesNothingIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	_, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.25",
	}}})
	if !errors.Is(err, ErrNoChange) {
		t.Fatalf("apply of an identical value returned %v, want ErrNoChange", err)
	}
	if got := read(t, path); got != sample {
		t.Error("a no-op apply rewrote the file")
	}
}

// ------------------------------------------------------------- insert ------

// Story 5.2's last acceptance criterion, executed. Clicking an unset key stages
// this operation; here it lands.
func TestInsertKeyAddsOneLineAndNothingElse(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	res, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpInsertKey, At: "services.web", Key: "restart", Value: "unless-stopped",
	}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Removed != 0 || res.Added != 1 {
		t.Errorf("insert produced %d removed / %d added, want 0 and 1:\n%s", res.Removed, res.Added, res.Diff)
	}
	after := read(t, path)
	if !strings.Contains(after, "    restart: unless-stopped") {
		t.Errorf("the key did not land at the file's own indent:\n%s", after)
	}
	// The comment on an untouched line is untouched.
	if !strings.Contains(after, "image: nginx:1.25   # pinned deliberately") {
		t.Error("an insert disturbed a line it had no business touching")
	}
}

// Several staged edits against one file apply as one write, each re-located
// against the buffer the previous one produced. Arithmetic on the original
// offsets would put the second edit in the wrong place the moment the first one
// changed a line count.
func TestSeveralOpsApplyAsOneWrite(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertKey, At: "services.web", Key: "restart", Value: "always"},
		{Operation: OpReplaceScalar, At: "services.db.image", Value: "postgres:17"},
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := read(t, path)
	if !strings.Contains(after, "restart: always") || !strings.Contains(after, "'postgres:17'") {
		t.Errorf("both edits did not land:\n%s", after)
	}
	if res.Added != 2 || res.Removed != 1 {
		t.Errorf("diff is %d removed / %d added, want 1 and 2", res.Removed, res.Added)
	}
}

// A request either applies whole or not at all. The staleness check runs over
// every operation before the first splice, so a bad second op cannot leave the
// first one written.
func TestAStaleSecondOpLeavesTheFileUntouched(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27"},
		{Operation: OpReplaceScalar, At: "services.db.image", Value: "postgres:17",
			Expect: &Expect{Start: 0, End: 4, Text: "wrong"}},
	}})
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("got %v, want ErrStaleRange", err)
	}
	if got := read(t, path); got != sample {
		t.Error("a partially refused request wrote the operations that came before the refusal")
	}
}

// ------------------------------------------------- endings, BOM, mode ------

// CRLF survives. This is the defect the corpus caught once: a rewritten line
// ending shows up as a whole-file diff the moment core.autocrlf gets involved,
// and it is invisible in any test that compares strings after normalising.
func TestCRLFSurvivesAnEdit(t *testing.T) {
	src := "services:\r\n  web:\r\n    image: nginx:1.25   # pinned\r\n    restart: always\r\n"
	path := write(t, "compose.yaml", src)
	res, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := read(t, path)
	if strings.Count(after, "\r\n") != strings.Count(src, "\r\n") {
		t.Errorf("line endings were rewritten: %q", after)
	}
	if !strings.Contains(after, "image: nginx:1.27   # pinned\r\n") {
		t.Errorf("the edited line lost its ending or its comment: %q", after)
	}
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("diff is %d/%d, want one line each way:\n%s", res.Removed, res.Added, res.Diff)
	}
}

// A BOM-prefixed compose file is EDITED, with a two-line diff, and keeps its
// byte order mark.
//
// This assertion used to read "refused, not damaged". The locator could not
// address a path in a BOM'd document at all \u2014 goccy folds the mark into the
// first token, so `services` becomes "<BOM>services" and EVERY path in the file
// misses, not merely a path on the first line. A refusal was the correct
// failure, and the test said so, while recording that it was a limitation and
// naming exactly what it would become if the limitation went: "the edit lands
// with a two-line diff; never nothing happened".
//
// It went. See internal/strategy/bom.go: the mark is a fixed three-byte prefix
// that introduces no newline, so the parser reads the file without it and every
// byte offset is shifted back by three. This fixture is BOM *and* CRLF, and the
// CRLF half turned out to be a second, independent defect \u2014 goccy counted a
// comment line in a CRLF document as two lines, which is why this file's first
// ten comment lines put every position thirteen lines too far down.
func TestBOMComposeFileIsEditedAndKeepsItsMark(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", "e10-bom-crlf.yml"))
	if err != nil {
		t.Skipf("regression file unavailable: %v", err)
	}
	path := write(t, "compose.yaml", string(src))
	res, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}})
	if err != nil {
		t.Fatalf("a BOM'd file is addressable now and this edit should land: %v", err)
	}
	after := read(t, path)
	if !strings.Contains(after, "nginx:1.27") {
		t.Fatal("the edit reported success and did not land: this is the silent no-op the engine must never produce")
	}
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("diff is %d/%d, want one line each way:\n%s", res.Removed, res.Added, res.Diff)
	}
	// The mark is a byte of the FILE, not of the document. Losing it would
	// change the file's encoding declaration, which is the silent rewrite this
	// engine exists not to do.
	if !strings.HasPrefix(after, "\ufeff") {
		t.Error("the byte order mark did not survive the edit")
	}
	// CRLF throughout, and the trailing comment on the edited line intact.
	if strings.Count(after, "\r\n") != strings.Count(string(src), "\r\n") {
		t.Error("line endings were rewritten")
	}
	if !strings.Contains(after, "image: nginx:1.27   # pinned deliberately\r\n") {
		t.Errorf("the edited line lost its ending or its comment:\n%s", after)
	}
}

// The READ path on a BOM'd file, which nothing covered: e10 exercised only the
// write. Every path in a BOM'd document used to miss, so this is the assertion
// that the file is ADDRESSABLE, separate from anything being written.
func TestBOMComposeFileIsAddressable(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", "e13-bom-read.yml"))
	if err != nil {
		t.Skipf("regression file unavailable: %v", err)
	}
	for _, at := range []string{"services.web.image", "services.web.ports.0", "services", "name"} {
		start, end, err := strategy.Range(src, resolve.ParsePath(at))
		if err != nil {
			t.Errorf("%s: not addressable in a BOM'd file: %v", at, err)
			continue
		}
		if start < 3 {
			t.Errorf("%s: range starts at byte %d, inside the three-byte mark", at, start)
		}
		if end > len(src) {
			t.Errorf("%s: range ends at %d, past the %d byte file", at, end, len(src))
		}
	}
	// The scalar range must be the lexeme exactly \u2014 that is the assertion that
	// catches an offset three bytes out, which is what the mark costs.
	start, end, err := strategy.ScalarRange(src, resolve.ParsePath("services.web.image"))
	if err != nil {
		t.Fatalf("scalar range: %v", err)
	}
	if got := string(src[start:end]); got != "nginx:1.25" {
		t.Errorf("services.web.image spans %q, want the image lexeme", got)
	}
}

// "Line endings, the BOM, quoting style, key order, comments and blank lines
// are untouched by construction" — this package's own header, asserted for the
// case that used to break it.
//
// A delete of the file's FIRST key is the only operation that can reach the
// mark, and it is the case neither BOM test covered: e10 and e13 both open with
// seven comment lines, so no operation on them ever starts at byte 0.
// e15-bom-first-key.yml puts `services:` on line 1.
func TestDeletingTheFirstKeyLeavesTheMark(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", "e15-bom-first-key.yml"))
	if err != nil {
		t.Skipf("regression file unavailable: %v", err)
	}
	path := write(t, "compose.yaml", string(src))

	res, err := Apply(Request{File: path, Ops: []Op{{Operation: OpDeleteKey, At: "services"}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := read(t, path)
	if !strings.HasPrefix(after, "\ufeff") {
		t.Errorf("deleting the first key took the byte order mark with it; the file now begins %q", firstRunes(after))
	}
	if strings.Contains(after, "image: nginx") {
		t.Errorf("the delete did not land:\n%s", after)
	}
	// AD-14 again: the range the preview reported has to be the range the write
	// touched. A start inside the mark means they disagree about this file.
	if got := res.Ops[0].Range.Start; got < 3 {
		t.Errorf("the reported range starts at byte %d, inside the three-byte mark", got)
	}
}

func firstRunes(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ErrWouldCorrupt: the re-parse before the write, which had no test at all.
//
// It reads as belt and braces over engines that are already measured, and that
// is why it survived deletion — nothing exercised it. It is not theoretical.
// The splice engine is a byte operation and does not read the value it is given:
// handed `a: b` for an image, it writes `image: a: b`, which is a mapping value
// where YAML does not allow one. A reader typing a value with a colon in it is
// an ordinary thing to do.
//
// Two properties, and the second is the one that catches deleting the CALL
// rather than the function: nothing reaches the disk.
func TestAValueThatWouldNotReparseIsRefused(t *testing.T) {
	for _, value := range []string{
		"a: b",          // a mapping value where one is not allowed
		"- x",           // a block sequence entry in a scalar position
		`"unterminated`, // an unclosed quote
	} {
		path := write(t, "compose.yaml", sample)
		_, err := Apply(Request{File: path, Ops: []Op{{
			Operation: OpReplaceScalar, At: "services.web.image", Value: value,
		}}})
		if !errors.Is(err, ErrWouldCorrupt) {
			t.Errorf("%q was accepted (err = %v); the engine spliced it and the result does not parse", value, err)
		}
		if got := read(t, path); got != sample {
			t.Errorf("%q: the file was written anyway:\n%s", value, got)
		}
		// A refusal, not a fault: the caller reverts the field and explains.
		if !Refused(err) || Reason(err) != "would-corrupt" {
			t.Errorf("%q: refused=%v reason=%q, want a would-corrupt refusal", value, Refused(err), Reason(err))
		}
	}
}

// A file with no trailing newline keeps having none. Adding one is a diff
// nobody asked for and every reviewer notices.
func TestAMissingFinalNewlineIsNotInvented(t *testing.T) {
	src := "services:\n  web:\n    image: nginx:1.25"
	path := write(t, "compose.yaml", src)
	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after := read(t, path); strings.HasSuffix(after, "\n") {
		t.Errorf("a trailing newline was invented: %q", after)
	}
}

// The file's mode survives the temp-file-and-rename write.
func TestApplyKeepsTheFileMode(t *testing.T) {
	path := write(t, "compose.yaml", sample)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Skipf("chmod unavailable: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27",
	}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode is %v, want 0640", info.Mode().Perm())
	}
}

// ---------------------------------------------------------- dockerfile -----

const dockerfileSample = "# escape=\\\n" +
	"FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build  # pinned\n" +
	"WORKDIR /src\n" +
	"RUN go build \\\n" +
	"      -o /out/app \\\n" +
	"      ./cmd/app\n" +
	"\n" +
	"from alpine:3.19 as runtime\n" +
	"COPY --from=build /out/app /app\n"

// R7.2. Changing a FROM preserves --platform, the AS clause, the keyword's
// casing and any trailing comment, and exactly one line changes.
func TestSetBaseImageChangesExactlyOneLine(t *testing.T) {
	path := write(t, "Dockerfile", dockerfileSample)
	res, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpSetBaseImage, Stage: 0, Value: "golang:1.24-alpine",
	}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Removed != 1 || res.Added != 1 {
		t.Errorf("diff is %d removed / %d added, want 1 and 1:\n%s", res.Removed, res.Added, res.Diff)
	}
	want := "FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build  # pinned\n"
	if !strings.Contains(read(t, path), want) {
		t.Errorf("the line was not rebuilt as written:\n%s", read(t, path))
	}
}

// Lower-case `from` stays lower-case. Normalising casing is a diff nobody asked
// for, and it is the kind that turns a one-line review into an argument.
func TestSetBaseImageKeepsKeywordCasing(t *testing.T) {
	path := write(t, "Dockerfile", dockerfileSample)
	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpSetBaseImage, Stage: 1, Value: "alpine:3.20",
	}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(read(t, path), "from alpine:3.20 as runtime\n") {
		t.Errorf("casing was normalised:\n%s", read(t, path))
	}
}

// R7.4. An edit that would need to reflow a continuation block is refused
// rather than silently reformatted.
func TestMultiLineInstructionRewriteIsRefused(t *testing.T) {
	path := write(t, "Dockerfile", dockerfileSample)
	// Instruction 3 is the three-line `RUN go build \ ... \ ...`.
	f := dockerfile.Parse([]byte(dockerfileSample))
	idx := -1
	for i, in := range f.Instructions {
		if in.Name == "RUN" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("fixture has no RUN")
	}
	_, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceArgs, Instruction: idx, Value: "go build ./...",
	}}})
	if !errors.Is(err, dockerfile.ErrMultiLine) {
		t.Fatalf("multi-line rewrite returned %v, want ErrMultiLine", err)
	}
	if !Refused(err) || Reason(err) != "multi-line" {
		t.Errorf("multi-line is not reported as a refusal with a reason slug: refused=%v reason=%q", Refused(err), Reason(err))
	}
	if got := read(t, path); got != dockerfileSample {
		t.Error("a refused rewrite wrote to the file")
	}
}

// R7.4 AT THIS LAYER, not at the engine's.
//
// `TestMultiLineInstructionRewriteIsRefused` above goes through Apply, and
// Apply reaches TWO guards: `locate` here and `dockerfile.ReplaceArgs` under
// it. Either one alone refuses, so either one could be deleted with a green
// suite. This test asks `locate` on its own — the range half of AD-14, which
// the extension's preview and its staleness pre-pass both use before any
// engine call — so deleting the ErrMultiLine branch in `locate` fails HERE
// while `dockerfile`'s own test covers the other side.
//
// It matters beyond tidiness: `locate` is what answers "what would this edit
// touch". Without the guard it happily reports a range spanning a whole
// continuation block, and the preview shown to the reader describes a rewrite
// the engine will then refuse.
func TestLocateRefusesAMultiLineReplaceArgs(t *testing.T) {
	src := []byte(dockerfileSample)
	f := dockerfile.Parse(src)

	multi, single := -1, -1
	for i, in := range f.Instructions {
		if in.Kind != dockerfile.KindInstruction {
			continue
		}
		if in.EndLine != in.StartLine && multi < 0 {
			multi = i
		}
		if in.EndLine == in.StartLine && in.Name == "WORKDIR" {
			single = i
		}
	}
	if multi < 0 || single < 0 {
		t.Fatalf("fixture needs one continuation block and one single-line instruction (multi=%d single=%d)", multi, single)
	}

	if _, err := locate(src, Op{Operation: OpReplaceArgs, Instruction: multi, Value: "go build ./..."}); !errors.Is(err, dockerfile.ErrMultiLine) {
		t.Fatalf("locate on a continuation block returned %v, want ErrMultiLine", err)
	}

	// The other direction: a guard that refused every instruction would pass
	// the assertion above and take the feature with it.
	rng, err := locate(src, Op{Operation: OpReplaceArgs, Instruction: single, Value: "/app"})
	if err != nil {
		t.Fatalf("locate on a single-line instruction: %v", err)
	}
	if got := string(src[rng.Start:rng.End]); got != "WORKDIR /src" {
		t.Errorf("located range reads %q, want the WORKDIR line", got)
	}
}

// A CRLF Dockerfile with a custom escape character and a heredoc survives a
// base-image change. Each of these is a documented way to corrupt a Dockerfile.
func TestDockerfileQuirksSurviveABaseImageChange(t *testing.T) {
	src := "\ufeff# escape=`\r\n" +
		"FROM mcr.microsoft.com/windows/servercore:ltsc2019\r\n" +
		"RUN <<EOF\r\n" +
		"echo hello\r\n" +
		"EOF\r\n"
	path := write(t, "Dockerfile", src)
	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpSetBaseImage, Stage: 0, Value: "mcr.microsoft.com/windows/servercore:ltsc2022",
	}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := read(t, path)
	if !strings.HasPrefix(after, "\ufeff") {
		t.Error("the BOM did not survive")
	}
	if strings.Count(after, "\r\n") != strings.Count(src, "\r\n") {
		t.Errorf("line endings were rewritten:\n%q", after)
	}
	if !strings.Contains(after, "# escape=`") || !strings.Contains(after, "echo hello") {
		t.Errorf("the directive or the heredoc body was damaged:\n%q", after)
	}
	if !strings.Contains(after, "servercore:ltsc2022") {
		t.Error("the edit did not land")
	}
}

// ------------------------------------------------------------- corpus ------

// The sweep. For every real compose file that declares a plain `image:` scalar,
// stage one edit through this package and assert the two properties the product
// is sold on: the diff is two lines, and the file still parses.
//
// This is editbench's criterion reached through the write path rather than
// through the engine directly. editbench proves the engine splices minimally;
// this proves that nothing between the reader's keystroke and the disk added a
// second opinion.
func TestCorpusScalarEditIsAlwaysTwoLines(t *testing.T) {
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files, err := corpus.Collect(corpusRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) == 0 {
		// NOT a skip. `corpus-repos/` absent is a legitimate skip — a fresh
		// clone has not fetched it. `corpus-repos/` present and empty is a
		// BROKEN CACHE: a CI restore that produced a directory and no files,
		// which skipped this sweep and turned the step green while proving
		// exactly nothing. Failing here is the difference between "we did not
		// run the corpus check" and "the corpus check passed".
		t.Fatalf("%s exists but holds no compose files — a broken corpus, not an absent one; run `make corpus`", corpusRoot)
	}

	dir := t.TempDir()
	var attempted, applied int
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		path, ok := firstScalarImage(src)
		if !ok {
			continue
		}
		attempted++

		// Copied, because a benchmark that edits the corpus in place would
		// invalidate every later run of `make gate` on this machine.
		work := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(work, src, 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Apply(Request{File: work, Ops: []Op{{
			Operation: OpReplaceScalar, At: path, Value: "example.invalid/replacement:v9.9.9",
		}}})
		if err != nil {
			if Refused(err) {
				continue // a refusal is a correct outcome, and it wrote nothing
			}
			t.Errorf("%s: %v", file, err)
			continue
		}
		applied++

		if res.Removed != 1 || res.Added != 1 {
			t.Errorf("%s: diff is %d removed / %d added, want one line each way:\n%s",
				file, res.Removed, res.Added, res.Diff)
		}
		after, err := os.ReadFile(work)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := goccyparser.ParseBytes(after, goccyparser.ParseComments); err != nil {
			t.Errorf("%s: the written file no longer parses: %v", file, err)
		}
		if !strings.Contains(string(after), "example.invalid/replacement:v9.9.9") {
			t.Errorf("%s: the edit did not land", file)
		}
		// Everything except the edited line is byte-identical. This is the
		// property the whole architecture exists to guarantee, asserted rather
		// than inferred from a line count.
		if !sameExceptOneLine(string(src), string(after)) {
			t.Errorf("%s: more than one line differs after a scalar edit", file)
		}
	}
	if applied == 0 {
		t.Fatalf("no corpus file was edited; the sweep proved nothing (%d attempted)", attempted)
	}
	t.Logf("corpus: %d files carried a plain image scalar, %d edited, all two-line diffs", attempted, applied)
}

// The same sweep for the other grammar. `make gate` holds dockerbench's five
// metrics; this holds the write path that sits on top of it.
func TestCorpusBaseImageEditIsAlwaysOneLine(t *testing.T) {
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files := collectDockerfiles(corpusRoot)
	if len(files) == 0 {
		t.Skip("corpus holds no Dockerfiles")
	}

	dir := t.TempDir()
	var attempted, applied int
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		f := dockerfile.Parse(src)
		stages := f.Stages()
		if len(stages) == 0 || f.Instructions[stages[0]].ImageRef == "" {
			continue
		}
		if f.Instructions[stages[0]].ImageRef == "example.invalid/base:v9" {
			continue
		}
		attempted++

		work := filepath.Join(dir, "Dockerfile")
		if err := os.WriteFile(work, src, 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Apply(Request{File: work, Ops: []Op{{
			Operation: OpSetBaseImage, Stage: 0, Value: "example.invalid/base:v9",
		}}})
		if err != nil {
			if Refused(err) {
				continue
			}
			t.Errorf("%s: %v", file, err)
			continue
		}
		applied++
		if res.Removed != 1 || res.Added != 1 {
			t.Errorf("%s: diff is %d removed / %d added, want one line each way:\n%s",
				file, res.Removed, res.Added, res.Diff)
		}
		after, err := os.ReadFile(work)
		if err != nil {
			t.Fatal(err)
		}
		if !sameExceptOneLine(string(src), string(after)) {
			t.Errorf("%s: more than one line differs after a base-image change", file)
		}
		// The stage structure is unchanged: an edit that added or lost a stage
		// would still pass a line count.
		if got := len(dockerfile.Parse(after).Stages()); got != len(stages) {
			t.Errorf("%s: stage count moved from %d to %d", file, len(stages), got)
		}
	}
	if applied == 0 {
		t.Fatalf("no corpus Dockerfile was edited; the sweep proved nothing (%d attempted)", attempted)
	}
	t.Logf("corpus: %d Dockerfiles with a locatable base image, %d edited, all one-line changes", attempted, applied)
}

// sameExceptOneLine reports whether a and b differ in exactly one line at the
// same position, and agree everywhere else byte for byte.
func sameExceptOneLine(a, b string) bool {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(al) != len(bl) {
		return false
	}
	diffs := 0
	for i := range al {
		if al[i] != bl[i] {
			diffs++
		}
	}
	return diffs == 1
}

// firstScalarImage finds a `services.<name>.image` holding a plain string, the
// same target editbench uses so the two measure the same operation.
func firstScalarImage(src []byte) (string, bool) {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 {
		return "", false
	}
	root, ok := f.Docs[0].Body.(*goccyast.MappingNode)
	if !ok {
		return "", false
	}
	for _, kv := range root.Values {
		if kv.Key.GetToken().Value != "services" {
			continue
		}
		svcs, ok := kv.Value.(*goccyast.MappingNode)
		if !ok {
			return "", false
		}
		for _, s := range svcs.Values {
			body, ok := s.Value.(*goccyast.MappingNode)
			if !ok {
				continue
			}
			for _, field := range body.Values {
				if field.Key.GetToken().Value != "image" {
					continue
				}
				if _, ok := field.Value.(*goccyast.StringNode); !ok {
					continue
				}
				// Rendered through resolve.Path so a service name holding a dot
				// survives the round trip into and back out of the request.
				return resolve.Path{"services", s.Key.GetToken().Value, "image"}.String(), true
			}
		}
	}
	return "", false
}

func collectDockerfiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		n := d.Name()
		if n == "Dockerfile" || n == "Containerfile" ||
			strings.HasPrefix(n, "Dockerfile.") || strings.HasSuffix(n, ".Dockerfile") {
			out = append(out, p)
		}
		return nil
	})
	return out
}
