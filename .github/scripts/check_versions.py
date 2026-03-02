#!/usr/bin/env python3
"""Validate project version consistency for release and CI checks."""

from __future__ import annotations

import argparse
import json
import re
import sys
import tomllib
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def read_pyproject_version() -> str:
    pyproject_path = ROOT / "whatsapp-mcp-server" / "pyproject.toml"
    data = tomllib.loads(pyproject_path.read_text(encoding="utf-8"))
    return data["project"]["version"]


def read_server_json_versions() -> tuple[str, str]:
    server_json_path = ROOT / "server.json"
    data = json.loads(server_json_path.read_text(encoding="utf-8"))

    server_version = data["version"]
    package_version = None
    for pkg in data.get("packages", []):
        if pkg.get("registryType") == "pypi" and pkg.get("identifier") == "whatsapp-mcp-server":
            package_version = pkg.get("version")
            break

    if not package_version:
        raise ValueError("server.json is missing the whatsapp-mcp-server PyPI package version")

    return server_version, package_version


def normalize_tag(tag: str) -> str:
    value = tag.strip()
    if value.startswith("refs/tags/"):
        value = value[len("refs/tags/") :]

    if not re.fullmatch(r"v\d+\.\d+\.\d+", value):
        raise ValueError(f"tag must match vMAJOR.MINOR.PATCH, got: {tag!r}")

    return value[1:]


def main() -> int:
    parser = argparse.ArgumentParser(description="Check version consistency across release files.")
    parser.add_argument("--tag", help="Release tag in vMAJOR.MINOR.PATCH format", default="")
    args = parser.parse_args()

    pyproject_version = read_pyproject_version()
    server_version, package_version = read_server_json_versions()

    errors: list[str] = []
    if pyproject_version != server_version:
        errors.append(
            f"Version mismatch: whatsapp-mcp-server/pyproject.toml={pyproject_version} "
            f"!= server.json.version={server_version}"
        )
    if pyproject_version != package_version:
        errors.append(
            f"Version mismatch: whatsapp-mcp-server/pyproject.toml={pyproject_version} "
            f"!= server.json.packages[whatsapp-mcp-server].version={package_version}"
        )

    tag = args.tag.strip()
    if tag:
        tag_version = normalize_tag(tag)
        if pyproject_version != tag_version:
            errors.append(
                f"Version mismatch: tag={tag_version} != whatsapp-mcp-server/pyproject.toml={pyproject_version}"
            )

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1

    print(
        "Version check passed: "
        f"pyproject={pyproject_version}, server.json={server_version}, package={package_version}"
        + (f", tag={normalize_tag(tag)}" if tag else "")
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
