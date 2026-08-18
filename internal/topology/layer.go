package topology

import (
	"sort"

	"github.com/elzouhery/composure/internal/resolve"
)

// finalise puts the graph into its canonical, deterministic shape and assigns
// layers. It runs once, at the end of Build, and is the only place ordering is
// established — everything before it may append in whatever order it walked.
func (g *Graph) finalise() {
	sort.SliceStable(g.nodes, func(i, j int) bool {
		a, b := g.nodes[i], g.nodes[j]
		if a.Kind != b.Kind {
			return a.Kind.rank() < b.Kind.rank()
		}
		return comparePath(a.Path, b.Path) < 0
	})
	g.index = make(map[string]int, len(g.nodes))
	for i, n := range g.nodes {
		g.index[key(n.Path)] = i
	}

	sort.SliceStable(g.edges, func(i, j int) bool {
		return g.edges[i].sortKey() < g.edges[j].sortKey()
	})
	sort.SliceStable(g.dangling, func(i, j int) bool {
		return g.dangling[i].sortKey() < g.dangling[j].sortKey()
	})

	g.buildDependsAdjacency()
	g.assignLayers()
}

// buildDependsAdjacency projects the depends_on edges onto node indices.
//
// Duplicates are collapsed — the same pair can be declared twice across a
// merged project — so that a repeated declaration cannot change a layer or
// inflate a blast radius.
func (g *Graph) buildDependsAdjacency() {
	n := len(g.nodes)
	g.dependsOut = make([][]int, n)
	g.dependsIn = make([][]int, n)
	seen := make(map[[2]int]bool)
	for _, e := range g.edges {
		if e.Kind != EdgeDependsOn {
			continue
		}
		from, okF := g.index[key(e.From)]
		to, okT := g.index[key(e.To)]
		if !okF || !okT || seen[[2]int{from, to}] {
			continue
		}
		seen[[2]int{from, to}] = true
		g.dependsOut[from] = append(g.dependsOut[from], to)
		g.dependsIn[to] = append(g.dependsIn[to], from)
	}
	// The edge list is already sorted, so these are too — but sorting them
	// explicitly makes the determinism a property of this function rather than
	// of a caller that could be reordered later.
	for i := range g.dependsOut {
		sort.Ints(g.dependsOut[i])
		sort.Ints(g.dependsIn[i])
	}
}

// assignLayers gives every node a layer index such that a node's layer is
// strictly greater than the layer of everything it depends on, so dependency
// order reads top to bottom.
//
// Cycles do not break it. The graph is condensed into its strongly connected
// components first, and layering runs on the condensation, which is a DAG by
// construction. Every member of a cycle therefore shares one layer, and the
// cycle is reported rather than raised: story 3.4 diagnoses these, and a
// layering pass that failed here would deny diagnostics the graph of the one
// stack that most needs them.
//
// Nodes with no depends_on edges — every network, volume, config, secret and
// port, and any service nothing waits on — sit at layer 0.
func (g *Graph) assignLayers() {
	comp, components := stronglyConnected(g.dependsOut)

	// Condensation edges, deduplicated, in component order.
	compCount := len(components)
	compOut := make([][]int, compCount)
	seen := make(map[[2]int]bool)
	for from, targets := range g.dependsOut {
		for _, to := range targets {
			a, b := comp[from], comp[to]
			if a == b || seen[[2]int{a, b}] {
				continue
			}
			seen[[2]int{a, b}] = true
			compOut[a] = append(compOut[a], b)
		}
	}

	// Longest path to a sink over the condensation: layer(c) = 1 + max over
	// what c depends on. Memoised, so the walk is linear in edges.
	layer := make([]int, compCount)
	state := make([]int8, compCount) // 0 unvisited, 1 in progress, 2 done
	var visit func(int) int
	visit = func(c int) int {
		switch state[c] {
		case 2:
			return layer[c]
		case 1:
			// Unreachable: the condensation is acyclic. Returning 0 rather
			// than recursing keeps a future change to the SCC pass from
			// turning a bug into a hang.
			return 0
		}
		state[c] = 1
		best := 0
		for _, dep := range compOut[c] {
			if v := visit(dep) + 1; v > best {
				best = v
			}
		}
		layer[c] = best
		state[c] = 2
		return best
	}
	for c := 0; c < compCount; c++ {
		visit(c)
	}
	for i := range g.nodes {
		g.nodes[i].Layer = layer[comp[i]]
	}

	// A component is a cycle if it has more than one member, or if it is a
	// single node that depends on itself. The self-dependency is a real
	// compose mistake and is invisible to a size test alone.
	g.cycles = nil
	for _, members := range components {
		cyclic := len(members) > 1
		if !cyclic && len(members) == 1 {
			for _, to := range g.dependsOut[members[0]] {
				if to == members[0] {
					cyclic = true
					break
				}
			}
		}
		if !cyclic {
			continue
		}
		row := make([]resolve.Path, 0, len(members))
		for _, m := range members {
			row = append(row, clonePath(g.nodes[m].Path))
		}
		sortPaths(row)
		g.cycles = append(g.cycles, row)
	}
	sort.SliceStable(g.cycles, func(i, j int) bool {
		a, b := g.cycles[i], g.cycles[j]
		for k := 0; k < len(a) && k < len(b); k++ {
			if c := comparePath(a[k], b[k]); c != 0 {
				return c < 0
			}
		}
		return len(a) < len(b)
	})
}

// stronglyConnected is Tarjan's algorithm over an adjacency list. It returns
// the component index of each node and the members of each component.
//
// Components come out in reverse topological order of the condensation, but
// nothing here relies on that: layering memoises its own longest path, and the
// cycle list is sorted afterwards. Node iteration is by index, and the index
// order is already canonical, so the numbering is deterministic.
func stronglyConnected(adj [][]int) (comp []int, components [][]int) {
	n := len(adj)
	comp = make([]int, n)
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
		comp[i] = -1
	}
	var stack []int
	next := 0

	// Iterative, not recursive: a 5,000-service chain in the corpus is a
	// legitimate input, and blowing the stack is the one failure mode this
	// package must not have.
	type frame struct{ node, child int }
	for root := 0; root < n; root++ {
		if index[root] != -1 {
			continue
		}
		work := []frame{{root, 0}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true

		for len(work) > 0 {
			f := &work[len(work)-1]
			if f.child < len(adj[f.node]) {
				w := adj[f.node][f.child]
				f.child++
				if index[w] == -1 {
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					work = append(work, frame{w, 0})
				} else if onStack[w] && index[w] < low[f.node] {
					low[f.node] = index[w]
				}
				continue
			}
			v := f.node
			work = work[:len(work)-1]
			if low[v] == index[v] {
				var members []int
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					comp[w] = len(components)
					members = append(members, w)
					if w == v {
						break
					}
				}
				sort.Ints(members)
				components = append(components, members)
			}
			if len(work) > 0 {
				parent := work[len(work)-1].node
				if low[v] < low[parent] {
					low[parent] = low[v]
				}
			}
		}
	}
	return comp, components
}

func sortPaths(ps []resolve.Path) {
	sort.SliceStable(ps, func(i, j int) bool { return comparePath(ps[i], ps[j]) < 0 })
}
