// What ships, and to which machine.
//
// Two invariants live here, and both fail the same way: silently, on someone
// else's computer, long after the change that broke them was reviewed.
//
//   1. ZERO RUNTIME NPM DEPENDENCIES. The licence gate (`make licence`,
//      CLEANROOM.md rule 5) walks the GO build graph. It cannot see
//      node_modules, so it cannot tell anyone that a transitive dependency
//      arrived under AGPL. The only tree size that gate can vouch for is
//      empty, which makes "empty" a licensing constraint rather than a taste
//      about bundle size — and therefore something to assert, not to remember.
//
//   2. THE PLATFORM BINARY NAMING. `goTarget()` derives a directory name from
//      process.platform and process.arch; the Makefile's CORE_TARGETS writes
//      binaries into directories it names itself. Nothing but this test spans
//      the two. If they disagree, every build is green, every test passes, and
//      the extension shows "the Composure core binary is not where the extension
//      expects it" to a reader whose machine is fine.
//
// Both read the real files rather than a copy, because a copy is the thing
// that goes stale.

import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import * as path from 'node:path';
import { describe, it } from 'node:test';
import { goTarget, resolveBinaryPath } from './core';

/** dist-test/ sits directly under extension/, which sits under the repo root. */
const EXTENSION_ROOT = path.join(__dirname, '..');
const REPO_ROOT = path.join(EXTENSION_ROOT, '..');

const manifest = JSON.parse(
  readFileSync(path.join(EXTENSION_ROOT, 'package.json'), 'utf8'),
) as Record<string, any>;

const makefile = readFileSync(path.join(REPO_ROOT, 'Makefile'), 'utf8');

/**
 * The CORE_TARGETS assignment from the Makefile, as a list.
 *
 * Parsed rather than duplicated. A list repeated in two places is a list that
 * agrees until the day it matters.
 */
function makefileCoreTargets(): string[] {
  const m = /^CORE_TARGETS\s*:?=\s*(.*)$/m.exec(makefile);
  assert.ok(m, 'the Makefile no longer declares CORE_TARGETS — this test cannot check what it cannot find');
  return m[1].trim().split(/\s+/).filter((s) => s !== '');
}

describe('runtime dependencies', () => {
  it('has none, because the licence gate cannot see node_modules', () => {
    const deps = manifest.dependencies ?? {};
    assert.deepEqual(
      Object.keys(deps),
      [],
      'a runtime npm dependency is invisible to `make licence`, which walks the Go build graph only. ' +
        'Bundle it, vendor it under a reviewed licence, or do without it.',
    );
  });

  it('keeps the build tooling in devDependencies, where it does not ship', () => {
    const dev = Object.keys(manifest.devDependencies ?? {});
    for (const expected of ['esbuild', 'typescript', '@vscode/vsce']) {
      assert.ok(dev.includes(expected), `${expected} should be a dev dependency`);
    }
  });
});

describe('the run is judged by TAP, not by the exit code', () => {
  // `node --test` (v24.10.0, measured) exits 0 when an assertion inside a
  // before/after hook fails: the assertion prints, the suite is marked failed,
  // and the counter still reads `fail 0`. Any check written into a hook is
  // therefore a check that can only pass, and nothing about that is visible to
  // the person who wrote it.
  //
  // run-tests.mjs closes it by reading the TAP stream, where the failed hook
  // appears as `not ok` regardless of the counter. This asserts the runner is
  // still the thing `npm test` invokes — reverting the script to a bare
  // `node --test` reopens the trap in one line and changes no test file.

  it('runs the suites through run-tests.mjs', () => {
    const script = String(manifest.scripts?.test ?? '');
    assert.match(
      script,
      /node run-tests\.mjs/,
      'npm test no longer goes through run-tests.mjs, so a failing hook exits 0 again',
    );
    assert.doesNotMatch(
      script,
      /node --test/,
      'npm test invokes `node --test` directly again, which does not fail on a failing hook',
    );
  });

  it('the runner discovers the suites rather than carrying a list of them', () => {
    // The list used to be spelled out in package.json AND in build.mjs. A suite
    // compiled by one and not run by the other is a suite nobody notices is
    // gone, which is the same class of silence.
    const runner = readFileSync(path.join(EXTENSION_ROOT, 'run-tests.mjs'), 'utf8');
    assert.match(runner, /readdirSync\(/, 'the runner no longer discovers the suites');
    assert.match(runner, /\.test\.js/, 'the runner no longer selects the compiled suites');
    assert.match(runner, /not ok/, 'the runner no longer looks for TAP failures');
    assert.match(runner, /test-reporter=tap/, 'the runner no longer produces a TAP stream to judge');
  });

  it('compiles every suite it is going to be asked to run', () => {
    // build.mjs is the only list left. If it stops compiling a suite, the
    // runner simply finds one fewer file and says nothing — so the count is
    // asserted against the source directories.
    const build = readFileSync(path.join(EXTENSION_ROOT, 'build.mjs'), 'utf8');
    const compiled = [...build.matchAll(/'((?:host|webview)\/[a-z0-9-]+\.test\.ts)'/g)].map((m) => m[1]);
    const onDisk = ['host', 'webview'].flatMap((dir) =>
      readdirSync(path.join(EXTENSION_ROOT, dir))
        .filter((name) => name.endsWith('.test.ts'))
        .map((name) => `${dir}/${name}`),
    );
    assert.deepEqual(
      onDisk.filter((f) => !compiled.includes(f)),
      [],
      'a test file exists that build.mjs never compiles, so `npm test` never runs it',
    );
  });
});

describe('the platform binary matrix', () => {
  const targets = makefileCoreTargets();

  it('covers every platform and architecture VS Code runs on', () => {
    // Not the Makefile's list re-stated — the set derived from the
    // (platform, arch) pairs a VS Code extension host can actually report.
    const reachable = new Set(
      [
        ['darwin', 'arm64'],
        ['darwin', 'x64'],
        ['linux', 'x64'],
        ['linux', 'arm64'],
        ['win32', 'x64'],
      ].map(([p, a]) => goTarget(p, a)),
    );
    for (const target of reachable) {
      assert.ok(
        targets.includes(target),
        `the Makefile builds no core for ${target}, so that platform gets a "no core" banner`,
      );
    }
  });

  it('names its directories exactly as activation looks for them', () => {
    // The join, not a re-spelling of it: this is the path the extension opens.
    for (const [platform, arch] of [
      ['darwin', 'arm64'],
      ['darwin', 'x64'],
      ['linux', 'x64'],
      ['linux', 'arm64'],
      ['win32', 'x64'],
    ]) {
      const { path: resolved, target } = resolveBinaryPath('/ext', '', platform, arch);
      const exe = platform === 'win32' ? 'composure.exe' : 'composure';
      assert.equal(resolved, path.join('/ext', 'bin', target, exe));
      assert.ok(
        targets.includes(target),
        `activation on ${platform}/${arch} opens bin/${target}/${exe}, which \`make extension-cores\` never writes`,
      );
    }
  });

  it('gives Windows the .exe suffix Go actually produces', () => {
    assert.ok(resolveBinaryPath('/ext', '', 'win32', 'x64').path.endsWith('composure.exe'));
    // Was: `endsWith(path.join('composure'))`, which `composure.exe` also satisfies —
    // "composure.exe".endsWith("composure") is false, but the assertion passed for
    // the wrong reason and would have kept passing had the suffix logic been
    // inverted, because every path here ends in one of the two. Assert the
    // whole basename, which distinguishes them.
    assert.equal(path.basename(resolveBinaryPath('/ext', '', 'linux', 'x64').path), 'composure');
    assert.equal(path.basename(resolveBinaryPath('/ext', '', 'darwin', 'arm64').path), 'composure');
    assert.equal(path.basename(resolveBinaryPath('/ext', '', 'win32', 'x64').path), 'composure.exe');
  });

  it('ships a VSIX for every Go target it builds, and no target with no binary', () => {
    // VSCE_TARGETS maps VS Code's vocabulary (x64, win32) onto Go's (amd64,
    // windows). A pair naming a Go target the matrix does not build produces a
    // VSIX with an empty bin/ — which installs cleanly and then cannot start.
    const m = /^VSCE_TARGETS\s*:?=\s*((?:.*\\\n)*.*)$/m.exec(makefile);
    assert.ok(m, 'the Makefile no longer declares VSCE_TARGETS');
    const pairs = m[1]
      .replace(/\\\n/g, ' ')
      .trim()
      .split(/\s+/)
      .filter((s) => s !== '');
    assert.ok(pairs.length > 0, 'VSCE_TARGETS is empty');

    const covered = new Set<string>();
    for (const pair of pairs) {
      const [vsceTarget, goTargetName] = pair.split(':');
      assert.ok(vsceTarget && goTargetName, `malformed VSCE_TARGETS entry: ${pair}`);
      assert.ok(
        targets.includes(goTargetName),
        `VSIX target ${vsceTarget} carries bin/${goTargetName}, which CORE_TARGETS does not build`,
      );
      covered.add(goTargetName);
    }
    for (const target of targets) {
      assert.ok(
        covered.has(target),
        `${target} is built but reaches no VSIX, so nobody on that platform can install it`,
      );
    }
  });
});

describe('marketplace metadata', () => {
  it('carries what the marketplace listing needs', () => {
    // Was `assert.ok(manifest[field])`, which passes for " " and for "TODO" —
    // a listing field that is present and useless is the failure this is for,
    // and truthiness cannot see it. Each field is now held to what it is FOR.
    for (const field of ['displayName', 'publisher']) {
      const value = manifest[field];
      assert.equal(typeof value, 'string', `${field} is not a string`);
      assert.ok(value.trim().length >= 4, `${field} is "${value}" — too short to be a real name`);
      assert.doesNotMatch(value, /^(todo|tbd|xxx|changeme)$/i, `${field} is still a placeholder`);
    }
    // A description is a sentence a stranger reads to decide whether to install.
    assert.equal(typeof manifest.description, 'string');
    assert.ok(
      manifest.description.trim().split(/\s+/).length >= 6,
      `the marketplace description is ${manifest.description.trim().split(/\s+/).length} words`,
    );
    // A licence has to be an identifier the marketplace resolves, not prose.
    assert.match(manifest.license, /^[A-Za-z0-9.+-]+$/, 'license is not an SPDX identifier');
    // A repository has to be somewhere a reader can actually go.
    const repo = typeof manifest.repository === 'string' ? manifest.repository : manifest.repository?.url;
    assert.match(String(repo), /^https?:\/\/\S+$/, 'repository is not a URL');

    for (const list of ['categories', 'keywords']) {
      const value = manifest[list];
      assert.ok(Array.isArray(value) && value.length > 0, `${list} is empty`);
      for (const entry of value) {
        assert.ok(
          typeof entry === 'string' && entry.trim() !== '',
          `${list} carries a blank entry`,
        );
      }
    }
  });

  it('is not marked private, which vsce refuses to package', () => {
    assert.notEqual(manifest.private, true);
  });

  it('excludes the sources, the maps and the tests from the package', () => {
    const ignore = readFileSync(path.join(EXTENSION_ROOT, '.vscodeignore'), 'utf8')
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l !== '' && !l.startsWith('#'));
    for (const pattern of ['host/**', 'webview/**', '**/*.map', 'node_modules/**', 'dist-test/**']) {
      assert.ok(ignore.includes(pattern), `.vscodeignore does not exclude ${pattern}`);
    }
  });
});
