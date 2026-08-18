// The write path against the REAL core binary — Epic 6's contract test.
//
// Everything else in this suite speaks to a stub, and a stub cannot catch the
// two ways the halves of this product drift apart across the write path:
//
//   1. The operation names, the `expect` shape and the result's field names are
//      written by struct tag in internal/edit and read by name in
//      shared/protocol.ts. Nothing in either language's type system spans that
//      gap; a renamed tag compiles clean on both sides and produces a Save
//      button that silently does nothing.
//
//   2. The refusal codes are two constants — `codeEditRefused` and
//      `codeStaleRange` in cmd/composure/serve.go, `CODE_EDIT_REFUSED` and
//      `CODE_STALE_RANGE` in host/edit.ts. Get them out of step and a stale
//      range is shown as a fault, which means the reader retries it, which
//      means the stage is never discarded.
//
// So: spawn the real binary and drive the whole write path through it, on a
// scratch copy of a real fixture. It writes to a temp directory and nowhere
// else.
//
// The binary is built by `make extension-core` into extension/bin/ and is
// gitignored, so a checkout without it skips rather than fails.

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import * as path from 'node:path';
import { describe, it } from 'node:test';
import { ComposureCore, goTarget } from './core';
import {
  apply,
  classify,
  commentText,
  expectOf,
  extractApply,
  extractPreview,
  planAdd,
  preview,
  reasonOf,
  refusalDetail,
} from './edit';
import type { DockerfileForm } from '../shared/protocol';

const EXTENSION_ROOT = path.join(__dirname, '..');
const CORE = path.join(
  EXTENSION_ROOT,
  'bin',
  goTarget(),
  process.platform === 'win32' ? 'composure.exe' : 'composure',
);

/** Every file under a directory, by relative path, with its content digest. */
function hashTree(root: string): Map<string, string> {
  const out = new Map<string, string>();
  const walk = (dir: string, prefix: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      const rel = prefix === '' ? entry.name : `${prefix}/${entry.name}`;
      if (entry.isDirectory()) {
        walk(full, rel);
        continue;
      }
      out.set(rel, createHash('sha256').update(readFileSync(full)).digest('hex'));
    }
  };
  if (existsSync(root)) {
    walk(root, '');
  }
  return out;
}

const haveCore = existsSync(CORE);
const skip = haveCore
  ? false
  : `no core binary at ${CORE} — run \`make extension-core\` (it is gitignored, so a fresh checkout has none)`;

const COMPOSE = `# a stack
services:
  web:
    image: nginx:1.27   # pinned deliberately
    ports:
      - "8080:80"

  db:
    image: 'postgres:16'
`;

const DOCKERFILE =
  'FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build  # pinned\n' +
  'WORKDIR /src\n' +
  'RUN go build \\\n' +
  '      -o /out/app ./cmd/app\n' +
  '\n' +
  'from alpine:3.19 as runtime\n' +
  'COPY --from=build /out/app /app\n';

describe('the real core binary — the write path', { skip }, () => {
  const scratch = haveCore ? mkdtempSync(path.join(tmpdir(), 'composure-edit-')) : '';
  let n = 0;

  /** A fresh scratch file, so no test can see another's write. */
  function file(name: string, body: string): string {
    const p = path.join(scratch, `${n++}-${name}`);
    writeFileSync(p, body);
    return p;
  }

  async function connect(): Promise<ComposureCore> {
    const core = new ComposureCore({ binaryPath: CORE, log: () => {}, onExit: () => {} });
    await core.start(15000);
    return core;
  }

  /**
   * Every committed file this suite can see, hashed before a single write.
   *
   * The `after` hook below used to be EMPTY, with a comment asserting in prose
   * that "nothing outside it was ever written, which is the property that
   * matters". It is the property that matters, and nothing checked it: this is
   * the one suite in the repository that drives a real binary whose entire job
   * is writing bytes into files, and its safety claim was a sentence.
   */
  const guarded = haveCore ? hashTree(path.join(__dirname, 'testdata')) : new Map<string, string>();

  // The claim the strip makes to the reader, end to end through the real core.
  it('previews a scalar edit as a two-line diff and writes nothing', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const result = await preview(core, target, [
        { operation: 'replace_scalar', at: 'services.web.image', value: 'nginx:1.28' },
      ]);

      assert.equal(result.removed, 1, `removed ${result.removed} lines:\n${result.diff}`);
      assert.equal(result.added, 1, `added ${result.added} lines:\n${result.diff}`);
      assert.equal(result.written, false);
      assert.match(
        result.diff,
        new RegExp(`^--- a/${path.basename(target)}$`, 'm'),
        'the diff does not name the file it touches',
      );
      // The trailing comment travels with the line, which is the whole of R4.1.
      assert.match(result.diff, /\+ {4}image: nginx:1\.28 {3}# pinned deliberately/);
      assert.equal(readFileSync(target, 'utf8'), COMPOSE, 'preview changed the file');

      // The range and the previous text are what a stage is held against.
      assert.equal(result.ops[0].before, 'nginx:1.27');
      assert.ok(result.ops[0].range.end > result.ops[0].range.start);
    } finally {
      core.dispose();
    }
  });

  it('writes exactly what the preview showed, and touches nothing else', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const ops = [
        { operation: 'replace_scalar' as const, at: 'services.web.image', value: 'nginx:1.28' },
      ];
      const shown = await preview(core, target, ops);
      const written = await apply(core, target, ops);

      assert.equal(shown.diff, written.diff, 'the diff moved between preview and apply');
      assert.equal(written.written, true);

      const after = readFileSync(target, 'utf8');
      const before = COMPOSE.split('\n');
      const now = after.split('\n');
      assert.equal(now.length, before.length, 'the line count moved');
      const changed = now.filter((line, i) => line !== before[i]);
      assert.equal(changed.length, 1, `${changed.length} lines differ: ${changed.join(' | ')}`);
      assert.equal(changed[0], '    image: nginx:1.28   # pinned deliberately');
    } finally {
      core.dispose();
    }
  });

  // Story 5.2's last acceptance criterion, completed by 6.1: clicking an unset
  // key inserts it with its schema default, staged.
  it('stages an unset key as an insert and lands it at the file’s own indent', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const result = await apply(core, target, [
        { operation: 'insert_key', at: 'services.db', key: 'restart', value: 'always' },
      ]);
      assert.equal(result.removed, 0);
      assert.equal(result.added, 1);
      assert.match(readFileSync(target, 'utf8'), /^ {4}restart: always$/m);
    } finally {
      core.dispose();
    }
  });

  // AD-19 over the real wire, including the code the client branches on.
  it('refuses a stale range with the code that makes the client discard it', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const staged = await preview(core, target, [
        { operation: 'replace_scalar', at: 'services.web.image', value: 'nginx:1.28' },
      ]);
      const moved = `# someone else edited this\n${COMPOSE}`;
      writeFileSync(target, moved);

      await assert.rejects(
        apply(core, target, [
          {
            operation: 'replace_scalar',
            at: 'services.web.image',
            value: 'nginx:1.28',
            expect: {
              start: staged.ops[0].range.start,
              end: staged.ops[0].range.end,
              text: staged.ops[0].before,
            },
          },
        ]),
        (err: unknown) => {
          assert.equal(classify(err), 'stale', 'a stale range did not classify as stale');
          assert.equal(reasonOf(err), 'stale-range');
          return true;
        },
      );
      assert.equal(readFileSync(target, 'utf8'), moved, 'a refused apply wrote to the file');
    } finally {
      core.dispose();
    }
  });

  // AD-8. The field reverts, nothing is written, and the client can tell this
  // apart from a fault so it explains rather than alarms.
  it('refuses a block insert into a flow mapping', async () => {
    const core = await connect();
    try {
      const flow = 'services:\n  web: {image: nginx}\n';
      const target = file('compose.yaml', flow);
      await assert.rejects(
        apply(core, target, [
          { operation: 'insert_key', at: 'services.web', key: 'restart', value: 'always' },
        ]),
        (err: unknown) => {
          assert.equal(classify(err), 'refused');
          assert.equal(reasonOf(err), 'flow-style');
          return true;
        },
      );
      assert.equal(readFileSync(target, 'utf8'), flow);
    } finally {
      core.dispose();
    }
  });

  /* ---- story 6.2 ------------------------------------------------------- */

  it('returns a stage form this client can render', async () => {
    const core = await connect();
    try {
      const target = file('Dockerfile', DOCKERFILE);
      const form = await core.request<DockerfileForm>('stack/dockerfile', { path: target, at: '' });

      assert.equal(form.missing, false);
      assert.equal(form.stages.length, 2, 'the two stages did not arrive');
      assert.equal(form.stages[0].label, 'build');
      assert.equal(form.stages[1].image_ref, 'alpine:3.19');
      assert.equal(form.escape_char, '\\');

      // R7.4 said in the form: the multi-line RUN must arrive marked so the
      // stage form never offers a field it would then refuse.
      const run = form.stages[0].instructions.find((i) => i.name === 'RUN');
      assert.ok(run, 'the RUN instruction is missing from stage 0');
      assert.equal(run.editable, false);
      assert.ok((run.not_editable ?? '') !== '', 'nothing says why it cannot be edited');
    } finally {
      core.dispose();
    }
  });

  // R7.2, asserted on the bytes: --platform, the AS clause, the keyword casing
  // and the trailing comment all survive, and exactly one line changes.
  it('changes a base image in one line, keeping everything else on it', async () => {
    const core = await connect();
    try {
      const target = file('Dockerfile', DOCKERFILE);
      const result = await apply(core, target, [
        { operation: 'set_base_image', stage: 0, value: 'golang:1.24-alpine' },
      ]);
      assert.equal(result.removed, 1);
      assert.equal(result.added, 1);
      const after = readFileSync(target, 'utf8');
      assert.match(
        after,
        /^FROM --platform=\$BUILDPLATFORM golang:1\.24-alpine AS build {2}# pinned$/m,
      );
      // Lower-case `from` on the second stage is untouched: normalising casing
      // is a diff nobody asked for.
      assert.match(after, /^from alpine:3\.19 as runtime$/m);
    } finally {
      core.dispose();
    }
  });

  it('refuses to reflow a multi-line instruction', async () => {
    const core = await connect();
    try {
      const target = file('Dockerfile', DOCKERFILE);
      const form = await core.request<DockerfileForm>('stack/dockerfile', { path: target, at: '' });
      const run = form.stages[0].instructions.find((i) => i.name === 'RUN');
      assert.ok(run);
      await assert.rejects(
        apply(core, target, [
          { operation: 'replace_args', instruction: run.index, value: 'go build ./...' },
        ]),
        (err: unknown) => {
          assert.equal(classify(err), 'refused');
          assert.equal(reasonOf(err), 'multi-line');
          return true;
        },
      );
      assert.equal(readFileSync(target, 'utf8'), DOCKERFILE);
    } finally {
      core.dispose();
    }
  });

  /* ---- story 6.3 ------------------------------------------------------- */

  it('resolves a Dockerfile through a build section, relative to the context', async () => {
    const core = await connect();
    try {
      const dir = mkdtempSync(path.join(tmpdir(), 'composure-build-'));
      const compose = path.join(dir, 'compose.yaml');
      writeFileSync(
        compose,
        'services:\n  api:\n    build:\n      context: ./api\n      dockerfile: Dockerfile.dev\n',
      );
      const { mkdirSync } = await import('node:fs');
      mkdirSync(path.join(dir, 'api'));
      writeFileSync(path.join(dir, 'api', 'Dockerfile.dev'), DOCKERFILE);

      const form = await core.request<DockerfileForm>('stack/dockerfile', {
        path: compose,
        at: 'services.api.build',
      });
      assert.equal(form.path, path.join(dir, 'api', 'Dockerfile.dev'));
      assert.equal(form.missing, false);
      assert.equal(form.stages.length, 2);
    } finally {
      core.dispose();
    }
  });

  // A build naming a file that is not there is an ANSWER, not an error. The
  // node renders as missing; it does not vanish and it does not raise a banner.
  it('reports a Dockerfile that is not there as missing, not as a failure', async () => {
    const core = await connect();
    try {
      const dir = mkdtempSync(path.join(tmpdir(), 'composure-build-'));
      const compose = path.join(dir, 'compose.yaml');
      writeFileSync(
        compose,
        'services:\n  api:\n    build:\n      context: ./api\n      dockerfile: Dockerfile.dev\n',
      );
      const form = await core.request<DockerfileForm>('stack/dockerfile', {
        path: compose,
        at: 'services.api.build',
      });
      assert.equal(form.missing, true);
      assert.ok(form.path.endsWith(path.join('api', 'Dockerfile.dev')), form.path);
      assert.deepEqual(form.stages, []);
    } finally {
      core.dispose();
    }
  });

  // The finding the graph's "missing" marker is derived from. The rule id is a
  // third constant crossing the boundary — host/staging.ts branches on it.
  it('raises a finding for a build whose Dockerfile is not there', async () => {
    const core = await connect();
    try {
      const dir = mkdtempSync(path.join(tmpdir(), 'composure-build-'));
      const compose = path.join(dir, 'compose.yaml');
      writeFileSync(compose, 'services:\n  api:\n    build: ./api\n');
      const report = await core.request<{ findings: { rule: string; anchors: { path: string }[] }[] }>(
        'stack/diagnose',
        { path: compose, profiles: [] },
      );
      const missing = report.findings.filter((f) => f.rule === 'build-dockerfile-missing');
      assert.equal(missing.length, 1, 'the missing Dockerfile raised no finding');
      assert.equal(missing[0].anchors[0].path, 'services.api.build');
    } finally {
      core.dispose();
    }
  });
  // A TEST, not an `after` hook.
  //
  // The hook this replaced was empty, and the first attempt to fill it put the
  /* ---- stories 7.3 and 7.4: declaring something new -------------------- */

  // The planner over the real wire. `stack/add` is a third place the two
  // languages meet — the kind strings and the returned `ops` shape — and the
  // stub cannot see a rename on either side.
  it('plans a service as two operations and writes them as one edit', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const { ops } = await planAdd(core, target, target, 'service', 'cache', 'redis:7');
      assert.equal(ops.length, 2, `the core planned ${ops.length} operations`);
      // Planning writes nothing.
      assert.equal(readFileSync(target, 'utf8'), COMPOSE, 'planning changed the file');

      const written = await apply(core, target, ops);
      assert.equal(written.added, 2, `the write added ${written.added} lines`);
      assert.equal(written.removed, 0);
      // The bytes: one contiguous block, every other byte identical.
      assert.equal(
        readFileSync(target, 'utf8'),
        `${COMPOSE}  cache:\n    image: redis:7\n`,
        'the file is not the source with one block added',
      );
    } finally {
      core.dispose();
    }
  });

  // The bug the owner hit, against the real core, through the real staging
  // arithmetic — the test the one above should have been.
  //
  // `apply(core, target, ops)` up there hands the plan over with no `expect` on
  // any operation, which is the CLI's shape. Nothing the reader does reaches the
  // core that way: `panel.ts stageAll` previews first and records each
  // operation's landed range as its `expect`, and the core then re-locates every
  // one of them before it writes. That re-location used to happen against the
  // file on disk, where `services.PolicyServer` does not exist yet because
  // operation 0 is what creates it — so adding a service from the panel failed
  // with `edit: operation 1: path segment "PolicyServer" not found` the instant
  // the name was typed, and there was no way past it.
  //
  // This walks the same four calls in the same order and asserts the bytes.
  it('adds a service through the staged path the panel actually uses', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const { ops } = await planAdd(core, target, target, 'service', 'PolicyServer', 'nginx');
      assert.equal(ops.length, 2, `the core planned ${ops.length} operations`);

      // stageAll: preview the whole set, then hold each operation against the
      // range the preview reported for it.
      const staging = await preview(core, target, ops);
      assert.equal(staging.ops.length, ops.length, 'the preview did not report every operation');
      const staged = ops.map((op, i) => ({
        ...op,
        expect: {
          start: staging.ops[i].range.start,
          end: staging.ops[i].range.end,
          text: staging.ops[i].before,
        },
      }));

      // refreshPending: the staged set, previewed again. This is where the
      // reader saw the failure, before any Save button existed.
      const refreshed = await preview(core, target, staged);
      assert.equal(refreshed.added, 2, `the pending diff added ${refreshed.added} lines`);
      assert.equal(readFileSync(target, 'utf8'), COMPOSE, 'previewing changed the file');

      // Save.
      const written = await apply(core, target, staged);
      assert.equal(written.added, 2, `the write added ${written.added} lines`);
      assert.equal(written.removed, 0);
      assert.equal(
        readFileSync(target, 'utf8'),
        `${COMPOSE}  PolicyServer:\n    image: nginx\n`,
        'the file is not the source with one block added',
      );
    } finally {
      core.dispose();
    }
  });

  it('declares a network with no body, creating the block it goes in', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      const { ops } = await planAdd(core, target, target, 'network', 'frontend', '');
      await apply(core, target, ops);
      assert.equal(
        readFileSync(target, 'utf8'),
        `${COMPOSE}networks:\n  frontend:\n`,
        'a default was invented, or the block was not created',
      );
    } finally {
      core.dispose();
    }
  });

  // Rule 6 over the real wire, for the refusal this story introduced: a name
  // the file already declares comes back as a REFUSAL with its slug, not as a
  // fault, and the reader is given the line that already declares it.
  it('refuses a duplicate name as a refusal, naming the line that has it', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      await assert.rejects(
        () => planAdd(core, target, target, 'service', 'db', 'redis:7'),
        (err: unknown) => {
          assert.equal(classify(err), 'refused', 'a duplicate name was reported as a fault');
          assert.equal(reasonOf(err), 'duplicate-name');
          // `db:` is line 8 of the fixture, and naming it is what lets the
          // reader go and look rather than guess which file has the collision.
          assert.match(
            (err as Error).message,
            /compose\.yaml:8/,
            `the refusal does not name the file and line: ${(err as Error).message}`,
          );
          return true;
        },
      );
      assert.equal(readFileSync(target, 'utf8'), COMPOSE, 'a refusal wrote to the file');
    } finally {
      core.dispose();
    }
  });

  it('refuses a value it would have to quote, rather than quoting it', async () => {
    const core = await connect();
    try {
      const target = file('compose.yaml', COMPOSE);
      await assert.rejects(
        () => planAdd(core, target, target, 'service', 'cache', '3.10'),
        (err: unknown) => {
          assert.equal(classify(err), 'refused');
          assert.equal(reasonOf(err), 'needs-quoting');
          return true;
        },
      );
      // And the reader's own quoting is accepted, written byte for byte.
      const { ops } = await planAdd(core, target, target, 'service', 'cache', '"3.10"');
      await apply(core, target, ops);
      assert.match(readFileSync(target, 'utf8'), /image: "3\.10"/);
    } finally {
      core.dispose();
    }
  });

  /* -----------------------------------------------------------------------
   * Epic 9, story 9.2 — one entry of a list, through the real engine.
   *
   * The fixture REPEATS entries and the edits are in the MIDDLE, which is the
   * shape `testdata/edge/e43-repeated-list-entries.yml` exists to enforce: a
   * list of distinct values edited at index 0 cannot tell an off-by-one from a
   * correct answer, because every wrong entry looks wrong.
   * -------------------------------------------------------------------- */

  const LIST = [
    'services:',
    '  web:',
    '    image: nginx',
    '    command:',
    '      - sh',
    '      - -c',
    '      - sh',
    '      - echo hi',
    '      - sh',
    '    ports:',
    '      - "8080:80"',
    '      - "8443:443"',
    '',
  ].join('\n');

  it('replaces one entry of a repeated list and leaves every other byte alone', async () => {
    const core = await connect();
    try {
      const target = file('list.yaml', LIST);
      const ops = [
        { operation: 'replace_scalar' as const, at: 'services.web.command[2]', value: 'bash' },
      ];
      const shown = await preview(core, target, ops);
      assert.equal(shown.added, 1, shown.diff);
      assert.equal(shown.removed, 1, shown.diff);
      // The entry that was there, not the one before or after it — and `sh`
      // is the text of [0], [2] and [4], so this pins the INDEX by way of the
      // range rather than by way of the text.
      assert.equal(shown.ops[0].before, 'sh');

      await apply(core, target, ops);
      const after = readFileSync(target, 'utf8');
      // The whole file, byte for byte, and the substitution is anchored to the
      // entry AFTER the one being changed.
      //
      // An earlier draft of this assertion undid the edit by replacing
      // `- bash` with `- sh` anywhere in the file and comparing to the source.
      // It passed with the index changed from 2 to 4 — because undoing a
      // position-independent substitution is position-independent, so the check
      // could not fail. This one names the neighbour, which only index 2 has.
      assert.equal(
        after,
        LIST.replace('      - sh\n      - echo hi', '      - bash\n      - echo hi'),
        `an edit to entry 2 landed somewhere else:\n${after}`,
      );
      // …and the other two `sh` entries are still there. A splice that hit the
      // wrong one would leave a file that still parses and still looks right.
      assert.equal(after.split('\n').filter((l) => l === '      - sh').length, 2);
    } finally {
      core.dispose();
    }
  });

  it('refuses an index the list has not got, by name and with the count', async () => {
    const core = await connect();
    try {
      const target = file('list.yaml', LIST);
      await assert.rejects(
        preview(core, target, [
          { operation: 'replace_scalar', at: 'services.web.ports[9]', value: 'x' },
        ]),
        (err: unknown) => {
          // `refused`, not `failed`: the reader is told the entry is not there,
          // not that the tool broke. And the sentence says how many there are.
          assert.equal(classify(err), 'refused');
          assert.equal(reasonOf(err), 'entry-index');
          assert.match(String((err as Error).message), /2 entries/);
          return true;
        },
      );
      assert.equal(readFileSync(target, 'utf8'), LIST, 'a refused edit wrote bytes');
    } finally {
      core.dispose();
    }
  });

  it('appends an entry to the end of a block sequence', async () => {
    const core = await connect();
    try {
      const target = file('list.yaml', LIST);
      const result = await apply(core, target, [
        { operation: 'insert_sequence_entry', at: 'services.web.ports', value: '"9090:90"' },
      ]);
      assert.equal(result.removed, 0, result.diff);
      assert.equal(result.added, 1, result.diff);
      assert.equal(readFileSync(target, 'utf8'), `${LIST}      - "9090:90"\n`);
    } finally {
      core.dispose();
    }
  });

  it('removes the entry that was named and nothing beside it', async () => {
    const core = await connect();
    try {
      const target = file('list.yaml', LIST);
      await apply(core, target, [
        { operation: 'delete_key', at: 'services.web.command[2]' },
      ]);
      const after = readFileSync(target, 'utf8');
      assert.equal(after, LIST.replace('      - sh\n      - echo hi', '      - echo hi'));
      assert.equal(after.split('\n').filter((l) => l === '      - sh').length, 2);
    } finally {
      core.dispose();
    }
  });

  // A numeric MAPPING key renders as `[8080]` too. `isEntryPath` cannot tell
  // them apart and does not have to: both resolve to a scalar with bytes, and
  // the core disambiguates by the parent node's kind. This pins that the
  // ambiguity stays a display one.
  it('reaches a numeric mapping key at the same spelling a list index uses', async () => {
    const core = await connect();
    try {
      const target = file('numeric.yaml', [
        'services:',
        '  web:',
        '    image: nginx',
        '    environment: {8080: "a key, not an index"}',
        '',
      ].join('\n'));
      const result = await preview(core, target, [
        { operation: 'replace_scalar', at: 'services.web.environment[8080]', value: 'still a key' },
      ]);
      assert.equal(result.ops[0].before, '"a key, not an index"');
    } finally {
      core.dispose();
    }
  });

  /* -----------------------------------------------------------------------
   * Epic 9, story 9.1 — comments, through the real engine.
   *
   * SEVERAL comments at SEVERAL indents in every fixture, and one `#` inside a
   * quoted value. A fixture with one comment in it cannot tell a read of the
   * right run from a read of the only run, and a test whose value contains no
   * `#` cannot fail on the first-hash scan.
   * -------------------------------------------------------------------- */

  const COMMENTED = [
    '# the stack itself',
    'services:',
    '  # about web, line one',
    '  # about web, line two',
    '  web:',
    '    image: nginx:1.27  # pinned by ops',
    '    # about the note',
    '    note: "a value with a # inside it"',
    '',
    '  # detached from db by the blank line above',
    '',
    '  db:',
    '    image: postgres:16',
    '',
  ].join('\n');

  it('reads back the run that belongs to the key, and not the one above it', async () => {
    const core = await connect();
    try {
      const target = file('commented.yaml', COMMENTED);
      // `web` carries a two-line run; `note` carries a one-line run four lines
      // further down; `image` carries none above and one trailing. Three
      // different answers out of one file is what a single-comment fixture
      // cannot ask for.
      const web = await preview(core, target, [
        { operation: 'delete_comment', at: 'services.web', where: 'above' },
      ]);
      assert.equal(commentText(web.ops[0].before), 'about web, line one\nabout web, line two');

      const note = await preview(core, target, [
        { operation: 'delete_comment', at: 'services.web.note', where: 'above' },
      ]);
      assert.equal(commentText(note.ops[0].before), 'about the note');

      const trailing = await preview(core, target, [
        { operation: 'delete_comment', at: 'services.web.image', where: 'trailing' },
      ]);
      assert.equal(commentText(trailing.ops[0].before), 'pinned by ops');

      // …and `image` has nothing above it. The refusal is BY NAME: "there was
      // nothing to delete" and "the delete did nothing" are different
      // sentences and this client branches on which one it got.
      await assert.rejects(
        preview(core, target, [
          { operation: 'delete_comment', at: 'services.web.image', where: 'above' },
        ]),
        (err: unknown) => {
          assert.equal(classify(err), 'refused');
          assert.equal(reasonOf(err), 'no-comment');
          return true;
        },
      );
      assert.equal(readFileSync(target, 'utf8'), COMMENTED, 'reading a comment wrote bytes');
    } finally {
      core.dispose();
    }
  });

  it('replaces a two-line run whole and leaves every other byte alone', async () => {
    const core = await connect();
    try {
      const target = file('commented.yaml', COMMENTED);
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.web', where: 'above', value: 'one line now' },
      ]);
      assert.equal(
        readFileSync(target, 'utf8'),
        COMMENTED.replace(
          '  # about web, line one\n  # about web, line two\n',
          '  # one line now\n',
        ),
        'replacing a run moved something else',
      );
    } finally {
      core.dispose();
    }
  });

  it('adds a trailing comment after the closing quote, not at the first hash', async () => {
    const core = await connect();
    try {
      const target = file('commented.yaml', COMMENTED);
      // The `#` inside `"a value with a # inside it"` is the whole point. A
      // scan from the first hash on the line writes a marker into somebody's
      // string and leaves a file that still parses.
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.web.note', where: 'trailing', value: 'mine' },
      ]);
      const after = readFileSync(target, 'utf8');
      assert.match(after, /note: "a value with a # inside it" {2}# mine|note: "a value with a # inside it" # mine/);
      assert.equal(
        after.replace(/(note: "a value with a # inside it") +# mine/, '$1'),
        COMMENTED,
        `a trailing comment moved something else:\n${after}`,
      );
    } finally {
      core.dispose();
    }
  });

  it('leaves the whitespace in front of an existing trailing comment as the file wrote it', async () => {
    const core = await connect();
    try {
      const target = file('commented.yaml', COMMENTED);
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.web.image', where: 'trailing', value: 'pinned harder' },
      ]);
      assert.equal(
        readFileSync(target, 'utf8'),
        COMMENTED.replace('# pinned by ops', '# pinned harder'),
        'the two spaces before the marker were normalised',
      );
    } finally {
      core.dispose();
    }
  });

  it('does not touch a run a blank line has detached from the key below it', async () => {
    const core = await connect();
    try {
      const target = file('commented.yaml', COMMENTED);
      // A blank line breaks the attachment. `db` therefore has no comment
      // above it, and the run above the blank belongs to nothing.
      await assert.rejects(
        preview(core, target, [
          { operation: 'delete_comment', at: 'services.db', where: 'above' },
        ]),
        (err: unknown) => reasonOf(err) === 'no-comment',
      );
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.db', where: 'above', value: 'about db' },
      ]);
      const after = readFileSync(target, 'utf8');
      assert.ok(
        after.includes('  # detached from db by the blank line above\n\n  # about db\n  db:'),
        `the run above the blank was disturbed:\n${after}`,
      );
    } finally {
      core.dispose();
    }
  });

  it('writes one marker when the reader typed one', async () => {
    const core = await connect();
    try {
      const target = file('commented.yaml', COMMENTED);
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.db', where: 'above', value: '# already marked' },
      ]);
      assert.match(readFileSync(target, 'utf8'), /^ {2}# already marked$/m);
      assert.doesNotMatch(readFileSync(target, 'utf8'), /## already marked/);
    } finally {
      core.dispose();
    }
  });

  it('keeps a CRLF file CRLF when a comment goes into it', async () => {
    const core = await connect();
    try {
      const target = file('crlf.yaml', COMMENTED.replace(/\n/g, '\r\n'));
      const source = readFileSync(target);
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.db', where: 'above', value: 'about db' },
      ]);
      const after = readFileSync(target);
      // The assertion is on the BYTES. A line-oriented comparison cannot see a
      // missing `\r`, which is what let story 7.1's defect ship.
      assert.equal(
        after.toString('binary').replace('  # about db\r\n', ''),
        source.toString('binary'),
        'a comment inserted into a CRLF file did not carry CRLF',
      );
    } finally {
      core.dispose();
    }
  });

  // D1, and the shape of the check that missed it. The CRLF case above INSERTS
  // a comment, which needs no read-back — the half that cannot fail. This one
  // READS a run out of a CRLF file and writes it back UNCHANGED, which is what
  // the reader does when they open a comment, change nothing and press Enter.
  // It was refused with a sentence about TRAILING comments for an ABOVE block,
  // because every line of the run still carried its `\r`.
  it('reads a comment out of a CRLF file and writes it back unchanged', async () => {
    const core = await connect();
    try {
      const target = file('crlf-roundtrip.yaml', COMMENTED.replace(/\n/g, '\r\n'));
      const source = readFileSync(target);
      const run = await preview(core, target, [
        { operation: 'delete_comment', at: 'services.web', where: 'above' },
      ]);
      const text = commentText(run.ops[0].before);
      assert.ok(!text.includes('\r'), `the reader's textarea holds a literal CR: ${JSON.stringify(text)}`);
      assert.equal(text, 'about web, line one\nabout web, line two');

      // Back in, unchanged. The write is REFUSED, and the refusal is the right
      // one: `no-change` says the file already reads that way, which is a
      // sentence the pane has. `comment-text` — a complaint about a carriage
      // return the reader never typed, worded for TRAILING comments — was the
      // defect, and it arrived for an ABOVE block.
      await assert.rejects(
        apply(core, target, [
          { operation: 'set_comment', at: 'services.web', where: 'above', value: text },
        ]),
        (err: unknown) => {
          assert.equal(classify(err), 'refused');
          assert.equal(reasonOf(err), 'no-change');
          return true;
        },
      );
      assert.equal(readFileSync(target).toString('binary'), source.toString('binary'));

      // And the round trip that CAN move bytes: change the run, then write the
      // text that was read back over it. The file must return to itself byte
      // for byte — a line-oriented comparison cannot see a lost `\r`.
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.web', where: 'above', value: 'something else' },
      ]);
      await apply(core, target, [
        { operation: 'set_comment', at: 'services.web', where: 'above', value: text },
      ]);
      assert.equal(
        readFileSync(target).toString('binary'),
        source.toString('binary'),
        'a CRLF comment did not survive being read, changed and written back',
      );

      // And the trailing one, which is where the refusal's own sentence came
      // from: one line, and it still has to survive the round trip.
      const trailing = await preview(core, target, [
        { operation: 'delete_comment', at: 'services.web.image', where: 'trailing' },
      ]);
      const one = commentText(trailing.ops[0].before);
      assert.equal(one, 'pinned by ops');
      await assert.rejects(
        apply(core, target, [
          { operation: 'set_comment', at: 'services.web.image', where: 'trailing', value: one },
        ]),
        // Nothing changed, so the engine says so rather than rewriting the
        // bytes it already wrote. `no-change` is a refusal the pane has a
        // sentence for; a `comment-text` refusal here was the defect.
        (err: unknown) => {
          assert.equal(reasonOf(err), 'no-change');
          return true;
        },
      );
      assert.equal(readFileSync(target).toString('binary'), source.toString('binary'));
    } finally {
      core.dispose();
    }
  });

  /* -----------------------------------------------------------------------
   * Epic 9, story 9.3 — moving a value into a variable, through the real
   * engine and over the real wire.
   *
   * The criterion the story names as its own trap: it is not enough that the
   * compose file now contains `${NAME}`. The compose file has to be BYTE-
   * IDENTICAL to the source apart from that one value, and every check below
   * asserts it that way.
   * -------------------------------------------------------------------- */

  const SECRET = [
    '# a stack',
    'services:',
    '  web:',
    '    image: nginx:1.27   # pinned deliberately',
    '    environment:',
    '      POSTGRES_PASSWORD: hunter2',
    '      SAFE: fine',
    '',
  ].join('\n');

  /** A fresh directory, so each move sees a `.env` of its own or none at all. */
  function dir(name: string, body: string): { home: string; compose: string } {
    const home = mkdtempSync(path.join(scratch, 'move-'));
    const compose = path.join(home, name);
    writeFileSync(compose, body);
    return { home, compose };
  }

  const AT = 'services.web.environment.POSTGRES_PASSWORD';

  it('shows both diffs and writes neither file', async () => {
    const core = await connect();
    try {
      const { home, compose } = dir('compose.yaml', SECRET);
      const result = await extractPreview(core, compose, AT);
      assert.equal(result.name, 'POSTGRES_PASSWORD', 'no name was derived from the key');
      assert.equal(result.value, 'hunter2');
      assert.match(result.compose.diff, /\+ {6}POSTGRES_PASSWORD: \$\{POSTGRES_PASSWORD\}/);
      assert.match(result.env_diff ?? '', /\+POSTGRES_PASSWORD=hunter2/);
      assert.equal(result.env_created, true);
      assert.equal(result.written, false);
      // Neither file. Asserted by comparing bytes and by the absence of the
      // `.env`, never by the absence of an error.
      assert.equal(readFileSync(compose, 'utf8'), SECRET, 'a preview changed the compose file');
      assert.equal(existsSync(path.join(home, '.env')), false, 'a preview created the .env');
    } finally {
      core.dispose();
    }
  });

  it('writes the .env beside the compose file and changes one value in it', async () => {
    const core = await connect();
    try {
      const { home, compose } = dir('compose.yaml', SECRET);
      const result = await extractApply(core, compose, AT);
      assert.equal(result.written, true);
      // THE criterion. Not "the file now contains ${POSTGRES_PASSWORD}".
      assert.equal(
        readFileSync(compose, 'utf8'),
        SECRET.replace(
          '      POSTGRES_PASSWORD: hunter2',
          '      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}',
        ),
        'the move touched more than the one value',
      );
      // …and the `.env` is the one beside the compose file, not an env_file.
      assert.equal(path.dirname(result.env_file), home);
      assert.equal(readFileSync(path.join(home, '.env'), 'utf8'), 'POSTGRES_PASSWORD=hunter2\n');
    } finally {
      core.dispose();
    }
  });

  /* ---- story 9.6, end to end against the real binary -------------------
   *
   * The proof that D1/D4 is actually closed rather than merely typed. The
   * extension records the preview's own range and sends it back; a colleague's
   * edit lands in between; the REAL core refuses with `ErrStaleRange`, which is
   * what makes `panel.ts`'s `classify(err) === 'stale'` branch reachable at
   * all. Before 2026-08-15 no `expect` was sent, so this write silently
   * succeeded against bytes nobody had looked at.
   */
  it('refuses when the file moved under the range the extension recorded', async () => {
    const core = await connect();
    try {
      const { home, compose } = dir('compose.yaml', SECRET);
      const preview = await extractPreview(core, compose, AT);
      const expect = expectOf(preview.compose);
      assert.ok(expect, 'the preview reported no operation to assert against');

      // Somebody else got there first. One line at the top shifts every offset
      // below it — exactly what a colleague's commit does.
      const moved = `# somebody else got here first\n${SECRET}`;
      writeFileSync(compose, moved);

      await assert.rejects(
        extractApply(core, compose, AT, undefined, expect, preview.env_expect),
        (err: unknown) => {
          assert.equal(classify(err), 'stale', 'the core did not treat the moved range as stale');
          return true;
        },
      );
      // Neither file, asserted by bytes rather than by the absence of an error.
      assert.equal(readFileSync(compose, 'utf8'), moved, 'a stale move wrote the compose file');
      assert.equal(existsSync(path.join(home, '.env')), false, 'a stale move created the .env');
    } finally {
      core.dispose();
    }
  });

  it('writes when the recorded range still holds, so the assertion is not simply refusing everything', async () => {
    const core = await connect();
    try {
      const { compose } = dir('compose.yaml', SECRET);
      const preview = await extractPreview(core, compose, AT);
      const result = await extractApply(
        core,
        compose,
        AT,
        undefined,
        expectOf(preview.compose),
        preview.env_expect,
      );
      assert.equal(result.written, true, 'an unchanged file was refused as stale');
    } finally {
      core.dispose();
    }
  });

  it('appends to an existing .env without moving a byte of it', async () => {
    const core = await connect();
    try {
      const { home, compose } = dir('compose.yaml', SECRET);
      const env = ['# secrets, do not commit', '', 'OTHER=value', 'THIRD=another', ''].join('\n');
      writeFileSync(path.join(home, '.env'), env);
      const result = await extractApply(core, compose, AT);
      assert.equal(result.env_created, false, 'an existing .env was reported as created');
      assert.equal(
        readFileSync(path.join(home, '.env'), 'utf8'),
        `${env}POSTGRES_PASSWORD=hunter2\n`,
        'the existing .env did not survive byte for byte',
      );
    } finally {
      core.dispose();
    }
  });

  it('refuses a name the .env already gives a different value, and touches nothing', async () => {
    const core = await connect();
    try {
      const { home, compose } = dir('compose.yaml', SECRET);
      const env = 'POSTGRES_PASSWORD=somebody-elses\n';
      writeFileSync(path.join(home, '.env'), env);
      await assert.rejects(extractApply(core, compose, AT), (err: unknown) => {
        assert.equal(classify(err), 'refused');
        assert.equal(reasonOf(err), 'var-conflict');
        return true;
      });
      assert.equal(readFileSync(compose, 'utf8'), SECRET, 'a refused move wrote the compose file');
      assert.equal(readFileSync(path.join(home, '.env'), 'utf8'), env, 'a refused move wrote the .env');
    } finally {
      core.dispose();
    }
  });

  it('is idempotent when the .env already says the same thing', async () => {
    const core = await connect();
    try {
      const { home, compose } = dir('compose.yaml', SECRET);
      const env = '# mine\nPOSTGRES_PASSWORD=hunter2\n';
      writeFileSync(path.join(home, '.env'), env);
      const result = await extractApply(core, compose, AT);
      assert.equal(result.env_unchanged, true);
      assert.equal(readFileSync(path.join(home, '.env'), 'utf8'), env, 'the .env was rewritten for nothing');
      assert.match(readFileSync(compose, 'utf8'), /\$\{POSTGRES_PASSWORD\}/);
    } finally {
      core.dispose();
    }
  });

  it('refuses a value that already comes from a variable', async () => {
    const core = await connect();
    try {
      const { compose } = dir(
        'compose.yaml',
        SECRET.replace('POSTGRES_PASSWORD: hunter2', 'POSTGRES_PASSWORD: ${DB_PASSWORD}'),
      );
      await assert.rejects(extractPreview(core, compose, AT), (err: unknown) => {
        assert.equal(classify(err), 'refused');
        assert.equal(reasonOf(err), 'already-interpolated');
        return true;
      });
    } finally {
      core.dispose();
    }
  });

  it('keeps the variable’s own name in the list form of environment', async () => {
    const core = await connect();
    try {
      const listForm = [
        'services:',
        '  web:',
        '    image: nginx',
        '    environment:',
        '      - POSTGRES_PASSWORD=hunter2',
        '      - SAFE=fine',
        '',
      ].join('\n');
      const { compose } = dir('compose.yaml', listForm);
      await extractApply(core, compose, 'services.web.environment[0]');
      // `- ${POSTGRES_PASSWORD}` is an entry with no `=`, which Compose reads
      // as pass-through-by-name — a different meaning, silently.
      assert.equal(
        readFileSync(compose, 'utf8'),
        listForm.replace(
          '      - POSTGRES_PASSWORD=hunter2',
          '      - POSTGRES_PASSWORD=${POSTGRES_PASSWORD}',
        ),
      );
    } finally {
      core.dispose();
    }
  });

  it('refuses a Dockerfile by name, saying the equivalent is an ARG', async () => {
    const core = await connect();
    try {
      const { compose } = dir('Dockerfile', DOCKERFILE);
      await assert.rejects(extractPreview(core, compose, 'anything'), (err: unknown) => {
        assert.equal(classify(err), 'refused');
        assert.equal(reasonOf(err), 'wrong-grammar');
        const said = refusalDetail(err);
        assert.match(said, /ARG/);
        assert.match(said, /build\.args|build arguments/);
        return true;
      });
    } finally {
      core.dispose();
    }
  });

  // assertion back in the hook — where it fired, printed the violation in full,
  // and `npm test` still exited 0 with `fail 0`. node:test does not count a
  // hook failure as a failing test, so a guard living in one cannot block a
  // pull request. It is declared last so it runs after the writes it guards.
  it('wrote nothing outside its own scratch directory', () => {
    const now = hashTree(path.join(__dirname, 'testdata'));
    const changed: string[] = [];
    for (const [file, digest] of guarded) {
      if (!now.has(file)) {
        changed.push(`${file} was DELETED`);
      } else if (now.get(file) !== digest) {
        changed.push(`${file} was MODIFIED`);
      }
    }
    for (const file of now.keys()) {
      if (!guarded.has(file)) {
        changed.push(`${file} was CREATED`);
      }
    }
    assert.deepEqual(
      changed,
      [],
      `the write-path suite touched a file outside its scratch directory:\n  ${changed.join('\n  ')}`,
    );
    // And the scratch directory really is somewhere disposable, rather than a
    // path that happens to sit inside the working tree.
    assert.ok(
      scratch.startsWith(tmpdir()),
      `the scratch directory is ${scratch}, which is not under the OS temp directory`,
    );
  });
});
