package dockerfile

import (
	"errors"
	"strings"
	"testing"
)

// R7.4 AT THE ENGINE LAYER.
//
// There are two guards against reflowing a continuation block: this one, in
// `File.ReplaceArgs`, and one in `internal/edit.locate`. Until this test
// existed, either guard could be deleted on its own with a green suite,
// because the other still refused — defence in depth that degrades silently.
// This test names the layer it pins: delete the `ErrMultiLine` check in
// `ReplaceArgs` and this fails, whatever `internal/edit` does.
func TestReplaceArgsRefusesAMultiLineInstruction(t *testing.T) {
	f := Parse([]byte(formSample))
	idx := indexOfInstruction(t, f, "RUN")
	if in := f.Instructions[idx]; in.EndLine == in.StartLine {
		t.Fatalf("fixture's RUN is single-line; this test needs a continuation block")
	}

	out, err := f.ReplaceArgs(idx, "npm ci")
	if !errors.Is(err, ErrMultiLine) {
		t.Fatalf("ReplaceArgs on a continuation block returned %v, want ErrMultiLine", err)
	}
	// Refusal, not a best effort: nothing is handed back for a caller to write.
	if out != nil {
		t.Errorf("a refused rewrite still produced %d bytes", len(out))
	}
}

// The other direction of the same guard. A mutant that refuses EVERY
// instruction would satisfy the test above and break the feature; this one
// fails under it. Together they pin the guard's condition, not its presence.
func TestReplaceArgsStillRewritesASingleLineInstruction(t *testing.T) {
	f := Parse([]byte(formSample))
	idx := indexOfInstruction(t, f, "WORKDIR")

	out, err := f.ReplaceArgs(idx, "/app")
	if err != nil {
		t.Fatalf("ReplaceArgs on a single-line instruction: %v", err)
	}
	if !strings.Contains(string(out), "WORKDIR /app\n") {
		t.Errorf("the rewritten line is not in the output:\n%s", out)
	}
	// Everything else is untouched — the multi-line RUN above all.
	if !strings.Contains(string(out), "RUN npm ci \\\n    --omit=dev\n") {
		t.Errorf("the continuation block was reflowed by an unrelated edit:\n%s", out)
	}
}

func indexOfInstruction(t *testing.T, f *File, name string) int {
	t.Helper()
	for i, in := range f.Instructions {
		if in.Kind == KindInstruction && in.Name == name {
			return i
		}
	}
	t.Fatalf("fixture has no %s instruction", name)
	return -1
}
