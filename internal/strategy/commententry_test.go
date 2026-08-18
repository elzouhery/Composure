package strategy

// Story 9.1 and 9.2 together: a comment on an ENTRY of a list, and the two
// places the comment engine answered about a position it had not located.
//
// Three defects an adversarial review found, each of them this engine's
// characteristic failure rather than a crash:
//
//	D2  `locate` answers a sequence entry with the VALUE token's column, so the
//	    run above the entry was looked for at the value's indent and never
//	    found. `delete_comment above ports[0]` said `no-comment` with the
//	    comment plainly there, and `set_comment above ports[1]` wrote a second
//	    one two columns deeper, attached to nothing.
//	D3  `above` had no flow guard where `trailing` has one, so a comment for an
//	    entry of `test: ["CMD", …]` was written above the `test:` line at the
//	    entry's column — owned by nothing, and disagreeing with the refusal the
//	    same target gets at `trailing`.
//	    `## mine` came back as `# # mine`: one marker stripped and one written,
//	    so a doubled marker could not survive a read followed by a write.
//
// Every check compares BUFFERS, for the reason the rest of this file does: a
// comment operation that reported success while writing at the wrong indent
// passes any "no error" test ever written.

import (
	"errors"
	"strings"
	"testing"
)

// The fixture cannot quietly become the useless kind: comments on entries, in
// the middle of lists, with a repeat under them.
func TestEntryCommentFixtureCommentsEntriesAndNotOnlyKeys(t *testing.T) {
	src := string(fixture(t, "e55-entry-comments.yml"))
	if !strings.Contains(src, "      # the public edge\n      - \"8080:80\"\n") {
		t.Fatal("e55 no longer comments a list ENTRY at the dash's indent, which is the whole of it")
	}
	if strings.Count(src, `- "8080:80"`) < 2 {
		t.Fatal("e55 no longer repeats a port; an off-by-one would be invisible against it")
	}
	if !strings.Contains(src, "      ## a section marker") {
		t.Fatal("e55 has lost its doubled marker, which is the round-trip case")
	}
}

// D2. The run above an entry is at the DASH's indent, not the value's.
func TestACommentAboveAListEntryIsFoundAtTheDashesIndent(t *testing.T) {
	src := fixture(t, "e55-entry-comments.yml")
	for _, c := range []struct {
		path []string
		want string
	}{
		{parse("services.web.ports[0]"), "      # the public edge\n"},
		{parse("services.web.ports[1]"), "      # the TLS edge\n      # and this run is two lines, so replacing one line of it is visible\n"},
	} {
		start, end, found, err := CommentRange(src, c.path, CommentAbove)
		if err != nil {
			t.Fatalf("%v: %v", c.path, err)
		}
		if !found {
			t.Fatalf("%v: the comment above the entry was not found; it is there, at the dash's indent", c.path)
		}
		if got := string(src[start:end]); got != c.want {
			t.Errorf("%v: the run reads %q, want %q", c.path, got, c.want)
		}
	}

	// And the entry that genuinely has none still has none: an indent that
	// matched everything would pass the two checks above.
	if _, _, found, err := CommentRange(src, parse("services.web.ports[2]"), CommentAbove); err != nil || found {
		t.Errorf("ports[2] reports found=%v err=%v; the entry above it is not its comment", found, err)
	}
	if _, err := (Splice{}).DeleteComment(src, parse("services.web.ports[2]"), CommentAbove); !errors.Is(err, ErrNoComment) {
		t.Errorf("deleting a comment ports[2] does not have: %v, want ErrNoComment", err)
	}
}

// The other half: a delete that says `no-comment` for a comment that is there
// is the sentence the pane printed, and a write at the wrong indent is what it
// did next.
func TestDeletingAndReplacingAListEntrysComment(t *testing.T) {
	src := fixture(t, "e55-entry-comments.yml")
	path := parse("services.web.ports[1]")

	start, end, err := CommentDeleteRange(src, path, CommentAbove)
	if err != nil {
		t.Fatalf("CommentDeleteRange: %v", err)
	}
	out, err := (Splice{}).DeleteComment(src, path, CommentAbove)
	if err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if back := string(out[:start]) + string(src[start:end]) + string(out[start:]); back != string(src) {
		t.Errorf("the delete took bytes it does not own.\n got: %q\nwant: %q", back, string(src))
	}
	if strings.Contains(string(out), "# the TLS edge") {
		t.Error("the run was reported deleted and is still there")
	}
	if !strings.Contains(string(out), "# the public edge") {
		t.Error("the delete took the run above the WRONG entry")
	}

	out, err = (Splice{}).SetComment(src, path, CommentAbove, "the TLS edge, now one line")
	if err != nil {
		t.Fatalf("SetComment: %v", err)
	}
	want := "      # the TLS edge, now one line\n      - \"8443:443\"\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("the replacement did not land at the entry's own indent:\n%s", out)
	}
	if strings.Count(string(out), "#") != strings.Count(string(src), "#")-1 {
		t.Errorf("a second comment was added rather than the run replaced:\n%s", out)
	}
}

// An entry with no comment above it takes one at the dash's indent — eight
// spaces is a comment that lines up with the value and belongs to nothing.
func TestAddingACommentAboveAnEntryThatHasNone(t *testing.T) {
	src := fixture(t, "e55-entry-comments.yml")
	path := parse("services.web.ports[2]")
	start, end, found, err := CommentRange(src, path, CommentAbove)
	if err != nil || found || start != end {
		t.Fatalf("found=%v start=%d end=%d err=%v, want an empty span", found, start, end, err)
	}
	out, err := (Splice{}).SetComment(src, path, CommentAbove, "the repeat")
	if err != nil {
		t.Fatal(err)
	}
	ins := "      # the repeat\n"
	if got := string(out[start : start+len(ins)]); got != ins {
		t.Errorf("wrote %q, want %q — an entry's comment takes the dash's indent", got, ins)
	}
	if back := substituted(out, start, len(ins), ""); back != string(src) {
		t.Errorf("the insert touched other bytes.\n got: %q\nwant: %q", back, string(src))
	}
}

// D3. `above` takes the same refusal `trailing` takes inside a flow
// collection. The two positions must agree about one target: an entry of
// `test: ["CMD", …]` has no line of its own, so there is nowhere above it for
// a comment to go, and writing one above the `test:` line at the entry's
// column attaches it to nothing.
func TestACommentInsideAFlowCollectionIsRefusedAtBothPositions(t *testing.T) {
	src := fixture(t, "e55-entry-comments.yml")
	for _, where := range []string{CommentAbove, CommentTrailing} {
		path := parse("services.web.healthcheck.test[1]")
		if _, _, _, err := CommentRange(src, path, where); !errors.Is(err, ErrFlowStyle) {
			t.Errorf("CommentRange %s: error is %v, want ErrFlowStyle", where, err)
		}
		if _, err := (Splice{}).SetComment(src, path, where, "nope"); !errors.Is(err, ErrFlowStyle) {
			t.Errorf("SetComment %s: error is %v, want ErrFlowStyle", where, err)
		}
		if _, err := (Splice{}).DeleteComment(src, path, where); !errors.Is(err, ErrFlowStyle) {
			t.Errorf("DeleteComment %s: error is %v, want ErrFlowStyle", where, err)
		}
	}

	// The key that HOLDS the flow collection still takes a comment above it —
	// a refusal that swallowed this one would be a different defect wearing
	// the fix's clothes.
	out, err := (Splice{}).SetComment(src, parse("services.web.healthcheck.test"), CommentAbove, "the check")
	if err != nil {
		t.Fatalf("above the key that holds a flow collection: %v", err)
	}
	if !strings.Contains(string(out), "      # the check\n      test: [") {
		t.Errorf("the comment above the flow KEY did not land:\n%s", out)
	}
}

// The reader's doubled marker is theirs. `## mine` read out of the file and
// written back unchanged has to produce `## mine`, or a `##` comment cannot
// survive being opened.
func TestADoubledMarkerRoundTrips(t *testing.T) {
	src := fixture(t, "e55-entry-comments.yml")
	path := parse("services.web.command[0]")
	start, end, found, err := CommentRange(src, path, CommentAbove)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	run := string(src[start:end])
	if got, want := run, "      ## a section marker the reader wrote with two hashes of their own\n"; got != want {
		t.Fatalf("the run reads %q, want %q", got, want)
	}
	// What the pane hands back is the run without its indent and without the
	// marker the engine writes — `commentText` in extension/host/edit.ts keeps
	// a doubled marker for exactly this reason.
	text := "## a section marker the reader wrote with two hashes of their own"
	out, err := (Splice{}).SetComment(src, path, CommentAbove, text)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("writing a `##` comment back unchanged changed the file.\n got: %q\nwant: %q",
			string(out), string(src))
	}

	// The half the round trip above CANNOT fail on, and the one the review
	// reported: a NEW `##` comment, where there is no marker in the file to
	// copy and the site's own `# ` is used. `## mine` came out as `# # mine` —
	// one marker stripped, one written, and the reader's second one moved into
	// their sentence.
	out, err = (Splice{}).SetComment(src, parse("services.web.ports[2]"), CommentAbove, "## the repeat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "      ## the repeat\n") {
		t.Errorf("a new `##` comment was not written as the reader typed it:\n%s", out)
	}
	if strings.Contains(string(out), "# # the repeat") {
		t.Error("the reader's second marker was pushed into their sentence")
	}
}

// A blank line inside a run is a bare marker. `# ` leaves a trailing space on
// a line nobody typed one on, and trailing whitespace is a change to a line
// this operation was not asked to change.
func TestABlankLineInsideARunCarriesNoTrailingSpace(t *testing.T) {
	src := fixture(t, "e55-entry-comments.yml")
	out, err := (Splice{}).SetComment(src, parse("services.web.ports[0]"), CommentAbove, "first\n\nthird")
	if err != nil {
		t.Fatal(err)
	}
	want := "      # first\n      #\n      # third\n      - \"8080:80\"\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("got:\n%s\nwant to contain %q", out, want)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("a line was written with trailing whitespace: %q", line)
		}
	}
}
