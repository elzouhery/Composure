// Counts edge crossings in the SHIPPED layout, on the real topologies captured
// by harness/capture.mjs.
//
// NOTE: this file measures the plain two-box cubic (`edgeGeometry`), which is
// what every edge was before routing existed. It is kept because it is the
// instrument the 2026-08-13 gap report's table was produced with, and moving it
// would make that table unreproducible. For what the canvas DRAWS today —
// routed paths, placed labels, and the layout timing — use graphmetrics.ts,
// which also takes `--direct` to reproduce the numbers below.
//
// Measured on the CURVES the canvas actually draws: `edgeGeometry` returns one
// cubic per edge, and each is sampled into a polyline before intersection, so
// this is what a reader sees rather than a straight-line idealisation of it.
// Edges that share an endpoint are not counted — a fan into one node is a fan,
// not a crossing.
import { layoutGraph, boxOf, edgeGeometry } from '../webview/layout';
import { readGraph } from '../host/topology';
import * as fs from 'node:fs';
import * as path from 'node:path';

const f = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'fixtures/core.json'), 'utf8'));
const SAMPLES = 24;

type P = { x: number; y: number };
type Poly = { pts: P[]; a: string; b: string };

function bezier(p0: P, p1: P, p2: P, p3: P, t: number): P {
  const u = 1 - t;
  return {
    x: u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x,
    y: u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y,
  };
}
function parse(d: string): [P, P, P, P] {
  const n = d.match(/-?\d+(\.\d+)?/g)!.map(Number);
  return [{ x: n[0], y: n[1] }, { x: n[2], y: n[3] }, { x: n[4], y: n[5] }, { x: n[6], y: n[7] }];
}
function cr(o: P, a: P, b: P) { return (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x); }
function hits(A: P, B: P, C: P, D: P) {
  const d1 = cr(A, B, C), d2 = cr(A, B, D), d3 = cr(C, D, A), d4 = cr(C, D, B);
  return (d1 > 0) !== (d2 > 0) && (d3 > 0) !== (d4 > 0);
}
function bbox(p: Poly) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const q of p.pts) { x0 = Math.min(x0, q.x); y0 = Math.min(y0, q.y); x1 = Math.max(x1, q.x); y1 = Math.max(y1, q.y); }
  return { x0, y0, x1, y1 };
}

function measure(name: string, raw: unknown) {
  const { graph } = readGraph(raw);
  const layout = layoutGraph(graph, {}, []);
  const boxes: Record<string, any> = {};
  for (const id of Object.keys(layout.positions)) boxes[id] = boxOf(layout.positions[id], layout.sizes[id]);
  const polys: Poly[] = [];
  for (const e of graph.edges) {
    if (e.from === e.to) continue;
    const a = boxes[e.from], b = boxes[e.to];
    if (!a || !b) continue;
    const [p0, p1, p2, p3] = parse(edgeGeometry(a, b).d);
    const pts: P[] = [];
    for (let i = 0; i <= SAMPLES; i++) pts.push(bezier(p0, p1, p2, p3, i / SAMPLES));
    polys.push({ pts, a: e.from, b: e.to });
  }
  const boxesOf = polys.map(bbox);
  let n = 0;
  for (let i = 0; i < polys.length; i++) {
    for (let j = i + 1; j < polys.length; j++) {
      const p = polys[i], q = polys[j];
      if (p.a === q.a || p.a === q.b || p.b === q.a || p.b === q.b) continue;
      const bi = boxesOf[i], bj = boxesOf[j];
      if (bi.x1 < bj.x0 || bj.x1 < bi.x0 || bi.y1 < bj.y0 || bj.y1 < bi.y0) continue;
      let found = false;
      for (let s = 0; s < SAMPLES && !found; s++)
        for (let t = 0; t < SAMPLES && !found; t++)
          if (hits(p.pts[s], p.pts[s + 1], q.pts[t], q.pts[t + 1])) found = true;
      if (found) n++;
    }
  }
  console.log(`${name}: ${graph.nodes.length} nodes, ${polys.length} drawn edges, ${n} crossing pairs`);
}

measure('webstack', f.topology);
measure('large', f['topology.large']);

/* ---- what the tangle actually is on a small stack ----------------------- */

import { edgeLabel } from '../webview/layout';

function occlusion(name: string, raw: unknown) {
  const { graph } = readGraph(raw);
  const layout = layoutGraph(graph, {}, []);
  const boxes: Record<string, any> = {};
  for (const id of Object.keys(layout.positions)) boxes[id] = boxOf(layout.positions[id], layout.sizes[id]);

  // Edges whose curve passes over a node box that is neither of its endpoints.
  let through = 0;
  const labels: { x0: number; y0: number; x1: number; y1: number; t: string }[] = [];
  for (const e of graph.edges) {
    if (e.from === e.to) continue;
    const a = boxes[e.from], b = boxes[e.to];
    if (!a || !b) continue;
    const g = edgeGeometry(a, b);
    const [p0, p1, p2, p3] = parse(g.d);
    let over = false;
    for (let i = 1; i < SAMPLES && !over; i++) {
      const q = bezier(p0, p1, p2, p3, i / SAMPLES);
      for (const [id, box] of Object.entries(boxes)) {
        if (id === e.from || id === e.to) continue;
        const bx: any = box;
        if (q.x > bx.x && q.x < bx.x + bx.width && q.y > bx.y && q.y < bx.y + bx.height) { over = true; break; }
      }
    }
    if (over) through++;
    const text = edgeLabel(e);
    if (text !== '') {
      // The plate graph.ts draws: ~6.2px per character at the label size, 14 tall.
      const w = text.length * 6.2 + 10, h = 14;
      labels.push({ x0: g.mid.x - w / 2, y0: g.mid.y - h / 2, x1: g.mid.x + w / 2, y1: g.mid.y + h / 2, t: text });
    }
  }
  let overlaps = 0;
  for (let i = 0; i < labels.length; i++)
    for (let j = i + 1; j < labels.length; j++) {
      const a = labels[i], b = labels[j];
      if (a.x1 > b.x0 && b.x1 > a.x0 && a.y1 > b.y0 && b.y1 > a.y0) overlaps++;
    }
  console.log(`${name}: ${through} edges pass over an unrelated node box; ${labels.length} edge labels, ${overlaps} overlapping label pairs`);
}

occlusion('webstack', f.topology);
occlusion('large', f['topology.large']);
