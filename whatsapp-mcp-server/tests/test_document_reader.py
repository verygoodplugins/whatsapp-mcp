"""Tests for reading PDF documents as text and page images.

The gap these cover: a PDF sent as a WhatsApp document was downloadable but
unreadable for a remote client — view_media handed it to ffmpeg, which failed
with "Invalid data found when processing input".
"""

import base64

import pytest

import document_reader
import main
import media_preview

needs_poppler = pytest.mark.skipif(not document_reader.poppler_available(), reason="poppler-utils not installed")


def _pdf(pages: list[str]) -> bytes:
    """Build a minimal valid PDF with one line of Helvetica text per page."""
    kids = " ".join(f"{4 + 2 * i} 0 R" for i in range(len(pages)))
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        f"<< /Type /Pages /Kids [{kids}] /Count {len(pages)} >>".encode(),
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    for i, text in enumerate(pages):
        stream = f"BT /F1 24 Tf 72 700 Td ({text}) Tj ET".encode()
        objects.append(
            f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
            f"/Resources << /Font << /F1 3 0 R >> >> /Contents {5 + 2 * i} 0 R >>".encode()
        )
        objects.append(b"<< /Length %d >>\nstream\n%s\nendstream" % (len(stream), stream))

    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for number, body in enumerate(objects, start=1):
        offsets.append(len(out))
        out += b"%d 0 obj\n%s\nendobj\n" % (number, body)
    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objects) + 1)
    out += b"".join(b"%010d 00000 n \n" % offset for offset in offsets)
    out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objects) + 1, xref)
    return bytes(out)


def _write_pdf(tmp_path, pages, name="document_20261021_221558_ABC"):
    # Named like the bridge names documents: no extension.
    path = tmp_path / name
    path.write_bytes(_pdf(pages))
    return path


def test_pdf_is_detected_by_signature_not_extension(tmp_path):
    assert document_reader.is_pdf(_write_pdf(tmp_path, ["hello"]))
    not_pdf = tmp_path / "document.pdf"
    not_pdf.write_bytes(b"PK\x03\x04docx")
    assert not document_reader.is_pdf(not_pdf)


def test_max_pages_is_bounded(tmp_path):
    with pytest.raises(document_reader.DocumentError, match="max_pages"):
        document_reader.read_pdf(_write_pdf(tmp_path, ["a"]), tmp_path, max_pages=21)


@needs_poppler
def test_read_pdf_returns_text_and_one_image_per_page(tmp_path):
    work_dir = tmp_path / "work"
    work_dir.mkdir()
    result = document_reader.read_pdf(_write_pdf(tmp_path, ["First page", "Second page"]), work_dir)

    assert result["page_count"] == 2
    assert (result["first_page"], result["last_page"]) == (1, 2)
    assert "First page" in result["text"] and "Second page" in result["text"]
    assert len(result["images"]) == 2
    assert all(image.startswith(b"\xff\xd8\xff") for image in result["images"])


@needs_poppler
def test_read_pdf_pages_through_a_long_document(tmp_path):
    work_dir = tmp_path / "work"
    work_dir.mkdir()
    pages = [f"Page {n}" for n in range(1, 13)]
    result = document_reader.read_pdf(_write_pdf(tmp_path, pages), work_dir, first_page=11, max_pages=5)

    assert (result["first_page"], result["last_page"]) == (11, 12)
    assert len(result["images"]) == 2
    assert "Page 11" in result["text"]


@needs_poppler
async def test_read_document_tool_returns_text_then_page_images(tmp_path, monkeypatch):
    source = _write_pdf(tmp_path, ["Passport copy"])
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: str(source))

    content = await main.mcp.call_tool("read_document", {"message_id": "m1", "chat_jid": "chat@s.whatsapp.net"})
    if isinstance(content, tuple):
        content = content[0]

    assert content[0].type == "text"
    assert "1 page(s)" in content[0].text and "Passport copy" in content[0].text
    assert content[1].type == "image"
    assert content[1].mimeType == "image/jpeg"
    assert base64.b64decode(content[1].data).startswith(b"\xff\xd8\xff")


@needs_poppler
def test_view_media_renders_pdf_pages_instead_of_failing_in_ffmpeg(tmp_path, monkeypatch):
    source = _write_pdf(tmp_path, ["one", "two"])
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: str(source))

    def ffmpeg_must_not_run(*_args, **_kwargs):
        raise AssertionError("PDFs must not go through ffmpeg")

    monkeypatch.setattr(media_preview, "render_preview", ffmpeg_must_not_run)

    images = main.view_media("m1", "chat@s.whatsapp.net")

    assert len(images) == 2
    assert all(image.to_image_content().mimeType == "image/jpeg" for image in images)


def test_read_document_reports_unsupported_documents(tmp_path, monkeypatch):
    source = tmp_path / "document_20261021_000000_XYZ"
    source.write_bytes(b"PK\x03\x04not-an-image")
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: str(source))
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: None)

    result = main.read_document("m1", "chat@s.whatsapp.net")

    assert result["success"] is False
    assert "not a PDF" in result["message"]
