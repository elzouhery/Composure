package resolve

// Story 1.8 — `extends:`, in the same file and across files.
//
// Applied AFTER the file chain has merged, which is where Compose applies it
// too. The order is load-bearing: an override file is allowed to change or add
// an `extends:`, and resolving extends per file would apply the base the first
// file named and then never revisit it.
//
// The rules differ from the file-merge rules in exactly one way, and it is the
// one the spec is explicit about: `depends_on`, `volumes_from` and `links` are
// NOT inherited. They name other services, and the services they name need not
// exist in the file being extended — inheriting them produces a dependency on
// something that was never declared. Everything else goes through the same
// table as every other merge (AD-4).

import (
	"fmt"
	"path/filepath"
	"strings"
)

// extendsExcluded are the keys a base service never contributes.
//
// They are excluded rather than merged because each names other services, and
// a service name is only meaningful inside the project that declares it.
var extendsExcluded = []string{"depends_on", "volumes_from", "links"}

// applyExtends resolves every service's `extends:` in the merged model.
func (l *loader) applyExtends(root *Value) error {
	if root == nil || root.Kind() != KindMapping {
		return nil
	}
	services, ok := root.Map().Get("services")
	if !ok || services.Kind() != KindMapping {
		return nil
	}
	for _, name := range services.Map().Keys() {
		v, err := l.extendService("", name, root, nil)
		if err != nil {
			return err
		}
		ko, _ := services.mapping.KeyOrigin(name)
		services.mapping.set(name, v, ko)
	}
	return nil
}

// extendKey identifies a service for cycle detection and caching. The empty
// file means the merged project rather than a file on disk.
func extendKey(file, name string) string {
	if file == "" {
		return "<project>|" + name
	}
	return absKey(file) + "|" + name
}

// extendService returns a service with its `extends:` chain applied.
//
// project is the merged model, used when file is empty. chain is the stack of
// services currently being extended, which is what turns a cycle into a named
// error instead of a stack overflow.
func (l *loader) extendService(file, name string, project *Value, chain []string) (*Value, error) {
	key := extendKey(file, name)
	if v, ok := l.extended[key]; ok {
		return clone(v), nil
	}
	for i, seen := range chain {
		if seen == key {
			return nil, &ExtendsError{
				Service: name,
				File:    file,
				Cycle:   append(append([]string{}, chain[i:]...), key),
				Reason:  "extends cycle",
			}
		}
	}

	svc, err := l.serviceIn(file, name, project)
	if err != nil {
		return nil, err
	}

	ext, hasExt := svc.Map().Get("extends")
	if !hasExt {
		l.cacheExtended(key, svc)
		return clone(svc), nil
	}

	baseName, baseFile, err := readExtends(file, name, ext)
	if err != nil {
		return nil, err
	}

	base, err := l.extendService(baseFile, baseName, project, append(chain, key))
	if err != nil {
		return nil, err
	}

	// The base's contribution, minus the keys extends does not share and minus
	// its own `extends:`, which has already been applied.
	contribution := clone(base)
	contribution.mapping.remove("extends")
	for _, k := range extendsExcluded {
		contribution.mapping.remove(k)
	}

	own := clone(svc)
	own.mapping.remove("extends")

	merged, _ := l.mergeInto(Path{"services", name}, contribution, own)
	l.cacheExtended(key, merged)
	return clone(merged), nil
}

func (l *loader) cacheExtended(key string, v *Value) {
	if l.extended == nil {
		l.extended = map[string]*Value{}
	}
	l.extended[key] = clone(v)
}

// serviceIn finds a service either in the merged project or in a named file,
// loading that file — and registering it in the ordered source-file list, so
// Origin.Step stays meaningful for everything it contributes.
func (l *loader) serviceIn(file, name string, project *Value) (*Value, error) {
	var services *Value
	if file == "" {
		s, ok := project.Map().Get("services")
		if !ok || s.Kind() != KindMapping {
			return nil, &ExtendsError{Service: name, Reason: "the project declares no services"}
		}
		services = s
	} else {
		doc, err := l.loadDoc(file, true)
		if err != nil {
			return nil, err
		}
		s, ok := doc.Map().Get("services")
		if !ok || s.Kind() != KindMapping {
			return nil, &ExtendsError{Service: name, File: file, Reason: "the file declares no services"}
		}
		services = s
	}
	svc, ok := services.Map().Get(name)
	if !ok {
		return nil, &ExtendsError{Service: name, File: file, Reason: "no such service", Known: services.Map().Keys()}
	}
	if svc.Kind() != KindMapping {
		return nil, &ExtendsError{Service: name, File: file, Reason: "the service is not a mapping"}
	}
	return svc, nil
}

// readExtends reads an `extends:` declaration in either syntax:
//
//	extends: base                       # same file, short form
//	extends: {service: base}            # same file
//	extends: {service: base, file: ...} # another file
//
// The `file:` path is resolved relative to the FILE THAT DECLARED THE EXTENDS,
// never the working directory, which is why the declaring position is read off
// the value's own Origin rather than passed in: after a merge, the service that
// carries an `extends:` may well not be the file the chain started from.
func readExtends(inFile, service string, ext *Value) (baseName, baseFile string, err error) {
	declaringDir := func(v *Value) string {
		if o := v.Origin(); o.File != "" {
			return filepath.Dir(o.File)
		}
		if inFile != "" {
			return filepath.Dir(inFile)
		}
		return "."
	}

	switch ext.Kind() {
	case KindScalar:
		s := strings.TrimSpace(ext.Scalar())
		if s == "" {
			return "", "", &ExtendsError{Service: service, File: inFile, Reason: "extends names no service"}
		}
		// Short form: same file as the declaration. See the mapping case below
		// for why this is inFile rather than "".
		return s, inFile, nil

	case KindMapping:
		sv, ok := ext.Map().Get("service")
		if !ok || sv.Kind() != KindScalar || strings.TrimSpace(sv.Scalar()) == "" {
			return "", "", &ExtendsError{Service: service, File: inFile, Reason: "extends has no service:"}
		}
		baseName = strings.TrimSpace(sv.Scalar())
		fv, ok := ext.Map().Get("file")
		if !ok || fv.Kind() != KindScalar || strings.TrimSpace(fv.Scalar()) == "" {
			// No `file:` means "in the same file this was written in" — which
			// is inFile, NOT the merged project.
			//
			// Returning "" here (the merged project) was wrong for the second
			// hop of a chain: `web` extends `base` in remote.yaml, and
			// `base` itself extends `core`. `core` lives in remote.yaml, and
			// looking it up in the merged project either refuses a valid
			// project or — worse — finds a same-named LOCAL service and
			// inherits from it, with provenance confirming the wrong answer.
			//
			// inFile is "" for the top-level call, which is the merged project,
			// and that stays correct: an `extends:` with no file, written in a
			// file of the chain, resolves against the merged chain exactly as
			// Compose does.
			return baseName, inFile, nil
		}
		f := strings.TrimSpace(fv.Scalar())
		if !filepath.IsAbs(f) {
			f = filepath.Join(declaringDir(fv), f)
		}
		return baseName, f, nil
	}
	return "", "", &ExtendsError{Service: service, File: inFile, Reason: "extends is neither a name nor a mapping"}
}

// ExtendsError names the offending service. A missing target additionally
// carries the names that ARE declared, because "no service called db" is much
// less useful than the same sentence with the four names that exist beside it.
type ExtendsError struct {
	Service string
	File    string
	Reason  string
	Cycle   []string
	Known   []string
}

func (e *ExtendsError) Error() string {
	where := "the project"
	if e.File != "" {
		where = e.File
	}
	if len(e.Cycle) > 0 {
		return fmt.Sprintf("extends cycle: %s", strings.Join(e.Cycle, " -> "))
	}
	msg := fmt.Sprintf("extends: %s in %s: %s", e.Service, where, e.Reason)
	if len(e.Known) > 0 {
		msg += fmt.Sprintf(" (declared there: %s)", strings.Join(e.Known, ", "))
	}
	return msg
}

func (e *ExtendsError) Unwrap() error {
	if len(e.Cycle) > 0 {
		return ErrExtendsCycle
	}
	return ErrExtendsTarget
}
