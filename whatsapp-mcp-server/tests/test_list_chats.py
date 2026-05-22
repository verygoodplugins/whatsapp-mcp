"""Regression tests for list_chats / get_chat.

The previous SQL referenced messages.* in the SELECT but only added the
LEFT JOIN messages clause when include_last_message=True, so calling
list_chats(include_last_message=False) errored out with
"no such column: messages.content" and silently returned [].
"""

import sqlite3

import pytest

import whatsapp


def _make_messages_db(path):
    """Create a minimal messages.db that matches the real bridge schema."""
    conn = sqlite3.connect(path)
    cursor = conn.cursor()
    cursor.executescript(
        """
        CREATE TABLE chats (
            jid TEXT PRIMARY KEY,
            name TEXT,
            last_message_time TIMESTAMP
        );
        CREATE TABLE messages (
            id TEXT,
            chat_jid TEXT,
            sender TEXT,
            content TEXT,
            timestamp TIMESTAMP,
            is_from_me BOOLEAN,
            media_type TEXT,
            filename TEXT,
            url TEXT,
            media_key BLOB,
            file_sha256 BLOB,
            file_enc_sha256 BLOB,
            file_length INTEGER,
            PRIMARY KEY (id, chat_jid),
            FOREIGN KEY (chat_jid) REFERENCES chats(jid)
        );
        """
    )
    cursor.execute(
        "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
        ("1234567890@s.whatsapp.net", "Alice", "2024-01-15 10:30:00+00:00"),
    )
    cursor.execute(
        """INSERT INTO messages
           (id, chat_jid, sender, content, timestamp, is_from_me)
           VALUES (?, ?, ?, ?, ?, ?)""",
        (
            "msg1",
            "1234567890@s.whatsapp.net",
            "1234567890",
            "hello world",
            "2024-01-15 10:30:00+00:00",
            0,
        ),
    )
    conn.commit()
    conn.close()


@pytest.fixture
def messages_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    _make_messages_db(str(db_path))
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


def test_list_chats_with_last_message(messages_db):
    """Default behavior: include the joined last_message fields."""
    chats = whatsapp.list_chats(limit=10)
    assert len(chats) == 1
    assert chats[0]["jid"] == "1234567890@s.whatsapp.net"
    assert chats[0]["name"] == "Alice"
    assert chats[0]["last_message"] == "hello world"
    assert chats[0]["last_sender"] == "1234567890"


def test_list_chats_without_last_message(messages_db):
    """Regression: include_last_message=False must not error and must
    still return the chat row with NULL last-message fields."""
    chats = whatsapp.list_chats(limit=10, include_last_message=False)
    assert len(chats) == 1
    assert chats[0]["jid"] == "1234567890@s.whatsapp.net"
    assert chats[0]["name"] == "Alice"
    assert chats[0]["last_message"] is None
    assert chats[0]["last_sender"] is None
    assert chats[0]["last_is_from_me"] is None


def test_list_chats_query_filter_with_include_last_message_false(messages_db):
    """Filter by query while not including the last message — both code paths
    should compose cleanly."""
    chats = whatsapp.list_chats(query="Alice", include_last_message=False)
    assert len(chats) == 1
    assert chats[0]["name"] == "Alice"

    chats = whatsapp.list_chats(query="Bob", include_last_message=False)
    assert chats == []


def test_get_chat_with_last_message(messages_db):
    chat = whatsapp.get_chat("1234567890@s.whatsapp.net")
    assert chat is not None
    assert chat["name"] == "Alice"
    assert chat["last_message"] == "hello world"


def test_get_chat_without_last_message(messages_db):
    """Regression: same bug existed in get_chat."""
    chat = whatsapp.get_chat("1234567890@s.whatsapp.net", include_last_message=False)
    assert chat is not None
    assert chat["name"] == "Alice"
    assert chat["last_message"] is None
    assert chat["last_sender"] is None
    assert chat["last_is_from_me"] is None


def test_get_chat_missing_jid_returns_none(messages_db):
    assert whatsapp.get_chat("nonexistent@s.whatsapp.net") is None
    assert whatsapp.get_chat("nonexistent@s.whatsapp.net", include_last_message=False) is None
