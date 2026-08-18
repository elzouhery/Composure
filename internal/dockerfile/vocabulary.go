package dockerfile

// The Dockerfile instruction vocabulary — what the grammar permits.
//
// This is the Dockerfile half of the inspector's differentiator: for a compose
// path, `available, not set` is the specification's keys minus what the file
// declares (AD-20). For a Dockerfile stage, it is the grammar's instructions
// minus what the stage already uses. The shape below deliberately mirrors
// internal/schema's Field/Node split — Declared, DeclaredCount, AvailableCount
// — so a client renders both from one component.
//
// # Provenance, stated because it is weaker than the compose side's
//
// AD-20's compose list is GENERATED from `schema/compose-spec.json`, vendored
// at a pinned commit. Nothing equivalent exists for Dockerfiles. The Compose
// Specification publishes a JSON Schema; the Dockerfile reference is prose on
// docs.docker.com, and the only machine-readable enumeration upstream is a set
// of Go string constants inside BuildKit's own frontend (Apache-2.0). There is
// no schema document to vendor. That is a fact about upstream, not a shortcut
// taken here, and it is written down rather than glossed because AD-20's whole
// argument is that a hand-maintained key list rots into a liability.
//
// Three things make this table something other than a hand-written list:
//
//  1. It is LOAD-BEARING. The parser reads it — Parse consults Lookup for
//     every instruction, and `Stage` is what decides whether the image
//     reference is located and byte-ranged. An entry that is wrong or missing
//     changes how the file is PARSED, not merely how a panel is drawn, so it
//     shows up as a failing parse test rather than as a quietly short list.
//     A decorative list drifts in silence; this one cannot.
//
//  2. It is checked against real files. TestVocabularyCoversCorpus reads every
//     Dockerfile in testdata and, when the corpus is present, every Dockerfile
//     in it, and fails on an instruction name the table does not know. The
//     alarm for "the grammar gained an instruction" is a test over real
//     Dockerfiles rather than someone's memory.
//
//  3. Not knowing is REPORTED, never hidden. An instruction the table does not
//     recognise parses exactly as before and is marked Known=false in the
//     form. The reader is told "this engine does not recognise RUNX", which is
//     the honest answer; it is never silently treated as valid, and it is
//     never dropped.
//
// If BuildKit ever publishes a schema document, this table should be replaced
// by a vendored copy of it and a `PROVENANCE.md` beside `schema/`'s. Until
// then the parser's own knowledge is the most authoritative source available
// inside this repository, which is what it is presented as.
//
// Summaries are one line each and describe what the instruction DOES. They are
// not copied from any BSL/SSPL/Elastic/AGPL source (CLEANROOM.md rule 3); the
// Dockerfile reference is CC-licensed documentation of a public grammar and
// these are independent one-line restatements of behaviour anyone can observe
// by running `docker build`.

import (
	"sort"
	"strings"
)

// InstructionSpec is one instruction the Dockerfile grammar permits, and what
// this engine knows about it.
type InstructionSpec struct {
	// Name is the canonical upper-cased name. Dockerfiles may write it in any
	// casing and this engine never normalises the file — see viewOf's NameRaw.
	Name string `json:"name"`
	// Summary is one line saying what the instruction does.
	Summary string `json:"summary"`
	// Flags are the `--flags` the instruction accepts, in the order the
	// reference presents them. Empty for instructions that take none.
	Flags []string `json:"flags,omitempty"`
	// Heredoc marks an instruction whose body may be a heredoc (`RUN <<EOF`).
	// The parser consumes such bodies, and getting this wrong means reading
	// shell script as Dockerfile instructions.
	Heredoc bool `json:"heredoc,omitempty"`
	// Stage marks the instruction that begins a build stage. Exactly one
	// entry has it, and Parse uses it to decide where to locate an image
	// reference — which is what makes this field load-bearing rather than
	// descriptive.
	Stage bool `json:"stage,omitempty"`
	// BeforeStage marks an instruction that is legal before the first FROM.
	// ARG is the one that matters: an ARG in the preamble is what decides the
	// base image, which is why BuildForm keeps the preamble rather than
	// dropping everything above the first stage.
	BeforeStage bool `json:"before_stage,omitempty"`
	// Deprecated is the grammar's own deprecation, with the reason.
	Deprecated     bool   `json:"deprecated,omitempty"`
	DeprecatedNote string `json:"deprecated_note,omitempty"`
}

// vocabulary is the table. Sorted by name so Vocabulary() needs no sort and
// review sees an alphabetical diff.
var vocabulary = []InstructionSpec{
	{
		Name:    "ADD",
		Summary: "copy files, a URL, or a git repository into the image",
		Flags:   []string{"--keep-git-dir", "--checksum", "--chown", "--chmod", "--link", "--exclude"},
		Heredoc: true,
	},
	{
		Name:        "ARG",
		Summary:     "declare a build-time variable, optionally with a default",
		BeforeStage: true,
	},
	{
		Name:    "CMD",
		Summary: "the default command, overridable on `docker run`",
	},
	{
		Name:    "COPY",
		Summary: "copy files from the build context or another stage into the image",
		Flags:   []string{"--from", "--chown", "--chmod", "--link", "--parents", "--exclude"},
		Heredoc: true,
	},
	{
		Name:    "ENTRYPOINT",
		Summary: "the executable the container always runs; CMD becomes its arguments",
	},
	{
		Name:    "ENV",
		Summary: "set an environment variable for the build and for the running container",
	},
	{
		Name:    "EXPOSE",
		Summary: "document a port the container listens on; publishes nothing by itself",
	},
	{
		Name:    "FROM",
		Summary: "begin a build stage from a base image",
		Flags:   []string{"--platform"},
		Stage:   true,
		// FROM is trivially legal before the first FROM: it IS the first one.
		BeforeStage: true,
	},
	{
		Name:    "HEALTHCHECK",
		Summary: "the command Docker runs to decide whether the container is healthy",
		Flags:   []string{"--interval", "--timeout", "--start-period", "--start-interval", "--retries"},
	},
	{
		Name:    "LABEL",
		Summary: "attach metadata to the image as key=value pairs",
	},
	{
		Name:           "MAINTAINER",
		Summary:        "the image author",
		Deprecated:     true,
		DeprecatedNote: "superseded by LABEL org.opencontainers.image.authors",
	},
	{
		Name:    "ONBUILD",
		Summary: "record an instruction to run when this image is used as a base",
	},
	{
		Name:    "RUN",
		Summary: "execute a command and commit the result as a new layer",
		Flags:   []string{"--mount", "--network", "--security"},
		Heredoc: true,
	},
	{
		Name:    "SHELL",
		Summary: "change the shell used for the shell form of RUN, CMD and ENTRYPOINT",
	},
	{
		Name:    "STOPSIGNAL",
		Summary: "the signal sent to stop the container",
	},
	{
		Name:    "USER",
		Summary: "the user (and optionally group) later instructions and the container run as",
	},
	{
		Name:    "VOLUME",
		Summary: "declare a path as a mount point holding externally managed data",
	},
	{
		Name:    "WORKDIR",
		Summary: "set the working directory for later instructions and for the container",
	},
}

// byName is the lookup index, built once from vocabulary. Deriving it rather
// than writing it twice is the point: two lists cannot disagree if there is
// one list.
var byName = func() map[string]InstructionSpec {
	m := make(map[string]InstructionSpec, len(vocabulary))
	for _, s := range vocabulary {
		m[s.Name] = s
	}
	return m
}()

// Vocabulary is every instruction the grammar permits, alphabetically. The
// returned slice is a copy: a caller that sorts or filters it must not be able
// to reorder the parser's own table.
func Vocabulary() []InstructionSpec {
	out := make([]InstructionSpec, len(vocabulary))
	copy(out, vocabulary)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the spec for an instruction name in any casing, and whether
// the grammar knows it. A false second return is reported to the reader, never
// swallowed — see InstructionView.Known.
func Lookup(name string) (InstructionSpec, bool) {
	s, ok := byName[strings.ToUpper(strings.TrimSpace(name))]
	return s, ok
}

// ---------------------------------------------------------------------------
// The `available, not set` split
// ---------------------------------------------------------------------------

// VocabularyEntry is one instruction alongside what the file says about it. It
// is the Dockerfile analogue of schema.Field, and carries the same Declared
// flag, so the two render through one component.
type VocabularyEntry struct {
	InstructionSpec
	// Declared is the whole split: false means this scope does not use the
	// instruction, and the form renders it as `available, not set`.
	Declared bool `json:"declared"`
	// Uses is how many times the scope uses it — 0 exactly when Declared is
	// false. A count rather than a boolean because "RUN appears eleven times"
	// is the fact a reader of a long stage wants.
	Uses int `json:"uses"`
	// Indices are the File.Instructions positions of those uses, so a client
	// can jump to them without searching. Nil when Declared is false.
	Indices []int `json:"indices,omitempty"`
}

// VocabularyView is the whole answer for one scope — a stage, or the file.
// It mirrors schema.Node: the counted split, then the fields, declared first.
type VocabularyView struct {
	// Scope is "file" or "stage".
	Scope string `json:"scope"`
	// DeclaredCount and AvailableCount are the two halves, counted once here
	// so a client never has to filter to render a heading.
	DeclaredCount  int `json:"declared_count"`
	AvailableCount int `json:"available_count"`
	// Instructions is every instruction the grammar permits, declared ones
	// first (in name order), then the available ones. One list, because the
	// reader is looking at one vocabulary.
	Instructions []VocabularyEntry `json:"instructions"`
	// Unknown are instruction names the file uses that this table does not
	// recognise. Never empty-by-omission: an engine that quietly dropped them
	// would be telling the reader their file contains only what we understand.
	Unknown []string `json:"unknown,omitempty"`
}

// vocabularyFor builds the split for a set of instruction indices.
func vocabularyFor(f *File, scope string, indices []int) VocabularyView {
	uses := map[string][]int{}
	var unknown []string
	seenUnknown := map[string]bool{}
	for _, i := range indices {
		in := f.Instructions[i]
		if in.Kind != KindInstruction || in.Name == "" {
			continue
		}
		if _, ok := byName[in.Name]; !ok {
			if !seenUnknown[in.Name] {
				seenUnknown[in.Name] = true
				unknown = append(unknown, in.Name)
			}
			continue
		}
		uses[in.Name] = append(uses[in.Name], i)
	}

	specs := Vocabulary()
	declared := make([]VocabularyEntry, 0, len(specs))
	available := make([]VocabularyEntry, 0, len(specs))
	for _, s := range specs {
		e := VocabularyEntry{InstructionSpec: s}
		if idx, ok := uses[s.Name]; ok {
			e.Declared, e.Uses, e.Indices = true, len(idx), idx
			declared = append(declared, e)
			continue
		}
		available = append(available, e)
	}
	sort.Strings(unknown)
	return VocabularyView{
		Scope:          scope,
		DeclaredCount:  len(declared),
		AvailableCount: len(available),
		Instructions:   append(declared, available...),
		Unknown:        unknown,
	}
}
