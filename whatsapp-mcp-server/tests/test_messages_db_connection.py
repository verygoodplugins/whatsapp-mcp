"""messages.db is bridge-owned: the MCP server must read it, never create or write it.

A wrong WHATSAPP_DB_PATH used to make sqlite3.connect() create an empty file
and every read tool answer "no messages" — the AI then confidently tells the
user their history is empty. Now it is a loud, actionable tool error.
"""

import sqlite3

import pytest

import whatsapp

SCHEMA = """
    CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP, last_read_time TIMESTAMP);
    CREATE TABLE messages (
        id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
        media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
        file_length INTEGER, quoted_message_id TEXT, PRIMARY KEY (id, chat_jid)
    );
"""


def _make_db(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA)
    conn.commit()
    conn.close()
    return path


@pytest.mark.parametrize(
    "call",
    [
        lambda: whatsapp.list_chats(),
        lambda: whatsapp.list_messages(include_context=False),
        lambda: whatsapp.get_chat("x@s.whatsapp.net"),
        lambda: whatsapp.get_direct_chat_by_contact("15551234567"),
        lambda: whatsapp.get_contact_chats("15551234567"),
        lambda: whatsapp.get_last_interaction("15551234567"),
        lambda: whatsapp.get_message_context("msg-1"),
    ],
)
def test_missing_messages_db_is_a_loud_error_and_creates_nothing(tmp_path, monkeypatch, call):
    missing = tmp_path / "store" / "messages.db"
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(missing))

    with pytest.raises(FileNotFoundError, match="WHATSAPP_DB_PATH"):
        call()

    assert not missing.exists(), "a read must never create a phantom empty database"


def test_sender_name_enrichment_degrades_to_jid_when_db_missing(tmp_path, monkeypatch):
    # get_sender_name decorates rows that were already read; it must not turn a
    # stubbed or cached result into a hard failure.
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(tmp_path / "missing.db"))
    assert whatsapp.get_sender_name("15551234567@s.whatsapp.net") == "15551234567@s.whatsapp.net"


def test_messages_db_is_opened_read_only(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(_make_db(tmp_path / "messages.db")))

    conn = whatsapp._connect_messages_db()
    try:
        with pytest.raises(sqlite3.OperationalError, match="readonly"):
            conn.execute("INSERT INTO chats (jid) VALUES ('x@s.whatsapp.net')")
    finally:
        conn.close()


def test_messages_db_path_with_uri_special_characters(tmp_path, monkeypatch):
    # Spaces, '#' and '?' are all legal in a path; the connection must take
    # the path as-is rather than treating it as a URI.
    db = _make_db(tmp_path / "wa mcp #1" / "why?" / "messages.db")
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db))

    assert whatsapp.list_chats() == []
    assert whatsapp.list_messages(include_context=False) == []
