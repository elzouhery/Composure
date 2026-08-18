// Finding a service in a stack too big to scan — story 4.4.
//
// Three ways of looking at a graph the core already sent: search, focus, and
// collapse. None of them is a fact about the stack, so none of them goes
// anywhere near a compose file, and none of them is computed by asking the core
// a different question. Search and collapse are views over the nodes already on
// screen; focus is the core's own `stack/impact` answer, dimmed.
//
// Every decision lives here rather than in a DOM callback, for the reason
// layout.ts exists: four defects shipped in 4.1 from logic buried in listeners
// no test could reach.
//
// THE ONE RULE OF SEARCH: it must not move a box. A search that re-lays out the
// graph destroys the spatial memory it exists to serve — the reader was looking
// for the thing they half-remember the position of. So search returns a SET OF
// IDS and nothing else; the renderer toggles classes and does not re-place a
// single node. Collapse is the one operation here that legitimately changes the
// node set, and expanding it restores the exact positions it started from,
// because the position map it was given was never mutated.

import { describeNode, kindWord } from './layout';
import { GROUP_PREFIX, isGroupId } from '../shared/protocol';
import type { CollapsedDetail, GraphEdge, GraphNode, Point, StackGraph } from '../shared/protocol';

/* -------------------------------------------------------------------------
 * Search.
 * ---------------------------------------------------------------------- */

/**
 * The text a node is searched by.
 *
 * The name, the config path, and whatever the node's second line says — which
 * for a service is its image, and an image is the other thing a reader hunts a
 * forty-service stack for. The kind is included so `network` finds the
 * networks, which is what typing the word plainly means.
 */
export function searchHaystack(node: GraphNode): string {
  return [node.name, node.id, node.image ?? '', describeNode(node), kindWord(node.kind)]
    .join(' ')
    .toLowerCase();
}

/** Whether one node matches. Plain case-insensitive substring; no globbing. */
export function matchesQuery(node: GraphNode, query: string): boolean {
  const q = query.trim().toLowerCase();
  return q === '' ? false : searchHaystack(node).includes(q);
}

/** Every node matching a query, by config path. Empty for an empty query. */
export function searchMatches(nodes: GraphNode[], query: string): Set<string> {
  const out = new Set<string>();
  if (query.trim() === '') {
    return out;
  }
  for (const node of nodes) {
    if (matchesQuery(node, query)) {
      out.add(node.id);
    }
  }
  return out;
}

/**
 * What the search field says about its own result.
 *
 * A search matching nothing is the case the acceptance criterion singles out:
 * the graph is left exactly as it was and the field says so, rather than
 * dimming every node in the stack and leaving the reader looking at a uniformly
 * grey canvas wondering whether the tool broke.
 */
export function searchStatus(query: string, matched: number, total: number): string {
  if (query.trim() === '') {
    return '';
  }
  if (matched === 0) {
    return `Nothing matches “${query.trim()}”. The graph is unchanged.`;
  }
  const nodes = total === 1 ? '1 node' : `${total} nodes`;
  return `${matched} of ${nodes} match “${query.trim()}”.`;
}

/* -------------------------------------------------------------------------
 * Emphasis — what search and focus mode both produce.
 * ---------------------------------------------------------------------- */

/**
 * The nodes to bring forward, or null for "emphasise nothing".
 *
 * Null and the empty set are DIFFERENT and the difference is the whole of the
 * no-match criterion: null means no filter is active, the empty set would mean
 * a filter matched nothing. A renderer handed the empty set would dim every
 * node; a renderer handed null dims none. So `emphasis` collapses the empty
 * match back to null on purpose, and the sentence carries the news instead.
 */
export function emphasis(matched: Set<string>): Set<string> | null {
  return matched.size === 0 ? null : matched;
}

/* -------------------------------------------------------------------------
 * Focus mode — the blast radius, from the core.
 * ---------------------------------------------------------------------- */

/**
 * The focus set for a `stack/impact` answer: the node itself, everything that
 * depends on it, and everything it depends on.
 *
 * The transitive closure is `topology.BlastRadius`'s and is never recomputed
 * here. A second implementation of "what breaks if this goes down" is a second
 * answer, and the two would part company the first time either changed.
 */
export function impactSet(id: string, dependents: string[], dependencies: string[]): Set<string> {
  return new Set<string>([id, ...dependents, ...dependencies]);
}

/** What focus mode says it is showing. Counts in words, never a bare number. */
export function focusStatus(name: string, dependents: number, dependencies: number): string {
  const up =
    dependents === 0
      ? 'nothing depends on it'
      : dependents === 1
        ? '1 thing depends on it'
        : `${dependents} things depend on it`;
  const down =
    dependencies === 0
      ? 'it depends on nothing'
      : dependencies === 1
        ? 'it depends on 1 thing'
        : `it depends on ${dependencies} things`;
  return `${name} — ${up}, ${down}.`;
}

/* -------------------------------------------------------------------------
 * Collapse.
 * ---------------------------------------------------------------------- */

// How a collapsed group's id is spelled, and the test for one. Defined in
// `shared/protocol` because the id crosses the boundary — the host has to be
// able to tell a group the canvas invented from a config path the core knows —
// and re-exported here so the collapse code reads in one place.
export { GROUP_PREFIX, isGroupId };

/**
 * The smallest group worth folding.
 *
 * A group of one folds a node into a box that says "1 service" — strictly more
 * chrome for strictly less information, and the reader loses the name.
 */
export const MIN_GROUP = 2;

export type CollapseBy = 'network' | 'profile';

export interface CollapsedView {
  /** The graph to draw: members replaced by one node per group. */
  graph: StackGraph;
  /** Member config path → the group id it folded into. */
  memberOf: Record<string, string>;
  /** Group id → the member config paths, in the order they folded. */
  members: Record<string, string[]>;
}

/**
 * Folds a graph by network or by profile.
 *
 * A service belongs to whichever group it declares FIRST — a service on three
 * networks cannot be in three boxes at once, and picking the first is
 * deterministic, explicable and stable across redraws. Everything hanging off a
 * folded service folds with it: its published ports and its Dockerfile node are
 * about that service, and leaving them behind would draw a satellite orbiting
 * nothing.
 *
 * Nothing here is persisted and nothing here is sent to the core. The group ids
 * are synthetic and are filtered out before positions are stored — node
 * identity in this product is the config path, and a group has none.
 */
export function collapse(graph: StackGraph, by: CollapseBy): CollapsedView {
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const keyOf = new Map<string, string>();

  if (by === 'network') {
    for (const e of graph.edges) {
      if (e.kind !== 'network' || keyOf.has(e.from)) {
        continue;
      }
      if (byId.get(e.from)?.kind === 'service' && byId.has(e.to)) {
        keyOf.set(e.from, e.to);
      }
    }
  } else {
    for (const node of graph.nodes) {
      if (node.kind === 'service' && node.profiles.length > 0) {
        keyOf.set(node.id, node.profiles[0]);
      }
    }
  }

  // Satellites follow their service: the Dockerfile node it builds and the
  // ports it publishes.
  for (const e of graph.edges) {
    if (e.kind !== 'build' && e.kind !== 'publish') {
      continue;
    }
    const key = keyOf.get(e.from);
    if (key !== undefined && byId.has(e.to) && !keyOf.has(e.to)) {
      keyOf.set(e.to, key);
    }
  }

  // Group in node order, so the fold is stable across draws.
  const grouped = new Map<string, string[]>();
  for (const node of graph.nodes) {
    const key = keyOf.get(node.id);
    if (key === undefined) {
      continue;
    }
    (grouped.get(key) ?? grouped.set(key, []).get(key)!).push(node.id);
  }
  // A group counts only the services in it: "3 services" must not become
  // "9 services" because each one publishes two ports.
  const serviceCount = (ids: string[]): number =>
    ids.filter((id) => byId.get(id)?.kind === 'service').length;
  for (const [key, ids] of [...grouped]) {
    if (serviceCount(ids) < MIN_GROUP) {
      grouped.delete(key);
    }
  }

  const memberOf: Record<string, string> = {};
  const members: Record<string, string[]> = {};
  const groupNodes: GraphNode[] = [];
  for (const [key, ids] of grouped) {
    const id = `${GROUP_PREFIX}${by}:${key}`;
    members[id] = ids;
    for (const member of ids) {
      memberOf[member] = id;
    }
    groupNodes.push(groupNode(id, key, by, ids, byId));
  }

  const kept = graph.nodes.filter((n) => memberOf[n.id] === undefined);
  const map = (id: string): string => memberOf[id] ?? id;

  const seen = new Set<string>();
  const edges: GraphEdge[] = [];
  for (const e of graph.edges) {
    const from = map(e.from);
    const to = map(e.to);
    if (from === to) {
      continue; // folded into the same box; the relation is now inside it
    }
    const key = `${e.kind} ${from} ${to}`;
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    edges.push({ ...e, from, to });
  }

  const dangling = graph.dangling.map((d) => ({ ...d, from: map(d.from), to: map(d.to) }));
  const cycles = graph.cycles
    .map((cycle) => [...new Set(cycle.map(map))])
    .filter((cycle) => cycle.length > 1);

  return {
    graph: {
      profiles: graph.profiles,
      nodes: [...kept, ...groupNodes],
      edges,
      cycles,
      dangling,
      max_layer: graph.max_layer,
    },
    memberOf,
    members,
  };
}

/**
 * The stand-in node for a folded group.
 *
 * It is drawn as a service because a service box is the one with room for a
 * name and a second line, and it carries `collapsed` so the renderer can mark
 * it and `describeNode` can say what it is. It sits at the shallowest layer any
 * of its members occupies: the group is reached as soon as its first member is.
 */
function groupNode(
  id: string,
  key: string,
  by: CollapseBy,
  ids: string[],
  byId: Map<string, GraphNode>,
): GraphNode {
  const first = byId.get(ids[0])!;
  const services = ids.filter((m) => byId.get(m)?.kind === 'service');
  const collapsed: CollapsedDetail = { by, count: services.length, members: ids };
  return {
    id,
    kind: 'service',
    name: by === 'network' ? lastSegmentOf(key) : key,
    origin: byId.get(key)?.origin ?? first.origin,
    declared: true,
    external: false,
    profiles: [],
    layer: Math.min(...ids.map((m) => byId.get(m)?.layer ?? 0)),
    collapsed,
  };
}

function lastSegmentOf(id: string): string {
  const parts = id.split('.');
  return parts[parts.length - 1] || id;
}

/**
 * Where a group node goes: the centre of the boxes it replaced.
 *
 * Not the origin, and not a fresh band. The reader collapses a group to make
 * room, and finding the resulting box in the same part of the canvas the group
 * occupied is the difference between a fold and a redraw.
 */
export function groupPosition(
  ids: string[],
  positions: Record<string, Point>,
): Point | null {
  const placed = ids.map((id) => positions[id]).filter((p): p is Point => p !== undefined);
  if (placed.length === 0) {
    return null;
  }
  const x = placed.reduce((sum, p) => sum + p.x, 0) / placed.length;
  const y = placed.reduce((sum, p) => sum + p.y, 0) / placed.length;
  return { x: Math.round(x), y: Math.round(y) };
}

/**
 * Positions for a collapsed draw: every node keeps exactly where it was, and
 * each group node lands at the centre of its members.
 *
 * The input map is never mutated — it is the map expanding restores from, and
 * the acceptance criterion is that expanding restores EXACTLY the previous
 * positions. A mutated map would restore something close instead.
 */
export function collapsedPositions(
  view: CollapsedView,
  positions: Record<string, Point>,
): Record<string, Point> {
  const out: Record<string, Point> = { ...positions };
  for (const [id, ids] of Object.entries(view.members)) {
    const p = groupPosition(ids, positions);
    if (p !== null) {
      out[id] = p;
    }
  }
  return out;
}

/**
 * What the collapse control says it did.
 *
 * Nothing to fold is stated rather than silently doing nothing: a stack with
 * one network per service has no groups, and a button that appears to do
 * nothing is indistinguishable from a broken one.
 */
export function collapseStatus(by: CollapseBy, view: CollapsedView): string {
  const groups = Object.keys(view.members).length;
  if (groups === 0) {
    return by === 'network'
      ? 'No network has two or more services on it, so there is nothing to fold.'
      : 'No profile has two or more services in it, so there is nothing to fold.';
  }
  const folded = Object.values(view.members).flat().length;
  const word = groups === 1 ? '1 group' : `${groups} groups`;
  return `${folded} nodes folded into ${word} by ${by}.`;
}

/* -------------------------------------------------------------------------
 * Profiles — story 4.6, R1.4.
 * ---------------------------------------------------------------------- */

/**
 * What the panel says while a profile filter is on.
 *
 * Two facts, and neither is optional.
 *
 *   1. THIS IS NOT THE WHOLE STACK. A filtered graph that presents itself as
 *      the complete one is the graph lying by omission — the reader compares
 *      dev against prod precisely because they cannot hold both in their head,
 *      and a canvas missing three services with nothing said is worse than no
 *      canvas at all.
 *   2. The resources are NOT filtered (DECISIONS.md 19). `topology.Build`
 *      filters services and deliberately leaves top-level networks, volumes,
 *      configs and secrets alone, so a filtered stack can show a network with
 *      nothing attached to it. That is a fact about the project rather than a
 *      rendering fault, and it has to be said here or it reads as one.
 *
 * The empty set is Compose's own default and is not a filter, so it says
 * nothing at all: a permanent banner over an unfiltered stack is noise, and
 * noise is what makes the filtered case unremarkable.
 */
export function profileNotice(active: string[]): string {
  if (active.length === 0) {
    return '';
  }
  const names = active.join(', ');
  const lead =
    active.length === 1
      ? `Filtered stack — the ${names} profile is on.`
      : `Filtered stack — the ${names} profiles are on.`;
  // "Services that DECLARE A PROFILE none of these activate", not "services no
  // active profile selects". The second reading is false about most of every
  // real compose file: a service that declares no profile at all is drawn under
  // every set — `topology.Build` keeps it — and no active profile selects it
  // either. The old wording therefore told the reader that the majority of
  // their stack had been removed while it sat on the canvas in front of them.
  return (
    `${lead} Services that declare a profile none of these activate are not drawn. ` +
    'Networks, volumes, configs and secrets are never filtered, so one may appear here with ' +
    'nothing attached to it.'
  );
}

/**
 * What auto-arrange says it did — story 4.2's last criterion, read backwards.
 *
 * A dragged position is view state that persists per workspace forever, so
 * after one drag the layout is partly manual and there was no way back. The
 * control that undoes it has to SAY what it undid, for two reasons: the boxes
 * that did not move give a keyboard reader no evidence anything happened, and
 * discarding positions is destructive — quietly throwing away the arrangement
 * someone spent five minutes on is worse than not offering the button.
 *
 * Nothing to discard is stated rather than silently doing nothing, exactly as
 * `collapseStatus` states an empty fold: a control that appears to do nothing
 * is indistinguishable from a broken one.
 */
export function arrangeStatus(discarded: number): string {
  if (discarded === 0) {
    return 'Every node is already where the layout put it — nothing to re-arrange.';
  }
  // "Stored positions", not "nodes you dragged". One drag persists the position
  // of EVERY box on the canvas — `graph.ts onMove` posts `currentPositions()`,
  // because the arrangement a reader is keeping is the whole picture and not
  // the one box they last touched — so a sentence counting dragged nodes would
  // say 13 after one drag. This counts what is actually being thrown away.
  const stored = discarded === 1 ? '1 stored position' : `${discarded} stored positions`;
  return `Re-arranged: ${stored} discarded. Every node is where the layout puts it.`;
}

/**
 * Positions worth persisting: the real nodes, never a synthetic group.
 *
 * A group id is not a config path and would sit in workspace state forever,
 * keyed to a way of looking at the graph rather than to anything in the file.
 */
export function persistablePositions(positions: Record<string, Point>): Record<string, Point> {
  const out: Record<string, Point> = {};
  for (const [id, p] of Object.entries(positions)) {
    if (!isGroupId(id)) {
      out[id] = p;
    }
  }
  return out;
}
