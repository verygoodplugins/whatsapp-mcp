import sqlite3

import pytest

import scheduling
import whatsapp


class FakeResponse:
    status_code = 200
    text = ""

    def __init__(self, payload):
        self._payload = payload

    def json(self):
        return self._payload


@pytest.fixture
def messages_db(tmp_path, monkeypatch):
    path = tmp_path / "messages.db"
    with sqlite3.connect(path) as conn:
        conn.executescript(
            """
            CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
            CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
                                   is_from_me BOOLEAN);
            CREATE TABLE scheduled_messages (
                id INTEGER PRIMARY KEY, chat_jid TEXT, recipient TEXT, message TEXT, send_at INTEGER,
                status TEXT, attempts INTEGER DEFAULT 0, next_attempt_at INTEGER, last_error TEXT,
                created_at INTEGER, sent_at INTEGER
            );
            INSERT INTO chats VALUES ('553185168687@s.whatsapp.net', 'Vitor', '2026-09-01 10:00:00+00:00');
            """
        )
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(tmp_path / "missing-whatsapp.db"))
    return path


def insert(db, *rows):
    with sqlite3.connect(db) as conn:
        conn.executemany(
            "INSERT INTO scheduled_messages (id, chat_jid, message, send_at, status) VALUES (?, ?, ?, ?, ?)",
            rows,
        )


def test_queues_are_listed_per_chat_in_send_order(messages_db):
    insert(
        messages_db,
        (1, "a@s.whatsapp.net", "a-late", 300, "pending"),
        (2, "b@s.whatsapp.net", "b-only", 100, "pending"),
        (3, "a@s.whatsapp.net", "a-early", 200, "pending"),
    )

    queue = scheduling.list_scheduled_messages()

    assert [(m["message"], m["queue_position"]) for m in queue] == [("a-early", 1), ("a-late", 2), ("b-only", 1)]


def test_failed_head_marks_the_rest_of_its_queue_blocked(messages_db):
    insert(
        messages_db,
        (1, "a@s.whatsapp.net", "failed", 100, "failed"),
        (2, "a@s.whatsapp.net", "waiting", 200, "pending"),
        (3, "b@s.whatsapp.net", "free", 300, "pending"),
    )

    blocked = {m["message"]: m["blocked"] for m in scheduling.list_scheduled_messages()}

    assert blocked == {"failed": True, "waiting": True, "free": False}


def test_finished_messages_are_hidden_unless_requested(messages_db):
    insert(
        messages_db,
        (1, "a@s.whatsapp.net", "sent", 100, "sent"),
        (2, "a@s.whatsapp.net", "pending", 200, "pending"),
    )

    assert [m["message"] for m in scheduling.list_scheduled_messages()] == ["pending"]
    everything = scheduling.list_scheduled_messages(include_finished=True)
    assert [m["message"] for m in everything] == ["sent", "pending"]
    assert "queue_position" not in everything[0]


def test_list_filters_by_chat_typed_as_a_phone_number(messages_db):
    insert(
        messages_db,
        (1, "553185168687@s.whatsapp.net", "mine", 100, "pending"),
        (2, "b@s.whatsapp.net", "other", 100, "pending"),
    )

    assert [m["message"] for m in scheduling.list_scheduled_messages("31 98516-8687")] == ["mine"]


def test_lid_chats_are_keyed_by_phone_jid_like_the_bridge_does(messages_db, tmp_path, monkeypatch):
    whatsmeow_db = tmp_path / "whatsapp.db"
    with sqlite3.connect(whatsmeow_db) as conn:
        conn.executescript(
            """
            CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT);
            CREATE TABLE whatsmeow_contacts (our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT,
                                             push_name TEXT, business_name TEXT);
            INSERT INTO whatsmeow_lid_map VALUES ('218038745993283', '553193393756');
            """
        )
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(whatsmeow_db))
    with sqlite3.connect(messages_db) as conn:
        conn.execute("INSERT INTO chats VALUES ('218038745993283@lid', 'Lid contact', '2026-09-02 10:00:00+00:00')")
    insert(messages_db, (1, "553193393756@s.whatsapp.net", "queued", 100, "pending"))

    assert scheduling.resolve_chat_jid("31 99339-3756") == "553193393756@s.whatsapp.net"
    assert scheduling.resolve_chat_jid("218038745993283@lid") == "553193393756@s.whatsapp.net"
    assert [m["message"] for m in scheduling.list_scheduled_messages("31 99339-3756")] == ["queued"]


def test_list_is_empty_before_the_bridge_creates_the_table(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(tmp_path / "empty.db"))
    assert scheduling.list_scheduled_messages() == []


def test_schedule_sends_resolved_chat_and_offset_timestamp(messages_db, monkeypatch):
    calls = []

    def fake_post(url, json, headers=None):
        calls.append((url, json))
        return FakeResponse({"success": True, "message": "Scheduled"})

    monkeypatch.setattr(scheduling.requests, "post", fake_post)

    result = scheduling.schedule_message("31 98516-8687", "oi", "2026-10-01T18:30:00-03:00")

    assert result["success"] is True
    url, payload = calls[0]
    assert url.endswith("/schedule")
    assert payload == {
        "recipient": "553185168687@s.whatsapp.net",
        "message": "oi",
        "send_at": "2026-10-01T18:30:00-03:00",
    }


def test_schedule_rejects_unparseable_time_without_calling_bridge(monkeypatch):
    monkeypatch.setattr(scheduling.requests, "post", pytest.fail)
    assert scheduling.schedule_message("123", "oi", "tomorrow-ish")["success"] is False


def test_naive_time_is_interpreted_as_local_time():
    assert scheduling.to_rfc3339("2026-10-01T18:30")[:19] == "2026-10-01T18:30:00"
    assert scheduling.to_rfc3339("2026-10-01T18:30")[19] in "+-"
