# Releasing (Maintainers)

This fork uses Release Please for version/changelog automation and release tags.

## 1) Ongoing Development

Use conventional commits on `main` (`feat:`, `fix:`, etc.).
Workflow `.github/workflows/release-please.yml` continuously updates a Release PR.

When the Release PR is merged, Release Please will:
- update `CHANGELOG.md`
- bump versions in:
  - `whatsapp-mcp-server/pyproject.toml`
  - `server.json` (`version` and package version)
- create the release tag (`vMAJOR.MINOR.PATCH`)
- create the GitHub release

## 2) First Fork Release Version Choice

If you need to force a specific first version (for example `0.1.0`), create a
commit with a `Release-As` footer:

```text
chore: bootstrap first fork release

Release-As: 0.1.0
```

Push that commit to `main`; Release Please will honor the requested version in
the generated Release PR.

## 3) Artifact Publishing

In the same workflow, after a release is created, a publish job builds and
uploads release artifacts to the created GitHub release:
- `whatsapp-bridge-linux-amd64`
- Python wheel + sdist for `whatsapp-mcp-server`
- `SHA256SUMS.txt`

## 4) Manual Fallback (Rare)

Workflow `.github/workflows/release.yml` is a manual fallback only
(`workflow_dispatch`) for rebuilding/re-uploading artifacts for an existing tag.
Use it when the automated publish step needs a rerun.
