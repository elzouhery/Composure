package diagnose

import (
	"strings"
	"testing"
)

const portRule = "host-port-collision"

func TestPortCollisionNamesBothSides(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    ports:
      - "8080:80"
  api:
    image: api
    ports:
      - "8080:3000"
`)
	got := wantCount(t, rep, portRule, 1)[0]
	if got.Severity != SeverityError {
		t.Errorf("severity is %s, want error — the stack cannot start", got.Severity)
	}
	if !strings.Contains(got.Message, `"web"`) || !strings.Contains(got.Message, `"api"`) {
		t.Errorf("message does not name both services: %s", got.Message)
	}
	// AD-7: both declarations, not one.
	if len(got.Anchors) != 2 {
		t.Fatalf("%d anchors, want the position of both declarations", len(got.Anchors))
	}
	lines := []int{got.Anchors[0].Origin.Line, got.Anchors[1].Origin.Line}
	if lines[0] == lines[1] {
		t.Errorf("both anchors point at line %d", lines[0])
	}
}

// The negative that matters most: this is what every multi-instance stack looks
// like, and reporting it would make the rule useless.
func TestPortCollisionSameContainerPortIsFine(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "8080:80"
  b:
    image: x
    ports:
      - "8081:80"
  c:
    image: x
    ports:
      - "8082:80"
`)
	wantNone(t, rep, portRule)
}

// Different explicit bind addresses do not contend.
func TestPortCollisionDistinctHostIPsDoNotCollide(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "127.0.0.1:8080:80"
  b:
    image: x
    ports:
      - "10.0.0.1:8080:80"
`)
	wantNone(t, rep, portRule)
}

// ...but "all interfaces" spelled two ways is the same interface.
func TestPortCollisionWildcardSpellingsDoCollide(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "0.0.0.0:8080:80"
  b:
    image: x
    ports:
      - "8080:81"
`)
	wantCount(t, rep, portRule, 1)
}

func TestPortCollisionDifferentProtocolsDoNotCollide(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "5353:53/udp"
  b:
    image: x
    ports:
      - "5353:53/tcp"
`)
	wantNone(t, rep, portRule)
}

// An exposed-but-unpublished port binds nothing on the host.
func TestPortCollisionUnpublishedPortsDoNotCollide(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "80"
  b:
    image: x
    ports:
      - "80"
`)
	// The short form "80" is a container port with no host side, so neither
	// declaration binds anything on the host.
	wantNone(t, rep, portRule)
}

// Profile filtering is topology's (AD-16), and the rule inherits it: a port
// published only under an inactive profile is not in the graph at all.
func TestPortCollisionRespectsProfiles(t *testing.T) {
	const src = `
services:
  web:
    image: nginx
    ports:
      - "8080:80"
  debug:
    image: debug
    profiles: [debug]
    ports:
      - "8080:9229"
`
	wantNone(t, run(t, src), portRule)
	wantCount(t, run(t, src, withProfiles("debug")), portRule, 1)
}

// A published range collides entry by entry.
func TestPortCollisionRangeOverlaps(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "3000-3002:3000"
  b:
    image: x
    ports:
      - "3002:80"
`)
	got := wantCount(t, rep, portRule, 1)[0]
	if !strings.Contains(got.Message, "3002") {
		t.Errorf("message does not name the overlapping port: %s", got.Message)
	}
}

// An uninterpolated variable on the host side is not a number and must not be
// guessed at — two services both publishing "${PORT}:80" are not evidence of a
// collision, they are evidence that interpolation has not run.
func TestPortCollisionIgnoresUninterpolatedPorts(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - "${PORT}:80"
  b:
    image: x
    ports:
      - "${PORT}:81"
`)
	wantNone(t, rep, portRule)
}

func TestPortCollisionFixTargetsTheLosingEntry(t *testing.T) {
	src := "services:\n  web:\n    image: nginx\n    ports:\n      - \"8080:80\"\n  api:\n    image: api\n    ports:\n      - \"8080:3000\"\n"
	rep := run(t, src)
	got := wantCount(t, rep, portRule, 1)[0]
	if got.Fix == nil {
		t.Fatalf("no fix described: %s", got.NoFix)
	}
	if got.Fix.Path.String() != "services.api.ports[0]" {
		t.Errorf("fix targets %s, want the second declaration", got.Fix.Path)
	}
	if covered := src[got.Fix.Range.Start:got.Fix.Range.End]; covered != `"8080:3000"` {
		t.Errorf("fix range covers %q", covered)
	}
	// The replacement port is the author's call: the tool cannot know what else
	// is listening on the host.
	if got.Fix.Value != "" {
		t.Errorf("the fix proposed a specific port %q", got.Fix.Value)
	}
}

// The long mapping form gets no fix, with a reason. Describing a scalar
// replacement over a mapping would report a range phase 2 would not touch.
func TestPortCollisionLongFormHasNoFix(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    ports:
      - target: 80
        published: "8080"
  b:
    image: x
    ports:
      - "8080:81"
`)
	got := wantCount(t, rep, portRule, 1)
	for _, f := range got {
		if f.Fix != nil && f.Fix.Path.String() == "services.a.ports[0]" {
			t.Error("a scalar fix was described for a long-form port entry")
		}
	}
}
