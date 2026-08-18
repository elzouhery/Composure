package main

// End-to-end tests for the command line itself, and the golden that pins the
// JSON wire the VS Code extension consumes.
//
// WHY A SUBPROCESS. Every other test in this package calls into the run*
// functions or drives `serve` in-process, and none of them can see `main`'s
// dispatch: deleting `case "topology"` from main.go removes the subcommand from
// the product while leaving runTopology, the library and the RPC server intact,
// so the whole suite stays green. Epic 2's acceptance criteria are written as
// CLI invocations — "when I run `composure topology --profile prod -json .`" — and
// the invocation is the thing under test. Spawning the built binary is the only
// way to exercise argument parsing, dispatch, stdout and the exit code as one
// unit, and it needs no change to production code to be testable, so the shape
// of `main` is not bent around the test.
//
// WHY A GOLDEN. TestServeExplainMatchesTheCLIStruct marshals the same struct
// down two paths and compares them; it is an agreement test with no independent
// reference, so any change to the struct moves both sides equally. Tagging
// `Override.Origin` as `json:"-"` — which deletes the file, line and column of
// every overridden value from every JSON output in the product, i.e. R1.8 —
// leaves it green. A committed golden is the independent reference: the diff is
// the review artefact, and every line of it is a change to a published
// contract.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden rewrites the golden files from this run. It is for regenerating
// them after a DELIBERATE wire change; the diff is what gets reviewed.
var updateGolden = flag.Bool("update-golden", false, "rewrite the CLI wire golden files")

// wireDir holds the fixture project. Commands are run WITH THIS AS THEIR
// WORKING DIRECTORY and named with bare basenames, so the file paths recorded
// in provenance are stable no matter where `go test` was invoked from.
const wireDir = "testdata/wire"

// wireChain is the three-file merge the golden pins. Three files, not two:
// `services.web.image` is then set once and replaced twice, so the override
// history has two entries and its ORDER is observable. With a single override
// a reversed list is indistinguishable from a correct one.
var wireChain = []string{"-f", "base.yaml", "-f", "override.yaml", "-f", "override2.yaml"}

var composureBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "composure-cli-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp dir:", err)
		os.Exit(1)
	}
	composureBin = filepath.Join(dir, "composure")
	build := exec.Command("go", "build", "-o", composureBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the composure binary under test:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type cliResult struct {
	stdout string
	stderr string
	code   int
}

// runCLI runs the built binary in dir with an environment stated in full.
//
// The environment is not inherited: the fixture interpolates ${COMPOSURE_WIRE_TAG}
// from its own `.env`, and Compose's precedence puts the host environment ABOVE
// `.env`, so an inherited variable of that name would silently change the
// golden on one machine and not another.
func runCLI(t *testing.T, dir string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(composureBin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	res := cliResult{stdout: out.String(), stderr: errOut.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.code = exit.ExitCode()
	default:
		t.Fatalf("running %s %s: %v", composureBin, strings.Join(args, " "), err)
	}
	return res
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(wireDir, name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		t.Logf("golden rewritten: %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden (run `go test ./cmd/composure -update-golden` to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the JSON wire changed.\n\n--- want (%s)\n%s\n--- got\n%s\n\n"+
			"If the change is deliberate, rerun with -update-golden and argue for the diff: "+
			"this is the contract the extension consumes.", path, want, got)
	}
}

// ---------------------------------------------------------------------------
// The golden wire.

// TestResolveWireGolden pins `composure resolve -json` byte for byte.
//
// This is the whole document: the file list, every value with its origin and
// override history, the profile block, and the findings. Gutting profilesWire,
// dropping Override.Origin, renaming a key, emitting a list as null or zeroing
// an origin all land in this diff.
func TestResolveWireGolden(t *testing.T) {
	res := runCLI(t, wireDir, append([]string{"resolve", "-json"}, wireChain...)...)
	if res.code != 0 {
		t.Fatalf("resolve exited %d\nstderr: %s", res.code, res.stderr)
	}
	checkGolden(t, "resolve.golden.json", []byte(res.stdout))
}

// explainGoldens are the paths the explain golden covers, one file each.
//
// Each is a different shape of answer, and each is a place provenance can be
// lost independently: an overridden scalar carries a history, an anchored value
// carries an alias site, an interpolated value carries its pre-interpolation
// text, and a null carries an origin while carrying no value at all.
var explainGoldens = []struct {
	name string
	at   string
}{
	{"explain.image.golden.json", "services.web.image"},
	{"explain.alias.golden.json", "services.web.restart"},
	{"explain.interpolated.golden.json", "services.web.environment.TAG"},
	{"explain.null.golden.json", "services.web.healthcheck"},
}

func TestExplainWireGolden(t *testing.T) {
	for _, tc := range explainGoldens {
		t.Run(tc.at, func(t *testing.T) {
			args := append([]string{"explain", "-json"}, wireChain...)
			args = append(args, tc.at)
			res := runCLI(t, wireDir, args...)
			if res.code != 0 {
				t.Fatalf("explain %s exited %d\nstderr: %s", tc.at, res.code, res.stderr)
			}
			checkGolden(t, tc.name, []byte(res.stdout))
		})
	}
}

// wireValue is the shape a resolved value takes on the wire, read back loosely
// so the guard below can walk it without depending on the model's Go types.
type wireValue struct {
	Kind   string `json:"kind"`
	Scalar string `json:"scalar"`
	Origin struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
		Step   int    `json:"step"`
	} `json:"origin"`
	Overrides []struct {
		Value  string          `json:"value"`
		Origin json.RawMessage `json:"origin"`
	} `json:"overrides"`
	Alias        string      `json:"alias"`
	AliasSite    *struct{}   `json:"alias_site"`
	Raw          string      `json:"raw"`
	Interpolated bool        `json:"interpolated"`
	Seq          []wireValue `json:"seq"`
	Map          []struct {
		Key   string    `json:"key"`
		Value wireValue `json:"value"`
	} `json:"map"`
}

type wireDoc struct {
	Files []struct {
		Path string `json:"path"`
		Step int    `json:"step"`
	} `json:"files"`
	Root     wireValue `json:"root"`
	Profiles struct {
		Declared []string `json:"declared"`
		Services []struct {
			Name         string      `json:"name"`
			Path         []string    `json:"path"`
			Members      []wireValue `json:"members"`
			AlwaysActive bool        `json:"always_active"`
		} `json:"services"`
	} `json:"profiles"`
	Findings []json.RawMessage `json:"findings"`
}

// TestWireFixtureStaysRich guards the golden against becoming weaker than it
// looks.
//
// A golden over a fixture that lost its second override, its anchor or its
// profiled service still passes — it just stops pinning them, silently. This is
// the degenerate-fixture failure the repository keeps producing, and it is the
// one thing a golden cannot catch about itself. Every count asserted here is
// asserted non-zero, so the guard cannot pass by inspecting nothing.
func TestWireFixtureStaysRich(t *testing.T) {
	res := runCLI(t, wireDir, append([]string{"resolve", "-json"}, wireChain...)...)
	if res.code != 0 {
		t.Fatalf("resolve exited %d\nstderr: %s", res.code, res.stderr)
	}
	var doc wireDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("decoding the wire: %v", err)
	}

	if len(doc.Files) != 3 {
		t.Errorf("the fixture merges %d files, want 3 — the override history needs two replacements to have an order", len(doc.Files))
	}

	// Walk every value once, counting the shapes the golden is supposed to pin.
	var (
		visited      int
		deepOverride int // values replaced twice or more
		anyOverride  int
		aliased      int
		interpolated int
		nulls        int
		positioned   int
	)
	var walk func(v wireValue, path string)
	walk = func(v wireValue, path string) {
		visited++
		if !(v.Origin.File == "" || v.Origin.Line < 1 || v.Origin.Column < 1) {
			positioned++
		}
		if len(v.Overrides) > 0 {
			anyOverride++
			for _, ov := range v.Overrides {
				if len(ov.Origin) == 0 || string(ov.Origin) == "null" {
					t.Errorf("%s: an override carries no origin — that is R1.8 gone from the wire", path)
				}
			}
		}
		if len(v.Overrides) >= 2 {
			deepOverride++
		}
		if v.Alias != "" {
			aliased++
		}
		if v.Interpolated && v.Raw != "" {
			interpolated++
		}
		if v.Kind == "null" {
			nulls++
		}
		for i, e := range v.Seq {
			walk(e, fmt.Sprintf("%s[%d]", path, i))
		}
		for _, e := range v.Map {
			walk(e.Value, path+"."+e.Key)
		}
	}
	walk(doc.Root, "")

	for _, c := range []struct {
		got  int
		what string
	}{
		{visited, "values in the document"},
		{positioned, "values carrying a real origin"},
		{anyOverride, "values with an override history"},
		{deepOverride, "values overridden twice or more (provenance with depth)"},
		{aliased, "values reached through an anchor"},
		{interpolated, "interpolated values"},
		{nulls, "null values"},
		{len(doc.Findings), "findings"},
	} {
		if c.got == 0 {
			t.Errorf("the fixture no longer produces any %s, so the golden stops pinning them", c.what)
		}
	}

	// The profile block, which is the part of the wire that can be replaced
	// wholesale with an empty struct today.
	if len(doc.Profiles.Declared) < 2 {
		t.Errorf("declared profiles = %v, want at least two distinct names", doc.Profiles.Declared)
	}
	var profiled, bare int
	for _, s := range doc.Profiles.Services {
		if s.AlwaysActive {
			bare++
			if len(s.Members) != 0 {
				t.Errorf("service %s is reported always-active while declaring %d profiles", s.Name, len(s.Members))
			}
			continue
		}
		profiled++
		if len(s.Members) == 0 {
			t.Errorf("service %s is reported profiled while declaring no profiles", s.Name)
		}
		for _, m := range s.Members {
			if m.Origin.File == "" || m.Origin.Line < 1 {
				t.Errorf("service %s: profile member %q carries no origin", s.Name, m.Scalar)
			}
		}
	}
	if profiled == 0 {
		t.Error("no service in the fixture declares a profile: always_active can never be false")
	}
	if bare == 0 {
		t.Error("no service in the fixture is bare: always_active can never be true")
	}
	// The services list must be complete — an absent entry and "always active"
	// must not look alike, which is what the wire's own comment promises.
	var services int
	for _, e := range doc.Root.Map {
		if e.Key == "services" {
			services = len(e.Value.Map)
		}
	}
	if services == 0 {
		t.Fatal("the fixture declares no services")
	}
	if len(doc.Profiles.Services) != services {
		t.Errorf("the profile block covers %d services, the document declares %d", len(doc.Profiles.Services), services)
	}
}

// ---------------------------------------------------------------------------
// Dispatch.

// TestUnknownSubcommandIsRefused establishes what a MISSING subcommand looks
// like, which is what every case below is distinguished from: a deleted `case`
// falls through to the default and exits 2 without touching stdout.
func TestUnknownSubcommandIsRefused(t *testing.T) {
	res := runCLI(t, wireDir, "toplogy", "-json", "base.yaml")
	if res.code != 2 {
		t.Errorf("an unknown subcommand exited %d, want 2", res.code)
	}
	if res.stdout != "" {
		t.Errorf("an unknown subcommand wrote to stdout: %q", res.stdout)
	}
	if !strings.Contains(res.stderr, "unknown command") {
		t.Errorf("stderr does not name the problem: %q", res.stderr)
	}
}

// TestEverySubcommandDispatches is the end-to-end coverage of `main`'s switch.
//
// Every subcommand whose acceptance criterion is written as a CLI invocation is
// here. Each case asserts the exit code, that stdout is JSON, and something
// only THAT subcommand produces — so deleting the case, emitting nothing, or
// dispatching to a different run* function each fail. `apply` is covered
// separately because it writes.
func TestEverySubcommandDispatches(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		// check receives the decoded stdout and asserts what only this
		// subcommand emits.
		check func(t *testing.T, doc map[string]any)
	}{
		{
			name: "resolve",
			args: []string{"resolve", "-json", "base.yaml"},
			check: func(t *testing.T, doc map[string]any) {
				root, ok := doc["root"].(map[string]any)
				if !ok {
					t.Fatalf("no root in %v", keysOf(doc))
				}
				if len(root["map"].([]any)) == 0 {
					t.Error("resolve emitted an empty document")
				}
				if _, ok := doc["profiles"]; !ok {
					t.Error("resolve emitted no profile block")
				}
			},
		},
		{
			name: "explain",
			args: []string{"explain", "-json", "services.web.image", "base.yaml"},
			check: func(t *testing.T, doc map[string]any) {
				if doc["path"] != "services.web.image" {
					t.Errorf("path = %v, want services.web.image", doc["path"])
				}
				if doc["value"] != "nginx:1.25" {
					t.Errorf("value = %v, want nginx:1.25", doc["value"])
				}
				if doc["file"] != "base.yaml" || doc["line"] == nil {
					t.Errorf("explain lost its provenance: file=%v line=%v", doc["file"], doc["line"])
				}
			},
		},
		{
			name: "topology",
			args: []string{"topology", "-json", "--profile", "edge", "base.yaml"},
			check: func(t *testing.T, doc map[string]any) {
				nodes, ok := doc["nodes"].([]any)
				if !ok || len(nodes) == 0 {
					t.Fatalf("topology emitted no nodes: %v", keysOf(doc))
				}
				if _, ok := doc["edges"]; !ok {
					t.Error("topology emitted no edge list")
				}
			},
		},
		{
			name: "diagnose",
			// The fixture references an undefined variable, so diagnose exits
			// by severity — 20 for a warning. That IS the documented contract
			// and a subcommand that vanished would exit 2 instead.
			args: []string{"diagnose", "-json", "base.yaml"},
			code: 20,
			check: func(t *testing.T, doc map[string]any) {
				rules, ok := doc["rules"].([]any)
				if !ok || len(rules) == 0 {
					t.Fatalf("diagnose ran no rules: %v", keysOf(doc))
				}
				findings, ok := doc["findings"].([]any)
				if !ok || len(findings) == 0 {
					t.Error("diagnose reported nothing on a fixture with an undefined variable")
				}
			},
		},
		{
			name: "schema",
			args: []string{"schema", "-json", "-at", "services.db", "base.yaml"},
			check: func(t *testing.T, doc map[string]any) {
				node, ok := doc["node"].(map[string]any)
				if !ok {
					t.Fatalf("schema emitted no node: %v", keysOf(doc))
				}
				fields, ok := node["fields"].([]any)
				if !ok || len(fields) == 0 {
					t.Error("schema listed no fields for services.db")
				}
			},
		},
		{
			name: "dockerfile",
			args: []string{"dockerfile", "-json", "Dockerfile"},
			check: func(t *testing.T, doc map[string]any) {
				stages, ok := doc["stages"].([]any)
				if !ok || len(stages) != 2 {
					t.Fatalf("dockerfile reported %v stages, want the fixture's 2", len(stages))
				}
			},
		},
		{
			name: "impact",
			args: []string{"impact", "-json", "--profile", "edge", "services.db", "base.yaml"},
			check: func(t *testing.T, doc map[string]any) {
				if doc["subject"] != "services.db" {
					t.Errorf("subject = %v, want services.db", doc["subject"])
				}
				dependents, ok := doc["dependents"].([]any)
				if !ok || len(dependents) == 0 {
					t.Error("impact reported no dependents for a service two others depend on")
				}
			},
		},
		{
			name: "preview",
			args: []string{"preview", "-json", "-op", "replace_scalar", "-at", "services.db.image", "-value", "postgres:17", "base.yaml"},
			check: func(t *testing.T, doc map[string]any) {
				if doc["written"] != false {
					t.Errorf("preview reported written=%v; preview writes nothing", doc["written"])
				}
				diff, _ := doc["diff"].(string)
				if !strings.Contains(diff, "postgres:17") {
					t.Errorf("preview produced no diff naming the new value: %q", diff)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runCLI(t, wireDir, tc.args...)
			if res.code != tc.code {
				t.Fatalf("`composure %s` exited %d, want %d\nstdout: %s\nstderr: %s",
					strings.Join(tc.args, " "), res.code, tc.code, res.stdout, res.stderr)
			}
			if strings.TrimSpace(res.stdout) == "" {
				t.Fatalf("`composure %s` wrote nothing to stdout\nstderr: %s", strings.Join(tc.args, " "), res.stderr)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
				t.Fatalf("stdout is not a JSON object: %v\n%s", err, res.stdout)
			}
			tc.check(t, doc)
		})
	}
}

func keysOf(doc map[string]any) []string {
	out := make([]string, 0, len(doc))
	for k := range doc {
		out = append(out, k)
	}
	return out
}

// TestApplyDispatchesAndWrites is `apply` end to end, on a copy of the fixture
// because it is the one subcommand that changes a file.
func TestApplyDispatchesAndWrites(t *testing.T) {
	dir := copyFixture(t)
	before, err := os.ReadFile(filepath.Join(dir, "base.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, dir, "apply", "-json", "-op", "replace_scalar",
		"-at", "services.db.image", "-value", "postgres:17", "base.yaml")
	if res.code != 0 {
		t.Fatalf("apply exited %d\nstderr: %s", res.code, res.stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
	}
	if doc["written"] != true {
		t.Errorf("apply reported written=%v", doc["written"])
	}
	after, err := os.ReadFile(filepath.Join(dir, "base.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("apply changed nothing on disk")
	}
	if !bytes.Contains(after, []byte("postgres:17")) {
		t.Error("the new value is not in the file")
	}
	// The splice promise, asserted end to end: one line differs and the rest of
	// the file is byte-identical.
	changed := 0
	b, a := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(a) != len(b) {
		t.Fatalf("apply changed the line count: %d -> %d", len(b), len(a))
	}
	for i := range b {
		if b[i] != a[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("apply changed %d lines, want exactly 1", changed)
	}
}

func copyFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(wireDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src, err := os.ReadFile(filepath.Join(wireDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// TestTopologyProfileFlagReachesTheGraph asserts the flag arrives at
// topology.Build, not merely that it parses.
//
// Replacing `profiles` with `nil` at the call site disconnects the flag while
// leaving the flag definition, the parser, the library and every in-process
// test intact. The only thing that changes is the graph the CLI prints — so the
// assertion is that two different --profile values print two different graphs,
// and that each names the profile it was given.
func TestTopologyProfileFlagReachesTheGraph(t *testing.T) {
	nodesFor := func(t *testing.T, args ...string) (nodes []string, profiles []string) {
		t.Helper()
		res := runCLI(t, wireDir, args...)
		if res.code != 0 {
			t.Fatalf("topology exited %d\nstderr: %s", res.code, res.stderr)
		}
		var doc struct {
			Profiles []string `json:"profiles"`
			Nodes    []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, res.stdout)
		}
		for _, n := range doc.Nodes {
			nodes = append(nodes, n.ID)
		}
		if len(nodes) == 0 {
			t.Fatalf("topology emitted no nodes for %v", args)
		}
		return nodes, doc.Profiles
	}

	none, noneProfiles := nodesFor(t, "topology", "-json", "base.yaml")
	frontend, frontendProfiles := nodesFor(t, "topology", "-json", "--profile", "frontend", "base.yaml")
	edge, edgeProfiles := nodesFor(t, "topology", "-json", "--profile", "edge", "base.yaml")

	// The flag is reported back as given.
	if len(noneProfiles) != 0 {
		t.Errorf("no --profile reported active profiles %v", noneProfiles)
	}
	if len(frontendProfiles) != 1 || frontendProfiles[0] != "frontend" {
		t.Errorf("--profile frontend reported %v", frontendProfiles)
	}
	if len(edgeProfiles) != 1 || edgeProfiles[0] != "edge" {
		t.Errorf("--profile edge reported %v", edgeProfiles)
	}

	// And it reaches the graph. `web` is in `frontend`, `metrics` is in `edge`
	// and neither is always-active, so all three node sets differ.
	has := func(set []string, id string) bool {
		for _, s := range set {
			if s == id {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		set   []string
		id    string
		want  bool
		label string
	}{
		{none, "services.db", true, "no --profile"},
		{none, "services.web", false, "no --profile"},
		{none, "services.metrics", false, "no --profile"},
		{frontend, "services.web", true, "--profile frontend"},
		{frontend, "services.metrics", false, "--profile frontend"},
		{edge, "services.metrics", true, "--profile edge"},
		{edge, "services.web", true, "--profile edge"},
	} {
		if got := has(c.set, c.id); got != c.want {
			t.Errorf("%s: %s present = %v, want %v (nodes: %v)", c.label, c.id, got, c.want, c.set)
		}
	}

	// Stated once more as the property, so a fixture change that made the two
	// profiles select the same services would be caught rather than silently
	// turning the comparison into a tautology.
	if strings.Join(frontend, ",") == strings.Join(edge, ",") {
		t.Errorf("--profile frontend and --profile edge produced the same graph: %v", frontend)
	}
	if strings.Join(none, ",") == strings.Join(frontend, ",") {
		t.Errorf("--profile frontend produced the same graph as no profile at all: %v", none)
	}

	// A comma-separated list is the same as two flags — the grammar the help
	// text promises, exercised through the process rather than through the
	// flag.Value in isolation.
	both, bothProfiles := nodesFor(t, "topology", "-json", "--profile", "frontend,edge", "base.yaml")
	if len(bothProfiles) != 2 {
		t.Errorf("--profile frontend,edge reported %v, want both", bothProfiles)
	}
	if !has(both, "services.web") || !has(both, "services.metrics") {
		t.Errorf("--profile frontend,edge selected %v", both)
	}
}
