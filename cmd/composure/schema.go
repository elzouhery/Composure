package main

// `composure schema` is the headless form of the inspector's differentiator: for
// any config path, what the Compose specification permits, what the file
// declares, and therefore what it does not.
//
// It exists before the UI for the reason every capability here does — the core
// has to be usable as a library, as a CLI and later as an MCP server, and a
// feature that only works from the GUI is built wrong. It is also the only way
// to check the generated list against the specification without opening an
// editor.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elzouhery/composure/internal/diagnose"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/schema"
)

func runSchema(path, at string, asJSON bool) {
	project, err := resolve.File(path)
	if err != nil {
		reportResolveFailure(path, err, asJSON)
	}

	// One reader of the environment, shared with the diagnostics, so that a
	// `${VAR}` shown with a resolution here and reported as undefined there
	// cannot disagree.
	env, envKnown := diagnose.Environment(filepath.Dir(path))

	res, err := schema.Inspect(path, project, schema.Options{
		At:       at,
		All:      at == "",
		Env:      env,
		EnvKnown: envKnown,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "composure: %v\n", err)
		os.Exit(1)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printSchema(res)
}

func printSchema(res *schema.Result) {
	fmt.Printf("\nSCHEMA — %s\n", res.Path)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("compose-spec: %s\n", res.SchemaCommit)
	if res.ComposeVersionKnown {
		fmt.Printf("installed compose: %s\n", res.ComposeVersion)
	} else {
		// Stated, not silent: it is the reason nothing below is marked.
		fmt.Println("installed compose: none found — nothing is marked unsupported")
	}
	if res.VersionField != "" {
		fmt.Printf("version field: %q — obsolete, ignored, and safe to delete\n", res.VersionField)
	}
	fmt.Printf("source files: %d\n", len(res.Files))
	for _, f := range res.Files {
		fmt.Printf("  [%d] %s\n", f.Step, f.Path)
	}
	if len(res.Profiles) > 0 {
		fmt.Printf("declared profiles: %s\n", strings.Join(res.Profiles, ", "))
	}

	nodes := res.Nodes
	if len(nodes) == 0 && res.Node != nil {
		nodes = []schema.Node{*res.Node}
	}
	for _, n := range nodes {
		printSchemaNode(n)
	}
	fmt.Println()
}

func printSchemaNode(n schema.Node) {
	name := n.Path
	if name == "" {
		name = "(stack)"
	}
	fmt.Printf("\n%s — %s\n", name, n.Schema)
	if !n.Known {
		fmt.Println("  the vendored specification does not describe this path;")
		fmt.Println("  what the file declares is shown, what else is possible is not known")
	}
	fmt.Printf("  %d declared, %d available and not set\n", n.DeclaredCount, n.AvailableCount)

	for _, f := range n.Fields {
		if !f.Declared {
			continue
		}
		fmt.Printf("    set     %-22s %s\n", f.Key, summarise(f))
		printAllowed(f)
	}
	for _, f := range n.Fields {
		if f.Declared {
			continue
		}
		var tags []string
		if f.Default != "" {
			tags = append(tags, "default "+f.Default)
		}
		if f.Support == schema.SupportNo {
			tags = append(tags, "needs compose "+f.MinVersion)
		}
		if f.Deprecated {
			tags = append(tags, "deprecated")
		}
		suffix := ""
		if len(tags) > 0 {
			suffix = "  [" + strings.Join(tags, " · ") + "]"
		}
		fmt.Printf("    not set %-22s %s%s\n", f.Key, f.Type, suffix)
		printAllowed(f)
	}
}

// printAllowed names the values the specification names for a key — story 7.9.
//
// On its own line rather than in the row, because the rows are aligned columns
// and a list of eight values would push the origin off the width. It is printed
// for a SET key as well as an unset one: "which values does this accept" is a
// question about a key someone has already written, and the extension's picker
// is offered on both.
//
// The source is stated in the reader's words rather than as the wire's slug.
// Only `schema` is a closed set; the other two are the values the specification
// NAMES, and a file may legitimately hold others — which is why nothing in this
// product turns this list into a control that can only produce these values.
func printAllowed(f schema.Field) {
	if len(f.Allowed) == 0 {
		return
	}
	note := ""
	switch f.AllowedSource {
	case "schema":
		note = " (the specification allows nothing else)"
	case "schema-branch":
		// `gpus`: an `enum` on one arm of a `oneOf` whose other arm is a list of
		// GPU device objects. The values are the specification's own and they
		// are exhaustive for THAT form; the key accepts another form entirely,
		// and a reader who believed "nothing else" would not write it.
		note = " (all the specification names for one of the forms this key accepts; it accepts others)"
	case "pattern":
		note = " (named by the specification's pattern; it permits other forms too)"
	case "description":
		note = " (listed in the specification's prose, not enforced by it)"
	}
	fmt.Printf("            one of %s%s\n", strings.Join(f.Allowed, " · "), note)
}

// summarise renders a declared value on one line. It never prints a count in
// place of the values — that is the incumbent's failure and the reason story
// 5.1 exists — it prints them, truncated by width alone.
func summarise(f schema.Field) string {
	if f.Value == nil {
		return ""
	}
	text := strings.Join(strings.Fields(inlineValue(*f.Value)), " ")
	if len(text) > 68 {
		text = text[:67] + "…"
	}
	return fmt.Sprintf("%-70s %s", text, f.Value.Origin)
}

// inlineValue renders a value tree on one line, recursively. Truncation is by
// width at the end, never by dropping entries here: a renderer that stopped
// after three keys would be printing a count in a different costume.
func inlineValue(v schema.ValueView) string {
	switch v.Kind {
	case "scalar":
		if v.Resolved != "" && v.Resolved != v.Text {
			return v.Text + " → " + v.Resolved
		}
		return v.Text
	case "sequence":
		parts := make([]string, 0, len(v.Seq))
		for _, item := range v.Seq {
			parts = append(parts, inlineValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case "mapping":
		parts := make([]string, 0, len(v.Entries))
		for _, e := range v.Entries {
			parts = append(parts, e.Key+"="+inlineValue(e.Value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case "null":
		return "~"
	}
	return v.Text
}
