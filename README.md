# Composure

*A local workbench for creating, understanding and safely editing container
and infrastructure configuration.*

> **The file is the source of truth. Everything else is a faithful view of it.**

Named for what the tool exists to preserve — yours, when the stack you inherited
is four files, thirty services and something already listening on 5432; and the
file's, which it will not reformat to answer you.

Composure ships as a **VS Code extension** with a Go core. The core is a
subprocess spoken to over JSON-RPC on stdio — the `gopls` / `rust-analyzer`
arrangement — and it is independently runnable as a CLI, so every capability is
scriptable and testable without an editor.

---

## What it is

You inherited a compose stack. It is four files, two of them overrides, one
`include:`, thirty services, and a `.env` you have never read. Something is
listening on 5432 and you do not know which file said so.

Composure answers that, on the file, without a daemon:

**It shows the resolved project, not one document.** `compose.override.yaml`,
explicit `-f` chains, `include:` followed recursively, `extends:` across files,
per-key Compose merge semantics, `.env` and `env_file` interpolation, anchors
and merge keys expanded — then drawn as a graph beside the editor.

**It answers which file set a value.** Every resolved value carries
`{file, line, column, merge step}`. The inspector shows
`compose.yml:12 · overrides :7` under the value, and clicking it puts your
cursor on the line that won. This is not recoverable from a flattened
`docker compose config`, which is exactly why Composure does not use one.

**It shows what you have *not* declared.** `available, not set` lists every key
the Compose specification permits on the selected thing and you have not used —
generated at runtime from the vendored specification, never from a list
somebody typed and stopped maintaining. Keys newer than the `docker compose` on
your machine are marked, not hidden.

**It writes back without wrecking the file.** Change a value, read the diff,
then save.

## Why not the free extensions already in this channel

Two incumbents give away part of this, and it is worth being precise about
which part.

| | Docker DX | Compose Visualizer | **Composure** |
| --- | :-: | :-: | :-: |
| Schema completion and hovers | yes | — | — |
| Dependency graph | — | yes | yes |
| Resolves the **merged** multi-file project | — | — | **yes** |
| Says which file, line and merge step set a value | — | — | **yes** |
| Lists the keys you have **not** declared | — | — | **yes** |
| **Writes back to the file** | — | — | **yes** |

Neither incumbent writes back, and neither shows what is absent. Reading is
commoditised here. The wedge is that Composure closes the loop: it is the only one
of the three that will change the file, and the only reason it is safe to is
the engine below.

---

## The engine, and the measurement behind it

**Never parse into a model and serialise back.** An edit locates the byte range
of the thing being changed and patches the original buffer. Unchanged bytes are
unchanged *by construction*, not by effort.

This is the opposite of what nearly every YAML example does, and the difference
is not marginal. Measured across **146 real compose files** from ten public
repositories — awesome-compose, Immich, Paperless-ngx, Nextcloud, Sentry
self-hosted, GitLab, n8n, Grafana, Airflow, example-voting-app:

| Strategy | Byte-identical round trip | Files damaged |
| --- | ---: | ---: |
| `yaml.v3` parse → re-encode | 19.86% | 117 |
| `goccy` parse → re-render | 58.22% | 59 |
| **splice bytes in place** | **98.63%** | **0** |

And on the operation the product actually performs — change one image tag, then
measure the diff you would have to put in a pull request. 71 of the 146 files
had an editable image scalar:

| Strategy | Minimal two-line diff | Avg lines changed | Worst case |
| --- | ---: | ---: | ---: |
| `yaml.v3` re-emit | 32.4% | 11.2 | **332 lines** |
| `goccy` re-emit | 62.0% | 3.4 | 22 lines |
| **splice** | **100%** (71/71) | **2.0** | **2 lines** |

The 332-line worst case is Sentry's `docker-compose.yml`. Changing one image
tag rewrites 332 lines of it under the re-emit approach, and two under splice.

Structural edits are held to a stricter bar than "it still parses": the output
must equal the input with **exactly one contiguous block** of lines added or
removed and nothing else touched. `InsertKey` and `DeleteKey` are at 100% on
that, across 92 and 74 operations. The Dockerfile engine — a separate engine,
different grammar — is at 100% on all five of its metrics across 179 real
Dockerfiles.

**Correctness of the resolver, not just of the bytes.** The merged model is
compared against `docker compose config` as an oracle across the corpus:

```
compared:     74  (both sides resolved)
  agreed:     74
  diverged:    0
PASS RATE: 100.00% of 80 compared projects
MULTI-FILE:  6 compared, 6 agreed (100.00%)
```

The CLI is the oracle in the *test harness only*. It is never in the resolution
path, because it returns a flattened document with no provenance — the entire
value being added.

**Speed.** A 500-service project (`examples/large`, 771 graph nodes, 767 edges)
resolves in **9.6 ms** and resolves-plus-builds-the-graph in **10.4 ms**,
measured in-process with `go test -bench` on an Apple M2 Pro. End to end through
the CLI, including process start and JSON-encoding the whole
provenance-bearing model, it is roughly 50–80 ms. The requirement was two
seconds.

All of these numbers are produced by `make bench` against a corpus you can
fetch in one command, and 15 of them are enforced against
[`benchmarks/baseline.json`](benchmarks/baseline.json) on every commit. See
**[RESULTS.md](RESULTS.md)** for the full tables, including the four bugs the
harness caught that unit tests did not.

> A failing fidelity test is never fixed by changing the threshold. CI blocks
> any pull request that edits the baseline without a written justification.

---

## Install

Search **Composure** in the extensions panel, or:

```bash
code --install-extension elzouhery.composure
```

On **Cursor, Windsurf, VSCodium, Gitpod** or any other fork, the same extension
is on Open VSX, which is what those editors search:

```bash
codium --install-extension elzouhery.composure
```

| | |
| --- | --- |
| VS Code Marketplace | [marketplace.visualstudio.com/items?itemName=elzouhery.composure](https://marketplace.visualstudio.com/items?itemName=elzouhery.composure) |
| Open VSX | [open-vsx.org/extension/elzouhery/composure](https://open-vsx.org/extension/elzouhery/composure) |

Seven platform builds are published to both — macOS on Apple silicon and Intel,
Linux x64 and arm64 on glibc and musl, and Windows x64. The registry serves the
one your machine needs, so you download a single static core binary rather than
seven. Nothing else installs, no daemon runs, and reading or editing needs no
Docker.

Then open a folder containing a `compose.yaml` and open the file. The panel
opens beside it, or run **Composure: Show Stack** from the command palette.

**[docs/USER-MANUAL.md](docs/USER-MANUAL.md)** is the manual: the graph and its
edge kinds, the inspector and provenance, staging and the pending diff, the full
list of why an edit gets refused, adding, comments, moving a value into a `.env`
or a build `ARG`, Docker Hub, the CLI and its exit codes, and troubleshooting.

### From source

```bash
git clone https://github.com/elzouhery/Composure.git
cd composure
make package                       # cross-compiles every core, builds the VSIX set
code --install-extension extension/build/composure-darwin-arm64.vsix
```

`make package` writes one VSIX per target — `darwin-arm64`, `darwin-x64`,
`linux-x64`, `linux-arm64`, `alpine-x64`, `alpine-arm64`, `win32-x64` — and each
carries exactly one core.

Requires Go 1.24+ and Node 20+ to build. Running it needs nothing but VS Code
1.85 or a fork on the same extension API.

## Quick start, without the editor

Every capability is a CLI subcommand before it is a panel, because the corpus
harness can only exercise headless code:

```bash
go build -o bin/composure ./cmd/composure

# the merged project, with provenance on every value
./bin/composure resolve examples/webstack

# which file, line and column set this value — and what it overrode
./bin/composure explain services.db.image examples/webstack

# what talks to what, and what is wrong with it
./bin/composure topology examples/webstack
./bin/composure diagnose examples/webstack

# what you set, and what the spec would let you set
./bin/composure schema -at services.web examples/webstack

# the diff an edit would produce. Writes nothing
./bin/composure preview -op replace_scalar \
    -at services.db.image -value postgres:17-alpine examples/webstack
```

Flags precede the positional path, `-json` included. Every subcommand emits a
stable schema under `-json`; the human table is a separate rendering of the
same struct.

`diagnose` exits on the highest severity it found — `0` clean, `10` hint, `20`
warning, `30` error — so it is usable as a CI check. The `examples/webstack`
project has three findings on purpose, so that command exits 20 rather than 0.
That is the tool working.

`composure serve` is the same core speaking JSON-RPC 2.0 on stdio, which is what
the extension spawns. Every capability above is reachable there too — that is
the rule, not a coincidence: nothing is built into the panel that the CLI
cannot also do, because the corpus harness can only exercise headless code.

## Reproducing the numbers

```bash
make corpus     # ~2 min, ~2.1GB of shallow clones. Fetched, never committed
make bench      # the five benchmarks as human-readable tables
make gate       # the same, checked against benchmarks/baseline.json — 26 metrics
make check      # build, vet, test, licence, gate: everything CI runs
```

`make differential` compares the resolver against `docker compose config` over
the corpus. It needs a Docker daemon, takes minutes, and is deliberately *not*
in the gate — a baselined metric that silently skips is a gate that passes
vacuously.

## Layout

| Path | Purpose |
| --- | --- |
| `internal/resolve` | Multi-file merge, interpolation, and the provenance model |
| `internal/topology` | The graph, derived from the resolved model |
| `internal/diagnose` | One file per rule, all returning anchored findings |
| `internal/strategy` | The YAML splice engine, and the two re-emit comparators it is measured against |
| `internal/dockerfile` | Dockerfile parse + splice: continuations, heredocs, escape directives |
| `internal/schema` | The vendored Compose specification; feeds `available, not set` |
| `internal/hub` | Docker Hub search and tag semantics |
| `internal/edit`, `internal/report`, `internal/corpus` | Edit operations, failure taxonomy, corpus fetching |
| `cmd/composure` | The CLI, and the JSON-RPC server the extension spawns |
| `cmd/fidelity`, `editbench`, `structbench`, `dockerbench`, `enginebench` | The five benchmarks |
| `cmd/gate`, `cmd/licencescan` | The CI gates |
| `cmd/differential` | The resolver checked against `docker compose config` |
| `extension/host` | Activation, subprocess lifecycle, JSON-RPC client, problems panel |
| `extension/webview` | The canvas, the inspector, the pending diff |
| `testdata/adversarial`, `testdata/edge` | Every bug ever found, kept as a permanent regression file |

## Where this actually is

Honest state, because a README that overstates is the first thing a reader
stops trusting.

| | |
| --- | --- |
| **Phase 1 — the read path** | Built. Resolution with provenance, topology, diagnostics, the inspector. |
| **Phase 2 — the edit path** | Built. Scalar edit in both grammars, diff before write, the Dockerfile stage form. This was the release gate. |
| **Published** | **Yes.** `elzouhery.composure` 0.2.0, all seven platform targets, on both the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=elzouhery.composure) and [Open VSX](https://open-vsx.org/extension/elzouhery/composure). The Open VSX namespace is not yet verified, so that listing carries a publisher warning. |
| **Phase 3+ — structural authoring, image discovery wired in, runtime** | Not started. The engines for discovery and Dockerfiles exist and are measured; the UI is not built. |
| **Terraform / OpenTofu** | **Not started.** A one-week `hclwrite` spike gates the commitment, and it has not been run. |
| **Corpus scale** | 146 compose files and 180 Dockerfiles. The quality bar wants 5,000+. |

Design decisions and their provenance are in **[DECISIONS.md](DECISIONS.md)**;
constraints for anyone writing code here are in [CLAUDE.md](CLAUDE.md) and
[AGENTS.md](AGENTS.md).

---

## Licence, and the commercial boundary

**Apache-2.0**, and the core stays Apache-2.0 **permanently**.

Paid features, if they ever exist, are **additive and organisational** — team
things layered on top, such as shared stack maps or drift detection across
environments. **No capability is ever removed from the free tier to create a
paid one.** The line is declared here, before the first release, and it does
not move.

This is not modesty, it is arithmetic. Relicensing permissive → restrictive has
triggered a community fork three times out of three — Terraform → OpenTofu,
Redis → Valkey, Elastic → OpenSearch — and Lens and Insomnia were both forked
within months of restricting something users already had. A fork takes the code
and the users; the only thing it cannot take is the trademark.

Apache-2.0 rather than MIT for the explicit patent grant and trademark
language, which a project intending a commercial tier later wants.

### Clean room

This is a clean-room implementation — see **[CLEANROOM.md](CLEANROOM.md)**. No
source under BSL, SSPL, the Elastic License or AGPL is an input to this
codebase, and `make licence` fails the build on any such dependency in the Go
graph. The extension carries **zero runtime npm dependencies**, asserted by a
test, because that licence scan walks the Go build graph and cannot see
`node_modules`.

Where a design decision was informed by observing a competitor's *behaviour* —
never its source — [DECISIONS.md](DECISIONS.md) says so explicitly.

### Contributing

Every commit carries a Developer Certificate of Origin sign-off
(`git commit -s`), and the `DCO sign-off` job in CI fails a pull request whose
commits lack one. [CONTRIBUTING.md](CONTRIBUTING.md) has the rest: the
clean-room rule, the definition of done, and why a threshold is never lowered
to make a build pass.

The DCO certifies origin. It does **not** grant relicensing rights, which
requirements §10 also asks for and which needs a signed CLA — still open, and
open on purpose: a CLA needs a legal entity as counterparty and none exists
yet.
