package edit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The fixture is testdata/edge/e42-merged-value.yml and its header says why it
// is shaped the way it is. Every assertion below names WHICH service it asks
// about, because the whole point of the fixture is that `inherits` and
// `declares` resolve to the same value at the same-looking path and only one of
// them has bytes there.
func mergedFixture(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edge", "e42-merged-value.yml"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	return src
}

// tempCopy writes the fixture somewhere an apply may damage.
func tempCopy(t *testing.T, src []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(p, src, 0o644); err != nil {
		t.Fatalf("writing the work copy: %v", err)
	}
	return p
}

func TestClassifyDeclaredValueIsEditable(t *testing.T) {
	got := Classify(mergedFixture(t), "services.declares.restart")
	if !got.Editable || got.Reason != "" {
		t.Fatalf("services.declares.restart writes `restart: always` in the file; classify said %+v", got)
	}
}

func TestClassifyInheritedValue(t *testing.T) {
	got := Classify(mergedFixture(t), "services.inherits.restart")
	if got.Editable {
		t.Fatalf("there is no restart under `inherits`; classify called it editable: %+v", got)
	}
	if got.Reason != ReasonInherited {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonInherited)
	}
	if got.Anchor != "defaults" {
		t.Fatalf("anchor = %q, want %q", got.Anchor, "defaults")
	}
	if got.Plan != string(OpInsertKey) {
		t.Fatalf("plan = %q, want %q — an inherited value is overridden on the service", got.Plan, OpInsertKey)
	}
	// The `<<: *defaults` line, which is the answer to "why does this service
	// have a restart I cannot see".
	if got.Through == nil || got.Through.Line != 40 {
		t.Fatalf("through = %+v, want the `<<: *defaults` on line 40", got.Through)
	}
	// And where the bytes actually are: the anchor's own `restart`.
	if got.BytesAt == nil || got.BytesAt.Line != 30 {
		t.Fatalf("bytes_at = %+v, want the anchor's restart on line 30", got.BytesAt)
	}
	if !strings.Contains(got.Detail, "*defaults") || !strings.Contains(got.Detail, "restart") {
		t.Fatalf("the reader is told nothing useful: %q", got.Detail)
	}
}

// A key reached THROUGH the merge and then descended into is a different and
// worse case: YAML replaces the whole merged key, so writing `interval` locally
// would silently drop `test` and `retries`.
func TestClassifyInheritedNested(t *testing.T) {
	got := Classify(mergedFixture(t), "services.inherits.healthcheck.interval")
	if got.Editable {
		t.Fatalf("classify called an inherited nested value editable: %+v", got)
	}
	if got.Reason != ReasonInheritedNested {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonInheritedNested)
	}
	if got.Plan != "" {
		t.Fatalf("plan = %q — overriding one key of a merged mapping drops its siblings, so there is no plan", got.Plan)
	}
	if !strings.Contains(got.Detail, "healthcheck") {
		t.Fatalf("the reader is not told which mapping would be replaced: %q", got.Detail)
	}
}

func TestClassifyAliasAndAnchorAndBlockScalar(t *testing.T) {
	src := mergedFixture(t)
	for _, tc := range []struct {
		at, reason, mustSay string
	}{
		{"services.references.entrypoint", ReasonAlias, "*entry"},
		{"services.anchored.entrypoint", ReasonAnchor, "&entry"},
		{"services.blocked.command", ReasonBlockScalar, "block scalar"},
	} {
		got := Classify(src, tc.at)
		if got.Editable {
			t.Errorf("%s: classify called it editable: %+v", tc.at, got)
		}
		if got.Reason != tc.reason {
			t.Errorf("%s: reason = %q, want %q", tc.at, got.Reason, tc.reason)
		}
		if !strings.Contains(got.Detail, tc.mustSay) {
			t.Errorf("%s: detail %q does not say %q", tc.at, got.Detail, tc.mustSay)
		}
	}
}

func TestClassifyAbsentKeyUnderFlowMapping(t *testing.T) {
	src := mergedFixture(t)
	if got := Classify(src, "services.flowed.deploy.replicas"); !got.Editable {
		t.Errorf("a scalar inside a flow mapping IS spliceable; classify said %+v", got)
	}
	got := Classify(src, "services.flowed.deploy.mode")
	if got.Editable || got.Reason != ReasonFlow {
		t.Fatalf("an insert into a flow mapping is refused by the engine; classify said %+v", got)
	}
	if got.Plan != "" {
		t.Fatalf("plan = %q, want none", got.Plan)
	}
}

func TestClassifyAbsentKeyPlansAnInsert(t *testing.T) {
	got := Classify(mergedFixture(t), "services.anchored.restart")
	if got.Editable || got.Reason != ReasonAbsent || got.Plan != string(OpInsertKey) {
		t.Fatalf("an absent key is inserted, unchanged from before: %+v", got)
	}
}

// The reproduction, at the level the CLI and the JSON-RPC server both use.
func TestReplaceScalarOnInheritedValueIsRefused(t *testing.T) {
	file := tempCopy(t, mergedFixture(t))
	_, err := Preview(Request{File: file, Ops: []Op{
		{Operation: OpReplaceScalar, At: "services.inherits.restart", Value: "always"},
	}})
	if !errors.Is(err, ErrInherited) {
		t.Fatalf("err = %v, want ErrInherited", err)
	}
	if !Refused(err) {
		t.Fatal("an inherited value is a refusal the reader can act on, not a fault")
	}
	if Reason(err) != ReasonInherited {
		t.Fatalf("slug = %q, want %q", Reason(err), ReasonInherited)
	}
	if !strings.Contains(err.Error(), "*defaults") {
		t.Fatalf("the refusal does not name the anchor: %v", err)
	}
}

// The override that answers it — decision 21. One key, one line, and the
// anchor untouched.
func TestInsertOverridesTheMergedValue(t *testing.T) {
	file := tempCopy(t, mergedFixture(t))
	res, err := Apply(Request{File: file, Ops: []Op{
		{Operation: OpInsertKey, At: "services.inherits", Key: "restart", Value: "always"},
	}})
	if err != nil {
		t.Fatalf("insert_key: %v", err)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Fatalf("added %d removed %d, want one line added and nothing removed", res.Added, res.Removed)
	}
	out, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Restart string `yaml:"restart"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("the result does not parse: %v", err)
	}
	if doc.Services["inherits"].Restart != "always" {
		t.Fatalf("inherits.restart = %q, want always — the local key must win over the merge", doc.Services["inherits"].Restart)
	}
	if doc.Services["declares"].Restart != "always" {
		t.Fatal("declares.restart moved; the edit was supposed to touch one service")
	}
	if !strings.Contains(string(out), "restart: unless-stopped") {
		t.Fatal("the anchor's own restart was rewritten; editing one service changed every service")
	}
	// The classification flips: the value now has bytes at the path it is read
	// from, so the next edit is a plain splice.
	if got := Classify(out, "services.inherits.restart"); !got.Editable {
		t.Fatalf("after the override the path is editable in place; classify said %+v", got)
	}
}

// Each of these produced a confident wrong answer before this change. They are
// listed together because the shape of the defect is one shape: a splice at a
// position whose bytes are not the value the reader is looking at.
func TestReplaceScalarRefusesWhatItUsedToDamage(t *testing.T) {
	for _, tc := range []struct {
		at   string
		want error
	}{
		// Wrote `entrypoint: ZZZentry` — it spliced over the `*`.
		{"services.references.entrypoint", ErrAliasValue},
		// Dropped `&entry`, leaving `*entry` below it dangling. Caught only by
		// the re-parse, and reported as "the result would not parse".
		{"services.anchored.entrypoint", ErrAnchoredValue},
		// Replaced the `|` and left `one` and `two` behind, which YAML then
		// read as a multi-line plain scalar. Valid, and not what the file said.
		{"services.blocked.command", ErrBlockScalar},
	} {
		file := tempCopy(t, mergedFixture(t))
		before, _ := os.ReadFile(file)
		_, err := Apply(Request{File: file, Ops: []Op{
			{Operation: OpReplaceScalar, At: tc.at, Value: "ZZZ"},
		}})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.at, err, tc.want)
		}
		if !Refused(err) {
			t.Errorf("%s: not reported as a refusal", tc.at)
		}
		if Reason(err) == "" {
			t.Errorf("%s: no reason slug for a client to branch on", tc.at)
		}
		after, _ := os.ReadFile(file)
		if string(before) != string(after) {
			t.Errorf("%s: the file was written despite the refusal", tc.at)
		}
	}
}

// A file the parser will not read must not become an accusation about anchors.
func TestClassifyUnreadableFile(t *testing.T) {
	got := Classify([]byte("services: [\n"), "services.web.image")
	if got.Editable || got.Reason != ReasonUnreadable {
		t.Fatalf("classify on unparseable bytes = %+v", got)
	}
}

// An inherited LIST is the `networks` case in examples/webstack: the reader can
// see it on the service and there is nothing on the service to add to. It gets
// the sentence and no plan, because appending to it here would not append.
func TestClassifyInheritedSequenceOffersNoPlan(t *testing.T) {
	got := Classify(mergedFixture(t), "services.inherits.networks")
	if got.Editable || got.Reason != ReasonInherited {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonInherited)
	}
	if got.Plan != "" {
		t.Fatalf("plan = %q — there is no operation that adds to a list the file does not declare here", got.Plan)
	}
	if !strings.Contains(got.Detail, "replace") {
		t.Fatalf("the reader is not told what writing it here would cost: %q", got.Detail)
	}
}
