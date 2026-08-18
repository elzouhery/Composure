package diagnose

import (
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

const unusedRule = "unused-resource"

func TestUnusedVolume(t *testing.T) {
	rep := run(t, `
services:
  db:
    image: postgres
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata:
  scratch:
`)
	got := wantCount(t, rep, unusedRule, 1)[0]
	if !strings.Contains(got.Message, `"scratch"`) {
		t.Errorf("named the wrong volume: %s", got.Message)
	}
	if got.Severity != SeverityWarning {
		t.Errorf("severity is %s, want warning", got.Severity)
	}
	if got.Anchors[0].Origin.Line != 9 {
		t.Errorf("anchored at line %d, want the declaration on line 9", got.Anchors[0].Origin.Line)
	}
}

func TestUnusedNetwork(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    networks: [front]
networks:
  front:
  back:
`)
	got := wantCount(t, rep, unusedRule, 1)[0]
	if !strings.Contains(got.Message, `network "back"`) {
		t.Errorf("named the wrong network: %s", got.Message)
	}
}

// The implicit default network is Compose's, not the author's leftover.
func TestDefaultNetworkIsExempt(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
networks:
  default:
    driver: bridge
`)
	wantNone(t, rep, unusedRule)
}

// A resource used only under another profile is doing its job. The active graph
// cannot see that, which is exactly why the rule consults a graph built with
// every declared profile active.
func TestResourceUsedOnlyUnderAnotherProfileIsNotUnused(t *testing.T) {
	const src = `
services:
  web:
    image: nginx
  seed:
    image: seeder
    profiles: [seed]
    volumes:
      - fixtures:/fixtures
volumes:
  fixtures:
`
	wantNone(t, run(t, src), unusedRule)
	wantNone(t, run(t, src, withProfiles("seed")), unusedRule)
}

// External resources report lower: something outside this project may use them.
func TestExternalUnusedResourceIsAHint(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
volumes:
  shared:
    external: true
`)
	got := wantCount(t, rep, unusedRule, 1)[0]
	if got.Severity != SeverityHint {
		t.Errorf("severity is %s, want hint for an external resource", got.Severity)
	}
	if !strings.Contains(got.Message, "external") {
		t.Errorf("message does not mention that it is external: %s", got.Message)
	}
}

// A resource referenced but never declared is a different problem and is not
// this rule's.
func TestUndeclaredResourceIsNotReportedAsUnused(t *testing.T) {
	rep := run(t, `
services:
  db:
    image: postgres
    volumes:
      - nowhere:/data
`)
	wantNone(t, rep, unusedRule)
}

// The topology invariant the `!n.Declared` guard rests on.
//
// That guard cannot fire today: topology creates an undeclared volume or network
// node only in resolveTarget, which creates it because an edge points at it, so
// the usage check above already skips every such node. Removing the guard fails
// no test, which is exactly why this one exists — it pins the REASON rather than
// the guard, so that if topology ever produces an undeclared node with no edge,
// the guard becomes load-bearing and something says so out loud instead of the
// rule quietly telling a user to delete a declaration that was never there.
func TestUndeclaredResourcesAreAlwaysEdgeTargets(t *testing.T) {
	project, err := resolve.BytesWith("compose.yaml", []byte(`
services:
  db:
    image: postgres
    volumes:
      - nowhere:/data
    networks: [absent]
`), resolve.Options{Env: map[string]string{}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}

	targets := usedTargets(graph)
	var undeclared int
	for _, n := range graph.Nodes() {
		if n.Kind != topology.NodeVolume && n.Kind != topology.NodeNetwork {
			continue
		}
		if n.Declared {
			continue
		}
		undeclared++
		if !targets[pathKey(n.Path)] {
			t.Errorf("undeclared %s %q is in the graph with no edge pointing at it. "+
				"The `!n.Declared` guard in this rule is now load-bearing and needs a test of its own.",
				n.Kind, n.Name)
		}
	}
	if undeclared == 0 {
		t.Fatal("the fixture produced no undeclared resource nodes; it no longer tests what it claims")
	}
}

// A bind mount is not a named volume and must not make a declared volume of the
// same-looking name count as used, nor be reported itself.
func TestBindMountsAreNotVolumes(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    volumes:
      - ./conf:/etc/nginx/conf.d:ro
`)
	wantNone(t, rep, unusedRule)
}

func TestUnusedFixDeletesTheDeclaration(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\nvolumes:\n  # left behind by a migration\n  scratch:\n"
	rep := run(t, src)
	got := wantCount(t, rep, unusedRule, 1)[0]
	if got.Fix == nil {
		t.Fatalf("no fix described: %s", got.NoFix)
	}
	if got.Fix.Operation != FixDeleteKey || got.Fix.Path.String() != "volumes.scratch" {
		t.Errorf("fix is %s %s", got.Fix.Operation, got.Fix.Path)
	}
	// The attached comment travels with the node, because that is what
	// strategy.DeleteKey does — and the reported range has to be the range the
	// engine would splice.
	covered := src[got.Fix.Range.Start:got.Fix.Range.End]
	if !strings.Contains(covered, "left behind") {
		t.Errorf("range %q does not include the comment DeleteKey would remove", covered)
	}
}
