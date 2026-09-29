"""Tests for falling back to whatsmeow's contact store for chat names.

messages.db stores the bare numeric user-part as chats.name whenever the bridge
could not resolve a display name. get_sender_name already consults
whatsmeow_contacts for message senders, but the chat-returning functions used
chats.name verbatim, so a chat whose name only ever became available after a
contact sync kept showing a phone number.
"""

import sqlite3

import pytest

import whatsapp

PEER = "31600000001@s.whatsapp.net"
NAMED = "31600000002@s.whatsapp.net"
GROUP = "120363000000000001@g.us"
LID_MAPPED = "111111111111111@lid"
LID_UNMAPPED = "222222222222222@lid"


def _make_messages_db(path):
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE chats (
            jid TEXT PRIMARY KEY,
            name TEXT,
            last_message_time TIMESTAMP,
            last_read_time TIMESTAMP
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
            PRIMARY KEY (id, chat_jid)
        );
        """
    )
    rows = [
        # Placeholder: name is just the number, as the bridge wrote it.
        (PEER, "31600000001", "2024-01-15 10:00:00+00:00"),
        # A real name that must never be second-guessed.
        (NAMED, "Bert", "2024-01-15 09:00:00+00:00"),
        # Group placeholder.
        (GROUP, "Group 120363000000000001", "2024-01-15 08:00:00+00:00"),
        # LID chats: one resolvable via whatsmeow_lid_map, one not.
        (LID_MAPPED, "111111111111111", "2024-01-15 07:00:00+00:00"),
        (LID_UNMAPPED, "222222222222222", "2024-01-15 06:00:00+00:00"),
    ]
    conn.executemany("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", rows)
    conn.commit()
    conn.close()


def _make_whatsmeow_db(path):
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE whatsmeow_contacts (
            our_jid TEXT,
            their_jid TEXT,
            first_name TEXT,
            full_name TEXT,
            push_name TEXT,
            business_name TEXT,
            PRIMARY KEY (our_jid, their_jid)
        );
        CREATE TABLE whatsmeow_lid_map (
            lid TEXT PRIMARY KEY,
            pn TEXT UNIQUE NOT NULL
        );
        """
    )
    conn.executemany(
        """INSERT INTO whatsmeow_contacts
           (our_jid, their_jid, first_name, full_name, push_name, business_name)
           VALUES (?, ?, ?, ?, ?, ?)""",
        [
            ("me@s.whatsapp.net", PEER, "", "Ada Lovelace", "Ada", ""),
            # A name also exists for the chat that already has one — it must lose.
            ("me@s.whatsapp.net", NAMED, "", "Should Not Win", "", ""),
            ("me@s.whatsapp.net", "31600000003@s.whatsapp.net", "", "Via LID", "", ""),
        ],
    )
    conn.execute(
        "INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)",
        ("111111111111111", "31600000003"),
    )
    conn.commit()
    conn.close()


@pytest.fixture
def dbs(tmp_path, monkeypatch):
    messages = tmp_path / "messages.db"
    whatsmeow = tmp_path / "whatsapp.db"
    _make_messages_db(str(messages))
    _make_whatsmeow_db(str(whatsmeow))
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(messages))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(whatsmeow))
    return messages, whatsmeow


def _by_jid(chats):
    return {chat["jid"]: chat for chat in chats}


def test_list_chats_resolves_placeholder_from_contact_store(dbs):
    chats = _by_jid(whatsapp.list_chats(limit=50))
    assert chats[PEER]["name"] == "Ada Lovelace"


def test_list_chats_keeps_real_stored_name(dbs):
    chats = _by_jid(whatsapp.list_chats(limit=50))
    assert chats[NAMED]["name"] == "Bert"


def test_list_chats_resolves_lid_via_lid_map(dbs):
    chats = _by_jid(whatsapp.list_chats(limit=50))
    assert chats[LID_MAPPED]["name"] == "Via LID"


def test_list_chats_keeps_placeholder_when_unresolvable(dbs):
    """A LID that is not in whatsmeow_lid_map has nothing to resolve to."""
    chats = _by_jid(whatsapp.list_chats(limit=50))
    assert chats[LID_UNMAPPED]["name"] == "222222222222222"
    assert chats[GROUP]["name"] == "Group 120363000000000001"


def test_get_chat_resolves_placeholder(dbs):
    chat = whatsapp.get_chat(PEER, include_last_message=False)
    assert chat is not None
    assert chat["name"] == "Ada Lovelace"


def test_missing_whatsmeow_db_is_not_fatal(dbs, monkeypatch):
    """The placeholder survives when whatsapp.db is absent."""
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", "/nonexistent/whatsapp.db")
    chats = _by_jid(whatsapp.list_chats(limit=50))
    assert chats[PEER]["name"] == "31600000001"
    assert chats[NAMED]["name"] == "Bert"


@pytest.mark.parametrize(
    ("jid", "name", "expected"),
    [
        (PEER, None, True),
        (PEER, "", True),
        (PEER, "31600000001", True),
        (GROUP, "Group 120363000000000001", True),
        (PEER, "Ada Lovelace", False),
        (PEER, "31699999999", False),
        (GROUP, "Family", False),
    ],
)
def test_is_placeholder_chat_name(jid, name, expected):
    assert whatsapp._is_placeholder_chat_name(jid, name) is expected
