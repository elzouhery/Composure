package edit

// Stories 7.3 and 7.4: adding a service, and declaring a network, a volume, a
// config or a secret.
//
// The planner under test writes nothing itself — it turns "the reader named a
// service" into the operations the splice engine already performs, and refuses
// before a single one of them is staged when the answer would be a damaged or
// a guessed-at file.
//
// Every positive assertion here is on the BYTES the file holds afterwards, and
// every one of them checks the whole file rather than the inserted line: a test
// that asserts the service arrived and not that every other byte is identical
// is the trap this epic exists to avoid. `assertExcisionRestores`
// (structural_test.go) is the mechanical form of it.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// fixture copies a repository fixture into a temp file and returns its path and
// its original bytes. The fixtures are the ones the story asks for and they are
// permanent files, not literals in a test that only this test can see.
func fixture(t *testing.T, name string) (string, string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, string(src)
}

// ------------------------------------------------------- story 7.3 ---------

// The story's own example, and its first criterion: ONE request carrying TWO
// operations. Not a service written now and an image written afterwards.
func TestPlanAddServiceIsOneRequestOfTwoOperations(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []Op{
		{Operation: OpInsertKey, At: "services", Key: "cache"},
		{Operation: OpInsertKey, At: "services.cache", Key: "image", Value: "redis:7"},
	}
	if len(ops) != len(want) {
		t.Fatalf("Plan returned %d operations, want %d: %+v", len(ops), len(want), ops)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Errorf("operation %d is %+v, want %+v", i, ops[i], want[i])
		}
	}

	// And it applies as one edit: one diff, one write, one undo.
	res, err := Apply(Request{File: path, Ops: ops})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	const wantFile = "services:\n  web:\n    image: nginx\n  cache:\n    image: redis:7\n"
	if got := read(t, path); got != wantFile {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, wantFile)
	}
	if res.Added != 2 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +2 -0 in ONE diff", res.Added, res.Removed)
	}
}

// R4.4 and the placement criterion: the file's own indentation, taken from the
// engine's inference, not from a constant here. A four-space file lands the
// service at 4 and its keys at 8.
func TestAddServiceTakesTheFilesOwnIndentation(t *testing.T) {
	path, src := fixture(t, "e3-four-space.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: ops}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := read(t, path)
	if !strings.Contains(got, "\n    cache:\n        image: redis:7\n") {
		t.Errorf("the service did not land at the file's own indentation (4 and 8):\n%s", got)
	}
	assertExcisionRestores(t, "e3-four-space.yml", []byte(src), []byte(got))
}

// The empty-stack case. EXPERIENCE.md's state table promises `Add service` is
// the one action on a stack with nothing in it, and a `services:` with no
// children has no sibling to copy an indent from — the engine falls back to the
// file's dominant step.
func TestAddServiceIntoAnEmptyServicesBlock(t *testing.T) {
	path, src := fixture(t, "e35-empty-services.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: ops}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := read(t, path)
	if !strings.HasSuffix(got, "services:\n  cache:\n    image: redis:7\n") {
		t.Errorf("an empty stack is a dead end, not a starting point:\n%s", got)
	}
	assertExcisionRestores(t, "e35-empty-services.yml", []byte(src), []byte(got))
}

// A file with no `services:` at all: story 7.2's root insert is what makes this
// reachable, and it is still ONE request — three operations, one diff.
func TestAddServiceCreatesTheServicesBlockWhenItIsAbsent(t *testing.T) {
	const src = "name: demo\nnetworks:\n  frontend:\n"
	path := write(t, "compose.yaml", src)

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(ops) != 3 || ops[0].At != "" || ops[0].Key != "services" {
		t.Fatalf("want a root insert first, got %+v", ops)
	}
	res, err := Apply(Request{File: path, Ops: ops})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	const want = "name: demo\nnetworks:\n  frontend:\nservices:\n  cache:\n    image: redis:7\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 3 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +3 -0", res.Added, res.Removed)
	}
}

// Every other byte, on a file that has something to lose: an anchor, a merge
// key, two comment styles, a blank line between services and a top-level block
// after them.
func TestAddServiceLeavesEveryOtherByteAlone(t *testing.T) {
	path, src := fixture(t, "e36-service-trailing-comment.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: ops}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := read(t, path)
	assertExcisionRestores(t, "e36-service-trailing-comment.yml", []byte(src), []byte(got))
	// The comment that belonged to `db`'s volumes stays inside `db`, and the
	// new service is a sibling of the existing ones rather than a child.
	if !strings.Contains(got, "\n  cache:\n    image: redis:7\n") {
		t.Errorf("the service is not a sibling at the services indent:\n%s", got)
	}
	if _, err := (strategy.Splice{}).Identity([]byte(got)); err != nil {
		t.Errorf("the result does not parse: %v", err)
	}
	// And it is genuinely a new service in the merged model, not a stray key.
	p, err := resolve.File(path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	names := p.Services().Keys()
	if strings.Join(names, ",") != "web,db,cache" {
		t.Errorf("services are %v, want web,db,cache appended in order", names)
	}
}

// CRLF, because every new line in this epic is written into a file whose ending
// is the file's own (story 7.1) and a two-operation insert is where a
// half-fixed implementation shows.
func TestAddServiceKeepsCRLF(t *testing.T) {
	src := "services:\r\n  web:\r\n    image: nginx\r\n"
	path := write(t, "compose.yaml", src)

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: ops}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "services:\r\n  web:\r\n    image: nginx\r\n  cache:\r\n    image: redis:7\r\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

// A flow-style `services:` cannot take a block child. The criterion is that the
// new surface REACHES the existing refusal rather than routing around it.
func TestAddServiceIntoAFlowMappingIsRefused(t *testing.T) {
	path, src := fixture(t, "e33-services-flow.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"})
	if err != nil {
		t.Fatalf("Plan refused before the engine could: %v", err)
	}
	_, err = Apply(Request{File: path, Ops: ops})
	if err == nil {
		t.Fatal("Apply succeeded on a flow-style services mapping")
	}
	if !errors.Is(err, strategy.ErrFlowStyle) {
		t.Errorf("error is %v, want ErrFlowStyle", err)
	}
	if !Refused(err) || Reason(err) != "flow-style" {
		t.Errorf("Refused=%v Reason=%q; the reader would be shown a fault", Refused(err), Reason(err))
	}
	if got := read(t, path); got != src {
		t.Error("a refusal wrote to the file")
	}
}

// A duplicate name is refused BEFORE anything is staged, naming the file and
// the line that already declares it. YAML's last-one-wins would otherwise
// discard one of the two services silently.
//
// The fixture has three services and the collision is on the middle one: a
// one-service fixture cannot tell a real lookup from "the file has a service".
func TestAddDuplicateServiceNameIsRefused(t *testing.T) {
	path, src := fixture(t, "e4-commented-service.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "legacy", Value: "redis:7"})
	if err == nil {
		t.Fatalf("Plan returned %d operations for a name the file already declares", len(ops))
	}
	if len(ops) != 0 {
		t.Errorf("a refusal returned %d operations; nothing may be staged", len(ops))
	}
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("error is %v, want ErrDuplicateName", err)
	}
	if !Refused(err) || Reason(err) != "duplicate-name" {
		t.Errorf("Refused=%v Reason=%q", Refused(err), Reason(err))
	}
	// The line that already declares it: `legacy:` is line 7 of the fixture.
	if !strings.Contains(err.Error(), "compose.yaml:7") {
		t.Errorf("the refusal does not name the file and line that declares it: %v", err)
	}
	// A name that is NOT taken, in the same file, still plans — otherwise the
	// check above passes for the wrong reason.
	if _, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: "redis:7"}); err != nil {
		t.Errorf("a free name was refused: %v", err)
	}
	if got := read(t, path); got != src {
		t.Error("planning wrote to the file")
	}
}

// The name may be taken by ANOTHER file in the project. The write still lands
// in one file, but the collision is in the merged configuration, and that is
// where last-one-wins actually happens.
func TestADuplicateDeclaredInAnotherFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	over := filepath.Join(dir, "compose.override.yaml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  cache:\n    image: redis:7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := resolve.Files(base, over)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Plan(Add{File: base, Kind: "service", Name: "cache", Value: "redis:8", Merged: project})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("error is %v, want ErrDuplicateName", err)
	}
	if !strings.Contains(err.Error(), "compose.override.yaml:2") {
		t.Errorf("the refusal does not name the OTHER file and line: %v", err)
	}
}

// A service name with no image is not a valid stack, and a compose file with a
// bare `cache:` under services is what a partial write looks like. Refused
// rather than written.
func TestAServiceWithNoImageIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache"})
	if err == nil {
		t.Fatalf("Plan accepted a service with no image: %+v", ops)
	}
	if !errors.Is(err, ErrNoImage) || !Refused(err) || Reason(err) != "no-image" {
		t.Errorf("err=%v Refused=%v Reason=%q, want a no-image refusal", err, Refused(err), Reason(err))
	}
}

// R4.1's quoting contract: a new value has no style to preserve, so the tool
// imposes none — and refuses rather than inventing one for a value that would
// not survive as a bare scalar.
func TestValuesThatWouldNotSurviveBareAreRefused(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")

	refused := map[string]string{
		"a float YAML would round":  "3.10",
		"a boolean in YAML 1.1":     "yes",
		"a boolean":                 "true",
		"a comment marker":          "redis #latest",
		"a trailing space":          "redis:7 ",
		"a leading space":           " redis:7",
		"a mapping indicator":       "a: b",
		"a leading dash":            "-redis",
		"a block sequence":          "- redis",
		"a line break":              "redis\n7",
		"a flow mapping":            "{a: b}",
		"a date":                    "2026-08-13",
		"a null":                    "~",
		"an alias":                  "*defaults",
		"a folded block":            ">",
		"an anchor":                 "&x",
		"a tab":                     "redis\t7",
		"a percent directive start": "%YAML",
	}
	for what, value := range refused {
		ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: value})
		if err == nil {
			t.Errorf("%s (%q) was accepted: %+v", what, value, ops)
			continue
		}
		if !errors.Is(err, ErrNeedsQuoting) {
			t.Errorf("%s (%q): err=%v, want ErrNeedsQuoting", what, value, err)
			continue
		}
		if !Refused(err) || Reason(err) != "needs-quoting" {
			t.Errorf("%s: Refused=%v Reason=%q", what, Refused(err), Reason(err))
		}
		// The refusal has to tell the reader what to do about it, and the one
		// thing that works is quoting it themselves.
		if !strings.Contains(err.Error(), "quot") {
			t.Errorf("%s: the refusal does not say quoting it will work: %v", what, err)
		}
	}

	accepted := map[string]string{
		"an ordinary tag":       "redis:7",
		"a tag with a dash":     "postgres:15-alpine",
		"a registry path":       "ghcr.io/org/app:1.2.3",
		"a digest":              "nginx@sha256:abc123",
		"the reader's quoting":  `"3.10"`,
		"single quotes":         `'yes'`,
		"a word":                "alpine",
		"an interpolated value": "${IMAGE}",
	}
	for what, value := range accepted {
		if _, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: value}); err != nil {
			t.Errorf("%s (%q) was refused: %v", what, value, err)
		}
	}
}

// What the reader typed is what the file gets — the quoting they chose
// included, and nothing added around it.
func TestAQuotedValueIsWrittenExactlyAsTyped(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "cache", Value: `"3.10"`})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: ops}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n  cache:\n    image: \"3.10\"\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
}

// A name is a key and gets the same treatment: nothing is quoted for the reader
// and nothing that would land as something other than what they typed is
// written.
func TestANameThatWouldNotSurviveBareIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")

	for _, name := range []string{"3.10", "yes", "a: b", "web ", "#cache", ""} {
		ops, err := Plan(Add{File: path, Kind: "service", Name: name, Value: "redis:7"})
		if err == nil {
			t.Errorf("the name %q was accepted: %+v", name, ops)
			continue
		}
		if !Refused(err) {
			t.Errorf("the name %q: %v is not classified as a refusal", name, err)
		}
	}
}

// A name is written as a KEY, and YAML caps an implicit key at 1024 characters.
//
// The defect this pins: `bare` tested the name as a VALUE — `yaml.Unmarshal("x:
// " + text)` — where nothing is capped. A 1030-character service name passed
// every check, was written, and produced `a.yml:4:3: yaml: line 4: could not
// find expected ':'` from the resolver on the very next command. Exit 0, "2
// lines added", a file this product could no longer read.
//
// Note what the test asserts on the accepting side: 1024 characters WORK. A
// refusal that fired one character early would satisfy "the bug is gone" and
// take a legal name away with it.
func TestANameTooLongToBeAKeyIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")

	for _, tc := range []struct {
		kind, value string
	}{
		{"service", "nginx"},
		{"network", ""}, // story 7.4's resource names are keys too
	} {
		name := strings.Repeat("n", implicitKeyLimit+6)
		ops, err := Plan(Add{File: path, Kind: tc.kind, Name: name, Value: tc.value})
		if err == nil {
			// Prove the damage rather than merely reporting acceptance: this is
			// the failure mode that does not crash.
			if _, aerr := Apply(Request{File: path, Ops: ops}); aerr == nil {
				t.Fatalf("%s: a %d-character name was accepted and written; the file now reads:\n%s",
					tc.kind, len(name), read(t, path))
			}
			t.Errorf("%s: a %d-character name was planned; only the write path refused it", tc.kind, len(name))
			continue
		}
		if !errors.Is(err, ErrNameTooLong) {
			t.Errorf("%s: err=%v, want ErrNameTooLong", tc.kind, err)
		}
		if !Refused(err) || Reason(err) != "name-too-long" {
			t.Errorf("%s: Refused=%v Reason=%q, want a name-too-long refusal", tc.kind, Refused(err), Reason(err))
		}
		// The advice every other refusal here gives is false for this one, and
		// a refusal offering a workaround that does not work is worse than none.
		if strings.Contains(err.Error(), "Quote it yourself") {
			t.Errorf("%s: the refusal tells the reader to quote it, which does not lift the limit: %v", tc.kind, err)
		}
		if !strings.Contains(err.Error(), "shorten") {
			t.Errorf("%s: the refusal does not say what would work: %v", tc.kind, err)
		}
	}

	// The legal side of the boundary, exactly at the limit, written and read
	// back — the fixture testdata/edge/e41-key-at-implicit-limit.yml is the
	// permanent form of it.
	atLimit := strings.Repeat("m", implicitKeyLimit)
	ops, err := Plan(Add{File: path, Kind: "service", Name: atLimit, Value: "nginx"})
	if err != nil {
		t.Fatalf("a %d-character name is legal YAML and was refused: %v", implicitKeyLimit, err)
	}
	if _, err := Apply(Request{File: path, Ops: mustStage(t, path, ops)}); err != nil {
		t.Fatalf("a %d-character name was refused by the write path: %v", implicitKeyLimit, err)
	}
	if _, _, err := strategy.Locate([]byte(read(t, path)), []string{"services", atLimit}); err != nil {
		t.Fatalf("the service is not addressable after the write: %v", err)
	}
}

// The same defect one layer down: even if the planner never saw the name — a
// client sending `insert_key` over the RPC, a future caller — the write path
// itself must not put a document on disk that yaml.v3 cannot read.
//
// This is the check that makes the planner's refusal a convenience rather than
// the only thing standing between the reader and a broken file.
func TestAnInsertThatWouldBreakYAMLv3IsRefusedByTheWritePath(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)

	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertKey, At: "services", Key: strings.Repeat("n", implicitKeyLimit+6)},
	}})
	if err == nil {
		t.Fatalf("the write path accepted an unreadable key; the file now reads:\n%s", read(t, path))
	}
	// name-too-long, not would-corrupt: the key is now checked in the
	// insert_key arm of the op validation, which runs BEFORE the splice and
	// before validate's re-parse. That is the better refusal — it names the
	// limit and the count instead of reporting that the result would not
	// parse — and it is what makes this an op-level guarantee rather than a
	// property of one planner. validate's yaml.v3 arm still stands behind it
	// and is pinned directly by TestValidateRefusesWhatOnlyYAMLv3Rejects,
	// because no operation can reach it any more.
	if !errors.Is(err, ErrNameTooLong) || !Refused(err) || Reason(err) != "name-too-long" {
		t.Errorf("err=%v Refused=%v Reason=%q, want a name-too-long refusal", err, Refused(err), Reason(err))
	}
	if got := read(t, path); got != src {
		t.Errorf("the file was written anyway:\n%s", got)
	}
}

// `foo.bar` and `v1.2` are legal compose service names — the vendored schema's
// own pattern is ^[a-zA-Z0-9._-]+$ — and they must WORK, not merely be refused
// politely. Before the fix the plan's second operation joined its path with a
// `.`, ParsePath split it back into the wrong segments, and the reader was told
// `path segment "foo" not found`: a fault, for a well-formed request.
func TestADottedServiceNameIsAddedAndAddressable(t *testing.T) {
	for _, name := range []string{"foo.bar", "v2.1", "a.b.c"} {
		path, original := fixture(t, "e40-dotted-names.yml")

		ops, err := Plan(Add{File: path, Kind: "service", Name: name, Value: "redis:7"})
		if err != nil {
			t.Fatalf("%s: Plan: %v", name, err)
		}
		// The path the second operation carries has to survive ParsePath as two
		// segments, and it is the path TYPE that guarantees it — not a rule
		// invented in the planner.
		if got := resolve.ParsePath(ops[len(ops)-1].At); len(got) != 2 || got[0] != "services" || got[1] != name {
			t.Fatalf("%s: the image operation addresses %v, want [services %s]", name, got, name)
		}
		res, err := Apply(Request{File: path, Ops: mustStage(t, path, ops)})
		if err != nil {
			t.Fatalf("%s: Apply: %v", name, err)
		}
		assertExcisionRestores(t, "e40-dotted-names.yml", []byte(original), []byte(read(t, path)))
		if res.Added != 2 || res.Removed != 0 {
			t.Errorf("%s: diff is +%d -%d, want +2 -0:\n%s", name, res.Added, res.Removed, res.Diff)
		}
		after := []byte(read(t, path))
		if _, _, err := strategy.Locate(after, []string{"services", name, "image"}); err != nil {
			t.Errorf("%s: the image did not land under the service: %v", name, err)
		}
		// And the service the file already had under a dotted name is untouched
		// and still addressable: the fix must not have moved anything.
		if _, _, err := strategy.Locate(after, []string{"services", "api.gateway", "image"}); err != nil {
			t.Errorf("%s: the existing dotted service is no longer addressable: %v", name, err)
		}
		// A dotted name the file already declares is still a duplicate. The
		// check reads the file's own bytes, and a name it cannot address is a
		// name it cannot refuse — which would write the second `v1.2:` YAML's
		// last-one-wins then discards in silence.
		if _, err := Plan(Add{File: path, Kind: "service", Name: "v1.2", Value: "redis:7"}); !errors.Is(err, ErrDuplicateName) {
			t.Errorf("adding v1.2 twice: err=%v, want ErrDuplicateName", err)
		}
	}
}

// A dotted RESOURCE name, story 7.4's half of the same path.
func TestADottedNetworkNameIsDeclared(t *testing.T) {
	path, original := fixture(t, "e40-dotted-names.yml")

	ops, err := Plan(Add{File: path, Kind: "network", Name: "back.end"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: mustStage(t, path, ops)}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertExcisionRestores(t, "e40-dotted-names.yml", []byte(original), []byte(read(t, path)))
	if _, _, err := strategy.Locate([]byte(read(t, path)), []string{"networks", "back.end"}); err != nil {
		t.Errorf("the network is not addressable after the write: %v", err)
	}
	// A duplicate of a dotted name is still caught: the duplicate check reads
	// the file's own bytes, and a name it cannot address is a name it cannot
	// refuse.
	if _, err := Plan(Add{File: path, Kind: "network", Name: "front.end"}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("declaring front.end twice: err=%v, want ErrDuplicateName", err)
	}
}

// ------------------------------------------------------- story 7.4 ---------

// The four resource kinds are one code path with a different first segment. The
// test enumerates them rather than picking one, because "no per-resource
// branch" is the criterion.
func TestDeclareEveryResourceKind(t *testing.T) {
	for _, tc := range []struct{ kind, block string }{
		{"network", "networks"},
		{"volume", "volumes"},
		{"config", "configs"},
		{"secret", "secrets"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			src := "services:\n  web:\n    image: nginx\n" + tc.block + ":\n  existing:\n"
			path := write(t, "compose.yaml", src)

			ops, err := Plan(Add{File: path, Kind: tc.kind, Name: "frontend"})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if len(ops) != 1 {
				t.Fatalf("want one operation into the existing block, got %+v", ops)
			}
			if ops[0].At != tc.block || ops[0].Key != "frontend" || ops[0].Value != "" {
				t.Fatalf("operation is %+v, want an insert of frontend under %s with no value", ops[0], tc.block)
			}
			if _, err := Apply(Request{File: path, Ops: ops}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			want := src + "  frontend:\n"
			got := read(t, path)
			if got != want {
				t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
			}
			// No invented body, and no trailing space: `frontend:` and nothing
			// else. DECISIONS.md 17.
			if strings.Contains(got, " \n") {
				t.Errorf("a line ends in a space: %q", got)
			}
			if strings.Contains(got, "driver") {
				t.Errorf("a default was invented: %q", got)
			}
		})
	}
}

// The block is often absent, which is what earns 7.4 its own story: the
// top-level insert from 7.2 and the entry, as one request.
func TestDeclareANetworkWhenTheBlockIsAbsent(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)

	ops, err := Plan(Add{File: path, Kind: "network", Name: "frontend"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("want the block and the entry as two operations, got %+v", ops)
	}
	res, err := Apply(Request{File: path, Ops: ops})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\nnetworks:\n  frontend:\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 2 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +2 -0 in one diff", res.Added, res.Removed)
	}
	assertExcisionRestores(t, "block-absent", []byte(src), []byte(read(t, path)))
}

// A resource takes no value. Passing one is a caller error, not a silent
// `frontend: something`.
func TestAResourceTakesNoValue(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")
	if _, err := Plan(Add{File: path, Kind: "network", Name: "frontend", Value: "bridge"}); err == nil {
		t.Error("a resource accepted a value; `networks.frontend: bridge` is not a declaration")
	}
}

func TestDeclaringIntoAFlowBlockIsRefused(t *testing.T) {
	path, src := fixture(t, "e33-services-flow.yml")

	ops, err := Plan(Add{File: path, Kind: "network", Name: "backend"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	_, err = Apply(Request{File: path, Ops: ops})
	if !errors.Is(err, strategy.ErrFlowStyle) {
		t.Fatalf("error is %v, want ErrFlowStyle", err)
	}
	if Reason(err) != "flow-style" {
		t.Errorf("Reason is %q", Reason(err))
	}
	if got := read(t, path); got != src {
		t.Error("a refusal wrote to the file")
	}
}

func TestADuplicateResourceNameIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\nnetworks:\n  frontend:\n  backend:\n")
	_, err := Plan(Add{File: path, Kind: "network", Name: "backend"})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("error is %v, want ErrDuplicateName", err)
	}
	if !strings.Contains(err.Error(), "compose.yaml:6") {
		t.Errorf("the refusal does not name the line that declares it: %v", err)
	}
}

func TestAnUnknownKindIsRejected(t *testing.T) {
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")
	if _, err := Plan(Add{File: path, Kind: "deployment", Name: "x"}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("error is %v, want ErrUnknownKind", err)
	}
	// Every kind the package advertises actually plans, so the list and the
	// implementation cannot drift.
	for _, kind := range AddKinds {
		value := ""
		if kind == "service" {
			value = "redis:7"
		}
		if _, err := Plan(Add{File: path, Kind: kind, Name: "thing", Value: value}); err != nil {
			t.Errorf("AddKinds advertises %q and Plan refuses it: %v", kind, err)
		}
	}
}

// ----------------------------------------------------- staged round-trip ----

// stage is what the extension host does to a plan before the reader ever
// presses Save — `panel.ts stageAll`, in seventeen lines instead of a browser.
//
// It matters that this is a FUNCTION and that the corpus sweep below calls it,
// because the difference between it and a bare `Apply(ops)` is the difference
// between the path the CLI takes and the path every edit made in the UI takes.
// A plan applied without Expect skips the staleness comparison entirely, so a
// sweep built on `Apply(ops)` measures a code path no reader is ever on.
//
// The Expect it records is the one the panel records: the range `stack/preview`
// reported for that operation, which for operation N is a range in the buffer
// operations 0..N-1 produced.
// It returns the preview's error rather than failing, because the corpus sweep
// has to tell a refusal (which it counts) from a fault (which it fails on) at
// the preview step exactly as it does at the apply step.
func stage(file string, ops []Op) ([]Op, error) {
	res, err := Preview(Request{File: file, Ops: ops})
	if err != nil {
		return nil, err
	}
	staged := make([]Op, len(ops))
	for i, op := range ops {
		staged[i] = op
		if i < len(res.Ops) {
			landed := res.Ops[i]
			staged[i].Expect = &Expect{
				Start: landed.Range.Start,
				End:   landed.Range.End,
				Text:  landed.Before,
			}
		}
	}
	return staged, nil
}

// mustStage is stage for a test that has already decided the plan is applicable.
func mustStage(t *testing.T, file string, ops []Op) []Op {
	t.Helper()
	staged, err := stage(file, ops)
	if err != nil {
		t.Fatalf("previewing the plan failed: %v", err)
	}
	return staged
}

// The reported bug, end to end through the write path: plan a service, stage it
// the way the panel stages it, and apply.
//
// Before the fix this failed with `edit: operation 1: path segment
// "PolicyServer" not found`. The staleness pass located EVERY operation against
// the file as it is on disk, and `services.PolicyServer` does not exist there —
// it is created by operation 0. The plan was well formed, the engine was
// correct, and the check in front of them refused a valid edit.
func TestAStagedAddSurvivesTheStalenessCheck(t *testing.T) {
	path, original := fixture(t, "e37-staged-add-service.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "PolicyServer", Value: "nginx"})
	if err != nil {
		t.Fatalf("planning refused a perfectly ordinary service: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("the plan is %d operations, want the name and the image", len(ops))
	}

	staged := mustStage(t, path, ops)
	if staged[1].Expect == nil {
		t.Fatal("the image insert was staged without an Expect; the test is not exercising the bug")
	}

	// The refresh the panel performs immediately after staging. This is where
	// the reader actually saw the failure — before any Save button existed.
	if _, err := Preview(Request{File: path, Ops: staged}); err != nil {
		t.Fatalf("re-previewing the staged set failed: %v", err)
	}

	res, err := Apply(Request{File: path, Ops: staged})
	if err != nil {
		t.Fatalf("applying the staged set failed: %v", err)
	}
	if !res.Written {
		t.Fatal("Apply reported no write")
	}
	if res.Added != 2 || res.Removed != 0 {
		t.Errorf("the write is +%d/-%d, want exactly two added lines", res.Added, res.Removed)
	}

	got := read(t, path)
	if !strings.Contains(got, "  PolicyServer:\n    image: nginx\n") {
		t.Errorf("the service did not land as one block:\n%s", got)
	}
	// Every other byte identical: excising the inserted block restores the file.
	assertExcisionRestores(t, "e37-staged-add-service.yml", []byte(original), []byte(got))
}

// The other half of the same contract: a stage against a range that HAS moved
// is still refused. The fix moved the comparison, it did not remove it, and a
// staleness check that never fires is worse than none because it is trusted.
func TestAStagedAddIsStillRefusedWhenTheFileMovedUnderIt(t *testing.T) {
	path, original := fixture(t, "e37-staged-add-service.yml")

	ops, err := Plan(Add{File: path, Kind: "service", Name: "PolicyServer", Value: "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	staged := mustStage(t, path, ops)

	// Somebody else adds a service above ours in the editor.
	moved := strings.Replace(original, "services:\n", "services:\n  audit:\n    image: alpine:3\n", 1)
	if moved == original {
		t.Fatal("the fixture no longer has the anchor this test edits")
	}
	if err := os.WriteFile(path, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(Request{File: path, Ops: staged}); !errors.Is(err, ErrStaleRange) {
		t.Fatalf("error is %v, want ErrStaleRange", err)
	}
	if got := read(t, path); got != moved {
		t.Error("a refused apply wrote to the file")
	}
}

// ------------------------------------------------------------- corpus ------

// The epic's constraint over files nobody wrote for this test: adding a service
// to a real compose file inserts exactly one contiguous block and leaves every
// other byte identical.
//
// The sweep applies the plan the way the PANEL applies it — previewed, staged
// with an Expect per operation, then applied — and not the way the CLI does.
// That distinction is the hole this sweep used to have. It reported "144
// attempted, 140 applied" while a service could not be added from the extension
// at all, because `Apply(ops)` with no Expect skips the staleness comparison
// entirely: 140 files agreed on a code path no reader is ever on. The one line
// that closes it is `stage(t, path, ops)`, and the cost of it is that a
// dependent operation set is now checked the way it is really used.
func TestCorpusAddServiceIsOneCleanBlock(t *testing.T) {
	files := corpusFiles(t)
	dir := t.TempDir()
	var attempted, applied, refused int

	for i, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if _, err := (strategy.Splice{}).Identity(src); err != nil {
			continue
		}
		path := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(path, src, 0o644); err != nil {
			t.Fatal(err)
		}
		attempted++
		ops, err := Plan(Add{File: path, Kind: "service", Name: "composure-probe", Value: "alpine:3"})
		if err != nil {
			if !Refused(err) {
				t.Errorf("%s: planning failed rather than refused: %v", file, err)
			}
			refused++
			continue
		}
		staged, err := stage(path, ops)
		if err != nil {
			if !Refused(err) {
				t.Errorf("%s: previewing the plan failed rather than refused: %v", file, err)
			}
			refused++
			continue
		}
		res, err := Apply(Request{File: path, Ops: staged})
		if err != nil {
			if !Refused(err) {
				t.Errorf("%s: %v", file, err)
			}
			refused++
			continue
		}
		applied++
		assertExcisionRestores(t, file, src, res.Bytes)
		// It still parses, and the service is addressable under the name it was
		// given — asserted through the engine rather than through the resolver,
		// because a corpus file copied out of its repository loses the
		// `include:`, `extends:` and `${VAR}` context the resolver needs and
		// would fail for reasons that have nothing to do with this edit.
		if _, err := (strategy.Splice{}).Identity(res.Bytes); err != nil {
			t.Errorf("%s: the result does not parse: %v", file, err)
			continue
		}
		if _, _, err := strategy.Locate(res.Bytes, []string{"services", "composure-probe"}); err != nil {
			t.Errorf("%s: the service is not addressable after the write: %v", file, err)
		}
		if _, _, err := strategy.Locate(res.Bytes, []string{"services", "composure-probe", "image"}); err != nil {
			t.Errorf("%s: the image did not land under the service: %v", file, err)
		}
		if i > 400 {
			break
		}
	}
	if attempted == 0 {
		t.Fatal("no corpus file was attempted")
	}
	if applied == 0 {
		t.Fatalf("every one of %d corpus files refused; the sweep proves nothing", attempted)
	}
	t.Logf("add service: %d attempted, %d applied, %d refused", attempted, applied, refused)
}

var _ = corpus.Collect

// validate's second parser, pinned where an operation can no longer reach it.
//
// goccy accepts an implicit key past YAML's 1024-character limit and yaml.v3 —
// which is what internal/resolve reads every file with — does not. That
// divergence is why validate re-parses with both. Once the insert_key arm began
// checking the key itself, the only operation that could produce the divergent
// document stopped producing it, and this arm would have been left as code with
// no test able to fail. So it is exercised directly, on a buffer built by hand.
func TestValidateRefusesWhatOnlyYAMLv3Rejects(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n"
	out := []byte("services:\n  web:\n    image: nginx\n  " +
		strings.Repeat("n", implicitKeyLimit+6) + ":\n")

	// The premise: goccy reads it. If this ever stops being true the test below
	// proves nothing, so it is asserted rather than assumed.
	if _, err := (strategy.Splice{}).Identity(out); err != nil {
		t.Fatalf("goccy already rejects this buffer, so it no longer isolates the yaml.v3 arm: %v", err)
	}

	err := validate("yaml", []byte(src), out)
	if !errors.Is(err, ErrWouldCorrupt) {
		t.Fatalf("err=%v, want ErrWouldCorrupt from the yaml.v3 re-parse", err)
	}
}

// A key that is really a comment.
//
// This is the shape that made the insert_key arm need its own check. Every
// other guard passes it: strategy splices the bytes correctly, goccy reads the
// result, yaml.v3 reads the result, and validate's re-parse is satisfied —
// because the file IS valid YAML. It simply does not contain the key the reader
// asked for. `#LOG: debug` is a comment, and the mapping is unchanged.
//
// Nothing that checks whether the document parses can see this. Only reading
// the key back out of what would be written can, which is what bare's readback
// does and why the check belongs on the operation rather than on the planner.
func TestAKeyThatWouldBecomeACommentIsRefused(t *testing.T) {
	const src = "services:\n  web:\n    environment:\n      NODE_ENV: production\n"

	for _, key := range []string{"#LOG", "# LOG", "#"} {
		path := write(t, "compose.yaml", src)
		_, err := Apply(Request{File: path, Ops: []Op{
			{Operation: OpInsertKey, At: "services.web.environment", Key: key, Value: "debug"},
		}})
		if err == nil {
			t.Errorf("key %q was accepted; the file now reads:\n%s", key, read(t, path))
			continue
		}
		if !Refused(err) {
			t.Errorf("key %q: Refused=false, err=%v — the reader is told the tool broke", key, err)
		}
		if got := read(t, path); got != src {
			t.Errorf("key %q: the file was written anyway:\n%s", key, got)
		}
	}
}
