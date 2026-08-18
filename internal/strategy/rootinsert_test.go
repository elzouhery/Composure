package strategy

import (
	"bytes"
	"errors"
	"testing"
)

// Story 7.2: add a top-level block to a compose file.
//
// This was not a missing feature but a live broken path: the schema layer
// offers the document root's unset keys, the inspector renders them as buttons,
// and clicking one staged an insert at the empty path, which locate rejected
// with a bare fmt.Errorf — so edit.Refused was false and the reader was told the
// tool had broken rather than that it had declined. Rule 6 wants one of two
// outcomes here, and "fault" is neither.

// A top-level key is appended after the last top-level key, at the root's own
// column, and the rest of the buffer is untouched byte for byte.
func TestInsertKeyAtRootAppendsTopLevel(t *testing.T) {
	src := []byte("name: demo\nservices:\n  web:\n    image: nginx\n")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey at root: %v", err)
	}
	const want = "name: demo\nservices:\n  web:\n    image: nginx\nnetworks:\n"
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// It lands AFTER a trailing comment block at column 0, and not between the last
// key and its comments. subtreeEnd treats a trailing comment as undecided, so
// this is the answer that has to be written deliberately — and pinned, because
// either behaviour is defensible and an undocumented one is not.
//
// The fixture is four-space indented and ends with a blank line before the
// comments, so an implementation that stopped at "the last line of the last
// key" or at "the end of the file" both fail it.
func TestInsertKeyAtRootLandsAfterTrailingComments(t *testing.T) {
	src := edgeSrc(t, "e24-root-trailing-comments.yml")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey at root: %v", err)
	}
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\nnetworks:")
	if !bytes.Contains(got, []byte("# See story 7.2.\nnetworks:\n")) {
		t.Errorf("the block did not land after the trailing comment run:\n%q", got)
	}
	if !bytes.HasSuffix(got, []byte("networks:\n")) {
		t.Errorf("the file should now end with the new block:\n%q", got)
	}
}

// The root is a mapping like any other: the new key copies the indent the root's
// existing keys use. On a fragment whose top-level keys sit at column 2, column
// 0 would reparent the whole file.
func TestInsertKeyAtRootCopiesRootIndent(t *testing.T) {
	src := edgeSrc(t, "e8-fragment-indented-services.yml")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey at root: %v", err)
	}
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\n  networks:")
}

// A BOM'd file: the mark survives a root insert, and the insert is the only
// change.
func TestInsertKeyAtRootKeepsBOM(t *testing.T) {
	src := edgeSrc(t, "e15-bom-first-key.yml")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey at root: %v", err)
	}
	if !bytes.HasPrefix(got, bomPrefix) {
		t.Errorf("the byte order mark did not survive the root insert")
	}
	assertSingleInsertion(t, src, got)
	assertInsertion(t, src, got, "\nnetworks:")
}

// A CRLF file: the new top-level line carries the file's ending (story 7.1
// applies to the root path too, because it is the same splice).
func TestInsertKeyAtRootCRLF(t *testing.T) {
	src := edgeSrc(t, "e18-crlf-four-space.yml")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey at root: %v", err)
	}
	assertNoLoneLF(t, got)
	assertInsertion(t, src, got, "\r\nnetworks:")
}

// A multi-document file targets the FIRST document, as f.Docs[0] does
// everywhere else in this engine — so the new key lands before the `---`, not
// at the end of the file where it would silently join a different document.
func TestInsertKeyAtRootTargetsFirstDocument(t *testing.T) {
	src := edgeSrc(t, "e28-multidoc.yml")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey at root: %v", err)
	}
	assertSingleInsertion(t, src, got)
	const want = "services:\n  web:\n    image: nginx\nnetworks:\n---\nservices:\n  other:\n    image: busybox\n"
	if string(got) != want {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

// A document with no root mapping is REFUSED with a typed sentinel and nothing
// is written. Appending `networks:` to an empty file invents a document shape,
// which is the scaffolding DECISIONS.md 20 keeps in phase 5.
func TestInsertKeyAtRootRefusesDocumentsWithNoRootMapping(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
	}{
		{"empty file", edgeSrc(t, "e25-root-empty.yml")},
		{"comments only", edgeSrc(t, "e26-root-comments-only.yml")},
		{"sequence at the root", edgeSrc(t, "e27-root-sequence.yml")},
		{"explicit null document", []byte("null\n")},
		{"scalar document", []byte("just a string\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Splice{}.InsertKey(c.src, nil, "networks", "")
			if !errors.Is(err, ErrNoRootMapping) {
				t.Fatalf("error is %v, want ErrNoRootMapping", err)
			}
			if got != nil {
				t.Errorf("a refusal returned %d bytes; it must touch nothing", len(got))
			}
		})
	}
}

// A flow-style root — the whole document written as `{services: {...}}` — is
// refused with ErrFlowStyle, like any other flow collection. isFlowMapping
// cannot see this one: it looks for text after the first ":" and a flow root
// opens with "{" before any colon.
func TestInsertKeyAtRootRefusesFlowRoot(t *testing.T) {
	src := []byte("{name: demo, services: {web: {image: nginx}}}\n")
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if !errors.Is(err, ErrFlowStyle) {
		t.Fatalf("error is %v, want ErrFlowStyle", err)
	}
	if got != nil {
		t.Errorf("a refusal returned %d bytes; it must touch nothing", len(got))
	}
}

// AD-14: the offset a preview reports for a root insert is the offset the write
// uses, and it comes from the same function.
func TestInsertionPointAtRoot(t *testing.T) {
	src := edgeSrc(t, "e24-root-trailing-comments.yml")
	off, indent, err := InsertionPoint(src, nil)
	if err != nil {
		t.Fatalf("InsertionPoint at root: %v", err)
	}
	if indent != 0 {
		t.Errorf("indent %d, want 0", indent)
	}
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if err != nil {
		t.Fatalf("InsertKey: %v", err)
	}
	if !bytes.Equal(got[:off], src[:off]) || !bytes.HasPrefix(got[off:], []byte("\nnetworks:")) {
		t.Errorf("the write did not happen at the offset the preview reported (%d)", off)
	}
	// A refusal is a refusal in both, or the preview shows a range for an edit
	// that cannot happen.
	if _, _, err := InsertionPoint(edgeSrc(t, "e26-root-comments-only.yml"), nil); !errors.Is(err, ErrNoRootMapping) {
		t.Errorf("InsertionPoint error is %v, want ErrNoRootMapping", err)
	}
}

// A file whose licence header sits before a `---` separator parses as TWO
// documents, the first of which is only comments. Four corpus files are written
// that way, and no path in them is addressable by this engine at all — Docs[0]
// is a CommentGroupNode and the mapping is Docs[1].
//
// This is not new and story 7.2 does not fix it. What the fixture pins is that
// the consequence is a refusal and not damage: the root insert declines rather
// than appending a top-level key to a comment block or to whichever document
// the parser happened to hand back.
func TestLeadingCommentDocumentIsRefusedNotDamaged(t *testing.T) {
	src := edgeSrc(t, "e30-comment-doc-then-separator.yml")

	// The pre-existing limitation, stated so a future reader sees the cause.
	if _, _, err := ScalarRange(src, []string{"services", "web", "image"}); err == nil {
		t.Errorf("services.web.image is addressable now; this fixture's premise has changed and the assertion below should be revisited")
	}
	got, err := Splice{}.InsertKey(src, nil, "networks", "")
	if !errors.Is(err, ErrNoRootMapping) {
		t.Fatalf("error is %v, want ErrNoRootMapping", err)
	}
	if got != nil {
		t.Errorf("a refusal returned %d bytes; it must touch nothing", len(got))
	}
}
