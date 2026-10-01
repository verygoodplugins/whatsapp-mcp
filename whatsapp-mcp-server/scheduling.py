"""Scheduled messages: queued and sent by the bridge, read here from messages.db.

The bridge keeps one queue per chat and only ever sends a queue's head, so
messages in a chat go out in (send_at, scheduling) order. A failed head blocks
the rest of its queue until it is cancelled.
"""

import sqlite3
from datetime import datetime
from typing import Any

import requests

import whatsapp
from phone import digits_only, looks_like_phone

ACTIVE_STATUSES = ("pending", "sending", "failed")


def schedule_message(recipient: str, message: str, send_at: str) -> dict[str, Any]:
    try:
        send_at_rfc3339 = to_rfc3339(send_at)
    except ValueError:
        return {"success": False, "message": f"Invalid send_at {send_at!r}; use ISO 8601, e.g. 2026-10-01T18:30"}
    return _post_to_bridge(
        "/schedule",
        {"recipient": resolve_chat_jid(recipient), "message": message, "send_at": send_at_rfc3339},
    )


def cancel_scheduled_message(schedule_id: int) -> dict[str, Any]:
    return _post_to_bridge("/schedule/cancel", {"id": schedule_id})


def list_scheduled_messages(chat: str | None = None, include_finished: bool = False) -> list[dict[str, Any]]:
    statuses = ACTIVE_STATUSES if not include_finished else (*ACTIVE_STATUSES, "sent", "cancelled")
    params: list[Any] = list(statuses)
    chat_filter = ""
    if chat:
        chat_filter = "AND chat_jid = ?"
        params.append(resolve_chat_jid(chat))

    try:
        conn = sqlite3.connect(whatsapp.MESSAGES_DB_PATH)
        try:
            rows = conn.execute(
                f"""
                SELECT id, chat_jid, recipient, message, send_at, status, attempts,
                       next_attempt_at, last_error, sent_at
                  FROM scheduled_messages
                 WHERE status IN ({",".join("?" * len(statuses))}) {chat_filter}
                 ORDER BY chat_jid, send_at, id
                """,
                params,
            ).fetchall()
        finally:
            conn.close()
    except sqlite3.OperationalError as e:
        if "no such table" in str(e):
            return []
        raise

    return _with_queue_positions([_row_to_dict(row) for row in rows])


def resolve_chat_jid(recipient: str) -> str:
    """Normalise a recipient to the chat JID the bridge keys its queue by.

    Typed phone numbers resolve to the existing chat for that number, so a
    number written differently from how WhatsApp stores it still lands in the
    same queue. "@lid" chats are keyed by their phone JID, as in the bridge.
    """
    if "@" not in recipient and looks_like_phone(recipient):
        chat = whatsapp.get_direct_chat_by_contact(recipient)
        recipient = chat["jid"] if chat else f"{digits_only(recipient)}@s.whatsapp.net"
    if recipient.endswith("@lid"):
        phone = whatsapp._resolve_lid_to_phone(recipient)
        return f"{phone}@s.whatsapp.net" if phone else recipient
    return recipient


def to_rfc3339(value: str) -> str:
    """Parse ISO 8601; a time without a UTC offset is taken as local time."""
    when = datetime.fromisoformat(value.strip())
    if when.tzinfo is None:
        when = when.astimezone()
    return when.isoformat(timespec="seconds")


def _post_to_bridge(path: str, payload: dict[str, Any]) -> dict[str, Any]:
    try:
        response = requests.post(
            f"{whatsapp.WHATSAPP_API_BASE_URL}{path}", json=payload, headers=whatsapp._bridge_headers()
        )
    except requests.RequestException as e:
        return {"success": False, "message": f"Bridge unreachable: {e}"}
    try:
        return response.json()
    except ValueError:
        return {"success": False, "message": f"HTTP {response.status_code}: {response.text}"}


def _row_to_dict(row: tuple) -> dict[str, Any]:
    (id_, chat_jid, recipient, message, send_at, status, attempts, next_attempt_at, last_error, sent_at) = row
    return {
        "id": id_,
        "chat_jid": chat_jid,
        "recipient": recipient,
        "message": message,
        "send_at": _local_iso(send_at),
        "status": status,
        "attempts": attempts,
        "next_attempt_at": _local_iso(next_attempt_at),
        "last_error": last_error,
        "sent_at": _local_iso(sent_at),
    }


def _with_queue_positions(items: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Number each chat's active messages in send order and flag blocked ones.

    Expects items ordered by chat, then send order.
    """
    position = 0
    previous_chat = None
    blocked = False
    for item in items:
        if item["chat_jid"] != previous_chat:
            previous_chat, position, blocked = item["chat_jid"], 0, False
        if item["status"] not in ACTIVE_STATUSES:
            continue
        position += 1
        blocked = blocked or item["status"] == "failed"
        item["queue_position"] = position
        item["blocked"] = blocked
    return items


def _local_iso(unix_seconds: int | None) -> str | None:
    if unix_seconds is None:
        return None
    return datetime.fromtimestamp(unix_seconds).astimezone().isoformat(timespec="seconds")
