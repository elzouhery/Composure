package schema

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	composespec "github.com/elzouhery/composure/schema"
)

const stack = `
version: "3.8"
services:
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: shipyard
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      LOG_LEVEL: ${LOG_LEVEL:-info}
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 5s
    x-owner: platform
  web:
    image: nginx
    profiles: [edge]
networks:
  default:
    driver: bridge
`

func mustProject(t *testing.T, src string) *resolve.Project {
	t.Helper()
	p, err := resolve.Bytes("compose.yaml", []byte(src))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

// Deterministic options: never probe the machine in a test. Whether the
// developer running this happens to have Compose installed must not decide
// what the assertions below see.
func opts() Options {
	return Options{
		Env:                 map[string]string{"DB_PASSWORD": "hunter2"},
		EnvKnown:            true,
		ComposeVersion:      "2.29.0",
		ComposeVersionKnown: true,
	}
}

func nodeAt(t *testing.T, p *resolve.Project, at string, o Options) *Node {
	t.Helper()
	n, err := Describe(p, resolve.ParsePath(at), o)
	if err != nil {
		t.Fatalf("describe %q: %v", at, err)
	}
	return n
}

func fieldNamed(t *testing.T, n *Node, key string) Field {
	t.Helper()
	for _, f := range n.Fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no field %q on %s; it has %d", key, n.Path, len(n.Fields))
	return Field{}
}

// ---------------------------------------------------------------------------
// AD-20 — the list is generated, and it is the schema's list.

func TestServiceOffersTheWholeSchemaMinusWhatIsDeclared(t *testing.T) {
	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())

	if !n.Known {
		t.Fatal("the specification describes a service; Known must be true")
	}
	// Declared: image, environment, healthcheck, x-owner. The extension key is
	// not in the schema and must still be shown — the inspector reports the
	// file, and hiding part of it because the vendored schema is older than the
	// file is the worst failure available to a tool whose claim is that it
	// shows you what is there.
	for _, key := range []string{"image", "environment", "healthcheck", "x-owner"} {
		if f := fieldNamed(t, n, key); !f.Declared {
			t.Errorf("%s is in the file but reported as not declared", key)
		}
	}
	// Available: a large, generated list. The exact number moves with the
	// vendored schema, so the assertion is on the shape, not a magic number.
	if n.AvailableCount < 60 {
		t.Errorf("only %d keys offered as available; the service schema has ~90", n.AvailableCount)
	}
	for _, key := range []string{"healthcheck", "image", "environment"} {
		for _, f := range n.Fields {
			if f.Key == key && !f.Declared {
				t.Errorf("%s is declared and must not also be offered as available", key)
			}
		}
	}
	// The differentiator, spelled out: keys nobody knows exist.
	for _, key := range []string{"develop", "ulimits", "cap_add", "security_opt", "tmpfs", "deploy"} {
		f := fieldNamed(t, n, key)
		if f.Declared {
			t.Errorf("%s is not in the file", key)
		}
	}
}

// The one thing that would make the list a lie: a hand-written copy of it.
func TestTheAvailableListComesFromTheVendoredFile(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal(composespec.Spec, &raw); err != nil {
		t.Fatalf("the vendored schema does not parse: %v", err)
	}
	defs := raw["$defs"].(map[string]any)
	svc := defs["service"].(map[string]any)
	props := svc["properties"].(map[string]any)

	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())
	seen := map[string]bool{}
	for _, f := range n.Fields {
		seen[f.Key] = true
	}
	for key := range props {
		if !seen[key] {
			t.Errorf("the schema names %q and the inspector does not offer it", key)
		}
	}
}

func TestUnknownPathStillShowsWhatIsDeclared(t *testing.T) {
	p := mustProject(t, `
services:
  db:
    x-thing:
      inner: 1
`)
	n := nodeAt(t, p, "services.db.x-thing", opts())
	// The specification says an `x-` extension may hold anything at all —
	// literally the empty schema. So there is nothing to offer, and the node
	// is marked free-form rather than reported as "0 available", which would
	// read as "nothing else is possible" instead of "anything is".
	if !n.FreeForm {
		t.Error("an x- extension permits any key; the node must say so")
	}
	if n.AvailableCount != 0 {
		t.Errorf("%d keys offered for a path the schema does not constrain", n.AvailableCount)
	}
	if f := fieldNamed(t, n, "inner"); !f.Declared || f.Value.Text != "1" {
		t.Errorf("the declared value is not carried through: %+v", f.Value)
	}
}

// ---------------------------------------------------------------------------
// AD-20 — the file's `version:` never selects a schema.

func TestVersionFieldIsReportedAndIgnored(t *testing.T) {
	p := mustProject(t, stack)
	res, err := Inspect("compose.yaml", p, opts())
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if res.VersionField != "3.8" {
		t.Errorf("the version field is %q, want it reported as 3.8", res.VersionField)
	}
	// And it changed nothing: the same file without the line offers the same
	// keys. This is the assertion that would fail the day someone "helpfully"
	// filters the schema by it.
	withVersion := nodeAt(t, p, "services.db", opts())
	without := nodeAt(t, mustProject(t, strings.Replace(stack, "version: \"3.8\"\n", "", 1)), "services.db", opts())
	if withVersion.AvailableCount != without.AvailableCount {
		t.Errorf("version: changed the available list, %d vs %d — it must be ignored",
			withVersion.AvailableCount, without.AvailableCount)
	}
}

// ---------------------------------------------------------------------------
// AD-21 — marked, never hidden; and nothing marked without a binary.

func TestKeysNewerThanTheInstalledComposeAreMarkedNotHidden(t *testing.T) {
	p := mustProject(t, stack)
	// 2.21.0 predates `develop` (2.22.0) and postdates `include` (2.20.0).
	o := opts()
	o.ComposeVersion, o.ComposeVersionKnown = "2.21.0", true
	n := nodeAt(t, p, "services.db", o)

	develop := fieldNamed(t, n, "develop") // present at all: not hidden
	if develop.Support != SupportNo {
		t.Errorf("develop support is %q on Compose 2.21.0, want %q", develop.Support, SupportNo)
	}
	if develop.MinVersion != "2.22.0" {
		t.Errorf("develop min version is %q", develop.MinVersion)
	}
	attach := fieldNamed(t, n, "attach")
	if attach.Support != SupportYes {
		t.Errorf("attach (2.20.0) support is %q on Compose 2.21.0, want %q", attach.Support, SupportYes)
	}
	// A key with no recorded minimum is offered with no mark, never as "no".
	if got := fieldNamed(t, n, "ulimits").Support; got != SupportUnknown {
		t.Errorf("ulimits has no recorded minimum; support is %q, want %q", got, SupportUnknown)
	}
}

func TestNoComposeBinaryMarksNothing(t *testing.T) {
	p := mustProject(t, stack)
	o := opts()
	o.ComposeVersion, o.ComposeVersionKnown = "", false
	n := nodeAt(t, p, "services.db", o)
	for _, f := range n.Fields {
		if f.Support != SupportUnknown {
			t.Fatalf("%s is marked %q with no Compose installed; the schema is offered whole", f.Key, f.Support)
		}
	}
	// And it is still the whole schema, not an empty pane.
	if n.AvailableCount < 60 {
		t.Errorf("only %d available with no binary; we degrade to useful, not to empty", n.AvailableCount)
	}
}

func TestVersionComparison(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.29.1", "2.22.0", 1},
		{"2.21.0", "2.22.0", -1},
		{"2.22.0", "2.22.0", 0},
		{"2.30.0-rc.1", "2.30.0", 0}, // an rc of the release that added a key has it
		{"2.30", "2.30.0", 0},
		{"3.0.0", "2.36.0", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if supportOf("2.22.0", "", false) != SupportUnknown {
		t.Error("an unknown installed version must mark nothing")
	}
	if supportOf("", "2.29.0", true) != SupportUnknown {
		t.Error("a key with no recorded minimum must mark nothing")
	}
}

// ---------------------------------------------------------------------------
// Story 5.1 — values, never bare keys; and ${VAR} literal plus resolution.

func TestEveryEnvironmentEntryCarriesItsValue(t *testing.T) {
	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())
	env := fieldNamed(t, n, "environment")
	if env.Value == nil || len(env.Value.Entries) != 3 {
		t.Fatalf("environment has %v entries, want three with their values", env.Value)
	}
	got := map[string]string{}
	for _, e := range env.Value.Entries {
		got[e.Key] = e.Value.Text
		if e.Value.Origin.IsZero() {
			t.Errorf("%s carries no origin", e.Key)
		}
		if e.Path == "" {
			t.Errorf("%s carries no config path, so nothing can join a finding to it", e.Key)
		}
	}
	if got["POSTGRES_DB"] != "shipyard" {
		t.Errorf("POSTGRES_DB is %q", got["POSTGRES_DB"])
	}
	// The literal, exactly as written. Never the resolution in its place.
	if got["POSTGRES_PASSWORD"] != "${DB_PASSWORD}" {
		t.Errorf("POSTGRES_PASSWORD is %q, want the literal", got["POSTGRES_PASSWORD"])
	}
}

func TestInterpolationShowsBothTheLiteralAndTheResolution(t *testing.T) {
	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())
	env := fieldNamed(t, n, "environment")
	byKey := map[string]ValueView{}
	for _, e := range env.Value.Entries {
		byKey[e.Key] = e.Value
	}

	pw := byKey["POSTGRES_PASSWORD"]
	if pw.Text != "${DB_PASSWORD}" || pw.Resolved != "hunter2" {
		t.Errorf("literal %q resolved %q, want both", pw.Text, pw.Resolved)
	}
	if len(pw.Undefined) != 0 {
		t.Errorf("DB_PASSWORD is defined; undefined = %v", pw.Undefined)
	}
	// A default IS a definition — the same rule the undefined-variable
	// diagnostic applies, and the two must not disagree.
	lvl := byKey["LOG_LEVEL"]
	if lvl.Resolved != "info" || len(lvl.Undefined) != 0 {
		t.Errorf("${LOG_LEVEL:-info} resolved to %q, undefined %v", lvl.Resolved, lvl.Undefined)
	}
	// A plain value has no resolution line at all.
	if db := byKey["POSTGRES_DB"]; db.Resolved != "" {
		t.Errorf("a value with no variable in it got a resolution: %q", db.Resolved)
	}
}

func TestUndefinedVariableIsNamedAndTheLiteralIsKept(t *testing.T) {
	p := mustProject(t, `
services:
  web:
    image: nginx
    environment:
      SECRET: ${SESSION_SECRET}
      URL: postgres://${DB_USER}:${DB_PASS}@db/app
`)
	o := opts()
	o.Env = map[string]string{}
	n := nodeAt(t, p, "services.web", o)
	env := fieldNamed(t, n, "environment")
	for _, e := range env.Value.Entries {
		switch e.Key {
		case "SECRET":
			if len(e.Value.Undefined) != 1 || e.Value.Undefined[0] != "SESSION_SECRET" {
				t.Errorf("undefined = %v", e.Value.Undefined)
			}
			// The literal is kept in place, not blanked: rendering an empty
			// string would be a confident wrong answer about what runs.
			if e.Value.Resolved != "${SESSION_SECRET}" {
				t.Errorf("resolved = %q, want the literal kept", e.Value.Resolved)
			}
		case "URL":
			if len(e.Value.Undefined) != 2 {
				t.Errorf("two undefined variables, got %v", e.Value.Undefined)
			}
		}
	}
}

func TestUnknownEnvironmentSaysNothing(t *testing.T) {
	p := mustProject(t, stack)
	o := opts()
	o.Env, o.EnvKnown = nil, false
	n := nodeAt(t, p, "services.db", o)
	for _, e := range fieldNamed(t, n, "environment").Value.Entries {
		if e.Value.Resolved != "" || len(e.Value.Undefined) != 0 {
			t.Errorf("%s claims a resolution with no environment established: %+v", e.Key, e.Value)
		}
		if e.Value.EnvKnown {
			t.Errorf("%s reports the environment as known when it is not", e.Key)
		}
	}
}

// ---------------------------------------------------------------------------
// Story 5.2 — defaults as placeholders, groups that carry their own list.

func TestUnsetKeysCarryTheSchemaDefaultWhereThereIsOne(t *testing.T) {
	p := mustProject(t, stack)
	hc := fieldNamed(t, nodeAt(t, p, "services.db", opts()), "healthcheck")
	if len(hc.Children) == 0 {
		t.Fatal("a declared healthcheck must carry its own field list, or the group has no available line")
	}
	var timeout, start Field
	for _, c := range hc.Children {
		switch c.Key {
		case "timeout":
			timeout = c
		case "start_period":
			start = c
		}
	}
	if timeout.Declared {
		t.Error("timeout is not in the file")
	}
	if timeout.Default != "30s" {
		t.Errorf("timeout default is %q, want the specification's 30s", timeout.Default)
	}
	if timeout.DefaultSource != "description" {
		t.Errorf("default source is %q; the spec records this one in prose", timeout.DefaultSource)
	}
	if start.Key == "" {
		t.Error("start_period must be offered on a healthcheck that omits it")
	}
	// And the declared children are still declared, with their values.
	for _, c := range hc.Children {
		if c.Key == "interval" && (!c.Declared || c.Value.Text != "5s") {
			t.Errorf("interval: declared=%v value=%+v", c.Declared, c.Value)
		}
	}
}

// A free-form map permits ANY key. Offering "these six" would be an invention,
// which is the same failure as a hand-written list wearing a different hat.
func TestFreeFormMapsOfferNothing(t *testing.T) {
	p := mustProject(t, stack)
	env := fieldNamed(t, nodeAt(t, p, "services.db", opts()), "environment")
	if !env.FreeForm {
		t.Error("environment is free-form; the schema names none of its keys")
	}
	if len(env.Children) != 0 {
		t.Errorf("%d children offered for a free-form map", len(env.Children))
	}
	n := nodeAt(t, p, "services.db.environment", opts())
	if n.AvailableCount != 0 {
		t.Errorf("%d keys offered inside environment; the schema permits any key at all", n.AvailableCount)
	}
	if n.DeclaredCount != 3 {
		t.Errorf("%d declared, want the three entries", n.DeclaredCount)
	}
}

func TestDescriptionsAreOneLineAndTheSpecsOwnWords(t *testing.T) {
	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())
	f := fieldNamed(t, n, "healthcheck")
	if f.Description == "" {
		t.Fatal("healthcheck has a description in the schema and must carry it")
	}
	if strings.Count(f.Description, ". ") > 0 {
		t.Errorf("the description is more than one sentence: %q", f.Description)
	}
	if len(f.Description) > 200 {
		t.Errorf("the description is %d characters", len(f.Description))
	}
}

// ---------------------------------------------------------------------------
// Story 5.1 — the stack itself, never an empty pane.

func TestStackLevelNodeIsNeverEmpty(t *testing.T) {
	p := mustProject(t, stack)
	res, err := Inspect("compose.yaml", p, opts())
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if res.Node.Path != "" || res.Node.Schema != "compose" {
		t.Errorf("the stack node is %+v", res.Node)
	}
	for _, key := range []string{"services", "networks"} {
		if f := fieldNamed(t, res.Node, key); !f.Declared {
			t.Errorf("%s is declared at the top level", key)
		}
	}
	if len(res.Files) != 1 || res.Files[0].Path != "compose.yaml" {
		t.Errorf("the source file list is %+v", res.Files)
	}
	if len(res.Profiles) != 1 || res.Profiles[0] != "edge" {
		t.Errorf("declared profiles are %v, want [edge]", res.Profiles)
	}
	if res.SchemaCommit != composespec.Commit {
		t.Errorf("the answer does not pin the schema revision: %q", res.SchemaCommit)
	}
}

func TestAllDescribesEveryService(t *testing.T) {
	p := mustProject(t, stack)
	o := opts()
	o.All = true
	res, err := Inspect("compose.yaml", p, o)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	seen := map[string]bool{}
	for _, n := range res.Nodes {
		seen[n.Path] = true
	}
	for _, want := range []string{"", "services.db", "services.web", "networks.default"} {
		if !seen[want] {
			t.Errorf("the -all answer has no node for %q", want)
		}
	}
}

func TestNilProjectIsRefused(t *testing.T) {
	if _, err := Inspect("compose.yaml", nil, opts()); err == nil {
		t.Error("inspecting nothing must be an error, not an empty answer")
	}
}

func TestUnknownConfigPathIsRefused(t *testing.T) {
	p := mustProject(t, stack)
	if _, err := Describe(p, resolve.ParsePath("services.nope.deploy.nothing"), opts()); err == nil {
		t.Error("a path in neither the file nor the schema must be refused, not answered emptily")
	}
}

// The wire shape is what the extension compiles against. A field renamed here
// draws an empty pane over there and nothing errors.
func TestWireShapeIsStable(t *testing.T) {
	p := mustProject(t, stack)
	res, err := Inspect("compose.yaml", p, opts())
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"path", "schema_commit", "compose_version", "compose_version_known",
		"files", "profiles", "node",
	} {
		if _, ok := back[key]; !ok {
			t.Errorf("the result has no %q", key)
		}
	}
	node := back["node"].(map[string]any)
	for _, key := range []string{"path", "schema", "known", "fields", "declared_count", "available_count"} {
		if _, ok := node[key]; !ok {
			t.Errorf("the node has no %q", key)
		}
	}
	fields := node["fields"].([]any)
	first := fields[0].(map[string]any)
	for _, key := range []string{"key", "declared", "path", "support"} {
		if _, ok := first[key]; !ok {
			t.Errorf("a field has no %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Story 7.9 — the values a key accepts.

// The three sources, each on a real key, asserted by their CONTENTS.
//
// Not by a count: a list of the right length and the wrong words is exactly the
// confident wrong answer this engine specialises in. And not on one key per
// source with one value in it, because a single-element list cannot tell a
// derived list from a hard-coded one.
func TestAllowedValuesComeFromTheSpecification(t *testing.T) {
	p := mustProject(t, stack)

	cases := []struct {
		at     string
		key    string
		want   []string
		source string
	}{
		// `enum` — the only machine-closed form, and the specification uses it
		// in nine places across the whole document.
		{"services.db.depends_on.web", "condition",
			[]string{"service_started", "service_healthy", "service_completed_successfully"}, "schema"},
		{"services.db", "cgroup", []string{"host", "private"}, "schema"},
		// `pattern` — an alternation of literals is a list, even though the
		// specification did not write it as one.
		{"services.db", "pull_policy",
			[]string{"always", "never", "build", "if_not_present", "missing", "refresh", "daily", "weekly"}, "pattern"},
		// prose — the specification's own "Options include: …" convention, which
		// is where it actually records most of these.
		{"services.db", "restart", []string{"no", "always", "on-failure", "unless-stopped"}, "description"},
		{"services.db", "network_mode", []string{"bridge", "host", "none"}, "description"},
		{"services.db.deploy", "mode", []string{"replicated", "global"}, "description"},
		{"services.db.deploy", "endpoint_mode", []string{"vip", "dnsrr"}, "description"},
	}
	for _, c := range cases {
		n := nodeAt(t, p, c.at, opts())
		f := fieldNamed(t, n, c.key)
		if got := strings.Join(f.Allowed, ","); got != strings.Join(c.want, ",") {
			t.Errorf("%s.%s allowed = [%s], want [%s]", c.at, c.key, got, strings.Join(c.want, ","))
		}
		if f.AllowedSource != c.source {
			t.Errorf("%s.%s allowed_source = %q, want %q", c.at, c.key, f.AllowedSource, c.source)
		}
	}
}

// The order is the specification's, never sorted. `no, always, on-failure,
// unless-stopped` is the order a reader comparing against the docs expects, and
// alphabetising it puts `always` first, which is not the default.
func TestAllowedValuesKeepTheSpecificationsOrder(t *testing.T) {
	p := mustProject(t, stack)
	f := fieldNamed(t, nodeAt(t, p, "services.db", opts()), "restart")
	if len(f.Allowed) == 0 || f.Allowed[0] != "no" {
		t.Fatalf("restart allowed = %v; the specification lists 'no' first", f.Allowed)
	}
}

// `schema` claims the list is CLOSED — "the specification allows nothing else",
// in the CLI's words and in every chip's accessible name. So it may only be
// claimed when the enumeration covers every form the key accepts.
//
// `gpus` is the counter-example the vendored specification actually contains:
// a `oneOf` of `{"type":"string","enum":["all"]}` and `{"type":"array", …}`, a
// list of GPU device objects. The enum sits on ONE arm. A reader told that the
// specification allows nothing else will not write the array form the
// specification does allow — and this engine's whole premise is that a
// confident wrong answer is worse than no answer.
//
// The values are still offered: `all` is real and useful. It is the CLAIM that
// is downgraded, to `schema-branch`.
func TestClosedIsClaimedOnlyWhenEveryFormIsEnumerated(t *testing.T) {
	p := mustProject(t, stack)
	f := fieldNamed(t, nodeAt(t, p, "services.web", opts()), "gpus")
	if strings.Join(f.Allowed, ",") != "all" {
		t.Errorf("gpus allowed = %v, want [all] — the string arm's enum, and nothing invented", f.Allowed)
	}
	if f.AllowedSource == "schema" {
		t.Errorf("gpus claims a closed set, but its `oneOf` also permits an array of GPU devices")
	}
	if f.AllowedSource != "schema-branch" {
		t.Errorf("gpus allowed_source = %q, want %q", f.AllowedSource, "schema-branch")
	}
	// And the downgrade is not a blanket one: a key whose single form IS
	// enumerated still says so, or the claim has simply been deleted.
	c := fieldNamed(t, nodeAt(t, p, "services.db.depends_on.web", opts()), "condition")
	if c.AllowedSource != "schema" {
		t.Errorf("depends_on.*.condition allowed_source = %q, want %q — it is a plain enum",
			c.AllowedSource, "schema")
	}
}

// The rule itself, on schemas written for it, because the vendored document
// contains only one shape of the failure and the next schema bump may bring
// another.
//
// Every case has more than one branch: a fixture with a single arm cannot tell
// "every form is enumerated" from "some form is".
func TestClosednessAcrossComposedBranches(t *testing.T) {
	const mins = `{"keys":{}}`
	for _, c := range []struct {
		name   string
		node   string
		want   string
		values string
	}{
		{"a plain enum is closed", `{"type":"string","enum":["a","b"]}`, "schema", "a,b"},
		{"a const is closed", `{"const":"a"}`, "schema", "a"},
		// The union across two enumerated arms is one closed set to a reader,
		// which is why enumValues unions rather than taking the first arm.
		{"every arm enumerated is closed",
			`{"oneOf":[{"type":"string","enum":["a"]},{"type":"string","enum":["b"]}]}`, "schema", "a,b"},
		// `gpus`'s shape.
		{"an enumerated arm beside a free one is not closed",
			`{"oneOf":[{"type":"string","enum":["a"]},{"type":"array","items":{"type":"string"}}]}`,
			"schema-branch", "a"},
		{"anyOf behaves the same as oneOf",
			`{"anyOf":[{"type":"string","enum":["a"]},{"type":"integer"}]}`, "schema-branch", "a"},
		// allOf is an intersection: one enumerated arm closes the whole node,
		// because a value must satisfy every arm.
		{"one enumerated arm of an allOf closes it",
			`{"allOf":[{"type":"string"},{"enum":["a","b"]}]}`, "schema", "a,b"},
		// A value the schema enumerates but this engine refuses to offer —
		// anything that is a shape rather than a value — makes the OFFERED list
		// shorter than the enumeration, so the offered list is not closed either.
		{"a dropped enum member opens the list",
			`{"type":"string","enum":["a","service:[service name]"]}`, "schema-branch", "a"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := `{"properties":{"k":` + c.node + `}}`
			s, err := parse([]byte(doc), []byte(mins), "test")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			node, ok := s.property(s.root, "k")
			if !ok {
				t.Fatal("the fixture has no property k")
			}
			values, source := allowedOf(s, node)
			if strings.Join(values, ",") != c.values {
				t.Errorf("allowed = %v, want [%s]", values, c.values)
			}
			if source != c.want {
				t.Errorf("allowed_source = %q, want %q", source, c.want)
			}
		})
	}
}

// A key with no fixed set carries none. An empty list and a list nobody
// supplied are the same thing on the wire, and offering one value for `image`
// would be an invention.
func TestKeysWithoutAFixedSetCarryNothing(t *testing.T) {
	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())
	for _, key := range []string{"image", "container_name", "working_dir", "hostname", "healthcheck", "ports"} {
		if f := fieldNamed(t, n, key); len(f.Allowed) > 0 {
			t.Errorf("%s offers %v; the specification does not constrain it", key, f.Allowed)
		}
	}
}

// The specification quotes values it PERMITS and it also quotes values it is
// giving as EXAMPLES, in the same punctuation. Reading the second as the first
// is the confident wrong answer this whole engine is built against: `interval`
// would offer `1s · 1m30s` as though those were the only two durations, and
// `logging.driver` would offer three of the dozen drivers that work.
//
// Every key below quotes two or more values in its description and must offer
// none of them. This is the test that fails if the cue that separates "Options
// include:" from "e.g." is ever loosened.
func TestExamplesInProseAreNotOfferedAsValues(t *testing.T) {
	p := mustProject(t, stack)
	for _, c := range []struct{ at, key string }{
		{"services.db.healthcheck", "interval"}, // "(e.g., '1s', '1m30s')"
		{"services.db.healthcheck", "timeout"},
		{"services.db.healthcheck", "start_period"},
		{"services.db", "stop_signal"}, // "(e.g., 'SIGTERM', 'SIGINT')"
		{"services.db", "stop_grace_period"},
		{"services.db.logging", "driver"}, // "such as 'json-file', 'syslog', …, etc."
		{"services.db.deploy.resources.limits", "memory"},
		{"services.db.deploy.restart_policy", "delay"},
	} {
		f := fieldNamed(t, nodeAt(t, p, c.at, opts()), c.key)
		if len(f.Allowed) > 0 {
			t.Errorf("%s.%s offers %v, but the specification is giving examples there, not a list",
				c.at, c.key, f.Allowed)
		}
	}
}

// A `|` inside a group belongs to the group. Splitting on it produces two
// halves of one arm and calls each of them a value — and the specification's
// own `pull_policy` pattern ends in `every_([0-9]+[wdhms])+`, so a grouped
// alternation arriving there is one schema bump away.
func TestAlternationSplitsOnlyAtTheTopLevel(t *testing.T) {
	for _, c := range []struct {
		pattern string
		want    string
	}{
		{"always|never|build", "always,never,build"},
		{"a|(b|c)|d", "a,(b|c),d"},
		{"a|[b|c]|d", "a,[b|c],d"},
		{`a|b\|c|d`, `a,b\|c,d`},
		{"solo", "solo"},
	} {
		if got := strings.Join(splitAlternation(c.pattern), ","); got != c.want {
			t.Errorf("splitAlternation(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// Every value offered anywhere in the document is a value a file could hold:
// no spaces, no `[placeholder]`, no `service:[service name]`. This is the whole
// hazard of reading prose, checked over the whole schema rather than the six
// keys above.
func TestNoOfferedValueIsAPlaceholder(t *testing.T) {
	spec, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Every map in the document, not just the ones a config path reaches: the
	// prose hazard is a property of the schema text, and the six keys checked
	// above cannot speak for the other nine hundred.
	var walk func(any, string, int)
	seen := 0
	walk = func(n any, path string, depth int) {
		if depth > 40 {
			return
		}
		switch node := n.(type) {
		case map[string]any:
			values, src := allowedOf(spec, node)
			for _, v := range values {
				seen++
				if v == "" || strings.ContainsAny(v, " \t[]<>()|`'\"/:") {
					t.Errorf("%s (%s) offers %q, which is prose rather than a value", path, src, v)
				}
			}
			for k, v := range node {
				walk(v, path+"/"+k, depth+1)
			}
		case []any:
			for i, v := range node {
				walk(v, fmt.Sprintf("%s/%d", path, i), depth+1)
			}
		}
	}
	walk(spec.root, "", 0)
	if seen < 20 {
		t.Fatalf("only %d values offered across the whole schema; the walk found nothing to check", seen)
	}
}

// The wire carries them. A field the extension cannot see is a field that does
// not exist.
func TestAllowedValuesReachTheWire(t *testing.T) {
	p := mustProject(t, stack)
	n := nodeAt(t, p, "services.db", opts())
	raw, err := json.Marshal(fieldNamed(t, n, "restart"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	list, ok := back["allowed"].([]any)
	if !ok {
		t.Fatalf("no \"allowed\" on the wire: %s", raw)
	}
	var got []string
	for _, v := range list {
		got = append(got, v.(string))
	}
	if strings.Join(got, ",") != "no,always,on-failure,unless-stopped" {
		t.Errorf("wire allowed = %v", got)
	}
	if back["allowed_source"] != "description" {
		t.Errorf("wire allowed_source = %v", back["allowed_source"])
	}
}
