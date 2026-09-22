"""Render WhatsApp media as image content a client can actually look at.

``download_media`` only hands back a local file path. A client that speaks MCP
but has no filesystem — Claude Desktop, a claude.ai chat — cannot open that
path, so images and videos are unreachable for it even though the server has
full access to the messages.

This module turns a media file into JPEG bytes: an image as itself, a video as
its first frame. Everything is downscaled first, because a single modern phone
photo is several megabytes and would flood the context of whatever asked for
it.
"""

import shutil
import subprocess
from pathlib import Path

# Voice notes are handled by transcription, not by rendering.
AUDIO_SUFFIXES = frozenset({".ogg", ".opus", ".m4a", ".mp3", ".wav", ".aac", ".amr"})

# Formats a client can display without any re-encoding.
PASSTHROUGH_SUFFIXES = frozenset({".jpg", ".jpeg", ".png", ".webp", ".gif"})

# Cap for handing a file back untouched when ffmpeg is unavailable. Above this
# an image is large enough that returning it unscaled is the wrong answer.
MAX_PASSTHROUGH_BYTES = 4_000_000

DEFAULT_MAX_DIMENSION = 1024
MIN_MAX_DIMENSION = 1
MAX_MAX_DIMENSION = 2048
FFMPEG_TIMEOUT_SECONDS = 60


class PreviewError(RuntimeError):
    """Media cannot be rendered as an image, with a reason worth showing."""


def is_audio(path: str | Path) -> bool:
    """True when the file is a voice note or other audio rather than a picture."""
    return Path(path).suffix.lower() in AUDIO_SUFFIXES


def ffmpeg_available() -> bool:
    return shutil.which("ffmpeg") is not None


def validate_max_dimension(max_dimension: int) -> None:
    """Reject preview dimensions outside the bounded, context-safe range."""
    if (
        not isinstance(max_dimension, int)
        or isinstance(max_dimension, bool)
        or not (MIN_MAX_DIMENSION <= max_dimension <= MAX_MAX_DIMENSION)
    ):
        raise PreviewError(f"max_dimension must be an integer from {MIN_MAX_DIMENSION} to {MAX_MAX_DIMENSION} pixels")


def scale_filter(max_dimension: int) -> str:
    """An ffmpeg filter that shrinks to fit a box but never enlarges."""
    return f"scale='min({max_dimension},iw)':'min({max_dimension},ih)':force_original_aspect_ratio=decrease"


def ffmpeg_command(source: Path, destination: Path, max_dimension: int) -> list[str]:
    """Build the render command.

    ``-frames:v 1`` takes the only frame a still image has and the first frame
    of a video, so one command covers both.
    """
    return [
        "ffmpeg",
        "-v",
        "error",
        "-y",
        "-i",
        str(source),
        "-frames:v",
        "1",
        "-vf",
        scale_filter(max_dimension),
        "-q:v",
        "4",
        str(destination),
    ]


def render_preview(
    path: str | Path,
    max_dimension: int = DEFAULT_MAX_DIMENSION,
    work_dir: str | Path | None = None,
) -> tuple[bytes, str]:
    """Return ``(image_bytes, image_format)`` for a media file.

    Raises:
        PreviewError: for audio, for media ffmpeg cannot read, and when no
            renderer is available and the file is too large to pass through.
    """
    source = Path(path)
    validate_max_dimension(max_dimension)
    if is_audio(source):
        raise PreviewError(
            "This message is audio, not an image. Use transcribe_audio, then read "
            "its stored transcript with list_messages."
        )
    if not source.is_file():
        raise PreviewError(f"Media file not found: {source}")

    suffix = source.suffix.lower()

    if not ffmpeg_available():
        if suffix in PASSTHROUGH_SUFFIXES and source.stat().st_size <= MAX_PASSTHROUGH_BYTES:
            image_format = "jpeg" if suffix in {".jpg", ".jpeg"} else suffix.lstrip(".")
            return source.read_bytes(), image_format
        raise PreviewError("ffmpeg is required to render this media at a size worth returning")

    destination = Path(work_dir or source.parent) / f"{source.stem}.preview.jpg"
    try:
        result = subprocess.run(
            ffmpeg_command(source, destination, max_dimension),
            capture_output=True,
            check=False,
            shell=False,
            stdin=subprocess.DEVNULL,
            timeout=FFMPEG_TIMEOUT_SECONDS,
        )
        if result.returncode != 0 or not destination.exists():
            detail = result.stderr.decode(errors="replace").strip()[:200]
            raise PreviewError(f"Could not render this media as an image: {detail}")
        return destination.read_bytes(), "jpeg"
    except subprocess.TimeoutExpired as exc:
        raise PreviewError(f"Could not render this media within {FFMPEG_TIMEOUT_SECONDS} seconds") from exc
    finally:
        destination.unlink(missing_ok=True)
