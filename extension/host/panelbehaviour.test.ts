// The panel, driven — stories 4.3, 5.4, 6.1 and 6.3.
//
// `host/panel.test.ts` states honestly that panel.ts imports `vscode`, which
// does not exist under `node --test`, and so reads the SOURCE rather than
// driving the class. That was true and it is no longer a limit: `vscode` is an
// ordinary CommonJS require, and a stub installed before the module is loaded
// makes the whole class drivable. The source scans it leaves behind are the ones
// that genuinely express a source property; everything a NEUTRALISING edit could
// walk past is here instead.
//
// The mutations that survived panel.test.ts and are caught here:
//
//   * `case 'reveal':` kept and its body emptied
//   * `onSourceChanged` neutralised by an early `return` placed BEFORE the line
//     the scan greps for
//   * watchers created for the entry file only
//   * the graph message posted with `selected: null`
//   * `missing: []` posted to the graph
//   * `Problems.publish` body replaced with `return`
//   * the problems-panel check being a PREFIX regex — `publishDISABLED?.()`
//   * `ViewStateStore.setPositions` made a no-op
//   * `showDockerfile` dropping the build context (`at: ''`)
//   * a `backToStack` round trip forgetting the prior selection

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { isGroupId } from '../shared/protocol';
import { positionCount } from '../shared/join';
import type { Finding, HostMessage, WebviewMessage } from '../shared/protocol';

/* -------------------------------------------------------------------------
 * A `vscode` small enough to run under `node --test`.
 * ---------------------------------------------------------------------- */

interface Recorded {
  posted: HostMessage[];
  watched: string[];
  revealed: { file: string; line: number; column: number }[];
  diagnostics: Map<string, any[]>;
  memento: Map<string, unknown>;
  requests: { method: string; params: any; timeout?: number }[];
  warnings: string[];
  disposed: boolean;
}

const rec: Recorded = {
  posted: [],
  watched: [],
  revealed: [],
  diagnostics: new Map(),
  memento: new Map(),
  requests: [],
  warnings: [],
  disposed: false,
};

/** The listeners the panel registered, so a test can be the editor. */
const hooks: {
  message?: (msg: WebviewMessage) => void;
  cursor?: (e: any) => void;
  save?: (doc: any) => void;
  watcher: { file: string; change: () => void }[];
} = { watcher: [] };

class Position {
  constructor(readonly line: number, readonly character: number) {}
}
class Range {
  start: Position;
  end: Position;
  constructor(a: any, b: any, c?: number, d?: number) {
    if (a instanceof Position) {
      this.start = a;
      this.end = b as Position;
    } else {
      this.start = new Position(a, b);
      this.end = new Position(c!, d!);
    }
  }
}
class Selection extends Range {}
class Location {
  constructor(readonly uri: any, readonly position: Position) {}
}
class Diagnostic {
  source = '';
  code = '';
  relatedInformation: any[] = [];
  constructor(readonly range: Range, readonly message: string, readonly severity: number) {}
}
class DiagnosticRelatedInformation {
  constructor(readonly location: Location, readonly message: string) {}
}

const vscodeStub = {
  ViewColumn: { One: 1, Beside: 2 },
  TextEditorRevealType: { InCenterIfOutsideViewport: 2 },
  DiagnosticSeverity: { Error: 0, Warning: 1, Information: 2, Hint: 3 },
  Position,
  Range,
  Selection,
  Location,
  Diagnostic,
  DiagnosticRelatedInformation,
  Uri: {
    file: (p: string) => ({ fsPath: p, scheme: 'file', toString: () => p }),
    joinPath: (base: any, ...parts: string[]) => ({
      fsPath: [base?.fsPath ?? '', ...parts].join('/'),
      toString: () => parts.join('/'),
    }),
  },
  window: {
    createWebviewPanel: () => ({
      webview: {
        html: '',
        cspSource: 'vscode-resource:',
        asWebviewUri: (u: any) => u,
        postMessage: (msg: HostMessage) => {
          rec.posted.push(msg);
          return Promise.resolve(true);
        },
        onDidReceiveMessage: (fn: (msg: WebviewMessage) => void) => {
          hooks.message = fn;
          return { dispose() {} };
        },
      },
      onDidDispose: () => ({ dispose() {} }),
      reveal: () => {},
      dispose: () => {
        rec.disposed = true;
      },
    }),
    onDidChangeTextEditorSelection: (fn: (e: any) => void) => {
      hooks.cursor = fn;
      return { dispose() {} };
    },
    showTextDocument: (_doc: any, _opts: any): Promise<any> =>
      Promise.resolve({
        selection: undefined as any,
        revealRange: () => {},
      }),
    showWarningMessage: (m: string) => {
      rec.warnings.push(m);
      return Promise.resolve(undefined);
    },
  },
  workspace: {
    onDidSaveTextDocument: (fn: (doc: any) => void) => {
      hooks.save = fn;
      return { dispose() {} };
    },
    createFileSystemWatcher: (glob: string) => {
      rec.watched.push(glob);
      const entry = { file: glob, change: () => {} };
      hooks.watcher.push(entry);
      return {
        onDidChange: (fn: () => void) => {
          entry.change = fn;
        },
        onDidCreate: () => {},
        onDidDelete: () => {},
        dispose: () => {
          hooks.watcher = hooks.watcher.filter((w) => w !== entry);
        },
      };
    },
    openTextDocument: (uri: any): Promise<any> =>
      Promise.resolve({
        uri,
        getWordRangeAtPosition: () => undefined,
      }),
    getConfiguration: () => ({ get: () => '' }),
  },
  languages: {
    createDiagnosticCollection: () => ({
      set: (uri: any, list: any[]) => {
        rec.diagnostics.set(uri.fsPath, list);
      },
      clear: () => {
        rec.diagnostics.clear();
      },
      dispose: () => {},
    }),
  },
};

/* `vscode` is resolved through `require`, so it is enough to answer that one
 * request before panel.ts is loaded. `require('./panel')` rather than a top
 * import, so the module body runs after the hook is in place. */
// eslint-disable-next-line @typescript-eslint/no-var-requires
const NodeModule = require('node:module');
const originalLoad = NodeModule._load;
NodeModule._load = function (request: string, ...rest: unknown[]): unknown {
  if (request === 'vscode') {
    return vscodeStub;
  }
  return originalLoad.call(this, request, ...rest);
};

// eslint-disable-next-line @typescript-eslint/no-var-requires
const { StackPanel } = require('./panel') as typeof import('./panel');
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { ViewStateStore } = require('./viewstate') as typeof import('./viewstate');

/* -------------------------------------------------------------------------
 * The core's answers.
 * ---------------------------------------------------------------------- */

const ENTRY = '/w/compose.yaml';
const OVERRIDE = '/w/compose.override.yaml';
const DOCKERFILE = '/w/svc/Dockerfile';

const origin = (file: string, line: number) => ({ file, line, column: 3, step: 0 });

const GRAPH = {
  profiles: [],
  nodes: [
    {
      id: 'services.web',
      kind: 'service',
      name: 'web',
      image: 'nginx:1.27',
      origin: origin(ENTRY, 2),
      declared: true,
      external: false,
      profiles: [],
      layer: 0,
    },
    {
      id: 'services.web.build',
      kind: 'dockerfile',
      name: 'Dockerfile',
      origin: origin(ENTRY, 5),
      declared: true,
      external: false,
      profiles: [],
      layer: 0,
      build: { context: './svc', dockerfile: 'Dockerfile', inline: false },
    },
    {
      id: 'services.api',
      kind: 'service',
      name: 'api',
      image: 'api:2',
      // Declared in the OVERRIDE, which is what makes the watcher set two files.
      origin: origin(OVERRIDE, 4),
      declared: true,
      external: false,
      profiles: [],
      layer: 0,
    },
  ],
  edges: [],
  cycles: [],
  dangling: [],
  max_layer: 0,
};

const FINDINGS: Finding[] = [
  {
    rule: 'build-dockerfile-missing',
    severity: 'error',
    title: 'the Dockerfile is not there',
    message: 'the build names a Dockerfile that does not exist',
    subjects: ['services.web'],
    anchors: [{ label: 'here', path: 'services.web.build', origin: origin(ENTRY, 5) }],
  },
  {
    rule: 'port-collision',
    severity: 'warning',
    title: 'two services publish the same port',
    message: 'both publish 8080',
    subjects: ['services.web', 'services.api'],
    anchors: [
      { label: 'web', path: 'services.web.ports[0]', origin: origin(ENTRY, 4) },
      { label: 'api', path: 'services.api.ports[0]', origin: origin(OVERRIDE, 6) },
    ],
  },
];

const SCHEMA = {
  path: ENTRY,
  schema_commit: '4e2fe7602af8c965ab4fef891e9dde9c5940775f',
  compose_version: '2.29.0',
  compose_version_known: true,
  files: [{ path: ENTRY, step: 0 }],
  profiles: [],
  node: {
    path: 'services.web',
    schema: 'service',
    known: true,
    fields: [{ key: 'image', path: 'services.web.image', declared: true, support: 'unknown' }],
    declared_count: 1,
    available_count: 0,
  },
};

const FORM = {
  path: DOCKERFILE,
  escape_char: '\\',
  crlf: false,
  bom: false,
  missing: false,
  context: './svc',
  dockerfile: 'Dockerfile',
  directives: [],
  preamble: [],
  stages: [],
};

/** A session that answers like the core and records what it was asked. */
function session(over: Record<string, (params: any) => unknown> = {}): any {
  return {
    binary: () => ({ path: '/bin/composure', target: 'darwin-arm64', configured: false }),
    restart: () => {},
    // The third argument is Epic 8's per-request bound. It is RECORDED rather
    // than ignored: the whole point of that parameter is that an optional
    // decoration gives up sooner than a request the pane cannot be drawn
    // without, and a session that dropped it on the floor would let the
    // difference stop existing with every test still green.
    request: (method: string, params: any, timeout?: number) => {
      rec.requests.push({ method, params, timeout });
      if (over[method]) {
        return Promise.resolve(over[method](params));
      }
      switch (method) {
        case 'stack/topology':
          return Promise.resolve(GRAPH);
        case 'stack/diagnose':
          return Promise.resolve({ path: params.path, profiles: [], findings: FINDINGS });
        case 'stack/schema':
          return Promise.resolve(SCHEMA);
        case 'stack/dockerfile':
          return Promise.resolve(FORM);
        case 'stack/preview':
          return Promise.resolve({ diff: '-a\n+b\n', added: 1, removed: 1, ops: [] });
        default:
          return Promise.resolve({});
      }
    },
  };
}

const context = (): any => ({
  extensionUri: { fsPath: '/ext' },
  workspaceState: {
    get: (key: string) => rec.memento.get(key),
    update: (key: string, value: unknown) => {
      rec.memento.set(key, value);
      return Promise.resolve();
    },
  },
});

/** A panel, drawn once, with everything it did recorded. */
async function open(over: Record<string, (params: any) => unknown> = {}): Promise<any> {
  const panel = StackPanel.show(context(), session(over), ENTRY);
  await (panel as any).draw();
  return panel;
}

const postsOf = <K extends HostMessage['type']>(type: K): any[] =>
  rec.posted.filter((m) => m.type === type);

/** Everything queued as microtasks and timers has run. */
const settle = (ms = 0): Promise<void> => new Promise((r) => setTimeout(r, ms));

/**
 * Delivers a webview message and waits for it to finish.
 *
 * The panel registers `(msg) => void this.onMessage(msg)`, so the listener
 * returns undefined rather than the promise — awaiting the CALL awaits nothing
 * at all, which is how a test of an async handler quietly asserts on the state
 * before the handler ran.
 */
async function msg(m: WebviewMessage): Promise<void> {
  hooks.message!(m);
  await settle(5);
}

beforeEach(() => {
  const existing = StackPanel.active();
  if (existing) {
    existing.dispose();
  }
  rec.posted = [];
  rec.watched = [];
  rec.revealed = [];
  rec.diagnostics = new Map();
  rec.memento = new Map();
  rec.requests = [];
  rec.warnings = [];
  hooks.watcher = [];
});

/* -------------------------------------------------------------------------
 * Story 4.3: every file the stack was merged from.
 * ---------------------------------------------------------------------- */

describe('the panel watches every file the drawn stack came from — story 4.3', () => {
  // MUTATION: `watchSources(sourceFiles(graph.nodes))` → `watchSources([file])`.
  // An override saved in another tab then changes the resolved stack and the
  // picture on screen does not move, silently — which is the third acceptance
  // criterion of the story deleted with no test able to see it.
  it('creates a watcher per source file, not one for the entry file', async () => {
    await open();
    assert.deepEqual(
      [...rec.watched].sort(),
      [OVERRIDE, ENTRY].sort(),
      `the panel watches ${JSON.stringify(rec.watched)} — an override changing on disk is invisible`,
    );
  });

  it('does not rebuild the watcher set when it has not changed', async () => {
    const panel = await open();
    const first = rec.watched.length;
    await panel.draw();
    assert.equal(rec.watched.length, first, 'every redraw churns a file handle per source file');
  });

  it('releases every watcher when the panel closes', async () => {
    const panel = await open();
    assert.ok(hooks.watcher.length > 0);
    panel.dispose();
    assert.equal(hooks.watcher.length, 0, 'closing the panel leaks a watcher per source file');
  });

  // MUTATION: `onSourceChanged` neutralised by an early `return` placed BEFORE
  // the two lines panel.test.ts greps for. The scan passes — the text is still
  // there — and a file changing under a staged edit no longer discards it,
  // which is AD-19's whole defence.
  it('discards the stages held against a file that changed, and redraws', async () => {
    const panel = await open();
    // Stage something, so there is work to lose.
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    await settle();
    rec.posted = [];
    const drawsBefore = rec.requests.filter((r) => r.method === 'stack/topology').length;

    // A save arriving through the document listener.
    (panel as any).lastDrawAt = 0;
    hooks.save!({ uri: { fsPath: ENTRY, scheme: 'file' } });
    await settle(5);

    assert.ok(
      postsOf('pendingCleared').length > 0,
      'a file changed under a staged edit and the stage was kept against a moved range',
    );
    assert.ok(
      rec.requests.filter((r) => r.method === 'stack/topology').length > drawsBefore,
      'a changed file no longer redraws, so the picture is of a file that no longer exists',
    );
  });

  it('reacts the same way when the change arrives through the watcher', async () => {
    const panel = await open();
    (panel as any).lastDrawAt = 0;
    const drawsBefore = rec.requests.filter((r) => r.method === 'stack/topology').length;
    const watcher = hooks.watcher.find((w) => w.file === OVERRIDE);
    assert.ok(watcher, 'the override is not watched at all');
    watcher!.change();
    await settle(5);
    assert.ok(
      rec.requests.filter((r) => r.method === 'stack/topology').length > drawsBefore,
      'an override written by another window does not redraw the stack',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 4.3: the cursor moves the selection.
 * ---------------------------------------------------------------------- */

describe('the cursor moves the graph selection — story 4.3', () => {
  it('posts the node the cursor landed in', async () => {
    await open();
    rec.posted = [];
    hooks.cursor!({
      textEditor: { document: { uri: { fsPath: ENTRY, scheme: 'file' } } },
      selections: [{ active: { line: 2 } }], // zero-based: line 3, inside `web`
    });
    await settle(200);
    assert.deepEqual(
      postsOf('selection').map((m) => m.id),
      ['services.web'],
      'the cursor moved in the YAML and the graph was never told',
    );
  });

  it('says nothing when the cursor has not left the node it was in', async () => {
    await open();
    hooks.cursor!({
      textEditor: { document: { uri: { fsPath: ENTRY, scheme: 'file' } } },
      selections: [{ active: { line: 2 } }],
    });
    await settle(200);
    rec.posted = [];
    hooks.cursor!({
      textEditor: { document: { uri: { fsPath: ENTRY, scheme: 'file' } } },
      selections: [{ active: { line: 3 } }],
    });
    await settle(200);
    assert.deepEqual(postsOf('selection'), [], 'scrolling inside one service re-resolves the stack');
  });

  it('debounces, so a held arrow key is one selection and not thirty', async () => {
    await open();
    rec.posted = [];
    for (let line = 1; line <= 30; line++) {
      hooks.cursor!({
        textEditor: { document: { uri: { fsPath: ENTRY, scheme: 'file' } } },
        selections: [{ active: { line } }],
      });
    }
    await settle(200);
    assert.equal(
      postsOf('selection').length,
      1,
      'a held arrow key costs one workspace write and one schema request per repeat',
    );
  });
});

/* -------------------------------------------------------------------------
 * The graph message.
 * ---------------------------------------------------------------------- */

describe('what the graph message carries', () => {
  // MUTATION: `selected: stored.selected` → `selected: null`. Reopening the
  // panel silently forgets what the reader was looking at.
  it('restores the stored selection', async () => {
    rec.memento.set(`composure.view:${ENTRY}`, {
      positions: { 'services.web': { x: 10, y: 20 } },
      selected: 'services.api',
      split: null,
    });
    await open();
    const graph = postsOf('graph')[0];
    assert.equal(graph.selected, 'services.api', 'a redraw forgot the stored selection');
    assert.deepEqual(graph.positions, { 'services.web': { x: 10, y: 20 } });
  });

  // MUTATION: `missing: missingDockerfileNodes(this.findings)` → `missing: []`.
  it('names the Dockerfile nodes whose file is not on disk — story 6.3', async () => {
    await open();
    assert.deepEqual(
      postsOf('graph')[0].missing,
      ['services.web.build'],
      'a build naming a file that is not there draws as an ordinary node',
    );
  });

  it('carries the per-node severity counts — story 5.4', async () => {
    await open();
    const severities = postsOf('graph')[0].severities;
    assert.ok(severities['services.web'], 'no node carries a finding count');
    assert.equal(severities['services.web'].error, 1);
  });
});

/* -------------------------------------------------------------------------
 * Story 5.4: the problems panel.
 * ---------------------------------------------------------------------- */

describe('findings reach VS Code’s problems panel — story 5.4', () => {
  // MUTATION 1: `Problems.publish`'s body replaced with `return`.
  // MUTATION 2: the call renamed to `this.problems.publishDISABLED?.()`, which
  // the previous check — a PREFIX regex on the source — accepted.
  //
  // Both are caught the same way: by asking the diagnostic collection what is
  // in it. Nothing here reads panel.ts as text.
  it('publishes one diagnostic per anchor, filed under the anchor’s own file', async () => {
    await open();
    assert.deepEqual(
      [...rec.diagnostics.keys()].sort(),
      [OVERRIDE, ENTRY].sort(),
      'the problems panel was never told anything',
    );
    assert.equal(rec.diagnostics.get(ENTRY)!.length, 2, 'the entry file lost a finding');
    assert.equal(
      rec.diagnostics.get(OVERRIDE)!.length,
      1,
      'the far end of a cross-file collision is not filed where the reader would look',
    );
  });

  it('publishes exactly as many entries as the inspector says it will', () => {
    // The two counts the owner saw disagree. They still disagree — they are
    // counting different things over different scopes and both are right — but
    // the inspector header now states the position count out loud, derived from
    // `positionCount`. This asserts the panel's own arithmetic is the same
    // function's answer, so the explanation on screen cannot go stale: three
    // findings, four positions, because the collision anchors twice.
    return open().then(() => {
      const published = [...rec.diagnostics.values()].flat().length;
      assert.equal(
        published,
        positionCount(FINDINGS),
        'the panel and the sentence explaining the panel disagree about what a position is',
      );
      assert.ok(
        published > FINDINGS.length,
        'the fixture has no multi-anchor finding, so this check proves nothing',
      );
    });
  });

  it('carries the message, the rule and the severity, and marks itself as ours', async () => {
    await open();
    const first = rec.diagnostics.get(ENTRY)![0];
    assert.equal(first.message, 'the build names a Dockerfile that does not exist');
    assert.equal(first.code, 'build-dockerfile-missing');
    assert.equal(first.source, 'Composure');
    assert.equal(first.severity, vscodeStub.DiagnosticSeverity.Error);
    assert.equal(first.range.start.line, 4, 'the position is off by one — the core’s lines are 1-based');
  });

  it('links the other end of a collision rather than repeating it as a second finding', async () => {
    await open();
    const collision = rec.diagnostics.get(ENTRY)!.find((d: any) => d.code === 'port-collision');
    assert.equal(collision.relatedInformation.length, 1);
    assert.equal(collision.relatedInformation[0].message, 'api');
    const far = rec.diagnostics.get(OVERRIDE)![0];
    assert.match(far.message, /^api: /, 'the second position reads as a duplicate finding');
  });

  // A failed diagnose is not a clean bill of health, asserted on the panel's
  // CONTENTS rather than on the source of the branch that fills it.
  it('says the checks did not run rather than emptying the panel', async () => {
    await open({
      'stack/diagnose': () => {
        throw new Error('the core exited');
      },
    });
    const entries = [...rec.diagnostics.values()].flat();
    assert.equal(entries.length, 1, 'a failed diagnose produced a spotless problems panel');
    assert.equal(entries[0].code, 'diagnostics-unavailable');
    assert.match(entries[0].message, /no rule ran, so no rule found anything/);
    assert.ok(postsOf('diagnosticsUnavailable').length > 0, 'the pane was not told either');
  });
});

/* -------------------------------------------------------------------------
 * Story 5.3: reveal.
 * ---------------------------------------------------------------------- */

describe('reveal moves the editor’s cursor — story 5.3', () => {
  // MUTATION: `case 'reveal':` kept and its body emptied. Every provenance
  // button in the inspector then does nothing, and the check that covered it
  // was `PANEL.includes("case 'reveal':")`.
  it('opens the file the provenance line named and selects the position', async () => {
    await open();
    let opened: any;
    let shown: any;
    vscodeStub.workspace.openTextDocument = (uri: any) => {
      opened = uri;
      return Promise.resolve({ uri, getWordRangeAtPosition: () => undefined });
    };
    const editor = { selection: undefined as any, revealRange: () => {} };
    vscodeStub.window.showTextDocument = (_doc: any, opts: any) => {
      shown = opts;
      return Promise.resolve(editor);
    };

    await msg({ type: 'reveal', file: OVERRIDE, line: 6, column: 3 });
    assert.equal(opened?.fsPath, OVERRIDE, 'pressing a provenance link opened nothing');
    assert.equal(shown.viewColumn, vscodeStub.ViewColumn.One, 'the text opened over the panel');
    assert.equal(shown.preview, false);
    assert.equal(editor.selection.start.line, 5, 'the cursor landed on the wrong line');
    assert.equal(editor.selection.start.character, 2);
  });

  it('refuses a position that names no file, rather than opening something', async () => {
    await open();
    let called = false;
    vscodeStub.workspace.openTextDocument = () => {
      called = true;
      return Promise.resolve({ uri: {}, getWordRangeAtPosition: () => undefined });
    };
    await msg({ type: 'reveal', file: '', line: 3, column: 1 });
    await msg({ type: 'reveal', file: ENTRY, line: 0, column: 1 });
    assert.equal(called, false, 'a provenance line with no position opened a document anyway');
  });
});

/* -------------------------------------------------------------------------
 * View state.
 * ---------------------------------------------------------------------- */

describe('view state is stored, and never reaches a file', () => {
  // MUTATION: `ViewStateStore.setPositions` made a no-op. Every drag is lost on
  // the next redraw and the graph re-flows to its computed layout, which is the
  // one thing a spatial view cannot survive. Nothing asserted the write.
  it('records a dragged position under the drawn file', async () => {
    await open();
    await msg({ type: 'positions', positions: { 'services.web': { x: 42, y: 7 } } });
    const stored = rec.memento.get(`composure.view:${ENTRY}`) as any;
    assert.deepEqual(
      stored?.positions,
      { 'services.web': { x: 42, y: 7 } },
      'a dragged node is forgotten the moment the panel redraws',
    );
  });

  it('keeps the selection and the split when only positions change', async () => {
    await open();
    await msg({ type: 'select', id: 'services.web' });
    await msg({ type: 'split', ratio: 0.7 });
    await msg({ type: 'positions', positions: { 'services.web': { x: 1, y: 2 } } });
    const stored = rec.memento.get(`composure.view:${ENTRY}`) as any;
    assert.equal(stored.selected, 'services.web', 'storing a position dropped the selection');
    assert.equal(stored.split, 0.7, 'storing a position dropped the divider');
  });

  it('clamps a split to something the reader can drag back', async () => {
    await open();
    await msg({ type: 'split', ratio: 0.99 });
    assert.equal((rec.memento.get(`composure.view:${ENTRY}`) as any).split, 0.85);
    await msg({ type: 'split', ratio: 0 });
    assert.equal((rec.memento.get(`composure.view:${ENTRY}`) as any).split, 0.25);
  });
});

/* -------------------------------------------------------------------------
 * Story 4.6: the profile set reaches the core.
 * ---------------------------------------------------------------------- */

describe('the active profile set reaches every core call — story 4.6', () => {
  /**
   * A core whose declared-profile answer is NOT derivable from the graph.
   *
   * `stack/schema` reports `debug` and `prod`; the graph the topology stub
   * returns mentions neither. A control built from the nodes rather than from
   * the core's answer would therefore be empty, which is the shape of the
   * defect this story closes: `prod`'s services are the ones the default
   * filter hides, so they are exactly the ones no node can name.
   */
  const declaring = (): Record<string, (params: any) => unknown> => ({
    'stack/schema': () => ({ ...SCHEMA, profiles: ['debug', 'prod'] }),
  });

  const paramsOf = (method: string): any[] =>
    rec.requests.filter((r) => r.method === method).map((r) => r.params);

  /**
   * A workspace state whose `update` settles on a LATER TURN, and its store.
   *
   * The `context()` helper above sets its map synchronously inside `update` and
   * returns an already-resolved promise, so a value is readable back the instant
   * the call is made. No `Memento` behaves like that — `workspaceState.update`
   * crosses into the extension host's storage and resolves on a later turn — and
   * every check in this suite that reads a value back through `get` is, under
   * that fixture, asserting on something a real editor would not have written
   * yet.
   *
   * The whole class of defect this closes is invisible without it: two messages
   * arriving before the first write lands, both reading the same stale view.
   */
  const deferred = (): { store: Map<string, unknown>; context: any } => {
    const store = new Map<string, unknown>();
    return {
      store,
      context: {
        extensionUri: { fsPath: '/ext' },
        workspaceState: {
          get: (key: string) => store.get(key),
          update: async (key: string, value: unknown) => {
            await settle(1);
            store.set(key, value);
          },
        },
      } as any,
    };
  };

  // THE MUTATION THIS STORY IS ABOUT: any ONE of the three call sites reverted
  // to `profiles: []`. That is the state the extension shipped in — a core
  // capability wired to nothing — and it is invisible from either end: the
  // graph draws, the rules run, focus mode dims something. The reader simply
  // never sees the stack they asked for, or sees a picture and a problem list
  // computed for two different stacks.
  it('sends the same set to topology, diagnose and impact', async () => {
    await open(declaring());
    await msg({ type: 'setProfiles', profiles: ['prod'] });
    rec.requests = [];
    // A redraw and a focus request, so all three methods are exercised under a
    // set that is not the default.
    await (StackPanel.active() as any).draw();
    await msg({ type: 'impact', id: 'services.web' });

    const methods = ['stack/topology', 'stack/diagnose', 'stack/impact'];
    for (const method of methods) {
      const seen = paramsOf(method);
      assert.ok(seen.length > 0, `${method} was never called, so nothing about it was checked`);
      for (const params of seen) {
        assert.deepEqual(
          params.profiles,
          ['prod'],
          `${method} was asked for ${JSON.stringify(params.profiles)} while the reader chose ["prod"]`,
        );
      }
    }
  });

  it('asks for Compose’s default when nothing is switched on', async () => {
    await open(declaring());
    await msg({ type: 'impact', id: 'services.web' });
    for (const method of ['stack/topology', 'stack/diagnose', 'stack/impact']) {
      const seen = paramsOf(method);
      assert.ok(seen.length > 0, `${method} was never called`);
      for (const params of seen) {
        assert.deepEqual(params.profiles, [], `${method} invented a filter nobody asked for`);
      }
    }
  });

  // MUTATION: `declared: normaliseProfiles(schema.profiles)` → `declared: []`,
  // or the list taken from `this.graph.nodes.flatMap(n => n.profiles)`. The
  // toolbar then offers nothing, and the capability stays unreachable while
  // every other check in this file passes.
  it('posts the profile list from the core’s own answer, not from the graph', async () => {
    await open(declaring());
    const posts = postsOf('profiles');
    assert.ok(posts.length > 0, 'the panel never told the webview which profiles exist');
    assert.deepEqual(
      posts[0].declared,
      ['debug', 'prod'],
      'the declared profiles are not the ones stack/schema reported',
    );
    assert.deepEqual(posts[0].active, [], 'the active set was not reported alongside them');
    assert.equal(posts[0].file, ENTRY);
  });

  it('reports no profiles for a project that declares none', async () => {
    await open();
    const posts = postsOf('profiles');
    assert.ok(posts.length > 0, 'the webview is left to guess whether this stack has profiles');
    assert.deepEqual(posts[0].declared, [], 'a project with no profiles was given some');
  });

  // MUTATION: `ViewStateStore.setProfiles` made a no-op, or the store write
  // dropped from `setProfiles`. The toggle works for exactly as long as the
  // panel stays open and is forgotten on reopen — and the reader has no way to
  // tell, because the buttons come back unpressed and the stack looks whole.
  it('persists the set as workspace view state, and writes nothing to a file', async () => {
    await open(declaring());
    await msg({ type: 'setProfiles', profiles: ['prod'] });
    const stored = rec.memento.get(`composure.view:${ENTRY}`) as any;
    assert.deepEqual(stored?.profiles, ['prod'], 'the chosen profile set is not view state');
    assert.deepEqual(
      rec.requests.filter((r) => r.method === 'stack/apply'),
      [],
      'choosing a profile wrote to a file',
    );
    // The other view state is still there: the write is a field, not a reset.
    assert.equal(stored.selected, null);
    assert.deepEqual(stored.positions, {});
  });

  // MUTATION: `profilesFor` returns `[]` instead of reading the store. The set
  // survives the session and is dropped on the next open, which is the same
  // silence as never storing it.
  it('restores the stored set when the panel is opened again', async () => {
    rec.memento.set(`composure.view:${ENTRY}`, {
      positions: {},
      selected: null,
      split: null,
      profiles: ['prod'],
    });
    await open(declaring());
    const first = rec.requests.find((r) => r.method === 'stack/topology');
    assert.ok(first, 'the panel drew nothing');
    assert.deepEqual(
      first!.params.profiles,
      ['prod'],
      'a reopened panel drew Compose’s default and said nothing about the set it had stored',
    );
    assert.deepEqual(postsOf('profiles')[0].active, ['prod'], 'the control comes back unpressed');
  });

  // MUTATION: `setProfiles` stores and does not redraw. The reader presses a
  // profile, the button goes down, and the graph is the old one — R1.4 is
  // literally "watch the topology change".
  it('redraws the stack when the set changes, and not when it does not', async () => {
    await open(declaring());
    const before = rec.requests.filter((r) => r.method === 'stack/topology').length;
    await msg({ type: 'setProfiles', profiles: ['prod'] });
    const after = rec.requests.filter((r) => r.method === 'stack/topology').length;
    assert.ok(after > before, 'toggling a profile did not re-ask the core for the topology');
    // The same set again is not a change: a redraw here is a flicker and a
    // needless resolve of every file in the project.
    await msg({ type: 'setProfiles', profiles: ['prod'] });
    assert.equal(
      rec.requests.filter((r) => r.method === 'stack/topology').length,
      after,
      'setting the same profile set redrew the stack anyway',
    );
  });

  // MUTATION: `fit: this.needsFit` → `fit: true`, or the stored positions
  // dropped from the graph message. Every toggle then re-fits the viewport and
  // re-flows the boxes, and comparing dev against prod — the whole reason the
  // control exists — becomes impossible.
  it('redraws a toggle with the stored positions and without re-fitting', async () => {
    rec.memento.set(`composure.view:${ENTRY}`, {
      positions: { 'services.web': { x: 40, y: 12 } },
      selected: null,
      split: null,
      profiles: [],
    });
    await open(declaring());
    rec.posted = [];
    await msg({ type: 'setProfiles', profiles: ['prod'] });
    const graph = postsOf('graph').pop();
    assert.ok(graph, 'the toggle drew no graph');
    assert.deepEqual(
      graph.positions,
      { 'services.web': { x: 40, y: 12 } },
      'the toggle threw away where the reader had put the boxes',
    );
    assert.equal(graph.fit, false, 'the toggle re-fitted the viewport under the reader');
  });

  // A set arriving from a webview is input. `['prod', 'prod', '  ', 7]` reaching
  // the core as-is is a filter naming a profile no project declares.
  it('normalises whatever the webview sends before it reaches the core', async () => {
    await open(declaring());
    rec.requests = [];
    await msg({ type: 'setProfiles', profiles: ['prod', 'prod', '  ', 'debug', 7 as any] });
    const params = rec.requests.find((r) => r.method === 'stack/topology')!.params;
    assert.deepEqual(
      params.profiles,
      ['debug', 'prod'],
      'a duplicate, a blank or a non-string reached the core as a profile name',
    );
  });

  // MUTATION: `ViewStateStore.update`'s queue removed, so each setter does its
  // own read-modify-write. The webview posts the captured positions and the new
  // profile set back to back; both handlers read the SAME stored view, and the
  // second write puts the first one's old value back. The lost field is silent
  // and looks exactly like a drag that did not stick.
  //
  // The Memento here settles on a later turn, which is what a real one does and
  // what the fast stub above hides.
  it('does not lose a position to a profile set written in the same breath', async () => {
    const { store: slow, context } = deferred();
    const panel = StackPanel.show(context, session(declaring()), ENTRY);
    await (panel as any).draw();

    // Both messages delivered before either has finished, exactly as the
    // webview posts them.
    hooks.message!({ type: 'positions', positions: { 'services.web': { x: 5, y: 6 } } });
    hooks.message!({ type: 'setProfiles', profiles: ['prod'] });
    await settle(30);

    const stored = slow.get(`composure.view:${ENTRY}`) as any;
    assert.deepEqual(
      stored?.positions,
      { 'services.web': { x: 5, y: 6 } },
      'the profile write clobbered the positions the toggle had just captured',
    );
    assert.deepEqual(stored?.profiles, ['prod'], 'the position write clobbered the profile set');
  });

  // THE DEFECT: a toggle that filters out the SELECTED service used to leave
  // the inspector rendering the SERVICE's field list under the STACK's name and
  // kind. `this.graph.nodes.find(...)` returned undefined, and the `??`
  // fallbacks quietly substituted `shortName(file)` and `'stack'` beside a
  // `schema` that was still `services.api`'s. Probed:
  //
  //   {"id":"services.api","name":"w/compose.yaml","kind":"stack","fields":"services.api"}
  //
  // The finding pills also vanished, because the findings had been recomputed
  // under the new profile set and none of them names a service that is no
  // longer there. Nothing anywhere said the service had been filtered out.
  //
  // Rule 6 — no silent failure — decides the shape of the fix: the reader is
  // TOLD the thing they selected is not in the active set, rather than shown
  // the stack in its place. The stored selection is deliberately KEPT: it is a
  // fact about where the reader was, the filter that hid it is one button
  // press, and clearing it would lose their place with no more warning than the
  // bug had.
  //
  // MUTATION: delete the guard and let the `??` fallbacks run again. Both
  // assertions below fail.
  it('says a selected service is filtered out, rather than showing the stack under its name', async () => {
    rec.memento.set(`composure.view:${ENTRY}`, {
      positions: {},
      selected: 'services.api',
      split: null,
      profiles: ['prod'],
    });
    await open({
      ...declaring(),
      // `prod` is on, and the core answers with a graph `api` is not in.
      'stack/topology': () => ({
        ...GRAPH,
        nodes: GRAPH.nodes.filter((n) => n.id !== 'services.api'),
      }),
    });

    assert.deepEqual(
      postsOf('inspection').filter((m) => m.inspection.id === 'services.api'),
      [],
      'the inspector was filled for a node that is not in the drawn stack',
    );
    const said = postsOf('inspectionFailed').filter((m) => m.id === 'services.api');
    assert.equal(said.length, 1, 'a filtered-out selection was inspected in silence');
    assert.equal(
      said[0].reason,
      'filtered',
      'the reason given does not distinguish "filtered out" from "could not be read"',
    );
    assert.match(said[0].detail, /prod/, 'the reader is not told which set filtered it out');

    // And it is still the stored selection, so switching `prod` off brings the
    // reader back to where they were.
    assert.equal(
      (rec.memento.get(`composure.view:${ENTRY}`) as any).selected,
      'services.api',
      'the reader’s place was thrown away rather than filtered',
    );
  });

  // THE DEFECT THIS TEST EXISTS FOR: a press and an unpress, one turn apart.
  //
  // `setProfiles` used to compare the incoming set against a SYNCHRONOUS read of
  // the Memento while the write for the previous toggle was still in flight.
  // `onDidReceiveMessage` is fire-and-forget, so the two handlers overlap: the
  // second one read the pre-toggle set, decided nothing had changed, and
  // returned — no write, no redraw, no message, no warning. The reader's second
  // press simply did not happen.
  //
  // It usually presents as a button that bounces back, because the first
  // toggle's own redraw re-posts `profiles` and resets the control. But when
  // `stack/schema` fails there is no `profiles` post at all, so the divergence
  // is durable AND persisted: unpressed buttons over a filtered graph, with the
  // filtered-stack notice hidden because the webview believes nothing is on.
  //
  // MUTATION: move the comparison back out of the queue — read
  // `this.store.get(file).profiles` in `setProfiles` and early-return on a
  // match. Both assertions below fail.
  it('does not swallow an unpress that arrives before the press has been stored', async () => {
    const { store: slow, context } = deferred();
    const panel = StackPanel.show(context, session(declaring()), ENTRY);
    await (panel as any).draw();
    rec.requests = [];

    // On, then off again before the first write has landed — a double-click, or
    // a reader who changed their mind. Delivered exactly as the webview
    // delivers them: no await between, because the host does not offer one.
    hooks.message!({ type: 'setProfiles', profiles: ['debug'] });
    hooks.message!({ type: 'setProfiles', profiles: [] });
    await settle(40);

    const stored = slow.get(`composure.view:${ENTRY}`) as any;
    assert.deepEqual(
      stored?.profiles,
      [],
      'the second press vanished: the panel stored a profile set the reader had already switched off',
    );
    const asked = rec.requests
      .filter((r) => r.method === 'stack/topology')
      .map((r) => r.params.profiles);
    assert.ok(
      asked.length >= 2,
      `only ${asked.length} topology call(s) for two toggles — one of the presses redrew nothing`,
    );
    assert.deepEqual(
      asked[asked.length - 1],
      [],
      `the last graph the reader was shown was built for ${JSON.stringify(asked[asked.length - 1])}`,
    );
  });

  /**
   * A `stack/topology` that does not answer until the test says so.
   *
   * Every other check in this suite awaits a round trip that has already
   * resolved, so nothing can arrive DURING one — and a draw that reads the
   * active set twice, once on each side of that await, therefore looks exactly
   * like a draw that reads it once.
   */
  const gatedTopology = (): {
    over: Record<string, (p: any) => unknown>;
    arm: () => void;
    open: () => void;
  } => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    let gated = false;
    return {
      over: {
        ...declaring(),
        'stack/topology': async () => {
          if (gated) {
            gated = false;
            await gate;
          }
          return GRAPH;
        },
      },
      arm: () => {
        gated = true;
      },
      open: () => release(),
    };
  };

  // THE DEFECT: `draw()` captured the active set for `stack/topology` and then
  // `diagnose()` READ IT AGAIN, after the topology round trip had awaited. A
  // toggle landing in that gap gave one draw a graph built for set A and
  // findings computed for set B — and that draw published those findings to
  // VS Code's problems panel and posted the graph with severity counts derived
  // from them. Old-set nodes wearing new-set badges. Probed:
  //
  //   PROBE6 topo [[],["prod"]]  diag [["prod"],["prod"]]
  //
  // It is transient — the queued redraw repaints consistently — but it is the
  // property the third acceptance criterion names in words: the picture, the
  // problems and the blast radius are never computed for three different
  // profile sets. It is also the same shape as the defect that held this story:
  // an unsynchronised read across an await.
  //
  // MUTATION: `this.diagnose(file, profiles)` → `this.diagnose(file)` reading
  // `this.profilesFor(file)` again, or the `profiles` post's `active` read from
  // the store rather than from the set the graph was built for. Both fail here.
  it('diagnoses the set the graph was built for when a toggle lands mid-draw', async () => {
    const gate = gatedTopology();
    const panel = StackPanel.show(context(), session(gate.over), ENTRY);
    gate.arm();
    const drawing = (panel as any).draw();
    await settle(1); // the topology request is in flight and will not answer yet

    // The reader presses `prod` while the first graph is still being fetched.
    hooks.message!({ type: 'setProfiles', profiles: ['prod'] });
    await settle(5); // stored, and a redraw queued behind the draw in progress
    gate.open();
    await drawing;
    await settle(30); // and the queued redraw

    // Every diagnose belongs to the topology call before it. Walked in order,
    // because the interleaving is the whole point: an assertion on the LAST
    // pair alone passes while the first draw was inconsistent.
    let built: string[] | undefined;
    let checked = 0;
    for (const r of rec.requests) {
      if (r.method === 'stack/topology') {
        built = r.params.profiles;
      }
      if (r.method === 'stack/diagnose') {
        assert.ok(built !== undefined, 'a diagnose ran for a draw that never asked for a graph');
        assert.deepEqual(
          r.params.profiles,
          built,
          `draw ${checked}: the graph was built for ${JSON.stringify(built)} and diagnosed for ` +
            `${JSON.stringify(r.params.profiles)} — the badges on screen are another stack’s`,
        );
        checked += 1;
      }
    }
    assert.ok(checked >= 2, `only ${checked} diagnose call(s): the toggle never landed mid-draw`);

    // And the control the reader is looking at agrees with the graph beneath
    // it: the first draw's `profiles` post is the set that draw drew.
    assert.deepEqual(
      postsOf('profiles')[0].active,
      [],
      'the profile control was posted the stored set rather than the one the graph was built for',
    );
  });

  // MUTATION: `impact` reads `this.profilesFor(this.file)` again instead of the
  // set the drawn graph was built from. The blast radius then names dependents
  // that are not on the canvas — the confident wrong answer in the one place
  // the reader asks "what breaks if this goes down".
  it('measures the blast radius for the graph on screen, not for a redraw in flight', async () => {
    const gate = gatedTopology();
    const panel = StackPanel.show(context(), session(gate.over), ENTRY);
    await (panel as any).draw(); // drawn for Compose's default set

    gate.arm();
    hooks.message!({ type: 'setProfiles', profiles: ['prod'] });
    await settle(5); // the redraw is in flight and gated; the canvas still shows []
    rec.requests = [];
    await msg({ type: 'impact', id: 'services.web' });

    const asked = rec.requests.find((r) => r.method === 'stack/impact');
    assert.ok(asked, 'focus mode never reached the core');
    assert.deepEqual(
      asked!.params.profiles,
      [],
      'the blast radius was measured for a profile set the picture on screen was not built for',
    );
    gate.open();
    await settle(30);
  });

  // MUTATION: `!isGroupId(id)` deleted from the filtered-selection guard. A
  // collapsed group (story 4.4) is not in `graph.nodes` and never was, so the
  // guard fires and the reader selecting a group is told
  // "group:profile:debug is not in the active profile set" — about a thing the
  // profile set has nothing to do with. Verified against the real binary:
  // `composure schema -at 'group:profile:debug'` answers "no such path in this
  // project", so a group selection belongs in the catch arm.
  //
  // This guard shipped unbacked: deleting it survived the whole suite.
  it('does not call a collapsed group a service the profile set filtered out', async () => {
    const GROUP = 'group:profile:debug';

    // A core that ANSWERS for the group path. This is the case the guard is
    // for: `group:…` is not in `graph.nodes` and never was — it is invented by
    // the webview's collapse — so the filtered-selection test above matches it
    // on every draw, and without `!isGroupId(id)` the reader folding services
    // by profile and clicking the fold is told the fold itself was filtered out
    // of the profile set.
    await open(declaring());
    rec.posted = [];
    await msg({ type: 'select', id: GROUP });
    assert.deepEqual(
      postsOf('inspectionFailed').filter((m) => m.id === GROUP && m.reason === 'filtered'),
      [],
      'a collapsed group was reported as a service the active profile set filtered out',
    );
    assert.ok(
      postsOf('inspection').some((m) => m.inspection.id === GROUP),
      'selecting a collapsed group left the inspector saying nothing at all',
    );

  });

  // The case today's binary actually produces: `composure schema -at
  // 'group:profile:debug'` answers "no such path in this project". A group
  // belongs in the catch arm, told in the core's own words — never in the
  // filtered arm, which would blame a profile filter for a fold.
  it('tells the reader what the core said about a group, not what the filter did', async () => {
    const GROUP = 'group:profile:debug';
    await open({
      ...declaring(),
      'stack/schema': (params: any) => {
        if (isGroupId(String(params.at))) {
          throw new Error('no such path in this project');
        }
        return { ...SCHEMA, profiles: ['debug', 'prod'] };
      },
    });
    rec.posted = [];
    await msg({ type: 'select', id: GROUP });
    const said = postsOf('inspectionFailed').filter((m) => m.id === GROUP);
    assert.equal(said.length, 1, 'a refused group inspection said nothing at all');
    assert.notEqual(said[0].reason, 'filtered', 'a refusal was reported as a profile filter');
    assert.match(
      said[0].detail,
      /no such path/,
      'the reader was not told what the core actually said about the group',
    );
  });

  /** A workspace state whose every write fails once the panel has drawn. */
  const breaking = (): any => {
    const store = new Map<string, unknown>();
    let live = false;
    return {
      extensionUri: { fsPath: '/ext' },
      workspaceState: {
        get: (key: string) => store.get(key),
        update: async (key: string, value: unknown) => {
          await settle(1);
          if (live) {
            throw new Error('workspace storage is unavailable');
          }
          store.set(key, value);
        },
        break: () => (live = true),
      },
    };
  };

  // THE DEFECT: `onDidReceiveMessage` is `(msg) => void this.onMessage(msg)`, so
  // a handler that REJECTS rejects into nothing — an unhandled promise
  // rejection and not a word on screen. The webview has already pressed the
  // button optimistically and the host never re-posts `profiles`, so the
  // control reads "prod on" over an unfiltered graph. Rule 6: no silent
  // failure.
  //
  // The fix is at the boundary rather than inside `setProfiles`, because every
  // one of the eighteen cases in `onMessage` is reached through that same one
  // line and each of them awaits a write, a request or both.
  //
  // MUTATION: drop the `.catch` from the boundary. Nothing is posted.
  it('tells the reader when a message handler fails instead of rejecting into nothing', async () => {
    const ctx = breaking();
    const panel = StackPanel.show(ctx, session(declaring()), ENTRY);
    await (panel as any).draw();
    ctx.workspaceState.break();
    rec.posted = [];

    await msg({ type: 'setProfiles', profiles: ['prod'] });
    await settle(10);

    const failures = postsOf('failure');
    assert.equal(
      failures.length,
      1,
      'a profile set that could not be stored left the button pressed over an unfiltered graph in silence',
    );
    assert.match(
      JSON.stringify(failures[0].failure),
      /storage is unavailable/,
      'the reader is told something failed but not what',
    );
  });

  // THE RESIDUAL of the defect above. The banner says something failed; the
  // BUTTON still says `prod` is on, because `toggleProfile` in the webview
  // presses it optimistically (`main.ts`, `aria-pressed` set before the
  // message is sent) and the only thing that ever un-presses it is a
  // `profiles` post from the host — which the failing path never made, because
  // the declared list lived nowhere but inside `inspect()`'s local scope.
  //
  // A banner plus a lying control is worse than either alone: the reader is
  // told a write failed AND shown a filter that is not applied. The fix is a
  // resync from the host rather than a webview that defers the press, because
  // the host is the only holder of both halves — `drawnProfiles` is what the
  // picture on screen was actually built for, and the webview cannot know it.
  //
  // MUTATION: drop the `resyncProfiles()` call from the boundary catch, or
  // stop caching `declared` in `inspect`. The `profiles` post disappears.
  it('un-presses an optimistically pressed profile button when the write fails', async () => {
    const ctx = breaking();
    const panel = StackPanel.show(ctx, session(declaring()), ENTRY);
    await (panel as any).draw();
    await settle(5);
    assert.ok(
      postsOf('profiles').length > 0,
      'the control was never built, so this test is not exercising a pressed button',
    );
    ctx.workspaceState.break();
    rec.posted = [];

    await msg({ type: 'setProfiles', profiles: ['prod'] });
    await settle(10);

    const resync = postsOf('profiles');
    assert.equal(
      resync.length,
      1,
      'the button stayed pressed for a profile set that was never stored — "prod on" over an unfiltered graph',
    );
    assert.deepEqual(
      resync[0].active,
      [],
      'the control was resynced to the set that failed to store, not to the one the graph was drawn for',
    );
    assert.deepEqual(
      resync[0].declared,
      ['debug', 'prod'],
      'the resync dropped the declared list and would detach the whole control',
    );
    assert.equal(resync[0].file, ENTRY, 'the resync named the wrong file');
  });

  // The other half, through a path the boundary catch cannot see. `draw()`
  // catches its own failures and posts a banner, so a toggle whose STORE
  // succeeded and whose redraw then failed resolves cleanly — `onMessage`
  // never rejects. The set is persisted, the button is pressed, and the canvas
  // is still the previous stack.
  //
  // This is also what pins the resync to `drawnProfiles` rather than to the
  // stored set: here the two DISAGREE, and only one of them describes the
  // picture on screen.
  //
  // MUTATION: `active: this.drawnProfiles` → `this.store.get(this.file).profiles`.
  // The control comes back pressed for a stack that was never drawn.
  it('resyncs the control to the drawn stack when the redraw itself fails', async () => {
    let drawn = 0;
    const panel = await open({
      ...declaring(),
      'stack/topology': () => {
        drawn += 1;
        if (drawn > 1) {
          throw new Error('resolve failed');
        }
        return GRAPH;
      },
    });
    assert.ok(panel);
    rec.posted = [];

    await msg({ type: 'setProfiles', profiles: ['prod'] });
    await settle(10);

    assert.deepEqual(
      (rec.memento.get(`composure.view:${ENTRY}`) as any)?.profiles,
      ['prod'],
      'the set was not stored, so this test is not exercising a stored-but-undrawn toggle',
    );
    assert.ok(postsOf('failure').length > 0, 'a failed redraw said nothing at all');
    const resync = postsOf('profiles');
    assert.equal(resync.length, 1, 'the button stayed pressed over a stack that was never drawn');
    assert.deepEqual(
      resync[0].active,
      [],
      'the control was resynced to the STORED set, not to the stack actually on screen',
    );
    assert.deepEqual(resync[0].declared, ['debug', 'prod'], 'the resync dropped the declared list');
  });

  // F5: a toggle arriving while the Dockerfile form is up posts a `graph`, the
  // webview swaps back to the stack — and the host used to keep
  // `this.dockerfile`, so `editTarget()` still answered with the Dockerfile
  // path. The next staged edit would then be held against a file the reader is
  // no longer looking at.
  //
  // MUTATION: drop `this.dockerfile = undefined` from `setProfiles`.
  it('leaves the Dockerfile view behind when a toggle redraws the stack', async () => {
    const panel = await open(declaring());
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    assert.equal((panel as any).editTarget(), DOCKERFILE, 'the Dockerfile view never opened');

    await msg({ type: 'setProfiles', profiles: ['prod'] });
    assert.ok(postsOf('graph').length > 0, 'the toggle drew no stack');
    assert.equal(
      (panel as any).editTarget(),
      ENTRY,
      'the stack is on screen and an edit would still be staged against the Dockerfile',
    );
  });
});

/* -------------------------------------------------------------------------
 * The write queue itself.
 * ---------------------------------------------------------------------- */

describe('the view-state write queue', () => {
  const FILE = '/w/compose.yaml';

  /** A Memento whose `update` fails for the first `failures` calls. */
  const flaky = (failures: number) => {
    const store = new Map<string, unknown>();
    let calls = 0;
    return {
      store,
      memento: {
        get: (key: string) => store.get(key),
        update: async (key: string, value: unknown) => {
          calls += 1;
          await settle(1);
          if (calls <= failures) {
            throw new Error('workspace storage is unavailable');
          }
          store.set(key, value);
        },
      } as any,
    };
  };

  // MUTATION: `this.writes = run.then(() => undefined, () => undefined)` →
  // `this.writes = run`. The queue's tail is then a REJECTED promise, every
  // later write chains off it and rejects without ever calling the Memento, and
  // the panel silently stops persisting anything for the rest of the session —
  // positions, selection, split and profile set alike. Nothing throws where
  // anyone can see it, because every caller is behind `void this.onMessage(...)`.
  //
  // This guard shipped unbacked: mutating it away survived the whole suite.
  it('keeps writing after one Memento.update throws', async () => {
    const { store, memento } = flaky(1);
    const view = new ViewStateStore(memento);

    await assert.rejects(
      view.setPositions(FILE, { 'services.web': { x: 1, y: 2 } }),
      /workspace storage is unavailable/,
      'a failed write was swallowed rather than reported to its caller',
    );

    // The very next write must reach the Memento and land.
    await view.setSelected(FILE, 'services.api');
    assert.equal(
      (store.get(`composure.view:${FILE}`) as any)?.selected,
      'services.api',
      'one failed write wedged the queue: nothing was ever persisted again',
    );

    // And so must the ones after it, including the profile set — whose return
    // value is what decides whether the graph redraws at all.
    assert.equal(await view.setProfiles(FILE, ['prod']), true);
    assert.deepEqual((store.get(`composure.view:${FILE}`) as any)?.profiles, ['prod']);
    assert.equal(
      await view.setProfiles(FILE, ['prod']),
      false,
      'the store reported a change where the set was identical',
    );
  });

  // The other half: a write that fails must not be reported as a change, or the
  // panel redraws under a profile set that was never stored and the control and
  // the canvas disagree from then on.
  it('reports no change when the write itself failed', async () => {
    const { memento } = flaky(1);
    const view = new ViewStateStore(memento);
    await assert.rejects(view.setProfiles(FILE, ['prod']));
    assert.deepEqual(
      view.get(FILE).profiles,
      [],
      'a failed write left the panel believing a profile set had been stored',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 6.1: a refused edit.
 * ---------------------------------------------------------------------- */

describe('a refused edit reverts the field and stages nothing — story 6.1', () => {
  /** A core that refuses every preview, the way it refuses a flow mapping. */
  function refusing(): Record<string, () => unknown> {
    return {
      'stack/preview': () => {
        const err: any = new Error('the mapping is written in flow style');
        err.code = -32002;
        err.data = { reason: 'flow-style' };
        Object.setPrototypeOf(err, RpcErrorProto);
        throw err;
      },
    };
  }
  // The refusal has to arrive as the core's own error type for `classify` to
  // read it, so the prototype is borrowed from the module that defines it.
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const { RpcError } = require('./core') as typeof import('./core');
  const RpcErrorProto = RpcError.prototype;

  it('names the refusal rather than failing silently', async () => {
    await open(refusing());
    rec.posted = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    const refused = postsOf('editRefused');
    assert.equal(refused.length, 1, 'a refused edit said nothing at all');
    assert.match(refused[0].detail, /flow style/);
    assert.equal(refused[0].title, 'That edit was not made');
  });

  // MUTATION: the `await this.inspect(...)` after `stageValue` deleted. The
  // refusal banner still appears and the FIELD still holds what the reader
  // typed — a value that is not in the file and is not staged either, which is
  // the pane telling them a change happened.
  it('refills the pane from the core, which is what makes the field revert', async () => {
    await open(refusing());
    rec.posted = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    const inspection = postsOf('inspection');
    assert.equal(
      inspection.length,
      1,
      'the pane was not refilled, so the field still shows a value nothing holds',
    );
    assert.deepEqual(inspection[0].inspection.staged, [], 'a refused edit entered the pending list');
    assert.deepEqual(inspection[0].inspection.pending, {});
  });

  it('leaves nothing staged, so Save has nothing to write', async () => {
    await open(refusing());
    rec.posted = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    rec.requests = [];
    await msg({ type: 'save' });
    assert.deepEqual(
      rec.requests.filter((r) => r.method === 'stack/apply'),
      [],
      'a refused edit was written anyway',
    );
  });

  it('writes only what a save asks for, and only once', async () => {
    await open();
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    rec.requests = [];
    await msg({ type: 'save' });
    const writes = rec.requests.filter((r) => r.method === 'stack/apply');
    assert.equal(writes.length, 1, `the save path performed ${writes.length} writes`);
    assert.equal(writes[0].params.file, ENTRY);
  });

  it('never writes without one', async () => {
    await open();
    rec.requests = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    await msg({ type: 'open', path: 'services.web.restart' });
    await msg({ type: 'select', id: 'services.web' });
    await msg({ type: 'positions', positions: {} });
    assert.deepEqual(
      rec.requests.filter((r) => r.method === 'stack/apply'),
      [],
      'something other than Save reached the write path',
    );
  });

  // MUTATION: `saveLabel: saveLabel(file)` → `saveLabel: 'Save'`. Story 6.1's
  // third criterion names the control "Save to compose.yml", and pending.ts's
  // own header says the filename is there because "in a multi-file project that
  // is the question the reader actually has" — the button is the last thing
  // read before a write, and which file it writes is the fact it exists to
  // carry.
  //
  // This was a partial-subject blind check, the shape the sweep keeps finding
  // at boundaries. `saveLabel()` is tested on its own (staging.test.ts:91) and
  // the webview renders whatever string it is handed (appdom.test.ts:465 posts
  // a literal 'Save to compose.yaml'). Both halves were pinned and the JOIN was
  // not, so the host could stop wiring the real file in and both suites stayed
  // green.
  it('names the file it would write to on the button that writes it', async () => {
    await open();
    rec.posted = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    const pending = postsOf('pending');
    assert.equal(pending.length, 1, 'staging an edit posted no pending strip');
    assert.equal(pending[0].file, ENTRY, 'the strip does not name the file it reports on');
    assert.equal(
      pending[0].saveLabel,
      'Save to compose.yaml',
      'the write control does not name the file it writes to',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 6.3: the Dockerfile, reached from the service.
 * ---------------------------------------------------------------------- */

describe('opening a Dockerfile from the canvas — story 6.3', () => {
  // MUTATION: `at: at ?? ''` → `at: ''`. The core then resolves the Dockerfile
  // relative to nothing instead of relative to the build context, so a build
  // declaring `context: ./svc` opens the WRONG FILE — or reports it missing —
  // and the panel says nothing about having asked the wrong question.
  it('asks the core AT the build node, so the context decides which file', async () => {
    await open();
    rec.requests = [];
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    const ask = rec.requests.find((r) => r.method === 'stack/dockerfile');
    assert.ok(ask, 'opening the node asked the core nothing');
    assert.equal(
      ask!.params.at,
      'services.web.build',
      'the build context was dropped, so the core resolves the Dockerfile from the wrong place',
    );
    assert.equal(ask!.params.path, ENTRY);
  });

  it('posts the form for the file the core resolved, not the path asked for', async () => {
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    const form = postsOf('dockerfile')[0];
    assert.ok(form, 'the stage form was never posted');
    assert.equal(form.file, DOCKERFILE);
    assert.equal(form.from, ENTRY, 'the way back to the stack was not recorded');
  });

  it('says there is nothing to open for an inline Dockerfile rather than opening an empty form', async () => {
    const panel = await open();
    (panel as any).graph.nodes.push({
      id: 'services.inline.build',
      kind: 'dockerfile',
      name: 'inline',
      origin: origin(ENTRY, 9),
      declared: true,
      external: false,
      profiles: [],
      layer: 0,
      build: { context: '.', dockerfile: '', inline: true },
    });
    rec.posted = [];
    await msg({ type: 'openDockerfile', id: 'services.inline.build' });
    assert.deepEqual(postsOf('dockerfile'), [], 'an inline Dockerfile opened an empty form');
    assert.match(postsOf('editRefused')[0].detail, /dockerfile_inline/);
  });

  // MUTATION: `setFile` returns early when the path has not changed, without
  // leaving the Dockerfile view. The owner reported this one: open a
  // Dockerfile, select the compose file again, and the pane keeps drawing the
  // stage form. `this.file` never left the compose file while the Dockerfile
  // was showing, so the commonest way of asking for the stack back — clicking
  // the compose file — was the one path that could not deliver it.
  it('leaves the Dockerfile view when the compose file it came from is selected again', async () => {
    const panel = await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    assert.equal(postsOf('dockerfile').length, 1, 'the stage form never opened');

    rec.posted = [];
    panel.setFile(ENTRY); // the same file the Dockerfile was reached from
    await settle(5);

    assert.deepEqual(
      postsOf('dockerfile'),
      [],
      'the stage form was drawn again after asking for the stack',
    );
    assert.ok(postsOf('graph')[0], 'the stack was never drawn');
  });

  it('opens nothing for a node that is not a Dockerfile', async () => {
    await open();
    rec.posted = [];
    await msg({ type: 'openDockerfile', id: 'services.web' });
    assert.deepEqual(postsOf('dockerfile'), []);
  });

  // MUTATION: `backToStack` redraws without the stored selection. The reader
  // opened the Dockerfile from a service and comes back to a deselected stack
  // and an inspector showing the whole file — story 6.3's second criterion.
  it('comes back to the stack with the selection intact', async () => {
    rec.memento.set(`composure.view:${ENTRY}`, { positions: {}, selected: 'services.web', split: null });
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    rec.posted = [];
    await msg({ type: 'backToStack' });
    await settle(5);
    const graph = postsOf('graph')[0];
    assert.ok(graph, 'the stack was never drawn again');
    assert.equal(
      graph.selected,
      'services.web',
      'the round trip through the Dockerfile forgot what was selected',
    );
  });

  it('stages an edit against the Dockerfile, not against the compose file', async () => {
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    rec.requests = [];
    await msg({ type: 'editStage', stage: 1, value: 'alpine:3.21' });
    const preview = rec.requests.find((r) => r.method === 'stack/preview');
    assert.ok(preview, 'editing a stage previewed nothing');
    assert.equal(preview!.params.file, DOCKERFILE, 'a Dockerfile edit was staged against the compose file');
    assert.deepEqual(preview!.params.ops, [
      { operation: 'set_base_image', stage: 1, value: 'alpine:3.21' },
    ]);
  });
});

/* -------------------------------------------------------------------------
 * Stories 7.6 and 7.7: adding to a Dockerfile.
 * ---------------------------------------------------------------------- */

describe('adding to a Dockerfile — stories 7.6 and 7.7', () => {
  // MUTATION: `stage: msg.stage` → `stage: 0`. Every instruction the reader
  // adds from any stage's `Available here` list lands in the FIRST stage — a
  // confident wrong write in the grammar where it is hardest to notice, and
  // one no assertion about "an op was staged" can see.
  it('stages the instruction against the stage it was typed into', async () => {
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    rec.requests = [];
    await msg({ type: 'addInstruction', stage: 1, text: 'HEALTHCHECK CMD curl -f http://localhost/' });
    const preview = rec.requests.find((r) => r.method === 'stack/preview');
    assert.ok(preview, 'adding an instruction previewed nothing');
    assert.equal(preview!.params.file, DOCKERFILE, 'the add was staged against the compose file');
    assert.deepEqual(preview!.params.ops, [
      {
        operation: 'insert_instruction',
        stage: 1,
        value: 'HEALTHCHECK CMD curl -f http://localhost/',
      },
    ]);
  });

  it('stages a new stage as insert_stage, with the name the reader gave it', async () => {
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    rec.requests = [];
    await msg({ type: 'addStage', image: 'nginx:1.27', name: 'serve' });
    const preview = rec.requests.find((r) => r.method === 'stack/preview');
    assert.ok(preview, 'adding a stage previewed nothing');
    assert.deepEqual(preview!.params.ops, [
      { operation: 'insert_stage', value: 'nginx:1.27', key: 'serve' },
    ]);
  });

  it('writes nothing until Save, and previews before it stages', async () => {
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    rec.requests = [];
    await msg({ type: 'addInstruction', stage: 0, text: 'USER app' });
    assert.equal(
      rec.requests.some((r) => r.method === 'stack/apply'),
      false,
      'adding an instruction wrote to the file',
    );
    const pending = postsOf('pending').at(-1);
    assert.ok(pending, 'nothing appeared in the pending strip');
    assert.equal(pending.file, DOCKERFILE, 'the diff does not name the file it would touch');
  });

  // Two different instructions in one stage are two stages, not one replacing
  // the other. The same instruction typed twice replaces — the reader corrected
  // themselves, they did not ask for two.
  it('keeps two different additions and replaces a re-typed one', async () => {
    await open();
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    await msg({ type: 'addInstruction', stage: 0, text: 'USER app' });
    await msg({ type: 'addInstruction', stage: 0, text: 'STOPSIGNAL SIGTERM' });
    rec.requests = [];
    await msg({ type: 'addInstruction', stage: 0, text: 'USER root' });
    const preview = rec.requests.find((r) => r.method === 'stack/preview');
    assert.deepEqual(
      preview!.params.ops.map((o: { value: string }) => o.value),
      ['USER root', 'STOPSIGNAL SIGTERM'],
      'a re-typed instruction did not replace its own stage, or a different one was lost',
    );
  });

  // The refusal the core sends comes back as a refusal in the reader's own
  // language, not as the engine's sentence and not as a fault.
  it('names a refused add in prose', async () => {
    // The refusal has to arrive as the core's own error type for `classify` to
    // read it, so the prototype is borrowed from the module that defines it.
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { RpcError } = require('./core') as typeof import('./core');
    await open({
      'stack/preview': () => {
        const err: any = new Error('line 3 already declares a stage called "build"');
        err.code = -32002;
        err.data = { reason: 'stage-name' };
        Object.setPrototypeOf(err, RpcError.prototype);
        throw err;
      },
    });
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    rec.posted = [];
    await msg({ type: 'addStage', image: 'alpine:3.20', name: 'build' });
    const refused = postsOf('editRefused')[0];
    assert.ok(refused, 'a refused add said nothing at all');
    assert.equal(refused.title, 'That edit was not made');
    assert.match(refused.detail, /already declares a stage/);
    assert.match(refused.detail, /Pick another name/, 'the reader is told the engine’s sentence and nothing else');
  });
});

/* -------------------------------------------------------------------------
 * Stories 7.3 and 7.4: adding a service, declaring a resource.
 * ---------------------------------------------------------------------- */

describe('adding to a compose file — stories 7.3 and 7.4', () => {
  /** The core's planner, answering as `stack/add` does. */
  const planner = (ops: unknown[]) => ({
    'stack/add': () => ({ ops }),
  });

  const SERVICE_OPS = [
    { operation: 'insert_key', at: 'services', key: 'cache', value: '' },
    { operation: 'insert_key', at: 'services.cache', key: 'image', value: 'redis:7' },
  ];

  // MUTATION: the two planned operations staged as two separate requests. The
  // reader gets two diffs, two undos, and a `cache:` with nothing under it on
  // disk if the second one fails — the partial write the write path exists to
  // make impossible.
  it('previews the whole plan as ONE request against the compose file', async () => {
    await open(planner(SERVICE_OPS));
    rec.requests = [];
    await msg({ type: 'add', kind: 'service', name: 'cache', value: 'redis:7' });

    const previews = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.ok(previews.length > 0, 'the add previewed nothing');
    // Every request carries the WHOLE plan. A pair staged one at a time would
    // show here as a request with one operation in it — which is the partial
    // write, previewed.
    for (const p of previews) {
      assert.equal(p.params.ops.length, 2, 'an operation of the plan travelled on its own');
      assert.equal(p.params.file, ENTRY, 'the declaration was staged against another file');
    }
    assert.deepEqual(
      previews[0].params.ops.map((o: { operation: string; at: string; key: string; value: string }) => [
        o.operation,
        o.at,
        o.key,
        o.value,
      ]),
      [
        ['insert_key', 'services', 'cache', ''],
        ['insert_key', 'services.cache', 'image', 'redis:7'],
      ],
      'the operations the core planned are not the operations that were staged',
    );
  });

  // The planner is the CORE's. Nothing in TypeScript decides where a service
  // goes, whether the name is free, or that a service needs an image.
  it('asks the core to plan it, naming the project and the file it writes', async () => {
    await open(planner(SERVICE_OPS));
    rec.requests = [];
    await msg({ type: 'add', kind: 'service', name: 'cache', value: 'redis:7' });
    const plan = rec.requests.find((r) => r.method === 'stack/add');
    assert.ok(plan, 'the panel planned the declaration itself instead of asking the core');
    assert.deepEqual(plan!.params, {
      path: ENTRY,
      file: ENTRY,
      kind: 'service',
      name: 'cache',
      value: 'redis:7',
    });
  });

  it('writes nothing, and shows the diff it would write', async () => {
    await open(planner(SERVICE_OPS));
    rec.posted = [];
    rec.requests = [];
    await msg({ type: 'add', kind: 'service', name: 'cache', value: 'redis:7' });
    assert.equal(
      rec.requests.some((r) => r.method === 'stack/apply'),
      false,
      'adding a service wrote to the file',
    );
    const pending = postsOf('pending').at(-1);
    assert.ok(pending, 'nothing appeared in the pending strip');
    assert.equal(pending.file, ENTRY, 'the diff does not name the file it would touch (R4.7)');
  });

  // A resource is the same path with a different first segment, and the block
  // it goes in often does not exist — two operations, still one request.
  it('stages a resource declaration, block and entry together', async () => {
    const ops = [
      { operation: 'insert_key', at: '', key: 'networks', value: '' },
      { operation: 'insert_key', at: 'networks', key: 'frontend', value: '' },
    ];
    await open(planner(ops));
    rec.requests = [];
    await msg({ type: 'add', kind: 'network', name: 'frontend', value: '' });
    const preview = rec.requests.find((r) => r.method === 'stack/preview');
    assert.ok(preview, 'declaring a network previewed nothing');
    assert.deepEqual(
      preview!.params.ops.map((o: { at: string; key: string }) => [o.at, o.key]),
      [
        ['', 'networks'],
        ['networks', 'frontend'],
      ],
    );
  });

  // Every kind reaches the core with the kind the reader chose. A mutation that
  // sent 'service' for all five would otherwise pass on the service test alone.
  it('sends the kind the reader chose, for every kind', async () => {
    for (const kind of ['service', 'network', 'volume', 'config', 'secret'] as const) {
      await open(planner([{ operation: 'insert_key', at: `${kind}s`, key: 'thing', value: '' }]));
      rec.requests = [];
      await msg({ type: 'add', kind, name: 'thing', value: kind === 'service' ? 'redis:7' : '' });
      const plan = rec.requests.find((r) => r.method === 'stack/add');
      assert.equal(plan!.params.kind, kind, `a ${kind} was planned as a ${plan!.params.kind}`);
    }
  });

  // Two declarations of the same kind, before either is written.
  //
  // Neither has reached the file, so the core plans the top-level block a
  // second time — and the two plans overlap. Keyed by the path each operation
  // CREATES, the repeated block insert replaces itself and both entries
  // survive. Keyed by anything coarser, declaring a second network throws the
  // first one away, and the reader watches their own work vanish from the
  // strip with no message.
  it('keeps both when two of a kind are declared before either is written', async () => {
    const panel = await open({
      'stack/add': (params: any) => ({
        ops: [
          { operation: 'insert_key', at: '', key: 'networks', value: '' },
          { operation: 'insert_key', at: 'networks', key: params.name, value: '' },
        ],
      }),
    });
    void panel;
    await msg({ type: 'add', kind: 'network', name: 'frontend', value: '' });
    rec.requests = [];
    await msg({ type: 'add', kind: 'network', name: 'backend', value: '' });

    const preview = rec.requests.find((r) => r.method === 'stack/preview');
    assert.ok(preview, 'the second declaration previewed nothing');
    assert.deepEqual(
      preview!.params.ops.map((o: { at: string; key: string }) => [o.at, o.key]),
      [
        ['', 'networks'],
        ['networks', 'frontend'],
        ['networks', 'backend'],
      ],
      'declaring a second network lost the first one, or asked for the block twice',
    );
  });

  // A declaration the core declines is a refusal in the reader's language, and
  // NOTHING is staged: the pending strip must not fill with an edit that was
  // never planned.
  it('names a refused declaration in prose and stages nothing', async () => {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { RpcError } = require('./core') as typeof import('./core');
    await open({
      'stack/add': () => {
        const err: any = new Error('services.cache is already declared at /w/compose.yaml:9');
        err.code = -32002;
        err.data = { reason: 'duplicate-name' };
        Object.setPrototypeOf(err, RpcError.prototype);
        throw err;
      },
    });
    rec.posted = [];
    rec.requests = [];
    await msg({ type: 'add', kind: 'service', name: 'cache', value: 'redis:7' });

    const refused = postsOf('editRefused')[0];
    assert.ok(refused, 'a refused declaration said nothing at all');
    assert.match(refused.detail, /already declared at \/w\/compose\.yaml:9/);
    assert.match(refused.detail, /Pick another name/);
    assert.equal(
      rec.requests.some((r) => r.method === 'stack/preview'),
      false,
      'a refused plan was staged anyway',
    );
    assert.equal(postsOf('pending').length, 0, 'the pending strip filled with an edit that was refused');
  });
});

/* -------------------------------------------------------------------------
 * Decision 21: editing a value the file inherits writes it HERE.
 *
 * The defect, in the owner's words: selecting `web`, changing the `restart`
 * combobox, and being told `edit: operation 0: path services.web.restart not
 * found`. The pane had rendered `unless-stopped` with the provenance
 * `compose.yaml:9 · from *defaults at compose.yaml:29` — so the value is not
 * written at that path at all, and the write path was right to say so.
 *
 * THE TRAP these tests are written against: a fixture whose service DECLARES
 * the key it also inherits cannot tell an inherited value from a declared one.
 * `restart` below is inherited and `image` is declared, and both assertions
 * name which they are about.
 * ---------------------------------------------------------------------- */

/** A schema answer where `restart` resolves but the file does not write it. */
const MERGED_SCHEMA = {
  ...SCHEMA,
  node: {
    ...SCHEMA.node,
    fields: [
      { key: 'image', path: 'services.web.image', declared: true, support: 'unknown' },
      // Declared as far as the RESOLVER is concerned — which is exactly why
      // `declared` could never have answered this question.
      { key: 'restart', path: 'services.web.restart', declared: true, support: 'unknown' },
    ],
  },
};

const INHERITED = {
  path: 'services.web.restart',
  editable: false,
  reason: 'inherited',
  plan: 'insert_key',
  anchor: 'defaults',
  detail: 'web does not set restart here — it arrives through `<<: *defaults` on line 29.',
  through: { line: 29, column: 5 },
  bytes_at: { line: 9, column: 12 },
};

const mergedCore = (extra: Record<string, (params: any) => unknown> = {}) => ({
  'stack/schema': () => MERGED_SCHEMA,
  'stack/editable': (params: any) => ({
    file: params.file,
    fields: (params.paths as string[]).map((path) =>
      path === 'services.web.restart'
        ? INHERITED
        : { path, editable: true, bytes_at: { line: 3, column: 12 } },
    ),
  }),
  ...extra,
});

describe('a value that arrives through a merge key — decision 21', () => {
  // MUTATION: `!overrides` dropped from the `declared` branch of `stageValue`.
  // The stage becomes a `replace_scalar` again and the core answers `path
  // services.web.restart not found` — the defect, restored.
  it('stages an insert on the service rather than a replace that lands on nothing', async () => {
    await open(mergedCore());
    rec.requests = [];
    await msg({ type: 'edit', path: 'services.web.restart', value: 'always' });

    // Two previews per stage: one to place the byte range the stage is held
    // against, one to refresh the pending strip. Both carry the same ops.
    const previewed = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.ok(previewed.length >= 1, 'the edit was not previewed');
    assert.deepEqual(previewed[0].params.ops, [
      { operation: 'insert_key', at: 'services.web', key: 'restart', value: 'always' },
    ]);
  });

  // The other half of the trap: a key the file DOES write is still a splice.
  it('leaves a declared value as a two-line replace', async () => {
    await open(mergedCore());
    rec.requests = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });

    const previewed = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.deepEqual(previewed[0].params.ops, [
      { operation: 'replace_scalar', at: 'services.web.image', value: 'nginx:1.28' },
    ]);
  });

  // MUTATION: `availability: Object.fromEntries(this.availability)` removed
  // from the inspection post. The pane renders the field with no sentence under
  // it, which is the rule-6 failure this whole change is about, and every
  // assertion above still passes.
  it('sends the pane where each value is written, so the field can say so', async () => {
    await open(mergedCore());
    const inspection = postsOf('inspection').pop();
    assert.ok(inspection, 'no inspection was posted');
    assert.equal(inspection.inspection.availability['services.web.restart'].reason, 'inherited');
    assert.equal(inspection.inspection.availability['services.web.image'].editable, true);
  });

  it('asks the core about the paths it is about to draw, and only once', async () => {
    await open(mergedCore());
    const asked = rec.requests.filter((r) => r.method === 'stack/editable');
    assert.equal(asked.length, 1, `stack/editable was called ${asked.length} times for one pane`);
    assert.deepEqual(asked[0].params.paths.sort(), [
      'services.web.image',
      'services.web.restart',
    ]);
  });

  // Losing the explanation is bad; losing the inspector because an explanation
  // could not be fetched would leave the reader with neither.
  it('still renders the pane when the core cannot say where values are written', async () => {
    await open(
      mergedCore({
        'stack/editable': () => {
          throw new Error('core went away');
        },
      }),
    );
    const inspection = postsOf('inspection').pop();
    assert.ok(inspection, 'the inspector was lost because an explanation could not be fetched');
    assert.deepEqual(inspection.inspection.availability, {});
  });
});

/* -------------------------------------------------------------------------
 * Epic 8: the network is not the pane's to wait for.
 * ---------------------------------------------------------------------- */

/** A schema answer whose `image` actually holds a value, so it is asked about. */
const SCHEMA_WITH_IMAGE = {
  ...SCHEMA,
  node: {
    ...SCHEMA.node,
    fields: [
      {
        key: 'image',
        path: 'services.web.image',
        declared: true,
        support: 'unknown',
        value: {
          kind: 'scalar',
          text: 'nginx:1.27',
          env_known: true,
          origin: origin(ENTRY, 3),
          overrides: [],
        },
      },
    ],
  },
};

const LOOKUP_OK = {
  reference: 'nginx:1.27',
  repository: 'library/nginx',
  display: 'nginx',
  tag: '1.27',
  state: 'ok',
  message: 'nginx:1.29 is a minor upgrade in the same family.',
  age: '14 months old',
  age_days: 425,
  pill: 'nginx:1.29 · minor · 40MB smaller',
  candidate: { reference: 'nginx:1.29', tag: '1.29', kind: 'minor' },
};

describe('the pane never waits on Docker Hub — Epic 8, DECISIONS.md 22', () => {
  // THE CLAIM OF THE WHOLE EPIC, as one assertion.
  //
  // MUTATION: move `void this.lookupImages(...)` above the `inspection` post,
  // or put an `await` in front of it. Either makes the inspector's paint depend
  // on a third party's undocumented endpoint, and this test is what says so:
  // the core accepts `image/lookup` and NEVER answers it, and the pane must be
  // complete anyway.
  it('posts the inspection when image/lookup never answers at all', async () => {
    await open({
      'stack/schema': () => SCHEMA_WITH_IMAGE,
      // Never resolves. Not a rejection — a rejection is a fast path that would
      // let a broken ordering pass.
      'image/lookup': () => new Promise(() => undefined),
    });
    await settle(20);
    const inspections = postsOf('inspection');
    assert.equal(inspections.length > 0, true, 'the inspector never rendered');
    // Complete, not merely present: the field list is there, from the file.
    assert.equal(
      inspections[0].inspection.schema.node.fields[0].path,
      'services.web.image',
      'the inspector rendered without the fields it is for',
    );
    // …and nothing was posted about an image, because nothing was learned.
    assert.deepEqual(postsOf('imageLookup'), []);
    // …and the graph is up too. A hung optional request must not hold the draw.
    assert.equal(postsOf('graph').length > 0, true, 'the graph never rendered');
  });

  it('posts the lookup after the inspection, never before it', async () => {
    await open({ 'stack/schema': () => SCHEMA_WITH_IMAGE, 'image/lookup': () => LOOKUP_OK });
    await settle(20);
    const order = rec.posted.map((m) => m.type);
    const inspection = order.indexOf('inspection');
    const lookup = order.indexOf('imageLookup');
    assert.ok(lookup >= 0, 'no lookup was posted');
    assert.ok(
      inspection >= 0 && inspection < lookup,
      `the lookup was posted at ${lookup} and the inspection at ${inspection}: ` +
        'the pane is waiting on Docker Hub',
    );
    const posted = postsOf('imageLookup')[0];
    assert.equal(posted.key, 'services.web.image');
    assert.equal(posted.lookup.pill, 'nginx:1.29 · minor · 40MB smaller');
  });

  // A pane with no image asks nothing. Docker Hub allows 180 requests a minute
  // for everyone behind one address; a request made for no reason is one a
  // colleague then cannot make.
  it('asks about nothing when the pane holds no image', async () => {
    await open({ 'image/lookup': () => LOOKUP_OK });
    await settle(20);
    assert.deepEqual(
      rec.requests.filter((r) => r.method === 'image/lookup'),
      [],
      'the panel asked Docker Hub about a pane with no image on it',
    );
  });

  // MUTATION: drop the `readLookup` guard, or the state check. A core from the
  // future with a tenth state would then put a sentence this build does not
  // understand under a reader's image field, as though it had been checked.
  it('drops an answer this build cannot read', async () => {
    await open({
      'stack/schema': () => SCHEMA_WITH_IMAGE,
      'image/lookup': () => ({ state: 'quantum', message: 'from a newer core', reference: 'x' }),
    });
    await settle(20);
    assert.deepEqual(postsOf('imageLookup'), []);
  });

  // `cancelled` means the reader selected something else. A sentence about a
  // question they stopped asking would arrive on the pane they moved TO.
  it('says nothing about a cancelled lookup', async () => {
    await open({
      'stack/schema': () => SCHEMA_WITH_IMAGE,
      'image/lookup': () => ({
        reference: 'nginx:1.27',
        state: 'cancelled',
        message: 'The lookup was cancelled.',
      }),
    });
    await settle(20);
    assert.deepEqual(postsOf('imageLookup'), []);
  });

  // Rate limiting is a first-class state and reaches the reader as a sentence.
  // Silence would read as "there is nothing newer".
  it('passes a rate-limited answer through as a state and a sentence', async () => {
    await open({
      'stack/schema': () => SCHEMA_WITH_IMAGE,
      'image/lookup': () => ({
        reference: 'nginx:1.27',
        state: 'rate-limited',
        message: 'Docker Hub is rate limiting this address. It passes on its own.',
      }),
    });
    await settle(20);
    const posted = postsOf('imageLookup')[0];
    assert.ok(posted, 'a rate-limited lookup said nothing at all');
    assert.equal(posted.lookup.state, 'rate-limited');
    assert.match(posted.lookup.message, /passes on its own/);
    // NOT a failure banner. The rest of the pane is still true.
    assert.deepEqual(postsOf('failure'), []);
  });

  // A lookup takes as long as a registry takes, which is longer than a reader
  // takes to click something else. An upgrade pill for one service's image on
  // another service's pane is the specific way an asynchronous decoration goes
  // wrong, and it is the purest form of the confident wrong answer this whole
  // project is arranged against.
  //
  // MUTATION: delete the `generation !== this.imageGeneration` guard. It
  // SURVIVED the first version of this suite, which is why this test exists.
  it('drops an answer that arrives after the reader has moved to another pane', async () => {
    // EVERY resolver, not the last one. Holding a single `release` binding was
    // the first version of this test and it was worthless: the selection change
    // starts a SECOND lookup, which overwrote the binding, so releasing it
    // released the current pane's own request and the assertion failed against
    // correct code. The one that has to be released is the FIRST.
    const pending: ((v: unknown) => void)[] = [];
    const panel = await open({
      'stack/schema': () => SCHEMA_WITH_IMAGE,
      'image/lookup': () => new Promise((r) => pending.push(r)),
    });
    await settle(10);
    assert.equal(pending.length, 1, 'no lookup was in flight, so this test proves nothing');
    // The reader selects something else while the request is out.
    await msg({ type: 'select', id: 'services.api' });
    rec.posted = [];
    pending[0](LOOKUP_OK);
    await settle(20);
    assert.deepEqual(
      postsOf('imageLookup'),
      [],
      'an answer fetched for the previous selection decorated the current one',
    );
    panel.dispose();
  });

  // The bound, and the fact that it is not the pane's own. A socket held open
  // for thirty seconds on a dead network is a request slot spent on a fact
  // nobody is waiting for.
  it('bounds the lookup more tightly than the requests a pane needs', async () => {
    await open({ 'stack/schema': () => SCHEMA_WITH_IMAGE, 'image/lookup': () => LOOKUP_OK });
    await settle(20);
    const ask = rec.requests.find((r) => r.method === 'image/lookup');
    assert.ok(ask, 'no lookup was requested');
    assert.ok(
      typeof (ask as any).timeout === 'number' && (ask as any).timeout <= 12000,
      `the lookup was sent with timeout ${(ask as any).timeout}`,
    );
  });
});

describe('searching Docker Hub from the webview — Epic 8', () => {
  it('answers a search with results', async () => {
    await open({
      'image/search': (p: any) => ({
        query: p.query,
        state: 'ok',
        message: '',
        results: [{ name: 'postgres', stars: 1, official: true }],
      }),
    });
    await msg({ type: 'searchImage', token: 7, query: 'postgres' });
    const posted = postsOf('imageSearch')[0];
    assert.ok(posted, 'the search was never answered');
    assert.equal(posted.token, 7, 'the answer carries a different token from the request');
    assert.equal(posted.answer.results[0].name, 'postgres');
  });

  // A popup that stays empty when Docker Hub is unreachable reads as "there is
  // no such image". The reader asked for this one, so unlike the pill it is
  // answered with a sentence rather than silence.
  it('answers a failed search with a state and a sentence, never silence', async () => {
    await open({
      'image/search': () => {
        throw new Error('boom');
      },
    });
    await msg({ type: 'searchImage', token: 1, query: 'postgres' });
    const posted = postsOf('imageSearch')[0];
    assert.ok(posted, 'a failed search said nothing at all');
    assert.notEqual(posted.answer.state, 'ok');
    assert.match(posted.answer.message, /still type any image reference/i);
    // The reader is not shown a failure banner for a search that did not work.
    assert.deepEqual(postsOf('failure'), []);
  });
});

describe('the reader’s own switch — composure.dockerHub', () => {
  it('makes no request at all when it is off, and says so only when asked', async () => {
    const previous = vscodeStub.workspace.getConfiguration;
    vscodeStub.workspace.getConfiguration = () => ({ get: () => 'off' });
    try {
      await open({ 'stack/schema': () => SCHEMA_WITH_IMAGE, 'image/lookup': () => LOOKUP_OK });
      await settle(20);
      assert.deepEqual(
        rec.requests.filter((r) => r.method.startsWith('image/')),
        [],
        'the panel talked to Docker Hub with the setting off',
      );
      // …and the inspector is untouched: exactly the pane that shipped before
      // this epic, not a pane covered in "switched off" notes.
      assert.deepEqual(postsOf('imageLookup'), []);

      // A search IS answered, because the reader explicitly asked for one and a
      // control that silently does nothing is worse than one that says why.
      await msg({ type: 'searchImage', token: 3, query: 'postgres' });
      const posted = postsOf('imageSearch')[0];
      assert.equal(posted.answer.state, 'disabled');
      assert.match(posted.answer.message, /switched off/i);
      assert.deepEqual(
        rec.requests.filter((r) => r.method.startsWith('image/')),
        [],
      );
    } finally {
      vscodeStub.workspace.getConfiguration = previous;
    }
  });
});

/* -------------------------------------------------------------------------
 * Epic 9, story 9.2 — one entry of a list, from the host's side.
 * ---------------------------------------------------------------------- */

describe('a list entry is spliced, never inserted — story 9.2', () => {
  // MUTATION: the `isEntryPath` branch removed from `stageValue`. The path is
  // not in `declared` — the wire schema carries no path for a sequence entry —
  // so it falls to the insert branch and stages
  // `insert_key at services.web.command key "2"`, which on a SEQUENCE adds
  // nothing at all. A confident wrong answer with a plan attached, which is
  // exactly the hole DECISIONS.md 24 is about.
  it('stages a replace_scalar for an entry the wire schema never named', async () => {
    await open();
    rec.requests = [];
    await msg({ type: 'edit', path: 'services.web.command[2]', value: 'bash' });
    const previewed = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.ok(previewed.length >= 1, 'the entry edit was not previewed at all');
    assert.deepEqual(previewed[0].params.ops, [
      { operation: 'replace_scalar', at: 'services.web.command[2]', value: 'bash' },
    ]);
  });

  it('appends an entry rather than naming a position the list has not got', async () => {
    await open();
    rec.requests = [];
    await msg({ type: 'addEntry', path: 'services.web.command', value: 'echo hi' });
    const previewed = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.deepEqual(previewed[0].params.ops, [
      { operation: 'insert_sequence_entry', at: 'services.web.command', value: 'echo hi' },
    ]);
  });

  it('stages two different additions to one list and replaces a restage of the same one', async () => {
    await open();
    await msg({ type: 'addEntry', path: 'services.web.command', value: 'echo hi' });
    await msg({ type: 'addEntry', path: 'services.web.command', value: 'echo bye' });
    rec.requests = [];
    await msg({ type: 'addEntry', path: 'services.web.command', value: 'echo bye' });
    const ops = rec.requests.filter((r) => r.method === 'stack/preview')[0].params.ops;
    assert.deepEqual(
      ops.map((o: any) => o.value),
      ['echo hi', 'echo bye'],
      'the same entry staged twice became two entries',
    );
  });

  it('removes the entry that was named, and nothing around it', async () => {
    await open();
    rec.requests = [];
    await msg({ type: 'removeEntry', path: 'services.web.command[1]' });
    const previewed = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.deepEqual(previewed[0].params.ops, [
      { operation: 'delete_key', at: 'services.web.command[1]' },
    ]);
  });

  /* Adding a key to a free-form mapping. `insert_key` is the same operation
   * `stageValue` uses for a key the file does not have, addressed at the
   * MAPPING rather than at the leaf — so the pane invents no new engine
   * capability and the diff is one line. Nothing is written: `Save to <file>`
   * still does that and it is still the only control that does. */
  it('inserts a key into the mapping the reader was looking at', async () => {
    await open();
    rec.requests = [];
    await msg({
      type: 'addKey',
      path: 'services.web.environment',
      key: 'LOG_LEVEL',
      value: 'debug',
    });
    const previewed = rec.requests.filter((r) => r.method === 'stack/preview');
    assert.deepEqual(previewed[0].params.ops, [
      {
        operation: 'insert_key',
        at: 'services.web.environment',
        key: 'LOG_LEVEL',
        value: 'debug',
      },
    ]);
  });

  it('keys the stage by the path it creates, so restaging one key is not two keys', async () => {
    await open();
    await msg({ type: 'addKey', path: 'services.web.environment', key: 'LOG_LEVEL', value: 'debug' });
    await msg({ type: 'addKey', path: 'services.web.environment', key: 'TZ', value: 'UTC' });
    rec.requests = [];
    await msg({ type: 'addKey', path: 'services.web.environment', key: 'LOG_LEVEL', value: 'trace' });
    const ops = rec.requests.filter((r) => r.method === 'stack/preview')[0].params.ops;
    assert.deepEqual(
      ops.map((o: any) => [o.key, o.value]),
      [['LOG_LEVEL', 'trace'], ['TZ', 'UTC']],
      'the same key staged twice became two keys',
    );
  });
});

/* -------------------------------------------------------------------------
 * Epic 9, story 9.1 — comments.
 *
 * Every fixture here carries SEVERAL comments. A file with one comment in it
 * cannot tell a read of the right run from a read of the only run, which is
 * the trap the story names by hand.
 * ---------------------------------------------------------------------- */

// The refusal has to arrive as the core's own error type for `classify` and
// `reasonOf` to read it, so the prototype is borrowed from the module that
// defines it — the same trick story 6.1's suite uses above.
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { RpcError: RpcErrorClass } = require('./core') as typeof import('./core');
const refusal = (message: string, reason: string): Error => {
  const err: any = new Error(message);
  err.code = -32002;
  err.data = { reason };
  Object.setPrototypeOf(err, RpcErrorClass.prototype);
  return err;
};

/** `CODE_STALE_RANGE`: the bytes moved since the preview the client recorded. */
const staleRange = (message: string): Error => {
  const err: any = new Error(message);
  err.code = -32003;
  Object.setPrototypeOf(err, RpcErrorClass.prototype);
  return err;
};

/**
 * A core that knows which key carries which comment, and refuses the rest by
 * name — the way the real one does.
 */
const commentCore = (
  carried: Record<string, { above?: string; trailing?: string }>,
  refuse: Record<string, string> = {},
) => ({
  'stack/preview': (params: any) => {
    const op = params.ops[params.ops.length - 1];
    if (op.operation !== 'delete_comment' && op.operation !== 'set_comment') {
      return { diff: '-a\n+b\n', added: 1, removed: 1, ops: params.ops.map(() => ({ range: { start: 0, end: 1, line: 1 }, before: '' })) };
    }
    if (op.operation === 'set_comment') {
      return { diff: '+# x\n', added: 1, removed: 0, ops: [{ range: { start: 0, end: 0, line: 1 }, before: '' }] };
    }
    const slug = refuse[`${op.where}:${op.at}`];
    if (slug !== undefined) {
      throw refusal(`the core declined: ${op.where} of ${op.at}`, slug);
    }
    const text = carried[op.at]?.[op.where as 'above' | 'trailing'];
    if (text === undefined) {
      throw refusal('there is no comment at that position', 'no-comment');
    }
    return {
      diff: `-${text}\n`,
      added: 0,
      removed: 1,
      ops: [{ range: { start: 0, end: text.length, line: 3 }, before: text }],
    };
  },
});

describe('a comment is a thing the reader can read and write — story 9.1', () => {
  // MUTATION: `readComments` answering only `above`. The trailing comment on
  // `image: nginx:1.27   # pinned deliberately` is the one the reader in the
  // owner's file actually has, and the block would offer an empty field over
  // the top of it — which, staged, REPLACES their sentence with whatever they
  // typed into what looked like a blank.
  it('reads both positions out of the engine’s own bytes', async () => {
    await open(
      commentCore({
        'services.web.image': { above: '    # one\n    # two\n', trailing: '# pinned deliberately' },
      }),
    );
    await msg({ type: 'openComment', path: 'services.web.image' });
    const posted = postsOf('comments').pop();
    assert.ok(posted, 'nothing was posted for the comment the reader asked to see');
    assert.equal(posted.path, 'services.web.image');
    assert.equal(posted.above, 'one\ntwo');
    assert.equal(posted.trailing, 'pinned deliberately');
  });

  // The other half, and the one a single-comment fixture cannot see: the run
  // that belongs to ANOTHER key must not be reported as this key's.
  it('reports no comment where the engine says there is none', async () => {
    await open(
      commentCore({
        'services.web.restart': { above: '    # about restart\n' },
        'services.web.image': { trailing: '# pinned' },
      }),
    );
    await msg({ type: 'openComment', path: 'services.web.image' });
    const posted = postsOf('comments').pop();
    assert.equal(posted.above, null, 'a comment belonging to another key was offered as this one’s');
    assert.equal(posted.trailing, 'pinned');
  });

  // Rule 6. A block scalar cannot carry a trailing comment and the engine says
  // so by name; the pane must pass that on rather than showing an empty field
  // that would be refused after the reader had typed in it.
  it('carries the engine’s refusal for a position it cannot write', async () => {
    await open(
      commentCore({ 'services.web.image': { above: '# a\n' } }, { 'trailing:services.web.image': 'comment-target' }),
    );
    await msg({ type: 'openComment', path: 'services.web.image' });
    const posted = postsOf('comments').pop();
    assert.equal(posted.trailing, null);
    const said = (posted.unavailable ?? []).find((u: any) => u.where === 'trailing');
    assert.ok(said, 'a position the engine refuses was offered as an empty field');
    assert.match(said.detail, /cannot carry a comment/i);
  });

  it('stages a comment at the position it was written for, and both positions at once', async () => {
    await open(commentCore({}));
    await msg({ type: 'setComment', path: 'services.web.image', where: 'above', text: 'why this pin' });
    rec.requests = [];
    await msg({ type: 'setComment', path: 'services.web.image', where: 'trailing', text: 'ops' });
    // `expect` is stripped: it is the byte range the stage is held against and
    // it is `stageAll`'s business, asserted where AD-19 is.
    const ops = rec.requests
      .filter((r) => r.method === 'stack/preview')
      .pop()!
      .params.ops.map(({ expect, ...op }: any) => op);
    assert.deepEqual(ops, [
      { operation: 'set_comment', at: 'services.web.image', where: 'above', value: 'why this pin' },
      { operation: 'set_comment', at: 'services.web.image', where: 'trailing', value: 'ops' },
    ]);
  });

  it('shows the staged text rather than the file’s, and says it is staged', async () => {
    await open(
      commentCore({ 'services.web.image': { above: '# what the file says\n' } }),
    );
    await msg({ type: 'setComment', path: 'services.web.image', where: 'above', text: 'what I typed' });
    const posted = postsOf('comments').pop();
    assert.equal(posted.above, 'what I typed', 'the pane would show the file and lose the edit');
    assert.deepEqual(posted.staged, ['above']);
  });

  it('stages a delete rather than a set with nothing in it', async () => {
    await open(commentCore({ 'services.web.image': { above: '# gone soon\n' } }));
    rec.requests = [];
    await msg({ type: 'deleteComment', path: 'services.web.image', where: 'above' });
    const ops = rec.requests.filter((r) => r.method === 'stack/preview')[0].params.ops;
    assert.deepEqual(ops, [
      { operation: 'delete_comment', at: 'services.web.image', where: 'above' },
    ]);
  });
});

/* -------------------------------------------------------------------------
 * Epic 9, story 9.3 — moving a value into a variable.
 *
 * The first operation in this product whose blast radius is larger than the
 * file the reader is looking at, so every check here is about the pair: both
 * diffs before anything is written, and one press that performs both.
 * ---------------------------------------------------------------------- */

const EXTRACTED = {
  name: 'POSTGRES_PASSWORD',
  value: 'hunter2',
  compose: {
    file: ENTRY,
    ops: [{ operation: 'replace_scalar', path: 'services.web.environment.POSTGRES_PASSWORD', range: { start: 1, end: 8, line: 5 }, before: 'hunter2', describe: '' }],
    diff: '--- a/compose.yaml\n+++ b/compose.yaml\n-      POSTGRES_PASSWORD: hunter2\n+      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}\n',
    added: 1,
    removed: 1,
    changed_lines: 2,
    written: false,
  },
  env_file: '/w/.env',
  env_diff: '--- a/.env\n+++ b/.env\n+POSTGRES_PASSWORD=hunter2\n',
  env_line: 'POSTGRES_PASSWORD=hunter2',
  env_created: true,
  env_unchanged: false,
  // Story 9.6: the `.env` half AS IT STANDS at preview time. The apply sends it
  // back, and the core refuses if the variable has moved under it since.
  env_expect: { defined: false },
  written: false,
};

const AT = 'services.web.environment.POSTGRES_PASSWORD';

const extractCore = (over: Record<string, (params: any) => unknown> = {}) => ({
  'stack/extract': (params: any) => ({ ...EXTRACTED, name: params.name || EXTRACTED.name }),
  'stack/extract-apply': () => ({ ...EXTRACTED, written: true }),
  ...over,
});

describe('a value moved into a variable — story 9.3', () => {
  it('shows both diffs and writes neither file', async () => {
    await open(extractCore());
    rec.requests = [];
    await msg({ type: 'openExtract', path: AT });
    const asked = rec.requests.filter((r) => r.method.startsWith('stack/extract'));
    assert.deepEqual(
      asked.map((r) => r.method),
      ['stack/extract'],
      'looking at what a move would do reached the writing method',
    );
    assert.deepEqual(asked[0].params, { file: ENTRY, at: AT });

    const posted = postsOf('extract').pop();
    assert.ok(posted, 'nothing was posted for the move the reader asked about');
    assert.equal(posted.result.name, 'POSTGRES_PASSWORD');
    // BOTH. A two-file operation that showed one diff would be a lie about the
    // half the reader cannot see.
    assert.match(posted.result.compose.diff, /\$\{POSTGRES_PASSWORD\}/);
    assert.match(posted.result.env_diff, /POSTGRES_PASSWORD=hunter2/);
    assert.equal(posted.result.env_created, true);
    assert.equal(posted.staged, false);
  });

  it('asks again with the name the reader edited', async () => {
    await open(extractCore());
    await msg({ type: 'openExtract', path: AT, name: 'DB_PASSWORD' });
    const asked = rec.requests.filter((r) => r.method === 'stack/extract').pop()!;
    assert.equal(asked.params.name, 'DB_PASSWORD');
    assert.equal(postsOf('extract').pop()!.result.name, 'DB_PASSWORD');
  });

  it('says why it will not, rather than offering a move that would be refused', async () => {
    await open(
      extractCore({
        'stack/extract': () => {
          throw refusal('services.web.image is already ${IMAGE}', 'already-interpolated');
        },
      }),
    );
    await msg({ type: 'openExtract', path: 'services.web.image' });
    const posted = postsOf('extract').pop();
    assert.ok(posted, 'a refused move said nothing at all');
    assert.equal(posted.result, undefined, 'a refused move still offered a diff');
    assert.match(posted.refused, /already comes from a variable/);
  });

  // MUTATION: the `env` half dropped from the pending message. The strip shows
  // the compose diff alone and `Save to compose.yaml and .env` writes a second
  // file the reader was never shown — which is the exact thing DECISIONS.md 25
  // says the preview exists to prevent.
  it('stages the move as a pending change carrying both diffs', async () => {
    await open(extractCore());
    await msg({ type: 'openExtract', path: AT });
    rec.posted = [];
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    const pending = postsOf('pending').pop();
    assert.ok(pending, 'staging a move staged nothing');
    assert.match(pending.diff, /\$\{POSTGRES_PASSWORD\}/);
    assert.ok(pending.env, 'the .env half of a two-file change was not shown');
    assert.match(pending.env.diff, /POSTGRES_PASSWORD=hunter2/);
    assert.match(pending.env.note, /creates/i);
    // `Save to <file>` is no longer literally one file, and the control says so.
    assert.match(pending.saveLabel, /compose\.yaml and \.env/);
  });

  it('writes both files through the one method that can, and only on save', async () => {
    await open(extractCore());
    await msg({ type: 'openExtract', path: AT });
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    rec.requests = [];
    await msg({ type: 'save' });
    const wrote = rec.requests.filter((r) => r.method.startsWith('stack/'));
    assert.ok(
      wrote.some((r) => r.method === 'stack/extract-apply'),
      `the save did not reach the two-file write: ${wrote.map((r) => r.method).join(', ')}`,
    );
    assert.equal(
      wrote.some((r) => r.method === 'stack/apply'),
      false,
      'the move was written through the one-file path, which cannot write the .env',
    );
    assert.equal(postsOf('pendingCleared').length > 0, true);
  });

  /* ---- story 9.6: the two-file write joins the staleness contract --------
   *
   * D1/D4. The client recorded a preview and then applied WITHOUT sending it
   * back, so the core applied against whatever the files said at write time.
   * PROTOCOL_REVISION 9 exists precisely to keep that client out — its stated
   * justification is that a client not sending these gets "no staleness
   * protection at all on the widest write in the product" — and the shipped
   * extension was that client, inside the handshake's own boundary.
   *
   * MUTATION: drop `expect` from the apply params and this test fails while
   * every other check in the suite still passes, because nothing else looks at
   * what the write asserts.
   */
  it('sends back the byte range it was shown, so a moved file refuses', async () => {
    await open(extractCore());
    await msg({ type: 'openExtract', path: AT });
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    rec.requests = [];
    await msg({ type: 'save' });
    const write = rec.requests.find((r) => r.method === 'stack/extract-apply')!;
    assert.ok(write, 'the save did not reach the two-file write');
    // Recorded from the preview's own operation — `range` and `before`, which
    // is exactly what `internal/edit`'s own staleness tests record.
    assert.deepEqual(
      write.params.expect,
      { start: 1, end: 8, text: 'hunter2' },
      `the compose half was written with no assertion: ${JSON.stringify(write.params)}`,
    );
  });

  it('sends back the .env expectation, which is about the variable and not a range', async () => {
    await open(extractCore());
    await msg({ type: 'openExtract', path: AT });
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    rec.requests = [];
    await msg({ type: 'save' });
    const write = rec.requests.find((r) => r.method === 'stack/extract-apply')!;
    // The `.env` edit is one appended line and has no range to compare, so its
    // assertion is a different SHAPE — `{ defined, value }`, passed back
    // verbatim from the preview rather than rebuilt here.
    assert.deepEqual(
      write.params.expect_env,
      { defined: false },
      `the .env half was written with no assertion: ${JSON.stringify(write.params)}`,
    );
  });

  it('discards the move and refills when the core says the range moved', async () => {
    await open(
      extractCore({
        'stack/extract-apply': () => {
          throw staleRange('the bytes at that range are not what you were shown');
        },
      }),
    );
    await msg({ type: 'openExtract', path: AT });
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    rec.posted = [];
    await msg({ type: 'save' });
    // AD-19: a stale stage is DISCARDED rather than retried, and the reader is
    // told. Nothing was written, and the pane is re-derived from the file that
    // actually exists.
    // AD-19's own words to the reader. A plain successful save ALSO posts
    // `pendingCleared`, and the refill afterwards posts another with no reason,
    // so the REASON is the whole assertion: without it this would pass against
    // a write that silently succeeded.
    const told = postsOf('pendingCleared').filter((m: any) => m.reason);
    assert.equal(
      told.length,
      1,
      `a stale two-file write did not tell the reader: ${JSON.stringify(postsOf('pendingCleared'))}`,
    );
    assert.match(told[0].reason, /chang|stale|moved/i);
    assert.equal(
      postsOf('editRefused').length,
      0,
      'a stale range was reported as a refusal rather than handled as staleness',
    );
  });

  // The two cannot be mixed and the reason is arithmetic, not taste: an
  // extract computes its byte ranges from the file ON DISK, and an ordinary
  // stage holds ranges against the same bytes. Whichever went first would
  // invalidate the other, and a partial write here is a stack that no longer
  // starts.
  it('refuses to mix a move with an ordinary staged edit, in both orders', async () => {
    await open(extractCore());
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    rec.posted = [];
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    const first = postsOf('editRefused').pop();
    assert.ok(first, 'a move was staged on top of an edit it would invalidate');
    assert.match(first.detail, /on its own/i);

    await msg({ type: 'discard' });
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    rec.posted = [];
    await msg({ type: 'edit', path: 'services.web.image', value: 'nginx:1.28' });
    const second = postsOf('editRefused').pop();
    assert.ok(second, 'an edit was staged on top of a move it would invalidate');
    assert.match(second.detail, /on its own/i);
  });

  it('drops a staged move on discard, and says the strip is empty', async () => {
    await open(extractCore());
    await msg({ type: 'openExtract', path: AT });
    await msg({ type: 'stageExtract', path: AT, name: 'POSTGRES_PASSWORD' });
    rec.posted = [];
    await msg({ type: 'discard' });
    assert.ok(postsOf('pendingCleared').length >= 1, 'the strip did not say the move was gone');
    assert.equal(postsOf('pending').length, 0, 'the strip still showed the discarded move');
    rec.requests = [];
    await msg({ type: 'save' });
    assert.deepEqual(
      rec.requests.filter((r) => r.method.startsWith('stack/extract')),
      [],
      'a discarded move was still written',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 9.4: a literal moved into a build argument.
 *
 * The Dockerfile half of the same gesture, and NOT the compose half with a
 * different parser. It writes ONE file, it reports a scope, and everything it
 * is asked about is addressed by an instruction index against the DOCKERFILE —
 * not by a path against the compose file the reader arrived through.
 * ---------------------------------------------------------------------- */

/** What the core answers for `FROM node:18` in the SECOND stage of two. */
const ARG_EXTRACTED = {
  name: 'NODE_VERSION',
  value: '18',
  dockerfile: {
    file: DOCKERFILE,
    // The substitution and the insertion, in the order the engine emits them.
    // The FIRST is the one carrying an assertable range: `insert_instruction`
    // has an empty range by definition, and asserting an empty range asserts
    // nothing. Story 9.6 records the substitution's.
    ops: [
      {
        operation: 'set_base_image',
        range: { start: 5, end: 12, line: 1 },
        before: 'node:18',
        describe: 'set the base image of stage 0 to node:${NODE_VERSION}',
      },
      {
        operation: 'insert_instruction_before',
        range: { start: 0, end: 0, line: 1 },
        before: '',
        describe: 'add ARG NODE_VERSION=18 above instruction 0',
      },
    ],
    diff:
      '--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1,3 +1,4 @@\n+ARG NODE_VERSION=18\n' +
      '-FROM node:18\n+FROM node:${NODE_VERSION}\n',
    added: 2,
    removed: 1,
    changed_lines: 3,
    written: false,
  },
  scope: 'global',
  scope_reason:
    'a FROM can only use an ARG declared before the FIRST FROM, so the declaration went above ' +
    'line 1 and could not go anywhere else',
  arg_line: 'ARG NODE_VERSION=18',
  declared: true,
  redeclared: false,
  already_declared: false,
  compose_note: 'Nothing feeds `NODE_VERSION` from compose.',
  written: false,
};

/** The instruction the literal comes out of: the SECOND stage's FROM. */
const ARG_AT = 15;

const argCore = (over: Record<string, (params: any) => unknown> = {}) => ({
  'stack/extract-arg': (params: any) => ({
    ...ARG_EXTRACTED,
    name: params.name || ARG_EXTRACTED.name,
  }),
  'stack/extract-arg-apply': () => ({ ...ARG_EXTRACTED, written: true }),
  ...over,
});

describe('a literal moved into a build argument — story 9.4', () => {
  /** The panel, with the Dockerfile view up — every message below is about it. */
  async function openDockerfile(over: Record<string, (params: any) => unknown> = {}) {
    const panel = await open(argCore(over));
    await msg({ type: 'openDockerfile', id: 'services.web.build' });
    return panel;
  }

  // MUTATION: `this.editTarget()` → `this.file` on the `openExtractArg` arm.
  // The question is then asked about the COMPOSE file, which the core answers
  // by refusing it as the wrong grammar — a feature that never works, and one
  // no assertion about "something was asked" can see.
  it('asks about the Dockerfile, at the instruction, and writes nothing', async () => {
    await openDockerfile();
    rec.requests = [];
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    const asked = rec.requests.filter((r) => r.method.startsWith('stack/extract-arg'));
    assert.deepEqual(
      asked.map((r) => r.method),
      ['stack/extract-arg'],
      'looking at what a move would do reached the writing method',
    );
    assert.deepEqual(asked[0].params, { file: DOCKERFILE, instruction: ARG_AT });

    const posted = postsOf('extractArg').pop();
    assert.ok(posted, 'nothing was posted for the move the reader asked about');
    assert.equal(posted.file, DOCKERFILE);
    assert.equal(posted.instruction, ARG_AT, 'the answer names another instruction');
    assert.equal(posted.staged, false);
  });

  // MUTATION: `scope`, `scope_reason` or `compose_note` stripped from the
  // posted answer. The reader gets a diff with an `ARG` in it and no statement
  // of which scope it landed in, why it could not be elsewhere, or that nothing
  // feeds it from compose — the three things DECISIONS.md 27 exists to say.
  it('carries the scope, the reason and the compose note through untouched', async () => {
    await openDockerfile();
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    const posted = postsOf('extractArg').pop()!;
    assert.equal(posted.result.scope, 'global');
    assert.equal(posted.result.scope_reason, ARG_EXTRACTED.scope_reason);
    assert.equal(posted.result.compose_note, ARG_EXTRACTED.compose_note);
    assert.equal(posted.result.declared, true);
    assert.equal(posted.result.redeclared, false);
    assert.equal(posted.result.already_declared, false);
  });

  it('asks again with the name the reader edited', async () => {
    await openDockerfile();
    await msg({ type: 'openExtractArg', instruction: ARG_AT, name: 'NODE_TAG' });
    const asked = rec.requests.filter((r) => r.method === 'stack/extract-arg').pop()!;
    assert.equal(asked.params.name, 'NODE_TAG');
    assert.equal(postsOf('extractArg').pop()!.result.name, 'NODE_TAG');
  });

  it('says why it will not, rather than offering a move that would be refused', async () => {
    await openDockerfile({
      'stack/extract-arg': () => {
        throw refusal('that FROM is pinned by digest', 'no-tag');
      },
    });
    rec.posted = [];
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    const posted = postsOf('extractArg').pop();
    assert.ok(posted, 'a refused move said nothing at all');
    assert.equal(posted.result, undefined, 'a refused move still offered a diff');
    assert.match(posted.refused, /digest is not a tag/);
  });

  // MUTATION: the pending message given an `env` half, or a `saveLabel` naming
  // two files. This operation writes ONE file — a `.env` line would look like
  // configuration and be inert, which is exactly what DECISIONS.md 27 refuses —
  // and a strip that named a second file would promise a write that never comes.
  it('stages it as one pending change against the Dockerfile alone', async () => {
    await openDockerfile();
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    rec.posted = [];
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    const pending = postsOf('pending').pop();
    assert.ok(pending, 'staging the move staged nothing');
    assert.equal(pending.file, DOCKERFILE);
    assert.match(pending.diff, /ARG NODE_VERSION=18/);
    assert.equal(pending.env, undefined, 'a one-file move offered a second file’s diff');
    assert.match(pending.saveLabel, /Dockerfile/);
    assert.doesNotMatch(pending.saveLabel, /\.env/);
    assert.equal(
      rec.requests.some((r) => r.method === 'stack/extract-arg-apply'),
      false,
      'staging the move wrote it',
    );
    assert.equal(postsOf('extractArg').pop().staged, true, 'the block does not say it is staged');
  });

  it('writes through the method that decides the placement, and only on save', async () => {
    await openDockerfile();
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    rec.requests = [];
    await msg({ type: 'save' });
    const wrote = rec.requests.filter((r) => r.method.startsWith('stack/'));
    assert.ok(
      wrote.some((r) => r.method === 'stack/extract-arg-apply'),
      `the save did not reach the move: ${wrote.map((r) => r.method).join(', ')}`,
    );
    assert.equal(
      wrote.some((r) => r.method === 'stack/apply'),
      false,
      'the move went through the ordinary write, which does not decide where the ARG may go',
    );
    assert.equal(postsOf('pendingCleared').length > 0, true);
  });

  /* ---- story 9.6, the other grammar ------------------------------------
   *
   * D1/D4. `ExtractArg.Expect` has been plumbed through the core since story
   * 9.4 and its own doc says sending nothing is "wrong for a staged UI edit
   * (AD-19)" — and the extension sent nothing, which made `panel.ts`'s
   * `classify(err) === 'stale'` branch on this path unreachable code.
   *
   * MUTATION: drop `expect` from the apply params and this fails alone.
   */
  it('sends back the substitution’s byte range, so a moved Dockerfile refuses', async () => {
    await openDockerfile();
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    rec.requests = [];
    await msg({ type: 'save' });
    const write = rec.requests.find((r) => r.method === 'stack/extract-arg-apply')!;
    assert.ok(write, 'the save did not reach the move');
    // The SUBSTITUTION's range, not the insertion's. An insert has an empty
    // range and asserting `[0,0)` against `''` passes against any file at all —
    // a check that could not fail, which is the shape this review was hunting.
    assert.deepEqual(
      write.params.expect,
      { start: 5, end: 12, text: 'node:18' },
      `the ARG move was written with no assertion: ${JSON.stringify(write.params)}`,
    );
  });

  it('discards the move and refills when the core says the range moved', async () => {
    await openDockerfile({
      'stack/extract-arg-apply': () => {
        throw staleRange('the bytes at that range are not what you were shown');
      },
    });
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    rec.posted = [];
    await msg({ type: 'save' });
    const told = postsOf('pendingCleared').filter((m: any) => m.reason);
    assert.equal(
      told.length,
      1,
      `a stale ARG move did not tell the reader: ${JSON.stringify(postsOf('pendingCleared'))}`,
    );
    assert.match(told[0].reason, /chang|stale|moved/i);
    assert.equal(
      postsOf('editRefused').length,
      0,
      'a stale range was reported as a refusal rather than handled as staleness',
    );
  });

  // The same arithmetic as story 9.3, in the other grammar: the substitution's
  // byte range and every staged `expect` are computed from the same bytes on
  // disk, so whichever was written first would invalidate the other.
  it('refuses to mix the move with an ordinary Dockerfile edit, in both orders', async () => {
    await openDockerfile();
    await msg({ type: 'editStage', stage: 1, value: 'alpine:3.21' });
    rec.posted = [];
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    const first = postsOf('editRefused').pop();
    assert.ok(first, 'a move was staged on top of an edit it would invalidate');
    assert.match(first.detail, /on its own/i);

    await msg({ type: 'discard' });
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    rec.posted = [];
    await msg({ type: 'editStage', stage: 1, value: 'alpine:3.21' });
    const second = postsOf('editRefused').pop();
    assert.ok(second, 'an edit was staged on top of a move it would invalidate');
    assert.match(second.detail, /on its own/i);
  });

  it('drops a staged move on discard, and says the strip is empty', async () => {
    await openDockerfile();
    await msg({ type: 'openExtractArg', instruction: ARG_AT });
    await msg({ type: 'stageExtractArg', instruction: ARG_AT, name: 'NODE_VERSION' });
    rec.posted = [];
    await msg({ type: 'discard' });
    assert.ok(postsOf('pendingCleared').length >= 1, 'the strip did not say the move was gone');
    rec.requests = [];
    await msg({ type: 'save' });
    assert.deepEqual(
      rec.requests.filter((r) => r.method.startsWith('stack/extract-arg')),
      [],
      'a discarded move was still written',
    );
  });
});
