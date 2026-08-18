"use strict";
var __create = Object.create;
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __getProtoOf = Object.getPrototypeOf;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
  // If the importer is in node compatibility mode or this is not an ESM
  // file that has been converted to a CommonJS file using a Babel-
  // compatible transform (i.e. "__esModule" has not been set), then set
  // "default" to the CommonJS "module.exports" for node compatibility.
  isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
  mod
));

// webview/layout.ts
var NODE_WIDTH = 188;
var NODE_HEIGHT = 94;
var RESOURCE_WIDTH = 152;
var RESOURCE_HEIGHT = 46;
var PORT_WIDTH = 132;
var PORT_HEIGHT = 46;
var DOCKERFILE_WIDTH = 172;
var DOCKERFILE_HEIGHT = 46;
var DETAIL_LINES = 2;
var DETAIL_COLUMNS = 23;
var GAP_X = 36;
var GAP_Y = 34;
var SATELLITE_GAP = 14;
var SATELLITE_INDENT = 14;
var DETAIL_Y = 41;
var LINE_HEIGHT = 16;
var MARKER_LINE_HEIGHT = 14;
var BOTTOM_PAD = 10;
function kindWord(kind) {
  return kind === "dockerfile" ? "Dockerfile" : kind;
}
function describeNode(node) {
  if (node.collapsed) {
    const n = node.collapsed.count;
    return `${n} ${n === 1 ? "service" : "services"} \xB7 collapsed by ${node.collapsed.by}`;
  }
  const tags = [];
  switch (node.kind) {
    case "service":
      if (node.image) {
        return node.image;
      }
      return node.declared ? "no image" : "not declared";
    case "port": {
      const p = node.port;
      if (!p) {
        return "port";
      }
      const host = p.host_ip ? `${p.host_ip}:` : "";
      const published = p.published ? `${host}${p.published} \u2192 ` : "exposed ";
      return `${published}${p.target}/${p.protocol}`;
    }
    case "dockerfile": {
      const b = node.build;
      if (!b) {
        return "Dockerfile";
      }
      if (b.inline) {
        return "inline, in the compose file";
      }
      return b.target ? `target ${b.target}` : `context ${b.context || "."}`;
    }
    default:
      tags.push(kindWord(node.kind));
      if (!node.declared) {
        tags.push("not declared");
      } else if (node.external) {
        tags.push("external");
      }
      if (node.internal) {
        tags.push("internal");
      }
      return tags.join(" \xB7 ");
  }
}
function serviceDetailLines(node) {
  return wrapImageRef(describeNode(node), DETAIL_COLUMNS, DETAIL_LINES);
}
function nodeSize(node, markers = 0) {
  switch (node.kind) {
    case "port":
      return { width: PORT_WIDTH, height: PORT_HEIGHT };
    case "dockerfile":
      return { width: DOCKERFILE_WIDTH, height: DOCKERFILE_HEIGHT };
    case "service": {
      const detail = serviceDetailLines(node).length;
      const content = DETAIL_Y + detail * LINE_HEIGHT + markers * MARKER_LINE_HEIGHT + BOTTOM_PAD;
      return { width: NODE_WIDTH, height: Math.max(NODE_HEIGHT, content) };
    }
    default:
      return { width: RESOURCE_WIDTH, height: RESOURCE_HEIGHT };
  }
}
function markerIndex(graph, missing = []) {
  const out = {};
  const push = (id, line) => {
    (out[id] ??= []).push(line);
  };
  for (const e of graph.edges) {
    if (e.kind !== "bind" || !e.mount) {
      continue;
    }
    const host = e.mount.host_path || e.mount.source || "(anonymous)";
    const mode = e.mount.read_only ? " ro" : "";
    push(e.from, `bind ${host} \u2192 ${e.mount.target}${mode}`);
  }
  for (const d of graph.dangling) {
    push(d.from, `unresolved ${d.kind} ${d.ref} \u2014 ${d.reason}`);
  }
  for (const cycle of graph.cycles) {
    const names = cycle.map(lastSegment);
    for (const id of cycle) {
      push(id, `dependency cycle: ${names.join(" \u2192 ")}`);
    }
  }
  for (const id of missing) {
    push(id, "missing \u2014 this file is not on disk");
  }
  return out;
}
function lastSegment(id) {
  const parts = id.split(".");
  return parts[parts.length - 1] || id;
}
function bandOf(node, maxLayer) {
  switch (node.kind) {
    case "port":
      return 0;
    case "service":
    case "dockerfile":
      return 1 + Math.max(0, maxLayer - node.layer);
    default:
      return maxLayer + 2;
  }
}
function layoutGraph(graph, saved, missing = []) {
  const markers = markerIndex(graph, missing);
  const sizes = {};
  for (const node of graph.nodes) {
    sizes[node.id] = nodeSize(node, markers[node.id]?.length ?? 0);
  }
  const parentOf = {};
  const satellites = /* @__PURE__ */ new Map();
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  for (const e of graph.edges) {
    if (e.kind !== "build" || !byId.has(e.to) || !byId.has(e.from)) {
      continue;
    }
    parentOf[e.to] = e.from;
    const list = satellites.get(e.from) ?? [];
    list.push(e.to);
    satellites.set(e.from, list);
  }
  const bandCount = Math.max(1, graph.max_layer + 3);
  const bands = Array.from({ length: bandCount }, () => []);
  for (const node of graph.nodes) {
    if (parentOf[node.id] !== void 0) {
      continue;
    }
    const band = Math.min(bandCount - 1, Math.max(0, bandOf(node, graph.max_layer)));
    bands[band].push(node.id);
  }
  orderBands(bands, graph.edges);
  const positions = {};
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
  for (const node of graph.nodes) {
    if (parentOf[node.id] !== void 0) {
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
  const ids = graph.nodes.map((n) => n.id).filter((id) => positions[id] !== void 0);
  const rows = navigationRows(ids, positions, sizes);
  return { positions, sizes, markers, rows, order: rows.flat(), parentOf };
}
var BAND_WRAP_MIN = 9;
function wrapBand(band) {
  if (band.length < BAND_WRAP_MIN) {
    return [band];
  }
  const perRow = Math.ceil(Math.sqrt(band.length * 1.6));
  const rows = [];
  for (let i = 0; i < band.length; i += perRow) {
    rows.push(band.slice(i, i + perRow));
  }
  return rows;
}
function satelliteExtent(id, satellites, sizes) {
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
var ORDER_SWEEPS = 12;
function orderSpan(bands, edges) {
  const at = /* @__PURE__ */ new Map();
  for (const band of bands) {
    const span = Math.max(1, band.length - 1);
    band.forEach((id, i) => at.set(id, band.length === 1 ? 0.5 : i / span));
  }
  let total = 0;
  for (const e of edges) {
    if (e.kind === "build" || e.from === e.to) {
      continue;
    }
    const u = at.get(e.from);
    const v = at.get(e.to);
    if (u !== void 0 && v !== void 0) {
      total += Math.abs(u - v);
    }
  }
  return total;
}
function orderBands(bands, edges) {
  const neighbours = /* @__PURE__ */ new Map();
  for (const e of edges) {
    if (e.kind === "build" || e.from === e.to) {
      continue;
    }
    (neighbours.get(e.from) ?? neighbours.set(e.from, []).get(e.from)).push(e.to);
    (neighbours.get(e.to) ?? neighbours.set(e.to, []).get(e.to)).push(e.from);
  }
  const sortAgainst = (band, reference) => {
    if (band.length < 2 || reference.length === 0) {
      return;
    }
    const rank = /* @__PURE__ */ new Map();
    reference.forEach((id, i) => rank.set(id, i));
    const span = Math.max(1, reference.length - 1);
    const ownSpan = Math.max(1, band.length - 1);
    const keyed = band.map((id, index) => {
      let sum = 0;
      let count = 0;
      for (const other of neighbours.get(id) ?? []) {
        const r = rank.get(other);
        if (r !== void 0) {
          sum += r;
          count++;
        }
      }
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
    for (let i = 1; i < bands.length; i++) {
      sortAgainst(bands[i], bands[i - 1]);
    }
    keepIfBetter();
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
function navigationRows(ids, positions, sizes) {
  const placed = ids.filter((id) => positions[id] !== void 0);
  const byY = [...placed].sort(
    (a, b) => positions[a].y - positions[b].y || positions[a].x - positions[b].x || (a < b ? -1 : a > b ? 1 : 0)
  );
  const rows = [];
  let current = [];
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
function sortByX(ids, positions) {
  return [...ids].sort(
    (a, b) => positions[a].x - positions[b].x || (a < b ? -1 : a > b ? 1 : 0)
  );
}
function boxOf(p, size) {
  return { x: p.x, y: p.y, width: size.width, height: size.height };
}
function edgeAnchors(a, b) {
  const aCx = a.x + a.width / 2;
  const bCx = b.x + b.width / 2;
  const aCy = a.y + a.height / 2;
  const bCy = b.y + b.height / 2;
  if (b.y >= a.y + a.height) {
    return { x1: aCx, y1: a.y + a.height, x2: bCx, y2: b.y, axis: "vertical" };
  }
  if (a.y >= b.y + b.height) {
    return { x1: aCx, y1: a.y, x2: bCx, y2: b.y + b.height, axis: "vertical" };
  }
  if (bCx >= aCx) {
    return { x1: a.x + a.width, y1: aCy, x2: b.x, y2: bCy, axis: "horizontal" };
  }
  return { x1: a.x, y1: aCy, x2: b.x + b.width, y2: bCy, axis: "horizontal" };
}
var CURVE_MIN = 14;
var CURVE_MAX = 64;
function edgeGeometry(a, b) {
  const { x1, y1, x2, y2, axis } = edgeAnchors(a, b);
  const distance = axis === "vertical" ? Math.abs(y2 - y1) : Math.abs(x2 - x1);
  const bend = Math.min(CURVE_MAX, Math.max(CURVE_MIN, distance / 2));
  const c1 = axis === "vertical" ? { x: x1, y: y1 + Math.sign(y2 - y1 || 1) * bend } : { x: x1 + Math.sign(x2 - x1 || 1) * bend, y: y1 };
  const c2 = axis === "vertical" ? { x: x2, y: y2 - Math.sign(y2 - y1 || 1) * bend } : { x: x2 - Math.sign(x2 - x1 || 1) * bend, y: y2 };
  const mid = {
    x: (x1 + 3 * c1.x + 3 * c2.x + x2) / 8,
    y: (y1 + 3 * c1.y + 3 * c2.y + y2) / 8
  };
  const d = `M${round(x1)} ${round(y1)}C${round(c1.x)} ${round(c1.y)} ${round(c2.x)} ${round(c2.y)} ${round(x2)} ${round(y2)}`;
  return { d, mid: { x: round(mid.x), y: round(mid.y) } };
}
function round(n) {
  return Math.round(n * 10) / 10;
}
function edgeLabel(edge) {
  if (edge.kind !== "depends_on" || !edge.depends_on) {
    return "";
  }
  const parts = [edge.depends_on.condition];
  if (edge.depends_on.required === "false") {
    parts.push("not required");
  }
  return parts.join(" \xB7 ");
}
function wrapText(text, columns, maxLines) {
  if (columns <= 0 || maxLines <= 0) {
    return [];
  }
  if (text.length <= columns) {
    return [text];
  }
  const lines = [];
  let rest = text;
  while (rest.length > columns && lines.length < maxLines - 1) {
    const window = rest.slice(0, columns + 1);
    let cut = Math.max(window.lastIndexOf("/"), window.lastIndexOf(":"));
    cut = cut > 0 ? cut + 1 : columns;
    const linesLeftAfter = maxLines - lines.length - 1;
    if (rest.length - cut > columns * linesLeftAfter) {
      cut = columns;
    }
    lines.push(rest.slice(0, cut));
    rest = rest.slice(cut);
  }
  lines.push(rest.length > columns ? `${rest.slice(0, columns - 1)}\u2026` : rest);
  return lines;
}
function wrapImageRef(image, columns, maxLines) {
  if (columns <= 0 || maxLines <= 0) {
    return [];
  }
  const budget = columns * maxLines;
  if (image.length <= budget) {
    return wrapText(image, columns, maxLines);
  }
  const tail = image.slice(-(budget - 1));
  return wrapText(`\u2026${tail}`, columns, maxLines);
}

// host/topology.ts
var MalformedGraphError = class extends Error {
  constructor(detail) {
    super(detail);
    this.detail = detail;
    this.name = "MalformedGraphError";
  }
};
function isRecord(v) {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function readGraph(raw) {
  if (!isRecord(raw)) {
    throw new MalformedGraphError("the core returned something that is not an object");
  }
  for (const key of ["nodes", "edges", "cycles", "dangling", "profiles"]) {
    if (!Array.isArray(raw[key])) {
      throw new MalformedGraphError(`the core's topology has no "${key}" array`);
    }
  }
  const nodes = raw.nodes.filter(
    (n) => isRecord(n) && typeof n.id === "string" && n.id !== "" && typeof n.kind === "string"
  );
  if (nodes.length !== raw.nodes.length) {
    throw new MalformedGraphError("the core returned a node with no id or no kind");
  }
  const known = new Set(nodes.map((n) => n.id));
  const edges = [];
  let droppedEdges = 0;
  for (const e of raw.edges) {
    if (!isRecord(e) || typeof e.from !== "string" || typeof e.to !== "string") {
      throw new MalformedGraphError("the core returned an edge with no endpoints");
    }
    if (!known.has(e.from) || !known.has(e.to)) {
      droppedEdges++;
      continue;
    }
    edges.push(e);
  }
  return {
    graph: {
      profiles: raw.profiles,
      nodes,
      edges,
      cycles: raw.cycles,
      dangling: raw.dangling,
      max_layer: typeof raw.max_layer === "number" ? raw.max_layer : 0
    },
    droppedEdges
  };
}

// harness/crossings.ts
var fs = __toESM(require("node:fs"));
var path = __toESM(require("node:path"));
var f = JSON.parse(fs.readFileSync(path.join(__dirname, "..", "fixtures/core.json"), "utf8"));
var SAMPLES = 24;
function bezier(p0, p1, p2, p3, t) {
  const u = 1 - t;
  return {
    x: u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x,
    y: u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y
  };
}
function parse(d) {
  const n = d.match(/-?\d+(\.\d+)?/g).map(Number);
  return [{ x: n[0], y: n[1] }, { x: n[2], y: n[3] }, { x: n[4], y: n[5] }, { x: n[6], y: n[7] }];
}
function cr(o, a, b) {
  return (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);
}
function hits(A, B, C, D) {
  const d1 = cr(A, B, C), d2 = cr(A, B, D), d3 = cr(C, D, A), d4 = cr(C, D, B);
  return d1 > 0 !== d2 > 0 && d3 > 0 !== d4 > 0;
}
function bbox(p) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const q of p.pts) {
    x0 = Math.min(x0, q.x);
    y0 = Math.min(y0, q.y);
    x1 = Math.max(x1, q.x);
    y1 = Math.max(y1, q.y);
  }
  return { x0, y0, x1, y1 };
}
function measure(name, raw) {
  const { graph } = readGraph(raw);
  const layout = layoutGraph(graph, {}, []);
  const boxes = {};
  for (const id of Object.keys(layout.positions)) boxes[id] = boxOf(layout.positions[id], layout.sizes[id]);
  const polys = [];
  for (const e of graph.edges) {
    if (e.from === e.to) continue;
    const a = boxes[e.from], b = boxes[e.to];
    if (!a || !b) continue;
    const [p0, p1, p2, p3] = parse(edgeGeometry(a, b).d);
    const pts = [];
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
measure("webstack", f.topology);
measure("large", f["topology.large"]);
function occlusion(name, raw) {
  const { graph } = readGraph(raw);
  const layout = layoutGraph(graph, {}, []);
  const boxes = {};
  for (const id of Object.keys(layout.positions)) boxes[id] = boxOf(layout.positions[id], layout.sizes[id]);
  let through = 0;
  const labels = [];
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
        const bx = box;
        if (q.x > bx.x && q.x < bx.x + bx.width && q.y > bx.y && q.y < bx.y + bx.height) {
          over = true;
          break;
        }
      }
    }
    if (over) through++;
    const text = edgeLabel(e);
    if (text !== "") {
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
occlusion("webstack", f.topology);
occlusion("large", f["topology.large"]);
