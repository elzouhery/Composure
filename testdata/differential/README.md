# Differential fixtures — multi-file projects

The corpus is 146 single compose files and **zero** multi-file projects: a
`find` for `compose.override.y*ml` and `docker-compose.override.y*ml` across all
of `corpus-repos/` returns nothing. So a differential harness pointed only at
the corpus compares every project against itself and never once exercises a
merge — which is the thing stories 1.4 and 1.6 built and the thing an oracle
exists to prove.

These fixtures are the merge coverage. Each directory is one project:

- a directory with a `CHAIN` file is an **explicit `-f` chain**, one path per
  line, merged left to right on both sides;
- a directory without one is the **automatic pickup**: `compose.yaml` plus
  `compose.override.yaml`, which is what `docker compose` does with no `-f` at
  all and what `resolve.Dir` does.

They are deliberately small and deliberately about the merge table: scalars that
replace, `command` that replaces rather than appends, mappings that merge
key-wise, and the list-shaped attributes where the question "does this dedupe or
does it append" has an answer only Compose can settle.

## What these fixtures found

`dedupe-rows/` is the direct question put to the oracle: a review-fix pass
deleted 19 rows from the merge table on the grounds that §13 says sequences simply
append, and there was no multi-file evidence either way because nothing in the
repository merged two files. There is now. Compose's own answer, from
`docker compose config` on these fixtures:

| deleted row | what Compose actually does |
|---|---|
| `cap_add`, `cap_drop`, `dns`, `dns_opt`, `dns_search`, `expose`, `tmpfs`, `links`, `profiles`, `devices` | **deduplicates** — the repeat appears once |
| `security_opt`, `group_add`, `volumes_from` | **rejects the document**: `items at 0 and 1 are equal` |
| `build.platforms`, `build.cache_from`, `deploy.placement.constraints`, `deploy.placement.preferences` | appends — the repeat appears twice |
| `build.tags` | deduplicates |
| `env_file` | not exercised here; Compose folds it into `environment` before this is observable |

So the deletion was right for four of them and wrong for the rest. The three in
the middle row are the sharp ones: a merged model that carries the duplicate is
not merely different from Compose's, it is a document Compose refuses to load.

## What was done about it

The merge table now encodes the oracle's answer, and every restored row is
labelled with the oracle rather than with a spec clause — see the provenance
notes in `internal/resolve/mergerules.go`, none of which say "§13:".

- The ten that deduplicate were restored as `appendUnique` on whole-value
  equality, plus `devices`, which the oracle showed is keyed on the **target**:
  `/dev/ttyS0:/dev/x` and `/dev/sda:/dev/x` come back as one device.
- `tmpfs` is keyed on the whole entry deliberately. `/tmp:size=64m` against
  `/tmp` is a Compose *error* (`target /tmp already mounted`), not a merge, so
  keying it on the path would pick a winner Compose never picks.
- `security_opt`, `group_add` and `volumes_from` still **append**, because that
  is what Compose does — it concatenates and then validates. The repeat is kept
  and reported as a `repeated-list-item` finding. Deduplicating them would have
  resolved, silently, a project `docker compose` refuses to load.
  `compose-rejects-duplicate/` is that case as a fixture; it lands in the
  harness's "oracle refused" bucket by design, and exists to be re-run by hand.
- `build.platforms`, `build.cache_from` and the two `deploy.placement.*` lists
  stay deleted: the oracle confirms Compose appends them.

`devices` and `build.tags` are not in `compareService`'s projection — Compose
renders devices in long form and `build` is not compared — so those two are
carried by unit tests in `internal/resolve/merge_test.go` rather than by a
fixture that would compare nothing.

## Expected divergences — the `DIVERGES` file

Everything above pairs **matching forms**: a list against a list, a mapping
against a mapping. That made the harness's 100% a true number about a set that
carefully excluded the one place this project and Compose are known to disagree.

When a collection is written as a **list in one file and a mapping in the
other** — `environment: [BASE_A=1]` in the base, `environment: {SHARED: over}`
in the override — Compose normalises both spellings to a mapping before merging
and keeps `BASE_A`. Composure replaces the collection whole, so `BASE_A` is not in
the resolved model. Verified against the oracle, in both directions:

| | `services.web.environment` |
|---|---|
| `docker compose config` | `BASE_A=1 SHARED=over` |
| composure | `SHARED=over` |

**The drop is deliberate and is not a bug to be fixed here.** The resolved model
keeps the shape each file wrote, because the splice engine edits those bytes; a
model showing a mapping where the file has a list would send an edit to a range
that is not there. What *was* a bug is that the loss was silent outside
`composure resolve -json` — it is now a `merge-form-mismatch` finding on the
`resolve` table, in `explain`, and as a `diagnose` rule.

So the fixture cannot assert agreement, and it must not be left out. A directory
may carry a `DIVERGES` file naming the config paths it is **expected** to
disagree about, one per line. The harness then asserts the disagreement **still
happens, in exactly those places**, and fails if it changes:

- it starts **agreeing** → failure. The design changed, or the merge quietly
  started normalising forms, and the register is now a lie.
- it diverges **somewhere new** → failure. The check is equality, not
  containment, so a documented divergence cannot become a hiding place for a
  real regression.

A registered project is reported in its own section, printed in full, and
excluded from **both** the pass rate and the multi-file figure — it is not an
agreement, and it is not a merge failure either. A reader must not be able to
mistake "known and documented" for "agrees".

`cross-form-environment/` and `cross-form-environment-reversed/` are the two
directions of this case, and they are the only fixtures in the repository that
demonstrate a genuine divergence from the oracle.
