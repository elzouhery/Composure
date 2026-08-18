// The control that offers the values a key accepts — story 7.9, rebuilt.
//
// WHY THIS IS NOT A `<datalist>`.
//
// The first build of this story bound the field to a `<datalist>` and the
// reasoning was sound as far as it went: a native `<select>` cannot hold a
// value that is not in its list, so `${RESTART_POLICY}` would stop being
// writable, and a scripted combobox is a popup, a roving tabindex and an
// `aria-activedescendant` to maintain for behaviour Chromium already ships.
//
// The outcome was still wrong, and one behaviour is what killed it: **a
// datalist filters its options against the input's current text.** So on
// `restart: unless-stopped` — the exact case the owner tested — opening the
// popup showed ONE entry, `unless-stopped`, because that is the only offered
// value the input's text is a prefix of. A control whose whole job is to say
// what a key accepts showed the reader nothing but what the field already said.
// The other three values were printed as a row of links UNDER the field, which
// is what the owner rejected in words: "the expected values should be in the
// list and not as links below it."
//
// Nothing about the datalist could be configured out of that. The filtering is
// the user agent's, there is no attribute for it, and the popup cannot be
// styled, themed, photographed or asserted on either. So the behaviour Chromium
// ships is the behaviour that had to go.
//
// What replaces it keeps every property the datalist was chosen FOR:
//
//   - the field is still the same `<input>`. Enter stages, Escape reverts, blur
//     commits, focus selects — `stageform.valueInput`, unchanged, so
//     `${RESTART_POLICY}` and any word from a Compose newer than the vendored
//     schema are typed and staged exactly as before. There is no `<select>`
//     anywhere in this file;
//   - zero runtime dependencies. This is the WAI-ARIA 1.2 combobox pattern
//     written out — `role="combobox"`, `aria-expanded`, `aria-controls`, a
//     `listbox` of `option`s and `aria-activedescendant` — and it is about a
//     hundred lines because that is what the pattern costs.
//
// And it adds the one property the datalist could not have: **the list is never
// filtered.** Opening it always shows every value the core reported, whatever
// the field currently holds. That is not an oversight to be improved into
// type-ahead later; it is the requirement, and filtering is the defect.

/** How one offered value is announced, and what the popup says about the set. */
export interface ComboSpec {
  /** The config path the field edits — the join key for everything else. */
  path: string;
  /** Every value the core reported, in the specification's own order. */
  values: string[];
  /** The sentence that says whether the set is closed. `inspector.allowedLead`. */
  lead: string;
  /** The `id` of the listbox, and the stem of every option's `id`. */
  listId: string;
  /** One value's accessible name, carrying the same closedness claim. */
  optionName: (value: string) => string;
  /** The toggle's accessible name. */
  toggleName: string;
}

/**
 * The popup, the arrow that opens it, and the keyboard that drives both.
 *
 * The input is NOT created here. It is the pane's own `valueInput`, built by
 * the caller and handed over with `attach`, which is the whole reason the
 * staging gesture survives: this class adds ARIA and a keydown pre-hook to a
 * control it does not own, and owns nothing about committing.
 */
export class Combo {
  /** The arrow. A real button, named, and out of the tab order by design. */
  readonly toggle: HTMLButtonElement;
  /** The floating surface: the closedness sentence, then the listbox. */
  readonly popup: HTMLElement;

  private readonly options: HTMLElement[] = [];
  private input: HTMLInputElement | null = null;
  private open = false;
  private active = -1;

  constructor(private readonly spec: ComboSpec) {
    const popup = el('div', 'combo-popup');
    popup.hidden = true;
    popup.dataset.for = spec.path;
    // A click anywhere on the surface must not blur the field: blur is what
    // commits, and a reader who clicks the popup's padding has not asked for
    // anything to be written. `tabindex` is on it for the same reason the
    // options carry one — it takes pointer events and therefore has to be
    // reachable, and the ARIA pattern keeps the tab stop on the input.
    popup.setAttribute('tabindex', '-1');
    popup.addEventListener('mousedown', (e: Event) => e.preventDefault());

    // The sentence that says whether the set is CLOSED. It used to sit under
    // the field beside the links; it moves in here rather than being lost,
    // because `gpus` and `restart` are genuinely different claims and a reader
    // shown a closed list that is not closed will believe the tool over the
    // file. It also travels in every option's accessible name, so the claim
    // reaches a reader who sees none of this.
    const lead = el('div', 'combo-lead');
    lead.textContent = spec.lead;
    popup.appendChild(lead);

    const listbox = el('div', 'combo-list');
    listbox.setAttribute('role', 'listbox');
    listbox.setAttribute('id', spec.listId);
    listbox.setAttribute('aria-label', spec.toggleName);
    for (const [i, value] of spec.values.entries()) {
      const option = el('div', 'combo-option mono');
      option.setAttribute('role', 'option');
      option.setAttribute('id', `${spec.listId}-${i}`);
      option.setAttribute('aria-selected', 'false');
      option.setAttribute('aria-label', spec.optionName(value));
      option.setAttribute('tabindex', '-1');
      option.dataset.value = value;
      // The mark that says "this is what the field holds", in a glyph and in
      // `aria-selected` — never in colour alone. It is always present and
      // always occupies its cell, so choosing a value does not reflow the list.
      const mark = el('span', 'combo-mark');
      mark.textContent = ' ';
      const text = el('span', 'combo-text');
      text.textContent = value;
      option.append(mark, text);
      option.addEventListener('mousedown', (e: Event) => e.preventDefault());
      option.addEventListener('click', () => this.choose(i));
      listbox.appendChild(option);
      this.options.push(option);
    }
    popup.appendChild(listbox);

    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'combo-toggle';
    // A glyph, not an icon font and not an SVG with a colour in it.
    toggle.textContent = '▾';
    toggle.dataset.for = spec.path;
    toggle.setAttribute('aria-label', spec.toggleName);
    // The ARIA pattern's rule: the input carries the tab stop and the arrow is
    // reachable from it with the down arrow key, so Tab does not stop twice on
    // one control. The input's own accessible name says the down arrow lists
    // the values, which is what makes that legal rather than a dead end.
    toggle.tabIndex = -1;
    toggle.addEventListener('mousedown', (e: Event) => e.preventDefault());
    toggle.addEventListener('click', () => {
      if (this.open) {
        this.close();
      } else {
        this.show();
      }
      this.input?.focus();
    });

    this.popup = popup;
    this.toggle = toggle;
  }

  /** The field this control belongs to. Adds the ARIA and nothing else. */
  attach(input: HTMLInputElement): void {
    this.input = input;
    input.classList.add('has-combo');
    input.setAttribute('role', 'combobox');
    input.setAttribute('aria-expanded', 'false');
    input.setAttribute('aria-controls', this.spec.listId);
    input.setAttribute('aria-haspopup', 'listbox');
    // `none`: this control never completes or filters what is typed, and
    // saying `list` would promise a reader that it does.
    input.setAttribute('aria-autocomplete', 'none');
    input.addEventListener('blur', () => this.close());
    this.mark();
  }

  /**
   * The keydown pre-hook, run BEFORE `valueInput`'s own handler.
   *
   * `true` means the key belonged to the popup and the field must not act on
   * it. That is the entire compatibility contract with DECISIONS.md 17: Enter
   * with the popup open CHOOSES a value and stages nothing, Enter with it
   * closed stages, and the reader's second Enter is the one that writes.
   * Escape closes the popup without changing anything; Escape with it closed
   * reverts the field exactly as it always has.
   */
  intercept(e: { key: string; preventDefault(): void }): boolean {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!this.open) {
        this.show();
      } else {
        this.move(1);
      }
      return true;
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (!this.open) {
        this.show();
      } else {
        this.move(-1);
      }
      return true;
    }
    if (!this.open) {
      // Every other key on a closed popup is the field's, untouched. This is
      // the branch that keeps Enter staging and Escape reverting.
      return false;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      if (this.active >= 0) {
        this.choose(this.active);
      } else {
        this.close();
      }
      return true;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      this.close();
      return true;
    }
    if (e.key === 'Tab') {
      // Not intercepted — focus really should leave — but the popup goes with
      // it, or it hangs over a field nobody is in.
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
   * Open, showing EVERY value.
   *
   * There is no filtering step here and there is no place to add one. The
   * active option starts on the value the field already holds when that is one
   * of them, which is how a reader arrives on their current choice rather than
   * on the top of a list — and it is the only way the current text influences
   * this popup at all.
   */
  private show(): void {
    this.open = true;
    this.popup.hidden = false;
    this.input?.setAttribute('aria-expanded', 'true');
    const current = this.spec.values.indexOf(this.input?.value ?? '');
    this.setActive(current >= 0 ? current : 0);
    this.mark();
  }

  private close(): void {
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
    }
    const id = index >= 0 ? `${this.spec.listId}-${index}` : '';
    this.input?.setAttribute('aria-activedescendant', id);
  }

  /**
   * Put a value in the field. STAGES NOTHING — DECISIONS.md 17's gesture,
   * unchanged from the chips this replaces: choosing says which value, Enter
   * says write it.
   */
  private choose(index: number): void {
    const value = this.spec.values[index];
    if (value === undefined) {
      return;
    }
    this.close();
    const input = this.input;
    if (input) {
      input.value = value;
      input.focus();
    }
    this.mark();
  }

  /** Which option the field's current text is, in the list and in ARIA. */
  private mark(): void {
    const current = this.input?.value ?? '';
    for (const [i, option] of this.options.entries()) {
      const is = this.spec.values[i] === current;
      option.setAttribute('aria-selected', is ? 'true' : 'false');
      option.classList.toggle('is-current', is);
      const mark = option.children[0];
      if (mark instanceof HTMLElement) {
        mark.textContent = is ? '✓' : ' ';
      }
    }
  }
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className = '',
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  return node;
}
