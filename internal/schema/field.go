package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Support says whether the machine's Compose can run a key (AD-21).
//
// The default is Unknown and it is load-bearing: with no `docker compose` on
// the machine, or no recorded minimum for the key, nothing is marked and the
// whole schema is offered. We degrade to useful, not to empty.
type Support string

const (
	// SupportUnknown — no installed binary, or no minimum recorded for the key.
	SupportUnknown Support = "unknown"
	// SupportYes — the installed Compose is at or past the key's minimum.
	SupportYes Support = "yes"
	// SupportNo — the key needs a newer Compose than the one installed. The
	// key is still SHOWN. Hiding it would teach the reader that the key does
	// not exist, when the truth is that upgrading would give it to them.
	SupportNo Support = "no"
)

// Field is one key the specification permits at a config path, and what the
// file does or does not say about it.
//
// Both halves are the point. A field with Declared true carries the value and
// its provenance; a field with Declared false is `available, not set` and
// carries the schema's default as the placeholder, so the reader learns what
// happens if they leave it alone.
type Field struct {
	Key string `json:"key"`
	// Type is the specification's type for the key, rendered for a human:
	// "string", "array", "boolean or string". Empty when the schema does not
	// constrain it.
	Type string `json:"type,omitempty"`
	// Description is one line, taken from the schema's own description. Never
	// rewritten: the label is the key's real name and the prose is the spec's
	// real prose, because a friendlier paraphrase is unsearchable.
	Description string `json:"description,omitempty"`
	// Default is the schema default rendered as text, empty when there is
	// none. DefaultSource says how it was found — see defaultOf.
	Default       string `json:"default,omitempty"`
	DefaultSource string `json:"default_source,omitempty"`
	// Allowed is the set of values the specification names for this key, in the
	// specification's own order, empty when it names none. AllowedSource says
	// how they were found — see allowedOf.
	//
	// It is a set of values the specification NAMES, which is not always the
	// set it permits: only `schema` is closed. So the control built on this is
	// a suggestion list on a field the reader can still type into, never a
	// choice of N — `${RESTART_POLICY}` has to stay writable, and so does a
	// value from a Compose newer than the vendored schema.
	Allowed       []string `json:"allowed,omitempty"`
	AllowedSource string   `json:"allowed_source,omitempty"`
	// Deprecated is the schema's own `deprecated: true`.
	Deprecated bool `json:"deprecated,omitempty"`
	// MinVersion is the earliest Compose known to understand the key, empty
	// when unknown. MinVersionNote is the one-word reason it is recorded.
	MinVersion     string  `json:"min_version,omitempty"`
	MinVersionNote string  `json:"min_version_note,omitempty"`
	Support        Support `json:"support"`

	// Declared is the whole `available, not set` split: false means the file
	// does not say this, and the inspector renders it as a link.
	Declared bool `json:"declared"`
	// Path is the config path this field addresses — `services.db.healthcheck`
	// — the same join key the resolver, topology and diagnostics use, so the
	// extension can match a finding to a field without inventing an id.
	Path string `json:"path"`
	// Value is what the file says, present only when Declared.
	Value *ValueView `json:"value,omitempty"`
	// Children are the fields of a declared mapping — `healthcheck` declared
	// without `start_period` puts that key in the healthcheck group's own
	// `available, not set` line. Nil for anything else.
	Children []Field `json:"children,omitempty"`
	// FreeForm marks a declared mapping whose keys the schema does not name:
	// `environment`, `labels`. Its entries are rendered; no available list is
	// offered, because the schema permits any key at all and offering "these
	// six" would be an invention.
	FreeForm bool `json:"free_form,omitempty"`
}

// describe builds a Field from a schema node, without the declared half.
func (s *Spec) describe(key, path string, node map[string]any) Field {
	f := Field{Key: key, Path: path, Support: SupportUnknown}
	if node == nil {
		return f
	}
	f.Type = typeName(s, node)
	f.Description = descriptionLine(s, node)
	f.Default, f.DefaultSource = defaultOf(s, node)
	f.Deprecated = deprecated(s, node)
	f.Allowed, f.AllowedSource = allowedOf(s, node)
	if mv, ok := s.minVer[key]; ok {
		f.MinVersion, f.MinVersionNote = mv.Min, mv.Note
	}
	return f
}

// deprecated reports the schema's own `deprecated: true` on any branch.
func deprecated(s *Spec, node map[string]any) bool {
	for _, b := range s.branches(node) {
		if v, ok := b["deprecated"].(bool); ok && v {
			return true
		}
	}
	return false
}

// typeName renders the permitted JSON types for a human.
//
// The specification expresses "a string or an object" three different ways —
// `"type": ["string","object"]`, a `oneOf` of two typed arms, and a bare
// `$ref` to something that does one of those. All three read the same to a
// reader, so all three render the same here.
func typeName(s *Spec, node map[string]any) string {
	set := map[string]bool{}
	for _, b := range s.branches(node) {
		switch t := b["type"].(type) {
		case string:
			set[t] = true
		case []any:
			for _, x := range t {
				if name, ok := x.(string); ok {
					set[name] = true
				}
			}
		}
	}
	if len(set) == 0 {
		return ""
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}

// firstSentence is the cheapest honest one-liner: up to the first full stop
// that is followed by whitespace, or the whole string.
var firstSentence = regexp.MustCompile(`^(.*?[.!?])(\s|$)`)

// descriptionLine is the schema's own description, cut to one sentence.
//
// The whole description is often three sentences of examples. The inspector
// has one line per field and a paragraph would push the value off screen, so
// the first sentence is taken — never a rewrite, because renaming or
// paraphrasing the spec's own words makes the field unsearchable against the
// documentation the reader will go and read next.
func descriptionLine(s *Spec, node map[string]any) string {
	for _, b := range s.branches(node) {
		d, ok := b["description"].(string)
		if !ok || strings.TrimSpace(d) == "" {
			continue
		}
		d = strings.Join(strings.Fields(d), " ")
		if m := firstSentence.FindStringSubmatch(d); m != nil {
			return m[1]
		}
		return d
	}
	return ""
}

// defaultsInProse finds the specification's own "Default: 30s." convention.
var defaultsInProse = regexp.MustCompile(`(?i)\bdefaults?\s*(?:to|:)\s*([^.;]+)`)

// defaultOf renders the key's default and says where it was found.
//
// Two sources, both inside the vendored schema and neither hand-written:
//
//   - the JSON Schema `default` keyword, which the Compose specification uses
//     in exactly two places;
//   - the description's own "Default: 30s." convention, which is where the
//     specification actually records most of them.
//
// The source is reported rather than hidden so that a reader — and a reviewer
// — can tell a machine-readable default from one read out of prose. Prose is
// the weaker of the two and it is labelled as such; it is still far better
// than showing no placeholder at all on `interval`, which is precisely the key
// someone adding a healthcheck needs to understand.
func defaultOf(s *Spec, node map[string]any) (string, string) {
	for _, b := range s.branches(node) {
		if raw, ok := b["default"]; ok {
			if text, ok := renderJSON(raw); ok {
				return text, "schema"
			}
		}
	}
	for _, b := range s.branches(node) {
		d, ok := b["description"].(string)
		if !ok {
			continue
		}
		if m := defaultsInProse.FindStringSubmatch(d); m != nil {
			text := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), "`'\""))
			if text != "" && len(text) <= 48 {
				return text, "description"
			}
		}
	}
	return "", ""
}

// renderJSON renders a scalar default as the text a file would contain.
func renderJSON(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return fmt.Sprintf("%t", t), true
	case float64:
		// %v on a float64 renders 3 as "3", not "3.000000".
		return strings.TrimSuffix(fmt.Sprintf("%v", t), ".0"), true
	case nil:
		return "null", true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// A value the specification names is only useful if a file could hold it. The
// prose is full of shapes — `service:[service name]`, `<protocol>`, `path/to/x`
// — and offering one of those as a value to pick is the confident wrong answer
// this engine specialises in, because it looks exactly like the real thing.
//
// So: letters, digits, and the four punctuation marks the real values use
// (`_`, `-`, `.`, `+`). `on-failure`, `if_not_present`, `sync+restart` and
// `service_completed_successfully` pass; anything with a space, a bracket, an
// angle or a colon does not.
var literalValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]*$`)

// quotedInProse finds the specification's own convention for naming a value:
// it single-quotes them. `'no'`, `'on-failure'`, `'vip'`.
var quotedInProse = regexp.MustCompile(`'([^']{1,40})'`)

// proseNamesValues is the cue that a description is LISTING the permitted
// values rather than merely mentioning one.
//
// Required, and it is the difference between reading a list and inventing one.
// `container_name` says "Specify a custom container name, rather than a
// generated default name." and quotes nothing; `command` quotes an example
// command. Without a cue, any description that happened to quote two things
// would become a picker offering them.
var proseNamesValues = regexp.MustCompile(`(?i)\b(options?\s+(?:include|are)|values?\s+(?:can\s+be|are|include)|one\s+of|either|:\s*'|'\s*(?:\(default\)\s*)?or\s+')`)

// allowedOf lists the values the specification names for a key, and says how
// they were found.
//
// Three sources, all inside the vendored schema and none hand-written — the
// same arrangement, and the same reason, as defaultOf. AD-20 is the constraint:
// nothing in this package may contain a list of Compose values any more than it
// may contain a list of Compose keys, because a maintained list falls behind the
// specification and then confidently tells the reader that a value which works
// does not exist.
//
//   - "schema": `enum` or `const`, covering EVERY form the key accepts. The
//     only CLOSED form, and the specification uses it in nine places in the
//     whole document — `depends_on.*.condition`, `cgroup`, a volume entry's
//     `type`, `develop.watch.*.action` and the two `order` keys among them.
//   - "schema-branch": `enum` or `const` on SOME of the forms the key accepts,
//     and not on the others. The values are real and they are the
//     specification's own; the claim that there is nothing else is not. `gpus`
//     is the whole population of this source in the vendored document: a
//     `oneOf` of `{"type":"string","enum":["all"]}` and an array of GPU device
//     objects, so `all` is exhaustive for the string form and says nothing
//     about the list form. It also catches the case where a value the
//     specification enumerates is one this package refuses to offer — a shape
//     like `service:[service name]` — because the offered list is then shorter
//     than the enumeration and closed would be a claim about a list we trimmed.
//   - "pattern": a `pattern` that is an alternation of literals. `pull_policy`
//     is written this way. Arms that are not literal — `every_([0-9]+[wdhms])+`
//     — are dropped rather than shown, so the list is shorter than the truth,
//     which is the safe direction for a control the reader can still type into.
//   - "description": the specification's own "Options include: 'no', 'always',
//     …" convention, which is where it records `restart`, `network_mode`,
//     `deploy.mode` and `deploy.endpoint_mode` — every key the owner named
//     except one. Weakest of the three, reported as such, and filtered hard by
//     literalValue so a shape like `service:[service name]` cannot become a
//     value to pick.
//
// The source travels because the three do not mean the same thing. Only the
// first is a closed set; the other two are the values the specification NAMES,
// and a file may hold others. That is what makes the control a typeable field
// with suggestions rather than a choice of N — see the story's control note.
//
// Order is the specification's own, never sorted: `no, always, on-failure,
// unless-stopped` is the order the documentation lists and the first entry is
// the default. Alphabetising it would put `always` first, which is the one
// value that is not.
func allowedOf(s *Spec, node map[string]any) ([]string, string) {
	if values := enumValues(s, node); len(values) > 0 {
		if enumIsClosed(s, node, 0) {
			return values, "schema"
		}
		return values, "schema-branch"
	}
	if values := patternValues(s, node); len(values) > 1 {
		return values, "pattern"
	}
	if values := proseValues(s, node); len(values) > 1 {
		return values, "description"
	}
	return nil, ""
}

// enumValues unions every `enum` and `const` on every branch.
//
// Unioned rather than taken from the first arm because a `oneOf` of two enums
// is one set to a reader. The union can only be wider than any single arm, and
// wider is the safe direction here: the control suggests, it does not restrict.
func enumValues(s *Spec, node map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw any) {
		text, ok := renderJSON(raw)
		if !ok || seen[text] || !literalValue.MatchString(text) {
			return
		}
		seen[text] = true
		out = append(out, text)
	}
	for _, b := range s.branches(node) {
		if list, ok := b["enum"].([]any); ok {
			for _, v := range list {
				add(v)
			}
		}
		if v, ok := b["const"]; ok {
			add(v)
		}
	}
	return out
}

// enumIsClosed reports whether the offered list is the whole of what the node
// permits — the question "the specification allows nothing else" is an answer
// to.
//
// enumValues UNIONS across every branch, which is the right way to build the
// list and the wrong way to judge it: a union is wider than any single arm, and
// on a `oneOf` whose other arm is an array it is not a bound on anything. So
// closedness is decided over the node's STRUCTURE rather than over the flat
// branch list:
//
//   - `enum` or `const` on the node closes it, provided every member survives
//     literalValue. A member dropped as a shape leaves the offered list shorter
//     than the enumeration, and a claim of closure would then be about a list
//     this package trimmed rather than about the specification.
//   - `oneOf` / `anyOf`: a value satisfies ONE arm, so the node is closed only
//     when EVERY arm is. This is the `gpus` case, and the defect this function
//     exists for.
//   - `allOf`: a value satisfies EVERY arm, so ONE enumerated arm closes the
//     node.
//
// Anything else — a bare `"type": "string"`, an empty schema — is open. False
// is the safe direction throughout: it downgrades the wording to "these are the
// values the specification names", which is true of every list this package can
// produce.
func enumIsClosed(s *Spec, node map[string]any, depth int) bool {
	// Bounded like branches(), and for the same reason. An unreadably deep
	// composition is one we decline to call closed.
	if node == nil || depth > 6 {
		return false
	}
	node = s.deref(node)
	if list, ok := node["enum"].([]any); ok && len(list) > 0 {
		return allOffered(list)
	}
	if v, ok := node["const"]; ok {
		return allOffered([]any{v})
	}
	open := false
	for _, key := range []string{"oneOf", "anyOf"} {
		arms, ok := node[key].([]any)
		if !ok || len(arms) == 0 {
			continue
		}
		closed := true
		for _, arm := range arms {
			m, ok := arm.(map[string]any)
			if !ok || !enumIsClosed(s, m, depth+1) {
				closed = false
				break
			}
		}
		if closed {
			return true
		}
		open = true
	}
	if open {
		return false
	}
	if arms, ok := node["allOf"].([]any); ok {
		for _, arm := range arms {
			if m, ok := arm.(map[string]any); ok && enumIsClosed(s, m, depth+1) {
				return true
			}
		}
	}
	return false
}

// allOffered reports whether every enumerated member reaches the offered list.
func allOffered(list []any) bool {
	for _, raw := range list {
		text, ok := renderJSON(raw)
		if !ok || !literalValue.MatchString(text) {
			return false
		}
	}
	return len(list) > 0
}

// patternValues reads a `pattern` that is an alternation of literals.
func patternValues(s *Spec, node map[string]any) []string {
	for _, b := range s.branches(node) {
		p, ok := b["pattern"].(string)
		if !ok {
			continue
		}
		var out []string
		seen := map[string]bool{}
		for _, arm := range splitAlternation(strings.TrimSuffix(strings.TrimPrefix(p, "^"), "$")) {
			if !literalValue.MatchString(arm) || seen[arm] {
				continue
			}
			seen[arm] = true
			out = append(out, arm)
		}
		if len(out) > 1 {
			return out
		}
	}
	return nil
}

// splitAlternation splits on the `|`s that are at the TOP level of a regular
// expression. A `|` inside a group belongs to that group, and splitting on it
// would produce two halves of one arm and call each of them a value.
func splitAlternation(p string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '|':
			if depth == 0 {
				out = append(out, p[start:i])
				start = i + 1
			}
		}
	}
	return append(out, p[start:])
}

// proseValues reads the specification's own convention for listing values.
//
// Scalars only. A description under `volumes` or `deploy` that quotes two words
// is prose about a structure, and offering them as values to type into a key
// that holds a list would be nonsense.
func proseValues(s *Spec, node map[string]any) []string {
	kind := typeName(s, node)
	if kind == "" || strings.Contains(kind, "object") || strings.Contains(kind, "array") {
		return nil
	}
	for _, b := range s.branches(node) {
		d, ok := b["description"].(string)
		if !ok || !proseNamesValues.MatchString(d) {
			continue
		}
		var out []string
		seen := map[string]bool{}
		for _, m := range quotedInProse.FindAllStringSubmatch(d, -1) {
			v := m[1]
			if !literalValue.MatchString(v) || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
		if len(out) > 1 {
			return out
		}
	}
	return nil
}
