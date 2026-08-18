// The inspector's arithmetic and its wording.
//
// A webview cannot be launched under `node --test`, so what is testable is
// what was extracted: every sentence the reader reads, and every decision
// about what they see. That is deliberate — the rendering below these
// functions is element creation, and the parts that can be WRONG rather than
// merely ugly all live here.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import {
  defaultIsProse,
  defaultValue,
  groupsOf,
  headerSummary,
  isObjectTyped,
  placeholderFor,
  severityScope,
  severitySummary,
  provenanceOf,
  severityWord,
  shortFile,
  sourceLine,
  supportNote,
} from './inspector';
import { severityBadge } from './layout';
import type { Inspection, Origin, SchemaField, ValueView } from '../shared/protocol';

const origin = (line: number, file = 'compose.yaml'): Origin => ({ file, line, column: 5, step: 0 });

function scalar(text: string, line: number, extra: Partial<ValueView> = {}): ValueView {
  return {
    kind: 'scalar',
    text,
    env_known: true,
    origin: origin(line),
    overrides: [],
    ...extra,
  };
}

function field(key: string, declared: boolean, extra: Partial<SchemaField> = {}): SchemaField {
  return { key, path: `services.db.${key}`, declared, support: 'unknown', ...extra };
}

function inspectionOf(node: Partial<Inspection['schema']['node']> & object): Inspection {
  return {
    id: 'services.db',
    name: 'db',
    kind: 'service',
    findings: [],
    staged: [],
    opened: [],
    pending: {},
    schema: {
      path: '/w/compose.yaml',
      schema_commit: '4e2fe7602af8c965ab4fef891e9dde9c5940775f',
      compose_version: '2.29.0',
      compose_version_known: true,
      files: [{ path: '/w/compose.yaml', step: 0 }],
      profiles: [],
      node: {
        path: 'services.db',
        schema: 'service',
        known: true,
        fields: [],
        declared_count: 6,
        available_count: 87,
        ...node,
      },
    },
  };
}

describe('shortFile', () => {
  it('names a file the way a reader would', () => {
    assert.equal(shortFile('/home/rana/work/policy/compose.yml'), 'policy/compose.yml');
    assert.equal(shortFile('compose.yml'), 'compose.yml');
    assert.equal(shortFile('C:\\work\\policy\\compose.yml'), 'policy/compose.yml');
  });
});

describe('provenanceOf — story 5.3', () => {
  it('gives file and line for a value set once', () => {
    const p = provenanceOf(scalar('nginx', 12));
    assert.ok(p);
    assert.equal(p.label, 'compose.yaml:12');
    assert.equal(p.line, 12);
    assert.deepEqual(p.overrides, []);
  });

  it('reads `compose.yaml:12 · overrides :7` when it replaced something', () => {
    const p = provenanceOf(
      scalar('nginx', 12, { overrides: [{ value: 'apache', origin: origin(7) }] }),
    );
    assert.ok(p);
    assert.equal(p.label, 'compose.yaml:12');
    assert.equal(p.overrides.length, 1);
    // Same file: the bare line. Repeating the filename in a 40%-wide pane
    // costs the line its readability and says nothing.
    assert.equal(p.overrides[0].label, ':7');
    assert.equal(p.overrides[0].line, 7, 'clicking it must go to the position it replaced');
    assert.equal(p.many, false);
  });

  it('names the other file when the override came from one', () => {
    const p = provenanceOf(
      scalar('nginx', 12, {
        overrides: [{ value: 'apache', origin: origin(7, '/w/compose.override.yml') }],
      }),
    );
    assert.equal(p?.overrides[0].label, 'w/compose.override.yml:7');
  });

  it('marks more than one override, which DESIGN.md colours', () => {
    const p = provenanceOf(
      scalar('nginx', 12, {
        overrides: [
          { value: 'a', origin: origin(7) },
          { value: 'b', origin: origin(9) },
        ],
      }),
    );
    assert.equal(p?.many, true);
  });

  it('returns nothing for a value with no position, rather than a false one', () => {
    assert.equal(provenanceOf(scalar('x', 0)), null);
    assert.equal(provenanceOf({ ...scalar('x', 4), origin: origin(4, '') }), null);
  });
});

describe('severityWord — N6', () => {
  it('says the severity in text, so colour is never carrying it alone', () => {
    assert.equal(severityWord('error'), 'error');
    assert.equal(severityWord('warning'), 'warning');
    assert.equal(severityWord('hint'), 'hint');
  });
});

describe('placeholderFor — story 5.2', () => {
  it('says "not set" plainly, never an em dash or an empty box', () => {
    assert.equal(placeholderFor(field('healthcheck', false)), 'not set');
  });

  it('shows the schema default, so the reader learns what happens if they leave it', () => {
    assert.equal(
      placeholderFor(field('interval', false, { default: '30s', default_source: 'schema' })),
      'not set — defaults to 30s',
    );
  });

  // The defect: `healthcheck.start_interval` carries the default "interval
  // value", which is a DESCRIPTION of a default and not a duration. Worded as
  // "defaults to interval value" it reads as something the reader could accept,
  // and one click used to write it into their file.
  it('words a prose default as prose, never as a value', () => {
    const prose = field('start_interval', false, {
      default: 'interval value',
      default_source: 'description',
    });
    const text = placeholderFor(prose);
    assert.match(text, /the spec describes the default as/);
    assert.doesNotMatch(text, /defaults to interval value/);
  });
});

describe('defaultValue — nothing the schema did not supply as a value', () => {
  it('offers a schema default as a value', () => {
    assert.equal(defaultValue(field('restart', false, { default: 'no', default_source: 'schema' })), 'no');
  });

  it('offers nothing at all for a default the spec only described', () => {
    // This is the whole of defect 2. `start_interval: interval value` is not a
    // duration, Compose would reject it, and nothing warned the reader.
    assert.equal(
      defaultValue(field('start_interval', false, { default: 'interval value', default_source: 'description' })),
      '',
    );
    assert.equal(defaultIsProse(field('start_interval', false, {
      default: 'interval value',
      default_source: 'description',
    })), true);
  });

  it('offers nothing for a key with no default, rather than an empty string to write', () => {
    // Verified against the real core: ZERO of the ninety-one unset top-level
    // service keys carry a default of any kind, so this is the ordinary case
    // and not an edge one. An insert carrying "" writes `key: ` — a trailing
    // space — which is why nothing is staged until the reader types.
    assert.equal(defaultValue(field('healthcheck', false)), '');
    assert.equal(defaultIsProse(field('healthcheck', false)), false);
  });
});

describe('isObjectTyped — what clicking an unset key should do', () => {
  it('reads the schema type, so a mapping opens instead of offering a box to type in', () => {
    assert.equal(isObjectTyped(field('healthcheck', false, { type: 'object' })), true);
    assert.equal(isObjectTyped(field('restart', false, { type: 'string' })), false);
    // The real schema reports unions as prose: `array or string`, `object`.
    assert.equal(isObjectTyped(field('build', false, { type: 'object or string' })), true);
    assert.equal(isObjectTyped(field('test', false, { type: 'array or string' })), false);
  });

  it('trusts declared children over a missing type', () => {
    assert.equal(isObjectTyped(field('deploy', true, { children: [field('replicas', false)] })), true);
    assert.equal(isObjectTyped(field('image', true)), false);
  });
});

describe('severitySummary — the inspector header', () => {
  it('counts in words, pluralised, worst first', () => {
    const f = (severity: 'error' | 'warning' | 'hint') => ({
      rule: 'r',
      severity,
      title: 't',
      message: 'm',
      subjects: [],
      anchors: [],
    });
    assert.equal(severitySummary([f('warning'), f('warning'), f('error')]), '1 error · 2 warnings');
    assert.equal(severitySummary([f('hint')]), '1 hint');
  });

  it('says nothing at all rather than a reassuring zero', () => {
    // "0 problems" is a claim about a stack, and after a failed diagnose it is
    // a claim nobody is in a position to make.
    assert.equal(severitySummary([]), '');
  });
});

describe('severityScope — the header and the problems panel count different things', () => {
  // The owner saw `2 warnings` in this header beside a panel listing three
  // rows and reported a bug. There was none: the header counts FINDINGS for
  // the SELECTION, the panel gets one entry per ANCHOR for the WHOLE STACK,
  // and `host-port-collision` is one finding with two anchors. Neither number
  // can be made into the other without lying — counting anchors here would
  // report one collision as two problems in a pane about one service — so each
  // says what it counts, and this side says it with the exact numbers.
  const collision = {
    rule: 'host-port-collision',
    severity: 'error' as const,
    title: 'Two services publish the same host port',
    message: 'both publish 8080',
    subjects: ['services.web', 'services.api'],
    anchors: [
      { label: 'web', path: 'services.web.ports[0]', origin: origin(8) },
      { label: 'api', path: 'services.api.ports[0]', origin: origin(12) },
    ],
  };

  it('states the unit, the scope and the exact position count', () => {
    const note = severityScope([collision], 'web');
    assert.match(note, /^1 finding in web, at 2 positions\./);
    assert.match(note, /Problems panel lists one entry per position/);
    assert.match(note, /whole stack/);
  });

  it('pluralises both counts independently', () => {
    const single = { ...collision, anchors: [collision.anchors[0]] };
    assert.match(severityScope([single], 'web'), /^1 finding in web, at 1 position\./);
    assert.match(severityScope([collision, single], 'web'), /^2 findings in web, at 3 positions\./);
  });

  it('counts only anchors that name a real position, exactly as the panel does', () => {
    // `Problems.publish` skips an anchor with no file or no line, so a header
    // that counted them would explain a divergence that is not there. Both
    // sides call `positionedAnchors`; this is the assertion that they agree.
    const half = {
      ...collision,
      anchors: [collision.anchors[0], { label: 'nowhere', path: 'services.api', origin: origin(0, '') }],
    };
    assert.match(severityScope([half], 'web'), /at 1 position\./);
  });

  it('says nothing when there is nothing to explain', () => {
    assert.equal(severityScope([], 'web'), '');
  });
});

describe('supportNote — AD-21', () => {
  it('marks a key the installed Compose cannot run', () => {
    assert.equal(
      supportNote(field('develop', false, { support: 'no', min_version: '2.22.0' })),
      'needs Compose 2.22.0',
    );
  });

  it('says nothing when no minimum is recorded, and nothing when it is met', () => {
    assert.equal(supportNote(field('develop', false, { support: 'unknown', min_version: '' })), '');
    assert.equal(supportNote(field('develop', false, { support: 'yes', min_version: '2.22.0' })), '');
    // The mark is only ever an annotation; a marked key is still in the list,
    // which is why this function returns text and never a boolean "hide".
    assert.notEqual(supportNote(field('x', false, { support: 'no', min_version: '9.0.0' })), '');
  });
});

describe('groupsOf', () => {
  it('splits declared from available, and gives a declared mapping its own group', () => {
    const fields = [
      field('image', true),
      field('healthcheck', true, { children: [field('interval', true), field('timeout', false)] }),
      field('develop', false),
      field('ulimits', false),
    ];
    const g = groupsOf(fields);
    assert.deepEqual(
      g.lead.map((f) => f.key),
      ['image'],
    );
    assert.deepEqual(
      g.groups.map((f) => f.key),
      ['healthcheck'],
      'a declared mapping is a group so it can carry its own available line',
    );
    assert.deepEqual(
      g.available.map((f) => f.key),
      ['develop', 'ulimits'],
    );
  });

  it('keeps declared fields in file order', () => {
    const g = groupsOf([field('restart', true), field('image', true), field('command', true)]);
    assert.deepEqual(
      g.lead.map((f) => f.key),
      ['restart', 'image', 'command'],
    );
  });

  it('never drops a field', () => {
    const fields = [field('a', true), field('b', false), field('c', true, { children: [field('d', false)] })];
    const g = groupsOf(fields);
    assert.equal(g.lead.length + g.groups.length + g.available.length, fields.length);
  });
});

describe('headerSummary', () => {
  it('spells the two halves out rather than badging them', () => {
    // "6 set · 87 available", never "6/93" — the cryptic-badge anti-pattern
    // the design names explicitly.
    assert.equal(headerSummary(inspectionOf({})), '6 set · 87 available');
  });

  it('says a free-form node permits any key rather than counting to zero', () => {
    assert.equal(
      headerSummary(inspectionOf({ free_form: true, declared_count: 3, available_count: 0 })),
      '3 set · any key permitted',
    );
  });

  it('says when the vendored specification does not describe the path', () => {
    const summary = headerSummary(inspectionOf({ known: false, declared_count: 1, available_count: 0 }));
    assert.match(summary, /does not describe/);
    assert.doesNotMatch(summary, /0 available/, 'nothing known is not the same as nothing available');
  });
});

describe('sourceLine — AD-20 and AD-21, on screen', () => {
  it('names the pinned specification and the Compose the marks were judged against', () => {
    assert.equal(sourceLine(inspectionOf({})), 'spec 4e2fe76 · compose 2.29.0');
  });

  it('says plainly when there is no binary, since that is why nothing is marked', () => {
    const i = inspectionOf({});
    i.schema.compose_version_known = false;
    i.schema.compose_version = '';
    assert.match(sourceLine(i), /no docker compose found/);
  });
});

describe('severityBadge — story 5.4', () => {
  it('counts everything and colours by the worst of it', () => {
    const badge = severityBadge({ error: 1, warning: 2, hint: 0 });
    assert.ok(badge);
    assert.equal(badge.count, 3);
    assert.equal(badge.level, 'error');
    assert.equal(badge.label, '3');
    assert.equal(badge.word, '1 error, 2 warnings');
  });

  it('does not let a warnings-only node look clean', () => {
    const badge = severityBadge({ error: 0, warning: 4, hint: 0 });
    assert.equal(badge?.level, 'warning');
    assert.equal(badge?.count, 4);
  });

  it('gives a clean node no badge at all', () => {
    assert.equal(severityBadge({ error: 0, warning: 0, hint: 0 }), null);
    assert.equal(severityBadge(undefined), null);
  });

  it('keeps a large count inside the circle without losing it from the words', () => {
    const badge = severityBadge({ error: 0, warning: 0, hint: 12 });
    assert.equal(badge?.label, '9+');
    assert.equal(badge?.word, '12 hints');
  });

  it('says the severity in words, so the dot is never the only signal', () => {
    assert.equal(severityBadge({ error: 1, warning: 0, hint: 0 })?.word, '1 error');
  });
});
