# AGENTS.md

Guidance for AI coding agents (Claude Code, Cursor, Codex, etc.) and for human contributors using them in this repository.

This file is the single source of truth for "how to contribute here". `CLAUDE.md` exists for tooling that looks for that filename and points to this file.

## Repository

- **Repo:** [`verygoodplugins/whatsapp-mcp`](https://github.com/verygoodplugins/whatsapp-mcp)
- **Origin remote:** always `origin` (this fork). PRs, issues, and `gh` commands target this fork, not the upstream `lharries/whatsapp-mcp`.
- **Default branch:** `main`. All PRs target `main`.
- **Releases:** automated via [release-please](https://github.com/googleapis/release-please) — do **not** hand-edit `CHANGELOG.md` or version numbers.

## Architecture (read first)

Two components, one repo:

```
whatsapp-mcp/
├── whatsapp-bridge/        # Go bridge — talks to WhatsApp Web via whatsmeow
│   ├── main.go             # REST API + event loop
│   ├── webhook.go          # Outgoing webhook for incoming messages
│   └── store/              # SQLite (whatsapp.db, messages.db) + media — gitignored
├── whatsapp-mcp-server/    # Python MCP server — exposes tools to AI clients
│   ├── main.py             # FastMCP tool definitions
│   ├── whatsapp.py         # DB queries + bridge HTTP client
│   └── audio.py            # FFmpeg helpers
└── .github/                # CI, release, security workflows
```

Data flow: AI client → MCP server (Python) → reads SQLite directly **or** calls bridge REST (`http://127.0.0.1:8080/api/*`) → bridge (Go) → WhatsApp Web.

Two SQLite databases:

- `whatsapp.db` — owned by whatsmeow (sessions, contacts, LID map). Treat as opaque.
- `messages.db` — owned by the bridge (chats, messages). Schema is ours.

## Scope rules

Before writing code, check [`ROADMAP.md`](./ROADMAP.md). Anything in "out of scope" should be turned into a polite "won't ship" reply, not a PR.

If unsure whether something is in scope, **open an issue first**. Do not open a PR larger than ~300 LOC without prior discussion.

## PR rules

1. **One concern per PR.** A PR titled "feat: X and also fix Y and refactor Z" gets sent back. Split it.
2. **Conventional commits in the title.** `feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `refactor:`, `test:`, `perf:`. Use `!` (`feat!:`) or `BREAKING CHANGE:` in the body for breaking changes.
3. **Reference an issue** for any `feat:` PR (`Closes #N`). Bug fixes don't strictly require an issue but are easier to review with one.
4. **Update docs in the same PR.** README, `CLAUDE.md`/`AGENTS.md`, or inline tool descriptions if you changed user-visible behavior.
5. **Tests.** Add or update tests for any code you touch in `whatsapp-mcp-server/`. The Go bridge has fewer tests today; matching the existing bar is fine, but don't *remove* coverage.
6. **No drive-by formatting.** Don't reformat files you didn't otherwise change. Keep diffs reviewable.
7. **No new top-level dependencies** without justification in the PR description.
8. **Security-sensitive changes** (auth, file paths, network bind, command exec) get extra scrutiny — call them out in the PR body.

## Local commands

```bash
# Go bridge
cd whatsapp-bridge
go run main.go              # dev
go build -o whatsapp-bridge && ./whatsapp-bridge   # release-ish
golangci-lint run           # lint
go test ./...               # tests (sparse today)

# Python MCP server
cd whatsapp-mcp-server
uv sync --extra dev
uv run main.py              # dev
uv run pytest -v            # tests
uv run ruff check .         # lint
uv run ruff format .        # format
```

## CI gates

Every PR runs (see `.github/workflows/`). Not every job is blocking today:

**Blocking — must be green to merge:**

- `Python Lint` (`ruff check` + `ruff format --check`)
- `Python Tests` (`pytest`)
- `Go Lint` (`golangci-lint`)
- `Go Build`
- `Version Consistency` (Python pkg version vs `.release-please-manifest.json`)
- `CodeQL Analysis (Python | Go)`

**Informational — runs on every PR but won't fail the build today (`continue-on-error: true`):**

- `Bandit Security Scan`
- `Python Dependency Audit` (`pip-audit`)
- `Go Vulnerability Check` (`govulncheck`)

A failing blocking job is a hard block — fix it or explain in the PR why it's unrelated. For informational scans, investigate findings and either fix them or note in the PR why they're acceptable.

## Environment variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `WHATSAPP_DB_PATH` | `../whatsapp-bridge/store/messages.db` | SQLite path used by the MCP server |
| `WHATSAPP_API_URL` | `http://localhost:8080/api` | Bridge REST endpoint |
| `WHATSAPP_BRIDGE_PORT` | `8080` | Port the bridge binds to |
| `WEBHOOK_URL` | `http://localhost:8769/whatsapp/webhook` | Outgoing webhook for incoming messages (empty = disabled) |
| `FORWARD_SELF` | `false` | Whether self-sent messages are forwarded |

When adding a new env var: document it here, in `README.md`, and in `.env.example`.

## Gotchas (read before editing)

1. **JIDs.** WhatsApp identifies users as `1234567890@s.whatsapp.net` (DM), `123456@g.us` (group), and `<random>@lid` (link-ID, anonymous). The bridge maintains a phone↔LID map in `whatsapp.db.whatsmeow_lid_map`. Many "user is missing" / "messages don't show" bugs trace back to JID-form mismatches. Always think about both forms.
2. **Media files** live under `store/{chat_jid}/` with timestamp + message-ID filenames. Don't hand-construct these paths in client code; use the bridge's `/api/download` endpoint.
3. **Audio.** WhatsApp voice messages must be Opus `.ogg`. The MCP server's `send_audio_message` tool auto-converts via FFmpeg if installed.
4. **History sync** is controlled by the *primary* device (the phone). The bridge can request more (see the `--full-history-pair` flag), but the phone has the final word.
5. **`messages.db` is the source of truth for the MCP server.** Don't make the MCP server dependent on the bridge being up for *read* operations.
6. **Outgoing calls are not visible to linked devices.** Don't promise features that depend on them.

## Where to make changes

| You want to… | Touch this file |
|---|---|
| Add or modify an MCP tool | `whatsapp-mcp-server/main.py` |
| Change DB queries / data conversion | `whatsapp-mcp-server/whatsapp.py` |
| Change bridge REST API or event handling | `whatsapp-bridge/main.go` |
| Change webhook payload | `whatsapp-bridge/webhook.go` |
| Change CI behavior | `.github/workflows/*.yml` |
| Change release behavior | `release-please-config.json`, `.release-please-manifest.json` |

## Persona for AI agents working in this repo

- **Be terse.** Don't restate the question.
- **Be decisive.** Pick the smallest change that fixes the problem.
- **Bias to action** for low-risk improvements (lint, tests, error messages, comments that explain *why*).
- **Ask** before architectural changes, dependency additions, or anything in `ROADMAP.md`'s "out of scope".
- **Cite files with `path:line`** when discussing code.
- **Never** edit `CHANGELOG.md`, version constants in `pyproject.toml`/`go.mod`, or `.release-please-manifest.json` directly.

## Reporting bugs / requesting features

- Bugs: use the **Bug report** issue template. Include bridge + MCP server versions, OS, exact reproduction.
- Features: use the **Feature request** template. State the problem, not the solution. Confirm it fits `ROADMAP.md`.

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the human-facing contribution guide.
