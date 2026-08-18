package dockerfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The instruction vocabulary is the Dockerfile half of AD-20's `available, not
// set`. AD-20 forbids a hand-maintained key list that falls behind and starts
// lying, and the Dockerfile grammar has no schema document to generate one
// from, so the guards are these tests plus the fact that the parser READS the
// table. See vocabulary.go's provenance note.

// The single most important property: the table is not decorative. Parse
// consults it, and the FROM entry's Stage flag is what makes the image
// reference locatable. Delete the FROM entry and this fails — which is what
// makes the table a source of behaviour rather than a display list.
func TestVocabularyIsLoadBearingForStages(t *testing.T) {
	spec, ok := Lookup("FROM")
	if !ok {
		t.Fatal("the vocabulary does not know FROM; the parser reads this table to locate image references")
	}
	if !spec.Stage {
		t.Fatal("FROM is not marked Stage; Parse would stop byte-ranging image references")
	}
	f := Parse([]byte("FROM alpine:3.20 AS base\nRUN true\n"))
	if len(f.Stages()) != 1 {
		t.Fatalf("%d stages, want 1", len(f.Stages()))
	}
	in := f.Instructions[f.Stages()[0]]
	if in.ImageRef != "alpine:3.20" {
		t.Errorf("image ref %q, want %q", in.ImageRef, "alpine:3.20")
	}
	if got := string(f.Source[in.ImageStart:in.ImageEnd]); got != "alpine:3.20" {
		t.Errorf("byte range covers %q, want %q", got, "alpine:3.20")
	}
}

// Casing is free in Dockerfiles and the engine never normalises the file, so
// the lookup must not care either.
func TestLookupIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{"from", "From", "FROM", "  from  "} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("Lookup(%q) missed", name)
		}
	}
	if _, ok := Lookup("RUNX"); ok {
		t.Error("Lookup accepted RUNX, which is not an instruction")
	}
}

// An unrecognised instruction is REPORTED, never corrected and never dropped.
// Silently treating a typo as valid, or omitting the line from the form, are
// both confident wrong answers about someone's file.
func TestUnknownInstructionIsReportedNotDropped(t *testing.T) {
	src := []byte("FROM alpine\nRUNX something\nRUN true\n")
	form := BuildForm("Dockerfile", src)
	if len(form.Stages) != 1 {
		t.Fatalf("%d stages, want 1", len(form.Stages))
	}
	var runx *InstructionView
	for i, in := range form.Stages[0].Instructions {
		if in.Name == "RUNX" {
			runx = &form.Stages[0].Instructions[i]
		}
	}
	if runx == nil {
		t.Fatal("RUNX was dropped from the form; an instruction this engine does not know is still in the reader's file")
	}
	if runx.Known {
		t.Error("RUNX is marked Known")
	}
	if got := strings.Join(form.Stages[0].Vocabulary.Unknown, ","); got != "RUNX" {
		t.Errorf("stage vocabulary reports unknown %q, want %q", got, "RUNX")
	}
	// And it must not be counted as a declared instruction — the split would
	// otherwise say the stage uses an instruction the grammar does not have.
	for _, e := range form.Stages[0].Vocabulary.Instructions {
		if e.Name == "RUNX" {
			t.Error("RUNX appears in the vocabulary split as a real instruction")
		}
	}
}

// The split itself: used + not used == the whole grammar, per stage, with no
// instruction in both halves and none missing from both.
func TestVocabularySplitIsExhaustiveAndDisjoint(t *testing.T) {
	src := []byte("FROM alpine AS build\nRUN make\nCOPY . /src\n\nFROM scratch\nCOPY --from=build /out /out\n")
	form := BuildForm("Dockerfile", src)
	total := len(Vocabulary())

	check := func(label string, v VocabularyView) {
		t.Helper()
		if v.DeclaredCount+v.AvailableCount != total {
			t.Errorf("%s: %d used + %d available = %d, want %d",
				label, v.DeclaredCount, v.AvailableCount, v.DeclaredCount+v.AvailableCount, total)
		}
		if len(v.Instructions) != total {
			t.Errorf("%s: %d entries, want %d", label, len(v.Instructions), total)
		}
		seen := map[string]bool{}
		declared := 0
		for _, e := range v.Instructions {
			if seen[e.Name] {
				t.Errorf("%s: %s appears twice", label, e.Name)
			}
			seen[e.Name] = true
			if e.Declared {
				declared++
				if e.Uses < 1 {
					t.Errorf("%s: %s is declared with %d uses", label, e.Name, e.Uses)
				}
				if len(e.Indices) != e.Uses {
					t.Errorf("%s: %s has %d uses and %d indices", label, e.Name, e.Uses, len(e.Indices))
				}
			} else if e.Uses != 0 {
				t.Errorf("%s: %s is not declared but reports %d uses", label, e.Name, e.Uses)
			}
		}
		if declared != v.DeclaredCount {
			t.Errorf("%s: DeclaredCount %d, counted %d", label, v.DeclaredCount, declared)
		}
	}

	check("file", form.Vocabulary)
	for _, s := range form.Stages {
		check("stage "+s.Label, s.Vocabulary)
	}
}

// Instruction scope is per stage, not per file, and the split has to say so.
// A USER set in a builder stage tells the reader nothing about the runtime
// stage, and a form that pooled them would report `available, not set` wrongly
// for every multi-stage file — which is most of them.
func TestVocabularyIsScopedToTheStage(t *testing.T) {
	src := []byte("FROM alpine AS build\nRUN make\nUSER builder\n\nFROM scratch\nCOPY --from=build /out /out\n")
	form := BuildForm("Dockerfile", src)
	if len(form.Stages) != 2 {
		t.Fatalf("%d stages, want 2", len(form.Stages))
	}
	if !declared(form.Stages[0].Vocabulary, "USER") {
		t.Error("stage 0 uses USER and the split does not say so")
	}
	if declared(form.Stages[1].Vocabulary, "USER") {
		t.Error("stage 1 does not use USER, but the split says it does")
	}
	if declared(form.Stages[1].Vocabulary, "RUN") {
		t.Error("stage 1 does not use RUN, but the split says it does")
	}
	if !declared(form.Stages[1].Vocabulary, "COPY") {
		t.Error("stage 1 uses COPY and the split does not say so")
	}
	// The file-level view pools them, which is the other question a reader asks.
	if !declared(form.Vocabulary, "USER") || !declared(form.Vocabulary, "COPY") {
		t.Error("the file-level split lost an instruction one of its stages uses")
	}
}

// An ARG in the preamble decides the base image, so it must not vanish from the
// file-level split just because it belongs to no stage.
func TestPreambleInstructionsCountAtFileLevel(t *testing.T) {
	form := BuildForm("Dockerfile", []byte("ARG TAG=3.20\nFROM alpine:${TAG}\n"))
	if !declared(form.Vocabulary, "ARG") {
		t.Error("the preamble ARG is missing from the file-level split")
	}
	if declared(form.Stages[0].Vocabulary, "ARG") {
		t.Error("the preamble ARG was attributed to stage 0, which does not contain it")
	}
}

// A Dockerfile that is not there declares nothing, so the whole grammar is
// available. The field is populated rather than left null: absent reads as "no
// vocabulary", which is a different claim.
func TestMissingFormCarriesTheWholeVocabulary(t *testing.T) {
	f := MissingForm("build/Dockerfile", "build", "")
	if f.Vocabulary.DeclaredCount != 0 {
		t.Errorf("a missing file declares %d instructions", f.Vocabulary.DeclaredCount)
	}
	if f.Vocabulary.AvailableCount != len(Vocabulary()) {
		t.Errorf("%d available, want the whole grammar (%d)", f.Vocabulary.AvailableCount, len(Vocabulary()))
	}
}

// The drift alarm. AD-20's objection to a hand-written list is that it falls
// behind and starts lying; the answer here is that real Dockerfiles are checked
// against it. The committed fixtures always run; the corpus runs when it has
// been fetched, and its absence is REPORTED rather than passing in silence.
func TestVocabularyCoversRealDockerfiles(t *testing.T) {
	unknown := map[string]int{}
	scanned := 0

	scan := func(path string) {
		src, err := os.ReadFile(path)
		if err != nil {
			return
		}
		scanned++
		for _, in := range Parse(src).Instructions {
			if in.Kind != KindInstruction || in.Name == "" || in.Known {
				continue
			}
			unknown[in.Name]++
		}
	}

	entries, err := os.ReadDir("../../testdata/dockerfiles")
	if err != nil {
		t.Fatalf("the committed Dockerfile fixtures are missing: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			scan(filepath.Join("../../testdata/dockerfiles", e.Name()))
		}
	}
	if scanned == 0 {
		t.Fatal("no fixtures scanned; this check cannot fail and is worthless")
	}

	corpus := "../../corpus-repos"
	if _, err := os.Stat(corpus); err == nil {
		_ = filepath.Walk(corpus, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if isDockerfileName(filepath.Base(path)) {
				scan(path)
			}
			return nil
		})
	} else {
		// Said out loud. A check that quietly skips its real input is a check
		// that passes vacuously, which is worse than no check.
		t.Logf("corpus-repos is not fetched: only the %d committed fixtures were scanned. "+
			"Run `make corpus` for the full sweep.", scanned)
	}

	// Heredoc bodies and shell script inside continuations are not
	// instructions, and the parser's own heredoc handling is what keeps them
	// out. Anything left is either a real instruction the table lacks or a
	// parser defect, and both are worth failing for.
	for name, n := range unknown {
		t.Errorf("%d Dockerfile line(s) start with %q, which the vocabulary does not know: "+
			"either the grammar gained an instruction or the parser mis-scanned a body", n, name)
	}
	t.Logf("scanned %d Dockerfile(s) against a %d-instruction vocabulary", scanned, len(Vocabulary()))
}

// Vocabulary() hands out a copy: a caller that sorts or truncates it must not
// be able to reorder the table the parser reads.
func TestVocabularyReturnsACopy(t *testing.T) {
	got := Vocabulary()
	if len(got) == 0 {
		t.Fatal("the vocabulary is empty")
	}
	got[0].Name = "MUTATED"
	if Vocabulary()[0].Name == "MUTATED" {
		t.Error("Vocabulary() exposes the table itself")
	}
	if _, ok := Lookup("ADD"); !ok {
		t.Error("mutating the returned slice damaged the lookup index")
	}
}

// isDockerfileName mirrors cmd/dockerbench's collector, `.dockerignore`
// exclusion included. GitLab ships `Dockerfile.assets.dockerignore`, whose
// contents are a list of paths — feed that to a Dockerfile parser and every
// line looks like an instruction nobody has ever heard of.
func isDockerfileName(n string) bool {
	if strings.HasSuffix(n, ".dockerignore") {
		return false
	}
	return n == "Dockerfile" || n == "Containerfile" ||
		strings.HasPrefix(n, "Dockerfile.") || strings.HasSuffix(n, ".Dockerfile")
}

func declared(v VocabularyView, name string) bool {
	for _, e := range v.Instructions {
		if e.Name == name {
			return e.Declared
		}
	}
	return false
}
