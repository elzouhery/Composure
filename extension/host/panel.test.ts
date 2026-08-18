// The host side of the message contract, and AD-19's three defences.
//
// `webview/view.test.ts` asserts that main.ts has a case for every HostMessage
// variant. There was no mirror in the other direction, and the consequence was
// measured: deleting `case 'stage'`, `case 'save'` or `case 'impact'` from
// panel.ts — including the ONLY path in the product that writes a byte — left
// 279 of 279 tests passing. A `switch` over a union with a missing case is legal
// TypeScript, so neither the compiler nor the suite said anything.
//
// This file reads the source rather than driving the class, and what it checks
// is that the handler and the defences are PRESENT — not that they behave.
//
// That used to be the whole story, on the grounds that `vscode` does not exist
// under `node --test`. It does now: `host/panelbehaviour.test.ts` installs a
// stub for that one require and drives StackPanel end to end, so every check
// here that a NEUTRALISING edit could walk past — a `case` kept and its body
// emptied, an early `return` placed above the line this greps for — has a
// behavioural twin over there. What is left here is the sweep: a `switch` over
// a union with a missing case is legal TypeScript, and enumerating the union
// out of the protocol is genuinely a property of the SOURCE. Behaviour over the
// write path is host/realedit.test.ts's job, against the real binary.

import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

const ROOT = [process.cwd(), resolve(process.cwd(), 'extension')].find((b) =>
  existsSync(resolve(b, 'shared/protocol.ts')),
);
assert.ok(ROOT, 'could not locate the extension sources');

const read = (relative: string): string => readFileSync(resolve(ROOT!, relative), 'utf8');
/**
 * Comments stripped. This file asserts things like "the open handler does not
 * stage", and panel.ts EXPLAINS in prose that opening does not stage — so a
 * check run against the raw text matches the explanation and fails on code that
 * is correct. Every assertion below is about code.
 */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');
}

const PANEL = stripComments(read('host/panel.ts'));
const PROTOCOL = read('shared/protocol.ts');

/** The `type:` literals declared in one union of the protocol. */
function variantsOf(union: string, until: string): string[] {
  const start = PROTOCOL.indexOf(union);
  assert.ok(start >= 0, `${union} is no longer declared`);
  const end = PROTOCOL.indexOf(until, start);
  assert.ok(end > start, `could not find the end of ${union}`);
  const body = PROTOCOL.slice(start, end);
  return [...new Set([...body.matchAll(/\btype:\s*'([a-zA-Z]+)'/g)].map((m) => m[1]))];
}

describe('the host handles everything the webview can send', () => {
  it('has a case for every WebviewMessage variant', () => {
    const kinds = variantsOf('export type WebviewMessage', 'export type EditOperation');
    assert.ok(kinds.length >= 15, `only found ${kinds.length} webview message kinds`);
    const unhandled = kinds.filter((k) => !PANEL.includes(`case '${k}':`));
    assert.deepEqual(
      unhandled,
      [],
      `the webview can send these and the host drops them silently:\n  ${unhandled.join('\n  ')}`,
    );
  });

  it('still handles the three whose loss was invisible', () => {
    // Named individually as well as by the sweep above, because these are the
    // ones that were measured: deleting any of them passed the whole suite.
    // `save` is the only message in the product that writes bytes.
    for (const kind of ['save', 'impact', 'open', 'edit', 'unstage', 'discard']) {
      assert.match(
        PANEL,
        new RegExp(`case '${kind}':`),
        `panel.ts no longer handles '${kind}'`,
      );
    }
  });

  it('routes save to the one method that writes, and nothing else to it', () => {
    // A `case 'save'` that fell through to a no-op would satisfy the check
    // above. The write is `apply`, and it is reached from exactly one place.
    const applyCalls = [...PANEL.matchAll(/\bapply\(/g)];
    assert.equal(applyCalls.length, 1, `apply() is called ${applyCalls.length} times; it must be one`);
    // The window is the whole method rather than its first 900 characters:
    // `save` now dispatches past TWO moves — story 9.3's two-file write and
    // story 9.4's build argument — before it reaches the ordinary path, and a
    // character count is a bound on how much dispatch may precede the write
    // rather than a statement about it.
    const save = PANEL.slice(
      PANEL.indexOf('private async save()'),
      PANEL.indexOf('private hasUnwritten'),
    );
    assert.match(save, /await apply\(this\.session, file, ops\)/);
  });

  it('never sends a webview message kind that does not exist', () => {
    // The reverse drift: the host posting a `type` the protocol never declared,
    // which compiles only because the object literal is inferred somewhere
    // loose — and arrives at a webview switch that ignores it.
    const declared = new Set(variantsOf('export type HostMessage', 'export type WebviewMessage'));
    const posted = [...PANEL.matchAll(/this\.post\(\{\s*type:\s*'([a-zA-Z]+)'/g)].map((m) => m[1]);
    assert.ok(posted.length > 5, 'no posts found, so this checks nothing');
    for (const kind of posted) {
      assert.ok(declared.has(kind), `panel.ts posts '${kind}', which HostMessage does not declare`);
    }
  });
});

describe('AD-19: a stale byte range is discarded, never rebased', () => {
  // All three of these defences passed their absence: deleting any one of them
  // left the suite green, so the guarantee rested entirely on the Go core
  // refusing the write. The core refusing is the backstop, not the design —
  // the extension is supposed to not ask.

  it('records the range each edit was staged against', () => {
    // Without `expect`, the core has nothing to compare at write time and every
    // stale write becomes a silent success against a moved range.
    assert.match(
      PANEL,
      /expect:\s*\{\s*start:\s*landed\.range\.start,\s*end:\s*landed\.range\.end,\s*text:\s*landed\.before\s*\}/,
      'a staged edit no longer records the byte range it was staged against',
    );
  });

  it('discards every stage when a save comes back stale', () => {
    const save = PANEL.slice(PANEL.indexOf('private async save()'), PANEL.indexOf('private discardStages'));
    assert.match(
      save,
      /classify\(err\)\s*===\s*'stale'/,
      'save() no longer tells a stale range from any other refusal',
    );
    assert.match(
      save,
      /this\.discardStages\(file,\s*true\)/,
      'a stale save no longer discards the stage — it will be retried against a moved range',
    );
  });

  it('discards every stage when the file changes underneath it', () => {
    const changed = PANEL.slice(
      PANEL.indexOf('private onSourceChanged'),
      PANEL.indexOf('private watchSources'),
    );
    // `hasUnwritten` rather than `staging.count` since story 9.3: a staged move
    // lives outside `Staging` — it writes two files and that store holds
    // operations against one — so a check that asked only `Staging` would let a
    // move be discarded with nothing said.
    assert.match(
      changed,
      /this\.discardStages\(path,\s*this\.hasUnwritten\(path\)\)/,
      'a file changing on disk no longer discards the stages held against it',
    );
    assert.match(
      PANEL,
      /hasUnwritten\(file: string\): boolean \{\s*return \(?\s*this\.staging\.count\(file\) > 0 \|\|\s*this\.extract\?\.file === file \|\|\s*this\.extractArg\?\.file === file/,
      'a staged move is no longer counted as unwritten work',
    );
    assert.match(changed, /this\.draw\(\)/, 'a changed file no longer redraws');
  });

  it('says so when a stage is discarded, rather than dropping it silently', () => {
    // A stage that vanished with nothing said is work the reader thinks they
    // still have.
    assert.match(PANEL, /reason:\s*stale\s*\?\s*STALE_MESSAGE/);
  });
});

describe('a failed diagnose is not a clean bill of health', () => {
  it('distinguishes "no findings" from "could not find out"', () => {
    // The catch used to `return []`, the caller published `[]`, and
    // DiagnosticCollection.clear() emptied the problems panel.
    const diagnose = PANEL.slice(
      PANEL.indexOf('private async diagnose('),
      PANEL.indexOf('private async inspect('),
    );
    assert.match(diagnose, /error:\s*null/, 'diagnose no longer reports success separately');
    assert.match(diagnose, /error:\s*detail/, 'diagnose no longer carries the reason it failed');
    assert.doesNotMatch(
      diagnose,
      /catch[\s\S]*return\s*\[\]/,
      'a failed diagnose returns an empty finding list again, which reads as "nothing wrong"',
    );
  });

  it('publishes a finding saying the checks did not run', () => {
    assert.match(
      PANEL,
      /this\.problems\.unavailable\(file,\s*diagnosis\.error\)/,
      'a failed diagnose no longer tells the problems panel anything',
    );
    // And it must not ALSO publish the empty set, which would clear it again.
    const draw = PANEL.slice(PANEL.indexOf('async draw()'), PANEL.indexOf('private async diagnose('));
    // The `\(` is load-bearing. Without it this is a PREFIX regex, and renaming
    // the call to `this.problems.publishDISABLED?.()` passes it — the twelfth
    // blind check the sweep recorded. The behavioural form, which asks the
    // diagnostic collection what is in it rather than asking the source what it
    // spells, is in host/panelbehaviour.test.ts.
    assert.match(draw, /if \(diagnosis\.error === null\) \{[\s\S]*?this\.problems\.publish\(/);
  });

  it('says it again on every selection, not only on the draw that failed', () => {
    // The warning is posted once when diagnose fails. Selecting a service after
    // that re-renders the pane — with no pills on any field, because the rules
    // never ran. Without this restatement the reader is looking at a clean
    // inspector that means "we do not know", which is the confident wrong
    // answer this codebase is built to refuse.
    //
    // Deleting the restatement left 344 of 344 tests passing: the first post
    // covers the failure itself, and nothing covered the second and later
    // selections.
    const inspect = PANEL.slice(
      PANEL.indexOf('private async inspect('),
      PANEL.indexOf('private recordShapes('),
    );
    assert.match(
      inspect,
      /if \(this\.diagnoseError !== null\)/,
      'inspect() no longer checks whether diagnostics failed before rendering a pane with no pills on it',
    );
    assert.match(
      inspect,
      /this\.post\(\{\s*type:\s*'diagnosticsUnavailable',\s*file,\s*detail:\s*this\.diagnoseError\s*\}\)/,
      'inspect() no longer restates the warning, so changing selection after a failed diagnose loses it',
    );
    // And it must be said BEFORE the inspection it qualifies, not after: the
    // webview renders on `inspection` and the banner would arrive to a pane
    // that has already drawn itself clean.
    assert.ok(
      inspect.indexOf("'diagnosticsUnavailable'") < inspect.indexOf("type: 'inspection'"),
      'the warning is now posted after the inspection it qualifies',
    );
  });
});

describe('an opened mapping is filled in before the pane renders it', () => {
  // `fieldsNeedingChildren` decides WHICH mappings need a second schema request
  // and host/inspect.test.ts covers that decision behaviourally. What it cannot
  // see is whether anything calls it: deleting the call from inspect() left the
  // whole suite green, and the reader got `healthcheck` as a box to type into
  // with no `test` and no `interval` anywhere.

  it('asks the core to describe them, from inside inspect', () => {
    const inspect = PANEL.slice(
      PANEL.indexOf('private async inspect('),
      PANEL.indexOf('private recordShapes('),
    );
    assert.match(
      inspect,
      /await this\.expandOpenMappings\(file, schema\)/,
      'inspect() no longer fetches the children of an opened mapping',
    );
  });

  it('uses the shared decision rather than a second copy of it', () => {
    const expand = PANEL.slice(
      PANEL.indexOf('private async expandOpenMappings('),
      PANEL.indexOf('private async reveal('),
    );
    assert.match(expand, /fieldsNeedingChildren\(/);
    assert.match(expand, /'stack\/schema'/, 'it no longer asks the core anything');
    assert.match(expand, /at: field\.path/, 'it no longer asks AT the unwritten path');
    assert.match(expand, /field\.children = sub\.node\.fields/, 'the answer is not attached');
    assert.match(expand, /MAX_EXPANSIONS/, 'the fan-out is unbounded');
  });

  it('records what the file says about each path, so the write path can branch', () => {
    // replace_scalar over a null value is not survivable — the core computes a
    // range with no bytes and the splice welds the next line on. The host has
    // to know which paths are null before it can avoid asking.
    assert.match(PANEL, /this\.recordShapes\(schema\)/);
    const record = PANEL.slice(
      PANEL.indexOf('private recordShapes('),
      PANEL.indexOf('private async expandOpenMappings('),
    );
    assert.match(record, /this\.nullValued\.add\(f\.path\)/);
    assert.match(record, /this\.declared\.add\(f\.path\)/);
  });

  it('never sends replace_scalar at a path the file declares as null', () => {
    const stageValue = PANEL.slice(
      PANEL.indexOf('private async stageValue('),
      PANEL.indexOf('private unwrittenAncestors('),
    );
    assert.match(
      stageValue,
      /this\.declared\.has\(path\) && !this\.nullValued\.has\(path\)/,
      'a null value would be replaced in place, which corrupts the line below it',
    );
    // The null case is a delete and an insert, which is a clean two-line diff.
    assert.match(stageValue, /operation: 'delete_key', at: path/);
  });
});

describe('nothing writes outside an explicit save', () => {
  it('reaches the write method from exactly one message', () => {
    const cases = [...PANEL.matchAll(/case '([a-zA-Z]+)':\s*\n\s*await this\.save\(\)/g)].map((m) => m[1]);
    assert.deepEqual(cases, ['save'], `save() is reachable from ${JSON.stringify(cases)}`);
  });

  it('opens a key without staging anything', () => {
    // The defect: `case 'stage'` staged an insert_key carrying the schema's
    // "default" the moment the reader clicked a key name.
    const open = PANEL.slice(PANEL.indexOf("case 'open':"), PANEL.indexOf("case 'close':"));
    assert.match(open, /this\.opened\.add\(msg\.path\)/);
    assert.doesNotMatch(open, /this\.stage|insert_key/, 'clicking an unset key stages an edit again');
  });

  it('never puts a schema default into an operation', () => {
    // Only `defaultValue` may decide what a default is worth writing, and it
    // lives in the webview. The host must not read `.default` at all.
    assert.doesNotMatch(
      PANEL,
      /value:\s*msg\.default|field\.default|\.default\s*\?\?\s*''/,
      'the host writes a schema default straight into an edit again',
    );
  });
});
