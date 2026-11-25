"""Starred messages: the `starred` flag, list_messages' starred_only filter, and star_message.

The bridge keeps stars in starred_messages (chat_jid, message_id, starred,
starred_ts), including unstars (starred = 0) and stars on messages it never
stored. Reads must keep working against a store from a bridge without the table.
"""

import sqlite3

import pytest

import whatsapp

SCHEMA = """
    CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
    CREATE TABLE messages (
        id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
        is_from_me BOOLEAN, media_type TEXT, filename TEXT, quoted_message_id TEXT,
        PRIMARY KEY (id, chat_jid)
    );
"""
STARRED_SCHEMA = """
    CREATE TABLE starred_messages (
        chat_jid TEXT NOT NULL, message_id TEXT NOT NULL,
        starred INTEGER NOT NULL, starred_ts INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY (chat_jid, message_id)
    );
"""

ALICE = "111@s.whatsapp.net"
BOB = "222@s.whatsapp.net"


def _write_db(path, *, with_starred_table: bool):
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA + (STARRED_SCHEMA if with_starred_table else ""))
    for jid in (ALICE, BOB):
        conn.execute("INSERT INTO chats VALUES (?, ?, ?)", (jid, jid, "2024-01-15 10:09:00+00:00"))
    for minute, (msg_id, jid) in enumerate([("a1", ALICE), ("a2", ALICE), ("a3", ALICE), ("b1", BOB)]):
        conn.execute(
            "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, 0)",
            (msg_id, jid, jid.split("@")[0], f"text {msg_id}", f"2024-01-15 10:0{minute}:00+00:00"),
        )
    if with_starred_table:
        conn.executemany(
            "INSERT INTO starred_messages VALUES (?, ?, ?, 1)",
            [
                (ALICE, "a2", 1),
                (ALICE, "a3", 0),  # unstarred later
                (BOB, "a2", 0),  # same id, other chat: must not leak into ALICE's a2
                (BOB, "never-stored", 1),
            ],
        )
    conn.commit()
    conn.close()


@pytest.fixture
def starred_db(tmp_path, monkeypatch):
    path = tmp_path / "messages.db"
    _write_db(path, with_starred_table=True)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))


@pytest.fixture
def legacy_db(tmp_path, monkeypatch):
    path = tmp_path / "legacy.db"
    _write_db(path, with_starred_table=False)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))


def _starred_by_id(**kwargs):
    return {m["id"]: m["starred"] for m in whatsapp.list_messages(include_context=False, **kwargs)}


def test_list_messages_reports_starred(starred_db):
    assert _starred_by_id() == {"a1": False, "a2": True, "a3": False, "b1": False}


def test_starred_only_returns_only_starred_stored_messages(starred_db):
    assert [m["id"] for m in whatsapp.list_messages(starred_only=True, include_context=False)] == ["a2"]


def test_starred_only_context_keeps_neighbours_with_their_own_flag(starred_db):
    messages = whatsapp.list_messages(starred_only=True, context_before=1, context_after=1)
    assert {m["id"]: m["starred"] for m in messages} == {"a1": False, "a2": True, "a3": False}


def test_get_message_context_reports_starred(starred_db):
    context = whatsapp.get_message_context("a1", before=0, after=1)
    assert context.message.starred is False
    assert [(m.id, m.starred) for m in context.after] == [("a2", True)]


def test_get_last_interaction_reports_starred(starred_db):
    assert whatsapp.get_last_interaction(ALICE)["starred"] is False  # a3 was unstarred
    conn = sqlite3.connect(whatsapp.MESSAGES_DB_PATH)
    conn.execute("UPDATE starred_messages SET starred = 1 WHERE chat_jid = ? AND message_id = 'a3'", (ALICE,))
    conn.commit()
    conn.close()
    assert whatsapp.get_last_interaction(ALICE)["starred"] is True


def test_store_without_starred_table(legacy_db):
    assert _starred_by_id() == {"a1": False, "a2": False, "a3": False, "b1": False}
    assert whatsapp.list_messages(starred_only=True, include_context=False) == []


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload if payload is not None else {"success": True, "message": "Message starred"}
        self.text = text

    def json(self):
        return self._payload


def _capture_post(monkeypatch, calls, response):
    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json})
        return response

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)


@pytest.mark.parametrize("starred", [True, False])
def test_star_message_posts_to_bridge(monkeypatch, starred):
    calls = []
    _capture_post(monkeypatch, calls, DummyResponse())

    success, _ = whatsapp.star_message(ALICE, " a2 ", starred)

    assert success is True
    assert len(calls) == 1
    assert calls[0]["url"].endswith("/star")
    assert calls[0]["json"] == {"chat_jid": ALICE, "message_id": "a2", "starred": starred}


def test_star_message_reports_unknown_message(monkeypatch):
    _capture_post(monkeypatch, [], DummyResponse(status_code=404, text="not found"))
    success, message = whatsapp.star_message(ALICE, "missing")
    assert success is False
    assert "not found" in message


@pytest.mark.parametrize("chat_jid, message_id", [("", "a2"), (ALICE, " ")])
def test_star_message_requires_chat_and_id(monkeypatch, chat_jid, message_id):
    calls = []
    _capture_post(monkeypatch, calls, DummyResponse())
    success, _ = whatsapp.star_message(chat_jid, message_id)
    assert success is False
    assert calls == []
