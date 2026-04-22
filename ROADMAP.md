# Roadmap

This document defines what this fork (`verygoodplugins/whatsapp-mcp`) optimizes for, what it explicitly does **not** want to become, and how to propose changes that fit.

> If your idea isn't on the "in scope" list, that doesn't mean it's bad — it usually means it belongs in a downstream fork or a separate companion project.

## North star

A small, **stable, observable, single-account** WhatsApp ↔ MCP bridge that "just works" for AI clients (Claude Desktop, Cursor, etc.) on a developer laptop or a single self-hosted server.

The fork is consumer-grade infra, not a SaaS platform.

## In scope

These areas accept new PRs and issues. Bugfixes here are always welcome.

### Bridge stability (`whatsapp-bridge/`)

- Reconnect/recover from `Disconnected`, `StreamReplaced`, `ConnectFailure`, `StreamError`.
- LID/phone JID resolution correctness (incoming and outgoing).
- Media handling: download race safety, filename collisions, MIME types, captions.
- Pair-time configuration (e.g. history sync window) — additive, opt-in flags only.
- Event coverage for message types whatsmeow already exposes (text, media, captions, reactions, edits, deletions, calls).

### MCP correctness (`whatsapp-mcp-server/`)

- Tool input validation and clear error messages (e.g. RFC 3339 timestamps).
- Search correctness across Unicode (CJK, Arabic, Devanagari, etc.).
- Contact resolution from the whatsmeow store.
- Tool descriptions tuned for agent ergonomics.
- Streaming/large result handling for `list_messages` and friends.

### Observability & ops

- Structured logging in the bridge.
- Health endpoints, version endpoints.
- Webhook reliability (retries, opt-in payload shape changes).
- Documentation: README, `CLAUDE.md`, `AGENTS.md`, troubleshooting guides.

### Security

- Bind defaults that fail safe (localhost first).
- Auditing of file/path inputs.
- Dependency hygiene (Dependabot, govulncheck, CodeQL, bandit).

### CI / release

- Reproducible builds.
- Conventional commits + release-please.
- Lint, test, security scans on every PR.

## Out of scope

These are not goals of this fork. PRs implementing them will likely be closed with a polite redirect.

- **Multi-account / multi-tenant.** One process, one paired device. Run multiple instances if you need multiple accounts.
- **Hosted SaaS / multi-user web UI.**
- **Plugin / extension system** inside the bridge or MCP server.
- **Reimplementing whatsmeow** primitives or vendored forks of it.
- **Bots, automations, or canned responses** beyond what an MCP client can already do via the existing tools.
- **Outbound call origination, video calling, payments, statuses, or channels.**
- **Cloud-specific deployment artifacts** (Helm charts, Terraform modules, vendor-specific CI). A simple `Dockerfile` is acceptable; full deployment kits are not.
- **Large refactors with no behavior change** (e.g. switching frameworks, restructuring directories) without prior discussion.

## How to propose something larger

1. **Open an issue first** describing the problem you're solving and the smallest change that solves it. Don't open a PR with > ~300 LOC of new code without discussion.
2. **Reference this roadmap** in the issue. Say which "in scope" bucket the work fits in (or argue why it should be added).
3. **Split big work into atomic PRs.** One concern per PR, each independently reviewable and revertible.
4. **Expect "no" sometimes.** A small maintained surface is worth more than a large unmaintained one.

## Release cadence

- Patch releases ship as fixes land via release-please.
- Minor releases are cut when a meaningful set of features lands (target: roughly monthly, demand-driven).
- No fixed schedule — quality over cadence.

## Versioning

We use [Semantic Versioning](https://semver.org/) and [Conventional Commits](https://www.conventionalcommits.org/) via release-please:

- `feat:` → minor bump
- `fix:` → patch bump
- `feat!:` / `fix!:` / `BREAKING CHANGE:` → major bump
- `chore:`, `docs:`, `ci:`, `test:`, `refactor:` → no bump

Pre-1.0 (where we are now), breaking changes can land in minor releases but should still be called out explicitly in the conventional commit footer and the PR description so release-please captures them in the generated `CHANGELOG.md` (which is auto-managed — don't hand-edit it).
