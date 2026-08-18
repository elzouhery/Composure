// Build script. esbuild is a dev-only tool: nothing it produces carries a
// runtime npm dependency, because there are none to carry.
//
//   node build.mjs           bundle the extension host and the webview
//   node build.mjs --watch   the same, rebuilt on change
//   node build.mjs --tests   bundle the host tests into dist-test/
//
// Three bundles, three targets: the host runs in Node inside the extension
// host, the webview runs in a browser context with no Node API at all, and the
// tests run under `node --test`.

import { cp, mkdir, rm } from 'node:fs/promises';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as esbuild from 'esbuild';

const root = path.dirname(fileURLToPath(import.meta.url));
const watch = process.argv.includes('--watch');
const tests = process.argv.includes('--tests');

/** @type {import('esbuild').BuildOptions} */
const common = {
  bundle: true,
  sourcemap: true,
  logLevel: 'info',
  absWorkingDir: root,
};

const hostBundle = {
  ...common,
  entryPoints: ['host/extension.ts'],
  outfile: 'dist/extension.js',
  platform: 'node',
  target: 'node18',
  format: 'cjs',
  // `vscode` is provided by the extension host at runtime and must never be
  // bundled; it does not exist on disk.
  external: ['vscode'],
};

const webviewBundle = {
  ...common,
  entryPoints: ['webview/main.ts'],
  outfile: 'dist/webview.js',
  platform: 'browser',
  target: 'es2020',
  format: 'iife',
};

async function copyStatic() {
  await mkdir(path.join(root, 'dist'), { recursive: true });
  await cp(path.join(root, 'webview/style.css'), path.join(root, 'dist/webview.css'));
}

if (tests) {
  await rm(path.join(root, 'dist-test'), { recursive: true, force: true });
  await mkdir(path.join(root, 'dist-test'), { recursive: true });
  // One bundle per suite, written flat into dist-test/ — not with `outdir`,
  // which would mirror host/ and webview/ and move `__dirname` out from under
  // the tests that resolve the stub core and the fixtures relative to it.
  const suites = [
    'host/core.test.ts',
    'host/realcore.test.ts',
    'host/realedit.test.ts',
    'host/staging.test.ts',
    'host/topology.test.ts',
    'host/inspect.test.ts',
    'host/images.test.ts',
    'host/packaging.test.ts',
    'webview/layout.test.ts',
    'webview/inspector.test.ts',
    'webview/pending.test.ts',
    'webview/view.test.ts',
    'webview/a11y.test.ts',
    'webview/testdom.test.ts',
    'webview/groups.test.ts',
    'host/panel.test.ts',
    // The render-level and behavioural suites, plus the shared fake DOM they
    // import. `fakedom.test.ts` is mostly a library, and it is compiled and run
    // like any other suite because it carries the checks that the DOM itself
    // does not lie — and because host/packaging.test.ts guarantees that every
    // `*.test.ts` on disk is compiled, which is a guarantee worth more than the
    // tidiness of leaving it out.
    'webview/fakedom.test.ts',
    'webview/graphdom.test.ts',
    'webview/appdom.test.ts',
    'webview/stageformdom.test.ts',
    'webview/addformdom.test.ts',
    'webview/imagedom.test.ts',
    'host/locate.test.ts',
    'host/panelbehaviour.test.ts',
  ];
  await Promise.all(
    suites.map((entry) =>
      esbuild.build({
        ...common,
        entryPoints: [entry],
        outfile: `dist-test/${path.basename(entry, '.ts')}.js`,
        platform: 'node',
        target: 'node18',
        format: 'cjs',
        external: ['vscode', 'node:test'],
      }),
    ),
  );
  // The stub core is plain JavaScript on purpose: the tests spawn it as a
  // process, so it has to be runnable without a build step of its own.
  await cp(path.join(root, 'host/testdata/stub-core.js'), path.join(root, 'dist-test/stub-core.js'));
  // The committed compose fixtures the real-binary test resolves.
  await cp(path.join(root, 'host/testdata'), path.join(root, 'dist-test/testdata'), {
    recursive: true,
  });
} else if (watch) {
  await copyStatic();
  const ctxs = await Promise.all([esbuild.context(hostBundle), esbuild.context(webviewBundle)]);
  await Promise.all(ctxs.map((c) => c.watch()));
} else {
  await copyStatic();
  await Promise.all([esbuild.build(hostBundle), esbuild.build(webviewBundle)]);
}
