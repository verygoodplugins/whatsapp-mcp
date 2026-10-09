"""Installed entry point: imported and executable routes share the restricted server."""

from readonly_main import get_chat, get_message_context, list_chats, list_messages, mcp, run

__all__ = ["get_chat", "get_message_context", "list_chats", "list_messages", "mcp", "run"]

if __name__ == "__main__":
    run()
