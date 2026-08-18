package diagnose

import (
	"strings"
	"testing"
)

const versionRule = "obsolete-version-field"

func TestObsoleteVersionFieldIsAHint(t *testing.T) {
	rep := run(t, `
version: "3.8"
services:
  web:
    image: nginx
`)
	got := wantCount(t, rep, versionRule, 1)[0]
	if got.Severity != SeverityHint {
		t.Errorf("severity is %s, want hint — nothing is broken by this line", got.Severity)
	}
	if !strings.Contains(got.Message, "3.8") {
		t.Errorf("the message does not quote the declared value: %s", got.Message)
	}
	if !strings.Contains(got.Message, "deleted") {
		t.Errorf("the message does not say what to do about it: %s", got.Message)
	}
	if len(got.Anchors) != 1 || got.Anchors[0].Origin.Line != 2 {
		t.Fatalf("anchors are %+v, want one at line 2", got.Anchors)
	}
	if got.Fix == nil {
		t.Fatalf("no fix offered: %s", got.NoFix)
	}
	if got.Fix.Operation != FixDeleteKey {
		t.Errorf("fix is %s, want a delete", got.Fix.Operation)
	}
	if got.Fix.Range.End <= got.Fix.Range.Start {
		t.Errorf("fix range %+v removes nothing", got.Fix.Range)
	}
}

// The overwhelmingly common case is a file with no version field at all, and
// a rule that fired on it would put a hint on every clean project.
func TestNoVersionFieldIsSilent(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
`)
	wantCount(t, rep, versionRule, 0)
}
