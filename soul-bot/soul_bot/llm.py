from __future__ import annotations

import asyncio
import json
import shutil
from dataclasses import dataclass
from pathlib import Path

from .config import BotConfig, claude_code_model_args


@dataclass(frozen=True)
class Decision:
    action: str
    reply: str | None = None
    reason: str = ""
    raw_response: str = ""


class SoulResponder:
    def __init__(self, prompt_path: str):
        self.prompt_path = Path(prompt_path)

    def load_prompt(self) -> str:
        if not self.prompt_path.exists():
            return ""
        return self.prompt_path.read_text(encoding="utf-8")

    async def decide_and_reply(
        self,
        *,
        config: BotConfig,
        sender: str,
        sender_role: str,
        content: str,
        chat_history: str,
        detected_weight: float | None,
        member_role: str | None,
        vision_context: str | None = None,
        current_time: str | None = None,
    ) -> Decision:
        if not content.strip() and not vision_context:
            return Decision(action="ignore", reason="empty_content")
        if shutil.which("claude") is None:
            return Decision(action="ignore", reason="provider_unavailable")

        schema = {
            "type": "object",
            "properties": {
                "action": {"type": "string", "enum": ["reply", "ignore", "escalate"]},
                "reply": {"type": ["string", "null"]},
                "reason": {"type": "string"},
            },
            "required": ["action", "reply", "reason"],
            "additionalProperties": False,
        }

        soul = self.load_prompt()
        weight_line = (
            f"Detected weight in this message: {detected_weight:g} kg (already stored)."
            if detected_weight is not None
            else "No weight detected in this message."
        )
        vision_block = f"\nImage analysis result (vision already ran on the attached photo):\n{vision_context}\n" if vision_context else ""
        now_line = f"Current local time (group timezone): {current_time}." if current_time else "Current local time is not provided."
        prompt = f"""
You are the admin assistant for a WhatsApp support group. The full group persona, voice, and policy live in the <soul> block below — follow it strictly.

<soul>
{soul}
</soul>

Decide what to do with the new incoming participant message. You see:
- The sender's JID and known role.
- Recent group chat history (most recent last).
- Whether a weight number was auto-detected in this message.
- The message content itself.

Return JSON only, matching the schema. Choose one action:
- "reply": you want to send a reply to the group. Put the reply text in "reply" (Hebrew if the participant wrote Hebrew, otherwise match their language). Keep it short and WhatsApp-friendly.
- "ignore": stay silent. Use this for casual chit-chat, emoji reactions, or anything that does not need a response.
- "escalate": flag for operator review. Use for medical, eating-disorder, pregnancy, medication, diabetes, urgent mental-health, or anything that should not be answered by a bot. Put a short internal note in "reply" describing why; the bot will NOT send "reply" to the group when action is "escalate".

"reason" is a short internal note (English ok) describing why you chose the action — for logging.

---

{now_line}
Sender JID: {sender}
Sender role: {sender_role} (known member role: {member_role or "unknown"})
{weight_line}{vision_block}
Recent group history (timestamps are in the same local timezone as "current local time"):
{chat_history or "(no recent messages)"}

New participant message (may be empty if the participant only sent a photo):
{content or "(no text, photo only)"}

Return only JSON matching the schema.
""".strip()

        cmd = [
            "claude",
            "-p",
            *claude_code_model_args(config),
            "--output-format",
            "json",
            "--json-schema",
            json.dumps(schema),
            prompt,
        ]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                cwd=config.claude_code_cwd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=config.claude_code_timeout_seconds)
        except TimeoutError:
            return Decision(action="ignore", reason="timeout")

        raw = stdout.decode("utf-8", errors="replace").strip()
        if proc.returncode != 0:
            err = stderr.decode("utf-8", errors="replace").strip() or raw
            return Decision(action="ignore", reason=f"provider_error: {err[:200]}", raw_response=raw)

        try:
            data = json.loads(raw)
            if isinstance(data.get("structured_output"), dict):
                data = data["structured_output"]
            elif isinstance(data.get("result"), str) and data["result"].strip():
                data = json.loads(data["result"])
        except json.JSONDecodeError:
            return Decision(action="ignore", reason="parse_error", raw_response=raw)

        action = str(data.get("action", "ignore"))
        reply = data.get("reply")
        reason = str(data.get("reason", ""))
        if action not in {"reply", "ignore", "escalate"}:
            return Decision(action="ignore", reason=f"invalid_action:{action}", raw_response=raw)
        if action == "reply" and not (isinstance(reply, str) and reply.strip()):
            return Decision(action="ignore", reason="reply_empty", raw_response=raw)
        return Decision(action=action, reply=reply if isinstance(reply, str) else None, reason=reason, raw_response=raw)

    async def compose_nudge(
        self,
        *,
        config: BotConfig,
        member_jid: str,
        member_history: str,
        nudge_kind: str,
        nudge_intent: str,
        cycle_anchor_local: str,
        attempt_number: int,
        current_time: str,
    ) -> Decision:
        """LLM-generated private nudge for a participant who hasn't logged a weight this cycle.

        Returns Decision with action="reply" (send DM) or action="ignore" (skip this participant).
        """
        if shutil.which("claude") is None:
            return Decision(action="ignore", reason="provider_unavailable")

        schema = {
            "type": "object",
            "properties": {
                "action": {"type": "string", "enum": ["reply", "ignore"]},
                "reply": {"type": ["string", "null"]},
                "reason": {"type": "string"},
            },
            "required": ["action", "reply", "reason"],
            "additionalProperties": False,
        }

        soul = self.load_prompt()
        prompt = f"""
You are composing a PRIVATE 1:1 WhatsApp nudge to a participant in a weight-loss support group, on behalf of the group admin persona below. This is NOT a group message — it is a direct message to a single person.

<soul>
{soul}
</soul>

Context:
- Current local time (group timezone): {current_time}.
- This week's weigh-in started at: {cycle_anchor_local}.
- This is nudge attempt #{attempt_number} (kind="{nudge_kind}") for this participant for this weigh-in cycle.
- Intent for this nudge: {nudge_intent or "(no specific intent — craft a gentle private check-in)"}.
- The participant has NOT logged a weight this cycle yet. They may have other reasons (busy, traveling, hesitant, struggling). Stay warm, never pressuring.

Recent activity of this specific participant in the group (most recent last; empty if no recent activity):
{member_history or "(no recent activity)"}

Decide:
- "reply": send a private nudge. Put the DM text in "reply" — Hebrew, short, WhatsApp-friendly, persona-consistent (Yafit, feminine voice). Match the tone implied by the intent and the attempt number (gentler each time). Do not shame, do not pressure. It's fine to offer alternatives ("just a quick word about how the week's going" instead of a number). Use at most one emoji.
- "ignore": skip this nudge for this participant if you judge that a nudge would be inappropriate right now (e.g., their recent messages suggest distress, a sensitive event, or they explicitly said they're skipping this week).

"reason" is a short English internal note explaining your choice.

Return only JSON matching the schema.
""".strip()

        cmd = [
            "claude",
            "-p",
            *claude_code_model_args(config),
            "--output-format",
            "json",
            "--json-schema",
            json.dumps(schema),
            prompt,
        ]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                cwd=config.claude_code_cwd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=config.claude_code_timeout_seconds)
        except TimeoutError:
            return Decision(action="ignore", reason="timeout")

        raw = stdout.decode("utf-8", errors="replace").strip()
        if proc.returncode != 0:
            err = stderr.decode("utf-8", errors="replace").strip() or raw
            return Decision(action="ignore", reason=f"provider_error: {err[:200]}", raw_response=raw)

        try:
            data = json.loads(raw)
            if isinstance(data.get("structured_output"), dict):
                data = data["structured_output"]
            elif isinstance(data.get("result"), str) and data["result"].strip():
                data = json.loads(data["result"])
        except json.JSONDecodeError:
            return Decision(action="ignore", reason="parse_error", raw_response=raw)

        action = str(data.get("action", "ignore"))
        reply = data.get("reply")
        reason = str(data.get("reason", ""))
        if action not in {"reply", "ignore"}:
            return Decision(action="ignore", reason=f"invalid_action:{action}", raw_response=raw)
        if action == "reply" and not (isinstance(reply, str) and reply.strip()):
            return Decision(action="ignore", reason="reply_empty", raw_response=raw)
        return Decision(action=action, reply=reply if isinstance(reply, str) else None, reason=reason, raw_response=raw)
