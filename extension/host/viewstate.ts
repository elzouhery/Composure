// Node positions and selection: the only state that exists in the view and not
// in the file.
//
// It lives in `workspaceState`, which is per-workspace storage inside VS Code,
// and it is keyed by config path so a re-resolve finds the same node again.
// Nothing here ever reaches a compose file — R2.6 makes the canvas a view of
// the file, and a canvas that writes coordinates into YAML has started
// accumulating truth the file does not contain.

import type * as vscode from 'vscode';
import type { Point } from '../shared/protocol';

export interface StoredView {
  positions: Record<string, Point>;
  selected: string | null;
  /**
   * The graph pane's share of the panel, 0..1. The divider between the canvas
   * and the inspector is draggable and persists per workspace — DESIGN.md's
   * split is the product's identity, and where the reader put it is theirs.
   *
   * Null means never dragged: the pane uses the designed default rather than a
   * number we would have had to invent here.
   */
  split: number | null;
  /**
   * The profiles the reader has switched on — story 4.6, R1.4.
   *
   * View state, exactly like a node position: it changes which question the
   * core is asked, and it never reaches a compose file. Writing it into the
   * YAML would be the canvas accumulating truth the file does not contain
   * (R2.6, AD-19), and a `profiles:` key means something else entirely there —
   * it declares membership, it does not activate.
   *
   * Empty is Compose's own default: only always-active services.
   */
  profiles: string[];
}

const EMPTY: StoredView = { positions: {}, selected: null, split: null, profiles: [] };

export class ViewStateStore {
  constructor(private readonly memento: vscode.Memento) {}

  private key(file: string): string {
    return `composure.view:${file}`;
  }

  get(file: string): StoredView {
    const stored = this.memento.get<StoredView>(this.key(file));
    if (!stored || typeof stored !== 'object') {
      return { ...EMPTY, positions: {} };
    }
    return {
      positions: stored.positions ?? {},
      selected: stored.selected ?? null,
      split: typeof stored.split === 'number' ? stored.split : null,
      // Read defensively: this is a Memento written by an older version of the
      // extension as often as by this one, and a stored view from before story
      // 4.6 has no `profiles` at all. A non-array here would be handed
      // straight to the core as a filter.
      profiles: normaliseProfiles(stored.profiles),
    };
  }

  /**
   * One read-modify-write against a stored view, serialised against every other.
   *
   * Every setter here reads the whole view, changes one field and writes the
   * whole view back, and `Memento.update` is asynchronous. Two of them started
   * before either finished — which is exactly what a profile toggle does, since
   * the webview posts the node positions it captured and then the new profile
   * set — both read the SAME old view, and the second write puts back the first
   * one's old value. The dropped field is silent and looks like a lost drag.
   */
  private async update(file: string, change: (view: StoredView) => StoredView): Promise<void> {
    const run = this.writes.then(async () => {
      await this.memento.update(this.key(file), change(this.get(file)));
    });
    // Kept as the tail whether it settles or throws, so one failed write does
    // not wedge the queue for the rest of the session.
    this.writes = run.then(
      () => undefined,
      () => undefined,
    );
    await run;
  }

  private writes: Promise<void> = Promise.resolve();

  async setPositions(file: string, positions: Record<string, Point>): Promise<void> {
    await this.update(file, (current) => ({ ...current, positions }));
  }

  async setSelected(file: string, selected: string | null): Promise<void> {
    await this.update(file, (current) => ({ ...current, selected }));
  }

  /**
   * The active profile set for a file — story 4.6. Answers whether it MOVED.
   *
   * Stored beside the positions, the selection and the split, and nowhere near
   * the file itself.
   *
   * The comparison happens HERE, inside the queue, and the answer is returned
   * rather than left for the caller to work out. That is the whole point: the
   * caller cannot ask "did this change?" without racing, because
   * `Memento.update` settles on a later turn and `onDidReceiveMessage` is
   * fire-and-forget. A panel that compared the incoming set against
   * `store.get(file).profiles` would, for two toggles one turn apart, read the
   * PRE-TOGGLE set for the second one, conclude nothing had changed, and drop
   * the reader's press on the floor — no write, no redraw, nothing said. Inside
   * the queue, `current` is by construction the value the previous toggle
   * wrote.
   */
  async setProfiles(file: string, profiles: string[]): Promise<boolean> {
    const next = normaliseProfiles(profiles);
    let changed = false;
    await this.update(file, (current) => {
      changed = !sameProfiles(current.profiles, next);
      // Unchanged is written back as-is rather than skipped: `update` is one
      // read-modify-write and an early exit here would need a second path
      // through the queue to stay correct. Writing the same value costs a
      // Memento round trip and cannot lose anything.
      return changed ? { ...current, profiles: next } : current;
    });
    return changed;
  }

  /** Where the reader dragged the divider, clamped to something usable. */
  async setSplit(file: string, ratio: number): Promise<void> {
    await this.update(file, (current) => ({ ...current, split: clampSplit(ratio) }));
  }
}

/** Whether two already-normalised profile sets select the same thing. */
function sameProfiles(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((name, i) => name === b[i]);
}

/**
 * The active profile set, in the one shape everything downstream may see:
 * strings only, trimmed, no blanks, no duplicates, in a stable order.
 *
 * Sorted rather than kept in press order because this set is compared — a
 * redraw that reordered `['prod','debug']` into `['debug','prod']` would look
 * like a different filter to anything holding the previous answer, and the two
 * select exactly the same services.
 */
export function normaliseProfiles(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const out = new Set<string>();
  for (const entry of value) {
    if (typeof entry !== 'string') {
      continue;
    }
    const name = entry.trim();
    if (name !== '') {
      out.add(name);
    }
  }
  return [...out].sort();
}

/**
 * Keeps a stored split usable.
 *
 * A ratio of 0 or 1 is a pane the reader cannot get back by dragging, because
 * there is nothing left to grab. Both panes stay present — DESIGN.md: the
 * inspector never opens or closes, and there is no mode.
 */
export function clampSplit(ratio: number): number {
  if (!Number.isFinite(ratio)) {
    return 0.6;
  }
  return Math.min(0.85, Math.max(0.25, ratio));
}
