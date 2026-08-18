# Manual checks — the canvas (4.1–4.5), Epic 5 (the inspector) and Epic 6 (the write path)

No CI job can assert that a webview rendered. This list is the acceptance
evidence for everything on screen, and it is why it lives in the repository
rather than only in a story file. Run it before moving a canvas story to review,
and again whenever the panel, the renderer or the failure paths change.

Everything that can be checked mechanically already is:

```bash
go build ./cmd/... ./internal/... && go vet ./cmd/... ./internal/...
go test ./cmd/... ./internal/...     # includes the JSON-RPC server tests
make gate                            # 15 metrics at or above baseline
make extension                       # core binary + type-check + bundles
npm --prefix extension test          # framing, lifecycle, layout, geometry, inspector, staging, the write path
npm --prefix extension run compile   # type-check and bundle
node extension/harness/rhythm.mjs --check    # the inspector's geometry, rendered
node extension/harness/canvas.mjs --check    # the graph pane's, rendered
node extension/harness/epic9.mjs --check     # Epic 9's three affordances, after the gesture that opens each

# Known core defects the extension works around

Found by driving the real binary over JSON-RPC on 2026-08-12. All three are in
Go, in `internal/edit`, and none of them is fixed here — this extension does not
own those files. Each is written down because the extension's behaviour only
makes sense against it.

1. **`replace_scalar` over a null value corrupts the file.** On
   `healthcheck:` (declared, no value) mid-file, `stack/preview` returns a diff
   that deletes `healthcheck:` AND the following line and emits
   `healthcheck:x restart:` — two lines welded into one. At end of file the same
   operation errors with `computed range NN:NN exceeds source length NN`. The
   range for an empty value is not well defined and the splice runs past it.
   *Workaround:* the pane never sends `replace_scalar` at a path it knows is
   null; it sends `delete_key` then `insert_key`, which is a clean two-line
   diff. `host/panel.test.ts` guards that the branch stays.

2. **`insert_key` with an empty value writes a trailing space.** Adding an
   object key emits `    healthcheck: ` — `od -c` shows `k : SPACE \n`. Nothing
   in TypeScript can prevent it; the value the extension sends is already the
   empty string. *Fix belongs in* `internal/edit`: omit the space when the
   rendered value is empty.

3. **The Dockerfile grammar has no vocabulary to expose.** `internal/dockerfile`
   parses whatever keyword it finds and holds no list of instructions, so there
   is nothing for `stack/dockerfile` to return as "instructions you could add".
   The Dockerfile stage form therefore has NO `available, not set` block, and
   putting a hardcoded instruction list in the extension would be the same
   defect AD-20 forbids for Compose keys. *Fix belongs in* `internal/dockerfile`
   plus a field on `DockerfileForm`.

# Zero runtime npm dependencies, and no colour that is not a theme token.
node -e "const d=require('./extension/package.json').dependencies||{}; if(Object.keys(d).length){process.exit(1)}"
grep -rnE '#[0-9a-fA-F]{3,8}\b|rgb\(|hsl\(' extension/webview extension/host extension/shared
```

The `grep` must print nothing. Both checks also run as tests in the suite, so a
literal added later fails `npm test` rather than waiting for someone to look.

Story 4.5 added a third mechanical layer, in `webview/a11y.ts` and
`webview/a11y.test.ts`. It scans the shipped webview sources rather than listing
what exists today, so a control written next year is checked by the same pass:

- every `<button>`, `<input>`, `<select>` and `<textarea>` carries an accessible
  name — its own text, an `aria-label`, or named children;
- every element that takes a click or pointer handler is either a native control
  or has been given a tabindex, so no capability is pointer-only;
- no rule removes an outline without restoring one on `:focus-visible`;
- every stylesheet rule that paints a severity colour is in a ledger naming what
  supplies the same information **in words**;
- every `--vscode-*` foreground the stylesheet paints is declared against the
  background token it lands on, and every one of those pairings is one VS Code
  contributes.

**That last check is not a measured contrast ratio and does not claim to be.**
`node --test` cannot render a pixel, has no theme loaded, and cannot resolve a
custom property to a colour; any number it printed would be invented. What it
refuses is a pairing the platform never promised, which is where a real contrast
failure in a themed extension comes from. The rest is the human's, below.

## Launching

1. `make extension` from the repository root. This builds
   `extension/bin/<goos>-<goarch>/composure` — where activation looks for it —
   and bundles the TypeScript.
2. Open `extension/` as the workspace root in VS Code and press `F5`, or pick
   **Run Extension** from the debug menu. An **Extension Development Host**
   window opens on `examples/`.
3. In the host window, `File → Open Folder` on any directory holding a compose
   file. `examples/webstack/compose.yaml` is the one to start with; `examples/broken/` and `examples/empty/` cover the banner and empty-state checks.
4. After a rebuild, reload the host with `Ctrl/Cmd+R`.

Where to look when something is wrong:

- `Help → Toggle Developer Tools` in the **host** window — webview console.
- `Output → Composure` in the host window — the core's stderr and the handshake.
- `ps aux | grep composure` — whether a core is running at all.

## The checks

Each one names what must be true. A check that "sort of" passes has failed.

### Happy path

Open `examples/webstack/compose.yaml`. The panel opens **beside** the editor,
not on top of it. Every node carries **one** label — its own name — with what
the file says about it on a second, dim line. No node shows a container name.
Everything is visible and centred without scrolling.

The core reports this file as **13 nodes and 17 edges**: 7 services, 1 network,
3 volumes, 1 published port, 1 Dockerfile; 5 `depends_on`, 7 network, 2 volume,
1 bind, 1 publish and 1 build edge. Count what is on screen against that. A node
or an edge that is in `composure topology examples/webstack/compose.yaml` and not
on the canvas is the defect this check exists to find.

### Node kinds — story 4.2

A network must not look like a service. Confirm, on `examples/webstack`:

- **Services** are the large filled cards on the raised surface.
- **Networks and volumes** are smaller capsules on the editor surface; the
  volume outline is dashed, `configs` and `secrets` dotted.
- The **published port** is a square box on the input surface, reading
  `8080 → 80/tcp`.
- The **Dockerfile** node hangs directly below `docs`, smaller, dashed, in the
  Dockerfile orange (`charts.orange`) — the only identity colour in the product.
  Clicking it selects it like any other node; opening the stage form is 6.3.
- Nothing anywhere shows a count in place of a value.

### Edge kinds — story 4.2

The legend strip under the header names exactly the edge kinds the drawn stack
declares — for `webstack`: `depends_on`, `network`, `volume`, `bind`, `publish`,
`build`. Kinds are told apart by dash pattern and weight, never by colour, so
this must survive a high-contrast theme.

Then check the four that are easy to get wrong:

1. Every `depends_on` edge carries its **condition** legibly beside it.
   `gateway → web` reads `service_healthy`; `gateway → docs` reads
   `service_started`. Which condition it is decides whether the stack starts, so
   an unlabelled dependency edge is a failed check.
2. The **bind mount** on `gateway` is a marker line on the node carrying the
   host path (`./gateway/nginx.conf`), **not** a line from the box back to
   itself.
3. The **build** edge runs from `docs` to its Dockerfile node, in orange.
4. Arrowheads point from the dependent to the dependency.

### Layered layout — story 4.2

`gateway` above `web` above `api` above `db` and `cache`; the published port
above `gateway`; the network and volumes at the bottom. Dependency order reads
top to bottom, and the eye can follow one chain from the entry point down.

### A cycle, and a reference that goes nowhere

Neither has an example project yet, so write one:

```yaml
services:
  a: {image: a, depends_on: [b]}
  b: {image: b, depends_on: [a]}
  c: {image: c, depends_on: [gone]}
  gone: {image: g, profiles: [never]}
```

- The layout **does not break**. `a` and `b` share a row (a cycle's members
  share a layer) with edges running sideways between them, and both nodes carry
  a `dependency cycle: a → b` line in warning colour.
- `c` carries `unresolved depends_on gone — filtered by profile`. The reference
  is **visible**, not silently dropped: a relation that disappears when a
  profile changes, with nothing said, is the graph lying by omission.
- Severity is in the words as well as the colour, in both themes.

## The inspector — Epic 5

The right pane. It is docked, permanently visible, and has no mode: nothing
opens it and nothing closes it. Everything below is on `examples/webstack`
unless it says otherwise.

Mechanically checkable first — run this and keep the numbers, because the
manual checks compare against them:

```bash
go run ./cmd/composure schema examples/webstack/compose.yaml            # every node
go run ./cmd/composure schema -at services.db examples/webstack/compose.yaml
go run ./cmd/composure diagnose examples/webstack/compose.yaml
```

`services.db` reports **6 declared and 87 available and not set**, and its
`healthcheck` group reports `test`, `interval` and `retries` set with `disable`,
`start_interval`, `start_period` and `timeout` available. Whatever is on screen
must equal that. A key in the CLI's list and not in the pane is the defect this
check exists to find.

### The split — story 5.1

The panel divides: graph left at roughly 60%, inspector right. Drag the divider
— both panes stay present and neither can be dragged away. Tab to the divider
and use the arrow keys; `Home` and `End` go to the extremes. Reload the window:
the divider is where you left it, and `git diff` on the compose file is empty.

### Values, never bare keys — story 5.1

Select `db`. Under **environment**, all three entries show their **values**:
`POSTGRES_DB shipyard`, `POSTGRES_USER shipyard`, `POSTGRES_PASSWORD hunter2`.
There is no "3 keys" anywhere. Select `web` and confirm the same for its four
entries, and that `healthcheck.test` shows both elements of its list rather
than the word `list` alone.

Every value is in the **editor** font and every label and sentence is in the UI
font. Change the editor font family in settings and confirm the split holds:
what moves is in the file, what does not is prose about it.

### `${VAR}` — story 5.1

`web`'s `SESSION_SECRET` reads the literal `${SESSION_SECRET}` on the value
line, with the resolution beneath it. Nothing defines it, so the line names it
as undefined in warning colour — the value still renders, because an
unresolvable variable is a finding and not an error.

Now `export SESSION_SECRET=abc` and restart VS Code (the core reads the
environment at spawn). The literal is unchanged and the line beneath now reads
`→ abc`. Both are on screen at once, always: the literal is what the file says
and the resolution is what it means.

### Nothing selected — story 5.1

Press `Escape` in the graph. The inspector does **not** empty: it shows the
stack — the source file list, the declared profiles (`migration`), and the
top-level `services`, `networks` and `volumes` with their entries.

### `available, not set` — story 5.2

Still on `db`: the pane is in **named groups** — `image`, `command`,
`environment`, `network`, `storage`, `health`, `resources`, `metadata`, `other`
— and each group ends with its own unset keys, not one list of ninety at the
foot of the pane. `healthcheck` is under `health`, `ulimits` under `resources`.
Measured against the real core on a one-service file: 93 fields across 9 groups.

Every key is there. There is no "show more", no "add property" menu and no
truncation anywhere; `webview/testdom.test.ts` asserts all ninety render.

A key whose default the specification records as a VALUE shows `defaults to …`.
A key whose default the specification only describes in PROSE — `interval`,
`retries`, `start_interval`, `start_period`, `timeout` inside `healthcheck` —
shows `the spec describes the default as “…”`, dotted-underlined. That
distinction is load-bearing: `start_interval`'s recorded default is the string
`interval value`, which is a description and not a duration, and one click used
to write it into the file.

Tab through the list: every key is a real tab stop with a visible focus ring,
and its accessible name says the key, that it is not set, and its default.

Confirm the list is **generated**: `grep -rn "healthcheck" extension/webview
extension/host extension/shared` finds no list of Compose keys. The only list
is `schema/compose-spec.json`, pinned at the commit named in
`schema/PROVENANCE.md`, and the pane's header line names that commit and the
`docker compose` on the machine.

### Compose compatibility — story 5.2, AD-21

The header reads `spec <commit> · compose <version>` when a binary is present.
Keys newer than it — `develop`, `provider`, `models` — are **shown** with
`needs Compose <x>` beside them, never hidden. Rename `docker` off `PATH` and
reload: the header reads `no docker compose found — nothing marked
unsupported`, and every key is offered plain. We degrade to useful, not to
empty.

### The obsolete `version:` field — story 5.2

Add `version: "3.8"` to the top of a compose file and save. The pane says the
field is ignored and can be deleted, a hint appears in the problems panel, and
the available list is **identical** to what it was — the field selects nothing.

### Provenance — story 5.3

Under every value is a line reading `compose.yaml:<line>`, always visible.
There is no tooltip anywhere carrying it. `db`'s `restart` and `networks` came
through the `<<: *defaults` merge key, so their lines point at the anchor and
also carry `from *defaults at compose.yaml:<line>` — both positions, because
neither is derivable from the other.

Click a provenance line. The editor's cursor moves to that position and selects
the range **and the inspector does not move**: same selection, same scroll
position, same pane. That is the whole difference from the incumbent's
`Reveal in YAML`, which replaces the UI with the text file.

To see an override, create `compose.override.yml` beside the file with a
different `image:` for one service — with multi-file merge, the line reads
`compose.yml:12 · overrides :7` and both halves click through.

### Diagnostics — story 5.4

`db`'s `POSTGRES_PASSWORD` carries a pill reading **hint** and the sentence the
rule wrote, inline, against that field. `web`'s `depends_on` on `api` and the
plaintext credential on `api` behave the same. Severity is a **word** as well
as a colour — confirm in a monochrome or high-contrast theme that nothing is
lost.

Open `View → Problems`. Every finding is there too, sourced `Composure`, with the
rule id as its code and the other end of a two-position finding under
*related information*. We publish into VS Code's panel and build none of our
own. Close the panel and reopen the compose file: the entries are not
duplicated. Point the panel at a different compose file: the old file's
problems are gone.

### The badge on the node — story 5.4

Before selecting anything, `db`, `api` and `web` carry a small filled circle
top-right with a count in it, coloured by the **worst** severity present. Hover
one: the tooltip spells it out (`1 hint`), and a screen reader gets the same
from the node's accessible name. A clean node carries no circle at all.

### Clicking an unset key gives you somewhere to type — story 5.2

This is the story's last acceptance criterion and it did not work before
2026-08-12: the key was staged, the pane rebuilt, and the key came back as a
BUTTON, with focus restored onto that button. There was no code path anywhere
that rendered an editable control for it, before or after a save.

**A scalar key.** Click `restart` on a service that has none. It leaves the
`available, not set` list and becomes a field in the `health` group, dashed, with
the caret already in it. Type `always` and press `Enter`: the strip shows one
line added. Press `Escape` in an untouched field instead and the key goes back
to the list. Nothing is staged until you type — `git diff` is empty and the
pending strip reads `no pending changes`.

**An object key.** Click `healthcheck`. It becomes a GROUP carrying its own
seven keys and its own `available, not set` line, because "type a value for
healthcheck" is not a question with an answer. The group says `Not in the file
yet.` Click `test` inside it, type a command and press `Enter`: the strip shows
**two** lines added — `healthcheck:` and `test:` staged together — and still
nothing is written.

**After a save.** With `healthcheck:` written and empty, the key renders as a
group with its six remaining unset keys, not as a read-only `~`. A scalar
declared with no value (`restart:` alone on a line) renders as a field you can
type in, with a note saying the line is rewritten at the foot of its mapping.

**Discarding one edit.** A staged field carries `Discard this edit`, and
`Escape` in a staged field does the same thing — it reverts to what the FILE
says, not to the stage. Before this, `unstage` existed in the protocol, in the
host and in `Staging.remove`, and nothing in the webview ever sent it: three
staged edits could be discarded all together or not at all.

### The pending strip is docked to the inspector

The strip sits at the foot of the INSPECTOR pane, not spanning the panel.
With nothing staged it reads `no pending changes` and keeps its place, rather
than vanishing — its absence has to be legible (DESIGN.md:186). Open a
Dockerfile: the strip moves with the view.

### A failed diagnose does not empty the problems panel

Make `stack/diagnose` fail (point `composure.corePath` at a build with the rule
registry broken, or kill the core between the topology and diagnose calls). The
problems panel must show **one Composure warning** saying the checks did not run —
NOT an empty panel. The graph toolbar says `Checks did not run: …`, and the
stack header says `checks did not run` where it would say `2 warnings`.

Before this, the catch returned `[]`, the panel published `[]`,
`DiagnosticCollection.clear()` emptied it, and the reader got a spotless
problems panel plus a `console.warn` nobody reads.

### An inspector that cannot be filled

Kill the core with a service selected. The pane says what failed and shows the
detail; it does not go blank and it does not keep showing the previous
service's fields. Press `Retry`; the graph and the pane both come back.

## The write path — Epic 6

**Work on a copy.** `cp -r examples/webstack /tmp/webstack && cd /tmp/webstack
&& git init && git add -A && git commit -m base`, and open that folder in the
host window. Every check below ends in `git diff`, and a check that reports the
wrong diff has failed even if the panel looked right.

Mechanically checkable first. These are the same code path the panel's buttons
use — `composure preview` is `stack/preview` and `composure apply` is `stack/apply` —
so a number here is the number the panel must show:

```bash
go run ./cmd/composure preview -op replace_scalar \
    -at services.web.image -value ghcr.io/shipyard/web:2.5.0 /tmp/webstack/compose.yaml
go run ./cmd/composure dockerfile /tmp/webstack/docs/Dockerfile
go run ./cmd/composure dockerfile -at services.docs.build /tmp/webstack/compose.yaml
```

The first prints a diff of exactly **two** lines and leaves `git diff` empty.
The second and third print the **same** two-stage form: `build` on
`node:18-alpine`, `runtime` on `nginx:1.27-alpine`.

### Editing a value stages it — story 6.1

Select `web`. Click the value beside `image`. It focuses and the text selects —
there is no edit mode, no pencil icon and nothing to click first. Type
`ghcr.io/shipyard/web:2.5.0` and press `Enter`.

- The pending strip appears at the foot, naming `compose.yaml`.
- The diff in it is **two lines**: one `-`, one `+`. Count them. The `---` and
  `+++` header lines are not changes and are not coloured as though they were.
- The trailing comment, the quoting style, the blank lines and the indentation
  in the diff's context lines are **identical** on both sides.
- The field shows what you typed, with `staged — the file still says
  ghcr.io/shipyard/web:2.4.1` beneath it. Both halves are on screen at once.
- `git diff` is **empty**, the editor tab shows **no** dirty indicator, and the
  file's mtime has not moved. Nothing has been written.

Press `Escape` in a field instead of `Enter`: the value reverts, the strip does
not change, and nothing is staged.

### The write — story 6.1

With that edit staged, press `Save to compose.yaml`. The button names the file;
if it says anything else, that is the defect.

- `git diff` now shows **exactly two lines changed**, and
  `git diff --numstat` reads `1  1  compose.yaml`.
- The editor's gutter marks one line. Nothing else in the file moved.
- The strip is gone and the pane redraws from the file.

Then `git checkout compose.yaml` to restore.

### Discard writes nothing

Stage two edits — a value and an unset key. The strip says `2 edits` and the
diff shows both. Press `Discard`: the strip goes, both fields revert, and
`git diff` is **empty**.

### A stale range is discarded, never rebased — story 6.1, AD-19

This is the check the whole epic exists for.

1. Select `web` and stage an `image` change. Do **not** save.
2. Switch to the text editor and add a line at the **top** of `compose.yaml` —
   `# a note` — and save. Every byte below it has just moved.
3. The panel re-resolves. The strip is **replaced by a message** saying the
   staged edits were discarded because the file changed on disk and a stale
   range is never written.
4. `git diff` shows **only** the line you typed. The staged edit is gone and was
   not applied at the old offset, at the new offset, or anywhere else.

A panel that silently kept the stage, or that saved it into the moved file, has
failed this check and the product's central claim with it.

### A refused edit reverts and says why — story 6.1, AD-8

Write a flow-style service into the copy and save:

```yaml
services:
  flow: {image: nginx}
```

Select `flow` and click any key in `available, not set`. The banner names what
could not be done — a block key cannot be added inside a flow mapping without
producing YAML that will not parse — and:

- there is **no** `Retry` button on it; retrying respawns the core, and a flow
  mapping is not a dead process;
- the key is **not** marked staged, the strip does not appear, and `git diff` is
  empty;
- the wording contains no apology and no exclamation mark.

### The Dockerfile as stages — story 6.2

Open `/tmp/webstack/docs/Dockerfile` in the editor. The panel switches to the
stage form: **two** groups, `build` and `runtime`, instructions in file order,
no canvas anywhere.

- Each group heads with the file's own name for the stage — the `AS` name, or
  the image where there is none. Never "stage 0".
- The `FROM` value is editable. The instructions below it are editable where
  they are one line.
- Change stage 1's `FROM` to `nginx:1.28-alpine` and press `Enter`. The strip
  shows a **one-line** change, `Save to Dockerfile` writes it, and `git diff`
  shows one line: the `--platform` flag, the `AS` clause, the keyword's casing
  and any trailing comment are **all** still there.
- Lower-case `from` stays lower-case. A normalised keyword is a failed check.

Now the refusal. `RUN npm ci` in this file is one line, so add a continuation to
it in the text editor:

```dockerfile
RUN npm ci \
    --omit=dev
```

Save, and look at the form: that instruction is shown **greyed and not
editable**, with a sentence saying rewriting it would need a line-break policy.
It is not hidden — the reader has to know the line is there — and it is not
offered and then refused at save time.

### Quirks survive — story 6.2

Take a copy of the Dockerfile and give it CRLF endings, a leading BOM and a
`# escape=\`` directive. The form's header states each of those out loud.
Change the base image and save, then check with `file` and `xxd` that the
endings, the mark and the directive are all still there, and that `git diff`
shows one line.

`make gate` holds `dockerbench` at 100% on all five metrics; that number is what
this check confirms is still true through the UI rather than only in the engine.

### Reaching the Dockerfile from the service — story 6.3

On `examples/webstack`, click the orange `docs/Dockerfile` node on the canvas.

- The stage form opens, for **that** file, resolved through the build context
  (`context: ./docs`, `dockerfile: Dockerfile`).
- Arrow-key across the canvas through the Dockerfile node instead of clicking
  it: the selection moves and **no** form opens. Holding an arrow key must not
  open a form per keystroke. Press `Enter` on it and the form opens.
- Dragging the node repositions it and does **not** open the form.
- Press `Back to the stack`. The graph returns with `docs/Dockerfile` still
  selected and every node where you left it.

### A build naming a Dockerfile that is not there — story 6.3

In the copy, change `services.docs.build.dockerfile` to `Dockerfile.missing` and
save.

- The node is **still on the canvas**, carrying `missing — this file is not on
  disk` under its label. It has not vanished.
- `View → Problems` holds a warning from `Composure` with the rule id
  `build-dockerfile-missing`, naming the resolved path.
- Clicking the node opens a form that says which file is not there, rather than
  an empty form or a failure banner.
- `go run ./cmd/composure diagnose /tmp/webstack/compose.yaml` reports the same
  finding. If the panel and the CLI disagree, the panel is computing something.

### Nothing writes without a press

The last check, and the one to repeat after any change to the panel. With a
compose file open and a stack drawn:

1. Click through every node, expand every group, stage several edits, drag the
   divider, resize the panel, toggle the theme, reload the window.
2. Do not press `Save`.
3. `git status` is **clean**.

There is no autosave and no debounce-to-disk anywhere in this product. The only
byte that ever reaches a file comes from a `Save to <file>` press.

### Themes

With the panel open, switch between Default Light Modern and Default Dark
Modern. Every surface, label, border and the canvas background follow
immediately, without a reload. Nothing keeps a light-theme colour on a dark
background. Check the node fill and the node border specifically — they are the
easiest place for a literal to hide.

### Keyboard

Tab into the graph. Right and left walk **every** node in reading order,
including networks, ports and Dockerfile nodes — hold the key down and confirm
the selection visits all 13 in `webstack` and comes to rest at the last one.
Up and down move between rows to the node nearest in x. `Home` and `End` jump to
the ends, `Escape` clears the selection. The focus ring is visible on the
selected node at every step, and a node selected off-screen pans into view.
Confirm the whole graph is reachable and traversable without touching the mouse.

### Contrast — story 4.5, and the only part of it a machine cannot do

The suite reads the pairings OUT OF the stylesheet — `derivedPairings()` — and
proves that every one it can resolve is a pairing VS Code contributes. It used
to compare two hand-written tables instead, and a real mispairing planted in
`style.css` passed 352 of 352 tests; that is the check `derivedPairings` exists
to replace. It still proves nothing about what those colours look like once a
theme resolves them, and it cannot resolve a pairing whose surface is declared
on an ancestor the selector never names. **These are the checks a human must do by eye**, and they are the
same list `webview/a11y.ts` exports as `MANUAL_CONTRAST_CHECKS`, so the two
cannot drift — a test asserts each line below appears here verbatim:

- Open the panel in Default Light Modern, Default Dark Modern, Light High Contrast and Dark High Contrast.
- The three diff line colours sit on a TRANSLUCENT diff background; the check resolves that to the editor background. Confirm added and removed lines are still readable in each theme, and in a theme with a heavy diff tint.
- The focus ring (--vscode-focusBorder) is a non-text indicator and needs 3:1 against whatever it surrounds. Tab to a node, a provenance link, an unset key, the divider, the search field, the focus and collapse buttons, an inspector field and the Save and Discard buttons, and confirm the ring is visible on each in all four themes.
- The selection ring and the focus ring are the same token. Confirm a selected-but-unfocused node is still distinguishable from a focused one.
- Node kinds are told apart by shape and dash pattern, not colour. Confirm that holds in Dark High Contrast, where dashes can vanish into the border colour.
- The dimmed state used by search and focus mode is an opacity, not a colour. Confirm dimmed nodes are still legible enough to read, and that matched nodes are obviously the ones in focus.
- The narrow (<600px) layout replaces the canvas with a list. Confirm the search field, its status line and the collapse controls are all still reachable and legible there.
- The value chip and the node box are the raised widget surface, separated from the pane by fill in the default themes and by --vscode-editorWidget-border in the high-contrast ones. RAISED_SURFACES checks that against VS Code's own defaults, which is four themes out of every theme there is. In a USER-authored theme that sets editorWidget.background to the editor background and contributes no widget border, the chip and the node box go flat again — confirm by eye in whatever theme you actually use.
- Most of the stylesheet pairs ink with a surface declared on an ANCESTOR the selector never names, which no CSS reader can resolve. derivedPairings() checks the ones it can and underivedInk() names the rest; those tokens rely on the INK_ON ledger, so read the ledger against the rendered pane once per release rather than trusting it.

Two further things the pairing check does not cover, by construction:

- **Fallbacks.** `var(--a, var(--b))` is checked on `--a` only, because `--b` is
  what a theme that omits `--a` falls back to and forbidding it would forbid
  writing a fallback at all. Confirm the banner still reads in a theme that
  leaves `inputValidation.errorForeground` unset.
- **Opacity.** `.edge`, `.is-dimmed` and `.canvas.is-stale` all reduce alpha,
  and alpha reduces contrast. None of them carries text a decision depends on;
  confirm that stays true.

Severity is stated in words everywhere it is stated in colour: the banner, the
inline diagnostics, the node badge's accessible name, and the `unresolved`,
`dependency cycle` and `missing` markers on a node.

### Direction A — what still diverges

Checked against the Direction 3 mockup.

Fixed 2026-08-12: semantic groups with per-group unset lists; values on an
input surface (`input-background`, 1px border, 3px radius, 6px padding); unset
fields dashed with no fill; the diagnostic pill inline in the field row; the
pending strip docked to the inspector with a `no pending changes` empty state;
a stack-header summary (`4 services · 1 network · 2 warnings`) and a severity
count in the inspector header.

Still divergent, deliberately:

- **No base-image age on the Dockerfile form.** The mockup's header reads
  `base image 14 months old`. That is registry metadata; the core does not
  expose it and the extension host makes no network calls. It needs a core
  method fronting what `cmd/hubsearch` already knows.
- **No `available, not set` on the Dockerfile form.** See the third core defect
  above — there is no instruction vocabulary to render.
- **The stage form shows `7 instructions · 3 add a layer`, not `6 layers`.**
  The number of layers in a built image depends on the builder, and we did not
  build it. The two numbers we do have are counted from what the core reported.

### Search, focus and collapse — story 4.4

Open `examples/large/compose.yaml` and let it settle.

**Search.** Type `svc-12` in the search field. Matching nodes come forward and
the rest dim. **No box moves** — this is the criterion the whole feature turns
on, so pick a node near an edge of the canvas, note where it is, and confirm it
is still exactly there while you type. The status line under the toolbar says
how many of how many matched. Clear the field with `Escape`; every node comes
back to full strength.

Now type `zzzzz`. Nothing matches, **nothing dims**, the graph is left exactly as
it was, and the line reads `Nothing matches “zzzzz”. The graph is unchanged.`

**Focus.** Select a service and press `Focus`. The core is asked for its blast
radius (`stack/impact`); when it answers, that node, everything that depends on
it and everything it depends on stay lit and the rest dim. The status line says
so in words, both directions. Press `Focus` again to clear it. With nothing
selected, pressing it says so rather than doing nothing.

**Collapse.** Press `By network`. Each network with two or more services on it
folds into a single node naming the network and its count, with the services'
ports and Dockerfile nodes folded in with them. Drag two nodes somewhere
memorable first, then collapse and press `By network` again — every node must
return to **exactly** where you left it, including the two you dragged. Pressing
`Enter` on a folded group expands it too.

**Keyboard.** Do all of the above with `Tab`, `Enter` and `Space` only. The
search field, the `Focus` button and both collapse buttons are tab stops in
order; the collapse and focus buttons announce their pressed state; the status
line is a live region and is read out when it changes.

**The file.** After all of it, `git diff` on the compose file is empty. Search
state, collapse state and positions are view state and never enter a file.

### Narrow panel

Between 900px and 600px the inspector's two-column field grid collapses to one
and the provenance lines lose their indent; nothing overflows sideways.

Drag the panel below 600px. The graph collapses to a header strip naming the
selected node and its kind, the divider goes away, and the inspector takes the
panel; the legend goes with the canvas; the page never scrolls horizontally.
The list of nodes holds **every** node kind, one button per node. Select a row,
widen the panel again, and the graph returns with that same node selected and
the same positions — one selection, not two.

### Bottom panel

Drag the panel into VS Code's bottom panel — short and wide. The header, banner
and graph all stay usable and nothing overflows.

### No compose file

Open a folder with no compose file (the **Run Extension (empty folder)** launch
configuration starts one). Nothing activates: no panel, no status bar item, and
`ps aux | grep composure` finds no process.

### No services

Open a valid YAML file named like a compose file whose `services:` key is empty.
The canvas states plainly that no services are declared, naming the file. Not a
blank pane, not an error dialog, and **no `Add service` button**.

### Missing binary

Rename `extension/bin/<goos>-<goarch>/composure` and reload the host. The panel
shows a banner naming the expected path and the detected platform, with a
`Retry` action — not a blank pane and not a silent hang. Restore the binary,
press `Retry`, and the graph draws.

### Crash mid-session

With a graph drawn, `kill` the `composure` process. The last good graph stays
visible but **dimmed**, a banner names the exit code, and `Retry` respawns the
core and redraws. Nothing restarts on its own — a crash loop that respawns
silently is how a broken binary looks healthy.

### Malformed file

Delete a closing bracket in the drawn file and save. The banner names the file,
line and column; the previous graph stays dimmed rather than vanishing. Fix the
file and save — the banner clears and the graph returns undimmed.

### Re-resolve on save

Rename a service in the YAML and save. That node relabels; every other node
stays exactly where it was, including one dragged before the save. The selection
does not follow the text cursor — bidirectional navigation is story 4.3.

Then add `depends_on:` to a service and save: a new edge appears with its
condition, and nothing else moves.

### Position never enters the file, including a Dockerfile node

Drag a service that has a Dockerfile node. The Dockerfile follows it, staying
attached below. Drag the Dockerfile node itself: it stays where you put it, and
dragging the service afterwards no longer moves it. Reopen the workspace — both
positions are restored and `git diff` on the compose file is **empty**.

### Position never enters the file

Drag several nodes. Close the workspace and reopen it. The positions are
restored, and `git diff` on the compose file is **empty**. This is the check
that catches a view accumulating truth the file does not contain.

### Large stack

Open `examples/large/compose.yaml`. The core reports **771 nodes and 767 edges**
for it. First paint inside two seconds, auto-fit still legible, and panning,
zooming and dragging a node all stay responsive.

Everything before the DOM is measured, on an M-series laptop, driving the real
core over the real JSON-RPC socket:

| Step | webstack (13 nodes) | large (771 nodes, 767 edges) |
| --- | --- | --- |
| `stack/topology` round trip | 1ms | 13ms |
| reading the answer | <1ms | 1ms |
| layered layout | <1ms | 3ms |
| edge routing | <1ms | 2ms |

Story 4.4's three features, measured the same way against the real
`stack/topology` answer for `examples/large` (771 nodes, 767 edges), 20 runs
each on an M-series laptop:

| Step | large (771 nodes, 767 edges) |
| --- | --- |
| layered layout, for comparison | 0.72ms |
| edge routing, for comparison | 0.30ms |
| **search — one keystroke over every node** | **0.20ms** |
| search — a query matching nothing | 0.16ms |
| **collapse by network — the fold** | **0.47ms** |
| collapse by network — fold and re-lay-out | 0.84ms |
| expand — re-lay-out at the saved positions | 1.34ms |

Search costs a fifth of a millisecond per keystroke on the biggest stack in the
repository, which is why it runs on `input` and is not debounced: a debounce
would be latency bought for nothing. Collapse folds 771 nodes into 108 drawn
ones across 4 network groups. None of these numbers includes Chromium's paint —
see the note below.

Edges are drawn as **one `<path>` element per edge kind**, each edge a subpath —
3 elements for `examples/large`, not 767. That is the measure that matters for
this file, and it is asserted in the suite. What no test here can measure is
Chromium's paint of the ~4,600 node elements that remain; that is what this
manual check is for. If it stutters, the node elements are the place to look,
not the edges.

`examples/large` declares no `depends_on` at all, so all 500 services share one
layer. A single band that wide is unusable, so a band past 8 members wraps into
sub-rows shaped like the panel — confirm the canvas is roughly 6,500 × 3,700
rather than one hairline row.
