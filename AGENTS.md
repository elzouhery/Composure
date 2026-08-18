## Composure

A local workbench for creating, understanding and safely editing container and
infrastructure configuration. Go core plus a VS Code extension — this repository
is the fidelity engine, its measurement harness and the extension that ships on
top of them. Requirements R1–R9 are settled; this repository holds only what
ships.

## Policy

- Never lower a number in `benchmarks/baseline.json` to make a build pass — fix
  the change that caused the regression, or argue the move in the PR body with
  a `BASELINE-CHANGE:` line.
- Never read the source of Dockhand (`github.com/Finsys/dockhand`) or any BSL,
  SSPL, Elastic-licensed or AGPL project, for any reason. Clean-room build —
  work from `CLEANROOM.md` and the requirements, never from a reference
  implementation.
- Never add a dependency outside MIT / Apache-2.0 / BSD / ISC; `make licence`
  fails the build on the rest.

## Where things are

- Engine contract and the forbidden list: `CLAUDE.md`
- Design decisions, and the provenance of each: `DECISIONS.md`
- Measured evidence behind every architectural claim: `RESULTS.md`
- Clean-room protocol and IP governance: `CLEANROOM.md`
- This is a brownfield repository. `internal/` and `cmd/` are proven and
  measured; the standing failure mode is an agent redesigning what already
  works. Read `DECISIONS.md` before proposing that it is wrong.

## Running and verifying

- `make gate` is the acceptance check, not `go test`. Only `internal/hub` and
  `cmd/licencescan` carry tests; a green `go test` says almost nothing.
- Run `make corpus` before `make gate` on a fresh clone — the corpus is fetched,
  never committed, and takes ~2 minutes for ~2.1GB of shallow clones.
- Build and vet with `./cmd/... ./internal/...`, never `./...` — `corpus-repos/`
  sits inside the module and contains thousands of unrelated Go files, two of
  them with a duplicate `main`.
- Benchmark flags go **before** the corpus path (`-json corpus-repos`). Flag
  parsing stops at the first positional argument, so `corpus-repos -json`
  silently ignores the flag and prints a table.

## Conventions that differ from defaults

- Never parse into a model and serialise back — locate the byte range and patch
  the buffer. Re-emitting scores 19.86% byte-identity against splice's 98.63%.
  Extend `internal/strategy` and `internal/dockerfile`; do not replace them.
- Refuse rather than corrupt. An operation that cannot be performed safely
  returns an exported `ErrX` sentinel (`ErrFlowStyle`) — never a best effort,
  never a silent no-op.
- Every resolved value carries `{file, line, column, merge step}` from the first
  commit that builds the merge (R1.8). It cannot be retrofitted; a merge built
  without it gets rewritten rather than extended.

## Known pitfalls

- This engine does not crash, it returns a confident wrong answer. Test for
  wrong bytes, not for exceptions — the four bugs the corpus caught and unit
  tests missed all passed cleanly while producing damaged output.
- Every new failure mode found becomes a permanent file in
  `testdata/adversarial/` or `testdata/edge/`. The corpus is the regression
  suite.
