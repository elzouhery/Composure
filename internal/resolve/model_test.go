package resolve

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// "Provenance by construction" is only true if something checks. Review does
// not scale to it: a future story adding `Image string` to a struct here would
// pass a code review and quietly break the one decision on the roadmap that
// cannot be retrofitted.
//
// So walk the exported type graph and fail on any field that could hold a
// configuration value without an origin attached — a bare scalar, a
// map[string]any, or a naked interface. Origin, Override and SourceFile are
// the metadata carriers and are allowed their own scalars; Value is the leaf
// wrapper itself, and its unexported fields are unreachable here anyway.
func TestModelHasNoProvenanceFreeLeaves(t *testing.T) {
	// The previous version of this check inspected ZERO fields and passed
	// regardless: Value sat in the carrier allowlist so the walk returned
	// immediately, and Project has only unexported fields, every one of which
	// the PkgPath skip discarded. It reported success while checking nothing.
	//
	// So the walk now starts from the package's exported API — the types a
	// caller can actually reach, including through method return values — and
	// fails if it ends up inspecting nothing, which is the failure mode that
	// hid the first version's vacuity.
	carriers := map[string]bool{
		"resolve.Origin":     true,
		"resolve.Override":   true,
		"resolve.SourceFile": true,
		// A Finding is diagnostic metadata ABOUT a value, not a value: it
		// carries the Origin of the thing it is about, in the same way an
		// Override does. Its strings are a rule id, a variable name and a
		// sentence of prose — none of them is a configuration value that could
		// end up rendered into a form or spliced into a file.
		//
		// This entry is the only kind of addition this allowlist may take, and
		// the bar is exactly that: a type that describes the model rather than
		// holding part of it. Anything a user could edit belongs in a *Value.
		"resolve.Finding": true,
	}

	inspected := 0
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, trail string)

	walk = func(rt reflect.Type, trail string) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] || carriers[rt.String()] {
			return
		}
		seen[rt] = true

		pathType := reflect.TypeOf(Path{})

		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			ft := f.Type
			// A Path is an ADDRESS of a value, never a value — the same bar as
			// OrderedMap.keys below. It is exempted by type rather than by field
			// name so that every future field holding one is covered, and it is
			// exempted HERE rather than in the carrier allowlist because Path is
			// a []string and the allowlist only sees structs.
			//
			// This exemption exists because repairing the slice blindness below
			// immediately found Explanation.Path — which is the check working,
			// not a defect.
			if ft == pathType {
				inspected++
				continue
			}
			// SLICES AND ARRAYS ARE UNWRAPPED, not skipped.
			//
			// The version of this walk that shipped unwrapped only pointers, so
			// it was blind to every slice: adding `Ports []int` or `Images
			// []string` to Project passed it, and []string is the most natural
			// way anyone would add configuration to this model. Only a BARE
			// scalar field failed, which is the one shape nobody writes.
			//
			// Project.envFileSourced []string was already sitting in the hole.
			for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
				ft = ft.Elem()
			}
			// Unexported fields of the leaf carrier itself are the mechanism —
			// Value.scalar is how a scalar is held at all. Everything else is
			// checked whether exported or not, because an unexported bare
			// scalar on a future Service type is the same defect.
			if rt.String() == "resolve.Value" {
				continue
			}
			inspected++
			switch ft.Kind() {
			case reflect.String, reflect.Bool,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
				reflect.Float32, reflect.Float64:
				// Two allowlisted name-lists. Both NAME things rather than
				// configure them, which is the same bar the carrier allowlist
				// above applies: nothing a user could edit, nothing that could
				// be rendered into a form or spliced into a file.
				if rt.String() == "resolve.OrderedMap" && f.Name == "keys" {
					continue // the key list is metadata, not a configuration value
				}
				if rt.String() == "resolve.Project" && f.Name == "envFileSourced" {
					continue // variable NAMES, not their values; see Project.EnvFileSourced
				}
				t.Errorf("%s.%s is a bare %s — a configuration leaf must be a *Value carrying an Origin (%s)",
					rt.String(), f.Name, ft.Kind(), trail)
			case reflect.Interface:
				t.Errorf("%s.%s is an interface — `any` at a leaf defeats provenance (%s)",
					rt.String(), f.Name, trail)
			case reflect.Map:
				if ft.Elem() == reflect.TypeOf((*Value)(nil)) || ft.Elem() == reflect.TypeOf(Origin{}) {
					continue // OrderedMap's internal indexes, keyed by name
				}
				t.Errorf("%s.%s is a map of %s — use OrderedMap, which keeps key order and key origins (%s)",
					rt.String(), f.Name, ft.Elem(), trail)
			default:
				walk(f.Type, trail+"."+f.Name)
			}
		}
	}

	// Start from every exported type a caller can hold, and from the return
	// types of the exported methods that hand them out.
	roots := []reflect.Type{
		reflect.TypeOf(Project{}),
		reflect.TypeOf(Value{}),
		reflect.TypeOf(OrderedMap{}),
	}
	for _, rt := range []reflect.Type{
		reflect.TypeOf(&Project{}), reflect.TypeOf(&Value{}), reflect.TypeOf(&OrderedMap{}),
	} {
		for i := 0; i < rt.NumMethod(); i++ {
			mt := rt.Method(i).Type
			for j := 0; j < mt.NumOut(); j++ {
				roots = append(roots, mt.Out(j))
			}
		}
	}
	for _, rt := range roots {
		walk(rt, rt.String())
	}

	if inspected == 0 {
		t.Fatal("inspected no fields — the check is vacuous, which is exactly how the first version passed")
	}
	t.Logf("inspected %d fields across %d types", inspected, len(seen))
}

// The model must expose no way to mutate a resolved value. An edit produces new
// bytes through the splice engine and the caller re-resolves; a setter here is
// how a second write path — and re-serialisation with it — gets back in.
func TestModelExposesNoMutators(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeOf(&Project{}),
		reflect.TypeOf(&Value{}),
		reflect.TypeOf(&OrderedMap{}),
	} {
		for i := 0; i < rt.NumMethod(); i++ {
			m := rt.Method(i)
			for _, prefix := range []string{"Set", "Add", "Put", "Insert", "Delete", "Remove", "Update", "Append"} {
				if strings.HasPrefix(m.Name, prefix) {
					t.Errorf("%s.%s looks like a mutator; the resolved model is immutable", rt, m.Name)
				}
			}
		}
	}
}

// Accessors must hand out copies. A caller that can reach into the backing
// array can mutate a resolved model through the back door.
func TestAccessorsDoNotAliasInternals(t *testing.T) {
	p, err := Bytes("compose.yml", []byte("services:\n  web:\n    image: nginx\n    ports:\n      - \"80:80\"\n"))
	if err != nil {
		t.Fatal(err)
	}

	ports, ok := p.At(Path{"services", "web", "ports"})
	if !ok {
		t.Fatal("ports did not resolve")
	}
	got := ports.Seq()
	got[0] = nil
	if ports.Seq()[0] == nil {
		t.Error("Seq() aliases the model's slice")
	}

	web, _ := p.At(Path{"services", "web"})
	keys := web.Map().Keys()
	keys[0] = "clobbered"
	if web.Map().Keys()[0] == "clobbered" {
		t.Error("Keys() aliases the model's slice")
	}

	files := p.Files()
	if len(files) > 0 {
		files[0].Path = "clobbered"
		if p.Files()[0].Path == "clobbered" {
			t.Error("Files() aliases the model's slice")
		}
	}
}

// Overrides() and Findings() were NOT covered by the test above, and the
// "present and empty" assertions elsewhere could not cover them either: those
// assert `!= nil` on an accessor that `make()`s a slice, and make never returns
// nil, so they pass whatever the accessor does. Rewriting either accessor to
// return the internal slice directly passed the entire suite — and a caller
// holding that slice can mutate a supposedly immutable model through it (AD-5).
//
// So both are exercised on a project where the lists are NON-EMPTY, because a
// zero-length slice cannot show aliasing however it is obtained.
func TestOverridesAndFindingsDoNotAliasInternals(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := write("compose.yaml", "services:\n  web:\n    image: nginx:1\n    environment:\n      A: ${NOPE}\n")
	over := write("compose.override.yaml", "services:\n  web:\n    image: nginx:2\n")

	p, err := Load(Options{Files: []string{base, over}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatal(err)
	}

	img, ok := p.At(Path{"services", "web", "image"})
	if !ok {
		t.Fatal("image did not resolve")
	}
	ov := img.Overrides()
	if len(ov) == 0 {
		t.Fatal("no override history to test with; the fixture must produce one")
	}
	ov[0].Value = "clobbered"
	if got := img.Overrides()[0].Value; got == "clobbered" {
		t.Error("Overrides() aliases the model's slice; a caller can rewrite the override history")
	}

	fs := p.Findings()
	if len(fs) == 0 {
		t.Fatal("no findings to test with; ${NOPE} must produce one")
	}
	fs[0].Message = "clobbered"
	if got := p.Findings()[0].Message; got == "clobbered" {
		t.Error("Findings() aliases the model's slice; a caller can rewrite what resolution reported")
	}
}

// "Present and empty, not absent" is a statement about the WIRE, and that is
// where it has to be asserted.
//
// The assertions this replaces tested `p.Findings() != nil` — but Findings()
// `make()`s its slice, and make never returns nil, so no implementation could
// have failed them. What can actually break is the JSON: a nil internal slice
// marshals to `null`, and a consumer that switched on `findings === null`
// against `findings.length === 0` would be told "nothing was checked" where the
// answer is "nothing was wrong".
func TestEmptyListsSerialiseAsListsNotNull(t *testing.T) {
	p, err := BytesWith("compose.yaml", []byte("services:\n  web:\n    image: nginx\n"), hermetic(nil))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"findings"} {
		got, present := wire[key]
		if !present {
			t.Errorf("%q is absent from the wire; absence and emptiness must not look alike", key)
			continue
		}
		if string(got) != "[]" {
			t.Errorf("%q serialised as %s, want []", key, got)
		}
	}
	if !bytes.Contains(raw, []byte(`"overrides":[]`)) {
		t.Error("an unoverridden value serialised its history as null, not []")
	}
}

func TestPathRoundTripsAndComparesBySegments(t *testing.T) {
	cases := []Path{
		{"services", "web", "image"},
		{"services", "web", "ports", "0"},
		{"services", "web", "environment", "some.key"},
		{"x-shared"},
	}
	for _, want := range cases {
		got := ParsePath(want.String())
		if !got.Equal(want) {
			t.Errorf("ParsePath(%q) = %v, want %v", want.String(), got, want)
		}
	}

	if (Path{"a", "b"}).Equal(Path{"a"}) {
		t.Error("paths of different lengths compared equal")
	}

	// Child must not alias its parent's backing array — two children of the
	// same parent would otherwise clobber each other.
	parent := Path{"services", "web"}
	a, b := parent.Child("image"), parent.Child("ports")
	if a[2] != "image" || b[2] != "ports" {
		t.Errorf("Child aliased the parent: a=%v b=%v", a, b)
	}
}

func TestPathIndexRendersAsBrackets(t *testing.T) {
	p := Path{"services", "web", "ports"}.Index(0)
	if got := p.String(); got != "services.web.ports[0]" {
		t.Errorf("String() = %q, want services.web.ports[0]", got)
	}
}
