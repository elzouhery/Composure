# Decisions

Required by [CLEANROOM.md](CLEANROOM.md) rule 4: *log the provenance of every
non-trivial design decision — what was decided, why, and what informed it. If a
decision was informed by observing a competitor's **behaviour** (not its code),
say so explicitly.*

That last clause is the reason this file has a specific shape. This is a
clean-room build. Dockhand (BSL 1.1) is a **requirements reference only**, and
its source has never been read by anyone who has written code here. Observing
what a shipped product *does* — running it, watching it mangle a file — is
legitimate and is how several decisions below were reached. Reading how it does
it is not, and would terminate our rights to every version of it retroactively.
The distinction is invisible in a diff, so it is recorded here instead.

Decisions marked **[competitor behaviour]** were informed by using or observing
a competing product, never by reading its source.

This file was written on **2026-08-12**, after phases 1 and 2 were built, and
is therefore partly a reconstruction from the specs, the architecture spine and
the state of the tree rather than a contemporaneous log. Where it reconstructs,
it names the story that made the change. From here it is appended to as
decisions are made.

**This is not the requirements register.** D1–D11 are settled in the
requirements register, §2, each with its reason. This file covers decisions taken *since*, plus the
provenance §2 does not carry.

---

## Contents

- [1. Splice, never re-emit (AD-1)](#1-splice-never-re-emit-ad-1)
- [2. The shell is a VS Code extension, not a desktop app (D5)](#2-the-shell-is-a-vs-code-extension-not-a-desktop-app-d5)
- [3. Provenance by construction (AD-3)](#3-provenance-by-construction-ad-3)
- [4. Merge semantics are a table, not branches (AD-4)](#4-merge-semantics-are-a-table-not-branches-ad-4)
- [5. Refuse rather than corrupt (AD-8)](#5-refuse-rather-than-corrupt-ad-8)
- [6. No `docker compose config` in the resolution path (AD-10)](#6-no-docker-compose-config-in-the-resolution-path-ad-10)
- [7. One `Path` type, owned by `resolve` (AD-14)](#7-one-path-type-owned-by-resolve-ad-14)
- [8. Profile filtering belongs to `topology` (AD-16)](#8-profile-filtering-belongs-to-topology-ad-16)
- [9. The webview owns no state the core does not (AD-19)](#9-the-webview-owns-no-state-the-core-does-not-ad-19)
- [10. The unset-key list is generated from a vendored schema (AD-20)](#10-the-unset-key-list-is-generated-from-a-vendored-schema-ad-20)
- [11. Compatibility is judged against the installed binary (AD-21)](#11-compatibility-is-judged-against-the-installed-binary-ad-21)
- [12. The alias-expansion reversal, stories 1.1 → 1.2](#12-the-alias-expansion-reversal-stories-11--12)
- [13. The credential rule matches key names only](#13-the-credential-rule-matches-key-names-only)
- [14. The differential is not baselined into the gate](#14-the-differential-is-not-baselined-into-the-gate)
- [15. Platform-specific VSIXes rather than one fat package](#15-platform-specific-vsixes-rather-than-one-fat-package)
- [16. Zero runtime npm dependencies](#16-zero-runtime-npm-dependencies)
- [17. Clicking an unset key opens a field and stages nothing](#17-clicking-an-unset-key-opens-a-field-and-stages-nothing)
- [18. A bind mount is a marker, not an edge](#18-a-bind-mount-is-a-marker-not-an-edge)
- [19. Profiles filter services, never the resource inventory](#19-profiles-filter-services-never-the-resource-inventory)
- [20. Structural insert moves out of phase 5; scaffolds and structural delete stay](#20-structural-insert-moves-out-of-phase-5-scaffolds-and-structural-delete-stay)
- [21. Editing an inherited value writes it on the service, never on the anchor](#21-editing-an-inherited-value-writes-it-on-the-service-never-on-the-anchor)
- [22. Image discovery moves out of phase 4; the network is a state, never an assumption](#22-image-discovery-moves-out-of-phase-4-the-network-is-a-state-never-an-assumption)
- [23. A comment is addressed by (path, position), and a run of them is one thing](#23-a-comment-is-addressed-by-path-position-and-a-run-of-them-is-one-thing)
- [24. A list entry is addressed by index, and a moved index is refused rather than rebased](#24-a-list-entry-is-addressed-by-index-and-a-moved-index-is-refused-rather-than-rebased)
- [25. Moving a value into a variable writes two files, in an order chosen so the only reachable partial state is inert](#25-moving-a-value-into-a-variable-writes-two-files-in-an-order-chosen-so-the-only-reachable-partial-state-is-inert)

---

## 1. Splice, never re-emit (AD-1)

**Decided.** No function may regenerate a whole document. An edit locates the
byte range of the thing being changed and patches the original buffer.
Unchanged bytes are unchanged by construction.

**Why.** It is the only approach that makes fidelity a property that cannot be
lost rather than a feature someone has to maintain.

**What informed it.** A measurement, not a preference. Three candidate engines
were built and scored against 146 real compose files before any product code
was written — [RESULTS.md](RESULTS.md), Tests 1 and 2:

| Strategy | Byte-identical | Files damaged | Worst single-tag edit |
| --- | ---: | ---: | ---: |
| `yaml.v3` re-emit | 19.86% | 117 | 332 lines |
| `goccy` re-emit | 58.22% | 59 | 22 lines |
| splice | **98.63%** | **0** | **2 lines** |

**[competitor behaviour]** The spike was commissioned because the closest
living competitor ships a visual editor that damages the file on contact —
observed by using it, and recorded in the requirements as *"the closest living
competitor ships a visual editor that silently destroys your file the moment
you touch it"* (§1). That observation set the bar; it did not suggest the
implementation, which came from measuring our own three candidates.

**Consequences accepted.** The engine parses twice — `yaml.v3` for the model,
and a separate locate pass for byte ranges. See decision 12's consequence note
and the BOM defect in RESULTS.md for what that has cost.

---

## 2. The shell is a VS Code extension, not a desktop app (D5)

**Decided 2026-08-11**, superseding the original D5 (a Wails desktop app). The
product ships as a VS Code extension; the Go core ships inside it as a platform
binary and is spawned as a subprocess over JSON-RPC on stdio.

**Why.** Three reasons, in the order they mattered:

1. **Distribution.** The marketplace is real discovery. A standalone app starts
   at zero and stays there.
2. **Proximity.** People edit compose files in their editor and will not
   alt-tab to change a port.
3. **Inheritance.** The text editor, diff viewer, git gutter, undo, file tree
   and problems panel all arrive free and better than we would build them.

**What informed it.** The category's own graveyard, which is a matter of public
record rather than observation of any one product: Panamax (2016), Kitematic
(2025) and DockStation (2025) were **all standalone apps**. Three for three is
not a coincidence worth betting against a fourth time.

**What it cost, and what was checked first.** This is the largest reversal in
the project, so the blast radius was established before it was taken: the
ports-and-adapters paradigm meant the shell is one adapter. Nothing in the
architecture spine moved except the adapter — which is the point of the
paradigm, and is stated as such in the architecture spine.
Cursor and Windsurf come free on the same extension API. JetBrains and vim do
not, and are deferred.

**[competitor behaviour]** The move also changed who the competitors are, and
this was reasoned about explicitly rather than discovered later. The adversarial
research in the requirements analysed the *standalone* category; VS Code is a
different market with two free incumbents already in it — **Docker DX** and
**Docker Compose Visualizer**. Both were used, and the finding was that neither
writes back to the file and neither shows what you have *not* declared. That is
where the wedge now sits, and it is why the release gate moved to the end of
phase 2 (see decision 10 and requirements §9). The risk register rates this
High and explicitly does *not* claim it is mitigated.

---

## 3. Provenance by construction (AD-3)

**Decided.** The resolved model contains no bare Go scalars. Every leaf is a
wrapper carrying `{file, line, column, mergeStep}`. `map[string]any` does not
appear in the model's type graph.

**Why.** So that a value which reached the model without provenance is
*unrepresentable*, rather than merely discouraged.

**What informed it.** A sequencing argument, and it is the single most
expensive thing on the roadmap to get wrong. R1.8 cannot be retrofitted: a
multi-file merge built without provenance gets **rewritten** later, not
extended. So provenance belongs in story *one* of the resolver, not in the
story that displays it — and stories 1.1 and 1.2 carried it before anything
rendered it.

**[competitor behaviour]** What provenance is *for* was sharpened by observing
Dockhand's visual layer: it receives a single file as a string and resolves
none of this — `${VAR}` renders literally into its node labels. Watching that
made the question "why is this service getting that port" concrete, and that
question is the whole phase-1 wedge.

---

## 4. Merge semantics are a table, not branches (AD-4)

**Decided.** Per-key merge behaviour is declared in one table keyed by config
path — `replace`, `append`, `appendUnique(key)` — and a generic walker reads
it. Adding a key means adding a row.

**Why.** The alternative is `ports` and `volumes` each receiving their own
implementation of the same uniqueness rule, and the two drifting.

**What informed it.** Compose specification §13 is itself written as a table.
Keeping ours in the same shape means the two are diffable **by inspection**,
which is the only review that will actually happen. Verified by decision 14's
differential: 80 of 80 projects agree with `docker compose config`, including
6 of 6 multi-file projects — the merge itself.

That qualifier is load-bearing and was earned the hard way. The harness
originally compared every project as **one file against itself**: it resolved
with `resolve.File` and handed the oracle a single `-f`, so it never exercised
an override file, a `-f` chain or multi-file provenance — exactly what it
existed to prove. The first number it produced was true and meant far less than
it was reported to mean.

---

## 5. Refuse rather than corrupt (AD-8, D10)

**Decided.** An operation that cannot be performed safely returns an exported
sentinel (`ErrFlowStyle` is the pattern), names the file and path, and changes
nothing. Never a partial write, never a silent no-op.

**Why.** An editor that emits an unparseable file is worse than one that says
no, because the damage surfaces later, in someone else's terminal, detached
from the action that caused it.

**What informed it.** A bug in our own first implementation, found by the
harness and not by unit tests. Structural insert into a flow-style mapping —
`compact: {image: nginx, restart: always}` — produced invalid YAML. The corpus
did not catch it, because none of the 146 real files uses flow-style services;
a hand-written adversarial file did, confirmed against a third-party parser.
RESULTS.md records the conclusion drawn at the time: *"refusing is the correct
behaviour and it should be a design principle, not an apology."*

**Reaffirmed 2026-08-12.** The UTF-8 BOM defect (RESULTS.md, post-spike
section) is this rule working: a BOM'd file cannot be edited, and the engine
says so and writes nothing rather than guessing. The refusal is *wrong* — it is
a false refusal and a real bug — but it is safe, which is the property being
bought.

---

## 6. No `docker compose config` in the resolution path (AD-10, R1.9)

**Decided.** The `docker compose` CLI is used for lifecycle only. It may be
invoked by the *test harness* as an oracle. It may never be invoked by
`resolve`.

**Why.** `docker compose config` returns a flattened document with no
provenance. Resolving through it would mean reimplementing the product's entire
differentiator as a shell-out that discards it.

**What informed it.** The requirement is explicit (R1.9), and the temptation is
real enough that the rule is enforced mechanically rather than by prose:
`internal/resolve`'s own test suite asserts that the package never shells out,
and the CLI is invoked from exactly one place in the codebase,
`cmd/differential`.

---

## 7. One `Path` type, owned by `resolve` (AD-14)

**Decided.** `Path` is a defined slice type in `resolve`, with one canonical
string form (`services.web.ports[0]`) and one parser back.
`strategy.Locate` takes it directly. No package builds a path by string
concatenation.

**Why.** The join key was about to exist in at least three incompatible
spellings — `services.web.ports[0]`, `services/web/ports/0`, and the splice
engine's pre-existing `[]string{"services","web"}`. With three spellings a
finding cannot be matched to a node, or a fix to a byte range.

**What informed it.** Noticing during architecture that the built engine
already had its own spelling and the new packages were each about to invent
one. The mechanism chosen is worth recording: `strategy.Locate` takes
`[]string`, not a named type, precisely so `resolve.Path` is assignable to it
and `strategy` can stay a **leaf package** with no import of the resolver.
Keeping it a leaf is what lets the corpus harness exercise the engine at
5,000-file scale without dragging the resolver in.

---

## 8. Profile filtering belongs to `topology`, not `resolve` (AD-16)

**Decided.** `resolve` produces the complete model with each service's profile
membership annotated, and is a pure function of the file set alone. `topology`
takes the active profile set as an explicit argument.

**Why.** R1.4 has two plausible readings — a resolved model already filtered by
profile, and a topology that filters again — and building both double-filters
and makes toggling a profile require a full re-resolve.

**What informed it.** Reading R1.4 as a UI requirement rather than a data one:
"let the user toggle profiles and watch the topology change" is a statement
about latency. Confirmed by measurement afterwards (RESULTS.md Test 7): a
toggle recomputes the graph in about a millisecond on 500 services, so no cache
is needed and none exists.

---

## 9. The webview owns no state the core does not (AD-19)

**Decided.** Webview state is exactly two things: **view state** (node
positions, pane split, selection), persisted per workspace and never entering a
file; and **staged edits**, a list of operations against byte ranges held until
an explicit write. Everything else is re-fetched from the core.

**Why.** R2.6 forbids a canvas that is a separate model. Without this rule it
arrives anyway, through the back door, as a cache.

**What informed it.** The corollary is the load-bearing part and it came from
the fidelity work: **a staged edit whose byte range has moved because the file
changed on disk is discarded, never rebased.** Writing a stale range is
precisely how a fidelity engine damages a file — it would splice correct bytes
into the wrong place, producing a plausible file that is wrong, which is this
engine's characteristic failure mode. `edit.ErrStaleRange` compares the *text*
at the range as well as the offsets, because a same-length change at the same
offset moves nothing and still means the reader is editing something else.

---

## 10. The unset-key list is generated from a vendored schema (AD-20)

**Decided.** `available, not set` is derived at runtime from
`schema/compose-spec.json`, vendored from the compose-spec project at a pinned
commit, minus what the resolved model declares. **No list of Compose keys
exists anywhere in the extension or the core.** Adding support for a new key
means bumping the vendored schema.

**Why.** This list is the product's differentiator. A hand-maintained list of
"properties you could add" falls behind the specification within one release
and then confidently tells the reader that a key which exists does not — the
differentiator rotting into a liability.

**Alternatives rejected, and why:**

- **A hand-written list.** Rots, as above.
- **Fetching the schema at runtime.** Would make the inspector's contents
  depend on connectivity. Pinning means the list is reproducible and its age is
  a fact anyone can read off `schema/PROVENANCE.md`.
- **Version-keyed schemas selected by the file's `version:` field.** Rejected
  hardest. The Compose Specification deliberately carries no version and is one
  current unified document; Compose itself ignores the field and warns about
  it. Filtering on it would *hide keys that work perfectly*. The field is
  instead reported as a hint by a diagnostic rule, on the reasoning that a
  field which is ignored should be deletable rather than mysterious.

**Licensing note.** The schema is Apache-2.0, which CLEANROOM.md rule 5
permits. But `make licence` walks the **Go build graph**, and a vendored data
file is not a module — the scan cannot see it. `schema/PROVENANCE.md` records
the upstream commit, the retrieval date, the byte count and the licence, and is
therefore what makes these bytes auditable. That gap is stated rather than
assumed closed.

**[competitor behaviour]** This element exists because of a comparison made
while using the incumbents. The design log records it as *"the single most
differentiating element in the design… Neither Docker DX nor Docker Compose
Visualizer has an equivalent. It is derived from the schema, so it is cheap."*
Separately, the requirements record that Dockhand's visual editor covers
roughly 15% of the Compose spec, with `healthcheck` and `deploy` as
display-only badges — observed by use. Both observations are of behaviour and
coverage, not of source.

---

## 11. Compatibility is judged against the installed binary (AD-21)

**Decided.** The schema is single and current. Where a key's minimum Compose
version is known, the inspector **marks** keys newer than the `docker compose`
found on the machine as unsupported-here. It never hides them. With no Compose
binary present, nothing is marked and the whole schema is offered.

**Why.** It is the live half of the question decision 10 rejected. The reader's
real constraint is not the file's obsolete `version:` field, it is the binary
that will run the stack — and marking rather than hiding teaches them both that
the key exists *and* that upgrading would give it to them.

**What informed it.** `compose-min-version.json` is **not** upstream; it is a
separate and explicitly partial record we maintain. That partiality is designed
in: a key absent from it is offered with no mark, so an incomplete record costs
an annotation and never a key. Failing open was the requirement; the data
shape follows from it.

---

## 12. The alias-expansion reversal, stories 1.1 → 1.2

The most instructive decision in the project, because it was made, reversed,
and then remade with the objections answered rather than forgotten.

**Round 1 — story 1.1 followed aliases.** The reasoning at the
time: following the alias keeps the model usable on the many real files that
use anchors, and origin points at the anchor's definition site.

**Round 2 — story 1.1's second review pass removed it.** Three parallel reviewers
found that it was scope creep past R1.6, and that it was actively wrong in
three ways:

1. It put the same values at two paths, **inflating the provenance metric** —
   the headline number of story 1.1.
2. It **discarded the alias's own position**, which is the position a user
   actually sees.
3. Repeated anchors **expanded exponentially**.

A separate review pass had already found that `x: &loop {self:
*loop}` sent the converter into unbounded recursion. That is not a recoverable
panic in Go — `recover` cannot catch stack exhaustion — so **a file on disk
could take the editor down with it.**

**Round 3 — story 1.2 reinstated expansion, answering each
objection rather than reintroducing it.** This is the part worth recording:

| Objection | How it was answered |
| --- | --- |
| Cycles kill the process | Detected by anchor identity on the expansion stack and **refused** (`ErrAliasCycle`) |
| Exponential fan-out | A shared node budget (`maxAliasNodes = 100_000`, `ErrAliasFanout`). A 40-level doubling chain that would expand 2⁴⁰ refuses in 0.02 s. Only values created *inside* an expansion count, so a large alias-free file can never trip it |
| Double counting | **Stated rather than hidden.** The corpus leaf count moves 5,017 → 7,147, because an expanded anchor genuinely does appear at every site referencing it. The number of *distinct cited positions* is unchanged |
| The alias's own position was lost | Every expanded value now carries **two** positions: `origin`, the anchor's definition site (where the bytes are), and `aliasSite`, the reference site (which answers "why does this service have a `networks:` I cannot see"). Neither is derivable from the other |

**Why the reversal was right both times.** 1.1's removal was right *for 1.1*:
the story's deliverable was a provenance metric, and alias-following was
silently corrupting it. 1.2's reinstatement was right because AD-12 requires it
— the resolved view and the file must not disagree about what exists — and by
then the three hazards had names, budgets and sentinels.

**What informed the final shape.** AD-12, and one measurement: the yaml.v3
re-emit strategy flattened merge keys in 2.1% of corpus files (RESULTS.md Test
1), which is the concrete form of "expanded for display, preserved in the
file". The visible payoff was recorded: the `webstack` example goes from 9
edges to 16.

**Process finding worth keeping.** The 1.1 spec never mentioned anchors at all
— its "never" list barred merging, `.env`, interpolation, `include`, `extends`
and profiles, but not alias-following. The silence is what allowed the scope
creep. A spec's "never" list is load-bearing, and an unlisted capability is not
thereby authorised.

**Detail decisions inside the expansion**, each recorded because each is a
trade someone will want to undo:

- The anchor node is **re-converted at each site, not shared**. Sharing one
  subtree would be cheaper in memory, but the model is walked as a tree, and a
  shared subtree makes every walk exponential even when the memory is not.
- Merge keys are detected by **tag** (`!!merge`), not by matching the text
  `<<`. A *quoted* `"<<"` is a perfectly ordinary string key and must not be
  treated as a directive.
- The merge key does not survive into the model. A `<<` key in a resolved view
  is a key no container runtime has ever heard of — while the bytes keep it
  exactly as written, because this package has no write path at all.
- Both new hazards became permanent regression files:
  `testdata/adversarial/07-merge-precedence.yml` and
  `testdata/edge/e9-recursive-anchor.yml`.

---

## 13. The credential rule matches key names only

**Decided.** R3.7 (credentials in plain `environment:` blocks) matches on
**key name only**, case-insensitively, against a closed nine-token list:
`password, passwd, pwd, secret, token, apikey, api_key, credential,
private_key`. There is **no entropy scoring and no value-shape heuristic**. The
one value-side test is a literal substring, `password=`, which identifies a
connection string.

**Why. Precision over recall, explicitly.** Entropy scoring and value-shape
guessing are what generate the false positives that get a rule switched off. A
rule that flags six things per file with five of them wrong is disabled within
a week and never re-enabled — at which point its recall is zero, not high.

**The consequence is accepted and tested, not tolerated.** An oddly named
credential such as `PolicyServer:Host:Identity:Admins:0:Pw: s3cret` slips
through, and there is a test named
`TestCredentialOddlyNamedKeyIsMissedOnPurpose` asserting that it produces **no
findings**. Recording a deliberate false negative as a passing test is the only
way the decision survives the next person who reads the rule and thinks it is
incomplete.

**Two exemptions, each a decision rather than an oversight:**

- **A value containing a variable reference** is exempt. `${DB_PASSWORD}` means
  the secret is *not in the file*, which is the thing being worried about.
- **A key ending `_FILE`** is exempt. `POSTGRES_PASSWORD_FILE` holds a path to
  a secret, not a secret, and it is the convention Docker itself documents for
  doing this right. Flagging it would punish the correct answer.

**One implementation detail that is a decision.** The rule reads the **raw**
value as written in the file, never the interpolated one. Reading the
interpolated value would report `POSTGRES_PASSWORD: ${DB_PASSWORD}` as a
plaintext credential on any machine where `DB_PASSWORD` happens to be exported
— a finding about the reader's shell, not about the file.

**Severity is `hint`, always.** It is advice, never a gate, and it never blocks
a save. The value is never printed, in the finding or anywhere else; the tests
assert that the secret does not appear in the message.

---

## 14. The differential is not baselined into the gate

**Decided.** `cmd/differential` compares the resolved model against
`docker compose config` across the corpus. It is run by `make differential`,
and is deliberately **not** in `make gate` and **not** in
`benchmarks/baseline.json`.

**Why.** Two reasons:

1. **It needs a Docker daemon and skips cleanly without one.** A baselined
   metric that silently skips is a gate that *passes vacuously*, which is worse
   than no gate — it reports a property nobody measured.
2. It takes minutes, because it forks the CLI once per project.

**What informed it.** The value is not in question: it agrees on 74 of 74
projects with zero divergences, and it earned its keep on first run by finding
a real bug that no unit test could have — `ParseDotEnv` took the whole line
after `=`, so `URL=host.com # free examples` handed the trailing comment to the
container. It is exactly the check that catches a merge rule which is subtly
wrong rather than obviously broken, and unit tests cannot catch that class,
because they are written from the same reading of the specification the code
was.

**This was a deviation from the written acceptance criteria; the criteria were
amended on 2026-08-12 to match it.** Story 1.5 used to say the rate *is* added
to `benchmarks/baseline.json`, that regressions fail `make gate`, and that
absent Docker "skips the check with a clear message rather than failing the
build". None of the three was true of the shipped harness, and the third was
backwards: a missing oracle is loud by default. `cmd/differential/main.go:99–118`
prints `NOTHING WAS COMPARED` and exits `exitSkipped` (`= 3`, at `:181`);
`-allow-skip` (`:83`) is the opt-in that turns that into an exit 0, and
`make differential` does not pass it. One loose end remains and is left rather
than papered over: the `Makefile`'s `gate` comment (`:176–182`) attributes the
refusal to a story condition, *"wire it into the gate ONLY if it is stable"*,
which has never appeared in `epics.md` — the two reasons it gives immediately
after are the real ones and stand on their own. The Makefile is owned elsewhere;
that quotation should go. The two reasons stand on their own merits without
it.
**Baseline it when CI has a daemon** — that action is still open, and until then
the harness gates itself on floors the target passes (`-min-multifile`,
`-min-pass`, `-min-multifile-pass`) rather than on a committed baseline.

**A related decision inside the harness.** It compares a **projection** of the
model, not the whole document. `docker compose config` does not emit the merged
document, it emits Compose's canonical model; comparing whole documents would
measure how faithfully we reproduce Compose's *canonicalisation* — which nobody
asked for — and would bury a real merge divergence under a thousand cosmetic
ones. The projection is deliberately readable in one function, because a field
not in it is a field not being checked, and that has to be visible.

**Known divergence risk, flagged rather than discovered later.** Treating
`env_file` as an interpolation source is R1.5's requirement and is *not* what
Compose does. It is the likeliest place this engine and the CLI can disagree.

---

## 15. Platform-specific VSIXes rather than one fat package

**Decided 2026-08-12.** `make package` produces seven platform-specific VSIXes
via `vsce --target`, each carrying exactly one core binary, rather than one
package carrying all five.

**Why.** A single package would be about 20 MB, of which every install
downloads roughly 16 MB of binaries it can never execute.

**What informed it.** Checking that `vsce` supports it before designing around
it — it does, via `--target`, and it stamps `TargetPlatform` into the manifest
so the marketplace serves the right one. Two mechanical details are decisions
in their own right:

- **The per-target ignore files are generated, not committed.** Five
  near-identical `.vscodeignore` files differing by one line is a set that
  drifts, and the one that drifts silently ships someone else's binary or none
  at all. They are generated from the single committed `.vscodeignore` by the
  `package` target.
- **`CGO_ENABLED=0`** is load-bearing rather than tidy. Static binaries are
  what let one Linux build serve glibc and musl alike — the two `alpine-*`
  targets carry the Linux binaries unchanged — and what makes cross-compilation
  work without a C toolchain per target.
- **The `bin/<goos>-<goarch>/` directory names are a contract**, not a
  convention: `goTarget()` in `host/core.ts` derives that exact spelling from
  `process.platform`/`process.arch`. A rename on either side is a "no core for
  this platform" banner on a stranger's machine, with green builds and green
  tests on ours. `host/packaging.test.ts` parses the Makefile's `CORE_TARGETS`
  and asserts the two agree, in both directions.

---

## 16. Zero runtime npm dependencies

**Decided.** The extension's `dependencies` are empty, and stay empty. Build
tooling (`esbuild`, `typescript`, `@vscode/vsce`) is `devDependencies` only.

**Why. This is a licensing constraint, not a taste about bundle size.**
`make licence` — the mechanical enforcement of CLEANROOM.md rule 5, which bars
BSL, SSPL, Elastic and AGPL anywhere in the tree — walks the **Go build graph**
and cannot see `node_modules`. It therefore cannot tell anyone that a
transitive npm dependency arrived under AGPL. The only tree size that gate can
vouch for is zero.

**What informed it.** Noticing that the rule and its enforcement had different
scopes. Until 2026-08-12 the constraint existed only as a comment in the
Makefile; it is now asserted by `host/packaging.test.ts`, because a constraint
that depends on everyone remembering it is not a constraint.

**Consequence accepted.** Anything the webview needs is written here or
bundled. There is no graph library; the layout, the routing and the collapse
are ours, in `webview/layout.ts` and `webview/view.ts`.

---

## 17. Clicking an unset key opens a field and stages nothing

**Decided 2026-08-12.** In the inspector's `available, not set` block, clicking
a key **opens an input for it in place with the caret in it, and stages
nothing.** The reader presses Enter to stage. The click sends `open`, not
`stage` (`extension/webview/inspector.ts:1158`); the host records the path as
opened and re-inspects (`extension/host/panel.ts:745–756`, the set at `:127`);
the pane renders the field through `unsetValue` (`inspector.ts:702`) and
`restoreFocus` puts the caret in it (`inspector.ts:483`). Enter is what reaches
`stageValue` (`host/panel.ts:843`) and the pending diff. A key that is a mapping
opens **its own sub-keys** instead of a text field, fetched from the core at
that path (`host/panel.ts:631 expandOpenMappings`).

**Why. The gesture the spec asked for is not merely risky, it is
unsatisfiable.** Story 5.2 and EXPERIENCE.md both said the click stages *the
key's default*. Measured against the shipped binary, that default does not
exist:

- A service declaring only `image:` reports **93 schema fields, 92 of them
  unset, and zero of the 92 carrying a default of any kind** —
  `composure schema -json -at services.only`, every `default` field absent. Every
  service in `examples/webstack/compose.yaml` gives the same answer (87–91
  unset, 0 with a default).
- The only place a service-level default exists at all is `healthcheck.*`, and
  **every one of them is prose lifted from the specification's description
  text**, not a value: `composure schema -json -at services.web.healthcheck`
  reports `start_interval` with `default: "interval value"` and
  `default_source: "description"`; `interval` and `timeout` likewise report
  `"30s"` from prose.

So a click-stages-the-default gesture stages either nothing or a sentence. It
staged a sentence: `start_interval: interval value` was written into a user's
file, and a defaultless key became `key: ` with a trailing space. **The code is
right and the specification was wrong**, which is the direction this
reconciliation runs least often and is worth recording for that reason alone.

**What informed it.** The defect report from the owner, and then the
measurement above rather than an argument about it. The fix is structural, not
a patch at the call site: `defaultValue` (`inspector.ts:131`) returns the empty
string unless `default_source === 'schema'`, so **no prose default can reach an
edit through any caller**, and `defaultIsProse` (`:144`) exists separately so
the placeholder can *say* what the prose is (`placeholderFor`, `:160`) without
offering it. Showing the default is still the teaching mechanism; committing it
on the reader's behalf was never part of it.

**Consequences accepted.** The reader presses one more key than the mockup
implied. In exchange, no gesture in this product writes a value the reader did
not type — which is the same principle as decision 5, arriving in the UI.

**Where this was recorded before, and why that was not enough.** It lived in a
code comment (`host/panel.ts:745–756`) and a row in
`MOCKUP-TRACEABILITY.md`. A traceability matrix records *whether* code matches a
design; it is not where a decision lives, and nobody reading `epics.md` would
find it. `epics.md` story 5.2, `EXPERIENCE.md`'s `available-not-set` pattern and
Rana's flow step 5 were amended to the shipped gesture on the same day.

---

## 18. A bind mount is a marker, not an edge

**Decided 2026-08-12** — a ratification of shipped behaviour, not a change.
Nine of the ten relationship kinds R2.3 names are drawn as edges on the canvas.
The tenth, `bind`, is not drawn at all: the core emits it as a **self-edge**
(`internal/topology/build.go:512–519`, `To: from`), and the renderer skips every
edge whose ends are equal (`extension/webview/layout.ts:786–788`). It surfaces
instead as a marker line on the service node reading
`bind <host path> → <container path>` (`layout.ts:258 markerIndex`, `:279–286`).

**Why.** A bind's far side is a directory on the machine, and a host directory
is not a node in this project — the reasoning is already written at
`build.go:508–511`. Drawing it as an edge would mean either a loop from a box
back to itself, which reads as a rendering bug (`layout.ts:245–250` says so),
or promoting host paths to a **new node kind for something no compose file
declares**. The second is a real design question and the answer may one day be
yes; it is not this phase's question, and inventing a node per host directory
would put paths in a graph whose node identity is a config `Path` (AD-6) and
which nothing else in the product can select, navigate to, or explain.

**What was considered and rejected.** Promoting host paths to nodes. Rejected
for the reason above, and deferred rather than closed: if the product ever
edits or validates host paths, this is the decision to reopen.

**Consequences accepted, and one of them is a real cost.** Story 4.2's title is
*"See every relationship as an edge, not as card text"*, and a bind is
therefore literally the exception to the story that forbids exceptions. The
criterion has been amended to say so rather than being quietly under-met. Two
specific residues:

- The information is on the card, which is the shape the story argues against.
  It is mitigated only by the marker carrying the **host path** — the fact a
  reader actually wants — rather than a count, which is the "cryptic badge"
  anti-pattern the design names.
- ~~**The legend still lists `bind`.**~~ **Closed 2026-08-13.** It did: a
  project with a bind mount rendered a legend row for a kind with no path on
  the canvas and no rule in the stylesheet. `legendEntries` now filters by the
  kinds that are actually *drawn*, reusing `edgePaths`' own `from !== to` skip
  rule rather than deleting the string `'bind'` from the list — so legend and
  canvas cannot drift, and if a bind ever gains a real far side the row returns
  by itself. No `.legend-bind` rule was added: styling the row would have made
  the wrong thing look intentional. The marker needs no key, because it states
  itself in words on the card. Pinned by an invariant test asserting
  `legendEntries` equals the kinds `edgePaths` emits paths for.

---

## 19. Profiles filter services, never the resource inventory

**Decided 2026-08-12** — a ratification of shipped behaviour. `topology.Build`
filters **services** by the active profile set (`internal/topology/build.go:105–125`)
and deliberately does not filter top-level networks, volumes, configs or
secrets (`build.go:127–133`, where the reasoning is already written). Verified
against the shipped binary: a project whose only service is `profiles: [debug]`
and which declares `debugnet` and `debugvol` reports, with no profile active,
**two nodes and zero edges** — the resources, and nothing attached to them.

**Why.** A profile selects which services *run*. The resources a project
*declares* are its inventory either way, and story 2.1's deliverable is that
inventory whole. Filtering them would also delete the evidence for two things
the product exists to surface: a volume declared and mounted by nothing (rule
`unused_resource`, story 3.5) and a resource that only one profile uses, which
is a fact about the project rather than about the toggle.

**What it contradicted.** Story 2.5's criterion said `--profile prod` yields the
active services *"plus the resources they use"* — which reads as a filter and
was never built. The criterion was amended on 2026-08-12; the code did not move.

**Consequences accepted.** Switching profiles changes the service population and
leaves the resource population fixed, so a filtered graph can show a network
with nothing on it. This is stated in the criterion rather than smoothed over,
and it is the reason a profile control in the UI must not present itself as
"the stack under profile X" without qualification (story 4.6).

**Not backed by a test, in either direction.** `TestProfileFiltering`
(`internal/topology/topology_test.go:779`) and every other profile fixture in
that file declare no top-level `networks:` or `volumes:` at all — checked by
scanning every raw-string fixture in the file for one containing both
`profiles` and a top-level resource section; there are none. So nothing would
fail if a future change started filtering resources, and nothing would fail if
it stopped. A test asserting this property in the direction recorded here is
owed.

---

## 20. Structural insert moves out of phase 5; scaffolds and structural delete stay

**Decided 2026-08-13.** This is a **plan change, not a defect**. Nothing that was
built contradicts a story. The plan promised less than the agreed design did,
and for four months nobody reconciled the two.

**What was deferred, and where it says so.** The FR Coverage Map's R4.1/R4.2 row
— `epics.md:186` when this was found, `:196` after the amendment — maps R4.1, R4.2
(scalar), R4.6 and R4.8 onto Epic 6 and closes the row with *"Structural
insert/delete stays in phase 5."* Requirements §9 agrees: phase 5 is
*"Structural authoring: insert and delete via the same engine, scaffolds, full
spec coverage"*. Epic 6 therefore
shipped, correctly against its own criteria, a product in which **nothing can be
created**: not a service, not a network, not a top-level block, not a Dockerfile
instruction, not a Dockerfile stage.

**Why it is being revisited now.** The owner used the extension and hit it. Two
elements of the agreed design promise the opposite and neither had a story:

- **`+ add stage`** in the Dockerfile pane header
  (`ux-designs/ux-Composure-2026-08-11/mockups/directions-3.html:573`). It had **no
  row at all** in MOCKUP-TRACEABILITY.md — the status that file's `unstoried`
  category exists to prevent.
- **`Available here: ENTRYPOINT · CMD · USER · ARG · LABEL · STOPSIGNAL · SHELL ·
  VOLUME · ONBUILD`** under the last stage (`directions-3.html:590`). Storied as
  `unstoried` at MOCKUP-TRACEABILITY.md:175 and recommended for a story on
  2026-08-12; epic-6 retrospective action item 22 asks for the same thing. It is
  the compose inspector's `available, not set` — the product's single
  differentiator (AD-20) — in the other grammar.

And there is a **live broken path in the shipped extension**, measured
2026-08-13. `internal/schema` offers the document root's own unset keys like any
other node (`internal/schema/inspect.go:240–246`); against a two-service file
`composure schema -json` reports `configs`, `include`, `models`, `name`,
`networks`, `secrets`, `version`, `volumes` as available. `inspector.ts:534–545
group` renders an available list for every node with no root special case, so
those are on screen as buttons (`inspector.ts:1067 available`, `:1083
availableKey`). Clicking one stages `insert_key` at `parentOf('networks')`,
which is `''` (`extension/host/staging.ts:167–175`), and `locate` refuses an
empty path (`internal/strategy/structural.go:170–172`) with a bare
`fmt.Errorf` — so `edit.Refused` is false (`internal/edit/edit.go:486–491`), the
server sends `codeEditFailed` rather than `codeEditRefused`, and the reader is
told the tool broke rather than that it declined. Measured directly:
`composure apply -op insert_key -at "" -key networks` exits 1 with
`edit: operation 0: empty path`.

**Why the change is smaller than "authoring in phase 2" sounds.** The engine
inventory was taken by reading and by running the shipped binary on 2026-08-13:

| Capability | State |
|---|---|
| Insert one `key: value` into an existing mapping | Built — `strategy.Splice.InsertKey` (`internal/strategy/structural.go:290–319`), gated at 100% (`structbench.insertkey.*`, `benchmarks/baseline.json`) |
| Several inserts as one atomic edit | Built — `edit.run` checks every op's staleness before the first splice and re-locates each against the previous buffer, one validate and one diff (`internal/edit/edit.go:208–297`); the inspector already chains ancestor inserts (`extension/host/panel.ts:1057 stageValue`, `:1078–1090`) |
| Insert at the **document root** | **Missing** — `locate` rejects the empty path (`structural.go:170–172`) |
| Insert a **sequence entry** (R4.2's `insert list item`) | **Missing** — not in the closed operation set (`internal/edit/edit.go:54–72`) |
| Insert a Dockerfile instruction | Built — `dockerfile.File.InsertAfter` (`internal/dockerfile/edit.go:57–69`), gated at 100% (`dockerbench.insert_after_clean_pct`; probe at `cmd/dockerbench/main.go:107–115`) — and **unreachable**: no operation, no CLI flag, no RPC method, no extension code |
| The Dockerfile instruction vocabulary | Built — `internal/dockerfile/vocabulary.go`, attached per stage and per file (`form.go:89`, `:121`, `:179–181`), asserted to arrive over both doors (`cmd/composure/dockerfile_vocabulary_test.go:32–34`) — and **unreachable**: `extension/shared/protocol.ts:530–554` carries no `vocabulary` field and `stageform.ts` renders none |

So the compose half is largely adapter work over a measured engine with two
holes in it, and the Dockerfile half is almost entirely adapter work over an
engine that has been gated at 100% since Epic 6 without a caller.

**One defect the change forces into the open.** Both insert primitives rebuild
the document by splitting on `"\n"` and rejoining on `"\n"`
(`internal/strategy/structural.go:253 joinLines`; `internal/dockerfile/edit.go:62`,
`:68`), and the inserted line is appended without a line ending of its own.
Measured on a CRLF compose file: `composure apply -op insert_key` produced
`...image: nginx\r\n  cache:\n` — an LF line in a CRLF file. That contradicts
R4.1's own table row, *"Line endings — **CRLF stays CRLF**"*. It is invisible
to the gate because
`singleBlockDiff` splits on `"\n"` too, so a line missing its `\r` still counts
as one clean inserted block; and it is invisible to the corpus because **0 of
146** corpus compose files and **1 of 152** corpus Dockerfiles use CRLF
(`corpus-repos/airflow/providers/informatica/dev/informatica_simulator/Dockerfile`,
measured 2026-08-13). A green gate says nothing about it, which is why story 7.1
requires the fixtures rather than the corpus.

**The new boundary.** Phase 5 does not disappear; it is cut in two along the
line between *adding a thing the reader named* and *inventing content the reader
did not*.

- **Moves forward, into Epic 7:** insert only — a top-level block, a service, a
  resource entry, a sequence entry, a Dockerfile instruction, a Dockerfile
  stage, and the `Available here` block that makes the last two discoverable.
  Each one is the reader naming a thing that goes in a place they chose.
- **Stays in phase 5:** R5.1's scaffolds and R5.4's *"starting scaffolds per
  stack, with every default visible and overridable"*. A scaffold writes lines
  nobody typed, which is a different product decision from inserting a key
  somebody clicked, and DECISIONS.md 17 already records what happens when this
  product writes a value the reader did not supply.
- **Also stays out, deliberately:** structural **delete** from the UI. The
  engine has it (`strategy.DeleteKey`, `dockerfile.File.Delete`) and both are
  gated, so this is not a capability gap — it is a scope call. Deleting a
  service is destructive, it is what git and undo are for, and Epic 7 is already
  eight stories. Recorded here so the next audit does not read its absence as an
  oversight.

**What this does not change.** Requirements §9's phase table is the roadmap of
record and is left as written; the amendment is made where the plan is
executable, in that FR Coverage Map row, which now names this entry. The requirements
should be reconciled at the next revision rather than edited underneath a
shipped table.

**Consequence accepted.** Epic 6 was the release gate and it is closed. Epic 7
lands after it, which means the first public release can create nothing. That is
the cost of the original deferral and it is not retroactively fixable; what is
fixable is that the deferral was invisible — it lived in the second half of one
table cell, and the two design elements it silently dropped had no row and no
story between them.

---

## 21. Editing an inherited value writes it on the service, never on the anchor

**Decided 2026-08-13**, from a defect the owner hit in the extension. It is a
product decision before it is a bug fix, and the reasoning is why.

**What happened.** Selecting `web` in `examples/webstack/compose.yaml` and
changing the `restart` combobox produced:

    That edit could not be made
    edit: operation 0: path services.web.restart not found

The pane was right and the engine was right. `services.web.restart` resolves to
`unless-stopped`, and the pane said so with the provenance decision 12 built:
`webstack/compose.yaml:9 · from *defaults at webstack/compose.yaml:29`. **The
file has no `restart` under `web` at all.** The value arrives through
`<<: *defaults`, so a `replace_scalar` aimed at that path lands on no bytes, and
`locate` correctly reported that there are none.

**The three options, and why (a).**

| | What it does | Verdict |
| --- | --- | --- |
| **(a) insert it on the service** | `insert_key restart: always` under `services.web`. YAML's own rule is that a locally declared key beats a merged one, so the service changes and the anchor does not | **Chosen** |
| (b) edit the anchor | one splice at `:9`, and every service merging `*defaults` changes with it | Rejected |
| (c) refuse and explain | honest, and leaves the reader with nowhere to go for a change that is entirely legitimate | Rejected as the *only* answer; kept as the answer for the four shapes that have no (a) |

(a) is what a merge-key override *is*. The reader clicked a field on `web`; they
did not ask a question about `db`, `api`, `cache`, `docs` and `worker`, which is
what (b) silently answers for them. And (b) is unrecoverable in the way this
product cannot afford: the diff is two lines, it looks exactly like the two-line
diff of an ordinary edit, and the blast radius is invisible in it.

(c) alone was rejected because refusing a legitimate edit is not honesty, it is
an unimplemented feature wearing honesty's clothes. The engine already had
everything (a) needs — `insert_key` with ancestor chaining, and `edit.Plan`'s
multi-operation atomic apply — so refusing would have been refusing to call a
function that was already gated.

**(b) is not forbidden, it is unrouted.** The anchor is a real place with real
bytes: `x-service-defaults.restart` is an ordinary scalar and editing it there
is an ordinary splice, which still works and is still a two-line diff. What was
rejected is *arriving* there by clicking a field on a service. The reader who
wants every service to change navigates to the anchor, which is where that
change is visible for what it is.

**The consequence is stated before the write, not discovered in the diff.**
`stack/editable` (new) answers, for each path the pane is about to draw, where
that value is actually written. The inspector renders the core's sentence under
the field:

> web does not set restart here — it arrives through `<<: *defaults` on line 29.
> Writing a value adds restart to web, which overrides *defaults for this one
> place; the anchor on line 9 and everything else that merges it are untouched.

**What the fix uncovered, which is the more important half.** The visible defect
was a refusal. Classifying the path turned up four more shapes of the same gap —
a resolved value that does not live at the path it is read from — and **none of
the other four failed.** Every one of them was this engine's characteristic
defect: not a crash, a confident wrong answer.

| The file says | `replace_scalar` wrote | Now |
| --- | --- | --- |
| `entrypoint: *entry` | `entrypoint: ZZZentry` — it spliced over the `*` | `ErrAliasValue` |
| `entrypoint: &entry /bin/sh` | dropped the anchor; every `*entry` below dangled. Caught only by the re-parse, and reported as "the result would not parse" | `ErrAnchoredValue` |
| `command: \|` + two lines | replaced the `\|`; the body became part of a multi-line **plain** scalar. Still valid YAML. Not what the file said | `ErrBlockScalar` |
| a key inside a merged `healthcheck` | would declare `healthcheck` locally, which REPLACES the merged mapping whole and drops `test` and `retries` | `ErrMergedMapping` |

The last one is why (a) is offered **only when the merge is crossed at the last
segment**. A merge key merges the keys of a mapping, not the mappings
recursively, so an override one level down is not an override — it is a
replacement with three keys missing. There is no safe (a) there, so that case
gets (c), with the reason said at the field.

**Where it lives.** `internal/edit/inherited.go` — `Classify`, one closed set of
slugs, one sentence per slug. It is called by `locate` before the splice, by
`composure editable`, and by `stack/editable` for the pane, so the engine's answer
and the pane's explanation are one implementation. `internal/strategy` is
**untouched**: no fidelity metric moves, and `make gate` still passes 29/29.

**Rule 6, the other half of this work.** "Some of the values are not editable and
there is nothing says why" — the owner, the same day. Every inert field now
carries its reason at the field, and the reasons distinguish *cannot be done
safely* from *not built yet*:

| Inert field | Why | Which kind |
| --- | --- | --- |
| an alias `*name` | splicing a reference rewrites what the file points at | cannot be done safely |
| an anchored value `&name x` | the splice cannot keep the anchor | cannot be done safely |
| a block scalar `\|` / `>` | the value is the lines below; replacing in place leaves them behind | **not built yet** — it needs a reflow policy this engine does not have |
| a key inside a merged mapping | a local override replaces the whole mapping | cannot be done safely |
| an unset key inside a flow mapping | `ErrFlowStyle`: a block key cannot go inside `{...}` | cannot be done safely |
| a list entry | it carries no config path in this wire schema, so there is nothing to address | **not built yet** |
| a mapping entry declared null | no bytes to replace | not a refusal: it stages a delete and an insert |

**The fixture, and the trap it exists to avoid.**
`testdata/edge/e42-merged-value.yml`. Its `inherits` service does **not** declare
`restart` and its `declares` service does — because a fixture whose service
declares the key it also inherits cannot tell an inherited value from a declared
one, and every check written against it would pass for the wrong reason.

**Consequences accepted.** A pane draws one extra request per selection
(`stack/editable`, batched — one call for the whole pane). An edit to an
inherited value produces a one-line **added** diff rather than a two-line
replacement, which is a different diff from the one an ordinary field produces
and is stated in the sentence above the reader's decision to write it.


---

## 22. Image discovery moves out of phase 4; the network is a state, never an assumption

**Decided 2026-08-13.** A **plan change, not a defect**, exactly as entry 20
was. Nothing that shipped contradicts a story; the roadmap simply ordered this
capability behind work the owner does not need first.

**Why it moves.** The owner used the extension and asked for it in one line:
*"Image names are not lookup from dockerhub or even option to search for image
in docker hub."* Requirements §9 puts R6 at phase 4 and the FR Coverage Map
closed the row with *"Phases 5–8. §9 ordering is load-bearing"*. That ordering
was load-bearing for a reason — comprehension before authoring, authoring before
discovery — and pulling one item forward out of an ordered roadmap is the kind
of change that deserves a paragraph rather than a commit message.

**Why the change is smaller than "a phase moves forward" sounds.** The inventory
was taken on 2026-08-13 by reading `internal/hub/hub.go` and running the shipped
`cmd/hubsearch`:

| Capability | State |
|---|---|
| Search, two endpoints, v4 with the legacy `index.docker.io` fallback (R6.1, R6.6) | Built — `hub.Search` |
| Tag listing with size, last-pushed, per-architecture digests (R6.2) | Built — `hub.Tags`, `Tag.Architectures` |
| Family filter so nightlies do not bury stable releases (R6.4) | Built — `Tags`' `nameFilter` |
| The tag semantics that decide what may be offered (R6.3) | Built and **tested** — `IsUnstable`, `IsDateTag`, `Version`, `CompareVersions`, `Classify`, `internal/hub/hub_test.go` |
| Discovery ending in an edit (R6.5) | Built — `cmd/hubsearch stale` joins it to `internal/dockerfile` |
| **Choosing the candidate** | Built and **unreachable** — it is in `cmd/hubsearch/main.go`, a `main` package nothing can import |
| Context, cancellation, per-request deadline | **Missing** — one 15-second `http.Client` timeout, no `context.Context` anywhere |
| A cache (R6.8) | **Missing** |
| Rate limiting and offline as typed states (R6.8) | **Missing** — both are `fmt.Errorf` strings |
| A guard that the reference is a Docker Hub one | **Missing** — `ghcr.io/x/y` was normalised and looked up as a Hub repo |
| Any caller: a `composure` subcommand, an RPC method, a line of the extension | **Missing entirely** |

So the epic is adapter work over a measured engine, plus the four rows in the
lower half — and those four rows are the whole of the risk.

**The decision that matters is not the rescope.** It is this: **every capability
in this product until now has been a pure function of files on disk, and this
one is not.** That is a genuinely new failure surface, and the rules it lands
under are:

- **The lookup is never in a render path.** The inspector is posted from the
  file, complete, and the lookup is a *separate* request whose answer arrives
  later as its own message or never arrives at all. There is no `await` between
  a selection and a pane. A pane that waits on Docker Hub is a pane that hangs
  in a coffee shop.
- **Offline, rate-limited, switched-off, another registry and not-comparable are
  five different states with five different sentences**, not five spellings of
  an error. The 180-per-minute limit is per **IP**, so the reader who hits it is
  most likely behind a corporate NAT and has done nothing wrong; telling them
  "request failed" would be both useless and untrue.
- **The reader can turn it off.** `composure.dockerHub: off` in the extension,
  `COMPOSURE_OFFLINE=1` for the core. A tool that has been local for its whole life
  does not silently start making requests, and the switch is also what lets a
  test prove a code path never opened a socket.
- **Cross-registry search is still out of scope** (requirements §3). A
  `ghcr.io` reference gets a sentence saying so, not an empty answer — an empty
  answer reads as "there is nothing newer", which is the confident wrong answer
  in the place a reader is deciding whether to upgrade.
- **No CVE facet** (R6.7). The public API does not carry vulnerability data.
  Docker Scout is paid beyond one repository and a local Trivy or Grype scan is
  a different product. The word does not appear in the change.

**What this does not change.** Requirements §9's phase table is the roadmap of
record and is left as written; the amendment is made where the plan is
executable, in the FR Coverage Map row, which now names this entry. Phase 4's
other half — R8 runtime, R7.5's layer and cache view — does **not** come forward
with it, and nothing in this epic writes a file: a chosen tag stages through
`replace_scalar` or `set_base_image`, the two operations Epic 6 already gated,
and `Save to <file>` remains the only control that touches disk (DECISIONS.md
17, AD-19).

**Consequence accepted.** The product now has a dependency on a third party's
**undocumented** endpoints — `api/search/v4` is shaped for Docker's own UI and
returns `pull_count` as the display string `"1B+"`. That is the standing
argument for eventually putting a thin service in front of search, so an
endpoint change is a server deploy rather than a release to every user. It is
not built now, because a backend before anyone has hit the ceiling is a hosting
bill, an account system and a privacy question about which images people search
for, in exchange for nothing. The endpoints are fields on the client so that
when it is built, one struct changes.

---

## 23. A comment is addressed by (path, position), and a run of them is one thing

**Decided 2026-08-13**, from the owner in one line: *"I want to be able to edit
comments and add comments anywhere."* It is a product decision before it is a
feature, because "anywhere" is the part that had to be narrowed.

**Why it was worth narrowing.** Comments are the whole thesis of this engine.
The splice exists so that changing a port does not move the sentence above it —
that is the property RESULTS.md measures and the reason four competitors are
dead. Which makes "comments are preserved and untouchable" a strange place to
have stopped.

**The addressing scheme, and what "anywhere" does not mean.** A comment is
addressed by `(config path, position)` and position is one of exactly **two**
values: `above` — the contiguous run of comment lines directly above the key, at
the key's own indent — and `trailing`, the `#…` after the value on the key's own
line. There is **no third position and no free-floating comment.**

A comment that belongs to nothing has no path to address it by. The only
address available for one is a line number, and a line number is an address that
moves the instant anything above it is edited — which is precisely the silent
rebase AD-19 refuses. So a comment nobody's key owns is left alone, and that is
a narrowing of the owner's word, said here rather than discovered in a bug.

**A run of comment lines is ONE comment.** Not a decision so much as a refusal
to invent a second one: `attachedCommentStart`
(`internal/strategy/structural.go:104–114`) already defines the run, and
`DeleteKey` has always used it to decide which comments travel with a deleted
node. A per-line address would be a second model of the same bytes, and the two
would disagree the day somebody edited between them. So `set_comment above` with
a two-line text replaces the whole run, and a key deleted with `delete_key`
still takes its comment with it — now pinned by a fixture rather than left as an
implementation detail of delete.

**Blank lines and indentation.** A blank line breaks the attachment, which is
again `attachedCommentStart`'s own rule rather than a new one; a new comment
lands between the blank and the key, and the run above the blank is untouched.
A comment takes the key's own indent. Nothing else about the file's spacing
changes: no blank line is added, none is removed, and the whitespace in front of
an existing trailing comment is copied rather than normalised — `image: nginx  #
x` keeps its two spaces. A **new** trailing comment gets exactly one space,
which is the minimum YAML requires for a comment to be a comment and no house
style beyond it.

**Where the positions come from, and where they could not.** goccy's comment
handling is unreliable and this repository has the scar: it counted a COMMENT
line in a CRLF document as two lines, which produced `ErrPositionMismatch`. So
**nothing in `internal/strategy/comment.go` reads a goccy comment token.** The
run is found by `scanLines` over the buffer; the trailing comment is found by
scanning the key's own line **forward from the end of the value**, never from
the first `#` on it — `note: "a value with a # inside it"   # a real one` is the
case that decides that, and a first-hash scan writes a marker into somebody's
string and leaves a file that still parses.

**What could not be buffer-derived, stated rather than hidden:** the key's own
anchor line still comes from `locate`, which is goccy. It is guarded by that
same `ErrPositionMismatch` assertion and by nothing else.

**Three refusals, because three shapes cannot be answered without a guess.**
`ErrCommentTarget` for a block scalar, an anchored or aliased value, a value
that does not end on its key's line, and an unknown position name;
`ErrFlowStyle` — the existing sentinel — for a flow collection, where finding the
end of the value means tracking quoting and nesting across a line;
`ErrCommentText` for text that would not be written as one comment. And
`ErrNoComment` for a delete at a position that has none, which is **not**
`ErrNoChange`: "there was nothing to delete" and "the delete did nothing" are
different sentences, and a silent success there tells somebody their comment is
gone when it is still in the file three lines up, attached to something else.

**Consequence accepted.** Editing a comment on a key whose value is a block
scalar is not possible, and neither is a comment attached to nothing. Both are
reachable in the text editor the extension sits inside, which is the answer the
product already gives for a block scalar's value (DECISIONS.md 21).

### Amended 2026-08-14, from an adversarial review of the shipped code

Three of the decision's own words turned out not to be true of the
implementation. Each amendment is the decision saying what it already meant.

**"At the key's own indent" is the indent of the LINE, not the column of the
token.** `locate` answers a sequence entry with the position of its VALUE, so
for

    ports:
      # the public edge
      - "8080:80"

the run was looked for at column 4 while the `-` and its comment sit at column
2. `delete_comment above ports[0]` therefore answered `no-comment` with the
comment plainly there, and `set_comment above ports[1]` wrote a SECOND one two
columns deeper, attached to nothing. It is UI-reachable: the inspector hangs the
comment control off every sequence entry. The indent is now read off the buffer
(`runIndent`), which is this decision's own "positions come from the buffer"
applied to the last number that was still coming out of goccy. One consequence
is accepted and named: an entry and a key written on the same line — `[0]` and
`[0].name` of `- name: web` — address the SAME run, because a run belongs to a
line and they share one.

**`above` takes the flow refusal `trailing` already took.** An entry of
`test: ["CMD", "wget"]` has no line of its own. `trailingSite` refused it with
`ErrFlowStyle`; `aboveSite` had no guard and wrote a comment above the `test:`
line at the entry's column, owned by nothing. Two positions disagreeing about
one target is AD-14 in the small, and the refusal was chosen over the
alternative — answering with the enclosing collection's line and indent —
because a comment "above" an entry that shares its line with three others is an
address for a thing the file does not have. The KEY that holds the flow
collection still takes a comment above it; only what is inside the brackets is
refused.

**A marker the reader DOUBLED is theirs, and neither stripped nor prefixed.**
"one leading `#` is stripped" plus "the site's marker is written" is one strip
and one write, and the pane strips one too — so `## mine` came back `# mine`
through the pane and `# # mine` when typed fresh. `##` is a section-header
convention real files use. It is now written exactly as it arrives, in both
halves of one rule (`commentBody` and `commentText`), and a blank line inside a
run carries the marker ALONE — `# ` left trailing whitespace on a line nobody
typed one on.

---

## 24. A list entry is addressed by index, and a moved index is refused rather than rebased

**Decided 2026-08-13**, from the owner in four words: *"i am unable to edit
cmd"* — a `healthcheck.test` rendering as `CMD · wget · -qO- ·
http://localhost:3000/healthz` with no way to change any of it.

**What was actually missing was less than the pane implied, and worse.**
Measured on 2026-08-13: `resolve.Path` already renders an index as `[0]` and
`ParsePath` reads it back; `locate` already resolves a numeric segment against a
sequence; and `replace_scalar` at `services.web.healthcheck.test[1]` **already
worked**, in the flow form the screenshot showed and in the block form, with a
two-line diff. DECISIONS.md 21's table recorded a list entry as *"not built
yet"* because it carries no config path **in the wire schema**, and that is a
true statement about a different layer. The engine has had the address all
along.

**So the decision is about the hole underneath it, which is this engine's
characteristic defect exactly:**

    edit.Classify(src, "services.web.ports[9]")   // a three-entry list
      → {"reason":"absent","plan":"insert_key",
         "detail":"The file does not set 9. Typing a value adds it."}

A confident wrong answer with an invitation attached. There is no ninth entry,
`insert_key` with the key `9` on a **sequence** adds nothing at all, and the
sentence tells the reader to try it. The write path was no better: a bare `path
services.web.ports.9 not found`, so `edit.Refused` was false and the reader was
told the tool had broken rather than that the entry is not there.

**Index-based, and content addressing was considered and rejected.** "The entry
whose text is `wget`" is unavailable in a list with repeats — `command:` and
`test:` routinely have them — and when it guesses wrong it silently edits a
different entry, which is the failure this product exists not to have. The cost
of an index is that it moves when the list changes, and **the answer to that is
not a cleverer address, it is AD-19**: an `Expect` recorded against the entry's
bytes catches the move and the edit is refused, never rebased. That is the
answer the rest of the write path already gives and this adds no second one.

**The two shapes of "the list changed", and both are refusals.** A list that
shrinks *below* the staged index is `ErrEntryIndex`, new here, whose sentence
says how many entries the list has. A list that still *has* that index but now
holds something else there is `ErrStaleRange`, which already existed — and it is
the sharper of the two, because the index still resolves and points at the wrong
value. Both are pinned by a fixture; a two-entry fixture would only ever reach
the first.

**The fixture and the trap it exists to avoid.**
`testdata/edge/e43-repeated-list-entries.yml`. Every list in it **repeats** at
least one entry and the interesting edits are in the **middle**, because a list
of distinct values edited at index 0 cannot tell an off-by-one from a correct
answer — every wrong entry looks wrong. A test asserts the repeats are still
there, so the fixture cannot quietly become the useless kind.

**One ambiguity is left exactly where it was.** `resolve.Path.String` renders a
numeric mapping key as `[8080]`, which reads like a sequence index. That is a
**display** ambiguity, documented at `internal/resolve/path.go:49–55`, and this
work must not turn it into a resolution one: `findGoccy` and `childOf` both
disambiguate by the parent node's kind, and a fixture pins that
`environment: {8080: "x"}` is still reachable and is still a key.

### Amended 2026-08-14, from an adversarial review of the shipped code

**There is now ONE reading of an index, and it is `strconv`'s.** There were two:
a hand-rolled `n = n*10 + int(c-'0')` with no bound in `internal/strategy`, and
`strconv.Atoi` in `internal/edit`. AD-14 says what happens next, and it happened
exactly where it cost. Measured through the real binary on a two-entry list:

    PREVIEW services.web.ports[18446744073709551616] -> ok, range before "8080:80"
    APPLY   the same path                            -> WROTE ENTRY 0

The literal overflows to 0; `…617` overflows to 1. After the wrap the index IS
in range, so `ErrEntryIndex` — the whole point of this decision — was bypassed,
and the engine wrote a real entry while reporting success. `Classify`, reading
the same segment with `Atoi`, answered `absent` + `plan: insert_key`: the
pre-9.2 confident wrong answer with an invitation attached, on the very path
this decision exists to refuse. `strategy.EntryIndex` is now the one reading,
and `internal/edit` calls it.

**And an index-SHAPED segment that is not a position is refused by name.**
`ports[-1]`, `ports[+1]`, `ports[1 ]` returned the bare `path
services.web.ports.-1 not found` — unclassifiable, so `edit.Refused` answered
false and the reader was told the tool had broken. They are now `ErrEntryIndex`.
None of them is reachable from the pane, which builds the address itself; the
core is a library, a CLI and later an MCP server, so "the caller is trusted" is
not the contract. They are refused rather than resolved — `+1` and `1 ` would be
second spellings of an address this engine writes one way, and a negative index
is Python's idea, not YAML's. A segment that is not index-shaped at all
(`ports.web`) keeps its own sentence, because asking a sequence for a key is a
different mistake from asking it for an entry it does not have.

---

## 25. Moving a value into a variable writes two files, in an order chosen so the only reachable partial state is inert

**Decided 2026-08-13**, from the owner: *"i want to be able to move any value to
be variable and that would create an .env file if missing or add to it the
variable and set the variable name in the docker file/compose."*

**The operation is small and the decision is not.** The compose half is
`replace_scalar` — gated since Epic 6, unchanged — and the `.env` half is one
appended line. What had to be designed is that **two files change**, because the
half-done state is a stack that no longer starts: a compose file saying
`${POSTGRES_PASSWORD}` with no `.env` to define it resolves to the **empty
string** under `docker compose`, and a database with a blank password is a
different kind of bad day from a tool that refused.

**Atomicity, in three parts, because there is no cross-file atomic rename.** The
residue is named rather than claimed away:

1. **Both new buffers are computed and both validated before either file is
   opened for writing.** The compose result re-parses through `edit.validate`;
   the `.env` result is read back with `resolve.ParseDotEnv` and the variable
   must return **exactly** the value that was taken out. A failure here writes
   nothing at all.
2. **Both temp files are created, written, synced and chmod'd before either
   rename.** Every ordinary failure — no disk, no permission, read-only mount —
   lands here, with both files untouched and both temps removed. This is why
   `writeFile` was split into `stageFile` plus a rename.
3. **The `.env` is renamed first and the compose file second.** The only window
   left is between two rename syscalls, and the state it can leave is a `.env`
   carrying a variable nothing references: **inert**, and **converging**, because
   re-running the operation finds the variable already present with the same
   value and treats that as a no-op. If the second rename fails the `.env` is
   rolled back — to its own bytes, or removed if this operation created it.

Each of the three has a test that makes it fail: an unwritable `.env`
directory, an unwritable compose directory, and `os.Rename` behind a package
variable so the window in step 3 can be entered on purpose. A failure path with
no test is a failure path nobody has run.

**Two of those three properties shipped with no test that could fail, and an
adversarial review found both.** Neither was a wrong line of code — each was a
correct property with nothing aimed at it, which is the harder defect to see:

- **The ORDER in step 3 was reversible with a fully green suite.** Both tests of
  the window failed the rename whose target is `compose.yaml`, and that failure
  produces the same end state under either order — so swapping the two renames
  passed the whole of `internal/edit`. Under the swap, failing the `.env` rename
  leaves a compose file reading `${POSTGRES_PASSWORD}` with no `.env` beside it:
  measured, and exactly what this decision forbids. The test that closes it fails
  the rename whose target basename is `.env` and asserts the compose file is
  byte-identical, no `.env` exists, and neither temp is left behind.
- **Step 1's whole-file `.env` readback was unbacked.** `renderEnvValue` already
  reads each candidate back on its own line, and every fixture was a well-formed
  `.env`, so deleting the outer check passed the whole suite. The interaction it
  is the only thing that sees: a `.env` holding an unterminated quote plus a
  value carrying a `"`. The new line's quote closes the old one, the variable the
  compose file now references is never defined, and it resolves to the empty
  string. With the check removed, measured: both files written.
  `testdata/edge/e49-unterminated-quote.env` and `e49-quote-in-value.yml` are
  the permanent regression pair.

Both are now proven in both directions — the test passes against the shipped
code and fails against the mutation that removes the property.

**The conflict refusal names the line that is in effect.** `resolve.ParseDotEnv`
takes the LAST definition of a repeated name; `envLineOf` returned the FIRST, so
a doubly-defined name was reported with the value from one line and the number
of another, sending the reader to settle the two by hand at the line that is not
the one in effect. `testdata/edge/e50-doubly-defined.env` holds the case.

**Which `.env`, and why it is not a preference.** The `.env` **in the directory
of the compose file being edited**, never an `env_file`. Compose does not
consult `env_file` for interpolation at all — it feeds the container, not the
template, and `internal/resolve/interpolate.go:124–131` already records the
divergence R1.5 makes there. So a value written into an `env_file` produces a
`${VAR}` that resolves to empty, which is the confident wrong answer in the one
place it costs the most. `-env` overrides it for a caller who knows better; the
default does not guess.

**The four other things that had to be settled.**

- **Already defined with a different value:** refused, naming the file, the line
  and both values. Overwriting destroys a value somebody configured, and a
  second line of the same name is a file whose meaning depends on which parser
  reads it. With the **same** value it is not a conflict at all: the compose
  half is written, the `.env` is left byte-identical, and the operation is
  idempotent — which is also what makes step 3's window converge.
- **Already interpolated:** `${DB_PASSWORD}` is refused. There is no literal to
  move, and moving a reference defines the variable as itself.
- **Naming:** `^[A-Za-z_][A-Za-z0-9_]*$`, because `${a-b}` is not a variable
  reference and a name outside that shape produces a compose file that means
  something other than what the reader asked for. A default is derived from the
  key. A name that shadows a host environment variable is **not** refused —
  whether the host's value should win is Compose's precedence question, not this
  operation's.
- **Quoting in the `.env`:** decided by **readback**, not by a character
  blocklist. Three spellings are tried — bare, double-quoted, single-quoted —
  and the first that `resolve.ParseDotEnv` returns unchanged is used. Same
  property `edit.bare` and `strategy.entryText` apply in their own grammars, and
  the same reason: `.env` quoting has no escapes, so a value that cannot survive
  any of the three is refused rather than mangled.

**Dockerfiles are refused by name, and that is the second decision here.** The
Dockerfile equivalent is `ARG`, not `${VAR}` from a `.env`: an ARG is
build-time, has to be re-declared after every `FROM`, and **`.env` never reaches
it** — `docker compose` passes build arguments only through `build.args`, so the
two-file write has no meaning there and a third file would be involved. An ARG
used before it is declared expands to the empty string with no error, which is
the same silent-empty failure the `.env` choice above exists to avoid. Half-
building it would ship an `ARG` that does nothing on some files and something on
others, so it is **story 9.4, written down and unbuilt**, and the refusal says
so in the reader's own language rather than returning a path error.

**Where this meets story 3.1, which is the point of it.** The
plaintext-credential rule has recommended exactly this fix since Epic 3 and
could only recommend half of it: `internal/diagnose/rule_credentials.go:215–229`
emits a `replace_scalar` to `${NAME}` and leaves the reader to place the secret
themselves. `composure extract` accepts the same config path the finding anchors,
so **the finding and the remedy are now the same address**, and the list-form
reasoning — `- NAME=${VAR}`, never `- ${VAR}`, which Compose reads as
pass-through-by-name — is reused from that rule rather than re-derived.

Changing the **rule** to advertise the completed fix is a change inside
`internal/diagnose` and was deliberately not made in this pass, which touched no
diagnostic. It is recorded here so the meeting point is not lost: the rule's
`Fix` shape has no field for "and write this to the .env", and adding one is a
change to the described-fix contract that deserves its own story rather than a
line in this one.

**A symlinked `.env` is written THROUGH, not replaced.** `proj/.env ->
../shared.env` — one secrets file shared by several stacks — is an ordinary
setup, and more ordinary for a `.env` than for anything else this product
writes. The staged-temp-plus-rename that makes every other write atomic
*replaces* a link: `proj/.env` became a regular file holding the new content
while `shared.env` still held the old, the link was gone, and the exit code was
0 with no warning. Every other stack pointing at the shared file never saw the
variable.

Two answers were possible and the choice is argued rather than assumed:

- **Refuse a symlinked `.env` by name.** Safe, and useless. The thing the reader
  would have to change is the project's layout, not the edit — so the refusal
  costs them the operation and teaches nothing they can act on. It also breaks a
  setup that has worked since Epic 6.
- **Follow the link and write the target.** Chosen. A link is a deliberate act
  by whoever set the project up, and writing through it is what its author
  expects. The accepted cost is stated plainly: **this can write a file outside
  the directory of the compose file being edited** — that is precisely what the
  link asks for, and the result reports the path that was actually written
  (`ExtractResult.EnvFile`), so a preview names the real file rather than the
  link. `EnvCreated` follows the target too, which matters because a rollback in
  step 3 removes a file it created: getting it wrong would DELETE somebody's
  shared secrets file.

**Where the resolution lives, and why not in `writeFile`.** In `runExtract`,
where the `.env` path is decided — `throughSymlink`, which follows only the FINAL
path component, one hop at a time. Deliberately **not** in `stageFile`, which
`writeFile` and therefore every other write in `internal/edit` share: putting it
there would change the behaviour of the compose half and of every edit since
Epic 6 in one unmeasured move, to fix a problem only reachable here. So the
asymmetry is recorded rather than hidden: **a symlinked compose file is still
replaced by a regular file**, exactly as before this change. That is pre-existing
behaviour, it is now written down, and closing it is a change to every write path
in the product rather than a line in this story.

`filepath.EvalSymlinks` was tried first and rejected: it resolves every *parent*
too, and on macOS `/var` is itself a link to `/private/var`, so it returns a
different string for a `.env` that is not a link at all — and every path the
function hands back stops matching the one the caller is holding. A dangling
link is written at the link's own path, as if absent; a loop returns the last
path and lets the OS's own error surface, rather than adding a sentinel every
caller must classify.

**The ARG refusal is for Dockerfiles, not for everything that is not compose.**
The refusal above tested `strategy.RootIsMapping` alone, so a YAML sequence, a
bare scalar and an empty file were all told "the Dockerfile equivalent of this is
an `ARG`" — `composure extract -at a list.yaml` answered with a paragraph about
build arguments. The test is now a `FROM`: that is what makes a file buildable,
what scopes an ARG, and what story 9.4 is about. Anything else gets a refusal
that says its root is not a mapping.

**The value is never printed on its own line.** `printExtract`'s headline read
`MOVED hunter2 into ${POSTGRES_PASSWORD}` — the secret, bare, with no context,
on the line most likely to be pasted into a chat window. It now names the config
path instead. The two **diffs** still carry the value, and that is deliberate:
a diff is what the reader approves, and the "both diffs, always" rule above
exists so that neither half is hidden.

**Consequences accepted.** Two: the residual window in step 3, named above and
proven inert; and the fact that this is the first write path in the product
whose blast radius is larger than the file the reader is looking at. `Save to
<file>` is no longer literally one file, and the preview says so by carrying
**both** diffs — a two-file operation that showed one diff would be a lie about
the half the reader cannot see.


---

## 26. A described fix can carry a remedy that is not a single-file splice

**Decided 2026-08-14**, story 9.5. **This row was referenced by shipped code
before it existed** — `cmd/composure/serve.go:81`, `internal/diagnose/remedy.go:8`,
`internal/diagnose/diagnose.go:157` and two files in `extension/` all cite
"DECISIONS.md 26", and there was no 26. That is the gap this row closes; the
decision itself shipped with story 9.5 and is restated here so the citation
resolves.

`diagnose.Fix` describes a **single-file splice** and nothing else. The
plaintext-credential rule has recommended moving a secret into `${NAME}` since
Epic 3 and could only recommend half of it, because there was no field for "and
write this to the `.env`".

- **`Fix` gains a `Remedy`, and it carries NO byte range.** There is no range to
  carry: the `.env` line does not exist until it is written and its offset is
  not knowable from the files the report was built from. `Remedy` therefore has
  no `Range` field at all — **unrepresentable rather than unchecked**, which is
  the only honest answer for a half that `disowned()` cannot see.
- **The splice half still passes every existing guard.** The remedy hangs off
  the fix the rule already emitted; `Run`'s `unownedFix` and `disowned` check
  that fix exactly as before.
- **It is data, never a call.** A `Fix` is data by contract (decision 17), so
  offering a remedy stages nothing and runs nothing.
- **One implementation of the readback.** Whether a value can be written as one
  `.env` line is `internal/edit`'s `renderEnvValue`, decided by readback through
  `resolve.ParseDotEnv`. `internal/diagnose` asks it rather than reimplementing
  it — a second answer to a format property is a second answer that eventually
  disagrees.
- **It is a protocol change.** `Fix` is on the wire: revision **8**.

**`Finding.NoRemedy` is KEPT and now surfaced — reviewed 2026-08-15.** An
adversarial review found it set by `Run`, typed in `extension/shared/protocol.ts`
as `no_remedy?: string`, and **read by nothing at all**: no Go code, no test, no
renderer. The decision was between removing it and surfacing it, and it is
surfaced, for the same reason rule 6 exists — a guard that silently discards is a
guard nobody can debug, and `NoRemedy` is the only record that a rule offered a
remedy `Run` took away. Removing it would make the drop invisible at exactly the
moment somebody needs to know why their new rule's remedy vanished. Two things
changed so that "read by nothing" is no longer true: `composure diagnose` prints
it under the fix it belongs to, and `TestRunDropsAMalformedRemedy` asserts it.
It stays latent in practice, because no shipped rule can produce a malformed
remedy — which is exactly why the guard that sets it went untested for a
release.

**That test was one of the checks that could not fail.** It was called
`TestRunDropsAMalformedRemedy` and it never called `Run`; it called
`unusableRemedy` directly, so the two criteria that are about `Run` — enforced
there **by construction** rather than trusted per rule, and the remedy dropped
**alone** without taking its correct `replace_scalar` with it — had no subject.
Gating the call site on a rule id that never matched left the suite green, and
so did writing `f.Fix, f.NoFix = nil, why` in place of
`f.Fix.Remedy, f.NoRemedy = nil, why`. The predicate's enumeration keeps its own
test under an honest name; the criteria are driven through `Run` with
`guards_test.go`'s `fakeRule`, which is the only way to reach a guard every
shipped rule self-censors past.

---

## 27. A Dockerfile literal moves into an `ARG`, in the one place the build can see it, and `build.args` is deliberately not wired

**Decided 2026-08-14**, story 9.4, which decision 25 deferred and wrote down
rather than half-building. It is **not** story 9.3 with a different parser, and
three properties of `ARG` decide every choice below. Getting any of them wrong
ships an `ARG` that does nothing on some files and something on others:

1. **An `ARG` used before it is declared expands to the empty string, with no
   error.** `FROM node:${NODE_VERSION}` above `ARG NODE_VERSION=18` builds
   `FROM node:` and fails somewhere else, or succeeds and produces something
   else. Placement is not a preference, it is the correctness condition.
2. **A `FROM` can only use an `ARG` declared before the FIRST `FROM`.**
   Absolute. There is exactly one legal position and no choice to offer.
3. **A global `ARG` is not visible inside a stage until it is re-declared
   there.** `ARG NAME` with no value is Docker's documented way to pull the
   global default in.

**Where the declaration goes.** Immediately **above the instruction that uses
it** — above that instruction's attached comment block, so a comment
documenting a `RUN` keeps documenting it — and, for a value in a `FROM`,
immediately above the **first** `FROM`. Never at the end of the stage. Story
7.6's `InstructionInsertionPoint` is **not** reused: it appends after a stage's
last instruction, which for an `ARG` is the one position guaranteed to be
wrong. The new anchor (`InstructionStartPoint` / `InsertBefore`) shares 7.6's
splice arithmetic — the same `contentEnd`, the same `endingFor`, the same
`checkText` refusals — and differs only in which line it lands on. The reader is
**told** which scope it landed in and why it could not be anywhere else, because
a placement rule the reader cannot see is a placement rule they cannot check.

`InstructionStartPoint` is also the only anchor in that engine that can return
offset **0**, and splicing there would put the new line in front of a UTF-8
**BOM** — the defect that already shipped once in this engine, where a file
reports zero stages while every operation succeeds at nothing. The offset steps
over it and `testdata/edge/e53-bom-crlf-arg.Dockerfile` pins the property.

**Where the literal goes: the `ARG` default, and NOT a `.env`.** `ARG` has a
default and `${VAR}` in compose does not, so unlike story 9.3 the literal can
stay in the file — and it must. `ARG NODE_VERSION` with no default is property 1
wearing a different hat: a bare `docker build .` would produce a different image
and say nothing. Writing the literal to a `.env` instead is the confident wrong
answer in the place it costs most, because `docker compose` passes build
arguments only through `build.args` and a `.env` line would look like
configuration and be inert. **So this operation writes ONE file**, which is the
sharpest contrast with story 9.3 and the reason the two are not one story.

**`build.args` is deliberately NOT wired, and that is a decision rather than an
omission.** Wiring it needs a third file, needs to know which compose service
builds this Dockerfile — a resolution question, not an edit one — and would put
the default in two places that can disagree. The result **says so** instead, in
the reader's own words, and names exactly what to write
(`ExtractArgResult.ComposeNote`).

**Quoting the default: refused, never guessed.** A build-argument default is
read by BuildKit and then, in most instructions, by a shell — two grammars this
engine does not model. Story 9.3 could decide `.env` quoting by **readback**
because `resolve.ParseDotEnv` exists to read it back; there is no second reader
for an `ARG` default, and inventing one would be inventing the grammar it claims
to check. So a value carrying a space, a quote, a `#`, a backslash, a `` ` `` or
a `$` is **refused** with the character named (`ErrArgValue`).

**The other four answers, and each is a test.**

- **Already declared in the scope being written to, with the same default:** not
  a conflict. No second declaration is written, the substitution is made, and
  the result says the declaration was already there — the same idempotence
  story 9.3 buys with its `.env` no-op. **"In the scope being written to" is
  POSITIONAL, not merely same-stage** — corrected 2026-08-15 after an
  adversarial review reproduced the defect. `argsInScope` collected every `ARG`
  in the target's stage, including ones BELOW it, which are not visible to the
  target at all (property 1, read backwards). A stage that re-declared the same
  name further down with the same value therefore reported "already declared",
  no declaration was written above the use, and `argReadback` then refused the
  whole operation with *"nothing declares FOO above instruction N"* — a refusal
  whose stated reason had been manufactured by the planning step immediately
  before it. `argsInScope` now ignores declarations at an index `>= target`;
  `testdata/edge/e56-later-arg-same-stage.Dockerfile` pins it, with a companion
  test on a declaration ABOVE the target so that "ignore what is below" and
  "ignore the whole stage" are not the same test.
- **Already declared with a different default, or with none:** refused
  (`ErrArgConflict`), naming the line and both values. A second declaration
  would shadow the first and the build would use whichever came last. The
  **global** disagreement is a separate arm from the **in-scope** one, and
  `testdata/edge/e52-global-arg.Dockerfile` holds both — a fixture with only
  one cannot tell that the other is missing.
- **A global `ARG` with the same value, and the literal inside a stage:** a bare
  `ARG NAME` is written in that stage, which is Docker's documented way of
  pulling the global default into scope, and the result reports that it
  **re-declared** rather than declared. A default here would shadow the global
  one.
- **A `FROM` pinned by digest, or with no tag:** refused by name (`ErrNoTag`). A
  digest is not a tag, and an absent tag is `:latest` by implication rather than
  a literal anybody wrote.

**The result is READ BACK through the parser before it is written.** The
declaration must exist, the value in scope at the use must be **exactly** the
literal that was taken out, and the declaration must sit at a lower instruction
index than its own use — below the first `FROM` when the use is a `FROM`. A
result that fails the readback is `ErrWouldCorrupt` rather than a write. This
engine does not crash; it returns a confident wrong answer, and `argReadback` is
aimed at hand-built buffers in tests so the guard has been seen to fail.

**That last sentence was only two-thirds true until 2026-08-15.** `argReadback`
has three arms and the tests reached two of them: the buffer aimed at the
"declaration below its own use" arm passed `useIdx=0`, so its scan loop ran zero
times, it exited at the *nothing declares it* arm, and the `requireBeforeFirstFrom`
arm — property 2, the one arm no amount of "it parses" would catch — was never
executed at all. `if false {` around it left the suite green. It is covered now
(`FROM a` / `ARG V=1` / `FROM b:${V}` with `useIdx=2`), and the test asserts the
SAME buffer is accepted with `requireBeforeFirstFrom` false, which is what proves
the third arm did the refusing rather than one of the two above it.

**Nothing here splices.** Both halves go through `edit.run` as ordinary
operations — `set_base_image` or `replace_args`, plus the new
`insert_instruction_before` — so they get the ordinary validate, the ordinary
diff and the ordinary atomic write. `make gate` is untouched by construction.

**One subcommand, dispatching on the FILE.** `composure extract` is one gesture
over two operations, and which one it means is the file's answer, never which
flag was typed. `edit.FileGrammar` is the single classifier both halves ask;
two would eventually disagree and send the reader the paragraph about the file
they are not holding, which this repository has already shipped once (decision
25's ARG-refusal paragraph, fired on `RootIsMapping` alone).

**Consequence accepted.** Decision 25's refusal paragraph said "Composure does not
half-build it", and that sentence is now false. It has been replaced by one
naming the operation that owns the request; the compose half still refuses a
Dockerfile, it just says where to go.

---

## 28. `.env` staleness is a statement about the VARIABLE, and the residue of decision 25 step 3 is not stale

**Decided 2026-08-14**, story 9.6, from an adversarial review of story 9.3 which
found that `edit.Extract` carried **no `Expect` field of any kind**. Every other
staged write in the product was covered by AD-19; the one write path with a
blast radius larger than the file the reader is looking at was not. The visible
symptom: `cmd/composure/serve.go`'s `errors.Is(err, edit.ErrStaleRange)` branch on
`stack/extract-apply` **could not fire**, because `ApplyExtract` had no code
path that returned it. A handler that looked like it participated in the
staleness contract did not. **That branch fires now.**

It was not closed inside the defect pass that found it, and the reason is that
"add an `Expect`" is a design question rather than a missing line.

**Two fields, and they are deliberately different SHAPES.**

- **`Expect`** — the compose half — is an ordinary AD-19 byte range, because the
  compose half is an ordinary `replace_scalar` against an ordinary range.
- **`ExpectEnv`** is `ExpectVar{Defined, Value}` and carries **no range**. The
  `.env` edit is one line appended at the end of a file; there is no range to
  compare. The meaningful assertion is about the **variable**: "this name was
  undefined when I previewed, or defined with exactly this value". **A field
  copied from the single-file contract would compare an offset that means
  nothing here and pass** — the exact shape of a check that cannot fail.
  `Defined` is a separate field from `Value` rather than `Value == ""` meaning
  absence, because `""` is a value a `.env` can legitimately hold.

**Either half moving refuses the whole thing, and the refusal names WHICH.** A
`.env` that changed while the compose file did not is a different situation from
the reverse, and a reader who is told only "something moved" cannot act on it.
The tests for this move **exactly one** of the two files and assert the other is
byte-identical: a test whose two files change together cannot tell which half
the check noticed, and would pass against an implementation that only ever looks
at one of them.

**The two refusals were asymmetric until 2026-08-15.** The `.env` half named its
own file and added "the compose file is not the half that moved". The compose
half came straight out of `edit.run`, which produces a refusal for ONE file and
so names a **config path and no file at all** — the whole answer for `apply`,
half an answer here, where the reader cannot tell which file moved nor whether
the `.env` had already been written. `runExtract` now wraps a compose-half
`ErrStaleRange` with the file's base name and the statement that the `.env` was
not touched and neither file was written. `edit.run`'s own message is left
alone: `apply` writes one file and has nothing to say about a second.

**`-expect-text` on its own is a guard, on `extract` as much as on `apply`.**
Both call sites in `cmd/composure/main.go` set `hasExpect` from the two integer
offsets only, so a caller who asked "write this only if it still reads X" was
answered as though they had asked for no guard at all. `apply`'s was fixed
first; `extract`'s — **the one command that writes two files** — was missed, and
had no test in either direction. It has both now
(`cmd/composure/expecttext_test.go`), and the refusing direction asserts that
the `.env` was not created, because "the compose file was not written" is
satisfied by an implementation that wrote the `.env` first and then refused.
`docs/USER-MANUAL.md` said the flag was ignored on its own; that is corrected.

**The carve-out, which is the whole reason this was a story.** Decision 25 step
3 leaves a designed-inert residue — a `.env` carrying a variable nothing
references — and it is **converging**: re-running the operation finds the name
already set to the same value and treats that as a no-op. That re-run finds a
`.env` that HAS changed since the preview. A naive equality check refuses it,
and refuses the very recovery the window was designed to survive. So one extra
state passes: **previously undefined, now defined as exactly the literal this
request is moving**. It is narrow on purpose — somebody else's value at the same
name is still stale — and `testdata/edge/e54-residue.env` freezes it.

**It refuses as staleness, never as `ErrVarConflict`.** The check runs BEFORE
`assignEnv`. The two look identical on disk and the reader's next move differs:
a conflict is answered by choosing another name, a stale preview by looking
again.

**The unversioned caller is decided explicitly.** A request carrying neither
field is applied against the files as they stand — what a CLI invocation needs.
A silently-mandatory field would break the client that already works.

> **Amended 2026-08-15.** The clause above used to read "and what the extension
> shipped against", and that sentence was the defect rather than a description
> of one. The extension pinned `PROTOCOL_REVISION = 9` — the revision whose
> entire justification is the paragraph below, that a client not sending these
> gets no staleness protection on the widest write in the product — while
> sending neither field, on either half. `expect_env` and `env_expect` appeared
> nowhere in `extension/` at all. So the shipped client was precisely the client
> the bump exists to keep out, standing inside the handshake's own boundary and
> passing it.
>
> It had a second, quieter consequence: `extension/host/panel.ts`'s
> `classify(err) === 'stale'` branch on **both** apply paths was unreachable.
> The core cannot return `ErrStaleRange` for a request that asserted nothing, so
> the branch was decoration — the same defect this decision was written to fix,
> one layer up, and the second time it has appeared in this story.
>
> **Closed by sending them, not by deleting the branches.** Both applies now
> send the assertion recorded from the preview the reader approved:
> `extension/host/edit.ts`'s `expectOf` reads the range and the `before` bytes
> off the preview's own operation for the compose half and for story 9.4's
> Dockerfile half, and `env_expect` travels back verbatim because it is an
> answer about the variable that the client cannot recompute. `expectOf`
> deliberately skips an operation with an EMPTY range — `stack/extract-arg`
> emits its `ARG` declaration as `insert_instruction_before` at `[0, 0)`, and
> asserting that an empty range still holds the empty string is true of every
> file that ever existed. Sending nothing is still legal and still means what
> this decision says it means; it is just no longer what this client does.

**Two contracts moved, and one of them is rendered by the extension.**
`ExtractResult` gained `env_expect`, the assertion a later apply sends back. It
is an **added** field rather than a changed one, so a client rendering that
result keeps rendering it. The compose half needs no equivalent:
`Compose.Ops[0].Range` and `.Before` have been the recorded `Expect` since
AD-19.

**Protocol revision 9.** Every new field is optional, so the wire is additive
and the bump is not about parsing. It is about what a client that does not send
them gets: no staleness protection at all on the widest write in the product,
while every other staged write has had it since AD-19. A client that shows
"this stage is stale, discard it" for a scalar edit and silently overwrites a
colleague's `.env` for a move is exactly the skew the handshake exists to put in
front of us rather than in front of the reader. Story 9.4's two methods —
`stack/extract-arg` and `stack/extract-arg-apply` — ride along in the same
revision; by revision 7's own rule they would not have justified one alone.

---

## Decisions still open

Not decided, and not to be closed by assumption. Carried forward from
requirements §12.

1. **The paid-tier boundary.** Team features — shared stack maps, drift
   detection, generated onboarding docs, policy checks, SSO — are the natural
   line. Confirm with users before building. The *outer* boundary is already
   fixed and is not open: the core stays Apache-2.0 permanently and no
   capability is ever removed from the free tier (README, CLEANROOM.md §10).
2. **Willingness to pay**, rated High in the risk register. The comprehension
   pain is evidenced; that anyone pays for it is not. Ten conversations with
   maintainers beats a month of building.
3. **Corpus scale.** 146 compose files and 179 Dockerfiles today; Q1 wants
   5,000+.
4. **Terraform commitment.** A one-week `hclwrite` spike of the same shape as
   the YAML one, before phase 8 is committed to. R9.4 is the reason: Terraform
   meaning lives in HCL *plus* state, provider schemas and plan output, so the
   compose architecture does not generalise for free.
5. **Replacing goccy in the locate path with `yaml.v3`.** RESULTS.md named this
   as next-step 1 in August. It is now not just cleanup — it is the fix for the
   BOM defect. See RESULTS.md, post-spike section.

## Governance not yet in place

Recorded here because CLEANROOM.md requires it and absence is easier to miss
than a wrong decision.

- **CLA or DCO with relicensing rights, from commit one** (§10). **Half done,
  2026-08-13.** §10 offers the choice but attaches a rider, and the rider is the
  hard part: a **DCO does not grant relicensing rights**. DCO 1.1 certifies
  origin and inbound licence, nothing more. So `CONTRIBUTING.md` and the `dco`
  CI job close the half a machine can check, and the relicensing half is
  **blocked on the legal entity below** — a CLA needs a counterparty to assign
  rights to. Every project that changed licence later could only do so because
  it owned or could relicense all of its code. Also open: commits before
  2026-08-13 predate the job and are unsigned; that work is single-author by
  the copyright holder, so the gap is bounded but real.
- **Trademark search in classes 9 and 42**, US and Saudi Arabia. Not done.
- **Separate legal entity** owning the repositories, CI and domains. Not done,
  and it now carries three things: the CLA above, changing the copyright line
  in `LICENSE` and `extension/LICENSE.txt` (both filled 2026-08-13 as
  `Copyright 2026 Mahmoud Ali ElZouhery`), and writing an assignment for the
  existing commits — renaming a copyright line does not itself move ownership.
- ~~**`DECISIONS.md`**, required by CLEANROOM.md rule 4.~~ This file, created
  2026-08-12.
- ~~**Commercial boundary declared in the README** before the first release.~~
  Done 2026-08-12.
