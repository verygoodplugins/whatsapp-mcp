"""send_audio_message must stage its converted file inside a bridge media root.

The bridge 403s any media_path outside WHATSAPP_MEDIA_ROOTS, and the transcoder
used to write into the system temp dir, so converting a non-.ogg file could
never succeed under the default confinement.
"""

import os

import pytest

import audio
import whatsapp


class _Resp:
    status_code = 200
    text = ""

    def json(self):
        return {"success": True, "message": "ok"}


@pytest.fixture
def home(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("WHATSAPP_MEDIA_ROOTS", raising=False)
    return tmp_path / "home"


def _recording_convert(monkeypatch, seen):
    def fake_convert(path, output_dir=None):
        seen["output_dir"] = output_dir
        out = os.path.join(output_dir, "converted.ogg")
        with open(out, "wb") as fh:
            fh.write(b"ogg")
        return out

    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", fake_convert)


def _capture_post(monkeypatch, seen):
    def fake_post(url, json, headers=None):
        seen["media_path"] = json["media_path"]
        return _Resp()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)


def test_converted_audio_is_staged_in_the_configured_root(tmp_path, monkeypatch):
    root = tmp_path / "outbox"
    root.mkdir()
    src = root / "voice.mp3"
    src.write_bytes(b"mp3")
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(root))
    seen = {}
    _recording_convert(monkeypatch, seen)
    _capture_post(monkeypatch, seen)

    ok, _ = whatsapp.send_audio_message("12025551234", str(src))

    assert ok is True
    assert seen["output_dir"] == os.path.realpath(root)
    assert whatsapp._within_media_roots(seen["media_path"], whatsapp._media_roots())
    assert not os.path.exists(seen["media_path"])  # cleaned up after the send


def test_default_outbox_is_created_and_used_when_roots_unset(home, monkeypatch):
    outbox = home / ".local" / "share" / "whatsapp-mcp" / "outbox"
    outbox.mkdir(parents=True)
    src = outbox / "voice.wav"
    src.write_bytes(b"wav")
    seen = {}
    _recording_convert(monkeypatch, seen)
    _capture_post(monkeypatch, seen)

    ok, _ = whatsapp.send_audio_message("12025551234", str(src))

    assert ok is True
    assert seen["output_dir"] == os.path.realpath(outbox)


def test_default_outbox_is_created_on_demand(home):
    roots = whatsapp._media_roots()

    assert whatsapp._audio_staging_dir(roots) == roots[0]
    assert os.path.isdir(roots[0])


def test_source_outside_the_roots_is_rejected_before_conversion(tmp_path, monkeypatch):
    root = tmp_path / "outbox"
    root.mkdir()
    elsewhere = tmp_path / "elsewhere.mp3"
    elsewhere.write_bytes(b"mp3")
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(root))
    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", lambda *a, **k: pytest.fail("must not convert"))
    monkeypatch.setattr(whatsapp.requests, "post", lambda *a, **k: pytest.fail("must not call the bridge"))

    ok, message = whatsapp.send_audio_message("12025551234", str(elsewhere))

    assert ok is False
    assert "outside the configured media roots" in message


def test_symlink_out_of_the_root_is_rejected(tmp_path, monkeypatch):
    root = tmp_path / "outbox"
    root.mkdir()
    target = tmp_path / "private.mp3"
    target.write_bytes(b"mp3")
    link = root / "innocent.mp3"
    link.symlink_to(target)
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(root))
    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", lambda *a, **k: pytest.fail("must not convert"))

    ok, message = whatsapp.send_audio_message("12025551234", str(link))

    assert ok is False
    assert "outside the configured media roots" in message


def test_no_usable_root_reports_a_clear_error(tmp_path, monkeypatch):
    missing = tmp_path / "missing-root"  # configured roots are never created
    src = tmp_path / "voice.mp3"
    src.write_bytes(b"mp3")
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", f"{missing}{os.pathsep}{tmp_path}")
    monkeypatch.setattr(whatsapp, "_audio_staging_dir", lambda roots: None)
    monkeypatch.setattr(whatsapp.audio, "convert_to_opus_ogg_temp", lambda *a, **k: pytest.fail("must not convert"))

    ok, message = whatsapp.send_audio_message("12025551234", str(src))

    assert ok is False
    assert "No existing, writable directory" in message


def test_staging_skips_roots_that_do_not_exist(tmp_path, monkeypatch):
    missing = tmp_path / "missing-root"
    usable = tmp_path / "usable"
    usable.mkdir()
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", f"{missing}{os.pathsep}{usable}")

    assert whatsapp._audio_staging_dir(whatsapp._media_roots()) == os.path.realpath(usable)
    assert not missing.exists()


def test_roots_use_the_platform_path_separator_not_commas(tmp_path, monkeypatch):
    a, b = tmp_path / "a", tmp_path / "b"
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", f" {a}{os.pathsep}{os.pathsep}relative/dir{os.pathsep}{b} ")

    assert whatsapp._media_roots() == [os.path.realpath(a), os.path.realpath(b)]

    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", f"{a},{b}")
    # One bogus entry, exactly what the bridge sees; nothing under a or b matches.
    assert not whatsapp._within_media_roots(str(a / "x.mp3"), whatsapp._media_roots())


def test_sibling_prefix_is_not_inside_the_root(tmp_path):
    root = str(tmp_path / "out")
    assert whatsapp._within_media_roots(os.path.join(root, "x.mp3"), [root])
    assert not whatsapp._within_media_roots(root + "baz/x.mp3", [root])


def test_convert_to_opus_ogg_temp_writes_into_output_dir(tmp_path, monkeypatch):
    out_dir = tmp_path / "outbox"
    out_dir.mkdir()
    monkeypatch.setattr(audio, "convert_to_opus_ogg", lambda *a, **k: None)

    result = audio.convert_to_opus_ogg_temp("in.mp3", output_dir=str(out_dir))

    assert os.path.dirname(result) == str(out_dir)
    assert result.endswith(".ogg")


def test_convert_to_opus_ogg_temp_defaults_to_system_temp(monkeypatch):
    monkeypatch.setattr(audio, "convert_to_opus_ogg", lambda *a, **k: None)

    result = audio.convert_to_opus_ogg_temp("in.mp3")
    try:
        assert os.path.dirname(result) == audio.tempfile.gettempdir()
    finally:
        os.unlink(result)
