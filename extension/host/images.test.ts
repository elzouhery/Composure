// The decisions the image half makes, isolated from `vscode` so they can be
// driven.
//
// NOTHING HERE OPENS A SOCKET. These are pure functions over shapes; the one
// module that talks to Docker Hub is the Go core, and `panelbehaviour.test.ts`
// drives that half against a fake session whose `image/lookup` can be made to
// answer late, answer badly or never answer at all.

import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import {
  IMAGE_LOOKUP_TIMEOUT_MS,
  imageLookupKeys,
  isSayable,
  readLookup,
  readSearch,
  stageLookupKey,
} from './images';
import type { SchemaField } from '../shared/protocol';

const field = (key: string, path: string, text: string): SchemaField => ({
  key,
  path,
  support: 'unknown',
  declared: true,
  value: {
    kind: 'scalar',
    text,
    env_known: true,
    origin: { file: '/tmp/compose.yml', line: 1, column: 1, step: 0 },
    overrides: [],
  },
});

describe('which values are worth asking Docker Hub about', () => {
  it('finds the image a service declares', () => {
    const keys = imageLookupKeys([
      field('image', 'services.web.image', 'postgres:16-alpine'),
      field('restart', 'services.web.restart', 'always'),
    ]);
    assert.deepEqual(keys, [{ key: 'services.web.image', ref: 'postgres:16-alpine' }]);
  });

  // A key called `image` that is not THE image key. `build.image` does not
  // exist today, but the walk is recursive and a rule that matched on the last
  // segment alone would look up whatever a nested `image:` happened to hold.
  it('asks about nothing but a value that looks like an image reference', () => {
    const keys = imageLookupKeys([
      field('image', 'services.web.image', ''),
      field('container_name', 'services.web.container_name', 'web'),
    ]);
    assert.deepEqual(keys, []);
  });

  // The lookup costs a request against a shared rate limit. A pane with nothing
  // to ask about must ask nothing at all.
  it('asks nothing when no field holds an image', () => {
    assert.deepEqual(imageLookupKeys([field('restart', 'services.web.restart', 'always')]), []);
    assert.deepEqual(imageLookupKeys(undefined), []);
  });

  it('keys a Dockerfile stage the way staging already keys it', () => {
    // The pill stages `set_base_image` under this key. Two spellings of it
    // would let a pill and its own staged edit address different things.
    assert.equal(stageLookupKey(0), 'stage:0');
    assert.equal(stageLookupKey(3), 'stage:3');
  });
});

describe('reading the core’s answer', () => {
  it('refuses an answer that is not shaped like a lookup', () => {
    for (const bad of [null, 42, 'ok', {}, { state: 'ok' }, { state: 7, message: 'x' }]) {
      assert.equal(readLookup(bad), null, `accepted ${JSON.stringify(bad)}`);
    }
  });

  it('refuses a state this build does not know', () => {
    // A core from the future with a seventh state. Rendering its message under
    // a pill we do not understand is worse than rendering nothing.
    assert.equal(readLookup({ state: 'quantum', message: 'x', reference: 'a' }), null);
  });

  it('accepts a well-formed one', () => {
    const l = readLookup({
      reference: 'postgres:16-alpine',
      repository: 'library/postgres',
      display: 'postgres',
      tag: '16-alpine',
      state: 'ok',
      message: 'postgres:18-alpine is a major upgrade in the same family.',
      pill: 'postgres:18-alpine · major · 3MB larger',
      age: '14 months old',
      candidate: { reference: 'postgres:18-alpine', tag: '18-alpine', kind: 'major' },
    });
    assert.ok(l);
    assert.equal(l.state, 'ok');
    assert.equal(l.candidate?.reference, 'postgres:18-alpine');
  });

  // A pill with no candidate is a button that stages nothing. The state says
  // `ok` and there is nothing to offer, so the answer is not usable.
  it('refuses an ok answer with no candidate to stage', () => {
    assert.equal(
      readLookup({ reference: 'a', state: 'ok', message: 'm', pill: 'x' }),
      null,
    );
  });

  it('refuses a search answer that is not shaped like one', () => {
    for (const bad of [null, {}, { state: 'ok' }, { state: 'ok', message: '', results: 'no' }]) {
      assert.equal(readSearch(bad), null, `accepted ${JSON.stringify(bad)}`);
    }
    const ok = readSearch({
      query: 'postgres',
      state: 'ok',
      message: '',
      results: [{ name: 'postgres', stars: 1, official: true }],
    });
    assert.ok(ok);
    assert.equal(ok.results.length, 1);
  });

  // A result with no name cannot be chosen, and one that arrives among good
  // ones must not take the whole list down with it.
  it('drops a nameless result and keeps the rest', () => {
    const ok = readSearch({
      query: 'p',
      state: 'ok',
      message: '',
      results: [{ stars: 1, official: false }, { name: 'postgres', stars: 2, official: true }],
    });
    assert.ok(ok);
    assert.deepEqual(ok.results.map((r) => r.name), ['postgres']);
  });
});

describe('what is worth saying to a reader', () => {
  // `cancelled` means the reader moved on. Saying anything about it would be
  // telling them about a question they stopped asking.
  it('says nothing about a cancelled lookup', () => {
    assert.equal(isSayable('cancelled'), false);
  });

  // Every other state has a sentence and the sentence is worth having: the
  // three failures because silence reads as "there is nothing newer", and the
  // rest because they are answers.
  it('says something about every other state', () => {
    for (const state of [
      'ok',
      'current',
      'offline',
      'rate-limited',
      'not-found',
      'other-registry',
      'not-comparable',
      'disabled',
    ] as const) {
      assert.equal(isSayable(state), true, `${state} would be silent`);
    }
  });
});

describe('the bound on a question nobody has to wait for', () => {
  it('is shorter than the pane’s own request timeout', () => {
    // The inspector's own requests get REQUEST_TIMEOUT_MS. This one must give
    // up sooner: it is optional, it is not in the render path, and a socket
    // held open for thirty seconds on a dead network is thirty seconds of a
    // request slot spent on a fact nobody is waiting for.
    assert.ok(
      IMAGE_LOOKUP_TIMEOUT_MS > 0 && IMAGE_LOOKUP_TIMEOUT_MS <= 12000,
      `IMAGE_LOOKUP_TIMEOUT_MS = ${IMAGE_LOOKUP_TIMEOUT_MS}`,
    );
  });
});
