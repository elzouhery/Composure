package resolve

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/strategy"
)

// offsetOf mirrors the conversion the splice engine performs, so that a test
// can prove an Origin lands on the bytes a user would see. It is duplicated
// rather than exported from strategy because the point is to check the two
// agree independently — a shared helper could be wrong in both places at once.
func offsetOf(src []byte, line, col int) (int, bool) {
	if line < 1 || col < 1 {
		return 0, false
	}
	off, cur := 0, 1
	for cur < line {
		i := strings.IndexByte(string(src[off:]), '\n')
		if i < 0 {
			return 0, false
		}
		off += i + 1
		cur++
	}
	return off + col - 1, true
}

// ── I/O matrix: happy paths ────────────────────────────────────────────────

func TestSingleServiceCarriesOrigin(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx:1.25\n")
	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(Path{"services", "web", "image"})
	if !ok {
		t.Fatal("services.web.image did not resolve")
	}
	if got := v.Scalar(); got != "nginx:1.25" {
		t.Fatalf("scalar = %q, want nginx:1.25", got)
	}
	o := v.Origin()
	if o.File != "compose.yml" || o.Line != 3 || o.Step != 0 {
		t.Fatalf("origin = %+v, want compose.yml line 3 step 0", o)
	}
	off, ok := offsetOf(src, o.Line, o.Column)
	if !ok || !strings.HasPrefix(string(src[off:]), "nginx:1.25") {
		t.Fatalf("origin column %d does not land on the value: %q", o.Column, string(src[off:]))
	}
}

func TestOverrideHistoryIsPresentAndEmpty(t *testing.T) {
	p, err := Bytes("compose.yml", []byte("services:\n  web:\n    image: nginx\n"))
	if err != nil {
		t.Fatal(err)
	}
	// The `!= nil` half of this assertion could not fail: the accessor `make()`s
	// its slice and make never returns nil. What can actually break is the
	// WIRE — a nil internal slice marshals to `null`, and a consumer switching
	// on `x === null` against `x.length === 0` would be told "nothing was
	// checked" where the answer is "nothing was wrong". So the wire is asserted
	// too. See TestEmptyListsSerialiseAsListsNotNull.
	var checked int
	p.Walk(func(path Path, v *Value) bool {
		ov := v.Overrides()
		if ov == nil {
			t.Errorf("%s: override history is nil; it must be present and empty", path)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(`"overrides":[]`)) {
			t.Errorf("%s: override history is not [] on the wire: %s", path, raw)
		}
		if len(ov) != 0 {
			t.Errorf("%s: single-file project has %d overrides, want 0", path, len(ov))
		}
		checked++
		return true
	})
	if checked == 0 {
		t.Fatal("walked nothing")
	}
}

func TestExtensionFieldsSurvive(t *testing.T) {
	src := []byte(`x-shared: &shared
  restart: always
services:
  web:
    image: nginx
    x-meta: owned-by-platform
`)
	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	if ext := p.Extensions(); len(ext) != 1 || ext[0] != "x-shared" {
		t.Fatalf("top-level extensions = %v, want [x-shared]", ext)
	}
	v, ok := p.At(Path{"services", "web", "x-meta"})
	if !ok {
		t.Fatal("service-level x-meta was dropped")
	}
	if v.Origin().IsZero() {
		t.Error("x-meta has no origin")
	}
}

func TestQuotedScalarKeepsTextAndPosition(t *testing.T) {
	src := []byte("services:\n  web:\n    image: \"nginx:1.25\"\n")
	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := p.At(Path{"services", "web", "image"})
	if got := v.Scalar(); got != "nginx:1.25" {
		t.Fatalf("scalar = %q, want the unquoted text", got)
	}
	// The quoting style itself belongs to the buffer, not the model — but the
	// origin must still land inside the quoted token so an editor can select it.
	off, ok := offsetOf(src, v.Origin().Line, v.Origin().Column)
	if !ok {
		t.Fatal("origin did not convert to an offset")
	}
	rest := string(src[off:])
	if !strings.HasPrefix(rest, `"nginx:1.25"`) && !strings.HasPrefix(rest, "nginx:1.25") {
		t.Fatalf("origin lands at %q, expected the value token", rest[:12])
	}
}

func TestNumericKeyIsNotCoerced(t *testing.T) {
	src := []byte("services:\n  web:\n    environment:\n      8080: \"x\"\n      0755: \"y\"\n")
	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	env, ok := p.At(Path{"services", "web", "environment"})
	if !ok {
		t.Fatal("environment did not resolve")
	}
	keys := env.Map().Keys()
	if len(keys) != 2 || keys[0] != "8080" || keys[1] != "0755" {
		t.Fatalf("keys = %v, want [8080 0755] in source order and as written", keys)
	}
}

func TestNullIsDistinctFromAbsent(t *testing.T) {
	p, err := Bytes("compose.yml", []byte("services:\n  web:\n    image: nginx\n    command:\n"))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(Path{"services", "web", "command"})
	if !ok {
		t.Fatal("an explicitly null key must be present in the model")
	}
	if v.Kind() != KindNull {
		t.Fatalf("kind = %v, want null", v.Kind())
	}
	if _, ok := p.At(Path{"services", "web", "entrypoint"}); ok {
		t.Fatal("an absent key must not resolve")
	}
}

// ── I/O matrix: error paths ────────────────────────────────────────────────

func TestMalformedYAMLYieldsParseErrorAndNoModel(t *testing.T) {
	p, err := Bytes("broken.yml", []byte("services:\n  web:\n    ports: [8080\n"))
	if p != nil {
		t.Fatal("a parse failure must not produce a partial model")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T (%v), want *ParseError", err, err)
	}
	if pe.File != "broken.yml" {
		t.Errorf("ParseError.File = %q, want broken.yml", pe.File)
	}
	if !strings.Contains(pe.Error(), "broken.yml") {
		t.Errorf("error message does not name the file: %s", pe)
	}
}

func TestNotAComposeFile(t *testing.T) {
	p, err := Bytes("notes.yml", []byte("title: shopping list\nitems:\n  - milk\n"))
	if p != nil {
		t.Fatal("expected no model")
	}
	if !errors.Is(err, ErrNotCompose) {
		t.Fatalf("err = %v, want ErrNotCompose", err)
	}
}

func TestMissingFile(t *testing.T) {
	p, err := File(filepath.Join(t.TempDir(), "nope.yml"))
	if p != nil {
		t.Fatal("expected no model")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "nope.yml") {
		t.Errorf("error does not name the path: %v", err)
	}
}

func TestEmptyFile(t *testing.T) {
	for name, src := range map[string]string{
		"zero bytes":    "",
		"whitespace":    "\n\n   \n",
		"only comments": "# nothing here\n# really\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Bytes("empty.yml", []byte(src))
			if p != nil {
				t.Fatal("expected no model")
			}
			if !errors.Is(err, ErrEmptyFile) {
				t.Fatalf("err = %v, want ErrEmptyFile", err)
			}
		})
	}
}

// ── Acceptance criteria ────────────────────────────────────────────────────

// Story 1.1 recorded aliases and refused to follow them; story 1.2 expands them
// for display. The contract this test guards is what changed and what did not:
// the alias now resolves to the anchor's value, that value's Origin is the
// ANCHOR's definition site — where the bytes are — and the reference site, which
// 1.1 was right to insist on keeping, is still recoverable from the value.
func TestAliasExpandsAndKeepsItsReferenceSite(t *testing.T) {
	src := []byte("x-base: &base\n  restart: always\nservices:\n  web:\n    image: nginx\n    deploy: *base\n")
	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(Path{"services", "web", "deploy"})
	if !ok {
		t.Fatal("deploy did not resolve")
	}
	if v.Kind() != KindMapping {
		t.Fatalf("kind = %v, want mapping — the alias must resolve to the anchor's value", v.Kind())
	}

	restart, ok := p.At(Path{"services", "web", "deploy", "restart"})
	if !ok || restart.Scalar() != "always" {
		t.Fatalf("expanded value = %v, want restart: always", restart)
	}
	if restart.Origin().Line != 2 {
		t.Errorf("expanded value origin line = %d, want 2 — the anchor, not the alias", restart.Origin().Line)
	}

	// The reference site must not be lost. A reader looking at line 6 needs a
	// way back to line 6; the anchor's origin alone cannot provide it.
	if v.Alias() != "base" {
		t.Errorf("alias name = %q, want base", v.Alias())
	}
	site, ok := v.AliasSite()
	if !ok {
		t.Fatal("the alias reference site was dropped")
	}
	if site.Line != 6 {
		t.Errorf("alias site line = %d, want 6", site.Line)
	}
	off, ok := offsetOf(src, site.Line, site.Column)
	if !ok || !strings.HasPrefix(string(src[off:]), "*base") {
		t.Errorf("alias site does not land on the `*base` token")
	}

	// Origin and AliasSite are two different positions and neither substitutes
	// for the other — that is the whole reason both are carried.
	if v.Origin().Line == site.Line {
		t.Error("origin and alias site coincide; one of them is not being recorded")
	}
}

// The merge key is a directive, not configuration. Once expanded it must not
// remain as a key in the model — a `<<` key in the resolved view is a key no
// container runtime has ever heard of — while the bytes keep it exactly as
// written, because this package has no write path at all (R4.1, AD-12).
func TestMergeKeyExpandsAndLeavesTheBytesAlone(t *testing.T) {
	src := []byte("x-base: &base\n  restart: always\nservices:\n  web:\n    <<: *base\n    image: nginx\n")
	before := string(src)

	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	web, _ := p.At(Path{"services", "web"})
	for _, k := range web.Map().Keys() {
		if k == "<<" {
			t.Fatal("`<<` survived into the resolved model; it is a directive, not a key")
		}
	}

	restart, ok := web.Map().Get("restart")
	if !ok {
		t.Fatal("the merge key contributed nothing")
	}
	if restart.Scalar() != "always" || restart.Origin().Line != 2 {
		t.Errorf("restart = %q at line %d, want always at line 2 (the anchor)", restart.Scalar(), restart.Origin().Line)
	}
	site, ok := restart.AliasSite()
	if !ok || site.Line != 5 {
		t.Errorf("merged key does not point back at the `<<: *base` on line 5: %+v ok=%v", site, ok)
	}

	if string(src) != before {
		t.Fatal("resolving mutated the caller's buffer")
	}
}

// A quoted `"<<"` is an ordinary string key, not a merge directive. yaml.v3
// distinguishes them by tag, and keying on the text instead would silently
// swallow a real key — or, worse, try to merge a string.
func TestQuotedMergeKeyIsAnOrdinaryKey(t *testing.T) {
	p, err := Bytes("c.yml", []byte("services:\n  web:\n    image: nginx\n    labels:\n      \"<<\": literal\n"))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(Path{"services", "web", "labels", "<<"})
	if !ok {
		t.Fatal(`a quoted "<<" key was treated as a merge directive and lost`)
	}
	if v.Scalar() != "literal" {
		t.Errorf("value = %q, want literal", v.Scalar())
	}
}

// Merge-key precedence, both halves of it. Getting either backwards silently
// changes what the stack does, which is the failure mode this engine is prone
// to: no crash, a confident wrong answer.
func TestMergePrecedence(t *testing.T) {
	t.Run("parent wins over merged", func(t *testing.T) {
		src := []byte("x-base: &base\n  restart: always\n  cpus: \"1\"\nservices:\n  web:\n    <<: *base\n    image: nginx\n    restart: \"no\"\n")
		p, err := Bytes("c.yml", src)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := p.At(Path{"services", "web", "restart"})
		if v.Scalar() != "no" {
			t.Fatalf("restart = %q, want the parent's own value \"no\"", v.Scalar())
		}
		if v.Origin().Line != 8 {
			t.Errorf("origin line = %d, want 8 — the overriding site", v.Origin().Line)
		}
		// The anchor's value is not discarded, it is recorded as what lost.
		ov := v.Overrides()
		if len(ov) != 1 {
			t.Fatalf("overrides = %v, want the anchor's value recorded", ov)
		}
		if ov[0].Value != "always" || ov[0].Origin.Line != 2 {
			t.Errorf("override = %+v, want always at line 2", ov[0])
		}
		// The keys the parent did not override still arrive.
		if c, ok := p.At(Path{"services", "web", "cpus"}); !ok || c.Scalar() != "1" {
			t.Error("a non-conflicting merged key was dropped")
		}
	})

	// The parent wins even when its key is written BELOW the `<<:`, which is the
	// usual layout. Applying merges as they are encountered would get this wrong.
	t.Run("parent wins regardless of where the merge key sits", func(t *testing.T) {
		for name, src := range map[string]string{
			"merge first": "x-b: &b\n  image: anchor\nservices:\n  web:\n    <<: *b\n    image: own\n",
			"merge last":  "x-b: &b\n  image: anchor\nservices:\n  web:\n    image: own\n    <<: *b\n",
		} {
			t.Run(name, func(t *testing.T) {
				p, err := Bytes("c.yml", []byte(src))
				if err != nil {
					t.Fatal(err)
				}
				v, _ := p.At(Path{"services", "web", "image"})
				if v.Scalar() != "own" {
					t.Fatalf("image = %q, want own", v.Scalar())
				}
				if len(v.Overrides()) != 1 {
					t.Errorf("the anchor's losing value was not recorded")
				}
			})
		}
	})

	t.Run("left wins over right", func(t *testing.T) {
		src := []byte("x-a: &a\n  restart: from-a\nx-b: &b\n  restart: from-b\n  extra: from-b\nservices:\n  web:\n    <<: [*a, *b]\n    image: nginx\n")
		p, err := Bytes("c.yml", src)
		if err != nil {
			t.Fatal(err)
		}
		v, ok := p.At(Path{"services", "web", "restart"})
		if !ok {
			t.Fatal("restart did not resolve")
		}
		if v.Scalar() != "from-a" {
			t.Fatalf("restart = %q, want from-a: earlier sources win in `<<: [*a, *b]`", v.Scalar())
		}
		if v.Origin().Line != 2 {
			t.Errorf("origin line = %d, want 2 — the &a anchor", v.Origin().Line)
		}
		ov := v.Overrides()
		if len(ov) != 1 || ov[0].Value != "from-b" {
			t.Errorf("overrides = %+v, want the losing from-b recorded", ov)
		}
		if e, ok := p.At(Path{"services", "web", "extra"}); !ok || e.Scalar() != "from-b" {
			t.Error("a key only the later source has was dropped")
		}
	})
}

// Merged keys land where the `<<:` was written, so the resolved view reads in
// roughly the order of the file rather than sorting anchors to the end.
func TestMergedKeysLandAtTheMergeKeyPosition(t *testing.T) {
	p, err := Bytes("c.yml", []byte("x-b: &b\n  restart: always\nservices:\n  web:\n    <<: *b\n    image: nginx\n"))
	if err != nil {
		t.Fatal(err)
	}
	web, _ := p.At(Path{"services", "web"})
	keys := web.Map().Keys()
	if len(keys) != 2 || keys[0] != "restart" || keys[1] != "image" {
		t.Fatalf("keys = %v, want [restart image] — merged keys at the `<<:` position", keys)
	}
}

// A `<<:` value YAML does not define. Refusing is the deliberate choice: the
// author plainly meant something to be merged, and quietly dropping the
// directive would produce a model missing configuration with nothing said.
func TestBadMergeValuesAreRefused(t *testing.T) {
	cases := map[string]string{
		"scalar":                 "services:\n  web:\n    <<: 3\n    image: nginx\n",
		"null":                   "services:\n  web:\n    <<:\n    image: nginx\n",
		"alias to a scalar":      "x-s: &s just-a-string\nservices:\n  web:\n    <<: *s\n    image: nginx\n",
		"alias to a sequence":    "x-s: &s\n  - one\nservices:\n  web:\n    <<: *s\n    image: nginx\n",
		"sequence of non-maps":   "services:\n  web:\n    <<: [1, 2]\n    image: nginx\n",
		"sequence with a scalar": "x-a: &a\n  k: v\nservices:\n  web:\n    <<: [*a, 2]\n    image: nginx\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := Bytes("t.yml", []byte(src))
			if p != nil {
				t.Fatal("accepted; a model quietly missing a merge is a confident wrong answer")
			}
			if !errors.Is(err, ErrBadMergeValue) {
				t.Fatalf("err = %v, want ErrBadMergeValue", err)
			}
		})
	}

	// An inline mapping IS a legal merge value, and must not be caught by the above.
	p, err := Bytes("t.yml", []byte("services:\n  web:\n    <<: {restart: always}\n    image: nginx\n"))
	if err != nil {
		t.Fatalf("an inline mapping is a legal merge value: %v", err)
	}
	if v, ok := p.At(Path{"services", "web", "restart"}); !ok || v.Scalar() != "always" {
		t.Error("an inline-mapping merge contributed nothing")
	}
}

// Two `<<:` in one mapping. YAML defines one merge key per mapping and says
// nothing about how two would order, so any precedence chosen here would be
// invented. Refuse, as for any duplicate key.
func TestTwoMergeKeysAreRefused(t *testing.T) {
	src := []byte("x-a: &a\n  p: 1\nx-b: &b\n  q: 2\nservices:\n  web:\n    <<: *a\n    <<: *b\n    image: nginx\n")
	p, err := Bytes("t.yml", []byte(src))
	if p != nil {
		t.Fatal("accepted two merge keys in one mapping")
	}
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("err = %v, want ErrDuplicateKey", err)
	}
}

// Expansion must be bounded. Anchors that reference anchors double at every
// level, so a file this size names 2^N nodes — building the model would exhaust
// memory long before it finished. Permanent: this is reason two of the three
// story 1.1 gave for not following aliases at all.
func TestExponentialFanoutIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-a0: &a0\n  k: v\n")
	const levels = 40
	for i := 1; i <= levels; i++ {
		// Each level references the one below it twice: 2^40 leaves if unbounded.
		b.WriteString("x-a" + strconv.Itoa(i) + ": &a" + strconv.Itoa(i) + "\n")
		b.WriteString("  l: *a" + strconv.Itoa(i-1) + "\n")
		b.WriteString("  r: *a" + strconv.Itoa(i-1) + "\n")
	}
	b.WriteString("services:\n  web:\n    image: nginx\n    labels: *a" + strconv.Itoa(levels) + "\n")

	done := make(chan error, 1)
	go func() {
		_, err := Bytes("fanout.yml", []byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAliasFanout) {
			t.Fatalf("err = %v, want ErrAliasFanout", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("expansion did not terminate; the fan-out budget is not bounding it")
	}
}

// The committed fixtures run everywhere, including CI, where the 2.1GB corpus
// is not fetched for the unit-test job. Keeping this separate from the corpus
// sweep matters: the corpus test skips when corpus-repos is absent, so before
// this split the story's central acceptance criterion never ran in CI at all.
func TestCommittedFixturesCarryProvenance(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../testdata/adversarial", "../../testdata/edge"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) < 10 {
		t.Fatalf("expected the committed fixture set, found %d files", len(files))
	}

	var resolved, leaves int
	for _, f := range files {
		p, err := File(f)
		if err != nil {
			continue // a typed refusal is a correct outcome
		}
		resolved++
		p.Walk(func(path Path, v *Value) bool {
			if v.Origin().IsZero() {
				t.Errorf("%s: %s has no position", f, path)
			}
			leaves++
			return true
		})
	}
	if resolved == 0 {
		t.Fatalf("resolved none of %d committed fixtures", len(files))
	}
	t.Logf("%d/%d fixtures resolved, %d values all positioned", resolved, len(files), leaves)
}

// The full corpus sweep. Skips when corpus-repos is absent — which is why the
// committed-fixture test above exists and does not.
func TestCorpusEveryLeafHasOriginAndNothingPanics(t *testing.T) {
	files, err := corpus.Collect("../../corpus-repos")
	if err != nil || len(files) == 0 {
		t.Skip("corpus not fetched; run `make corpus`")
	}

	var resolved, rejected, leaves, expanded int
	byErr := map[string]int{}
	for _, f := range files {
		p, err := File(f)
		if err != nil {
			rejected++
			byErr[refusalKind(err)]++
			continue
		}
		resolved++
		p.Walk(func(path Path, v *Value) bool {
			if v.Origin().IsZero() {
				t.Errorf("%s: %s has no position", f, path)
			}
			// An expanded value must be able to name both of its positions.
			if site, ok := v.AliasSite(); ok {
				expanded++
				if site.IsZero() {
					t.Errorf("%s: %s came from an alias with no reference site", f, path)
				}
				if v.Alias() == "" {
					t.Errorf("%s: %s came from an alias with no anchor name", f, path)
				}
			}
			leaves++
			return true
		})
	}
	// A floor, so a regression that starts refusing most of the corpus fails
	// rather than passing on `resolved > 0`.
	if resolved < len(files)/4 {
		t.Fatalf("resolved only %d of %d corpus files", resolved, len(files))
	}
	t.Logf("resolved %d, refused %d, %d values all positioned, %d of them expanded from anchors",
		resolved, rejected, leaves, expanded)
	for e, n := range byErr {
		t.Logf("  refused %4d × %s", n, e)
	}
}

// The committed merge-precedence fixture, checked against the answers its
// comments claim. A fixture nobody asserts against is a file, not a regression
// test — and every one of these differs from what "apply merges as you meet
// them, last write wins" produces.
func TestMergePrecedenceFixture(t *testing.T) {
	p, err := File("../../testdata/adversarial/07-merge-precedence.yml")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"services.parent-wins-below.restart":   "from-parent",
		"services.parent-wins-below.image":     "own-image",
		"services.parent-wins-below.only-in-a": "yes-a",
		"services.parent-wins-above.restart":   "from-parent",
		"services.parent-wins-above.image":     "own-image",
		"services.left-wins.restart":           "from-a",
		"services.left-wins.only-in-b":         "yes-b",
		"services.left-wins.image":             "seq-image",
		"services.nested-merge.restart":        "from-nested",
		"services.nested-merge.only-in-a":      "yes-a",
		"services.nested-merge.image":          "nested-image",
		"services.inline-merge.restart":        "from-inline",
		"services.literal-merge-key.restart":   "from-b",
		"services.literal-merge-key.labels.<<": "not-a-directive",
	} {
		v, ok := p.At(ParsePath(path))
		if !ok {
			t.Errorf("%s did not resolve", path)
			continue
		}
		if v.Scalar() != want {
			t.Errorf("%s = %q, want %q", path, v.Scalar(), want)
		}
	}

	// No `<<` survives as a service key anywhere — only the quoted one, nested
	// under labels, which the loop above already checked.
	p.Walk(func(path Path, v *Value) bool {
		if len(path) == 3 && path[0] == "services" && path[2] == "<<" {
			t.Errorf("%s: the merge directive survived into the model", path)
		}
		return true
	})
}

// The committed recursive-anchor fixture. It must be refused, and the refusal
// must name the reason rather than surfacing as a parse error or a hang.
func TestRecursiveAnchorFixtureIsRefused(t *testing.T) {
	p, err := File("../../testdata/edge/e9-recursive-anchor.yml")
	if p != nil {
		t.Fatal("accepted a self-referential anchor")
	}
	if !errors.Is(err, ErrAliasCycle) {
		t.Fatalf("err = %v, want ErrAliasCycle", err)
	}
}

// refusalKind buckets a refusal by its sentinel so the corpus sweep can report
// a breakdown. Parse errors carry a per-file message, so they collapse to one
// bucket rather than producing a line each.
func refusalKind(err error) string {
	for _, s := range []error{
		ErrEmptyFile, ErrNotFound, ErrLegacyFormat, ErrNotCompose, ErrMultiDocument,
		ErrDuplicateKey, ErrComplexKey, ErrTooDeep,
		ErrAliasCycle, ErrAliasFanout, ErrBadMergeValue, ErrUnknownAnchor,
	} {
		if errors.Is(err, s) {
			return s.Error()
		}
	}
	var pe *ParseError
	if errors.As(err, &pe) {
		return "yaml parse error"
	}
	return "other"
}

// The origins this package produces must address the same bytes the splice
// engine addresses, or a fix computed in the inspector would splice the wrong
// range. Path passing straight into strategy.Locate is the interop AD-14 wants.
func TestOriginsAgreeWithSpliceEngine(t *testing.T) {
	src := []byte("services:\n  web:\n    image: nginx:1.25\n    ports:\n      - \"8080:80\"\n")
	p, err := Bytes("compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	path := Path{"services", "web", "image"}

	// Path is assignable to []string with no conversion and no import cycle.
	lineIdx, colIdx, err := strategy.Locate(src, path)
	if err != nil {
		t.Fatalf("strategy.Locate rejected the shared path type: %v", err)
	}

	web, _ := p.At(Path{"services", "web"})
	keyOrigin, ok := web.Map().KeyOrigin("image")
	if !ok {
		t.Fatal("no key origin recorded for image")
	}
	if got := keyOrigin.Line - 1; got != lineIdx {
		t.Fatalf("key origin line index %d, splice engine says %d", got, lineIdx)
	}
	// THE COLUMN TOO. Comparing only the line meant shifting every KeyOrigin
	// by seven columns passed this test and every other one in the suite, while
	// an edit computed from the origin would splice a range seven bytes off —
	// which is the whole class of damage this test exists to rule out.
	if got := keyOrigin.Column - 1; got != colIdx {
		t.Fatalf("key origin column index %d, splice engine says %d", got, colIdx)
	}
}

// Legacy v1 files and build-time fragments are both valid YAML and both refused
// — but for a reason worth naming. "Not a compose file" sends someone hunting
// for a typo; naming the format tells them what is actually wrong. Found in the
// wild: 60 of 158 corpus files land here, so this is the common refusal.
func TestLegacyAndFragmentGetTheirOwnError(t *testing.T) {
	for _, f := range []string{
		"../../testdata/edge/e7-legacy-v1-toplevel-services.yml",
		"../../testdata/edge/e8-fragment-indented-services.yml",
	} {
		p, err := File(f)
		if p != nil {
			t.Errorf("%s: expected no model", f)
		}
		if !errors.Is(err, ErrLegacyFormat) {
			t.Errorf("%s: err = %v, want ErrLegacyFormat", f, err)
		}
	}

	// A document that is simply not compose must not be mistaken for legacy
	// format — the detector keys on service shape, not on being a mapping.
	_, err := Bytes("notes.yml", []byte("title: shopping list\nitems:\n  - milk\n"))
	if !errors.Is(err, ErrNotCompose) {
		t.Errorf("plain YAML err = %v, want ErrNotCompose", err)
	}
}

// A self-referential anchor is expressible in YAML, and following it blindly
// exhausts the stack — which kills the process outright rather than raising a
// recoverable panic. Found by review, not by the corpus: no real file does this,
// and a file that did would take the editor down with it.
func TestRecursiveAnchorDoesNotBlowTheStack(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on a recursive anchor: %v", r)
		}
	}()
	// A mutual cycle needs a forward reference, which YAML itself forbids — the
	// parser rejects it before expansion ever sees it. Kept as a case because
	// "refused" is the property under test, not which layer refuses.
	if p, err := Bytes("cycle.yml", []byte("services:\n  web:\n    image: nginx\nx-a: &a\n  b: *b\nx-b: &b\n  a: *a\n")); p != nil || err == nil {
		t.Error("a mutual anchor cycle was accepted")
	}

	for name, src := range map[string]string{
		"self-reference":      "services:\n  web:\n    image: nginx\nx-loop: &loop\n  self: *loop\n",
		"cycle in a merge":    "services:\n  web:\n    image: nginx\nx-loop: &loop\n  <<: *loop\n",
		"cycle behind a use":  "x-loop: &loop\n  self: *loop\nservices:\n  web:\n    image: nginx\n    labels: *loop\n",
		"cycle in a sequence": "services:\n  web:\n    image: nginx\nx-loop: &loop\n  - *loop\n",
	} {
		p, err := Bytes("cycle.yml", []byte(src))
		if p != nil {
			t.Errorf("%s: accepted a cyclic anchor", name)
		}
		if !errors.Is(err, ErrAliasCycle) {
			t.Errorf("%s: err = %v, want ErrAliasCycle", name, err)
		}
	}

	// A file where the same anchor is used twice from sibling positions is not a
	// cycle, and must not be mistaken for one — the guard tracks the expansion
	// stack, not every anchor ever seen.
	if _, err := Bytes("ok.yml", []byte("x-b: &b\n  k: v\nservices:\n  web:\n    image: nginx\n    a: *b\n    c: *b\n")); err != nil {
		t.Errorf("repeated (non-cyclic) use of one anchor was refused: %v", err)
	}
}

// Walk documents that returning false stops the whole walk. It previously only
// skipped the subtree, which is a different contract and a quiet one to get
// wrong at a call site.
func TestWalkStopsWhenCallbackReturnsFalse(t *testing.T) {
	p, err := Bytes("c.yml", []byte("services:\n  a:\n    image: x\n  b:\n    image: y\n  c:\n    image: z\n"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	p.Walk(func(_ Path, _ *Value) bool {
		seen++
		return seen < 2
	})
	if seen != 2 {
		t.Fatalf("walk visited %d values after the callback asked it to stop at 2", seen)
	}
}

// Every refusal added by the review pass. Each one previously produced a model
// that disagreed with the file — the confident wrong answer, not a crash.
func TestReviewFoundRefusals(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want error
	}{
		{"multi-document", "services:\n  a:\n    image: x\n---\nservices:\n  b:\n    image: y\n", ErrMultiDocument},
		{"duplicate key", "services:\n  web:\n    image: first\n    image: second\n", ErrDuplicateKey},
		{"duplicate service", "services:\n  web:\n    image: a\n  web:\n    image: b\n", ErrDuplicateKey},
		{"complex key", "services:\n  web:\n    image: x\n? [a, b]\n: v\n", ErrComplexKey},
		{"services is a scalar", "services: nope\n", ErrNotCompose},
		{"services is a sequence", "services:\n  - web\n", ErrNotCompose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Bytes("t.yml", []byte(tc.src))
			if p != nil {
				t.Fatalf("accepted; a model that disagrees with the file is worse than a refusal")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Deep nesting must be refused rather than recursing until the stack dies.
// Stack exhaustion is fatal in Go — recover() cannot catch it — so the guard
// has to be a check, not a rescue.
func TestDeepNestingIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString("services:\n  web:\n    image: x\n    labels:\n")
	for i := 0; i < maxDepth+50; i++ {
		b.WriteString(strings.Repeat(" ", 6+i*2) + "a:\n")
	}
	_, err := Bytes("deep.yml", []byte(b.String()))
	if err == nil {
		t.Fatal("accepted a document nested past maxDepth")
	}
	// The SENTINEL, not merely "an error". `err != nil` passed for any refusal
	// at all — including a parse failure or an out-of-memory — so it could not
	// tell "the depth guard fired" from "something else went wrong first",
	// which is the only thing this test exists to establish.
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err = %v, want ErrTooDeep", err)
	}
}

// The JSON is the contract the extension is written against. Key order is a
// fidelity property of the file, so it must survive the wire; and every value
// must carry its origin and a present-but-empty override list.
func TestJSONPreservesOrderAndProvenance(t *testing.T) {
	src := []byte("services:\n  web:\n    environment:\n      8080: \"a\"\n      0755: \"b\"\n      zeta: \"c\"\n")
	p, err := Bytes("c.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	i8080, i0755, izeta := strings.Index(s, `"8080"`), strings.Index(s, `"0755"`), strings.Index(s, `"zeta"`)
	if i8080 < 0 || i0755 < 0 || izeta < 0 {
		t.Fatalf("keys missing from JSON: %s", s)
	}
	if !(i8080 < i0755 && i0755 < izeta) {
		t.Error("source key order was not preserved on the wire")
	}
	if !strings.Contains(s, `"key_origin"`) {
		t.Error("key origins are absent; a consumer cannot compute the range an edit would touch")
	}
	if !strings.Contains(s, `"overrides":[]`) {
		t.Error(`overrides must serialise as a present, empty list`)
	}
}

// The parse position is what the extension places a cursor with, so it must
// address the line the mistake is actually on.
//
// yamlErrorPosition took yaml.v3's number verbatim. That number is 0-BASED for
// a parser-stage error and 1-based for a scanner-stage one (decode.go's `fail`
// increments only for yaml_SCANNER_ERROR), so `ports: [8080` — the single most
// common way a compose file breaks — pointed one line ABOVE the mistake, at a
// line that was perfectly fine. The column was always 0, which means "no
// position" and made the line useless to a caller needing both.
func TestParseErrorPositionsAddressTheRealLine(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		line    int
		col     int
		stage   string
		comment string
	}{
		{
			name: "unterminated flow sequence",
			src:  "services:\n  web:\n    ports: [8080\n",
			line: 3, col: 5,
			stage: "parser", comment: "yaml.v3 says line 2",
		},
		{
			name: "unterminated flow mapping",
			src:  "services:\n  web:\n    x: {a: 1\n",
			line: 3, col: 5,
			stage: "parser", comment: "yaml.v3 says line 2",
		},
		{
			name: "two colons on one line",
			src:  "services:\n  web:\n    image: x\n    a: b: c\n",
			line: 4, col: 5,
			stage: "scanner", comment: "a scanner error is already 1-based and must NOT be shifted",
		},
		{
			name: "tab where an indent is expected",
			src:  "services:\n  web:\n\timage: x\n",
			line: 3, col: 2,
			stage: "scanner", comment: "column 2: the tab is the indent, so the first non-blank column is the `i`",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Bytes("broken.yml", []byte(c.src))
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %T (%v), want *ParseError", err, err)
			}
			if pe.Line != c.line {
				t.Errorf("line = %d, want %d (%s error; %s). yaml.v3 said: %v",
					pe.Line, c.line, c.stage, c.comment, pe.Err)
			}
			// The column is the first non-blank column of the blamed line — not
			// the offending token, which yaml.v3 does not expose. It must at
			// least be a real position, because 0 is not one.
			if pe.Column != c.col {
				t.Errorf("column = %d, want %d (the first non-blank column of line %d)", pe.Column, c.col, c.line)
			}
			if pe.Column < 1 {
				t.Error("column 0 is not a position; the extension cannot place a cursor with it")
			}
			// The position must be inside the file. Pointing past the end is
			// worse than admitting the position is unknown.
			if n := strings.Count(c.src, "\n"); pe.Line > n {
				t.Errorf("line %d is past the end of a %d-line file", pe.Line, n)
			}
		})
	}
}

// The parser/scanner classification above is a list of strings copied out of
// yaml.v3. If the library is upgraded and the list drifts, the symptom is a
// line number one off in someone's editor — so the list is checked against the
// parser itself rather than trusted.
func TestYAMLParserProblemListStillMatchesTheLibrary(t *testing.T) {
	// A document whose error is raised by the PARSER, whose message must
	// therefore appear in the list.
	_, err := Bytes("x.yml", []byte("services:\n  web:\n    ports: [8080\n"))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T, want *ParseError", err)
	}
	msg := pe.Err.Error()
	var matched bool
	for _, p := range yamlParserProblems {
		if strings.Contains(msg, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("yaml.v3 now reports %q, which yamlParserProblems does not list; "+
			"the 0-based correction will silently stop being applied", msg)
	}
}

// A position the source cannot contain is reported as unknown rather than
// invented.
func TestAnOutOfRangeParsePositionIsReportedAsUnknown(t *testing.T) {
	line, col := yamlErrorPosition(errors.New("yaml: line 900: something"), []byte("a: 1\n"))
	if line != 0 || col != 0 {
		t.Errorf("position = %d:%d, want 0:0 — pointing past the end of a file is worse than admitting the position is unknown", line, col)
	}
}
