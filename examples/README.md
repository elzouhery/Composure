# Examples

Compose projects for exercising the extension by hand. Nothing here is
fixture data for the corpus harness — that lives in `testdata/`.

| Path | What it is for |
|---|---|
| `webstack/` | The realistic one. Eight services, a network, three volumes, anchors, a build context with a Dockerfile, an undefined variable and a plaintext password. Draws well and has something to say to every later story. |
| `broken/` | Malformed YAML. Should produce a named banner with the parse position, and dim the last good graph rather than clearing it. |
| `empty/` | `services: {}`. Should produce a plain empty state — and no `Add service` button, since authoring is a later phase. |
| `large/` | 500 services, for the N3 check: a resolved topology must open in under two seconds. |

Every one is named `compose.yaml` on purpose. The extension activates on
`workspaceContains` globs, and the fixtures in `testdata/` are named for the
engine harness — `e1-no-trailing-newline.yml`, `01-comments.yml` — so none of
them match and none of them activate anything.

## Running the extension against them

```bash
make extension-core          # build the binary the extension spawns
npm --prefix extension ci
npm --prefix extension run compile
```

Then open `extension/` as the workspace root in VS Code and press **F5**. The
Extension Development Host opens on this `examples/` folder; open
`webstack/compose.yaml` from its file tree.

`extension/TESTING.md` has the full check list. The three worth doing first:

1. **Auto-fit** — every node visible on open, nothing adrift in empty space.
2. **Theme** — switch between a light and a dark theme. Colours follow with no
   reload, because the webview owns no palette of its own.
3. **Positions never enter the file** — drag a node, then run
   `git diff examples/webstack/compose.yaml`. It must be empty. Node positions
   are view state; the canvas is a view of the file, never a second model.

## Without the extension

Everything the extension can do is reachable headlessly, which is the point of
the CLI-before-UI rule:

```bash
go run ./cmd/composure resolve examples/webstack/compose.yaml
go run ./cmd/composure resolve -json examples/webstack/compose.yaml
```

## What these deliberately contain

Each is bait for a later story, so the examples stay useful as the product grows:

- `${SESSION_SECRET}` is referenced and defined nowhere — story 3.6.
- `POSTGRES_PASSWORD: hunter2` is a plaintext credential — story 3.1.
- `scratch` is a declared volume that nothing mounts — story 3.5.
- `legacy-importer` sits behind a `migration` profile and joins no network —
  stories 1.9 and 3.7.
- `<<: *defaults` must survive every edit unexpanded — R4.1, and the reason the
  splice engine exists.
- `docs` builds from a Dockerfile whose base image is a year behind — phase 6.
