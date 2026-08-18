package main

// `composure explain <config-path>` — story 1.10.
//
// The question the product exists to answer, on the command line: which file
// set this value, and what did it override. Everything printed here is read off
// the resolved model; nothing is recomputed, because the merge recorded it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// explainJSON is the wire shape (AD-9): the table below is a rendering of this
// struct and never a second answer.
type explainJSON struct {
	Path      string               `json:"path"`
	File      string               `json:"file"`
	Line      int                  `json:"line"`
	Column    int                  `json:"column"`
	Step      int                  `json:"step"`
	Kind      string               `json:"kind"`
	Value     string               `json:"value"`
	Raw       string               `json:"raw,omitempty"`
	Overrides []resolve.Override   `json:"overrides"`
	Alias     string               `json:"alias,omitempty"`
	AliasSite *resolve.Origin      `json:"alias_site,omitempty"`
	Files     []resolve.SourceFile `json:"files"`
	// Dropped is the configuration at or under this path that the merge did not
	// keep. Present and empty, never absent: "nothing was lost here" has to be
	// sayable, and an omitted field says it to nobody.
	Dropped []resolve.Finding `json:"dropped"`
}

func explanationWire(p *resolve.Project, e *resolve.Explanation) explainJSON {
	v := e.Value
	o := v.Origin()
	out := explainJSON{
		Path:      e.Path.String(),
		File:      o.File,
		Line:      o.Line,
		Column:    o.Column,
		Step:      o.Step,
		Kind:      v.Kind().String(),
		Value:     v.Scalar(),
		Overrides: e.Overrides(),
		Files:     p.Files(),
		Dropped:   droppedUnder(p, e.Path),
	}
	if v.Interpolated() {
		out.Raw = v.Raw()
	}
	if site, ok := v.AliasSite(); ok {
		out.Alias, out.AliasSite = v.Alias(), &site
	}
	return out
}

// droppedUnder is every form-mismatch finding at `at` or anywhere beneath it.
//
// Beneath, and not merely at, because explain is the tool people point at a
// SERVICE — `services.web` — as often as at a leaf, and a merge that dropped
// half of `services.web.environment` is the single most important thing anyone
// asking "what is this service actually getting" needs to be told. The table
// answered that with "a collection — see the file", which is the silence
// CLAUDE.md rule 6 forbids.
func droppedUnder(p *resolve.Project, at resolve.Path) []resolve.Finding {
	out := []resolve.Finding{}
	for _, f := range p.Findings() {
		if f.Kind != resolve.FindingFormMismatch {
			continue
		}
		if len(f.Path) < len(at) {
			continue
		}
		if f.Path[:len(at)].Equal(at) {
			out = append(out, f)
		}
	}
	return out
}

func runExplain(project *resolve.Project, entry string, at string, asJSON bool) {
	path := resolve.ParsePath(at)
	e, err := project.Explain(path)
	if err != nil {
		reportExplainFailure(at, err, asJSON)
	}

	wire := explanationWire(project, e)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(wire); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\nEXPLAIN — %s\n", wire.Path)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("entry:  %s\n", entry)
	fmt.Printf("kind:   %s\n", wire.Kind)
	if wire.Kind == "scalar" {
		fmt.Printf("value:  %s\n", wire.Value)
		if wire.Raw != "" {
			fmt.Printf("as written: %s\n", wire.Raw)
		}
	}
	fmt.Printf("set by: %s:%d:%d  [step %d]\n", wire.File, wire.Line, wire.Column, wire.Step)
	if wire.Alias != "" {
		fmt.Printf("        reached through *%s at %s\n", wire.Alias, wire.AliasSite)
	}

	// Present and empty, never absent: "never overridden" has to be sayable.
	fmt.Printf("\nOVERRODE (%d)\n", len(wire.Overrides))
	if len(wire.Overrides) == 0 {
		fmt.Println("  nothing — this value was set once and never replaced")
	}
	for i, ov := range wire.Overrides {
		val := ov.Value
		if val == "" {
			val = "(a collection — see the file)"
		}
		fmt.Printf("  %d. %-40s %s\n", i+1, val, ov.Origin)
	}

	// The one thing explain used to refuse to say. A collection replaced across
	// a form mismatch loses entries, and the override line above renders the
	// loser as "(a collection — see the file)" — which is exactly the shrug that
	// makes the loss invisible. Name what went.
	if len(wire.Dropped) > 0 {
		fmt.Printf("\nDROPPED IN MERGE (%d)  <- written in the files, NOT in the resolved value\n", len(wire.Dropped))
		for _, d := range wire.Dropped {
			lost := "entries only the earlier declaration set"
			if len(d.Dropped) > 0 {
				lost = strings.Join(d.Dropped, ", ")
			}
			fmt.Printf("  %s\n", d.Path)
			fmt.Printf("      lost:  %s\n", lost)
			if !d.Displaced.IsZero() {
				fmt.Printf("      from:  %s\n", d.Displaced)
			}
			fmt.Printf("      kept:  %s\n", d.Origin)
			fmt.Printf("      why:   the two files write this key in different shapes — a list in one, a mapping in\n")
			fmt.Printf("             the other. Reconciling them would mean re-emitting the collection, so the later\n")
			fmt.Printf("             declaration replaces the earlier whole. `docker compose config` keeps both.\n")
		}
	}

	fmt.Printf("\nSOURCE FILES (%d)\n", len(wire.Files))
	for _, f := range wire.Files {
		marker := "  "
		if f.Path == wire.File {
			marker = "->"
		}
		fmt.Printf("  %s [%d] %s\n", marker, f.Step, f.Path)
	}
	fmt.Println()
}

// reportExplainFailure prints a path that addresses nothing, with the closest
// paths that do. It never returns.
func reportExplainFailure(at string, err error, asJSON bool) {
	var pe *resolve.PathError
	if asJSON {
		env := map[string]any{"error": err.Error(), "path": at}
		if errors.As(err, &pe) {
			env["closest"] = pe.Closest
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(env)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "composure: %v\n", err)
	os.Exit(1)
}
