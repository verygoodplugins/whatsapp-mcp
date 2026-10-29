"""Forwarded messages: the bridge stores WhatsApp's forwarding score in
`forwarding_score` (NULL = written by the sender); the MCP tools surface it as
`forwarded` / `forwarding_score`, and a store from an older bridge still reads."""

import sqlite3

import pytest

import whatsapp

BASE = """
    CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
    CREATE TABLE messages (
        id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
        is_from_me BOOLEAN, media_type TEXT, filename TEXT, quoted_message_id TEXT{extra},
        PRIMARY KEY (id, chat_jid)
    );
"""
JID = "12025550100@s.whatsapp.net"


def make_store(tmp_path, with_forwards: bool) -> str:
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    conn.executescript(BASE.format(extra=", forwarding_score INTEGER" if with_forwards else ""))
    conn.execute("INSERT INTO chats VALUES (?, ?, ?)", (JID, "Test", "2024-01-15 10:31:00+00:00"))
    rows = [
        ("m1", "hello", "2024-01-15 10:30:00+00:00"),
        ("m2", "share this with everyone", "2024-01-15 10:31:00+00:00"),
    ]
    for mid, text, ts in rows:
        conn.execute(
            "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, 0)",
            (mid, JID, JID, text, ts),
        )
    if with_forwards:
        conn.execute("UPDATE messages SET forwarding_score = 7 WHERE id = 'm2'")
    conn.commit()
    conn.close()
    return str(path)


def test_forwarded_message_is_marked(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", make_store(tmp_path, with_forwards=True))
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: jid)
    by_id = {m["id"]: m for m in whatsapp.list_messages(chat_jid=JID, include_context=False)}
    assert by_id["m2"]["forwarded"] is True and by_id["m2"]["forwarding_score"] == 7
    assert by_id["m1"]["forwarded"] is False and by_id["m1"]["forwarding_score"] is None
    ctx = whatsapp.get_message_context("m2", before=1, after=1)
    assert ctx.message.forwarding_score == 7 and ctx.before[0].forwarding_score is None


@pytest.mark.parametrize("include_context", [False, True])
def test_store_without_forward_column_still_reads(tmp_path, monkeypatch, include_context):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", make_store(tmp_path, with_forwards=False))
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: jid)
    msgs = whatsapp.list_messages(chat_jid=JID, include_context=include_context)
    assert {m["id"] for m in msgs} == {"m1", "m2"}
    assert not any(m["forwarded"] for m in msgs)
