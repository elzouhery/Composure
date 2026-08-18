package diagnose

import (
	"fmt"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 3.3 — `depends_on` with `condition: service_healthy` on a service that
// can never report healthy.
//
// The dependency edge is read from the topology, not by re-walking `depends_on`
// in the resolved model (AD-17). That matters more here than it looks: the
// short array form and the long mapping form both mean a dependency, the short
// form defaults to `service_started`, and a second implementation of that
// defaulting is a second place for it to be wrong.
//
// Whether the target HAS a healthcheck is a value local to that one service, so
// it is read from the resolved model — that is what AD-17's second sentence
// permits.
//
// Two ways to be unsatisfiable, one finding:
//
//   - the target declares no `healthcheck:` at all;
//   - the target declares one with `disable: true`, which can never report
//     healthy either.
//
// A target that is not declared in this project — a service referenced but
// never written down — is NOT reported. There is no definition to anchor the
// second half of the finding at, and AD-7 says a finding that cannot anchor
// itself is not shipped. It is also the honest answer: we cannot see what that
// service's image declares.

type healthcheckRule struct{}

func (healthcheckRule) id() string { return "healthy-without-healthcheck" }

func (healthcheckRule) title() string {
	return "Waiting on a health condition that can never be met"
}

func (r healthcheckRule) apply(c *context) []Finding {
	// The guard that used to stand here went silent on any project using
	// extends: or include:, because the healthcheck this rule reports as
	// missing was commonly in a file the resolver could not see. Stories 1.7
	// and 1.8 expand both, so the model is no longer a fragment and the rule
	// can speak.
	var out []Finding
	for _, e := range c.Graph.Edges() {
		if e.Kind != topology.EdgeDependsOn || e.Depends == nil {
			continue
		}
		if e.Depends.Condition != topology.ConditionHealthy {
			continue
		}
		// Nothing of ours to anchor at, and nothing we can see: a service
		// referenced but never written down might well ship a healthcheck in its
		// image, and AD-7 has no definition to point the second anchor at.
		//
		// The `!target.Declared` half cannot currently fire on its own — an
		// undeclared service is not in Project.Services(), so the serviceValue
		// lookup below returns false for the same input. Mutation-tested:
		// dropping it fails no test. It stays because it states the rule's own
		// reason rather than relying on a lookup three lines later happening to
		// agree, and TestUndeclaredDependencyHasNoServiceValue pins the agreement.
		target, ok := c.Graph.Node(e.To)
		if !ok || !target.Declared {
			continue
		}
		if e.Origin.IsZero() || target.Origin.IsZero() {
			continue // AD-7
		}
		svc, ok := c.serviceValue(e.To)
		if !ok {
			continue
		}
		state, reason := healthcheckState(svc)
		if state == healthOK {
			continue
		}
		out = append(out, r.finding(c, e, target, state, reason))
	}
	return out
}

// healthState distinguishes the ways a health condition can be unsatisfiable,
// because the EDIT that resolves them is different in each case and the fix must
// not be the same in all three.
type healthState int

const (
	// healthOK: a check is declared and enabled. Whether its command works is
	// not a static question, so it is trusted.
	healthOK healthState = iota
	// healthAbsent: no `healthcheck:` key at all. The fix inserts one.
	healthAbsent
	// healthDisabled: `healthcheck: {disable: true}`. Inserting a `healthcheck`
	// key here would add a SECOND one to the same mapping — a duplicate key, and
	// in a merge-key-free document an outright invalid one. The edit is to
	// remove the `disable` flag.
	healthDisabled
	// healthTestNone: `test: NONE`. The edit is to write a real command, which
	// only the author can do.
	healthTestNone
)

// healthcheckState reports whether the service can ever go healthy, and why
// not. An absent key and a disabled check are the two cases; a declared check
// is trusted, because judging whether its command works is not a static
// question.
func healthcheckState(svc *resolve.Value) (healthState, string) {
	hc, ok := field(svc, "healthcheck")
	if !ok || hc == nil || hc.Kind() == resolve.KindNull {
		return healthAbsent, "it declares no healthcheck, and none is visible to Composure from its image"
	}
	if truthy(field(hc, "disable")) {
		return healthDisabled, "its healthcheck is disabled, so it can never report healthy"
	}
	// `test: NONE` is Compose's other spelling of disabled.
	if t, ok := field(hc, "test"); ok && t != nil {
		if t.Kind() == resolve.KindScalar && t.Scalar() == "NONE" {
			return healthTestNone, "its healthcheck is `test: NONE`, which disables it"
		}
		if t.Kind() == resolve.KindSequence {
			items := t.Seq()
			if len(items) == 1 && items[0].Scalar() == "NONE" {
				return healthTestNone, "its healthcheck is `test: [NONE]`, which disables it"
			}
		}
	}
	return healthOK, ""
}

func (r healthcheckRule) finding(c *context, e topology.Edge, target topology.Node, state healthState, reason string) Finding {
	from, to := serviceName(e.From), serviceName(e.To)
	f := Finding{
		Rule:     r.id(),
		Severity: SeverityError,
		Title:    r.title(),
		Message: fmt.Sprintf("service %q waits for %q to become healthy, but %s. Startup will hang until the dependency times out.",
			from, to, reason),
		Subjects: []resolve.Path{e.From, e.To},
		Anchors: []Anchor{
			{Label: fmt.Sprintf("%s waits on %s here", from, to), Path: e.From, Origin: e.Origin},
			{Label: fmt.Sprintf("%s is defined here", to), Path: target.Path, Origin: target.Origin},
		},
	}
	f.Fix, f.NoFix = r.fix(c, target, state, from, to)
	return f
}

// fix describes the edit, which is a DIFFERENT edit in each of the three states.
//
// The version this replaced always described an insert of a `healthcheck` key.
// For a target that already declares `healthcheck: {disable: true}` that is
// wrong twice over: the key is already there, so the insert adds a duplicate,
// and the thing standing between the service and health is the `disable` flag
// the insert does not touch. A fix that adds a key next to the one causing the
// problem is not a smaller version of the right fix, it is a different edit with
// the same name.
func (r healthcheckRule) fix(c *context, target topology.Node, state healthState, from, to string) (*Fix, string) {
	switch state {
	case healthAbsent:
		// The mechanical half of the fix is adding the key; what to put in it is
		// the author's, so the described insert carries no value and says so.
		return c.insertKeyFix(target.Path, target.Origin, "healthcheck", "", true,
			fmt.Sprintf("add a healthcheck to %q, or change the condition on %q to service_started", to, from))

	case healthDisabled:
		// Removing `disable:` is the edit — but only when something is left
		// behind that can actually run. A healthcheck whose only key is
		// `disable` would become an empty `healthcheck:` mapping, which is not a
		// working check either, and describing an edit that trades one broken
		// state for another is not a fix.
		svc, ok := c.serviceValue(target.Path)
		if !ok {
			return nil, healthNoFixDisabled(to)
		}
		hc, ok := field(svc, "healthcheck")
		if !ok || hc == nil {
			return nil, healthNoFixDisabled(to)
		}
		if t, ok := field(hc, "test"); !ok || t == nil || t.Kind() == resolve.KindNull {
			return nil, healthNoFixDisabled(to)
		}
		disable, ok := field(hc, "disable")
		if !ok || disable == nil {
			return nil, healthNoFixDisabled(to)
		}
		origin := disable.Origin()
		if origin.IsZero() {
			if o, ok := hc.Map().KeyOrigin("disable"); ok {
				origin = o
			}
		}
		if origin.IsZero() {
			return nil, healthNoFixDisabled(to)
		}
		return c.deleteKeyFix(target.Path.Child("healthcheck").Child("disable"), origin,
			fmt.Sprintf("delete `disable` from %q's healthcheck so the test it already declares can run", to))

	default: // healthTestNone
		return nil, fmt.Sprintf("no fix is described: %q's healthcheck is disabled with `test: NONE`, and replacing it means writing "+
			"the command that decides whether the service is healthy — which only its author knows", to)
	}
}

func healthNoFixDisabled(to string) string {
	return fmt.Sprintf("no fix is described: %q declares a healthcheck and disables it, so the edit is to remove `disable` or to write "+
		"a test for it to run — and with no test declared, removing `disable` alone would leave an empty healthcheck that is no more "+
		"able to report healthy than the disabled one", to)
}
