// The inspector — the entire right pane, and the product's wedge.
//
// Four things it does, and each is a story:
//
//  5.1  every declared value is shown WITH its value, never a bare key and
//       never a count in place of what the keys say;
//  5.2  every key the schema permits here and the file does not declare is
//       listed, always visible, never behind a "show more";
//  5.3  provenance sits under each value, always visible, never a tooltip;
//  5.4  a finding sits on the field that caused it, with its severity in text.
//
// It computes nothing about the stack. The fields arrive from `stack/schema`,
// generated from the vendored Compose specification (AD-20), and the findings
// arrive from `stack/diagnose`. This file turns them into DOM.
//
// Two rules from DESIGN.md are load-bearing here and are worth restating
// because breaking either is invisible in review:
//
//  * Monospace means "this is literally in your file". Anything rendered in
//    the code face exists verbatim in the YAML; prose about the file is in the
//    UI face. The `mono` class is not decoration.
//  * Every colour is a `--vscode-*` token, and there is no colour in this file
//    at all — severity is a class name, and the stylesheet resolves it.
//
// The arithmetic and the wording are exported as pure functions so the tests
// can reach them; a webview cannot be launched under `node --test`.

import type {
  Availability,
  CommentsAt,
  CommentWhere,
  ExtractResult,
  Finding,
  ImageLookup,
  ImageSearchAnswer,
  Inspection,
  SchemaField,
  Severity,
  ValueView,
  WebviewMessage,
} from '../shared/protocol';
import { ADD_KINDS, COMMENT_POSITIONS, commentKey, entryPath } from '../shared/protocol';
import { findingKey, findingsForField, findingsForNode, positionCount } from '../shared/join';
import { groupFields } from './groups';
import { Combo } from './combo';
import { valueInput } from './stageform';
import { diffBodyLines, diffLineKind } from './pending';
import { ImageSearch } from './imagesearch';
import { ageNote, stateNote, upgradePill } from './imagepill';

/** How deep a declared value is rendered before it stops descending. */
const MAX_VALUE_DEPTH = 6;

/**
 * The top-level blocks `+ add` already declares into.
 *
 * `services`, `networks`, `volumes`, `configs` and `secrets` are free-form —
 * any name is legal — so the add-a-key composer would appear on all five, and
 * it must not. `+ add` is the gesture for those, and it is a BETTER one: it
 * asks the core where the declaration goes, refuses a name the stack already
 * carries, refuses a name that would not survive as a bare key, and refuses a
 * service with no image. A two-field composer beside it offers to write `foo:`
 * with nothing under it, which `edit.Plan` explicitly calls "not a stack
 * anything can run" — the same act, with the checks taken off.
 *
 * DERIVED from `ADD_KINDS`, which mirrors `edit.AddKinds`, plus the core's own
 * `Add.Block()` rule that a kind's block is its name plus `s`. It is not a list
 * of Compose keys — there is none of those in this extension (AD-20) — it is
 * the list of things this pane already has another affordance for, taken from
 * the constant that affordance is itself built from.
 */
const ADD_BLOCKS = new Set(ADD_KINDS.map((kind) => `${kind}s`));

/**
 * One nesting step, in pixels.
 *
 * It is here rather than only in the stylesheet because the key column has to
 * shrink by exactly the same amount the row is pushed right — see `--ind` in
 * style.css. The two numbers are one number, so they are written once.
 */
export const INDENT_STEP = 16;

/**
 * The longest run of joined list values that still reads as a value rather
 * than as a paragraph. Past it the sequence keeps one row per item.
 */
export const INLINE_LIST_MAX = 72;

/* -------------------------------------------------------------------------
 * Pure functions. Every one of these is a sentence the reader will read, or a
 * decision about what they see; all of them are tested.
 * ---------------------------------------------------------------------- */

/** The file named the way a reader names it: the last two segments. */
export function shortFile(file: string): string {
  const parts = file.replace(/\\/g, '/').split('/');
  return parts.slice(-2).join('/');
}

/**
 * The provenance line beneath a value — story 5.3.
 *
 * `compose.yml:12` when the value was set once, and
 * `compose.yml:12 · overrides :7` when it replaced something. The overridden
 * position is rendered as a bare `:7` when it is in the same file, because
 * repeating the filename in a 40%-wide pane costs the line its readability and
 * says nothing.
 */
export interface Provenance {
  /** `compose.yml:12`, and where clicking it goes. */
  label: string;
  file: string;
  line: number;
  column: number;
  /** What this value replaced, oldest first. Empty when nothing was replaced. */
  overrides: { label: string; file: string; line: number; column: number }[];
  /** True when more than one value was replaced — DESIGN.md colours that. */
  many: boolean;
}

export function provenanceOf(value: ValueView): Provenance | null {
  const origin = value.origin;
  if (!origin || !origin.file || origin.line < 1) {
    return null;
  }
  const overrides = (value.overrides ?? [])
    .filter((o) => o.origin.file !== '' && o.origin.line >= 1)
    .map((o) => ({
      label: o.origin.file === origin.file ? `:${o.origin.line}` : `${shortFile(o.origin.file)}:${o.origin.line}`,
      file: o.origin.file,
      line: o.origin.line,
      column: o.origin.column,
    }));
  return {
    label: `${shortFile(origin.file)}:${origin.line}`,
    file: origin.file,
    line: origin.line,
    column: origin.column,
    overrides,
    many: overrides.length > 1,
  };
}

/**
 * A short sequence of scalars, as the values themselves.
 *
 * What this replaces: `networks: [shipyard]` rendered as a row reading
 * `networks` `list`, then an indented row reading `[0]` `shipyard`, then two
 * provenance lines — four lines and two words the file does not contain
 * (`list`, `[0]`) to say `shipyard`. The mockup prints one row. The type word
 * and the index are noise precisely when the entries are right there.
 *
 * Returns the items when they can be shown on the parent's own row, and null
 * when the tree has to stay. It refuses in every case where collapsing would
 * DROP something the tree was showing:
 *
 *   a non-scalar item        it has entries of its own to draw;
 *   an alias                 `*name` is a reference and carries its own site;
 *   an unresolved variable   it needs its own warning ink and its own
 *                            resolution sentence under it;
 *   a multi-line scalar      a block scalar is not a row;
 *   an item whose provenance says more than the parent's — a different
 *                            file:line, or an override the parent has not got;
 *   more than `INLINE_LIST_MAX` characters of value.
 *
 * So a collapsed list never costs the reader a fact. Everything else stays a
 * tree, unchanged.
 */
export function inlineList(value: ValueView): ValueView[] | null {
  if (value.kind !== 'sequence') {
    return null;
  }
  const items = value.seq ?? [];
  if (items.length === 0) {
    return null;
  }
  const own = provenanceOf(value);
  for (const item of items) {
    if (item.kind !== 'scalar' || item.alias || item.text.includes('\n')) {
      return null;
    }
    if (item.undefined && item.undefined.length > 0) {
      return null;
    }
    const prov = provenanceOf(item);
    if (prov && (own === null || prov.label !== own.label || prov.overrides.length > 0)) {
      return null;
    }
  }
  const width = items.reduce((n, i) => n + i.text.length, 0) + (items.length - 1) * 3;
  return width <= INLINE_LIST_MAX ? items : null;
}

/**
 * Severity as a word — N6.
 *
 * The colour is a duplicate of this, never a substitute for it. A reader with
 * a red-green deficiency, a monochrome theme or a screen reader gets the same
 * information as everyone else because it is written down.
 */
export function severityWord(severity: Severity): string {
  switch (severity) {
    case 'error':
      return 'error';
    case 'warning':
      return 'warning';
    default:
      return 'hint';
  }
}

/**
 * What the pill READS — the rule, not the severity word.
 *
 * The owner's decision, 2026-08-13. The pill used to read `hint`, which every
 * hint in the pane also read, so it told the reader the one thing the colour
 * already told them and nothing about WHICH rule fired. The mockup reads
 * `plaintext`. Both now travel: the rule is the visible text, and the severity
 * is in the colour, in the accessible name and in the tooltip — so story 4.5's
 * floor is intact, since the severity is still carried in text (`pillName`) and
 * not in colour alone.
 *
 * A finding with no rule falls back to the severity word rather than rendering
 * an empty pill. An empty pill passes every colour check there is and says
 * nothing at all, which is the exact failure this file's header names.
 */
export function pillText(finding: Finding): string {
  return finding.rule === '' ? severityWord(finding.severity) : finding.rule;
}

/**
 * The pill's accessible name — N6's carrier, and the reason the visible text is
 * allowed to be the rule.
 *
 * A screen reader announces this instead of the pill's text, so the severity
 * word has to be in here: it is the only place a reader who cannot see the
 * colour gets it.
 */
export function pillName(finding: Finding): string {
  return `${severityWord(finding.severity)}: ${pillText(finding)}`;
}

/** The pill's tooltip: severity, rule, and the rule's own title. */
export function pillTitle(finding: Finding): string {
  const name = pillName(finding);
  return finding.title === '' || finding.title === finding.rule ? name : `${name} — ${finding.title}`;
}

/**
 * The value a click may WRITE, and the empty string when there is none.
 *
 * The Compose specification records defaults two ways and the core reports
 * which one (`default_source`): `schema` means the specification carried a real
 * `"default":` for the key, and `description` means a regular expression found
 * something that reads like a default in the key's PROSE. The second is a
 * description, not a value — `healthcheck.start_interval` has the prose default
 * "interval value", and one click used to stage `start_interval: interval
 * value`, which is not a duration and would not run.
 *
 * So a prose default is shown and never written. The rule lives in one place:
 * nothing in this extension may put `field.default` into an edit without coming
 * through here.
 */
export function defaultValue(field: SchemaField): string {
  if (field.default_source !== 'schema') {
    return '';
  }
  return field.default ?? '';
}

/**
 * Whether a default is a value the reader could accept, or prose about one.
 *
 * Kept separate from `defaultValue` because the two answer different questions:
 * this decides how the placeholder is WORDED, that decides what a commit writes.
 */
export function defaultIsProse(field: SchemaField): boolean {
  return field.default !== undefined && field.default !== '' && field.default_source !== 'schema';
}

/**
 * What an unset field shows in place of a value.
 *
 * "not set" — not "none configured", not an em dash, not an empty box
 * (EXPERIENCE.md's voice). Where the specification records a default it is
 * shown too, because the reader learning what happens if they leave the key
 * alone is the entire teaching mechanism.
 *
 * A prose default is worded as what it is. "defaults to interval value" reads
 * as something a reader could type; "the spec describes the default as
 * `interval value`" reads as prose, which is what it is.
 */
export function placeholderFor(field: SchemaField): string {
  if (defaultIsProse(field)) {
    return `not set — the spec describes the default as “${field.default}”`;
  }
  const value = defaultValue(field);
  if (value !== '') {
    return `not set — defaults to ${value}`;
  }
  return 'not set';
}

/**
 * The values the specification names for a key — story 7.9. Empty when none.
 *
 * There is no list of Compose values in this file, or anywhere under
 * `extension/`. This reads what `internal/schema` derived from
 * `schema/compose-spec.json` at runtime, exactly as the `available, not set`
 * list does for keys (AD-20).
 */
export function allowedValues(field: SchemaField): string[] {
  return field.allowed ?? [];
}

/**
 * Whether the offered values are the only ones the specification permits.
 *
 * True for exactly one of the four sources. The Compose specification records
 * a fixed set as a JSON Schema `enum` in nine places in the whole document; the
 * rest — `restart`, `network_mode`, `deploy.mode`, `deploy.endpoint_mode` — are
 * `"type": "string"` with the values written out in English, and `pull_policy`
 * is a regular expression whose last arm is not a literal at all.
 *
 * `schema-branch` is an `enum` too, and it is NOT closed: it covers one of the
 * forms the key accepts and not the others. `gpus` is `"all"` or a list of GPU
 * device objects, and this used to read as "the specification allows nothing
 * else" on the one key in the document where that was false.
 *
 * So the wording differs, and the difference is not cosmetic: a reader shown a
 * closed list that is not closed will believe the tool over the file.
 */
export function allowedIsClosed(field: SchemaField): boolean {
  return field.allowed_source === 'schema';
}

/**
 * The sentence that introduces the values, in the reader's words rather than
 * the wire's slug.
 *
 * It used to introduce a row of links under the field. The links are gone —
 * the owner rejected them — and the sentence moved into the popup, where it
 * heads the list it is about. It is a standalone sentence now rather than a
 * clause ending in a colon, because nothing follows it on the same line.
 */
export function allowedLead(field: SchemaField): string {
  switch (field.allowed_source) {
    case 'schema':
      return 'One of these — the specification allows nothing else.';
    case 'schema-branch':
      // `gpus` — enumerated on one arm of a `oneOf` and not on the other, which
      // is a list of GPU device objects. Naming the other form is the point: a
      // reader offered `all` under "nothing else" would never write the list.
      return 'All the specification names for one of this key’s forms; it accepts other forms too.';
    case 'pattern':
      return 'Named by the specification’s pattern, which permits other forms too.';
    default:
      // `description`, and anything a newer core sends that this build does not
      // recognise. The weaker claim is the safe default: it never tells the
      // reader a list is closed when it may not be.
      return 'Listed in the specification’s prose, not enforced by it.';
  }
}

/**
 * The suffix on the field's accessible name.
 *
 * The popup is not open when the reader arrives on the field, so nothing has
 * announced that a list exists — and the values are no longer printed under the
 * field where they used to be. This sentence is what tells a reader who cannot
 * see the arrow that there is one, and which key opens it.
 */
export function allowedName(field: SchemaField): string {
  const values = allowedValues(field);
  if (values.length === 0) {
    return '';
  }
  return (
    ` ${values.length} values are offered; press the down arrow to list them.` +
    ' Any other value can still be typed.'
  );
}

/**
 * The accessible name of the arrow that opens the list, and of the list itself.
 */
export function allowedToggleName(field: SchemaField, path: string): string {
  return `Show the ${allowedValues(field).length} values the specification names for ${path}`;
}

/**
 * One offered value's accessible name.
 *
 * It repeats the closedness claim, because `aria-activedescendant` means a
 * screen reader announces the OPTION rather than the sentence above it, and a
 * reader told `all` is the only thing `gpus` accepts would never write the list
 * of device objects that is the key's other form.
 */
export function allowedOptionName(field: SchemaField, path: string, value: string): string {
  return allowedIsClosed(field)
    ? `${value} — put it in ${path}. The specification allows nothing else here`
    : `${value} — put it in ${path}. Other values are valid too; this list is what the specification names`;
}

/**
 * The listbox's id for a field, and the stem of each option's id.
 *
 * Derived from the config path, which is already unique per field in a pane —
 * the same join key the resolver, topology and diagnostics use. Non-identifier
 * characters are replaced so the value is a legal `id` and a legal
 * `aria-controls` / `aria-activedescendant` reference on any path the schema
 * can produce.
 */
export function optionListId(path: string): string {
  return `allowed-${path.replace(/[^A-Za-z0-9_-]/g, '_')}`;
}

/**
 * AD-21's mark, in words. Empty when nothing is claimed.
 *
 * Only ever an annotation on a key that is still listed. Hiding it would teach
 * the reader that the key does not exist; this teaches them that it does, and
 * that upgrading would give it to them.
 */
export function supportNote(field: SchemaField): string {
  if (field.support !== 'no' || !field.min_version) {
    return '';
  }
  return `needs Compose ${field.min_version}`;
}

/**
 * Whether a key holds a mapping — the thing you cannot type a value into.
 *
 * `healthcheck` is `type: object`; `restart` is `type: string`. The difference
 * decides what clicking an unset key does: a scalar gets an input with the
 * cursor in it, and an object gets OPENED, showing its own children and their
 * own `available, not set` list, because "type a value for healthcheck" is not
 * a question with an answer.
 */
export function isObjectTyped(field: SchemaField): boolean {
  if (field.children && field.children.length > 0) {
    return true;
  }
  return /\bobject\b/.test(field.type ?? '');
}

/**
 * Whether the file declares this key with no value at all — `healthcheck:` on a
 * line by itself.
 *
 * This is the state the old pane could not render: `scalarValue` excluded
 * `kind: 'null'` from editable and there were no children either, so it showed
 * a read-only `~` and the reader had no route to `test` or `interval` from
 * there. It is also the state a save produced, which is why adding an attribute
 * was a dead end AFTER the write as well as before it.
 */
export function isDeclaredNull(field: SchemaField): boolean {
  return field.declared === true && field.value?.kind === 'null';
}

/**
 * The groups the inspector renders for a NESTED mapping's children.
 *
 * The node's own fields are grouped semantically (see `webview/groups.ts`);
 * this is the simpler split used one level down, inside `healthcheck` or
 * `deploy`, where "image / network / health" would mean nothing. A rendered
 * child that is itself a mapping becomes its own sub-group carrying its own
 * unset list; everything else is a row; and what the file does not declare is
 * the group's `available, not set` line.
 */
export interface FieldGroups {
  /** Keys rendered as a row, in file order. */
  lead: SchemaField[];
  /** Mappings with children, each its own sub-group. */
  groups: SchemaField[];
  /** Every key the schema permits here that the file does not declare. */
  available: SchemaField[];
}

export function groupsOf(
  fields: SchemaField[],
  rendered: (field: SchemaField) => boolean = (f) => f.declared,
): FieldGroups {
  const out: FieldGroups = { lead: [], groups: [], available: [] };
  for (const f of fields) {
    if (!rendered(f)) {
      out.available.push(f);
      continue;
    }
    if (f.children && f.children.length > 0) {
      out.groups.push(f);
      continue;
    }
    out.lead.push(f);
  }
  return out;
}

/**
 * The one-line summary in the inspector's header.
 *
 * Spelled out rather than badged: `6 set · 87 available` and never `6/93`,
 * which is the cryptic-badge anti-pattern the design explicitly names.
 */
export function headerSummary(inspection: Inspection): string {
  const node = inspection.schema.node;
  if (!node) {
    return '';
  }
  const parts = [`${node.declared_count} set`];
  if (node.free_form) {
    // The schema permits any key here, so a count of what is "available" would
    // be an invention. Say which it is.
    parts.push('any key permitted');
  } else if (node.known) {
    parts.push(`${node.available_count} available`);
  } else {
    parts.push('the vendored spec does not describe this');
  }
  return parts.join(' · ');
}

/**
 * The severity count in the inspector's own header — Direction A's pane headers
 * carry one, and this pane had none.
 *
 * Spelled out, and in words as well as colour (N6): `2 warnings`, never a bare
 * amber `2`. Empty when there is nothing to say, so the header never carries a
 * cheerful "0 problems" for a stack nobody managed to check.
 */
export function severitySummary(findings: Finding[]): string {
  const counts = { error: 0, warning: 0, hint: 0 };
  for (const f of findings) {
    counts[f.severity] += 1;
  }
  const parts: string[] = [];
  for (const severity of ['error', 'warning', 'hint'] as const) {
    const n = counts[severity];
    if (n > 0) {
      parts.push(n === 1 ? `1 ${severity}` : `${n} ${severity}s`);
    }
  }
  return parts.join(' · ');
}

/**
 * What that count is counting, and over what — the fix for two honest numbers
 * that contradict each other on screen.
 *
 * The header counts FINDINGS for the SELECTION. VS Code's problems panel gets
 * one entry per POSITION, for the WHOLE STACK. Both are right and they diverge
 * the moment a finding anchors twice: `host-port-collision` names two services
 * and anchors in both, so one finding here is two rows there. The owner read
 * `2 warnings` beside a panel showing three and reasonably concluded one of
 * them was broken.
 *
 * Neither number can be changed into the other — counting anchors here would
 * report one collision as two problems in a pane about one service, and the
 * panel cannot be scoped to a selection it knows nothing about. So each says
 * what it is counting, and this side says it exactly rather than vaguely: the
 * position count is derived from `positionCount`, the same function that
 * decides what `Problems.publish` emits.
 */
export function severityScope(findings: Finding[], name: string): string {
  if (findings.length === 0) {
    return '';
  }
  const found = findings.length === 1 ? '1 finding' : `${findings.length} findings`;
  const positions = positionCount(findings);
  const where = positions === 1 ? '1 position' : `${positions} positions`;
  return (
    `${found} in ${name}, at ${where}. The Problems panel lists one entry per position and ` +
    'covers the whole stack, so its count is the larger one.'
  );
}

/**
 * The line under the header naming which specification the list came from, and
 * what Compose the marks were judged against (AD-20, AD-21).
 *
 * On screen permanently rather than in an about box: the available list is a
 * claim about what is possible, and a claim whose source is not visible is one
 * the reader has to take on trust.
 */
export function sourceLine(inspection: Inspection): string {
  const commit = inspection.schema.schema_commit.slice(0, 7);
  const compose = inspection.schema.compose_version_known
    ? `compose ${inspection.schema.compose_version}`
    : 'no docker compose found — nothing marked unsupported';
  return `spec ${commit} · ${compose}`;
}

/* -------------------------------------------------------------------------
 * The view.
 * ---------------------------------------------------------------------- */

export interface InspectorCallbacks {
  send: (msg: WebviewMessage) => void;
}

/**
 * What a value tree needs in order to put a pill on the row that caused it, and
 * what it reports back about having done so.
 *
 * `claimed` is an out-parameter on purpose: the field header that owns the tree
 * needs to know which findings a row beneath it already showed, and the only
 * source of that answer which cannot drift from the rendering is the rendering.
 */
interface EntryDiagnostics {
  findings: Finding[];
  claimed: Set<string>;
}

/**
 * One group's worth of keys inside the pane's single `available, not set`
 * block. The label is the group the keys belong to, or '' when there is only
 * one run and nothing to distinguish it from.
 */
interface AvailableRun {
  label: string;
  fields: SchemaField[];
}

export class InspectorView {
  readonly element: HTMLElement;
  private readonly heading = el('span', 'pane-file inspector-name mono');
  private readonly summary = el('span', 'inspector-summary');
  private readonly severity = el('span', 'inspector-severity');
  private readonly source = el('span', 'inspector-source');
  private readonly body = el('div', 'inspector-body');
  /**
   * The pending-diff strip is docked at the foot OF THIS PANE (DESIGN.md:184),
   * not at the foot of the panel. It used to be appended to the root, where it
   * spanned the graph as well — a strip reporting what one field would do,
   * drawn under a canvas that has nothing to do with it.
   */
  readonly footer = el('div', 'inspector-footer');
  /** Config paths staged in this render, for the staged marks on keys. */
  private staged = new Set<string>();
  /** Config paths opened for editing but not staged — story 5.2's cursor. */
  private opened = new Set<string>();
  /** Staged values by path, so an edited field shows what the reader typed. */
  private pending: Record<string, string> = {};
  /**
   * Where each value is actually WRITTEN — the core's `stack/editable` answer,
   * keyed by config path.
   *
   * The pane renders a value it RESOLVED, and a resolved value does not have to
   * live at the path it is read from. `restart` on a service whose file says
   * `<<: *defaults` and nothing else is the case that broke: the field was
   * offered, the reader typed in it, and the engine answered `path
   * services.web.restart not found`. Rule 6 — a field that cannot be edited, or
   * that will not be edited where the reader thinks, has to say so AT the
   * field and BEFORE the write.
   *
   * A path with no entry is not a claim that the field is ordinary; it is the
   * absence of an answer, and the field renders exactly as it did before.
   */
  private availability: Record<string, Availability> = {};
  /**
   * The key just staged. The pane is rebuilt wholesale after a stage, which
   * destroys the button that was pressed and drops focus to <body> — a
   * keyboard reader would lose their place in an 87-key list every time they
   * added a field. Restored after the next render.
   */
  private focusAfterRender: string | null = null;
  /**
   * The value combobox built for each path in THIS render pass — story 7.9.
   *
   * The control has two halves that land in two places: an `intercept` hook
   * that has to be handed to `valueInput` at the moment the input is built, and
   * an arrow plus a popup that are appended to the row afterwards. Keyed by
   * path rather than held in a field, because a row's value cell and its
   * combobox are assembled by two different methods and the map is the only
   * thing that makes the join explicit. Cleared at the top of every render, so
   * a control from a previous pane can never be appended to this one.
   */
  private combos = new Map<string, Combo>();
  /**
   * What Docker Hub said about each image value, keyed by config path — Epic 8.
   *
   * It arrives in its own message, AFTER this pane has already rendered, and it
   * may never arrive at all. Held here rather than on the inspection because it
   * is not part of the inspection: every other field on this pane is a fact
   * about the reader's files and this one is a fact about a third party's
   * registry, so it is kept apart and cleared when the file changes.
   */
  private lookups = new Map<string, ImageLookup>();
  /** The last inspection, so a lookup landing can redraw without a refetch. */
  private lastInspection: Inspection | null = null;
  /**
   * Lists the reader has opened out of their collapsed form — story 9.2.
   *
   * View state, and the only kind decision 9 permits: it says which rows are on
   * screen and never what any of them holds. Nothing is staged by expanding a
   * list and nothing is fetched; the entries were already in the inspection.
   *
   * `inlineList` collapses a short run of scalars onto the parent's own row
   * because collapsing costs the reader no fact — and it must not cost them the
   * edit either, which is exactly what the owner hit: `CMD · wget · -qO- ·
   * http://localhost:3000/healthz`, inert. So a collapsed entry is a BUTTON that
   * opens the list at that entry, and an expanded list is rows with fields in
   * them. Neither form is a dead end and the compact one is still compact.
   *
   * Cleared when the selection changes: a list opened on `web` has nothing to
   * say about `db`, and an index is only meaningful against the list it indexes.
   */
  private expanded = new Set<string>();
  /**
   * Keys whose comment block is open — story 9.1. View state, like `expanded`.
   *
   * A comment is not a field, and this pane is rows of key/value pairs. The two
   * shapes that were available were a permanent third column for something most
   * rows have not got, or a block the reader opens on the row they mean; the
   * second is what a comment is — a thing said about one key — and it costs a
   * pane of ninety keys nothing when nobody has opened one.
   */
  private commentsOpen = new Set<string>();
  /**
   * What the engine says is at each open key's two positions.
   *
   * It arrives in its own message after the block is already on screen, for the
   * same reason an image lookup does: reading a comment is two requests, and a
   * pane that asked about every key it drew would make a hundred and eighty of
   * them to render one service.
   */
  private comments = new Map<string, CommentsAt>();
  /** Values whose move-into-a-variable block is open — story 9.3. View state. */
  private extractsOpen = new Set<string>();
  /** What the host said each open move would do. Arrives after the block. */
  private extracts = new Map<
    string,
    { path: string; staged: boolean; result?: ExtractResult; refused?: string }
  >();
  /** The search control built for each image field in THIS render pass. */
  private searches = new Map<string, ImageSearch>();

  constructor(private readonly callbacks: InspectorCallbacks) {
    this.element = el('aside', 'pane inspector');
    // A landmark per pane, and a language for the document — the accessibility
    // floor N6 sets.
    this.element.setAttribute('aria-label', 'Inspector');

    const header = el('header', 'pane-header inspector-header');
    const title = el('span', 'pane-title');
    title.textContent = 'Inspector';
    header.append(title, this.heading, this.summary, this.severity, this.source);
    this.element.append(header, this.body, this.footer);

    // Never an empty pane, not even before the first answer arrives.
    this.showMessage('Reading the stack…');
  }

  /**
   * What Docker Hub said about one image value — Epic 8.
   *
   * It arrives late, on its own, and the pane redraws from the inspection it
   * already has rather than asking the host for anything. That is the whole
   * arrangement: the pane was correct and complete before this landed, and a
   * lookup that never lands changes nothing.
   *
   * A key for a pane that is no longer on screen is dropped. A lookup takes as
   * long as a registry takes, which is longer than a reader takes to click
   * something else, and an upgrade pill for `postgres` on the pane for `redis`
   * is the specific way an asynchronous decoration goes wrong.
   */
  setLookup(key: string, lookup: ImageLookup): void {
    const inspection = this.lastInspection;
    if (!inspection) {
      return;
    }
    this.lookups.set(key, lookup);
    this.render(inspection);
  }

  /** One search answer, routed to whichever field's popup asked for it. */
  receiveSearch(token: number, answer: ImageSearchAnswer): void {
    for (const search of this.searches.values()) {
      search.receive(token, answer);
    }
  }

  /**
   * Forgets every lookup. Called when the drawn file changes.
   *
   * An answer about `compose.yml`'s `postgres:16` must not decorate
   * `other/compose.yml`'s field of the same name — the key is a config path and
   * two projects share those freely.
   */
  clearLookups(): void {
    this.lookups.clear();
  }

  /** Replaces the body with one line of prose. Used for waiting and for refusal. */
  showMessage(text: string, detail = ''): void {
    this.heading.textContent = '';
    this.summary.textContent = '';
    this.source.textContent = '';
    this.body.textContent = '';
    const p = el('p', 'inspector-message');
    p.textContent = text;
    this.body.appendChild(p);
    if (detail !== '') {
      const pre = el('pre', 'inspector-detail mono');
      pre.textContent = detail;
      this.body.appendChild(pre);
    }
  }

  /**
   * Renders one selection.
   *
   * Rebuilt wholesale rather than diffed. The pane is a list of rows over a
   * model that is re-fetched from the core on every change, and a reconciler
   * here would be a second place for the pane and the file to disagree.
   */
  render(inspection: Inspection): void {
    const node = inspection.schema.node;
    this.staged = new Set(inspection.staged);
    this.opened = new Set(inspection.opened ?? []);
    this.pending = inspection.pending ?? {};
    this.availability = inspection.availability ?? {};
    this.combos.clear();
    this.searches.clear();
    if (this.lastInspection !== null && this.lastInspection.id !== inspection.id) {
      // A different thing is selected. `services.web.command[2]` is not a
      // sentence about `db`, and carrying an opened list across would open a
      // list on the next pane that the reader never touched.
      this.expanded.clear();
      this.commentsOpen.clear();
      this.comments.clear();
      this.extractsOpen.clear();
      this.extracts.clear();
    }
    this.lastInspection = inspection;
    this.body.textContent = '';
    this.heading.textContent = inspection.name;
    this.summary.textContent = headerSummary(inspection);
    // Direction A's inspector header carries a severity count. Empty when there
    // is nothing to say — never a reassuring "0 problems".
    this.severity.textContent = severitySummary(inspection.findings);
    this.severity.hidden = this.severity.textContent === '';
    // …and what it is counting, since the problems panel counts something else
    // over a wider scope and the two numbers sit side by side on screen.
    const scope = severityScope(inspection.findings, inspection.name);
    this.severity.title = scope;
    this.severity.setAttribute(
      'aria-label',
      scope === '' ? '' : `${this.severity.textContent} — ${scope}`,
    );
    this.source.textContent = sourceLine(inspection);

    if (!node) {
      this.showMessage('The core returned no fields for this selection.');
      return;
    }

    // Findings that are about the node rather than about one of its fields.
    // They go at the head, where they are read before the values they concern.
    const fieldPaths = node.fields.map((f) => f.path);
    for (const f of findingsForNode(inspection.findings, inspection.id, fieldPaths)) {
      this.body.appendChild(this.diagnostic(f));
    }

    if (inspection.id === null) {
      this.body.appendChild(this.stackFacts(inspection));
    }
    if (inspection.schema.version_field !== undefined && inspection.id === null) {
      // AD-20 in the pane: say the field is dead, since the reader believes it
      // is choosing their feature set.
      const note = el('p', 'inspector-note');
      note.textContent =
        `This file declares version: ${inspection.schema.version_field}. Compose ignores it and ` +
        `so does Composure — there is one current specification, and what you can set is decided by ` +
        `the docker compose on your machine. The line can be deleted.`;
      this.body.appendChild(note);
    }

    // Grouped semantically, the way Direction A draws it: `Image`,
    // `Environment`, `Health`.
    //
    // A free-form mapping (`environment`, `labels`) is not grouped: its keys are
    // the reader's own and no map of Compose key names knows anything about them.
    const rendered = (f: SchemaField): boolean =>
      f.declared || this.opened.has(f.path) || this.staged.has(f.path);
    const sections = node.free_form
      ? [{ label: 'entries', ...pick(node.fields, rendered) }]
      : groupFields(node.fields, rendered);

    // What the file DOES NOT say, collected into one statement at the foot of
    // the pane instead of one paragraph per group.
    //
    // This used to render per group, and the argument for that — recorded here
    // and in `groups.ts` — was that "one ninety-key list at the foot contains
    // the same keys and teaches nothing, because the reader has to already know
    // that `ulimits` is a resource control". The argument was right about the
    // teaching and wrong about the remedy. Measured on `services.web`
    // (`harness/metrics.mjs`): TEN dashed blocks, each with its own heading,
    // rule and `You can also set` lead, five of them under a group heading that
    // introduced no fields at all — 1880px of content in a 620px viewport, so
    // nine of the ten paragraphs were below the fold and the reader scrolled
    // three screens to learn that `develop` exists. A differentiator nobody
    // scrolls to teaches nothing either.
    //
    // So: one block, at the foot, as `directions-3.html:335` and `DESIGN.md:167`
    // both draw it — but the keys inside it stay ORDERED AND LABELLED by group,
    // which is the part of the old argument that holds. `ulimits` is still
    // printed under the word `resources`; it is printed there once, in a
    // statement the reader can see without scrolling, rather than at the foot of
    // a heading that had nothing under it.
    //
    // Nothing is dropped, sliced, or put behind a "show more" — every key in
    // every group arrives in this block, which is what story 5.2 requires and
    // what `testdom.test.ts` asserts by full contents rather than by count.
    const offered: AvailableRun[] = [];

    let anyRendered = false;
    for (const section of sections) {
      anyRendered = anyRendered || section.fields.length > 0;
      if (section.available.length > 0) {
        offered.push({ label: section.label, fields: section.available });
      }
      // A group that turned out empty costs a heading, two rules and a
      // paragraph, and says nothing the foot block does not say better.
      if (section.fields.length === 0) {
        continue;
      }
      this.body.appendChild(
        this.group(section.label, section.fields, [], inspection, node.known),
      );
    }

    const anyAvailable = offered.length > 0;
    if (anyAvailable) {
      this.body.appendChild(this.availableBlock(offered, node.known));
    }

    if (!anyRendered) {
      const p = el('p', 'inspector-message');
      p.textContent = node.missing
        ? 'This path is not in the file. Everything below is what could be written here.'
        : 'This declares nothing yet. Everything below is what could be written here.';
      this.body.insertBefore(p, this.body.firstChild);
    }

    if (!anyAvailable && node.free_form) {
      const p = el('p', 'inspector-note');
      p.textContent = 'The specification names no keys here — any key is permitted.';
      this.body.appendChild(p);
    } else if (!anyAvailable && !node.known) {
      const p = el('p', 'inspector-note');
      p.textContent =
        'The vendored Compose specification does not describe this path, so what else is ' +
        'possible here is not known. What the file declares is shown above.';
      this.body.appendChild(p);
    }

    this.restoreFocus();
  }

  /** Puts focus back on the key the reader just staged, after the rebuild. */
  private restoreFocus(): void {
    const path = this.focusAfterRender;
    this.focusAfterRender = null;
    if (path === null) {
      return;
    }
    // An edited value refocuses the INPUT, not the block that holds it: a
    // keyboard reader who has just pressed Enter on a field wants the cursor
    // back in that field, not on its container.
    const target =
      this.body.querySelector(`[data-field="${cssEscape(path)}"]`) ??
      this.body.querySelector(`[data-path="${cssEscape(path)}"]`);
    if (target instanceof HTMLElement) {
      target.focus();
    }
  }

  /* ---- pieces --------------------------------------------------------- */

  /** The stack's own properties, shown when nothing is selected (story 5.1). */
  private stackFacts(inspection: Inspection): HTMLElement {
    const section = el('section', 'grp');
    section.appendChild(groupHeading('this stack'));
    const files = inspection.schema.files.map((f) => f.path);
    section.appendChild(
      this.factRow('source files', files.length > 0 ? files.map(shortFile).join(' · ') : 'none'),
    );
    section.appendChild(
      this.factRow(
        'profiles',
        inspection.schema.profiles.length > 0 ? inspection.schema.profiles.join(' · ') : 'none declared',
      ),
    );
    return section;
  }

  private factRow(key: string, value: string): HTMLElement {
    const row = el('div', 'field');
    const k = el('span', 'field-key');
    k.textContent = key;
    const v = el('span', 'field-value mono');
    v.textContent = value;
    row.append(k, v);
    return row;
  }

  /** One group: a heading, its rows, and its own `available, not set` line. */
  private group(
    label: string,
    fields: SchemaField[],
    available: SchemaField[],
    inspection: Inspection,
    known = true,
    depth = 0,
  ): HTMLElement {
    const section = el('section', depth > 0 ? 'grp grp-nested' : 'grp');
    if (depth > 0) {
      indent(section, depth);
    }
    section.appendChild(groupHeading(label, fields.length));
    for (const f of fields) {
      section.appendChild(this.field(f, inspection, depth));
    }
    if (available.length > 0) {
      section.appendChild(this.available(available, known));
    }
    return section;
  }

  /**
   * A key that holds a mapping, rendered as its own sub-group: the entries it
   * has, and the entries it could have.
   *
   * This is the second half of the add-attribute fix. `healthcheck` — opened by
   * the reader, or declared with a null value after a save — has no children in
   * the node's own schema answer; the HOST fetches them with a second
   * `stack/schema` at that path and attaches them, because the core will
   * describe a path the file does not contain. Without it the reader reached
   * `healthcheck` and found nothing: no value to type into, no children, and no
   * list of what could go inside.
   */
  private mappingGroup(field: SchemaField, inspection: Inspection, depth: number): HTMLElement {
    const rendered = (f: SchemaField): boolean =>
      f.declared || this.opened.has(f.path) || this.staged.has(f.path);
    const nested = groupsOf(field.children ?? [], rendered);
    const section = this.group(
      field.key,
      nested.lead.concat(nested.groups),
      nested.available,
      inspection,
      true,
      depth + 1,
    );
    section.dataset.path = field.path;
    if (this.staged.has(field.path)) {
      section.classList.add('is-staged');
    }
    // Where a mapping's own provenance goes — and it is NOT index 1.
    //
    // It used to be inserted straight after the heading, which put
    // `webstack/compose.yaml:36` between `HEALTHCHECK` and `test`. A provenance
    // line means "the value on the row above came from here", every time it
    // appears on this pane, so sitting above the first row made it read as
    // `test`'s — the wrong file position attached to the wrong key, which is
    // the one kind of wrong answer this pane must not give.
    //
    // Its home is the HEADING. `webstack/compose.yaml:36` is where the mapping
    // itself is declared, the heading is the mapping, and the mockup's `.grp-t`
    // is a two-slot flex row with exactly this shape — name on the left, what
    // the group is on the right (directions-3.html:575). It stays a visible,
    // focusable `prov-link` button and does not become a tooltip (story 5.3).
    const prov = field.value ? provenanceOf(field.value) : null;
    if (prov) {
      const head = section.querySelector('.grp-t');
      const line = el('span', 'grp-prov');
      line.appendChild(
        this.provLink(prov.label, prov.file, prov.line, prov.column, 'Reveal'),
      );
      (head ?? section).appendChild(line);
    }
    if (!field.declared) {
      // Said out loud: the file does not contain this mapping yet, and it will
      // not until the reader gives one of its keys a value. A group that looked
      // identical to a declared one would be the tool claiming a write it has
      // not made.
      const note = el('div', 'field-note');
      note.textContent =
        'Not in the file yet. Give one of these a value and both lines are staged together.';
      section.insertBefore(note, section.children[1] ?? null);
    }
    return section;
  }

  /** One field: key, value, provenance, and any finding on it. */
  private field(field: SchemaField, inspection: Inspection, depth: number): HTMLElement {
    // A mapping — declared with entries, declared with nothing, or opened by
    // the reader — is a sub-group rather than a row. There is no value to type.
    if (field.children && field.children.length > 0) {
      return this.mappingGroup(field, inspection, depth);
    }

    // The findings a row BENEATH this one has already shown.
    //
    // The defect this replaces: the core anchors at mapping ENTRY paths —
    // `services.db.environment.POSTGRES_PASSWORD` — and `stack/schema` returns
    // `environment` as ONE field whose keys arrive as `value.entries[]`, each
    // with its own path. Only this function asked for findings, so every pill
    // for every environment key landed on the `environment` header row, which is
    // not the row that caused it and, with twenty keys under it, does not say
    // which one did. `entryRow` now asks too, and records what it showed here so
    // the header does not repeat it — the same claimed-by-a-descendant rule
    // `findingsForNode` applies one level up.
    //
    // Recorded by what the tree ACTUALLY rendered rather than by re-deriving the
    // paths: the tree stops at MAX_VALUE_DEPTH, and a set computed separately
    // would claim findings for rows that were never drawn and silently drop them
    // from both places.
    const claimed = new Set<string>();
    const diag: EntryDiagnostics = { findings: inspection.findings, claimed };

    // NOT `is-nested`, even at depth. The mapping group holding this field has
    // already stepped right once; indenting the block again stepped twice for
    // one level of nesting, and every step moved the value column with it.
    // Measured before: one pane had values starting at x=820, x=831 and x=844
    // — three columns for one form.
    const wrap = el('div', 'field-block');
    wrap.dataset.path = field.path;
    const isStaged = this.staged.has(field.path);
    if (isStaged) {
      wrap.classList.add('is-staged');
    }

    const row = el('div', 'field');
    const key = el('span', 'field-key');
    key.textContent = field.key;
    if (field.description) {
      key.title = field.description;
    }
    row.appendChild(key);

    const value = field.value;

    // Story 5.2's last acceptance criterion, and the defect this pane shipped
    // with: an unset key the reader has clicked must be somewhere they can
    // TYPE, right now, with the cursor in it. Two states arrive here and both
    // used to be dead ends —
    //
    //   opened or staged, undeclared   the file does not contain the key. A
    //                                  commit stages an insert_key; until then
    //                                  nothing is staged at all, so no value
    //                                  the schema never supplied is written.
    //   declared with a null value     `healthcheck:` alone on a line, which is
    //                                  what a save used to produce. The old
    //                                  code rendered a read-only `~`.
    if (!value || value.kind === 'null') {
      row.appendChild(this.unsetValue(field, isStaged));
      // The value list, on the UNSET path too — `available, not set` is the
      // primary discovery route this story exists for, and the reader who has
      // never written `restart` is the reader who needs the list most. The
      // first build of 7.9 asserted the binding on the declared path only, and
      // deleting it here left every assertion passing.
      this.appendCombo(row, field);
      wrap.appendChild(row);
      if (isStaged) {
        wrap.appendChild(this.unstageControl(field.path));
      }
      this.appendUnsetNote(wrap, field, value?.kind === 'null');
      // A key the file does not have is ordinarily just an insert — but not
      // when the mapping it would go into is written in flow style, which the
      // engine refuses. The reader finds that out here rather than from a
      // banner after they have typed.
      this.appendAvailabilityNote(wrap, field.path, 'null-value');
      let linked = new Set<string>();
      if (value) {
        const prov = provenanceOf(value);
        if (prov) {
          wrap.appendChild(this.provenance(prov));
          linked = this.linkedPositions(prov);
        }
      }
      // Under the provenance rather than above it: the provenance belongs to
      // the VALUE and reads as part of the row, and putting a second line of
      // link-blue between them made one block of three lines where the reader
      // needs two. What the specification offers is a separate thought and
      // comes after.
      for (const f of findingsForField(inspection.findings, field.path)) {
        this.appendDiagnostic(wrap, row, f, linked);
      }
      return wrap;
    }

    // Story 9.2's `+ entry` row, held until after the provenance line below.
    let addTo: string | null = null;
    if (value.kind === 'scalar' || value.kind === 'alias') {
      // Story 6.1: a plain scalar is editable in place — click, type, Enter.
      // An alias is NOT: the value the file holds at this path is `*name`, and
      // splicing over it would rewrite the reference rather than the anchor,
      // which is a change the reader did not ask for and would not expect.
      row.appendChild(this.scalarValue(field.path, value, field));
      // A key the file already SETS is the common case for this — `restart:
      // unless-stoped` is a typo someone made because nothing offered them the
      // four words — so the list is not confined to the unset half.
      this.appendCombo(row, field);
      // Epic 8: the search popup, on the one key it is for.
      this.appendImageSearch(row, field);
      // Stories 9.1 and 9.3: the row's own controls, in one grid cell.
      this.appendRowActions(row, field.path, { value });
      wrap.appendChild(row);
      // …and the upgrade pill, under the value and indented to the value
      // column. It was in the ROW first, which is where the mockup draws it —
      // and at the inspector's real width that cost the reader the value
      // itself. The measurement is in style.css beside `.pill-upgrade`.
      this.appendUpgradePill(wrap, field.path);
    } else {
      // A sequence or a mapping. The key is a heading and every element gets
      // its own row — a count in place of the entries is exactly the failure
      // story 5.1 exists to fix.
      //
      // What used to sit in the value cell here was the word `list` or `map`.
      // It is not in the file, it says nothing the rows underneath do not say
      // louder, and it cost one full row per collection. A short list of
      // scalars does not even get the rows: it prints as its values.
      addTo = this.appendCollection(wrap, row, value, depth, diag, field.path);
      // After, not before. `appendCollection` puts the value cell on this row
      // for a collapsed list, and a control appended first would take the
      // value's grid cell and push the values onto a second line.
      this.appendRowActions(row, field.path);
    }

    if (value.kind === 'scalar' || value.kind === 'alias') {
      // Same snapshot, same reason as `entryRow`: a scalar renders no children,
      // so what this row will show is settled by now.
      this.appendResolution(
        wrap,
        value,
        findingsForField(inspection.findings, field.path).filter((f) => !claimed.has(findingKey(f))),
      );
    }
    const prov = provenanceOf(value);
    if (prov) {
      wrap.appendChild(this.provenance(prov, value.alias, value.alias_site));
    }
    // Under the provenance, because it is the same thought continued: the
    // provenance says where the value came FROM, and this says what that means
    // for changing it here.
    this.appendAvailabilityNote(wrap, field.path);
    // Epic 8's line, after both: everything above is about the reader's files
    // and this one is about Docker Hub, so it comes last and says so.
    this.appendImageNote(wrap, field.path);
    // A key with a value list whose value is a MAPPING or a SEQUENCE has no
    // field to open a popup on, so the specification's words are stated as
    // text. Never for a scalar: that row got the combobox above, and printing
    // the values a second time underneath is the row of links the owner
    // rejected, back again under another name.
    if (!this.combos.has(field.path)) {
      this.appendAllowedNote(wrap, field);
    }
    if (addTo !== null) {
      wrap.appendChild(this.addEntryRow(addTo, depth + 1));
    } else if (this.canAddKey(field)) {
      // The mapping form of the same gesture, at the same place: the foot of
      // the thing it adds to. Exclusive with `+ entry` by construction, because
      // `addTo` is a SEQUENCE's path and this is a MAPPING's — the file writes
      // `environment` one way or the other and the pane adds to whichever it
      // wrote, never converting between them.
      wrap.appendChild(this.addKeyRow(field.path, depth + 1));
    }
    this.appendCommentBlock(wrap, field.path);
    this.appendExtractBlock(wrap, field.path);
    if (field.path in this.pending && value.kind === 'scalar') {
      // What the file still says. The editor's own dirty indicator does not
      // appear until we write, so this line is the only thing distinguishing
      // "staged" from "saved" — and the difference is the whole product.
      const note = el('div', 'field-note is-staged-note');
      note.textContent = `staged — the file still says ${value.text}`;
      wrap.appendChild(note);
      wrap.appendChild(this.unstageControl(field.path));
    }
    // What is left after the entries took theirs. A mapping where SOME entries
    // have findings and some do not keeps a pill on the header only for findings
    // anchored at the mapping itself — the header row is not a summary of its
    // children, it is a row, and a pill on it means "this key", not "something
    // under this key".
    for (const f of findingsForField(inspection.findings, field.path)) {
      if (claimed.has(findingKey(f))) {
        continue;
      }
      this.appendDiagnostic(wrap, row, f, this.linkedPositions(prov, value.alias_site));
    }
    return wrap;
  }

  /**
   * The value cell for a key that is not set: an input with the cursor in it.
   *
   * `restoreFocus` puts the caret here immediately after the click that opened
   * the key, which is the acceptance criterion in one line. The input carries
   * the schema's default as its VALUE only when the specification supplied one
   * as a value; a prose default is a placeholder, and an untouched field commits
   * nothing, so it can never be written by accident.
   */
  private unsetValue(field: SchemaField, isStaged: boolean): HTMLElement {
    const path = field.path;
    const staged = this.pending[path];
    const seed = staged ?? defaultValue(field);
    const combo = this.comboFor(field);
    const search = this.imageSearchFor(field);
    const input = valueInput({
      value: seed,
      placeholder: placeholderFor(field),
      label:
        (isStaged
          ? `${path} — staged, not yet written. Press Enter to restage, Escape to discard this edit`
          : `${path} — not set. Type a value and press Enter to stage it, Escape to leave it unset`) +
        allowedName(field),
      fieldKey: path,
      intercept: keyHook(combo, search),
      commit: (next) => this.callbacks.send({ type: 'edit', path, value: next }),
      onCommit: () => {
        this.focusAfterRender = path;
      },
      // Escape on a field the reader has not staged puts the key back in the
      // `available, not set` list. On a staged one it DISCARDS THE STAGE, which
      // is what the accessible name has always promised and what the control
      // did not do — it used to revert to the stage, not to the file.
      revert: () => {
        this.focusAfterRender = null;
        this.callbacks.send(isStaged ? { type: 'unstage', path } : { type: 'close', path });
      },
    });
    if (seed === '') {
      input.classList.add('is-unset');
    }
    combo?.attach(input);
    search?.attach(input);
    return input;
  }

  /**
   * The note under an unset field: what the specification says, and — for the
   * one state this engine rewrites rather than splices — what that costs.
   */
  private appendUnsetNote(wrap: HTMLElement, field: SchemaField, declaredNull: boolean): void {
    if (defaultIsProse(field)) {
      // AD-20's hazard, said in the pane. The specification records this
      // default in PROSE, so it is not a value and Composure will not write it.
      const note = el('div', 'field-note');
      note.textContent =
        `The specification describes the default as “${field.default}” in prose rather than ` +
        'recording a value, so it is not filled in. Type what you want written.';
      wrap.appendChild(note);
    }
    if (declaredNull) {
      const note = el('div', 'field-note');
      note.textContent =
        'The file declares this key with no value. Typing one rewrites the line at the foot of ' +
        'its mapping — one line removed, one added — because a value cannot be spliced into a ' +
        'range that has no bytes.';
      wrap.appendChild(note);
    }
  }

  /**
   * The values a key accepts — story 7.9, and the owner's own request: "if the
   * key accepts only predefined values … then the input should be a dropdown
   * list with the allowed values that I can override."
   *
   * THIS REPLACES A `<datalist>` AND A ROW OF LINKS, both of which the owner
   * tested and rejected. The full reasoning — including why the previous
   * agent's argument for the datalist was sound and the result was still wrong
   * — is at the top of `webview/combo.ts`. In one line: a datalist filters its
   * options against the field's current text, so on `restart: unless-stopped`
   * the popup contained exactly one entry, the value the field already showed,
   * and the other three were printed underneath as links.
   *
   * What this builds instead is a real combobox: an arrow, a popup, and a list
   * that is NEVER filtered. The field is still the same `valueInput` — Enter
   * stages, Escape reverts, blur commits — so `${RESTART_POLICY}` and any word
   * from a Compose newer than the vendored schema stay typeable, which is the
   * property a `<select>` cannot have and the reason one is still refused.
   *
   * Choosing a value FILLS THE FIELD AND STAGES NOTHING. DECISIONS.md 17's
   * gesture, unchanged from the chips this replaces: choosing says which value,
   * Enter says write it. That is why Enter has two meanings here and why they
   * do not collide — with the popup open it chooses, with it closed it stages.
   */
  private comboFor(field: SchemaField): Combo | null {
    const values = allowedValues(field);
    if (values.length === 0) {
      return null;
    }
    const path = field.path;
    const combo = new Combo({
      path,
      values,
      lead: allowedLead(field),
      listId: optionListId(path),
      toggleName: allowedToggleName(field, path),
      optionName: (value) => allowedOptionName(field, path, value),
    });
    this.combos.set(path, combo);
    return combo;
  }

  /**
   * The arrow and the popup, into the ROW rather than the block.
   *
   * Both are absolutely positioned against `.field`, which is what keeps the
   * value cell a direct grid child of the row: `harness/rhythm.mjs` measures the
   * value column as `.field > .field-value`, so wrapping the input in a
   * flex container would have quietly taken every field with a value list OUT
   * of the one-value-column check — the exact rows this change touches.
   */
  private appendCombo(row: HTMLElement, field: SchemaField): void {
    const combo = this.combos.get(field.path);
    if (!combo) {
      return;
    }
    row.append(combo.toggle, combo.popup);
  }

  /* ---- Epic 8: what Docker Hub says about an image value --------------- */

  /**
   * The upgrade pill, under the value and aligned to the value column.
   *
   * NOT a third grid child of `.field`, for two reasons that both came out of
   * the harness: `rhythm.mjs` measures the one value column as
   * `.field > .field-value`, and — the one that decided it — a 250px pill in a
   * 318px cell collapsed `ghcr.io/shipyard/web:2.4.1` to a single clipped
   * glyph at the default split. See style.css beside `.pill-upgrade`.
   *
   * Pressing it STAGES through the same `edit` message a typed value uses. Not
   * a second write path, not a write: the reference goes into the field, a
   * pending diff appears, and `Save to <file>` is still the only control in
   * this product that touches a file (DECISIONS.md 17, AD-19).
   */
  private appendUpgradePill(wrap: HTMLElement, path: string): void {
    const lookup = this.lookups.get(path);
    if (!lookup) {
      return;
    }
    const pill = upgradePill(lookup, {
      stage: (reference) => {
        this.focusAfterRender = path;
        this.callbacks.send({ type: 'edit', path, value: reference });
      },
    });
    if (pill) {
      wrap.appendChild(pill);
    }
  }

  /**
   * The line under an image value: how old it is, and — when there is no pill —
   * why there is not.
   *
   * Every state gets one except the offer itself, which gets the pill and the
   * age. Saying nothing on `offline` or `rate-limited` would read as "there is
   * nothing newer", which is the confident wrong answer in the exact place a
   * reader decides whether to upgrade (CLAUDE.md rule 6).
   */
  private appendImageNote(wrap: HTMLElement, path: string): void {
    const lookup = this.lookups.get(path);
    if (!lookup) {
      return;
    }
    const note = stateNote(lookup);
    if (note) {
      wrap.appendChild(note);
      return;
    }
    const age = ageNote(lookup);
    if (age !== '') {
      const line = el('div', 'field-note image-note');
      line.dataset.imageState = lookup.state;
      line.textContent = age;
      wrap.appendChild(line);
    }
  }

  /**
   * The Docker Hub search popup, on the `image` key and on nothing else.
   *
   * The owner's request in full was two things — "Image names are not lookup
   * from dockerhub or even option to search for image in docker hub" — and this
   * is the second. Choosing a result FILLS THE FIELD and stages nothing:
   * DECISIONS.md 17's gesture, identical to story 7.9's value list, because a
   * repository name is not yet a reference the reader has decided on. Enter is
   * what stages, as everywhere else in this pane.
   */
  private appendImageSearch(row: HTMLElement, field: SchemaField): void {
    const search = this.searches.get(field.path);
    if (!search) {
      return;
    }
    row.append(search.toggle, search.popup);
  }

  /**
   * Builds the search control for an image field, before the input exists.
   *
   * Same two-halves-in-two-places shape as `comboFor`: the `intercept` hook has
   * to be handed to `valueInput` at the moment the input is built, and the
   * button and popup are appended to the row afterwards.
   */
  private imageSearchFor(field: SchemaField): ImageSearch | null {
    if (field.key !== 'image' || field.path === '') {
      return null;
    }
    const path = field.path;
    const search = new ImageSearch({
      key: path,
      search: (token, query) => this.callbacks.send({ type: 'searchImage', token, query }),
      // Fills the field and nothing else. The control has put the text in the
      // input already; this hook exists so a caller that DOES want to stage —
      // the add form does not, and neither does this — has somewhere to do it.
      choose: () => undefined,
    });
    this.searches.set(path, search);
    return search;
  }

  /**
   * The values, as plain text, for a field that has no input to hang a popup on.
   *
   * `gpus` accepts `all` or a list of device objects; when the file writes the
   * list, the value cell is rows rather than a field and there is nothing to
   * open. Saying nothing there would drop the specification's own words on the
   * one key where they are most easily missed, so they are stated — as text,
   * not as links, because there is no field for a link to fill.
   */
  private appendAllowedNote(wrap: HTMLElement, field: SchemaField): void {
    const values = allowedValues(field);
    if (values.length === 0) {
      return;
    }
    const note = el('div', 'field-note allowed-note');
    note.textContent = `${allowedLead(field)} ${values.join(', ')}`;
    wrap.appendChild(note);
  }

  /**
   * The row's own controls — `Remove`, `#`, `${}` — in ONE element.
   *
   * One rather than three, and appended LAST, because `.field` is a grid: a
   * direct child claims a cell, so three loose buttons on a two-column row
   * wrapped onto a line of their own and dragged the value cell of every
   * collapsed list into the key column with them. `rhythm.mjs --check` caught
   * exactly that — `one value column x = 806, 678, 694` — which is what that
   * check is for. The container takes one `auto` column that `style.css` adds
   * with `:has(> .field-actions)`, so a row without controls has the geometry
   * it always had.
   */
  private appendRowActions(
    row: HTMLElement,
    path: string | undefined,
    opts: { value?: ValueView; list?: string } = {},
  ): void {
    if (path === undefined || path === '') {
      return;
    }
    const actions = el('span', 'field-actions');
    if (opts.list !== undefined && this.canEditList(opts.list)) {
      actions.appendChild(this.removeEntryControl(path));
    }
    this.appendCommentControl(actions, path);
    if (opts.value) {
      this.appendExtractControl(actions, path, opts.value);
    }
    if (actions.children.length > 0) {
      row.appendChild(actions);
    }
  }

  /* ---- Epic 9, story 9.3: moving a value into a variable --------------- */

  /**
   * What the host says a move would do — story 9.3.
   *
   * Arrives after the block is on screen, like a comment and like an image
   * lookup, and for the same reason: it is a round trip the pane does not make
   * until the reader asks for one.
   */
  setExtract(answer: { path: string; staged: boolean; result?: ExtractResult; refused?: string }): void {
    this.extracts.set(answer.path, answer);
    if (this.lastInspection) {
      this.render(this.lastInspection);
    }
  }

  /**
   * The control that offers the move — `${}`, on a value the pane can write.
   *
   * `${}` because that is what the file will say afterwards, which is the one
   * thing the reader needs to recognise. Offered on every editable scalar
   * rather than only on the ones a rule flagged: the owner asked to move *any*
   * value, and a control that appeared only on passwords would be a different
   * feature wearing this one's name.
   */
  private appendExtractControl(row: HTMLElement, path: string | undefined, value: ValueView): void {
    if (path === undefined || path === '' || value.kind !== 'scalar' || !this.writable(path)) {
      return;
    }
    const open = this.extractsOpen.has(path);
    const button = document.createElement('button');
    button.type = 'button';
    button.className = open ? 'row-action extract-open is-open' : 'row-action extract-open';
    button.textContent = '${}';
    button.dataset.path = path;
    button.setAttribute(
      'aria-label',
      open
        ? `Close the move of ${path} into a variable`
        : `Move ${path} into a variable in the .env file`,
    );
    button.setAttribute('aria-expanded', open ? 'true' : 'false');
    button.title = button.getAttribute('aria-label') ?? '';
    button.addEventListener('click', () => {
      if (this.extractsOpen.has(path)) {
        this.extractsOpen.delete(path);
        this.callbacks.send({ type: 'closeExtract', path });
      } else {
        this.extractsOpen.add(path);
        this.callbacks.send({ type: 'openExtract', path });
      }
      if (this.lastInspection) {
        this.render(this.lastInspection);
      }
    });
    row.appendChild(button);
  }

  /**
   * The move, laid out as the decision it is: a name, both diffs, and a press.
   *
   * BOTH diffs, always. The compose half and the `.env` half are one operation
   * and the reader approves them together or not at all — a two-file change
   * that showed one diff would be a lie about the half they cannot see
   * (DECISIONS.md 25). Nothing here writes; the control at the foot STAGES, and
   * `Save to <both files>` is still the only thing in this product that writes.
   */
  private appendExtractBlock(wrap: HTMLElement, path: string | undefined): void {
    if (path === undefined || path === '' || !this.extractsOpen.has(path)) {
      return;
    }
    const block = el('div', 'extract-block');
    block.dataset.path = path;
    const answer = this.extracts.get(path);
    if (answer === undefined) {
      const note = el('div', 'field-note');
      note.textContent = 'Working out what this would change…';
      block.appendChild(note);
      wrap.appendChild(block);
      return;
    }
    if (answer.refused !== undefined || answer.result === undefined) {
      // No name field, no diffs and no button. A refusal is not a fault and it
      // is not a form either: there is nothing here for the reader to fill in.
      const note = el('div', 'field-note is-readonly');
      note.textContent = answer.refused ?? 'That value cannot be moved into a variable.';
      block.appendChild(note);
      wrap.appendChild(block);
      return;
    }
    const result = answer.result;

    const nameRow = el('div', 'field-block extract-name');
    const row = el('div', 'field');
    const key = el('span', 'field-key comment-key');
    key.textContent = 'variable name';
    row.appendChild(key);
    row.appendChild(
      valueInput({
        value: result.name,
        label:
          `The name ${path} will be moved into. Press Enter to see what that name would do, ` +
          'Escape to close',
        fieldKey: `extract:${path}`,
        // Enter RE-ASKS rather than staging. The diffs underneath are the diffs
        // for the name in this field, and a name that staged on Enter would
        // stage a pair of diffs the reader has never seen.
        commit: (next) => this.callbacks.send({ type: 'openExtract', path, name: next }),
        revert: () => {
          this.extractsOpen.delete(path);
          this.callbacks.send({ type: 'closeExtract', path });
          if (this.lastInspection) {
            this.render(this.lastInspection);
          }
        },
      }),
    );
    nameRow.appendChild(row);
    block.appendChild(nameRow);

    block.appendChild(this.extractDiff(`${shortFile(result.compose.file)} — the compose file`, result.compose.diff));
    if (!result.env_unchanged) {
      block.appendChild(this.extractDiff(`${shortFile(result.env_file)} — the variable`, result.env_diff ?? ''));
    }
    const note = el('div', 'field-note');
    note.textContent = this.envNote(result);
    block.appendChild(note);

    const controls = el('div', 'field-controls');
    const stage = document.createElement('button');
    stage.type = 'button';
    stage.className = answer.staged ? 'extract-stage is-staged' : 'extract-stage';
    stage.textContent = answer.staged ? 'Staged' : `Stage this move`;
    stage.setAttribute(
      'aria-label',
      answer.staged
        ? `${path} is staged as a move into ${result.name}. Nothing is written until you save`
        : `Stage the move of ${path} into ${result.name}. Nothing is written until you save`,
    );
    stage.addEventListener('click', () => {
      this.callbacks.send({ type: 'stageExtract', path, name: result.name });
    });
    controls.appendChild(stage);
    block.appendChild(controls);
    wrap.appendChild(block);
  }

  /** One of the two diffs, verbatim — nothing here computes what would change. */
  private extractDiff(label: string, diff: string): HTMLElement {
    const box = el('div', 'extract-diff-box');
    const head = el('div', 'extract-diff-label');
    head.textContent = label;
    const body = el('pre', 'extract-diff mono');
    body.setAttribute('aria-label', `Unified diff of ${label}`);
    for (const line of diffBodyLines(diff)) {
      const row = el('span', `diff-line diff-${diffLineKind(line)}`);
      row.textContent = line;
      body.appendChild(row);
    }
    box.append(head, body);
    return box;
  }

  /**
   * Which of the three shapes the `.env` half is, in words.
   *
   * `env_created` and `env_unchanged` are two booleans, and a reader deciding
   * whether to press a button that writes two files needs a sentence.
   */
  private envNote(result: ExtractResult): string {
    const where = shortFile(result.env_file);
    if (result.env_unchanged) {
      return (
        `${where} already gives ${result.name} this value, so it is left byte-identical. ` +
        'Only the compose file changes.'
      );
    }
    if (result.env_created) {
      return `This creates ${where} beside the compose file and puts ${result.name} in it.`;
    }
    return `This appends a line to ${where}. Every byte already in it stays as it is.`;
  }

  /* ---- Epic 9, story 9.1: comments ------------------------------------ */

  /**
   * One key's comment, as the host read it out of the engine — story 9.1.
   *
   * Late, like an image lookup, and for the same reason: the block was already
   * on screen when this was asked for, and the pane says so while it waits.
   */
  setComments(comments: CommentsAt): void {
    this.comments.set(comments.path, comments);
    if (this.lastInspection) {
      this.render(this.lastInspection);
    }
  }

  /**
   * The control that opens a key's comment — one per row that has bytes.
   *
   * `#` and not a word, because it is the mark the file itself uses and the row
   * it sits on already carries a key, a value and sometimes a pill. The
   * accessible name is the whole sentence; the glyph is for the eye.
   *
   * Not offered on a key the file does not declare. There is no line for a
   * comment to attach to, and `set_comment` at a path with no bytes is a
   * refusal the reader would meet after typing a sentence.
   */
  private appendCommentControl(row: HTMLElement, path: string | undefined): void {
    if (path === undefined || path === '') {
      return;
    }
    const open = this.commentsOpen.has(path);
    const button = document.createElement('button');
    button.type = 'button';
    button.className = open ? 'row-action comment-open is-open' : 'row-action comment-open';
    button.textContent = '#';
    button.dataset.path = path;
    button.setAttribute(
      'aria-label',
      open ? `Close the comment on ${path}` : `Add or edit the comment on ${path}`,
    );
    button.setAttribute('aria-expanded', open ? 'true' : 'false');
    button.title = button.getAttribute('aria-label') ?? '';
    button.addEventListener('click', () => {
      if (this.commentsOpen.has(path)) {
        this.commentsOpen.delete(path);
        this.callbacks.send({ type: 'closeComment', path });
      } else {
        this.commentsOpen.add(path);
        // The engine's answer, asked for now rather than for every key on the
        // pane. Nothing is staged and nothing is written by opening it.
        this.callbacks.send({ type: 'openComment', path });
      }
      if (this.lastInspection) {
        this.render(this.lastInspection);
      }
    });
    row.appendChild(button);
  }

  /**
   * The comment block: the two positions, and nothing else.
   *
   * There is no third position because there is no third address. A comment
   * that belongs to no key can only be named by a line number, and a line
   * number moves the instant anything above it is edited — the silent rebase
   * AD-19 refuses everywhere else in this product (DECISIONS.md 23).
   */
  private appendCommentBlock(wrap: HTMLElement, path: string | undefined): void {
    if (path === undefined || path === '' || !this.commentsOpen.has(path)) {
      return;
    }
    const block = el('div', 'comment-block');
    block.dataset.path = path;
    const at = this.comments.get(path);
    if (at === undefined) {
      // Never a blank box where a field will be: an empty one is
      // indistinguishable from "this key has no comment", and a reader who
      // typed into it would replace something they never saw.
      const note = el('div', 'field-note');
      note.textContent = 'Reading what this key already says…';
      block.appendChild(note);
      wrap.appendChild(block);
      return;
    }
    for (const where of COMMENT_POSITIONS) {
      const refused = (at.unavailable ?? []).find((u) => u.where === where);
      if (refused) {
        const note = el('div', 'field-note is-readonly');
        note.textContent = refused.detail;
        block.appendChild(note);
        continue;
      }
      block.appendChild(this.commentRow(path, where, at));
    }
    wrap.appendChild(block);
  }

  /** One position: its label, its field, and the delete when there is one. */
  private commentRow(path: string, where: CommentWhere, at: CommentsAt): HTMLElement {
    const wrap = el('div', 'field-block comment-row');
    const row = el('div', 'field');
    const key = el('span', 'field-key comment-key');
    // Short enough for the key column, which ellipsises: `comment on the li…`
    // told the reader nothing. The full sentence is in the accessible name.
    key.textContent = where === 'above' ? 'comment above' : 'on the line';
    row.appendChild(key);

    const current = at[where];
    const staged = at.staged.includes(where);
    row.appendChild(this.commentField(path, where, current ?? '', staged));
    if (current !== null) {
      // Only where there is one. `delete_comment` at a position with none is
      // refused BY NAME — "there was nothing to delete" and "the delete did
      // nothing" are different sentences — and offering the control anyway
      // would make the reader read one of them for no reason.
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'row-action comment-remove';
      remove.textContent = 'Remove';
      remove.setAttribute('aria-label', `Stage the removal of the ${where} comment on ${path}`);
      remove.addEventListener('click', () => {
        this.callbacks.send({ type: 'deleteComment', path, where });
      });
      row.appendChild(remove);
    }
    wrap.appendChild(row);
    if (staged) {
      const note = el('div', 'field-note comment-staged');
      note.textContent =
        current === null
          ? 'staged — the comment is in the file until you save'
          : 'staged, not yet written — the file still says what it said';
      wrap.appendChild(note);
    }
    return wrap;
  }

  /**
   * The field for one position.
   *
   * `above` is a `<textarea>` because a run of comment lines is ONE comment and
   * writing a two-line one has to be possible; `trailing` is an `<input>`
   * because a trailing comment is one line by construction, and the engine
   * refuses a line break there rather than putting the second half into the
   * mapping below. The control is the shape of what the engine will accept.
   *
   * Enter stages, Escape closes — the same two keys every other field on this
   * pane answers to. Shift+Enter is the newline, in the one place on this pane
   * that needs one.
   */
  private commentField(
    path: string,
    where: CommentWhere,
    value: string,
    staged: boolean,
  ): HTMLElement {
    const key = commentKey(path, where);
    const label =
      `${where === 'above' ? 'The comment above' : 'The comment on the line of'} ${path}` +
      (staged ? ' — staged, not yet written.' : '.') +
      ' Press Enter to stage it, Escape to close' +
      (where === 'above' ? '. Shift and Enter start a new line' : '');
    const field = document.createElement(where === 'above' ? 'textarea' : 'input') as HTMLElement & {
      value: string;
      placeholder: string;
      type: string;
    };
    if (where !== 'above') {
      field.type = 'text';
    }
    field.className = 'field-value field-input comment-input mono';
    field.value = value;
    field.placeholder = 'no comment here yet';
    field.dataset.field = key;
    field.setAttribute('aria-label', label);
    field.addEventListener('keydown', (e) => {
      const event = e as KeyboardEvent;
      if (event.key === 'Enter' && !(where === 'above' && event.shiftKey)) {
        event.preventDefault();
        this.callbacks.send({ type: 'setComment', path, where, text: field.value });
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        this.commentsOpen.delete(path);
        this.callbacks.send({ type: 'closeComment', path });
        if (this.lastInspection) {
          this.render(this.lastInspection);
        }
      }
    });
    return field;
  }

  /**
   * `Discard` for ONE staged edit.
   *
   * The `unstage` message, the host case and `Staging.remove` have all existed
   * since story 6.1 and nothing in the webview ever sent it: a reader with three
   * staged edits could discard all three or save all three, and nothing in
   * between. This is that control, and Escape in the field sends the same thing.
   */
  private unstageControl(path: string): HTMLElement {
    const line = el('div', 'field-controls');
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'field-unstage';
    button.textContent = 'Discard this edit';
    button.setAttribute('aria-label', `Discard the staged edit to ${path}`);
    button.addEventListener('click', () => {
      this.callbacks.send({ type: 'unstage', path });
    });
    line.appendChild(button);
    return line;
  }

  /**
   * A finding, split across the row it concerns and the line beneath it.
   *
   * DESIGN.md:180 — the pill "sits inline with the field it concerns". It used
   * to be a separate block indented to 34%, which reads as a paragraph about the
   * pane rather than a mark on one value. The severity word travels on the pill
   * and the sentence goes underneath, because the sentence is a paragraph and a
   * row is a row.
   */
  private appendDiagnostic(
    wrap: HTMLElement,
    row: HTMLElement,
    finding: Finding,
    already: ReadonlySet<string> = new Set(),
  ): void {
    row.appendChild(this.severityPill(finding));
    const line = el('div', `diag diag-${finding.severity}`);
    line.dataset.key = findingKey(finding);
    const text = el('span', 'diag-text');
    text.textContent = finding.message;
    line.appendChild(text);
    // Every anchor stays REACHABLE; an anchor already reachable from this block
    // is not printed a second time.
    //
    // `undefined-variable` on `SESSION_SECRET` anchors at exactly the position
    // the value's own provenance line already links to, so the block printed
    // `webstack/compose.yaml:34` twice, sixteen pixels apart, as two buttons
    // going to the same place. The second was not a second position — it was
    // the same one, said again. A finding whose anchors are somewhere ELSE
    // still prints all of them: a collision is two positions and the reader
    // needs both, so this skips a POSITION and never a finding.
    //
    // `already` is passed in rather than read back out of `wrap`, because the
    // caller is the only thing that knows which provenance it rendered — and
    // because a DOM query here would make this depend on selector support the
    // test DOM does not have, which is how a check stops being able to fail.
    for (const anchor of finding.anchors) {
      if (!anchor.origin.file || anchor.origin.line < 1) {
        continue;
      }
      if (already.has(`${anchor.origin.file}:${anchor.origin.line}`)) {
        continue;
      }
      line.appendChild(
        this.provLink(
          `${shortFile(anchor.origin.file)}:${anchor.origin.line}`,
          anchor.origin.file,
          anchor.origin.line,
          anchor.origin.column,
          anchor.label,
        ),
      );
    }
    wrap.appendChild(line);
  }

  /**
   * The pill itself.
   *
   * It reads the RULE. The severity is in the class the stylesheet colours, in
   * the accessible name and in the tooltip — three carriers, two of them text,
   * so N6 holds without the pill spending its only line saying `hint` for the
   * fourth time in one pane.
   */
  private severityPill(finding: Finding): HTMLElement {
    const pill = el('span', `pill pill-${finding.severity}`);
    pill.textContent = pillText(finding);
    pill.title = pillTitle(finding);
    // Overrides the visible text for a screen reader, which is the point: the
    // severity word has to reach a reader who cannot see the colour.
    pill.setAttribute('aria-label', pillName(finding));
    return pill;
  }

  /**
   * A declared sequence or mapping on a row: either as its values, or as a
   * heading with the entries beneath it.
   *
   * Neither branch prints the words `list` or `map`. Those are the reader's own
   * conclusion from what is drawn — a run of values on the row, or named rows
   * underneath it — and spending a row saying it out loud is what made a
   * one-element list four lines tall.
   */
  private appendCollection(
    wrap: HTMLElement,
    row: HTMLElement,
    value: ValueView,
    depth: number,
    diag: EntryDiagnostics,
    path?: string,
  ): string | null {
    const items = inlineList(value);
    // Story 9.2. The collapsed form stays collapsed until the reader asks for
    // an entry, and asking for one is pressing it — the row is a route rather
    // than a summary. `expanded` is the reader's answer and it is the only
    // thing that overrides `inlineList`'s.
    if (items !== null && !(path !== undefined && this.expanded.has(path))) {
      row.appendChild(this.inlineValues(items, path));
      wrap.appendChild(row);
      return null;
    }
    // A heading row: the key, and nothing in the value column. `is-parent` lets
    // the key take only the width it needs, so a finding's pill sits beside the
    // key it concerns rather than across an empty cell.
    row.classList.add('is-parent');
    wrap.appendChild(row);
    wrap.appendChild(this.valueTree(value, depth + 1, diag, path));
    // The other half of story 9.2, and the reason `insert_sequence_entry`
    // exists: a list the reader can change one entry of is a list they can add
    // one to. The position is the END and is never theirs to name — an index
    // they chose is an index the list may not have.
    //
    // RETURNED rather than appended, because the caller has a provenance line
    // still to write and every provenance line on this pane means "the value
    // ABOVE came from here". Appended here, the list's own `compose.yaml:36`
    // landed under `+ entry` and claimed to be its. The caller appends it after.
    return value.kind === 'sequence' && this.canEditList(path) ? (path as string) : null;
  }

  /**
   * The value cell for a collapsed list.
   *
   * The cell is in the code face because every item in it is literally in the
   * file. The separator is NOT in the file, so it takes the UI face back —
   * DESIGN.md's rule is what makes this several elements rather than one
   * joined string, and it is what stops a reader reading ` · ` as content.
   *
   * Each item is a BUTTON when the list has an address (story 9.2). It is what
   * makes `CMD · wget · -qO- · http://localhost:3000/healthz` a thing the
   * reader can get into: pressing an entry opens the list at that entry with
   * the caret in its field. It stages nothing — DECISIONS.md 17's gesture,
   * unchanged: pressing says WHICH entry, Enter says what it should say.
   */
  private inlineValues(items: ValueView[], path?: string): HTMLElement {
    const cell = el('span', 'field-value mono');
    for (const [i, item] of items.entries()) {
      if (i > 0) {
        const sep = el('span', 'field-sep');
        sep.textContent = ' · ';
        cell.appendChild(sep);
      }
      if (path === undefined || path === '') {
        const v = el('span', 'field-item');
        v.textContent = item.text;
        cell.appendChild(v);
        continue;
      }
      const at = entryPath(path, i);
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'field-item mono';
      button.textContent = item.text;
      button.dataset.path = at;
      // The index is in the accessible NAME, because the text is not enough to
      // tell two entries apart and a list with repeats is the common case:
      // `command: [sh, -c, sh]` announces "sh, sh" twice otherwise.
      button.setAttribute('aria-label', `${at} — ${item.text}. Opens the list to edit this entry`);
      button.title = `${at} — open the list to edit this entry`;
      button.addEventListener('click', () => {
        this.expanded.add(path);
        this.focusAfterRender = at;
        if (this.lastInspection) {
          this.render(this.lastInspection);
        }
      });
      cell.appendChild(button);
    }
    return cell;
  }

  /**
   * The entries of a declared sequence or mapping, each with its own line.
   *
   * `parent` is the sequence's own config path, and it is what the entry
   * addresses are built from. It is threaded rather than derived because a
   * sequence entry has no `path` on the wire — `internal/schema` gives it none
   * — so the address is constructed, once, by `entryPath` in shared/protocol.ts.
   */
  private valueTree(
    value: ValueView,
    depth: number,
    diag: EntryDiagnostics,
    parent?: string,
  ): HTMLElement {
    const list = el('div', 'value-tree');
    if (depth > MAX_VALUE_DEPTH) {
      return list;
    }
    if (value.kind === 'sequence') {
      for (const [i, item] of (value.seq ?? []).entries()) {
        list.appendChild(
          this.entryRow(
            String(i),
            item,
            depth,
            true,
            diag,
            parent === undefined || parent === '' ? undefined : entryPath(parent, i),
            parent,
          ),
        );
      }
      return list;
    }
    for (const entry of value.entries ?? []) {
      list.appendChild(this.entryRow(entry.key, entry.value, depth, false, diag, entry.path));
    }
    return list;
  }

  /**
   * Whether this pane may add an entry to a list, or remove one — story 9.2.
   *
   * The core's answer, never a guess. A list whose bytes are here reports
   * `not-scalar` — "this key holds a collection, not a value" — which is
   * precisely the state an entry operation is for. A list that arrives whole
   * through a merge key reports `inherited`, and appending to it would either
   * write on the anchor (changing every service that merges it) or declare the
   * key locally, which REPLACES the merged sequence rather than extending it.
   * Neither is what the reader pressing `+` asked for, so nothing is offered.
   */
  private canEditList(path: string | undefined): boolean {
    if (path === undefined || path === '') {
      return false;
    }
    const a = this.availability[path];
    if (a === undefined) {
      return true;
    }
    return a.editable || a.reason === 'not-scalar' || a.plan === 'insert_key';
  }

  /**
   * `+ add an entry` — a field at the foot of an expanded list.
   *
   * A field rather than a button, and for the reason DECISIONS.md 17 gives: a
   * control that added an empty entry would write a `- ` nobody typed. Enter
   * stages; the field is empty on every render, so a second entry is a second
   * Enter rather than an edit to the first.
   */
  private addEntryRow(path: string, depth: number): HTMLElement {
    const wrap = el('div', 'field-block is-nested is-add-entry');
    indent(wrap, depth);
    const row = el('div', 'field');
    const key = el('span', 'field-key field-key-add');
    key.textContent = '+ entry';
    row.appendChild(key);
    row.appendChild(
      valueInput({
        value: '',
        placeholder: 'add an entry',
        label: `${path} — type a new entry and press Enter to stage it at the end of the list`,
        fieldKey: `${path}[+]`,
        commit: (next) => {
          if (next.trim() === '') {
            return;
          }
          this.callbacks.send({ type: 'addEntry', path, value: next });
        },
      }),
    );
    wrap.appendChild(row);
    return wrap;
  }

  /**
   * Whether this pane may add a key to this mapping.
   *
   * TWO answers, and neither of them is this pane's own opinion.
   *
   * The first is the SPECIFICATION's, and it is the whole reason this control
   * exists. `environment`, `labels`, `build.args`, `sysctls`, `extra_hosts`,
   * `deploy.labels` permit any key, so `stack/schema` has no `available, not
   * set` list for them — and that list is the only route this pane ever offered
   * to a key the file does not have. A reader looking at `environment` with
   * three keys had nowhere to add a fourth.
   *
   * Which mappings those are is `SchemaField.free_form`, generated from
   * `schema/compose-spec.json` by `internal/schema`. It is READ, never
   * enumerated: a hand-maintained list of "the free-form keys" is a list of
   * Compose keys under another name, which is what AD-20 forbids and what story
   * 5.2's "runtime-derived, no hand list" criterion rules out. It is also
   * simply wrong within a release — `build.args` is free-form and lives one
   * level down, and the next Compose adds one nobody here will hear about.
   *
   * The second is the CORE's, and it is the same one `canEditList` asks: a
   * mapping that arrives whole through `<<: *defaults` reports `inherited`, and
   * a key written on this service would REPLACE the merged mapping rather than
   * extend it. Not what the reader pressing `+` asked for, so nothing is
   * offered. The mapping's own path reaches `stack/editable` because
   * `host/inspect.ts` now asks about it, which is the half of this that is not
   * visible from here.
   */
  private canAddKey(field: SchemaField): boolean {
    return (
      field.free_form === true &&
      field.value?.kind === 'mapping' &&
      !ADD_BLOCKS.has(field.path) &&
      this.canEditList(field.path)
    );
  }

  /**
   * `+ key` — a key field and a value field at the foot of a free-form mapping.
   *
   * The same shape as `+ entry` and for the same reasons: fields rather than a
   * button, because a control that added an empty key would write a `KEY:`
   * nobody typed (DECISIONS.md 17); Enter stages and Escape closes; and both
   * fields are empty on every render, so a second key is a second Enter rather
   * than an edit to the first.
   *
   * TWO fields rather than one, because a mapping entry is two things the file
   * writes separately — and the alternative, one field holding `KEY=value`, is
   * the LIST form's syntax. Offering it here would invite the reader to type a
   * list entry into a mapping, and the pane would either refuse it or, worse,
   * split it on the first `=` and write a value the reader did not type.
   *
   * It does NOT validate what was typed. Whether a name survives being written
   * as a bare key is `internal/edit`'s answer and a second one here would
   * disagree with it — the same discipline `addform.ts` keeps for a service
   * name. See the report accompanying this change: `bareKey` runs on the
   * `stack/add` path and NOT on `insert_key`, so today the core's guard for
   * this path is the re-parse in `edit.validate`, which catches a key that
   * breaks the document and not one that reads back as something else.
   */
  private addKeyRow(path: string, depth: number): HTMLElement {
    const wrap = el('div', 'field-block is-nested is-add-entry is-add-key');
    wrap.dataset.addKey = path;
    indent(wrap, depth);
    const row = el('div', 'field');

    // The key cell is an INPUT in the key column — the same cell the keys above
    // it occupy, in the same code face, because what goes in it is bytes that
    // end up in the file (DESIGN.md's monospace rule). `field-key` is what
    // places it in track 1 of the four-track grid; nothing new is placed.
    const key = document.createElement('input');
    key.type = 'text';
    key.className = 'field-key field-input mono';
    key.placeholder = '+ key';
    key.dataset.field = `${path}[+key]`;
    key.setAttribute('aria-label', `${path} — the name of a key to add to this mapping`);

    const value = valueInput({
      value: '',
      placeholder: 'add a value',
      label: `${path} — the value for the key beside it. Press Enter to stage the pair`,
      fieldKey: `${path}[+value]`,
      commit: () => this.commitAddKey(path, key, value),
      // Escape means "close the composer", and the composer is both fields:
      // clearing one and leaving the other half-typed would leave a key with no
      // value on screen looking like something had been staged.
      revert: () => {
        key.value = '';
      },
    });

    key.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        this.commitAddKey(path, key, value);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        key.value = '';
        value.value = '';
      }
    });

    row.append(key, value);
    wrap.appendChild(row);
    return wrap;
  }

  /**
   * Stages the pair, or does nothing.
   *
   * Nothing, when the key is empty — a value with no key is a line this pane
   * would have to invent a name for, and inventing one is the failure story 5.2
   * exists to stop. An empty VALUE is allowed and is not the same case: `KEY:`
   * is what the reader writes for a variable passed through from the
   * environment, and it is what the engine writes for a mapping it is creating.
   */
  private commitAddKey(path: string, key: HTMLInputElement, value: HTMLInputElement): void {
    const name = key.value.trim();
    if (name === '') {
      return;
    }
    this.callbacks.send({ type: 'addKey', path, key: name, value: value.value });
  }

  /** `Remove` for one entry of a list — story 9.2, staged and never written. */
  private removeEntryControl(path: string): HTMLElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'row-action entry-remove';
    button.textContent = 'Remove';
    button.setAttribute('aria-label', `Stage the removal of ${path}`);
    button.title = `Stage the removal of ${path}. Nothing is written until you save.`;
    button.addEventListener('click', () => {
      this.callbacks.send({ type: 'removeEntry', path });
    });
    return button;
  }

  /**
   * A scalar's value cell: an editable input where an edit is safe, a plain
   * span where it is not — story 6.1.
   *
   * Three cases are deliberately NOT editable, and each is a value that does
   * not literally exist at that path in the file:
   *
   *   an alias   the file holds `*name`; splicing over it would rewrite the
   *              reference rather than the anchor;
   *   a null     `~` and an empty value are the same node and replacing one in
   *              place would be guessing which the reader wants;
   *   no path    a sequence element, which carries no config path in this wire
   *              schema and therefore cannot be addressed by an edit.
   *
   * Offering a field the engine would then refuse teaches the reader the tool
   * is unreliable, which is the impression a fidelity engine cannot afford.
   */
  private scalarValue(
    path: string | undefined,
    value: ValueView,
    field?: SchemaField,
  ): HTMLElement {
    const editable =
      value.kind === 'scalar' && path !== undefined && path !== '' && this.writable(path);
    // Story 7.9. A key the file already SETS is the common case for this —
    // `restart: unless-stoped` is a typo someone made because nothing offered
    // them the four words — so the list is not confined to the unset half.
    //
    // Only for a cell the reader can actually type in: an alias, a null and a
    // sequence element render as a read-only span, and hanging a popup off text
    // that cannot be edited would offer a choice the engine would then refuse.
    const combo = field && editable ? this.comboFor(field) : null;
    const search = field && editable ? this.imageSearchFor(field) : null;
    const named = (base: string): string => base + (field ? allowedName(field) : '');
    if (editable && path in this.pending) {
      // A staged edit: the field shows what the reader typed, and the line
      // beneath says what the file still holds. Both halves travel, because
      // showing one without the other misrepresents which is true.
      const staged = valueInput({
        value: this.pending[path],
        label: named(
          `${path} — staged, not yet written. Press Enter to restage, Escape to discard this edit`,
        ),
        fieldKey: path,
        intercept: keyHook(combo, search),
        commit: (next) => this.callbacks.send({ type: 'edit', path, value: next }),
        onCommit: () => {
          this.focusAfterRender = path;
        },
        // Escape means revert TO THE FILE. It used to restore the staged text,
        // which is not a revert — the accessible name promised one thing and
        // the control did another, and the reader had no way to get the file's
        // own value back short of discarding every staged edit they had.
        revert: () => {
          this.focusAfterRender = null;
          this.callbacks.send({ type: 'unstage', path });
        },
      });
      combo?.attach(staged);
      search?.attach(staged);
      return staged;
    }
    if (!editable) {
      const v = el('span', 'field-value mono');
      // Never truncated in the DOM: CSS ellipsis keeps the row scannable and
      // the full text is still selectable and still read by a screen reader.
      v.textContent = value.kind === 'null' ? '~' : value.text;
      if (value.undefined && value.undefined.length > 0) {
        v.classList.add('is-unresolved');
      }
      return v;
    }
    const input = valueInput({
      value: value.text,
      label: named(`${path} — press Enter to stage the change, Escape to revert`),
      fieldKey: path,
      intercept: keyHook(combo, search),
      commit: (next) => this.callbacks.send({ type: 'edit', path, value: next }),
      onCommit: () => {
        this.focusAfterRender = path;
      },
    });
    if (value.undefined && value.undefined.length > 0) {
      input.classList.add('is-unresolved');
    }
    combo?.attach(input);
    search?.attach(input);
    return input;
  }

  /**
   * Whether a field may be typed in — the core's answer, never a guess here.
   *
   * Two states are typeable. A value with real bytes at this path is spliced in
   * place, and a value the file INHERITS is typed here and staged as an insert
   * that overrides the anchor for this one place (decision 21) — which is why
   * `plan` is consulted rather than `editable` alone.
   *
   * Everything else — an alias, an anchored value, a block scalar, a key inside
   * a mapping that arrives whole through a merge — renders read-only WITH the
   * core's sentence under it. Offering a field the engine would refuse teaches
   * the reader the tool is unreliable; offering one and saying nothing is the
   * silent failure CLAUDE.md rule 6 forbids.
   */
  private writable(path: string): boolean {
    const a = this.availability[path];
    if (a === undefined) {
      return true;
    }
    return a.editable || a.plan === 'insert_key';
  }

  /**
   * The sentence under a field about where its value is actually written, and
   * what typing here would therefore do.
   *
   * It is rendered BEFORE any write, on every state that has something to say —
   * including the two the reader can still type in, because "this will be
   * written on the service rather than on the anchor" is a consequence they
   * must not meet for the first time in the diff.
   *
   * Nothing is said for a path the file plainly declares, or for one it plainly
   * does not have: `absent` is the ordinary insert the `available, not set`
   * list already explains, and a note on every unset key in a 90-key list would
   * be noise that buries the four that matter.
   */
  private appendAvailabilityNote(
    wrap: HTMLElement,
    path: string | undefined,
    // The unset branch has already printed its own sentence about a key the
    // file declares with no value — the one that says what rewriting the line
    // costs. Saying it twice under one field is worse than saying it once.
    saidAlready: 'null-value' | '' = '',
  ): void {
    if (path === undefined || path === '') {
      return;
    }
    const a = this.availability[path];
    if (!a || a.editable || a.detail === undefined || a.detail === '') {
      return;
    }
    if (a.reason === 'absent' || a.reason === 'not-scalar' || a.reason === saidAlready) {
      return;
    }
    const note = el('div', 'field-note');
    // The reason is in the text, not only in the styling: `is-readonly` is a
    // colour and a reader who cannot see it gets the same sentence.
    note.classList.add(a.plan === undefined || a.plan === '' ? 'is-readonly' : 'is-override');
    note.textContent = a.detail;
    wrap.appendChild(note);
  }

  /**
   * One entry of a declared mapping or sequence — and, since this defect, the
   * row that carries the pill for a finding anchored at it.
   *
   * `stack/schema` describes `environment` as one field with no children, so
   * every one of its keys arrives here and NOWHERE else. This function never
   * received the findings, so no mapping-entry row could ever carry a pill,
   * which is every anchor `plaintext-credential` and `undefined-variable`
   * produce. The join in shared/join.ts was right the whole time; this row
   * simply never asked it.
   */
  private entryRow(
    key: string,
    value: ValueView,
    depth: number,
    index: boolean,
    diag: EntryDiagnostics,
    path?: string,
    /** The sequence this entry belongs to, when it is one. Story 9.2. */
    list?: string,
  ): HTMLElement {
    const wrap = el('div', 'field-block is-nested');
    indent(wrap, depth);
    if (path !== undefined) {
      wrap.dataset.path = path;
      if (this.staged.has(path)) {
        wrap.classList.add('is-staged');
      }
    }
    const row = el('div', 'field');
    const k = el('span', index ? 'field-key mono' : 'field-key field-key-literal mono');
    k.textContent = index ? `[${key}]` : key;
    row.appendChild(k);

    let addTo: string | null = null;
    if (value.kind === 'scalar' || value.kind === 'alias' || value.kind === 'null') {
      row.appendChild(this.scalarValue(path, value));
      // Story 9.2. Only on a list whose bytes are here, and only on an entry
      // that has an address: the control is the counterpart of the `+` at the
      // foot of the same list, and both are staged rather than written.
      this.appendRowActions(row, path, { value, list });
      wrap.appendChild(row);
      // The findings this row is about to show, worked out BEFORE the
      // resolution sentence rather than after it, because whether that sentence
      // is worth a line depends on whether a rule already said the same thing.
      // Safe to snapshot here and only here: this branch renders no children,
      // so nothing can claim a finding between this line and the loop below.
      this.appendResolution(
        wrap,
        value,
        path !== undefined && path !== ''
          ? findingsForField(diag.findings, path).filter((f) => !diag.claimed.has(findingKey(f)))
          : [],
      );
    } else {
      addTo = this.appendCollection(wrap, row, value, depth, diag, path);
      this.appendRowActions(row, path);
    }

    const prov = provenanceOf(value);
    if (prov) {
      wrap.appendChild(this.provenance(prov, value.alias, value.alias_site));
    }
    this.appendAvailabilityNote(wrap, path);
    if (addTo !== null) {
      wrap.appendChild(this.addEntryRow(addTo, depth + 1));
    }
    this.appendCommentBlock(wrap, path);
    this.appendExtractBlock(wrap, path);

    // A sequence element carries no config path in this wire schema, so nothing
    // can be joined to it and the finding stays on the field above — which is
    // the honest place for it, not a silent drop.
    if (path !== undefined && path !== '') {
      // Re-derived, NOT `mine`: `appendCollection` may have rendered nested rows
      // since, and each of those claims the findings it showed. `mine` is a
      // snapshot from before that happened, and using it here would print a
      // finding on this row that a row underneath it has already printed.
      for (const f of findingsForField(diag.findings, path)) {
        // Deepest row wins, and it wins by construction: the nested tree above
        // has already rendered by the time this loop runs, so a finding
        // anchored at `depends_on.db.condition` is claimed by that row and
        // `depends_on.db` — its parent — skips it. Exactly the rule the field
        // header applies one level up, and the same rule `findingsForNode`
        // applies one level above that.
        if (diag.claimed.has(findingKey(f))) {
          continue;
        }
        diag.claimed.add(findingKey(f));
        this.appendDiagnostic(wrap, row, f, this.linkedPositions(prov, value.alias_site));
      }
    }
    return wrap;
  }

  /**
   * The resolution line beneath a `${VAR}` — story 5.1.
   *
   * The literal stays in the value row above; this says what it means. With no
   * environment established nothing is said at all, because silence is honest
   * and "undefined" would not be.
   */
  private appendResolution(wrap: HTMLElement, value: ValueView, findings: Finding[] = []): void {
    if (!value.env_known || !value.resolved || value.resolved === value.text) {
      if (value.env_known && value.undefined && value.undefined.length > 0) {
        // Said once, by whichever of the two says more.
        //
        // A `${VAR}` that resolves to nothing produced BOTH this sentence and a
        // rule's message, one under the other, about the same variable:
        //
        //   SESSION_SECRET — undefined in .env and the environment
        //   ${SESSION_SECRET} is referenced in one place but is defined nowhere…
        //
        // The rule's message is the longer answer and it is the one that must
        // stay reachable, so when it already names every undefined variable
        // this line is the one that goes. It is dropped ONLY then: a finding
        // about something else, or about one of two variables, leaves the
        // sentence in place, because then it is carrying a name nothing else on
        // the block carries.
        const missing = value.undefined;
        const saidByRule = findings.some((f) => missing.every((v) => f.message.includes(v)));
        if (!saidByRule) {
          const line = el('div', 'resolution is-unresolved');
          line.textContent = `${missing.join(', ')} — undefined in .env and the environment`;
          wrap.appendChild(line);
        }
      }
      return;
    }
    const line = el('div', 'resolution');
    const prefix = el('span', 'resolution-arrow');
    prefix.textContent = '→ ';
    const text = el('span', 'mono');
    text.textContent = value.resolved;
    line.append(prefix, text);
    if (value.undefined && value.undefined.length > 0) {
      line.classList.add('is-unresolved');
      const note = el('span', 'resolution-note');
      note.textContent = ` · ${value.undefined.join(', ')} undefined`;
      line.appendChild(note);
    }
    wrap.appendChild(line);
  }

  /**
   * The provenance line — story 5.3. Always visible, never a hover.
   *
   * Each position is a button, so it is reachable by keyboard and announced as
   * something that can be pressed. Pressing it moves the editor's cursor; the
   * inspector does not move at all.
   */
  private provenance(prov: Provenance, alias?: string, aliasSite?: ValueView['alias_site']): HTMLElement {
    const line = el('div', 'prov');
    line.appendChild(this.provLink(prov.label, prov.file, prov.line, prov.column, 'Reveal'));
    for (const o of prov.overrides) {
      const sep = el('span', 'prov-sep');
      sep.textContent = ' · ';
      const word = el('span', prov.many ? 'prov-word is-many' : 'prov-word');
      word.textContent = 'overrides ';
      line.append(sep, word);
      line.appendChild(this.provLink(o.label, o.file, o.line, o.column, 'Reveal what this replaced'));
    }
    if (alias && aliasSite && aliasSite.line >= 1) {
      const sep = el('span', 'prov-sep');
      sep.textContent = ' · ';
      const word = el('span', 'prov-word');
      word.textContent = `from *${alias} at `;
      line.append(sep, word);
      line.appendChild(
        this.provLink(
          `${shortFile(aliasSite.file)}:${aliasSite.line}`,
          aliasSite.file,
          aliasSite.line,
          aliasSite.column,
          'Reveal the anchor reference',
        ),
      );
    }
    return line;
  }

  /**
   * Every position a provenance line already links to, as `file:line`.
   *
   * What a diagnostic checks its anchors against, so the same position is not
   * printed twice under one row. Includes what the value overrides and the
   * anchor an alias came from: each of those is a link the reader can already
   * press from this block, and a finding pointing at one of them is pointing
   * somewhere they can already get to.
   */
  private linkedPositions(
    prov: Provenance | null,
    aliasSite?: ValueView['alias_site'],
  ): Set<string> {
    const out = new Set<string>();
    if (prov) {
      out.add(`${prov.file}:${prov.line}`);
      for (const o of prov.overrides) {
        out.add(`${o.file}:${o.line}`);
      }
    }
    if (aliasSite && aliasSite.line >= 1) {
      out.add(`${aliasSite.file}:${aliasSite.line}`);
    }
    return out;
  }

  private provLink(
    label: string,
    file: string,
    line: number,
    column: number,
    action: string,
  ): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'prov-link mono';
    button.textContent = label;
    button.title = `${action}: ${file}:${line}`;
    // The position this link goes to, in a form another element can compare
    // against — so a finding anchored where the value already is does not print
    // the same `file:line` twice under one row. Compared as data rather than as
    // rendered text: two links can label the same position differently and
    // `shortFile` is one rename away from making that true.
    button.dataset.at = `${file}:${line}`;
    button.addEventListener('click', () => {
      this.callbacks.send({ type: 'reveal', file, line, column });
    });
    return button;
  }

  /**
   * A finding, inline against the field that caused it — story 5.4.
   *
   * The severity is a WORD as well as a colour. The same finding is also in
   * VS Code's problems panel; we publish there rather than building our own,
   * because the reader already has one and it is already accessible.
   */
  private diagnostic(finding: Finding): HTMLElement {
    const line = el('div', `diag diag-${finding.severity}`);
    line.dataset.key = findingKey(finding);
    // The same pill the field rows carry. It used to be built here a second
    // time, one class short, which is how the two could have said different
    // things about the same finding.
    const pill = this.severityPill(finding);
    const text = el('span', 'diag-text');
    text.textContent = finding.message;
    line.append(pill, text);

    // Every anchor is reachable: a collision is two positions and the reader
    // needs both.
    for (const anchor of finding.anchors) {
      if (!anchor.origin.file || anchor.origin.line < 1) {
        continue;
      }
      line.appendChild(
        this.provLink(
          `${shortFile(anchor.origin.file)}:${anchor.origin.line}`,
          anchor.origin.file,
          anchor.origin.line,
          anchor.origin.column,
          anchor.label,
        ),
      );
    }
    return line;
  }

  /**
   * `available, not set` — story 5.2, and the reason the product exists.
   *
   * Every key, always rendered. If it does not fit, the pane scrolls; it does
   * not truncate, and there is no "show more" anywhere in this function.
   */
  private available(fields: SchemaField[], known: boolean): HTMLElement {
    return this.availableBlock([{ label: '', fields }], known);
  }

  /**
   * `available, not set` — one block, however many groups fed it.
   *
   * A run per group, each introduced by the group's name in the same dim label
   * face the headings use, so `ulimits` still arrives under the word
   * `resources`. A single run (a free-form mapping, or a nested mapping group)
   * prints no label: there is nothing to distinguish it from.
   */
  private availableBlock(runs: AvailableRun[], known: boolean): HTMLElement {
    const block = el('div', 'addable');
    const lead = el('span', 'addable-lead');
    lead.textContent = known ? 'You can also set ' : 'Also permitted here: ';
    block.appendChild(lead);

    const labelled = runs.length > 1 || (runs[0]?.label ?? '') !== '';
    for (const run of runs) {
      // Unlabelled and alone — a nested mapping's keys — stays on the lead's own
      // line. There is no second run for it to be confused with, and a line
      // break before three keys is a line spent on nothing.
      const list = labelled ? el('span', 'addable-run') : block;
      if (labelled && run.label !== '') {
        const tag = el('span', 'addable-group');
        tag.textContent = run.label;
        list.appendChild(tag);
      }
      for (const [i, f] of run.fields.entries()) {
        if (i > 0) {
          const sep = el('span', 'addable-sep');
          sep.textContent = ' · ';
          list.appendChild(sep);
        }
        list.appendChild(this.availableKey(f));
      }
      if (list !== block) {
        block.appendChild(list);
      }
    }
    return block;
  }

  private availableKey(field: SchemaField): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'addable-key mono';
    button.textContent = field.key;
    button.dataset.path = field.path;
    const isStaged = this.staged.has(field.path);
    if (isStaged) {
      // Staged, not written. The file is untouched — story 6.1 owns the write
      // — and saying so is the difference between a promise and a lie.
      button.classList.add('is-staged');
    }
    if (field.support === 'no') {
      button.classList.add('is-unsupported');
    }
    if (field.deprecated) {
      button.classList.add('is-deprecated');
    }

    // The default as a placeholder: the reader learns what happens if they
    // leave the key alone (story 5.2), without having to click it. A default
    // the specification recorded in PROSE is marked as prose — it is a
    // description of the default and not a value, and rendering the two
    // identically is what put `start_interval: interval value` one click away
    // from the reader's file.
    const shown = defaultValue(field);
    if (shown !== '') {
      const hint = el('span', 'addable-default');
      hint.textContent = ` ${shown}`;
      button.appendChild(hint);
    } else if (defaultIsProse(field)) {
      const hint = el('span', 'addable-default is-prose');
      hint.textContent = ` spec says: ${field.default}`;
      button.appendChild(hint);
    }
    if (isStaged) {
      const note = el('span', 'addable-mark');
      note.textContent = ' staged';
      button.appendChild(note);
    }
    const mark = supportNote(field);
    if (mark !== '') {
      const note = el('span', 'addable-mark');
      note.textContent = ` ${mark}`;
      button.appendChild(note);
    }

    const described = [
      field.description ?? '',
      field.type ? `type: ${field.type}` : '',
      shown !== '' ? `default: ${shown}` : '',
      defaultIsProse(field) ? `the spec describes the default as “${field.default}”` : '',
      mark,
      field.deprecated ? 'deprecated in the specification' : '',
    ]
      .filter((s) => s !== '')
      .join('\n');
    button.title = described === '' ? field.key : `${field.key}\n${described}`;
    // The accessible name carries the mark too: a key the installed Compose
    // cannot run must not sound identical to one it can, and a key that opens a
    // group must not sound like one that opens a field.
    button.setAttribute(
      'aria-label',
      [
        field.key,
        isStaged ? 'staged, not yet written' : 'not set',
        isObjectTyped(field) ? 'opens its own keys' : 'opens a field to type in',
        shown !== '' ? `defaults to ${shown}` : '',
        mark,
      ]
        .filter((s) => s !== '')
        .join(', '),
    );

    button.addEventListener('click', () => {
      // OPEN, not stage. The reader has said which key they want, not what it
      // should say, and staging here is what wrote `start_interval: interval
      // value` and `healthcheck: ` into people's files.
      this.focusAfterRender = field.path;
      this.callbacks.send({ type: 'open', path: field.path });
    });
    return button;
  }
}

/** The nested split, in the shape `groupFields` returns for a free-form node. */
/**
 * The keydown pre-hook for a field that may carry two popups.
 *
 * A field can have a value list (story 7.9) and — on `image` — a Docker Hub
 * search (Epic 8). In practice never both, because `image` has no fixed set of
 * values and nothing else is searched for; but "in practice never" is exactly
 * the assumption that stops being true, and two `intercept` props on one input
 * would silently mean the second one wins.
 *
 * First claim wins, and a key nothing claims falls through to `valueInput`'s
 * own handler — which is what keeps Enter staging and Escape reverting.
 */
function keyHook(
  ...controls: ({ intercept(e: { key: string; preventDefault(): void }): boolean } | null)[]
): ((e: { key: string; preventDefault(): void }) => boolean) | undefined {
  const live = controls.filter((c): c is NonNullable<typeof c> => c !== null);
  if (live.length === 0) {
    return undefined;
  }
  return (e) => live.some((c) => c.intercept(e));
}

function pick(
  fields: SchemaField[],
  rendered: (field: SchemaField) => boolean,
): { fields: SchemaField[]; available: SchemaField[] } {
  const out = { fields: [] as SchemaField[], available: [] as SchemaField[] };
  for (const f of fields) {
    (rendered(f) ? out.fields : out.available).push(f);
  }
  return out;
}

/**
 * Escapes a config path for an attribute selector.
 *
 * A path holds dots and brackets — `services.web.ports[0]` — every one of
 * which is a selector operator. CSS.escape is not in every webview runtime, so
 * the quoting is done here rather than assumed.
 */
function cssEscape(value: string): string {
  return value.replace(/["\\]/g, '\\$&');
}

/**
 * How far right this container sits, in whole nesting steps.
 *
 * The stylesheet subtracts `--ind` from the key column, so the value column
 * lands on the SAME x at every depth while the key still steps right. Without
 * it a nested row's value column drifted right by one indent per level and a
 * form of nested maps read as a staircase rather than as two columns.
 *
 * A length, never a colour: the colour-literal guard has nothing to see here.
 */
function indent(element: HTMLElement, level: number): void {
  element.style.setProperty('--ind', `${level * INDENT_STEP}px`);
}

function groupHeading(label: string, count?: number): HTMLElement {
  const head = el('div', 'grp-t');
  const name = el('span', 'grp-name');
  name.textContent = label;
  head.appendChild(name);
  if (count !== undefined && count > 0) {
    // The mockup's group header carries a count of what is set. It counts rows
    // that are ALSO on screen underneath it — never a count standing in for
    // values the reader cannot see, which is the anti-pattern.
    const n = el('span', 'grp-count');
    n.textContent = String(count);
    head.appendChild(n);
  }
  return head;
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  node.className = className;
  return node;
}
