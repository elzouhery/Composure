package diagnose

import (
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// Shared reading helpers. Nothing here derives a relation — every relational
// question goes through the graph (AD-17). These are for local reads and for
// turning a node back into the name a human wrote.

// serviceName is the last segment of a services.<name> path.
func serviceName(p resolve.Path) string {
	if len(p) >= 2 && p[0] == "services" {
		return p[1]
	}
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1]
}

// serviceValue returns the resolved value of the service a node addresses.
func (c *context) serviceValue(p resolve.Path) (*resolve.Value, bool) {
	if len(p) < 2 || p[0] != "services" {
		return nil, false
	}
	return c.Project.At(resolve.Path{"services", p[1]})
}

// field is a one-level lookup that tolerates a nil parent.
func field(v *resolve.Value, key string) (*resolve.Value, bool) {
	if v == nil || v.Kind() != resolve.KindMapping {
		return nil, false
	}
	return v.Map().Get(key)
}

// truthy reads a compose boolean. The model keeps scalars exactly as written,
// so `true`, `True` and `"true"` all arrive as text and all mean the same.
func truthy(v *resolve.Value, ok bool) bool {
	if !ok || v == nil || v.Kind() != resolve.KindScalar {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v.Scalar())) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// interpolated reports whether a scalar contains a variable reference of any
// form — `${VAR}`, `${VAR:-x}` or bare `$VAR`. `$$` is Compose's escape for a
// literal dollar and is not a reference.
//
// It is used to answer one question: is the value actually in this file? A
// credential behind a variable is not, which is the whole reason story 3.1
// exempts it.
func interpolated(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 >= len(s) {
			return false
		}
		switch {
		case s[i+1] == '$':
			i++ // escaped literal dollar
		case s[i+1] == '{':
			return true
		case isNameStart(s[i+1]):
			return true
		}
	}
	return false
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameChar(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

// undefinedUnder reports whether anything at or below a path interpolated a
// variable that nothing defined.
//
// It reads the resolver's findings (story 1.3) rather than re-scanning scalars
// for `${`, which is possible now and was not before: interpolation happens at
// load, so a scalar in the model is already substituted and a text scan would
// find nothing to warn about.
//
// It exists for the rules that reason about a FILESYSTEM path. `context:
// ${CTX}/api` with CTX undefined resolves to `/api`, and reporting "/api/Dockerfile
// is not there" would be a confident wrong answer about a path that was never
// meant to be read from this disk.
func (c *context) undefinedUnder(p resolve.Path) bool {
	for _, f := range c.Project.Findings() {
		if f.Kind != resolve.FindingUndefinedVariable {
			continue
		}
		if len(f.Path) < len(p) {
			continue
		}
		if p.Equal(f.Path[:len(p)]) {
			return true
		}
	}
	return false
}

// pathKey renders a path as an unambiguous map key. Path.String is documented
// as lossy for numeric mapping keys, and a join key cannot be.
func pathKey(p resolve.Path) string { return strings.Join(p, "\x00") }
