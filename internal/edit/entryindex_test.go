package edit

// Story 9.2, the classifier's half.
//
// The engine's refusal is only useful if the layer above it tells the reader
// the tool DECLINED. Before this, `services.web.ports[9]` on a three-entry list
// was classified `absent` with `plan: insert_key` — a confident wrong answer
// with an invitation attached, because insert_key on a sequence adds nothing —
// and the write path returned an unclassified error, so `Refused` was false.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/strategy"
)

func edgeFixture(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

func TestAnEntryTheListDoesNotHaveIsClassifiedAsSuch(t *testing.T) {
	src := edgeFixture(t, "e43-repeated-list-entries.yml")
	a := Classify([]byte(src), "services.web.ports[9]")
	if a.Reason != ReasonEntryIndex {
		t.Errorf("reason is %q, want %q", a.Reason, ReasonEntryIndex)
	}
	if a.Editable {
		t.Error("it is reported editable")
	}
	if a.Plan != "" {
		t.Errorf("plan is %q; insert_key on a sequence adds nothing and offering it is the defect", a.Plan)
	}
	if !strings.Contains(a.Detail, "3") {
		t.Errorf("the sentence does not say how many entries the list has: %q", a.Detail)
	}
}

// The same question asked in the spellings a library caller can produce, which
// is where the two implementations of one address disagreed. `Classify` read an
// index too large for an int as an ABSENT KEY and offered `insert_key` — the
// pre-9.2 answer, and the confident-wrong-answer-with-an-invitation this story
// exists to delete — while `strategy` wrapped the same segment to entry 0 and
// wrote it. One implementation now: strategy.EntryIndex, both sides.
func TestEveryIndexSpellingTheListDoesNotHaveIsAnEntryIndex(t *testing.T) {
	src := edgeFixture(t, "e43-repeated-list-entries.yml")
	for _, seg := range []string{
		"18446744073709551616", // wrapped to entry 0
		"18446744073709551617", // wrapped to entry 1
		"9223372036854775808",  // one past MaxInt64
		"-1", "+1", "1 ", " 1",
	} {
		a := Classify([]byte(src), "services.web.ports["+seg+"]")
		if a.Reason != ReasonEntryIndex {
			t.Errorf("ports[%s]: reason is %q, want %q", seg, a.Reason, ReasonEntryIndex)
		}
		if a.Plan != "" {
			t.Errorf("ports[%s]: plan is %q; insert_key on a sequence adds nothing", seg, a.Plan)
		}
		if a.Editable {
			t.Errorf("ports[%s]: reported editable", seg)
		}
	}
}

// And the write path agrees with the classifier, on the bytes. The overflow is
// the one that mattered: it returned no error and edited a real entry, so a
// check that only asserted an error would have passed against the defect if the
// error had been nil for a different reason.
func TestAnOverflowingIndexWritesNothing(t *testing.T) {
	src := edgeFixture(t, "e43-repeated-list-entries.yml")
	for _, seg := range []string{"18446744073709551616", "18446744073709551617", "-1", "1 "} {
		path := write(t, "compose.yaml", src)
		_, err := Apply(Request{File: path, Ops: []Op{
			{Operation: OpReplaceScalar, At: "services.web.ports[" + seg + "]", Value: "9999:9999"},
		}})
		if !errors.Is(err, strategy.ErrEntryIndex) {
			t.Errorf("ports[%s]: error is %v, want ErrEntryIndex", seg, err)
		}
		if !Refused(err) || Reason(err) != "entry-index" {
			t.Errorf("ports[%s]: Refused=%v Reason=%q", seg, Refused(err), Reason(err))
		}
		if got := read(t, path); got != src {
			t.Errorf("ports[%s]: the file was written:\n%s", seg, got)
		}
	}
}

// An entry that IS there is ordinary and editable — the other half, without
// which the check above passes for a classifier that refuses every index.
func TestAnEntryTheListDoesHaveIsEditable(t *testing.T) {
	src := edgeFixture(t, "e43-repeated-list-entries.yml")
	for _, p := range []string{
		"services.web.ports[2]",
		"services.web.command[2]",
		"services.web.healthcheck.test[1]",
	} {
		if a := Classify([]byte(src), p); !a.Editable {
			t.Errorf("%s: not editable (%s: %s)", p, a.Reason, a.Detail)
		}
	}
}

func TestReplacingAnEntryTheListDoesNotHaveIsARefusal(t *testing.T) {
	src := edgeFixture(t, "e43-repeated-list-entries.yml")
	path := write(t, "compose.yaml", src)
	_, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.ports[9]", Value: "x"},
	}})
	if !errors.Is(err, strategy.ErrEntryIndex) {
		t.Fatalf("error is %v, want ErrEntryIndex", err)
	}
	if !Refused(err) || Reason(err) != "entry-index" {
		t.Errorf("Refused=%v Reason=%q, want an entry-index refusal", Refused(err), Reason(err))
	}
	if got := read(t, path); got != src {
		t.Error("a refusal wrote to the file")
	}
}

// Editing one entry of a repeated list, end to end, asserted on the BYTES.
func TestReplacingTheMiddleEntryTouchesOnlyIt(t *testing.T) {
	src := edgeFixture(t, "e43-repeated-list-entries.yml")
	path := write(t, "compose.yaml", src)
	res, err := Apply(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.healthcheck.test[1]", Value: "curl"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 {
		t.Errorf("%d lines changed, want 2", res.Changed)
	}
	got := read(t, path)
	want := strings.Replace(src, `"CMD", "wget"`, `"CMD", "curl"`, 1)
	if got != want {
		t.Errorf("bytes differ.\n got: %q\nwant: %q", got, want)
	}
}

// AD-19 is the whole answer to "an index moves when the list does". Asserted
// here rather than assumed, because index addressing is what makes it
// load-bearing: entry 1 of a list is a different value once entry 0 is gone.
func TestAStagedEntryEditIsRefusedAfterTheListChanges(t *testing.T) {
	// THREE entries, shrinking to two. A list that shrinks below the staged
	// index is caught by ErrEntryIndex before the Expect is ever compared, so a
	// two-entry fixture would prove the wrong thing: the case that matters is
	// the one where the index STILL RESOLVES and now points at a different
	// value, which is the silent rebase AD-19 exists to forbid.
	before := "services:\n  web:\n    image: nginx\n    ports:\n      - \"8080:80\"\n      - \"8443:443\"\n      - \"9000:9000\"\n"
	path := write(t, "compose.yaml", before)
	staged, err := Preview(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.ports[1]", Value: "9443:443"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	expect := &Expect{
		Start: staged.Ops[0].Range.Start,
		End:   staged.Ops[0].Range.End,
		Text:  staged.Ops[0].Before,
	}

	// The reader deletes the first entry in their editor. Entry 1 is now a
	// different string at a different offset.
	after := "services:\n  web:\n    image: nginx\n    ports:\n      - \"8443:443\"\n      - \"9000:9000\"\n"
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.ports[1]", Value: "9443:443", Expect: expect},
	}})
	if !errors.Is(err, ErrStaleRange) {
		t.Fatalf("error is %v, want ErrStaleRange — the staged index was rebased onto a list that has moved", err)
	}
	if got := read(t, path); got != after {
		t.Error("a stale-range refusal wrote to the file")
	}

	// And the other shape: the list shrinks BELOW the staged index. That is
	// ErrEntryIndex rather than ErrStaleRange, and it is still a refusal that
	// writes nothing. Both are asserted because a check that only knew one of
	// them would pass against an engine that had lost the other.
	shorter := "services:\n  web:\n    image: nginx\n    ports:\n      - \"8443:443\"\n"
	if err := os.WriteFile(path, []byte(shorter), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(Request{File: path, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.web.ports[1]", Value: "9443:443", Expect: expect},
	}})
	if !errors.Is(err, strategy.ErrEntryIndex) {
		t.Fatalf("error is %v, want ErrEntryIndex", err)
	}
	if !Refused(err) {
		t.Error("it is not classified as a refusal")
	}
	if got := read(t, path); got != shorter {
		t.Error("a refusal wrote to the file")
	}
}
