"""Ordering and neighbour lookups must follow instants, not stored text.

The bridge stores timestamps as local wall clock plus UTC offset. Sorting the
raw strings is only chronological while the offset never changes; after a DST
switch or a trip across timezones "10:00:00-04:00" (14:00Z) sorts before
"12:00:00+01:00" (11:00Z). These fixtures mix offsets so a string sort gives
the wrong answer on every assertion.
"""

import sqlite3

import pytest

import whatsapp

CONTACT = "15550000001"
DM = f"{CONTACT}@s.whatsapp.net"
GROUP = "120363000000000001@g.us"
OTHER = "15550000002@s.whatsapp.net"

# (id, chat, sender, stored timestamp, UTC instant)
MESSAGES = [
    ("dm-1400z", DM, CONTACT, "2026-09-07 10:00:00-04:00", "14:00Z"),
    ("dm-1100z", DM, CONTACT, "2026-09-07 12:00:00+01:00", "11:00Z"),
    ("dm-1130z", DM, CONTACT, "2026-09-07 11:30:00+00:00", "11:30Z"),
    ("grp-1200z", GROUP, CONTACT, "2026-09-07 13:00:00+01:00", "12:00Z"),
    ("oth-1300z", OTHER, "15550000002", "2026-09-07 14:00:00+01:00", "13:00Z"),
]
CHATS = [
    (DM, "Contact", "2026-09-07 10:00:00-04:00"),  # newest instant, smallest string
    (GROUP, "Group", "2026-09-07 13:00:00+01:00"),
    (OTHER, "Other", "2026-09-07 14:00:00+01:00"),
]


@pytest.fixture
def mixed_offset_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    conn = sqlite3.connect(db_path)
    conn.executescript(
        """
        CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP, last_read_time TIMESTAMP);
        CREATE TABLE messages (
            id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
            media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
            file_length INTEGER, quoted_message_id TEXT, PRIMARY KEY (id, chat_jid)
        );
        """
    )
    conn.executemany("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", CHATS)
    conn.executemany(
        "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, 0)",
        [(i, c, s, i, ts) for i, c, s, ts, _ in MESSAGES],
    )
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(tmp_path / "absent-whatsapp.db"))
    return db_path


def test_list_messages_orders_by_instant(mixed_offset_db):
    oldest = [m["id"] for m in whatsapp.list_messages(chat_jid=DM, include_context=False, sort_by="oldest")]
    newest = [m["id"] for m in whatsapp.list_messages(chat_jid=DM, include_context=False, sort_by="newest")]
    assert oldest == ["dm-1100z", "dm-1130z", "dm-1400z"]
    assert newest == ["dm-1400z", "dm-1130z", "dm-1100z"]


def test_message_context_neighbours_follow_instants(mixed_offset_db):
    ctx = whatsapp.get_message_context("dm-1130z", before=5, after=5)
    assert [m.id for m in ctx.before] == ["dm-1100z"]
    assert [m.id for m in ctx.after] == ["dm-1400z"]


def test_list_chats_orders_by_instant(mixed_offset_db):
    jids = [c["jid"] for c in whatsapp.list_chats(limit=10, sort_by="last_active")]
    assert jids == [DM, OTHER, GROUP]  # 14:00Z, 13:00Z, 12:00Z


def test_contact_chats_order_by_instant(mixed_offset_db):
    jids = [c["jid"] for c in whatsapp.get_contact_chats(DM)]
    assert jids == [DM, GROUP]


def test_last_interaction_is_latest_instant(mixed_offset_db):
    last = whatsapp.get_last_interaction(DM)
    assert last is not None
    assert last["id"] == "dm-1400z"
