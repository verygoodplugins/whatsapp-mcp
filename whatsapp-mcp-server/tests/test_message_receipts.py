import sqlite3

import whatsapp

CHAT = "123456789@g.us"


def make_store(tmp_path, with_receipts: bool) -> str:
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    if with_receipts:
        conn.execute(
            "CREATE TABLE receipts (message_id TEXT, chat_jid TEXT, reader TEXT, type TEXT, timestamp TIMESTAMP)"
        )
        conn.executemany(
            "INSERT INTO receipts VALUES (?, ?, ?, ?, ?)",
            [
                ("m1", CHAT, "12025550100", "delivered", "2026-10-20 21:00:00+00:00"),
                ("m1", CHAT, "12025550100", "read", "2026-10-20 21:05:00+00:00"),
                ("m1", CHAT, "12025550101", "delivered", "2026-10-20 21:00:02+00:00"),
                ("m2", CHAT, "12025550100", "read", "2026-10-20 22:00:00+00:00"),
            ],
        )
    conn.commit()
    conn.close()
    return str(path)


def test_each_reader_has_their_furthest_receipt(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", make_store(tmp_path, with_receipts=True))
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: {"12025550100": "Ann"}.get(jid, jid))
    got = whatsapp.get_message_receipts("m1", CHAT)
    assert got == [
        {"reader_phone": "12025550100", "reader_name": "Ann", "status": "read", "at": "2026-10-20 21:05:00+00:00"},
        {
            "reader_phone": "12025550101",
            "reader_name": "12025550101",
            "status": "delivered",
            "at": "2026-10-20 21:00:02+00:00",
        },
    ]


def test_store_without_receipts_table(tmp_path, monkeypatch):
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", make_store(tmp_path, with_receipts=False))
    assert whatsapp.get_message_receipts("m1", CHAT) == []
