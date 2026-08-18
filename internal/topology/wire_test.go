package topology

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

// updateGolden rewrites testdata/wire.golden.json from this run. It is for
// regenerating the file after a DELIBERATE schema change, and the diff is the
// review artefact: every line of it is a change to the contract the extension
// and `composure topology -json` consume.
var updateGolden = flag.Bool("update-golden", false, "rewrite the wire-schema golden file")

const (
	wireFixture = "testdata/wire.yaml"
	wireGolden  = "testdata/wire.golden.json"
)

// buildWireFixture builds the graph the golden pins.
//
// The fixture is read from a file rather than embedded in this test so that the
// line and column numbers in the golden address something a reader can open.
// The name handed to the resolver is the bare basename, so the recorded
// provenance does not carry a path that differs between a package run and a
// repository-root run.
func buildWireFixture(t *testing.T) *Graph {
	t.Helper()
	src, err := os.ReadFile(wireFixture)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	p, err := resolve.Bytes(filepath.Base(wireFixture), src)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	g, err := Build(p, []string{"full"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

func wireJSON(t *testing.T, g *Graph) []byte {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatalf("indent: %v", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// TestWireSchemaGolden pins the JSON contract byte for byte.
//
// MarshalJSON is the product surface: `composure topology -json` and the
// stack/topology RPC both go through it, and the extension reads the result. A
// determinism test cannot see this — it marshals twice and compares the two
// outputs, so it catches instability and never wrongness. Renaming `id`,
// emitting a list as null, dropping `dangling`, hardcoding `read_only` or
// `layer`, or zeroing an origin all leave two runs agreeing perfectly.
//
// The fixture exercises every NodeKind and every EdgeKind, plus dangling
// references, a cycle, an external resource and both port forms, so a change to
// any part of the schema lands in this diff.
func TestWireSchemaGolden(t *testing.T) {
	got := wireJSON(t, buildWireFixture(t))

	if *updateGolden {
		if err := os.WriteFile(wireGolden, got, 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		t.Logf("golden rewritten: %s", wireGolden)
		return
	}

	want, err := os.ReadFile(wireGolden)
	if err != nil {
		t.Fatalf("reading golden (run `go test ./internal/topology -update-golden` to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the JSON wire schema changed.\n\n--- want (%s)\n%s\n--- got\n%s\n\n"+
			"If the change is deliberate, rerun with -update-golden and argue for the diff: "+
			"this is the contract the extension consumes.", wireGolden, want, got)
	}
}

// TestWireSchemaCoversEveryKind guards the golden against becoming weaker than
// it looks. A golden over a fixture that lost a node kind still passes; it just
// stops pinning that kind. This asserts the fixture is still complete.
func TestWireSchemaCoversEveryKind(t *testing.T) {
	g := buildWireFixture(t)

	nodeKinds := map[NodeKind]bool{}
	for _, n := range g.Nodes() {
		nodeKinds[n.Kind] = true
	}
	for _, k := range []NodeKind{
		NodeService, NodeNetwork, NodeVolume, NodeConfig, NodeSecret, NodePort, NodeDockerfile,
	} {
		if !nodeKinds[k] {
			t.Errorf("the wire fixture no longer produces a %q node, so the golden stops pinning it", k)
		}
	}

	edgeKinds := map[EdgeKind]bool{}
	for _, e := range g.Edges() {
		edgeKinds[e.Kind] = true
	}
	for _, k := range []EdgeKind{
		EdgeDependsOn, EdgeNetwork, EdgeVolume, EdgeBind, EdgeConfig,
		EdgeSecret, EdgeLink, EdgeNetworkMode, EdgePublish, EdgeBuild,
	} {
		if !edgeKinds[k] {
			t.Errorf("the wire fixture no longer produces a %q edge, so the golden stops pinning it", k)
		}
	}

	if len(g.Cycles()) == 0 {
		t.Error("the wire fixture no longer contains a cycle")
	}
	if len(g.Dangling()) == 0 {
		t.Error("the wire fixture no longer contains a dangling reference")
	}
	if g.MaxLayer() == 0 {
		t.Error("the wire fixture no longer layers anything above 0, so max_layer is pinned at its zero value")
	}
}

// TestWireListsAreNeverNull is the promise MarshalJSON's own doc comment makes:
// every list is present and empty rather than null, so a client can iterate
// without a nil check. An empty graph is where that breaks, and the golden
// fixture — which is deliberately full — cannot see it.
func TestWireListsAreNeverNull(t *testing.T) {
	// Every list empty at once: a project whose only service is behind an
	// inactive profile has no nodes, no edges, no cycles and no dangling
	// references. This is the shape the promise is about, and it is the shape
	// the golden fixture — deliberately full — cannot exercise.
	empty := buildSrc(t, "services:\n  a: {image: x, profiles: [off]}\n")
	if len(empty.Nodes()) != 0 || len(empty.Edges()) != 0 {
		t.Fatalf("the fixture is no longer empty: %d nodes, %d edges", len(empty.Nodes()), len(empty.Edges()))
	}
	// The zero Graph is in the list too. Build never produces one — the
	// builder normalises the profile list before finalise sees it — so the
	// nil-guard in MarshalJSON is only reachable from here, and a guard no test
	// can reach is a guard that can be deleted.
	for _, g := range []*Graph{empty, &Graph{}, buildWireFixture(t)} {
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"profiles", "nodes", "edges", "cycles", "dangling"} {
			v, ok := doc[k]
			if !ok {
				t.Errorf("%q is absent from the wire shape", k)
				continue
			}
			if string(v) == "null" {
				t.Errorf("%q is null; the schema promises an empty list a client can iterate", k)
			}
		}
		if _, ok := doc["max_layer"]; !ok {
			t.Error("max_layer is absent from the wire shape")
		}
	}

	// A node's own `profiles` list has the same promise, and a service that
	// declares none is where it breaks.
	raw, err := json.Marshal(buildSrc(t, "services:\n  a: {image: x}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Nodes []map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Nodes) != 1 {
		t.Fatalf("want one node, got %d", len(doc.Nodes))
	}
	if string(doc.Nodes[0]["profiles"]) != "[]" {
		t.Errorf("node profiles = %s, want []", doc.Nodes[0]["profiles"])
	}
}

// ---------------------------------------------------------------------------
// Canonical order.
//
// Every sort in layer.go was unfalsifiable before these: resolve.OrderedMap
// already makes the builder deterministic, so two runs agree whether or not
// finalise sorts anything. These assert the ORDER ITSELF — the content
// graph.go's doc comment promises — over inputs whose source order is
// deliberately not the canonical one.

// orderSrc declares its services in reverse alphabetical order and its edges in
// an order no sort would produce. Every list below therefore differs from the
// order the builder appended in.
const orderSrc = `
services:
  zulu:
    image: x
    ports: ["9000:90"]
    volumes: ["./z:/z"]
    networks: [znet]
    depends_on: [alpha]
    links: [alpha]
  mike:
    image: x
    profiles: [off]
  alpha:
    image: x
    build: ./a
    volumes: ["shared:/s"]
    depends_on: [mike]
networks:
  znet: {}
volumes:
  shared: {}
`

func TestNodeOrderIsKindThenPath(t *testing.T) {
	g := buildSrc(t, orderSrc)
	got := nodeIDs(g)
	want := []string{
		// Services first, by path — not the reverse order the file declares.
		"service services.alpha",
		"service services.zulu",
		"network networks.znet",
		"volume volumes.shared",
		"port services.zulu.ports[0]",
		"dockerfile services.alpha.build",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("node order:\n got %v\nwant %v", got, want)
	}
}

func TestEdgeOrderIsCanonical(t *testing.T) {
	g := buildSrc(t, orderSrc)
	got := edgeStrings(g)
	// From-path first, then edge kind, then to-path: `alpha` before `zulu`
	// though the file declares zulu first, and within a service the kinds in
	// their own order rather than the order walkServices appends them.
	want := []string{
		"build services.alpha -> services.alpha.build",
		"volume services.alpha -> volumes.shared",
		"bind services.zulu -> services.zulu",
		"depends_on services.zulu -> services.alpha",
		"link services.zulu -> services.alpha",
		"network services.zulu -> networks.znet",
		"publish services.zulu -> services.zulu.ports[0]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edge order:\n got %v\nwant %v", got, want)
	}

	// Stated as the invariant too, so a future edge kind is covered without
	// anyone remembering to extend the table above.
	edges := g.Edges()
	for i := 1; i < len(edges); i++ {
		if edges[i-1].sortKey() > edges[i].sortKey() {
			t.Errorf("edges %d and %d are out of order:\n %q\n %q",
				i-1, i, edges[i-1].sortKey(), edges[i].sortKey())
		}
	}
}

func TestDanglingOrderIsCanonical(t *testing.T) {
	// Two dangling reports, produced in the order the file declares the
	// services — which is the reverse of the canonical one.
	g := buildSrc(t, `
services:
  zulu:
    image: x
    depends_on: [hidden]
  alpha:
    image: x
    volumes: ["/anon"]
  hidden:
    image: x
    profiles: [off]
`)
	d := g.Dangling()
	if len(d) != 2 {
		t.Fatalf("dangling = %+v, want two entries", d)
	}
	if d[0].From.String() != "services.alpha" || d[1].From.String() != "services.zulu" {
		t.Errorf("dangling order = %s, %s; want alpha before zulu",
			d[0].From, d[1].From)
	}
	for i := 1; i < len(d); i++ {
		if d[i-1].sortKey() > d[i].sortKey() {
			t.Errorf("dangling %d and %d are out of order", i-1, i)
		}
	}
}

// TestCycleOrderIsCanonical uses a shape whose DISCOVERY order is not its
// sorted order: Tarjan reaches the {b,c} component before it can close {a,d},
// so an unsorted cycle list reports {b,c} first.
func TestCycleOrderIsCanonical(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    image: x
    depends_on: [b, d]
  b:
    image: x
    depends_on: [c]
  c:
    image: x
    depends_on: [b]
  d:
    image: x
    depends_on: [a]
`)
	var got []string
	for _, c := range g.Cycles() {
		var members []string
		for _, p := range c {
			members = append(members, p.String())
		}
		got = append(got, strings.Join(members, "+"))
	}
	want := []string{"services.a+services.d", "services.b+services.c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cycle order:\n got %v\nwant %v", got, want)
	}
}

// TestSCCMembersAreSorted exercises Tarjan directly. The member order is not
// observable through Build — assignLayers sorts the paths it renders from them
// — so the only way to hold the property is to assert it where it is
// established.
func TestSCCMembersAreSorted(t *testing.T) {
	// 0 -> 1 -> 2 -> 0, plus an isolated 3. Tarjan pops the cycle off its stack
	// in reverse discovery order, so an unsorted result is [2 1 0].
	adj := [][]int{{1}, {2}, {0}, nil}
	comp, components := stronglyConnected(adj)
	if len(components) != 2 {
		t.Fatalf("components = %v, want two", components)
	}
	for i, members := range components {
		if !sort.IntsAreSorted(members) {
			t.Errorf("component %d members are not ascending: %v", i, members)
		}
	}
	// And the cycle really is the three-member one, so the assertion above is
	// not passing over a set of singletons.
	var big []int
	for _, members := range components {
		if len(members) > len(big) {
			big = members
		}
	}
	if !reflect.DeepEqual(big, []int{0, 1, 2}) {
		t.Errorf("cycle members = %v, want [0 1 2]", big)
	}
	if comp[0] != comp[1] || comp[1] != comp[2] || comp[3] == comp[0] {
		t.Errorf("component assignment = %v", comp)
	}
}

// TestFinaliseSortsAScrambledGraph drives finalise() on a graph assembled by
// hand in a deliberately wrong order.
//
// This is the only way to make the node, edge, dangling and adjacency sorts
// falsifiable as a set: the builder feeds finalise a nearly-canonical order
// already, because resolve.OrderedMap preserves source order and the walk is
// kind-by-kind. Deleting any sort in finalise leaves every end-to-end test
// green for the adjacency lists in particular, because depends_on always joins
// two services and service indices are already in path order.
func TestFinaliseSortsAScrambledGraph(t *testing.T) {
	n := func(kind NodeKind, seg ...string) Node {
		return Node{Path: resolve.Path(seg), Kind: kind, Name: seg[len(seg)-1]}
	}
	dep := func(from, to string) Edge {
		return Edge{
			Kind: EdgeDependsOn,
			From: resolve.Path{"services", from},
			To:   resolve.Path{"services", to},
		}
	}
	g := &Graph{
		nodes: []Node{
			n(NodeVolume, "volumes", "v"),
			n(NodeService, "services", "d"),
			n(NodeService, "services", "a"),
			n(NodeNetwork, "networks", "n"),
			n(NodeService, "services", "c"),
			n(NodeService, "services", "b"),
		},
		edges: []Edge{
			dep("a", "d"),
			dep("a", "b"),
			{Kind: EdgeNetwork, From: resolve.Path{"services", "a"}, To: resolve.Path{"networks", "n"}},
			dep("c", "b"),
			dep("b", "d"),
		},
		dangling: []Dangling{
			{Kind: EdgeVolume, From: resolve.Path{"services", "c"}, To: resolve.Path{"volumes", "z"}},
			{Kind: EdgeVolume, From: resolve.Path{"services", "a"}, To: resolve.Path{"volumes", "z"}},
		},
	}
	g.finalise()

	wantNodes := []string{
		"service services.a", "service services.b", "service services.c",
		"service services.d", "network networks.n", "volume volumes.v",
	}
	if got := nodeIDs(g); !reflect.DeepEqual(got, wantNodes) {
		t.Errorf("nodes:\n got %v\nwant %v", got, wantNodes)
	}
	wantEdges := []string{
		"depends_on services.a -> services.b",
		"depends_on services.a -> services.d",
		"network services.a -> networks.n",
		"depends_on services.b -> services.d",
		"depends_on services.c -> services.b",
	}
	if got := edgeStrings(g); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges:\n got %v\nwant %v", got, wantEdges)
	}
	if got := g.Dangling(); got[0].From.String() != "services.a" {
		t.Errorf("dangling order = %s first, want services.a", got[0].From)
	}

	// index is rebuilt from the sorted node slice, so a stale index would make
	// every lookup answer about a different node.
	for i, node := range g.nodes {
		if g.index[key(node.Path)] != i {
			t.Errorf("index[%s] = %d, want %d", node.Path, g.index[key(node.Path)], i)
		}
	}

	// Adjacency: node a (index 0) depends on b (1) and d (3), appended in the
	// edge order a->b, a->d — but node d's dependsIn is filled from a (0) and
	// b (1) in that order only because the edges happen to sort that way. The
	// assertion is on the sorted result.
	for i, row := range g.dependsOut {
		if !sort.IntsAreSorted(row) {
			t.Errorf("dependsOut[%d] = %v is not ascending", i, row)
		}
	}
	for i, row := range g.dependsIn {
		if !sort.IntsAreSorted(row) {
			t.Errorf("dependsIn[%d] = %v is not ascending", i, row)
		}
	}
	if want := []int{1, 3}; !reflect.DeepEqual(g.dependsOut[0], want) {
		t.Errorf("dependsOut[a] = %v, want %v", g.dependsOut[0], want)
	}
	if want := []int{0, 1}; !reflect.DeepEqual(g.dependsIn[3], want) {
		t.Errorf("dependsIn[d] = %v, want %v", g.dependsIn[3], want)
	}
}

// TestFinaliseCanonicalisesAcrossNodeKinds is the adjacency sort and the
// cycle-member sort on their own.
//
// Both are invisible to anything Build can produce, and for one reason: a
// depends_on edge always joins two SERVICES, and among services the node index
// order and the edge sort order are the same order. So the edge sort leaves the
// adjacency lists already ascending and the SCC member sort leaves the cycle
// rows already in path order, and deleting either sort changes nothing.
//
// The two sorts are not decoration — buildDependsAdjacency's own comment says
// the point is to make determinism a property of that function rather than of
// whatever calls it. Holding that means feeding it an order its caller does not
// currently produce, which is what this does: a depends_on edge to a network.
// Index order (services before networks) and key order (networks before
// services) then disagree, and the sorts are the only thing that reconciles
// them.
func TestFinaliseCanonicalisesAcrossNodeKinds(t *testing.T) {
	dep := func(from, to resolve.Path) Edge {
		return Edge{Kind: EdgeDependsOn, From: from, To: to}
	}
	a := resolve.Path{"services", "a"}
	b := resolve.Path{"services", "b"}
	n := resolve.Path{"networks", "n"}
	g := &Graph{
		nodes: []Node{
			{Path: n, Kind: NodeNetwork, Name: "n"},
			{Path: a, Kind: NodeService, Name: "a"},
			{Path: b, Kind: NodeService, Name: "b"},
		},
		edges: []Edge{dep(a, n), dep(n, a), dep(a, b), dep(n, b)},
	}
	g.finalise()
	// Sorted node order: a=0, b=1, n=2. Edge order is by From key then To key,
	// so `networks.n` sorts ahead of `services.a` on both sides — the reverse
	// of the index order.
	if want := []int{1, 2}; !reflect.DeepEqual(g.dependsOut[0], want) {
		t.Errorf("dependsOut[a] = %v, want %v ascending", g.dependsOut[0], want)
	}
	if want := []int{0, 2}; !reflect.DeepEqual(g.dependsIn[1], want) {
		t.Errorf("dependsIn[b] = %v, want %v ascending", g.dependsIn[1], want)
	}

	// a and n are a cycle, and its members are reported in path order —
	// networks before services — not in node-index order.
	cycles := g.Cycles()
	if len(cycles) != 1 {
		t.Fatalf("cycles = %v, want exactly one", cycles)
	}
	var members []string
	for _, p := range cycles[0] {
		members = append(members, p.String())
	}
	if want := []string{"networks.n", "services.a"}; !reflect.DeepEqual(members, want) {
		t.Errorf("cycle members = %v, want %v in path order", members, want)
	}
}

// ---------------------------------------------------------------------------
// Provenance and the guards.

// TestServiceNodeOriginIsTheDeclarationKey closes R1.8's hole for the node kind
// every consumer starts from. Network, external, port and build origins were
// pinned; the service origin was not, and zeroing it left the suite green.
func TestServiceNodeOriginIsTheDeclarationKey(t *testing.T) {
	g := buildSrc(t, "# a comment\nservices:\n  web:\n    image: nginx\n  api:\n    image: busybox\n")
	for _, tc := range []struct {
		path string
		line int
	}{
		{"services.web", 3},
		{"services.api", 5},
	} {
		n, ok := g.Node(resolve.ParsePath(tc.path))
		if !ok {
			t.Fatalf("%s is not a node", tc.path)
		}
		if n.Origin.File != "test.yaml" || n.Origin.Line != tc.line || n.Origin.Column != 3 {
			t.Errorf("%s origin = %s, want test.yaml:%d:3 (the service key)", tc.path, n.Origin, tc.line)
		}
	}
}

// TestAddNodeRefusesADuplicate drives the guard directly.
//
// It cannot be reached through Build today — every caller checks hasNode first,
// and no two paths collide — but the guard is the reason the builder type
// exists, and a test that cannot fail is not a test. Constructing the collision
// is the only honest way to hold it.
func TestAddNodeRefusesADuplicate(t *testing.T) {
	b := &builder{nodeAt: map[string]int{}, declared: map[string]bool{}}
	p := resolve.Path{"services", "web"}
	b.addNode(Node{Path: p, Kind: NodeService, Name: "first"})
	b.addNode(Node{Path: p, Kind: NodeService, Name: "second"})
	if len(b.nodes) != 1 {
		t.Fatalf("addNode admitted a duplicate path: %d nodes", len(b.nodes))
	}
	if b.nodes[0].Name != "first" {
		t.Errorf("the duplicate overwrote the original: name = %q, want %q", b.nodes[0].Name, "first")
	}
	if b.nodeAt[key(p)] != 0 {
		t.Errorf("index for %s = %d, want 0", p, b.nodeAt[key(p)])
	}
}

// TestRepeatedDependsOnPairIsCollapsed is buildDependsAdjacency's dedupe. A
// sequence may name the same service twice — `depends_on: [b, b]` is legal —
// and two entries in the adjacency list would inflate nothing visible today,
// which is exactly why the dedupe needs its own assertion.
func TestRepeatedDependsOnPairIsCollapsed(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, depends_on: [b, b]}\n  b: {image: x}\n")
	var deps int
	for _, e := range g.Edges() {
		if e.Kind == EdgeDependsOn {
			deps++
		}
	}
	if deps != 2 {
		t.Fatalf("the file declares the pair twice; got %d edges, want both carried", deps)
	}
	ai := g.index[key(resolve.Path{"services", "a"})]
	bi := g.index[key(resolve.Path{"services", "b"})]
	if got := g.dependsOut[ai]; len(got) != 1 || got[0] != bi {
		t.Errorf("dependsOut[a] = %v, want exactly one entry for b", got)
	}
	if got := g.dependsIn[bi]; len(got) != 1 || got[0] != ai {
		t.Errorf("dependsIn[b] = %v, want exactly one entry for a", got)
	}
}

func TestMaxLayer(t *testing.T) {
	deep := buildSrc(t, `
services:
  gateway: {image: x, depends_on: [web]}
  web: {image: x, depends_on: [api]}
  api: {image: x, depends_on: [db]}
  db: {image: x}
`)
	if got := deep.MaxLayer(); got != 3 {
		t.Errorf("MaxLayer() = %d, want 3", got)
	}
	flat := buildSrc(t, "services:\n  a: {image: x}\n  b: {image: x}\n")
	if got := flat.MaxLayer(); got != 0 {
		t.Errorf("MaxLayer() on a graph with no depends_on = %d, want 0", got)
	}
	// And it is the same number the wire shape publishes.
	raw, err := json.Marshal(deep)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MaxLayer int `json:"max_layer"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MaxLayer != 3 {
		t.Errorf("max_layer in the wire shape = %d, want 3", doc.MaxLayer)
	}
}

// ---------------------------------------------------------------------------
// Scalar readers.

func TestTruthy(t *testing.T) {
	// Every one of these is a real shape: `external: yes` in particular is
	// common, and yaml.v3 hands it over as the string "yes" rather than a bool.
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"yes", true},
		{"Yes", true},
		{"on", true},
		{"ON", true},
		{"1", true},
		{"false", false},
		{"no", false},
		{"off", false},
		{"0", false},
		{"maybe", false},
		{`""`, false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			g := buildSrc(t, "services:\n  a: {image: x}\nvolumes:\n  v:\n    external: "+tc.value+"\n")
			n, ok := g.Node(resolve.Path{"volumes", "v"})
			if !ok {
				t.Fatal("v is not a node")
			}
			if n.External != tc.want {
				t.Errorf("external: %s read as %v, want %v", tc.value, n.External, tc.want)
			}
		})
	}

	// The deprecated mapping form is still an assertion that the resource is
	// external, and an absent key is not.
	g := buildSrc(t, "services:\n  a: {image: x}\nvolumes:\n  m: {external: {name: legacy}}\n  plain: {}\n")
	m, _ := g.Node(resolve.Path{"volumes", "m"})
	if !m.External {
		t.Error("external: {name: x} was not read as external")
	}
	plain, _ := g.Node(resolve.Path{"volumes", "plain"})
	if plain.External {
		t.Error("a volume with no external key was reported external")
	}
	if truthy(nil, false) || truthy(nil, true) {
		t.Error("truthy(nil) is true")
	}
}

// TestLongPortDefaultsToTCP: the default is the whole answer to "which
// protocol", and removing it leaves an empty string that reads as a port
// nobody can classify.
func TestLongPortDefaultsToTCP(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    image: x
    ports:
      - target: 80
        published: "8080"
`)
	n, ok := g.Node(resolve.ParsePath("services.a.ports[0]"))
	if !ok {
		t.Fatal("no port node")
	}
	if n.Port.Protocol != defaultProtocol {
		t.Errorf("protocol = %q, want %q", n.Port.Protocol, defaultProtocol)
	}
	// Raw carries no protocol suffix when the protocol is the default: a label
	// reading `8080:80/tcp` for a file that says `8080:80` is not what the file
	// says.
	if n.Port.Raw != "8080:80" {
		t.Errorf("raw = %q, want %q", n.Port.Raw, "8080:80")
	}
}

// ---------------------------------------------------------------------------
// reach().

// TestBlastRadiusOfASelfLoop pins what reach() actually does with a cycle that
// leads back to the start: the start is excluded unconditionally.
//
// The doc comment used to claim the opposite — that a cycle back to the start
// leaves it in — while TestBlastRadiusSurvivesACycle pinned the exclusion. The
// code was right and the comment was the defect; this is the case that makes
// the difference visible, because here the ONLY thing the walk reaches is the
// start itself.
func TestBlastRadiusOfASelfLoop(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, depends_on: [a]}\n")
	impact, ok := g.BlastRadius(resolve.Path{"services", "a"})
	if !ok {
		t.Fatal("a is not a node")
	}
	if len(impact.Dependents) != 0 {
		t.Errorf("dependents of a self-dependent service = %v, want empty: the subject is excluded", impact.Dependents)
	}
	if len(impact.Dependencies) != 0 {
		t.Errorf("dependencies of a self-dependent service = %v, want empty", impact.Dependencies)
	}
	if impact.Subject.String() != "services.a" {
		t.Errorf("subject = %s", impact.Subject)
	}
	// The self-loop is still reported as a cycle: excluding it from the blast
	// radius must not hide it.
	if len(g.Cycles()) != 1 {
		t.Errorf("cycles = %v, want the self-loop reported", g.Cycles())
	}
}

// TestBlastRadiusExcludesTheSubjectInsideALongerCycle is the same rule one hop
// out: a→b→c→a reaches a again, and a is still not its own dependent.
func TestBlastRadiusExcludesTheSubjectInsideALongerCycle(t *testing.T) {
	g := buildSrc(t, `
services:
  a: {image: x, depends_on: [b]}
  b: {image: x, depends_on: [c]}
  c: {image: x, depends_on: [a]}
`)
	impact, _ := g.BlastRadius(resolve.Path{"services", "a"})
	var got []string
	for _, p := range impact.Dependencies {
		got = append(got, p.String())
	}
	if want := []string{"services.b", "services.c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dependencies = %v, want %v — the subject is excluded even though the cycle returns to it", got, want)
	}
}
