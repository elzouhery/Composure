package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A licence gate that cannot fail is decoration. These cases assert that it
// fires on each banned family, on an unreadable licence, and on an
// unrecognised one — and stays quiet on the permissive families.
func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		licence  string
		wantPass bool
		wantWord string // substring expected in the failure reason
	}{
		{
			name:     "BSL 1.1",
			licence:  "Business Source License 1.1\n\nLicensor: Example Ltd",
			wantWord: "BSL",
		},
		{
			name:     "SSPL",
			licence:  "SERVER SIDE PUBLIC LICENSE\nVersion 1, October 16, 2018",
			wantWord: "SSPL",
		},
		{
			name:     "Elastic License",
			licence:  "Elastic License 2.0\n\nAcceptance",
			wantWord: "Elastic",
		},
		{
			name:     "AGPL",
			licence:  "GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007",
			wantWord: "AGPL",
		},
		{
			name:     "unrecognised text",
			licence:  "You may use this software if you send the author a postcard.",
			wantWord: "unrecognised",
		},
		{
			name:     "Apache-2.0",
			licence:  "Apache License\nVersion 2.0, January 2004",
			wantPass: true,
		},
		{
			name:     "MIT",
			licence:  "MIT License\n\nPermission is hereby granted, free of charge, to any person",
			wantPass: true,
		},
		{
			name:     "BSD-3-Clause",
			licence:  "Redistribution and use in source and binary forms, with or without modification",
			wantPass: true,
		},
		{
			name:     "ISC",
			licence:  "ISC License\n\nPermission to use, copy, modify, and distribute this software",
			wantPass: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(tc.licence), 0o644); err != nil {
				t.Fatal(err)
			}
			got := classify(module{Path: "example.com/dep", Version: "v1.0.0", Dir: dir})
			if tc.wantPass {
				if got.reason != "" {
					t.Fatalf("expected pass, got failure: %s", got.reason)
				}
				if got.licence == "" {
					t.Fatal("passed but did not name the licence")
				}
				return
			}
			if got.reason == "" {
				t.Fatalf("expected failure, but the module passed as %q", got.licence)
			}
			if !strings.Contains(got.reason, tc.wantWord) {
				t.Fatalf("reason %q does not mention %q", got.reason, tc.wantWord)
			}
		})
	}
}

// A module with no licence file at all must fail. An unreadable licence looks
// exactly like an incompatible one, so the scan fails closed.
func TestClassifyMissingLicenceFails(t *testing.T) {
	got := classify(module{Path: "example.com/dep", Version: "v1.0.0", Dir: t.TempDir()})
	if got.reason == "" {
		t.Fatal("a module with no licence file must not pass the scan")
	}
	if !strings.Contains(got.reason, "no licence file") {
		t.Fatalf("unexpected reason: %s", got.reason)
	}
}

// A module that was never downloaded must fail rather than be skipped.
func TestClassifyUndownloadedFails(t *testing.T) {
	got := classify(module{Path: "example.com/dep", Version: "v1.0.0"})
	if got.reason == "" {
		t.Fatal("an undownloaded module must not pass the scan")
	}
}
