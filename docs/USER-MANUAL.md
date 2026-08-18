# Composure — user manual

Composure reads the Docker Compose files and Dockerfiles you already have and
shows you three things about them: the stack they describe, drawn as a graph
and laid out as a form; where every value in that stack actually came from —
which file, which line, which merge step won; and what you have *not* declared
but could. It also writes back. An edit locates the bytes of the thing you
changed and patches them in place, so the comment above the line you edited,
the quoting style, the blank lines, the key order, the CRLF endings and the
byte-order mark are all still there afterwards. Nothing is ever parsed into a
model and re-serialised. Changing one image tag produces a two-line diff, not a
rewritten file.

It ships as a VS Code extension with a Go core, and the core is also a
standalone CLI. Every capability in the pane exists as a subcommand first.

> The file is the source of truth. Everything else is a faithful view of it.

**Contents**

1. [Install](#1-install)
2. [The first five minutes](#2-the-first-five-minutes)
3. [The stack view](#3-the-stack-view)
4. [The inspector](#4-the-inspector)
5. [Editing](#5-editing)
6. [Why an edit is refused](#6-why-an-edit-is-refused)
7. [Adding something that is not there yet](#7-adding-something-that-is-not-there-yet)
8. [Comments](#8-comments)
9. [Moving a value into a variable](#9-moving-a-value-into-a-variable)
10. [Docker Hub](#10-docker-hub)
11. [The CLI](#11-the-cli)
12. [Troubleshooting](#12-troubleshooting)
13. [What Composure will not do](#13-what-composure-will-not-do)
14. [Not built yet](#14-not-built-yet)

---

## 1. Install

### The extension

Composure is packaged as **one VSIX per platform**, and each one carries a single
statically linked Go core binary — the process the extension spawns and talks
to over JSON-RPC on stdio, the way VS Code talks to `gopls` or
`rust-analyzer`. There is no all-platforms package, because it would ship every
install four binaries it can never execute.

The targets are `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`,
`alpine-x64`, `alpine-arm64` and `win32-x64`. The `alpine-*` builds carry the
same binaries as their `linux-*` counterparts: the core is built with
`CGO_ENABLED=0`, so one static build serves glibc and musl alike.

Install the VSIX for your platform:

```bash
code --install-extension composure-darwin-arm64.vsix
```

To build them yourself from a clone of the repository:

```bash
make package
ls extension/build/          # one .vsix per target
```

`make package` cross-compiles every core and runs `vsce package --target` once
per platform. It needs Go 1.24+ and Node 20+. Running the extension needs
neither — only VS Code 1.85 or later, or a fork on the same extension API.
Cursor and Windsurf work; JetBrains and vim do not.

No Docker daemon is required to read, resolve or edit anything.

### Opening it

The extension activates when the workspace contains a file matching
`compose.{yml,yaml}`, `compose.*.{yml,yaml}`, `docker-compose.{yml,yaml}` or
`docker-compose.*.{yml,yaml}`. Open one of those files and the panel opens
beside it. If it does not, run **Composure: Show Stack** from the command palette.

Opening a Dockerfile — or clicking a Dockerfile node on the canvas — gives you
the Dockerfile view instead. Those are the only two surfaces. Settings live in
VS Code settings, diagnostics live in the problems panel, and the file lives in
the editor.

### Settings

| Setting | Default | What it does |
| --- | --- | --- |
| `composure.corePath` | `""` | Absolute path to a core binary. For development. When empty, the extension uses the binary shipped for your platform under `bin/<goos>-<goarch>/`. Machine-scoped on purpose — it names an executable to run, and a `.vscode/settings.json` committed to a repository must not be able to redirect it. |
| `composure.dockerHub` | `"on"` | Whether Composure may ask Docker Hub how old an image is and what newer tags exist. This is the only part of Composure that leaves your machine. Set it to `off` and the pane is exactly what it is with no network: no upgrade pills, no image search, no requests made. |

### The CLI

The same binary is the CLI. From a clone:

```bash
go build -o bin/composure ./cmd/composure
./bin/composure resolve examples/webstack
```

The examples used throughout this manual live in `examples/webstack/`.

---

## 2. The first five minutes

Everything below uses `examples/webstack/compose.yaml`, which ships with the
repository. It is a deliberately ordinary file: nine services — one of them
behind a `migration` profile, so eight are drawn by default — one network, three
volumes, an anchor merged into most services, a variable nothing defines, and a
password in plain text.

```yaml
x-service-defaults: &defaults
  restart: unless-stopped
  networks:
    - shipyard

services:
  # Public edge. The only service with a published port.
  gateway:
    <<: *defaults
    image: nginx:1.27-alpine
    ports:
      - "8080:80"
    depends_on:
      web:
        condition: service_healthy
      docs:
        condition: service_started
    ...
  web:
    <<: *defaults
    image: ghcr.io/shipyard/web:2.4.1
    environment:
      NODE_ENV: production
      API_URL: http://api:3000
      SESSION_SECRET: ${SESSION_SECRET}
    ...
  db:
    <<: *defaults
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: hunter2
```

Open it and the pane shows:

- a graph with `gateway` at the top, `web` under it, `api` under that, and
  `db`, `cache`, `docs`, `worker` on the bottom row — dependency order reads
  top to bottom;
- the `shipyard` network, the `pgdata`, `cachedata` and `scratch` volumes and
  the published port `8080:80` as nodes of their own;
- a dashed orange `./docs/Dockerfile` node hanging off `docs`, because that
  service builds rather than pulls;
- a badge on every node with findings against it, carrying the count.

Click `web` and the inspector fills with every value the service actually
resolves to, each with the file and line that set it underneath. `restart`
reads `unless-stopped` and its provenance line says
`compose.yaml:9 · from *defaults at compose.yaml:29` — the value is written on
the anchor on line 9 and reaches `web` through the merge key on line 29.
`SESSION_SECRET` renders as the literal `${SESSION_SECRET}` in warning colour
with the reason under it: nothing defines it, so it interpolates to an empty
string.

> **[Screenshot: the stack view with `web` selected.]** It belongs here, showing
> the graph on the left and the inspector on the right with the provenance and
> the `undefined-variable` finding visible. The extension's own render harness
> (`extension/harness/`) produces exactly this capture against the shipped
> bundle and the shipped core, so it can be reproduced rather than taken on
> trust.

The same three answers from the CLI:

```console
$ composure explain services.web.restart examples/webstack

EXPLAIN — services.web.restart
==============================================================================
entry:  examples/webstack/compose.yaml
kind:   scalar
value:  unless-stopped
set by: examples/webstack/compose.yaml:9:12  [step 0]
        reached through *defaults at examples/webstack/compose.yaml:29:9

OVERRODE (0)
  nothing — this value was set once and never replaced
```

---

## 3. The stack view

### What is drawn

Nodes: **services**, **networks**, **volumes**, **configs**, **secrets**,
**published ports**, and the **Dockerfile** that builds each image. A published
port is a node rather than text on a card because a port collision is a
relationship between two things, and you cannot draw a relationship to a label.

Edges, with a legend across the top of the canvas. **The legend names only the
kinds actually present in this stack**, so it is never a fixed list of things
you cannot see:

| Edge | What it means |
| --- | --- |
| `depends_on` | A start-order dependency. The condition is written on the edge — `service_started`, `service_healthy`, `service_completed_successfully`. Both the short list form and the long mapping form produce this edge. |
| `network` | The service is attached to that network. |
| `network_mode` | The service shares another service's network namespace. |
| `link` | A legacy `links:` entry. |
| `volume` | The service mounts that named volume. The mount string and its mode are on the edge. |
| `config` / `secret` | The service consumes that config or secret. |
| `publish` | The service publishes that host port. |
| `build` | The service builds from a Dockerfile rather than pulling an image. |
| `bind` | A bind mount. |

A **bind mount** is the one that behaves differently. The host path is not part
of the project, so there is no node on the other end: the edge is recorded
against the service itself, as a marker rather than a line drawn to nowhere. It
appears in `composure topology` output — `bind services.gateway -> services.gateway`
— and is not drawn in the legend, because a self-edge is not a relationship
between two things on the canvas.

### The controls

A toolbar across the top of the graph pane. Every control is a real focusable
element, reachable with Tab, and says what it does.

- **Search.** Type and matching nodes stay lit while the rest dim. `Escape`
  clears the field. A keystroke filters; it never re-runs the layout, so it does
  not lag on a large stack.
- **Focus.** A toggle. With a node selected, it dims everything outside that
  node's blast radius — what it depends on and what depends on it, over
  `depends_on` edges only. A shared network is a connection, not a dependency,
  so it does not widen the radius. The same answer is `composure impact`.
- **Auto-arrange.** Re-runs the layered layout and discards positions you
  dragged. Node positions are per-workspace view state and never enter a file —
  drag a node, then run `git diff` on the compose file, and it is empty.
- **Collapse · By network / By profile.** Folds the services into one box per
  network, or per profile. Press the same button again to unfold.
- **Profiles.** One toggle per profile the project declares, and the group only
  appears when the project declares one. `examples/webstack` declares
  `migration`, so there is one toggle for it. This is a real filter, not a
  visual one: the chosen set is what the graph, the diagnostics and the blast
  radius are all computed under. With nothing toggled, only always-active
  services are included, which is what Compose does. Top-level networks,
  volumes, configs and secrets are never filtered, so a filtered stack can show
  a resource with nothing attached to it. If you select a service and then
  filter it out, the pane says so rather than showing the stack under its name.
- **`+ add`.** Opens the composer for declaring something the file does not
  have yet — see [§7](#7-adding-something-that-is-not-there-yet).

### Navigation

Selection is single: one node, one inspector. Clicking a node selects it and
fills the inspector; double-clicking focuses it. Moving the cursor in the YAML
moves the selection in the graph, and clicking a provenance line moves the
cursor in the editor while the inspector stays where it is.

The graph is a listbox for keyboard purposes: arrow keys move the selection
along edges, `Enter` focuses, `Escape` clears.

---

## 4. The inspector

The inspector shows the **resolved** value of everything the selected thing
declares, grouped, with nothing hidden behind a disclosure. A key is never
shown without its value or an explicit `not set`.

Its header names what you are looking at and counts it — for `web` in the
example stack, `6 set · 87 available`, plus the finding count, plus the
specification revision and the `docker compose` version found on this machine.

### Provenance

Under every value is the file and line that set it. Three shapes:

```
compose.yaml:30                                     set once, here
compose.yaml:9 · from *defaults at compose.yaml:29  reached through a merge key
compose.override.yaml:7 · overrides compose.yaml:12 replaced something
```

Clicking any of them puts the editor cursor on that line and selects the range.
The inspector does not move.

This is the question the product exists to answer, and it is not recoverable
from `docker compose config`, which returns a flattened document with the
provenance thrown away. That is why Composure does not use one.

### `available, not set`

At the foot of each group is a line naming every key the Compose specification
permits there that this file does not use. The lead is **`You can also set`**
for keys the specification names, and **`Also permitted here:`** for a
free-form mapping where any key is legal.

The list is generated at runtime from the vendored specification
(`schema/compose-spec.json`). No list of Compose keys is written down anywhere
in this codebase, so it cannot go stale against the specification. Keys that
need a newer `docker compose` than the one on your machine are **marked and
still listed** — you learn the key exists and that upgrading would give it to
you.

Clicking a key **opens a field for it and puts the cursor in it. Nothing is
staged until you press `Enter`.** The click is the question, not the answer.
Where the specification carries a real default the field is seeded with it;
where the "default" is prose lifted from the key's description, that prose
appears as placeholder text and is never seeded — writing `interval value` into
someone's healthcheck is exactly the failure this rule exists to prevent.

Clicking a key that is itself a mapping — `healthcheck`, say — opens *its* keys:
`test`, `interval`, `timeout`, `retries`, `start_period`, `start_interval`,
`disable`, each unset, each carrying what the specification says about it.

### Allowed values

Where the specification enumerates what a key accepts, the field is a picker
rather than a free text box, and a sentence beneath it says how much that list
can be trusted:

| Sentence | Means |
| --- | --- |
| `One of these — the specification allows nothing else.` | A closed set. `cgroup` is `host` or `private`. |
| `All the specification names for one of this key's forms; it accepts other forms too.` | Enumerated on one branch of a `oneOf`. `gpus` accepts `all`, and also a list of device objects. |
| `Named by the specification's pattern, which permits other forms too.` | Derived from a regex. |
| `Listed in the specification's prose, not enforced by it.` | The values are in the description text. `restart` is `no`, `always`, `on-failure`, `unless-stopped` — the specification says so in prose and does not enforce it. |

You can always type a value the picker does not offer. The sentence exists so
you know whether doing that is a typo or a legitimate form.

### Diagnostics

Findings appear **on the field that caused them**, not in a separate list, and
also in VS Code's problems panel — which is already accessible, already
searchable, and already the place you look. Ten rules run:

- host port collisions across the **merged** configuration;
- a `service_healthy` dependency on a service that declares no healthcheck —
  it will never start;
- circular `depends_on`;
- volumes and networks declared and mounted or joined by nothing;
- variables referenced and defined nowhere;
- services nothing can reach — no published port, no shared network;
- credentials sitting in a plain `environment:` block, matched on the key name
  and on URIs carrying a password in their userinfo;
- a `build:` naming a Dockerfile that is not there;
- a key that the merge combined from two different shapes — a list in one file
  and a mapping in another;
- an obsolete top-level `version:` field, which Compose ignores. A hint, not a
  warning.

A finding says what breaks and then what to do, and where the fix is a single
edit it names the exact operation and byte range. Severity is carried in text
as well as colour.

---

## 5. Editing

### Staging

Click a value; it is editable immediately. There is no edit mode and no pencil
icon. `Enter` or blur **stages** the change; `Escape` reverts it. Staged fields
carry a left accent border.

Nothing is written without an explicit press. There is no autosave and no
debounce-to-disk — the engine's whole claim is that it does not touch files it
was not told to touch, and the editor's own dirty indicator does not appear
until Composure writes.

### The pending diff

Staged changes accumulate in the `Pending` strip at the foot of the pane, which
shows the unified diff of exactly what would be written. Two controls:
**`Save to <file>`** — naming the file, because in a multi-file project that is
the question you actually have — and **`Discard`**.

For one image tag the strip reads:

```diff
@@ -70,7 +70,7 @@

   db:
     <<: *defaults
-    image: postgres:16-alpine
+    image: postgres:17-alpine
     environment:
```

Two lines. That is the product. The anchor is untouched, the comments around it
are untouched, and the diff is small enough to put in a pull request that a
reviewer approves in ten seconds.

> **[Screenshot: the pending strip with one scalar edit staged.]** It belongs
> here, and should show the `−`/`+` pair and the `Save to compose.yaml` button
> in the same frame.

### Save

`Save to <file>` writes. After that, undo belongs to the editor: `⌘Z` in the
text editor is the undo path, and Composure keeps no parallel history.

The only modal in the product is the confirmation when a write touches more
than one file.

### If the file changes underneath you

Every staged edit records where its target was when you staged it. If the file
changes on disk and a staged range has moved, the stage is discarded and the
pane says so. It is never rebased onto the new position — writing a stale byte
range is precisely how a fidelity engine damages a file.

---

## 6. Why an edit is refused

This is the section you will actually need. Composure refuses rather than guesses,
and it refuses with a reason. An editor that emits an unparseable file is worse
than one that says no, because the damage surfaces later in someone else's
terminal.

A refusal is not a crash and does not read like one: nothing is written, the
field reverts, and the message names what could not be done and why. From the
CLI a refusal exits **3** and prints `composure: nothing was written.` A genuine
failure — a file that will not parse, a path that does not exist — exits 1.

### Flow-style collections

```yaml
services: {web: {image: nginx}, api: {image: node}}
```

```console
$ composure preview -op insert_key -at services.web -key restart -value always flow.yml
composure: edit: operation 0: target is a flow-style collection; block insertion would produce invalid YAML
composure: nothing was written.
```

You cannot insert a block-style child into a flow-style collection; the result
would not parse. **Replacing** a scalar inside a flow collection is fine, and so
is replacing one entry of a flow sequence — `services.web.healthcheck.test[1]`
in the example stack is inside `["CMD", "wget", …]` and edits normally. It is
insertion, and comments, that are refused.

### Block scalars

```yaml
command: |
  echo hi
  echo there
```

```console
composure: edit: operation 0: that value is a block scalar; replacing it in place would
leave its body behind: This is a block scalar — its value is the indented lines below
the `|` or `>`. Composure does not rewrite block scalars yet: it only ever replaces bytes
in place, and doing that here would leave the body behind. Edit it in the file.
```

Note the wording: **not built yet**, rather than impossible. Rewriting a block
scalar needs a line-break and indentation policy, which is a formatting opinion
this engine does not have. Edit it in the text editor.

### Anchors and aliases

Two different refusals, and the difference matters.

```yaml
    entrypoint: &ep /bin/sh     # web
    entrypoint: *ep             # api
```

Editing the **anchor**:

```console
composure: edit: operation 0: that value defines an anchor; replacing it would remove the
anchor other values reference: This value carries the anchor `&ep`, and other places in
the file reference it as `*ep`. This engine replaces the value's bytes, which would take
the anchor with it and leave those references pointing at nothing, so it refuses.
```

Editing the **alias**:

```console
composure: edit: operation 0: that value is an alias reference; splicing over it would
rewrite the reference, not the value: This line reads `*ep` — a reference to the anchor
on line 7, not a value. Replacing the reference in place would change what the file
points at rather than what it says, so Composure refuses. Edit the anchor, or replace the
reference by hand in the file.
```

Anchors and merge keys are expanded for display and left exactly as written in
the file. That is the point.

### An inherited value, and a merged mapping

This one is not a dead end — it is a redirection, and the pane handles it for
you.

In the example stack `web` does not set `restart`. The value arrives through
`<<: *defaults` on line 29, and the anchor that carries it is shared by six
other services. Writing to line 9 would change all seven.

```console
$ composure editable -at services.web.restart examples/webstack/compose.yaml

services.web.restart — examples/webstack/compose.yaml
==============================================================================
  not editable in place (inherited)
  an edit here would be staged as insert_key
  reached through line 29
  its bytes are at line 9

  web does not set restart here — it arrives through `<<: *defaults` on line 29.
  Writing a value adds restart to web, which overrides *defaults for this one
  place; the anchor on line 9 and everything else that merges it are untouched.
```

**In the pane this is transparent.** The inspector asks the core where the value
is *written* before it stages anything, and stages an `insert_key` on the
service rather than a `replace_scalar` on the anchor. You type a value, and the
diff shows one line added to `web`.

**From the CLI you have to choose the right operation.** A `replace_scalar`
against an inherited path is refused:

```console
$ composure preview -op replace_scalar -at services.web.restart -value always examples/webstack
composure: edit: operation 0: that value is not written here; it arrives through a merge key: …
composure: nothing was written.
```

Use `-op insert_key -at services.web -key restart -value always` instead. Ask
`composure editable` first if you are scripting it; that is what it is for.

The same rule covers a value inherited across files through `extends:` and a
value that arrives through a multi-file merge: Composure writes it where you are
looking at it, never on the thing it was inherited from.

### A stale range

```console
$ composure preview -op replace_scalar -at services.db.image -value x \
    -expect-start 1990 -expect-end 2013 examples/webstack/compose.yaml
composure: the file changed since this edit was staged; the staged range no longer matches:
services.db.image is now at bytes 1995-2013, was staged at 1990-2013
composure: nothing was written.
```

The pane does this for you on every staged edit. From a script, pass
`-expect-start` / `-expect-end`, or **`-expect-text` on its own** — a text
assertion with no byte range is a guard in its own right, on `preview`, on
`apply` and on `extract` alike, and it is often the one you want: it survives an
edit above the target that shifts every offset below it, which is exactly what a
colleague's commit does.

```console
$ composure preview -op replace_scalar -at services.db.image -value x \
    -expect-text postgres:15-alpine examples/webstack/compose.yaml
composure: the file changed since this edit was staged; the staged range no longer matches:
services.db.image now reads "postgres:16-alpine", was staged as "postgres:15-alpine"
composure: nothing was written.
```

(Earlier releases ignored `-expect-text` unless a byte range came with it. That
was a staleness check that silently did nothing, which is worse than an absent
flag, and it is fixed on every command that takes the flag. On `extract` — the
one command that writes two files — a refusal names the compose file and states
that the `.env` beside it was not touched either.)

### A list index that does not exist

```console
$ composure preview -op replace_scalar -at 'services.web.healthcheck.test[9]' -value curl .
composure: edit: operation 0: that list has no entry at that position: entry 9 was asked
for and the list has 4 entries (0 through 3)
```

An index moves when the list moves. The answer to that is the staleness
assertion above, never a rebase.

### A comment where a comment cannot be placed

`set_comment` and `delete_comment` refuse on a block scalar, on an anchored or
aliased value, and inside a flow collection — the same three cases, for the same
reason. Deleting a comment that is not there is also refused, rather than
reported as a delete that removed nothing:

```console
composure: edit: operation 0: there is no comment at that position; refusing rather than
reporting a delete that removed nothing: trailing of services.db.image
```

### A name that is already declared

```console
$ composure add -kind service -name db -value redis:7 examples/webstack/compose.yaml
composure: that name is already declared: services.db is already declared at
examples/webstack/compose.yaml:71
```

A duplicate key is not an error in YAML; it is silently discarded by the
parser. Refusing is the only honest answer.

### A value YAML would read back as something else

```console
$ composure add -kind service -name t1 -value 3.10 examples/webstack/compose.yaml
composure: that would not survive unquoted: YAML reads `3.10` as a number, not as text —
and whether you meant the text or the a number is a question only you can answer.
Quote it yourself — "3.10" — and it goes in as typed
```

What you type is what the file gets. Nothing is quoted, unquoted or re-quoted
for you, because quoting on your behalf is a formatting opinion and guessing
what you meant is worse. Quote it yourself and it goes in byte for byte.

### The two refusal families

Everything above is one of two kinds, and the message tells you which:

- **"cannot be done safely"** — flow-style insertion, an anchor, an alias, a
  stale range, a duplicate name, an ambiguous scalar, a missing list index, a
  `.env` conflict. These are permanent. The operation is not safe, and no
  version of Composure will do it.
- **"not built yet"** — block scalars, and a multi-line Dockerfile instruction,
  which cannot be rewritten in place without a line-break policy. These say so
  in the message. Edit them in the text editor.

Both exit 3 and both write nothing.

---

## 7. Adding something that is not there yet

Everything here **inserts what you named where you chose**. Nothing scaffolds:
Composure does not write a default driver, a starter healthcheck or any other line
you did not type. That is a deliberate boundary — see
[§14](#14-not-built-yet).

### A service

From the pane, `+ add` on the canvas. From the CLI:

```console
$ composure add -kind service -name cache2 -value redis:7 examples/webstack/compose.yaml

WOULD CHANGE — examples/webstack/compose.yaml
==============================================================================
  insert_key       add cache2 under services
  insert_key       add image: redis:7 under services.cache2

@@ -106,6 +106,8 @@
       - migration
   PolicyServer:
     image: redis:latest
+  cache2:
+    image: redis:7

 networks:
```

Add `-write` to perform it. A service is two operations — the name, then its
image — but it is **one request, one diff and one undo**. A `cache2:` with
nothing under it is not a stack anything can run, and leaving one on disk
because a second call failed is exactly the partial write this path exists to
make impossible.

The name goes after the last thing already in its block, at the indentation the
file already uses, with the file's own line ending.

### A network, volume, config or secret

```bash
composure add -write -kind network -name frontend .
composure add -write -kind volume  -name pgdata .
composure add -write -kind config  -name nginx .
composure add -write -kind secret  -name api-key .
```

One operation, or two when the top-level block does not exist in the file yet
(`networks:` then `frontend:`). It is written as `frontend:` with no trailing
space and no invented body. A default driver is a scaffold, and scaffolds are
not this.

Top-level blocks can be inserted at the document root directly:
`-op insert_key -at "" -key configs`.

### A key in a free-form mapping

`environment`, `labels`, `build.args` and anything else the specification
leaves open get a **`+ key`** row at the foot of the mapping in the inspector:
a key field and a value field. Fill both, press `Enter`, and it stages.

Which mappings are free-form is derived from the specification, not from a list
somebody typed — a hand-maintained list of "the free-form keys" is wrong within
a release.

### A list entry

An expanded list gets a **`+ entry`** row at its foot. Type and press `Enter`
to stage an entry at the end of the list. It is a field rather than a button
for the same reason as everything else here: a button would add a `- ` nobody
typed. The field clears on every render, so a second entry is a second `Enter`.

Each existing entry carries a **`Remove`** control, which stages the removal and
writes nothing until you save.

```bash
composure preview -op insert_sequence_entry -at services.gateway.ports -value "9090:90" .
```

### A Dockerfile instruction

The Dockerfile view lists each stage with its instructions in order, and under
the last one an **`Available here:`** line naming every instruction the grammar
permits in that stage and this one does not use — `ADD`, `ARG`, `CMD`,
`ENTRYPOINT`, `ENV`, `EXPOSE`, `HEALTHCHECK`, `LABEL`, `MAINTAINER` (struck
through, superseded by `LABEL org.opencontainers.image.authors`), `ONBUILD`,
`SHELL`, `STOPSIGNAL`, `USER`, `VOLUME`. It is the compose inspector's
`available, not set`, in the other grammar.

An instruction is appended to the end of the stage you name, because order is
semantic in that grammar:

```bash
composure preview -op insert_instruction -stage 1 -value "USER app" ./Dockerfile
```

### A build stage

**`+ add stage`** in the Dockerfile pane header, or:

```bash
composure preview -op insert_stage -value nginx:1.27 -key serve ./Dockerfile
```

A `FROM` is appended after the file's last instruction, with `-key` as its `AS`
name and no `AS` clause when `-key` is empty. The keyword takes the casing the
file already uses.

---

## 8. Comments

Comments are the whole thesis. The splice engine exists so that changing a port
does not move the sentence above it — so authoring them is the same operation
as everything else.

Every editable row in the inspector carries a **`#`** control. It opens a field
for the comment above the key, and one for the comment trailing the value on
the key's own line.

```bash
composure preview -op set_comment -at services.db.image -where above \
    -value "# pinned for the 16 -> 17 migration" .
composure preview -op set_comment -at services.db.image -where trailing \
    -value "# see INFRA-902" .
composure preview -op delete_comment -at services.db.image -where trailing .
```

Two things to know:

- **`above` is a run, and a run is one comment.** Three comment lines over a key
  are one thing. A two-line `-value` writes two lines and replaces the whole
  run.
- **`trailing` is found from the end of the value**, not from the first `#` on
  the line — so a `#` inside a quoted value is not mistaken for the start of a
  comment.

The refusals are in [§6](#6-why-an-edit-is-refused): block scalars, anchored or
aliased values, flow collections, and deleting a comment that is not there.

---

## 9. Moving a value into a variable

### Into a `.env` (compose)

Every scalar row in the inspector carries a **`${}`** control. Press it, give
the variable a name, and Composure shows you **both** diffs before anything is
written — the compose file and the `.env`.

```console
$ composure extract -at services.db.environment.POSTGRES_PASSWORD -name POSTGRES_PASSWORD \
    examples/webstack/compose.yaml

WOULD MOVE the value at services.db.environment.POSTGRES_PASSWORD into ${POSTGRES_PASSWORD}
==============================================================================
@@ -74,7 +74,7 @@
     environment:
       POSTGRES_DB: shipyard
       POSTGRES_USER: shipyard
-      POSTGRES_PASSWORD: hunter2      # plaintext credential — a finding later
+      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}      # plaintext credential — a finding later
     volumes:

examples/webstack/.env (new)
--- a/.env
+++ b/.env
@@ -0,0 +1 @@
+POSTGRES_PASSWORD=hunter2
```

Note that the trailing comment survives the move.

This is the only operation in the product that changes two files, which is why
it is not an `-op`: it is its own subcommand, both diffs are shown, and without
`-write` neither file is written. In the pane the save button reads
`Save to compose.yaml and .env`.

The `.env` written is the one **Compose interpolates from** — never an
`env_file`, which Compose does not consult for interpolation, so a value written
there would resolve to nothing.

If the `.env` already sets that name to a different value, the whole thing is
refused, naming both values:

```console
composure: that variable is already set to something else: .env already sets
POSTGRES_PASSWORD at .env:1, to "different" — and the value being moved is "hunter2".
Overwriting would destroy a value somebody configured, and a second line of the same
name is a file whose meaning depends on which parser reads it. Choose another name, or
settle the two by hand
```

The same name already set to the *same* value is not a conflict; the `.env` is
left byte-identical.

The plaintext-credential diagnostic offers this move as its own remedy, and
prints the exact command:

```
and: move the literal into ${POSTGRES_PASSWORD} and write POSTGRES_PASSWORD=… to .env.
     Nothing is written until you run it
     composure extract -at services.db.environment.POSTGRES_PASSWORD -name POSTGRES_PASSWORD -write …
```

### Into a build argument (Dockerfile)

A Dockerfile has no `.env` equivalent — `docker compose` passes build arguments
only through `build.args`, and a `.env` never reaches an `ARG`. So the same
subcommand does a different operation, chosen by the file's own grammar:

```console
$ composure extract -instruction 5 -name NODE_VERSION ./Dockerfile

WOULD MOVE the value into the build argument ${NODE_VERSION}
==============================================================================
@@ -3,7 +3,8 @@
 # the image-discovery work (phase 6) offers an upgrade for.

-FROM node:18-alpine AS build
+ARG NODE_VERSION=18-alpine
+FROM node:${NODE_VERSION} AS build
 WORKDIR /app

Scope: global. a FROM can only use an ARG declared before the FIRST FROM, so the
declaration went above line 6 and could not go anywhere else — inside a stage it
would expand to the empty string with no error
```

`-instruction N` is the zero-based index over **all** instructions in the file,
the same one `-op replace_args` takes. `composure dockerfile -json` reports it as
`index` on each instruction.

Where the declaration lands is decided by Docker's own rules, not by
convenience. A `FROM` can only use an `ARG` declared before the first `FROM`, so
that goes at the top. A value inside a stage gets its declaration directly above
its own instruction inside that stage, because an `ARG` used before it is
declared expands to the empty string with no error at all. A global `ARG` a
stage cannot see is pulled in with a bare `ARG NAME`, which is Docker's own way
of doing it.

This writes **one** file. The literal stays in it as the `ARG`'s default and is
never written to a `.env`. Composure does not wire `build.args` for you — which
service builds this Dockerfile is a resolution question rather than an edit one,
and putting the default in two places is two answers that can disagree — but the
result tells you exactly what to write:

```
Nothing feeds `NODE_VERSION` from compose. … to set it from the stack, add
`NODE_VERSION: ${NODE_VERSION}` under the service's `build.args:` yourself.
```

A value that cannot be written as a **bare** `ARG` default — one containing a
space, a quote, a `#`, a backslash or a `$` — is refused rather than quoted on a
guess, because an `ARG` default has no second reader to check the quoting
against.

> **This has no pane affordance yet.** The `${}` control is on compose rows only;
> the Dockerfile stage form does not offer the move. The core supports it and
> the CLI performs it. Use the CLI.

---

## 10. Docker Hub

This is the only part of Composure that leaves your machine. Everything else is a
pure function of files on disk.

### The upgrade pill

Anywhere an image reference is shown — a compose `image:` row in the inspector,
and each stage's `FROM` in the Dockerfile view — Composure shows the tag's age and,
where there is one, a pill naming a newer tag:

```
FROM   node:18-alpine
       [ node:22-alpine · minor · 40MB smaller ]
       line 6
       The image reference alone is replaced. The --platform flag, the AS clause,
       the keyword casing and any trailing comment stay exactly as written.
       This tag is 14 months old. Docker Hub, not your files.
```

The Dockerfile pane header carries the file-level answer:
`2 stages · 5 instructions add a layer · base image 14 months old`.

Accepting a pill stages an ordinary edit. It goes through the same pending diff
and the same `Save to <file>` press as everything else, and it replaces the
image reference alone. It is not a second write path.

Where there is **no** pill, the row says why — `offline`, `rate-limited`,
`other-registry` and the rest each get a sentence. Saying nothing would read as
"there is nothing newer", which is the confident wrong answer in the exact place
you are deciding whether to upgrade.

A candidate is only offered if it is **in the same family** as the tag it would
replace, **stable**, **not a date stamp**, and a **strictly higher version** —
never merely pushed more recently. `alpine:edge` and `golang:tip-alpine3.24` are
rolling builds and are strictly worse than the pin they would replace, so they
are never offered.

The same answer from the CLI:

```console
$ composure image stale examples/webstack/docs/Dockerfile

BASE IMAGES — examples/webstack/docs/Dockerfile
==============================================================================
2 stages · base image 16 months old

stage 0 — build
  FROM node:18-alpine
  16 months old
  UPGRADE  node:26-alpine · major · 18MB larger
```

### Image search

The `image:` field in the inspector, and the image field in the `+ add`
composer, carry a search control. Type a name and you get Docker Hub
repositories with their badge, pulls and stars; choosing one fills the field and
stages like any other edit.

```console
$ composure image search postgres

DOCKER HUB — "postgres"
==============================================================================
REPOSITORY                         BADGE                 PULLS    STARS  DESCRIPTION
postgres                           official                1B+    14981  The PostgreSQL object-relational databa…
dhi/postgres                       hardened                5M+        0  The PostgreSQL object-relational databa…
cimg/postgres                      verified_publisher    500M+        9
```

### One image, in detail

```console
$ composure image lookup postgres:16-alpine

IMAGE — postgres:16-alpine
==============================================================================
state:  ok
        postgres:18-alpine is a major upgrade in the same family.

current: 1 day old, 111MB

UPGRADE  postgres:18-alpine · major · 3.8MB larger

ALSO AVAILABLE (1)
  postgres:17-alpine               major       112MB

quota 180/180 remaining
```

### Offline, rate-limited, and everything else

A lookup answers with a **state** and a sentence, never an error string, and it
**exits 0 whichever it is** — being offline is not a script failure. The states
are `ok`, `current`, `offline`, `rate-limited`, `not-found`, `other-registry`,
`not-comparable` and `disabled`.

```console
$ COMPOSURE_OFFLINE=1 composure image lookup postgres:16-alpine
state:  disabled
        Docker Hub lookup is switched off, so nothing here reaches the
        network. Everything else on this pane is read from your files.

$ composure image lookup ghcr.io/shipyard/web:2.4.1
state:  other-registry
        Composure looks images up on Docker Hub. This one is on ghcr.io, and
        searching across registries is deliberately not built — so nothing
        is claimed about it either way.
```

That last one matters. An empty answer reads as "there is nothing newer", which
would be a confident wrong answer about an image Composure never looked at.
A reference built from a variable, `scratch`, and a digest pin are each named
as what they are: there is no tag to compare, which is not the same as there
being nothing to say.

Every request is bounded by `-timeout`, five seconds by default, so a hung
socket cannot outlive the question. Anonymous Docker Hub allows 180 requests a
minute per IP address, shared by everyone behind it; when that runs out the
state is `rate-limited` and the rest of the pane is unaffected.

Two ways to switch it all off: `COMPOSURE_OFFLINE=1` in the environment, which
refuses before the transport is entered and suppresses even a cached answer, or
`composure.dockerHub: off` in VS Code settings.

There is no vulnerability facet and there will not be one — Docker Hub's public
API does not carry that data.

---

## 11. The CLI

The CLI is not a debugging aid. It is the primary surface: every capability is
a subcommand before it is a pane, because the corpus harness that gates this
engine can only exercise headless code.

```
composure resolve  [-json] [-f file...] <path>   resolve a project, with provenance
composure explain  [-json] <config-path> <path>  which file set a value, and what it overrode
composure topology [-json] [-profile p] <path>   the graph of what talks to what
composure impact   [-json] [-profile p] <config-path> <path>   what breaks if a node goes down
composure diagnose [-json] [-profile p] <path>   what is wrong with the stack
composure schema   [-json] [-at path] <path>     what you set, and what you could set
composure dockerfile [-json] [-at p] <path>      a Dockerfile as stages and instructions
composure editable [-json] -at ... <path>        whether a value can be edited in place, and why not
composure preview  [-json] -op ... <path>        the diff an edit would produce. Writes nothing
composure apply    [-json] -op ... <path>        the same edit, written through the splice engine
composure add      [-json] [-write] -kind ... <path>            declare a service, network, volume, config or secret
composure extract  [-json] [-write] -at ... <path>              move a literal into a variable, and into the .env
composure extract  [-json] [-write] -instruction N <Dockerfile> move a literal into a build argument
composure image lookup [-json] <reference>       what an image is, and what is newer
composure image search [-json] <query>           find an image on Docker Hub by name
composure image stale  [-json] <Dockerfile>      every stage's base image, and the file's age
composure serve                                  JSON-RPC 2.0 over stdio, for the editor
```

**Flags precede the positional path**, `-json` included. A directory is expanded
with the Compose candidate order — `compose.yaml`, `compose.yml`,
`docker-compose.yaml`, `docker-compose.yml` — and its `compose.override.yaml` is
picked up automatically. With no path at all, the current directory is used.

`-f` names an explicit chain, repeatable, merged left to right by the Compose
merge rules. **Passing `-f` disables the automatic override pickup**, which is
what Compose does.

Every subcommand emits a stable schema under `-json`; the human table is a
separate rendering of the same struct, so reformatting a table cannot move a
gate.

### What each subcommand answers

| Subcommand | The question |
| --- | --- |
| `resolve` | What is the effective configuration, and where did each value come from? |
| `explain` | Which file, line and column set this one value, and what did it override? |
| `topology` | What talks to what, in what start order? |
| `impact` | What breaks if this goes down, and what does it need first? |
| `diagnose` | What is wrong with this stack? |
| `schema` | What does this thing declare, and what could it declare? |
| `dockerfile` | What are this Dockerfile's stages, instructions and image references? |
| `editable` | Can this value be edited in place, and if not, where would the edit land? |
| `preview` / `apply` | What exactly would change on disk? Then: do it. |
| `add` | Declare something that is not in the file yet. |
| `extract` | Move a literal into a `.env` variable, or into a build `ARG`. |
| `image` | What is this image, what is newer, and how old is it? |

`preview` and `apply` are the same code one boolean apart: **the diff `preview`
prints is the diff `apply` writes.**

`explain` on a path that addresses nothing is an error naming the closest paths
that do:

```console
$ composure explain services.nope.image examples/webstack
composure: no such config path: services.nope.image
did you mean: services.api.image, services.cache.image, services.db.image, services.web.image, services.worker.image
```

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. Also: a clean `diagnose`, and **every** `image` state including offline and rate-limited. |
| `1` | Failure. A file that will not parse, a path that does not resolve, a missing file in a `-f` chain. |
| `2` | Usage. A missing flag, an unknown subcommand, no arguments. |
| `3` | **Refused.** The operation could not be performed safely; nothing was written. |
| `10` / `20` / `30` | `diagnose` only: the highest severity found — hint, warning, error. |

### Checking a stack in CI

`diagnose` exits on the highest severity it found, so it is a gate with no
wrapper:

```yaml
- run: composure diagnose .
```

That fails the job on any finding. To fail only on warnings and above, allow the
hint code through:

```bash
composure diagnose . || [ $? -eq 10 ]
```

Or take the JSON and decide for yourself:

```bash
composure diagnose -json . | jq -e '[.findings[] | select(.severity == "error")] | length == 0'
```

`examples/webstack` has six findings on purpose and exits **20**. That is the
tool working, not the tool failing.

A file that will not parse exits 1 rather than producing findings, so a broken
file and a bad stack are distinguishable in a pipeline.

### Scripting an edit across many repositories

This is the other thing the CLI is genuinely better at than the pane: the same
change, in fifty repositories, each with a two-line diff and no reformatting.

```bash
#!/usr/bin/env bash
set -euo pipefail

for repo in repos/*/; do
  file="$repo/compose.yaml"
  [ -f "$file" ] || continue

  # Look before you leap. `editable` always exits 0 and answers in the payload:
  # `editable: true` means a replace will land; `reason` is `absent`,
  # `inherited` or similar otherwise, with `plan` naming the op to use instead.
  composure editable -json -at services.db.image "$file" \
    | jq -e '.editable' > /dev/null || { echo "SKIP     $file"; continue; }

  if composure apply -op replace_scalar \
       -at services.db.image -value postgres:17-alpine "$file"; then
    git -C "$repo" commit -am "bump postgres to 17-alpine"
  else
    case $? in
      3) echo "REFUSED  $file" ;;   # anchor, alias, block scalar, flow style…
      *) echo "FAILED   $file" ;;   # will not parse
    esac
  fi
done
```

The exit codes are what make this safe. Code 3 means the file is untouched and
you should look at it by hand; anything else means the file was never a
candidate. There is no case in which a partially written file is left behind.

Preview first over the whole set, if you would rather read before writing:

```bash
for f in repos/*/compose.yaml; do
  composure preview -op replace_scalar -at services.db.image -value postgres:17-alpine "$f"
done
```

### `composure serve`

The same core speaking JSON-RPC 2.0 with LSP-style `Content-Length` framing on
stdin and stdout. This is what the extension spawns. `stdout` carries the
protocol and nothing else; every diagnostic goes to `stderr`.

Methods: `initialize`, `stack/resolve`, `stack/explain`, `stack/topology`,
`stack/impact`, `stack/diagnose`, `stack/schema`, `stack/dockerfile`,
`stack/editable`, `stack/preview`, `stack/apply`, `stack/add`, `stack/extract`,
`stack/extract-apply`, `stack/extract-arg`, `stack/extract-arg-apply`,
`image/lookup`, `image/search`, `shutdown`, `exit`.

Each read method returns exactly what the corresponding `-json` subcommand
prints. `stack/preview` writes nothing. `stack/add` is `composure add` without the
write: it returns the operations that perform the declaration and touches
nothing, so a client can hold them alongside whatever else you have staged and
send the lot as one apply.

---

## 12. Troubleshooting

### "No Composure core for this platform"

The banner names the path it looked at and the platform it derived:

```
No Composure core for this platform
expected: /path/to/extension/bin/darwin-arm64/composure
platform: darwin-arm64
```

You have installed a VSIX built for a different platform, or a
platform-independent one. Install the VSIX matching the `platform:` line. The
targets are listed in [§1](#1-install).

If you are developing against a local build, set `composure.corePath` to the
absolute path of your `go build` output. The setting is machine-scoped and
cannot be set from a workspace `settings.json`, on purpose — it names an
executable to run.

### "The Composure core is the wrong version"

```
The Composure core is the wrong version
core protocol 8, extension expects 9
```

The extension and the core binary are from different builds. This is checked at
the handshake rather than at the gesture, deliberately: a core missing a method
would otherwise fail at the moment you pressed Save, with your edit staged and
your file half in mind. Reinstall the extension, or — if you set
`composure.corePath` — rebuild the core from the matching commit with
`make extension-core`.

### A file that will not parse

The graph pane **holds the last good graph, dimmed**, with a banner naming the
file and line, and the inspector goes read-only. The view is not cleared: you
are mid-edit, and losing the picture is worse than a stale one. Fix the syntax
error and it redraws.

From the CLI:

```console
$ composure resolve examples/broken
composure: examples/broken/compose.yaml:7:5: yaml: line 6: did not find expected ',' or ']'
```

Exit 1, not a finding. A file that does not parse has no configuration to have
findings about.

### A file with a BOM, or CRLF line endings

Both are handled, and both stay. A UTF-8 byte-order mark is preserved; CRLF
files stay CRLF, including for content Composure inserts. There is nothing to
configure and no reason to convert the file first.

This is worth stating because it has been wrong before. A BOM once made a file
report zero stages while every operation failed silently, and an inserted line
once landed with an LF ending in a CRLF file. Both are permanent regression
fixtures now (`testdata/edge/e10-bom-crlf.yml`, `e14-crlf-comments.yml`,
`e18-crlf-four-space.yml`, `e21-crlf.Dockerfile`, and others). If you find a
third case, it belongs in that directory.

### An edit that stages and then vanishes

The file changed on disk under a staged edit. Composure discards the stage rather
than writing a byte range that has moved, and says so. Re-stage it.

### The pane is empty, or does not open

Composure activates on `workspaceContains` — the compose file must be in the open
folder, not just open as a loose file. Run **Composure: Show Stack** from the
command palette. If the core logged something, it is in the Composure output
channel.

### Docker Hub says nothing

Check `composure.dockerHub` and `COMPOSURE_OFFLINE`. If both are on and you still get
`offline` or `rate-limited`, that is the network or the 180-per-minute anonymous
quota; the rest of the pane is unaffected either way.

---

## 13. What Composure will not do

These are decisions, not gaps.

**It never reformats.** Not indentation, not quoting style, not key order, not
flow style, not blank lines, not line endings, not the BOM. An edit patches the
bytes of the thing you changed and nothing else. Measured across 146 real
compose files from ten public repositories: 98.63% of them round-trip
byte-identical and none is damaged, against 19.86% for the
parse-and-re-serialise approach. Changing one image tag in Sentry's compose file
rewrites 332 lines under re-emit, and 2 under Composure.

**It never re-emits.** There is no code path in this product that regenerates a
document from a model. Structural edits are held to a stricter bar than "it
still parses": the output must equal the input with exactly one contiguous
block of lines added or removed, and nothing else touched.

**It refuses rather than guesses.** Every case in [§6](#6-why-an-edit-is-refused)
is a place where a plausible guess exists and Composure does not make it.

**It does not run `docker compose config`.** That returns a flattened document
with the provenance thrown away, which is the entire value being added. The
Docker CLI is used for lifecycle only, and as an oracle in the test harness —
never in the resolution path.

**It does not need a daemon**, and does not run your stack. Reading, resolving,
diagnosing and editing are all pure functions of files on disk.

**Nothing leaves your machine** except Docker Hub lookups, and those are one
setting away from off.

**Node positions never enter the file.** Drag every node in the graph, then run
`git diff`. It is empty.

---

## 14. Not built yet

Stated plainly, because a manual that omits a gap is a manual you stop trusting
when you find it.

| | State |
| --- | --- |
| **Layer-cost annotation** | **Not built.** The Dockerfile view counts instructions that add a layer — `5 instructions add a layer` — but does not report the size of any one layer, or which layer breaks the cache when a source file changes. The design called for both. |
| **Structural delete from the UI** | **Deliberately out of scope**, not missing. One entry of a list can be removed with its `Remove` control. Deleting a service, a network or a key is not offered: it is destructive, it is what git and undo are for. The engine has `delete_key`, and `composure apply -op delete_key -at …` performs it from the CLI. |
| **Moving a Dockerfile value into a build `ARG`, from the pane** | **Not built at the time of writing.** The core method and the CLI form both work — `composure extract -instruction N <Dockerfile>` — but the stage form has no control for it. The compose half (`${}`, into a `.env`) is built. |
| **Rewriting a block scalar** | Not built. Refused with a message that says so. |
| **Rewriting a multi-line Dockerfile instruction** | Not built. Refused with a message that says so. It cannot be done in place without a line-break policy. |
| **Scaffolds** | Deliberately out of scope. Composure inserts what you named; it does not write starter content you did not type. |
| **Multi-select** | Deferred. Selection is single: one node, one inspector. |
| **Cross-registry image search** | Deliberately out of scope. Docker Hub only, and a reference on another registry is named as such rather than answered emptily. |
| **Vulnerability data** | Will not be built. Docker Hub's public API does not carry it. |
| **Kubernetes, Swarm, Terraform** | Out of scope for this product. |

---

## Where the numbers come from

Every measured claim in this manual is produced by `make bench` against a corpus
you can fetch in one command, and 29 of the metrics are enforced against
`benchmarks/baseline.json` on every commit. `RESULTS.md` has the full tables,
including the four bugs the harness caught that the unit tests did not.

```bash
make corpus     # ~2 min, ~2.1GB of shallow clones. Fetched, never committed
make bench      # the five benchmarks as human-readable tables
make gate       # the same, checked against the committed baseline
```

A failing fidelity test is never fixed by changing the threshold. CI blocks any
pull request that edits the baseline without a written justification.

## Further reading

- [`README.md`](../README.md) — what the product is, and the measurement behind it
- [`DECISIONS.md`](../DECISIONS.md) — every design decision and why it was made
- [`RESULTS.md`](../RESULTS.md) — the full benchmark tables
- [`CLEANROOM.md`](../CLEANROOM.md) — the clean-room rule and the licence gate

Composure is Apache-2.0, and the core stays Apache-2.0 permanently. No capability
is ever removed from the free tier to create a paid one.
