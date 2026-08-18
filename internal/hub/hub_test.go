package hub

import "testing"

// The tag-semantics cases previously lived in cmd/probe as a hand-rolled main
// that printed failures and exited 0 — so nothing gated on them and CI never
// ran them. They are the rules that decide what counts as an upgrade, which is
// the hard part of image discovery: recommend `alpine:edge` over `alpine:3.19`
// once and the user never trusts the feature again.
func TestIsUnstable(t *testing.T) {
	cases := map[string]bool{
		// Prerelease markers wedged between digits with no separator — the
		// case that slipped past the separator checks and motivated the
		// digit-prefix rule.
		"1.27rc2-alpine3.24": true,
		"3.0alpha":           true,
		"2.0.0-beta1":        true,
		"1.0.0-rc.1":         true,

		// Rolling and nightly builds: strictly worse than the pin they replace.
		"edge":       true,
		"tip-alpine": true,

		// Not unstable, but never a pin — offering it loses determinism.
		"latest": true,

		// Ordinary pinned tags.
		"3.19":          false,
		"1.24-alpine":   false,
		"3.20.1-alpine": false,
		"22-bookworm":   false,
		"16.1":          false,

		// A bare date stamp is a real release, not a prerelease.
		"20260805": false,

		// "arch" contains "rc"; the digit-prefix rule must not catch it.
		"8.0-arch": false,
	}

	for tag, want := range cases {
		if got := IsUnstable(tag); got != want {
			t.Errorf("IsUnstable(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestIsDateTag(t *testing.T) {
	cases := map[string]bool{
		"20260805":    true,
		"2026-08-05":  true,
		"3.19":        false, // too few digits to be a date stamp
		"1.24-alpine": false, // contains letters
	}

	for tag, want := range cases {
		if got := IsDateTag(tag); got != want {
			t.Errorf("IsDateTag(%q) = %v, want %v", tag, got, want)
		}
	}
}
