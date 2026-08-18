package resolve

import "strings"

// The merge walker — stories 1.4, 1.6, 1.7 and 1.8 share this one
// implementation. It contains no per-key knowledge whatsoever: every decision
// comes from mergeTable in mergerules.go (AD-4).
//
// Provenance is the reason this is not a three-line deep merge. When a value
// loses, it is not discarded: it is pushed onto the winner's override history
// with its own Origin, oldest first, so that story 1.10 can answer "which file
// set this, and what did it replace" without re-reading anything.

// mergeInto folds src onto dst at path and returns the value that survives.
//
// The second return reports that the key must be REMOVED — `!reset`. That is a
// third outcome, distinct from "dst survived" and "src survived", and returning
// it rather than a magic Value is what keeps a reset from being merged into
// something by a caller that forgot about it.
func (l *loader) mergeInto(path Path, dst, src *Value) (out *Value, remove bool) {
	if src == nil {
		return dst, false
	}
	if src.resetTag {
		// `!reset` removes the declaration entirely, whatever was there.
		return nil, true
	}
	if dst == nil {
		return src, false
	}
	if src.overrideTag {
		// `!override` bypasses the rule for this key: no appending, no
		// key-wise merge, no uniqueness. The author said "this, not that".
		return replaceWith(dst, src), false
	}

	r := ruleFor(path)
	if r.how == behaviourReplace {
		return replaceWith(dst, src), false
	}

	switch {
	case dst.kind == KindMapping && src.kind == KindMapping:
		l.mergeMapping(path, dst, src)
		return dst, false

	case dst.kind == KindSequence && src.kind == KindSequence:
		if r.how == behaviourAppendUnique && r.key != nil {
			l.mergeSequenceUnique(path, dst, src, r.key)
			return dst, false
		}
		// compose-spec §13 default: sequences append.
		dst.seq = append(dst.seq, src.seq...)
		if r.how == behaviourAppendStrictUnique && r.key != nil {
			// Compose appends these too — and then refuses the project if the
			// result repeats. The repeat is KEPT, because collapsing it would
			// hand back a model for a document Compose will not load, and
			// named, because a merge that quietly produces an unloadable
			// project is the worst of the three outcomes.
			l.noteRepeats(path, dst, r.key)
		}
		return dst, false
	}

	// A SEQUENCE MEETING A MAPPING is the two spellings of one key written in
	// two files: `environment: [A=1, B=2]` in the base and `environment: {B:
	// two}` in the override. Compose normalises both to a mapping before it
	// merges, and keeps A.
	//
	// This engine does not normalise, because the resolved model must keep the
	// shape the file wrote — the splice engine edits those bytes, and an
	// inspector that showed a mapping where the file has a list would send an
	// edit to a range that is not there. So the base's entries really are
	// dropped here.
	//
	// What must NOT happen is dropping them silently, which is what this did
	// before: the resolved model showed only B and nothing anywhere said A had
	// existed. A finding names it (AD-13 — a finding, never an error, because
	// the file is legal and Compose runs it).
	if isContainerKind(dst.kind) && isContainerKind(src.kind) && dst.kind != src.kind {
		dropped := droppedEntries(dst, src)
		l.note(Finding{
			Kind:      FindingFormMismatch,
			Path:      path,
			Origin:    src.origin,
			Dropped:   dropped,
			Displaced: dst.origin,
			Message: "declared as a " + dst.kind.String() + " in " + dst.origin.File +
				" and as a " + src.kind.String() + " in " + src.origin.File +
				": the later declaration replaces the earlier one whole, so " + droppedPhrase(dropped) +
				" not in the resolved value. Reconciling the two forms would mean re-emitting the " +
				"collection in one shape, which this engine never does — it keeps the shape each file " +
				"wrote so an edit addresses real bytes. Compose normalises both forms and keeps them; " +
				"write both files in the same form to agree with it",
		})
	}

	// Different kinds, or two scalars. The later declaration wins and records
	// what it displaced.
	return replaceWith(dst, src), false
}

func isContainerKind(k Kind) bool { return k == KindSequence || k == KindMapping }

// droppedEntries names what the earlier declaration set and the later one does
// not, so a reader is told which keys went rather than merely that some did.
//
// Both compose spellings of a key/value collection are read the same way: a
// mapping contributes its keys, a sequence contributes the part of each entry
// before the first `=`. That covers the two forms this finding is ever raised
// for — `environment: [A=1]` against `environment: {A: 1}`, and `depends_on:
// [db]` against `depends_on: {db: {condition: ...}}` — and an entry with no
// readable name is reported as written rather than guessed at.
func droppedEntries(dst, src *Value) []string {
	kept := map[string]bool{}
	for _, n := range entryNames(src) {
		kept[n] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range entryNames(dst) {
		if kept[n] || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func entryNames(v *Value) []string {
	if v == nil {
		return nil
	}
	var out []string
	switch v.kind {
	case KindMapping:
		out = append(out, v.mapping.Keys()...)
	case KindSequence:
		for _, e := range v.seq {
			if e == nil || e.kind != KindScalar {
				continue
			}
			name, _, found := strings.Cut(e.scalar, "=")
			if !found {
				name = e.scalar
			}
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

// droppedPhrase renders the loss for the message. The two arms are different
// claims: naming the entries is what makes the finding actionable, and refusing
// to invent a list when none could be read is what keeps it honest.
func droppedPhrase(dropped []string) string {
	if len(dropped) == 0 {
		return "entries only the earlier form set are"
	}
	return "\"" + strings.Join(dropped, "\", \"") + "\" " + isAre(len(dropped))
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// note records a finding, tolerating a nil loader so that a merge run outside a
// load — there is none today, and a future one must not panic — still merges.
func (l *loader) note(f Finding) {
	if l == nil {
		return
	}
	l.findings = append(l.findings, f)
}

// noteRepeats reports every repeated element in a merged sequence whose
// attribute Compose validates for uniqueness (behaviourAppendStrictUnique).
//
// It is the same contract as the form-mismatch finding above: a finding, never
// an error, and never a silent fix. Compose's own message is quoted so that
// someone who then runs `docker compose config` recognises what they are
// looking at rather than treating the two reports as two problems.
//
// The scan is over the WHOLE merged sequence rather than only across the seam,
// because that is what Compose validates — but note the limit: this runs from
// the merge walker, so a single file that repeats an entry with nothing to
// merge against never reaches here. That case is a document-level validation
// and does not belong to the merge table.
func (l *loader) noteRepeats(path Path, seq *Value, key uniqueKeyFn) {
	first := map[string]*Value{}
	for _, e := range seq.seq {
		k, ok := key(e)
		if !ok {
			continue
		}
		prev, seen := first[k]
		if !seen {
			first[k] = e
			continue
		}
		l.note(Finding{
			Kind:   FindingRepeatedListItem,
			Path:   path,
			Origin: e.origin,
			Message: "\"" + k + "\" is declared here and already in " + prev.origin.File +
				": Compose appends these lists rather than deduplicating them, and then refuses the " +
				"project — \"items at 0 and 1 are equal\". The repeat is kept as written, because " +
				"dropping it would resolve a project `docker compose` will not load; remove one of " +
				"the two declarations",
		})
	}
}

// mergeMapping merges src's keys into dst in place.
//
// Keys already present keep their position — a merge must not reorder a file's
// keys, because the resolved view is read against the file. Keys only src has
// are appended in src's own order.
func (l *loader) mergeMapping(path Path, dst, src *Value) {
	for _, k := range src.mapping.Keys() {
		sv, _ := src.mapping.Get(k)
		sko, _ := src.mapping.KeyOrigin(k)

		dv, present := dst.mapping.Get(k)
		if !present {
			if sv != nil && sv.resetTag {
				// `!reset` on a key that is not there is a no-op, not an
				// insertion of a null.
				continue
			}
			dst.mapping.set(k, sv, sko)
			continue
		}
		merged, remove := l.mergeInto(path.Child(k), dv, sv)
		if remove {
			dst.mapping.remove(k)
			continue
		}
		// The key's own position moves to the file that last set the value,
		// so that "jump to the key" lands where the effective value is.
		dst.mapping.set(k, merged, sko)
	}
}

// mergeSequenceUnique merges two sequences whose elements name a resource.
//
// An element whose key matches one already present merges INTO it — that is
// what makes `- "8080:80"` in an override change a publication rather than add
// a second one. An element with no readable key is appended: duplicating is
// recoverable, and inventing an identity silently deletes.
func (l *loader) mergeSequenceUnique(path Path, dst, src *Value, key uniqueKeyFn) {
	index := map[string]int{}
	for i, e := range dst.seq {
		if k, ok := key(e); ok {
			// First occurrence wins the slot. A base file that already lists
			// the same key twice is its own problem; the merge must not
			// silently pick the second.
			if _, seen := index[k]; !seen {
				index[k] = i
			}
		}
	}
	for _, e := range src.seq {
		k, ok := key(e)
		if !ok {
			dst.seq = append(dst.seq, e)
			continue
		}
		i, seen := index[k]
		if !seen {
			index[k] = len(dst.seq)
			dst.seq = append(dst.seq, e)
			continue
		}
		// Element-wise merge under the same path: the rule that governs the
		// sequence governs its elements too, and appending an index segment
		// here would make every row in the table need a `.*` twin.
		merged, remove := l.mergeInto(path, dst.seq[i], e)
		if remove {
			dst.seq = append(dst.seq[:i], dst.seq[i+1:]...)
			// Rebuild rather than patch: every index after i has shifted, and
			// a stale index is how an element gets merged into its neighbour.
			index = map[string]int{}
			for j, d := range dst.seq {
				if kk, ok := key(d); ok {
					if _, dup := index[kk]; !dup {
						index[kk] = j
					}
				}
			}
			continue
		}
		dst.seq[i] = merged
	}
}

// replaceWith makes src the effective value and records what it displaced.
//
// The history is chronological, oldest first: everything dst had already
// replaced, then dst itself, then anything src had replaced inside its own
// file — an anchor it overrode, for instance.
func replaceWith(dst, src *Value) *Value {
	history := make([]Override, 0, len(dst.overrides)+1+len(src.overrides))
	history = append(history, dst.overrides...)
	history = append(history, overrideOf(dst))
	history = append(history, src.overrides...)
	src.overrides = history
	return src
}

// clone deep-copies a value so that a merge cannot reach back into the tree it
// read from. `extends:` needs this: the base service stays in the model in its
// own right, and merging the extending service's keys onto the original would
// rewrite the base.
func clone(v *Value) *Value {
	if v == nil {
		return nil
	}
	out := &Value{
		kind:      v.kind,
		scalar:    v.scalar,
		raw:       v.raw,
		origin:    v.origin,
		aliasName: v.aliasName,
		aliasSite: v.aliasSite,
		fromAlias: v.fromAlias,
		wasInterp: v.wasInterp,
		resetTag:  v.resetTag,
		//nolint // overrideTag is copied deliberately: an `!override` survives
		// being carried through an extends, or the second merge would apply
		// the rule the author bypassed.
		overrideTag: v.overrideTag,
		overrides:   append([]Override{}, v.overrides...),
	}
	switch v.kind {
	case KindSequence:
		out.seq = make([]*Value, len(v.seq))
		for i, e := range v.seq {
			out.seq[i] = clone(e)
		}
	case KindMapping:
		out.mapping = newOrderedMap()
		for _, k := range v.mapping.Keys() {
			e, _ := v.mapping.Get(k)
			ko, _ := v.mapping.KeyOrigin(k)
			out.mapping.set(k, clone(e), ko)
		}
	}
	return out
}

// dropResets removes anything still marked `!reset` once every merge has run.
//
// A `!reset` in the LAST file of a chain has nothing left to remove, and a
// `!reset` in a single file has nothing to remove at all. Both mean the same
// thing — the declaration is not there — so both are swept here rather than
// left in the model as a null that reads like `key:` with no value.
func dropResets(v *Value) {
	if v == nil {
		return
	}
	switch v.kind {
	case KindMapping:
		for _, k := range v.mapping.Keys() {
			e, _ := v.mapping.Get(k)
			if e != nil && e.resetTag {
				v.mapping.remove(k)
				continue
			}
			dropResets(e)
		}
	case KindSequence:
		kept := v.seq[:0]
		for _, e := range v.seq {
			if e != nil && e.resetTag {
				continue
			}
			dropResets(e)
			kept = append(kept, e)
		}
		v.seq = kept
	}
}
