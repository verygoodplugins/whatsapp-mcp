# WhatsApp MCP Server

[![CI](https://github.com/verygoodplugins/whatsapp-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/verygoodplugins/whatsapp-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Python 3.11+](https://img.shields.io/badge/python-3.11+-blue.svg)](https://www.python.org/downloads/)
[![Go 1.24+](https://img.shields.io/badge/go-1.24+-00ADD8.svg)](https://go.dev/)

A Model Context Protocol (MCP) server for WhatsApp, enabling Claude to read and send WhatsApp messages.

> Originally created by [Luke Harries](https://github.com/lharries/whatsapp-mcp). Maintained by [Very Good Plugins](https://verygoodplugins.com).

## Features

- Search and read personal WhatsApp messages (text, images, videos, documents, audio)
- Search contacts by name or phone number
- Send messages to individuals or groups
- Send media files (images, videos, documents, voice messages)
- Download media from received messages
- All messages stored locally in SQLite - only sent to Claude when you allow it

## Quick Start

### Prerequisites

- Go 1.24+
- Python 3.11+
- [uv](https://docs.astral.sh/uv/) package manager
- Claude Desktop or Cursor
- FFmpeg (optional, for voice message conversion)

### Installation

1. **Clone the repository**

   ```bash
   git clone https://github.com/verygoodplugins/whatsapp-mcp.git
   cd whatsapp-mcp
   ```

2. **Start the WhatsApp bridge**

   ```bash
   cd whatsapp-bridge
   go run main.go
   ```

   Scan the QR code with WhatsApp on your phone to authenticate.

3. **Configure Claude Desktop**

   Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

   ```json
   {
     "mcpServers": {
       "whatsapp": {
         "command": "uv",
         "args": [
           "--directory",
           "/path/to/whatsapp-mcp/whatsapp-mcp-server",
           "run",
           "main.py"
         ]
       }
     }
   }
   ```

   Replace `/path/to/whatsapp-mcp` with your actual path.

4. **Restart Claude Desktop**

## Environment Variables

Copy `.env.example` to `.env` and configure as needed:

| Variable | Default | Description |
| -------- | ------- | ----------- |
| `WHATSAPP_BRIDGE_PORT` | `8080` | Port for Go bridge REST API |
| `WEBHOOK_URL` | `http://localhost:8769/whatsapp/webhook` | Webhook for incoming messages |
| `FORWARD_SELF` | `false` | Forward messages sent by self |
| `WHATSAPP_DB_PATH` | `../whatsapp-bridge/store/messages.db` | Path to SQLite database |
| `WHATSAPP_API_URL` | `http://localhost:8080/api` | Go bridge REST API URL |

## MCP Tools

Messages include `sender_display` showing "Name (phone)" format for easy identification by agents.

| Tool | Description |
| ---- | ----------- |
| `search_contacts` | Search contacts by name or phone number |
| `get_contact` | Resolve phone number to contact name |
| `list_messages` | Get messages with filters, date ranges, and sorting (default 50, max 500) |
| `list_chats` | List all chats with metadata (default 50, max 200) |
| `get_chat` | Get specific chat metadata by JID |
| `get_direct_chat_by_contact` | Find DM with a contact |
| `get_contact_chats` | List all chats involving a contact |
| `get_last_interaction` | Get last message with a contact |
| `get_message_context` | Get messages around a specific message |
| `send_message` | Send text message |
| `send_file` | Send media file (image, video, document) |
| `send_audio_message` | Send voice message (converts to Opus .ogg) |
| `download_media` | Download media from a message |

## Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  Claude Desktop │ ──► │ Python MCP Server│ ──► │   Go Bridge     │
│                 │     │   (FastMCP)      │     │  (whatsmeow)    │
└─────────────────┘     └──────────────────┘     └─────────────────┘
                               │                        │
                               ▼                        ▼
                        ┌──────────────┐         ┌──────────────┐
                        │   SQLite     │◄────────│  WhatsApp    │
                        │  messages.db │         │  Web API     │
                        └──────────────┘         └──────────────┘
```

1. **Go Bridge** (`whatsapp-bridge/`): Connects to WhatsApp Web using [whatsmeow](https://github.com/tulir/whatsmeow), handles QR authentication, and stores messages in SQLite.

2. **Python MCP Server** (`whatsapp-mcp-server/`): Implements the [Model Context Protocol](https://modelcontextprotocol.io/) with 14 tools for Claude to interact with WhatsApp.

## Development

### Running Tests

```bash
cd whatsapp-mcp-server
uv pip install -e ".[dev]"
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

## Troubleshooting

### Authentication Issues

- **QR Code Not Displaying**: Restart the bridge. Check terminal QR code support.
- **Device Limit Reached**: Remove a linked device from WhatsApp Settings > Linked Devices.
- **No Messages Loading**: Initial sync can take several minutes for large chat histories.
- **Out of Sync**: Delete `whatsapp-bridge/store/*.db` files and re-authenticate.

### Windows

Windows requires CGO for go-sqlite3. Install [MSYS2](https://www.msys2.org/) and enable CGO:

```bash
go env -w CGO_ENABLED=1
go run main.go
```

## Security Notice

> **Caution**: As with many MCP servers, this is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection could lead to private data exfiltration. Use with awareness.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Credits & History

This project is a maintained fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp), originally created by [Luke Harries](https://github.com/lharries).

**Why we forked:** The original repository hasn't been updated since April 2025. We needed continued maintenance, bug fixes, and new features for production use.

**What we changed:**

- Fixed compilation issues from dependency upgrades
- Added contact name resolution in message responses
- Improved message querying (date ranges, sorting, larger batches)
- Fixed linting issues and improved code quality
- Added CI/CD pipeline with GitHub Actions
- Published to MCP registry

We're grateful to Luke for creating the original project!

### Dependencies

- [whatsmeow](https://github.com/tulir/whatsmeow) - WhatsApp Web API library for Go
- [FastMCP](https://github.com/jlowin/fastmcp) - Fast Model Context Protocol implementation
- [Model Context Protocol](https://modelcontextprotocol.io/) - Anthropic's MCP specification
