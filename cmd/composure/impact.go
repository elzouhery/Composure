package main

// `composure impact` — the blast radius of one node: what breaks if it goes down,
// and what it needs in order to come up.
//
// The answer is `topology.BlastRadius` and nothing else. It walks depends_on
// edges only, because that is the relation Compose orders startup by; a shared
// network is a connection, not a dependency, and folding the two together would
// report the whole stack as the blast radius of every service on the default
// network.
//
// This exists as a command before the canvas uses it (CLI before UI, always):
// the extension's focus mode asks `stack/impact` for exactly this payload, so
// the graph pane dims a set the core computed rather than one a webview
// recomputed from the edges it happened to be sent.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// impactWire is the blast radius as the CLI prints it and the RPC returns it.
//
// Paths are rendered strings — `services.web` — because that is the join key
// every other surface in this product uses, and a client that received
// `["services","web"]` here and `"services.web"` from stack/topology would have
// to own a second notion of identity.
type impactWire struct {
	// Path is the file the graph was derived from, so a JSON consumer can tell
	// which project an answer belongs to.
	Path string `json:"path"`
	// Subject is the node asked about, as the graph knows it.
	Subject string `json:"subject"`
	// Dependents break if the subject goes down. Never null: an empty answer is
	// `[]`, which is a fact, where null reads as "not computed".
	Dependents []string `json:"dependents"`
	// Dependencies the subject needs before it can start.
	Dependencies []string `json:"dependencies"`
}

// impactOf runs BlastRadius and shapes it for the wire. The bool is false when
// the path names nothing in the graph — a typo, or a service the active profile
// set removed, and those are answered rather than guessed at.
func impactOf(g *topology.Graph, path, at string) (impactWire, bool) {
	imp, ok := g.BlastRadius(resolve.ParsePath(at))
	if !ok {
		return impactWire{}, false
	}
	return impactWire{
		Path:         path,
		Subject:      imp.Subject.String(),
		Dependents:   pathStrings(imp.Dependents),
		Dependencies: pathStrings(imp.Dependencies),
	}, true
}

// pathStrings renders paths and never returns nil, so `[]` survives the round
// trip as an empty list rather than as a null a client has to defend against.
func pathStrings(ps []resolve.Path) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func runImpact(project *resolve.Project, path, at string, profiles []string, asJSON bool) {
	graph, err := topology.Build(project, profiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "composure: %v\n", err)
		os.Exit(1)
	}
	wire, ok := impactOf(graph, path, at)
	if !ok {
		// Named, never an empty result: "no blast radius" and "no such node"
		// are different answers and only one of them is about the stack.
		fmt.Fprintf(os.Stderr, "composure: %s is not a node in this graph\n", at)
		os.Exit(1)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(wire); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\nIMPACT — %s\n", wire.Subject)
	fmt.Println(strings.Repeat("=", 78))
	list := func(label string, ids []string) {
		fmt.Printf("\n%s (%d)\n", label, len(ids))
		if len(ids) == 0 {
			fmt.Println("  none")
			return
		}
		for _, id := range ids {
			fmt.Printf("  %s\n", id)
		}
	}
	list("BREAKS IF THIS GOES DOWN", wire.Dependents)
	list("NEEDED BEFORE THIS STARTS", wire.Dependencies)
	fmt.Println()
}
