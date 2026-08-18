// What the graph pane is actually like to read, in numbers.
//
// A sibling of crossings.ts rather than a replacement: that file measures the
// two-box cubic and is the instrument the 2026-08-13 gap report's table was
// produced with. This one measures what `routeEdges` DRAWS — routed polylines
// included — and adds the two figures that turned out to be the actual
// complaint: how many edges pass over a node box that is not an endpoint, and
// how many edge labels overlap another one. It also times the layout, because
// routing is not free and a graph that reads well but stutters is not a fix.
//
// Sampled on the drawn path, so a change to routing cannot improve the number
// without improving the picture.
import {
  layoutGraph,
  boxOf,
  routeEdges,
  edgeGeometry,
  edgeLabel,
  placeLabels,
  LABEL_CHAR_WIDTH,
  LABEL_PAD_X,
  LABEL_HEIGHT,
  LABEL_TOP,
} from '../webview/layout';
import { readGraph } from '../host/topology';
import * as fs from 'node:fs';
import * as path from 'node:path';

const f = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'fixtures/core.json'), 'utf8'));
const SAMPLES = 24;
const verbose = process.argv.includes('-v');
// `--direct` measures the geometry as it was BEFORE routing and label
// avoidance existed — one cubic per edge, every label pinned to its midpoint —
// so a before/after pair is produced by one instrument rather than two.
const direct = process.argv.includes('--direct');

function geometryOf(edges: any[], boxes: Record<string, any>) {
  if (!direct) {
    return routeEdges(edges, boxes);
  }
  const out: any[] = [];
  for (const e of edges) {
    if (e.from === e.to) continue;
    const a = boxes[e.from], b = boxes[e.to];
    if (!a || !b) continue;
    out.push({ edge: e, geometry: edgeGeometry(a, b) });
  }
  return out;
}

function labelsOf(routed: any[], boxes: any[]) {
  const wanted = routed
    .map((r: any) => ({ edge: r.edge, text: edgeLabel(r.edge), mid: r.geometry.mid }))
    .filter((r: any) => r.text !== '');
  if (!direct) {
    return placeLabels(wanted, boxes);
  }
  return wanted.map((w: any) => {
    const width = w.text.length * LABEL_CHAR_WIDTH + LABEL_PAD_X;
    return {
      text: w.text,
      at: w.mid,
      plate: { x: w.mid.x - width / 2, y: w.mid.y + LABEL_TOP, width, height: LABEL_HEIGHT },
      dropped: false,
    };
  });
}

type P = { x: number; y: number };

function lerp(a: P, b: P, t: number): P {
  return { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
}
function quad(p0: P, p1: P, p2: P, t: number): P {
  const u = 1 - t;
  return {
    x: u * u * p0.x + 2 * u * t * p1.x + t * t * p2.x,
    y: u * u * p0.y + 2 * u * t * p1.y + t * t * p2.y,
  };
}
function cubic(p0: P, p1: P, p2: P, p3: P, t: number): P {
  const u = 1 - t;
  return {
    x: u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x,
    y: u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y,
  };
}

/** Samples an SVG path built from M / L / C / Q into a polyline. */
function samplePath(d: string, per: number): P[] {
  const tokens = d.match(/[MLCQ]|-?\d+(?:\.\d+)?/g) ?? [];
  const pts: P[] = [];
  let i = 0;
  let cur: P = { x: 0, y: 0 };
  const num = () => Number(tokens[i++]);
  while (i < tokens.length) {
    const cmd = tokens[i++];
    if (cmd === 'M') {
      cur = { x: num(), y: num() };
      pts.push(cur);
    } else if (cmd === 'L') {
      const to = { x: num(), y: num() };
      for (let s = 1; s <= 4; s++) pts.push(lerp(cur, to, s / 4));
      cur = to;
    } else if (cmd === 'Q') {
      const c = { x: num(), y: num() };
      const to = { x: num(), y: num() };
      for (let s = 1; s <= 4; s++) pts.push(quad(cur, c, to, s / 4));
      cur = to;
    } else if (cmd === 'C') {
      const c1 = { x: num(), y: num() };
      const c2 = { x: num(), y: num() };
      const to = { x: num(), y: num() };
      for (let s = 1; s <= per; s++) pts.push(cubic(cur, c1, c2, to, s / per));
      cur = to;
    } else {
      i++; // unreachable; keeps a malformed path from spinning
    }
  }
  return pts;
}

function cr(o: P, a: P, b: P) {
  return (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);
}
function hits(A: P, B: P, C: P, D: P) {
  const d1 = cr(A, B, C), d2 = cr(A, B, D), d3 = cr(C, D, A), d4 = cr(C, D, B);
  return (d1 > 0) !== (d2 > 0) && (d3 > 0) !== (d4 > 0);
}

function measure(name: string, raw: unknown) {
  const { graph } = readGraph(raw);

  // Timing: the whole draw-time arithmetic, layout and routing together, which
  // is what runs on every re-layout and every drag frame.
  let ms = Infinity;
  for (let run = 0; run < 3; run++) {
    const t0 = process.hrtime.bigint();
    const l = layoutGraph(graph, {}, []);
    const bx: Record<string, any> = {};
    for (const id of Object.keys(l.positions)) bx[id] = boxOf(l.positions[id], l.sizes[id]);
    geometryOf(graph.edges as any, bx);
    ms = Math.min(ms, Number(process.hrtime.bigint() - t0) / 1e6);
  }

  const layout = layoutGraph(graph, {}, []);
  const boxes: Record<string, any> = {};
  for (const id of Object.keys(layout.positions)) boxes[id] = boxOf(layout.positions[id], layout.sizes[id]);
  const routed = geometryOf(graph.edges as any, boxes);

  const polys = routed.map((r) => ({ pts: samplePath(r.geometry.d, SAMPLES), a: r.edge.from, b: r.edge.to }));

  // Occlusion: a drawn edge passing over a box that is neither of its endpoints.
  let through = 0;
  const occluded: string[] = [];
  const entries = Object.entries(boxes);
  for (let k = 0; k < polys.length; k++) {
    const p = polys[k];
    let over = false;
    for (let s = 1; s < p.pts.length - 1 && !over; s++) {
      const q = p.pts[s];
      for (const [id, bx] of entries) {
        if (id === p.a || id === p.b) continue;
        const o: any = bx;
        if (q.x > o.x && q.x < o.x + o.width && q.y > o.y && q.y < o.y + o.height) { over = true; break; }
      }
    }
    if (over) { through++; occluded.push(`${p.a} -> ${p.b}`); }
  }

  // Crossings, between edges that do not share an endpoint.
  const bbs = polys.map((p) => {
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const q of p.pts) { x0 = Math.min(x0, q.x); y0 = Math.min(y0, q.y); x1 = Math.max(x1, q.x); y1 = Math.max(y1, q.y); }
    return { x0, y0, x1, y1 };
  });
  let crossings = 0;
  const crossPairs: string[] = [];
  for (let i = 0; i < polys.length; i++) {
    for (let j = i + 1; j < polys.length; j++) {
      const p = polys[i], q = polys[j];
      if (p.a === q.a || p.a === q.b || p.b === q.a || p.b === q.b) continue;
      const bi = bbs[i], bj = bbs[j];
      if (bi.x1 < bj.x0 || bj.x1 < bi.x0 || bi.y1 < bj.y0 || bj.y1 < bi.y0) continue;
      let found = false;
      for (let s = 0; s + 1 < p.pts.length && !found; s++)
        for (let t = 0; t + 1 < q.pts.length && !found; t++)
          if (hits(p.pts[s], p.pts[s + 1], q.pts[t], q.pts[t + 1])) found = true;
      if (found) { crossings++; if (verbose && crossPairs.length < 12) crossPairs.push(`${p.a}->${p.b} x ${q.a}->${q.b}`); }
    }
  }

  // Labels, placed exactly as graph.ts places them.
  const placed = labelsOf(routed, Object.values(boxes));
  let overlaps = 0;
  const pairs: string[] = [];
  for (let i = 0; i < placed.length; i++)
    for (let j = i + 1; j < placed.length; j++) {
      const a = placed[i].plate, b = placed[j].plate;
      if (a.x + a.width > b.x && b.x + b.width > a.x && a.y + a.height > b.y && b.y + b.height > a.y) {
        overlaps++;
        pairs.push(`${placed[i].text} / ${placed[j].text}`);
      }
    }
  const dropped = placed.filter((p) => p.dropped).length;

  console.log(
    `${name}: ${graph.nodes.length} nodes, ${polys.length} drawn edges, ` +
      `${crossings} crossing pairs, ${through} occluded edges, ` +
      `${placed.length - dropped}/${placed.length} labels shown, ${overlaps} overlapping label pairs, ` +
      `layout+route ${ms.toFixed(1)}ms`,
  );
  if (verbose) {
    for (const o of occluded.slice(0, 20)) console.log(`    occluded: ${o}`);
    for (const p of pairs) console.log(`    label overlap: ${p}`);
    for (const c of crossPairs) console.log(`    crossing: ${c}`);
  }
}

measure('webstack', f.topology);
measure('large', f['topology.large']);
