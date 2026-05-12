# WhatsApp Soul Bot

`soul-bot` is a deployable sidecar for a WhatsApp support group. The WhatsApp bridge forwards incoming group messages to this service via `WEBHOOK_URL`; the bot stores message history, distinguishes operators from participants, tracks text-submitted weights, and can send reminders through the bridge REST API.

## What It Does

- Watches configured WhatsApp group JIDs only.
- Classifies senders as:
  - `operator`: allowed phone/JID in `operators`.
  - `participant`: regular group member.
  - `self`: messages sent by the linked WhatsApp account.
- Stores chat history in SQLite.
- Detects simple text weights like `82.4 קג` or `82.4 kg`.
- Saves incoming image media to disk for audit/review.
- Can run a vision provider on image messages to extract scale weight, mark the photo unreadable, or route to operator review.
- Supports operator commands in the group:
  - `/members`
  - `/summary`
  - `/remind`
- Sends a weekly weigh-in reminder.
- Uses `SOUL.md` as the persona/safety prompt for optional LLM replies.

Auto replies are disabled by default. With `auto_reply: true`, the bot still does not answer every participant message. It classifies participant messages first:

- casual chat -> ignore
- support question -> LLM-generated group reply
- text weight -> store and optionally acknowledge
- image/photo -> vision review if enabled, otherwise operator review
- sensitive topic -> operator review

## Vision For Scale Photos

The bridge can forward image messages as base64. The bot saves each image under `bot.media_dir`, records the path in SQLite, and can optionally analyze the image.

For local demos without an Anthropic API key, set:

```yaml
bot:
  auto_reply: true
  vision_enabled: true
  vision_provider: "claude_code"
  media_dir: "/Users/or/projects/Whasr/soul-bot/data/media"
  claude_code_cwd: "/Users/or/projects/whatsapp-mcp-test-sandbox"
  claude_code_model: "claude-opus-4-6"
```

`claude_code` shells out to your local `claude -p` CLI and asks it to inspect the saved image path. Set `claude_code_model` (or `SOUL_BOT_CLAUDE_CODE_MODEL`) to pin a Claude Code model instead of using the CLI default. This is useful for a laptop demo. For a 24/7 server, use a server-friendly API/provider instead of relying on an interactive Claude Code install.

## Local Docker Demo

1. Create a local config:

   ```bash
   cp soul-bot/config.example.yaml soul-bot/config.yaml
   ```

2. Edit `soul-bot/config.yaml`:

   ```yaml
   watched_groups:
     - jid: "120363426809776331@g.us"
       name: "רזים על זה"

   operators:
     - "YOUR_PHONE@s.whatsapp.net"
   ```

3. Start both services:

   ```bash
   docker compose up --build
   ```

4. Scan the QR printed by `whatsapp-bridge` logs:

   ```bash
   docker compose logs -f whatsapp-bridge
   ```

5. Check health:

   ```bash
   curl http://localhost:8769/health
   ```

For a server, persist the two named volumes and keep `config.yaml` outside the image. Move the existing `whatsapp-bridge/store/whatsapp.db` into the Docker volume if you want to preserve the linked WhatsApp session.
