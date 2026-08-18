// Publishing findings to VS Code's problems panel.
//
// Story 5.4: we do not build our own. The reader already has one, they already
// know its keybinding, it already filters and sorts and groups by file, and it
// is already accessible to a screen reader. Every problems panel we build is a
// worse version of one the reader knows.
//
// The inspector shows the same findings inline against the field that caused
// them; this is the second surface, not a replacement for the first. A reader
// with the panel open sees the list, a reader with the pane open sees it on the
// setting, and neither has to map one onto the other.

import * as vscode from 'vscode';
import type { Finding, Severity } from '../shared/protocol';
import { positionedAnchors } from '../shared/join';

/** VS Code's severities, ours mapped onto them. */
export function severityOf(s: Severity): vscode.DiagnosticSeverity {
  switch (s) {
    case 'error':
      return vscode.DiagnosticSeverity.Error;
    case 'warning':
      return vscode.DiagnosticSeverity.Warning;
    default:
      // Hint renders as a faint underline with no entry in the panel's default
      // filter. Information is the honest level for advice we want READ —
      // "your version: field is obsolete" is useless if nobody sees it.
      return vscode.DiagnosticSeverity.Information;
  }
}

/**
 * Owns one diagnostic collection for the whole extension.
 *
 * One collection, replaced wholesale on every re-diagnose. Appending would
 * accumulate findings the file no longer produces, and a problems panel that
 * reports a fixed problem is worse than one that reports nothing.
 */
export class Problems implements vscode.Disposable {
  private readonly collection: vscode.DiagnosticCollection;

  constructor() {
    this.collection = vscode.languages.createDiagnosticCollection('composure');
  }

  /**
   * Replaces every published finding with this set.
   *
   * A finding can anchor in more than one file — a port collision across a
   * compose file and its override — so they are grouped by anchor file rather
   * than filed under the entry path.
   */
  publish(findings: Finding[]): void {
    const byFile = new Map<string, vscode.Diagnostic[]>();
    for (const f of findings) {
      // `positionedAnchors` rather than a local filter: the inspector header
      // tells the reader how many entries these findings will produce here, and
      // it can only be right if both sides ask the same question.
      const positioned = positionedAnchors(f);
      for (const [i, anchor] of positioned.entries()) {
        const line = anchor.origin.line - 1;
        const column = Math.max(0, anchor.origin.column - 1);
        const range = new vscode.Range(line, column, line, column + 1);
        // The message is the same sentence the inspector shows. Beyond the
        // first anchor it is prefixed with the anchor's own label, so the
        // second position of a collision reads as the other end of one thing
        // rather than as a duplicate finding.
        const message = i === 0 ? f.message : `${anchor.label}: ${f.message}`;
        const d = new vscode.Diagnostic(range, message, severityOf(f.severity));
        d.source = 'Composure';
        d.code = f.rule;
        const rest = positioned.filter((_, j) => j !== i);
        if (rest.length > 0) {
          d.relatedInformation = rest.map(
            (a) =>
              new vscode.DiagnosticRelatedInformation(
                new vscode.Location(
                  vscode.Uri.file(a.origin.file),
                  new vscode.Position(Math.max(0, a.origin.line - 1), Math.max(0, a.origin.column - 1)),
                ),
                a.label,
              ),
          );
        }
        const list = byFile.get(anchor.origin.file) ?? [];
        list.push(d);
        byFile.set(anchor.origin.file, list);
      }
    }

    // Clear first: a file that produced findings last time and none now must
    // lose its entries, and `set` only touches the URIs it is given.
    this.collection.clear();
    for (const [file, diagnostics] of byFile) {
      this.collection.set(vscode.Uri.file(file), diagnostics);
    }
  }

  /**
   * The rules did not run, and the panel says so.
   *
   * This is the difference between "your stack is clean" and "we could not find
   * out", and until now the product could only say the first. A failed
   * `stack/diagnose` published an empty array, `publish` cleared the collection,
   * and the reader got a spotless problems panel plus a `console.warn` nobody
   * reads. An empty panel is a claim; this is the retraction of that claim.
   *
   * Warning rather than Error: nothing in the reader's file is known to be
   * wrong. What is wrong is that we cannot tell them either way, and that is
   * exactly what the message says.
   */
  unavailable(file: string, detail: string): void {
    const range = new vscode.Range(0, 0, 0, 1);
    const d = new vscode.Diagnostic(
      range,
      `Composure could not run its checks on this stack: ${detail}. ` +
        'Nothing here is a clean bill of health — no rule ran, so no rule found anything.',
      vscode.DiagnosticSeverity.Warning,
    );
    d.source = 'Composure';
    d.code = 'diagnostics-unavailable';
    this.collection.clear();
    this.collection.set(vscode.Uri.file(file), [d]);
  }

  /** Removes everything we published. Used when the drawn file changes. */
  clear(): void {
    this.collection.clear();
  }

  dispose(): void {
    this.collection.dispose();
  }
}
