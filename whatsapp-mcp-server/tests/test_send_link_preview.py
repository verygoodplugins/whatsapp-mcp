import whatsapp


class DummyResponse:
    status_code = 200
    text = "OK"

    def json(self):
        return {"success": True, "message": "sent"}


def test_link_preview_rides_along_with_the_text(monkeypatch):
    calls = []
    monkeypatch.setattr(
        whatsapp.requests, "post", lambda url, json, headers=None: calls.append(json) or DummyResponse()
    )
    card = {"url": "https://example.org/a", "title": "A page", "description": "", "thumbnail": "/9j/4AAQ"}

    ok, _ = whatsapp.send_message("12025551234", "look https://example.org/a", link_preview=card)

    assert ok is True
    assert calls[0]["link_preview"] == {"url": "https://example.org/a", "title": "A page", "thumbnail": "/9j/4AAQ"}


def test_link_preview_url_must_be_in_the_text(monkeypatch):
    calls = []
    monkeypatch.setattr(
        whatsapp.requests, "post", lambda url, json, headers=None: calls.append(json) or DummyResponse()
    )

    ok, msg = whatsapp.send_message("12025551234", "no link here", link_preview={"url": "https://example.org/a"})

    assert ok is False and "must appear" in msg and not calls
    whatsapp.send_message("12025551234", "plain text")
    assert "link_preview" not in calls[0]


def test_invalid_thumbnail_is_dropped_not_the_message(monkeypatch):
    calls = []
    monkeypatch.setattr(
        whatsapp.requests, "post", lambda url, json, headers=None: calls.append(json) or DummyResponse()
    )
    card = {"url": "https://example.org/a", "title": "A page", "thumbnail": "https://example.org/thumb.jpg"}

    ok, _ = whatsapp.send_message("12025551234", "look https://example.org/a", link_preview=card)

    assert ok is True
    assert calls[0]["link_preview"] == {"url": "https://example.org/a", "title": "A page"}
