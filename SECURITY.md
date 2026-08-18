# Security

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub's private vulnerability reporting:
the **Security** tab → **Report a vulnerability**. That opens a private thread
visible only to the maintainers.

You will get an acknowledgement within 72 hours and an assessment within 7
days. If the report is valid, you will be told when a fix ships and credited in
the release notes unless you ask not to be.

This is a single-maintainer project. There is no on-call rotation and no paid
support contract, and it is better to say so than to imply a response time
nobody is staffed to meet.

## What is in scope

Composure reads configuration files and writes edits back to them. The failure
modes that matter are the ones where the product's own guarantees do not hold:

- **Writing bytes the user did not ask for.** The edit path splices bytes at a
  located range and never re-serialises the document. Anything that gets it to
  corrupt a file, damage a region outside the edit, or silently drop content is
  a security issue, not merely a bug — the file being edited is often the one
  that deploys production.
- **Reading outside the workspace.** `include:`, `extends:` and `env_file:` all
  take paths from the file being read. A crafted compose file that makes the
  core read or write outside the workspace root is in scope.
- **Command execution.** The core spawns `docker compose` for version
  detection. Anything that turns file content into an argument or a shell
  string is in scope.
- **Leaking secrets.** The inspector displays resolved values, including ones
  interpolated from `.env`. Anything that writes a resolved secret somewhere it
  was not displayed — a log, a temp file, a crash dump, telemetry — is in
  scope.
- **The extension host boundary.** The webview renders untrusted file content.
  Anything that escapes it into the extension host is in scope.
- **Denial of service against the editor.** The engine is expected to survive
  hostile input: a file on disk must not be able to take the editor down. An
  input causing unbounded recursion, unbounded memory, or a hang is in scope,
  because `recover` cannot catch stack exhaustion in Go and the crash takes the
  editor with it.

## What is not in scope

- Composure not detecting an insecure Docker configuration. It reports what a
  file says; it is not a security scanner, and a missed finding is a feature
  request.
- Vulnerabilities in Docker, in the Compose specification, or in an image
  Composure names.
- Anything requiring the attacker to already have write access to the machine
  running the editor.

## Supported versions

The latest published release. This project has not reached 1.0 and there are no
maintained release branches; a fix ships in the next version.

## Dependencies

The core links exactly two third-party Go modules, both permissively licensed
and listed in `NOTICE`. The extension has no runtime npm dependencies — the
package is four files plus the Go binary. This is a deliberate constraint, not
an accident: `make licence` fails the build on a dependency outside
MIT / Apache-2.0 / BSD / ISC, and the smaller the graph, the less of it can go
wrong.
