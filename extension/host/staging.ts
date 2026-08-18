// Staged edits — intent, held until an explicit press.
//
// Nothing here writes. A stage is an `EditOp` the core can execute, recorded
// against a file, with the byte range it was staged against. `Save to <file>`
// hands the whole list to `stack/apply` and this store is emptied; every other
// path through the product leaves the file exactly as it was.
//
// AD-19 is why this is a list of OPERATIONS against config paths and not a
// mutated model: the webview owns exactly two things, view state and staged
// edits, and everything else is re-fetched from the core. A staged edit is an
// intention to change a named path, so a re-resolve that moves the file cannot
// leave a stale value sitting in a pane pretending to be the file's contents.
//
// Each stage carries the range it was recorded against, and the core compares
// that against the file at write time. If it has moved, the write is REFUSED
// and the stage is discarded — never rebased. Rebasing means guessing what the
// reader meant about a document they have since changed, and a wrong guess
// writes damage into a file they trusted us with.
//
// It is in-memory on purpose. A staged edit that survived a window reload would
// be an edit the reader forgot they had asked for, held against a file that may
// have moved underneath it.

import type { EditOp, Finding } from '../shared/protocol';

/**
 * The rule id the core uses for a `build:` naming a Dockerfile that is not
 * there. Story 6.3: the node renders as missing, and the fact that it is
 * missing comes from the core's own diagnostic rather than from a stat() in
 * here — two answers to "does this file exist" would disagree with the problems
 * panel the first time a path was interpolated.
 */
export const RULE_DOCKERFILE_MISSING = 'build-dockerfile-missing';

/** One staged edit: what the core will do, and what to call it on screen. */
export interface StagedEdit {
  /**
   * What this stage is keyed by, so staging the same thing twice replaces
   * rather than duplicates. For a YAML operation it is the config path, which
   * is also what the inspector marks as staged; for a Dockerfile operation it
   * is `stage:0` or `instruction:4`.
   */
  key: string;
  /** One line naming the change, for the pending strip. */
  label: string;
  op: EditOp;
}

export class Staging {
  private byFile = new Map<string, Map<string, StagedEdit>>();

  /**
   * Records intent, replacing any earlier stage on the same key.
   *
   * Replacing rather than appending is the right call for a field the reader
   * edits twice: two `replace_scalar` operations against one path would apply
   * in order and the first would be invisible, which is a diff that does not
   * match what the pane says.
   */
  set(file: string, entry: StagedEdit): void {
    const forFile = this.byFile.get(file) ?? new Map<string, StagedEdit>();
    forFile.set(entry.key, entry);
    this.byFile.set(file, forFile);
  }

  /** Drops one stage. Returns false when there was nothing to drop. */
  remove(file: string, key: string): boolean {
    const forFile = this.byFile.get(file);
    if (!forFile?.delete(key)) {
      return false;
    }
    if (forFile.size === 0) {
      this.byFile.delete(file);
    }
    return true;
  }

  has(file: string, key: string): boolean {
    return this.byFile.get(file)?.has(key) ?? false;
  }

  /** The staged keys for a file, in the order they were staged. */
  paths(file: string): string[] {
    return Array.from(this.byFile.get(file)?.keys() ?? []);
  }

  entries(file: string): StagedEdit[] {
    return Array.from(this.byFile.get(file)?.values() ?? []);
  }

  /** The operations, in staging order — exactly what a request carries. */
  ops(file: string): EditOp[] {
    return this.entries(file).map((e) => e.op);
  }

  count(file: string): number {
    return this.byFile.get(file)?.size ?? 0;
  }

  /**
   * Drops everything staged against a file.
   *
   * Called when the file changes on disk, and after a successful write. A stage
   * held against a document that has moved is discarded rather than rebased.
   */
  clear(file: string): boolean {
    return this.byFile.delete(file);
  }
}

/** The file, named the way a reader would name it: the last segment. */
export function fileName(file: string): string {
  const parts = file.replace(/\\/g, '/').split('/');
  return parts[parts.length - 1] ?? file;
}

/**
 * The write button's full text — `Save to compose.yml`.
 *
 * EXPERIENCE.md: a control says exactly what happens, and it names the file,
 * because in a multi-file project that is the question the reader actually has.
 * It is a pure function so the wording is tested rather than trusted.
 */
export function saveLabel(file: string): string {
  return `Save to ${fileName(file)}`;
}

/**
 * One line naming a staged edit, for the strip. Deliberately literal: it uses
 * the file's own key names, never a friendlier rewording, because a renamed key
 * is unsearchable and unlearnable.
 */
export function describeEdit(op: EditOp): string {
  switch (op.operation) {
    case 'replace_scalar':
      return `${op.at} → ${op.value ?? ''}`;
    case 'insert_key':
      return op.value ? `add ${op.key}: ${op.value}` : `add ${op.key}`;
    case 'delete_key':
      return `remove ${op.at}`;
    case 'insert_sequence_entry':
      return `add ${op.value ?? ''} to ${op.at}`;
    case 'set_comment':
      return `${op.where === 'trailing' ? 'comment on the line of' : 'comment above'} ${op.at}`;
    case 'delete_comment':
      return `remove the ${op.where ?? 'above'} comment on ${op.at}`;
    case 'set_base_image':
      return `stage ${op.stage ?? 0} base image → ${op.value ?? ''}`;
    case 'replace_args':
      return `instruction ${op.instruction ?? 0} → ${op.value ?? ''}`;
    case 'insert_instruction':
      return `add ${op.value ?? ''} to stage ${op.stage ?? 0}`;
    case 'insert_stage':
      return op.key
        ? `add stage ${op.key} from ${op.value ?? ''}`
        : `add a stage from ${op.value ?? ''}`;
  }
}

/** The staging key for a Dockerfile stage's base image. */
export function stageKey(stage: number): string {
  return `stage:${stage}`;
}

/** The staging key for one instruction's body. */
export function instructionKey(instruction: number): string {
  return `instruction:${instruction}`;
}

/**
 * The staging key for an instruction being ADDED to a stage — story 7.6.
 *
 * Keyed by stage and by the instruction's first word, so a reader who types a
 * HEALTHCHECK, thinks better of the arguments and types it again replaces their
 * own stage rather than staging two HEALTHCHECKs. Two DIFFERENT instructions in
 * one stage are two keys and both stage, which is the case that matters.
 *
 * The first word is a staging key, not a vocabulary: nothing here decides
 * whether the word is a real instruction, and the core refuses the ones it will
 * not write. The webview builds the same string when it marks a row staged —
 * `webview/stageform.ts` — and both sides pin the literal in their own tests.
 */
export function addInstructionKey(stage: number, text: string): string {
  const first = text.trim().split(/\s+/)[0] ?? '';
  return `add-instruction:${stage}:${first.toUpperCase()}`;
}

/**
 * The staging key for an entry being APPENDED to a list — story 9.2.
 *
 * Keyed by the list AND by the text, the same shape and for the same reason as
 * `addInstructionKey`: a reader adding `8080:80` and then `8443:443` to
 * `ports` has staged two entries and both must land, while pressing Enter
 * twice on the same text replaces their own stage rather than publishing the
 * port twice. There is no index in it because there is no index to have — the
 * entry goes on the end, and naming a position the list has not got is the
 * refusal `entry-index` exists for.
 */
export function addEntryKey(path: string, value: string): string {
  return `add-entry:${path}:${value}`;
}

/** The staging key for one entry being removed — story 9.2. */
export function removeEntryKey(path: string): string {
  return `remove-entry:${path}`;
}

/*
 * The staging key for a comment — story 9.1 — is `commentKey` in
 * shared/protocol.ts, re-exported here so this module still names every key
 * the panel stages by. It lives in `shared` because the WEBVIEW needs it too:
 * the comment block names its two fields by it, and a second spelling would
 * mean a field saying "staged" about somebody else's stage.
 */
export { commentKey } from '../shared/protocol';

/** The staging key for a stage being added — story 7.7. */
export function addStageKey(image: string, name: string): string {
  return `add-stage:${name !== '' ? name : image}`;
}

/**
 * The config path an insert creates: `insert_key` at `services` with the key
 * `cache` adds `services.cache`, and at the document root it adds `services`.
 *
 * It is the staging key for a declaration (stories 7.3 and 7.4), chosen so that
 * it is the same key `stageValue` gives an inserted leaf: the reader who
 * declares `cache` twice replaces their own stage rather than staging two, and
 * the inspector marks the path they see as staged. An operation that is not an
 * insert keys by its own path, which is the only thing it has.
 */
export function addedPath(op: EditOp): string {
  if (op.operation !== 'insert_key') {
    return op.at ?? '';
  }
  return op.at ? `${op.at}.${op.key ?? ''}` : (op.key ?? '');
}

/**
 * The mapping a key would be inserted into: `services.db.healthcheck` is added
 * under `services.db`.
 *
 * The last segment is dropped, index brackets included, because
 * `services.web.ports[3]` sits under `services.web.ports`. Anything that leaves
 * nothing behind — a single top-level key — returns the empty path, which the
 * core reads as the document root.
 */
export function parentOf(path: string): string {
  const bracket = path.lastIndexOf('[');
  const dot = path.lastIndexOf('.');
  const cut = Math.max(bracket, dot);
  if (cut <= 0) {
    return '';
  }
  return path.slice(0, cut);
}

/**
 * The staged value for each path that has one, so an edited field can show what
 * the reader typed rather than what the file still says.
 *
 * Only `replace_scalar` contributes: an insert has no previous value to stand
 * against, and its key is already marked staged in the `available, not set`
 * list where the reader clicked it.
 */
export function pendingValues(entries: StagedEdit[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const e of entries) {
    if (e.op.operation === 'replace_scalar' && e.op.at) {
      out[e.op.at] = e.op.value ?? '';
    }
  }
  return out;
}

/**
 * Config paths of Dockerfile nodes the core reported as missing (story 6.3).
 *
 * Derived from the findings rather than from a stat() here: the core already
 * decided which build sections name a file that is not there, and it decided it
 * with the same path arithmetic that opens the file. A second answer in
 * TypeScript would disagree the first time a context held a `${VAR}`.
 */
export function missingDockerfileNodes(findings: Finding[]): string[] {
  const out = new Set<string>();
  for (const f of findings) {
    if (f.rule !== RULE_DOCKERFILE_MISSING) {
      continue;
    }
    for (const anchor of f.anchors) {
      if (anchor.path) {
        out.add(anchor.path);
      }
    }
  }
  return Array.from(out);
}
