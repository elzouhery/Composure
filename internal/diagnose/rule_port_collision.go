package diagnose

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
	"github.com/elzouhery/composure/internal/topology"
)

// Story 3.2 — two services publishing the same host port.
//
// The rule runs over the MERGED configuration, which is the whole point: a
// collision between a base file and an override file is the case a single-file
// linter cannot catch, and the finding names both files because both Origins
// are carried.
//
// It reads published ports as topology nodes (AD-17). Port is a node kind
// precisely so that a collision can name the two positions rather than the two
// services, and so that profile filtering has already happened — a port
// published only under an inactive profile is not in this graph and therefore
// does not collide. Diagnosing with that profile active brings it back.
//
// Two things it deliberately does not report:
//
//   - The same CONTAINER port on different host ports. That is normal, and it
//     is what every multi-instance stack looks like.
//   - Two declarations bound to different explicit host IPs. `127.0.0.1:80` and
//     `10.0.0.1:80` do not contend. Only the addresses that mean "all
//     interfaces" are folded together, and only with each other.

type portCollisionRule struct{}

func (portCollisionRule) id() string { return "host-port-collision" }

func (portCollisionRule) title() string { return "Two services publish the same host port" }

// publication is one port declaration, flattened to a single host port so that
// a published range collides entry by entry.
type publication struct {
	service resolve.Path
	node    topology.Node
	port    int
}

func (r portCollisionRule) apply(c *context) []Finding {
	groups := map[string][]publication{}
	var order []string

	for _, n := range c.Graph.Nodes() {
		if n.Kind != topology.NodePort || n.Port == nil {
			continue
		}
		// An exposed-but-not-published port binds nothing on the host.
		if strings.TrimSpace(n.Port.Published) == "" {
			continue
		}
		if n.Origin.IsZero() {
			continue // AD-7: unanchorable, so not reported
		}
		svc := servicePathOf(n.Path)
		if svc == nil {
			continue
		}
		lo, hi, ok := parsePortRange(n.Port.Published)
		if !ok {
			// A published side that is not a number or a range is almost
			// always an uninterpolated variable. Guessing would be a confident
			// wrong answer; saying nothing is correct.
			continue
		}
		host := normaliseHostIP(n.Port.HostIP)
		proto := n.Port.Protocol
		if proto == "" {
			proto = "tcp"
		}
		for p := lo; p <= hi; p++ {
			k := host + "/" + proto + "/" + strconv.Itoa(p)
			if _, seen := groups[k]; !seen {
				order = append(order, k)
			}
			groups[k] = append(groups[k], publication{service: svc, node: n, port: p})
		}
	}

	sort.Strings(order)
	var out []Finding
	for _, k := range order {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		if f, ok := r.finding(c, g); ok {
			out = append(out, f)
		}
	}
	return out
}

func (r portCollisionRule) finding(c *context, g []publication) (Finding, bool) {
	// MERGE ORDER, not filename order. Origin.Step is the index of the file in
	// the project's ordered chain (AD-15) and exists for exactly this: the
	// declaration that arrived last is the one that introduced the conflict, and
	// the earlier one is the incumbent. Sorting by filename got this backwards
	// whenever the override sorted before the base — `-f zbase.yaml -f
	// aover.yaml` offered the fix to the base file, which is the one declaration
	// the author did not just add.
	sort.SliceStable(g, func(i, j int) bool {
		a, b := g[i].node.Origin, g[j].node.Origin
		if a.Step != b.Step {
			return a.Step < b.Step
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})

	var (
		names    []string
		subjects []resolve.Path
		anchors  []Anchor
		files    = map[string]bool{}
	)
	for _, p := range g {
		names = append(names, serviceName(p.service))
		subjects = append(subjects, p.service)
		anchors = append(anchors, Anchor{
			Label:  fmt.Sprintf("%s publishes %s", serviceName(p.service), p.node.Port.Raw),
			Path:   p.node.Path,
			Origin: p.node.Origin,
		})
		files[p.node.Origin.File] = true
	}

	where := ""
	if len(files) > 1 {
		where = fmt.Sprintf(" The declarations are in %d different files, so this collision is only visible after the merge.", len(files))
	}

	f := Finding{
		Rule:     r.id(),
		Severity: SeverityError,
		Title:    r.title(),
		Message: fmt.Sprintf("%s host port %d/%s; only one of them can bind it.%s",
			bindPhrase(names), g[0].port, protocolOf(g[0]), where),
		Subjects: subjects,
		Anchors:  anchors,
	}

	// The mechanical fix is to change one side. The last declaration in MERGE
	// order is the one offered — the first is, by that order, the incumbent.
	victim := g[len(g)-1]
	f.Fix, f.NoFix = r.fix(c, victim)
	return f, true
}

// fix describes changing the losing side's host port.
//
// It is offered only for the short string form. The long mapping form's
// `published:` is its own scalar at its own path and a fix could name it, but
// the port node's identity is the whole entry — describing a scalar replacement
// over a mapping would report a range phase 2 would not touch, and a wrong
// range is worse than none.
func (r portCollisionRule) fix(c *context, victim publication) (*Fix, string) {
	entry, ok := c.Project.At(victim.node.Path)
	if !ok || entry.Kind() != resolve.KindScalar {
		return nil, "no fix is described: the port is declared in the long mapping form, where the host side is one key among several and which of them to change is the author's call"
	}
	// The replacement port is the user's choice, not ours: an editor that picked
	// one would be guessing at what else is listening on the host. That is said
	// with NeedsValue rather than by leaving Value empty — an empty Value applied
	// verbatim writes `- ""`, which is valid YAML and invalid compose.
	return c.replaceScalarPlaceholderFix(victim.node.Path, victim.node.Origin,
		fmt.Sprintf("replace the host port in %s's entry %q with a free port",
			serviceName(victim.service), victim.node.Port.Raw))
}

// bindPhrase renders the subject of the collision sentence. One service can
// collide with itself by publishing the same port twice, and "services "web"
// and "web"" reads like a bug in the tool.
func bindPhrase(names []string) string {
	uniq := dedupe(names)
	if len(uniq) == 1 {
		return fmt.Sprintf("service %q publishes two entries that both bind", uniq[0])
	}
	return joinNames(uniq) + " all bind"
}

func protocolOf(p publication) string {
	if p.node.Port != nil && p.node.Port.Protocol != "" {
		return p.node.Port.Protocol
	}
	return "tcp"
}

// servicePathOf turns a port node's path — services.web.ports[0] — back into
// the service that publishes it.
func servicePathOf(p resolve.Path) resolve.Path {
	if len(p) < 2 || p[0] != "services" {
		return nil
	}
	return resolve.Path{"services", p[1]}
}

// normaliseHostIP folds the spellings of "every interface" together and leaves
// every explicit address alone. `0.0.0.0:80` and `80` contend; `127.0.0.1:80`
// does not contend with `10.0.0.1:80`.
func normaliseHostIP(ip string) string {
	switch strings.TrimSpace(ip) {
	case "", "0.0.0.0", "::", "[::]", "*":
		return "*"
	}
	return strings.TrimSpace(ip)
}

// parsePortRange reads "8080" or "3000-3005". Anything else — a variable that
// was never interpolated, most often — is refused rather than guessed at.
//
// A range wider than maxRangeSpan is refused too. The arithmetic would be
// correct and the report would be useless: one finding per port across a
// thousand-port range is not a diagnostic, it is a denial of service on the
// reader.
const maxRangeSpan = 256

func parsePortRange(s string) (lo, hi int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	a, b, isRange := strings.Cut(s, "-")
	lo, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil || lo < 0 {
		return 0, 0, false
	}
	if !isRange {
		return lo, lo, true
	}
	hi, err = strconv.Atoi(strings.TrimSpace(b))
	if err != nil || hi < lo || hi-lo >= maxRangeSpan {
		return 0, 0, false
	}
	return lo, hi, true
}

// joinNames renders `services "a" and "b"`, or `services "a", "b" and "c"`.
func joinNames(uniq []string) string {
	switch len(uniq) {
	case 0:
		return "no services"
	case 1:
		return fmt.Sprintf("service %q", uniq[0])
	case 2:
		return fmt.Sprintf("services %q and %q", uniq[0], uniq[1])
	}
	quoted := make([]string, len(uniq))
	for i, n := range uniq {
		quoted[i] = strconv.Quote(n)
	}
	return "services " + strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

func dedupe(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
