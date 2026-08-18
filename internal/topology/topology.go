// Package topology turns a resolved compose project into the graph of what
// talks to what: the nodes a stack declares and the real edges between them.
//
// The graph is a pure derivation of the resolved model (AD-6). It is
// recomputed, never stored, and it holds no identity that is not already in
// the file — a node's identity is its resolve.Path, never a generated id.
// There are no exported mutators on any type here; Build is the only way a
// Graph comes into existence, and every accessor hands back a copy.
//
// The dependency direction is one-way: topology imports resolve and nothing
// else from the core. resolve stays a leaf. diagnose will import topology
// (AD-17) rather than re-deriving any relation modelled here — if a rule needs
// an edge, the edge is defined in this package or it does not exist.
//
// Profile filtering lives here, not in resolve (AD-16). Build takes the active
// profile set as an explicit argument, so toggling a profile recomputes the
// graph from the one resolved model and never re-reads a file.
package topology

import (
	"errors"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// ErrNoProject is returned by Build when handed a nil project. A graph of
// nothing is a confident wrong answer — the caller resolved nothing and would
// be told the stack is empty — so it is refused instead.
var ErrNoProject = errors.New("topology: no resolved project")

// NodeKind is what a node is. The set is closed: it mirrors the top-level
// sections Compose declares, plus published ports, which need an identity of
// their own so R3.1 can collide-check them and point at the line that declared
// each side.
type NodeKind string

const (
	NodeService NodeKind = "service"
	NodeNetwork NodeKind = "network"
	NodeVolume  NodeKind = "volume"
	NodeConfig  NodeKind = "config"
	NodeSecret  NodeKind = "secret"
	// NodePort is one entry of a service's `ports:` list. It is a node rather
	// than a field on the service because a service publishes many ports, each
	// written at its own position, and a collision finding has to name the two
	// positions rather than the two services.
	NodePort NodeKind = "port"
	// NodeDockerfile is the file a service's `build:` section names. It is a
	// node rather than a field on the service because it is the boundary
	// between the two grammars: a different file, a different engine
	// (internal/dockerfile) and — from story 6.3 — a different editor behind
	// it. A service that builds is not a service that pulls, and the graph has
	// to be able to say which it is looking at.
	//
	// Its identity is the `build` key's own path (`services.docs.build`), not
	// the filename: two services can build the same Dockerfile with different
	// contexts and targets, and folding them into one node would lose which
	// service the reader clicked.
	NodeDockerfile NodeKind = "dockerfile"
)

// kindRank orders the node kinds for deterministic output. The order is the
// order a reader wants them in, not alphabetical.
func (k NodeKind) rank() int {
	switch k {
	case NodeService:
		return 0
	case NodeNetwork:
		return 1
	case NodeVolume:
		return 2
	case NodeConfig:
		return 3
	case NodeSecret:
		return 4
	case NodePort:
		return 5
	case NodeDockerfile:
		return 6
	}
	return 7
}

// EdgeKind is what a relation is. Each declaration form gets its own kind
// rather than being folded into a neighbour: `links`, `network_mode:
// service:x` and a network attachment all put two services on the same
// network in practice, but they fail differently and a diagnostic has to be
// able to tell them apart.
type EdgeKind string

const (
	// EdgeDependsOn is `depends_on`, in either the short or the long form.
	EdgeDependsOn EdgeKind = "depends_on"
	// EdgeNetwork is an attachment declared under a service's `networks:`.
	EdgeNetwork EdgeKind = "network"
	// EdgeVolume is a named-volume mount.
	EdgeVolume EdgeKind = "volume"
	// EdgeBind is a host bind mount. Its target is the service's own mount
	// point, so it has no node on the far side and points back at the service.
	EdgeBind EdgeKind = "bind"
	// EdgeConfig is a `configs:` mount on a service.
	EdgeConfig EdgeKind = "config"
	// EdgeSecret is a `secrets:` mount on a service.
	EdgeSecret EdgeKind = "secret"
	// EdgeLink is a legacy `links:` entry.
	EdgeLink EdgeKind = "link"
	// EdgeNetworkMode is `network_mode: service:x` — the service shares
	// another service's network namespace outright.
	EdgeNetworkMode EdgeKind = "network_mode"
	// EdgePublish attaches a published port to the service that publishes it.
	EdgePublish EdgeKind = "publish"
	// EdgeBuild attaches a Dockerfile to the service whose `build:` section
	// names it.
	EdgeBuild EdgeKind = "build"
)

// Conditions a depends_on edge can carry. ConditionStarted is what the short
// array form means, and what the long form means when `condition:` is absent.
const (
	ConditionStarted            = "service_started"
	ConditionHealthy            = "service_healthy"
	ConditionCompletedOK        = "service_completed_successfully"
	defaultDependsOnCondition   = ConditionStarted
	modeReadOnly                = "ro"
	modeReadWrite               = "rw"
	defaultProtocol             = "tcp"
	networkModeServicePrefix    = "service:"
	networkModeContainerPrefix  = "container:"
	danglingReasonProfile       = "filtered by profile"
	danglingReasonNotAService   = "network_mode target is not a service"
	danglingReasonAnonymousBind = "mount has no source"
)

// Node is one thing the stack declares — or one thing it uses without
// declaring, which is the same node with Declared false.
//
// Path is the identity. Nothing here is generated: two builds of the same
// model produce the same Path for the same node, which is what makes the graph
// joinable against findings and byte ranges (AD-14).
type Node struct {
	// Path addresses the declaration in the resolved model. It is the node's
	// identity and the join key with every other package.
	Path resolve.Path
	Kind NodeKind
	// Name is the last meaningful segment — the service or resource name as
	// written. Display only; never an identity.
	Name string
	// Origin is where the declaration that created this node was written. For
	// a declared resource that is the key; for an undeclared one it is the
	// first reference to it, which is the only position the file offers.
	Origin resolve.Origin
	// Declared reports whether the project has a top-level entry for this
	// node. A false here is the "used but never declared" case: the node is
	// present rather than dropped, so a reference never dangles into nothing.
	Declared bool
	// External is true when the node is outside this project's lifecycle:
	// either declared `external: true`, or used without being declared at all.
	External bool
	// Internal is `internal: true` on a network — no outbound connectivity.
	Internal bool
	// Profiles is a service's declared `profiles:` membership, in source
	// order. Empty means always active.
	Profiles []string
	// Layer is the dependency layer assigned by Build: strictly greater than
	// the layer of everything the node depends on, except within a cycle,
	// whose members share one layer. Nodes with no depends_on edges are 0.
	Layer int
	// Image is `image:` on a service, exactly as written, and empty for every
	// other kind and for a service that only builds. It is display detail, not
	// a relation — the graph carries it because a node that names a service
	// without naming what it runs is a key with no value, which is the
	// incumbent's failure this product exists to correct.
	Image string
	// Port carries the parsed detail of a NodePort node, nil for every other
	// kind.
	Port *PortDetail
	// Build carries the parsed detail of a NodeDockerfile node, nil for every
	// other kind.
	Build *BuildDetail
}

// BuildDetail is a service's `build:` section, in either the string shorthand
// or the mapping form.
//
// Nothing here is resolved against a working directory. Compose says the
// dockerfile is relative to the context and the context is relative to the
// file that declared it, and inventing an absolute path out of that would be a
// path the file does not contain — the caller has the Origin and can do it
// with a real base.
type BuildDetail struct {
	// Context is `context:`, or the whole value in the string shorthand.
	Context string
	// Dockerfile is `dockerfile:` as written, empty when the file does not say
	// — which means `Dockerfile`, and Reference spells that out.
	Dockerfile string
	// Target is `target:`, the stage the build stops at.
	Target string
	// Reference is Context and Dockerfile joined the way Compose reads them,
	// for display: `docs/Dockerfile`. Empty when the build is inline.
	Reference string
	// Inline is `dockerfile_inline:` — the Dockerfile is written in the
	// compose file itself and there is no second file. Reporting a filename
	// for one of these would be a confident wrong answer.
	Inline bool
}

// PortDetail is a published port, parsed from either the short string form or
// the long mapping form.
type PortDetail struct {
	// HostIP is the bind address when one was written ("127.0.0.1", "::1").
	HostIP string
	// Published is the host side exactly as written, so a range ("3000-3005")
	// and a single port are both expressible and neither is coerced. Empty
	// when the port is exposed without being published.
	Published string
	// Target is the container side as written.
	Target string
	// Protocol defaults to tcp when the file does not say.
	Protocol string
	// Raw is the entry as written, for display.
	Raw string
}

// DependsDetail is the payload of a depends_on edge.
type DependsDetail struct {
	// Condition is always populated: the short array form means
	// service_started, and so does a long form with no `condition:`.
	Condition string
	// Restart and Required are carried exactly as written, and empty when the
	// key is absent — "absent" and "false" are different statements about a
	// stack and the difference is the user's to see.
	Restart  string
	Required string
}

// MountDetail is the payload of a volume, bind, config or secret edge.
type MountDetail struct {
	// Source is the named volume, config or secret name, or for a bind the
	// host path exactly as written — unresolved, because resolving it against
	// a working directory would invent a path the file does not contain.
	Source string
	// Target is the path inside the container.
	Target string
	// Mode is "ro" or "rw". Compose's default is rw, so an absent option
	// reports rw rather than empty.
	Mode string
	// HostPath is set for binds only, and equals Source. It exists as its own
	// field so a consumer can ask "is there a host path" without first
	// deciding what kind of mount it is looking at.
	HostPath string
}

// ReadOnly reports whether the mount is ro.
func (m MountDetail) ReadOnly() bool { return m.Mode == modeReadOnly }

// AttachDetail is the payload of a network attachment or a link: the names the
// service answers to on the far side.
type AttachDetail struct {
	Aliases []string
}

// Edge is one declared relation. From and To are node Paths, so an edge joins
// against the resolved model and the splice engine without translation.
type Edge struct {
	Kind   EdgeKind
	From   resolve.Path
	To     resolve.Path
	Origin resolve.Origin

	// Exactly one of these is non-nil for a kind that carries detail, and all
	// are nil for a kind that does not.
	Depends *DependsDetail
	Attach  *AttachDetail
	Mount   *MountDetail
	Port    *PortDetail
	Build   *BuildDetail
}

// Dangling is a reference that named something the filtered graph does not
// contain. The edge is dropped rather than left pointing at a node that is not
// there, and the drop is reported here so a caller can say why the picture
// changed when a profile was toggled.
type Dangling struct {
	Kind EdgeKind
	From resolve.Path
	// To is the path the reference would have addressed, had the node been
	// present. It is a path into the resolved model, not into this graph.
	To resolve.Path
	// Ref is the name as written.
	Ref    string
	Reason string
	Origin resolve.Origin
}

// Impact is the answer to a blast-radius question, over depends_on edges.
//
// Dependents is what breaks if the subject does: the transitive set of nodes
// that wait on it. Dependencies is what it waits on. Both exclude the subject
// and are sorted.
type Impact struct {
	Subject      resolve.Path
	Dependents   []resolve.Path
	Dependencies []resolve.Path
}

// key renders a path as an unambiguous map key. Path.String is for display and
// is documented as lossy for numeric mapping keys; a join key cannot be.
func key(p resolve.Path) string { return strings.Join(p, "\x00") }

// comparePath orders two paths segment by segment. Ordering by the rendered
// string would sort by a lossy projection, which is how two distinct nodes end
// up adjacent and then indistinguishable.
func comparePath(a, b resolve.Path) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func clonePath(p resolve.Path) resolve.Path {
	out := make(resolve.Path, len(p))
	copy(out, p)
	return out
}

func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// clone deep-copies a node so a caller cannot reach into the graph through a
// returned slice header.
func (n Node) clone() Node {
	out := n
	out.Path = clonePath(n.Path)
	out.Profiles = cloneStrings(n.Profiles)
	if n.Port != nil {
		p := *n.Port
		out.Port = &p
	}
	if n.Build != nil {
		b := *n.Build
		out.Build = &b
	}
	return out
}

func (e Edge) clone() Edge {
	out := e
	out.From = clonePath(e.From)
	out.To = clonePath(e.To)
	if e.Depends != nil {
		d := *e.Depends
		out.Depends = &d
	}
	if e.Attach != nil {
		a := *e.Attach
		a.Aliases = cloneStrings(e.Attach.Aliases)
		out.Attach = &a
	}
	if e.Mount != nil {
		m := *e.Mount
		out.Mount = &m
	}
	if e.Port != nil {
		p := *e.Port
		out.Port = &p
	}
	if e.Build != nil {
		b := *e.Build
		out.Build = &b
	}
	return out
}

func (d Dangling) clone() Dangling {
	out := d
	out.From = clonePath(d.From)
	out.To = clonePath(d.To)
	return out
}

// sortKey is a total order over edges, so that two builds of the same model
// emit them in the same sequence. Origin is part of the key because the same
// relation can be declared twice — the same volume at two mount points — and
// the position is the only thing that tells them apart.
func (e Edge) sortKey() string {
	var b strings.Builder
	b.WriteString(key(e.From))
	b.WriteString("\x01")
	b.WriteString(string(e.Kind))
	b.WriteString("\x01")
	b.WriteString(key(e.To))
	b.WriteString("\x01")
	b.WriteString(e.Origin.String())
	b.WriteString("\x01")
	if e.Mount != nil {
		b.WriteString(e.Mount.Source + ">" + e.Mount.Target + ":" + e.Mount.Mode)
	}
	if e.Port != nil {
		b.WriteString(e.Port.Raw)
	}
	if e.Depends != nil {
		b.WriteString(e.Depends.Condition)
	}
	if e.Attach != nil {
		b.WriteString(strings.Join(e.Attach.Aliases, ","))
	}
	if e.Build != nil {
		b.WriteString(e.Build.Reference + ":" + e.Build.Target)
	}
	return b.String()
}

func (d Dangling) sortKey() string {
	return key(d.From) + "\x01" + string(d.Kind) + "\x01" + key(d.To) + "\x01" + d.Origin.String()
}
