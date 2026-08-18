// Lifecycle and framing, driven against a stub process.
//
// The rows exercised here are the ones from the story's edge-case matrix that
// do not need an editor: a missing binary, a core that will not start, a core
// that dies mid-session, a refused compose file, and a frame that arrives in
// two pieces. Everything on screen is verified by hand — see TESTING.md.
//
// Run with `npm test`.

import { panelAction } from './compose.js';
import assert from 'node:assert/strict';
import * as path from 'node:path';
import { after, describe, it } from 'node:test';
import {
  CoreError,
  Framer,
  ComposureCore,
  RpcError,
  goTarget,
  resolveBinaryPath,
  type ExitInfo,
} from './core';
import { readGraph } from './topology';
import { isComposeFile } from './compose';

// The stub is copied next to the compiled test by build.mjs, so this resolves
// from dist-test/ at run time.
const STUB = path.join(__dirname, 'stub-core.js');

const started: ComposureCore[] = [];

function makeCore(
  mode: string,
  log: string[] = [],
  onExit: (i: ExitInfo) => void = () => {},
  requestTimeoutMs?: number,
) {
  process.env.COMPOSURE_STUB_MODE = mode;
  const core = new ComposureCore({
    binaryPath: STUB,
    log: (line) => log.push(line),
    onExit,
    requestTimeoutMs,
  });
  started.push(core);
  return core;
}

after(() => {
  for (const core of started) {
    core.dispose();
  }
});

describe('binary selection', () => {
  it('files binaries under Go GOOS-GOARCH names', () => {
    assert.equal(goTarget('darwin', 'arm64'), 'darwin-arm64');
    assert.equal(goTarget('linux', 'x64'), 'linux-amd64');
    assert.equal(goTarget('win32', 'x64'), 'windows-amd64');
  });

  it('looks inside the extension for the platform binary', () => {
    const found = resolveBinaryPath('/ext', '', 'linux', 'x64');
    assert.equal(found.path, path.join('/ext', 'bin', 'linux-amd64', 'composure'));
    assert.equal(found.configured, false);
    assert.equal(resolveBinaryPath('/ext', '', 'win32', 'x64').path.endsWith('composure.exe'), true);
  });

  it('prefers a configured path, for development', () => {
    const found = resolveBinaryPath('/ext', '/usr/local/bin/composure', 'darwin', 'arm64');
    assert.equal(found.path, '/usr/local/bin/composure');
    assert.equal(found.configured, true);
  });
});

describe('framing', () => {
  it('reassembles a body split across chunks', () => {
    const framer = new Framer();
    const frame = Framer.encode('{"a":1}');
    assert.deepEqual(framer.push(frame.subarray(0, 12)), []);
    assert.deepEqual(framer.push(frame.subarray(12)), ['{"a":1}']);
  });

  it('returns every body in a chunk carrying several frames', () => {
    const framer = new Framer();
    const both = Buffer.concat([Framer.encode('"one"'), Framer.encode('"two"')]);
    assert.deepEqual(framer.push(both), ['"one"', '"two"']);
  });

  it('keeps multibyte bodies intact', () => {
    const framer = new Framer();
    const body = JSON.stringify({ image: 'ghcr.io/ünïcode:1 · 2' });
    assert.deepEqual(framer.push(Framer.encode(body)), [body]);
  });

  it('refuses a frame it cannot resynchronise', () => {
    const framer = new Framer();
    assert.throws(
      () => framer.push(Buffer.from('Content-Length: not-a-number\r\n\r\n{}', 'utf8')),
      (err: unknown) => err instanceof CoreError && err.kind === 'protocol',
    );
  });
});

describe('lifecycle', () => {
  it('names a missing binary rather than throwing something anonymous', async () => {
    const core = new ComposureCore({
      binaryPath: path.join(__dirname, 'no-such-core-binary'),
      log: () => {},
      onExit: () => {},
    });
    await assert.rejects(
      core.start(),
      (err: unknown) => err instanceof CoreError && err.kind === 'core-missing',
    );
  });

  it('fails when the core exits before the handshake', async () => {
    const core = makeCore('exit-now');
    await assert.rejects(
      core.start(1000),
      (err: unknown) =>
        err instanceof CoreError && (err.kind === 'core-crashed' || err.kind === 'spawn-failed'),
    );
  });

  it('bounds the handshake rather than hanging', async () => {
    const core = makeCore('silent');
    await assert.rejects(
      core.start(150),
      (err: unknown) => err instanceof CoreError && err.kind === 'spawn-failed',
    );
  });

  it('refuses a core speaking another protocol revision', async () => {
    const core = makeCore('wrong-version');
    await assert.rejects(
      core.start(2000),
      (err: unknown) => err instanceof CoreError && err.kind === 'protocol',
    );
  });

  it('derives a topology over the wire', async () => {
    const core = makeCore('ok');
    await core.start(2000);
    const answer = await core.request<unknown>('stack/topology', { path: '/tmp/compose.yml', profiles: [] });
    const { graph, droppedEdges } = readGraph(answer);
    assert.equal(droppedEdges, 0);
    assert.deepEqual(
      graph.nodes.map((n) => [n.id, n.kind]),
      [
        ['services.web', 'service'],
        ['services.db', 'service'],
        ['services.web.ports[0]', 'port'],
      ],
    );
    assert.deepEqual(
      graph.edges.map((e) => e.kind),
      ['depends_on', 'publish'],
    );
  });

  it('survives frames that arrive in two chunks', async () => {
    const core = makeCore('dribble');
    await core.start(2000);
    const answer = await core.request<unknown>('stack/topology', { path: '/tmp/compose.yml', profiles: [] });
    assert.equal(readGraph(answer).graph.nodes.length, 3);
  });

  it('surfaces a refused compose file as a positioned error', async () => {
    const core = makeCore('refuse');
    await core.start(2000);
    await assert.rejects(
      core.request('stack/topology', { path: '/tmp/compose.yml', profiles: [] }),
      (err: unknown) =>
        err instanceof RpcError && err.code === -32001 && err.data?.line === 7 && err.data?.column === 3,
    );
  });

  it('rejects in-flight calls when the core dies, and reports the exit', async () => {
    const exits: ExitInfo[] = [];
    const core = makeCore('crash-after', [], (info) => exits.push(info));
    await core.start(2000);
    await assert.rejects(
      core.request('stack/topology', { path: '/tmp/compose.yml', profiles: [] }),
      (err: unknown) => err instanceof CoreError && err.kind === 'core-crashed',
    );
    // A later call must fail immediately rather than wait on a dead pipe.
    await assert.rejects(
      core.request('stack/topology', { path: '/tmp/compose.yml', profiles: [] }),
      (err: unknown) => err instanceof CoreError && err.kind === 'core-crashed',
    );
    assert.equal(core.running, false);
    assert.equal(exits.length, 1);
    assert.equal(exits[0].code, 3);
  });

  it('rejects in-flight calls when the core sends an unreadable frame', async () => {
    const core = makeCore('bad-frame');
    await core.start(2000);
    await assert.rejects(
      core.request('stack/topology', { path: '/tmp/compose.yml', profiles: [] }),
      (err: unknown) => err instanceof CoreError && err.kind === 'protocol',
    );
  });

  // Every call is bounded, not just the handshake. A core that accepts a
  // request and never answers it is alive, so nothing else in this file
  // catches it — and the panel coalesces draws while one is in flight, so a
  // single unanswered resolve silently swallows every redraw after it.
  it('bounds a request the core never answers', async () => {
    const core = makeCore('stall', [], () => {}, 200);
    await core.start(2000);
    const started = Date.now();
    await assert.rejects(
      core.request('stack/topology', { path: '/tmp/compose.yml', profiles: [] }),
      (err: unknown) => err instanceof CoreError && err.kind === 'timeout',
    );
    assert.ok(Date.now() - started < 2000, 'the request was not bounded');
    // The process is still alive and a bounded call is not a dead core: the
    // timeout is about this request, not about the session.
    assert.equal(core.running, true);
  });

  it('does not time out a request that was answered', async () => {
    const core = makeCore('ok', [], () => {}, 400);
    await core.start(2000);
    const answer = await core.request<unknown>('stack/topology', { path: '/tmp/compose.yml', profiles: [] });
    assert.equal(readGraph(answer).graph.nodes.length, 3);
    // Well past the bound: a stale timer must not reject an already-settled
    // promise or fire against a reused id.
    await new Promise((r) => setTimeout(r, 600));
    assert.equal(core.running, true);
  });

  // Bytes with no CRLFCRLF in them are not a frame in progress, they are a
  // stream that will never produce one. Without a cap the buffer grows for as
  // long as the peer keeps writing.
  it('refuses a header block that never ends', async () => {
    const core = makeCore('header-flood', [], () => {}, 5000);
    await core.start(2000);
    await assert.rejects(
      core.request('stack/topology', { path: '/tmp/compose.yml', profiles: [] }),
      (err: unknown) => err instanceof CoreError && err.kind === 'protocol',
    );
  });
});

describe('compose file detection', () => {
  it('accepts Compose\u2019s own candidate names and override files', () => {
    for (const name of [
      '/w/compose.yml',
      '/w/compose.yaml',
      '/w/docker-compose.yml',
      '/w/docker-compose.yaml',
      '/w/compose.override.yml',
      '/w/docker-compose.override.yaml',
      'C:\\w\\docker-compose.yml',
    ]) {
      assert.equal(isComposeFile(name), true, name);
    }
  });

  it('accepts a named variant, the way Compose users actually write them', () => {
    for (const name of ['/w/compose.prod.yml', '/w/compose.dev.yaml', '/w/docker-compose.ci.yml']) {
      assert.equal(isComposeFile(name), true, name);
    }
  });

  it('leaves every other YAML file alone', () => {
    for (const name of ['/w/deployment.yaml', '/w/.gitlab-ci.yml', '/w/composer.json', '/w/compose.txt']) {
      assert.equal(isComposeFile(name), false, name);
    }
  });

  // Two definitions of "a compose file" exist: this regex, and the
  // `workspaceContains:` globs that decide whether the extension is ever
  // loaded. They drifted — the globs listed eight literal names while the
  // regex accepted a variant segment, so a workspace holding only
  // `compose.prod.yml` never activated and none of this code ran.
  //
  // Under-activation is the dangerous direction: it produces no error, just an
  // extension that appears not to exist. So the assertion is one-way — every
  // name the regex accepts must be reachable by some activation glob.
  it('activates for every filename it claims to handle', () => {
    const manifest = require(path.join(__dirname, '..', 'package.json')) as {
      activationEvents: string[];
    };
    const globs = manifest.activationEvents
      .filter((e) => e.startsWith('workspaceContains:'))
      .map((e) => e.slice('workspaceContains:'.length));
    assert.ok(globs.length > 0, 'no workspaceContains activation events');

    for (const name of [
      'compose.yml',
      'compose.yaml',
      'docker-compose.yml',
      'docker-compose.yaml',
      'compose.override.yml',
      'compose.prod.yml',
      'compose.dev.yaml',
      'docker-compose.override.yaml',
      'docker-compose.ci.yml',
    ]) {
      assert.equal(isComposeFile(`/w/${name}`), true, `regex rejects ${name}`);
      assert.ok(
        globs.some((g) => globMatches(g, `w/${name}`)),
        `no activation glob matches ${name} — a workspace holding only this file would never activate the extension`,
      );
    }
  });
});

/** The subset of VS Code's glob syntax the activation events use. */
function globMatches(glob: string, filePath: string): boolean {
  const source = glob
    .replace(/[.+^$()|[\]\\]/g, '\\$&')
    .replace(/\{([^}]*)\}/g, (_, group: string) => `(${group.split(',').join('|')})`)
    .replace(/\*\*\//g, ' ')
    .replace(/\*/g, '[^/]*')
    .replace(/ /g, '(?:.*/)?');
  return new RegExp(`^${source}$`).test(filePath);
}

describe('panelAction', () => {
  it('opens the panel when a compose file is opened and none exists', () => {
    // The shipped defect: this returned 'none' in effect, because the wiring
    // only ever repointed an existing panel. Activation fires before any
    // editor is open in a freshly opened workspace, so nothing ever appeared.
    assert.equal(panelAction({ hasPanel: false, dismissed: false }), 'open');
  });

  it('repoints an existing panel rather than stacking another', () => {
    assert.equal(panelAction({ hasPanel: true, dismissed: false }), 'repoint');
  });

  it('stays closed after the reader closes it themselves', () => {
    assert.equal(panelAction({ hasPanel: false, dismissed: true }), 'none');
  });

  it('still repoints if the reader reopens it after dismissing', () => {
    assert.equal(panelAction({ hasPanel: true, dismissed: true }), 'repoint');
  });
});
