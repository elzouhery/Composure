// layout.ts is the only module in this extension that computes anything, and
// what it computes is pixel coordinates. All of it is pure — no DOM, no
// `vscode`, no process — so it is testable under `node --test` exactly as it
// stands.
//
// Story 4.2 replaced the grid with a layered arrangement and added edges, so
// most of what is below is new: which band a node lands in, how a band is
// ordered, where an edge meets a box, and which key moves the selection where.
// Every one of those is a decision, and a decision that only exists inside a
// DOM callback is a decision no test can reach.

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  DOCKERFILE_WIDTH,

  MAX_SCALE,
  MIN_SCALE,
  NODE_HEIGHT,
  NODE_WIDTH,
  PORT_WIDTH,
  RESOURCE_WIDTH,
  SATELLITE_GAP,
  bandOf,
  boxOf,
  clampScale,
  contentBox,
  describeNode,
  edgeAnchors,
  edgeGeometry,
  edgeLabel,
  edgePaths,
  fitToView,
  kindWord,
  layoutGraph,
  legendEntries,
  markerIndex,
  navigate,
  navigationRows,
  nodeSize,
  orderBands,
  orderSpan,
  placeLabels,
  roundedPath,
  routeEdges,
  routeGeometry,
  clearCorridor,
  ROUTE_CLEARANCE,
  shouldRefit,
  truncateHead,
  truncateTail,
  wrapText,
  wrapBand,
  wrapImageRef,
} from './layout';
import type {
  EdgeKind,
  GraphEdge,
  GraphNode,
  NodeKind,
  Point,
  StackGraph,
} from '../shared/protocol';

const ORIGIN = { file: 'compose.yml', line: 1, column: 1, step: 0 };

function node(id: string, kind: NodeKind = 'service', extra: Partial<GraphNode> = {}): GraphNode {
  return {
    id,
    kind,
    name: id.split('.').pop() ?? id,
    origin: ORIGIN,
    declared: true,
    external: false,
    profiles: [],
    layer: 0,
    ...extra,
  };
}

function service(name: string, layer = 0, extra: Partial<GraphNode> = {}): GraphNode {
  return node(`services.${name}`, 'service', { layer, ...extra });
}

function edge(kind: GraphEdge['kind'], from: string, to: string, extra: Partial<GraphEdge> = {}): GraphEdge {
  return { kind, from, to, origin: ORIGIN, ...extra };
}

function graphOf(nodes: GraphNode[], edges: GraphEdge[] = [], extra: Partial<StackGraph> = {}): StackGraph {
  return {
    profiles: [],
    nodes,
    edges,
    cycles: [],
    dangling: [],
    max_layer: Math.max(0, ...nodes.map((n) => n.layer)),
    ...extra,
  };
}

/**
 * The example from examples/webstack, in the shape the core reports it. Four
 * dependency layers, one network, two named volumes, one published port and one
 * Dockerfile — enough that every band is occupied.
 */
function webstack(): StackGraph {
  const nodes = [
    service('gateway', 3),
    service('web', 2),
    service('api', 1),
    service('db', 0),
    service('cache', 0),
    service('docs', 0),
    node('networks.shipyard', 'network'),
    node('volumes.pgdata', 'volume'),
    node('services.gateway.ports[0]', 'port', {
      name: '8080:80',
      port: { published: '8080', target: '80', protocol: 'tcp', raw: '8080:80' },
    }),
    node('services.docs.build', 'dockerfile', {
      name: './docs/Dockerfile',
      build: { context: './docs', dockerfile: 'Dockerfile', reference: './docs/Dockerfile' },
    }),
  ];
  const edges = [
    edge('depends_on', 'services.gateway', 'services.web', {
      depends_on: { condition: 'service_healthy' },
    }),
    edge('depends_on', 'services.gateway', 'services.docs', {
      depends_on: { condition: 'service_started' },
    }),
    edge('depends_on', 'services.web', 'services.api', {
      depends_on: { condition: 'service_healthy' },
    }),
    edge('depends_on', 'services.api', 'services.db', { depends_on: { condition: 'service_started' } }),
    edge('depends_on', 'services.api', 'services.cache', {
      depends_on: { condition: 'service_started' },
    }),
    edge('network', 'services.gateway', 'networks.shipyard'),
    edge('network', 'services.web', 'networks.shipyard'),
    edge('volume', 'services.db', 'volumes.pgdata', {
      mount: { source: 'pgdata', target: '/var/lib/postgresql/data', mode: 'rw', read_only: false },
    }),
    edge('publish', 'services.gateway', 'services.gateway.ports[0]'),
    edge('build', 'services.docs', 'services.docs.build'),
    edge('bind', 'services.gateway', 'services.gateway', {
      mount: {
        source: './gateway/nginx.conf',
        target: '/etc/nginx/conf.d/default.conf',
        mode: 'ro',
        read_only: true,
        host_path: './gateway/nginx.conf',
      },
    }),
  ];
  return graphOf(nodes, edges);
}

describe('bandOf', () => {
  // Published ports are where traffic enters and services depend downward, so
  // the reading order is ports, then dependents, then their dependencies, then
  // the resources the whole stack rests on.
  it('puts published ports above everything', () => {
    assert.equal(bandOf(node('services.a.ports[0]', 'port'), 3), 0);
  });

  it('inverts the layer so a dependent is drawn above what it depends on', () => {
    const maxLayer = 3;
    const top = bandOf(service('gateway', 3), maxLayer);
    const bottom = bandOf(service('db', 0), maxLayer);
    assert.ok(top < bottom, `layer 3 at band ${top} must be above layer 0 at band ${bottom}`);
  });

  it('settles networks, volumes, configs and secrets at the bottom', () => {
    for (const kind of ['network', 'volume', 'config', 'secret'] as NodeKind[]) {
      assert.equal(bandOf(node(`${kind}s.x`, kind), 2), 4);
    }
  });
});

describe('nodeSize', () => {
  it('gives each kind its own footprint, so a network cannot read as a service', () => {
    assert.equal(nodeSize(service('web')).width, NODE_WIDTH);
    assert.equal(nodeSize(node('networks.a', 'network')).width, RESOURCE_WIDTH);
    assert.equal(nodeSize(node('services.a.ports[0]', 'port')).width, PORT_WIDTH);
    assert.equal(nodeSize(node('services.a.build', 'dockerfile')).width, DOCKERFILE_WIDTH);
  });

  it('grows a service to fit its markers rather than clipping them away', () => {
    const plain = nodeSize(service('web'));
    const withMarkers = nodeSize(service('web'), 3);
    assert.ok(withMarkers.height > plain.height, 'markers did not make room for themselves');
  });

  it('never shrinks below the standard service box', () => {
    assert.equal(nodeSize(service('web'), 0).height, NODE_HEIGHT);
  });

  it('grows for a wrapped image reference too', () => {
    const long = service('web', 0, { image: 'registry.example.com/team/service:2026.08.1' });
    assert.ok(nodeSize(long).height >= NODE_HEIGHT);
  });
});

describe('describeNode', () => {
  it('shows a service its image, which is the thing a reader scans for', () => {
    assert.equal(describeNode(service('web', 0, { image: 'nginx:1.27' })), 'nginx:1.27');
  });

  it('says a service has no image rather than leaving the line blank', () => {
    assert.equal(describeNode(service('docs')), 'no image');
  });

  it('distinguishes external from never declared', () => {
    assert.equal(
      describeNode(node('networks.a', 'network', { declared: true, external: true })),
      'network · external',
    );
    assert.equal(
      describeNode(node('networks.a', 'network', { declared: false, external: true })),
      'network · not declared',
    );
  });

  it('spells out a port rather than badging it', () => {
    const p = node('services.a.ports[0]', 'port', {
      port: { published: '8080', target: '80', protocol: 'tcp', raw: '8080:80' },
    });
    assert.equal(describeNode(p), '8080 → 80/tcp');
  });

  it('says an inline Dockerfile is inline instead of naming a file', () => {
    const df = node('services.a.build', 'dockerfile', {
      build: { context: '.', inline: true },
    });
    assert.equal(describeNode(df), 'inline, in the compose file');
  });

  it('names the Dockerfile kind in the domain’s own capitalisation', () => {
    assert.equal(kindWord('dockerfile'), 'Dockerfile');
    assert.equal(kindWord('network'), 'network');
  });
});

describe('markerIndex', () => {
  // A bind's far side is a path on a host that is not in this file. Drawn as an
  // edge it is a loop from a box back to itself, which reads as a bug.
  it('turns a bind self-edge into a marker carrying the host path', () => {
    const markers = markerIndex(webstack());
    const lines = markers['services.gateway'] ?? [];
    assert.ok(
      lines.some((l) => l.includes('./gateway/nginx.conf')),
      `no host path in ${JSON.stringify(lines)}`,
    );
    assert.ok(lines.some((l) => l.endsWith(' ro')), 'read-only was not carried');
  });

  it('makes a dangling reference visible on the node that made it', () => {
    const g = graphOf([service('a')], [], {
      dangling: [
        {
          kind: 'depends_on',
          from: 'services.a',
          to: 'services.b',
          ref: 'b',
          reason: 'filtered by profile',
          origin: ORIGIN,
        },
      ],
    });
    const lines = markerIndex(g)['services.a'];
    assert.ok(lines[0].includes('unresolved'), lines[0]);
    assert.ok(lines[0].includes('b'), lines[0]);
    assert.ok(lines[0].includes('filtered by profile'), lines[0]);
  });

  it('names every member of a cycle, on every member', () => {
    const g = graphOf([service('a'), service('b')], [], {
      cycles: [['services.a', 'services.b']],
    });
    const markers = markerIndex(g);
    for (const id of ['services.a', 'services.b']) {
      assert.match(markers[id][0], /dependency cycle: a → b/);
    }
  });
});

describe('layoutGraph', () => {
  it('reads dependency order top to bottom', () => {
    const { positions } = layoutGraph(webstack(), {});
    const y = (id: string) => positions[id].y;
    assert.ok(y('services.gateway') < y('services.web'), 'gateway is not above web');
    assert.ok(y('services.web') < y('services.api'), 'web is not above api');
    assert.ok(y('services.api') < y('services.db'), 'api is not above db');
    assert.ok(y('services.gateway.ports[0]') < y('services.gateway'), 'the port is not on top');
    assert.ok(y('networks.shipyard') > y('services.db'), 'the network is not at the bottom');
  });

  it('hangs a Dockerfile node below the service that builds it', () => {
    const { positions, sizes } = layoutGraph(webstack(), {});
    const docs = positions['services.docs'];
    const df = positions['services.docs.build'];
    assert.ok(df.y >= docs.y + sizes['services.docs'].height, 'the Dockerfile is not below');
    assert.equal(df.y, docs.y + sizes['services.docs'].height + SATELLITE_GAP);
    assert.ok(df.x >= docs.x, 'the Dockerfile is not attached under its service');
  });

  it('places every node exactly once and nowhere twice', () => {
    const { positions } = layoutGraph(webstack(), {});
    const g = webstack();
    assert.equal(Object.keys(positions).length, g.nodes.length);
    const seen = Object.values(positions).map((p) => `${p.x},${p.y}`);
    assert.equal(new Set(seen).size, seen.length, `two nodes share a position: ${seen.join(' ')}`);
  });

  it('keeps a dragged node exactly where the reader put it', () => {
    const saved = { 'services.db': { x: 900, y: -40 } };
    const { positions } = layoutGraph(webstack(), saved);
    assert.deepEqual(positions['services.db'], { x: 900, y: -40 });
  });

  it('copies saved positions rather than aliasing the caller’s object', () => {
    const saved: Record<string, Point> = { 'services.db': { x: 10, y: 20 } };
    const { positions } = layoutGraph(webstack(), saved);
    positions['services.db'].x = 999;
    assert.equal(saved['services.db'].x, 10);
  });

  it('ignores a saved position that is not a finite number', () => {
    for (const bad of [{ x: NaN, y: 0 }, { x: 0, y: Infinity }] as Point[]) {
      const { positions } = layoutGraph(webstack(), { 'services.db': bad });
      assert.ok(Number.isFinite(positions['services.db'].x));
      assert.ok(Number.isFinite(positions['services.db'].y));
    }
  });

  it('drags the Dockerfile along with the service it is attached to', () => {
    const saved = { 'services.docs': { x: 900, y: 900 } };
    const { positions, sizes } = layoutGraph(webstack(), saved);
    assert.equal(
      positions['services.docs.build'].y,
      900 + sizes['services.docs'].height + SATELLITE_GAP,
    );
  });

  it('drops a saved position whose node no longer exists', () => {
    const { positions } = layoutGraph(graphOf([service('a')]), {
      'services.gone': { x: 5, y: 5 },
    });
    assert.deepEqual(Object.keys(positions), ['services.a']);
  });

  it('is deterministic: the same graph lays out identically twice', () => {
    // Spatial memory is the whole reason positions are stable. A layout that
    // reorders itself between two identical draws destroys it.
    assert.deepEqual(layoutGraph(webstack(), {}).positions, layoutGraph(webstack(), {}).positions);
  });

  it('lays out a cycle without breaking, because the members share a layer', () => {
    const g = graphOf(
      [service('a', 0), service('b', 0)],
      [edge('depends_on', 'services.a', 'services.b'), edge('depends_on', 'services.b', 'services.a')],
      { cycles: [['services.a', 'services.b']] },
    );
    const { positions, markers } = layoutGraph(g, {});
    assert.equal(positions['services.a'].y, positions['services.b'].y, 'a cycle should share a row');
    assert.notEqual(positions['services.a'].x, positions['services.b'].x);
    assert.ok(markers['services.a'][0].startsWith('dependency cycle'));
  });

  it('survives a graph with no nodes at all', () => {
    const empty = layoutGraph(graphOf([]), {});
    assert.deepEqual(empty.positions, {});
    assert.deepEqual(empty.rows, []);
    assert.deepEqual(empty.order, []);
  });

  it('records which service each Dockerfile belongs to', () => {
    assert.equal(layoutGraph(webstack(), {}).parentOf['services.docs.build'], 'services.docs');
  });
});

describe('orderBands', () => {
  it('pulls a node under the neighbour it is connected to', () => {
    // b connects to y, a connects to x. Left alone the lower band is [b, a] and
    // the two edges cross; the barycentre pass uncrosses them.
    const bands = [
      ['x', 'y'],
      ['b', 'a'],
    ];
    orderBands(bands, [edge('depends_on', 'a', 'x'), edge('depends_on', 'b', 'y')]);
    assert.deepEqual(bands[1], ['a', 'b']);
  });

  it('leaves a node with no neighbours in its incoming relative place', () => {
    const bands = [['x'], ['a', 'lonely', 'b']];
    orderBands(bands, [edge('network', 'a', 'x'), edge('network', 'b', 'x')]);
    assert.equal(bands[1].length, 3);
    assert.ok(bands[1].includes('lonely'));
  });

  it('is stable: ordering an already-ordered graph changes nothing', () => {
    const once = [['x', 'y'], ['a', 'b']];
    const edges = [edge('depends_on', 'a', 'x'), edge('depends_on', 'b', 'y')];
    orderBands(once, edges);
    const first = once.map((b) => [...b]);
    orderBands(once, edges);
    assert.deepEqual(once, first);
  });

  it('does not fold a build edge into the ordering', () => {
    // A Dockerfile is a satellite of its service, never a band member, so its
    // edge must not drag the service anywhere.
    const bands = [['x'], ['a', 'b']];
    orderBands(bands, [edge('build', 'b', 'x')]);
    assert.deepEqual(bands[1], ['a', 'b']);
  });
});

describe('navigationRows and navigate', () => {
  const positions = {
    a: { x: 0, y: 0 },
    b: { x: 200, y: 0 },
    c: { x: 0, y: 200 },
    d: { x: 200, y: 200 },
  };
  const sizes = Object.fromEntries(
    Object.keys(positions).map((id) => [id, { width: 100, height: 40 }]),
  );
  const rows = navigationRows(Object.keys(positions), positions, sizes);

  it('groups by row and orders each row left to right', () => {
    assert.deepEqual(rows, [
      ['a', 'b'],
      ['c', 'd'],
    ]);
  });

  it('keeps a slightly misaligned node in the same row', () => {
    const nudged = { ...positions, b: { x: 200, y: 8 } };
    assert.deepEqual(navigationRows(Object.keys(nudged), nudged, sizes)[0], ['a', 'b']);
  });

  // Left and right walk the whole graph rather than stopping at the end of a
  // row: a listbox with holes in it that only a mouse can fill fails N6.
  it('walks every node with the right arrow alone', () => {
    let at: string | null = null;
    const visited: string[] = [];
    for (let i = 0; i < 4; i++) {
      at = navigate(rows, positions, at, 'ArrowRight');
      visited.push(at!);
    }
    assert.deepEqual(visited, ['a', 'b', 'c', 'd']);
  });

  it('moves down to the node nearest in x', () => {
    assert.equal(navigate(rows, positions, 'b', 'ArrowDown'), 'd');
    assert.equal(navigate(rows, positions, 'a', 'ArrowDown'), 'c');
  });

  it('stays put at the top and bottom rather than wrapping', () => {
    assert.equal(navigate(rows, positions, 'a', 'ArrowUp'), 'a');
    assert.equal(navigate(rows, positions, 'd', 'ArrowDown'), 'd');
  });

  it('jumps to the ends with Home and End', () => {
    assert.equal(navigate(rows, positions, 'c', 'Home'), 'a');
    assert.equal(navigate(rows, positions, 'a', 'End'), 'd');
  });

  it('starts at the first node when nothing is selected', () => {
    for (const key of ['ArrowRight', 'ArrowLeft', 'ArrowUp', 'ArrowDown']) {
      assert.equal(navigate(rows, positions, null, key), 'a');
    }
  });

  it('reports no answer for a key it does not handle', () => {
    assert.equal(navigate(rows, positions, 'a', 'x'), null);
  });

  it('has no answer at all for an empty graph', () => {
    assert.equal(navigate([], {}, null, 'ArrowRight'), null);
  });
});

describe('edgeAnchors', () => {
  const upper = { x: 0, y: 0, width: 100, height: 50 };
  const lower = { x: 0, y: 200, width: 100, height: 50 };

  it('leaves the bottom of the upper box and arrives at the top of the lower', () => {
    const a = edgeAnchors(upper, lower);
    assert.deepEqual(a, { x1: 50, y1: 50, x2: 50, y2: 200, axis: 'vertical' });
  });

  it('reverses when the target is above', () => {
    const a = edgeAnchors(lower, upper);
    assert.deepEqual(a, { x1: 50, y1: 200, x2: 50, y2: 50, axis: 'vertical' });
  });

  // Two members of a cycle share a layer, so they sit side by side. An edge
  // that always left the bottom would run backwards through its own box.
  it('goes side to side when the boxes overlap vertically', () => {
    const right = { x: 300, y: 10, width: 100, height: 50 };
    const a = edgeAnchors(upper, right);
    assert.equal(a.axis, 'horizontal');
    assert.equal(a.x1, 100, 'did not leave the right-hand side');
    assert.equal(a.x2, 300, 'did not arrive at the left-hand side');
  });

  it('leaves the left side when the target is to the left', () => {
    const left = { x: -300, y: 10, width: 100, height: 50 };
    const a = edgeAnchors(upper, left);
    assert.equal(a.x1, 0);
    assert.equal(a.x2, -200);
  });
});

describe('edgeGeometry', () => {
  const upper = { x: 0, y: 0, width: 100, height: 50 };
  const lower = { x: 400, y: 200, width: 100, height: 50 };

  it('produces a single cubic between the two anchors', () => {
    const { d } = edgeGeometry(upper, lower);
    assert.match(d, /^M50 50C/);
    assert.ok(d.endsWith('450 200'), d);
  });

  // A straight-line midpoint sits off the curve wherever it bends, and a
  // condition floating beside the wrong edge is worse than no condition.
  it('puts the label on the curve, not on the chord', () => {
    const { mid } = edgeGeometry(upper, lower);
    assert.equal(mid.y, 125, 'the midpoint is not halfway down');
    assert.equal(mid.x, 250, 'the midpoint is not halfway across');
  });

  it('bends by a bounded amount, so a long edge does not loop', () => {
    const far = { x: 0, y: 100000, width: 100, height: 50 };
    const { d } = edgeGeometry(upper, far);
    const control = Number(d.split('C')[1].split(' ')[1]);
    assert.ok(control <= 50 + 64, `control point ran away to ${control}`);
  });

  it('does not divide by zero when two boxes touch', () => {
    const touching = { x: 0, y: 50, width: 100, height: 50 };
    const { d, mid } = edgeGeometry(upper, touching);
    assert.ok(!d.includes('NaN'), d);
    assert.ok(Number.isFinite(mid.x) && Number.isFinite(mid.y));
  });
});

describe('edgePaths', () => {
  const g = webstack();
  const { positions, sizes } = layoutGraph(g, {});
  const boxes = Object.fromEntries(
    Object.keys(positions).map((id) => [id, boxOf(positions[id], sizes[id])]),
  );

  // 767 edges is 767 style resolutions and 767 paint boxes for lines nobody
  // clicks. Subpaths in one `d` render identically and cost one element.
  it('emits one path per edge kind, not one per edge', () => {
    const paths = edgePaths(g.edges, boxes);
    const kinds = paths.map((p) => p.kind).sort();
    assert.deepEqual(kinds, ['build', 'depends_on', 'network', 'publish', 'volume']);
    const depends = paths.find((p) => p.kind === 'depends_on')!;
    assert.equal(depends.count, 5);
    assert.equal(depends.d.split('M').length - 1, 5, 'five subpaths, one per edge');
  });

  it('draws no line for a bind, which has no far side', () => {
    assert.ok(!edgePaths(g.edges, boxes).some((p) => p.kind === 'bind'));
  });

  it('skips an edge whose endpoint was never placed', () => {
    const orphan = [edge('depends_on', 'services.gateway', 'services.ghost')];
    assert.deepEqual(edgePaths(orphan, boxes), []);
  });
});

describe('edgeLabel', () => {
  // Which condition it is decides whether the stack starts, so it is on the
  // canvas — in the file's own word, not a friendlier one.
  it('carries the condition verbatim', () => {
    for (const condition of ['service_started', 'service_healthy', 'service_completed_successfully']) {
      assert.equal(
        edgeLabel(edge('depends_on', 'a', 'b', { depends_on: { condition } })),
        condition,
      );
    }
  });

  it('says when a dependency is declared not required', () => {
    assert.equal(
      edgeLabel(
        edge('depends_on', 'a', 'b', {
          depends_on: { condition: 'service_healthy', required: 'false' },
        }),
      ),
      'service_healthy · not required',
    );
  });

  it('labels nothing else', () => {
    assert.equal(edgeLabel(edge('network', 'a', 'b')), '');
    assert.equal(edgeLabel(edge('depends_on', 'a', 'b')), '');
  });
});

describe('legendEntries', () => {
  // DECISIONS.md 18: a bind is a MARKER on the service card, never an edge.
  // `internal/topology` emits it as a self-edge and `edgePaths` skips every
  // self-edge, so a `bind` row in the legend keys a stroke that does not exist
  // anywhere on the canvas — and it fell back to the plain swatch, so it was
  // drawn as an unremarkable thin line identical to `network`.
  //
  // It is dropped rather than restyled as a marker key: the list's accessible
  // name is "Edge kinds in this stack" and every row is a line swatch, so a
  // row IS a promise of a stroke. The marker itself needs no key — it reads
  // `bind ./gateway/nginx.conf → /etc/nginx/conf.d/default.conf` in words on
  // the card, which is the thing a legend exists to supply for marks that
  // cannot say what they are.
  it('leaves out bind, which is a marker on the card and never a path', () => {
    const kinds = legendEntries(webstack().edges);
    assert.deepEqual(kinds, ['depends_on', 'network', 'volume', 'publish', 'build']);
    assert.ok(!kinds.includes('bind' as EdgeKind), 'the legend keys a stroke the canvas never draws');
  });

  // The invariant behind that, and the reason the fix filters by drawn-ness
  // rather than deleting the string `'bind'` from the order: the legend names
  // exactly the kinds `edgePaths` emits a path for. If a bind ever gains a
  // real far side — DECISIONS.md 18 says that decision may reopen — the row
  // comes back on its own instead of being silently missing.
  it('names exactly the kinds edgePaths actually draws', () => {
    const g = webstack();
    const { positions, sizes } = layoutGraph(g, {});
    const boxes = Object.fromEntries(
      Object.entries(positions).map(([id, p]) => [
        id,
        boxOf(p, sizes[id] ?? { width: NODE_WIDTH, height: NODE_HEIGHT }),
      ]),
    );
    const drawn = edgePaths(g.edges, boxes).map((p) => p.kind);
    assert.deepEqual([...legendEntries(g.edges)].sort(), [...drawn].sort());
  });

  it('is empty for a stack with no edges', () => {
    assert.deepEqual(legendEntries([]), []);
  });
});

describe('truncate', () => {
  it('keeps the tail of a path, which is the identifying part', () => {
    assert.equal(truncateTail('/very/long/path/to/nginx.conf', 12), '…/nginx.conf');
  });

  it('keeps the head of a name', () => {
    assert.equal(truncateHead('policyserver-management', 10), 'policyser…');
  });

  it('leaves text that fits alone, either way round', () => {
    assert.equal(truncateTail('short', 12), 'short');
    assert.equal(truncateHead('short', 12), 'short');
  });
});

describe('contentBox', () => {
  it('is one node big when there is nothing to measure', () => {
    assert.deepEqual(contentBox({}), { x: 0, y: 0, width: NODE_WIDTH, height: NODE_HEIGHT });
  });

  it('covers a single node exactly, including its extent', () => {
    assert.deepEqual(contentBox({ a: { x: 10, y: 20 } }), {
      x: 10,
      y: 20,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    });
  });

  it('spans every node, negative coordinates included', () => {
    const box = contentBox({ a: { x: -100, y: -50 }, b: { x: 200, y: 100 } });
    assert.deepEqual(box, {
      x: -100,
      y: -50,
      width: 300 + NODE_WIDTH,
      height: 150 + NODE_HEIGHT,
    });
  });
});

describe('fitToView', () => {
  const box = { x: 0, y: 0, width: 400, height: 200 };

  it('refuses to compute a transform for a pane with no area', () => {
    assert.deepEqual(fitToView(box, 0, 500), { x: 0, y: 0, k: 1 });
    assert.deepEqual(fitToView(box, 500, 0), { x: 0, y: 0, k: 1 });
    assert.deepEqual(fitToView(box, -10, -10), { x: 0, y: 0, k: 1 });
  });

  // A four-node stack blown up to fill the pane reads as a different product
  // than the same stack with forty services in it.
  it('never magnifies past 1', () => {
    assert.equal(fitToView(box, 4000, 4000).k, 1);
  });

  it('shrinks to fit the tighter axis', () => {
    const { k } = fitToView(box, 200, 4000, 0);
    assert.equal(k, 0.5);
  });

  it('honours the padding', () => {
    const { k } = fitToView(box, 240, 4000, 20);
    assert.equal(k, (240 - 40) / 400);
  });

  it('centres the box in the pane', () => {
    const view = fitToView(box, 800, 400, 0);
    assert.equal(view.k, 1);
    assert.equal(view.x, 200);
    assert.equal(view.y, 100);
  });

  it('centres a box that does not start at the origin', () => {
    const view = fitToView({ x: 100, y: 50, width: 400, height: 200 }, 800, 400, 0);
    // The left edge of the content lands at the same place either way.
    assert.equal(view.x + 100 * view.k, 200);
    assert.equal(view.y + 50 * view.k, 100);
  });

  it('clamps rather than dividing by a zero-sized box', () => {
    const view = fitToView({ x: 0, y: 0, width: 0, height: 0 }, 800, 400);
    assert.ok(Number.isFinite(view.x) && Number.isFinite(view.y));
    assert.equal(view.k, 1);
  });

  it('never returns a scale outside the clamp', () => {
    const tiny = fitToView({ x: 0, y: 0, width: 1e6, height: 1e6 }, 100, 100);
    assert.equal(tiny.k, MIN_SCALE);
  });
});

describe('clampScale', () => {
  it('passes a scale already in range through unchanged', () => {
    assert.equal(clampScale(0.5), 0.5);
    assert.equal(clampScale(1), 1);
  });

  it('clamps both ends', () => {
    assert.equal(clampScale(0), MIN_SCALE);
    assert.equal(clampScale(-3), MIN_SCALE);
    assert.equal(clampScale(1000), MAX_SCALE);
  });

  it('holds at the boundaries', () => {
    assert.equal(clampScale(MIN_SCALE), MIN_SCALE);
    assert.equal(clampScale(MAX_SCALE), MAX_SCALE);
  });
});

describe('shouldRefit', () => {
  it('re-fits when the pane resizes and the reader has not taken over', () => {
    // The shipped defect: nothing re-fitted, so a graph fitted against a
    // pre-layout measurement stayed tiny in the corner forever.
    assert.equal(
      shouldRefit({ hasNodes: true, userAdjusted: false, width: 1200, height: 800 }),
      true,
    );
  });

  it('leaves a view the reader has panned or zoomed alone', () => {
    assert.equal(
      shouldRefit({ hasNodes: true, userAdjusted: true, width: 1200, height: 800 }),
      false,
    );
  });

  it('does not fit to a zero-sized pane', () => {
    // Fitting here returns the no-op viewport and burns the one chance to get
    // the initial fit right — which is how the corner-stack shipped.
    assert.equal(
      shouldRefit({ hasNodes: true, userAdjusted: false, width: 0, height: 0 }),
      false,
    );
  });

  it('does nothing when there is no graph', () => {
    assert.equal(
      shouldRefit({ hasNodes: false, userAdjusted: false, width: 1200, height: 800 }),
      false,
    );
  });
});

describe('stylesheet', () => {
  it('makes the hidden attribute actually hide', () => {
    // Every element the webview hides — the failure banner, the empty state,
    // the narrow-panel list — sets `display` in an author rule, which beats the
    // UA stylesheet's `[hidden] { display: none }`. Without an explicit rule,
    // `element.hidden = true` is inert: an empty banner sat on screen forever
    // and a stale empty-state message covered a correctly drawn graph.
    // Read the source stylesheet, not the bundle: the tests run from a
    // dist-test/ bundle where import.meta.url no longer points at the source.
    // Resolved against candidates rather than cwd: `npm --prefix extension
    // test` does not run from extension/, so a cwd-relative path passed
    // locally and failed from the repo root — a test that depends on where it
    // was invoked from is not a check.
    const candidates = [
      resolve(process.cwd(), 'webview/style.css'),
      resolve(process.cwd(), 'extension/webview/style.css'),
    ];
    const found = candidates.find((c) => existsSync(c));
    assert.ok(found, `style.css not found; looked in ${candidates.join(', ')}`);
    const source = readFileSync(found, 'utf8');
    // Strip comments first — the comment explaining this rule quotes the UA
    // stylesheet's own `[hidden] { display: none }`, which the naive match
    // found instead of the rule, and then reported the rule as broken.
    const css = source.replace(/\/\*[\s\S]*?\*\//g, '');
    const rule = css.match(/\[hidden\]\s*\{([^}]*)\}/);
    assert.ok(rule, 'style.css has no [hidden] rule');
    assert.match(rule[1], /display:\s*none\s*!important/);
  });
});

describe('wrapText', () => {
  it('leaves text that already fits alone', () => {
    assert.deepEqual(wrapText('nginx:alpine', 30, 2), ['nginx:alpine']);
  });

  it('wraps a long image reference instead of hiding its tag', () => {
    // The reason this exists. Elided to one line this reads
    // "ghcr.io/composure/example-0:1.0.…" — losing the tag, which is the single
    // thing a reader scans a canvas of images for.
    const lines = wrapText('ghcr.io/composure/example-0:1.0.3 · 2 ports', 30, 2);
    assert.equal(lines.length, 2);
    assert.ok(lines.join('').includes('1.0.3'), 'the tag survived the wrap');
    for (const line of lines) {
      assert.ok(line.length <= 30, `line too long: ${line}`);
    }
  });

  it('breaks after a separator, not mid-token', () => {
    const lines = wrapText('registry.example.com/team/service:2.4.1', 24, 2);
    assert.ok(
      lines[0].endsWith('/') || lines[0].endsWith(':'),
      `broke mid-token: ${lines[0]}`,
    );
  });

  it('elides only on the last line, when it genuinely cannot fit', () => {
    const lines = wrapText('a'.repeat(200), 20, 2);
    assert.equal(lines.length, 2);
    assert.ok(lines[1].endsWith('…'));
    assert.ok(!lines[0].includes('…'), 'earlier lines must not elide');
  });

  it('never exceeds the line budget', () => {
    for (const n of [1, 2, 3]) {
      assert.ok(wrapText('x/'.repeat(80), 12, n).length <= n);
    }
  });
});

describe('wrapImageRef', () => {
  it('keeps the tag when the registry path is too long to fit', () => {
    // wrapText elides from the end, which here spends the whole budget on the
    // registry host and drops the tag — the part a reader is scanning for and
    // the part that changes on an upgrade.
    const lines = wrapImageRef(
      'registry.internal.example.com/platform/very-long-service-name:2026.08.1',
      23,
      2,
    );
    assert.ok(lines.join('').includes('2026.08.1'), `tag was dropped: ${lines.join('|')}`);
    assert.ok(lines[0].startsWith('…'), 'the dropped head must be marked');
    for (const line of lines) {
      assert.ok(line.length <= 23, `line too long: ${line}`);
    }
  });

  it('wraps normally when the whole reference fits the budget', () => {
    assert.deepEqual(wrapImageRef('ghcr.io/composure/example-0:1.0.3', 23, 2), [
      'ghcr.io/composure/',
      'example-0:1.0.3',
    ]);
  });

  it('leaves a short reference on one line', () => {
    assert.deepEqual(wrapImageRef('redis:7-alpine', 23, 2), ['redis:7-alpine']);
  });
});

describe('contentBox with mixed sizes', () => {
  // Node kinds are different sizes now. Measuring them all as service-sized
  // over-reports the extent, and the auto-fit then leaves a margin of empty
  // canvas on two sides — the failure this product exists to replace, in a
  // milder form.
  it('measures each node at its own size', () => {
    const box = contentBox(
      { a: { x: 0, y: 0 }, b: { x: 100, y: 100 } },
      { a: { width: 50, height: 20 }, b: { width: 10, height: 10 } },
    );
    assert.deepEqual(box, { x: 0, y: 0, width: 110, height: 110 });
  });

  it('falls back to the service box for a node whose size is unknown', () => {
    assert.deepEqual(contentBox({ a: { x: 0, y: 0 } }, {}), {
      x: 0,
      y: 0,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    });
  });
});

describe('the 500-service case', () => {
  /** A stack the shape of examples/large: 500 services in a dependency chain. */
  function largeGraph(count: number): StackGraph {
    const nodes: GraphNode[] = [];
    const edges: GraphEdge[] = [];
    for (let i = 0; i < count; i++) {
      nodes.push(service(`svc-${i}`, i % 12, { image: `ghcr.io/composure/example-${i}:1.0.3` }));
      nodes.push(
        node(`services.svc-${i}.ports[0]`, 'port', {
          name: `${8000 + i}:80`,
          port: { published: String(8000 + i), target: '80', protocol: 'tcp', raw: `${8000 + i}:80` },
        }),
      );
      edges.push(edge('publish', `services.svc-${i}`, `services.svc-${i}.ports[0]`));
      if (i > 0) {
        edges.push(
          edge('depends_on', `services.svc-${i}`, `services.svc-${i - 1}`, {
            depends_on: { condition: 'service_started' },
          }),
        );
      }
    }
    nodes.push(node('networks.default', 'network'));
    for (let i = 0; i < count; i++) {
      edges.push(edge('network', `services.svc-${i}`, 'networks.default'));
    }
    return graphOf(nodes, edges, { max_layer: 11 });
  }

  // Not a benchmark with a pass mark — machines differ — but a ceiling loose
  // enough that only an accidental quadratic can break it. The arithmetic here
  // runs once per draw and once per drag frame, so a regression in it is felt
  // as a canvas that stutters when a node is moved.
  it('lays out and routes a 1001-node graph in well under a second', () => {
    const g = largeGraph(500);
    assert.equal(g.nodes.length, 1001);
    const started = Date.now();
    const { positions, sizes, rows } = layoutGraph(g, {});
    const boxes = Object.fromEntries(
      Object.keys(positions).map((id) => [id, boxOf(positions[id], sizes[id])]),
    );
    const paths = edgePaths(g.edges, boxes);
    const elapsed = Date.now() - started;

    assert.equal(Object.keys(positions).length, g.nodes.length);
    assert.equal(rows.flat().length, g.nodes.length, 'every node must stay keyboard-reachable');
    assert.equal(paths.length, 3, 'one path element per edge kind, whatever the edge count');
    assert.ok(elapsed < 1000, `layout and routing took ${elapsed}ms`);
  });

  // Routing and the iterated ordering are both new cost on the hot path:
  // `redrawEdges` runs this on every pointermove of a drag. Measured on the
  // real 771-node examples/large by harness/graphmetrics.ts: 1.4ms before,
  // 10.8ms after. The ceiling here is loose because machines differ, but it is
  // tight enough that losing the corridor cache — which is what takes the
  // 771-node case from 33ms to 9ms — breaks it.
  it('routes a 1001-node graph inside a drag frame budget', () => {
    const g = largeGraph(500);
    const { positions, sizes } = layoutGraph(g, {});
    const boxes = Object.fromEntries(
      Object.keys(positions).map((id) => [id, boxOf(positions[id], sizes[id])]),
    );
    let best = Infinity;
    for (let run = 0; run < 3; run++) {
      const started = Date.now();
      const routed = routeEdges(g.edges, boxes);
      best = Math.min(best, Date.now() - started);
      assert.equal(routed.length, g.edges.filter((e) => e.from !== e.to).length);
    }
    assert.ok(best < 150, `routing 1001 nodes took ${best}ms`);
  });

  it('keeps every node in exactly one row, so no node is unreachable', () => {
    const { rows, positions } = layoutGraph(largeGraph(120), {});
    const seen = new Set(rows.flat());
    assert.equal(seen.size, Object.keys(positions).length);
  });
});

/**
 * Every way CSS lets you write a colour that is not a theme token.
 *
 * The old guard knew three: hex, `rgb()` and `hsl()`. It did not know
 * `color: red`, and it did not know `oklch()`, `lab()`, `lch()`, `oklab()`,
 * `hwb()`, `color()` or `color-mix()` — all of which are shipping CSS, all of
 * which break every theme but the one they were written against, and none of
 * which anything in this repository would have reported.
 *
 * Assembled from fragments so that this file does not trip the check it defines,
 * and so the repository-wide grep that runs the same rule does not either.
 */
function colourLiteral(): RegExp {
  const functions = ['rg' + 'ba?', 'hs' + 'la?', 'hw' + 'b', 'la' + 'b', 'lc' + 'h', 'okl' + 'ab', 'okl' + 'ch', 'col' + 'or', 'col' + 'or-mix'];
  return new RegExp(['#[0-9a-fA-F]{3,8}\\b', `\\b(?:${functions.join('|')})\\(`].join('|'));
}

/**
 * Properties whose ENTIRE value is a colour.
 *
 * For these, anything that is not a `--vscode-*` token or one of the four
 * keywords that name no colour at all is a literal — which is how `color: red`
 * is caught without needing a list of the hundred and forty-eight CSS colour
 * names.
 */
const COLOUR_ONLY_PROPERTIES = [
  'color',
  'fill',
  'stroke',
  'background-color',
  'border-color',
  'border-left-color',
  'border-top-color',
  'outline-color',
  'caret-color',
  'text-decoration-color',
  'column-rule-color',
  'stop-color',
  'flood-color',
];

/**
 * Properties whose value CONTAINS a colour among other things.
 *
 * The guard above cannot be pointed at these: `1px solid var(--vscode-panel-border)`
 * is not a token and is not a keyword, so the colour-only rule would call every
 * one of them a literal. The stylesheet ships 73 such declarations. A hex or a
 * colour function inside one is still caught by `colourLiteral()`, which is a
 * line scan and does not care which property it is on — but a NAMED colour in a
 * shorthand is invisible to both, and `background: rebeccapurple` survived the
 * whole suite because of it.
 */
const COLOUR_BEARING_SHORTHANDS = [
  'background',
  'border',
  'border-top',
  'border-right',
  'border-bottom',
  'border-left',
  'border-block',
  'border-inline',
  'outline',
  'box-shadow',
  'text-shadow',
  'text-decoration',
  'column-rule',
  'list-style',
];

/** Values that name no colour and are therefore always allowed. */
const COLOURLESS_KEYWORDS = ['transparent', 'inherit', 'currentcolor', 'none', 'unset', 'initial', 'revert'];

/**
 * Every colour CSS names, so that `red` and `rebeccapurple` are told apart from
 * `solid`, `1px`, `center` and `no-repeat` by knowledge rather than by guess.
 *
 * The full list, not a handful of examples: a guard that knows six colour names
 * reports six colour names and lets the other hundred and forty-two through,
 * which is the same failure it was written to fix. `transparent` and
 * `currentcolor` are deliberately NOT here — they name no colour and live in
 * COLOURLESS_KEYWORDS, so both remain legitimate in a shorthand.
 */
const CSS_NAMED_COLOURS = new Set(
  ('aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet ' +
    'brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan ' +
    'darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen ' +
    'darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey ' +
    'darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite ' +
    'forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew ' +
    'hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue ' +
    'lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon ' +
    'lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime ' +
    'limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple ' +
    'mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue ' +
    'mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid ' +
    'palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum ' +
    'powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen ' +
    'seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal ' +
    'thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen').split(' '),
);

/**
 * A value with every function call removed, innermost first.
 *
 * `var(--vscode-charts-red)` ends in the word `red`, and a naive word scan would
 * report the one thing this whole rule is trying to encourage. Stripping calls
 * also removes `url(...)` and `linear-gradient(...)`; anything a colour function
 * hides is `colourLiteral()`'s job, not this one's.
 */
function withoutCalls(value: string): string {
  let out = value;
  for (;;) {
    const next = out.replace(/[a-zA-Z-]*\([^()]*\)/g, ' ');
    if (next === out) {
      return out;
    }
    out = next;
  }
}

/** The CSS named colours sitting bare in a shorthand value. */
function namedColoursIn(value: string): string[] {
  return withoutCalls(value)
    .toLowerCase()
    .split(/[^a-z]+/)
    .filter((word) => CSS_NAMED_COLOURS.has(word));
}

/** Every declaration in the sheet, as `[property, value]`, for one property list. */
function declarationsOf(css: string, properties: string[]): [string, string][] {
  const pattern = new RegExp(`(^|[;{\\s])(${properties.join('|')})\\s*:\\s*([^;}]+)`, 'gi');
  return [...css.matchAll(pattern)].map((m) => [m[2], m[3].trim()]);
}

/**
 * Declarations painting a colour that is not a theme token.
 *
 * Two rules, because CSS has two shapes. A colour-only property must BE a token:
 * anything else is a literal, which is how `color: red` is caught without a word
 * list. A shorthand may carry lengths, styles, keywords and `var()`, so only a
 * bare CSS colour name in it is a finding.
 */
export function untokenisedColours(css: string): string[] {
  const out: string[] = [];
  for (const [property, value] of declarationsOf(css, COLOUR_ONLY_PROPERTIES)) {
    const lower = value.toLowerCase();
    if (lower.includes('var(--vscode-') || COLOURLESS_KEYWORDS.includes(lower)) {
      continue;
    }
    out.push(`${property}: ${value}`);
  }
  for (const [property, value] of declarationsOf(css, COLOUR_BEARING_SHORTHANDS)) {
    if (namedColoursIn(value).length > 0) {
      out.push(`${property}: ${value}`);
    }
  }
  return out;
}

describe('no colour literals', () => {
  // DESIGN.md: Composure has no palette. Every colour is a `--vscode-*` token, on
  // the canvas as much as in the chrome — a single literal breaks every theme
  // except the one it was written against, and there are thousands of themes.
  // This is the mechanical form of that rule; the alternative is a promise.
  //
  // The pattern is assembled from fragments rather than written out, because a
  // test that spells the thing it forbids is a test that fails on itself — and
  // it would also trip the repository-wide grep that runs the same check.
  it('finds no colour literal anywhere in the shipped webview and host sources', () => {
    const literal = colourLiteral();
    const bases = [process.cwd(), resolve(process.cwd(), 'extension')];
    const base = bases.find((b) => existsSync(resolve(b, 'webview/style.css')));
    assert.ok(base, 'could not locate the extension sources');

    const offenders: string[] = [];
    const walk = (dir: string): void => {
      for (const entry of readdirSync(dir, { withFileTypes: true })) {
        const full = resolve(dir, entry.name);
        if (entry.isDirectory()) {
          walk(full);
          continue;
        }
        // Tests are not shipped, and this one has to be able to name the
        // pattern it enforces.
        if (!/\.(ts|css)$/.test(entry.name) || /\.test\.ts$/.test(entry.name)) {
          continue;
        }
        readFileSync(full, 'utf8')
          .split('\n')
          .forEach((line, i) => {
            if (literal.test(line)) {
              offenders.push(`${full}:${i + 1}: ${line.trim()}`);
            }
          });
      }
    };
    for (const root of ['webview', 'host', 'shared']) {
      walk(resolve(base!, root));
    }
    assert.deepEqual(offenders, [], `colour literals:\n${offenders.join('\n')}`);
  });

  it('actually catches one when it is there', () => {
    // A grep test that matches nothing is indistinguishable from a grep test
    // that is broken.
    const literal = colourLiteral();
    assert.ok(literal.test('color: #' + 'ff0000;'));
    assert.ok(literal.test('fill: r' + 'gb(1, 2, 3);'));
    assert.ok(literal.test('fill: h' + 'sl(1, 2%, 3%);'));
    assert.ok(!literal.test('fill: var(--vscode-charts-orange);'));
  });

  // The formats the old guard did not know. Every one of these is shipping CSS
  // and every one would have gone in unreported.
  it('catches the colour functions the old three-pattern guard missed', () => {
    const literal = colourLiteral();
    for (const value of [
      'color: okl' + 'ch(0.7 0.1 200)',
      'fill: la' + 'b(50% 40 59.5)',
      'stroke: lc' + 'h(50% 70 200)',
      'color: okl' + 'ab(0.4 0.1 0.1)',
      'background: hw' + 'b(90 10% 10%)',
      'color: col' + 'or(display-p3 1 0 0)',
      'border-color: col' + 'or-mix(in srgb, red 50%, blue)',
    ]) {
      assert.ok(literal.test(value), `${value} is not caught`);
    }
  });

  it('catches a named colour, which no pattern of literals can see', () => {
    // `color: red` contains no hex, no function and no digits. The only way to
    // catch it is to require that a colour-only property IS a theme token.
    assert.deepEqual(untokenisedColours('.x { color: red; }'), ['color: red']);
    assert.deepEqual(untokenisedColours('.x { fill: rebeccapurple }'), ['fill: rebeccapurple']);
    assert.deepEqual(untokenisedColours('.x { color: var(--vscode-foreground); }'), []);
    // Fallbacks are still tokens.
    assert.deepEqual(
      untokenisedColours('.x { color: var(--vscode-a, var(--vscode-b)); }'),
      [],
    );
    // Keywords that name no colour are not literals.
    for (const keyword of ['transparent', 'inherit', 'currentColor', 'none']) {
      assert.deepEqual(untokenisedColours(`.x { fill: ${keyword}; }`), []);
    }
  });

  it('catches a named colour in a SHORTHAND, which the colour-only rule cannot see', () => {
    // The mutation that survived the whole suite. `background` is not a
    // colour-only property, so the rule above never looked at it; `rebeccapurple`
    // is not a hex and not a function, so the line scan never saw it either.
    assert.deepEqual(
      untokenisedColours('.x { background: rebeccapurple; }'),
      ['background: rebeccapurple'],
    );
    assert.deepEqual(
      untokenisedColours('.x { border: 1px solid red; }'),
      ['border: 1px solid red'],
    );
    assert.deepEqual(
      untokenisedColours('.x { outline: 2px dashed hotpink; }'),
      ['outline: 2px dashed hotpink'],
    );
    for (const declaration of [
      'border-left: 3px solid tan',
      'border-bottom: 1px dotted plum',
      'border-top: 1px solid seagreen',
      'box-shadow: 0 1px 2px black',
      'text-decoration: underline wavy salmon',
    ]) {
      assert.equal(
        untokenisedColours(`.x { ${declaration}; }`).length,
        1,
        `${declaration} went unreported`,
      );
    }
  });

  it('tolerates everything a legitimate shorthand carries', () => {
    // The 73 shipped declarations are multi-token by nature. A guard that
    // reported them would be turned off within a week, so it has to know a
    // length from a line style from a colour name.
    for (const declaration of [
      'border: 1px solid var(--vscode-panel-border)',
      'border-left: 3px solid var(--vscode-charts-red)',
      'background: var(--vscode-editorWidget-background, var(--vscode-editor-background))',
      'outline: none',
      'background: transparent',
      'border-bottom: 1px solid currentColor',
      'border: 0',
      'background: no-repeat center / contain',
      'box-shadow: 0 0 0 1px var(--vscode-focusBorder)',
      'border-top: 1px dashed var(--vscode-descriptionForeground)',
    ]) {
      assert.deepEqual(
        untokenisedColours(`.x { ${declaration}; }`),
        [],
        `${declaration} was reported as a colour literal`,
      );
    }
    // `var(--vscode-charts-red)` ends in a colour name. Reporting it would make
    // the rule punish the exact thing it exists to require.
    assert.deepEqual(untokenisedColours('.x { background: var(--vscode-charts-red); }'), []);
  });

  it('knows the whole CSS colour list, not a handful of examples', () => {
    // 148 names ship in CSS. A guard that knows six lets 142 through.
    assert.equal(CSS_NAMED_COLOURS.size, 148);
    for (const name of ['red', 'rebeccapurple', 'hotpink', 'tan', 'peru', 'linen']) {
      assert.ok(CSS_NAMED_COLOURS.has(name), `${name} is not in the list`);
    }
    // These name no colour and must stay legal.
    for (const name of ['transparent', 'currentcolor', 'none', 'inherit']) {
      assert.equal(CSS_NAMED_COLOURS.has(name), false, `${name} is not a colour`);
    }
  });

  it('finds no untokenised colour in the shipped stylesheet', () => {
    const base = [process.cwd(), resolve(process.cwd(), 'extension')].find((b) =>
      existsSync(resolve(b, 'webview/style.css')),
    );
    const offenders = untokenisedColours(readFileSync(resolve(base!, 'webview/style.css'), 'utf8'));
    assert.deepEqual(
      offenders,
      [],
      `these declarations paint a colour that is not a theme token:\n  ${offenders.join('\n  ')}`,
    );
  });
});

describe('wrapBand', () => {
  // Measured, not guessed: examples/large declares 500 services and no
  // depends_on at all, so every one of them lands in one layer. Unwrapped that
  // band is 112,000px wide, auto-fit clamps at the minimum scale, and the
  // reader gets a hairline in an empty canvas — worse than the grid it replaced.
  it('leaves a small band as one row, so a layer never reads as two', () => {
    for (const n of [1, 4, 8]) {
      const band = Array.from({ length: n }, (_, i) => `n${i}`);
      assert.deepEqual(wrapBand(band), [band]);
    }
  });

  it('splits a large band into rows slightly wider than tall', () => {
    const band = Array.from({ length: 500 }, (_, i) => `n${i}`);
    const rows = wrapBand(band);
    assert.equal(rows.length, 18);
    assert.equal(rows[0].length, 29);
    assert.equal(rows.flat().length, 500, 'wrapping lost a node');
    assert.deepEqual(rows.flat(), band, 'wrapping reordered the band');
  });

  it('keeps the 500-service canvas inside a shape auto-fit can use', () => {
    const nodes = Array.from({ length: 500 }, (_, i) => service(`svc-${i}`, 0));
    const { positions, sizes } = layoutGraph(graphOf(nodes), {});
    const box = contentBox(positions, sizes);
    assert.ok(box.width < 8000, `the canvas is ${Math.round(box.width)}px wide`);
    // Wider than tall, the way a VS Code panel is.
    assert.ok(box.width > box.height, 'the canvas is taller than it is wide');
  });
});

/* -------------------------------------------------------------------------
 * Routing, ordering and label placement — design-fidelity gap 5.
 *
 * The 2026-08-13 gap report measured the graph pane on the real topologies and
 * found the tangle was NOT crossings: `examples/webstack` had exactly one
 * crossing, but six of its sixteen edges ran underneath a node box that was
 * neither of their endpoints, and one pair of `depends_on` labels overlapped.
 *
 * So these tests assert those three numbers on a fixture with four occupied
 * bands. A three-node fixture cannot tell a good arrangement from a lucky one,
 * and none of the checks below would fail on one.
 */

/** Samples an M/L/C/Q path into a polyline, the way the harness does. */
function samplePath(d: string): Point[] {
  const tokens = d.match(/[MLCQ]|-?\d+(?:\.\d+)?/g) ?? [];
  const pts: Point[] = [];
  let i = 0;
  let cur: Point = { x: 0, y: 0 };
  const num = () => Number(tokens[i++]);
  const push = (p: Point) => pts.push(p);
  while (i < tokens.length) {
    const cmd = tokens[i++];
    if (cmd === 'M') {
      cur = { x: num(), y: num() };
      push(cur);
    } else if (cmd === 'L') {
      const to = { x: num(), y: num() };
      for (let s = 1; s <= 8; s++) {
        const t = s / 8;
        push({ x: cur.x + (to.x - cur.x) * t, y: cur.y + (to.y - cur.y) * t });
      }
      cur = to;
    } else if (cmd === 'Q') {
      const c = { x: num(), y: num() };
      const to = { x: num(), y: num() };
      for (let s = 1; s <= 8; s++) {
        const t = s / 8;
        const u = 1 - t;
        push({
          x: u * u * cur.x + 2 * u * t * c.x + t * t * to.x,
          y: u * u * cur.y + 2 * u * t * c.y + t * t * to.y,
        });
      }
      cur = to;
    } else if (cmd === 'C') {
      const c1 = { x: num(), y: num() };
      const c2 = { x: num(), y: num() };
      const to = { x: num(), y: num() };
      for (let s = 1; s <= 24; s++) {
        const t = s / 24;
        const u = 1 - t;
        push({
          x: u * u * u * cur.x + 3 * u * u * t * c1.x + 3 * u * t * t * c2.x + t * t * t * to.x,
          y: u * u * u * cur.y + 3 * u * u * t * c1.y + 3 * u * t * t * c2.y + t * t * t * to.y,
        });
      }
      cur = to;
    } else {
      i++;
    }
  }
  return pts;
}

/**
 * How many of a graph's drawn edges pass over a box that is not an endpoint.
 *
 * The gap report's own measure, on the path that is actually drawn — so an
 * improvement here cannot be claimed by a change that only moves the number.
 */
function occludedEdges(graph: StackGraph, geometryOf: 'direct' | 'routed'): string[] {
  const { positions, sizes } = layoutGraph(graph, {});
  const boxes: Record<string, ReturnType<typeof boxOf>> = {};
  for (const id of Object.keys(positions)) {
    boxes[id] = boxOf(positions[id], sizes[id]);
  }
  const drawn =
    geometryOf === 'routed'
      ? routeEdges(graph.edges, boxes)
      : graph.edges
          .filter((e) => e.from !== e.to && boxes[e.from] && boxes[e.to])
          .map((e) => ({ edge: e, geometry: edgeGeometry(boxes[e.from], boxes[e.to]) }));

  const over: string[] = [];
  for (const { edge: e, geometry } of drawn) {
    const pts = samplePath(geometry.d);
    let hit = false;
    for (let i = 1; i < pts.length - 1 && !hit; i++) {
      for (const [id, b] of Object.entries(boxes)) {
        if (id === e.from || id === e.to) {
          continue;
        }
        if (pts[i].x > b.x && pts[i].x < b.x + b.width && pts[i].y > b.y && pts[i].y < b.y + b.height) {
          hit = true;
          break;
        }
      }
    }
    if (hit) {
      over.push(`${e.from} -> ${e.to}`);
    }
  }
  return over;
}

describe('routing an edge around the boxes in its way', () => {
  it('leaves no edge running under a node that is not one of its endpoints', () => {
    const graph = webstack();
    const before = occludedEdges(graph, 'direct');
    const after = occludedEdges(graph, 'routed');
    // The direct cubic is what shipped, and it is measurably bad on this
    // topology: the numbers below are the failure, not a placeholder.
    // Three on this fixture; six of sixteen on the real examples/webstack,
    // which the harness measures (`harness/graphmetrics.ts`). Either way the
    // assertion below has something to fail against.
    assert.ok(
      before.length >= 3,
      `the direct curve is supposed to be occluded here; it was ${before.length}`,
    );
    assert.deepEqual(after, [], `still running under a box: ${after.join(', ')}`);
  });

  it('does not widen the canvas to get out of the way', () => {
    const graph = webstack();
    const { positions, sizes } = layoutGraph(graph, {});
    const boxes: Record<string, ReturnType<typeof boxOf>> = {};
    for (const id of Object.keys(positions)) {
      boxes[id] = boxOf(positions[id], sizes[id]);
    }
    const box = contentBox(positions, sizes);
    for (const { geometry } of routeEdges(graph.edges, boxes)) {
      for (const p of samplePath(geometry.d)) {
        assert.ok(
          p.x >= box.x - 1 && p.x <= box.x + box.width + 1,
          `a route left the canvas at x=${p.x}, which auto-fit would not show`,
        );
      }
    }
  });

  it('leaves an unobstructed edge exactly as it was — one cubic, unchanged', () => {
    // Two boxes with nothing between them. Every edge in a stack simple enough
    // to have no obstacles must be byte-identical to what shipped, or this
    // change is a rewrite of the whole canvas rather than a fix to six edges.
    const a = { x: 0, y: 0, width: NODE_WIDTH, height: NODE_HEIGHT };
    const b = { x: 0, y: 200, width: NODE_WIDTH, height: NODE_HEIGHT };
    assert.equal(routeGeometry(a, b, [a, b]).d, edgeGeometry(a, b).d);
  });

  it('keeps the direct curve when there is no corridor to use', () => {
    // A wall: the whole width between the two boxes is covered, so there is
    // nowhere to route through. Refusing beats inventing a line.
    const a = { x: 0, y: 0, width: 100, height: 40 };
    const b = { x: 0, y: 300, width: 100, height: 40 };
    const wall = { x: -400, y: 140, width: 900, height: 40 };
    assert.equal(routeGeometry(a, b, [a, b, wall]).d, edgeGeometry(a, b).d);
  });

  it('keeps the direct curve when there is no gutter to turn in', () => {
    // The obstacle starts 2px below the source: there is no clear strip to run
    // sideways along, and a corner drawn inside a box is worse than a crossing.
    const a = { x: 0, y: 0, width: 100, height: 40 };
    const b = { x: 0, y: 300, width: 100, height: 40 };
    const tight = { x: 20, y: 42, width: 60, height: 200 };
    assert.equal(routeGeometry(a, b, [a, b, tight]).d, edgeGeometry(a, b).d);
  });

  it('routes around a box that the direct curve goes through', () => {
    const a = { x: 0, y: 0, width: 100, height: 40 };
    const b = { x: 0, y: 400, width: 100, height: 40 };
    const wall = { x: -60, y: 180, width: 220, height: 60 };
    // A box off to the right, in the source's own band. It is not in the way;
    // it is there because the corridor may only be looked for inside the
    // graph's own horizontal extent, and without it there is no graph to the
    // right of the wall to route through.
    const far = { x: 300, y: 0, width: 100, height: 40 };
    const routed = routeGeometry(a, b, [a, b, wall, far]);
    assert.notEqual(routed.d, edgeGeometry(a, b).d);
    for (const p of samplePath(routed.d)) {
      assert.ok(
        !(p.x > wall.x && p.x < wall.x + wall.width && p.y > wall.y && p.y < wall.y + wall.height),
        `the route still enters the box at ${p.x},${p.y}`,
      );
    }
  });

  it('is deterministic: the same graph routes identically twice', () => {
    const graph = webstack();
    const geometry = () => {
      const { positions, sizes } = layoutGraph(graph, {});
      const boxes: Record<string, ReturnType<typeof boxOf>> = {};
      for (const id of Object.keys(positions)) {
        boxes[id] = boxOf(positions[id], sizes[id]);
      }
      return routeEdges(graph.edges, boxes).map((r) => r.geometry.d);
    };
    assert.deepEqual(geometry(), geometry());
  });

  it('still emits one path element per edge kind, whatever the routing did', () => {
    const graph = webstack();
    const { positions, sizes } = layoutGraph(graph, {});
    const boxes: Record<string, ReturnType<typeof boxOf>> = {};
    for (const id of Object.keys(positions)) {
      boxes[id] = boxOf(positions[id], sizes[id]);
    }
    const kinds = edgePaths(graph.edges, boxes).map((p) => p.kind);
    assert.deepEqual(new Set(kinds).size, kinds.length, 'a kind was emitted twice');
  });
});

describe('clearCorridor', () => {
  it('takes the free x nearest the one wanted', () => {
    const blocked = [{ x: 0, y: 0, width: 100, height: 10 }];
    // Wanted 50, which is inside the box; the nearest free x is just past its
    // right edge, because the clearance is the same either side and 50 is not
    // in the middle.
    const x = clearCorridor(blocked, 60, { min: -500, max: 500 });
    assert.ok(x !== null && x >= 100 + ROUTE_CLEARANCE, `chose ${x}`);
  });

  it('refuses rather than leaving the graph when everything is covered', () => {
    const wall = [{ x: -1000, y: 0, width: 2000, height: 10 }];
    assert.equal(clearCorridor(wall, 0, { min: -400, max: 400 }), null);
  });

  it('merges touching boxes rather than routing between two abutting ones', () => {
    const row = [
      { x: 0, y: 0, width: 100, height: 10 },
      { x: 105, y: 0, width: 100, height: 10 },
    ];
    // The 5px gap is narrower than twice the clearance, so it is not a corridor.
    const x = clearCorridor(row, 102, { min: -500, max: 500 });
    assert.ok(x !== null && (x <= -ROUTE_CLEARANCE || x >= 205 + ROUTE_CLEARANCE), `chose ${x}`);
  });
});

describe('roundedPath', () => {
  it('drops a zero-length step rather than emitting a corner of NaN', () => {
    const d = roundedPath(
      [
        { x: 0, y: 0 },
        { x: 0, y: 0 },
        { x: 0, y: 50 },
      ],
      8,
    );
    assert.ok(!d.includes('NaN'), d);
    assert.equal(d, 'M0 0L0 50');
  });

  it('rounds a corner without moving the ends', () => {
    const d = roundedPath(
      [
        { x: 0, y: 0 },
        { x: 0, y: 100 },
        { x: 100, y: 100 },
      ],
      8,
    );
    assert.ok(d.startsWith('M0 0'), d);
    assert.ok(d.endsWith('L100 100'), d);
    assert.ok(d.includes('Q0 100'), d);
  });
});

describe('placing edge labels so two of them are never one', () => {
  function labels(n: number, mid: Point) {
    return Array.from({ length: n }, (_, i) => ({
      edge: edge('depends_on', `a${i}`, `b${i}`, {
        depends_on: { condition: 'service_healthy' },
      }),
      text: 'service_healthy',
      mid,
    }));
  }

  it('separates two labels that want the same point', () => {
    const placed = placeLabels(labels(2, { x: 0, y: 0 }));
    assert.equal(placed.filter((p) => p.dropped).length, 0);
    const [a, b] = placed.map((p) => p.plate);
    assert.ok(
      !(a.x + a.width > b.x && b.x + b.width > a.x && a.y + a.height > b.y && b.y + b.height > a.y),
      'the two plates still overlap',
    );
  });

  it('drops a label it cannot place rather than printing it through another', () => {
    // More labels on one point than there are offsets to try.
    const placed = placeLabels(labels(12, { x: 0, y: 0 }));
    assert.ok(placed.some((p) => p.dropped), 'nothing was dropped, so something overlaps');
    const shown = placed.filter((p) => !p.dropped).map((p) => p.plate);
    for (let i = 0; i < shown.length; i++) {
      for (let j = i + 1; j < shown.length; j++) {
        const a = shown[i];
        const b = shown[j];
        assert.ok(
          !(a.x + a.width > b.x && b.x + b.width > a.x && a.y + a.height > b.y && b.y + b.height > a.y),
          'two drawn labels overlap',
        );
      }
    }
  });

  it('prefers an offset that is also clear of the node boxes', () => {
    const box = { x: -60, y: -8, width: 120, height: 15 };
    const [placed] = placeLabels(labels(1, { x: 0, y: 0 }), [box]);
    assert.equal(placed.dropped, false);
    assert.notEqual(placed.at.y, 0, 'the label stayed on top of a node box');
  });

  it('leaves no pair of labels overlapping on the four-band example', () => {
    const graph = webstack();
    const { positions, sizes } = layoutGraph(graph, {});
    const boxes: Record<string, ReturnType<typeof boxOf>> = {};
    for (const id of Object.keys(positions)) {
      boxes[id] = boxOf(positions[id], sizes[id]);
    }
    const routed = routeEdges(graph.edges, boxes);
    const placed = placeLabels(
      routed
        .map(({ edge: e, geometry }) => ({ edge: e, text: edgeLabel(e), mid: geometry.mid }))
        .filter((entry) => entry.text !== ''),
      Object.values(boxes),
    ).filter((p) => !p.dropped);
    assert.ok(placed.length >= 5, `only ${placed.length} labels were placed`);
    for (let i = 0; i < placed.length; i++) {
      for (let j = i + 1; j < placed.length; j++) {
        const a = placed[i].plate;
        const b = placed[j].plate;
        assert.ok(
          !(a.x + a.width > b.x && b.x + b.width > a.x && a.y + a.height > b.y && b.y + b.height > a.y),
          `${placed[i].text} overlaps ${placed[j].text}`,
        );
      }
    }
  });
});

describe('orderBands, sweeping until it stops improving', () => {
  it('re-orders the services against the resource band below them', () => {
    // The defect the single downward sweep had: the bottom band is ordered
    // against the services and the services are never ordered against IT, so
    // every resource attachment is accommodated by nobody. With nothing above
    // the service band, one downward sweep can do nothing at all here.
    // Four services on two networks, interleaved. Re-ordering the resource band
    // cannot fix this — there are only two resources and their order is already
    // the best available — so the ONLY move that shortens anything is moving
    // the services, which one downward sweep can never do.
    const bands = [[], ['a', 'b', 'c', 'd'], ['r1', 'r2']];
    orderBands(bands, [
      edge('network', 'a', 'r1'),
      edge('network', 'b', 'r2'),
      edge('network', 'c', 'r1'),
      edge('network', 'd', 'r2'),
    ]);
    assert.deepEqual(bands[1], ['a', 'c', 'b', 'd']);
  });

  it('never leaves an arrangement worse than the one it was given', () => {
    // The property that makes sweeping safe: a barycentre pass is not monotone,
    // so the sweeps are scored and the best one is what survives.
    const bands = [
      ['p0', 'p1', 'p2', 'p3'],
      ['s0', 's1', 's2', 's3', 's4', 's5'],
      ['n0', 'n1', 'n2'],
    ];
    const edges = [
      edge('publish', 's5', 'p0'),
      edge('publish', 's3', 'p1'),
      edge('publish', 's1', 'p2'),
      edge('publish', 's0', 'p3'),
      edge('network', 's0', 'n0'),
      edge('network', 's1', 'n1'),
      edge('network', 's2', 'n2'),
      edge('network', 's3', 'n0'),
      edge('network', 's4', 'n1'),
      edge('network', 's5', 'n2'),
    ];
    const before = orderSpan(bands.map((b) => [...b]), edges);
    orderBands(bands, edges);
    assert.ok(
      orderSpan(bands, edges) <= before,
      `the ordering made it worse: ${orderSpan(bands, edges)} > ${before}`,
    );
  });

  it('loses no node and duplicates none, however many sweeps it runs', () => {
    const bands = [
      Array.from({ length: 20 }, (_, i) => `p${i}`),
      Array.from({ length: 40 }, (_, i) => `s${i}`),
      Array.from({ length: 7 }, (_, i) => `n${i}`),
    ];
    const flat = bands.flat().slice().sort();
    const edges: GraphEdge[] = [];
    for (let i = 0; i < 40; i++) {
      edges.push(edge('network', `s${i}`, `n${i % 7}`));
      if (i < 20) {
        edges.push(edge('publish', `s${(i * 7) % 40}`, `p${i}`));
      }
    }
    orderBands(bands, edges);
    assert.deepEqual(bands.flat().slice().sort(), flat);
  });

  it('is stable: ordering an already-ordered set of bands changes nothing', () => {
    const bands = [
      ['p0', 'p1', 'p2'],
      ['s0', 's1', 's2', 's3'],
      ['n0', 'n1'],
    ];
    const edges = [
      edge('publish', 's0', 'p0'),
      edge('publish', 's1', 'p1'),
      edge('publish', 's3', 'p2'),
      edge('network', 's0', 'n0'),
      edge('network', 's1', 'n0'),
      edge('network', 's2', 'n1'),
      edge('network', 's3', 'n1'),
    ];
    orderBands(bands, edges);
    const once = bands.map((b) => [...b]);
    orderBands(bands, edges);
    assert.deepEqual(bands, once);
  });
});
