package diagnose

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

const credRule = "plaintext-credential"

func TestCredentialInMappingForm(t *testing.T) {
	rep := run(t, `
services:
  db:
    image: postgres
    environment:
      POSTGRES_USER: shipyard
      POSTGRES_PASSWORD: hunter2
`)
	got := wantCount(t, rep, credRule, 1)[0]
	if got.Severity != SeverityHint {
		t.Errorf("severity is %s, want hint — this rule is advice, never a gate", got.Severity)
	}
	a := got.Anchors[0].Origin
	if a.Line != 7 || a.Column != 26 {
		t.Errorf("anchored at %s, want the value on line 7 column 26", a)
	}
	if strings.Contains(got.Message, "hunter2") {
		t.Fatalf("the message printed the secret: %s", got.Message)
	}
}

func TestCredentialInListForm(t *testing.T) {
	rep := run(t, `
services:
  db:
    image: postgres
    environment:
      - POSTGRES_USER=shipyard
      - POSTGRES_PASSWORD=hunter2
`)
	got := wantCount(t, rep, credRule, 1)[0]
	if !strings.Contains(got.Message, "POSTGRES_PASSWORD") {
		t.Errorf("message does not name the key: %s", got.Message)
	}
	if strings.Contains(got.Message, "hunter2") {
		t.Fatalf("the message printed the secret: %s", got.Message)
	}
}

func TestCredentialConnectionString(t *testing.T) {
	rep := run(t, `
services:
  app:
    image: app
    environment:
      ConnectionStrings__Default: "Server=db;Database=app;User Id=sa;Password=Str0ng!;"
`)
	got := wantCount(t, rep, credRule, 1)[0]
	if !strings.Contains(got.Message, "connection string") {
		t.Errorf("message does not explain the match: %s", got.Message)
	}
	if strings.Contains(got.Message, "Str0ng!") {
		t.Fatalf("the message printed the secret: %s", got.Message)
	}
}

func TestCredentialMatchesEveryToken(t *testing.T) {
	for _, key := range []string{
		"PASSWORD", "db_passwd", "MYSQL_PWD", "APP_SECRET", "GITHUB_TOKEN",
		"apikey", "SERVICE_API_KEY", "aws_credential", "SSH_PRIVATE_KEY",
		"MixedCasePassWord",
	} {
		t.Run(key, func(t *testing.T) {
			rep := run(t, "services:\n  s:\n    image: x\n    environment:\n      "+key+": literal\n")
			wantCount(t, rep, credRule, 1)
		})
	}
}

// The exemptions. Each of these is a decision, not an oversight.

// A variable reference means the secret is not in the file, which is the thing
// the rule was worried about.
func TestCredentialVariableReferenceIsNotFlagged(t *testing.T) {
	rep := run(t, `
services:
  db:
    image: postgres
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      LICENSE_KEY: ${LICENSE_KEY:-none}
      MIXED_SECRET: "prefix-${PART}"
      BARE_TOKEN: $HOST_TOKEN
`, withEnv(map[string]string{"DB_PASSWORD": "x", "PART": "y", "HOST_TOKEN": "z"}))
	wantNone(t, rep, credRule)
}

// `*_FILE` is Docker's own documented way of doing this right. Flagging it
// would punish the correct answer.
func TestCredentialFileIndirectionIsNotFlagged(t *testing.T) {
	rep := run(t, `
services:
  db:
    image: postgres
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/db_password
`)
	wantNone(t, rep, credRule)
}

// The precision-over-recall decision, as a test rather than a comment. These
// keys are unmistakably credentials to a human and the rule misses them,
// deliberately: matching them would require value-shape guessing, and that is
// what generates the false positives that get a rule switched off.
//
// `DB_CONN: postgres://user:hunter2@db:5432/app` used to be a third line of
// this test. It moved out to TestCredentialURIUserinfo when the value-side URI
// test shipped — the key name is still ignored, but the VALUE now announces
// itself structurally. The two lines that remain are the philosophy: nothing
// about `LOGIN_DETAILS` or `...:Pw` is recognisable without guessing at shape.
func TestCredentialOddlyNamedKeyIsMissedOnPurpose(t *testing.T) {
	rep := run(t, `
services:
  app:
    image: app
    environment:
      PolicyServer:Host:Identity:Admins:0:Pw: s3cret
      LOGIN_DETAILS: hunter2
`)
	wantNone(t, rep, credRule)
}

// The URI userinfo extension. `DATABASE_URL: postgres://u:p@db/x` carries a
// password in plain text under a key name that says nothing at all, which is
// the case the closed token list cannot reach.
func TestCredentialURIUserinfo(t *testing.T) {
	rep := run(t, `
services:
  app:
    image: app
    environment:
      DATABASE_URL: postgres://shipyard:hunter2@db:5432/shipyard
`)
	got := wantCount(t, rep, credRule, 1)[0]
	if got.Severity != SeverityHint {
		t.Errorf("severity is %s, want hint — a credential in a URI is the same advice, not a gate", got.Severity)
	}
	a := got.Anchors[0].Origin
	if a.Line != 6 || a.Column != 21 {
		t.Errorf("anchored at %s, want the value on line 6 column 21", a)
	}
	if !strings.Contains(got.Message, "DATABASE_URL") {
		t.Errorf("message does not name the key: %s", got.Message)
	}
	if !strings.Contains(got.Message, "URI") {
		t.Errorf("message does not explain the match: %s", got.Message)
	}
	// The whole reason this shape is dangerous: the secret is in the middle of
	// a longer string, so a message that quoted "the value" would leak it.
	for _, leak := range []string{"hunter2", "shipyard:hunter2", "postgres://"} {
		if strings.Contains(got.Message, leak) {
			t.Fatalf("the message printed %q: %s", leak, got.Message)
		}
	}
	if got.Fix == nil {
		t.Fatalf("no fix described: %s", got.NoFix)
	}
	if got.Fix.Value != "${DATABASE_URL}" {
		t.Errorf("fix value is %q", got.Fix.Value)
	}
}

// In the list form the URI is the tail of a `NAME=value` entry, so the matcher
// has to be applied to the tail and not to the whole entry.
func TestCredentialURIInListForm(t *testing.T) {
	rep := run(t, `
services:
  app:
    image: app
    environment:
      - DATABASE_URL=postgres://shipyard:hunter2@db:5432/shipyard
`)
	got := wantCount(t, rep, credRule, 1)[0]
	if strings.Contains(got.Message, "hunter2") {
		t.Fatalf("the message printed the secret: %s", got.Message)
	}
	if got.Fix == nil || got.Fix.Value != "DATABASE_URL=${DATABASE_URL}" {
		t.Errorf("fix does not keep the variable name: %+v", got.Fix)
	}
}

// The false positives, one at a time. Every shape here was seen in the corpus
// or is the documented right answer; each must stay silent on its own, because
// a table that only asserts the aggregate cannot tell "all of them pass" from
// "the rule is switched off".
func TestCredentialURIShapesThatMustNotFire(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"braced_interpolation", "postgres://user:${DB_PASSWORD}@db:5432/app"},
		{"bare_interpolation", "postgres://user:$DB_PASSWORD@db:5432/app"},
		// These two differ from the firing `redis://:s3cr3t@redis:6379/0` in
		// TestCredentialURIShapesControlFires by the password alone. Only the
		// password decides; the username is not part of the question.
		{"empty_password_no_user", "redis://:@redis:6379/0"},
		{"empty_password", "redis://default:@redis:6379/0"},
		{"username_only", "https://ci-bot@github.com/acme/app.git"},
		{"no_scheme_host_port", "1001@127.0.0.1:29093"},
		{"email", "ops@example.com"},
		{"no_scheme_go_dsn", "root:rootpass@(db:3306)/"},
		{"address_inside_options", "-Dgreenmail.users=test@localhost:test -Dgreenmail.verbose"},
		{"no_userinfo", "https://registry.example.com/v2/"},
		{"digest_reference", "ghcr.io/acme/app@sha256:abc123"},
		// Not a false positive — a deliberate miss. The matcher is anchored at
		// the start of the value; unanchoring it finds zero extra credentials
		// across the corpus, so it buys no recall for unmeasured noise. This
		// case is here so that deleting the `^` fails a test.
		{"buried_in_an_argument_string", "--dsn=postgres://user:hunter2@db:5432/app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := run(t, "services:\n  s:\n    image: x\n    environment:\n      SOME_URL: \""+tc.value+"\"\n",
				withEnv(map[string]string{"DB_PASSWORD": "x"}))
			wantNone(t, rep, credRule)
		})
	}
}

// The control for the table above: the same harness, the same key name, a value
// that IS a credential. Without this, a rule that never fires passes every case
// in TestCredentialURIShapesThatMustNotFire.
func TestCredentialURIShapesControlFires(t *testing.T) {
	// Redis' documented "password, no username" URL. Found by mutation: making
	// the username mandatory in uriUserinfo killed no test, because the password
	// is the only part the rule is about.
	rep := run(t, "services:\n  s:\n    image: x\n    environment:\n      SOME_URL: \"redis://:s3cr3t@redis:6379/0\"\n")
	wantCount(t, rep, credRule, 1)
	_ = rep
	rep = run(t, "services:\n  s:\n    image: x\n    environment:\n      SOME_URL: \"postgres://user:hunter2@db:5432/app\"\n",
		withEnv(map[string]string{"DB_PASSWORD": "x"}))
	wantCount(t, rep, credRule, 1)
}

// Only `environment:` is in scope. A secret declared through the `secrets:`
// mechanism is a file path, and a build arg is not this rule's business.
func TestCredentialScopeIsEnvironmentOnly(t *testing.T) {
	rep := run(t, `
services:
  app:
    image: app
    secrets:
      - db_password
secrets:
  db_password:
    file: ./db_password.txt
`)
	wantNone(t, rep, credRule)
}

func TestCredentialEmptyValueIsNotFlagged(t *testing.T) {
	rep := run(t, `
services:
  app:
    image: app
    environment:
      APP_SECRET: ""
      OTHER_TOKEN:
`)
	wantNone(t, rep, credRule)
}

// The permanent regression fixture, read from disk rather than written inline
// so that it also travels through the resolver's committed-fixture sweep and
// structbench. It asserts the EXACT set of lines that fire: a rule that went
// silent fails on the six missing lines, and a rule that got greedy fails on
// whichever of the twelve quiet lines it woke up on.
func TestCredentialURIFixture(t *testing.T) {
	const path = "../../testdata/edge/e29-uri-credential.yml"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	project, err := resolve.BytesWith(path, src, resolve.Options{
		Env: map[string]string{"DB_PASSWORD": "from-the-shell"}, IgnoreHostEnv: true,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	graph, err := topology.Build(project, nil)
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	rep, err := Run(Input{
		Path: path, Project: project, Graph: graph, AllProfiles: graph,
		Sources: map[string][]byte{path: src},
		Env:     map[string]string{"DB_PASSWORD": "from-the-shell"}, EnvKnown: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var lines []int
	for _, f := range findingsFor(rep, credRule) {
		lines = append(lines, f.Anchors[0].Origin.Line)
		// Whatever else changes, the secret never reaches the message. The
		// interpolation case is the trap: `DB_PASSWORD` is defined above, so a
		// rule reading Scalar() instead of Raw() would print "from-the-shell".
		for _, leak := range []string{"hunter2", "ghp_deadbeef", "p%40ssw0rd", "s3cr3t", "from-the-shell"} {
			if strings.Contains(f.Message, leak) {
				t.Errorf("line %d: message leaked %q: %s", f.Anchors[0].Origin.Line, leak, f.Message)
			}
		}
	}
	sort.Ints(lines)
	want := []int{13, 14, 15, 16, 17, 18}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("fired on lines %v, want %v — every other line in that fixture is a\n"+
			"shape measured against the corpus and must stay silent", lines, want)
	}
}

// Story 3.8: the fix names the operation, the path and the exact bytes — and
// the bytes are the value alone, never the trailing comment.
func TestCredentialFixIsTheValueAlone(t *testing.T) {
	src := "services:\n  db:\n    image: postgres\n    environment:\n      POSTGRES_PASSWORD: hunter2   # oops\n"
	rep := run(t, src)
	got := wantCount(t, rep, credRule, 1)[0]
	if got.Fix == nil {
		t.Fatalf("no fix described: %s", got.NoFix)
	}
	if got.Fix.Operation != FixReplaceScalar {
		t.Errorf("operation is %q", got.Fix.Operation)
	}
	if got.Fix.Path.String() != "services.db.environment.POSTGRES_PASSWORD" {
		t.Errorf("fix path is %s", got.Fix.Path)
	}
	if covered := src[got.Fix.Range.Start:got.Fix.Range.End]; covered != "hunter2" {
		t.Errorf("fix range covers %q, want %q", covered, "hunter2")
	}
	if got.Fix.Value != "${POSTGRES_PASSWORD}" {
		t.Errorf("fix value is %q", got.Fix.Value)
	}
}

// A value that arrived through a merge key does not exist at that path in the
// file, so no range can be derived. The finding still stands; the fix is
// declined with a reason, which is AD-8's refuse-rather-than-corrupt applied to
// a described edit.
func TestCredentialThroughAnchorHasNoFix(t *testing.T) {
	rep := run(t, `
x-env: &env
  APP_SECRET: hunter2

services:
  app:
    image: app
    environment:
      <<: *env
`)
	got := wantCount(t, rep, credRule, 1)[0]
	if got.Fix != nil {
		t.Fatalf("a fix was described for a value that is not at that path in the file")
	}
	if !strings.Contains(got.NoFix, "locate") {
		t.Errorf("the refusal does not say why: %s", got.NoFix)
	}
}
