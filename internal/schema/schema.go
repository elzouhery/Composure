// Package schema answers, for any config path in a compose project, which keys
// the specification permits there, which of those the file declares, and
// therefore which it does not.
//
// That last list — `available, not set` — is the product's differentiator, and
// AD-20 makes one rule about it structural: it is GENERATED from
// `schema/compose-spec.json` at runtime, never hand-written. A maintained list
// of "properties you could add" falls behind the Compose specification within
// one release and then confidently tells the reader that a key which exists
// does not. Nothing in this package, and nothing in the extension, contains a
// list of Compose keys. Adding support for a new key is bumping the vendored
// schema.
//
// Two corollaries, both AD-20 and AD-21:
//
//   - There is exactly ONE schema. The file's top-level `version:` field is
//     obsolete and ignored by Compose itself, so it must never be used to
//     select one. It is ignored here too — and reported as a hint by
//     internal/diagnose's obsolete-version-field rule, because a field that is
//     ignored should be deletable rather than mysterious.
//   - Compatibility is judged against the reader's INSTALLED `docker compose`,
//     not the file. Where a key's minimum is known, a key newer than the
//     installed binary is MARKED, never hidden: the reader learns the key
//     exists and that upgrading would give it to them. With no binary present
//     nothing is marked and the whole schema is offered.
//
// The package reads a *resolve.Project and never a file, so the declared /
// not-declared split carries the resolved model's provenance for free.
package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	composespec "github.com/elzouhery/composure/schema"
)

// Spec is the parsed Compose Specification schema.
//
// It is loaded once and shared: the document is 75KB of JSON and immutable
// once parsed, and re-parsing it per request would put a measurable cost on
// every selection change in the inspector.
type Spec struct {
	root     map[string]any
	minVer   map[string]minVersion
	commit   string
	warnings []string
}

type minVersion struct {
	Min   string `json:"min"`
	Scope string `json:"scope"`
	Note  string `json:"note"`
}

var (
	loadOnce sync.Once
	loaded   *Spec
	loadErr  error
)

// Load returns the vendored specification, parsing it at most once.
func Load() (*Spec, error) {
	loadOnce.Do(func() {
		loaded, loadErr = parse(composespec.Spec, composespec.MinVersions, composespec.Commit)
	})
	return loaded, loadErr
}

func parse(specJSON, minJSON []byte, commit string) (*Spec, error) {
	var root map[string]any
	if err := json.Unmarshal(specJSON, &root); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSchemaUnreadable, err)
	}
	if _, ok := root["properties"].(map[string]any); !ok {
		// A schema with no top-level properties would produce an empty
		// available list on every node — the inspector's differentiator
		// silently switched off. Refuse instead (CLAUDE.md rule 6).
		return nil, fmt.Errorf("%w: the document has no top-level \"properties\"", ErrSchemaUnreadable)
	}
	s := &Spec{root: root, commit: commit, minVer: map[string]minVersion{}}

	var mins struct {
		Keys map[string]minVersion `json:"keys"`
	}
	if err := json.Unmarshal(minJSON, &mins); err != nil {
		return nil, fmt.Errorf("%w: min-version table: %v", ErrSchemaUnreadable, err)
	}
	for k, v := range mins.Keys {
		s.minVer[k] = v
	}
	return s, nil
}

// Commit is the upstream revision the schema was vendored at.
func (s *Spec) Commit() string { return s.commit }

// deref follows `$ref` until it reaches a node that is not one. Only local
// refs (`#/$defs/x`) exist in this document; a remote ref would need a fetch
// and this package does no I/O.
func (s *Spec) deref(n map[string]any) map[string]any {
	for i := 0; i < 16; i++ { // bounded: a self-referential $ref must not hang
		ref, ok := n["$ref"].(string)
		if !ok {
			return n
		}
		target, ok := s.resolveRef(ref)
		if !ok {
			return n
		}
		n = target
	}
	return n
}

func (s *Spec) resolveRef(ref string) (map[string]any, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	cur := any(s.root)
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[unescapePointer(seg)]
		if !ok {
			return nil, false
		}
	}
	out, ok := cur.(map[string]any)
	return out, ok
}

// unescapePointer applies RFC 6901's two escapes. Compose's own schema uses
// neither, but a pointer containing a literal `/` would otherwise silently
// resolve to nothing, which is a wrong answer rather than a failure.
func unescapePointer(seg string) string {
	seg = strings.ReplaceAll(seg, "~1", "/")
	return strings.ReplaceAll(seg, "~0", "~")
}

// branches flattens a node into every object-shaped alternative it stands for:
// itself, plus each arm of oneOf / anyOf / allOf, each dereferenced.
//
// Compose's schema leans on oneOf heavily — `build` is "a string, or an object
// with these fourteen keys" — and a reader that only looked at the top node
// would offer nothing at all for those. The union is the honest answer to
// "what may be written here".
func (s *Spec) branches(n map[string]any) []map[string]any {
	var out []map[string]any
	// Depth-bounded rather than visited-set-guarded: map identity is not
	// comparable in Go, and the specification's composition nests three deep
	// at worst.
	var walk func(map[string]any, int)
	walk = func(node map[string]any, depth int) {
		if node == nil || depth > 6 {
			return
		}
		node = s.deref(node)
		out = append(out, node)
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			arms, ok := node[key].([]any)
			if !ok {
				continue
			}
			for _, arm := range arms {
				if m, ok := arm.(map[string]any); ok {
					walk(m, depth+1)
				}
			}
		}
	}
	walk(n, 0)
	return out
}

// Child returns the schema node addressed by one path segment, and whether the
// specification describes it at all.
//
// `known` false is a real answer and not a failure: `x-` extensions, and any
// key a newer file uses that this vendored schema predates, land here. The
// inspector still shows what the file declares; it just cannot say what else
// is permitted, and says so rather than showing an empty list that reads as
// "nothing else is possible".
func (s *Spec) Child(n map[string]any, seg string) (map[string]any, bool) {
	for _, b := range s.branches(n) {
		if props, ok := b["properties"].(map[string]any); ok {
			if child, ok := props[seg].(map[string]any); ok {
				return s.deref(child), true
			}
		}
		if pp, ok := b["patternProperties"].(map[string]any); ok {
			for pattern, child := range pp {
				re, err := regexp.Compile(pattern)
				if err != nil || !re.MatchString(seg) {
					continue
				}
				if m, ok := child.(map[string]any); ok {
					return s.deref(m), true
				}
			}
		}
		if items, ok := b["items"].(map[string]any); ok && isIndex(seg) {
			return s.deref(items), true
		}
		if ap, ok := b["additionalProperties"].(map[string]any); ok {
			return s.deref(ap), true
		}
	}
	return nil, false
}

// At walks a whole config path from the document root.
func (s *Spec) At(path []string) (map[string]any, bool) {
	cur := s.root
	for _, seg := range path {
		next, ok := s.Child(cur, seg)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// PropertyNames lists every key the node permits, in the specification's own
// alphabetical order.
//
// Sorted rather than left in map order because Go randomises map iteration and
// an `available, not set` list that reshuffles between two draws of the same
// file is unreadable. The specification's JSON is itself alphabetical, so this
// is also the order a reader comparing against the spec expects.
func (s *Spec) PropertyNames(n map[string]any) []string {
	set := map[string]bool{}
	for _, b := range s.branches(n) {
		props, ok := b["properties"].(map[string]any)
		if !ok {
			continue
		}
		for k := range props {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// property returns the schema for one named key of a node.
func (s *Spec) property(n map[string]any, key string) (map[string]any, bool) {
	for _, b := range s.branches(n) {
		props, ok := b["properties"].(map[string]any)
		if !ok {
			continue
		}
		if child, ok := props[key].(map[string]any); ok {
			return s.deref(child), true
		}
	}
	return nil, false
}

// FreeForm reports whether a node accepts keys the schema does not name —
// `environment`, `labels`, `driver_opts`.
//
// The distinction decides whether an `available, not set` list means anything.
// For a free-form map the honest list is empty: the schema permits any key at
// all, and offering "these six" would be an invention. The inspector renders
// what is declared and says nothing about what is not.
func (s *Spec) FreeForm(n map[string]any) bool {
	named := len(s.PropertyNames(n)) > 0
	for _, b := range s.branches(n) {
		// The empty schema, `{}` — which is what the specification says about
		// an `x-` extension's contents. It permits anything, so there is
		// nothing to offer and saying "0 available" would read as "nothing
		// else is possible" rather than "anything is".
		if !named && len(b) == 0 {
			return true
		}
		if _, ok := b["patternProperties"].(map[string]any); ok && !named {
			return true
		}
		switch ap := b["additionalProperties"].(type) {
		case bool:
			if ap && !named {
				return true
			}
		case map[string]any:
			if !named {
				return true
			}
		}
	}
	return false
}

// isIndex reports whether a path segment is a sequence index.
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
