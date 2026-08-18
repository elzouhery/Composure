package edit

// The null-value corruption, reached through the surface it was reported on:
// `composure preview -op replace_scalar` and its apply twin, which is what the
// extension's Save button calls.
//
// internal/strategy holds the engine tests. These exist because the defect was
// found HERE, and because a fix that works in the engine and not through the
// write path is not a fix. Every assertion is on exact bytes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reported reproduction, verbatim. Before the fix the diff removed BOTH
// `    healthcheck:` and `    restart: always` and added the single welded line
// `    healthcheck:x restart: always` — a destroyed line and a three-line diff.
// R4.1's promise is two.
func TestPreviewNullValueIsATwoLineDiff(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n    healthcheck:\n    restart: always\n"
	path := write(t, "compose.yaml", src)

	res, err := Preview(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.healthcheck", Value: "x",
	}}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    healthcheck: x\n    restart: always\n"
	if string(res.Bytes) != want {
		t.Fatalf("result bytes\n got: %q\nwant: %q", res.Bytes, want)
	}
	if res.Added != 1 || res.Removed != 1 {
		t.Errorf("diff is +%d/-%d, want +1/-1 — R4.1 is a two-line diff", res.Added, res.Removed)
	}
	if strings.Contains(res.Diff, "restart: always") &&
		strings.Contains(res.Diff, "-    restart: always") {
		t.Errorf("the diff removes a line the edit does not own:\n%s", res.Diff)
	}
	// Preview never writes.
	if got := read(t, path); got != src {
		t.Error("preview changed the file")
	}
}

// Applying it: the following line must come back byte-identical off disk.
func TestApplyNullValueLeavesFollowingLineByteIdentical(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n    healthcheck:\n    restart: always\n    ports:\n      - \"8080:80\"\n"
	path := write(t, "compose.yaml", src)

	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.healthcheck", Value: "x",
	}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	before := strings.Split(src, "\n")
	after := strings.Split(read(t, path), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count moved %d -> %d", len(before), len(after))
	}
	for i := range before {
		if i == 3 {
			continue // the edited line
		}
		if before[i] != after[i] {
			t.Errorf("line %d changed and the edit does not own it\n got: %q\nwant: %q",
				i+1, after[i], before[i])
		}
	}
	if after[3] != "    healthcheck: x" {
		t.Errorf("edited line is %q, want %q", after[3], "    healthcheck: x")
	}
}

// End-of-file failed with "computed range 51:55 exceeds source length 51".
// A performable edit refused is a confident wrong answer too.
func TestApplyNullValueAtEndOfFile(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n    healthcheck:\n"
	path := write(t, "compose.yaml", src)

	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.healthcheck", Value: "x",
	}}}); err != nil {
		t.Fatalf("apply at EOF: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    healthcheck: x\n"
	if got := read(t, path); got != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// The permanent regression fixtures, driven through the write path. A file that
// exists but nothing reads is not a regression test.
func TestNullValueRegressionFixtures(t *testing.T) {
	for _, name := range []string{
		"e11-null-value-mid-file.yml",
		"e12-null-value-eof.yml",
	} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
			if err != nil {
				t.Fatalf("regression fixture missing: %v", err)
			}
			path := write(t, "compose.yaml", string(src))
			res, err := Apply(Request{File: path, Ops: []Op{{
				Operation: OpReplaceScalar,
				At:        "services.web.healthcheck",
				Value:     "CMD-SHELL true",
			}}})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if res.Added != 1 || res.Removed != 1 {
				t.Errorf("diff is +%d/-%d, want +1/-1:\n%s", res.Added, res.Removed, res.Diff)
			}
			after := read(t, path)
			if !strings.Contains(after, "    healthcheck: CMD-SHELL true") {
				t.Errorf("the edit did not land:\n%s", after)
			}
			// Everything else is byte-identical.
			bl, al := strings.Split(string(src), "\n"), strings.Split(after, "\n")
			if len(bl) != len(al) {
				t.Fatalf("line count moved %d -> %d", len(bl), len(al))
			}
			changed := 0
			for i := range bl {
				if bl[i] != al[i] {
					changed++
				}
			}
			if changed != 1 {
				t.Errorf("%d lines changed, want exactly 1", changed)
			}
		})
	}
}

// insert_key with an empty value wrote `key: ` — a trailing space, confirmed
// with od -c. Asserted on exact bytes: `key: ` parses, so a parse check would
// have passed against the defect.
func TestInsertKeyWithNoValueWritesNoTrailingSpace(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)

	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpInsertKey, At: "services.web", Key: "healthcheck",
	}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    healthcheck:\n"
	got := read(t, path)
	if got != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
	for i, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %d ends in a space: %q", i+1, line)
		}
	}
}

// The whole story-5.2 gesture: click an unset key, then fill it in. Two edits,
// both of which were broken, and the composition is what the reader does.
func TestInsertThenSetProducesOneCleanLine(t *testing.T) {
	const src = "services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n"
	path := write(t, "compose.yaml", src)

	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpInsertKey, At: "services.web", Key: "restart",
	}}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.restart", Value: "always",
	}}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	const want = "services:\n  web:\n    image: nginx\n    restart: always\n  db:\n    image: postgres\n"
	if got := read(t, path); got != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// Setting a null to nothing changes nothing, and the write path says so with
// ErrNoChange instead of touching the file to add a space.
func TestSettingANullToEmptyIsNoChange(t *testing.T) {
	const src = "services:\n  web:\n    healthcheck:\n    restart: always\n"
	path := write(t, "compose.yaml", src)

	_, err := Apply(Request{File: path, Ops: []Op{{
		Operation: OpReplaceScalar, At: "services.web.healthcheck", Value: "",
	}}})
	if err == nil {
		t.Fatal("setting a null to empty reported a change")
	}
	if Reason(err) != "no-change" {
		t.Errorf("reason is %q, want %q (err: %v)", Reason(err), "no-change", err)
	}
	if got := read(t, path); got != src {
		t.Errorf("the file was written\n got: %q\nwant: %q", got, src)
	}
}
