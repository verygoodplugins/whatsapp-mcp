"""Tests for list_polls."""

import json
import sqlite3

import pytest

import whatsapp


def _make_polls_db(path):
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
        """
    )
    rows = [
        (
            "POLL_OLD",
            "111@s.whatsapp.net",
            "111",
            0,
            "Old?",
            json.dumps(["a", "b"]),
            1,
            0,
            "2024-01-01 09:00:00+00:00",
        ),
        (
            "POLL_MID",
            "111@s.whatsapp.net",
            "111",
            1,
            "Lunch?",
            json.dumps(["pizza", "salad", "ramen"]),
            1,
            0,
            "2024-02-01 12:00:00+00:00",
        ),
        (
            "POLL_NEW",
            "222@g.us",
            "999",
            0,
            "Outing?",
            json.dumps(["beach", "park"]),
            2,
            1,
            "2024-03-01 15:00:00+00:00",
        ),
    ]
    conn.executemany(
        "INSERT INTO polls VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        rows,
    )
    conn.commit()
    conn.close()


@pytest.fixture
def polls_db(tmp_path, monkeypatch):
    db_path = tmp_path / "messages.db"
    _make_polls_db(str(db_path))
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(db_path))
    return db_path


def test_list_polls_orders_newest_first(polls_db):
    polls = whatsapp.list_polls()
    assert [p["message_id"] for p in polls] == ["POLL_NEW", "POLL_MID", "POLL_OLD"]


def test_list_polls_filters_by_chat(polls_db):
    polls = whatsapp.list_polls(chat_jid="111@s.whatsapp.net")
    assert {p["message_id"] for p in polls} == {"POLL_OLD", "POLL_MID"}


def test_list_polls_returns_options_as_list(polls_db):
    polls = whatsapp.list_polls(chat_jid="222@g.us")
    assert polls[0]["options"] == ["beach", "park"]
    assert polls[0]["selectable_option_count"] == 2
    assert polls[0]["is_from_me"] is False


def test_list_polls_pagination(polls_db):
    page0 = whatsapp.list_polls(limit=2, page=0)
    page1 = whatsapp.list_polls(limit=2, page=1)
    assert len(page0) == 2
    assert len(page1) == 1
    assert page0[0]["message_id"] != page1[0]["message_id"]
