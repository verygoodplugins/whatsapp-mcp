"""Ordering and neighbour lookups must follow instants, not stored text.

The bridge stores timestamps as local wall clock plus UTC offset. Sorting the
raw strings is only chronological while the offset never changes; after a DST
switch or a trip across timezones "10:00:00-04:00" (14:00Z) sorts before
"12:00:00+01:00" (11:00Z). These fixtures mix offsets so a string sort gives
the wrong answer on every assertion. SQLite's date functions apply the offset
but keep only milliseconds, so the same-millisecond fixtures also catch a key
that ties distinct instants.
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

# Distinct instants within one millisecond, inserted out of order and stored at
# offsets that also make a string sort wrong.
SAME_MS_CONTACT = "15550000003"
SAME_MS_DM = f"{SAME_MS_CONTACT}@s.whatsapp.net"
SAME_MS_MESSAGES = [
    ("ms-1234", SAME_MS_DM, SAME_MS_CONTACT, "2026-09-07 12:00:00.1234+00:00", "12:00:00.1234Z"),
    ("ms-1235", SAME_MS_DM, SAME_MS_CONTACT, "2026-09-07 13:00:00.1235+01:00", "12:00:00.1235Z"),
    ("ms-1233", SAME_MS_DM, SAME_MS_CONTACT, "2026-09-07 14:00:00.1233+02:00", "12:00:00.1233Z"),
]
SAME_MS_CHATS = [
    ("15550000021@s.whatsapp.net", "A", "2026-09-07 14:00:00.1233+02:00"),
    ("15550000022@s.whatsapp.net", "B", "2026-09-07 12:00:00.1234+00:00"),
    (SAME_MS_DM, "C", "2026-09-07 13:00:00.1235+01:00"),
]


def _make_db(tmp_path, monkeypatch, chats, messages):
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
    conn.executemany("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", chats)
    conn.executemany(
        "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, 0)",
        [(i, c, s, i, ts) for i, c, s, ts, _ in messages],
    )
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(tmp_path / "absent-whatsapp.db"))
    return db_path


@pytest.fixture
def mixed_offset_db(tmp_path, monkeypatch):
    return _make_db(tmp_path, monkeypatch, CHATS, MESSAGES)


@pytest.fixture
def same_ms_db(tmp_path, monkeypatch):
    return _make_db(tmp_path, monkeypatch, SAME_MS_CHATS, SAME_MS_MESSAGES)


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


def test_list_messages_orders_within_millisecond(same_ms_db):
    oldest = [m["id"] for m in whatsapp.list_messages(chat_jid=SAME_MS_DM, include_context=False, sort_by="oldest")]
    newest = [m["id"] for m in whatsapp.list_messages(chat_jid=SAME_MS_DM, include_context=False, sort_by="newest")]
    assert oldest == ["ms-1233", "ms-1234", "ms-1235"]
    assert newest == ["ms-1235", "ms-1234", "ms-1233"]


def test_message_context_neighbours_within_millisecond(same_ms_db):
    ctx = whatsapp.get_message_context("ms-1234", before=5, after=5)
    assert [m.id for m in ctx.before] == ["ms-1233"]
    assert [m.id for m in ctx.after] == ["ms-1235"]


def test_list_chats_orders_within_millisecond(same_ms_db):
    jids = [c["jid"] for c in whatsapp.list_chats(limit=10, sort_by="last_active")]
    assert jids == [SAME_MS_DM, "15550000022@s.whatsapp.net", "15550000021@s.whatsapp.net"]


def test_last_interaction_within_millisecond(same_ms_db):
    last = whatsapp.get_last_interaction(SAME_MS_DM)
    assert last is not None
    assert last["id"] == "ms-1235"


def test_instant_sql_normalises_offset_and_keeps_fraction():
    conn = sqlite3.connect(":memory:")
    cases = {
        "2024-01-15 14:00:00.12335+02:00": "2024-01-15 12:00:00.123350000",
        # Older SQLite releases round .9995 and up into the next second.
        "2024-01-15 12:00:00.9999+00:00": "2024-01-15 12:00:00.999900000",
        "2024-01-15 07:00:00.5-05:00": "2024-01-15 12:00:00.500000000",
        "2024-01-15 12:00:00+00:00": "2024-01-15 12:00:00.000000000",
        "2024-01-15T12:00:00.123456789Z": "2024-01-15 12:00:00.123456789",
        "not a time": None,
    }
    for stored, key in cases.items():
        assert conn.execute(f"SELECT {whatsapp._instant_sql('?1')}", (stored,)).fetchone()[0] == key, stored
