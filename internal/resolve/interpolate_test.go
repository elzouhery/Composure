package resolve

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Story 1.3. Every test here states its own environment and sets
// IgnoreHostEnv, so the suite cannot pass or fail on whatever the developer
// happens to have exported — which is the failure mode a test of interpolation
// is most prone to.

func hermetic(env map[string]string) Options {
	return Options{Env: env, IgnoreHostEnv: true}
}

// The six forms, plus the escape, in one table. This is the grammar the story
// names, and each row is one clause of it.
func TestInterpolationGrammar(t *testing.T) {
	env := map[string]string{"SET": "value", "EMPTY": ""}
	cases := []struct {
		in   string
		want string
	}{
		// The plain forms.
		{"${SET}", "value"},
		{"$SET", "value"},
		{"prefix-${SET}-suffix", "prefix-value-suffix"},
		{"${MISSING}", ""},

		// `:-` treats empty as unset; `-` does not. This pair is the one that
		// silently changes what a stack runs if it is implemented backwards.
		{"${MISSING:-fallback}", "fallback"},
		{"${EMPTY:-fallback}", "fallback"},
		{"${SET:-fallback}", "value"},
		{"${MISSING-fallback}", "fallback"},
		{"${EMPTY-fallback}", ""},
		{"${SET-fallback}", "value"},

		// `:+` and `+`, the mirror image.
		{"${SET:+alt}", "alt"},
		{"${EMPTY:+alt}", ""},
		{"${MISSING:+alt}", ""},
		{"${EMPTY+alt}", "alt"},
		{"${MISSING+alt}", ""},

		// `$$` is a literal dollar, and it is what keeps every awk one-liner in
		// the corpus from being read as a reference.
		{"$$HOME", "$HOME"},
		{"echo $$1", "echo $1"},
		{"$$$SET", "$value"},

		// Nesting: a default is itself a template.
		{"${MISSING:-${SET}}", "value"},
		{"${MISSING:-${ALSO_MISSING:-deep}}", "deep"},
		{"${MISSING:-$SET}", "value"},

		// Not references at all.
		{"no variables", "no variables"},
		{"100$", "100$"},
		{"$(date)", "$(date)"},
		{"cost: $5", "cost: $5"},

		// Malformed: left exactly as written rather than guessed at.
		{"${", "${"},
		{"${}", "${}"},
		{"${1BAD}", "${1BAD}"},
	}

	for _, c := range cases {
		src := "services:\n  web:\n    image: nginx\n    environment:\n      A: " +
			quoteYAML(c.in) + "\n"
		p, err := BytesWith("compose.yaml", []byte(src), hermetic(env))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		v, ok := p.At(Path{"services", "web", "environment", "A"})
		if !ok {
			t.Errorf("%q: value did not resolve", c.in)
			continue
		}
		if got := v.Scalar(); got != c.want {
			t.Errorf("%q interpolated to %q, want %q", c.in, got, c.want)
		}
	}
}

func quoteYAML(s string) string { return "\"" + strings.ReplaceAll(s, `"`, `\"`) + "\"" }

// The text as written survives. R4.1: the buffer is never rewritten, and a
// model that had thrown away `${TAG}` could not tell an editor what bytes are
// there to splice.
func TestInterpolationKeepsTheTextAsWritten(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  web:\n    image: nginx:${TAG}\n    restart: always\n"),
		hermetic(map[string]string{"TAG": "1.27"}))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := p.At(Path{"services", "web", "image"})
	if got := img.Scalar(); got != "nginx:1.27" {
		t.Errorf("Scalar() = %q, want the interpolated value", got)
	}
	if got := img.Raw(); got != "nginx:${TAG}" {
		t.Errorf("Raw() = %q, want the text as written", got)
	}
	if !img.Interpolated() {
		t.Error("the value does not report that it was interpolated")
	}

	// A value with no reference reports Raw == Scalar, so a caller never has
	// to check which accessor to use.
	r, _ := p.At(Path{"services", "web", "restart"})
	if r.Raw() != "always" || r.Interpolated() {
		t.Errorf("a plain value reports Raw=%q interpolated=%v", r.Raw(), r.Interpolated())
	}
}

// An undefined variable resolves to empty and the resolve SUCCEEDS, recording a
// finding. This is AD-13's line: errors and findings never cross.
func TestUndefinedVariableIsAFindingNotAnError(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  web:\n    image: nginx\n    environment:\n      SECRET: ${SESSION_SECRET}\n"),
		hermetic(nil))
	if err != nil {
		t.Fatalf("an undefined variable must not fail the resolve: %v", err)
	}
	v, _ := p.At(Path{"services", "web", "environment", "SECRET"})
	if v.Scalar() != "" {
		t.Errorf("resolved to %q, want the empty string", v.Scalar())
	}

	fs := p.Findings()
	if len(fs) != 1 {
		t.Fatalf("%d findings, want exactly one", len(fs))
	}
	f := fs[0]
	if f.Kind != FindingUndefinedVariable || f.Variable != "SESSION_SECRET" {
		t.Errorf("finding = %+v", f)
	}
	if f.Origin.Line != 5 || f.Origin.File != "compose.yaml" {
		t.Errorf("finding is anchored at %s, want compose.yaml:5", f.Origin)
	}
	if !f.Path.Equal(Path{"services", "web", "environment", "SECRET"}) {
		t.Errorf("finding path = %s", f.Path)
	}
}

// A project with nothing wrong still has a findings list — present and empty,
// so "nothing was wrong" and "nothing was checked" cannot look alike.
func TestFindingsArePresentAndEmpty(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte("services:\n  web:\n    image: nginx\n"), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	// The `!= nil` half of this assertion could not fail: the accessor `make()`s
	// its slice and make never returns nil. What can actually break is the
	// WIRE — a nil internal slice marshals to `null`, and a consumer switching
	// on `x === null` against `x.length === 0` would be told "nothing was
	// checked" where the answer is "nothing was wrong". So the wire is asserted
	// too. See TestEmptyListsSerialiseAsListsNotNull.
	if f := p.Findings(); f == nil || len(f) != 0 {
		t.Errorf("findings = %v, want an empty non-nil list", f)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"findings":[]`)) {
		t.Errorf("findings are not [] on the wire: %s", raw)
	}
}

// A default is a definition. Nothing is reported, because nothing is wrong.
func TestDefaultsRaiseNoFinding(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"services:\n  web:\n    image: nginx:${TAG:-latest}\n"), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Findings()) != 0 {
		t.Errorf("findings on a defaulted variable: %+v", p.Findings())
	}
}

// `${VAR:?message}` exists in order to fail, and it fails with the author's
// message — not a paraphrase, because that message is the only explanation
// anybody has of what the variable is for.
func TestRequiredVariableFailsWithTheAuthorsMessage(t *testing.T) {
	for _, form := range []string{"${DB_URL:?set DB_URL in .env}", "${DB_URL?set DB_URL in .env}"} {
		src := "services:\n  web:\n    image: nginx\n    environment:\n      URL: \"" + form + "\"\n"
		p, err := BytesWith("compose.yaml", []byte(src), hermetic(nil))
		if err == nil {
			t.Fatalf("%s resolved successfully; that form exists to fail", form)
		}
		if p != nil {
			t.Error("a model was returned alongside the error (AD-13)")
		}
		var ie *InterpolationError
		if !errors.As(err, &ie) {
			t.Fatalf("%s produced %T, want *InterpolationError", form, err)
		}
		if ie.Variable != "DB_URL" || ie.Message != "set DB_URL in .env" {
			t.Errorf("error = %+v", ie)
		}
		if ie.Line != 5 {
			t.Errorf("error is at line %d, want 5", ie.Line)
		}
		if !errors.Is(err, ErrRequiredVariable) {
			t.Error("the error does not unwrap to ErrRequiredVariable")
		}
	}
}

// An EMPTY value satisfies `?` but not `:?`. Same pairing as `-` and `:-`.
func TestRequiredVariableAndTheEmptyString(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\n    environment:\n      A: \"${E:?needed}\"\n"
	if _, err := BytesWith("compose.yaml", []byte(src), hermetic(map[string]string{"E": ""})); err == nil {
		t.Error("${E:?} accepted an empty value; the colon form treats empty as unset")
	}
	src = "services:\n  web:\n    image: nginx\n    environment:\n      A: \"${E?needed}\"\n"
	if _, err := BytesWith("compose.yaml", []byte(src), hermetic(map[string]string{"E": ""})); err != nil {
		t.Errorf("${E?} rejected a set-but-empty value: %v", err)
	}
}

// Precedence: the environment beats `.env`, which beats `env_file`. Compose's
// own order for the first two; the third is R1.5's addition and sits lowest so
// it can only ever fill in a name that would otherwise be empty.
func TestEnvironmentPrecedence(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env", "SHARED=from-dotenv\nONLY_DOTENV=dotenv\n")
	write(t, dir, "service.env", "SHARED=from-envfile\nONLY_ENVFILE=envfile\n")
	write(t, dir, "compose.yaml", `
services:
  web:
    image: nginx
    env_file:
      - service.env
    environment:
      A: ${SHARED}
      B: ${ONLY_DOTENV}
      C: ${ONLY_ENVFILE}
      D: ${FROM_HOST}
`)
	p, err := Load(Options{
		Files: []string{filepath.Join(dir, "compose.yaml")},
		Env:   map[string]string{"FROM_HOST": "host", "SHARED": ""},
		// The overlay stands in for the host environment here; SHARED is set
		// but EMPTY in it, which must NOT beat .env — an exported empty is
		// what an unset variable looks like to a shell.
		IgnoreHostEnv: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "", "B": "dotenv", "C": "envfile", "D": "host"}
	for k, w := range want {
		v, ok := p.At(Path{"services", "web", "environment", k})
		if !ok {
			t.Errorf("%s did not resolve", k)
			continue
		}
		if v.Scalar() != w {
			t.Errorf("%s = %q, want %q", k, v.Scalar(), w)
		}
	}
	// The env_file source is Compose's own divergence and is recorded, not
	// hidden: this is the list story 1.5's harness reads.
	if got := p.EnvFileSourced(); len(got) != 1 || got[0] != "ONLY_ENVFILE" {
		t.Errorf("EnvFileSourced = %v, want [ONLY_ENVFILE]", got)
	}
}

// AD-11: interpolation is PER FILE. Two files in a chain, each next to its own
// `.env`, resolve the same variable differently, and each value carries the
// origin of the file that interpolated it.
func TestInterpolationIsPerFileBeforeMerge(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, a, ".env", "TAG=from-a\n")
	write(t, b, ".env", "TAG=from-b\n")
	write(t, a, "compose.yaml", "services:\n  web:\n    image: nginx:${TAG}\n  only-a:\n    image: busybox:${TAG}\n")
	write(t, b, "compose.yaml", "services:\n  web:\n    image: nginx:${TAG}\n")

	p, err := Load(Options{
		Files:         []string{filepath.Join(a, "compose.yaml"), filepath.Join(b, "compose.yaml")},
		IgnoreHostEnv: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The winning value came from b and was interpolated against b's .env.
	web, _ := p.At(Path{"services", "web", "image"})
	if web.Scalar() != "nginx:from-b" {
		t.Errorf("web image = %q, want nginx:from-b", web.Scalar())
	}
	if web.Origin().Step != 1 {
		t.Errorf("web image came from step %d, want 1", web.Origin().Step)
	}
	// The value it overrode was interpolated against A's .env — which is the
	// property that would be lost if interpolation ran after the merge.
	ov := web.Overrides()
	if len(ov) != 1 || ov[0].Value != "nginx:from-a" {
		t.Fatalf("override history = %+v, want the value A interpolated", ov)
	}
	if ov[0].Origin.Step != 0 {
		t.Errorf("the overridden value came from step %d, want 0", ov[0].Origin.Step)
	}
	// A service only A declares keeps A's resolution untouched.
	only, _ := p.At(Path{"services", "only-a", "image"})
	if only.Scalar() != "busybox:from-a" {
		t.Errorf("only-a image = %q, want busybox:from-a", only.Scalar())
	}
}

// `.env` syntax, including the inline comment that the differential harness
// against `docker compose config` caught and no unit test had thought to write.
func TestDotEnvSyntax(t *testing.T) {
	src := []byte(`
# a comment line
export EXPORTED=yes
QUOTED="has # hash and spaces"
SINGLE='single quoted'
INLINE=value # this is a comment, not part of the value
NOSPACE=1.1.1.1#5054
TRAILING=value
EMPTY=
`)
	got := ParseDotEnv(src)
	want := map[string]string{
		"EXPORTED": "yes",
		"QUOTED":   "has # hash and spaces",
		"SINGLE":   "single quoted",
		"INLINE":   "value",
		// A `#` with no space before it is part of the value. This is a real
		// line from the corpus — a pihole DNS server on a non-standard port.
		"NOSPACE":  "1.1.1.1#5054",
		"TRAILING": "value",
		"EMPTY":    "",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d entries, want %d: %v", len(got), len(want), got)
	}
}

// The permanent regression file for the inline-comment defect.
func TestDotEnvInlineCommentFixture(t *testing.T) {
	dir := "../../testdata/edge/dotenv-inline-comment"
	p, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(Path{"services", "vpn", "environment", "SERVERURL"})
	if !ok {
		t.Fatal("SERVERURL did not resolve")
	}
	if v.Scalar() != "your-domain.dyndns.com" {
		t.Errorf("SERVERURL = %q; the inline comment leaked into the value", v.Scalar())
	}
}

// An `x-` extension field is interpolated like anything else — it is a value in
// the document, and R1.7 says it survives, not that it is inert.
func TestExtensionFieldsAreInterpolated(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte(
		"x-shared: &s\n  tag: ${TAG}\nservices:\n  web:\n    image: nginx\n"),
		hermetic(map[string]string{"TAG": "1.0"}))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(Path{"x-shared", "tag"})
	if !ok || v.Scalar() != "1.0" {
		t.Errorf("x-shared.tag = %v", v)
	}
}

// write creates a file under dir and returns its path, so a caller that needs
// to name the file in a `-f` chain does not rebuild the join by hand.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ParseDotEnv was line-oriented, and both consequences were silent corruption
// in the layer that feeds every ${VAR}.
func TestParseDotEnvMultiLineAndEscapes(t *testing.T) {
	cases := []struct {
		name string
		src  string
		key  string
		want string
	}{
		{
			// TRUNCATION. `KEY="line one\nline two"` written across two lines
			// parsed to "line one" and the remainder was discarded.
			name: "multi-line double quoted",
			src:  "KEY=\"line one\nline two\"\nAFTER=ok\n",
			key:  "KEY", want: "line one\nline two",
		},
		{
			name: "the entry after a multi-line value is still read",
			src:  "KEY=\"line one\nline two\"\nAFTER=ok\n",
			key:  "AFTER", want: "ok",
		},
		{
			name: "multi-line single quoted",
			src:  "KEY='a\nb'\n",
			key:  "KEY", want: "a\nb",
		},
		{
			// ESCAPES. `\n` in a double-quoted value is a newline to Compose;
			// this yielded four literal characters.
			name: "double-quoted escapes are interpreted",
			src:  `ESC="a\nb"` + "\n",
			key:  "ESC", want: "a\nb",
		},
		{
			name: "tab and backslash",
			src:  `ESC="a\tb\\c"` + "\n",
			key:  "ESC", want: "a\tb\\c",
		},
		{
			name: "an embedded quote",
			src:  `ESC="say \"hi\""` + "\n",
			key:  "ESC", want: `say "hi"`,
		},
		{
			// An escape nothing defines is left as written rather than having
			// its backslash quietly removed. `\d` in a regex is a real thing to
			// find in a .env.
			name: "an undefined escape is left alone",
			src:  `RE="^\d+$"` + "\n",
			key:  "RE", want: `^\d+$`,
		},
		{
			// Single quotes are literal in the shell and here.
			name: "single quotes do not interpret escapes",
			src:  `ESC='a\nb'` + "\n",
			key:  "ESC", want: `a\nb`,
		},
		{
			// A multi-line value in a CRLF file must equal the same value in an
			// LF file, or a checkout on Windows changes what a project resolves
			// to.
			name: "CRLF inside a multi-line value",
			src:  "KEY=\"a\r\nb\"\r\n",
			key:  "KEY", want: "a\nb",
		},
		// The behaviour that already worked, pinned so the rewrite cannot have
		// cost it.
		{name: "inline comment", src: "URL=example.com # a comment\n", key: "URL", want: "example.com"},
		{name: "hash inside a value", src: "DNS=1.1.1.1#5054\n", key: "DNS", want: "1.1.1.1#5054"},
		{name: "quoted hash", src: "P=\"a#b\" # comment\n", key: "P", want: "a#b"},
		{name: "export prefix", src: "export A=1\n", key: "A", want: "1"},
		{name: "empty value", src: "A=\nB=2\n", key: "A", want: ""},
		{name: "value containing equals", src: "A=b=c\n", key: "A", want: "b=c"},
		{
			// An UNTERMINATED quote is a typo, not a value that runs to EOF.
			// Swallowing the rest of the file would take every later entry with
			// it, which is a worse silent loss than the one being fixed.
			name: "unterminated quote does not eat the file",
			src:  "BAD=\"oops\nGOOD=fine\n",
			key:  "GOOD", want: "fine",
		},
		{name: "unterminated quote keeps its own line", src: "BAD=\"oops\nGOOD=fine\n", key: "BAD", want: "oops"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseDotEnv([]byte(c.src))
			if v, ok := got[c.key]; !ok {
				t.Fatalf("%s is absent; parsed %v", c.key, got)
			} else if v != c.want {
				t.Errorf("%s = %q, want %q", c.key, v, c.want)
			}
		})
	}
}

// A multi-line `.env` value reaches interpolation whole. The unit test above
// proves the parser; this proves the wiring, which is where a value gets
// truncated on the way to a ${VAR} in practice.
func TestMultiLineDotEnvValueReachesInterpolation(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env", "CERT=\"-----BEGIN-----\nbody\n-----END-----\"\n")
	entry := write(t, dir, "compose.yaml", "services:\n  web:\n    image: nginx\n    environment:\n      CERT: ${CERT}\n")

	p, err := Load(Options{Files: []string{entry}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.At(ParsePath("services.web.environment.CERT"))
	if !ok {
		t.Fatal("CERT did not resolve")
	}
	if want := "-----BEGIN-----\nbody\n-----END-----"; v.Scalar() != want {
		t.Errorf("CERT = %q, want %q", v.Scalar(), want)
	}
}
