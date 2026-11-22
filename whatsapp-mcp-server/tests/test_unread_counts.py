"""Per-chat unread counts and synced app-state flags (archived, muted, marked unread).

The bridge stores the read marker (chats.last_read_time) and the phone's
archive / mute / mark-as-unread state on the chats row. These tests cover the
MCP side: the count derived from messages, the flags, and list_chats'
unread_only filter, including against stores that predate the columns.
"""

import sqlite3
from datetime import datetime, timedelta

import pytest

import whatsapp

CHATS_SCHEMA = """
    CREATE TABLE chats (
        jid TEXT PRIMARY KEY,
        name TEXT,
        last_message_time TIMESTAMP,
        last_read_time TIMESTAMP,
        archived INTEGER,
        archived_ts INTEGER NOT NULL DEFAULT 0,
        muted_until INTEGER,
        muted_ts INTEGER NOT NULL DEFAULT 0,
        marked_unread INTEGER,
        marked_unread_ts INTEGER NOT NULL DEFAULT 0
    );
"""

LEGACY_CHATS_SCHEMA = """
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
        deleted_at TIMESTAMP,
        PRIMARY KEY (id, chat_jid)
    );
"""

LEGACY_MESSAGES_SCHEMA = MESSAGES_SCHEMA.replace("deleted_at TIMESTAMP,\n", "")


def ts(minute: int) -> str:
    """Bridge timestamp format, minute offsets from 10:00 UTC."""
    return f"2024-01-15 10:{minute:02d}:00+00:00"


def future_ms() -> int:
    return int((datetime.now() + timedelta(days=1)).timestamp() * 1000)


def past_ms() -> int:
    return int((datetime.now() - timedelta(days=1)).timestamp() * 1000)


# chat jid -> (chat columns, messages as (minute, is_from_me, media_type, deleted))
FIXTURE = {
    # Marker at :10; two inbound after it, plus a reaction and a deleted message.
    "counted@s.whatsapp.net": (
        {"last_read_time": ts(10)},
        [
            (5, 0, None, False),
            (11, 0, None, False),
            (12, 0, "image", False),
            (13, 0, "reaction", False),
            (14, 0, None, True),
        ],
    ),
    # Marker behind our own message: a send through the bridge doesn't read
    # the chat on the phone, so with a marker our messages are ignored.
    "replied@s.whatsapp.net": (
        {"last_read_time": ts(1)},
        [(2, 0, None, False), (3, 1, None, False), (4, 0, None, False)],
    ),
    # No marker: our own reaction is not a reply and doesn't reset the count.
    "reacted@s.whatsapp.net": (
        {},
        [(1, 0, None, False), (2, 1, "reaction", False), (3, 0, None, False)],
    ),
    # No marker: count falls back to inbound since our last message.
    "nomarker@s.whatsapp.net": (
        {},
        [(1, 0, None, False), (2, 1, None, False), (3, 0, None, False), (4, 0, None, False)],
    ),
    "read@s.whatsapp.net": ({"last_read_time": ts(30)}, [(20, 0, None, False)]),
    "marked@s.whatsapp.net": ({"last_read_time": ts(30), "marked_unread": 1}, [(20, 0, None, False)]),
    "archived@s.whatsapp.net": ({"last_read_time": ts(0), "archived": 1}, [(21, 0, None, False)]),
    "muted_forever@g.us": ({"last_read_time": ts(0), "muted_until": -1}, [(22, 0, None, False)]),
    "muted_later@s.whatsapp.net": ({"last_read_time": ts(30), "muted_until": "FUTURE"}, [(23, 0, None, False)]),
    "mute_expired@s.whatsapp.net": ({"last_read_time": ts(30), "muted_until": "PAST"}, [(24, 0, None, False)]),
    "status@broadcast": ({}, [(25, 0, None, False)]),
}


def _insert(conn, *, legacy: bool):
    for jid, (columns, messages) in FIXTURE.items():
        last = ts(max(m[0] for m in messages))
        if legacy:
            conn.execute("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", (jid, jid, last))
        else:
            values = dict(columns)
            if values.get("muted_until") == "FUTURE":
                values["muted_until"] = future_ms()
            elif values.get("muted_until") == "PAST":
                values["muted_until"] = past_ms()
            values.update(jid=jid, name=jid, last_message_time=last)
            names = ", ".join(values)
            conn.execute(f"INSERT INTO chats ({names}) VALUES ({', '.join('?' * len(values))})", tuple(values.values()))
        for i, (minute, is_from_me, media_type, deleted) in enumerate(messages):
            row = {
                "id": f"{jid}-{i}",
                "chat_jid": jid,
                "sender": "me" if is_from_me else jid,
                "content": "hi",
                "timestamp": ts(minute),
                "is_from_me": is_from_me,
                "media_type": media_type,
            }
            if deleted and not legacy:
                row["deleted_at"] = ts(minute + 1)
            conn.execute(
                f"INSERT INTO messages ({', '.join(row)}) VALUES ({', '.join('?' * len(row))})", tuple(row.values())
            )


@pytest.fixture
def state_db(tmp_path, monkeypatch):
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    conn.executescript(CHATS_SCHEMA + MESSAGES_SCHEMA)
    _insert(conn, legacy=False)
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))
    return path


@pytest.fixture
def legacy_db(tmp_path, monkeypatch):
    path = tmp_path / "legacy.db"
    conn = sqlite3.connect(path)
    conn.executescript(LEGACY_CHATS_SCHEMA + LEGACY_MESSAGES_SCHEMA)
    _insert(conn, legacy=True)
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))
    return path


def _chats(**kwargs):
    return {c["jid"]: c for c in whatsapp.list_chats(limit=50, **kwargs)}


def test_unread_count_excludes_reactions_deleted_and_read(state_db):
    chats = _chats()
    assert chats["counted@s.whatsapp.net"]["unread_count"] == 2
    assert chats["counted@s.whatsapp.net"]["unread"] is True
    assert chats["replied@s.whatsapp.net"]["unread_count"] == 2
    assert chats["reacted@s.whatsapp.net"]["unread_count"] == 2
    assert chats["nomarker@s.whatsapp.net"]["unread_count"] == 2
    assert chats["read@s.whatsapp.net"]["unread_count"] == 0
    assert chats["read@s.whatsapp.net"]["unread"] is False
    assert chats["status@broadcast"]["unread_count"] == 0
    assert chats["status@broadcast"]["unread"] is False


def test_marked_unread_is_unread_without_new_messages(state_db):
    chat = _chats()["marked@s.whatsapp.net"]
    assert chat["unread_count"] == 0
    assert chat["marked_unread"] is True
    assert chat["unread"] is True


def test_mute_flags(state_db):
    chats = _chats()
    assert chats["muted_forever@g.us"]["muted"] is True
    assert chats["muted_forever@g.us"]["muted_until"] == "always"
    assert chats["muted_later@s.whatsapp.net"]["muted"] is True
    assert datetime.fromisoformat(chats["muted_later@s.whatsapp.net"]["muted_until"]) > datetime.now().astimezone()
    assert chats["mute_expired@s.whatsapp.net"]["muted"] is False
    assert chats["mute_expired@s.whatsapp.net"]["muted_until"] is None
    assert chats["read@s.whatsapp.net"]["muted"] is False


def test_unread_only_leaves_out_archived_but_keeps_muted(state_db):
    unread = set(_chats(unread_only=True))
    assert unread == {
        "counted@s.whatsapp.net",
        "replied@s.whatsapp.net",
        "reacted@s.whatsapp.net",
        "nomarker@s.whatsapp.net",
        "marked@s.whatsapp.net",
        "muted_forever@g.us",
    }
    with_archived = set(_chats(unread_only=True, include_archived=True))
    assert with_archived == unread | {"archived@s.whatsapp.net"}
    # Without unread_only, archived chats are listed and flagged.
    assert _chats()["archived@s.whatsapp.net"]["archived"] is True


def test_unread_only_filters_before_pagination(state_db):
    page = whatsapp.list_chats(limit=2, page=0, unread_only=True)
    assert len(page) == 2
    assert all(c["unread"] for c in page)


def test_get_chat_carries_state(state_db):
    chat = whatsapp.get_chat("muted_forever@g.us")
    assert chat["unread_count"] == 1
    assert chat["muted"] is True
    assert chat["archived"] is False


def test_legacy_store_without_state_columns(legacy_db):
    chats = _chats()
    assert len(chats) == len(FIXTURE)
    assert all(c["archived"] is False and c["muted"] is False and c["marked_unread"] is False for c in chats.values())
    # No marker column: counted since our own last message; the deleted
    # message counts because the store can't say it was deleted.
    assert chats["counted@s.whatsapp.net"]["unread_count"] == 4
    assert chats["replied@s.whatsapp.net"]["unread_count"] == 1
    assert "counted@s.whatsapp.net" in _chats(unread_only=True)
