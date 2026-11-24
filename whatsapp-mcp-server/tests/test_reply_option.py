import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

import main as mcp_main
import whatsapp


@pytest.fixture
def bridge(monkeypatch):
    received = []
    responses = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers["Content-Length"]))
            received.append({"path": self.path, "auth": self.headers.get("Authorization"), "json": json.loads(body)})
            status, payload = responses.pop(0)
            data = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, *args):
            pass

    server = HTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    monkeypatch.setattr(whatsapp, "WHATSAPP_API_BASE_URL", f"http://127.0.0.1:{server.server_port}/api")
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    yield received, responses
    server.shutdown()
    server.server_close()


def test_reply_with_option_posts_payload_with_auth(bridge):
    received, responses = bridge
    responses.append((200, {"success": True, "message": 'Selected "Exame" in reply to ABC'}))

    success, message = whatsapp.reply_with_option("15551234567@s.whatsapp.net", "ABC", "Exame")

    assert success is True
    assert message == 'Selected "Exame" in reply to ABC'
    assert received == [
        {
            "path": "/api/reply-option",
            "auth": "Bearer test-token",
            "json": {"chat_jid": "15551234567@s.whatsapp.net", "message_id": "ABC", "option": "Exame"},
        }
    ]


def test_reply_with_option_surfaces_bridge_error_message(bridge):
    _, responses = bridge
    responses.append((422, {"success": False, "message": 'form option "Agendar" cannot be answered by the bridge'}))

    success, message = whatsapp.reply_with_option("15551234567@s.whatsapp.net", "ABC", "Agendar")

    assert success is False
    assert message == 'Error: HTTP 422 - form option "Agendar" cannot be answered by the bridge'


def test_reply_with_option_handles_non_json_error(bridge):
    _, responses = bridge
    responses.append((400, b"chat_jid, message_id, and option are required\n"))

    success, message = whatsapp.reply_with_option("15551234567@s.whatsapp.net", "ABC", "1")

    assert success is False
    assert message.startswith("Error: HTTP 400 - chat_jid, message_id, and option are required")


@pytest.mark.parametrize(
    ("chat_jid", "message_id", "option"),
    [("", "ABC", "1"), ("15551234567@s.whatsapp.net", "", "1"), ("15551234567@s.whatsapp.net", "ABC", "  ")],
)
def test_reply_with_option_requires_all_fields(bridge, chat_jid, message_id, option):
    received, _ = bridge

    success, message = whatsapp.reply_with_option(chat_jid, message_id, option)

    assert success is False
    assert "must be provided" in message
    assert received == []


def test_reply_with_option_reports_unreachable_bridge(monkeypatch):
    monkeypatch.setattr(whatsapp, "WHATSAPP_API_BASE_URL", "http://127.0.0.1:9/api")

    success, message = whatsapp.reply_with_option("15551234567@s.whatsapp.net", "ABC", "1")

    assert success is False
    assert message.startswith("Request error:")


def test_reply_to_interactive_message_tool_returns_bridge_result(bridge):
    _, responses = bridge
    responses.append((200, {"success": True, "message": 'Selected "Consulta" in reply to ABC'}))

    result = mcp_main.reply_to_interactive_message("15551234567@s.whatsapp.net", "ABC", "1")

    assert result == {"success": True, "message": 'Selected "Consulta" in reply to ABC'}
