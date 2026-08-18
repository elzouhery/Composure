package strategy

// Story 9.1 — comments as things that can be authored, not only preserved.
//
// Every check here compares BUFFERS. The engine's characteristic failure is a
// confident wrong answer, and a comment operation that reported success while
// eating the line below it would pass any "no error" test ever written.

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// splice out [start,end) and put back what was there: the result must be the
// source. This is the story's own acceptance criterion, as a function.
func substituted(out []byte, start int, newLen int, old string) string {
	return string(out[:start]) + old + string(out[start+newLen:])
}

func TestCommentFixtureCarriesMoreThanOneComment(t *testing.T) {
	src := string(fixture(t, "e44-comments-everywhere.yml"))
	if n := strings.Count(src, "#"); n < 8 {
		t.Fatalf("e44 has %d hashes; a comment fixture with one comment in it cannot tell "+
			"'replaced the right run' from 'replaced a run'", n)
	}
	if !strings.Contains(src, `"a value with a # inside it"`) {
		t.Fatal("e44 no longer carries a `#` inside a quoted value, which is the trailing-comment trap")
	}
}

// ---------------------------------------------------------------- above ---

func TestReplacingACommentBlockAboveAKey(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	start, end, found, err := CommentRange(src, []string{"services", "web"}, CommentAbove)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	old := string(src[start:end])
	if strings.Count(old, "\n") != 3 {
		t.Fatalf("the run above `web` is %q; the fixture's three-line block is the point of it", old)
	}

	out, err := (Splice{}).SetComment(src, []string{"services", "web"}, CommentAbove, "one line now")
	if err != nil {
		t.Fatal(err)
	}
	want := "  # one line now\n"
	if got := string(out[start : start+len(want)]); got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
	if back := substituted(out, start, len(want), old); back != string(src) {
		t.Errorf("bytes outside the comment run changed.\n got: %q\nwant: %q", back, string(src))
	}
}

func TestAddingACommentAboveAKeyThatHasNone(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	path := []string{"services", "db"}
	start, end, found, err := CommentRange(src, path, CommentAbove)
	if err != nil {
		t.Fatal(err)
	}
	if found || start != end {
		t.Fatalf("found=%v start=%d end=%d, want an empty span at the insertion point", found, start, end)
	}
	out, err := (Splice{}).SetComment(src, path, CommentAbove, "the database")
	if err != nil {
		t.Fatal(err)
	}
	ins := "  # the database\n"
	if got := string(out[start : start+len(ins)]); got != ins {
		t.Errorf("wrote %q, want %q", got, ins)
	}
	if back := substituted(out, start, len(ins), ""); back != string(src) {
		t.Errorf("the insert touched other bytes.\n got: %q\nwant: %q", back, string(src))
	}
}

// A two-line text is ONE comment written as two lines. attachedCommentStart
// already treats a run as one thing and DeleteKey already removes it as one;
// a per-line address would be a second model of the same bytes.
func TestAMultiLineCommentIsOneThing(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	path := []string{"services", "db"}
	out, err := (Splice{}).SetComment(src, path, CommentAbove, "first\nsecond")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "  # first\n  # second\n  db:") {
		t.Fatalf("two lines were not written as two comment lines:\n%s", out)
	}
	// And it reads back as ONE comment covering both lines.
	start, end, found, err := CommentRange(out, path, CommentAbove)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got := string(out[start:end]); got != "  # first\n  # second\n" {
		t.Errorf("the run reads back as %q", got)
	}
}

// A blank line breaks attachment. That is attachedCommentStart's own rule and
// this pins it: the run above the blank must not be touched.
func TestABlankLineBreaksTheAttachment(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	path := []string{"services", "web", "restart"}
	_, _, found, err := CommentRange(src, path, CommentAbove)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("the comment above the blank line was reported as attached to `restart`")
	}
	out, err := (Splice{}).SetComment(src, path, CommentAbove, "always up")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\n\n    # always up\n    restart: always") {
		t.Fatalf("the new comment did not land between the blank and the key:\n%s", out)
	}
	if !strings.Contains(string(out), "is NOT attached to it.\n\n") {
		t.Error("the unattached run above the blank line was disturbed")
	}
}

// ------------------------------------------------------------- trailing ---

func TestReplacingATrailingCommentKeepsTheGap(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	path := []string{"services", "web", "image"}
	start, end, found, err := CommentRange(src, path, CommentTrailing)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	old := string(src[start:end])
	out, err := (Splice{}).SetComment(src, path, CommentTrailing, "now unpinned")
	if err != nil {
		t.Fatal(err)
	}
	want := "# now unpinned"
	if got := string(out[start : start+len(want)]); got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
	// The file wrote TWO spaces before the hash. Normalising them to one is a
	// change to a line nobody asked about.
	if !strings.Contains(string(out), "image: nginx  # now unpinned\n") {
		t.Errorf("the gap before the hash was normalised:\n%s", out)
	}
	if back := substituted(out, start, len(want), old); back != string(src) {
		t.Errorf("bytes outside the comment changed")
	}
}

// The trap. `note: "a value with a # inside it"   # and a real trailing
// comment` — a scan that takes the first `#` on the line writes into the value.
func TestAHashInsideAValueIsNotTheComment(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	path := []string{"services", "web", "labels", "note"}
	start, end, found, err := CommentRange(src, path, CommentTrailing)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got := string(src[start:end]); got != "# and a real trailing comment" {
		t.Fatalf("the comment span is %q — the `#` inside the quoted value was taken for the comment", got)
	}
	out, err := (Splice{}).SetComment(src, path, CommentTrailing, "replaced")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `note: "a value with a # inside it"   # replaced`) {
		t.Fatalf("the value was damaged:\n%s", out)
	}
}

func TestAddingATrailingCommentToALineThatHasNone(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	out, err := (Splice{}).SetComment(src, []string{"services", "web", "image"}, CommentTrailing, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "services:\n  web:\n    image: nginx # pinned\n" {
		t.Errorf("got %q", got)
	}
}

// A key whose value is a mapping — `services:` — takes a trailing comment
// after the colon. Nothing here has a scalar to measure from, so the colon is.
func TestATrailingCommentOnAMappingKey(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	out, err := (Splice{}).SetComment(src, []string{"services"}, CommentTrailing, "everything we run")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "services: # everything we run\n  web:\n    image: nginx\n" {
		t.Errorf("got %q", got)
	}
}

func TestATrailingCommentIsRefusedWhereTheValueEndIsAGuess(t *testing.T) {
	cases := map[string]struct {
		src  string
		path []string
		want error
	}{
		"block scalar": {
			src:  "services:\n  web:\n    command: |\n      echo hi\n",
			path: []string{"services", "web", "command"},
			want: ErrCommentTarget,
		},
		"flow sequence": {
			src:  "services:\n  web:\n    ports: [\"80:80\"]\n",
			path: []string{"services", "web", "ports"},
			want: ErrFlowStyle,
		},
		"flow mapping": {
			src:  "services:\n  web: {image: nginx}\n",
			path: []string{"services", "web"},
			want: ErrFlowStyle,
		},
		"alias": {
			src:  "x: &a nginx\nservices:\n  web:\n    image: *a\n",
			path: []string{"services", "web", "image"},
			want: ErrCommentTarget,
		},
	}
	for name, c := range cases {
		_, err := (Splice{}).SetComment([]byte(c.src), c.path, CommentTrailing, "nope")
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error is %v, want %v", name, err, c.want)
		}
		if _, _, _, err := CommentRange([]byte(c.src), c.path, CommentTrailing); !errors.Is(err, c.want) {
			t.Errorf("%s: CommentRange error is %v, want %v — a preview that did not refuse "+
				"what the write refuses is a lie the reader cannot check", name, err, c.want)
		}
	}
}

// ---------------------------------------------------------------- text ---

func TestCommentTextThatWillNotBeWrittenAsAComment(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	path := []string{"services", "web", "image"}
	for name, tc := range map[string]struct{ where, text string }{
		"empty":                {CommentAbove, "   "},
		"trailing line break":  {CommentTrailing, "one\ntwo"},
		"carriage return":      {CommentAbove, "one\rtwo"},
		"trailing empty":       {CommentTrailing, ""},
		"above only a hash":    {CommentAbove, "#"},
		"trailing only a hash": {CommentTrailing, "# "},
	} {
		if _, err := (Splice{}).SetComment(src, path, tc.where, tc.text); !errors.Is(err, ErrCommentText) {
			t.Errorf("%s: error is %v, want ErrCommentText", name, err)
		}
	}
}

// The reader's own `#` is theirs; the engine does not double it.
func TestTheReadersOwnHashIsNotDoubled(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	out, err := (Splice{}).SetComment(src, []string{"services", "web", "image"}, CommentTrailing, "# pinned")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "image: nginx # pinned\n") {
		t.Errorf("got %q", out)
	}
	out, err = (Splice{}).SetComment(src, []string{"services", "web"}, CommentAbove, "#one\n# two")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "  # one\n  # two\n  web:") {
		t.Errorf("got %q", out)
	}
}

// --------------------------------------------------------------- delete ---

func TestDeletingComments(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	out, err := (Splice{}).DeleteComment(src, []string{"services", "web"}, CommentAbove)
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := CommentDeleteRange(src, []string{"services", "web"}, CommentAbove)
	if err != nil {
		t.Fatal(err)
	}
	if back := substituted(out, start, 0, string(src[start:end])); back != string(src) {
		t.Errorf("the delete took bytes it does not own")
	}

	// Trailing: the gap goes with the comment, so no trailing whitespace is
	// left behind on the line.
	out, err = (Splice{}).DeleteComment(src, []string{"services", "web", "image"}, CommentTrailing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "    image: nginx\n") {
		t.Fatalf("the line kept its trailing whitespace:\n%s", out)
	}
}

// Not a silent no-op and not ErrNoChange. "There was nothing to delete" and
// "the delete did nothing" are different sentences.
func TestDeletingACommentThatIsNotThere(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	for _, where := range []string{CommentAbove, CommentTrailing} {
		if _, err := (Splice{}).DeleteComment(src, []string{"services", "web", "image"}, where); !errors.Is(err, ErrNoComment) {
			t.Errorf("%s: error is %v, want ErrNoComment", where, err)
		}
		if _, _, err := CommentDeleteRange(src, []string{"services", "web", "image"}, where); !errors.Is(err, ErrNoComment) {
			t.Errorf("%s: CommentDeleteRange error is %v, want ErrNoComment", where, err)
		}
	}
}

func TestAnUnknownCommentPositionIsRefused(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx\n")
	if _, err := (Splice{}).SetComment(src, []string{"services"}, "beside", "x"); !errors.Is(err, ErrCommentTarget) {
		t.Errorf("error is %v, want ErrCommentTarget", err)
	}
}

// ------------------------------------------------------- endings and BOM ---

func TestCommentsCarryTheFilesOwnLineEnding(t *testing.T) {
	src := []byte("services:\r\n  web:\r\n    image: nginx\r\n")
	out, err := (Splice{}).SetComment(src, []string{"services", "web"}, CommentAbove, "the front door")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "services:\r\n  # the front door\r\n  web:\r\n    image: nginx\r\n" {
		t.Errorf("got %q", got)
	}
	// On the bytes, not on a diff: a line-oriented comparison cannot see a
	// missing "\r", which is what let story 7.1's defect ship.
	for i := 0; i+1 <= len(out); i++ {
		if out[i] == '\n' && (i == 0 || out[i-1] != '\r') {
			t.Fatalf("byte %d is an LF with no CR before it:\n%q", i, out)
		}
	}

	out, err = (Splice{}).SetComment(src, []string{"services", "web", "image"}, CommentTrailing, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "services:\r\n  web:\r\n    image: nginx # pinned\r\n" {
		t.Errorf("trailing on CRLF: got %q", got)
	}
}

func TestTheByteOrderMarkSurvivesACommentAtTheHead(t *testing.T) {
	src := append([]byte("\xef\xbb\xbf"), []byte("services:\n  web:\n    image: nginx\n")...)
	out, err := (Splice{}).SetComment(src, []string{"services"}, CommentAbove, "everything we run")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "\xef\xbb\xbf# everything we run\nservices:\n  web:\n    image: nginx\n" {
		t.Errorf("got %q", got)
	}
}

// A file with no trailing newline: a comment above its LAST key still needs a
// line ending, and it takes the file's own kind.
func TestACommentAboveTheLastKeyOfAFileWithNoTrailingNewline(t *testing.T) {
	src := []byte("services:\r\n  web:\r\n    image: nginx")
	out, err := (Splice{}).SetComment(src, []string{"services", "web", "image"}, CommentAbove, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "services:\r\n  web:\r\n    # pinned\r\n    image: nginx" {
		t.Errorf("got %q", got)
	}
}

// The property comment ownership rests on, pinned rather than assumed: a
// comment above a key travels with the key when the key is deleted.
func TestAnAttachedCommentGoesWithADeletedKey(t *testing.T) {
	src := fixture(t, "e44-comments-everywhere.yml")
	out, err := (Splice{}).DeleteKey(src, []string{"services", "web"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "web is the front door") {
		t.Error("the comment attached to `web` outlived it")
	}
	if !strings.Contains(string(out), "# The services block.") {
		t.Error("a comment attached to something else went with it")
	}
}

// The three properties of story 7.1 asserted on a PERMANENT fixture rather than
// an inline literal, all at once and on one file: a byte order mark, CRLF
// endings throughout, and no trailing newline. Each of the three has broken
// something in this engine before, and a comment operation touches all three at
// once — it inserts a line, it may insert it at the head of the file, and it
// may insert it above the last line.
func TestCommentsAgainstTheBomCrlfFixture(t *testing.T) {
	src := fixture(t, "e47-bom-crlf-comments.yml")
	if !bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		t.Fatal("e47 has lost its byte order mark, which is half of what it is for")
	}
	if bytes.Count(src, []byte("\n")) != bytes.Count(src, []byte("\r\n")) {
		t.Fatal("e47 has an LF that is not part of a CRLF; the fixture no longer proves what it claims")
	}
	if bytes.HasSuffix(src, []byte("\n")) {
		t.Fatal("e47 has gained a trailing newline; the no-final-newline case is gone")
	}

	crlfOnly := func(t *testing.T, out []byte, what string) {
		t.Helper()
		if !bytes.HasPrefix(out, []byte("\xef\xbb\xbf")) {
			t.Errorf("%s: the byte order mark is gone", what)
		}
		for i := range out {
			if out[i] == '\n' && (i == 0 || out[i-1] != '\r') {
				t.Errorf("%s: byte %d is an LF with no CR before it:\n%q", what, i, out)
				return
			}
		}
	}

	// At the head of the file, where the mark is.
	out, err := (Splice{}).SetComment(src, []string{"services"}, CommentAbove, "everything we run")
	if err != nil {
		t.Fatal(err)
	}
	crlfOnly(t, out, "above the first key")
	if !bytes.Contains(out, []byte("# everything we run\r\nservices:")) {
		t.Errorf("above the first key: %q", out)
	}

	// Above the LAST line of a file that has no trailing newline.
	out, err = (Splice{}).SetComment(src, []string{"services", "db", "image"}, CommentAbove, "the database")
	if err != nil {
		t.Fatal(err)
	}
	crlfOnly(t, out, "above the last line")
	if bytes.HasSuffix(out, []byte("\n")) {
		t.Error("above the last line: a trailing newline was invented")
	}

	// Replacing a two-line run, and a trailing comment, on the same file.
	out, err = (Splice{}).SetComment(src, []string{"services", "web"}, CommentAbove, "one line now")
	if err != nil {
		t.Fatal(err)
	}
	crlfOnly(t, out, "replacing a run")
	if bytes.Contains(out, []byte("A fixture with one comment")) {
		t.Error("replacing a run: only the first line of it went")
	}

	out, err = (Splice{}).SetComment(src, []string{"services", "web", "image"}, CommentTrailing, "pinned by ops")
	if err != nil {
		t.Fatal(err)
	}
	crlfOnly(t, out, "a trailing comment")
	if !bytes.Contains(out, []byte("image: nginx  # pinned by ops\r\n")) {
		t.Errorf("a trailing comment: the gap was normalised:\n%q", out)
	}

	// And a delete puts the file back exactly as it was.
	out, err = (Splice{}).DeleteComment(src, []string{"services", "web"}, CommentAbove)
	if err != nil {
		t.Fatal(err)
	}
	crlfOnly(t, out, "deleting a run")
	start, end, err := CommentDeleteRange(src, []string{"services", "web"}, CommentAbove)
	if err != nil {
		t.Fatal(err)
	}
	if back := string(out[:start]) + string(src[start:end]) + string(out[start:]); back != string(src) {
		t.Errorf("deleting a run took bytes it does not own")
	}
}
