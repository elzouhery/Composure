<!--
Three of this repository's CI jobs fail on things a PR description controls
rather than on code. They are listed below so the failure arrives here, before
you push, rather than as a red check afterwards.
-->

## What this changes, and why

<!-- The reasoning, not the diff. The diff is already in the PR. -->

## How it was verified

<!--
`make check` is the local gate: fmt, build, vet, test, licence, gate.
`make gate` needs the corpus — `make corpus` first on a fresh clone, ~2.1GB.
Say what you actually ran. "Should be fine" is not a verification.
-->

- [ ] `make check` passes
- [ ] Every new failure mode found is a permanent fixture in `testdata/adversarial/` or `testdata/edge/`

## Sign-off

- [ ] Every commit carries `Signed-off-by:` (`git commit -s`)

<!--
The `DCO sign-off` job checks every non-merge commit and reports all failures
in one run. To fix commits already pushed:  git rebase --signoff <base>
See CONTRIBUTING.md for what the trailer certifies.
-->

## If this touches `benchmarks/baseline.json`

The `Baseline guard` job fails unless this description contains a line starting
with `BASELINE-CHANGE:`. Delete this section if the file is untouched.

<!--
  BASELINE-CHANGE: <what moved, in which direction, and why>

A failing fidelity test is never fixed by changing the threshold. If a number
goes DOWN, say what capability was traded away and what makes that trade
correct. "To make CI pass" is not an answer.
-->

## Clean room

By opening this pull request you confirm you have not read the source of
Dockhand (`github.com/Finsys/dockhand`) or of any BSL, SSPL, Elastic-licensed
or AGPL project in the course of writing it. See `CLEANROOM.md` — this one is
not a style rule; it is the rule that ends the project if broken.
