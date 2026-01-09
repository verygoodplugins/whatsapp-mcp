# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Contact names included in message responses (not just JIDs)
- `sort_order` parameter for `list_messages` (newest_first/oldest_first)
- Increased default and maximum message limits
- CI/CD pipeline with GitHub Actions (lint, build, test)
- Comprehensive documentation (README, CLAUDE.md)
- Environment variable examples (.env.example)
- Python test suite with pytest

### Fixed

- Go compilation errors from whatsmeow API changes (context.Background() parameters)
- golangci.yml configuration (removed deprecated linters)
- Python linting issues (185 errors fixed)
- Trailing whitespace in SQL queries

### Changed

- Upgraded whatsmeow to latest version (v0.0.0-20260107124630)
- Modernized Python code style (ruff format)
- Improved error handling in audio conversion

### Removed

- Unused package.json from Go bridge (was for wrangler, not needed)

---

## Fork History

This project is a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp), originally created by [Luke Harries](https://github.com/lharries) in 2024.

The original repository was last updated in April 2025. This fork by [Very Good Plugins](https://verygoodplugins.com) continues active development with bug fixes, new features, and MCP registry publication.
