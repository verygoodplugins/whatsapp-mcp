"""Tests for typing/presence state functions."""

import whatsapp


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload or {"success": True, "typing": []}
        self.text = text

    def json(self):
        return self._payload


def test_get_typing_state_sends_auth_headers(monkeypatch):
    """get_typing_state includes authorization headers."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_get(url, params=None, headers=None):
        calls.append({"url": url, "params": params, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "get", fake_get)

    whatsapp.get_typing_state()

    assert calls[0]["url"].endswith("/typing")
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}


def test_get_typing_state_passes_chat_jid_filter(monkeypatch):
    """get_typing_state passes chat_jid as query param when provided."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_get(url, params=None, headers=None):
        calls.append({"url": url, "params": params, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "get", fake_get)

    whatsapp.get_typing_state(chat_jid="12025551234@s.whatsapp.net")

    assert calls[0]["params"] == {"chat_jid": "12025551234@s.whatsapp.net"}


def test_get_typing_state_returns_typing_list(monkeypatch):
    """get_typing_state returns the typing list from the response."""
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    typing_data = [
        {
            "chat_jid": "12025551234@s.whatsapp.net",
            "sender_jid": "98765432100@s.whatsapp.net",
            "is_typing": True,
            "media": "",
            "updated_at": "2026-08-15T10:30:00Z",
        }
    ]

    def fake_get(url, params=None, headers=None):
        return DummyResponse(payload={"success": True, "typing": typing_data})

    monkeypatch.setattr(whatsapp.requests, "get", fake_get)

    result = whatsapp.get_typing_state()

    assert result == typing_data


def test_get_typing_state_returns_empty_on_failure(monkeypatch):
    """get_typing_state returns empty list on HTTP error."""
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_get(url, params=None, headers=None):
        return DummyResponse(status_code=500, payload={}, text="Internal Server Error")

    monkeypatch.setattr(whatsapp.requests, "get", fake_get)

    result = whatsapp.get_typing_state()

    assert result == []


def test_get_typing_state_returns_empty_on_success_false(monkeypatch):
    """get_typing_state returns empty list when success=false."""
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_get(url, params=None, headers=None):
        return DummyResponse(payload={"success": False})

    monkeypatch.setattr(whatsapp.requests, "get", fake_get)

    result = whatsapp.get_typing_state()

    assert result == []


def test_get_typing_state_with_audio_media(monkeypatch):
    """get_typing_state correctly returns audio (voice recording) state."""
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    typing_data = [
        {
            "chat_jid": "12025551234@s.whatsapp.net",
            "sender_jid": "98765432100@s.whatsapp.net",
            "is_typing": True,
            "media": "audio",
            "updated_at": "2026-08-15T10:30:00Z",
        }
    ]

    def fake_get(url, params=None, headers=None):
        return DummyResponse(payload={"success": True, "typing": typing_data})

    monkeypatch.setattr(whatsapp.requests, "get", fake_get)

    result = whatsapp.get_typing_state()

    assert result[0]["media"] == "audio"
