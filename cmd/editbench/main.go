// Command editbench measures the operation the product actually performs:
// change ONE value in a real compose file and count how many lines the
// resulting diff touches.
//
// Identity (the `fidelity check` metric) proves an engine does no harm when
// idle. This proves it does no collateral damage when working. A user judges
// the tool by the diff they have to put in a pull request, so this is the
// number that decides whether the product is trustworthy.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/strategy"
	goccyast "github.com/goccy/go-yaml/ast"
	goccyparser "github.com/goccy/go-yaml/parser"
)

type score struct {
	name       string
	attempted  int
	succeeded  int
	perfect    int // diff was minimal: one line removed, one added
	totalLines int
	worst      int
	worstPath  string
}

// scoreJSON is the machine-readable shape consumed by the CI gate. Kept
// separate from the display code so a formatting change cannot move a gate.
type scoreJSON struct {
	Strategy   string  `json:"strategy"`
	Attempted  int     `json:"attempted"`
	Applied    int     `json:"applied"`
	Minimal    int     `json:"minimal"`
	MinimalPct float64 `json:"minimal_pct"`
	AvgLines   float64 `json:"avg_lines"`
	Worst      int     `json:"worst"`
	WorstPath  string  `json:"worst_path"`
}

func main() {
	asJSON := flag.Bool("json", false, "emit JSON instead of the table")
	flag.Parse()

	root := "corpus-repos"
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	files, err := corpus.Collect(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Strings(files)

	scores := map[string]*score{}
	for _, s := range strategy.All() {
		scores[s.Name()] = &score{name: s.Name()}
	}

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		svc, ok := firstServiceWithImage(src)
		if !ok {
			continue
		}
		p := []string{"services", svc, "image"}
		const newImage = "example.invalid/replacement:v9.9.9"

		for _, s := range strategy.All() {
			sc := scores[s.Name()]
			sc.attempted++
			out, err := s.Edit(src, p, newImage)
			if err != nil {
				continue
			}
			if !strings.Contains(string(out), newImage) {
				continue // edit silently did not apply
			}
			sc.succeeded++
			n := changedLines(string(src), string(out))
			sc.totalLines += n
			if n <= 2 {
				sc.perfect++
			}
			if n > sc.worst {
				sc.worst, sc.worstPath = n, path
			}
		}
	}

	if *asJSON {
		out := []scoreJSON{}
		for _, s := range strategy.All() {
			sc := scores[s.Name()]
			row := scoreJSON{
				Strategy:  sc.name,
				Attempted: sc.attempted,
				Applied:   sc.succeeded,
				Minimal:   sc.perfect,
				Worst:     sc.worst,
				WorstPath: sc.worstPath,
			}
			if sc.succeeded > 0 {
				row.MinimalPct = float64(sc.perfect) / float64(sc.succeeded) * 100
				row.AvgLines = float64(sc.totalLines) / float64(sc.succeeded)
			}
			out = append(out, row)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\nEDIT BENCHMARK — change one image tag, measure diff size\n")
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("%-18s %9s %9s %11s %11s %8s\n",
		"strategy", "attempted", "applied", "minimal", "avg lines", "worst")
	for _, s := range strategy.All() {
		sc := scores[s.Name()]
		avg := 0.0
		if sc.succeeded > 0 {
			avg = float64(sc.totalLines) / float64(sc.succeeded)
		}
		pct := 0.0
		if sc.succeeded > 0 {
			pct = float64(sc.perfect) / float64(sc.succeeded) * 100
		}
		fmt.Printf("%-18s %9d %9d %10.1f%% %11.1f %8d\n",
			sc.name, sc.attempted, sc.succeeded, pct, avg, sc.worst)
	}
	fmt.Println()
	for _, s := range strategy.All() {
		sc := scores[s.Name()]
		if sc.worstPath != "" {
			fmt.Printf("  %-18s worst case: %d lines changed in %s\n", sc.name, sc.worst, sc.worstPath)
		}
	}
	fmt.Println()
}

// firstServiceWithImage finds a service whose `image` is a plain string scalar.
func firstServiceWithImage(src []byte) (string, bool) {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 {
		return "", false
	}
	root, ok := f.Docs[0].Body.(*goccyast.MappingNode)
	if !ok {
		return "", false
	}
	for _, kv := range root.Values {
		if kv.Key.GetToken().Value != "services" {
			continue
		}
		svcs, ok := kv.Value.(*goccyast.MappingNode)
		if !ok {
			return "", false
		}
		for _, s := range svcs.Values {
			body, ok := s.Value.(*goccyast.MappingNode)
			if !ok {
				continue
			}
			for _, f := range body.Values {
				if f.Key.GetToken().Value == "image" {
					if _, ok := f.Value.(*goccyast.StringNode); ok {
						return s.Key.GetToken().Value, true
					}
				}
			}
		}
	}
	return "", false
}

// changedLines counts lines present in one version and not the other,
// a cheap stand-in for `git diff --numstat`.
func changedLines(a, b string) int {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	count := func(x []string) map[string]int {
		m := map[string]int{}
		for _, l := range x {
			m[l]++
		}
		return m
	}
	am, bm := count(al), count(bl)
	diff := 0
	for l, n := range am {
		if bn := bm[l]; n > bn {
			diff += n - bn
		}
	}
	for l, n := range bm {
		if an := am[l]; n > an {
			diff += n - an
		}
	}
	return diff
}
