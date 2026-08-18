// `+ add`, driven — stories 7.3 and 7.4.
//
// The control is asserted by what it POSTS, not by what it renders: a composer
// that draws five kind buttons and sends `service` for all of them looks right
// in a screenshot and writes the wrong thing into someone's file. Every test
// here goes through the real AddFormView and reads the messages it sent.

import { describe, it, beforeEach } from 'node:test';
import assert from 'node:assert/strict';

import { installDom, walk, type El } from './fakedom.test';

installDom();

// eslint-disable-next-line @typescript-eslint/no-var-requires
const { AddFormView } = require('./addform') as typeof import('./addform');

import { ADD_KINDS } from '../shared/protocol';
import type { AddKind, WebviewMessage } from '../shared/protocol';

let sent: WebviewMessage[] = [];

function mount() {
  sent = [];
  const view = new AddFormView({ send: (m: WebviewMessage) => sent.push(m) });
  return { view, root: view.element as unknown as El };
}

/** Every element carrying a `data-field`, by its key. */
function fields(root: El): Map<string, El> {
  const out = new Map<string, El>();
  for (const e of walk(root)) {
    if (e.dataset.field !== undefined) {
      out.set(e.dataset.field, e);
    }
  }
  return out;
}

function kindButton(root: El, kind: string): El {
  const found = walk(root).find((e) => e.dataset.kind === kind);
  assert.ok(found, `no control for the kind ${kind}`);
  return found;
}

function press(el: El, key: string): void {
  el.fire('keydown', { key, preventDefault: () => {} });
}

function click(el: El): void {
  el.fire('click', { preventDefault: () => {} });
}

beforeEach(() => {
  (globalThis as any).document.activeElement = null;
});

describe('the add control offers every kind the core can declare', () => {
  // MUTATION: the kind loop replaced by a single `service` button. The composer
  // still opens, a service still goes in, and story 7.4 is silently gone.
  it('draws one control per kind, from the shared list', () => {
    const { root } = mount();
    const drawn = walk(root)
      .filter((e) => e.dataset.kind !== undefined)
      .map((e) => e.dataset.kind);
    assert.deepEqual(drawn, [...ADD_KINDS]);
  });

  it('names what it is for, and starts unpressed', () => {
    const { root } = mount();
    const toggle = walk(root).find((e) => e.dataset.control === 'add');
    assert.ok(toggle, 'there is no control to open the composer');
    assert.equal(toggle!.getAttribute('aria-pressed'), 'false');
    assert.match(toggle!.getAttribute('aria-label') ?? '', /service/);
    assert.match(toggle!.getAttribute('aria-label') ?? '', /secret/);
  });

  it('keeps the composer shut until it is opened', () => {
    const { root } = mount();
    const composer = walk(root).find((e) => e.classList.contains('add-composer'));
    assert.equal(composer!.hidden, true, 'the composer is open before anyone asked for it');
  });
});

describe('what the reader types is what is sent', () => {
  // MUTATION: `kind: this.kind` → `kind: 'service'`. Declaring a network sends
  // a service, and the core plans `services.frontend` — a confident wrong write
  // that a test asserting "a message was sent" cannot see.
  it('sends the kind that is selected, for every kind', () => {
    for (const kind of ADD_KINDS as AddKind[]) {
      const { view, root } = mount();
      view.openFor(kind);
      const name = fields(root).get('add:name')!;
      name.value = 'thing';
      if (kind === 'service') {
        fields(root).get('add:image')!.value = 'redis:7';
      }
      press(name, 'Enter');
      assert.equal(sent.length, 1, `declaring a ${kind} sent ${sent.length} messages`);
      assert.deepEqual(sent[0], {
        type: 'add',
        kind,
        name: 'thing',
        value: kind === 'service' ? 'redis:7' : '',
      });
    }
  });

  it('sends the kind the reader clicked, not the one it opened on', () => {
    const { view, root } = mount();
    view.openFor('service');
    click(kindButton(root, 'volume'));
    const name = fields(root).get('add:name')!;
    name.value = 'pgdata';
    press(name, 'Enter');
    assert.deepEqual(sent, [{ type: 'add', kind: 'volume', name: 'pgdata', value: '' }]);
  });

  // The value field belongs to a service and to nothing else: a network with an
  // image field is an offer to write `frontend: bridge`, which declares nothing.
  it('offers a value field for a service and for no other kind', () => {
    const { view, root } = mount();
    view.openFor('service');
    assert.equal(fields(root).get('add:image')!.hidden, false);
    click(kindButton(root, 'network'));
    assert.equal(fields(root).get('add:image')!.hidden, true);
  });

  // A service typed with an image the reader means to be a string — `3.10` —
  // travels EXACTLY as typed. The webview does not quote it, and it does not
  // refuse it either: the core owns that refusal and its sentence.
  it('does not quote, trim to nothing, or otherwise improve the value', () => {
    const { view, root } = mount();
    view.openFor('service');
    fields(root).get('add:name')!.value = 'cache';
    const image = fields(root).get('add:image')!;
    image.value = '3.10';
    press(image, 'Enter');
    assert.deepEqual(sent, [{ type: 'add', kind: 'service', name: 'cache', value: '3.10' }]);
  });

  // Enter in the IMAGE field means the same thing as Enter in the name: the
  // reader has finished. Without it, typing the image and pressing Enter did
  // nothing at all.
  it('submits from either field', () => {
    const { view, root } = mount();
    view.openFor('service');
    fields(root).get('add:name')!.value = 'cache';
    fields(root).get('add:image')!.value = 'redis:7';
    press(fields(root).get('add:image')!, 'Enter');
    assert.equal(sent.length, 1);
  });

  it('sends nothing at all for an empty form, and closes', () => {
    const { view, root } = mount();
    view.openFor('service');
    press(fields(root).get('add:name')!, 'Enter');
    assert.deepEqual(sent, [], 'an empty form asked the core to plan nothing');
    const composer = walk(root).find((e) => e.classList.contains('add-composer'));
    assert.equal(composer!.hidden, true);
  });

  // A service with no image IS sent: whether that is allowed is the core's
  // answer, with a sentence of its own, and deciding it here would be a second
  // answer that disagrees the first time the core's rule changes.
  it('sends a service with no image rather than deciding for the core', () => {
    const { view, root } = mount();
    view.openFor('service');
    fields(root).get('add:name')!.value = 'cache';
    press(fields(root).get('add:name')!, 'Enter');
    assert.deepEqual(sent, [{ type: 'add', kind: 'service', name: 'cache', value: '' }]);
  });

  it('closes on Escape without sending anything', () => {
    const { view, root } = mount();
    view.openFor('service');
    fields(root).get('add:name')!.value = 'cache';
    press(fields(root).get('add:name')!, 'Escape');
    assert.deepEqual(sent, []);
    const composer = walk(root).find((e) => e.classList.contains('add-composer'));
    assert.equal(composer!.hidden, true);
  });

  it('forgets what was half-typed when it closes', () => {
    const { view, root } = mount();
    view.openFor('service');
    fields(root).get('add:name')!.value = 'cache';
    press(fields(root).get('add:name')!, 'Escape');
    view.openFor('service');
    assert.equal(fields(root).get('add:name')!.value, '', 'a name the reader abandoned came back');
  });
});

describe('the composer says what will happen before it happens', () => {
  it('tells a service from a resource in words', () => {
    const { view, root } = mount();
    view.openFor('service');
    const note = () => walk(root).find((e) => e.classList.contains('add-note'))!.textContent;
    assert.match(note(), /image/);
    click(kindButton(root, 'network'));
    assert.match(note(), /nothing under it/);
    assert.match(note(), /no default driver/);
  });

  it('says nothing is written until the reader saves, whichever kind is chosen', () => {
    const { view, root } = mount();
    for (const kind of ADD_KINDS as AddKind[]) {
      view.openFor(kind);
      const note = walk(root).find((e) => e.classList.contains('add-note'))!.textContent;
      assert.match(note, /Nothing is written until you save/, `the ${kind} note does not say so`);
    }
  });
});
