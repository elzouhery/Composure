// Command gate runs the benchmarks and compares every headline metric against a
// committed baseline. It is the CI acceptance gate.
//
// Five producers: the four fidelity benchmarks over the splice engines
// (fidelity, editbench, structbench, dockerbench) and cmd/enginebench over the
// four engines above them (resolve, topology, diagnose, edit). Every one of
// them is a pure function of the corpus and the code — nothing here forks a
// daemon, and a metric that cannot be measured without one does not belong in
// the baseline, because a baselined metric that SKIPS is a gate that passes
// vacuously.
//
// The rule it exists to enforce, from the handoff brief §5:
//
//	A failing fidelity test is never fixed by changing the threshold.
//
// So the thresholds do not live here. They live in benchmarks/baseline.json,
// which is a tracked file — lowering one is a visible diff in a pull request,
// reviewable as the architectural decision it is, rather than a constant edited
// inside a test file at 2am to make the suite green.
//
// Every run appends a row to benchmarks/history.jsonl, so a pass rate is a
// tracked metric with history rather than a boolean. A slow downward drift that
// never breaches the baseline is still visible there.
//
// Usage:
//
//	gate -corpus corpus-repos            compare against the baseline, fail on regression
//	gate -corpus corpus-repos -update    rewrite the baseline from this run
//	gate -corpus corpus-repos -record    append to history without gating
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// tolerance absorbs floating-point noise only. It is deliberately far smaller
// than any real regression: a single file dropping out of a 146-file corpus
// moves the rate by 0.68 points, a hundred times this value.
const tolerance = 0.005

// metric is one gated number. Higher is always better, so the check is uniform:
// value must be >= baseline - tolerance.
type metric struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
}

type baseline struct {
	// Corpus counts are recorded for context. They are not gated — the corpus
	// is expected to grow, and a story exists to take it past 5,000 files.
	ComposeFiles    int      `json:"compose_files"`
	DockerfileFiles int      `json:"dockerfile_files"`
	Metrics         []metric `json:"metrics"`
}

func main() {
	corpusDir := flag.String("corpus", "corpus-repos", "corpus directory")
	baselinePath := flag.String("baseline", "benchmarks/baseline.json", "committed baseline")
	historyPath := flag.String("history", "benchmarks/history.jsonl", "append-only metric history")
	commit := flag.String("commit", "", "commit SHA to stamp on the history row")
	stamp := flag.String("timestamp", "", "RFC3339 timestamp to stamp on the history row")
	update := flag.Bool("update", false, "rewrite the baseline from this run instead of gating")
	record := flag.Bool("record", false, "append to history without gating")
	flag.Parse()

	if _, err := os.Stat(*corpusDir); err != nil {
		fatalf("corpus directory %q not found — run `fidelity fetch %s` first", *corpusDir, *corpusDir)
	}

	current, err := measure(*corpusDir)
	if err != nil {
		fatalf("%v", err)
	}

	if err := appendHistory(*historyPath, current, *commit, *stamp); err != nil {
		fatalf("recording history: %v", err)
	}

	if *update {
		if err := writeBaseline(*baselinePath, current); err != nil {
			fatalf("writing baseline: %v", err)
		}
		fmt.Printf("baseline written to %s (%d metrics)\n", *baselinePath, len(current.Metrics))
		report(current, nil)
		return
	}

	if *record {
		report(current, nil)
		return
	}

	base, err := readBaseline(*baselinePath)
	if err != nil {
		fatalf("reading baseline: %v — run with -update to create it", err)
	}

	regressions := compare(base, current)
	report(current, &base)

	if len(regressions) > 0 {
		fmt.Fprintf(os.Stderr, "\nFIDELITY GATE FAILED — %d metric(s) regressed\n\n", len(regressions))
		for _, r := range regressions {
			fmt.Fprintf(os.Stderr, "  %-44s %7.2f  ->  %7.2f   (%+.2f)\n",
				r.key, r.was, r.now, r.now-r.was)
		}
		fmt.Fprintf(os.Stderr, `
Do not lower the baseline to make this pass. The baseline records a measured
property of the engine; editing it does not restore that property, it only
stops CI from telling you the property is gone. Fix the change that caused
the regression, or make the case for the baseline move in the pull request
description with the reasoning attached.
`)
		os.Exit(1)
	}

	fmt.Printf("\nFIDELITY GATE PASSED — %d metric(s) at or above baseline\n", len(base.Metrics))
}

// measure runs every benchmark and flattens their JSON into gated metrics.
func measure(corpusDir string) (baseline, error) {
	var out baseline

	// 1. fidelity check — the identity pass rate, the headline metric.
	var fidelity []struct {
		Strategy    string  `json:"strategy"`
		Total       int     `json:"total"`
		ParseErrors int     `json:"parse_errors"`
		Identical   int     `json:"identical"`
		Damaged     int     `json:"damaged"`
		PassRate    float64 `json:"pass_rate"`
	}
	// Flags must precede the positional directory — `flag` stops parsing at the
	// first non-flag argument.
	if err := runJSON(&fidelity, "./cmd/fidelity", "check", "-json", corpusDir); err != nil {
		return out, fmt.Errorf("fidelity check: %w", err)
	}
	for _, s := range fidelity {
		if s.Strategy != "splice" {
			continue // the re-emit strategies are documented losers, kept for comparison
		}
		out.ComposeFiles = s.Total
		out.Metrics = append(out.Metrics,
			metric{"fidelity.splice.identity_pct", s.PassRate},
			// Damaged files are gated as a negated count so that "higher is
			// better" holds for every metric and the comparison stays uniform.
			metric{"fidelity.splice.undamaged", float64(s.Total - s.Damaged)},
		)
	}

	// 2. editbench — scalar edit diff size.
	var edit []struct {
		Strategy   string  `json:"strategy"`
		Applied    int     `json:"applied"`
		MinimalPct float64 `json:"minimal_pct"`
		AvgLines   float64 `json:"avg_lines"`
		Worst      int     `json:"worst"`
	}
	if err := runJSON(&edit, "./cmd/editbench", "-json", corpusDir); err != nil {
		return out, fmt.Errorf("editbench: %w", err)
	}
	for _, s := range edit {
		if s.Strategy != "splice" {
			continue
		}
		out.Metrics = append(out.Metrics,
			metric{"editbench.splice.minimal_pct", s.MinimalPct},
			// Negated so higher is better: a two-line diff is the floor, and
			// any growth in the worst case is a regression worth blocking.
			metric{"editbench.splice.neg_worst_lines", -float64(s.Worst)},
		)
	}

	// 3. structbench — insert and delete collateral damage.
	var structural []struct {
		Operation   string  `json:"operation"`
		CleanPct    float64 `json:"clean_pct"`
		ReparsedPct float64 `json:"reparses_pct"`
		CorrectPct  float64 `json:"correct_pct"`
	}
	if err := runJSON(&structural, "./cmd/structbench", "-json", corpusDir); err != nil {
		return out, fmt.Errorf("structbench: %w", err)
	}
	for _, s := range structural {
		op := strings.ToLower(s.Operation)
		out.Metrics = append(out.Metrics,
			metric{"structbench." + op + ".clean_pct", s.CleanPct},
			metric{"structbench." + op + ".reparses_pct", s.ReparsedPct},
			metric{"structbench." + op + ".correct_pct", s.CorrectPct},
		)
	}

	// 4. dockerbench — the separate Dockerfile engine.
	var docker struct {
		Corpus            int     `json:"corpus"`
		IdentityPct       float64 `json:"identity_pct"`
		SetBaseAppliedPct float64 `json:"set_base_image_applied_pct"`
		SetBaseMinimalPct float64 `json:"set_base_image_minimal_pct"`
		InsertCleanPct    float64 `json:"insert_after_clean_pct"`
		DeleteCleanPct    float64 `json:"delete_clean_pct"`
	}
	if err := runJSON(&docker, "./cmd/dockerbench", "-json", corpusDir); err != nil {
		return out, fmt.Errorf("dockerbench: %w", err)
	}
	out.DockerfileFiles = docker.Corpus
	out.Metrics = append(out.Metrics,
		metric{"dockerbench.identity_pct", docker.IdentityPct},
		metric{"dockerbench.set_base_image_applied_pct", docker.SetBaseAppliedPct},
		metric{"dockerbench.set_base_image_minimal_pct", docker.SetBaseMinimalPct},
		metric{"dockerbench.insert_after_clean_pct", docker.InsertCleanPct},
		metric{"dockerbench.delete_clean_pct", docker.DeleteCleanPct},
	)

	// 5. enginebench — the four engines the four benchmarks above never touch.
	//
	// fidelity, editbench, structbench and dockerbench all measure the SPLICE
	// ENGINES. Until this ran, internal/resolve, internal/topology,
	// internal/diagnose and internal/edit had no gated number between them:
	// provenance could vanish off every leaf value, every graph node could lose
	// the position that makes it clickable, and the gate would stay green.
	//
	// Every metric here is daemon-free and hermetic — see the header of
	// cmd/enginebench, and the note above `gate` in the Makefile for why that
	// is the requirement rather than a preference.
	var engines struct {
		Corpus int `json:"corpus"`

		Resolved      int     `json:"resolve_resolved"`
		ResolvedPct   float64 `json:"resolve_resolved_pct"`
		ProvenancePct float64 `json:"resolve_leaf_provenance_pct"`

		GraphsPct     float64 `json:"topology_built_pct"`
		NodeOriginPct float64 `json:"topology_node_origin_pct"`

		ReportsPct     float64 `json:"diagnose_analyzed_pct"`
		AnchoredPct    float64 `json:"diagnose_anchored_pct"`
		AnchorBytesPct float64 `json:"diagnose_anchor_bytes_pct"`

		Edited       int     `json:"edit_edited"`
		PreviewPct   float64 `json:"edit_preview_equals_apply_pct"`
		StalePctRate float64 `json:"edit_stale_refused_pct"`
	}
	if err := runJSON(&engines, "./cmd/enginebench", "-json", corpusDir); err != nil {
		return out, fmt.Errorf("enginebench: %w", err)
	}
	out.Metrics = append(out.Metrics,
		// resolve
		metric{"resolve.corpus.resolved_pct", engines.ResolvedPct},
		// The DENOMINATOR of every per-project rate below it, gated as a count.
		// A rate is a fraction and a fraction can be preserved by shrinking
		// both halves: a change that resolved four corpus files instead of
		// eighty-five would hold every percentage at 100 and prove nothing.
		// enginebench refuses outright on a zero denominator; this is what
		// catches the same failure at 5%.
		metric{"resolve.corpus.resolved", float64(engines.Resolved)},
		metric{"resolve.corpus.leaf_provenance_pct", engines.ProvenancePct},
		// topology
		metric{"topology.corpus.built_pct", engines.GraphsPct},
		metric{"topology.corpus.node_origin_pct", engines.NodeOriginPct},
		// diagnose
		metric{"diagnose.corpus.analyzed_pct", engines.ReportsPct},
		metric{"diagnose.corpus.anchored_pct", engines.AnchoredPct},
		metric{"diagnose.corpus.anchor_bytes_pct", engines.AnchorBytesPct},
		// edit
		metric{"edit.corpus.preview_equals_apply_pct", engines.PreviewPct},
		metric{"edit.corpus.edited", float64(engines.Edited)},
		metric{"edit.corpus.stale_refused_pct", engines.StalePctRate},
	)

	sort.Slice(out.Metrics, func(i, j int) bool { return out.Metrics[i].Key < out.Metrics[j].Key })
	return out, nil
}

// runJSON runs `go run <pkg> <args...>` and decodes stdout into v.
func runJSON(v any, pkg string, args ...string) error {
	cmd := exec.Command("go", append([]string{"run", pkg}, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

type regression struct {
	key      string
	was, now float64
}

func compare(base, current baseline) []regression {
	now := map[string]float64{}
	for _, m := range current.Metrics {
		now[m.Key] = m.Value
	}
	var out []regression
	for _, m := range base.Metrics {
		v, ok := now[m.Key]
		if !ok {
			// A baselined metric that stopped being produced is a regression
			// too — otherwise deleting a benchmark silently passes the gate.
			out = append(out, regression{m.Key + " (no longer measured)", m.Value, 0})
			continue
		}
		if v < m.Value-tolerance {
			out = append(out, regression{m.Key, m.Value, v})
		}
	}
	return out
}

func report(current baseline, base *baseline) {
	fmt.Printf("\nFIDELITY GATE — %d compose files, %d Dockerfiles\n",
		current.ComposeFiles, current.DockerfileFiles)
	fmt.Println(strings.Repeat("=", 78))
	was := map[string]float64{}
	if base != nil {
		for _, m := range base.Metrics {
			was[m.Key] = m.Value
		}
	}
	for _, m := range current.Metrics {
		if base == nil {
			fmt.Printf("  %-44s %8.2f\n", m.Key, m.Value)
			continue
		}
		b, ok := was[m.Key]
		switch {
		case !ok:
			fmt.Printf("  %-44s %8.2f   (new, not yet baselined)\n", m.Key, m.Value)
		case m.Value < b-tolerance:
			fmt.Printf("  %-44s %8.2f   REGRESSED from %.2f\n", m.Key, m.Value, b)
		case m.Value > b+tolerance:
			fmt.Printf("  %-44s %8.2f   improved from %.2f\n", m.Key, m.Value, b)
		default:
			fmt.Printf("  %-44s %8.2f   ok\n", m.Key, m.Value)
		}
	}
}

func readBaseline(path string) (baseline, error) {
	var b baseline
	data, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, err
	}
	if len(b.Metrics) == 0 {
		return b, fmt.Errorf("baseline %s has no metrics", path)
	}
	return b, nil
}

func writeBaseline(path string, b baseline) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// appendHistory writes one row per run so the pass rate can be read as a trend.
// A regression that stays inside the baseline is invisible to the gate but
// visible here, which is the point.
func appendHistory(path string, b baseline, commit, timestamp string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	row := map[string]any{
		"compose_files":    b.ComposeFiles,
		"dockerfile_files": b.DockerfileFiles,
	}
	if commit != "" {
		row["commit"] = commit
	}
	if timestamp != "" {
		row["timestamp"] = timestamp
	}
	for _, m := range b.Metrics {
		row[m.Key] = m.Value
	}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gate: "+format+"\n", args...)
	os.Exit(1)
}
