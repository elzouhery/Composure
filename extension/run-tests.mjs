// The test runner, because `node --test` cannot be trusted to report a red run.
//
// THE TRAP, reproduced on node v24.10.0. A failing assertion inside an `after()`
// hook prints in full, is listed under "failing tests", and then:
//
//     ℹ tests 1     ℹ pass 1     ℹ fail 0        exit code 0
//
// The suite is marked failed. The COUNTER is not, and the process exits clean.
// `npm test` is green, CI is green, and the check that fired is the one nobody
// finds out about. Nothing in this repository currently asserts inside a hook —
// the only one left is an assertion-free `beforeEach` — so nothing is broken
// today. It is permanently trap-shaped, which is worse than broken: the next
// person to write a setup-time invariant ("the fixtures loaded", "the stub core
// exited", "no test leaked a stage") gets a check that can only ever pass.
//
// So the run is not judged by the exit code. It is judged by the TAP stream,
// which reports the failed hook as `not ok` regardless of what the counter says.
// TAP is a specified format; the spec reporter's layout is not, which is why the
// machine-read copy is TAP and the human-read copy is the spec output.
//
//   node run-tests.mjs           run every suite in dist-test/
//
// Suites are discovered rather than listed. The list used to live in
// package.json AND in build.mjs, and a suite added to one and not the other is
// a suite that is compiled and never run.

import { spawn } from 'node:child_process';
import { readFileSync, readdirSync, rmSync } from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.join(root, 'dist-test');

const suites = readdirSync(outDir)
  .filter((name) => name.endsWith('.test.js'))
  .sort()
  .map((name) => path.join(outDir, name));

if (suites.length === 0) {
  console.error('run-tests: no suites in dist-test/ — run `node build.mjs --tests` first.');
  process.exit(1);
}

const tapFile = path.join(outDir, 'run.tap');
rmSync(tapFile, { force: true });

const child = spawn(
  process.execPath,
  [
    '--test',
    '--test-reporter=spec',
    '--test-reporter-destination=stdout',
    '--test-reporter=tap',
    `--test-reporter-destination=${tapFile}`,
    ...suites,
  ],
  { stdio: 'inherit', cwd: root },
);

child.on('exit', (code, signal) => {
  let tap = '';
  try {
    tap = readFileSync(tapFile, 'utf8');
  } catch {
    console.error('run-tests: the TAP stream was not written, so the run cannot be judged.');
    process.exit(1);
  }

  // Every failure TAP knows about, at any nesting depth. A test that failed
  // normally also exits non-zero; a HOOK that failed does not, and this is the
  // only place it is visible.
  const failures = tap
    .split('\n')
    .filter((line) => /^[ \t]*not ok /.test(line))
    .map((line) => line.trim());

  if (failures.length > 0 && (code === 0 || code === null)) {
    console.error(
      '\nrun-tests: node --test exited 0 while TAP reported failures. This is the hook trap:\n' +
        'a failing assertion inside before/after/beforeEach/afterEach is not counted, so the\n' +
        'run reports `fail 0` and exits clean. Failures:\n' +
        failures.map((f) => `  ${f}`).join('\n') +
        '\n',
    );
    process.exit(1);
  }

  if (signal) {
    console.error(`run-tests: the test process was killed by ${signal}.`);
    process.exit(1);
  }
  process.exit(code ?? 1);
});
