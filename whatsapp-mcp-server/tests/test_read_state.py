"""End-to-end tests for the chat read-state read path.

The bridge records how far each chat has been read in chats.last_read_time
(read receipts + history-sync backfill). These tests cover the MCP side:
that the marker is surfaced on every chat tool and that the derived `unread`
flag distinguishes a genuinely unread chat from one whose last message merely
happens to be inbound.
"""

import sqlite3

import pytest

import whatsapp

CHATS = [
    # (jid, name, last_message_time, last_read_time, last message is_from_me)
    ("unread@s.whatsapp.net", "Unread", "2024-01-15 10:30:00+00:00", "2024-01-15 09:00:00+00:00", 0),
    ("read@s.whatsapp.net", "Read", "2024-01-15 10:30:00+00:00", "2024-01-15 11:00:00+00:00", 0),
    ("mine@s.whatsapp.net", "Mine", "2024-01-15 10:30:00+00:00", None, 1),
    ("nomarker@s.whatsapp.net", "NoMarker", "2024-01-15 10:30:00+00:00", None, 0),
]

SCHEMA_WITH_READ_STATE = """
    CREATE TABLE chats (
        jid TEXT PRIMARY KEY,
        name TEXT,
        last_message_time TIMESTAMP,
        last_read_time TIMESTAMP
    );
"""

# A store written by a bridge that predates the read-state migration.
SCHEMA_WITHOUT_READ_STATE = """
    CREATE TABLE chats (
        jid TEXT PRIMARY KEY,
        name TEXT,
        last_message_time TIMESTAMP
    );
"""

MESSAGES_SCHEMA = """
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


def _make_db(path, *, with_read_state: bool):
    conn = sqlite3.connect(path)
    cursor = conn.cursor()
    cursor.executescript((SCHEMA_WITH_READ_STATE if with_read_state else SCHEMA_WITHOUT_READ_STATE) + MESSAGES_SCHEMA)
    for jid, name, last_message_time, last_read_time, is_from_me in CHATS:
        if with_read_state:
            cursor.execute(
                "INSERT INTO chats (jid, name, last_message_time, last_read_time) VALUES (?, ?, ?, ?)",
                (jid, name, last_message_time, last_read_time),
            )
        else:
            cursor.execute(
                "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
                (jid, name, last_message_time),
            )
        cursor.execute(
            """INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
               VALUES (?, ?, ?, ?, ?, ?)""",
            (f"msg-{jid}", jid, "me" if is_from_me else jid, "hello", last_message_time, is_from_me),
        )
    conn.commit()
    conn.close()


@pytest.fixture
def read_state_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    _make_db(str(db_path), with_read_state=True)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


@pytest.fixture
def legacy_db(tmp_path, monkeypatch):
    db_path = tmp_path / "legacy.db"
    _make_db(str(db_path), with_read_state=False)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


@pytest.mark.parametrize("include_last_message", [True, False])
def test_list_chats_exposes_read_state(read_state_db, include_last_message):
    chats = {c["jid"]: c for c in whatsapp.list_chats(limit=10, include_last_message=include_last_message)}

    assert chats["unread@s.whatsapp.net"]["last_read_time"] == "2024-01-15T09:00:00+00:00"
    assert chats["unread@s.whatsapp.net"]["unread"] is True
    # Read on another device after the last message arrived.
    assert chats["read@s.whatsapp.net"]["last_read_time"] == "2024-01-15T11:00:00+00:00"
    assert chats["read@s.whatsapp.net"]["unread"] is False
    # Our own message can never be unread, marker or not.
    assert chats["mine@s.whatsapp.net"]["last_read_time"] is None
    assert chats["mine@s.whatsapp.net"]["unread"] is False
    # No marker: falls back to "last message is inbound".
    assert chats["nomarker@s.whatsapp.net"]["last_read_time"] is None
    assert chats["nomarker@s.whatsapp.net"]["unread"] is True


@pytest.mark.parametrize("include_last_message", [True, False])
def test_get_chat_exposes_read_state(read_state_db, include_last_message):
    chat = whatsapp.get_chat("read@s.whatsapp.net", include_last_message=include_last_message)
    assert chat["last_read_time"] == "2024-01-15T11:00:00+00:00"
    assert chat["unread"] is False

    chat = whatsapp.get_chat("unread@s.whatsapp.net", include_last_message=include_last_message)
    assert chat["last_read_time"] == "2024-01-15T09:00:00+00:00"
    assert chat["unread"] is True


def test_get_direct_chat_by_contact_exposes_read_state(read_state_db):
    chat = whatsapp.get_direct_chat_by_contact("unread")
    assert chat["last_read_time"] == "2024-01-15T09:00:00+00:00"
    assert chat["unread"] is True


def test_get_contact_chats_exposes_read_state(read_state_db):
    chats = whatsapp.get_contact_chats("read@s.whatsapp.net")
    chat = next(c for c in chats if c["jid"] == "read@s.whatsapp.net")
    assert chat["last_read_time"] == "2024-01-15T11:00:00+00:00"
    assert chat["unread"] is False


def test_reads_work_against_store_without_read_state_column(legacy_db):
    """A messages.db from a bridge older than the migration must still read."""
    chats = {c["jid"]: c for c in whatsapp.list_chats(limit=10)}
    assert len(chats) == len(CHATS)
    assert all(c["last_read_time"] is None for c in chats.values())
    assert chats["unread@s.whatsapp.net"]["unread"] is True
    assert chats["mine@s.whatsapp.net"]["unread"] is False

    chat = whatsapp.get_chat("read@s.whatsapp.net")
    assert chat["last_read_time"] is None
    assert chat["unread"] is True
