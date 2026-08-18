// The inspector's host half: reading the core's answers, and joining a finding
// to the thing it is about.
//
// Both halves fail silently if they are wrong. A schema answer that is not
// checked renders as a service with nothing set — indistinguishable from the
// truth. A join that is wrong puts a badge on a node whose pane shows no
// problem, which reads as a rendering bug rather than as a wrong answer.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  MalformedSchemaError,
  findingKey,
  findingsFor,
  findingsForField,
  findingsForNode,
  pathWithin,
  readReport,
  readSchema,
  severityCounts,
  fieldsNeedingChildren,
  looksLikeMapping,
  editablePaths,
} from './inspect';
import { clampSplit } from './viewstate';
import type { Finding, Origin, SchemaField } from '../shared/protocol';

const origin = (line: number, file = 'compose.yaml'): Origin => ({
  file,
  line,
  column: 5,
  step: 0,
});

function field(key: string, declared: boolean, extra: Partial<SchemaField> = {}): SchemaField {
  return {
    key,
    path: `services.db.${key}`,
    declared,
    support: 'unknown',
    ...extra,
  };
}

function schemaAnswer(fields: SchemaField[] = [field('image', true)]): unknown {
  return {
    path: 'compose.yaml',
    schema_commit: '4e2fe7602af8c965ab4fef891e9dde9c5940775f',
    compose_version: '2.29.0',
    compose_version_known: true,
    files: [{ path: 'compose.yaml', step: 0 }],
    profiles: [],
    node: {
      path: 'services.db',
      schema: 'service',
      known: true,
      fields,
      declared_count: fields.filter((f) => f.declared).length,
      available_count: fields.filter((f) => !f.declared).length,
    },
  };
}

function finding(
  rule: string,
  severity: Finding['severity'],
  subjects: string[],
  anchors: string[],
): Finding {
  return {
    rule,
    severity,
    title: rule,
    message: `${rule} happened`,
    subjects,
    anchors: anchors.map((path, i) => ({ label: 'here', path, origin: origin(10 + i) })),
  };
}

describe('readSchema', () => {
  it('accepts the shape the wire promises', () => {
    const schema = readSchema(schemaAnswer());
    assert.equal(schema.node?.path, 'services.db');
    assert.equal(schema.node?.fields.length, 1);
    assert.equal(schema.compose_version_known, true);
  });

  it('refuses an answer that does not say which specification it came from', () => {
    // AD-20: the available list is a claim about what is possible. A claim
    // whose source is unknown is not one the inspector should present.
    const raw = schemaAnswer() as Record<string, unknown>;
    delete raw.schema_commit;
    assert.throws(() => readSchema(raw), MalformedSchemaError);
  });

  it('refuses an answer with no node rather than rendering an empty pane', () => {
    const raw = schemaAnswer() as Record<string, unknown>;
    delete raw.node;
    assert.throws(() => readSchema(raw), MalformedSchemaError);
  });

  it('refuses a field that does not say whether it is declared', () => {
    // The whole product is the declared / not-declared split. A field that
    // does not say would render as available and teach the reader to add a key
    // that is already there.
    const raw = schemaAnswer() as { node: { fields: unknown[] } };
    raw.node.fields = [{ key: 'image', path: 'services.db.image', support: 'unknown' }];
    assert.throws(() => readSchema(raw), MalformedSchemaError);
  });

  it('refuses a missing files array rather than reporting a stack with no sources', () => {
    const raw = schemaAnswer() as Record<string, unknown>;
    delete raw.files;
    assert.throws(() => readSchema(raw), MalformedSchemaError);
  });
});

describe('readReport', () => {
  it('accepts a report', () => {
    const rep = readReport({
      path: 'compose.yaml',
      profiles: [],
      findings: [finding('r', 'error', ['services.db'], ['services.db.ports[0]'])],
    });
    assert.equal(rep.findings.length, 1);
  });

  it('refuses a finding with no position', () => {
    // AD-7 is enforced in Go. A finding that arrives without one could be
    // placed neither on a field nor in the problems panel.
    assert.throws(
      () =>
        readReport({
          findings: [{ rule: 'r', severity: 'error', title: 't', message: 'm', subjects: [], anchors: [] }],
        }),
      MalformedSchemaError,
    );
  });

  it('refuses a result with no findings array', () => {
    assert.throws(() => readReport({ path: 'compose.yaml' }), MalformedSchemaError);
  });
});

describe('pathWithin', () => {
  it('matches a path and everything inside it', () => {
    assert.ok(pathWithin('services.web', 'services.web'));
    assert.ok(pathWithin('services.web.image', 'services.web'));
    assert.ok(pathWithin('services.web.ports[0]', 'services.web'));
  });

  it('does not let one service swallow another with a longer name', () => {
    // The bug this exists to prevent: `services.web` matching
    // `services.webhook` by bare prefix, so every webhook finding shows up on
    // the web service's pane and its node badge.
    assert.equal(pathWithin('services.webhook.image', 'services.web'), false);
    assert.equal(pathWithin('services.webhook', 'services.web'), false);
  });

  it('treats the stack as containing everything', () => {
    assert.ok(pathWithin('services.web.image', ''));
  });
});

describe('findingsFor', () => {
  const findings = [
    finding('collision', 'error', ['services.a', 'services.b'], ['services.a.ports[0]', 'services.b.ports[0]']),
    finding('credential', 'hint', ['services.a'], ['services.a.environment.PASSWORD']),
    finding('unused', 'warning', ['volumes.scratch'], ['volumes.scratch']),
  ];

  it('gives both ends of a collision to both services', () => {
    assert.equal(findingsFor(findings, 'services.a').length, 2);
    assert.equal(findingsFor(findings, 'services.b').length, 1);
  });

  it('gives the stack everything', () => {
    assert.equal(findingsFor(findings, null).length, 3);
  });

  it('puts a finding on the field that caused it', () => {
    const onField = findingsForField(findings, 'services.a.environment');
    assert.equal(onField.length, 1);
    assert.equal(onField[0].rule, 'credential');
  });

  it('keeps a node-level finding off every field', () => {
    const nodeLevel = finding('cycle', 'error', ['services.a'], ['services.a']);
    const fields = ['services.a.image', 'services.a.environment'];
    const left = findingsForNode([nodeLevel, findings[1]], 'services.a', fields);
    assert.deepEqual(
      left.map((f) => f.rule),
      ['cycle'],
      'the credential finding belongs to the environment field, not to the head of the pane',
    );
  });

  it('gives every finding a stable identity', () => {
    // Was: `findingKey(findings[0]) === findingKey(findings[0])` — f(x) === f(x)
    // for a pure function, which holds for `() => ''` as readily as for a real
    // key. Stability means the key survives a REBUILT finding with the same
    // content, which is what a re-diagnose produces.
    const rebuilt = JSON.parse(JSON.stringify(findings[0])) as typeof findings[0];
    assert.notEqual(rebuilt, findings[0], 'the fixture was not actually rebuilt');
    assert.equal(findingKey(rebuilt), findingKey(findings[0]));
    // And it must distinguish: two findings that differ only in position are
    // two findings, or the inspector renders one and drops the other.
    assert.notEqual(findingKey(findings[0]), findingKey(findings[1]));
    const moved = { ...findings[0], anchors: [{ ...findings[0].anchors[0], origin: { ...findings[0].anchors[0].origin, line: 99 } }] };
    assert.notEqual(findingKey(moved), findingKey(findings[0]));
  });
});

describe('severityCounts', () => {
  const findings = [
    finding('a', 'error', ['services.web'], ['services.web.ports[0]']),
    finding('b', 'warning', ['services.web'], ['services.web.depends_on.api']),
    finding('c', 'hint', ['services.web'], ['services.web.environment.PW']),
    finding('d', 'warning', ['volumes.data'], ['volumes.data']),
  ];

  it('counts by severity, per node', () => {
    const counts = severityCounts(findings, ['services.web', 'volumes.data', 'services.api']);
    assert.deepEqual(counts['services.web'], { error: 1, warning: 1, hint: 1 });
    assert.deepEqual(counts['volumes.data'], { error: 0, warning: 1, hint: 0 });
  });

  it('gives a clean node no entry at all, so it carries no badge', () => {
    const counts = severityCounts(findings, ['services.api']);
    assert.equal(counts['services.api'], undefined);
  });

  it('never counts against a node that is not in the graph', () => {
    const counts = severityCounts(findings, ['services.web']);
    assert.deepEqual(Object.keys(counts), ['services.web']);
  });
});

describe('clampSplit', () => {
  it('keeps both panes reachable', () => {
    // A pane dragged to nothing cannot be dragged back, and DESIGN.md says
    // both are permanently visible.
    assert.equal(clampSplit(0), 0.25);
    assert.equal(clampSplit(1), 0.85);
    assert.equal(clampSplit(0.5), 0.5);
  });

  it('survives a value that is not a number', () => {
    assert.equal(clampSplit(Number.NaN), 0.6);
  });
});

describe('fieldsNeedingChildren — the host half of the add-attribute fix', () => {
  const f = (key: string, over: Partial<SchemaField> = {}): SchemaField =>
    ({ key, path: `services.db.${key}`, declared: false, support: 'unknown', ...over });
  const nullValue = { kind: 'null' as const, text: '', env_known: true, origin: { file: 'c.yaml', line: 1, column: 1, step: 0 }, overrides: [] };

  it('asks for the children of a mapping the reader opened', () => {
    // Without this the pane renders `healthcheck` as a box to type a value
    // into, and the reader never reaches `test` or `interval` — which is the
    // dead end the whole change exists to remove.
    const fields = [f('healthcheck', { type: 'object' }), f('restart', { type: 'string' })];
    const wanted = fieldsNeedingChildren(fields, (p) => p === 'services.db.healthcheck');
    assert.deepEqual(wanted.map((f) => f.key), ['healthcheck']);
  });

  it('asks for the children of a key the file declares with no value', () => {
    // What a save produces: `healthcheck:` alone on a line.
    const fields = [f('healthcheck', { type: 'object', declared: true, value: nullValue })];
    assert.deepEqual(
      fieldsNeedingChildren(fields, () => false).map((f) => f.key),
      ['healthcheck'],
    );
  });

  it('does not ask for a scalar, which has no children to show', () => {
    const fields = [f('restart', { type: 'string' }), f('image', { type: 'string' })];
    assert.deepEqual(fieldsNeedingChildren(fields, () => true), []);
  });

  it('does not ask again for a mapping the core already walked', () => {
    const fields = [
      f('healthcheck', { type: 'object', declared: true, children: [f('test')] }),
    ];
    assert.deepEqual(fieldsNeedingChildren(fields, () => true), []);
  });

  it('reaches a mapping nested inside another one', () => {
    const fields = [
      f('deploy', {
        type: 'object',
        declared: true,
        children: [{ ...f('resources', { type: 'object' }), path: 'services.db.deploy.resources' }],
      }),
    ];
    assert.deepEqual(
      fieldsNeedingChildren(fields, (p) => p === 'services.db.deploy.resources').map((f) => f.key),
      ['resources'],
    );
  });

  it('reads a union type as a mapping, the way the spec renders one', () => {
    assert.equal(looksLikeMapping(f('build', { type: 'object or string' })), true);
    assert.equal(looksLikeMapping(f('test', { type: 'array or string' })), false);
  });
});


/* -------------------------------------------------------------------------
 * Epic 9, story 9.2 — the address of a list entry.
 *
 * `internal/schema`'s wire shape gives a sequence entry no path of its own, so
 * the address has to be CONSTRUCTED. It is constructed in exactly one place —
 * `entryPath` in shared/protocol.ts — and both the host and the webview call
 * it, because two spellings of `command[2]` would be two answers about which
 * bytes an edit lands on.
 * ---------------------------------------------------------------------- */

describe('the paths the pane has to know the editability of — story 9.2', () => {
  const seq = (items: string[]): SchemaField => ({
    key: 'command',
    path: 'services.db.command',
    declared: true,
    support: 'unknown',
    value: {
      kind: 'sequence',
      text: '',
      env_known: true,
      origin: origin(9),
      overrides: [],
      seq: items.map((t) => ({
        kind: 'scalar' as const,
        text: t,
        env_known: true,
        origin: origin(9),
        overrides: [],
      })),
    },
  });

  it('asks about every entry of a sequence, by index', () => {
    // Repeats on purpose: a list of distinct entries cannot tell an off-by-one
    // from a correct answer.
    assert.deepEqual(editablePaths([seq(['sh', '-c', 'sh'])], []), [
      'services.db.command',
      'services.db.command[0]',
      'services.db.command[1]',
      'services.db.command[2]',
    ]);
  });

  it('does not invent an index for an entry that is not a scalar', () => {
    const field = seq([]);
    field.value!.seq = [
      { kind: 'mapping', text: '', env_known: true, origin: origin(9), overrides: [], entries: [] },
    ];
    assert.deepEqual(editablePaths([field], []), ['services.db.command']);
  });

  /* A free-form mapping now carries a control of its own — the add-a-key
   * composer — for exactly the reason a sequence does: the pane must not offer
   * one on a mapping the file does not write here. Without the mapping's own
   * path in this list the answer never arrives, `availability` is silent, and
   * the pane treats silence as "ordinary" and offers the composer on a mapping
   * that arrives whole through `<<: *defaults`. */
  const map = (key: string, extra: Partial<SchemaField> = {}): SchemaField => ({
    key,
    path: `services.db.${key}`,
    declared: true,
    support: 'unknown',
    value: {
      kind: 'mapping',
      text: '',
      env_known: true,
      origin: origin(9),
      overrides: [],
      entries: [
        {
          key: 'A',
          key_origin: origin(10),
          path: `services.db.${key}.A`,
          value: { kind: 'scalar', text: '1', env_known: true, origin: origin(10), overrides: [] },
        },
      ],
    },
    ...extra,
  });

  it('asks about a free-form mapping’s own path, and not a described one’s', () => {
    assert.deepEqual(editablePaths([map('environment', { free_form: true })], []), [
      'services.db.environment',
      'services.db.environment.A',
    ]);
    // `healthcheck` has a closed key set, no composer, and therefore no
    // question to ask about the container itself.
    assert.deepEqual(editablePaths([map('healthcheck')], []), ['services.db.healthcheck.A']);
  });
});
