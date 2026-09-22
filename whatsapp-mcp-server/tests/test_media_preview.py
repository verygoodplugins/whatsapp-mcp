"""Tests for rendering media as image content.

The gap these cover: download_media hands back a file path, which a client
without filesystem access cannot open, so media was unreachable for it. The
rendering path has to stay honest about what it cannot do — silently returning
nothing would look identical to "there is no media".
"""

import subprocess

import pytest

import media_preview


def _write(path, data=b"x"):
    path.write_bytes(data)
    return path


def test_audio_is_rejected_with_a_pointer_to_the_transcript(tmp_path):
    voice_note = _write(tmp_path / "audio_1.ogg")
    with pytest.raises(media_preview.PreviewError) as excinfo:
        media_preview.render_preview(voice_note)
    assert "transcribe_audio" in str(excinfo.value)


def test_missing_file_is_reported(tmp_path):
    with pytest.raises(media_preview.PreviewError) as excinfo:
        media_preview.render_preview(tmp_path / "gone.jpg")
    assert "not found" in str(excinfo.value).lower()


def test_small_image_passes_through_without_ffmpeg(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: None)
    image = _write(tmp_path / "image_1.png", b"png-bytes")
    data, image_format = media_preview.render_preview(image)
    assert data == b"png-bytes"
    assert image_format == "png"


def test_jpg_suffix_is_normalised_to_jpeg(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: None)
    image = _write(tmp_path / "image_1.jpg", b"jpeg-bytes")
    _, image_format = media_preview.render_preview(image)
    assert image_format == "jpeg"


def test_large_file_without_ffmpeg_fails_loudly(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: None)
    image = _write(tmp_path / "image_1.jpg", b"0" * (media_preview.MAX_PASSTHROUGH_BYTES + 1))
    with pytest.raises(media_preview.PreviewError) as excinfo:
        media_preview.render_preview(image)
    assert "ffmpeg" in str(excinfo.value)


def test_video_without_ffmpeg_fails_loudly(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: None)
    video = _write(tmp_path / "video_1.mp4")
    with pytest.raises(media_preview.PreviewError):
        media_preview.render_preview(video)


def test_scale_filter_never_enlarges():
    assert "force_original_aspect_ratio=decrease" in media_preview.scale_filter(800)
    assert "min(800,iw)" in media_preview.scale_filter(800)


def test_command_takes_a_single_frame_so_video_and_image_share_one_path(tmp_path):
    command = media_preview.ffmpeg_command(tmp_path / "in.mp4", tmp_path / "out.jpg", 640)
    assert command[:2] == ["ffmpeg", "-v"]
    assert "-frames:v" in command and command[command.index("-frames:v") + 1] == "1"
    assert media_preview.scale_filter(640) in command


def test_rendered_bytes_are_returned_and_the_temp_file_is_cleaned_up(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: "/usr/bin/ffmpeg")
    source = _write(tmp_path / "video_1.mp4")
    work_dir = tmp_path / "work"
    work_dir.mkdir()

    def fake_run(command, **kwargs):
        assert kwargs["stdin"] is subprocess.DEVNULL
        assert kwargs["timeout"] == media_preview.FFMPEG_TIMEOUT_SECONDS
        assert kwargs["shell"] is False
        destination = media_preview.Path(command[-1])
        destination.write_bytes(b"rendered-jpeg")
        return subprocess.CompletedProcess(command, 0, b"", b"")

    monkeypatch.setattr(media_preview.subprocess, "run", fake_run)
    data, image_format = media_preview.render_preview(source, work_dir=work_dir)
    assert (data, image_format) == (b"rendered-jpeg", "jpeg")
    assert list(work_dir.iterdir()) == []


def test_render_timeout_is_reported_and_the_temp_file_is_cleaned_up(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: "/usr/bin/ffmpeg")
    source = _write(tmp_path / "video_1.mp4")
    work_dir = tmp_path / "work"
    work_dir.mkdir()

    def fake_run(command, **_kwargs):
        media_preview.Path(command[-1]).write_bytes(b"partial-preview")
        raise subprocess.TimeoutExpired(command, media_preview.FFMPEG_TIMEOUT_SECONDS)

    monkeypatch.setattr(media_preview.subprocess, "run", fake_run)
    with pytest.raises(media_preview.PreviewError, match="within 60 seconds"):
        media_preview.render_preview(source, work_dir=work_dir)
    assert list(work_dir.iterdir()) == []


@pytest.mark.parametrize("max_dimension", [0, -1, 2049, 1.5, True])
def test_invalid_max_dimension_is_rejected(max_dimension):
    with pytest.raises(media_preview.PreviewError, match="1 to 2048"):
        media_preview.validate_max_dimension(max_dimension)


def test_ffmpeg_failure_surfaces_its_reason(tmp_path, monkeypatch):
    monkeypatch.setattr(media_preview.shutil, "which", lambda _: "/usr/bin/ffmpeg")
    source = _write(tmp_path / "document_1.pdf")

    def fake_run(command, **_kwargs):
        return subprocess.CompletedProcess(command, 1, b"", b"Invalid data found")

    monkeypatch.setattr(media_preview.subprocess, "run", fake_run)
    with pytest.raises(media_preview.PreviewError) as excinfo:
        media_preview.render_preview(source)
    assert "Invalid data found" in str(excinfo.value)
