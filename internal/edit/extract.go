package edit

// Moving a literal into a variable — story 9.3.
//
// `POSTGRES_PASSWORD: hunter2` becomes `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}`
// in the compose file and `POSTGRES_PASSWORD=hunter2` in the `.env`, created if
// it is not there.
//
// THE COMPOSE HALF IS NOT NEW. It is `replace_scalar`, gated since Epic 6, run
// through the ordinary `run` path with the ordinary validate and the ordinary
// diff. Nothing here computes an offset and nothing here splices. What IS new
// is that two files change, and that is the whole of this file.
//
// ATOMICITY, IN THREE PARTS, BECAUSE THERE IS NO CROSS-FILE ATOMIC RENAME.
//
//	1. Both new buffers are computed and BOTH VALIDATED before either file is
//	   opened for writing. The compose result re-parses (edit.validate); the
//	   `.env` result is read back with resolve.ParseDotEnv and the variable must
//	   return EXACTLY the original value. A failure here writes nothing at all.
//
//	2. Both temp files are created, written, synced and chmod'd BEFORE either
//	   rename. Every ordinary failure — no disk, no permission, read-only mount
//	   — lands here, with both files untouched and both temps removed.
//
//	3. The `.env` is renamed first and the compose file second. The only window
//	   left is between two rename syscalls, and the state it can leave is a
//	   `.env` carrying a variable nothing references: INERT, and converging,
//	   because re-running the operation finds the variable already present with
//	   the same value and treats that as a no-op rather than a conflict. If the
//	   second rename fails, the `.env` is rolled back — to its own bytes, or
//	   removed if this operation created it.
//
// The order is not arbitrary and the residue is named rather than claimed away.
// The other order leaves a compose file saying `${POSTGRES_PASSWORD}` with
// nothing to define it, which under `docker compose` is the EMPTY STRING — a
// database with a blank password, which is a different kind of bad day from a
// tool that refused.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/elzouhery/composure/internal/resolve"
)

// Sentinels. Every one of them means NEITHER file was touched.
var (
	// ErrVarName is a name Compose would not interpolate.
	ErrVarName = errors.New("that is not a variable name Compose interpolates")
	// ErrVarConflict is a name the `.env` already defines with a different
	// value. Refused rather than overwritten: the value in the `.env` is one
	// somebody configured, and a second line of the same name is a file whose
	// meaning depends on which parser reads it.
	ErrVarConflict = errors.New("that variable is already set to something else")
	// ErrVarValue is a value that cannot be written as one `.env` line that
	// reads back as itself.
	ErrVarValue = errors.New("that value cannot be written as one .env line")
	// ErrAlreadyInterpolated is a value that already comes from a variable.
	// There is no literal to move, and moving `${DB_PASSWORD}` would define
	// `${DB_PASSWORD}` as `${DB_PASSWORD}`.
	ErrAlreadyInterpolated = errors.New("that value already comes from a variable")
	// ErrNoLiteral is a path with no literal scalar to move — a mapping, a
	// sequence, a key the file does not have, an empty value.
	ErrNoLiteral = errors.New("there is no literal value at that path to move")
	// ErrTwoFileWrite is the residual window of step 3 above: the `.env`
	// landed and the compose file did not. It is an OPERATIONAL failure rather
	// than a refusal — the reader's next move is to fix the disk, not the
	// request — and the `.env` has been rolled back before it is returned.
	ErrTwoFileWrite = errors.New("the .env was written and the compose file could not be; the .env has been rolled back")
)

// renameFile is os.Rename behind a variable so a test can make step 3 fail.
// The window between two renames cannot be reached any other way, and a
// failure path with no test is a failure path nobody has run.
var renameFile = os.Rename

// Extract is the reader's request: move the literal at At into a variable.
type Extract struct {
	// File is the compose file holding the value. One file, as every write
	// request in this package is.
	//
	// The SECOND file is the `.env`, and it is DERIVED BY DEFAULT — the `.env`
	// in this file's own directory. It is not sealed: EnvFile below overrides
	// it, `composure extract -env` sets that, and so does `env_file` on the wire.
	//
	// That override is deliberate and is DECISIONS.md 25's own wording ("`-env`
	// overrides it for a caller who knows better; the default does not guess").
	// This comment used to say a request "cannot span two arbitrary files",
	// which contradicted the field three lines below it and both callers — a
	// reader reconciling the two had no way to know which was the contract.
	// The contract is: derived unless named, and named is allowed.
	File string
	// At is the config path of the value, in resolve.Path's canonical form.
	At string
	// Name is the variable's name. Empty derives one from the key.
	Name string
	// EnvFile overrides which `.env` is written. Empty is the `.env` in the
	// directory of File, which is the ONLY file `docker compose` consults for
	// interpolation — see DECISIONS.md 25 for why an env_file is not an option.
	EnvFile string
	// Expect is the COMPOSE half's staleness assertion — story 9.6, and an
	// ordinary AD-19 byte-range Expect, because the compose half is an ordinary
	// replace_scalar and its target is an ordinary range.
	//
	// Optional, and that is decided rather than inherited: a request carrying
	// no expectation is applied against the files as they stand, which is right
	// for a CLI invocation and is what the extension shipped against. A
	// silently-mandatory field would break every client that already works.
	Expect *Expect `json:"expect,omitempty"`
	// ExpectEnv is the `.env` half's, and it is deliberately NOT an Expect.
	//
	// The `.env` edit has no range to assert: it is one line appended at the
	// end of a file. A byte-range field copied from the single-file contract
	// would compare an offset that means nothing here and PASS — the exact
	// shape of a check that cannot fail. What is meaningful is a statement
	// about the VARIABLE. See ExpectVar.
	ExpectEnv *ExpectVar `json:"expect_env,omitempty"`
}

// ExpectVar is the `.env` half of the staleness contract: what the file said
// about the NAME when the reader previewed, not what bytes were where.
//
// Two states and no third. `Defined` false means "nothing set this name";
// `Defined` true means "it was set, to Value". Value is ignored when Defined is
// false rather than overloaded to mean absence, because "" is a value a `.env`
// can legitimately hold and the two must not collide.
type ExpectVar struct {
	Defined bool   `json:"defined"`
	Value   string `json:"value,omitempty"`
}

// ExtractResult is what a preview or an apply produced. Both halves, always:
// a two-file operation that reported one diff would be a lie about the half
// the reader cannot see.
type ExtractResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Compose is the ordinary edit result for the compose file.
	Compose *Result `json:"compose"`
	// EnvFile is the path written, EnvDiff its unified diff, EnvLine the line
	// added. EnvUnchanged means the variable was already there with this value.
	EnvFile      string `json:"env_file"`
	EnvDiff      string `json:"env_diff,omitempty"`
	EnvLine      string `json:"env_line,omitempty"`
	EnvCreated   bool   `json:"env_created"`
	EnvUnchanged bool   `json:"env_unchanged"`
	// EnvExpect is the `.env` half AS IT STANDS NOW: the assertion a later
	// apply sends back. Story 9.6, and it is an ADDED field rather than a
	// changed one so that a client rendering this result keeps rendering it.
	//
	// The compose half needs no equivalent — Compose.Ops[0].Range and .Before
	// are already the Expect a client records, and have been since AD-19.
	EnvExpect *ExpectVar `json:"env_expect,omitempty"`
	Written   bool       `json:"written"`
}

// PreviewExtract computes both diffs and writes neither file.
func PreviewExtract(ex Extract) (*ExtractResult, error) { return runExtract(ex, false) }

// ApplyExtract performs the move.
func ApplyExtract(ex Extract) (*ExtractResult, error) { return runExtract(ex, true) }

func runExtract(ex Extract, write bool) (*ExtractResult, error) {
	if strings.TrimSpace(ex.File) == "" {
		return nil, fmt.Errorf("edit: no file named")
	}
	src, err := os.ReadFile(ex.File)
	if err != nil {
		return nil, fmt.Errorf("edit: %w", err)
	}
	// The grammar of a request is the FILE's, as requestGrammar established.
	// A Dockerfile gets the reason rather than a path error, because "ARG, and
	// it is not built" is the answer and "path x not found" is not.
	if FileGrammar(src) != GrammarYAML {
		// The ARG paragraph is for a DOCKERFILE. It was reached on
		// `RootIsMapping` alone, so a YAML sequence, a bare scalar and an empty
		// file were all told "the Dockerfile equivalent of this is an `ARG`" —
		// a confident wrong answer about which file the reader is holding.
		//
		// The test for one is a FROM: that is what makes a file buildable, what
		// scopes an ARG, and the thing story 9.4 is about. A file with none is
		// not a Dockerfile for this purpose whatever it is called.
		if FileGrammar(src) == GrammarDockerfile {
			return nil, fmt.Errorf("%w: %s is not a compose file. The Dockerfile equivalent of this is an `ARG`, "+
				"which is build-time, has to be re-declared after every FROM, and is never fed by a `.env` — "+
				"`docker compose` passes build arguments only through `build.args`. That is a different operation "+
				"with a different failure surface, and it is `composure extract -instruction N` on this file "+
				"(story 9.4), never this one",
				ErrWrongGrammar, filepath.Base(ex.File))
		}
		return nil, fmt.Errorf("%w: %s is not a compose file — this operation moves a value out of a compose "+
			"mapping, and the root of this file is not a mapping", ErrWrongGrammar, filepath.Base(ex.File))
	}

	literal, envKey, err := literalAt(src, ex.At)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(ex.Name)
	if name == "" {
		if envKey != "" {
			name = envKey
		} else {
			name = deriveVarName(leafOf(ex.At))
		}
	}
	if err := validVarName(name); err != nil {
		return nil, err
	}

	// The compose half: the ordinary replace_scalar, previewed through the
	// ordinary path so its validate and its diff are the ordinary ones. In the
	// list form the variable's own NAME is kept, because `- ${VAR}` is an entry
	// with no `=`, which Compose reads as pass-through-by-name.
	replacement := "${" + name + "}"
	if envKey != "" {
		replacement = envKey + "=" + replacement
	}
	composeRes, err := Preview(Request{File: ex.File, Ops: []Op{
		{Operation: OpReplaceScalar, At: ex.At, Value: replacement, Expect: ex.Expect},
	}})
	if err != nil {
		// Story 9.6's compose half, said the way the `.env` half says it.
		//
		// `run` produces a refusal for ONE file, so it names a config path and
		// nothing else — which is the whole answer for `apply` and half an
		// answer here. checkEnvExpect below names its file and adds "the
		// compose file is not the half that moved"; this said neither, so a
		// reader holding a refusal from the one operation that writes TWO files
		// could not tell which file had moved, nor whether the `.env` had
		// already been written. It has not: nothing is written until both
		// halves are computed, and this is the sentence that says so.
		if errors.Is(err, ErrStaleRange) {
			return nil, fmt.Errorf("%w — that is %s, and the `.env` beside it is not the half that moved: "+
				"it was not touched and neither file was written. Look at the compose file again",
				err, filepath.Base(ex.File))
		}
		return nil, err
	}

	envPath := ex.EnvFile
	if strings.TrimSpace(envPath) == "" {
		envPath = filepath.Join(filepath.Dir(ex.File), ".env")
	}
	// A symlinked `.env` is written THROUGH. See throughSymlink for why, and for
	// why it is done HERE rather than in stageFile, which the compose path and
	// every other write in this package share.
	envPath = throughSymlink(envPath)
	envSrc, readErr := os.ReadFile(envPath)
	created := false
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("edit: %w", readErr)
		}
		envSrc, created = nil, true
	}
	// The `.env` half of the staleness contract, checked BEFORE assignEnv so
	// that a name somebody else set comes back as staleness rather than as
	// ErrVarConflict. The two look identical on disk and the reader's next move
	// differs: a conflict is answered by choosing another name, a stale preview
	// by looking again.
	envNow, envDefined := resolve.ParseDotEnv(envSrc)[name]
	if err := checkEnvExpect(ex.ExpectEnv, envPath, name, literal, envNow, envDefined); err != nil {
		return nil, err
	}

	assign, err := assignEnv(envSrc, envPath, name, literal)
	if err != nil {
		return nil, err
	}
	// Step 1's second validation: the line that will be written must read back
	// as exactly the value that was taken out. Anything else silently changes
	// the stack's behaviour while reporting success.
	if !assign.Unchanged {
		if back := resolve.ParseDotEnv(assign.Bytes)[name]; back != literal {
			return nil, fmt.Errorf("%w: the line this would write reads back as %q rather than %q",
				ErrVarValue, back, literal)
		}
	}

	res := &ExtractResult{
		Name:         name,
		Value:        literal,
		Compose:      composeRes,
		EnvFile:      envPath,
		EnvLine:      assign.Line,
		EnvCreated:   created && !assign.Unchanged,
		EnvUnchanged: assign.Unchanged,
		EnvExpect:    &ExpectVar{Defined: envDefined, Value: envNow},
	}
	if !assign.Unchanged {
		d := Unified(filepath.Base(envPath), envSrc, assign.Bytes)
		res.EnvDiff = d.Text
	}
	if !write {
		return res, nil
	}

	if assign.Unchanged {
		// One file after all. The ordinary single-file write, with its own
		// atomic rename, and no two-file window to reason about.
		if err := writeFile(ex.File, composeRes.Bytes); err != nil {
			return nil, fmt.Errorf("edit: %w", err)
		}
		res.Written, res.Compose.Written = true, true
		return res, nil
	}
	if err := writeBoth(envPath, assign.Bytes, created, ex.File, composeRes.Bytes); err != nil {
		return nil, err
	}
	res.Written, res.Compose.Written = true, true
	return res, nil
}

// checkEnvExpect is story 9.6's `.env` half, and the carve-out in the middle of
// it is the whole reason this was a story rather than a copied field.
//
// THREE STATES PASS and one refuses:
//
//	the name is still undefined and was undefined at preview
//	the name still holds exactly the value it held at preview
//	the name is now defined AS THE LITERAL BEING MOVED, having been undefined
//	at preview — DECISIONS.md 25 step 3's residue, which is this operation's
//	OWN half-landed write and converges to a no-op on the re-run
//
// The third is the one a naive check gets wrong. The `.env` did change since
// the preview, so an equality test refuses — and it refuses the re-run that was
// designed to fix the window, which is the property that made the window
// survivable in the first place. It is narrow on purpose: only the literal this
// very request is moving qualifies, so somebody else's value at the same name
// is still stale.
func checkEnvExpect(want *ExpectVar, envPath, name, literal, have string, defined bool) error {
	if want == nil {
		return nil
	}
	if defined == want.Defined && (!defined || have == want.Value) {
		return nil
	}
	if !want.Defined && defined && have == literal {
		return nil
	}
	was := "was not set at all"
	if want.Defined {
		was = fmt.Sprintf("was set to %q", want.Value)
	}
	now := "is not set at all"
	if defined {
		now = fmt.Sprintf("is set to %q", have)
	}
	return fmt.Errorf("%w: %s changed underneath this edit — %s %s there now and %s when this was previewed. "+
		"The compose file is not the half that moved. Look at it again rather than choosing another name",
		ErrStaleRange, envPath, name, now, was)
}

// writeBoth is steps 2 and 3 of the atomicity rule at the head of this file.
func writeBoth(envPath string, envData []byte, envCreated bool, composePath string, composeData []byte) error {
	envBefore, err := os.ReadFile(envPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("edit: %w", err)
	}

	// Step 2. Both temps, complete on disk, before either rename.
	envTmp, err := stageFile(envPath, envData)
	if err != nil {
		return fmt.Errorf("edit: staging %s: %w", envPath, err)
	}
	composeTmp, err := stageFile(composePath, composeData)
	if err != nil {
		_ = os.Remove(envTmp)
		return fmt.Errorf("edit: staging %s: %w", composePath, err)
	}

	// Step 3. The .env first, so the only reachable partial state is inert.
	if err := renameFile(envTmp, envPath); err != nil {
		_ = os.Remove(envTmp)
		_ = os.Remove(composeTmp)
		return fmt.Errorf("edit: writing %s: %w", envPath, err)
	}
	if err := renameFile(composeTmp, composePath); err != nil {
		_ = os.Remove(composeTmp)
		rollback := rollbackEnv(envPath, envBefore, envCreated)
		if rollback != nil {
			return fmt.Errorf("%w: %v — AND THE ROLLBACK ALSO FAILED (%v), so %s now defines a variable "+
				"the compose file does not reference. It is inert; remove it or re-run this operation",
				ErrTwoFileWrite, err, rollback, envPath)
		}
		return fmt.Errorf("%w: %v", ErrTwoFileWrite, err)
	}
	return nil
}

// rollbackEnv puts the .env back: removed if this operation created it,
// restored to its own bytes otherwise.
func rollbackEnv(path string, before []byte, created bool) error {
	if created {
		return os.Remove(path)
	}
	tmp, err := stageFile(path, before)
	if err != nil {
		return err
	}
	if err := renameFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// literalAt is the value at path and, for a list-form `environment:` entry, the
// variable name written inside it.
//
// It reads with yaml.v3 — the parser the rest of the core reads with — and asks
// two questions the byte range cannot answer: what the scalar DECODES to (a
// quoted `"a\tb"` is not the bytes between the quotes), and whether it already
// carries a variable reference.
func literalAt(src []byte, at string) (value, envKey string, err error) {
	a := Classify(src, at)
	if !a.Editable {
		if refusal := refusalFor(a); refusal != nil {
			return "", "", refusal
		}
		detail := a.Detail
		if detail == "" {
			detail = "there is nothing at that path"
		}
		return "", "", fmt.Errorf("%w: %s", ErrNoLiteral, detail)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil || len(doc.Content) == 0 {
		return "", "", fmt.Errorf("%w: the file does not parse", ErrNoLiteral)
	}
	node := doc.Content[0]
	for _, seg := range resolve.ParsePath(at) {
		next, _, ok := childOf(node, seg)
		if !ok {
			return "", "", fmt.Errorf("%w: %s is not in this file", ErrNoLiteral, at)
		}
		node = next
	}
	raw := node.Value

	// The list form: the scalar is the whole `NAME=value`. Detected by the
	// SHAPE of the entry and not by the key above it, because `command:` and
	// `labels:` also hold `k=v` entries — and getting it wrong in the other
	// direction is the harmless one: an entry with no `=` is treated whole.
	if key, rest, ok := strings.Cut(raw, "="); ok && validVarName(key) == nil && isListEntry(at) {
		envKey, raw = key, rest
	}

	if strings.TrimSpace(raw) == "" {
		return "", "", fmt.Errorf("%w: %s has no value to move", ErrNoLiteral, at)
	}
	if interpolated(raw) {
		return "", "", fmt.Errorf("%w: %s already reads %q. There is no literal here to move, and moving a "+
			"reference would define the variable as itself", ErrAlreadyInterpolated, at, raw)
	}
	return raw, envKey, nil
}

// isListEntry reports whether the path's last segment is a sequence index.
func isListEntry(at string) bool {
	segs := resolve.ParsePath(at)
	if len(segs) == 0 {
		return false
	}
	last := segs[len(segs)-1]
	if last == "" {
		return false
	}
	for i := 0; i < len(last); i++ {
		if last[i] < '0' || last[i] > '9' {
			return false
		}
	}
	return true
}

// interpolated reports whether s carries a Compose variable reference.
//
// `$$` is a LITERAL dollar in Compose's own grammar and is skipped rather than
// counted, so `password$$1` is a literal and is moved.
func interpolated(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 >= len(s) {
			return false
		}
		switch c := s[i+1]; {
		case c == '$':
			i++ // an escaped dollar: skip both
		case c == '{':
			return true
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
			return true
		}
	}
	return false
}

func leafOf(at string) string {
	segs := resolve.ParsePath(at)
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// throughSymlink resolves a `.env` that is a symlink to the file it points at,
// so the variable lands where the stack actually reads it.
//
// Why this exists. Every write in this package stages a temp file beside its
// target and renames over it, which is what makes a write atomic. Renaming over
// a symlink REPLACES THE LINK: the destination keeps its old contents and the
// link is gone. For a compose file that is a curiosity. For a `.env` it is a
// silent failure of the kind rule 6 forbids — `proj/.env -> ../shared.env` is
// an ordinary way to share one secrets file across several stacks, and a reader
// who moved a password into it would be told it was written while the file the
// stack reads still held the old value, and the sharing was quietly undone.
//
// Why writing THROUGH rather than refusing. A link is a deliberate act by
// whoever set the project up, and following it is what they asked for. Refusing
// would be safe but useless: the reader cannot act on it, because the thing to
// change is the project's layout, not the edit.
//
// Why HERE and not in stageFile. stageFile is shared by the compose path and
// every other write in this package. Making it follow links would change the
// behaviour of writes nobody has audited for it, on a much larger blast radius,
// to fix a problem that has only ever been reachable on this one path. The
// narrow fix is the honest one; if the compose path ever needs it, that is its
// own change with its own evidence.
//
// A broken link — one pointing at a file that does not exist yet — is left
// alone: EvalSymlinks fails on it, and the caller then creates the `.env` at
// the link's own path, which is the same thing it would have done without a
// link at all.
func throughSymlink(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		// No `.env` yet, or it cannot be stat'd. Either way there is no link to
		// follow, and the caller's own os.ReadFile reports anything that matters.
		return path
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path
	}
	// Only the FINAL component is followed, one hop at a time. EvalSymlinks
	// would resolve every parent too, and on macOS /var is itself a link to
	// /private/var — so a caller who passed a path under /var would get a
	// different string back for a `.env` that is not a link at all, and every
	// path this function returns would stop matching the one the caller holds.
	const maxHops = 16
	for i := 0; i < maxHops; i++ {
		target, err := os.Readlink(path)
		if err != nil {
			// A dangling link. Write at the link's own path, as if it were
			// absent — which is what the caller does when there is no .env.
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = filepath.Clean(target)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return path
		}
	}
	// A loop, or a chain deeper than anything a project legitimately has.
	// Refusing here would need a sentinel the caller must classify; returning
	// the last path lets the ordinary write fail with the OS's own error.
	return path
}
