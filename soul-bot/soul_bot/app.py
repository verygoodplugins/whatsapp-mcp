from __future__ import annotations

import asyncio
import logging
import os
import re
from contextlib import asynccontextmanager
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo

logger = logging.getLogger("soul_bot")
if not logger.handlers:
    _handler = logging.StreamHandler()
    _handler.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(name)s: %(message)s"))
    logger.addHandler(_handler)
    logger.setLevel(logging.INFO)
    logger.propagate = False

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from .bridge import WhatsAppBridge
from .config import AppConfig, WeeklyWeighInConfig, load_config, normalize_jid
from .db import BotStore
from .llm import SoulResponder
from .media import save_media_base64
from .vision import analyze_weight_photo

WEIGHT_RE = re.compile(r"(?<!\d)([4-9]\d(?:[.,]\d)?|1[0-9]{2}(?:[.,]\d)?|2[0-4]\d(?:[.,]\d)?)(?:\s*(?:kg|קג|ק\"ג|קילו))?", re.I)
SENSITIVE_RE = re.compile(
    r"הקאה|להקיא|צום|לא לאכול|משלשל|כדורים|אוזמפיק|ווגובי|סוכרת|הריון|בהריון|דיכאון|אובד|פגיעה עצמית|"
    r"purge|vomit|fasting|laxative|ozempic|wegovy|diabetes|pregnan|suicid|self harm|eating disorder",
    re.I,
)
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
    sender: str = ""
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
    eventType: str | None = None
    participants: list[str] = Field(default_factory=list)


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
        task = asyncio.create_task(reminder_loop(config, store, bridge, responder))
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

        if payload.eventType in {"group_join", "group_leave"}:
            return await handle_group_event(payload, store, bridge, config.bot.auto_reply)

        sender = normalize_jid(payload.sender)
        role = classify_sender(config, sender, payload.isFromMe)
        store.upsert_member(payload.chatJID, sender, role)
        message_id = store.store_message(payload.model_dump(), role)
        media_path = save_payload_media(config, payload, message_id)
        if media_path:
            store.set_message_media_path(message_id, media_path)

        weight = extract_weight(payload.content)
        if weight is not None and role == "participant":
            store.store_weight(payload.chatJID, sender, weight, message_id, "auto-detected from text")

        action = "stored"
        reply = None
        decision_reason = ""
        if role == "operator":
            action, reply = await handle_operator_command(payload, store, config.weekly_weigh_in.timezone)
        elif config.bot.auto_reply and role == "participant":
            if payload.mediaType == "image" and config.bot.vision_enabled and media_path:
                async with bridge.typing(payload.chatJID):
                    vision = await analyze_weight_photo(config.bot, media_path, sender)
                    store.store_vision_review(
                        message_id=message_id,
                        provider=config.bot.vision_provider,
                        status=vision.status,
                        weight_kg=vision.weight_kg,
                        confidence=vision.confidence,
                        explanation=vision.explanation,
                        raw_response=vision.raw_response,
                    )
                    if vision.is_weight:
                        store.store_weight(payload.chatJID, sender, vision.weight_kg or 0, message_id, "auto-detected from image")
                    caption = (payload.content or "").strip()
                    if caption:
                        vision_context = (
                            f"status={vision.status}; "
                            f"weight_kg={vision.weight_kg}; "
                            f"confidence={vision.confidence}; "
                            f"explanation={vision.explanation}"
                        )
                        decision = await responder.decide_and_reply(
                            config=config.bot,
                            sender=sender,
                            sender_role=role,
                            content=caption,
                            chat_history=format_history(store, payload.chatJID, config.weekly_weigh_in.timezone),
                            detected_weight=vision.weight_kg if vision.is_weight else weight,
                            member_role=role,
                            vision_context=vision_context,
                            current_time=current_local_time(config.weekly_weigh_in.timezone),
                        )
                        action = f"vision+llm_{decision.action}"
                        decision_reason = f"vision={vision.status}; llm={decision.reason}"
                        if decision.action == "reply":
                            reply = decision.reply
                    else:
                        action = f"vision_{vision.status}"
                        decision_reason = vision.explanation
                        if vision.is_weight:
                            reply = vision.reply or f"נרשם, תודה ❤️ קלטתי {vision.weight_kg:g} ק״ג."
                        elif vision.status in {"not_readable", "not_scale_photo"}:
                            reply = vision.reply or "קיבלתי את התמונה, אבל לא הצלחתי לקרוא ממנה משקל ברור. אפשר לשלוח שוב תמונה חדה יותר או לכתוב את המשקל?"
            elif payload.mediaType == "image":
                action = "operator_review_photo"
            elif SENSITIVE_RE.search(payload.content or ""):
                action = "operator_review_sensitive"
                decision_reason = "sensitive_regex_match"
            else:
                async with bridge.typing(payload.chatJID):
                    decision = await responder.decide_and_reply(
                        config=config.bot,
                        sender=sender,
                        sender_role=role,
                        content=payload.content,
                        chat_history=format_history(store, payload.chatJID, config.weekly_weigh_in.timezone),
                        detected_weight=weight,
                        member_role=role,
                        current_time=current_local_time(config.weekly_weigh_in.timezone),
                    )
                action = f"llm_{decision.action}"
                decision_reason = decision.reason
                if decision.action == "reply":
                    reply = decision.reply

        if reply:
            await bridge.send_message(payload.chatJID, reply)
            store.store_message(
                {"chatJID": payload.chatJID, "sender": "soul-bot", "content": reply, "isFromMe": True},
                role="self",
            )
            action = f"{action}_sent"

        logger.info(
            "decision msg_id=%s chat=%s sender=%s role=%s action=%s reply_len=%s reason=%s",
            message_id,
            payload.chatJID,
            sender,
            role,
            action,
            len(reply) if reply else 0,
            decision_reason[:200],
        )

        return {
            "ok": True,
            "role": role,
            "action": action,
            "message_id": message_id,
            "weight_kg": weight,
            "media_path": media_path,
            "reply": reply,
        }

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


def save_payload_media(config: AppConfig, payload: WebhookPayload, message_id: int) -> str | None:
    if not payload.mediaBase64:
        return None
    return save_media_base64(
        payload.mediaBase64,
        media_dir=config.bot.media_dir,
        message_id=message_id,
        filename=payload.mediaFilename,
    )


WELCOME_TEMPLATE = (
    "ברוכים הבאים לקבוצה ❤️ אני יפית התזונאית — כאן לעודד, להקשיב, ולעזור לכם בדרך.\n"
    "כשתרצו, ספרו לנו קצת על עצמכם ועל המטרה שלכם."
)


async def handle_group_event(payload: WebhookPayload, store: BotStore, bridge: WhatsAppBridge, auto_reply: bool) -> dict:
    new_participants = [normalize_jid(p) for p in payload.participants if p]
    for jid in new_participants:
        if payload.eventType == "group_join":
            store.upsert_member(payload.chatJID, jid, "participant")
        elif payload.eventType == "group_leave":
            store.upsert_member(payload.chatJID, jid, "left")

    reply = None
    action = f"event_{payload.eventType}"
    if payload.eventType == "group_join" and auto_reply:
        reply = WELCOME_TEMPLATE
        async with bridge.typing(payload.chatJID):
            await bridge.send_message(payload.chatJID, reply)
        store.store_message(
            {"chatJID": payload.chatJID, "sender": "soul-bot", "content": reply, "isFromMe": True},
            role="self",
        )
        action = f"{action}_welcomed"

    logger.info(
        "group_event chat=%s event=%s participants=%s action=%s",
        payload.chatJID,
        payload.eventType,
        ",".join(new_participants),
        action,
    )
    return {"ok": True, "event": payload.eventType, "participants": new_participants, "action": action, "reply": reply}


async def handle_operator_command(payload: WebhookPayload, store: BotStore, tz_name: str) -> tuple[str, str | None]:
    command = (payload.content or "").strip()
    if not command.startswith("/"):
        return "operator_message_stored", None

    if command.startswith("/members"):
        members = store.list_members(payload.chatJID)
        text = "Members:\n" + "\n".join(f"- {member['member_jid']} ({member['role']})" for member in members)
        return "members", text

    if command.startswith("/summary"):
        history = format_history(store, payload.chatJID, tz_name)
        text = f"Recent activity:\n{history}" if history else "No recent activity yet."
        return "summary", text

    if command.startswith("/remind"):
        message = "בוקר טוב ❤️ תזכורת שקילה שבועית: כשתוכלו, שלחו תמונה חיה עם המשקל שלכם או כתבו את המשקל בפרטי."
        store.store_reminder(payload.chatJID, "manual_weigh_in", message)
        return "reminder", message

    if command.startswith("/weights"):
        parts = command.split(maxsplit=1)
        member_filter = normalize_jid(parts[1].strip()) if len(parts) > 1 else None
        entries = store.list_weights(payload.chatJID, member_filter, limit=50)
        if not entries:
            text = f"No weight entries{' for ' + member_filter if member_filter else ''} yet."
        else:
            lines = [
                f"- {to_local_time(entry['created_at'], tz_name)} | {entry['member_jid']}: {entry['weight_kg']:g} kg"
                + (f" ({entry['note']})" if entry['note'] else "")
                for entry in entries
            ]
            header = f"Weight history{' for ' + member_filter if member_filter else ''} (last {len(entries)}):"
            text = header + "\n" + "\n".join(lines)
        return "weights", text

    return "unknown_operator_command", None


def to_local_time(utc_str: str | None, tz_name: str) -> str:
    if not utc_str:
        return ""
    try:
        dt = datetime.fromisoformat(utc_str.replace(" ", "T")).replace(tzinfo=ZoneInfo("UTC"))
    except ValueError:
        return utc_str
    return dt.astimezone(ZoneInfo(tz_name)).strftime("%Y-%m-%d %H:%M:%S")


def current_local_time(tz_name: str) -> str:
    return datetime.now(ZoneInfo(tz_name)).strftime("%Y-%m-%d %H:%M:%S (%A)")


def format_history(store: BotStore, chat_jid: str, tz_name: str, limit: int = 12) -> str:
    messages = list(reversed(store.recent_messages(chat_jid, limit=limit)))
    return "\n".join(
        f"[{to_local_time(message.created_at, tz_name)}] {message.role} {message.sender_jid}: {message.content or '[' + str(message.media_type) + ']'}"
        for message in messages
    )


async def reminder_loop(
    config: AppConfig, store: BotStore, bridge: WhatsAppBridge, responder: SoulResponder
) -> None:
    while True:
        try:
            await maybe_send_weekly_reminder(config, store, bridge)
            await maybe_send_weigh_in_nudges(config, store, bridge, responder)
        except Exception as exc:
            logger.warning("reminder_loop error: %s", exc)
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
            store.store_message(
                {"chatJID": group.jid, "sender": "soul-bot", "content": weekly.message, "isFromMe": True},
                role="self",
            )
            logger.info("weekly_weigh_in sent chat=%s day=%s time=%s", group.jid, weekly.day, weekly.time)
        else:
            logger.warning("weekly_weigh_in send failed chat=%s result=%s", group.jid, result)


def _cycle_anchor_utc(weekly: WeeklyWeighInConfig, now_local: datetime) -> datetime | None:
    """Return the most recent weigh-in datetime (in UTC) on/before now, or None if there hasn't been one this week."""
    target_weekday = WEEKDAYS.get(weekly.day.lower())
    if target_weekday is None:
        return None
    hour, minute = [int(part) for part in weekly.time.split(":", 1)]
    days_back = (now_local.weekday() - target_weekday) % 7
    candidate = (now_local - timedelta(days=days_back)).replace(hour=hour, minute=minute, second=0, microsecond=0)
    if candidate > now_local:
        candidate -= timedelta(days=7)
    return candidate.astimezone(ZoneInfo("UTC"))


def _format_member_history(messages, tz_name: str) -> str:
    messages = list(reversed(messages))
    return "\n".join(
        f"[{to_local_time(m.created_at, tz_name)}] {m.role}: {m.content or '[' + str(m.media_type) + ']'}"
        for m in messages
    )


async def _send_one_nudge(
    config: AppConfig,
    store: BotStore,
    bridge: WhatsAppBridge,
    responder: SoulResponder,
    group_jid: str,
    member_jid: str,
    nudge,
    nudges_in_config: tuple,
    anchor_local: datetime,
    anchor_utc_iso: str,
) -> None:
    tz_name = config.weekly_weigh_in.timezone
    history_messages = store.recent_member_messages(group_jid, member_jid, limit=15)
    member_history = _format_member_history(history_messages, tz_name)
    attempt_number = 1
    for idx, n in enumerate(nudges_in_config, start=1):
        if n.kind == nudge.kind:
            attempt_number = idx
            break

    decision = await responder.compose_nudge(
        config=config.bot,
        member_jid=member_jid,
        member_history=member_history,
        nudge_kind=nudge.kind,
        nudge_intent=nudge.intent,
        cycle_anchor_local=anchor_local.strftime("%Y-%m-%d %H:%M %A"),
        attempt_number=attempt_number,
        current_time=current_local_time(tz_name),
    )
    text = decision.reply if decision.action == "reply" else None
    if not text and nudge.message:
        # Fall back to the configured static message if the LLM ignored or failed.
        text = nudge.message
        logger.info(
            "weigh_in_nudge using fallback chat=%s member=%s kind=%s reason=%s",
            group_jid, member_jid, nudge.kind, decision.reason,
        )
    if not text:
        logger.info(
            "weigh_in_nudge skipped chat=%s member=%s kind=%s reason=%s",
            group_jid, member_jid, nudge.kind, decision.reason,
        )
        return

    result = await bridge.send_message(member_jid, text)
    if result.get("success"):
        store.store_reminder(group_jid, nudge.kind, text, member_jid=member_jid)
        logger.info(
            "weigh_in_nudge sent chat=%s member=%s kind=%s attempt=%s reason=%s",
            group_jid, member_jid, nudge.kind, attempt_number, decision.reason[:120],
        )
    else:
        logger.warning(
            "weigh_in_nudge send failed chat=%s member=%s kind=%s result=%s",
            group_jid, member_jid, nudge.kind, result,
        )


async def maybe_send_weigh_in_nudges(
    config: AppConfig, store: BotStore, bridge: WhatsAppBridge, responder: SoulResponder
) -> None:
    weekly = config.weekly_weigh_in
    if not weekly.enabled or not weekly.nudges:
        return

    tz = ZoneInfo(weekly.timezone)
    now_local = datetime.now(tz)
    anchor_utc = _cycle_anchor_utc(weekly, now_local)
    if anchor_utc is None:
        return
    anchor_local = anchor_utc.astimezone(tz)
    anchor_utc_iso = anchor_utc.strftime("%Y-%m-%d %H:%M:%S")

    for nudge in weekly.nudges:
        target_local = anchor_local + timedelta(hours=nudge.offset_hours)
        # Fire if "now" is in [target, target + 90s) — gives the per-minute loop a window without double-fire.
        if not (target_local <= now_local < target_local + timedelta(seconds=90)):
            continue
        tasks = []
        for group in config.watched_groups:
            for member in store.list_members(group.jid):
                if member.get("role") != "participant":
                    continue
                member_jid = member["member_jid"]
                if store.member_weighed_since(group.jid, member_jid, anchor_utc_iso):
                    continue
                if store.member_reminder_since(group.jid, member_jid, nudge.kind, anchor_utc_iso):
                    continue
                tasks.append(
                    _send_one_nudge(
                        config, store, bridge, responder,
                        group.jid, member_jid, nudge, weekly.nudges,
                        anchor_local, anchor_utc_iso,
                    )
                )
        if tasks:
            logger.info("weigh_in_nudge fan-out kind=%s targets=%s", nudge.kind, len(tasks))
            await asyncio.gather(*tasks, return_exceptions=True)


app = create_app()


if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("PORT", "8769"))
    uvicorn.run("soul_bot.app:app", host="0.0.0.0", port=port)
