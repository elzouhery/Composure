package diagnose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Analyze is the convenience entry point: resolve, derive, diagnose.
//
// It exists so the CLI and the RPC surface run the same three steps in the same
// order rather than each assembling an Input of its own — the rule serve.go
// already follows for stack/resolve. A caller that already holds a project and
// a graph should build an Input and call Run.
//
// A file that will not parse comes back as the resolver's typed error and no
// report at all (AD-13).
func Analyze(path string, profiles []string) (*Report, error) {
	project, err := resolve.File(path)
	if err != nil {
		return nil, err
	}
	return AnalyzeProject(path, project, profiles)
}

// AnalyzeProject diagnoses an already-resolved project. The entry path is still
// taken because it names the project directory, which is where a .env lives.
func AnalyzeProject(path string, project *resolve.Project, profiles []string) (*Report, error) {
	if project == nil {
		return nil, ErrNoProject
	}
	graph, err := topology.Build(project, profiles)
	if err != nil {
		return nil, err
	}
	// A second graph with every declared profile active. Rule 3.5 needs to
	// distinguish "nothing uses this volume" from "only a service another
	// profile would start uses it", and the active graph cannot say — a
	// filtered service contributes no edges at all. Building a second graph
	// keeps the answer inside topology (AD-17) instead of re-walking mounts.
	all, err := topology.Build(project, declaredProfiles(project))
	if err != nil {
		return nil, err
	}

	env, known := environment(filepath.Dir(path))
	return Run(Input{
		Path:        path,
		Project:     project,
		Graph:       graph,
		AllProfiles: all,
		Sources:     readSources(project),
		Env:         env,
		EnvKnown:    known,
	})
}

// declaredProfiles is every profile name any service mentions, sorted.
//
// This reads `profiles:` off each service, which is a value local to that
// service and not a relation — AD-17 is about edges, and this derives none. It
// exists only to hand topology an active set; the graph still does the
// filtering.
func declaredProfiles(p *resolve.Project) []string {
	seen := map[string]bool{}
	var out []string
	services := p.Services()
	for _, name := range services.Keys() {
		svc, _ := services.Get(name)
		v, ok := svc.At(resolve.Path{"profiles"})
		if !ok || v.Kind() != resolve.KindSequence {
			continue
		}
		for _, item := range v.Seq() {
			s := strings.TrimSpace(item.Scalar())
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// readSources loads every file the project was built from, keyed the way
// Origin.File names it. A file that cannot be read is skipped rather than
// fatal: it costs its findings a fix, never their existence.
func readSources(p *resolve.Project) map[string][]byte {
	out := map[string][]byte{}
	for _, f := range p.Files() {
		src, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		out[f.Path] = src
	}
	return out
}

// Environment is the one reader of the project environment in the codebase.
//
// It is exported because the inspector needs exactly the same answer: a
// `${VAR}` rendered with its resolution beneath (story 5.1) and a `${VAR}`
// reported as undefined (story 3.6) must agree, and they can only be
// guaranteed to agree by reading one implementation. A second `.env` parser in
// internal/schema would be a second precedence order, and the two would
// disagree on the day someone exports a variable that the file also sets.
//
// The `.env` PARSER itself lives in internal/resolve — story 1.3 needs it for
// the interpolation pass, and two parsers of the same format is two precedence
// orders. What is left here is the layering, which the inspector shares.
//
// dir is the directory holding the entry compose file.
func Environment(dir string) (map[string]string, bool) { return environment(dir) }

// environment is the variable environment Compose interpolation would draw on:
// the host environment, with a project `.env` beneath it — the host wins, which
// is Compose's own precedence.
//
// The bool reports whether the answer is trustworthy enough to base a finding
// on. It is false only when the host environment could not be read at all,
// which cannot currently happen; it is threaded through so that the
// undefined-variable rule stays silent rather than confident if that changes.
func environment(dir string) (map[string]string, bool) {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	if src, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
		for k, v := range resolve.ParseDotEnv(src) {
			if _, taken := out[k]; !taken {
				out[k] = v
			}
		}
	}
	return out, true
}
