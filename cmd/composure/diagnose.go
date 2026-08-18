package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/diagnose"
	"github.com/elzouhery/composure/internal/resolve"
)

// Exit codes for `composure diagnose`.
//
// They are deliberately clear of the codes the rest of the CLI already uses —
// 1 for an operational failure, 2 for a usage error — so that a CI job can tell
// "the stack has warnings" from "the file would not parse" without reading
// stderr. A clean stack exits 0: findings are not errors, and neither is their
// absence (AD-13).
const (
	exitHint    = 10
	exitWarning = 20
	exitError   = 30
)

func exitCodeFor(rep *diagnose.Report) int {
	worst, any := rep.Highest()
	if !any {
		return 0
	}
	switch worst {
	case diagnose.SeverityHint:
		return exitHint
	case diagnose.SeverityWarning:
		return exitWarning
	}
	return exitError
}

// runDiagnose resolves, derives the topology and applies every rule. A file
// that will not parse is a resolve failure and exits the way every other
// subcommand does; a file that resolves but is wrong is findings.
func runDiagnose(project *resolve.Project, path string, profiles []string, asJSON bool) {
	rep, err := diagnose.AnalyzeProject(path, project, profiles)
	if err != nil {
		reportResolveFailure(path, err, asJSON)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(os.Stderr, "composure: %v\n", err)
			os.Exit(1)
		}
		os.Exit(exitCodeFor(rep))
	}
	printDiagnose(path, rep)
	os.Exit(exitCodeFor(rep))
}

func printDiagnose(path string, rep *diagnose.Report) {
	fmt.Printf("\nDIAGNOSE — %s\n", path)
	fmt.Println(strings.Repeat("=", 78))
	if len(rep.Profiles) > 0 {
		fmt.Printf("active profiles: %s\n", strings.Join(rep.Profiles, ", "))
	} else {
		fmt.Println("active profiles: none (only always-active services)")
	}
	fmt.Printf("rules: %d\n", len(rep.Rules))

	if len(rep.Findings) == 0 {
		fmt.Printf("\nno findings\n\n")
		return
	}

	for _, f := range rep.Findings {
		fmt.Printf("\n%-7s  %s  [%s]\n", strings.ToUpper(f.Severity.String()), f.Title, f.Rule)
		fmt.Printf("         %s\n", f.Message)
		for _, a := range f.Anchors {
			fmt.Printf("         at %s  %s\n", a.Origin, a.Label)
		}
		switch {
		case f.Fix != nil:
			fmt.Printf("         fix: %s\n", f.Fix.Describe)
			fmt.Printf("              %s %s in %s, bytes %d-%d (line %d)\n",
				f.Fix.Operation, f.Fix.Path, f.Fix.File,
				f.Fix.Range.Start, f.Fix.Range.End, f.Fix.Range.Line)
			// Story 9.5. The half of the advice that is not a splice in this
			// file. Printed as the COMMAND, because CLI before UI means the
			// thing a pane offers and the thing a terminal runs are one thing,
			// and because a remedy nobody can copy is a remedy nobody runs.
			if r := f.Fix.Remedy; r != nil {
				fmt.Printf("         and: %s\n", r.Describe)
				fmt.Printf("              %s\n", r.Command)
			} else if f.NoRemedy != "" {
				// Run dropped a remedy a rule had offered. Rule 6: a guard that
				// silently discards is a guard nobody can debug, and until this
				// line existed NoRemedy was set, typed on the wire, and read by
				// nothing at all. No shipped rule can reach it — every one that
				// offers a remedy self-censors — so this is the same latent
				// shape as the guard that sets it, printed rather than dropped.
				fmt.Printf("         and: %s\n", f.NoRemedy)
			}
		default:
			fmt.Printf("         fix: %s\n", f.NoFix)
		}
	}

	counts := rep.Counts()
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Printf("\nBY RULE\n")
	for _, id := range ids {
		fmt.Printf("  %-28s %d\n", id, counts[id])
	}

	worst, _ := rep.Highest()
	fmt.Printf("\n%d findings, highest severity %s (exit %d)\n\n",
		len(rep.Findings), worst, exitCodeFor(rep))
}
