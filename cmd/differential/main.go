// Command differential is story 1.5: the resolved model compared against
// `docker compose config` over the corpus.
//
// This is the epic's risk boundary. A merge rule that is subtly wrong produces
// a model that looks right, resolves without error and disagrees with what
// `docker compose up` would actually run — the confident-wrong-answer failure
// this whole engine is prone to. Unit tests cannot catch it, because they are
// written from the same reading of the specification the code was.
//
// AD-10, and it is the reason this is a command and not a package: the Compose
// CLI is a TEST ORACLE and nothing else. It is invoked here and nowhere else in
// the codebase; internal/resolve never shells out, and internal/resolve's own
// test suite asserts that mechanically.
//
// WHAT IS COMPARED: PROJECTS, NOT FILES.
//
// The first version of this harness called resolve.File and passed the oracle a
// single -f. Every comparison was therefore one file against itself, and the
// merge — the thing stories 1.4 and 1.6 built, the thing an oracle exists to
// prove, the thing this file's own header calls the epic's risk boundary — was
// never once exercised. A pass rate measured that way says nothing about the
// merge table.
//
// So the unit is a PROJECT: an ordered file chain resolved through resolve.Load
// and handed to the oracle as the same chain. Three shapes are compared:
//
//   - a single corpus file, which is still worth comparing for interpolation,
//     `include:` and `extends:`;
//   - a directory with an override file, resolved by the automatic pickup on
//     both sides — no -f at all;
//   - an explicit -f chain, merged left to right on both sides, including a
//     chain that names one file twice.
//
// The corpus supplies the first and nothing else: it contains 146 compose files
// and zero override files. The multi-file projects are fixtures under
// testdata/differential, and they are not optional decoration — without them
// this harness measures single-file resolution and calls it a merge check.
//
// WHAT IS COMPARED, and why not everything.
//
// `docker compose config` does not emit the merged document. It emits Compose's
// canonical model: short syntax expanded to long, `environment` lists turned
// into maps, bind mounts absolutised against the project directory, a `default`
// network invented, a project `name` added. Comparing the two documents whole
// would measure how faithfully this tool reproduces Compose's canonicalisation,
// which is not a thing anybody asked for and which would bury a real merge
// divergence in a thousand cosmetic ones.
//
// So the comparison is over a projection: the fields the MERGE decides, each
// normalised to a form both sides can express. Every divergence is reported
// with the config path that diverged, which is the acceptance criterion. The
// projection is listed in compareService below and is deliberately readable —
// a field that is not in it is not being checked, and that has to be visible.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elzouhery/composure/internal/corpus"
	"github.com/elzouhery/composure/internal/resolve"
)

// oracleTimeout bounds one CLI invocation. A hung oracle must not hang the
// harness: it is a measurement, not a build step.
const oracleTimeout = 30 * time.Second

func main() {
	asJSON := flag.Bool("json", false, "emit JSON instead of the table")
	limit := flag.Int("limit", 0, "compare at most this many corpus projects (0 = all)")
	verbose := flag.Bool("v", false, "print every divergence, not just the first three per project")
	fixtures := flag.String("fixtures", defaultFixtures, "multi-file fixture directory (empty to skip)")
	allowSkip := flag.Bool("allow-skip", false, "exit 0 when no Docker daemon is present instead of failing")
	minMulti := flag.Int("min-multifile", 0, "fail unless at least this many MULTI-FILE projects were compared")
	minPass := flag.Float64("min-pass", 0, "fail unless the pass rate is at least this percentage")
	// Separate from -min-pass because the two rates are measured over
	// different populations. The headline rate is mostly corpus files, whose
	// oracle output shifts between Compose releases; the multi-file rate is
	// over committed fixtures only, where there is no drift for a floor to
	// absorb. One number cannot be right for both.
	minMultiPass := flag.Float64("min-multifile-pass", 0, "fail unless the MULTI-FILE pass rate is at least this percentage")
	flag.Parse()

	root := "corpus-repos"
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	oracle, ok := findOracle()
	if !ok {
		// A missing `docker compose` is LOUD by default.
		//
		// It used to be a silent success, and that is the shape of a check that
		// passes vacuously: an invocation that proves nothing exits 0 and reads
		// as a green result. -allow-skip is the opt-in for a caller that has
		// genuinely decided a machine without Docker is acceptable, and it has
		// to say so.
		//
		// THIS AMENDS STORY 1.5's ACCEPTANCE CRITERION, and the amendment is
		// recorded rather than left as a contradiction between the story and
		// the code. The AC says "absent Docker skips with a message". It gets
		// the message — printed above, naming exactly what was not compared —
		// and it does not get the exit 0, because `make differential` is a
		// check and a check that reports success for a run that compared
		// nothing is the defect this harness has already been repaired from
		// once. The skip is still available; it has to be asked for. See
		// DECISIONS.md §14.
		msg := "docker compose is not on this machine, so NOTHING WAS COMPARED"
		if *asJSON {
			emit(report{Skipped: true, SkipReason: msg})
		} else {
			fmt.Printf("\nDIFFERENTIAL — SKIPPED, NOTHING WAS COMPARED\n%s\n"+
				"This run measured no merge. Pass -allow-skip if that is acceptable here.\n\n", msg)
		}
		if *allowSkip {
			return
		}
		os.Exit(exitSkipped)
	}

	projects, err := discover(root, *fixtures, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "differential: %v\n", err)
		os.Exit(1)
	}

	rep := report{Oracle: strings.Join(oracle, " "), Root: root, Fixtures: *fixtures}
	for _, p := range projects {
		rep.add(compareOne(oracle, p))
	}
	rep.finish()

	if *asJSON {
		emit(rep)
	} else {
		rep.print(*verbose)
	}

	// Self-gating, opt-in, and deliberately NOT wired into
	// benchmarks/baseline.json — see the note on `make differential`.
	//
	// -min-multifile is the important one: it is what stops the fixtures being
	// deleted, renamed or silently failing to resolve and leaving a harness
	// that compares 146 files against themselves and reports 100%.
	var failed bool
	// Always enforced, with no flag to turn it off. A register whose assertion
	// is optional is a comment, and the whole point of the mechanism is that a
	// documented divergence stays exactly the divergence that was documented.
	if rep.Changed > 0 {
		fmt.Fprintf(os.Stderr, "\ndifferential: %d project(s) registered in %s no longer diverge as documented — "+
			"see the report above; update the register deliberately or fix the change that moved it\n", rep.Changed, divergesFile)
		failed = true
	}
	if *minMulti > 0 && rep.MultiFile < *minMulti {
		fmt.Fprintf(os.Stderr, "\ndifferential: compared %d multi-file projects, want at least %d — "+
			"the merge was not exercised\n", rep.MultiFile, *minMulti)
		failed = true
	}
	if *minPass > 0 && rep.PassPct < *minPass {
		fmt.Fprintf(os.Stderr, "\ndifferential: pass rate %.2f%% (%d of %d compared) is below the %.2f%% floor\n",
			rep.PassPct, rep.Passed, rep.Compared, *minPass)
		failed = true
	}
	// The multi-file floor is checked even when nothing multi-file was
	// compared, and that is deliberate: MultiFilePct is 0 for an empty set,
	// so "the fixtures all vanished" fails here as well as at -min-multifile
	// rather than passing as a rate over nothing.
	if *minMultiPass > 0 && rep.MultiFilePct < *minMultiPass {
		fmt.Fprintf(os.Stderr, "\ndifferential: MULTI-FILE pass rate %.2f%% (%d of %d merges) is below the %.2f%% floor — "+
			"this is the merge, and it is the one number the corpus cannot tell you anything about\n",
			rep.MultiFilePct, rep.MultiFilePassed, rep.MultiFile, *minMultiPass)
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}

// exitSkipped is its own code so a caller can tell "the oracle was absent" from
// "the oracle disagreed". Both are failures; they are not the same failure.
const exitSkipped = 3

// defaultFixtures is where the multi-file projects live, relative to the
// repository root. It is a default rather than a constant so a caller running
// from elsewhere can point at them.
const defaultFixtures = "testdata/differential"

// findOracle locates the Compose CLI. Both spellings are accepted because both
// exist in the wild and a machine with only the plugin form is not a machine
// without Compose.
func findOracle() ([]string, bool) {
	if p, err := exec.LookPath("docker"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), oracleTimeout)
		defer cancel()
		if err := exec.CommandContext(ctx, p, "compose", "version").Run(); err == nil {
			return []string{p, "compose"}, true
		}
	}
	if p, err := exec.LookPath("docker-compose"); err == nil {
		return []string{p}, true
	}
	return nil, false
}

// ---- what a project is ------------------------------------------------------

// project is one unit of comparison: an ordered file chain, or a directory
// whose chain both sides work out for themselves.
type project struct {
	// Name identifies the project in the report.
	Name string `json:"name"`
	// Dir is the project directory. It is what --project-directory is pinned
	// to on the oracle side and what bind sources and `.env` resolve against.
	Dir string `json:"dir"`
	// Files is the explicit chain, in order. Empty means the automatic pickup:
	// resolve.Dir on this side, no -f at all on the oracle's.
	Files []string `json:"files,omitempty"`
	// Pickup records that the chain is discovered rather than given, so the
	// report can say what was actually exercised.
	Pickup bool `json:"pickup"`
	// Diverges is the expected-divergence register: config paths this project
	// and Compose are KNOWN to disagree about, by design. Read from a DIVERGES
	// file in the fixture directory. See divergesFile.
	Diverges []string `json:"diverges,omitempty"`
}

// MultiFile reports whether comparing this project exercises a merge.
//
// A directory-pickup project counts only when the override file is really
// there; a directory holding one compose file merges nothing, and counting it
// would inflate the one number that says whether this harness is doing its job.
func (p project) MultiFile() bool {
	if len(p.Files) > 0 {
		return len(p.Files) > 1
	}
	return len(pickupFiles(p.Dir)) > 1
}

// resolveProject is the composure side. It goes through resolve.Load, not
// resolve.File: Load is what applies the chain, the override pickup, include
// and extends, and it is what a merge divergence would live in.
func (p project) resolve() (*resolve.Project, error) {
	if len(p.Files) > 0 {
		return resolve.Load(resolve.Options{Files: p.Files})
	}
	return resolve.Load(resolve.Options{Dir: p.Dir})
}

// composeCandidates is Compose's own filename order for a directory.
var composeCandidates = []string{
	"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
}

// pickupFiles is the chain a directory implies: the first base filename that
// exists, plus its matching override. It exists so MultiFile() can tell a
// directory that merges from one that does not — the resolution itself is left
// to resolve.Dir and to Compose, each doing its own pickup, because agreeing on
// the pickup is part of what is being compared.
func pickupFiles(dir string) []string {
	var out []string
	for _, name := range composeCandidates {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			continue
		}
		out = append(out, filepath.Join(dir, name))
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		ext := filepath.Ext(name)
		for _, o := range []string{base + ".override" + ext, base + ".override.yaml", base + ".override.yml"} {
			p := filepath.Join(dir, o)
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
				break
			}
		}
		break
	}
	return out
}

// discover builds the comparison set: the corpus, then the fixtures.
//
// The fixtures are appended AFTER the limit is applied to the corpus, so
// `-limit 20` still exercises the merge. A limit that could silently drop every
// multi-file project would make the fast run and the full run measure different
// things while reporting the same headline.
func discover(root, fixtures string, limit int) ([]project, error) {
	var out []project

	if root != "" {
		corpusProjects, err := collect(root)
		if err != nil {
			return nil, err
		}
		if limit > 0 && len(corpusProjects) > limit {
			corpusProjects = corpusProjects[:limit]
		}
		out = append(out, corpusProjects...)
	}

	if fixtures != "" {
		fixtureProjects, err := collectFixtures(fixtures)
		if err != nil {
			return nil, fmt.Errorf("fixtures: %w", err)
		}
		if len(fixtureProjects) == 0 {
			return nil, fmt.Errorf("fixtures: %s holds no projects — the merge would not be exercised", fixtures)
		}
		out = append(out, fixtureProjects...)
	}
	return out, nil
}

func collect(root string) ([]project, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []project{{Name: root, Dir: filepath.Dir(root), Files: []string{root}}}, nil
	}
	files, err := corpus.Collect(root)
	if err != nil {
		return nil, err
	}

	// A directory holding a base AND an override is one project, not two files.
	// The corpus has none today — a sweep for override files across all of
	// corpus-repos returns nothing — but a corpus that grows into one must be
	// compared as the merge it is rather than as two unrelated fragments.
	merged := map[string]bool{}
	var out []project
	for _, f := range files {
		dir := filepath.Dir(f)
		if merged[dir] {
			continue
		}
		if chain := pickupFiles(dir); len(chain) > 1 {
			merged[dir] = true
			out = append(out, project{Name: dir, Dir: dir, Pickup: true})
			continue
		}
		out = append(out, project{Name: f, Dir: dir, Files: []string{f}})
	}
	return out, nil
}

// chainFile names the file that turns a fixture directory into an explicit -f
// chain. Its lines are paths relative to the directory, in merge order.
const chainFile = "CHAIN"

// divergesFile names the expected-divergence register: config paths this
// project and Compose are known to disagree about, one per line.
//
// WHY A REGISTER RATHER THAN A DELETED FIXTURE OR A GREEN ONE.
//
// Every fixture here paired MATCHING forms, so the 100% figure was real and
// blind: the only divergence the project can actually demonstrate — a key
// written as a list in one file and a mapping in the other — was not
// represented at all. Adding it makes the differential go red, and a
// permanently red harness is one nobody reads, so neither "leave it out" nor
// "let it fail forever" is honest.
//
// So a registered fixture is asserted to DISAGREE, in exactly the places
// named. The check has teeth in both directions:
//
//   - it starts agreeing        -> failure. The design changed, or the merge
//     quietly started normalising forms, and this
//     register is now a lie.
//   - it diverges somewhere new -> failure. A real regression hiding behind a
//     documented one.
//
// And a registered divergence is never counted as a pass. It gets its own
// bucket, its own headline line and its own section in the report, because the
// one outcome this must not produce is a reader mistaking "known and
// documented" for "agrees".
const divergesFile = "DIVERGES"

func collectFixtures(dir string) ([]project, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		chain, err := readChain(sub)
		if err != nil {
			return nil, err
		}
		p := project{Name: sub, Dir: sub, Files: chain, Pickup: len(chain) == 0}
		if len(chain) == 0 && len(pickupFiles(sub)) == 0 {
			continue // not a project directory
		}
		diverges, err := readDiverges(sub)
		if err != nil {
			return nil, err
		}
		p.Diverges = diverges
		out = append(out, p)
	}
	return out, nil
}

func readChain(dir string) ([]string, error) {
	lines, err := readList(dir, chainFile)
	if err != nil {
		return nil, err
	}
	if lines == nil {
		return nil, nil
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s/%s names no files", dir, chainFile)
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, filepath.Join(dir, l))
	}
	return out, nil
}

// readDiverges reads the expected-divergence register. A present-but-empty file
// is a mistake worth naming rather than a project with no expectations: it reads
// as "documented" and asserts nothing.
func readDiverges(dir string) ([]string, error) {
	lines, err := readList(dir, divergesFile)
	if err != nil {
		return nil, err
	}
	if lines == nil {
		return nil, nil
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s/%s names no config paths, so it documents nothing and asserts nothing", dir, divergesFile)
	}
	sort.Strings(lines)
	return lines, nil
}

// readList reads a directory control file into its non-comment, non-blank
// lines. A nil return means the file is absent, which is different from a file
// with no content in it.
func readList(dir, name string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// ---- one project ------------------------------------------------------------

type divergence struct {
	Path      string `json:"path"`
	Composure string `json:"composure"`
	Compose   string `json:"compose"`
}

type outcome struct {
	File string `json:"file"`
	// Files is the chain that was merged, present when there was more than one.
	Files []string `json:"files,omitempty"`
	// MultiFile records whether this comparison exercised a merge. It is the
	// number the headline is worth nothing without.
	MultiFile bool `json:"multi_file"`
	// Status is one of: pass, diverged, documented-divergence,
	// divergence-changed, both-refused, oracle-refused, composure-refused.
	//
	// documented-divergence is NOT a pass. It is a project registered in a
	// DIVERGES file that diverged in exactly the places registered, so the
	// disagreement is the one we wrote down rather than a new one.
	// divergence-changed is a hard failure: a registered project that agreed, or
	// disagreed somewhere else.
	Status      string       `json:"status"`
	Detail      string       `json:"detail,omitempty"`
	Divergences []divergence `json:"divergences,omitempty"`
	// Expected is the register this outcome was judged against, when there was
	// one, so the report can be read without opening the fixture.
	Expected []string `json:"expected_divergences,omitempty"`
}

func compareOne(oracle []string, p project) outcome {
	res := outcome{File: p.Name, MultiFile: p.MultiFile()}
	if len(p.Files) > 1 {
		res.Files = p.Files
	}

	proj, lerr := p.resolve()

	// Every profile the project declares is activated on the oracle side.
	// `docker compose config` FILTERS by profile; the resolver deliberately
	// does not (AD-16), so without this a service behind a profile reads as
	// present on one side and absent on the other, which is a difference in
	// what the two tools are FOR rather than a divergence in the merge.
	var profiles []string
	if lerr == nil {
		profiles = proj.DeclaredProfiles()
	}
	oracleDoc, oerr := runOracle(oracle, p, profiles)

	switch {
	case lerr != nil && oerr != nil:
		// Both refused. That is agreement, and the most common one in this
		// corpus: 59 of its files are Compose v1 fragments. It is not counted
		// as a pass, because nothing was compared.
		res.Status, res.Detail = "both-refused", lerr.Error()
		return res
	case oerr != nil:
		res.Status, res.Detail = "oracle-refused", oerr.Error()
		return res
	case lerr != nil:
		res.Status, res.Detail = "composure-refused", lerr.Error()
		return res
	}

	div := compareProjects(proj, oracleDoc, p.Dir)

	if len(p.Diverges) > 0 {
		res.Expected = p.Diverges
		res.Status, res.Detail = judgeRegistered(p.Diverges, div)
		res.Divergences = div
		return res
	}

	if len(div) == 0 {
		res.Status = "pass"
		return res
	}
	res.Status, res.Divergences = "diverged", div
	return res
}

// judgeRegistered compares what a registered project ACTUALLY diverged on
// against what it is documented to diverge on.
//
// Equality, not containment, in both directions. A registered divergence that
// stopped happening means the register is now a lie about the engine, and a
// register that hides a second, unregistered divergence is a suppression rather
// than a document — which is the failure mode this whole mechanism exists to
// avoid.
func judgeRegistered(expected []string, got []divergence) (status, detail string) {
	actual := map[string]bool{}
	for _, d := range got {
		actual[d.Path] = true
	}
	want := map[string]bool{}
	for _, p := range expected {
		want[p] = true
	}

	var missing, unexpected []string
	for p := range want {
		if !actual[p] {
			missing = append(missing, p)
		}
	}
	for p := range actual {
		if !want[p] {
			unexpected = append(unexpected, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)

	switch {
	case len(missing) == 0 && len(unexpected) == 0:
		return "documented-divergence", ""
	case len(missing) > 0 && len(unexpected) == 0:
		return "divergence-changed", fmt.Sprintf(
			"registered as diverging on %s and it now AGREES there. Either the engine changed or the "+
				"register is wrong — update %s deliberately, do not delete the fixture",
			strings.Join(missing, ", "), divergesFile)
	case len(missing) == 0:
		return "divergence-changed", fmt.Sprintf(
			"diverges on %s, which is NOT in %s. A documented divergence must not be a hiding place for a new one",
			strings.Join(unexpected, ", "), divergesFile)
	default:
		return "divergence-changed", fmt.Sprintf(
			"no longer diverges on %s, and now diverges on %s instead; neither matches %s",
			strings.Join(missing, ", "), strings.Join(unexpected, ", "), divergesFile)
	}
}

// runOracle asks the CLI for its canonical model.
//
// --project-directory is pinned to the file's own directory so the oracle reads
// the same `.env` the resolver did; without it Compose would use the harness's
// working directory and the two would interpolate from different environments,
// which reads as a merge divergence and is not one.
// The chain is passed as one -f per file IN ORDER, which is the whole point:
// `-f base -f override` is what makes the oracle merge, and the order is what
// decides which file wins. A chain that names the same file twice is passed
// twice, because Compose accepts that and the resolver has a documented
// behaviour for it (Origin.Step must still identify a position).
//
// When the project is a directory pickup, NO -f is passed at all and Compose
// works the chain out for itself — which makes the pickup rule part of what is
// being compared rather than something the harness quietly supplies to both
// sides.
func runOracle(oracle []string, p project, profiles []string) (map[string]any, error) {
	args, dir, err := oracleArgs(oracle, p, profiles)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), oracleTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, oracle[0], args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(errorLine(msg))
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("oracle output is not JSON: %w", err)
	}
	return doc, nil
}

// oracleArgs builds the command line, and is separate from running it so the
// chain construction is testable without a Docker daemon. It is the part most
// worth testing: passing one -f where the project has three is exactly how this
// harness ends up comparing a merge against a single file and calling it a
// pass, which is the defect it is being repaired from.
func oracleArgs(oracle []string, p project, profiles []string) (args []string, dir string, err error) {
	dir, err = filepath.Abs(p.Dir)
	if err != nil {
		return nil, "", err
	}
	// ABSOLUTE, and this is not a tidiness point: the command runs with its
	// working directory already set to the project, so a relative
	// --project-directory would be resolved a second time against itself and
	// land in a directory that does not exist. Compose then finds no `.env`,
	// interpolates every variable to empty, and 40% of the corpus reads as an
	// environment divergence that is entirely the harness's fault.
	args = append(append([]string{}, oracle[1:]...), "--project-directory", dir)
	for _, pr := range profiles {
		args = append(args, "--profile", pr)
	}
	for _, f := range p.Files {
		abs, err := filepath.Abs(f)
		if err != nil {
			return nil, "", err
		}
		args = append(args, "-f", abs)
	}
	args = append(args, "config", "--format", "json")
	return args, dir, nil
}

// errorLine picks the reason out of the oracle's stderr. Compose writes
// deprecation and unset-variable WARNINGS first and the actual failure last, so
// taking the first line reports "the version attribute is obsolete" as the
// reason a project could not be read.
func errorLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.Contains(l, "level=warning") {
			continue
		}
		return l
	}
	return strings.TrimSpace(s)
}

// ---- the projection ---------------------------------------------------------

func compareProjects(p *resolve.Project, oracle map[string]any, dir string) []divergence {
	var out []divergence

	lsvc := p.Services()
	osvc := asMap(oracle["services"])

	names := union(lsvc.Keys(), keysOf(osvc))
	for _, name := range names {
		lv, lok := lsvc.Get(name)
		ov, ook := osvc[name]
		path := "services." + name
		if !lok {
			out = append(out, divergence{Path: path, Composure: "(absent)", Compose: "(present)"})
			continue
		}
		if !ook {
			out = append(out, divergence{Path: path, Composure: "(present)", Compose: "(absent)"})
			continue
		}
		out = append(out, compareService(path, lv, asMap(ov), dir)...)
	}

	// Top-level resource names. Compose invents a `default` network for a
	// project that declares none, so its presence on one side only is not a
	// divergence — it is canonicalisation.
	for _, section := range []string{"networks", "volumes", "configs", "secrets"} {
		lnames := topLevelNames(p, section)
		onames := keysOf(asMap(oracle[section]))
		if section == "networks" {
			onames = without(onames, "default", lnames)
		}
		// Compose PRUNES a top-level resource no service references, and
		// invents a `default` network. So the check is containment rather than
		// equality: everything the oracle kept must be here. A name only this
		// model has is a resource Compose dropped as unused, which story 2.1
		// deliberately keeps as a node.
		if missing := notIn(onames, lnames); len(missing) > 0 {
			out = append(out, divergence{
				Path:      section,
				Composure: strings.Join(normSet(lnames), ","),
				Compose:   strings.Join(normSet(onames), ","),
			})
		}
	}
	return out
}

// compareService is the projection, and it is meant to be read as a list.
//
// Each row is a field whose value the MERGE decides, with the normaliser that
// makes the two sides comparable. A field absent from this list is not checked.
func compareService(path string, l *resolve.Value, o map[string]any, dir string) []divergence {
	var out []divergence
	add := func(field, lv, ov string) {
		if lv != ov {
			out = append(out, divergence{Path: path + "." + field, Composure: lv, Compose: ov})
		}
	}

	// Plain scalars: replaced by the merge, so the last file must win on both
	// sides or the chain order is wrong.
	for _, f := range []string{"image", "restart", "user", "working_dir", "hostname", "container_name", "network_mode", "pid", "init", "privileged"} {
		lv, ov := scalarOf(field(l, f)), scalarOfAny(o[f])
		if lv == "" && ov == "" {
			continue
		}
		add(f, lv, ov)
	}

	// The three keys compose-spec §13 REPLACES rather than appends. A tool that
	// appended them produces a command line nobody wrote, and this is the check
	// that would catch it.
	for _, f := range []string{"command", "entrypoint"} {
		lv, ov := commandTokens(field(l, f)), oracleCommand(o[f])
		if lv == "" && ov == "" {
			continue
		}
		add(f, lv, ov)
	}
	if lh, oh := field(l, "healthcheck"), asMap(o["healthcheck"]); lh != nil || oh != nil {
		lv, ov := healthTest(field(lh, "test")), oracleCommand(oh["test"])
		if lv != "" || ov != "" {
			add("healthcheck.test", lv, ov)
		}
	}

	// Key/value collections: merged on the variable name.
	// The oracle folds every `env_file` into `environment`; the resolved model
	// keeps the two apart, because the file does. Those names are removed from
	// the oracle's side before comparing — except where the document declares
	// the same name itself, where the value IS the merge's business and is
	// compared.
	lenv := keyValueOf(field(l, "environment"))
	oenv := anyKeyValue(o["environment"])
	for name := range passthroughNames(field(l, "environment")) {
		delete(oenv, name)
	}
	for name := range envFileNames(l, dir) {
		if _, declared := lenv[name]; !declared {
			delete(oenv, name)
		}
	}
	if d, ok := diffMaps(path+".environment", lenv, oenv); !ok {
		out = append(out, d)
	}
	if d, ok := diffMaps(path+".labels", keyValueOf(field(l, "labels")), anyKeyValue(o["labels"])); !ok {
		out = append(out, d)
	}

	// Sequences with a uniqueness key — the rows most likely to be wrong.
	if d, ok := diffSets(path+".ports", portSet(field(l, "ports")), oraclePortSet(o["ports"])); !ok {
		out = append(out, d)
	}
	if d, ok := diffSets(path+".volumes", mountTargets(field(l, "volumes")), oracleMountTargets(o["volumes"])); !ok {
		out = append(out, d)
	}
	// Compose DERIVES a depends_on from every `links:` entry. That derivation
	// is a relation, and in this architecture relations belong to topology
	// (AD-17), which models links as an edge kind of their own — so the
	// resolved model correctly carries only what the file declares. The same
	// derivation is applied here to make the two comparable.
	ldeps := dependsOn(field(l, "depends_on"))
	for _, link := range stringSet(field(l, "links")) {
		name, _, _ := strings.Cut(link, ":")
		if name != "" {
			if _, already := ldeps[name]; !already {
				ldeps[name] = "service_started"
			}
		}
	}
	if d, ok := diffMaps(path+".depends_on", ldeps, oracleDependsOn(o["depends_on"])); !ok {
		out = append(out, d)
	}
	if d, ok := diffSets(path+".profiles", stringSet(field(l, "profiles")), anyStringSet(o["profiles"])); !ok {
		out = append(out, d)
	}

	// The plain list-shaped attributes. §13 says nothing about any of them, so
	// the spec default is "sequences append" — and whether Compose ACTUALLY
	// appends a repeated entry or collapses it is a question only the oracle
	// can settle. It is exactly the question the merge table's deleted dedupe
	// rows were about, and it cannot be answered without a project that
	// declares the same entry in two files, which is what the multi-file
	// fixtures are for.
	for _, f := range listAttributes {
		lv, ov := stringSet(field(l, f)), anyStringSet(o[f])
		if len(lv) == 0 && len(ov) == 0 {
			continue
		}
		if f == "extra_hosts" {
			// Compose renders `name:ip` back out as `name=ip`. Same statement,
			// different spelling.
			lv, ov = mapEach(lv, hostEntry), mapEach(ov, hostEntry)
		}
		if d, ok := diffSets(path+"."+f, lv, ov); !ok {
			out = append(out, d)
		}
	}

	// `extends:` must be GONE on both sides: it is a load-time directive, and
	// a model that still carries one has not applied it.
	if _, still := lookup(l, "extends"); still {
		out = append(out, divergence{Path: path + ".extends", Composure: "(unresolved)", Compose: "(applied)"})
	}
	return out
}

// ---- normalisers ------------------------------------------------------------
//
// These are written independently of internal/resolve's own key extractors, on
// purpose. An oracle that shares its normalisation with the thing it is testing
// agrees with it by construction.

func field(v *resolve.Value, key string) *resolve.Value {
	got, _ := lookup(v, key)
	return got
}

func lookup(v *resolve.Value, key string) (*resolve.Value, bool) {
	if v == nil || v.Kind() != resolve.KindMapping {
		return nil, false
	}
	return v.Map().Get(key)
}

func scalarOf(v *resolve.Value) string {
	if v == nil || v.Kind() != resolve.KindScalar {
		return ""
	}
	return strings.TrimSpace(v.Scalar())
}

func scalarOfAny(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// commandTokens renders a `command:`/`entrypoint:` from the resolved model as
// its exact argument list. The string form is tokenised; the list form is
// already tokenised and is taken as written.
func commandTokens(v *resolve.Value) string {
	if v == nil {
		return ""
	}
	switch v.Kind() {
	case resolve.KindScalar:
		return shellForm(v.Scalar())
	case resolve.KindSequence:
		var parts []string
		for _, e := range v.Seq() {
			parts = append(parts, e.Scalar())
		}
		return listForm(parts)
	}
	return ""
}

// oracleCommand renders the same field from the oracle's document. Compose
// canonicalises to a list, so the string branch is defensive rather than
// expected — and it tokenises rather than guessing, for the same reason.
func oracleCommand(v any) string {
	switch t := v.(type) {
	case string:
		return shellForm(unescapeDollar(t))
	case []any:
		var parts []string
		for _, e := range t {
			parts = append(parts, unescapeDollar(scalarOfAny(e)))
		}
		return listForm(parts)
	}
	return ""
}

// unescapeDollar undoes the oracle's re-escaping.
//
// `docker compose config` emits a document meant to be fed back to Compose, so
// it RE-escapes a literal dollar as `$$`. The resolved model holds the
// interpolated result, where that same dollar is a single character. The two
// agree; only the rendering differs.
func unescapeDollar(s string) string { return strings.ReplaceAll(s, "$$", "$") }

// ---- shell form -------------------------------------------------------------
//
// Compose parses a shell-form `command:` into WORDS, the way a shell would, and
// drops the quotes doing it. The resolved model keeps the string exactly as
// written, quotes and all, because that is what the file says. Neither is
// wrong; comparing them means tokenising.
//
// The previous implementation deleted every quote character and split on
// whitespace. Its own comment admitted it "cannot tell `-c \"a b\"` from `-c a
// b`" — which is precisely the case a `command:` bug produces, because an
// argument that loses its quoting becomes two arguments and runs something
// nobody wrote. That is not a comparison worth reporting a pass rate from.
//
// It is exact now, with one honest exception: a string whose quoting does not
// close is not tokenisable, and rather than guess, tokenise reports that it
// cannot compare it. See errUnbalanced.

// errUnbalanced marks a command line whose quoting never closes. It is not a
// divergence — nothing has been compared — and it is reported as its own thing
// so that "the two sides disagree" and "this harness cannot read one side" stay
// different answers.
var errUnbalanced = errors.New("unbalanced quoting")

// shellTokens splits a shell-form string into the words a POSIX shell would
// pass to exec: single quotes are literal, double quotes allow backslash
// escapes, and an unquoted backslash escapes the next character.
func shellTokens(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		started bool
	)
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			flush()
		case '\'':
			started = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errUnbalanced
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case '"':
			started = true
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '\\' && i+1 < len(s) {
					// Inside double quotes a backslash only escapes these.
					switch s[i+1] {
					case '"', '\\', '$', '`':
						cur.WriteByte(s[i+1])
						i++
						continue
					}
					cur.WriteByte('\\')
					continue
				}
				if s[i] == '"' {
					closed = true
					break
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, errUnbalanced
			}
		case '\\':
			started = true
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			}
		default:
			started = true
			cur.WriteByte(c)
		}
	}
	flush()
	return out, nil
}

// unreadable is what a normaliser emits when it cannot make a value
// comparable. Both sides producing it is agreement that neither can be read;
// one side producing it is reported, and reads as what it is.
const unreadable = "(not comparable: unbalanced quoting)"

// renderTokens quotes every token, so a comparison cannot confuse one argument
// containing a space with two arguments.
//
// A trailing newline is trimmed from each token. A YAML block scalar —
// `test: |` with a python script under it, which is how the corpus's largest
// project writes its healthchecks — carries a final newline the file really
// does contain, and Compose strips it on the way out. That is Compose's
// canonicalisation, not a difference in the command, and it is the ONLY thing
// trimmed: interior whitespace is left exactly as written, because the
// difference between `a  b` and `a b` inside one argument is a real one.
func renderTokens(tokens []string) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = strconv.Quote(strings.TrimRight(t, "\n"))
	}
	return strings.Join(parts, " ")
}

// shellForm renders a shell-form string as its exact token list.
func shellForm(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	tokens, err := shellTokens(s)
	if err != nil {
		return unreadable
	}
	return renderTokens(tokens)
}

// listForm renders an already-tokenised command — the long `command: [a, b]`
// form, and every command the oracle emits, since Compose always canonicalises
// to a list. No tokenisation is involved: the file already said where the
// argument boundaries are.
func listForm(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return renderTokens(tokens)
}

// passthroughNames is every `FOO:` written with no value — Compose's
// environment passthrough. See keyValueOf.
func passthroughNames(v *resolve.Value) map[string]bool {
	out := map[string]bool{}
	if v == nil || v.Kind() != resolve.KindMapping {
		return out
	}
	m := v.Map()
	for _, k := range m.Keys() {
		if e, _ := m.Get(k); e != nil && e.Kind() == resolve.KindNull {
			out[k] = true
		}
	}
	return out
}

// envFileNames is every variable name the service's `env_file` entries supply.
//
// Reading the files is the only way to tell "Compose folded this in" from "the
// merge lost a key", and the difference between those two is the whole point of
// this harness.
func envFileNames(l *resolve.Value, dir string) map[string]bool {
	out := map[string]bool{}
	ef := field(l, "env_file")
	if ef == nil {
		return out
	}
	var paths []string
	switch ef.Kind() {
	case resolve.KindScalar:
		paths = append(paths, ef.Scalar())
	case resolve.KindSequence:
		for _, e := range ef.Seq() {
			switch e.Kind() {
			case resolve.KindScalar:
				paths = append(paths, e.Scalar())
			case resolve.KindMapping:
				paths = append(paths, scalarOf(field(e, "path")))
			}
		}
	}
	for _, rel := range paths {
		if strings.TrimSpace(rel) == "" {
			continue
		}
		p := rel
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for k := range resolve.ParseDotEnv(src) {
			out[k] = true
		}
	}
	return out
}

// healthTest renders `healthcheck.test`, adding the implicit `CMD-SHELL` that
// Compose adds when the test is written as a bare string. Compose's expansion
// is not a different test; it is the same test spelled out.
//
// The string is NOT tokenised. `test: curl -f http://x` becomes the two-element
// list ["CMD-SHELL", "curl -f http://x"] — the whole command line is one
// argument, because CMD-SHELL hands it to a shell. Splitting it into words was
// the old behaviour and it agreed with the oracle only because the oracle's
// side was being re-joined and re-split to match.
func healthTest(v *resolve.Value) string {
	if v == nil {
		return ""
	}
	if v.Kind() == resolve.KindScalar {
		return listForm([]string{"CMD-SHELL", v.Scalar()})
	}
	return commandTokens(v)
}

// keyValueOf reads `environment`/`labels` in either syntax into a map.
func keyValueOf(v *resolve.Value) map[string]string {
	out := map[string]string{}
	if v == nil {
		return out
	}
	switch v.Kind() {
	case resolve.KindMapping:
		m := v.Map()
		for _, k := range m.Keys() {
			e, _ := m.Get(k)
			if e != nil && e.Kind() == resolve.KindNull {
				// `FOO:` with no value is Compose's environment PASSTHROUGH:
				// the value comes from the process environment at run time, not
				// from the document. The oracle fills it in; the model keeps
				// the null, because a null is what the file says. Neither is
				// wrong, and there is nothing here for a merge to get wrong,
				// so the name is skipped.
				continue
			}
			out[k] = scalarOf(e)
		}
	case resolve.KindSequence:
		for _, e := range v.Seq() {
			if e.Kind() != resolve.KindScalar {
				continue
			}
			k, val, _ := strings.Cut(e.Scalar(), "=")
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return out
}

func anyKeyValue(v any) map[string]string {
	out := map[string]string{}
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			out[k] = scalarOfAny(e)
		}
	case []any:
		for _, e := range t {
			s := scalarOfAny(e)
			k, val, _ := strings.Cut(s, "=")
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return out
}

// portSet normalises published ports to `published:target/protocol`.
func portSet(v *resolve.Value) []string {
	var out []string
	if v == nil || v.Kind() != resolve.KindSequence {
		return out
	}
	for _, e := range v.Seq() {
		switch e.Kind() {
		case resolve.KindScalar:
			out = append(out, normalisePortString(e.Scalar())...)
		case resolve.KindMapping:
			target := scalarOf(field(e, "target"))
			published := scalarOf(field(e, "published"))
			proto := scalarOf(field(e, "protocol"))
			if proto == "" {
				proto = "tcp"
			}
			out = append(out, published+":"+target+"/"+proto)
		}
	}
	return out
}

// normalisePortString expands the short syntax, including a host range, into
// the same shape the long syntax produces.
func normalisePortString(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	proto := "tcp"
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		proto, s = s[i+1:], s[:i]
	}
	var published, target string
	if i := strings.LastIndexByte(s, ':'); i < 0 {
		target = s
	} else {
		target = s[i+1:]
		rest := s[:i]
		if j := strings.LastIndexByte(rest, ':'); j >= 0 {
			rest = rest[j+1:] // drop the host IP; Compose reports it separately
		}
		published = rest
	}
	// A range `8000-8002:80` expands on the Compose side into one entry per
	// port. Comparing the unexpanded form against the expanded one is a
	// divergence about ranges, not about merging, so both collapse to the
	// range's own text.
	return []string{published + ":" + target + "/" + proto}
}

func oraclePortSet(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	// Compose expands a host range into one entry per port. Collapsing them
	// back to a contiguous range would be guesswork, so a project publishing a
	// range is compared on the container side only.
	var out []string
	for _, e := range list {
		m := asMap(e)
		target := scalarOfAny(m["target"])
		published := scalarOfAny(m["published"])
		proto := scalarOfAny(m["protocol"])
		if proto == "" {
			proto = "tcp"
		}
		out = append(out, published+":"+target+"/"+proto)
	}
	return out
}

// mountTargets compares the container path a mount lands on. The SOURCE is not
// compared: Compose absolutises a bind source against the project directory,
// and comparing `./data` against `/home/x/repo/data` measures nothing.
func mountTargets(v *resolve.Value) []string {
	var out []string
	if v == nil || v.Kind() != resolve.KindSequence {
		return out
	}
	for _, e := range v.Seq() {
		switch e.Kind() {
		case resolve.KindScalar:
			parts := strings.Split(e.Scalar(), ":")
			// A single-letter first field is a Windows drive letter — but only
			// if it is a LETTER. `.:/code` also splits to a one-character first
			// field and is an ordinary relative bind.
			if len(parts) > 1 && len(parts[0]) == 1 && isASCIILetter(parts[0][0]) {
				parts = append([]string{parts[0] + ":" + parts[1]}, parts[2:]...)
			}
			if len(parts) == 1 {
				out = append(out, normaliseTarget(parts[0]))
			} else {
				out = append(out, normaliseTarget(parts[1]))
			}
		case resolve.KindMapping:
			out = append(out, normaliseTarget(scalarOf(field(e, "target"))))
		}
	}
	return out
}

// normaliseTarget drops a trailing slash. `/var/www/html/` and `/var/www/html`
// are the same mount point, and Compose canonicalises one to the other.
func normaliseTarget(s string) string {
	if len(s) > 1 {
		return strings.TrimSuffix(s, "/")
	}
	return s
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func oracleMountTargets(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list {
		out = append(out, normaliseTarget(scalarOfAny(asMap(e)["target"])))
	}
	return out
}

func dependsOn(v *resolve.Value) map[string]string {
	out := map[string]string{}
	if v == nil {
		return out
	}
	switch v.Kind() {
	case resolve.KindSequence:
		for _, e := range v.Seq() {
			if s := scalarOf(e); s != "" {
				out[s] = "service_started"
			}
		}
	case resolve.KindMapping:
		m := v.Map()
		for _, k := range m.Keys() {
			e, _ := m.Get(k)
			cond := scalarOf(field(e, "condition"))
			if cond == "" {
				cond = "service_started"
			}
			out[k] = cond
		}
	}
	return out
}

func oracleDependsOn(v any) map[string]string {
	out := map[string]string{}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if s := scalarOfAny(e); s != "" {
				out[s] = "service_started"
			}
		}
	case map[string]any:
		for k, e := range t {
			cond := scalarOfAny(asMap(e)["condition"])
			if cond == "" {
				cond = "service_started"
			}
			out[k] = cond
		}
	}
	return out
}

// listAttributes are the service keys whose value is a plain list of strings on
// both sides, so a multiset comparison over them is meaningful without any
// per-field normalisation. Keys whose canonical form differs structurally —
// `environment`, `env_file`, `networks`, `build` — are handled above or not at
// all, deliberately.
var listAttributes = []string{
	"expose", "dns", "dns_opt", "dns_search", "cap_add", "cap_drop",
	"tmpfs", "links", "security_opt", "group_add", "volumes_from",
	"external_links", "extra_hosts",
}

func mapEach(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

// hostEntry normalises an `extra_hosts` entry to `name=address`. Compose emits
// that spelling; a file may write either.
func hostEntry(s string) string {
	if strings.Contains(s, "=") {
		return s
	}
	if i := strings.Index(s, ":"); i > 0 {
		return s[:i] + "=" + s[i+1:]
	}
	return s
}

func stringSet(v *resolve.Value) []string {
	var out []string
	if v == nil {
		return out
	}
	switch v.Kind() {
	case resolve.KindScalar:
		return []string{scalarOf(v)}
	case resolve.KindSequence:
		for _, e := range v.Seq() {
			if s := scalarOf(e); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func anyStringSet(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list {
		if s := scalarOfAny(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func topLevelNames(p *resolve.Project, section string) []string {
	v, ok := p.At(resolve.Path{section})
	if !ok || v.Kind() != resolve.KindMapping {
		return nil
	}
	return v.Map().Keys()
}

// ---- set and map comparison -------------------------------------------------

// diffSets compares two lists as MULTISETS: sorted, with duplicates kept.
//
// It used to deduplicate both sides before comparing, and that hid the single
// most likely merge bug there is. `appendUnique` failing to recognise that an
// override re-declares a port, a volume, an exposed port or a capability
// produces a list with the entry twice — and against a deduplicating comparison
// that is byte-identical to the correct answer, so the harness reported pass.
// Compose deduplicates these lists; a model that does not is not the same
// model, and the difference has to be visible.
func diffSets(path string, l, o []string) (divergence, bool) {
	ls, os_ := normMulti(l), normMulti(o)
	if strings.Join(ls, ",") == strings.Join(os_, ",") {
		return divergence{}, true
	}
	return divergence{Path: path, Composure: render(ls), Compose: render(os_)}, false
}

// render annotates repeats so a multiplicity divergence reads as one. Without
// it, `8080:80/tcp,8080:80/tcp` against `8080:80/tcp` looks like a typo in the
// report rather than the finding it is.
func render(in []string) string {
	var b strings.Builder
	for i := 0; i < len(in); {
		j := i
		for j < len(in) && in[j] == in[i] {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(in[i])
		if j-i > 1 {
			fmt.Fprintf(&b, " (x%d)", j-i)
		}
		i = j
	}
	return b.String()
}

func diffMaps(path string, l, o map[string]string) (divergence, bool) {
	if len(l) == 0 && len(o) == 0 {
		return divergence{}, true
	}
	lr, or := renderMap(l), renderMap(o)
	if lr == or {
		return divergence{}, true
	}
	return divergence{Path: path, Composure: lr, Compose: or}, false
}

func renderMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k + "=" + m[k])
	}
	return b.String()
}

// normSet deduplicates and sorts. It is for the places where a SET is genuinely
// the right model — top-level resource NAMES, which are mapping keys and cannot
// repeat — and nowhere else.
func normSet(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// normMulti sorts and keeps duplicates. Empty entries are dropped: they are a
// rendering artefact of a normaliser that had nothing to say, not a list
// member either side declared.
func normMulti(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// notIn returns the members of want that have are absent from got.
func notIn(want, got []string) []string {
	have := map[string]bool{}
	for _, s := range got {
		have[s] = true
	}
	var out []string
	for _, s := range want {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

func without(in []string, name string, unless []string) []string {
	for _, u := range unless {
		if u == name {
			return in
		}
	}
	out := in[:0:0]
	for _, s := range in {
		if s != name {
			out = append(out, s)
		}
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ---- reporting --------------------------------------------------------------

type report struct {
	Skipped    bool   `json:"skipped"`
	SkipReason string `json:"skip_reason,omitempty"`
	Root       string `json:"root,omitempty"`
	Fixtures   string `json:"fixtures,omitempty"`
	Oracle     string `json:"oracle,omitempty"`

	Files            int `json:"files"`
	Compared         int `json:"compared"`
	Passed           int `json:"passed"`
	Diverged         int `json:"diverged"`
	BothRefused      int `json:"both_refused"`
	OracleRefused    int `json:"oracle_refused"`
	ComposureRefused int `json:"composure_refused"`

	// Documented is how many projects diverged exactly as their DIVERGES file
	// says they do. They are NOT in Compared and NOT in Passed: a documented
	// disagreement is a disagreement, and folding it into a pass rate is how a
	// harness comes to report 100% while knowing it is wrong somewhere.
	Documented int `json:"documented_divergences"`
	// DocumentedMultiFile is how many of those exercised a merge, so the
	// merge-coverage claim can be read without them being counted as agreement.
	DocumentedMultiFile int `json:"documented_divergences_multi_file"`
	// Changed is how many registered projects diverged differently than
	// documented — in either direction. Any of these fails the run.
	Changed int `json:"divergences_changed"`

	// MultiFile is how many of the COMPARED projects merged more than one
	// file, and MultiFilePassed how many of those agreed. Without these two the
	// headline is unreadable: a 100% pass rate over 146 single files says
	// nothing about the merge, which is the only thing this harness exists to
	// check.
	MultiFile       int     `json:"multi_file"`
	MultiFilePassed int     `json:"multi_file_passed"`
	MultiFilePct    float64 `json:"multi_file_pass_pct"`

	// PassPct is the headline: of the projects BOTH sides resolved, the share
	// on which they agree. Projects neither side could read are excluded,
	// because a shared refusal proves nothing about the merge.
	PassPct float64 `json:"pass_pct"`

	Outcomes []outcome `json:"outcomes"`
}

func (r *report) add(o outcome) {
	r.Files++
	r.Outcomes = append(r.Outcomes, o)
	switch o.Status {
	case "pass":
		r.Compared++
		r.Passed++
		if o.MultiFile {
			r.MultiFile++
			r.MultiFilePassed++
		}
	case "diverged":
		r.Compared++
		r.Diverged++
		if o.MultiFile {
			r.MultiFile++
		}
	case "documented-divergence":
		// Its own bucket, entirely outside Compared and MultiFile. It is neither
		// an agreement to be counted nor an unexplained failure to be chased,
		// and putting it in MultiFile-but-not-MultiFilePassed would read as "the
		// merge is 75% correct" — a documented divergence dressed up as a merge
		// bug is no better than one dressed up as a pass.
		r.Documented++
		if o.MultiFile {
			r.DocumentedMultiFile++
		}
	case "divergence-changed":
		r.Changed++
	case "both-refused":
		r.BothRefused++
	case "oracle-refused":
		r.OracleRefused++
	case "composure-refused":
		r.ComposureRefused++
	}
}

func (r *report) finish() {
	if r.Compared > 0 {
		r.PassPct = float64(r.Passed) * 100 / float64(r.Compared)
	}
	if r.MultiFile > 0 {
		r.MultiFilePct = float64(r.MultiFilePassed) * 100 / float64(r.MultiFile)
	}
}

func (r *report) print(verbose bool) {
	fmt.Printf("\nDIFFERENTIAL — composure vs %s config\n", r.Oracle)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("corpus:          %s\n", r.Root)
	if r.Fixtures != "" {
		fmt.Printf("fixtures:           %s\n", r.Fixtures)
	}
	fmt.Printf("projects:           %d\n", r.Files)
	fmt.Printf("compared:           %d  (both sides resolved)\n", r.Compared)
	fmt.Printf("  agreed:           %d\n", r.Passed)
	fmt.Printf("  diverged:         %d\n", r.Diverged)
	fmt.Printf("both refused:       %d\n", r.BothRefused)
	fmt.Printf("oracle refused:     %d  (composure resolved, compose would not)\n", r.OracleRefused)
	fmt.Printf("composure refused:  %d  (compose resolved, composure would not)\n", r.ComposureRefused)
	fmt.Printf("\nPASS RATE: %.2f%% of %d compared projects\n", r.PassPct, r.Compared)
	fmt.Printf("MULTI-FILE:  %d compared, %d agreed (%.2f%%)  <- the merge; the line above is mostly single files\n",
		r.MultiFile, r.MultiFilePassed, r.MultiFilePct)
	if r.MultiFile == 0 {
		fmt.Printf("\n  *** NO MULTI-FILE PROJECT WAS COMPARED. This run exercised no merge. ***\n")
	}
	// Printed directly under the headline, and worded so it cannot be read as
	// part of it. A pass rate quoted without this line is quoted over a set that
	// deliberately excludes the places we know we are different.
	if r.Documented > 0 {
		fmt.Printf("\nDOCUMENTED DIVERGENCES: %d project(s) DISAGREE with `docker compose` by design (%d of them merges).\n",
			r.Documented, r.DocumentedMultiFile)
		fmt.Printf("  They are NOT in the %d compared above, NOT in the multi-file figure, and NOT agreement.\n", r.Compared)
		fmt.Printf("  The harness asserts each disagreement still happens, and fails if it changes either way.\n")
	}
	if r.Changed > 0 {
		fmt.Printf("\n*** %d REGISTERED DIVERGENCE(S) CHANGED. A documented disagreement is now a different\n", r.Changed)
		fmt.Printf("    disagreement, or no disagreement at all. This is a failure either way. ***\n")
	}

	for _, o := range r.Outcomes {
		if o.Status != "documented-divergence" && o.Status != "divergence-changed" {
			continue
		}
		label := "DOCUMENTED DIVERGENCE — not agreement, and not counted in the pass rate"
		if o.Status == "divergence-changed" {
			label = "REGISTERED DIVERGENCE CHANGED — " + o.Detail
		}
		fmt.Printf("\n%s\n  %s\n", o.File, label)
		fmt.Printf("  registered in %s: %s\n", divergesFile, strings.Join(o.Expected, ", "))
		for _, d := range o.Divergences {
			fmt.Printf("  %-44s composure=%q compose=%q\n", d.Path, trim(d.Composure), trim(d.Compose))
		}
	}

	shown := 0
	for _, o := range r.Outcomes {
		if o.Status != "diverged" {
			continue
		}
		switch {
		case o.MultiFile && len(o.Files) > 0:
			fmt.Printf("\n%s  [MERGE: %s]\n", o.File, strings.Join(baseNames(o.Files), " + "))
		case o.MultiFile:
			fmt.Printf("\n%s  [MERGE: directory pickup]\n", o.File)
		default:
			fmt.Printf("\n%s\n", o.File)
		}
		for i, d := range o.Divergences {
			if !verbose && i == 3 {
				fmt.Printf("  ... and %d more\n", len(o.Divergences)-3)
				break
			}
			fmt.Printf("  %-44s composure=%q compose=%q\n", d.Path, trim(d.Composure), trim(d.Compose))
		}
		shown++
	}
	if r.ComposureRefused > 0 {
		fmt.Printf("\nREFUSED BY COMPOSURE ONLY\n")
		for _, o := range r.Outcomes {
			if o.Status == "composure-refused" {
				fmt.Printf("  %-60s %s\n", o.File, trim(o.Detail))
			}
		}
	}
	fmt.Println()
}

func baseNames(paths []string) []string {
	return mapEach(paths, filepath.Base)
}

func trim(s string) string {
	if len(s) > 110 {
		return s[:107] + "..."
	}
	return s
}

func emit(r report) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		fmt.Fprintf(os.Stderr, "differential: %v\n", err)
		os.Exit(1)
	}
}
