"""Contact lookups must anchor on the JID user part.

An unanchored LIKE '%<phone>%' let a short number match a longer, unrelated
JID by substring and return the wrong person's chat or name.
"""

import sqlite3

import pytest

import whatsapp

LONG_JID = "15551234567@s.whatsapp.net"


@pytest.fixture
def lookup_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    conn = sqlite3.connect(db_path)
    conn.executescript(
        """
        CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP, last_read_time TIMESTAMP);
        CREATE TABLE messages (
            id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
            media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
            file_length INTEGER, PRIMARY KEY (id, chat_jid)
        );
        """
    )
    conn.execute(
        "INSERT INTO chats VALUES (?, ?, ?, ?)",
        (LONG_JID, "Long Number", "2024-01-15 10:30:00+00:00", None),
    )
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    monkeypatch.setattr(whatsapp, "_resolve_name_from_whatsmeow", lambda jid: None)
    return db_path


def test_direct_chat_lookup_rejects_substring_match(lookup_db):
    # "5551234" is a substring of the stored JID but not the same contact.
    assert whatsapp.get_direct_chat_by_contact("5551234") is None


def test_direct_chat_lookup_matches_exact_user_part(lookup_db):
    chat = whatsapp.get_direct_chat_by_contact("15551234567")
    assert chat is not None
    assert chat["jid"] == LONG_JID


def test_direct_chat_lookup_accepts_full_jid(lookup_db):
    chat = whatsapp.get_direct_chat_by_contact(LONG_JID)
    assert chat is not None
    assert chat["jid"] == LONG_JID


def test_direct_chat_lookup_treats_like_wildcards_literally(lookup_db):
    assert whatsapp.get_direct_chat_by_contact("%") is None
    assert whatsapp.get_direct_chat_by_contact("_5551234567") is None


def test_sender_name_fallback_does_not_borrow_other_contacts_name(lookup_db):
    name = whatsapp.get_sender_name("5551234@s.whatsapp.net")
    assert name != "Long Number"
