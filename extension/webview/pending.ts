// The pending-diff strip — story 6.1.
//
// It is docked at the foot of the panel and it is the only place in this
// product where a write can be started. Two controls, and both say exactly what
// they do: `Save to <file>` names the file, because in a multi-file project
// that is the question the reader actually has, and `Discard` throws the stage
// away without touching anything.
//
// The diff it shows is the core's, verbatim. Nothing here computes what would
// change, formats a hunk, or decides how many lines moved — all of that arrives
// from `stack/preview`, which is the same call `stack/apply` performs before it
// writes. If this file ever grows a diff algorithm, the number on screen and
// the number in the file stop being the same number.
//
// The strip is hidden when nothing is staged, and it never appears on its own.

import type { WebviewMessage } from '../shared/protocol';

/**
 * The one-line summary above the diff.
 *
 * Spelled out rather than badged — `2 edits · 1 line removed, 1 added` — and
 * never `+1/-1`, which is the cryptic-badge anti-pattern the design names. The
 * counts are the core's, and the sentence is the reader's evidence that a
 * one-value change is a one-line change.
 */
export function pendingSummary(count: number, removed: number, added: number): string {
  const edits = count === 1 ? '1 edit' : `${count} edits`;
  const lines = [
    removed === 1 ? '1 line removed' : `${removed} lines removed`,
    added === 1 ? '1 added' : `${added} added`,
  ].join(', ');
  return `${edits} · ${lines}`;
}

/**
 * What the strip says when nothing is staged — DESIGN.md:186.
 *
 * A constant rather than a literal at the one call site, because the wording IS
 * the requirement and a test asserts this exact string reaches the pane.
 */
export const EMPTY_SUMMARY = 'no pending changes';

/**
 * The accessible name for the whole strip, so a screen reader learns there is
 * unwritten work without having to find the diff and read it.
 */
export function pendingLabel(count: number, file: string): string {
  const edits = count === 1 ? '1 staged edit' : `${count} staged edits`;
  return `${edits} against ${file}, not yet written`;
}

/**
 * Which line of a unified diff this is, as a class suffix.
 *
 * The `---` and `+++` headers are NOT additions and removals and must not be
 * coloured as though they were; a reader counting green lines would count two
 * extra on every diff.
 */
export function diffLineKind(line: string): 'meta' | 'hunk' | 'add' | 'del' | 'ctx' {
  if (line.startsWith('--- ') || line.startsWith('+++ ') || line.startsWith('\\ ')) {
    return 'meta';
  }
  if (line.startsWith('@@')) {
    return 'hunk';
  }
  if (line.startsWith('+')) {
    return 'add';
  }
  if (line.startsWith('-')) {
    return 'del';
  }
  return 'ctx';
}

/**
 * Whether a diff line names the FILE rather than showing a change.
 *
 * `--- a/compose.yaml` and `+++ b/compose.yaml` say which file the diff is
 * against — twice — and in this strip that question is already answered, in
 * words, on the control that performs the write: `Save to compose.yaml`, two
 * rows above. Measured on the shipped bundle at 1100x720, they were two of the
 * three lines the box had room for, and the `−`/`+` pair they pushed out is the
 * one thing story 6.1 exists to show.
 *
 * The `@@ -27,7 +27,7 @@` hunk header is deliberately NOT dropped with them: it
 * is the only line carrying WHERE in the file the change lands, and on a diff
 * with two hunks it is the only thing saying the changes are in two places.
 * Nothing else in the strip says that.
 *
 * `\ No newline at end of file` also stays — that is a statement about the
 * bytes, which is this product's whole subject.
 *
 * POSITIONAL, and that is not fussiness. Removing a line that itself begins
 * `--` produces `---   thing`, which `diffLineKind` already cannot tell from a
 * file header — a mis-COLOURED line. Dropping it would be a DELETED line, and a
 * strip that silently omits part of the change is worse than the defect this
 * function exists to fix. In a unified diff the header is the first two lines
 * and content never is, so index settles it.
 */
export function isFileHeaderLine(line: string, index: number): boolean {
  return index < 2 && (line.startsWith('--- ') || line.startsWith('+++ '));
}

/**
 * The lines the strip renders: the core's diff, minus the file-name header.
 *
 * Still verbatim — nothing here computes, reformats or re-orders a line. The
 * only editing is dropping two lines that name a file the strip already names.
 */
export function diffBodyLines(diff: string): string[] {
  return diff
    .split('\n')
    .filter((line, index) => line !== '' && !isFileHeaderLine(line, index));
}

/**
 * Which line to scroll to when the diff is taller than the box.
 *
 * The first `−` or `+`, with one line of context above it so the change is
 * shown in its surroundings rather than flush against the top edge. A diff of
 * pure context has no change to find, so it starts at the top.
 *
 * This is what stops the 1.1 defect returning in a different shape: shrinking
 * the box is a layout accident away, and a scroll box that opens at line 1 of a
 * unified diff shows context, which is by definition the part that did not
 * change.
 */
export function firstChangeLine(lines: string[]): number {
  const at = lines.findIndex((line) => {
    const kind = diffLineKind(line);
    return kind === 'add' || kind === 'del';
  });
  return at < 0 ? 0 : Math.max(0, at - 1);
}

export interface PendingCallbacks {
  send: (msg: WebviewMessage) => void;
}

export class PendingView {
  readonly element: HTMLElement;
  private readonly summary = el('span', 'pending-summary');
  private readonly note = el('p', 'pending-note');
  private readonly body = el('pre', 'pending-diff mono');
  /** The second diff of a two-file move — story 9.3. Empty for everything else. */
  private readonly second = el('div', 'pending-second');
  private readonly save = document.createElement('button');
  private readonly discard = document.createElement('button');

  constructor(private readonly callbacks: PendingCallbacks) {
    this.element = el('section', 'pending');
    this.element.setAttribute('aria-label', 'Pending changes');
    // A region rather than an alert: it is persistent state, not an
    // interruption, and an alert role would have a screen reader announce the
    // whole diff over whatever the reader was doing.
    this.element.setAttribute('role', 'region');

    const header = el('header', 'pending-header');
    const title = el('span', 'pane-title');
    title.textContent = 'Pending';

    this.save.type = 'button';
    this.save.className = 'button button-primary';
    this.discard.type = 'button';
    this.discard.className = 'button';
    this.discard.textContent = 'Discard';
    this.save.addEventListener('click', () => this.callbacks.send({ type: 'save' }));
    this.discard.addEventListener('click', () => this.callbacks.send({ type: 'discard' }));

    const controls = el('div', 'pending-controls');
    controls.append(this.discard, this.save);
    header.append(title, this.summary, controls);

    this.note.hidden = true;
    this.body.setAttribute('aria-label', 'Unified diff of the staged changes');
    this.second.hidden = true;
    this.element.append(header, this.note, this.body, this.second);
    // Never hidden: the empty state is a sentence, not an absence.
    this.clear();
  }

  /**
   * Shows the strip for a staged set.
   *
   * `second` is story 9.3's `.env` half, and it is the reason this signature
   * grew rather than the strip growing a second view: a move is ONE staged
   * thing that changes two files, and showing one of its diffs would be a lie
   * about the half the reader cannot see. Absent for everything else, because
   * nothing else in this product writes a second file.
   */
  render(
    file: string,
    count: number,
    diff: string,
    removed: number,
    added: number,
    saveLabel: string,
    second?: { file: string; diff: string; note: string },
  ): void {
    this.note.hidden = true;
    this.note.textContent = '';
    this.summary.textContent = pendingSummary(count, removed, added);
    this.save.hidden = false;
    this.discard.hidden = false;
    this.element.classList.remove('is-empty');
    this.save.textContent = saveLabel;
    // The button says the filename; the accessible name says what pressing it
    // does, since "Save to compose.yml" alone does not say it writes to disk.
    this.save.setAttribute('aria-label', `${saveLabel} — writes the staged changes to disk`);
    this.element.setAttribute('aria-label', pendingLabel(count, file));
    this.renderDiff(diff);
    this.second.textContent = '';
    if (second) {
      const label = el('div', 'pending-second-label');
      label.textContent = second.note;
      this.second.appendChild(label);
      const body = el('pre', 'pending-diff mono');
      body.setAttribute('aria-label', `Unified diff of ${second.file}`);
      for (const line of diffBodyLines(second.diff)) {
        const row = el('span', `diff-line diff-${diffLineKind(line)}`);
        row.textContent = line;
        body.appendChild(row);
      }
      this.second.appendChild(body);
      this.second.hidden = false;
    } else {
      this.second.hidden = true;
    }
    this.element.hidden = false;
    this.element.classList.remove('is-message');
  }

  /**
   * Empties the strip without hiding it.
   *
   * DESIGN.md:186 — "Empty state is not blank: it reads 'no pending changes',
   * so its absence is legible." It used to set `hidden = true`, so the strip
   * vanished entirely and the reader had no way to tell a pane with nothing
   * staged from a pane whose strip had failed to appear. The words matter for
   * the same reason "not set" matters on a field: absence is content.
   *
   * `reason` is shown instead when the stage was DISCARDED rather than saved —
   * a byte range that moved is the case that matters (AD-19), and a stage that
   * vanished with nothing said is work the reader thinks they still have.
   */
  clear(reason?: string): void {
    this.body.textContent = '';
    this.second.textContent = '';
    this.second.hidden = true;
    this.summary.textContent = EMPTY_SUMMARY;
    this.save.textContent = '';
    this.save.hidden = true;
    this.discard.hidden = true;
    this.element.setAttribute('aria-label', EMPTY_SUMMARY);
    this.element.classList.add('is-empty');
    this.element.hidden = false;
    if (!reason) {
      this.note.textContent = '';
      this.note.hidden = true;
      this.element.classList.remove('is-message');
      return;
    }
    this.note.textContent = reason;
    this.note.hidden = false;
    this.element.classList.add('is-message');
  }

  /** Reports whether the strip is showing anything. */
  get visible(): boolean {
    return !this.element.hidden;
  }

  private renderDiff(diff: string): void {
    this.body.textContent = '';
    const lines = diffBodyLines(diff);
    const rows: HTMLElement[] = [];
    for (const line of lines) {
      const row = el('span', `diff-line diff-${diffLineKind(line)}`);
      row.textContent = line;
      this.body.appendChild(row);
      rows.push(row);
    }
    this.revealChange(rows[firstChangeLine(lines)]);
  }

  /**
   * Scrolls the first changed line to the top of the box, if it is not already
   * showing.
   *
   * A no-op in the common case — the strip now sizes to its content — but the
   * common case is not the only one: a staged rename touching four keys, or a
   * short panel, still produces a diff taller than half the viewport, and then
   * the box opens on the hunk header and the reader scrolls to find the change.
   * Guarded rather than assumed, because this runs under a DOM fake in the unit
   * tests where none of these measurements exist.
   */
  private revealChange(target: HTMLElement | undefined): void {
    if (target === undefined || typeof this.body.scrollHeight !== 'number') {
      return;
    }
    if (this.body.scrollHeight <= this.body.clientHeight) {
      return;
    }
    const box = this.body.getBoundingClientRect?.();
    const row = target.getBoundingClientRect?.();
    if (box === undefined || row === undefined) {
      return;
    }
    this.body.scrollTop += row.top - box.top;
  }
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  return node;
}
