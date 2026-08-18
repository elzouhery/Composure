package diagnose

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 6.3's last acceptance criterion — a `build.dockerfile` pointing at a
// file that does not exist.
//
// The graph already carries a Dockerfile node for every `build:` section; what
// it cannot say is whether the file is there, because topology resolves nothing
// against a working directory on purpose. This rule is the one place that
// crosses that line, and it crosses it with the same path arithmetic the RPC
// that opens the file uses (dockerfile.ResolveBuild) — a file this rule reports
// as missing is exactly the file `stack/dockerfile` would try to open, or the
// two would disagree in front of the reader.
//
// It is a WARNING rather than an error. `docker compose build` will certainly
// fail, but `up` on a stack whose images are already built will not, and a rule
// that fires red on a working stack gets switched off.
//
// It stays silent wherever the answer would be a guess. A git or URL context, a
// context still holding `${VAR}`, and an inline Dockerfile are all cases where
// "that file does not exist" would be a confident wrong answer about a path
// that was never meant to be read from this disk.

type missingDockerfileRule struct{}

func (missingDockerfileRule) id() string { return "build-dockerfile-missing" }

func (missingDockerfileRule) title() string { return "build names a Dockerfile that is not there" }

func (r missingDockerfileRule) apply(c *context) []Finding {
	var out []Finding
	for _, n := range c.Graph.Nodes() {
		if n.Kind != topology.NodeDockerfile || n.Build == nil || n.Build.Inline {
			continue
		}
		if n.Origin.IsZero() {
			continue // AD-7: a finding that cannot say where it is does not exist
		}
		// A context or dockerfile built out of a variable nothing defines is
		// not a path on this disk. Interpolation runs at load now (story 1.3),
		// so `${CTX}/api` has already become `/api` by the time the graph sees
		// it — the reference is invisible here and only the resolver's finding
		// records that it was ever there.
		if c.undefinedUnder(n.Path) {
			continue
		}
		base := filepath.Dir(n.Origin.File)
		resolved, ok := dockerfile.ResolveBuild(base, n.Build.Context, n.Build.Dockerfile)
		if !ok {
			continue
		}
		if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
			continue
		}
		out = append(out, Finding{
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"%s builds from %s, and that file is not there (%s). The build will fail; "+
					"check the context and the dockerfile: name, or create the file.",
				serviceOf(n.Path), n.Build.Reference, resolved),
			Subjects: []resolve.Path{n.Path},
			Anchors: []Anchor{{
				Label:  "declared here",
				Path:   n.Path,
				Origin: n.Origin,
			}},
			// Creating a file is not a splice operation. Describing a fix this
			// engine cannot perform would be prose pretending to be data.
			NoFix: "no fix is described: creating a file is not an edit to an existing one, and this engine only splices bytes into files that exist",
		})
	}
	return out
}

// serviceOf names the service a build node hangs off: the node's path is
// `services.<name>.build`.
func serviceOf(p resolve.Path) string {
	if len(p) >= 2 && p[0] == "services" {
		return p[1]
	}
	return p.String()
}
