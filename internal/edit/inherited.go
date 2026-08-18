package edit

// Where a value the reader can see is actually WRITTEN — and what that means
// for editing it.
//
// The pane resolves. The engine splices. Between those two facts sits a gap
// nothing in this codebase used to name: a resolved value does not have to live
// at the path it is read from. `services.web.restart` reads `unless-stopped`,
// and `services.web` has no `restart` key at all — the value arrives through
// `<<: *defaults`. The pane offered a field, the reader typed in it, and the
// engine answered `path services.web.restart not found`.
//
// That was the visible half. The invisible half was worse, because four more
// shapes of the same gap did NOT fail:
//
//	*entry       spliced over the `*` and wrote `entrypoint: ZZZentry`
//	&entry       dropped the anchor and left every `*entry` below it dangling
//	a `|` block  replaced the `|`, and the body became part of a plain scalar
//	a merged     a locally written key REPLACES the whole merged mapping, so
//	sub-mapping  overriding one key of an inherited healthcheck drops the rest
//
// Every one of those is this engine's characteristic failure — not a crash, a
// confident wrong answer. So classification happens BEFORE the splice, refuses
// by name, and the same answer is what the inspector renders at the field, so
// the reader learns the consequence before the write rather than from the diff.
//
// This file adds no write path and touches no engine: internal/strategy is
// unchanged. It reads the document with yaml.v3 — the same parser the rest of
// the core parses with — and answers a question about it.

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/strategy"
)

// The reasons a path is not a plain spliceable scalar. They are a closed set
// of slugs so a client can branch on them, and every one of them has a sentence
// in Detail for a client that would rather just show the reader.
const (
	// ReasonInherited: the mapping does not declare this key; it arrives
	// through a `<<:` merge key. Editable by INSERTING it here — decision 21.
	ReasonInherited = "inherited"
	// ReasonInheritedNested: an ancestor of this key arrives through a merge.
	// Writing the key locally would declare the ancestor locally too, and a
	// locally declared key replaces the merged one WHOLE — its siblings go.
	ReasonInheritedNested = "inherited-nested"
	// ReasonAlias: the bytes at this path are `*name`, a reference.
	ReasonAlias = "alias"
	// ReasonAnchor: the bytes at this path define `&name`, which other places
	// reference.
	ReasonAnchor = "anchor"
	// ReasonBlockScalar: a `|` or `>` scalar, whose value is the lines below.
	ReasonBlockScalar = "block-scalar"
	// ReasonFlow: the mapping this key would be inserted into is written in
	// flow style, which the insert engine refuses (strategy.ErrFlowStyle).
	ReasonFlow = "flow-style"
	// ReasonAbsent: the file simply does not have this key. The ordinary
	// insert, unchanged, and not a refusal of any kind.
	ReasonAbsent = "absent"
	// ReasonEntryIndex: the path addresses an entry of a list by an index the
	// list does not have. It is deliberately NOT ReasonAbsent: an absent key
	// can be added and this cannot, and the `insert_key` plan ReasonAbsent
	// carries adds nothing at all when the parent is a sequence. Offering it
	// was the confident wrong answer story 9.2 closes.
	ReasonEntryIndex = "entry-index"
	// ReasonNotScalar: a mapping or a sequence. There is no scalar to replace;
	// the pane renders it as a group rather than a field.
	ReasonNotScalar = "not-scalar"
	// ReasonNull: `key:` with nothing after it. The pane already handles this
	// as a delete plus an insert; it is named here so the set is complete.
	ReasonNull = "null-value"
	// ReasonUnreadable: the document does not parse. Nothing may be claimed
	// about anchors in a file we cannot read.
	ReasonUnreadable = "unreadable"
)

// Sentinel refusals. Each one means nothing was written and each one is a
// refusal rather than a fault: the reader's next move differs, but in all four
// cases the file on disk is exactly as it was.
var (
	// ErrInherited is a replace_scalar aimed at a value that arrives through a
	// merge key. The caller's move is an insert on the mapping the reader is
	// looking at — see Availability.Plan.
	ErrInherited = errors.New("that value is not written here; it arrives through a merge key")
	// ErrMergedMapping is a replace_scalar aimed INSIDE a mapping that arrives
	// whole through a merge key. Overriding one of its keys locally replaces
	// the whole mapping and drops the rest, so there is no safe override to
	// offer — unlike ErrInherited, which has one.
	ErrMergedMapping = errors.New("that value is inside a mapping this place inherits whole; overriding one key would drop the others")
	// ErrAliasValue is a replace_scalar aimed at `*name`. Splicing over a
	// reference rewrites what the file POINTS AT, which is not what a reader
	// changing a value asked for. It wrote `ZZZentry` for `*entry`.
	ErrAliasValue = errors.New("that value is an alias reference; splicing over it would rewrite the reference, not the value")
	// ErrAnchoredValue is a replace_scalar aimed at a value carrying `&name`.
	// The splice cannot keep the anchor, and dropping it leaves every `*name`
	// in the file dangling.
	ErrAnchoredValue = errors.New("that value defines an anchor; replacing it would remove the anchor other values reference")
	// ErrBlockScalar is a replace_scalar aimed at a `|` or `>` scalar. The
	// splice replaced the indicator and the body stayed behind, which YAML then
	// read as a continuation of the new value.
	ErrBlockScalar = errors.New("that value is a block scalar; replacing it in place would leave its body behind")
)

// Site is a position in the file being classified. The file is the one the
// caller passed, so it is not repeated here.
type Site struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Availability answers "can the reader edit this, and if not, why" for one
// config path in one file.
//
// It is the same answer three consumers need — `locate` before it splices, the
// CLI's `composure editable`, and the inspector, which renders Detail at the field
// so that the consequence of an edit is visible BEFORE the write. One
// implementation because two would disagree, and the one that disagreed would
// be the one that wrote bytes.
type Availability struct {
	Path string `json:"path"`
	// Editable is true only for a scalar with real bytes at this exact path,
	// which is the one case replace_scalar is safe on.
	Editable bool `json:"editable"`
	// Reason is empty when Editable, and one of the slugs above otherwise.
	Reason string `json:"reason,omitempty"`
	// Detail is one sentence for a reader, naming the thing in the file that
	// caused it — an anchor by name, a mapping by key. Never a slug.
	Detail string `json:"detail,omitempty"`
	// Plan names the operation that WOULD carry the reader's intent, when one
	// exists: `insert_key` for an inherited or absent key. Empty means there is
	// nothing safe to offer and the field is read-only.
	Plan string `json:"plan,omitempty"`
	// Anchor is the anchor's name, without `&` or `*`, for the three reasons
	// that involve one.
	Anchor string `json:"anchor,omitempty"`
	// Through is where the reference that pulled the value in is written — the
	// `<<: *defaults` or the `*entry`. It is the position the reader cannot
	// find by looking at the key they clicked.
	Through *Site `json:"through,omitempty"`
	// BytesAt is where the value actually lives: the anchor's own key.
	BytesAt *Site `json:"bytes_at,omitempty"`
}

// ClassifyFile is Classify against a file on disk.
func ClassifyFile(file, at string) (Availability, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return Availability{Path: at}, err
	}
	return Classify(src, at), nil
}

// Classify answers Availability for one path. It never fails: a document that
// does not parse is an answer (ReasonUnreadable), not an error, because every
// caller has to render something either way.
func Classify(src []byte, at string) Availability {
	out := Availability{Path: at}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil || len(doc.Content) == 0 {
		out.Reason, out.Detail = ReasonUnreadable, "This file does not parse, so nothing can be said about where its values are written."
		return out
	}
	segs := resolve.ParsePath(at)
	if len(segs) == 0 {
		out.Reason, out.Detail = ReasonNotScalar, "That is the document itself, not a value."
		return out
	}

	node := doc.Content[0]
	var (
		// The merge crossed on the way down, and at which segment. Depth
		// matters: crossing it on the LAST segment is an override the reader
		// can have, crossing it earlier is not.
		merge      *mergeCrossing
		mergeDepth = -1
		parent     *yaml.Node
	)
	for i, seg := range segs {
		parent = node
		next, via, ok := childOf(node, seg)
		if !ok {
			return absentAt(out, segs, i, parent, merge, mergeDepth)
		}
		if via != nil && merge == nil {
			merge, mergeDepth = via, i
		}
		node = next
	}

	// A merge crossed at the last segment: the key is not declared here, and
	// declaring it here overrides the anchor for this mapping alone.
	if merge != nil && mergeDepth == len(segs)-1 {
		out.Reason = ReasonInherited
		out.Anchor = merge.anchor
		out.Through = &Site{Line: merge.line, Column: merge.column}
		out.BytesAt = &Site{Line: node.Line, Column: node.Column}
		if node.Kind != yaml.ScalarNode {
			// A merged LIST or MAPPING has no field to type into, and the
			// operation that would add to it — insert_sequence_entry — cannot
			// locate a path with no bytes either. So there is no plan, and the
			// note exists to answer the question the reader actually has:
			// why does this service have a `networks:` I cannot see.
			out.Detail = fmt.Sprintf(
				"%s does not declare %s — the whole thing arrives through `<<: *%s` on line %d, and its bytes "+
					"are at line %d. Composure cannot add to it here: a key written on %s would replace the "+
					"inherited %s entirely rather than extend it. Edit it at the anchor, or write %s out on %s "+
					"in the file first.",
				owner(segs), leaf(segs), merge.anchor, merge.line, node.Line, owner(segs), leaf(segs), leaf(segs), owner(segs))
			return out
		}
		out.Plan = string(OpInsertKey)
		out.Detail = fmt.Sprintf(
			"%s does not set %s here — it arrives through `<<: *%s` on line %d. "+
				"Writing a value adds %s to %s, which overrides *%s for this one place; "+
				"the anchor on line %d and everything else that merges it are untouched.",
			owner(segs), leaf(segs), merge.anchor, merge.line, leaf(segs), owner(segs), merge.anchor, node.Line)
		return out
	}
	// A merge crossed further up: the key the reader wants sits INSIDE a
	// mapping this place does not declare either, and YAML replaces a merged
	// key whole rather than deep-merging it.
	if merge != nil {
		out.Reason = ReasonInheritedNested
		out.Anchor = merge.anchor
		out.Through = &Site{Line: merge.line, Column: merge.column}
		out.BytesAt = &Site{Line: node.Line, Column: node.Column}
		out.Detail = fmt.Sprintf(
			"This is inside `%s`, which %s does not declare — it comes whole from `<<: *%s` on line %d. "+
				"A `%s` written here would REPLACE the merged one rather than add to it, dropping its other keys, "+
				"so Composure will not write it. Edit it at the anchor on line %d if every place that merges it should change.",
			segs[mergeDepth], owner(segs[:mergeDepth+1]), merge.anchor, merge.line, segs[mergeDepth], node.Line)
		return out
	}

	switch {
	case node.Kind == yaml.AliasNode:
		out.Reason = ReasonAlias
		out.Anchor = node.Value
		out.Through = &Site{Line: node.Line, Column: node.Column}
		if node.Alias != nil {
			out.BytesAt = &Site{Line: node.Alias.Line, Column: node.Alias.Column}
		}
		out.Detail = fmt.Sprintf(
			"This line reads `*%s` — a reference to the anchor on line %d, not a value. "+
				"Replacing the reference in place would change what the file points at rather than what it says, "+
				"so Composure refuses. Edit the anchor, or replace the reference by hand in the file.",
			node.Value, anchorLine(node))
		return out
	case node.Anchor != "":
		out.Reason = ReasonAnchor
		out.Anchor = node.Anchor
		out.BytesAt = &Site{Line: node.Line, Column: node.Column}
		out.Detail = fmt.Sprintf(
			"This value carries the anchor `&%s`, and other places in the file reference it as `*%s`. "+
				"This engine replaces the value's bytes, which would take the anchor with it and leave those "+
				"references pointing at nothing, so it refuses.",
			node.Anchor, node.Anchor)
		return out
	case node.Kind == yaml.ScalarNode && (node.Style == yaml.LiteralStyle || node.Style == yaml.FoldedStyle):
		out.Reason = ReasonBlockScalar
		out.BytesAt = &Site{Line: node.Line, Column: node.Column}
		out.Detail = "This is a block scalar — its value is the indented lines below the `|` or `>`. " +
			"Composure does not rewrite block scalars yet: it only ever replaces bytes in place, and doing that here " +
			"would leave the body behind. Edit it in the file."
		return out
	case node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		out.Reason = ReasonNull
		out.Plan = string(OpInsertKey)
		out.BytesAt = &Site{Line: node.Line, Column: node.Column}
		out.Detail = "The file declares this key with no value, so there are no bytes to replace. " +
			"Typing one rewrites the line at the foot of its mapping."
		return out
	case node.Kind != yaml.ScalarNode:
		out.Reason = ReasonNotScalar
		out.BytesAt = &Site{Line: node.Line, Column: node.Column}
		out.Detail = "This key holds a collection, not a value."
		return out
	}

	out.Editable = true
	out.BytesAt = &Site{Line: node.Line, Column: node.Column}
	return out
}

// absentAt classifies a path the walk could not follow to the end.
func absentAt(out Availability, segs resolve.Path, i int, parent *yaml.Node, merge *mergeCrossing, mergeDepth int) Availability {
	// An index the list does not have, before anything else. It is not an
	// absent key, it has no plan, and the sentence says how long the list is —
	// which is the one fact the reader needs and the one `absent` withheld.
	if n := deref(parent); n != nil && n.Kind == yaml.SequenceNode {
		// strategy.EntryIndex, not a second reading of the same segment. This
		// used to be strconv.Atoi here and a hand-rolled loop in the engine, and
		// the two disagreed exactly where it cost: an index too large for an int
		// was ABSENT here — `plan: insert_key`, the pre-9.2 invitation — and
		// entry 0 there, which the write path then wrote.
		if _, ok, shaped := strategy.EntryIndex(segs[i]); ok || shaped {
			out.Reason = ReasonEntryIndex
			out.BytesAt = &Site{Line: n.Line, Column: n.Column}
			out.Detail = fmt.Sprintf(
				"That list has no entry %s. It has %d %s, numbered 0 through %d, and an entry cannot be "+
					"added by naming a position it does not have — add one to the end instead.",
				segs[i], len(n.Content), plural(len(n.Content), "entry", "entries"), len(n.Content)-1)
			if len(n.Content) == 0 {
				out.Detail = fmt.Sprintf("That list is empty, so it has no entry %s to edit.", segs[i])
			}
			return out
		}
	}
	// The missing segment is inside something inherited: the same replace-whole
	// hazard as above, reported the same way.
	if merge != nil && mergeDepth < i {
		out.Reason = ReasonInheritedNested
		out.Anchor = merge.anchor
		out.Through = &Site{Line: merge.line, Column: merge.column}
		out.Detail = fmt.Sprintf(
			"This is inside `%s`, which comes whole from `<<: *%s` on line %d. A `%s` written here would REPLACE "+
				"the merged one rather than add to it, so Composure will not write it.",
			segs[mergeDepth], merge.anchor, merge.line, segs[mergeDepth])
		return out
	}
	// The last segment only: anything missing further up is a path the reader's
	// pane cannot have produced, and the insert engine's own ancestor chaining
	// is what handles the rest.
	if flow(parent) {
		out.Reason = ReasonFlow
		out.Detail = fmt.Sprintf(
			"`%s` is written in flow style — `{...}` on one line. Adding a key to it means rewriting that line, "+
				"which this engine will not do, so Composure refuses rather than reformatting the file.",
			owner(segs[:i+1]))
		return out
	}
	out.Reason = ReasonAbsent
	out.Plan = string(OpInsertKey)
	out.Detail = fmt.Sprintf("The file does not set %s. Typing a value adds it.", leaf(segs))
	return out
}

// mergeCrossing records that a lookup went through a `<<:` merge key: which
// anchor, and where the merge key is written.
type mergeCrossing struct {
	anchor string
	line   int
	column int
}

// childOf resolves one path segment against a node, following merge keys the
// way Compose does but NOT pretending the result is written where it was found.
//
// A locally declared key always wins over a merged one, which is YAML's rule
// and the reason the override in decision 21 works at all. Merge keys are
// detected by TAG, never by matching the text `<<`: a quoted "<<" is an
// ordinary string key (decision 12 records the same choice in the resolver).
func childOf(node *yaml.Node, seg string) (*yaml.Node, *mergeCrossing, bool) {
	if node == nil {
		return nil, nil, false
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	switch node.Kind {
	case yaml.SequenceNode:
		// One reading of an index, shared with the engine that splices it.
		// `strconv.Atoi` alone accepted `+1` here — so Classify called
		// `ports[+1]` EDITABLE while the write path answered `not found`.
		i, ok, _ := strategy.EntryIndex(seg)
		if !ok || i < 0 || i >= len(node.Content) {
			return nil, nil, false
		}
		return node.Content[i], nil, true
	case yaml.MappingNode:
	default:
		return nil, nil, false
	}
	// Locally declared keys first, in the file's own order. A merge key's own
	// key node is `<<` and matches no ordinary segment, so it needs no guard
	// here — it is skipped by the comparison itself.
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == seg {
			return node.Content[i+1], nil, true
		}
	}
	// Nothing local. Now the merge keys, in the order the file writes them.
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Tag != "!!merge" {
			continue
		}
		for _, target := range mergeTargets(v) {
			if target.Alias == nil {
				continue
			}
			// One level: a merged mapping may itself merge, and the crossing
			// reported is the FIRST one, which is the one written in front of
			// the reader.
			if found, _, ok := childOf(target.Alias, seg); ok {
				return found, &mergeCrossing{anchor: target.Value, line: k.Line, column: k.Column}, true
			}
		}
	}
	return nil, nil, false
}

// mergeTargets is the alias nodes a merge key names: `<<: *a` is one, and
// `<<: [*a, *b]` is two, in precedence order.
func mergeTargets(v *yaml.Node) []*yaml.Node {
	if v == nil {
		return nil
	}
	if v.Kind == yaml.SequenceNode {
		return v.Content
	}
	return []*yaml.Node{v}
}

func flow(n *yaml.Node) bool { return n != nil && n.Style == yaml.FlowStyle }

// deref follows an alias so that a list reached through `*name` is still a
// list. childOf does the same on the way down; not doing it here would report
// an aliased sequence as an ordinary absent key.
func deref(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func anchorLine(n *yaml.Node) int {
	if n.Alias != nil {
		return n.Alias.Line
	}
	return 0
}

// leaf is the key the reader clicked, and owner is the thing that holds it —
// the two words every sentence above needs.
func leaf(p resolve.Path) string {
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1]
}

func owner(p resolve.Path) string {
	if len(p) < 2 {
		return "this file"
	}
	return p[len(p)-2]
}

// refusalFor turns a classification into the sentinel `locate` returns, or nil
// when the path is one replace_scalar may touch.
//
// Only the shapes that used to DAMAGE the file are refused here. An absent key,
// a null and a collection are left to the engines, which already answer them —
// changing those would move behaviour the gate measures for reasons that have
// nothing to do with this defect.
func refusalFor(a Availability) error {
	switch a.Reason {
	case ReasonInherited:
		return fmt.Errorf("%w: %s", ErrInherited, a.Detail)
	case ReasonInheritedNested:
		return fmt.Errorf("%w: %s", ErrMergedMapping, a.Detail)
	case ReasonAlias:
		return fmt.Errorf("%w: %s", ErrAliasValue, a.Detail)
	case ReasonAnchor:
		return fmt.Errorf("%w: %s", ErrAnchoredValue, a.Detail)
	case ReasonBlockScalar:
		return fmt.Errorf("%w: %s", ErrBlockScalar, a.Detail)
	}
	return nil
}
