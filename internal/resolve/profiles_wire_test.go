package resolve

// The profile block of `composure resolve -json`, held at the layer it is produced.
//
// cmd/composure's golden pins the whole document byte for byte and is the primary
// guard; this is the same three properties asserted where they are decided, so
// that `go test ./internal/resolve` on its own is not blind to them. Before
// either existed, profilesWire could be replaced with `return profilesJSON{}` —
// emptying `declared` and deleting the entire per-service array from every JSON
// output — with a green suite.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// profileWireFixture is deliberately not thin: a service in two profiles, a
// service in one, and a service in none. A fixture with only the first two
// cannot tell `len(members) == 0` from `len(members) >= 0`, and a fixture with
// only the last cannot tell the profile block from an empty one.
const profileWireFixture = `services:
  web:
    image: nginx
    profiles:
      - frontend
      - edge
  metrics:
    image: prom/prometheus
    profiles: [edge]
  db:
    image: postgres
`

func profileWireProject(t *testing.T) *Project {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(profileWireFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(Options{Files: []string{path}, IgnoreHostEnv: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return p
}

// profileWire is the block as a consumer receives it — read back out of the
// marshalled document rather than off the unexported struct, because the wire
// is what is being held and a field could be dropped by a tag alone.
type profileWireRead struct {
	Declared []string `json:"declared"`
	Services []struct {
		Name    string   `json:"name"`
		Path    []string `json:"path"`
		Members []struct {
			Scalar string `json:"scalar"`
			Origin Origin `json:"origin"`
		} `json:"members"`
		AlwaysActive bool `json:"always_active"`
	} `json:"services"`
}

func readProfileWire(t *testing.T, p *Project) profileWireRead {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc struct {
		Profiles profileWireRead `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc.Profiles
}

func TestProfileWireDeclaresEveryNameOnce(t *testing.T) {
	got := readProfileWire(t, profileWireProject(t)).Declared
	want := []string{"edge", "frontend"} // sorted, deduplicated
	if len(got) != len(want) {
		t.Fatalf("declared = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("declared = %v, want %v", got, want)
		}
	}
}

// TestProfileWireCoversEveryServiceWithItsMembership is the block's own promise:
// an entry for every service, so an absent entry and "always active" do not look
// alike, each carrying the profile names as POSITIONED values.
func TestProfileWireCoversEveryServiceWithItsMembership(t *testing.T) {
	p := profileWireProject(t)
	wire := readProfileWire(t, p)

	declared := p.Services().Keys()
	if len(declared) != 3 {
		t.Fatalf("the fixture declares %d services, want 3", len(declared))
	}
	if len(wire.Services) != len(declared) {
		t.Fatalf("the wire covers %d services, the document declares %d", len(wire.Services), len(declared))
	}

	want := map[string]struct {
		members      []string
		alwaysActive bool
	}{
		"web":     {[]string{"frontend", "edge"}, false},
		"metrics": {[]string{"edge"}, false},
		"db":      {nil, true},
	}
	seen := 0
	for _, s := range wire.Services {
		exp, ok := want[s.Name]
		if !ok {
			t.Errorf("unexpected service %q in the profile block", s.Name)
			continue
		}
		seen++
		if s.AlwaysActive != exp.alwaysActive {
			t.Errorf("%s: always_active = %v, want %v", s.Name, s.AlwaysActive, exp.alwaysActive)
		}
		if len(s.Members) != len(exp.members) {
			t.Errorf("%s: %d members, want %d", s.Name, len(s.Members), len(exp.members))
			continue
		}
		for i, m := range s.Members {
			if m.Scalar != exp.members[i] {
				t.Errorf("%s: member %d = %q, want %q (source order)", s.Name, i, m.Scalar, exp.members[i])
			}
			// R1.8: a membership a consumer cannot jump to is not an answer.
			if m.Origin.IsZero() {
				t.Errorf("%s: member %q carries no origin", s.Name, m.Scalar)
			}
		}
		if len(s.Path) != 2 || s.Path[0] != "services" || s.Path[1] != s.Name {
			t.Errorf("%s: path = %v, want [services %s]", s.Name, s.Path, s.Name)
		}
	}
	if seen != len(want) {
		t.Errorf("the wire named %d of the %d services", seen, len(want))
	}
}
