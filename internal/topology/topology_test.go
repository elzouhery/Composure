package topology

import (
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

// buildSrc resolves inline compose text and derives its topology. Tests use
// real compose text rather than a hand-built model: the parsing of the short
// and long forms is most of what this package does, and a fixture model would
// test the fixture.
func buildSrc(t *testing.T, src string, profiles ...string) *Graph {
	t.Helper()
	p, err := resolve.Bytes("test.yaml", []byte(src))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	g, err := Build(p, profiles)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

// nodeIDs renders the node set as sorted "kind id" strings.
func nodeIDs(g *Graph) []string {
	var out []string
	for _, n := range g.Nodes() {
		out = append(out, string(n.Kind)+" "+n.Path.String())
	}
	return out
}

func edgeStrings(g *Graph) []string {
	var out []string
	for _, e := range g.Edges() {
		out = append(out, string(e.Kind)+" "+e.From.String()+" -> "+e.To.String())
	}
	return out
}

func hasAll(t *testing.T, got, want []string) {
	t.Helper()
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("missing %q\ngot:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
}

// ---------------------------------------------------------------------------
// Story 2.1 — the node set.

func TestNodeKinds(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "every top-level section becomes a node",
			src: `
services:
  web:
    image: nginx
networks:
  front: {}
volumes:
  data: {}
configs:
  conf: {file: ./c}
secrets:
  key: {file: ./k}
`,
			want: []string{
				"service services.web",
				"network networks.front",
				"volume volumes.data",
				"config configs.conf",
				"secret secrets.key",
			},
		},
		{
			name: "a declared resource nothing mounts is still a node",
			src: `
services:
  web: {image: nginx}
volumes:
  orphan:
`,
			want: []string{"volume volumes.orphan"},
		},
		{
			name: "published ports are nodes of their own",
			src: `
services:
  web:
    image: nginx
    ports: ["8080:80", "443:443/udp"]
`,
			want: []string{
				"port services.web.ports[0]",
				"port services.web.ports[1]",
			},
		},
		{
			name: "a network used but never declared is external, not dropped",
			src: `
services:
  web:
    image: nginx
    networks: [ghost]
`,
			want: []string{"network networks.ghost"},
		},
		{
			name: "a volume used but never declared is external",
			src: `
services:
  web:
    image: nginx
    volumes: ["ghostvol:/data"]
`,
			want: []string{"volume volumes.ghostvol"},
		},
		{
			name: "a service depended on but never declared is external",
			src: `
services:
  web:
    image: nginx
    depends_on: [ghostsvc]
`,
			want: []string{"service services.ghostsvc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildSrc(t, tc.src)
			hasAll(t, nodeIDs(g), tc.want)
		})
	}
}

func TestUndeclaredNodesAreExternalAndCarryTheReferenceOrigin(t *testing.T) {
	g := buildSrc(t, `
services:
  web:
    image: nginx
    networks: [ghost]
networks:
  real:
    external: true
    internal: true
`)
	ghost, ok := g.Node(resolve.Path{"networks", "ghost"})
	if !ok {
		t.Fatal("ghost network is not a node")
	}
	if ghost.Declared {
		t.Error("an undeclared network reported Declared true")
	}
	if !ghost.External {
		t.Error("an undeclared network is not marked external")
	}
	if ghost.Origin.Line != 5 {
		t.Errorf("origin should be the reference that created it, got line %d", ghost.Origin.Line)
	}

	real, _ := g.Node(resolve.Path{"networks", "real"})
	if !real.Declared || !real.External || !real.Internal {
		t.Errorf("declared external internal network: got declared=%v external=%v internal=%v",
			real.Declared, real.External, real.Internal)
	}
}

func TestNodeOriginIsTheDeclarationKey(t *testing.T) {
	// `scratch:` with no value: the value node's position is wherever the
	// parser landed, so the node must anchor on the key instead.
	g := buildSrc(t, `
services:
  web: {image: nginx}
volumes:
  scratch:
`)
	n, ok := g.Node(resolve.Path{"volumes", "scratch"})
	if !ok {
		t.Fatal("scratch is not a node")
	}
	if n.Origin.Line != 5 || n.Origin.Column != 3 {
		t.Errorf("origin = %s, want test.yaml:5:3 (the key)", n.Origin)
	}
}

// ---------------------------------------------------------------------------
// Story 2.2 — depends_on.

func TestDependsOn(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		to        string
		condition string
		restart   string
		required  string
	}{
		{
			name:      "short array form implies service_started",
			src:       "services:\n  a: {image: x, depends_on: [b]}\n  b: {image: x}\n",
			to:        "services.b",
			condition: ConditionStarted,
		},
		{
			name:      "long form with no condition also means service_started",
			src:       "services:\n  a:\n    image: x\n    depends_on:\n      b: {}\n  b: {image: x}\n",
			to:        "services.b",
			condition: ConditionStarted,
		},
		{
			name:      "service_healthy",
			src:       "services:\n  a:\n    image: x\n    depends_on:\n      b:\n        condition: service_healthy\n  b: {image: x}\n",
			to:        "services.b",
			condition: ConditionHealthy,
		},
		{
			name:      "service_completed_successfully",
			src:       "services:\n  a:\n    image: x\n    depends_on:\n      b:\n        condition: service_completed_successfully\n  b: {image: x}\n",
			to:        "services.b",
			condition: ConditionCompletedOK,
		},
		{
			name:      "restart and required are carried",
			src:       "services:\n  a:\n    image: x\n    depends_on:\n      b:\n        condition: service_healthy\n        restart: true\n        required: false\n  b: {image: x}\n",
			to:        "services.b",
			condition: ConditionHealthy,
			restart:   "true",
			required:  "false",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildSrc(t, tc.src)
			var found *Edge
			for _, e := range g.Edges() {
				if e.Kind == EdgeDependsOn && e.To.String() == tc.to {
					edge := e
					found = &edge
				}
			}
			if found == nil {
				t.Fatalf("no depends_on edge to %s in %v", tc.to, edgeStrings(g))
			}
			if found.Depends == nil {
				t.Fatal("depends_on edge carries no detail")
			}
			if found.Depends.Condition != tc.condition {
				t.Errorf("condition = %q, want %q", found.Depends.Condition, tc.condition)
			}
			if found.Depends.Restart != tc.restart {
				t.Errorf("restart = %q, want %q", found.Depends.Restart, tc.restart)
			}
			if found.Depends.Required != tc.required {
				t.Errorf("required = %q, want %q", found.Depends.Required, tc.required)
			}
			if found.Origin.IsZero() {
				t.Error("edge carries no origin")
			}
		})
	}
}

func TestDependsOnBothFormsShareOneEdgeType(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    image: x
    depends_on: [c]
  b:
    image: x
    depends_on:
      c:
        condition: service_healthy
  c: {image: x}
`)
	var count int
	for _, e := range g.Edges() {
		if e.Kind == EdgeDependsOn {
			count++
			if e.Depends == nil || e.Depends.Condition == "" {
				t.Error("an edge from one of the two forms has no condition")
			}
		}
	}
	if count != 2 {
		t.Errorf("got %d depends_on edges, want 2: %v", count, edgeStrings(g))
	}
}

// ---------------------------------------------------------------------------
// Story 2.3 — networks, mounts, links, network_mode, ports.

func TestEdgeKinds(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "network attachment, sequence form",
			src:  "services:\n  a: {image: x, networks: [front]}\nnetworks:\n  front: {}\n",
			want: []string{"network services.a -> networks.front"},
		},
		{
			name: "network attachment, mapping form",
			src:  "services:\n  a:\n    image: x\n    networks:\n      front:\n        aliases: [alpha, beta]\nnetworks:\n  front: {}\n",
			want: []string{"network services.a -> networks.front"},
		},
		{
			name: "named volume mount",
			src:  "services:\n  a: {image: x, volumes: [\"data:/var/lib\"]}\nvolumes:\n  data: {}\n",
			want: []string{"volume services.a -> volumes.data"},
		},
		{
			name: "bind mount points back at the service and is its own kind",
			src:  "services:\n  a: {image: x, volumes: [\"./conf:/etc/conf:ro\"]}\n",
			want: []string{"bind services.a -> services.a"},
		},
		{
			name: "long-form mount",
			src:  "services:\n  a:\n    image: x\n    volumes:\n      - type: volume\n        source: data\n        target: /var/lib\n        read_only: true\nvolumes:\n  data: {}\n",
			want: []string{"volume services.a -> volumes.data"},
		},
		{
			name: "links are not folded into network attachment",
			src:  "services:\n  a: {image: x, links: [\"b:alias\"]}\n  b: {image: x}\n",
			want: []string{"link services.a -> services.b"},
		},
		{
			name: "network_mode service is its own kind",
			src:  "services:\n  a: {image: x, network_mode: \"service:b\"}\n  b: {image: x}\n",
			want: []string{"network_mode services.a -> services.b"},
		},
		{
			name: "config mount",
			src:  "services:\n  a: {image: x, configs: [conf]}\nconfigs:\n  conf: {file: ./c}\n",
			want: []string{"config services.a -> configs.conf"},
		},
		{
			name: "secret mount, long form",
			src:  "services:\n  a:\n    image: x\n    secrets:\n      - source: key\n        target: /run/secrets/key\nsecrets:\n  key: {file: ./k}\n",
			want: []string{"secret services.a -> secrets.key"},
		},
		{
			name: "published port attaches to its service",
			src:  "services:\n  a: {image: x, ports: [\"8080:80\"]}\n",
			want: []string{"publish services.a -> services.a.ports[0]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildSrc(t, tc.src)
			hasAll(t, edgeStrings(g), tc.want)
		})
	}
}

func TestNetworkAttachmentCarriesAliases(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    image: x
    networks:
      front:
        aliases: [alpha, beta]
      back: {}
networks:
  front: {internal: true}
  back: {}
`)
	got := map[string][]string{}
	for _, e := range g.Edges() {
		if e.Kind == EdgeNetwork {
			if e.Attach == nil {
				t.Fatalf("network edge to %s carries no attach detail", e.To)
			}
			got[e.To.String()] = e.Attach.Aliases
		}
	}
	if want := []string{"alpha", "beta"}; strings.Join(got["networks.front"], ",") != strings.Join(want, ",") {
		t.Errorf("front aliases = %v, want %v", got["networks.front"], want)
	}
	if aliases, ok := got["networks.back"]; !ok || aliases == nil || len(aliases) != 0 {
		t.Errorf("back aliases = %v, want a present empty list", aliases)
	}
	front, _ := g.Node(resolve.Path{"networks", "front"})
	if !front.Internal {
		t.Error("internal: true was not flagged on the network node")
	}
}

func TestMountDetail(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		kind     EdgeKind
		source   string
		target   string
		mode     string
		hostPath string
	}{
		{
			name:   "named volume defaults to rw",
			src:    "services:\n  a: {image: x, volumes: [\"data:/var/lib\"]}\n",
			kind:   EdgeVolume,
			source: "data", target: "/var/lib", mode: "rw",
		},
		{
			name:   "named volume ro",
			src:    "services:\n  a: {image: x, volumes: [\"data:/var/lib:ro\"]}\n",
			kind:   EdgeVolume,
			source: "data", target: "/var/lib", mode: "ro",
		},
		{
			name:   "bind carries the host path",
			src:    "services:\n  a: {image: x, volumes: [\"./conf:/etc/conf:ro\"]}\n",
			kind:   EdgeBind,
			source: "./conf", target: "/etc/conf", mode: "ro", hostPath: "./conf",
		},
		{
			name:   "absolute bind",
			src:    "services:\n  a: {image: x, volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]}\n",
			kind:   EdgeBind,
			source: "/var/run/docker.sock", target: "/var/run/docker.sock", mode: "rw",
			hostPath: "/var/run/docker.sock",
		},
		{
			name:   "long form read_only",
			src:    "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: ./src\n        target: /app\n        read_only: true\n",
			kind:   EdgeBind,
			source: "./src", target: "/app", mode: "ro", hostPath: "./src",
		},
		{
			name:   "config mounts are read-only by definition",
			src:    "services:\n  a:\n    image: x\n    configs:\n      - source: conf\n        target: /etc/conf\n",
			kind:   EdgeConfig,
			source: "conf", target: "/etc/conf", mode: "ro",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildSrc(t, tc.src)
			var found *MountDetail
			for _, e := range g.Edges() {
				if e.Kind == tc.kind && e.Mount != nil {
					found = e.Mount
				}
			}
			if found == nil {
				t.Fatalf("no %s edge with a mount: %v", tc.kind, edgeStrings(g))
			}
			if found.Source != tc.source || found.Target != tc.target || found.Mode != tc.mode {
				t.Errorf("mount = %+v, want source=%q target=%q mode=%q", *found, tc.source, tc.target, tc.mode)
			}
			if found.HostPath != tc.hostPath {
				t.Errorf("host path = %q, want %q", found.HostPath, tc.hostPath)
			}
			if found.ReadOnly() != (tc.mode == "ro") {
				t.Errorf("ReadOnly() = %v for mode %q", found.ReadOnly(), tc.mode)
			}
		})
	}
}

func TestLinkAlias(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, links: [\"b:db\", c]}\n  b: {image: x}\n  c: {image: x}\n")
	got := map[string][]string{}
	for _, e := range g.Edges() {
		if e.Kind == EdgeLink {
			got[e.To.String()] = e.Attach.Aliases
		}
	}
	if len(got["services.b"]) != 1 || got["services.b"][0] != "db" {
		t.Errorf("link alias = %v, want [db]", got["services.b"])
	}
	if len(got["services.c"]) != 0 {
		t.Errorf("unaliased link = %v, want empty", got["services.c"])
	}
}

func TestNetworkModeContainerIsReportedNotInvented(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, network_mode: \"container:outside\"}\n")
	if g.Has(resolve.Path{"services", "outside"}) {
		t.Error("a container: reference invented a node")
	}
	d := g.Dangling()
	if len(d) != 1 || d[0].Kind != EdgeNetworkMode {
		t.Fatalf("dangling = %+v, want one network_mode entry", d)
	}
}

func TestNetworkModeHostProducesNoEdge(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, network_mode: host}\n")
	for _, e := range g.Edges() {
		if e.Kind == EdgeNetworkMode {
			t.Errorf("network_mode: host produced an edge: %v", e)
		}
	}
	if len(g.Dangling()) != 0 {
		t.Errorf("network_mode: host reported a dangling reference: %+v", g.Dangling())
	}
}

func TestPortDetail(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		hostIP    string
		published string
		target    string
		protocol  string
	}{
		{name: "container port only", raw: "80", target: "80", protocol: "tcp"},
		{name: "host and container", raw: "8080:80", published: "8080", target: "80", protocol: "tcp"},
		{name: "protocol", raw: "53:53/udp", published: "53", target: "53", protocol: "udp"},
		{name: "host ip", raw: "127.0.0.1:8080:80", hostIP: "127.0.0.1", published: "8080", target: "80", protocol: "tcp"},
		{name: "ipv6 host ip", raw: "[::1]:8080:80", hostIP: "::1", published: "8080", target: "80", protocol: "tcp"},
		{name: "range", raw: "3000-3005:3000-3005", published: "3000-3005", target: "3000-3005", protocol: "tcp"},
		{name: "host ip with protocol", raw: "0.0.0.0:5000:5000/udp", hostIP: "0.0.0.0", published: "5000", target: "5000", protocol: "udp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseShortPort(tc.raw)
			if got.HostIP != tc.hostIP || got.Published != tc.published ||
				got.Target != tc.target || got.Protocol != tc.protocol {
				t.Errorf("parseShortPort(%q) = %+v", tc.raw, got)
			}
			if got.Raw != tc.raw {
				t.Errorf("raw = %q, want %q", got.Raw, tc.raw)
			}
		})
	}
}

func TestLongPortForm(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    image: x
    ports:
      - target: 80
        published: "8080"
        protocol: udp
        host_ip: 127.0.0.1
`)
	n, ok := g.Node(resolve.Path{"services", "a", "ports", "0"})
	if !ok {
		t.Fatal("no port node")
	}
	if n.Port.Target != "80" || n.Port.Published != "8080" ||
		n.Port.Protocol != "udp" || n.Port.HostIP != "127.0.0.1" {
		t.Errorf("port = %+v", *n.Port)
	}
	if n.Origin.IsZero() {
		t.Error("port node carries no origin")
	}
}

func TestSplitMount(t *testing.T) {
	cases := []struct{ raw, source, target, options string }{
		{"/data", "", "/data", ""},
		{"data:/var/lib", "data", "/var/lib", ""},
		{"data:/var/lib:ro", "data", "/var/lib", "ro"},
		{"./x:/y:ro,z", "./x", "/y", "ro,z"},
		{`C:\data:/data:ro`, `C:\data`, "/data", "ro"},
		{"C:/data:/data", "C:/data", "/data", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			source, target, options := splitMount(tc.raw)
			if source != tc.source || target != tc.target || options != tc.options {
				t.Errorf("splitMount(%q) = %q, %q, %q", tc.raw, source, target, options)
			}
		})
	}
}

func TestIsHostPath(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"data", false},
		{"my_volume-1", false},
		{"./x", true},
		{"/x", true},
		{"~/x", true},
		{"${DIR}/x", true},
		{`C:\x`, true},
		{"", false},
	} {
		if got := isHostPath(tc.in); got != tc.want {
			t.Errorf("isHostPath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestAnonymousVolumeIsReportedNotInvented(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, volumes: [\"/var/lib/data\"]}\n")
	for _, e := range g.Edges() {
		if e.Kind == EdgeVolume {
			t.Errorf("an anonymous volume produced a volume edge: %v", e)
		}
	}
	if len(g.Dangling()) != 1 {
		t.Errorf("dangling = %+v, want the anonymous mount reported", g.Dangling())
	}
}

// ---------------------------------------------------------------------------
// Story 2.4 — layering and blast radius.

func TestLayeringOrdersDependenciesBelowDependents(t *testing.T) {
	g := buildSrc(t, `
services:
  gateway:
    image: x
    depends_on: [web]
  web:
    image: x
    depends_on: [api]
  api:
    image: x
    depends_on: [db, cache]
  db: {image: x}
  cache: {image: x}
`)
	layer := map[string]int{}
	for _, n := range g.Nodes() {
		layer[n.Path.String()] = n.Layer
	}
	want := map[string]int{
		"services.db": 0, "services.cache": 0,
		"services.api": 1, "services.web": 2, "services.gateway": 3,
	}
	for id, l := range want {
		if layer[id] != l {
			t.Errorf("layer[%s] = %d, want %d", id, layer[id], l)
		}
	}

	// The invariant, stated directly rather than through the table: every
	// depends_on edge must go strictly downward.
	for _, e := range g.Edges() {
		if e.Kind != EdgeDependsOn {
			continue
		}
		if layer[e.From.String()] <= layer[e.To.String()] {
			t.Errorf("%s (L%d) depends on %s (L%d): not strictly ordered",
				e.From, layer[e.From.String()], e.To, layer[e.To.String()])
		}
	}
}

// TestLayeringSucceedsOnACycle is the constraint story 3.4 depends on: if
// layering failed here, diagnostics could never run on the stack that most
// needs them.
func TestLayeringSucceedsOnACycle(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [c]
  c:
    image: x
    depends_on: [a]
  outside:
    image: x
    depends_on: [a]
`)
	cycles := g.Cycles()
	if len(cycles) != 1 {
		t.Fatalf("cycles = %v, want exactly one", cycles)
	}
	var members []string
	for _, p := range cycles[0] {
		members = append(members, p.String())
	}
	if strings.Join(members, ",") != "services.a,services.b,services.c" {
		t.Errorf("cycle members = %v", members)
	}

	// Layering succeeded: the cycle's members share a layer, and a node
	// outside it still sits above.
	layer := map[string]int{}
	for _, n := range g.Nodes() {
		layer[n.Path.String()] = n.Layer
	}
	if layer["services.a"] != layer["services.b"] || layer["services.b"] != layer["services.c"] {
		t.Errorf("cycle members were not grouped: %v", layer)
	}
	if layer["services.outside"] <= layer["services.a"] {
		t.Errorf("a node outside the cycle was not layered above it: %v", layer)
	}
}

func TestSelfDependencyIsACycle(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, depends_on: [a]}\n")
	cycles := g.Cycles()
	if len(cycles) != 1 || len(cycles[0]) != 1 || cycles[0][0].String() != "services.a" {
		t.Errorf("cycles = %v, want [[services.a]]", cycles)
	}
}

func TestBlastRadius(t *testing.T) {
	g := buildSrc(t, `
services:
  gateway: {image: x, depends_on: [web]}
  web: {image: x, depends_on: [api]}
  worker: {image: x, depends_on: [api]}
  api: {image: x, depends_on: [db]}
  db: {image: x}
  lonely: {image: x}
`)
	impact, ok := g.BlastRadius(resolve.Path{"services", "api"})
	if !ok {
		t.Fatal("api is not a node")
	}
	var dependents, dependencies []string
	for _, p := range impact.Dependents {
		dependents = append(dependents, p.String())
	}
	for _, p := range impact.Dependencies {
		dependencies = append(dependencies, p.String())
	}
	if strings.Join(dependents, ",") != "services.gateway,services.web,services.worker" {
		t.Errorf("dependents = %v", dependents)
	}
	if strings.Join(dependencies, ",") != "services.db" {
		t.Errorf("dependencies = %v", dependencies)
	}

	if _, ok := g.BlastRadius(resolve.Path{"services", "nope"}); ok {
		t.Error("blast radius of a node that does not exist reported ok")
	}

	lonely, _ := g.BlastRadius(resolve.Path{"services", "lonely"})
	if len(lonely.Dependents) != 0 || len(lonely.Dependencies) != 0 {
		t.Errorf("isolated node has a blast radius: %+v", lonely)
	}
}

func TestBlastRadiusSurvivesACycle(t *testing.T) {
	g := buildSrc(t, `
services:
  a: {image: x, depends_on: [b]}
  b: {image: x, depends_on: [a]}
`)
	impact, ok := g.BlastRadius(resolve.Path{"services", "a"})
	if !ok {
		t.Fatal("a is not a node")
	}
	if len(impact.Dependents) != 1 || impact.Dependents[0].String() != "services.b" {
		t.Errorf("dependents = %v", impact.Dependents)
	}
}

// ---------------------------------------------------------------------------
// Story 2.5 — profiles.

func TestProfileFiltering(t *testing.T) {
	const src = `
services:
  web:
    image: x
    depends_on: [tools]
  tools:
    image: x
    profiles: [debug]
  prodonly:
    image: x
    profiles: [prod, staging]
`
	cases := []struct {
		name     string
		profiles []string
		want     []string
		absent   []string
	}{
		{
			name:   "no profile flag means always-active services only",
			want:   []string{"service services.web"},
			absent: []string{"service services.tools", "service services.prodonly"},
		},
		{
			name:     "one profile",
			profiles: []string{"debug"},
			want:     []string{"service services.web", "service services.tools"},
			absent:   []string{"service services.prodonly"},
		},
		{
			name:     "membership is any-of",
			profiles: []string{"staging"},
			want:     []string{"service services.prodonly"},
			absent:   []string{"service services.tools"},
		},
		{
			name:     "several profiles",
			profiles: []string{"debug", "prod"},
			want:     []string{"service services.web", "service services.tools", "service services.prodonly"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildSrc(t, src, tc.profiles...)
			ids := nodeIDs(g)
			hasAll(t, ids, tc.want)
			set := map[string]bool{}
			for _, id := range ids {
				set[id] = true
			}
			for _, a := range tc.absent {
				if set[a] {
					t.Errorf("%q should have been filtered out", a)
				}
			}
		})
	}
}

// DECISIONS.md 19, in the direction it is recorded, and story 2.5's amended
// criterion: profiles filter SERVICES and never the resource inventory.
//
// The decision recorded that nothing tested this in either direction — every
// other profile fixture in this file declares no top-level `networks:` or
// `volumes:` at all, so a future change that started filtering resources, or
// one that stopped, would fail nothing. This is the fixture that combines them.
//
// `debugnet` and `debugvol` are named ONLY by a `profiles: [debug]` service, so
// with no profile active there is nothing left attached to them. Both must still
// be nodes, and there must be no edge — the resources, and nothing on them.
func TestProfilesDoNotFilterTheResourceInventory(t *testing.T) {
	const src = `
services:
  tools:
    image: x
    profiles: [debug]
    networks: [debugnet]
    volumes:
      - debugvol:/data
networks:
  debugnet: {}
volumes:
  debugvol: {}
`
	want := []string{"network networks.debugnet", "volume volumes.debugvol"}

	bare := buildSrc(t, src)
	ids := nodeIDs(bare)
	hasAll(t, ids, want)
	// The premise, asserted rather than assumed: the only service IS filtered
	// out. Without that this fixture cannot distinguish "resources survive
	// filtering" from "the service that uses them is still here".
	for _, id := range ids {
		if id == "service services.tools" {
			t.Fatal("the profiled service was not filtered out; this asserts nothing")
		}
	}
	if got := edgeStrings(bare); len(got) != 0 {
		t.Errorf("edges = %v, want none — every end of every edge was filtered out", got)
	}

	// And with the profile active the same resources are nodes, now with the
	// edges attached. The inventory is identical either way; only the service
	// population and the edges move.
	debug := buildSrc(t, src, "debug")
	hasAll(t, nodeIDs(debug), append(append([]string{}, want...), "service services.tools"))
	if len(edgeStrings(debug)) == 0 {
		t.Error("no edge with the profile active; the fixture no longer shows the contrast")
	}
}

func TestFilteredEdgeIsDroppedAndReported(t *testing.T) {
	g := buildSrc(t, `
services:
  web:
    image: x
    depends_on: [tools]
  tools:
    image: x
    profiles: [debug]
`)
	for _, e := range g.Edges() {
		if e.Kind == EdgeDependsOn {
			t.Errorf("an edge survived pointing at a filtered node: %v", e)
		}
	}
	if g.Has(resolve.Path{"services", "tools"}) {
		t.Error("the filtered service is still a node")
	}
	d := g.Dangling()
	if len(d) != 1 {
		t.Fatalf("dangling = %+v, want one entry", d)
	}
	if d[0].Ref != "tools" || d[0].Reason != danglingReasonProfile || d[0].From.String() != "services.web" {
		t.Errorf("dangling entry = %+v", d[0])
	}
	if d[0].Origin.IsZero() {
		t.Error("a dangling report with no position cannot be acted on")
	}
}

// TestTwoProfileSetsFromOneResolvedModel is AD-16 stated as a test: toggling a
// profile recomputes the graph and never re-reads a file.
func TestTwoProfileSetsFromOneResolvedModel(t *testing.T) {
	p, err := resolve.Bytes("test.yaml", []byte(`
services:
  web: {image: x}
  tools: {image: x, profiles: [debug]}
`))
	if err != nil {
		t.Fatal(err)
	}
	bare, err := Build(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	debug, err := Build(p, []string{"debug"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Nodes()) != 1 {
		t.Errorf("default graph has %d nodes, want 1", len(bare.Nodes()))
	}
	if len(debug.Nodes()) != 2 {
		t.Errorf("debug graph has %d nodes, want 2", len(debug.Nodes()))
	}
	// The first graph must not have been mutated by the second build.
	if len(bare.Nodes()) != 1 {
		t.Error("building a second graph changed the first")
	}
}

func TestProfilesAreNormalised(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x}\n", "b", "a", "b", " ", "a")
	got := g.Profiles()
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("profiles = %v, want [a b] sorted and deduplicated", got)
	}
}

// ---------------------------------------------------------------------------
// Contract.

func TestBuildRefusesNilProject(t *testing.T) {
	if _, err := Build(nil, nil); err != ErrNoProject {
		t.Errorf("Build(nil) error = %v, want ErrNoProject", err)
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	g := buildSrc(t, "services:\n  a: {image: x, networks: [n]}\nnetworks:\n  n: {}\n")
	nodes := g.Nodes()
	nodes[0].Name = "vandalised"
	nodes[0].Path[0] = "wrecked"
	if again := g.Nodes(); again[0].Name == "vandalised" || again[0].Path[0] == "wrecked" {
		t.Error("Nodes() handed out a view into the graph")
	}
	edges := g.Edges()
	if len(edges) > 0 && edges[0].Attach != nil {
		edges[0].Attach.Aliases = append(edges[0].Attach.Aliases, "x")
		edges[0].From[0] = "wrecked"
		if again := g.Edges(); again[0].From[0] == "wrecked" {
			t.Error("Edges() handed out a view into the graph")
		}
	}
}

func TestSurvivesShapesThatAreNotComposeShaped(t *testing.T) {
	// Every one of these is a key holding the wrong kind. None may panic and
	// none may invent an edge.
	cases := []string{
		"services:\n  a: {image: x, depends_on: \"b\"}\n",
		"services:\n  a: {image: x, networks: \"front\"}\n",
		"services:\n  a: {image: x, volumes: \"data:/x\"}\n",
		"services:\n  a: {image: x, ports: 8080}\n",
		"services:\n  a: {image: x, links: {b: c}}\n",
		"services:\n  a: {image: x, network_mode: {}}\n",
		"services:\n  a: {image: x, depends_on: [null]}\n",
		"services:\n  a: {image: x, volumes: [null]}\n",
		"services:\n  a:\n",
		"services:\n  a: {image: x, configs: [{}]}\n",
		"services:\n  a: {image: x, profiles: prod}\n",
		"services:\n  a: {image: x, build: []}\n",
		"services:\n  a: {image: x, build: {}}\n",
		"services:\n  a: {image: x, build: {context: null}}\n",
	}
	for _, src := range cases {
		t.Run(strings.TrimSpace(src), func(t *testing.T) {
			p, err := resolve.Bytes("test.yaml", []byte(src))
			if err != nil {
				t.Skipf("resolve refused it, which is also a valid answer: %v", err)
			}
			g, err := Build(p, nil)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if g == nil {
				t.Fatal("nil graph with no error")
			}
			_ = g.Nodes()
			_ = g.Edges()
		})
	}
}

// ---------------------------------------------------------------------------
// Story 4.2 — the Dockerfile node.
//
// A service that builds is not a service that pulls. The graph has to say
// which, because it is the boundary between the two grammars and — from story
// 6.3 — between two editors.

func TestDockerfileNodeFromBothBuildForms(t *testing.T) {
	g := buildSrc(t, `
services:
  short:
    build: ./docs
  long:
    build:
      context: ./api
      dockerfile: Dockerfile.dev
      target: runtime
  bare:
    build:
      dockerfile: build/Dockerfile
  pulled:
    image: nginx
`)
	hasAll(t, nodeIDs(g), []string{
		"dockerfile services.short.build",
		"dockerfile services.long.build",
		"dockerfile services.bare.build",
	})
	hasAll(t, edgeStrings(g), []string{
		"build services.short -> services.short.build",
		"build services.long -> services.long.build",
	})
	for _, n := range g.Nodes() {
		if n.Path.String() == "services.pulled.build" {
			t.Error("a service with no build section got a Dockerfile node")
		}
	}

	want := map[string]BuildDetail{
		"services.short.build": {Context: "./docs", Reference: "./docs/Dockerfile"},
		"services.long.build": {
			Context: "./api", Dockerfile: "Dockerfile.dev", Target: "runtime",
			Reference: "./api/Dockerfile.dev",
		},
		// No context: the dockerfile is relative to the compose file's own
		// directory, which is what "" means here. Inventing "." would be a
		// path the file does not contain.
		"services.bare.build": {Dockerfile: "build/Dockerfile", Reference: "build/Dockerfile"},
	}
	for path, expect := range want {
		n, ok := g.Node(resolve.ParsePath(path))
		if !ok {
			t.Fatalf("no node at %s", path)
		}
		if n.Build == nil {
			t.Fatalf("%s carries no build detail", path)
		}
		if *n.Build != expect {
			t.Errorf("%s build detail:\n got %+v\nwant %+v", path, *n.Build, expect)
		}
		if n.Name != expect.Reference {
			t.Errorf("%s label is %q, want the reference %q", path, n.Name, expect.Reference)
		}
	}
}

// An inline Dockerfile has no second file. Naming one would be a confident
// wrong answer — the exact failure mode this engine has.
func TestInlineDockerfileNamesNoFile(t *testing.T) {
	g := buildSrc(t, `
services:
  a:
    build:
      context: .
      dockerfile_inline: |
        FROM alpine
`)
	n, ok := g.Node(resolve.ParsePath("services.a.build"))
	if !ok {
		t.Fatal("no Dockerfile node for an inline build")
	}
	if n.Build == nil || !n.Build.Inline {
		t.Fatalf("inline build not reported: %+v", n.Build)
	}
	if n.Build.Reference != "" {
		t.Errorf("an inline build reported the filename %q", n.Build.Reference)
	}
	if n.Name != "dockerfile_inline" {
		t.Errorf("label is %q", n.Name)
	}
}

// The node's Origin is the `build` key, not the first line of the mapping
// under it: that is where a reader looks and where 6.3 puts a cursor.
func TestDockerfileOriginIsTheBuildKey(t *testing.T) {
	g := buildSrc(t, "services:\n  a:\n    image: x\n    build:\n      context: ./api\n")
	n, _ := g.Node(resolve.ParsePath("services.a.build"))
	if n.Origin.Line != 4 {
		t.Errorf("origin line is %d, want the `build:` key at 4 (%s)", n.Origin.Line, n.Origin)
	}
}

func TestBuildReference(t *testing.T) {
	cases := []struct{ context, dockerfile, want string }{
		{"", "", ""},
		{".", "", "Dockerfile"},
		{"./docs", "", "./docs/Dockerfile"},
		{"docs/", "Dockerfile.prod", "docs/Dockerfile.prod"},
		{"", "build/Dockerfile", "build/Dockerfile"},
		{"./api", "/abs/Dockerfile", "/abs/Dockerfile"},
		// A variable is not expanded and not mangled into a path.
		{"${CONTEXT}", "", "${CONTEXT}/Dockerfile"},
		{"https://github.com/x/y.git", "Dockerfile", "https://github.com/x/y.git Dockerfile"},
	}
	for _, c := range cases {
		if got := buildReference(c.context, c.dockerfile); got != c.want {
			t.Errorf("buildReference(%q, %q) = %q, want %q", c.context, c.dockerfile, got, c.want)
		}
	}
}

// A service filtered out by profile contributes no Dockerfile node either —
// the whole service is gone, and half of it staying would be a node with no
// service to hang from.
func TestDockerfileFollowsTheProfileFilter(t *testing.T) {
	src := "services:\n  a:\n    build: ./a\n    profiles: [dev]\n"
	if got := len(buildSrc(t, src).Nodes()); got != 0 {
		t.Errorf("inactive profile left %d nodes", got)
	}
	if got := len(buildSrc(t, src, "dev").Nodes()); got != 2 {
		t.Errorf("active profile produced %d nodes, want the service and its Dockerfile", got)
	}
}

// The image is carried on the service node so the canvas can show a value
// rather than a bare key. It is what was written, and empty when nothing was.
func TestServiceCarriesItsImageAsWritten(t *testing.T) {
	g := buildSrc(t, "services:\n  a:\n    image: ghcr.io/x/y:1.2.3\n  b:\n    build: ./b\n")
	a, _ := g.Node(resolve.ParsePath("services.a"))
	if a.Image != "ghcr.io/x/y:1.2.3" {
		t.Errorf("image is %q", a.Image)
	}
	b, _ := g.Node(resolve.ParsePath("services.b"))
	if b.Image != "" {
		t.Errorf("a service with no image reported %q", b.Image)
	}
}
