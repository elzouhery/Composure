// The Dockerfile view — story 6.2.
//
// One group per build stage, instructions in order, each editable. No canvas:
// R5.6 says a Dockerfile is a linear list and a graph over it adds nothing. The
// one relation a graph would draw — stage B copying `--from=A` — is already
// written on the COPY line, and drawing it as an arrow would add a layout to
// something that already has one.
//
// Everything here arrives from the core's `stack/dockerfile`. This file parses
// no Dockerfile, joins no continuation and decides no casing; a second parser
// in TypeScript would be a second answer about a grammar whose every quirk is a
// way to corrupt a file.
//
// Two rules carry over from the inspector and are load-bearing:
//
//  * Monospace means "this is literally in your file". Instruction text is in
//    the code face; the sentence explaining why a field cannot be edited is not.
//  * There is no colour in this file at all.

import type {
  DockerInstruction,
  DockerStage,
  DockerVocabularyEntry,
  DockerfileForm,
  ExtractArgResult,
  ImageLookup,
  WebviewMessage,
} from '../shared/protocol';
import { ageNote, stateNote, upgradePill } from './imagepill';
import { diffBodyLines, diffLineKind } from './pending';

/** What the host says a move into a build argument would do — story 9.4. */
export interface ArgMoveAnswer {
  instruction: number;
  staged: boolean;
  result?: ExtractArgResult;
  refused?: string;
}

/** The heading for one stage group. Never "stage 0" when the file names it. */
export function stageHeading(stage: DockerStage): string {
  return stage.name !== '' ? stage.name : stage.image_ref;
}

/**
 * The line under a stage heading, naming what the file says and nothing more.
 */
export function stageDetail(stage: DockerStage): string {
  const parts = [stage.image_ref];
  if (stage.platform) {
    parts.push(stage.platform);
  }
  if (stage.name !== '') {
    parts.push(`as ${stage.name}`);
  }
  return parts.join(' · ');
}

/**
 * What a Dockerfile's marks are worth saying out loud.
 *
 * A custom escape character, CRLF endings and a BOM are each a documented way
 * to corrupt this grammar, and each is invisible in the text. Stating them is
 * how the reader knows the tool saw what they have rather than what it assumed.
 */
export function fileMarks(form: DockerfileForm): string {
  const parts: string[] = [];
  if (form.escape_char !== '\\') {
    parts.push(`escape character ${form.escape_char}`);
  }
  if (form.crlf) {
    parts.push('CRLF line endings');
  }
  if (form.bom) {
    parts.push('byte order mark');
  }
  return parts.join(' · ');
}

/**
 * The instructions that add a layer to the built image.
 *
 * Counting, not parsing: these are the names the CORE already reported for
 * instructions it already found. Nothing here reads a Dockerfile.
 */
export const LAYER_INSTRUCTIONS = ['RUN', 'COPY', 'ADD'];

/**
 * A stage's size, in the two numbers a reader building it actually has.
 *
 * Direction A's Dockerfile pane carries a layer count and this form had none.
 * It is worded as what it is — `7 instructions · 3 add a layer` — rather than
 * as "3 layers", because the number of layers in the built image depends on the
 * builder, and stating a figure we did not measure is the kind of confident
 * wrong answer this product exists not to give.
 */
export function stageSize(stage: DockerStage): string {
  const real = stage.instructions.filter((i) => i.kind === 'instruction');
  const layers = real.filter((i) => LAYER_INSTRUCTIONS.includes((i.name ?? '').toUpperCase()));
  const instructions = real.length === 1 ? '1 instruction' : `${real.length} instructions`;
  return `${instructions} · ${layers.length} add a layer`;
}

/**
 * The pane header's one-line summary of the whole file — the mockup's
 * `2 stages · 6 layers · base image 14 months old` (directions-3.html:572),
 * minus the third clause.
 *
 * **The age arrived in Epic 8** and is the third clause when, and only when,
 * Docker Hub has answered. Until then this comment said it was deferred to
 * phase 6 and that "a header that read `base image 14 months old` because the
 * mockup does would be the confident wrong answer this product exists not to
 * give" — which is still exactly right, and is why `age` is a parameter with no
 * default rather than something this function could ever invent. With no answer
 * the header is the two numbers `stack/dockerfile` alone can support, and the
 * pane is byte-for-byte what shipped before the epic.
 *
 * It is the OLDEST stage's base, not the first stage's: the header is a claim
 * about the file.
 *
 * The layer clause keeps `stageSize`'s wording rather than the mockup's
 * `6 layers`, and for `stageSize`'s reason: how many layers the BUILT image has
 * is the builder's answer, not ours. What we counted is instructions that add
 * one, so that is what it says. The preamble counts too — an `ARG` before the
 * first `FROM` is in the file and belongs to the file's totals.
 */
export function formSummary(form: DockerfileForm, age = ''): string {
  const stages = form.stages.length === 1 ? '1 stage' : `${form.stages.length} stages`;
  const every = form.stages
    .flatMap((s) => s.instructions)
    .concat(form.preamble)
    .filter((i) => i.kind === 'instruction');
  const layers = every.filter((i) => LAYER_INSTRUCTIONS.includes((i.name ?? '').toUpperCase()));
  const adds = layers.length === 1 ? '1 instruction adds a layer' : `${layers.length} instructions add a layer`;
  return age === '' ? `${stages} · ${adds}` : `${stages} · ${adds} · base image ${age}`;
}

/**
 * The oldest stage base among whatever Docker Hub has answered about so far.
 *
 * `age_days` is the ordering key rather than the words, because "14 months old"
 * and "9 days old" do not sort as strings — and a header that reported the
 * NEWEST base as the file's age would be reassuring and wrong.
 *
 * Answers arrive one at a time, so this is computed from what has landed rather
 * than waiting for all of them: a header that said nothing until the last stage
 * answered would say nothing at all whenever one stage is on another registry.
 */
export function oldestBaseAge(lookups: Map<string, ImageLookup>): string {
  let best = 0;
  let words = '';
  for (const lookup of lookups.values()) {
    const days = lookup.age_days ?? 0;
    if (lookup.age && days > best) {
      best = days;
      words = lookup.age;
    }
  }
  return words;
}

/** The word for an instruction row's leading label. */
export function instructionLabel(instruction: DockerInstruction): string {
  if (instruction.kind === 'comment') {
    return 'comment';
  }
  if (instruction.kind === 'directive') {
    return 'directive';
  }
  return instruction.name ?? '';
}

/**
 * The class for that label — and whether it goes in the code face.
 *
 * DESIGN.md's one load-bearing type rule: **monospace means this is literally
 * in your file.** `FROM`, `WORKDIR` and `COPY --from=build` are the file's own
 * bytes; `comment` and `directive` are OUR words for a row the file does not
 * name. The compose pane has held that line since story 5.1 —
 * `field-key-literal mono` on an environment variable's name — and this pane
 * rendered every key, keyword and all, in the UI face at the UI size. Measured:
 * `FROM` at 13px UI against `NODE_ENV` at 12px code, in two panes one click
 * apart, both of them file bytes.
 */
export function instructionLabelClass(instruction: DockerInstruction): string {
  return instruction.kind === 'comment' || instruction.kind === 'directive'
    ? 'field-key'
    : 'field-key field-key-literal mono';
}

export interface StageFormCallbacks {
  send: (msg: WebviewMessage) => void;
}

/**
 * The staging key the HOST records an added instruction under
 * (`host/staging.ts addInstructionKey`), rebuilt here so a row the reader has
 * already staged can say so.
 *
 * Both sides pin the literal in their own tests, which is the same arrangement
 * `stage:` and `instruction:` have had since story 6.2. The alternative — a
 * shared module for one string — would put host staging vocabulary into the
 * webview's import graph for nothing.
 */
function stagedInstructionKey(stage: number, name: string): string {
  return `add-instruction:${stage}:${name.toUpperCase()}`;
}

/**
 * The index space a positional answer is addressed in, as one string.
 *
 * Every instruction the form carries, in file order, as `index:text`. An edit
 * that inserts or removes a line renumbers everything at or after it, so any
 * recorded `{ instruction: n }` stops meaning what it meant — and an edit that
 * rewrites an instruction in place changes what index n IS without moving it.
 * Both show up here; neither shows up in the path.
 *
 * `text` is the instruction's source bytes verbatim, which is the strongest
 * identity available on the wire and the one that changes exactly when the
 * bytes the answer was computed from change.
 */
function fingerprint(form: DockerfileForm): string {
  const parts: string[] = [form.path];
  const add = (list: readonly DockerInstruction[] | undefined): void => {
    for (const i of list ?? []) {
      parts.push(`${i.index}:${i.text ?? ''}`);
    }
  };
  add(form.directives);
  add(form.preamble);
  for (const stage of form.stages ?? []) {
    if (stage.from) {
      parts.push(`${stage.from.index}:${stage.from.text ?? ''}`);
    }
    add(stage.instructions);
  }
  return parts.join('\n');
}

export class StageFormView {
  readonly element: HTMLElement;
  private readonly heading = el('span', 'pane-file mono');
  /** `2 stages · 6 instructions add a layer` — the mockup's header line. */
  private readonly summary = el('span', 'pane-summary');
  private readonly marks = el('span', 'inspector-source');
  private readonly back = document.createElement('button');
  private readonly body = el('div', 'inspector-body stageform-body');
  /** Where the pending strip docks while this view is up. */
  readonly footer = el('div', 'inspector-footer');
  /** The field to refocus after the rebuild a stage triggers. */
  private focusAfterRender: string | null = null;
  /** `+ add stage` — story 7.7, and the mockup's own lower-case wording. */
  private readonly addStage = document.createElement('button');
  /**
   * View state, and only view state: whether the stage composer is open, and
   * which available instruction the reader has opened a field for.
   *
   * Neither stages anything. DECISIONS.md 17's gesture, unchanged from the
   * compose side: clicking a key OPENS it, and Enter is what stages — because
   * at the click the reader has said WHICH instruction they want and not what
   * it should say.
   */
  private addStageOpen = false;
  private openInstruction: string | null = null;
  /**
   * What Docker Hub said about each stage's base image, keyed `stage:0` — the
   * same key the staged edit uses, so a pill and its own edit cannot address
   * different things.
   *
   * It arrives AFTER this form has already rendered and may never arrive. The
   * form is complete without it.
   */
  private lookups = new Map<string, ImageLookup>();
  /**
   * Which rows have the move block open, and what the host said about each —
   * story 9.4, and the same two-map arrangement the inspector's `${}` uses.
   *
   * Both are keyed by the CORE's instruction index, which is the handle every
   * edit in this grammar already takes. View state and nothing else: opening a
   * block stages nothing, and a move that has been staged is dropped through
   * `Discard` with the strip saying so.
   */
  private argMovesOpen = new Set<number>();
  private argMoves = new Map<number, ArgMoveAnswer>();
  /** The last thing rendered, so opening a field can redraw without the host. */
  private last: { form: DockerfileForm; from: string | null; staged: Set<string> } | null = null;

  constructor(private readonly callbacks: StageFormCallbacks) {
    this.element = el('section', 'pane stageform');
    this.element.setAttribute('aria-label', 'Dockerfile');

    const header = el('header', 'pane-header inspector-header');
    const title = el('span', 'pane-title');
    title.textContent = 'Dockerfile';
    this.back.type = 'button';
    this.back.className = 'button';
    this.back.textContent = 'Back to the stack';
    this.back.addEventListener('click', () => this.callbacks.send({ type: 'backToStack' }));
    this.back.hidden = true;

    // The control the design has carried since it was agreed
    // (directions-3.html:573), at last attached to an engine that can do it.
    // Its text IS its accessible name, and it reports whether the composer is
    // open through aria-pressed, the same idiom the toolbar's profile, collapse
    // and focus buttons use.
    this.addStage.type = 'button';
    // A QUIET control at the right of the header, not a filled primary button
    // on a row of its own. The mockup puts `+ add stage` in the pane header's
    // right-hand slot at the header's own size (directions-3.html:572), and a
    // filled button there claims to be the main thing on the pane — which,
    // against reading the Dockerfile, it is not.
    this.addStage.className = 'button button-quiet pane-control';
    this.addStage.textContent = '+ add stage';
    this.addStage.dataset.control = 'add-stage';
    this.addStage.setAttribute('aria-pressed', 'false');
    this.addStage.addEventListener('click', () => {
      this.addStageOpen = !this.addStageOpen;
      this.focusAfterRender = this.addStageOpen ? 'add-stage:image' : null;
      this.rerender();
    });

    // Reading order IS the mockup's header: what the pane is, which file, what
    // is in it — then the controls, pushed to the right edge by `.pane-summary`
    // taking the slack. `.inspector-source` (the escape/CRLF/BOM marks) is last
    // of the text because it is the rarest and, unlike the summary, usually
    // empty; it keeps its own line only when it has something to say.
    header.append(title, this.heading, this.summary, this.addStage, this.back, this.marks);
    this.element.append(header, this.body, this.footer);
  }

  render(form: DockerfileForm, from: string | null, staged: Set<string>): void {
    // A move is an answer about ONE file's bytes, addressed by an index into
    // that file's instructions. Carried across to another Dockerfile it would
    // draw one file's diff under another file's row, with an index that means
    // something else there — and carried across a CHANGE to the same file it
    // does exactly the same thing, because inserting one line shifts every
    // index at or after it by one.
    //
    // Comparing the path alone could only ever catch the first of those. Every
    // save and every external edit arrives through `refill()` at the SAME
    // path, so the block stayed open showing the diff and the scope sentence
    // computed before the change, and its button went on posting an index that
    // now addressed a different instruction (D2). Story 9.3's compose half is
    // immune because it is keyed by config path, which survives an edit; this
    // half is positional, and DECISIONS 24 requires a positional answer to be
    // rebased or dropped.
    //
    // Dropped, not rebased: rebasing would mean guessing which instruction the
    // reader meant after the file moved under them, and a wrong guess here
    // stages an ARG for a value nobody asked about. Re-asking is one press.
    //
    // The fingerprint is the instruction list itself rather than the path,
    // because that IS the index space the answer is addressed in. It is
    // deliberately not a whole-form comparison: an unrelated refill — a Docker
    // Hub lookup landing, a selection change — must not close a block the
    // reader is reading, and those do not move an instruction.
    if (this.last && fingerprint(this.last.form) !== fingerprint(form)) {
      this.argMovesOpen.clear();
      this.argMoves.clear();
    }
    this.last = { form, from, staged };
    this.body.textContent = '';
    this.heading.textContent = shortFile(form.path);
    // A file that is not there has no stages to count, and `0 stages` about a
    // file that does not exist is a statement about the wrong thing.
    this.summary.textContent = form.missing ? '' : formSummary(form, oldestBaseAge(this.lookups));
    this.marks.textContent = fileMarks(form);
    // A file that is not there has no end to append a stage to. The control
    // stays in the header and goes quiet rather than disappearing, so its
    // absence is a state of this file and not a missing feature.
    this.addStage.hidden = form.missing;
    this.addStage.setAttribute('aria-pressed', String(this.addStageOpen && !form.missing));
    // Returning to the compose view is offered only when there is one to
    // return to. A button that goes nowhere is worse than no button.
    this.back.hidden = from === null;

    if (form.missing) {
      // Story 6.3: a build naming a file that is not there renders as missing.
      // It does not vanish, and it does not become a failure banner — the
      // reader asked a question and "that file does not exist" is the answer.
      const p = el('p', 'inspector-message');
      p.textContent = `${form.path} is not there.`;
      const detail = el('p', 'inspector-note');
      detail.textContent =
        form.context || form.dockerfile
          ? `The build declares context ${form.context || '.'} and dockerfile ` +
            `${form.dockerfile || 'Dockerfile'}, which resolves to a file that does not exist. ` +
            'The build will fail; the finding is in the problems panel.'
          : 'The build names a Dockerfile that does not exist. The build will fail.';
      this.body.append(p, detail);
      return;
    }

    for (const d of form.directives) {
      this.body.appendChild(this.readOnlyRow(d));
    }
    if (form.preamble.some((i) => i.kind === 'instruction')) {
      const section = el('section', 'grp');
      section.appendChild(heading('before the first stage'));
      for (const in_ of form.preamble) {
        if (in_.kind === 'instruction') {
          section.appendChild(this.instructionRow(in_, staged));
        }
      }
      this.body.appendChild(section);
    }

    for (const stage of form.stages) {
      this.body.appendChild(this.stage(stage, staged));
    }
    if (form.stages.length === 0) {
      const p = el('p', 'inspector-message');
      p.textContent = 'This file declares no build stages.';
      this.body.appendChild(p);
    }
    if (this.addStageOpen && !form.missing) {
      this.body.appendChild(this.stageComposer());
    }
    this.restoreFocus();
  }

  /**
   * What Docker Hub said about one stage's base image — Epic 8.
   *
   * Redraws from what the host last sent rather than asking for anything: the
   * form was correct and complete before this landed, and one that never lands
   * changes nothing about it.
   */
  setLookup(key: string, lookup: ImageLookup): void {
    this.lookups.set(key, lookup);
    this.rerender();
  }

  /** Forgets every lookup. Called when the form points at a different file. */
  clearLookups(): void {
    this.lookups.clear();
  }

  /**
   * What the host says a move into a build argument would do — story 9.4.
   *
   * Arrives after the block is on screen and never before, like a comment and
   * like an image lookup: it is a round trip the pane does not make until the
   * reader asks for one.
   */
  setExtractArg(answer: ArgMoveAnswer): void {
    this.argMoves.set(answer.instruction, answer);
    this.rerender();
  }

  /** Redraws from what the host last sent. Opening a field is not a re-fetch. */
  private rerender(): void {
    if (this.last) {
      this.render(this.last.form, this.last.from, this.last.staged);
    }
  }

  /**
   * Where a new stage is described — story 7.7.
   *
   * Two fields, because a stage is an image and, optionally, a name; and an
   * empty name means no `AS` clause rather than a name we invented. Enter in
   * either stages, Escape closes, and nothing is written by either: the write
   * is still the one `Save to <file>` press.
   *
   * Both halves of that sentence are now assertions in `stageformdom.test.ts`.
   * They were prose only, and one of them was false: Escape used to restore an
   * already-empty field and blur, leaving the composer open and `+ add stage`
   * still reading `aria-pressed="true"`, so a reader who opened the composer
   * from the keyboard had no key that closed it again.
   */
  private stageComposer(): HTMLElement {
    const wrap = el('div', 'field-block');
    const row = el('div', 'field');
    const key = el('span', 'field-key field-key-literal mono');
    key.textContent = 'FROM';
    row.appendChild(key);

    // Escape backs out of the composer itself, which is what `revert` is for:
    // "what Escape means when there is something to revert TO beyond this
    // input's own text". Focus goes back to the control that opened it, because
    // this pane's body is about to be rebuilt without the field the reader is
    // standing in.
    const close = (): void => {
      this.addStageOpen = false;
      this.focusAfterRender = null;
      this.rerender();
      this.addStage.focus();
    };

    const name = valueInput({
      value: '',
      label: 'the new stage’s AS name, optional',
      fieldKey: 'add-stage:name',
      placeholder: 'name (optional)',
      // Deliberately inert, and not the same thing as "Enter does nothing here"
      // — the Enter listener below submits this field too. What it declines is
      // BLUR: tabbing from the name to the image field would otherwise stage a
      // whole stage on the way past.
      commit: () => {},
      revert: close,
    });
    const submit = (image: string): void => {
      // No image, nothing to add: a `FROM` with no reference is not a stage,
      // and the panel does not ask the core to refuse what it can see.
      if (image.trim() === '') {
        return;
      }
      this.addStageOpen = false;
      this.callbacks.send({ type: 'addStage', image: image.trim(), name: name.value.trim() });
    };
    row.appendChild(
      valueInput({
        value: '',
        label: 'the image the new stage builds from',
        fieldKey: 'add-stage:image',
        placeholder: 'image reference',
        commit: submit,
        revert: close,
      }),
    );
    row.appendChild(name);
    // Enter in the NAME field means the same thing: the reader has finished.
    name.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        const image = this.body.querySelector('[data-field="add-stage:image"]');
        submit(image instanceof HTMLInputElement ? image.value : '');
      }
    });
    wrap.appendChild(row);

    const note = el('div', 'field-note');
    note.textContent =
      'The stage is appended after the last instruction in the file, because a stage’s ' +
      'instructions are the ones that follow its FROM. Nothing is written until you save.';
    wrap.appendChild(note);
    return wrap;
  }

  /** Replaces the body with one line of prose. */
  showMessage(text: string): void {
    this.body.textContent = '';
    const p = el('p', 'inspector-message');
    p.textContent = text;
    this.body.appendChild(p);
  }

  private stage(stage: DockerStage, staged: Set<string>): HTMLElement {
    const section = el('section', 'grp');
    section.appendChild(heading(stageHeading(stage)));

    const detail = el('div', 'stage-detail');
    detail.textContent = `${stageDetail(stage)} · ${stageSize(stage)}`;
    section.appendChild(detail);

    // The base image is the one field on this page anyone came here to change.
    const key = `stage:${stage.index}`;
    const lookup = this.lookups.get(key);
    section.appendChild(
      this.editableRow({
        label: 'FROM',
        value: stage.image_ref,
        fieldKey: key,
        staged: staged.has(key),
        line: stage.from.start_line,
        describe:
          'The image reference alone is replaced. The --platform flag, the AS clause, the ' +
          'keyword casing and any trailing comment stay exactly as written.',
        commit: (value) =>
          this.callbacks.send({ type: 'editStage', stage: stage.index, value }),
        // Epic 8. The mockup's pill, beside the FROM it is about
        // (directions-3.html:576), pressing it STAGES a `set_base_image`
        // through the same message a typed reference uses — never a second
        // write path, and never a write (DECISIONS.md 17).
        pill: lookup
          ? upgradePill(lookup, {
              stage: (reference) => {
                this.focusAfterRender = key;
                this.callbacks.send({ type: 'editStage', stage: stage.index, value: reference });
              },
            })
          : null,
        // …and the line under it, for every state that is not an offer. Silence
        // on `offline` would read as "there is nothing newer".
        note: lookup ? stateNote(lookup) ?? ageLine(lookup) : null,
        // Story 9.4. The FROM's own instruction index, never the stage's: a
        // stage index and an instruction index are different numbers that look
        // alike, and the core takes the second one.
        move: typeof stage.from.index === 'number' ? stage.from.index : null,
      }),
    );

    for (const in_ of stage.instructions) {
      if (in_.kind === 'blank') {
        continue;
      }
      section.appendChild(this.instructionRow(in_, staged));
    }
    this.appendVocabulary(section, stage, staged);
    return section;
  }

  /**
   * `Available here` — story 7.8, and the product's differentiator in the
   * grammar that had no schema to derive one from.
   *
   * EVERY instruction the core offers, always visible, never collapsed behind a
   * "show more" — the same rule as the compose inspector's `available, not set`
   * (story 5.2, DESIGN.md:166). If it does not fit, the pane scrolls.
   *
   * There is no list of Dockerfile instruction names in this file, or anywhere
   * else under extension/. Every name, summary, count and deprecation below
   * arrives from `internal/dockerfile/vocabulary.go`, which the PARSER reads —
   * so a wrong entry fails a parse test rather than quietly shortening a list.
   */
  private appendVocabulary(section: HTMLElement, stage: DockerStage, staged: Set<string>): void {
    const vocabulary = stage.vocabulary;
    if (!vocabulary) {
      return;
    }
    const declared = vocabulary.instructions.filter((e) => e.declared);
    const available = vocabulary.instructions.filter((e) => !e.declared);

    if (declared.length > 0) {
      const block = el('div', 'addable');
      const lead = el('span', 'addable-lead');
      lead.textContent = 'Set here: ';
      block.appendChild(lead);
      for (const [i, entry] of declared.entries()) {
        if (i > 0) {
          const sep = el('span', 'addable-sep');
          sep.textContent = ' · ';
          block.appendChild(sep);
        }
        block.appendChild(this.declaredKey(stage, entry));
      }
      section.appendChild(block);
    }

    if (available.length > 0) {
      const block = el('div', 'addable');
      const lead = el('span', 'addable-lead');
      lead.textContent = 'Available here: ';
      block.appendChild(lead);
      for (const [i, entry] of available.entries()) {
        if (i > 0) {
          const sep = el('span', 'addable-sep');
          sep.textContent = ' · ';
          block.appendChild(sep);
        }
        block.appendChild(this.availableKey(stage, entry, staged));
      }
      section.appendChild(block);
    }

    // Never dropped: an engine that quietly discarded a name it does not know
    // would be telling the reader their file contains only what we understand.
    if (vocabulary.unknown && vocabulary.unknown.length > 0) {
      const line = el('div', 'addable addable-unknown');
      line.textContent = `Not recognised by this engine, and left exactly as written: ${vocabulary.unknown.join(' · ')}`;
      section.appendChild(line);
    }

    // The field for whichever available instruction is open, under the list it
    // was opened from.
    const openName = this.openNameFor(stage.index);
    if (openName !== null) {
      section.appendChild(this.addInstructionRow(stage, openName));
    }
  }

  /** An instruction the stage already uses, with its count and a way to it. */
  private declaredKey(stage: DockerStage, entry: DockerVocabularyEntry): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'addable-key mono';
    button.textContent = `${entry.name} × ${entry.uses}`;
    button.dataset.uses = String(entry.uses);
    button.setAttribute('aria-label', `${entry.name}, used ${entry.uses} times in this stage — go to the first`);
    button.setAttribute('title', entry.summary);
    // Where the uses ARE comes from the core (`indices`); nothing here searches
    // the form for them.
    const first = entry.indices?.[0];
    button.addEventListener('click', () => {
      const key = entry.name === 'FROM' ? `stage:${stage.index}` : `instruction:${first ?? 0}`;
      const target = this.body.querySelector(`[data-field="${cssEscape(key)}"]`);
      if (target instanceof HTMLElement) {
        target.focus();
      }
    });
    return button;
  }

  /** An instruction the stage does NOT use. Clicking opens a field. */
  private availableKey(
    stage: DockerStage,
    entry: DockerVocabularyEntry,
    staged: Set<string>,
  ): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'addable-key mono';
    button.textContent = entry.name;
    button.dataset.add = `${stage.index}:${entry.name}`;
    button.setAttribute('title', entry.summary);
    button.setAttribute('aria-label', `${entry.name} — ${entry.summary}. Add it to this stage`);
    if (entry.deprecated) {
      // Struck through with the reason, never removed: the grammar still
      // accepts it and a file in front of the reader may well use it.
      button.classList.add('is-deprecated');
      const note = el('span', 'addable-mark');
      note.textContent = ` ${entry.deprecated_note ?? 'deprecated'}`;
      button.appendChild(note);
    }
    if (staged.has(stagedInstructionKey(stage.index, entry.name))) {
      button.classList.add('is-staged');
      const note = el('span', 'addable-mark');
      note.textContent = ' staged';
      button.appendChild(note);
    }
    button.addEventListener('click', () => {
      this.openInstruction = `${stage.index}:${entry.name}`;
      this.focusAfterRender = `add:${stage.index}:${entry.name}`;
      this.rerender();
    });
    return button;
  }

  /** The instruction currently open for this stage, if any. */
  private openNameFor(stageIndex: number): string | null {
    if (this.openInstruction === null) {
      return null;
    }
    const [index, name] = this.openInstruction.split(':');
    return Number(index) === stageIndex ? (name ?? null) : null;
  }

  /**
   * The field an available instruction opens into.
   *
   * Nothing is seeded. The vocabulary carries a summary and a flags list and no
   * defaults, so there is nothing honest to put here — and a value the reader
   * did not type is exactly what DECISIONS.md 17 records the cost of.
   */
  private addInstructionRow(stage: DockerStage, name: string): HTMLElement {
    const wrap = el('div', 'field-block');
    const row = el('div', 'field');
    const key = el('span', 'field-key mono');
    key.textContent = name;
    row.appendChild(key);
    row.appendChild(
      valueInput({
        value: '',
        label: `${name}, added to ${stageHeading(stage)}`,
        fieldKey: `add:${stage.index}:${name}`,
        placeholder: 'the rest of the instruction',
        commit: (value) => {
          // Enter with nothing typed writes nothing: a bare HEALTHCHECK with no
          // arguments is not an instruction.
          if (value.trim() === '') {
            return;
          }
          this.openInstruction = null;
          this.callbacks.send({
            type: 'addInstruction',
            stage: stage.index,
            text: `${name} ${value.trim()}`,
          });
        },
        revert: () => {
          this.openInstruction = null;
          this.rerender();
        },
      }),
    );
    wrap.appendChild(row);
    const note = el('div', 'field-note');
    note.textContent = `It is added at the end of this stage. ${
      stage.vocabulary?.instructions.find((e) => e.name === name)?.summary ?? ''
    }`;
    wrap.appendChild(note);
    return wrap;
  }

  private instructionRow(in_: DockerInstruction, staged: Set<string>): HTMLElement {
    if (in_.kind !== 'instruction' || !in_.editable) {
      return this.readOnlyRow(in_);
    }
    const key = `instruction:${in_.index}`;
    return this.editableRow({
      label: instructionLabel(in_),
      labelClass: instructionLabelClass(in_),
      value: in_.args ?? '',
      fieldKey: key,
      staged: staged.has(key),
      line: in_.start_line,
      prefix: (in_.flags ?? []).join(' '),
      describe: '',
      commit: (value) =>
        this.callbacks.send({ type: 'editInstruction', instruction: in_.index, value }),
      // Story 9.4, on the same index the edit above is staged against.
      move: typeof in_.index === 'number' ? in_.index : null,
    });
  }

  /**
   * A row the engine will not rewrite: a comment, a directive, or a multi-line
   * instruction.
   *
   * R7.4 is said HERE rather than discovered when the reader presses Save. A
   * form that offers a field and then refuses it has taught the reader that the
   * tool is unreliable, which is exactly the impression a fidelity engine
   * cannot afford.
   */
  private readOnlyRow(in_: DockerInstruction): HTMLElement {
    const wrap = el('div', 'field-block is-readonly');
    const row = el('div', 'field');
    const key = el('span', instructionLabelClass(in_));
    key.textContent = instructionLabel(in_);
    const value = el('span', 'field-value mono');
    value.textContent = in_.text;
    row.append(key, value);
    wrap.appendChild(row);

    const prov = el('div', 'prov');
    prov.textContent =
      in_.start_line === in_.end_line ? `line ${in_.start_line}` : `lines ${in_.start_line}–${in_.end_line}`;
    wrap.appendChild(prov);

    if (in_.not_editable) {
      const note = el('div', 'field-note');
      note.textContent = in_.not_editable;
      wrap.appendChild(note);
    }
    return wrap;
  }

  private editableRow(spec: {
    label: string;
    /** The label's class. Defaults to the code face — see `instructionLabelClass`. */
    labelClass?: string;
    value: string;
    fieldKey: string;
    staged: boolean;
    line: number;
    prefix?: string;
    describe: string;
    commit: (value: string) => void;
    /** Epic 8's upgrade pill, into the ROW beside the value. Absent by default. */
    pill?: HTMLElement | null;
    /** Epic 8's line under the value: how old it is, or why nothing was found. */
    note?: HTMLElement | null;
    /**
     * The instruction this row's literal would be moved out of — story 9.4.
     *
     * The core's index over all instructions, or null for a row that has none
     * (a fixture, or a form from a core that predates the field). Which of
     * these rows actually HAS a literal to move is the core's answer, not this
     * pane's: the gesture is offered on every editable row and a row with
     * nothing to move comes back refused, in words, at the control that was
     * pressed — the same rule the compose `${}` follows.
     */
    move?: number | null;
  }): HTMLElement {
    const wrap = el('div', 'field-block');
    wrap.dataset.path = spec.fieldKey;
    if (spec.staged) {
      wrap.classList.add('is-staged');
    }

    const row = el('div', 'field');
    const key = el('span', spec.labelClass ?? 'field-key field-key-literal mono');
    key.textContent = spec.label;
    row.appendChild(key);
    const input = valueInput({
      value: spec.value,
      label: `${spec.label} at line ${spec.line}`,
      fieldKey: spec.fieldKey,
      commit: spec.commit,
      onCommit: () => {
        this.focusAfterRender = spec.fieldKey;
      },
    });
    if (spec.prefix) {
      // `.field` is a TWO-column grid. A flags span appended beside the input
      // made three children, so the browser wrapped the input onto an implicit
      // second row and put it back in column one — the key column. Measured on
      // `COPY --from=build /app/dist /usr/share/nginx/html`: `--from=build` sat
      // at the value column and the paths sat at x=10 w=367, inside the key
      // cell, underneath the word `COPY`, while every other row in the file
      // measured x=383 w=707. The reader could not tell which value belonged to
      // which instruction on the one row in the file that carries a flag.
      //
      // The flags and the value are ONE cell: they are one instruction's
      // argument, and the flag is not editable here — the engine rewrites the
      // argument and leaves `--from=build` exactly as written.
      const cell = el('span', 'field-value-cell');
      const flags = el('span', 'field-flags mono');
      flags.textContent = spec.prefix;
      cell.append(flags, input);
      row.appendChild(cell);
    } else {
      row.appendChild(input);
    }
    // Track four of the row's four — `key | value | mark | actions`. One child
    // holding the controls together, never loose buttons: three grid children
    // would overflow the four tracks and wrap onto a line of their own.
    if (typeof spec.move === 'number') {
      const actions = el('span', 'field-actions');
      actions.appendChild(this.argMoveControl(spec.move));
      row.appendChild(actions);
    }
    wrap.appendChild(row);
    // Under the value, indented to the value column — the same placement the
    // compose inspector uses, for the reason recorded beside `.pill-upgrade`
    // in style.css. One control, one behaviour, in both grammars.
    if (spec.pill) {
      wrap.appendChild(spec.pill);
    }

    const prov = el('div', 'prov');
    prov.textContent = `line ${spec.line}`;
    wrap.appendChild(prov);

    if (spec.describe !== '') {
      const note = el('div', 'field-note');
      note.textContent = spec.describe;
      wrap.appendChild(note);
    }
    // Last, because everything above it is about the reader's file and this one
    // is about Docker Hub.
    if (spec.note) {
      wrap.appendChild(spec.note);
    }
    if (spec.staged) {
      const mark = el('div', 'field-note is-staged-note');
      mark.textContent = 'staged, not yet written';
      wrap.appendChild(mark);
    }
    if (typeof spec.move === 'number') {
      this.appendArgMoveBlock(wrap, spec.move);
    }
    return wrap;
  }

  /* ---- Epic 9, story 9.4: moving a literal into a build argument -------- */

  /**
   * The control that offers the move — `${}`, the same mark and the same box
   * the compose pane's gesture takes.
   *
   * `${}` because that is what the instruction will say afterwards, which is
   * the one thing the reader has to recognise; `.row-action` because a control
   * that acts on the row it sits in has ONE shape in this product.
   */
  private argMoveControl(instruction: number): HTMLButtonElement {
    const open = this.argMovesOpen.has(instruction);
    const button = document.createElement('button');
    button.type = 'button';
    button.className = open ? 'row-action extract-open is-open' : 'row-action extract-open';
    button.textContent = '${}';
    button.dataset.instruction = String(instruction);
    button.setAttribute(
      'aria-label',
      open
        ? 'Close the move of this value into a build argument'
        : 'Move this value into a build argument declared where the build can see it',
    );
    button.setAttribute('aria-expanded', open ? 'true' : 'false');
    button.title = button.getAttribute('aria-label') ?? '';
    button.addEventListener('click', () => {
      if (this.argMovesOpen.has(instruction)) {
        this.argMovesOpen.delete(instruction);
        this.callbacks.send({ type: 'closeExtractArg', instruction });
      } else {
        this.argMovesOpen.add(instruction);
        this.callbacks.send({ type: 'openExtractArg', instruction });
      }
      this.rerender();
    });
    return button;
  }

  /**
   * The move, laid out as the decision it is: a name, ONE diff, where the `ARG`
   * landed and why, what compose will and will not do about it, and a press.
   *
   * One diff, because this operation writes one file — the sharpest contrast
   * with story 9.3, whose two diffs travel together for the opposite reason.
   * Nothing here writes: the control at the foot STAGES, and `Save to <file>`
   * is still the only thing in this product that reaches a file.
   */
  private appendArgMoveBlock(wrap: HTMLElement, instruction: number): void {
    if (!this.argMovesOpen.has(instruction)) {
      return;
    }
    const block = el('div', 'extract-block');
    block.dataset.instruction = String(instruction);
    const answer = this.argMoves.get(instruction);
    if (answer === undefined) {
      const note = el('div', 'field-note');
      note.textContent = 'Working out what this would change…';
      block.appendChild(note);
      wrap.appendChild(block);
      return;
    }
    if (answer.refused !== undefined || answer.result === undefined) {
      // No name field, no diff and no button. A refusal is not a fault and it
      // is not a form either — `arg-value`, `arg-conflict` and `no-tag` are all
      // answers, and there is nothing here for the reader to fill in.
      const note = el('div', 'field-note is-readonly');
      note.textContent = answer.refused ?? 'That value cannot be moved into a build argument.';
      block.appendChild(note);
      wrap.appendChild(block);
      return;
    }
    const result = answer.result;

    const nameRow = el('div', 'field-block extract-name');
    const row = el('div', 'field');
    const key = el('span', 'field-key comment-key');
    key.textContent = 'argument name';
    row.appendChild(key);
    row.appendChild(
      valueInput({
        value: result.name,
        label:
          'The name the build argument will take. Press Enter to see what that name would do, ' +
          'Escape to close',
        fieldKey: `extract-arg:${instruction}`,
        // Enter RE-ASKS rather than staging, exactly as the compose half does.
        // The diff underneath is the diff for the name in this field, and where
        // the declaration may legally go can change with it — a name that
        // staged on Enter would stage bytes the reader has never seen.
        commit: (next) => this.callbacks.send({ type: 'openExtractArg', instruction, name: next }),
        revert: () => {
          this.argMovesOpen.delete(instruction);
          this.callbacks.send({ type: 'closeExtractArg', instruction });
          this.rerender();
        },
      }),
    );
    nameRow.appendChild(row);
    block.appendChild(nameRow);

    block.appendChild(
      this.argDiff(`${shortFile(result.dockerfile.file)} — the Dockerfile`, result.dockerfile.diff),
    );

    // Where the declaration landed and why it could not be elsewhere. An `ARG`
    // used before it is declared expands to the empty string with no error, so
    // this is the correctness condition of the operation rather than a detail,
    // and the reason is the CORE's sentence rather than a rewording of it.
    const scope = el('div', 'field-note');
    scope.textContent = scopeNote(result);
    block.appendChild(scope);

    // `build.args` is deliberately not wired (DECISIONS.md 27). Without this
    // the reader is left with an argument nothing supplies.
    const compose = el('div', 'field-note');
    compose.textContent = result.compose_note;
    block.appendChild(compose);

    const controls = el('div', 'field-controls');
    const stage = document.createElement('button');
    stage.type = 'button';
    stage.className = answer.staged ? 'extract-stage is-staged' : 'extract-stage';
    stage.textContent = answer.staged ? 'Staged' : 'Stage this move';
    stage.setAttribute(
      'aria-label',
      answer.staged
        ? `This value is staged as a move into ${result.name}. Nothing is written until you save`
        : `Stage the move of this value into ${result.name}. Nothing is written until you save`,
    );
    stage.addEventListener('click', () => {
      this.callbacks.send({ type: 'stageExtractArg', instruction, name: result.name });
    });
    controls.appendChild(stage);
    block.appendChild(controls);
    wrap.appendChild(block);
  }

  /** The diff, verbatim — nothing here computes what would change. */
  private argDiff(label: string, diff: string): HTMLElement {
    const box = el('div', 'extract-diff-box');
    const head = el('div', 'extract-diff-label');
    head.textContent = label;
    const body = el('pre', 'extract-diff mono');
    body.setAttribute('aria-label', `Unified diff of ${label}`);
    for (const line of diffBodyLines(diff)) {
      const row = el('span', `diff-line diff-${diffLineKind(line)}`);
      row.textContent = line;
      body.appendChild(row);
    }
    box.append(head, body);
    return box;
  }

  private restoreFocus(): void {
    const key = this.focusAfterRender;
    this.focusAfterRender = null;
    if (key === null) {
      return;
    }
    const target = this.body.querySelector(`[data-field="${cssEscape(key)}"]`);
    if (target instanceof HTMLElement) {
      target.focus();
    }
  }
}

/**
 * An editable value.
 *
 * EXPERIENCE.md: editing is immediate — no edit mode, no pencil icon. `Enter`
 * or blur stages the change; `Escape` reverts, which costs nothing because
 * reverting is simply not sending anything and letting the pane refill from the
 * core.
 *
 * Exported because the inspector uses the identical control: one behaviour for
 * "change a value", in both grammars.
 */
export function valueInput(spec: {
  value: string;
  label: string;
  fieldKey: string;
  /** Shown when the field is empty: the schema default, or "not set". */
  placeholder?: string;
  commit: (value: string) => void;
  onCommit?: () => void;
  /**
   * What Escape means when there is something to revert TO beyond this input's
   * own text.
   *
   * Without it, Escape on a staged field restored the STAGED value — which is
   * not a revert, and the accessible name said it was one. A field with a stage
   * behind it passes a callback that discards the stage; a field without one
   * simply restores the text it was rendered with.
   */
  revert?: () => void;
  /**
   * A keydown pre-hook, run BEFORE this control's own Enter / Escape — story
   * 7.9's combobox (`webview/combo.ts`).
   *
   * Returning `true` means the key belonged to something layered over the
   * field and the field must not act on it: Enter with the value list open
   * CHOOSES a value and stages nothing, Escape with it open closes the list and
   * reverts nothing. With the list closed the hook returns `false` for both and
   * this control behaves exactly as it has since story 6.1 — which is why the
   * combobox could be added without a second implementation of staging.
   *
   * A hook rather than a listener the caller adds afterwards, because listeners
   * fire in registration order: a handler added after this one cannot stop this
   * one from staging.
   */
  intercept?: (e: KeyboardEvent) => boolean;
}): HTMLInputElement {
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'field-value field-input mono';
  input.value = spec.value;
  if (spec.placeholder !== undefined) {
    input.placeholder = spec.placeholder;
  }
  input.dataset.field = spec.fieldKey;
  input.setAttribute('aria-label', spec.label);
  // The original, so Escape and an unchanged blur both cost nothing.
  const original = spec.value;
  let reverted = false;

  const commit = (): void => {
    const next = input.value;
    if (reverted || next === original) {
      return;
    }
    spec.onCommit?.();
    spec.commit(next);
  };

  input.addEventListener('keydown', (e) => {
    if (spec.intercept?.(e) === true) {
      return;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      commit();
      return;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      input.value = original;
      if (spec.revert) {
        // The blur this causes must not then commit the text just put back.
        reverted = true;
        spec.revert();
        return;
      }
      input.blur();
    }
  });
  input.addEventListener('blur', commit);
  // Single click focuses and selects the value — no edit mode to enter.
  input.addEventListener('focus', () => input.select());
  return input;
}

/**
 * Which scope the declaration landed in, and the core's own reason why it could
 * not be anywhere else — story 9.4.
 *
 * The lead-in names the scope and says which of the three outcomes this is,
 * because `declared`, `redeclared` and `already_declared` are three booleans and
 * a reader about to press a button needs a sentence. `scope_reason` then
 * follows VERBATIM: it names the line the declaration went above and the
 * consequence of it being anywhere else, and a reworded copy here would be a
 * second account of a rule this pane does not implement.
 */
export function scopeNote(result: ExtractArgResult): string {
  const where =
    result.scope === 'global' ? 'the global scope, before the first FROM' : result.scope;
  if (result.already_declared) {
    return (
      `${result.name} is already declared in ${where} with this value, so nothing is declared ` +
      `again and only the instruction changes. ${result.scope_reason}`
    );
  }
  if (result.redeclared) {
    return (
      `A bare re-declaration — \`${result.arg_line ?? `ARG ${result.name}`}\`, with no default — ` +
      `goes into ${where}. ${result.scope_reason}`
    );
  }
  return `\`${result.arg_line ?? ''}\` goes into ${where}. ${result.scope_reason}`;
}

/** The file, named the way a reader names it: the last two segments. */
export function shortFile(file: string): string {
  const parts = file.replace(/\\/g, '/').split('/');
  return parts.slice(-2).join('/');
}

/**
 * The age line for a stage whose base IS current — the one state with no
 * sentence of its own worth repeating and an age worth stating.
 */
function ageLine(lookup: ImageLookup): HTMLElement | null {
  const text = ageNote(lookup);
  if (text === '') {
    return null;
  }
  const line = el('div', 'field-note image-note');
  line.dataset.imageState = lookup.state;
  line.textContent = text;
  return line;
}

function heading(label: string): HTMLElement {
  const head = el('div', 'grp-t');
  const name = el('span', 'grp-name');
  name.textContent = label;
  head.appendChild(name);
  return head;
}

function cssEscape(value: string): string {
  return value.replace(/["\\]/g, '\\$&');
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  return node;
}
