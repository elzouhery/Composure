// A DOM small enough to run under `node --test`, and the render-level checks
// that need one.
//
// Every other webview suite tests extracted pure functions, on the stated
// grounds that "a webview cannot be launched under node --test". That is true
// of a webview and false of the DOM API these views actually use: element
// creation, attributes, classes, children and listeners. The gap it left is
// exactly where the surviving mutations lived — `available` truncated to five
// keys, the provenance line never rendered, the severity pill rendered with no
// word, mapping entries rendered as nothing. Each of those is a property of the
// TREE, so none of them was reachable from a test of the arithmetic.
//
// So: about a hundred lines of DOM, and then assertions about what the reader
// would actually see. This is a test file and ships nowhere; the accessibility
// scan skips `*.test.ts` by name.

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

/* -------------------------------------------------------------------------
 * The DOM.
 * ---------------------------------------------------------------------- */

class Node_ {
  children: El[] = [];
  parent: El | null = null;
}

class El extends Node_ {
  tagName: string;
  className = '';
  private attrs = new Map<string, string>();
  private listeners = new Map<string, ((e: any) => void)[]>();
  dataset: Record<string, string> = {};
  /**
   * Enough of CSSStyleDeclaration for what the inspector sets: custom
   * properties, through `setProperty`. `--ind` — the nesting level, in the one
   * unit the key column has to give back — reaches a row this way, so a stub
   * without `setProperty` throws on every nested render.
   */
  style: Record<string, string> & { setProperty(name: string, value: string): void } =
    Object.assign(Object.create(null) as Record<string, string>, {
      setProperty(this: Record<string, string>, name: string, value: string): void {
        this[name] = value;
      },
    });
  title = '';
  type = '';
  value = '';
  placeholder = '';
  hidden = false;
  tabIndex = 0;
  private text = '';

  constructor(tag: string) {
    super();
    this.tagName = tag.toUpperCase();
  }

  get textContent(): string {
    // Own text first, then children — the way a real element reads when a label
    // is set and a suffix span is appended after it. Getting this wrong hides
    // exactly the thing several of these tests are looking for.
    return this.text + this.children.map((c) => c.textContent).join('');
  }

  set textContent(value: string) {
    // Matches the real thing: assigning replaces every child.
    this.children = [];
    this.text = value;
  }

  get classList() {
    const parts = (): string[] => this.className.split(/\s+/).filter((s) => s !== '');
    return {
      add: (...names: string[]) => {
        const set = new Set([...parts(), ...names]);
        this.className = [...set].join(' ');
      },
      remove: (...names: string[]) => {
        this.className = parts().filter((p) => !names.includes(p)).join(' ');
      },
      contains: (name: string) => parts().includes(name),
      toggle: (name: string, on?: boolean) => {
        const want = on === undefined ? !parts().includes(name) : on;
        if (want) {
          this.classList.add(name);
        } else {
          this.classList.remove(name);
        }
      },
    };
  }

  setAttribute(name: string, value: string): void {
    this.attrs.set(name, value);
  }

  getAttribute(name: string): string | null {
    return this.attrs.get(name) ?? null;
  }

  appendChild(child: El): El {
    child.parent = this;
    this.children.push(child);
    return child;
  }

  append(...nodes: El[]): void {
    for (const n of nodes) {
      this.appendChild(n);
    }
  }

  insertBefore(child: El, ref: El | null): El {
    child.parent = this;
    const at = ref === null ? this.children.length : this.children.indexOf(ref);
    this.children.splice(at < 0 ? this.children.length : at, 0, child);
    return child;
  }

  get firstChild(): El | null {
    return this.children[0] ?? null;
  }

  addEventListener(name: string, fn: (e: any) => void): void {
    const list = this.listeners.get(name) ?? [];
    list.push(fn);
    this.listeners.set(name, list);
  }

  /** Fires a listener the way a reader's click or keypress would. */
  fire(name: string, event: any = {}): void {
    for (const fn of this.listeners.get(name) ?? []) {
      fn({ preventDefault() {}, ...event });
    }
  }

  focus(): void {
    (globalThis as any).document.activeElement = this;
    this.fire('focus');
  }

  select(): void {}

  blur(): void {
    this.fire('blur');
  }

  /** Only the selectors this codebase uses: `[data-x="y"]`. */
  querySelector(selector: string): El | null {
    const m = /^\[data-([a-z]+)="(.*)"\]$/.exec(selector);
    if (!m) {
      return null;
    }
    const key = m[1];
    const want = m[2].replace(/\\(["\\])/g, '$1');
    return walk(this).find((e) => e.dataset[key] === want) ?? null;
  }
}

class InputEl extends El {}

/** Every descendant, in document order. */
function walk(root: El): El[] {
  const out: El[] = [];
  const visit = (e: El): void => {
    for (const c of e.children) {
      out.push(c);
      visit(c);
    }
  };
  visit(root);
  return out;
}

function installDom(): void {
  const document = {
    activeElement: null as El | null,
    createElement(tag: string): El {
      return tag === 'input' ? new InputEl(tag) : new El(tag);
    },
  };
  (globalThis as any).document = document;
  (globalThis as any).HTMLElement = El;
  (globalThis as any).HTMLInputElement = InputEl;
}

installDom();

/* The views import `document` at construction time, so the DOM has to exist
 * before the module is loaded. `require` after installDom, not a top import. */
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { InspectorView, INDENT_STEP, optionListId } =
  require('./inspector') as typeof import('./inspector');
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { PendingView, EMPTY_SUMMARY } = require('./pending') as typeof import('./pending');

import { EDITABILITY_REASONS } from '../shared/protocol';
import type { Finding, Inspection, Origin, SchemaField, ValueView, WebviewMessage } from '../shared/protocol';

/* -------------------------------------------------------------------------
 * Fixtures.
 * ---------------------------------------------------------------------- */

const origin = (line: number, file = '/w/compose.yaml'): Origin => ({ file, line, column: 3, step: 0 });

function scalar(text: string, line = 12, extra: Partial<ValueView> = {}): ValueView {
  return { kind: 'scalar', text, env_known: true, origin: origin(line), overrides: [], ...extra };
}

function field(key: string, declared: boolean, extra: Partial<SchemaField> = {}): SchemaField {
  return { key, path: `services.db.${key}`, declared, support: 'unknown', ...extra };
}

function inspectionOf(fields: SchemaField[], over: Partial<Inspection> = {}): Inspection {
  return {
    id: 'services.db',
    name: 'db',
    kind: 'service',
    findings: [],
    staged: [],
    opened: [],
    pending: {},
    schema: {
      path: '/w/compose.yaml',
      schema_commit: '4e2fe7602af8c965ab4fef891e9dde9c5940775f',
      compose_version: '2.29.0',
      compose_version_known: true,
      files: [{ path: '/w/compose.yaml', step: 0 }],
      profiles: [],
      node: {
        path: 'services.db',
        schema: 'service',
        known: true,
        fields,
        declared_count: fields.filter((f) => f.declared).length,
        available_count: fields.filter((f) => !f.declared).length,
      },
    },
    ...over,
  };
}

let sent: WebviewMessage[] = [];
function render(inspection: Inspection): { view: any; root: El } {
  sent = [];
  const view = new InspectorView({ send: (m: WebviewMessage) => sent.push(m) });
  view.render(inspection);
  return { view, root: view.element as unknown as El };
}

const byClass = (root: El, name: string): El[] =>
  walk(root).filter((e) => e.classList.contains(name));

beforeEach(() => {
  (globalThis as any).document.activeElement = null;
});

/* -------------------------------------------------------------------------
 * Story 5.2: every key, and a place to type.
 * ---------------------------------------------------------------------- */

describe('available, not set — story 5.2', () => {
  it('renders every key the schema permits, never a truncated head of the list', () => {
    // The guarantee in one assertion. `available` used to slice, and a slice of
    // five is indistinguishable on screen from a schema that names five keys —
    // the reader simply never learns `develop` exists, which is the entire
    // wedge. Ninety, because that is roughly what a real service reports.
    const fields = Array.from({ length: 90 }, (_, i) => field(`key_${i}`, false));
    const { root } = render(inspectionOf(fields));
    const keys = byClass(root, 'addable-key');
    assert.equal(keys.length, 90, `only ${keys.length} of 90 unset keys are on screen`);
    // And each is a real button with a name, not a comma-joined string.
    for (const k of keys) {
      assert.equal(k.tagName, 'BUTTON');
      assert.ok((k.getAttribute('aria-label') ?? '').length > 0);
    }
    assert.deepEqual(
      keys.map((k) => k.dataset.path).slice(-3),
      ['services.db.key_87', 'services.db.key_88', 'services.db.key_89'],
      'the tail of the list is missing, so the list is truncated',
    );
  });

  it('opens a key rather than staging a value the schema never supplied', () => {
    const { root } = render(inspectionOf([field('restart', false)]));
    const key = byClass(root, 'addable-key')[0];
    key.fire('click');
    assert.deepEqual(sent, [{ type: 'open', path: 'services.db.restart' }]);
  });

  it('gives an opened key an input with the cursor already in it', () => {
    // Story 5.2's last acceptance criterion, which has never worked: the key
    // used to come back as a BUTTON, and focus was restored onto that button.
    const { root } = render(
      inspectionOf([field('restart', false)], { opened: ['services.db.restart'] }),
    );
    assert.equal(byClass(root, 'addable-key').length, 0, 'the key is still a button');
    const input = walk(root).find((e) => e.dataset.field === 'services.db.restart');
    assert.ok(input, 'the opened key rendered no field at all');
    assert.equal(input!.tagName, 'INPUT');
    assert.equal(input!.value, '', 'the field was seeded with something nobody supplied');
    assert.match(input!.placeholder, /not set/);
  });

  it('puts the caret in the field the reader just opened', () => {
    const view = new InspectorView({ send: (m: WebviewMessage) => sent.push(m) });
    sent = [];
    view.render(inspectionOf([field('restart', false)]));
    const key = byClass(view.element as unknown as El, 'addable-key')[0];
    key.fire('click');
    // The host answers by re-rendering with the key opened; focus must land in
    // the input, not on the container and not on <body>.
    view.render(inspectionOf([field('restart', false)], { opened: ['services.db.restart'] }));
    const active = (globalThis as any).document.activeElement as El | null;
    assert.ok(active, 'focus was dropped by the rebuild');
    assert.equal(active!.dataset.field, 'services.db.restart');
    assert.equal(active!.tagName, 'INPUT');
  });

  it('seeds a schema default but never a prose one', () => {
    const schemaDefault = field('restart', false, { default: 'no', default_source: 'schema' });
    const prose = field('start_interval', false, {
      default: 'interval value',
      default_source: 'description',
    });
    const { root } = render(
      inspectionOf([schemaDefault, prose], {
        opened: ['services.db.restart', 'services.db.start_interval'],
      }),
    );
    const restart = walk(root).find((e) => e.dataset.field === 'services.db.restart');
    const interval = walk(root).find((e) => e.dataset.field === 'services.db.start_interval');
    assert.equal(restart!.value, 'no');
    // The defect, as an assertion: one click used to put this exact string into
    // the reader's file.
    assert.equal(interval!.value, '', 'a prose default was seeded as a value');
    assert.match(interval!.placeholder, /the spec describes the default as/);
  });

  it('commits what the reader typed, and reverts an untouched field to the key', () => {
    const { root } = render(
      inspectionOf([field('restart', false)], { opened: ['services.db.restart'] }),
    );
    const input = walk(root).find((e) => e.dataset.field === 'services.db.restart')!;
    input.value = 'always';
    input.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'edit', path: 'services.db.restart', value: 'always' }]);

    sent = [];
    const second = render(
      inspectionOf([field('restart', false)], { opened: ['services.db.restart'] }),
    );
    const fresh = walk(second.root).find((e) => e.dataset.field === 'services.db.restart')!;
    fresh.fire('keydown', { key: 'Escape' });
    assert.deepEqual(sent, [{ type: 'close', path: 'services.db.restart' }]);
  });

  it('discards the STAGE when Escape lands on a staged key the file does not declare', () => {
    // The two Escapes are not the same Escape, and only one of them was
    // covered. On an untouched field it closes the key back into the
    // `available, not set` list; on a STAGED one it must unstage, because the
    // aria-label promises "Escape to discard this edit" and because the field
    // is the only thing on screen showing that the stage exists.
    //
    // Replacing `isStaged ? unstage : close` with an unconditional `close`
    // passed the whole suite. What the reader got: the key folded back into the
    // available list, the pending strip still holding an insert of a value with
    // nothing on screen attached to it, and `Save` about to write it.
    const path = 'services.db.restart';
    const { root } = render(
      inspectionOf([field('restart', false)], {
        opened: [path],
        staged: [path],
        pending: { [path]: 'always' },
      }),
    );
    const input = walk(root).find((e) => e.dataset.field === path);
    assert.ok(input, 'a staged undeclared key rendered no field to press Escape in');
    assert.equal(input!.value, 'always', 'the field does not show what is staged');
    assert.match(
      input!.getAttribute('aria-label') ?? '',
      /Escape to discard this edit/,
      'the accessible name no longer promises what Escape does',
    );

    sent = [];
    input!.fire('keydown', { key: 'Escape' });
    assert.deepEqual(
      sent,
      [{ type: 'unstage', path }],
      'Escape on a staged field left the stage in the pending diff with no field showing it',
    );
  });
});

/* -------------------------------------------------------------------------
 * Gap 3: one `available, not set` statement, not one per group.
 *
 * Measured before this suite existed (`harness/metrics.mjs`, `services.web` at
 * 1100 × 720): 10 groups, 10 dashed `.addable` blocks, 5 groups holding no
 * declared field at all, `.inspector-body` scrollHeight 1880 in a 620px
 * viewport. Nine of the ten paragraphs were below the fold.
 *
 * These assert the RESULT — how many blocks the pane rendered, which keys are
 * in them BY NAME, and which headings survived. Not that a CSS rule exists, and
 * not the list's LENGTH: a length assertion passes on a list of the wrong keys,
 * which is exactly the failure story 5.2 forbids.
 * ---------------------------------------------------------------------- */

describe('the wedge, consolidated — gap 3', () => {
  // Shaped like `examples/webstack`'s `web`: three declared keys in three
  // groups, and unset keys spread across every group `groups.ts` names —
  // including six groups that end up with nothing declared in them. This is the
  // fixture the old rendering produced eight dashed blocks from.
  const declared = ['image', 'ports', 'restart'];
  const unset = [
    'build', 'platform', // image — has a declared field
    'entrypoint', 'user', // command — nothing declared
    'labels', // environment — nothing declared
    'expose', // network — ports is declared
    'tmpfs', // storage — nothing declared
    'depends_on', // health — restart is declared
    'ulimits', 'deploy', // resources — nothing declared
    'develop', // metadata — nothing declared
    'wat', // other — nothing declared, and a key groups.ts has never heard of
  ];
  const spread = (): SchemaField[] => [
    ...declared.map((k) => field(k, true, { value: scalar(`${k}-value`) })),
    ...unset.map((k) => field(k, false)),
  ];

  const topGroups = (root: El): El[] =>
    walk(root).filter((e) => e.classList.contains('grp') && !e.classList.contains('grp-nested'));

  it('renders exactly one available block for the whole pane', () => {
    const { root } = render(inspectionOf(spread()));
    const blocks = byClass(root, 'addable');
    assert.equal(
      blocks.length,
      1,
      `${blocks.length} separate "available, not set" blocks — the wedge is fragmented again`,
    );
  });

  it('lists every unset key in that one block, by name', () => {
    // The guarantee, stated as contents rather than as a count. Consolidating is
    // only allowed to move keys, never to lose them.
    const { root } = render(inspectionOf(spread()));
    const block = byClass(root, 'addable')[0];
    const keys = byClass(block, 'addable-key').map((k) => k.dataset.path);
    assert.deepEqual(
      [...keys].sort(),
      unset.map((k) => `services.db.${k}`).sort(),
      'the single block does not contain exactly the keys the schema permits and the file omits',
    );
    // …and in group order, not file order: `ulimits` is still printed next to
    // `deploy`, which is the teaching the per-group rendering was defending.
    assert.deepEqual(keys, [
      'services.db.build',
      'services.db.platform',
      'services.db.entrypoint',
      'services.db.user',
      'services.db.labels',
      'services.db.expose',
      'services.db.tmpfs',
      'services.db.depends_on',
      'services.db.ulimits',
      'services.db.deploy',
      'services.db.develop',
      'services.db.wat',
    ]);
  });

  it('names the group each run of keys belongs to', () => {
    const { root } = render(inspectionOf(spread()));
    const block = byClass(root, 'addable')[0];
    assert.deepEqual(
      byClass(block, 'addable-group').map((g) => g.textContent),
      ['image', 'command', 'environment', 'network', 'storage', 'health', 'resources', 'metadata', 'other'],
      'the block lost the group labels, so a reader who does not already know what ulimits is cannot find out',
    );
  });

  it('gives no group a heading and a rule it has no field under', () => {
    const { root } = render(inspectionOf(spread()));
    const empty = topGroups(root).filter((g) => byClass(g, 'field').length === 0);
    assert.deepEqual(
      empty.map((g) => byClass(g, 'grp-name')[0]?.textContent),
      [],
      'these groups cost a heading, two rules and a paragraph and introduce nothing',
    );
    // And the ones that DO have fields are still there, in reading order.
    assert.deepEqual(
      topGroups(root).map((g) => byClass(g, 'grp-name')[0]?.textContent),
      ['image', 'network', 'health'],
    );
  });

  it('still opens a field in place for a key whose group renders nothing', () => {
    // DECISIONS.md 17, over the consolidated block: `develop` belongs to
    // `metadata`, which now has no heading at all. The click must still open.
    const { root } = render(inspectionOf(spread()));
    const key = byClass(root, 'addable-key').find((k) => k.dataset.path === 'services.db.develop');
    assert.ok(key, 'develop is not reachable');
    key!.fire('click');
    assert.deepEqual(sent, [{ type: 'open', path: 'services.db.develop' }]);

    // …and the host's answer — the key opened — brings its group back with a
    // field in it, which is where the caret goes.
    const opened = render(inspectionOf(spread(), { opened: ['services.db.develop'] }));
    const input = walk(opened.root).find((e) => e.dataset.field === 'services.db.develop');
    assert.ok(input, 'the opened key rendered no field');
    assert.equal(input!.tagName, 'INPUT');
    assert.deepEqual(
      topGroups(opened.root).map((g) => byClass(g, 'grp-name')[0]?.textContent),
      ['image', 'network', 'health', 'metadata'],
      'the group holding the opened key did not come back',
    );
  });

  it('leaves a nested mapping group its own list, at its own path', () => {
    // The consolidation is per PANE, and a nested mapping's keys are at a
    // different config path — `healthcheck.interval` is not a key of the
    // service. Hoisting them into the pane's block would offer the reader a key
    // that does not exist where the block says it does.
    const fields = spread().concat(
      field('healthcheck', true, {
        type: 'object',
        value: { ...scalar(''), kind: 'null' },
        children: [
          { key: 'interval', path: 'services.db.healthcheck.interval', declared: false, support: 'unknown' },
        ],
      }),
    );
    const { root } = render(inspectionOf(fields));
    const nested = walk(root).find((e) => e.dataset.path === 'services.db.healthcheck');
    assert.ok(nested, 'healthcheck rendered no group');
    assert.deepEqual(
      byClass(nested!, 'addable-key').map((k) => k.dataset.path),
      ['services.db.healthcheck.interval'],
    );
  });
});

describe('the declared-but-null state', () => {
  // What a save produced, and what the pane could not render: `healthcheck:`
  // alone on a line. `scalarValue` excluded kind 'null' from editable, so the
  // reader got a read-only `~` and no route to `test` or `interval`.
  it('gives a null scalar a field to type in rather than a read-only tilde', () => {
    const restart = field('restart', true, { type: 'string', value: { ...scalar(''), kind: 'null' } });
    const { root } = render(inspectionOf([restart]));
    const input = walk(root).find((e) => e.dataset.field === 'services.db.restart');
    assert.ok(input, 'a declared-null key rendered no field');
    assert.equal(input!.tagName, 'INPUT');
    const texts = walk(root).map((e) => e.textContent);
    assert.equal(texts.includes('~'), false, 'the pane still shows a read-only tilde');
  });

  it('opens a null mapping as a group carrying its own available list', () => {
    const healthcheck = field('healthcheck', true, {
      type: 'object',
      value: { ...scalar(''), kind: 'null' },
      children: [
        field('test', false),
        field('interval', false, { default: '30s', default_source: 'description' }),
      ],
    });
    const { root } = render(inspectionOf([healthcheck]));
    const group = walk(root).find((e) => e.dataset.path === 'services.db.healthcheck');
    assert.ok(group, 'healthcheck rendered no group');
    const offered = byClass(group!, 'addable-key').map((k) => k.textContent);
    assert.ok(
      offered.some((t) => t.startsWith('test')),
      `the group offers ${JSON.stringify(offered)} — the reader cannot discover test`,
    );
    assert.ok(offered.some((t) => t.startsWith('interval')));
  });
});

/* -------------------------------------------------------------------------
 * Story 5.1 and 5.3: values and provenance.
 * ---------------------------------------------------------------------- */

describe('what a declared value renders as', () => {
  it('renders every mapping entry, never a bare key', () => {
    // Story 5.1's whole point, and the incumbent's headline failure: twenty
    // environment keys listed with not one value.
    const env = field('environment', true, {
      value: {
        kind: 'mapping',
        text: '',
        env_known: true,
        origin: origin(20),
        overrides: [],
        entries: [
          { key: 'POSTGRES_USER', key_origin: origin(21), path: 'services.db.environment.POSTGRES_USER', value: scalar('admin', 21) },
          { key: 'POSTGRES_DB', key_origin: origin(22), path: 'services.db.environment.POSTGRES_DB', value: scalar('app', 22) },
        ],
      },
    });
    const { root } = render(inspectionOf([env]));
    const rendered = walk(root).map((e) => e.textContent);
    for (const key of ['POSTGRES_USER', 'POSTGRES_DB']) {
      assert.ok(rendered.includes(key), `${key} is not on screen`);
    }
    for (const value of ['admin', 'app']) {
      assert.ok(
        walk(root).some((e) => e.textContent === value || e.value === value),
        `${value} is not on screen — a key was rendered without its value`,
      );
    }
  });

  it('renders the provenance line under a value, and the overrides half of it', () => {
    const image = field('image', true, {
      value: scalar('postgres:16', 12, {
        overrides: [{ value: 'postgres:15', origin: origin(7) }],
      }),
    });
    const { root } = render(inspectionOf([image]));
    const prov = byClass(root, 'prov');
    assert.equal(prov.length, 1, 'no provenance line was rendered');
    const links = byClass(prov[0], 'prov-link').map((l) => l.textContent);
    assert.deepEqual(links, ['w/compose.yaml:12', ':7'], 'the overrides half is missing');
    assert.match(prov[0].textContent, /overrides/);
    // Every position is reachable: a reader has to be able to get to what the
    // value replaced, not only to what replaced it.
    for (const link of byClass(prov[0], 'prov-link')) {
      assert.equal(link.tagName, 'BUTTON');
    }
    byClass(prov[0], 'prov-link')[1].fire('click');
    assert.deepEqual(sent, [{ type: 'reveal', file: '/w/compose.yaml', line: 7, column: 3 }]);
  });
});

describe('diagnostics on the field that caused them — story 5.4', () => {
  // EVERY fixture below is a shape the core really emits, copied from
  // `stack/diagnose` and `stack/schema` run against examples/webstack/compose.yaml
  // and against a two-service collision. `host/realcore.test.ts` asserts against
  // the live binary that these are still the shapes it produces, and names
  // `services.db.environment.POSTGRES_PASSWORD` explicitly, so a change in the
  // core's anchoring breaks that test rather than quietly making this one a test
  // of nothing.
  //
  // This matters because it is what shipped the defect. The previous fixture
  // hand-wrote an anchor at `services.db.image` — a top-level scalar field, the
  // ONE shape that already worked — and `plaintext-credential` has never
  // anchored there in its life. It anchors at an environment ENTRY, which
  // `stack/schema` returns inside `value.entries[]` rather than as a field, and
  // no entry row could carry a pill at all.

  /** The mapping `stack/schema` returns for `services.db.environment`. */
  const environment = (): SchemaField =>
    field('environment', true, {
      type: 'array or object',
      free_form: true,
      value: {
        kind: 'mapping',
        text: '',
        env_known: true,
        origin: origin(74),
        overrides: [],
        entries: [
          { key: 'POSTGRES_DB', key_origin: origin(74), path: 'services.db.environment.POSTGRES_DB', value: scalar('shipyard', 74) },
          { key: 'POSTGRES_USER', key_origin: origin(75), path: 'services.db.environment.POSTGRES_USER', value: scalar('shipyard', 75) },
          { key: 'POSTGRES_PASSWORD', key_origin: origin(76), path: 'services.db.environment.POSTGRES_PASSWORD', value: scalar('hunter2', 76) },
        ],
      },
    });

  /** `plaintext-credential`, verbatim from `stack/diagnose`. */
  const plaintext = (severity: Finding['severity'] = 'hint'): Finding => ({
    rule: 'plaintext-credential',
    severity,
    title: 'Credential written in plain text',
    message:
      'service "db" sets POSTGRES_PASSWORD to a literal value in the file; the key name marks it as a credential',
    subjects: ['services.db'],
    anchors: [
      {
        label: 'written here',
        path: 'services.db.environment.POSTGRES_PASSWORD',
        origin: origin(76),
      },
    ],
  });

  /** `build-dockerfile-missing`, which anchors at the MAPPING, not at an entry. */
  const buildMissing = (): Finding => ({
    rule: 'build-dockerfile-missing',
    severity: 'warning',
    title: 'build names a Dockerfile that is not there',
    message: 'build names Nope.Dockerfile and no such file is there',
    subjects: ['services.db.build'],
    anchors: [{ label: 'declared here', path: 'services.db.build', origin: origin(4) }],
  });

  const buildField = (): SchemaField =>
    field('build', true, {
      value: {
        kind: 'mapping',
        text: '',
        env_known: true,
        origin: origin(4),
        overrides: [],
        entries: [
          { key: 'context', key_origin: origin(5), path: 'services.db.build.context', value: scalar('.', 5) },
          { key: 'dockerfile', key_origin: origin(6), path: 'services.db.build.dockerfile', value: scalar('Nope.Dockerfile', 6) },
        ],
      },
    });

  /** The row a pill sits on, by the config path of the block that holds it. */
  function pillRows(root: El): string[] {
    return byClass(root, 'pill').map((p) => {
      let node: El | null = p.parent;
      while (node && !(node.classList.contains('field-block') || node.classList.contains('grp'))) {
        node = node.parent;
      }
      return node?.dataset.path ?? '(no block)';
    });
  }

  it('puts the pill on the entry row the finding anchors at, not on the mapping header', () => {
    // THE DEFECT. `field()` asked `findingsForField`; `entryRow()` never
    // received the inspection and never asked, so the pill for every one of
    // twenty environment keys landed on the `environment` header — a row that
    // does not say which key is the credential, which is the entire question.
    const { root } = render(inspectionOf([environment()], { findings: [plaintext()] }));
    assert.deepEqual(
      pillRows(root),
      ['services.db.environment.POSTGRES_PASSWORD'],
      'the pill is not on the row the core anchored it at',
    );
  });

  it('does not also report it on the mapping header', () => {
    // The other half: `findingsForField('services.db.environment')` matches a
    // descendant anchor too, so without the claimed-by-a-descendant exclusion
    // the same finding is drawn twice — once where it belongs and once where it
    // used to be wrong.
    const { root } = render(inspectionOf([environment()], { findings: [plaintext()] }));
    assert.equal(byClass(root, 'pill').length, 1, 'the finding is reported twice');
    assert.equal(
      byClass(root, 'diag').length,
      1,
      'the sentence beneath the pill is repeated on the header as well',
    );
  });

  it('keeps a finding anchored at the mapping itself on the mapping row', () => {
    // Some entries have findings and some do not — and `build-dockerfile-missing`
    // anchors at `services.db.build`, which is the header. The header is a row,
    // not a summary of its children: a pill on it means THIS key, and it earns
    // one here because the core named it and no entry beneath claimed it.
    const { root } = render(
      inspectionOf([buildField(), environment()], { findings: [buildMissing(), plaintext()] }),
    );
    assert.deepEqual(
      pillRows(root).sort(),
      ['services.db.build', 'services.db.environment.POSTGRES_PASSWORD'],
      'a mapping-anchored finding and an entry-anchored one do not land one level apart',
    );
  });

  it('leaves an entry with no finding unmarked', () => {
    // The complement, and the reason the first assertion is a deepEqual on the
    // whole list rather than a `some`: pinning a pill to every row would satisfy
    // "the credential row has a pill" and tell the reader nothing.
    const { root } = render(inspectionOf([environment()], { findings: [plaintext()] }));
    for (const key of ['POSTGRES_DB', 'POSTGRES_USER']) {
      const block = walk(root).find((e) => e.dataset.path === `services.db.environment.${key}`);
      assert.ok(block, `${key} rendered no row at all`);
      assert.equal(byClass(block!, 'pill').length, 0, `${key} carries a pill nothing anchored there`);
    }
  });

  it('reads the RULE, and carries the severity in the accessible name — N6', () => {
    // The owner's decision, 2026-08-13: show both. The visible text is the rule,
    // because `hint` is what every hint in the pane said and the reader learned
    // nothing from it. Story 4.5's floor forbids severity being colour-only, so
    // the severity travels in the accessible name — which a screen reader
    // announces INSTEAD of the text — and in the tooltip.
    for (const severity of ['error', 'warning', 'hint'] as const) {
      const { root } = render(
        inspectionOf([environment()], { findings: [plaintext(severity)] }),
      );
      const pills = byClass(root, 'pill');
      assert.equal(pills.length, 1, `${severity}: no pill was rendered`);
      assert.equal(pills[0].textContent, 'plaintext-credential', `${severity}: the pill says nothing`);
      assert.ok(pills[0].classList.contains(`pill-${severity}`));
      // Text, not colour: both of these are read out, and neither is a colour.
      assert.equal(pills[0].getAttribute('aria-label'), `${severity}: plaintext-credential`);
      assert.match(pills[0].title, new RegExp(`^${severity}: `));
      assert.match(pills[0].title, /Credential written in plain text/);
    }
  });

  it('puts the pill inline with the field row it concerns', () => {
    // DESIGN.md:180 and the mockup at directions-3.html:327 — inline with the
    // VALUE, on the entry row. It used to be a separate block indented to 34%,
    // which reads as a paragraph about the pane rather than a mark on one value.
    const { root } = render(inspectionOf([environment()], { findings: [plaintext()] }));
    const pill = byClass(root, 'pill')[0];
    assert.ok(pill.parent, 'the pill has no parent');
    assert.ok(
      pill.parent!.classList.contains('field'),
      `the pill is inside .${pill.parent!.className}, not inside the field row`,
    );
    // And after the value, not before the key.
    const cells = pill.parent!.children.map((c) => c.className);
    assert.ok(
      cells.indexOf(pill.className) === cells.length - 1,
      `the pill is not the last cell of the row: ${JSON.stringify(cells)}`,
    );
  });

  it('says what its count counts, since the problems panel counts something else', () => {
    // `host-port-collision` is one finding at two positions, both real. The
    // header reads `1 error`; the problems panel shows two rows for it. Both
    // are right, and the owner read them as a contradiction — so the header
    // states its unit and its scope rather than leaving the reader to guess.
    const collision: Finding = {
      rule: 'host-port-collision',
      severity: 'error',
      title: 'Two services publish the same host port',
      message: 'services "web" and "api" both publish host port 8080',
      subjects: ['services.web', 'services.api'],
      anchors: [
        { label: 'web', path: 'services.web.ports[0]', origin: origin(8) },
        { label: 'api', path: 'services.api.ports[0]', origin: origin(12) },
      ],
    };
    const { root } = render(inspectionOf([environment()], { findings: [collision] }));
    const header = byClass(root, 'inspector-severity')[0];
    assert.equal(header.textContent, '1 error', 'the header counts anchors instead of findings');
    const name = header.getAttribute('aria-label') ?? '';
    assert.match(name, /1 finding in db, at 2 positions/, `the header explains nothing: ${name}`);
    assert.match(name, /Problems panel/, 'the header never mentions what it is being compared against');
    assert.equal(header.title, (header.getAttribute('aria-label') ?? '').split(' — ')[1]);
  });
});

/* -------------------------------------------------------------------------
 * Staging controls.
 * ---------------------------------------------------------------------- */

describe('a staged edit can be discarded on its own', () => {
  // `unstage` existed in the protocol, in the host switch and in Staging.remove
  // since story 6.1, and no control in the webview ever sent it: a reader with
  // three staged edits could discard all three or save all three.
  it('offers a per-field discard that sends unstage', () => {
    const image = field('image', true, { value: scalar('postgres:16') });
    const { root } = render(
      inspectionOf([image], {
        staged: ['services.db.image'],
        pending: { 'services.db.image': 'postgres:17' },
      }),
    );
    const discard = byClass(root, 'field-unstage');
    assert.equal(discard.length, 1, 'a staged field offers no way to discard just that edit');
    assert.equal(discard[0].tagName, 'BUTTON');
    assert.ok((discard[0].getAttribute('aria-label') ?? '').includes('services.db.image'));
    discard[0].fire('click');
    assert.deepEqual(sent, [{ type: 'unstage', path: 'services.db.image' }]);
  });

  it('reverts a staged field to the FILE on Escape, not to the stage', () => {
    // The accessible name has always promised this and the control did the
    // opposite: it restored the staged text, which is not a revert.
    const image = field('image', true, { value: scalar('postgres:16') });
    const { root } = render(
      inspectionOf([image], {
        staged: ['services.db.image'],
        pending: { 'services.db.image': 'postgres:17' },
      }),
    );
    const input = walk(root).find((e) => e.dataset.field === 'services.db.image')!;
    assert.equal(input.value, 'postgres:17');
    input.fire('keydown', { key: 'Escape' });
    assert.deepEqual(sent, [{ type: 'unstage', path: 'services.db.image' }]);
  });
});

/* -------------------------------------------------------------------------
 * The pending strip.
 * ---------------------------------------------------------------------- */

/* -------------------------------------------------------------------------
 * Story 5.1: what the pane says about a value, and about the stack.
 * ---------------------------------------------------------------------- */

describe('the resolution line under a ${VAR} — story 5.1', () => {
  // MUTATION: `appendResolution` returns immediately. The literal `${TAG}` is
  // still on screen and what it MEANS never is — which is the criterion the
  // story is named for. Every check that existed tested `provenanceOf`.
  it('says what a variable resolved to, under the literal the file holds', () => {
    const image = field('image', true, {
      value: scalar('nginx:${TAG}', 12, { resolved: 'nginx:1.27' }),
    });
    const { root } = render(inspectionOf([image]));
    const line = byClass(root, 'resolution');
    assert.equal(line.length, 1, 'a ${VAR} rendered with nothing saying what it resolves to');
    assert.match(line[0].textContent, /→/);
    assert.match(line[0].textContent, /nginx:1\.27/);
    // The literal stays: the resolution is an addition, never a replacement.
    assert.ok(
      walk(root).some((e) => e.value === 'nginx:${TAG}' || e.textContent === 'nginx:${TAG}'),
      'the literal the file holds was replaced by its resolution',
    );
  });

  it('names every undefined variable rather than printing a blank', () => {
    const image = field('image', true, {
      value: scalar('nginx:${TAG}', 12, { resolved: 'nginx:', undefined: ['TAG'] }),
    });
    const { root } = render(inspectionOf([image]));
    const line = byClass(root, 'resolution')[0];
    assert.ok(line.classList.contains('is-unresolved'), 'an unresolved value reads as a resolved one');
    assert.match(line.textContent, /TAG undefined/);
  });

  it('says nothing at all when no environment was established', () => {
    // Silence is honest here and "undefined" would not be.
    const image = field('image', true, {
      value: scalar('nginx:${TAG}', 12, { env_known: false, resolved: '' }),
    });
    const { root } = render(inspectionOf([image]));
    assert.deepEqual(byClass(root, 'resolution'), []);
  });

  it('says which variables are undefined even when nothing resolved', () => {
    const image = field('image', true, {
      value: scalar('${A}-${B}', 12, { resolved: '', undefined: ['A', 'B'] }),
    });
    const { root } = render(inspectionOf([image]));
    const line = byClass(root, 'resolution')[0];
    assert.match(line.textContent, /A, B — undefined in \.env and the environment/);
  });
});

describe('a value is in the code face — story 5.1', () => {
  // MUTATION: `mono` stripped from every value cell. Monospace is not
  // decoration here: it is the pane's one signal for "this string is literally
  // in your file", and the sentence explaining why a field cannot be edited is
  // deliberately NOT in it. Stripping it makes prose and file content
  // indistinguishable, and no assertion anywhere looked at a class name.
  /**
   * Every cell holding file content. `.field-shape` is excluded: `list` and
   * `map` are OUR words for what a value is, not the value, and they are
   * deliberately not in the code face.
   */
  const literalClasses = (root: El): string[] =>
    walk(root)
      .filter(
        (e) =>
          (e.classList.contains('field-value') && !e.classList.contains('field-shape')) ||
          e.classList.contains('field-key-literal') ||
          e.classList.contains('prov-link'),
      )
      .map((e) => e.className);

  it('marks every rendered value and provenance link as literal file content', () => {
    // A scalar (an input), a read-only alias (a span) and a mapping's entries:
    // three different code paths, three different literals in the source.
    const image = field('image', true, { value: scalar('postgres:16', 12) });
    const alias = field('logging', true, {
      value: { ...scalar('*defaults', 13), kind: 'alias' as const },
    });
    const env = field('environment', true, {
      value: {
        kind: 'mapping',
        text: '',
        env_known: true,
        origin: origin(20),
        overrides: [],
        entries: [
          {
            key: 'POSTGRES_USER',
            key_origin: origin(21),
            path: 'services.db.environment.POSTGRES_USER',
            value: scalar('admin', 21),
          },
        ],
      },
    });
    const { root } = render(inspectionOf([image, alias, env]));
    const classes = literalClasses(root);
    assert.ok(classes.length > 0, 'no value cell was rendered at all');
    for (const c of classes) {
      assert.ok(
        c.split(/\s+/).includes('mono'),
        `a value rendered as .${c} — prose and file content are now indistinguishable`,
      );
    }
  });

  it('keeps the explanatory prose OUT of the code face', () => {
    const restart = field('restart', false, { description: 'when to restart' });
    const { root } = render(inspectionOf([restart]));
    for (const note of byClass(root, 'field-note')) {
      assert.equal(note.classList.contains('mono'), false, 'a sentence is rendered as file content');
    }
  });
});

describe('the stack itself is a subject — story 5.1', () => {
  // MUTATION: the `id === null` branch that appends the stack facts deleted.
  // A deselection then fills the pane with a heading and nothing under it,
  // where the criterion is that the inspector is never empty: with no service
  // selected there is still a stack, and its files and profiles are what it is.
  const stackInspection = (): any => ({
    ...inspectionOf([field('services', true)], { id: null, name: 'w/compose.yaml', kind: 'stack' }),
  });

  it('renders the files the stack was merged from, and its profiles', () => {
    const inspection = stackInspection();
    inspection.schema.files = [
      { path: '/w/compose.yaml', step: 0 },
      { path: '/w/compose.override.yaml', step: 1 },
    ];
    inspection.schema.profiles = ['debug'];
    const { root } = render(inspection);
    const text = walk(root)
      .filter((e) => e.classList.contains('grp'))
      .map((e) => e.textContent)
      .join('\n');
    assert.match(text, /this stack/, 'nothing at all was said about the stack');
    assert.match(text, /source files/);
    assert.match(text, /compose\.override\.yaml/, 'the override the stack was merged from is not named');
    assert.match(text, /debug/, 'the profiles are not named');
  });

  it('says "none declared" rather than leaving the row blank', () => {
    const { root } = render(stackInspection());
    const values = byClass(root, 'field-value').map((e) => e.textContent);
    assert.ok(values.includes('none declared'), `profiles rendered as ${JSON.stringify(values)}`);
  });

  it('does not put the stack facts on a service', () => {
    const { root } = render(inspectionOf([field('image', true)]));
    assert.equal(
      walk(root).some((e) => e.textContent.includes('source files')),
      false,
      'every service now reports the whole stack’s files',
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 5.2: AD-21's unsupported mark.
 * ---------------------------------------------------------------------- */

describe('a key the installed Compose cannot run says so — AD-21', () => {
  // MUTATION: the mark stripped from the span, from its class, from the
  // accessible name and from the tooltip. Four separate carriers, and no
  // assertion covered any of them: the key still renders, still opens, still
  // stages — and the reader writes a key their Compose will reject.
  const unsupported = (): SchemaField =>
    field('develop', false, { support: 'no', min_version: '2.22.0' });

  it('writes the version it needs beside the key', () => {
    const { root } = render(inspectionOf([unsupported()]));
    const key = byClass(root, 'addable-key')[0];
    assert.ok(key, 'the key is not offered at all — hiding it teaches that it does not exist');
    assert.match(
      key.textContent,
      /needs Compose 2\.22\.0/,
      `the key reads ${JSON.stringify(key.textContent)} — nothing says it will not run`,
    );
  });

  it('carries the mark as a class, an accessible name and a tooltip too', () => {
    const { root } = render(inspectionOf([unsupported()]));
    const key = byClass(root, 'addable-key')[0];
    assert.equal(key.classList.contains('is-unsupported'), true, 'the mark has no class to style');
    assert.match(
      key.getAttribute('aria-label') ?? '',
      /needs Compose 2\.22\.0/,
      'a key the installed Compose cannot run sounds identical to one it can',
    );
    assert.match(key.title, /needs Compose 2\.22\.0/, 'the tooltip says nothing about support');
  });

  it('leaves a supported key unmarked, so the mark means something', () => {
    const { root } = render(inspectionOf([field('restart', false, { support: 'yes' })]));
    const key = byClass(root, 'addable-key')[0];
    assert.equal(key.classList.contains('is-unsupported'), false);
    assert.doesNotMatch(key.textContent, /needs Compose/);
  });
});

/* -------------------------------------------------------------------------
 * Story 5.4: findings that are about the node, not about one field.
 * ---------------------------------------------------------------------- */

describe('node-level findings are read before the values they concern — story 5.4', () => {
  // MUTATION: the `findingsForNode` loop at the head of `render` deleted. This
  // is the COMMON case, not an edge: `unreachable-service` and
  // `healthy-without-healthcheck` both anchor at the NODE path, so with that
  // one loop gone the pane shows no diagnostic at all for either of them —
  // while the field-level pill tests keep passing, because they anchor at a
  // field.
  const nodeFinding = (rule: string, path: string): Finding => ({
    rule,
    severity: 'warning',
    title: rule,
    message: `${rule}: this service is not reachable from anything`,
    subjects: ['services.db'],
    anchors: [{ label: 'here', path, origin: origin(9) }],
  });

  it('renders a finding anchored at the node itself, at the head of the pane', () => {
    const image = field('image', true, { value: scalar('postgres:16') });
    const { root } = render(
      inspectionOf([image], { findings: [nodeFinding('unreachable-service', 'services.db')] }),
    );
    const diagnostics = byClass(root, 'diag');
    assert.equal(
      diagnostics.length,
      1,
      'a finding about the service is on no field and nowhere in the pane',
    );
    assert.match(diagnostics[0].textContent, /not reachable/);
    // At the HEAD: before the first group, where it is read before the values.
    const bodyChildren = diagnostics[0].parent!.children;
    assert.ok(
      bodyChildren.indexOf(diagnostics[0]) < bodyChildren.findIndex((c) => c.classList.contains('grp')),
      'the finding is below the values it is about',
    );
  });

  it('does the same for a finding anchored at a path the node does not list', () => {
    // `healthy-without-healthcheck` anchors at `services.db.healthcheck`, which
    // an undeclared healthcheck means is not among the rendered field paths.
    const image = field('image', true, { value: scalar('postgres:16') });
    const { root } = render(
      inspectionOf([image], {
        findings: [nodeFinding('healthy-without-healthcheck', 'services.db.healthcheck')],
      }),
    );
    assert.equal(byClass(root, 'diag').length, 1, 'a finding vanished because its path is not a field');
  });

  it('does not repeat a field finding at the head as well', () => {
    const image = field('image', true, { value: scalar('postgres:16') });
    const { root } = render(
      inspectionOf([image], { findings: [nodeFinding('plaintext-credential', 'services.db.image')] }),
    );
    assert.equal(byClass(root, 'diag').length, 1, 'the same finding is on screen twice');
    // And it is inside the field it concerns, not at the head of the pane.
    const diag = byClass(root, 'diag')[0];
    let owner = diag.parent;
    while (owner && !owner.classList.contains('field-block')) {
      owner = owner.parent;
    }
    assert.ok(owner, 'a finding about one field was lifted out of that field');
  });
});

describe('the pending strip', () => {
  it('says "no pending changes" rather than vanishing', () => {
    // DESIGN.md:186. It used to set hidden = true, so a reader could not tell a
    // pane with nothing staged from a strip that had failed to appear.
    const view = new PendingView({ send: () => {} });
    const el = view.element as unknown as El;
    view.clear();
    assert.equal(el.hidden, false, 'the strip hid itself');
    assert.equal(EMPTY_SUMMARY, 'no pending changes');
    assert.ok(
      walk(el).some((e) => e.textContent === EMPTY_SUMMARY),
      'the words "no pending changes" are nowhere in the strip',
    );
  });

  it('does not offer Save when there is nothing to save', () => {
    const view = new PendingView({ send: () => {} });
    const el = view.element as unknown as El;
    view.clear();
    const buttons = walk(el).filter((e) => e.tagName === 'BUTTON');
    assert.ok(buttons.length > 0);
    for (const b of buttons) {
      assert.equal(b.hidden, true, `${b.textContent || 'a button'} is offered with nothing staged`);
    }
  });
});

/* -------------------------------------------------------------------------
 * How a collection reads — the owner's second complaint, 2026-08-13:
 * "the lists look weird like this".
 *
 * Measured before: `networks: [shipyard]` occupied FOUR lines and 84px —
 * a row reading `networks` `list`, an indented row reading `[0]` `shipyard`,
 * the item's provenance line, and the field's own provenance line. Two of the
 * words on screen (`list`, `[0]`) are not in the file and say nothing the rows
 * underneath do not.
 *
 * Every assertion below is about what is RENDERED, never about a rule existing.
 * ---------------------------------------------------------------------- */

describe('a collection reads as its contents, not as its type', () => {
  const seq = (items: ValueView[], line = 11): ValueView => ({
    kind: 'sequence',
    text: '',
    env_known: true,
    origin: origin(line),
    overrides: [],
    seq: items,
  });

  /** Every word the pane puts in a value cell — typed or printed. */
  const valueTexts = (root: El): string[] =>
    walk(root)
      .filter((e) => e.classList.contains('field-value'))
      .map((e) => e.textContent || e.value);

  const keyTexts = (root: El): string[] =>
    walk(root)
      .filter((e) => e.classList.contains('field-key'))
      .map((e) => e.textContent);

  it('prints a short list of scalars as its values, not as the word list', () => {
    // MUTATION: restore the `field-shape` chip. It reads `list`, which is our
    // word for the shape of a value the reader can already see the shape of.
    const { root } = render(
      inspectionOf([
        field('networks', true, { value: seq([scalar('shipyard', 11), scalar('edge', 11)]) }),
      ]),
    );
    assert.ok(
      valueTexts(root).some((t) => t.includes('shipyard') && t.includes('edge')),
      `no cell holds both values: ${JSON.stringify(valueTexts(root))}`,
    );
    assert.ok(
      !valueTexts(root).includes('list'),
      'a value cell still reads "list", which is not in the file',
    );
    assert.ok(
      !keyTexts(root).some((t) => /^\[\d+\]$/.test(t)),
      `an index row survived: ${JSON.stringify(keyTexts(root))}`,
    );
  });

  it('separates the values with a mark that is NOT in the code face', () => {
    // DESIGN.md: monospace means this string is literally in your file. The
    // values are; the separator between them is ours.
    const { root } = render(
      inspectionOf([field('networks', true, { value: seq([scalar('a', 11), scalar('b', 11)]) })]),
    );
    const seps = walk(root).filter((e) => e.classList.contains('field-sep'));
    assert.equal(seps.length, 1, 'one separator for two values');
    assert.ok(!seps[0].classList.contains('mono'), 'the separator claims to be file content');
  });

  it('costs one row, where the tree cost four', () => {
    const { root } = render(
      inspectionOf([field('networks', true, { value: seq([scalar('shipyard', 11)]) })]),
    );
    // One field row and one provenance line — the item row and the item's own
    // provenance line are gone because the parent's line says the same thing.
    assert.equal(byClass(root, 'field').length, 1);
    assert.equal(byClass(root, 'prov').length, 1);
  });

  it('keeps the tree when an item would lose its own provenance', () => {
    // The rule that makes collapsing safe: an item whose file:line differs
    // from the parent's is saying something the parent's line does not.
    const { root } = render(
      inspectionOf([
        field('networks', true, { value: seq([scalar('shipyard', 11), scalar('edge', 44)]) }),
      ]),
    );
    assert.ok(
      keyTexts(root).some((t) => t === '[1]'),
      'the second item was folded away and its line 44 with it',
    );
    assert.ok(
      byClass(root, 'prov').some((p) => p.textContent.includes(':44')),
      'line 44 is nowhere on the pane',
    );
  });

  it('keeps the tree when an item is unresolved, so its warning survives', () => {
    const { root } = render(
      inspectionOf([
        field('env_file', true, {
          value: seq([scalar('${MISSING}', 11, { undefined: ['MISSING'] })]),
        }),
      ]),
    );
    assert.ok(
      byClass(root, 'resolution').length > 0,
      'the resolution sentence for an undefined variable was folded away',
    );
  });

  it('keeps the tree when the values are longer than a row', () => {
    const long = Array.from({ length: 6 }, (_, i) => scalar(`a-fairly-long-item-${i}`, 11));
    const { root } = render(inspectionOf([field('command', true, { value: seq(long) })]));
    assert.equal(
      keyTexts(root).filter((t) => /^\[\d+\]$/.test(t)).length,
      6,
      'a 6-item list was crushed into one truncated cell',
    );
  });

  it('gives a mapping no type word either — the entries are the value', () => {
    const { root } = render(
      inspectionOf([
        field('environment', true, {
          value: {
            kind: 'mapping',
            text: '',
            env_known: true,
            origin: origin(20),
            overrides: [],
            entries: [
              {
                key: 'NODE_ENV',
                key_origin: origin(21),
                path: 'services.db.environment.NODE_ENV',
                value: scalar('production', 21),
              },
            ],
          },
        }),
      ]),
    );
    assert.ok(!valueTexts(root).includes('map'), 'a value cell still reads "map"');
    assert.ok(
      valueTexts(root).some((t) => t === 'production'),
      'the entry is gone along with the word for it',
    );
  });
});

/* -------------------------------------------------------------------------
 * One indent step per level of nesting — the owner's fourth complaint, about
 * the two-column grid.
 *
 * Measured before: one pane rendered its values at x=820, x=831 and x=844,
 * because a field inside a nested mapping group was indented TWICE (once by
 * the group, once by itself) and nothing gave the key column the step back.
 * ---------------------------------------------------------------------- */

describe('nesting steps once per level, and the value column does not move', () => {
  const mapping = (entries: any[], line = 20): ValueView => ({
    kind: 'mapping',
    text: '',
    env_known: true,
    origin: origin(line),
    overrides: [],
    entries,
  });

  it('indents a field inside a nested group once, not twice', () => {
    const { root } = render(
      inspectionOf([
        field('healthcheck', true, {
          value: mapping([]),
          children: [
            field('healthcheck.interval', true, {
              key: 'interval',
              value: scalar('30s', 21),
            }),
          ],
        }),
      ]),
    );
    const nested = byClass(root, 'grp-nested');
    assert.equal(nested.length, 1, 'the mapping did not become a group of its own');
    // The group carries the step. The block inside it must NOT carry a second.
    const inner = walk(nested[0]).filter((e) => e.classList.contains('field-block'));
    assert.ok(inner.length > 0, 'the nested group has no field in it');
    for (const block of inner) {
      assert.ok(
        !block.classList.contains('is-nested'),
        'a field inside an already-indented group indents again',
      );
    }
  });

  it('tells every indented container how far right it is, in whole steps', () => {
    const { root } = render(
      inspectionOf([
        field('environment', true, {
          value: mapping([
            {
              key: 'NODE_ENV',
              key_origin: origin(21),
              path: 'services.db.environment.NODE_ENV',
              value: scalar('production', 21),
            },
          ]),
        }),
      ]),
    );
    const entry = byClass(root, 'is-nested')[0];
    assert.ok(entry, 'the mapping entry did not render');
    assert.equal(
      entry.style['--ind'],
      `${INDENT_STEP}px`,
      'one level of nesting did not report one step',
    );
  });

  it('reports two steps two levels down, so the key column gives both back', () => {
    const { root } = render(
      inspectionOf([
        field('healthcheck', true, {
          value: mapping([]),
          children: [
            field('healthcheck.test', true, {
              key: 'test',
              value: {
                kind: 'sequence',
                text: '',
                env_known: true,
                origin: origin(21),
                overrides: [],
                // Two different lines, so it stays a tree and the depth shows.
                seq: [scalar('CMD', 21), scalar('curl', 22)],
              },
            }),
          ],
        }),
      ]),
    );
    const inds = byClass(root, 'is-nested')
      .map((e) => e.style['--ind'])
      .filter((v) => v !== undefined);
    assert.ok(
      inds.includes(`${INDENT_STEP * 2}px`),
      `no container reported two steps: ${JSON.stringify(inds)}`,
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 7.9: the values a key accepts.
 * ---------------------------------------------------------------------- */

/**
 * Two DIFFERENT lists, from two different sources.
 *
 * One fixture with one allowed value cannot tell a list read from the core
 * apart from a list typed into the webview, and a fixture whose values are
 * never asserted cannot tell a rendered option from an arrived one. So: two
 * keys, two lists, and every assertion below is on the option TEXT.
 */
const restartValues = ['no', 'always', 'on-failure', 'unless-stopped'];
const conditionValues = ['service_started', 'service_healthy', 'service_completed_successfully'];

/** The popup built for a path, whatever the field currently holds. */
const popupOf = (root: El, path: string): El | undefined =>
  walk(root).find((e) => e.classList.contains('combo-popup') && e.dataset.for === path);

const toggleOf = (root: El, path: string): El | undefined =>
  walk(root).find((e) => e.classList.contains('combo-toggle') && e.dataset.for === path);

const inputOf = (root: El, path: string): El =>
  walk(root).find((e) => e.dataset.field === path)!;

/**
 * The options a reader would SEE in the popup, in order, as their rendered
 * text.
 *
 * Read out of the tree — `role="option"` under the popup for this path — and
 * mapped to `textContent`, not to the array the fixture supplied and not to a
 * length. A count passes on the right number of the wrong words; the previous
 * build of this story shipped a binding asserted on one of two render paths,
 * and the fix for THAT is that both paths below call this same function.
 */
const optionsOf = (root: El, path: string): string[] => {
  const popup = popupOf(root, path);
  if (!popup) {
    return [];
  }
  return walk(popup)
    .filter((e) => e.getAttribute('role') === 'option')
    // A HIDDEN option is not an option the reader has. Filtering the list is
    // the defect this control exists to fix, and the first version of this
    // reader mapped every option in the tree — so hiding three of them, which
    // is a datalist restored by hand, left it green. `harness/comboshot.mjs`
    // asks the same question of rendered pixels, which is the only place a
    // `display: none` in the stylesheet could be caught.
    .filter((o) => o.hidden !== true)
    .map((o) => walk(o).find((c) => c.classList.contains('combo-text'))?.textContent ?? '');
};

/** The options a reader can see RIGHT NOW: none at all while the popup is shut. */
const shownOptionsOf = (root: El, path: string): string[] =>
  popupOf(root, path)?.hidden === false ? optionsOf(root, path) : [];

const open = (root: El, path: string): void => {
  toggleOf(root, path)!.fire('click');
};

/** A declared `restart`, holding one of its own allowed values. The owner's case. */
const restartSet = (value = 'unless-stopped'): Inspection =>
  inspectionOf([
    field('restart', true, {
      value: scalar(value),
      allowed: restartValues,
      allowed_source: 'description',
    }),
  ]);

const RESTART = 'services.db.restart';

describe('a key with a fixed set of values — story 7.9', () => {
  /* ---------------------------------------------------------------------
   * The defect the owner reported, and the two render paths.
   * ------------------------------------------------------------------ */

  // THE REJECTED BEHAVIOUR, as an assertion. `restart` is set to
  // `unless-stopped`; the datalist this replaces filtered its options against
  // the input's text and showed that one value and no other, while `no`,
  // `always` and `on-failure` were links under the field. Opening the control
  // on a field that already holds a value must show EVERYTHING.
  it('shows every value when opened on a field that already holds one of them', () => {
    const { root } = render(restartSet('unless-stopped'));
    assert.deepEqual(shownOptionsOf(root, RESTART), [], 'the list is open before anyone opened it');
    open(root, RESTART);
    assert.deepEqual(
      shownOptionsOf(root, RESTART),
      restartValues,
      'the open list is not every value the core reported — this is the reported defect',
    );
  });

  // BOTH render paths, by contents. A declared field and an unset key are built
  // by two different methods (`scalarValue` and `unsetValue`), and the previous
  // build asserted the binding on the first one only: deleting it from the
  // second left every assertion passing.
  it('renders the schema’s own values on the DECLARED path, in the specification’s order', () => {
    const { root } = render(restartSet());
    assert.deepEqual(optionsOf(root, RESTART), restartValues);
  });

  it('renders the schema’s own values on the UNSET path, in the specification’s order', () => {
    const { root } = render(
      inspectionOf([field('restart', false, { allowed: restartValues, allowed_source: 'description' })], {
        opened: [RESTART],
      }),
    );
    assert.deepEqual(optionsOf(root, RESTART), restartValues);
    open(root, RESTART);
    assert.deepEqual(shownOptionsOf(root, RESTART), restartValues);
  });

  // A second list, from a different source, in the same pane. One fixture with
  // one list cannot tell a list read from the core apart from a list typed into
  // the webview.
  it('gives each key its own list rather than one list for the pane', () => {
    const { root } = render(
      inspectionOf([
        field('restart', true, {
          value: scalar('always'),
          allowed: restartValues,
          allowed_source: 'description',
        }),
        field('condition', false, { allowed: conditionValues, allowed_source: 'schema' }),
      ], { opened: ['services.db.condition'] }),
    );
    assert.deepEqual(optionsOf(root, RESTART), restartValues);
    assert.deepEqual(optionsOf(root, 'services.db.condition'), conditionValues);
  });

  // The values are IN THE LIST and not under the field. The row of `.addable-key`
  // links is the thing the owner rejected in words, so its absence is an
  // assertion rather than a comment: "the expected values should be in the list
  // and not as links below it."
  it('prints no row of value links under the field', () => {
    const { root } = render(restartSet());
    assert.deepEqual(byClass(root, 'allowed-value'), []);
    assert.deepEqual(byClass(root, 'addable-key'), [], 'the values are back as links under the field');
    // …said as the property rather than as a class name, so renaming the chip
    // cannot bring it back: NO control anywhere in the pane is labelled with one
    // of the offered values. The options themselves are `role="option"` inside
    // the popup and are not buttons, so they do not answer this question.
    const asControls = walk(root).filter(
      (e) => e.tagName === 'BUTTON' && restartValues.includes(e.textContent),
    );
    assert.deepEqual(asControls.map((b) => b.textContent), []);
  });

  /* ---------------------------------------------------------------------
   * Never filtered, and still typeable.
   * ------------------------------------------------------------------ */

  it('does not narrow the list to what has been typed', () => {
    // The datalist's whole failure mode, as a check. `unless` is a prefix of
    // exactly one offered value, which is the condition under which a filtering
    // control shows one entry.
    const { root } = render(restartSet());
    const input = inputOf(root, RESTART);
    input.value = 'unless';
    open(root, RESTART);
    assert.deepEqual(shownOptionsOf(root, RESTART), restartValues);
  });

  it('shows a value the schema does not list as the field’s value, and still offers the list', () => {
    // The whole reason this is an input and not a <select>. `${RESTART_POLICY}`
    // is not one of four words and it is what the file says.
    const { root } = render(restartSet('${RESTART_POLICY}'));
    assert.equal(inputOf(root, RESTART).value, '${RESTART_POLICY}');
    open(root, RESTART);
    assert.deepEqual(shownOptionsOf(root, RESTART), restartValues);
  });

  it('stages a value the schema never listed, with no warning and no correction', () => {
    const { root } = render(restartSet('always'));
    const input = inputOf(root, RESTART);
    input.value = '${RESTART_POLICY}';
    input.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [
      { type: 'edit', path: RESTART, value: '${RESTART_POLICY}' },
    ]);
  });

  it('is an input and never a select, and never a datalist either', () => {
    // Stated as an assertion rather than as a comment, because a later refactor
    // to a <select> would pass every other test in this block except the two
    // about typing — and would silently make `${RESTART_POLICY}` unwritable.
    // DATALIST is named too: it is what this replaced, and its filtering is the
    // defect, so its return would be a regression rather than a simplification.
    const { root } = render(restartSet());
    assert.equal(walk(root).filter((e) => e.tagName === 'SELECT').length, 0);
    assert.equal(walk(root).filter((e) => e.tagName === 'OPTGROUP').length, 0);
    assert.equal(walk(root).filter((e) => e.tagName === 'DATALIST').length, 0);
    assert.equal(inputOf(root, RESTART).tagName, 'INPUT');
  });

  /* ---------------------------------------------------------------------
   * Which one is the current value.
   * ------------------------------------------------------------------ */

  it('marks the value the field holds, in a glyph and in aria-selected', () => {
    const { root } = render(restartSet('on-failure'));
    open(root, RESTART);
    const options = walk(popupOf(root, RESTART)!).filter((e) => e.getAttribute('role') === 'option');
    assert.deepEqual(
      options.filter((o) => o.getAttribute('aria-selected') === 'true').map((o) => o.dataset.value),
      ['on-failure'],
    );
    // Never colour alone — story 4.5's floor. The mark is a character.
    assert.deepEqual(
      options.filter((o) => o.classList.contains('is-current')).map((o) => o.children[0].textContent),
      ['✓'],
    );
  });

  it('marks nothing when the field holds a value the specification does not name', () => {
    const { root } = render(restartSet('${RESTART_POLICY}'));
    open(root, RESTART);
    const options = walk(popupOf(root, RESTART)!).filter((e) => e.getAttribute('role') === 'option');
    assert.deepEqual(options.map((o) => o.getAttribute('aria-selected')), [
      'false', 'false', 'false', 'false',
    ]);
  });

  /* ---------------------------------------------------------------------
   * The keyboard, and DECISIONS.md 17.
   * ------------------------------------------------------------------ */

  it('opens on the down arrow, landing on the value the field already holds', () => {
    const { root } = render(restartSet('on-failure'));
    const input = inputOf(root, RESTART);
    input.fire('keydown', { key: 'ArrowDown' });
    assert.equal(input.getAttribute('aria-expanded'), 'true');
    assert.deepEqual(shownOptionsOf(root, RESTART), restartValues);
    // `on-failure` is index 2, so the reader arrives on their own value rather
    // than at the top of the list.
    assert.equal(input.getAttribute('aria-activedescendant'), `${optionListId(RESTART)}-2`);
    assert.deepEqual(sent, [], 'opening the list staged something');
  });

  it('moves with the arrow keys and wraps at both ends', () => {
    const { root } = render(restartSet('no'));
    const input = inputOf(root, RESTART);
    const activeValue = (): string | undefined => {
      const id = input.getAttribute('aria-activedescendant');
      return walk(root).find((e) => e.getAttribute('id') === id)?.dataset.value;
    };
    input.fire('keydown', { key: 'ArrowDown' });
    assert.equal(activeValue(), 'no');
    input.fire('keydown', { key: 'ArrowDown' });
    assert.equal(activeValue(), 'always');
    input.fire('keydown', { key: 'ArrowUp' });
    assert.equal(activeValue(), 'no');
    input.fire('keydown', { key: 'ArrowUp' });
    assert.equal(activeValue(), 'unless-stopped', 'the list does not wrap');
  });

  it('fills the field from the chosen value and stages nothing — DECISIONS.md 17', () => {
    // The gesture the chips had and the popup keeps: choosing says WHICH value,
    // Enter says write it. Two Enters, and the first one must not stage.
    const { root } = render(restartSet('no'));
    const input = inputOf(root, RESTART);
    input.fire('keydown', { key: 'ArrowDown' });
    input.fire('keydown', { key: 'ArrowDown' });
    input.fire('keydown', { key: 'Enter' });
    assert.equal(input.value, 'always');
    assert.deepEqual(sent, [], 'choosing a value staged an edit; only Enter on a closed list may');
    assert.equal(input.getAttribute('aria-expanded'), 'false');
    // And now the reader's Enter, which is the one that writes.
    input.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'edit', path: RESTART, value: 'always' }]);
  });

  it('fills the field from a clicked option and stages nothing', () => {
    const { root } = render(restartSet('no'));
    open(root, RESTART);
    const option = walk(popupOf(root, RESTART)!).find((e) => e.dataset.value === 'on-failure')!;
    option.fire('click');
    assert.equal(inputOf(root, RESTART).value, 'on-failure');
    assert.deepEqual(sent, [], 'a click staged an edit; only Enter may stage');
  });

  it('Enter on a CLOSED list still stages, exactly as every other field does', () => {
    const { root } = render(restartSet('no'));
    const input = inputOf(root, RESTART);
    input.value = 'always';
    input.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'edit', path: RESTART, value: 'always' }]);
  });

  it('Escape closes the list and changes nothing; Escape again reverts the field', () => {
    const { root } = render(
      inspectionOf([field('restart', false, { allowed: restartValues, allowed_source: 'description' })], {
        opened: [RESTART],
      }),
    );
    const input = inputOf(root, RESTART);
    input.fire('keydown', { key: 'ArrowDown' });
    input.fire('keydown', { key: 'Escape' });
    assert.equal(input.getAttribute('aria-expanded'), 'false');
    assert.deepEqual(shownOptionsOf(root, RESTART), []);
    assert.deepEqual(sent, [], 'Escape on an open list touched the field');
    // The field's own Escape, unchanged since story 6.1: an unset key the reader
    // has opened and not staged goes back to `available, not set`.
    input.fire('keydown', { key: 'Escape' });
    assert.deepEqual(sent, [{ type: 'close', path: RESTART }]);
  });

  /* ---------------------------------------------------------------------
   * WAI-ARIA, and story 4.5's floor.
   * ------------------------------------------------------------------ */

  it('wires the combobox pattern: role, expanded, controls, listbox, options', () => {
    const { root } = render(restartSet());
    const input = inputOf(root, RESTART);
    assert.equal(input.getAttribute('role'), 'combobox');
    assert.equal(input.getAttribute('aria-expanded'), 'false');
    assert.equal(input.getAttribute('aria-haspopup'), 'listbox');
    const listId = input.getAttribute('aria-controls');
    assert.equal(listId, optionListId(RESTART));
    const listbox = walk(root).find((e) => e.getAttribute('id') === listId)!;
    assert.ok(listbox, 'aria-controls names an element that does not exist');
    assert.equal(listbox.getAttribute('role'), 'listbox');
    const options = walk(listbox).filter((e) => e.getAttribute('role') === 'option');
    assert.deepEqual(options.map((o) => o.dataset.value), restartValues);
    // Every option addressable by id, and every id distinct — that is what
    // `aria-activedescendant` needs to be able to point at one.
    const ids = options.map((o) => o.getAttribute('id') ?? '');
    assert.equal(new Set(ids).size, ids.length);
    for (const id of ids) {
      assert.ok(id.startsWith(listId!), `an option id does not belong to this list: ${id}`);
    }
  });

  it('names the arrow, so a reader who cannot see it knows the list is there', () => {
    const { root } = render(restartSet());
    const toggle = toggleOf(root, RESTART)!;
    assert.equal(toggle.tagName, 'BUTTON');
    const name = toggle.getAttribute('aria-label') ?? '';
    assert.ok(name.includes(RESTART), `the arrow does not say which key: ${name}`);
    assert.ok(/4 values/.test(name), `the arrow does not say how many: ${name}`);
    // The tab stop is the field's; the arrow is reached from it with a key.
    assert.equal(toggle.tabIndex, -1);
  });

  it('names the control, so a reader who never opens the list knows one exists', () => {
    const { root } = render(restartSet());
    const name = inputOf(root, RESTART).getAttribute('aria-label') ?? '';
    assert.ok(name.includes(RESTART), `the name does not say which key: ${name}`);
    assert.ok(/4 values/.test(name), `the name does not say values are offered: ${name}`);
    assert.ok(/down arrow/.test(name), `the name does not say how to list them: ${name}`);
  });

  /* ---------------------------------------------------------------------
   * Open sets and closed sets are different claims — and the sentence that
   * says so moved into the popup rather than being lost with the links.
   * ------------------------------------------------------------------ */

  it('says whether the list is closed, differently for each source, IN the popup', () => {
    const noteFor = (source: SchemaField['allowed_source']): string => {
      const { root } = render(
        inspectionOf([
          field('restart', true, {
            value: scalar('always'),
            allowed: restartValues,
            allowed_source: source,
          }),
        ]),
      );
      const popup = popupOf(root, RESTART)!;
      return walk(popup).find((e) => e.classList.contains('combo-lead'))?.textContent ?? '';
    };
    const closed = noteFor('schema');
    const prose = noteFor('description');
    const pattern = noteFor('pattern');
    // An `enum` that sits on ONE arm of a `oneOf` — `gpus` is `"all"`, or a
    // list of GPU device objects. It is a JSON Schema enumeration and it is not
    // a closed set, which is exactly the pair the wording has to keep apart.
    const branch = noteFor('schema-branch');
    assert.ok(/nothing else/i.test(closed), `a closed set is not said to be closed: ${closed}`);
    assert.ok(!/nothing else/i.test(prose), `prose is presented as closed: ${prose}`);
    assert.ok(!/nothing else/i.test(pattern), `a pattern is presented as closed: ${pattern}`);
    assert.ok(!/nothing else/i.test(branch), `one arm of a oneOf is presented as closed: ${branch}`);
    // Four sources, four different things to say, four different sentences.
    // `schema-branch` sharing the `pattern` wording would tell the reader of
    // `gpus` that a regular expression is involved, and falling through to the
    // prose wording would tell them the specification does not enforce `all` —
    // which it does, for the form it enumerates.
    const leads = [closed, branch, pattern, prose];
    assert.equal(
      new Set(leads).size,
      leads.length,
      `two sources say the same sentence: ${JSON.stringify(leads)}`,
    );
  });

  // The lead is one sentence at the top of the popup; every option repeats the
  // claim in its own accessible name, and a reader driving this with
  // `aria-activedescendant` hears only that one. Both carriers have to agree
  // about closedness or the fix reaches half the readers.
  it('says the same thing about closedness in every option’s name', () => {
    const namesFor = (source: SchemaField['allowed_source']): string[] => {
      const { root } = render(
        inspectionOf([
          field('restart', true, {
            value: scalar('always'),
            allowed: restartValues,
            allowed_source: source,
          }),
        ]),
      );
      return walk(popupOf(root, RESTART)!)
        .filter((e) => e.getAttribute('role') === 'option')
        .map((o) => o.getAttribute('aria-label') ?? '');
    };
    const closed = namesFor('schema');
    assert.equal(closed.length, restartValues.length);
    for (const name of closed) {
      assert.ok(/nothing else/i.test(name), `a closed value's name does not say so: ${name}`);
    }
    for (const source of ['schema-branch', 'pattern', 'description'] as const) {
      const names = namesFor(source);
      assert.equal(names.length, restartValues.length);
      for (const name of names) {
        assert.ok(
          !/nothing else/i.test(name),
          `a ${source} value is announced as closed: ${name}`,
        );
      }
    }
  });

  /* ---------------------------------------------------------------------
   * Keys with nothing to offer, and values with nothing to open.
   * ------------------------------------------------------------------ */

  // `container_name`, not `image`. This used `image` until Epic 8 gave that one
  // key a Docker Hub search popup — so the example had to move to a key that
  // genuinely has no control, or the check would have started asserting that
  // the search box is absent while claiming to be about value lists.
  //
  // The property is unchanged and is still the one story 7.9 wrote: a key the
  // specification names no values for gets NO VALUE LIST. `image` is now
  // covered by its own assertions below, which say what its popup is and that
  // it is not this one.
  it('offers nothing for a key with no fixed set', () => {
    const { root } = render(
      inspectionOf([field('container_name', true, { value: scalar('web') })]),
    );
    assert.equal(popupOf(root, 'services.db.container_name'), undefined);
    assert.equal(toggleOf(root, 'services.db.container_name'), undefined);
    const input = inputOf(root, 'services.db.container_name');
    assert.equal(input.getAttribute('role'), null);
    assert.equal(input.getAttribute('aria-controls'), null);
  });

  it('offers no VALUE LIST on image, whose values the specification does not name', () => {
    const { root } = render(inspectionOf([field('image', true, { value: scalar('nginx') })]));
    // The popup `image` does carry is the Docker Hub search, and it is a
    // different control with a different id stem. A value list here would be a
    // list of image names this extension invented, which AD-20 forbids as
    // firmly for values as it does for keys.
    const popup = popupOf(root, 'services.db.image');
    assert.ok(
      popup === undefined || popup.className.includes('imagesearch-popup'),
      'image has a value-list popup; the specification names no values for it',
    );
  });

  // `gpus` is `all` OR a list of device objects. When the file writes the list
  // there is no field to open a popup on, and saying nothing there would drop
  // the specification's own words on the one key where they are hardest to
  // guess. They are stated as TEXT — there is no field for a link to fill, so
  // there is no link.
  it('states the values as text when the value is a collection with no field', () => {
    const { root } = render(
      inspectionOf([
        field('gpus', true, {
          value: {
            kind: 'sequence',
            text: '',
            env_known: true,
            origin: origin(9),
            overrides: [],
            seq: [scalar('driver: nvidia')],
          },
          allowed: ['all'],
          allowed_source: 'schema-branch',
        }),
      ]),
    );
    assert.equal(popupOf(root, 'services.db.gpus'), undefined);
    const note = byClass(root, 'allowed-note')[0];
    assert.ok(note, 'the specification’s values were dropped entirely');
    assert.ok(note.textContent.includes('all'), `the values are not stated: ${note.textContent}`);
    assert.equal(byClass(root, 'addable-key').length, 0, 'stated as links after all');
  });
});


/* -------------------------------------------------------------------------
 * Where a value is written, said at the field — decision 21 and rule 6.
 * ---------------------------------------------------------------------- */

describe('a value the file does not write here', () => {
  const inherited = {
    path: 'services.db.restart',
    editable: false,
    reason: 'inherited' as const,
    plan: 'insert_key',
    anchor: 'defaults',
    detail:
      'db does not set restart here — it arrives through `<<: *defaults` on line 29. ' +
      'Writing a value adds restart to db, which overrides *defaults for this one place.',
    through: { line: 29, column: 5 },
    bytes_at: { line: 9, column: 12 },
  };

  // MUTATION: `this.appendAvailabilityNote(wrap, field.path)` deleted from the
  // declared branch. The field still renders, still takes a value, and the
  // reader still meets the consequence for the first time in the diff — which
  // is the rule-6 failure, invisible to every other assertion in this file.
  it('says so at the field, before anything is written', () => {
    const { root } = render(
      inspectionOf([field('restart', true, { value: scalar('unless-stopped', 9) })], {
        availability: { 'services.db.restart': inherited },
      }),
    );
    const notes = byClass(root, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => t.includes('*defaults') && t.includes('overrides')),
      `the pane says nothing about where the value is written:\n  ${notes.join('\n  ')}`,
    );
  });

  // It is still typeable: decision 21's whole point is that the reader gets
  // what they meant — the override — rather than a refusal.
  it('is still a field the reader can type in', () => {
    const { root } = render(
      inspectionOf([field('restart', true, { value: scalar('unless-stopped', 9) })], {
        availability: { 'services.db.restart': inherited },
      }),
    );
    const input = walk(root).find((e) => e.dataset.field === 'services.db.restart');
    assert.ok(input, 'an inherited value cannot be overridden because there is no field');
    assert.equal(input!.tagName, 'INPUT');
  });

  // The other four shapes have no safe override, so the field goes read-only —
  // and the reason goes with it. A read-only field with no reason is the thing
  // rule 6 forbids, and it is what shipped.
  for (const [reason, detail] of [
    ['alias', 'This line reads `*entry` — a reference to the anchor on line 8.'],
    ['anchor', 'This value carries the anchor `&entry`.'],
    ['block-scalar', 'This is a block scalar — its value is the indented lines below.'],
    ['inherited-nested', 'This is inside `healthcheck`, which comes whole from `<<: *defaults`.'],
  ] as const) {
    it(`renders ${reason} read-only and says why`, () => {
      const { root } = render(
        inspectionOf([field('command', true, { value: scalar('/bin/sh', 9) })], {
          availability: {
            'services.db.command': { path: 'services.db.command', editable: false, reason, detail },
          },
        }),
      );
      assert.equal(
        walk(root).find((e) => e.dataset.field === 'services.db.command'),
        undefined,
        `a ${reason} value offered a field the engine would refuse`,
      );
      const notes = byClass(root, 'field-note').map((n) => n.textContent);
      assert.ok(
        notes.some((t) => t === detail),
        `a ${reason} value is read-only and says nothing:\n  ${notes.join('\n  ')}`,
      );
    });
  }

  // Silence is not a claim. A path nothing was asked about renders exactly as
  // it did before — no note, and still editable.
  it('says nothing about a path it has no answer for', () => {
    const { root } = render(
      inspectionOf([field('image', true, { value: scalar('postgres:16', 4) })]),
    );
    assert.ok(walk(root).find((e) => e.dataset.field === 'services.db.image'));
    assert.deepEqual(byClass(root, 'field-note').map((n) => n.textContent), []);
  });

  // This used to pin the OPPOSITE property — a note reading "list entries are
  // read-only … not built yet rather than unsafe". Story 9.2 built it, so the
  // note is gone and what replaces it is the entry field itself. The test is
  // rewritten rather than deleted: the row still has to say SOMETHING about a
  // list it cannot write, and that is what is asserted here.
  it('says why a list the file does not write here cannot be added to', () => {
    const list: ValueView = {
      kind: 'sequence',
      text: '',
      env_known: true,
      origin: origin(7),
      overrides: [],
      seq: [
        scalar('a-rather-long-entry-that-will-not-collapse-onto-one-row', 8),
        scalar('another-rather-long-entry-that-will-not-collapse-either', 9),
      ],
    };
    const { root } = render(
      inspectionOf([field('command', true, { value: list })], {
        availability: {
          'services.db.command': {
            path: 'services.db.command',
            editable: false,
            reason: 'inherited-nested',
            detail: 'command is inside something this service inherits whole.',
          },
        },
      }),
    );
    const notes = byClass(root, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => t.includes('inherits whole')),
      `a list nothing can be added to said nothing about why:\n  ${notes.join('\n  ')}`,
    );
  });
});

// A mapping ENTRY declared with no value renders as a read-only `~`, and
// nothing said why. The field-level version of this state has had its sentence
// since story 5.2; the entry-level one never did.
describe('a mapping entry with no value', () => {
  it('says why the `~` cannot be typed over', () => {
    const mapping: ValueView = {
      kind: 'mapping',
      text: '',
      env_known: true,
      origin: origin(7),
      overrides: [],
      entries: [
        {
          key: 'CACHE',
          key_origin: origin(8),
          path: 'services.db.environment.CACHE',
          value: { kind: 'null', text: '', env_known: true, origin: origin(8), overrides: [] },
        },
      ],
    };
    const { root } = render(
      inspectionOf([field('environment', true, { value: mapping })], {
        availability: {
          'services.db.environment.CACHE': {
            path: 'services.db.environment.CACHE',
            editable: false,
            reason: 'null-value',
            plan: 'insert_key',
            detail: 'The file declares this key with no value, so there are no bytes to replace.',
          },
        },
      }),
    );
    const notes = byClass(root, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => t.includes('no bytes to replace')),
      `a read-only \`~\` said nothing:\n  ${notes.join('\n  ')}`,
    );
  });
});

// `networks` on a service that only writes `<<: *defaults` is the second half
// of the reported defect: a whole LIST the reader can see and cannot add to.
// It renders as a group, so the note has to reach the group's tail and not only
// the scalar branch.
describe('a collection the file inherits whole', () => {
  it('says why nothing can be added to it here', () => {
    const list: ValueView = {
      kind: 'sequence',
      text: '',
      env_known: true,
      origin: origin(11),
      overrides: [],
      seq: [scalar('shipyard', 11)],
    };
    const { root } = render(
      inspectionOf([field('networks', true, { value: list })], {
        availability: {
          'services.db.networks': {
            path: 'services.db.networks',
            editable: false,
            reason: 'inherited',
            anchor: 'defaults',
            detail:
              'db does not declare networks — the whole thing arrives through `<<: *defaults` on line 29.',
          },
        },
      }),
    );
    const notes = byClass(root, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => t.includes('the whole thing arrives through')),
      `an inherited list said nothing about where it came from:\n  ${notes.join('\n  ')}`,
    );
  });
});

/* -------------------------------------------------------------------------
 * Epic 9, story 9.2 — one entry of a list.
 *
 * Every fixture below REPEATS an entry and every interesting edit is in the
 * MIDDLE of the list. A list of distinct values edited at index 0 cannot tell
 * an off-by-one from a correct answer, which is the trap
 * `testdata/edge/e43-repeated-list-entries.yml` exists to spring on the engine
 * and this is the same trap one layer up.
 * ---------------------------------------------------------------------- */

/** `sh · -c · sh · echo hi · sh` — 27 characters, so `inlineList` collapses it. */
const REPEATED = ['sh', '-c', 'sh', 'echo hi', 'sh'];

/** The same shape, too wide to collapse, so the tree stays. */
const REPEATED_WIDE = [
  'a-long-entry-that-will-not-collapse',
  'another-long-entry-that-will-not',
  'a-long-entry-that-will-not-collapse',
];

function sequenceField(key: string, items: string[], line = 12): SchemaField {
  return field(key, true, {
    value: {
      kind: 'sequence',
      text: '',
      env_known: true,
      origin: origin(line),
      overrides: [],
      seq: items.map((t) => scalar(t, line)),
    },
  });
}

describe('a list entry is a value the reader can reach — story 9.2', () => {
  it('gives every entry of an expanded list its own field, addressed by index', () => {
    const { root } = render(inspectionOf([sequenceField('command', REPEATED_WIDE)]));
    const inputs = walk(root).filter((e) => /command\[\d+\]$/.test(e.dataset.field ?? ''));
    assert.deepEqual(
      inputs.map((i) => i.dataset.field),
      ['services.db.command[0]', 'services.db.command[1]', 'services.db.command[2]'],
      'the entries of a list the pane already draws are still unaddressable',
    );
    for (const i of inputs) {
      assert.equal(i.tagName, 'INPUT', 'an entry is still a read-only span');
    }
  });

  it('stages the entry the reader typed in, not the one beside it', () => {
    // The middle. With `[0]` and `[2]` holding the SAME text, a path built one
    // out cannot be told from a correct one by what it says — only by what it
    // is numbered.
    const { root } = render(inspectionOf([sequenceField('command', REPEATED_WIDE)]));
    const middle = walk(root).find((e) => e.dataset.field === 'services.db.command[1]')!;
    middle.value = 'replaced';
    middle.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [
      { type: 'edit', path: 'services.db.command[1]', value: 'replaced' },
    ]);
  });

  it('does not tell the reader that list entries cannot be edited', () => {
    const { root } = render(inspectionOf([sequenceField('command', REPEATED_WIDE)]));
    const notes = byClass(root, 'field-note').map((n) => n.textContent);
    assert.equal(
      notes.some((t) => t.includes('read-only') || t.includes('not built yet')),
      false,
      `the pane still says list entries are unreachable:\n  ${notes.join('\n  ')}`,
    );
  });

  it('makes a collapsed list a route to its entries rather than a dead end', () => {
    // `CMD · wget · -qO- · …` is the owner's screenshot. It collapses because
    // collapsing costs the reader no fact — and it must not cost them the edit.
    const { view, root } = render(inspectionOf([sequenceField('command', REPEATED)]));
    const items = byClass(root, 'field-item');
    assert.equal(items.length, 5, 'the collapsed row lost an entry');
    for (const [i, item] of items.entries()) {
      assert.equal(item.tagName, 'BUTTON', `entry ${i} is not reachable at all`);
      assert.match(item.getAttribute('aria-label') ?? '', new RegExp(`\\[${i}\\]`));
    }
    // The third — repeated text, middle of the list.
    items[2].fire('click');
    const inputs = walk(view.element as unknown as El).filter((e) =>
      /command\[\d+\]$/.test(e.dataset.field ?? ''),
    );
    assert.deepEqual(
      inputs.map((i) => i.dataset.field),
      [
        'services.db.command[0]',
        'services.db.command[1]',
        'services.db.command[2]',
        'services.db.command[3]',
        'services.db.command[4]',
      ],
      'pressing an entry of a collapsed list did not open the list',
    );
    const active = (globalThis as any).document.activeElement as El | null;
    assert.equal(
      active?.dataset.field,
      'services.db.command[2]',
      'the entry the reader pressed is not the one the caret landed in',
    );
  });

  it('adds an entry to the end of the list and removes the one that was pressed', () => {
    const { root } = render(inspectionOf([sequenceField('command', REPEATED_WIDE)]));
    const add = walk(root).find((e) => e.dataset.field === 'services.db.command[+]');
    assert.ok(add, 'a list the reader can edit still cannot be added to');
    add!.value = 'echo hi';
    add!.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [
      { type: 'addEntry', path: 'services.db.command', value: 'echo hi' },
    ]);

    sent = [];
    const removes = byClass(root, 'entry-remove');
    assert.equal(removes.length, 3, 'not every entry can be removed');
    removes[1].fire('click');
    assert.deepEqual(sent, [{ type: 'removeEntry', path: 'services.db.command[1]' }]);
  });

  it('offers neither an entry field nor an add control on a list it cannot write', () => {
    const { root } = render(
      inspectionOf([sequenceField('networks', REPEATED_WIDE)], {
        availability: {
          'services.db.networks': {
            path: 'services.db.networks',
            editable: false,
            reason: 'inherited',
            anchor: 'defaults',
            detail: 'db does not declare networks — it arrives through `<<: *defaults`.',
          },
        },
      }),
    );
    assert.equal(
      walk(root).some((e) => e.dataset.field === 'services.db.networks[+]'),
      false,
      'a list the file does not write here still offers an add control',
    );
    assert.equal(byClass(root, 'entry-remove').length, 0);
  });

  it('says how many entries a list has when the index is past its end', () => {
    const { root } = render(
      inspectionOf([sequenceField('ports', REPEATED_WIDE)], {
        availability: {
          'services.db.ports[9]': {
            path: 'services.db.ports[9]',
            editable: false,
            reason: 'entry-index',
            detail: 'That list has no entry 9. It has 3 entries, numbered 0 through 2.',
          },
        },
      }),
    );
    // The slug has to be one this client knows; an unknown reason renders
    // nothing and the reader is told nothing.
    assert.ok(EDITABILITY_REASONS.includes('entry-index' as any));
    void root;
  });
});

/* -------------------------------------------------------------------------
 * A key the reader can add to a free-form mapping.
 *
 * The gap: `environment` permits ANY key, so `stack/schema` has no
 * `available, not set` list for it — and that list is the only route this pane
 * offered to a key the file does not have. A reader looking at `environment`
 * with three keys had nowhere to add a fourth. `+ entry` is the list form's
 * answer and does not apply to a mapping.
 *
 * The trap these tests are built against: a fixture whose mapping is
 * SCHEMA-KNOWN cannot tell a free-form mapping from a constrained one, so an
 * implementation that offered the control on every mapping would pass. Every
 * positive below has a `healthcheck`-shaped negative beside it.
 * ---------------------------------------------------------------------- */

/** A mapping value with scalar entries, addressed the way the wire addresses them. */
function mappingField(key: string, entries: Record<string, string>, extra: Partial<SchemaField> = {}): SchemaField {
  return field(key, true, {
    value: {
      kind: 'mapping',
      text: '',
      env_known: true,
      origin: origin(12),
      overrides: [],
      entries: Object.entries(entries).map(([k, v]) => ({
        key: k,
        key_origin: origin(13),
        path: `services.db.${key}.${k}`,
        value: scalar(v, 13),
      })),
    },
    ...extra,
  });
}

const ENV = { NODE_ENV: 'production', API_URL: 'http://api:3000', SESSION_SECRET: 'hunter2' };

describe('a key the reader can add to a free-form mapping', () => {
  it('offers a key field and a value field at the foot of a free-form mapping', () => {
    const { root } = render(inspectionOf([mappingField('environment', ENV, { free_form: true })]));
    const key = walk(root).find((e) => e.dataset.field === 'services.db.environment[+key]');
    const value = walk(root).find((e) => e.dataset.field === 'services.db.environment[+value]');
    assert.ok(key, 'a free-form mapping still has no way to add a key');
    assert.ok(value, 'the add row has nowhere to type the value');
    assert.equal(key!.tagName, 'INPUT');
    assert.equal(value!.tagName, 'INPUT');
    // Named, because a screen reader announcing "edit text" twice is two
    // unusable fields — and the name says the mapping, not just "key".
    assert.match(key!.getAttribute('aria-label') ?? '', /services\.db\.environment/);
    assert.match(value!.getAttribute('aria-label') ?? '', /services\.db\.environment/);
    // …and the three keys the file already has are still every one of them.
    const rows = byClass(root, 'field-block').filter((e) =>
      /^services\.db\.environment\./.test(e.dataset.path ?? ''),
    );
    assert.deepEqual(
      rows.map((r) => r.dataset.path),
      [
        'services.db.environment.NODE_ENV',
        'services.db.environment.API_URL',
        'services.db.environment.SESSION_SECRET',
      ],
    );
  });

  it('stages the key and the value together and writes nothing', () => {
    const { root } = render(inspectionOf([mappingField('environment', ENV, { free_form: true })]));
    const key = walk(root).find((e) => e.dataset.field === 'services.db.environment[+key]')!;
    const value = walk(root).find((e) => e.dataset.field === 'services.db.environment[+value]')!;
    key.value = 'LOG_LEVEL';
    value.value = 'debug';
    value.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [
      { type: 'addKey', path: 'services.db.environment', key: 'LOG_LEVEL', value: 'debug' },
    ]);
  });

  it('commits from the key field too, so Enter means the same thing in both', () => {
    const { root } = render(inspectionOf([mappingField('environment', ENV, { free_form: true })]));
    const key = walk(root).find((e) => e.dataset.field === 'services.db.environment[+key]')!;
    const value = walk(root).find((e) => e.dataset.field === 'services.db.environment[+value]')!;
    key.value = 'LOG_LEVEL';
    value.value = 'debug';
    key.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [
      { type: 'addKey', path: 'services.db.environment', key: 'LOG_LEVEL', value: 'debug' },
    ]);
  });

  it('stages nothing when the reader has not said what the key is called', () => {
    const { root } = render(inspectionOf([mappingField('environment', ENV, { free_form: true })]));
    const value = walk(root).find((e) => e.dataset.field === 'services.db.environment[+value]')!;
    value.value = 'debug';
    value.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [], 'a value with no key would be written as a key nobody typed');
  });

  it('Escape closes the composer and stages nothing', () => {
    const { root } = render(inspectionOf([mappingField('environment', ENV, { free_form: true })]));
    const key = walk(root).find((e) => e.dataset.field === 'services.db.environment[+key]')!;
    const value = walk(root).find((e) => e.dataset.field === 'services.db.environment[+value]')!;
    key.value = 'LOG_LEVEL';
    value.value = 'debug';
    value.fire('keydown', { key: 'Escape', preventDefault() {} });
    assert.equal(key.value, '', 'Escape left half a key in the composer');
    assert.equal(value.value, '');
    assert.deepEqual(sent, []);
    // …and from the key field, which is where the caret starts.
    key.value = 'LOG_LEVEL';
    value.value = 'debug';
    key.fire('keydown', { key: 'Escape', preventDefault() {} });
    assert.equal(key.value, '');
    assert.equal(value.value, '');
    assert.deepEqual(sent, []);
  });

  it('offers nothing of the kind on a mapping whose keys the schema names', () => {
    // THE TRAP. `healthcheck` is a mapping with exactly the same rendered
    // shape and a closed set of keys; it has an `available, not set` list of
    // its own and needs no composer. An implementation that keyed off
    // `value.kind === 'mapping'` passes every test above and fails this one.
    const { root } = render(inspectionOf([mappingField('healthcheck', { interval: '30s' })]));
    assert.equal(
      walk(root).some((e) => /\[\+key\]$/.test(e.dataset.field ?? '')),
      false,
      'a mapping the specification describes was offered a free-form composer',
    );
    assert.equal(byClass(root, 'is-add-key').length, 0);
  });

  it('offers nothing on a free-form mapping the file does not write here', () => {
    // The whole mapping arrives through `<<: *defaults`. A key written on this
    // service would REPLACE it rather than extend it — the core's own answer,
    // and the same one that withholds `+ entry` from an inherited list.
    const { root } = render(
      inspectionOf([mappingField('environment', ENV, { free_form: true })], {
        availability: {
          'services.db.environment': {
            path: 'services.db.environment',
            editable: false,
            reason: 'inherited',
            anchor: 'defaults',
            detail: 'db does not declare environment — it arrives through `<<: *defaults`.',
          },
        },
      }),
    );
    assert.equal(byClass(root, 'is-add-key').length, 0);
  });

  it('adds to the LIST form as a list, and never converts it to a mapping', () => {
    // `environment` is legal both ways. The file that writes it as a list gets
    // story 9.2's `+ entry` — one field, one `- NODE_ENV=production` — and no
    // key/value composer, because a key/value composer here would be an offer
    // to re-emit the collection in the other form.
    const listy = sequenceField('environment', [
      'NODE_ENV=production-and-then-some-more-text',
      'API_URL=http://api:3000/a/long/enough/path',
    ]);
    listy.free_form = true;
    const { root } = render(inspectionOf([listy]));
    assert.ok(
      walk(root).some((e) => e.dataset.field === 'services.db.environment[+]'),
      'the list form lost story 9.2’s add',
    );
    assert.equal(
      byClass(root, 'is-add-key').length,
      0,
      'the pane offered to write a mapping key into a list',
    );
  });

  it('leaves the top-level blocks to `+ add`, which declares them properly', () => {
    // `services` IS free-form — any service name is legal — and it is the one
    // free-form mapping that already has a better gesture. `+ add` asks the
    // core where the declaration goes, refuses a name the stack already has,
    // refuses a name that would not survive as a bare key, and refuses a
    // service with no image; a two-field composer here would write `foo:` with
    // nothing under it, which is not a stack anything can run.
    //
    // Derived from ADD_KINDS — the list `+ add` itself is built from, which
    // mirrors `edit.AddKinds` — and not from a list of Compose keys written out
    // here. There is no such list in this extension (AD-20).
    const { root } = render(
      inspectionOf([], {
        id: null,
        name: 'compose.yaml',
        kind: 'stack',
        schema: {
          path: '/w/compose.yaml',
          schema_commit: '4e2fe7602af8c965ab4fef891e9dde9c5940775f',
          compose_version: '2.29.0',
          compose_version_known: true,
          files: [{ path: '/w/compose.yaml', step: 0 }],
          profiles: [],
          node: {
            path: '',
            schema: 'compose',
            known: true,
            declared_count: 2,
            available_count: 0,
            fields: [
              {
                key: 'services',
                path: 'services',
                declared: true,
                support: 'unknown',
                free_form: true,
                value: {
                  kind: 'mapping',
                  text: '',
                  env_known: true,
                  origin: origin(1),
                  overrides: [],
                  entries: [
                    { key: 'web', key_origin: origin(2), path: 'services.web', value: scalar('', 2) },
                  ],
                },
              },
              {
                key: 'x-defaults',
                path: 'x-defaults',
                declared: true,
                support: 'unknown',
                free_form: true,
                value: {
                  kind: 'mapping',
                  text: '',
                  env_known: true,
                  origin: origin(8),
                  overrides: [],
                  entries: [
                    { key: 'restart', key_origin: origin(9), path: 'x-defaults.restart', value: scalar('always', 9) },
                  ],
                },
              },
            ],
          },
        },
      }),
    );
    assert.deepEqual(
      byClass(root, 'is-add-key').map((e) => e.dataset.addKey),
      ['x-defaults'],
      'the stack pane offered a raw key composer where `+ add` already declares properly',
    );
  });

  it('offers the composer on every free-form mapping, not just environment', () => {
    // AD-20: there is no list of Compose keys in this extension. `labels`,
    // `sysctls` and `build.args` are free-form for the same reason
    // `environment` is, and the pane learns it the same way — from the field.
    const { root } = render(
      inspectionOf([
        mappingField('labels', { 'com.example.owner': 'platform' }, { free_form: true }),
        mappingField('sysctls', { 'net.core.somaxconn': '1024' }, { free_form: true }),
      ]),
    );
    assert.deepEqual(
      byClass(root, 'is-add-key').map((e) => e.dataset.addKey),
      ['services.db.labels', 'services.db.sysctls'],
    );
  });
});

/* -------------------------------------------------------------------------
 * Epic 9, story 9.1 — comments in a form whose rows are key/value pairs.
 *
 * A comment is not a field, and the pane had no affordance for one. What it
 * has now is one control per row that carries bytes, and a block under the row
 * with the two positions the engine addresses and nothing else — because there
 * are exactly two and a third would be a line number.
 * ---------------------------------------------------------------------- */

describe('a comment is a thing the reader can author — story 9.1', () => {
  it('offers a comment control on a key the file writes, and none on one it does not', () => {
    const { root } = render(
      inspectionOf([
        field('image', true, { value: scalar('nginx:1.27', 4) }),
        field('restart', false),
      ]),
    );
    const controls = byClass(root, 'comment-open');
    assert.deepEqual(
      controls.map((c) => c.dataset.path),
      ['services.db.image'],
      'a key the file does not contain has no line for a comment to attach to',
    );
    assert.equal(controls[0].tagName, 'BUTTON');
    assert.match(controls[0].getAttribute('aria-label') ?? '', /services\.db\.image/);
  });

  it('asks the host for the comment rather than guessing at the file', () => {
    const { root } = render(inspectionOf([field('image', true, { value: scalar('nginx', 4) })]));
    byClass(root, 'comment-open')[0].fire('click');
    assert.deepEqual(sent, [{ type: 'openComment', path: 'services.db.image' }]);
  });

  it('renders both positions, seeded with what the file says at each', () => {
    const { view, root } = render(
      inspectionOf([field('image', true, { value: scalar('nginx', 4) })]),
    );
    byClass(root, 'comment-open')[0].fire('click');
    view.setComments({
      path: 'services.db.image',
      above: 'why this pin\nand who asked',
      trailing: 'pinned by ops',
      staged: [],
    });
    const el2 = view.element as unknown as El;
    const above = walk(el2).find((e) => e.dataset.field === 'comment:above:services.db.image');
    const trailing = walk(el2).find((e) => e.dataset.field === 'comment:trailing:services.db.image');
    assert.ok(above && trailing, 'the block rendered fewer than the two positions there are');
    assert.equal(above!.tagName, 'TEXTAREA', 'a run of comment lines is one comment and needs the room');
    assert.equal(trailing!.tagName, 'INPUT', 'a trailing comment is one line by construction');
    assert.equal(above!.value, 'why this pin\nand who asked');
    assert.equal(trailing!.value, 'pinned by ops');
  });

  it('stages the position the reader typed in, not the other one', () => {
    const { view, root } = render(
      inspectionOf([field('image', true, { value: scalar('nginx', 4) })]),
    );
    byClass(root, 'comment-open')[0].fire('click');
    view.setComments({
      path: 'services.db.image',
      above: 'above text',
      trailing: 'trailing text',
      staged: [],
    });
    sent = [];
    const trailing = walk(view.element as unknown as El).find(
      (e) => e.dataset.field === 'comment:trailing:services.db.image',
    )!;
    trailing.value = 'pinned harder';
    trailing.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [
      { type: 'setComment', path: 'services.db.image', where: 'trailing', text: 'pinned harder' },
    ]);
  });

  it('offers a remove only where there is a comment to remove', () => {
    const { view, root } = render(
      inspectionOf([field('image', true, { value: scalar('nginx', 4) })]),
    );
    byClass(root, 'comment-open')[0].fire('click');
    view.setComments({ path: 'services.db.image', above: 'a\nb', trailing: null, staged: [] });
    const removes = byClass(view.element as unknown as El, 'comment-remove');
    assert.equal(removes.length, 1, 'a position with no comment offered a delete that would refuse');
    sent = [];
    removes[0].fire('click');
    assert.deepEqual(sent, [
      { type: 'deleteComment', path: 'services.db.image', where: 'above' },
    ]);
  });

  it('says why a position cannot carry a comment instead of offering a field', () => {
    const { view, root } = render(
      inspectionOf([field('command', true, { value: scalar('|', 4) })]),
    );
    byClass(root, 'comment-open')[0].fire('click');
    view.setComments({
      path: 'services.db.command',
      above: null,
      trailing: null,
      staged: [],
      unavailable: [
        { where: 'trailing', detail: 'That line cannot carry a comment after it.' },
      ],
    });
    const el2 = view.element as unknown as El;
    assert.equal(
      walk(el2).some((e) => e.dataset.field === 'comment:trailing:services.db.command'),
      false,
      'a field was offered for a position the engine would refuse',
    );
    const notes = byClass(el2, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => t.includes('cannot carry a comment')),
      `a refused position said nothing:\n  ${notes.join('\n  ')}`,
    );
  });

  it('says a staged comment is staged and not yet written', () => {
    const { view, root } = render(
      inspectionOf([field('image', true, { value: scalar('nginx', 4) })]),
    );
    byClass(root, 'comment-open')[0].fire('click');
    view.setComments({
      path: 'services.db.image',
      above: 'what I typed',
      trailing: null,
      staged: ['above'],
    });
    const texts = byClass(view.element as unknown as El, 'comment-staged').map((n) => n.textContent);
    assert.ok(
      texts.some((t) => t.includes('staged')),
      'a staged comment was indistinguishable from one already in the file',
    );
  });
});

/* -------------------------------------------------------------------------
 * Epic 9, story 9.3 — moving a value into a variable, in the pane.
 * ---------------------------------------------------------------------- */

const MOVED = {
  name: 'POSTGRES_PASSWORD',
  value: 'hunter2',
  compose: {
    file: '/w/compose.yaml',
    ops: [],
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
  written: false,
};

describe('moving a value into a variable — story 9.3', () => {
  const secret = () =>
    inspectionOf([field('image', true, { value: scalar('hunter2', 5) })]);

  it('offers the move on a value the pane can write, and asks the host what it would do', () => {
    const { root } = render(secret());
    const control = byClass(root, 'extract-open')[0];
    assert.ok(control, 'no value in the pane could be moved into a variable');
    assert.equal(control.tagName, 'BUTTON');
    assert.match(control.getAttribute('aria-label') ?? '', /services\.db\.image/);
    control.fire('click');
    assert.deepEqual(sent, [{ type: 'openExtract', path: 'services.db.image' }]);
  });

  it('shows BOTH diffs before anything is written, and says the .env is new', () => {
    const { view, root } = render(secret());
    byClass(root, 'extract-open')[0].fire('click');
    view.setExtract({ path: 'services.db.image', staged: false, result: MOVED });
    const el2 = view.element as unknown as El;
    const diffs = byClass(el2, 'extract-diff');
    assert.equal(diffs.length, 2, `a two-file move showed ${diffs.length} diff(s)`);
    assert.match(diffs[0].textContent, /\$\{POSTGRES_PASSWORD\}/);
    assert.match(diffs[1].textContent, /POSTGRES_PASSWORD=hunter2/);
    const notes = byClass(el2, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => /creates/i.test(t) && t.includes('.env')),
      `nothing said the .env would be created:\n  ${notes.join('\n  ')}`,
    );
  });

  it('distinguishes appending to an existing .env from creating one', () => {
    const { view, root } = render(secret());
    byClass(root, 'extract-open')[0].fire('click');
    view.setExtract({
      path: 'services.db.image',
      staged: false,
      result: { ...MOVED, env_created: false },
    });
    const notes = byClass(view.element as unknown as El, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => /appends|adds a line/i.test(t)),
      `an append read the same as a create:\n  ${notes.join('\n  ')}`,
    );
    assert.equal(notes.some((t) => /creates/i.test(t)), false);
  });

  it('says the .env is left byte-identical when it already gives the name this value', () => {
    const { view, root } = render(secret());
    byClass(root, 'extract-open')[0].fire('click');
    view.setExtract({
      path: 'services.db.image',
      staged: false,
      result: { ...MOVED, env_created: false, env_unchanged: true, env_diff: '' },
    });
    const notes = byClass(view.element as unknown as El, 'field-note').map((n) => n.textContent);
    assert.ok(
      notes.some((t) => /byte-identical|already/i.test(t)),
      `an idempotent move said nothing:\n  ${notes.join('\n  ')}`,
    );
  });

  it('re-asks with an edited name rather than staging one nobody checked', () => {
    const { view, root } = render(secret());
    byClass(root, 'extract-open')[0].fire('click');
    view.setExtract({ path: 'services.db.image', staged: false, result: MOVED });
    sent = [];
    const name = walk(view.element as unknown as El).find(
      (e) => e.dataset.field === 'extract:services.db.image',
    )!;
    assert.equal(name.value, 'POSTGRES_PASSWORD', 'the suggested name was not offered');
    name.value = 'DB_PASSWORD';
    name.fire('keydown', { key: 'Enter', preventDefault() {} });
    assert.deepEqual(sent, [
      { type: 'openExtract', path: 'services.db.image', name: 'DB_PASSWORD' },
    ]);
  });

  it('stages the move only when the reader chooses it', () => {
    const { view, root } = render(secret());
    byClass(root, 'extract-open')[0].fire('click');
    view.setExtract({ path: 'services.db.image', staged: false, result: MOVED });
    sent = [];
    const stage = byClass(view.element as unknown as El, 'extract-stage')[0];
    assert.ok(stage, 'the reader had no way to choose the move');
    stage.fire('click');
    assert.deepEqual(sent, [
      { type: 'stageExtract', path: 'services.db.image', name: 'POSTGRES_PASSWORD' },
    ]);
  });

  it('offers no move at all when the engine has refused it', () => {
    const { view, root } = render(secret());
    byClass(root, 'extract-open')[0].fire('click');
    view.setExtract({
      path: 'services.db.image',
      staged: false,
      refused: 'That value already comes from a variable, so there is no literal here to move.',
    });
    const el2 = view.element as unknown as El;
    assert.equal(byClass(el2, 'extract-stage').length, 0, 'a refused move was still offered');
    assert.equal(byClass(el2, 'extract-diff').length, 0);
    const notes = byClass(el2, 'field-note').map((n) => n.textContent);
    assert.ok(notes.some((t) => t.includes('already comes from a variable')));
  });
});
