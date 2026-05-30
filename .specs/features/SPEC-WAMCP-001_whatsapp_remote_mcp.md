---
id: SPEC-WAMCP-001
title: "WhatsApp Remote-MCP Server for HITL Mobile Agents"
status: "Approved"
owner: "hitl-whatsapp-mcp"
created_by: "cos"
approved_by: "ceo"
products: ["hitl-whatsapp-mcp"]
last_updated: 2026-05-30
mission: "MISSION-2026-389"
depends_on: []
---

# 1. Executive Summary

Fork `verygoodplugins/whatsapp-mcp` (MIT) into a HITL Empire satellite and turn
it into a **secure, remote MCP server** so mobile agents in the **hitl-app** can
read, search, and send a user's WhatsApp messages — the WhatsApp analogue of how
`wacli` serves `openclaw`/`hermes` desktop agents — with setup that is as easy
and as safe (Tailscale-only + auth) as possible for non-technical users.

# 2. CEO Business Outcomes

*Primary success criteria, verified by CRITIC alongside acceptance criteria.*

- [ ] **BO1 — Phone reads WhatsApp:** From the hitl-app, a connected agent can
  list/search recent WhatsApp chats and read messages for the logged-in user.
- [ ] **BO2 — Phone sends WhatsApp:** From the hitl-app, the agent can send a
  WhatsApp message to a contact/chat the user selects.
- [ ] **BO3 — 15-minute setup:** A non-developer can self-host the server (QR
  login + Tailscale + one run command) in ≤15 minutes following the guide.
- [ ] **BO4 — Secure by default:** The remote server is unreachable from the
  public internet by default (tailnet-only) and rejects every unauthenticated
  request; it fails closed if exposed without auth.
- [ ] **BO5 — Privacy preserved:** WhatsApp message history stays on the user's
  own device/host; nothing is synced to empire servers.

# 3. User Stories

- [ ] As a hitl-app user, I want to connect my self-hosted WhatsApp MCP server
  by pasting a URL + token, so my mobile agent can act on my WhatsApp.
- [ ] As a self-hoster, I want a copy-paste guide (QR login, Tailscale, run),
  so I don't need to understand MCP internals.
- [ ] As a security-conscious user, I want the server reachable only over my
  private tailnet and protected by a token, so my chats aren't exposed.
- [ ] As an operator, I want the server to refuse to start in an unsafe config
  (public bind, no auth), so I can't accidentally leak my messages.

# 4. Technical Implementation

## 4.1 Architecture

```
hitl-app (mobile agent)
        │  MCP over streamable-http/SSE  (bearer token)
        ▼
[ Tailscale tailnet ]  ◄── private, TLS by Tailscale, no public Funnel by default
        ▼
WhatsApp MCP Server (our fork)
   ├─ Python MCP server  ── auth middleware (FastMCP/FastAPI bearer/API-key)
   │        │ local REST (127.0.0.1:8080, bearer)
   │        ▼
   └─ Go whatsmeow bridge ── WhatsApp Web multidevice (QR login, ~20d re-auth)
            │
            ▼
        local SQLite (messages/contacts/calls) — never leaves host
```

- **Base:** fork of `verygoodplugins/whatsapp-mcp` (MIT), retaining attribution.
- **Remote transport:** adopt **PR #112** (`WHATSAPP_MCP_TRANSPORT` =
  stdio|http|sse, `WHATSAPP_MCP_HOST`/`PORT`) — prefer cherry-pick over reimpl.
  **Fallback (PR-112 contingency):** PR #112 is OPEN/unmerged upstream; if it
  diverges or conflicts on cherry-pick, reimplement the env-driven transport
  selection directly against the fork's current entry point (same env-var
  contract below).
- **Auth (our addition):** bearer token / API key required on every request when
  transport != stdio; mirror `hitl-cli`'s FastMCP + bearer pattern. Use latest
  FastAPI/Starlette security utilities (`HTTPBearer`/`APIKeyHeader`, DI auth).
- **Network posture (documented default — P1-5):** the default is **`tailscale
  serve`** — the MCP server binds **loopback** (`127.0.0.1`) and Tailscale
  proxies tailnet traffic to it over TLS. Directly binding the tailnet
  interface IP is a supported-but-non-default advanced option (a *non-loopback*
  bind, which the fail-closed guard then requires auth for). Public Funnel is an
  explicit, documented opt-in only.
- **Fail-closed guard (P1-6):** the process refuses to start when **either**
  (a) it is bound to a non-loopback address with auth disabled, **or** (b) auth
  is enabled but the token is missing, empty, or below the minimum-entropy bar
  (e.g. a default/placeholder like `test`/`changeme`, or < 32 chars). The setup
  flow generates a strong random secret so users never hand-pick a weak one.

### 4.1.1 Configuration env-var contract (P1-2)

*Authoritative names/defaults for the transport + auth layer. Workers and the
verification plan (§6) MUST use exactly these.*

| Variable | Default | Accepted values | Notes |
|---|---|---|---|
| `WHATSAPP_MCP_TRANSPORT` | `stdio` | `stdio` \| `http` \| `sse` | From PR-112. |
| `WHATSAPP_MCP_HOST` | `127.0.0.1` | IP / hostname | From PR-112. Non-loopback ⇒ guard requires auth. |
| `WHATSAPP_MCP_PORT` | `8089` | port | MCP listener port. MUST differ from the Go bridge REST port (127.0.0.1:8080); 8089 matches §6.2. |
| `WHATSAPP_MCP_AUTH` | `on` | `on` \| `off` | `off` is **only legal on a loopback bind**; with any non-loopback host the guard exits non-zero. |
| `WHATSAPP_MCP_TOKEN` | *(none)* | string ≥ 32 chars, non-trivial | Required whenever `WHATSAPP_MCP_AUTH=on`. Validated as bearer/API-key on every non-stdio request. Generated by the setup flow. |

- When `WHATSAPP_MCP_TRANSPORT=stdio`, auth/host/port are ignored (local pipe).
- A missing/weak `WHATSAPP_MCP_TOKEN` while `WHATSAPP_MCP_AUTH=on` is a
  fail-closed start error (see guard above), not a silent no-auth fallback.

## 4.2 Components

1. **Forked repo `slaser79/hitl-whatsapp-mcp`** bootstrapped per
   `prompts/BOOTSTRAP.md` (.specs scaffolding, CLAUDE.md, CI, webhook to HQ).
2. **Transport layer** — PR-112 env-driven stdio/http/sse selection.
3. **Auth middleware** — token validation + fail-closed safety guard.
4. **Tailscale integration + run scripts** — `tailscale serve` helper / compose.
5. **Setup guides** — QR login, Tailscale, one-command run, hitl-app registration.
6. **hitl-app client integration** — register remote MCP server (URL + token).
   *(Tracked as a cross-repo follow-up against `hitl-app`; see §7.)*

## 4.3 Edge Cases & Failure Modes (P1-1)

*Each failure must surface clearly to the hitl-app user rather than hang or fail
silently. These behaviors are owned by the implementation phases (P-IMPL-1..3).*

| # | Condition | Expected behavior & user-visible surfacing |
|---|---|---|
| EC1 | **WhatsApp session expired** (whatsmeow ~20-day re-auth lapses) | MCP tool calls return a structured, typed error (e.g. `whatsapp_session_expired`) — NOT a generic 500. The hitl-app shows "WhatsApp needs re-linking" and the SETUP guide documents the re-auth step (re-run the QR-login command on the host, rescan). Server stays up; no crash loop. |
| EC2 | **Go whatsmeow bridge down/crashed** while the Python MCP server is up | MCP server detects the bridge is unreachable (local REST health probe) and returns a typed `bridge_unavailable` error to the agent; it does not 500 or hang. Optional: the run script supervises/restarts the bridge. The hitl-app surfaces "WhatsApp service unavailable, retrying." |
| EC3 | **Tailscale not running / not authed** at start | The run script detects tailnet is down (no `tailscale serve` / interface) and fails fast with a clear, actionable message ("Tailscale is not connected — run `tailscale up`") rather than silently falling back to a public/loopback-only bind that the user thinks is remote. |
| EC4 | **Auth misconfig** (weak/missing token, or `auth=off` on non-loopback) | Fail-closed guard (§4.1) exits non-zero at startup with a precise message naming the offending var — covered by AC4. |
| EC5 | **Valid token, transport up, but agent sends to unknown/invalid chat** | Send tool returns a typed `chat_not_found` / validation error; no partial send, no crash. |

# 5. Acceptance Criteria

- [ ] AC1: Repo `slaser79/hitl-whatsapp-mcp` exists, MIT license + upstream
  attribution preserved, registered in HQ `config/empire.yaml`, BOOTSTRAP.md
  scaffolding present, CI green on `main`, webhook to HQ configured.
- [ ] AC2: Server starts in `http` (streamable-http) and `sse` transports via
  `WHATSAPP_MCP_TRANSPORT`; stdio still works (regression-safe).
- [ ] AC3: Every non-stdio request without a valid bearer token / API key is
  rejected with 401; a valid token is accepted.
- [ ] AC4: Fail-closed guard (maps to BO4, P1-5/P1-6): the process exits
  non-zero with a clear, var-naming message when **either** (a) it is bound to a
  non-loopback address with `WHATSAPP_MCP_AUTH=off`, **or** (b)
  `WHATSAPP_MCP_AUTH=on` but `WHATSAPP_MCP_TOKEN` is missing, empty, or weak
  (< 32 chars / known placeholder). Loopback + `auth=off` is permitted.
- [ ] AC5: With the default `tailscale serve` posture (server on loopback,
  Tailscale proxying), the MCP endpoint is reachable from a second tailnet
  device and NOT from the public internet.
- [ ] AC6: `SETUP.md` guide lets a fresh user QR-login + run + connect in
  documented, copy-paste, verified steps — and a timed dry-run of that
  walkthrough completes in **≤15 minutes** (maps to BO3), recorded in the
  verification notes.
- [ ] AC7: Core MCP tools (list chats, search/read messages, send message,
  get_contact) work end-to-end over the remote transport with auth.
- [ ] AC8: Unit tests cover transport selection, auth accept/reject, the
  fail-closed guard (both arms of AC4), and the typed failure-mode errors of §4.3.
- [ ] AC9: Privacy / local-only (maps to BO5, P1-3): the server performs **no
  outbound network egress to empire infrastructure**, and the message/contact
  SQLite DB path resolves to a local-only location on the host. Verified by an
  egress check (no connections to empire domains during a tool exercise) and a
  config assertion on the DB path.
- [ ] AC10: Failure-mode handling (maps to §4.3, P1-1): an expired WhatsApp
  session (EC1), a downed whatsmeow bridge (EC2), and a missing Tailscale
  connection (EC3) each produce the defined typed error / fail-fast message
  instead of a generic 500, hang, or silent fallback.

# 6. Verification Plan

## 6.1 Automated Tests
```bash
# In the satellite repo
uv run pytest -v                      # transport selection, auth, fail-closed guard
uv run ruff check .                   # lint clean
```

## 6.2 Verification Script
```bash
# Start in http transport on loopback with auth, exercise auth gate
export WHATSAPP_MCP_TRANSPORT=http WHATSAPP_MCP_HOST=127.0.0.1 WHATSAPP_MCP_PORT=8089
export WHATSAPP_MCP_AUTH=on WHATSAPP_MCP_TOKEN="$(openssl rand -hex 24)"  # ≥32 chars

# 1) request without token -> expect HTTP 401
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8089/mcp)
test "$code" = "401"

# 2) request with valid token -> expect a non-401 status (200/protocol response)
code=$(curl -s -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer $WHATSAPP_MCP_TOKEN" http://127.0.0.1:8089/mcp)
test "$code" != "401"

# 3) fail-closed (AC4a): non-loopback bind with auth off must exit non-zero
WHATSAPP_MCP_HOST=0.0.0.0 WHATSAPP_MCP_AUTH=off <run-cmd>; test $? -ne 0

# 4) fail-closed (AC4b): auth on with a weak/empty token must exit non-zero
WHATSAPP_MCP_AUTH=on WHATSAPP_MCP_TOKEN=test <run-cmd>; test $? -ne 0
WHATSAPP_MCP_AUTH=on WHATSAPP_MCP_TOKEN= <run-cmd>; test $? -ne 0
```

## 6.3 Expected Outcomes
- All tests pass; ruff clean.
- Auth gate returns 401 unauthenticated, succeeds with valid token.
- Fail-closed guard exits non-zero on unsafe bind (auth off + non-loopback) AND
  on a missing/weak token while auth is on.
- Privacy (AC9): no outbound connections to empire domains during a tool
  exercise; DB path resolves local-only.
- Failure modes (AC10): expired session / downed bridge / no-Tailscale each
  yield the defined typed error or fail-fast message.
- Manual: a second tailnet device reaches the endpoint; public internet does not.

# 7. Scope & Phasing

- **This mission (MISSION-2026-389):** research (done), this spec, CRITIC review,
  CEO sign-off, **repo bootstrap** (create repo, register, scaffold, CI, webhook).
- **Implementation phases (JIT after bootstrap):** P-IMPL-1 transport (PR-112),
  P-IMPL-2 auth + fail-closed guard, P-IMPL-3 Tailscale run scripts + SETUP.md
  (incl. a token rotation/revocation note, P2-5), P-IMPL-4 hitl-app client
  integration (cross-repo to `hitl-app`).
- **Ordering constraint (P2-3):** register product `hitl-whatsapp-mcp` in HQ
  `config/empire.yaml` **during the bootstrap phase, before** any SpecRouter sync
  of this spec to the satellite — otherwise SpecRouter cannot resolve the target.

# 8. Non-Goals

- Changing the WhatsApp/whatsmeow protocol or the Go bridge internals.
- Public-internet exposure by default (Funnel is opt-in only).
- Server-side storage of message content or tokens in empire infra.
- Multi-tenant hosting — this is a single-user self-hosted server.

# 9. Spec Review History

- **2026-05-30 — CRITIC (spec_reviewer), issue #2885:** verdict
  **APPROVED_WITH_CHANGES** — 0 P0, 6 P1, 5 P2. Report archived at
  `.specs/reports/SPEC-WAMCP-001_spec_review.md`.
- **2026-05-30 — CoS revision (this spec):** folded all 6 P1 fixes into the spec
  — P1-1 §4.3 Edge Cases & Failure Modes; P1-2 §4.1.1 env-var contract; P1-3 AC9
  (BO5 privacy); P1-4 AC6 (≤15-min timed); P1-5 §4.1 tailscale-serve default +
  AC4/AC5; P1-6 AC4b weak/empty-token guard. Also folded P2-2 (PR-112 fallback),
  P2-3 (SpecRouter ordering), P2-4 (verification-script fix), P2-5 (token
  rotation note). Remaining P2-1 (split compound AC1) deferred to implementation.
