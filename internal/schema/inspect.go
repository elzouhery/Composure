package schema

import (
	"sort"
	"strings"

	"github.com/elzouhery/composure/internal/resolve"
)

// Options is everything Inspect reads that is not the project.
//
// Every input is injectable. The Compose probe and the environment are both
// facts about the machine, and a package that reaches for them itself cannot
// be tested for the two cases that matter most: no binary present, and no
// environment established.
type Options struct {
	// At is the config path to describe, empty for the stack itself.
	At string
	// All also describes every service and every top-level resource. The CLI
	// wants that; the inspector asks for one node at a time.
	All bool
	// Env is the variable environment `${VAR}` is resolved against, and
	// EnvKnown says whether one was established at all. A nil Env and an empty
	// one are different claims: with EnvKnown false the inspector shows the
	// literal and says nothing about what it resolves to, because silence is
	// not evidence.
	Env      map[string]string
	EnvKnown bool
	// ComposeVersion and ComposeVersionKnown are AD-21's input. Left zero,
	// Inspect probes the machine once.
	ComposeVersion      string
	ComposeVersionKnown bool
	// ChildDepth is how far to descend into declared mappings, so that a
	// declared `healthcheck` can carry its own `available, not set` line. One
	// level is what the inspector renders; deeper is available to a caller
	// that wants it and costs payload.
	ChildDepth int
	// Probed is set by Inspect when it ran the probe itself. Callers do not
	// set it.
	Probed bool
}

// Node is one config path described: what may be written there, and what is.
type Node struct {
	// Path is the config path, empty for the stack itself.
	Path string `json:"path"`
	// Schema names the specification node — "compose", "service", "network".
	Schema string `json:"schema"`
	// Known is false for a path the vendored specification does not describe:
	// an `x-` extension, or a key from a Compose newer than the vendored
	// schema. The declared values still render. What cannot be shown is the
	// available list, and the inspector says so rather than showing an empty
	// one, which would read as "nothing else is possible".
	Known bool `json:"known"`
	// Title is the schema's own one-line description of the node.
	Title  string  `json:"title,omitempty"`
	Fields []Field `json:"fields"`
	// DeclaredCount and AvailableCount are the two halves, counted once here
	// so every surface reports the same numbers.
	DeclaredCount  int `json:"declared_count"`
	AvailableCount int `json:"available_count"`
	// FreeForm marks a node whose keys the schema does not name.
	FreeForm bool `json:"free_form,omitempty"`
	// Missing is true when the path names nothing in the resolved model. The
	// schema half still answers: "what could go here" is a question worth
	// answering about a service that does not exist yet.
	Missing bool `json:"missing,omitempty"`
}

// Result is a whole `composure schema` answer.
type Result struct {
	// Path is the entry compose file, as the caller named it.
	Path string `json:"path"`
	// SchemaCommit pins which revision of the specification produced this. It
	// is on the wire because "which spec is this from" is the question the
	// available list's credibility rests on.
	SchemaCommit string `json:"schema_commit"`
	// ComposeVersion is the `docker compose` on this machine, empty and
	// ComposeVersionKnown false when there is none — in which case nothing is
	// marked (AD-21).
	ComposeVersion      string `json:"compose_version"`
	ComposeVersionKnown bool   `json:"compose_version_known"`
	// Files is the project's ordered source list. Origin.Step indexes it, and
	// the inspector shows it as the stack's own property when nothing is
	// selected (story 5.1).
	Files []resolve.SourceFile `json:"files"`
	// Profiles is every profile the project declares, sorted. Also a
	// stack-level property.
	Profiles []string `json:"profiles"`
	// VersionField is set when the file carries the obsolete top-level
	// `version:` key. It is reported, never acted on: the schema is single and
	// current, and Compose ignores the field too (AD-20). The matching
	// hint-level finding comes from internal/diagnose.
	VersionField string `json:"version_field,omitempty"`
	// Node is the requested path.
	Node *Node `json:"node,omitempty"`
	// Nodes is every node, present only when Options.All.
	Nodes []Node `json:"nodes,omitempty"`
}

// Inspect describes what the specification permits against what the project
// declares.
func Inspect(entry string, project *resolve.Project, opt Options) (*Result, error) {
	if project == nil {
		return nil, ErrNoProject
	}
	spec, err := Load()
	if err != nil {
		return nil, err
	}
	if !opt.ComposeVersionKnown && opt.ComposeVersion == "" {
		opt.ComposeVersion, opt.ComposeVersionKnown = InstalledCompose()
		opt.Probed = true
	}
	if opt.ChildDepth == 0 {
		opt.ChildDepth = 1
	}

	res := &Result{
		Path:                entry,
		SchemaCommit:        spec.Commit(),
		ComposeVersion:      opt.ComposeVersion,
		ComposeVersionKnown: opt.ComposeVersionKnown,
		Files:               project.Files(),
		Profiles:            declaredProfiles(project),
	}
	if res.Files == nil {
		res.Files = []resolve.SourceFile{}
	}
	// AD-20: the field is read so it can be reported, and then ignored. It
	// never selects a schema — there is only one, and it is current.
	if v, ok := project.At(resolve.Path{"version"}); ok && v.Kind() == resolve.KindScalar {
		res.VersionField = v.Scalar()
	}

	node, err := describeNode(spec, project, resolve.ParsePath(opt.At), opt)
	if err != nil {
		return nil, err
	}
	res.Node = node

	if opt.All {
		res.Nodes = append(res.Nodes, *node)
		for _, p := range allPaths(project) {
			if p.String() == opt.At {
				continue
			}
			n, err := describeNode(spec, project, p, opt)
			if err != nil {
				continue
			}
			res.Nodes = append(res.Nodes, *n)
		}
	}
	return res, nil
}

// Describe is Inspect for one path, without the project-level header. It is
// the entry point a caller that already knows the machine's Compose wants.
func Describe(project *resolve.Project, at resolve.Path, opt Options) (*Node, error) {
	if project == nil {
		return nil, ErrNoProject
	}
	spec, err := Load()
	if err != nil {
		return nil, err
	}
	if opt.ChildDepth == 0 {
		opt.ChildDepth = 1
	}
	return describeNode(spec, project, at, opt)
}

func describeNode(spec *Spec, project *resolve.Project, at resolve.Path, opt Options) (*Node, error) {
	schemaNode, known := spec.At(at)
	value, present := project.At(at)
	if len(at) > 0 && !present && !known {
		return nil, ErrUnknownPath
	}

	out := &Node{
		Path:    at.String(),
		Schema:  schemaLabel(at),
		Known:   known,
		Missing: !present,
	}
	if known {
		out.Title = descriptionLine(spec, schemaNode)
		out.FreeForm = spec.FreeForm(schemaNode)
	}
	e := env{vars: opt.Env, known: opt.EnvKnown}
	out.Fields = fieldsFor(spec, schemaNode, known, value, at, e, opt, opt.ChildDepth)
	for _, f := range out.Fields {
		if f.Declared {
			out.DeclaredCount++
		} else {
			out.AvailableCount++
		}
	}
	return out, nil
}

// fieldsFor is the whole of AD-20 in one function.
//
// The permitted set comes from the schema and the declared set comes from the
// resolved model; the two are unioned, never intersected. A key the file
// declares that the schema does not name — an `x-` extension, or a key from a
// Compose newer than the vendored spec — is still shown with its value: the
// inspector reports the file, and hiding part of the file because our schema
// is out of date would be the worst possible failure for a tool whose claim is
// that it shows you what is there.
//
// Declared keys come first, in FILE order, because that is the order the
// reader is scrolling in the editor beside the pane. Everything else follows
// alphabetically.
func fieldsFor(
	spec *Spec,
	schemaNode map[string]any,
	known bool,
	value *resolve.Value,
	at resolve.Path,
	e env,
	opt Options,
	depth int,
) []Field {
	declared := map[string]*resolve.Value{}
	var order []string
	if value != nil && value.Kind() == resolve.KindMapping {
		m := value.Map()
		for _, k := range m.Keys() {
			child, _ := m.Get(k)
			declared[k] = child
			order = append(order, k)
		}
	}

	var available []string
	freeForm := known && spec.FreeForm(schemaNode)
	if known && !freeForm {
		for _, k := range spec.PropertyNames(schemaNode) {
			if _, isDeclared := declared[k]; !isDeclared {
				available = append(available, k)
			}
		}
		sort.Strings(available)
	}

	out := make([]Field, 0, len(order)+len(available))
	for _, key := range order {
		childPath := at.Child(key)
		var childSchema map[string]any
		childKnown := false
		if known {
			childSchema, childKnown = spec.property(schemaNode, key)
			if !childKnown {
				// A free-form map's entries have a schema too — the value
				// schema behind patternProperties — even though the KEY is not
				// named. Reaching for it is what gives `environment` entries a
				// type.
				childSchema, childKnown = spec.Child(schemaNode, key)
			}
		}
		f := spec.describe(key, childPath.String(), childSchema)
		f.Declared = true
		f.Support = supportOf(f.MinVersion, opt.ComposeVersion, opt.ComposeVersionKnown)
		v := viewOf(declared[key], childPath, e, 0)
		f.Value = &v
		if childKnown && childSchema != nil {
			f.FreeForm = spec.FreeForm(childSchema)
		}
		// Descend into a declared mapping so the group can carry its own
		// available list — a `healthcheck` written without `start_period`
		// teaches that `start_period` exists.
		if depth > 0 && declared[key].Kind() == resolve.KindMapping && childKnown && !f.FreeForm {
			f.Children = fieldsFor(spec, childSchema, true, declared[key], childPath, e, opt, depth-1)
		}
		out = append(out, f)
	}

	for _, key := range available {
		childSchema, _ := spec.property(schemaNode, key)
		f := spec.describe(key, at.Child(key).String(), childSchema)
		f.Declared = false
		f.Support = supportOf(f.MinVersion, opt.ComposeVersion, opt.ComposeVersionKnown)
		out = append(out, f)
	}
	return out
}

// schemaLabel names the specification node a path lands on, for display.
//
// Derived from the path's shape rather than from a table of names, so a path
// nobody anticipated still gets a sensible label instead of "unknown".
func schemaLabel(at resolve.Path) string {
	if len(at) == 0 {
		return "compose"
	}
	if len(at) == 2 {
		switch at[0] {
		case "services":
			return "service"
		case "networks":
			return "network"
		case "volumes":
			return "volume"
		case "configs":
			return "config"
		case "secrets":
			return "secret"
		case "models":
			return "model"
		}
	}
	last := at[len(at)-1]
	// A sequence index is not a name. `services.gateway.ports[0]` labelled "0"
	// tells the reader nothing; "ports entry" tells them what they selected.
	if isIndex(last) && len(at) >= 2 {
		return at[len(at)-2] + " entry"
	}
	return last
}

// allPaths lists every node a reader can select: the services and the
// top-level resources, in the file's own order.
func allPaths(p *resolve.Project) []resolve.Path {
	var out []resolve.Path
	add := func(top string, m *resolve.OrderedMap) {
		for _, k := range m.Keys() {
			out = append(out, resolve.Path{top, k})
		}
	}
	add("services", p.Services())
	add("networks", p.Networks())
	add("volumes", p.Volumes())
	add("configs", p.Configs())
	add("secrets", p.Secrets())
	return out
}

// declaredProfiles is every profile any service names, sorted and deduplicated.
// A stack-level property: story 5.1 asks for the declared profiles when nothing
// is selected.
func declaredProfiles(p *resolve.Project) []string {
	seen := map[string]bool{}
	var out []string
	services := p.Services()
	for _, name := range services.Keys() {
		svc, _ := services.Get(name)
		profiles, ok := svc.At(resolve.Path{"profiles"})
		if !ok || profiles.Kind() != resolve.KindSequence {
			continue
		}
		for _, item := range profiles.Seq() {
			name := strings.TrimSpace(item.Scalar())
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}
