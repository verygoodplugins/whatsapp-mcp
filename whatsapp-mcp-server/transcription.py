"""Transcribe voice notes with whisper.cpp or a configured HTTP service.

An audio message carries no text, so its ``content`` column in messages.db is
empty and a client that cannot read the filesystem has no way to learn what was
said. Transcribing into that empty column makes the words reachable through the
ordinary message tools, for any client, without a second server and without
transcribing the same note twice.

Whisper runs locally by default. The optional OpenAI-compatible provider sends
audio only to the endpoint explicitly configured by the operator.
"""

import os
import shutil
import sqlite3
import subprocess
import time
from pathlib import Path
from urllib.parse import urlsplit

import requests

# Marks text this module produced. Speech recognition gets words wrong, so a
# transcript must never be mistaken for something a human typed — and the
# marker is what lets a later run tell its own output from a real message.
TRANSCRIPT_MARKER = "[transcript"

DEFAULT_LANGUAGE = "auto"

# Suffixes the bridge uses for voice notes and other audio.
AUDIO_SUFFIXES = frozenset({".ogg", ".opus", ".m4a", ".mp3", ".wav", ".aac", ".amr"})
WHISPER_BINARY = "whisper-cli"

# Whisper wants 16 kHz mono PCM; phone voice notes are Opus.
TARGET_SAMPLE_RATE = "16000"
TRANSCRIPTION_TIMEOUT = 300


class TranscriptionError(RuntimeError):
    """Transcription could not run, with a reason worth showing the caller."""


def is_audio(path: str | Path) -> bool:
    """True when the file looks like a voice note rather than a picture."""
    return Path(path).suffix.lower() in AUDIO_SUFFIXES


def whisper_available() -> bool:
    return shutil.which(WHISPER_BINARY) is not None


def ffmpeg_available() -> bool:
    return shutil.which("ffmpeg") is not None


def model_path() -> str:
    """The whisper.cpp model to use, from WHISPER_MODEL.

    There is no conventional location for these files, so this is required
    rather than guessed.
    """
    configured = os.getenv("WHISPER_MODEL", "").strip()
    if not configured:
        raise TranscriptionError(
            "Set WHISPER_MODEL to a whisper.cpp model file (for example "
            "ggml-large-v3-turbo.bin) to enable transcription."
        )
    expanded = os.path.expanduser(configured)
    if not os.path.isfile(expanded):
        raise TranscriptionError(f"WHISPER_MODEL does not point at a file: {expanded}")
    return expanded


def language() -> str:
    return os.getenv("WHISPER_LANGUAGE", DEFAULT_LANGUAGE).strip() or DEFAULT_LANGUAGE


def provider_name() -> str:
    provider = os.getenv("WHATSAPP_TRANSCRIPTION_PROVIDER", "whisper_cpp").strip() or "whisper_cpp"
    if provider not in {"whisper_cpp", "openai_compatible"}:
        raise TranscriptionError("WHATSAPP_TRANSCRIPTION_PROVIDER must be whisper_cpp or openai_compatible")
    return provider


def configured_model(provider: str) -> str:
    if provider == "whisper_cpp":
        return model_path()
    model = os.getenv("WHATSAPP_TRANSCRIPTION_MODEL", "").strip()
    if not model:
        raise TranscriptionError("Set WHATSAPP_TRANSCRIPTION_MODEL to the model ID served by your endpoint")
    return model


def label(model: str, provider: str = "whisper_cpp") -> str:
    """The prefix stored in front of a transcript."""
    source = f"whisper {Path(model).stem}" if provider == "whisper_cpp" else f"{provider} {model}"
    return f"{TRANSCRIPT_MARKER} ({source})] "


def transcribe_http(source: Path, model: str) -> str:
    url = os.getenv("WHATSAPP_TRANSCRIPTION_URL", "").strip()
    try:
        parsed = urlsplit(url)
        valid = parsed.scheme in {"http", "https"} and parsed.hostname and not parsed.username and not parsed.password
    except ValueError:
        valid = False
    if not valid:
        raise TranscriptionError(
            "Set WHATSAPP_TRANSCRIPTION_URL to a full HTTP(S) transcription endpoint without credentials"
        )
    data = {"model": model, "response_format": "json"}
    lang = os.getenv("WHATSAPP_TRANSCRIPTION_LANGUAGE", "auto").strip()
    if lang and lang != "auto":
        data["language"] = lang
    key = os.getenv("WHATSAPP_TRANSCRIPTION_API_KEY", "").strip()
    headers = {"Authorization": f"Bearer {key}"} if key else {}
    try:
        with requests.Session() as session, source.open("rb") as audio:
            # Ambient proxies can send even loopback uploads off-machine, and
            # .netrc can override the explicitly configured bearer token.
            session.trust_env = False
            # No redirect or fallback: the operator chooses where private audio goes.
            response = session.post(
                url,
                files={"file": (source.name, audio)},
                data=data,
                headers=headers,
                timeout=(10, TRANSCRIPTION_TIMEOUT),
                allow_redirects=False,
            )
        with response:
            if not 200 <= response.status_code < 300:
                raise TranscriptionError(f"Transcription endpoint returned HTTP {response.status_code}")
            payload = response.json()
    except (requests.RequestException, ValueError, OSError) as exc:
        # Exception URLs and response bodies can contain credentials or private data.
        raise TranscriptionError(f"Transcription request failed ({type(exc).__name__})") from None
    text = payload.get("text") if isinstance(payload, dict) else None
    if not isinstance(text, str) or not text.strip():
        raise TranscriptionError("Transcription endpoint returned no non-empty text field")
    return normalise(text)


def decode_command(source: Path, destination: Path) -> list[str]:
    return [
        "ffmpeg",
        "-nostdin",
        "-v",
        "error",
        "-y",
        "-i",
        str(source),
        "-ar",
        TARGET_SAMPLE_RATE,
        "-ac",
        "1",
        "-c:a",
        "pcm_s16le",
        str(destination),
    ]


def whisper_command(model: str, wav: Path, lang: str) -> list[str]:
    return [WHISPER_BINARY, "-m", model, "-l", lang, "-nt", "-f", str(wav)]


def normalise(text: str) -> str:
    """Collapse whisper's line-wrapped output into one line."""
    return " ".join(text.split())


def transcribe_file(
    path: str | Path, work_dir: str | Path, model: str | None = None, provider: str = "whisper_cpp"
) -> str:
    """Transcribe one audio file and return its text.

    Raises:
        TranscriptionError: when a tool or the model is missing, or when
            decoding or recognition fails.
    """
    source = Path(path)
    if not source.is_file():
        raise TranscriptionError(f"Audio file not found: {source}")
    if provider == "openai_compatible":
        return transcribe_http(source, model or configured_model(provider))
    if not ffmpeg_available():
        raise TranscriptionError("FFmpeg is required to decode voice notes for transcription")
    if not whisper_available():
        raise TranscriptionError(f"{WHISPER_BINARY} not found on PATH. Install whisper.cpp to enable transcription.")

    resolved_model = model or model_path()
    wav = Path(work_dir) / f"{source.stem}.wav"

    decoded = run_command(decode_command(source, wav), timeout=60)
    if decoded.returncode != 0 or not wav.exists():
        detail = decoded.stderr.decode(errors="replace").strip()[:200]
        raise TranscriptionError(f"Could not decode the audio: {detail}")

    recognised = run_command(whisper_command(resolved_model, wav, language()), timeout=TRANSCRIPTION_TIMEOUT)
    if recognised.returncode != 0:
        detail = recognised.stderr.decode(errors="replace").strip()[:200]
        raise TranscriptionError(f"Transcription failed: {detail}")

    text = normalise(recognised.stdout.decode(errors="replace"))
    if not text:
        raise TranscriptionError("Transcription produced no text")
    return text


def run_command(command: list[str], timeout: int) -> subprocess.CompletedProcess:
    try:
        return subprocess.run(command, stdin=subprocess.DEVNULL, capture_output=True, check=False, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise TranscriptionError(f"{command[0]} failed ({type(exc).__name__})") from None


def stored_transcript(db_path: str, message_id: str, chat_jid: str) -> str | None:
    """The transcript already on the message row, if any."""
    try:
        connection = sqlite3.connect(db_path, timeout=10)
    except sqlite3.Error:
        return None
    try:
        row = connection.execute(
            "SELECT content FROM messages WHERE id = ? AND chat_jid = ? AND media_type = 'audio'",
            (message_id, chat_jid),
        ).fetchone()
    except sqlite3.Error:
        return None
    finally:
        connection.close()
    if row and (row[0] or "").startswith(TRANSCRIPT_MARKER):
        return row[0]
    return None


def store_transcript(
    db_path: str,
    message_id: str,
    chat_jid: str,
    text: str,
    model: str,
    attempts: int = 3,
    provider: str = "whisper_cpp",
) -> bool:
    """Write a transcript onto the message row. True when it is there afterwards.

    The statement is parameter-bound. Transcripts contain apostrophes, and
    quoting them by hand produces invalid SQL that skips exactly those rows.

    Only a blank field or a transcript from an earlier run is replaced, so a
    real text message is never overwritten. The bridge writes to the same
    database continuously, so a single attempt can lose the race with
    "database is locked"; after writing, the row is read back, because a silent
    miss would leave the text invisible to clients, which is the whole point of
    storing it.
    """
    content = f"{label(model, provider)}{text}"
    for attempt in range(attempts):
        try:
            connection = sqlite3.connect(db_path, timeout=10)
            try:
                with connection:
                    cursor = connection.execute(
                        "UPDATE messages SET content = ? "
                        "WHERE id = ? AND chat_jid = ? AND media_type = 'audio' "
                        "AND (content IS NULL OR content = '' OR content LIKE ?)",
                        (content, message_id, chat_jid, f"{TRANSCRIPT_MARKER}%"),
                    )
                    changed = cursor.rowcount
                row = connection.execute(
                    "SELECT content FROM messages WHERE id = ? AND chat_jid = ?", (message_id, chat_jid)
                ).fetchone()
            finally:
                connection.close()
            if row and (row[0] or "") == content:
                return True
            if changed == 0:
                # The statement ran and matched nothing: no such audio row, or
                # the row holds text a human wrote. Retrying cannot change
                # that, and only a lock is worth waiting out.
                return False
        except sqlite3.Error:
            pass
        if attempt + 1 < attempts:
            time.sleep(2 * (attempt + 1))
    return False
