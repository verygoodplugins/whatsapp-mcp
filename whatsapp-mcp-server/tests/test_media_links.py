"""Signed media download URLs: signing, verification and the HTTP route."""

from urllib.parse import parse_qs, urlsplit

import pytest
from starlette.testclient import TestClient

import main
import media_links

BASE = "https://wa.example/acct"


@pytest.fixture
def store(tmp_path, monkeypatch):
    root = tmp_path / "store"
    (root / "123@s.whatsapp.net").mkdir(parents=True)
    (root / "123@s.whatsapp.net" / "doc 1.pdf").write_bytes(b"%PDF-bytes")
    (tmp_path / "secret.txt").write_text("outside the store")
    monkeypatch.setattr(main, "MESSAGES_DB_PATH", str(root / "messages.db"))
    monkeypatch.setenv("WHATSAPP_MEDIA_BASE_URL", BASE + "/")
    return root


def _split(url):
    parts = urlsplit(url)
    query = {k: v[0] for k, v in parse_qs(parts.query).items()}
    return parts, query


def test_no_url_without_base(store, monkeypatch):
    monkeypatch.delenv("WHATSAPP_MEDIA_BASE_URL")
    assert media_links.signed_url(str(store / "123@s.whatsapp.net" / "doc 1.pdf"), store) is None


def test_no_url_for_file_outside_store(store):
    assert media_links.signed_url(str(store.parent / "secret.txt"), store) is None


def test_signed_url_round_trips(store):
    url = media_links.signed_url(str(store / "123@s.whatsapp.net" / "doc 1.pdf"), store, now=1000)
    parts, query = _split(url)
    assert url.startswith(BASE + "/media/123%40s.whatsapp.net/doc%201.pdf?")
    assert query["expires"] == str(1000 + media_links.URL_TTL_SECONDS)
    target = media_links.resolve("123@s.whatsapp.net/doc 1.pdf", query["expires"], query["sig"], store, now=1000)
    assert target == (store / "123@s.whatsapp.net" / "doc 1.pdf").resolve()


def test_resolve_rejects_expired_tampered_and_escaping(store):
    url = media_links.signed_url(str(store / "123@s.whatsapp.net" / "doc 1.pdf"), store, now=1000)
    _, query = _split(url)
    rel = "123@s.whatsapp.net/doc 1.pdf"
    late = 1001 + media_links.URL_TTL_SECONDS
    assert media_links.resolve(rel, query["expires"], query["sig"], store, now=late) is None
    assert media_links.resolve(rel, str(int(query["expires"]) + 1), query["sig"], store, now=1000) is None
    assert media_links.resolve("123@s.whatsapp.net/other.pdf", query["expires"], query["sig"], store, now=1000) is None
    assert media_links.resolve(rel, "soon", query["sig"], store, now=1000) is None

    escaping = "../secret.txt"
    sig = media_links._signature(escaping, 5000)
    assert media_links.resolve(escaping, "5000", sig, store, now=1000) is None


def test_download_media_returns_fetchable_url(store, monkeypatch):
    file_path = str(store / "123@s.whatsapp.net" / "doc 1.pdf")
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: file_path)

    result = main.download_media("message-1", "123@s.whatsapp.net")

    assert result["file_path"] == file_path
    parts, _ = _split(result["url"])
    client = TestClient(main.mcp.streamable_http_app())
    response = client.get(parts.path.removeprefix("/acct") + "?" + parts.query)
    assert response.status_code == 200
    assert response.content == b"%PDF-bytes"
    assert client.get(parts.path.removeprefix("/acct") + "?expires=1&sig=x").status_code == 404


def test_download_media_without_base_has_no_url(store, monkeypatch):
    monkeypatch.delenv("WHATSAPP_MEDIA_BASE_URL")
    file_path = str(store / "123@s.whatsapp.net" / "doc 1.pdf")
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: file_path)

    assert "url" not in main.download_media("message-1", "123@s.whatsapp.net")
