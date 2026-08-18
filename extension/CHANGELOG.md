# Changelog

All notable changes to the Composure extension are recorded here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Phases 1 and 2 are built and this version has never been published. The entry
below describes what a first release would contain, not something anyone has
installed.

### Added

**Read the stack.**

- A graph of the resolved project, opened beside a compose file. Services,
  networks, volumes, configs, secrets, published ports, and the Dockerfile that
  builds each image.
- `depends_on` in both the short and long form, with the condition
  (`service_started`, `service_healthy`, `service_completed_successfully`)
  written on the edge.
- Layered layout so dependency order reads top to bottom; pan, wheel zoom and
  drag. Node positions are per-workspace view state and never enter a file.
- Search with the rest dimmed, collapse by network or profile, and a focus mode
  that dims everything outside one service's blast radius. Measured at 0.20ms
  per keystroke over a 771-node graph.
- Moving the cursor in the YAML moves the selection in the graph.
- A profile toggle per profile the project declares, taken from the core's own
  answer. The chosen set is what the graph, the diagnostics and the blast radius
  are all computed under; a node that exists in both sets keeps its position
  across a toggle; the panel says in words when what is on screen is filtered;
  and the set is per-workspace view state that never enters a file. Top-level
  networks, volumes, configs and secrets are never filtered, so a filtered stack
  can show a resource with nothing attached to it. Selecting a service and then
  filtering it out says so, rather than showing the stack under its name, and
  the blast radius is recomputed for the set now on screen rather than left
  standing from the one before it.
- Keyboard navigation throughout, and both light and dark themes.

**Resolution with provenance.**

- Multi-file merge: `compose.override.yaml` picked up automatically, explicit
  `-f` chains, `include:` followed recursively, and `extends:` across files —
  with per-key Compose merge semantics, `!reset` and `!override`.
- Interpolation from `.env`, every `env_file` and the environment, covering
  `${VAR}`, `${VAR:-default}`, `${VAR-default}`, `${VAR:+alt}`, `${VAR:?err}`
  and `$$`.
- Anchors, aliases and merge keys expanded for display and left exactly as
  written in the file.
- Every resolved value carries `{file, line, column, merge step}`. The
  inspector shows it as `compose.yml:12 · overrides :7`, and clicking it moves
  the cursor to the file and line that set the value.

**The inspector.**

- Every declared value, never a bare key, with its provenance underneath.
- `available, not set` — every key the Compose specification permits that this
  service does not declare, generated at runtime from the vendored
  `compose-spec.json`, never from a hand-written list.
- Keys newer than the `docker compose` found on the machine are marked as
  unsupported here rather than hidden.

**Diagnostics**, inline against the field that caused them and published to VS
Code's problems panel: host port collisions across the merged configuration, a
`service_healthy` dependency on a service with no healthcheck, circular
`depends_on`, declared-but-unused volumes and networks, variables referenced
and defined nowhere, unreachable services, and credentials in plain
`environment:` blocks.

**The edit path.**

- Change a scalar and see the diff before anything is written. `Save to <file>`
  names the file it will write.
- Edits are applied by splicing bytes at a located range, never by
  re-serialising the document: comments, blank lines, indentation, quoting
  style, key order, flow style, anchors, CRLF and a UTF-8 BOM all survive
  unchanged, by construction rather than by effort.
- An operation that cannot be performed safely is refused with a typed error
  and changes nothing. A flow-style mapping cannot take a block child, so the
  engine returns `ErrFlowStyle` instead of writing invalid YAML.
- A Dockerfile stage form, reached by clicking the Dockerfile node.

**Packaging.**

- Platform-specific builds for macOS (Apple silicon and Intel), Linux (x64 and
  arm64, glibc and musl) and Windows x64. The Go core ships inside the
  extension and is spawned as a subprocess over JSON-RPC on stdio; there are no
  runtime npm dependencies.

### Known limitations

- Base-image age is shown; per-layer size and the cache-break point are not.
  Those are the builder's answers, not the file's, and reporting them from a
  file alone would be a number with nothing behind it.
- Structural *delete* is not offered from the pane, except for one entry of a
  list. Removing a service or a key is a scope call rather than a missing
  capability — the engine can do it, and the CLI does.
- Below 900px the toolbar wraps past the height the design pass pinned. Which
  controls collapse at that width is an open design question.
- Contrast is guarded by a test that refuses colour pairings VS Code never
  promised, not by measuring a rendered pixel. The residue that needs a human
  eye is listed in `TESTING.md`.
