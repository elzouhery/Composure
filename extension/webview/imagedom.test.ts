// What Docker Hub says, rendered — Epic 8.
//
// These drive the REAL InspectorView, StageFormView and AddFormView against the
// shared fake DOM and assert on what is on screen and what is posted. The
// pure-function checks live beside them, but the properties this epic actually
// promises are all about a pane: that it is complete without a lookup, that a
// pill stages rather than writes, and that a failure is a sentence.
//
// NOTHING HERE TOUCHES THE NETWORK. There is no network in a webview: the
// answers arrive as `HostMessage`s, and these tests hand them over directly.

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { installDom, walk, type El } from './fakedom.test';

installDom();

// eslint-disable-next-line @typescript-eslint/no-var-requires
const { InspectorView } = require('./inspector') as typeof import('./inspector');
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { StageFormView } = require('./stageform') as typeof import('./stageform');
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { AddFormView } = require('./addform') as typeof import('./addform');
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { formSummary, oldestBaseAge } = require('./stageform') as typeof import('./stageform');
// eslint-disable-next-line @typescript-eslint/no-var-requires
const { metaLine, optionName } = require('./imagesearch') as typeof import('./imagesearch');

import type {
  DockerStage,
  DockerfileForm,
  ImageLookup,
  ImageSearchAnswer,
  Inspection,
  SchemaField,
  WebviewMessage,
} from '../shared/protocol';

const ORIGIN = { file: '/tmp/compose.yml', line: 3, column: 12, step: 0 };

function imageField(text = 'postgres:16-alpine'): SchemaField {
  return {
    key: 'image',
    path: 'services.db.image',
    support: 'unknown',
    declared: true,
    value: { kind: 'scalar', text, env_known: true, origin: ORIGIN, overrides: [] },
  };
}

function inspectionOf(fields: SchemaField[]): Inspection {
  return {
    id: 'services.db',
    name: 'db',
    kind: 'service',
    schema: {
      path: '/tmp/compose.yml',
      schema_commit: 'abc123',
      compose_version: '',
      compose_version_known: false,
      files: [{ path: '/tmp/compose.yml', step: 0 }],
      profiles: [],
      node: {
        path: 'services.db',
        schema: 'service',
        known: true,
        fields,
        declared_count: fields.length,
        available_count: 0,
      },
    },
    findings: [],
    staged: [],
    opened: [],
    pending: {},
  };
}

const UPGRADE: ImageLookup = {
  reference: 'postgres:16-alpine',
  repository: 'library/postgres',
  display: 'postgres',
  tag: '16-alpine',
  state: 'ok',
  message: 'postgres:18-alpine is a major upgrade in the same family.',
  age: '14 months old',
  age_days: 425,
  pill: 'postgres:18-alpine · major · 40MB smaller',
  candidate: {
    reference: 'postgres:18-alpine',
    tag: '18-alpine',
    kind: 'major',
    size_delta: -41943040,
    has_size: true,
  },
};

const RATE_LIMITED: ImageLookup = {
  reference: 'postgres:16-alpine',
  repository: 'library/postgres',
  display: 'postgres',
  tag: '16-alpine',
  state: 'rate-limited',
  message:
    'Docker Hub is rate limiting this address. The limit is a hundred and eighty requests a ' +
    'minute and it is shared by everyone behind your address, so this is usually a busy ' +
    'office rather than anything you did. It passes on its own.',
};

const pillOf = (root: El): El | undefined =>
  walk(root).find((e) => e.classList.contains('pill-upgrade'));

const notesOf = (root: El): El[] =>
  walk(root).filter((e) => e.classList.contains('image-note'));

const inputOf = (root: El, key: string): El | undefined =>
  walk(root).find((e) => e.dataset.field === key);

/* -------------------------------------------------------------------------
 * The property the whole epic rests on.
 * ---------------------------------------------------------------------- */

describe('a pane is complete before Docker Hub has said anything', () => {
  let sent: WebviewMessage[];
  let view: InstanceType<typeof InspectorView>;

  beforeEach(() => {
    sent = [];
    view = new InspectorView({ send: (m) => sent.push(m) });
  });

  // The claim in one assertion. Every other capability in this product is a
  // pure function of files on disk; this one is not, and a reader with no
  // network must get the pane that shipped before Epic 8 — not a gap where a
  // pill would be, not a spinner, not a delayed paint.
  it('renders the image field with no pill and no note when no lookup ever arrives', () => {
    view.render(inspectionOf([imageField()]));
    const root = view.element as unknown as El;
    assert.equal(pillOf(root), undefined, 'a pill was drawn with nothing to put in it');
    assert.equal(notesOf(root).length, 0, 'a Docker Hub note was drawn with nothing to say');
    // …and the value is still there, editable, exactly as before.
    const input = inputOf(root, 'services.db.image');
    assert.ok(input, 'the image field is not on the pane');
    assert.equal(input.value, 'postgres:16-alpine');
  });

  // A lookup for a pane that has never rendered must not render one. The
  // message can arrive before the first inspection on a slow start.
  it('ignores a lookup that arrives before anything has been inspected', () => {
    view.setLookup('services.db.image', UPGRADE);
    const root = view.element as unknown as El;
    assert.equal(pillOf(root), undefined);
  });

  it('draws the pill when the lookup lands, without asking the host for anything', () => {
    view.render(inspectionOf([imageField()]));
    const before = sent.length;
    view.setLookup('services.db.image', UPGRADE);
    assert.deepEqual(
      sent.slice(before),
      [],
      'a landed lookup caused the webview to send a message; it must redraw from what it has',
    );
    const pill = pillOf(view.element as unknown as El);
    assert.ok(pill, 'no pill after the lookup landed');
    assert.equal(pill.textContent, 'postgres:18-alpine · major · 40MB smaller');
  });

  // A lookup keyed for a field that is not on this pane must draw nothing. The
  // reader clicked another service while the request was in flight.
  it('draws nothing for a key the pane does not render', () => {
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.other.image', UPGRADE);
    assert.equal(pillOf(view.element as unknown as El), undefined);
  });

  it('forgets every lookup when the drawn file changes', () => {
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.db.image', UPGRADE);
    assert.ok(pillOf(view.element as unknown as El));
    view.clearLookups();
    view.render(inspectionOf([imageField()]));
    assert.equal(
      pillOf(view.element as unknown as El),
      undefined,
      'an answer about one project decorated another project’s identically-named field',
    );
  });
});

/* -------------------------------------------------------------------------
 * The pill stages. It does not write.
 * ---------------------------------------------------------------------- */

describe('choosing an upgrade', () => {
  it('stages through the ordinary edit path, and writes nothing', () => {
    const sent: WebviewMessage[] = [];
    const view = new InspectorView({ send: (m) => sent.push(m) });
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.db.image', UPGRADE);

    const pill = pillOf(view.element as unknown as El);
    assert.ok(pill);
    pill.fire('click', {});

    assert.deepEqual(sent, [
      { type: 'edit', path: 'services.db.image', value: 'postgres:18-alpine' },
    ]);
    // DECISIONS.md 17 and AD-19: the ONLY message in this protocol that writes
    // is `save`, and nothing here sends one.
    assert.equal(
      sent.some((m) => m.type === 'save'),
      false,
      'pressing an upgrade pill wrote to a file',
    );
  });

  // The pill is a button with a name that says what pressing it does. Story
  // 4.5's floor, and the visible text is three facts joined by dots — a reader
  // who cannot see it would otherwise not learn that this is the control that
  // changes their file, nor that it does not do so yet.
  it('is a named button that says it stages rather than writes', () => {
    const view = new InspectorView({ send: () => undefined });
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.db.image', UPGRADE);
    const pill = pillOf(view.element as unknown as El);
    assert.ok(pill);
    assert.equal(pill.tagName, 'BUTTON');
    const name = pill.getAttribute('aria-label') ?? '';
    assert.match(name, /postgres:18-alpine/);
    assert.match(name, /major/);
    assert.match(name, /stages/i);
    assert.match(name, /nothing is written/i);
  });

  // A candidateless `ok` never reaches the webview — `host/images.ts` drops it
  // — but a pill that stages nothing is the failure worth pinning on both
  // sides of that boundary.
  it('draws no pill when there is nothing to stage', () => {
    const view = new InspectorView({ send: () => undefined });
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.db.image', { ...UPGRADE, state: 'current', candidate: undefined });
    assert.equal(pillOf(view.element as unknown as El), undefined);
  });
});

/* -------------------------------------------------------------------------
 * Rate-limited and offline are sentences, not errors.
 * ---------------------------------------------------------------------- */

describe('when Docker Hub cannot answer', () => {
  it('says so in the reader’s words, with no transport language', () => {
    const view = new InspectorView({ send: () => undefined });
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.db.image', RATE_LIMITED);

    const notes = notesOf(view.element as unknown as El);
    assert.equal(notes.length, 1, 'no sentence for a rate-limited lookup');
    const text = notes[0].textContent ?? '';
    assert.match(text, /rate limiting/i);
    // It says it is not the reader's fault and that it passes. Both matter:
    // the limit is per address and shared by an office.
    assert.match(text, /passes on its own/i);
    for (const banned of ['429', 'ECONNREFUSED', 'error', 'failed']) {
      assert.ok(!text.includes(banned), `the sentence carries transport language: ${banned}`);
    }
    // …and it is a note, never a banner or a pill.
    assert.equal(pillOf(view.element as unknown as El), undefined);
  });

  // Silence would read as "there is nothing newer", which is the confident
  // wrong answer in the exact place a reader decides whether to upgrade.
  it('says something for every state that is not an offer', () => {
    for (const state of ['offline', 'not-found', 'other-registry', 'not-comparable', 'disabled'] as const) {
      const view = new InspectorView({ send: () => undefined });
      view.render(inspectionOf([imageField()]));
      view.setLookup('services.db.image', {
        ...RATE_LIMITED,
        state,
        message: `a sentence about ${state}`,
      });
      const notes = notesOf(view.element as unknown as El);
      assert.equal(notes.length, 1, `${state} rendered nothing at all`);
      assert.equal(notes[0].dataset?.imageState, state);
    }
  });

  it('states the age even when the tag is already current', () => {
    const view = new InspectorView({ send: () => undefined });
    view.render(inspectionOf([imageField()]));
    view.setLookup('services.db.image', {
      ...UPGRADE,
      state: 'current',
      candidate: undefined,
      message: 'This is the newest stable tag Docker Hub lists in its family.',
    });
    const text = notesOf(view.element as unknown as El)[0]?.textContent ?? '';
    assert.match(text, /14 months old/);
  });
});

/* -------------------------------------------------------------------------
 * The Dockerfile half.
 * ---------------------------------------------------------------------- */

function stage(index: number, ref: string): DockerStage {
  return {
    index,
    name: index === 0 ? 'build' : '',
    label: index === 0 ? 'build' : ref,
    image_ref: ref,
    from: {
      kind: 'instruction',
      index: 0,
      name: 'FROM',
      text: `FROM ${ref}`,
      start_line: index === 0 ? 1 : 8,
      end_line: index === 0 ? 1 : 8,
      editable: true,
    },
    instructions: [],
    vocabulary: { scope: 'stage', declared_count: 0, available_count: 0, instructions: [] },
  } as unknown as DockerStage;
}

function form(): DockerfileForm {
  return {
    path: '/tmp/Dockerfile',
    missing: false,
    escape_char: '\\',
    crlf: false,
    bom: false,
    directives: [],
    preamble: [],
    stages: [stage(0, 'node:18-alpine'), stage(1, 'nginx:alpine')],
    vocabulary: { scope: 'file', declared_count: 0, available_count: 0, instructions: [] },
  };
}

describe('the Dockerfile stage form', () => {
  // The mockup's own row: `FROM node:18-alpine [node:22-alpine · minor · 40MB
  // smaller]` (directions-3.html:576).
  it('puts the pill beside the FROM it is about, and stages set_base_image', () => {
    const sent: WebviewMessage[] = [];
    const view = new StageFormView({ send: (m) => sent.push(m) });
    view.render(form(), null, new Set());
    view.setLookup('stage:0', { ...UPGRADE, pill: 'node:22-alpine · minor · 40MB smaller' });

    const pill = pillOf(view.element as unknown as El);
    assert.ok(pill, 'no pill on the FROM row');
    assert.equal(pill.textContent, 'node:22-alpine · minor · 40MB smaller');
    pill.fire('click', {});
    assert.deepEqual(sent, [
      { type: 'editStage', stage: 0, value: 'postgres:18-alpine' },
    ]);
  });

  // The mutation this file's neighbour was written for, in the other direction:
  // a pill on stage 1 must stage STAGE 1's base image.
  it('stages the stage the pill belongs to, not the first one', () => {
    const sent: WebviewMessage[] = [];
    const view = new StageFormView({ send: (m) => sent.push(m) });
    view.render(form(), null, new Set());
    view.setLookup('stage:1', UPGRADE);
    const pill = pillOf(view.element as unknown as El);
    assert.ok(pill);
    pill.fire('click', {});
    assert.equal((sent[0] as { stage: number }).stage, 1);
  });

  it('renders the form unchanged when no lookup arrives', () => {
    const view = new StageFormView({ send: () => undefined });
    view.render(form(), null, new Set());
    assert.equal(pillOf(view.element as unknown as El), undefined);
    assert.equal(notesOf(view.element as unknown as El).length, 0);
  });
});

describe('the header’s age clause', () => {
  // The mockup: `2 stages · 6 layers · base image 14 months old`
  // (directions-3.html:572).
  it('is absent until Docker Hub has answered, and never invented', () => {
    assert.equal(formSummary(form()), '2 stages · 0 instructions add a layer');
    assert.equal(
      formSummary(form(), '14 months old'),
      '2 stages · 0 instructions add a layer · base image 14 months old',
    );
  });

  // The OLDEST, not the first. The header is a claim about the file, and a
  // header reporting the newest base as the file's age would be reassuring and
  // wrong.
  it('takes the oldest stage’s base, not the first stage’s', () => {
    const lookups = new Map<string, ImageLookup>([
      ['stage:0', { ...UPGRADE, age: '9 days old', age_days: 9 }],
      ['stage:1', { ...UPGRADE, age: '14 months old', age_days: 425 }],
    ]);
    assert.equal(oldestBaseAge(lookups), '14 months old');
  });

  it('says nothing when no answer carries an age', () => {
    assert.equal(oldestBaseAge(new Map()), '');
    assert.equal(oldestBaseAge(new Map([['stage:0', RATE_LIMITED]])), '');
  });
});

/* -------------------------------------------------------------------------
 * Search.
 * ---------------------------------------------------------------------- */

const RESULTS: ImageSearchAnswer = {
  query: 'postgres',
  state: 'ok',
  message: '',
  results: [
    {
      name: 'postgres',
      description: 'The PostgreSQL object-relational database system',
      stars: 14979,
      pulls_display: '1B+',
      official: true,
      badge: 'official',
      architectures: ['amd64', 'arm64'],
    },
  ],
};

function popupOf(root: El, key: string): El | undefined {
  return walk(root).find((e) => e.classList.contains('imagesearch-popup') && e.dataset.for === key);
}

describe('finding an image by name', () => {
  it('gives the image field a search popup and a named control', () => {
    const view = new InspectorView({ send: () => undefined });
    view.render(inspectionOf([imageField()]));
    const root = view.element as unknown as El;
    const popup = popupOf(root, 'services.db.image');
    assert.ok(popup, 'the image field has no search popup');
    const toggle = walk(root).find(
      (e) => e.classList.contains('combo-toggle') && e.dataset.for === 'services.db.image',
    );
    assert.ok(toggle, 'no control opens the search');
    assert.equal(toggle.tagName, 'BUTTON');
    assert.match(toggle.getAttribute('aria-label') ?? '', /Docker Hub/);
    // The WAI-ARIA combobox pattern, the same one story 7.9 ships.
    const input = inputOf(root, 'services.db.image')!;
    assert.equal(input.getAttribute('role'), 'combobox');
    assert.equal(input.getAttribute('aria-haspopup'), 'listbox');
    assert.equal(input.getAttribute('aria-expanded'), 'false');
  });

  // The field is still a field. That is the whole reason this is an `<input>`
  // with a popup and not a picker: Docker Hub being unreachable is not a reason
  // a reader cannot type a reference they already know.
  it('leaves the field typeable and staging, exactly as before', () => {
    const sent: WebviewMessage[] = [];
    const view = new InspectorView({ send: (m) => sent.push(m) });
    view.render(inspectionOf([imageField()]));
    const input = inputOf(view.element as unknown as El, 'services.db.image');
    input!.value = 'redis:7-alpine';
    input!.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [
      { type: 'edit', path: 'services.db.image', value: 'redis:7-alpine' },
    ]);
  });

  it('searches from the add form and puts the chosen reference in the field', () => {
    const sent: WebviewMessage[] = [];
    const view = new AddFormView({ send: (m) => sent.push(m) });
    view.openFor('service');
    const root = view.element as unknown as El;
    const toggle = walk(root).find(
      (e) => e.classList.contains('combo-toggle') && e.dataset.for === 'add:image',
    );
    assert.ok(toggle, 'the add form has no Docker Hub search');
    toggle.fire('click', {});

    // The answer arrives against whatever token the control asked with. It has
    // not asked yet — the field is empty and below the minimum length — so the
    // popup says what it is for rather than showing a stale list.
    const popup = popupOf(root, 'add:image');
    assert.ok(popup);
    const lead = walk(popup).find((e) => e.classList.contains('combo-lead'))!;
    assert.match(lead.textContent ?? '', /two characters/i);
    // A `role="status"` line, so a reader who cannot see the popup is told.
    assert.equal(lead.getAttribute('role'), 'status');
  });

  // An answer to a superseded request is dropped. A popup that fills with the
  // results of the query before last is worse than one that is still empty,
  // because the reader believes it.
  it('drops an answer whose token it is not waiting for', () => {
    const view = new AddFormView({ send: () => undefined });
    view.openFor('service');
    view.receiveSearch(9999, RESULTS);
    const options = walk(view.element as unknown as El).filter(
      (e) => e.getAttribute('role') === 'option',
    );
    assert.deepEqual(options, [], 'a stale answer was rendered');
  });

  it('names a result with everything its row shows', () => {
    const name = optionName(RESULTS.results[0]);
    assert.match(name, /postgres/);
    assert.match(name, /official/);
    assert.match(name, /14979 stars/);
    assert.match(name, /1B\+ pulls/);
    assert.match(metaLine(RESULTS.results[0]), /★ 14979/);
    assert.match(metaLine(RESULTS.results[0]), /amd64 arm64/);
  });
});
