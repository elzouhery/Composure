// The one test that drives the REAL core binary.
//
// Everything else in this suite speaks to a stub, which is what makes the
// lifecycle testable without a Go toolchain — and which means nothing else here
// can catch the two ways the two halves of this product drift apart:
//
//   1. The protocol revision is two constants. `protocolRevision` lives in
//      cmd/composure/serve.go and `PROTOCOL_REVISION` lives in host/core.ts, and
//      each side's own tests compare the value to the constant that produced
//      it. Bump one and both suites stay green while the extension refuses
//      every core in the field.
//
//   2. The wire keys are read by name in shared/protocol.ts and host/topology.ts
//      and written by struct tag in internal/topology. Nothing in either
//      language's type system spans that gap; a renamed tag compiles clean on
//      both sides and draws an empty panel.
//
// So: spawn the real binary, handshake against it, and derive the topology of a
// real committed compose file. If either constant or any key has moved, this
// fails.
//
// The binary is built by `make extension-core` into extension/bin/ and is
// gitignored, so a checkout without it skips rather than fails — with a message
// that says which command produces it.
//
// THAT SKIP USED TO BE SILENT, and a silent skip on the only cross-language
// check in the suite is how a boundary regression hides: the run reports
// `pass 682, fail 0` instead of `pass 683`, one number nobody knows the right
// value of, and the drift ships. So the skip is now BOTH loud and, in the runs
// that matter, fatal:
//
//   * `make extension-test` and CI build the core first and set
//     COMPOSURE_REQUIRE_CORE=1 (CI's own `CI` variable does the same), and with it
//     set a missing binary is a FAILING test that names the command.
//   * Without it — a contributor with no Go toolchain — the guard test reports
//     itself skipped, which moves the suite's `skipped` count off zero, and a
//     banner naming every skipped boundary goes to stderr.

import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import * as path from 'node:path';
import { describe, it } from 'node:test';
import { ComposureCore, PROTOCOL_REVISION, goTarget } from './core';
import { readGraph } from './topology';
import { readReport, readSchema, severityCounts } from './inspect';
import { groupsOf, placeholderFor, provenanceOf } from '../webview/inspector';
import { layoutGraph, describeNode, edgeLabel } from '../webview/layout';
import type { DockerfileForm, SchemaField, StackSchema, ValueView } from '../shared/protocol';

/** dist-test/ sits directly under extension/, so bin/ is one level up. */
const EXTENSION_ROOT = path.join(__dirname, '..');
const CORE = path.join(
  EXTENSION_ROOT,
  'bin',
  goTarget(),
  process.platform === 'win32' ? 'composure.exe' : 'composure',
);

// Copied next to the compiled test by build.mjs, the same way the stub is.
const FIXTURE = path.join(__dirname, 'testdata', 'compose.fixture.yml');
/**
 * A fixture that actually trips rules.
 *
 * The diagnostics test used FIXTURE, which is deliberately boring and produces
 * no findings at all — so the loop that checked the finding-to-field join never
 * ran a single iteration.
 */
const FINDINGS_FIXTURE = path.join(__dirname, 'testdata', 'compose.findings.yml');
/**
 * A file whose mappings are free-form, and one whose keys the schema names.
 *
 * Separate from FIXTURE because that one is deliberately boring and declares no
 * mapping at all, so the mark this fixture is about could not be observed on it
 * — and a fixture that cannot show the property is a test that cannot fail.
 */
const FREEFORM_FIXTURE = path.join(__dirname, 'testdata', 'compose.freeform.yml');
/** A two-stage build, for the vocabulary that crosses the boundary (7.8). */
const DOCKERFILE_FIXTURE = path.join(__dirname, 'testdata', 'Dockerfile.fixture');

const haveCore = existsSync(CORE);
const MISSING = `no core binary at ${CORE} — run \`make extension-core\` (it is gitignored, so a fresh checkout has none)`;
/**
 * Whether a run without the binary is a failure rather than a skip.
 *
 * `make extension-test` sets it, and it builds the core in the same rule, so it
 * can only be missing there if the build silently produced nothing. GitHub sets
 * `CI` on every runner and the workflow runs `make extension-core` before the
 * suite, so the same holds there. Everywhere else — a contributor with no Go
 * toolchain — the suite still runs, and says what it did not check.
 */
const coreRequired = process.env.COMPOSURE_REQUIRE_CORE === '1' || process.env.CI === 'true' || process.env.CI === '1';
const skip = haveCore ? false : MISSING;

if (!haveCore) {
  // stderr, at load time, whatever the reporter is doing with stdout. A run
  // that did not exercise the boundary must not be able to look like one that
  // did, and this is the only line that says so at a glance.
  console.error(
    `\n!!! REAL-CORE BOUNDARY NOT EXERCISED: ${MISSING}\n` +
      '!!! The protocol revision and every wire key crossing Go/TypeScript went unchecked in this run.\n',
  );
}

// Always registered, never skipped by the `skip` above: this is the check that
// says whether the checks below ran, and a check that skips with them would be
// worth nothing. It is the one test in the suite whose result is about the run
// rather than about the product.
describe('the real core binary’s contract tests', () => {
  it('actually ran, or this run does not stand for the boundary', (t) => {
    if (!haveCore && !coreRequired) {
      // Reported as a skip, so the suite's `skipped` count leaves zero and the
      // difference between "683 passed" and "683 passed, 1 skipped" is on the
      // summary line the reader already looks at.
      t.skip(MISSING);
      return;
    }
    assert.ok(
      haveCore,
      `${MISSING}\nCOMPOSURE_REQUIRE_CORE (or CI) is set, so this run is one that must exercise the boundary.`,
    );
  });
});

/**
 * The node the panel would have selected in order to show this path.
 *
 * `services.db.environment.POSTGRES_PASSWORD` is shown in the pane for
 * `services.db`; `volumes.scratch` is its own node. Two segments either way.
 */
function serviceOf(configPath: string): string {
  const parts = configPath.split('.');
  return parts.length >= 2 ? `${parts[0]}.${parts[1]}` : configPath;
}

/**
 * Every config path `stack/schema` gives the inspector a place for.
 *
 * Three sources, and the middle one is the whole point: the node itself (a
 * node-level finding goes at the head of the pane), every field, and every
 * ENTRY of a declared mapping at any depth — which is where `environment`'s
 * keys live, since `environment` arrives as one field with no children.
 */
function renderedPaths(schema: StackSchema): string[] {
  const out: string[] = [];
  const node = schema.node;
  if (!node) {
    return out;
  }
  out.push(node.path);
  const value = (v: ValueView | undefined): void => {
    for (const entry of v?.entries ?? []) {
      out.push(entry.path);
      value(entry.value);
    }
  };
  const fields = (list: SchemaField[] | undefined): void => {
    for (const f of list ?? []) {
      out.push(f.path);
      value(f.value);
      fields(f.children);
    }
  };
  fields(node.fields);
  return out;
}

describe('the real core binary', { skip }, () => {
  async function connect(): Promise<ComposureCore> {
    const core = new ComposureCore({
      binaryPath: CORE,
      log: () => {},
      onExit: () => {},
    });
    await core.start(15000);
    return core;
  }

  // The handshake the extension actually performs. core.start() already refuses
  // a mismatch with a `protocol` CoreError, so reaching this line at all proves
  // the two constants agree; the explicit assertion is here so the failure
  // names the drift rather than reporting a generic startup error.
  it('agrees with this client on the protocol revision', async () => {
    const core = new ComposureCore({
      binaryPath: CORE,
      log: () => {},
      onExit: () => {},
    });
    try {
      await core.start(15000);
      const handshake = await core.request<{
        serverName: string;
        version: string;
        protocol: number;
      }>('initialize', {}, 15000);
      assert.equal(
        handshake.protocol,
        PROTOCOL_REVISION,
        `the core reports protocol ${handshake.protocol} and this client speaks ${PROTOCOL_REVISION}: ` +
          'protocolRevision in cmd/composure/serve.go and PROTOCOL_REVISION in extension/host/core.ts ' +
          'were bumped on one side only',
      );
      assert.equal(handshake.serverName, 'composure');
      assert.notEqual(handshake.version, '');
    } finally {
      core.dispose();
    }
  });

  // The payload the Go core produces, read by the client's own reader and laid
  // out by the client's own layout. Nothing else in either suite proves these
  // two halves fit: the Go tests assert the wire keys and the TypeScript tests
  // assert the reader, but only over bytes the same language wrote.
  it('produces a topology this client can read and lay out', async () => {
    const core = await connect();
    try {
      const answer = await core.request<unknown>('stack/topology', { path: FIXTURE, profiles: [] }, 15000);
      const { graph, droppedEdges } = readGraph(answer);

      assert.equal(droppedEdges, 0, 'the core emitted an edge with an endpoint that is not a node');
      assert.deepEqual(
        graph.nodes.filter((n) => n.kind === 'service').map((n) => n.id),
        ['services.api', 'services.db', 'services.web'],
      );
      assert.deepEqual(
        graph.nodes.filter((n) => n.kind === 'service').map((n) => n.image),
        ['ghcr.io/example/api:2.1', 'postgres:16', 'nginx:1.27'],
        'the image did not survive the wire',
      );
      // Two published ports on web, each its own node with its own position.
      assert.deepEqual(
        graph.nodes.filter((n) => n.kind === 'port').map((n) => n.name),
        ['8080:80', '8443:443'],
      );
      // The one relation this fixture declares, with the condition the short
      // array form implies.
      const depends = graph.edges.filter((e) => e.kind === 'depends_on');
      assert.equal(depends.length, 1);
      assert.equal(depends[0].from, 'services.api');
      assert.equal(depends[0].to, 'services.db');
      assert.equal(edgeLabel(depends[0]), 'service_started');
      assert.equal(graph.edges.filter((e) => e.kind === 'publish').length, 2);

      // Provenance, R1.8: every node knows where it was written.
      for (const node of graph.nodes) {
        assert.equal(node.origin.file, FIXTURE, `${node.name} does not carry its source file`);
        assert.ok(node.origin.line > 0, `${node.name} carries no line`);
      }
      // And the layout the canvas would draw: api above db, because api waits
      // on it, and every node placed exactly once.
      const { positions, rows } = layoutGraph(graph, {});
      assert.equal(Object.keys(positions).length, graph.nodes.length);
      assert.ok(
        positions['services.api'].y < positions['services.db'].y,
        'dependency order does not read top to bottom',
      );
      assert.equal(rows.flat().length, graph.nodes.length, 'a node is unreachable by keyboard');
      assert.equal(describeNode(graph.nodes.find((n) => n.id === 'services.web')!), 'nginx:1.27');
    } finally {
      core.dispose();
    }
  });

  // A profile set is part of the request, not session state on the core.
  it('takes the profile set per request', async () => {
    const core = await connect();
    try {
      const answer = await core.request<unknown>(
        'stack/topology',
        { path: FIXTURE, profiles: ['nothing-declares-this'] },
        15000,
      );
      const { graph } = readGraph(answer);
      assert.deepEqual(graph.profiles, ['nothing-declares-this']);
      // Every service in the fixture is always-active, so an unknown profile
      // adds nothing and removes nothing.
      assert.equal(graph.nodes.filter((n) => n.kind === 'service').length, 3);
    } finally {
      core.dispose();
    }
  });

  // Story 4.6, against the real binary. The stub-driven suites prove the
  // extension SENDS the set; only this proves that sending it changes the
  // answer, that the answer names the profiles the control is built from, and
  // that the resource inventory survives the toggle (DECISIONS.md 19, which is
  // recorded as backed by no test in either direction).
  describe('a profile toggle changes the topology and nothing else', () => {
    const PROFILES_FIXTURE = path.join(__dirname, 'testdata', 'compose.profiles.yml');

    const idsOf = (graph: { nodes: { id: string; kind: string }[] }, kind: string): string[] =>
      graph.nodes.filter((n) => n.kind === kind).map((n) => n.id).sort();

    it('adds the profile’s services, keeps every resource, and says what it dropped', async () => {
      const core = await connect();
      try {
        const off = readGraph(
          await core.request<unknown>('stack/topology', { path: PROFILES_FIXTURE, profiles: [] }, 15000),
        ).graph;
        const on = readGraph(
          await core.request<unknown>(
            'stack/topology',
            { path: PROFILES_FIXTURE, profiles: ['debug'] },
            15000,
          ),
        ).graph;

        // The toggle is connected: one service appears, and it is the one the
        // profile names.
        assert.deepEqual(idsOf(off, 'service'), ['services.web']);
        assert.deepEqual(idsOf(on, 'service'), ['services.tools', 'services.web']);

        // DECISIONS.md 19: the resource inventory is stable across the toggle.
        for (const kind of ['network', 'volume']) {
          assert.deepEqual(
            idsOf(off, kind),
            idsOf(on, kind),
            `the ${kind} inventory changed when a profile was switched on`,
          );
        }
        assert.deepEqual(idsOf(on, 'network'), ['networks.frontnet']);
        assert.deepEqual(idsOf(on, 'volume'), ['volumes.data']);

        // The reference the filter broke is reported rather than dropped, with
        // the reason the canvas puts on the node.
        const dropped = off.dangling.filter((d) => d.ref === 'tools');
        assert.equal(dropped.length, 1, 'a reference to a filtered service vanished silently');
        assert.equal(dropped[0].from, 'services.web');
        assert.match(dropped[0].reason, /profile/, `the reason given was "${dropped[0].reason}"`);
        assert.deepEqual(
          on.dangling.filter((d) => d.ref === 'tools'),
          [],
          'the reference is still reported as broken with the profile on',
        );
        // And with the profile on it is a real edge again.
        assert.ok(
          on.edges.some((e) => e.kind === 'depends_on' && e.to === 'services.tools'),
          'switching the profile on did not restore the relationship',
        );
      } finally {
        core.dispose();
      }
    });

    // This check replaces one that could not fail, and the reason is worth
    // keeping: it looped `for (const profiles of [[], ['debug']])` and passed
    // `profiles` to `stack/schema`. That method has no such parameter
    // (`schemaParams`, cmd/composure/serve.go), and the server decodes with a
    // plain `json.Unmarshal` and no `DisallowUnknownFields` — so both
    // iterations sent BYTE-IDENTICAL requests, got identical answers, and the
    // stated property ("the list does not move with the active set") had no
    // way of being false. Two passes of the same assertion is one assertion.
    //
    // What is checked instead are two things `stack/schema` can genuinely get
    // wrong, and one of them is the actual criterion.
    it('names every declared profile whatever is asked about, including one no node can name', async () => {
      const core = await connect();
      try {
        const declaredAt = async (at: string): Promise<string[]> =>
          readSchema(
            await core.request<unknown>('stack/schema', { path: PROFILES_FIXTURE, at }, 15000),
          ).profiles;

        // 1. The list is the PROJECT's, not the selected node's. The panel asks
        //    `stack/schema` at whatever is selected and builds the control out
        //    of the answer, so a `profiles` scoped to the node would make the
        //    toolbar change shape as the reader clicks around — and go empty on
        //    `web`, which declares no profile at all. This can fail: it is a
        //    claim about `declaredProfiles`' scope.
        for (const at of ['', 'services.web', 'services.tools']) {
          assert.deepEqual(
            await declaredAt(at),
            ['debug'],
            `stack/schema at "${at}" reported a profile list scoped to the selection`,
          );
        }

        // 2. THE CRITERION: the declared list must name a profile that NO node
        //    of the drawn graph can name. With Compose's default set, `tools`
        //    is filtered out, so `debug` appears on nothing the webview holds —
        //    a control derived from the canvas would be empty, and the reader
        //    would have no way to switch the profile back on. This is what
        //    makes "taken from the core's own answer" a requirement.
        const drawn = readGraph(
          await core.request<unknown>(
            'stack/topology',
            { path: PROFILES_FIXTURE, profiles: [] },
            15000,
          ),
        ).graph;
        const fromNodes = [...new Set(drawn.nodes.flatMap((n) => n.profiles ?? []))].sort();
        assert.deepEqual(
          fromNodes,
          [],
          'the fixture leaks its profile onto a drawn node, so a canvas-derived control would ' +
            'look identical to the core-derived one and this check could not fail',
        );
        assert.deepEqual(
          await declaredAt(''),
          ['debug'],
          'the core does not report a profile whose only service it just filtered out',
        );
      } finally {
        core.dispose();
      }
    });

    it('diagnoses and computes impact under the same set the graph was built from', async () => {
      const core = await connect();
      try {
        // `stack/diagnose` echoes the set it ran under, so the join the panel
        // relies on — one profile set, three questions — is observable.
        const report = readReport(
          await core.request<unknown>(
            'stack/diagnose',
            { path: PROFILES_FIXTURE, profiles: ['debug'] },
            15000,
          ),
        );
        assert.deepEqual(report.profiles, ['debug'], 'the core did not run the rules under the set it was given');

        // And impact: `services.tools` is not a node at all with the profile
        // off, which is why the panel must send the same set here.
        const impact = await core.request<{ dependents: string[] }>(
          'stack/impact',
          { path: PROFILES_FIXTURE, at: 'services.tools', profiles: ['debug'] },
          15000,
        );
        assert.deepEqual(impact.dependents, ['services.web']);
        await assert.rejects(
          core.request('stack/impact', { path: PROFILES_FIXTURE, at: 'services.tools', profiles: [] }, 15000),
          'the blast radius of a filtered-out service was answered instead of refused',
        );
      } finally {
        core.dispose();
      }
    });
  });

  // Story 5.2, against the real generated list. This is the only test in the
  // repository that proves the inspector's differentiator survives the wire:
  // the keys come from the vendored specification, through Go, through JSON,
  // into the reader that puts them on screen. A renamed struct tag anywhere on
  // that path draws an empty `available, not set` line and nothing else fails.
  it('offers the generated available-not-set list this client can render', async () => {
    const core = await connect();
    try {
      const answer = await core.request<unknown>(
        'stack/schema',
        { path: FIXTURE, at: 'services.web' },
        15000,
      );
      const schema = readSchema(answer);
      assert.ok(schema.schema_commit.length >= 7, 'the answer does not pin a specification revision');
      const node = schema.node!;
      assert.equal(node.path, 'services.web');
      assert.equal(node.schema, 'service');

      const { lead, available } = groupsOf(node.fields);
      // 5.1: declared keys arrive WITH their values, never as bare keys.
      const image = lead.find((f) => f.key === 'image');
      assert.ok(image, 'image is declared in the fixture');
      assert.equal(image.value?.text, 'nginx:1.27', 'the value did not survive the wire');
      // 5.3: and with a position the pane can render and click.
      const prov = provenanceOf(image.value!);
      assert.ok(prov, 'the value carries no provenance');
      assert.equal(prov.line, 10);
      assert.match(prov.label, /compose\.fixture\.yml:10$/);

      // Ports is a declared sequence: every element, with its own value.
      const ports = lead.find((f) => f.key === 'ports');
      assert.deepEqual(
        (ports?.value?.seq ?? []).map((v) => v.text),
        ['8080:80', '8443:443'],
        'a list rendered as a count instead of its values',
      );

      // 5.2: the rest of the specification, generated and not hand-written.
      assert.ok(
        available.length > 60,
        `only ${available.length} keys offered; the service schema names about ninety`,
      );
      for (const key of ['healthcheck', 'develop', 'ulimits', 'deploy', 'security_opt']) {
        assert.ok(
          available.some((f) => f.key === key),
          `${key} is not offered, and the reader will never learn it exists`,
        );
      }
      assert.equal(
        available.some((f) => f.key === 'image'),
        false,
        'a declared key must not also be offered as available',
      );
      // A default arrives as a placeholder rather than as nothing.
      const restart = available.find((f) => f.key === 'restart');
      assert.ok(restart);
      assert.match(placeholderFor(restart), /^not set/);
    } finally {
      core.dispose();
    }
  });

  /**
   * The mark that says a mapping accepts keys the specification does not name.
   *
   * This is the ONE fact the add-a-key composer is built on, and it is the one
   * fact no other test in this suite can vouch for. Every webview test renders
   * a `SchemaField` with `free_form: true` typed into it by hand, so all of
   * them would keep passing if `internal/schema` stopped sending the field, if
   * the JSON tag were renamed, or if `spec.FreeForm` started answering `true`
   * for every mapping there is. Each of those draws an `+ key` composer on
   * `healthcheck` — a mapping with a closed key set and an `available, not set`
   * list of its own — which is the pane inventing a Compose key list, the exact
   * thing AD-20 forbids.
   *
   * So: the shipped binary, a committed file, and both halves asserted — the
   * free-form mappings carry the mark and the described one does not.
   */
  it('marks a free-form mapping, and only a free-form mapping', async () => {
    const core = await connect();
    try {
      const at = async (node: string): Promise<StackSchema> =>
        readSchema(
          await core.request<unknown>('stack/schema', { path: FREEFORM_FIXTURE, at: node }, 15000),
        );

      const web = (await at('services.web')).node!;
      const declared = web.fields.filter((f) => f.declared);
      const marked = declared.filter((f) => f.free_form === true).map((f) => f.key).sort();
      assert.deepEqual(
        marked,
        ['environment', 'labels'],
        `the core marks ${JSON.stringify(marked)} free-form; the file writes environment and ` +
          'labels as free-form mappings and healthcheck as a described one',
      );
      // Both halves said separately, so a mark that spread to everything and a
      // mark that vanished are two different failures with two different lines.
      const health = declared.find((f) => f.key === 'healthcheck');
      assert.ok(health, 'healthcheck is declared in the fixture');
      assert.notEqual(
        health!.free_form,
        true,
        'a mapping whose keys the specification NAMES was marked free-form — the pane would ' +
          'offer a composer beside its own available-key list',
      );
      assert.equal(health!.value?.kind, 'mapping', 'the two mappings must be the same shape');
      const env = declared.find((f) => f.key === 'environment');
      assert.equal(env!.value?.kind, 'mapping');

      // The other legal form of the SAME key. The mark travels with it, and it
      // is a sequence, which is how the pane knows to offer `+ entry` instead.
      const api = (await at('services.api')).node!;
      const apiEnv = api.fields.find((f) => f.key === 'environment');
      assert.equal(apiEnv?.free_form, true, 'the list form of environment lost the mark');
      assert.equal(apiEnv?.value?.kind, 'sequence');

      // …and one level down, where a hand-maintained list of "the free-form
      // keys" would have to remember `build.args` separately.
      const build = (await at('services.build')).node!.fields.find((f) => f.key === 'build');
      const args = (build?.children ?? []).find((f) => f.key === 'args');
      assert.ok(args, 'build.args is not described at all');
      assert.equal(args!.free_form, true, 'build.args lost the mark a nested field must carry');
    } finally {
      core.dispose();
    }
  });

  // Story 5.4, against the real rules: a finding lands on the field that
  // caused it and on the node that carries the badge.
  it('produces findings this client can place on a field and on a node', async () => {
    const core = await connect();
    try {
      const answer = await core.request<unknown>(
        'stack/diagnose',
        { path: FINDINGS_FIXTURE, profiles: [] },
        15000,
      );
      const report = readReport(answer);
      // Against a fixture that PRODUCES findings. This used to run on the
      // deliberately boring one, where `report.findings` was empty: the loop
      // below never executed, and the severity assertion above it read
      // `count.error + count.warning + count.hint > 0` over `severityCounts`,
      // which only emits an entry for a node that already has at least one
      // finding — true by construction, for every entry, always.
      assert.ok(
        report.findings.length >= 2,
        `the findings fixture produced ${report.findings.length} findings; the rules it is ` +
          'built to trip are healthy-without-healthcheck and plaintext-credential',
      );
      const rules = new Set(report.findings.map((f) => f.rule));
      assert.ok(rules.has('healthy-without-healthcheck'), `rules fired: ${[...rules].join(', ')}`);
      assert.ok(rules.has('plaintext-credential'), `rules fired: ${[...rules].join(', ')}`);

      // The badge counts what is actually there, rather than merely being
      // non-zero: the count for a node equals the findings that name it.
      const ids = ['services.web', 'services.db'];
      const counts = severityCounts(report.findings, ids);
      for (const id of ids) {
        const mine = report.findings.filter((f) => f.subjects.includes(id));
        const count = counts[id] ?? { error: 0, warning: 0, hint: 0 };
        assert.equal(
          count.error + count.warning + count.hint,
          mine.length,
          `${id} badges ${count.error + count.warning + count.hint} of ${mine.length} findings`,
        );
      }

      // Every anchor names a path `stack/schema` DESCRIBES for that node.
      //
      // What was here before could not fail. It joined each finding against its
      // own anchor path — `findingsForField([f], f.anchors[0].path)` — and
      // `findingsForField` starts with `path === id → true`, so the assertion
      // was `1 === 1` for every finding with any path at all, forever. It said
      // nothing about the schema and therefore nothing about the inspector.
      //
      // This asks the real question: the inspector can only put a pill on a path
      // `stack/schema` gave it, and `stack/schema` gives paths two ways —
      // `node.fields[].path`, and `value.entries[].path` for the keys of a
      // declared mapping, at any depth. A finding anchored somewhere neither
      // list reaches is one the pane cannot place. This is the check that would
      // have caught the defect: the anchors are all entry paths, `entryRow` was
      // the only renderer for entry paths, and it was never given the findings.
      let checked = 0;
      for (const f of report.findings) {
        assert.ok(f.anchors.length > 0, 'a finding arrived with no position');
        for (const anchor of f.anchors) {
          assert.ok(anchor.path.length > 0, 'a finding arrived with no config path to join on');
          const node = serviceOf(anchor.path);
          const answer = await core.request<unknown>(
            'stack/schema',
            { path: FINDINGS_FIXTURE, at: node },
            15000,
          );
          const rendered = renderedPaths(readSchema(answer));
          assert.ok(
            rendered.includes(anchor.path),
            `${f.rule} anchors at ${anchor.path}, which stack/schema does not describe under ` +
              `${node === '' ? 'the stack' : node} — the pane has no row to put it on. ` +
              `It offers: ${rendered.slice(0, 12).join(', ')}…`,
          );
          checked += 1;
        }
      }
      assert.ok(checked >= 3, `only ${checked} anchors were checked`);

      // Named, not merely counted. `webview/testdom.test.ts` renders a fixture
      // copied from this exact shape; if the core stops anchoring inside a
      // mapping entry, that suite becomes a test of a shape nothing produces and
      // only this assertion says so.
      const anchored = report.findings.flatMap((f) => f.anchors.map((a) => a.path));
      assert.ok(
        anchored.includes('services.db.environment.POSTGRES_PASSWORD'),
        `plaintext-credential no longer anchors at a mapping ENTRY: ${anchored.join(', ')}`,
      );
    } finally {
      core.dispose();
    }
  });

  /**
   * Story 7.8's boundary, and the one assertion that spans it.
   *
   * `internal/dockerfile/vocabulary.go` writes these keys as struct tags and
   * `shared/protocol.ts` reads them by name. A renamed tag compiles clean on
   * both sides and draws a stage group with nothing under it — which looks
   * exactly like a Dockerfile that uses every instruction there is.
   */
  it('sends a stage vocabulary this client can render', async () => {
    const core = await connect();
    try {
      const answer = (await core.request<DockerfileForm>(
        'stack/dockerfile',
        { path: DOCKERFILE_FIXTURE },
        15000,
      )) as DockerfileForm;

      assert.equal(answer.stages.length, 2, 'the fixture is a two-stage build');
      assert.ok(answer.vocabulary, 'the form carries no file-level vocabulary');

      const runtime = answer.stages[1];
      assert.ok(runtime.vocabulary, 'a stage arrived with no vocabulary: the panel would draw no available list');
      assert.equal(runtime.vocabulary.scope, 'stage');
      const names = runtime.vocabulary.instructions.map((e) => e.name);
      assert.equal(
        runtime.vocabulary.declared_count + runtime.vocabulary.available_count,
        names.length,
        'the counted split and the list disagree',
      );
      // The split itself, per stage: the runtime stage uses COPY and not RUN,
      // and the builder is the other way round. A vocabulary computed for the
      // FILE would report both as declared in both stages.
      const declared = new Set(
        runtime.vocabulary.instructions.filter((e) => e.declared).map((e) => e.name),
      );
      assert.equal(declared.has('COPY'), true, 'the runtime stage declares COPY and the wire says otherwise');
      assert.equal(declared.has('RUN'), false, 'the builder’s RUN leaked into the runtime stage');
      assert.equal(declared.has('FROM'), true);

      // What the reader is here for: the half the stage does NOT use, with a
      // sentence on each, and the deprecated one struck through rather than
      // dropped.
      const available = runtime.vocabulary.instructions.filter((e) => !e.declared);
      for (const want of ['HEALTHCHECK', 'USER', 'ENTRYPOINT', 'CMD', 'STOPSIGNAL', 'SHELL', 'VOLUME', 'ONBUILD']) {
        const entry = available.find((e) => e.name === want);
        assert.ok(entry, `${want} is not offered on a stage that does not use it`);
        assert.ok(entry!.summary.length > 0, `${want} arrives with no description`);
      }
      const maintainer = runtime.vocabulary.instructions.find((e) => e.name === 'MAINTAINER');
      assert.ok(maintainer, 'MAINTAINER vanished from the vocabulary');
      assert.equal(maintainer!.deprecated, true);
      assert.ok((maintainer!.deprecated_note ?? '').length > 0, 'a deprecation with no reason is a strike-through nobody can act on');
      assert.equal(maintainer!.declared, true, 'the fixture uses MAINTAINER in this stage');
      assert.ok((maintainer!.indices ?? []).length > 0, 'no index to jump to the use');
    } finally {
      core.dispose();
    }
  });

  // A refused file must arrive as a positioned JSON-RPC error from the real
  // core, not just from the stub that was written to produce one.
  it('refuses a malformed file with a position', async () => {
    const core = await connect();
    try {
      const bad = path.join(__dirname, 'testdata', 'compose.malformed.yml');
      await assert.rejects(
        core.request('stack/topology', { path: bad, profiles: [] }, 15000),
        (err: unknown) => {
          const e = err as { code?: number; data?: { file?: string; line?: number } };
          assert.equal(e.code, -32001);
          assert.ok((e.data?.line ?? 0) > 0, 'the core reported no position');
          return true;
        },
      );
    } finally {
      core.dispose();
    }
  });
});
