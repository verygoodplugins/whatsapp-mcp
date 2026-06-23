import os

import whatsapp


def test_bridge_token_path_colocated_with_whatsmeow_db():
    """The token file lives in the same store dir as whatsapp.db, so relocating
    the data directory (e.g. into a Docker volume) keeps bridge auth working."""
    assert os.path.dirname(whatsapp._BRIDGE_TOKEN_PATH) == os.path.dirname(
        whatsapp.WHATSMEOW_DB_PATH
    )


def test_read_bridge_token_reads_from_relocated_store(monkeypatch, tmp_path):
    """When WHATSMEOW_DB_PATH points at a relocated store, the token is read from
    that same directory (regression for HTTP 401 in containerized deploys)."""
    store = tmp_path / "store"
    store.mkdir()
    (store / ".bridge-token").write_text("vol-token\n", encoding="utf-8")
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(store / "whatsapp.db"))
    monkeypatch.setattr(whatsapp, "_BRIDGE_TOKEN_PATH", str(store / ".bridge-token"))
    monkeypatch.delenv("WHATSAPP_BRIDGE_TOKEN", raising=False)
    assert whatsapp._read_bridge_token() == "vol-token"
