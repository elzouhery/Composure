package edit

// Writing a `.env` — the second half of story 9.3.
//
// It is a splice, not a rewrite, for the same reason everything else here is:
// a `.env` holds comments, blank lines and inline comments on values, and a
// re-emit from a parsed map would lose all three while reporting success. The
// only edit this file performs is ONE APPENDED LINE.
//
// Reading is `resolve.ParseDotEnv`, which is the one reader of that format in
// this codebase. A second parser here would be a second precedence order, and
// the two would disagree the day somebody quoted a value.

import (
	"fmt"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// envAssignment is the result of planning one `.env` change.
type envAssignment struct {
	// Bytes is the whole new file. Equal to the source when Unchanged.
	Bytes []byte
	// Unchanged is true when the variable is already defined with exactly this
	// value. Not a conflict and not a write: the operation is idempotent, which
	// is also what makes the window between the two renames converge.
	Unchanged bool
	// Line is the new line, without its ending, for a diff.
	Line string
}

// assignEnv plans `name=value` into src.
//
// Three answers, and each of them is a decision recorded in DECISIONS.md 25:
// already defined with the same value is a no-op, already defined with a
// different one is a REFUSAL naming both, and undefined is one appended line.
func assignEnv(src []byte, file, name, value string) (envAssignment, error) {
	if err := validVarName(name); err != nil {
		return envAssignment{}, err
	}
	existing := resolve.ParseDotEnv(src)
	if have, ok := existing[name]; ok {
		if have == value {
			return envAssignment{Bytes: src, Unchanged: true}, nil
		}
		return envAssignment{}, fmt.Errorf(
			"%w: %s already sets %s at %s:%d, to %q — and the value being moved is %q. "+
				"Overwriting would destroy a value somebody configured, and a second line of the same name "+
				"is a file whose meaning depends on which parser reads it. Choose another name, or settle the two by hand",
			ErrVarConflict, file, name, file, envLineOf(src, name), have, value)
	}

	rendered, err := renderEnvValue(name, value)
	if err != nil {
		return envAssignment{}, err
	}
	line := name + "=" + rendered
	ending := envEnding(src)

	var b strings.Builder
	b.Write(src)
	if len(src) > 0 && !strings.HasSuffix(string(src), "\n") {
		// The last line carries no ending of its own. One is needed to separate
		// it from the new line, and it is the file's own kind.
		b.WriteString(ending)
	}
	b.WriteString(line)
	// The file's trailing newline, or its absence, is preserved: a file that
	// ended without one still does.
	if len(src) == 0 || strings.HasSuffix(string(src), "\n") {
		b.WriteString(ending)
	}
	return envAssignment{Bytes: []byte(b.String()), Line: line}, nil
}

// envEnding is the line ending the file already uses. A file with no line break
// at all gets "\n", which is the only defensible answer when there is nothing
// local to copy — the same rule strategy.endingFor applies in the other format.
func envEnding(src []byte) string {
	if strings.Contains(string(src), "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// envLineOf is the 1-based line a name is defined on, for the refusal to name.
// Zero when it cannot be found, which cannot happen for a name ParseDotEnv
// returned and is handled rather than asserted.
//
// It reports the LAST definition, because that is the one `resolve.ParseDotEnv`
// returns — each line overwrites the map entry before it, as `docker compose`
// does. Reporting the FIRST, which this did, quoted the value from one line and
// the number of another, and sent the reader to settle the two by hand starting
// at the line that is not in effect.
//
// A commented-out definition below the live one — `# DUPLICATE=old`, which is
// how people actually edit these files — needs NO skip of its own, and one was
// written here and then removed: the `#` stays attached to the key, so `k` is
// `"# DUPLICATE"` and cannot equal the name. A comment-skip branch here is a
// check that cannot fail, and `e50-doubly-defined.env` carries such a line so
// that the property is asserted rather than assumed.
func envLineOf(src []byte, name string) int {
	found := 0
	for i, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == name {
			found = i + 1
		}
	}
	return found
}

// EnvLine is the `.env` line that would define name as value, or the typed
// refusal saying it cannot be written as one.
//
// It is exported for exactly one caller: `internal/diagnose`, whose credential
// rule offers `composure extract` as a remedy (story 9.5) and must not offer it
// for a value the operation would refuse. The alternative was a second
// implementation of the same readback property in the diagnostic, and the head
// of this file already says what a second answer to a `.env` question costs —
// the two disagree the day somebody quotes a value.
//
// It writes nothing and it is pure. `internal/diagnose` calls it for the
// ANSWER, not for the action: a Fix is data, never an operation performed
// (internal/diagnose/fix.go), and offering a remedy stages nothing.
func EnvLine(name, value string) (string, error) {
	if err := validVarName(name); err != nil {
		return "", err
	}
	rendered, err := renderEnvValue(name, value)
	if err != nil {
		return "", err
	}
	return name + "=" + rendered, nil
}

// renderEnvValue is the right-hand side of the new line: the bytes that make
// `resolve.ParseDotEnv` return exactly value.
//
// The test is a READBACK, not a character blocklist — the same property
// `edit.bare` and `strategy.entryText` apply in their own grammars. Three
// candidate spellings are tried in order of least imposition, and the first
// that reads back is used. If none does, the operation refuses rather than
// writing a line whose meaning differs from the value it was given.
func renderEnvValue(name, value string) (string, error) {
	for _, candidate := range []string{
		value,
		`"` + value + `"`,
		`'` + value + `'`,
	} {
		if !strings.ContainsAny(candidate, "\n\r") &&
			resolve.ParseDotEnv([]byte(name + "=" + candidate + "\n"))[name] == value {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %q cannot be written as one `.env` line that reads back as itself — "+
		"a `.env` value is one line and its quoting has no escapes. Put it in the file yourself and reference it",
		ErrVarValue, value)
}

// validVarName is the shape `docker compose` will interpolate. Anything else is
// not a refusal about taste: `${a-b}` is not a variable reference at all, so a
// name outside this shape produces a compose file that means something other
// than what the reader asked for.
func validVarName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: no name was given and none could be derived from the key. Choose one", ErrVarName)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("%w: %q is not a name Compose interpolates. A name is a letter or `_` "+
				"followed by letters, digits or `_` — `${%s}` would be read as text, not as a variable",
				ErrVarName, name, name)
		}
	}
	return nil
}

// deriveVarName suggests a name from the key the value sits under.
//
// It is a suggestion and nothing more: the caller may always name it, and a
// derivation that does not produce a valid name refuses rather than inventing
// one, because a variable called `_` helps nobody.
func deriveVarName(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}
