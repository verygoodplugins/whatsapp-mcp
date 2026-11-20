"""Read WhatsApp documents (PDFs) as text and page images.

``download_media`` returns a path on the bridge host, which a remote client
cannot open, and ``view_media`` renders through ffmpeg, which cannot read a
PDF. Scanned documents — passport copies, signed forms — usually carry no text
layer at all, so the page images matter as much as the text.

Rendering uses poppler (``pdfinfo``, ``pdftotext``, ``pdftoppm``). Pages are
bounded and downscaled for the same reason previews are: a long PDF must not
flood the context of whatever asked for it.
"""

import re
import shutil
import subprocess
from pathlib import Path

DEFAULT_MAX_PAGES = 5
MAX_MAX_PAGES = 20
# Enough for a long contract; beyond this the text is truncated with a note.
MAX_TEXT_CHARS = 60_000
POPPLER_TIMEOUT_SECONDS = 60

PDF_SIGNATURE = b"%PDF-"


class DocumentError(RuntimeError):
    """A document cannot be read, with a reason worth showing."""


def is_pdf(path: str | Path) -> bool:
    """True when the file's bytes are a PDF.

    The bridge saves documents without an extension, so the suffix proves
    nothing; the signature does.
    """
    try:
        with Path(path).open("rb") as document:
            return document.read(len(PDF_SIGNATURE)) == PDF_SIGNATURE
    except OSError:
        return False


def poppler_available() -> bool:
    return all(shutil.which(tool) for tool in ("pdfinfo", "pdftotext", "pdftoppm"))


def validate_max_pages(max_pages: int) -> None:
    if not isinstance(max_pages, int) or isinstance(max_pages, bool) or not (1 <= max_pages <= MAX_MAX_PAGES):
        raise DocumentError(f"max_pages must be an integer from 1 to {MAX_MAX_PAGES}")


def _run(command: list[str]) -> bytes:
    try:
        result = subprocess.run(
            command,
            capture_output=True,
            check=False,
            shell=False,
            stdin=subprocess.DEVNULL,
            timeout=POPPLER_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired as exc:
        raise DocumentError(f"{command[0]} did not finish within {POPPLER_TIMEOUT_SECONDS} seconds") from exc
    if result.returncode != 0:
        detail = result.stderr.decode(errors="replace").strip()[:200]
        raise DocumentError(f"Could not read this PDF ({command[0]}): {detail}")
    return result.stdout


def page_count(path: str | Path) -> int:
    output = _run(["pdfinfo", str(path)]).decode(errors="replace")
    match = re.search(r"^Pages:\s+(\d+)", output, re.MULTILINE)
    if not match:
        raise DocumentError("Could not determine the PDF's page count")
    return int(match.group(1))


def extract_text(path: str | Path, first_page: int, last_page: int) -> str:
    output = _run(["pdftotext", "-layout", "-f", str(first_page), "-l", str(last_page), str(path), "-"])
    return output.decode(errors="replace").strip()


def render_pages(
    path: str | Path,
    first_page: int,
    last_page: int,
    max_dimension: int,
    work_dir: str | Path,
) -> list[bytes]:
    """Return one JPEG per page, longest edge at most ``max_dimension``."""
    prefix = Path(work_dir) / "page"
    _run(
        [
            "pdftoppm",
            "-jpeg",
            "-jpegopt",
            "quality=80",
            "-scale-to",
            str(max_dimension),
            "-f",
            str(first_page),
            "-l",
            str(last_page),
            str(path),
            str(prefix),
        ]
    )
    # pdftoppm zero-pads page numbers to the document's width, so sort numerically.
    pages = sorted(Path(work_dir).glob("page-*.jpg"), key=lambda p: int(p.stem.rsplit("-", 1)[1]))
    return [page.read_bytes() for page in pages]


def read_pdf(
    path: str | Path,
    work_dir: str | Path,
    first_page: int = 1,
    max_pages: int = DEFAULT_MAX_PAGES,
    max_dimension: int = 1024,
    include_images: bool = True,
) -> dict:
    """Return the PDF's text and page images for a bounded page range.

    Returns a dict with ``page_count``, ``first_page``, ``last_page``,
    ``text``, ``text_truncated`` and ``images`` (JPEG bytes, one per page).
    """
    source = Path(path)
    validate_max_pages(max_pages)
    if not source.is_file():
        raise DocumentError(f"Document file not found: {source}")
    if not is_pdf(source):
        raise DocumentError("This document is not a PDF")
    if not poppler_available():
        raise DocumentError("poppler-utils (pdfinfo, pdftotext, pdftoppm) is required to read PDFs")

    total = page_count(source)
    if not isinstance(first_page, int) or isinstance(first_page, bool) or not (1 <= first_page <= max(total, 1)):
        raise DocumentError(f"first_page must be an integer from 1 to {total}")
    last_page = min(total, first_page + max_pages - 1)

    text = extract_text(source, first_page, last_page)
    truncated = len(text) > MAX_TEXT_CHARS
    if truncated:
        text = text[:MAX_TEXT_CHARS]

    images = render_pages(source, first_page, last_page, max_dimension, work_dir) if include_images else []
    return {
        "page_count": total,
        "first_page": first_page,
        "last_page": last_page,
        "text": text,
        "text_truncated": truncated,
        "images": images,
    }
