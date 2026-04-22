<!--
Thanks for the PR! A couple of quick checks before you submit:

- Read ROADMAP.md to confirm scope.
- One concern per PR. Split anything bigger.
- Conventional-commit title (feat/fix/chore/docs/ci/refactor/test/perf).
-->

## Summary

<!-- What does this PR do, and why? 1-3 bullets is plenty. -->

-

## Type of change

- [ ] `fix` — bug fix
- [ ] `feat` — new feature
- [ ] `chore` / `docs` / `ci` / `refactor` / `test` / `perf`
- [ ] Breaking change (`!` in commit, or `BREAKING CHANGE:` in body)

## Scope check

- [ ] This change fits the [`ROADMAP.md`](https://github.com/verygoodplugins/whatsapp-mcp/blob/main/ROADMAP.md) "in scope" list, **or** I've opened an issue first to discuss
- [ ] PR is focused on one concern (split if not)
- [ ] PR is ≤ ~300 LOC, **or** justified in the description

## Linked issues

<!-- "Closes #N" / "Refs #N" -->

## Testing

<!-- How did you verify this? Manual steps, new tests, screenshots/logs as needed. -->

- [ ] Added or updated tests
- [ ] Ran `uv run pytest -v` (Python changes)
- [ ] Ran `golangci-lint run` and `go build ./...` (Go changes)
- [ ] Manually exercised the affected code path

## Docs

- [ ] Updated `README.md` (if user-visible)
- [ ] Updated `AGENTS.md` / `CLAUDE.md` (if contributor-visible)
- [ ] Updated tool descriptions in `whatsapp-mcp-server/main.py` (if MCP tools changed)
- [ ] Updated `.env.example` (if env vars changed)

## Risk / rollback

<!-- Anything reviewers should worry about? How do we revert if this misbehaves? -->
