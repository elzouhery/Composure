# Clean-room protocol

This project is licensed **Apache-2.0** and must remain free of third-party
code that carries incompatible terms. Dockhand (BSL 1.1) is used as a
**requirements reference only**. Its source is not, and must never be, an input
to this implementation.

BSL 1.1 termination is automatic and total — a single violation ends your rights
to *every* version, retroactively. The cost of being careful here is very low.
The cost of being careless is the whole project.

## Rules

1. **No copying, adaptation, or transcription** of BSL-licensed source. Not code,
   not data structures, not file layouts, not identifier names.
2. **Separate the roles.** Anyone who has read Dockhand's source may write
   *requirements* — "the product needs a graph editor that can attach a volume
   to a service" — but must not write the corresponding implementation. Whoever
   implements works from the requirement, not the reference.
3. **Requirements must be behavioural, not structural.** Describe what a user can
   do and what the output must be. Never describe how the reference achieves it.
4. **Log the provenance of every non-trivial design decision** in `DECISIONS.md`:
   what was decided, why, and what informed it. If a decision was informed by
   observing a competitor's *behaviour* (not its code), say so explicitly.
5. **Dependencies must be permissive** — MIT, Apache-2.0, BSD, ISC. No BSL, SSPL,
   Elastic License, or AGPL in the dependency tree. Run a licence scan in CI and
   fail the build on violations.
6. **CLA or DCO with relicensing rights from every contributor, from commit one.**
   Every project that successfully changed licence later could only do so because
   it owned or could relicense all of its code. Without this you are frozen.

## Why Apache-2.0 rather than MIT

Apache-2.0 carries an explicit patent grant and explicit trademark language.
MIT has neither. For a project intended to carry a commercial tier later, the
patent grant is worth the extra paragraphs.

## Commercial boundary — declare it now, in the README

Lens and Insomnia were both forked within months of restricting something users
already had. Relicensing permissive → restrictive has triggered a community fork
three times out of three (Terraform → OpenTofu, Redis → Valkey, Elastic →
OpenSearch). The core stays Apache-2.0 permanently. Paid features are *additive*
and organisational — never a capability removed from the free tier.

State this in the README before the first release, and never move the line.

## Corporate separation

Given the GRC platform precedent — built for an employer, owned by the employer:

- Separate legal entity before the first commit.
- Repositories, CI, cloud accounts and domains owned by that entity.
- Contributors engaged by that entity under written IP-assignment agreements.
  Not staff seconded from another company on that company's time.
- No employer client data, brand assets, or proprietary material anywhere in the
  repository or its test corpus.
- Read your employment contract for assignment and non-compete clauses, and get
  a written carve-out signed before work starts.

*Not legal advice. Have a lawyer review the entity structure and the contributor
agreement before money is committed.*
