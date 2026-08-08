import whatsapp


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload or {"success": True, "message": "sent"}
        self.text = text

    def json(self):
        return self._payload


def _capture_post(monkeypatch, calls):
    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)


def test_send_file_with_caption_sends_one_request(monkeypatch, tmp_path):
    """The caption rides along in the same /send call, so the file and text
    arrive as a single attachment-with-caption message rather than two."""
    calls = []
    media = tmp_path / "report.pdf"
    media.write_bytes(b"%PDF-1.4")
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    _capture_post(monkeypatch, calls)

    success, _ = whatsapp.send_file("12025551234@s.whatsapp.net", str(media), "Q3 numbers")

    assert success is True
    assert len(calls) == 1
    assert calls[0]["url"].endswith("/send")
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}
    payload = calls[0]["json"]
    assert payload["media_path"] == str(media)
    assert payload["message"] == "Q3 numbers"


def test_send_file_without_caption_omits_message_field(monkeypatch, tmp_path):
    """A bare attachment must not send an empty message field — the bridge would
    otherwise set an empty Caption on the media message."""
    calls = []
    media = tmp_path / "photo.jpg"
    media.write_bytes(b"\xff\xd8\xff")
    _capture_post(monkeypatch, calls)

    success, _ = whatsapp.send_file("12025551234@s.whatsapp.net", str(media))

    assert success is True
    assert "message" not in calls[0]["json"]


def test_send_file_empty_caption_omits_message_field(monkeypatch, tmp_path):
    calls = []
    media = tmp_path / "photo.jpg"
    media.write_bytes(b"\xff\xd8\xff")
    _capture_post(monkeypatch, calls)

    whatsapp.send_file("12025551234@s.whatsapp.net", str(media), "")

    assert "message" not in calls[0]["json"]


def test_send_file_missing_file_does_not_call_bridge(monkeypatch, tmp_path):
    calls = []
    monkeypatch.setattr(whatsapp.requests, "post", lambda *a, **kw: calls.append(1))

    success, message = whatsapp.send_file("12025551234@s.whatsapp.net", str(tmp_path / "absent.pdf"), "hi")

    assert success is False
    assert "not found" in message
    assert calls == []
