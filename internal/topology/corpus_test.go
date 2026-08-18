package topology

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/resolve"
)

const (
	corpusRoot   = "../../corpus-repos"
	largeExample = "../../examples/large/compose.yaml"
)

// TestDeterminism is story 2.1's last acceptance criterion. The graph is a
// pure derivation, so the same project resolved twice must produce the same
// graph — ordering included, because a canvas that reorders itself between two
// identical runs is a canvas nobody can diff.
//
// It runs over real files rather than a fixture: the ordering hazard is Go map
// iteration, and a three-node fixture can be deterministic by luck.
func TestDeterminism(t *testing.T) {
	files := []string{largeExample, "../../examples/webstack/compose.yaml"}
	for _, file := range files {
		t.Run(filepath.Base(filepath.Dir(file)), func(t *testing.T) {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Skipf("example missing: %v", err)
			}
			// Two independent resolves, not one model built twice: map
			// iteration order can differ in the resolver as well.
			first := buildFrom(t, file, src)
			second := buildFrom(t, file, src)

			if !reflect.DeepEqual(first.Nodes(), second.Nodes()) {
				t.Error("node sets differ between two builds")
			}
			if !reflect.DeepEqual(first.Edges(), second.Edges()) {
				t.Error("edge sets differ between two builds")
			}
			if !reflect.DeepEqual(first.Cycles(), second.Cycles()) {
				t.Error("cycle reports differ between two builds")
			}
			if !reflect.DeepEqual(first.Dangling(), second.Dangling()) {
				t.Error("dangling reports differ between two builds")
			}

			a, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(second)
			if err != nil {
				t.Fatal(err)
			}
			if string(a) != string(b) {
				t.Error("the JSON wire shape is not byte-identical between two builds")
			}
		})
	}
}

func buildFrom(t *testing.T, name string, src []byte) *Graph {
	t.Helper()
	p, err := resolve.Bytes(name, src)
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	g, err := Build(p, nil)
	if err != nil {
		t.Fatalf("build %s: %v", name, err)
	}
	return g
}

// TestN3LargeProjectIsFast is the N3 budget, measured rather than assumed: a
// 500-service project must build a topology and compute layers in well under
// two seconds.
//
// The threshold is deliberately a fraction of the budget. The requirement is
// two seconds for resolve *and* first paint, so a derivation that took the
// whole budget on its own would already have failed it.
func TestN3LargeProjectIsFast(t *testing.T) {
	src, err := os.ReadFile(largeExample)
	if err != nil {
		t.Skipf("large example missing: %v", err)
	}
	p, err := resolve.Bytes(largeExample, src)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	services := p.Services().Len()
	if services < 500 {
		t.Fatalf("the performance fixture has %d services, want at least 500", services)
	}

	start := time.Now()
	g, err := Build(p, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	elapsed := time.Since(start)

	if len(g.Nodes()) < services {
		t.Errorf("graph has %d nodes for %d services", len(g.Nodes()), services)
	}
	// 400ms is a fifth of the N3 budget, and it is generous: the measurement on
	// a developer laptop is two orders of magnitude below it. A regression that
	// crosses this line is an algorithmic change, not noise.
	if elapsed > 400*time.Millisecond {
		t.Errorf("built a %d-service topology in %v, want well under 2s", services, elapsed)
	}
	t.Logf("%d services: %d nodes, %d edges, built in %v",
		services, len(g.Nodes()), len(g.Edges()), elapsed)
}

// TestCorpus sweeps the fetched corpus. It asserts two things a unit test
// cannot: that no real-world file panics the builder, and that every node's
// identity is a path that actually addresses something in the resolved model.
//
// The second is the load-bearing one. A node whose Path does not resolve is a
// generated id wearing a path's clothes, and AD-6 forbids exactly that — the
// failure would be silent, because the graph would still render.
func TestCorpus(t *testing.T) {
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files, err := corpus.Collect(corpusRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) == 0 {
		// NOT a skip. `corpus-repos/` absent is a legitimate skip — a fresh
		// clone has not fetched it. `corpus-repos/` present and empty is a
		// BROKEN CACHE: a CI restore that produced a directory and no files,
		// which skipped this sweep and turned the step green while proving
		// exactly nothing. Failing here is the difference between "we did not
		// run the corpus check" and "the corpus check passed".
		t.Fatalf("%s exists but holds no compose files — a broken corpus, not an absent one; run `make corpus`", corpusRoot)
	}

	var built, nodes, edges int
	for _, file := range files {
		p, err := resolve.File(file)
		if err != nil {
			// A file the resolver refuses is not this package's business. The
			// corpus is full of Compose v1 fragments and templates.
			continue
		}
		g, err := Build(p, nil)
		if err != nil {
			t.Errorf("%s: build: %v", file, err)
			continue
		}
		built++
		nodes += len(g.nodes)
		edges += len(g.edges)

		for _, n := range g.Nodes() {
			if len(n.Path) == 0 {
				t.Errorf("%s: a node has an empty path", file)
				continue
			}
			if _, ok := p.At(n.Path); !ok {
				// The one legitimate case: a resource used but never
				// declared, which is a node precisely so the reference does
				// not dangle. It must say so.
				if n.Declared {
					t.Errorf("%s: node %s claims to be declared but does not resolve in the model",
						file, n.Path)
				}
				if !n.External {
					t.Errorf("%s: node %s resolves to nothing and is not marked external",
						file, n.Path)
				}
			}
		}
		for _, e := range g.Edges() {
			if !g.Has(e.From) {
				t.Errorf("%s: edge %s starts at %s, which is not a node", file, e.Kind, e.From)
			}
			if !g.Has(e.To) {
				t.Errorf("%s: edge %s ends at %s, which is not a node", file, e.Kind, e.To)
			}
		}
	}
	if built == 0 {
		t.Fatal("no corpus file resolved; the sweep proved nothing")
	}
	t.Logf("%d of %d corpus files resolved: %d nodes, %d edges", built, len(files), nodes, edges)
}

// TestCorpusDeterminism rebuilds a slice of the corpus twice and compares the
// wire shape. Determinism over one crafted example is weaker evidence than
// determinism over files nobody wrote for this test.
func TestCorpusDeterminism(t *testing.T) {
	if _, err := os.Stat(corpusRoot); err != nil {
		t.Skipf("corpus not fetched (%s); run `make corpus`", corpusRoot)
	}
	files, err := corpus.Collect(corpusRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	const sample = 200
	if len(files) > sample {
		files = files[:sample]
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if _, err := resolve.Bytes(file, src); err != nil {
			continue
		}
		a, errA := json.Marshal(buildFrom(t, file, src))
		b, errB := json.Marshal(buildFrom(t, file, src))
		if errA != nil || errB != nil {
			t.Fatalf("%s: marshal: %v %v", file, errA, errB)
		}
		if string(a) != string(b) {
			t.Fatalf("%s: two builds produced different JSON", file)
		}
	}
}
