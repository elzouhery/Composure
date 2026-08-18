package edit

// The grammar of a request is the FILE's, not operation 0's.
//
// `validate` took the grammar from `req.Ops[0].Operation.Grammar()` and the
// dockerfile arm asserts only that the result still holds instructions — which
// an appended `FROM alpine` satisfies on any file at all. So:
//
//	composure apply -op insert_stage -value alpine compose.yaml
//	  -> exit 0, "1 line added"
//	composure resolve compose.yaml
//	  -> compose.yaml:7:1: yaml: line 7: could not find expected ':'
//
// The YAML re-parse never ran on a YAML file. `set_base_image` could not reach
// it — there is no FROM to find — but `insert_stage` and `insert_instruction`,
// both new in epic 7, can, because the stage anchor falls back to the last line
// of anything. It is reachable from the CLI and over the RPC.
//
// These tests are the two halves of the fix: a Dockerfile operation on a YAML
// file is refused, and a Dockerfile operation on a Dockerfile still works —
// including for every Dockerfile fixture in the repository, so the check cannot
// be paying for itself by refusing real work.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/strategy"
)

const composeForGrammar = "services:\n  web:\n    image: nginx\n    ports:\n      - \"80:80\"\n"

func TestDockerfileOperationOnAComposeFileIsRefused(t *testing.T) {
	for _, op := range []Op{
		{Operation: OpInsertStage, Value: "alpine"},
		{Operation: OpInsertInstruction, Stage: 0, Value: "USER app"},
		{Operation: OpSetBaseImage, Stage: 0, Value: "alpine"},
		{Operation: OpReplaceArgs, Instruction: 0, Value: "echo hi"},
	} {
		path := write(t, "compose.yaml", composeForGrammar)
		_, err := Apply(Request{File: path, Ops: []Op{op}})
		if err == nil {
			t.Errorf("%s was applied to a compose file; it now reads:\n%s", op.Operation, read(t, path))
			continue
		}
		if !errors.Is(err, ErrWrongGrammar) {
			t.Errorf("%s: error is %v, want ErrWrongGrammar", op.Operation, err)
		}
		if !Refused(err) || Reason(err) != "wrong-grammar" {
			t.Errorf("%s: Refused=%v Reason=%q, want a wrong-grammar refusal",
				op.Operation, Refused(err), Reason(err))
		}
		if got := read(t, path); got != composeForGrammar {
			t.Errorf("%s: a refusal wrote to the file:\n%s", op.Operation, got)
		}
	}
}

// Preview is the same code path with one boolean flipped, and a preview that
// showed a diff the write refuses is a lie the reader cannot check.
func TestPreviewOfADockerfileOperationOnAComposeFileIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", composeForGrammar)
	if _, err := Preview(Request{File: path, Ops: []Op{{Operation: OpInsertStage, Value: "alpine"}}}); !errors.Is(err, ErrWrongGrammar) {
		t.Fatalf("Preview error is %v, want ErrWrongGrammar", err)
	}
}

// One request, one file, one grammar. A request mixing the two cannot be
// validated against either without lying about the other, and operation 0
// deciding for the rest is exactly how the defect above got in.
func TestARequestSpanningTwoGrammarsIsRefused(t *testing.T) {
	path := write(t, "compose.yaml", composeForGrammar)
	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertKey, At: "services", Key: "cache"},
		{Operation: OpInsertStage, Value: "alpine"},
	}})
	if !errors.Is(err, ErrMixedGrammar) {
		t.Fatalf("error is %v, want ErrMixedGrammar", err)
	}
	if !Refused(err) || Reason(err) != "mixed-grammar" {
		t.Errorf("Refused=%v Reason=%q, want a mixed-grammar refusal", Refused(err), Reason(err))
	}
	if got := read(t, path); got != composeForGrammar {
		t.Errorf("a refusal wrote to the file:\n%s", got)
	}
}

// The other half, and the one that decides whether the check is worth having:
// every Dockerfile this repository keeps as a fixture must still take a stage.
// A grammar test that refused a real Dockerfile would be a regression wearing a
// fix's clothes.
func TestEveryDockerfileFixtureStillTakesAStage(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "testdata", "dockerfiles"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "dockerfiles", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strategy.RootIsMapping(src) {
			t.Errorf("%s parses as a YAML mapping, so the grammar check would refuse a real Dockerfile", e.Name())
			continue
		}
		seen++
		path := write(t, "Dockerfile", string(src))
		if _, err := Apply(Request{File: path, Ops: []Op{{Operation: OpInsertStage, Value: "alpine:3.20"}}}); err != nil {
			// A refusal the ENGINE makes — a directives-only file with nowhere
			// safe to anchor — is the engine's own decision and not this check's.
			if Refused(err) && Reason(err) != "wrong-grammar" && Reason(err) != "mixed-grammar" {
				continue
			}
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if !strings.Contains(read(t, path), "alpine:3.20") {
			t.Errorf("%s: the stage did not land", e.Name())
		}
	}
	if seen == 0 {
		t.Fatal("no Dockerfile fixture was exercised; the sweep proves nothing")
	}
}

// The same question asked of the corpus rather than the fixtures: 179
// Dockerfiles, none of which may look like a YAML mapping to this check.
func TestNoCorpusDockerfileLooksLikeYAML(t *testing.T) {
	root := filepath.Join("..", "..", "corpus-repos")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", root)
	}
	seen, mapping := 0, 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // an unreadable corpus entry is not this test's subject
		}
		base := filepath.Base(path)
		if base != "Dockerfile" && !strings.HasPrefix(base, "Dockerfile.") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		seen++
		if strategy.RootIsMapping(src) {
			mapping++
			t.Errorf("%s parses as a YAML mapping", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Skip("no Dockerfiles under the corpus")
	}
	t.Logf("%d corpus Dockerfiles, %d of them mistakable for YAML", seen, mapping)
}
