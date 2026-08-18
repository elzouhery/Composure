// Where the nodes go, how wide they are, and where an edge runs between two of
// them.
//
// This is the one place in the extension that computes anything, and what it
// computes is pixel coordinates. It is pure throughout — no DOM, no `vscode`,
// no process — which is what lets `node --test` cover the arithmetic that
// decides whether a stack is legible. Everything with a decision in it lives
// here rather than in a DOM callback no test can reach; graph.ts is left with
// element creation and event plumbing.
//
// The layered arrangement replaces story 4.1's grid. A grid was the only honest
// arrangement of nodes with no known relationships; now the core reports the
// dependency layer of every node, and an arrangement that ignores it would be
// throwing away the answer to the question the canvas exists to answer.

import type {
  EdgeKind,
  GraphDangling,
  GraphEdge,
  GraphNode,
  NodeKind,
  Point,
  SeverityCount,
  StackGraph,
} from '../shared/protocol';

/* -------------------------------------------------------------------------
 * Sizes.
 *
 * A service is the biggest box because it carries the most text. Everything
 * else is deliberately smaller and a different shape class in the stylesheet: a
 * reader must be able to tell a network from a service without reading either.
 */

/** The service box. Still exported under the old names: it is the default size. */
export const NODE_WIDTH = 188;
/**
 * The service box's height when it carries one image line and no markers.
 *
 * DERIVED, not chosen: it is `DETAIL_Y + BOTTOM_PAD` — the last baseline plus
 * the space under it — and it is asserted against the painted glyphs by
 * `harness/canvas.mjs`, which measures the gap between a card's lowest text
 * bounding box and the bottom of its rectangle.
 *
 * It used to be a flat 94 floor while a one-line service's ink ended at 44,
 * which put **50px of empty box** under every such card and **34px** under the
 * tallest one on `examples/webstack`. That is the loose, unlike-the-mockup
 * reading the owner described: the mockup's card (`directions-3.html:290`) is
 * 188×40 with its second line 9px off the bottom, and ours was 188×94 with
 * half of it empty. The width has always matched; only the height did not.
 */
export const NODE_HEIGHT = 53;
export const RESOURCE_WIDTH = 152;
export const RESOURCE_HEIGHT = 46;
export const PORT_WIDTH = 132;
export const PORT_HEIGHT = 46;
export const DOCKERFILE_WIDTH = 172;
export const DOCKERFILE_HEIGHT = 46;

/** Detail lines a service node will show before it gives up and elides. */
export const DETAIL_LINES = 2;
/**
 * Characters that fit on one detail line.
 *
 * Derived rather than eyeballed: node text is set in the editor font, which is
 * monospace, so a character count IS a width. A monospace advance is ~0.6em,
 * the detail line renders at 0.85em, and the box is 188px with 12px padding
 * either side — so the budget is 164 / (0.6 * 0.85 * fontSize).
 *
 * That yields 24.7 columns at a 13px editor font and 23.0 at 14px, so 23 is
 * the value that holds across the sizes people actually use. It does NOT hold
 * at 16px and above, which is why the node also clips: the font belongs to the
 * reader and no fixed column count can be right for all of them.
 */
export const DETAIL_COLUMNS = 23;
/** Characters of the service name that fit on its line, same derivation. */
export const NAME_COLUMNS = 19;
/** The same budget for the narrower boxes, scaled by width. */
export const RESOURCE_COLUMNS = 18;
export const PORT_COLUMNS = 15;
export const DOCKERFILE_COLUMNS = 21;

export const GAP_X = 36;
export const GAP_Y = 34;
/** Where the Dockerfile node hangs below the service that builds it. */
export const SATELLITE_GAP = 14;
export const SATELLITE_INDENT = 14;

/** Text metrics inside a node, in px. Mirrored by the stylesheet's font sizes. */
export const PAD_X = 12;
export const NAME_Y = 23;
export const DETAIL_Y = 41;
export const LINE_HEIGHT = 16;
export const MARKER_LINE_HEIGHT = 14;
/**
 * The space under a card's LAST BASELINE — not under its last line box.
 *
 * The distinction is the whole of the dead-space defect: `DETAIL_Y` is already
 * a baseline, so adding a full line height for the line that baseline belongs
 * to counted the same line twice. 12 covers the descender plus the inset the
 * mockup leaves (its 9px, at its smaller type).
 */
export const BOTTOM_PAD = 12;

export interface Size {
  width: number;
  height: number;
}

export interface Box extends Point, Size {}

/* -------------------------------------------------------------------------
 * Node text. Pure, because what a node says decides how tall it is.
 */

/** The word for a kind, in the domain's own vocabulary. Never a badge. */
export function kindWord(kind: NodeKind): string {
  return kind === 'dockerfile' ? 'Dockerfile' : kind;
}

/**
 * The stack pane's header summary — `4 services · 1 network · 2 warnings`.
 *
 * Direction A's pane headers carry one and this pane had none, which left the
 * reader with no count of anything until they had scanned the canvas by eye.
 *
 * Spelled out and pluralised, never a badge: `1 network`, not `1n`. Ports are
 * left out on purpose — they are satellites of the services already counted,
 * and counting them again would make a four-service stack claim eleven things.
 * Severity is a word as well as a colour (N6), and is omitted entirely when the
 * rules did not run, because "0 warnings" would be a claim nobody can make.
 */
export function stackSummary(
  nodes: GraphNode[],
  severities: Record<string, SeverityCount>,
  diagnosed = true,
): string {
  const counted: NodeKind[] = ['service', 'network', 'volume', 'config', 'secret', 'dockerfile'];
  const parts: string[] = [];
  for (const kind of counted) {
    const n = nodes.filter((node) => node.kind === kind && !node.collapsed).length;
    if (n === 0) {
      continue;
    }
    const word = kindWord(kind);
    parts.push(n === 1 ? `1 ${word}` : `${n} ${word}s`);
  }
  if (!diagnosed) {
    parts.push('checks did not run');
    return parts.join(' · ');
  }
  const totals = { error: 0, warning: 0, hint: 0 };
  for (const count of Object.values(severities)) {
    totals.error += count.error;
    totals.warning += count.warning;
    totals.hint += count.hint;
  }
  for (const severity of ['error', 'warning'] as const) {
    const n = totals[severity];
    if (n > 0) {
      parts.push(n === 1 ? `1 ${severity}` : `${n} ${severity}s`);
    }
  }
  return parts.join(' · ');
}

/**
 * The second line of a node: what the file says about it, never a second name.
 *
 * A service shows its image, because that is the thing a reader scans a canvas
 * for. Every other kind shows what kind of thing it is and whether it is this
 * project's to manage — `external` and `not declared` are different statements
 * and both are worth more than a decoration.
 */
export function describeNode(node: GraphNode): string {
  // A folded group — story 4.4. It says what it holds and how it was grouped,
  // because a box named `backend` with nothing else on it is indistinguishable
  // from a service called backend.
  if (node.collapsed) {
    const n = node.collapsed.count;
    return `${n} ${n === 1 ? 'service' : 'services'} · collapsed by ${node.collapsed.by}`;
  }
  const tags: string[] = [];
  switch (node.kind) {
    case 'service':
      if (node.image) {
        return node.image;
      }
      return node.declared ? 'no image' : 'not declared';
    case 'port': {
      const p = node.port;
      if (!p) {
        return 'port';
      }
      const host = p.host_ip ? `${p.host_ip}:` : '';
      const published = p.published ? `${host}${p.published} → ` : 'exposed ';
      return `${published}${p.target}/${p.protocol}`;
    }
    case 'dockerfile': {
      const b = node.build;
      if (!b) {
        return 'Dockerfile';
      }
      if (b.inline) {
        return 'inline, in the compose file';
      }
      return b.target ? `target ${b.target}` : `context ${b.context || '.'}`;
    }
    default:
      tags.push(kindWord(node.kind));
      if (!node.declared) {
        tags.push('not declared');
      } else if (node.external) {
        tags.push('external');
      }
      if (node.internal) {
        tags.push('internal');
      }
      return tags.join(' · ');
  }
}

/** The wrapped image lines a service node will draw. Decides its height. */
export function serviceDetailLines(node: GraphNode): string[] {
  return wrapImageRef(describeNode(node), DETAIL_COLUMNS, DETAIL_LINES);
}

/** How many characters a kind's box can hold on one line. */
export function columnsFor(kind: NodeKind): number {
  switch (kind) {
    case 'service':
      return DETAIL_COLUMNS;
    case 'port':
      return PORT_COLUMNS;
    case 'dockerfile':
      return DOCKERFILE_COLUMNS;
    default:
      return RESOURCE_COLUMNS;
  }
}

/**
 * The size of a node's box.
 *
 * Only a service grows: its markers are the bind mounts, dangling references
 * and cycle membership that have nowhere else to go, and a fixed height would
 * either clip them away or leave a hole under every node that has none.
 */
export function nodeSize(node: GraphNode, markers = 0): Size {
  switch (node.kind) {
    case 'port':
      return { width: PORT_WIDTH, height: PORT_HEIGHT };
    case 'dockerfile':
      return { width: DOCKERFILE_WIDTH, height: DOCKERFILE_HEIGHT };
    case 'service': {
      // `DETAIL_Y` is the FIRST detail baseline, so only the lines after it
      // add height. Counting all of them is what put half a card of nothing
      // under every one-line service; `harness/canvas.mjs --check` measures the
      // result on the glyphs so the arithmetic cannot drift back.
      const detail = serviceDetailLines(node).length;
      const content =
        DETAIL_Y + (detail - 1) * LINE_HEIGHT + markers * MARKER_LINE_HEIGHT + BOTTOM_PAD;
      return { width: NODE_WIDTH, height: Math.max(NODE_HEIGHT, content) };
    }
    default:
      return { width: RESOURCE_WIDTH, height: RESOURCE_HEIGHT };
  }
}

/**
 * The lines a service carries that are not edges to anywhere.
 *
 * Three things end up here, and all three are invisible otherwise:
 *
 *   - A bind mount is a self-edge. Its far side is a path on a host that is not
 *     in this file, so there is no node to draw a line to; drawn as an edge it
 *     is a loop from a box back to itself, which reads as a rendering bug. The
 *     host path is the information, so the host path is what the node says.
 *   - A dangling reference is a relation the core dropped because the thing it
 *     named is not in the filtered graph. Dropping it silently is the graph
 *     lying by omission — a profile toggle would remove a line and say nothing.
 *   - Cycle membership. The layout survives a cycle (the members share a layer)
 *     but two edges pointing sideways at each other do not announce themselves,
 *     and a cycle is the reason a stack never starts.
 */
export function markerIndex(
  graph: {
    edges: GraphEdge[];
    dangling: GraphDangling[];
    cycles: string[][];
  },
  /**
   * Config paths of Dockerfile nodes whose file is not on disk — story 6.3.
   *
   * The node renders as missing rather than being omitted. A build that names a
   * file which is not there is exactly the thing the reader needs to see; a
   * graph that quietly drops it is the graph lying by omission, which is the
   * same failure the dangling markers above exist to prevent.
   */
  missing: string[] = [],
): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  const push = (id: string, line: string) => {
    (out[id] ??= []).push(line);
  };

  for (const e of graph.edges) {
    if (e.kind !== 'bind' || !e.mount) {
      continue;
    }
    const host = e.mount.host_path || e.mount.source || '(anonymous)';
    const mode = e.mount.read_only ? ' ro' : '';
    push(e.from, `bind ${host} → ${e.mount.target}${mode}`);
  }
  for (const d of graph.dangling) {
    push(d.from, `unresolved ${d.kind} ${d.ref} — ${d.reason}`);
  }
  for (const cycle of graph.cycles) {
    const names = cycle.map(lastSegment);
    for (const id of cycle) {
      push(id, `dependency cycle: ${names.join(' → ')}`);
    }
  }
  for (const id of missing) {
    push(id, 'missing — this file is not on disk');
  }
  return out;
}

/** The readable tail of a config path: `services.web` → `web`. */
export function lastSegment(id: string): string {
  const parts = id.split('.');
  return parts[parts.length - 1] || id;
}

/* -------------------------------------------------------------------------
 * The layered layout.
 */

export interface GraphLayout {
  positions: Record<string, Point>;
  sizes: Record<string, Size>;
  /** Marker lines per node, computed once and reused by the renderer. */
  markers: Record<string, string[]>;
  /** Nodes grouped into visual rows, top to bottom, each ordered left to right. */
  rows: string[][];
  /** Every node id in reading order — the listbox's order. */
  order: string[];
  /** Dockerfile node id → the service it hangs from. */
  parentOf: Record<string, string>;
}

/**
 * Which horizontal band a node belongs to.
 *
 * Published ports sit above everything: they are where traffic enters, and a
 * reader tracing a request starts there. Services occupy one band per
 * dependency layer, inverted so that a service is drawn ABOVE the things it
 * depends on — `gateway` at the top, `db` at the bottom, which is the direction
 * the arrows already point and the direction the eye reads. Networks, volumes,
 * configs and secrets settle at the bottom: they are what the stack rests on,
 * they are attached to almost everything, and interleaving them with the
 * service layers is what turns a graph into wool.
 */
export function bandOf(node: GraphNode, maxLayer: number): number {
  switch (node.kind) {
    case 'port':
      return 0;
    case 'service':
    case 'dockerfile':
      return 1 + Math.max(0, maxLayer - node.layer);
    default:
      return maxLayer + 2;
  }
}

/**
 * Places every node, keeping any position the reader chose.
 *
 * Saved positions are matched by config path, so a re-resolve that renames a
 * service moves that node and leaves every other one exactly where it was put.
 * A dragged node stays exactly where the reader dropped it — that is the whole
 * contract of a saved position, and re-flowing it into a band would silently
 * discard a decision the reader made deliberately.
 */
export function layoutGraph(
  graph: StackGraph,
  saved: Record<string, Point>,
  missing: string[] = [],
): GraphLayout {
  const markers = markerIndex(graph, missing);
  const sizes: Record<string, Size> = {};
  for (const node of graph.nodes) {
    sizes[node.id] = nodeSize(node, markers[node.id]?.length ?? 0);
  }

  // Dockerfile nodes do not take a band of their own: they hang off the service
  // that builds them, which is the whole point of the node kind.
  const parentOf: Record<string, string> = {};
  const satellites = new Map<string, string[]>();
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  for (const e of graph.edges) {
    if (e.kind !== 'build' || !byId.has(e.to) || !byId.has(e.from)) {
      continue;
    }
    parentOf[e.to] = e.from;
    const list = satellites.get(e.from) ?? [];
    list.push(e.to);
    satellites.set(e.from, list);
  }

  const bandCount = Math.max(1, graph.max_layer + 3);
  const bands: string[][] = Array.from({ length: bandCount }, () => []);
  for (const node of graph.nodes) {
    if (parentOf[node.id] !== undefined) {
      continue;
    }
    const band = Math.min(bandCount - 1, Math.max(0, bandOf(node, graph.max_layer)));
    bands[band].push(node.id);
  }

  orderBands(bands, graph.edges);

  const positions: Record<string, Point> = {};
  let y = 0;
  for (const band of bands) {
    if (band.length === 0) {
      continue;
    }
    for (const run of wrapBand(band)) {
      let rowHeight = 0;
      let total = 0;
      for (const id of run) {
        total += sizes[id].width;
        rowHeight = Math.max(rowHeight, sizes[id].height + satelliteExtent(id, satellites, sizes));
      }
      total += GAP_X * (run.length - 1);
      let x = -total / 2;
      for (const id of run) {
        positions[id] = { x, y };
        x += sizes[id].width + GAP_X;
      }
      y += rowHeight + GAP_Y;
    }
  }

  // The reader's own placements win over the band, and a satellite follows the
  // service it is attached to — including one the reader has dragged.
  for (const node of graph.nodes) {
    if (parentOf[node.id] !== undefined) {
      continue;
    }
    const kept = saved[node.id];
    if (kept && Number.isFinite(kept.x) && Number.isFinite(kept.y)) {
      positions[node.id] = { x: kept.x, y: kept.y };
    }
  }
  for (const [parent, children] of satellites) {
    const base = positions[parent];
    if (!base) {
      continue;
    }
    let sy = base.y + sizes[parent].height + SATELLITE_GAP;
    for (const child of children) {
      const kept = saved[child];
      if (kept && Number.isFinite(kept.x) && Number.isFinite(kept.y)) {
        positions[child] = { x: kept.x, y: kept.y };
      } else {
        positions[child] = { x: base.x + SATELLITE_INDENT, y: sy };
      }
      sy += sizes[child].height + 6;
    }
  }

  const ids = graph.nodes.map((n) => n.id).filter((id) => positions[id] !== undefined);
  const rows = navigationRows(ids, positions, sizes);
  return { positions, sizes, markers, rows, order: rows.flat(), parentOf };
}

/**
 * Splits an over-wide band into sub-rows.
 *
 * Measured, not guessed: examples/large declares 500 services and no
 * `depends_on` at all, so every one of them lands in a single layer. Laid out
 * as one row that band is 112,000px wide and 94px tall — auto-fit clamps at the
 * minimum scale and the reader gets a hairline in the middle of an empty
 * canvas, which is worse than the grid this replaced. A layered layout is only
 * an improvement where there are layers.
 *
 * The wrap width is the same shape as the old grid's: slightly wider than tall,
 * because a VS Code panel is wider than it is high both beside the editor and
 * in the bottom panel. Small bands are never split — a four-service layer
 * broken over two rows would read as two layers, which is a lie about the
 * dependency order.
 */
export const BAND_WRAP_MIN = 9;

export function wrapBand(band: string[]): string[][] {
  if (band.length < BAND_WRAP_MIN) {
    return [band];
  }
  const perRow = Math.ceil(Math.sqrt(band.length * 1.6));
  const rows: string[][] = [];
  for (let i = 0; i < band.length; i += perRow) {
    rows.push(band.slice(i, i + perRow));
  }
  return rows;
}

function satelliteExtent(
  id: string,
  satellites: Map<string, string[]>,
  sizes: Record<string, Size>,
): number {
  const children = satellites.get(id);
  if (!children || children.length === 0) {
    return 0;
  }
  let extent = SATELLITE_GAP;
  for (const child of children) {
    extent += sizes[child].height + 6;
  }
  return extent;
}

/** How many down-then-up barycentre sweeps are run before the best is kept. */
export const ORDER_SWEEPS = 12;

/**
 * How far apart, in band-relative units, every edge's two ends are.
 *
 * The number an ordering is trying to make small, and the reason the sweeps
 * below are not run blind. A barycentre sweep is not monotone — it can and does
 * make an arrangement worse — so a sweep that does not improve this is thrown
 * away rather than kept and swept again.
 *
 * Band-relative rather than in pixels because pixels are not decided yet when
 * the ordering runs, and because it has to compare a band of four with a band
 * of five hundred. It counts edges that SKIP a band too, which an
 * adjacent-layer crossing count cannot: on `examples/webstack` the worst edge
 * in the picture ran from the top band to the fourth, and no pairwise
 * layer-by-layer measure sees it at all.
 */
export function orderSpan(bands: string[][], edges: GraphEdge[]): number {
  const at = new Map<string, number>();
  for (const band of bands) {
    const span = Math.max(1, band.length - 1);
    band.forEach((id, i) => at.set(id, band.length === 1 ? 0.5 : i / span));
  }
  let total = 0;
  for (const e of edges) {
    if (e.kind === 'build' || e.from === e.to) {
      continue;
    }
    const u = at.get(e.from);
    const v = at.get(e.to);
    if (u !== undefined && v !== undefined) {
      total += Math.abs(u - v);
    }
  }
  return total;
}

/**
 * Orders each band to put connected nodes near each other.
 *
 * Barycentre sweeps, the standard method for layered drawing: a node sits at
 * the average position of its neighbours in the reference band. This used to be
 * ONE downward sweep, and the measured consequence was specific rather than
 * theoretical — the bottom band holds the networks, volumes, configs and
 * secrets that almost every service attaches to, it was ordered against the
 * services, and the services were then never re-ordered against IT. The
 * resource attachments were accommodated by nobody, which is the fan converging
 * on one network node in the 2026-08-13 captures.
 *
 * So the sweeps alternate down and up, and — because a barycentre sweep is not
 * monotone — every intermediate arrangement is scored by `orderSpan` and the
 * best one is what survives. Sweeping and hoping is how an ordering pass gets
 * credit for a result it did not produce.
 *
 * Nodes with no neighbour in the reference band keep their relative place
 * rather than being swept to one end, because their incoming order is the
 * core's deterministic one and drifting is worse than arbitrary. Ties are
 * broken by the original index and the best score must be strictly beaten to
 * replace the one held, so the result is stable: the same graph lays out
 * identically twice, which is what makes a canvas diffable and spatial memory
 * possible at all.
 */
export function orderBands(bands: string[][], edges: GraphEdge[]): void {
  const neighbours = new Map<string, string[]>();
  for (const e of edges) {
    if (e.kind === 'build' || e.from === e.to) {
      continue;
    }
    (neighbours.get(e.from) ?? neighbours.set(e.from, []).get(e.from)!).push(e.to);
    (neighbours.get(e.to) ?? neighbours.set(e.to, []).get(e.to)!).push(e.from);
  }

  const sortAgainst = (band: string[], reference: string[]): void => {
    if (band.length < 2 || reference.length === 0) {
      return;
    }
    const rank = new Map<string, number>();
    reference.forEach((id, i) => rank.set(id, i));
    const span = Math.max(1, reference.length - 1);
    const ownSpan = Math.max(1, band.length - 1);
    const keyed = band.map((id, index) => {
      let sum = 0;
      let count = 0;
      for (const other of neighbours.get(id) ?? []) {
        const r = rank.get(other);
        if (r !== undefined) {
          sum += r;
          count++;
        }
      }
      // No neighbour above: hold the node's own relative place, expressed in
      // the same 0..1 units so the two keys are comparable at all.
      const key = count === 0 ? index / ownSpan : sum / count / span;
      return { id, key, index };
    });
    keyed.sort((a, b) => a.key - b.key || a.index - b.index);
    band.length = 0;
    band.push(...keyed.map((k) => k.id));
  };

  if (bands.length < 2) {
    return;
  }

  let best = bands.map((band) => [...band]);
  let bestScore = orderSpan(bands, edges);
  const keepIfBetter = () => {
    const score = orderSpan(bands, edges);
    if (score < bestScore - 1e-9) {
      bestScore = score;
      best = bands.map((band) => [...band]);
    }
  };

  for (let sweep = 0; sweep < ORDER_SWEEPS; sweep++) {
    // Downward: each band against the one above it.
    for (let i = 1; i < bands.length; i++) {
      sortAgainst(bands[i], bands[i - 1]);
    }
    keepIfBetter();
    // Upward: each band against the one below it — including the port band,
    // which has nothing above it, and including the services against the
    // resources they all attach to.
    for (let i = bands.length - 2; i >= 0; i--) {
      sortAgainst(bands[i], bands[i + 1]);
    }
    keepIfBetter();
  }

  for (let i = 0; i < bands.length; i++) {
    bands[i].length = 0;
    bands[i].push(...best[i]);
  }
}

/**
 * Groups placed nodes into visual rows, top to bottom, left to right.
 *
 * Derived from the final positions rather than from the bands, because a
 * dragged node is wherever the reader put it and arrow-key navigation has to
 * follow what is on screen, not what the layout intended. A new row starts when
 * a node's top edge clears the current row's half-height — the same rule the
 * eye uses.
 */
export function navigationRows(
  ids: string[],
  positions: Record<string, Point>,
  sizes: Record<string, Size>,
): string[][] {
  const placed = ids.filter((id) => positions[id] !== undefined);
  const byY = [...placed].sort(
    (a, b) =>
      positions[a].y - positions[b].y ||
      positions[a].x - positions[b].x ||
      (a < b ? -1 : a > b ? 1 : 0),
  );
  const rows: string[][] = [];
  let current: string[] = [];
  let top = 0;
  let tolerance = 0;
  const flush = () => {
    if (current.length > 0) {
      rows.push(sortByX(current, positions));
      current = [];
    }
  };
  for (const id of byY) {
    const y = positions[id].y;
    const half = (sizes[id]?.height ?? NODE_HEIGHT) / 2;
    if (current.length === 0) {
      current = [id];
      top = y;
      tolerance = half;
      continue;
    }
    if (y - top <= tolerance) {
      current.push(id);
      tolerance = Math.max(tolerance, half);
    } else {
      flush();
      current = [id];
      top = y;
      tolerance = half;
    }
  }
  flush();
  return rows;
}

function sortByX(ids: string[], positions: Record<string, Point>): string[] {
  return [...ids].sort(
    (a, b) => positions[a].x - positions[b].x || (a < b ? -1 : a > b ? 1 : 0),
  );
}

/**
 * The next node for an arrow key. Pure, so the keyboard contract is testable.
 *
 * Left and right walk the whole graph in reading order rather than stopping at
 * the end of a row: every node has to be reachable with one key held down, or
 * the listbox has holes in it that only a mouse can fill. Up and down move to
 * the node nearest in x on the adjacent row, which is what the eye expects.
 */
export function navigate(
  rows: string[][],
  positions: Record<string, Point>,
  current: string | null,
  key: string,
): string | null {
  const order = rows.flat();
  if (order.length === 0) {
    return null;
  }
  const index = current === null ? -1 : order.indexOf(current);
  switch (key) {
    case 'Home':
      return order[0];
    case 'End':
      return order[order.length - 1];
    case 'ArrowRight':
      return index < 0 ? order[0] : order[Math.min(order.length - 1, index + 1)];
    case 'ArrowLeft':
      return index < 0 ? order[0] : order[Math.max(0, index - 1)];
    case 'ArrowDown':
    case 'ArrowUp': {
      if (index < 0) {
        return order[0];
      }
      const row = rows.findIndex((r) => r.includes(current!));
      const next = key === 'ArrowDown' ? row + 1 : row - 1;
      if (row < 0 || next < 0 || next >= rows.length) {
        return current;
      }
      return nearestInRow(rows[next], positions, positions[current!]?.x ?? 0);
    }
    default:
      return null;
  }
}

function nearestInRow(row: string[], positions: Record<string, Point>, x: number): string | null {
  let best: string | null = null;
  let bestDistance = Infinity;
  for (const id of row) {
    const distance = Math.abs((positions[id]?.x ?? 0) - x);
    if (distance < bestDistance) {
      bestDistance = distance;
      best = id;
    }
  }
  return best;
}

/* -------------------------------------------------------------------------
 * Edge geometry.
 */

export function boxOf(p: Point, size: Size): Box {
  return { x: p.x, y: p.y, width: size.width, height: size.height };
}

export interface Anchors {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  /** Which way the curve leaves the first box. */
  axis: 'vertical' | 'horizontal';
}

/**
 * Where an edge meets each box.
 *
 * Vertical when the boxes do not overlap vertically, which is the layered case
 * and most of the graph: the line leaves the bottom of the upper box and
 * arrives at the top of the lower one, so the direction of the relation is
 * legible without reading the arrowhead. Side by side — two members of a cycle
 * sharing a layer, or a node the reader dragged — it leaves the near side
 * instead. An edge that always left the bottom would run backwards through its
 * own box, which reads as a line to nowhere.
 */
export function edgeAnchors(a: Box, b: Box): Anchors {
  const aCx = a.x + a.width / 2;
  const bCx = b.x + b.width / 2;
  const aCy = a.y + a.height / 2;
  const bCy = b.y + b.height / 2;
  if (b.y >= a.y + a.height) {
    return { x1: aCx, y1: a.y + a.height, x2: bCx, y2: b.y, axis: 'vertical' };
  }
  if (a.y >= b.y + b.height) {
    return { x1: aCx, y1: a.y, x2: bCx, y2: b.y + b.height, axis: 'vertical' };
  }
  if (bCx >= aCx) {
    return { x1: a.x + a.width, y1: aCy, x2: b.x, y2: bCy, axis: 'horizontal' };
  }
  return { x1: a.x, y1: aCy, x2: b.x + b.width, y2: bCy, axis: 'horizontal' };
}

export interface EdgeGeometry {
  /** SVG path data. */
  d: string;
  /** The point on the curve where a label goes. */
  mid: Point;
}

export const CURVE_MIN = 14;
export const CURVE_MAX = 64;

/**
 * A cubic between two boxes, and the point halfway along it.
 *
 * The midpoint is computed from the curve rather than from the two endpoints:
 * a straight-line midpoint sits off the curve wherever it bends, and a
 * `service_healthy` label floating beside the wrong edge is worse than no
 * label — which condition an edge carries decides whether the stack starts.
 */
export function edgeGeometry(a: Box, b: Box): EdgeGeometry {
  const { x1, y1, x2, y2, axis } = edgeAnchors(a, b);
  const distance = axis === 'vertical' ? Math.abs(y2 - y1) : Math.abs(x2 - x1);
  const bend = Math.min(CURVE_MAX, Math.max(CURVE_MIN, distance / 2));
  const c1: Point =
    axis === 'vertical'
      ? { x: x1, y: y1 + Math.sign(y2 - y1 || 1) * bend }
      : { x: x1 + Math.sign(x2 - x1 || 1) * bend, y: y1 };
  const c2: Point =
    axis === 'vertical'
      ? { x: x2, y: y2 - Math.sign(y2 - y1 || 1) * bend }
      : { x: x2 - Math.sign(x2 - x1 || 1) * bend, y: y2 };
  const mid: Point = {
    x: (x1 + 3 * c1.x + 3 * c2.x + x2) / 8,
    y: (y1 + 3 * c1.y + 3 * c2.y + y2) / 8,
  };
  const d =
    `M${round(x1)} ${round(y1)}C${round(c1.x)} ${round(c1.y)} ` +
    `${round(c2.x)} ${round(c2.y)} ${round(x2)} ${round(y2)}`;
  return { d, mid: { x: round(mid.x), y: round(mid.y) } };
}

function round(n: number): number {
  return Math.round(n * 10) / 10;
}

/* -------------------------------------------------------------------------
 * Routing.
 *
 * A cubic between two boxes is the right line when nothing is in the way. On a
 * layered graph a great many edges skip a band — a service on the top layer
 * attaches to the network everything attaches to — and a cubic drawn straight
 * at it runs underneath every box in between. Measured on `examples/webstack`:
 * six of sixteen edges passed over a node that was neither of their endpoints,
 * which is what makes a seven-service stack read as a tangle. It is not a
 * crossing problem; that stack has exactly one crossing.
 *
 * So an edge that would pass over a box is re-drawn as a route: down into the
 * gutter below the band it leaves, sideways to a vertical corridor that is
 * clear of every box it has to pass, down that corridor, and into the target
 * from the gutter above it. Corners are rounded so it still reads as a line
 * rather than as plumbing.
 *
 * Three rules keep this from making things worse:
 *   - it is only ever attempted when the direct cubic is measured to be
 *     obstructed, so an unobstructed edge is byte-identical to before;
 *   - the corridor is searched only INSIDE the horizontal extent of the graph,
 *     so a route can never widen the canvas and make auto-fit smaller;
 *   - if no clear corridor exists, or there is no gutter to turn in, the direct
 *     cubic is kept. A worse-looking honest line beats an invented one.
 */

/** How close a route may pass to a box it is going around, in px. */
export const ROUTE_CLEARANCE = 9;
/** How far into the gutter a route turns before running sideways. */
export const ROUTE_TURN = 16;
/** The radius of a route's corners. */
export const ROUTE_RADIUS = 8;
/** Samples used to decide whether a curve passes over a box. */
const OCCLUSION_SAMPLES = 16;

function cubicAt(p0: Point, p1: Point, p2: Point, p3: Point, t: number): Point {
  const u = 1 - t;
  return {
    x: u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x,
    y: u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y,
  };
}

function inside(box: Box, x: number, y: number): boolean {
  return x > box.x && x < box.x + box.width && y > box.y && y < box.y + box.height;
}

/**
 * Whether the direct cubic between two boxes passes over any of `obstacles`.
 *
 * Sampled on the curve rather than on the straight line between the anchors:
 * the curve is what gets drawn, and on a long edge it bows far enough from the
 * chord to change the answer.
 */
export function curveObstructed(a: Box, b: Box, obstacles: Box[]): boolean {
  if (obstacles.length === 0) {
    return false;
  }
  const { x1, y1, x2, y2, axis } = edgeAnchors(a, b);
  const [c1, c2] = controlPoints(x1, y1, x2, y2, axis);
  const lo = { x: Math.min(x1, x2, c1.x, c2.x), y: Math.min(y1, y2, c1.y, c2.y) };
  const hi = { x: Math.max(x1, x2, c1.x, c2.x), y: Math.max(y1, y2, c1.y, c2.y) };
  const near = obstacles.filter(
    (o) =>
      o !== a &&
      o !== b &&
      o.x < hi.x &&
      o.x + o.width > lo.x &&
      o.y < hi.y &&
      o.y + o.height > lo.y,
  );
  if (near.length === 0) {
    return false;
  }
  for (let i = 1; i < OCCLUSION_SAMPLES; i++) {
    const p = cubicAt({ x: x1, y: y1 }, c1, c2, { x: x2, y: y2 }, i / OCCLUSION_SAMPLES);
    for (const o of near) {
      if (inside(o, p.x, p.y)) {
        return true;
      }
    }
  }
  return false;
}

function controlPoints(
  x1: number,
  y1: number,
  x2: number,
  y2: number,
  axis: 'vertical' | 'horizontal',
): [Point, Point] {
  const distance = axis === 'vertical' ? Math.abs(y2 - y1) : Math.abs(x2 - x1);
  const bend = Math.min(CURVE_MAX, Math.max(CURVE_MIN, distance / 2));
  return axis === 'vertical'
    ? [
        { x: x1, y: y1 + Math.sign(y2 - y1 || 1) * bend },
        { x: x2, y: y2 - Math.sign(y2 - y1 || 1) * bend },
      ]
    : [
        { x: x1 + Math.sign(x2 - x1 || 1) * bend, y: y1 },
        { x: x2 - Math.sign(x2 - x1 || 1) * bend, y: y2 },
      ];
}

/**
 * The x nearest `desired` that no box in `blockers` covers, or null.
 *
 * Bounded by the graph's own horizontal extent: a corridor outside it would be
 * drawn where auto-fit is not looking, so an edge that cannot be routed inside
 * the canvas keeps its direct curve instead.
 */
export function clearCorridor(
  blockers: Box[],
  desired: number,
  bounds: { min: number; max: number },
): number | null {
  return nearestFree(mergedSpans(blockers), desired, bounds);
}

/** The x intervals a route may not use, padded by the clearance and merged. */
function mergedSpans(blockers: Box[]): [number, number][] {
  const spans = blockers
    .map((o) => [o.x - ROUTE_CLEARANCE, o.x + o.width + ROUTE_CLEARANCE] as [number, number])
    .sort((p, q) => p[0] - q[0]);
  const merged: [number, number][] = [];
  for (const span of spans) {
    const last = merged[merged.length - 1];
    if (last && span[0] <= last[1]) {
      last[1] = Math.max(last[1], span[1]);
    } else {
      merged.push([span[0], span[1]]);
    }
  }
  return merged;
}

function nearestFree(
  merged: [number, number][],
  desired: number,
  bounds: { min: number; max: number },
): number | null {
  if (bounds.min > bounds.max) {
    return null;
  }
  let best: number | null = null;
  const consider = (lo: number, hi: number) => {
    const l = Math.max(lo, bounds.min);
    const h = Math.min(hi, bounds.max);
    if (l > h) {
      return;
    }
    const x = Math.min(h, Math.max(l, desired));
    if (best === null || Math.abs(x - desired) < Math.abs(best - desired)) {
      best = x;
    }
  };
  let cursor = -Infinity;
  for (const [lo, hi] of merged) {
    consider(cursor, lo);
    cursor = Math.max(cursor, hi);
  }
  consider(cursor, Infinity);
  return best;
}

/** A polyline with rounded corners, as SVG path data. */
export function roundedPath(points: Point[], radius: number): string {
  const pts = points.filter(
    (p, i) => i === 0 || Math.abs(p.x - points[i - 1].x) > 0.01 || Math.abs(p.y - points[i - 1].y) > 0.01,
  );
  if (pts.length < 2) {
    return '';
  }
  let d = `M${round(pts[0].x)} ${round(pts[0].y)}`;
  for (let i = 1; i < pts.length - 1; i++) {
    const prev = pts[i - 1];
    const here = pts[i];
    const next = pts[i + 1];
    const inLen = Math.hypot(here.x - prev.x, here.y - prev.y);
    const outLen = Math.hypot(next.x - here.x, next.y - here.y);
    const r = Math.min(radius, inLen / 2, outLen / 2);
    const a = {
      x: here.x + ((prev.x - here.x) / inLen) * r,
      y: here.y + ((prev.y - here.y) / inLen) * r,
    };
    const b = {
      x: here.x + ((next.x - here.x) / outLen) * r,
      y: here.y + ((next.y - here.y) / outLen) * r,
    };
    d += `L${round(a.x)} ${round(a.y)}Q${round(here.x)} ${round(here.y)} ${round(b.x)} ${round(b.y)}`;
  }
  const end = pts[pts.length - 1];
  d += `L${round(end.x)} ${round(end.y)}`;
  return d;
}

/**
 * An edge drawn around the boxes in its way, or the plain cubic when it is not
 * in anything's way — or when nothing legible can be drawn instead.
 */
export function routeGeometry(
  a: Box,
  b: Box,
  obstacles: Box[],
  extent?: { min: number; max: number },
  spans?: Map<string, [number, number][]>,
): EdgeGeometry {
  const direct = edgeGeometry(a, b);
  const { x1, y1, x2, y2, axis } = edgeAnchors(a, b);
  const lo = Math.min(y1, y2);
  const hi = Math.max(y1, y2);

  // One scan of the obstacle list, not three. Everything the router needs to
  // know is about boxes whose vertical extent meets the edge's, and the curve's
  // own bounding box is inside that strip whichever way the edge leaves — so
  // this same list answers "is the direct curve blocked", "where are the
  // gutters" and "where is there a corridor".
  const between: Box[] = [];
  for (const o of obstacles) {
    if (o !== a && o !== b && o.y < hi && o.y + o.height > lo) {
      between.push(o);
    }
  }
  if (!curveObstructed(a, b, between)) {
    return direct;
  }
  // Only the layered case is routed. A sideways edge is short by construction —
  // two members of a cycle, or a node the reader dragged — and bending it round
  // a box would say something about the graph that is not true.
  if (axis !== 'vertical') {
    return direct;
  }
  const dir = Math.sign(y2 - y1 || 1);

  // The gutters: the clear strip just outside the band being left, and the one
  // just outside the band being entered. Folded rather than spread — a spread
  // of one array element per obstacle is a call with 700 arguments on the
  // 771-node example, and an unbounded one on anything larger.
  // The nearest obstacle IN THE DIRECTION OF TRAVEL — not the topmost box in
  // the strip, whichever side of the source it is on.
  //
  // `between` is filtered vertically only, so it always contains the source's
  // own band-mates: they overlap the edge's vertical extent by definition. The
  // old fold took the minimum `y` over that whole list, which for a downward
  // edge is a box BESIDE the source, sitting one band up. The near gap then
  // measured a distance the edge never travels, and the turn was placed at the
  // full `ROUTE_TURN` inside a gutter that was 14px deep — straight through the
  // Dockerfile node hanging under the service the edge leaves. That is exactly
  // the `services.docs -> networks.shipyard` occlusion, and it only surfaced
  // when the service card stopped being 94px tall, because until then the
  // arithmetic happened to land clear.
  let nearEdge = dir > 0 ? Infinity : -Infinity;
  let farEdge = dir > 0 ? -Infinity : Infinity;
  for (const o of between) {
    const oTop = o.y;
    const oBottom = o.y + o.height;
    if (dir > 0) {
      if (oTop > y1) {
        nearEdge = Math.min(nearEdge, oTop);
      }
      if (oBottom < y2) {
        farEdge = Math.max(farEdge, oBottom);
      }
    } else {
      if (oBottom < y1) {
        nearEdge = Math.max(nearEdge, oBottom);
      }
      if (oTop > y2) {
        farEdge = Math.min(farEdge, oTop);
      }
    }
  }
  // Nothing ahead on that side is not a reason to give up: it means the whole
  // run to the turn is clear, so the turn takes its full offset.
  const nearGap = Number.isFinite(nearEdge) ? Math.abs(nearEdge - y1) : Infinity;
  const farGap = Number.isFinite(farEdge) ? Math.abs(y2 - farEdge) : Infinity;
  if (nearGap < 10 || farGap < 10) {
    return direct;
  }
  const turn1 = y1 + dir * Math.min(ROUTE_TURN, nearGap / 2);
  const turn2 = y2 - dir * Math.min(ROUTE_TURN, farGap / 2);
  if ((turn2 - turn1) * dir <= 0) {
    return direct;
  }

  const corridorLo = Math.min(turn1, turn2);
  const corridorHi = Math.max(turn1, turn2);
  // The merged forbidden intervals depend only on the corridor's vertical
  // extent, and on a layered graph hundreds of edges share one. Sorting and
  // merging them once per distinct extent rather than once per edge is what
  // keeps the 771-node example inside a frame.
  const key = `${corridorLo}:${corridorHi}`;
  let merged = spans?.get(key);
  if (merged === undefined) {
    const blockers: Box[] = [];
    for (const o of between) {
      if (o.y < corridorHi && o.y + o.height > corridorLo) {
        blockers.push(o);
      }
    }
    merged = mergedSpans(blockers);
    spans?.set(key, merged);
  }
  let bounds = extent;
  if (bounds === undefined) {
    let min = Infinity;
    let max = -Infinity;
    for (const o of obstacles) {
      min = Math.min(min, o.x);
      max = Math.max(max, o.x + o.width);
    }
    bounds = { min, max };
  }
  const corridor = nearestFree(merged, (x1 + x2) / 2, bounds);
  if (corridor === null) {
    return direct;
  }

  const d = roundedPath(
    [
      { x: x1, y: y1 },
      { x: x1, y: turn1 },
      { x: corridor, y: turn1 },
      { x: corridor, y: turn2 },
      { x: x2, y: turn2 },
      { x: x2, y: y2 },
    ],
    ROUTE_RADIUS,
  );
  if (d === '') {
    return direct;
  }
  return { d, mid: { x: round(corridor), y: round((turn1 + turn2) / 2) } };
}

/**
 * Every drawable edge, routed around the boxes it would otherwise cross.
 *
 * One pass over the edges, one obstacle list per edge — the endpoints of an
 * edge are never obstacles to it, or every edge would be routed around the two
 * boxes it is trying to touch.
 */
export function routeEdges(
  edges: GraphEdge[],
  boxes: Record<string, Box>,
): { edge: GraphEdge; geometry: EdgeGeometry }[] {
  // Built once. The two endpoints are excluded by identity inside the router —
  // `boxes[id]` hands back the same object every time — rather than by rebuilding
  // the list per edge, which on the 771-node example would be 590,000 copies.
  const all = Object.values(boxes);
  let min = Infinity;
  let max = -Infinity;
  for (const o of all) {
    min = Math.min(min, o.x);
    max = Math.max(max, o.x + o.width);
  }
  const extent = { min, max };
  // Shared across the whole pass. Sound because a corridor never overlaps its
  // own edge's endpoints: it starts past the anchor edge of one box and ends
  // before the other, so no edge's own boxes are ever among the blockers a
  // cache entry was built from.
  const spans = new Map<string, [number, number][]>();
  const out: { edge: GraphEdge; geometry: EdgeGeometry }[] = [];
  for (const e of edges) {
    if (e.from === e.to) {
      continue; // a bind is a marker on its service, never a loop to nowhere
    }
    const a = boxes[e.from];
    const b = boxes[e.to];
    if (!a || !b) {
      continue;
    }
    out.push({ edge: e, geometry: routeGeometry(a, b, all, extent, spans) });
  }
  return out;
}

/**
 * Every edge of one kind as a single path, and the labels that go with it.
 *
 * One `<path>` per kind rather than one per edge, because the 500-service
 * example declares 767 edges and each SVG element is a style resolution, a
 * layout box and a paint. Subpaths in one `d` attribute render identically and
 * cost one element. Edges are not individually selectable in this story, so
 * nothing is lost by it; when they become selectable, this is the function that
 * changes.
 */
export function edgePaths(
  edges: GraphEdge[],
  boxes: Record<string, Box>,
): { kind: string; d: string; count: number }[] {
  return edgeLayers(routeEdges(edges, boxes));
}

/** The same grouping, over edges that have already been routed. */
export function edgeLayers(
  routed: { edge: GraphEdge; geometry: EdgeGeometry }[],
): { kind: string; d: string; count: number }[] {
  const byKind = new Map<string, string[]>();
  const counts = new Map<string, number>();
  for (const { edge: e, geometry } of routed) {
    const list = byKind.get(e.kind) ?? [];
    list.push(geometry.d);
    byKind.set(e.kind, list);
    counts.set(e.kind, (counts.get(e.kind) ?? 0) + 1);
  }
  return [...byKind.entries()].map(([kind, parts]) => ({
    kind,
    d: parts.join(''),
    count: counts.get(kind) ?? 0,
  }));
}

/**
 * The label an edge carries, or '' for one that carries none.
 *
 * Only `depends_on` gets one, and it is the condition verbatim as the file
 * wrote it. Which condition it is decides whether the stack starts —
 * `service_healthy` against a service with no healthcheck never starts at all —
 * so it is not a detail for a tooltip. The file's own word, not a friendlier
 * one: renaming a schema key makes it unsearchable.
 */
export function edgeLabel(edge: GraphEdge): string {
  if (edge.kind !== 'depends_on' || !edge.depends_on) {
    return '';
  }
  const parts = [edge.depends_on.condition];
  if (edge.depends_on.required === 'false') {
    parts.push('not required');
  }
  return parts.join(' · ');
}

/* -------------------------------------------------------------------------
 * Edge labels.
 */

/** The plate a label is drawn on. Mirrored by `.edge-label-plate`. */
export const LABEL_CHAR_WIDTH = 6.2;
export const LABEL_PAD_X = 8;
export const LABEL_HEIGHT = 15;
/** How far the plate's top sits above the point the label is anchored to. */
export const LABEL_TOP = -8;

export interface LabelPlacement {
  edge: GraphEdge;
  text: string;
  /** Where the label group is translated to. */
  at: Point;
  /** The rectangle the plate occupies, in graph space. */
  plate: Box;
  /** True when nowhere legible was found and the label must not be drawn. */
  dropped: boolean;
}

/**
 * Where each edge label goes, given that two of them may want the same pixel.
 *
 * `service_healthy` printed on top of `service_started` is what
 * `02-service-selected.png` shows — the two conditions on the `web`→`api` pair
 * rendered as `service_healthy  e_started`, which is not either of the words
 * and is worse than showing one of them. Which condition an edge carries
 * decides whether the stack starts, so an unreadable label is a wrong answer,
 * not a cosmetic one.
 *
 * A label is nudged along its own edge's midpoint — up first, then down, then
 * sideways — and takes the first offset clear of every label already placed. A
 * candidate that also clears the node boxes is preferred over one that only
 * clears the labels, because a plate on top of a service name hides the name.
 * If nothing is clear the label is DROPPED rather than overlapped: the reader
 * can still select the edge's target and read the condition in the inspector,
 * and two words printed through each other say nothing at all.
 *
 * Deterministic: candidates are tried in a fixed order and the input order is
 * the core's, so the same graph labels identically twice.
 */
export function placeLabels(
  labels: { edge: GraphEdge; text: string; mid: Point }[],
  obstacles: Box[] = [],
): LabelPlacement[] {
  const offsets: Point[] = [
    { x: 0, y: 0 },
    { x: 0, y: -(LABEL_HEIGHT + 3) },
    { x: 0, y: LABEL_HEIGHT + 3 },
    { x: 0, y: -2 * (LABEL_HEIGHT + 3) },
    { x: 0, y: 2 * (LABEL_HEIGHT + 3) },
  ];
  const overlaps = (a: Box, b: Box) =>
    a.x + a.width > b.x && b.x + b.width > a.x && a.y + a.height > b.y && b.y + b.height > a.y;

  const taken: Box[] = [];
  const out: LabelPlacement[] = [];
  for (const label of labels) {
    const width = label.text.length * LABEL_CHAR_WIDTH + LABEL_PAD_X;
    const sideways = width / 2 + 10;
    const candidates = [...offsets, { x: sideways, y: 0 }, { x: -sideways, y: 0 }];
    let chosen: { at: Point; plate: Box } | null = null;
    let fallback: { at: Point; plate: Box } | null = null;
    for (const offset of candidates) {
      const at = { x: label.mid.x + offset.x, y: label.mid.y + offset.y };
      const plate: Box = {
        x: at.x - width / 2,
        y: at.y + LABEL_TOP,
        width,
        height: LABEL_HEIGHT,
      };
      if (taken.some((t) => overlaps(t, plate))) {
        continue;
      }
      if (fallback === null) {
        fallback = { at, plate };
      }
      if (!obstacles.some((o) => overlaps(o, plate))) {
        chosen = { at, plate };
        break;
      }
    }
    const placed = chosen ?? fallback;
    if (placed === null) {
      out.push({
        edge: label.edge,
        text: label.text,
        at: label.mid,
        plate: { x: label.mid.x, y: label.mid.y, width: 0, height: 0 },
        dropped: true,
      });
      continue;
    }
    taken.push(placed.plate);
    out.push({ edge: label.edge, text: label.text, at: placed.at, plate: placed.plate, dropped: false });
  }
  return out;
}

/**
 * The edge kinds DRAWN, in a fixed order, for the legend.
 *
 * Drawn, not declared. The list's accessible name is "Edge kinds in this
 * stack" and every row is a line swatch, so a row is a promise that a stroke
 * of that weight exists somewhere on the canvas. `edgePaths` skips self-edges,
 * and `bind` is always a self-edge (DECISIONS.md 18: a host directory is not a
 * node, so a bind is a marker on the service card rather than a path), so a
 * project with a bind mount used to render a legend row for a kind with no
 * stroke anywhere — falling back to the plain swatch, indistinguishable from
 * `network`.
 *
 * Filtered by the same rule `edgePaths` uses rather than by deleting `bind`
 * from the order below, so the two cannot drift: if a bind ever gains a real
 * far side — DECISIONS.md 18 keeps that open — the row returns on its own
 * instead of being silently missing.
 */
export function legendEntries(edges: GraphEdge[]): EdgeKind[] {
  const order: EdgeKind[] = [
    'depends_on',
    'network',
    'network_mode',
    'link',
    'volume',
    'bind',
    'config',
    'secret',
    'publish',
    'build',
  ];
  const present = new Set(edges.filter((e) => e.from !== e.to).map((e) => e.kind));
  return order.filter((kind) => present.has(kind));
}

/* -------------------------------------------------------------------------
 * The viewport.
 */

/** The extent of the drawn graph, in graph space. */
export function contentBox(
  positions: Record<string, Point>,
  sizes: Record<string, Size> = {},
): Box {
  const ids = Object.keys(positions);
  if (ids.length === 0) {
    return { x: 0, y: 0, width: NODE_WIDTH, height: NODE_HEIGHT };
  }
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const id of ids) {
    const p = positions[id];
    const size = sizes[id] ?? { width: NODE_WIDTH, height: NODE_HEIGHT };
    minX = Math.min(minX, p.x);
    minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x + size.width);
    maxY = Math.max(maxY, p.y + size.height);
  }
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY };
}

export interface Viewport {
  x: number;
  y: number;
  k: number;
}

export const MIN_SCALE = 0.08;
export const MAX_SCALE = 2;

/**
 * The transform that puts the whole graph on screen.
 *
 * Auto-fit on open is not a nicety: four nodes adrift in an empty canvas is
 * the named anti-pattern this product is replacing.
 */
export function fitToView(box: Box, width: number, height: number, pad = 28): Viewport {
  if (width <= 0 || height <= 0) {
    return { x: 0, y: 0, k: 1 };
  }
  // Never magnified past 1: a four-node stack blown up to fill the pane reads
  // as a different product than the same stack with forty services in it.
  const k = clampScale(
    Math.min(
      1,
      (width - pad * 2) / Math.max(box.width, 1),
      (height - pad * 2) / Math.max(box.height, 1),
    ),
  );
  return {
    x: (width - box.width * k) / 2 - box.x * k,
    y: (height - box.height * k) / 2 - box.y * k,
    k,
  };
}

export function clampScale(k: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, k));
}

/**
 * Whether a pane resize should re-fit the graph.
 *
 * Extracted so it can be tested: the original defect was that nothing re-fitted
 * at all, and the code that should have lived in a DOM callback no test could
 * reach. The graph was fitted once against whatever the canvas measured at
 * first paint — in a webview, routinely before layout settles — and a stack
 * then rendered tiny in the corner of an empty canvas, which is the exact
 * failure this product replaces.
 */
export function shouldRefit(state: {
  hasNodes: boolean;
  userAdjusted: boolean;
  width: number;
  height: number;
}): boolean {
  if (!state.hasNodes) {
    return false;
  }
  // A zero-sized pane measures nothing useful; fitting to it produces the
  // no-op viewport and burns the one chance to get this right.
  if (state.width <= 0 || state.height <= 0) {
    return false;
  }
  // Once the reader has panned or zoomed, the view is theirs. Yanking it back
  // to fit because the pane moved is worse than a stale zoom level.
  return !state.userAdjusted;
}

/* -------------------------------------------------------------------------
 * Text.
 */

/**
 * Break text onto at most `maxLines` lines of `columns` characters, eliding
 * only when it genuinely will not fit.
 *
 * Image references are the reason this exists: `ghcr.io/composure/example-0:1.0.3`
 * elided to one line reads `ghcr.io/composure/example-0:1.0.…`, which hides the
 * tag — the single most important part of an image reference, and the one thing
 * a reader is scanning the canvas for.
 *
 * Breaks prefer a path separator or the tag colon, so a wrapped reference
 * splits where a reader would split it, not mid-token.
 */
export function wrapText(text: string, columns: number, maxLines: number): string[] {
  if (columns <= 0 || maxLines <= 0) {
    return [];
  }
  if (text.length <= columns) {
    return [text];
  }

  const lines: string[] = [];
  let rest = text;

  while (rest.length > columns && lines.length < maxLines - 1) {
    const window = rest.slice(0, columns + 1);
    // Break after a separator so the delimiter stays with the line it ends,
    // which is how a registry path reads naturally.
    let cut = Math.max(window.lastIndexOf('/'), window.lastIndexOf(':'));
    cut = cut > 0 ? cut + 1 : columns;

    // ...but a pretty break that leaves more than the remaining lines can hold
    // is not a pretty break: it spends capacity on whitespace and then elides
    // the tail, which on an image reference means losing the tag. When that
    // happens, fill the line instead.
    const linesLeftAfter = maxLines - lines.length - 1;
    if (rest.length - cut > columns * linesLeftAfter) {
      cut = columns;
    }

    lines.push(rest.slice(0, cut));
    rest = rest.slice(cut);
  }

  lines.push(rest.length > columns ? `${rest.slice(0, columns - 1)}…` : rest);
  return lines;
}

/**
 * Wrap an image reference, keeping the end when it cannot all fit.
 *
 * `wrapText` elides from the end, which is right for prose and wrong here: on
 * `registry.internal.example.com/platform/service:2026.08.1` it spends the
 * whole budget on the registry host and drops the tag — the one part a reader
 * is scanning for, and the part that changes when someone upgrades.
 *
 * So when the reference will not fit, the leading registry path goes instead,
 * marked with a leading ellipsis. `…/platform/service:2026.08.1` tells a reader
 * what they need; the full reference is always in the node's tooltip.
 */
export function wrapImageRef(image: string, columns: number, maxLines: number): string[] {
  if (columns <= 0 || maxLines <= 0) {
    return [];
  }
  const budget = columns * maxLines;
  if (image.length <= budget) {
    return wrapText(image, columns, maxLines);
  }
  // Keep the tail, and mark that the head was dropped.
  const tail = image.slice(-(budget - 1));
  return wrapText(`…${tail}`, columns, maxLines);
}

/**
 * One line of at most `columns`, keeping the end.
 *
 * Used for the marker lines, where the text is a host path or a reference name:
 * the tail is the identifying part and the head is a directory prefix shared
 * with everything else on the canvas.
 */
export function truncateTail(text: string, columns: number): string {
  if (columns <= 0) {
    return '';
  }
  if (text.length <= columns) {
    return text;
  }
  return `…${text.slice(-(columns - 1))}`;
}

/** One line of at most `columns`, keeping the beginning. For names. */
export function truncateHead(text: string, columns: number): string {
  if (columns <= 0) {
    return '';
  }
  return text.length <= columns ? text : `${text.slice(0, columns - 1)}…`;
}

/* -------------------------------------------------------------------------
 * Diagnostics on the canvas — story 5.4.
 */

/**
 * The badge a node carries when findings anchor inside it.
 *
 * One number and one severity: the count of everything found, coloured by the
 * WORST of it. Two badges on a 188px box would be unreadable, and a badge that
 * showed only errors would leave a service with four warnings looking clean.
 *
 * `label` is the number; `word` is the same information in text, for the
 * node's accessible name. N6: severity is never carried by colour alone.
 */
export interface SeverityBadge {
  count: number;
  level: 'error' | 'warning' | 'hint';
  label: string;
  word: string;
}

export function severityBadge(count: {
  error: number;
  warning: number;
  hint: number;
} | undefined): SeverityBadge | null {
  if (!count) {
    return null;
  }
  const total = count.error + count.warning + count.hint;
  if (total <= 0) {
    return null;
  }
  const level = count.error > 0 ? 'error' : count.warning > 0 ? 'warning' : 'hint';
  const parts: string[] = [];
  if (count.error > 0) {
    parts.push(`${count.error} error${count.error === 1 ? '' : 's'}`);
  }
  if (count.warning > 0) {
    parts.push(`${count.warning} warning${count.warning === 1 ? '' : 's'}`);
  }
  if (count.hint > 0) {
    parts.push(`${count.hint} hint${count.hint === 1 ? '' : 's'}`);
  }
  return {
    count: total,
    level,
    // Beyond nine the digit stops fitting the circle, and the exact number is
    // in the accessible name and the tooltip either way.
    label: total > 9 ? '9+' : String(total),
    word: parts.join(', '),
  };
}
