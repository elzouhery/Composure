// Which files this extension considers a compose file.
//
// Deliberately narrow: Compose's own candidate names plus any single-segment
// variant — `compose.override.yml`, `compose.prod.yml`, `docker-compose.ci.yaml`.
// A bare `onLanguage:yaml` activation would open a panel over every Kubernetes
// manifest and CI config in the workspace, which is the accident the spec's
// activation rules exist to prevent.
//
// This regex and the `workspaceContains:` globs in package.json are one
// definition expressed twice, and they must agree. When they did not — the
// globs listed eight literal names while this accepted a variant segment — a
// workspace holding only `compose.prod.yml` never activated the extension at
// all, so nothing here ever ran. The globs are the wider of the two now:
// `compose.{yml,yaml}` plus `compose.*.{yml,yaml}`, and the same for
// `docker-compose`. Over-activation costs a no-op activation; under-activation
// costs the whole feature.

const COMPOSE_NAME = /^(docker-)?compose(\.[A-Za-z0-9_-]+)?\.ya?ml$/;

/** Reports whether a path names a compose file, by filename alone. */
export function isComposeFile(fsPath: string): boolean {
  const name = fsPath.replace(/\\/g, '/').split('/').pop() ?? '';
  return COMPOSE_NAME.test(name);
}

/**
 * What opening a compose file should do to the panel.
 *
 * Extracted from the activation wiring so it can be tested at all: the wiring
 * imports `vscode` and cannot be loaded under `node --test`, which is how the
 * original defect shipped. That code called `StackPanel.active()?.setFile()`,
 * so with no panel open the optional chain silently did nothing and the panel
 * never appeared — the one gesture the whole extension hangs on.
 */
export type PanelAction = 'open' | 'repoint' | 'none';

export function panelAction(state: { hasPanel: boolean; dismissed: boolean }): PanelAction {
  if (state.hasPanel) {
    return 'repoint';
  }
  // Closing the panel is a decision. Re-opening it on the next editor switch
  // would be nagging; the command is how the reader takes it back.
  return state.dismissed ? 'none' : 'open';
}

/**
 * Which files this extension treats as a Dockerfile — story 6.2's second door.
 *
 * BuildKit's own conventions and nothing wider: `Dockerfile`, `Containerfile`,
 * a suffixed variant (`Dockerfile.dev`), and a prefixed one (`api.Dockerfile`).
 * A looser rule would open a stage form over files that merely mention docker,
 * which is the same over-activation the compose rule above exists to prevent.
 */
const DOCKERFILE_NAME = /^(Dockerfile|Containerfile)(\.[A-Za-z0-9_.-]+)?$|\.(Dockerfile|Containerfile)$/;

export function isDockerfile(fsPath: string): boolean {
  const name = fsPath.replace(/\\/g, '/').split('/').pop() ?? '';
  return DOCKERFILE_NAME.test(name);
}
