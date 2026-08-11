import whatsapp


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload or {"success": True, "message": "Message revoked for everyone and removed locally"}
        self.text = text

    def json(self):
        return self._payload


def test_delete_message_posts_authenticated_payload(monkeypatch):
    """delete_message sends chat_jid, message_id and for_everyone to /delete with auth."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, message = whatsapp.delete_message("12025551234@s.whatsapp.net", "3AABCDEF01234567")

    assert success is True
    assert len(calls) == 1
    assert calls[0]["url"].endswith("/delete")
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}
    payload = calls[0]["json"]
    assert payload["chat_jid"] == "12025551234@s.whatsapp.net"
    assert payload["message_id"] == "3AABCDEF01234567"
    # Revoking for both sides is the default, matching the WhatsApp UI button.
    assert payload["for_everyone"] is True
    assert "revoked" in message


def test_delete_message_local_only_sets_for_everyone_false(monkeypatch):
    """for_everyone=False is forwarded so the bridge only drops the local row."""
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"json": json})
        return DummyResponse(payload={"success": True, "message": "Message removed locally (remote unchanged)"})

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, _ = whatsapp.delete_message("12025551234@s.whatsapp.net", "3AABCDEF01234567", for_everyone=False)

    assert success is True
    assert calls[0]["json"]["for_everyone"] is False


def test_delete_message_missing_chat_jid_does_not_call_bridge(monkeypatch):
    calls = []
    monkeypatch.setattr(whatsapp.requests, "post", lambda *a, **kw: calls.append(1))

    success, message = whatsapp.delete_message("", "3AABCDEF01234567")

    assert success is False
    assert "required" in message
    assert calls == []


def test_delete_message_missing_message_id_does_not_call_bridge(monkeypatch):
    calls = []
    monkeypatch.setattr(whatsapp.requests, "post", lambda *a, **kw: calls.append(1))

    success, message = whatsapp.delete_message("12025551234@s.whatsapp.net", "")

    assert success is False
    assert "required" in message
    assert calls == []


def test_delete_message_surfaces_bridge_error_status(monkeypatch):
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    monkeypatch.setattr(
        whatsapp.requests,
        "post",
        lambda url, json, headers=None: DummyResponse(status_code=401, payload={}, text="Unauthorized"),
    )

    success, message = whatsapp.delete_message("12025551234@s.whatsapp.net", "3AABCDEF01234567")

    assert success is False
    assert "HTTP 401" in message
