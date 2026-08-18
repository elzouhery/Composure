# Composure — project constraints

Read this before writing any code. These are not preferences.

**This is a brownfield repository.** `internal/` and `cmd/` hold proven, measured
code, and requirements R1–R9 are settled. Do not re-derive an architecture that
already exists and is measured: [DECISIONS.md](DECISIONS.md) records what was
decided and why, and [RESULTS.md](RESULTS.md) the evidence behind it. Read both
before proposing that either is wrong.

Operational rules shared with every harness live in [AGENTS.md](AGENTS.md).

---

## The one rule that matters

> **Never parse into a model and serialise back. Ever.**

Editing works by locating the byte range of the thing being changed and patching
the original buffer. Unchanged bytes stay unchanged *by construction*.

This is counterintuitive and it is the opposite of what almost every code
example on the internet does. The instinct — `yaml.Unmarshal` → modify struct →
`yaml.Marshal` — is measured, and it is wrong:

| Approach | Byte-identity on 146 real compose files |
|---|---|
| `yaml.v3` parse → re-encode | **19.86%** |
| `goccy` parse → re-render | **58.22%** |
| **splice bytes in place** | **98.63%**, zero files damaged |

Changing one image tag in Sentry's compose file rewrites **332 lines** under the
re-emit approach and **2 lines** under splice.

If you find yourself writing `yaml.Marshal`, `yaml.Dump`, `f.String()` on an AST,
or any function that regenerates a whole document — stop. You are about to undo
the only thing that makes this product different from four dead competitors.

The engines in `internal/strategy` and `internal/dockerfile` already implement
this correctly. Extend them. Do not replace them.

---

## Forbidden

1. **No re-serialisation.** See above.
2. **No lowering a fidelity threshold to make a test pass.** If the corpus pass
   rate drops, the change is wrong — not the test. This is the single most
   likely way for this project to fail silently.
3. **No reading Dockhand's source** (`github.com/Finsys/dockhand`) or any other
   BSL, SSPL, Elastic-licensed or AGPL project, for any reason, including
   "just to see how they did it". This is a clean-room build under Apache-2.0.
   See `CLEANROOM.md`. A licence-scan gate runs in CI.
4. **No database as a source of truth for configuration.** Files are canonical.
   SQLite is local app state only — window positions, recent projects.
5. **No `docker compose config` for resolution.** It returns a flattened
   document with no provenance, which is the entire value being added. The CLI
   is for lifecycle (`up`/`down`/`restart`/`pull`) only.
6. **No silent failure.** If an operation cannot be performed safely, return a
   typed error and refuse. An editor that emits an unparseable file is worse
   than one that says no, because the damage surfaces later in someone else's
   terminal. Convention: exported `ErrX` sentinel values, e.g. `ErrFlowStyle`.
7. **No scope creep from the out-of-scope list** in the requirements §3:
   Kubernetes, Swarm, CI/CD, multi-host agents, backups, notification channels,
   RBAC/SSO, cross-registry search, bundled AI.

---

## Definition of done — every story

A story is not done until all of these pass:

- [ ] `go build ./cmd/... ./internal/...` and `go vet ./cmd/... ./internal/...`
      clean — note the patterns, never `./...`: the fetched corpus lives inside
      the module directory and holds thousands of unrelated third-party Go files
- [ ] **`make gate` passes** — all 26 baselined metrics at or above
      `benchmarks/baseline.json`. This is the mechanical form of the four
      checks below; run it rather than eyeballing four tables
- [ ] **Corpus identity pass rate has not regressed** (`fidelity check`)
- [ ] **Every structural edit changes exactly one contiguous block** (`structbench`)
- [ ] **Every scalar edit produces a two-line diff** (`editbench`)
- [ ] Dockerfile engine metrics unchanged (`dockerbench`)
- [ ] Any new failure mode discovered is added to `testdata/adversarial/` or
      `testdata/edge/` as a permanent regression file
- [ ] No new dependency outside MIT / Apache-2.0 / BSD / ISC (`make licence`)

The benchmarks are the acceptance criteria, not a smoke test. They have already
caught four bugs that unit tests did not: a swallowed newline that welded a
comment onto a value, an invalid-YAML insert on flow mappings, a CRLF line
ending silently rewritten to LF, and a UTF-8 BOM that made a file report zero
stages while every operation failed without error.

Note the shape of those bugs. **This engine does not crash — it returns a
confident wrong answer.** Test accordingly.

---

## Architecture invariants

- **Go core, in-process.** The UI is a view. Every capability is exposed and
  tested at the core boundary before any UI is written against it.
- **Core must be usable three ways**: as a library, as a headless CLI, and later
  as an MCP server. If a feature only works from the GUI, it is built wrong.
- **CLI before UI, always.** The corpus harness can only exercise headless code,
  and corpus-scale testing is what makes this engine trustworthy.
- **Parse with `yaml.v3`, splice with offsets derived from its `Line`/`Column`.**
  Measured: `yaml.v3` had zero parse errors across 146 files; goccy rejected 2
  valid ones. Offset conversion is ~9 lines (`offsetOf`).
- **The Dockerfile engine is separate from the YAML engine.** Different grammar.
  Do not attempt to unify them.

---

## Provenance is a day-one structural requirement

Requirement R1.8: every resolved value carries `{file, line, column, merge step}`.

This **cannot be retrofitted.** If the multi-file merge is built without it, the
merge gets rewritten later — not extended. Design the merge to carry provenance
from the first commit, even in stories that do not yet display it.

Provenance is what lets the product answer *"why is this service getting that
port"*, which is the question the entire phase-1 wedge exists to answer.

---

## Repository facts

- Module path: `github.com/elzouhery/composure`
- Licence: Apache-2.0. Contributors sign a CLA/DCO with relicensing rights.
- The corpus (`corpus-repos/`) is **fetched, not committed** — 2.3GB of shallow
  clones. `fidelity fetch corpus-repos` rebuilds it. It is gitignored.
- `RESULTS.md` holds the measured evidence behind every architectural claim
  here. If you want to challenge a decision, re-run the benchmark first.

## Commands

```bash
make corpus       # rebuild the corpus (~2 min, ~2.1GB of shallow clones)
make gate         # THE GATE — all five benchmarks vs the committed baseline
make check        # build, vet, test, licence, gate — everything CI runs
make bench        # the five benchmarks as human-readable tables
make licence      # CLEANROOM.md rule 5: no BSL / SSPL / Elastic / AGPL

go run ./cmd/hubsearch stale <Dockerfile>
```

Individually, when you need one table rather than the gate:

```bash
make fidelity     # identity pass rate + defect breakdown
make editbench    # scalar edit diff size
make structbench  # insert/delete collateral damage
make dockerbench  # Dockerfile engine
```

Each benchmark also takes `-json` before the corpus path; that is what
`cmd/gate` consumes, so reformatting a table cannot move a gate.

If `make gate` fails, the change is wrong — **not the baseline**. Editing
`benchmarks/baseline.json` does not restore the property it records, it only
stops CI from reporting that the property is gone. CI blocks any pull request
that changes it without a `BASELINE-CHANGE:` justification.
