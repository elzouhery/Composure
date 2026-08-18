#!/usr/bin/env node
// A stand-in for `composure serve`, so the client's lifecycle can be tested
// without a Go toolchain and without a real compose file.
//
// Plain JavaScript on purpose: the tests spawn it as a process, so it has to
// run without a build step of its own. Each mode reproduces one row of the
// story's edge-case matrix.
//
// The mode comes from COMPOSURE_STUB_MODE, because the client spawns its binary
// with a fixed `serve` argument and the tests must not get a say in that.
//
//   ok            speaks the protocol correctly
//   refuse        answers stack/topology with -32001 + position
//   wrong-version handshakes with a protocol revision from the future
//   silent        never answers anything
//   exit-now      exits 2 before the handshake lands
//   crash-after   handshakes, then dies on the next request
//   dribble       correct, but every frame arrives in two chunks
//   bad-frame     handshakes, then emits an unparseable header
//   stall         handshakes, then never answers another request
//   header-flood  handshakes, then emits bytes that never end a frame header
//   hub-silent    correct, but image/lookup and image/search NEVER answer.
//                 Epic 8's whole claim in one mode: the pane must be complete
//                 without them, so a test drives a full draw against this and
//                 asserts every message that matters still arrived.
//   hub-limited   answers image/* with the rate-limited state and a sentence

'use strict';

const mode = process.env.COMPOSURE_STUB_MODE || 'ok';

const PROJECT = {
  files: [{ path: '/tmp/compose.yml', step: 0 }],
  root: {
    kind: 'mapping',
    scalar: '',
    map: [
      {
        key: 'services',
        key_origin: { file: '/tmp/compose.yml', line: 1, column: 1, step: 0 },
        value: {
          kind: 'mapping',
          scalar: '',
          map: [
            {
              key: 'web',
              key_origin: { file: '/tmp/compose.yml', line: 2, column: 3, step: 0 },
              value: {
                kind: 'mapping',
                scalar: '',
                map: [
                  {
                    key: 'image',
                    key_origin: { file: '/tmp/compose.yml', line: 3, column: 5, step: 0 },
                    value: {
                      kind: 'scalar',
                      scalar: 'nginx:1.27',
                      origin: { file: '/tmp/compose.yml', line: 3, column: 12, step: 0 },
                      overrides: [],
                    },
                  },
                ],
                origin: { file: '/tmp/compose.yml', line: 2, column: 3, step: 0 },
                overrides: [],
              },
            },
          ],
          origin: { file: '/tmp/compose.yml', line: 1, column: 1, step: 0 },
          overrides: [],
        },
      },
    ],
    origin: { file: '/tmp/compose.yml', line: 1, column: 1, step: 0 },
    overrides: [],
  },
};

// The graph the panel actually asks for. Small on purpose: this stub exists to
// exercise framing and process lifecycle, not the topology engine — the real
// binary does that in realcore.test.ts.
const O = { file: '/tmp/compose.yml', line: 2, column: 3, step: 0 };
const GRAPH = {
  profiles: [],
  nodes: [
    { id: 'services.web', kind: 'service', name: 'web', origin: O, declared: true, external: false, profiles: [], layer: 1, image: 'nginx:1.27' },
    { id: 'services.db', kind: 'service', name: 'db', origin: O, declared: true, external: false, profiles: [], layer: 0, image: 'postgres:16' },
    { id: 'services.web.ports[0]', kind: 'port', name: '8080:80', origin: O, declared: true, external: false, profiles: [], layer: 0, port: { published: '8080', target: '80', protocol: 'tcp', raw: '8080:80' } },
  ],
  edges: [
    { kind: 'depends_on', from: 'services.web', to: 'services.db', origin: O, depends_on: { condition: 'service_healthy' } },
    { kind: 'publish', from: 'services.web', to: 'services.web.ports[0]', origin: O },
  ],
  cycles: [],
  dangling: [],
  max_layer: 1,
};

function send(obj) {
  const body = Buffer.from(JSON.stringify(obj), 'utf8');
  const header = Buffer.from(`Content-Length: ${body.length}\r\n\r\n`, 'ascii');
  if (mode === 'dribble') {
    process.stdout.write(header);
    setTimeout(() => process.stdout.write(body), 5);
    return;
  }
  process.stdout.write(Buffer.concat([header, body]));
}

let buffer = Buffer.alloc(0);
process.stdin.on('data', (chunk) => {
  buffer = Buffer.concat([buffer, chunk]);
  for (;;) {
    const end = buffer.indexOf('\r\n\r\n');
    if (end < 0) return;
    const header = buffer.subarray(0, end).toString('ascii');
    const match = /content-length:\s*(\d+)/i.exec(header);
    if (!match) return;
    const length = Number(match[1]);
    if (buffer.length < end + 4 + length) return;
    const body = buffer.subarray(end + 4, end + 4 + length).toString('utf8');
    buffer = buffer.subarray(end + 4 + length);
    handle(JSON.parse(body));
  }
});

function handle(req) {
  if (mode === 'silent') return;

  switch (req.method) {
    case 'initialize':
      send({
        jsonrpc: '2.0',
        id: req.id,
        result: {
          serverName: 'composure',
          version: '0.1.0-stub',
          // Must equal PROTOCOL_REVISION in host/core.ts. Written here as a
          // literal because this file is spawned as a bare node process with
          // no build step; a mismatch is loud rather than silent — every
          // lifecycle test refuses at the handshake, which is exactly what the
          // handshake is for.
          protocol: mode === 'wrong-version' ? 99 : 9,
        },
      });
      return;
    case 'stack/resolve':
    case 'stack/topology':
      if (mode === 'crash-after') {
        process.exit(3);
      }
      // Accepts the request and simply never answers it. This is the failure
      // the per-request timeout exists for: the process is alive, the pipe is
      // open, and the caller waits forever.
      if (mode === 'stall') {
        return;
      }
      // Bytes that will never terminate a frame header. Without a cap on the
      // header scan the client's buffer grows for as long as this runs.
      if (mode === 'header-flood') {
        process.stdout.write('Content-Length: 4\r\n'.repeat(8000));
        return;
      }
      if (mode === 'bad-frame') {
        process.stdout.write('Content-Length: not-a-number\r\n\r\n{}');
        return;
      }
      if (mode === 'refuse') {
        send({
          jsonrpc: '2.0',
          id: req.id,
          error: {
            code: -32001,
            message: 'compose.yml:7:3: did not find expected node content',
            data: { file: '/tmp/compose.yml', line: 7, column: 3 },
          },
        });
        return;
      }
      send({
        jsonrpc: '2.0',
        id: req.id,
        result: req.method === 'stack/topology' ? GRAPH : PROJECT,
      });
      return;
    case 'image/lookup':
      // Epic 8. The three shapes that matter, and the one that matters most is
      // `hub-silent`: a core that accepts the request and never answers it.
      // Everything about the pane has to be true anyway.
      if (mode === 'hub-silent') {
        return;
      }
      send({
        jsonrpc: '2.0',
        id: req.id,
        result:
          mode === 'hub-limited'
            ? {
                reference: (req.params && req.params.ref) || '',
                repository: '',
                display: '',
                tag: '',
                state: 'rate-limited',
                message:
                  'Docker Hub is rate limiting this address. The limit is a hundred and eighty ' +
                  'requests a minute and it is shared by everyone behind your address.',
              }
            : {
                reference: (req.params && req.params.ref) || 'nginx:1.27',
                repository: 'library/nginx',
                display: 'nginx',
                tag: '1.27',
                state: 'ok',
                message: 'nginx:1.29 is a minor upgrade in the same family.',
                age: '14 months old',
                age_days: 425,
                pill: 'nginx:1.29 · minor · 40MB smaller',
                candidate: {
                  reference: 'nginx:1.29',
                  tag: '1.29',
                  kind: 'minor',
                  size_delta: -41943040,
                  has_size: true,
                },
              },
      });
      return;
    case 'image/search':
      if (mode === 'hub-silent') {
        return;
      }
      send({
        jsonrpc: '2.0',
        id: req.id,
        result:
          mode === 'hub-limited'
            ? { query: (req.params && req.params.query) || '', state: 'rate-limited', message: 'Docker Hub is rate limiting this address.', results: [] }
            : {
                query: (req.params && req.params.query) || '',
                state: 'ok',
                message: '',
                results: [
                  {
                    name: 'postgres',
                    description: 'The PostgreSQL object-relational database system',
                    stars: 14979,
                    pulls_display: '1B+',
                    official: true,
                    badge: 'official',
                    architectures: ['amd64', 'arm64'],
                  },
                ],
              },
      });
      return;
    case 'shutdown':
      send({ jsonrpc: '2.0', id: req.id, result: null });
      return;
    case 'exit':
      process.exit(0);
      return;
    default:
      send({
        jsonrpc: '2.0',
        id: req.id,
        error: { code: -32601, message: `unknown method "${req.method}"` },
      });
  }
}

if (mode === 'exit-now') {
  process.exit(2);
}
