package resolve

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Story 1.4 — one test per row of the merge table, written from compose-spec
// §13 rather than from the implementation. A row and its test are meant to be
// read side by side; if the table drifts from the spec, one of these fails.

// spec13Attributes are the SEVEN attributes compose-spec §13 actually names —
// three replaced, four unique-resource. Only a row for one of these may carry a
// "§13:" citation; everything else in the table is Compose behaviour measured
// against the CLI, and must say which evidence establishes it.
var spec13Attributes = map[string]bool{
	"services.*.command":          true,
	"services.*.entrypoint":       true,
	"services.*.healthcheck.test": true,
	"services.*.ports":            true,
	"services.*.volumes":          true,
	"services.*.secrets":          true,
	"services.*.configs":          true,
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// merge is the two-file case: base plus override, in that order.
func merge(t *testing.T, base, override string) *Project {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "compose.yaml", base)
	write(t, dir, "compose.override.yaml", override)
	p, err := Load(Options{Dir: dir, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return p
}

func scalarAt(t *testing.T, p *Project, path string) string {
	t.Helper()
	v, ok := p.At(ParsePath(path))
	if !ok {
		t.Fatalf("%s did not resolve", path)
	}
	return v.Scalar()
}

func seqAt(t *testing.T, p *Project, path string) []string {
	t.Helper()
	v, ok := p.At(ParsePath(path))
	if !ok {
		t.Fatalf("%s did not resolve", path)
	}
	if v.Kind() != KindSequence {
		t.Fatalf("%s is %s, want a sequence", path, v.Kind())
	}
	var out []string
	for _, e := range v.Seq() {
		if e.Kind() == KindMapping {
			var parts []string
			for _, k := range e.Map().Keys() {
				c, _ := e.Map().Get(k)
				parts = append(parts, k+"="+c.Scalar())
			}
			out = append(out, "{"+strings.Join(parts, ",")+"}")
			continue
		}
		out = append(out, e.Scalar())
	}
	return out
}

func keysAt(t *testing.T, p *Project, path string) []string {
	t.Helper()
	v, ok := p.At(ParsePath(path))
	if !ok {
		t.Fatalf("%s did not resolve", path)
	}
	return v.Map().Keys()
}

// §13, the default: mappings merge key-wise.
func TestMergeMappingsKeyWise(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      A: 1\n      B: 2\n",
		"services:\n  web:\n    environment:\n      B: two\n      C: 3\n")
	if got := scalarAt(t, p, "services.web.environment.A"); got != "1" {
		t.Errorf("A = %q; a key only the base sets must survive", got)
	}
	if got := scalarAt(t, p, "services.web.environment.B"); got != "two" {
		t.Errorf("B = %q, want the override's value", got)
	}
	if got := scalarAt(t, p, "services.web.environment.C"); got != "3" {
		t.Errorf("C = %q; a key only the override sets must appear", got)
	}
	if got := scalarAt(t, p, "services.web.image"); got != "nginx" {
		t.Errorf("image = %q; an untouched key must not move", got)
	}
	// Key ORDER is the base's, with new keys appended. A merge that reordered
	// the file's keys would make the resolved view unreadable against the file.
	if got := strings.Join(keysAt(t, p, "services.web.environment"), ","); got != "A,B,C" {
		t.Errorf("key order = %s, want A,B,C", got)
	}
}

// §13, the default: sequences append.
//
// This used to merge `dns_search`, which now carries a dedupe row measured
// against the CLI and so no longer exercises the default at all. It merges an
// unlisted key instead — the only way to test the default rule is to pick a
// path the table does not name.
func TestMergeSequencesAppend(t *testing.T) {
	const path = "services.web.some_unlisted_sequence"
	if got := ruleFor(ParsePath(path)).how; got != behaviourDefault {
		t.Fatalf("%s takes the %s rule; this test must exercise the DEFAULT one", path, got)
	}
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    some_unlisted_sequence:\n      - a.example\n",
		"services:\n  web:\n    some_unlisted_sequence:\n      - b.example\n")
	if got := strings.Join(seqAt(t, p, path), ","); got != "a.example,b.example" {
		t.Errorf("sequence = %s, want both entries in order", got)
	}
}

// §13, replaced not appended. The three keys every naive deep merge gets wrong.
func TestMergeReplacesCommandEntrypointAndHealthcheckTest(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    command: ["a", "b"]
    entrypoint: ["/base"]
    healthcheck:
      test: ["CMD", "base"]
      interval: 10s
`, `
services:
  web:
    command: ["c"]
    entrypoint: ["/override"]
    healthcheck:
      test: ["CMD", "override"]
`)
	if got := strings.Join(seqAt(t, p, "services.web.command"), ","); got != "c" {
		t.Errorf("command = %s, want only the override's — appending produces a command line nobody wrote", got)
	}
	if got := strings.Join(seqAt(t, p, "services.web.entrypoint"), ","); got != "/override" {
		t.Errorf("entrypoint = %s", got)
	}
	if got := strings.Join(seqAt(t, p, "services.web.healthcheck.test"), ","); got != "CMD,override" {
		t.Errorf("healthcheck.test = %s", got)
	}
	// The rest of `healthcheck` still merges key-wise: only `test` is replaced.
	if got := scalarAt(t, p, "services.web.healthcheck.interval"); got != "10s" {
		t.Errorf("healthcheck.interval = %q; only .test is replaced", got)
	}
}

// §13, unique resource: ports merge on published/target/protocol.
func TestMergePortsOnTheirUniquenessKey(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    ports:
      - "8080:80"
      - "8443:443"
`, `
services:
  web:
    ports:
      - "8080:80/tcp"
      - "9000:9000"
`)
	got := seqAt(t, p, "services.web.ports")
	// `8080:80` and `8080:80/tcp` are the same publication — tcp is the
	// default protocol — so the override replaces rather than duplicates.
	want := []string{"8080:80/tcp", "8443:443", "9000:9000"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ports = %v, want %v", got, want)
	}
}

// A DIFFERENT host port for the same container port is a different
// publication, and collapsing the two would silently delete one.
func TestMergePortsKeepsDistinctPublications(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    ports:\n      - \"8080:80\"\n",
		"services:\n  web:\n    ports:\n      - \"9090:80\"\n")
	if got := seqAt(t, p, "services.web.ports"); len(got) != 2 {
		t.Errorf("ports = %v, want both publications", got)
	}
}

// §13, unique resource: volumes merge on the container path.
func TestMergeVolumesOnTarget(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    volumes:
      - ./base:/data
      - shared:/shared
`, `
services:
  web:
    volumes:
      - ./override:/data
      - ./extra:/extra
`)
	got := seqAt(t, p, "services.web.volumes")
	want := []string{"./override:/data", "shared:/shared", "./extra:/extra"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("volumes = %v, want %v", got, want)
	}
}

// A Windows source keeps its drive letter rather than being read as a mode.
func TestMergeVolumesToleratesADriveLetter(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    volumes:\n      - 'C:\\data:/data'\n",
		"services:\n  web:\n    volumes:\n      - 'D:\\other:/data'\n")
	if got := seqAt(t, p, "services.web.volumes"); len(got) != 1 || !strings.HasPrefix(got[0], "D:") {
		t.Errorf("volumes = %v, want the override's single mount at /data", got)
	}
}

// §13, unique resource: secrets and configs are unique on TARGET, not source.
//
// The version of this test that shipped was written from the implementation
// rather than from the spec, and locked the divergence in: it asserted that two
// secrets from ONE source mounted at TWO paths collapse into one. That is a
// mount silently deleted. The spec's uniqueness table is volumes → target,
// secrets → target, configs → target, ports → {ip, target, published,
// protocol}, and this test now states it.
func TestMergeSecretsAndConfigsOnTarget(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    secrets:
      - source: db-password
        target: /etc/db
      - source: cert
        target: /etc/tls
    configs:
      - nginx-conf
secrets:
  db-password:
    file: ./db.txt
  cert:
    file: ./cert.pem
  rotated-cert:
    file: ./cert2.pem
configs:
  nginx-conf:
    file: ./nginx.conf
`, `
services:
  web:
    secrets:
      - source: db-password
        target: /etc/db-copy
      - source: rotated-cert
        target: /etc/tls
      - source: api-key
    configs:
      - nginx-conf
      - extra-conf
`)
	secrets := seqAt(t, p, "services.web.secrets")

	// SAME SOURCE, TWO TARGETS: two mounts. Keying on source deleted one.
	if len(secrets) != 4 {
		t.Fatalf("secrets = %v, want four mounts: /etc/db, /etc/tls, /etc/db-copy and the api-key default", secrets)
	}
	if got := scalarAt(t, p, "services.web.secrets[0].target"); got != "/etc/db" {
		t.Errorf("secrets[0].target = %q; the base mount at /etc/db must survive a second mount of the same source", got)
	}
	if got := scalarAt(t, p, "services.web.secrets[2].target"); got != "/etc/db-copy" {
		t.Errorf("secrets[2].target = %q, want the override's second mount of db-password", got)
	}

	// TWO SOURCES, ONE TARGET: one mount, the later source winning. Appending
	// leaves a container told to write two files to one path.
	if got := scalarAt(t, p, "services.web.secrets[1].source"); got != "rotated-cert" {
		t.Errorf("secrets[1].source = %q; a second declaration at /etc/tls replaces the first", got)
	}
	if got := scalarAt(t, p, "services.web.secrets[1].target"); got != "/etc/tls" {
		t.Errorf("secrets[1].target = %q", got)
	}

	// The short form has no target and mounts at the default path, so it is a
	// distinct mount and appends.
	if got := scalarAt(t, p, "services.web.secrets[3].source"); got != "api-key" {
		t.Errorf("secrets[3].source = %q, want api-key appended", got)
	}

	// Configs: the short form's identity is its default target `/<source>`, so a
	// repeated name is one mount and a new name is a second.
	if got := seqAt(t, p, "services.web.configs"); strings.Join(got, ",") != "nginx-conf,extra-conf" {
		t.Errorf("configs = %v; a repeated name must not appear twice", got)
	}
}

// A short form and a long form naming the same effective mount are ONE mount.
// `- cert` mounts at /run/secrets/cert, and so does `{source: cert}` with no
// target, so an override that spells it long must change the mount rather than
// add a second one beside it.
func TestMergeSecretShortAndLongFormMeetAtTheDefaultTarget(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    secrets:
      - cert
secrets:
  cert:
    file: ./cert.pem
`, `
services:
  web:
    secrets:
      - source: cert
        mode: 0400
`)
	got := seqAt(t, p, "services.web.secrets")
	if len(got) != 1 {
		t.Fatalf("secrets = %v, want one mount at /run/secrets/cert", got)
	}
	if v := scalarAt(t, p, "services.web.secrets[0].mode"); v != "0400" {
		t.Errorf("mode = %q, want the override's", v)
	}
}

// The list form of environment merges on the variable name, not by appending.
func TestMergeEnvironmentListOnVariableName(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      - A=1\n      - B=2\n",
		"services:\n  web:\n    environment:\n      - B=two\n      - C=3\n")
	got := seqAt(t, p, "services.web.environment")
	want := []string{"A=1", "B=two", "C=3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("environment = %v, want %v", got, want)
	}
}

// The list-shaped attributes Compose DEDUPLICATES across files.
//
// Every expectation below is the oracle's, not the implementation's. §13 says
// nothing about any of these keys, so the only authority is what
// `docker compose config` returns for a project that declares the same entry
// twice — testdata/differential/dedupe-rows, which is the fixture that caught
// the deletion of these rows and reported them as divergences:
//
//	services.a.profiles   composure="p1 (x2),p2"       compose="p1,p2"
//	services.a.expose     composure="3000 (x2),3001"   compose="3000,3001"
//	services.a.dns        composure="1.1.1.1 (x2),8.8.8.8" compose="1.1.1.1,8.8.8.8"
//
// A base entry the override repeats must appear ONCE, and the ordering is the
// oracle's too: base `[A_ONE, B_TWO]` + override `[C_THREE, A_ONE]` comes back
// from Compose as `A_ONE, B_TWO, C_THREE`, so the first occurrence keeps its
// position and only genuinely new entries land at the end.
func TestMergeDeduplicatesWhatTheComposeCLIDeduplicates(t *testing.T) {
	for _, c := range []struct{ key, base, over, want string }{
		{"cap_add", "[NET_ADMIN, SYS_TIME]", "[SYS_PTRACE, NET_ADMIN]", "NET_ADMIN,SYS_TIME,SYS_PTRACE"},
		{"cap_drop", "[MKNOD]", "[MKNOD, SETUID]", "MKNOD,SETUID"},
		{"dns", `["1.1.1.1"]`, `["1.1.1.1", "8.8.8.8"]`, "1.1.1.1,8.8.8.8"},
		{"dns_opt", `["use-vc"]`, `["use-vc", "no-tld-query"]`, "use-vc,no-tld-query"},
		{"dns_search", `["example.com"]`, `["example.com", "other.com"]`, "example.com,other.com"},
		{"expose", `["3000"]`, `["3000", "3001"]`, "3000,3001"},
		{"tmpfs", `["/tmp"]`, `["/tmp", "/run"]`, "/tmp,/run"},
		{"links", `["b"]`, `["b"]`, "b"},
		{"profiles", `["p1"]`, `["p1", "p2"]`, "p1,p2"},
	} {
		t.Run(c.key, func(t *testing.T) {
			p := merge(t,
				"services:\n  web:\n    image: nginx\n    "+c.key+": "+c.base+"\n",
				"services:\n  web:\n    "+c.key+": "+c.over+"\n")
			got := strings.Join(seqAt(t, p, "services.web."+c.key), ",")
			if got != c.want {
				t.Errorf("%s = %q, want %q: `docker compose config` returns the repeated entry once, "+
					"in the base's position", c.key, got, c.want)
			}
		})
	}
}

// build.tags is in the same group and lives under a nested path, so it also
// proves the pattern reaches past `services.*.<key>`.
func TestMergeBuildTagsDeduplicate(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    build:\n      context: .\n      tags: [\"x:1\"]\n",
		"services:\n  web:\n    build:\n      tags: [\"x:1\", \"y:2\"]\n")
	if got := strings.Join(seqAt(t, p, "services.web.build.tags"), ","); got != "x:1,y:2" {
		t.Errorf("build.tags = %q, want \"x:1,y:2\": the oracle returns the repeated tag once", got)
	}
}

// links is deduplicated on the WHOLE entry, and `b` is not `b:alias`.
//
// The oracle settles it: `docker compose config` on base `links: [b]` +
// override `links: [b:alias]` returns BOTH, because the alias is part of what
// was asked for. Keying links on the service name would delete one of them.
func TestMergeLinksKeepsAnAliasedLinkBesideThePlainOne(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    links: [\"b\"]\n  b:\n    image: redis\n",
		"services:\n  web:\n    links: [\"b:alias\"]\n")
	if got := strings.Join(seqAt(t, p, "services.web.links"), ","); got != "b,b:alias" {
		t.Errorf("links = %q, want \"b,b:alias\": Compose keeps both", got)
	}
}

// tmpfs is keyed on the whole entry rather than on any part of it.
//
// Two oracle observations pin this, and the first one is the discriminating
// case. `docker compose config` on `/tmp:size=64m` + `/run:size=64m` returns
// BOTH — obviously, they are different mounts — and a merge that keyed tmpfs
// the way it keys a volume would read `size=64m` as the target of each and
// collapse them into one. This assertion exists to fail if that is ever tried:
// the earlier version of it used `/tmp:size=64m` against `/tmp`, which passes
// under either keying and therefore proved nothing.
//
// The second: `/tmp:size=64m` against `/tmp` is not a merge for Compose at all,
// it is an error — "services.a.tmpfs[1]: target /tmp already mounted as
// services.a.tmpfs[0]". So there is no winner to pick, and picking one would
// hide the refusal.
func TestMergeTmpfsIsKeyedOnTheWholeEntry(t *testing.T) {
	distinct := merge(t,
		"services:\n  web:\n    image: nginx\n    tmpfs: [\"/tmp:size=64m\"]\n",
		"services:\n  web:\n    tmpfs: [\"/run:size=64m\"]\n")
	if got := strings.Join(seqAt(t, distinct, "services.web.tmpfs"), ","); got != "/tmp:size=64m,/run:size=64m" {
		t.Errorf("tmpfs = %q, want both mounts: they share only their options, and Compose returns both", got)
	}

	sharedPath := merge(t,
		"services:\n  web:\n    image: nginx\n    tmpfs: [\"/tmp:size=64m\"]\n",
		"services:\n  web:\n    tmpfs: [\"/tmp\"]\n")
	if got := strings.Join(seqAt(t, sharedPath, "services.web.tmpfs"), ","); got != "/tmp:size=64m,/tmp" {
		t.Errorf("tmpfs = %q, want both entries: Compose does not choose between them, it errors", got)
	}
}

// devices is the one attribute in the dedupe group keyed on something other
// than the whole entry, and getting it wrong is silent in both directions.
//
// The oracle:
//
//	/dev/ttyS0:/dev/x  +  /dev/sda:/dev/x   -> ONE device, source /dev/sda
//	/dev/x:/dev/a      +  /dev/x:/dev/b     -> TWO devices
//
// So the identity is the container path, and the later declaration wins it —
// exactly a volume's rule, and nothing like whole-value equality.
func TestMergeDevicesKeyedOnTargetNotOnTheWholeEntry(t *testing.T) {
	same := merge(t,
		"services:\n  web:\n    image: nginx\n    devices: [\"/dev/ttyS0:/dev/x\"]\n",
		"services:\n  web:\n    devices: [\"/dev/sda:/dev/x\"]\n")
	if got := seqAt(t, same, "services.web.devices"); len(got) != 1 || got[0] != "/dev/sda:/dev/x" {
		t.Errorf("devices = %v, want one entry /dev/sda:/dev/x: two devices at one container path are one device", got)
	}

	distinct := merge(t,
		"services:\n  web:\n    image: nginx\n    devices: [\"/dev/x:/dev/a\"]\n",
		"services:\n  web:\n    devices: [\"/dev/x:/dev/b\"]\n")
	if got := strings.Join(seqAt(t, distinct, "services.web.devices"), ","); got != "/dev/x:/dev/a,/dev/x:/dev/b" {
		t.Errorf("devices = %q, want both: one source at two container paths is two devices", got)
	}
}

// The four rows whose deletion the oracle CONFIRMED. Compose appends these, so
// the repeat must survive the merge — restoring a dedupe here would be the same
// mistake in the other direction.
func TestMergeKeepsTheRepeatWhereTheOracleSaysComposeAppends(t *testing.T) {
	for _, c := range []struct{ path, yaml, want string }{
		{"services.web.build.platforms",
			"    build:\n      platforms: [\"linux/amd64\"]\n", "linux/amd64,linux/amd64"},
		{"services.web.build.cache_from",
			"    build:\n      cache_from: [\"type=local\"]\n", "type=local,type=local"},
		{"services.web.deploy.placement.constraints",
			"    deploy:\n      placement:\n        constraints: [\"node.role==worker\"]\n", "node.role==worker,node.role==worker"},
		{"services.web.deploy.placement.preferences",
			"    deploy:\n      placement:\n        preferences: [\"spread=node.labels.zone\"]\n", "spread=node.labels.zone,spread=node.labels.zone"},
	} {
		t.Run(c.path, func(t *testing.T) {
			p := merge(t,
				"services:\n  web:\n    image: nginx\n"+c.yaml,
				"services:\n  web:\n"+c.yaml)
			if got := strings.Join(seqAt(t, p, c.path), ","); got != c.want {
				t.Errorf("%s = %q, want %q: the oracle returns the repeat twice, so no dedupe row belongs here",
					c.path, got, c.want)
			}
		})
	}
}

// security_opt, group_add and volumes_from are the sharp ones: Compose neither
// deduplicates nor tolerates the repeat. It concatenates and then REFUSES the
// project —
//
//	validating .../compose.override.yaml: services.a.security_opt items at 0 and 1 are equal
//
// — while two files declaring DIFFERENT entries merge to both. So the merge
// must keep both copies (deduplicating would resolve a project that cannot be
// loaded, in silence) and must SAY SO. Same contract as the form-mismatch
// finding: a finding, because the files are legal; never an error, never a fix.
func TestRepeatedStrictListItemIsKeptAndReported(t *testing.T) {
	for _, c := range []struct{ key, entry string }{
		{"security_opt", "no-new-privileges:true"},
		{"group_add", "1001"},
		{"volumes_from", "b"},
	} {
		t.Run(c.key, func(t *testing.T) {
			p := merge(t,
				"services:\n  web:\n    image: nginx\n    "+c.key+": [\""+c.entry+"\"]\n  b:\n    image: redis\n",
				"services:\n  web:\n    "+c.key+": [\""+c.entry+"\", \"other\"]\n")

			got := strings.Join(seqAt(t, p, "services.web."+c.key), ",")
			if want := c.entry + "," + c.entry + ",other"; got != want {
				t.Errorf("%s = %q, want %q: Compose appends these, and collapsing the repeat would "+
					"resolve a document Compose refuses to load", c.key, got, want)
			}

			var found *Finding
			for i, f := range p.Findings() {
				if f.Kind == FindingRepeatedListItem && f.Path.Equal(ParsePath("services.web."+c.key)) {
					found = &p.Findings()[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("no %s finding for %s; the merge produced a project `docker compose` rejects "+
					"and said nothing, which is the defect", FindingRepeatedListItem, c.key)
			}
			if !strings.Contains(found.Message, c.entry) {
				t.Errorf("finding does not name the repeated entry %q: %s", c.entry, found.Message)
			}
			if !strings.Contains(found.Message, "items at 0 and 1 are equal") {
				t.Errorf("finding does not quote Compose's own refusal, so a reader cannot connect the "+
					"two reports: %s", found.Message)
			}
		})
	}
}

// The complement, and the one that keeps the finding from becoming noise: two
// files declaring DIFFERENT security_opt entries are a project Compose accepts
// — verified, it returns both — so nothing may be reported.
func TestDistinctStrictListItemsMergeWithoutAFinding(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    security_opt: [\"label:disable\"]\n",
		"services:\n  web:\n    security_opt: [\"no-new-privileges:true\"]\n")
	if got := strings.Join(seqAt(t, p, "services.web.security_opt"), ","); got != "label:disable,no-new-privileges:true" {
		t.Errorf("security_opt = %q, want both entries in file order", got)
	}
	for _, f := range p.Findings() {
		if f.Kind == FindingRepeatedListItem {
			t.Errorf("unexpected finding on a project Compose accepts: %s", f.Message)
		}
	}
}

// `!reset` removes a declaration.
func TestResetRemovesADeclaration(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    ports:
      - "8080:80"
    environment:
      A: 1
`, `
services:
  web:
    ports: !reset []
    environment:
      A: !reset null
`)
	if v, ok := p.At(ParsePath("services.web.ports")); ok {
		t.Errorf("ports survived a !reset as %v", v.Kind())
	}
	if v, ok := p.At(ParsePath("services.web.environment.A")); ok {
		t.Errorf("environment.A survived a !reset as %q", v.Scalar())
	}
	// The service itself is still there — !reset removed a key, not the world.
	if _, ok := p.At(ParsePath("services.web.image")); !ok {
		t.Error("!reset removed more than it was pointed at")
	}
}

// A `!reset` in a single file has nothing to remove and must not survive as a
// null that reads like an empty declaration.
func TestResetInASingleFileLeavesNothingBehind(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  web:\n    image: nginx\n    command: !reset null\n"), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.At(ParsePath("services.web.command")); ok {
		t.Error("a !reset in a single file left a value in the model")
	}
}

// `!override` bypasses the merge rule for its key: no appending, no key-wise
// merge, no uniqueness.
func TestOverrideTagBypassesTheRule(t *testing.T) {
	p := merge(t, `
services:
  web:
    image: nginx
    dns_search:
      - a.example
      - b.example
`, `
services:
  web:
    dns_search: !override
      - only.example
`)
	if got := seqAt(t, p, "services.web.dns_search"); strings.Join(got, ",") != "only.example" {
		t.Errorf("dns_search = %v; !override must replace, not append", got)
	}
}

// The provenance half of story 1.4, and the reason the merge is not three
// lines: an overridden value reports the override file as its origin and
// carries what it replaced, with that value's own origin.
func TestOverrideHistoryCarriesTheReplacedValueAndItsOrigin(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx:1.0\n",
		"services:\n  web:\n    image: nginx:2.0\n")

	v, _ := p.At(ParsePath("services.web.image"))
	if v.Scalar() != "nginx:2.0" {
		t.Fatalf("image = %q", v.Scalar())
	}
	o := v.Origin()
	if filepath.Base(o.File) != "compose.override.yaml" {
		t.Errorf("origin file = %s, want the override file", o.File)
	}
	// AD-15: Step is the index into the ordered file list, not a per-key
	// counter.
	if o.Step != 1 {
		t.Errorf("Step = %d, want 1 — the index of the override file", o.Step)
	}
	files := p.Files()
	if len(files) != 2 || files[o.Step].Path != o.File {
		t.Errorf("Step does not index the file list: files=%v origin=%v", files, o)
	}

	ov := v.Overrides()
	if len(ov) != 1 {
		t.Fatalf("override history = %+v, want one entry", ov)
	}
	if ov[0].Value != "nginx:1.0" {
		t.Errorf("overrode %q, want nginx:1.0", ov[0].Value)
	}
	if filepath.Base(ov[0].Origin.File) != "compose.yaml" || ov[0].Origin.Step != 0 {
		t.Errorf("the replaced value's origin is %v, want the base file at step 0", ov[0].Origin)
	}
}

// A value nothing overrode still reports an override history — present and
// empty, the shape story 1.1 fixed and every merge story appends to.
func TestUnoverriddenValueKeepsAnEmptyHistory(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    restart: always\n",
		"services:\n  web:\n    image: nginx:2\n")
	// The `!= nil` half of this assertion could not fail: the accessor `make()`s
	// its slice and make never returns nil. What can actually break is the
	// WIRE — a nil internal slice marshals to `null`, and a consumer switching
	// on `x === null` against `x.length === 0` would be told "nothing was
	// checked" where the answer is "nothing was wrong". So the wire is asserted
	// too. See TestEmptyListsSerialiseAsListsNotNull.
	v, _ := p.At(ParsePath("services.web.restart"))
	if got := v.Overrides(); got == nil || len(got) != 0 {
		t.Errorf("override history = %v, want present and empty", got)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"overrides":[]`)) {
		t.Errorf("override history is not [] on the wire: %s", raw)
	}
}

// The table is the interface. This test reads it rather than the walker,
// because AD-4's promise is that adding a key means adding a row — and a row
// that the walker cannot act on breaks that promise silently.
func TestEveryTableRowIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range mergeTable {
		if seen[r.pattern] {
			t.Errorf("%s appears twice in the table; the second row is dead", r.pattern)
		}
		seen[r.pattern] = true
		if r.pattern == "" {
			t.Error("a row has no pattern")
		}
		if r.how == behaviourAppendUnique && r.key == nil {
			t.Errorf("%s is appendUnique with no key function — the walker would fall back to appending", r.pattern)
		}
		if r.how == behaviourAppendStrictUnique && r.key == nil {
			t.Errorf("%s is appendStrictUnique with no key function — the repeat would go unreported", r.pattern)
		}
		if r.how != behaviourAppendUnique && r.how != behaviourAppendStrictUnique && r.key != nil {
			t.Errorf("%s carries a key function its behaviour never reads", r.pattern)
		}
		// Provenance is the promise this table makes: a row may cite §13 only
		// if §13 names the attribute. A fabricated citation reads as verified
		// and is worse than no row at all — it is what this table was audited
		// for once already.
		// A citation is a note that OPENS with "§13:" — that is the convention
		// the table uses, and it is what a reader takes as "the spec says so".
		// A note that merely contains the string is saying the opposite
		// ("Compose behaviour, not §13: ..."), which is the honest label.
		if strings.HasPrefix(r.specNote, "§13:") && !spec13Attributes[r.pattern] {
			t.Errorf("%s cites \"§13:\" but §13 names only %v; label it with the evidence that actually "+
				"establishes it", r.pattern, sortedKeys(spec13Attributes))
		}
		if r.how != behaviourDefault && r.specNote == "" {
			t.Errorf("%s changes the merge and states no provenance", r.pattern)
		}
	}
	// The rows the acceptance criteria name, checked by lookup rather than by
	// reading the slice, so a row moved to another file still counts.
	for _, c := range []struct {
		path string
		want behaviour
	}{
		{"services.web.command", behaviourReplace},
		{"services.web.entrypoint", behaviourReplace},
		{"services.web.healthcheck.test", behaviourReplace},
		{"services.web.ports", behaviourAppendUnique},
		{"services.web.volumes", behaviourAppendUnique},
		{"services.web.secrets", behaviourAppendUnique},
		{"services.web.configs", behaviourAppendUnique},
		{"services.web.image", behaviourDefault},
		{"services.web.environment", behaviourAppendUnique},
		{"x-anything", behaviourDefault},
		// Not §13, and not guesses either: each of these is what
		// `docker compose config` was observed to do on a two-file project.
		// See testdata/differential/dedupe-rows and the notes in mergerules.go.
		{"services.web.cap_add", behaviourAppendUnique},
		{"services.web.cap_drop", behaviourAppendUnique},
		{"services.web.dns", behaviourAppendUnique},
		{"services.web.dns_opt", behaviourAppendUnique},
		{"services.web.dns_search", behaviourAppendUnique},
		{"services.web.expose", behaviourAppendUnique},
		{"services.web.tmpfs", behaviourAppendUnique},
		{"services.web.links", behaviourAppendUnique},
		{"services.web.profiles", behaviourAppendUnique},
		{"services.web.devices", behaviourAppendUnique},
		{"services.web.build.tags", behaviourAppendUnique},
		// Compose appends and then rejects the repeat.
		{"services.web.security_opt", behaviourAppendStrictUnique},
		{"services.web.group_add", behaviourAppendStrictUnique},
		{"services.web.volumes_from", behaviourAppendStrictUnique},
		// The oracle says Compose APPENDS these four, so they must carry no
		// row at all — a dedupe restored here would be the audit's mistake
		// made in the opposite direction.
		{"services.web.build.platforms", behaviourDefault},
		{"services.web.build.cache_from", behaviourDefault},
		{"services.web.deploy.placement.constraints", behaviourDefault},
		{"services.web.deploy.placement.preferences", behaviourDefault},
		{"services.web.env_file", behaviourDefault},
	} {
		if got := ruleFor(ParsePath(c.path)).how; got != c.want {
			t.Errorf("ruleFor(%s) = %s, want %s", c.path, got, c.want)
		}
	}
}

// A path is generalised before the table is consulted: one row covers every
// service, and a service called `command` does not collide with the key.
func TestPatternGeneralisation(t *testing.T) {
	cases := map[string]string{
		"services.web.ports":            "services.*.ports",
		"services.anything-here.ports":  "services.*.ports",
		"services.web.ports.0":          "services.*.ports.*",
		"networks.frontend.driver":      "networks.*.driver",
		"services.command.command":      "services.*.command",
		"volumes.data":                  "volumes.*",
		"x-shared.anchor":               "x-shared.anchor",
		"services.web.build.args":       "services.*.build.args",
		"services.web.deploy.resources": "services.*.deploy.resources",
	}
	for in, want := range cases {
		if got := patternOf(ParsePath(in)); got != want {
			t.Errorf("patternOf(%s) = %s, want %s", in, got, want)
		}
	}
}

// The uniqueness key extractors, each against the syntaxes the spec allows.
func TestUniquenessKeyExtractors(t *testing.T) {
	scalar := func(s string) *Value { v := newValue(KindScalar, Origin{}); v.scalar = s; return v }
	mapping := func(kv ...string) *Value {
		v := newValue(KindMapping, Origin{})
		v.mapping = newOrderedMap()
		for i := 0; i+1 < len(kv); i += 2 {
			v.mapping.set(kv[i], scalar(kv[i+1]), Origin{})
		}
		return v
	}

	cases := []struct {
		name string
		fn   uniqueKeyFn
		in   *Value
		want string
	}{
		{"port short", keyPort, scalar("8080:80"), "|8080|80|tcp"},
		{"port protocol", keyPort, scalar("53:53/udp"), "|53|53|udp"},
		{"port container only", keyPort, scalar("80"), "||80|tcp"},
		{"port with host ip", keyPort, scalar("127.0.0.1:8080:80"), "127.0.0.1|8080|80|tcp"},
		{"port long", keyPort, mapping("target", "80", "published", "8080"), "|8080|80|tcp"},
		{"volume short", keyMountTarget, scalar("./src:/app"), "/app"},
		{"volume anonymous", keyMountTarget, scalar("/data"), "/data"},
		{"volume with mode", keyMountTarget, scalar("./src:/app:ro"), "/app"},
		{"volume windows", keyMountTarget, scalar(`C:\data:/data`), "/data"},
		{"volume long", keyMountTarget, mapping("type", "bind", "target", "/app"), "/app"},
		// §13 keys secrets and configs on TARGET. The short form carries no
		// target, so the DEFAULT mount path is the identity — that is the path
		// the container gets, and it is what makes `- db-password` and the long
		// form naming the same default meet as one mount instead of two.
		{"secret short", keySecretTarget, scalar("db-password"), "/run/secrets/db-password"},
		{"secret long implicit target", keySecretTarget, mapping("source", "db-password"), "/run/secrets/db-password"},
		{"secret long explicit target", keySecretTarget, mapping("source", "db-password", "target", "/etc/db"), "/etc/db"},
		{"config short", keyConfigTarget, scalar("nginx-conf"), "/nginx-conf"},
		{"config long explicit target", keyConfigTarget, mapping("source", "nginx-conf", "target", "/etc/nginx.conf"), "/etc/nginx.conf"},
		{"env pair", keyBeforeEquals, scalar("A=1"), "A"},
		{"env passthrough", keyBeforeEquals, scalar("A"), "A"},
		{"extra host", keyBeforeColonOrEquals, scalar("host.docker.internal:172.17.0.1"), "host.docker.internal"},
	}
	for _, c := range cases {
		got, ok := c.fn(c.in)
		if !ok {
			t.Errorf("%s: no key extracted", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s: key = %q, want %q", c.name, got, c.want)
		}
	}

	// An element with no readable identity is APPENDED, never guessed at: a
	// wrong identity silently deletes, a duplicate merely repeats.
	if _, ok := keyMountTarget(newValue(KindNull, Origin{})); ok {
		t.Error("a null mount produced an identity")
	}
}

// The committed fixture for the two merge tags. It is a single file, where
// neither tag has anything to act on — which is exactly the case a resolver
// gets wrong by carrying `!reset` into the model as an ordinary null.
func TestMergeTagFixture(t *testing.T) {
	p, err := File("../../testdata/adversarial/08-merge-tags.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{
		"services.api.command",
		"services.api.ports",
		"services.api.environment.DROPPED",
	} {
		if v, ok := p.At(ParsePath(gone)); ok {
			t.Errorf("%s survived a !reset as %s", gone, v.Kind())
		}
	}
	if got := scalarAt(t, p, "services.api.environment.KEEP"); got != "kept" {
		t.Errorf("!reset removed a sibling key: KEEP = %q", got)
	}
	if got := seqAt(t, p, "services.api.dns_search"); strings.Join(got, ",") != "internal.example" {
		t.Errorf("dns_search = %v; !override must leave an ordinary value", got)
	}
	// A QUOTED "!reset" is a string. The same distinction the merge key makes
	// for a quoted "<<".
	if got := scalarAt(t, p, "services.worker.command"); got != "!reset" {
		t.Errorf("worker command = %q; a quoted tag is a string", got)
	}
}

// ---- the rules that had no coverage at all ---------------------------------

// §13's DEFAULT rule for sequences — append — had ZERO coverage. Replacing
// merge.go's `dst.seq = append(dst.seq, src.seq...)` with `dst.seq = src.seq`
// passed the entire suite.
//
// TestMergeSequencesAppend looked like it covered this and did not: it merged
// dns_search, which the table used to route to appendUnique, so it exercised
// the uniqueness path and asserted a result the default rule happens to produce
// too. A test of the default rule has to merge a path the table does not list,
// and has to assert something appending does that replacing does not — which
// means a DUPLICATE, since that is the only observable difference.
func TestMergeDefaultRuleAppendsAndKeepsDuplicates(t *testing.T) {
	const path = "services.web.some_unlisted_sequence"
	if got := ruleFor(ParsePath(path)).how; got != behaviourDefault {
		t.Fatalf("%s takes the %s rule; this test must exercise the DEFAULT one", path, got)
	}
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    some_unlisted_sequence:\n      - a\n      - b\n",
		"services:\n  web:\n    some_unlisted_sequence:\n      - b\n      - c\n")
	got := seqAt(t, p, path)
	// a,b,b,c — the base's entries FIRST (so replacing fails), and b twice (so
	// a stray dedupe fails).
	if want := "a,b,b,c"; strings.Join(got, ",") != want {
		t.Errorf("sequence = %v, want %s: the default rule appends, it does not replace and does not dedupe", got, want)
	}
}

// "The key's own position moves to the file that last set the value" — merge.go
// passing src's key origin to dst.mapping.set — was untested. Keeping the base
// file's KeyOrigin passed everything.
//
// It decides WHICH FILE AN EDIT IS SPLICED INTO: an inspector that offers to
// change the effective image tag must edit the override that set it, not the
// base whose value is not in effect.
func TestKeyOriginMovesToTheFileThatSetTheValue(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: nginx:1\n    restart: always\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(over, []byte("services:\n  web:\n    image: nginx:2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(Options{Files: []string{base, over}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	web, ok := p.At(ParsePath("services.web"))
	if !ok {
		t.Fatal("services.web did not resolve")
	}

	ko, ok := web.Map().KeyOrigin("image")
	if !ok {
		t.Fatal("no key origin for image")
	}
	if ko.File != over {
		t.Errorf("image key origin is in %s, want %s: an edit must be spliced into the file that set the effective value", ko.File, over)
	}
	if ko.Line != 3 {
		t.Errorf("image key line = %d, want 3", ko.Line)
	}
	if ko.Step != 1 {
		t.Errorf("image key step = %d, want 1", ko.Step)
	}

	// A key ONLY the base sets keeps the base's position. The rule is "the file
	// that last set the value", not "the last file".
	rko, ok := web.Map().KeyOrigin("restart")
	if !ok {
		t.Fatal("no key origin for restart")
	}
	if rko.File != base {
		t.Errorf("restart key origin is in %s, want %s", rko.File, base)
	}
}

// A sequence in one file meeting a mapping in the other DROPS the sequence's
// entries — this engine keeps the shape the file wrote, so it cannot normalise
// the way Compose does. What it must never do is drop them silently, which is
// what it did: `environment: [A=1, B=2]` then `environment: {B: two}` resolved
// to B alone, with A=1 gone and nothing anywhere saying so.
func TestMergingAListOntoAMappingRaisesAFinding(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      - A=1\n      - B=2\n",
		"services:\n  web:\n    environment:\n      B: two\n")

	if _, ok := p.At(ParsePath("services.web.environment.A")); ok {
		t.Error("A survived; if this engine has started normalising, this test must be rewritten, not deleted")
	}

	var found *Finding
	for i, f := range p.Findings() {
		if f.Kind == FindingFormMismatch {
			found = &p.Findings()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no %s finding; the base's entries were dropped in silence, which is the defect", FindingFormMismatch)
	}
	if !found.Path.Equal(ParsePath("services.web.environment")) {
		t.Errorf("finding path = %s, want services.web.environment", found.Path)
	}
	if !strings.Contains(found.Message, "sequence") || !strings.Contains(found.Message, "mapping") {
		t.Errorf("finding does not name both forms: %s", found.Message)
	}

	// The finding has to carry WHAT was lost and WHERE it is still written, not
	// merely that something was. By the time anyone reads it the earlier value
	// is gone from the model — that is the complaint — so this is the only place
	// the answer exists.
	if len(found.Dropped) != 1 || found.Dropped[0] != "A" {
		t.Errorf("dropped = %v, want [A]: B is set by both files and is not a loss", found.Dropped)
	}
	if found.Displaced.IsZero() {
		t.Error("the finding does not say where the lost entries are written")
	}
	if !strings.Contains(found.Message, `"A"`) {
		t.Errorf("the message does not name the lost entry: %s", found.Message)
	}
}

// The same loss the other way round: a mapping in the base replaced by a list
// in the override. The divergence was verified in both directions, so both are
// pinned — a fix that only reported one of them would still lose configuration
// silently half the time.
func TestMergingAMappingOntoAListRaisesAFinding(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      A: \"1\"\n      B: two\n",
		"services:\n  web:\n    environment:\n      - B=2\n")

	var found *Finding
	for i, f := range p.Findings() {
		if f.Kind == FindingFormMismatch {
			found = &p.Findings()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no %s finding in the mapping-then-list direction", FindingFormMismatch)
	}
	if len(found.Dropped) != 1 || found.Dropped[0] != "A" {
		t.Errorf("dropped = %v, want [A]", found.Dropped)
	}
}

// depends_on is the same class, and it is the one where the silence costs most:
// a dropped dependency changes start order.
func TestMergingDependsOnAcrossFormsRaisesAFinding(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    depends_on:\n      - db\n      - cache\n  db:\n    image: postgres\n  cache:\n    image: redis\n",
		"services:\n  web:\n    depends_on:\n      db:\n        condition: service_healthy\n")
	var kinds []string
	for _, f := range p.Findings() {
		kinds = append(kinds, f.Kind)
	}
	if !strings.Contains(strings.Join(kinds, ","), FindingFormMismatch) {
		t.Errorf("findings = %v; the dropped dependency on cache must be reported", kinds)
	}
}

// Two files agreeing on the form merge normally and raise NOTHING. A finding
// that fires on ordinary merges is noise, and noise is how a real one gets
// ignored.
func TestMatchingFormsRaiseNoFormMismatchFinding(t *testing.T) {
	p := merge(t,
		"services:\n  web:\n    image: nginx\n    environment:\n      A: 1\n",
		"services:\n  web:\n    environment:\n      B: 2\n")
	for _, f := range p.Findings() {
		if f.Kind == FindingFormMismatch {
			t.Errorf("unexpected finding on a same-form merge: %s", f.Message)
		}
	}
}
