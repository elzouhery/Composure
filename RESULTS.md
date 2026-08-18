# Fidelity Spike — Results

**Question:** can a YAML engine edit a real docker-compose file without damaging it?
**Answer:** not by re-serialising. Only by splicing bytes. The moat is reachable, and the architecture follows from the number.

Corpus: **146 real compose files** harvested from 10 public repositories (awesome-compose, Immich, Paperless-ngx, Nextcloud, Sentry self-hosted, GitLab, n8n, Grafana, Airflow, example-voting-app) plus **6 hand-written adversarial files**, each stressing one fidelity dimension.

---

## Test 1 — Identity

Parse the file, write it back with **no semantic change**, require byte-identical output. An engine that fails this cannot be trusted to edit anything.

| Strategy | Pass rate | Identical | Damaged | Parse errors |
|---|---:|---:|---:|---:|
| `reemit-yaml.v3` — parse to `yaml.Node`, re-encode | **19.86%** | 29/146 | 117 | 0 |
| `reemit-goccy` — parse to goccy AST, re-render | **58.22%** | 85/146 | 59 | 2 |
| `splice` — patch bytes in place, never re-emit | **98.63%** | 144/146 | **0** | 2 |

Damage profile for the re-emit strategies:

| Defect | yaml.v3 | goccy |
|---|---:|---:|
| blank lines lost | 53.4% | 25.3% |
| indentation changed | 52.7% | 10.3% |
| line count changed | 56.2% | 29.5% |
| document markers changed | 8.2% | — |
| trailing newline changed | 6.8% | 6.8% |
| merge keys flattened | 2.1% | — |
| flow style collapsed to block | 1.4% | — |

## Test 2 — Edit

The operation the product actually performs: change one `image` value, measure the diff the user would have to put in a pull request. 71 files had an editable image scalar.

| Strategy | Applied | Minimal diff | Avg lines changed | Worst case |
|---|---:|---:|---:|---:|
| `reemit-yaml.v3` | 71/71 | 32.4% | 11.2 | **332 lines** |
| `reemit-goccy` | 71/71 | 62.0% | 3.4 | 22 lines |
| `splice` | 71/71 | **100%** | **2.0** | **2 lines** |

*Minimal = one line removed, one line added — the theoretical floor for a single-value change.*

Worst case for both re-emit strategies was Sentry's `self-hosted/docker-compose.yml`. Changing one image tag rewrites **332 lines** of it under yaml.v3.

## Test 3 — Structural edits

Insertion and deletion are the hard half. Scalar replacement overwrites a known byte range; insert and delete change the *shape* of the document, which means answering two questions the parser will not answer for you: where does a node's subtree end, and what indentation should new content use.

Pass condition is deliberately strict: the output must equal the input with **exactly one contiguous block** of lines added or removed, and **nothing else touched**. "It still parses and the value is there" is a weak test — it is satisfied by an engine that also silently reindented forty unrelated lines.

| Operation | Attempted | Applied | Refused | Single-block clean | Reparses | Correct |
|---|---:|---:|---:|---:|---:|---:|
| `InsertKey` | 93 | 92 | 1 | **100%** | **100%** | **100%** |
| `DeleteKey` | 74 | 74 | 0 | **100%** | **100%** | **100%** |

Verified behaviours, each on a purpose-built edge case:

- **Indentation is inferred, not assumed.** A 4-space file gets a 4-space insertion.
- **Comments travel with the node they document.** Deleting a service takes the `# legacy, see INFRA-902` block directly above it.
- **Blank-line hygiene.** A node surrounded by blank lines on both sides collapses one on delete — a human would not leave a double gap.
- **Files with no trailing newline** keep having no trailing newline.
- **Deeply nested subtrees** end where they should: inserting into a service that contains `deploy.resources.limits` appends at the service level, not inside `limits`.

**The one refusal is the interesting result.** Flow-style services — `compact: {image: nginx, restart: always}` — cannot take a block child; the output would be invalid YAML. The first implementation produced exactly that, and the corpus did not catch it because no real file in the 146 uses flow-style services. It took a hand-written edge case to surface, confirmed against a third-party parser:

```
expected <block end>, but found '<block mapping start>'
  in "/tmp/bad.yml", line 3, column 5
```

The engine now returns `ErrFlowStyle` and declines. **Refusing is the correct behaviour and it should be a design principle, not an apology.** An editor that silently emits an unparseable file is worse than one that says no, because the damage surfaces later in someone else's terminal. Every operation the engine cannot perform safely must fail loudly at the point of the request.

## Test 4 — Dockerfile engine

A separate engine: the splice principle carries over, the grammar shares nothing with YAML. Corpus of **179 real Dockerfiles** from the same repositories — 59 multi-stage, 32 using heredocs, 1 with a custom escape character — plus hand-written edge cases.

| Metric | Result |
|---|---|
| Identity (byte-identical) | **100%** (179/179) |
| `SetBaseImage` applied | **100%** (179/179) |
| `SetBaseImage` single-line diff | **100%** (179/179) |
| `InsertAfter` single-block clean | **100%** (179/179) |
| `Delete` single-block clean | **100%** (126/126) |

Verified against the constructs that break naive parsers: `--platform` flags, `AS` clauses and trailing comments all survive a base-image swap untouched; a `# escape=`` ` directive correctly switches the continuation character to a backtick; heredoc bodies are consumed, so a literal `FROM this-is-not-an-instruction` inside a `RUN <<EOT` block is not parsed as a stage; lowercase `from` stays lowercase.

**Two bugs the corpus alone would never have found:**

- **CRLF.** The carriage return was captured as the final byte of the image reference, so replacing it silently rewrote that line's ending from CRLF to LF. Exactly one Dockerfile in the 179-file corpus uses CRLF — enough to be real, not enough to be noticed.
- **UTF-8 BOM.** A leading BOM made the first instruction parse as `﻿FROM`, which matches nothing. The file reported *zero stages* and every base-image operation failed silently rather than erroring.

Both are Windows-authored files, and both fail *quietly*. That is the pattern worth internalising: this engine's failure mode is not a crash, it is a wrong answer delivered confidently.

## Test 5 — Docker Hub search, measured against the live API

| Finding | Detail |
|---|---|
| Authentication | none required for search or tags |
| Rate limit | **180 requests per minute, per IP** |
| Tag metadata | compressed size, last-pushed timestamp, per-architecture digests |
| CVE counts | **not available** — needs Scout (paid beyond one repo) or a local Trivy/Grype scan |

**`hub.docker.com/v2/search/repositories/` is behind Cloudflare bot protection.** It answers curl with 200 and a Go client with 403 and a "Just a moment..." interstitial, from the same IP, regardless of User-Agent — the rejection is on the TLS fingerprint. No header fixes it, and testing with curl will never reveal it. Two endpoints do answer a Go client: `api/search/v4` (richer — badges, architectures, publisher) and the legacy `index.docker.io/v1/search`. The client here uses v4 with the legacy endpoint as fallback.

**That, not the rate limit, is the real argument about a backend.** 180/min per IP is enormous headroom for a desktop app, so v1 needs no server. But none of these endpoints is a documented contract, and Cloudflare's posture toward non-browser clients can change without notice. Build search behind an interface now; add a thin service later if and when endpoint churn starts costing desktop releases.

### The hard part of image lookup is tag semantics, not the API

Every one of these was found by running the tool against a real Dockerfile:

| Naive rule | What it suggested | Why it's wrong |
|---|---|---|
| newest push date, same family | `alpine:3.19` → `alpine:edge` | rolling branch, not a release |
| newest push date, same family | `golang:1.23-alpine` → `golang:tip-alpine3.24` | nightly build |
| highest numeric version | `alpine:3.19` → `alpine:20260805` | date stamp sorts above every real version |
| stable + higher version | `golang:1.23-alpine` → `golang:1.27rc2-alpine3.24` | release candidate — `rc` sits between digits with no separator |
| unfiltered first page of tags | `golang:1.23-alpine` → *"no newer tag"* | the first 100 tags by push date are all `tip-*` nightlies |

The engine now requires a candidate to be the same family, stable, date-free, and a genuinely **higher version** — and it labels the step `patch`, `minor` or `major` rather than hiding a major bump inside a one-line diff. Working output:

```
stage 0 — golang:1.23-alpine  (AS builder)
  candidate: golang:1.26.5-alpine3.24 [minor] — pushed 33d ago, 68.1MB
  diff:
    - FROM golang:1.23-alpine AS builder  # pinned
    + FROM golang:1.26.5-alpine3.24 AS builder  # pinned
```

Note the preserved `# pinned` comment. **That is the whole product in four lines** — discovery, judgement and surgical application, and it only works because the splice engine is underneath. A search box alone is a feature anyone can build against a public API.

---

## What this means

**1. Splice is the architecture.** Do not parse into a model and serialise back. Parse only to locate the byte range of the value being changed, then patch the original buffer. Unchanged bytes are unchanged *by construction* — fidelity stops being a feature you maintain and becomes a property you cannot lose. Every competitor treats it as the former, which is why they all leak.

**2. Go's `yaml.v3` is better than `js-yaml` but nowhere near good enough.** Worth knowing precisely: it *does* preserve comments and anchors, which `js-yaml` structurally cannot. But it strips every blank line, re-indents entire unrelated blocks to its own preference, and emits explicit `!!merge <<: *common` tags that were never in the source. At 19.86% it is not a round-trip engine, it is a formatter.

**3. Parse with `yaml.v3`, splice with your own offsets.** The two splice "failures" are not damage — they are goccy's parser rejecting valid YAML that `yaml.v3` reads fine:

```
corpus-repos/grafana/devenv/docker/blocks/sensugo/docker-compose.yaml
  goccy:  [16:1] value is not allowed in this context
  yaml.v3: <nil>
```

`yaml.v3` had **zero** parse errors across all 146 files. Its `Node` carries `Line` and `Column`, which convert to byte offsets cheaply (`offsetOf` in `strategy.go` is nine lines). That combination gets you to 100%: goccy's robustness problem disappears and yaml.v3's serialisation problem never arises because you never serialise.

**4. The harness paid for itself immediately.** The first splice implementation trimmed only the left side of goccy's token `Origin`, which carries trailing trivia. The end offset overshot by one byte, swallowed the newline, and silently joined the following comment onto the value line:

```diff
-    image: mcr.microsoft.com/azure-sql-edge:1.0.4
-    # If you really want to use MS SQL Server, uncomment the following line
+    image: example.invalid/replacement:v9.9.9# If you really want to use MS SQL Server, uncomment the following line
```

Plausible-looking code, silent corruption, caught in one run against real files. This is the class of bug that destroys trust in an editor, and it is invisible to unit tests written against files you made up. **Keep the corpus in CI and treat the pass rate as the project's headline health metric.**

---

## Verdict on the spike question

> *If the byte-identical pass rate can't get above 95%, the moat isn't reachable.*

**It's reachable.** 98.63% today with an unmodified off-the-shelf parser and roughly 400 lines of splice logic, with a clear path to 100% by swapping the parser. Zero files damaged. Every scalar edit produced the minimum possible diff, and every structural edit changed exactly one contiguous block.

That is a claim no competitor can make, and it is checkable by anyone who clones this repo.

**Two bugs found by the harness, neither by unit tests.** The scalar splice swallowed a newline and welded a comment onto a value line; the structural insert produced invalid YAML on flow-style mappings. Both were plausible-looking code. Both would have destroyed user trust on contact. The corpus and the strict single-block assertion are not overhead — they are the reason the engine is trustworthy, and they belong in CI from the first commit.

---

## Reproducing

```bash
go build -o bin/fidelity ./cmd/fidelity
./bin/fidelity fetch corpus-repos     # shallow-clone the corpus (~2 min)
./bin/fidelity check corpus-repos     # identity pass rates + defect breakdown
./bin/fidelity check testdata         # adversarial files only
go run ./cmd/editbench corpus-repos   # edit benchmark
go run ./cmd/demo                     # side-by-side diff of all three strategies
```

Flags: `-v` lists damaged files, `-json` emits machine-readable scores, `-limit N` caps the run.

## Next steps

1. Replace goccy with `yaml.v3` parsing plus the existing offset conversion; confirm 100%.
2. Expand the corpus from 146 to 5,000+ files. GitHub code search with a token, or clone the top 500 repositories that contain a compose file.
3. ~~Extend `Edit` beyond scalar replacement~~ — **done**, see Test 3. Next: list-item insertion, key reordering, and moving a service between files in a multi-file setup.
4. Add `hclwrite` as a fourth strategy against a Terraform corpus, to confirm the phase-3 assumption before committing to it.

---
---

# Post-spike measurements — 2026-08-12

**Everything above this line is the August spike and is left exactly as it was
written.** It is the historical record: the question was whether the moat was
reachable, and those are the numbers that answered it.

This section is different. Phases 1 and 2 are built — resolution with
provenance, topology, diagnostics, the inspector, and the edit path, behind a
VS Code extension. The question is no longer "is it reachable" but "did
anything drift, and is the rest of the product as measured as the engine was".

Measured on 2026-08-12, Go 1.24.7, Apple M2 Pro, against the same
146-file / 179-Dockerfile corpus.

## The gate has not moved

The five benchmarks are now 26 named metrics checked against
`benchmarks/baseline.json` by `cmd/gate`, run by CI on every commit. Full
output of `make gate`:

```
FIDELITY GATE — 146 compose files, 180 Dockerfiles
==============================================================================
  dockerbench.delete_clean_pct                   100.00   ok
  dockerbench.identity_pct                       100.00   ok
  dockerbench.insert_after_clean_pct             100.00   ok
  dockerbench.set_base_image_applied_pct         100.00   ok
  dockerbench.set_base_image_minimal_pct         100.00   ok
  editbench.splice.minimal_pct                   100.00   ok
  editbench.splice.neg_worst_lines                -2.00   ok
  fidelity.splice.identity_pct                    98.63   ok
  fidelity.splice.undamaged                      146.00   ok
  structbench.deletekey.clean_pct                100.00   ok
  structbench.deletekey.correct_pct              100.00   ok
  structbench.deletekey.reparses_pct             100.00   ok
  structbench.insertkey.clean_pct                100.00   ok
  structbench.insertkey.correct_pct              100.00   ok
  structbench.insertkey.reparses_pct             100.00   ok

FIDELITY GATE PASSED — 26 metric(s) at or above baseline
```

Identity is still 98.63% with zero files damaged; every scalar edit is still a
two-line diff; the Dockerfile engine is still 100% on all five. Six epics of
new code sit on top of this engine and none of it moved a number. That is the
result being reported, and it is the whole reason the gate exists.

## Test 6 — the resolver against `docker compose config`

New, and the one that matters most for phase 1. The spike proved the *bytes*
were safe. It said nothing about whether the merged model is **semantically
right**, and a resolver can be confidently wrong in a way no fidelity metric
would ever notice.

So the merged model is compared against `docker compose config` as an oracle,
project by project, across the corpus. Compose Docker v5.3.1:

| | |
| --- | ---: |
| compose files in the corpus | 146 |
| projects both sides resolved | 74 |
| **agreed** | **74** |
| **diverged** | **0** |
| both refused | 61 |
| `composure` resolved, compose would not | 11 |
| compose resolved, `composure` would not | **0** |

**100.00% agreement across 74 compared projects.**

Two rows deserve reading carefully, because the headline number alone would be
easy to fake:

- **`composure` refused nothing compose accepted.** That is the column a resolver
  cheats on — refuse the hard files and agree perfectly on the easy ones. It is
  zero.
- **The 11 that compose refused and `composure` resolved** are not wins. They are
  mostly files depending on a `.env` or a build context the corpus checkout
  does not carry, where the CLI fails and the resolver, whose job is to model
  the file rather than run it, does not.

`docker compose` is the oracle **in the test harness only**. AD-10 forbids it in
the resolution path, and this harness is the reason that rule is affordable:
you can refuse to shell out for resolution *and* still know you agree with the
reference implementation.

This is deliberately **not** in `make gate`. It needs a Docker daemon and skips
cleanly without one, and a baselined metric that silently skips is a gate that
passes vacuously — worse than no gate. It also takes minutes, because it forks
the CLI once per project. Run it with `make differential` when the merge
changes.

## Test 7 — resolution and graph at scale (N3)

N3 asks for a 500-service resolved topology in under two seconds.
`examples/large` is 500 services, resolving to a 771-node, 767-edge graph.
In-process, `go test -bench`, 20 iterations:

| Operation | Time |
| --- | ---: |
| resolve 500 services, every leaf carrying provenance | **9.6 ms** |
| resolve **and** build the topology graph | **10.4 ms** |

End to end through the CLI — process start, resolve, and JSON-encoding the
entire provenance-bearing model to stdout — it is roughly 50–80 ms.

The requirement is two seconds. This is ~190× inside it, which is worth stating
plainly because it settles a deferred question rather than just looking good:
the architecture spine deferred "whether AD-6's pure derivation needs
memoisation" as a measurement nobody had taken. It has now been taken. It does
not. Recomputing the graph from the resolved model on every profile toggle
costs a millisecond, so no cache is justified, and not having one is what keeps
AD-2 true — nothing accumulates state the file does not have.

The webview side is measured separately in `extension/TESTING.md`: over the
same 771-node graph, 0.20 ms for a search keystroke, 0.47 ms to collapse by
network, 1.34 ms to expand and re-lay-out. Search is not debounced because a
debounce would be latency bought for nothing.

## Test 8 — the extension suite

279 tests, 73 suites, 0 failing, 1.9 s, under `node --test`. Two of them are
load-bearing in a way unit-test counts usually are not:

- **`host/realcore.test.ts` spawns the real Go binary.** Everything else in the
  suite drives a stub, which is what makes lifecycle testable without a Go
  toolchain — and which means nothing else can catch the two ways the halves
  drift apart. The protocol revision is two constants in two languages, and the
  wire keys are read by name in TypeScript and written by struct tag in Go. No
  type system spans either gap.
- **`webview/a11y.test.ts` scans the shipped webview sources** rather than
  listing what exists today, so a control written next year is checked by the
  same pass. It caught two real defects on the code it was written against:
  the failure banner painted the workbench foreground on
  `inputValidation-errorBackground`, and the port node painted it on
  `input-background`. VS Code contributes neither pairing.

  What it does not do is measure a contrast ratio. `node --test` cannot render
  a pixel or resolve a theme token; it refuses pairings the platform never
  promised. The residue needing a human eye is carried in `TESTING.md`, and a
  test asserts the two lists cannot drift.

## Test 9 — packaging

Five static core binaries, cross-compiled with `CGO_ENABLED=0`, `-trimpath`
and `-s -w`:

| Target | Size |
| --- | ---: |
| `darwin-arm64` | 3.85 MB |
| `darwin-amd64` | 3.98 MB |
| `linux-amd64` | 3.96 MB |
| `linux-arm64` | 3.87 MB |
| `windows-amd64` | 4.09 MB |

Packaged into seven platform-specific VSIXes — the two `alpine-*` targets reuse
the Linux binaries, which is what static linking buys. Each is **10 files**:
the manifest, `package.json`, readme, changelog, licence, three `dist/` bundles
and **exactly one** core binary.

| VSIX | Size |
| --- | ---: |
| `composure-darwin-arm64.vsix` | 1.63 MB |
| `composure-darwin-x64.vsix` | 1.73 MB |
| `composure-linux-x64.vsix` | 1.73 MB |
| `composure-linux-arm64.vsix` | 1.59 MB |
| `composure-alpine-x64.vsix` | 1.73 MB |
| `composure-alpine-arm64.vsix` | 1.59 MB |
| `composure-win32-x64.vsix` | 1.78 MB |

An all-platforms VSIX would carry five binaries so that every install downloads
four it can never run. The executable bit survives packaging (`-rwxr-xr-x` in
the archive), and the source, the tests, the source maps and `node_modules` do
not ship.

**Runtime npm dependencies: zero**, now asserted by
`host/packaging.test.ts` rather than remembered. This is a licensing
constraint, not a taste about bundle size: `make licence` walks the *Go* build
graph and cannot see `node_modules`, so the only tree size that gate can vouch
for is empty. The Go graph itself is two modules, both permissive:

```
LICENCE SCAN — 2 third-party module(s) in the build graph
  ok    github.com/goccy/go-yaml    v1.19.2    MIT
  ok    gopkg.in/yaml.v3            v3.0.1     Apache-2.0
```

## A defect this pass found: a UTF-8 BOM makes a file read-only

Recorded here rather than quietly fixed, because it is the fifth instance of
the pattern this document keeps warning about and the shape is more instructive
than the bug.

A compose file beginning with a UTF-8 BOM **resolves correctly** — `resolve`
and `explain` return the right values with the right line and column — and then
**cannot be edited**. `preview` and `apply` both answer:

```
composure: edit: operation 0: path services.web.image not found
```

Isolated across four permutations of the same three-line file:

| File | `preview` |
| --- | --- |
| plain LF | ok, two-line diff |
| CRLF | ok, two-line diff, CRLF preserved |
| **BOM** | **path not found** |
| **BOM + CRLF** | **path not found** |

So CRLF is fine end to end; the BOM alone is the fault. The cause is that the
two halves parse with different parsers. Resolution uses `yaml.v3`, which
strips the BOM. `strategy.locate` — which is what every edit operation uses to
find a byte range — parses with **goccy**, which does not, so the first key's
token is `"﻿services"` and every path lookup misses at the first segment.

Note what is good about it and what is not. It is **not** silent corruption:
the engine refuses, names the operation, and writes nothing, which is AD-8
doing its job. But it is a false refusal, and a false refusal on a whole class
of Windows-authored files means Composure is a read-only tool for anyone whose
editor writes a BOM — without ever saying so.

It also points at unfinished business this document already flagged. "Next
steps" item 1 above, written in August, was *"replace goccy with `yaml.v3`
parsing plus the existing offset conversion"*. That has not been done, and this
is the first user-visible consequence of it. The fix is the one already
specified, not a BOM special case.

Tracked as deferred work. It needs a fixture in `testdata/edge/` as a permanent
regression file, which is the project's own rule for any new failure mode.

## What is still not measured

Honest gaps, so nobody reads the tables above as broader than they are.

- **Corpus scale.** Still 146 compose files and 180 Dockerfiles. Q1 wants
  ≥99% identity on 5,000+. 98.63% on 146 is not that claim, and the two should
  not be conflated.
- **`internal/resolve` has no gated metric.** Every engine in this repository
  has one except the newest. Provenance coverage and corpus resolve rate are
  exactly the kind of number that degrades silently. The differential is the
  natural candidate and is unbaselined for the good reason given above; a
  daemon-free resolve-rate metric is not, and should exist.
- **No contrast ratio is measured**, only pairings refused. See Test 8.
- **Nothing here measures the extension in a real editor.** Every number is
  from `node --test` or `go test`. Chromium's paint of ~4,600 node elements is
  a manual check in `TESTING.md`.
- **`hclwrite` against a Terraform corpus** — still item 4 of the August next
  steps, still not run, and phase 8 is still gated on it.
