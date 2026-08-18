// The pending-diff strip and the Dockerfile stage form — stories 6.1 and 6.2.
//
// A webview cannot be launched under `node --test`, so what is tested is what
// was extracted: every sentence the reader reads, and every decision about what
// they see. The rendering below these functions is element creation; the parts
// that can be WRONG rather than merely ugly all live here.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  diffBodyLines,
  diffLineKind,
  firstChangeLine,
  isFileHeaderLine,
  pendingLabel,
  pendingSummary,
} from './pending';
import { fileMarks, instructionLabel, stageDetail, stageHeading } from './stageform';
import { markerIndex } from './layout';
import type { DockerInstruction, DockerStage, DockerfileForm } from '../shared/protocol';

describe('the pending summary', () => {
  // Spelled out, never `+1/-1`: the cryptic badge is the anti-pattern the
  // design names, and this line is the reader's evidence that a one-value
  // change is a one-line change.
  it('says the counts in words', () => {
    assert.equal(pendingSummary(1, 1, 1), '1 edit · 1 line removed, 1 added');
    assert.equal(pendingSummary(3, 2, 5), '3 edits · 2 lines removed, 5 added');
    assert.equal(pendingSummary(1, 0, 1), '1 edit · 0 lines removed, 1 added');
  });

  it('tells a screen reader there is unwritten work, and names the file', () => {
    assert.equal(pendingLabel(1, '/w/compose.yml'), '1 staged edit against /w/compose.yml, not yet written');
    assert.match(pendingLabel(2, '/w/compose.yml'), /^2 staged edits/);
    assert.match(pendingLabel(1, '/w/compose.yml'), /not yet written/);
  });
});

describe('diffLineKind', () => {
  // The `---` and `+++` headers are not additions and removals. Colouring them
  // as though they were would put two extra green lines on every diff, which
  // is exactly the kind of miscount this product cannot afford to display.
  it('does not mistake the file headers for changes', () => {
    assert.equal(diffLineKind('--- a/compose.yaml'), 'meta');
    assert.equal(diffLineKind('+++ b/compose.yaml'), 'meta');
    assert.equal(diffLineKind('\\ No newline at end of file'), 'meta');
  });

  it('classifies hunk headers, additions, removals and context', () => {
    assert.equal(diffLineKind('@@ -27,7 +27,7 @@'), 'hunk');
    assert.equal(diffLineKind('+    image: nginx:1.28'), 'add');
    assert.equal(diffLineKind('-    image: nginx:1.27'), 'del');
    assert.equal(diffLineKind('     ports:'), 'ctx');
  });
});

/* -------------------------------------------------------------------------
 * What the strip SHOWS, as opposed to what the core sent.
 *
 * Measured before this existed: with one scalar edit staged, the diff box was
 * 51px tall and its content was 154px. The three lines that fitted were
 * `--- a/compose.yaml`, `+++ b/compose.yaml` and `@@ -27,7 +27,7 @@`; the `−`
 * and `+` lines were lines 7 and 8. The reader could not see the change they
 * were about to write — on the one screen the whole product exists for.
 * ---------------------------------------------------------------------- */

describe('the diff shows the change, not the header', () => {
  const diff = [
    '--- a/compose.yaml',
    '+++ b/compose.yaml',
    '@@ -27,7 +27,7 @@',
    '     ports:',
    '       - 8080:80',
    '     image: x',
    '-    image: ghcr.io/shipyard/web:2.4.1',
    '+    image: ghcr.io/shipyard/web:2.5.0',
    '     restart: always',
    '',
  ].join('\n');

  // MUTATION: `isFileHeaderLine` returns false. The two file-name lines come
  // back and take the top of the box again.
  it('drops the two lines that name a file the Save button already names', () => {
    assert.equal(isFileHeaderLine('--- a/compose.yaml', 0), true);
    assert.equal(isFileHeaderLine('+++ b/compose.yaml', 1), true);
    // NOT dropped: it says WHERE, which nothing else in the strip says.
    assert.equal(isFileHeaderLine('@@ -27,7 +27,7 @@', 2), false);
    // NOT dropped: a statement about the bytes, which is the subject here.
    assert.equal(isFileHeaderLine('\\ No newline at end of file', 9), false);
    // NOT dropped, and this is the one that matters: removing a line that
    // itself starts with `--` produces a line `diffLineKind` cannot tell from a
    // header. Mis-colouring it is survivable; deleting it is not.
    assert.equal(isFileHeaderLine('---    image: x', 6), false);
    assert.equal(isFileHeaderLine('-    image: x', 6), false);
    assert.equal(isFileHeaderLine('+    image: x', 7), false);
  });

  it('renders the rest verbatim, in order, with the blank tail dropped', () => {
    assert.deepEqual(diffBodyLines(diff), [
      '@@ -27,7 +27,7 @@',
      '     ports:',
      '       - 8080:80',
      '     image: x',
      '-    image: ghcr.io/shipyard/web:2.4.1',
      '+    image: ghcr.io/shipyard/web:2.5.0',
      '     restart: always',
    ]);
  });

  it('is two lines shorter than what the core sent, and no shorter', () => {
    // The count, not the shape: a filter that dropped context lines would pass
    // every assertion above about the header and quietly delete evidence.
    const sent = diff.split('\n').filter((l) => l !== '');
    assert.equal(diffBodyLines(diff).length, sent.length - 2);
  });

  // MUTATION: `firstChangeLine` returns 0. The box then opens on the hunk
  // header — which is context by definition — whenever it has to scroll.
  it('scrolls to one line above the first change, not to the top', () => {
    const lines = diffBodyLines(diff);
    assert.equal(firstChangeLine(lines), 3);
    assert.equal(lines[firstChangeLine(lines)], '     image: x');
    assert.equal(lines[firstChangeLine(lines) + 1], '-    image: ghcr.io/shipyard/web:2.4.1');
  });

  it('finds an addition when there is no removal before it', () => {
    assert.equal(firstChangeLine(['@@ -1 +1 @@', ' a', '+b']), 1);
  });

  it('stays at the top when a diff has no change in it at all', () => {
    assert.equal(firstChangeLine(['@@ -1 +1 @@', ' a', ' b']), 0);
    assert.equal(firstChangeLine([]), 0);
  });

  it('does not go negative when the change is the very first line', () => {
    assert.equal(firstChangeLine(['-a', '+b']), 0);
  });
});

/* -------------------------------------------------------------------------
 * The stage form.
 * ---------------------------------------------------------------------- */

const instruction = (over: Partial<DockerInstruction> = {}): DockerInstruction => ({
  index: 1,
  kind: 'instruction',
  name: 'RUN',
  name_raw: 'RUN',
  text: 'RUN npm ci',
  start_line: 3,
  end_line: 3,
  editable: true,
  ...over,
});

/** An empty vocabulary: this suite is about the pending strip, not story 7.8. */
const noVocabulary = (scope: 'file' | 'stage') => ({
  scope,
  declared_count: 0,
  available_count: 0,
  instructions: [],
});

const stage = (over: Partial<DockerStage> = {}): DockerStage => ({
  index: 0,
  name: 'build',
  label: 'build',
  image_ref: 'node:18-alpine',
  from: instruction({ name: 'FROM', kind: 'instruction' }),
  instructions: [],
  vocabulary: noVocabulary('stage'),
  ...over,
});

const form = (over: Partial<DockerfileForm> = {}): DockerfileForm => ({
  path: '/w/api/Dockerfile',
  missing: false,
  escape_char: '\\',
  crlf: false,
  bom: false,
  directives: [],
  stages: [],
  preamble: [],
  vocabulary: noVocabulary('file'),
  ...over,
});

describe('the stage form’s headings', () => {
  // The file's own name for the stage, never an invented "stage 0".
  it('uses the AS name when the file gives one, and the image when it does not', () => {
    assert.equal(stageHeading(stage()), 'build');
    assert.equal(stageHeading(stage({ name: '', label: 'alpine:3.19', image_ref: 'alpine:3.19' })), 'alpine:3.19');
  });

  it('states what the file says about the stage and nothing more', () => {
    assert.equal(stageDetail(stage()), 'node:18-alpine · as build');
    assert.equal(
      stageDetail(stage({ platform: '--platform=$BUILDPLATFORM' })),
      'node:18-alpine · --platform=$BUILDPLATFORM · as build',
    );
    assert.equal(stageDetail(stage({ name: '' })), 'node:18-alpine');
  });
});

describe('fileMarks', () => {
  // A custom escape character, CRLF and a BOM are each a documented way to
  // corrupt this grammar and each is invisible in the text. Saying them is how
  // the reader knows the tool saw what they have.
  it('says nothing about an ordinary file', () => {
    assert.equal(fileMarks(form()), '');
  });

  it('names every quirk that is present', () => {
    assert.equal(
      fileMarks(form({ escape_char: '`', crlf: true, bom: true })),
      'escape character ` · CRLF line endings · byte order mark',
    );
  });
});

describe('instructionLabel', () => {
  it('labels a comment and a directive as such, and an instruction by name', () => {
    assert.equal(instructionLabel(instruction()), 'RUN');
    assert.equal(instructionLabel(instruction({ kind: 'comment', name: undefined })), 'comment');
    assert.equal(instructionLabel(instruction({ kind: 'directive', name: undefined })), 'directive');
  });
});

describe('the missing Dockerfile marker — story 6.3', () => {
  const empty = { edges: [], dangling: [], cycles: [] };

  // The node renders as missing rather than being omitted. A graph that quietly
  // drops a build whose Dockerfile is not there is the graph lying by omission,
  // and the reader's build fails with the picture still saying everything is
  // fine.
  it('puts a marker on the node, saying the word as well as carrying a colour', () => {
    const markers = markerIndex(empty, ['services.api.build']);
    assert.deepEqual(markers['services.api.build'], ['missing — this file is not on disk']);
  });

  it('marks nothing when nothing is missing', () => {
    assert.deepEqual(markerIndex(empty, []), {});
    assert.deepEqual(markerIndex(empty), {});
  });
});
