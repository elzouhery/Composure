package diagnose

import (
	"fmt"

	"github.com/elzouhery/composure/internal/resolve"
)

// Story 5.2's last-but-two criterion, and the diagnostic half of AD-20.
//
// A top-level `version:` is obsolete. Compose ignores it entirely — it does not
// select a schema, it does not gate a feature, and Compose itself prints a
// warning about it. Composure ignores it too: there is exactly ONE Compose
// specification, it is current, and internal/schema never reads this field to
// choose anything. Compatibility is judged against the installed binary
// instead (AD-21).
//
// So why say anything at all? Because a field that is silently ignored is
// worse than one that is complained about. The reader believes `version: "3.8"`
// is doing something — that is why it is still in tens of thousands of files —
// and the inspector offering keys the reader thinks their "version" forbids
// makes no sense until someone says the field is dead. Hint severity: nothing
// is broken, and a hint never fails a build on its own.
//
// The fix is a delete, with the exact range strategy.DeleteKey would remove, so
// story 6.1 can apply it rather than reinterpret it.

type versionFieldRule struct{}

func (versionFieldRule) id() string { return "obsolete-version-field" }

func (versionFieldRule) title() string { return "Obsolete top-level version field" }

func (r versionFieldRule) apply(c *context) []Finding {
	path := resolve.Path{"version"}
	v, ok := c.Project.At(path)
	if !ok {
		return nil
	}
	origin := v.Origin()
	if origin.IsZero() {
		return nil // AD-7: nothing to anchor at
	}

	declared := ""
	if v.Kind() == resolve.KindScalar {
		declared = v.Scalar()
	}
	message := "The top-level `version` field is obsolete. Compose ignores it, and so does Composure: " +
		"there is one current specification and the keys available to you are decided by the " +
		"`docker compose` on your machine, not by this line. It can be deleted."
	if declared != "" {
		message = fmt.Sprintf(
			"`version: %s` is obsolete. Compose ignores it, and so does Composure: there is one current "+
				"specification and the keys available to you are decided by the `docker compose` on your "+
				"machine, not by this line. It can be deleted.",
			declared)
	}

	f := Finding{
		Severity: SeverityHint,
		Message:  message,
		Subjects: []resolve.Path{path},
		Anchors:  []Anchor{{Label: "declared here", Path: path, Origin: origin}},
	}
	fix, why := c.deleteKeyFix(path, origin, "delete the obsolete `version` field")
	if fix != nil {
		f.Fix = fix
	} else {
		f.NoFix = why
	}
	return []Finding{f}
}
