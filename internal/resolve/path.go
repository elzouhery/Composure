package resolve

import (
	"strconv"
	"strings"
)

// Path addresses one node in a resolved project: ["services", "web", "ports",
// "0"]. It is the join key between the resolver, the topology graph, the
// diagnostics and the splice engine — a finding, a graph node and a byte range
// all identify their subject the same way.
//
// The underlying type is deliberately []string rather than a struct of typed
// segments. strategy.Locate takes a []string, and a named slice type is
// assignable to an unnamed one, so a Path passes straight through without the
// splice engine importing this package. That keeps strategy a leaf, which is
// what lets the corpus harness exercise it at scale without dragging the
// resolver in. If Path ever needs fields, it has to move to its own package
// and that interop is lost — treat the underlying type as load-bearing.
type Path []string

// Child returns a new Path with seg appended. It copies rather than appending
// in place, so a Path handed out to a caller can never be aliased and mutated
// by a later Child call on the same parent.
func (p Path) Child(seg string) Path {
	out := make(Path, len(p)+1)
	copy(out, p)
	out[len(p)] = seg
	return out
}

// Index returns a new Path addressing the i'th element of a sequence.
func (p Path) Index(i int) Path { return p.Child(strconv.Itoa(i)) }

// Equal reports whether two paths address the same node. Identity is the
// segment slice, never the rendered string — see String for why that matters.
func (p Path) Equal(q Path) bool {
	if len(p) != len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// String renders a path for display: services.web.ports[0].
//
// Rendering is lossy in one case and it is worth naming. A segment of all
// digits renders as an index, so a genuinely numeric mapping key — compose
// files do use these, `environment: {8080: "x"}` — renders as [8080] and reads
// like a sequence index. That is a display ambiguity only. Nothing compares
// paths by their rendered form; Equal compares segments.
func (p Path) String() string {
	var b strings.Builder
	for i, seg := range p {
		switch {
		case isIndexSegment(seg):
			b.WriteByte('[')
			b.WriteString(seg)
			b.WriteByte(']')
		case needsQuoting(seg):
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(strconv.Quote(seg))
		default:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(seg)
		}
	}
	return b.String()
}

// ParsePath is the inverse of String for paths that survive the round trip.
// A segment rendered as [n] comes back as the bare digits, which is correct for
// sequence indices and is the documented lossy case for numeric mapping keys.
func ParsePath(s string) Path {
	var (
		out Path
		cur strings.Builder
		i   int
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i < len(s) {
		switch s[i] {
		case '.':
			flush()
			i++
		case '[':
			flush()
			j := strings.IndexByte(s[i:], ']')
			if j < 0 { // unterminated — take the rest verbatim
				cur.WriteString(s[i+1:])
				i = len(s)
				continue
			}
			out = append(out, s[i+1:i+j])
			i += j + 1
		case '"':
			flush()
			rest := s[i:]
			if seg, err := strconv.Unquote(quotedPrefix(rest)); err == nil {
				out = append(out, seg)
				i += len(quotedPrefix(rest))
				continue
			}
			cur.WriteByte(s[i])
			i++
		default:
			cur.WriteByte(s[i])
			i++
		}
	}
	flush()
	return out
}

func quotedPrefix(s string) string {
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			return s[:i+1]
		}
	}
	return s
}

func isIndexSegment(s string) bool {
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

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	return strings.ContainsAny(s, `.[]"`)
}
