package resolve

// Story 1.10 — "which file set this value, and what did it override".
//
// This is the question the whole product exists to answer, and by this point in
// the epic it is a lookup rather than a computation: the merge already recorded
// the answer on the value, oldest first, each entry with its own Origin. If
// this file had to reconstruct anything, the merge would be built wrong.

import (
	"fmt"
	"sort"
	"strings"
)

// Explanation is one value's provenance: where it is set now, and the ordered
// list of what it replaced.
//
// It carries the *Value rather than a flattened copy so that the answer cannot
// drift from the model — the origin, the override history, the alias site and
// the pre-interpolation text are all the ones the resolver produced.
type Explanation struct {
	Path  Path   `json:"path"`
	Value *Value `json:"value"`
}

// Origin is where the effective value is set.
func (e *Explanation) Origin() Origin { return e.Value.Origin() }

// Overrides is what it replaced, oldest first. Present and empty for a value
// that was set once — a field, never an absence.
func (e *Explanation) Overrides() []Override { return e.Value.Overrides() }

// Explain answers for one config path.
//
// A path that addresses nothing returns a typed error naming it and the
// closest paths that do exist. "No such path" on its own sends someone hunting
// for a typo they have already read four times.
func (p *Project) Explain(path Path) (*Explanation, error) {
	v, ok := p.At(path)
	if !ok {
		return nil, &PathError{Path: path, Closest: p.closestPaths(path, 5)}
	}
	return &Explanation{Path: append(Path{}, path...), Value: v}, nil
}

// PathError reports a config path that addresses nothing.
type PathError struct {
	Path Path
	// Closest are existing paths ranked by similarity, nearest first.
	Closest []string
}

func (e *PathError) Error() string {
	msg := fmt.Sprintf("no such config path: %s", e.Path.String())
	if len(e.Closest) > 0 {
		msg += "\ndid you mean: " + strings.Join(e.Closest, ", ")
	}
	return msg
}

func (e *PathError) Unwrap() error { return ErrNoSuchPath }

// closestPaths ranks the model's real paths against the one that missed.
//
// Ranking is by shared prefix first and edit distance second, in that order and
// not the other way round: `services.db.imag` should suggest `services.db.image`
// ahead of `services.web.image`, and pure string distance does not know that
// the first two segments were right.
func (p *Project) closestPaths(want Path, n int) []string {
	type candidate struct {
		path   string
		prefix int
		dist   int
	}
	target := want.String()

	var cands []candidate
	p.Walk(func(got Path, _ *Value) bool {
		if len(got) == 0 {
			return true
		}
		cands = append(cands, candidate{
			path:   got.String(),
			prefix: sharedPrefix(want, got),
			dist:   editDistance(target, got.String()),
		})
		return true
	})

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].prefix != cands[j].prefix {
			return cands[i].prefix > cands[j].prefix
		}
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].path < cands[j].path
	})

	out := make([]string, 0, n)
	for _, c := range cands {
		// A candidate that shares no prefix AND is not close is noise. Offering
		// five unrelated paths is worse than offering none: it reads as though
		// the tool did not understand the question.
		if c.prefix == 0 && c.dist > len(target)/2+2 {
			continue
		}
		out = append(out, c.path)
		if len(out) == n {
			break
		}
	}
	return out
}

func sharedPrefix(a, b Path) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// editDistance is Levenshtein, two rows at a time. Only used for ranking a
// handful of suggestions, so clarity beats cleverness.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
