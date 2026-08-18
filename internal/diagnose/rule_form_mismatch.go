package diagnose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// Configuration that the merge dropped because two files spelled one key in two
// different shapes.
//
// FED BY THE RESOLVER, like the undefined-variable rule. internal/resolve
// raises a FindingFormMismatch when `environment` (or `depends_on`, or any
// other collection) is a LIST in one file and a MAPPING in the other: the later
// declaration replaces the earlier one whole, so entries only the earlier form
// set are not in the resolved model. `docker compose config` keeps them.
//
// THE DROP ITSELF IS NOT THE DEFECT and this rule does not ask for it to be
// fixed. Normalising the two forms would mean re-emitting the collection in one
// shape, and the resolved model has to keep the shape each file wrote or the
// splice engine addresses bytes that are not there. That trade is deliberate.
//
// The defect this rule closes is that the drop was SILENT for anyone not
// parsing `composure resolve -json`. Configuration disappeared and no human-facing
// surface said so, which is CLAUDE.md rule 6 — an operation that cannot be
// performed safely must say so rather than quietly produce a lesser answer.
// Being a rule is what carries it into `composure diagnose` and, through the same
// report, into the extension's problems panel; no per-rule registration exists
// there, so a new id needs nothing outside this package.
//
// Warning, not error: the project runs. It runs with less configuration than
// the author wrote, which is exactly the kind of wrong answer this engine is
// prone to producing confidently.

type formMismatchRule struct{}

func (formMismatchRule) id() string { return "merge-form-mismatch" }

func (formMismatchRule) title() string { return "A merged key was written in two different shapes" }

func (r formMismatchRule) apply(c *context) []Finding {
	var out []Finding
	for _, f := range c.Project.Findings() {
		if f.Kind != resolve.FindingFormMismatch {
			continue
		}
		if f.Origin.IsZero() {
			continue // AD-7: nothing to anchor at
		}
		path := append(resolve.Path{}, f.Path...)

		anchors := []Anchor{{Label: "kept, in this shape", Path: path, Origin: f.Origin}}
		if !f.Displaced.IsZero() {
			// The second anchor is the one worth having: it is the position of
			// the declaration whose entries are gone, and it is where the reader
			// has to go to get them back.
			anchors = append(anchors, Anchor{Label: "entries dropped from here", Path: path, Origin: f.Displaced})
		}

		out = append(out, Finding{
			Rule:     r.id(),
			Severity: SeverityWarning,
			Title:    r.title(),
			Message:  formMismatchMessage(path, f),
			Subjects: []resolve.Path{path},
			Anchors:  anchors,
			NoFix: "no mechanical fix is described: reconciling the two shapes means rewriting one of the two " +
				"collections whole, and this engine never re-emits a collection — it patches the bytes a file " +
				"already has. Rewrite the two declarations in the same form, by hand, choosing which one to keep",
		})
	}
	// Deterministic order regardless of the order the merge walked keys in.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Anchors[0].Origin, out[j].Anchors[0].Origin
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// formMismatchMessage says what was dropped, from which file, and why — the
// three things the JSON-only version of this finding was saying to nobody.
func formMismatchMessage(path resolve.Path, f resolve.Finding) string {
	what := "entries only the earlier declaration set are"
	if len(f.Dropped) > 0 {
		what = quoteList(f.Dropped) + " " + isAre(len(f.Dropped))
	}
	from := f.Displaced.File
	if from == "" {
		from = "the earlier file"
	}
	return fmt.Sprintf(
		"%s is written in two different shapes across the merge, so %s NOT in the resolved model — dropped from %s, "+
			"which %s replaces whole. `docker compose config` keeps them; Composure does not, because reconciling the two "+
			"shapes would mean re-emitting the collection and the model has to keep the shape each file wrote. "+
			"Write both declarations in the same form.",
		path, what, from, f.Origin.File)
}

func quoteList(in []string) string {
	return "`" + strings.Join(in, "`, `") + "`"
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
