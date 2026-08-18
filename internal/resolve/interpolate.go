package resolve

// Story 1.3 — interpolation, the way Compose does it.
//
// Two rules from AD-11 shape everything here:
//
//   - Interpolation runs PER FILE, at load, BEFORE any merge. A value that
//     arrives at the merge is already a literal, and it carries the Origin of
//     the file that interpolated it. Interpolating after the merge would report
//     the winning file's position for a substitution the losing file performed.
//   - An undefined variable resolves to empty and produces a FINDING, not an
//     error. `${VAR:?message}` is the single exception: that form exists in
//     order to fail, so it fails, carrying the author's message.
//
// The text as written is never lost. Value.Raw() returns the pre-interpolation
// scalar, so the buffer, the inspector and the credential rule can all still
// see `${DB_PASSWORD}` where the file says `${DB_PASSWORD}`. Nothing here
// touches bytes on disk.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrRequiredVariable is the sentinel behind `${VAR:?msg}` with VAR unset. It
// is an error and not a finding because the author asked for exactly that.
var ErrRequiredVariable = errors.New("required variable is not set")

// InterpolationError reports a `${VAR:?message}` whose variable is not set. It
// carries the author's message verbatim: that message is the whole reason the
// form exists, and paraphrasing it would throw away the only explanation
// anybody has.
type InterpolationError struct {
	File     string
	Line     int
	Column   int
	Variable string
	Message  string
}

func (e *InterpolationError) Error() string {
	msg := e.Message
	if strings.TrimSpace(msg) == "" {
		msg = "required variable is not set"
	}
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d:%d: required variable %s is not set: %s", e.File, e.Line, e.Column, e.Variable, msg)
	}
	return fmt.Sprintf("%s: required variable %s is not set: %s", e.File, e.Variable, msg)
}

func (e *InterpolationError) Unwrap() error { return ErrRequiredVariable }

// Finding is something the resolver noticed that does not stop it resolving.
//
// AD-13: errors and findings are different kinds and never cross. A file that
// cannot be parsed produces an error and no model; a file that references a
// variable nobody defines produces a model plus one of these.
//
// It is deliberately a small, flat record rather than the richer type in
// internal/diagnose: resolve imports nothing else under internal/, so the two
// cannot share a type, and duplicating three strings is cheaper than inverting
// that dependency. internal/diagnose reads this list rather than re-scanning
// the file for `${`.
type Finding struct {
	// Kind is a stable identifier: "undefined-variable" or
	// "invalid-interpolation".
	Kind string `json:"kind"`
	// Variable is the name referenced, where the finding is about one.
	Variable string `json:"variable,omitempty"`
	// Path addresses the value the reference appeared in.
	Path Path `json:"path"`
	// Origin is where that value is written. On a FindingFormMismatch it is the
	// LATER declaration — the one that won.
	Origin  Origin `json:"origin"`
	Message string `json:"message"`

	// Dropped names the entries the earlier declaration set that are not in the
	// resolved value, in the order that declaration wrote them. Only
	// FindingFormMismatch sets it.
	//
	// It is a field rather than something a consumer re-derives because by the
	// time anyone reads a finding the earlier value is gone from the model —
	// that is the whole complaint — so "which keys did I lose" is answerable
	// here and nowhere else.
	Dropped []string `json:"dropped,omitempty"`
	// Displaced is where the earlier declaration is written: the file whose
	// entries were lost. Only FindingFormMismatch sets it.
	Displaced Origin `json:"displaced,omitzero"`
}

const (
	// FindingUndefinedVariable: referenced, defined nowhere, interpolated to
	// the empty string.
	FindingUndefinedVariable = "undefined-variable"
	// FindingInvalidInterpolation: a `${` the Compose grammar does not define —
	// unterminated, empty, or an operator that is not one of the six. The text
	// is left exactly as written rather than guessed at.
	FindingInvalidInterpolation = "invalid-interpolation"
	// FindingFormMismatch: one key written as a sequence in one file and as a
	// mapping in another. The later declaration replaces the earlier whole, so
	// entries only the earlier form set are gone — Compose would have kept
	// them. Raised so the loss is stated rather than silent.
	FindingFormMismatch = "merge-form-mismatch"
	// FindingRepeatedListItem: the same entry declared in two files for one of
	// the attributes Compose concatenates and then validates for uniqueness —
	// `security_opt`, `group_add`, `volumes_from`. Compose refuses such a
	// project ("items at 0 and 1 are equal"), so the merge keeps the repeat
	// rather than collapsing it, and says so here. Deduplicating instead would
	// resolve a document that cannot be run.
	FindingRepeatedListItem = "repeated-list-item"
	// FindingEmptyProfileName: a `profiles:` entry that resolved to the empty
	// string, usually an unset variable. The service is in a profile nobody can
	// name, so it is neither always-active nor startable by any `--profile`.
	FindingEmptyProfileName = "empty-profile-name"
)

// varEnv is the variable environment one file is interpolated against, as a
// stack of layers. Highest-precedence layer first.
//
// Compose's own precedence is host environment over `.env`. R1.5 additionally
// requires `env_file` entries to be a source, which Compose itself does not do
// for interpolation — `env_file` feeds the container, not the template. The two
// are reconciled by putting env_file underneath both, so a project that behaves
// identically under Compose still behaves identically here, and the extra
// source only ever fills in a name that would otherwise have been empty. A
// value that only an env_file could supply is recorded on the Project as an
// env-file-sourced name, so the divergence is visible rather than silent.
type varEnv struct {
	layers []envLayer
	// fromEnvFile collects names that resolved only because an env_file
	// supplied them. Compose would have left these empty.
	fromEnvFile map[string]bool
}

type envLayer struct {
	name string // "environment", ".env", "env_file:<path>", "explicit"
	vals map[string]string
	// envFile marks a layer Compose would not consult for interpolation.
	envFile bool
}

func (e *varEnv) lookup(name string) (string, bool) {
	for _, l := range e.layers {
		if v, ok := l.vals[name]; ok {
			if l.envFile {
				if e.fromEnvFile == nil {
					e.fromEnvFile = map[string]bool{}
				}
				e.fromEnvFile[name] = true
			}
			return v, true
		}
	}
	return "", false
}

// hostEnv snapshots the process environment.
func hostEnv() map[string]string {
	out := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// buildEnv assembles the environment one file is interpolated against.
//
// overlay is an explicit environment supplied by the caller and wins over
// everything, including the host: it is how a test states its inputs and how a
// future `--env-file` flag will be wired without a second precedence order.
func buildEnv(dir string, overlay map[string]string, envFiles []string, useHost bool) *varEnv {
	e := &varEnv{}
	if len(overlay) > 0 {
		cp := make(map[string]string, len(overlay))
		for k, v := range overlay {
			cp[k] = v
		}
		e.layers = append(e.layers, envLayer{name: "explicit", vals: cp})
	}
	if useHost {
		e.layers = append(e.layers, envLayer{name: "environment", vals: hostEnv()})
	}
	if dir != "" {
		if src, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
			e.layers = append(e.layers, envLayer{name: ".env", vals: ParseDotEnv(src)})
		}
	}
	// env_file layers, lowest of all. Later files in a service's list override
	// earlier ones, which is Compose's own rule for the container environment,
	// so the layers are pushed in reverse: the first layer consulted is the
	// last file declared.
	for i := len(envFiles) - 1; i >= 0; i-- {
		p := envFiles[i]
		src, err := os.ReadFile(p)
		if err != nil {
			continue // a missing env_file is the diagnostics layer's business
		}
		e.layers = append(e.layers, envLayer{name: "env_file:" + p, vals: ParseDotEnv(src), envFile: true})
	}
	return e
}

// ParseDotEnv reads the subset of `.env` syntax Compose defines: KEY=value one
// per line, `#` comments, optional surrounding quotes, and an `export ` prefix
// tolerated because half the world writes one.
//
// It is exported because it is the ONE reader of that format in the codebase.
// A second parser elsewhere is a second precedence order, and the two disagree
// on the day someone quotes a value.
func ParseDotEnv(src []byte) map[string]string {
	out := map[string]string{}
	s := string(src)

	for pos := 0; pos < len(s); {
		lineEnd := len(s)
		if nl := strings.IndexByte(s[pos:], '\n'); nl >= 0 {
			lineEnd = pos + nl
		}
		next := lineEnd + 1

		raw := s[pos:lineEnd]
		body := strings.TrimLeft(raw, " \t")
		lead := len(raw) - len(body)
		if t := strings.TrimSpace(body); t == "" || strings.HasPrefix(t, "#") {
			pos = next
			continue
		}
		// `export ` is tolerated because half the world writes one.
		if rest := strings.TrimPrefix(body, "export "); rest != body {
			lead += len(body) - len(strings.TrimLeft(rest, " \t"))
			body = strings.TrimLeft(rest, " \t")
		}

		eq := strings.IndexByte(body, '=')
		if eq < 0 {
			pos = next
			continue
		}
		key := strings.TrimSpace(body[:eq])
		if key == "" {
			pos = next
			continue
		}

		// The value is read out of the ORIGINAL buffer rather than out of the
		// line, because a quoted value may not end on this line.
		vs := pos + lead + eq + 1
		for vs < lineEnd && (s[vs] == ' ' || s[vs] == '\t') {
			vs++
		}
		if vs < lineEnd && (s[vs] == '"' || s[vs] == '\'') {
			val, end, closed := readQuotedDotEnv(s, vs)
			if closed {
				out[key] = val
				// Anything after the closing quote is a comment, and the entry
				// ends with the line the quote closed on.
				pos = len(s)
				if nl := strings.IndexByte(s[end:], '\n'); nl >= 0 {
					pos = end + nl + 1
				}
				continue
			}
			// No closing quote anywhere in the file: this is a typo, not a
			// multi-line value. Fall through to the single-line reading rather
			// than swallowing every remaining entry into one value.
		}
		out[key] = dotEnvValue(s[vs:lineEnd])
		pos = next
	}
	return out
}

// readQuotedDotEnv reads a quoted `.env` value starting at the opening quote and
// returns the value, the offset just past the closing quote, and whether a
// closing quote was found at all.
//
// Two things the line-oriented reader this replaced got silently wrong:
//
//   - A value may SPAN LINES. `KEY="line one\nline two"` written across two
//     lines parsed to "line one" and the remainder was discarded — half a
//     value, no error, in the layer that feeds every ${VAR}.
//   - A double-quoted value INTERPRETS ESCAPES, as Compose does. `ESC="a\nb"`
//     is three characters with a newline in the middle, not the four literal
//     characters `a`, `\`, `n`, `b`.
//
// Single quotes are literal in both respects: they may span lines, and a
// backslash inside them is a backslash. That is the shell convention Compose
// follows and it is the only way to write a value containing a backslash.
//
// A CR immediately before a LF inside a multi-line value is dropped, so a file
// with CRLF endings yields the same value as the same file with LF endings.
// Every other CR is kept.
func readQuotedDotEnv(s string, i int) (value string, end int, closed bool) {
	q := s[i]
	i++
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		switch {
		case c == q:
			return b.String(), i + 1, true

		case c == '\\' && q == '"' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'f':
				b.WriteByte('\f')
			case 'b':
				b.WriteByte('\b')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case '\'':
				b.WriteByte('\'')
			default:
				// An escape nothing defines is left exactly as written. Dropping
				// the backslash would change a value on the author's behalf, and
				// `\d` in a regex is a real thing to find in a `.env`.
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			i++

		case c == '\r' && i+1 < len(s) && s[i+1] == '\n':
			i++ // the LF on the next pass carries the line break

		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), len(s), false
}

// dotEnvValue reads the value half of a `.env` line.
//
// The INLINE COMMENT is the part that is easy to get wrong and was: a `.env`
// holding
//
//	VPN_SERVER_URL=your-domain.dyndns.com # free examples http://duckdns.org
//
// defines the URL, not the URL followed by a sentence about duckdns. Taking the
// whole line put that sentence into a service's environment, and it was the
// differential harness against `docker compose config` that found it — no unit
// test had thought to write a comment on a value. See
// testdata/edge/dotenv-inline-comment/.
//
// A QUOTED value is taken whole, quotes stripped, and anything after the
// closing quote is a comment. That is how a value containing a `#` is written.
func dotEnvValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if q := v[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(v[1:], q); end >= 0 {
			return v[1 : end+1]
		}
		// Unterminated quote: take the rest, minus the opening quote. Refusing
		// would fail a resolve over a file Compose reads happily.
		return v[1:]
	}
	// Unquoted: a `#` preceded by whitespace starts a comment. A `#` with no
	// space before it is part of the value — `PIHOLE_DNS_=1.1.1.1#5054` is a
	// real line from this corpus.
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// interpResult is what one scalar's interpolation produced.
type interpResult struct {
	text     string
	changed  bool
	findings []Finding // Path and Origin filled in by the caller
	err      *InterpolationError
}

// interpolate expands every variable reference in s.
//
// The full Compose grammar, and nothing invented beyond it:
//
//	$$          a literal dollar
//	$VAR        bare reference
//	${VAR}      braced reference
//	${VAR:-d}   d if VAR is unset OR empty
//	${VAR-d}    d if VAR is unset
//	${VAR:+a}   a if VAR is set AND non-empty, otherwise empty
//	${VAR+a}    a if VAR is set, otherwise empty
//	${VAR:?m}   fail with m if VAR is unset OR empty
//	${VAR?m}    fail with m if VAR is unset
//
// Defaults and alternates are themselves interpolated, so `${A:-${B}}` works.
// Anything else after the name is not Compose syntax: the text is left exactly
// as written and a finding is raised, because guessing at a form the spec does
// not define is how an editor silently changes what a stack does.
func interpolate(s string, env *varEnv) interpResult {
	if !strings.ContainsRune(s, '$') {
		return interpResult{text: s}
	}
	var b strings.Builder
	res := interpResult{}
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			// A trailing bare `$` is a literal dollar, not a truncated
			// reference. Compose treats it the same way.
			b.WriteByte('$')
			i++
			continue
		}
		if s[i+1] == '$' {
			b.WriteByte('$')
			res.changed = true
			i += 2
			continue
		}
		if s[i+1] == '{' {
			end, ok := matchBrace(s, i+1)
			if !ok {
				res.findings = append(res.findings, Finding{
					Kind:    FindingInvalidInterpolation,
					Message: fmt.Sprintf("%q has an unterminated ${ — left exactly as written", s),
				})
				b.WriteString(s[i:])
				i = len(s)
				continue
			}
			body := s[i+2 : end]
			out, sub := expandBraced(body, env)
			res.findings = append(res.findings, sub.findings...)
			if sub.err != nil && res.err == nil {
				res.err = sub.err
			}
			b.WriteString(out)
			res.changed = true
			i = end + 1
			continue
		}
		if isNameStartByte(s[i+1]) {
			j := i + 1
			for j < len(s) && isNameByte(s[j]) {
				j++
			}
			name := s[i+1 : j]
			v, ok := env.lookup(name)
			if !ok {
				res.findings = append(res.findings, undefinedFinding(name))
			}
			b.WriteString(v)
			res.changed = true
			i = j
			continue
		}
		// `$` followed by anything else — `$(date)`, `$5` — is a literal
		// dollar. Shell substitution is the container's business.
		b.WriteByte('$')
		i++
	}
	res.text = b.String()
	if !res.changed && res.text == s {
		res.changed = false
	}
	return res
}

func undefinedFinding(name string) Finding {
	return Finding{
		Kind:     FindingUndefinedVariable,
		Variable: name,
		Message: fmt.Sprintf("${%s} is referenced but defined in no .env file, env_file or the environment; "+
			"it interpolates to an empty string", name),
	}
}

// matchBrace returns the index of the `}` closing the `{` at open, honouring
// nested `${...}` inside a default. Nesting is why this is not IndexByte: the
// first `}` in `${A:-${B}}` closes the inner reference, not the outer one.
func matchBrace(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// expandBraced handles the inside of a `${...}`.
func expandBraced(body string, env *varEnv) (string, interpResult) {
	var res interpResult

	// The name runs to the first character that cannot be part of one.
	j := 0
	for j < len(body) && isNameByte(body[j]) {
		j++
	}
	name, op := body[:j], body[j:]
	if name == "" || !isNameStartByte(name[0]) {
		res.findings = append(res.findings, Finding{
			Kind:    FindingInvalidInterpolation,
			Message: fmt.Sprintf("${%s} does not name a variable — left exactly as written", body),
		})
		return "${" + body + "}", res
	}

	val, set := env.lookup(name)

	if op == "" {
		if !set {
			res.findings = append(res.findings, undefinedFinding(name))
		}
		return val, res
	}

	// Split the operator from its argument. The colon variants additionally
	// treat an empty value as unset — that is the entire difference between
	// `:-` and `-`, and getting it backwards changes what a stack runs.
	var (
		colon bool
		verb  byte
		arg   string
	)
	if op[0] == ':' {
		colon = true
		if len(op) < 2 {
			res.findings = append(res.findings, Finding{
				Kind:    FindingInvalidInterpolation,
				Message: fmt.Sprintf("${%s} ends in a bare ':' — left exactly as written", body),
			})
			return "${" + body + "}", res
		}
		verb, arg = op[1], op[2:]
	} else {
		verb, arg = op[0], op[1:]
	}

	switch verb {
	case '-', '+', '?':
	default:
		res.findings = append(res.findings, Finding{
			Kind:    FindingInvalidInterpolation,
			Message: fmt.Sprintf("${%s} uses an operator Compose does not define — left exactly as written", body),
		})
		return "${" + body + "}", res
	}

	present := set && (!colon || val != "")

	// The argument is itself a template: `${A:-${B}}` and `${A:-$B}` both work.
	expandArg := func() string {
		sub := interpolate(arg, env)
		res.findings = append(res.findings, sub.findings...)
		if sub.err != nil && res.err == nil {
			res.err = sub.err
		}
		return sub.text
	}

	switch verb {
	case '-':
		if present {
			return val, res
		}
		// A default IS a definition. No finding: the author said what to use.
		return expandArg(), res
	case '+':
		if present {
			return expandArg(), res
		}
		return "", res
	default: // '?'
		if present {
			return val, res
		}
		res.err = &InterpolationError{Variable: name, Message: expandArg()}
		return "", res
	}
}

func isNameStartByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameByte(c byte) bool { return isNameStartByte(c) || c >= '0' && c <= '9' }
