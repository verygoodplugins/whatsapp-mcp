# Contributing

Thanks for your interest in `verygoodplugins/whatsapp-mcp`. This fork is small and intentionally narrow — please read [`ROADMAP.md`](./ROADMAP.md) and [`AGENTS.md`](./AGENTS.md) before opening a PR.

## TL;DR

- **Open an issue first** for anything bigger than a clear bug fix.
- **One concern per PR**, conventional-commit title, ≤ ~300 LOC where possible.
- **Required CI checks green** (lint, tests, CodeQL, version consistency); `bandit`, `pip-audit`, and `govulncheck` run as informational security scans — investigate findings even though they don't currently fail the build.
- **Update docs** in the same PR as user-visible changes.

## Setup

```bash
git clone https://github.com/verygoodplugins/whatsapp-mcp.git
cd whatsapp-mcp

# Bridge
cd whatsapp-bridge
go run .           # scan QR to pair

# MCP server (separate terminal)
cd ../whatsapp-mcp-server
uv sync --extra dev
uv run main.py
```

## Workflow

1. **Discuss** — open or comment on an issue. Confirm scope (`ROADMAP.md`).
2. **Branch** — fork or push to a branch named `<type>/<short-slug>`, e.g. `fix/lid-sender-filter`, `feat/typing-indicator`.
3. **Commit** — use [Conventional Commits](https://www.conventionalcommits.org/):
   - `feat:` user-visible feature → minor bump
   - `fix:` user-visible bug fix → patch bump
   - `chore:`, `docs:`, `ci:`, `refactor:`, `test:`, `perf:` → no version bump
   - `feat!:` / `fix!:` / `BREAKING CHANGE:` in body → major bump
4. **Test locally** — `uv run pytest`, `golangci-lint run`, `go build ./...`.
5. **Open a PR** to `main` against `verygoodplugins/whatsapp-mcp`. Use the PR template.
6. **Iterate** — address review comments. Squash isn't required; meaningful commit history is fine.
7. **Merge** — maintainers merge once CI is green and at least one approving review is in. Release-please cuts the next release automatically.

## What gets merged quickly

- Small, focused bug fixes with a clear reproduction.
- Test additions for existing code.
- Documentation improvements.
- Dependency updates (most are auto-handled by Dependabot).
- Reliability fixes for the Go bridge (reconnect, race conditions, JID resolution).

## What needs more discussion

- New MCP tools (need a real-world use case + tool description).
- New env vars or CLI flags.
- Schema changes to `messages.db`.
- Anything touching the webhook payload shape.
- Anything touching authentication or network bind defaults.

## What we'll likely close

- Multi-account / multi-tenant support.
- Plugin systems, extension points.
- Hosted-SaaS or web-UI features.
- Massive refactors with no behavior change.
- Mega-PRs (> ~500 LOC) without a prior issue.

(See `ROADMAP.md` for the full out-of-scope list.)

## Code style

- **Python:** ruff for lint and format. Type hints on public functions.
- **Go:** golangci-lint must pass. Standard `gofmt`. Add comments for non-obvious behavior, not for "what the code does".
- **Tests:** pytest in Python, standard `testing` in Go.

## Security

If you find a vulnerability, **please don't open a public issue**. Email security@verygoodplugins.com (or open a private security advisory on GitHub) with details.

## Credit

If a contribution is non-trivial, your name lands in the release notes (auto-generated). Significant ongoing contributors may be invited as repo collaborators.

Thanks for keeping this project small, sharp, and useful.
