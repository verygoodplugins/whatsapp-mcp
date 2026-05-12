from __future__ import annotations

import asyncio
import os
import re
from contextlib import asynccontextmanager
from datetime import datetime
from zoneinfo import ZoneInfo

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from .bridge import WhatsAppBridge
from .config import AppConfig, load_config, normalize_jid
from .db import BotStore
from .llm import SoulResponder

WEIGHT_RE = re.compile(r"(?<!\d)([4-9]\d(?:[.,]\d)?|1[0-9]{2}(?:[.,]\d)?|2[0-4]\d(?:[.,]\d)?)(?:\s*(?:kg|קג|ק\"ג|קילו))?", re.I)
WEEKDAYS = {
    "monday": 0,
    "tuesday": 1,
    "wednesday": 2,
    "thursday": 3,
    "friday": 4,
    "saturday": 5,
    "sunday": 6,
}


class WebhookPayload(BaseModel):
    sender: str
    content: str = ""
    chatJID: str
    isFromMe: bool = False
    quotedMessageId: str | None = None
    quotedSender: str | None = None
    quotedContent: str | None = None
    messageId: str | None = None
    mediaType: str | None = None
    mimeType: str | None = None
    mediaFilename: str | None = None
    mediaBase64: str | None = None


class SendRequest(BaseModel):
    recipient: str
    message: str


class WeightRequest(BaseModel):
    chat_jid: str
    member_jid: str
    weight_kg: float = Field(gt=0)
    note: str = ""


def create_app(config: AppConfig | None = None, store: BotStore | None = None) -> FastAPI:
    config = config or load_config()
    store = store or BotStore(config.bot.database_path)
    bridge = WhatsAppBridge(config.bot.bridge_api_url)
    responder = SoulResponder(config.bot.soul_prompt_path)

    for group in config.watched_groups:
        store.upsert_group(group.jid, group.name)
        for operator in config.operator_ids:
            store.upsert_member(group.jid, operator, "operator")

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        task = asyncio.create_task(reminder_loop(config, store, bridge))
        try:
            yield
        finally:
            task.cancel()

    app = FastAPI(title="WhatsApp Soul Bot", lifespan=lifespan)

    @app.get("/health")
    def health() -> dict:
        return {
            "status": "ok",
            "watched_groups": list(config.watched_group_jids),
            "auto_reply": config.bot.auto_reply,
        }

    @app.post("/whatsapp/webhook")
    async def whatsapp_webhook(payload: WebhookPayload) -> dict:
        if payload.chatJID not in config.watched_group_jids:
            return {"ok": True, "ignored": True, "reason": "unwatched_group"}

        sender = normalize_jid(payload.sender)
        role = classify_sender(config, sender, payload.isFromMe)
        store.upsert_member(payload.chatJID, sender, role)
        message_id = store.store_message(payload.model_dump(), role)

        weight = extract_weight(payload.content)
        if weight is not None and role == "participant":
            store.store_weight(payload.chatJID, sender, weight, message_id, "auto-detected from text")

        action = "stored"
        reply = None
        if role == "operator":
            action, reply = await handle_operator_command(payload, store, bridge)
        elif config.bot.auto_reply and role == "participant":
            reply = await responder.maybe_reply(
                sender_role=role,
                sender=sender,
                content=payload.content,
                chat_history=format_history(store, payload.chatJID),
            )
            if reply:
                await bridge.send_message(payload.chatJID, reply)
                action = "auto_replied"

        return {"ok": True, "role": role, "action": action, "message_id": message_id, "weight_kg": weight, "reply": reply}

    @app.get("/admin/groups/{chat_jid}/members")
    def list_members(chat_jid: str) -> dict:
        return {"members": store.list_members(chat_jid)}

    @app.get("/admin/groups/{chat_jid}/summary")
    def group_summary(chat_jid: str, limit: int = 20) -> dict:
        messages = store.recent_messages(chat_jid, limit=limit)
        return {
            "chat_jid": chat_jid,
            "recent_messages": [message.__dict__ for message in messages],
        }

    @app.post("/admin/send")
    async def admin_send(req: SendRequest) -> dict:
        return await bridge.send_message(req.recipient, req.message)

    @app.post("/admin/weights")
    def admin_weight(req: WeightRequest) -> dict:
        weight_id = store.store_weight(normalize_jid(req.chat_jid), normalize_jid(req.member_jid), req.weight_kg, None, req.note)
        return {"ok": True, "weight_id": weight_id}

    return app


def classify_sender(config: AppConfig, sender: str, is_from_me: bool) -> str:
    if is_from_me:
        return "self"
    if normalize_jid(sender) in config.operator_ids:
        return "operator"
    return "participant"


def extract_weight(content: str) -> float | None:
    match = WEIGHT_RE.search(content or "")
    if not match:
        return None
    return float(match.group(1).replace(",", "."))


async def handle_operator_command(payload: WebhookPayload, store: BotStore, bridge: WhatsAppBridge) -> tuple[str, str | None]:
    command = (payload.content or "").strip()
    if not command.startswith("/"):
        return "operator_message_stored", None

    if command.startswith("/members"):
        members = store.list_members(payload.chatJID)
        text = "Members:\n" + "\n".join(f"- {member['member_jid']} ({member['role']})" for member in members)
        await bridge.send_message(payload.chatJID, text)
        return "members_sent", text

    if command.startswith("/summary"):
        history = format_history(store, payload.chatJID)
        text = f"Recent activity:\n{history}" if history else "No recent activity yet."
        await bridge.send_message(payload.chatJID, text)
        return "summary_sent", text

    if command.startswith("/remind"):
        message = "בוקר טוב ❤️ תזכורת שקילה שבועית: כשתוכלו, שלחו תמונה חיה עם המשקל שלכם או כתבו את המשקל בפרטי."
        await bridge.send_message(payload.chatJID, message)
        store.store_reminder(payload.chatJID, "manual_weigh_in", message)
        return "reminder_sent", message

    return "unknown_operator_command", None


def format_history(store: BotStore, chat_jid: str, limit: int = 12) -> str:
    messages = list(reversed(store.recent_messages(chat_jid, limit=limit)))
    return "\n".join(
        f"[{message.created_at}] {message.role} {message.sender_jid}: {message.content or '[' + str(message.media_type) + ']'}"
        for message in messages
    )


async def reminder_loop(config: AppConfig, store: BotStore, bridge: WhatsAppBridge) -> None:
    while True:
        try:
            await maybe_send_weekly_reminder(config, store, bridge)
        except Exception:
            pass
        await asyncio.sleep(60)


async def maybe_send_weekly_reminder(config: AppConfig, store: BotStore, bridge: WhatsAppBridge) -> None:
    weekly = config.weekly_weigh_in
    if not weekly.enabled:
        return

    tz = ZoneInfo(weekly.timezone)
    now = datetime.now(tz)
    target_weekday = WEEKDAYS.get(weekly.day.lower())
    if target_weekday is None or now.weekday() != target_weekday:
        return

    hour, minute = [int(part) for part in weekly.time.split(":", 1)]
    if now.hour != hour or now.minute != minute:
        return

    today = now.date().isoformat()
    for group in config.watched_groups:
        if store.latest_reminder_date(group.jid, "weekly_weigh_in") == today:
            continue
        result = await bridge.send_message(group.jid, weekly.message)
        if result.get("success"):
            store.store_reminder(group.jid, "weekly_weigh_in", weekly.message)


app = create_app()


if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("PORT", "8769"))
    uvicorn.run("soul_bot.app:app", host="0.0.0.0", port=port)
