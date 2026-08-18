// Package corpus collects docker-compose files to measure round-trip fidelity against.
//
// Two sources matter and they measure different things:
//
//	testdata/adversarial — hand-written files that each stress one fidelity
//	  dimension (comments, anchors, merge keys, quoting, blank lines, numeric
//	  keys). These isolate failure modes so you know WHAT breaks.
//
//	corpus-repos/<repo>        — real compose files harvested from public repositories.
//	  These tell you HOW OFTEN it breaks in the wild, which is the number that
//	  decides whether the product is viable.
package corpus

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repos are public repositories that contain a useful density of real compose files.
var Repos = []string{
	"https://github.com/docker/awesome-compose",
	"https://github.com/dockersamples/example-voting-app",
	"https://github.com/immich-app/immich",
	"https://github.com/paperless-ngx/paperless-ngx",
	"https://github.com/nextcloud/docker",
	"https://github.com/getsentry/self-hosted",
	"https://github.com/gitlabhq/gitlabhq",
	"https://github.com/n8n-io/n8n",
	"https://github.com/grafana/grafana",
	"https://github.com/apache/airflow",
}

var composeNames = []string{
	"docker-compose.yml", "docker-compose.yaml",
	"compose.yml", "compose.yaml",
}

// IsCompose reports whether a filename looks like a compose file, including
// the common override and environment-suffixed variants.
func IsCompose(name string) bool {
	lower := strings.ToLower(name)
	for _, n := range composeNames {
		if lower == n {
			return true
		}
	}
	// docker-compose.prod.yml, compose.override.yaml, docker-compose-dev.yml ...
	if (strings.HasPrefix(lower, "docker-compose") || strings.HasPrefix(lower, "compose")) &&
		(strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")) {
		return true
	}
	return false
}

// Collect walks root and returns every compose file path found.
func Collect(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees rather than aborting the walk
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if IsCompose(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// Fetch shallow-clones the repo list into dir. Existing clones are skipped.
func Fetch(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, url := range Repos {
		name := strings.TrimSuffix(filepath.Base(url), ".git")
		dest := filepath.Join(dir, name)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		cmd := exec.Command("git", "clone", "--depth", "1", "--filter=blob:none", url, dest)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			// A single unreachable repo should not sink the run.
			continue
		}
	}
	return nil
}
