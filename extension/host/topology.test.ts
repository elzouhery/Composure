// The host's one check on the core's answer.
//
// These cases are all the same shape: the core says something slightly wrong,
// and the question is whether the panel draws a smaller stack than the file
// declares without saying so. That is the failure mode this whole project is
// organised around — the engine does not crash, it returns a confident wrong
// answer — and a missing `edges` array is exactly it: a stack with every node
// and no relationships looks like a stack with no relationships.

import assert from 'node:assert/strict';
import { describe, it } from 'node:test';
import { MalformedGraphError, readGraph } from './topology';

const ORIGIN = { file: 'compose.yml', line: 1, column: 1, step: 0 };

function wire(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    profiles: [],
    nodes: [
      { id: 'services.web', kind: 'service', name: 'web', origin: ORIGIN, declared: true, external: false, profiles: [], layer: 1 },
      { id: 'services.db', kind: 'service', name: 'db', origin: ORIGIN, declared: true, external: false, profiles: [], layer: 0 },
    ],
    edges: [{ kind: 'depends_on', from: 'services.web', to: 'services.db', origin: ORIGIN }],
    cycles: [],
    dangling: [],
    max_layer: 1,
    ...extra,
  };
}

describe('readGraph', () => {
  it('passes a well-formed graph through unchanged', () => {
    const { graph, droppedEdges } = readGraph(wire());
    assert.equal(graph.nodes.length, 2);
    assert.equal(graph.edges.length, 1);
    assert.equal(graph.max_layer, 1);
    assert.equal(droppedEdges, 0);
  });

  it('refuses an answer that is not an object', () => {
    for (const bad of [null, undefined, 42, 'graph', []]) {
      assert.throws(() => readGraph(bad), MalformedGraphError, `accepted ${JSON.stringify(bad)}`);
    }
  });

  // The wire schema states that every list is present and empty rather than
  // null, precisely so a client cannot mistake "none" for "not implemented".
  it('refuses a missing list rather than drawing a stack with fewer relations', () => {
    for (const key of ['nodes', 'edges', 'cycles', 'dangling', 'profiles']) {
      const broken = wire();
      delete broken[key];
      assert.throws(() => readGraph(broken), MalformedGraphError, `accepted a missing ${key}`);
    }
  });

  it('refuses a null list, which is not the same as an empty one', () => {
    assert.throws(() => readGraph(wire({ edges: null })), MalformedGraphError);
  });

  it('refuses a node with no id — identity is the config path, always', () => {
    assert.throws(
      () => readGraph(wire({ nodes: [{ kind: 'service', name: 'web' }] })),
      MalformedGraphError,
    );
    assert.throws(
      () => readGraph(wire({ nodes: [{ id: '', kind: 'service', name: 'web' }] })),
      MalformedGraphError,
    );
  });

  it('refuses an edge with no endpoints', () => {
    assert.throws(
      () => readGraph(wire({ edges: [{ kind: 'depends_on', origin: ORIGIN }] })),
      MalformedGraphError,
    );
  });

  // An edge to a node that is not there has no geometry. The core reports those
  // as `dangling` instead, so a non-zero count is an upstream bug — reported,
  // never absorbed.
  it('drops an edge pointing at a node that is not in the set, and counts it', () => {
    const { graph, droppedEdges } = readGraph(
      wire({
        edges: [
          { kind: 'depends_on', from: 'services.web', to: 'services.ghost', origin: ORIGIN },
          { kind: 'depends_on', from: 'services.web', to: 'services.db', origin: ORIGIN },
        ],
      }),
    );
    assert.equal(droppedEdges, 1);
    assert.equal(graph.edges.length, 1);
    assert.equal(graph.edges[0].to, 'services.db');
  });

  it('keeps an empty graph as an empty graph', () => {
    const { graph } = readGraph(wire({ nodes: [], edges: [] }));
    assert.deepEqual(graph.nodes, []);
    assert.deepEqual(graph.edges, []);
  });

  it('defaults an absent max_layer to zero rather than to NaN', () => {
    const broken = wire();
    delete broken.max_layer;
    assert.equal(readGraph(broken).graph.max_layer, 0);
  });

  it('carries dangling references through — they are the point', () => {
    const { graph } = readGraph(
      wire({
        dangling: [
          {
            kind: 'depends_on',
            from: 'services.web',
            to: 'services.gone',
            ref: 'gone',
            reason: 'filtered by profile',
            origin: ORIGIN,
          },
        ],
      }),
    );
    assert.equal(graph.dangling.length, 1);
    assert.equal(graph.dangling[0].reason, 'filtered by profile');
  });
});
