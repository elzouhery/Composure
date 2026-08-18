// Command structbench measures insertion and deletion — the hard half of editing.
//
// The pass condition is strict and worth stating plainly: after an insert, the
// output must equal the input with exactly ONE contiguous block of lines added
// and nothing else touched. After a delete, exactly one contiguous block
// removed and nothing else touched.
//
// Anything weaker lets collateral damage hide. "The file still parses and the
// value is there" is satisfied by an engine that also silently reindented forty
// unrelated lines.
package main

import (
	"encoding/json"
	"errors"
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

type opScore struct {
	name      string
	attempted int
	applied   int
	refused   int // engine declined rather than risk corruption
	clean     int // exactly one contiguous block changed, nothing else
	reparsed  int // output still parses as YAML
	correct   int // the intended semantic change is present in the reparsed doc
	failures  []string
}

func (s *opScore) rate(n int) float64 {
	if s.applied == 0 {
		return 0
	}
	return float64(n) / float64(s.applied) * 100
}

// opJSON is the machine-readable shape consumed by the CI gate. Kept separate
// from the display code so a formatting change cannot move a gate.
type opJSON struct {
	Operation   string  `json:"operation"`
	Attempted   int     `json:"attempted"`
	Applied     int     `json:"applied"`
	Refused     int     `json:"refused"`
	Clean       int     `json:"clean"`
	CleanPct    float64 `json:"clean_pct"`
	ReparsedPct float64 `json:"reparses_pct"`
	CorrectPct  float64 `json:"correct_pct"`
	Failures    int     `json:"failures"`
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
	for _, dir := range []string{"testdata/adversarial", "testdata/edge"} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			files = append(files, dir+"/"+e.Name())
		}
	}
	sort.Strings(files)

	sp := strategy.Splice{}
	ins := &opScore{name: "InsertKey"}
	entry := &opScore{name: "InsertEntry"}
	del := &opScore{name: "DeleteKey"}

	const probeKey = "x-fidelity-probe"
	const probeVal = "inserted-by-harness"
	// A probe entry that survives as a bare scalar and cannot collide with a
	// real one: no `#`, no leading `-`, nothing YAML reads back as a number or a
	// boolean, and a hostname in a reserved TLD.
	const probeEntry = "probe.invalid"

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		services := serviceNames(src)
		if len(services) == 0 {
			continue
		}

		// ---- insert into the first service -------------------------------
		target := services[0]
		ins.attempted++
		out, err := sp.InsertKey(src, []string{"services", target}, probeKey, probeVal)
		if err == strategy.ErrFlowStyle {
			ins.refused++
		} else if err == nil {
			ins.applied++
			kind, block, ok := singleBlockDiff(string(src), string(out))
			if ok && kind == "insert" && len(block) == 1 {
				ins.clean++
			} else {
				ins.failures = append(ins.failures,
					fmt.Sprintf("%s: kind=%s blocklen=%d ok=%v", path, kind, len(block), ok))
			}
			if _, perr := goccyparser.ParseBytes(out, goccyparser.ParseComments); perr == nil {
				ins.reparsed++
				if hasKey(out, []string{"services", target, probeKey}) {
					ins.correct++
				}
			}
		}

		// ---- append an entry to the first block sequence --------------------
		//
		// Story 7.5's operation, measured where every other structural operation
		// is measured. It had no corpus number at all: the story's acceptance
		// criterion asked for this key and the epic shipped with the evidence in
		// a Go test instead, which measures a different thing (the write path)
		// and is not gated against a baseline.
		//
		// The target is chosen from the file rather than fixed, because a fixed
		// path would silently measure nothing on most of the corpus. Flow
		// sequences and mapping-entry sequences are excluded by the finder: both
		// are refusals or a different shape, and a sweep of refusals measures the
		// refusal rather than the write.
		if seqPath, ok := firstBlockSequence(src); ok {
			entry.attempted++
			out, err := sp.InsertSequenceEntry(src, seqPath, probeEntry)
			if errors.Is(err, strategy.ErrFlowStyle) || errors.Is(err, strategy.ErrNotASequence) {
				entry.refused++
			} else if err == nil {
				entry.applied++
				kind, block, ok := singleBlockDiff(string(src), string(out))
				if ok && kind == "insert" && len(block) == 1 {
					entry.clean++
				} else {
					entry.failures = append(entry.failures,
						fmt.Sprintf("%s: kind=%s blocklen=%d ok=%v", path, kind, len(block), ok))
				}
				if _, perr := goccyparser.ParseBytes(out, goccyparser.ParseComments); perr == nil {
					entry.reparsed++
					// Correct means the entry is the LAST one of the sequence it
					// was appended to — not merely that the text is somewhere in
					// the file. An entry written at the wrong column still
					// parses; it just belongs to something else.
					if lastEntryIs(out, seqPath, probeEntry) {
						entry.correct++
					}
				}
			}
		}

		// ---- delete a service (needs at least two so the file stays valid) --
		if len(services) >= 2 {
			victim := services[len(services)-1]
			del.attempted++
			out, err := sp.DeleteKey(src, []string{"services", victim})
			if err == strategy.ErrFlowStyle {
				del.refused++
			} else if err == nil {
				del.applied++
				kind, _, ok := singleBlockDiff(string(src), string(out))
				if ok && kind == "delete" {
					del.clean++
				} else {
					del.failures = append(del.failures,
						fmt.Sprintf("%s: kind=%s ok=%v", path, kind, ok))
				}
				if _, perr := goccyparser.ParseBytes(out, goccyparser.ParseComments); perr == nil {
					del.reparsed++
					if !hasKey(out, []string{"services", victim}) {
						del.correct++
					}
				}
			}
		}
	}

	if *asJSON {
		out := []opJSON{}
		for _, s := range []*opScore{ins, entry, del} {
			out = append(out, opJSON{
				Operation:   s.name,
				Attempted:   s.attempted,
				Applied:     s.applied,
				Refused:     s.refused,
				Clean:       s.clean,
				CleanPct:    s.rate(s.clean),
				ReparsedPct: s.rate(s.reparsed),
				CorrectPct:  s.rate(s.correct),
				Failures:    len(s.failures),
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\nSTRUCTURAL EDIT BENCHMARK — splice engine\n")
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("%-12s %9s %8s %8s %19s %10s %9s\n",
		"operation", "attempted", "applied", "refused", "single-block clean", "reparses", "correct")
	for _, s := range []*opScore{ins, entry, del} {
		fmt.Printf("%-12s %9d %8d %8d %18.1f%% %9.1f%% %8.1f%%\n",
			s.name, s.attempted, s.applied, s.refused,
			s.rate(s.clean), s.rate(s.reparsed), s.rate(s.correct))
	}
	fmt.Println()
	for _, s := range []*opScore{ins, entry, del} {
		if len(s.failures) == 0 {
			continue
		}
		fmt.Printf("  %s — %d files with collateral damage:\n", s.name, len(s.failures))
		for i, f := range s.failures {
			if i >= 10 {
				fmt.Printf("    ... and %d more\n", len(s.failures)-10)
				break
			}
			fmt.Printf("    %s\n", f)
		}
	}
	fmt.Println()
}

// singleBlockDiff reports whether b differs from a by exactly one contiguous
// run of inserted or deleted lines, and returns that run.
//
// It works by matching a common prefix and a common suffix; whatever remains in
// the middle is the change. If BOTH sides have leftover middle content, the
// edit modified lines rather than purely adding or removing them, which for a
// structural operation means collateral damage.
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
	aMid := al[p : len(al)-s]
	bMid := bl[p : len(bl)-s]

	switch {
	case len(aMid) == 0 && len(bMid) == 0:
		return "none", nil, true
	case len(aMid) == 0:
		return "insert", bMid, true
	case len(bMid) == 0:
		return "delete", aMid, true
	default:
		return "mixed", aMid, false
	}
}

// firstBlockSequence finds a `services.<name>.<key>` whose value is a non-empty
// BLOCK sequence of scalars — the shape the operation is for.
//
// It is the same finder internal/edit's sweep uses, in the shape this package
// speaks: a []string path, so cmd/structbench keeps depending on nothing but
// the engine.
func firstBlockSequence(src []byte) ([]string, bool) {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 {
		return nil, false
	}
	root, ok := f.Docs[0].Body.(*goccyast.MappingNode)
	if !ok {
		return nil, false
	}
	for _, kv := range root.Values {
		if kv.Key.GetToken().Value != "services" {
			continue
		}
		svcs, ok := kv.Value.(*goccyast.MappingNode)
		if !ok {
			return nil, false
		}
		for _, svc := range svcs.Values {
			body, ok := svc.Value.(*goccyast.MappingNode)
			if !ok {
				continue
			}
			for _, field := range body.Values {
				seq, ok := field.Value.(*goccyast.SequenceNode)
				if !ok || seq.IsFlowStyle || len(seq.Values) == 0 {
					continue
				}
				if _, isMap := seq.Values[0].(*goccyast.MappingNode); isMap {
					continue
				}
				return []string{"services", svc.Key.GetToken().Value, field.Key.GetToken().Value}, true
			}
		}
	}
	return nil, false
}

// lastEntryIs reports whether the sequence at path ends with an entry reading
// exactly value.
func lastEntryIs(src []byte, path []string, value string) bool {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 {
		return false
	}
	node := f.Docs[0].Body
	for _, seg := range path {
		m, ok := node.(*goccyast.MappingNode)
		if !ok {
			return false
		}
		found := false
		for _, kv := range m.Values {
			if kv.Key.GetToken().Value == seg {
				node, found = kv.Value, true
				break
			}
		}
		if !found {
			return false
		}
	}
	seq, ok := node.(*goccyast.SequenceNode)
	if !ok || len(seq.Values) == 0 {
		return false
	}
	last := seq.Values[len(seq.Values)-1].GetToken()
	return last != nil && last.Value == value
}

func serviceNames(src []byte) []string {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 {
		return nil
	}
	root, ok := f.Docs[0].Body.(*goccyast.MappingNode)
	if !ok {
		return nil
	}
	for _, kv := range root.Values {
		if kv.Key.GetToken().Value != "services" {
			continue
		}
		m, ok := kv.Value.(*goccyast.MappingNode)
		if !ok {
			return nil
		}
		var names []string
		for _, s := range m.Values {
			// Only services whose body is a mapping can host an inserted key.
			if _, ok := s.Value.(*goccyast.MappingNode); ok {
				names = append(names, s.Key.GetToken().Value)
			}
		}
		return names
	}
	return nil
}

func hasKey(src []byte, path []string) bool {
	f, err := goccyparser.ParseBytes(src, goccyparser.ParseComments)
	if err != nil || len(f.Docs) == 0 {
		return false
	}
	node := f.Docs[0].Body
	for _, seg := range path {
		m, ok := node.(*goccyast.MappingNode)
		if !ok {
			return false
		}
		found := false
		for _, kv := range m.Values {
			if kv.Key.GetToken().Value == seg {
				node = kv.Value
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
