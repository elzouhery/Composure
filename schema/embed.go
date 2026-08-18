// Package composespec carries the vendored Compose Specification JSON Schema
// and the small amount of metadata that says where it came from.
//
// It holds bytes and constants, and nothing else. Every decision derived from
// the schema — which keys a path permits, which of those a file declares, what
// a key defaults to — lives in internal/schema. This package exists only
// because `go:embed` cannot reach outside its own directory, and AD-20 names
// the file's location: `schema/compose-spec.json`, vendored from the
// compose-spec project at a pinned commit.
//
// Bumping the schema is bumping Commit and the file together. It is never an
// edit to the UI: a hand-maintained list of "properties you could add" falls
// behind the specification within one release and starts lying about what is
// possible, which is the failure AD-20 exists to prevent.
package composespec

import _ "embed"

// Spec is the Compose Specification JSON Schema, verbatim.
//
//go:embed compose-spec.json
var Spec []byte

// MinVersions records, per key, the earliest Compose that understands it.
//
//go:embed compose-min-version.json
var MinVersions []byte

const (
	// Source is the upstream project.
	Source = "https://github.com/compose-spec/compose-spec"
	// Commit pins exactly which revision of schema/compose-spec.json is
	// vendored here. It is the answer to "which spec is this list from",
	// which is a question the inspector's credibility rests on.
	Commit = "4e2fe7602af8c965ab4fef891e9dde9c5940775f"
	// Retrieved is when the file above was fetched, ISO-8601.
	Retrieved = "2026-08-12"
	// Licence is the upstream licence. Apache-2.0 is on the CLEANROOM.md
	// allow-list; the licence gate walks the Go build graph and cannot see a
	// vendored data file, so the fact is recorded here and in
	// schema/PROVENANCE.md rather than relied upon to be scanned.
	Licence = "Apache-2.0"
)
