// Package edit is the write path: the one place in this codebase that puts
// bytes back on disk.
//
// Everything above it — the CLI, the JSON-RPC server, the extension — describes
// an edit as data and hands it here. Everything below it is the splice engines
// in internal/strategy and internal/dockerfile, which are not touched: this
// package locates, refuses, diffs and writes, and it performs no edit of its
// own. That division is why `make gate` still measures the thing the product
// ships. If a fidelity number moves, the cause is in an engine, not here.
//
// Four rules shape it, and each is a story acceptance criterion:
//
//	Preview never writes.        Preview and Apply run the identical code path
//	                             and differ in one boolean. A preview whose diff
//	                             is not the diff the write produces is a lie the
//	                             reader cannot check.
//
//	A stale range is discarded.  An operation staged against a byte range that
//	                             has since moved is REFUSED, never rebased
//	                             (AD-19). Rebasing means guessing what the
//	                             reader meant about a file they have since
//	                             changed, and a wrong guess writes damage.
//
//	Refusal beats corruption.    ErrFlowStyle and ErrMultiLine come back
//	                             untouched from the engines, and the result is
//	                             re-parsed before it is written. An editor that
//	                             emits an unparseable file is worse than one
//	                             that says no, because the damage surfaces later
//	                             in someone else's terminal.
//
//	Nothing is normalised.       The bytes written are the source bytes with one
//	                             span replaced. Line endings, the BOM, quoting
//	                             style, key order, comments and blank lines are
//	                             untouched by construction, not by care.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/elzouhery/composure/internal/dockerfile"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// Operation names an edit the engines can already perform. The set is closed on
// purpose: a request is something the splice engine can execute, never prose it
// has to interpret. These strings are the same ones diagnose.Fix uses, so a
// described fix is directly executable here.
type Operation string

const (
	// OpReplaceScalar overwrites the scalar at Path with Value. This is the
	// two-line diff R4.1 is about.
	OpReplaceScalar Operation = "replace_scalar"
	// OpInsertKey adds `Key: Value` as a child of the mapping at Path. It is
	// what completes story 5.2 — clicking an unset key stages this.
	OpInsertKey Operation = "insert_key"
	// OpDeleteKey removes the key at Path with its subtree and attached
	// comments.
	OpDeleteKey Operation = "delete_key"
	// OpInsertSequenceEntry appends `- Value` to the block sequence at Path.
	// R4.2's "insert list item" (story 7.5): the operation that lets a service
	// the reader has just added publish a port or join a network.
	//
	// It is deliberately NOT in the diagnose.Fix constants
	// (internal/diagnose/diagnose.go). Those strings exist so that a described
	// fix is directly executable, and no rule describes a fix that appends a
	// sequence entry — every current one replaces a scalar, deletes a key or
	// inserts one. Adding a fourth constant nothing emits would claim a
	// capability the rules do not have; the day a rule needs it, the string is
	// this one.
	OpInsertSequenceEntry Operation = "insert_sequence_entry"
	// OpSetComment writes the comment at (At, Where) — story 9.1. It replaces
	// the comment that is there or inserts one where there is none, which is
	// one operation rather than two because the reader's gesture is the same
	// and the engine's answer to "where does it go" is the same function.
	//
	// Where is `above` or `trailing` and there is no third position: a comment
	// that belongs to no key has no path to be addressed by, and a line number
	// is an address that moves the instant anything above it is edited.
	OpSetComment Operation = "set_comment"
	// OpDeleteComment removes the comment at (At, Where). It REFUSES when
	// there is none rather than succeeding at nothing — see strategy.ErrNoComment.
	OpDeleteComment Operation = "delete_comment"
	// OpSetBaseImage replaces the image reference of the FROM at Stage,
	// preserving --platform, the AS clause, keyword casing and any trailing
	// comment (R7.2).
	OpSetBaseImage Operation = "set_base_image"
	// OpReplaceArgs rewrites the body of the single-line instruction at
	// Instruction, keeping its name and flags. A multi-line instruction is
	// refused rather than reflowed (R7.4).
	OpReplaceArgs Operation = "replace_args"
	// OpInsertInstruction adds Value as an instruction at the end of the build
	// stage at Stage (story 7.6) — the Dockerfile answer to OpInsertKey, and
	// what the `Available here` list stages when the reader presses Enter.
	//
	// Position is the caller's: order is semantic in this grammar, so the
	// operation names the STAGE and the engine appends after that stage's last
	// instruction. It is deliberately not "after instruction N": an index the
	// webview computed would be a second answer to where a stage ends.
	OpInsertInstruction Operation = "insert_instruction"
	// OpInsertInstructionBefore adds Value as an instruction directly ABOVE the
	// instruction at Instruction, above its attached comment block — story 9.4.
	//
	// It is a second placement rather than a flag on OpInsertInstruction, and
	// the reason is the whole of 9.4: 7.6 appends after a stage's LAST
	// instruction, and for an `ARG` that is the one position guaranteed to be
	// wrong — an ARG used before it is declared expands to the empty string
	// with no error, so "at the end of the stage" is a declaration after its
	// own use. The two share the splice arithmetic and differ only in which
	// line they land on.
	//
	// It names an INSTRUCTION rather than a stage, because "above this
	// instruction" is exactly the position, and there is no stage-level answer
	// to it that is not a guess about which line the reader meant.
	OpInsertInstructionBefore Operation = "insert_instruction_before"
	// OpInsertStage appends a build stage — `FROM Value` with an optional
	// `AS Key` — after the last instruction of the file (story 7.7). That is
	// the `+ add stage` control the design has shown since it was agreed.
	OpInsertStage Operation = "insert_stage"
)

// Grammar reports which engine an operation belongs to. The two engines are
// separate and stay separate; nothing here unifies them.
func (o Operation) Grammar() string {
	switch o {
	case OpSetBaseImage, OpReplaceArgs, OpInsertInstruction, OpInsertInstructionBefore, OpInsertStage:
		return "dockerfile"
	default:
		return "yaml"
	}
}

// Sentinel errors. Every one of them means "nothing was written", and callers
// distinguish them because the reader's next move differs: a refusal is
// something they can work around, a stale range is something they have to
// restage, and a path that does not resolve is a bug in the caller.
var (
	// ErrStaleRange is an operation whose recorded byte range no longer
	// matches the file. AD-19: discard, never rebase.
	ErrStaleRange = errors.New("the file changed since this edit was staged; the staged range no longer matches")
	// ErrNoOps is a request with nothing in it.
	ErrNoOps = errors.New("no operations to apply")
	// ErrNoChange is a request that resolved cleanly and produced identical
	// bytes. It is refused rather than written: rewriting a file with its own
	// contents dirties an editor buffer and touches an mtime for nothing.
	ErrNoChange = errors.New("the edit would not change the file")
	// ErrWouldCorrupt is a result the parser will not accept. It can only fire
	// if an engine has a defect, which is exactly why it is checked: this
	// engine does not crash, it returns a confident wrong answer.
	ErrWouldCorrupt = errors.New("the result would not parse; refusing to write it")
	// ErrUnknownOperation is an operation name outside the closed set.
	ErrUnknownOperation = errors.New("unknown operation")
	// ErrMixedGrammar is a request whose operations do not all belong to the
	// same engine. One request is one file, and one file is one grammar: a
	// request holding a YAML operation and a Dockerfile operation cannot be
	// validated against either without lying about the other.
	ErrMixedGrammar = errors.New("the operations in this request belong to different grammars")
	// ErrWrongGrammar is an operation aimed at a file the other engine owns —
	// `insert_stage` on a compose file. It appended `FROM alpine` to the
	// document, reported "1 line added" and exited 0, and the damage surfaced
	// the next time anything tried to resolve the file. Refusing is CLAUDE.md
	// rule 6; the grammar of a request is the FILE's, never operation 0's.
	ErrWrongGrammar = errors.New("that operation belongs to the other grammar")
)

// Expect is the byte range an operation was staged against, as it stood in the
// file at the moment the reader asked for the edit.
//
// It is the whole of the staleness check. On apply the range is recomputed and
// compared; if it has moved, or the bytes inside it are not the bytes that were
// there, the operation is refused. Text is compared as well as the offsets
// because a same-length change at the same offset moves nothing and still means
// the reader is editing something else.
//
// "As it stood in the file" means as it stood in the buffer this operation was
// PREVIEWED against — for operation N in a request, the file with operations
// 0..N-1 already applied. A staged set is previewed as a set (the extension's
// `stageAll`), so operation N's range is only meaningful relative to its
// predecessors, and `run` checks it there. Recording one against the raw file
// while the preview reported it against a spliced buffer is what makes an Expect
// a lie about a document nobody has.
type Expect struct {
	Start int `json:"start"`
	End   int `json:"end"`
	// Text is the bytes at [Start, End) when the edit was staged. Empty is
	// meaningful for an insertion, whose range is empty by definition, so a
	// caller that does not want the text compared omits the whole Expect.
	Text string `json:"text"`
}

// assertsRange reports whether this expectation makes a claim about WHERE the
// target was, as opposed to only what it said.
//
// A caller that recorded a preview knows both and asserts both. A caller that
// only knows the text — a script asserting "replace this only if it still reads
// nginx" — cannot compute a byte offset without doing the locate itself, which
// is the work it is asking this engine to do. Negative offsets say so, and are
// the symmetric case to the empty Text the field above already documents.
func (e *Expect) assertsRange() bool { return e.Start >= 0 && e.End >= 0 }

// Op is one edit, as data.
type Op struct {
	Operation Operation `json:"operation"`
	// At is the config path for a YAML operation, in resolve.Path's canonical
	// string form: `services.web.image`. For an insert it names the MAPPING the
	// key is added to, not the key.
	At string `json:"at,omitempty"`
	// Key is the key an insert adds, or — for OpInsertStage — the new stage's
	// AS name. Empty means no AS clause, and nothing is invented.
	Key string `json:"key,omitempty"`
	// Value is the new scalar, the inserted value, the new image reference, or
	// the whole text of an inserted instruction.
	Value string `json:"value,omitempty"`
	// Where is the comment position for OpSetComment and OpDeleteComment:
	// strategy.CommentAbove or strategy.CommentTrailing. It is a field of its
	// own rather than a reuse of Key because a position is not a key, and the
	// two would be indistinguishable in a request log the day one is wrong.
	Where string `json:"where,omitempty"`
	// Stage is the zero-based index over FROM instructions, for OpSetBaseImage
	// and for OpInsertInstruction — which stage the new instruction joins.
	Stage int `json:"stage,omitempty"`
	// Instruction is the zero-based index over all instructions, for
	// OpReplaceArgs.
	Instruction int `json:"instruction,omitempty"`
	// Expect is the staleness assertion. Optional; without it the edit is
	// applied against wherever the path resolves now, which is right for a CLI
	// invocation and wrong for a staged UI edit.
	Expect *Expect `json:"expect,omitempty"`
}

// Request is a set of operations against ONE file, applied in order.
//
// One file, because a diff the reader is asked to approve has to name the file
// it touches, and an operation set spanning three files produces three diffs
// and one button. In order, because two edits to the same document are not
// commutative in general.
type Request struct {
	File string `json:"file"`
	Ops  []Op   `json:"ops"`
	// Write turns a preview into an apply. It is the ONLY difference between
	// the two, so a preview cannot show a diff the write does not produce.
	Write bool `json:"write"`
}

// ByteRange is the half-open span an operation touches, [Start, End). An insert
// has Start == End: it touches no existing byte.
type ByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
	// Line is the 1-based line the range begins on.
	Line int `json:"line"`
}

// OpResult is one operation's outcome: where it landed and what was there.
type OpResult struct {
	Operation Operation `json:"operation"`
	Path      string    `json:"path,omitempty"`
	Range     ByteRange `json:"range"`
	// Before is the bytes the range held, so a caller can record an Expect for
	// a later apply without re-reading the file itself.
	Before string `json:"before"`
	// Describe is one sentence a human can read instead of the operation name.
	Describe string `json:"describe"`
}

// Result is what a preview or an apply produces.
type Result struct {
	File    string     `json:"file"`
	Ops     []OpResult `json:"ops"`
	Diff    string     `json:"diff"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
	Changed int        `json:"changed_lines"`
	Written bool       `json:"written"`
	// Bytes is the resulting document. Populated for a preview so a caller can
	// verify it independently; a caller that only wants the diff ignores it.
	Bytes []byte `json:"-"`
}

// Preview computes the diff an edit would produce and writes nothing.
func Preview(req Request) (*Result, error) {
	req.Write = false
	return run(req)
}

// Apply performs the edit and writes the file.
func Apply(req Request) (*Result, error) {
	req.Write = true
	return run(req)
}

func run(req Request) (*Result, error) {
	if strings.TrimSpace(req.File) == "" {
		return nil, fmt.Errorf("edit: no file named")
	}
	if len(req.Ops) == 0 {
		return nil, ErrNoOps
	}
	src, err := os.ReadFile(req.File)
	if err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	grammar, err := requestGrammar(req, src)
	if err != nil {
		return nil, err
	}

	// Apply, re-locating each operation against the buffer the previous one
	// produced. Re-locating rather than arithmetic on the original offsets is
	// what makes a set of edits safe: an insert two lines above moves every
	// byte after it, and adjusting offsets by hand is the same class of guess
	// AD-19 forbids.
	//
	// Staleness is checked HERE, one operation at a time, against that same
	// evolving buffer — not in a pass over the original bytes before the first
	// splice. Two reasons, and the first is a bug the second explains:
	//
	//	A dependent operation cannot be located in the original file at all.
	//	`add service` plans `insert_key PolicyServer under services` followed by
	//	`insert_key image under services.PolicyServer`, and the second path does
	//	not exist until the first has run. Locating it against the on-disk bytes
	//	reports `path segment "PolicyServer" not found` and refuses an edit that
	//	is perfectly well formed.
	//
	//	An Expect is recorded against the buffer its operation was previewed
	//	against, which for operation N is the buffer operations 0..N-1 produced.
	//	Comparing it to the original file is comparing two different documents;
	//	when it did not fail outright it would report a spurious stale range as
	//	soon as an earlier operation shifted a later one's offsets.
	//
	// Nothing is written before the loop, `validate` and the diff have all
	// completed, so a refusal on operation N still leaves the file untouched:
	// atomicity comes from writing last, never from checking first.
	out := append([]byte(nil), src...)
	results := make([]OpResult, 0, len(req.Ops))
	for i, op := range req.Ops {
		rng, err := locate(out, op)
		if err != nil {
			return nil, fmt.Errorf("edit: operation %d: %w", i, err)
		}
		before := ""
		if rng.Start <= rng.End && rng.End <= len(out) {
			before = string(out[rng.Start:rng.End])
		}
		if op.Expect != nil {
			if op.Expect.assertsRange() && (rng.Start != op.Expect.Start || rng.End != op.Expect.End) {
				return nil, fmt.Errorf("%w: %s is now at bytes %d-%d, was staged at %d-%d",
					ErrStaleRange, describeTarget(op), rng.Start, rng.End, op.Expect.Start, op.Expect.End)
			}
			if op.Expect.Text != "" && before != op.Expect.Text {
				return nil, fmt.Errorf("%w: %s now reads %q, was staged as %q",
					ErrStaleRange, describeTarget(op), before, op.Expect.Text)
			}
		}
		next, err := perform(out, op)
		if err != nil {
			return nil, fmt.Errorf("edit: operation %d: %w", i, err)
		}
		results = append(results, OpResult{
			Operation: op.Operation,
			Path:      op.At,
			Range:     rng,
			Before:    before,
			Describe:  describeOp(op),
		})
		out = next
	}

	if err := validate(grammar, src, out); err != nil {
		return nil, err
	}

	diff := Unified(filepath.Base(req.File), src, out)
	res := &Result{
		File:    req.File,
		Ops:     results,
		Diff:    diff.Text,
		Added:   diff.Added,
		Removed: diff.Removed,
		Changed: diff.Changed(),
		Bytes:   out,
	}
	if diff.Empty() {
		return res, ErrNoChange
	}
	if !req.Write {
		return res, nil
	}
	if err := writeFile(req.File, out); err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	res.Written = true
	return res, nil
}

// locate returns the byte range an operation would touch, WITHOUT performing
// it. It is derived through the same functions that perform the edit (AD-14):
// the range a preview reports has to be the range the write touches, and the
// only way to guarantee that is to derive both from one implementation.
func locate(src []byte, op Op) (ByteRange, error) {
	switch op.Operation {
	case OpReplaceScalar:
		// Before the engine, not after it. The engine's answer for four of
		// these shapes was a splice at a position whose bytes are not the value
		// the reader is looking at — see internal/edit/inherited.go, which
		// names all four and what each one wrote. Classifying first is what
		// turns a confident wrong answer into a refusal with a sentence in it.
		if err := refusalFor(Classify(src, op.At)); err != nil {
			return ByteRange{}, err
		}
		start, end, err := strategy.ScalarRange(src, resolve.ParsePath(op.At))
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: start, End: end, Line: lineAt(src, start)}, nil
	case OpInsertKey:
		// The key is checked HERE rather than only in edit.Plan, because Plan is
		// one caller of this operation and not the gate for it. A key beginning
		// with `#` is the case that makes this load-bearing: validate's re-parse
		// passes, the file is valid YAML, and the key the reader typed silently
		// became a comment. bareKey's readback is what sees that.
		if err := bareKey(op.Key, "key"); err != nil {
			return ByteRange{}, err
		}
		offset, _, err := strategy.InsertionPoint(src, resolve.ParsePath(op.At))
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: offset, End: offset, Line: lineAt(src, offset)}, nil
	case OpInsertSequenceEntry:
		offset, _, err := strategy.SequenceInsertionPoint(src, resolve.ParsePath(op.At))
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: offset, End: offset, Line: lineAt(src, offset)}, nil
	case OpSetComment:
		// Through the engine's own function, refusals included: a preview that
		// accepted what the write refuses is a lie the reader cannot check.
		start, end, _, err := strategy.CommentRange(src, resolve.ParsePath(op.At), op.Where)
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: start, End: end, Line: lineAt(src, start)}, nil
	case OpDeleteComment:
		start, end, err := strategy.CommentDeleteRange(src, resolve.ParsePath(op.At), op.Where)
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: start, End: end, Line: lineAt(src, start)}, nil
	case OpDeleteKey:
		start, end, err := strategy.DeleteRange(src, resolve.ParsePath(op.At))
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: start, End: end, Line: lineAt(src, start)}, nil
	case OpSetBaseImage:
		in, err := fromInstruction(src, op.Stage)
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: in.ImageStart, End: in.ImageEnd, Line: in.StartLine + 1}, nil
	case OpInsertInstruction:
		// The insertion point comes from the engine's own anchor function —
		// the one InsertInstruction splices at — so the range a preview shows
		// is the range the write touches (AD-14). Nothing here counts bytes.
		offset, err := dockerfile.Parse(src).InstructionInsertionPoint(op.Stage)
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: offset, End: offset, Line: lineAt(src, offset)}, nil
	case OpInsertInstructionBefore:
		// The engine's own anchor again (AD-14): InstructionStartPoint is the
		// offset InsertBefore splices at, so a preview cannot report a position
		// the write does not use.
		offset, err := dockerfile.Parse(src).InstructionStartPoint(op.Instruction)
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: offset, End: offset, Line: lineAt(src, offset)}, nil
	case OpInsertStage:
		offset, err := dockerfile.Parse(src).StageInsertionPoint()
		if err != nil {
			return ByteRange{}, err
		}
		return ByteRange{Start: offset, End: offset, Line: lineAt(src, offset)}, nil
	case OpReplaceArgs:
		f := dockerfile.Parse(src)
		if op.Instruction < 0 || op.Instruction >= len(f.Instructions) {
			return ByteRange{}, fmt.Errorf("instruction %d out of range (%d instructions)", op.Instruction, len(f.Instructions))
		}
		in := f.Instructions[op.Instruction]
		if in.EndLine != in.StartLine {
			return ByteRange{}, fmt.Errorf("%w: instruction %d spans %d lines",
				dockerfile.ErrMultiLine, op.Instruction, in.EndLine-in.StartLine+1)
		}
		return ByteRange{Start: in.StartByte, End: in.EndByte, Line: in.StartLine + 1}, nil
	}
	return ByteRange{}, fmt.Errorf("%w %q", ErrUnknownOperation, op.Operation)
}

// perform hands the operation to the engine that owns it. Every branch is one
// call into internal/strategy or internal/dockerfile and no byte arithmetic of
// its own — this package must never grow a second splice implementation.
func perform(src []byte, op Op) ([]byte, error) {
	switch op.Operation {
	case OpReplaceScalar:
		return strategy.Splice{}.Edit(src, resolve.ParsePath(op.At), op.Value)
	case OpInsertKey:
		if strings.TrimSpace(op.Key) == "" {
			return nil, fmt.Errorf("insert_key needs a key")
		}
		return strategy.Splice{}.InsertKey(src, resolve.ParsePath(op.At), op.Key, op.Value)
	case OpInsertSequenceEntry:
		return strategy.Splice{}.InsertSequenceEntry(src, resolve.ParsePath(op.At), op.Value)
	case OpSetComment:
		return strategy.Splice{}.SetComment(src, resolve.ParsePath(op.At), op.Where, op.Value)
	case OpDeleteComment:
		return strategy.Splice{}.DeleteComment(src, resolve.ParsePath(op.At), op.Where)
	case OpDeleteKey:
		return strategy.Splice{}.DeleteKey(src, resolve.ParsePath(op.At))
	case OpSetBaseImage:
		return dockerfile.Parse(src).SetBaseImage(op.Stage, op.Value)
	case OpReplaceArgs:
		return dockerfile.Parse(src).ReplaceArgs(op.Instruction, op.Value)
	case OpInsertInstruction:
		return dockerfile.Parse(src).InsertInstruction(op.Stage, op.Value)
	case OpInsertInstructionBefore:
		return dockerfile.Parse(src).InsertBefore(op.Instruction, op.Value)
	case OpInsertStage:
		return dockerfile.Parse(src).InsertStage(op.Value, op.Key)
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownOperation, op.Operation)
}

func fromInstruction(src []byte, stage int) (dockerfile.Instruction, error) {
	f := dockerfile.Parse(src)
	stages := f.Stages()
	if stage < 0 || stage >= len(stages) {
		return dockerfile.Instruction{}, fmt.Errorf("stage %d out of range (%d FROM instructions)", stage, len(stages))
	}
	in := f.Instructions[stages[stage]]
	if in.ImageRef == "" {
		return dockerfile.Instruction{}, fmt.Errorf("stage %d: could not locate the image reference", stage)
	}
	return in, nil
}

// requestGrammar answers which engine this request belongs to, and refuses when
// the answer disagrees with the FILE.
//
// It replaces `req.Ops[0].Operation.Grammar()`, which was the whole of the
// answer and was wrong twice over. Operation 0's grammar says nothing about
// operations 1..N, and neither says anything about the file: `composure apply -op
// insert_stage -value alpine compose.yaml` appended `FROM alpine` to a compose
// document, validated it as a Dockerfile — where the only assertion is "the
// result holds instructions", which an appended FROM satisfies — and exited 0
// with "1 line added". The YAML re-parse never ran on a YAML file.
//
// The YAML direction needs no equivalent check: a YAML operation on a Dockerfile
// cannot find its path and is refused by locate before anything is spliced.
func requestGrammar(req Request, src []byte) (string, error) {
	grammar := req.Ops[0].Operation.Grammar()
	for i, op := range req.Ops {
		if g := op.Operation.Grammar(); g != grammar {
			return "", fmt.Errorf("%w: operation 0 is a %s operation and operation %d is a %s one",
				ErrMixedGrammar, grammar, i, g)
		}
	}
	if grammar == "dockerfile" && strategy.RootIsMapping(src) {
		return "", fmt.Errorf("%w: %s parses as a YAML mapping, and %q is a Dockerfile operation",
			ErrWrongGrammar, filepath.Base(req.File), req.Ops[0].Operation)
	}
	return grammar, nil
}

// validate re-parses the result before it is allowed near the filesystem.
//
// It is belt and braces over engines that are already measured, and it stays
// because the characteristic failure here is not a crash but a confident wrong
// answer. A splice that produces unparseable YAML costs nothing to detect and
// everything to ship.
func validate(grammar string, src, out []byte) error {
	switch grammar {
	case "yaml":
		if _, err := (strategy.Splice{}).Identity(out); err != nil {
			return fmt.Errorf("%w: %v", ErrWouldCorrupt, err)
		}
		// And with the OTHER parser, because the two do not agree and the
		// disagreement is where a confident wrong answer lives. goccy accepts an
		// implicit key over YAML's 1024-character limit; yaml.v3 — which is what
		// internal/resolve reads the file with — does not. A 1030-character
		// service name therefore passed Identity, was written, and made the file
		// unreadable to the rest of this product.
		//
		// The assertion is comparative, never absolute: an edit must not TAKE
		// AWAY yaml.v3-parseability. A file that already fails yaml.v3 is not
		// this operation's fault and refusing to edit it would be a new refusal
		// nobody asked for.
		if parsesAsYAML(src) && !parsesAsYAML(out) {
			return fmt.Errorf("%w: the result no longer parses as YAML 1.2 (%v)",
				ErrWouldCorrupt, yamlParseError(out))
		}
	case "dockerfile":
		// A Dockerfile has no parse failure to speak of — every line is
		// something — so the assertion that means anything is that the file
		// still holds instructions at all.
		if len(dockerfile.Parse(out).Instructions) == 0 && len(out) > 0 {
			return fmt.Errorf("%w: the result holds no instructions", ErrWouldCorrupt)
		}
	}
	return nil
}

// parsesAsYAML reports whether every document in src is readable by yaml.v3,
// the parser internal/resolve uses.
func parsesAsYAML(src []byte) bool { return yamlParseError(src) == nil }

func yamlParseError(src []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// writeFile replaces the file's contents, keeping its mode.
//
// Write to a sibling temp file and rename: a crash halfway through a direct
// write leaves a truncated compose file, which is the single worst thing this
// product could do to someone. Rename within the same directory is atomic on
// every filesystem this runs on.
func writeFile(path string, data []byte) error {
	name, err := stageFile(path, data)
	if err != nil {
		return err
	}
	defer os.Remove(name) // no-op once the rename has succeeded
	return renameFile(name, path)
}

// stageFile writes data to a sibling temp file, complete and synced, and
// returns its name. It performs no rename: the caller decides when — and, for
// story 9.3's two-file write, in what ORDER — the swaps happen.
//
// Splitting it out is what makes the two-file write's step 2 possible: every
// ordinary failure (no disk, no permission, read-only mount) happens here, with
// nothing renamed and both originals intact.
func stageFile(path string, data []byte) (string, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".composure-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func lineAt(src []byte, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	line := 1
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

func describeTarget(op Op) string {
	switch op.Operation {
	case OpSetComment, OpDeleteComment:
		return fmt.Sprintf("the %s comment on %s", op.Where, op.At)
	case OpSetBaseImage:
		return fmt.Sprintf("the FROM of stage %d", op.Stage)
	case OpReplaceArgs:
		return fmt.Sprintf("instruction %d", op.Instruction)
	case OpInsertInstruction:
		return fmt.Sprintf("the end of stage %d", op.Stage)
	case OpInsertInstructionBefore:
		return fmt.Sprintf("the line above instruction %d", op.Instruction)
	case OpInsertStage:
		return "the end of the file"
	}
	if op.At == "" {
		return "the target"
	}
	return op.At
}

func describeOp(op Op) string {
	switch op.Operation {
	case OpReplaceScalar:
		return fmt.Sprintf("set %s to %s", op.At, op.Value)
	case OpInsertKey:
		if op.Value == "" {
			return fmt.Sprintf("add %s under %s", op.Key, op.At)
		}
		return fmt.Sprintf("add %s: %s under %s", op.Key, op.Value, op.At)
	case OpInsertSequenceEntry:
		return fmt.Sprintf("add %s to %s", op.Value, op.At)
	case OpSetComment:
		return fmt.Sprintf("write the %s comment on %s", op.Where, op.At)
	case OpDeleteComment:
		return fmt.Sprintf("remove the %s comment on %s", op.Where, op.At)
	case OpDeleteKey:
		return fmt.Sprintf("remove %s", op.At)
	case OpSetBaseImage:
		return fmt.Sprintf("set the base image of stage %d to %s", op.Stage, op.Value)
	case OpReplaceArgs:
		return fmt.Sprintf("rewrite instruction %d to %s", op.Instruction, op.Value)
	case OpInsertInstruction:
		return fmt.Sprintf("add %s to stage %d", op.Value, op.Stage)
	case OpInsertInstructionBefore:
		return fmt.Sprintf("add %s above instruction %d", op.Value, op.Instruction)
	case OpInsertStage:
		if op.Key == "" {
			return fmt.Sprintf("add a stage from %s", op.Value)
		}
		return fmt.Sprintf("add a stage %s from %s", op.Key, op.Value)
	}
	return string(op.Operation)
}

// Refused reports whether err is a safe refusal — an edit the engine declined
// to perform because performing it would damage the file — as opposed to an
// operational failure. The distinction is what the caller shows the reader:
// a refusal reverts the field and explains itself, a failure is a fault.
func Refused(err error) bool {
	return errors.Is(err, strategy.ErrFlowStyle) ||
		errors.Is(err, strategy.ErrNoRootMapping) ||
		errors.Is(err, strategy.ErrNotASequence) ||
		errors.Is(err, strategy.ErrNullEntry) ||
		// Story 9.2. An index the list does not have. Declined, not broken:
		// nothing was written and the reader's next move is a position that
		// exists.
		errors.Is(err, strategy.ErrEntryIndex) ||
		// Story 9.1. Three declined comment operations: there is nothing at
		// that position, the text would not be written as a comment, or the
		// engine cannot say where the line's value ends. None of them wrote.
		errors.Is(err, strategy.ErrNoComment) ||
		errors.Is(err, strategy.ErrCommentText) ||
		errors.Is(err, strategy.ErrCommentTarget) ||
		// Story 9.3. Five declined moves; in every one of them NEITHER file
		// was touched. ErrTwoFileWrite is deliberately NOT here: it is an
		// operational failure, and the reader's next move is the disk.
		errors.Is(err, ErrVarName) ||
		errors.Is(err, ErrVarConflict) ||
		errors.Is(err, ErrVarValue) ||
		errors.Is(err, ErrAlreadyInterpolated) ||
		errors.Is(err, ErrNoLiteral) ||
		// Story 9.4. Three declined moves into a build argument; every one of
		// them left the Dockerfile byte-identical.
		errors.Is(err, ErrArgValue) ||
		errors.Is(err, ErrArgConflict) ||
		errors.Is(err, ErrNoTag) ||
		// Story 6.5. The parser's position and the file's bytes disagree, so
		// the engine declined to splice at a guessed offset. Nothing was
		// written and the reader can act on it — a refusal, not a fault.
		errors.Is(err, strategy.ErrPositionMismatch) ||
		errors.Is(err, dockerfile.ErrMultiLine) ||
		errors.Is(err, dockerfile.ErrNoInsertionPoint) ||
		errors.Is(err, dockerfile.ErrInsertText) ||
		errors.Is(err, dockerfile.ErrStageName) ||
		errors.Is(err, strategy.ErrEntryText) ||
		errors.Is(err, ErrWouldCorrupt) ||
		// A request aimed at the wrong grammar, or spanning two. Both are
		// declined requests rather than faults: nothing was written, and the
		// caller's next move is to send the operation the file's engine owns.
		errors.Is(err, ErrWrongGrammar) ||
		errors.Is(err, ErrMixedGrammar) ||
		errors.Is(err, ErrNoChange) ||
		// Stories 7.3 and 7.4. All four are declined requests, not faults: the
		// reader can act on every one of them without leaving the panel.
		errors.Is(err, ErrDuplicateName) ||
		errors.Is(err, ErrNeedsQuoting) ||
		errors.Is(err, ErrNoImage) ||
		errors.Is(err, ErrNameTooLong) ||
		errors.Is(err, ErrNoName) ||
		// The four shapes of "the value is not written where it is read".
		// Every one of them leaves the file untouched, and three of them tell
		// the reader where the bytes ARE, so all four are refusals rather than
		// faults.
		errors.Is(err, ErrInherited) ||
		errors.Is(err, ErrMergedMapping) ||
		errors.Is(err, ErrAliasValue) ||
		errors.Is(err, ErrAnchoredValue) ||
		errors.Is(err, ErrBlockScalar)
}

// Reason is a stable slug naming why an edit did not happen, for a client that
// has to branch on it. Empty for an error that is not a refusal or a stale
// range.
func Reason(err error) string {
	switch {
	case errors.Is(err, ErrStaleRange):
		return "stale-range"
	case errors.Is(err, strategy.ErrFlowStyle):
		return "flow-style"
	case errors.Is(err, strategy.ErrNoRootMapping):
		return "no-root-mapping"
	case errors.Is(err, strategy.ErrNotASequence):
		return "not-a-sequence"
	case errors.Is(err, strategy.ErrNullEntry):
		return "null-entry"
	case errors.Is(err, strategy.ErrEntryIndex):
		return ReasonEntryIndex
	case errors.Is(err, strategy.ErrNoComment):
		return "no-comment"
	case errors.Is(err, strategy.ErrCommentText):
		return "comment-text"
	case errors.Is(err, strategy.ErrCommentTarget):
		return "comment-target"
	case errors.Is(err, ErrVarName):
		return "var-name"
	case errors.Is(err, ErrVarConflict):
		return "var-conflict"
	case errors.Is(err, ErrVarValue):
		return "var-value"
	case errors.Is(err, ErrAlreadyInterpolated):
		return "already-interpolated"
	case errors.Is(err, ErrNoLiteral):
		return "no-literal"
	case errors.Is(err, ErrArgValue):
		return "arg-value"
	case errors.Is(err, ErrArgConflict):
		return "arg-conflict"
	case errors.Is(err, ErrNoTag):
		return "no-tag"
	case errors.Is(err, strategy.ErrEntryText):
		return "entry-text"
	case errors.Is(err, strategy.ErrPositionMismatch):
		return "position-mismatch"
	case errors.Is(err, dockerfile.ErrMultiLine):
		return "multi-line"
	case errors.Is(err, dockerfile.ErrNoInsertionPoint):
		return "no-insertion-point"
	case errors.Is(err, dockerfile.ErrInsertText):
		return "insert-text"
	case errors.Is(err, dockerfile.ErrStageName):
		return "stage-name"
	case errors.Is(err, ErrWouldCorrupt):
		return "would-corrupt"
	case errors.Is(err, ErrNoChange):
		return "no-change"
	case errors.Is(err, ErrDuplicateName):
		return "duplicate-name"
	case errors.Is(err, ErrNeedsQuoting):
		return "needs-quoting"
	case errors.Is(err, ErrNoImage):
		return "no-image"
	case errors.Is(err, ErrNoName):
		return "no-name"
	case errors.Is(err, ErrNameTooLong):
		return "name-too-long"
	case errors.Is(err, ErrWrongGrammar):
		return "wrong-grammar"
	case errors.Is(err, ErrMixedGrammar):
		return "mixed-grammar"
	case errors.Is(err, ErrInherited):
		return ReasonInherited
	case errors.Is(err, ErrMergedMapping):
		return ReasonInheritedNested
	case errors.Is(err, ErrAliasValue):
		return ReasonAlias
	case errors.Is(err, ErrAnchoredValue):
		return ReasonAnchor
	case errors.Is(err, ErrBlockScalar):
		return ReasonBlockScalar
	}
	return ""
}
