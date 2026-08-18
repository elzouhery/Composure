package diagnose

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// Story 3.1 — a credential written in plain text in a service's `environment:`.
//
// The design decision that matters here is what this rule refuses to do.
//
// The primary matcher is the KEY NAME, case-insensitively, on any path segment
// below `environment:`. There is no entropy scoring and no guess at what a
// value "looks like". Those are what generate the false positives that get a
// rule switched off, and a rule that flags six things per file with five of
// them wrong is switched off in a week and never switched back on. Precision
// over recall is the explicit choice: an oddly named credential —
// `PolicyServer:Host:Identity:Admins:0:Pw`, or `LOGIN_DETAILS` — slips through,
// and that is CORRECT behaviour, not a gap to close.
//
// Alongside it are exactly two value-side tests. Neither is a heuristic. Each
// recognises a value whose GRAMMAR states that a password is present, so there
// is nothing to guess:
//
//  1. `connectionStringMarker` — the literal substring `password=`. An
//     ADO/JDBC-style connection string carrying its own password.
//
//  2. `uriUserinfo` — a value that BEGINS with `scheme://user:password@`. RFC
//     3986 userinfo. `DATABASE_URL: postgres://shipyard:hunter2@db/shipyard`
//     is a plaintext credential under a key name that says nothing, which is
//     the one shape the closed token list provably cannot reach.
//
// Test 2 was added after being measured against the corpus rather than argued
// about, because the failure mode of this rule is noise and noise is a number.
// Over 146 compose files / 1180 `environment:` scalars:
//
//	43 values contain an `@`; the matcher fires on 27 of them
//	27/27 are true positives — a literal user:password pair, verified by hand
//	 0/27 are false positives
//	16 rejected: 8 × `redis://:@redis` (empty password, from Airflow's own
//	   quickstart), 5 × no scheme (`root:rootpass@(db:3306)/`, `1001@127.0.0.1`,
//	   an email inside an option string), 3 × no userinfo
//	 0 values were saved by the `${VAR}` exemption — the exemption is load
//	   bearing by design, not by luck, and is pinned by tests instead
//
//	 5 of 85 diagnosable corpus files gain a finding, and all 5 already carried
//	   a plaintext-credential hint. The extension lights up NO previously clean
//	   file, which is the property that decides whether a pane gets ignored.
//
// Anchoring at the start of the value, rather than searching for the pattern
// anywhere inside it, cost zero findings on that corpus and is what rejects
// `-Dgreenmail.users=test@localhost:test`. It stays anchored until a
// measurement says otherwise.
//
// What is deliberately NOT done: no placeholder denylist. `airflow:airflow` and
// `grafana:grafana` are 24 of the 27 hits and they are real, working passwords
// for the database in the same file — usually the same literal the key-name
// matcher already flags two dozen lines away. Deciding that some passwords are
// too silly to mention is exactly the value-shape guessing this rule refuses.
//
// Two exemptions keep it quiet:
//
//   - A value containing a variable reference is never flagged. `${DB_PASSWORD}`
//     means the secret is not in the file, which is the thing we were worried
//     about.
//   - A key ending `_FILE` is never flagged. `POSTGRES_PASSWORD_FILE` holds a
//     path to a secret, not a secret, and it is the convention Docker itself
//     documents for doing this right — complaining about it would punish the
//     correct answer.
//
// Severity is a hint, always — including for the URI form. The ladder is
// hint/warning/error where error means "this stack cannot work as written" and
// warning means "a real defect the stack may survive". A stack with a password
// in its DSN works perfectly; nothing about it is a configuration defect, and
// `POSTGRES_PASSWORD: hunter2` and `DATABASE_URL: postgres://u:hunter2@db/x`
// are the same secret written twice. Giving one of them a severity that changes
// the process exit code, and the other not, would be incoherent. It is advice,
// never a gate, and it never blocks a save. The value is never printed.

// secretKeyTokens are matched as case-insensitive substrings of a path segment.
// The list is closed and deliberately short; every addition costs precision.
var secretKeyTokens = []string{
	"password", "passwd", "pwd", "secret", "token",
	"apikey", "api_key", "credential", "private_key",
}

// connectionStringMarker is the first value-side test: an ADO/JDBC-style
// connection string carrying its own password.
const connectionStringMarker = "password="

// uriUserinfo is the second value-side test: RFC 3986 userinfo carrying a
// password. Every part of it is doing work, and each piece was chosen against
// a shape that occurs in the corpus:
//
//	^                    anchored. `-Dgreenmail.users=test@localhost:test` has
//	                     an address in the middle of an option string and is not
//	                     a credential. Costs 0 findings on the corpus.
//	[a-z][a-z0-9+.-]*:// a scheme is mandatory, so `1001@127.0.0.1:29093` (a
//	                     Kafka quorum voter) and `ops@example.com` cannot match.
//	                     `db+postgresql+psycopg` is why `+` and `.` are in the
//	                     class. Case-insensitive.
//	[^/\s:@]*           an OPTIONAL username, stopping before the host.
//	                     `redis://:s3cr3t@redis` is the standard Redis URL for
//	                     "password, no user" and is a leak like any other. This
//	                     one was found by mutation: making the username
//	                     mandatory killed no test, because the password is what
//	                     the rule is actually about.
//	:[^/\s@]+@           a NON-EMPTY password. This is the load-bearing part.
//	                     `redis://:@redis:6379/0` and `redis://default:@redis`
//	                     declare no password and must not fire;
//	                     `https://ci-bot@github.com/x.git` has no `:` in the
//	                     userinfo at all and cannot match either. Excluding `/`
//	                     from the password is spec-correct (an unencoded `/` in
//	                     userinfo is not a legal URI) but is NOT pinned by a
//	                     test: every input that distinguishes it is malformed,
//	                     and asserting either answer on malformed input would be
//	                     a check that only records what the code happens to do.
//
// A value containing `${VAR}` never reaches this — `check` exempts it earlier,
// which is what keeps `postgres://user:${DB_PASSWORD}@db/app` silent.
var uriUserinfo = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.\-]*://[^/\s:@]*:[^/\s@]+@`)

type credentialsRule struct{}

func (credentialsRule) id() string { return "plaintext-credential" }

func (credentialsRule) title() string { return "Credential written in plain text" }

func (r credentialsRule) apply(c *context) []Finding {
	var out []Finding
	services := c.Project.Services()
	for _, name := range services.Keys() {
		svc, _ := services.Get(name)
		env, ok := field(svc, "environment")
		if !ok || env == nil {
			continue
		}
		base := resolve.Path{"services", name, "environment"}
		env.Walk(func(rel resolve.Path, v *resolve.Value) bool {
			if v.Kind() != resolve.KindScalar {
				return true
			}
			path := append(append(resolve.Path{}, base...), rel...)
			if f, ok := r.check(c, name, path, rel, v); ok {
				out = append(out, f)
			}
			return true
		})
	}
	return out
}

// check applies the matcher to one scalar and builds the finding.
//
// rel is the path below `environment:`. In the mapping form its last segment is
// the variable name; in the list form the entry is `NAME=value` and the name
// lives inside the scalar, so both are considered.
func (r credentialsRule) check(c *context, service string, path, rel resolve.Path, v *resolve.Value) (Finding, bool) {
	// Raw, not Scalar: the model interpolates at load (story 1.3), and this
	// rule is about what is WRITTEN IN THE FILE. Reading the interpolated
	// value would report `POSTGRES_PASSWORD: ${DB_PASSWORD}` as a plaintext
	// credential on any machine where DB_PASSWORD happens to be exported —
	// a finding about the reader's shell, not about the file.
	raw := v.Raw()
	if strings.TrimSpace(raw) == "" {
		return Finding{}, false
	}

	name, value := rel.String(), raw
	if len(rel) > 0 {
		name = rel[len(rel)-1]
	}
	// List form: `- POSTGRES_PASSWORD=hunter2`. The segment is an index, so the
	// key name has to come out of the entry itself.
	if k, val, ok := listFormEntry(rel, raw); ok {
		name, value = k, val
	}

	// The secret is not in the file. Nothing to report — and this test comes
	// before the matcher so that a variable reference under a matching key is
	// exempt too, which is the case the acceptance criteria name.
	if interpolated(value) {
		return Finding{}, false
	}
	if strings.TrimSpace(value) == "" {
		return Finding{}, false
	}

	reason, matched := r.match(name, rel, value)
	if !matched {
		return Finding{}, false
	}

	origin := v.Origin()
	if origin.IsZero() {
		// AD-7: no anchor, no finding. Silently declining is right here — the
		// alternative is a hint pointing at nothing.
		return Finding{}, false
	}

	f := Finding{
		Rule:     r.id(),
		Severity: SeverityHint,
		Title:    r.title(),
		// The value is never interpolated into this string.
		Message:  fmt.Sprintf("service %q sets %s to a literal value in the file; %s", service, name, reason),
		Subjects: []resolve.Path{{"services", service}},
		Anchors: []Anchor{{
			Label:  "written here",
			Path:   path,
			Origin: origin,
		}},
	}
	// The replacement has to be a whole valid entry, and the two forms of
	// `environment:` are not the same lexeme.
	//
	// In the mapping form the scalar is the VALUE alone, so `${SECRET}` replaces
	// it correctly. In the list form the scalar is the whole `NAME=value` entry,
	// so replacing it with `${SECRET}` would leave `- ${SECRET}` — an entry with
	// no `=`, which Compose reads as "pass through the host variable whose name
	// is whatever that interpolates to". The variable's own name has to be kept.
	ref := "${" + envVarName(name) + "}"
	replacement := ref
	if _, _, isList := listFormEntry(rel, raw); isList {
		replacement = name + "=" + ref
	}
	fix, why := c.replaceScalarFix(path, origin, replacement,
		fmt.Sprintf("replace the literal with %s and supply it from the environment or a secret", ref))
	f.Fix, f.NoFix = fix, why
	// Story 9.5. The other half of the advice: where the literal goes.
	//
	// Attached HERE and nowhere else, deliberately. Every rule in this package
	// reaches the same locateFix helpers, so a remedy hung off a shared helper
	// would be offered by all of them — and `composure extract` moves a literal
	// into a variable, which is the answer to a plaintext credential and to
	// nothing else in the rule set.
	if fix != nil {
		fix.Remedy = extractRemedy(fix, envVarName(name), value)
	}
	return f, true
}

// match is the whole matcher. Key name, case-insensitively, on any segment —
// plus the two grammatical value-side tests.
func (r credentialsRule) match(name string, rel resolve.Path, value string) (reason string, ok bool) {
	if strings.Contains(strings.ToLower(value), connectionStringMarker) {
		return "the value is a connection string carrying a password", true
	}
	// TrimSpace only: the value is matched as written, never decoded. The
	// reason string names the shape and not one byte of the value.
	if uriUserinfo.MatchString(strings.TrimSpace(value)) {
		return "the value is a URI carrying a password in its userinfo", true
	}
	for _, seg := range append(append(resolve.Path{}, rel...), name) {
		lower := strings.ToLower(seg)
		if strings.HasSuffix(lower, "_file") {
			// A path to a secret is not a secret. This is the documented right
			// way to do it and must not be punished.
			continue
		}
		for _, tok := range secretKeyTokens {
			if strings.Contains(lower, tok) {
				return "the key name marks it as a credential", true
			}
		}
	}
	return "", false
}

// listFormEntry splits `NAME=value` when the path segment is a sequence index.
// A bare `- NAME` with no `=` takes its value from the host environment and
// carries no secret, so it is not an entry.
func listFormEntry(rel resolve.Path, raw string) (name, value string, ok bool) {
	if len(rel) == 0 || !isIndex(rel[len(rel)-1]) {
		return "", "", false
	}
	k, v, found := strings.Cut(raw, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(k), v, true
}

func isIndex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// envVarName turns a key into a plausible variable name for the suggested fix.
// It is a display detail of the description; nothing depends on it.
func envVarName(key string) string {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 32)
		case isNameChar(c):
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || !isNameStart(out[0]) {
		out = "_" + out
	}
	return out
}
