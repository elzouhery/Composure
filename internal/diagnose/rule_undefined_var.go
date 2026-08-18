package diagnose

import (
	"fmt"
	"sort"

	"github.com/elzouhery/composure/internal/resolve"
)

// Story 3.6 — a variable the stack references that nothing defines.
//
// FED BY THE RESOLVER. This rule performs no text scanning of its own: story
// 1.3's interpolation pass runs per file, at load, before any merge, and
// records a resolve.Finding at every reference it could not satisfy. That list
// is what this rule reads.
//
// The difference from the provisional version this replaced is not cosmetic.
// The resolver knows what a variable IS — it knows the six operator forms, it
// knows that a default is a definition, it knows the environment each
// individual file was interpolated against, and it knows the exact Origin of
// the value that held the reference. A text scan over resolved scalars knew
// none of that, and its own comment said so.
//
// What survives from the provisional version is the OUTPUT shape, which was
// never provisional: one finding per variable carrying every reference site, so
// that five references are five Origins on one finding rather than five
// findings.
//
// Two behaviours the resolver now guarantees, and this rule therefore does not
// re-implement:
//
//   - `${VAR:-default}` and `${VAR-default}` never appear here. A default is a
//     definition, so interpolation succeeds and raises nothing.
//   - `$$` is an escape for a literal dollar and is not a reference.
//
// `${VAR:?message}` never appears here either, for a different reason: that
// form fails the resolve outright with the author's message, so a project
// containing an unsatisfied one produces no model for any rule to run against.

type undefinedVarRule struct{}

func (undefinedVarRule) id() string { return "undefined-variable" }

func (undefinedVarRule) title() string { return "Variable referenced but never defined" }

func (r undefinedVarRule) apply(c *context) []Finding {
	byName := map[string][]resolve.Finding{}
	var order []string
	for _, f := range c.Project.Findings() {
		if f.Kind != resolve.FindingUndefinedVariable {
			continue
		}
		if f.Origin.IsZero() {
			continue // AD-7: an unanchorable reference is not reported
		}
		if _, seen := byName[f.Variable]; !seen {
			order = append(order, f.Variable)
		}
		byName[f.Variable] = append(byName[f.Variable], f)
	}

	sort.Strings(order)
	out := make([]Finding, 0, len(order))
	for _, name := range order {
		refs := dedupeReferences(byName[name])
		anchors := make([]Anchor, 0, len(refs))
		subjects := make([]resolve.Path, 0, len(refs))
		for _, ref := range refs {
			anchors = append(anchors, Anchor{
				Label:  "referenced here",
				Path:   append(resolve.Path{}, ref.Path...),
				Origin: ref.Origin,
			})
			subjects = append(subjects, append(resolve.Path{}, ref.Path...))
		}
		out = append(out, Finding{
			Rule:     r.id(),
			Severity: SeverityWarning,
			Title:    r.title(),
			Message: fmt.Sprintf("${%s} is referenced %s but is defined in no .env file, env_file or the environment; "+
				"it interpolates to an empty string.", name, plural(len(refs), "place", "places")),
			Subjects: subjects,
			Anchors:  anchors,
			NoFix:    "no automatic fix is offered: the definition belongs in a .env file or the host environment, not in this document, and only the author knows what the value should be",
		})
	}
	return out
}

// dedupeReferences collapses references that address the same position.
//
// An anchor expanded at three sites genuinely holds the reference three times,
// and the resolver reports each — that is correct for the model. For a reader,
// three anchors pointing at one line in one file is noise, so identical
// positions on the same path collapse to one.
func dedupeReferences(in []resolve.Finding) []resolve.Finding {
	seen := map[string]bool{}
	out := make([]resolve.Finding, 0, len(in))
	for _, f := range in {
		k := f.Origin.String() + "\x00" + pathKey(f.Path)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "in one " + one
	}
	return fmt.Sprintf("in %d %s", n, many)
}
