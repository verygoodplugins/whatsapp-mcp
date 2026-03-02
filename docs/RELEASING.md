# Releasing (Maintainers)

This fork uses tag-driven releases and version consistency checks in CI.

## 1) Prepare release commit

1. Update `CHANGELOG.md`:
   - Move relevant entries from `Unreleased` into a new version section.
2. Bump version in both files:
   - `whatsapp-mcp-server/pyproject.toml` (`project.version`)
   - `server.json` (`version` and `packages[].version` for `whatsapp-mcp-server`)
3. Ensure PR CI is green.

## 2) Tag the release

Use semantic version tags in the format `vMAJOR.MINOR.PATCH`.

```bash
git checkout main
git pull --ff-only origin main
git tag v0.1.0
git push origin v0.1.0
```

## 3) What CI does on tag push

Workflow: `.github/workflows/release.yml`

- Validates versions are in sync (and match tag)
- Verifies Go lint config + runs lint/tests/build
- Runs Python tests
- Builds artifacts:
  - `whatsapp-bridge-linux-amd64`
  - Python wheel + sdist for `whatsapp-mcp-server`
  - `SHA256SUMS.txt`
- Creates a GitHub release and uploads artifacts

## 4) Optional: manual validation without publishing

Run the release workflow manually (`workflow_dispatch`) from GitHub Actions.
You can pass `tag` to validate a candidate version format and file consistency
without pushing a tag.
