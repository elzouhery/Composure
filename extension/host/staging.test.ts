// The staging store and the wording around it — story 6.1.
//
// Nothing here can prove that no byte reached a file; that is asserted on the
// Go side, over the real binary, in internal/edit and cmd/composure. What this
// suite covers is the part that lives only in TypeScript: which operation a
// gesture becomes, how two edits to one field combine, and the exact words on
// the control that writes.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  Staging,
  addEntryKey,
  commentKey,
  describeEdit,
  fileName,
  instructionKey,
  missingDockerfileNodes,
  parentOf,
  pendingValues,
  saveLabel,
  stageKey,
  type StagedEdit,
} from './staging';
import { CoreError, RpcError } from './core';
import {
  CODE_EDIT_REFUSED,
  CODE_STALE_RANGE,
  EDIT_REASONS,
  STALE_MESSAGE,
  classify,
  commentText,
  expectOf,
  reasonOf,
  refusalDetail,
  type EditReason,
} from './edit';
import { ADD_KINDS } from '../shared/protocol';
import type { Finding, Origin } from '../shared/protocol';

const edit = (key: string, at: string, value: string): StagedEdit => ({
  key,
  label: '',
  op: { operation: 'replace_scalar', at, value },
});

describe('Staging', () => {
  it('holds edits per file, in the order they were staged', () => {
    const s = new Staging();
    s.set('/w/compose.yml', edit('services.web.image', 'services.web.image', 'nginx:1.28'));
    s.set('/w/compose.yml', edit('services.db.image', 'services.db.image', 'postgres:17'));
    s.set('/w/other.yml', edit('services.a.image', 'services.a.image', 'x'));

    assert.deepEqual(s.paths('/w/compose.yml'), ['services.web.image', 'services.db.image']);
    assert.equal(s.count('/w/compose.yml'), 2);
    assert.equal(s.count('/w/other.yml'), 1);
    assert.equal(s.count('/w/nothing.yml'), 0);
  });

  // Two edits to one field are one edit. Appending both would apply them in
  // order and make the first invisible, so the diff on screen would not be the
  // diff the reader thinks they asked for.
  it('replaces an earlier edit on the same key rather than stacking them', () => {
    const s = new Staging();
    s.set('/w/c.yml', edit('services.web.image', 'services.web.image', 'nginx:1.28'));
    s.set('/w/c.yml', edit('services.web.image', 'services.web.image', 'nginx:1.29'));
    assert.equal(s.count('/w/c.yml'), 1);
    assert.deepEqual(s.ops('/w/c.yml'), [
      { operation: 'replace_scalar', at: 'services.web.image', value: 'nginx:1.29' },
    ]);
  });

  it('drops one stage and reports whether there was one', () => {
    const s = new Staging();
    s.set('/w/c.yml', edit('a', 'a', '1'));
    assert.equal(s.remove('/w/c.yml', 'b'), false);
    assert.equal(s.remove('/w/c.yml', 'a'), true);
    assert.equal(s.count('/w/c.yml'), 0);
  });

  // AD-19's mechanism at this level: clearing is total. A stage held against a
  // file that moved is discarded, never partly kept.
  it('clears a whole file and says whether anything was held', () => {
    const s = new Staging();
    assert.equal(s.clear('/w/c.yml'), false);
    s.set('/w/c.yml', edit('a', 'a', '1'));
    assert.equal(s.clear('/w/c.yml'), true);
    assert.deepEqual(s.ops('/w/c.yml'), []);
  });
});

describe('the write control', () => {
  // EXPERIENCE.md: a control says exactly what happens, and it names the file,
  // because in a multi-file project that is the question the reader has.
  it('names the file it writes to', () => {
    assert.equal(saveLabel('/w/project/compose.yml'), 'Save to compose.yml');
    assert.equal(saveLabel('C:\\w\\compose.override.yml'), 'Save to compose.override.yml');
    assert.equal(fileName('/w/api/Dockerfile'), 'Dockerfile');
  });

  it('describes an edit in the file’s own words, never a friendlier rewording', () => {
    assert.equal(
      describeEdit({ operation: 'replace_scalar', at: 'services.web.image', value: 'nginx:1.28' }),
      'services.web.image → nginx:1.28',
    );
    assert.equal(
      describeEdit({ operation: 'insert_key', at: 'services.db', key: 'restart', value: 'always' }),
      'add restart: always',
    );
    assert.equal(
      describeEdit({ operation: 'set_base_image', stage: 1, value: 'alpine:3.20' }),
      'stage 1 base image → alpine:3.20',
    );
  });

  it('keys Dockerfile stages apart from instructions', () => {
    assert.equal(stageKey(0), 'stage:0');
    assert.equal(instructionKey(0), 'instruction:0');
    assert.notEqual(stageKey(0), instructionKey(0));
  });
});

describe('parentOf', () => {
  // Clicking an unset key stages an insert into the MAPPING that holds it, so
  // getting this wrong puts the key in the wrong service.
  it('is the mapping a key is inserted into', () => {
    assert.equal(parentOf('services.db.healthcheck'), 'services.db');
    assert.equal(parentOf('services.web.ports[3]'), 'services.web.ports');
    assert.equal(parentOf('services'), '');
    assert.equal(parentOf(''), '');
  });
});

describe('pendingValues', () => {
  // The field shows what the reader typed; the line beneath says what the file
  // still holds. Only a scalar replacement has a value to show.
  it('maps a staged scalar to its new value and ignores an insert', () => {
    assert.deepEqual(
      pendingValues([
        edit('services.web.image', 'services.web.image', 'nginx:1.28'),
        { key: 'services.db.restart', label: '', op: { operation: 'insert_key', at: 'services.db', key: 'restart', value: 'always' } },
      ]),
      { 'services.web.image': 'nginx:1.28' },
    );
  });
});

describe('missingDockerfileNodes', () => {
  const origin: Origin = { file: '/w/compose.yml', line: 3, column: 5, step: 0 };
  const finding = (rule: string, path: string): Finding => ({
    rule,
    severity: 'warning',
    title: 't',
    message: 'm',
    subjects: [],
    anchors: [{ label: 'declared here', path, origin }],
  });

  // Story 6.3: the node renders as missing, and the fact comes from the CORE's
  // finding. A stat() in the extension would be a second answer, and it would
  // disagree with the problems panel the first time a context held a ${VAR}.
  it('collects the nodes the core reported missing, and nothing else', () => {
    assert.deepEqual(
      missingDockerfileNodes([
        finding('build-dockerfile-missing', 'services.api.build'),
        finding('plaintext-credential', 'services.db.environment.PASSWORD'),
        finding('build-dockerfile-missing', 'services.api.build'),
      ]),
      ['services.api.build'],
    );
    assert.deepEqual(missingDockerfileNodes([]), []);
  });
});

describe('classifying a failed edit', () => {
  // Three outcomes, three different responses. Collapsing them either loses the
  // reader's other staged work or writes over someone else's change.
  it('tells a stale range from a refusal from a fault', () => {
    assert.equal(classify(new RpcError(CODE_STALE_RANGE, 'moved', { reason: 'stale-range' })), 'stale');
    assert.equal(classify(new RpcError(CODE_EDIT_REFUSED, 'flow', { reason: 'flow-style' })), 'refused');
    assert.equal(classify(new RpcError(-32603, 'boom', undefined)), 'failed');
    assert.equal(classify(new CoreError('core-crashed', 'gone')), 'failed');
    assert.equal(classify(new Error('anything')), 'failed');
  });

  it('carries the core’s reason slug through', () => {
    assert.equal(reasonOf(new RpcError(CODE_EDIT_REFUSED, 'x', { reason: 'multi-line' })), 'multi-line');
    assert.equal(reasonOf(new Error('x')), undefined);
  });

  // EXPERIENCE.md's voice: no apologies, no exclamation, never "invalid
  // configuration". The reader is told the shape of the obstacle.
  it('says what could not be done and why, for each known refusal', () => {
    const flow = refusalDetail(new RpcError(CODE_EDIT_REFUSED, 'x', { reason: 'flow-style' }));
    assert.match(flow, /flow style/);
    assert.match(flow, /will not parse/);

    const multi = refusalDetail(new RpcError(CODE_EDIT_REFUSED, 'x', { reason: 'multi-line' }));
    assert.match(multi, /spans several lines/);

    // An unknown reason still says something concrete rather than going blank.
    assert.equal(refusalDetail(new Error('the path does not resolve')), 'the path does not resolve');
  });

  // Story 6.5. `position-mismatch` used to have no arm at all, so the reader
  // was shown the engine's own sentence — "the parser's position does not match
  // the file's bytes… reported at byte 1204, where the file reads ` \"x`" —
  // which is accurate, is about the parser, and tells nobody what to do.
  //
  // The sentinel has TWO producers: the rune-vs-byte column skew
  // (testdata/edge/e17-multibyte-flow.yml) and the CRLF comment-line miscount
  // guarded at internal/strategy/structural.go. So the sentence names the
  // disagreement itself — something on that line is counted differently by the
  // parser and by the file — rather than runes, which would be wrong for half
  // the cases it will be shown for.
  it('names the obstacle behind a position mismatch, not the parser', () => {
    const text = refusalDetail(
      new RpcError(CODE_EDIT_REFUSED, 'the parser’s position does not match the file’s bytes', {
        reason: 'position-mismatch',
      }),
    );
    // Not the default arm: the engine's own sentence must not be what the
    // reader reads.
    assert.doesNotMatch(text, /parser/i, 'the refusal talks about the parser');
    assert.doesNotMatch(text, /byte|rune|offset|column/i, 'the refusal is in engine units');
    // The three things the reader needs: what the obstacle is, that the file is
    // untouched, and what makes the value editable.
    assert.match(text, /counts?\b|counted/i, 'it does not say what the disagreement is');
    assert.match(text, /nothing was written/i, 'it does not say the file is untouched');
    assert.match(text, /line of its own|own line/i, 'it does not say what makes the value editable');
  });

  // The voice rule applied to EVERY branch, not to two hand-picked ones.
  //
  // It used to be two `doesNotMatch` calls sitting under the two messages that
  // had been written correctly, so `no-change`, `would-corrupt`, `stale-range`
  // and the STALE_MESSAGE strip were all unchecked — and a new reason worded
  // "Sorry, that failed!" would have shipped green.
  it('keeps the voice in every refusal it can produce', () => {
    // ENUMERATED, not listed. This used to be five literals while the core
    // could already return eight slugs, so `no-root-mapping`, `not-a-sequence`
    // and `null-entry` — every one of them reachable from a button on screen —
    // were unchecked, and a reader who hit one was shown the engine's own
    // sentence. A slug added to EDIT_REASONS is covered by this pass the day it
    // is added.
    const reasons: EditReason[] = [...EDIT_REASONS];
    const said = new Map<string, string>();
    for (const reason of reasons) {
      const text = refusalDetail(new RpcError(CODE_EDIT_REFUSED, 'the core said this', { reason }));
      assert.doesNotMatch(text, /sorry|apolog|oops|!/i, `${reason} apologises or exclaims`);
      assert.doesNotMatch(text, /invalid configuration/i, `${reason} says "invalid configuration"`);
      assert.ok(text.trim().length > 0, `${reason} refuses with nothing at all`);
      said.set(reason, text);
    }
    // Distinct answers, or the reader is told the same thing whatever went
    // wrong — which is what "says what could not be done" exists to prevent.
    assert.equal(
      new Set(said.values()).size,
      reasons.length,
      `two refusals produce the same sentence:\n  ${[...said].map(([r, t]) => `${r}: ${t}`).join('\n  ')}`,
    );
    assert.doesNotMatch(STALE_MESSAGE, /sorry|apolog|!/i);
    assert.match(STALE_MESSAGE, /discarded/, 'the stale strip does not say what happened to the work');
  });

  // The KIND set is written in two languages too, and drifts the same way: a
  // kind this extension offers and the core does not is a button that refuses
  // whatever the reader types into it, and a kind the core gained and this list
  // never heard of is a capability with no control. So `AddKinds` in
  // internal/edit/add.go is read and compared, exactly as the slugs are.
  it('offers the kinds the core can actually declare, and no others', () => {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { readFileSync, existsSync } = require('node:fs') as typeof import('node:fs');
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const path = require('node:path') as typeof import('node:path');
    const source = path.join(__dirname, '..', '..', 'internal', 'edit', 'add.go');
    if (!existsSync(source)) {
      return; // a packaged tree with no Go sources beside it
    }
    const go = readFileSync(source, 'utf8');
    const decl = go.slice(go.indexOf('var AddKinds'));
    const list = decl.slice(0, decl.indexOf('}'));
    const kinds = [...list.matchAll(/"([a-z]+)"/g)].map((m) => m[1]);
    assert.ok(kinds.length >= 5, `only ${kinds.length} kinds found in AddKinds; the scan is broken`);
    assert.deepEqual(
      [...ADD_KINDS],
      kinds,
      'internal/edit and shared/protocol.ts disagree about what can be declared',
    );
  });

  // The slug set is written in two languages: `edit.Reason` in Go returns them
  // and this module branches on them. Nothing in either type system spans that
  // gap — a slug the core sends and this file has never heard of falls through
  // to the default arm, and the reader is shown engine language in place of
  // prose. So the Go source is read and compared.
  it('has an arm for every slug the core can actually send', () => {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const { readFileSync, existsSync } = require('node:fs') as typeof import('node:fs');
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const path = require('node:path') as typeof import('node:path');
    const source = path.join(__dirname, '..', '..', 'internal', 'edit', 'edit.go');
    if (!existsSync(source)) {
      // Only in a packaged tree with no Go sources beside it; in the repository
      // this always runs.
      return;
    }
    const go = readFileSync(source, 'utf8');
    const body = go.slice(go.indexOf('func Reason('));
    const slugs = [...body.matchAll(/return "([a-z-]+)"/g)].map((m) => m[1]);
    // `Reason` returns named constants as well as literals — the five slugs
    // decision 21 added are `ReasonInherited` and friends, declared in
    // internal/edit/inherited.go. A scan that read only the literals would
    // report that this file has an arm for every slug the core can send while
    // five of them went unchecked, which is the exact shape of the gap this
    // check exists to close.
    const constants = path.join(__dirname, '..', '..', 'internal', 'edit', 'inherited.go');
    if (existsSync(constants)) {
      const declared = new Map(
        [...readFileSync(constants, 'utf8').matchAll(/^\t(Reason[A-Za-z]+)\s*=\s*"([a-z-]+)"/gm)].map(
          (m) => [m[1], m[2]],
        ),
      );
      for (const m of body.matchAll(/return (Reason[A-Za-z]+)/g)) {
        const slug = declared.get(m[1]);
        assert.ok(slug, `edit.Reason returns ${m[1]}, which is not declared in inherited.go`);
        slugs.push(slug);
      }
    }
    assert.ok(slugs.length >= 8, `only ${slugs.length} slugs found in edit.Reason; the scan is broken`);
    for (const slug of slugs) {
      assert.ok(
        (EDIT_REASONS as readonly string[]).includes(slug),
        `internal/edit can return "${slug}" and host/edit.ts has no arm for it: ` +
          'the reader would be shown the engine’s own sentence',
      );
      if (slug === 'stale-range') {
        // The one slug whose answer IS the core's sentence, deliberately: it
        // names the byte range that moved, which no prose here could.
        continue;
      }
      const text = refusalDetail(new RpcError(CODE_EDIT_REFUSED, 'the core said this', { reason: slug as EditReason }));
      assert.notEqual(text, 'the core said this', `"${slug}" falls through to the default arm`);
    }
  });
});


/* -------------------------------------------------------------------------
 * Epic 9 — the wording and the keys.
 * ---------------------------------------------------------------------- */

describe('a comment read back out of the engine’s own bytes — story 9.1', () => {
  // The trap named in the story: a fixture with ONE comment proves nothing.
  // A run of comment lines is ONE comment (DECISIONS.md 23), so the shape that
  // matters is several lines at an indent, and every one of them has to arrive.
  it('strips the indent and the marker from a run of comment lines', () => {
    assert.equal(
      commentText('    # one\n    # two\n    # three\n'),
      'one\ntwo\nthree',
    );
  });

  it('takes exactly one space after the marker, and no more', () => {
    // `#   indented in the comment` is the reader's own layout inside their
    // sentence. Eating it would rewrite what they wrote.
    assert.equal(commentText('# pinned by ops'), 'pinned by ops');
    assert.equal(commentText('#pinned'), 'pinned');
    assert.equal(commentText('#   three spaces'), '  three spaces');
  });

  it('reads an empty comment line as an empty line, not as nothing', () => {
    assert.equal(commentText('  # one\n  #\n  # three\n'), 'one\n\nthree');
  });

  it('answers the empty string for no comment at all', () => {
    assert.equal(commentText(''), '');
  });

  // D1. The run comes back with the FILE's line endings in it, and a CRLF file
  // sends `\r\n`. Splitting on `\n` alone left a `\r` at the end of every line
  // but the last, `commentBody` refuses any text carrying one (ErrCommentText),
  // and the reader saw a refusal about TRAILING comments for an ABOVE block —
  // after changing nothing. The literal CRs were in the textarea too.
  it('reads a run out of a CRLF file without carrying the carriage returns', () => {
    assert.equal(commentText('    # one\r\n    # two\r\n'), 'one\ntwo');
    assert.equal(commentText('# pinned by ops\r\n'), 'pinned by ops');
    assert.ok(!commentText('  # one\r\n  #\r\n  # three\r\n').includes('\r'));
    assert.equal(commentText('  # one\r\n  #\r\n  # three\r\n'), 'one\n\nthree');
    // A file with mixed endings is a real file, and neither line may keep its
    // ending: the ending is the FILE's to choose and the comment's text is the
    // reader's.
    assert.equal(commentText('  # one\r\n  # two\n'), 'one\ntwo');
  });

  // The reader's doubled marker is theirs. Stripping one of the two here and
  // writing one back in the engine loses it, so a `##` section header could not
  // survive being opened in the pane and saved unchanged.
  it('keeps a marker the reader doubled', () => {
    assert.equal(commentText('  ## a section\n'), '## a section');
    assert.equal(commentText('  ### deeper\n'), '### deeper');
    // And one marker is still the engine's, at every spelling of the gap.
    assert.equal(commentText('  # a sentence\n'), 'a sentence');
  });
});

describe('the staging keys Epic 9 adds', () => {
  it('keys a comment by position as well as by path', () => {
    // Two positions on one key are two stages, and both land. One position
    // staged twice replaces.
    assert.notEqual(
      commentKey('services.web.image', 'above'),
      commentKey('services.web.image', 'trailing'),
    );
    assert.equal(commentKey('services.web.image', 'above'), commentKey('services.web.image', 'above'));
  });

  it('keys an added entry by its text, so two additions to one list both land', () => {
    assert.notEqual(
      addEntryKey('services.web.ports', '8080:80'),
      addEntryKey('services.web.ports', '8443:443'),
    );
  });

  it('names every Epic 9 operation in the strip', () => {
    assert.equal(
      describeEdit({ operation: 'insert_sequence_entry', at: 'services.web.ports', value: '9090:90' }),
      'add 9090:90 to services.web.ports',
    );
    assert.match(
      describeEdit({ operation: 'set_comment', at: 'services.web.image', where: 'above', value: 'x' }),
      /above services\.web\.image/,
    );
    assert.match(
      describeEdit({ operation: 'set_comment', at: 'services.web.image', where: 'trailing', value: 'x' }),
      /line of services\.web\.image/,
    );
    assert.match(
      describeEdit({ operation: 'delete_comment', at: 'services.web.image', where: 'above' }),
      /remove the above comment/,
    );
  });
});

/* -------------------------------------------------------------------------
 * The assertion an apply sends back — story 9.6, DECISIONS.md 28.
 *
 * `expectOf` is the whole of the client's half of the staleness contract on the
 * two extract paths. Before 2026-08-15 neither apply sent anything, which made
 * `panel.ts`'s stale branch on both paths unreachable code.
 * ---------------------------------------------------------------------- */

describe('the assertion recorded from a preview', () => {
  const op = (over: Record<string, unknown> = {}): any => ({
    operation: 'replace_scalar',
    range: { start: 4, end: 11, line: 2 },
    before: 'hunter2',
    describe: '',
    ...over,
  });

  it('is the range and the bytes the preview reported', () => {
    assert.deepEqual(expectOf({ ops: [op()] }), { start: 4, end: 11, text: 'hunter2' });
  });

  // The trap this guard exists for. `stack/extract-arg` emits the `ARG`
  // declaration as an INSERT, whose range is `[0, 0)` by definition, and an
  // insert can come first. Asserting that an empty range still holds the empty
  // string is true of every file that has ever existed, so taking `ops[0]`
  // blindly would send an assertion that cannot fail — protection reported and
  // not present, which is worse than none.
  it('skips an insertion’s empty range and takes the substitution’s', () => {
    const insert = op({ operation: 'insert_instruction_before', range: { start: 0, end: 0, line: 1 }, before: '' });
    assert.deepEqual(
      expectOf({ ops: [insert, op()] }),
      { start: 4, end: 11, text: 'hunter2' },
      'an empty range was sent as the assertion',
    );
  });

  // No assertion is the documented pre-9.6 behaviour and it is honest: the core
  // applies against the files as they stand. A FABRICATED assertion is not.
  it('is nothing at all when there is nothing assertable', () => {
    const insert = op({ operation: 'insert_key', range: { start: 0, end: 0, line: 1 }, before: '' });
    assert.equal(expectOf({ ops: [insert] }), undefined);
    assert.equal(expectOf({ ops: [] }), undefined);
    assert.equal(expectOf(undefined), undefined);
  });
});
