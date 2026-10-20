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


def test_download_without_token_surfaces_bridge_401(monkeypatch, tmp_path):
    calls = []
    missing_token = tmp_path / "missing-token"
    monkeypatch.delenv("WHATSAPP_BRIDGE_TOKEN", raising=False)
    monkeypatch.setattr(whatsapp, "_BRIDGE_TOKEN_PATH", str(missing_token))

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse(status_code=401, payload={"success": False}, text="Unauthorized")

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    path = whatsapp.download_media("msg-id", "12025551234@s.whatsapp.net")

    assert path is None
    assert calls[0]["headers"] == {}


@pytest.mark.parametrize(
    ("func_name", "args", "expected_suffix"),
    [
        ("download_media", ("msg-id", "12025551234@s.whatsapp.net"), "/download"),
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
