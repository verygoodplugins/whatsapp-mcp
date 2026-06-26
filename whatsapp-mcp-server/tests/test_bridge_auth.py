import importlib

import pytest

import whatsapp


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload or {"success": True, "message": "sent", "path": "/tmp/media.jpg"}
        self.text = text

    def json(self):
        return self._payload


def test_bridge_headers_uses_env_token(monkeypatch):
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "env-token")

    assert whatsapp._bridge_headers() == {"Authorization": "Bearer env-token"}


def test_bridge_headers_falls_back_to_token_file(monkeypatch, tmp_path):
    token_file = tmp_path / ".bridge-token"
    token_file.write_text("file-token\n", encoding="utf-8")
    monkeypatch.delenv("WHATSAPP_BRIDGE_TOKEN", raising=False)
    monkeypatch.setattr(whatsapp, "_BRIDGE_TOKEN_PATH", str(token_file))

    assert whatsapp._bridge_headers() == {"Authorization": "Bearer file-token"}


def test_bridge_headers_reads_token_next_to_whatsmeow_db_path(monkeypatch, tmp_path):
    store_dir = tmp_path / "store"
    store_dir.mkdir()
    (store_dir / ".bridge-token").write_text("volume-token\n", encoding="utf-8")

    monkeypatch.delenv("WHATSAPP_BRIDGE_TOKEN", raising=False)
    monkeypatch.setenv("WHATSMEOW_DB_PATH", str(store_dir / "whatsapp.db"))

    try:
        importlib.reload(whatsapp)
        assert whatsapp._bridge_headers() == {"Authorization": "Bearer volume-token"}
    finally:
        monkeypatch.delenv("WHATSMEOW_DB_PATH", raising=False)
        importlib.reload(whatsapp)


def test_bridge_headers_prefers_env_over_token_file(monkeypatch, tmp_path):
    token_file = tmp_path / ".bridge-token"
    token_file.write_text("file-token\n", encoding="utf-8")
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "env-token")
    monkeypatch.setattr(whatsapp, "_BRIDGE_TOKEN_PATH", str(token_file))

    assert whatsapp._bridge_headers() == {"Authorization": "Bearer env-token"}


def test_send_message_without_token_surfaces_bridge_401(monkeypatch, tmp_path):
    calls = []
    missing_token = tmp_path / "missing-token"
    monkeypatch.delenv("WHATSAPP_BRIDGE_TOKEN", raising=False)
    monkeypatch.setattr(whatsapp, "_BRIDGE_TOKEN_PATH", str(missing_token))

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse(status_code=401, payload={"success": False}, text="Unauthorized")

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, message = whatsapp.send_message("12025551234", "hello")

    assert success is False
    assert "HTTP 401" in message
    assert calls[0]["headers"] == {}


@pytest.mark.parametrize(
    ("func_name", "args", "expected_suffix"),
    [
        ("send_message", ("12025551234", "hello"), "/send"),
        ("send_file", ("12025551234", "FILE"), "/send"),
        ("send_audio_message", ("12025551234", "FILE"), "/send"),
        ("download_media", ("msg-id", "12025551234@s.whatsapp.net"), "/download"),
        ("send_reaction", ("12025551234@s.whatsapp.net", "3AABCDEF01234567", "👍"), "/react"),
    ],
)
def test_bridge_post_helpers_include_auth_headers(monkeypatch, tmp_path, func_name, args, expected_suffix):
    calls = []
    media_file = tmp_path / "voice.ogg"
    media_file.write_bytes(b"ogg")
    resolved_args = tuple(str(media_file) if arg == "FILE" else arg for arg in args)
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "env-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    getattr(whatsapp, func_name)(*resolved_args)

    assert calls[0]["url"].endswith(expected_suffix)
    assert calls[0]["headers"] == {"Authorization": "Bearer env-token"}


def test_send_reaction_posts_correct_payload(monkeypatch):
    """send_reaction sends recipient, message_id, emoji, from_me, sender_jid to /react."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse(payload={"ok": True})

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, msg = whatsapp.send_reaction(
        "12025551234@s.whatsapp.net",
        "3AABCDEF01234567",
        "👍",
        from_me=False,
        sender_jid="98765@s.whatsapp.net",
    )

    assert success is True
    assert len(calls) == 1
    assert calls[0]["url"].endswith("/react")
    payload = calls[0]["json"]
    assert payload["recipient"] == "12025551234@s.whatsapp.net"
    assert payload["message_id"] == "3AABCDEF01234567"
    assert payload["emoji"] == "👍"
    assert payload["from_me"] is False
    assert payload["sender_jid"] == "98765@s.whatsapp.net"
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}


def test_send_reaction_empty_emoji_sends_removal(monkeypatch):
    """An empty emoji string is forwarded as-is (reaction removal)."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json})
        return DummyResponse(payload={"ok": True})

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, _ = whatsapp.send_reaction("12025551234@s.whatsapp.net", "3AABCDEF01234567", "")

    assert success is True
    assert calls[0]["json"]["emoji"] == ""


def test_send_reaction_missing_recipient_returns_error():
    """send_reaction returns failure without calling the bridge when recipient is empty."""
    success, msg = whatsapp.send_reaction("", "3AABCDEF01234567", "👍")
    assert success is False
    assert "Recipient" in msg


def test_send_reaction_missing_message_id_returns_error():
    """send_reaction returns failure without calling the bridge when message_id is empty."""
    success, msg = whatsapp.send_reaction("12025551234@s.whatsapp.net", "", "👍")
    assert success is False
    assert "Message ID" in msg


def test_send_message_with_quoted_reply_includes_quote_fields(monkeypatch):
    """send_message passes quoted_message_id, quoted_sender_jid, quoted_content to /api/send."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, _ = whatsapp.send_message(
        "12025551234@s.whatsapp.net",
        "Great point!",
        quoted_message_id="3AORIGINAL0000001",
        quoted_sender_jid="99887766@s.whatsapp.net",
        quoted_content="original text",
    )

    assert success is True
    payload = calls[0]["json"]
    assert payload["recipient"] == "12025551234@s.whatsapp.net"
    assert payload["message"] == "Great point!"
    assert payload["quoted_message_id"] == "3AORIGINAL0000001"
    assert payload["quoted_sender_jid"] == "99887766@s.whatsapp.net"
    assert payload["quoted_content"] == "original text"
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}


def test_send_message_without_quote_omits_quote_fields(monkeypatch):
    """send_message without a quoted_message_id does not include quote fields."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    whatsapp.send_message("12025551234@s.whatsapp.net", "Hello!")

    payload = calls[0]["json"]
    assert "quoted_message_id" not in payload
    assert "quoted_sender_jid" not in payload
    assert "quoted_content" not in payload
