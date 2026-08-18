package topology

import (
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// Build derives the topology of a resolved project under an active profile set.
//
// profiles is the explicit active set (AD-16). Nil or empty means Compose's
// default: only services with no `profiles:` key are included. Building twice
// with the same arguments produces an identical graph, ordering included —
// nothing here iterates a Go map without sorting what comes out.
//
// Build reads. It never writes to the project, never caches, and returns a
// value that is a total function of its arguments.
func Build(p *resolve.Project, profiles []string) (*Graph, error) {
	if p == nil {
		return nil, ErrNoProject
	}

	b := &builder{
		project:  p,
		active:   normaliseProfiles(profiles),
		nodeAt:   map[string]int{},
		declared: map[string]bool{},
	}
	b.activeSet = map[string]bool{}
	for _, name := range b.active {
		b.activeSet[name] = true
	}

	b.collectServices()
	b.collectResources()
	b.walkServices()

	g := &Graph{
		profiles: b.active,
		nodes:    b.nodes,
		edges:    b.edges,
		dangling: b.dangling,
	}
	g.finalise()
	return g, nil
}

// builder accumulates the graph. It exists so that node creation, which can
// happen from a reference as well as from a declaration, has one place to go
// through — two paths into the node set is how a node ends up in the index
// twice under two identities.
type builder struct {
	project   *resolve.Project
	active    []string
	activeSet map[string]bool

	nodes  []Node
	nodeAt map[string]int
	edges  []Edge

	dangling []Dangling

	// declared is every service name with a top-level entry, whether or not
	// the profile filter kept it. It is what tells a reference to a filtered
	// service (dangling) apart from a reference to a service that was never
	// written down at all (external node).
	declared map[string]bool
}

func normaliseProfiles(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func (b *builder) addNode(n Node) {
	k := key(n.Path)
	if _, dup := b.nodeAt[k]; dup {
		return
	}
	b.nodeAt[k] = len(b.nodes)
	b.nodes = append(b.nodes, n)
}

func (b *builder) hasNode(p resolve.Path) bool {
	_, ok := b.nodeAt[key(p)]
	return ok
}

// collectServices creates a node for every service the active profile set
// keeps, and records every declared name so filtering can be explained later.
func (b *builder) collectServices() {
	services := b.project.Services()
	for _, name := range services.Keys() {
		b.declared[name] = true
		v, _ := services.Get(name)
		path := resolve.Path{"services", name}
		profiles := profilesOf(v)
		if !profileActive(profiles, b.activeSet) {
			continue
		}
		b.addNode(Node{
			Path:     path,
			Kind:     NodeService,
			Name:     name,
			Origin:   declarationOrigin(services, name, v),
			Declared: true,
			Profiles: profiles,
			Image:    scalarOf(mustField(v, "image")),
		})
	}
}

// collectResources creates a node for every declared top-level network,
// volume, config and secret.
//
// They are not profile-filtered. A profile selects which services run; the
// resources a project declares are its inventory either way, and story 2.1
// wants that inventory whole — a volume declared and mounted by nothing is
// exactly the kind of leftover the graph exists to surface.
func (b *builder) collectResources() {
	sections := []struct {
		key  string
		kind NodeKind
		m    *resolve.OrderedMap
	}{
		{"networks", NodeNetwork, b.project.Networks()},
		{"volumes", NodeVolume, b.project.Volumes()},
		{"configs", NodeConfig, b.project.Configs()},
		{"secrets", NodeSecret, b.project.Secrets()},
	}
	for _, s := range sections {
		for _, name := range s.m.Keys() {
			v, _ := s.m.Get(name)
			b.addNode(Node{
				Path:     resolve.Path{s.key, name},
				Kind:     s.kind,
				Name:     name,
				Origin:   declarationOrigin(s.m, name, v),
				Declared: true,
				External: truthy(field(v, "external")),
				Internal: s.kind == NodeNetwork && truthy(field(v, "internal")),
			})
		}
	}
}

// declarationOrigin prefers the key's position over the value's.
//
// The key is where the declaration was written — `pgdata:` with nothing after
// it has a value whose position the parser puts wherever the next token
// happens to be, which is a different line and reads as a lie.
func declarationOrigin(m *resolve.OrderedMap, name string, v *resolve.Value) resolve.Origin {
	if o, ok := m.KeyOrigin(name); ok && !o.IsZero() {
		return o
	}
	if v != nil {
		return v.Origin()
	}
	return resolve.Origin{}
}

func profilesOf(svc *resolve.Value) []string {
	v, ok := field(svc, "profiles")
	if !ok {
		return nil
	}
	var out []string
	for _, item := range seqItems(v) {
		if s := scalarOf(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// profileActive is Compose's own rule: a service with no profiles is always
// active, and one with profiles needs any single member of the active set.
func profileActive(profiles []string, active map[string]bool) bool {
	if len(profiles) == 0 {
		return true
	}
	for _, p := range profiles {
		if active[p] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Edges.

func (b *builder) walkServices() {
	services := b.project.Services()
	for _, name := range services.Keys() {
		path := resolve.Path{"services", name}
		if !b.hasNode(path) {
			continue // filtered out by profile; it contributes no edges
		}
		svc, _ := services.Get(name)
		b.dependsOn(path, svc)
		b.networks(path, svc)
		b.networkMode(path, svc)
		b.mounts(path, svc)
		b.links(path, svc)
		b.fileMounts(path, svc, "configs", EdgeConfig)
		b.fileMounts(path, svc, "secrets", EdgeSecret)
		b.ports(path, svc)
		b.build(path, svc)
	}
}

// resolveTarget turns a name written on a service into an edge target.
//
// Three outcomes, and the difference between the last two is the point of the
// function: the node exists; the node was filtered out by the active profile
// set, so the edge is dropped and reported as dangling; or nothing ever
// declared it, so it becomes an external node and the edge survives pointing
// at a node that says "this is not from here".
func (b *builder) resolveTarget(from resolve.Path, kind EdgeKind, section, name string, origin resolve.Origin) (resolve.Path, bool) {
	if name == "" {
		return nil, false
	}
	target := resolve.Path{section, name}
	if b.hasNode(target) {
		return target, true
	}
	if section == "services" && b.declared[name] {
		b.dangling = append(b.dangling, Dangling{
			Kind:   kind,
			From:   from,
			To:     target,
			Ref:    name,
			Reason: danglingReasonProfile,
			Origin: origin,
		})
		return nil, false
	}
	b.addNode(Node{
		Path:     target,
		Kind:     sectionKind(section),
		Name:     name,
		Origin:   origin,
		Declared: false,
		External: true,
	})
	return target, true
}

func sectionKind(section string) NodeKind {
	switch section {
	case "services":
		return NodeService
	case "networks":
		return NodeNetwork
	case "volumes":
		return NodeVolume
	case "configs":
		return NodeConfig
	case "secrets":
		return NodeSecret
	}
	return NodeKind(section)
}

// dependsOn handles both declared forms with one edge type, as story 2.2
// requires: the short array means service_started, and so does a long entry
// that omits `condition:`.
func (b *builder) dependsOn(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "depends_on")
	if !ok || v == nil {
		return
	}
	switch v.Kind() {
	case resolve.KindSequence:
		for _, item := range v.Seq() {
			name := scalarOf(item)
			if name == "" {
				continue
			}
			origin := item.Origin()
			to, ok := b.resolveTarget(from, EdgeDependsOn, "services", name, origin)
			if !ok {
				continue
			}
			b.edges = append(b.edges, Edge{
				Kind:    EdgeDependsOn,
				From:    from,
				To:      to,
				Origin:  origin,
				Depends: &DependsDetail{Condition: defaultDependsOnCondition},
			})
		}
	case resolve.KindMapping:
		m := v.Map()
		for _, name := range m.Keys() {
			entry, _ := m.Get(name)
			origin := declarationOrigin(m, name, entry)
			to, ok := b.resolveTarget(from, EdgeDependsOn, "services", name, origin)
			if !ok {
				continue
			}
			d := &DependsDetail{Condition: defaultDependsOnCondition}
			if c := scalarOf(mustField(entry, "condition")); c != "" {
				d.Condition = c
			}
			d.Restart = scalarOf(mustField(entry, "restart"))
			d.Required = scalarOf(mustField(entry, "required"))
			b.edges = append(b.edges, Edge{
				Kind:    EdgeDependsOn,
				From:    from,
				To:      to,
				Origin:  origin,
				Depends: d,
			})
		}
	}
}

// networks handles `networks:` on a service in both forms. The mapping form
// carries aliases; the sequence form carries none, which is an empty list
// rather than a nil one so a consumer never has to distinguish.
func (b *builder) networks(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "networks")
	if !ok || v == nil {
		return
	}
	add := func(name string, origin resolve.Origin, aliases []string) {
		to, ok := b.resolveTarget(from, EdgeNetwork, "networks", name, origin)
		if !ok {
			return
		}
		if aliases == nil {
			aliases = []string{}
		}
		b.edges = append(b.edges, Edge{
			Kind:   EdgeNetwork,
			From:   from,
			To:     to,
			Origin: origin,
			Attach: &AttachDetail{Aliases: aliases},
		})
	}
	switch v.Kind() {
	case resolve.KindSequence:
		for _, item := range v.Seq() {
			if name := scalarOf(item); name != "" {
				add(name, item.Origin(), nil)
			}
		}
	case resolve.KindMapping:
		m := v.Map()
		for _, name := range m.Keys() {
			entry, _ := m.Get(name)
			var aliases []string
			for _, a := range seqItems(mustField(entry, "aliases")) {
				if s := scalarOf(a); s != "" {
					aliases = append(aliases, s)
				}
			}
			add(name, declarationOrigin(m, name, entry), aliases)
		}
	}
}

// networkMode models `network_mode: service:x` as an edge of its own kind.
// It is not a network attachment: the two services share one namespace, which
// has different reachability and different failure modes, and folding it into
// EdgeNetwork would make them indistinguishable to a diagnostic.
func (b *builder) networkMode(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "network_mode")
	if !ok {
		return
	}
	mode := scalarOf(v)
	origin := v.Origin()
	switch {
	case strings.HasPrefix(mode, networkModeServicePrefix):
		name := strings.TrimPrefix(mode, networkModeServicePrefix)
		to, ok := b.resolveTarget(from, EdgeNetworkMode, "services", name, origin)
		if !ok {
			return
		}
		b.edges = append(b.edges, Edge{Kind: EdgeNetworkMode, From: from, To: to, Origin: origin})
	case strings.HasPrefix(mode, networkModeContainerPrefix):
		// A container outside this project. It is not a node — nothing in the
		// model declares it — so the reference is reported rather than
		// invented into existence.
		b.dangling = append(b.dangling, Dangling{
			Kind:   EdgeNetworkMode,
			From:   from,
			To:     resolve.Path{"services", strings.TrimPrefix(mode, networkModeContainerPrefix)},
			Ref:    mode,
			Reason: danglingReasonNotAService,
			Origin: origin,
		})
	}
	// host, bridge, none and the rest declare no relation to anything in the
	// project, so they produce no edge.
}

// mounts handles `volumes:` on a service: short string form and long mapping
// form, named volumes and host binds.
func (b *builder) mounts(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "volumes")
	if !ok {
		return
	}
	for _, item := range seqItems(v) {
		if item == nil {
			continue
		}
		switch item.Kind() {
		case resolve.KindScalar:
			b.shortMount(from, item.Scalar(), item.Origin())
		case resolve.KindMapping:
			b.longMount(from, item)
		}
	}
}

func (b *builder) shortMount(from resolve.Path, raw string, origin resolve.Origin) {
	source, target, options := splitMount(raw)
	if source == "" {
		// An anonymous volume: `- /var/lib/data`. Docker creates it at run
		// time, so there is nothing in the file to point an edge at. Recorded
		// as a dropped reference rather than silently ignored.
		b.dangling = append(b.dangling, Dangling{
			Kind:   EdgeVolume,
			From:   from,
			To:     from,
			Ref:    raw,
			Reason: danglingReasonAnonymousBind,
			Origin: origin,
		})
		return
	}
	mode := modeFromOptions(options)
	if isHostPath(source) {
		b.addBind(from, source, target, mode, origin)
		return
	}
	b.addVolume(from, source, target, mode, origin)
}

func (b *builder) longMount(from resolve.Path, item *resolve.Value) {
	origin := item.Origin()
	if m := item.Map(); m != nil {
		if o, ok := m.KeyOrigin(firstKey(m)); ok && !o.IsZero() {
			origin = o
		}
	}
	source := scalarOf(mustField(item, "source"))
	target := scalarOf(mustField(item, "target"))
	typ := scalarOf(mustField(item, "type"))
	mode := modeReadWrite
	if truthy(field(item, "read_only")) {
		mode = modeReadOnly
	}
	switch {
	case typ == "bind":
		b.addBind(from, source, target, mode, origin)
	case typ == "volume":
		if source == "" {
			return // an anonymous long-form volume names nothing
		}
		b.addVolume(from, source, target, mode, origin)
	case typ != "":
		// tmpfs, npipe, cluster: real mounts with no node on the far side.
		return
	case source == "":
		return
	case isHostPath(source):
		b.addBind(from, source, target, mode, origin)
	default:
		b.addVolume(from, source, target, mode, origin)
	}
}

func (b *builder) addVolume(from resolve.Path, source, target, mode string, origin resolve.Origin) {
	to, ok := b.resolveTarget(from, EdgeVolume, "volumes", source, origin)
	if !ok {
		return
	}
	b.edges = append(b.edges, Edge{
		Kind:   EdgeVolume,
		From:   from,
		To:     to,
		Origin: origin,
		Mount:  &MountDetail{Source: source, Target: target, Mode: mode},
	})
}

// addBind records a host bind. Its far side is the host, which is not a node
// in this project, so the edge points back at the service and carries the host
// path in the mount detail. Inventing a node per host directory would put
// paths in the graph that no compose file declares.
func (b *builder) addBind(from resolve.Path, source, target, mode string, origin resolve.Origin) {
	b.edges = append(b.edges, Edge{
		Kind:   EdgeBind,
		From:   from,
		To:     from,
		Origin: origin,
		Mount:  &MountDetail{Source: source, Target: target, Mode: mode, HostPath: source},
	})
}

func (b *builder) links(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "links")
	if !ok {
		return
	}
	for _, item := range seqItems(v) {
		raw := scalarOf(item)
		if raw == "" {
			continue
		}
		name, alias, hasAlias := strings.Cut(raw, ":")
		origin := item.Origin()
		to, ok := b.resolveTarget(from, EdgeLink, "services", name, origin)
		if !ok {
			continue
		}
		aliases := []string{}
		if hasAlias && alias != "" {
			aliases = append(aliases, alias)
		}
		b.edges = append(b.edges, Edge{
			Kind:   EdgeLink,
			From:   from,
			To:     to,
			Origin: origin,
			Attach: &AttachDetail{Aliases: aliases},
		})
	}
}

// fileMounts handles `configs:` and `secrets:` on a service, which share a
// grammar: a list of names, or a list of mappings with `source` and `target`.
func (b *builder) fileMounts(from resolve.Path, svc *resolve.Value, section string, kind EdgeKind) {
	v, ok := field(svc, section)
	if !ok {
		return
	}
	for _, item := range seqItems(v) {
		if item == nil {
			continue
		}
		var source, target string
		origin := item.Origin()
		switch item.Kind() {
		case resolve.KindScalar:
			source = item.Scalar()
		case resolve.KindMapping:
			source = scalarOf(mustField(item, "source"))
			target = scalarOf(mustField(item, "target"))
			if m := item.Map(); m != nil {
				if o, ok := m.KeyOrigin(firstKey(m)); ok && !o.IsZero() {
					origin = o
				}
			}
		default:
			continue
		}
		to, ok := b.resolveTarget(from, kind, section, source, origin)
		if !ok {
			continue
		}
		// A config or secret mount is read-only in Docker, always. Reporting
		// rw here would be a confident wrong answer.
		b.edges = append(b.edges, Edge{
			Kind:   kind,
			From:   from,
			To:     to,
			Origin: origin,
			Mount:  &MountDetail{Source: source, Target: target, Mode: modeReadOnly},
		})
	}
}

// ports creates a node per published port and attaches it to its service.
//
// The Origin on both the node and the edge is the position of the entry that
// declared it, which is what R3.1 needs to say "these two lines collide"
// rather than "these two services collide".
func (b *builder) ports(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "ports")
	if !ok || v == nil || v.Kind() != resolve.KindSequence {
		return
	}
	for i, item := range v.Seq() {
		if item == nil {
			continue
		}
		var detail PortDetail
		origin := item.Origin()
		switch item.Kind() {
		case resolve.KindScalar:
			detail = parseShortPort(item.Scalar())
		case resolve.KindMapping:
			detail = longPort(item)
			if m := item.Map(); m != nil {
				if o, ok := m.KeyOrigin(firstKey(m)); ok && !o.IsZero() {
					origin = o
				}
			}
		default:
			continue
		}
		path := from.Child("ports").Index(i)
		d := detail
		b.addNode(Node{
			Path:     path,
			Kind:     NodePort,
			Name:     detail.Raw,
			Origin:   origin,
			Declared: true,
			Port:     &d,
		})
		e := detail
		b.edges = append(b.edges, Edge{
			Kind:   EdgePublish,
			From:   from,
			To:     path,
			Origin: origin,
			Port:   &e,
		})
	}
}

// build creates the Dockerfile node a service's `build:` section names, and
// attaches it to that service.
//
// Both declared forms produce the same node: the string shorthand
// (`build: ./docs`) is a context with the default filename, and the mapping
// form may name the file and a target stage. A `build:` that declares neither
// a context nor a file names nothing, and nothing is what it gets — a node
// labelled `Dockerfile` for a build that does not identify one would be
// invented, not read.
func (b *builder) build(from resolve.Path, svc *resolve.Value) {
	v, ok := field(svc, "build")
	if !ok || v == nil {
		return
	}

	var d BuildDetail
	switch v.Kind() {
	case resolve.KindScalar:
		d.Context = v.Scalar()
	case resolve.KindMapping:
		d.Context = scalarOf(mustField(v, "context"))
		d.Dockerfile = scalarOf(mustField(v, "dockerfile"))
		d.Target = scalarOf(mustField(v, "target"))
		if _, inline := field(v, "dockerfile_inline"); inline {
			d.Inline = true
		}
	default:
		return
	}
	if !d.Inline {
		d.Reference = buildReference(d.Context, d.Dockerfile)
		if d.Reference == "" {
			return
		}
	}

	// The position of the `build` key itself, which is where a reader looks
	// and where story 6.3 has to place a cursor. The value's own origin is the
	// first line of the mapping under it, one line lower.
	origin := v.Origin()
	if m := svc.Map(); m != nil {
		if o, ok := m.KeyOrigin("build"); ok && !o.IsZero() {
			origin = o
		}
	}

	path := from.Child("build")
	name := d.Reference
	if d.Inline {
		name = "dockerfile_inline"
	}
	node := d
	b.addNode(Node{
		Path:     path,
		Kind:     NodeDockerfile,
		Name:     name,
		Origin:   origin,
		Declared: true,
		Build:    &node,
	})
	edge := d
	b.edges = append(b.edges, Edge{
		Kind:   EdgeBuild,
		From:   from,
		To:     path,
		Origin: origin,
		Build:  &edge,
	})
}

// buildReference joins a context and a dockerfile the way Compose reads them —
// the dockerfile is relative to the context — without touching a filesystem.
//
// It is a textual join and stays one: `${BUILD_CONTEXT}/api` is a legal
// context and normalising it as a path would either mangle the variable or
// resolve it against a directory this package has no business knowing.
func buildReference(context, dockerfile string) string {
	if dockerfile == "" {
		if context == "" {
			return ""
		}
		dockerfile = "Dockerfile"
	}
	switch {
	case context == "", context == ".", context == "./":
		return dockerfile
	// A dockerfile written absolutely, or as a URL-ish context's own path, is
	// already the whole answer.
	case strings.HasPrefix(dockerfile, "/"), strings.HasPrefix(dockerfile, "~"):
		return dockerfile
	// A git or URL context does not join with a path separator meaningfully;
	// naming both, in order, is the honest rendering.
	case strings.Contains(context, "://"):
		return context + " " + dockerfile
	}
	return strings.TrimSuffix(context, "/") + "/" + dockerfile
}

// ---------------------------------------------------------------------------
// Scalar-shaped parsing. Nothing here coerces: a port stays the text that was
// written, so a range and a quoted number survive as themselves.

// parseShortPort reads [HOST_IP:][PUBLISHED:]TARGET[/PROTOCOL].
func parseShortPort(raw string) PortDetail {
	d := PortDetail{Protocol: defaultProtocol, Raw: raw}
	rest := raw
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		if proto := rest[i+1:]; proto != "" {
			d.Protocol = proto
		}
		rest = rest[:i]
	}
	// An IPv6 bind address is bracketed, and its colons must not be split on.
	if strings.HasPrefix(rest, "[") {
		if j := strings.Index(rest, "]"); j > 0 {
			d.HostIP = rest[1:j]
			rest = strings.TrimPrefix(rest[j+1:], ":")
		}
	}
	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 0:
	case 1:
		d.Target = parts[0]
	case 2:
		d.Published, d.Target = parts[0], parts[1]
	default:
		if d.HostIP == "" {
			d.HostIP = parts[0]
			parts = parts[1:]
		}
		d.Published, d.Target = parts[0], parts[len(parts)-1]
	}
	return d
}

func longPort(item *resolve.Value) PortDetail {
	d := PortDetail{
		HostIP:    scalarOf(mustField(item, "host_ip")),
		Published: scalarOf(mustField(item, "published")),
		Target:    scalarOf(mustField(item, "target")),
		Protocol:  scalarOf(mustField(item, "protocol")),
	}
	if d.Protocol == "" {
		d.Protocol = defaultProtocol
	}
	d.Raw = d.Published
	if d.Raw == "" {
		d.Raw = d.Target
	} else {
		d.Raw += ":" + d.Target
	}
	if d.Protocol != defaultProtocol {
		d.Raw += "/" + d.Protocol
	}
	return d
}

// splitMount reads [SOURCE:]TARGET[:OPTIONS] from the short volume syntax.
//
// A Windows drive letter is rejoined before splitting: `C:\data:/data:ro` has
// four colon-separated fields and three of them are not what they look like.
func splitMount(raw string) (source, target, options string) {
	parts := strings.Split(raw, ":")
	if len(parts) >= 2 && len(parts[0]) == 1 && isASCIILetter(parts[0][0]) &&
		(strings.HasPrefix(parts[1], `\`) || strings.HasPrefix(parts[1], "/")) {
		parts = append([]string{parts[0] + ":" + parts[1]}, parts[2:]...)
	}
	switch len(parts) {
	case 0:
		return "", "", ""
	case 1:
		return "", parts[0], ""
	case 2:
		return parts[0], parts[1], ""
	default:
		return parts[0], parts[1], strings.Join(parts[2:], ",")
	}
}

// modeFromOptions reads ro/rw out of a short-form option list. Compose's
// default is rw, so an absent option is reported as rw rather than as nothing:
// the question a reader asks is "can it write", and "" is not an answer.
func modeFromOptions(options string) string {
	for _, opt := range strings.Split(options, ",") {
		switch strings.TrimSpace(opt) {
		case modeReadOnly:
			return modeReadOnly
		case modeReadWrite:
			return modeReadWrite
		}
	}
	return modeReadWrite
}

// isHostPath decides bind versus named volume the way Compose does: a named
// volume is a bare name, and anything with a separator or a leading dot, tilde
// or variable is a path on the host.
func isHostPath(source string) bool {
	if source == "" {
		return false
	}
	if strings.ContainsAny(source, `/\`) {
		return true
	}
	switch source[0] {
	case '.', '~', '$':
		return true
	}
	return false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ---------------------------------------------------------------------------
// resolve.Value accessors, nil-safe throughout. A compose file in the wild
// puts a scalar where a mapping belongs often enough that every one of these
// has to survive it without panicking.

func field(v *resolve.Value, k string) (*resolve.Value, bool) {
	if v == nil || v.Kind() != resolve.KindMapping {
		return nil, false
	}
	return v.Map().Get(k)
}

// mustField is field for the common case where absent and nil are the same
// answer to the caller.
func mustField(v *resolve.Value, k string) *resolve.Value {
	got, _ := field(v, k)
	return got
}

func scalarOf(v *resolve.Value) string {
	if v == nil || v.Kind() != resolve.KindScalar {
		return ""
	}
	return v.Scalar()
}

func seqItems(v *resolve.Value) []*resolve.Value {
	if v == nil || v.Kind() != resolve.KindSequence {
		return nil
	}
	return v.Seq()
}

// truthy reads a YAML boolean without coercing the model. `external:` with a
// mapping under it is the deprecated `external: {name: x}` form, which is
// still an assertion that the resource is external.
func truthy(v *resolve.Value, ok bool) bool {
	if !ok || v == nil {
		return false
	}
	switch v.Kind() {
	case resolve.KindMapping:
		return true
	case resolve.KindScalar:
		switch strings.ToLower(v.Scalar()) {
		case "true", "yes", "on", "1":
			return true
		}
	}
	return false
}

func firstKey(m *resolve.OrderedMap) string {
	keys := m.Keys()
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
