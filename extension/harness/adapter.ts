// Turns the captured core answers into the exact HostMessage stream the
// extension host posts, using the SHIPPED host adapters (`host/topology.ts`,
// `host/inspect.ts`) rather than a second copy of them. Bundled to
// harness/dist/adapter.js and loaded by harness/index.html.
//
// Nothing in extension/webview, extension/host or extension/shared is modified
// by this file; it only calls them.

import { readGraph } from '../host/topology';
import { readReport, readSchema, findingsFor, severityCounts } from '../host/inspect';
import { missingDockerfileNodes, saveLabel } from '../host/staging';
import type { HostMessage, NodeKind } from '../shared/protocol';

type Fixtures = Record<string, any>;

/**
 * Epic 8's answers, as the host posts them.
 *
 * FIXED RATHER THAN FETCHED, and deliberately: `capture.mjs` runs the real core
 * against real files, and a screenshot whose contents depend on what Docker Hub
 * happened to be serving that afternoon is not a design reference — it is a
 * different picture every time it is taken. The SHAPE is the shipped wire type,
 * so a renamed field breaks this the way it breaks the pane.
 *
 * These are also the only messages in the harness that are not derived from a
 * file on disk, which is exactly the fact Epic 8 is about.
 */
const IMAGE_UPGRADE = {
  reference: 'nginx:1.27',
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
};

/** The state a screenshot most needs to prove is not an error banner. */
const IMAGE_RATE_LIMITED = {
  reference: 'nginx:1.27',
  repository: 'library/nginx',
  display: 'nginx',
  tag: '1.27',
  state: 'rate-limited',
  message:
    'Docker Hub is rate limiting this address. The limit is a hundred and eighty requests a ' +
    'minute and it is shared by everyone behind your address, so this is usually a busy ' +
    'office rather than anything you did. It passes on its own.',
};

declare global {
  interface Window {
    ComposureHarness: {
      messages(fixtures: Fixtures, scenario: string): HostMessage[];
    };
  }
}

function stackMessages(f: Fixtures, key: string, diagnoseKey: string, file: string) {
  const { graph } = readGraph(f[key]);
  const report = readReport(f[diagnoseKey]);
  return { graph, findings: report.findings, file };
}

function inspection(
  f: Fixtures,
  schemaKey: string,
  id: string | null,
  name: string,
  kind: NodeKind | 'stack',
  findings: any[],
  extra: { staged?: string[]; opened?: string[]; pending?: Record<string, string> } = {},
) {
  return {
    id,
    name,
    kind,
    schema: readSchema(f[schemaKey]),
    findings: findingsFor(findings, id),
    staged: extra.staged ?? [],
    opened: extra.opened ?? [],
    pending: extra.pending ?? {},
  };
}

window.ComposureHarness = {
  messages(f: Fixtures, scenario: string): HostMessage[] {
    const out: HostMessage[] = [];
    const webstack = f.paths.webstack as string;

    if (scenario === 'empty') {
      const { graph, findings, file } = stackMessages(f, 'topology.empty', 'diagnose.empty', f.paths.empty);
      out.push({ type: 'clearFailure' } as HostMessage);
      out.push({ type: 'empty', file } as HostMessage);
      out.push({
        type: 'inspection',
        file,
        inspection: inspection(f, 'schema.empty', null, 'compose.yaml', 'stack', findings),
      } as HostMessage);
      out.push({ type: 'pendingCleared', file } as HostMessage);
      void graph;
      return out;
    }

    if (scenario === 'dockerfile' || scenario === 'dockerfile-upgrade') {
      const file = f.paths.dockerfile as string;
      out.push({ type: 'dockerfile', file, form: f.dockerfile, from: null, staged: [] } as HostMessage);
      out.push({ type: 'pendingCleared', file } as HostMessage);
      if (scenario === 'dockerfile-upgrade') {
        // AFTER the form, which is where the host sends it and the whole point:
        // the pane is complete before this arrives.
        out.push({
          type: 'imageLookup',
          file,
          key: 'stage:0',
          lookup: IMAGE_UPGRADE as any,
        } as HostMessage);
      }
      return out;
    }

    const large = scenario === 'large';
    const { graph, findings, file } = large
      ? stackMessages(f, 'topology.large', 'diagnose.empty', f.paths.large)
      : stackMessages(f, 'topology', 'diagnose', webstack);

    out.push({ type: 'clearFailure' } as HostMessage);
    out.push({
      type: 'graph',
      file,
      graph,
      missing: missingDockerfileNodes(findings),
      positions: {},
      selected: scenario === 'stack' ? null : 'services.web',
      fit: true,
      severities: severityCounts(findings, graph.nodes.map((n: any) => n.id)),
    } as HostMessage);
    out.push({
      type: 'profiles',
      file,
      declared: readSchema(f['schema.stack']).profiles,
      active: [],
    } as HostMessage);

    if (scenario === 'stack' || large) {
      out.push({
        type: 'inspection',
        file,
        inspection: inspection(f, 'schema.stack', null, 'compose.yaml', 'stack', findings),
      } as HostMessage);
      out.push({ type: 'pendingCleared', file } as HostMessage);
      return out;
    }

    // scenario 'service' — a service selected, provenance + finding pills.
    // scenario 'pending' — the same, with one scalar edit staged.
    const staged = scenario === 'pending' ? ['services.web.image'] : [];
    const pending =
      scenario === 'pending' ? { 'services.web.image': 'ghcr.io/shipyard/web:2.5.0' } : {};
    out.push({
      type: 'inspection',
      file,
      inspection: inspection(f, 'schema.web', 'services.web', 'web', 'service', findings, {
        staged,
        pending,
      }),
    } as HostMessage);
    if (scenario === 'pending') {
      const p = f.preview;
      out.push({
        type: 'pending',
        file,
        count: 1,
        diff: p.diff,
        added: p.added ?? 1,
        removed: p.removed ?? 1,
        saveLabel: saveLabel(file),
      } as HostMessage);
    } else {
      out.push({ type: 'pendingCleared', file } as HostMessage);
    }
    // Epic 8, last in the stream because it is last in the host: the inspector
    // has already been posted and drawn by the time Docker Hub says anything.
    if (scenario === 'upgrade' || scenario === 'ratelimited') {
      out.push({
        type: 'imageLookup',
        file,
        key: 'services.web.image',
        lookup: (scenario === 'upgrade' ? IMAGE_UPGRADE : IMAGE_RATE_LIMITED) as any,
      } as HostMessage);
    }
    return out;
  },
};
