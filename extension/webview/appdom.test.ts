// The webview shell, driven end to end — stories 4.2, 4.3, 4.4, 5.4, 6.1, 6.3.
//
// `webview/main.ts` is the switch every host message lands in, and the only
// check over it was a source scan asserting that a `case 'x':` label exists for
// every variant of the union. A case kept and its BODY emptied passes that scan
// exactly, which is how `case 'selection':` — half of story 4.3's headline
// feature — shipped with nothing behind it.
//
// So this boots the real App against the fake DOM, posts real host messages at
// it, and asserts on the tree it drew and the messages it posted back. Each test
// names the mutation it was watched to fail against.

import { describe, it, before, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { installDom, walk, type El } from './fakedom.test';
import type {
  DockerfileForm,
  EdgeKind,
  GraphEdge,
  GraphNode,
  HostMessage,
  NodeKind,
  StackGraph,
  WebviewMessage,
} from '../shared/protocol';

const d = installDom();

/** Everything the webview posted to the host, in order. */
const posted: WebviewMessage[] = [];
(globalThis as any).acquireVsCodeApi = () => ({
  postMessage: (msg: WebviewMessage) => {
    posted.push(msg);
  },
});

// main.ts constructs the App at module load, so it is required after the DOM
// and `acquireVsCodeApi` exist — not imported at the top.
before(() => {
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  require('./main');
});

const root = (): El => d.document.getElementById('app') as El;

function send(msg: HostMessage): void {
  d.window.fire('message', { data: msg });
  d.clock.flush();
}

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

const edge = (kind: EdgeKind, from: string, to: string): GraphEdge => ({ kind, from, to, origin });

const GRAPH: StackGraph = {
  profiles: [],
  nodes: [
    service('web'),
    service('api'),
    node('services.web.build', 'dockerfile', { name: 'Dockerfile' }),
  ],
  edges: [edge('build', 'services.web', 'services.web.build')],
  cycles: [],
  dangling: [],
  max_layer: 0,
};

function graphMessage(over: Partial<Extract<HostMessage, { type: 'graph' }>> = {}): HostMessage {
  return {
    type: 'graph',
    file: '/w/compose.yaml',
    graph: GRAPH,
    positions: {},
    selected: null,
    fit: true,
    severities: {},
    missing: [],
    ...over,
  } as HostMessage;
}

/** The one-line strip naming the selection, shown in the narrow layout. */
const strip = (): El => walk(root()).find((e) => e.classList.contains('strip'))!;
const toolbarStatus = (): El => walk(root()).find((e) => e.classList.contains('toolbar-status'))!;
const graphEl = (): El => walk(root()).find((e) => e.classList.contains('graph'))!;
const nodesOf = (): Map<string, El> => {
  const out = new Map<string, El>();
  for (const e of walk(graphEl())) {
    if (e.classList.contains('node') && e.dataset.id !== undefined) {
      out.set(e.dataset.id, e);
    }
  }
  return out;
};
const rows = (): El[] => walk(root()).filter((e) => e.classList.contains('service-row'));

beforeEach(() => {
  posted.length = 0;
  d.document.activeElement = null;
  d.document.body.clientWidth = 900;
  // The App is module-level and outlives every test in this file, so a search
  // query left live by one test decides what the NEXT one sees dimmed —
  // `applyFilter` gives a live query priority over focus mode. The tests that
  // set one also clear it; this makes the guarantee structural rather than a
  // habit, so a new test that forgets cannot quietly weaken an old one.
  const field = walk(root()).find((e) => e.classList.contains('toolbar-search'));
  if (field !== undefined && field.value !== '') {
    field.value = '';
    field.fire('input');
  }
});

/* -------------------------------------------------------------------------
 * The graph message.
 * ---------------------------------------------------------------------- */

describe('a graph message becomes a drawn stack', () => {
  it('draws every node the core reported, and a row per node beside it', () => {
    send(graphMessage());
    assert.deepEqual(
      [...nodesOf().keys()].sort(),
      ['services.api', 'services.web', 'services.web.build'],
    );
    assert.equal(rows().length, 3, 'the narrow list disagrees with the canvas');
  });

  // MUTATION (host side, mirrored here): the graph message carries
  // `selected: null` instead of the stored selection. The reader reopens the
  // panel and the inspector shows the stack rather than the service they were
  // looking at — silently, because nothing on screen claims a selection.
  it('restores the selection the message carries', () => {
    send(graphMessage({ selected: 'services.api' }));
    assert.equal(
      nodesOf().get('services.api')!.getAttribute('aria-selected'),
      'true',
      'the restored selection is not on the canvas',
    );
    assert.match(strip().textContent, /api/, 'the strip does not name the restored selection');
    assert.equal(
      graphEl().getAttribute('aria-activedescendant'),
      'node:services.api',
      'a screen reader is not told what is selected',
    );
  });

  it('marks a Dockerfile node whose file is not on disk — story 6.3', () => {
    // MUTATION: `missing: []` posted to the graph. The build then draws as an
    // ordinary node and the reader has no way to see that the file it names is
    // not there, which is the one thing that answers "why does my build fail".
    send(graphMessage({ missing: ['services.web.build'] }));
    const row = rows().find((r) => r.dataset.id === 'services.web.build')!;
    assert.match(
      row.textContent,
      /missing — this file is not on disk/,
      'a missing Dockerfile is presented as an ordinary one',
    );
    assert.equal(row.classList.contains('is-missing'), true);
    const marker = walk(nodesOf().get('services.web.build')!).find((e) =>
      e.classList.contains('marker-missing'),
    );
    assert.ok(marker, 'the canvas says nothing about the missing file');
  });

  it('says the rules did not run rather than drawing a clean stack', () => {
    send(graphMessage());
    send({ type: 'diagnosticsUnavailable', file: '/w/compose.yaml', detail: 'core exited' });
    assert.match(toolbarStatus().textContent, /Checks did not run/);
    assert.match(toolbarStatus().textContent, /no answer, not no problem/);
  });
});

/* -------------------------------------------------------------------------
 * Story 4.6: choosing which profiles are active.
 * ---------------------------------------------------------------------- */

/**
 * A stack with two profiles, of which the drawn graph knows about ONE.
 *
 * The asymmetry is deliberate and is what makes these checks able to fail. If
 * every declared profile also appeared on a node, a control built out of the
 * nodes would look identical to one built out of the core's declared list —
 * and the criterion is precisely that the list comes from the core, because a
 * service the active set filters OUT is not a node at all.
 *
 * `services.tools` carries `debug`; nothing on the canvas mentions `prod`.
 */
const PROFILE_GRAPH: StackGraph = {
  profiles: [],
  nodes: [
    service('web'),
    service('api'),
    service('tools', { profiles: ['debug'] }),
    node('networks.frontnet', 'network', { name: 'frontnet' }),
    node('volumes.data', 'volume', { name: 'data' }),
  ],
  edges: [],
  cycles: [],
  dangling: [],
  max_layer: 0,
};

const profileButtons = (): El[] =>
  walk(root()).filter((e) => e.dataset.profile !== undefined);
const profileGroup = (): El | undefined =>
  walk(root()).find((e) => e.classList.contains('toolbar-profiles'));
const profileNote = (): El => walk(root()).find((e) => e.classList.contains('toolbar-note'))!;
const searchField = (): El => walk(root()).find((e) => e.classList.contains('toolbar-search'))!;
/** Empties the search field for real, listener and all. */
const clearSearch = (): void => {
  const field = searchField();
  field.value = '';
  field.fire('input');
};
const transformOf = (id: string): string => nodesOf().get(id)!.getAttribute('transform')!;

describe('the profile control — story 4.6', () => {
  // MUTATION: `renderProfiles` builds its list from `this.baseGraph.nodes`
  // (`new Set(nodes.flatMap(n => n.profiles))`) instead of from the message.
  // `debug` still appears, so a fixture whose profiles all appear on nodes
  // would pass — and `prod`, the profile whose services are FILTERED OUT and
  // which the reader most needs, silently never appears.
  it('offers one toggle per profile the core says the project declares', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    assert.deepEqual(
      profileButtons().map((b) => b.dataset.profile),
      ['debug', 'prod'],
      'the profile control is not the core’s declared list',
    );
    for (const button of profileButtons()) {
      // The same contract the existing toolbar buttons hold: a real button, an
      // accessible name, and its state reported rather than shown in colour.
      assert.equal(button.tagName, 'BUTTON', 'a profile toggle is not a real control');
      assert.equal(button.type, 'button');
      assert.ok(
        (button.getAttribute('aria-label') ?? '').includes(button.dataset.profile!),
        'a profile toggle has no accessible name naming the profile',
      );
      assert.equal(button.getAttribute('aria-pressed'), 'false');
      // Criterion 9: tab-reachable. It holds today because these are native
      // <button>s and nothing sets `tabindex` — which is a property nothing was
      // asserting, so a later `tabindex="-1"` (to stop the toolbar eating tab
      // stops, say) would take the whole control off the keyboard in silence.
      assert.equal(
        button.getAttribute('tabindex'),
        null,
        'a profile toggle was taken out of the tab order',
      );
    }
    const group = profileGroup()!;
    assert.equal(group.getAttribute('role'), 'group');
    assert.ok(group.getAttribute('aria-label'), 'the profile group has no name');
  });

  // MUTATION: the `declared.length === 0` branch deleted, so the group is built
  // empty. The reader gets a labelled control that filters nothing and a tab
  // stop that does nothing.
  it('offers no control at all when the project declares no profile', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug'], active: [] });
    assert.equal(profileButtons().length, 1, 'the control was never built, so its absence proves nothing');
    send({ type: 'profiles', file: '/w/compose.yaml', declared: [], active: [] });
    assert.deepEqual(profileButtons(), [], 'a stack with no profiles is offered profile toggles');
    assert.equal(
      profileGroup(),
      undefined,
      'the profile control is present and empty rather than absent',
    );
  });

  // MUTATION: the click listener deleted, or `setProfiles` never posted. The
  // buttons are drawn, they even look pressed, and the core is never asked a
  // different question — which is exactly the state this story exists to end.
  it('posts the whole active set when a profile is pressed, and reports its state', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    const debug = profileButtons().find((b) => b.dataset.profile === 'debug')!;
    const prod = profileButtons().find((b) => b.dataset.profile === 'prod')!;

    posted.length = 0;
    debug.fire('click');
    assert.deepEqual(
      posted.filter((m) => m.type === 'setProfiles'),
      [{ type: 'setProfiles', profiles: ['debug'] }],
      'pressing a profile asked the core nothing',
    );
    assert.equal(debug.getAttribute('aria-pressed'), 'true');
    assert.equal(prod.getAttribute('aria-pressed'), 'false');

    posted.length = 0;
    prod.fire('click');
    assert.deepEqual(
      posted.filter((m) => m.type === 'setProfiles'),
      [{ type: 'setProfiles', profiles: ['debug', 'prod'] }],
      'a second profile replaced the first instead of joining it',
    );

    posted.length = 0;
    debug.fire('click');
    assert.deepEqual(
      posted.filter((m) => m.type === 'setProfiles'),
      [{ type: 'setProfiles', profiles: ['prod'] }],
      'pressing a pressed profile does not switch it off',
    );
    assert.equal(debug.getAttribute('aria-pressed'), 'false');
  });

  it('shows the active set the host reports, so a reopened panel is not blank', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: ['prod'] });
    const pressed = profileButtons()
      .filter((b) => b.getAttribute('aria-pressed') === 'true')
      .map((b) => b.dataset.profile);
    assert.deepEqual(pressed, ['prod'], 'the restored active set is not on the control');
  });

  // MUTATION: `profileNotice` returns '' for a non-empty set, or the sentence
  // is written into `toolbarStatus` instead of its own element. The reader is
  // then looking at three of five services with nothing on screen saying so —
  // and in the toolbarStatus case, the first keystroke in the search field
  // erases the warning.
  it('says in words that the stack on screen is filtered, and keeps saying it', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    assert.equal(profileNote().textContent, '', 'an unfiltered stack carries a filter warning');

    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: ['debug'] });
    assert.notEqual(
      profileNote().textContent,
      '',
      'a filtered stack presents itself as the whole stack',
    );
    assert.match(profileNote().textContent, /debug/);
    assert.match(profileNote().textContent, /not drawn|filtered/i);
    // DECISIONS.md 19: the resources are NOT filtered, so a network with
    // nothing on it is a fact about the project rather than a rendering fault.
    assert.match(profileNote().textContent, /Networks, volumes, configs and secrets/);

    const search = searchField();
    search.value = 'web';
    search.fire('input');
    assert.notEqual(
      profileNote().textContent,
      '',
      'typing a search erased the filtered-stack warning',
    );
    assert.match(profileNote().textContent, /debug/);
    // The App is module-level and outlives this test. A live query left behind
    // wins over focus mode in `applyFilter`, so it would silently decide what
    // the NEXT test sees dimmed — which is how a check that cannot fail gets
    // written by accident.
    clearSearch();
  });

  // MUTATION 1: the `keepPositions` merge in `case 'graph':` deleted.
  // MUTATION 2: `capturePositions()` / the `positions` post before the toggle
  // deleted.
  //
  // Either one re-flows the canvas on every toggle: `services.api` leaves the
  // band, and every box after it slides left onto the space it left. The reader
  // toggles a profile to COMPARE two shapes of the same stack, and comparing is
  // exactly what a canvas that rearranges itself makes impossible.
  it('keeps a node that survives a toggle exactly where it was', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    const before = transformOf('services.web');
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    posted.length = 0;
    profileButtons().find((b) => b.dataset.profile === 'prod')!.fire('click');

    // The webview hands the host every current position before the node set
    // changes, so the redraw can carry them.
    const carried = posted.find((m) => m.type === 'positions') as
      | { type: 'positions'; positions: Record<string, { x: number; y: number }> }
      | undefined;
    assert.ok(carried, 'a toggle discarded the positions instead of persisting them');
    assert.ok(
      carried!.positions['services.web'],
      'the captured positions do not include the nodes on screen',
    );

    // The answer, with `api` filtered out and — the hostile case — no positions
    // echoed back at all, which is what a Memento write that has not landed yet
    // looks like.
    const filtered: StackGraph = {
      ...PROFILE_GRAPH,
      nodes: PROFILE_GRAPH.nodes.filter((n) => n.id !== 'services.api'),
    };
    send(graphMessage({ graph: filtered, positions: {}, fit: false }));
    assert.equal(nodesOf().has('services.api'), false, 'the filtered node is still drawn');
    assert.equal(
      transformOf('services.web'),
      before,
      'a surviving node moved when a profile was toggled',
    );
  });

  // MUTATION: `this.keepPositions = false` deleted from `case 'graph':`.
  //
  // The two sibling resets — `case 'empty':` and `case 'failure':` — are both
  // backed; the SUCCESS path was not, and it is the path a toggle normally
  // takes. Left standing, the flag applies the positions captured from ONE
  // node set to whatever graph arrives next, which may be another file
  // entirely. Probed: after a successful toggle, the next file drew
  // `services.web` at translate(4000 3000) instead of its laid-out
  // translate(-318 0) — a box at coordinates nothing on screen ever put it at,
  // in a file the reader had not toggled anything in. It reads as a layout bug
  // and it does not reproduce.
  it('does not carry a toggled graph’s positions into the next file drawn', () => {
    // A clean baseline: `empty` clears both the flag and the map, so the
    // laid-out transform below is the layout's own answer and not a leftover
    // from another test.
    send({ type: 'empty', file: '/w/other.yaml' });
    send(graphMessage({ file: '/w/other.yaml', graph: GRAPH, positions: {}, fit: true }));
    const laidOut = transformOf('services.web');

    send(graphMessage({ graph: PROFILE_GRAPH, positions: {}, fit: true }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    profileButtons().find((b) => b.dataset.profile === 'prod')!.fire('click');

    // The toggle's answer — the SUCCESS path — carrying a position no layout
    // would produce, so a map that survives this draw is unmistakable.
    send(
      graphMessage({
        graph: PROFILE_GRAPH,
        positions: { 'services.web': { x: 4000, y: 3000 } },
        fit: false,
      }),
    );

    // A different file, laid out from scratch and echoing no positions at all.
    send(graphMessage({ file: '/w/other.yaml', graph: GRAPH, positions: {}, fit: true }));
    assert.equal(
      transformOf('services.web'),
      laidOut,
      'a toggle in one file put the next file’s node at a position nothing laid out',
    );
  });

  // MUTATION: `drawGraph` filters the node list by the active profile set
  // before rendering. AD-16 puts the filter in `internal/topology` and nowhere
  // else, and DECISIONS.md 19 says the resources are not filtered at all — a
  // webview that "helpfully" hid the network `tools` was on would delete the
  // evidence for the unused-resource rule and disagree with the core's own
  // node count.
  it('draws every node the core sent, resources included, and drops none of its own', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: ['prod'] });
    // `tools` declares `debug`, which is NOT in the active set: the core sent
    // it, so it is drawn. Deciding otherwise here would be a second filter.
    //
    // The expectation is a LITERAL list, not `PROFILE_GRAPH.nodes.map(...)`.
    // The App holds that same array, so an in-place filtering mutant
    // (`this.baseGraph.nodes = nodes.filter(...)`) shrinks the fixture and the
    // expectation together and the check passes while the defect is present.
    // An expectation read live off the thing under test is not an expectation.
    assert.deepEqual(
      [...nodesOf().keys()].sort(),
      [
        'networks.frontnet',
        'services.api',
        'services.tools',
        'services.web',
        'volumes.data',
      ],
      'the webview dropped a node the core sent',
    );
    for (const id of ['networks.frontnet', 'volumes.data']) {
      assert.ok(nodesOf().has(id), `${id} vanished when a profile was switched on`);
    }
  });

  // MUTATION: the `graph.dangling` loop in `markerIndex` deleted. The edge from
  // `web` to `tools` simply is not there, with nothing said — the graph lying
  // by omission, which is the one failure mode a filter must not have.
  it('shows a reference the profile filter broke, rather than dropping it', () => {
    send(
      graphMessage({
        graph: {
          ...PROFILE_GRAPH,
          nodes: PROFILE_GRAPH.nodes.filter((n) => n.id !== 'services.tools'),
          dangling: [
            {
              kind: 'depends_on',
              from: 'services.web',
              to: 'services.tools',
              ref: 'tools',
              reason: 'filtered by profile',
              origin,
            },
          ],
        },
      }),
    );
    const markers = walk(nodesOf().get('services.web')!).filter((e) =>
      e.classList.contains('node-marker'),
    );
    assert.ok(markers.length > 0, 'a dangling reference left no trace on the canvas');
    assert.ok(
      markers.some((m) => m.className.includes('marker-unresolved')),
      'the marker is not marked as unresolved, so it reads as an ordinary detail',
    );
    // The marker line is clipped to the width of the box, so the whole sentence
    // lives in the node's `<title>` — which is what a tooltip and a screen
    // reader get. Both halves are asserted: the line is on the canvas, and the
    // text says which reference broke and that a profile is why.
    const title = walk(nodesOf().get('services.web')!).find((e) => e.tagName === 'title')!;
    assert.match(
      title.textContent,
      /unresolved depends_on tools/,
      'the node does not name what went missing',
    );
    assert.match(title.textContent, /filtered by profile/, 'nothing says a profile is why');
  });

  // MUTATION: `this.profileNote.hidden = sentence === ''` restored, or the note
  // appended to the tree only when there is something to say.
  //
  // A live region has to be IN THE ACCESSIBILITY TREE BEFORE the text lands.
  // Screen readers subscribe to the region when they see it; a region that
  // appears already carrying its text is routinely announced late or not at
  // all, which is the one failure mode this particular sentence cannot have —
  // it is the only thing telling a reader who cannot see the canvas that the
  // canvas is not the whole stack. So the element is present and empty, and
  // the stylesheet gives an empty one no height rather than the DOM removing
  // it.
  it('keeps the filtered-stack live region in the tree even when it says nothing', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    const note = profileNote();
    assert.ok(note, 'the live region is not in the tree at all');
    assert.notEqual(note.hidden, true, 'the live region is hidden until it has something to say');
    assert.equal(note.getAttribute('role'), 'status');
    assert.equal(note.getAttribute('aria-live'), 'polite');
    assert.equal(note.textContent, '');

    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: ['prod'] });
    assert.notEqual(profileNote().hidden, true, 'the region is revealed rather than filled');
    assert.match(profileNote().textContent, /prod/);
  });

  // THE DEFECT: a stored set naming profiles the project no longer declares.
  // The bar detaches — "absent rather than empty" — and the sentence went on
  // announcing a filtered stack, with no control anywhere to switch it off.
  // The set is inert (the core filters by profile MEMBERSHIP, and nothing
  // declares these), so the stack on screen is whole and the notice was simply
  // false, with the reader given nothing to press.
  //
  // MUTATION: compute the notice from `this.activeProfiles` alone again.
  it('claims no filter when nothing the project declares is active', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug'], active: ['debug'] });
    assert.match(profileNote().textContent, /debug/, 'the filtered case is not set up');

    // The profiles were removed from the file; the stored set survives.
    send({ type: 'profiles', file: '/w/compose.yaml', declared: [], active: ['debug'] });
    assert.equal(profileGroup(), undefined, 'a control was offered for nothing');
    assert.equal(
      profileNote().textContent,
      '',
      'the panel calls the stack filtered by a profile it offers no way to switch off',
    );

    // And a set that is only PARTLY declared names the half that can filter.
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug'], active: ['debug', 'gone'] });
    assert.match(profileNote().textContent, /the debug profile is on/, 'the live half is not named');
    assert.doesNotMatch(
      profileNote().textContent,
      /gone/,
      'the notice names a profile that filters nothing',
    );
  });

  // MUTATION: `aria-label` back to `Activate the ${name} profile` for every
  // state. A screen reader then announces "Activate the debug profile, pressed"
  // — the name says the opposite of what pressing will now do. The WAI-ARIA
  // toggle-button pattern is that the NAME is constant and `aria-pressed`
  // carries the state, so the name must not be an instruction that goes stale.
  it('names a profile toggle the same way whether or not it is pressed', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    const debug = (): El => profileButtons().find((b) => b.dataset.profile === 'debug')!;
    const unpressed = debug().getAttribute('aria-label');
    assert.ok(unpressed?.includes('debug'), 'the toggle does not name its profile');
    assert.doesNotMatch(
      unpressed!,
      /^Activate /,
      'the accessible name is an instruction that will be wrong as soon as it is pressed',
    );

    debug().fire('click');
    assert.equal(debug().getAttribute('aria-pressed'), 'true');
    assert.equal(
      debug().getAttribute('aria-label'),
      unpressed,
      'the accessible name changed under a reader who only changed the state',
    );
    debug().fire('click'); // leave it as it was found
  });

  // THE DEFECT: `keepPositions` is set when a toggle is sent and cleared only
  // in `case 'graph':`. A toggle whose redraw FAILS — the core died, the file
  // stopped parsing — therefore leaves the flag standing, and the next graph
  // to arrive keeps positions captured from a different node set, possibly for
  // a different file entirely. The symptom is boxes at coordinates nothing on
  // screen ever put them at, which reads as a layout bug and is unreproducible.
  //
  // MUTATION: remove the `keepPositions = false` from the failure and empty
  // cases. `services.web` then comes back at the coordinates it was dragged to
  // in the previous file instead of where this graph lays it out.
  it('does not carry positions across a toggle whose redraw failed', () => {
    // A clean draw, so the layout's own answer for this graph is known.
    send(graphMessage({ graph: PROFILE_GRAPH, positions: {} }));
    const fresh = transformOf('services.web');
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });

    // The reader drags it somewhere else.
    send(graphMessage({ graph: PROFILE_GRAPH, positions: { 'services.web': { x: 4000, y: 3000 } } }));
    const moved = transformOf('services.web');
    assert.notEqual(moved, fresh, 'the position was not actually moved, so nothing is being tested');

    // A toggle — which arms `keepPositions` — and then a redraw that fails.
    profileButtons().find((b) => b.dataset.profile === 'prod')!.fire('click');
    send({
      type: 'failure',
      failure: { kind: 'core-crashed', title: 'the core exited', detail: 'signal: killed' },
    } as HostMessage);

    // The next graph is a different file with no stored positions at all.
    send(
      graphMessage({
        file: '/w/other/compose.yaml',
        graph: PROFILE_GRAPH,
        positions: {},
        fit: true,
      }),
    );
    assert.equal(
      transformOf('services.web'),
      fresh,
      'a failed toggle carried its positions into the next file’s graph',
    );
  });

  // The webview half of the same defect the host test covers: a selection the
  // active set filtered out. The pane must not present it as a fault ("could
  // not be inspected" sends the reader looking for a broken file) and must not
  // present the STACK's fields under the service's name, which is what the
  // `??` fallbacks in `inspect()` used to produce.
  //
  // MUTATION: drop the `reason === 'filtered'` arm. The pane then says
  // "services.api could not be inspected" about a service that reads fine.
  it('says a filtered-out selection is filtered, not that it could not be read', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({
      type: 'inspectionFailed',
      file: '/w/compose.yaml',
      id: 'services.api',
      reason: 'filtered',
      detail: 'It is not in the stack drawn for the active profile set (prod).',
    });
    const text = walk(root())
      .filter((e) => e.classList.contains('inspector-message'))
      .map((e) => e.textContent)
      .join(' ');
    assert.match(text, /services\.api is not in the active profile set/);
    assert.doesNotMatch(
      text,
      /could not be inspected/,
      'a filtered service is reported as a failure to read it',
    );

    // And an ordinary failure still reads as one.
    send({
      type: 'inspectionFailed',
      file: '/w/compose.yaml',
      id: 'services.web',
      detail: 'core exited',
    });
    assert.match(
      walk(root())
        .filter((e) => e.classList.contains('inspector-message'))
        .map((e) => e.textContent)
        .join(' '),
      /services\.web could not be inspected/,
    );
  });

  /* ---- the blast radius across a toggle ------------------------------- */

  const focusButton = (): El => walk(root()).find((e) => e.textContent === 'Focus')!;
  const dimmed = (): string[] =>
    [...nodesOf()]
      .filter(([, e]) => e.classList.contains('is-dimmed'))
      .map(([id]) => id)
      .sort();

  /** Focus mode, on, for `id`, with the core's answer in. */
  const focusOn = (id: string, dependents: string[]): void => {
    // A live query beats focus mode in `applyFilter`, so it would decide the
    // dimming these tests are about. The App is module-level and outlives each
    // test, so both leftovers are cleared rather than assumed: focus left ON by
    // an earlier test would make the click below switch it OFF, and the test
    // would then assert against a state it never reached.
    clearSearch();
    if (focusButton().getAttribute('aria-pressed') === 'true') {
      focusButton().fire('click');
    }
    send({ type: 'selection', file: '/w/compose.yaml', id } as HostMessage);
    focusButton().fire('click');
    assert.equal(
      focusButton().getAttribute('aria-pressed'),
      'true',
      'focus mode is not on, so nothing below is testing focus mode',
    );
    send({ type: 'impact', id, dependents, dependencies: [] } as HostMessage);
  };

  // THE DEFECT: `case 'graph':` never touched `focusId`, `focusRadius` or
  // `focusMessage`, and a profile toggle re-requested nothing. So a toggle
  // redrew the canvas while `applyFilter()` went on dimming by an id set
  // computed under the PREVIOUS profile set, with the toolbar still reading
  // "web affects 1 service" from a graph that no longer exists.
  //
  // AC 3 — "the picture, the problems and the blast radius are never computed
  // for three different profile sets" — was met for the REQUEST and broken for
  // what is on screen, which is the only place the reader can see it.
  //
  // MUTATION: delete the focus reset in `case 'graph':`. The re-request
  // disappears and the stale dimming comes back; both assertions fail.
  it('does not go on dimming by a blast radius computed for the previous profile set', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    focusOn('services.web', ['services.api']);
    assert.deepEqual(
      dimmed(),
      ['networks.frontnet', 'services.tools', 'volumes.data'],
      'focus mode dimmed nothing, so this test cannot see it going stale',
    );

    profileButtons().find((b) => b.dataset.profile === 'prod')!.fire('click');
    posted.length = 0;
    // The answer: `api` is gone under the new set, and `tools` has arrived.
    send(
      graphMessage({
        graph: {
          ...PROFILE_GRAPH,
          nodes: PROFILE_GRAPH.nodes.filter((n) => n.id !== 'services.api'),
        },
        positions: {},
        fit: false,
      }),
    );

    // Nothing is dimmed on the strength of the old answer. A blast radius shown
    // before it has been computed is the same wrong answer focus mode already
    // refuses to give at switch-on time.
    assert.deepEqual(
      dimmed(),
      [],
      'the new graph is dimmed by a blast radius computed for the profile set before the toggle',
    );
    // And the toolbar no longer reports the COUNT it measured under the old
    // set. `focusStatus` is the only thing that says "depends on"; what stands
    // in its place is the same pending sentence switch-on shows, because the
    // honest answer until the core replies is that we are asking.
    assert.doesNotMatch(
      toolbarStatus().textContent,
      /depends? on/,
      'the toolbar still reports the blast radius it measured under the old profile set',
    );
    assert.match(
      toolbarStatus().textContent,
      /Asking the core/,
      'nothing says the blast radius is being recomputed',
    );

    // And it is re-asked, because the node is still there: focus mode stays on
    // rather than silently switching itself off under the reader.
    assert.deepEqual(
      posted.filter((m) => m.type === 'impact'),
      [{ type: 'impact', id: 'services.web' }],
      'the blast radius was never recomputed for the set now on screen',
    );
    assert.equal(focusButton().getAttribute('aria-pressed'), 'true');

    send({
      type: 'impact',
      file: '/w/compose.yaml',
      id: 'services.web',
      dependents: [],
      dependencies: [],
    } as HostMessage);
    assert.deepEqual(
      dimmed(),
      ['networks.frontnet', 'services.tools', 'volumes.data'],
      'the recomputed radius was not applied',
    );
  });

  // The other half: the focused node is itself filtered out. There is nothing
  // to re-ask about, so focus mode goes off and SAYS it went off — a control
  // that quietly un-presses itself is a control the reader stops trusting.
  it('switches focus off in words when the focused node is filtered out', () => {
    send(graphMessage({ graph: PROFILE_GRAPH }));
    send({ type: 'profiles', file: '/w/compose.yaml', declared: ['debug', 'prod'], active: [] });
    focusOn('services.api', ['services.web']);

    profileButtons().find((b) => b.dataset.profile === 'prod')!.fire('click');
    posted.length = 0;
    send(
      graphMessage({
        graph: {
          ...PROFILE_GRAPH,
          nodes: PROFILE_GRAPH.nodes.filter((n) => n.id !== 'services.api'),
        },
        positions: {},
        fit: false,
      }),
    );

    assert.deepEqual(dimmed(), [], 'a node that is gone is still dimming the canvas');
    assert.deepEqual(
      posted.filter((m) => m.type === 'impact'),
      [],
      'the blast radius of a node that is not in the stack was asked for',
    );
    assert.equal(
      focusButton().getAttribute('aria-pressed'),
      'false',
      'focus mode is still pressed for a node that is no longer drawn',
    );
    assert.match(
      toolbarStatus().textContent,
      /no longer in the stack|not in the stack/i,
      'focus mode switched itself off without saying so',
    );
  });
});

/* -------------------------------------------------------------------------
 * Auto-arrange: the way back from a dragged layout.
 *
 * Story 4.2's last criterion makes a dragged position view state that persists
 * per workspace. It had no inverse, so one drag left the layout partly manual
 * for the life of the workspace and nothing on screen could re-run it — the
 * owner's words, 2026-08-13: "the layout view does not have auto arrange".
 * ---------------------------------------------------------------------- */

describe('auto-arrange — putting the layout back', () => {
  const arrangeButton = (): El =>
    walk(root()).find((e) => e.dataset.control === 'arrange')!;
  /** A graph drawn with the host's stored positions for one node. */
  const withStored = (): void => {
    send(graphMessage({ positions: { 'services.web': { x: 900, y: 640 } } }));
  };

  // MUTATION: `dataset.control` dropped, or the button appended anywhere but
  // the toolbar. It is the same contract every control in that row holds, and
  // the profile toggles have the identical assertion for the identical reason:
  // a `tabindex="-1"` added later to stop the toolbar eating tab stops takes
  // the whole capability off the keyboard in silence.
  it('sits in the toolbar as a real, named, tab-reachable button', () => {
    send(graphMessage());
    const button = arrangeButton();
    assert.equal(button.tagName, 'BUTTON', 'auto-arrange is not a real control');
    assert.equal(button.type, 'button');
    assert.equal(
      button.parent?.className,
      'toolbar',
      'auto-arrange is not with the other graph controls',
    );
    assert.match(
      button.getAttribute('aria-label') ?? '',
      /arrange/i,
      'auto-arrange has no accessible name saying what it does',
    );
    assert.equal(button.getAttribute('tabindex'), null, 'auto-arrange was taken out of the tab order');
    // Never `disabled`, in either state: a disabled button is not focusable, so
    // a reader tabbing the toolbar of a stack nobody has dragged would never
    // meet the control and would never learn it exists.
    assert.equal(
      button.getAttribute('disabled'),
      null,
      'auto-arrange is unreachable by keyboard when idle',
    );
  });

  // MUTATION: `this.basePositions = {}` deleted from `autoArrange`, leaving
  // only the post to the host. The stored positions are cleared on the far side
  // and every box stays exactly where it was, so the button looks broken until
  // the panel is reopened — and the reader has by then lost the arrangement
  // they were trying to keep.
  //
  // Asserted on the DRAWN TRANSFORM, not on a message: the whole point of the
  // control is that the picture moves.
  it('puts a dragged node back where the layout computes it', () => {
    send(graphMessage({ positions: {} }));
    const computed = transformOf('services.web');
    withStored();
    assert.equal(
      transformOf('services.web'),
      'translate(900 640)',
      'the stored position was never applied, so nothing here proves it was discarded',
    );
    arrangeButton().fire('click');
    assert.equal(
      transformOf('services.web'),
      computed,
      'the node did not return to the computed arrangement',
    );
  });

  // MUTATION: the `send` deleted. The canvas re-flows and the stored positions
  // survive in workspace state, so the next redraw — a save, a file change,
  // reopening the panel — puts every box straight back.
  it('discards the stored positions on the host side, and writes nothing to the file', () => {
    withStored();
    posted.length = 0;
    arrangeButton().fire('click');
    const positions = posted.filter((m) => m.type === 'positions');
    assert.equal(positions.length, 1, 'the host was not told to discard the stored positions');
    assert.deepEqual(
      (positions[0] as { positions: Record<string, unknown> }).positions,
      {},
      'auto-arrange sent positions instead of clearing them',
    );
    // Position is view state (R2.6, AD-19). `positions` is the message
    // `panelbehaviour.test.ts` already pins to the non-writing path; nothing
    // here may reach an edit, a stage or a save.
    assert.deepEqual(
      posted.filter((m) => ['edit', 'stage', 'save', 'add'].includes(m.type)),
      [],
      'auto-arrange reached the write path',
    );
  });

  // MUTATION: `arrangeStatus` returns '' — or `applyFilter` is never called
  // after it. A keyboard reader gets a control that reports nothing, over a
  // canvas they cannot see, having just destroyed an arrangement.
  it('says how much was discarded, because discarding is destructive', () => {
    withStored();
    arrangeButton().fire('click');
    assert.match(
      toolbarStatus().textContent,
      /discarded/i,
      'auto-arrange threw the reader’s arrangement away in silence',
    );
    assert.match(
      toolbarStatus().textContent,
      /1 stored position\b/,
      'the count is not what was discarded',
    );
  });

  // MUTATION: the `discarded === 0` branch deleted. The button posts an empty
  // map on every press — a workspace-state write per press for no change — and
  // says it re-arranged something when nothing had been moved.
  it('says so, and posts nothing, when there is nothing to re-arrange', () => {
    send(graphMessage({ positions: {} }));
    posted.length = 0;
    const button = arrangeButton();
    assert.equal(
      button.getAttribute('aria-disabled'),
      'true',
      'a control with nothing to do reports itself as available',
    );
    button.fire('click');
    assert.deepEqual(
      posted.filter((m) => m.type === 'positions'),
      [],
      'an empty position map was written for a press that changed nothing',
    );
    assert.match(
      toolbarStatus().textContent,
      /nothing to re-arrange/i,
      'the press did nothing and said nothing',
    );
  });

  // MUTATION: `updateArrangeState` never called from `drawGraph`/`onMove`. The
  // control reports itself unavailable over a layout the reader has dragged,
  // which is precisely when it is the thing they need.
  it('becomes available exactly when there is something stored', () => {
    send(graphMessage({ positions: {} }));
    assert.equal(arrangeButton().getAttribute('aria-disabled'), 'true');
    withStored();
    assert.equal(
      arrangeButton().getAttribute('aria-disabled'),
      'false',
      'a stack drawn from stored positions offers no way back',
    );
    arrangeButton().fire('click');
    assert.equal(
      arrangeButton().getAttribute('aria-disabled'),
      'true',
      'the control still claims there is something to discard',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 4.3: the cursor moved in the YAML.
 * ---------------------------------------------------------------------- */

describe('the graph follows the cursor — story 4.3', () => {
  // MUTATION: `case 'selection':` kept and its body emptied. Every check that
  // existed was `main.includes("case 'selection':")`, so the whole second
  // direction of the headline feature could be deleted in place. What the
  // reader gets: a graph that never moves as they scroll the file.
  it('moves the graph selection to the node the host named', () => {
    send(graphMessage({ selected: null }));
    send({ type: 'selection', id: 'services.api' });
    assert.equal(
      nodesOf().get('services.api')!.getAttribute('aria-selected'),
      'true',
      'the cursor moved and the graph did not follow',
    );
    assert.match(strip().textContent, /api/);
  });

  it('clears the selection when the cursor leaves every node', () => {
    send(graphMessage({ selected: 'services.api' }));
    send({ type: 'selection', id: null });
    for (const el of nodesOf().values()) {
      assert.equal(el.getAttribute('aria-selected'), 'false');
    }
    assert.equal(strip().textContent, 'No node selected');
  });

  // FINDING, recorded rather than repaired. main.ts:322 says of this case
  // "Nothing is posted back — the host already knows, and an echo would be two
  // views chasing each other." It is posted back: `followSelection` calls
  // `graph.selectById`, whose `select()` fires `onSelect`, which debounces a
  // `select` message. The loop terminates because the host's
  // `selectFromCursor` drops an id equal to the selection it already holds, so
  // the cost is one redundant workspace write and one redundant `stack/schema`
  // per cursor move, not a ping-pong. This asserts the property that actually
  // holds — at most one echo, carrying the same id — so the day the host's
  // guard is removed, this is what says so.
  it('echoes the selection at most once, so the two views cannot chase each other', () => {
    send(graphMessage());
    posted.length = 0;
    send({ type: 'selection', id: 'services.api' });
    d.clock.flush();
    const echoes = posted.filter((m) => m.type === 'select');
    assert.ok(echoes.length <= 1, `following the cursor posted ${echoes.length} selections back`);
    for (const e of echoes) {
      assert.deepEqual(
        e,
        { type: 'select', id: 'services.api' },
        'the echo names a different node than the cursor did, which is a loop',
      );
    }
  });
});

/* -------------------------------------------------------------------------
 * Story 4.2: the 600px reflow.
 * ---------------------------------------------------------------------- */

describe('a narrow panel gives up the canvas — story 4.2', () => {
  // MUTATION: NARROW_PX 600 → 0. The reflow never engages, so a 400px panel
  // shows a canvas nobody can read and hides the list that exists to replace
  // it. Nothing measured the threshold; the constant was simply a number.
  it('replaces the canvas with a list and a strip below 600px', () => {
    send(graphMessage());
    d.document.body.clientWidth = 400;
    d.resize();
    d.clock.flush();
    assert.equal(root().classList.contains('is-narrow'), true, 'the 600px reflow never engaged');
    assert.equal(strip().hidden, false, 'a narrow panel shows no strip naming the selection');
    const list = walk(root()).find((e) => e.classList.contains('narrow-list'))!;
    assert.equal(list.hidden, false, 'a narrow panel offers no list, so nothing is reachable');
    const divider = walk(root()).find((e) => e.classList.contains('divider'))!;
    assert.equal(divider.hidden, true, 'a divider is offered with nothing left to drag');
  });

  it('keeps the canvas above it', () => {
    send(graphMessage());
    d.document.body.clientWidth = 900;
    d.resize();
    d.clock.flush();
    assert.equal(root().classList.contains('is-narrow'), false);
    assert.equal(walk(root()).find((e) => e.classList.contains('narrow-list'))!.hidden, true);
  });

  it('selects from the list the same way the canvas does', () => {
    send(graphMessage());
    d.document.body.clientWidth = 400;
    d.resize();
    posted.length = 0;
    rows().find((r) => r.dataset.id === 'services.api')!.fire('click');
    d.clock.flush();
    assert.deepEqual(
      posted.filter((m) => m.type === 'select'),
      [{ type: 'select', id: 'services.api' }],
    );
    assert.equal(
      nodesOf().get('services.api')!.getAttribute('aria-selected'),
      'true',
      'widening the panel would reveal a graph that disagrees with the row pressed',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 6.3: reaching the Dockerfile from the service.
 * ---------------------------------------------------------------------- */

describe('opening a Dockerfile from the canvas — story 6.3', () => {
  // MUTATION: clicking a Dockerfile node sends nothing. The node is drawn, it
  // selects, it is keyboard reachable — and the gesture the whole story exists
  // for does nothing at all.
  it('posts openDockerfile when the node is clicked', () => {
    send(graphMessage());
    posted.length = 0;
    const build = nodesOf().get('services.web.build')!;
    build.fire('pointerdown', { button: 0, pointerId: 1, clientX: 10, clientY: 10 });
    graphEl().fire('pointerup', { pointerId: 1, clientX: 10, clientY: 10 });
    d.clock.flush();
    assert.deepEqual(
      posted.filter((m) => m.type === 'openDockerfile'),
      [{ type: 'openDockerfile', id: 'services.web.build' }],
      'clicking the Dockerfile node opened nothing',
    );
  });

  it('does not open it when the press was a drag', () => {
    send(graphMessage());
    posted.length = 0;
    const build = nodesOf().get('services.web.build')!;
    build.fire('pointerdown', { button: 0, pointerId: 2, clientX: 10, clientY: 10 });
    graphEl().fire('pointermove', { pointerId: 2, clientX: 90, clientY: 90 });
    graphEl().fire('pointerup', { pointerId: 2, clientX: 90, clientY: 90 });
    d.clock.flush();
    assert.deepEqual(posted.filter((m) => m.type === 'openDockerfile'), []);
    assert.ok(posted.some((m) => m.type === 'positions'), 'a drag did not persist the position');
  });

  it('opens the same thing from the keyboard', () => {
    send(graphMessage({ selected: 'services.web.build' }));
    posted.length = 0;
    graphEl().fire('keydown', { key: 'Enter' });
    d.clock.flush();
    assert.deepEqual(
      posted.filter((m) => m.type === 'openDockerfile'),
      [{ type: 'openDockerfile', id: 'services.web.build' }],
      'the canvas is reachable by keyboard and not usable by one',
    );
  });
});

/* -------------------------------------------------------------------------
 * The Dockerfile view, and the way back.
 * ---------------------------------------------------------------------- */

function form(over: Partial<DockerfileForm> = {}): DockerfileForm {
  return {
    path: '/w/Dockerfile',
    escape_char: '\\',
    crlf: false,
    bom: false,
    missing: false,
    context: '',
    dockerfile: '',
    directives: [],
    preamble: [],
    stages: [
      {
        index: 0,
        name: 'builder',
        image_ref: 'golang:1.23',
        platform: '',
        from: { start_line: 1, end_line: 1 },
        instructions: [],
      },
      {
        index: 1,
        name: '',
        image_ref: 'alpine:3.20',
        platform: '',
        from: { start_line: 8, end_line: 8 },
        instructions: [],
      },
    ],
    ...over,
  } as DockerfileForm;
}

describe('the Dockerfile view replaces the stack view', () => {
  it('swaps the panes, and swaps them back', () => {
    send(graphMessage());
    const split = walk(root()).find((e) => e.classList.contains('split'))!;
    const stageForm = walk(root()).find((e) => e.classList.contains('stageform'))!;
    assert.equal(split.hidden, false);
    assert.equal(stageForm.hidden, true);

    send({ type: 'dockerfile', file: '/w/Dockerfile', form: form(), from: '/w/compose.yaml', staged: [] });
    assert.equal(split.hidden, true, 'the stack view is still up behind the Dockerfile');
    assert.equal(stageForm.hidden, false, 'the Dockerfile view never appeared');

    send(graphMessage());
    assert.equal(split.hidden, false, 'a graph message did not restore the stack view');
    assert.equal(stageForm.hidden, true);
  });

  // MUTATION: `backToStack` never posted. The reader who opened a Dockerfile
  // from the canvas is stuck on it.
  it('offers a way back only when there is one, and posts it', () => {
    send({ type: 'dockerfile', file: '/w/Dockerfile', form: form(), from: '/w/compose.yaml', staged: [] });
    const back = walk(root()).find((e) => e.textContent === 'Back to the stack')!;
    assert.equal(back.hidden, false);
    posted.length = 0;
    back.fire('click');
    assert.deepEqual(posted, [{ type: 'backToStack' }]);

    send({ type: 'dockerfile', file: '/w/Dockerfile', form: form(), from: null, staged: [] });
    const alone = walk(root()).find((e) => e.textContent === 'Back to the stack')!;
    assert.equal(alone.hidden, true, 'a button that goes nowhere is offered');
  });

  // MUTATION: the `extractArg` arm of the webview's switch kept and its body
  // emptied. The reader presses `${}`, the host answers, and the block waits
  // forever on a sentence that never arrives — a feature that is wired at both
  // ends and connected in the middle by nothing. The source scan over
  // `HostMessage` variants cannot see this: the case is still there.
  it('routes what the host says about a build-argument move to the Dockerfile view — story 9.4', () => {
    const twoStage = form({
      stages: [
        {
          index: 0,
          name: 'builder',
          image_ref: 'golang:1.23',
          platform: '',
          from: { index: 0, start_line: 1, end_line: 1 },
          instructions: [],
        },
        {
          index: 1,
          name: '',
          image_ref: 'node:18',
          platform: '',
          // The SECOND stage's FROM, whose ARG must be declared above the
          // FIRST one — the case a single-stage fixture cannot express.
          from: { index: 4, start_line: 8, end_line: 8 },
          instructions: [],
        },
      ],
    } as any);
    send({ type: 'dockerfile', file: '/w/Dockerfile', form: twoStage, from: null, staged: [] });
    const control = walk(root()).find(
      (e) => e.classList.contains('extract-open') && e.dataset.instruction === '4',
    );
    assert.ok(control, 'the second stage’s FROM offers no way to move its tag into an ARG');
    posted.length = 0;
    control!.fire('click');
    assert.deepEqual(posted, [{ type: 'openExtractArg', instruction: 4 }]);

    send({
      type: 'extractArg',
      file: '/w/Dockerfile',
      instruction: 4,
      staged: false,
      result: {
        name: 'NODE_VERSION',
        value: '18',
        dockerfile: {
          file: '/w/Dockerfile',
          ops: [],
          diff: '@@ -1,2 +1,3 @@\n+ARG NODE_VERSION=18\n-FROM node:18\n+FROM node:${NODE_VERSION}\n',
          added: 2,
          removed: 1,
          changed_lines: 3,
          written: false,
        },
        scope: 'global',
        scope_reason: 'a FROM can only use an ARG declared before the FIRST FROM',
        arg_line: 'ARG NODE_VERSION=18',
        declared: true,
        redeclared: false,
        already_declared: false,
        compose_note: 'Nothing feeds `NODE_VERSION` from compose.',
        written: false,
      },
    } as any);
    const block = walk(root()).find((e) => e.classList.contains('extract-block'));
    assert.ok(block, 'the host’s answer never reached the Dockerfile view');
    assert.match(block!.textContent, /ARG NODE_VERSION=18/);
    assert.match(block!.textContent, /a FROM can only use an ARG declared before the FIRST FROM/);
    assert.match(block!.textContent, /Nothing feeds `NODE_VERSION` from compose/);
  });

  it('says a build names a file that is not there, rather than drawing an empty form', () => {
    send({
      type: 'dockerfile',
      file: '/w/Dockerfile',
      form: form({ missing: true, context: './svc', dockerfile: 'Dockerfile.prod', stages: [] }),
      from: '/w/compose.yaml',
      staged: [],
    });
    const text = walk(root())
      .filter((e) => e.classList.contains('inspector-message') || e.classList.contains('inspector-note'))
      .map((e) => e.textContent)
      .join(' ');
    assert.match(text, /is not there/);
    assert.match(text, /context \.\/svc/);
    assert.match(text, /Dockerfile\.prod/);
  });
});

/* -------------------------------------------------------------------------
 * Story 6.1: the pending strip and a refused edit.
 * ---------------------------------------------------------------------- */

describe('the pending strip is docked with the surface it reports on', () => {
  it('renders the core’s diff line by line, minus the file-name header', () => {
    // MUTATION: `renderDiff` renders nothing. The strip then says "1 edit · 1
    // line removed, 1 added" over an empty box: a claim about a change with no
    // evidence, which is the one thing a write gesture may not do.
    //
    // The `--- a` / `+++ b` pair is NOT rendered: the file is named in words on
    // the Save button two rows up, and measured in the shipped bundle those two
    // lines were two of the three the box had room for, with the `−`/`+` pair
    // below the fold. The `@@` header stays — it is the only line that says
    // where in the file the change lands.
    send(graphMessage());
    send({
      type: 'pending',
      file: '/w/compose.yaml',
      count: 1,
      diff: '--- a\n+++ b\n@@ -3,1 +3,1 @@\n-    image: nginx:1.27\n+    image: nginx:1.28\n',
      removed: 1,
      added: 1,
      saveLabel: 'Save to compose.yaml',
    });
    const lines = walk(root()).filter((e) => e.classList.contains('diff-line'));
    assert.equal(lines.length, 3, `the strip rendered ${lines.length} diff lines`);
    assert.deepEqual(
      lines.map((l) => l.className.replace('diff-line ', '')),
      ['diff-hunk', 'diff-del', 'diff-add'],
      'the +++/--- headers are coloured as an addition and a removal',
    );
    // The change itself is the FIRST thing after the hunk header, which is the
    // property the whole fix is about.
    assert.equal(lines[1].textContent, '-    image: nginx:1.27');
    assert.ok(lines.some((l) => l.textContent === '+    image: nginx:1.28'));
    const summary = walk(root()).find((e) => e.classList.contains('pending-summary'))!;
    assert.equal(summary.textContent, '1 edit · 1 line removed, 1 added');
  });

  // MUTATION: the Save button's click listener deleted — one line from the
  // write gesture doing nothing. `pending.test.ts` covered the LABEL.
  it('posts save when Save is pressed, and discard when Discard is', () => {
    send(graphMessage());
    send({
      type: 'pending',
      file: '/w/compose.yaml',
      count: 1,
      diff: '-a\n+b\n',
      removed: 1,
      added: 1,
      saveLabel: 'Save to compose.yaml',
    });
    const buttons = walk(root()).filter((e) => e.className.includes('button') && !e.hidden);
    const save = buttons.find((b) => b.textContent.startsWith('Save to'))!;
    const discard = buttons.find((b) => b.textContent === 'Discard')!;
    posted.length = 0;
    save.fire('click');
    assert.deepEqual(posted, [{ type: 'save' }], 'pressing Save wrote nothing');
    posted.length = 0;
    discard.fire('click');
    assert.deepEqual(posted, [{ type: 'discard' }]);
  });

  it('says why a stage vanished rather than dropping it silently', () => {
    send(graphMessage());
    send({ type: 'pendingCleared', file: '/w/compose.yaml', reason: 'the file moved under the edit' });
    const note = walk(root()).find((e) => e.classList.contains('pending-note'))!;
    assert.equal(note.hidden, false);
    assert.match(note.textContent, /the file moved under the edit/);
  });

  // MUTATION: `case 'editRefused':` emptied. The edit was refused, nothing was
  // written, and the reader is told nothing at all — the field simply snaps back
  // and the tool looks broken rather than careful.
  it('names a refused edit, and offers no Retry for it', () => {
    send(graphMessage());
    send({
      type: 'editRefused',
      file: '/w/compose.yaml',
      path: 'services.web.image',
      title: 'That edit was not made',
      detail: 'the mapping is written in flow style',
    });
    const banner = walk(root()).find((e) => e.classList.contains('banner'))!;
    assert.equal(banner.hidden, false, 'a refused edit said nothing');
    assert.match(banner.textContent, /That edit was not made/);
    assert.match(banner.textContent, /flow style/);
    assert.equal(banner.dataset.kind, 'refused');
    const retry = walk(banner).find((e) => e.textContent === 'Retry')!;
    assert.equal(retry.hidden, true, 'a refusal offers to respawn a healthy core');
  });
});

/* -------------------------------------------------------------------------
 * Story 7.3: the empty stack is a starting point.
 * ---------------------------------------------------------------------- */

describe('a stack with nothing in it offers the one action that starts it', () => {
  // EXPERIENCE.md's empty-stack row, and MOCKUP-TRACEABILITY.md's first
  // spine-vs-code disagreement, resolved in the spine's favour: the code used
  // to state plainly that there were no services and stop there, because
  // authoring was a later phase.
  //
  // MUTATION: the button left in place with its click handler removed. The
  // sentence is still right, the control is still on screen, and the reader
  // presses it and nothing opens.
  it('says what is missing AND offers Add service', () => {
    send({ type: 'empty', file: '/w/compose.yaml' });
    const empty = walk(root()).find((e) => e.classList.contains('empty'))!;
    assert.equal(empty.hidden, false);
    assert.match(empty.textContent, /No services are declared in .*compose\.yaml/);

    const action = walk(root()).find((e) => e.dataset.control === 'empty-add-service');
    assert.ok(action, 'the empty state offers nothing to do about it');
    assert.equal(action!.textContent, 'Add service');

    // Pressing it opens the composer, on `service`, with the cursor in the name
    // field — the state table's "one action", actually connected.
    action!.fire('click', { preventDefault: () => {} });
    const composer = walk(root()).find((e) => e.classList.contains('add-composer'))!;
    assert.equal(composer.hidden, false, 'the one action on an empty stack opens nothing');
    const name = walk(root()).find((e) => e.dataset.field === 'add:name')!;
    assert.equal(d.document.activeElement, name, 'the reader has to go and find the field');
  });

  // The declaration travels as one message with what the reader typed in it.
  it('sends the add the reader typed, from the empty state', () => {
    send({ type: 'empty', file: '/w/compose.yaml' });
    posted.length = 0;
    walk(root())
      .find((e) => e.dataset.control === 'empty-add-service')!
      .fire('click', { preventDefault: () => {} });
    const name = walk(root()).find((e) => e.dataset.field === 'add:name')!;
    const image = walk(root()).find((e) => e.dataset.field === 'add:image')!;
    name.value = 'web';
    image.value = 'nginx:1.27';
    image.fire('keydown', { key: 'Enter', preventDefault: () => {} });
    assert.deepEqual(posted, [{ type: 'add', kind: 'service', name: 'web', value: 'nginx:1.27' }]);
  });

  // And the control is not only for the empty case: a stack with services in it
  // can be added to as well, which is the ordinary case of story 7.3.
  it('offers the same control over a drawn stack', () => {
    send(graphMessage());
    const toggle = walk(root()).find((e) => e.dataset.control === 'add');
    assert.ok(toggle, 'a stack that already has services cannot be added to');
    assert.equal(toggle!.hidden, false);
  });
});
