from __future__ import annotations

import base64
import binascii
import re
from pathlib import Path


def save_media_base64(media_base64: str, *, media_dir: str, message_id: int, filename: str | None = None) -> str | None:
    if not media_base64:
        return None
    try:
        data = base64.b64decode(media_base64, validate=True)
    except binascii.Error:
        return None

    path = Path(media_dir)
    path.mkdir(parents=True, exist_ok=True)
    out = path / f"{message_id}-{sanitize_filename(filename or 'image.jpg')}"
    out.write_bytes(data)
    return str(out.resolve())


def sanitize_filename(filename: str) -> str:
    filename = filename.split("/")[-1].split("\\")[-1]
    filename = re.sub(r"[^A-Za-z0-9._-]", "_", filename)
    return filename or "image.jpg"
