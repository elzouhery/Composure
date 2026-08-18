package resolve

// Story 1.9 — profile membership, annotated on every service.
//
// AD-16 in one line: `resolve` takes NO profile argument. Everything here is a
// read over the model that already exists, so the resolver stays a pure
// function of the file set and toggling a profile can never re-read a file.
// Filtering is topology's job and is already built.
//
// Nothing here introduces a type that could hold a configuration value without
// an origin: profile names are handed out as the `*Value`s they already are in
// the model, and the derived lists are plain `[]string` sets that name things
// rather than configure them.

import (
	"sort"
	"strings"
)

// ProfilesOf returns the profile names a service declares, as the values they
// are in the model — each carrying the Origin of the line that declared it.
//
// The bool reports whether the service exists, not whether it declares
// profiles: a service with no `profiles:` key returns an empty list and true,
// which is the "always active" case and is a real answer.
func (p *Project) ProfilesOf(service string) ([]*Value, bool) {
	svc, ok := p.At(Path{"services", service})
	if !ok {
		return nil, false
	}
	v, ok := svc.At(Path{"profiles"})
	if !ok {
		return []*Value{}, true
	}
	return profileValues(v), true
}

// profileValues reads a `profiles:` declaration into the list of values it
// names, in either spelling.
//
// It is one function rather than a switch in each caller because the two
// callers must not disagree about what a `profiles:` declaration contains — the
// scalar spelling in particular, which is the branch that decides whether a
// gated service is reported as always-active.
func profileValues(v *Value) []*Value {
	switch v.Kind() {
	case KindSequence:
		return v.Seq()
	case KindScalar:
		// `profiles: dev` is not the documented spelling, but files write it
		// and Compose accepts it. Reading it as a one-element list beats
		// reporting the service as always active, which is the opposite of
		// what the author asked for.
		return []*Value{v}
	}
	return []*Value{}
}

// AlwaysActive reports whether a service starts with no profile enabled — the
// Compose default for a service that declares no `profiles:`.
//
// A service whose only profile name resolved to the empty string — `profiles:
// [${UNSET}]` — is NOT always-active, because the author gated it, and Compose
// will not start it without a `--profile`. It is also not in any profile
// DeclaredProfiles can offer, because there is no name to offer. That
// combination makes the service unstartable, so it is reported as a finding at
// load (FindingEmptyProfileName) rather than left for a user to discover by the
// service never coming up.
func (p *Project) AlwaysActive(service string) bool {
	names, ok := p.ProfilesOf(service)
	return ok && len(names) == 0
}

// noteEmptyProfileNames records a finding for every `profiles:` entry that
// resolved to the empty string.
//
// This is the one place the two profile views can disagree: DeclaredProfiles
// skips an empty name because there is nothing to offer, while AlwaysActive
// counts it because the author did gate the service. Both are right on their own
// terms, and the service that falls between them is in a profile nobody can name
// — it will never start, and before this nothing said so.
func (l *loader) noteEmptyProfileNames(root *Value) {
	if root == nil || root.Kind() != KindMapping {
		return
	}
	services, ok := root.Map().Get("services")
	if !ok || services.Kind() != KindMapping {
		return
	}
	for _, name := range services.Map().Keys() {
		svc, _ := services.Map().Get(name)
		if svc == nil || svc.Kind() != KindMapping {
			continue
		}
		pv, ok := svc.Map().Get("profiles")
		if !ok || pv == nil {
			continue
		}
		for i, e := range profileValues(pv) {
			if e == nil || strings.TrimSpace(e.Scalar()) != "" {
				continue
			}
			l.note(Finding{
				Kind:   FindingEmptyProfileName,
				Path:   Path{"services", name, "profiles"}.Index(i),
				Origin: e.Origin(),
				Message: "service " + name + " declares a profile whose name is empty" +
					emptyProfileCause(e) + ": the service is gated, so it is not started by " +
					"default, and no --profile can name it either. It cannot be started at all",
			})
		}
	}
}

// emptyProfileCause names the usual culprit when it can see it: a variable
// reference that resolved to nothing. It says nothing when it cannot, rather
// than guessing.
func emptyProfileCause(v *Value) string {
	if raw := strings.TrimSpace(v.Raw()); v.Interpolated() && raw != "" {
		return " (" + raw + " resolved to nothing)"
	}
	return ""
}

// DeclaredProfiles is every profile name any service in the project mentions,
// sorted and deduplicated.
//
// A name that resolved to the empty string is skipped: there is nothing a caller
// could offer or a user could type. The service that declared it is still gated
// — see AlwaysActive — and that gap is reported as a finding at load.
//
// It exists so a caller can offer the set without walking every service, which
// is the acceptance criterion, and so that `composure topology --profile` can be
// told what the valid answers are.
func (p *Project) DeclaredProfiles() []string {
	seen := map[string]bool{}
	var out []string
	services := p.Services()
	for _, name := range services.Keys() {
		vals, _ := p.ProfilesOf(name)
		for _, v := range vals {
			s := v.Scalar()
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// profilesJSON is the wire shape. It is unexported: the model's exported type
// graph must not grow a struct holding bare configuration strings (AD-3), and
// a marshalling shape is not part of that graph.
type profilesJSON struct {
	// Declared is every profile name the project mentions.
	Declared []string `json:"declared"`
	// Services annotates membership per service, always present for every
	// service — an absent entry and "always active" must not look alike.
	Services []serviceProfileJSON `json:"services"`
}

type serviceProfileJSON struct {
	Name string `json:"name"`
	Path Path   `json:"path"`
	// Members carries each profile name as the positioned value it is, so a
	// consumer can jump to the line that put a service in a profile.
	Members []*Value `json:"members"`
	// AlwaysActive is true when the service declares no profiles, which is
	// Compose's default and what `docker compose up` with no --profile runs.
	AlwaysActive bool `json:"always_active"`
}

func (p *Project) profilesWire() profilesJSON {
	out := profilesJSON{Declared: p.DeclaredProfiles(), Services: []serviceProfileJSON{}}
	services := p.Services()
	for _, name := range services.Keys() {
		members, ok := p.ProfilesOf(name)
		if !ok {
			continue
		}
		if members == nil {
			members = []*Value{}
		}
		out.Services = append(out.Services, serviceProfileJSON{
			Name:         name,
			Path:         Path{"services", name},
			Members:      members,
			AlwaysActive: len(members) == 0,
		})
	}
	return out
}
