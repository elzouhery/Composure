// Activation, wiring, and reaping the subprocess on the way out.
//
// Activation is scoped by `workspaceContains` globs on Compose's own candidate
// names: a workspace with no compose file must not activate this extension, so
// there is no `onStartupFinished` and no bare `onLanguage:yaml` here.

import * as vscode from 'vscode';
import { isComposeFile, isDockerfile, panelAction } from './compose';
import { StackPanel } from './panel';
import { CoreSession } from './session';

let session: CoreSession | undefined;

// Set when the reader closes the panel themselves. Opening a compose file is
// the gesture that opens the panel, but re-opening it on every editor switch
// after someone deliberately closed it is nagging. The command clears this.
let dismissed = false;

export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel('Composure');
  StackPanel.onUserClose = () => {
    dismissed = true;
  };
  context.subscriptions.push(output);

  session = new CoreSession(context.extensionPath, output, (failure) => {
    // A core that died between requests still has to reach the reader. The
    // panel dims the last good graph and names the exit; it does not clear it.
    StackPanel.active()?.showFailure(failure);
  });
  context.subscriptions.push(session);

  context.subscriptions.push(
    vscode.commands.registerCommand('composure.showStack', () => {
      const file = composeFileInFocus();
      if (!file) {
        void vscode.window.showInformationMessage(
          'Composure draws the compose file in the active editor. Open one first.',
        );
        return;
      }
      dismissed = false;
      StackPanel.show(context, session!, file);
    }),
  );

  // Opening a compose file is the gesture that opens the panel.
  //
  // This previously called `StackPanel.active()?.setFile(file)`, which only
  // ever REPOINTED a panel that already existed — and the sole place one was
  // created was the activation check below, which runs before any editor is
  // open in a freshly opened workspace. So activation fired, the reader opened
  // a compose file, and nothing happened. The comment described the intent; the
  // optional chain quietly did less.
  context.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor((editor) => {
      const file = editor?.document.uri.fsPath;
      if (!file || editor.document.uri.scheme !== 'file') {
        return;
      }
      if (isComposeFile(file)) {
        showOrRepoint(context, file);
        return;
      }
      // Story 6.2's second door: opening a Dockerfile shows its stage form.
      // Only into a panel that is already open — activating the whole extension
      // because someone opened a Dockerfile in a repository with no compose
      // file at all is exactly the over-activation the spec's rules forbid.
      if (isDockerfile(file)) {
        void StackPanel.active()?.setDockerfile(file);
      }
    }),
  );

  // A compose file may already be open when activation runs — reopening a
  // window restores editors before the workspaceContains glob fires.
  const active = composeFileInFocus();
  if (active) {
    showOrRepoint(context, active);
  }
}

/**
 * Repoint the panel if one is open, otherwise open it — unless the reader
 * closed it themselves, in which case the command is how they get it back.
 */
function showOrRepoint(context: vscode.ExtensionContext, file: string): void {
  const panel = StackPanel.active();
  switch (panelAction({ hasPanel: panel !== undefined, dismissed })) {
    case 'repoint':
      panel!.setFile(file);
      return;
    case 'open':
      if (session) {
        StackPanel.show(context, session, file);
      }
      return;
    case 'none':
      return;
  }
}

/** The compose file the reader is looking at, if they are looking at one. */
function composeFileInFocus(): string | undefined {
  const doc = vscode.window.activeTextEditor?.document;
  if (doc && doc.uri.scheme === 'file' && isComposeFile(doc.uri.fsPath)) {
    return doc.uri.fsPath;
  }
  const drawn = StackPanel.active()?.drawnFile;
  return drawn;
}

export function deactivate(): void {
  // A leaked core outlives the window that spawned it.
  session?.dispose();
  session = undefined;
}
