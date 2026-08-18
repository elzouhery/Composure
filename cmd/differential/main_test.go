package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
)

// The normalisers are the only thing standing between a real divergence and a
// green result. This package had no test file at all, which means the number it
// reported rested on code nobody had ever checked in isolation — and one of
// those normalisers had a comment admitting it could not distinguish two
// different command lines.

// ---- shell form -------------------------------------------------------------

func TestShellTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"nginx -g daemon", []string{"nginx", "-g", "daemon"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		// The case the old implementation could not tell apart, in both
		// spellings. If these two produce the same rendering, the harness
		// cannot see a `command:` that lost its quoting — which turns one
		// argument into two and runs something nobody wrote.
		{`sh -c "a b"`, []string{"sh", "-c", "a b"}},
		{`sh -c a b`, []string{"sh", "-c", "a", "b"}},
		{`sh -c 'a b'`, []string{"sh", "-c", "a b"}},
		{`echo "it's"`, []string{"echo", "it's"}},
		{`echo 'say "hi"'`, []string{"echo", `say "hi"`}},
		{`echo a\ b`, []string{"echo", "a b"}},
		{`echo "a\"b"`, []string{"echo", `a"b`}},
		// A backslash inside double quotes is literal unless it escapes one of
		// the four characters a shell treats specially there.
		{`echo "a\nb"`, []string{"echo", `a\nb`}},
		{"", nil},
		// An empty argument is an argument.
		{`a "" b`, []string{"a", "", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := shellTokens(tc.in)
			if err != nil {
				t.Fatalf("shellTokens(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("shellTokens(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestShellTokensDistinguishesQuoting is the point of the rewrite stated on its
// own: the two forms must not render alike.
func TestShellTokensDistinguishesQuoting(t *testing.T) {
	quoted := shellForm(`sh -c "a b"`)
	bare := shellForm(`sh -c a b`)
	if quoted == bare {
		t.Errorf("`-c \"a b\"` and `-c a b` both render as %q; a command divergence is invisible", quoted)
	}
	// And a quoted argument matches the same argument written as a list
	// element, which is how the oracle always spells it.
	if want := listForm([]string{"sh", "-c", "a b"}); quoted != want {
		t.Errorf("shell form %q does not match the list form %q", quoted, want)
	}
	if want := listForm([]string{"sh", "-c", "a", "b"}); bare != want {
		t.Errorf("shell form %q does not match the list form %q", bare, want)
	}
}

// TestShellFormRefusesUnbalancedQuoting: a string whose quoting never closes is
// not tokenisable, and guessing at it is how a harness reports a divergence
// that is its own fault. It says so instead.
func TestShellFormRefusesUnbalancedQuoting(t *testing.T) {
	for _, in := range []string{`sh -c "unterminated`, `sh -c 'unterminated`} {
		if _, err := shellTokens(in); !errors.Is(err, errUnbalanced) {
			t.Errorf("shellTokens(%q) error = %v, want errUnbalanced", in, err)
		}
		if got := shellForm(in); got != unreadable {
			t.Errorf("shellForm(%q) = %q, want %q", in, got, unreadable)
		}
	}
	// Both sides unreadable is agreement that neither can be read, and it must
	// not read as a divergence.
	if shellForm(`a "b`) != shellForm(`a "b`) {
		t.Error("two identical unreadable command lines compare unequal")
	}
}

// ---- healthcheck.test -------------------------------------------------------

func TestHealthTest(t *testing.T) {
	// A bare string is ONE argument to CMD-SHELL, not a word list. Splitting it
	// was the old behaviour, and it agreed with the oracle only because the
	// oracle's list was being re-joined and re-split to match.
	l := valueOf(t, "healthcheck:\n  test: curl -f http://localhost/health\n")
	got := healthTest(field(field(l, "healthcheck"), "test"))
	want := listForm([]string{"CMD-SHELL", "curl -f http://localhost/health"})
	if got != want {
		t.Errorf("healthTest = %q, want %q", got, want)
	}
	// And it must equal what the oracle emits for exactly that input.
	oracleSide := oracleCommand([]any{"CMD-SHELL", "curl -f http://localhost/health"})
	if got != oracleSide {
		t.Errorf("composure %q != oracle %q for the same healthcheck", got, oracleSide)
	}

	// The list form is taken as written.
	l = valueOf(t, "healthcheck:\n  test: [\"CMD\", \"pg_isready\", \"-U\", \"postgres\"]\n")
	if got, want := healthTest(field(field(l, "healthcheck"), "test")), listForm([]string{"CMD", "pg_isready", "-U", "postgres"}); got != want {
		t.Errorf("healthTest(list) = %q, want %q", got, want)
	}
	if healthTest(nil) != "" {
		t.Error("healthTest(nil) is not empty")
	}
}

func TestCommandTokens(t *testing.T) {
	l := valueOf(t, "command: nginx -g \"daemon off;\"\n")
	if got, want := commandTokens(field(l, "command")), listForm([]string{"nginx", "-g", "daemon off;"}); got != want {
		t.Errorf("commandTokens(string) = %q, want %q", got, want)
	}
	l = valueOf(t, "command: [\"nginx\", \"-g\", \"daemon off;\"]\n")
	if got, want := commandTokens(field(l, "command")), listForm([]string{"nginx", "-g", "daemon off;"}); got != want {
		t.Errorf("commandTokens(list) = %q, want %q", got, want)
	}
	if commandTokens(nil) != "" {
		t.Error("commandTokens(nil) is not empty")
	}
}

func TestOracleCommandUnescapesDollar(t *testing.T) {
	// `docker compose config` emits a document meant to be fed back in, so a
	// literal dollar comes out doubled. Failing to undo it reports every
	// `$$VAR` as a divergence.
	got := oracleCommand([]any{"sh", "-c", "echo $$HOME"})
	want := listForm([]string{"sh", "-c", "echo $HOME"})
	if got != want {
		t.Errorf("oracleCommand = %q, want %q", got, want)
	}
}

// ---- ports ------------------------------------------------------------------

func TestNormalisePortString(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"80", []string{":80/tcp"}},
		{"8080:80", []string{"8080:80/tcp"}},
		{"8080:80/udp", []string{"8080:80/udp"}},
		{"127.0.0.1:8080:80", []string{"8080:80/tcp"}},
		{"[::1]:8080:80", []string{"8080:80/tcp"}},
		{"3000-3005:3000-3005", []string{"3000-3005:3000-3005/tcp"}},
		{"  ", nil},
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalisePortString(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalisePortString(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPortSetMatchesTheOracleShape(t *testing.T) {
	l := valueOf(t, "ports:\n  - \"8080:80\"\n  - target: 443\n    published: \"8443\"\n    protocol: udp\n")
	got := portSet(field(l, "ports"))
	// The same two ports as the oracle spells them.
	oracle := oraclePortSet([]any{
		map[string]any{"target": float64(80), "published": "8080", "protocol": "tcp"},
		map[string]any{"target": float64(443), "published": "8443", "protocol": "udp"},
	})
	if d, ok := diffSets("ports", got, oracle); !ok {
		t.Errorf("the two sides disagree on identical ports: %+v", d)
	}
}

// ---- mounts -----------------------------------------------------------------

func TestMountTargets(t *testing.T) {
	l := valueOf(t, `volumes:
  - data:/var/lib/data
  - ./conf:/etc/nginx/conf.d:ro
  - /var/run/docker.sock:/var/run/docker.sock
  - "C:\\data:/data:ro"
  - .:/code
  - /anonymous
  - /trailing/
  - type: bind
    source: ./src
    target: /app
`)
	got := mountTargets(field(l, "volumes"))
	want := []string{
		"/var/lib/data", "/etc/nginx/conf.d", "/var/run/docker.sock",
		"/data", "/code", "/anonymous", "/trailing", "/app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mountTargets:\n got %v\nwant %v", got, want)
	}
}

func TestNormaliseTarget(t *testing.T) {
	for in, want := range map[string]string{
		"/var/www/html/": "/var/www/html",
		"/var/www/html":  "/var/www/html",
		"/":              "/",
		"":               "",
	} {
		if got := normaliseTarget(in); got != want {
			t.Errorf("normaliseTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- env_file ---------------------------------------------------------------

func TestEnvFileNames(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.env", "FROM_A=1\n# comment\nSHARED=a\n")
	write("b.env", "FROM_B=2\n")

	// Every declared form: a bare string, a list, and the long `{path:}` form.
	for _, src := range []string{
		"env_file: a.env\n",
		"env_file:\n  - a.env\n  - b.env\n",
		"env_file:\n  - path: a.env\n    required: false\n",
	} {
		names := envFileNames(valueOf(t, src), dir)
		if !names["FROM_A"] {
			t.Errorf("%q: FROM_A not found; the oracle's folded-in variables would read as merge losses", src)
		}
	}

	names := envFileNames(valueOf(t, "env_file:\n  - a.env\n  - b.env\n"), dir)
	var got []string
	for k := range names {
		got = append(got, k)
	}
	sort.Strings(got)
	if want := []string{"FROM_A", "FROM_B", "SHARED"}; !reflect.DeepEqual(got, want) {
		t.Errorf("envFileNames = %v, want %v", got, want)
	}

	// A file that is not there is not an error: `env_file` with `required:
	// false` is a real shape, and the oracle supplies nothing for it either.
	if n := envFileNames(valueOf(t, "env_file: missing.env\n"), dir); len(n) != 0 {
		t.Errorf("a missing env_file yielded %v", n)
	}
	if n := envFileNames(valueOf(t, "image: x\n"), dir); len(n) != 0 {
		t.Errorf("a service with no env_file yielded %v", n)
	}
}

// ---- set comparison ---------------------------------------------------------

// TestDiffSetsComparesMultiplicity is the repair of the harness's most
// load-bearing bug. Both sides were deduplicated before comparing, so an
// override that DUPLICATED a port or a volume instead of merging it produced a
// set identical to the correct one and the harness reported pass — which is
// precisely the failure `appendUnique` has when a merge rule is missing.
func TestDiffSetsComparesMultiplicity(t *testing.T) {
	composure := []string{"8080:80/tcp", "8080:80/tcp", "8443:443/tcp"}
	compose := []string{"8080:80/tcp", "8443:443/tcp"}
	d, ok := diffSets("services.web.ports", composure, compose)
	if ok {
		t.Fatal("a duplicated port compared equal to the merged one")
	}
	if !strings.Contains(d.Composure, "x2") {
		t.Errorf("the divergence does not say the entry is repeated: %+v", d)
	}

	// Order is still not significant, and genuinely equal lists still pass.
	if _, ok := diffSets("p", []string{"b", "a"}, []string{"a", "b"}); !ok {
		t.Error("two equal lists in different orders compared unequal")
	}
	if _, ok := diffSets("p", []string{"a", "a"}, []string{"a", "a"}); !ok {
		t.Error("two equally-duplicated lists compared unequal")
	}
	if _, ok := diffSets("p", nil, nil); !ok {
		t.Error("two empty lists compared unequal")
	}
	// An empty string is a normaliser with nothing to say, not a member.
	if _, ok := diffSets("p", []string{"a", ""}, []string{"a"}); !ok {
		t.Error("an empty entry was counted as a list member")
	}
}

func TestNormSetStillDeduplicates(t *testing.T) {
	// Top-level resource names are mapping keys and cannot repeat, so a set is
	// the right model there and normSet must keep behaving like one.
	if got, want := normSet([]string{"b", "a", "b", " "}), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("normSet = %v, want %v", got, want)
	}
}

func TestRenderAnnotatesRepeats(t *testing.T) {
	if got, want := render([]string{"a", "a", "b"}), "a (x2),b"; got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
	if got := render(nil); got != "" {
		t.Errorf("render(nil) = %q", got)
	}
}

func TestHostEntry(t *testing.T) {
	// Compose emits `name=ip`; a file may write either spelling.
	for in, want := range map[string]string{
		"gateway:10.0.0.1":                  "gateway=10.0.0.1",
		"gateway=10.0.0.1":                  "gateway=10.0.0.1",
		"host.docker.internal:host-gateway": "host.docker.internal=host-gateway",
	} {
		if got := hostEntry(in); got != want {
			t.Errorf("hostEntry(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestListAttributesCoverTheDeletedMergeRows pins the projection itself.
//
// A projection is only as good as its list, and a list is the easiest thing in
// a harness to shorten by accident: dropping a field removes divergences and
// RAISES the pass rate, so nothing complains. These particular fields are the
// ones commit 89fa598 deleted from the merge table on the grounds that §13 says
// sequences append. Whether Compose actually appends a repeat is the question
// the multi-file fixtures put to the oracle, and it can only be asked for a
// field that is in this list.
func TestListAttributesCoverTheDeletedMergeRows(t *testing.T) {
	have := map[string]bool{}
	for _, f := range listAttributes {
		if have[f] {
			t.Errorf("%q is listed twice", f)
		}
		have[f] = true
	}
	for _, f := range []string{
		"cap_add", "cap_drop", "dns", "dns_opt", "dns_search",
		"expose", "tmpfs", "links", "security_opt", "group_add", "volumes_from",
	} {
		if !have[f] {
			t.Errorf("%q was deleted from the merge table and is not compared here, "+
				"so the divergence it can cause is invisible", f)
		}
	}
	// extra_hosts kept its rule; it is compared to hold the rule, not to
	// question it.
	if !have["extra_hosts"] {
		t.Error("extra_hosts is not compared")
	}
}

// TestCompareServiceProjection drives the projection itself against a
// hand-written oracle document, so the wiring is falsifiable without a Docker
// daemon. A field that is in listAttributes but never reaches diffSets is a
// field that cannot produce a divergence, and the only symptom is a pass rate
// that goes up.
func TestCompareServiceProjection(t *testing.T) {
	l := valueOf(t, `image: nginx:1.25
command: ["nginx", "-g", "daemon off;"]
environment:
  LOG_LEVEL: debug
ports:
  - "8080:80"
volumes:
  - data:/var/lib/data
cap_add: [NET_ADMIN, NET_ADMIN]
expose: ["3000", "3000"]
dns: ["1.1.1.1"]
`)
	oracle := map[string]any{
		"image":       "nginx:1.25",
		"command":     []any{"nginx", "-g", "daemon off;"},
		"environment": map[string]any{"LOG_LEVEL": "debug"},
		"ports": []any{map[string]any{
			"target": float64(80), "published": "8080", "protocol": "tcp",
		}},
		"volumes": []any{map[string]any{"target": "/var/lib/data"}},
		// Compose deduplicates these; the model above does not.
		"cap_add": []any{"NET_ADMIN"},
		"expose":  []any{"3000"},
		"dns":     []any{"1.1.1.1"},
	}

	got := map[string]divergence{}
	for _, d := range compareService("services.s", l, oracle, t.TempDir()) {
		got[d.Path] = d
	}
	for _, want := range []string{"services.s.cap_add", "services.s.expose"} {
		if _, ok := got[want]; !ok {
			t.Errorf("a duplicated %s was not reported; the field is not reaching the comparison", want)
		}
	}
	// Everything the two sides agree on must stay silent, or the harness
	// reports noise and a real divergence is lost in it.
	for _, quiet := range []string{
		"services.s.image", "services.s.command", "services.s.environment",
		"services.s.ports", "services.s.volumes", "services.s.dns",
	} {
		if d, ok := got[quiet]; ok {
			t.Errorf("%s was reported as a divergence though the two sides agree: %+v", quiet, d)
		}
	}

	// And the other half of the same assertion: a field that agrees is silent
	// only because it agrees, not because nothing is looking at it. Every row
	// below is made to disagree and must be reported.
	disagree := map[string]any{
		"image":       "nginx:1.24",
		"command":     []any{"nginx"},
		"entrypoint":  []any{"/entry.sh"},
		"environment": map[string]any{"LOG_LEVEL": "info"},
		"labels":      map[string]any{"a": "b"},
		"ports": []any{map[string]any{
			"target": float64(80), "published": "9090", "protocol": "tcp",
		}},
		"volumes":    []any{map[string]any{"target": "/elsewhere"}},
		"depends_on": map[string]any{"other": map[string]any{"condition": "service_started"}},
		"profiles":   []any{"prod"},
		"dns":        []any{"9.9.9.9"},
	}
	got = map[string]divergence{}
	for _, d := range compareService("services.s", l, disagree, t.TempDir()) {
		got[d.Path] = d
	}
	for _, want := range []string{
		"services.s.image", "services.s.command", "services.s.entrypoint",
		"services.s.environment", "services.s.labels", "services.s.ports",
		"services.s.volumes", "services.s.depends_on", "services.s.profiles",
		"services.s.dns",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s differs between the two sides and was NOT reported: the field is not being compared", want)
		}
	}
}

// ---- project discovery ------------------------------------------------------

// TestFixturesAreMultiFile is the guard on the whole point of this harness. If
// the fixtures stop being multi-file — renamed, emptied, a CHAIN file deleted —
// every comparison silently becomes one file against itself again, and the pass
// rate goes UP while measuring less.
func TestFixturesAreMultiFile(t *testing.T) {
	projects, err := collectFixtures(fixtureRoot(t))
	if err != nil {
		t.Fatalf("collectFixtures: %v", err)
	}
	if len(projects) < 5 {
		t.Fatalf("found %d fixture projects, want at least 5", len(projects))
	}
	var chains, pickups int
	for _, p := range projects {
		if !p.MultiFile() {
			t.Errorf("fixture %s merges nothing: files=%v pickup=%v", p.Name, p.Files, p.Pickup)
		}
		if len(p.Files) > 0 {
			chains++
		} else {
			pickups++
		}
	}
	// Both discovery paths must be exercised: an explicit -f chain and the
	// automatic override pickup are different code in resolve.Load and
	// different arguments to the oracle.
	if chains == 0 {
		t.Error("no fixture uses an explicit -f chain")
	}
	if pickups == 0 {
		t.Error("no fixture uses the automatic override pickup")
	}
}

// TestFixturesResolve: every fixture must resolve through resolve.Load, and
// each must actually merge — a chain whose second file changed nothing would
// pass the differential without proving anything.
func TestFixturesResolve(t *testing.T) {
	projects, err := collectFixtures(fixtureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		t.Run(filepath.Base(p.Name), func(t *testing.T) {
			proj, err := p.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if proj.Services().Len() == 0 {
				t.Fatal("resolved to no services")
			}
			// Provenance from more than one file is what "a merge happened"
			// looks like from the inside (R1.8), and it is the only check here
			// that a second file was read at all rather than silently skipped.
			if n := len(proj.Files()); n < 2 {
				t.Errorf("resolved from %d source files, want at least 2", n)
			}
		})
	}
}

func TestReadChain(t *testing.T) {
	dir := t.TempDir()
	if got, err := readChain(dir); err != nil || got != nil {
		t.Errorf("a directory with no CHAIN = %v, %v; want nil, nil", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, chainFile), []byte("# a note\nbase.yaml\n\nprod.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readChain(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "base.yaml"), filepath.Join(dir, "prod.yaml")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readChain = %v, want %v", got, want)
	}

	// A CHAIN naming nothing is a fixture that would silently compare one file
	// against itself. It is refused instead.
	if err := os.WriteFile(filepath.Join(dir, chainFile), []byte("# only a comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readChain(dir); err == nil {
		t.Error("an empty CHAIN was accepted")
	}
}

func TestPickupFiles(t *testing.T) {
	dir := t.TempDir()
	if got := pickupFiles(dir); len(got) != 0 {
		t.Errorf("an empty directory yielded %v", got)
	}
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch("docker-compose.yml")
	if got := pickupFiles(dir); len(got) != 1 {
		t.Errorf("a lone compose file yielded %v, want one entry", got)
	}
	touch("docker-compose.override.yml")
	got := pickupFiles(dir)
	if len(got) != 2 || filepath.Base(got[0]) != "docker-compose.yml" ||
		filepath.Base(got[1]) != "docker-compose.override.yml" {
		t.Errorf("pickupFiles = %v, want base then override", got)
	}

	// The base filename decides which override is picked, and compose.yaml
	// wins over docker-compose.yml.
	touch("compose.yaml")
	touch("compose.override.yaml")
	got = pickupFiles(dir)
	if len(got) != 2 || filepath.Base(got[0]) != "compose.yaml" ||
		filepath.Base(got[1]) != "compose.override.yaml" {
		t.Errorf("pickupFiles = %v, want compose.yaml then its override", got)
	}
}

// TestProjectMultiFile: a single-file project must not be counted toward the
// multi-file total. That number is the one the whole report rests on.
func TestProjectMultiFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  a: {image: x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if (project{Dir: dir, Files: []string{filepath.Join(dir, "compose.yaml")}}).MultiFile() {
		t.Error("a one-file chain counted as multi-file")
	}
	if (project{Dir: dir, Pickup: true}).MultiFile() {
		t.Error("a directory with no override counted as multi-file")
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.override.yaml"), []byte("services:\n  a: {image: y}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !(project{Dir: dir, Pickup: true}).MultiFile() {
		t.Error("a directory with an override did not count as multi-file")
	}
	if !(project{Dir: dir, Files: []string{"a", "b"}}).MultiFile() {
		t.Error("a two-file chain did not count as multi-file")
	}
}

// TestDiscoverKeepsFixturesUnderALimit: -limit trims the corpus and must never
// trim the fixtures, or the fast run and the full run measure different things
// while printing the same headline.
func TestDiscoverKeepsFixturesUnderALimit(t *testing.T) {
	root := t.TempDir() // an empty corpus
	projects, err := discover(root, fixtureRoot(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	var multi int
	for _, p := range projects {
		if p.MultiFile() {
			multi++
		}
	}
	if multi < 5 {
		t.Errorf("-limit 1 left %d multi-file projects, want every fixture kept", multi)
	}
}

// TestDiscoverRefusesAnEmptyFixtureDirectory: silently finding no fixtures is
// how this harness goes back to comparing files against themselves.
func TestDiscoverRefusesAnEmptyFixtureDirectory(t *testing.T) {
	if _, err := discover(t.TempDir(), t.TempDir(), 0); err == nil {
		t.Error("an empty fixture directory was accepted")
	}
}

// ---- the oracle command line ------------------------------------------------

// TestOracleArgsPassesTheWholeChain: the oracle must be given every file, in
// order. Handing it one -f while resolving three on this side is how a merge
// gets compared against a single file and reported as agreement — the exact
// defect this harness is being repaired from.
func TestOracleArgsPassesTheWholeChain(t *testing.T) {
	dir := t.TempDir()
	chain := []string{
		filepath.Join(dir, "base.yaml"),
		filepath.Join(dir, "prod.yaml"),
		filepath.Join(dir, "base.yaml"), // a repeat is legal and must survive
	}
	args, wd, err := oracleArgs([]string{"/usr/bin/docker", "compose"}, project{Dir: dir, Files: chain}, []string{"debug", "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if wd != dir {
		t.Errorf("working directory = %q, want %q", wd, dir)
	}

	var files []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			files = append(files, args[i+1])
			if !filepath.IsAbs(args[i+1]) {
				t.Errorf("-f %q is not absolute", args[i+1])
			}
		}
	}
	if !reflect.DeepEqual(files, chain) {
		t.Errorf("-f chain = %v, want %v in order, repeat included", files, chain)
	}

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"compose", "--project-directory " + dir, "--profile debug", "--profile ci",
		"config --format json",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %v are missing %q", args, want)
		}
	}
	// --project-directory must precede the files: it is what `.env` and every
	// relative path resolve against.
	if strings.Index(joined, "--project-directory") > strings.Index(joined, "-f ") {
		t.Error("--project-directory comes after the file chain")
	}
}

// TestOracleArgsForADirectoryPickupPassesNoFiles: when the chain is discovered
// rather than given, Compose must do its own pickup. Supplying the files would
// make the harness hand both sides an answer instead of comparing them.
func TestOracleArgsForADirectoryPickupPassesNoFiles(t *testing.T) {
	dir := t.TempDir()
	args, _, err := oracleArgs([]string{"/usr/bin/docker", "compose"}, project{Dir: dir, Pickup: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if a == "-f" {
			t.Errorf("a directory-pickup project passed -f: %v", args)
		}
	}
}

// ---- report -----------------------------------------------------------------

func TestReportCountsMultiFileSeparately(t *testing.T) {
	var r report
	r.add(outcome{File: "single", Status: "pass"})
	r.add(outcome{File: "merge-ok", Status: "pass", MultiFile: true})
	r.add(outcome{File: "merge-bad", Status: "diverged", MultiFile: true})
	r.add(outcome{File: "v1", Status: "both-refused"})
	r.finish()

	if r.Compared != 3 || r.Passed != 2 || r.Diverged != 1 {
		t.Errorf("totals = %+v", r)
	}
	if r.MultiFile != 2 || r.MultiFilePassed != 1 {
		t.Errorf("multi-file totals = %d compared, %d passed; want 2, 1", r.MultiFile, r.MultiFilePassed)
	}
	if r.MultiFilePct != 50 {
		t.Errorf("multi-file pass rate = %v, want 50", r.MultiFilePct)
	}
	// The headline is 66.67% while the merge is at 50%: the two numbers are
	// different questions, and reporting only the first is what made the
	// original result mean less than it said.
	if r.PassPct == r.MultiFilePct {
		t.Error("the overall and multi-file rates are being computed from the same set")
	}
}

// ---- helpers ----------------------------------------------------------------

// valueOf resolves a fragment as a service body and hands back its value.
func valueOf(t *testing.T, body string) *resolve.Value {
	t.Helper()
	var b strings.Builder
	b.WriteString("services:\n  s:\n")
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	p, err := resolve.Bytes("frag.yaml", []byte(b.String()))
	if err != nil {
		t.Fatalf("resolve %q: %v", b.String(), err)
	}
	v, ok := p.At(resolve.Path{"services", "s"})
	if !ok {
		t.Fatal("no service in the fragment")
	}
	return v
}

// fixtureRoot finds testdata/differential from the package directory.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", defaultFixtures)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixtures missing at %s: %v", root, err)
	}
	return root
}

// ---------------------------------------------------------------------------
// The expected-divergence register.
//
// Every fixture here paired MATCHING forms, so the 100% pass rate was real and
// blind to the one divergence this project can actually demonstrate: a key
// written as a list in one file and a mapping in the other, where Compose
// normalises and keeps both entries and composure drops the earlier ones. The
// register is how that is asserted to STILL HAPPEN without leaving the harness
// permanently red — and how it is kept from reading as agreement.

func TestReadDiverges(t *testing.T) {
	dir := t.TempDir()
	if got, err := readDiverges(dir); err != nil || got != nil {
		t.Errorf("a directory with no %s = %v, %v; want nil, nil", divergesFile, got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, divergesFile),
		[]byte("# why\nservices.web.environment\n\nservices.db.labels\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readDiverges(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"services.db.labels", "services.web.environment"}) {
		t.Errorf("readDiverges = %v", got)
	}

	// A register that names nothing documents nothing and asserts nothing,
	// while reading as "known". Refused.
	if err := os.WriteFile(filepath.Join(dir, divergesFile), []byte("# only prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDiverges(dir); err == nil {
		t.Errorf("an empty %s was accepted", divergesFile)
	}
}

// The judgement, in all four shapes. Equality in both directions is the whole
// contract: a register that only required containment would let a real
// regression hide behind a documented divergence.
func TestJudgeRegistered(t *testing.T) {
	div := func(paths ...string) []divergence {
		out := make([]divergence, 0, len(paths))
		for _, p := range paths {
			out = append(out, divergence{Path: p, Composure: "a", Compose: "b"})
		}
		return out
	}
	cases := []struct {
		name       string
		expected   []string
		got        []divergence
		wantStatus string
		wantDetail string
	}{
		{"exactly as registered", []string{"services.web.environment"},
			div("services.web.environment"), "documented-divergence", ""},
		{"it now agrees", []string{"services.web.environment"},
			nil, "divergence-changed", "now AGREES"},
		{"a new divergence as well", []string{"services.web.environment"},
			div("services.web.environment", "services.web.ports"), "divergence-changed", "NOT in"},
		{"a different divergence entirely", []string{"services.web.environment"},
			div("services.web.ports"), "divergence-changed", "no longer diverges"},
	}
	for _, c := range cases {
		status, detail := judgeRegistered(c.expected, c.got)
		if status != c.wantStatus {
			t.Errorf("%s: status = %q, want %q", c.name, status, c.wantStatus)
		}
		if c.wantDetail != "" && !strings.Contains(detail, c.wantDetail) {
			t.Errorf("%s: detail = %q, want it to mention %q", c.name, detail, c.wantDetail)
		}
	}
}

// A registered divergence is never counted as agreement — not in the pass rate,
// and not in the multi-file figure either. Reporting it as a failed merge would
// be as misleading as reporting it as a pass.
func TestDocumentedDivergenceIsNeitherPassNorMergeFailure(t *testing.T) {
	var r report
	r.add(outcome{File: "clean", MultiFile: true, Status: "pass"})
	r.add(outcome{File: "registered", MultiFile: true, Status: "documented-divergence",
		Expected: []string{"services.web.environment"}})
	r.finish()

	if r.Compared != 1 || r.Passed != 1 {
		t.Errorf("compared=%d passed=%d, want the documented divergence excluded from both", r.Compared, r.Passed)
	}
	if r.PassPct != 100 {
		t.Errorf("pass rate = %.2f, want 100 over the one project that was actually compared", r.PassPct)
	}
	if r.MultiFile != 1 || r.MultiFilePct != 100 {
		t.Errorf("multi-file = %d at %.2f%%, want the documented divergence out of the merge figure too",
			r.MultiFile, r.MultiFilePct)
	}
	if r.Documented != 1 || r.DocumentedMultiFile != 1 {
		t.Errorf("documented=%d (%d merges), want it counted in its own bucket", r.Documented, r.DocumentedMultiFile)
	}
}

// The cross-form fixtures exist, in both directions, and are registered. They
// are the only thing in this repository that demonstrates the divergence, so
// deleting one has to break a test rather than raise the pass rate.
func TestCrossFormFixturesAreRegistered(t *testing.T) {
	projects, err := collectFixtures(fixtureRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, p := range projects {
		base := filepath.Base(p.Name)
		if !strings.HasPrefix(base, "cross-form-environment") {
			continue
		}
		found[base] = true
		if len(p.Diverges) == 0 {
			t.Errorf("%s has no %s: it would report as a plain divergence and read as a regression", base, divergesFile)
		}
		if !p.MultiFile() {
			t.Errorf("%s merges nothing, so it cannot demonstrate a merge divergence", base)
		}
	}
	for _, want := range []string{"cross-form-environment", "cross-form-environment-reversed"} {
		if !found[want] {
			t.Errorf("fixture %s is missing; the only divergence this project can demonstrate is unrepresented", want)
		}
	}
}
