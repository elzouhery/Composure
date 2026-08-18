// Captures REAL core answers for the screenshot harness.
//
// Every fixture under harness/fixtures/ is verbatim output from the shipped
// binary (bin/<goos>-<goarch>/composure serve) run against examples/. Nothing here
// is hand-written: a hand-written fixture reproduces the exact mistake that let
// the design gap ship — a check that passes because the input was invented to
// make it pass.
import { open } from './rpc.mjs';
import { mkdirSync, writeFileSync } from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '..', '..');
const bin = path.join(repo, 'extension', 'bin', 'darwin-arm64', 'composure');
const out = path.join(here, 'fixtures');
mkdirSync(out, { recursive: true });

const webstack = path.join(repo, 'examples', 'webstack', 'compose.yaml');
const large = path.join(repo, 'examples', 'large', 'compose.yaml');
const empty = path.join(repo, 'examples', 'empty', 'compose.yaml');
const dockerfile = path.join(repo, 'examples', 'webstack', 'docs', 'Dockerfile');

const core = open(bin);
const fixtures = {};
const save = (name, value) => { fixtures[name] = value; };

try {
  save('initialize', await core.request('initialize', { protocol: 4 }));
  save('topology', await core.request('stack/topology', { path: webstack, profiles: [] }));
  save('diagnose', await core.request('stack/diagnose', { path: webstack, profiles: [] }));
  save('schema.stack', await core.request('stack/schema', { path: webstack, at: '' }));
  save('schema.web', await core.request('stack/schema', { path: webstack, at: 'services.web' }));
  save('schema.worker', await core.request('stack/schema', { path: webstack, at: 'services.worker' }));
  save('schema.web.healthcheck', await core.request('stack/schema', { path: webstack, at: 'services.web.healthcheck' }));
  save('dockerfile', await core.request('stack/dockerfile', { path: dockerfile, at: '' }));
  save('preview', await core.request('stack/preview', {
    file: webstack,
    ops: [{ operation: 'replace_scalar', at: 'services.web.image', value: 'ghcr.io/shipyard/web:2.5.0' }],
  }));
  save('topology.large', await core.request('stack/topology', { path: large, profiles: [] }));
  save('topology.empty', await core.request('stack/topology', { path: empty, profiles: [] }));
  save('schema.empty', await core.request('stack/schema', { path: empty, at: '' }));
  save('diagnose.empty', await core.request('stack/diagnose', { path: empty, profiles: [] }));
} finally {
  core.close();
}

fixtures.paths = { webstack, large, empty, dockerfile };
writeFileSync(path.join(out, 'core.json'), JSON.stringify(fixtures, null, 2));
console.log('captured', Object.keys(fixtures).join(', '));
