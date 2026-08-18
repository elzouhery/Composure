// The upgrade pill, and the sentence that stands in for it — Epic 8.
//
// The mockup has carried this since the design was agreed:
//
//   FROM  node:18-alpine  [ node:22-alpine · minor · 40MB smaller ]
//
// (`ux-designs/ux-Composure-2026-08-11/mockups/directions-3.html:576`.)
//
// ONE MODULE FOR BOTH GRAMMARS. The compose inspector and the Dockerfile stage
// form show the same fact about the same kind of value, and two renderers would
// be two answers — the way the finding pill and the value list are each one
// component used twice.
//
// THE PILL IS A BUTTON AND PRESSING IT STAGES. Not writes: DECISIONS.md 17 and
// AD-19 are unchanged by this epic. The reference goes into the field through
// the ordinary edit path, a pending diff appears, `Save to <file>` is still the
// only control in this product that touches a file, and the stage is
// discardable like every other. What the pill removes is the typing, not the
// decision.
//
// The pill's TEXT is not composed here. It arrives as `lookup.pill`, built by
// `internal/hub`, so the CLI table, the JSON and this button cannot word one
// upgrade three ways.

import type { ImageLookup, ImageState } from '../shared/protocol';

/**
 * How a state is described where the pill would have gone.
 *
 * `ok` has no entry: it gets the pill. Everything else gets a quiet line, and
 * every one of them is worth a line — silence on `offline` or `rate-limited`
 * reads as "there is nothing newer", which is the confident wrong answer in
 * exactly the place a reader is deciding whether to upgrade.
 */
export function stateWord(state: ImageState): string {
  switch (state) {
    case 'current':
      return 'up to date';
    case 'offline':
      return 'Docker Hub unreachable';
    case 'rate-limited':
      return 'Docker Hub is busy';
    case 'not-found':
      return 'not on Docker Hub';
    case 'other-registry':
      return 'another registry';
    case 'not-comparable':
      return 'no tag to compare';
    case 'disabled':
      return 'lookup off';
    default:
      return '';
  }
}

/**
 * The pill's accessible name.
 *
 * It says what pressing it DOES, in full, because the visible text is three
 * facts joined by dots and a reader who cannot see it would otherwise be told
 * only that there is a newer tag — not that this button is what puts it in
 * their file, nor that doing so writes nothing yet.
 */
export function pillName(lookup: ImageLookup): string {
  const c = lookup.candidate;
  if (!c) {
    return '';
  }
  // Whether it is bigger or smaller, never by how much: the number is already
  // on the pill's face, and repeating it in the name means a screen reader
  // hears the same megabytes twice.
  const size =
    c.has_size && typeof c.size_delta === 'number' && c.size_delta !== 0
      ? `, and ${c.size_delta < 0 ? 'smaller' : 'larger'}`
      : '';
  return (
    `Upgrade ${lookup.reference} to ${c.reference} — a ${c.kind} version change${size}. ` +
    'Choosing it stages the change; nothing is written until you save.'
  );
}

/** The line under a value when there is no pill: how old it is, and the state. */
export function noteText(lookup: ImageLookup): string {
  const parts: string[] = [];
  if (lookup.age) {
    parts.push(`This tag is ${lookup.age}.`);
  }
  if (lookup.message) {
    parts.push(lookup.message);
  }
  return parts.join(' ');
}

/** The line under a value when there IS a pill: the age, which the pill omits. */
export function ageNote(lookup: ImageLookup): string {
  return lookup.age ? `This tag is ${lookup.age}. Docker Hub, not your files.` : '';
}

export interface PillCallbacks {
  /** Stage the candidate's reference against whatever this pill belongs to. */
  stage(reference: string): void;
}

/**
 * The pill itself, or null when there is nothing to offer.
 *
 * Null rather than a disabled button: a control that cannot be pressed still
 * occupies the row and still reads as a control, and there is nothing here for
 * a reader to do about `postgres` being current.
 */
export function upgradePill(lookup: ImageLookup, callbacks: PillCallbacks): HTMLElement | null {
  const candidate = lookup.candidate;
  if (lookup.state !== 'ok' || !candidate || !lookup.pill) {
    return null;
  }
  const button = document.createElement('button');
  button.type = 'button';
  // `warn` is the mockup's own class on this pill. The colour is a
  // `--vscode-*` token in style.css; there is no colour literal here or there.
  button.className = 'pill pill-upgrade';
  button.dataset.upgrade = lookup.reference;
  button.textContent = lookup.pill;
  button.setAttribute('aria-label', pillName(lookup));
  button.addEventListener('click', () => callbacks.stage(candidate.reference));
  return button;
}

/**
 * The quiet line for every state that is not an offer.
 *
 * `cancelled` never reaches here — the host drops it — but the guard is kept
 * because a state with no word would otherwise render a line reading "Docker
 * Hub: ." at a reader.
 */
export function stateNote(lookup: ImageLookup): HTMLElement | null {
  if (lookup.state === 'ok') {
    return null;
  }
  const word = stateWord(lookup.state);
  const text = noteText(lookup);
  if (word === '' && text === '') {
    return null;
  }
  const note = document.createElement('div');
  note.className = 'field-note image-note';
  note.dataset.imageState = lookup.state;
  note.textContent = text !== '' ? text : word;
  return note;
}
