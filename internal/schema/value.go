package schema

import (
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// ValueView is a declared value shaped for display: what the file says, where
// it says it, and — for a scalar holding `${VAR}` — what that resolves to.
//
// It is a projection, not a second model. The resolver's Value is immutable and
// carries everything here except the interpolation, which needs an environment
// the resolver does not read. Building the projection in Go rather than in the
// webview is deliberate: the extension already learned once that a model
// re-derived in TypeScript is a second model that silently diverges (see
// host/topology.ts), and rendering `${VAR}` correctly is exactly the kind of
// detail that would diverge.
//
// Nothing here summarises. Story 5.1's first acceptance criterion is that
// twenty environment entries render as twenty keys AND twenty values; a count
// in place of the values is the incumbent's failure, so there is no field on
// this type that could hold one.
type ValueView struct {
	Kind string `json:"kind"`
	// Text is the scalar exactly as written — unquoted but never coerced.
	// Empty for a sequence or a mapping.
	Text string `json:"text"`
	// Resolved is Text with `${VAR}` substituted, present ONLY when Text
	// contains a reference. The literal is what the file says and stays in
	// Text; this is what it means. Both are shown — story 5.1.
	Resolved string `json:"resolved,omitempty"`
	// Undefined names the variables nothing defines, in reference order. A
	// non-empty list with Resolved set means a partial resolution.
	Undefined []string `json:"undefined,omitempty"`
	// EnvKnown is false when no environment could be established at all, in
	// which case Resolved and Undefined say nothing and the inspector must not
	// claim a variable is undefined. Silence is not evidence.
	EnvKnown bool `json:"env_known"`

	Origin    resolve.Origin     `json:"origin"`
	Overrides []resolve.Override `json:"overrides"`

	// Alias and AliasSite carry an anchor expansion. Origin is where the bytes
	// are (the anchor); AliasSite is the `*name` the reader is looking at.
	// Neither is derivable from the other, so both travel.
	Alias     string          `json:"alias,omitempty"`
	AliasSite *resolve.Origin `json:"alias_site,omitempty"`

	Seq     []ValueView `json:"seq,omitempty"`
	Entries []Entry     `json:"entries,omitempty"`
}

// Entry is one key of a declared mapping, with the position of the key itself
// — which is what an edit locates, and what a reader clicking a key expects
// the cursor to land on.
type Entry struct {
	Key       string         `json:"key"`
	KeyOrigin resolve.Origin `json:"key_origin"`
	Path      string         `json:"path"`
	Value     ValueView      `json:"value"`
}

// maxViewDepth bounds the projection. Compose nests four or five deep at worst
// (`services.x.deploy.resources.limits.cpus`); a bound an order of magnitude
// past that costs nothing and makes a pathological file impossible to hang on.
const maxViewDepth = 24

// env is the variable environment a `${VAR}` is resolved against.
type env struct {
	vars  map[string]string
	known bool
}

// viewOf projects a resolved value for display.
func viewOf(v *resolve.Value, path resolve.Path, e env, depth int) ValueView {
	out := ValueView{Kind: v.Kind().String(), Origin: v.Origin(), Overrides: v.Overrides(), EnvKnown: e.known}
	if out.Overrides == nil {
		out.Overrides = []resolve.Override{}
	}
	if name := v.Alias(); name != "" {
		out.Alias = name
	}
	if site, ok := v.AliasSite(); ok {
		s := site
		out.AliasSite = &s
	}
	if depth > maxViewDepth {
		return out
	}

	switch v.Kind() {
	case resolve.KindScalar, resolve.KindAlias:
		// Raw, not Scalar. Story 1.3 interpolates at LOAD, so Scalar() is
		// already the substituted text; the literal the reader is looking at —
		// the bytes an edit would touch — is Raw(). Showing the substitution in
		// the Text field would make the inspector disagree with the file.
		out.Text = v.Raw()
		if e.known && references(out.Text) {
			out.Resolved, out.Undefined = interpolate(out.Text, e.vars)
		}
	case resolve.KindSequence:
		for i, item := range v.Seq() {
			out.Seq = append(out.Seq, viewOf(item, path.Index(i), e, depth+1))
		}
	case resolve.KindMapping:
		m := v.Map()
		for _, k := range m.Keys() {
			child, _ := m.Get(k)
			ko, _ := m.KeyOrigin(k)
			childPath := path.Child(k)
			out.Entries = append(out.Entries, Entry{
				Key:       k,
				KeyOrigin: ko,
				Path:      childPath.String(),
				Value:     viewOf(child, childPath, e, depth+1),
			})
		}
	}
	return out
}

// references reports whether a scalar contains a Compose variable reference.
//
// `$$` is Compose's escape for a literal dollar and is not one — the same rule
// internal/diagnose's undefined-variable rule applies, and the two must agree
// or the inspector shows a resolution beneath a value the diagnostics say has
// no variable in it.
func references(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			i++
			continue
		}
		if i+1 < len(s) && (s[i+1] == '{' || isNameStart(s[i+1])) {
			return true
		}
	}
	return false
}

// interpolate substitutes `${VAR}`, `${VAR:-default}`, `${VAR-default}` and
// bare `$VAR`, and reports which names nothing defined.
//
// This is a DISPLAY resolution and it deliberately differs from story 1.3's
// resolver pass in ONE way, which is the reason it still exists: an undefined
// reference is left standing as `${SESSION_SECRET}` rather than blanked. The
// resolver produces what Compose would actually run, which for an undefined
// variable is the empty string; rendering `DATABASE_URL: postgres://:@db` in an
// inspector would be a confident wrong answer about a stack the reader is
// trying to understand. Undefined names the variables, and the literal stays
// visible.
//
// Everything else it reads comes from the model: Text is Value.Raw(), the
// pre-interpolation bytes. It does not read a file and does not parse a .env.
//
// A name with no definition is left as the literal reference rather than
// blanked. Rendering `DATABASE_URL: postgres://:@db` because two variables were
// undefined would be a confident wrong answer about what the stack will run.
func interpolate(s string, vars map[string]string) (string, []string) {
	var b strings.Builder
	var undefined []string
	seen := map[string]bool{}
	note := func(name string) {
		if !seen[name] {
			seen[name] = true
			undefined = append(undefined, name)
		}
	}

	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteString("$$") // an escape, carried through untouched
			i += 2
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:]) // unterminated; not a reference
				break
			}
			body := s[i+2 : i+2+end]
			i += 2 + end + 1
			name, fallback, hasFallback := splitReference(body)
			if value, ok := vars[name]; ok && value != "" {
				b.WriteString(value)
				continue
			}
			if value, ok := vars[name]; ok && value == "" && !hasFallback {
				continue // defined and empty
			}
			if hasFallback {
				b.WriteString(fallback)
				continue
			}
			note(name)
			b.WriteString("${" + body + "}")
			continue
		}
		if i+1 < len(s) && isNameStart(s[i+1]) {
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			name := s[i+1 : j]
			if value, ok := vars[name]; ok {
				b.WriteString(value)
			} else {
				note(name)
				b.WriteString(s[i:j])
			}
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), undefined
}

// splitReference separates `NAME:-default` and `NAME-default` from a bare
// `NAME`. A default is a definition, so a reference carrying one is never
// reported as undefined — the same rule the undefined-variable diagnostic
// applies.
func splitReference(body string) (name, fallback string, hasFallback bool) {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ':':
			if i+1 < len(body) && (body[i+1] == '-' || body[i+1] == '?' || body[i+1] == '+') {
				return body[:i], body[i+2:], body[i+1] == '-' || body[i+1] == '+'
			}
		case '-', '?', '+':
			return body[:i], body[i+1:], body[i] == '-' || body[i] == '+'
		}
	}
	return body, "", false
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }
