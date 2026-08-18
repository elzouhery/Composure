// Semantic grouping, and the property that makes a map of key names safe.
//
// The objection to grouping by a table of key names is real and is written down
// in webview/groups.ts: a hand-written list falls behind the specification and
// starts putting new keys nowhere. The answer is not to trust the list — it is
// to make "nowhere" impossible, and then to check that.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { GROUP_OF, GROUP_ORDER, OTHER, groupFields, groupFor } from './groups';
import type { SchemaField } from '../shared/protocol';

function field(key: string, declared = false): SchemaField {
  return { key, path: `services.db.${key}`, declared, support: 'unknown' };
}

const declaredOnly = (f: SchemaField): boolean => f.declared;

describe('grouping never loses a key', () => {
  it('returns every field it was given, exactly once', () => {
    const fields = Object.keys(GROUP_OF).map((k) => field(k, k.length % 2 === 0));
    const groups = groupFields(fields, declaredOnly);
    const back = groups.flatMap((g) => [...g.fields, ...g.available]);
    assert.equal(back.length, fields.length, 'a field was dropped or duplicated');
    assert.deepEqual(
      back.map((f) => f.key).sort(),
      fields.map((f) => f.key).sort(),
    );
  });

  // The failure mode the structural rule was guarding against: the Compose
  // specification adds a key and the map has never heard of it.
  it('shows a key it has never heard of rather than dropping it', () => {
    const unknown = ['quantum_limits', 'zzz_new_key', 'not_in_any_table'];
    for (const key of unknown) {
      assert.equal(GROUP_OF[key], undefined, `${key} is in the map, so it proves nothing here`);
      assert.equal(groupFor(key), OTHER);
    }
    const fields = [field('image', true), ...unknown.map((k) => field(k))];
    const groups = groupFields(fields, declaredOnly);
    const other = groups.find((g) => g.label === OTHER);
    assert.ok(other, 'unknown keys reached no group at all');
    assert.deepEqual(other!.available.map((f) => f.key), unknown);
  });

  it('gives every group its own available list rather than one at the foot', () => {
    // 5.2: "each group ends with the keys the schema permits HERE". One list at
    // the foot of the pane contains the same keys and teaches nothing.
    const fields = [
      field('image', true),
      field('pull_policy'),
      field('healthcheck'),
      field('restart'),
      field('ports', true),
      field('expose'),
    ];
    const groups = groupFields(fields, declaredOnly);
    const byLabel = new Map(groups.map((g) => [g.label, g]));
    assert.deepEqual(byLabel.get('image')!.available.map((f) => f.key), ['pull_policy']);
    assert.deepEqual(byLabel.get('health')!.available.map((f) => f.key), ['healthcheck', 'restart']);
    assert.deepEqual(byLabel.get('network')!.available.map((f) => f.key), ['expose']);
    // Not one big list: the health keys are not offered under `image`.
    assert.equal(byLabel.get('image')!.available.some((f) => f.key === 'healthcheck'), false);
  });

  it('groups the keys the mockup names, under the headings it names', () => {
    // Direction A shows `Image`, `Environment` and `Health`. These three are
    // the ones the design is explicit about, so they are the ones pinned.
    assert.equal(groupFor('image'), 'image');
    assert.equal(groupFor('environment'), 'environment');
    assert.equal(groupFor('healthcheck'), 'health');
  });

  it('prints groups in the declared reading order, with other last', () => {
    const fields = [field('mystery'), field('healthcheck'), field('image'), field('ports')];
    const labels = groupFields(fields, declaredOnly).map((g) => g.label);
    assert.deepEqual(labels, ['image', 'network', 'health', OTHER]);
    assert.equal(labels[labels.length - 1], OTHER, 'an uncategorised key interrupts the middle');
  });

  it('keeps file order inside a group', () => {
    const fields = [field('ports', true), field('expose', true), field('networks', true)];
    const groups = groupFields(fields, declaredOnly);
    assert.deepEqual(groups[0].fields.map((f) => f.key), ['ports', 'expose', 'networks']);
  });

  it('renders whatever the caller says is rendered, not only what is declared', () => {
    // An opened key is a field with the cursor in it, so it has to leave the
    // available list even though the file does not declare it.
    const fields = [field('restart'), field('healthcheck')];
    const groups = groupFields(fields, (f) => f.key === 'restart');
    const health = groups.find((g) => g.label === 'health')!;
    assert.deepEqual(health.fields.map((f) => f.key), ['restart']);
    assert.deepEqual(health.available.map((f) => f.key), ['healthcheck']);
  });

  it('names every group it can produce in the printing order', () => {
    // A group with no place in GROUP_ORDER would print after `other`, which is
    // where uncategorised keys go — so it would look like a bug rather than a
    // heading.
    const named = new Set(Object.values(GROUP_OF));
    for (const label of named) {
      assert.ok(GROUP_ORDER.includes(label), `${label} is assigned to keys but never printed in order`);
    }
    assert.ok(GROUP_ORDER.includes(OTHER));
  });
});
