// The one thing the host does to the core's answer: check it is shaped like a
// graph before it becomes pixels.
//
// This replaces host/project.ts, which projected service nodes out of
// `stack/resolve` by walking the resolved document in TypeScript. That was a
// second graph model in a second language, and it could only ever produce the
// half of the graph it happened to know how to walk. The graph is derived once,
// in Go, by `internal/topology`, and reaches the canvas through
// `stack/topology`. There is no other way to get one.
//
// So nothing below derives anything. It reads the fields the wire schema
// promises and refuses the answer if they are not there — because the failure
// mode of a missing array is not a crash, it is a canvas that quietly draws
// fewer relationships than the file declares, which is exactly the confident
// wrong answer this project is built to avoid.

import type { GraphEdge, GraphNode, StackGraph } from '../shared/protocol';

/** Thrown when the core's answer is not a graph. Named, never silent. */
export class MalformedGraphError extends Error {
  constructor(readonly detail: string) {
    super(detail);
    this.name = 'MalformedGraphError';
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * Validates a `stack/topology` result and returns it as a StackGraph.
 *
 * The wire schema states that every list is present and empty rather than
 * null, precisely so a client cannot mistake "none" for "not implemented". This
 * holds the core to that: a missing `edges` is a protocol break, not an
 * edgeless stack.
 *
 * Edges pointing at nodes that are not in the node set are dropped and counted
 * rather than kept — an edge to nothing has no geometry, and drawing a line to
 * the origin is worse than not drawing it. The core does not emit them (it
 * reports those as `dangling`), so a non-zero count here is a bug upstream and
 * the caller is told.
 */
export function readGraph(raw: unknown): { graph: StackGraph; droppedEdges: number } {
  if (!isRecord(raw)) {
    throw new MalformedGraphError('the core returned something that is not an object');
  }
  for (const key of ['nodes', 'edges', 'cycles', 'dangling', 'profiles']) {
    if (!Array.isArray(raw[key])) {
      throw new MalformedGraphError(`the core's topology has no "${key}" array`);
    }
  }
  const nodes = (raw.nodes as GraphNode[]).filter(
    (n) => isRecord(n) && typeof n.id === 'string' && n.id !== '' && typeof n.kind === 'string',
  );
  if (nodes.length !== (raw.nodes as unknown[]).length) {
    throw new MalformedGraphError('the core returned a node with no id or no kind');
  }

  const known = new Set(nodes.map((n) => n.id));
  const edges: GraphEdge[] = [];
  let droppedEdges = 0;
  for (const e of raw.edges as GraphEdge[]) {
    if (!isRecord(e) || typeof e.from !== 'string' || typeof e.to !== 'string') {
      throw new MalformedGraphError('the core returned an edge with no endpoints');
    }
    if (!known.has(e.from) || !known.has(e.to)) {
      droppedEdges++;
      continue;
    }
    edges.push(e);
  }

  return {
    graph: {
      profiles: raw.profiles as string[],
      nodes,
      edges,
      cycles: raw.cycles as string[][],
      dangling: raw.dangling as StackGraph['dangling'],
      max_layer: typeof raw.max_layer === 'number' ? raw.max_layer : 0,
    },
    droppedEdges,
  };
}
