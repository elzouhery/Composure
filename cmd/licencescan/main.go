// Command licencescan enforces CLEANROOM.md rule 5: no BSL, SSPL, Elastic
// License or AGPL anywhere in the dependency tree.
//
// The stakes are asymmetric and worth restating. BSL 1.1 termination is
// automatic and total — a single violation ends the rights to every version,
// retroactively. A cheap gate that runs on every push is the correct trade.
//
// The scan works from `go list -deps -json` over the real build graph, so it
// sees what actually links into the binaries rather than what go.mod happens to
// mention. Every module is resolved to a licence by reading the licence file in
// its module cache directory; a module whose licence cannot be determined is a
// failure, not a pass, because an unreadable licence is exactly what an
// incompatible one looks like when the scan is written optimistically.
//
// Usage:
//
//	licencescan                 scan ./cmd/... ./internal/...
//	licencescan -v              also list every allowed module
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// forbidden are the licence families that CLEANROOM.md rule 5 bans outright.
// Each entry is matched case-insensitively against the module's licence text.
var forbidden = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"BSL / Business Source License", regexp.MustCompile(`(?i)business source license|\bBUSL-1\.1\b|\bBSL-1\.1\b`)},
	{"SSPL / Server Side Public License", regexp.MustCompile(`(?i)server side public license|\bSSPL\b`)},
	{"Elastic License", regexp.MustCompile(`(?i)elastic license`)},
	{"AGPL", regexp.MustCompile(`(?i)affero general public license|\bAGPL\b`)},
}

// allowed are the permissive families CLEANROOM.md rule 5 permits: MIT,
// Apache-2.0, BSD, ISC. Matched only after the forbidden check, so a file that
// somehow mentions both fails.
var allowed = []struct {
	name    string
	pattern *regexp.Regexp
}{
	// (?s) so that `.` spans the newline in the conventional "Apache License\n
	// Version 2.0, January 2004" header.
	{"Apache-2.0", regexp.MustCompile(`(?is)apache license.{0,40}version 2\.0|(?i)\bApache-2\.0\b`)},
	{"MIT", regexp.MustCompile(`(?i)\bMIT License\b|Permission is hereby granted, free of charge`)},
	{"BSD", regexp.MustCompile(`(?i)redistribution and use in source and binary forms|\bBSD-[23]-Clause\b`)},
	{"ISC", regexp.MustCompile(`(?i)\bISC License\b|Permission to use, copy, modify, and(/or)? distribute this software`)},
	{"MPL-2.0", regexp.MustCompile(`(?is)mozilla public license.{0,40}version 2\.0|(?i)\bMPL-2\.0\b`)},
}

// licenceFileNames are the conventional names, in the order they are preferred.
var licenceFileNames = []string{
	"LICENSE", "LICENCE", "LICENSE.md", "LICENCE.md", "LICENSE.txt", "LICENCE.txt",
	"COPYING", "COPYING.md", "LICENSE-APACHE", "LICENSE-MIT",
}

type module struct {
	Path    string  `json:"Path"`
	Version string  `json:"Version"`
	Dir     string  `json:"Dir"`
	Main    bool    `json:"Main"`
	Replace *module `json:"Replace"`
}

type pkg struct {
	Standard bool    `json:"Standard"`
	Module   *module `json:"Module"`
}

type verdict struct {
	module  string
	version string
	licence string
	reason  string
}

func main() {
	verbose := flag.Bool("v", false, "list every allowed module, not just failures")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		// Deliberately not ./... — the fetched corpus lives inside the module
		// directory and contains thousands of unrelated third-party Go files.
		patterns = []string{"./cmd/...", "./internal/..."}
	}

	mods, err := dependencyModules(patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "licencescan: %v\n", err)
		os.Exit(1)
	}

	var ok, bad []verdict
	for _, m := range mods {
		v := classify(m)
		if v.reason == "" {
			ok = append(ok, v)
		} else {
			bad = append(bad, v)
		}
	}

	fmt.Printf("\nLICENCE SCAN — %d third-party module(s) in the build graph\n", len(mods))
	fmt.Println(strings.Repeat("=", 78))
	if *verbose || len(bad) > 0 {
		for _, v := range ok {
			fmt.Printf("  ok    %-48s %-12s %s\n", v.module, v.version, v.licence)
		}
	}
	for _, v := range bad {
		fmt.Printf("  FAIL  %-48s %-12s %s\n", v.module, v.version, v.reason)
	}

	if len(bad) > 0 {
		fmt.Fprintf(os.Stderr, `
LICENCE SCAN FAILED — %d module(s) violate CLEANROOM.md rule 5.

Permitted: MIT, Apache-2.0, BSD, ISC, MPL-2.0.
Banned:    BSL, SSPL, Elastic License, AGPL.

BSL termination is automatic, total and retroactive across every version.
Remove the dependency or replace it. Do not add it to an ignore list.
`, len(bad))
		os.Exit(1)
	}

	fmt.Printf("\nLICENCE SCAN PASSED — all %d module(s) permissively licensed\n", len(ok))
}

// dependencyModules returns every non-stdlib, non-main module reachable from
// the given package patterns, deduplicated and sorted.
func dependencyModules(patterns []string) ([]module, error) {
	args := append([]string{"list", "-deps", "-json"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}

	seen := map[string]module{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decoding go list output: %w", err)
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		m := *p.Module
		if m.Replace != nil {
			m = *m.Replace // a replace directive is what actually builds
		}
		seen[m.Path] = m
	}

	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// classify reads a module's licence file and decides. An empty reason means the
// module passed.
func classify(m module) verdict {
	v := verdict{module: m.Path, version: m.Version}

	if m.Dir == "" {
		v.reason = "module not downloaded — run `go mod download` before scanning"
		return v
	}

	path, text := readLicence(m.Dir)
	if path == "" {
		// Fail closed. An unreadable licence looks identical to an
		// incompatible one when the scan is written to give benefit of doubt.
		v.reason = "no licence file found in " + m.Dir
		return v
	}

	for _, f := range forbidden {
		if f.pattern.MatchString(text) {
			v.reason = "FORBIDDEN: " + f.name + " (" + filepath.Base(path) + ")"
			return v
		}
	}
	for _, a := range allowed {
		if a.pattern.MatchString(text) {
			v.licence = a.name
			return v
		}
	}
	v.reason = "unrecognised licence in " + path + " — classify it by hand and extend licencescan"
	return v
}

func readLicence(dir string) (string, string) {
	for _, name := range licenceFileNames {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err == nil && len(data) > 0 {
			return p, string(data)
		}
	}
	return "", ""
}
