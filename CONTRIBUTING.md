# Contributing to Composure

Read [CLAUDE.md](CLAUDE.md) before writing code and [CLEANROOM.md](CLEANROOM.md)
before reading anything. Both are constraints, not preferences.

---

## 1. Sign off every commit (DCO)

Every commit must carry a `Signed-off-by` trailer matching the commit author:

```
Signed-off-by: Jane Doe <jane@example.com>
```

`git commit -s` adds it from your `user.name` and `user.email`. The
`DCO sign-off` job in [.github/workflows/ci.yml](.github/workflows/ci.yml)
fails any pull request with a commit that lacks one, because a rule nothing
checks is not a rule: prose is the weakest form of a constraint.

The trailer means you certify the
[Developer Certificate of Origin 1.1](https://developercertificate.org/): you
wrote the contribution or have the right to submit it, and it is contributed
under this project's licence, Apache-2.0.

**What the DCO does not do.** It certifies origin. It does *not* grant the
project the right to relicense your contribution. Requirements section 10 and
[CLEANROOM.md](CLEANROOM.md) rule 6 ask for "CLA or DCO **with relicensing
rights**"; a DCO alone satisfies the first half and not the second. The second
half needs a signed CLA, and a CLA needs a legal entity to be the counterparty
— which does not exist yet (see `LICENSE`, the note on the copyright holder).
So this is deliberately partial, and stated as partial rather than left to be
discovered. Until a CLA is in place, the project cannot relicense contributed
code without asking every contributor.

## 2. Clean room — the rule that ends the project if broken

**Do not read the source of Dockhand (`github.com/Finsys/dockhand`) or of any
BSL, SSPL, Elastic-licensed or AGPL project. For any reason. Including "just to
see how they did it."**

BSL 1.1 termination is automatic, total and retroactive across every version.
Dockhand is a *requirements* reference — behaviour observed from the outside —
and never an implementation input. If a design decision was informed by watching
a competitor's behaviour, say so explicitly in [DECISIONS.md](DECISIONS.md);
that is [CLEANROOM.md](CLEANROOM.md) rule 4.

Dependencies must be MIT, Apache-2.0, BSD or ISC. `make licence`
(`cmd/licencescan`) scans the Go build graph and fails the build on anything
else; it runs as the `Licence scan` CI job. It walks Go modules only — it cannot
see `node_modules`, which is why the extension carries zero runtime npm
dependencies and a test asserts it.

## 3. Definition of done

A change is not done because it compiles. From [CLAUDE.md](CLAUDE.md):

- `go build ./cmd/... ./internal/...` and `go vet ./cmd/... ./internal/...`
  clean. **Note the patterns — never `./...`**: the fetched corpus lives inside
  the module directory and holds thousands of unrelated third-party Go files.
- **`make gate` passes** — every metric at or above `benchmarks/baseline.json`.
  Run `make corpus` first on a fresh clone; the corpus is fetched, not
  committed.
- Any new failure mode you discover becomes a permanent regression file in
  `testdata/adversarial/` or `testdata/edge/`.
- No new dependency outside MIT / Apache-2.0 / BSD / ISC (`make licence`).

The benchmarks are the acceptance criteria, not a smoke test. This engine does
not crash — it returns a confident wrong answer, so test for wrong bytes rather
than for exceptions.

## 4. Never lower a threshold to make a build pass

If `make gate` fails, the change is wrong — not the baseline. Editing
`benchmarks/baseline.json` does not restore the property it records; it only
stops CI from reporting that the property is gone. The `Baseline guard` CI job
fails any pull request that edits that file without a line in the PR body
starting `BASELINE-CHANGE:` saying what moved, in which direction, and why. "To
make CI pass" is not an answer.

The one rule underneath all of it: **never parse into a model and serialise
back**. Locate the byte range and patch the buffer. Re-emitting scores 19.86%
byte-identity where splice scores 98.63%.

## 5. Pull requests

- Branch off `main`; do not commit to `main` directly.
- One concern per pull request. The gate output is the review evidence.
- Say what you measured. If you changed the engine, the pass rate before and
  after belongs in the description.
