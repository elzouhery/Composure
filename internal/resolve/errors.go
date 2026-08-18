package resolve

import (
	"errors"
	"fmt"
)

// Sentinels. A file that cannot be resolved produces one of these and no
// model — never a partial one. A partial model is how a tool ends up confidently
// reporting a stack it only half read.
//
// These are errors, not findings. A configuration that resolves but is
// questionable — an undefined variable, a port collision — produces a model
// plus a finding. The two kinds never cross.
var (
	// ErrNotFound: the path does not exist or cannot be read.
	ErrNotFound = errors.New("compose file not found")
	// ErrEmptyFile: the file exists and has no content. An empty file is not
	// an empty project; it is far more likely to be a mistake.
	ErrEmptyFile = errors.New("compose file is empty")
	// ErrNotCompose: parses as YAML but is not a compose file.
	ErrNotCompose = errors.New("not a compose file")
	// ErrLegacyFormat: services are declared at the top level with no
	// `services:` key. That is Compose v1 format, which Compose V2 dropped —
	// or a fragment meant to be concatenated into a larger file, which several
	// projects generate at build time.
	//
	// It gets its own sentinel because the two failures need different advice.
	// "Not a compose file" sends someone looking for a typo; naming the legacy
	// format tells them what to actually do. 60 of 158 corpus files land here,
	// so this is the common refusal, not an edge case.
	// ErrMultiDocument: more than one YAML document in the file. Compose does
	// not define multi-document projects, and decoding only the first would
	// silently lose every service after the `---` — the confident-wrong-answer
	// failure this engine is prone to.
	ErrMultiDocument = errors.New("file contains more than one YAML document")
	// ErrDuplicateKey: the same key twice in one mapping. YAML's own spec calls
	// this an error and Compose rejects it. Accepting it means the model reports
	// the last value at the first key's position, disagreeing with the file
	// about both what it says and where it says it.
	ErrDuplicateKey = errors.New("duplicate key in mapping")
	// ErrTooDeep: nesting beyond maxDepth. Unbounded recursion on a deeply
	// nested document exhausts the stack, which kills the process rather than
	// raising a recoverable panic.
	// ErrComplexKey: a non-scalar mapping key (`? [a, b] : v`). Every complex
	// key would collapse to the empty string and silently overwrite the last.
	ErrComplexKey = errors.New("complex (non-scalar) mapping key")
	ErrTooDeep    = errors.New("document nesting is too deep")
	// ErrAliasCycle: an anchor that reaches itself, directly or through another
	// anchor. Expanding it recurses forever, and stack exhaustion is fatal in Go
	// — recover() cannot catch it — so the cycle is detected rather than survived.
	// No real compose file does this; a file that did would take the editor down
	// with it, so it is refused rather than half-expanded.
	ErrAliasCycle = errors.New("anchor references itself")
	// ErrAliasFanout: alias expansion exceeded maxAliasNodes. Anchors that
	// reference anchors expand 2^N, so a small file can name more nodes than
	// there is memory for. Refusing beats being killed by the OOM killer with no
	// message at all.
	ErrAliasFanout = errors.New("anchor expansion is too large")
	// ErrBadMergeValue: a `<<:` whose value is not a mapping, an alias to a
	// mapping, or a sequence of those. YAML defines nothing else, and silently
	// ignoring the directive would drop configuration the author meant to apply.
	ErrBadMergeValue = errors.New("merge key value is not a mapping or a sequence of mappings")
	// ErrUnknownAnchor: an alias to an anchor that was never defined. yaml.v3
	// rejects this while parsing, so reaching it means the parser changed
	// behaviour — refuse rather than resolve the alias to nothing.
	ErrUnknownAnchor = errors.New("alias references an undefined anchor")
	ErrLegacyFormat  = errors.New("services declared at the top level: Compose v1 format or a fragment, not a v2 compose file")

	// ErrBadInclude: an `include:` entry that names no file — not a path, not
	// a mapping with one. Silently skipping it would drop every service the
	// author meant to pull in, and report a smaller stack with no explanation.
	ErrBadInclude = errors.New("include entry does not name a file")
	// ErrIncludeCycle: files that include each other. Following it recurses
	// until the stack is gone, which Go cannot recover from — so the cycle is
	// detected and named. See IncludeCycleError for the chain.
	ErrIncludeCycle = errors.New("include cycle")
	// ErrExtendsCycle: services that extend each other, directly or through a
	// third. Same reasoning as the include cycle. See ExtendsError.
	ErrExtendsCycle = errors.New("extends cycle")
	// ErrExtendsTarget: an `extends:` naming a service that is not there, or a
	// declaration that is not a service name or a {service, file} mapping.
	// Resolving it to nothing would produce a service missing exactly the keys
	// the author moved into the base.
	ErrExtendsTarget = errors.New("extends target cannot be resolved")
	// ErrNoSuchPath: a config path that addresses nothing in the resolved
	// model. See PathError, which carries the closest paths that do exist.
	ErrNoSuchPath = errors.New("no such config path")
)

// ParseError reports a YAML document that could not be parsed, with the
// position the parser stopped at so a caller can put the cursor there.
type ParseError struct {
	File   string
	Line   int
	Column int
	Err    error
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d:%d: %v", e.File, e.Line, e.Column, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.File, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// FileError attaches a file path to a sentinel so callers can use errors.Is on
// the sentinel while still being told which file failed.
type FileError struct {
	File string
	Err  error
}

func (e *FileError) Error() string { return fmt.Sprintf("%s: %v", e.File, e.Err) }

func (e *FileError) Unwrap() error { return e.Err }
