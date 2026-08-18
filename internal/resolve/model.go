package resolve

import (
	"encoding/json"
)

// Kind distinguishes the four shapes a resolved value can take.
type Kind uint8

const (
	// KindNull is an explicitly null value — `command:` with nothing after it.
	// It is distinct from a key being absent, which the file also expresses
	// and which a form has to render differently.
	KindNull Kind = iota
	KindScalar
	KindSequence
	KindMapping
	// KindAlias was an unfollowed `*anchor` reference. Story 1.2 expands
	// aliases, so the resolver no longer produces this kind: an alias resolves
	// to its anchor's value and reports that value's kind, carrying the anchor
	// name and the reference site alongside — see Value.Alias and
	// Value.AliasSite.
	//
	// The constant is kept rather than removed. It is part of the JSON wire
	// contract the extension is written against, and a consumer that switches
	// on kind should keep compiling and keep handling the case.
	KindAlias
)

func (k Kind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindScalar:
		return "scalar"
	case KindSequence:
		return "sequence"
	case KindMapping:
		return "mapping"
	case KindAlias:
		return "alias"
	}
	return "unknown"
}

// Value is a node in the resolved model, carrying its own provenance.
//
// The model is one Value tree rather than a struct per Compose key. A typed
// struct would need the whole Compose schema before anything could resolve,
// and could not hold an origin per leaf without wrapping every field anyway.
// A three-shape union carries provenance uniformly, survives keys the schema
// has not caught up with — `x-` extensions among them — and lets the schema
// layer arrive later without touching this type.
//
// Fields are unexported and there are no setters: a resolved model is
// immutable. An edit produces new bytes through the splice engine and the
// caller re-resolves. A mutable model is how re-serialisation gets back in.
type Value struct {
	kind      Kind
	scalar    string
	seq       []*Value
	mapping   *OrderedMap
	origin    Origin
	overrides []Override

	// A value that arrived through an alias has two positions, and both are
	// load-bearing. origin is the ANCHOR's definition site, because that is
	// where the bytes actually are: an editor that wants to change the value
	// must go there, and an inspector that reported the alias site would send
	// it to `*base`, which is not a value at all.
	//
	// But the reference site is what the reader is looking at. `deploy: *base`
	// on line 40 has to lead back to line 40, and for a merge key the answer to
	// "why does this service have a `networks:` I cannot see" IS the position of
	// the `<<: *defaults`. So the two are carried side by side rather than one
	// displacing the other: origin stays the anchor, aliasSite records the
	// reference. Neither is derivable from the other.
	//
	// For a merge key, aliasSite is set on each key the anchor contributed —
	// the point of expansion. Deeper descendants do not repeat it; they are
	// reached through a value that has it.
	aliasName string
	aliasSite Origin
	fromAlias bool

	// raw is the scalar exactly as the file writes it, before interpolation.
	// scalar is the interpolated result. They differ only when the text held a
	// variable reference.
	//
	// Both are kept because both are load-bearing and neither is derivable
	// from the other. The splice engine edits `${DB_PASSWORD}` — the bytes
	// that are there — while the reader needs to see what it resolved to, and
	// the credential rule must be able to tell "hunter2 is written in this
	// file" from "a variable that happens to be set to hunter2 today".
	raw       string
	wasInterp bool

	// resetTag and overrideTag are the two Compose merge tags. `!reset`
	// removes a declaration; `!override` bypasses the merge rule for its key.
	// They are directives about the merge rather than configuration, so they
	// live as flags and never reach the wire as values.
	resetTag    bool
	overrideTag bool
}

func newValue(kind Kind, origin Origin) *Value {
	// overrides is allocated empty rather than left nil so that "never
	// overridden" is a present, empty list at every layer including JSON.
	return &Value{kind: kind, origin: origin, overrides: []Override{}}
}

func (v *Value) Kind() Kind { return v.kind }

// Scalar returns the value exactly as written in the file — unquoted but
// otherwise untouched, never coerced to a number or a bool.
//
// Coercion is how the re-emit strategies destroy files: `"3.9"` becomes 3.9,
// which serialises back without its quotes and changes meaning. Keeping the
// raw text is what makes a two-line diff possible.
func (v *Value) Scalar() string { return v.scalar }

// Raw returns the scalar as the file writes it, before interpolation —
// `${DB_PASSWORD}` where Scalar() returns what that resolved to.
//
// It is the same string as Scalar() for any value that held no variable
// reference, so a caller that wants the bytes can always ask for Raw and a
// caller that wants the effective value can always ask for Scalar, without
// either having to check first.
func (v *Value) Raw() string {
	if v.wasInterp {
		return v.raw
	}
	return v.scalar
}

// Interpolated reports whether this value's text contained a variable
// reference that was expanded at load.
func (v *Value) Interpolated() bool { return v.wasInterp }

// Seq returns the elements of a sequence, or nil for any other kind.
func (v *Value) Seq() []*Value {
	if v.kind != KindSequence {
		return nil
	}
	out := make([]*Value, len(v.seq))
	copy(out, v.seq)
	return out
}

// Map returns the mapping, or nil for any other kind.
func (v *Value) Map() *OrderedMap {
	if v.kind != KindMapping {
		return nil
	}
	return v.mapping
}

func (v *Value) Origin() Origin { return v.origin }

// Alias returns the name of the anchor this value was expanded from, or "" if
// the value was written where it stands.
//
// A value can carry an anchor name and any kind at all: `deploy: *base` where
// `&base` is a mapping resolves to KindMapping with Alias() == "base".
func (v *Value) Alias() string { return v.aliasName }

// AliasSite returns where the `*anchor` reference that pulled this value in was
// written, and whether there was one. Origin() is the anchor's definition site;
// this is the site the reader is actually looking at.
func (v *Value) AliasSite() (Origin, bool) {
	if !v.fromAlias {
		return Origin{}, false
	}
	return v.aliasSite, true
}

// Overrides returns what this value replaced during merge, oldest first.
// Present and empty when nothing was overridden.
func (v *Value) Overrides() []Override {
	out := make([]Override, len(v.overrides))
	copy(out, v.overrides)
	return out
}

// At walks a path from this value. The bool reports whether the path resolved.
func (v *Value) At(p Path) (*Value, bool) {
	cur := v
	for _, seg := range p {
		if cur == nil {
			return nil, false
		}
		switch cur.kind {
		case KindMapping:
			next, ok := cur.mapping.Get(seg)
			if !ok {
				return nil, false
			}
			cur = next
		case KindSequence:
			i, ok := parseIndex(seg)
			if !ok || i < 0 || i >= len(cur.seq) {
				return nil, false
			}
			cur = cur.seq[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Walk calls fn for every value in the tree, depth first, with its path.
// Returning false from fn stops the whole walk — not just that subtree.
func (v *Value) Walk(fn func(Path, *Value) bool) { v.walk(Path{}, fn) }

// walk reports whether the caller should keep going.
func (v *Value) walk(p Path, fn func(Path, *Value) bool) bool {
	if v == nil {
		return true // an absent child is not a reason to stop
	}
	if !fn(p, v) {
		return false
	}
	switch v.kind {
	case KindSequence:
		for i, e := range v.seq {
			if !e.walk(p.Index(i), fn) {
				return false
			}
		}
	case KindMapping:
		for _, k := range v.mapping.Keys() {
			e, _ := v.mapping.Get(k)
			if !e.walk(p.Child(k), fn) {
				return false
			}
		}
	}
	return true
}

// valueJSON is the wire shape. Kept separate from Value so the model's fields
// can stay unexported and immutable without losing a stable JSON contract.
type valueJSON struct {
	Kind string `json:"kind"`
	// Scalar has no omitempty: `image: ""` and an absent value are different
	// things, and the model is careful about that distinction everywhere else.
	Scalar    string     `json:"scalar"`
	HasScalar bool       `json:"-"`
	Seq       []*Value   `json:"seq,omitempty"`
	Map       []mapEntry `json:"map,omitempty"`
	Origin    Origin     `json:"origin"`
	Overrides []Override `json:"overrides"`
	// Alias and AliasSite are present only on values that came from an anchor.
	// Origin above is the anchor's definition site in that case; AliasSite is
	// where the `*name` that pulled it in was written.
	Alias     string  `json:"alias,omitempty"`
	AliasSite *Origin `json:"alias_site,omitempty"`
	// Raw and Interpolated are present only on a value whose text held a
	// variable reference. Raw is what the file says; Scalar is what it
	// resolved to. A consumer that renders `${VAR}` with its resolution
	// beneath needs both, and the splice engine needs Raw to know what bytes
	// are actually there.
	Raw          string `json:"raw,omitempty"`
	Interpolated bool   `json:"interpolated,omitempty"`
}

type mapEntry struct {
	Key string `json:"key"`
	// KeyOrigin is where the key itself was written — the position the splice
	// engine locates when it replaces or deletes a subtree. Without it on the
	// wire, a consumer of the JSON cannot compute the range an edit would touch.
	KeyOrigin Origin `json:"key_origin"`
	Value     *Value `json:"value"`
}

// MarshalJSON emits mappings as an ordered array of entries rather than a JSON
// object. Key order is a fidelity property of the source file, and a JSON
// object does not preserve it.
func (v *Value) MarshalJSON() ([]byte, error) {
	out := valueJSON{
		Kind:      v.kind.String(),
		Scalar:    v.scalar,
		HasScalar: v.kind == KindScalar || v.kind == KindAlias,
		Seq:       v.seq,
		Origin:    v.origin,
		Overrides: v.overrides,
	}
	if out.Overrides == nil {
		out.Overrides = []Override{}
	}
	if v.fromAlias {
		site := v.aliasSite
		out.Alias, out.AliasSite = v.aliasName, &site
	}
	if v.wasInterp {
		out.Raw, out.Interpolated = v.raw, true
	}
	if v.kind == KindMapping && v.mapping != nil {
		for _, k := range v.mapping.Keys() {
			e, _ := v.mapping.Get(k)
			ko, _ := v.mapping.KeyOrigin(k)
			out.Map = append(out.Map, mapEntry{Key: k, KeyOrigin: ko, Value: e})
		}
	}
	return json.Marshal(out)
}

// OrderedMap is a mapping that remembers the order its keys appeared in, and
// where each key was written. Key order is preserved exactly, including for
// numeric-looking keys, because reordering a file is damage.
type OrderedMap struct {
	keys       []string
	vals       map[string]*Value
	keyOrigins map[string]Origin
}

func newOrderedMap() *OrderedMap {
	return &OrderedMap{vals: map[string]*Value{}, keyOrigins: map[string]Origin{}}
}

func (m *OrderedMap) set(key string, v *Value, keyOrigin Origin) {
	if _, seen := m.vals[key]; !seen {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
	m.keyOrigins[key] = keyOrigin
}

// remove drops a key and its position. It is unexported and used only by the
// merge, where `!reset` means "this declaration is not there" — the resolved
// model stays immutable to every caller outside this package.
func (m *OrderedMap) remove(key string) {
	if _, ok := m.vals[key]; !ok {
		return
	}
	delete(m.vals, key)
	delete(m.keyOrigins, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			return
		}
	}
}

// Keys returns the keys in source order.
func (m *OrderedMap) Keys() []string {
	if m == nil {
		return nil
	}
	out := make([]string, len(m.keys))
	copy(out, m.keys)
	return out
}

func (m *OrderedMap) Get(key string) (*Value, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.vals[key]
	return v, ok
}

// KeyOrigin returns where the key itself was written, which is what the splice
// engine locates when it replaces or deletes a whole subtree.
func (m *OrderedMap) KeyOrigin(key string) (Origin, bool) {
	if m == nil {
		return Origin{}, false
	}
	o, ok := m.keyOrigins[key]
	return o, ok
}

func (m *OrderedMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// Project is a resolved compose project: the ordered list of files it was built
// from, and the document as a single provenance-carrying tree.
//
// Services, networks and the rest are accessors over that tree rather than
// typed fields. That is what makes AD-3 structural — there is no field on this
// type that could hold a configuration value without an Origin, because there
// is no configuration field at all.
type Project struct {
	files []SourceFile
	root  *Value
	// findings are what resolution noticed without being stopped by it —
	// currently the undefined variables story 1.3 records. They are NOT
	// errors and never become errors (AD-13).
	findings []Finding
	// envFileSourced names variables that resolved only because an `env_file`
	// supplied them. Compose does not consult env_file for interpolation, so
	// these are exactly the names where this engine and the CLI can disagree,
	// and story 1.5's differential harness reads the list rather than guessing.
	envFileSourced []string
}

// Findings returns what resolution noticed, in the order it noticed it.
// Present and empty rather than nil, for the same reason the override history
// is: "nothing was wrong" and "nothing was checked" must not look alike.
func (p *Project) Findings() []Finding {
	out := make([]Finding, len(p.findings))
	copy(out, p.findings)
	return out
}

// EnvFileSourced returns the variable names that only an `env_file` defined.
// Compose would have interpolated these to empty; see varEnv for why the extra
// source exists and why it sits underneath every other layer.
func (p *Project) EnvFileSourced() []string {
	out := make([]string, len(p.envFileSourced))
	copy(out, p.envFileSourced)
	return out
}

// Files returns the ordered source files. Origin.Step indexes this list.
func (p *Project) Files() []SourceFile {
	out := make([]SourceFile, len(p.files))
	copy(out, p.files)
	return out
}

// Root is the whole document.
func (p *Project) Root() *Value { return p.root }

// At resolves a path against the document.
func (p *Project) At(path Path) (*Value, bool) {
	if p.root == nil {
		return nil, false
	}
	return p.root.At(path)
}

func (p *Project) topLevel(key string) *OrderedMap {
	v, ok := p.At(Path{key})
	if !ok || v.Kind() != KindMapping {
		return nil
	}
	return v.Map()
}

// Services returns the services mapping, or nil if absent.
func (p *Project) Services() *OrderedMap { return p.topLevel("services") }

// Networks returns the top-level networks mapping, or nil if absent.
func (p *Project) Networks() *OrderedMap { return p.topLevel("networks") }

// Volumes returns the top-level volumes mapping, or nil if absent.
func (p *Project) Volumes() *OrderedMap { return p.topLevel("volumes") }

// Configs returns the top-level configs mapping, or nil if absent.
func (p *Project) Configs() *OrderedMap { return p.topLevel("configs") }

// Secrets returns the top-level secrets mapping, or nil if absent.
func (p *Project) Secrets() *OrderedMap { return p.topLevel("secrets") }

// Extensions returns the top-level `x-` keys in source order. They are carried
// through untouched — an unrecognised key is the file's business, not ours.
func (p *Project) Extensions() []string {
	root := p.Root()
	if root == nil || root.Kind() != KindMapping {
		return nil
	}
	var out []string
	for _, k := range root.Map().Keys() {
		if len(k) > 2 && k[0] == 'x' && k[1] == '-' {
			out = append(out, k)
		}
	}
	return out
}

// Walk visits every value in the project with its path.
func (p *Project) Walk(fn func(Path, *Value) bool) {
	if p.root != nil {
		p.root.Walk(fn)
	}
}

// MarshalJSON emits the stable wire schema consumed by `composure resolve -json`
// and, later, by the extension.
func (p *Project) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Files    []SourceFile `json:"files"`
		Root     *Value       `json:"root"`
		Profiles profilesJSON `json:"profiles"`
		Findings []Finding    `json:"findings"`
	}{
		Files:    p.Files(),
		Root:     p.root,
		Profiles: p.profilesWire(),
		Findings: p.Findings(),
	})
}

func parseIndex(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
