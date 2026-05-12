from __future__ import annotations

import httpx


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
