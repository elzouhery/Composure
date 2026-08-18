// Search, focus and collapse — story 4.4.
//
// A webview cannot be launched under `node --test`, so what is tested is what
// was extracted: every decision these three features make. That is the whole
// reason view.ts exists as a pure module — four defects shipped in 4.1 from
// logic buried in DOM callbacks no test could reach, and the criteria here
// ("the graph must not re-lay-out", "expanding restores exactly the previous
// positions") are exactly the kind that fail silently and look fine.

import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  GROUP_PREFIX,
  collapse,
  collapseStatus,
  collapsedPositions,
  emphasis,
  focusStatus,
  groupPosition,
  impactSet,
  isGroupId,
  matchesQuery,
  persistablePositions,
  profileNotice,
  searchHaystack,
  searchMatches,
  searchStatus,
} from './view';
import { layoutGraph } from './layout';
import type { EdgeKind, GraphEdge, GraphNode, NodeKind, Point, StackGraph } from '../shared/protocol';

const origin = { file: 'compose.yaml', line: 1, column: 1, step: 0 };

function node(id: string, kind: NodeKind, over: Partial<GraphNode> = {}): GraphNode {
  return {
    id,
    kind,
    name: id.split('.').pop() ?? id,
    origin,
    declared: true,
    external: false,
    profiles: [],
    layer: 0,
    ...over,
  };
}

function service(name: string, over: Partial<GraphNode> = {}): GraphNode {
  return node(`services.${name}`, 'service', { name, ...over });
}

function edge(kind: EdgeKind, from: string, to: string): GraphEdge {
  return { kind, from, to, origin };
}

function graphOf(nodes: GraphNode[], edges: GraphEdge[] = []): StackGraph {
  return { profiles: [], nodes, edges, cycles: [], dangling: [], max_layer: 0 };
}

/* -------------------------------------------------------------------------
 * The message contract.
 * ---------------------------------------------------------------------- */

describe('the webview handles everything the host can send', () => {
  // Found by writing this test: the host has posted `selection` since story
  // 4.3 — the cursor moving in the YAML moving the graph selection — and the
  // webview's switch had no case for it, so the message was silently dropped
  // and half of a shipped acceptance criterion did nothing. It compiles clean:
  // a `switch` over a union with a missing case is legal TypeScript.
  //
  // Story 4.4 then added `impact` and `impactFailed`, which would have gone the
  // same way. So this is mechanical from here on.
  it('has a case for every HostMessage variant', () => {
    const root = [process.cwd(), resolve(process.cwd(), 'extension')].find((b) =>
      existsSync(resolve(b, 'shared/protocol.ts')),
    );
    assert.ok(root, 'could not locate the extension sources');
    const protocol = readFileSync(resolve(root, 'shared/protocol.ts'), 'utf8');
    const main = readFileSync(resolve(root, 'webview/main.ts'), 'utf8');

    const union = protocol.slice(
      protocol.indexOf('export type HostMessage'),
      protocol.indexOf('export type WebviewMessage'),
    );
    const kinds = [...union.matchAll(/type:\s*'([a-zA-Z]+)'/g)].map((m) => m[1]);
    assert.ok(kinds.length >= 12, `only found ${kinds.length} host message kinds`);

    const unhandled = [...new Set(kinds)].filter((k) => !main.includes(`case '${k}':`));
    assert.deepEqual(
      unhandled,
      [],
      `the host can send these and the webview drops them silently:\n  ${unhandled.join('\n  ')}`,
    );
  });
});

describe('a keystroke never re-lays-out the graph', () => {
  // Story 4.4's central promise, and the one that fails SILENTLY and looks
  // fine: a search that re-ran the layout would still highlight correctly, it
  // would just throw the reader's viewport away on every character and cost
  // 0.84ms × 1001 nodes doing it. Nothing checked that `onSearch` only changes
  // classes — deleting the distinction passed the whole suite.
  //
  // main.ts cannot be instantiated here (it calls acquireVsCodeApi at module
  // load), so this reads its source with comments stripped. What it proves is
  // that the search path does not CALL the layout, which is the mutation; it
  // does not prove the layout is cheap, which the benchmark below does.
  const root = [process.cwd(), resolve(process.cwd(), 'extension')].find((b) =>
    existsSync(resolve(b, 'webview/main.ts')),
  );
  const source = readFileSync(resolve(root!, 'webview/main.ts'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^[ \t]*\/\/.*$/gm, '');

  /** The body of one method, by brace matching from its signature. */
  function methodBody(name: string): string {
    const at = source.indexOf(`private ${name}(`);
    assert.ok(at >= 0, `main.ts no longer declares ${name}`);
    let depth = 0;
    for (let i = source.indexOf('{', at); i < source.length; i++) {
      if (source[i] === '{') depth++;
      if (source[i] === '}') {
        depth--;
        if (depth === 0) {
          return source.slice(at, i + 1);
        }
      }
    }
    assert.fail(`could not find the end of ${name}`);
  }

  it('handles a keystroke without drawing the graph again', () => {
    const onSearch = methodBody('onSearch');
    assert.match(onSearch, /this\.applyFilter\(\)/, 'a keystroke no longer applies the filter');
    for (const forbidden of ['drawGraph', 'layoutGraph', 'capturePositions', 'setCollapse']) {
      assert.ok(
        !onSearch.includes(forbidden),
        `onSearch calls ${forbidden}, so every keystroke re-lays-out the graph`,
      );
    }
  });

  it('applies emphasis by class, and lays nothing out while doing it', () => {
    const applyFilter = methodBody('applyFilter');
    assert.match(applyFilter, /this\.graph\.emphasise\(ids\)/);
    for (const forbidden of ['drawGraph', 'layoutGraph', 'this.graph.render']) {
      assert.ok(
        !applyFilter.includes(forbidden),
        `applyFilter calls ${forbidden}, so search and focus re-lay-out the graph`,
      );
    }
  });

  it('re-lays-out only where the node set actually changed', () => {
    // Collapse DOES change the node set, so it must draw. This is the control:
    // without it, the two assertions above are satisfied by a build that never
    // lays out at all.
    assert.match(methodBody('setCollapse'), /this\.drawGraph\(/);
  });
});

/* -------------------------------------------------------------------------
 * Search.
 * ---------------------------------------------------------------------- */

describe('search', () => {
  const nodes = [
    service('web', { image: 'nginx:1.27' }),
    service('api', { image: 'ghcr.io/acme/api:2.1' }),
    service('db', { image: 'postgres:16' }),
    node('networks.backend', 'network'),
  ];

  it('matches on the name, the image, the config path and the kind', () => {
    // All four are things a reader types into a search box looking for a
    // service, and a box that only matched the name would send them back to
    // reading every node — which is the problem this story exists to solve.
    assert.deepEqual([...searchMatches(nodes, 'web')], ['services.web']);
    assert.deepEqual([...searchMatches(nodes, 'postgres')], ['services.db']);
    assert.deepEqual([...searchMatches(nodes, 'ghcr.io')], ['services.api']);
    assert.deepEqual([...searchMatches(nodes, 'networks.')], ['networks.backend']);
    assert.deepEqual([...searchMatches(nodes, 'network')], ['networks.backend']);
  });

  it('is case-insensitive and ignores surrounding whitespace', () => {
    assert.deepEqual([...searchMatches(nodes, '  NGINX ')], ['services.web']);
  });

  it('matches nothing at all for an empty query, rather than everything', () => {
    // The difference decides whether opening the panel dims the whole stack.
    assert.equal(searchMatches(nodes, '').size, 0);
    assert.equal(searchMatches(nodes, '   ').size, 0);
    assert.equal(matchesQuery(nodes[0], ''), false);
  });

  it('includes the second line of a node in what is searched', () => {
    assert.match(searchHaystack(nodes[3]), /network/);
  });

  it('says how many matched, in a sentence', () => {
    assert.equal(searchStatus('db', 1, 4), '1 of 4 nodes match “db”.');
    assert.equal(searchStatus('', 0, 4), '');
  });

  // The acceptance criterion, verbatim: a search that matches nothing leaves
  // the graph as it was and the field says so. `emphasis` returning null is
  // what "as it was" means to the renderer — the empty set would dim every
  // node in the stack.
  it('leaves the graph alone when nothing matches, and says so', () => {
    const matched = searchMatches(nodes, 'zzz');
    assert.equal(matched.size, 0);
    assert.equal(emphasis(matched), null);
    assert.equal(searchStatus('zzz', 0, 4), 'Nothing matches “zzz”. The graph is unchanged.');
  });

  it('emphasises exactly the matches when there are some', () => {
    const matched = searchMatches(nodes, 'services.');
    assert.equal(emphasis(matched)?.size, 3);
  });

  // The other half of "the graph must not re-lay-out": searching produces a
  // set of ids and NOTHING else. If this module ever returns positions, the
  // renderer will have something to move.
  it('returns ids only — nothing in search touches a position', () => {
    const before = layoutGraph(graphOf(nodes), {});
    searchMatches(nodes, 'web');
    const after = layoutGraph(graphOf(nodes), {});
    assert.deepEqual(after.positions, before.positions);
  });
});

/* -------------------------------------------------------------------------
 * Focus mode.
 * ---------------------------------------------------------------------- */

describe('focus mode', () => {
  it('is the core’s answer plus the node itself, and nothing derived', () => {
    const set = impactSet('services.db', ['services.api'], ['volumes.data']);
    assert.deepEqual([...set].sort(), ['services.api', 'services.db', 'volumes.data']);
  });

  it('does not double-count a node that is both a dependent and a dependency', () => {
    const set = impactSet('a', ['b'], ['b']);
    assert.equal(set.size, 2);
  });

  it('says the radius in words, both directions, singular and plural', () => {
    assert.equal(focusStatus('db', 3, 1), 'db — 3 things depend on it, it depends on 1 thing.');
    assert.equal(focusStatus('db', 1, 0), 'db — 1 thing depends on it, it depends on nothing.');
    assert.equal(
      focusStatus('db', 0, 0),
      'db — nothing depends on it, it depends on nothing.',
    );
  });
});

/* -------------------------------------------------------------------------
 * Collapse.
 * ---------------------------------------------------------------------- */

describe('collapse by network', () => {
  const nodes = [
    service('web'),
    service('api'),
    service('db'),
    node('networks.frontend', 'network'),
    node('networks.backend', 'network'),
    node('services.web.ports[0]', 'port'),
  ];
  const edges = [
    edge('network', 'services.web', 'networks.frontend'),
    edge('network', 'services.api', 'networks.frontend'),
    edge('network', 'services.db', 'networks.backend'),
    edge('publish', 'services.web', 'services.web.ports[0]'),
    edge('depends_on', 'services.web', 'services.api'),
    edge('depends_on', 'services.api', 'services.db'),
  ];
  const view = collapse(graphOf(nodes, edges), 'network');
  const group = `${GROUP_PREFIX}network:networks.frontend`;

  it('folds the members of a network into one node naming the group', () => {
    const folded = view.graph.nodes.find((n) => n.id === group);
    assert.ok(folded, 'no group node was produced');
    assert.equal(folded.name, 'frontend');
    assert.equal(folded.collapsed?.by, 'network');
  });

  it('counts the services, not the ports that hang off them', () => {
    // A group that said "3 services" because one of them publishes a port is a
    // confident wrong answer about the size of the thing being hidden.
    assert.equal(view.graph.nodes.find((n) => n.id === group)?.collapsed?.count, 2);
  });

  it('takes the satellites with it — a port cannot orbit a node that is gone', () => {
    assert.equal(view.memberOf['services.web.ports[0]'], group);
    assert.equal(
      view.graph.nodes.some((n) => n.id === 'services.web.ports[0]'),
      false,
    );
  });

  it('leaves a group of one alone', () => {
    // `db` is the only service on `backend`: folding it would replace a named
    // box with an unnamed one holding exactly the same information.
    assert.equal(view.memberOf['services.db'], undefined);
    assert.ok(view.graph.nodes.some((n) => n.id === 'services.db'));
  });

  it('drops relations that are now inside the box, and keeps the ones that cross it', () => {
    const kinds = view.graph.edges.map((e) => `${e.kind} ${e.from} → ${e.to}`);
    assert.equal(
      kinds.includes(`depends_on ${group} → ${group}`),
      false,
      'a relation between two folded members must not be drawn as a loop',
    );
    assert.ok(kinds.includes(`depends_on ${group} → services.db`));
  });

  it('does not emit the same edge twice after rewriting endpoints', () => {
    const seen = view.graph.edges.map((e) => `${e.kind} ${e.from} ${e.to}`);
    assert.equal(new Set(seen).size, seen.length);
  });

  it('gives the group an id that can never be a config path', () => {
    assert.ok(isGroupId(group));
    assert.equal(isGroupId('services.web'), false);
  });

  it('says what it folded', () => {
    assert.equal(collapseStatus('network', view), '3 nodes folded into 1 group by network.');
  });

  it('says when there is nothing to fold rather than appearing to do nothing', () => {
    const alone = collapse(graphOf([service('solo'), node('networks.n', 'network')], [
      edge('network', 'services.solo', 'networks.n'),
    ]), 'network');
    assert.equal(Object.keys(alone.members).length, 0);
    assert.match(collapseStatus('network', alone), /nothing to fold/);
  });
});

describe('collapse by profile', () => {
  const nodes = [
    service('a', { profiles: ['debug'] }),
    service('b', { profiles: ['debug', 'extra'] }),
    service('c', { profiles: [] }),
  ];
  const view = collapse(graphOf(nodes), 'profile');

  it('folds by the first profile a service declares, and leaves the rest', () => {
    // A service in three profiles cannot be in three boxes at once. First
    // declared is deterministic and explicable; anything else drifts between
    // redraws, which is the one thing a spatial view cannot survive.
    const group = `${GROUP_PREFIX}profile:debug`;
    assert.equal(view.memberOf['services.a'], group);
    assert.equal(view.memberOf['services.b'], group);
    assert.equal(view.memberOf['services.c'], undefined);
    assert.equal(view.graph.nodes.find((n) => n.id === group)?.name, 'debug');
  });
});

describe('positions across a collapse', () => {
  const nodes = [service('web'), service('api'), node('networks.n', 'network')];
  const edges = [
    edge('network', 'services.web', 'networks.n'),
    edge('network', 'services.api', 'networks.n'),
  ];
  const positions: Record<string, Point> = {
    'services.web': { x: 0, y: 0 },
    'services.api': { x: 200, y: 100 },
    'networks.n': { x: 100, y: 300 },
  };

  it('puts the group where the group was, not at the origin', () => {
    assert.deepEqual(groupPosition(['services.web', 'services.api'], positions), { x: 100, y: 50 });
    assert.equal(groupPosition(['nothing.here'], positions), null);
  });

  // The acceptance criterion: expanding restores EXACTLY the previous
  // positions. It is true by construction — the map is never mutated — and
  // this is the test that keeps it true.
  it('never mutates the positions it was given, so expanding restores them exactly', () => {
    const view = collapse(graphOf(nodes, edges), 'network');
    const snapshot = JSON.parse(JSON.stringify(positions));
    const collapsedMap = collapsedPositions(view, positions);
    assert.deepEqual(positions, snapshot, 'collapsing mutated the saved positions');
    assert.notEqual(collapsedMap, positions);
    // Every real node is still at its exact coordinate in the collapsed map,
    // and laying the base graph out again with the untouched map reproduces it.
    for (const id of Object.keys(positions)) {
      assert.deepEqual(collapsedMap[id], positions[id]);
    }
    const restored = layoutGraph(graphOf(nodes, edges), positions);
    for (const id of Object.keys(positions)) {
      assert.deepEqual(restored.positions[id], positions[id]);
    }
  });

  it('refuses to persist a group position — a group has no config path', () => {
    const view = collapse(graphOf(nodes, edges), 'network');
    const collapsedMap = collapsedPositions(view, positions);
    const persisted = persistablePositions(collapsedMap);
    assert.deepEqual(Object.keys(persisted).sort(), Object.keys(positions).sort());
    assert.equal(
      Object.keys(persisted).some(isGroupId),
      false,
      'a synthetic group id must never reach workspace state',
    );
  });
});

/* -------------------------------------------------------------------------
 * The size that matters.
 * ---------------------------------------------------------------------- */

describe('a stack too big to scan', () => {
  /** The shape of examples/large: 500 services, their ports, one network. */
  function largeGraph(count: number): StackGraph {
    const nodes: GraphNode[] = [node('networks.default', 'network')];
    const edges: GraphEdge[] = [];
    for (let i = 0; i < count; i++) {
      nodes.push(service(`svc-${i}`, { image: `ghcr.io/composure/example-${i}:1.0.3` }));
      nodes.push(
        node(`services.svc-${i}.ports[0]`, 'port', {
          name: `${8000 + i}:80`,
          port: { published: String(8000 + i), target: '80', protocol: 'tcp', raw: `${8000 + i}:80` },
        }),
      );
      edges.push(edge('publish', `services.svc-${i}`, `services.svc-${i}.ports[0]`));
      edges.push(edge('network', `services.svc-${i}`, 'networks.default'));
      if (i > 0) {
        edges.push(edge('depends_on', `services.svc-${i}`, `services.svc-${i - 1}`));
      }
    }
    return graphOf(nodes, edges);
  }

  // Not a benchmark with a pass mark — machines differ — but a ceiling loose
  // enough that only an accidental quadratic can break it. Search runs on every
  // keystroke, so a linear scan is the budget it has.
  it('searches a 1001-node graph on every keystroke without defeating it', () => {
    const g = largeGraph(500);
    assert.equal(g.nodes.length, 1001);
    const started = Date.now();
    let matched = 0;
    for (const q of ['svc-1', 'svc-12', 'svc-123', 'ghcr', 'nothing-here']) {
      matched += searchMatches(g.nodes, q).size;
    }
    const elapsed = Date.now() - started;
    assert.ok(matched > 0);
    assert.ok(elapsed < 500, `five searches over 1001 nodes took ${elapsed}ms`);
  });

  it('collapses and re-lays-out a 1001-node graph well inside a frame budget', () => {
    const g = largeGraph(500);
    const started = Date.now();
    const view = collapse(g, 'network');
    const positions = collapsedPositions(view, {});
    const placed = layoutGraph(view.graph, positions);
    const elapsed = Date.now() - started;

    // 500 services and their 500 ports fold into one node; the network they
    // were on is what is left beside it.
    assert.equal(view.graph.nodes.length, 2);
    assert.equal(Object.keys(placed.positions).length, 2);
    assert.ok(elapsed < 1000, `collapse and relayout took ${elapsed}ms`);
  });

  it('keeps every node keyboard-reachable in the collapsed view', () => {
    const view = collapse(largeGraph(20), 'network');
    const placed = layoutGraph(view.graph, {});
    assert.equal(placed.rows.flat().length, view.graph.nodes.length);
  });
});

/* -------------------------------------------------------------------------
 * The filtered-stack sentence — story 4.6.
 * ---------------------------------------------------------------------- */

describe('the sentence that says the stack on screen is filtered', () => {
  it('says nothing at all for Compose’s default set', () => {
    // A permanent banner over an unfiltered stack is noise, and noise is what
    // makes the filtered case unremarkable.
    assert.equal(profileNotice([]), '');
  });

  // THE DEFECT: the sentence read "Services that no active profile selects are
  // not drawn", which is false about the majority of every real compose file.
  // A service that declares NO profile is always drawn — `topology.Build` keeps
  // it under every set — and no active profile "selects" it either. So the
  // sentence told the reader that most of their stack had been removed while
  // it sat on the canvas in front of them, which is a worse failure than
  // silence: it is a confident wrong answer about what they are looking at.
  //
  // MUTATION: restore the old wording. Both assertions below fail.
  it('names the services that are actually missing, not every service without a profile', () => {
    const said = profileNotice(['debug']);
    assert.match(
      said,
      /declare a profile/,
      'the sentence does not say that being filtered out requires declaring a profile',
    );
    assert.doesNotMatch(
      said,
      /no active profile selects/,
      'the sentence claims services with no profile at all are not drawn — they always are',
    );
  });

  it('names the active set, and agrees with itself about how many there are', () => {
    assert.match(profileNotice(['debug']), /the debug profile is on/);
    assert.match(profileNotice(['debug', 'prod']), /the debug, prod profiles are on/);
  });

  // DECISIONS.md 19, which the criterion names: a filtered stack can show a
  // network with nothing attached to it, and that is a fact about the project
  // rather than a rendering fault. Said here or it reads as one.
  it('says the resource inventory is never filtered', () => {
    assert.match(profileNotice(['prod']), /Networks, volumes, configs and secrets/);
    assert.match(profileNotice(['prod']), /never filtered/);
  });
});
