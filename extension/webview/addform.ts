// `+ add` — declaring something the file does not have yet.
//
// Stories 7.3 and 7.4. The control the empty-stack state has promised since
// EXPERIENCE.md was written and the code refused to draw, "on the grounds that
// authoring is a later phase and a button that opens nothing is worse than no
// button". The button now opens something (DECISIONS.md 20).
//
// What this module knows is what the reader typed. It does not know where a
// service goes, whether a name is free, what a resource's default driver would
// be, or whether a value needs quoting — all four are the core's answers, asked
// for over `stack/add` before anything is staged, because a second answer here
// would disagree with the file the moment a stack had two of them.
//
// Nothing here writes. The declaration joins whatever else the reader has
// staged and lands with the one `Save to <file>` press, as one edit and one
// undo.

import { ADD_KINDS } from '../shared/protocol';
import { ImageSearch } from './imagesearch';
import type { AddKind, ImageSearchAnswer, WebviewMessage } from '../shared/protocol';

export interface AddFormCallbacks {
  send(msg: WebviewMessage): void;
}

/** The kinds, and the word each one puts in front of its name field. */
const PLACEHOLDER: Record<AddKind, string> = {
  service: 'service name',
  network: 'network name',
  volume: 'volume name',
  config: 'config name',
  secret: 'secret name',
};

export class AddFormView {
  readonly element = el('div', 'addbar');

  private readonly toggle = document.createElement('button');
  private readonly composer = el('div', 'add-composer');
  private readonly kinds = el('div', 'add-kinds');
  private readonly note = el('p', 'add-note');
  private readonly nameInput = document.createElement('input');
  private readonly valueInput = document.createElement('input');
  private readonly kindButtons = new Map<AddKind, HTMLButtonElement>();

  private isOpen = false;
  private kind: AddKind = 'service';
  /**
   * Docker Hub search on the image field — Epic 8, and the half of the owner's
   * request that is about NAMING an image rather than upgrading one.
   *
   * This is the form where it matters most: the reader is declaring a service
   * that does not exist yet, so unlike the inspector there is no current value
   * to work from and nothing on screen tells them how the image is spelled.
   *
   * Built once, in the constructor, because unlike the inspector this form is
   * not rebuilt on every render — the input it is attached to outlives every
   * open and close.
   */
  private readonly search: ImageSearch;

  constructor(private readonly callbacks: AddFormCallbacks) {
    this.toggle.type = 'button';
    this.toggle.className = 'toolbar-button';
    this.toggle.textContent = '+ add';
    this.toggle.dataset.control = 'add';
    this.toggle.setAttribute('aria-label', 'Declare a service, network, volume, config or secret');
    this.toggle.setAttribute('aria-pressed', 'false');
    this.toggle.addEventListener('click', () => {
      if (this.isOpen) {
        this.close();
        return;
      }
      this.openFor(this.kind);
    });

    // One button per kind, from the shared list — never a list written out
    // again here. A kind this control offered and the core did not is a control
    // that refuses whatever the reader types into it.
    this.kinds.setAttribute('role', 'group');
    this.kinds.setAttribute('aria-label', 'What to declare');
    for (const kind of ADD_KINDS) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'toolbar-button';
      button.textContent = kind;
      button.dataset.kind = kind;
      button.setAttribute('aria-pressed', String(kind === this.kind));
      button.addEventListener('click', () => this.choose(kind));
      this.kindButtons.set(kind, button);
      this.kinds.appendChild(button);
    }

    this.nameInput.type = 'text';
    this.nameInput.className = 'toolbar-search mono';
    this.nameInput.dataset.field = 'add:name';
    this.valueInput.type = 'text';
    this.valueInput.className = 'toolbar-search mono';
    this.valueInput.dataset.field = 'add:image';
    this.valueInput.placeholder = 'image, e.g. redis:7';
    this.valueInput.setAttribute('aria-label', 'the image the new service runs');
    this.search = new ImageSearch({
      key: 'add:image',
      search: (token, query) => this.callbacks.send({ type: 'searchImage', token, query }),
      // Fills the field and nothing else — DECISIONS.md 17's gesture. There is
      // nothing to stage yet in any case: the reader has not said what the
      // service is called, and `stack/add` plans the whole declaration at once.
      choose: () => undefined,
    });
    for (const input of [this.nameInput, this.valueInput]) {
      input.addEventListener('keydown', (e) => {
        // The popup gets first refusal on every key, exactly as `combo.ts` does
        // in the inspector: Enter with it open CHOOSES, Enter with it closed
        // submits the form, Escape closes it and then closes the composer.
        if (input === this.valueInput && this.search.intercept(e)) {
          return;
        }
        if (e.key === 'Enter') {
          e.preventDefault();
          this.submit();
          return;
        }
        if (e.key === 'Escape') {
          e.preventDefault();
          this.close();
        }
      });
    }

    this.search.attach(this.valueInput);
    this.composer.append(
      this.kinds,
      this.nameInput,
      this.valueInput,
      this.search.toggle,
      this.search.popup,
      this.note,
    );
    this.composer.hidden = true;
    this.element.append(this.toggle, this.composer);
    this.applyKind();
  }

  /** Opens the composer on a kind and puts the cursor in the name field. */
  openFor(kind: AddKind): void {
    this.kind = kind;
    this.isOpen = true;
    this.composer.hidden = false;
    this.toggle.setAttribute('aria-pressed', 'true');
    this.applyKind();
    this.nameInput.focus();
  }

  /** Closes it and forgets what was half-typed. Nothing was staged. */
  close(): void {
    this.isOpen = false;
    this.composer.hidden = true;
    this.toggle.setAttribute('aria-pressed', 'false');
    this.nameInput.value = '';
    this.valueInput.value = '';
  }

  /** One search answer, for the popup on the image field. */
  receiveSearch(token: number, answer: ImageSearchAnswer): void {
    this.search.receive(token, answer);
  }

  private choose(kind: AddKind): void {
    this.kind = kind;
    this.applyKind();
    this.nameInput.focus();
  }

  private applyKind(): void {
    for (const [kind, button] of this.kindButtons) {
      button.setAttribute('aria-pressed', String(kind === this.kind));
    }
    this.nameInput.placeholder = PLACEHOLDER[this.kind];
    this.nameInput.setAttribute('aria-label', `the ${this.kind}’s name`);
    // Only a service takes a value, and the field is ABSENT rather than
    // disabled for the other four: a network with an image field is an offer to
    // write `frontend: bridge`, which declares nothing.
    // Only a service takes an image, so the search control goes with the field
    // — a magnifier beside a hidden input is a control that searches nothing.
    this.valueInput.hidden = this.kind !== 'service';
    this.search.toggle.hidden = this.kind !== 'service';
    if (this.kind !== 'service') {
      this.search.popup.hidden = true;
    }
    this.note.textContent =
      this.kind === 'service'
        ? 'The service and its image go in together, after the last service in the file, ' +
          'at the indentation the file already uses. Nothing is written until you save.'
        : `The ${this.kind} is declared with nothing under it — no default driver, no invented ` +
          'body. Attaching it to a service is a separate act. Nothing is written until you save.';
  }

  /**
   * Hands what the reader typed to the host.
   *
   * It does not validate. Whether a service needs an image, whether the name is
   * free and whether the value survives unquoted are refusals with sentences of
   * their own in the core, and a check here would either duplicate them or —
   * worse — quietly disagree. The one thing it declines to do is send an empty
   * form, because there is nothing in it to declare.
   */
  private submit(): void {
    const name = this.nameInput.value.trim();
    const value = this.valueInput.value.trim();
    if (name === '' && value === '') {
      this.close();
      return;
    }
    this.callbacks.send({
      type: 'add',
      kind: this.kind,
      name,
      value: this.kind === 'service' ? value : '',
    });
    this.close();
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
