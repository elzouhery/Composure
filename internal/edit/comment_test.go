package edit

// Story 9.1 over the write path: two operations, one closed set, and the same
// four rules the package doc states. Preview never writes; a refusal is a
// refusal and not a fault; the bytes are the source's with one span replaced.

import (
	"errors"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/strategy"
)

func TestSetAndDeleteACommentThroughTheWritePath(t *testing.T) {
	src := edgeFixture(t, "e44-comments-everywhere.yml")
	path := write(t, "compose.yaml", src)

	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpSetComment, At: "services.web", Where: strategy.CommentAbove, Value: "the front door"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Three comment lines out, one in.
	if res.Removed != 3 || res.Added != 1 {
		t.Errorf("%d removed / %d added, want 3 / 1", res.Removed, res.Added)
	}
	got := read(t, path)
	want := strings.Replace(src,
		"  # web is the front door.\n"+
			"  # It is documented in two lines on purpose: a fixture with ONE comment\n"+
			"  # cannot tell \"replaced the run\" from \"replaced the first line of it\".\n",
		"  # the front door\n", 1)
	if got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}

	if _, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpDeleteComment, At: "services.web", Where: strategy.CommentAbove},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != strings.Replace(want, "  # the front door\n", "", 1) {
		t.Errorf("the delete took bytes it does not own: %q", got)
	}
}

// A staged comment edit chains with an ordinary one, in one atomic request —
// which is the property `run` already provides and which a second write path
// would have broken.
func TestACommentAndAValueChangeAreOneEdit(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)
	if _, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.image", Value: "nginx:1.27"},
		{Operation: OpSetComment, At: "services.web.image", Where: strategy.CommentTrailing, Value: "pinned 2026-08-13"},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "services:\n  web:\n    image: nginx:1.27 # pinned 2026-08-13\n" {
		t.Errorf("got %q", got)
	}
}

func TestPreviewOfACommentWritesNothing(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\n"
	path := write(t, "compose.yaml", src)
	res, err := Preview(Request{File: path, Ops: []Op{
		{Operation: OpSetComment, At: "services.web", Where: strategy.CommentAbove, Value: "hello"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Diff, "+  # hello") {
		t.Errorf("the preview diff does not show the comment:\n%s", res.Diff)
	}
	if got := read(t, path); got != src {
		t.Error("preview wrote to the file")
	}
	// AD-14: the range the preview reports is the range the write touches.
	if res.Ops[0].Range.Start != res.Ops[0].Range.End {
		t.Errorf("an insert reported a non-empty range %d-%d", res.Ops[0].Range.Start, res.Ops[0].Range.End)
	}
}

// AD-14, and the one that would otherwise be a check that could not fail: a
// delete removes the whitespace in front of a trailing comment as well as the
// comment, so the range a preview REPORTS has to include that whitespace. A
// delete located through the set range would refuse and succeed identically and
// still describe a narrower edit than the one it performs.
func TestThePreviewedRangeOfACommentDeleteIsTheRangeItRemoves(t *testing.T) {
	src := "services:\n  web:\n    image: nginx   # pinned\n"
	path := write(t, "compose.yaml", src)
	res, err := Preview(Request{File: path, Ops: []Op{
		{Operation: OpDeleteComment, At: "services.web.image", Where: strategy.CommentTrailing},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Ops[0].Before; got != "   # pinned" {
		t.Fatalf("the reported range holds %q; a delete removes the gap too", got)
	}
	// And the write agrees with it, byte for byte.
	if _, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpDeleteComment, At: "services.web.image", Where: strategy.CommentTrailing},
	}}); err != nil {
		t.Fatal(err)
	}
	want := src[:res.Ops[0].Range.Start] + src[res.Ops[0].Range.End:]
	if got := read(t, path); got != want {
		t.Errorf("the write touched a different span.\n got: %q\nwant: %q", got, want)
	}
}

func TestCommentRefusalsAreClassified(t *testing.T) {
	cases := []struct {
		name string
		src  string
		op   Op
		is   error
		slug string
	}{
		{
			"no comment to delete",
			"services:\n  web:\n    image: nginx\n",
			Op{Operation: OpDeleteComment, At: "services.web.image", Where: strategy.CommentAbove},
			strategy.ErrNoComment, "no-comment",
		},
		{
			"empty text",
			"services:\n  web:\n    image: nginx\n",
			Op{Operation: OpSetComment, At: "services.web.image", Where: strategy.CommentTrailing, Value: "  "},
			strategy.ErrCommentText, "comment-text",
		},
		{
			"two lines trailing",
			"services:\n  web:\n    image: nginx\n",
			Op{Operation: OpSetComment, At: "services.web.image", Where: strategy.CommentTrailing, Value: "a\nb"},
			strategy.ErrCommentText, "comment-text",
		},
		{
			"block scalar",
			"services:\n  web:\n    command: |\n      echo hi\n",
			Op{Operation: OpSetComment, At: "services.web.command", Where: strategy.CommentTrailing, Value: "x"},
			strategy.ErrCommentTarget, "comment-target",
		},
		{
			"unknown position",
			"services:\n  web:\n    image: nginx\n",
			Op{Operation: OpSetComment, At: "services.web.image", Where: "beside", Value: "x"},
			strategy.ErrCommentTarget, "comment-target",
		},
		{
			"flow collection",
			"services:\n  web:\n    ports: [\"80:80\"]\n",
			Op{Operation: OpSetComment, At: "services.web.ports", Where: strategy.CommentTrailing, Value: "x"},
			strategy.ErrFlowStyle, "flow-style",
		},
	}
	for _, c := range cases {
		path := write(t, "compose.yaml", c.src)
		_, err := Apply(Request{File: path, Ops: []Op{c.op}})
		if !errors.Is(err, c.is) {
			t.Errorf("%s: error is %v, want %v", c.name, err, c.is)
			continue
		}
		if !Refused(err) {
			t.Errorf("%s: not classified as a refusal, so the reader is told the tool broke", c.name)
		}
		if got := Reason(err); got != c.slug {
			t.Errorf("%s: Reason is %q, want %q", c.name, got, c.slug)
		}
		if got := read(t, path); got != c.src {
			t.Errorf("%s: a refusal wrote to the file", c.name)
		}
		// Preview must refuse whatever the write refuses.
		if _, perr := Preview(Request{File: path, Ops: []Op{c.op}}); !errors.Is(perr, c.is) {
			t.Errorf("%s: preview error is %v, want %v", c.name, perr, c.is)
		}
	}
}

// A comment operation is a YAML operation and must not be usable on a
// Dockerfile, for the reason ErrWrongGrammar exists.
func TestACommentOperationIsAYAMLOperation(t *testing.T) {
	if got := OpSetComment.Grammar(); got != "yaml" {
		t.Errorf("set_comment is %q", got)
	}
	if got := OpDeleteComment.Grammar(); got != "yaml" {
		t.Errorf("delete_comment is %q", got)
	}
	path := write(t, "compose.yaml", "services:\n  web:\n    image: nginx\n")
	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpSetComment, At: "services.web", Where: strategy.CommentAbove, Value: "a"},
		{Operation: OpInsertStage, Value: "alpine"},
	}})
	if !errors.Is(err, ErrMixedGrammar) {
		t.Fatalf("error is %v, want ErrMixedGrammar", err)
	}
}
