package diagnose

import (
	"fmt"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 3.5 — a top-level volume or network that nothing uses.
//
// Usage is read from the graph's edges (AD-17): a volume is used when some
// service has an EdgeVolume pointing at it, a network when some service has an
// EdgeNetwork. Re-walking `volumes:` and `networks:` on every service would be
// a second mount parser, and the short-form mount grammar is exactly the place
// two parsers disagree.
//
// Three carve-outs, all of them the difference between a useful rule and one
// that gets switched off:
//
//   - The implicit `default` network is exempt. Compose creates it whether or
//     not a service names it, and a project that declares it in order to set a
//     driver has not left anything behind.
//   - A resource used only by a service outside the ACTIVE profile set is not
//     unused. The active graph cannot see that — a filtered service contributes
//     no edges at all — so usage is checked against a second graph built with
//     every declared profile active. The declaration is doing its job under
//     another profile, and telling someone to delete it would break their
//     stack the next time they passed --profile.
//   - An external volume or network reports at a lower severity. Something
//     outside this project may be using it, and this project's files cannot
//     know.

type unusedResourceRule struct{}

func (unusedResourceRule) id() string { return "unused-resource" }

func (unusedResourceRule) title() string { return "Declared resource that nothing uses" }

func (r unusedResourceRule) apply(c *context) []Finding {
	// The guard that used to stand here went silent on any project using
	// extends: or include:, because "nothing mounts this" is a claim a
	// fragment cannot support. Stories 1.7 and 1.8 expand both, so the mounts
	// that live in an included or extended file are in the model.
	// The active graph is the fallback, not a second answer: when AllProfiles is
	// absent there is no wider graph to ask, and "used under the active profiles"
	// is the most this can claim. Building both unconditionally computed one of
	// them for nothing.
	graph := c.Graph
	if c.AllProfiles != nil {
		graph = c.AllProfiles
	}
	usedAnyProfile := usedTargets(graph)

	var out []Finding
	for _, n := range c.Graph.Nodes() {
		if n.Kind != topology.NodeVolume && n.Kind != topology.NodeNetwork {
			continue
		}
		// Used without being declared is a different finding entirely, and this
		// rule must not claim such a node is unused.
		//
		// The guard cannot currently fire, and that is worth stating rather than
		// leaving for someone to rediscover by deleting it: topology creates an
		// undeclared volume or network node only in resolveTarget, which creates
		// it BECAUSE an edge points at it and returns it so the caller can add
		// that edge. So every undeclared node is in usedAnyProfile and the check
		// below already skips it. Mutation-tested: removing this line fails no
		// test today.
		//
		// It stays because that subsumption is a property of how topology
		// happens to build graphs, not of anything this rule controls, and the
		// failure it would prevent is telling a user to delete a declaration
		// that is not there. TestUndeclaredResourcesAreAlwaysEdgeTargets pins
		// the topology invariant, so if it ever stops holding this guard becomes
		// live and the suite says so.
		if !n.Declared {
			continue
		}
		if n.Kind == topology.NodeNetwork && n.Name == "default" {
			continue
		}
		if n.Origin.IsZero() {
			continue // AD-7
		}
		if usedAnyProfile[pathKey(n.Path)] {
			continue
		}
		out = append(out, r.finding(c, n))
	}
	return out
}

// usedTargets is every node some edge points at, as a set.
func usedTargets(g *topology.Graph) map[string]bool {
	out := map[string]bool{}
	for _, e := range g.Edges() {
		switch e.Kind {
		case topology.EdgeVolume, topology.EdgeNetwork:
			out[pathKey(e.To)] = true
		}
	}
	return out
}

func (r unusedResourceRule) finding(c *context, n topology.Node) Finding {
	kind := "volume"
	verb := "mounts"
	if n.Kind == topology.NodeNetwork {
		kind, verb = "network", "joins"
	}

	severity := SeverityWarning
	suffix := ""
	if n.External {
		// Something outside this project may be using it. Lower, not silent:
		// an external declaration nothing references is still dead weight in
		// this file, it is just not safe to assume it is dead everywhere.
		severity = SeverityHint
		suffix = " It is declared external, so something outside this project may still use it — check before removing the declaration."
	}

	f := Finding{
		Rule:     r.id(),
		Severity: severity,
		Title:    r.title(),
		Message: fmt.Sprintf("%s %q is declared but no service %s it, under any profile this project declares.%s",
			kind, n.Name, verb, suffix),
		Subjects: []resolve.Path{n.Path},
		Anchors: []Anchor{{
			Label:  fmt.Sprintf("%s declared here", kind),
			Path:   n.Path,
			Origin: n.Origin,
		}},
	}
	f.Fix, f.NoFix = c.deleteKeyFix(n.Path, n.Origin,
		fmt.Sprintf("delete the %s declaration for %q", kind, n.Name))
	return f
}
