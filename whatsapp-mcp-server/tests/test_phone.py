import sqlite3

import pytest

import whatsapp
from phone import phone_matches

STORED_WITHOUT_NINE = "553185168687"


@pytest.mark.parametrize(
    "typed",
    [
        "553185168687",
        "5531985168687",
        "+55 (31) 98516-8687",
        "31 98516-8687",
        "3185168687",
        "985168687",
        "85168687",
    ],
)
def test_brazilian_number_matches_in_any_common_form(typed):
    assert phone_matches(typed, STORED_WITHOUT_NINE)


@pytest.mark.parametrize("typed", ["21 98516-8687", "5521985168687", "8516", "not a phone"])
def test_different_area_code_or_too_few_digits_do_not_match(typed):
    assert not phone_matches(typed, STORED_WITHOUT_NINE)


def test_foreign_number_matches_with_or_without_country_code():
    assert phone_matches("+1 202 555 1234", "12025551234")
    assert phone_matches("202 555 1234", "12025551234")


@pytest.fixture
def stores(tmp_path, monkeypatch):
    messages_db = tmp_path / "messages.db"
    whatsmeow_db = tmp_path / "whatsapp.db"
    with sqlite3.connect(messages_db) as conn:
        conn.executescript(
            """
            CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
            CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
                                   is_from_me BOOLEAN, media_type TEXT, filename TEXT);
            INSERT INTO chats VALUES ('553185168687@s.whatsapp.net', 'Vitor', '2026-09-01 10:00:00+00:00');
            INSERT INTO chats VALUES ('218038745993283@lid', 'Lid contact', '2026-09-02 10:00:00+00:00');
            INSERT INTO chats VALUES ('120363426636987287@g.us', 'Group 85168687', '2026-09-03 10:00:00+00:00');
            """
        )
    with sqlite3.connect(whatsmeow_db) as conn:
        conn.executescript(
            """
            CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT);
            CREATE TABLE whatsmeow_contacts (our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT,
                                             push_name TEXT, business_name TEXT);
            INSERT INTO whatsmeow_lid_map VALUES ('218038745993283', '553193393756');
            """
        )
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(messages_db))
    monkeypatch.setattr(whatsapp, "WHATSMEOW_DB_PATH", str(whatsmeow_db))


def test_direct_chat_found_by_number_typed_with_the_extra_nine(stores):
    chat = whatsapp.get_direct_chat_by_contact("31 98516-8687")
    assert chat["jid"] == "553185168687@s.whatsapp.net"


def test_direct_chat_found_for_lid_contact_by_real_phone(stores):
    chat = whatsapp.get_direct_chat_by_contact("31 99339-3756")
    assert chat["jid"] == "218038745993283@lid"


def test_groups_are_never_returned_as_direct_chats(stores):
    assert whatsapp.get_direct_chat_by_contact("120363426636987287") is None


def test_search_contacts_reports_resolved_phone_and_lid(stores):
    [contact] = [c for c in whatsapp.search_contacts("5531993393756") if c["jid"].endswith("@lid")]
    assert contact["phone_number"] == "553193393756"
    assert contact["lid"] == "218038745993283"
