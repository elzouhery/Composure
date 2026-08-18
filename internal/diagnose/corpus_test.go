package diagnose

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

const corpusRoot = "../../corpus-repos"

// TestCorpus is the sweep that unit tests cannot substitute for. It asserts
// three things over files nobody wrote for this test:
//
//   - no real-world compose file panics a rule;
//   - every finding is anchored to a real position, with a path that addresses
//     something (AD-7);
//   - a described fix's byte range is inside the file it names, and covers
//     bytes rather than air.
//
// It also prints the per-rule counts. That number is the ship/no-ship signal
// for a diagnostic: a rule that fires on a third of the corpus is not finding
// defects, it is describing normality, and it will be switched off within a
// week of anyone using it.
func TestCorpus(t *testing.T) {
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files, err := corpus.Collect(corpusRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) == 0 {
		// NOT a skip. `corpus-repos/` absent is a legitimate skip — a fresh
		// clone has not fetched it. `corpus-repos/` present and empty is a
		// BROKEN CACHE: a CI restore that produced a directory and no files,
		// which skipped this sweep and turned the step green while proving
		// exactly nothing. Failing here is the difference between "we did not
		// run the corpus check" and "the corpus check passed".
		t.Fatalf("%s exists but holds no compose files — a broken corpus, not an absent one; run `make corpus`", corpusRoot)
	}

	counts := map[string]int{}
	filesWith := map[string]int{}
	fixes := map[string]int{}
	var diagnosed, withFindings, total int

	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		project, err := resolve.File(file)
		if err != nil {
			// A file the resolver refuses is not this package's business.
			continue
		}
		graph, err := topology.Build(project, nil)
		if err != nil {
			t.Errorf("%s: topology: %v", file, err)
			continue
		}
		all, err := topology.Build(project, declaredProfiles(project))
		if err != nil {
			t.Errorf("%s: topology (all profiles): %v", file, err)
			continue
		}
		// Env deliberately empty and known: the sweep must measure what the
		// undefined-variable rule would say in a clean checkout, not what it
		// says with the test machine's shell exported into it.
		rep, err := Run(Input{
			Path:        file,
			Project:     project,
			Graph:       graph,
			AllProfiles: all,
			Sources:     map[string][]byte{file: src},
			Env:         map[string]string{},
			EnvKnown:    true,
		})
		if err != nil {
			t.Errorf("%s: run: %v", file, err)
			continue
		}
		diagnosed++
		if len(rep.Findings) > 0 {
			withFindings++
		}
		seenRule := map[string]bool{}
		for _, f := range rep.Findings {
			total++
			counts[f.Rule]++
			if !seenRule[f.Rule] {
				seenRule[f.Rule] = true
				filesWith[f.Rule]++
			}
			checkFinding(t, file, src, f)
			if f.Fix != nil {
				fixes[f.Rule]++
			}
		}
		if _, err := json.Marshal(rep); err != nil {
			t.Errorf("%s: report does not marshal: %v", file, err)
		}
	}

	if diagnosed == 0 {
		t.Fatal("no corpus file was diagnosed; the sweep proved nothing")
	}

	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	t.Logf("%d of %d corpus files diagnosed; %d had findings; %d findings total",
		diagnosed, len(files), withFindings, total)
	t.Logf("%-28s %8s %8s %8s %8s", "RULE", "FINDINGS", "FILES", "%FILES", "W/FIX")
	for _, id := range ids {
		t.Logf("%-28s %8d %8d %7.1f%% %8d",
			id, counts[id], filesWith[id],
			100*float64(filesWith[id])/float64(diagnosed), fixes[id])
	}
}

// checkFinding enforces the invariants a rule cannot be trusted to keep by
// itself. The engine's characteristic failure is a confident wrong answer, so
// the positions are checked against the file rather than merely for presence.
func checkFinding(t *testing.T, file string, src []byte, f Finding) {
	t.Helper()
	if len(f.Anchors) == 0 {
		t.Errorf("%s: %s: finding with no anchor", file, f.Rule)
		return
	}
	for _, a := range f.Anchors {
		if a.Origin.IsZero() {
			t.Errorf("%s: %s: anchor %q has no position", file, f.Rule, a.Label)
		}
		if len(a.Path) == 0 {
			t.Errorf("%s: %s: anchor %q has no path", file, f.Rule, a.Label)
		}
	}
	if f.Fix == nil {
		if f.NoFix == "" {
			t.Errorf("%s: %s: no fix and no reason", file, f.Rule)
		}
		return
	}
	if filepath.Clean(f.Fix.File) != filepath.Clean(file) {
		// The sweep resolves one file per project, so every fix belongs to that
		// file. A fix naming another means an Origin leaked in from somewhere.
		// Multi-file projects are covered in multifile_test.go, which drives the
		// real merge — this sweep's value is scale, not chain shape.
		t.Errorf("%s: %s: fix names %s", file, f.Rule, f.Fix.File)
		return
	}
	r := f.Fix.Range
	if r.Start < 0 || r.End > len(src) || r.End < r.Start {
		t.Errorf("%s: %s: fix range %d-%d is outside a %d byte file", file, f.Rule, r.Start, r.End, len(src))
		return
	}
	if f.Fix.Operation != FixInsertKey && r.Start == r.End {
		t.Errorf("%s: %s: %s fix covers no bytes", file, f.Rule, f.Fix.Operation)
	}
	// A value-writing fix either carries a value or says the author must supply
	// one. `- ""` is valid YAML and invalid compose, and a consumer cannot tell
	// the two apart without this.
	if f.Fix.Value == "" && !f.Fix.NeedsValue &&
		(f.Fix.Operation == FixReplaceScalar || f.Fix.Operation == FixInsertKey) {
		t.Errorf("%s: %s: %s fix carries no value and does not declare that one is needed",
			file, f.Rule, f.Fix.Operation)
	}
	// THE INVARIANT. Range.Line comes from the anchor's Origin; Start and End
	// come from strategy's locator. They are derived independently, so requiring
	// them to agree is free — and it is the whole detector for a fix computed
	// against the wrong node. Zero mismatches across the corpus today; the two
	// reproduced data-loss cases both mismatch.
	checkFixOwnsItsRange(t, file, src, f)
}

// checkFixOwnsItsRange asserts that the bytes a fix names are where the fix says
// they are. It re-derives the line from the bytes rather than trusting the
// number the fix reports, so a rule that set both from the same wrong place is
// still caught by the anchor comparison beneath it.
func checkFixOwnsItsRange(t *testing.T, file string, src []byte, f Finding) {
	t.Helper()
	r := f.Fix.Range
	begins := 1 + bytes.Count(src[:r.Start], []byte("\n"))

	// The anchor the fix is about: the one on the line it reports, or — for an
	// insert, whose point is below its target — any anchor in the same file.
	var origin resolve.Origin
	for _, a := range f.Anchors {
		if a.Origin.File == f.Fix.File && a.Origin.Line == r.Line {
			origin = a.Origin
			break
		}
	}
	if origin.IsZero() && f.Fix.Operation == FixInsertKey {
		for _, a := range f.Anchors {
			if a.Origin.File == f.Fix.File {
				origin = a.Origin
				break
			}
		}
	}
	if origin.IsZero() {
		t.Errorf("%s: %s: fix reports line %d but no anchor of the finding is there", file, f.Rule, r.Line)
		return
	}

	switch f.Fix.Operation {
	case FixReplaceScalar:
		if begins != origin.Line {
			t.Errorf("%s: %s: replace_scalar covers bytes on line %d, but the finding is anchored at line %d — "+
				"the fix does not own the bytes it names", file, f.Rule, begins, origin.Line)
		}
	case FixDeleteKey:
		end := r.End
		if end > r.Start {
			end--
		}
		ends := 1 + bytes.Count(src[:end], []byte("\n"))
		if origin.Line < begins || origin.Line > ends {
			t.Errorf("%s: %s: delete_key spans lines %d-%d, which does not contain the anchored line %d",
				file, f.Rule, begins, ends, origin.Line)
		}
	case FixInsertKey:
		if begins < origin.Line {
			t.Errorf("%s: %s: insertion point is on line %d, above the target declared at line %d",
				file, f.Rule, begins, origin.Line)
		}
	}
}

// TestCorpusDeterminism diagnoses a slice of the corpus twice and compares the
// wire shape. A report that reorders itself between two runs cannot be diffed
// in CI, which is most of what a CI gate is for.
func TestCorpusDeterminism(t *testing.T) {
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files, err := corpus.Collect(corpusRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	const sample = 200
	if len(files) > sample {
		files = files[:sample]
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if _, err := resolve.Bytes(file, src); err != nil {
			continue
		}
		a := diagnoseOnce(t, file, src)
		b := diagnoseOnce(t, file, src)
		if a != b {
			t.Fatalf("%s: two runs produced different JSON", file)
		}
	}
}

func diagnoseOnce(t *testing.T, file string, src []byte) string {
	t.Helper()
	project, err := resolve.Bytes(file, src)
	if err != nil {
		t.Fatalf("%s: resolve: %v", file, err)
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("%s: topology: %v", file, err)
	}
	all, err := topology.Build(project, declaredProfiles(project))
	if err != nil {
		t.Fatalf("%s: topology: %v", file, err)
	}
	rep, err := Run(Input{
		Path: file, Project: project, Graph: graph, AllProfiles: all,
		Sources: map[string][]byte{file: src}, Env: map[string]string{}, EnvKnown: true,
	})
	if err != nil {
		t.Fatalf("%s: run: %v", file, err)
	}
	out, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("%s: marshal: %v", file, err)
	}
	return string(out)
}
