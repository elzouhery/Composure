package edit

// Epic 7's write-path obligations: a top-level insert (story 7.2) and a
// sequence-entry insert (story 7.5) reached through the SAME surface the
// extension's buttons use, and refusals that come back classified as refusals.
//
// Story 6.5 is the standing evidence for why the classification is a criterion
// and not a nicety: a refusal reported as a failure tells the reader the tool
// broke when the tool declined. Before story 7.2 that is exactly what a reader
// clicking the root-level `networks` button was shown — the engine returned a
// bare fmt.Errorf("empty path"), so Refused was false and the host sent
// codeEditFailed.
//
// Every assertion here is on BYTES read back from the file, never on the
// absence of an error.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
	goccyast "github.com/goccy/go-yaml/ast"
	goccyparser "github.com/goccy/go-yaml/parser"
)

func TestApplyInsertKeyAtRoot(t *testing.T) {
	const src = "name: demo\nservices:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)

	res, err := Apply(Request{File: path, Ops: []Op{{Operation: OpInsertKey, At: "", Key: "networks"}}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	const want = "name: demo\nservices:\n  web:\n    image: nginx\nnetworks:\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +1 -0", res.Added, res.Removed)
	}
	// AD-14: the range the result reports is the range the write touched.
	if len(res.Ops) != 1 || res.Ops[0].Range.Start != res.Ops[0].Range.End {
		t.Errorf("an insert must report an empty range, got %+v", res.Ops[0].Range)
	}
	if res.Ops[0].Range.Start != len("name: demo\nservices:\n  web:\n    image: nginx") {
		t.Errorf("range start %d does not point at the end of the last top-level key's subtree",
			res.Ops[0].Range.Start)
	}
}

// The live broken path from DECISIONS.md 20, closed: a root insert on a file
// with no root mapping is a REFUSAL the reader can be told about, not a fault.
func TestRootInsertOnEmptyFileIsARefusal(t *testing.T) {
	path := write(t, "compose.yaml", "# nothing here yet\n")

	before := read(t, path)
	_, err := Apply(Request{File: path, Ops: []Op{{Operation: OpInsertKey, At: "", Key: "networks"}}})
	if err == nil {
		t.Fatal("Apply succeeded on a document with no root mapping")
	}
	if !errors.Is(err, strategy.ErrNoRootMapping) {
		t.Errorf("error is %v, want ErrNoRootMapping", err)
	}
	if !Refused(err) {
		t.Errorf("Refused(%v) is false; the reader would be shown a fault instead of a refusal", err)
	}
	if got := Reason(err); got != "no-root-mapping" {
		t.Errorf("Reason is %q, want %q", got, "no-root-mapping")
	}
	if got := read(t, path); got != before {
		t.Errorf("a refusal wrote to the file:\n got: %q\nwant: %q", got, before)
	}
}

// Story 7.5 through the write path, on a four-space file so the assertion also
// fails an insert that assumed the file's step was 2.
func TestApplyInsertSequenceEntry(t *testing.T) {
	const src = "services:\n    web:\n        image: nginx\n        ports:\n            - \"8080:80\"\n"
	path := write(t, "compose.yaml", src)

	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertSequenceEntry, At: "services.web.ports", Value: `"9090:90"`},
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := src + "            - \"9090:90\"\n"
	if got := read(t, path); got != want {
		t.Fatalf("file on disk\n got: %q\nwant: %q", got, want)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Errorf("diff reports +%d -%d, want +1 -0", res.Added, res.Removed)
	}
	if res.Ops[0].Range.Start != res.Ops[0].Range.End {
		t.Errorf("an insert must report an empty range, got %+v", res.Ops[0].Range)
	}
	if !strings.Contains(res.Ops[0].Describe, "ports") {
		t.Errorf("describe %q does not name the sequence", res.Ops[0].Describe)
	}
}

// A flow sequence is refused through the write path with the sentinel the panel
// already has an arm for, and nothing is written.
func TestInsertSequenceEntryIntoFlowSequenceIsARefusal(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n"
	path := write(t, "compose.yaml", src)

	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertSequenceEntry, At: "services.web.ports", Value: `"9090:90"`},
	}})
	if !errors.Is(err, strategy.ErrFlowStyle) {
		t.Fatalf("error is %v, want ErrFlowStyle", err)
	}
	if !Refused(err) {
		t.Errorf("Refused(%v) is false", err)
	}
	if got := read(t, path); got != src {
		t.Errorf("a refusal wrote to the file:\n got: %q", got)
	}
}

// An entry with no value would write a bare `- ` and leave the reader a null
// sequence entry. Refused — and classified as a refusal, so the reader is told
// the tool declined rather than that it broke.
func TestInsertSequenceEntryWithNoValueIsARefusal(t *testing.T) {
	const src = "services:\n  web:\n    ports:\n      - \"8080:80\"\n"
	path := write(t, "compose.yaml", src)

	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertSequenceEntry, At: "services.web.ports"},
	}})
	if !errors.Is(err, strategy.ErrNullEntry) {
		t.Fatalf("error is %v, want ErrNullEntry", err)
	}
	if !Refused(err) {
		t.Errorf("Refused(%v) is false", err)
	}
	if got := Reason(err); got != "null-entry" {
		t.Errorf("Reason is %q, want %q", got, "null-entry")
	}
	if got := read(t, path); got != src {
		t.Errorf("a refusal wrote to the file:\n got: %q", got)
	}
}

// The same refusal, reached through the CLI's own shape — no Expect, one
// operation, exactly what `composure apply -op insert_sequence_entry` sends.
//
// Both of these exited 0 and wrote. `# nope` produced `- # nope`: the null
// entry the sentinel above exists to prevent. The newline produced TWO lines,
// the second of which joined the parent mapping and gave the service a
// `restart` key nobody typed — and that one PARSES, so the write path's
// re-parse waved it through. Refusal is the only correct answer to either.
func TestInsertSequenceEntryTextIsRefusedThroughApply(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n    ports:\n      - \"80:80\"\n      - \"443:443\"\n"

	for _, tc := range []struct {
		what, value, reason string
		sentinel            error
	}{
		{"a comment marker", "# nope", "null-entry", strategy.ErrNullEntry},
		{"a line break", "x\n    restart: always", "entry-text", strategy.ErrEntryText},
		{"a trailing comment", "8080:80 # published", "entry-text", strategy.ErrEntryText},
	} {
		path := write(t, "compose.yaml", src)
		_, err := Apply(Request{File: path, Ops: []Op{
			{Operation: OpInsertSequenceEntry, At: "services.web.ports", Value: tc.value},
		}})
		if err == nil {
			t.Errorf("%s (%q) was written; the file now reads:\n%s", tc.what, tc.value, read(t, path))
			continue
		}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("%s: error is %v, want %v", tc.what, err, tc.sentinel)
		}
		if !Refused(err) || Reason(err) != tc.reason {
			t.Errorf("%s: Refused=%v Reason=%q, want %q", tc.what, Refused(err), Reason(err), tc.reason)
		}
		if got := read(t, path); got != src {
			t.Errorf("%s: a refusal wrote to the file:\n%s", tc.what, got)
		}
	}

	// And the ordinary case still goes through, one line, unchanged elsewhere:
	// the refusal must not have taken publishing a port with it.
	path := write(t, "compose.yaml", src)
	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpInsertSequenceEntry, At: "services.web.ports", Value: "8080:80"},
	}})
	if err != nil {
		t.Fatalf("an ordinary port entry was refused: %v", err)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Errorf("diff is +%d -%d, want +1 -0", res.Added, res.Removed)
	}
	if got := read(t, path); got != src+"      - 8080:80\n" {
		t.Errorf("file on disk:\n%s", got)
	}
}

// Story 7.3's shape, proven at the boundary this story owns: a top-level block
// and its first entry as ONE request of two operations, previewed as one diff
// and written atomically. A `networks:` block with nothing under it is not what
// the reader asked for, and leaving one on disk because the second op failed is
// the partial write edit.run exists to make impossible.
func TestRootBlockAndEntryApplyAsOneEdit(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)

	res, err := Preview(Request{File: path, Ops: []Op{
		{Operation: OpInsertKey, At: "", Key: "networks"},
		{Operation: OpInsertKey, At: "networks", Key: "frontend"},
	}})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\nnetworks:\n  frontend:\n"
	if string(res.Bytes) != want {
		t.Fatalf("preview bytes\n got: %q\nwant: %q", res.Bytes, want)
	}
	if got := read(t, path); got != src {
		t.Errorf("Preview wrote to the file")
	}
	if res.Added != 2 {
		t.Errorf("diff reports +%d, want +2 in one diff", res.Added)
	}
	// `frontend:` with no trailing space: the engine writes no byte the reader
	// did not ask for.
	if strings.Contains(want, " \n") || strings.Contains(string(res.Bytes), " \n") {
		t.Errorf("a line ends in a space: %q", res.Bytes)
	}
}

// ------------------------------------------------------------- corpus ------

// Corpus sweeps for the two new operations.
//
// structbench is where a structural operation's corpus number belongs, and
// story 7.5 asks for one as a new baselined key: `structbench.insertentry.*`
// now exists and is gated. These sweeps are the layer above it — the same files
// through the WRITE PATH the extension uses rather than through the engine
// directly, which is a different question from the one the benchmark answers.
//
// Both stage their operations the way the panel stages them, with an Expect
// recorded from the preview. A sweep built on a bare `Apply(ops)` skips the
// staleness comparison entirely and measures a code path no reader is on; that
// hole was repaired for the add sweep and left open in these two.
//
// What both assert is the epic's constraint: excising the one inserted line
// from the result yields the source byte for byte.

func corpusFiles(t *testing.T) []string {
	t.Helper()
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files, err := corpus.Collect(corpusRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) == 0 {
		// Not a skip: a present-but-empty corpus is a broken cache, and a sweep
		// that measured nothing must not read as green.
		t.Fatalf("%s exists but holds no compose files; run `make corpus`", corpusRoot)
	}
	return files
}

// assertExcisionRestores is the epic-7 constraint over a corpus file: one
// contiguous block added, every other byte identical.
func assertExcisionRestores(t *testing.T, file string, src, got []byte) {
	t.Helper()
	if len(got) <= len(src) {
		t.Errorf("%s: result is not longer than the source", file)
		return
	}
	n := 0
	for n < len(src) && src[n] == got[n] {
		n++
	}
	m := 0
	for m < len(src)-n && src[len(src)-1-m] == got[len(got)-1-m] {
		m++
	}
	rebuilt := append(append([]byte{}, got[:n]...), got[len(got)-m:]...)
	if string(rebuilt) != string(src) {
		t.Errorf("%s: excising the inserted block does not restore the source", file)
	}
}

func TestCorpusRootInsertIsOneCleanBlock(t *testing.T) {
	files := corpusFiles(t)
	dir := t.TempDir()
	var attempted, applied, refused int
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if _, err := (strategy.Splice{}).Identity(src); err != nil {
			continue // the file does not parse at all; nothing to measure here
		}
		attempted++
		work := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(work, src, 0o644); err != nil {
			t.Fatal(err)
		}
		staged, err := stage(work, []Op{{Operation: OpInsertKey, At: "", Key: "x-composure-probe"}})
		if err != nil {
			if Refused(err) {
				refused++
				t.Logf("refused %s at preview: %s", file, Reason(err))
				continue
			}
			t.Errorf("%s: previewing failed rather than refused: %v", file, err)
			continue
		}
		res, err := Apply(Request{File: work, Ops: staged})
		if err != nil {
			if Refused(err) {
				refused++
				t.Logf("refused %s: %s", file, Reason(err))
				continue // a document with no root mapping, correctly declined
			}
			t.Errorf("%s: %v", file, err)
			continue
		}
		applied++
		// One added line, nothing removed — except on a file with no trailing
		// newline, where the last line necessarily gains the terminator that
		// separates it from the new one. A line-oriented diff renders that as
		// the last line changing, and it is the one case story 7.1 names as
		// unavoidable. The bytes are still checked below either way.
		// The anchor is the last NON-BLANK line, so what matters is whether
		// THAT line is terminated — not whether the file ends with a newline.
		// A file ending in blank lines is terminated at its anchor; a file whose
		// last content line runs to EOF is not.
		wantAdded, wantRemoved := 1, 0
		if tail := string(src)[len(strings.TrimRight(string(src), " \t\r\n")):]; !strings.Contains(tail, "\n") {
			wantAdded, wantRemoved = 2, 1
		}
		if res.Added != wantAdded || res.Removed != wantRemoved {
			t.Errorf("%s: diff is +%d -%d, want +%d -%d:\n%s",
				file, res.Added, res.Removed, wantAdded, wantRemoved, res.Diff)
		}
		after, err := os.ReadFile(work)
		if err != nil {
			t.Fatal(err)
		}
		assertExcisionRestores(t, file, src, after)
	}
	if applied == 0 {
		t.Fatalf("no root insert was applied across %d corpus files", attempted)
	}
	t.Logf("root insert: %d applied, %d refused, of %d parseable corpus files", applied, refused, attempted)
}

func TestCorpusSequenceEntryInsertIsOneCleanBlock(t *testing.T) {
	files := corpusFiles(t)
	dir := t.TempDir()
	var attempted, applied int
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		path, ok := firstBlockSequence(src)
		if !ok {
			continue
		}
		attempted++
		work := filepath.Join(dir, "compose.yaml")
		if err := os.WriteFile(work, src, 0o644); err != nil {
			t.Fatal(err)
		}
		staged, err := stage(work, []Op{
			{Operation: OpInsertSequenceEntry, At: path, Value: "example.invalid-probe"},
		})
		if err != nil {
			if Refused(err) {
				continue
			}
			t.Errorf("%s (%s): previewing failed rather than refused: %v", file, path, err)
			continue
		}
		res, err := Apply(Request{File: work, Ops: staged})
		if err != nil {
			if Refused(err) {
				continue
			}
			t.Errorf("%s (%s): %v", file, path, err)
			continue
		}
		applied++
		if res.Added != 1 || res.Removed != 0 {
			t.Errorf("%s: diff is +%d -%d, want +1 -0:\n%s", file, res.Added, res.Removed, res.Diff)
		}
		after, err := os.ReadFile(work)
		if err != nil {
			t.Fatal(err)
		}
		assertExcisionRestores(t, file, src, after)

		// And the entry has to be readable back as an entry of that sequence.
		// A line written at the wrong column still parses; it just belongs to
		// something else, which is the failure this engine specialises in.
		if !strings.Contains(string(after), "example.invalid-probe") {
			t.Errorf("%s: the entry is not in the file", file)
			continue
		}
		f, err := goccyparser.ParseBytes(after, goccyparser.ParseComments)
		if err != nil {
			t.Errorf("%s: the result does not parse: %v", file, err)
			continue
		}
		node := findNode(f.Docs[0].Body, resolve.ParsePath(path))
		seq, ok := node.(*goccyast.SequenceNode)
		if !ok {
			t.Errorf("%s: %s is no longer a sequence after the insert", file, path)
			continue
		}
		last := seq.Values[len(seq.Values)-1]
		if got := strings.TrimSpace(last.GetToken().Value); got != "example.invalid-probe" {
			t.Errorf("%s: the last entry of %s reads %q, so the new entry landed somewhere else",
				file, path, got)
		}
	}
	if applied == 0 {
		t.Fatalf("no sequence entry was inserted across %d corpus files", attempted)
	}
	t.Logf("sequence entry insert: %d applied of %d corpus files with a block sequence", applied, attempted)
}

// firstBlockSequence finds a `services.<name>.<key>` whose value is a non-empty
// BLOCK sequence — the target the operation is for. Flow sequences are excluded
// here because they are refused by design and a sweep of refusals measures the
// refusal, not the write.
func firstBlockSequence(src []byte) (string, bool) {
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
		for _, svc := range svcs.Values {
			body, ok := svc.Value.(*goccyast.MappingNode)
			if !ok {
				continue
			}
			for _, field := range body.Values {
				seq, ok := field.Value.(*goccyast.SequenceNode)
				if !ok || seq.IsFlowStyle || len(seq.Values) == 0 {
					continue
				}
				// Scalar entries only: a mapping entry's own indentation is a
				// different question, covered by the fixtures.
				if _, isMap := seq.Values[0].(*goccyast.MappingNode); isMap {
					continue
				}
				return resolve.Path{"services", svc.Key.GetToken().Value, field.Key.GetToken().Value}.String(), true
			}
		}
	}
	return "", false
}

// findNode walks a parsed document by path. It exists only so the sweep can
// read the result back; the engine's own traversal is not exported.
func findNode(n goccyast.Node, path resolve.Path) goccyast.Node {
	for _, seg := range path {
		m, ok := n.(*goccyast.MappingNode)
		if !ok {
			return nil
		}
		found := false
		for _, kv := range m.Values {
			if kv.Key.GetToken().Value == seg {
				n, found = kv.Value, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return n
}
