package diagnose

import (
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

const healthRule = "healthy-without-healthcheck"

func TestHealthyWithoutHealthcheck(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
`)
	got := wantCount(t, rep, healthRule, 1)[0]
	if got.Severity != SeverityError {
		t.Errorf("severity is %s, want error — startup can never complete", got.Severity)
	}
	if !strings.Contains(got.Message, `"web"`) || !strings.Contains(got.Message, `"db"`) {
		t.Errorf("message does not name both services: %s", got.Message)
	}
	// The story asks for two anchors: the depends_on, and B's definition.
	if len(got.Anchors) != 2 {
		t.Fatalf("%d anchors, want the depends_on and the definition", len(got.Anchors))
	}
	if got.Anchors[0].Origin.Line != 6 {
		t.Errorf("first anchor at line %d, want the depends_on entry on line 6", got.Anchors[0].Origin.Line)
	}
	if got.Anchors[1].Origin.Line != 8 {
		t.Errorf("second anchor at line %d, want db's definition on line 8", got.Anchors[1].Origin.Line)
	}
}

// A disabled healthcheck can never report healthy either.
func TestHealthyWithDisabledHealthcheck(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
    healthcheck:
      disable: true
`)
	got := wantCount(t, rep, healthRule, 1)[0]
	if !strings.Contains(got.Message, "disabled") {
		t.Errorf("message does not say the check is disabled: %s", got.Message)
	}
}

func TestHealthyWithTestNONE(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
    healthcheck:
      test: ["NONE"]
`)
	wantCount(t, rep, healthRule, 1)
}

// The negatives.

func TestHealthyWithAHealthcheckIsFine(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 5s
`)
	wantNone(t, rep, healthRule)
}

// service_started is the default and needs no healthcheck. The short array form
// means service_started too, and that defaulting is topology's — this test is
// as much about AD-17 as about the rule.
func TestStartedConditionNeedsNoHealthcheck(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      - db
  api:
    image: api
    depends_on:
      db:
        condition: service_started
  db:
    image: postgres
`)
	wantNone(t, rep, healthRule)
}

// A service referenced but never declared has no definition to anchor at, and
// nothing this tool can see says whether its image ships a healthcheck.
// Reporting it would be a guess wearing a finding's clothes (AD-7).
func TestHealthyOnUndeclaredServiceIsNotReported(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      external-db:
        condition: service_healthy
`)
	wantNone(t, rep, healthRule)
}

// The invariant behind the rule's `!target.Declared` guard: an undeclared
// dependency target has no entry in the resolved services either, so the
// serviceValue lookup that follows would refuse the same input.
//
// The guard therefore cannot fire on its own today, and removing it fails no
// test. This pins the agreement rather than the guard, so that if an undeclared
// node ever gains a resolvable service value, the guard becomes load-bearing and
// the suite says so instead of the rule reporting a missing healthcheck on a
// service whose image nobody here can read.
func TestUndeclaredDependencyHasNoServiceValue(t *testing.T) {
	project, err := resolve.BytesWith("compose.yaml", []byte(`
services:
  web:
    image: nginx
    depends_on:
      external-db:
        condition: service_healthy
`), resolve.Options{Env: map[string]string{}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	c := &context{Input: Input{Project: project, Graph: graph}}

	var checked int
	for _, n := range graph.Nodes() {
		if n.Kind != topology.NodeService || n.Declared {
			continue
		}
		checked++
		if _, ok := c.serviceValue(n.Path); ok {
			t.Errorf("undeclared service %q has a resolvable service value; the `!target.Declared` "+
				"guard in this rule is now load-bearing and needs a test of its own", n.Name)
		}
	}
	if checked == 0 {
		t.Fatal("the fixture produced no undeclared service node; it no longer tests what it claims")
	}
}

// The dependency is filtered out by profile, so there is no edge and no
// finding — the rule reads the graph, and the graph has already filtered.
func TestHealthyRespectsProfiles(t *testing.T) {
	const src = `
services:
  web:
    image: nginx
    profiles: [full]
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
`
	wantNone(t, run(t, src), healthRule)
	wantCount(t, run(t, src, withProfiles("full")), healthRule, 1)
}

func TestHealthyFixInsertsIntoTheTarget(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\n    depends_on:\n      db:\n        condition: service_healthy\n  db:\n    image: postgres\n"
	rep := run(t, src)
	got := wantCount(t, rep, healthRule, 1)[0]
	if got.Fix == nil {
		t.Fatalf("no fix described: %s", got.NoFix)
	}
	if got.Fix.Operation != FixInsertKey || got.Fix.Key != "healthcheck" {
		t.Errorf("fix is %s %q", got.Fix.Operation, got.Fix.Key)
	}
	if got.Fix.Path.String() != "services.db" {
		t.Errorf("fix targets %s, want the service that lacks the check", got.Fix.Path)
	}
	// An insert touches no existing byte.
	if got.Fix.Range.Start != got.Fix.Range.End {
		t.Errorf("insertion reports a non-empty range %d-%d", got.Fix.Range.Start, got.Fix.Range.End)
	}
	if got.Fix.Range.Start != len(src)-1 {
		t.Errorf("insertion point is %d, want the end of db's block at %d", got.Fix.Range.Start, len(src)-1)
	}
	// AC 3.8: "the byte range in the file it would touch". Range.Line is the
	// other half of that claim and it was unbacked — deleting `line = lineAt(src,
	// start)` in locateFix, so an insert reported the ANCHOR's line instead of
	// the line it lands on, left the whole suite green.
	//
	// `services.db` is declared on line 7; the insertion point is at the end of
	// its block, on line 8. The two numbers differ here on purpose: a fixture
	// where the service is its own last line cannot tell the two apart.
	if want := 8; got.Fix.Range.Line != want {
		t.Errorf("insertion reports line %d, want %d — the line the insertion point "+
			"actually lands on, not line 7 where `db:` is declared", got.Fix.Range.Line, want)
	}
	if declared := 2 + strings.Count(src[:strings.Index(src, "\n  db:")], "\n"); declared != 7 {
		t.Fatalf("fixture regressed: `db:` is declared on line %d, not 7, so line 8 no longer "+
			"distinguishes the insertion point from the origin the fix was anchored at", declared)
	}
	// The body is the author's, and that has to be said rather than implied by
	// an empty Value — `healthcheck:` with a null value is not a healthcheck.
	if !got.Fix.NeedsValue {
		t.Error("the insert does not declare that the author must supply the check")
	}
}

// The insert is only correct when there is no `healthcheck:` key to collide
// with. Where one is already there and disabled, inserting a second is a
// duplicate key, and it does not touch the flag that is actually preventing the
// service from reporting healthy.
//
// Both branches are tested because the bug was that only one existed: the rule
// described the same insert for every case it fired on.
func TestHealthyDisabledCheckIsNotFixedByInsertingASecondOne(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\n    depends_on:\n      db:\n        condition: service_healthy\n  db:\n    image: postgres\n    healthcheck:\n      test: [\"CMD\", \"pg_isready\"]\n      disable: true\n"
	rep := run(t, src)
	got := wantCount(t, rep, healthRule, 1)[0]
	if got.Fix == nil {
		t.Fatalf("no fix described for a disabled check that declares a test: %s", got.NoFix)
	}
	if got.Fix.Operation == FixInsertKey {
		t.Fatal("the fix inserts a `healthcheck` key into a service that already has one; " +
			"that is a duplicate key, and it leaves `disable: true` in place")
	}
	if got.Fix.Operation != FixDeleteKey {
		t.Fatalf("fix is %s, want the removal of `disable`", got.Fix.Operation)
	}
	if got.Fix.Path.String() != "services.db.healthcheck.disable" {
		t.Errorf("fix targets %s, want the disable flag", got.Fix.Path)
	}
	if covered := src[got.Fix.Range.Start:got.Fix.Range.End]; !strings.Contains(covered, "disable: true") {
		t.Errorf("fix range covers %q, not the disable flag", covered)
	}
}

// ...but removing `disable` from a healthcheck that declares nothing else would
// leave an empty `healthcheck:` mapping, which cannot report healthy either.
// Trading one broken state for another is not a fix, so this one is refused.
func TestHealthyDisabledWithNoTestIsRefusedRatherThanHalfFixed(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
    healthcheck:
      disable: true
`)
	got := wantCount(t, rep, healthRule, 1)[0]
	if got.Fix != nil {
		t.Fatalf("a fix was described that would leave an empty healthcheck: %s %s",
			got.Fix.Operation, got.Fix.Path)
	}
	if !strings.Contains(got.NoFix, "disable") {
		t.Errorf("the refusal does not name what is wrong: %s", got.NoFix)
	}
}

// `test: NONE` needs a real command, and only the author knows what it is.
func TestHealthyTestNoneIsRefusedWithAReason(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres
    healthcheck:
      test: ["NONE"]
`)
	got := wantCount(t, rep, healthRule, 1)[0]
	if got.Fix != nil {
		t.Fatalf("a fix was described for `test: NONE`: %s", got.Fix.Describe)
	}
	if !strings.Contains(got.NoFix, "NONE") {
		t.Errorf("the refusal does not say what is in the way: %s", got.NoFix)
	}
}
