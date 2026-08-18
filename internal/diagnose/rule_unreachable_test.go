package diagnose

import (
	"strings"
	"testing"
)

const unreachableRuleID = "unreachable-service"

func TestUnreachableService(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    networks: [front]
    ports:
      - "8080:80"
  hermit:
    image: busybox
    networks: [lonely]
networks:
  front:
  lonely:
`)
	got := wantCount(t, rep, unreachableRuleID, 1)[0]
	if !strings.Contains(got.Message, `"hermit"`) {
		t.Errorf("named the wrong service: %s", got.Message)
	}
	if got.Anchors[0].Origin.Line != 8 {
		t.Errorf("anchored at line %d, want hermit's declaration on line 8", got.Anchors[0].Origin.Line)
	}
	if got.Fix != nil {
		t.Error("a fix was offered; publish, attach and delete are all valid answers")
	}
}

// The negatives, and they are the whole rule.

// `internal:` means no route OUT of the network, not that the network carries
// no traffic. Reporting these would flag every correctly hardened database.
func TestInternalNetworkIsNotUnreachable(t *testing.T) {
	rep := run(t, `
services:
  api:
    image: api
    networks: [back]
  db:
    image: postgres
    networks: [back]
networks:
  back:
    internal: true
`)
	wantNone(t, rep, unreachableRuleID)
}

// A shared namespace is a real path, and it is the edge story 2.3 built.
//
// The fixture has to be built so the EdgeNetworkMode branch is the only thing
// exempting either service. The version this replaced gave `web` a published
// port and `sidecar` a `network_mode:` key — the first exempted by publishes[],
// the second by declaresNetworkMode() — so the whole networkMode branch of the
// rule could be deleted with the suite green. Here `web` publishes nothing,
// joins no network, and is alone on the implicit default: without the
// network_mode EDGE naming it as a target, it is unreachable.
func TestNetworkModeServiceIsNotUnreachable(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
  sidecar:
    image: sidecar
    network_mode: service:web
`)
	wantNone(t, rep, unreachableRuleID)
}

// The proof that the fixture above is load-bearing: the same `web`, with no
// service pointing a network_mode at it, IS reported. If this fails, the test
// above stopped testing the network_mode branch.
func TestTheNetworkModeFixtureWouldOtherwiseBeUnreachable(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
  sidecar:
    image: sidecar
    network_mode: none
`)
	got := wantCount(t, rep, unreachableRuleID, 1)[0]
	if !strings.Contains(got.Message, `"web"`) {
		t.Errorf("named the wrong service: %s", got.Message)
	}
}

// `network_mode: host` is reachable on every port the process binds; `none` is
// deliberate. Neither is something to be told about.
func TestNetworkModeHostAndNoneAreLeftAlone(t *testing.T) {
	rep := run(t, `
services:
  metrics:
    image: node-exporter
    network_mode: host
  isolated:
    image: batch
    network_mode: none
`)
	wantNone(t, rep, unreachableRuleID)
}

// The commonest shape in the corpus by a wide margin: no networks declared at
// all. Compose puts every one of these on the implicit default network, and a
// rule that missed that would report every service of every simple stack.
func TestImplicitDefaultNetworkCounts(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
  api:
    image: api
  db:
    image: postgres
`)
	wantNone(t, rep, unreachableRuleID)
}

func TestPublishedPortIsReachable(t *testing.T) {
	rep := run(t, `
services:
  a:
    image: x
    networks: [one]
    ports:
      - "8080:80"
  b:
    image: x
    networks: [two]
    ports:
      - "8081:80"
networks:
  one:
  two:
`)
	wantNone(t, rep, unreachableRuleID)
}

func TestLegacyLinkIsAPath(t *testing.T) {
	rep := run(t, `
services:
  web:
    image: nginx
    networks: [front]
    ports:
      - "8080:80"
    links:
      - worker
  worker:
    image: worker
    networks: [back]
networks:
  front:
  back:
`)
	wantNone(t, rep, unreachableRuleID)
}

// A profile-gated service on no network is not in the active graph at all, so
// it is only reported when its profile is on.
func TestUnreachableRespectsProfiles(t *testing.T) {
	const src = `
services:
  web:
    image: nginx
    networks: [front]
    ports:
      - "8080:80"
  importer:
    image: importer
    profiles: [migration]
    networks: [alone]
networks:
  front:
  alone:
`
	wantNone(t, run(t, src), unreachableRuleID)
	got := wantCount(t, run(t, src, withProfiles("migration")), unreachableRuleID, 1)[0]
	if !strings.Contains(got.Message, `"importer"`) {
		t.Errorf("named the wrong service: %s", got.Message)
	}
}

// A single service alone in a project, with no port, genuinely is unreachable.
// It is the one case where the implicit default network does not save it.
func TestLoneServiceWithNoPortIsUnreachable(t *testing.T) {
	rep := run(t, `
services:
  batch:
    image: batch
    command: ["run-once"]
`)
	wantCount(t, rep, unreachableRuleID, 1)
}
