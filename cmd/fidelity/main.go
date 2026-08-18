// Command fidelity measures whether a YAML round-trip engine can edit a
// docker-compose file without damaging it.
//
//	fidelity fetch  <dir>          shallow-clone public repos into <dir>
//	fidelity check  <dir> [-v]     score every compose file under <dir>
//
// The headline metric is the identity pass rate: parse a file, write it back
// with no semantic change, and require the output to be byte-identical to the
// input. An engine that cannot do that cannot be trusted to edit a file either.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/report"
	"github.com/elzouhery/composure/internal/strategy"
)

type stratScore struct {
	Name        string                `json:"strategy"`
	Total       int                   `json:"total"`
	ParseErrors int                   `json:"parse_errors"`
	Identical   int                   `json:"identical"`
	Damaged     int                   `json:"damaged"`
	PassRate    float64               `json:"pass_rate"`
	Defects     map[report.Defect]int `json:"defects"`
	Worst       []report.Result       `json:"-"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "fetch":
		fs := flag.NewFlagSet("fetch", flag.ExitOnError)
		_ = fs.Parse(os.Args[2:])
		dir := arg(fs, 0, "corpus-repos")
		if err := corpus.Fetch(dir); err != nil {
			die(err)
		}
		files, _ := corpus.Collect(dir)
		fmt.Printf("corpus: %d compose files under %s\n", len(files), dir)
	case "check":
		fs := flag.NewFlagSet("check", flag.ExitOnError)
		verbose := fs.Bool("v", false, "list every damaged file")
		asJSON := fs.Bool("json", false, "emit JSON")
		limit := fs.Int("limit", 0, "cap the number of files checked (0 = all)")
		_ = fs.Parse(os.Args[2:])
		dir := arg(fs, 0, "testdata")
		check(dir, *verbose, *asJSON, *limit)
	default:
		usage()
	}
}

func check(dir string, verbose, asJSON bool, limit int) {
	files, err := corpus.Collect(dir)
	if err != nil {
		die(err)
	}
	// Include hand-written adversarial files even though they don't match
	// compose naming conventions in every case.
	extra, _ := filepath.Glob(filepath.Join(dir, "adversarial", "*.yml"))
	files = append(files, extra...)
	files = dedupe(files)
	sort.Strings(files)
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	if len(files) == 0 {
		die(fmt.Errorf("no compose files found under %s", dir))
	}

	var scores []stratScore
	for _, s := range strategy.All() {
		sc := stratScore{Name: s.Name(), Defects: map[report.Defect]int{}}
		for _, path := range files {
			src, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			sc.Total++
			out, err := s.Identity(src)
			if err != nil {
				sc.ParseErrors++
				continue
			}
			res := report.Result{
				Path:      path,
				Identical: string(src) == string(out),
				InBytes:   len(src),
				OutBytes:  len(out),
			}
			if res.Identical {
				sc.Identical++
			} else {
				sc.Damaged++
				res.Defects = report.Classify(string(src), string(out))
				for _, d := range res.Defects {
					sc.Defects[d]++
				}
				if len(sc.Worst) < 2000 {
					sc.Worst = append(sc.Worst, res)
				}
			}
		}
		if sc.Total > 0 {
			sc.PassRate = float64(sc.Identical) / float64(sc.Total) * 100
		}
		scores = append(scores, sc)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(scores)
		return
	}

	fmt.Printf("\nCORPUS: %d compose files under %s\n", len(files), dir)
	fmt.Println(strings.Repeat("=", 78))
	for _, sc := range scores {
		fmt.Printf("\n%-18s  identity pass rate: %6.2f%%   (%d/%d identical, %d damaged, %d parse errors)\n",
			sc.Name, sc.PassRate, sc.Identical, sc.Total, sc.Damaged, sc.ParseErrors)
		if len(sc.Defects) > 0 {
			fmt.Println("  defect breakdown (files affected):")
			for _, d := range report.Ordered() {
				if n := sc.Defects[d]; n > 0 {
					pct := float64(n) / float64(sc.Total) * 100
					fmt.Printf("    %-26s %5d  %5.1f%%  %s\n", d, n, pct, bar(pct))
				}
			}
		}
		if verbose && len(sc.Worst) > 0 {
			fmt.Println("  damaged files:")
			for _, r := range sc.Worst {
				if len(sc.Worst) > 25 {
					break
				}
				fmt.Printf("    %-52s %s\n", trim(r.Path, 52), joinDefects(r.Defects))
			}
		}
	}
	fmt.Println()
	verdict(scores)
}

func verdict(scores []stratScore) {
	fmt.Println(strings.Repeat("=", 78))
	fmt.Println("VERDICT")
	for _, sc := range scores {
		var msg string
		switch {
		case sc.PassRate >= 99.5:
			msg = "VIABLE — safe to build the editor on this engine"
		case sc.PassRate >= 90:
			msg = "MARGINAL — would need per-defect remediation before shipping"
		default:
			msg = "NOT VIABLE as a round-trip engine"
		}
		fmt.Printf("  %-18s %6.2f%%  %s\n", sc.Name, sc.PassRate, msg)
	}
	fmt.Println()
}

func bar(pct float64) string {
	n := int(pct / 4)
	if n > 25 {
		n = 25
	}
	return strings.Repeat("#", n)
}

func joinDefects(ds []report.Defect) string {
	s := make([]string, len(ds))
	for i, d := range ds {
		s[i] = string(d)
	}
	return strings.Join(s, ", ")
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n+3:]
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func arg(fs *flag.FlagSet, i int, def string) string {
	if fs.NArg() > i {
		return fs.Arg(i)
	}
	return def
}

func usage() {
	fmt.Fprintln(os.Stderr, `fidelity — measure YAML round-trip damage on real docker-compose files

  fidelity fetch <dir>              shallow-clone public repos into <dir>
  fidelity check <dir> [-v] [-json] [-limit N]

The headline metric is the identity pass rate: parse a file, write it back with
no semantic change, require byte-identical output.`)
	os.Exit(2)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
