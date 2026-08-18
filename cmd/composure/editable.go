package main

// `composure editable` — where a value is actually written, and what an edit at
// that path would do.
//
// CLI before UI, always. The inspector greys a field out and prints a sentence
// under it; this is the same answer at a shell, from the same function, so the
// pane's explanation can be checked without a webview and a regression in it is
// visible to a script.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/elzouhery/composure/internal/edit"
)

// runEditable prints one path's availability.
//
// It takes a file rather than a project on purpose: an edit is against one
// file's bytes, and "which file holds this value" is a question the resolver
// answers separately (`composure explain`). Asking this command to guess would
// give the reader an answer about a file they did not name.
func runEditable(file, at string, asJSON bool) {
	if strings.TrimSpace(at) == "" {
		fmt.Fprintln(os.Stderr, "composure: editable needs -at")
		os.Exit(2)
	}
	a, err := edit.ClassifyFile(file, at)
	if err != nil {
		fmt.Fprintf(os.Stderr, "composure: %v\n", err)
		os.Exit(1)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(a); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("\n%s — %s\n", a.Path, file)
	fmt.Println(strings.Repeat("=", 78))
	if a.Editable {
		fmt.Printf("  editable in place at line %d\n\n", a.BytesAt.Line)
		return
	}
	fmt.Printf("  not editable in place (%s)\n", a.Reason)
	if a.Plan != "" {
		fmt.Printf("  an edit here would be staged as %s\n", a.Plan)
	}
	if a.Through != nil {
		fmt.Printf("  reached through line %d\n", a.Through.Line)
	}
	if a.BytesAt != nil {
		fmt.Printf("  its bytes are at line %d\n", a.BytesAt.Line)
	}
	fmt.Printf("\n  %s\n\n", a.Detail)
}
