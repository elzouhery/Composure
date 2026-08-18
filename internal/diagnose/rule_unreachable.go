package diagnose

import (
	"fmt"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 3.7 — a service nothing can reach.
//
// The test is: no published port, and no network shared with any other service.
// Everything about it that is subtle is in what does NOT count as unreachable.
//
//   - A service on an `internal:` network with others is reachable. `internal`
//     means no route OUT of the network, not that the network does not carry
//     traffic. Treating it as isolation would flag every correctly hardened
//     database in the corpus, which is the shape of false positive that ends a
//     rule's life.
//   - A service reachable through another's `network_mode: service:x` is
//     reachable. The two share one namespace; that is a real path, and it is
//     the edge story 2.3 built rather than something re-derived here.
//   - A legacy `links:` entry is a path too.
//   - The IMPLICIT default network counts. Compose puts every service that
//     names no network onto one shared network, and it is the commonest shape
//     in the corpus by a wide margin. A rule that ignored it would report every
//     service of every simple stack. Membership is read off the graph: a
//     service with no network attachment and no network_mode is on the default.
//
// A service that declares `network_mode:` of any kind — host, none, container:,
// service: — is left alone. `host` is reachable on every port the process
// binds, `none` is deliberate isolation, and neither is something to be told
// about. Reading that one key off the service is a local value read, which
// AD-17 permits; every relation the rule uses is still an edge from the graph.
//
// No mechanical fix. Whether the answer is to publish a port, join a network,
// or delete the service depends entirely on what it was for.

const unreachableNoFix = "no automatic fix is offered: publishing a port, attaching a network and deleting the service are all valid answers, and which one is right depends on what the service was for"

type unreachableRule struct{}

func (unreachableRule) id() string { return "unreachable-service" }

func (unreachableRule) title() string { return "Service nothing can reach" }

func (r unreachableRule) apply(c *context) []Finding {
	// The guard that used to stand here went silent on any project using
	// extends: or include:, because the networks connecting these services
	// lived in a file the resolver could not see. Stories 1.7 and 1.8 expand
	// both, so those networks are in the model now and reachability is a claim
	// this rule can actually support.
	g := c.Graph

	// Every relation below comes from the graph's edge list, walked once.
	var (
		publishes   = map[string]bool{}
		attached    = map[string][]resolve.Path{} // service -> networks
		networkPeer = map[string]map[string]bool{}
		namespaced  = map[string]bool{} // touched by a network_mode or link edge
		linked      = map[string]bool{}
	)
	for _, e := range g.Edges() {
		switch e.Kind {
		case topology.EdgePublish:
			if e.Port != nil && e.Port.Published != "" {
				publishes[pathKey(e.From)] = true
			}
		case topology.EdgeNetwork:
			attached[pathKey(e.From)] = append(attached[pathKey(e.From)], e.To)
			if networkPeer[pathKey(e.To)] == nil {
				networkPeer[pathKey(e.To)] = map[string]bool{}
			}
			networkPeer[pathKey(e.To)][pathKey(e.From)] = true
		case topology.EdgeNetworkMode:
			namespaced[pathKey(e.From)] = true
			namespaced[pathKey(e.To)] = true
		case topology.EdgeLink:
			linked[pathKey(e.From)] = true
			linked[pathKey(e.To)] = true
		}
	}

	// The implicit default network: services that named no network and set no
	// network_mode. Compose attaches them all to one network, so if there is
	// more than one they can reach each other.
	var implicitDefault []topology.Node
	for _, n := range g.Nodes() {
		if n.Kind != topology.NodeService {
			continue
		}
		if len(attached[pathKey(n.Path)]) > 0 || r.declaresNetworkMode(c, n.Path) {
			continue
		}
		implicitDefault = append(implicitDefault, n)
	}
	onDefault := map[string]bool{}
	if len(implicitDefault) > 1 {
		for _, n := range implicitDefault {
			onDefault[pathKey(n.Path)] = true
		}
	}

	var out []Finding
	for _, n := range g.Nodes() {
		if n.Kind != topology.NodeService || !n.Declared {
			continue
		}
		k := pathKey(n.Path)
		if publishes[k] || namespaced[k] || linked[k] || onDefault[k] {
			continue
		}
		if r.declaresNetworkMode(c, n.Path) {
			continue
		}
		if r.sharesANetwork(attached[k], networkPeer, k) {
			continue
		}
		if n.Origin.IsZero() {
			continue // AD-7
		}
		out = append(out, Finding{
			Rule:     r.id(),
			Severity: SeverityWarning,
			Title:    r.title(),
			Message: fmt.Sprintf("service %q publishes no port and shares no network with any other service, so nothing in this project or outside it can reach it.%s",
				n.Name, r.aloneOnNetworks(attached[k])),
			Subjects: []resolve.Path{n.Path},
			Anchors: []Anchor{{
				Label:  "service declared here",
				Path:   n.Path,
				Origin: n.Origin,
			}},
			NoFix: unreachableNoFix,
		})
	}
	return out
}

// sharesANetwork reports whether any network this service is attached to has
// another service on it. An internal network counts: internal means no route
// out, not no traffic.
func (r unreachableRule) sharesANetwork(nets []resolve.Path, peers map[string]map[string]bool, self string) bool {
	for _, net := range nets {
		for peer := range peers[pathKey(net)] {
			if peer != self {
				return true
			}
		}
	}
	return false
}

func (r unreachableRule) aloneOnNetworks(nets []resolve.Path) string {
	if len(nets) == 0 {
		return ""
	}
	if len(nets) == 1 {
		return fmt.Sprintf(" It is the only service on network %q.", serviceName(nets[0]))
	}
	return " It is the only service on every network it joins."
}

// declaresNetworkMode reads one key off one service. It is a local value read,
// not a relation — the graph models `network_mode: service:x` as an edge and
// this rule uses that edge for reachability; what this answers is the different
// question of whether the service opted out of ordinary networking at all, for
// which `host` and `none` produce no edge and so cannot be seen in the graph.
func (r unreachableRule) declaresNetworkMode(c *context, p resolve.Path) bool {
	svc, ok := c.serviceValue(p)
	if !ok {
		return false
	}
	v, ok := field(svc, "network_mode")
	return ok && v != nil && v.Kind() == resolve.KindScalar && v.Scalar() != ""
}
