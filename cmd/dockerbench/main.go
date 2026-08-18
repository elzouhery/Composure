// Command dockerbench measures the Dockerfile engine the same way the YAML one
// was measured: identity, scalar edit, structural edit — against real files.
//
// The headline operation is base-image replacement, because that is what an
// image-search feature actually produces: "swap this FROM, here's the diff."
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/dockerfile"
)

// metricsJSON is the machine-readable shape consumed by the CI gate. Kept
// separate from the display code so a formatting change cannot move a gate.
type metricsJSON struct {
	Corpus            int     `json:"corpus"`
	MultiStage        int     `json:"multi_stage"`
	Heredoc           int     `json:"heredoc"`
	CustomEscape      int     `json:"custom_escape"`
	IdentityPct       float64 `json:"identity_pct"`
	SetBaseAppliedPct float64 `json:"set_base_image_applied_pct"`
	SetBaseMinimalPct float64 `json:"set_base_image_minimal_pct"`
	InsertCleanPct    float64 `json:"insert_after_clean_pct"`
	DeleteCleanPct    float64 `json:"delete_clean_pct"`
}

func main() {
	asJSON := flag.Bool("json", false, "emit JSON instead of the table")
	flag.Parse()

	root := "corpus-repos"
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	files := collect(root)
	for _, dir := range []string{"testdata/dockerfiles"} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			files = append(files, dir+"/"+e.Name())
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no Dockerfiles found")
		os.Exit(1)
	}

	var (
		total, identity           int
		withFrom, fromOK, fromMin int
		insAttempt, insClean      int
		delAttempt, delClean      int
		multiStage, heredoc, esc  int
		fromFail                  []string
	)

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		total++
		f := dockerfile.Parse(src)

		if string(f.Bytes()) == string(src) {
			identity++
		}
		stages := f.Stages()
		if len(stages) > 1 {
			multiStage++
		}
		if f.EscapeChar == '`' {
			esc++
		}
		for _, in := range f.Instructions {
			if in.HasHeredoc {
				heredoc++
				break
			}
		}

		// ---- base image replacement --------------------------------------
		if len(stages) > 0 {
			withFrom++
			out, err := f.SetBaseImage(0, "example.invalid/base:v9.9.9")
			if err == nil {
				fromOK++
				if kind, block, ok := singleBlockDiff(string(src), string(out)); ok && kind == "modify" && len(block) == 1 {
					fromMin++
				} else {
					fromFail = append(fromFail, fmt.Sprintf("%s (kind=%s len=%d)", path, kind, len(block)))
				}
			} else {
				fromFail = append(fromFail, fmt.Sprintf("%s: %v", path, err))
			}
		}

		// ---- insert -------------------------------------------------------
		if i := f.Find("FROM"); i >= 0 {
			insAttempt++
			out, err := f.InsertAfter(i, "LABEL fidelity.probe=\"inserted\"")
			if err == nil {
				if kind, block, ok := singleBlockDiff(string(src), string(out)); ok && kind == "insert" && len(block) == 1 {
					insClean++
				}
			}
		}

		// ---- delete -------------------------------------------------------
		if i := findDeletable(f); i >= 0 {
			delAttempt++
			out, err := f.Delete(i)
			if err == nil {
				if kind, _, ok := singleBlockDiff(string(src), string(out)); ok && kind == "delete" {
					delClean++
				}
			}
		}
	}

	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d) * 100
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(metricsJSON{
			Corpus:            total,
			MultiStage:        multiStage,
			Heredoc:           heredoc,
			CustomEscape:      esc,
			IdentityPct:       pct(identity, total),
			SetBaseAppliedPct: pct(fromOK, withFrom),
			SetBaseMinimalPct: pct(fromMin, fromOK),
			InsertCleanPct:    pct(insClean, insAttempt),
			DeleteCleanPct:    pct(delClean, delAttempt),
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\nDOCKERFILE ENGINE BENCHMARK\n")
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("corpus: %d Dockerfiles  (%d multi-stage, %d with heredocs, %d with custom escape char)\n\n",
		total, multiStage, heredoc, esc)
	fmt.Printf("  %-34s %6.2f%%  (%d/%d)\n", "identity (byte-identical)", pct(identity, total), identity, total)
	fmt.Printf("  %-34s %6.2f%%  (%d/%d)\n", "SetBaseImage applied", pct(fromOK, withFrom), fromOK, withFrom)
	fmt.Printf("  %-34s %6.2f%%  (%d/%d)\n", "SetBaseImage single-line diff", pct(fromMin, fromOK), fromMin, fromOK)
	fmt.Printf("  %-34s %6.2f%%  (%d/%d)\n", "InsertAfter single-block clean", pct(insClean, insAttempt), insClean, insAttempt)
	fmt.Printf("  %-34s %6.2f%%  (%d/%d)\n", "Delete single-block clean", pct(delClean, delAttempt), delClean, delAttempt)

	if len(fromFail) > 0 {
		fmt.Printf("\n  SetBaseImage problems (%d):\n", len(fromFail))
		for i, s := range fromFail {
			if i >= 12 {
				fmt.Printf("    ... and %d more\n", len(fromFail)-12)
				break
			}
			fmt.Printf("    %s\n", s)
		}
	}
	fmt.Println()
}

// findDeletable picks an instruction safe to delete for the benchmark: a LABEL,
// ENV or EXPOSE, never a FROM (which would restructure the build).
func findDeletable(f *dockerfile.File) int {
	for i, in := range f.Instructions {
		switch in.Name {
		case "LABEL", "ENV", "EXPOSE", "WORKDIR":
			return i
		}
	}
	return -1
}

// singleBlockDiff classifies a change as a pure insert, pure delete, or an
// in-place modification of one contiguous run.
func singleBlockDiff(a, b string) (kind string, block []string, ok bool) {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	p := 0
	for p < len(al) && p < len(bl) && al[p] == bl[p] {
		p++
	}
	s := 0
	for s < len(al)-p && s < len(bl)-p && al[len(al)-1-s] == bl[len(bl)-1-s] {
		s++
	}
	aMid, bMid := al[p:len(al)-s], bl[p:len(bl)-s]
	switch {
	case len(aMid) == 0 && len(bMid) == 0:
		return "none", nil, true
	case len(aMid) == 0:
		return "insert", bMid, true
	case len(bMid) == 0:
		return "delete", aMid, true
	case len(aMid) == len(bMid):
		return "modify", bMid, true
	default:
		return "mixed", bMid, false
	}
}

func collect(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		n := d.Name()
		if n == "Dockerfile" || n == "Containerfile" ||
			strings.HasPrefix(n, "Dockerfile.") || strings.HasSuffix(n, ".Dockerfile") {
			if strings.HasSuffix(n, ".dockerignore") {
				return nil
			}
			out = append(out, p)
		}
		return nil
	})
	return out
}
