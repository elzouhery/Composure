// The stack panel: a webview beside the editor, and the bridge between it and
// the core.
//
// Everything the webview receives has already been through Go. The panel asks
// `stack/topology` for a path, checks the answer is shaped like a graph and
// posts it; the webview turns it into pixels and posts back the two things it
// alone knows — where the reader dragged a node, and which node is selected.
//
// It used to ask `stack/resolve` and walk the resolved document itself to find
// the services. That projection is gone: it was a second graph model in a
// second language, it could only ever produce the node kinds it happened to
// know how to walk, and it had no edges at all.
//
// The panel never touches the filesystem and never writes to a compose file.
// It reads.

import { randomBytes } from 'node:crypto';
import * as vscode from 'vscode';
import { CODE_RESOLVE_FAILED, RpcError } from './core';
import { MalformedGraphError, readGraph } from './topology';
import { nodeAtCursor, sourceFiles } from './locate';
import {
  editablePaths,
  fieldsNeedingChildren,
  findingsFor,
  readReport,
  readSchema,
  severityCounts,
} from './inspect';
import { Problems } from './problems';
import {
  Staging,
  addedPath,
  addEntryKey,
  commentKey,
  addInstructionKey,
  addStageKey,
  describeEdit,
  instructionKey,
  missingDockerfileNodes,
  parentOf,
  fileName,
  pendingValues,
  removeEntryKey,
  saveLabel,
  stageKey,
  type StagedEdit,
} from './staging';
import {
  apply,
  classify,
  commentText,
  editable,
  EXTRACT_ALONE,
  expectOf,
  extractApply,
  extractArgApply,
  extractArgPreview,
  extractPreview,
  planAdd,
  preview,
  reasonOf,
  refusalDetail,
  STALE_MESSAGE,
} from './edit';
import {
  IMAGE_LOOKUP_TIMEOUT_MS,
  imageLookupKeys,
  isSayable,
  readLookup,
  readSearch,
  stageLookupKey,
} from './images';
import { toFailure, type CoreSession } from './session';
import { ViewStateStore, normaliseProfiles } from './viewstate';
import { COMMENT_POSITIONS, isEntryPath, isGroupId } from '../shared/protocol';
import type {
  AddKind,
  CommentsAt,
  CommentWhere,
  ExtractArgResult,
  ExtractResult,
  Availability,
  DockerfileForm,
  EditOp,
  EditResult,
  Finding,
  HostMessage,
  Failure,
  NodeKind,
  SchemaField,
  StackGraph,
  StackSchema,
  WebviewMessage,
} from '../shared/protocol';

/**
 * How long the cursor must settle before the graph selection follows it.
 *
 * A held arrow key produces one selection change per repeat, and each one would
 * otherwise cost a workspace-state write, a `stack/schema` request and a post.
 * The reader only cares where the travel stopped.
 */
const CURSOR_DEBOUNCE_MS = 120;

/**
 * How long after a redraw a second notification for the same change is ignored.
 *
 * Saving a file fires the document listener and the filesystem watcher both.
 * One change is one redraw.
 */
const REDRAW_DEDUPE_MS = 400;

/**
 * How many unwritten mappings one inspection will describe.
 *
 * Each is a `stack/schema` round trip. A reader cannot usefully have more than
 * a handful of keys open at once, and an unbounded fan-out would be one request
 * per unset mapping on every render.
 */
const MAX_EXPANSIONS = 8;

/**
 * How many images one pane will ask Docker Hub about.
 *
 * Docker Hub allows 180 requests a minute PER ADDRESS, shared by everyone
 * behind it (R6.8). A pane is one service and has one image; the bound exists
 * so that a pathological schema answer cannot turn one selection into forty
 * requests against a limit a colleague may need.
 */
const MAX_IMAGE_LOOKUPS = 4;

/** The same bound for a Dockerfile: a form with sixty stages asks about eight. */
const MAX_STAGE_LOOKUPS = 8;

export class StackPanel {
  /** One panel per window. A second view of the same stack is two truths. */
  private static current: StackPanel | undefined;

  private readonly disposables: vscode.Disposable[] = [];
  private readonly store: ViewStateStore;
  /** The problems panel is VS Code's; we publish into it and own nothing. */
  private readonly problems = new Problems();
  /**
   * Staged edits, per file. Intent only: the sole path from here to a
   * filesystem is the `save` message, which is sent by exactly one control.
   */
  private readonly staging = new Staging();
  /**
   * The Dockerfile currently shown as a stage form, and the compose file the
   * reader reached it through. Null when the stack view is up.
   *
   * `from` is what makes "back to the stack" possible with the selection
   * intact — story 6.3's second acceptance criterion.
   */
  private dockerfile: { path: string; from: string | null } | undefined;
  /**
   * The findings for the drawn file, kept so a selection change can fill the
   * inspector without re-running the rules. Re-fetched on every draw; never
   * amended, because a stale finding on a fixed field is a wrong answer.
   */
  private findings: Finding[] = [];
  /**
   * Why the last `stack/diagnose` produced nothing, or null when it ran.
   *
   * "The rules found nothing" and "the rules did not run" are different answers
   * and used to be the same empty array: a failed diagnose published `[]`, which
   * CLEARED VS Code's problems panel and told the reader their stack was clean.
   */
  private diagnoseError: string | null = null;
  /**
   * Unset keys the reader has opened for editing — story 5.2's last criterion.
   *
   * NOT staged. An opened key is a field with the cursor in it and contributes
   * nothing to the pending diff, because the reader has said which key they
   * want and not yet what it should say. Cleared whenever the selection or the
   * file changes, because it is a statement about the pane on screen.
   */
  private opened = new Set<string>();
  /**
   * What the last inspection said about the shape of each path: which exist,
   * which are mappings, and which the file declares with no value at all.
   *
   * The write path needs all three. `restart` opened but undeclared becomes an
   * `insert_key`; `restart` declared-null becomes a delete and an insert,
   * because the engine cannot splice a value into a range with no bytes; and an
   * unwritten mapping has to be inserted before anything can be inserted INTO
   * it.
   */
  private declared = new Set<string>();
  private nullValued = new Set<string>();
  /**
   * Where each rendered path is actually WRITTEN, as the core last said —
   * `stack/editable`, keyed by config path.
   *
   * `declared` above is not this question and cannot answer it. The core
   * reports `services.web.restart` as declared because the RESOLVED service
   * has one; the FILE has no such key, the value arrives through `<<:
   * *defaults`, and a `replace_scalar` aimed there lands on nothing. That was
   * the defect: the pane offered a field, the reader typed in it, and the write
   * path answered `path services.web.restart not found`.
   *
   * Held per pane and rebuilt on every inspection, never merged across files.
   * An availability computed from a file's bytes must not outlive them.
   */
  private availability = new Map<string, Availability>();
  /** The graph currently drawn, for naming a selection in the inspector. */
  /**
   * The move the reader has staged — story 9.3.
   *
   * NOT in `Staging`, and that is the design rather than an omission. Every
   * entry in that store is an `EditOp` against ONE file, previewed together and
   * written together by `stack/apply`; a move writes two files through a method
   * of its own, in an order chosen so that the only reachable partial state is
   * an inert `.env` line. Putting it in the same list would mean either
   * pretending it is one operation or teaching the store about two files, and
   * both end with `Save` writing something the strip did not show.
   *
   * It is exclusive for the same arithmetic: an extract computes its ranges
   * from the file on disk and every ordinary stage holds an `expect` against
   * those same bytes, so whichever went first would invalidate the other.
   */
  private extract: { file: string; path: string; name: string; result: ExtractResult } | undefined;

  /**
   * The build-argument move the reader has staged — story 9.4.
   *
   * A second field rather than a second kind in the one above, because the two
   * are held against DIFFERENT files: a compose move lives on `this.file` and
   * this one on the open Dockerfile, and `Staging` already holds a reader's
   * work per file for exactly that reason. One slot for both would drop a
   * staged compose move the moment a Dockerfile was opened and a literal moved,
   * silently, which is the one thing a staging store must never do.
   *
   * Exclusive against ordinary stages on the SAME file, and for the same
   * arithmetic as the compose half: the substitution's byte range and every
   * staged `expect` are computed from the same bytes on disk, so whichever was
   * written first would invalidate the other.
   */
  private extractArg:
    | { file: string; instruction: number; name: string; result: ExtractArgResult }
    | undefined;

  private graph: StackGraph | undefined;
  /**
   * The profile set `this.graph` was built for — story 4.6's third criterion.
   *
   * Not the same thing as the stored set. `profilesFor` answers "what has the
   * reader switched on", which can change at any await; this answers "what is
   * the picture on screen made of", and everything computed ABOUT that picture
   * — its findings, its severity badges, the blast radius drawn over it — has
   * to use this one or it describes a stack the reader is not looking at.
   */
  private drawnProfiles: string[] = [];
  /**
   * The profiles this project DECLARES, as the core last reported them.
   *
   * `undefined` until an inspection has answered once — which is not the same
   * thing as `[]`, and the difference decides whether the toolbar has a
   * control at all: `[]` tells the webview to detach it.
   *
   * Cached only so a failure can resync the control. The webview presses a
   * profile button optimistically, and the only thing that un-presses it is a
   * `profiles` post; before this field existed the declared list lived in
   * `inspect()`'s local scope, so a handler that threw left the button reading
   * "prod on" over a graph that was never filtered, with a banner beside it
   * saying something had failed. It is not read for anything else — the
   * authoritative answer is still the one `stack/schema` gives on each
   * inspection.
   */
  private declaredProfiles: string[] | undefined;
  /** The selection the inspector is showing, so a redraw can refill it. */
  private selection: string | null = null;
  /** True until this panel has drawn `file` once: auto-fit, then keep the view. */
  private needsFit = true;
  private drawing = false;
  /** A save that lands mid-resolve is redrawn after it, never dropped. */
  private queued = false;
  /** Set by dispose(). Nothing is posted to a webview that no longer exists. */
  private disposed = false;
  /**
   * Watchers on every file the drawn graph declares something in — story 4.3's
   * third criterion. A save through the editor is caught by
   * onDidSaveTextDocument; this is the other half, a file changed on disk by a
   * rebase, a generator or another window.
   */
  private watchers: vscode.FileSystemWatcher[] = [];
  /** The file set those watchers cover, so an unchanged set is not rebuilt. */
  private watching: string[] = [];
  /**
   * When the last redraw started. A save fires the document listener AND the
   * filesystem watcher; redrawing twice for one change is wasted work and a
   * visible flicker, so the second one inside this window is dropped.
   */
  private lastDrawAt = 0;
  /** Debounce for cursor movement. Holding an arrow key must not resolve a stack. */
  private cursorTimer: ReturnType<typeof setTimeout> | undefined;
  /**
   * Which pane the in-flight image lookups belong to — Epic 8.
   *
   * Bumped on every draw, every selection and every Dockerfile switch. A lookup
   * takes as long as Docker Hub takes, which is longer than a reader takes to
   * click something else, so an answer whose generation is stale is DROPPED. An
   * upgrade pill for `postgres` on the pane for `redis` is a confident wrong
   * answer of the purest kind, and it is the specific way an asynchronous
   * decoration goes wrong.
   */
  private imageGeneration = 0;

  /** Called when the reader closes the panel themselves, never on teardown. */
  static onUserClose: (() => void) | undefined;

  static show(context: vscode.ExtensionContext, session: CoreSession, file: string): StackPanel {
    if (StackPanel.current) {
      StackPanel.current.setFile(file);
      StackPanel.current.panel.reveal(vscode.ViewColumn.Beside, true);
      return StackPanel.current;
    }
    const panel = vscode.window.createWebviewPanel(
      'composure.stack',
      'Stack',
      { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true },
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(context.extensionUri, 'dist')],
      },
    );
    StackPanel.current = new StackPanel(panel, context, session, file);
    return StackPanel.current;
  }

  static active(): StackPanel | undefined {
    return StackPanel.current;
  }

  private constructor(
    private readonly panel: vscode.WebviewPanel,
    private readonly context: vscode.ExtensionContext,
    private readonly session: CoreSession,
    private file: string,
  ) {
    this.store = new ViewStateStore(context.workspaceState);
    this.panel.webview.html = this.html();

    this.disposables.push(
      // Rule 6, at the boundary rather than in any one handler. Every case in
      // `onMessage` awaits a workspace write, a core request or both, and all
      // eighteen of them arrive through this single line — so a `void` here
      // turns ANY of them failing into an unhandled promise rejection and not a
      // word on screen. The reader had already pressed the button: the control
      // says the profile is on, the graph beneath it is unfiltered, and nothing
      // said otherwise.
      this.panel.webview.onDidReceiveMessage((msg: WebviewMessage) => {
        void this.onMessage(msg).catch((err) => {
          this.post({ type: 'failure', failure: this.describe(err) });
          // The banner alone leaves a lying control behind. Every optimistic
          // press in the webview is a profile toggle, and a failed handler is
          // exactly when the picture and the buttons above it diverge.
          this.resyncProfiles();
        });
      }),
      // Story 4.3: the cursor moving in the YAML moves the graph selection.
      // The two views track one position, and this is the direction the panel
      // used not to have.
      vscode.window.onDidChangeTextEditorSelection((e) => this.onCursor(e)),
      // Re-resolve when the drawn file is saved.
      vscode.workspace.onDidSaveTextDocument((doc) => {
        const path = doc.uri.fsPath;
        if (path === this.dockerfile?.path) {
          // The Dockerfile the form is showing was edited as text. Same rule.
          this.discardStages(path, this.hasUnwritten(path));
          void this.showDockerfile(path, this.dockerfile.from);
          return;
        }
        // Any file the drawn stack was merged from, not only the entry file:
        // an override saved in another tab changes this picture exactly as the
        // entry file does, and redrawing only for one of them shows a stack
        // that no longer matches what is on disk.
        if (path !== this.file && !this.watching.includes(path)) {
          return;
        }
        this.onSourceChanged(path);
      }),
    );
    this.panel.onDidDispose(() => {
      // Distinguishable from extension teardown: deactivate() disposes the
      // session, not the panel.
      StackPanel.onUserClose?.();
      this.dispose();
    });
  }

  /** Points the panel at a different compose file. */
  setFile(file: string): void {
    if (file === this.file) {
      // Same stack — but the pane may be showing a Dockerfile reached FROM it,
      // and `this.file` never left the compose file while it did. Selecting the
      // compose file again is a request to see the stack, so the early return
      // has to leave the Dockerfile view before it takes. Without this, opening
      // a Dockerfile and then coming back showed the stage form indefinitely:
      // the one line below that clears it was unreachable for the commonest
      // way of asking.
      if (this.dockerfile) {
        this.dockerfile = undefined;
        void this.draw();
      }
      return;
    }
    // Findings and stages belong to the file that produced them. Carrying
    // either across would put one file's problems on another's fields.
    this.problems.clear();
    this.discardStages(this.file);
    this.findings = [];
    this.diagnoseError = null;
    this.opened.clear();
    this.declared.clear();
    this.nullValued.clear();
    this.availability.clear();
    // One file's profile names are not another's. Kept only to resync a
    // control after a failure, and resyncing the NEW file's toolbar from the
    // OLD file's declared list is the same class of lie the cache exists to
    // stop; the next inspection refills it.
    this.declaredProfiles = undefined;
    this.file = file;
    this.needsFit = true;
    // Opening a compose file leaves the Dockerfile view, or the panel would
    // keep drawing a stage form while claiming to show another stack.
    this.dockerfile = undefined;
    void this.draw();
  }

  get drawnFile(): string {
    return this.file;
  }

  /* ---- story 4.6: the active profile set ------------------------------- */

  /**
   * The profiles switched on for a file — R1.4's toggle, from view state.
   *
   * Read fresh from the store on every use rather than cached in a field. The
   * cost is a Memento read; what it buys is that there is exactly ONE answer to
   * "which profiles are active", so the graph, the findings and the blast
   * radius cannot be computed for three different sets. A cached copy is
   * precisely how they would drift.
   *
   * Empty is Compose's own default: only always-active services.
   */
  private profilesFor(file: string): string[] {
    return this.store.get(file).profiles;
  }

  /**
   * The reader turned a profile on or off.
   *
   * Stored as view state and then re-asked of the core — the filtering itself
   * happens in `internal/topology` and nowhere else (AD-16), so a toggle is
   * another call with another argument rather than something this panel or the
   * webview does to a graph it already has.
   *
   * The redraw carries the stored positions, so every node that exists in both
   * sets is drawn exactly where it was: the graph is not re-laid-out under a
   * reader who only changed what is in it.
   *
   * WHETHER IT CHANGED IS THE STORE'S ANSWER, not a comparison made here. The
   * store's writes are serialised; a `Memento.get` here is not, and
   * `onDidReceiveMessage` is fire-and-forget, so two toggles a turn apart both
   * ran this method against the SAME pre-toggle value. Pressing `debug` on and
   * off again quickly made the second press compare `[]` against `[]`, return,
   * and leave the panel filtered by a profile whose button was not pressed —
   * persisted, with nothing on screen saying so. Comparing inside the queue is
   * the only place the previous toggle is guaranteed to be visible.
   */
  /**
   * Puts the profile control back in step with the graph on screen.
   *
   * The webview presses a toggle OPTIMISTICALLY — `aria-pressed` moves before
   * the message is sent, so the control answers instantly rather than after a
   * round trip through the store and the core. That is the right trade for the
   * path that works and the wrong one for the path that throws: nothing else
   * ever un-presses the button, so a failed write left the reader looking at
   * "prod on" over a completely unfiltered stack. Rule 6 is not satisfied by a
   * banner while a control is still asserting something false.
   *
   * The set posted is `drawnProfiles` — what the picture was actually built
   * for — never the stored set, which may itself be the thing that failed to
   * change. Silent before the first inspection has answered: the webview has
   * no control to correct then, and posting `declared: []` would tell it to
   * detach one it may be about to be given.
   */
  private resyncProfiles(): void {
    if (this.declaredProfiles === undefined) {
      return;
    }
    this.post({
      type: 'profiles',
      file: this.file,
      declared: this.declaredProfiles,
      active: this.drawnProfiles,
    });
  }

  private async setProfiles(profiles: string[]): Promise<void> {
    const file = this.file;
    // Normalisation happens in the store, so the value compared is the value
    // stored and the value sent to the core — one shape, one place.
    if (!(await this.store.setProfiles(file, profiles))) {
      return; // nothing changed; a redraw would only make the canvas flicker
    }
    // `draw()` posts a `graph`, and the webview answers that by swapping back
    // to the stack. Keeping `this.dockerfile` would leave `editTarget()`
    // pointing at a file the reader is no longer looking at, and the next
    // staged edit would be held against it — the same line `backToStack` runs
    // for the same reason.
    this.dockerfile = undefined;
    await this.draw();
  }

  /* ---- story 4.3: the two views track one position -------------------- */

  /**
   * Moves the graph selection to whatever node the cursor is now inside.
   *
   * Debounced, because a held arrow key produces one of these per repeat and
   * each one costs a workspace-state write, a schema request and a post. The
   * reader only ever cares where the cursor stopped.
   *
   * Nothing is posted when the answer has not changed, so scrolling inside one
   * service is silent; and the webview does not echo the selection back, which
   * is what stops the two views chasing each other.
   */
  private onCursor(e: vscode.TextEditorSelectionChangeEvent): void {
    if (this.dockerfile || this.disposed) {
      return; // the stage form is up; there is no graph selection to move
    }
    const doc = e.textEditor.document;
    if (doc.uri.scheme !== 'file') {
      return;
    }
    const path = doc.uri.fsPath;
    const graph = this.graph;
    if (!graph || (path !== this.file && !this.watching.includes(path))) {
      return;
    }
    const line = e.selections[0]?.active.line;
    if (line === undefined) {
      return;
    }
    if (this.cursorTimer !== undefined) {
      clearTimeout(this.cursorTimer);
    }
    this.cursorTimer = setTimeout(() => {
      this.cursorTimer = undefined;
      void this.selectFromCursor(nodeAtCursor(graph.nodes, path, line + 1));
    }, CURSOR_DEBOUNCE_MS);
  }

  private async selectFromCursor(id: string | null): Promise<void> {
    if (this.disposed || id === this.selection) {
      return;
    }
    this.selection = id;
    this.post({ type: 'selection', id });
    await this.store.setSelected(this.file, id);
    await this.inspect(this.file, id);
  }

  /**
   * A file the drawn stack is made of changed — saved here, or written on disk
   * by something else entirely.
   *
   * Both routes land here so they cannot behave differently. AD-19: a stage is
   * held against a byte range in a document that has just moved. Discard it
   * rather than rebase — writing a stale range is how a fidelity engine damages
   * a file. The reader is told, because a stage that vanished silently is work
   * they think they still have.
   */
  private onSourceChanged(path: string): void {
    if (this.disposed) {
      return;
    }
    // A save fires the document listener and the watcher both. One change is
    // one redraw.
    if (Date.now() - this.lastDrawAt < REDRAW_DEDUPE_MS) {
      return;
    }
    this.discardStages(path, this.hasUnwritten(path));
    void this.draw();
  }

  /**
   * Points the filesystem watchers at the files the drawn graph came from.
   *
   * Rebuilt only when the set changes: disposing and recreating watchers on
   * every draw would churn file handles for a set that is the same three paths
   * every time.
   */
  private watchSources(files: string[]): void {
    if (files.length === this.watching.length && files.every((f, i) => f === this.watching[i])) {
      return;
    }
    for (const w of this.watchers) {
      w.dispose();
    }
    this.watchers = [];
    this.watching = files;
    for (const file of files) {
      const watcher = vscode.workspace.createFileSystemWatcher(file);
      // Change, create and delete alike: a file that vanishes changes the
      // resolved stack as much as one that is edited, and the redraw either
      // shows the new picture or names the failure.
      watcher.onDidChange(() => this.onSourceChanged(file));
      watcher.onDidCreate(() => this.onSourceChanged(file));
      watcher.onDidDelete(() => this.onSourceChanged(file));
      this.watchers.push(watcher);
    }
  }

  /* ---- story 4.4: focus mode ------------------------------------------ */

  /**
   * The blast radius of one node, from the core.
   *
   * `topology.BlastRadius` computes it; this asks and forwards. A closure
   * walked here over the edges the webview happens to hold would be a second
   * answer to "what breaks if this goes down", and the two would part company
   * the first time either changed.
   */
  private async impact(id: string): Promise<void> {
    try {
      const answer = await this.session.request<unknown>('stack/impact', {
        // Story 4.6: the SAME active set the drawn graph was built from. A
        // blast radius computed under a different profile set than the picture
        // it dims would name dependents that are not on the canvas — the
        // confident wrong answer, in the one place the reader asks "what
        // breaks if this goes down".
        //
        // The set the DRAWN graph was built for, not the stored one: a toggle
        // whose redraw is still in flight has already changed the store, and
        // the picture this dims is still the previous one.
        path: this.file,
        profiles: this.drawnProfiles,
        at: id,
      });
      const value = answer as { dependents?: unknown; dependencies?: unknown };
      const list = (v: unknown): string[] =>
        Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
      this.post({
        type: 'impact',
        id,
        dependents: list(value?.dependents),
        dependencies: list(value?.dependencies),
      });
    } catch (err) {
      // Named, and nothing is dimmed. A focus mode that dimmed an empty set
      // because a request failed would read as "nothing depends on this",
      // which is a confident wrong answer about the reader's stack.
      this.post({
        type: 'impactFailed',
        id,
        detail: err instanceof RpcError ? err.message : err instanceof Error ? err.message : String(err),
      });
    }
  }

  /** Resolves the drawn file and posts the result. Every outcome is a posted state. */
  async draw(): Promise<void> {
    if (this.drawing) {
      this.queued = true;
      return;
    }
    this.drawing = true;
    this.lastDrawAt = Date.now();
    // The file is captured here, not read again after the await. setFile() can
    // land mid-resolve, and posting this answer under the new filename would
    // label one file's nodes with another file's name.
    const file = this.file;
    this.post({ type: 'loading', file });
    try {
      // Story 4.6, R1.4: the set the reader switched on, from view state.
      // Empty is Compose's default and is what a project with no profile
      // control still sends. The filtering happens in `internal/topology` and
      // nowhere else (AD-16) — this asks a different question, it does not
      // post-process an answer.
      const profiles = this.profilesFor(file);
      const answer = await this.session.request<unknown>('stack/topology', {
        path: file,
        profiles,
      });
      if (file !== this.file) {
        return; // superseded; the redraw for the current file is already queued
      }
      const { graph, droppedEdges } = readGraph(answer);
      if (droppedEdges > 0) {
        // Not silent, and not fatal either: the graph still draws, and every
        // edge the core did emit is still true. An edge whose endpoint is not
        // a node has no geometry, so it cannot be drawn — but it can be said.
        console.warn(
          `composure: ${droppedEdges} edge(s) in ${file} name an endpoint that is not a node; ` +
            'the core should have reported those as dangling',
        );
      }
      // Findings before the draw, so the nodes carry their counts on first
      // paint rather than acquiring them a moment later (story 5.4). A
      // diagnose that fails does not stop the graph: the picture is still true
      // and losing it because a rule threw is the wrong trade.
      this.graph = graph;
      // The graph and the set it was built for move together. Everything later
      // in this draw, and everything asked ABOUT this picture until the next
      // one replaces it, uses this rather than reading the store again.
      this.drawnProfiles = profiles;
      // Every file this stack was merged from is watched, not just the entry
      // one — story 4.3's third criterion.
      this.watchSources(sourceFiles(graph.nodes));
      const diagnosis = await this.diagnose(file, profiles);
      this.findings = diagnosis.findings;
      this.diagnoseError = diagnosis.error;
      if (file !== this.file) {
        return;
      }
      if (diagnosis.error === null) {
        this.problems.publish(this.findings);
      } else {
        // Not an empty panel. The reader is told, in the place they go to find
        // out whether their stack is healthy, that we could not tell them.
        this.problems.unavailable(file, diagnosis.error);
        this.post({ type: 'diagnosticsUnavailable', file, detail: diagnosis.error });
      }

      if (graph.nodes.length === 0) {
        this.post({ type: 'clearFailure' });
        this.post({ type: 'empty', file });
        // The inspector never goes blank. With no services there is still a
        // stack: its files, its profiles, and everything it could declare.
        await this.inspect(file, null);
        return;
      }
      const stored = this.store.get(file);
      this.post({ type: 'clearFailure' });
      if (stored.split !== null) {
        this.post({ type: 'split', ratio: stored.split });
      }
      this.post({
        type: 'graph',
        file,
        graph,
        missing: missingDockerfileNodes(this.findings),
        positions: stored.positions,
        selected: stored.selected,
        fit: this.needsFit,
        severities: severityCounts(
          this.findings,
          graph.nodes.map((n) => n.id),
        ),
      });
      this.needsFit = false;
      this.selection = stored.selected;
      await this.inspect(file, stored.selected);
      // A redraw must not silently drop the strip: the stages are still held
      // and the reader still has unwritten work.
      await this.refreshPending();
    } catch (err) {
      if (file !== this.file) {
        return; // a failure about a file the panel is no longer showing
      }
      this.post({ type: 'failure', failure: this.describe(err) });
      // The other half of the optimistic-press problem, and the one the
      // boundary catch cannot see: a profile toggle whose STORE succeeded and
      // whose redraw then failed resolves cleanly here, so `onMessage` never
      // rejects. The set is persisted, the button is pressed, and the canvas
      // is still the previous stack. `drawnProfiles` is untouched by a failed
      // draw, so this puts the control back on the picture that is actually up.
      this.resyncProfiles();
    } finally {
      this.drawing = false;
      if (this.queued) {
        this.queued = false;
        void this.draw();
      }
    }
  }

  /**
   * The findings for a file, or a reason there are none.
   *
   * A diagnose that fails does not stop the graph: the picture is still true and
   * losing it because a rule threw is the wrong trade. But it must not be
   * reported as a clean stack either, and it was — the catch returned `[]`, the
   * caller published `[]`, and `DiagnosticCollection.clear()` emptied VS Code's
   * problems panel. The reader saw no problems and a `console.warn` nobody
   * reads, which is this engine's characteristic failure: not a crash, a
   * confident wrong answer.
   *
   * So the two outcomes are two different values, and every caller has to say
   * which one it got.
   */
  private async diagnose(
    file: string,
    profiles: string[],
  ): Promise<{ findings: Finding[]; error: string | null }> {
    try {
      const answer = await this.session.request<unknown>('stack/diagnose', {
        // The same active set the graph was built from (story 4.6). Diagnosing
        // a different profile set than the one on screen would put a finding
        // on a service the reader cannot see, and hide one on a service they
        // can.
        //
        // PASSED IN, never read again here. It used to be a second
        // `profilesFor(file)`, on the far side of the topology round trip: a
        // toggle landing in that gap gave one draw a graph built for set A and
        // findings for set B, published to the problems panel and posted as
        // the severity badges on set A's nodes.
        path: file,
        profiles,
      });
      return { findings: readReport(answer).findings, error: null };
    } catch (err) {
      const detail =
        err instanceof RpcError ? err.message : err instanceof Error ? err.message : String(err);
      console.warn(`composure: diagnostics unavailable for ${file}: ${detail}`);
      return { findings: [], error: detail };
    }
  }

  /**
   * Fills the inspector for a selection. `id` null is the stack itself.
   *
   * The pane is never left empty and never left stale: every path through this
   * posts either an inspection or a named reason there is none. An inspector
   * that silently keeps the previous service's fields after a failed fetch is
   * the confident wrong answer in its purest form.
   */
  private async inspect(file: string, id: string | null): Promise<void> {
    try {
      const answer = await this.session.request<unknown>('stack/schema', {
        path: file,
        at: id ?? '',
      });
      if (file !== this.file) {
        return;
      }
      const schema = readSchema(answer);
      // Story 4.6's first criterion: the profile control is built from the
      // CORE's declared-profile answer, which every `stack/schema` result
      // carries whatever path it was asked about
      // (`internal/schema/inspect.go`, `declaredProfiles`). It is taken from
      // here rather than from the graph on purpose — a service filtered OUT is
      // not a node, so a list derived from the canvas would be missing exactly
      // the profiles the reader needs in order to switch them back on.
      this.declaredProfiles = normaliseProfiles(schema.profiles);
      this.post({
        type: 'profiles',
        file,
        declared: this.declaredProfiles,
        // The set the graph on screen was built for. Posting the STORED set
        // would press a button for a filter the picture beneath it does not
        // have yet — the control and the canvas disagreeing is the whole class
        // of defect this story keeps closing.
        active: this.drawnProfiles,
      });
      this.recordShapes(schema);
      await this.expandOpenMappings(file, schema);
      await this.recordAvailability(file, schema);
      if (file !== this.file) {
        return;
      }
      if (this.diagnoseError !== null) {
        // Said again on every selection, because the pane is about to render a
        // service with no pills on it and that must not read as "no problems
        // here". The rules did not run for any of them.
        this.post({ type: 'diagnosticsUnavailable', file, detail: this.diagnoseError });
      }
      const node = id === null ? undefined : this.graph?.nodes.find((n) => n.id === id);
      // Story 4.6. A selection the active profile set filtered OUT is not a
      // node, and the fallbacks below would render the SERVICE's field list
      // under the STACK's name and kind — `services.api`'s ninety fields headed
      // `compose.yaml`, kind `stack`, with its finding pills gone because the
      // rules were re-run under the new set. That is the confident wrong answer
      // in the pane whose entire job is to say what one thing is. Rule 6: say
      // so instead.
      //
      // A synthetic collapse group (`group:…`) is excluded: it is not in the
      // node list either and never was, and it already has its own path through
      // the catch below.
      if (id !== null && node === undefined && this.graph !== undefined && !isGroupId(id)) {
        const active = this.drawnProfiles;
        this.post({
          type: 'inspectionFailed',
          file,
          id,
          reason: 'filtered',
          detail:
            active.length === 0
              ? 'It is not in the stack drawn for Compose’s default profile set.'
              : `It is not in the stack drawn for the active profile set (${active.join(', ')}). ` +
                'Switch the profile that selects it back on to see it again.',
        });
        return;
      }
      this.post({
        type: 'inspection',
        file,
        inspection: {
          id,
          name: node?.name ?? shortName(file),
          kind: (node?.kind as NodeKind) ?? 'stack',
          schema,
          findings: findingsFor(this.findings, id),
          staged: this.staging.paths(file),
          opened: [...this.opened],
          pending: pendingValues(this.staging.entries(file)),
          availability: Object.fromEntries(this.availability),
        },
      });
      // Epic 8, AFTER the post and deliberately not awaited.
      //
      // This is the line the whole design of DECISIONS.md 22 is arranged
      // around. The inspection is on screen by the time Docker Hub is asked
      // anything, so a reader with no network gets exactly the pane that
      // shipped before this epic, and a slow answer decorates a drawn pane
      // rather than delaying one. Moving this above the post, or putting an
      // `await` in front of it, would make the inspector's paint depend on a
      // third party's undocumented endpoint.
      this.imageGeneration++;
      void this.lookupImages(file, imageLookupKeys(schema.node?.fields));
    } catch (err) {
      if (file !== this.file) {
        return;
      }
      const detail = err instanceof RpcError ? err.message : err instanceof Error ? err.message : String(err);
      this.post({ type: 'inspectionFailed', file, id, detail });
    }
  }

  /**
   * Records what the file says about the shape of every path in an inspection.
   *
   * The write path branches on this — declared, declared-null, or absent — and
   * the answer has to come from the core rather than from what the pane
   * happens to be showing.
   */
  private recordShapes(schema: StackSchema): void {
    const walk = (fields: SchemaField[] | undefined): void => {
      for (const f of fields ?? []) {
        if (f.declared) {
          this.declared.add(f.path);
          if (f.value?.kind === 'null') {
            this.nullValued.add(f.path);
          } else {
            this.nullValued.delete(f.path);
          }
        } else {
          this.declared.delete(f.path);
          this.nullValued.delete(f.path);
        }
        walk(f.children);
      }
    };
    walk(schema.node?.fields);
  }

  /**
   * Asks the core where each value the pane is about to draw is WRITTEN, and
   * remembers the answer for the write path and for the pane.
   *
   * A failure here is not a failed inspection. The pane still renders — with no
   * availability, which it treats as "nothing is claimed" — and the engine
   * still refuses whatever it cannot do. Losing the explanation is bad; losing
   * the inspector because an explanation could not be fetched is worse, and the
   * reader would have neither.
   */
  private async recordAvailability(file: string, schema: StackSchema): Promise<void> {
    const paths = editablePaths(schema.node?.fields, [...this.opened]);
    if (paths.length === 0) {
      return;
    }
    let answer: { fields: Availability[] };
    try {
      answer = await editable(this.session, file, paths);
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      console.warn(`composure: could not tell where ${file}'s values are written: ${detail}`);
      return;
    }
    if (file !== this.file) {
      return;
    }
    for (const a of answer.fields ?? []) {
      if (typeof a?.path === 'string' && a.path !== '') {
        this.availability.set(a.path, a);
      }
    }
  }

  /**
   * Fetches the children of every mapping the reader has opened, or that the
   * file declares with no value, and attaches them to the field.
   *
   * This is what makes an object-typed key openable at all. `stack/schema` at
   * `services.web` reports `healthcheck` with `declared: false` and NO children
   * — the specification's sub-keys are only walked for a path the file
   * contains. Asking the core again AT that path returns all seven of them with
   * `missing: true`, which is exactly the list the reader needs in order to
   * discover `test` and `interval`.
   *
   * Bounded, because each one is a round trip: a reader can only have opened so
   * many keys before the pane stops being a pane, and an unbounded fan-out here
   * would be a schema request per unset mapping on every keystroke.
   */
  private async expandOpenMappings(file: string, schema: StackSchema): Promise<void> {
    const wanted = fieldsNeedingChildren(
      schema.node?.fields,
      (path) => this.opened.has(path) || this.staging.has(file, path),
    );

    for (const field of wanted.slice(0, MAX_EXPANSIONS)) {
      try {
        const answer = await this.session.request<unknown>('stack/schema', {
          path: file,
          at: field.path,
        });
        const sub = readSchema(answer);
        if (sub.node && sub.node.fields.length > 0) {
          field.children = sub.node.fields;
        }
      } catch (err) {
        // Not fatal and not silent. The key still renders; what the reader does
        // not get is the list of what could go inside it, and saying so beats
        // an empty group that looks like "this mapping permits nothing".
        console.warn(`composure: could not describe ${field.path}: ${String(err)}`);
      }
    }
  }

  /**
   * Story 5.3: move the editor's cursor to a position and select the range.
   *
   * `viewColumn: One` and no panel reveal — this is `Reveal in YAML` done
   * properly. The incumbent's version replaces the UI with the text file; ours
   * moves the cursor and leaves the inspector exactly where it was.
   */
  private async reveal(file: string, line: number, column: number): Promise<void> {
    if (!file || line < 1) {
      return;
    }
    try {
      const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(file));
      const position = new vscode.Position(line - 1, Math.max(0, column - 1));
      const editor = await vscode.window.showTextDocument(doc, {
        viewColumn: vscode.ViewColumn.One,
        preserveFocus: false,
        preview: false,
      });
      // The word at the position, so the reader sees WHAT was selected rather
      // than a bare caret they then have to locate.
      const range = doc.getWordRangeAtPosition(position) ?? new vscode.Range(position, position);
      editor.selection = new vscode.Selection(range.start, range.end);
      editor.revealRange(range, vscode.TextEditorRevealType.InCenterIfOutsideViewport);
    } catch (err) {
      void vscode.window.showWarningMessage(
        `Composure could not open ${file}: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  }

  /** Shows a failure the panel did not ask for — a core that died between calls. */
  showFailure(failure: Failure): void {
    this.post({ type: 'failure', failure });
  }

  private describe(err: unknown): Failure {
    // A refused compose file is not a process failure: the request was well
    // formed and the core answered it with a position.
    if (err instanceof RpcError && err.code === CODE_RESOLVE_FAILED) {
      const d = err.data ?? {};
      const where = d.line ? `${d.file ?? this.file}:${d.line}:${d.column ?? 0}` : (d.file ?? this.file);
      return { kind: 'parse-error', title: 'This file could not be read', detail: `${where}\n${err.message}` };
    }
    if (err instanceof RpcError) {
      return { kind: 'internal', title: 'The Composure core refused the request', detail: err.message };
    }
    // A core whose answer is not a graph is a version skew, not a bad file.
    // Naming it beats drawing an empty canvas that looks like an empty stack.
    if (err instanceof MalformedGraphError) {
      return {
        kind: 'internal',
        title: 'The Composure core returned a topology this extension cannot read',
        detail: `${err.detail}\nthis is a version mismatch between the extension and the core binary`,
      };
    }
    return toFailure(err, this.session.binary());
  }

  private async onMessage(msg: WebviewMessage): Promise<void> {
    switch (msg.type) {
      case 'ready':
        await this.draw();
        break;
      case 'retry':
        // Explicit, reader-initiated, and the only path back from a dead core.
        this.session.restart();
        await this.draw();
        break;
      case 'positions':
        await this.store.setPositions(this.file, msg.positions);
        break;
      case 'select':
        this.selection = msg.id;
        // An opened key belongs to the pane it was opened in. Carrying it to
        // the next selection would put a stray input on another service.
        this.opened.clear();
        await this.store.setSelected(this.file, msg.id);
        // The inspector is docked and has no mode: a selection change refills
        // it, and a deselection fills it with the stack rather than emptying
        // it (story 5.1).
        await this.inspect(this.file, msg.id);
        break;
      case 'reveal':
        await this.reveal(msg.file, msg.line, msg.column);
        break;
      case 'impact':
        await this.impact(msg.id);
        break;
      case 'open':
        // Story 5.2's last acceptance criterion. Clicking an unset key gives
        // the reader somewhere to TYPE — it does not write, and it does not
        // stage, because at this instant they have not said what the value is.
        //
        // The old `stage` case staged an insert_key here carrying
        // `field.default`, which is how a prose default became a line in
        // someone's file and how a key with no default became `key: ` with a
        // trailing space.
        this.opened.add(msg.path);
        await this.inspect(this.file, this.selection);
        break;
      case 'close':
        this.opened.delete(msg.path);
        await this.inspect(this.file, this.selection);
        break;
      case 'edit':
        // Story 6.1. A value change stages and nothing is written.
        await this.stageValue(this.file, msg.path, msg.value);
        // The pane is refilled from the core either way: on a refusal that IS
        // the revert, because the core is the only thing that knows what the
        // field says.
        await this.inspect(this.file, this.selection);
        break;
      case 'editStage':
        await this.stage(this.dockerfile?.path ?? this.file, {
          key: stageKey(msg.stage),
          label: '',
          op: { operation: 'set_base_image', stage: msg.stage, value: msg.value },
        });
        await this.refreshDockerfile();
        break;
      case 'editInstruction':
        await this.stage(this.dockerfile?.path ?? this.file, {
          key: instructionKey(msg.instruction),
          label: '',
          op: { operation: 'replace_args', instruction: msg.instruction, value: msg.value },
        });
        await this.refreshDockerfile();
        break;
      case 'addInstruction':
        // Story 7.6. The reader named an instruction and a stage; the position
        // inside that stage is the core's, computed by the same function the
        // form's stage membership comes from. Nothing is written — this stages,
        // and `Save to <file>` writes.
        await this.stage(this.dockerfile?.path ?? this.file, {
          key: addInstructionKey(msg.stage, msg.text),
          label: '',
          op: { operation: 'insert_instruction', stage: msg.stage, value: msg.text },
        });
        await this.refreshDockerfile();
        break;
      case 'addStage':
        // Story 7.7. `+ add stage`, at last connected to the engine that has
        // been able to do it since Epic 6 (DECISIONS.md 20).
        await this.stage(this.dockerfile?.path ?? this.file, {
          key: addStageKey(msg.image, msg.name),
          label: '',
          op: { operation: 'insert_stage', value: msg.image, key: msg.name },
        });
        await this.refreshDockerfile();
        break;
      case 'add':
        // Stories 7.3 and 7.4. The reader named a service or a resource; where
        // it goes, whether the name is free and whether the value can be
        // written bare are all the core's answers, asked for before anything is
        // staged.
        await this.addDeclaration(msg.kind, msg.name, msg.value);
        break;
      case 'openExtract':
        // Story 9.3. A question. `stack/extract` previews and writes nothing —
        // it is one boolean away from the method that writes, which is why the
        // diffs on screen are the diffs that would land.
        await this.previewExtract(this.file, msg.path, msg.name);
        break;
      case 'closeExtract':
        // Nothing to do, and deliberately nothing. Closing the block is the
        // webview's own view state; a move that has been STAGED is dropped
        // through `Discard`, with the strip saying so, which is the same rule
        // an opened key follows. The case exists so that the exhaustive switch
        // over `WebviewMessage` stays exhaustive.
        break;
      case 'stageExtract':
        await this.stageExtract(this.file, msg.path, msg.name);
        break;
      case 'openExtractArg':
        // Story 9.4. A question about the DOCKERFILE, so it is asked against
        // the file the stage form is drawn from rather than `this.file`, which
        // is still the compose file the reader arrived through.
        await this.previewExtractArg(this.editTarget(), msg.instruction, msg.name);
        break;
      case 'closeExtractArg':
        // Deliberately nothing, for the reason `closeExtract` gives: closing
        // the block is the webview's own view state, and a move that has been
        // STAGED is dropped through `Discard` with the strip saying so.
        break;
      case 'stageExtractArg':
        await this.stageExtractArg(this.editTarget(), msg.instruction, msg.name);
        break;
      case 'openComment':
        // Story 9.1. It READS, and it reads by asking the engine what it would
        // delete — which is the only answer that cannot disagree with what a
        // write would do, because it is the same function.
        await this.readComments(this.file, msg.path);
        break;
      case 'closeComment':
        // Nothing was staged by opening it, so nothing has to be unstaged. The
        // block is the webview's own view state and it has already closed.
        break;
      case 'setComment':
        await this.stage(this.file, {
          key: commentKey(msg.path, msg.where),
          label: '',
          op: { operation: 'set_comment', at: msg.path, where: msg.where, value: msg.text },
        });
        // The block is redrawn from the STAGE, so the reader's own words stay
        // in the field they typed them in. Re-reading the file here would put
        // the file's sentence back over the top of theirs, which looks exactly
        // like the edit having been lost.
        await this.readComments(this.file, msg.path);
        await this.inspect(this.file, this.selection);
        break;
      case 'deleteComment':
        await this.stage(this.file, {
          key: commentKey(msg.path, msg.where),
          label: '',
          op: { operation: 'delete_comment', at: msg.path, where: msg.where },
        });
        await this.readComments(this.file, msg.path);
        await this.inspect(this.file, this.selection);
        break;
      case 'addEntry':
        // Story 9.2. The end of the list, chosen by the engine — the request
        // names the LIST and never a position, because a position the reader
        // named is a position the list may not have.
        await this.stage(this.file, {
          key: addEntryKey(msg.path, msg.value),
          label: '',
          op: { operation: 'insert_sequence_entry', at: msg.path, value: msg.value },
        });
        await this.inspect(this.file, this.selection);
        break;
      case 'addKey': {
        // A key added to a free-form mapping — `environment`, `labels`,
        // `build.args`. One `insert_key`, at the MAPPING, which is the same
        // operation `stageValue` stages for any key the file does not have;
        // nothing new is asked of the engine and the diff is one line.
        //
        // Keyed by `addedPath`, the config path the insert CREATES, exactly as
        // a declaration is (stories 7.3 and 7.4). So a reader who types
        // `LOG_LEVEL` twice replaces their own stage rather than staging two
        // `LOG_LEVEL:` lines, and the inspector marks the path they can see.
        const op: EditOp = {
          operation: 'insert_key',
          at: msg.path,
          key: msg.key,
          value: msg.value,
        };
        await this.stage(this.file, { key: addedPath(op), label: '', op });
        await this.inspect(this.file, this.selection);
        break;
      }
      case 'removeEntry':
        // Story 9.2, and staged like everything else: the reader sees the
        // one-line diff in the strip and `Save to <file>` is still the only
        // control in this product that touches a file.
        await this.stage(this.file, {
          key: removeEntryKey(msg.path),
          label: '',
          op: { operation: 'delete_key', at: msg.path },
        });
        await this.inspect(this.file, this.selection);
        break;
      case 'unstage':
        if (this.staging.remove(this.editTarget(), msg.path)) {
          // A key whose only reason to be on screen was the stage goes back to
          // the `available, not set` list rather than lingering as an empty
          // field the reader has to dismiss twice.
          this.opened.delete(msg.path);
          await this.refreshPending();
          await this.refill();
        }
        break;
      case 'save':
        await this.save();
        break;
      case 'discard':
        this.discardStages(this.editTarget());
        await this.refill();
        break;
      case 'openDockerfile':
        await this.openDockerfileNode(msg.id);
        break;
      case 'backToStack':
        this.dockerfile = undefined;
        await this.draw();
        break;
      case 'split':
        await this.store.setSplit(this.file, msg.ratio);
        break;
      case 'searchImage':
        // Epic 8. The only message in this protocol that causes a network
        // request, and it exists because a reader typed into a search box.
        await this.searchImages(msg.token, msg.query);
        break;
      case 'setProfiles':
        // Story 4.6, R1.4: "let the user toggle profiles and watch the
        // topology change". View state, then a redraw — never a file.
        await this.setProfiles(msg.profiles);
        break;
    }
  }

  /* ---- Epic 8: image discovery ---------------------------------------- */

  /**
   * Whether this window may talk to Docker Hub at all.
   *
   * `composure.dockerHub`, default `on`. A tool that has been a pure function of
   * files on disk for its whole life does not silently start making requests,
   * and a reader who turns this off gets EXACTLY the product that shipped
   * before Epic 8 — not a pane full of "switched off" notes, which would be a
   * second way of talking about Docker Hub on every field.
   */
  private hubEnabled(): boolean {
    return vscode.workspace.getConfiguration('composure').get<string>('dockerHub', 'on') !== 'off';
  }

  /**
   * Asks Docker Hub about the images in a pane that is ALREADY ON SCREEN.
   *
   * NOTHING AWAITS THIS. It is started after `inspect` has posted, it is
   * `void`ed by its caller, and every one of its own failures is swallowed into
   * a dropped message. The property being defended is that the inspector on a
   * machine with no route to the internet is byte-for-byte the inspector that
   * shipped before this epic — no gap, no spinner, no delayed paint. See
   * DECISIONS.md 22.
   *
   * The requests are made one at a time rather than in parallel: a pane asks
   * about one image in practice, and serialising means a rate-limited reader
   * spends one request finding out rather than four.
   */
  private async lookupImages(file: string, asks: { key: string; ref: string }[]): Promise<void> {
    if (!this.hubEnabled() || asks.length === 0) {
      return;
    }
    const generation = this.imageGeneration;
    for (const ask of asks.slice(0, MAX_IMAGE_LOOKUPS)) {
      let answer: unknown;
      try {
        answer = await this.session.request<unknown>(
          'image/lookup',
          { ref: ask.ref },
          IMAGE_LOOKUP_TIMEOUT_MS,
        );
      } catch (err) {
        // Deliberately silent on screen. The core answers a network outcome as
        // a RESULT with a sentence, so reaching here means the core itself is
        // gone or too slow — which every other request in this panel already
        // reports, loudly, through its own banner. A second banner about an
        // optional decoration would be noise on top of the real failure.
        console.warn(`composure: image lookup for ${ask.ref} did not answer: ${String(err)}`);
        return;
      }
      if (generation !== this.imageGeneration || file !== this.editTarget()) {
        return; // the reader moved on; this answer is about a pane that is gone
      }
      const lookup = readLookup(answer);
      if (lookup === null || !isSayable(lookup.state)) {
        continue;
      }
      this.post({ type: 'imageLookup', file, key: ask.key, lookup });
    }
  }

  /** The same, for the base image of every stage in the open Dockerfile. */
  private async lookupStageImages(file: string, form: DockerfileForm): Promise<void> {
    await this.lookupImages(
      file,
      form.stages
        .slice(0, MAX_STAGE_LOOKUPS)
        .filter((s) => s.image_ref.trim() !== '')
        .map((s) => ({ key: stageLookupKey(s.index), ref: s.image_ref })),
    );
  }

  /**
   * Finds an image by name — the one request in this product a reader causes
   * directly.
   *
   * A failure is answered with a STATE and a sentence rather than swallowed,
   * because unlike the pill this is something the reader asked for and is
   * watching. A popup that stays empty when Docker Hub is busy reads as "there
   * is no such image".
   */
  private async searchImages(token: number, query: string): Promise<void> {
    if (!this.hubEnabled()) {
      this.post({
        type: 'imageSearch',
        token,
        answer: {
          query,
          state: 'disabled',
          message:
            'Docker Hub search is switched off in this window (composure.dockerHub). ' +
            'You can still type any image reference you like.',
          results: [],
        },
      });
      return;
    }
    let answer: unknown;
    try {
      answer = await this.session.request<unknown>(
        'image/search',
        { query, limit: 15 },
        IMAGE_LOOKUP_TIMEOUT_MS,
      );
    } catch (err) {
      this.post({
        type: 'imageSearch',
        token,
        answer: {
          query,
          state: 'offline',
          message:
            'Docker Hub could not be reached, so there is nothing to show here. ' +
            'You can still type any image reference you like.',
          results: [],
        },
      });
      console.warn(`composure: image search for ${query} failed: ${String(err)}`);
      return;
    }
    const parsed = readSearch(answer);
    if (parsed === null) {
      this.post({
        type: 'imageSearch',
        token,
        answer: {
          query,
          state: 'offline',
          message:
            'Docker Hub answered with something this version does not understand. ' +
            'You can still type any image reference you like.',
          results: [],
        },
      });
      return;
    }
    this.post({ type: 'imageSearch', token, answer: parsed });
  }

  /* ---- the write path — story 6.1 ------------------------------------- */

  /** The file staged edits are held against: the Dockerfile if one is open. */
  private editTarget(): string {
    return this.dockerfile?.path ?? this.file;
  }

  /* ---- Epic 9, story 9.3: moving a value into a variable --------------- */

  /**
   * What the move would do, both halves, with nothing written.
   *
   * A refusal is posted as part of the same answer rather than as a banner:
   * `${DB_PASSWORD}` has no literal to move and a `.env` that already gives the
   * name a different value is somebody's configured value, and both of those
   * belong at the control the reader just pressed rather than in a strip at the
   * other end of the pane.
   */
  private async previewExtract(file: string, path: string, name?: string): Promise<void> {
    let result: ExtractResult;
    try {
      result = await extractPreview(this.session, file, path, name);
    } catch (err) {
      if (file !== this.file) {
        return;
      }
      this.post({ type: 'extract', file, path, staged: false, refused: refusalDetail(err) });
      return;
    }
    if (file !== this.file) {
      return;
    }
    this.post({
      type: 'extract',
      file,
      path,
      staged: this.extract?.path === path && this.extract.file === file,
      result,
    });
  }

  /**
   * Records the move the reader chose. Writes nothing — `Save` does.
   *
   * The preview is re-asked with the reader's final name rather than trusting
   * the one already on screen, because the name in the field is what they will
   * see in the strip and the two must be the same request.
   */
  private async stageExtract(file: string, path: string, name: string): Promise<void> {
    if (this.staging.count(file) > 0) {
      this.post({
        type: 'editRefused',
        file,
        path,
        title: 'That move was not staged',
        detail: EXTRACT_ALONE,
      });
      return;
    }
    let result: ExtractResult;
    try {
      result = await extractPreview(this.session, file, path, name);
    } catch (err) {
      this.reportEditFailure(file, path, err);
      return;
    }
    this.extract = { file, path, name: result.name, result };
    await this.refreshPending();
    await this.inspect(this.file, this.selection);
  }

  /* ---- Epic 9, story 9.4: moving a literal into a build argument -------- */

  /**
   * What the move would do, with nothing written — story 9.4.
   *
   * A refusal is posted as part of the same answer rather than as a banner, for
   * the reason the compose half gives: a `FROM` pinned by digest, a value that
   * cannot be a bare `ARG` default and a name already declared with something
   * else are all answers to what the reader just asked, and they belong at the
   * control they pressed.
   */
  private async previewExtractArg(file: string, instruction: number, name?: string): Promise<void> {
    let result: ExtractArgResult;
    try {
      result = await extractArgPreview(this.session, file, instruction, name);
    } catch (err) {
      if (file !== this.editTarget()) {
        return;
      }
      this.post({ type: 'extractArg', file, instruction, staged: false, refused: refusalDetail(err) });
      return;
    }
    if (file !== this.editTarget()) {
      return;
    }
    this.post({
      type: 'extractArg',
      file,
      instruction,
      staged: this.extractArg?.instruction === instruction && this.extractArg.file === file,
      result,
    });
  }

  /**
   * Records the move the reader chose. Writes nothing — `Save` does.
   *
   * The preview is re-asked with the reader's final name rather than trusting
   * the one on screen, exactly as the compose half does: the name in the field
   * is what the strip will show, and the two must be the same request.
   */
  private async stageExtractArg(file: string, instruction: number, name: string): Promise<void> {
    if (this.staging.count(file) > 0) {
      this.post({
        type: 'editRefused',
        file,
        path: `instruction:${instruction}`,
        title: 'That move was not staged',
        detail: EXTRACT_ALONE,
      });
      return;
    }
    let result: ExtractArgResult;
    try {
      result = await extractArgPreview(this.session, file, instruction, name);
    } catch (err) {
      this.reportEditFailure(file, `instruction:${instruction}`, err);
      return;
    }
    this.extractArg = { file, instruction, name: result.name, result };
    await this.refreshPending();
    // The block the reader is looking at says so, rather than waiting for the
    // next question to make it true. `staged: true` is the whole difference.
    this.post({ type: 'extractArg', file, instruction, staged: true, result });
  }

  /** The sentence under the `.env` diff — which of the three shapes this is. */
  private envNote(result: ExtractResult): string {
    const where = fileName(result.env_file);
    if (result.env_unchanged) {
      return (
        `${where} already gives ${result.name} this value, so it is left byte-identical. ` +
        'Only the compose file changes.'
      );
    }
    if (result.env_created) {
      return `This creates ${where} beside the compose file and puts ${result.name} in it.`;
    }
    return `This appends a line to the existing ${where}. Every byte already in it stays as it is.`;
  }

  /* ---- Epic 9, story 9.1: comments ------------------------------------- */

  /**
   * What the file says at one key's two comment positions, and what is staged
   * against them.
   *
   * Asked as a `delete_comment` PREVIEW, which is the only way to get the
   * answer without a second implementation of it: the range a delete would
   * remove is the comment, by definition, and `ops[0].before` is the bytes in
   * it. A preview writes nothing, and the refusal for a position with no
   * comment is `no-comment` — a name, not a silence, which is exactly why that
   * sentinel exists rather than `ErrNoChange`.
   *
   * A staged comment wins over the file's, because the reader typed it and the
   * file has not been written. That is the same both-halves-travel rule the
   * value fields already follow.
   */
  private async readComments(file: string, path: string): Promise<void> {
    const answer: CommentsAt = { path, above: null, trailing: null, staged: [] };
    const unavailable: { where: CommentWhere; detail: string }[] = [];
    for (const where of COMMENT_POSITIONS) {
      const stage = this.staging
        .entries(file)
        .find((e) => e.key === commentKey(path, where));
      if (stage) {
        answer.staged.push(where);
        answer[where] = stage.op.operation === 'delete_comment' ? null : (stage.op.value ?? '');
        continue;
      }
      try {
        const result = await preview(this.session, file, [
          { operation: 'delete_comment', at: path, where },
        ]);
        answer[where] = commentText(result.ops[0]?.before ?? '');
      } catch (err) {
        const reason = reasonOf(err);
        if (reason === 'no-comment') {
          // Nothing there, which is a fact and not a failure: the field opens
          // empty and typing in it adds one.
          continue;
        }
        // Anything else is the engine declining to touch this position at all
        // — a block scalar, a flow collection, an alias. Rule 6: no field, and
        // the reason at the place the field would have been.
        unavailable.push({ where, detail: refusalDetail(err) });
      }
    }
    if (unavailable.length > 0) {
      answer.unavailable = unavailable;
    }
    if (file !== this.file) {
      return;
    }
    this.post({ type: 'comments', file, ...answer });
  }

  /**
   * Turns "the reader typed this into that field" into operations the engine
   * can execute.
   *
   * Three shapes, decided by what the file actually says at the path — which is
   * the core's answer from the last inspection, never a guess here:
   *
   *   the file declares a value   `replace_scalar`. One line out, one line in.
   *   the file does not have it   `insert_key` into the parent mapping, plus an
   *                               `insert_key` for every ancestor mapping the
   *                               reader opened that is not in the file either,
   *                               ordered outermost first. `healthcheck.test`
   *                               typed into an unwritten `healthcheck` is two
   *                               operations and a two-line diff.
   *   the file declares it NULL   a delete and an insert. `replace_scalar` over
   *                               a null is not survivable: the core computes a
   *                               range with no bytes in it, and mid-file that
   *                               splice welds the following line onto this one
   *                               (see TESTING.md — it is a core defect, and
   *                               this is why the pane refuses to send it).
   */
  private async stageValue(file: string, path: string, value: string): Promise<void> {
    const entries: StagedEdit[] = [];

    // Story 9.2, and the first thing this function has to know, because every
    // branch below it asks `declared`. A sequence entry is NEVER in `declared`:
    // `internal/schema` gives it no path in the wire schema, so `recordShapes`
    // has nothing to record. Without this the path falls through to the insert
    // branch and stages `insert_key at services.web.command key "2"`, which on
    // a sequence adds nothing at all — a confident wrong answer with a plan
    // attached, and DECISIONS.md 24's whole subject.
    //
    // The bytes are there; `locate` has resolved a numeric segment against a
    // sequence since Epic 1. So the operation is the ordinary two-line splice,
    // and the index moving under a stage is caught by the `expect` that
    // `stageAll` records against it, exactly as it is everywhere else.
    if (isEntryPath(path)) {
      await this.stageAll(file, [
        { key: path, label: '', op: { operation: 'replace_scalar', at: path, value } },
      ]);
      return;
    }

    // Decision 21. A value the file does not write HERE — it arrives through
    // `<<: *anchor` — is staged as an INSERT on the mapping the reader is
    // looking at, which is what a merge-key override is in YAML: the local key
    // wins, and the anchor is not touched. The alternative, editing the anchor,
    // would change every service that merges it, and the reader who clicked a
    // field on `web` did not ask about `db`.
    //
    // The core decides, not this branch: `plan` is `stack/editable`'s answer,
    // and a reason with no plan (an inherited MAPPING, an alias, a block
    // scalar) reaches here only if the pane offered a field it should not have
    // — in which case the engine refuses by name and nothing is written.
    const inherited = this.availability.get(path);
    const overrides = inherited?.reason === 'inherited' && inherited.plan === 'insert_key';

    if (this.declared.has(path) && !this.nullValued.has(path) && !overrides) {
      entries.push({
        key: path,
        label: '',
        op: { operation: 'replace_scalar', at: path, value },
      });
    } else if (this.nullValued.has(path)) {
      entries.push({
        key: path,
        label: '',
        op: { operation: 'delete_key', at: path },
      });
      entries.push({
        key: `${path}#set`,
        label: '',
        op: { operation: 'insert_key', at: parentOf(path), key: leafOf(path), value },
      });
    } else {
      for (const ancestor of this.unwrittenAncestors(path)) {
        entries.push({
          key: ancestor,
          label: '',
          op: {
            operation: 'insert_key',
            at: parentOf(ancestor),
            key: leafOf(ancestor),
            // Empty on purpose: the mapping is a container and its VALUE is the
            // keys underneath. Nothing the schema did not supply is written.
            value: '',
          },
        });
      }
      entries.push({
        key: path,
        label: '',
        op: { operation: 'insert_key', at: parentOf(path), key: leafOf(path), value },
      });
    }
    await this.stageAll(file, entries);
  }

  /**
   * Declares something the file does not have yet — stories 7.3 and 7.4.
   *
   * The plan comes from the core and is staged WHOLE. A service is its name and
   * its image, and they are one request, one diff and one undo: a `cache:` with
   * nothing under it is not a stack anything can run, and leaving one on disk
   * because a second call failed is the partial write `edit.run` was built to
   * make impossible. Nothing here writes — `Save to <file>` still does.
   */
  private async addDeclaration(kind: AddKind, name: string, value: string): Promise<void> {
    // The compose file, never the Dockerfile that may be open behind this: a
    // service does not go in a Dockerfile, and `editTarget` would send it there.
    const file = this.file;
    let plan: { ops: EditOp[] };
    try {
      plan = await planAdd(this.session, this.file, file, kind, name, value);
    } catch (err) {
      // Refused before anything was staged, which is the point of asking first:
      // the pending strip never fills with an edit the core will not perform.
      this.reportEditFailure(file, '', err);
      return;
    }
    await this.stageAll(
      file,
      plan.ops.map((op) => ({
        // Keyed by the config path of the thing being added — the same key
        // `stageValue` uses for an insert — so declaring `cache` twice replaces
        // the reader's own stage rather than staging two of them, and the
        // inspector marks the same path staged.
        key: addedPath(op),
        label: '',
        op,
      })),
    );
  }

  /**
   * The mapping ancestors of a path that the reader has opened and the file
   * does not contain, outermost first.
   *
   * Only opened ones: this never invents intermediate structure the reader did
   * not ask for. It is what makes typing a value into an unwritten
   * `healthcheck` produce `healthcheck:` and `test:` together rather than an
   * operation the core refuses with "path segment not found".
   */
  private unwrittenAncestors(path: string): string[] {
    const out: string[] = [];
    for (const candidate of this.opened) {
      if (candidate === path || !path.startsWith(`${candidate}.`)) {
        continue;
      }
      if (this.declared.has(candidate) || this.staging.has(this.editTarget(), candidate)) {
        continue;
      }
      out.push(candidate);
    }
    return out.sort((a, b) => a.length - b.length);
  }

  /** Stages one edit. The single-operation case, which most edits are. */
  private async stage(file: string, entry: StagedEdit): Promise<void> {
    await this.stageAll(file, [entry]);
  }

  /**
   * Stages a set of edits together, after asking the core what it would do.
   *
   * The preview is not decoration. It is what produces the byte ranges the
   * stages are held against, and it is where a refusal surfaces — BEFORE the
   * reader has a `Save` button that will fail. An edit the engine cannot
   * perform safely never enters the pending list at all.
   *
   * The WHOLE set is previewed rather than each operation alone, because these
   * operations are not independent: an insert into a mapping that a previous
   * insert creates cannot be previewed on its own, and the byte range of a
   * later operation depends on the earlier ones.
   */
  private async stageAll(file: string, entries: StagedEdit[]): Promise<void> {
    // Story 9.3, the other direction. A move holds byte ranges against the file
    // on disk and so does every stage here; whichever was written first would
    // move the other, and AD-19 would then discard it. Refusing now is the same
    // answer one step earlier, and nothing has been written either way.
    // Story 9.4's move is the same arithmetic against a Dockerfile, so it is
    // the same refusal: a check that asked only about the compose half would
    // let an ordinary instruction edit sit on top of a staged `ARG` move and
    // AD-19 would then discard one of them at the write.
    if (
      (this.extract && this.extract.file === file) ||
      (this.extractArg && this.extractArg.file === file)
    ) {
      this.post({
        type: 'editRefused',
        file,
        path: entries[0]?.key ?? '',
        title: 'That edit was not staged',
        detail: EXTRACT_ALONE,
      });
      return;
    }
    // Existing stages keep their position; a restage of the same key replaces
    // in place rather than moving to the end, so the diff does not reshuffle
    // under the reader between one keystroke and the next.
    const merged = new Map<string, StagedEdit>();
    for (const e of this.staging.entries(file)) {
      merged.set(e.key, e);
    }
    for (const e of entries) {
      merged.set(e.key, e);
    }
    const ordered = [...merged.values()];

    let result: EditResult;
    try {
      result = await preview(
        this.session,
        file,
        ordered.map((e) => e.op),
      );
    } catch (err) {
      // Nothing is half-staged: the set the reader had before this attempt is
      // exactly the set they still have.
      this.reportEditFailure(file, entries[0]?.key ?? '', err);
      return;
    }

    // AD-19's record: where each edit sits NOW. The core compares it against
    // the file at write time and refuses if it has moved.
    ordered.forEach((entry, i) => {
      const landed = result.ops[i];
      const op: EditOp = landed
        ? {
            ...entry.op,
            expect: { start: landed.range.start, end: landed.range.end, text: landed.before },
          }
        : entry.op;
      this.staging.set(file, { ...entry, op, label: describeEdit(op) });
    });
    await this.refreshPending();
  }

  /**
   * Recomputes the pending diff for everything staged and posts it.
   *
   * The diff comes from `stack/preview` and is never assembled here: the whole
   * claim is that the diff the reader approves is the diff that lands, and two
   * implementations of "what would change" cannot both be that.
   */
  private async refreshPending(): Promise<boolean> {
    const file = this.editTarget();
    // Story 9.3. A staged move is the whole of the pending set — it is
    // exclusive — and it carries TWO diffs, because it writes two files.
    const move = this.extract;
    if (move && move.file === file) {
      this.post({
        type: 'pending',
        file,
        count: 1,
        diff: move.result.compose.diff,
        added: move.result.compose.added,
        removed: move.result.compose.removed,
        saveLabel: `${saveLabel(file)} and ${fileName(move.result.env_file)}`,
        env: {
          file: move.result.env_file,
          diff: move.result.env_diff ?? '',
          note: this.envNote(move.result),
        },
      });
      return true;
    }
    // Story 9.4. Also the whole of the pending set, and also exclusive — but
    // ONE file, so the strip is the ordinary one-diff strip and the button says
    // one name. A second diff here would name a file this operation does not
    // write, which is DECISIONS.md 27's point read backwards.
    const argMove = this.extractArg;
    if (argMove && argMove.file === file) {
      this.post({
        type: 'pending',
        file,
        count: 1,
        diff: argMove.result.dockerfile.diff,
        added: argMove.result.dockerfile.added,
        removed: argMove.result.dockerfile.removed,
        saveLabel: saveLabel(file),
      });
      return true;
    }
    const ops = this.staging.ops(file);
    if (ops.length === 0) {
      this.post({ type: 'pendingCleared', file });
      return true;
    }
    try {
      const result = await preview(this.session, file, ops);
      this.post({
        type: 'pending',
        file,
        count: ops.length,
        diff: result.diff,
        added: result.added,
        removed: result.removed,
        saveLabel: saveLabel(file),
      });
      return true;
    } catch (err) {
      this.reportEditFailure(file, '', err);
      return false;
    }
  }

  /**
   * Writes. The only method in this extension that does.
   *
   * On a stale range the whole stage is discarded (AD-19) rather than retried,
   * and the reader is told. On any other refusal the stages stay put, because
   * the reader can still discard them or fix the file.
   */
  private async save(): Promise<void> {
    const file = this.editTarget();
    // Story 9.3. The one staged thing in this product that `stack/apply`
    // cannot perform: it writes the `.env` and the compose file, in that order,
    // with both buffers computed and validated before either is opened.
    const move = this.extract;
    if (move && move.file === file) {
      try {
        // Story 9.6: the preview the reader approved, sent back as the
        // assertion. The compose half's range comes off its own operation; the
        // `.env` half travels verbatim, because it is an answer about the
        // variable rather than a range this side could recompute.
        await extractApply(
          this.session,
          move.file,
          move.path,
          move.name,
          expectOf(move.result.compose),
          move.result.env_expect,
        );
      } catch (err) {
        if (classify(err) === 'stale') {
          this.extract = undefined;
          this.discardStages(file, true);
          await this.refill();
          return;
        }
        this.reportEditFailure(file, move.path, err);
        return;
      }
      this.extract = undefined;
      this.post({ type: 'pendingCleared', file });
      await this.refill();
      return;
    }
    // Story 9.4. One file, and still not `stack/apply`: the substitution and
    // the declaration are decided together, because where the `ARG` may legally
    // go depends on which instruction the literal came out of.
    const argMove = this.extractArg;
    if (argMove && argMove.file === file) {
      try {
        await extractArgApply(
          this.session,
          argMove.file,
          argMove.instruction,
          argMove.name,
          expectOf(argMove.result.dockerfile),
        );
      } catch (err) {
        if (classify(err) === 'stale') {
          this.extractArg = undefined;
          this.discardStages(file, true);
          await this.refill();
          return;
        }
        this.reportEditFailure(file, `instruction:${argMove.instruction}`, err);
        return;
      }
      this.extractArg = undefined;
      this.post({ type: 'pendingCleared', file });
      await this.refill();
      return;
    }
    const ops = this.staging.ops(file);
    if (ops.length === 0) {
      return;
    }
    try {
      await apply(this.session, file, ops);
    } catch (err) {
      if (classify(err) === 'stale') {
        this.discardStages(file, true);
        await this.refill();
        return;
      }
      this.reportEditFailure(file, '', err);
      return;
    }
    // Written. The stage is spent, and everything on screen is re-derived from
    // the file that now exists rather than from what we believed we wrote.
    this.staging.clear(file);
    this.post({ type: 'pendingCleared', file });
    await this.refill();
  }

  /**
   * Drops every stage against a file and says so when it was a discard rather
   * than a save. Silence here would be work the reader thinks they still have.
   */
  /**
   * Whether anything is staged against a file, in either of the two forms.
   *
   * The extract is counted, and it has to be: a file changing on disk discards
   * every stage held against it, and a discard that says nothing is work the
   * reader still thinks they have. Story 9.3's move lives outside `Staging` —
   * it writes two files and that store holds operations against one — so a
   * check that asked only `Staging` would drop it in silence.
   */
  private hasUnwritten(file: string): boolean {
    return (
      this.staging.count(file) > 0 ||
      this.extract?.file === file ||
      this.extractArg?.file === file
    );
  }

  private discardStages(file: string, stale = false): void {
    const hadMove = this.extract?.file === file || this.extractArg?.file === file;
    if (this.extract?.file === file) {
      this.extract = undefined;
    }
    if (this.extractArg?.file === file) {
      this.extractArg = undefined;
    }
    const had = this.staging.clear(file) || hadMove;
    if (!had && !stale) {
      return;
    }
    this.post({
      type: 'pendingCleared',
      file,
      reason: stale ? STALE_MESSAGE : undefined,
    });
  }

  /** Redraws whichever view is up, from the core. */
  private async refill(): Promise<void> {
    if (this.dockerfile) {
      await this.showDockerfile(this.dockerfile.path, this.dockerfile.from);
      return;
    }
    await this.draw();
  }

  /**
   * Names a failed edit for the reader.
   *
   * A refusal is not a fault and must not read like one: it says what could not
   * be done and why, the field reverts because the pane is refilled from the
   * core, and nothing was written.
   */
  private reportEditFailure(file: string, path: string, err: unknown): void {
    const kind = classify(err);
    if (kind === 'stale') {
      this.discardStages(file, true);
      return;
    }
    this.post({
      type: 'editRefused',
      file,
      path,
      title: kind === 'refused' ? 'That edit was not made' : 'That edit could not be made',
      detail: refusalDetail(err),
    });
  }

  /* ---- the Dockerfile view — stories 6.2 and 6.3 ----------------------- */

  /** Opens the stage form for a Dockerfile node on the canvas (story 6.3). */
  private async openDockerfileNode(id: string): Promise<void> {
    const node = this.graph?.nodes.find((n) => n.id === id);
    if (!node || node.kind !== 'dockerfile') {
      return;
    }
    if (node.build?.inline) {
      // There is no second file to open: the Dockerfile is written in the
      // compose file itself. Saying so beats opening an empty form.
      this.post({
        type: 'editRefused',
        file: this.file,
        path: id,
        title: 'There is no Dockerfile to open',
        detail:
          'This build declares `dockerfile_inline:` — the Dockerfile is written in the ' +
          'compose file itself, and the inspector already shows it.',
      });
      return;
    }
    await this.showDockerfile(this.file, this.file, id);
  }

  /** Points the panel at a Dockerfile opened directly in the editor. */
  async setDockerfile(path: string): Promise<void> {
    if (this.dockerfile?.path === path) {
      return;
    }
    await this.showDockerfile(path, null);
  }

  /**
   * Fetches and posts the stage form.
   *
   * `at` is set when the file is reached through a compose `build:` section, in
   * which case the CORE resolves it relative to the build context — Compose's
   * own reading of where a Dockerfile lives. Resolving it here would be a
   * second answer, and it would be the one that disagreed with the diagnostic.
   */
  private async showDockerfile(path: string, from: string | null, at?: string): Promise<void> {
    try {
      const form = await this.session.request<DockerfileForm>('stack/dockerfile', {
        path,
        at: at ?? '',
      });
      const target = form.path || path;
      this.dockerfile = { path: target, from };
      this.post({ type: 'clearFailure' });
      this.post({
        type: 'dockerfile',
        file: target,
        form,
        from,
        staged: this.staging.paths(target),
      });
      await this.refreshPending();
      // Same rule as the inspector's: the form is on screen first, and the
      // base-image pills decorate it afterwards or never.
      this.imageGeneration++;
      void this.lookupStageImages(target, form);
    } catch (err) {
      this.post({ type: 'failure', failure: this.describe(err) });
    }
  }

  /** Re-fetches the open stage form, if one is open. */
  private async refreshDockerfile(): Promise<void> {
    if (this.dockerfile) {
      await this.showDockerfile(this.dockerfile.path, this.dockerfile.from);
    }
  }

  /**
   * Posts to the webview, if there still is one.
   *
   * A resolve outlives the panel that asked for it whenever the reader closes
   * the tab mid-draw, and postMessage on a disposed panel throws inside an
   * unawaited promise — an unhandled rejection in the extension host for a
   * message nobody was going to read.
   */
  private post(msg: HostMessage): void {
    if (this.disposed) {
      return;
    }
    try {
      void this.panel.webview.postMessage(msg).then(undefined, () => undefined);
    } catch {
      // The panel went away between the check and the call.
    }
  }

  private html(): string {
    const webview = this.panel.webview;
    const nonce = makeNonce();
    const script = webview.asWebviewUri(vscode.Uri.joinPath(this.context.extensionUri, 'dist', 'webview.js'));
    const style = webview.asWebviewUri(vscode.Uri.joinPath(this.context.extensionUri, 'dist', 'webview.css'));
    // default-src 'none' plus a nonce: no CDN, no remote font, no inline
    // handler, and no way for a compose file's contents to become script.
    const csp = [
      `default-src 'none'`,
      `img-src ${webview.cspSource}`,
      `style-src ${webview.cspSource}`,
      `font-src ${webview.cspSource}`,
      `script-src 'nonce-${nonce}'`,
    ].join('; ');
    return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link href="${style}" rel="stylesheet">
<title>Stack</title>
</head>
<body>
<div id="app"></div>
<script nonce="${nonce}" src="${script}"></script>
</body>
</html>`;
  }

  dispose(): void {
    if (this.disposed) {
      return;
    }
    this.disposed = true;
    StackPanel.current = undefined;

    // Watchers and the cursor timer are not in `disposables`, so nothing else
    // releases them. One watcher is created per source file of the drawn stack,
    // so without this every open/close cycle leaks a handle plus a callback
    // that would redraw a panel which no longer exists; the debounced cursor
    // timeout can likewise fire into a disposed webview.
    if (this.cursorTimer !== undefined) {
      clearTimeout(this.cursorTimer);
      this.cursorTimer = undefined;
    }
    for (const w of this.watchers) {
      w.dispose();
    }
    this.watchers = [];
    this.watching = [];

    // Diagnostics outlive nothing: a problems panel still listing a stack
    // whose view is closed is a panel reporting on something nobody can see.
    this.problems.dispose();
    for (const d of this.disposables) {
      d.dispose();
    }
    this.disposables.length = 0;
    this.panel.dispose();
  }
}

/** The last segment of a config path — the key's own name. */
function leafOf(path: string): string {
  const parent = parentOf(path);
  return parent === '' ? path : path.slice(parent.length + 1);
}

/** The file, named the way a reader would name it: the last two segments. */
function shortName(file: string): string {
  const parts = file.replace(/\\/g, '/').split('/');
  return parts.slice(-2).join('/');
}

/**
 * A CSP nonce, from the platform CSPRNG.
 *
 * Math.random() is a predictable PRNG and a predictable nonce is not a nonce —
 * it is a fixed string an attacker who can inject markup can simply write out.
 * This runs in a Node host, so node:crypto is right there.
 */
function makeNonce(): string {
  return randomBytes(24).toString('base64');
}
