from __future__ import annotations

import os
from pathlib import Path

import httpx


class SoulResponder:
    def __init__(self, prompt_path: str):
        self.prompt_path = Path(prompt_path)
        self.api_key = os.getenv("ANTHROPIC_API_KEY")
        self.model = os.getenv("SOUL_BOT_MODEL", "claude-sonnet-4-5")

    def load_prompt(self) -> str:
        if not self.prompt_path.exists():
            return ""
        return self.prompt_path.read_text(encoding="utf-8")

    async def maybe_reply(self, *, sender_role: str, sender: str, content: str, chat_history: str) -> str | None:
        if not self.api_key or sender_role != "participant" or not content.strip():
            return None

        soul = self.load_prompt()
        payload = {
            "model": self.model,
            "max_tokens": 450,
            "system": soul,
            "messages": [
                {
                    "role": "user",
                    "content": (
                        f"Sender: {sender}\n"
                        f"Recent group history:\n{chat_history}\n\n"
                        f"Participant message:\n{content}\n\n"
                        "Reply as the support-group admin assistant. Keep it short and WhatsApp-friendly."
                    ),
                }
            ],
        }
        async with httpx.AsyncClient(timeout=45) as client:
            response = await client.post(
                "https://api.anthropic.com/v1/messages",
                headers={
                    "x-api-key": self.api_key,
                    "anthropic-version": "2023-06-01",
                    "content-type": "application/json",
                },
                json=payload,
            )
            response.raise_for_status()
            data = response.json()
        chunks = data.get("content", [])
        text = "".join(chunk.get("text", "") for chunk in chunks if chunk.get("type") == "text")
        return text.strip() or None
