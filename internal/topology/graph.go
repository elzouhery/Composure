package topology

import (
	"encoding/json"

	"github.com/elzouhery/composure/internal/resolve"
)

// Graph is the derived topology of one resolved project under one active
// profile set.
//
// Its fields are unexported and it has no setters. A graph is produced by
// Build and is thereafter immutable: it is a function of the model, so the way
// to change it is to change the file and resolve again. A mutable graph is how
// canvas state that exists nowhere in the file gets in (R2.6).
type Graph struct {
	profiles []string
	nodes    []Node
	edges    []Edge
	cycles   [][]resolve.Path
	dangling []Dangling

	// index maps a node's identity to its position in nodes.
	index map[string]int
	// dependsOut and dependsIn are the depends_on relation, prebuilt because
	// layering and every blast-radius query walk it. They hold indices into
	// nodes, never pointers, so nothing here can alias a returned Node.
	dependsOut [][]int
	dependsIn  [][]int
}

// Profiles returns the active profile set the graph was built for, sorted and
// deduplicated. Empty means Compose's default: only always-active services.
func (g *Graph) Profiles() []string { return cloneStrings(g.profiles) }

// Nodes returns every node, in a deterministic order: by kind, then by path.
func (g *Graph) Nodes() []Node {
	out := make([]Node, len(g.nodes))
	for i, n := range g.nodes {
		out[i] = n.clone()
	}
	return out
}

// Edges returns every edge, in a deterministic order.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, len(g.edges))
	for i, e := range g.edges {
		out[i] = e.clone()
	}
	return out
}

// Node looks a node up by its path.
func (g *Graph) Node(p resolve.Path) (Node, bool) {
	i, ok := g.index[key(p)]
	if !ok {
		return Node{}, false
	}
	return g.nodes[i].clone(), true
}

// Has reports whether a path is a node in this graph.
func (g *Graph) Has(p resolve.Path) bool {
	_, ok := g.index[key(p)]
	return ok
}

// Cycles returns the dependency cycles found while layering, each as its
// members' paths sorted, and the list itself sorted.
//
// Sorted means the RING ORDER IS NOT PRESERVED. A cycle is reported as the set
// of services caught in it, not as the path around it; joining consecutive
// members would draw edges the file never declared. The ring is recoverable
// from the depends_on edges among the members.
//
// Layering succeeds on a cyclic graph — the members share a layer — so this is
// a report, not an error. Story 3.4 diagnoses these; if layering failed here,
// diagnostics could never run on the stack that most needs them.
func (g *Graph) Cycles() [][]resolve.Path {
	out := make([][]resolve.Path, len(g.cycles))
	for i, c := range g.cycles {
		row := make([]resolve.Path, len(c))
		for j, p := range c {
			row[j] = clonePath(p)
		}
		out[i] = row
	}
	return out
}

// Dangling returns references dropped because the thing they named is not in
// this graph — a service filtered out by the active profile set, most often.
func (g *Graph) Dangling() []Dangling {
	out := make([]Dangling, len(g.dangling))
	for i, d := range g.dangling {
		out[i] = d.clone()
	}
	return out
}

// MaxLayer is the highest layer index assigned. Zero for a graph with no
// depends_on edges at all, which is also the layer of an isolated node.
func (g *Graph) MaxLayer() int {
	max := 0
	for _, n := range g.nodes {
		if n.Layer > max {
			max = n.Layer
		}
	}
	return max
}

// BlastRadius answers "what breaks if this goes down, and what does it need".
//
// It walks depends_on edges only. Those are the relation Compose actually
// orders startup by; a shared network or volume is a connection, not a
// dependency, and folding the two together would report a whole stack as the
// blast radius of every service that happens to be on the default network.
//
// The bool reports whether the path is a node at all.
func (g *Graph) BlastRadius(p resolve.Path) (Impact, bool) {
	start, ok := g.index[key(p)]
	if !ok {
		return Impact{}, false
	}
	return Impact{
		Subject:      clonePath(g.nodes[start].Path),
		Dependents:   g.reach(start, g.dependsIn),
		Dependencies: g.reach(start, g.dependsOut),
	}, true
}

// reach is a breadth-first transitive closure that excludes the start node,
// unconditionally.
//
// A cycle DOES lead back to the start — a→b→c→a reaches a again — and it is
// still dropped, because the question a blast radius answers is "what ELSE
// breaks". A subject listed among its own dependents reads as a finding rather
// than as a restatement of the subject. The cycle itself is not hidden by this:
// Cycles() reports it, and story 3.4 diagnoses it from there.
func (g *Graph) reach(start int, adj [][]int) []resolve.Path {
	seen := make(map[int]bool, len(g.nodes))
	queue := append([]int(nil), adj[start]...)
	var out []resolve.Path
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if cur != start {
			out = append(out, clonePath(g.nodes[cur].Path))
		}
		queue = append(queue, adj[cur]...)
	}
	// adj is already in sorted-node order, but a breadth-first walk visits in
	// distance order, so sort explicitly rather than relying on that.
	sortPaths(out)
	return out
}

// ---------------------------------------------------------------------------
// Wire shape.
//
// The JSON is the product surface for `composure topology -json` and the
// stack/topology RPC method, and the two share this marshaller so they cannot
// drift — the same rule serve.go already follows for stack/resolve.

type nodeJSON struct {
	// ID is Path.String(): the one canonical string form of the join key
	// (AD-14). resolve.ParsePath turns it back into segments.
	ID       string         `json:"id"`
	Kind     NodeKind       `json:"kind"`
	Name     string         `json:"name"`
	Origin   resolve.Origin `json:"origin"`
	Declared bool           `json:"declared"`
	External bool           `json:"external"`
	Internal bool           `json:"internal,omitempty"`
	Profiles []string       `json:"profiles"`
	Layer    int            `json:"layer"`
	Image    string         `json:"image,omitempty"`
	Port     *portJSON      `json:"port,omitempty"`
	Build    *buildJSON     `json:"build,omitempty"`
}

type buildJSON struct {
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Target     string `json:"target,omitempty"`
	Reference  string `json:"reference,omitempty"`
	Inline     bool   `json:"inline,omitempty"`
}

type portJSON struct {
	HostIP    string `json:"host_ip,omitempty"`
	Published string `json:"published"`
	Target    string `json:"target"`
	Protocol  string `json:"protocol"`
	Raw       string `json:"raw"`
}

type edgeJSON struct {
	Kind    EdgeKind       `json:"kind"`
	From    string         `json:"from"`
	To      string         `json:"to"`
	Origin  resolve.Origin `json:"origin"`
	Depends *dependsJSON   `json:"depends_on,omitempty"`
	Attach  *attachJSON    `json:"attach,omitempty"`
	Mount   *mountJSON     `json:"mount,omitempty"`
	Port    *portJSON      `json:"port,omitempty"`
	Build   *buildJSON     `json:"build,omitempty"`
}

type dependsJSON struct {
	Condition string `json:"condition"`
	Restart   string `json:"restart,omitempty"`
	Required  string `json:"required,omitempty"`
}

type attachJSON struct {
	Aliases []string `json:"aliases"`
}

type mountJSON struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Mode     string `json:"mode"`
	ReadOnly bool   `json:"read_only"`
	HostPath string `json:"host_path,omitempty"`
}

type danglingJSON struct {
	Kind   EdgeKind       `json:"kind"`
	From   string         `json:"from"`
	To     string         `json:"to"`
	Ref    string         `json:"ref"`
	Reason string         `json:"reason"`
	Origin resolve.Origin `json:"origin"`
}

type graphJSON struct {
	// Profiles is present and possibly empty rather than omitted: "no active
	// profiles" is a statement about the graph, not a missing field.
	Profiles []string   `json:"profiles"`
	Nodes    []nodeJSON `json:"nodes"`
	Edges    []edgeJSON `json:"edges"`
	// Cycles is each cycle's MEMBER SET, sorted — not the ring.
	//
	// This is a real limitation and it is stated rather than papered over: a
	// consumer that joined cycles[i][k] to cycles[i][k+1] would draw arrows
	// that do not exist, because sorting has thrown the traversal order away.
	// The edges are in `edges`; a client that wants the ring recovers it by
	// walking the depends_on edges among the members, which is what
	// internal/diagnose does.
	Cycles   [][]string     `json:"cycles"`
	Dangling []danglingJSON `json:"dangling"`
	MaxLayer int            `json:"max_layer"`
}

func portToJSON(p *PortDetail) *portJSON {
	if p == nil {
		return nil
	}
	return &portJSON{
		HostIP:    p.HostIP,
		Published: p.Published,
		Target:    p.Target,
		Protocol:  p.Protocol,
		Raw:       p.Raw,
	}
}

func buildToJSON(b *BuildDetail) *buildJSON {
	if b == nil {
		return nil
	}
	return &buildJSON{
		Context:    b.Context,
		Dockerfile: b.Dockerfile,
		Target:     b.Target,
		Reference:  b.Reference,
		Inline:     b.Inline,
	}
}

// MarshalJSON emits the stable wire schema. Every list is present and empty
// rather than null, so a client can iterate without a nil check and cannot
// mistake "none" for "not implemented".
//
// The schema is pinned byte for byte by TestWireSchemaGolden against
// testdata/wire.golden.json, over a fixture that exercises every node kind and
// every edge kind. Changing a field name, a list's emptiness, a default or an
// origin shows up as a diff in that file — which is the review artefact,
// because this is a contract other processes parse.
func (g *Graph) MarshalJSON() ([]byte, error) {
	out := graphJSON{
		Profiles: g.profiles,
		Nodes:    make([]nodeJSON, 0, len(g.nodes)),
		Edges:    make([]edgeJSON, 0, len(g.edges)),
		Cycles:   make([][]string, 0, len(g.cycles)),
		Dangling: make([]danglingJSON, 0, len(g.dangling)),
		MaxLayer: g.MaxLayer(),
	}
	if out.Profiles == nil {
		out.Profiles = []string{}
	}
	for _, n := range g.nodes {
		profiles := n.Profiles
		if profiles == nil {
			profiles = []string{}
		}
		out.Nodes = append(out.Nodes, nodeJSON{
			ID:       n.Path.String(),
			Kind:     n.Kind,
			Name:     n.Name,
			Origin:   n.Origin,
			Declared: n.Declared,
			External: n.External,
			Internal: n.Internal,
			Profiles: profiles,
			Layer:    n.Layer,
			Image:    n.Image,
			Port:     portToJSON(n.Port),
			Build:    buildToJSON(n.Build),
		})
	}
	for _, e := range g.edges {
		row := edgeJSON{
			Kind:   e.Kind,
			From:   e.From.String(),
			To:     e.To.String(),
			Origin: e.Origin,
			Port:   portToJSON(e.Port),
			Build:  buildToJSON(e.Build),
		}
		if e.Depends != nil {
			row.Depends = &dependsJSON{
				Condition: e.Depends.Condition,
				Restart:   e.Depends.Restart,
				Required:  e.Depends.Required,
			}
		}
		if e.Attach != nil {
			aliases := e.Attach.Aliases
			if aliases == nil {
				aliases = []string{}
			}
			row.Attach = &attachJSON{Aliases: aliases}
		}
		if e.Mount != nil {
			row.Mount = &mountJSON{
				Source:   e.Mount.Source,
				Target:   e.Mount.Target,
				Mode:     e.Mount.Mode,
				ReadOnly: e.Mount.ReadOnly(),
				HostPath: e.Mount.HostPath,
			}
		}
		out.Edges = append(out.Edges, row)
	}
	for _, c := range g.cycles {
		row := make([]string, 0, len(c))
		for _, p := range c {
			row = append(row, p.String())
		}
		out.Cycles = append(out.Cycles, row)
	}
	for _, d := range g.dangling {
		out.Dangling = append(out.Dangling, danglingJSON{
			Kind:   d.Kind,
			From:   d.From.String(),
			To:     d.To.String(),
			Ref:    d.Ref,
			Reason: d.Reason,
			Origin: d.Origin,
		})
	}
	return json.Marshal(out)
}
