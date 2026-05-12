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
- Supports operator commands in the group:
  - `/members`
  - `/summary`
  - `/remind`
- Sends a weekly weigh-in reminder.
- Uses `SOUL.md` as the persona/safety prompt for optional LLM replies.

Auto replies are disabled by default. To enable them, set `bot.auto_reply: true` and provide `ANTHROPIC_API_KEY`.

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
