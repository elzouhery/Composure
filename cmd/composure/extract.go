package main

// `composure extract` — move a literal into a variable, and into the `.env`.
//
// It is the one command in this product that changes two files, so it is the
// one command that cannot go through `edit.Request`: a request is one file,
// because a diff the reader approves has to name the file it touches. Here the
// second file is DERIVED BY DEFAULT — the `.env` beside the compose file — so
// the pair is still one gesture with one answer. `-env` names it instead, for
// the caller who knows better (DECISIONS.md 25); the result reports whichever
// file was actually written, so the answer still names both halves.
//
// CLI before UI, always. Nothing in the editor will write these two files by a
// route this command cannot.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/elzouhery/composure/internal/edit"
)

type extractFlags struct {
	at      string
	name    string
	envFile string
	// Story 9.4's half. instruction is -1 when the flag was not given, so
	// "instruction 0" and "no instruction" are distinguishable — they are
	// different requests and a zero value cannot say which.
	instruction int
	part        string
	// Story 9.6. The compose (or Dockerfile) half's byte-range assertion, and
	// the `.env` half's assertion about the VARIABLE. Both optional, both only
	// sent when the reader actually gave them: a request with no expectation is
	// applied against the files as they stand, which is what a CLI invocation
	// needs and what every existing caller does.
	hasExpect      bool
	expectStart    int
	expectEnd      int
	expectText     string
	hasExpectEnv   bool
	expectEnvSet   bool
	expectEnvValue string
}

// runExtract dispatches on the FILE's grammar, never on which flag was typed.
// `composure extract` is one subcommand over two operations because the reader's
// gesture is one — "move this value out into a name" — and the file decides
// which of the two that means. A compose file gets story 9.3's two-file write;
// a Dockerfile gets story 9.4's `ARG`, which writes ONE file.
func runExtract(file string, f extractFlags, write, asJSON bool) {
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "composure: %v\n", err)
		os.Exit(1)
	}
	if edit.FileGrammar(src) == edit.GrammarDockerfile {
		runExtractArg(file, f, write, asJSON)
		return
	}
	if strings.TrimSpace(f.at) == "" {
		fmt.Fprintln(os.Stderr, "composure: extract needs -at")
		os.Exit(2)
	}
	req := edit.Extract{File: file, At: f.at, Name: f.name, EnvFile: f.envFile}
	if f.hasExpect {
		req.Expect = &edit.Expect{Start: f.expectStart, End: f.expectEnd, Text: f.expectText}
	}
	if f.hasExpectEnv {
		req.ExpectEnv = &edit.ExpectVar{Defined: f.expectEnvSet, Value: f.expectEnvValue}
	}
	var res *edit.ExtractResult
	if write {
		res, err = edit.ApplyExtract(req)
	} else {
		res, err = edit.PreviewExtract(req)
	}
	if err != nil {
		reportEditFailure(file, err, asJSON)
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
	printExtract(res, f.at)
}

// printExtract writes the human form. It names the PATH and the variable, never
// the value.
//
// The headline read `MOVED hunter2 into ${POSTGRES_PASSWORD}` — the secret,
// bare, with no context, on the line most likely to be pasted into a chat
// window or scrolled past in a CI log. This is the command that exists to fix a
// plaintext credential, and `internal/diagnose`'s credential rule goes to
// lengths never to print a value; printing it here undid that.
//
// The two diffs below DO carry the value, and that is deliberate. A diff is the
// thing the reader approves, and DECISIONS.md 25 requires both halves of a
// two-file write to be shown — a preview that hid the line it is about to write
// would be the lie the "both diffs" rule exists to prevent. What is removed is
// the one line that carried the secret and added nothing.
func printExtract(res *edit.ExtractResult, at string) {
	verb := "WOULD MOVE"
	if res.Written {
		verb = "MOVED"
	}
	fmt.Printf("\n%s the value at %s into ${%s}\n", verb, at, res.Name)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Print(res.Compose.Diff)
	fmt.Println()
	switch {
	case res.EnvUnchanged:
		// Said out loud rather than left as an empty diff. "Nothing changed" and
		// "nothing needed to change" are different sentences.
		fmt.Printf("%s already sets %s to this value; it is left byte-identical.\n\n", res.EnvFile, res.Name)
	case res.EnvCreated:
		fmt.Printf("%s (new)\n", res.EnvFile)
		fmt.Print(res.EnvDiff)
		fmt.Println()
	default:
		fmt.Print(res.EnvDiff)
		fmt.Println()
	}
}

// runExtractArg is story 9.4 — the same subcommand, the other grammar.
func runExtractArg(file string, f extractFlags, write, asJSON bool) {
	if f.instruction < 0 {
		fmt.Fprintln(os.Stderr, "composure: extract on a Dockerfile needs -instruction N — "+
			"the zero-based index over ALL instructions, the same one `-op replace_args` takes")
		os.Exit(2)
	}
	req := edit.ExtractArg{File: file, Instruction: f.instruction, Part: f.part, Name: f.name}
	if f.hasExpect {
		req.Expect = &edit.Expect{Start: f.expectStart, End: f.expectEnd, Text: f.expectText}
	}
	var (
		res *edit.ExtractArgResult
		err error
	)
	if write {
		res, err = edit.ApplyExtractArg(req)
	} else {
		res, err = edit.PreviewExtractArg(req)
	}
	if err != nil {
		reportEditFailure(file, err, asJSON)
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
	printExtractArg(res)
}

// printExtractArg says where the declaration landed and WHY it could not be
// anywhere else. The placement rule is the correctness condition of this whole
// operation — an ARG used before it is declared expands to the empty string
// with no error — and a rule the reader cannot see is a rule they cannot check.
func printExtractArg(res *edit.ExtractArgResult) {
	verb := "WOULD MOVE"
	if res.Written {
		verb = "MOVED"
	}
	fmt.Printf("\n%s the value into the build argument ${%s}\n", verb, res.Name)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Print(res.Dockerfile.Diff)
	fmt.Println()
	switch {
	case res.AlreadyDeclared:
		fmt.Printf("`%s` was already declared in %s with this default; no second declaration was written.\n",
			res.Name, res.Scope)
	case res.Redeclared:
		fmt.Printf("Scope: %s. %s\n", res.Scope, res.ScopeReason)
	default:
		fmt.Printf("Scope: %s. %s\n", res.Scope, res.ScopeReason)
	}
	fmt.Printf("\n%s\n\n", res.ComposeNote)
}
