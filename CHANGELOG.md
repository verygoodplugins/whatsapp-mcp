# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0](https://github.com/verygoodplugins/whatsapp-mcp/compare/v0.1.0...v0.2.0) (2026-04-22)


### Features

* add image media support in webhook forwarding ([#45](https://github.com/verygoodplugins/whatsapp-mcp/issues/45)) ([c5f409c](https://github.com/verygoodplugins/whatsapp-mcp/commit/c5f409c78da8b7029b418e2fa428316dad7f970b))
* **bridge:** --full-history-pair flag to request full history at pair time ([#37](https://github.com/verygoodplugins/whatsapp-mcp/issues/37)) ([2358dd5](https://github.com/verygoodplugins/whatsapp-mcp/commit/2358dd5cf8cda17110076446c73637eab23bc7c3))
* **bridge:** capture incoming WhatsApp call events ([#39](https://github.com/verygoodplugins/whatsapp-mcp/issues/39)) ([197a0c9](https://github.com/verygoodplugins/whatsapp-mcp/commit/197a0c9de304b762db3e3a7fb98d9dd4ce510f74))


### Bug Fixes

* **bridge:** auto-download runs after StoreMessage to avoid lookup race ([#41](https://github.com/verygoodplugins/whatsapp-mcp/issues/41)) ([0c267f5](https://github.com/verygoodplugins/whatsapp-mcp/commit/0c267f5f0834138f54d7e2d731ffbd8f8deb5929))
* **bridge:** handle StreamReplaced event to recover from session conflicts ([#27](https://github.com/verygoodplugins/whatsapp-mcp/issues/27)) ([0cd6475](https://github.com/verygoodplugins/whatsapp-mcp/commit/0cd647560705acbbb3d46b236e1e09df7b20ceee))
* **bridge:** include message ID in media filenames to prevent same-second collisions ([#40](https://github.com/verygoodplugins/whatsapp-mcp/issues/40)) ([1e819c1](https://github.com/verygoodplugins/whatsapp-mcp/commit/1e819c1d67d7ed087ab3298253f4f987cd3e48e9))
* **bridge:** resolve [@lid](https://github.com/lid) sender to phone JID in webhook payload ([#56](https://github.com/verygoodplugins/whatsapp-mcp/issues/56)) ([746fca0](https://github.com/verygoodplugins/whatsapp-mcp/commit/746fca03297438f12b53ad3df4d33a6200773a05))
* **bridge:** surface image/video/document captions in extractTextContent ([#42](https://github.com/verygoodplugins/whatsapp-mcp/issues/42)) ([fbb3f28](https://github.com/verygoodplugins/whatsapp-mcp/commit/fbb3f283f0296a5f7e4aaf72eec2b99853de41b8))
* **mcp:** match messages by both phone number and LID via whatsmeow_lid_map ([#43](https://github.com/verygoodplugins/whatsapp-mcp/issues/43)) ([04c8755](https://github.com/verygoodplugins/whatsapp-mcp/commit/04c875568424a125b3e824ba094427f8d899d7c3))
* **mcp:** resolve contacts via whatsmeow store with LID → phone fallback ([#30](https://github.com/verygoodplugins/whatsapp-mcp/issues/30)) ([b9b0175](https://github.com/verygoodplugins/whatsapp-mcp/commit/b9b0175e6475d5402feedc190f44758045985992))
* pin anyio&lt;4.9 to avoid cancel scope regression ([#44](https://github.com/verygoodplugins/whatsapp-mcp/issues/44)) ([627db67](https://github.com/verygoodplugins/whatsapp-mcp/commit/627db6746076108ba4ca370ff3f95420ccbb30ef))
* security hardening for LAN exposure and Unicode search ([#55](https://github.com/verygoodplugins/whatsapp-mcp/issues/55)) ([8097f39](https://github.com/verygoodplugins/whatsapp-mcp/commit/8097f39dd4a19edd2ebab52d086704b528019005))


### Documentation

* add ROADMAP, AGENTS, CONTRIBUTING, CODEOWNERS, issue/PR templates ([#47](https://github.com/verygoodplugins/whatsapp-mcp/issues/47)) ([c2e4f5b](https://github.com/verygoodplugins/whatsapp-mcp/commit/c2e4f5b22cc3de42b4850222d3cc3ebfa4efbd14))
* document WHATSMEOW_DB_PATH env var ([#51](https://github.com/verygoodplugins/whatsapp-mcp/issues/51)) ([c24d151](https://github.com/verygoodplugins/whatsapp-mcp/commit/c24d151c9ebe07c052fa1e3a3662c10795747280))
* update remaining "go run main.go" references to "go run ." ([#50](https://github.com/verygoodplugins/whatsapp-mcp/issues/50)) ([53e1ca7](https://github.com/verygoodplugins/whatsapp-mcp/commit/53e1ca7dd2a470fd5f947f22ee667753cf7e6be6))

## 0.1.0 (2026-03-02)

### Added

**Go Bridge:**

- `/api/typing` endpoint - Send typing indicators to chats
- `/api/health` endpoint - Check WhatsApp connection status
- Webhook system for incoming messages (`webhook.go`)
  - Configurable via `WEBHOOK_URL` environment variable
  - Includes quoted message info (reply context)
  - `FORWARD_SELF` option to include self-sent messages
- Auto-download of media files when messages arrive
- Quoted message extraction (reply-to context: ID, sender, content)
- Connection status checks before API operations
- HTTP server timeouts for stability (read: 30s, write: 60s, idle: 120s)

**Python MCP Server:**

- `get_contact` tool - Resolve phone number to contact name
- `sender_display` field in messages - Shows "Name (phone)" format
- `sender_phone` field - Extracted phone number from JID
- Environment variable configuration (`WHATSAPP_DB_PATH`, `WHATSAPP_API_URL`)
- Test suite with pytest (8 tests)
- `sort_by` parameter for `list_messages` ("newest" or "oldest")
- Increased default limits (20 → 50 messages/chats)
- Maximum limits (500 messages, 200 chats)

**Infrastructure:**

- CI/CD pipeline with GitHub Actions (lint, build, test)
- Release Please workflow for automated release PRs/tags/changelog (`.github/workflows/release-please.yml`)
- Manual fallback release workflow for artifact re-publish (`.github/workflows/release.yml`)
- Version consistency check across `pyproject.toml`, `server.json`, and release tags
- Comprehensive documentation (README, CLAUDE.md)
- Maintainer release playbook (`docs/RELEASING.md`)
- Environment variable examples (`.env.example`)

### Fixed

- Go compilation errors from whatsmeow API changes (added `context.Background()`)
- Media filename consistency - now uses message timestamp instead of download time
- Startup migration now consolidates legacy `@lid` chat/message rows into mapped phone JIDs (`@s.whatsapp.net`) to prevent split chat history
- golangci.yml configuration (removed deprecated linters)
- Python linting issues (185 errors fixed)
- Trailing whitespace in SQL queries

### Changed

- Upgraded whatsmeow to latest version (v0.0.0-20260107124630)
- Upgraded Go linting pipeline to golangci-lint v2 (CI action + v2 config schema)
- Modernized Python type hints (`Optional[str]` → `str | None`)
- Improved docstrings with date format examples
- Media download uses message timestamp for consistent filenames
- Refactored `send_message` to use unified `recipient` parameter
- Adopted Release Please for automated versioning/changelog ([`#15`](https://github.com/verygoodplugins/whatsapp-mcp/issues/15)) ([6d45958](https://github.com/verygoodplugins/whatsapp-mcp/commit/6d45958139effa3079ff27a9708d400f89ba9ddf))

### Removed

- Unused `package.json` from Go bridge (was for wrangler, not needed)
- Compiled binary from git tracking

---

## Fork History

This project is a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp), originally created by [Luke Harries](https://github.com/lharries) in 2024.

The original repository was last updated in April 2025. This fork by [Very Good Plugins](https://verygoodplugins.com) continues active development with bug fixes, new features, and MCP registry publication.
