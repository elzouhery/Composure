# Vendored schema provenance

`compose-spec.json` is the Compose Specification JSON Schema, vendored verbatim.

| | |
| --- | --- |
| Upstream | https://github.com/compose-spec/compose-spec |
| Path | `schema/compose-spec.json` |
| Commit | `4e2fe7602af8c965ab4fef891e9dde9c5940775f` |
| Retrieved | 2026-08-12 |
| Licence | Apache-2.0 |
| Bytes | 76664 |

Fetched with:

```
curl -o schema/compose-spec.json \
  https://raw.githubusercontent.com/compose-spec/compose-spec/4e2fe7602af8c965ab4fef891e9dde9c5940775f/schema/compose-spec.json
```

The same commit's `LICENSE` is Apache-2.0, which CLEANROOM.md rule 5 permits.
`make licence` walks the **Go build graph** and resolves each module to a
licence file in the module cache; a vendored data file is not a module and the
scan cannot see it, so this record — not the gate — is what makes the licence
of these bytes auditable. Bumping the schema means bumping the commit here, in
`embed.go`, and the file, in one commit.

## Why it is vendored rather than fetched

AD-20: the `available, not set` list is the product's differentiator, and it is
generated from this file at runtime. A network fetch would make the inspector's
contents depend on connectivity, and a hand-written list would fall behind the
specification within a release and start lying about what is possible. Pinning
means the list is reproducible and its age is a fact anyone can read off this
table.

## `compose-min-version.json`

Not upstream. The Compose Specification carries no version information — it is
deliberately one current unified document, which is why the file's own
`version:` field must never select a schema (AD-20). `compose-min-version.json`
is the separate, explicitly partial record AD-21 needs: where a key's minimum
Compose is known, the inspector marks keys newer than the installed binary as
unsupported-here instead of hiding them. A key absent from that file is offered
with no mark, so an incomplete record costs an annotation and never a key.
