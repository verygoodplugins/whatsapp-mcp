from datetime import datetime

import main
from whatsapp import Message, MessageContext


def test_get_message_context_serializes_dataclass(monkeypatch):
    target = Message(
        id="target",
        timestamp=datetime(2024, 1, 15, 10, 30, 0),
        sender="12025550100@s.whatsapp.net",
        content="target message",
        is_from_me=False,
        chat_jid="12025550100@s.whatsapp.net",
        chat_name="Test Chat",
    )
    before = Message(
        id="before",
        timestamp=datetime(2024, 1, 15, 10, 29, 0),
        sender="12025550100@s.whatsapp.net",
        content="before message",
        is_from_me=False,
        chat_jid="12025550100@s.whatsapp.net",
        chat_name="Test Chat",
    )
    after = Message(
        id="after",
        timestamp=datetime(2024, 1, 15, 10, 31, 0),
        sender="me@s.whatsapp.net",
        content="after message",
        is_from_me=True,
        chat_jid="12025550100@s.whatsapp.net",
        chat_name="Test Chat",
    )

    monkeypatch.setattr(
        main,
        "whatsapp_get_message_context",
        lambda message_id, before_count, after_count: MessageContext(
            message=target,
            before=[before],
            after=[after],
        ),
    )

    result = main.get_message_context("target", before=1, after=1)

    assert result["message"]["id"] == "target"
    assert result["before"][0]["id"] == "before"
    assert result["after"][0]["id"] == "after"
