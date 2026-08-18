// Which node the cursor is inside — story 4.3, the direction that did not
// exist.
//
// Pure, and deliberately so. Deciding which node a line belongs to is the whole
// of the graph-follows-cursor feature, and a decision that only exists inside a
// `onDidChangeTextEditorSelection` callback is a decision no test can reach.
// The callback in panel.ts is left with nothing but plumbing.
//
// This computes NOTHING about the stack. Every position it reads is an origin
// the Go core already reported (R1.8), and the only arithmetic is "which of
// these declared lines is the cursor at or after".

import type { GraphNode } from '../shared/protocol';

/** A node and the span of lines the cursor may sit in to mean it. */
export interface NodeRange {
  id: string;
  file: string;
  /** First line of the range, 1-based, inclusive. */
  start: number;
  /** Last line, inclusive. Infinity for the last declaration in a file. */
  end: number;
}

/**
 * The top-level section a config path belongs to: `services.web.ports[0]` →
 * `services`.
 */
function sectionOf(id: string): string {
  const cut = id.search(/[.[]/);
  return cut < 0 ? id : id.slice(0, cut);
}

/**
 * True for a node that OWNS a range rather than sitting inside one: a service,
 * a network, a volume — anything declared directly under a top-level section.
 *
 * A published port and a Dockerfile node are declared inside a service, so they
 * are matched by their exact line instead. Two segments and no index is exactly
 * the shape of `services.web`, and it is the shape topology gives those nodes.
 */
export function isOwner(id: string): boolean {
  return /^[^.[\]]+\.[^.[\]]+$/.test(id);
}

/**
 * The line ranges of every owner node in one file.
 *
 * An owner's range runs from its own declaration to the line before the next
 * one. Where the next owner belongs to a DIFFERENT top-level section, the range
 * stops one line earlier still: the line between them is the section key
 * (`networks:`), which belongs to no service, and stretching the last service
 * over it would report a selection for a line that declares nothing.
 *
 * This is a heuristic over declared positions, not a parse. It is right for the
 * shape compose files actually have, and where it is unsure it selects nothing
 * rather than something — a graph that jumps to the wrong service as the cursor
 * moves is worse than one that does not move at all.
 */
export function nodeRanges(nodes: GraphNode[], file: string): NodeRange[] {
  const owners = nodes
    .filter((n) => n.origin?.file === file && n.origin.line >= 1 && isOwner(n.id))
    .map((n) => ({ id: n.id, line: n.origin.line }))
    .sort((a, b) => a.line - b.line || (a.id < b.id ? -1 : 1));

  const out: NodeRange[] = [];
  for (const [i, owner] of owners.entries()) {
    const next = owners[i + 1];
    let end = Infinity;
    if (next) {
      const sameSection = sectionOf(owner.id) === sectionOf(next.id);
      end = next.line - (sameSection ? 1 : 2);
    }
    // A degenerate range — two declarations on adjacent lines across a section
    // boundary — still covers its own line and no more.
    out.push({ id: owner.id, file, start: owner.line, end: Math.max(end, owner.line) });
  }
  return out;
}

/**
 * The node the cursor is in, or null.
 *
 * An exact hit on a nested node's own declared line wins over the owner that
 * contains it: with the cursor on `- "8080:80"` the reader is looking at that
 * published port, and selecting the service instead would be answering a
 * question they did not ask. The longest matching id wins so the innermost node
 * is chosen; where nothing matches exactly, the owner whose range covers the
 * line is the answer, and where no range covers it, the answer is null —
 * meaning the stack itself, which is what an empty selection already shows.
 */
export function nodeAtCursor(nodes: GraphNode[], file: string, line: number): string | null {
  if (!file || line < 1) {
    return null;
  }
  let exact: string | null = null;
  for (const n of nodes) {
    if (n.origin?.file !== file || n.origin.line !== line || isOwner(n.id)) {
      continue;
    }
    if (exact === null || n.id.length > exact.length) {
      exact = n.id;
    }
  }
  if (exact !== null) {
    return exact;
  }
  for (const range of nodeRanges(nodes, file)) {
    if (line >= range.start && line <= range.end) {
      return range.id;
    }
  }
  return null;
}

/**
 * Every file the drawn graph has a declaration in.
 *
 * Story 4.3's third criterion watches these: a stack is more than one file, and
 * an override changing on disk changes the picture exactly as the entry file
 * does. Sorted and de-duplicated so the watcher set is stable across redraws.
 */
export function sourceFiles(nodes: GraphNode[]): string[] {
  const seen = new Set<string>();
  for (const n of nodes) {
    if (n.origin?.file) {
      seen.add(n.origin.file);
    }
  }
  return [...seen].sort();
}
