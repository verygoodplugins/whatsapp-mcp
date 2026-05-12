from __future__ import annotations

import contextlib
import logging

import httpx

logger = logging.getLogger("soul_bot")


class WhatsAppBridge:
    def __init__(self, api_url: str):
        self.api_url = api_url.rstrip("/")

    async def send_message(self, recipient: str, message: str) -> dict:
        async with httpx.AsyncClient(timeout=30) as client:
            response = await client.post(
                f"{self.api_url}/send",
                json={"recipient": recipient, "message": message},
            )
            try:
                data = response.json()
            except ValueError:
                data = {"success": False, "message": response.text}
            if response.status_code != 200:
                data.setdefault("success", False)
            return data

    async def set_typing(self, recipient: str, is_typing: bool) -> None:
        try:
            async with httpx.AsyncClient(timeout=10) as client:
                await client.post(
                    f"{self.api_url}/typing",
                    json={"recipient": recipient, "is_typing": is_typing},
                )
        except httpx.HTTPError as exc:
            logger.warning("typing indicator failed recipient=%s is_typing=%s err=%s", recipient, is_typing, exc)

    @contextlib.asynccontextmanager
    async def typing(self, recipient: str):
        await self.set_typing(recipient, True)
        try:
            yield
        finally:
            await self.set_typing(recipient, False)
