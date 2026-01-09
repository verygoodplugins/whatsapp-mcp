# WhatsApp MCP

WhatsApp integration for Claude via the Model Context Protocol (MCP).

## Project Structure

```
whatsapp-mcp/
├── whatsapp-bridge/          # Go bridge (WhatsApp Web connection)
│   ├── main.go               # Main bridge application
│   ├── webhook.go            # Webhook handler for incoming messages
│   ├── go.mod                # Go dependencies
│   └── store/                # SQLite database + media (gitignored)
└── whatsapp-mcp-server/      # Python MCP server
    ├── main.py               # FastMCP server with 14 tools
    ├── whatsapp.py           # Core logic and database queries
    ├── audio.py              # FFmpeg audio conversion utilities
    └── tests/                # pytest tests
```

## Architecture

1. **Go Bridge** (`whatsapp-bridge/`) - Connects to WhatsApp Web using whatsmeow library
   - REST API on port 8080 (`/api/send`, `/api/download`, `/api/health`, `/api/typing`)
   - Stores messages in SQLite (`store/messages.db`)
   - Forwards incoming messages to webhook URL

2. **Python MCP Server** (`whatsapp-mcp-server/`) - Exposes WhatsApp to Claude
   - 14 MCP tools: search_contacts, list_messages, send_message, etc.
   - Reads from SQLite database
   - Calls Go bridge REST API for actions

## Development

### Prerequisites
- Go 1.24+
- Python 3.11+
- FFmpeg (optional, for audio conversion)

### Running the Bridge
```bash
cd whatsapp-bridge
go build -o whatsapp-bridge
./whatsapp-bridge
# Scan QR code with WhatsApp mobile app
```

### Running the MCP Server
```bash
cd whatsapp-mcp-server
uv run mcp run
```

### Testing
```bash
cd whatsapp-mcp-server
uv run pytest -v
```

### Linting
```bash
# Python
cd whatsapp-mcp-server
uv run ruff check .
uv run ruff format .

# Go
cd whatsapp-bridge
golangci-lint run
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WHATSAPP_DB_PATH` | `../whatsapp-bridge/store/messages.db` | Path to SQLite database |
| `WHATSAPP_API_URL` | `http://localhost:8080/api` | Go bridge REST API URL |
| `WHATSAPP_BRIDGE_PORT` | `8080` | Port for Go bridge REST API |
| `WEBHOOK_URL` | `http://localhost:8769/whatsapp/webhook` | Webhook for incoming messages |
| `FORWARD_SELF` | `false` | Forward messages sent by self |

## MCP Tools

- `search_contacts` - Search contacts by name or phone
- `get_contact` - Resolve phone number to contact name
- `list_messages` - Get messages with filters and context
- `list_chats` - List all chats
- `get_chat` - Get chat metadata by JID
- `get_direct_chat_by_contact` - Find DM with contact
- `get_contact_chats` - List all chats with contact
- `get_last_interaction` - Last message with contact
- `get_message_context` - Messages around a specific message
- `send_message` - Send text message
- `send_file` - Send media file
- `send_audio_message` - Send voice message
- `download_media` - Download media from message

## Key Files to Understand

- `whatsapp-mcp-server/whatsapp.py` - Core database queries and data conversion
- `whatsapp-bridge/main.go` - WhatsApp connection and REST API
- `.github/workflows/ci.yml` - CI pipeline with linting and tests

## Gotchas

1. **JID Format**: WhatsApp IDs are JIDs like `1234567890@s.whatsapp.net` (DM) or `123456@g.us` (group)
2. **Media Files**: Stored in `store/{sender_jid}/` directories
3. **Database**: SQLite with `messages` and `chats` tables
4. **Audio**: Must be Opus .ogg format for voice messages (FFmpeg converts automatically)
