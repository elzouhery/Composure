// Package report classifies the ways a round-trip can damage a YAML file.
//
// The taxonomy exists because a single "pass/fail" number tells you nothing
// actionable. Knowing that 40% of failures are lost blank lines and 2% are
// expanded anchors tells you exactly which defect to attack first.
package report

import (
	"regexp"
	"sort"
	"strings"
)

// Defect is a named category of fidelity loss.
type Defect string

const (
	CommentsLost      Defect = "comments_lost"
	AnchorsExpanded   Defect = "anchors_expanded"
	MergeKeysFlat     Defect = "merge_keys_flattened"
	QuotingChanged    Defect = "quoting_changed"
	FlowToBlock       Defect = "flow_style_to_block"
	BlankLinesLost    Defect = "blank_lines_lost"
	IndentChanged     Defect = "indentation_changed"
	KeyOrderChanged   Defect = "key_order_changed"
	TrailingNewline   Defect = "trailing_newline_changed"
	DocMarkersChanged Defect = "document_markers_changed"
	LengthChanged     Defect = "line_count_changed"
	OtherDiff         Defect = "other"
)

// Ordered returns the defect categories in reporting order.
func Ordered() []Defect {
	return []Defect{
		CommentsLost, AnchorsExpanded, MergeKeysFlat, QuotingChanged,
		FlowToBlock, BlankLinesLost, IndentChanged, KeyOrderChanged,
		DocMarkersChanged, TrailingNewline, LengthChanged, OtherDiff,
	}
}

var (
	reComment   = regexp.MustCompile(`(^|\s)#`)
	reAnchor    = regexp.MustCompile(`&[A-Za-z0-9_\-.]+`)
	reAlias     = regexp.MustCompile(`\*[A-Za-z0-9_\-.]+`)
	reMergeKey  = regexp.MustCompile(`^\s*<<\s*:`)
	reFlowSeq   = regexp.MustCompile(`:\s*\[`)
	reFlowMap   = regexp.MustCompile(`:\s*\{`)
	reDocMarker = regexp.MustCompile(`^(---|\.\.\.)`)
	reKeyLine   = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.\-"']+)\s*:`)
	reQuoted    = regexp.MustCompile(`:\s*("[^"]*"|'[^']*')\s*(#.*)?$`)
)

// Result is the outcome of comparing one file's input against its round-tripped output.
type Result struct {
	Path      string
	Identical bool
	Defects   []Defect
	InBytes   int
	OutBytes  int
	Err       error
}

// Classify diffs original against roundtripped and returns the defect categories present.
// It is deliberately heuristic: the goal is to bucket failures for triage, not to
// produce a formal proof of what changed.
func Classify(orig, out string) []Defect {
	if orig == out {
		return nil
	}
	set := map[Defect]bool{}

	oLines := strings.Split(orig, "\n")
	nLines := strings.Split(out, "\n")

	count := func(lines []string, re *regexp.Regexp) int {
		n := 0
		for _, l := range lines {
			n += len(re.FindAllString(l, -1))
		}
		return n
	}
	countLines := func(lines []string, pred func(string) bool) int {
		n := 0
		for _, l := range lines {
			if pred(l) {
				n++
			}
		}
		return n
	}

	// Comments: any reduction in the number of '#' comment markers.
	if count(oLines, reComment) > count(nLines, reComment) {
		set[CommentsLost] = true
	}
	// Anchors and aliases: any reduction means they were expanded/inlined.
	if count(oLines, reAnchor) > count(nLines, reAnchor) ||
		count(oLines, reAlias) > count(nLines, reAlias) {
		set[AnchorsExpanded] = true
	}
	// Merge keys.
	if countLines(oLines, reMergeKey.MatchString) > countLines(nLines, reMergeKey.MatchString) {
		set[MergeKeysFlat] = true
	}
	// Flow style collapsed to block.
	if count(oLines, reFlowSeq)+count(oLines, reFlowMap) >
		count(nLines, reFlowSeq)+count(nLines, reFlowMap) {
		set[FlowToBlock] = true
	}
	// Blank lines.
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	if countLines(oLines, blank) != countLines(nLines, blank) {
		set[BlankLinesLost] = true
	}
	// Quoting style.
	if count(oLines, reQuoted) != count(nLines, reQuoted) {
		set[QuotingChanged] = true
	}
	// Document markers.
	if countLines(oLines, reDocMarker.MatchString) != countLines(nLines, reDocMarker.MatchString) {
		set[DocMarkersChanged] = true
	}
	// Trailing newline.
	if strings.HasSuffix(orig, "\n") != strings.HasSuffix(out, "\n") {
		set[TrailingNewline] = true
	}
	// Indentation: compare the multiset of leading-whitespace widths on key lines.
	if indentSignature(oLines) != indentSignature(nLines) {
		set[IndentChanged] = true
	}
	// Key order: compare the sequence of key names, ignoring indentation.
	if ko, no := keyOrder(oLines), keyOrder(nLines); !sameOrder(ko, no) {
		set[KeyOrderChanged] = true
	}
	// Line count.
	if len(oLines) != len(nLines) {
		set[LengthChanged] = true
	}

	if len(set) == 0 {
		set[OtherDiff] = true
	}

	out2 := make([]Defect, 0, len(set))
	for _, d := range Ordered() {
		if set[d] {
			out2 = append(out2, d)
		}
	}
	return out2
}

func indentSignature(lines []string) string {
	var widths []int
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		widths = append(widths, len(l)-len(strings.TrimLeft(l, " ")))
	}
	sort.Ints(widths)
	var b strings.Builder
	for _, w := range widths {
		b.WriteByte(byte('0' + w%10))
	}
	return b.String()
}

func keyOrder(lines []string) []string {
	var keys []string
	for _, l := range lines {
		if m := reKeyLine.FindStringSubmatch(l); m != nil {
			keys = append(keys, m[2])
		}
	}
	return keys
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		// Different counts is a length problem, not necessarily an order problem.
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
