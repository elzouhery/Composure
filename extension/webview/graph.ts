// The graph pane: inline SVG, no framework, no graph library.
//
// It draws the graph the Go core derived — every node kind and every edge kind
// it reports — and it decides nothing about the stack. Where a box goes and
// where a line runs is arithmetic, and all of that arithmetic lives in
// layout.ts where a test can reach it. What is left here is element creation
// and event plumbing.
//
// Every colour comes from the stylesheet, and every colour in the stylesheet is
// a `--vscode-*` token. Nothing here sets a fill or a stroke.

import {
  columnsFor,
  contentBox,
  clampScale,
  describeNode,
  edgeLabel,
  edgeLayers,
  fitToView,
  placeLabels,
  routeEdges,
  kindWord,
  layoutGraph,
  navigate,
  serviceDetailLines,
  severityBadge,
  shouldRefit,
  truncateHead,
  truncateTail,
  boxOf,
  DETAIL_COLUMNS,
  DETAIL_Y,
  LINE_HEIGHT,
  MARKER_LINE_HEIGHT,
  NAME_COLUMNS,
  NAME_Y,
  NODE_HEIGHT,
  PAD_X,
  type Box,
  type GraphLayout,
  type Viewport,
} from './layout';
import type {
  GraphEdge,
  GraphNode,
  Point,
  SeverityCount,
  StackGraph,
} from '../shared/protocol';

const SVG_NS = 'http://www.w3.org/2000/svg';

/** The empty graph, so a caller never has to hold a null. */
export const EMPTY_GRAPH: StackGraph = {
  profiles: [],
  nodes: [],
  edges: [],
  cycles: [],
  dangling: [],
  max_layer: 0,
};

export interface GraphCallbacks {
  onSelect: (node: GraphNode | null) => void;
  onMove: (positions: Record<string, Point>) => void;
  /**
   * The reader deliberately opened a node — a click that was not a drag, or
   * Enter on the focused node. Story 6.3 uses it to open a Dockerfile's stage
   * form.
   *
   * It is separate from onSelect on purpose. Arrow-key traversal moves the
   * selection across every node in the graph, and if traversal opened things,
   * holding an arrow key would open a form per keystroke.
   */
  onActivate?: (node: GraphNode) => void;
}

interface Rendered {
  node: GraphNode;
  group: SVGGElement;
}

/**
 * How far a pointer may travel and still count as a click rather than a drag.
 *
 * Without a slop a reader with an ordinary hand never produces a click on a
 * draggable node: a press always moves a pixel or two, and the node would
 * silently reposition instead of opening.
 */
const CLICK_SLOP_PX = 3;

/** The widths a clip path is needed for, keyed by node kind. */
const CLIP_KINDS = ['service', 'network', 'port', 'dockerfile'] as const;

export class GraphView {
  readonly element: SVGSVGElement;
  private readonly viewportGroup: SVGGElement;
  private readonly edgeLayer: SVGGElement;
  private readonly labelLayer: SVGGElement;
  private readonly nodeLayer: SVGGElement;

  private graph: StackGraph = EMPTY_GRAPH;
  /**
   * Findings per node, by config path — story 5.4's badge. Empty until the
   * host has diagnosed the file; a node with no entry carries no badge, which
   * is the honest rendering of "nothing found" and of "not yet asked" alike.
   */
  private severities: Record<string, SeverityCount> = {};
  private placement: GraphLayout = {
    positions: {},
    sizes: {},
    markers: {},
    rows: [],
    order: [],
    parentOf: {},
  };
  private rendered = new Map<string, Rendered>();
  /**
   * The nodes search or focus mode brought forward, or null for no filter —
   * story 4.4.
   *
   * Held here so a redraw can re-apply it, and applied as CLASSES ONLY. Nothing
   * about emphasis touches a position: a search that moved the boxes would
   * destroy the spatial memory it exists to serve, and that is the acceptance
   * criterion the whole feature turns on.
   */
  private emphasised: Set<string> | null = null;
  private view: Viewport = { x: 0, y: 0, k: 1 };
  private selectedId: string | null = null;
  private panning: {
    pointerId: number;
    startX: number;
    startY: number;
    origin: Viewport;
  } | null = null;
  private dragging: {
    pointerId: number;
    id: string;
    offsetX: number;
    offsetY: number;
    /** Where the press landed, so a click can be told from a drag. */
    startX: number;
    startY: number;
    moved: boolean;
  } | null = null;

  /**
   * Set once the reader pans, zooms or drags. After that the view is theirs and
   * a resize must not yank it back to fit — but until then, fit is the right
   * answer every time the pane changes size.
   */
  private userAdjusted = false;
  private resizeObserver: ResizeObserver | undefined;

  constructor(private readonly callbacks: GraphCallbacks) {
    this.element = document.createElementNS(SVG_NS, 'svg');
    this.element.appendChild(this.defs());
    this.element.classList.add('graph');
    // A listbox, because a graph reachable only by mouse fails outright.
    this.element.setAttribute('role', 'listbox');
    this.element.setAttribute('aria-label', 'Nodes in this stack');
    this.element.setAttribute('tabindex', '0');

    this.viewportGroup = document.createElementNS(SVG_NS, 'g');
    // Edges under labels under nodes: a line crossing a box must go behind it,
    // and a condition must never be hidden by the box it sits beside.
    this.edgeLayer = document.createElementNS(SVG_NS, 'g');
    this.edgeLayer.setAttribute('class', 'edges');
    this.labelLayer = document.createElementNS(SVG_NS, 'g');
    this.labelLayer.setAttribute('class', 'edge-labels');
    this.nodeLayer = document.createElementNS(SVG_NS, 'g');
    this.nodeLayer.setAttribute('class', 'nodes');
    this.viewportGroup.append(this.edgeLayer, this.labelLayer, this.nodeLayer);
    this.element.appendChild(this.viewportGroup);

    // Re-fit whenever the pane changes size, until the reader takes over.
    //
    // The graph was previously fitted exactly once, against whatever the canvas
    // measured at first paint — which in a webview is routinely before layout
    // has settled, or at a width the panel is about to change. The result was a
    // stack rendered tiny in the top-left corner of an enormous empty canvas:
    // precisely the failure this product exists to replace.
    if (typeof ResizeObserver !== 'undefined') {
      this.resizeObserver = new ResizeObserver(() => {
        const rect = this.element.getBoundingClientRect();
        if (
          shouldRefit({
            hasNodes: this.graph.nodes.length > 0,
            userAdjusted: this.userAdjusted,
            width: rect.width,
            height: rect.height,
          })
        ) {
          this.fit();
        }
      });
      this.resizeObserver.observe(this.element);
    }

    this.element.addEventListener('keydown', (e) => this.onKeyDown(e));
    this.element.addEventListener('wheel', (e) => this.onWheel(e), { passive: false });
    this.element.addEventListener('pointerdown', (e) => this.onBackgroundPointerDown(e));
    this.element.addEventListener('pointermove', (e) => this.onPointerMove(e));
    this.element.addEventListener('pointerup', (e) => this.onPointerUp(e));
    this.element.addEventListener('pointercancel', (e) => this.onPointerUp(e));
  }

  /**
   * Clips and arrowheads.
   *
   * One clip per node width — text coordinates are local to each node's
   * transform, so a clip is shared by every node of a kind. This is the
   * guarantee behind the column counts: they are right for the font sizes
   * people use, and the clip is what keeps a 20px editor font from spilling an
   * image reference across its neighbour.
   */
  private defs(): SVGDefsElement {
    const defs = document.createElementNS(SVG_NS, 'defs');
    for (const kind of CLIP_KINDS) {
      const clip = document.createElementNS(SVG_NS, 'clipPath');
      clip.setAttribute('id', `composure-clip-${kind}`);
      const rect = document.createElementNS(SVG_NS, 'rect');
      // Generous in height and exact in width: the overflow that matters is
      // sideways, and a node's height is computed to fit its own text.
      rect.setAttribute('width', String(clipWidth(kind)));
      rect.setAttribute('height', '400');
      clip.appendChild(rect);
      defs.appendChild(clip);
    }
    for (const variant of ['default', 'strong', 'build']) {
      const marker = document.createElementNS(SVG_NS, 'marker');
      marker.setAttribute('id', `composure-arrow-${variant}`);
      marker.setAttribute('viewBox', '0 0 8 8');
      marker.setAttribute('refX', '7');
      marker.setAttribute('refY', '4');
      marker.setAttribute('markerWidth', '6');
      marker.setAttribute('markerHeight', '6');
      marker.setAttribute('orient', 'auto-start-reverse');
      const head = document.createElementNS(SVG_NS, 'path');
      head.setAttribute('class', `arrow-head arrow-${variant}`);
      head.setAttribute('d', 'M0 1L7 4L0 7z');
      marker.appendChild(head);
      defs.appendChild(marker);
    }
    return defs;
  }

  /**
   * Draws a graph. `fit` auto-fits; otherwise the reader's view is kept.
   *
   * `selected` is the selection to restore, by config path — the host's stored
   * one on a first draw. Omit it to keep whatever is selected now.
   */
  render(
    graph: StackGraph,
    saved: Record<string, Point>,
    fit: boolean,
    selected?: string | null,
    severities?: Record<string, SeverityCount>,
    missing: string[] = [],
  ): void {
    // Every group in the viewport is about to be destroyed, and destroying the
    // focused element drops focus to <body>. A save while a node is focused is
    // an ordinary thing to do, and losing your place in the graph because the
    // file was written is not acceptable for a keyboard reader. The node is
    // identified by config path, so it survives the rebuild.
    if (fit) {
      this.userAdjusted = false;
    }
    const focusedId = this.focusedNodeId();
    const hadFocus = focusedId !== null || document.activeElement === this.element;

    this.graph = graph;
    this.severities = severities ?? {};
    this.placement = layoutGraph(graph, saved, missing);

    this.edgeLayer.textContent = '';
    this.labelLayer.textContent = '';
    this.nodeLayer.textContent = '';
    this.rendered.clear();

    this.drawEdges();
    for (const node of graph.nodes) {
      const group = this.createNode(node);
      this.nodeLayer.appendChild(group);
      this.rendered.set(node.id, { node, group });
    }

    // A selection that no longer exists is dropped rather than remembered.
    const wanted = selected === undefined ? this.selectedId : selected;
    const resolved = wanted !== null && this.rendered.has(wanted) ? wanted : null;
    const dropped = this.selectedId !== null && resolved === null;
    this.selectedId = resolved;

    if (fit) {
      this.fit();
    } else {
      this.applyViewport();
    }
    this.applySelection();
    this.applyRovingTabIndex();
    this.applyEmphasis();

    if (dropped) {
      this.callbacks.onSelect(null);
    } else if (selected !== undefined && resolved !== null) {
      // A restored selection is still a selection change as far as the shell's
      // strip is concerned.
      this.callbacks.onSelect(this.selection());
    }

    if (hadFocus) {
      this.restoreFocus(focusedId);
    }
  }

  /**
   * Brings a set of nodes forward and pushes the rest back — story 4.4.
   *
   * `null` clears the filter. This is a class toggle over the nodes already
   * drawn: no layout runs, no position changes, and no element is created or
   * destroyed, so the canvas the reader was looking at is the canvas they are
   * still looking at. Dimmed nodes keep their tab stop and their accessible
   * name; dimming is emphasis, not removal, and a keyboard reader must still be
   * able to walk the whole stack.
   */
  emphasise(ids: Set<string> | null): void {
    this.emphasised = ids;
    this.applyEmphasis();
  }

  private applyEmphasis(): void {
    const active = this.emphasised !== null;
    this.element.classList.toggle('is-filtered', active);
    for (const [id, entry] of this.rendered) {
      const match = active && this.emphasised!.has(id);
      entry.group.classList.toggle('is-match', match);
      entry.group.classList.toggle('is-dimmed', active && !match);
    }
  }

  /** Every node in the drawn graph, in reading order. */
  nodes(): GraphNode[] {
    const byId = new Map(this.graph.nodes.map((n) => [n.id, n]));
    return this.placement.order.map((id) => byId.get(id)!).filter(Boolean);
  }

  /* ---- edges ---------------------------------------------------------- */

  private boxes(): Record<string, Box> {
    const out: Record<string, Box> = {};
    for (const id of Object.keys(this.placement.positions)) {
      out[id] = boxOf(this.placement.positions[id], this.placement.sizes[id]);
    }
    return out;
  }

  private drawEdges(): void {
    const boxes = this.boxes();
    // Routed once and used twice: the paths and the labels have to agree about
    // where an edge runs, and routing it a second time for the label is the
    // most expensive arithmetic on the pane done again for nothing.
    const routed = routeEdges(this.graph.edges, boxes);
    for (const { kind, d, count } of edgeLayers(routed)) {
      const path = document.createElementNS(SVG_NS, 'path');
      path.setAttribute('class', `edge edge-${kind}`);
      path.setAttribute('d', d);
      path.setAttribute('fill', 'none');
      path.setAttribute('marker-end', `url(#composure-arrow-${arrowVariant(kind)})`);
      // Decorative to a screen reader: the relations reach it through each
      // node's description, which is text rather than geometry.
      path.setAttribute('aria-hidden', 'true');
      path.dataset.count = String(count);
      this.edgeLayer.appendChild(path);
    }

    const wanted = routed
      .map(({ edge, geometry }) => ({ edge, text: edgeLabel(edge), mid: geometry.mid }))
      .filter((entry) => entry.text !== '');
    for (const placement of placeLabels(wanted, Object.values(boxes))) {
      // A label with nowhere legible to go is not drawn. Two conditions
      // printed through each other are not two labels, they are neither.
      if (placement.dropped) {
        continue;
      }
      this.labelLayer.appendChild(
        this.edgeLabelElement(placement.edge, placement.text, placement.at),
      );
    }
  }

  /**
   * A `depends_on` condition, placed on its own edge.
   *
   * It gets a backing rectangle because the edge runs underneath it, and
   * `service_healthy` legible only where it does not cross a line is not
   * legible. The rectangle is sized from the text length in the same monospace
   * budget every other measurement here uses.
   */
  private edgeLabelElement(edge: GraphEdge, text: string, mid: Point): SVGGElement {
    const group = document.createElementNS(SVG_NS, 'g');
    group.setAttribute('class', `edge-label condition-${edge.depends_on?.condition ?? 'unknown'}`);
    group.setAttribute('aria-hidden', 'true');
    group.setAttribute('transform', `translate(${mid.x} ${mid.y})`);

    const width = text.length * 6.2 + 8;
    const plate = document.createElementNS(SVG_NS, 'rect');
    plate.setAttribute('class', 'edge-label-plate');
    plate.setAttribute('x', String(-width / 2));
    plate.setAttribute('y', '-8');
    plate.setAttribute('width', String(width));
    plate.setAttribute('height', '15');
    plate.setAttribute('rx', '3');

    const label = document.createElementNS(SVG_NS, 'text');
    label.setAttribute('class', 'edge-label-text');
    label.setAttribute('text-anchor', 'middle');
    label.setAttribute('y', '3');
    label.textContent = text;

    group.append(plate, label);
    return group;
  }

  /* ---- nodes ---------------------------------------------------------- */

  private createNode(node: GraphNode): SVGGElement {
    const group = document.createElementNS(SVG_NS, 'g');
    const size = this.placement.sizes[node.id] ?? { width: 0, height: NODE_HEIGHT };
    group.setAttribute('class', `node node-${node.kind}`);
    group.setAttribute('role', 'option');
    group.setAttribute('tabindex', '-1');
    group.setAttribute('aria-selected', 'false');
    group.setAttribute('id', nodeElementId(node.id));
    // One label per node — never the service name and container name both. The
    // kind is said as well, because a screen reader has no shape to read.
    const badge = severityBadge(this.severities[node.id]);
    // A folded group says what it is and how to open it — story 4.4. Without
    // the last clause a keyboard reader is told there is a box and not that
    // pressing Enter unfolds it, which makes the capability pointer-only in
    // everything but the code.
    const name = node.collapsed
      ? `${node.name}, ${describeNode(node)}, press Enter to expand`
      : `${node.name}, ${kindWord(node.kind)}`;
    group.setAttribute('aria-label', badge === null ? name : `${name}, ${badge.word}`);
    if (node.collapsed) {
      group.classList.add('is-group');
    }
    group.dataset.id = node.id;
    group.dataset.kind = node.kind;
    if (!node.declared) {
      group.classList.add('is-undeclared');
    }
    if (node.external) {
      group.classList.add('is-external');
    }

    const box = document.createElementNS(SVG_NS, 'rect');
    box.setAttribute('class', 'node-box');
    box.setAttribute('width', String(size.width));
    box.setAttribute('height', String(size.height));
    box.setAttribute('rx', '4');
    group.appendChild(box);

    const clip = `url(#composure-clip-${clipKindOf(node)})`;
    const columns = columnsFor(node.kind);

    const label = document.createElementNS(SVG_NS, 'text');
    label.setAttribute('class', 'node-label');
    label.setAttribute('x', String(PAD_X));
    label.setAttribute('y', String(node.kind === 'service' ? NAME_Y : 20));
    label.setAttribute('clip-path', clip);
    label.textContent = truncateHead(
      node.name,
      node.kind === 'service' ? NAME_COLUMNS : columns,
    );
    group.appendChild(label);

    const detail = document.createElementNS(SVG_NS, 'text');
    detail.setAttribute('class', 'node-detail');
    detail.setAttribute('x', String(PAD_X));
    detail.setAttribute('clip-path', clip);
    let bottom: number;
    if (node.kind === 'service') {
      // The image is wrapped, not elided: the tag is the part a reader scans
      // for, and a single elided line is exactly where it gets cut off.
      detail.setAttribute('y', String(DETAIL_Y));
      const lines = serviceDetailLines(node);
      lines.forEach((line, i) => {
        const span = document.createElementNS(SVG_NS, 'tspan');
        span.setAttribute('x', String(PAD_X));
        if (i > 0) {
          span.setAttribute('dy', '1.25em');
        }
        span.textContent = line;
        detail.appendChild(span);
      });
      bottom = DETAIL_Y + lines.length * LINE_HEIGHT;
    } else {
      detail.setAttribute('y', '36');
      detail.textContent = truncateTail(describeNode(node), columns);
      bottom = 36 + LINE_HEIGHT;
    }
    group.appendChild(detail);

    // Bind mounts, dangling references and cycle membership: the three things
    // that have no far side to draw a line to.
    const markers = this.placement.markers[node.id] ?? [];
    markers.forEach((marker, i) => {
      const line = document.createElementNS(SVG_NS, 'text');
      line.setAttribute('class', `node-marker ${markerClass(marker)}`);
      line.setAttribute('x', String(PAD_X));
      line.setAttribute('y', String(bottom + i * MARKER_LINE_HEIGHT));
      line.setAttribute('clip-path', clip);
      line.textContent = truncateTail(marker, DETAIL_COLUMNS);
      group.appendChild(line);
    });

    // The diagnostic count, top-right — story 5.4. Problems are visible before
    // anything is selected, so a reader opening a stack knows where to look.
    if (badge !== null) {
      // Sized against the card, not against nothing: 11px across on a card that
      // is now 53px tall rather than 94. At r = 6.5 the dot was a quarter of the
      // card's height and read as a second element competing with the name; the
      // mockup's is r = 5 on the same 188px card (`directions-3.html:293`).
      const dot = document.createElementNS(SVG_NS, 'circle');
      dot.setAttribute('class', `node-badge node-badge-${badge.level}`);
      dot.setAttribute('cx', String(size.width - 11));
      dot.setAttribute('cy', '10');
      dot.setAttribute('r', '5.5');
      const count = document.createElementNS(SVG_NS, 'text');
      count.setAttribute('class', 'node-badge-count');
      count.setAttribute('x', String(size.width - 11));
      count.setAttribute('y', '13');
      count.setAttribute('text-anchor', 'middle');
      count.textContent = badge.label;
      group.append(dot, count);
    }

    const title = document.createElementNS(SVG_NS, 'title');
    title.textContent = [
      `${node.name} — ${kindWord(node.kind)}`,
      describeNode(node),
      ...markers,
      ...(badge === null ? [] : [badge.word]),
      `${node.origin.file}:${node.origin.line}`,
    ].join('\n');
    group.appendChild(title);

    group.addEventListener('pointerdown', (e) => this.onNodePointerDown(e, node));
    group.addEventListener('focus', () => this.select(node.id, false));

    this.place(group, this.positionOf(node.id));
    return group;
  }

  /* ---- view ----------------------------------------------------------- */

  /** The config path of the node that currently holds focus, if any. */
  private focusedNodeId(): string | null {
    const active = document.activeElement;
    if (!active || !(active instanceof Element)) {
      return null;
    }
    const group = active.closest('.node');
    const id = group instanceof SVGElement ? group.dataset.id : undefined;
    return id !== undefined && this.rendered.has(id) ? id : null;
  }

  /**
   * Puts focus back where it was, on the node with the same config path.
   *
   * If that node is gone, focus lands on the selection, then on the graph
   * itself — anywhere but <body>, which is nowhere.
   */
  private restoreFocus(id: string | null): void {
    const target =
      (id && this.rendered.get(id)) || (this.selectedId && this.rendered.get(this.selectedId));
    if (target) {
      target.group.focus();
      return;
    }
    this.element.focus();
  }

  /** The current positions, for persisting as view state. */
  currentPositions(): Record<string, Point> {
    return { ...this.placement.positions };
  }

  selection(): GraphNode | null {
    return this.selectedId ? (this.rendered.get(this.selectedId)?.node ?? null) : null;
  }

  /** Fits the whole graph into the pane. Called on open and on demand. */
  fit(): void {
    this.userAdjusted = false;
    const rect = this.element.getBoundingClientRect();
    this.view = fitToView(
      contentBox(this.placement.positions, this.placement.sizes),
      rect.width,
      rect.height,
    );
    this.applyViewport();
  }

  private positionOf(id: string): Point {
    return this.placement.positions[id] ?? { x: 0, y: 0 };
  }

  private place(group: SVGGElement, p: Point): void {
    group.setAttribute('transform', `translate(${p.x} ${p.y})`);
  }

  private applyViewport(): void {
    this.viewportGroup.setAttribute(
      'transform',
      `translate(${this.view.x} ${this.view.y}) scale(${this.view.k})`,
    );
  }

  private applySelection(): void {
    for (const [id, entry] of this.rendered) {
      const selected = id === this.selectedId;
      entry.group.classList.toggle('is-selected', selected);
      entry.group.setAttribute('aria-selected', selected ? 'true' : 'false');
    }
    if (this.selectedId) {
      this.element.setAttribute('aria-activedescendant', nodeElementId(this.selectedId));
    } else {
      this.element.removeAttribute('aria-activedescendant');
    }
  }

  /**
   * Roving tabindex: one Tab stop into the graph, arrows from there. The ring
   * is never removed — the stylesheet draws it and there is no `outline: none`
   * anywhere in this extension.
   */
  private applyRovingTabIndex(): void {
    const first = this.selectedId ?? this.placement.order[0];
    for (const [id, entry] of this.rendered) {
      entry.group.setAttribute('tabindex', id === first ? '0' : '-1');
    }
    this.element.setAttribute('tabindex', this.graph.nodes.length === 0 ? '0' : '-1');
  }

  private select(id: string | null, focus: boolean): void {
    if (this.selectedId === id) {
      if (focus && id) {
        this.rendered.get(id)?.group.focus();
      }
      return;
    }
    this.selectedId = id;
    this.applySelection();
    this.applyRovingTabIndex();
    if (id && focus) {
      this.rendered.get(id)?.group.focus();
    }
    this.callbacks.onSelect(this.selection());
  }

  /** Tells the shell the reader opened a node, if anything is listening. */
  private activate(id: string): void {
    const entry = this.rendered.get(id);
    if (entry) {
      this.callbacks.onActivate?.(entry.node);
    }
  }

  /** Selects by config path, from outside — the narrow list uses this. */
  selectById(id: string | null): void {
    this.select(id !== null && this.rendered.has(id) ? id : null, false);
  }

  private onKeyDown(e: KeyboardEvent): void {
    if (this.placement.order.length === 0) {
      return;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      this.select(null, false);
      return;
    }
    if (e.key === 'Enter' || e.key === ' ') {
      if (this.selectedId) {
        e.preventDefault();
        this.select(this.selectedId, true);
        // Enter is the keyboard's click. Whatever a mouse press opens, this
        // opens, or the canvas is reachable by keyboard but not usable by one.
        this.activate(this.selectedId);
      }
      return;
    }
    const next = navigate(this.placement.rows, this.placement.positions, this.selectedId, e.key);
    if (next === null) {
      return;
    }
    e.preventDefault();
    this.select(next, true);
    this.ensureVisible(next);
  }

  /** Pans just far enough that a keyboard-selected node is on screen. */
  private ensureVisible(id: string): void {
    const p = this.positionOf(id);
    const size = this.placement.sizes[id] ?? { width: 0, height: NODE_HEIGHT };
    const rect = this.element.getBoundingClientRect();
    const pad = 16;
    const left = p.x * this.view.k + this.view.x;
    const top = p.y * this.view.k + this.view.y;
    const right = left + size.width * this.view.k;
    const bottom = top + size.height * this.view.k;
    if (left < pad) {
      this.view.x += pad - left;
    } else if (right > rect.width - pad) {
      this.view.x -= right - (rect.width - pad);
    }
    if (top < pad) {
      this.view.y += pad - top;
    } else if (bottom > rect.height - pad) {
      this.view.y -= bottom - (rect.height - pad);
    }
    this.applyViewport();
  }

  private onWheel(e: WheelEvent): void {
    e.preventDefault();
    this.userAdjusted = true;
    const rect = this.element.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const py = e.clientY - rect.top;
    const k = clampScale(this.view.k * Math.exp(-e.deltaY / 400));
    // Zoom about the pointer, so the thing under the cursor stays under it.
    this.view = {
      k,
      x: px - ((px - this.view.x) / this.view.k) * k,
      y: py - ((py - this.view.y) / this.view.k) * k,
    };
    this.applyViewport();
  }

  private onBackgroundPointerDown(e: PointerEvent): void {
    if (this.dragging || e.button !== 0) {
      return;
    }
    this.userAdjusted = true;
    if ((e.target as Element).closest('.node')) {
      return; // the node handler owns this one
    }
    this.panning = {
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      origin: { ...this.view },
    };
    this.element.setPointerCapture(e.pointerId);
    this.element.classList.add('is-panning');
  }

  private onNodePointerDown(e: PointerEvent, node: GraphNode): void {
    if (e.button !== 0) {
      return;
    }
    e.stopPropagation();
    this.select(node.id, true);
    const p = this.positionOf(node.id);
    this.dragging = {
      pointerId: e.pointerId,
      id: node.id,
      offsetX: e.clientX / this.view.k - p.x,
      offsetY: e.clientY / this.view.k - p.y,
      startX: e.clientX,
      startY: e.clientY,
      moved: false,
    };
    this.element.setPointerCapture(e.pointerId);
  }

  private onPointerMove(e: PointerEvent): void {
    if (this.dragging && this.dragging.pointerId === e.pointerId) {
      const entry = this.rendered.get(this.dragging.id);
      if (!entry) {
        return;
      }
      const p = {
        x: e.clientX / this.view.k - this.dragging.offsetX,
        y: e.clientY / this.view.k - this.dragging.offsetY,
      };
      if (
        Math.abs(e.clientX - this.dragging.startX) > CLICK_SLOP_PX ||
        Math.abs(e.clientY - this.dragging.startY) > CLICK_SLOP_PX
      ) {
        this.dragging.moved = true;
      }
      this.placement.positions[this.dragging.id] = p;
      this.place(entry.group, p);
      // The lines have to follow the box. Edges are one path per kind, so this
      // is a handful of attribute writes rather than one per edge.
      this.redrawEdges();
      return;
    }
    if (this.panning && this.panning.pointerId === e.pointerId) {
      this.view = {
        k: this.panning.origin.k,
        x: this.panning.origin.x + (e.clientX - this.panning.startX),
        y: this.panning.origin.y + (e.clientY - this.panning.startY),
      };
      this.applyViewport();
    }
  }

  private redrawEdges(): void {
    this.edgeLayer.textContent = '';
    this.labelLayer.textContent = '';
    this.drawEdges();
  }

  private onPointerUp(e: PointerEvent): void {
    if (this.dragging && this.dragging.pointerId === e.pointerId) {
      const { id, moved } = this.dragging;
      this.dragging = null;
      this.element.releasePointerCapture?.(e.pointerId);
      if (!moved) {
        // A press that did not travel is a click, not a drag. Repositioning a
        // node by a pixel must not count as opening it, and opening it must not
        // count as repositioning it.
        this.activate(id);
        return;
      }
      // Position is view state. It is persisted per workspace by the host and
      // it never, under any circumstance, enters the compose file.
      this.callbacks.onMove(this.currentPositions());
      return;
    }
    if (this.panning && this.panning.pointerId === e.pointerId) {
      this.panning = null;
      this.element.releasePointerCapture?.(e.pointerId);
      this.element.classList.remove('is-panning');
    }
  }
}

/** Which shared clip a node's text uses. Grouped by box width. */
function clipKindOf(node: GraphNode): string {
  switch (node.kind) {
    case 'service':
    case 'port':
    case 'dockerfile':
      return node.kind;
    default:
      return 'network';
  }
}

function clipWidth(kind: string): number {
  switch (kind) {
    case 'service':
      return 188;
    case 'port':
      return 132;
    case 'dockerfile':
      return 172;
    default:
      return 152;
  }
}

/** Which arrowhead an edge kind takes. Three, so a marker can carry a colour. */
export function arrowVariant(kind: string): 'default' | 'strong' | 'build' {
  if (kind === 'depends_on') {
    return 'strong';
  }
  if (kind === 'build') {
    return 'build';
  }
  return 'default';
}

/** Severity carried in text as well as style: an unresolved marker says so. */
export function markerClass(marker: string): string {
  if (marker.startsWith('missing')) {
    return 'marker-missing';
  }
  if (marker.startsWith('unresolved')) {
    return 'marker-unresolved';
  }
  if (marker.startsWith('dependency cycle')) {
    return 'marker-cycle';
  }
  return 'marker-bind';
}

function nodeElementId(id: string): string {
  return `node:${id}`;
}
