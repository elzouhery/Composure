# Composure — development targets.
#
# Package patterns are ./cmd/... ./internal/... rather than ./... throughout.
# The fetched corpus lives inside the module directory and contains thousands
# of unrelated third-party Go files; ./... tries to build them and fails.

PKGS    := ./cmd/... ./internal/...
CORPUS  := corpus-repos

## The extension carries a platform binary and picks it at activation, so the
## build output is filed under Go's own GOOS-GOARCH name.
GOOS    := $(shell go env GOOS)
GOARCH  := $(shell go env GOARCH)
GOEXE   := $(shell go env GOEXE)
EXTBIN  := extension/bin/$(GOOS)-$(GOARCH)/composure$(GOEXE)

## The platform matrix the extension ships, named in Go's own GOOS-GOARCH.
##
## These directory names are not cosmetic — they are the selection key.
## `goTarget()` in extension/host/core.ts maps process.platform/process.arch
## onto exactly this spelling and joins it to `bin/`, so a name that drifts
## here is a "no core for this platform" banner on someone else's machine
## rather than a build error on ours. extension/host/packaging.test.ts asserts
## the two lists agree.
CORE_TARGETS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64

## VS Code's own target names, which are a different vocabulary — x64 not
## amd64, win32 not windows — mapped to the Go target whose binary they carry.
## alpine-* reuse the linux binaries: CGO is off, so they are static and run
## against musl unchanged.
VSCE_TARGETS := darwin-arm64:darwin-arm64 \
                darwin-x64:darwin-amd64 \
                linux-x64:linux-amd64 \
                linux-arm64:linux-arm64 \
                win32-x64:windows-amd64 \
                alpine-x64:linux-amd64 \
                alpine-arm64:linux-arm64

.PHONY: publish publish-ovsx all build fmt vet test check corpus fidelity editbench structbench dockerbench enginebench differential bench gate baseline licence clean extension extension-core extension-cores extension-web extension-test package package-clean

all: check

build:
	go build -o bin/fidelity ./cmd/fidelity
	go build $(PKGS)

## fmt — the formatting check. `gofmt -l` prints every file whose formatting
## differs from canonical and exits 0 regardless, so a bare invocation is not a
## check; the guard below turns the printed list into a failure. This target
## exists because nine files had drifted while gofmt lived only in an editor
## setting: a check that runs on one machine is not a check.
##
## Directories, not $(PKGS): the fetched corpus lives inside the module and
## `gofmt ./...` would walk thousands of unrelated third-party files.
fmt:
	@out=$$(gofmt -l ./cmd ./internal ./schema); \
	if [ -n "$$out" ]; then \
		echo "gofmt: these files are not formatted:"; \
		echo "$$out" | sed 's/^/  /'; \
		echo "run: gofmt -w ./cmd ./internal ./schema"; \
		exit 1; \
	fi
	@echo "gofmt: ./cmd ./internal ./schema clean"

vet:
	go vet $(PKGS)

test:
	go test $(PKGS)

## check — everything CI runs, minus the corpus fetch.
check: fmt build vet test licence gate

## corpus — rebuild the ~2.1GB of shallow clones. Fetched, never committed.
corpus:
	go run ./cmd/fidelity fetch $(CORPUS)

## The benchmarks, individually. These are the acceptance criteria for
## every story, not a smoke test — see CLAUDE.md "Definition of done".
fidelity:
	go run ./cmd/fidelity check $(CORPUS)

editbench:
	go run ./cmd/editbench $(CORPUS)

structbench:
	go run ./cmd/structbench $(CORPUS)

dockerbench:
	go run ./cmd/dockerbench $(CORPUS)

## enginebench — the four engines the four benchmarks above never touch:
## internal/resolve, internal/topology, internal/diagnose, internal/edit.
##
## The four above all measure the SPLICE ENGINES, which is right — they are
## what makes this product different — but it left everything built on top of
## them with no gated number at all. Provenance could come off every leaf
## value, every graph node could lose the position that makes it clickable, and
## `make gate` would stay green through both. That is retro action item X4.
##
## Daemon-free, hermetic, and a pure function of the corpus and the code — the
## same bar every other gated metric meets, and the bar the differential cannot
## meet. See the note above `gate`.
enginebench:
	go run ./cmd/enginebench $(CORPUS)

## differential — story 1.5: the resolved model compared against
## `docker compose config`, for SEMANTIC equivalence, over the corpus AND over
## the multi-file projects in testdata/differential.
##
## The fixtures are not optional. The corpus is 146 single files and contains
## zero override files, so a corpus-only run compares every project against
## itself and never exercises a merge — which is the one thing this harness
## exists to check. -min-multifile is what makes that failure loud: if the
## fixtures are deleted, renamed, or stop resolving, this target fails instead
## of reporting a higher pass rate over less.
##
## THE PASS RATE WAS BLIND, AND A `DIVERGES` FILE IS WHAT FIXED IT.
##
## Every fixture paired MATCHING forms — a list against a list, a mapping
## against a mapping — so 100% was a true number about a set that carefully
## excluded the one place this engine and Compose are known to disagree. When
## `environment` (or `depends_on`, or any collection) is a LIST in one file and
## a MAPPING in the other, Compose normalises both spellings and keeps the base
## file's entries; composure replaces the collection whole and loses them. That is
## deliberate — the resolved model keeps the shape each file wrote, because the
## splice engine edits those bytes — but it is a real divergence, and a harness
## that never showed it was measuring around it.
##
## testdata/differential/cross-form-environment{,-reversed} are that case, in
## both directions. Adding them plainly would make this target permanently RED,
## and a red harness is one nobody reads; deleting them would put the blindness
## back. So a fixture may carry a `DIVERGES` file: an EXPECTED-DIVERGENCE
## REGISTER naming the config paths it is known to disagree about. The harness
## then asserts the DISAGREEMENT STILL HAPPENS, in exactly those places, and
## fails if it changes in either direction — a fixture that starts agreeing is
## as much a signal as one that starts diverging somewhere new. A register is
## not a suppression: it cannot hide a second divergence behind a documented
## one, because the check is equality and not containment.
##
## A registered project is reported in its own bucket, printed in full, and
## excluded from BOTH the pass rate and the multi-file figure. Not counted as a
## pass, because it is not one; not counted as a failed merge, because that
## would read as a merge bug. The one outcome this must never produce is a
## reader mistaking "known and documented" for "agrees" — which is exactly what
## the old 100% invited.
##
## STILL NOT in benchmarks/baseline.json, and the reasoning has changed rather
## than gone away. See `gate`'s note below.
##
## THE FLOORS, AND WHY THEY ARE TWO DIFFERENT NUMBERS.
##
## `-min-pass` was left unset here for a stated reason: the multi-file rate was
## 66.67% because of real divergences in the merge table, and a floor at the
## current number would record a bug as the expected result. Those divergences
## are fixed — 80 of 80 compared projects agree, 6 of 6 merges agree, and the
## two cross-form projects that genuinely disagree are in the DIVERGES register
## and excluded from both figures by design.
##
## So the floor goes in, because the deferral outlived its reason and what was
## left was a target that could not fail: a reviewer broke the
## `services.*.ports` comparison, watched the pass rate collapse, and watched
## this target exit 0. Twice. The same mutation now exits non-zero at both
## floors below — 10.00% of 80 compared and 3 of 6 merges.
##
## It is two floors because the two rates are measured over different things.
##
##  -min-multifile-pass 100. The multi-file figure is over testdata/differential
##     and nothing else. Those fixtures are committed, tiny and ours; the corpus
##     contributes no merge at all. There is no drift for a floor to absorb, so
##     anything under 100 is a merge that changed, and the only honest floor is
##     the whole number. Together with -min-multifile 6 it says both halves:
##     six merges were compared, and all six agreed.
##
##  -min-pass 97. The headline rate is mostly the 74 corpus files, and those DO
##     move without this repository moving — the oracle canonicalises
##     differently between Compose releases, which is the same machine-dependence
##     that keeps this out of the gate entirely. 97% of 80 is a two-project
##     allowance: enough that one release-note change in Compose's output does
##     not stop a laptop, and far too little to sit through a real regression.
##     The mutation this was verified against — the ports comparison broken on
##     purpose — puts it at 10.00%, which is not a close call.
##
## Neither floor replaces the checks that are always on and have no flag: a
## project registered in a DIVERGES file that stops diverging, or starts
## diverging somewhere else, fails this target on its own.
##
## It takes minutes: it forks the CLI once per project. Run it when the merge
## changes.
differential:
	go run ./cmd/differential -min-multifile 6 -min-multifile-pass 100 -min-pass 97 $(CORPUS)

bench: fidelity editbench structbench dockerbench enginebench

## gate — run every benchmark and fail on any regression against the committed
## baseline. This is what CI enforces.
##
## If this fails, the change is wrong. Not the baseline.
##
## WHY THE DIFFERENTIAL PASS RATE IS NOT ONE OF THESE METRICS.
##
## Story 1.5's acceptance criterion asked for it, and it was not ducked — it
## was refused, and the criterion has since been amended to say so. (An earlier
## version of this comment attributed "wire it into the gate ONLY if it is
## stable" to the story; no such text was ever in epics.md, and a quotation
## nobody can find is worse than no quotation.) Two things stop it:
##
##  1. It needs a Docker daemon. Every other gated metric is a pure function of
##     the corpus and the code. This one is a function of the machine, and of
##     which Compose version is on it — the oracle's canonicalisation changes
##     between releases, so the same commit measures differently on two
##     runners. A metric that moves when nothing in this repository moved
##     cannot be compared against a committed number.
##
##  2. A baselined metric that skips is a gate that passes VACUOUSLY. `gate`
##     runs on machines without Docker — the build job, and every laptop — and
##     the only two options there are to fail the whole gate on a machine fact
##     or to record a skip as a pass. Neither is a gate.
##
## So what is enforced instead is the thing that CAN be enforced honestly, and
## it is the failure that actually happened here: `make differential` now
## fails, loudly and with its own exit code, when it compares no multi-file
## project or when Docker is absent (`-allow-skip` is the opt-in, and it has to
## be typed). The harness reported "100% of 74 compared projects" for a run in
## which the merge was never once exercised; -min-multifile is what makes that
## specific lie impossible to tell again.
##
## `-min-pass` and `-min-multifile-pass` are the floors on the rates
## themselves, and they ARE set in the target above now that the merge-table
## divergences they were waiting on are fixed. The reasoning for each number is
## with them. A floor there is not a substitute for a baselined metric — it is
## the strongest thing a machine-dependent measurement can honestly carry.
##
## AND WHAT STORY 1.5's THIRD CRITERION GETS INSTEAD.
##
## It asks for a gated number over this engine, and the reasoning above refuses
## the differential's. So the number comes from `enginebench`, which measures
## the same four engines the differential does — resolve, topology, diagnose,
## edit — over the same corpus, without a daemon: how much of the corpus
## resolves, whether every leaf value still carries the {file, line, column}
## R1.8 is written in, whether every graph node can say where it was declared,
## whether every finding's anchor lands on a byte that exists, and whether a
## preview still equals its own apply. Those are pure functions of the corpus
## and the code, so they can be compared against a committed number on any
## machine, which is the whole property the differential lacks.
gate:
	go run ./cmd/gate -corpus $(CORPUS)

## baseline — rewrite benchmarks/baseline.json from the current measurement.
## Only for a genuine improvement, and the diff must be argued for in the PR.
baseline:
	go run ./cmd/gate -corpus $(CORPUS) -update

## licence — CLEANROOM.md rule 5: no BSL, SSPL, Elastic License or AGPL.
licence:
	go run ./cmd/licencescan -v

## extension — the VS Code extension: the core binary for this machine, plus
## the TypeScript bundles. Deliberately not part of `check`: `check` is the Go
## gate, and it must not start depending on a Node toolchain being present.
extension: extension-core extension-web

## extension-core — the binary the extension spawns, where activation looks
## for it. Never committed; extension/bin/ is gitignored.
extension-core:
	mkdir -p extension/bin/$(GOOS)-$(GOARCH)
	go build -o $(EXTBIN) ./cmd/composure

## extension-cores — every platform binary the extension may need, filed under
## the name activation looks for. This is what `make package` consumes.
##
## CGO_ENABLED=0 is load-bearing rather than tidy: it makes each binary static,
## which is what lets one linux build serve glibc and musl alike (the alpine-*
## VSIX targets carry it unchanged) and what keeps cross-compilation working at
## all without a C toolchain per target. -trimpath strips local paths out of
## the binary; -s -w drop the symbol and DWARF tables, which is roughly a third
## of the size for a tool nobody debugs from a marketplace install.
extension-cores:
	@for t in $(CORE_TARGETS); do \
	  goos=$${t%-*}; goarch=$${t##*-}; exe=composure; \
	  if [ "$$goos" = windows ]; then exe=composure.exe; fi; \
	  mkdir -p extension/bin/$$t || exit 1; \
	  echo "  building $$t"; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
	    go build -trimpath -ldflags '-s -w' -o extension/bin/$$t/$$exe ./cmd/composure || exit 1; \
	done
	@echo
	@ls -l extension/bin/*/composure extension/bin/*/composure.exe 2>/dev/null

## package — the marketplace artefacts: one platform-specific .vsix per target,
## each carrying exactly one core binary.
##
## vsce supports platform-specific packaging (`--target`), and it is worth
## using: an all-platforms VSIX would carry five binaries so that every install
## downloads four it can never execute. The per-target ignore file is generated
## rather than committed — five near-identical ignore files differing by one
## line is a set that drifts, and the one that drifts silently ships someone
## else's binary or none at all.
package: extension-cores extension-web
	@mkdir -p extension/build
	@rm -f extension/build/*.vsix
	@for pair in $(VSCE_TARGETS); do \
	  vt=$${pair%%:*}; gt=$${pair##*:}; \
	  ign=extension/build/.vscodeignore.$$vt; \
	  cp extension/.vscodeignore $$ign || exit 1; \
	  printf '\n# Generated by `make package`. Every core except this target'\''s.\nbin/**\n!bin/%s/**\n' "$$gt" >> $$ign; \
	  ( cd extension && npx --no-install vsce package \
	      --target $$vt \
	      --ignoreFile build/.vscodeignore.$$vt \
	      --out build/composure-$$vt.vsix ) || exit 1; \
	done
	@echo
	@ls -lh extension/build/*.vsix

## publish — push every platform build to the VS Code Marketplace.
##
## This is the one target in this file that is not reversible. A version number
## can never be reused on the marketplace: publishing 0.1.0 and finding a defect
## means 0.1.1, not a re-push. So it refuses unless VSCE_PAT is set AND the
## packages already exist, rather than helpfully rebuilding and shipping
## whatever happens to be in the tree.
##
## Before the first run:
##   1. a publisher whose ID is exactly the `publisher` field in
##      extension/package.json must exist at
##      https://marketplace.visualstudio.com/manage
##   2. VSCE_PAT must hold an Azure DevOps token with Marketplace > Manage,
##      scoped to ALL accessible organizations — a token scoped to one
##      organisation authenticates and then fails at the upload
##
## Each VSIX is published against its own --target, because a platform-specific
## extension is seven separate uploads sharing one version; vsce reads the
## target from the package.
publish:
	@test -n "$$VSCE_PAT" || { echo "publish: VSCE_PAT is not set"; exit 1; }
	@ls extension/build/*.vsix >/dev/null 2>&1 || { echo "publish: no packages — run 'make package' first"; exit 1; }
	@echo "About to publish version $$(node -p "require('./extension/package.json').version") as publisher $$(node -p "require('./extension/package.json').publisher")."
	@echo "A published version can never be reused. Ctrl-C now if that is not what you want."
	@sleep 5
	@for f in extension/build/*.vsix; do \
	  echo "  publishing $$f"; \
	  ( cd extension && npx --no-install vsce publish --packagePath "../$$f" --pat "$$VSCE_PAT" ) || exit 1; \
	done
	@echo
	@echo "Published. The listing takes a few minutes to appear."

## publish-ovsx — the same seven packages to the Open VSX registry.
##
## Cursor, Windsurf, VSCodium, Gitpod and Coder cannot reach the Microsoft
## marketplace at all, and this extension's README tells those readers it works
## for them. Until this target has run, that sentence is false for every one of
## them, which is why this is not an optional second channel.
##
## Irreversible on the same terms as `publish`: a version is spent the moment it
## lands. It differs in one way — Open VSX maintains its own accepted target
## list, so a single refused TARGET must not abandon the uploads queued behind
## it. Refusals are collected and the target fails at the END, naming them, so
## a partial publish is reported rather than hidden.
##
## Before the first run:
##   1. an eclipse.org account whose GitHub username matches yours EXACTLY, with
##      the Eclipse Publisher Agreement signed at https://open-vsx.org
##   2. the namespace created:  npx ovsx create-namespace elzouhery -p $$OVSX_PAT
##   3. OVSX_PAT holding a token from open-vsx.org Settings > Access Tokens
##
## A successful upload is NOT a successful publish. Open VSX has answered a
## publish with success and left the version inactive and therefore invisible
## — it did exactly that on 0.1.0, accepting seven targets and serving one. So
## this target does not trust its own exit code: it asks the registry which
## targets are actually installable and fails if any is missing.
##
## Creating a namespace does NOT verify it. Until ownership is granted through
## an issue at https://github.com/EclipseFdn/open-vsx.org/issues the listing
## carries an unverified-publisher warning, and the badge clears on the next
## version published after the grant.
publish-ovsx:
	@test -n "$$OVSX_PAT" || { echo "publish-ovsx: OVSX_PAT is not set"; exit 1; }
	@ls extension/build/*.vsix >/dev/null 2>&1 || { echo "publish-ovsx: no packages — run 'make package' first"; exit 1; }
	@echo "About to publish version $$(node -p "require('./extension/package.json').version") to Open VSX as namespace $$(node -p "require('./extension/package.json').publisher")."
	@echo "A published version can never be reused. Ctrl-C now if that is not what you want."
	@sleep 5
	@refused=""; for f in extension/build/*.vsix; do \
	  echo "  publishing $$f"; \
	  ( cd extension && npx --no-install ovsx publish "../$$f" -p "$$OVSX_PAT" ) \
	    || refused="$$refused $$f"; \
	done; \
	if [ -n "$$refused" ]; then \
	  echo; echo "publish-ovsx: REFUSED:$$refused"; \
	  echo "Every other target published. Do not bump the version to retry these."; \
	  exit 1; \
	fi
	@echo
	@echo "Accepted by the registry. Verifying every target is ACTIVE — acceptance is not visibility."
	@sleep 20
	@node extension/verify-ovsx.mjs
	@echo
	@echo "Published to Open VSX. https://open-vsx.org/extension/elzouhery/composure"

package-clean:
	rm -rf extension/build

## extension-web — type-check and bundle. Zero runtime npm dependencies: the
## licence gate walks the Go build graph only and cannot see node_modules, so
## the only safe size for that tree is empty.
##
## `npm ci` rather than `npm install`: the lockfile is committed, and install
## may resolve a different tree than the one that was reviewed. Same command
## here as in CI, so a green local build and a green CI build mean the same
## thing.
extension-web:
	npm --prefix extension ci
	npm --prefix extension run compile

## extension-test — the extension suite. It needs the core binary: the
## real-binary contract test skips without one, and that test is the only thing
## that catches a protocol revision bumped on one side only.
##
## COMPOSURE_REQUIRE_CORE turns that skip into a failure. This rule builds the core
## immediately above, so the binary can only be missing here if the build
## produced nothing — and a run that reports green with the one cross-language
## check silently skipped is exactly the hole this variable closes. CI sets its
## own `CI` variable, which the suite honours the same way.
extension-test: extension-core
	COMPOSURE_REQUIRE_CORE=1 npm --prefix extension test

clean:
	rm -rf bin extension/dist extension/dist-test extension/bin extension/build
