"""Tool-level media preview coverage."""

import base64

import pytest
from mcp.server.fastmcp.exceptions import ToolError

import main
import media_preview


def test_view_media_returns_mcp_image_content(tmp_path, monkeypatch):
    source = tmp_path / "photo.png"
    source.write_bytes(b"source-bytes")
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: str(source))
    monkeypatch.setattr(media_preview, "render_preview", lambda *_args, **_kwargs: (b"image-bytes", "png"))

    image = main.view_media("message-1", "chat@g.us")

    assert image.to_image_content().type == "image"
    assert image.to_image_content().mimeType == "image/png"
    assert base64.b64decode(image.to_image_content().data) == b"image-bytes"


def test_view_media_rejects_invalid_dimension_before_download(monkeypatch):
    def should_not_download(*_args):
        raise AssertionError("invalid input must not download media")

    monkeypatch.setattr(main, "whatsapp_download_media", should_not_download)

    result = main.view_media("message-1", "chat@g.us", max_dimension=2049)

    assert result == {
        "success": False,
        "message": "max_dimension must be an integer from 1 to 2048 pixels",
    }


@pytest.mark.asyncio
async def test_view_media_mcp_uses_png_mime_for_png_bytes_in_generated_jpg(tmp_path, monkeypatch):
    png_bytes = base64.b64decode(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADElEQVR42mNk+M/wHwAF/gL+9qMyAAAAAElFTkSuQmCC"
    )
    source = tmp_path / "generated.jpg"
    source.write_bytes(png_bytes)
    monkeypatch.setattr(main, "whatsapp_download_media", lambda *_args: str(source))
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: None)

    content = (await main.mcp.call_tool("view_media", {"message_id": "message-1", "chat_jid": "chat@g.us"}))[0]

    assert content.type == "image"
    assert content.mimeType == "image/png"
    assert base64.b64decode(content.data) == png_bytes


@pytest.mark.parametrize("max_dimension", [True, "1024", 1.5])
async def test_view_media_mcp_rejects_coerced_dimensions_before_download(monkeypatch, max_dimension):
    download_calls = 0

    def should_not_download(*_args):
        nonlocal download_calls
        download_calls += 1
        raise AssertionError("invalid MCP input must not download media")

    monkeypatch.setattr(main, "whatsapp_download_media", should_not_download)

    with pytest.raises(ToolError, match="Input should be a valid integer"):
        await main.mcp.call_tool(
            "view_media",
            {"message_id": "message-1", "chat_jid": "chat@g.us", "max_dimension": max_dimension},
        )

    assert download_calls == 0
