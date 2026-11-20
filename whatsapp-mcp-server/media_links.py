"""Signed, expiring download URLs for media in the bridge store.

download_media returns a path on the server's disk, which a client on another
machine cannot open. When WHATSAPP_MEDIA_BASE_URL is set, download_media also
returns a URL served by this server's own HTTP transport. The URL carries an
HMAC over the store-relative path and an expiry, so it grants access to that
one file only, and only until it expires or the server restarts.
"""

import hashlib
import hmac
import os
import secrets
import time
from pathlib import Path
from urllib.parse import quote, urlencode

ROUTE_PREFIX = "/media/"
URL_TTL_SECONDS = 3600

# Per process: a restart invalidates every URL handed out before it.
_SECRET = secrets.token_bytes(32)


def base_url() -> str | None:
    """Public URL under which clients reach this server, or None if unset."""
    return os.getenv("WHATSAPP_MEDIA_BASE_URL", "").strip().rstrip("/") or None


def _signature(rel_path: str, expires: int) -> str:
    return hmac.new(_SECRET, f"{rel_path}\n{expires}".encode(), hashlib.sha256).hexdigest()


def _relative(file_path: str, store_root: Path) -> str | None:
    try:
        return Path(file_path).resolve().relative_to(store_root.resolve()).as_posix()
    except ValueError:
        return None


def signed_url(file_path: str, store_root: Path, now: float | None = None) -> str | None:
    """Return a download URL for a file inside the store, or None.

    None when WHATSAPP_MEDIA_BASE_URL is unset or the file is outside the store.
    """
    base = base_url()
    rel = _relative(file_path, store_root)
    if base is None or rel is None:
        return None
    expires = int(now if now is not None else time.time()) + URL_TTL_SECONDS
    query = urlencode({"expires": expires, "sig": _signature(rel, expires)})
    return f"{base}{ROUTE_PREFIX}{quote(rel)}?{query}"


def resolve(rel_path: str, expires: str, sig: str, store_root: Path, now: float | None = None) -> Path | None:
    """Return the file a signed URL names, or None if it is invalid, expired or gone."""
    try:
        expires_at = int(expires)
    except ValueError:
        return None
    if expires_at < (now if now is not None else time.time()):
        return None
    if not hmac.compare_digest(sig, _signature(rel_path, expires_at)):
        return None
    root = store_root.resolve()
    target = (root / rel_path).resolve()
    if not target.is_relative_to(root) or not target.is_file():
        return None
    return target
