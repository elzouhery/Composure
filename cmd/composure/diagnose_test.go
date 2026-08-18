package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/elzouhery/composure/internal/diagnose"
)

// diagnoseFixture trips three rules at three severities, so the exit-code
// mapping can be checked at every level from one file.
const diagnoseFixture = `services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
    environment:
      APP_SECRET: hunter2
  api:
    image: api
    ports:
      - "8080:3000"
volumes:
  orphan:
`

const cleanFixture = `services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
  api:
    image: api
`

// N5: the exit code is the whole reason this command is usable in CI. A clean
// stack is not an error, and a stack with findings reports the worst of them.
func TestDiagnoseExitCodeFollowsSeverity(t *testing.T) {
	cases := []struct {
		name     string
		findings []diagnose.Finding
		want     int
	}{
		{"clean", nil, 0},
		{"hint", []diagnose.Finding{{Severity: diagnose.SeverityHint}}, exitHint},
		{"warning", []diagnose.Finding{
			{Severity: diagnose.SeverityHint},
			{Severity: diagnose.SeverityWarning},
		}, exitWarning},
		{"error", []diagnose.Finding{
			{Severity: diagnose.SeverityWarning},
			{Severity: diagnose.SeverityError},
			{Severity: diagnose.SeverityHint},
		}, exitError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exitCodeFor(&diagnose.Report{Findings: c.findings}); got != c.want {
				t.Errorf("exit code %d, want %d", got, c.want)
			}
		})
	}
}

// The exit codes must not collide with the codes the rest of the CLI uses for
// an operational failure (1) or a usage error (2), or a CI job cannot tell a
// warning from a file that would not parse.
func TestDiagnoseExitCodesAreDistinctFromFailureCodes(t *testing.T) {
	for _, code := range []int{exitHint, exitWarning, exitError} {
		if code == 0 || code == 1 || code == 2 {
			t.Errorf("finding exit code %d collides with an operational or usage code", code)
		}
	}
}

func TestDiagnoseCleanStackHasNoFindings(t *testing.T) {
	path := writeFixture(t, "compose.yaml", cleanFixture)
	rep, err := diagnose.Analyze(path, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("clean stack produced findings: %+v", rep.Findings)
	}
	if exitCodeFor(rep) != 0 {
		t.Errorf("clean stack exits %d", exitCodeFor(rep))
	}
}

// A file that will not parse is an error, never a finding (AD-13).
func TestDiagnoseRefusesAnUnparseableFile(t *testing.T) {
	path := writeFixture(t, "compose.yaml", "services:\n  web:\n   - broken: [\n")
	if _, err := diagnose.Analyze(path, nil); err == nil {
		t.Fatal("an unparseable file produced a report instead of an error")
	}
}

// ---------------------------------------------------------------------------
// The RPC surface.

func TestServeDiagnoseMatchesLibraryOutput(t *testing.T) {
	path := writeFixture(t, "compose.yaml", diagnoseFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/diagnose","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/diagnose returned an error: %+v", resp.Error)
	}

	rep, err := diagnose.Analyze(path, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	want, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, resp.Result, want) {
		t.Errorf("RPC payload differs from the library marshaller\n rpc: %s\n lib: %s", resp.Result, want)
	}
}

// The literal wire keys a client navigates by name. Nothing in the Go build
// fails when one of these changes; the panel simply shows no findings.
func TestServeDiagnoseWireKeys(t *testing.T) {
	path := writeFixture(t, "compose.yaml", diagnoseFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/diagnose","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("stack/diagnose returned an error: %+v", resp.Error)
	}

	var payload map[string]any
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, key := range []string{"path", "profiles", "findings", "rules"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload has no %q", key)
		}
	}
	findings, ok := payload["findings"].([]any)
	if !ok || len(findings) == 0 {
		t.Fatalf("findings is %T with no entries", payload["findings"])
	}
	first, _ := findings[0].(map[string]any)
	for _, key := range []string{"rule", "severity", "title", "message", "subjects", "anchors"} {
		if _, ok := first[key]; !ok {
			t.Errorf("finding has no %q", key)
		}
	}
	if sev, _ := first["severity"].(string); sev == "" {
		t.Errorf("severity is %v, want a name like \"error\"", first["severity"])
	}
	anchors, ok := first["anchors"].([]any)
	if !ok || len(anchors) == 0 {
		t.Fatal("the first finding carries no anchors (AD-7)")
	}
	anchor, _ := anchors[0].(map[string]any)
	for _, key := range []string{"label", "path", "origin"} {
		if _, ok := anchor[key]; !ok {
			t.Errorf("anchor has no %q", key)
		}
	}
	origin, _ := anchor["origin"].(map[string]any)
	for _, key := range []string{"file", "line", "column", "step"} {
		if _, ok := origin[key]; !ok {
			t.Errorf("origin has no %q", key)
		}
	}
}

// Findings are never a JSON-RPC error, however severe (AD-13). Only a file the
// resolver refuses is an error, and it carries a position.
func TestServeDiagnoseFindingsAreNotErrors(t *testing.T) {
	path := writeFixture(t, "compose.yaml", diagnoseFixture)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/diagnose","params":{"path":%q}}`, path))
	resp := s.read()
	if resp.Error != nil {
		t.Fatalf("a stack with findings came back as an RPC error: %+v", resp.Error)
	}

	bad := writeFixture(t, "broken.yaml", "services:\n  web:\n   - broken: [\n")
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"stack/diagnose","params":{"path":%q}}`, bad))
	resp = s.read()
	if resp.Error == nil {
		t.Fatal("an unparseable file came back as a result")
	}
	if resp.Error.Code != codeResolveFailed {
		t.Errorf("error code %d, want %d", resp.Error.Code, codeResolveFailed)
	}
}

func TestServeDiagnoseHonoursProfiles(t *testing.T) {
	const src = `services:
  web:
    image: nginx
    ports:
      - "8080:80"
  debug:
    image: debug
    profiles: [debug]
    ports:
      - "8080:9229"
`
	path := writeFixture(t, "compose.yaml", src)

	s := start(t)
	s.handshake(1)
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"stack/diagnose","params":{"path":%q}}`, path))
	if got := countFindings(t, s.read().Result, "host-port-collision"); got != 0 {
		t.Errorf("%d collisions with no profile active, want 0", got)
	}
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"stack/diagnose","params":{"path":%q,"profiles":["debug"]}}`, path))
	if got := countFindings(t, s.read().Result, "host-port-collision"); got != 1 {
		t.Errorf("%d collisions with the debug profile active, want 1", got)
	}
}

func TestServeDiagnoseRequiresAPath(t *testing.T) {
	s := start(t)
	s.handshake(1)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"stack/diagnose","params":{}}`)
	resp := s.read()
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Fatalf("want invalid params, got %+v", resp.Error)
	}
}

func countFindings(t *testing.T, raw json.RawMessage, rule string) int {
	t.Helper()
	var rep struct {
		Findings []struct {
			Rule string `json:"rule"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	n := 0
	for _, f := range rep.Findings {
		if f.Rule == rule {
			n++
		}
	}
	return n
}
