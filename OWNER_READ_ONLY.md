# Owner-controlled read-only profile

This local profile replaces the normal executable entry points. It exposes four
stdio MCP tools: `list_chats`, `get_chat`, `list_messages`, and
`get_message_context`. It never registers contacts, sending, reactions, read
receipts, history requests, downloads, transcription, or remote transports.
The Go executable requires the same policy before startup; its REST surface is
limited to authenticated loopback health. Inherited media/webhook settings cannot
enable those features.

This is application filtering, not a WhatsApp protocol permission. A linked
companion still receives account-wide protocol data, and whatsmeow maintains its
own contacts/session state. SQLite is plaintext; mode 0700/0600 does not isolate
other processes running as the same macOS user. Do not pair until the owner has
accepted these limits and selected exact groups. Use a new private runtime
outside synced folders; never import an existing account-wide message database.

## Local permission CLI

There is no existing GUI in this repository. `owner_policy.py` is a local CLI;
it never opens a network listener or reads messages/session credentials. Policy
files must be absolute paths, current-user owned mode 0600, in a mode 0700
directory without symlinks. Concurrent CLI changes are serialized; replacement
is atomic and the previous policy is retained for rollback.

```sh
python owner_policy.py --policy /absolute/private/runtime/policy.json init
python owner_policy.py --policy /absolute/private/runtime/policy.json status
python owner_policy.py --policy /absolute/private/runtime/policy.json --dry-run groups 123-456@g.us
python owner_policy.py --policy /absolute/private/runtime/policy.json --confirm groups 123-456@g.us
python owner_policy.py --policy /absolute/private/runtime/policy.json --confirm enable
python owner_policy.py --policy /absolute/private/runtime/policy.json revoke
python owner_policy.py --policy /absolute/private/runtime/policy.json --dry-run rollback
```

Replace the sample JID with groups chosen by the owner. `init` starts disabled
with no groups. Only `read_messages` and `cache_messages` can be enabled;
`capability NAME off` narrows access, and `--confirm capability NAME on` restores
a supported capability. Unsupported capabilities always fail. Group additions,
enabling access, and a rollback that broadens permissions require `--confirm`.
Policy is reread for each tool/event and before returning tool results. Revocation
blocks subsequent reads/cache writes; it does not erase existing plaintext cache
or disconnect the companion. Stop the bridge and unlink on the phone for full
account revocation. Already returned data cannot be recalled.

## Future launch, after separate pairing approval

Set `WHATSAPP_POLICY_FILE` to the private policy in both processes.
Set `WHATSAPP_DB_PATH` only for Python, to the new runtime's
`store/messages.db`. Launch the Go binary with its working directory set to that
runtime. `--check-policy` validates without creating storage or connecting.
`--pair-private` permits first pairing only from an interactive private terminal;
never capture its QR in shared logs. `--full-history-pair` is rejected.
Run Python `main.py` over stdio. HTTP/SSE are rejected even if environment
variables request them. No OAuth grant, cloud gateway, or remote access is
installed by this change.

WhatsApp text and group names remain untrusted model input. The response wrapper
labels them as such, limits results to 64 KiB, and clips long content. A consuming
assistant must treat them as data and must not follow embedded requests to use
other tools or disclose information. This wrapper does not eliminate prompt
injection in a client with other powerful tools.

## Verification and rollback

Synthetic policy tests cover exact group scope, duplicate IDs across chats,
revocation, malformed/overpermissive policy, disabled mutations/egress, bounded
responses and SQLite write rejection. Go/Python dependencies must use reviewed
lockfiles and a patched private toolchain. Run the full Python suite, Ruff,
Go tests/build/vet and dependency advisory checks before pairing. Real pairing,
phone-driven history and cloud OAuth are separate acceptance steps.

Stop the foreground processes and remove their client configuration to uninstall.
If paired later, unlink the companion in WhatsApp first, then remove the dedicated
runtime (including policy backups, database sidecars, session and token files),
private dependencies and this checkout. Reverting to stock code reintroduces the
broad read/write surface; do not launch stock code with a restricted session/cache.
