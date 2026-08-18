// Reading the core's `stack/schema` and `stack/diagnose` answers, and joining
// them by the config path both already carry.
//
// Nothing here derives anything about a stack. The available-key list comes
// from `internal/schema`, generated from the vendored Compose specification
// (AD-20); the findings come from `internal/diagnose`. This module checks the
// shapes are what the wire promises and matches a finding to the field it is
// about — a string comparison, not a model.
//
// The check is not ceremony. The failure mode of a missing array here is not a
// crash: it is an inspector that renders a service as having nothing set, which
// is indistinguishable from the truth and is exactly the confident wrong answer
// this project exists to avoid. So a malformed answer is refused by name.
//
// This module imports nothing from `vscode`, so the tests can drive it.

import { entryPath } from '../shared/protocol';
import type {
  DiagnoseReport,
  Finding,
  SchemaField,
  SchemaNode,
  StackSchema,
  ValueView,
} from '../shared/protocol';

/** Thrown when the core's answer is not the shape the wire promises. */
export class MalformedSchemaError extends Error {
  constructor(readonly detail: string) {
    super(detail);
    this.name = 'MalformedSchemaError';
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * Validates a `stack/schema` result.
 *
 * `node` is required. An answer without one would leave the pane with nothing
 * to render and no way to say why.
 */
export function readSchema(raw: unknown): StackSchema {
  if (!isRecord(raw)) {
    throw new MalformedSchemaError('the core returned something that is not an object');
  }
  for (const key of ['files', 'profiles']) {
    if (!Array.isArray(raw[key])) {
      throw new MalformedSchemaError(`the core's schema answer has no "${key}" array`);
    }
  }
  if (typeof raw.schema_commit !== 'string' || raw.schema_commit === '') {
    // AD-20: the list's credibility rests on which specification it came from.
    // An answer that cannot say is not one the inspector should present.
    throw new MalformedSchemaError('the core did not say which schema revision the list came from');
  }
  const node = readNode(raw.node);
  return {
    path: typeof raw.path === 'string' ? raw.path : '',
    schema_commit: raw.schema_commit,
    compose_version: typeof raw.compose_version === 'string' ? raw.compose_version : '',
    compose_version_known: raw.compose_version_known === true,
    files: raw.files as StackSchema['files'],
    profiles: raw.profiles as string[],
    version_field: typeof raw.version_field === 'string' ? raw.version_field : undefined,
    node,
  };
}

function readNode(raw: unknown): SchemaNode {
  if (!isRecord(raw)) {
    throw new MalformedSchemaError('the core returned no node to inspect');
  }
  if (!Array.isArray(raw.fields)) {
    throw new MalformedSchemaError('the node has no "fields" array');
  }
  for (const f of raw.fields) {
    if (!isRecord(f) || typeof f.key !== 'string' || typeof f.declared !== 'boolean') {
      throw new MalformedSchemaError('a field has no key, or does not say whether it is declared');
    }
  }
  return {
    path: typeof raw.path === 'string' ? raw.path : '',
    schema: typeof raw.schema === 'string' ? raw.schema : '',
    known: raw.known === true,
    title: typeof raw.title === 'string' ? raw.title : undefined,
    fields: raw.fields as SchemaField[],
    declared_count: typeof raw.declared_count === 'number' ? raw.declared_count : 0,
    available_count: typeof raw.available_count === 'number' ? raw.available_count : 0,
    free_form: raw.free_form === true,
    missing: raw.missing === true,
  };
}

/** Validates a `stack/diagnose` result. */
export function readReport(raw: unknown): DiagnoseReport {
  if (!isRecord(raw) || !Array.isArray(raw.findings)) {
    throw new MalformedSchemaError('the core returned a report with no "findings" array');
  }
  const findings: Finding[] = [];
  for (const f of raw.findings) {
    if (!isRecord(f) || typeof f.message !== 'string' || !Array.isArray(f.anchors)) {
      throw new MalformedSchemaError('a finding has no message or no anchors');
    }
    if (f.anchors.length === 0) {
      // AD-7 is enforced in Go; a finding that reaches here without a position
      // could not be placed on a field or in the problems panel.
      throw new MalformedSchemaError('a finding carries no position');
    }
    findings.push(f as unknown as Finding);
  }
  return {
    path: typeof raw.path === 'string' ? raw.path : '',
    profiles: Array.isArray(raw.profiles) ? (raw.profiles as string[]) : [],
    findings,
  };
}

/**
 * Whether a field holds a mapping, so opening it should show its keys rather
 * than a box to type a value into.
 *
 * The declared shape wins when there is one; otherwise the schema's type, which
 * the specification renders as prose for unions — `object`, `object or string`.
 */
export function looksLikeMapping(field: SchemaField): boolean {
  if (field.value && field.value.kind === 'mapping') {
    return true;
  }
  return /\bobject\b/.test(field.type ?? '');
}

/**
 * The fields whose children have to be fetched before the pane can render them.
 *
 * This is the host half of the add-attribute fix, and it is a decision rather
 * than plumbing, so it lives here where it can be tested without a `vscode`.
 *
 * `stack/schema` at `services.web` reports `healthcheck` with `declared: false`
 * and NO children — the specification's sub-keys are only walked for a path the
 * file actually contains. Asking the core again AT that path returns all seven
 * with `missing: true`, which is the list the reader needs in order to discover
 * `test` and `interval`. Two states need it:
 *
 *   opened or staged   the reader clicked the key; it is a group now.
 *   declared as null   `healthcheck:` alone on a line, which is what a save
 *                      produces, and which used to render as a read-only `~`
 *                      with no route to anything inside it.
 *
 * A field that already has children is left alone: the core walked it, and a
 * second request would replace a real answer with the same answer.
 */
export function fieldsNeedingChildren(
  fields: SchemaField[] | undefined,
  isOpen: (path: string) => boolean,
): SchemaField[] {
  const out: SchemaField[] = [];
  const walk = (list: SchemaField[] | undefined): void => {
    for (const f of list ?? []) {
      const hasChildren = (f.children?.length ?? 0) > 0;
      const declaredNull = f.declared && f.value?.kind === 'null';
      if (!hasChildren && (isOpen(f.path) || declaredNull) && looksLikeMapping(f)) {
        out.push(f);
      }
      walk(f.children);
    }
  };
  walk(fields);
  return out;
}

/**
 * The config paths the pane is about to render a VALUE for, and therefore the
 * paths it has to know the editability of before it draws them.
 *
 * Scalars and nulls only, plus every key the reader has opened. A mapping or a
 * sequence is drawn as a group and its own entries arrive here in their own
 * right, so asking about the container as well would double the request and
 * answer a question no field asks.
 *
 * `opened` is included because an unset key the reader clicked becomes a field
 * with the cursor in it, and the one thing that field must not do is accept a
 * value the engine will refuse — a key inside a flow mapping, most of all.
 *
 * Bounded: a pane that would ask about more than MAX_EDITABILITY_PATHS paths
 * asks about the first of them and no more. The cap is far past any real
 * service (the largest in the corpus renders 93 fields) and exists so that a
 * pathological file cannot turn one selection into an unbounded request.
 */
export const MAX_EDITABILITY_PATHS = 500;

export function editablePaths(fields: SchemaField[] | undefined, opened: string[]): string[] {
  const out = new Set<string>();
  const walkValue = (v: ValueView | undefined, at: string): void => {
    for (const entry of v?.entries ?? []) {
      if (entry.path === '') {
        continue;
      }
      if (entry.value.kind === 'scalar' || entry.value.kind === 'null') {
        out.add(entry.path);
      }
      walkValue(entry.value, entry.path);
    }
    // Story 9.2. A sequence entry carries no path in the wire schema, so the
    // address is constructed — once, by `entryPath`, which the webview calls
    // too. Scalars only: an entry that is a mapping is a group of its own and
    // its keys arrive through `entries` above with paths the core supplied.
    if (at === '') {
      return;
    }
    for (const [i, item] of (v?.seq ?? []).entries()) {
      if (item.kind === 'scalar' || item.kind === 'null') {
        out.add(entryPath(at, i));
      }
      walkValue(item, entryPath(at, i));
    }
  };
  const walk = (list: SchemaField[] | undefined): void => {
    for (const f of list ?? []) {
      const kind = f.value?.kind;
      // A SEQUENCE's own path is asked about as well, which a DESCRIBED
      // mapping's is not, and the difference is a control rather than a field:
      // the pane offers `+ add an entry` on a list, and it must not offer one
      // on a list the file does not write here (`inherited`) or writes in flow
      // style.
      //
      // A FREE-FORM mapping now has the same kind of control — the add-a-key
      // composer — for the same reason, so its own path is asked about too. A
      // mapping the specification describes still is not: its keys arrive as
      // fields with an `available, not set` list of their own, and asking about
      // the container would be a question no control on screen asks.
      //
      // Getting this wrong is not a missing answer, it is a wrong one: a path
      // nothing asked about has no `availability` entry, the pane reads silence
      // as "ordinary", and the composer appears on an `environment` that
      // arrives whole through `<<: *defaults` — where a key written here
      // REPLACES the inherited mapping rather than extending it.
      const freeFormMap = kind === 'mapping' && f.free_form === true;
      if (
        f.path !== '' &&
        (kind === undefined ||
          kind === 'scalar' ||
          kind === 'null' ||
          kind === 'alias' ||
          kind === 'sequence' ||
          freeFormMap)
      ) {
        out.add(f.path);
      }
      walkValue(f.value, f.path);
      walk(f.children);
    }
  };
  walk(fields);
  for (const path of opened) {
    out.add(path);
  }
  return Array.from(out).slice(0, MAX_EDITABILITY_PATHS);
}

/* The join lives in shared/join.ts: the inspector needs the same answer, and
   two implementations of "is this finding about this field" would disagree. */
export {
  findingKey,
  findingsFor,
  findingsForField,
  findingsForNode,
  pathWithin,
  severityCounts,
} from '../shared/join';
