package edit

// A unified diff, computed over lines.
//
// It exists because the product's central claim is a number a human reads off a
// diff — "changing one image tag is a two-line diff" — and a claim the reader
// cannot see is a claim they have to take on trust. `stack/preview` returns this
// text and nothing else changes on disk.
//
// Two properties matter more than elegance here:
//
//  1. A line is whatever sits between "\n" bytes, carriage return included. A
//     CRLF file's lines end in "\r", the "\r" travels inside the line, and a
//     diff of a CRLF file against itself is therefore empty. Normalising line
//     endings to compare them would report "no change" for a file whose endings
//     had in fact been rewritten — the exact defect the corpus caught once.
//  2. The common prefix and suffix are trimmed before anything quadratic runs.
//     A splice changes a handful of lines in a file that may hold ten thousand,
//     so the region an LCS actually has to consider is tiny, and a full table
//     over a 10,000-line file would be 100M cells for a two-line answer.

import (
	"fmt"
	"strings"
)

// maxLCSCells caps the dynamic-programming table. Past it the middle region is
// reported as a wholesale replacement rather than a minimal edit script: a
// slower, prettier answer is not worth an unbounded allocation, and an edit
// that rewrites thousands of lines has already failed the only test that
// matters.
const maxLCSCells = 4 << 20

// contextLines is the number of unchanged lines shown around each hunk. Three
// is what `diff -u` and git use, and a diff that reads differently from the one
// in the reader's terminal is a diff they have to translate.
const contextLines = 3

// Diff is a unified diff and the counts a caller asserts on.
//
// Added and Removed count body lines only — the `---`, `+++` and `@@` lines are
// not changes to the file. "A two-line diff" means Removed == 1 && Added == 1,
// and that is the sentence R4.1 is written in.
type Diff struct {
	Text    string `json:"text"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Changed is the total number of body lines the diff touches.
func (d Diff) Changed() int { return d.Added + d.Removed }

// Empty reports whether the two inputs were identical.
func (d Diff) Empty() bool { return d.Added == 0 && d.Removed == 0 }

// splitLines splits on "\n" and reports whether the input ended with one.
//
// The trailing empty element that Split produces for a file ending in a newline
// is dropped and remembered instead: carried through as a line it would show up
// as a spurious change whenever the last real line was edited.
func splitLines(src []byte) (lines []string, endsWithNewline bool) {
	if len(src) == 0 {
		return nil, true
	}
	parts := strings.Split(string(src), "\n")
	if parts[len(parts)-1] == "" {
		return parts[:len(parts)-1], true
	}
	return parts, false
}

type edit struct {
	// op is ' ' for context, '-' for a removal and '+' for an addition.
	op   byte
	text string
	// noEOL marks the last line of a file that does not end in a newline.
	noEOL bool
}

// eofMark is appended to the final line of a side that does not end in a
// newline, for the duration of the comparison only.
//
// It is what makes "a\nb\n" and "a\nb" compare as different. Without it the two
// split into identical line lists, the diff comes back empty, and a splice that
// ate the last byte of a file passes review as "no change" — which is precisely
// the confident wrong answer this engine is prone to. The mark is stripped
// before anything is rendered, and it holds NUL bytes so no real line can
// collide with it.
const eofMark = "\x00\x00composure:no-newline-at-eof"

// Unified computes the unified diff between a and b, labelled with name.
func Unified(name string, a, b []byte) Diff {
	al, aEnds := splitLines(a)
	bl, bEnds := splitLines(b)
	if !aEnds && len(al) > 0 {
		al[len(al)-1] += eofMark
	}
	if !bEnds && len(bl) > 0 {
		bl[len(bl)-1] += eofMark
	}

	script := diffLines(al, bl)
	// Strip the marker back off and turn it into git's own annotation.
	for i := range script {
		if strings.HasSuffix(script[i].text, eofMark) {
			script[i].text = strings.TrimSuffix(script[i].text, eofMark)
			script[i].noEOL = true
		}
	}

	var out strings.Builder
	added, removed := 0, 0
	for _, e := range script {
		switch e.op {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return Diff{}
	}

	fmt.Fprintf(&out, "--- a/%s\n", name)
	fmt.Fprintf(&out, "+++ b/%s\n", name)
	for _, h := range hunks(script) {
		out.WriteString(h)
	}
	return Diff{Text: out.String(), Added: added, Removed: removed}
}

// diffLines produces the edit script: context, removals and additions in order.
func diffLines(a, b []string) []edit {
	// Trim the common prefix and suffix. This is what keeps the quadratic part
	// proportional to the size of the edit rather than the size of the file.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	script := make([]edit, 0, len(a)+len(b))
	for i := 0; i < pre; i++ {
		script = append(script, edit{op: ' ', text: a[i]})
	}
	script = append(script, middle(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for i := len(a) - suf; i < len(a); i++ {
		script = append(script, edit{op: ' ', text: a[i]})
	}
	return script
}

// middle diffs the region that prefix/suffix trimming did not settle.
func middle(a, b []string) []edit {
	switch {
	case len(a) == 0 && len(b) == 0:
		return nil
	case len(a) == 0 || len(b) == 0 || len(a)*len(b) > maxLCSCells:
		out := make([]edit, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, edit{op: '-', text: l})
		}
		for _, l := range b {
			out = append(out, edit{op: '+', text: l})
		}
		return out
	}

	// Longest common subsequence. Rows are a, columns are b.
	n, m := len(a), len(b)
	table := make([]int, (n+1)*(m+1))
	at := func(i, j int) int { return i*(m+1) + j }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[at(i, j)] = table[at(i+1, j+1)] + 1
				continue
			}
			if table[at(i+1, j)] >= table[at(i, j+1)] {
				table[at(i, j)] = table[at(i+1, j)]
			} else {
				table[at(i, j)] = table[at(i, j+1)]
			}
		}
	}

	out := make([]edit, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, edit{op: ' ', text: a[i]})
			i, j = i+1, j+1
		case table[at(i+1, j)] >= table[at(i, j+1)]:
			out = append(out, edit{op: '-', text: a[i]})
			i++
		default:
			out = append(out, edit{op: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, edit{op: '-', text: a[i]})
	}
	for ; j < m; j++ {
		out = append(out, edit{op: '+', text: b[j]})
	}
	return out
}

// hunks renders the edit script as unified-diff hunks with contextLines of
// context on each side, merging hunks that would otherwise overlap.
func hunks(script []edit) []string {
	// Indices of every changed entry.
	var changed []int
	for i, e := range script {
		if e.op != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	var out []string
	i := 0
	for i < len(changed) {
		start := changed[i] - contextLines
		if start < 0 {
			start = 0
		}
		end := changed[i] + contextLines
		j := i
		for j+1 < len(changed) && changed[j+1]-contextLines <= end+1 {
			j++
			end = changed[j] + contextLines
		}
		if end > len(script)-1 {
			end = len(script) - 1
		}
		out = append(out, renderHunk(script, start, end))
		i = j + 1
	}
	return out
}

func renderHunk(script []edit, start, end int) string {
	// 1-based line numbers of the hunk's first line on each side.
	aLine, bLine := 1, 1
	for _, e := range script[:start] {
		if e.op != '+' {
			aLine++
		}
		if e.op != '-' {
			bLine++
		}
	}
	aCount, bCount := 0, 0
	for _, e := range script[start : end+1] {
		if e.op != '+' {
			aCount++
		}
		if e.op != '-' {
			bCount++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "@@ -%s +%s @@\n", span(aLine, aCount), span(bLine, bCount))
	for _, e := range script[start : end+1] {
		b.WriteByte(e.op)
		b.WriteString(e.text)
		b.WriteByte('\n')
		if e.noEOL {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
	return b.String()
}

// span is git's rendering of a hunk range: `12,4`, or `12` when the range is
// exactly one line, or `0,0` for a side that contributes nothing.
func span(start, count int) string {
	if count == 0 {
		return "0,0"
	}
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
