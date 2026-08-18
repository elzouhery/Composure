// What the canvas actually draws — stories 4.2, 4.4, 4.5 and 5.4.
//
// `webview/graph.ts` is 888 lines and was the single largest untested surface in
// the product. Four uncaught mutations across three stories lived in it, and
// every one of them survived because the checks that covered graph.ts were
// SOURCE SCANS: `applyRovingTabIndex` present by name while every node gets
// `tabindex="-1"`, `applyEmphasis` present while it toggles nothing.
//
// Everything below drives the real GraphView against the fake DOM in
// `fakedom.test.ts` and asserts on the tree it produced. Each test names the
// mutation it was watched to fail against.

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { installDom, walk, type El } from './fakedom.test';

installDom();

/* The view creates elements at construction, so the DOM has to exist before the
 * module is loaded. `require` after installDom, not a top import. */
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { GraphView, arrowVariant, markerClass } = require('./graph') as typeof import('./graph');

import type {
  EdgeKind,
  GraphEdge,
  GraphNode,
  NodeKind,
  Point,
  SeverityCount,
  StackGraph,
} from '../shared/protocol';

/* -------------------------------------------------------------------------
 * Fixtures.
 * ---------------------------------------------------------------------- */

const origin = { file: '/w/compose.yaml', line: 3, column: 1, step: 0 };

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

const service = (name: string, over: Partial<GraphNode> = {}): GraphNode =>
  node(`services.${name}`, 'service', { name, image: `${name}:1`, ...over });

const edge = (kind: EdgeKind, from: string, to: string): GraphEdge => ({
  kind,
  from,
  to,
  origin,
});

function graphOf(nodes: GraphNode[], edges: GraphEdge[] = []): StackGraph {
  return { profiles: [], nodes, edges, cycles: [], dangling: [], max_layer: 0 };
}

interface Drawn {
  view: any;
  root: El;
  selected: (GraphNode | null)[];
  moved: Record<string, Point>[];
  activated: GraphNode[];
}

function draw(
  graph: StackGraph,
  opts: {
    saved?: Record<string, Point>;
    selected?: string | null;
    severities?: Record<string, SeverityCount>;
    missing?: string[];
  } = {},
): Drawn {
  const selected: (GraphNode | null)[] = [];
  const moved: Record<string, Point>[] = [];
  const activated: GraphNode[] = [];
  const view = new GraphView({
    onSelect: (n: GraphNode | null) => selected.push(n),
    onMove: (p: Record<string, Point>) => moved.push(p),
    onActivate: (n: GraphNode) => activated.push(n),
  });
  view.render(
    graph,
    opts.saved ?? {},
    true,
    opts.selected,
    opts.severities ?? {},
    opts.missing ?? [],
  );
  return { view, root: view.element as unknown as El, selected, moved, activated };
}

/** Every drawn node group, by config path. */
function nodesOf(root: El): Map<string, El> {
  const out = new Map<string, El>();
  for (const e of walk(root)) {
    if (e.classList.contains('node') && e.dataset.id !== undefined) {
      out.set(e.dataset.id, e);
    }
  }
  return out;
}

/** Every drawn edge path, by the kind class it carries. */
function edgesOf(root: El): El[] {
  return walk(root).filter((e) => e.tagName === 'path' && e.classList.contains('edge'));
}

beforeEach(() => {
  (globalThis as any).document.activeElement = null;
});

/* -------------------------------------------------------------------------
 * Story 4.4: search dims what did not match, and nothing moves.
 * ---------------------------------------------------------------------- */

describe('emphasis is applied to the drawn canvas — story 4.4', () => {
  const graph = graphOf([service('web'), service('api'), service('db')]);

  // MUTATION: `applyEmphasis` body emptied. The whole rendering half of search:
  // `searchMatches` still returns the right ids, `emphasise` is still called,
  // the status line still says "1 of 3 nodes match" — and the canvas does not
  // change at all. Every check that existed tested the id set.
  it('marks the matches and dims the rest', () => {
    const { view, root } = draw(graph);
    view.emphasise(new Set(['services.api']));
    const drawn = nodesOf(root);
    assert.equal(
      drawn.get('services.api')!.classList.contains('is-match'),
      true,
      'the matched node is not marked, so a search highlights nothing',
    );
    for (const id of ['services.web', 'services.db']) {
      assert.equal(
        drawn.get(id)!.classList.contains('is-dimmed'),
        true,
        `${id} is not dimmed, so a search pushes nothing back`,
      );
      assert.equal(drawn.get(id)!.classList.contains('is-match'), false);
    }
  });

  // MUTATION: the `is-filtered` toggle on the canvas removed. The stylesheet
  // hangs the whole dimmed presentation off it.
  it('puts the canvas itself into the filtered state, and takes it back out', () => {
    const { view, root } = draw(graph);
    assert.equal(root.classList.contains('is-filtered'), false);
    view.emphasise(new Set(['services.api']));
    assert.equal(
      root.classList.contains('is-filtered'),
      true,
      'the canvas never enters the filtered state, so nothing dims',
    );
    view.emphasise(null);
    assert.equal(root.classList.contains('is-filtered'), false, 'clearing the search leaves the canvas filtered');
    for (const el of nodesOf(root).values()) {
      assert.equal(el.classList.contains('is-dimmed'), false, 'a cleared search leaves nodes dimmed');
      assert.equal(el.classList.contains('is-match'), false);
    }
  });

  it('moves no box while emphasising — the acceptance criterion, on the tree', () => {
    const { view, root } = draw(graph);
    const before = [...nodesOf(root).entries()].map(([id, el]) => [id, el.getAttribute('transform')]);
    view.emphasise(new Set(['services.api']));
    const after = [...nodesOf(root).entries()].map(([id, el]) => [id, el.getAttribute('transform')]);
    assert.deepEqual(after, before, 'emphasis moved a node');
  });

  it('survives a redraw — a filter is re-applied to the boxes drawn again', () => {
    const { view, root } = draw(graph);
    view.emphasise(new Set(['services.api']));
    view.render(graph, {}, false, undefined, {}, []);
    assert.equal(nodesOf(root).get('services.api')!.classList.contains('is-match'), true);
  });
});

/* -------------------------------------------------------------------------
 * Story 4.2: ten kinds of relation, drawn as ten distinguishable lines.
 * ---------------------------------------------------------------------- */

describe('an edge carries its kind — story 4.2', () => {
  const nodes = [service('web'), service('api'), node('networks.n', 'network')];

  // MUTATION: `edge edge-${kind}` reduced to `edge`. Every one of the ten kinds
  // then renders as the identical line, which is the story's headline claim
  // deleted — and the arithmetic in `edgePaths` is untouched, so every layout
  // test still passes.
  it('names the kind on the drawn path, so the stylesheet can tell them apart', () => {
    const { root } = draw(
      graphOf(nodes, [
        edge('depends_on', 'services.web', 'services.api'),
        edge('network', 'services.web', 'networks.n'),
      ]),
    );
    const classes = edgesOf(root).map((e) => e.className);
    assert.equal(classes.length, 2, `expected one path per kind, got ${classes.length}`);
    assert.ok(
      classes.some((c) => c.includes('edge-depends_on')),
      `no path carries its kind: ${JSON.stringify(classes)}`,
    );
    assert.ok(classes.some((c) => c.includes('edge-network')));
  });

  // MUTATION: `arrowVariant` returns 'default' unconditionally. `depends_on`
  // then loses the arrowhead drawn at full ink and `build` loses its own, so
  // the two relations that decide whether a stack starts and what it is built
  // from look like every other line.
  it('points each kind at the arrowhead its weight requires', () => {
    assert.equal(arrowVariant('depends_on'), 'strong');
    assert.equal(arrowVariant('build'), 'build');
    assert.equal(arrowVariant('network'), 'default');
    const { root } = draw(
      graphOf(nodes, [
        edge('depends_on', 'services.web', 'services.api'),
        edge('network', 'services.web', 'networks.n'),
      ]),
    );
    const markers = new Map(
      edgesOf(root).map((e) => [
        (e.className.match(/edge-(\S+)/) ?? [])[1],
        e.getAttribute('marker-end'),
      ]),
    );
    assert.equal(markers.get('depends_on'), 'url(#composure-arrow-strong)');
    assert.equal(markers.get('network'), 'url(#composure-arrow-default)');
    assert.notEqual(
      markers.get('depends_on'),
      markers.get('network'),
      'every kind now takes the same arrowhead',
    );
  });

  it('declares one marker per variant, so the url resolves to something', () => {
    const { root } = draw(graphOf(nodes));
    const ids = walk(root)
      .filter((e) => e.tagName === 'marker')
      .map((e) => e.getAttribute('id'));
    for (const variant of ['default', 'strong', 'build']) {
      assert.ok(ids.includes(`composure-arrow-${variant}`), `no marker for ${variant}`);
    }
  });

  it('puts the depends_on condition on the canvas, not in a tooltip', () => {
    const g = graphOf(nodes, [
      {
        ...edge('depends_on', 'services.web', 'services.api'),
        depends_on: { condition: 'service_healthy', required: 'true' },
      },
    ]);
    const { root } = draw(g);
    const labels = walk(root).filter((e) => e.classList.contains('edge-label'));
    assert.equal(labels.length, 1, 'the condition is not drawn');
    assert.match(labels[0].textContent, /service_healthy/);
    assert.ok(labels[0].className.includes('condition-service_healthy'));
  });

  // Design-fidelity gap 5. `02-service-selected.png` shows the two conditions
  // on one pair of services rendered through each other as
  // `service_healthy  e_started` — which is neither word. Both edges have the
  // same two endpoints, so before placement they were translated to the same
  // point, and the second plate covered the first.
  it('does not print two conditions on the same pixel', () => {
    const g = graphOf(
      [service('web'), service('api')],
      [
        { ...edge('depends_on', 'services.web', 'services.api'), depends_on: { condition: 'service_healthy' } },
        { ...edge('depends_on', 'services.web', 'services.api'), depends_on: { condition: 'service_started' } },
      ] as GraphEdge[],
    );
    const { root } = draw(g);
    const labels = walk(root).filter((e) => e.classList.contains('edge-label'));
    assert.equal(labels.length, 2, 'a condition went missing');
    const at = labels.map((l) => l.getAttribute('transform'));
    assert.notEqual(at[0], at[1], `both conditions are drawn at ${at[0]}`);
  });

  it('draws no label at all rather than a stack of unreadable ones', () => {
    // Twelve conditions on one pair: there are not twelve legible places to put
    // a plate, so some of them must not be drawn. A label that cannot be read
    // is a wrong answer about whether the stack starts, not a cosmetic one.
    const g = graphOf(
      [service('web'), service('api')],
      Array.from({ length: 12 }, () => ({
        ...edge('depends_on', 'services.web', 'services.api'),
        depends_on: { condition: 'service_healthy' },
      })) as GraphEdge[],
    );
    const { root } = draw(g);
    const labels = walk(root).filter((e) => e.classList.contains('edge-label'));
    assert.ok(labels.length > 0, 'every label was dropped');
    assert.ok(labels.length < 12, `all ${labels.length} were drawn, so some overlap`);
    const at = labels.map((l) => l.getAttribute('transform'));
    assert.equal(new Set(at).size, at.length, 'two labels share a position');
  });
});

/* -------------------------------------------------------------------------
 * The stylesheet's half of the same claim.
 * ---------------------------------------------------------------------- */

describe('each edge kind resolves to a DISTINCT visual', () => {
  // A kind class on the path is worth nothing if two classes paint the same
  // line. MUTATION: the dash pattern stripped from `.edge-volume`,
  // `.edge-link` or `.edge-publish` — three kinds collapse onto the base
  // `.edge` rule and become indistinguishable, in a stylesheet whose own
  // comment says kinds are told apart by dash pattern and weight.
  const ROOT = [process.cwd(), resolve(process.cwd(), 'extension')].find((b) =>
    existsSync(resolve(b, 'webview/style.css')),
  );
  assert.ok(ROOT, 'could not locate the extension sources');
  const css = readFileSync(resolve(ROOT!, 'webview/style.css'), 'utf8').replace(
    /\/\*[\s\S]*?\*\//g,
    '',
  );

  /** The cascade of `.edge` plus `.edge-<kind>`, as the properties that separate them. */
  function visualOf(kind: string): Record<string, string> {
    // Seeded with the rendered default rather than left absent: a rule that
    // omits `stroke-dasharray` draws the identical solid line as one that sets
    // it to `none`, and comparing the DECLARATIONS rather than the result would
    // read those two as different visuals. Stripping the dash off `.edge-volume`
    // is exactly that mutation.
    const out: Record<string, string> = { 'stroke-dasharray': 'none' };
    for (const rule of css.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
      const selectors = rule[1].split(',').map((s) => s.trim());
      if (!selectors.includes('.edge') && !selectors.includes(`.edge-${kind}`)) {
        continue;
      }
      for (const decl of rule[2].split(';')) {
        const m = decl.match(/^\s*(stroke|stroke-width|stroke-dasharray|opacity)\s*:\s*(.+?)\s*$/);
        if (m) {
          out[m[1]] = m[2];
        }
      }
    }
    return out;
  }

  /**
   * `bind` is deliberately not drawn as a line: `layout.edgePaths` skips every
   * edge where `from === to`, and the core gives a bind edge `To: from` because
   * a host directory is not a node. It renders as a marker on the service
   * instead. That decision is asserted rather than assumed, below.
   */
  const DRAWN_KINDS: EdgeKind[] = [
    'depends_on',
    'network',
    'network_mode',
    'link',
    'volume',
    'config',
    'secret',
    'publish',
    'build',
  ];

  it('gives no two drawn kinds the same stroke, weight, dash and opacity', () => {
    const seen = new Map<string, string>();
    for (const kind of DRAWN_KINDS) {
      const visual = JSON.stringify(visualOf(kind));
      const clash = seen.get(visual);
      assert.equal(
        clash,
        undefined,
        `.edge-${kind} and .edge-${clash} render identically: ${visual}`,
      );
      seen.set(visual, kind);
    }
  });

  it('gives every drawn kind a rule of its own', () => {
    for (const kind of DRAWN_KINDS) {
      assert.match(
        css,
        new RegExp(`\\.edge-${kind}\\b`),
        `.edge-${kind} has no rule, so it renders as the base edge`,
      );
    }
  });

  it('records that a bind is a marker on its service and never a line', () => {
    const bind: GraphEdge = {
      ...edge('bind', 'services.web', 'services.web'),
      mount: { source: './data', target: '/data', mode: 'rw', read_only: false, host_path: '/w/data' },
    };
    const { root } = draw(graphOf([service('web')], [bind]));
    assert.deepEqual(edgesOf(root), [], 'a self-edge was drawn as a path to nowhere');
    const marker = walk(root).find((e) => e.classList.contains('node-marker'));
    assert.ok(marker, 'a bind produced neither a line nor a marker — it is invisible');
    assert.equal(markerClass('bind mount /data'), 'marker-bind');
  });
});

/* -------------------------------------------------------------------------
 * Story 4.5: one tab stop, and it moves.
 * ---------------------------------------------------------------------- */

describe('the graph has exactly one tab stop — story 4.5', () => {
  const graph = graphOf([service('web'), service('api'), service('db')]);

  // MUTATION: `applyRovingTabIndex` gives every node `tabindex="-1"`. The
  // function is still there, still called, still named — and the graph has no
  // tab stop at all, so a keyboard reader cannot reach a single node. The
  // existing check was `assert.match(graph, /applyRovingTabIndex/)`.
  it('offers one node to Tab and holds the rest at -1', () => {
    const { root } = draw(graph);
    const stops = [...nodesOf(root).entries()].filter(([, el]) => el.getAttribute('tabindex') === '0');
    assert.equal(
      stops.length,
      1,
      `${stops.length} nodes are tab stops; the listbox pattern is exactly one`,
    );
    assert.equal(stops[0][0], 'services.web', 'the tab stop is not the first node in reading order');
    for (const [id, el] of nodesOf(root)) {
      if (id !== stops[0][0]) {
        assert.equal(el.getAttribute('tabindex'), '-1', `${id} is a second tab stop`);
      }
    }
  });

  it('moves the tab stop to whatever is selected', () => {
    const { view, root } = draw(graph);
    view.selectById('services.db');
    const stops = [...nodesOf(root).entries()]
      .filter(([, el]) => el.getAttribute('tabindex') === '0')
      .map(([id]) => id);
    assert.deepEqual(
      stops,
      ['services.db'],
      'the tab stop did not follow the selection, so Tab returns to the top of the graph',
    );
  });

  it('holds the tab stop on the canvas itself only when there is nothing in it', () => {
    const { root } = draw(graph);
    assert.equal(root.getAttribute('tabindex'), '-1', 'the canvas competes with its own nodes for Tab');
    const { root: empty } = draw(graphOf([]));
    assert.equal(empty.getAttribute('tabindex'), '0', 'an empty graph cannot be reached at all');
  });

  it('names every node, and says what it is', () => {
    const { root } = draw(graph);
    for (const [id, el] of nodesOf(root)) {
      const label = el.getAttribute('aria-label') ?? '';
      assert.ok(label.length > 0, `${id} has no accessible name`);
      assert.match(label, /service/, `${id} is announced without saying what it is`);
      assert.equal(el.getAttribute('role'), 'option');
    }
  });

  it('reports the selection through aria-activedescendant, and drops it again', () => {
    const { view, root } = draw(graph);
    view.selectById('services.api');
    assert.equal(root.getAttribute('aria-activedescendant'), 'node:services.api');
    assert.equal(nodesOf(root).get('services.api')!.getAttribute('aria-selected'), 'true');
    view.selectById(null);
    assert.equal(root.getAttribute('aria-activedescendant'), null);
  });
});

/* -------------------------------------------------------------------------
 * Story 4.4: a folded group says how much it folded.
 * ---------------------------------------------------------------------- */

describe('a collapsed group is legible — story 4.4', () => {
  const folded = node('group:network:networks.frontend', 'service', {
    name: 'frontend',
    collapsed: { by: 'network', count: 4, members: [] },
  });

  // MUTATION: `describeNode` drops the count from a collapsed group's line
  // (layout.ts:156). The box then reads `frontend`, which is indistinguishable
  // from a service called frontend — a folded group that does not say how many
  // nodes are inside it is a box that has hidden the stack.
  it('says how many nodes it holds and how they were grouped', () => {
    const { root } = draw(graphOf([folded]));
    const group = nodesOf(root).get(folded.id)!;
    assert.match(
      group.textContent,
      /4 services/,
      `the folded box reads ${JSON.stringify(group.textContent)} — the count is gone`,
    );
    assert.match(group.textContent, /collapsed by network/);
  });

  it('says the same thing to a screen reader, and how to open it', () => {
    const { root } = draw(graphOf([folded]));
    const label = nodesOf(root).get(folded.id)!.getAttribute('aria-label') ?? '';
    assert.match(label, /4 services/, 'the group announces no size');
    assert.match(label, /press Enter to expand/, 'unfolding is a pointer-only capability');
  });

  it('marks the box as a group so the stylesheet can draw it as one', () => {
    const { root } = draw(graphOf([folded]));
    assert.equal(nodesOf(root).get(folded.id)!.classList.contains('is-group'), true);
  });
});

/* -------------------------------------------------------------------------
 * Story 5.4: the badge on the node.
 * ---------------------------------------------------------------------- */

describe('a node carries its diagnostic count — story 5.4', () => {
  const graph = graphOf([service('web'), service('db')]);
  const severities = { 'services.web': { error: 1, warning: 2, hint: 0 } };

  // MUTATION: the badge block deleted from `createNode`. Problems then exist
  // only once something is selected, so a reader opening a stack of forty
  // services has no idea where to look — which is story 5.4's first sentence.
  it('draws the disc and the number', () => {
    const { root } = draw(graph, { severities });
    const web = nodesOf(root).get('services.web')!;
    const dot = walk(web).find((e) => e.classList.contains('node-badge'));
    assert.ok(dot, 'no badge was drawn on a node with three findings');
    assert.ok(dot!.classList.contains('node-badge-error'), 'the badge does not carry its level');
    const count = walk(web).find((e) => e.classList.contains('node-badge-count'));
    assert.equal(count?.textContent, '3', 'the badge shows no number');
  });

  it('says the count in words as well, in the name and the tooltip', () => {
    const { root } = draw(graph, { severities });
    const web = nodesOf(root).get('services.web')!;
    assert.match(web.getAttribute('aria-label') ?? '', /1 error, 2 warnings/);
    const title = walk(web).find((e) => e.tagName === 'title');
    assert.match(title!.textContent, /1 error, 2 warnings/);
  });

  it('draws no badge where nothing was found, and none before anything was asked', () => {
    const { root } = draw(graph, { severities });
    const db = nodesOf(root).get('services.db')!;
    assert.equal(walk(db).some((e) => e.classList.contains('node-badge')), false);
    const { root: undiagnosed } = draw(graph);
    assert.equal(
      walk(undiagnosed).some((e) => e.classList.contains('node-badge')),
      false,
      'a badge appeared before the rules had run',
    );
  });
});

/* -------------------------------------------------------------------------
 * Selection and activation, which is what the shell is wired to.
 * ---------------------------------------------------------------------- */

describe('opening a node is separate from selecting one', () => {
  const graph = graphOf([service('web'), node('services.web.build', 'dockerfile')]);

  it('activates on Enter, and only when something is selected', () => {
    const { view, root, activated } = draw(graph);
    view.selectById('services.web.build');
    root.fire('keydown', { key: 'Enter' });
    assert.deepEqual(
      activated.map((n) => n.id),
      ['services.web.build'],
      'Enter did not open the node under the cursor',
    );
  });

  it('does not activate on arrow travel', () => {
    const { view, root, activated } = draw(graph);
    view.selectById('services.web');
    root.fire('keydown', { key: 'ArrowRight' });
    root.fire('keydown', { key: 'ArrowDown' });
    assert.deepEqual(activated, [], 'holding an arrow key opens a form per repeat');
  });

  it('reports a selection that no longer exists as a deselection', () => {
    const { view, selected } = draw(graph, { selected: 'services.web' });
    selected.length = 0;
    view.render(graphOf([service('api')]), {}, false);
    assert.deepEqual(selected, [null], 'a vanished selection was silently kept');
  });
});
