// Finding an image on Docker Hub by name — Epic 8, R6.1.
//
// The owner's request was two things and this is the second: "Image names are
// not lookup from dockerhub or even option to search for image in docker hub."
//
// IT IS THE SAME COMBOBOX STORY 7.9 ALREADY SHIPS, with one difference. The
// reasoning at the top of `combo.ts` holds here without amendment — the field
// is still a real `<input>` that takes anything typed into it, there is no
// `<select>` anywhere, and the WAI-ARIA 1.2 pattern is written out because
// that is what the pattern costs. What differs is where the options come from:
// story 7.9's arrive with the pane, from a vendored schema, and are known
// before the control exists. These arrive later, from a network that may not
// be there, and there may be none.
//
// So this control has states 7.9's has not, and every one of them keeps the
// field typeable:
//
//   nothing typed   the popup says what it is for.
//   waiting         it says so. Never a spinner over a field.
//   results         a listbox. Choosing one FILLS the field.
//   a sentence      offline, rate-limited, switched off, nothing found. The
//                   sentence is the core's, worded once in Go.
//
// A SEARCH BOX THAT STOPS WORKING MUST NOT STOP BEING A TEXT FIELD. That is
// the whole reason the failure states render inside the popup rather than
// disabling anything: Docker Hub being busy is not a reason a reader cannot
// type `postgres:16-alpine`, which they knew the whole time.

/** How long the field must settle before a request is made. */
export const SEARCH_DEBOUNCE_MS = 300;

/** Below this a query is not worth a share of a rate limit shared by an office. */
export const MIN_QUERY_LENGTH = 2;

import type { ImageRepo, ImageSearchAnswer } from '../shared/protocol';

export interface ImageSearchSpec {
  /** The join key for the field this belongs to — a config path, or `add:image`. */
  key: string;
  /** Sends the request. The view routes the answer back through `receive`. */
  search(token: number, query: string): void;
  /**
   * What choosing a result does. It FILLS THE FIELD — the caller decides
   * whether that also stages, because a search box in the add form has nothing
   * to stage against yet and one in the inspector does.
   */
  choose(reference: string): void;
}

/** Where a token comes from. Module-scoped so two controls cannot collide. */
let nextToken = 1;

export class ImageSearch {
  /** The magnifier. A real button, named, out of the tab order by design. */
  readonly toggle: HTMLButtonElement;
  /** The floating surface: a sentence, then the listbox. */
  readonly popup: HTMLElement;

  private readonly listbox: HTMLElement;
  private readonly lead: HTMLElement;
  private readonly listId: string;
  private input: HTMLInputElement | null = null;
  private options: HTMLElement[] = [];
  private values: string[] = [];
  private open = false;
  private active = -1;
  private timer: ReturnType<typeof setTimeout> | undefined;
  /** The request the reader is still waiting for. An older answer is dropped. */
  private waiting = 0;

  constructor(private readonly spec: ImageSearchSpec) {
    this.listId = `imagesearch-${spec.key.replace(/[^A-Za-z0-9_-]/g, '-')}`;

    const popup = el('div', 'combo-popup imagesearch-popup');
    popup.hidden = true;
    popup.dataset.for = spec.key;
    popup.setAttribute('tabindex', '-1');
    // A click on the surface must not blur the field: blur is what commits, and
    // a reader who clicked the popup's padding has asked for nothing to happen.
    popup.addEventListener('mousedown', (e: Event) => e.preventDefault());

    this.lead = el('div', 'combo-lead');
    this.lead.textContent = 'Type at least two characters to search Docker Hub.';
    // Announced as it changes: this line carries "searching", "nothing found"
    // and every failure sentence, and a reader who cannot see the popup gets
    // none of them otherwise.
    this.lead.setAttribute('role', 'status');
    this.lead.setAttribute('aria-live', 'polite');
    popup.appendChild(this.lead);

    this.listbox = el('div', 'combo-list');
    this.listbox.setAttribute('role', 'listbox');
    this.listbox.setAttribute('id', this.listId);
    this.listbox.setAttribute('aria-label', 'Docker Hub search results');
    popup.appendChild(this.listbox);

    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'combo-toggle';
    // A glyph, not an icon font and not an SVG with a colour in it.
    toggle.textContent = '⌕';
    toggle.dataset.for = spec.key;
    toggle.setAttribute('aria-label', `Search Docker Hub for an image for ${spec.key}`);
    // The ARIA pattern: the input carries the tab stop and the arrow is reached
    // from it with the down arrow key, so Tab does not stop twice on one
    // control. The input's own name says the down arrow searches.
    toggle.tabIndex = -1;
    toggle.addEventListener('mousedown', (e: Event) => e.preventDefault());
    toggle.addEventListener('click', () => {
      if (this.open) {
        this.close();
      } else {
        this.show();
        this.request(this.input?.value ?? '');
      }
      this.input?.focus();
    });

    this.popup = popup;
    this.toggle = toggle;
  }

  /**
   * The field this belongs to. Adds ARIA and a listener, and owns nothing about
   * committing — the input is the pane's own `valueInput`, so Enter stages,
   * Escape reverts and blur commits exactly as they did before this existed.
   */
  attach(input: HTMLInputElement): void {
    this.input = input;
    input.classList.add('has-combo');
    input.setAttribute('role', 'combobox');
    input.setAttribute('aria-expanded', 'false');
    input.setAttribute('aria-controls', this.listId);
    input.setAttribute('aria-haspopup', 'listbox');
    // `list`, not `none`: unlike story 7.9's control this one genuinely does
    // narrow what is offered by what is typed, because what is typed is the
    // query. Saying `none` here would be the same lie in the other direction.
    input.setAttribute('aria-autocomplete', 'list');
    input.addEventListener('input', () => {
      if (!this.open) {
        this.show();
      }
      this.request(input.value);
    });
    input.addEventListener('blur', () => this.close());
  }

  /**
   * The keydown pre-hook, run BEFORE `valueInput`'s own handler.
   *
   * `true` means the key belonged to the popup. The contract is DECISIONS.md
   * 17's, identical to `combo.ts`: Enter with the popup open CHOOSES and stages
   * nothing, Enter with it closed stages, Escape closes and then reverts.
   */
  intercept(e: { key: string; preventDefault(): void }): boolean {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!this.open) {
        this.show();
        this.request(this.input?.value ?? '');
      } else {
        this.move(e.key === 'ArrowDown' ? 1 : -1);
      }
      return true;
    }
    if (!this.open) {
      return false;
    }
    if (e.key === 'Enter') {
      if (this.active < 0) {
        // Nothing is highlighted, so there is nothing to choose and the field's
        // own Enter should do what it always does — stage what was typed. The
        // popup goes, because it would otherwise hang over a committed field.
        this.close();
        return false;
      }
      e.preventDefault();
      this.choose(this.active);
      return true;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      this.close();
      return true;
    }
    if (e.key === 'Tab') {
      this.close();
      return false;
    }
    return false;
  }

  /** Whether the popup is showing. Read by the tests and by nothing else. */
  get expanded(): boolean {
    return this.open;
  }

  /**
   * One answer from the host.
   *
   * An answer to a request the reader has already superseded is DROPPED. A
   * popup that fills with the results of the query before last is worse than
   * one that is still empty, because the reader believes it.
   */
  receive(token: number, answer: ImageSearchAnswer): void {
    if (token !== this.waiting) {
      return;
    }
    this.render(answer);
  }

  private request(query: string): void {
    const q = query.trim();
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
    }
    if (q.length < MIN_QUERY_LENGTH) {
      this.waiting = 0;
      this.renderEmpty('Type at least two characters to search Docker Hub.');
      return;
    }
    this.lead.textContent = `Searching Docker Hub for “${q}”…`;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      const token = nextToken++;
      this.waiting = token;
      this.spec.search(token, q);
    }, SEARCH_DEBOUNCE_MS);
  }

  private renderEmpty(sentence: string): void {
    this.lead.textContent = sentence;
    this.listbox.textContent = '';
    this.options = [];
    this.values = [];
    this.setActive(-1);
  }

  private render(answer: ImageSearchAnswer): void {
    if (answer.results.length === 0) {
      // The core's own sentence, whatever the reason: offline, rate-limited,
      // switched off, or simply nothing matching. One wording, composed in Go,
      // so the popup and the pane cannot describe one 429 two ways.
      this.renderEmpty(answer.message || `Docker Hub has nothing matching “${answer.query}”.`);
      return;
    }
    this.lead.textContent =
      answer.message !== ''
        ? answer.message
        : `${answer.results.length} on Docker Hub. Choosing one fills the field; ` +
          'nothing is written until you save.';
    this.listbox.textContent = '';
    this.options = [];
    this.values = [];
    for (const [i, repo] of answer.results.entries()) {
      const option = el('div', 'combo-option imagesearch-option');
      option.setAttribute('role', 'option');
      option.setAttribute('id', `${this.listId}-${i}`);
      option.setAttribute('aria-selected', 'false');
      option.setAttribute('aria-label', optionName(repo));
      option.setAttribute('tabindex', '-1');
      option.dataset.value = repo.name;

      const name = el('span', 'imagesearch-name mono');
      name.textContent = repo.name;
      const meta = el('span', 'imagesearch-meta');
      meta.textContent = metaLine(repo);
      option.append(name, meta);
      if (repo.description) {
        const desc = el('span', 'imagesearch-desc');
        desc.textContent = repo.description;
        option.appendChild(desc);
      }
      option.addEventListener('mousedown', (e: Event) => e.preventDefault());
      option.addEventListener('click', () => this.choose(i));
      this.listbox.appendChild(option);
      this.options.push(option);
      this.values.push(repo.name);
    }
    this.setActive(0);
  }

  private show(): void {
    this.open = true;
    this.popup.hidden = false;
    this.input?.setAttribute('aria-expanded', 'true');
  }

  private close(): void {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    if (!this.open) {
      return;
    }
    this.open = false;
    this.popup.hidden = true;
    this.input?.setAttribute('aria-expanded', 'false');
    this.setActive(-1);
  }

  private move(by: number): void {
    const n = this.options.length;
    if (n === 0) {
      return;
    }
    this.setActive((this.active + by + n) % n);
  }

  private setActive(index: number): void {
    this.active = index;
    for (const [i, option] of this.options.entries()) {
      option.classList.toggle('is-active', i === index);
      option.setAttribute('aria-selected', String(i === index));
    }
    this.input?.setAttribute('aria-activedescendant', index >= 0 ? `${this.listId}-${index}` : '');
  }

  /**
   * Put a repository in the field.
   *
   * The NAME, not a name plus a guessed tag. Docker Hub search answers with
   * repositories and this control has not asked which tag; appending `:latest`
   * would write a pin the reader did not choose, and `latest` is the one tag
   * this product already refuses to offer as an upgrade (`hub.IsUnstable`).
   */
  private choose(index: number): void {
    const value = this.values[index];
    if (value === undefined) {
      return;
    }
    this.close();
    if (this.input) {
      this.input.value = value;
      this.input.focus();
    }
    this.spec.choose(value);
  }
}

/** One result's accessible name: everything the row shows, as a sentence. */
export function optionName(repo: ImageRepo): string {
  const parts = [repo.name];
  if (repo.badge) {
    parts.push(repo.badge.replace(/_/g, ' '));
  }
  parts.push(`${repo.stars} stars`);
  if (repo.pulls_display) {
    parts.push(`${repo.pulls_display} pulls`);
  }
  if (repo.description) {
    parts.push(repo.description);
  }
  return parts.join(', ');
}

/** The metadata line under a result — R6.1's fields, minus the description. */
export function metaLine(repo: ImageRepo): string {
  const parts: string[] = [];
  if (repo.badge) {
    parts.push(repo.badge.replace(/_/g, ' '));
  }
  parts.push(`★ ${repo.stars}`);
  if (repo.pulls_display) {
    parts.push(`${repo.pulls_display} pulls`);
  }
  if (repo.architectures && repo.architectures.length > 0) {
    parts.push(repo.architectures.join(' '));
  }
  return parts.join(' · ');
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  return node;
}
