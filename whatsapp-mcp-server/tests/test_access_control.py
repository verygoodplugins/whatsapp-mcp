import sqlite3
from datetime import datetime

import pytest

import main as mcp_main
import whatsapp

ALLOWED_JID = "11111111111@s.whatsapp.net"
HIDDEN_JID = "22222222222@s.whatsapp.net"
SEND_ONLY_JID = "33333333333@s.whatsapp.net"

OLD_TIME = "2024-01-01 10:00:00+00:00"
GAP_TIME = "2024-01-02 10:00:00+00:00"
NEW_TIME = "2024-01-03 10:00:00+00:00"


def _unix(timestamp: str) -> int:
    return int(datetime.fromisoformat(timestamp).timestamp())


def _make_messages_db(path: str, *, with_permissions: bool = True) -> None:
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE chats (
            jid TEXT PRIMARY KEY,
            name TEXT,
            last_message_time TIMESTAMP
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
            quoted_message_id TEXT,
            PRIMARY KEY (id, chat_jid),
            FOREIGN KEY (chat_jid) REFERENCES chats(jid)
        );
        """
    )
    if with_permissions:
        conn.execute(
            """
            CREATE TABLE mcp_chat_permissions (
                chat_jid TEXT PRIMARY KEY,
                read_new_since_unix INTEGER,
                read_history_through_unix INTEGER,
                can_send INTEGER NOT NULL DEFAULT 0,
                updated_at_unix INTEGER NOT NULL,
                FOREIGN KEY (chat_jid) REFERENCES chats(jid)
            )
            """
        )
        conn.execute(
            """
            CREATE TABLE mcp_access_settings (
                singleton_id INTEGER PRIMARY KEY,
                allow_start_new_conversations INTEGER NOT NULL DEFAULT 0,
                updated_at_unix INTEGER NOT NULL DEFAULT 0,
                revision INTEGER NOT NULL DEFAULT 1
            )
            """
        )
        conn.execute(
            """
            INSERT INTO mcp_access_settings
                (singleton_id, allow_start_new_conversations, updated_at_unix, revision)
            VALUES (1, 0, 0, 1)
            """
        )

    conn.executemany(
        "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
        [
            (ALLOWED_JID, "Allowed Alice", NEW_TIME),
            (HIDDEN_JID, "Hidden Bob", NEW_TIME),
            (SEND_ONLY_JID, "Send Only Carol", NEW_TIME),
        ],
    )
    conn.executemany(
        """
        INSERT INTO messages
            (id, chat_jid, sender, content, timestamp, is_from_me, media_type)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        """,
        [
            ("allowed-old", ALLOWED_JID, ALLOWED_JID, "old readable", OLD_TIME, 0, None),
            ("allowed-gap", ALLOWED_JID, ALLOWED_JID, "revoked gap", GAP_TIME, 0, None),
            ("allowed-new", ALLOWED_JID, ALLOWED_JID, "new readable", NEW_TIME, 0, "image"),
            ("hidden-new", HIDDEN_JID, HIDDEN_JID, "hidden content", NEW_TIME, 0, None),
            (
                "send-only-new",
                SEND_ONLY_JID,
                SEND_ONLY_JID,
                "send-only preview",
                NEW_TIME,
                0,
                None,
            ),
        ],
    )
    conn.commit()
    conn.close()


@pytest.fixture
def access_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    _make_messages_db(str(db_path))
    missing_whatsmeow = tmp_path / "missing-whatsapp.db"
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(missing_whatsmeow))
    return db_path


def _set_access(
    db_path,
    chat_jid: str,
    *,
    read_new_since: int | None = None,
    read_history_through: int | None = None,
    can_send: bool = False,
) -> None:
    conn = sqlite3.connect(db_path)
    conn.execute(
        """
        INSERT OR REPLACE INTO mcp_chat_permissions
            (chat_jid, read_new_since_unix, read_history_through_unix, can_send, updated_at_unix)
        VALUES (?, ?, ?, ?, ?)
        """,
        (chat_jid, read_new_since, read_history_through, int(can_send), _unix(NEW_TIME)),
    )
    conn.commit()
    conn.close()


def _set_start_new_conversations(db_path, enabled: bool) -> None:
    conn = sqlite3.connect(db_path)
    conn.execute(
        """
        UPDATE mcp_access_settings
        SET allow_start_new_conversations = ?
        WHERE singleton_id = 1
        """,
        (int(enabled),),
    )
    conn.commit()
    conn.close()


class DummyResponse:
    status_code = 200
    text = "OK"

    def __init__(self, payload=None):
        self._payload = payload or {"success": True, "message": "sent", "path": "/tmp/media.jpg"}

    def json(self):
        return self._payload


def test_table_presence_enables_default_deny_for_all_discovery_paths(access_db, monkeypatch):
    assert whatsapp.list_messages(include_context=False) == []
    assert whatsapp.list_chats() == []
    assert whatsapp.get_chat(HIDDEN_JID) is None
    assert whatsapp.get_direct_chat_by_contact(HIDDEN_JID.split("@")[0]) is None
    assert whatsapp.get_last_interaction(HIDDEN_JID) is None
    assert whatsapp.get_contact_chats(HIDDEN_JID) == []
    assert whatsapp.search_contacts("") == []

    def unexpected_name_lookup(_jid):
        raise AssertionError("hidden contact name fallback must not run")

    monkeypatch.setattr(mcp_main, "whatsapp_get_sender_name", unexpected_name_lookup)
    contact = mcp_main.get_contact(identifier=HIDDEN_JID)
    assert contact["resolved"] is False
    assert contact["display_name"] is None


def test_independent_read_windows_preserve_revocation_gap_and_context(access_db):
    _set_access(
        access_db,
        ALLOWED_JID,
        read_new_since=_unix(NEW_TIME),
        read_history_through=_unix(OLD_TIME),
    )

    messages = whatsapp.list_messages(
        chat_jid=ALLOWED_JID,
        include_context=False,
        sort_by="oldest",
    )
    assert [message["id"] for message in messages] == ["allowed-old", "allowed-new"]

    context = whatsapp.get_message_context(
        "allowed-new",
        before=5,
        after=5,
        chat_jid=ALLOWED_JID,
    )
    assert [message.id for message in context.before] == ["allowed-old"]
    with pytest.raises(ValueError, match="not found or not readable"):
        whatsapp.get_message_context("allowed-gap", chat_jid=ALLOWED_JID)

    assert whatsapp.get_last_interaction(ALLOWED_JID)["id"] == "allowed-new"
    assert whatsapp.get_contact_chats(ALLOWED_JID)[0]["jid"] == ALLOWED_JID


def test_history_only_and_new_only_are_independent(access_db):
    _set_access(access_db, ALLOWED_JID, read_history_through=_unix(OLD_TIME))
    history = whatsapp.list_messages(chat_jid=ALLOWED_JID, include_context=False)
    assert [message["id"] for message in history] == ["allowed-old"]

    _set_access(access_db, ALLOWED_JID, read_new_since=_unix(NEW_TIME))
    new_messages = whatsapp.list_messages(chat_jid=ALLOWED_JID, include_context=False)
    assert [message["id"] for message in new_messages] == ["allowed-new"]


def test_chat_previews_use_latest_readable_message_and_redact_send_only(access_db):
    _set_access(access_db, ALLOWED_JID, read_history_through=_unix(OLD_TIME))
    _set_access(access_db, SEND_ONLY_JID, can_send=True)

    chats = {chat["jid"]: chat for chat in whatsapp.list_chats(limit=10)}
    assert set(chats) == {ALLOWED_JID, SEND_ONLY_JID}
    assert chats[ALLOWED_JID]["last_message"] == "old readable"
    assert chats[ALLOWED_JID]["last_message_time"] == datetime.fromisoformat(OLD_TIME).isoformat()
    assert chats[SEND_ONLY_JID]["last_message"] is None
    assert chats[SEND_ONLY_JID]["last_message_time"] is None

    send_only = whatsapp.get_chat(SEND_ONLY_JID)
    assert send_only["name"] == "Send Only Carol"
    assert send_only["last_message"] is None
    assert whatsapp.get_chat(HIDDEN_JID) is None


@pytest.mark.parametrize(
    ("function_name", "args"),
    [
        ("send_message", (HIDDEN_JID, "hello")),
        ("send_file", (HIDDEN_JID, "/does/not/exist")),
        ("send_audio_message", (HIDDEN_JID, "/does/not/exist")),
        ("send_reaction", (HIDDEN_JID, "hidden-new", "👍")),
    ],
)
def test_all_outbound_helpers_deny_before_post(access_db, monkeypatch, function_name, args):
    def unexpected_post(*_args, **_kwargs):
        raise AssertionError("denied outbound operation must not call the bridge")

    monkeypatch.setattr(whatsapp.requests, "post", unexpected_post)
    success, message = getattr(whatsapp, function_name)(*args)
    assert success is False
    assert "not allowed" in message


def test_send_only_permission_allows_send_without_read(access_db, monkeypatch):
    _set_access(access_db, SEND_ONLY_JID, can_send=True)
    calls = []

    def fake_post(url, json, headers=None):
        calls.append((url, json, headers))
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)
    success, _ = whatsapp.send_message(SEND_ONLY_JID.split("@")[0], "hello")

    assert success is True
    assert calls[0][1]["recipient"] == SEND_ONLY_JID
    assert whatsapp.list_messages(chat_jid=SEND_ONLY_JID, include_context=False) == []


def test_global_setting_allows_starting_new_direct_conversations(access_db, tmp_path, monkeypatch):
    _set_start_new_conversations(access_db, True)
    new_phone = "15559876543"
    media_path = tmp_path / "first-message.ogg"
    media_path.write_bytes(b"test media")
    calls = []

    def fake_post(url, json, headers=None):
        calls.append((url, json, headers))
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    assert whatsapp.send_message(new_phone, "hello")[0] is True
    assert whatsapp.send_file(new_phone, str(media_path))[0] is True
    assert whatsapp.send_audio_message(new_phone, str(media_path))[0] is True
    assert [call[1]["recipient"] for call in calls] == [
        f"{new_phone}@s.whatsapp.net",
        f"{new_phone}@s.whatsapp.net",
        f"{new_phone}@s.whatsapp.net",
    ]


def test_global_start_setting_does_not_override_existing_or_noninitial_actions(access_db, monkeypatch):
    _set_start_new_conversations(access_db, True)
    new_phone = "15559876543"

    def unexpected_post(*_args, **_kwargs):
        raise AssertionError("non-initial or explicitly blocked operation must not call the bridge")

    monkeypatch.setattr(whatsapp.requests, "post", unexpected_post)

    assert whatsapp.send_message(HIDDEN_JID, "blocked")[0] is False
    assert (
        whatsapp.send_message(
            new_phone,
            "quoted",
            quoted_message_id="unknown-message",
        )[0]
        is False
    )
    assert whatsapp.send_reaction(new_phone, "unknown-message", "👍")[0] is False
    assert whatsapp.send_message("120363099999999999@g.us", "group")[0] is False
    assert whatsapp.send_message("not-a-phone", "invalid")[0] is False


def test_download_requires_the_specific_message_to_be_readable(access_db, monkeypatch):
    _set_access(
        access_db,
        ALLOWED_JID,
        read_new_since=_unix(NEW_TIME),
        read_history_through=_unix(OLD_TIME),
    )
    calls = []

    def fake_post(url, json, headers=None):
        calls.append(json)
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    assert whatsapp.download_media("allowed-new", ALLOWED_JID) == "/tmp/media.jpg"
    assert whatsapp.download_media("allowed-gap", ALLOWED_JID) is None
    assert whatsapp.download_media("allowed-new", HIDDEN_JID) is None
    assert calls == [{"message_id": "allowed-new", "chat_jid": ALLOWED_JID}]


def test_contact_search_only_returns_visible_direct_chats(access_db):
    _set_access(access_db, ALLOWED_JID, read_new_since=_unix(NEW_TIME))
    _set_access(access_db, SEND_ONLY_JID, can_send=True)

    contacts = whatsapp.search_contacts("")
    assert {contact["jid"] for contact in contacts} == {ALLOWED_JID, SEND_ONLY_JID}
    assert whatsapp.get_direct_chat_by_contact(ALLOWED_JID.split("@")[0])["jid"] == ALLOWED_JID
    assert whatsapp.get_direct_chat_by_contact(HIDDEN_JID.split("@")[0]) is None


def test_lid_alias_resolves_to_canonical_phone_permission(access_db, tmp_path, monkeypatch):
    lid = "98765432101234"
    whatsmeow_path = tmp_path / "whatsapp.db"
    conn = sqlite3.connect(whatsmeow_path)
    conn.execute("CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT)")
    conn.execute(
        "INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)",
        (lid, ALLOWED_JID.split("@")[0]),
    )
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(whatsmeow_path))
    _set_access(access_db, ALLOWED_JID, can_send=True)
    calls = []

    def fake_post(url, json, headers=None):
        calls.append(json)
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)
    success, _ = whatsapp.send_message(f"{lid}@lid", "hello")

    assert success is True
    assert calls[0]["recipient"] == ALLOWED_JID
    assert whatsapp.get_chat(f"{lid}@lid")["jid"] == ALLOWED_JID


def test_missing_permissions_table_preserves_legacy_allow(tmp_path, monkeypatch):
    db_path = tmp_path / "legacy.db"
    _make_messages_db(str(db_path), with_permissions=False)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(tmp_path / "missing.db"))
    monkeypatch.setenv("WHATSAPP_MCP_LEGACY_ALLOW_ALL", "true")
    monkeypatch.setattr(whatsapp.requests, "post", lambda *_args, **_kwargs: DummyResponse())

    assert len(whatsapp.list_chats(limit=10)) == 3
    assert len(whatsapp.list_messages(include_context=False)) == 5
    assert whatsapp.send_message(HIDDEN_JID, "legacy")[0] is True


def test_missing_permissions_table_fails_closed_without_explicit_opt_in(tmp_path, monkeypatch):
    db_path = tmp_path / "missing-acl.db"
    _make_messages_db(str(db_path), with_permissions=False)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(tmp_path / "missing.db"))
    monkeypatch.delenv("WHATSAPP_MCP_LEGACY_ALLOW_ALL", raising=False)

    def unexpected_post(*_args, **_kwargs):
        raise AssertionError("missing ACL state must not call the bridge")

    monkeypatch.setattr(whatsapp.requests, "post", unexpected_post)

    assert whatsapp.access_control_enabled() is True
    assert whatsapp.list_chats(limit=10) == []
    assert whatsapp.list_messages(include_context=False) == []
    assert whatsapp.send_message(HIDDEN_JID, "denied")[0] is False
    assert whatsapp.download_media("hidden-new", HIDDEN_JID) is None


def test_malformed_permissions_table_fails_closed(access_db, monkeypatch):
    conn = sqlite3.connect(access_db)
    conn.execute("DROP TABLE mcp_chat_permissions")
    conn.execute("CREATE TABLE mcp_chat_permissions (chat_jid TEXT PRIMARY KEY)")
    conn.commit()
    conn.close()

    def unexpected_post(*_args, **_kwargs):
        raise AssertionError("ACL query failure must not call the bridge")

    monkeypatch.setattr(whatsapp.requests, "post", unexpected_post)
    assert whatsapp.list_messages(include_context=False) == []
    assert whatsapp.list_chats() == []
    assert whatsapp.send_message(ALLOWED_JID, "denied")[0] is False
