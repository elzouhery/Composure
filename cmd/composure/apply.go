package main

// `composure preview` and `composure apply` — the headless write path.
//
// They are one function and one boolean apart, because the product's claim is
// that the diff you approve is the diff that lands. Two code paths would be two
// answers, and the second one would be the one that touched your file.
//
// CLI before UI, always: the extension's `Save to <file>` press does exactly
// what `composure apply` does, over the same `stack/apply` request the CLI builds
// here. Nothing in the editor can write bytes the CLI cannot.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/elzouhery/composure/internal/edit"
)

// Exit codes for the edit commands.
//
//	0  the edit applies (or, for preview, would apply)
//	1  an operational failure — the file could not be read or written
//	2  a usage error
//	3  a REFUSAL: the engine declined to perform the edit safely, or the staged
//	   range has moved. A CI job can tell "cannot be done" from "went wrong"
//	   without reading stderr, and neither wrote anything.
const exitRefused = 3

type editFlags struct {
	op          string
	at          string
	key         string
	value       string
	where       string
	stage       int
	instruction int
	expectStart int
	expectEnd   int
	expectText  string
	hasExpect   bool
}

// runEdit previews or applies one operation against one file.
func runEdit(path string, f editFlags, write, asJSON bool) {
	op := edit.Op{
		Operation: edit.Operation(strings.TrimSpace(f.op)),
		At:        f.at,
		Key:       f.key,
		Value:     f.value,
		Where:     f.where,

		Stage:       f.stage,
		Instruction: f.instruction,
	}
	if f.hasExpect {
		// AD-19 from a shell: a script that recorded a range earlier can assert
		// it here, and a file that has moved under it refuses rather than
		// writing a stale span.
		op.Expect = &edit.Expect{Start: f.expectStart, End: f.expectEnd, Text: f.expectText}
	}

	req := edit.Request{File: path, Ops: []edit.Op{op}}
	var (
		res *edit.Result
		err error
	)
	if write {
		res, err = edit.Apply(req)
	} else {
		res, err = edit.Preview(req)
	}
	if err != nil {
		reportEditFailure(path, err, asJSON)
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
	printEdit(res)
}

func printEdit(res *edit.Result) {
	verb := "WOULD CHANGE"
	if res.Written {
		verb = "WROTE"
	}
	fmt.Printf("\n%s — %s\n", verb, res.File)
	fmt.Println(strings.Repeat("=", 78))
	for _, o := range res.Ops {
		fmt.Printf("  %-16s %s\n", o.Operation, o.Describe)
		fmt.Printf("  %-16s bytes %d-%d (line %d), was %q\n", "", o.Range.Start, o.Range.End, o.Range.Line, o.Before)
	}
	fmt.Println()
	fmt.Print(res.Diff)
	// The headline number. R4.1 is "one removed, one added" and printing the
	// count is what makes a regression visible rather than something someone
	// has to notice in a diff.
	fmt.Printf("\n%d line(s) changed: %d removed, %d added\n\n", res.Changed, res.Removed, res.Added)
}

// reportEditFailure prints an edit failure in whichever shape the caller asked
// for and exits. It never returns.
//
// A refusal is not a crash and must not read like one: it names what could not
// be done and why, carries a stable reason slug for a machine, and exits 3.
func reportEditFailure(path string, err error, asJSON bool) {
	reason := edit.Reason(err)
	refused := edit.Refused(err) || errors.Is(err, edit.ErrStaleRange)
	code := 1
	if refused {
		code = exitRefused
	}
	if asJSON {
		env := map[string]any{"error": err.Error(), "file": path, "written": false}
		if reason != "" {
			env["reason"] = reason
		}
		env["refused"] = refused
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(env)
		os.Exit(code)
	}
	fmt.Fprintf(os.Stderr, "composure: %v\n", err)
	if refused {
		fmt.Fprintln(os.Stderr, "composure: nothing was written.")
	}
	os.Exit(code)
}
