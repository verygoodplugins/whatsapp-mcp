"""Edited messages: the bridge keeps the current text in `content`, the edit time in
`edited_at` and the first version in `original_content`; the MCP tools surface both,
and a store from a bridge that predates edit tracking still reads (as never edited)."""

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


def make_store(tmp_path, with_edits: bool) -> str:
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    conn.executescript(BASE.format(extra=", edited_at TIMESTAMP, original_content TEXT" if with_edits else ""))
    conn.execute("INSERT INTO chats VALUES (?, ?, ?)", (JID, "Test", "2024-01-15 10:31:00+00:00"))
    rows = [("m1", "hello", "2024-01-15 10:30:00+00:00"), ("m2", "see you at 5", "2024-01-15 10:31:00+00:00")]
    for mid, text, ts in rows:
        conn.execute(
            "INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, 0)",
            (mid, JID, JID, text, ts),
        )
    if with_edits:
        conn.execute(
            "UPDATE messages SET edited_at = ?, original_content = ? WHERE id = 'm2'",
            ("2024-01-15 10:35:00+00:00", "see you at 4"),
        )
    conn.commit()
    conn.close()
    return str(path)


def test_edited_message_carries_its_original_text(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", make_store(tmp_path, with_edits=True))
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: jid)
    by_id = {m["id"]: m for m in whatsapp.list_messages(chat_jid=JID, include_context=False)}
    assert by_id["m2"]["content"] == "see you at 5"
    assert by_id["m2"]["original_content"] == "see you at 4"
    assert by_id["m2"]["edited_at"].startswith("2024-01-15T10:35")
    assert by_id["m1"]["edited_at"] is None and by_id["m1"]["original_content"] is None
    ctx = whatsapp.get_message_context("m2", before=1, after=1)
    assert ctx.message.original_content == "see you at 4" and ctx.before[0].edited_at is None


@pytest.mark.parametrize("include_context", [False, True])
def test_store_without_edit_columns_still_reads(tmp_path, monkeypatch, include_context):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", make_store(tmp_path, with_edits=False))
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: jid)
    msgs = whatsapp.list_messages(chat_jid=JID, include_context=include_context)
    assert {m["id"] for m in msgs} == {"m1", "m2"}
    assert all(m["edited_at"] is None and m["original_content"] is None for m in msgs)
