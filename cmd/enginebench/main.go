// Command enginebench measures the four engines the fidelity benchmarks never
// touch — internal/resolve, internal/topology, internal/diagnose and
// internal/edit — over the same corpus, and emits the numbers `make gate`
// baselines.
//
// WHY THIS EXISTS.
//
// benchmarks/baseline.json held fifteen metrics and every one of them came from
// dockerbench, editbench, fidelity or structbench. Those four measure the
// SPLICE ENGINES, which is right — they are what makes this product different —
// but it left resolve, topology, diagnose and edit with no gated number at all.
// A change that halved the share of the corpus that resolves, or that stripped
// the Origin off every graph node, moved nothing the gate could see. That is
// retro action item X4, and it is story 1.5's third criterion arriving by the
// only route that can be honest about it.
//
// DAEMON-FREE, AND THAT IS THE WHOLE DESIGN CONSTRAINT.
//
// The differential harness is the obvious thing to baseline and it is
// deliberately not baselined, for a reason spelled out at length above `gate`
// in the Makefile: it needs a Docker daemon, so it measures the machine as much
// as the code, and a baselined metric that SKIPS is a gate that passes
// vacuously. Every metric here is a pure function of the corpus and the code.
// Nothing below forks a process, opens a socket or reads the host environment —
// resolution runs with IgnoreHostEnv so that the same commit measures the same
// on two machines, which is exactly the property the differential cannot offer.
//
// NO PERCENTAGE OVER AN EMPTY SET.
//
// Every rate here is a fraction, and a fraction with no denominator is the
// oldest way to report 100% while measuring nothing — the same shape as the
// differential's "100% of 74 projects" for a run that never merged two files.
// So every denominator is checked before it is divided by, and a sweep whose
// subject disappeared is a hard failure of this command rather than a perfect
// score. Two denominators are additionally emitted as gated metrics, because
// "the sweep ran and its subject shrank by half" is a regression the gate
// should see and a non-zero check cannot.
//
// Usage:
//
//	enginebench [-json] [corpus-dir]
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/diagnose"
	"github.com/elzouhery/composure/internal/edit"
	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

func main() {
	asJSON := flag.Bool("json", false, "emit JSON instead of the table")
	flag.Parse()

	root := "corpus-repos"
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	files, err := corpus.Collect(root)
	if err != nil {
		fatalf("collecting %s: %v", root, err)
	}
	if len(files) == 0 {
		// Not a skip, and not a zero score. `corpus-repos` absent is a fresh
		// clone; `corpus-repos` present and empty is a broken cache, and the
		// difference between "the sweep did not run" and "the sweep passed" is
		// the entire point of this command.
		fatalf("%s holds no compose files — run `make corpus`", root)
	}

	rep := run(files)
	if problem := rep.degenerate(); problem != "" {
		fatalf("%s — every rate below would be a percentage of nothing", problem)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fatalf("%v", err)
		}
		return
	}
	rep.print()
}

// report is the wire shape cmd/gate consumes. Counts and rates are both here:
// a rate is unreadable without the size of the set it is over.
type report struct {
	Corpus int `json:"corpus"`

	// resolve
	Resolved       int     `json:"resolve_resolved"`
	Refused        int     `json:"resolve_refused"`
	ResolvedPct    float64 `json:"resolve_resolved_pct"`
	Leaves         int     `json:"resolve_leaves"`
	LeavesPositive int     `json:"resolve_leaves_with_origin"`
	ProvenancePct  float64 `json:"resolve_leaf_provenance_pct"`

	// topology
	Graphs        int     `json:"topology_graphs"`
	GraphsPct     float64 `json:"topology_built_pct"`
	Nodes         int     `json:"topology_nodes"`
	NodesPositive int     `json:"topology_nodes_with_origin"`
	NodeOriginPct float64 `json:"topology_node_origin_pct"`

	// diagnose
	Reports        int     `json:"diagnose_reports"`
	ReportsPct     float64 `json:"diagnose_analyzed_pct"`
	Findings       int     `json:"diagnose_findings"`
	Anchored       int     `json:"diagnose_findings_anchored"`
	AnchoredPct    float64 `json:"diagnose_anchored_pct"`
	Anchors        int     `json:"diagnose_anchors"`
	AnchorsOnBytes int     `json:"diagnose_anchors_on_real_bytes"`
	AnchorBytesPct float64 `json:"diagnose_anchor_bytes_pct"`

	// edit
	EditAttempted int `json:"edit_attempted"`
	EditRefused   int `json:"edit_refused"`
	// NotLocatable is an image scalar the resolved model addresses and the
	// splice engine cannot: a document whose top level the locate walk reads
	// as something other than a mapping, most often. It is reported and NOT
	// gated as a rate — it is a property of internal/strategy's locate path,
	// which fidelity, editbench and structbench already gate five ways, and a
	// second floor on it here would gate the same engine twice while pretending
	// to measure the write path above it.
	NotLocatable int     `json:"edit_not_locatable"`
	Edited       int     `json:"edit_edited"`
	PreviewEqual int     `json:"edit_preview_equals_apply"`
	PreviewPct   float64 `json:"edit_preview_equals_apply_pct"`
	StaleStaged  int     `json:"edit_stale_staged"`
	StaleRefused int     `json:"edit_stale_refused"`
	StalePct     float64 `json:"edit_stale_refused_pct"`

	// Defects are the individual failures behind any rate below 100, so a
	// number that moves can be read without re-running the sweep by hand.
	Defects []string `json:"defects,omitempty"`
}

// degenerate names the first denominator that would make a rate meaningless.
// Each of these is a sweep whose subject vanished — a corpus that stopped
// resolving, a project that stopped graphing, a rule set that went silent —
// and every one of them would otherwise report a flawless percentage.
func (r *report) degenerate() string {
	switch {
	case r.Resolved == 0:
		return "not one corpus file resolved"
	case r.Leaves == 0:
		return "the resolved corpus holds no leaf values"
	case r.Graphs == 0:
		return "no resolved project produced a topology graph"
	case r.Nodes == 0:
		return "the graphs hold no nodes"
	case r.Reports == 0:
		return "no project could be diagnosed"
	case r.Findings == 0:
		return "the whole corpus produced no finding, so no anchor was checked"
	case r.Anchors == 0:
		return "no finding carried an anchor"
	case r.Edited == 0:
		return "no corpus file could be edited"
	case r.StaleStaged == 0:
		return "no stale edit was staged"
	}
	return ""
}

// defect records one failure, capped so that a systemic break prints a summary
// rather than 40,000 lines.
func (r *report) defect(format string, args ...any) {
	const max = 40
	if len(r.Defects) < max {
		r.Defects = append(r.Defects, fmt.Sprintf(format, args...))
		return
	}
	if len(r.Defects) == max {
		r.Defects = append(r.Defects, "... and more; rerun with the sweep in a debugger")
	}
}

func run(files []string) *report {
	r := &report{Corpus: len(files)}
	work, err := os.MkdirTemp("", "enginebench")
	if err != nil {
		fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(work)

	// One read of each source file, shared by the anchor check. Reading a file
	// per anchor would turn a linear sweep quadratic on the projects that
	// produce the most findings.
	sources := map[string][]byte{}
	readSource := func(path string) []byte {
		if b, ok := sources[path]; ok {
			return b
		}
		b, err := os.ReadFile(path)
		if err != nil {
			b = nil
		}
		sources[path] = b
		return b
	}

	for _, file := range files {
		// IgnoreHostEnv is load-bearing: with the process environment in play
		// the same file resolves differently on two machines and a baselined
		// number becomes a fact about somebody's shell.
		p, err := resolve.Load(resolve.Options{Files: []string{file}, IgnoreHostEnv: true})
		if err != nil {
			// A refusal is a correct outcome, not a defect. 60 of the corpus's
			// files are Compose v1 fragments and refusing them is the
			// documented behaviour — see resolve.ErrLegacyFormat.
			r.Refused++
			continue
		}
		r.Resolved++

		measureResolve(r, file, p)
		g := measureTopology(r, file, p)
		measureDiagnose(r, file, p, g, readSource)
		measureEdit(r, file, p, work)
	}

	r.ResolvedPct = pct(r.Resolved, r.Resolved+r.Refused)
	r.ProvenancePct = pct(r.LeavesPositive, r.Leaves)
	r.GraphsPct = pct(r.Graphs, r.Resolved)
	r.NodeOriginPct = pct(r.NodesPositive, r.Nodes)
	r.ReportsPct = pct(r.Reports, r.Resolved)
	r.AnchoredPct = pct(r.Anchored, r.Findings)
	r.AnchorBytesPct = pct(r.AnchorsOnBytes, r.Anchors)
	r.PreviewPct = pct(r.PreviewEqual, r.Edited)
	r.StalePct = pct(r.StaleRefused, r.StaleStaged)
	return r
}

// measureResolve is R1.8 as a number: every leaf value knows where it was
// written. A leaf is a scalar or an explicit null — the values a user reads and
// an inspector puts a cursor on. Origin.IsZero demands a line and a column, not
// merely a filename, so a positionless origin cannot count toward this.
func measureResolve(r *report, file string, p *resolve.Project) {
	p.Walk(func(path resolve.Path, v *resolve.Value) bool {
		switch v.Kind() {
		case resolve.KindScalar, resolve.KindNull:
		default:
			return true
		}
		r.Leaves++
		if v.Origin().IsZero() {
			r.defect("%s: %s has no position", file, path)
			return true
		}
		r.LeavesPositive++
		return true
	})
}

// measureTopology asks two things: does the graph build at all, and can every
// node say where it was declared. The second is what makes a node clickable —
// a node with no Origin is a box that cannot open the line that created it.
func measureTopology(r *report, file string, p *resolve.Project) *topology.Graph {
	g, err := topology.Build(p, nil)
	if err != nil {
		r.defect("%s: topology.Build: %v", file, err)
		return nil
	}
	r.Graphs++
	for _, n := range g.Nodes() {
		r.Nodes++
		if n.Origin.IsZero() {
			r.defect("%s: node %s has no declaration position", file, n.Path)
			continue
		}
		r.NodesPositive++
	}
	return g
}

// measureDiagnose is AD-7 as a number: a finding that cannot say where it is
// does not exist. Two rates, because "carries an anchor" and "the anchor
// addresses a real byte" are different claims and only the second one is worth
// anything to a reader with the file open. An anchor pointing at line 400 of a
// 40-line file is a finding nobody can act on, and it is exactly the confident
// wrong answer this engine is prone to.
func measureDiagnose(r *report, file string, p *resolve.Project, g *topology.Graph, read func(string) []byte) {
	if g == nil {
		return
	}
	rep, err := diagnose.AnalyzeProject(file, p, nil)
	if err != nil {
		r.defect("%s: diagnose: %v", file, err)
		return
	}
	r.Reports++
	for _, f := range rep.Findings {
		r.Findings++
		if len(f.Anchors) > 0 {
			r.Anchored++
		} else {
			r.defect("%s: finding %s carries no anchor", file, f.Rule)
		}
		for _, a := range f.Anchors {
			r.Anchors++
			if why := addressesRealBytes(a.Origin, read); why != "" {
				r.defect("%s: %s anchor %q %s", file, f.Rule, a.Label, why)
				continue
			}
			r.AnchorsOnBytes++
		}
	}
}

// addressesRealBytes reports why an origin does not address a position that
// exists in the file it names, or "" when it does.
func addressesRealBytes(o resolve.Origin, read func(string) []byte) string {
	if o.IsZero() {
		return "has no position"
	}
	src := read(o.File)
	if src == nil {
		return fmt.Sprintf("names %s, which cannot be read", o.File)
	}
	lines := strings.Split(string(src), "\n")
	if o.Line < 1 || o.Line > len(lines) {
		return fmt.Sprintf("names line %d of a %d-line file", o.Line, len(lines))
	}
	// Column is 1-based and may sit one past the last character — the position
	// just after a value is a real place to put a cursor.
	if o.Column < 1 || o.Column > len(lines[o.Line-1])+1 {
		return fmt.Sprintf("names column %d of a %d-character line", o.Column, len(lines[o.Line-1]))
	}
	return ""
}

// measureEdit checks the two properties of the write path that editbench does
// not and cannot.
//
// editbench measures the ENGINE: a scalar splice is a two-line diff. That is
// already gated. What is not gated is everything between the reader's keystroke
// and the disk, and it has two promises of its own:
//
//   - PREVIEW NEVER LIES. Preview and Apply differ in one boolean, so the diff
//     shown and the bytes written must be the same answer. A preview that
//     disagrees with its own apply is unfalsifiable by the reader — they
//     approved a diff nobody produced.
//   - A STALE RANGE IS DISCARDED (AD-19). An operation staged against bytes
//     that have since changed is refused, never rebased. Rebasing guesses what
//     the reader meant about a file they have since edited, and a wrong guess
//     writes damage into it.
//
// Both are measured on a COPY. A benchmark that edited the corpus in place
// would invalidate every later `make gate` on the machine that ran it.
func measureEdit(r *report, file string, p *resolve.Project, work string) {
	path, ok := firstImageScalar(p)
	if !ok {
		return
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return
	}
	r.EditAttempted++

	const replacement = "example.invalid/enginebench:v0.0.0"
	op := edit.Op{Operation: edit.OpReplaceScalar, At: path, Value: replacement}

	copyTo := filepath.Join(work, "compose.yaml")
	if err := os.WriteFile(copyTo, src, 0o644); err != nil {
		fatalf("writing the work copy: %v", err)
	}

	prev, err := edit.Preview(edit.Request{File: copyTo, Ops: []edit.Op{op}})
	if err != nil {
		if edit.Refused(err) {
			// A refusal wrote nothing, which is the correct outcome for a flow
			// mapping or a multi-line scalar. It is not a defect and it is not
			// a pass either — it leaves the denominator.
			r.EditRefused++
			return
		}
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "not a mapping") {
			// The locate walk cannot address the path the resolved model can.
			// Counted rather than complained about — see NotLocatable. The
			// match is on the message because internal/strategy raises these
			// with fmt.Errorf and no sentinel to test with errors.Is; a
			// sentinel there would be the better fix and belongs in that
			// package, not in a measurement harness.
			r.NotLocatable++
			return
		}
		r.defect("%s: preview: %v", file, err)
		return
	}
	// Preview must not have touched the file. This is checked before the apply
	// rather than asserted in prose, because "preview never writes" is a claim
	// about a code path nobody watches.
	if after, err := os.ReadFile(copyTo); err != nil || string(after) != string(src) {
		r.defect("%s: preview WROTE to the file", file)
	}

	// The stale check, staged against the range preview reported and bytes
	// that were never there. Same offsets, different content: the case a
	// length-preserving edit by somebody else produces, and the one an offset
	// comparison alone walks straight past.
	if len(prev.Ops) > 0 {
		o := prev.Ops[0]
		r.StaleStaged++
		stale := op
		stale.Expect = &edit.Expect{Start: o.Range.Start, End: o.Range.End, Text: o.Before + "-not-what-was-there"}
		res, err := edit.Apply(edit.Request{File: copyTo, Ops: []edit.Op{stale}, Write: true})
		switch {
		case err == nil:
			r.defect("%s: a stale range was APPLIED (%d ops)", file, len(res.Ops))
		case errors.Is(err, edit.ErrStaleRange):
			r.StaleRefused++
		default:
			r.defect("%s: a stale range failed with %v, not a stale-range refusal", file, err)
		}
		// Whatever happened, restore the copy: a stale apply that wrote would
		// otherwise make the comparison below measure the wrong file.
		if err := os.WriteFile(copyTo, src, 0o644); err != nil {
			fatalf("restoring the work copy: %v", err)
		}
	}

	applied, err := edit.Apply(edit.Request{File: copyTo, Ops: []edit.Op{op}, Write: true})
	if err != nil {
		if edit.Refused(err) {
			// Preview accepted it and apply refused it. They run one code path
			// and differ in one boolean, so this is a real disagreement.
			r.defect("%s: preview accepted an edit apply refused: %v", file, err)
			return
		}
		r.defect("%s: apply: %v", file, err)
		return
	}
	r.Edited++

	after, err := os.ReadFile(copyTo)
	if err != nil {
		r.defect("%s: reading the applied file: %v", file, err)
		return
	}
	switch {
	case prev.Diff != applied.Diff:
		r.defect("%s: preview showed a different diff than apply wrote", file)
	case prev.Added != applied.Added || prev.Removed != applied.Removed:
		r.defect("%s: preview counted %d/%d lines, apply wrote %d/%d",
			file, prev.Added, prev.Removed, applied.Added, applied.Removed)
	case string(prev.Bytes) != string(after):
		r.defect("%s: the bytes preview computed are not the bytes apply wrote", file)
	default:
		r.PreviewEqual++
	}
}

// firstImageScalar finds a plain `services.<name>.image` scalar to edit — the
// same subject editbench and internal/edit's corpus sweep use, so a file that
// carries one here carries one there.
//
// The path comes out of the RESOLVED model rather than a regex over the source,
// which is what makes it addressable: the string handed to edit.Op.At is the
// canonical path form, and it round-trips through resolve.ParsePath.
func firstImageScalar(p *resolve.Project) (string, bool) {
	svc := p.Services()
	if svc == nil {
		return "", false
	}
	for _, name := range svc.Keys() {
		s, ok := svc.Get(name)
		if !ok || s.Kind() != resolve.KindMapping {
			continue
		}
		img, ok := s.Map().Get("image")
		if !ok || img.Kind() != resolve.KindScalar {
			continue
		}
		if strings.TrimSpace(img.Scalar()) == "" {
			continue
		}
		// An interpolated image is skipped. The bytes on disk are `${TAG}` and
		// the model holds what it expanded to, so an edit against it measures
		// interpolation rather than the write path.
		if img.Interpolated() {
			continue
		}
		return resolve.Path{"services", name, "image"}.String(), true
	}
	return "", false
}

func pct(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) * 100 / float64(of)
}

func (r *report) print() {
	fmt.Printf("\nENGINE BENCH — %d compose files\n", r.Corpus)
	fmt.Println(strings.Repeat("=", 78))
	row := func(label string, value float64, n, of int, unit string) {
		fmt.Printf("  %-40s %7.2f%%   %d/%d %s\n", label, value, n, of, unit)
	}
	fmt.Println("resolve")
	row("corpus files resolved", r.ResolvedPct, r.Resolved, r.Resolved+r.Refused, "files")
	row("leaf values with a position", r.ProvenancePct, r.LeavesPositive, r.Leaves, "leaves")
	fmt.Println("topology")
	row("projects that build a graph", r.GraphsPct, r.Graphs, r.Resolved, "projects")
	row("nodes with a declaration origin", r.NodeOriginPct, r.NodesPositive, r.Nodes, "nodes")
	fmt.Println("diagnose")
	row("projects diagnosed", r.ReportsPct, r.Reports, r.Resolved, "projects")
	row("findings carrying an anchor", r.AnchoredPct, r.Anchored, r.Findings, "findings")
	row("anchors on real bytes", r.AnchorBytesPct, r.AnchorsOnBytes, r.Anchors, "anchors")
	fmt.Println("edit")
	row("preview equals apply", r.PreviewPct, r.PreviewEqual, r.Edited, "edits")
	row("stale ranges refused", r.StalePct, r.StaleRefused, r.StaleStaged, "staged")
	fmt.Printf("\n  %d edits attempted, %d refused safely, %d not locatable, %d applied\n",
		r.EditAttempted, r.EditRefused, r.NotLocatable, r.Edited)
	if len(r.Defects) > 0 {
		fmt.Printf("\nDEFECTS (%d)\n", len(r.Defects))
		sort.Strings(r.Defects)
		for _, d := range r.Defects {
			fmt.Printf("  %s\n", d)
		}
	}
	fmt.Println()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "enginebench: "+format+"\n", args...)
	os.Exit(1)
}
