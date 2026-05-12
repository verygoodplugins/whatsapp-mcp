from __future__ import annotations

import asyncio
import json
import os
import shutil
from dataclasses import dataclass

from .config import BotConfig, claude_code_model_args


@dataclass(frozen=True)
class VisionResult:
    status: str
    weight_kg: float | None = None
    confidence: float | None = None
    explanation: str = ""
    reply: str | None = None
    raw_response: str = ""

    @property
    def is_weight(self) -> bool:
        return self.status == "weight_readable" and self.weight_kg is not None


async def analyze_weight_photo(config: BotConfig, image_path: str, sender: str) -> VisionResult:
    if not config.vision_enabled or config.vision_provider == "none":
        return VisionResult(status="vision_disabled", explanation="Vision is disabled")
    if config.vision_provider == "claude_code":
        return await analyze_with_claude_code(config, image_path, sender)
    return VisionResult(status="unsupported_provider", explanation=f"Unsupported vision provider: {config.vision_provider}")


async def analyze_with_claude_code(config: BotConfig, image_path: str, sender: str) -> VisionResult:
    if shutil.which("claude") is None:
        return VisionResult(status="provider_unavailable", explanation="claude CLI was not found")

    schema = {
        "type": "object",
        "properties": {
            "status": {"type": "string", "enum": ["weight_readable", "not_readable", "not_scale_photo", "operator_review"]},
            "weight_kg": {"type": ["number", "null"]},
            "confidence": {"type": ["number", "null"], "minimum": 0, "maximum": 1},
            "explanation": {"type": "string"},
            "reply": {"type": ["string", "null"]},
        },
        "required": ["status", "weight_kg", "confidence", "explanation", "reply"],
        "additionalProperties": False,
    }
    prompt = f"""
You are reviewing a WhatsApp image sent to a weight-loss support group by {sender}.

Image path: {image_path}

Task:
- Inspect the image.
- If it clearly shows a scale reading, extract the weight in kilograms.
- If it is not readable, say so.
- If it is not a scale/weight photo, say so.
- Do not guess. If uncertain, use not_readable or operator_review.
- Reply in Hebrew, short and supportive.

Return only JSON matching the schema.
""".strip()

    abs_image_path = os.path.abspath(image_path)
    image_dir = os.path.dirname(abs_image_path)
    cmd = [
        "claude",
        "-p",
        *claude_code_model_args(config),
        "--output-format",
        "json",
        "--allowedTools",
        "Read",
        "--add-dir",
        image_dir,
        "--json-schema",
        json.dumps(schema),
        prompt.replace(image_path, abs_image_path),
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
        return VisionResult(status="timeout", explanation="Claude Code vision analysis timed out")

    raw = stdout.decode("utf-8", errors="replace").strip()
    if proc.returncode != 0:
        explanation = stderr.decode("utf-8", errors="replace").strip() or raw
        return VisionResult(status="provider_error", explanation=explanation, raw_response=raw)

    try:
        data = json.loads(raw)
        if isinstance(data.get("structured_output"), dict):
            data = data["structured_output"]
        elif isinstance(data.get("result"), str) and data["result"].strip():
            data = json.loads(data["result"])
    except json.JSONDecodeError:
        return VisionResult(status="parse_error", explanation="Could not parse Claude Code vision response", raw_response=raw)

    return VisionResult(
        status=str(data.get("status", "operator_review")),
        weight_kg=data.get("weight_kg"),
        confidence=data.get("confidence"),
        explanation=str(data.get("explanation", "")),
        reply=data.get("reply"),
        raw_response=raw,
    )
