package diagnose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// project writes a compose file and any sibling files into a temp directory and
// returns the compose file's path.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "compose.yaml")
}

func findingsOf(t *testing.T, path, rule string) []Finding {
	t.Helper()
	rep, err := Analyze(path, nil)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var out []Finding
	for _, f := range rep.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// Story 6.3's last acceptance criterion: a build.dockerfile pointing at a file
// that does not exist raises a finding. We do not silently omit it.
func TestMissingDockerfileIsReported(t *testing.T) {
	path := project(t, map[string]string{
		"compose.yaml": "services:\n  api:\n    build:\n      context: ./api\n      dockerfile: Dockerfile.dev\n",
		"api/keep":     "",
	})
	got := findingsOf(t, path, "build-dockerfile-missing")
	if len(got) != 1 {
		t.Fatalf("%d findings, want 1", len(got))
	}
	if !strings.Contains(got[0].Message, "Dockerfile.dev") {
		t.Errorf("the message does not name the file: %s", got[0].Message)
	}
	// AD-7: it says where the build is declared, so the reader can go there.
	if len(got[0].Anchors) == 0 || got[0].Anchors[0].Origin.Line < 1 {
		t.Errorf("the finding is not anchored: %+v", got[0].Anchors)
	}
	if got[0].Fix != nil || got[0].NoFix == "" {
		t.Error("creating a file is not a splice operation and must not be offered as a fix")
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("severity is %v; a stack whose images are already built still runs", got[0].Severity)
	}
}

func TestPresentDockerfileIsNotReported(t *testing.T) {
	path := project(t, map[string]string{
		"compose.yaml":   "services:\n  api:\n    build: ./api\n",
		"api/Dockerfile": "FROM alpine\n",
	})
	if got := findingsOf(t, path, "build-dockerfile-missing"); len(got) != 0 {
		t.Errorf("%d findings on a build whose Dockerfile is there: %+v", len(got), got)
	}
}

// The rule stays silent wherever the answer would be a guess. Each of these
// names a path that was never meant to be read from this disk, and "that file
// does not exist" would be a confident wrong answer about all of them.
func TestMissingDockerfileRuleStaysSilentWhereItCannotKnow(t *testing.T) {
	cases := map[string]string{
		"a git context":            "services:\n  api:\n    build: https://github.com/x/y.git\n",
		"an uninterpolated ${VAR}": "services:\n  api:\n    build:\n      context: ${CTX}/api\n",
		"an inline Dockerfile":     "services:\n  api:\n    build:\n      dockerfile_inline: |\n        FROM alpine\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := project(t, map[string]string{"compose.yaml": body})
			if got := findingsOf(t, path, "build-dockerfile-missing"); len(got) != 0 {
				t.Errorf("%d findings: %+v", len(got), got)
			}
		})
	}
}

// A directory where a Dockerfile should be is still missing: `docker build`
// cannot read a directory as a Dockerfile, and reporting nothing would leave
// the reader with a build that fails and a graph that says everything is fine.
func TestADirectoryInTheDockerfilePositionIsMissing(t *testing.T) {
	path := project(t, map[string]string{
		"compose.yaml":         "services:\n  api:\n    build: ./api\n",
		"api/Dockerfile/inner": "",
	})
	if got := findingsOf(t, path, "build-dockerfile-missing"); len(got) != 1 {
		t.Errorf("%d findings, want 1", len(got))
	}
}
