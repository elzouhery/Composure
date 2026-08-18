package resolve

// Loading a project: the file chain, the automatic override pickup, `include:`
// and the ordered source-file list that Origin.Step indexes.
//
// Stories 1.4, 1.6 and 1.7 live here. The order of operations is the part worth
// stating, because every one of them is a decision:
//
//  1. Decide the ordered entry file list. Explicit `-f` files are used exactly
//     as given and DISABLE the automatic `compose.override.yaml` pickup, which
//     is what Compose does; a directory gets the candidate order plus its
//     override file.
//  2. Load each file: parse, interpolate against that file's own environment
//     (AD-11 — per file, before merge), convert to positioned values.
//  3. Expand that file's `include:` — recursively, every path relative to the
//     file that declared it — and merge the file's own content ON TOP of what
//     it included, because a local declaration wins.
//  4. Merge the chain left to right through the one rule table (AD-4).
//  5. Apply `extends:` to the merged model (story 1.8).
//  6. Sweep `!reset` declarations that had nothing left to remove.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Options describes a project to load. The zero value loads the current
// directory the way `docker compose` would.
type Options struct {
	// Files is an explicit chain, merged left to right — the `-f` form. When
	// it is set, the automatic override pickup is disabled, matching Compose.
	Files []string
	// Dir is the project directory, used only when Files is empty. Empty
	// means the working directory.
	Dir string
	// Env is an explicit variable environment for interpolation. It wins over
	// the host environment and over `.env`; it is how a test states its inputs
	// without exporting anything, and where a future `--env-file` lands.
	Env map[string]string
	// IgnoreHostEnv drops the process environment from the interpolation
	// layers. A corpus sweep needs this: with the host environment in play the
	// same file resolves differently on two machines, and a differential
	// harness cannot tell a merge bug from a stray exported variable.
	IgnoreHostEnv bool
}

// Load resolves a project.
//
// It returns a Project or an error, never both and never a partial model
// (AD-13). A variable nobody defines is a finding on the Project, not an error;
// `${VAR:?message}` is the one form that fails, because that is what it is for.
func Load(opts Options) (*Project, error) {
	entries, err := entryFiles(opts)
	if err != nil {
		return nil, err
	}

	l := &loader{opts: opts, steps: map[string]int{}, envFileSourced: map[string]bool{}}
	// Register the whole chain up front, so that Origin.Step is the position
	// in the chain a caller passed — story 1.6's acceptance criterion — and
	// files discovered later through include and extends append after it.
	//
	// One entry per POSITION, never deduplicated. `docker compose -f base -f
	// override -f base` is legal and used — the third file re-asserts what the
	// second changed — and collapsing the repeat made Origin.Step stop
	// identifying a position in the chain: values set by the third file
	// reported Step 0. AD-15 requires a step number to mean something on its
	// own, and it cannot if two positions share one.
	chainSteps := make([]int, len(entries))
	for i, f := range entries {
		chainSteps[i] = l.addStep(f)
	}

	var root *Value
	for i, f := range entries {
		doc, err := l.loadDocAt(f, i == 0, chainSteps[i])
		if err != nil {
			return nil, err
		}
		if root == nil {
			root = doc
			continue
		}
		merged, _ := l.mergeInto(Path{}, root, doc)
		root = merged
	}

	if err := l.applyExtends(root); err != nil {
		return nil, err
	}
	dropResets(root)
	l.noteEmptyProfileNames(root)

	// The merged project must still be a compose project. A chain whose files
	// are individually plausible but which together declare no services is a
	// mistake worth naming rather than an empty model worth trusting.
	if err := validate(entries[0], root, true); err != nil {
		return nil, err
	}

	return l.project(root), nil
}

// File resolves a single compose file. Naming a file explicitly disables the
// override pickup, exactly as `docker compose -f` does.
func File(path string) (*Project, error) { return Load(Options{Files: []string{path}}) }

// Files resolves an explicit chain, merged left to right (story 1.6).
func Files(paths ...string) (*Project, error) { return Load(Options{Files: paths}) }

// Dir resolves a project directory: the Compose candidate order, plus the
// automatic override file if one is there.
func Dir(dir string) (*Project, error) { return Load(Options{Dir: dir}) }

// loader carries the state one Load shares: the ordered file registry, the
// findings, and the stacks that stop include and extends recursing forever.
type loader struct {
	opts  Options
	files []SourceFile
	steps map[string]int

	findings       []Finding
	envFileSourced map[string]bool

	// includeStack is the chain of files currently being included, by absolute
	// path. A target already on it is a cycle.
	includeStack []string
	// docs caches a loaded file by absolute path. `extends:` across files
	// reads the same base file once per extending service otherwise, and each
	// read would register another step for a file already in the list.
	docs map[string]*Value
	// extended caches a service with its `extends:` chain already applied,
	// keyed by file and name. Ten services extending one base resolve the base
	// once.
	extended map[string]*Value
}

// step registers a file DISCOVERED during resolution — an `include:` target, an
// `extends: file:` target — and returns its index. The path is kept as it was
// given, that being what Origin.File reports and what a caller opens, while
// identity is by absolute path: a file two `include:`s reach by two spellings
// is one step, not two.
//
// The explicit `-f` chain does not come through here. Its positions are
// assigned by addStep, because there a repeat is a position rather than a
// duplicate. See Load.
func (l *loader) step(path string) int {
	key := absKey(path)
	if s, ok := l.steps[key]; ok {
		return s
	}
	return l.addStep(path)
}

// addStep appends a position unconditionally and returns its index.
//
// The first position a path takes is the one a later lookup by path resolves
// to, so a file that is both an entry and an include target reports the entry's
// step for the include.
func (l *loader) addStep(path string) int {
	key := absKey(path)
	s := len(l.files)
	if _, ok := l.steps[key]; !ok {
		l.steps[key] = s
	}
	l.files = append(l.files, SourceFile{Path: path, Step: s})
	return s
}

func absKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func (l *loader) project(root *Value) *Project {
	names := make([]string, 0, len(l.envFileSourced))
	for n := range l.envFileSourced {
		names = append(names, n)
	}
	sort.Strings(names)
	findings := l.findings
	if findings == nil {
		findings = []Finding{}
	}
	return &Project{
		files:          append([]SourceFile{}, l.files...),
		root:           root,
		findings:       findings,
		envFileSourced: names,
	}
}

// entryFiles decides the ordered chain to merge.
func entryFiles(opts Options) ([]string, error) {
	if len(opts.Files) > 0 {
		for _, f := range opts.Files {
			// Every file is checked before anything is read: a chain with a
			// missing third file must resolve nothing at all, rather than
			// half-merging and then failing.
			if st, err := os.Stat(f); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil, &FileError{File: f, Err: ErrNotFound}
				}
				return nil, &FileError{File: f, Err: err}
			} else if st.IsDir() {
				return nil, &FileError{File: f, Err: ErrNotFound}
			}
		}
		return append([]string{}, opts.Files...), nil
	}

	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	base, ok := discoverCompose(dir)
	if !ok {
		return nil, &FileError{File: filepath.Join(dir, "compose.yaml"), Err: ErrNotFound}
	}
	out := []string{base}
	if ov, ok := discoverOverride(base); ok {
		out = append(out, ov)
	}
	return out, nil
}

// composeCandidates is Compose's own order for finding the project file.
var composeCandidates = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

func discoverCompose(dir string) (string, bool) {
	for _, c := range composeCandidates {
		p := filepath.Join(dir, c)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// discoverOverride finds the override file that goes with a base file:
// `compose.yaml` takes `compose.override.yaml` or `.yml`, and
// `docker-compose.yml` takes `docker-compose.override.yml` or `.yaml`. The
// prefix is the base file's own name, so a project that uses the legacy
// spelling does not silently pick up an override written for the new one.
func discoverOverride(base string) (string, bool) {
	dir := filepath.Dir(base)
	name := filepath.Base(base)
	stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
	for _, ext := range []string{".yaml", ".yml"} {
		p := filepath.Join(dir, stem+".override"+ext)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// loadDoc reads one file DISCOVERED during resolution — an `include:` target, an
// `extends: file:` target — into a positioned value tree, with its `include:`
// already expanded. Its step is assigned by path, so a file reached twice is one
// step.
func (l *loader) loadDoc(path string, strict bool) (*Value, error) {
	return l.loadDocAt(path, strict, -1)
}

// loadDocAt is loadDoc with the chain position spelled out. A step of -1 means
// "assign one by path", which is what every discovered file gets; a step of 0 or
// more is a position in the explicit `-f` chain and is used verbatim.
//
// A file loaded at an explicit step BYPASSES THE CACHE in both directions, and
// that is the whole reason this function is separate.
//
// The cache holds one converted tree per absolute path, and every Origin in that
// tree carries the step and the path spelling of the load that filled it. Serving
// the third entry of `-f base -f override -f base` from it therefore handed back
// values stamped with the FIRST entry's step and the first entry's spelling of
// the path — the same defect the step dedupe caused, arriving by a second route
// and just as silent. Re-stamping the clone's origins was the tempting fix and is
// wrong: a document's tree also holds values that came from files it pulled in
// through `include:`, and those carry their own steps. Walking the tree to
// overwrite every step would flatten an included file's provenance onto the
// including file's chain position, which is a worse answer than the one being
// fixed. So a repeated entry is simply read again, at its own position. It costs
// one file read on a chain that names the same file twice.
//
// Only the document's OWN nodes take the given step: expandIncludes below calls
// loadDoc, not loadDocAt, so everything it pulls in keeps the step its own path
// earns.
func (l *loader) loadDocAt(path string, strict bool, step int) (*Value, error) {
	cacheable := step < 0
	if cacheable {
		if v, ok := l.docs[absKey(path)]; ok {
			return clone(v), nil
		}
	}
	src, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &FileError{File: path, Err: ErrNotFound}
		}
		return nil, &FileError{File: path, Err: err}
	}
	doc, err := l.convertFileAt(path, src, strict, step)
	if err != nil {
		return nil, err
	}
	doc, err = l.expandIncludes(path, doc)
	if err != nil {
		return nil, err
	}
	if !cacheable {
		return doc, nil
	}
	if l.docs == nil {
		l.docs = map[string]*Value{}
	}
	l.docs[absKey(path)] = doc
	return clone(doc), nil
}

// convertFile parses, interpolates and converts one document. It does not
// expand include or extends — that is the caller's job, and keeping it out of
// here is what lets `Bytes` resolve an in-memory document by the same route.
func (l *loader) convertFile(path string, src []byte, strict bool) (*Value, error) {
	return l.convertFileAt(path, src, strict, -1)
}

// convertFileAt is convertFile with the chain position spelled out; -1 means
// "assign one by path". See loadDocAt.
func (l *loader) convertFileAt(path string, src []byte, strict bool, step int) (*Value, error) {
	node, err := parseDocument(path, src)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	// Bootstrap environment first, because the `env_file` paths may themselves
	// be interpolated and cannot be read until they are. Compose has the same
	// ordering problem and solves it the same way.
	bootstrap := buildEnv(dir, l.opts.Env, nil, !l.opts.IgnoreHostEnv)
	env := buildEnv(dir, l.opts.Env, envFilePaths(node, dir, bootstrap), !l.opts.IgnoreHostEnv)

	if step < 0 {
		step = l.step(path)
	}
	c := &convCtx{
		file:   path,
		step:   step,
		active: map[*yaml.Node]bool{},
		budget: maxAliasNodes,
		env:    env,
		loader: l,
	}
	root, cerr := c.convert(node, 0, Path{})
	if cerr != nil {
		var ie *InterpolationError
		if errors.As(cerr, &ie) {
			return nil, cerr
		}
		return nil, &FileError{File: path, Err: cerr}
	}
	for name := range env.fromEnvFile {
		l.envFileSourced[name] = true
	}
	if err := validate(path, root, strict); err != nil {
		return nil, err
	}
	return root, nil
}

// validate applies the "is this a compose file at all" rules.
//
// requireServices is true for the entry file and for anything loaded as a
// project in its own right — an `include:` target, an `extends: file:` target.
// It is false for the later files of a chain: an override file that only tunes
// `networks:` is legal, and refusing it would refuse a real project.
func validate(name string, root *Value, requireServices bool) error {
	if root == nil || root.Kind() != KindMapping {
		return &FileError{File: name, Err: ErrNotCompose}
	}
	if svc, ok := root.Map().Get("services"); ok {
		if svc.Kind() != KindMapping && svc.Kind() != KindNull {
			return &FileError{File: name, Err: ErrNotCompose}
		}
		return nil
	}
	if looksLikeTopLevelServices(root) {
		return &FileError{File: name, Err: ErrLegacyFormat}
	}
	if requireServices {
		return &FileError{File: name, Err: ErrNotCompose}
	}
	// No services, but nothing that looks like a mis-declared one either. A
	// fragment that only carries `networks:` or `x-` keys is a legal member of
	// a chain.
	return nil
}

// envFilePaths reads every `env_file` a document declares, resolved relative to
// the document's directory.
//
// R1.5 asks for `env_file` as an interpolation source. Compose itself does not
// consult it for interpolation — see varEnv — so these land in the lowest
// layer, where they can only fill in a name that would otherwise be empty.
func envFilePaths(node *yaml.Node, dir string, boot *varEnv) []string {
	var out []string
	add := func(s string) {
		s = interpolate(s, boot).text
		if s = strings.TrimSpace(s); s == "" {
			return
		}
		if !filepath.IsAbs(s) {
			s = filepath.Join(dir, s)
		}
		out = append(out, s)
	}

	services := yamlChild(node, "services")
	if services == nil {
		return nil
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		ef := yamlChild(services.Content[i+1], "env_file")
		if ef == nil {
			continue
		}
		switch ef.Kind {
		case yaml.ScalarNode:
			add(ef.Value)
		case yaml.SequenceNode:
			for _, e := range ef.Content {
				switch e.Kind {
				case yaml.ScalarNode:
					add(e.Value)
				case yaml.MappingNode:
					if p := yamlChild(e, "path"); p != nil && p.Kind == yaml.ScalarNode {
						add(p.Value)
					}
				}
			}
		}
	}
	return out
}

// ---- include ---------------------------------------------------------------

// expandIncludes resolves a document's `include:` and returns the document
// merged on top of everything it included.
//
// Precedence is the file's own: an included project supplies declarations, and
// the including file overrides them. Compose reads it the same way, and the
// other order would make `include:` able to silently change the file that used
// it.
func (l *loader) expandIncludes(path string, doc *Value) (*Value, error) {
	inc, ok := doc.Map().Get("include")
	if !ok {
		return doc, nil
	}
	// The directive itself is not configuration and does not reach the model,
	// exactly as `<<:` does not.
	doc.mapping.remove("include")

	key := absKey(path)
	for _, seen := range l.includeStack {
		if seen == key {
			return nil, &IncludeCycleError{Cycle: append(append([]string{}, l.includeStack...), path)}
		}
	}
	l.includeStack = append(l.includeStack, key)
	defer func() { l.includeStack = l.includeStack[:len(l.includeStack)-1] }()

	var entries []*Value
	switch inc.Kind() {
	case KindSequence:
		entries = inc.Seq()
	case KindScalar, KindMapping:
		entries = []*Value{inc}
	default:
		return nil, &FileError{File: path, Err: ErrBadInclude}
	}

	dir := filepath.Dir(path)
	var included *Value
	for _, entry := range entries {
		paths, err := includePaths(path, dir, entry)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			sub, err := l.loadDoc(p, true)
			if err != nil {
				return nil, err
			}
			if included == nil {
				included = sub
				continue
			}
			merged, _ := l.mergeInto(Path{}, included, sub)
			included = merged
		}
	}
	if included == nil {
		return doc, nil
	}
	merged, _ := l.mergeInto(Path{}, included, doc)
	return merged, nil
}

// includePaths reads one `include:` entry into the ordered list of files it
// names, every one of them relative to the FILE THAT DECLARED IT — never the
// working directory. That is R1.2's whole content, and getting it wrong makes a
// project resolve differently depending on where the user was standing.
func includePaths(declaring, dir string, entry *Value) ([]string, error) {
	resolveRel := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || filepath.IsAbs(s) {
			return s
		}
		return filepath.Join(dir, s)
	}

	switch entry.Kind() {
	case KindScalar:
		p := resolveRel(entry.Scalar())
		if p == "" {
			return nil, &FileError{File: declaring, Err: ErrBadInclude}
		}
		return []string{p}, nil

	case KindMapping:
		// `project_directory` re-bases the paths, which is how a shared
		// fragment refers to files next to itself.
		if pd, ok := entry.Map().Get("project_directory"); ok && pd.Kind() == KindScalar {
			if s := strings.TrimSpace(pd.Scalar()); s != "" {
				if filepath.IsAbs(s) {
					dir = s
				} else {
					dir = filepath.Join(dir, s)
				}
			}
		}
		pv, ok := entry.Map().Get("path")
		if !ok {
			return nil, &FileError{File: declaring, Err: ErrBadInclude}
		}
		switch pv.Kind() {
		case KindScalar:
			return []string{resolveRel(pv.Scalar())}, nil
		case KindSequence:
			var out []string
			for _, e := range pv.Seq() {
				if e.Kind() != KindScalar {
					return nil, &FileError{File: declaring, Err: ErrBadInclude}
				}
				out = append(out, resolveRel(e.Scalar()))
			}
			if len(out) == 0 {
				return nil, &FileError{File: declaring, Err: ErrBadInclude}
			}
			return out, nil
		}
	}
	return nil, &FileError{File: declaring, Err: ErrBadInclude}
}

// IncludeCycleError names the cycle rather than recursing until the stack is
// gone. Stack exhaustion is fatal in Go — recover cannot catch it — so a cycle
// is detected, not survived.
type IncludeCycleError struct {
	Cycle []string
}

func (e *IncludeCycleError) Error() string {
	return fmt.Sprintf("include cycle: %s", strings.Join(e.Cycle, " -> "))
}

func (e *IncludeCycleError) Unwrap() error { return ErrIncludeCycle }
