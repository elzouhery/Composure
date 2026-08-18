package main

// `composure dockerfile` — the stage form, headless.
//
// Story 6.2's UI is a projection of this and nothing else. Two doors reach it:
// naming the Dockerfile directly, and naming a compose file plus the config
// path of a `build:` section, which resolves the file relative to the build
// context the way Compose reads it (story 6.3).
//
// A build that names a Dockerfile which is not there comes back as a form with
// `missing: true`, not as an error and not as silence. Story 6.3's last
// acceptance criterion is precisely that we do not silently omit it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// runDockerfile reads a Dockerfile — or the one a compose build section names —
// into the stage form and prints it.
func runDockerfile(path, at string, asJSON bool) {
	form, err := dockerfileForm(path, at)
	if err != nil {
		reportResolveFailure(path, err, asJSON)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(form); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printDockerfile(form)
}

// dockerfileForm is the shared implementation behind `composure dockerfile` and
// `stack/dockerfile`. One function, so the CLI and the editor cannot be shown
// two different Dockerfiles for the same build section.
func dockerfileForm(path, at string) (*dockerfile.Form, error) {
	target, ctx, name := path, "", ""
	if strings.TrimSpace(at) != "" {
		var err error
		target, ctx, name, err = dockerfileFromBuild(path, at)
		if err != nil {
			return nil, err
		}
	}
	src, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing is an answer, not a failure. The reader gets a form that
			// says which file is not there.
			return dockerfile.MissingForm(target, ctx, name), nil
		}
		return nil, err
	}
	form := dockerfile.BuildForm(target, src)
	form.Context, form.Dockerfile = ctx, name
	return form, nil
}

// dockerfileFromBuild resolves the Dockerfile a `build:` section names.
//
// The context is relative to the compose file that declared it and the
// dockerfile is relative to the context — Compose's own reading. The join is
// internal/dockerfile.ResolveBuild, which the missing-Dockerfile diagnostic
// also uses, so a file the rule reports as missing is the same file this
// command tries to open.
func dockerfileFromBuild(composePath, at string) (target, context, name string, err error) {
	project, err := resolve.File(composePath)
	if err != nil {
		return "", "", "", err
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		return "", "", "", err
	}
	want := resolve.ParsePath(at)
	for _, n := range graph.Nodes() {
		if n.Kind != topology.NodeDockerfile || !n.Path.Equal(want) {
			continue
		}
		if n.Build == nil {
			return "", "", "", fmt.Errorf("%s carries no build detail", at)
		}
		if n.Build.Inline {
			return "", "", "", fmt.Errorf("%s is a dockerfile_inline: the Dockerfile is written in the compose file and there is no second file to open", at)
		}
		base := filepath.Dir(n.Origin.File)
		if n.Origin.File == "" {
			base = filepath.Dir(composePath)
		}
		resolved, ok := dockerfile.ResolveBuild(base, n.Build.Context, n.Build.Dockerfile)
		if !ok {
			return "", "", "", fmt.Errorf("%s names a context this command cannot resolve to a local file (%q)", at, n.Build.Context)
		}
		return resolved, n.Build.Context, n.Build.Dockerfile, nil
	}
	return "", "", "", fmt.Errorf("%s is not a build section in %s", at, composePath)
}

func printDockerfile(f *dockerfile.Form) {
	fmt.Printf("\nDOCKERFILE — %s\n", f.Path)
	fmt.Println(strings.Repeat("=", 78))
	if f.Missing {
		fmt.Printf("this file does not exist\n")
		if f.Context != "" || f.Dockerfile != "" {
			fmt.Printf("named by build context %q, dockerfile %q\n", f.Context, f.Dockerfile)
		}
		fmt.Println()
		return
	}
	marks := []string{"escape=" + f.EscapeChar}
	if f.CRLF {
		marks = append(marks, "CRLF")
	}
	if f.BOM {
		marks = append(marks, "BOM")
	}
	fmt.Printf("%s\n", strings.Join(marks, " · "))

	for _, d := range f.Directives {
		fmt.Printf("  directive  %s\n", d.Text)
	}
	for _, p := range f.Preamble {
		if p.Kind != "instruction" {
			continue
		}
		fmt.Printf("  [%3d] %-8s %s\n", p.Index, p.Name, p.Args)
	}

	for _, s := range f.Stages {
		fmt.Printf("\nSTAGE %d — %s\n", s.Index, s.Label)
		fmt.Printf("  from      %s", s.ImageRef)
		if s.Platform != "" {
			fmt.Printf("  %s", s.Platform)
		}
		if s.Name != "" {
			fmt.Printf("  as %s", s.Name)
		}
		fmt.Printf("   (line %d, bytes %d-%d)\n", s.From.StartLine, s.From.ImageStart, s.From.ImageEnd)
		for _, in := range s.Instructions {
			if in.Kind == "blank" {
				continue
			}
			mark := " "
			if !in.Editable && in.Kind == "instruction" {
				mark = "*"
			}
			label := in.Name
			if in.Kind == "comment" {
				label = "#"
			}
			fmt.Printf("  [%3d]%s %-8s %s\n", in.Index, mark, label, firstLine(in.Text))
		}
		printStageVocabulary(s.Vocabulary)
	}
	fmt.Printf("\n%d stage(s). Lines marked * span several lines and will not be rewritten in place.\n", len(f.Stages))
	printFileVocabulary(f.Vocabulary)
	fmt.Println()
}

// printStageVocabulary is the one-line-per-half form: a stage that uses four
// instructions does not need fourteen lines of prose to say what it does not
// use, but it does need the names, because the names are the offer.
func printStageVocabulary(v dockerfile.VocabularyView) {
	fmt.Printf("  %d used, %d available and not used\n", v.DeclaredCount, v.AvailableCount)
	if names := vocabNames(v, false); names != "" {
		fmt.Printf("    not used  %s\n", names)
	}
	printUnknown("    ", v)
}

// printFileVocabulary is the whole grammar with its descriptions — the
// Dockerfile form of `composure schema`'s `available, not set`, printed in the
// same two-column shape so the two commands read alike.
func printFileVocabulary(v dockerfile.VocabularyView) {
	fmt.Printf("\nINSTRUCTION VOCABULARY — %d used, %d available and not used\n",
		v.DeclaredCount, v.AvailableCount)
	for _, e := range v.Instructions {
		if !e.Declared {
			continue
		}
		fmt.Printf("    used    %-12s x%-3d %s\n", e.Name, e.Uses, e.Summary)
	}
	for _, e := range v.Instructions {
		if e.Declared {
			continue
		}
		note := e.Summary
		if e.Deprecated {
			note += "  [deprecated: " + e.DeprecatedNote + "]"
		}
		fmt.Printf("    not set %-12s     %s\n", e.Name, note)
	}
	printUnknown("    ", v)
}

func vocabNames(v dockerfile.VocabularyView, declared bool) string {
	var names []string
	for _, e := range v.Instructions {
		if e.Declared == declared {
			names = append(names, e.Name)
		}
	}
	return strings.Join(names, " ")
}

// printUnknown says out loud what this engine does not recognise. Dropping it
// would tell the reader their file contains only what we understand.
func printUnknown(indent string, v dockerfile.VocabularyView) {
	if len(v.Unknown) > 0 {
		fmt.Printf("%sunrecognised by this engine: %s\n", indent, strings.Join(v.Unknown, ", "))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return strings.TrimSpace(s)
}
