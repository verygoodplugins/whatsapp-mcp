from datetime import datetime

import pytest

import main
from whatsapp import Message, MessageContext


@pytest.mark.parametrize(
    ("tool_name", "kwargs"),
    [
        ("list_messages", {"limit": -1}),
        ("list_messages", {"page": -1}),
        ("list_messages", {"context_before": -1}),
        ("list_messages", {"context_after": -1}),
        ("list_chats", {"limit": -1}),
        ("list_chats", {"page": -1}),
        ("get_contact_chats", {"jid": "123@s.whatsapp.net", "limit": -1}),
        ("get_contact_chats", {"jid": "123@s.whatsapp.net", "page": -1}),
        ("get_message_context", {"message_id": "message-1", "before": -1}),
        ("get_message_context", {"message_id": "message-1", "after": -1}),
    ],
)
def test_numeric_tool_arguments_reject_negative_values(tool_name, kwargs):
    with pytest.raises(ValueError, match="must be non-negative"):
        getattr(main, tool_name)(**kwargs)


def test_list_tool_arguments_are_capped(monkeypatch):
    calls = {}

    def fake_list_messages(**kwargs):
        calls["messages"] = kwargs
        return []

    def fake_list_chats(**kwargs):
        calls["chats"] = kwargs
        return []

    def fake_contact_chats(jid, limit, page):
        calls["contact_chats"] = (jid, limit, page)
        return []

    monkeypatch.setattr(main, "whatsapp_list_messages", fake_list_messages)
    monkeypatch.setattr(main, "whatsapp_list_chats", fake_list_chats)
    monkeypatch.setattr(main, "whatsapp_get_contact_chats", fake_contact_chats)

    main.list_messages(limit=10_000, context_before=10_000, context_after=10_000)
    main.list_chats(limit=10_000)
    main.get_contact_chats("123@s.whatsapp.net", limit=10_000)

    assert calls["messages"]["limit"] == main._MAX_MESSAGES
    assert calls["messages"]["context_before"] == main._MAX_CONTEXT_MESSAGES
    assert calls["messages"]["context_after"] == main._MAX_CONTEXT_MESSAGES
    assert calls["chats"]["limit"] == main._MAX_CHATS
    assert calls["contact_chats"][1] == main._MAX_CHATS


def test_message_context_counts_are_capped(monkeypatch):
    target = Message(
        id="target",
        timestamp=datetime(2024, 1, 15, 10, 30),
        sender="me@s.whatsapp.net",
        content="target",
        is_from_me=True,
        chat_jid="123@s.whatsapp.net",
    )
    captured = {}

    def fake_context(message_id, before, after):
        captured["args"] = (message_id, before, after)
        return MessageContext(message=target, before=[], after=[])

    monkeypatch.setattr(main, "whatsapp_get_message_context", fake_context)

    main.get_message_context("target", before=10_000, after=10_000)

    assert captured["args"] == (
        "target",
        main._MAX_CONTEXT_MESSAGES,
        main._MAX_CONTEXT_MESSAGES,
    )
