# Screenshot harness

Renders the **shipped** webview bundle outside VS Code, against **real core
answers**, and photographs it. It exists because every verification in this
repository checked that elements exist and behave; none compared appearance, so
`MOCKUP-TRACEABILITY.md` could carry 55 `built` rows while the product looked
wrong.

Nothing in `webview/`, `host/` or `shared/` is modified by anything here. The
harness only *calls* them: `adapter.ts` imports `host/topology.ts`,
`host/inspect.ts` and `host/staging.ts` so the HostMessage stream the page
receives is assembled by the same code the extension host uses.

## Running it

```bash
npm --prefix extension install          # puppeteer-core is a devDependency
node extension/harness/capture.mjs      # real core answers  -> fixtures/core.json
                                        # NOT COMMITTED - run this first
node extension/harness/shoot.mjs        # screenshots -> ../shots-render/
node extension/harness/metrics.mjs      # computed styles and counts
node extension/harness/probe.mjs        # row geometry, chrome heights
node extension/harness/pendingfit.mjs   # ASSERTS the staged change is visible; exits 1 if not
node extension/harness/comboshot.mjs    # ASSERTS every allowed value is visibly on screen; exits 1 if not
node extension/harness/surfaces.mjs     # resolved surface colours and their ratio against the pane
node extension/harness/rhythm.mjs       # every row in the inspector, with its geometry
node extension/harness/rhythm.mjs --check   # ASSERTS one value column and a rhythm; exits 1 if not
node extension/harness/canvas.mjs           # the graph pane's painted geometry and resolved colour
node extension/harness/canvas.mjs --check   # ASSERTS the card reads and auto-arrange works; exits 1 if not
node extension/harness/epic9.mjs            # what a list, a comment and a move measure once opened
node extension/harness/epic9.mjs --check    # ASSERTS all three are reachable and readable; exits 1 if not
node extension/harness/epic9.mjs --shots ../shots-epic9   # six captures: three affordances x two themes
node extension/harness/system.mjs           # the pane's type, shape and control vocabularies
node extension/harness/system.mjs --check   # ASSERTS the pane is ONE design; exits 1 if not
node extension/harness/system.mjs --shots ../shots-freeform  # the add-a-key composer, Dark+ and Light+
node -e "require('esbuild').buildSync({entryPoints:['extension/harness/crossings.ts'],bundle:true,platform:'node',format:'cjs',outfile:'extension/harness/dist/crossings.cjs'})" \
  && node extension/harness/dist/crossings.cjs   # graph crossings, unrouted cubics
node -e "require('esbuild').buildSync({entryPoints:['extension/harness/graphmetrics.ts'],bundle:true,platform:'node',format:'cjs',outfile:'extension/harness/dist/graphmetrics.cjs'})" \
  && node extension/harness/dist/graphmetrics.cjs        # what the canvas draws
```

`fixtures/core.json` is generated, not committed. It is verbatim output of the
shipped binary and bakes in absolute paths to this working copy, so a committed
copy is stale for every clone at a different path — which is exactly what
happened when this repository moved. `capture.mjs` needs a built core in
`extension/bin/`, which is not committed either, so the build is a prerequisite
for the harness regardless and regenerating the fixture costs nothing extra.

`graphmetrics.ts` is the graph pane's instrument: crossings, edges occluded by
a node box that is not an endpoint, edge labels shown and overlapping, and the
layout+routing time — all sampled from the paths `routeEdges` actually emits,
so a number cannot improve without the picture improving. `--direct` measures
the same topologies with the pre-routing geometry (one cubic per edge, every
label pinned to its midpoint), which is what makes a before/after pair one
instrument rather than two. `-v` names the offending edges and label pairs.

Measured before the routing work (one downward barycentre sweep, one cubic per
edge) and after it (sweeps scored by `orderSpan`, edges routed, labels placed):

| Stack | Crossing pairs | Occluded edges | Overlapping labels | layout+route |
| --- | --- | --- | --- | --- |
| `examples/webstack` | 1 → 2 | 6 → **0** | 1 → **0** | 0.1 → 0.5ms |
| `examples/large` | 77,888 → **68,214** | 765 → **1** | 0 → 0 | 1.4 → 11.2ms |

Re-measured after the card lost its dead space (94px → 53/67/69px) and
`routeGeometry` started measuring the near gap in the DIRECTION OF TRAVEL
rather than to the topmost box in the strip — which is what a shorter card
exposed: `services.docs -> networks.shipyard` turned 16px below a service whose
Dockerfile node hangs 14px under it.

| Stack | Crossing pairs | Occluded edges | Overlapping labels | layout+route |
| --- | --- | --- | --- | --- |
| `examples/webstack` | 2 → 2 | 0 → **0** | 0 → 0 | 0.5 → 0.5ms |
| `examples/large` | 68,214 → 68,248 | 1 → **0** | 0 → 0 | 11.9 → 11.0ms |

The 34 extra crossings on `examples/large` (+0.05%) are the trade for a denser
band: shorter cards put more of the graph inside one screen and the corridors
are chosen from a tighter set. The occluded edge it removes is worth more —
that is the number the reader misreads a graph over.

The webstack crossing is the trade and it is deliberate: a route that steps
round a box has to cross the gutter to get there. One crossing on a
seven-service stack is what the reader was never confused by; six edges landing
on the wrong box, and two conditions printed through each other, is what they
were. On `examples/large` the iterated ordering more than pays for the routes'
extra crossings — 77,888 down to 68,214 — and 765 occluded edges become one.

`canvas.mjs` is the graph pane's APPEARANCE instrument, and the answer to the
owner's 2026-08-13 note — *"still the UI looks different than the mockup"* and
*"the layout view does not have auto arrange"*. `graphmetrics.ts` answers
whether the picture is legible and never paints anything, so it cannot see a
card that does not read as a card, a name set at the weight of the line under
it, or 50px of empty box below the last word. Those are resolved colours and
painted glyph boxes, which is a browser's job. Every assertion is a rendered
fact, and the round trip at the end is driven with the browser's own mouse —
synthetic `PointerEvent`s cannot be used, because `setPointerCapture` rejects a
pointer id the browser has never seen and the drag silently never starts.

Measured on the bundle that preceded the design gaps 3–5 work, **8 of its
checks fail**:

| Assertion | On the previous bundle |
| --- | --- |
| no service card carries dead space under its last line | FAIL — worst **50.1px**, every card a flat 94 |
| the service name is heavier than the image line | FAIL — 400 against 400, in both themes |
| a network node reads against the canvas | FAIL in Light+ — **1.34:1**, a 35%-translucent hairline on white |
| a volume node reads against the canvas | FAIL in Light+ — same rule |
| the toolbar carries an auto-arrange control | FAIL — there was none |

After: cards 53 / 67 / 69px with at most 12.4px under the last line, name 600
against 400, capsules 1.70:1 in Light+ and 2.26:1 in Dark+, and a drag on a
service followed by a press on `Auto-arrange` returns the box to
`translate(-94 284)` while posting an **empty** position map — the message
`host/panelbehaviour.test.ts` already pins to the non-writing path.

`capture.mjs` spawns `bin/darwin-arm64/composure serve` and records verbatim
`stack/topology`, `stack/diagnose`, `stack/schema`, `stack/dockerfile` and
`stack/preview` answers for `examples/webstack`, `examples/large` and
`examples/empty`. **Do not hand-write a fixture** — a fixture invented to make a
check pass is the exact mistake that let this gap ship.

A browser is required but not bundled: `shoot.mjs` drives the locally installed
Google Chrome through `puppeteer-core`. Override with `CHROME=/path/to/chrome`.
`SHOTS=/some/dir` sends the captures somewhere other than the gap report's
committed directory, which is what a before/after pair wants.

`comboshot.mjs` is story 7.9's instrument, and the answer to the owner's
2026-08-13 rejection — *"the expected values should be in the list and not as
links below it"*. It opens the value list on `services.web.restart`, a field
that already holds `unless-stopped`, and asserts that all four values the core
reported have a painted box inside the popup's viewport in both default themes,
that the popup starts at the value column, that its surface differs from the
pane's, and that the document contains no `datalist`, no `select` and no value
chip under the field. The `<datalist>` this replaced could not be photographed
at all, and it showed **one** option on that field because a datalist filters
its list against the input's own text — which is the whole defect. Measured on
this commit: 4 of 4 visible, Dark+ and Light+, popup 291×120 at the field's own
x, `rgb(37,37,38)` and `rgb(243,243,243)` against a transparent pane.

`rhythm.mjs` is the inspector's instrument, and the answer to the owner's
2026-08-13 note — *"the forms need some spacing, and the lists look weird like
this"*. It dumps every element in the pane in document order with its box, its
size and its weight; `--check` turns the properties that must not regress into
an exit code, over `service` and `dockerfile` in both default themes:

- **one value column.** Every value in the pane starts on the same x whatever
  depth it is at, and every provenance line, resolution sentence and diagnostic
  starts where its value's box does. On the design gaps 3–5 bundle the service
  pane measured
  **five** value columns — x = 826, 838, 850, 861, 849 — because each level of
  nesting pushed the whole row right, and the Dockerfile pane put `line 6` at
  **x = 383** under values that started at **x = 124**.
- **a rhythm.** A section sits further from the section above it than a row does
  from the row above it, by at least the 4px base × 4, and a section heading is
  not the same weight as a provenance line. On that same bundle: 12px against
  4px, both at weight 400.
- **no cell reads `list` or `map`** — our word for the shape of a value whose
  entries are on screen underneath it.

Ten of its original thirteen checks fail on the design gaps 3–5 bundle, which is
the point: it is a check that can fail.

It now also carries the 2026-08-13 gap set, twenty more assertions over the same
two scenarios and two themes. Each is a rendered fact, never "a CSS rule
exists", and each was run against the previous commit's bundle to confirm it
fails there — **13 of the 20 do**, and the two that need a source edit rather
than a rebuild to falsify were falsified that way:

| Assertion | On the previous bundle |
| --- | --- |
| no provenance line between a heading and its first row | FAIL — `healthcheck` put `webstack/compose.yaml:36` above `test` |
| no file position is linked twice under one row | FAIL when the de-duplication is removed — `SESSION_SECRET` → `compose.yaml:34`, twice |
| a finding costs at most 4 elements and 90px | FAIL — `SESSION_SECRET` measured **6 elements, 117px** |
| an available key is quieter than a value | FAIL — key 12px against value 12px |
| the header says what is in the file | FAIL — no summary element at all |
| the header control is on the header line, at its right | FAIL — `+ add stage` on its own row, at x=10 |
| the port node box is not the canvas colour | FAIL in Light+ — **1.000:1** |
| the port box takes the same surface as every node box | FAIL in both themes |

Base-image age is deliberately absent from the header assertion and from the
header: it needs the registry round-trip deferred to phase 6 (`cmd/hubsearch`),
and a header reading `base image 14 months old` because the mockup does would be
a number nothing measured.

Three of these are CHECKS rather than instruments: `pendingfit.mjs` exits non-zero
when the staged `−`/`+` pair is not inside the visible rectangle of the box that
shows it, at three panel sizes, and `surfaces.mjs` prints the composited colour
of each raised surface and its WCAG ratio against the pane behind it — the
number that was **1.000:1** for the Light+ value chip. Neither can run under
`node --test`: one needs layout and the other needs a theme resolved by a real
engine, and the absence of both is how a chip that was not there passed 615
tests.

`epic9.mjs` is the Epic 9 sibling of `rhythm.mjs`, and it exists because
`rhythm.mjs` cannot see any of what Epic 9 added. Every control in that epic is
behind a PRESS — a collapsed list opens, a comment block opens, a move opens —
so none of them is on screen when `rhythm.mjs` measures. This script drives the
gesture first and then measures, over `service` in both default themes:

- **the owner's own row is a way in.** `CMD · wget · -qO- ·
  http://localhost:3000/healthz` is a run of BUTTONS, each announcing its index,
  and pressing the middle one opens the list with the caret in *that* entry's
  field. The defect it guards is the one reported in four words — *"i am unable
  to edit cmd"*: the row on screen and nothing to do with it.
- **the geometry survives the gesture.** An expanded list, a comment block and a
  move block each keep the pane's ONE value column. Each of the three is a
  nesting level `rhythm.mjs` never sees, and the comment block failed this the
  first time it was measured — `x = 806, 816`, the ten pixels of its own rule and
  padding — which is what the check is for.
- **both positions, in the shapes the engine accepts.** A `<textarea>` for the
  run above, because a run of comment lines is ONE comment; a single-line
  `<input>` for the trailing one, because the engine refuses a line break there.
- **both diffs.** A move shows the compose diff and the `.env` diff, both inside
  the pane's rectangle, and the pending strip carries the `.env` half too with
  the write control reading `Save to compose.yaml and .env`. A two-file
  operation showing one diff is the lie DECISIONS.md 25 exists to prevent, and a
  second diff scrolled out of view is that lie with extra steps.
- **every control these gestures produce has an accessible name** — story 4.5's
  floor, applied to controls that do not exist until something is pressed and
  are therefore invisible to a scan of the initial render.

Each of its assertions was falsified before being trusted: dropping `msg.env` at
the routing point in `main.ts` fails the strip check; appending the row controls
as three loose grid children fails the value-column checks (and did — that is
how the `.field-actions` container came to exist); appending `+ entry` before
the list's provenance line put `compose.yaml:36` under it, claiming to be its.

`theme.js` supplies the `--vscode-*` custom properties for Dark+ and Light+,
taken from VS Code's own colour-registry defaults (MIT).

## Constraints

- `dependencies` in `package.json` must stay `{}` — `host/packaging.test.ts`
  asserts it and `make licence` cannot see `node_modules`. **`npm install
  --save-dev` deletes the empty object**; restore it by hand afterwards.
- `harness/` is outside `tsconfig.json`'s `include`, so it is not type-checked
  by `npm run compile`. Keep it that way: it is a measuring instrument, not
  shipped code.

## `system.mjs` — is it ONE design?

The answer to the owner's third rejection, and the one none of the instruments
above can give. `rhythm.mjs` measures the inspector's geometry, `canvas.mjs` the
graph's appearance, `epic9.mjs` what three gestures produce — and all three were
**green** on a bundle the owner said still did not look like the design.

They are green because each measures the part it was written for. What none of
them can see is whether the parts belong to each other. Since the mockup was
agreed the pane gained eight things it has no counterpart for — the
allowed-value combobox and its popup, the Docker Hub upgrade pill, the image
search, the comment affordance, the move-to-variable block with two diffs,
list-entry expansion, the availability note and auto-arrange — each built by a
different pass, each internally consistent, and together three visual dialects.

Measured on the bundle before this one, over the compose pane, the Dockerfile
pane and the stack pane, in Dark+ and Light+, with the three Epic 9 affordances
open:

| Measured | Before | After |
| --- | --- | --- |
| distinct font sizes painted | **10** — 8.64, 9, 9.6, 10.2, 11.05, 11.52, 11.96, 12, 12.48, 13 | **5** — 9, 10.2, 11.52, 12, 13, all named steps |
| multipliers behind them | 7 (`0.72 0.75 0.78 0.8 0.85 0.9 0.92`) + one `9px` literal | 2, in one `:root` block |
| shapes among the four row-action controls | **4** — 17px / 16px / 13px / 13px tall, two faces, two radii | **1** — 16px, one box, face only varies |
| x positions the row-action glyphs landed on | **8** — 724, 761, 762, 921, 1042, 1063, 1072, 1076 | **1** gutter; every row ends at 1090 |
| distinct row right edges | 1078 and 1090, and 1096 on every editable Dockerfile row | **1090**, all 94 rows |
| the two "quiet" chrome controls | 22px vs 17px, `2px 6px` vs `1px 8px`, 13px vs 11.52px | identical, asserted as a rendered shape |
| corners in the chrome | 3px, plus a 2px search field and a 2px `Stage this move` | **3px**, everywhere |
| Dockerfile instruction keywords | 13px **UI face** — file bytes in the prose face | 12px code face, `comment`/`directive` still prose |
| rules between the pane header and the canvas | **3** | **1** |

The checks it runs, each falsified before it was trusted (the recipe is in the
comment above each one):

- **the type scale is closed.** Every size painted is one of the six declared
  steps. Not *how many* sizes — a count is met by picking any five — but
  *which*, so a rule that invents `11px` fails whatever else is on the pane.
- **a control that acts on a row has one shape**, and is declared one. The face
  is allowed to differ, because `#` is a mark the file uses and `Remove` is our
  word; nothing else is.
- **the row's right edge is a gutter.** The counterpart of `rhythm.mjs`'s one
  value column, for the other side of the row: every row ends on the same x, and
  no row puts its controls left of its own value — which a parent row did,
  putting `#` hard against the word `environment`, 350px left of every other one.
- **one chrome corner**, and a graph node vocabulary that is CLOSED at the three
  shapes that mean something: the 4px service card, the 12px resource capsule and
  the square published port.
- **the chrome above the canvas has one edge, and stays one toolbar row.** Four
  pixels of button padding wrapped that row and put 28px back on the graph while
  every other check stayed green — which is why the number is pinned and not
  just the rule count.
- **monospace means it is in the file** (DESIGN.md's one load-bearing type rule),
  asserted on the pane that broke it.

Run it with the other four on any commit that touches `extension/webview/`.
