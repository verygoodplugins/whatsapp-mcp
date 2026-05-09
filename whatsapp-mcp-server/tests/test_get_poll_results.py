"""Tests for get_poll_results aggregation."""

import json
import sqlite3

import pytest

import whatsapp


def _make_db_with_poll(path):
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE polls (
            message_id        TEXT,
            chat_jid          TEXT,
            sender            TEXT,
            is_from_me        BOOLEAN,
            name              TEXT,
            options_json      TEXT,
            selectable_count  INTEGER,
            is_group          BOOLEAN,
            timestamp         TIMESTAMP,
            PRIMARY KEY (message_id, chat_jid)
        );
        CREATE TABLE poll_votes (
            poll_message_id        TEXT,
            poll_chat_jid          TEXT,
            voter                  TEXT,
            selected_options_json  TEXT,
            timestamp              TIMESTAMP,
            PRIMARY KEY (poll_message_id, poll_chat_jid, voter)
        );
        """
    )
    conn.execute(
        "INSERT INTO polls VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (
            "POLL1",
            "999@g.us",
            "111",
            1,
            "Lunch?",
            json.dumps(["pizza", "salad", "ramen"]),
            2,  # multi-select up to 2
            1,
            "2024-02-01 12:00:00+00:00",
        ),
    )
    # Single-select voter
    conn.execute(
        "INSERT INTO poll_votes VALUES (?, ?, ?, ?, ?)",
        ("POLL1", "999@g.us", "alice", json.dumps(["pizza"]), "2024-02-01 12:01:00+00:00"),
    )
    # Multi-select voter (counts toward two options but is a single voter)
    conn.execute(
        "INSERT INTO poll_votes VALUES (?, ?, ?, ?, ?)",
        ("POLL1", "999@g.us", "bob", json.dumps(["pizza", "ramen"]), "2024-02-01 12:02:00+00:00"),
    )
    # Voter who cleared their vote — should not contribute to total_voters.
    conn.execute(
        "INSERT INTO poll_votes VALUES (?, ?, ?, ?, ?)",
        ("POLL1", "999@g.us", "carol", json.dumps([]), "2024-02-01 12:03:00+00:00"),
    )
    conn.commit()
    conn.close()


@pytest.fixture
def db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    _make_db_with_poll(str(db_path))
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


def test_get_poll_results_aggregates_votes(db):
    result = whatsapp.get_poll_results("POLL1", "999@g.us")
    assert result is not None
    assert result["poll"]["name"] == "Lunch?"
    assert result["total_voters"] == 2  # alice + bob; carol cleared

    by_option = {opt["name"]: opt for opt in result["options"]}
    assert by_option["pizza"]["vote_count"] == 2
    assert sorted(by_option["pizza"]["voters"]) == ["alice", "bob"]
    assert by_option["ramen"]["vote_count"] == 1
    assert by_option["ramen"]["voters"] == ["bob"]
    # Salad got no votes but must still appear in the ballot.
    assert by_option["salad"]["vote_count"] == 0
    assert by_option["salad"]["voters"] == []


def test_get_poll_results_preserves_option_order(db):
    result = whatsapp.get_poll_results("POLL1", "999@g.us")
    assert [opt["name"] for opt in result["options"]] == ["pizza", "salad", "ramen"]


def test_get_poll_results_missing_poll_returns_none(db):
    assert whatsapp.get_poll_results("DOES_NOT_EXIST", "999@g.us") is None
