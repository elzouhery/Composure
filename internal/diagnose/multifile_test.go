package diagnose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Multi-file diagnosis, which nothing in this package exercised before.
//
// Every other test in the package resolves ONE document — diagnose_test.go's
// run() calls resolve.BytesWith, corpus_test.go calls resolve.File. That is the
// single case in which a merged path and a file path coincide, so the whole
// class of defect where a fix is computed against the wrong file's bytes was
// invisible to the suite. These drive resolve.Load with a real chain.

// project writes files into a temp directory and diagnoses them the way the CLI
// does: resolve the real chain, build both graphs, read every source file back.
//
// files is name -> content. entry names the entry file; chain, when set, is an
// explicit -f list and disables the automatic override pickup, exactly as
// Compose does.
type multiProject struct {
	dir   string
	entry string
	rep   *Report
	src   map[string][]byte
	// proj is the resolved model the report was built from, so a test can assert
	// what the RESOLVER raised as well as what diagnose did with it. A rule that
	// filters resolver findings by Kind cannot be pinned without it: a fixture
	// producing no resolver finding at all cannot tell a filter from no filter.
	proj *resolve.Project
}

func diagnoseProject(t *testing.T, entry string, chain []string, files map[string]string) *multiProject {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	opts := resolve.Options{Env: map[string]string{}, IgnoreHostEnv: true}
	if len(chain) > 0 {
		for _, f := range chain {
			opts.Files = append(opts.Files, filepath.Join(dir, f))
		}
	} else {
		opts.Dir = dir
	}
	proj, err := resolve.Load(opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	graph, err := topology.Build(proj, nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	all, err := topology.Build(proj, declaredProfiles(proj))
	if err != nil {
		t.Fatalf("topology (all profiles): %v", err)
	}

	// Read the sources back the way analyze.go does, keyed as Origin.File names
	// them. Anything else would test a map this package builds for itself.
	src := map[string][]byte{}
	for _, f := range proj.Files() {
		b, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		src[f.Path] = b
	}

	path := filepath.Join(dir, entry)
	rep, err := Run(Input{
		Path: path, Project: proj, Graph: graph, AllProfiles: all,
		Sources: src, Env: map[string]string{}, EnvKnown: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return &multiProject{dir: dir, entry: path, rep: rep, src: src, proj: proj}
}

// covered returns the bytes a fix names, read out of the file the fix names.
func (p *multiProject) covered(t *testing.T, f *Fix) string {
	t.Helper()
	src, ok := p.src[f.File]
	if !ok {
		t.Fatalf("fix names %s, which is not one of the project's source files", f.File)
	}
	if f.Range.Start < 0 || f.Range.End > len(src) || f.Range.End < f.Range.Start {
		t.Fatalf("fix range %d-%d is not inside a %d byte file", f.Range.Start, f.Range.End, len(src))
	}
	return string(src[f.Range.Start:f.Range.End])
}

func (p *multiProject) base(name string) string { return filepath.Join(p.dir, name) }

// ---------------------------------------------------------------------------
// THE SHIP-BLOCKER.
//
// A merged sequence index is not an index into any one file. This is the exact
// reproduction that was verified destroying user data: the fix was anchored
// correctly at the credential and described a range covering an unrelated
// variable two lines below it. Applying it would have deleted `B=2`, left
// `DB_PASSWORD=hunter2` in the file, and returned success.
//
// It needs no flags: a plain `include:` where both files declare the same
// service is enough, because include merges the included file UNDER the
// includer's own content and the includer's sequence entries shift up by the
// length of the included one.

const includedFragment = `services:
  app:
    environment:
      - Z=0
`

const includingBase = `include:
  - db.yaml
services:
  app:
    image: nginx
    environment:
      - DB_PASSWORD=hunter2
      - B=2
      - C=3
`

func TestMergedSequenceIndexNeverSplicesAnotherEntry(t *testing.T) {
	p := diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml": includingBase,
		"db.yaml":      includedFragment,
	})

	got := wantCount(t, p.rep, credRule, 1)[0]

	// The finding itself was never wrong: it is anchored at the credential.
	anchor := got.Anchors[0].Origin
	if filepath.Base(anchor.File) != "compose.yaml" || anchor.Line != 7 {
		t.Fatalf("anchored at %s, want compose.yaml:7 — the DB_PASSWORD entry", anchor)
	}
	// Merged, DB_PASSWORD is the SECOND entry: db.yaml's Z=0 is merged under it.
	// That index is what used to be handed to the locator.
	if p := got.Subjects; len(p) == 0 {
		t.Fatal("finding names no subject")
	}

	// A fix IS required here, not merely a safe refusal.
	//
	// Refusing is an acceptable answer to a case that cannot be resolved, and
	// the guard in fix.go turns a wrong range into one. But this case CAN be
	// resolved — the entry is in this file and the resolver knows exactly where
	// — so accepting a refusal here would let the remap be deleted with the
	// suite green, leaving only the guard between a merged index and the wrong
	// bytes. Requiring the fix pins the remap; the guard is pinned separately in
	// fix_test.go.
	if got.Fix == nil {
		t.Fatalf("no fix was described for a credential this file plainly writes: %s", got.NoFix)
	}
	// The list form's scalar is the whole `NAME=value` entry, so that is what the
	// fix owns. What it must never own is `B=2`, which is what it named before.
	covered := p.covered(t, got.Fix)
	if covered != "DB_PASSWORD=hunter2" {
		t.Fatalf("the fix names %q; it must own the credential's own entry or none. "+
			"This is the data-loss reproduction: naming any other entry destroys an unrelated "+
			"variable and leaves the credential in the file, reporting success.", covered)
	}
	// The replacement keeps the variable's name: `- ${DB_PASSWORD}` would be an
	// entry with no `=` and a different meaning entirely.
	if got.Fix.Value != "DB_PASSWORD=${DB_PASSWORD}" {
		t.Errorf("fix value is %q, which is not a valid list-form entry", got.Fix.Value)
	}
	if filepath.Base(got.Fix.File) != "compose.yaml" {
		t.Errorf("fix names file %s, want compose.yaml", got.Fix.File)
	}
	// The fix's own path addresses the FILE, so File+Path+Range are consistent.
	if got.Fix.Path.String() != "services.app.environment[0]" {
		t.Errorf("fix path is %s, want the entry's index IN compose.yaml, which is 0", got.Fix.Path)
	}
}

// The override-file spelling of the same defect, reached with no flags either:
// compose.override.yaml is picked up automatically.
func TestOverrideFileCredentialFixOwnsItsBytes(t *testing.T) {
	p := diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml": `services:
  app:
    image: nginx
    environment:
      - A=1
      - B=2
`,
		"compose.override.yaml": `services:
  app:
    environment:
      - API_TOKEN=sekrit
      - D=4
      - E=5
`,
	})
	got := wantCount(t, p.rep, credRule, 1)[0]
	if filepath.Base(got.Anchors[0].Origin.File) != "compose.override.yaml" {
		t.Fatalf("anchored in %s, want the override file", got.Anchors[0].Origin.File)
	}
	if got.Fix == nil {
		return // refusing is acceptable
	}
	if covered := p.covered(t, got.Fix); covered != "API_TOKEN=sekrit" {
		t.Errorf("fix covers %q, want the credential's own entry in the override file", covered)
	}
}

// A fix must never name a file the finding is not in. The merged model has no
// bytes; every fix belongs to exactly one source file.
func TestFixNamesTheFileItsAnchorIsIn(t *testing.T) {
	p := diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml": includingBase,
		"db.yaml":      includedFragment,
	})
	for _, f := range p.rep.Findings {
		if f.Fix == nil {
			continue
		}
		var anchored bool
		for _, a := range f.Anchors {
			if a.Origin.File == f.Fix.File {
				anchored = true
			}
		}
		if !anchored {
			t.Errorf("%s: fix names %s but no anchor is in that file", f.Rule, f.Fix.File)
		}
	}
}

// ---------------------------------------------------------------------------
// Story 3.2 across files.

// The collision a single-file linter cannot see, and the sentence that says so.
func TestPortCollisionAcrossFilesSaysTheyAreInDifferentFiles(t *testing.T) {
	p := diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml": `services:
  web:
    image: nginx
    ports:
      - "8080:80"
`,
		"compose.override.yaml": `services:
  api:
    image: api
    ports:
      - "8080:3000"
`,
	})
	got := wantCount(t, p.rep, portRule, 1)[0]
	if !strings.Contains(got.Message, "2 different files") {
		t.Errorf("the message does not say the declarations are in different files, "+
			"which is the whole reason a merged linter exists: %s", got.Message)
	}
	if !strings.Contains(got.Message, "only visible after the merge") {
		t.Errorf("message does not explain why a single-file check would miss it: %s", got.Message)
	}
	// Both files are named, so the reader can open either.
	files := map[string]bool{}
	for _, a := range got.Anchors {
		files[filepath.Base(a.Origin.File)] = true
	}
	if !files["compose.yaml"] || !files["compose.override.yaml"] {
		t.Errorf("anchors name %v, want both files", files)
	}
}

// A collision inside ONE file must NOT claim to be a cross-file one.
func TestPortCollisionInOneFileDoesNotClaimMultipleFiles(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    ports:
      - "8080:80"
  api:
    image: api
    ports:
      - "8080:3000"
`)
	got := wantCount(t, rep, portRule, 1)[0]
	if strings.Contains(got.Message, "different files") {
		t.Errorf("a single-file collision claims to span files: %s", got.Message)
	}
}

// AD-15: the fix goes to the declaration that arrived LAST IN THE MERGE, which
// is the one that introduced the conflict. Filename order is not merge order,
// and this chain is built so the two disagree: zbase.yaml is step 0 and sorts
// last, aover.yaml is step 1 and sorts first.
func TestPortCollisionFixTargetsTheLatestMergeStepNotTheLastFilename(t *testing.T) {
	p := diagnoseProject(t, "zbase.yaml", []string{"zbase.yaml", "aover.yaml"}, map[string]string{
		"zbase.yaml": `services:
  web:
    image: nginx
    ports:
      - "8080:80"
`,
		"aover.yaml": `services:
  api:
    image: api
    ports:
      - "8080:3000"
`,
	})
	got := wantCount(t, p.rep, portRule, 1)[0]
	if got.Fix == nil {
		t.Fatalf("no fix described: %s", got.NoFix)
	}
	if filepath.Base(got.Fix.File) != "aover.yaml" {
		t.Errorf("fix targets %s; the incumbent is zbase.yaml at merge step 0 and the conflict "+
			"was introduced by aover.yaml at step 1, whatever the two files are called",
			filepath.Base(got.Fix.File))
	}
	if covered := p.covered(t, got.Fix); covered != `"8080:3000"` {
		t.Errorf("fix covers %q, want the overriding declaration", covered)
	}
	// The anchors read in merge order too, so "the first is the incumbent" is
	// true of what the reader sees and not only of what the fix picked.
	if filepath.Base(got.Anchors[0].Origin.File) != "zbase.yaml" {
		t.Errorf("first anchor is %s, want the incumbent at step 0",
			filepath.Base(got.Anchors[0].Origin.File))
	}
}

// ---------------------------------------------------------------------------
// The invariant, over a whole multi-file project.

// Every fix in a multi-file project owns the bytes it names. This is the guard
// stated as a property rather than a case: it would have failed on the
// reproduction above before the fix, and it fails for any future rule that
// computes a range against the wrong file or the wrong element.
func TestEveryMultiFileFixOwnsItsRange(t *testing.T) {
	p := diagnoseProject(t, "compose.yaml", nil, map[string]string{
		"compose.yaml": `include:
  - shared.yaml
services:
  app:
    image: nginx
    environment:
      - APP_SECRET=hunter2
      - B=2
    ports:
      - "9000:80"
  side:
    image: busybox
    ports:
      - "9000:81"
volumes:
  orphan:
`,
		"shared.yaml": `services:
  app:
    environment:
      - SHARED=1
volumes:
  also-orphan:
`,
	})
	if len(p.rep.Findings) == 0 {
		t.Fatal("the multi-file fixture produced no findings; it is meant to trip several rules")
	}
	for _, f := range p.rep.Findings {
		if f.Fix == nil {
			continue
		}
		assertFixOwnsItsRange(t, p.src[f.Fix.File], f)
	}
}

// assertFixOwnsItsRange is the reviewer's free detector, as an assertion.
//
// Fix.Range.Line comes from the anchor's Origin; Start and End come from
// strategy's locator. They are computed independently, so requiring them to
// agree costs nothing and catches every fix computed against the wrong node.
func assertFixOwnsItsRange(t *testing.T, src []byte, f Finding) {
	t.Helper()
	if src == nil {
		t.Errorf("%s: fix names %s, which is not a source file of this project", f.Rule, f.Fix.File)
		return
	}
	r := f.Fix.Range
	if r.Start < 0 || r.End > len(src) || r.End < r.Start {
		t.Errorf("%s: fix range %d-%d is not inside a %d byte file", f.Rule, r.Start, r.End, len(src))
		return
	}
	// The line the range actually begins on, counted from the bytes.
	begins := 1 + strings.Count(string(src[:r.Start]), "\n")

	// The Origin the fix was derived from. A finding can carry several anchors in
	// one file — a port collision names both ends — so the one to check against
	// is the anchor the fix actually targets, identified by the line it reports.
	// Falling back to "the first anchor in this file" would fail a correct fix
	// that targets the second declaration, which is what it did here first.
	var origin resolve.Origin
	for _, a := range f.Anchors {
		if a.Origin.File == f.Fix.File && a.Origin.Line == r.Line {
			origin = a.Origin
			break
		}
	}
	if origin.IsZero() {
		if f.Fix.Operation == FixInsertKey {
			// An insertion point sits at the end of the target's block, which is
			// below the line the target was declared on, so it has no anchor of
			// its own line. Check it against the anchor in its file.
			for _, a := range f.Anchors {
				if a.Origin.File == f.Fix.File {
					origin = a.Origin
					break
				}
			}
		}
		if origin.IsZero() {
			t.Errorf("%s: fix reports line %d in %s, but no anchor of this finding is there — "+
				"a fix must be traceable to the position it claims to be about", f.Rule, r.Line, f.Fix.File)
			return
		}
	}

	switch f.Fix.Operation {
	case FixReplaceScalar:
		if begins != origin.Line {
			t.Errorf("%s: replace_scalar range begins on line %d, but the finding is anchored at line %d in %s — "+
				"the fix does not own the bytes it names", f.Rule, begins, origin.Line, f.Fix.File)
		}
		if r.Line != origin.Line {
			t.Errorf("%s: fix reports line %d, anchor is at %d", f.Rule, r.Line, origin.Line)
		}
	case FixDeleteKey:
		ends := 1 + strings.Count(string(src[:max(r.Start, r.End-1)]), "\n")
		if origin.Line < begins || origin.Line > ends {
			t.Errorf("%s: delete_key range spans lines %d-%d, which does not contain the anchored line %d",
				f.Rule, begins, ends, origin.Line)
		}
	case FixInsertKey:
		if r.Start != r.End {
			t.Errorf("%s: insert names a non-empty range %d-%d", f.Rule, r.Start, r.End)
		}
		if begins < origin.Line {
			t.Errorf("%s: insertion point is on line %d, above the target declared at line %d",
				f.Rule, begins, origin.Line)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
