package edit

// Moving a Dockerfile literal into a build argument — story 9.4.
//
// `FROM node:18` becomes `FROM node:${NODE_VERSION}` with `ARG NODE_VERSION=18`
// written where the build can actually see it.
//
// THIS IS NOT STORY 9.3 WITH A DIFFERENT PARSER, and the differences are the
// reason it was deferred rather than half-built. Three properties of `ARG`
// decide every choice in this file, and getting any of them wrong ships an ARG
// that does nothing on some files and something on others:
//
//	1. An ARG used BEFORE it is declared expands to the empty string, with no
//	   error. `FROM node:${NODE_VERSION}` above `ARG NODE_VERSION=18` builds
//	   `FROM node:` and fails somewhere else, or worse, succeeds. Placement is
//	   not a preference, it is the correctness condition.
//	2. A FROM line can only use an ARG declared before the FIRST FROM.
//	   Absolute. If the value being moved is in a FROM there is exactly one
//	   legal position for the declaration and no choice to offer.
//	3. A global ARG is not visible inside a stage until it is re-declared
//	   there. `ARG NAME` with no value is Docker's documented way to pull the
//	   global default in, and it is the difference between a value that reaches
//	   the instruction and an empty string that does not.
//
// FOUR THINGS THAT HAD TO BE SETTLED, all recorded in DECISIONS.md 27.
//
// WHERE THE DECLARATION GOES. Immediately above the instruction that uses it —
// above that instruction's attached comment block, so a comment documenting a
// RUN keeps documenting it — and, when the value is in a FROM, immediately
// above the FIRST FROM in the file. Never at the end of the stage. Story 7.6's
// InstructionInsertionPoint is deliberately NOT reused: it appends after a
// stage's last instruction, which for an ARG is the one position guaranteed to
// be wrong. The new anchor (InstructionStartPoint / InsertBefore) shares 7.6's
// splice arithmetic and differs only in which line it lands on. The reader is
// TOLD which scope it landed in and why it could not be anywhere else; a
// placement rule the reader cannot see is a placement rule they cannot check.
//
// WHERE THE LITERAL GOES: THE ARG DEFAULT, AND NOT A `.env`. ARG has a default
// and `${VAR}` in compose does not, so unlike story 9.3 the literal can stay in
// the file — and it must. `ARG NODE_VERSION` with no default is property 1
// wearing a different hat: a bare `docker build .` would produce a different
// image and say nothing. Writing the literal to a `.env` instead is the
// confident wrong answer in the place it costs most, because `docker compose`
// passes build arguments only through `build.args` and a `.env` line would look
// like configuration and be inert. SO THIS OPERATION WRITES ONE FILE, which is
// the sharpest contrast with story 9.3 and the reason the two are not one.
//
// `build.args` IS DELIBERATELY NOT WIRED. Wiring it needs a third file, needs
// to know which compose service builds this Dockerfile — a resolution question,
// not an edit one — and would put the default in two places that can disagree.
// The result SAYS SO instead, in the reader's own words, and names what to
// write if they want it.
//
// QUOTING THE DEFAULT. A build-argument default has shell quoting rules this
// engine does not model, so a value that cannot be written BARE is refused
// rather than quoted on a guess. Story 9.3 could decide quoting by readback
// because resolve.ParseDotEnv exists to read it back; there is no second reader
// for an ARG default, and inventing one would be inventing the grammar it
// claims to check.
//
// NOTHING HERE SPLICES. Both halves — the substitution and the declaration —
// go through the ordinary `run` path as ordinary operations, so they get the
// ordinary validate, the ordinary diff and the ordinary atomic write. What is
// new in this file is deciding WHICH two operations, and refusing when there
// are none that are safe.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/strategy"
)

// ScopeGlobal is the scope name for a declaration before the first FROM.
const ScopeGlobal = "global"

// The grammar names FileGrammar answers with. They are the same two strings
// Operation.Grammar returns, because they are the same question.
const (
	GrammarYAML       = "yaml"
	GrammarDockerfile = "dockerfile"
)

// FileGrammar reports which engine owns a file, from the FILE and nothing else
// — not its name, not the operation aimed at it.
//
// A compose file is one whose root is a mapping. A Dockerfile is one with a
// FROM in it: that is what makes a file buildable, what scopes an ARG, and what
// story 9.4 is about — a file called `Dockerfile` with no FROM is not a
// Dockerfile for any purpose this package has. Anything else gets "", and the
// caller says so in its own words, because "it is not a compose file" and "it
// is not a Dockerfile" are different sentences to a reader holding one of them.
//
// It exists so that `composure extract` has ONE dispatch. The CLI, the RPC surface
// and both operations ask this; a second classifier would send the reader the
// paragraph about the file they are not holding, which this repository has
// already shipped once (DECISIONS.md 25, the ARG-refusal paragraph).
func FileGrammar(src []byte) string {
	if strategy.RootIsMapping(src) {
		return GrammarYAML
	}
	if len(dockerfile.Parse(src).Stages()) > 0 {
		return GrammarDockerfile
	}
	return ""
}

var (
	// ErrArgValue is a value that cannot be written as a bare ARG default.
	// Refused rather than quoted: see the head of this file.
	ErrArgValue = errors.New("that value cannot be written as a bare ARG default")
	// ErrArgConflict is a build argument of that name already declared in the
	// scope being written to, with a different default or with none. A second
	// declaration would shadow the first and the two would disagree.
	ErrArgConflict = errors.New("that build argument is already declared with something else")
	// ErrNoTag is a FROM pinned by digest, or one with no tag at all. A digest
	// is not a tag and `:latest` by implication is not a literal anybody wrote,
	// so there is nothing here to name.
	ErrNoTag = errors.New("that FROM has no tag to move")
)

// ExtractArg is the reader's request: move the literal at Instruction into a
// build argument.
type ExtractArg struct {
	// File is the Dockerfile. ONE file — unlike story 9.3, which is the
	// difference the head of this file exists to explain.
	File string
	// Instruction is the zero-based index over ALL instructions, the same index
	// `composure dockerfile` prints and OpReplaceArgs takes.
	Instruction int
	// Part names which piece of the instruction moves. For a FROM it is "tag"
	// and there is no other answer; empty means "tag". For everything else the
	// instruction body must be one `KEY=value` and Part is ignored.
	Part string
	// Name is the build argument's name. Empty derives one: the key for a
	// `KEY=value` body, and `<IMAGE>_VERSION` for a FROM tag.
	Name string
	// Expect is the staleness assertion for the substitution, exactly as
	// Op.Expect is. Optional; without it the edit is applied against wherever
	// the instruction is now, which is right for a CLI invocation and wrong for
	// a staged UI edit (AD-19).
	Expect *Expect
}

// ExtractArgResult is what a preview or an apply produced.
//
// Scope and ScopeReason are not decoration. The placement rule is the
// correctness condition of this whole operation, and a rule the reader cannot
// see is a rule they cannot check.
type ExtractArgResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Dockerfile is the ordinary edit result: both operations, one diff.
	Dockerfile *Result `json:"dockerfile"`
	// Scope is "global" or "stage N" — where the declaration landed.
	Scope string `json:"scope"`
	// ScopeReason is why it could not be anywhere else.
	ScopeReason string `json:"scope_reason"`
	// ArgLine is the declaration written, without its line ending. Empty when
	// AlreadyDeclared.
	ArgLine string `json:"arg_line,omitempty"`
	// Declared is a new declaration carrying the default.
	Declared bool `json:"declared"`
	// Redeclared is a bare `ARG NAME` pulling a global default into a stage.
	Redeclared bool `json:"redeclared"`
	// AlreadyDeclared is the idempotent case: the declaration was already
	// there with exactly this default, so only the substitution was written.
	AlreadyDeclared bool `json:"already_declared"`
	// ComposeNote says that `build.args` is not wired and what to write if the
	// reader wants it. DECISIONS.md 27: the absence is a sentence, not a gap.
	ComposeNote string `json:"compose_note"`
	Written     bool   `json:"written"`
}

// PreviewExtractArg computes the diff and writes nothing.
func PreviewExtractArg(ex ExtractArg) (*ExtractArgResult, error) { return runExtractArg(ex, false) }

// ApplyExtractArg performs the move.
func ApplyExtractArg(ex ExtractArg) (*ExtractArgResult, error) { return runExtractArg(ex, true) }

func runExtractArg(ex ExtractArg, write bool) (*ExtractArgResult, error) {
	if strings.TrimSpace(ex.File) == "" {
		return nil, fmt.Errorf("edit: no file named")
	}
	src, err := os.ReadFile(ex.File)
	if err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	// The grammar of a request is the FILE's — the same rule story 9.3 applies
	// in the other direction, and the reason it names the other operation
	// rather than returning a path error. FileGrammar is the ONE answer both
	// halves of `composure extract` classify with; two would eventually disagree
	// and the reader would get the wrong paragraph about the wrong file.
	if FileGrammar(src) == GrammarYAML {
		return nil, fmt.Errorf("%w: %s is a compose file, and an `ARG` is a Dockerfile instruction. "+
			"The compose equivalent is moving the value into a `${VAR}` with a `.env` beside it — "+
			"`composure extract -at <config path>`", ErrWrongGrammar, filepath.Base(ex.File))
	}
	f := dockerfile.Parse(src)
	if FileGrammar(src) != GrammarDockerfile {
		return nil, fmt.Errorf("%w: %s declares no build stage, so it has no scope for an `ARG` to live in "+
			"and nothing a build argument could reach", ErrWrongGrammar, filepath.Base(ex.File))
	}
	if ex.Instruction < 0 || ex.Instruction >= len(f.Instructions) {
		return nil, fmt.Errorf("instruction %d out of range (%d instructions)", ex.Instruction, len(f.Instructions))
	}
	in := f.Instructions[ex.Instruction]
	if in.Kind != dockerfile.KindInstruction {
		return nil, fmt.Errorf("%w: instruction %d is a comment or a blank line, not an instruction with a value in it",
			ErrNoLiteral, ex.Instruction)
	}

	plan, err := planExtractArg(f, ex, in)
	if err != nil {
		return nil, err
	}

	ops := []Op{plan.substitute}
	if !plan.alreadyDeclared {
		ops = append(ops, Op{
			Operation:   OpInsertInstructionBefore,
			Instruction: plan.anchor,
			Value:       plan.argLine,
		})
	}
	res, err := run(Request{File: ex.File, Ops: ops, Write: false})
	if err != nil {
		return nil, err
	}

	// The readback. This engine does not crash — it returns a confident wrong
	// answer — so the result is parsed again and the property that makes the
	// whole operation correct is asserted on the bytes rather than assumed from
	// the plan that produced them.
	useIdx := ex.Instruction
	if !plan.alreadyDeclared && plan.anchor <= ex.Instruction {
		useIdx++
	}
	if err := argReadback(res.Bytes, plan.name, plan.literal, useIdx, plan.fromTag); err != nil {
		return nil, err
	}

	out := &ExtractArgResult{
		Name:            plan.name,
		Value:           plan.literal,
		Dockerfile:      res,
		Scope:           plan.scope,
		ScopeReason:     plan.scopeReason,
		Declared:        plan.declared,
		Redeclared:      plan.redeclared,
		AlreadyDeclared: plan.alreadyDeclared,
		ComposeNote:     composeArgNote(plan.name),
	}
	if !plan.alreadyDeclared {
		out.ArgLine = plan.argLine
	}
	if !write {
		return out, nil
	}
	if err := writeFile(ex.File, res.Bytes); err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	out.Written, res.Written = true, true
	return out, nil
}

// argPlan is every decision this operation makes, made before a byte moves.
type argPlan struct {
	name        string
	literal     string
	substitute  Op
	anchor      int // instruction index the declaration is written ABOVE
	argLine     string
	scope       string
	scopeReason string
	fromTag     bool // the value came out of a FROM, so the ARG must be global

	declared        bool
	redeclared      bool
	alreadyDeclared bool
}

func planExtractArg(f *dockerfile.File, ex ExtractArg, in dockerfile.Instruction) (argPlan, error) {
	var p argPlan
	stages := f.Stages()

	if in.Name == "FROM" {
		part := strings.TrimSpace(ex.Part)
		if part == "" {
			part = "tag"
		}
		if part != "tag" {
			return p, fmt.Errorf("%w: the only part of a FROM this moves is its tag, and %q is not it. "+
				"Moving the repository would name a variable after the thing that identifies the image",
				ErrNoLiteral, part)
		}
		repo, tag, ok := splitImageTag(in.ImageRef)
		if !ok {
			return p, fmt.Errorf("%w: %s is pinned by digest or carries no tag. A digest is not a tag, and an "+
				"absent tag is `:latest` by implication rather than a literal anybody wrote", ErrNoTag, in.ImageRef)
		}
		if interpolated(tag) {
			return p, fmt.Errorf("%w: that FROM already reads %q. There is no literal here to move, and moving a "+
				"reference would declare the argument as itself", ErrAlreadyInterpolated, in.ImageRef)
		}
		p.fromTag, p.literal = true, tag
		p.name = strings.TrimSpace(ex.Name)
		if p.name == "" {
			p.name = deriveArgName(repo)
		}
		if err := validVarName(p.name); err != nil {
			return p, err
		}
		stageIdx := 0
		for i, s := range stages {
			if s == ex.Instruction {
				stageIdx = i
			}
		}
		p.substitute = Op{Operation: OpSetBaseImage, Stage: stageIdx, Value: repo + ":${" + p.name + "}",
			Expect: ex.Expect}
		// Property 2, and there is no choice to offer: above the FIRST FROM.
		p.anchor = stages[0]
		p.scope = ScopeGlobal
		p.scopeReason = fmt.Sprintf("a FROM can only use an ARG declared before the FIRST FROM, so the "+
			"declaration went above line %d and could not go anywhere else — inside a stage it would expand "+
			"to the empty string with no error", f.Instructions[stages[0]].StartLine+1)
	} else {
		key, value, err := singleAssignment(in)
		if err != nil {
			return p, err
		}
		if interpolated(value) {
			return p, fmt.Errorf("%w: instruction %d already reads %q. There is no literal here to move, and "+
				"moving a reference would declare the argument as itself",
				ErrAlreadyInterpolated, ex.Instruction, in.Args)
		}
		p.literal = value
		p.name = strings.TrimSpace(ex.Name)
		if p.name == "" {
			p.name = key
		}
		if err := validVarName(p.name); err != nil {
			return p, err
		}
		p.substitute = Op{Operation: OpReplaceArgs, Instruction: ex.Instruction,
			Value: key + "=${" + p.name + "}", Expect: ex.Expect}
		// Property 1: directly above its own use, never at the end of the stage.
		p.anchor = ex.Instruction
		if stage, ok := stageOf(f, ex.Instruction); ok {
			p.scope = fmt.Sprintf("stage %d", stage)
			p.scopeReason = fmt.Sprintf("the declaration went inside stage %d, directly above line %d. "+
				"Before the first FROM the stage could not see it without a re-declaration, and at the end "+
				"of the stage it would be declared after its own use", stage, in.StartLine+1)
		} else {
			p.scope = ScopeGlobal
			p.scopeReason = fmt.Sprintf("instruction %d sits before the first FROM, so its scope is the "+
				"global one and the declaration went directly above line %d", ex.Instruction, in.StartLine+1)
		}
	}

	if err := bareArgDefault(p.literal); err != nil {
		return p, err
	}
	return decideDeclaration(f, ex.Instruction, p)
}

// decideDeclaration answers the last question: is a declaration written at all,
// does it carry the default, and is what is already there compatible.
//
// The order matters. The scope being WRITTEN TO is asked first, because a stage
// that already declares the name correctly makes the global one irrelevant; the
// global one is asked second, and only for a stage, because that is the only
// case where a bare re-declaration is the right answer.
func decideDeclaration(f *dockerfile.File, target int, p argPlan) (argPlan, error) {
	scopeDecls := argsInScope(f, target, p.scope == ScopeGlobal)
	if have, ok := scopeDecls[p.name]; ok {
		if have.hasDefault && have.value == p.literal {
			// Idempotent, the same way story 9.3's `.env` no-op is: the
			// declaration is already exactly what would be written.
			p.alreadyDeclared = true
			return p, nil
		}
		what := fmt.Sprintf("to %q", have.value)
		if !have.hasDefault {
			what = "with no default at all, so a bare `docker build .` gets the empty string"
		}
		return p, fmt.Errorf("%w: line %d already declares `%s` in %s %s — and the value being moved is %q. "+
			"A second declaration would shadow the first and the build would use whichever came last. "+
			"Choose another name, or settle the two by hand",
			ErrArgConflict, have.line, p.name, p.scope, what, p.literal)
	}

	if p.scope != ScopeGlobal {
		if have, ok := argsInScope(f, target, true)[p.name]; ok {
			if !have.hasDefault || have.value != p.literal {
				what := fmt.Sprintf("to %q", have.value)
				if !have.hasDefault {
					what = "with no default"
				}
				return p, fmt.Errorf("%w: line %d declares `%s` globally %s, and the value being moved is %q. "+
					"Re-declaring it bare in %s would pull in the global value rather than this one, and giving "+
					"it a default here would shadow the global one — two answers that disagree",
					ErrArgConflict, have.line, p.name, what, p.literal, p.scope)
			}
			// Property 3: `ARG NAME` with no value is Docker's documented way to
			// pull the global default into a stage, and writing a default here
			// instead would shadow it.
			p.redeclared, p.argLine = true, "ARG "+p.name
			p.scopeReason += fmt.Sprintf(". Line %d already declares it globally with the same value, so "+
				"what went in is a bare `ARG %s`, which is how Docker pulls a global default into a stage",
				have.line, p.name)
			return p, nil
		}
	}
	p.declared, p.argLine = true, "ARG "+p.name+"="+p.literal
	return p, nil
}

// argDecl is one `ARG NAME[=default]` as the parser found it.
type argDecl struct {
	value      string
	hasDefault bool
	line       int // 1-based
}

// argsInScope collects the ARG declarations visible to the instruction at
// target: the global ones (before the first FROM) when global is true,
// otherwise the ones inside target's own stage.
//
// VISIBLE, which is positional and not merely same-stage. An ARG is in scope
// for the instructions BELOW it and no others — property 1 at the head of this
// file, read in the other direction — so a declaration at or after target says
// nothing about what target can see.
//
// Collecting the whole stage was a live defect. A stage that re-declares the
// same name further down with the SAME value made decideDeclaration report
// `alreadyDeclared`, so no declaration was written above the use, and
// argReadback — which correctly only looks above the use — then refused the
// whole operation with "nothing declares FOO above instruction N". The reader
// got a refusal whose stated reason had been manufactured by the step before
// it. The same collection also produced a spurious ErrArgConflict against a
// later declaration that could not shadow anything at target.
func argsInScope(f *dockerfile.File, target int, global bool) map[string]argDecl {
	out := map[string]argDecl{}
	firstFrom := f.Stages()[0]
	stage, inStage := stageOf(f, target)

	for i, in := range f.Instructions {
		if in.Kind != dockerfile.KindInstruction || in.Name != "ARG" {
			continue
		}
		// Positional scope. Harmless in the global branch — a global ARG is
		// before the first FROM and so before any target that has a stage — and
		// the whole correction in the stage branch.
		if i >= target {
			continue
		}
		if global {
			if i >= firstFrom {
				continue
			}
		} else {
			if s, ok := stageOf(f, i); !ok || !inStage || s != stage {
				continue
			}
		}
		for _, field := range strings.Fields(in.Args) {
			name, value, hasDefault := strings.Cut(field, "=")
			if validVarName(name) != nil {
				continue
			}
			// A later declaration wins, the same way a later `.env` line does.
			out[name] = argDecl{value: value, hasDefault: hasDefault, line: in.StartLine + 1}
		}
	}
	return out
}

// stageOf is the stage index the instruction at idx belongs to, or false for
// the preamble before the first FROM.
func stageOf(f *dockerfile.File, idx int) (int, bool) {
	for s := range f.Stages() {
		for _, i := range f.StageInstructions(s) {
			if i == idx {
				return s, true
			}
		}
	}
	return 0, false
}

// singleAssignment is the instruction body as one `KEY=value`, or the refusal.
//
// One assignment and no more: `RUN apt-get update && apt-get install -y curl`
// has several words and no single literal in it, and choosing one would be a
// guess. `ENV A=1 B=2` is refused for the same reason — the request names an
// instruction, not which of its two values.
func singleAssignment(in dockerfile.Instruction) (key, value string, err error) {
	key, rest, ok := strings.Cut(in.Args, "=")
	if !ok || validVarName(key) != nil {
		return "", "", fmt.Errorf("%w: instruction %s has no single `KEY=value` in it (%q). There is no one "+
			"literal there and choosing one would be a guess", ErrNoLiteral, in.Name, in.Args)
	}
	// The value is taken VERBATIM rather than through strings.Fields, because
	// `ENV G="hello world"` is one assignment whose value has a space in it —
	// and splitting on whitespace would report it as "no single KEY=value" and
	// send the reader the wrong refusal. The right refusal is that the value
	// cannot be a bare ARG default, and bareArgDefault gives it.
	//
	// A SECOND assignment is what is actually being excluded: `ENV A=1 B=2`
	// names an instruction rather than which of its two values, and choosing
	// one would be the guess this refuses.
	for _, field := range strings.Fields(rest)[1:] {
		if k, _, ok := strings.Cut(field, "="); ok && validVarName(k) == nil {
			return "", "", fmt.Errorf("%w: instruction %s assigns more than one name (%q). The request names "+
				"an instruction, not which of its values, and choosing one would be a guess",
				ErrNoLiteral, in.Name, in.Args)
		}
	}
	if strings.TrimSpace(rest) == "" {
		return "", "", fmt.Errorf("%w: %s= has no value to move", ErrNoLiteral, key)
	}
	return key, rest, nil
}

// splitImageTag separates `node:18` into `node` and `18`.
//
// A digest — `alpine@sha256:...` — has no tag, and the colon inside it is the
// digest algorithm's. A registry port — `reg:5000/node` — has a colon that is
// not a tag separator either, which is why the last colon must come after the
// last slash.
func splitImageTag(ref string) (repo, tag string, ok bool) {
	if ref == "" || strings.Contains(ref, "@") {
		return "", "", false
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || i < strings.LastIndex(ref, "/") || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// deriveArgName suggests a name for a FROM tag: `node` becomes NODE_VERSION.
//
// A suggestion and nothing more — the caller may always name it. It is derived
// from the image's last path segment because that is the only word in the line
// that says what the version belongs to; deriving it from the tag itself would
// name the variable after the value it is meant to replace.
func deriveArgName(repo string) string {
	seg := repo
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	base := deriveVarName(seg)
	if base == "" {
		return ""
	}
	return base + "_VERSION"
}

// bareArgDefault is the quoting decision, and it is a REFUSAL rather than a
// spelling.
//
// Story 9.3 decides `.env` quoting by readback because resolve.ParseDotEnv
// exists to read a `.env` line back. There is no second reader for an ARG
// default: the value goes to BuildKit's own parser and then, in many
// instructions, through a shell. Writing `ARG G="hello world"` would be
// inventing the grammar this check claims to verify, so a value that cannot
// stand bare is refused with the character named.
func bareArgDefault(v string) error {
	if v == "" {
		return fmt.Errorf("%w: it is empty, and `ARG NAME=` declares the empty string rather than nothing", ErrArgValue)
	}
	for _, r := range v {
		bad := r == ' ' || r == '\t' || r == '"' || r == '\'' || r == '#' ||
			r == '\\' || r == '$' || r == '`' || r < 0x20 || r == 0x7f
		if bad {
			return fmt.Errorf("%w: %q contains %q. An ARG default is read by BuildKit and then, in most "+
				"instructions, by a shell — two grammars this engine does not model — so it is refused rather "+
				"than quoted on a guess. Declare the ARG yourself and reference it", ErrArgValue, v, string(r))
		}
	}
	return nil
}

// argReadback parses the RESULT and asserts the property that makes this
// operation correct, on the bytes rather than on the plan that produced them.
//
// Three things, and each of them is a way to ship an ARG that does nothing:
//
//	the instruction that was edited now references ${name}
//	a declaration of name exists at a LOWER instruction index than that use
//	the value in scope at the use is EXACTLY the literal that was taken out
//
// and, for a FROM, that the declaration is below the first FROM — property 2,
// which no amount of "it parses" would catch.
//
// requireBeforeFirstFrom is passed rather than inferred so the check can be
// aimed at a hand-built buffer in a test. A guard exercised only through the
// code that is supposed to satisfy it is a guard nobody has seen fail.
func argReadback(out []byte, name, literal string, useIdx int, requireBeforeFirstFrom bool) error {
	f := dockerfile.Parse(out)
	if useIdx < 0 || useIdx >= len(f.Instructions) {
		return fmt.Errorf("%w: the instruction that was edited is not in the result", ErrWouldCorrupt)
	}
	use := f.Instructions[useIdx]
	ref := "${" + name + "}"
	if !strings.Contains(use.Args, ref) && !strings.Contains(use.ImageRef, ref) {
		return fmt.Errorf("%w: instruction %d does not reference %s after the edit", ErrWouldCorrupt, useIdx, ref)
	}

	effective, firstDecl, found := "", -1, false
	for i := 0; i < useIdx; i++ {
		in := f.Instructions[i]
		if in.Kind != dockerfile.KindInstruction || in.Name != "ARG" {
			continue
		}
		for _, field := range strings.Fields(in.Args) {
			key, value, hasDefault := strings.Cut(field, "=")
			if key != name {
				continue
			}
			found = true
			if firstDecl < 0 {
				firstDecl = i
			}
			if hasDefault {
				effective = value
			}
		}
	}
	if !found {
		return fmt.Errorf("%w: nothing declares `%s` above instruction %d, so it would expand to the empty "+
			"string with no error", ErrWouldCorrupt, name, useIdx)
	}
	if effective != literal {
		return fmt.Errorf("%w: `%s` is in scope as %q at instruction %d, and the literal taken out was %q",
			ErrWouldCorrupt, name, effective, useIdx, literal)
	}
	if requireBeforeFirstFrom {
		stages := f.Stages()
		if len(stages) == 0 || firstDecl > stages[0] {
			return fmt.Errorf("%w: `%s` is declared after the first FROM, and a FROM can only use an ARG "+
				"declared before it", ErrWouldCorrupt, name)
		}
	}
	return nil
}

// composeArgNote is DECISIONS.md 27 said to the reader: `build.args` is not
// wired, that is a decision rather than an omission, and here is what to write.
func composeArgNote(name string) string {
	return fmt.Sprintf("Nothing feeds `%s` from compose. `docker compose` passes build arguments only through "+
		"`build.args` — a `.env` never reaches an ARG — so to set it from the stack, add `%s: ${%s}` under the "+
		"service's `build.args:` yourself. Composure does not write that third file: which service builds this "+
		"Dockerfile is a resolution question rather than an edit one, and putting the default in two places is "+
		"two answers that can disagree (DECISIONS.md 27).", name, name, name)
}
