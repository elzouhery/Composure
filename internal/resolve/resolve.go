// Package resolve turns compose files into an effective configuration where
// every value knows where it came from.
//
// The provenance is the point. `docker compose config` already flattens a
// project, but it discards which file set which value, and that is the question
// the product exists to answer. Story 1.1 covers a single file; merging,
// interpolation, include, extends and profiles arrive in later stories against
// this same model.
//
// This package reads. It has no write path, and must not grow one — edits go
// through internal/strategy, which patches byte ranges in the original buffer.
package resolve

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxDepth caps recursion in convert. Real compose files nest a handful of
// levels; a document deep enough to hit this is adversarial, and unbounded
// recursion exhausts the stack, which kills the process outright.
const maxDepth = 200

// maxAliasNodes caps how many values alias expansion may create in one
// document. Anchors that reference anchors expand 2^N: a forty-line file can
// name a billion nodes, and the process dies building a model nobody asked for.
//
// Only values created *inside* an expansion count against it, so a large but
// alias-free file can never trip the budget no matter how big it gets. The
// largest compose file in the corpus expands a few thousand nodes, so the
// headroom here is three orders of magnitude.
const maxAliasNodes = 100_000

// mergeTag is the tag yaml.v3 resolves a plain `<<` key to. Keying on the tag
// rather than on the text is what keeps a *quoted* `"<<"` — which is a
// perfectly ordinary string key — from being treated as a merge directive.
const mergeTag = "!!merge"

// Bytes resolves compose content already in memory. name is used for
// provenance and error reporting; it need not exist on disk.
//
// It is the single-document entry point: the content is parsed, interpolated
// and converted, but nothing is read from disk on its behalf beyond the `.env`
// and `env_file` sources that interpolation draws on, relative to name's
// directory. `include:` and `extends:` naming other files are resolved
// relative to that directory too, so a caller holding an unsaved buffer gets
// the same answer as one holding the file.
func Bytes(name string, src []byte) (*Project, error) {
	return BytesWith(name, src, Options{})
}

// BytesWith is Bytes with an explicit interpolation environment.
//
// It is what a test uses to state its inputs — `Options{Env: ..., IgnoreHostEnv:
// true}` makes a resolve hermetic, so a suite cannot pass or fail on whatever
// the developer happens to have exported — and it is what the editor will use
// for an unsaved buffer.
func BytesWith(name string, src []byte, opts Options) (*Project, error) {
	if len(strings.TrimSpace(string(src))) == 0 {
		return nil, &FileError{File: name, Err: ErrEmptyFile}
	}
	l := &loader{opts: opts, steps: map[string]int{}, envFileSourced: map[string]bool{}}
	l.step(name)
	root, err := l.convertFile(name, src, true)
	if err != nil {
		return nil, err
	}
	root, err = l.expandIncludes(name, root)
	if err != nil {
		return nil, err
	}
	if err := l.applyExtends(root); err != nil {
		return nil, err
	}
	dropResets(root)
	l.noteEmptyProfileNames(root)
	if err := validate(name, root, true); err != nil {
		return nil, err
	}
	return l.project(root), nil
}

// parseDocument decodes one YAML document and applies the refusals that have to
// happen before anything is converted.
func parseDocument(name string, src []byte) (*yaml.Node, error) {
	// Decode rather than Unmarshal: Unmarshal reads only the first document,
	// so a `---`-separated file would resolve with everything after the marker
	// invisible and no error raised.
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &FileError{File: name, Err: ErrEmptyFile}
		}
		line, col := yamlErrorPosition(err, src)
		return nil, &ParseError{File: name, Line: line, Column: col, Err: err}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, &FileError{File: name, Err: ErrMultiDocument}
	}

	// An all-comments file decodes into a zero node with no content.
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, &FileError{File: name, Err: ErrEmptyFile}
	}
	return doc.Content[0], nil
}

// yamlChild reads one key out of a yaml mapping node without converting it.
// The env_file scan needs it: the paths have to be known before the document
// can be interpolated, and the document cannot be converted before it is.
func yamlChild(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Kind == yaml.ScalarNode && n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// convCtx carries the per-document state that alias expansion needs: the two
// bounds that keep expansion from killing the process, and the set of anchors
// currently being expanded.
//
// It is a struct rather than extra parameters because the budget has to be
// shared across the whole walk. A per-branch counter bounds each path through
// the document and still lets the total blow up exponentially, which is the
// exact failure it exists to prevent.
type convCtx struct {
	file string
	step int
	// active holds the anchor nodes on the current expansion stack, by pointer
	// identity. An alias whose target is already on it is a cycle.
	active map[*yaml.Node]bool
	// budget is the number of values alias expansion may still create.
	budget int
	// inAlias is the expansion nesting depth; values created while it is
	// positive are the ones that count against the budget.
	inAlias int

	// env is the variable environment this FILE is interpolated against.
	// AD-11: interpolation happens here, at conversion, before any merge, so
	// every value carries the provenance of the file that interpolated it.
	env *varEnv
	// loader collects findings. It is the loader rather than a slice so that
	// a finding raised while converting an included file lands on the one
	// project the caller gets back.
	loader *loader
}

// resetTagName and overrideTagName are Compose's two merge tags. They are
// directives about the merge, not configuration, so they are read off the node
// and carried as flags — they never reach the model as values.
const (
	resetTagName    = "!reset"
	overrideTagName = "!override"
)

// convert walks a yaml.v3 node into the resolved model, stamping every value
// with where it was written.
//
// yaml.v3's Line and Column are 1-based and point at the value token, which is
// exactly what Origin wants — the position a user would put their cursor on.
//
// Aliases and merge keys are expanded here, for display only: the buffer is
// never touched, and `<<: *base` remains in the file exactly as written. Story
// 1.1 deliberately did not follow aliases, for three reasons this code has to
// keep answering — cycles (`active`), exponential fan-out (`budget`), and
// double counting. The third is not a bug to fix but a fact to state: an
// expanded anchor genuinely does appear at every site that references it, so
// the model holds one positioned value per site. Each carries the anchor's
// Origin, so "how many distinct positions are cited" is unchanged; only "how
// many nodes are in the tree" grows, which is what expansion means.
func (c *convCtx) convert(n *yaml.Node, depth int, p Path) (*Value, error) {
	if n == nil {
		return newValue(KindNull, Origin{File: c.file, Step: c.step}), nil
	}
	if depth > maxDepth {
		return nil, ErrTooDeep
	}
	if c.inAlias > 0 {
		c.budget--
		if c.budget < 0 {
			return nil, ErrAliasFanout
		}
	}

	origin := c.originOf(n)

	// `!reset` is a removal, whatever shape follows it. Converting the shape
	// as well would put a value in the model that the merge is about to
	// delete, and a `!reset` in the last file of a chain would survive as a
	// null that reads like an empty declaration.
	if n.Tag == resetTagName {
		v := newValue(KindNull, origin)
		v.resetTag = true
		return v, nil
	}

	v, err := c.convertNode(n, depth, p, origin)
	if err != nil {
		return nil, err
	}
	if n.Tag == overrideTagName {
		v.overrideTag = true
	}
	return v, nil
}

func (c *convCtx) convertNode(n *yaml.Node, depth int, p Path, origin Origin) (*Value, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return newValue(KindNull, origin), nil
		}
		return c.convert(n.Content[0], depth+1, p)

	case yaml.AliasNode:
		return c.expandAlias(n, depth, p)

	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return newValue(KindNull, origin), nil
		}
		v := newValue(KindScalar, origin)
		// n.Value is the unquoted text as written — never coerced. Quoting
		// style lives in n.Style and belongs to the buffer, not the model.
		//
		// Interpolation happens here and nowhere else (AD-11): at load, per
		// file, before any merge. The pre-interpolation text is kept on the
		// value, so the bytes in the file stay recoverable from the model.
		v.scalar = n.Value
		if err := c.interpolateValue(v, p, origin); err != nil {
			return nil, err
		}
		return v, nil

	case yaml.SequenceNode:
		v := newValue(KindSequence, origin)
		v.seq = make([]*Value, 0, len(n.Content))
		for i, e := range n.Content {
			child, err := c.convert(e, depth+1, p.Index(i))
			if err != nil {
				return nil, err
			}
			v.seq = append(v.seq, child)
		}
		return v, nil

	case yaml.MappingNode:
		return c.convertMapping(n, origin, depth, p)
	}

	return newValue(KindNull, origin), nil
}

// interpolateValue expands the variables in a scalar and records what it found.
//
// An undefined variable is a FINDING and the resolve succeeds; `${VAR:?msg}` is
// an ERROR carrying the author's message, because that form exists in order to
// fail. The two never cross (AD-13).
func (c *convCtx) interpolateValue(v *Value, p Path, origin Origin) error {
	if c.env == nil || !strings.ContainsRune(v.scalar, '$') {
		return nil
	}
	res := interpolate(v.scalar, c.env)
	if res.err != nil {
		res.err.File, res.err.Line, res.err.Column = origin.File, origin.Line, origin.Column
		return res.err
	}
	if res.changed {
		v.raw, v.scalar, v.wasInterp = v.scalar, res.text, true
	}
	if c.loader != nil {
		for _, f := range res.findings {
			f.Path = append(Path{}, p...)
			f.Origin = origin
			c.loader.findings = append(c.loader.findings, f)
		}
	}
	return nil
}

func (c *convCtx) originOf(n *yaml.Node) Origin {
	return Origin{File: c.file, Line: n.Line, Column: n.Column, Step: c.step}
}

// expandAlias resolves a `*anchor` to the anchor's value.
//
// The anchor node is re-converted rather than shared, so every reference site
// gets its own value tree. Sharing one subtree between paths would be cheaper,
// but the model is walked as a tree — by Walk, by MarshalJSON, by every path
// consumer — and a shared subtree makes those walks exponential even when the
// memory is not. The budget bounds the copy instead.
func (c *convCtx) expandAlias(n *yaml.Node, depth int, p Path) (*Value, error) {
	target := n.Alias
	if target == nil {
		// yaml.v3 rejects an undefined anchor at decode time, so this is
		// unreachable in practice. Refusing beats returning an empty value that
		// reads as "the anchor is empty".
		return nil, ErrUnknownAnchor
	}
	if c.active[target] {
		return nil, ErrAliasCycle
	}

	c.active[target] = true
	c.inAlias++
	v, err := c.convert(target, depth+1, p)
	c.inAlias--
	delete(c.active, target)
	if err != nil {
		return nil, err
	}

	// The value's Origin already points at the anchor's definition site — that
	// is where the bytes are. The reference site is recorded alongside it, so a
	// reader looking at `deploy: *base` on line 40 can get back to line 40.
	v.aliasName = n.Value
	v.aliasSite = c.originOf(n)
	v.fromAlias = true
	return v, nil
}

// ownEntry is a key written directly in a mapping, converted before merge keys
// are applied so that a `<<:` appearing *above* it can still tell that it loses.
type ownEntry struct {
	key       string
	val       *Value
	keyOrigin Origin
}

// mapItem is one position in a mapping's source order: either an own key or a
// merge directive. Keeping the order lets merged keys land where the `<<:` was
// written, which is where a reader expects to see them.
type mapItem struct {
	merge     bool
	mergeNode *yaml.Node
	own       *ownEntry
}

func (c *convCtx) convertMapping(n *yaml.Node, origin Origin, depth int, p Path) (*Value, error) {
	v := newValue(KindMapping, origin)
	v.mapping = newOrderedMap()

	// Pass one: convert every key written directly in this mapping, and note
	// where the merge directives sit. Own keys have to exist before any merge
	// is applied — merge-key semantics are that the parent's own keys win, and
	// a `<<:` written on the first line still loses to a key on the last.
	own := map[string]*ownEntry{}
	items := make([]mapItem, 0, len(n.Content)/2)
	seenMerge := false

	// Content alternates key, value. An odd length would mean a key with no
	// value, which yaml.v3 does not emit — but silently dropping the trailing
	// key would be exactly the confident-wrong-answer failure this engine is
	// prone to, so pair it with an explicit null instead.
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode {
			// A complex key (`? [a, b] : v`). Every one of them would
			// collapse to the empty-string key and overwrite the last.
			return nil, ErrComplexKey
		}
		var vn *yaml.Node
		if i+1 < len(n.Content) {
			vn = n.Content[i+1]
		}

		if k.Tag == mergeTag {
			// Two `<<:` in one mapping. YAML defines one merge key per mapping
			// and says nothing about how two would order against each other, so
			// any answer here would be invented. Refuse, as for any duplicate.
			if seenMerge {
				return nil, ErrDuplicateKey
			}
			seenMerge = true
			items = append(items, mapItem{merge: true, mergeNode: vn})
			continue
		}

		key := k.Value
		if _, dup := own[key]; dup {
			return nil, ErrDuplicateKey
		}
		keyOrigin := Origin{File: c.file, Line: k.Line, Column: k.Column, Step: c.step}

		var child *Value
		if vn != nil {
			cv, err := c.convert(vn, depth+1, p.Child(key))
			if err != nil {
				return nil, err
			}
			child = cv
		} else {
			// A key with no value has no position of its own; anchor it at the key.
			child = newValue(KindNull, keyOrigin)
		}
		e := &ownEntry{key: key, val: child, keyOrigin: keyOrigin}
		own[key] = e
		items = append(items, mapItem{own: e})
	}

	// Pass two: lay the mapping out in source order, expanding each merge
	// directive in place. The `<<` key itself never reaches the model — it is a
	// directive, not configuration.
	for _, it := range items {
		if !it.merge {
			v.mapping.set(it.own.key, it.own.val, it.own.keyOrigin)
			continue
		}
		if err := c.applyMerge(v, own, it.mergeNode, depth+1, p); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// applyMerge folds one merge directive into dst.
//
// Precedence, both parts of it, is YAML's and getting either backwards silently
// changes what a stack does:
//
//   - the parent's own keys beat anything merged in;
//   - within `<<: [*a, *b]`, earlier sources beat later ones.
//
// A key that loses is not discarded. It is appended to the winner's override
// history with the anchor's own position, which is how the resolved view can
// say "this came from here, and it replaced that".
func (c *convCtx) applyMerge(dst *Value, own map[string]*ownEntry, node *yaml.Node, depth int, p Path) error {
	sources, err := c.mergeSources(node, depth, p)
	if err != nil {
		return err
	}
	for _, src := range sources {
		for _, k := range src.mapping.Keys() {
			mv, _ := src.mapping.Get(k)
			ko, _ := src.mapping.KeyOrigin(k)

			// Carry the reference site down onto each contributed key. The
			// value's Origin is the anchor's — that is where it is written —
			// but the `<<: *base` that pulled it in has to stay recoverable.
			if src.fromAlias {
				mv.aliasName = src.aliasName
				mv.aliasSite = src.aliasSite
				mv.fromAlias = true
			}

			if e, ok := own[k]; ok {
				e.val.overrides = append(e.val.overrides, overrideOf(mv))
				continue
			}
			if prev, ok := dst.mapping.Get(k); ok {
				// An earlier source in the same `<<: [*a, *b]` already won.
				prev.overrides = append(prev.overrides, overrideOf(mv))
				continue
			}
			dst.mapping.set(k, mv, ko)
		}
	}
	return nil
}

// mergeSources reads the value of a `<<:` as the ordered list of mappings it
// names. Anything that is not a mapping, or an alias to one, is refused: YAML
// defines the merge value as a mapping or a sequence of mappings, and quietly
// ignoring `<<: 3` would drop configuration the author clearly meant to apply.
func (c *convCtx) mergeSources(node *yaml.Node, depth int, p Path) ([]*Value, error) {
	if node == nil {
		return nil, ErrBadMergeValue
	}
	switch node.Kind {
	case yaml.AliasNode, yaml.MappingNode:
		v, err := c.convert(node, depth, p)
		if err != nil {
			return nil, err
		}
		if v.kind != KindMapping {
			return nil, ErrBadMergeValue
		}
		return []*Value{v}, nil

	case yaml.SequenceNode:
		out := make([]*Value, 0, len(node.Content))
		for _, e := range node.Content {
			if e.Kind != yaml.AliasNode && e.Kind != yaml.MappingNode {
				return nil, ErrBadMergeValue
			}
			v, err := c.convert(e, depth+1, p)
			if err != nil {
				return nil, err
			}
			if v.kind != KindMapping {
				return nil, ErrBadMergeValue
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, ErrBadMergeValue
}

// overrideOf records what a losing value was. Sequences and mappings keep only
// their position: reconstructing a replaced subtree is the file's job.
func overrideOf(v *Value) Override {
	o := Override{Origin: v.origin}
	if v.kind == KindScalar {
		o.Value = v.scalar
	}
	return o
}

// yamlParserProblems is yaml.v3's CLOSED set of parser-stage problem strings,
// taken from parserc.go in gopkg.in/yaml.v3@v3.0.1. It exists to tell a parser
// error from a scanner error, which is the difference between a line number
// that is right and one that is off by one — see yamlErrorPosition.
//
// If the library is upgraded and this set drifts, the failure is a line number
// one off, not a crash. TestYAMLErrorPositionsAreTheRealLines pins the mapping
// against the parser itself, so the drift is caught here rather than in a
// user's editor.
var yamlParserProblems = []string{
	"did not find expected ',' or ']'",
	"did not find expected ',' or '}'",
	"did not find expected '-' indicator",
	"did not find expected <document start>",
	"did not find expected <stream-start>",
	"did not find expected key",
	"did not find expected node content",
	"found duplicate %TAG directive",
	"found duplicate %YAML directive",
	"found incompatible YAML document",
	"found undefined tag handle",
}

// yamlErrorPosition digs a line and column out of a yaml.v3 error.
//
// WHAT THIS RETURNS, EXACTLY, because the extension places a cursor with it and
// a position that claims more than it knows is the confident-wrong-answer
// failure this engine is prone to:
//
//   - The LINE is the line yaml.v3 blamed, corrected. yaml.v3 does not expose
//     the mark structurally — the parser struct is unexported — so the number is
//     read out of the message, and the number in the message is 0-BASED for a
//     parser-stage error and 1-based for a scanner-stage error (decode.go:112,
//     `fail`, which increments only for yaml_SCANNER_ERROR). The old code took
//     it verbatim, so every flow-sequence error — `ports: [8080`, the single
//     most common way a compose file breaks — pointed one line ABOVE the
//     mistake, at a line that was perfectly fine.
//
//   - The line may be the START OF THE CONSTRUCT rather than the offending
//     token. `fail` prefers context_mark over problem_mark when it has one, and
//     the message does not say which it used. So this is the position of the
//     thing that went wrong, not always the character.
//
//   - The COLUMN is the first non-blank column of that line. It is NOT the
//     offending token's column: that lives in problem_mark, which is
//     unreachable. Returning the indent is honest — it puts the cursor on the
//     content of the blamed line — where the 0 this used to return means "no
//     position" and made the line useless to a caller that needs both.
//
//   - A line the source does not have returns 0, 0. Pointing past the end of a
//     file is worse than admitting the position is unknown.
func yamlErrorPosition(err error, src []byte) (line, col int) {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return 0, 0
	}
	msg := err.Error()
	const marker = "line "
	i := strings.Index(msg, marker)
	if i < 0 {
		return 0, 0
	}
	rest := msg[i+len(marker):]
	n := 0
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		n = n*10 + int(rest[digits]-'0')
		digits++
	}
	if digits == 0 {
		return 0, 0
	}
	for _, p := range yamlParserProblems {
		if strings.Contains(msg, p) {
			n++ // parser-stage marks are 0-based
			break
		}
	}

	lines := strings.Split(string(src), "\n")
	if n < 1 || n > len(lines) {
		return 0, 0
	}
	text := strings.TrimSuffix(lines[n-1], "\r")
	col = 1
	for col-1 < len(text) && (text[col-1] == ' ' || text[col-1] == '\t') {
		col++
	}
	if col-1 >= len(text) {
		col = 1 // a blank line has no content to point at
	}
	return n, col
}

// looksLikeTopLevelServices reports whether a root mapping is a set of service
// definitions with no `services:` wrapper — Compose v1 format, or a fragment
// that some build step concatenates into a real file.
//
// The test is deliberately structural rather than name-based: at least one
// top-level value must be a mapping carrying `image` or `build`, the two keys
// that make a service a service. Guessing from key names would misfire on any
// document that happens to contain a mapping called `web`.
func looksLikeTopLevelServices(root *Value) bool {
	m := root.Map()
	if m == nil {
		return false
	}
	for _, k := range m.Keys() {
		v, _ := m.Get(k)
		if v == nil || v.Kind() != KindMapping {
			continue
		}
		if _, ok := v.Map().Get("image"); ok {
			return true
		}
		if _, ok := v.Map().Get("build"); ok {
			return true
		}
	}
	return false
}
