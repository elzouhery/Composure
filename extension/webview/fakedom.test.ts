// A DOM small enough to run under `node --test`, shared by every render-level
// suite in this directory.
//
// It is named `*.test.ts` so the accessibility scan skips it by name — it ships
// nowhere — but it declares no tests of its own and is not in `build.mjs`'s
// suite list. It is bundled into whichever suite imports it.
//
// WHY THIS EXISTS. Every webview suite used to test extracted pure functions on
// the stated grounds that "a webview cannot be launched under node --test". That
// is true of a webview and false of the DOM API these views actually use:
// element creation, attributes, classes, children, listeners and focus. The gap
// it left is exactly where the surviving mutations lived — a `case` kept and its
// body emptied, `applyRovingTabIndex` present by name while every node gets
// `tabindex="-1"`, an edge drawn without its kind class. Each of those is a
// property of the TREE or of a MESSAGE, so none was reachable from a test of the
// arithmetic, and each passed a source scan that greps for a token the mutation
// leaves in place.
//
// So: about three hundred lines of DOM, and then assertions about what the
// reader would actually see and what the view would actually post.

/* -------------------------------------------------------------------------
 * Selectors. Only the shapes this codebase writes.
 * ---------------------------------------------------------------------- */

interface SimpleSelector {
  tag: string | null;
  classes: string[];
  attrs: { name: string; value: string }[];
}

function parseSimple(part: string): SimpleSelector {
  const out: SimpleSelector = { tag: null, classes: [], attrs: [] };
  const pattern = /^([a-zA-Z][\w-]*)|\.([\w-]+)|\[([\w-]+)="((?:[^"\\]|\\.)*)"\]/;
  let rest = part;
  while (rest !== '') {
    const m = pattern.exec(rest);
    if (!m) {
      break;
    }
    if (m[1]) {
      out.tag = m[1].toLowerCase();
    } else if (m[2]) {
      out.classes.push(m[2]);
    } else if (m[3]) {
      out.attrs.push({ name: m[3], value: m[4].replace(/\\(["\\])/g, '$1') });
    }
    rest = rest.slice(m[0].length);
  }
  return out;
}

function matchesSimple(el: El, sel: SimpleSelector): boolean {
  if (sel.tag !== null && el.tagName.toLowerCase() !== sel.tag) {
    return false;
  }
  for (const c of sel.classes) {
    if (!el.classList.contains(c)) {
      return false;
    }
  }
  for (const a of sel.attrs) {
    const dataKey = a.name.startsWith('data-') ? camel(a.name.slice(5)) : null;
    const actual = dataKey !== null ? el.dataset[dataKey] : (el.getAttribute(a.name) ?? undefined);
    if (actual !== a.value) {
      return false;
    }
  }
  return true;
}

function camel(name: string): string {
  return name.replace(/-([a-z])/g, (_, c: string) => c.toUpperCase());
}

/** `.a .b` and `.a` — descendant combinators only, which is all that is used. */
function matchesSelector(el: El, selector: string): boolean {
  for (const alternative of selector.split(',')) {
    const parts = alternative.trim().split(/\s+/).filter((p) => p !== '');
    if (parts.length === 0) {
      continue;
    }
    if (!matchesSimple(el, parseSimple(parts[parts.length - 1]))) {
      continue;
    }
    let ok = true;
    let cursor: El | null = el.parent;
    for (let i = parts.length - 2; i >= 0; i--) {
      const want = parseSimple(parts[i]);
      while (cursor !== null && !matchesSimple(cursor, want)) {
        cursor = cursor.parent;
      }
      if (cursor === null) {
        ok = false;
        break;
      }
      cursor = cursor.parent;
    }
    if (ok) {
      return true;
    }
  }
  return false;
}

/* -------------------------------------------------------------------------
 * The tree.
 * ---------------------------------------------------------------------- */

export class El {
  tagName: string;
  className = '';
  children: El[] = [];
  parent: El | null = null;
  private attrs = new Map<string, string>();
  private listeners = new Map<string, ((e: any) => void)[]>();
  dataset: Record<string, string> = {};
  /**
   * Enough of CSSStyleDeclaration for what the webview actually sets: named
   * properties by assignment (`flexBasis`), and CUSTOM properties through
   * `setProperty` — which is how the nesting level reaches a row as `--ind`.
   * A plain object had no `setProperty`, so the first custom property the
   * webview set threw here and nowhere else.
   */
  style: Record<string, string> & {
    setProperty(name: string, value: string): void;
    getPropertyValue(name: string): string;
    removeProperty(name: string): void;
  } = Object.assign(Object.create(null) as Record<string, string>, {
    setProperty(this: Record<string, string>, name: string, value: string): void {
      this[name] = value;
    },
    getPropertyValue(this: Record<string, string>, name: string): string {
      return this[name] ?? '';
    },
    removeProperty(this: Record<string, string>, name: string): void {
      delete this[name];
    },
  });
  title = '';
  type = '';
  value = '';
  placeholder = '';
  hidden = false;
  tabIndex = 0;
  /** What `getBoundingClientRect` reports. Overridable per element. */
  rect = { width: 800, height: 600, left: 0, top: 0, right: 800, bottom: 600 };
  clientWidth = 800;
  clientHeight = 600;
  private text = '';

  constructor(tag: string, preserveCase = false) {
    this.tagName = preserveCase ? tag : tag.toUpperCase();
  }

  get textContent(): string {
    // Own text first, then children — the way a real element reads when a label
    // is set and a suffix span is appended after it.
    return this.text + this.children.map((c) => c.textContent).join('');
  }

  set textContent(value: string) {
    // Matches the real thing: assigning replaces every child.
    this.children = [];
    this.text = value;
  }

  get classList() {
    const parts = (): string[] => this.className.split(/\s+/).filter((s) => s !== '');
    return {
      add: (...names: string[]) => {
        const set = new Set([...parts(), ...names]);
        this.className = [...set].join(' ');
      },
      remove: (...names: string[]) => {
        this.className = parts().filter((p) => !names.includes(p)).join(' ');
      },
      contains: (name: string) => parts().includes(name),
      toggle: (name: string, on?: boolean) => {
        const want = on === undefined ? !parts().includes(name) : on;
        if (want) {
          this.classList.add(name);
        } else {
          this.classList.remove(name);
        }
      },
    };
  }

  setAttribute(name: string, value: string): void {
    if (name === 'class') {
      this.className = value;
      return;
    }
    if (name === 'tabindex') {
      this.tabIndex = Number(value);
    }
    this.attrs.set(name, value);
  }

  getAttribute(name: string): string | null {
    if (name === 'class') {
      return this.className;
    }
    return this.attrs.get(name) ?? null;
  }

  removeAttribute(name: string): void {
    this.attrs.delete(name);
  }

  hasAttribute(name: string): boolean {
    return this.getAttribute(name) !== null;
  }

  appendChild(child: El): El {
    if (child.parent) {
      child.parent.children = child.parent.children.filter((c) => c !== child);
    }
    child.parent = this;
    this.children.push(child);
    return child;
  }

  append(...nodes: El[]): void {
    for (const n of nodes) {
      this.appendChild(n);
    }
  }

  insertBefore(child: El, ref: El | null): El {
    child.parent = this;
    const at = ref === null ? this.children.length : this.children.indexOf(ref);
    this.children.splice(at < 0 ? this.children.length : at, 0, child);
    return child;
  }

  /**
   * Detaches this element from its parent, as `ChildNode.remove()` does.
   *
   * A control that must be ABSENT rather than merely empty (story 4.6's profile
   * group) is removed from the tree, so a test can assert it is not there at
   * all — which is a different claim from `hidden`.
   */
  remove(): void {
    if (this.parent) {
      this.parent.children = this.parent.children.filter((c) => c !== this);
      this.parent = null;
    }
  }

  get firstChild(): El | null {
    return this.children[0] ?? null;
  }

  addEventListener(name: string, fn: (e: any) => void, _options?: unknown): void {
    const list = this.listeners.get(name) ?? [];
    list.push(fn);
    this.listeners.set(name, list);
  }

  /** How many listeners are attached — a deleted listener is a deleted gesture. */
  listenerCount(name: string): number {
    return (this.listeners.get(name) ?? []).length;
  }

  /** Fires a listener the way a reader's click or keypress would. */
  fire(name: string, event: any = {}): void {
    for (const fn of [...(this.listeners.get(name) ?? [])]) {
      fn({ preventDefault() {}, stopPropagation() {}, target: this, ...event });
    }
  }

  focus(): void {
    (globalThis as any).document.activeElement = this;
    this.fire('focus');
  }

  select(): void {}

  blur(): void {
    this.fire('blur');
  }

  getBoundingClientRect(): typeof this.rect {
    return this.rect;
  }

  setPointerCapture(_id: number): void {}
  releasePointerCapture(_id: number): void {}

  matches(selector: string): boolean {
    return matchesSelector(this, selector);
  }

  closest(selector: string): El | null {
    let cursor: El | null = this;
    while (cursor !== null) {
      if (matchesSelector(cursor, selector)) {
        return cursor;
      }
      cursor = cursor.parent;
    }
    return null;
  }

  querySelector(selector: string): El | null {
    return walk(this).find((e) => matchesSelector(e, selector)) ?? null;
  }

  querySelectorAll(selector: string): El[] {
    return walk(this).filter((e) => matchesSelector(e, selector));
  }
}

export class HtmlEl extends El {}
export class InputEl extends HtmlEl {}
export class SvgEl extends El {}

/** Every descendant, in document order. */
export function walk(root: El): El[] {
  const out: El[] = [];
  const visit = (e: El): void => {
    for (const c of e.children) {
      out.push(c);
      visit(c);
    }
  };
  visit(root);
  return out;
}

export const byClass = (root: El, name: string): El[] =>
  walk(root).filter((e) => e.classList.contains(name));

/* -------------------------------------------------------------------------
 * Globals.
 * ---------------------------------------------------------------------- */

export interface Clock {
  /** Runs every timer due now, repeatedly, so a timer that sets a timer runs. */
  flush(): void;
  pending(): number;
}

export interface FakeWindow {
  addEventListener(name: string, fn: (e: any) => void): void;
  fire(name: string, event: any): void;
  setTimeout(fn: () => void, ms: number): number;
  clearTimeout(id: number): void;
}

export interface Dom {
  document: any;
  window: FakeWindow;
  clock: Clock;
  /** Every ResizeObserver callback registered, fired together. */
  resize(): void;
  body: El;
}

let installed: Dom | undefined;

/**
 * Installs a DOM on `globalThis`.
 *
 * Idempotent per call: each call replaces the previous document, so a suite can
 * start from a clean tree between cases.
 */
export function installDom(): Dom {
  const timers = new Map<number, { at: number; fn: () => void }>();
  let nextTimer = 1;
  let now = 0;

  const clock: Clock = {
    flush(): void {
      for (let round = 0; round < 50 && timers.size > 0; round++) {
        const due = [...timers.entries()].sort((a, b) => a[1].at - b[1].at);
        now = Math.max(now, due[due.length - 1][1].at);
        for (const [id, t] of due) {
          if (timers.delete(id)) {
            t.fn();
          }
        }
      }
    },
    pending: () => timers.size,
  };

  const body = new HtmlEl('body');
  const listeners = new Map<string, ((e: any) => void)[]>();
  const observers: (() => void)[] = [];

  const window: FakeWindow = {
    addEventListener(name, fn) {
      const list = listeners.get(name) ?? [];
      list.push(fn);
      listeners.set(name, list);
    },
    fire(name, event) {
      for (const fn of [...(listeners.get(name) ?? [])]) {
        fn(event);
      }
    },
    setTimeout(fn, ms) {
      const id = nextTimer++;
      timers.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id);
    },
  };

  const byId = new Map<string, El>();
  const document = {
    activeElement: null as El | null,
    body,
    createElement(tag: string): El {
      return tag === 'input' ? new InputEl(tag) : new HtmlEl(tag);
    },
    createElementNS(_ns: string, tag: string): El {
      return new SvgEl(tag, true);
    },
    getElementById(id: string): El | null {
      let found = byId.get(id);
      if (!found) {
        found = new HtmlEl('div');
        found.setAttribute('id', id);
        body.appendChild(found);
        byId.set(id, found);
      }
      return found;
    },
    querySelector(selector: string): El | null {
      return body.querySelector(selector);
    },
  };

  class FakeResizeObserver {
    constructor(private readonly fn: () => void) {
      observers.push(() => this.fn());
    }
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }

  const g = globalThis as any;
  g.document = document;
  g.window = window;
  g.Element = El;
  g.HTMLElement = HtmlEl;
  g.HTMLInputElement = InputEl;
  g.SVGElement = SvgEl;
  g.ResizeObserver = FakeResizeObserver;

  installed = {
    document,
    window,
    clock,
    body,
    resize(): void {
      for (const fn of observers) {
        fn();
      }
    },
  };
  return installed;
}

export function dom(): Dom {
  if (!installed) {
    throw new Error('installDom() has not been called');
  }
  return installed;
}

/* -------------------------------------------------------------------------
 * The DOM checks itself.
 *
 * Every render-level assertion in this directory is only as good as this file:
 * a fake DOM that quietly disagrees with the real one turns a suite into a
 * theory about a browser nobody has. These are the behaviours the views depend
 * on and that a hand-written DOM gets wrong — `textContent` reading own text
 * before children, assignment clearing children, `classList.toggle` with an
 * explicit state, and descendant selectors.
 *
 * It also gives this file a test, so `build.mjs` can compile it alongside the
 * suites that import it and `host/packaging.test.ts` keeps its guarantee that
 * every `*.test.ts` on disk is compiled.
 * ---------------------------------------------------------------------- */

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

describe('the fake DOM behaves the way the real one does', () => {
  const d = installDom();

  it('reads own text before children, and assignment clears them', () => {
    const span = d.document.createElement('span') as El;
    span.textContent = 'restart';
    const suffix = d.document.createElement('span') as El;
    suffix.textContent = ' needs Compose 2.22.0';
    span.appendChild(suffix);
    assert.equal(span.textContent, 'restart needs Compose 2.22.0');
    span.textContent = 'restart';
    assert.deepEqual(span.children, [], 'assigning textContent left the children in place');
  });

  it('toggles a class to an explicit state, not just on and off', () => {
    const el = d.document.createElement('div') as El;
    el.classList.toggle('is-match', false);
    assert.equal(el.classList.contains('is-match'), false, 'toggle(name, false) added the class');
    el.classList.toggle('is-match', true);
    el.classList.toggle('is-match', true);
    assert.equal(el.className, 'is-match', 'toggle(name, true) added the class twice');
  });

  it('matches descendant selectors and walks up to the nearest ancestor', () => {
    const outer = d.document.createElement('div') as El;
    outer.className = 'node';
    const inner = d.document.createElement('span') as El;
    inner.className = 'label';
    outer.appendChild(inner);
    assert.equal(inner.closest('.node'), outer);
    assert.equal(inner.closest('.missing'), null);
    assert.deepEqual(outer.querySelectorAll('.node .label'), [inner]);
    assert.deepEqual(outer.querySelectorAll('.other .label'), []);
  });

  it('finds an element by a data attribute holding a config path', () => {
    const root = d.document.createElement('div') as El;
    const field = d.document.createElement('input') as El;
    field.dataset.field = 'services.web.ports[0]';
    root.appendChild(field);
    assert.equal(root.querySelector('[data-field="services.web.ports[0]"]'), field);
  });

  it('detaches an element from its parent, and re-appending puts it back once', () => {
    const parent = d.document.createElement('div') as El;
    const child = d.document.createElement('span') as El;
    parent.appendChild(child);
    child.remove();
    assert.deepEqual(parent.children, [], 'remove() left the child attached');
    assert.equal(child.parent, null);
    parent.appendChild(child);
    parent.appendChild(child);
    assert.deepEqual(parent.children, [child], 'appending twice attached the child twice');
  });

  it('runs a timer that schedules another timer', () => {
    const order: string[] = [];
    d.window.setTimeout(() => {
      order.push('first');
      d.window.setTimeout(() => order.push('second'), 10);
    }, 10);
    d.clock.flush();
    assert.deepEqual(order, ['first', 'second']);
    assert.equal(d.clock.pending(), 0);
  });
});
