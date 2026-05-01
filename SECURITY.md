# Security Policy

## Supported Versions

Security fixes ship on the latest minor release. Older minors are not patched.

| Version | Supported          |
| ------- | ------------------ |
| 0.2.x   | :white_check_mark: |
| < 0.2   | :x:                |

## Reporting a Vulnerability

Please report vulnerabilities **privately** — do not open a public issue, PR, or discussion.

**Preferred:** Use GitHub's [private vulnerability reporting](https://github.com/verygoodplugins/whatsapp-mcp/security/advisories/new) on this repository. This creates a draft Security Advisory visible only to maintainers and you, and lets us collaborate on a fix in a private fork before disclosure.

**Alternative:** Email `security@verygoodplugins.com` with details.

When reporting, please include where possible:

- A description of the issue and its impact
- Affected versions
- Steps to reproduce or a proof of concept
- Any suggested mitigations

## What to Expect

- **Acknowledgment** within 72 hours of receipt
- **Initial triage and severity assessment** within 7 days
- **Fix and disclosure** for confirmed issues, typically within 30 days for high/critical severity, longer for lower-severity issues with mitigations
- A draft Security Advisory created on this repo, with you invited as a collaborator on the private fork if you'd like to participate in the fix
- A CVE requested through GitHub when the issue warrants one
- Credit in the published advisory and release notes (unless you'd prefer to remain anonymous)

If you don't hear back within 72 hours, please re-send — this is a solo-maintained project and occasional travel happens.

## Scope and Threat Model

The threat model assumes the human user of the host is trusted, but **does not** assume every process running on that host is trusted. In MCP environments, sibling MCP servers, IDE extensions, and tool-triggered flows can act as effective callers — issues that allow such callers to abuse the bridge are in scope.

**In scope:**

- The `whatsapp-bridge` Go binary and its REST/HTTP surface
- The `whatsapp-mcp-server` Python MCP server
- Published Docker images and release artifacts
- Documentation that materially affects security posture (e.g. install or configuration instructions)

**Out of scope:**

- WhatsApp itself, the WhatsApp Web protocol, or `whatsmeow` upstream (please report those upstream)
- Third-party MCP clients consuming this server
- Social engineering, physical attacks, or attacks requiring root/admin compromise of the host
- Denial of service via brute request volume
- Issues that require the user to deliberately install untrusted code outside this project's release artifacts

## Disclosure Policy

We follow coordinated disclosure. Once a fix is available and released, the Security Advisory is published and credit is given to the reporter. Disclosure dates are coordinated with the reporter where reasonable.

## Acknowledgments

Researchers who responsibly disclose vulnerabilities are credited here once the corresponding fix has shipped. Thanks to everyone who keeps this project safer.
