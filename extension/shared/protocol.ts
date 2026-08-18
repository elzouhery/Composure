// The contract between the extension host and the webview.
//
// Every message crosses `postMessage`, so both sides compile against this file
// and neither invents a field. Nothing here is a model: the graph is derived in
// Go by `internal/topology` and reaches the webview through `stack/topology`
// unchanged. The webview turns it into pixels and computes nothing about the
// stack that the core did not already say.
//
// The node and edge shapes below mirror the wire schema in
// internal/topology/graph.go field for field, in its snake_case. They are
// deliberately NOT re-cased on the way in: a rename here would be a second
// vocabulary for the same graph, and the two would drift silently — a renamed
// Go struct tag compiles clean on both sides and draws an empty panel. The
// real-binary test in host/realcore.test.ts is what spans that gap.

/**
 * How a collapsed group's id is spelled, so it can never be a config path.
 *
 * A synthetic group node exists only in the webview's collapsed view (story
 * 4.4) and yet its id crosses this boundary — the reader can select one, and
 * `select` carries whatever is selected. So it belongs here rather than in the
 * webview alone: both sides have to be able to tell "an id the core knows" from
 * "an id the canvas invented", and two spellings of that test would drift.
 */
export const GROUP_PREFIX = 'group:';

/** Whether an id belongs to a synthetic group node rather than to the file. */
export function isGroupId(id: string): boolean {
  return id.startsWith(GROUP_PREFIX);
}

/* ---------------------------------------------------------------------------
 * The address of one entry of a list — Epic 9, story 9.2, DECISIONS.md 24.
 *
 * `resolve.Path` has rendered an index as `[n]` and `ParsePath` has read it
 * back since Epic 1, and `replace_scalar` at `services.web.healthcheck.test[1]`
 * has always worked. What has never existed is the address on the WIRE:
 * `internal/schema` gives a sequence entry no `path` of its own, so the client
 * has to construct it.
 *
 * It is constructed HERE and nowhere else, and both the host and the webview
 * call these. Two spellings of `command[2]` would be two answers about which
 * bytes an edit lands on, which is AD-14's rule arriving in TypeScript.
 *
 * Index-based, and content addressing was considered and rejected: "the entry
 * whose text is `wget`" is unavailable in a list with repeats, and `command:`
 * and `test:` routinely have them. The cost of an index is that it moves when
 * the list changes, and the answer to that is not a cleverer address — it is
 * the `expect` every staged edit already carries, which refuses rather than
 * rebases.
 * ------------------------------------------------------------------------ */

/** `services.web.command` + 2 → `services.web.command[2]`. */
export function entryPath(list: string, index: number): string {
  return `${list}[${index}]`;
}

/**
 * Whether a path names a sequence entry rather than a mapping key.
 *
 * The trailing-bracket test is deliberately the WHOLE test. A numeric mapping
 * key renders as `[8080]` too — `resolve.Path.String`'s documented display
 * ambiguity — and this function inherits it, which is safe precisely because
 * nothing here resolves anything: the core disambiguates by the parent node's
 * kind, and `environment: {8080: "x"}` stays a key on both sides. What this
 * decides is only which OPERATION carries the reader's intent, and the answer
 * for both shapes is the same one: splice the scalar that is already there.
 */
export function isEntryPath(path: string): boolean {
  return /\[\d+\]$/.test(path);
}

/** The list an entry belongs to: `services.web.command[2]` → `services.web.command`. */
export function listOf(path: string): string {
  return path.replace(/\[\d+\]$/, '');
}

/** A node position in graph space. View state — it never enters a file. */
export interface Point {
  x: number;
  y: number;
}

/** Where something was written. R1.8: every value carries this. */
export interface Origin {
  file: string;
  line: number;
  column: number;
  step: number;
}

/**
 * What a node is. The set is closed and mirrors `topology.NodeKind`.
 *
 * A network is not a service and must not be drawn as one: the whole point of
 * the canvas is that the reader can tell what kind of thing they are looking at
 * without reading the label.
 */
export type NodeKind =
  | 'service'
  | 'network'
  | 'volume'
  | 'config'
  | 'secret'
  | 'port'
  | 'dockerfile';

/**
 * What a relation is. Mirrors `topology.EdgeKind`.
 *
 * Each declaration form is its own kind rather than being folded into a
 * neighbour: `links`, `network_mode: service:x` and a network attachment all
 * put two services on the same network, but they fail differently.
 */
export type EdgeKind =
  | 'depends_on'
  | 'network'
  | 'volume'
  | 'bind'
  | 'config'
  | 'secret'
  | 'link'
  | 'network_mode'
  | 'publish'
  | 'build';

export interface PortDetail {
  host_ip?: string;
  published: string;
  target: string;
  protocol: string;
  raw: string;
}

export interface BuildDetail {
  context: string;
  dockerfile?: string;
  target?: string;
  /** Context and dockerfile joined for display. Empty when the build is inline. */
  reference?: string;
  inline?: boolean;
}

export interface DependsDetail {
  /** Always present: the short array form means `service_started`. */
  condition: string;
  restart?: string;
  required?: string;
}

export interface AttachDetail {
  aliases: string[];
}

export interface MountDetail {
  source: string;
  target: string;
  mode: string;
  read_only: boolean;
  /** Binds only: the host path exactly as written. */
  host_path?: string;
}

/**
 * One node.
 *
 * `id` is the config path (`services.web`, `services.web.ports[0]`) — the same
 * join key the resolver, topology and diagnostics use. A generated id could not
 * survive a re-resolve and could not be matched to a byte range, which is why
 * saved positions are keyed by it.
 */
/**
 * A node that stands in for a collapsed group — story 4.4.
 *
 * Set ONLY by the webview's collapse view, never by the core, and never
 * persisted anywhere. Collapse is a way of looking at the graph the core sent;
 * it is not a fact about the stack, so it has no business in a compose file or
 * in the saved positions keyed by config path.
 */
export interface CollapsedDetail {
  /** What the members were grouped by. */
  by: 'network' | 'profile';
  /** How many nodes folded into this one. */
  count: number;
  /** The config paths that folded in, so expanding restores exactly them. */
  members: string[];
}

export interface GraphNode {
  id: string;
  kind: NodeKind;
  /** The node's single label. There is never a second one. */
  name: string;
  origin: Origin;
  declared: boolean;
  external: boolean;
  internal?: boolean;
  profiles: string[];
  layer: number;
  /** `image:` as written. Services only, and empty for one that only builds. */
  image?: string;
  port?: PortDetail;
  build?: BuildDetail;
  /** Present only on a synthetic group node the webview folded. Never on the wire. */
  collapsed?: CollapsedDetail;
}

export interface GraphEdge {
  kind: EdgeKind;
  from: string;
  to: string;
  origin: Origin;
  depends_on?: DependsDetail;
  attach?: AttachDetail;
  mount?: MountDetail;
  port?: PortDetail;
  build?: BuildDetail;
}

/**
 * A reference that named something the filtered graph does not contain.
 *
 * The core drops the edge rather than pointing it at a node that is not there,
 * and reports it here. The canvas must show these: a reference that vanishes
 * when a profile is toggled, with nothing said, is the graph lying by omission.
 */
export interface GraphDangling {
  kind: EdgeKind;
  from: string;
  to: string;
  ref: string;
  reason: string;
  origin: Origin;
}

/** The whole derived topology, exactly as `stack/topology` returns it. */
export interface StackGraph {
  profiles: string[];
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Dependency cycles, each as its members' ids. Layering survives them. */
  cycles: string[][];
  dangling: GraphDangling[];
  max_layer: number;
}

/* ---------------------------------------------------------------------------
 * Image discovery — Epic 8, R6.
 *
 * These mirror `internal/hub`'s wire schema field for field, in its snake_case,
 * and are deliberately NOT re-cased. `cmd/composure/image_serve_test.go` asserts
 * the literal names on the Go side against a decoded map, so a struct tag
 * cannot supply the name it is being checked against.
 *
 * THE ONE STRUCTURAL FACT ABOUT EVERYTHING BELOW: this is the only data in this
 * protocol that does not come from a file on disk. It therefore arrives in its
 * OWN message, after the pane it belongs to has already been drawn, and the
 * pane must render correctly having never received it (DECISIONS.md 22).
 * ------------------------------------------------------------------------ */

/**
 * What a lookup found. A closed set mirroring `hub.State`.
 *
 * Six of the nine are not failures at all, and none of them is an error: a
 * pane that showed a banner because Docker Hub is busy would be putting a fault
 * over a panel whose every other statement is still true.
 */
export type ImageState =
  /** A newer stable tag exists in the same family. */
  | 'ok'
  /** The lookup ran and this IS the newest. A real answer, not an absence. */
  | 'current'
  /** Docker Hub could not be reached. */
  | 'offline'
  /** The 180-a-minute-per-address ceiling. Usually a shared office address. */
  | 'rate-limited'
  /** Docker Hub has no such repository. */
  | 'not-found'
  /** Somebody else's registry. Cross-registry search is out of scope (§3). */
  | 'other-registry'
  /** `${VAR}`, `scratch`, or a digest pin: no single tag to compare. */
  | 'not-comparable'
  /** The reader's own switch — `composure.dockerHub: off`. Never `offline`. */
  | 'disabled'
  /** Nobody is waiting for this answer any more. */
  | 'cancelled';

export interface ImageCandidate {
  /** What would go in the file: `postgres:18-alpine`. */
  reference: string;
  tag: string;
  /** `patch` | `minor` | `major` — surfaced, never hidden. */
  kind: string;
  pushed?: string;
  size?: number;
  /** This tag's size minus the current one's, in bytes. */
  size_delta?: number;
  /** False means the sizes could not be compared — NOT that they are equal. */
  has_size?: boolean;
}

export interface ImageLookup {
  reference: string;
  repository: string;
  /** The repository as the FILE spells it, so a pill never reads `library/…`. */
  display: string;
  tag: string;
  state: ImageState;
  /**
   * The sentence a reader sees. Composed in Go, so the CLI, the JSON and this
   * pane cannot word one 429 three ways. Never an error string.
   */
  message: string;
  current_pushed?: string;
  current_size?: number;
  /** `14 months old` — the mockup's own words (directions-3.html:572). */
  age?: string;
  age_days?: number;
  candidate?: ImageCandidate;
  alternatives?: ImageCandidate[];
  /**
   * `node:22-alpine · minor · 40MB smaller` — directions-3.html:576, composed
   * in Go. The webview never assembles this string: three surfaces wording one
   * fact three ways is exactly what a shared core is for.
   */
  pill?: string;
  rate_limit?: { limit?: string; remaining?: string };
}

/** One search result. R6.1's fields, all of them. */
export interface ImageRepo {
  name: string;
  description?: string;
  stars: number;
  /** `1B+` — the v4 endpoint returns a display string, not a count. */
  pulls_display?: string;
  official: boolean;
  /** `official` | `verified_publisher` | `open_source` | `hardened`. */
  badge?: string;
  architectures?: string[];
}

export interface ImageSearchAnswer {
  query: string;
  state: ImageState;
  message: string;
  results: ImageRepo[];
  rate_limit?: { limit?: string; remaining?: string };
}

export type FailureKind =
  | 'core-missing'
  | 'spawn-failed'
  | 'core-crashed'
  | 'timeout'
  | 'parse-error'
  | 'internal';

/**
 * A named failure with a retry. Every failure mode gets one of these — an
 * empty panel and a silent hang are both worse than a banner that is wrong.
 */
export interface Failure {
  kind: FailureKind;
  /** One terse line naming what failed. */
  title: string;
  /** The detail a reader can act on: a path, an exit code, a position. */
  detail: string;
}

export type HostMessage =
  | { type: 'loading'; file: string }
  | {
      type: 'graph';
      file: string;
      graph: StackGraph;
      /**
       * Config paths of Dockerfile nodes whose file is not on disk (story
       * 6.3). Derived by the HOST from the core's own findings, never by the
       * webview: a webview that stat'ed files would be a second answer to
       * "does this exist", and it would disagree with the problems panel.
       */
      missing: string[];
      positions: Record<string, Point>;
      /**
       * The node the reader had selected when this file was last drawn, by
       * config path. Restored on draw — storing a selection and never reading
       * it back is a write with no reader.
       */
      selected: string | null;
      /** True on the first draw of a file: auto-fit rather than keep the view. */
      fit: boolean;
      /**
       * Findings per node, by config path (story 5.4). The node carries the
       * count, so problems are visible before anything is selected.
       */
      severities: Record<string, SeverityCount>;
    }
  | { type: 'empty'; file: string }
  /** The stored pane split for this workspace. View state; never a file. */
  | { type: 'split'; ratio: number }
  /** The inspector's contents for the current selection. Never empty. */
  | { type: 'inspection'; file: string; inspection: Inspection }
  /**
   * The inspector could not be filled. The pane says so in place of the
   * fields; it does not go blank, which would read as "nothing is set".
   *
   * `reason: 'filtered'` is the one case that is not a failure at all — story
   * 4.6. The selection names a real service that the active profile set has
   * filtered OUT of the drawn stack, so there is nothing on the canvas to
   * inspect and the answer is a sentence rather than a fault. It is a separate
   * reason rather than a differently-worded detail because the two need
   * different words: "could not be read" is a defect, "not in the active
   * profile set" is a filter the reader can undo with one press.
   */
  | {
      type: 'inspectionFailed';
      file: string;
      id: string | null;
      detail: string;
      reason?: 'filtered';
    }
  /**
   * The pending-diff strip — story 6.1. Everything staged against one file,
   * as the exact unified diff a write would produce, computed by the core's
   * `stack/preview` and never by this extension.
   *
   * `saveLabel` names the file because that is the question a reader of a
   * multi-file project actually has: `Save to compose.override.yml`.
   */
  | {
      type: 'pending';
      file: string;
      /** How many edits are staged. */
      count: number;
      diff: string;
      added: number;
      removed: number;
      /** The full text of the write button, e.g. `Save to compose.yml`. */
      saveLabel: string;
      /**
       * The `.env` half of a staged move — story 9.3.
       *
       * Present only for an extract, because an extract is the only staged
       * thing in this product that writes a second file. `note` says which of
       * the three shapes it is in words, because `env_created` and
       * `env_unchanged` are two booleans and a reader deciding whether to press
       * Save needs a sentence.
       */
      env?: { file: string; diff: string; note: string };
    }
  /**
   * Nothing is staged any more. `reason` is present when the stage was
   * DISCARDED rather than saved or dropped by the reader — a byte range that
   * moved (AD-19) is the case that matters, and it has to be said out loud.
   */
  | { type: 'pendingCleared'; file: string; reason?: string }
  /**
   * An edit the engine refused. The field reverts (the pane is refilled from
   * the core), nothing was written, and this says what could not be done and
   * why. `ErrFlowStyle` is the known case.
   */
  | { type: 'editRefused'; file: string; path: string; title: string; detail: string }
  /**
   * What the file says at one key's two comment positions — story 9.1.
   *
   * Arrives after the reader opened the block and never before: reading a
   * comment costs two requests, and a pane that asked about every key it drew
   * would make ninety of them to render one service.
   */
  | ({ type: 'comments'; file: string } & CommentsAt)
  /**
   * What a move into a variable would do — story 9.3.
   *
   * Exactly one of `result` and `refused` is present. A refusal is not a fault
   * and is shown where the diffs would have been: `${DB_PASSWORD}` has no
   * literal to move, and a `.env` that already gives the name a different value
   * is somebody's configured value that overwriting would destroy.
   */
  | {
      type: 'extract';
      file: string;
      path: string;
      staged: boolean;
      result?: ExtractResult;
      refused?: string;
    }
  /**
   * What moving a Dockerfile literal into a build argument would do — story 9.4.
   *
   * Exactly one of `result` and `refused` is present, for the same reason the
   * compose half above has that shape: a value that is already `${VAR}`, one
   * that cannot be a bare `ARG` default, a `FROM` pinned by digest and a name
   * already declared with something else are all answers rather than faults,
   * and they belong at the control the reader just pressed.
   */
  | {
      type: 'extractArg';
      file: string;
      instruction: number;
      staged: boolean;
      result?: ExtractArgResult;
      refused?: string;
    }
  /**
   * The Dockerfile view — stories 6.2 and 6.3. `from` is the compose file the
   * reader came through, so `Back to the stack` can be offered and the graph
   * selection restored; null when the Dockerfile was opened directly.
   */
  | {
      type: 'dockerfile';
      file: string;
      form: DockerfileForm;
      from: string | null;
      /** Staging keys with an edit against them: `stage:0`, `instruction:4`. */
      staged: string[];
    }
  /**
   * The reader moved the cursor in the YAML into a node's range — story 4.3's
   * second direction. The webview moves its selection to `id` and posts
   * nothing back: the host already knows, and an echo would be a message loop
   * between two views that agree.
   */
  | { type: 'selection'; id: string | null }
  /**
   * A node's blast radius, from `stack/impact` — story 4.4's focus mode.
   *
   * Computed by `topology.BlastRadius` in Go. The webview dims what is not in
   * the set and computes nothing: a transitive closure written a second time
   * here would be a second answer to "what breaks if this goes down".
   */
  | { type: 'impact'; id: string; dependents: string[]; dependencies: string[] }
  /** Focus could not be established. Nothing is dimmed and the reason is said. */
  | { type: 'impactFailed'; id: string; detail: string }
  /**
   * The rules did not run. NOT the same message as "the rules ran and found
   * nothing", and the difference is the whole point: an empty problems panel
   * after a failed diagnose tells the reader their stack is clean when we do
   * not know that. Every surface that would otherwise show no problems says
   * this instead, and the problems panel carries a finding of its own.
   */
  | { type: 'diagnosticsUnavailable'; file: string; detail: string }
  /**
   * The profiles this project declares, and the ones currently active — story
   * 4.6.
   *
   * `declared` is the CORE's own answer (`profiles` in `stack/schema`,
   * generated by `internal/schema`'s `declaredProfiles`). The webview never
   * derives a profile name from the nodes it happens to be drawing: a service
   * filtered OUT is not on the canvas, so a list built from the nodes would be
   * missing exactly the profiles the reader wants to switch on.
   *
   * `active` is the set the host asked the core for — the same set that
   * produced this graph, these findings and this blast radius.
   */
  | { type: 'profiles'; file: string; declared: string[]; active: string[] }
  /**
   * What Docker Hub says about one image reference — Epic 8.
   *
   * It arrives SEPARATELY, after the pane it belongs to has already been drawn,
   * and it may never arrive at all. That is the whole design: every other
   * message in this protocol is a function of files on disk, this one is not,
   * and a pane that waited for it would hang in a coffee shop.
   *
   * `key` is the join: a config path (`services.web.image`) for the inspector,
   * or `stage:0` for the Dockerfile form — the same keys staging already uses,
   * so a pill and the edit it stages cannot address different things.
   */
  | { type: 'imageLookup'; file: string; key: string; lookup: ImageLookup }
  /**
   * Search results for one popup — Epic 8. `token` is the request the reader is
   * still waiting for; an answer to an older one is dropped rather than shown,
   * because a popup that fills with the results of the query before last is
   * worse than one that is still empty.
   */
  | { type: 'imageSearch'; token: number; answer: ImageSearchAnswer }
  | { type: 'failure'; failure: Failure }
  | { type: 'clearFailure' };

export type WebviewMessage =
  | { type: 'ready' }
  | { type: 'retry' }
  | { type: 'positions'; positions: Record<string, Point> }
  | { type: 'select'; id: string | null }
  /**
   * Move the editor's cursor to a position and select the range there — story
   * 5.3's provenance click. The inspector stays put: this moves a cursor, it
   * does not replace the UI with the text file.
   */
  | { type: 'reveal'; file: string; line: number; column: number }
  /**
   * The reader clicked a key the file does not declare — story 5.2's last
   * acceptance criterion.
   *
   * It OPENS the key: the inspector renders it as a real field with the cursor
   * in it, and for an object-typed key it renders that key's own children and
   * their own `available, not set` list. Nothing is staged and nothing is
   * written, because at this instant the reader has said WHICH key they want
   * and not what it should say.
   *
   * This replaced a `stage` message that staged an `insert_key` here, which is
   * why adding an attribute was a dead end: it wrote a value the schema never
   * supplied and then rendered the key as a button again.
   */
  | { type: 'open'; path: string }
  /**
   * The reader gave up on an opened key — Escape in a field they never typed
   * into. It goes back to the `available, not set` list, and since nothing was
   * ever staged, nothing has to be unstaged.
   */
  | { type: 'close'; path: string }
  /**
   * Stage a scalar change — story 6.1. `Enter` or blur in an inspector field
   * sends this; `Escape` sends nothing at all, which is what makes revert free.
   */
  | { type: 'edit'; path: string; value: string }
  /**
   * Stage a new entry at the END of the list at `path` — story 9.2.
   *
   * The position is the end and is not the reader's to choose: an index they
   * named would be a position the list may not have, which is the exact
   * confident wrong answer `entry-index` exists to refuse.
   */
  /**
   * Show me the comment on this key — story 9.1.
   *
   * It reads rather than writes, and it is a request to the HOST rather than
   * something the webview works out, because the only trustworthy answer to
   * "which lines are the comment above this key" is the engine's own: it is
   * `attachedCommentStart`, the function `delete_key` has always used to
   * decide which comments travel with a deleted node. A second definition in
   * TypeScript would be a second answer, and the one that wrote bytes would be
   * the wrong one (DECISIONS.md 23).
   */
  | { type: 'openComment'; path: string }
  /** The reader closed the comment block. Nothing was staged, so nothing unstages. */
  | { type: 'closeComment'; path: string }
  /** Stage the comment at (`path`, `where`). Replaces the run that is there. */
  | { type: 'setComment'; path: string; where: CommentWhere; text: string }
  /** Stage the removal of the comment at (`path`, `where`). */
  | { type: 'deleteComment'; path: string; where: CommentWhere }
  /**
   * What would moving this value into a variable do — story 9.3.
   *
   * A question, never a write. `name` is absent the first time (the core
   * derives one from the key) and carries the reader's own the moment they
   * change it, so the diffs on screen are always the diffs for the name in the
   * field above them.
   */
  | { type: 'openExtract'; path: string; name?: string }
  /** The reader closed the move without choosing it. Nothing was staged. */
  | { type: 'closeExtract'; path: string }
  /**
   * Stage the move the reader has just been shown — story 9.3.
   *
   * `Save to <file>` still performs it, and it is the only control that does.
   * What it saves is no longer literally one file, which is why the strip
   * carries both diffs and the button names both files.
   */
  | { type: 'stageExtract'; path: string; name: string }
  /**
   * What would moving this Dockerfile literal into a build argument do — story
   * 9.4, the same gesture in the other grammar.
   *
   * `instruction` is the core's own index over all instructions — the handle
   * `set_base_image` and `replace_args` already take — and it is the ONLY thing
   * the pane sends: which piece of a `FROM` moves has one answer (the tag), and
   * for anything else the body must be a single `KEY=value`. Both are the
   * core's to decide, and this file parses no Dockerfile.
   */
  | { type: 'openExtractArg'; instruction: number; name?: string }
  /** The reader closed the block without choosing it. Nothing was staged. */
  | { type: 'closeExtractArg'; instruction: number }
  /** Stage the move the reader has just been shown. `Save to <file>` writes it. */
  | { type: 'stageExtractArg'; instruction: number; name: string }
  | { type: 'addEntry'; path: string; value: string }
  /**
   * Add a key to a FREE-FORM mapping — `environment`, `labels`, `build.args`.
   *
   * The gap this closes: a free-form mapping has no `available, not set` list,
   * because the specification names none of its keys — and that list was the
   * only route this pane offered to a key the file does not have. A reader
   * looking at `environment` with three keys had no way to add a fourth.
   *
   * `path` is the MAPPING, never the leaf: the host stages one `insert_key`,
   * the same operation `stageValue` uses for any key the file lacks, so no new
   * engine capability is invented and the diff is one line. Which mappings are
   * free-form is the CORE's answer (`SchemaField.free_form`, generated from
   * `schema/compose-spec.json`); there is no list of them in this extension,
   * any more than there is a list of Compose keys (AD-20).
   *
   * Sent only for the MAPPING form. `environment` written as a list takes
   * `addEntry` instead — the pane adds in whichever form the file uses, and
   * changing one into the other would re-emit the whole collection, which is
   * the one thing this product does not do.
   */
  | { type: 'addKey'; path: string; key: string; value: string }
  /**
   * Stage the removal of ONE entry, addressed by index — story 9.2.
   *
   * Narrow on purpose. DECISIONS.md 20 keeps structural delete out of the UI
   * because deleting a service is destructive and unrecoverable in a way this
   * product cannot afford; one entry of a list the reader is looking at is the
   * counterpart of the add beside it, it is staged rather than written, and it
   * is visible as a one-line diff before `Save to <file>` is pressed.
   */
  | { type: 'removeEntry'; path: string }
  /** Drop one staged edit without writing. */
  | { type: 'unstage'; path: string }
  /** `Save to <file>`. The ONLY message in this protocol that writes bytes. */
  | { type: 'save' }
  /** `Discard`. Drops every stage against the drawn file; writes nothing. */
  | { type: 'discard' }
  /** Open the stage form for a Dockerfile node on the canvas — story 6.3. */
  | { type: 'openDockerfile'; id: string }
  /** Leave the Dockerfile view. The graph comes back with its selection. */
  | { type: 'backToStack' }
  /** Stage a new base image for a build stage — story 6.2. */
  | { type: 'editStage'; stage: number; value: string }
  /** Stage a rewrite of a single-line instruction's body — story 6.2. */
  | { type: 'editInstruction'; instruction: number; value: string }
  /**
   * Stage a NEW instruction at the end of a stage — story 7.6, sent when the
   * reader presses Enter in an `Available here` field.
   *
   * `text` is the whole instruction line as they typed it. The webview does not
   * split it, case it, or decide where it goes: the stage index is the position
   * and the core's engine owns the rest (DECISIONS.md 20).
   */
  | { type: 'addInstruction'; stage: number; text: string }
  /**
   * Stage a new build stage — story 7.7, the `+ add stage` control the design
   * has shown since it was agreed. An empty `name` means no `AS` clause, and
   * nothing is invented.
   */
  | { type: 'addStage'; image: string; name: string }
  /**
   * Ask the core for a node's blast radius — story 4.4's focus mode. The
   * webview never derives one; it asks, dims what comes back, and says so if
   * the answer does not arrive.
   */
  | { type: 'impact'; id: string }
  /** The reader dragged the divider. View state, per workspace. */
  | { type: 'split'; ratio: number }
  /**
   * The reader turned a profile on or off — story 4.6, R1.4.
   *
   * The whole set travels rather than a delta, so the host never has to
   * reconstruct what the toolbar believes. The host stores it as view state
   * (never in a file) and re-asks the core: `stack/topology`, `stack/diagnose`
   * and `stack/impact` all get this set, because the picture, the problems and
   * the blast radius must never be computed for three different profile sets.
   */
  | { type: 'setProfiles'; profiles: string[] }
  /**
   * Declare something the file does not have yet — stories 7.3 and 7.4.
   *
   * One message for all five kinds, because they are one code path in the core
   * too: a kind's top-level block is its name plus `s`, and a switch here would
   * be the fourth place the resource kinds are enumerated. `value` is a
   * service's image and is empty for everything else.
   *
   * Nothing here decides where the declaration goes, whether the name is free,
   * or whether the value needs quoting. The webview sends what the reader
   * typed; `stack/add` plans it and refuses what it will not write.
   */
  | { type: 'add'; kind: AddKind; name: string; value: string }
  /**
   * Find an image on Docker Hub by name — Epic 8.
   *
   * The ONLY message in this protocol that causes a network request, and it is
   * sent solely because a reader typed into a search box. Nothing draws one of
   * these; the upgrade pill's lookup is started by the HOST after the pane is
   * already on screen, and the webview never asks for it.
   *
   * `token` comes back on the answer so a stale reply is dropped.
   */
  | { type: 'searchImage'; token: number; query: string };

/**
 * What can be declared. It mirrors `edit.AddKinds` in `internal/edit/add.go`,
 * and `host/staging.test.ts` reads that file and fails if the two lists drift —
 * a kind this list carries and the core does not is a control that refuses
 * whatever the reader types into it.
 */
export type AddKind = 'service' | 'network' | 'volume' | 'config' | 'secret';

/** The kinds, as data, in the order the core lists them. */
export const ADD_KINDS: AddKind[] = ['service', 'network', 'volume', 'config', 'secret'];

/* ---------------------------------------------------------------------------
 * The write path — Epic 6.
 *
 * These mirror internal/edit's wire schema field for field, in its snake_case,
 * and are deliberately NOT re-cased. The set of operations is closed on both
 * sides because it is closed in the engine: a request is something the splice
 * engine can execute, never prose it has to interpret.
 * ------------------------------------------------------------------------ */

export type EditOperation =
  | 'replace_scalar'
  | 'insert_key'
  | 'delete_key'
  /** Story 9.2: `- value` appended to the block sequence at `at`. */
  | 'insert_sequence_entry'
  /** Story 9.1: the comment at (`at`, `where`), written or replaced whole. */
  | 'set_comment'
  /** Story 9.1: the comment at (`at`, `where`), removed. Refuses when none. */
  | 'delete_comment'
  | 'set_base_image'
  | 'replace_args'
  /** Story 7.6: an instruction added at the end of the stage `stage` names. */
  | 'insert_instruction'
  /** Story 7.7: a `FROM value AS key` appended after the file's last instruction. */
  | 'insert_stage';

/**
 * The byte range an edit was staged against, as it stood when the reader asked
 * for it.
 *
 * This is AD-19's whole mechanism. The core recomputes the range at write time
 * and refuses if it has moved — a stale range is DISCARDED, never rebased,
 * because rebasing means guessing about a file the reader has since changed.
 */
export interface EditExpect {
  start: number;
  end: number;
  text: string;
}

export interface EditOp {
  operation: EditOperation;
  /** Config path. For `insert_key`, the MAPPING the key is added to. */
  at?: string;
  key?: string;
  value?: string;
  /**
   * Zero-based index over FROM instructions, for `set_base_image` and for
   * `insert_instruction` — which stage the new instruction joins.
   */
  stage?: number;
  /** Zero-based index over all instructions, for `replace_args`. */
  instruction?: number;
  /** Which comment, for `set_comment` and `delete_comment`. */
  where?: CommentWhere;
  expect?: EditExpect;
}

/**
 * Where a comment sits relative to the key that owns it — story 9.1,
 * DECISIONS.md 23.
 *
 * Exactly two values, and there is no third. `above` is the contiguous run of
 * comment lines directly above the key at the key's own indent, which is one
 * comment however many lines it spans; `trailing` is the `#…` after the value
 * on the key's own line. A comment that belongs to no key has no path to be
 * addressed by, and the only address available for one is a line number — an
 * address that moves the instant anything above it is edited, which is the
 * silent rebase this product refuses everywhere else.
 */
export type CommentWhere = 'above' | 'trailing';

/** The positions, as data, in the order the pane offers them. */
export const COMMENT_POSITIONS: CommentWhere[] = ['above', 'trailing'];

/**
 * How a staged comment is keyed — `(path, position)` and nothing else.
 *
 * It lives here rather than beside the other staging keys because BOTH sides
 * need it: the host keys the stage by it and the webview names its fields by
 * it, and a second spelling would mean a field that says "staged" about
 * something else's stage.
 */
export function commentKey(path: string, where: CommentWhere): string {
  return `comment:${where}:${path}`;
}

/** What the file says at each of a key's two comment positions — story 9.1. */
export interface CommentsAt {
  path: string;
  /** The comment run above the key, markers and indent stripped. Null: none. */
  above: string | null;
  /** The comment after the value on the key's own line. Null: none. */
  trailing: string | null;
  /** The positions with a staged edit against them — shown as staged, not written. */
  staged: CommentWhere[];
  /**
   * Positions the engine will not write here, with its own sentence.
   *
   * A block scalar, a flow collection and an alias cannot carry a trailing
   * comment without the engine guessing where the value ends, and it refuses
   * rather than guess. The pane offers no field there and says why — offering
   * an empty box that is refused after the reader has typed in it is the
   * silent failure rule 6 forbids, arriving one gesture later.
   */
  unavailable?: { where: CommentWhere; detail: string }[];
}

export interface EditByteRange {
  start: number;
  end: number;
  line: number;
}

export interface EditOpResult {
  operation: EditOperation;
  path?: string;
  range: EditByteRange;
  /** What the range held before the edit — what an `expect` is recorded from. */
  before: string;
  describe: string;
}

/** What `stack/preview` and `stack/apply` return. */
export interface EditResult {
  file: string;
  ops: EditOpResult[];
  diff: string;
  added: number;
  removed: number;
  changed_lines: number;
  /** False for every preview. True only for an apply that reached the disk. */
  written: boolean;
}

/**
 * What a move-into-a-variable would do — story 9.3, DECISIONS.md 25.
 *
 * Mirrors `edit.ExtractResult` field for field, in its snake_case. BOTH halves
 * travel, always: the operation writes two files, and a preview that showed one
 * diff would be a lie about the half the reader cannot see.
 */
/**
 * The `.env` half's staleness assertion — story 9.6, `edit.ExpectVar`.
 *
 * A different SHAPE from `EditExpect` on purpose. The `.env` edit is one
 * appended line and has no byte range to compare, so the assertion is about the
 * VARIABLE: was it defined when the preview was computed, and if so as what. A
 * byte-range field copied from the single-file contract would compare nothing
 * and pass against any file at all.
 */
export interface EditExpectVar {
  defined: boolean;
  value?: string;
}

export interface ExtractResult {
  name: string;
  value: string;
  /** The compose half — an ordinary `replace_scalar` result. */
  compose: EditResult;
  /** The `.env` in the directory of the compose file, never an `env_file`. */
  env_file: string;
  env_diff?: string;
  env_line?: string;
  /** True when this operation would create the `.env` rather than append to it. */
  env_created: boolean;
  /** True when the name is already there with this value: the `.env` is untouched. */
  env_unchanged: boolean;
  /**
   * The `.env` half AS IT STANDS at preview time — the assertion the apply
   * sends back. Optional on the wire (revision 9 is additive), so a core older
   * than the field simply omits it and the apply asserts the compose half only.
   */
  env_expect?: EditExpectVar;
  written: boolean;
}

/**
 * What moving a Dockerfile literal into a build argument would do — story 9.4,
 * DECISIONS.md 27.
 *
 * Mirrors `edit.ExtractArgResult` field for field, in its snake_case. ONE file,
 * which is the sharpest contrast with the compose half above and the reason the
 * two are separate shapes rather than one with optional halves.
 *
 * `scope` and `scope_reason` are not decoration. An `ARG` used before it is
 * declared expands to the empty string with no error, a `FROM` can only use one
 * declared before the FIRST `FROM`, and a global `ARG` is invisible inside a
 * stage until it is re-declared there — so placement is the correctness
 * condition of the whole operation, and a placement rule the reader cannot see
 * is one they cannot check.
 */
export interface ExtractArgResult {
  name: string;
  value: string;
  /** The ordinary edit result: the substitution and the declaration, one diff. */
  dockerfile: EditResult;
  /** `global` or `stage N` — where the declaration landed. */
  scope: string;
  /** Why it could not be anywhere else, in the core's own sentence. */
  scope_reason: string;
  /** The declaration written, without its line ending. Absent when already there. */
  arg_line?: string;
  /** A new declaration carrying the default. */
  declared: boolean;
  /** A bare `ARG NAME`, pulling a global default into a stage. */
  redeclared: boolean;
  /** The idempotent case: only the substitution was written. */
  already_declared: boolean;
  /**
   * What to write in the compose file to feed this argument.
   *
   * `build.args` is deliberately not wired (DECISIONS.md 27), so the absence is
   * a sentence rather than a gap — an `ARG` nothing supplies is what losing it
   * leaves the reader with.
   */
  compose_note: string;
  written: boolean;
}

/* ---------------------------------------------------------------------------
 * The Dockerfile stage form — story 6.2.
 *
 * One group per build stage, instructions in order. No canvas: a Dockerfile is
 * a linear list and a graph over it adds nothing (R5.6).
 * ------------------------------------------------------------------------ */

export interface DockerInstruction {
  /** Position in the parsed instruction list — the handle an edit uses. */
  index: number;
  kind: 'instruction' | 'comment' | 'blank' | 'directive';
  name?: string;
  name_raw?: string;
  flags?: string[];
  args?: string;
  /** The instruction's source bytes, verbatim, continuations included. */
  text: string;
  start_line: number;
  end_line: number;
  /** False for anything the engine will not rewrite in place (R7.4). */
  editable: boolean;
  /** Why not, in one sentence, when `editable` is false. */
  not_editable?: string;
  heredoc?: boolean;
  image_ref?: string;
  image_start?: number;
  image_end?: number;
  stage_name?: string;
  platform?: string;
}

/**
 * One instruction the Dockerfile grammar permits, and what this scope says
 * about it — story 7.8, and AD-20's differentiator in the other grammar.
 *
 * Field for field the Go struct's json tags
 * (`internal/dockerfile/vocabulary.go`), and deliberately the same
 * declared/count split `SchemaField` carries, so one component renders both.
 * NO list of Dockerfile instruction names exists anywhere under `extension/`:
 * a second vocabulary in TypeScript is a second answer about a grammar whose
 * every quirk is a way to corrupt a file.
 */
export interface DockerVocabularyEntry {
  name: string;
  summary: string;
  flags?: string[];
  heredoc?: boolean;
  stage?: boolean;
  before_stage?: boolean;
  deprecated?: boolean;
  deprecated_note?: string;
  /** False means this scope does not use it: `available, not set`. */
  declared: boolean;
  /** How many times — "RUN appears eleven times" is the fact a reader wants. */
  uses: number;
  /** Where those uses are, so nothing in the webview has to search for them. */
  indices?: number[];
}

export interface DockerVocabulary {
  scope: 'file' | 'stage';
  declared_count: number;
  available_count: number;
  /** Every instruction the grammar permits, declared ones first. */
  instructions: DockerVocabularyEntry[];
  /** Names the file uses that the core does not recognise. Never dropped. */
  unknown?: string[];
}

export interface DockerStage {
  index: number;
  name: string;
  /** The AS name if there is one, otherwise the image. Never "stage 0". */
  label: string;
  image_ref: string;
  platform?: string;
  from: DockerInstruction;
  instructions: DockerInstruction[];
  /** What this stage uses, and therefore what it does not — story 7.8. */
  vocabulary: DockerVocabulary;
}

export interface DockerfileForm {
  path: string;
  /** True when the build names a file that is not there (story 6.3). */
  missing: boolean;
  context?: string;
  dockerfile?: string;
  escape_char: string;
  crlf: boolean;
  bom: boolean;
  directives: DockerInstruction[];
  stages: DockerStage[];
  /** Anything before the first FROM that is not a directive — the ARGs. */
  preamble: DockerInstruction[];
  /** The whole file's split, same shape as a stage's. */
  vocabulary: DockerVocabulary;
}

/* ---------------------------------------------------------------------------
 * The inspector — Epic 5.
 *
 * Everything below mirrors a Go wire schema field for field, in its snake_case,
 * and is deliberately NOT re-cased on the way in. A renamed struct tag compiles
 * clean on both sides and draws an empty pane, which looks exactly like a
 * service with nothing set; `cmd/composure/schema_test.go` asserts these literal
 * names on the Go side and `host/realcore.test.ts` spans the gap.
 * ------------------------------------------------------------------------ */

/** What a value the file declares was replaced by during merge. */
export interface Override {
  value: string;
  origin: Origin;
}

/**
 * A declared value, shaped for display: what the file says, where it says it,
 * and — for a scalar holding `${VAR}` — what that resolves to.
 *
 * Both halves of an interpolation travel. Story 5.1: the literal is what the
 * file says and stays in `text`; `resolved` is what it means, shown beneath.
 */
export interface ValueView {
  kind: 'scalar' | 'sequence' | 'mapping' | 'null' | 'alias';
  /** The scalar exactly as written. Empty for a sequence or a mapping. */
  text: string;
  /** Present only when `text` contains a `${VAR}` reference. */
  resolved?: string;
  /** Variables nothing defines. Meaningless unless `env_known`. */
  undefined?: string[];
  /** False when no environment could be established: say nothing, not "undefined". */
  env_known: boolean;
  origin: Origin;
  overrides: Override[];
  /** The anchor this value came from, and where the `*name` was written. */
  alias?: string;
  alias_site?: Origin;
  seq?: ValueView[];
  entries?: ValueEntry[];
}

export interface ValueEntry {
  key: string;
  key_origin: Origin;
  /** The config path of this entry — the join key for a finding. */
  path: string;
  value: ValueView;
}

/**
 * Why a value cannot be edited where it is read — `stack/editable`'s closed set
 * of slugs, mirroring the constants in `internal/edit/inherited.go`.
 *
 * A resolved value does not have to live at the path it is read from, and until
 * this existed the pane could not tell. It offered a field for
 * `services.web.restart` on a service whose file says no such thing — the value
 * arrives through `<<: *defaults` — and the engine answered `path ... not
 * found` after the reader had typed in it.
 */
export const EDITABILITY_REASONS = [
  /* The mapping does not declare this key; a `<<:` merge key supplies it.
     Editable by writing it HERE, which overrides the anchor for this place
     alone — decision 21. */
  'inherited',
  /* An ANCESTOR of this key is inherited. YAML replaces a merged key whole, so
     writing this one locally would drop its siblings. No override to offer. */
  'inherited-nested',
  /* The bytes at this path are `*name` — a reference, not a value. */
  'alias',
  /* The bytes at this path carry `&name`, which other places reference. */
  'anchor',
  /* A `|` or `>` scalar, whose value is the lines below it. */
  'block-scalar',
  /* The mapping a key would be added to is written in flow style. */
  'flow-style',
  /* The file simply does not have the key. The ordinary insert. */
  'absent',
  /* A list index past the end of the list. Story 9.2: the sentence says how
     many entries the list actually has, and there is NO plan — `insert_key`
     with the key `9` on a sequence adds nothing at all, and offering it was a
     confident wrong answer with an invitation attached. */
  'entry-index',
  /* A mapping or a sequence: no scalar to replace. */
  'not-scalar',
  /* `key:` with nothing after it — a delete and an insert. */
  'null-value',
  /* The document does not parse, so nothing may be claimed about it. */
  'unreadable',
] as const;

export type EditabilityReason = (typeof EDITABILITY_REASONS)[number];

/**
 * Where one value is actually written, and what an edit at that path would do.
 *
 * `detail` is the sentence the pane renders AT the field, before any write.
 * CLAUDE.md rule 6: a field that cannot be edited and does not say why is a
 * silent failure, and the reader must not discover the consequence in the diff.
 */
export interface Availability {
  path: string;
  /** True only for a scalar with real bytes at this exact path. */
  editable: boolean;
  reason?: EditabilityReason;
  /** One sentence for a reader, naming the anchor or mapping responsible. */
  detail?: string;
  /** The operation that WOULD carry the reader's intent, when one exists. */
  plan?: string;
  anchor?: string;
  /** Where the `<<: *defaults` or `*name` that pulled the value in is written. */
  through?: { line: number; column: number };
  /** Where the value's bytes actually are. */
  bytes_at?: { line: number; column: number };
}

/** AD-21's mark. `unknown` is the default and means nothing is claimed. */
export type Support = 'unknown' | 'yes' | 'no';

/**
 * One key the Compose specification permits at a config path, and what the file
 * does or does not say about it.
 *
 * `declared: false` is the differentiator — `available, not set`. It is
 * GENERATED from `schema/compose-spec.json` (AD-20); no list of Compose keys
 * exists anywhere in this extension, and adding one would be the defect.
 */
export interface SchemaField {
  key: string;
  type?: string;
  description?: string;
  default?: string;
  default_source?: string;
  deprecated?: boolean;
  min_version?: string;
  min_version_note?: string;
  support: Support;
  declared: boolean;
  /** The config path this field addresses, e.g. `services.db.healthcheck`. */
  path: string;
  value?: ValueView;
  /** The fields of a declared mapping, so a group carries its own unset list. */
  children?: SchemaField[];
  /** A mapping whose keys the schema does not name: `environment`, `labels`. */
  free_form?: boolean;
  /**
   * The values the specification NAMES for this key, in the specification's own
   * order — story 7.9. Absent when it names none.
   *
   * Not the values it PERMITS: `allowed_source` says which of the three forms
   * it was read from and only `schema` (a JSON Schema `enum` or `const`) is
   * closed. `pattern` dropped the arms that were not literals and `description`
   * read the specification's own "Options include: 'no', …" prose, and a file
   * may legitimately hold a value neither of those lists — an interpolated
   * `${RESTART_POLICY}`, or a word from a Compose newer than the vendored
   * schema.
   *
   * That is why the control built on this is an `<input>` bound to a
   * `<datalist>` and never a `<select>`. A select cannot hold a value that is
   * not one of its options, and the values that matter most here are exactly
   * the ones the specification does not know about.
   *
   * AD-20: there is no list of Compose values anywhere in this extension, any
   * more than there is a list of Compose keys. Both are generated from
   * `schema/compose-spec.json` at runtime by `internal/schema`.
   */
  allowed?: string[];
  /**
   * Which of the four forms the list was read from, and — the part that matters
   * — whether it is CLOSED. Only `schema` is.
   *
   * `schema-branch` is an `enum` that covers some of the forms the key accepts
   * and not the others: `gpus` is `"all"`, or a list of GPU device objects, so
   * `all` is the whole truth about the string form and says nothing about the
   * list form. It is rendered with the open wording, because a reader shown a
   * closed list that is not closed will believe the tool over the file.
   */
  allowed_source?: 'schema' | 'schema-branch' | 'pattern' | 'description';
}

export interface SchemaNode {
  path: string;
  schema: string;
  known: boolean;
  title?: string;
  fields: SchemaField[];
  declared_count: number;
  available_count: number;
  free_form?: boolean;
  missing?: boolean;
}

export interface SourceFile {
  path: string;
  step: number;
}

/** The whole `stack/schema` answer. */
export interface StackSchema {
  path: string;
  schema_commit: string;
  compose_version: string;
  compose_version_known: boolean;
  files: SourceFile[];
  profiles: string[];
  /** The obsolete top-level `version:`, reported and never acted on (AD-20). */
  version_field?: string;
  node?: SchemaNode;
  nodes?: SchemaNode[];
}

export type Severity = 'hint' | 'warning' | 'error';

export interface FindingAnchor {
  label: string;
  path: string;
  origin: Origin;
}

/**
 * The half of a described fix that is NOT a splice in one file — story 9.5,
 * DECISIONS.md 26.
 *
 * It carries NO byte range, and that is the design rather than an omission: the
 * `.env` line does not exist until it is written, so there is nothing for the
 * core's `disowned` guard to check it against, and a range that cannot be
 * checked is the unverifiable claim that guard exists to stop. See the core's
 * `diagnose.Remedy`.
 *
 * It is data. Rendering it stages nothing (DECISIONS.md 17) — the reader runs
 * `command`, or does not.
 */
export interface FindingRemedy {
  /** `extract`, today the only one. */
  operation: string;
  /** The config path — the SAME address the fix names. */
  at: string;
  /** The variable the operation would define. */
  name: string;
  /** The `.env` it would write. */
  file: string;
  /** The exact headless invocation. */
  command: string;
  describe: string;
}

/**
 * The edit that would resolve a finding, as `stack/diagnose` describes it.
 *
 * Typed here rather than left off the interface because the core has always
 * sent it and the panel has always dropped it on the floor; a field this client
 * cannot name is a field it cannot decide to render.
 */
export interface FindingFix {
  operation: string;
  path: string;
  file: string;
  range: { start: number; end: number; line: number };
  key?: string;
  value?: string;
  needs_value?: boolean;
  describe: string;
  remedy?: FindingRemedy;
}

/** One thing a rule found, exactly as `stack/diagnose` returns it. */
export interface Finding {
  rule: string;
  severity: Severity;
  title: string;
  message: string;
  subjects: string[];
  anchors: FindingAnchor[];
  fix?: FindingFix;
  no_fix?: string;
  /** Why a remedy a rule OFFERED was dropped by the core's own guard. */
  no_remedy?: string;
}

export interface DiagnoseReport {
  path: string;
  profiles: string[];
  findings: Finding[];
}

/** Findings per severity on one graph node, for the badge (story 5.4). */
export interface SeverityCount {
  error: number;
  warning: number;
  hint: number;
}

/**
 * What the inspector needs to render one selection.
 *
 * Assembled by the host from `stack/schema` and `stack/diagnose`, never
 * derived: the fields come from Go and the findings come from Go, and the host
 * only joins them by the config path both already carry.
 */
export interface Inspection {
  /** The selected node's config path, or null for the stack itself. */
  id: string | null;
  /** The node's display name — the service name, or the file for the stack. */
  name: string;
  kind: NodeKind | 'stack';
  schema: StackSchema;
  /** Findings that anchor anywhere inside this node. */
  findings: Finding[];
  /** Config paths the reader has staged an edit against. Nothing is written. */
  staged: string[];
  /**
   * Config paths the reader has OPENED but not yet staged — an unset key they
   * clicked and have not typed a value into.
   *
   * Separate from `staged` on purpose. An opened key is rendered as a field
   * with the cursor in it and contributes NOTHING to the pending diff, because
   * the reader has not said what it should say. This is the distinction that
   * makes "clicking an unset key gives you somewhere to type" possible without
   * writing a value the schema never supplied.
   */
  opened: string[];
  /**
   * The staged value for each path in `staged` that carries one.
   *
   * The field shows this rather than what the file says, with the file's own
   * value stated beneath it. Showing the file's value in a field the reader has
   * just typed into would look like the edit was lost; showing the staged value
   * with nothing said would look like the file had already been written. Both
   * halves travel, the way an interpolated `${VAR}` does.
   */
  pending: Record<string, string>;
  /**
   * Where each rendered path is actually WRITTEN, keyed by config path — the
   * core's answer from `stack/editable`, never re-derived here.
   *
   * A path with no entry is one nothing asked about, which the pane treats as
   * "ordinary": silence is not a claim that a field is fine, it is the absence
   * of an answer, and the engine still refuses anything it cannot do.
   *
   * Optional for the same reason: the request that fills it can fail while the
   * inspection succeeds, and an inspector that refused to render because it
   * could not fetch an explanation would leave the reader with neither.
   */
  availability?: Record<string, Availability>;
}
