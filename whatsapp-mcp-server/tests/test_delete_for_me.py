"""Tests for delete_messages_for_me (WhatsApp "Delete for me")."""

import asyncio
import sqlite3

import pytest

import main
import whatsapp

CHAT = "12025551234@s.whatsapp.net"
OTHER_CHAT = "12025559999@s.whatsapp.net"


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload if payload is not None else {"success": True, "message": "deleted"}
        self.text = text

    def json(self):
        return self._payload


@pytest.fixture
def messages_db(tmp_path, monkeypatch):
    # Timestamps use the "YYYY-MM-DD HH:MM:SS-03:00" form go-sqlite3 writes.
    rows = [
        ("a1", CHAT, "me", "late on the 12th", "2026-10-12 23:59:59-03:00", 1, None, None),
        ("a2", CHAT, "peer", "start of the 13th", "2026-10-13 00:00:00-03:00", 0, None, None),
        ("a3", CHAT, "me", "noon on the 13th", "2026-10-13 12:00:00-03:00", 1, "image", "pic.jpg"),
        ("a4", CHAT, "peer", "reaction on the 13th", "2026-10-13 12:01:00-03:00", 0, "reaction", "a3"),
        ("a5", CHAT, "peer", "start of the 14th", "2026-10-14 00:00:00-03:00", 0, None, None),
        ("b1", OTHER_CHAT, "peer", "other chat on the 13th", "2026-10-13 09:00:00-03:00", 0, None, None),
    ]
    db_path = tmp_path / "messages.db"
    conn = sqlite3.connect(db_path)
    conn.executescript(
        """
        CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
        CREATE TABLE messages (
            id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
            is_from_me BOOLEAN, media_type TEXT, filename TEXT,
            PRIMARY KEY (id, chat_jid)
        );
        """
    )
    conn.executemany("INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)", rows)
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


# --- whatsapp.select_messages_for_deletion ---------------------------------


def test_select_by_range_is_half_open_scoped_to_chat_and_skips_reactions(messages_db):
    found = whatsapp.select_messages_for_deletion(
        CHAT, after="2026-10-13T00:00:00-03:00", before="2026-10-14T00:00:00-03:00"
    )

    assert [m["id"] for m in found] == ["a2", "a3"]
    assert found[0]["is_from_me"] is False
    assert found[1]["is_from_me"] is True
    assert found[1]["media_type"] == "image"
    assert "content" not in found[0]


def test_select_by_ids_ignores_unknown_ids_and_other_chats(messages_db):
    found = whatsapp.select_messages_for_deletion(CHAT, message_ids=["a1", "b1", "nope"])

    assert [m["id"] for m in found] == ["a1"]


def test_select_respects_limit(messages_db):
    found = whatsapp.select_messages_for_deletion(CHAT, after="2026-10-01", before="2026-11-01", limit=2)

    assert [m["id"] for m in found] == ["a1", "a2"]


@pytest.mark.parametrize(
    "kwargs",
    [
        {},  # no selector at all
        {"after": "2026-10-13"},  # one bound only would widen to the whole chat
        {"before": "2026-10-14"},
        {"message_ids": ["a1"], "after": "2026-10-13", "before": "2026-10-14"},
    ],
)
def test_select_rejects_ambiguous_selectors(messages_db, kwargs):
    with pytest.raises(ValueError):
        whatsapp.select_messages_for_deletion(CHAT, **kwargs)


def test_select_rejects_invalid_date_and_oversized_id_list(messages_db):
    with pytest.raises(ValueError, match="after"):
        whatsapp.select_messages_for_deletion(CHAT, after="yesterday", before="2026-10-14")
    with pytest.raises(ValueError, match="At most"):
        whatsapp.select_messages_for_deletion(
            CHAT, message_ids=[f"m{i}" for i in range(whatsapp.MAX_DELETE_PER_CALL + 1)]
        )


def test_select_by_many_ids_spans_sql_chunks_and_stays_ordered(messages_db):
    # More IDs than one IN() chunk, with real hits on both sides of the chunk boundary.
    ids = ["a5"] + [f"nope{i}" for i in range(whatsapp._SQL_IN_CHUNK + 50)] + ["a1", "a2"]

    found = whatsapp.select_messages_for_deletion(CHAT, message_ids=ids)

    assert [m["id"] for m in found] == ["a1", "a2", "a5"]


# --- whatsapp.delete_messages_for_me (bridge client) ------------------------


def test_delete_messages_for_me_posts_payload_with_auth(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")

    def fake_post(url, json, headers=None):
        calls.append({"url": url, "json": json, "headers": headers})
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    success, msg = whatsapp.delete_messages_for_me(CHAT, [" a2 ", "a3"])

    assert success is True
    assert msg == "deleted"
    assert calls[0]["url"].endswith("/delete-for-me")
    assert calls[0]["json"] == {"chat_jid": CHAT, "message_ids": ["a2", "a3"]}
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}


@pytest.mark.parametrize(
    ("chat_jid", "ids", "fragment"),
    [
        ("", ["a2"], "Chat JID"),
        (CHAT, [], "message ID"),
        (CHAT, ["a2", "  "], "message ID"),
        (CHAT, [f"m{i}" for i in range(201)], "At most"),
    ],
)
def test_delete_messages_for_me_validates_before_calling_bridge(monkeypatch, chat_jid, ids, fragment):
    def fail_post(*args, **kwargs):
        raise AssertionError("bridge must not be called")

    monkeypatch.setattr(whatsapp.requests, "post", fail_post)

    success, msg = whatsapp.delete_messages_for_me(chat_jid, ids)

    assert success is False
    assert fragment in msg


def test_delete_messages_for_me_surfaces_bridge_error(monkeypatch):
    monkeypatch.setattr(
        whatsapp.requests,
        "post",
        lambda url, json, headers=None: DummyResponse(status_code=404, text="messages not found: a2"),
    )

    success, msg = whatsapp.delete_messages_for_me(CHAT, ["a2"])

    assert success is False
    assert "HTTP 404" in msg
    assert "messages not found" in msg


# --- whatsapp.delete_messages_for_me_batched ---------------------------------


def test_batched_splits_into_bridge_sized_requests(monkeypatch):
    sizes = []

    def fake_post(url, json, headers=None):
        sizes.append(len(json["message_ids"]))
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    result = whatsapp.delete_messages_for_me_batched(CHAT, [f"m{i}" for i in range(450)])

    assert sizes == [200, 200, 50]
    assert result == {"success": True, "deleted": 450, "batches": 3, "message": "450 message(s) deleted for me"}


def test_batched_stops_at_first_failure_and_reports_exact_progress(monkeypatch):
    responses = iter([DummyResponse(), DummyResponse(status_code=503, text="not connected")])
    calls = []

    def fake_post(url, json, headers=None):
        calls.append(len(json["message_ids"]))
        return next(responses)

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)

    result = whatsapp.delete_messages_for_me_batched(CHAT, [f"m{i}" for i in range(450)])

    assert calls == [200, 200], "no third request after the failure"
    assert result["success"] is False
    assert (result["deleted"], result["batches"], result["failed_batch"]) == (200, 1, 2)
    assert "HTTP 503" in result["message"]


# --- main.delete_messages_for_me (MCP tool) ---------------------------------


def _row(message_id, **overrides):
    row = {
        "id": message_id,
        "timestamp": "2026-10-13 12:00:00-03:00",
        "sender": "peer",
        "is_from_me": False,
        "media_type": None,
    }
    row.update(overrides)
    return row


@pytest.fixture
def tool_calls(monkeypatch):
    """Stub the DB selection and the bridge call; record what reached the bridge."""
    state = {"selection": [_row("a2"), _row("a3", is_from_me=True)], "deleted": []}

    monkeypatch.setattr(main, "whatsapp_select_messages_for_deletion", lambda *a, **k: state["selection"])

    def fake_delete(chat_jid, ids):
        state["deleted"].append((chat_jid, ids))
        return {"success": True, "deleted": len(ids), "batches": 1, "message": "Messages deleted for me"}

    monkeypatch.setattr(main, "whatsapp_delete_messages_for_me_batched", fake_delete)
    return state


def test_tool_defaults_to_dry_run_and_never_calls_bridge(tool_calls):
    result = main.delete_messages_for_me(CHAT, after="2026-10-13", before="2026-10-14")

    assert result["success"] is True
    assert result["dry_run"] is True
    assert result["count"] == 2
    assert [m["id"] for m in result["messages"]] == ["a2", "a3"]
    assert all("content" not in m for m in result["messages"])
    assert tool_calls["deleted"] == []


def test_tool_confirm_deletes_exactly_the_selected_ids(tool_calls):
    result = main.delete_messages_for_me(CHAT, after="2026-10-13", before="2026-10-14", confirm=True)

    assert result == {"success": True, "deleted": 2, "message": "Messages deleted for me"}
    assert tool_calls["deleted"] == [(CHAT, ["a2", "a3"])]


def test_tool_reports_partial_progress_when_a_batch_fails(tool_calls, monkeypatch):
    failure = {"success": False, "deleted": 200, "batches": 1, "failed_batch": 2, "message": "Error: HTTP 503"}
    monkeypatch.setattr(main, "whatsapp_delete_messages_for_me_batched", lambda chat_jid, ids: failure)

    result = main.delete_messages_for_me(CHAT, message_ids=["a2", "a3"], confirm=True)

    assert result == {
        "success": False,
        "error_code": "bridge_error",
        "message": "Error: HTTP 503",
        "deleted": 200,
        "failed_batch": 2,
    }


def test_tool_refuses_when_nothing_matches(tool_calls):
    tool_calls["selection"] = []

    result = main.delete_messages_for_me(CHAT, after="2026-10-13", before="2026-10-14", confirm=True)

    assert result["success"] is False
    assert result["error_code"] == "no_match"
    assert "sample of the history" in result["message"], "tell the agent why a visible message can be missing"
    assert tool_calls["deleted"] == []


def test_tool_refuses_oversized_selection(tool_calls):
    tool_calls["selection"] = [_row(f"m{i}") for i in range(whatsapp.MAX_DELETE_PER_CALL + 1)]

    result = main.delete_messages_for_me(CHAT, after="2026-10-01", before="2026-11-01", confirm=True)

    assert result["success"] is False
    assert result["error_code"] == "too_many"
    assert result["max"] == whatsapp.MAX_DELETE_PER_CALL
    assert tool_calls["deleted"] == []


def test_tool_accepts_more_than_one_bridge_batch(tool_calls):
    tool_calls["selection"] = [_row(f"m{i}") for i in range(450)]

    result = main.delete_messages_for_me(CHAT, after="2026-10-01", before="2026-11-01", confirm=True)

    assert result["success"] is True
    assert result["deleted"] == 450
    assert len(tool_calls["deleted"][0][1]) == 450, "batching is the client's job, not the tool's"


def test_tool_all_or_nothing_when_some_ids_are_unknown(tool_calls):
    result = main.delete_messages_for_me(CHAT, message_ids=["a2", "ghost"], confirm=True)

    assert result["success"] is False
    assert result["error_code"] == "unknown_ids"
    assert result["missing_ids"] == ["ghost"]
    assert tool_calls["deleted"] == []


def test_tool_turns_selector_errors_into_failure_results(monkeypatch):
    def bad_selection(*args, **kwargs):
        raise ValueError("Provide message_ids, or both after and before")

    monkeypatch.setattr(main, "whatsapp_select_messages_for_deletion", bad_selection)

    result = main.delete_messages_for_me(CHAT, after="2026-10-13", confirm=True)

    assert result["success"] is False
    assert result["error_code"] == "invalid_selector"
    assert "both after and before" in result["message"]


def test_tool_is_advertised_as_destructive_with_the_dry_run_workflow():
    tool = next(t for t in asyncio.run(main.mcp.list_tools()) if t.name == "delete_messages_for_me")

    assert tool.annotations.destructiveHint is True
    assert tool.annotations.readOnlyHint is False
    assert set(tool.inputSchema["properties"]) == {"chat_jid", "after", "before", "message_ids", "confirm"}
    assert tool.inputSchema["required"] == ["chat_jid"]
    assert "confirm" in tool.description and "dry run" in tool.description.lower()
