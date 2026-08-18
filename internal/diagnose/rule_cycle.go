package diagnose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 3.4 — a circular `depends_on`.
//
// The cycles are the ones the layering pass already found (AD-17, story 2.4).
// This rule does not run a second cycle detection: layering condenses the graph
// into strongly connected components to assign layers at all, so the components
// are a by-product it has to compute anyway, and a second Tarjan here would be
// a second answer that could disagree with the picture on the canvas.
//
// Graph.Cycles returns each cycle's members sorted, because a set has no
// canonical starting point. A reader needs the ring. The order is recovered by
// walking the graph's own depends_on edges between the members — still
// topology's edges, never `depends_on` re-read from the model.
//
// A self-dependency is a cycle of one and is reported as such. Two independent
// cycles are two findings; they are never merged, because breaking a link in
// one does nothing for the other.
//
// No fix is offered, and that is the answer rather than an omission. Which link
// of a ring to break is a judgement about what the stack means — story 3.8 asks
// the rule to say so explicitly.

const cycleNoFix = "no automatic fix is offered: breaking a dependency ring means deciding which service does not actually need to wait for the next, and that is a judgement about what the stack means, not an edit a tool can choose"

type cycleRule struct{}

func (cycleRule) id() string { return "depends-on-cycle" }

func (cycleRule) title() string { return "Circular depends_on" }

func (r cycleRule) apply(c *context) []Finding {
	var out []Finding
	for _, members := range c.Graph.Cycles() {
		if len(members) == 0 {
			continue
		}
		ring, edges := orderCycle(c.Graph, members)
		if len(edges) == 0 {
			// Every member is in the cycle by the graph's own reckoning, so
			// there is always at least one edge between them. If there somehow
			// is not, there is nothing to anchor at and AD-7 applies.
			continue
		}
		var anchors []Anchor
		for _, e := range edges {
			if e.Origin.IsZero() {
				continue
			}
			anchors = append(anchors, Anchor{
				Label:  fmt.Sprintf("%s depends on %s", serviceName(e.From), serviceName(e.To)),
				Path:   e.From,
				Origin: e.Origin,
			})
		}
		if len(anchors) == 0 {
			continue
		}
		out = append(out, Finding{
			Rule:     r.id(),
			Severity: SeverityError,
			Title:    r.title(),
			Message:  r.message(ring),
			Subjects: ring,
			Anchors:  anchors,
			NoFix:    cycleNoFix,
		})
	}
	return out
}

func (r cycleRule) message(ring []resolve.Path) string {
	names := make([]string, 0, len(ring))
	for _, p := range ring {
		names = append(names, serviceName(p))
	}
	if len(names) == 1 {
		return fmt.Sprintf("service %q depends on itself, so it can never start.", names[0])
	}
	return fmt.Sprintf("depends_on forms a cycle: %s. None of these services can start, because each is waiting on the next.",
		strings.Join(append(names, names[0]), " -> "))
}

// orderCycle turns a cycle's member set into the ring a reader can follow, and
// returns the depends_on edges that form it.
//
// The walk starts at the lowest-sorted member so that the same cycle renders
// identically on every run — a report that rotates between runs cannot be
// diffed in CI. From each member it follows the single depends_on edge that
// stays inside the cycle; where a member has more than one such edge the
// component is a tangle rather than a simple ring, and the walk takes the
// lowest-sorted continuation and stops when it closes or runs out. Every member
// still appears: any it did not reach are appended in sorted order, so the
// finding never silently loses a service.
func orderCycle(g *topology.Graph, members []resolve.Path) ([]resolve.Path, []topology.Edge) {
	inCycle := map[string]resolve.Path{}
	for _, m := range members {
		inCycle[pathKey(m)] = m
	}

	// The depends_on edges that stay inside the component, indexed by source
	// and sorted so the walk is deterministic.
	next := map[string][]topology.Edge{}
	for _, e := range g.Edges() {
		if e.Kind != topology.EdgeDependsOn {
			continue
		}
		if _, ok := inCycle[pathKey(e.From)]; !ok {
			continue
		}
		if _, ok := inCycle[pathKey(e.To)]; !ok {
			continue
		}
		next[pathKey(e.From)] = append(next[pathKey(e.From)], e)
	}
	for k := range next {
		edges := next[k]
		sort.SliceStable(edges, func(i, j int) bool {
			return strings.Join(edges[i].To, "\x00") < strings.Join(edges[j].To, "\x00")
		})
		next[k] = edges
	}

	start := members[0] // Cycles() already sorted them
	var (
		ring  []resolve.Path
		used  []topology.Edge
		seen  = map[string]bool{}
		cur   = start
		guard = len(members) + 1
	)
	for i := 0; i < guard; i++ {
		k := pathKey(cur)
		if seen[k] {
			break
		}
		seen[k] = true
		ring = append(ring, cur)
		edges := next[k]
		if len(edges) == 0 {
			break
		}
		used = append(used, edges[0])
		cur = edges[0].To
	}
	for _, m := range members {
		if !seen[pathKey(m)] {
			ring = append(ring, m)
			seen[pathKey(m)] = true
			// A member the ring walk did not reach still has its declaring
			// edges, and the finding must anchor at them.
			used = append(used, next[pathKey(m)]...)
		}
	}
	return ring, used
}
