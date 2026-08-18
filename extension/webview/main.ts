// The webview shell: a header strip, a banner, a legend, and the graph.
//
// It holds no model. Every node and every edge it draws arrived from the Go
// core through the extension host, and the only things it sends back are the
// two facts it alone knows — where a node was dragged and which node is
// selected. Nothing here parses YAML, merges a file or touches a disk.
//
// No inline handlers anywhere: the CSP is `default-src 'none'` with a nonce,
// and every listener below is attached in code.

import { EMPTY_GRAPH, GraphView } from './graph';
import { InspectorView } from './inspector';
import { PendingView } from './pending';
import { StageFormView } from './stageform';
import { AddFormView } from './addform';
import { describeNode, kindWord, legendEntries, stackSummary } from './layout';
import {
  arrangeStatus,
  collapse,
  collapsedPositions,
  collapseStatus,
  emphasis,
  focusStatus,
  impactSet,
  persistablePositions,
  profileNotice,
  searchMatches,
  searchStatus,
  type CollapseBy,
  type CollapsedView,
} from './view';
import type {
  GraphNode,
  Failure,
  HostMessage,
  Point,
  SeverityCount,
  StackGraph,
  WebviewMessage,
} from '../shared/protocol';

declare function acquireVsCodeApi(): { postMessage(msg: WebviewMessage): void };

const vscode = acquireVsCodeApi();

/** Nothing under 200ms; a thin bar beyond it. Never a spinner over the canvas. */
const LOADING_DELAY_MS = 200;

/**
 * Below this the canvas is hidden and the stack is presented as a list.
 *
 * Not just a strip: hiding the canvas also hides the graph, which is the only
 * thing in the pane that can be selected or focused, and the empty state, which
 * lives inside it. A narrow panel used to show one line of text and offer no
 * way to reach anything. The list below is the same information in the shape a
 * 400px column can actually hold, and every row is a real button.
 */
const NARROW_PX = 600;

/**
 * The graph pane's default share of the panel.
 *
 * DESIGN.md: graph left at roughly 60%, inspector right at 40%, both
 * permanently visible. The divider is draggable and persists per workspace;
 * the inspector never opens or closes, and there is no mode.
 */
const DEFAULT_SPLIT = 0.6;

/** The narrowest either pane may be dragged to, as a share of the panel. */
const MIN_SPLIT = 0.25;
const MAX_SPLIT = 0.85;

/**
 * Selection is written to a Memento on the host side. Holding an arrow key
 * would otherwise post one message and perform one workspace-state write per
 * repeat; the reader only ever cares where the key travel stopped.
 */
const SELECT_COMMIT_MS = 150;

class App {
  private readonly root = document.getElementById('app') as HTMLDivElement;
  private readonly header = el('header', 'pane-header');
  private readonly title = el('span', 'pane-title');
  private readonly fileLabel = el('span', 'pane-file mono');
  /** Direction A's stack header: `4 services · 1 network · 2 warnings`. */
  private readonly headerSummary = el('span', 'pane-summary');
  private readonly progress = el('div', 'progress');
  private readonly banner = el('div', 'banner');
  private readonly bannerTitle = el('strong', 'banner-title');
  private readonly bannerDetail = el('pre', 'banner-detail mono');
  private readonly retry = document.createElement('button');
  /* ---- story 4.4: the controls that make a big stack usable ------------ */
  private readonly toolbar = el('div', 'toolbar');
  private readonly search = document.createElement('input');
  private readonly focusButton = document.createElement('button');
  /**
   * Auto-arrange — story 4.2's dragged positions, given a way back.
   *
   * Positions persist per workspace, so one drag makes the layout partly
   * manual forever and nothing on screen could re-run it. This discards the
   * stored positions for this file and draws the computed arrangement again.
   * It is the only control here that THROWS SOMETHING AWAY, which is why it
   * reports what it discarded rather than just redrawing.
   */
  private readonly arrangeButton = document.createElement('button');
  private readonly networkButton = document.createElement('button');
  private readonly profileButton = document.createElement('button');
  private readonly toolbarStatus = el('p', 'toolbar-status');
  /* ---- story 4.6: the profile control --------------------------------- */
  /**
   * The group holding one toggle per declared profile.
   *
   * Detached from the toolbar entirely when the project declares no profiles —
   * "absent rather than empty", because a filter with nothing to filter teaches
   * the reader nothing and takes a tab stop to skip past.
   */
  private readonly profileBar = el('div', 'toolbar-profiles');
  /** One button per declared profile, by name. Rebuilt from the core's answer. */
  private profileButtons = new Map<string, HTMLButtonElement>();
  /**
   * What the project declares — the core's answer, held so the notice can tell
   * an active profile that filters something from one that no longer exists.
   */
  private declaredProfiles = new Set<string>();
  /** The profiles currently switched on. Mirrors what the host stores. */
  private activeProfiles = new Set<string>();
  /**
   * The line that says the stack on screen is a filtered one.
   *
   * Its own element rather than the shared status line: search, focus and
   * collapse all speak through `toolbarStatus`, and this must not be overwritten
   * by the next thing the reader types. A filtered stack presenting itself as
   * the whole stack is the one thing this control may not do.
   */
  private readonly profileNote = el('p', 'toolbar-note');
  /**
   * Set between sending a profile toggle and the graph that answers it.
   *
   * It makes the next graph message KEEP the positions captured just before the
   * toggle, so a node that exists in both profile sets is drawn exactly where
   * it was rather than re-flowed into a band.
   */
  private keepPositions = false;
  private readonly legend = el('ul', 'legend');
  private readonly strip = el('div', 'strip');
  private readonly narrow = el('nav', 'narrow-list');
  private readonly list = el('ul', 'service-list');
  private readonly narrowNote = el('p', 'narrow-note');
  private readonly canvas = el('main', 'canvas');
  private readonly emptyState = el('p', 'empty');
  /** The sentence in the empty state, kept apart from the action beside it. */
  private readonly emptyText = el('span', 'empty-text');
  /**
   * EXPERIENCE.md's empty-stack state: one action, `Add service` — story 7.3.
   *
   * MOCKUP-TRACEABILITY.md's first spine-vs-code disagreement was here: the
   * code refused this button because authoring was a later phase and a button
   * that opens nothing is worse than no button. It opens something now.
   */
  private readonly emptyAdd = document.createElement('button');
  /** `+ add` and its composer — stories 7.3 and 7.4. */
  private readonly addForm: AddFormView;
  /**
   * The file the held image lookups are about — Epic 8.
   *
   * Not the same field as anything else here, because it answers a different
   * question: everything else tracks what is DRAWN, and this tracks what a set
   * of third-party answers was fetched FOR.
   */
  private imageFile = '';
  private readonly graph: GraphView;
  /** The split: graph left, inspector right, both always visible. */
  private readonly split = el('div', 'split');
  private readonly graphPane = el('div', 'pane pane-graph');
  private readonly divider = el('div', 'divider');
  private readonly inspector: InspectorView;
  /** The docked pending-diff strip. Hidden until something is staged. */
  private readonly pending: PendingView;
  /** The Dockerfile view. Replaces the split when a stage form is open. */
  private readonly stageForm: StageFormView;
  private splitRatio = DEFAULT_SPLIT;
  private dividerDrag: { pointerId: number } | null = null;

  private loadingTimer: number | undefined;
  private selectTimer: number | undefined;
  private hasGraph = false;
  private model: StackGraph = EMPTY_GRAPH;
  private selected: GraphNode | null = null;
  /** Dockerfile nodes whose file is not on disk, by config path (story 6.3). */
  private missing = new Set<string>();

  /* ---- story 4.4 state -------------------------------------------------
   *
   * All of it is view state and none of it is ever sent anywhere near a file.
   * `base` is the core's own graph and the positions keyed by config path; the
   * collapsed view is derived from it on demand and thrown away on expand,
   * which is what makes "expanding restores exactly the previous positions"
   * true by construction rather than by careful bookkeeping.
   */
  private baseGraph: StackGraph = EMPTY_GRAPH;
  private basePositions: Record<string, Point> = {};
  private severities: Record<string, SeverityCount> = {};
  private query = '';
  /** The node focus mode is centred on, and the blast radius the core returned. */
  private focusId: string | null = null;
  private focusRadius: Set<string> | null = null;
  /** What focus mode last said, kept so a search does not erase it. */
  private focusMessage = '';
  private collapseBy: CollapseBy | null = null;
  private collapsedView: CollapsedView | null = null;
  /** What the collapse control last said, shown when nothing else is speaking. */
  private collapseMessage = '';
  /** Set when the rules could not run. Cleared by a draw that diagnoses. */
  private diagnosticsNote = '';

  constructor() {
    this.graph = new GraphView({
      onSelect: (node) => this.onSelect(node),
      onMove: (positions) => this.onMove(positions),
      // Story 6.3: clicking the Dockerfile node opens its stage form. Only a
      // click or Enter — arrow-key traversal selects and opens nothing, or
      // holding a key would open a form per repeat.
      onActivate: (node) => {
        // A folded group opens where a Dockerfile node opens its form: the
        // same gesture, on the same key, because to the reader both are "open
        // the thing under the cursor".
        if (node.collapsed) {
          this.setCollapse(null);
          return;
        }
        if (node.kind === 'dockerfile') {
          this.send({ type: 'openDockerfile', id: node.id });
        }
      },
    });
    this.inspector = new InspectorView({ send: (msg) => this.send(msg) });
    this.pending = new PendingView({ send: (msg) => this.send(msg) });
    this.stageForm = new StageFormView({ send: (msg) => this.send(msg) });
    this.addForm = new AddFormView({ send: (msg) => this.send(msg) });

    this.title.textContent = 'Stack';
    this.header.append(this.title, this.fileLabel, this.headerSummary, this.progress);
    this.progress.setAttribute('role', 'progressbar');
    this.progress.setAttribute('aria-label', 'Resolving');
    this.progress.hidden = true;

    this.retry.className = 'button';
    this.retry.type = 'button';
    this.retry.textContent = 'Retry';
    this.retry.addEventListener('click', () => this.send({ type: 'retry' }));

    const bannerText = el('div', 'banner-text');
    bannerText.append(this.bannerTitle, this.bannerDetail);
    this.banner.append(bannerText, this.retry);
    this.banner.setAttribute('role', 'alert');
    this.banner.hidden = true;

    // The edge kinds are drawn as different lines, and a line is not
    // self-describing. The legend names the kinds actually present — a fixed
    // key listing ten relations a stack does not have is noise.
    this.legend.setAttribute('aria-label', 'Edge kinds in this stack');
    this.legend.hidden = true;

    this.canvas.setAttribute('aria-label', 'Stack graph');
    this.canvas.appendChild(this.graph.element);
    this.emptyAdd.type = 'button';
    this.emptyAdd.className = 'button';
    this.emptyAdd.textContent = 'Add service';
    this.emptyAdd.dataset.control = 'empty-add-service';
    this.emptyAdd.addEventListener('click', () => this.addForm.openFor('service'));
    this.emptyState.append(this.emptyText, this.emptyAdd);
    this.emptyState.hidden = true;
    this.canvas.appendChild(this.emptyState);

    this.strip.hidden = true;
    this.narrow.hidden = true;
    this.narrow.setAttribute('aria-label', 'Nodes in this stack');
    this.narrowNote.hidden = true;
    this.narrow.append(this.list, this.narrowNote);

    // The split IS the identity: two panes, both permanently visible, a
    // draggable divider between them. There is no mode, nothing opens and
    // nothing closes.
    this.setUpToolbar();

    this.graphPane.setAttribute('aria-label', 'Stack graph pane');
    this.graphPane.append(
      this.toolbar,
      this.addForm.element,
      this.profileNote,
      this.toolbarStatus,
      this.legend,
      this.strip,
      this.narrow,
      this.canvas,
    );
    this.setUpDivider();
    this.split.append(this.graphPane, this.divider, this.inspector.element);
    this.applySplit();
    this.stageForm.element.hidden = true;
    // DESIGN.md:184 — the strip is docked at the foot OF THE INSPECTOR, not of
    // the panel. It used to be appended to the root, where it spanned the graph
    // as well: a diff of one field, drawn under a canvas it has nothing to do
    // with, taking height from both panes. It moves with whichever pane is up,
    // because the Dockerfile view replaces the split entirely.
    this.inspector.footer.appendChild(this.pending.element);
    this.root.append(this.header, this.banner, this.split, this.stageForm.element);

    window.addEventListener('message', (e: MessageEvent<HostMessage>) => this.onHostMessage(e.data));
    new ResizeObserver(() => this.onResize()).observe(document.body);
    this.onResize();
    this.send({ type: 'ready' });
  }

  private send(msg: WebviewMessage): void {
    vscode.postMessage(msg);
  }

  private onHostMessage(msg: HostMessage): void {
    switch (msg.type) {
      case 'loading':
        this.fileLabel.textContent = short(msg.file);
        this.showLoading(true);
        // Epic 8. A lookup is keyed by config path, and two projects share
        // those freely — `services.web.image` exists in most compose files
        // ever written. An answer about the file being left must not decorate
        // the identically-named field of the file being opened, so the moment
        // the drawn file changes, everything Docker Hub said is forgotten.
        if (msg.file !== this.imageFile) {
          this.imageFile = msg.file;
          this.inspector.clearLookups();
          this.stageForm.clearLookups();
        }
        break;
      case 'graph':
        this.showLoading(false);
        this.showStack();
        // A graph arriving means diagnose was attempted for this draw; the host
        // re-posts diagnosticsUnavailable if it failed again.
        this.diagnosticsNote = '';
        // A drawn stack means the previous failure no longer applies. Leaving
        // the banner up reports a problem that is over, which is its own kind
        // of wrong answer.
        this.banner.hidden = true;
        this.emptyState.hidden = true;
        this.graph.element.classList.remove('is-hidden');
        this.fileLabel.textContent = short(msg.file);
        this.missing = new Set(msg.missing ?? []);
        this.baseGraph = msg.graph;
        // A graph answering a profile toggle keeps the positions captured just
        // before it was sent, so every node that survives the toggle stays
        // where it is. Any other graph is a fresh draw of a file and takes the
        // host's stored positions as they are.
        this.basePositions = {
          ...(this.keepPositions ? this.basePositions : {}),
          ...(msg.positions as Record<string, Point>),
        };
        this.keepPositions = false;
        this.severities = msg.severities as Record<string, SeverityCount>;
        this.hasGraph = true;
        // Before the draw, so `applyFilter` at the end of it never runs against
        // a blast radius measured on the graph this one replaces.
        this.retargetFocus();
        this.drawGraph(msg.fit, msg.selected);
        this.setStale(false);
        break;
      case 'split':
        this.splitRatio = clampSplit(msg.ratio);
        this.applySplit();
        break;
      case 'profiles':
        // Story 4.6. Both halves come from the host: the declared list is the
        // core's own answer, and the active set is what the core was actually
        // asked for.
        this.renderProfiles(msg.declared, msg.active);
        break;
      case 'inspection':
        this.inspector.render(msg.inspection);
        break;
      case 'inspectionFailed':
        // Named, never blank. An inspector that silently keeps the previous
        // service's fields is the confident wrong answer in its purest form.
        //
        // `filtered` is not a failure: story 4.6's active set has hidden the
        // thing that is selected, and saying "could not be inspected" about a
        // service that is perfectly readable would send the reader looking for
        // a fault instead of a button.
        this.inspector.showMessage(
          msg.id === null
            ? 'The stack properties could not be read.'
            : msg.reason === 'filtered'
              ? `${msg.id} is not in the active profile set.`
              : `${msg.id} could not be inspected.`,
          msg.detail,
        );
        break;
      case 'empty':
        this.showLoading(false);
        this.showStack();
        this.banner.hidden = true;
        this.model = EMPTY_GRAPH;
        this.baseGraph = EMPTY_GRAPH;
        // See `case 'failure'`: the toggle this was armed for is over, and it
        // must not survive into whatever is drawn next.
        this.keepPositions = false;
        this.basePositions = {};
        this.severities = {};
        this.collapsedView = null;
        this.graph.render(EMPTY_GRAPH, {}, true, null, {});
        this.graph.element.classList.add('is-hidden');
        this.hasGraph = false;
        this.fileLabel.textContent = short(msg.file);
        // Absence stated plainly, naming the file. No `Add service` button:
        // authoring is phase 5 and a button that opens nothing is worse than
        // no button.
        this.emptyText.textContent = `No services are declared in ${short(msg.file)}. `;
        this.emptyState.hidden = false;
        this.renderLegend();
        this.renderList();
        this.updateStrip();
        this.updateArrangeState();
        break;
      case 'pending':
        this.pending.render(
          msg.file,
          msg.count,
          msg.diff,
          msg.removed,
          msg.added,
          msg.saveLabel,
          // Story 9.3. The `.env` half of a staged move — present only for the
          // one staged thing in this product that writes a second file.
          msg.env,
        );
        break;
      case 'pendingCleared':
        this.pending.clear(msg.reason);
        break;
      case 'editRefused':
        // EXPERIENCE.md's refused-edit state: the field has already reverted,
        // because the pane is refilled from the core; this says what could not
        // be done and why. Nothing was written.
        this.showRefusal(msg.title, msg.detail);
        break;
      case 'dockerfile':
        this.showLoading(false);
        this.banner.hidden = true;
        this.fileLabel.textContent = short(msg.file);
        this.showStageForm();
        if (msg.file !== this.imageFile) {
          this.imageFile = msg.file;
          this.stageForm.clearLookups();
        }
        this.stageForm.render(msg.form, msg.from, new Set(msg.staged ?? []));
        break;
      case 'selection':
        // Story 4.3's second direction: the cursor moved in the YAML and the
        // graph follows it. Nothing is posted back — the host already knows,
        // and an echo would be two views chasing each other.
        this.followSelection(msg.id);
        break;
      case 'impact':
        // Story 4.4's focus mode. The blast radius is `topology.BlastRadius`'s
        // answer; this dims what is not in it and computes nothing.
        if (msg.id === this.focusId) {
          this.focusRadius = impactSet(msg.id, msg.dependents, msg.dependencies);
          this.focusMessage = focusStatus(
            this.nameOf(msg.id),
            msg.dependents.length,
            msg.dependencies.length,
          );
          this.applyFilter();
        }
        break;
      case 'impactFailed':
        // Nothing is dimmed. A focus mode that dimmed an empty set because a
        // request failed would read as "nothing depends on this".
        if (msg.id === this.focusId) {
          this.focusId = null;
          this.focusRadius = null;
          this.focusMessage = `The blast radius of ${this.nameOf(msg.id)} could not be established: ${msg.detail}`;
          this.applyFilter();
        }
        break;
      case 'diagnosticsUnavailable':
        // The pane is about to draw nodes with no badges and fields with no
        // pills. Without this line that reads as "nothing wrong here", which is
        // a claim nobody is in a position to make.
        this.diagnosticsNote = `Checks did not run: ${msg.detail}. No badge here means no answer, not no problem.`;
        this.applyFilter();
        break;
      case 'failure':
        this.showLoading(false);
        // The redraw a toggle armed `keepPositions` for is not coming. Left
        // standing, the flag applies positions captured from THIS node set to
        // whatever graph arrives next — which may be another file entirely, and
        // then boxes sit at coordinates nothing on screen ever put them at. It
        // reads as a layout bug and it does not reproduce.
        this.keepPositions = false;
        this.showFailure(msg.failure);
        break;
      case 'extract':
        // Story 9.3. Routed and nothing else: both diffs are the core's, and a
        // second answer to "what would this change" computed here would be the
        // one the reader approved and not the one that landed.
        this.inspector.setExtract({
          path: msg.path,
          staged: msg.staged,
          result: msg.result,
          refused: msg.refused,
        });
        break;
      case 'extractArg':
        // Story 9.4, and routed for story 9.3's reason: which scope the `ARG`
        // may legally go in is the core's answer, and a second one worked out
        // here would be the one the reader approved rather than the one that
        // landed.
        this.stageForm.setExtractArg({
          instruction: msg.instruction,
          staged: msg.staged,
          result: msg.result,
          refused: msg.refused,
        });
        break;
      case 'comments':
        // Story 9.1. Late, on its own, and only for a block the reader opened.
        // Routed and nothing else: which lines are the comment above a key is
        // the engine's answer, and a second one worked out here would be the
        // one that wrote bytes.
        this.inspector.setComments({
          path: msg.path,
          above: msg.above,
          trailing: msg.trailing,
          staged: msg.staged,
          unavailable: msg.unavailable,
        });
        break;
      case 'imageLookup':
        // Epic 8. It arrives on its own, after the pane it belongs to has been
        // drawn, and it may never arrive at all. Routing it to whichever view is
        // up is the whole of the webview's part in this: nothing here computes
        // anything about an image, and a pane that never receives one of these
        // is exactly the pane that shipped before this epic.
        if (msg.key.startsWith('stage:')) {
          this.stageForm.setLookup(msg.key, msg.lookup);
        } else {
          this.inspector.setLookup(msg.key, msg.lookup);
        }
        break;
      case 'imageSearch':
        // Broadcast to every popup that could have asked. Each one compares the
        // token against the request IT is still waiting for and drops anything
        // else, so an answer cannot land in a field that did not ask for it.
        this.inspector.receiveSearch(msg.token, msg.answer);
        this.addForm.receiveSearch(msg.token, msg.answer);
        break;
      case 'clearFailure':
        this.banner.hidden = true;
        this.setStale(false);
        break;
    }
  }

  /**
   * A named failure and a retry, every time. The last good graph stays on
   * screen, dimmed — losing the picture mid-edit is worse than a stale one.
   */
  private showFailure(failure: Failure): void {
    this.bannerTitle.textContent = failure.title;
    this.bannerDetail.textContent = failure.detail;
    this.banner.dataset.kind = failure.kind;
    this.retry.hidden = false;
    this.banner.hidden = false;
    this.setStale(this.hasGraph);
  }

  /**
   * A refused edit — story 6.1's last acceptance criterion.
   *
   * The same banner, without the retry: `Retry` respawns the core, and a
   * flow-style mapping is not a dead process. Offering it would invite the
   * reader to restart a healthy core over an edit that will be refused again
   * for the same good reason. The canvas is not dimmed either — nothing about
   * the drawn stack has become untrustworthy.
   */
  private showRefusal(title: string, detail: string): void {
    this.bannerTitle.textContent = title;
    this.bannerDetail.textContent = detail;
    this.banner.dataset.kind = 'refused';
    this.retry.hidden = true;
    this.banner.hidden = false;
  }

  /**
   * The stack view: the split, and no stage form.
   *
   * Switching is a swap rather than a mode with state: the two views are
   * mutually exclusive and the host decides which is up, so nothing here has to
   * remember what it was showing.
   */
  private showStack(): void {
    this.split.hidden = false;
    this.stageForm.element.hidden = true;
    // The strip follows the surface it reports on.
    this.inspector.footer.appendChild(this.pending.element);
  }

  /** The Dockerfile view — stories 6.2 and 6.3. */
  private showStageForm(): void {
    this.split.hidden = true;
    this.stageForm.element.hidden = false;
    this.stageForm.footer.appendChild(this.pending.element);
  }

  private setStale(stale: boolean): void {
    this.canvas.classList.toggle('is-stale', stale);
  }

  private showLoading(loading: boolean): void {
    if (this.loadingTimer !== undefined) {
      window.clearTimeout(this.loadingTimer);
      this.loadingTimer = undefined;
    }
    if (!loading) {
      this.progress.hidden = true;
      return;
    }
    this.loadingTimer = window.setTimeout(() => {
      this.progress.hidden = false;
    }, LOADING_DELAY_MS);
  }

  private onSelect(node: GraphNode | null): void {
    this.selected = node;
    this.commitSelection(node ? node.id : null);
    this.updateStrip();
    this.markSelectedRow();
  }

  /**
   * Tells the host which node is selected, once the selection settles.
   *
   * Each of these costs a postMessage and a workspace-state write on the other
   * side, and arrow-key navigation produces one per repeat.
   */
  private commitSelection(id: string | null): void {
    if (this.selectTimer !== undefined) {
      window.clearTimeout(this.selectTimer);
    }
    this.selectTimer = window.setTimeout(() => {
      this.selectTimer = undefined;
      this.send({ type: 'select', id });
    }, SELECT_COMMIT_MS);
  }

  /** One row per edge kind the drawn stack actually declares. */
  private renderLegend(): void {
    this.legend.textContent = '';
    const kinds = legendEntries(this.model.edges);
    for (const kind of kinds) {
      const item = document.createElement('li');
      item.className = `legend-item legend-${kind}`;
      const swatch = document.createElement('span');
      swatch.className = 'legend-swatch';
      swatch.setAttribute('aria-hidden', 'true');
      const label = document.createElement('span');
      label.className = 'legend-label mono';
      label.textContent = kind;
      item.append(swatch, label);
      this.legend.appendChild(item);
    }
    this.legend.hidden = kinds.length === 0;
  }

  /**
   * The narrow-panel fallback: one button per node, every kind included.
   *
   * Rebuilt on every draw rather than diffed — the node set is a list of rows
   * and this is not the place to grow a reconciler.
   */
  private renderList(): void {
    this.list.textContent = '';
    for (const node of this.model.nodes) {
      const item = document.createElement('li');
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'service-row';
      button.dataset.id = node.id;
      button.dataset.kind = node.kind;
      // One accessible name per row: the node's name, then what the file says
      // about it — the same second line the node carries.
      const name = el('span', 'service-name mono');
      name.textContent = node.name;
      const detail = el('span', 'service-detail mono');
      const missing = this.missing.has(node.id) ? ' · missing — this file is not on disk' : '';
      detail.textContent = `${kindWord(node.kind)} · ${describeNode(node)}${missing}`;
      if (missing !== '') {
        button.classList.add('is-missing');
      }
      button.append(name, detail);
      button.addEventListener('click', () => this.selectById(node.id));
      item.appendChild(button);
      this.list.appendChild(item);
    }
    this.narrowNote.textContent = this.hasGraph ? '' : 'No services are declared in this file.';
    this.narrowNote.hidden = this.hasGraph;
    this.markSelectedRow();
  }

  private selectById(id: string): void {
    const node = this.model.nodes.find((n) => n.id === id) ?? null;
    this.selected = node;
    // The canvas and the list are one selection, not two: widening the panel
    // must not reveal a graph that disagrees with the row the reader pressed.
    this.graph.selectById(id);
    this.commitSelection(id);
    this.updateStrip();
    this.markSelectedRow();
  }

  private markSelectedRow(): void {
    for (const row of Array.from(this.list.querySelectorAll('.service-row'))) {
      const selected = row instanceof HTMLElement && row.dataset.id === this.selected?.id;
      row.classList.toggle('is-selected', selected);
      row.setAttribute('aria-current', selected ? 'true' : 'false');
    }
  }

  private updateStrip(): void {
    if (this.selected) {
      this.strip.textContent = `${this.selected.name} — ${kindWord(this.selected.kind)} · ${describeNode(this.selected)}`;
    } else {
      this.strip.textContent = this.hasGraph ? 'No node selected' : 'No services';
    }
  }

  /**
   * Panel width, not viewport width, is the responsive constraint. Below
   * 600px the canvas is replaced by a strip naming the selection and a list of
   * nodes; the page never scrolls sideways.
   */
  private onResize(): void {
    const narrow = document.body.clientWidth < NARROW_PX;
    this.root.classList.toggle('is-narrow', narrow);
    this.strip.hidden = !narrow;
    this.narrow.hidden = !narrow;
    // Below 600px the canvas gives up and the inspector takes the panel; the
    // divider goes with it, because there is nothing left to drag.
    this.divider.hidden = narrow;
    this.applySplit();
    this.updateStrip();
  }

  /* ---- story 4.4: search, focus and collapse --------------------------- */

  /**
   * The controls, all of them real focusable elements.
   *
   * A search field, a focus toggle and two collapse toggles, in a toolbar with
   * a status line beside it. Nothing here is a hover affordance and nothing is
   * an icon on its own: every control is reachable with Tab and says what it
   * does, because a capability that needs a pointer is a capability half the
   * readers of this product do not have (N6).
   */
  private setUpToolbar(): void {
    this.toolbar.setAttribute('role', 'toolbar');
    this.toolbar.setAttribute('aria-label', 'Graph controls');

    this.search.type = 'search';
    this.search.className = 'toolbar-search mono';
    this.search.placeholder = 'Search';
    this.search.setAttribute('aria-label', 'Search the nodes in this stack');
    this.search.addEventListener('input', () => this.onSearch(this.search.value));
    this.search.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && this.search.value !== '') {
        e.preventDefault();
        this.search.value = '';
        this.onSearch('');
      }
    });

    this.focusButton.type = 'button';
    this.focusButton.className = 'toolbar-button';
    this.focusButton.textContent = 'Focus';
    this.focusButton.setAttribute('aria-pressed', 'false');
    this.focusButton.setAttribute(
      'aria-label',
      'Focus on the selected node’s blast radius — what it depends on, and what depends on it',
    );
    this.focusButton.addEventListener('click', () => this.toggleFocus());

    // The way back from a dragged layout. Same idiom as every control beside
    // it: a real button, an accessible name, and no `tabindex` of its own.
    //
    // It is never `disabled`. A disabled button is not focusable, so a
    // keyboard reader tabbing the toolbar of a stack nobody has dragged simply
    // would not meet the control and would never learn it exists;
    // `aria-disabled` keeps it in the tab order, announces it as unavailable,
    // and the press it still receives answers in words. Nothing is posted in
    // that state — there is nothing stored to clear, and a message would cost
    // a workspace-state write per press for no change.
    this.arrangeButton.type = 'button';
    this.arrangeButton.className = 'toolbar-button';
    this.arrangeButton.textContent = 'Auto-arrange';
    this.arrangeButton.dataset.control = 'arrange';
    this.arrangeButton.setAttribute(
      'aria-label',
      'Auto-arrange — re-run the layout and discard the positions you dragged',
    );
    this.arrangeButton.addEventListener('click', () => this.autoArrange());

    // Written out rather than looped, so each control's name is attached to
    // the field a reader — and the accessibility scan — can find it by.
    this.networkButton.type = 'button';
    this.networkButton.className = 'toolbar-button';
    this.networkButton.textContent = 'By network';
    this.networkButton.setAttribute('aria-pressed', 'false');
    this.networkButton.setAttribute('aria-label', 'Collapse the graph by network');
    this.networkButton.addEventListener('click', () => this.onCollapsePressed('network'));

    this.profileButton.type = 'button';
    this.profileButton.className = 'toolbar-button';
    this.profileButton.textContent = 'By profile';
    this.profileButton.setAttribute('aria-pressed', 'false');
    this.profileButton.setAttribute('aria-label', 'Collapse the graph by profile');
    this.profileButton.addEventListener('click', () => this.onCollapsePressed('profile'));

    const collapseLabel = el('span', 'toolbar-label');
    collapseLabel.textContent = 'Collapse';
    this.toolbar.append(
      this.search,
      this.focusButton,
      this.arrangeButton,
      collapseLabel,
      this.networkButton,
      this.profileButton,
    );
    this.updateArrangeState();

    // Story 4.6's group. It is appended only when the project declares a
    // profile, so a stack with none carries no empty control and no stray tab
    // stop.
    this.profileBar.setAttribute('role', 'group');
    this.profileBar.setAttribute('aria-label', 'Profiles declared by this stack');

    // A live region: the reader who cannot see the dimming is told how many
    // nodes matched, or that nothing did. `polite`, because it must not
    // interrupt what a screen reader is already reading out.
    this.toolbarStatus.setAttribute('role', 'status');
    this.toolbarStatus.setAttribute('aria-live', 'polite');

    this.profileNote.setAttribute('role', 'status');
    this.profileNote.setAttribute('aria-live', 'polite');
    // NOT hidden, ever. A live region has to be in the accessibility tree
    // BEFORE the text lands: a screen reader subscribes to the region when it
    // sees it, and a region that appears already carrying its sentence is
    // routinely announced late or not at all. This is the one sentence that
    // cannot afford that — it is what tells a reader who cannot see the canvas
    // that the canvas is not the whole stack. It stays present and empty, and
    // `.toolbar-note:empty` in the stylesheet gives it no height.
  }

  /* ---- story 4.6: which profiles are active ---------------------------- */

  /**
   * Builds the profile control from the CORE's declared-profile list.
   *
   * Every name here came from `stack/schema` (`internal/schema`'s
   * `declaredProfiles`) by way of the host. None of it is derived from the
   * nodes on the canvas, and it could not be: a service the active set filters
   * out is not a node at all, so a list read off the graph would be missing
   * exactly the profiles the reader needs in order to see it again.
   */
  private renderProfiles(declared: string[], active: string[]): void {
    this.activeProfiles = new Set(active);
    this.declaredProfiles = new Set(declared);
    this.profileButtons = new Map();
    this.profileBar.textContent = '';
    if (declared.length === 0) {
      // Absent, not empty.
      this.profileBar.remove();
      this.updateProfileNote();
      return;
    }
    const label = el('span', 'toolbar-label');
    label.textContent = 'Profiles';
    this.profileBar.appendChild(label);
    for (const name of declared) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'toolbar-button';
      button.dataset.profile = name;
      button.textContent = name;
      // The same idiom as Focus and the two collapse buttons: a real button,
      // an accessible name, and `aria-pressed` carrying the state — never
      // colour alone (N6, R2.5).
      //
      // The name is a NOUN, not an instruction. It used to read "Activate the
      // debug profile" in both states, so a screen reader announced "Activate
      // the debug profile, pressed" — a name telling the reader to do the thing
      // they have already done. The WAI-ARIA toggle-button pattern is that the
      // name stays constant and `aria-pressed` is what moves; a name that
      // changes with the state is announced as a different control.
      button.setAttribute('aria-label', `${name} profile`);
      button.setAttribute('aria-pressed', String(this.activeProfiles.has(name)));
      button.addEventListener('click', () => this.toggleProfile(name));
      this.profileBar.appendChild(button);
      this.profileButtons.set(name, button);
    }
    this.toolbar.appendChild(this.profileBar);
    this.updateProfileNote();
  }

  /**
   * Turns one profile on or off and asks the host for the stack under the new
   * set.
   *
   * Nothing is filtered here. The webview drops no node of its own (AD-16): it
   * posts the set and draws whatever the core answers, so the graph on screen is
   * always something `internal/topology` actually derived.
   */
  private toggleProfile(name: string): void {
    const next = new Set(this.activeProfiles);
    if (!next.delete(name)) {
      next.add(name);
    }
    this.activeProfiles = next;
    for (const [profile, button] of this.profileButtons) {
      button.setAttribute('aria-pressed', String(next.has(profile)));
    }
    this.updateProfileNote();
    // Where every box is RIGHT NOW, folded into the position map before the
    // node set changes. A node that exists in both sets is then drawn from a
    // saved position rather than laid out afresh — the same promise search and
    // collapse make: a change to what you are looking at never moves the boxes.
    this.capturePositions();
    this.keepPositions = true;
    this.send({ type: 'positions', positions: this.basePositions });
    this.send({ type: 'setProfiles', profiles: [...next].sort() });
  }

  /**
   * The line that says, in words, that this is not the whole stack.
   *
   * Computed from the active profiles the project still DECLARES, not from the
   * stored set alone. A set naming profiles that have since been deleted from
   * the file filters nothing — the core filters by declared membership — and
   * the toolbar has by then detached the control, so announcing a filtered
   * stack would be false and would leave the reader nothing to press. The
   * stored set is kept as it is: if the profile comes back, so does the filter
   * the reader chose.
   */
  private updateProfileNote(): void {
    const filtering = [...this.activeProfiles].filter((p) => this.declaredProfiles.has(p)).sort();
    // Emptied rather than hidden: see `setUpToolbar`.
    this.profileNote.textContent = profileNotice(filtering);
  }

  /**
   * Draws whatever view is currently selected, from the core's own graph.
   *
   * The collapsed graph is derived here and nowhere else, and the base graph is
   * never modified, so expanding is simply drawing the base again with the
   * positions that were already held.
   */
  private drawGraph(fit: boolean, selected?: string | null): void {
    const view = this.collapseBy === null ? null : collapse(this.baseGraph, this.collapseBy);
    this.collapsedView = view;
    this.model = view === null ? this.baseGraph : view.graph;
    const positions =
      view === null ? this.basePositions : collapsedPositions(view, this.basePositions);
    // A selection whose node has just folded moves to the group that swallowed
    // it. Dropping it instead would silently deselect whatever the reader was
    // looking at, and refill the inspector with the stack.
    const shown =
      selected === undefined || selected === null || view === null
        ? selected
        : (view.memberOf[selected] ?? selected);
    this.graph.render(
      this.model,
      positions,
      fit,
      shown,
      this.severities,
      [...this.missing],
    );
    this.renderLegend();
    this.renderList();
    this.updateStrip();
    this.headerSummary.textContent = stackSummary(
      this.baseGraph.nodes,
      this.severities,
      this.diagnosticsNote === '',
    );
    // A draw is where the stored positions arrive, so it is where the control
    // that discards them learns whether it has anything to discard.
    this.updateArrangeState();
    this.applyFilter();
  }

  /** The reader typed. Nothing is re-laid-out; only classes change. */
  private onSearch(value: string): void {
    this.query = value;
    this.applyFilter();
  }

  /**
   * Turns focus mode on for the selected node, or off.
   *
   * On asks the core; the answer arrives as an `impact` message. Nothing is
   * dimmed until it does, because dimming before the answer would show a blast
   * radius of one — the confident wrong answer this product exists to avoid.
   */
  private toggleFocus(): void {
    if (this.focusId !== null) {
      this.focusId = null;
      this.focusRadius = null;
      this.focusMessage = '';
      this.applyFilter();
      return;
    }
    const id = this.selected?.id ?? null;
    if (id === null) {
      this.focusMessage = 'Select a node first — focus mode shows one node’s blast radius.';
      this.applyFilter();
      return;
    }
    if (this.collapsedView?.members[id]) {
      this.focusMessage = 'That is a folded group. Expand it and select a service inside it.';
      this.applyFilter();
      return;
    }
    this.focusId = id;
    this.focusRadius = null;
    this.focusMessage = `Asking the core what ${this.nameOf(id)} affects…`;
    this.applyFilter();
    this.send({ type: 'impact', id });
  }

  /**
   * A new graph arrived under focus mode — story 4.6's third criterion, on
   * screen rather than on the wire.
   *
   * A blast radius belongs to ONE graph. `stack/impact` is answered for a
   * profile set (host `impact()`), and a profile toggle produces a different
   * node set, different edges and therefore a different answer. Nothing here
   * used to notice: `case 'graph':` never touched `focusId`, `focusRadius` or
   * `focusMessage`, so the redraw went on dimming by an id set computed under
   * the PREVIOUS set while the toolbar repeated a count from a graph that no
   * longer existed. AC 3 says the picture, the problems and the blast radius
   * are never computed for three different profile sets; it was met for the
   * three REQUESTS and broken for the three answers on screen.
   *
   * So the stale radius is dropped in every case, and then:
   *
   *   * the focused node survived — ask again. Focus mode stays on and nothing
   *     is dimmed until the new answer lands, which is the same rule
   *     `toggleFocus` already follows at switch-on: dimming before the answer
   *     would show a blast radius of one.
   *   * it did not — focus mode goes off and SAYS so, naming the node while the
   *     old model can still name it. A control that silently un-presses itself
   *     teaches the reader that the toolbar does not mean anything.
   *
   * This also covers a redraw the reader did not ask for — a saved override, a
   * file changed on disk — where the radius goes stale for exactly the same
   * reason and used to survive for exactly as long.
   */
  private retargetFocus(): void {
    const id = this.focusId;
    if (id === null) {
      return;
    }
    // `nameOf` reads the model still on screen, so the node can be named even
    // in the branch where the new graph has dropped it.
    const name = this.nameOf(id);
    this.focusRadius = null;
    if (!this.baseGraph.nodes.some((n) => n.id === id)) {
      this.focusId = null;
      this.focusMessage = `${name} is no longer in the stack, so focus mode is off.`;
      return;
    }
    this.focusMessage = `Asking the core what ${name} affects…`;
    this.send({ type: 'impact', id });
  }

  /* ---- auto-arrange ----------------------------------------------------- */

  /**
   * Throws the stored positions away and draws the computed layout again.
   *
   * Three things happen, in this order and no other:
   *
   *   1. the webview's own copy of the positions is emptied, because
   *      `drawGraph` lays out from it and a copy left standing would put every
   *      box straight back where it was;
   *   2. the HOST is told, with an empty position map. `host/panel.ts` hands
   *      that to `ViewStateStore.setPositions`, which is a Memento write and
   *      NOT a file write — `panelbehaviour.test.ts` ("never writes without
   *      one") already pins `positions` to the non-writing path, and this
   *      control deliberately reuses that message rather than inventing a
   *      second one that would need its own proof of the same property;
   *   3. the graph is drawn with `fit: true`, because an arrangement the
   *      reader did not choose is not one they have a viewport for. `fit` also
   *      clears `userAdjusted` inside the canvas, so the next pane resize fits
   *      again — which is the state a stack nobody has touched is in.
   *
   * The selection survives: re-arranging is a statement about where boxes are,
   * not about which one the reader is reading.
   */
  private autoArrange(): void {
    // A leftover sentence from a focus mode that is already OFF outranks
    // everything in `applyFilter`, so without this the reader presses the
    // button and reads "api is no longer in the stack" — a true sentence about
    // something else entirely, and no report of what just happened. A standing
    // sentence about a mode that is on is left alone; that one is still true.
    if (this.focusId === null) {
      this.focusMessage = '';
    }
    // `basePositions` is exactly the set the host has STORED: the map a graph
    // message carried, plus whatever `onMove` has folded in since. It is
    // deliberately not `capturePositions()`, which would sweep in the computed
    // position of every node on the canvas — the count would then be the size
    // of the stack rather than the number of boxes the reader moved, and the
    // sentence would claim to have discarded work nobody did.
    const discarded = Object.keys(this.basePositions).length;
    if (discarded === 0) {
      // Nothing stored. Say so — and post nothing: the store is already empty
      // and a redraw would move nothing, so a silent no-op would read as a
      // dead button.
      this.collapseMessage = arrangeStatus(0);
      this.applyFilter();
      return;
    }
    this.basePositions = {};
    this.send({ type: 'positions', positions: {} });
    this.drawGraph(true, this.selected?.id ?? null);
    this.collapseMessage = arrangeStatus(discarded);
    this.updateArrangeState();
    this.applyFilter();
  }

  /**
   * Whether there is anything to re-arrange, on the control itself.
   *
   * `aria-disabled` rather than `disabled`: see the constructor. The class is
   * what the stylesheet dims; the attribute is what a screen reader reads.
   */
  private updateArrangeState(): void {
    const stored = Object.keys(this.basePositions).length > 0;
    this.arrangeButton.setAttribute('aria-disabled', String(!stored));
    this.arrangeButton.classList.toggle('is-unavailable', !stored);
  }

  /** A collapse button: pressing the one already pressed unfolds the graph. */
  private onCollapsePressed(by: CollapseBy): void {
    this.setCollapse(this.collapseBy === by ? null : by);
  }

  /** Folds the graph, or unfolds it. `null` restores exactly what was there. */
  private setCollapse(by: CollapseBy | null): void {
    // Pick up any dragging done since the last draw before the node set
    // changes, so an expand puts every box back where the reader left it.
    this.capturePositions();
    this.collapseBy = by;
    // `fit: false` — folding must not throw the reader's viewport away either.
    this.drawGraph(false, this.selected?.id ?? null);
    if (by === null || this.collapsedView === null) {
      this.collapseMessage = this.hasGraph ? 'Expanded. Every node is back where it was.' : '';
    } else {
      this.collapseMessage = collapseStatus(by, this.collapsedView);
    }
    this.networkButton.setAttribute('aria-pressed', String(by === 'network'));
    this.profileButton.setAttribute('aria-pressed', String(by === 'profile'));
    this.applyFilter();
  }

  /**
   * Applies search and focus to the drawn graph. No layout, ever.
   *
   * A live query wins over focus mode: the reader is typing, and what they are
   * typing is the more recent statement of what they are looking for. Focus
   * mode stays on underneath and comes back when the field is cleared.
   */
  private applyFilter(): void {
    const nodes = this.model.nodes;
    let ids: Set<string> | null = null;
    let sentence = '';
    if (this.query.trim() !== '') {
      const matched = searchMatches(nodes, this.query);
      ids = emphasis(matched);
      sentence = searchStatus(this.query, matched.size, nodes.length);
    } else if (this.focusRadius !== null) {
      ids = emphasis(this.focusRadius);
      sentence = this.focusMessage;
    } else {
      sentence = this.focusMessage || this.collapseMessage || this.diagnosticsNote;
    }
    this.focusButton.setAttribute('aria-pressed', String(this.focusId !== null));
    this.graph.emphasise(ids);
    this.markFilteredRows(ids);
    this.toolbarStatus.textContent = sentence;
    this.toolbarStatus.hidden = sentence === '';
  }

  /** The same emphasis on the narrow-panel list, which has no canvas. */
  private markFilteredRows(ids: Set<string> | null): void {
    for (const row of Array.from(this.list.querySelectorAll('.service-row'))) {
      if (!(row instanceof HTMLElement)) {
        continue;
      }
      const match = ids !== null && ids.has(row.dataset.id ?? '');
      row.classList.toggle('is-match', match);
      row.classList.toggle('is-dimmed', ids !== null && !match);
    }
  }

  /** A node the reader dragged. View state, and never a group id. */
  private onMove(positions: Record<string, Point>): void {
    this.basePositions = { ...this.basePositions, ...persistablePositions(positions) };
    this.send({ type: 'positions', positions: this.basePositions });
    // The first drag is what makes auto-arrange mean something.
    this.updateArrangeState();
  }

  /** Folds the current on-screen positions of real nodes back into the base. */
  private capturePositions(): void {
    this.basePositions = {
      ...this.basePositions,
      ...persistablePositions(this.graph.currentPositions()),
    };
  }

  /** The selection followed the cursor in the YAML — story 4.3. */
  private followSelection(id: string | null): void {
    this.graph.selectById(id);
    this.selected = id === null ? null : (this.model.nodes.find((n) => n.id === id) ?? null);
    this.updateStrip();
    this.markSelectedRow();
  }

  /** A node's display name, or its config path if it is not on screen. */
  private nameOf(id: string): string {
    return this.model.nodes.find((n) => n.id === id)?.name ?? id;
  }

  /* ---- the divider ---------------------------------------------------- */

  /**
   * Where the divider sits, as a CSS custom property both panes read.
   *
   * Flex-basis on a percentage rather than a pixel width, so a resized panel
   * keeps the reader's proportion instead of squeezing one pane to nothing.
   */
  private applySplit(): void {
    const narrow = this.root.classList.contains('is-narrow');
    const ratio = narrow ? 1 : this.splitRatio;
    this.graphPane.style.flexBasis = `${(ratio * 100).toFixed(2)}%`;
    this.inspector.element.style.flexBasis = `${((1 - ratio) * 100).toFixed(2)}%`;
    this.divider.setAttribute('aria-valuenow', String(Math.round(ratio * 100)));
  }

  /**
   * The divider is a real control: a separator role, a tab stop, and arrow
   * keys that move it. A divider reachable only by mouse fails N6 outright,
   * and it is the one control that decides how much of the wedge you can see.
   */
  private setUpDivider(): void {
    this.divider.setAttribute('role', 'separator');
    this.divider.setAttribute('aria-orientation', 'vertical');
    this.divider.setAttribute('aria-label', 'Resize the graph and inspector panes');
    this.divider.setAttribute('aria-valuemin', String(Math.round(MIN_SPLIT * 100)));
    this.divider.setAttribute('aria-valuemax', String(Math.round(MAX_SPLIT * 100)));
    this.divider.tabIndex = 0;

    this.divider.addEventListener('pointerdown', (e) => {
      this.dividerDrag = { pointerId: e.pointerId };
      this.divider.setPointerCapture(e.pointerId);
      this.divider.classList.add('is-dragging');
      e.preventDefault();
    });
    this.divider.addEventListener('pointermove', (e) => {
      if (!this.dividerDrag || this.dividerDrag.pointerId !== e.pointerId) {
        return;
      }
      const width = this.split.clientWidth;
      if (width <= 0) {
        return;
      }
      this.splitRatio = clampSplit((e.clientX - this.split.getBoundingClientRect().left) / width);
      this.applySplit();
    });
    const end = (e: PointerEvent): void => {
      if (!this.dividerDrag || this.dividerDrag.pointerId !== e.pointerId) {
        return;
      }
      this.dividerDrag = null;
      this.divider.classList.remove('is-dragging');
      this.divider.releasePointerCapture?.(e.pointerId);
      // Persisted per workspace, and never anywhere near the file: where the
      // reader put a divider is view state (R2.6, AD-19).
      this.send({ type: 'split', ratio: this.splitRatio });
    };
    this.divider.addEventListener('pointerup', end);
    this.divider.addEventListener('pointercancel', end);

    this.divider.addEventListener('keydown', (e) => {
      const step = e.shiftKey ? 0.1 : 0.02;
      let moved = false;
      if (e.key === 'ArrowLeft') {
        this.splitRatio = clampSplit(this.splitRatio - step);
        moved = true;
      } else if (e.key === 'ArrowRight') {
        this.splitRatio = clampSplit(this.splitRatio + step);
        moved = true;
      } else if (e.key === 'Home') {
        this.splitRatio = MIN_SPLIT;
        moved = true;
      } else if (e.key === 'End') {
        this.splitRatio = MAX_SPLIT;
        moved = true;
      }
      if (moved) {
        e.preventDefault();
        this.applySplit();
        this.send({ type: 'split', ratio: this.splitRatio });
      }
    });
  }
}

/**
 * Keeps a split usable.
 *
 * A ratio at either extreme is a pane the reader cannot drag back, because
 * there is nothing left to grab — and DESIGN.md says both panes are
 * permanently visible. Mirrors host/viewstate.ts's clamp; the two agree
 * because a stored value and a dragged one must land in the same range.
 */
function clampSplit(ratio: number): number {
  if (!Number.isFinite(ratio)) {
    return DEFAULT_SPLIT;
  }
  return Math.min(MAX_SPLIT, Math.max(MIN_SPLIT, ratio));
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  return node;
}

/** The file, named the way a reader would name it: the last two segments. */
function short(file: string): string {
  const parts = file.replace(/\\/g, '/').split('/');
  return parts.slice(-2).join('/');
}

new App();
