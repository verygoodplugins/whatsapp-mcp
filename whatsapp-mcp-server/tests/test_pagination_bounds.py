"""Pagination bounds must be clamped below as well as above.

SQLite treats a negative LIMIT as unbounded, so limit=-1 dumped the whole
message table (and, with include_context, fired one extra query per row);
a negative page produced a negative OFFSET.
"""

import pytest

import main as mcp_main


@pytest.mark.parametrize(
    ("tool", "captured_attr", "cap"),
    [
        ("list_messages", "whatsapp_list_messages", 500),
        ("list_chats", "whatsapp_list_chats", 200),
    ],
)
def test_negative_pagination_is_clamped(monkeypatch, tool, captured_attr, cap):
    seen = {}

    def fake(*args, **kwargs):
        seen.update(kwargs)
        return []

    monkeypatch.setattr(mcp_main, captured_attr, fake)
    getattr(mcp_main, tool)(limit=-1, page=-3)
    assert seen["limit"] == 1
    assert seen["page"] == 0
    seen.clear()
    getattr(mcp_main, tool)(limit=10_000, page=2)
    assert seen["limit"] == cap
    assert seen["page"] == 2


def test_get_contact_chats_clamps_pagination(monkeypatch):
    seen = {}
    monkeypatch.setattr(
        mcp_main, "whatsapp_get_contact_chats", lambda jid, limit, page: seen.update(limit=limit, page=page) or []
    )
    mcp_main.get_contact_chats("x@s.whatsapp.net", limit=-5, page=-1)
    assert seen == {"limit": 1, "page": 0}
