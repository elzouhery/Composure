// The write path's client side.
//
// This module knows how to ask the core for a diff and how to ask it to write,
// and it knows nothing else. In particular it does not know how to compute a
// diff, splice a byte, or decide whether an edit is safe — all three of those
// live in Go, behind `stack/preview` and `stack/apply`, because a second
// implementation of any of them in TypeScript would be a second answer and the
// two would disagree in front of the reader.
//
// It imports nothing from `vscode`, so the tests can drive it against a stub
// process exactly the way host/core.ts is driven.

import { RpcError } from './core';
import type {
  AddKind,
  Availability,
  EditExpect,
  EditExpectVar,
  EditOp,
  EditOpResult,
  EditResult,
  ExtractArgResult,
  ExtractResult,
} from '../shared/protocol';

/** JSON-RPC codes the core uses for the write path. They mirror serve.go. */
export const CODE_EDIT_REFUSED = -32002;
export const CODE_STALE_RANGE = -32003;
export const CODE_EDIT_FAILED = -32004;

/**
 * The stable reason slugs the core attaches to a refused edit.
 *
 * This union has to hold every slug `edit.Reason` can return
 * (`internal/edit/edit.go`). A slug with no arm below falls through to the
 * default and the reader is shown the engine's own sentence instead of prose —
 * which is the failure story 6.5 is about, one layer up.
 */
export const EDIT_REASONS = [
  'stale-range',
  'flow-style',
  'no-root-mapping',
  'not-a-sequence',
  'null-entry',
  /* Story 6.5 — the parser's position and the file's bytes disagree. */
  'position-mismatch',
  'multi-line',
  'no-insertion-point',
  'insert-text',
  'stage-name',
  'would-corrupt',
  'no-change',
  /* Stories 7.3 and 7.4 — the four ways a declaration is declined. */
  'duplicate-name',
  'needs-quoting',
  'no-image',
  'no-name',
  /* Epic 7's review — four ways new content is declined before it is written. */
  'entry-text',
  'name-too-long',
  'wrong-grammar',
  'mixed-grammar',
  /* The value is not written where it is read. Four shapes, and until they
     were named the first produced `path ... not found` in front of a reader
     who had just typed in the field, and the other three wrote a confident
     wrong answer. See internal/edit/inherited.go. */
  'inherited',
  'inherited-nested',
  'alias',
  'anchor',
  'block-scalar',
  /* Epic 9 — one entry of a list, a comment, and moving a value into .env.
     The core owns the specifics: how many entries the list has, which line
     of .env already holds the name. These arms say what happened; the
     core's own sentence says where. */
  'entry-index',
  'no-comment',
  'comment-text',
  'comment-target',
  'var-name',
  'var-conflict',
  'var-value',
  'already-interpolated',
  'no-literal',
  /* Story 9.4 — the Dockerfile half of moving a value out. An ARG default is
     read by BuildKit and then by a shell, and there is no second reader to
     check a quoting guess against the way ParseDotEnv checks a .env line — so
     these three refuse rather than guess. */
  'arg-value',
  'arg-conflict',
  'no-tag',
] as const;

export type EditReason = (typeof EDIT_REASONS)[number];

export interface EditFailureData {
  file?: string;
  reason?: EditReason;
  written?: boolean;
}

/**
 * What went wrong with an edit, in the terms the caller has to branch on.
 *
 * Three outcomes, three different things to do:
 *
 *   refused  the engine declined to do it safely. Revert the field, say what
 *            could not be done and why, keep everything else staged.
 *   stale    the byte range moved. DISCARD the stage — all of it — and say so.
 *   failed   something else. A fault, shown as one.
 *
 * Collapsing these into one would either lose the reader's other staged work or
 * write over someone else's change, and both are unrecoverable in the way this
 * product cannot afford.
 */
export type EditOutcome = 'refused' | 'stale' | 'failed';

export function classify(err: unknown): EditOutcome {
  if (err instanceof RpcError) {
    if (err.code === CODE_STALE_RANGE) {
      return 'stale';
    }
    if (err.code === CODE_EDIT_REFUSED) {
      return 'refused';
    }
  }
  return 'failed';
}

/** The reason slug, when the core sent one. */
export function reasonOf(err: unknown): EditReason | undefined {
  if (err instanceof RpcError) {
    return (err.data as EditFailureData | undefined)?.reason;
  }
  return undefined;
}

/**
 * A sentence for the reader, naming what could not be done and why.
 *
 * EXPERIENCE.md's voice: no apologies, no exclamation, and never "invalid
 * configuration". The reader is told the shape of the obstacle, because that is
 * what tells them whether to work around it or to go and look at the file.
 */
export function refusalDetail(err: unknown): string {
  const message = err instanceof Error ? err.message : String(err);
  switch (reasonOf(err)) {
    case 'flow-style':
      return (
        'That key is written in flow style — `web: {image: nginx}` — and a block key ' +
        'cannot be added inside one without producing YAML that will not parse. ' +
        'Rewrite the mapping across lines and Composure can add it.'
      );
    case 'no-root-mapping':
      return (
        'That file has no top-level mapping to add a key to — it is empty, or a list, ' +
        'or nothing but comments. Composure adds what you name to a place you chose; it ' +
        'does not invent the shape of a compose file around it. Write one key in the ' +
        'file and Composure can add the rest.'
      );
    case 'not-a-sequence':
      return (
        'That key does not hold a list, so there is no entry to append to it. ' +
        'Check what the file says there — a value and a list of one look alike in a ' +
        'panel and are different documents.'
      );
    case 'null-entry':
      return (
        'That would write a list entry with nothing in it, which is a null the next ' +
        'reader has to guess at. Type the value and it goes in with it.'
      );
    // Story 6.5. Deliberately NOT "the parser reports a rune column and the
    // offset is in bytes", true though that is of the commonest producer: the
    // same refusal is raised when the line numbering itself disagrees, and a
    // sentence about accents would be wrong there. What holds for both is that
    // the line is counted two different ways, so the engine cannot say which
    // bytes belong to this value — and declines rather than guess.
    case 'position-mismatch':
      return (
        'That value shares its line with text Composure and the file count differently, so ' +
        'there is no way to be sure which characters on the line belong to it. Nothing ' +
        'was written — a near-miss here overwrites the value next to it. Put the value ' +
        'on a line of its own and Composure can edit it.'
      );
    case 'no-insertion-point':
      return (
        'There is nowhere in that file it is safe to put the line. A Dockerfile with ' +
        'no stage has nothing to add to, and a heredoc that is never closed swallows ' +
        'everything after it — so the end of the stage is inside a shell script. ' +
        'Nothing was written.'
      );
    // These two carry the specific fact — which character, which line already
    // declares the name — in the core's own sentence, and the reader needs it
    // to act. The prose says what to do about it and the message says what it
    // was.
    case 'insert-text':
      return (
        `${message}. Deciding where your lines break is not this tool’s call — ` +
        'write it as a single line and Composure will add it exactly as you typed it.'
      );
    case 'stage-name':
      return (
        `${message}. Two stages with one name is a build that resolves to whichever ` +
        'one the builder picks, so nothing was written. Pick another name and the ' +
        'stage goes in.'
      );
    case 'multi-line':
      return (
        'That instruction spans several lines. Rewriting it means deciding where to ' +
        'break the lines, which is a formatting policy Composure will not impose on your ' +
        'file. Edit it in the text editor instead.'
      );
    case 'no-change':
      return 'That is what the file already says, so there is nothing to write.';
    // These two carry the specific fact in the core's own sentence — which file
    // and line already declares the name, which character will not survive — and
    // the reader needs it to act, so the message leads and the prose says what
    // to do about it.
    case 'duplicate-name':
      return (
        `${message}. Two things with one name is a file where the last one wins and ` +
        'the other disappears without a word, so nothing was staged. Pick another ' +
        'name and it goes in.'
      );
    case 'needs-quoting':
      return (
        `${message}. Composure writes what you type and nothing else — deciding that you ` +
        'meant the text rather than the number is not its call.'
      );
    case 'no-image':
      return (
        'A service with no image is a name with nothing under it, which is not a stack ' +
        'anything can run. Give it an image and the two go in together, as one edit.'
      );
    case 'no-name':
      return 'Nothing was named, so there is nothing to declare. Type a name and it goes in.';
    case 'arg-value':
      return (
        'That value cannot be a bare ARG default. A build argument\u2019s default is read by ' +
        'BuildKit and then by a shell, and Composure will not guess the quoting on your behalf. ' +
        'Declare the ARG yourself and reference it.'
      );
    case 'arg-conflict':
      return (
        `${message}. A second declaration would shadow the first and the build would use ` +
        'whichever came last, so nothing was written. Choose another name, or settle the two by hand.'
      );
    case 'no-tag':
      return (
        'That FROM has no tag to move \u2014 it is pinned by digest, or carries no tag at all. ' +
        'A digest is not a tag, and an absent tag is :latest by implication rather than ' +
        'something anybody wrote.'
      );
    case 'entry-index':
      return `That list has no entry in that position, so nothing was written. ${message}`;
    case 'no-comment':
      return 'There is no comment there to remove. Nothing was written — this is not an edit that did nothing, it is a comment that was never there.';
    case 'comment-text':
      return 'That would not go in as one comment, so nothing was written. A trailing comment has to fit on the line it trails.';
    case 'comment-target':
      return `That line cannot carry a comment after it without changing what it means, so nothing was written. ${message}`;
    case 'var-name':
      return 'Compose only interpolates a name that starts with a letter or an underscore and continues with letters, digits or underscores. Nothing was written.';
    case 'var-conflict':
      return `The .env file already gives that name a different value, and overwriting it would change every other place that reads it. Nothing was written. ${message}`;
    case 'var-value':
      return 'That value cannot be written as one .env line that reads back as itself, so nothing was written.';
    case 'already-interpolated':
      return 'That value already comes from a variable, so there is no literal here to move.';
    case 'no-literal':
      return 'There is no literal value at that path to move into a variable.';
    case 'entry-text':
      return 'That would not go into the list as one entry, so nothing was written. Everything after the first line break would land in the mapping around it.';
    case 'name-too-long':
      return 'YAML stops reading a key after 1024 characters, so a file written with that name could not be read back. Quoting does not lift the limit — a shorter name will go in.';
    case 'wrong-grammar':
      return `That edit belongs to the other kind of file, so nothing was written. ${message}`;
    case 'mixed-grammar':
      return 'That change mixes compose edits and Dockerfile edits in one write, and the two are different grammars. Nothing was written — make them separately.';
    /* The five ways a value is not written where it is read. Each leads with
       what happened and then carries the CORE's own sentence, which names the
       anchor, the line and the consequence — the same sentence the pane
       already printed under the field. Rewording that half here would give the
       reader two different accounts of one refusal, and this half exists
       because a banner has to say what was refused before it explains. */
    case 'inherited':
      return `That value is not written on this service, so there was nothing to change. ${message}`;
    case 'inherited-nested':
      return `That key is inside something this service inherits whole, so writing it here would drop the rest of it. Nothing was written. ${message}`;
    case 'alias':
      return `That line holds a reference to an anchor rather than a value. Nothing was written. ${message}`;
    case 'anchor':
      return `That value carries an anchor other places in the file reference. Nothing was written. ${message}`;
    case 'block-scalar':
      return `That is a block scalar, and Composure does not rewrite those yet. Nothing was written. ${message}`;
    case 'stale-range':
      return message;
    case 'would-corrupt':
      return `The result would not parse, so nothing was written. ${message}`;
    default:
      return message;
  }
}

/**
 * A comment's text, out of the bytes the engine reports for it — story 9.1.
 *
 * The engine is the one that decides WHICH bytes are the comment: the run above
 * a key is `attachedCommentStart`, the same function `DeleteKey` has always
 * used, and the trailing one is found by scanning forward from the end of the
 * value that `scalarSpan` reports. Neither is re-derived here. What this does
 * is take the answer — `    # one\n    # two\n` — and show it to a reader as
 * two lines they can edit, which is presentation and nothing else.
 *
 * The inverse is the engine's too: `set_comment` writes the indent and the
 * marker, and it writes exactly ONE `#` even when the reader typed one. So
 * this strips a marker and it never has to put one back.
 *
 * Exactly one space after the marker is eaten. `#   three spaces` is the
 * reader's own layout inside their own sentence and normalising it would be
 * the formatting opinion R7.4 refuses in the other grammar.
 *
 * A marker the reader DOUBLED is kept. `## section` is a comment style real
 * files use, and it is not the marker the engine writes back — strip one of the
 * two here and `commentBody` writes one there, and `## mine` comes back as
 * `# mine`. The two halves are one rule, written down in both places
 * (internal/strategy/comment.go `commentBody`).
 *
 * The split is on `\r?\n`, and that is the whole of D1. The bytes come out of
 * the engine with the FILE's endings in them, so every line of a run in a CRLF
 * file used to keep its `\r` — and `commentBody` refuses any text carrying one
 * (ErrCommentText). Opening a comment on a CRLF file, changing nothing and
 * pressing Enter was therefore REFUSED, with a sentence about trailing comments
 * shown for an above block, and the reader saw literal CRs in the textarea. A
 * line ending is the file's to choose and is never the comment's text.
 */
export function commentText(before: string): string {
  if (before === '') {
    return '';
  }
  const lines = before.split(/\r?\n/);
  // A run ends with a newline, which splits into a trailing empty element. A
  // trailing comment has none. Dropping it unconditionally would eat the last
  // line of a comment whose text ends in a blank one, so it is dropped only
  // when it is the artefact of the split.
  if (lines.length > 1 && lines[lines.length - 1] === '') {
    lines.pop();
  }
  return lines
    .map((line) => {
      const hash = line.indexOf('#');
      if (hash < 0) {
        return line.trim();
      }
      const rest = line.slice(hash + 1);
      if (rest.startsWith('#')) {
        // The reader's own doubled marker, kept whole so it can go back in.
        return line.slice(hash);
      }
      return rest.startsWith(' ') ? rest.slice(1) : rest;
    })
    .join('\n');
}

/** The message shown when a stage is discarded because the file moved (AD-19). */
export const STALE_MESSAGE =
  'The file changed on disk, so the staged edits were held against byte ranges that ' +
  'have moved. They have been discarded rather than rebased — writing a stale range ' +
  'is how an editor damages a file. Nothing was written; make the change again.';

/** Anything that can send a JSON-RPC request. Narrowed so the tests can stub it. */
export interface Requester {
  request<T>(method: string, params: unknown): Promise<T>;
}

/**
 * Asks the core where each of these values is actually WRITTEN — story 5.3's
 * provenance, asked of the write path rather than the read one.
 *
 * One request for the whole pane rather than one per field: the answers are
 * independent, so batching is a transport decision, and ninety round trips to
 * draw one service is not one.
 *
 * The classification is never re-derived in TypeScript. A second answer here
 * would disagree with the engine the first time a file used a merge key, and
 * the disagreement would surface as a field the pane offered and the engine
 * then refused — which is the defect this call exists to close.
 */
export function editable(
  session: Requester,
  file: string,
  paths: string[],
): Promise<{ file: string; fields: Availability[] }> {
  return session.request<{ file: string; fields: Availability[] }>('stack/editable', {
    file,
    paths,
  });
}

/** Asks the core what an edit would do. Writes nothing, ever. */
export function preview(session: Requester, file: string, ops: EditOp[]): Promise<EditResult> {
  return session.request<EditResult>('stack/preview', { file, ops });
}

/**
 * Performs the edit.
 *
 * The only function in this extension that reaches a filesystem, and it does so
 * by asking the core to. It is called from exactly one place: the `save`
 * message, which is sent by exactly one control.
 */
export function apply(session: Requester, file: string, ops: EditOp[]): Promise<EditResult> {
  return session.request<EditResult>('stack/apply', { file, ops });
}

/**
 * Asks the core what moving a value into a variable WOULD do — story 9.3.
 *
 * Writes neither file. Both diffs come back, always, because the operation
 * changes two files and a preview that showed one would be a lie about the half
 * the reader cannot see (DECISIONS.md 25).
 */
export function extractPreview(
  session: Requester,
  file: string,
  at: string,
  name?: string,
): Promise<ExtractResult> {
  const params: Record<string, string> = { file, at };
  if (name !== undefined && name !== '') {
    params.name = name;
  }
  return session.request<ExtractResult>('stack/extract', params);
}

/**
 * Performs the move.
 *
 * The SECOND function in this extension that reaches a filesystem, and the
 * reason it exists separately from `apply` is that no sequence of `EditOp`s can
 * express it: `stack/apply` writes one file, and the whole point of this
 * operation is that it writes two, in an order chosen so that the only
 * reachable partial state is an inert `.env` entry nothing references.
 *
 * Called from exactly one place: the `save` message, which is sent by exactly
 * one control — the same control, saying the names of both files.
 */
export function extractApply(
  session: Requester,
  file: string,
  at: string,
  name?: string,
  expect?: EditExpect,
  expectEnv?: EditExpectVar,
): Promise<ExtractResult> {
  const params: Record<string, unknown> = { file, at };
  if (name !== undefined && name !== '') {
    params.name = name;
  }
  // Story 9.6, and the reason PROTOCOL_REVISION is 9. Both are recorded from
  // the preview the reader approved and sent back unchanged: without them the
  // core applies against whatever the files say at write time, which on THIS
  // method means silently overwriting a colleague's `.env` — the one write in
  // this product whose blast radius is larger than the file on screen, and the
  // only one that had no staleness protection at all.
  //
  // Both stay OPTIONAL here because they are optional on the wire: a preview
  // that reported no operation, or a core older than `env_expect`, still
  // applies exactly as before rather than failing at the gesture.
  if (expect !== undefined) {
    params.expect = expect;
  }
  if (expectEnv !== undefined) {
    params.expect_env = expectEnv;
  }
  return session.request<ExtractResult>('stack/extract-apply', params);
}

/**
 * The assertion to send back with an apply, recorded from the preview.
 *
 * The SUBSTITUTION's range, which is the first operation with a non-empty one.
 * An insertion's range is empty by definition — `stack/extract-arg` emits the
 * `ARG` declaration as `insert_instruction_before` at `[0, 0)` — and asserting
 * that an empty range still holds the empty string is true of every file that
 * has ever existed. A check that cannot fail is worse than no check, because it
 * reports protection that is not there.
 *
 * `undefined` when there is nothing assertable, which the caller passes
 * straight through: no assertion is the documented pre-9.6 behaviour, and it is
 * honest. A fabricated one is not.
 */
export function expectOf(result: { ops?: EditOpResult[] } | undefined): EditExpect | undefined {
  const op = (result?.ops ?? []).find((o) => o.range && o.range.end > o.range.start);
  if (op === undefined) {
    return undefined;
  }
  return { start: op.range.start, end: op.range.end, text: op.before };
}

/**
 * Asks the core what moving a Dockerfile literal into a build argument WOULD
 * do — story 9.4. Writes nothing.
 *
 * A separate method from `extractPreview` rather than a mode of it, exactly as
 * it is a separate method on the wire: this one writes ONE file and reports a
 * SCOPE, and a shape whose fields meant different things depending on what was
 * sent is a shape no client can render honestly.
 *
 * `part` is not sent and there is nothing missing. For a `FROM` there is one
 * answer — the tag — and for everything else the body must be a single
 * `KEY=value` and the core ignores it. A flag this extension chose would be a
 * second opinion about a grammar it does not parse.
 */
export function extractArgPreview(
  session: Requester,
  file: string,
  instruction: number,
  name?: string,
): Promise<ExtractArgResult> {
  const params: Record<string, string | number> = { file, instruction };
  if (name !== undefined && name !== '') {
    params.name = name;
  }
  return session.request<ExtractArgResult>('stack/extract-arg', params);
}

/**
 * Performs the move.
 *
 * The THIRD function in this extension that reaches a filesystem. It writes one
 * file and could not be `stack/apply`: the substitution and the declaration are
 * decided together — where the `ARG` may legally go depends on which
 * instruction the literal came out of — and a client assembling those two
 * operations itself would be deciding the placement rule, which is the
 * correctness condition of the whole story.
 *
 * Called from exactly one place: the `save` message, sent by exactly one
 * control.
 */
export function extractArgApply(
  session: Requester,
  file: string,
  instruction: number,
  name?: string,
  expect?: EditExpect,
): Promise<ExtractArgResult> {
  const params: Record<string, unknown> = { file, instruction };
  if (name !== undefined && name !== '') {
    params.name = name;
  }
  // `ExtractArg.Expect` has been plumbed through the core since story 9.4, and
  // its own doc says sending nothing is "wrong for a staged UI edit (AD-19)".
  // Sending it is what makes `panel.ts`'s stale branch on this path reachable:
  // without it the core can only ever apply against the file as it stands, so
  // the branch was code that could not run.
  if (expect !== undefined) {
    params.expect = expect;
  }
  return session.request<ExtractArgResult>('stack/extract-arg-apply', params);
}

/**
 * The sentence that says why a move cannot be staged beside ordinary edits.
 *
 * Not a core refusal — the core is never asked. An extract computes its byte
 * ranges from the file ON DISK, and every ordinary stage holds an `expect`
 * against those same bytes; whichever was written first would move the other,
 * and AD-19 would then discard it. Refusing before either exists is the same
 * answer arrived at one step earlier, and it costs the reader nothing they
 * cannot get by saving or discarding first.
 */
export const EXTRACT_ALONE =
  'Moving a value into a variable changes two files at once, so it is staged on its own. ' +
  'Save or discard what is already staged and the move can go in by itself — ' +
  'nothing has been written either way.';

/**
 * Asks the core how to declare something — stories 7.3 and 7.4.
 *
 * It returns OPERATIONS, and writes nothing. The declaration then travels the
 * ordinary staging path: previewed with whatever else the reader has staged,
 * held until `Save to <file>`, written as one edit with one undo. A method that
 * wrote here would be a second write path and the reader would have two undos
 * for one action.
 *
 * `path` is the project — the name is checked against the whole merged
 * configuration, because a service declared in an override collides with one
 * added to the base and last-one-wins would discard one of them silently.
 * `file` is what gets written.
 */
export function planAdd(
  session: Requester,
  path: string,
  file: string,
  kind: AddKind,
  name: string,
  value: string,
): Promise<{ ops: EditOp[] }> {
  return session.request<{ ops: EditOp[] }>('stack/add', { path, file, kind, name, value });
}
