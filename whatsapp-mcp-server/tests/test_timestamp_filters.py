"""list_messages after/before must compare instants, not strings.

The bridge stores messages.timestamp with the bridge host's UTC offset. A
lexicographic comparison against a UTC or naive bound is off by that offset,
so e.g. after="09:00Z" used to admit a message stamped "10:00+02:00" (08:00Z).
Normalising through SQLite's date functions alone would fix the offset but
collapse distinct instants within one millisecond.
"""

import sqlite3
import time
from datetime import UTC, datetime

import pytest

import whatsapp

CHAT = "15551234567@s.whatsapp.net"

# (id, stored timestamp as go-sqlite3 writes it, UTC instant)
ROWS = [
    ("m-0800z", "2024-01-15 10:00:00+02:00", "08:00Z"),
    ("m-0900z", "2024-01-15 11:00:00+02:00", "09:00Z"),
    ("m-1000z", "2024-01-15 12:00:00.500+02:00", "10:00Z"),
]


# Distinct instants inside one millisecond, the earlier one stored at +02:00.
# SQLite's date functions keep only milliseconds and would read them as equal.
SAME_MS_ROWS = [
    ("m-1233", "2024-01-15 14:00:00.1233+02:00", "12:00:00.1233Z"),
    ("m-1234", "2024-01-15 12:00:00.1234+00:00", "12:00:00.1234Z"),
]


def _make_db(tmp_path, monkeypatch, rows):
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
    conn.execute("INSERT INTO chats VALUES (?, ?, ?, ?)", (CHAT, "Alice", rows[-1][1], None))
    for msg_id, ts, _ in rows:
        conn.execute(
            "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, 0)",
            (msg_id, CHAT, "15551234567", msg_id, ts),
        )
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


@pytest.fixture
def tz_db(tmp_path, monkeypatch):
    return _make_db(tmp_path, monkeypatch, ROWS)


@pytest.fixture
def same_ms_db(tmp_path, monkeypatch):
    return _make_db(tmp_path, monkeypatch, SAME_MS_ROWS)


def _ids(**kwargs):
    return sorted(m["id"] for m in whatsapp.list_messages(include_context=False, sort_by="oldest", **kwargs))


def test_after_compares_instants_not_strings(tz_db):
    # Strictly after 09:00Z: only the 10:00Z row. The old string compare also
    # returned the 08:00Z and 09:00Z rows because "10:00:00+02:00" > "09:00:00+00:00".
    assert _ids(after="2024-01-15T09:00:00Z") == ["m-1000z"]


def test_before_compares_instants_not_strings(tz_db):
    assert _ids(before="2024-01-15T09:30:00+00:00") == ["m-0800z", "m-0900z"]


def test_bounds_accept_any_offset(tz_db):
    # 18:00 at +09:00 is 09:00Z — same instant as above, same answer.
    assert _ids(after="2024-01-15T18:00:00+09:00") == ["m-1000z"]


def test_sub_second_precision_is_kept(tz_db):
    # m-1000z is stamped 10:00:00.500Z; a bound at 10:00:00.250Z must still admit it.
    assert _ids(after="2024-01-15T10:00:00.250Z") == ["m-1000z"]
    assert _ids(after="2024-01-15T10:00:00.750Z") == []


def test_naive_bound_is_read_as_server_local_time(tz_db, monkeypatch):
    monkeypatch.setenv("TZ", "Asia/Tokyo")  # UTC+9, no DST
    time.tzset()
    try:
        # 18:00 Tokyo == 09:00Z
        assert _ids(after="2024-01-15T18:00:00") == ["m-1000z"]
        assert _ids(before="2024-01-15T18:00:00") == ["m-0800z"]
    finally:
        monkeypatch.delenv("TZ")
        time.tzset()


def test_invalid_bound_raises_value_error(tz_db):
    with pytest.raises(ValueError, match="Invalid date format for 'after'"):
        whatsapp.list_messages(after="yesterday", include_context=False)


def test_before_keeps_sub_millisecond_precision(same_ms_db):
    assert _ids(before="2024-01-15T12:00:00.123400Z") == ["m-1233"]


def test_after_keeps_sub_millisecond_precision(same_ms_db):
    assert _ids(after="2024-01-15T12:00:00.123300Z") == ["m-1234"]


def test_bound_keeps_digits_beyond_microseconds(same_ms_db):
    # .12340001 is 10ns after m-1234; datetime alone would truncate it to .123400.
    assert _ids(after="2024-01-15T12:00:00.12340001Z") == []
    assert _ids(before="2024-01-15T12:00:00.12340001Z") == ["m-1233", "m-1234"]


def test_utc_bound_formats_nanoseconds():
    assert whatsapp._utc_bound("2024-01-15T10:00:00.5+02:00", "after") == "2024-01-15 08:00:00.500000000"
    assert whatsapp._utc_bound("2024-01-15T10:00:00Z", "after") == "2024-01-15 10:00:00.000000000"
    assert whatsapp._utc_bound("2024-01-15T10:00:00.123456789Z", "after") == "2024-01-15 10:00:00.123456789"
    aware = datetime(2024, 1, 15, 10, tzinfo=UTC)
    assert whatsapp._utc_bound(aware.isoformat(), "before") == "2024-01-15 10:00:00.000000000"


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
