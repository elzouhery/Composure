// The Dockerfile as stages — story 6.2.
//
// This is the suite the review called the one that matters most: with the
// mutation `stage: stage.index` → `stage: 0`, editing stage 1's base image
// rewrites STAGE 0's. A confident wrong WRITE, in the grammar where a wrong
// write is hardest to notice, and invisible to 352 tests — every existing check
// over stageform.ts tested `stageHeading`, `stageDetail`, `stageSize` and
// `fileMarks`, all of which are pure functions of one stage that never see an
// index.
//
// So these drive the real StageFormView and assert on what it posts.

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { installDom, walk, type El } from './fakedom.test';

installDom();

// eslint-disable-next-line @typescript-eslint/no-var-requires
const { StageFormView } = require('./stageform') as typeof import('./stageform');

import type {
  DockerInstruction,
  DockerStage,
  DockerVocabulary,
  DockerfileForm,
  WebviewMessage,
} from '../shared/protocol';

/* -------------------------------------------------------------------------
 * Fixtures: a real two-stage build.
 * ---------------------------------------------------------------------- */

function instruction(over: Partial<DockerInstruction> = {}): DockerInstruction {
  return {
    kind: 'instruction',
    index: 0,
    name: 'RUN',
    args: 'go build ./...',
    flags: [],
    text: 'RUN go build ./...',
    start_line: 2,
    end_line: 2,
    editable: true,
    ...over,
  } as DockerInstruction;
}

function stage(index: number, over: Partial<DockerStage> = {}): DockerStage {
  return {
    index,
    name: index === 0 ? 'builder' : '',
    image_ref: index === 0 ? 'golang:1.23' : 'alpine:3.20',
    platform: '',
    from: { start_line: index === 0 ? 1 : 8, end_line: index === 0 ? 1 : 8 },
    instructions: [],
    ...over,
  } as DockerStage;
}

function form(over: Partial<DockerfileForm> = {}): DockerfileForm {
  return {
    path: '/w/svc/Dockerfile',
    escape_char: '\\',
    crlf: false,
    bom: false,
    missing: false,
    context: '',
    dockerfile: '',
    directives: [],
    preamble: [],
    stages: [
      stage(0, { instructions: [instruction({ index: 0 })] }),
      stage(1, { instructions: [instruction({ index: 1, name: 'CMD', args: './app', text: 'CMD ./app', start_line: 9 })] }),
      stage(2, { name: 'test', image_ref: 'builder', instructions: [] }),
    ],
    ...over,
  } as DockerfileForm;
}

let sent: WebviewMessage[] = [];
function render(f: DockerfileForm, staged = new Set<string>(), from: string | null = '/w/compose.yaml') {
  sent = [];
  const view = new StageFormView({ send: (m: WebviewMessage) => sent.push(m) });
  view.render(f, from, staged);
  return { view, root: view.element as unknown as El };
}

/** Every group the form drew, in order. */
const groups = (root: El): El[] => walk(root).filter((e) => e.classList.contains('grp'));
/** Every editable field, keyed by the `data-field` the row carries. */
function fields(root: El): Map<string, El> {
  const out = new Map<string, El>();
  for (const e of walk(root)) {
    if (e.dataset.field !== undefined) {
      out.set(e.dataset.field, e);
    }
  }
  return out;
}

beforeEach(() => {
  (globalThis as any).document.activeElement = null;
});

/* -------------------------------------------------------------------------
 * Every stage, drawn.
 * ---------------------------------------------------------------------- */

describe('a multi-stage build renders as its stages — story 6.2', () => {
  // MUTATION: the stage loop deleted, so the form renders ZERO stages. The
  // header still names the file, the marks still render, `Back to the stack`
  // still works — and the page is empty. Nothing measured how many groups the
  // form produced.
  it('draws one group per stage, headed by the name the file gives it', () => {
    const { root } = render(form());
    const headings = walk(root)
      .filter((e) => e.classList.contains('grp-name'))
      .map((e) => e.textContent);
    assert.deepEqual(
      headings,
      ['builder', 'alpine:3.20', 'test'],
      `the form drew ${headings.length} stage groups`,
    );
    assert.equal(groups(root).length, 3);
  });

  it('says what each stage is and how big it is', () => {
    const { root } = render(form());
    const details = walk(root)
      .filter((e) => e.classList.contains('stage-detail'))
      .map((e) => e.textContent);
    assert.equal(details.length, 3);
    assert.match(details[0], /golang:1\.23 · as builder/);
    assert.match(details[0], /1 instruction · 1 add a layer/);
  });

  it('says so when a file declares no stages at all, rather than drawing nothing', () => {
    const { root } = render(form({ stages: [] }));
    assert.equal(groups(root).length, 0);
    const message = walk(root).find((e) => e.classList.contains('inspector-message'));
    assert.equal(message?.textContent, 'This file declares no build stages.');
  });

  it('names the marks that are invisible in the text', () => {
    const { root } = render(form({ crlf: true, bom: true, escape_char: '`' }));
    const marks = walk(root).find((e) => e.classList.contains('inspector-source'))!;
    assert.match(marks.textContent, /escape character `/);
    assert.match(marks.textContent, /CRLF line endings/);
    assert.match(marks.textContent, /byte order mark/);
  });
});

/* -------------------------------------------------------------------------
 * THE ONE THAT MATTERS: which stage an edit lands in.
 * ---------------------------------------------------------------------- */

describe('an edit names the stage it was typed into — story 6.2', () => {
  // MUTATION: `stage: stage.index` → `stage: 0`. Editing stage 1's base image
  // rewrites stage 0's. Every stage still renders, every field still commits,
  // the pending diff still appears, and the file the reader saves is wrong in a
  // way they will find out about at build time.
  it('sends the index of the stage the field belongs to, not the first one', () => {
    const { root } = render(form());
    const byKey = fields(root);
    for (const index of [0, 1, 2]) {
      const input = byKey.get(`stage:${index}`);
      assert.ok(input, `stage ${index} has no editable FROM field`);
      sent = [];
      input!.value = `changed-${index}`;
      input!.fire('keydown', { key: 'Enter' });
      assert.deepEqual(
        sent,
        [{ type: 'editStage', stage: index, value: `changed-${index}` }],
        `editing stage ${index} was posted against another stage`,
      );
    }
  });

  it('sends the index of the instruction, not of the row', () => {
    // The same mutation shape one level down: `instruction: in_.index` → 0
    // rewrites the first RUN in the file whichever one was edited.
    const { root } = render(form());
    const byKey = fields(root);
    for (const index of [0, 1]) {
      const input = byKey.get(`instruction:${index}`);
      assert.ok(input, `instruction ${index} is not editable`);
      sent = [];
      input!.value = `args-${index}`;
      input!.fire('keydown', { key: 'Enter' });
      assert.deepEqual(sent, [{ type: 'editInstruction', instruction: index, value: `args-${index}` }]);
    }
  });

  it('keeps the fields of two stages distinct, so one cannot stand in for the other', () => {
    const { root } = render(form());
    const byKey = fields(root);
    assert.notEqual(byKey.get('stage:0'), byKey.get('stage:1'));
    assert.equal(byKey.get('stage:0')!.value, 'golang:1.23');
    assert.equal(byKey.get('stage:1')!.value, 'alpine:3.20');
  });

  it('posts nothing at all when the value was not changed', () => {
    const { root } = render(form());
    const input = fields(root).get('stage:0')!;
    sent = [];
    input.fire('keydown', { key: 'Enter' });
    input.fire('blur');
    assert.deepEqual(sent, [], 'an untouched field staged an edit');
  });

  it('reverts on Escape without posting', () => {
    const { root } = render(form());
    const input = fields(root).get('stage:1')!;
    input.value = 'typed';
    sent = [];
    input.fire('keydown', { key: 'Escape' });
    assert.equal(input.value, 'alpine:3.20', 'Escape did not put the file’s value back');
    assert.deepEqual(sent, []);
  });
});

/* -------------------------------------------------------------------------
 * What the engine will not rewrite is said here, not at Save.
 * ---------------------------------------------------------------------- */

describe('a row the engine refuses is refused on the page — R7.4', () => {
  it('renders an uneditable instruction read-only, with the reason', () => {
    const { root } = render(
      form({
        stages: [
          stage(0, {
            instructions: [
              instruction({
                index: 0,
                editable: false,
                not_editable: 'this instruction spans a line continuation',
                start_line: 3,
                end_line: 5,
              }),
            ],
          }),
        ],
      }),
    );
    assert.equal(fields(root).has('instruction:0'), false, 'a field is offered that Save would refuse');
    const block = walk(root).find((e) => e.classList.contains('is-readonly'))!;
    assert.match(block.textContent, /this instruction spans a line continuation/);
    assert.match(block.textContent, /lines 3–5/, 'a multi-line instruction reports one line');
  });

  it('marks a staged field as staged and not yet written', () => {
    const { root } = render(form(), new Set(['stage:1']));
    const block = walk(root).find((e) => e.dataset.path === 'stage:1')!;
    assert.equal(block.classList.contains('is-staged'), true);
    assert.match(block.textContent, /staged, not yet written/);
    const other = walk(root).find((e) => e.dataset.path === 'stage:0')!;
    assert.equal(other.classList.contains('is-staged'), false, 'every stage reads as staged');
  });

  it('gives every editable field an accessible name carrying its line', () => {
    const { root } = render(form());
    for (const [key, input] of fields(root)) {
      const label = input.getAttribute('aria-label') ?? '';
      assert.ok(label.length > 0, `${key} has no accessible name`);
      assert.match(label, /at line \d+/, `${key} does not say where it is in the file`);
    }
  });
});

/* -------------------------------------------------------------------------
 * Story 7.8: `Available here` — the differentiator in the other grammar.
 * ---------------------------------------------------------------------- */

/** The core's answer, in the shape internal/dockerfile/vocabulary.go sends it. */
function vocabulary(
  declared: string[],
  available: string[],
  over: Partial<DockerVocabulary> = {},
): DockerVocabulary {
  return {
    scope: 'stage',
    declared_count: declared.length,
    available_count: available.length,
    instructions: [
      ...declared.map((name, i) => ({
        name,
        summary: `what ${name} does`,
        declared: true,
        uses: i + 1,
        indices: [i],
      })),
      ...available.map((name) => ({
        name,
        summary: `what ${name} does`,
        declared: false,
        uses: 0,
      })),
    ],
    ...over,
  };
}

/** Every instruction offered under a stage group, in order. */
function offered(root: El): string[] {
  return walk(root)
    .filter((e) => e.classList.contains('addable-key') && e.dataset.add !== undefined)
    .map((e) => e.textContent.trim().split(' ')[0]);
}

// The mockup's own list, and the reason this story exists at all.
const RUNTIME_AVAILABLE = [
  'ENTRYPOINT',
  'CMD',
  'USER',
  'ARG',
  'LABEL',
  'STOPSIGNAL',
  'SHELL',
  'VOLUME',
  'ONBUILD',
];

describe('a stage ends with everything it could declare and does not — story 7.8', () => {
  // MUTATION: `.slice(0, 5)` anywhere in the available list. The panel still
  // draws a plausible row of instructions and the reader never learns that
  // ONBUILD exists. This is why the assertion is the WHOLE list rather than a
  // count or a spot check — the compose side's own rule (story 5.2).
  it('offers every available instruction, never a truncated head of the list', () => {
    const f = form({
      stages: [
        stage(0, {
          instructions: [instruction({ index: 0 })],
          vocabulary: vocabulary(['FROM', 'RUN', 'COPY', 'WORKDIR', 'EXPOSE', 'ENV', 'ADD', 'HEALTHCHECK'], RUNTIME_AVAILABLE),
        }),
      ],
    });
    const { root } = render(f);
    assert.deepEqual(
      offered(root),
      RUNTIME_AVAILABLE,
      'the available list is not what the core sent — something filtered, sorted or truncated it',
    );
    const block = walk(root).find((e) => e.classList.contains('addable'))!;
    assert.doesNotMatch(block.textContent, /show more|more…|\.\.\./i, 'the list is collapsed behind something');
  });

  it('takes the list from the core and holds no instruction names of its own', () => {
    const { root } = render(
      form({
        stages: [
          stage(0, { vocabulary: vocabulary(['FROM'], ['INVENTED_BY_THE_CORE']) }),
        ],
      }),
    );
    assert.deepEqual(offered(root), ['INVENTED_BY_THE_CORE']);
  });

  it('says what an instruction does, so a bare name is never the whole offer', () => {
    const { root } = render(
      form({ stages: [stage(0, { vocabulary: vocabulary(['FROM'], ['HEALTHCHECK']) })] }),
    );
    const key = walk(root).find((e) => e.dataset.add === '0:HEALTHCHECK')!;
    assert.match(key.getAttribute('title') ?? '', /what HEALTHCHECK does/);
    assert.match(key.getAttribute('aria-label') ?? '', /HEALTHCHECK/);
  });

  it('strikes a deprecated instruction through with its reason rather than dropping it', () => {
    const vocab = vocabulary(['FROM'], ['MAINTAINER']);
    vocab.instructions[1].deprecated = true;
    vocab.instructions[1].deprecated_note = 'superseded by LABEL org.opencontainers.image.authors';
    const { root } = render(form({ stages: [stage(0, { vocabulary: vocab })] }));
    const key = walk(root).find((e) => e.dataset.add === '0:MAINTAINER');
    assert.ok(key, 'a deprecated instruction was dropped from the list');
    assert.equal(key!.classList.contains('is-deprecated'), true);
    assert.match(key!.textContent, /superseded by LABEL/);
  });

  it('shows an instruction the core does not recognise rather than pretending the file has only what we understand', () => {
    const { root } = render(
      form({
        stages: [
          stage(0, { vocabulary: vocabulary(['FROM'], ['CMD'], { unknown: ['RUNX'] }) }),
        ],
      }),
    );
    const block = walk(root).find((e) => e.classList.contains('addable-unknown'));
    assert.ok(block, 'an unrecognised instruction vanished from the panel');
    assert.match(block!.textContent, /RUNX/);
  });

  it('counts what the stage already uses, and every stage counts its own', () => {
    const { root } = render(
      form({
        stages: [
          stage(0, { vocabulary: vocabulary(['RUN'], ['CMD']) }),
          stage(1, { vocabulary: vocabulary(['COPY'], ['USER']) }),
        ],
      }),
    );
    const declared = walk(root)
      .filter((e) => e.dataset.uses !== undefined)
      .map((e) => e.textContent.trim());
    assert.deepEqual(declared, ['RUN × 1', 'COPY × 1'], 'the declared half is missing, or a stage borrowed another stage’s');
  });

  it('opens an input in place and stages NOTHING until Enter — DECISIONS.md 17', () => {
    const { view, root } = render(
      form({ stages: [stage(0, { vocabulary: vocabulary(['FROM'], ['HEALTHCHECK']) })] }),
    );
    const key = walk(root).find((e) => e.dataset.add === '0:HEALTHCHECK')!;
    sent = [];
    key.fire('click');
    assert.deepEqual(sent, [], 'clicking an available instruction staged something');

    const input = fields(view.element as unknown as El).get('add:0:HEALTHCHECK');
    assert.ok(input, 'clicking the instruction opened no field to type in');
    input!.value = 'CMD curl -f http://localhost/';
    input!.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [
      { type: 'addInstruction', stage: 0, text: 'HEALTHCHECK CMD curl -f http://localhost/' },
    ]);
  });

  it('stages the instruction into the stage it was opened in, not the first one', () => {
    const { view, root } = render(
      form({
        stages: [
          stage(0, { vocabulary: vocabulary(['FROM'], ['USER']) }),
          stage(1, { vocabulary: vocabulary(['FROM'], ['USER']) }),
        ],
      }),
    );
    walk(root).find((e) => e.dataset.add === '1:USER')!.fire('click');
    const input = fields(view.element as unknown as El).get('add:1:USER')!;
    sent = [];
    input.value = 'app';
    input.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'addInstruction', stage: 1, text: 'USER app' }]);
  });

  it('writes nothing for an instruction with no value typed', () => {
    const { view, root } = render(
      form({ stages: [stage(0, { vocabulary: vocabulary(['FROM'], ['HEALTHCHECK']) })] }),
    );
    walk(root).find((e) => e.dataset.add === '0:HEALTHCHECK')!.fire('click');
    const input = fields(view.element as unknown as El).get('add:0:HEALTHCHECK')!;
    sent = [];
    input.value = '   ';
    input.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [], 'a bare instruction with no arguments was staged');
  });

  it('marks an instruction already staged, and does not offer it as though nothing had happened', () => {
    const { root } = render(
      form({ stages: [stage(0, { vocabulary: vocabulary(['FROM'], ['USER']) })] }),
      new Set(['add-instruction:0:USER']),
    );
    const key = walk(root).find((e) => e.dataset.add === '0:USER')!;
    assert.equal(key.classList.contains('is-staged'), true);
    assert.match(key.textContent, /staged/);
  });
});

/* -------------------------------------------------------------------------
 * Story 7.7: `+ add stage`.
 * ---------------------------------------------------------------------- */

describe('the + add stage control — story 7.7', () => {
  const addStage = (root: El): El =>
    walk(root).find((e) => e.dataset.control === 'add-stage')!;

  it('sits in the pane header, reads what the design says, and reports its state', () => {
    const { root } = render(form());
    const button = addStage(root);
    assert.ok(button, 'the header has no add-stage control at all');
    assert.equal(button.textContent.trim(), '+ add stage');
    assert.equal(button.getAttribute('aria-pressed'), 'false');
    assert.ok((button.getAttribute('aria-label') ?? button.textContent).length > 0);
  });

  it('opens a composer and nothing else — pressing it writes nothing', () => {
    const { view, root } = render(form());
    sent = [];
    addStage(root).fire('click');
    assert.deepEqual(sent, [], 'pressing + add stage staged something before anything was typed');
    const el = view.element as unknown as El;
    assert.equal(addStage(el).getAttribute('aria-pressed'), 'true');
    assert.ok(fields(el).get('add-stage:image'), 'the composer offers nowhere to type an image');
    assert.ok(fields(el).get('add-stage:name'), 'the composer offers no place for the AS name');
  });

  it('stages the image and the name the reader typed', () => {
    const { view, root } = render(form());
    addStage(root).fire('click');
    const el = view.element as unknown as El;
    fields(el).get('add-stage:name')!.value = 'serve';
    const image = fields(el).get('add-stage:image')!;
    image.value = 'nginx:1.27';
    sent = [];
    image.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'addStage', image: 'nginx:1.27', name: 'serve' }]);
  });

  it('sends no AS name when the reader gave none, rather than inventing one', () => {
    const { view, root } = render(form());
    addStage(root).fire('click');
    const el = view.element as unknown as El;
    const image = fields(el).get('add-stage:image')!;
    image.value = 'nginx:1.27';
    sent = [];
    image.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'addStage', image: 'nginx:1.27', name: '' }]);
  });

  it('sends nothing when no image was typed', () => {
    const { view, root } = render(form());
    addStage(root).fire('click');
    const el = view.element as unknown as El;
    fields(el).get('add-stage:name')!.value = 'serve';
    sent = [];
    fields(el).get('add-stage:image')!.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [], 'a stage with no image was staged');
  });

  // The check above cannot fail: `valueInput` returns early when the text is
  // unchanged, so an untouched image field never reaches the composer's own
  // guard at all. The NAME field's Enter calls submit() directly with whatever
  // the image field holds, so this is the one gesture that can carry an empty
  // image into it — and it is the gesture a reader makes: type the name, press
  // Enter, having forgotten the image. Deleting the guard leaves the check
  // above green and sends `FROM ` with no reference.
  it('writes nothing when Enter comes from the name field and no image was typed', () => {
    const { view, root } = render(form());
    addStage(root).fire('click');
    const el = view.element as unknown as El;
    const name = fields(el).get('add-stage:name')!;
    name.value = 'serve';
    sent = [];
    name.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [], 'a stage with no image was staged from the name field');
  });

  // "Enter in either stages" is what the composer's own docstring promises, and
  // the name field's `commit` is deliberately inert, so nothing else in this
  // file proves the promise. Enter in the name field submits the image the
  // reader typed, exactly as Enter in the image field does.
  it('stages from the name field too — Enter in either means the reader has finished', () => {
    const { view, root } = render(form());
    addStage(root).fire('click');
    const el = view.element as unknown as El;
    fields(el).get('add-stage:image')!.value = 'nginx:1.27';
    const name = fields(el).get('add-stage:name')!;
    name.value = 'serve';
    sent = [];
    name.fire('keydown', { key: 'Enter' });
    assert.deepEqual(sent, [{ type: 'addStage', image: 'nginx:1.27', name: 'serve' }]);
  });

  // And only Enter. Tabbing out of the name field blurs it, and a blur that
  // staged a stage would write one every time the reader moved between the two
  // fields — which is why the name field's own `commit` does nothing.
  it('stages nothing when the name field is merely left', () => {
    const { view, root } = render(form());
    addStage(root).fire('click');
    const el = view.element as unknown as El;
    fields(el).get('add-stage:image')!.value = 'nginx:1.27';
    const name = fields(el).get('add-stage:name')!;
    name.value = 'serve';
    sent = [];
    name.fire('blur');
    assert.deepEqual(sent, [], 'leaving the name field staged a stage');
  });

  // The other half of the composer's docstring, and the half that was false:
  // Escape restored an already-empty field and blurred it, so the composer
  // stayed open and `+ add stage` stayed pressed. A control the keyboard can
  // open and cannot close is not a keyboard-reachable control.
  for (const field of ['add-stage:image', 'add-stage:name']) {
    it(`closes the composer on Escape in ${field}, writing nothing`, () => {
      const { view, root } = render(form());
      addStage(root).fire('click');
      const el = view.element as unknown as El;
      const input = fields(el).get(field)!;
      input.value = 'nginx:1.27';
      sent = [];
      input.fire('keydown', { key: 'Escape' });
      const after = view.element as unknown as El;
      assert.equal(fields(after).get('add-stage:image'), undefined, 'the composer stayed open');
      assert.equal(addStage(after).getAttribute('aria-pressed'), 'false');
      assert.deepEqual(sent, [], 'Escape wrote something');
    });
  }

  it('offers nothing to add to a file that is not there', () => {
    const { root } = render(form({ missing: true }));
    const button = addStage(root);
    assert.ok(button, 'the control vanished from the header rather than being disabled');
    assert.equal(button.hidden, true, 'a missing Dockerfile offers + add stage');
    // And it comes back for a file that exists, so `hidden` is a state and not
    // a control nobody can ever reach.
    assert.equal(addStage(render(form()).root).hidden, false);
  });
});

/* -------------------------------------------------------------------------
 * Gap 4b: the row that carries an instruction flag.
 *
 * `.field` is a TWO-column grid (`style.css .field`). `editableRow` appended
 * the flags span as a THIRD child, so the browser wrapped the input onto an
 * implicit second grid row and placed it back in column one — the key column.
 * Measured in the harness on `examples/webstack/docs/Dockerfile`'s
 * `COPY --from=build /app/dist /usr/share/nginx/html`: the value sat at
 * `x=10 w=367`, inside the key cell, while every other row in the file measured
 * `x=383 w=707`.
 *
 * `node --test` cannot lay out a grid, so this asserts the CAUSE as the set of
 * children a row draws rather than the pixel: a child the grid has no PLACED
 * track for auto-places into the first free cell, which is the key column, and
 * the row is broken whatever the columns are set to. The pixel is asserted by
 * `harness/probe.mjs`, which is the only thing in this repository that can see
 * it.
 *
 * The row has since become the design pass's FOUR tracks —
 * `key | value | mark | actions` — and `.field-actions` is placed into track 4
 * by a rule of its own (`style.css .field > .field-actions`), so story 9.4's
 * `${}` is a third child that cannot wrap. What this still refuses is a child
 * that is none of those: the flags span, which had no track and took the
 * value's.
 * ---------------------------------------------------------------------- */

describe('a flagged instruction — gap 4b', () => {
  const flagged = (): DockerfileForm =>
    form({
      stages: [
        stage(0, {
          instructions: [
            instruction({ index: 0, name: 'WORKDIR', args: '/app', flags: [], text: 'WORKDIR /app' }),
            instruction({
              index: 1,
              name: 'COPY',
              args: '/app/dist /usr/share/nginx/html',
              flags: ['--from=build'],
              text: 'COPY --from=build /app/dist /usr/share/nginx/html',
              start_line: 7,
            }),
          ],
        }),
      ],
    });

  it('gives every field row only children the grid has a placed track for', () => {
    const { root } = render(flagged());
    const rows = walk(root).filter((e) => e.classList.contains('field'));
    assert.ok(rows.length > 0, 'the form drew no rows at all');
    // Track 1 is the key, track 2 the value (a bare input, or the cell that
    // holds a flag and its input), track 4 the row actions. Anything else has
    // no `grid-column` of its own and auto-places into the key column.
    const placed = ['field-key', 'field-value', 'field-value-cell', 'field-actions'];
    const stray = rows
      .flatMap((r) => r.children.map((c: El) => c.className))
      .filter((c: string) => !placed.some((p) => c.split(' ').includes(p)));
    assert.deepEqual(
      stray,
      [],
      `${stray.length} row child(ren) — ${JSON.stringify(stray)} — have no placed track, ` +
        'so they auto-place into the first free cell, which is the key column',
    );
    const wide = rows.map((r) => r.children.length).filter((n) => n > 3);
    assert.deepEqual(wide, [], `${wide.length} row(s) draw more children than the grid has tracks`);
  });

  it('puts the flag and the value it qualifies in the same cell', () => {
    const { root } = render(flagged());
    const flags = walk(root).find((e) => e.classList.contains('field-flags'));
    assert.ok(flags, 'the flag is not rendered at all — the reader cannot see --from=build');
    assert.equal(flags!.textContent, '--from=build');
    const cell = walk(root).find((e) => e.classList.contains('field-value-cell'));
    assert.ok(cell, 'the flag is not inside a value cell, so it is its own grid child');
    const inside = cell!.children.map((c: El) => c.className);
    assert.equal(inside.length, 2, `the value cell holds ${JSON.stringify(inside)}`);
    assert.ok(inside[0].includes('field-flags'));
    assert.ok(inside[1].includes('field-input'), 'the input is not in the cell with its flag');
    // The flag qualifies the row the reader edits, and the row the reader edits
    // is still the COPY at index 1 — not the unflagged WORKDIR above it.
    const input = cell!.children[1] as El;
    assert.equal(input.dataset.field, 'instruction:1');
    assert.equal(input.value, '/app/dist /usr/share/nginx/html');
  });

  it('leaves an unflagged row with no value cell wrapper', () => {
    // The wrapper exists for the flagged case only: an extra span on every row
    // in the file is a second thing that can go wrong for no gain.
    const { root } = render(form());
    assert.equal(
      walk(root).filter((e) => e.classList.contains('field-value-cell')).length,
      0,
    );
  });
});

/* -------------------------------------------------------------------------
 * Story 9.4: moving a literal into a build argument.
 *
 * The fixture is TWO stages, and that is the whole point of it. A single-stage
 * Dockerfile cannot tell a global `ARG` from a stage-scoped one — the second
 * FROM's declaration must go above the FIRST FROM, and a value inside stage 1
 * must be declared inside stage 1 — so a fixture with one stage passes every
 * assertion below while the instruction index sent to the host is wrong.
 * ---------------------------------------------------------------------- */

describe('a literal moved into a build argument — story 9.4', () => {
  /** Two stages, four instructions, every index distinct and none of them 0. */
  const argForm = (): DockerfileForm =>
    form({
      stages: [
        stage(0, {
          from: { index: 1, start_line: 2, end_line: 2 } as DockerInstruction,
          instructions: [
            instruction({ index: 2, name: 'RUN', args: 'npm ci', text: 'RUN npm ci', start_line: 3 }),
          ],
        }),
        stage(1, {
          from: { index: 3, start_line: 4, end_line: 4 } as DockerInstruction,
          instructions: [
            instruction({
              index: 4,
              name: 'ENV',
              args: 'NODE_ENV=production',
              text: 'ENV NODE_ENV=production',
              start_line: 5,
            }),
          ],
        }),
      ],
    });

  const RESULT = {
    name: 'NODE_VERSION',
    value: '18',
    dockerfile: {
      file: '/w/svc/Dockerfile',
      ops: [],
      diff:
        '--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1,4 +1,5 @@\n+ARG NODE_VERSION=18\n' +
        '-FROM node:18\n+FROM node:${NODE_VERSION}\n',
      added: 2,
      removed: 1,
      changed_lines: 3,
      written: false,
    },
    scope: 'global',
    scope_reason:
      'a FROM can only use an ARG declared before the FIRST FROM, so the declaration went above ' +
      'line 2 and could not go anywhere else — inside a stage it would expand to the empty string ' +
      'with no error',
    arg_line: 'ARG NODE_VERSION=18',
    declared: true,
    redeclared: false,
    already_declared: false,
    compose_note:
      'Nothing feeds `NODE_VERSION` from compose. `docker compose` passes build arguments only ' +
      'through `build.args` — a `.env` never reaches an ARG — so to set it from the stack, add ' +
      '`NODE_VERSION: ${NODE_VERSION}` under the service’s `build.args:` yourself.',
    written: false,
  } as any;

  /** Every `${}` control on the page, keyed by the instruction it acts on. */
  function moves(root: El): Map<string, El> {
    const out = new Map<string, El>();
    for (const e of walk(root)) {
      if (e.classList.contains('extract-open') && e.dataset.instruction !== undefined) {
        out.set(e.dataset.instruction, e);
      }
    }
    return out;
  }

  const blockText = (root: El): string =>
    walk(root)
      .filter((e) => e.classList.contains('extract-block'))
      .map((e) => e.textContent)
      .join(' ');

  // MUTATION: `instruction: index` → `instruction: 0` on the control the FROM
  // row builds. Every stage still offers the gesture, the block still opens,
  // the diff still renders — and the ARG the reader stages is computed for
  // another instruction entirely, in the grammar where a wrong write is
  // hardest to notice. This is the same shape as story 6.2's `stage: 0`.
  it('asks about the instruction the control sits on, in every stage', () => {
    const { root } = render(argForm());
    const byIndex = moves(root);
    assert.deepEqual(
      [...byIndex.keys()].sort(),
      ['1', '2', '3', '4'],
      `the gesture is offered on ${[...byIndex.keys()].join(', ')}`,
    );
    for (const index of ['1', '2', '3', '4']) {
      sent = [];
      byIndex.get(index)!.fire('click');
      assert.deepEqual(
        sent,
        [{ type: 'openExtractArg', instruction: Number(index) }],
        `pressing the control on instruction ${index} asked about another one`,
      );
    }
  });

  // The second stage's FROM, on its own, because it is the case a one-stage
  // fixture cannot express: its ARG must be declared above the FIRST FROM.
  it('asks about the SECOND stage’s FROM, not the first one', () => {
    const { root } = render(argForm());
    sent = [];
    moves(root).get('3')!.fire('click');
    assert.deepEqual(sent, [{ type: 'openExtractArg', instruction: 3 }]);
  });

  it('waits for the host rather than inventing what the move would do', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    assert.match(blockText(root), /Working out what this would change/);
    assert.equal(
      walk(root).filter((e) => e.classList.contains('extract-diff')).length,
      0,
      'a diff was drawn before the core answered',
    );
    void view;
  });

  // MUTATION: the scope note dropped from the block. The reader sees a diff
  // with an `ARG` line in it and no statement of which scope it landed in or
  // why it could not be elsewhere — which is the correctness condition of this
  // whole operation, rendered as decoration the pane may omit.
  it('says which scope the declaration landed in, in the core’s own sentence', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    const text = blockText(root);
    assert.match(text, /global/, 'the block does not say which scope the ARG went into');
    assert.ok(
      text.includes(RESULT.scope_reason),
      `the core’s reason was reworded or dropped:\n${text}`,
    );
  });

  // MUTATION: `compose_note` dropped. `build.args` is deliberately not wired
  // (DECISIONS.md 27), so this sentence is the only thing standing between the
  // reader and an ARG that nothing supplies.
  it('carries the compose note, verbatim, so the argument is not left unfed', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    assert.ok(
      blockText(root).includes(RESULT.compose_note),
      'the reader is not told that nothing feeds this ARG from compose',
    );
  });

  it('shows the ONE diff this operation writes, and names the file', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    const diffs = walk(root).filter((e) => e.classList.contains('extract-diff'));
    assert.equal(diffs.length, 1, `the one-file move drew ${diffs.length} diffs`);
    assert.match(diffs[0].textContent, /ARG NODE_VERSION=18/);
    assert.match(diffs[0].textContent, /FROM node:\$\{NODE_VERSION\}/);
    const label = walk(root).find((e) => e.classList.contains('extract-diff-label'))!;
    assert.match(label.textContent, /Dockerfile/);
  });

  // MUTATION: the `redeclared` and `already_declared` arms collapsed into the
  // ordinary one. A bare `ARG NAME` then reads as a new default, and an
  // idempotent move reads as a second declaration — the two states the reader
  // has to be able to tell apart before pressing anything.
  it('tells a bare re-declaration apart from a new default', () => {
    const { view, root } = render(argForm());
    moves(root).get('4')!.fire('click');
    view.setExtractArg({
      instruction: 4,
      staged: false,
      result: { ...RESULT, scope: 'stage 1', declared: false, redeclared: true, arg_line: 'ARG NODE_VERSION' },
    });
    const text = blockText(root);
    assert.match(text, /stage 1/, 'the stage-scoped declaration does not say which stage');
    assert.match(text, /re-declar|bare/i, 'a bare re-declaration reads as a new default');
  });

  it('says a declaration that was already there was not written again', () => {
    const { view, root } = render(argForm());
    moves(root).get('4')!.fire('click');
    view.setExtractArg({
      instruction: 4,
      staged: false,
      result: { ...RESULT, declared: false, already_declared: true, arg_line: undefined },
    });
    assert.match(blockText(root), /already declared/i);
  });

  // A refusal is an answer, not a fault and not a form: `arg-value`,
  // `arg-conflict` and `no-tag` all land here.
  it('renders a refusal where the diff would have been, with nothing to press', () => {
    const { view, root } = render(argForm());
    moves(root).get('1')!.fire('click');
    view.setExtractArg({
      instruction: 1,
      staged: false,
      refused: 'That FROM has no tag to move — it is pinned by digest.',
    });
    assert.match(blockText(root), /pinned by digest/);
    assert.equal(
      walk(root).filter((e) => e.classList.contains('extract-diff')).length,
      0,
      'a refused move still offered a diff',
    );
    assert.equal(
      walk(root).filter((e) => e.classList.contains('extract-stage')).length,
      0,
      'a refused move still offered a control that stages it',
    );
  });

  // MUTATION: the stage control posts `save`, or writes through any other
  // message. Nothing in this pane writes: the control STAGES and `Save to
  // <file>` is still the only thing in the product that reaches a file.
  it('stages the move with the name in the field, and writes nothing', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    const name = fields(root).get('extract-arg:3');
    assert.ok(name, 'the name the ARG will take is not in a field the reader can change');
    assert.equal(name!.value, 'NODE_VERSION');
    sent = [];
    walk(root).find((e) => e.classList.contains('extract-stage'))!.fire('click');
    assert.deepEqual(sent, [{ type: 'stageExtractArg', instruction: 3, name: 'NODE_VERSION' }]);
  });

  it('re-asks with the name the reader typed rather than staging it unseen', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    const name = fields(root).get('extract-arg:3')!;
    name.value = 'NODE_TAG';
    sent = [];
    name.fire('keydown', { key: 'Enter' });
    assert.deepEqual(
      sent,
      [{ type: 'openExtractArg', instruction: 3, name: 'NODE_TAG' }],
      'Enter in the name field staged a pair of bytes the reader has never seen',
    );
  });

  it('says when the move is staged, and closes on a second press', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: true, result: RESULT });
    const control = walk(root).find((e) => e.classList.contains('extract-stage'))!;
    assert.match(control.textContent, /staged/i);
    sent = [];
    moves(root).get('3')!.fire('click');
    assert.deepEqual(sent, [{ type: 'closeExtractArg', instruction: 3 }]);
    assert.equal(walk(root).filter((e) => e.classList.contains('extract-block')).length, 0);
  });

  it('gives every control it draws an accessible name — story 4.5’s floor', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    const unnamed = walk(root)
      .filter((e) => e.tagName === 'BUTTON' || e.tagName === 'INPUT')
      .filter((e) => (e.getAttribute('aria-label') ?? '') === '' && (e.textContent ?? '') === '');
    assert.deepEqual(unnamed.map((e) => e.className), [], 'a control was drawn with no name');
  });

  /**
   * The same file, re-sent, with the ARG the reader just staged now IN it.
   *
   * Indices are positions in the parsed instruction list, so inserting one line
   * at the top shifts every index at or after it by one: what was the second
   * stage's FROM at 3 is the first stage's `RUN npm ci` at 3 afterwards. This
   * is the form the host sends through `refill()` after a save and after any
   * external change to the file, and `refill()` goes to the SAME path — so the
   * path comparison that guarded this could never fire on it.
   */
  const argFormWithArgInserted = (): DockerfileForm =>
    form({
      preamble: [
        instruction({
          index: 1,
          kind: 'instruction',
          name: 'ARG',
          args: 'NODE_VERSION=18',
          text: 'ARG NODE_VERSION=18',
          start_line: 1,
        }),
      ],
      stages: [
        stage(0, {
          from: { index: 2, start_line: 3, end_line: 3 } as DockerInstruction,
          instructions: [
            instruction({ index: 3, name: 'RUN', args: 'npm ci', text: 'RUN npm ci', start_line: 4 }),
          ],
        }),
        stage(1, {
          from: { index: 4, start_line: 5, end_line: 5 } as DockerInstruction,
          instructions: [
            instruction({
              index: 5,
              name: 'ENV',
              args: 'NODE_ENV=production',
              text: 'ENV NODE_ENV=production',
              start_line: 6,
            }),
          ],
        }),
      ],
    });

  // D2. The block survived a same-path re-render, so it kept showing the diff
  // and the scope sentence computed BEFORE the change, and its button still
  // posted `{ type: 'stageExtractArg', instruction: 3 }` — an index that now
  // addresses `RUN npm ci` instead of the FROM the answer was about. Staging
  // from it would write an ARG for a value nobody asked about, which is
  // DECISIONS 24's rebase rule broken in the grammar it matters most in.
  it('forgets an open move when the same file comes back with the indices shifted', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    assert.equal(walk(root).filter((e) => e.classList.contains('extract-block')).length, 1);

    view.render(argFormWithArgInserted(), null, new Set());

    assert.equal(
      walk(root).filter((e) => e.classList.contains('extract-block')).length,
      0,
      'the move stayed open against an index that now addresses a different instruction',
    );
  });

  // The other half of the same rule: a re-render that did NOT move anything
  // must not close the block under the reader. `setLookup` and every unrelated
  // refill come through here, and a block that vanished whenever the host spoke
  // would be unusable for the round trip it exists to make.
  it('keeps an open move when the same file comes back unchanged', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    assert.equal(walk(root).filter((e) => e.classList.contains('extract-block')).length, 1);

    view.render(argForm(), null, new Set());

    assert.equal(
      walk(root).filter((e) => e.classList.contains('extract-block')).length,
      1,
      'an unrelated refill closed the block the reader was reading',
    );
  });

  it('forgets an open move when the form points at another file', () => {
    const { view, root } = render(argForm());
    moves(root).get('3')!.fire('click');
    view.setExtractArg({ instruction: 3, staged: false, result: RESULT });
    assert.equal(walk(root).filter((e) => e.classList.contains('extract-block')).length, 1);
    view.render({ ...argForm(), path: '/w/other/Dockerfile' }, null, new Set());
    assert.equal(
      walk(root).filter((e) => e.classList.contains('extract-block')).length,
      0,
      'a move stayed open over a different file, showing another file’s diff',
    );
  });
});
