"""stage_file copies files from opt-in WHATSAPP_STAGE_ROOTS into the outbox."""

import os

import pytest

import whatsapp


@pytest.fixture
def dirs(tmp_path, monkeypatch):
    src = tmp_path / "work"
    src.mkdir()
    outbox = tmp_path / "outbox"
    outbox.mkdir()
    monkeypatch.setenv("WHATSAPP_STAGE_ROOTS", str(src))
    monkeypatch.setenv("WHATSAPP_MEDIA_ROOTS", str(outbox))
    monkeypatch.delenv("WHATSAPP_STAGE_MAX_MB", raising=False)
    return src, outbox


def test_disabled_without_stage_roots(tmp_path, monkeypatch):
    monkeypatch.delenv("WHATSAPP_STAGE_ROOTS", raising=False)
    f = tmp_path / "a.pdf"
    f.write_bytes(b"x")
    ok, msg, staged = whatsapp.stage_file(str(f))
    assert not ok and staged is None
    assert "WHATSAPP_STAGE_ROOTS" in msg


def test_copies_into_outbox_keeping_name(dirs):
    src, outbox = dirs
    f = src / "Angebot.pdf"
    f.write_bytes(b"pdf-bytes")
    ok, _, staged = whatsapp.stage_file(str(f))
    assert ok
    assert os.path.basename(staged) == "Angebot.pdf"
    assert os.path.realpath(staged).startswith(os.path.realpath(outbox) + os.sep)
    assert open(staged, "rb").read() == b"pdf-bytes"
    assert f.exists()  # source untouched


def test_rejects_outside_roots(dirs, tmp_path):
    other = tmp_path / "secret.txt"
    other.write_text("x")
    ok, msg, _ = whatsapp.stage_file(str(other))
    assert not ok and "outside" in msg


def test_rejects_symlink_escaping_root(dirs, tmp_path):
    src, _ = dirs
    target = tmp_path / "secret.txt"
    target.write_text("x")
    link = src / "harmless.txt"
    link.symlink_to(target)
    ok, msg, _ = whatsapp.stage_file(str(link))
    assert not ok and "outside" in msg


@pytest.mark.parametrize("rel", [".env", ".ssh/id_rsa"])
def test_rejects_hidden_paths(dirs, rel):
    src, _ = dirs
    f = src / rel
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_text("x")
    ok, msg, _ = whatsapp.stage_file(str(f))
    assert not ok and "hidden" in msg


def test_rejects_relative_path(dirs):
    ok, msg, _ = whatsapp.stage_file("relative/file.pdf")
    assert not ok and "absolute" in msg


def test_rejects_too_large(dirs, monkeypatch):
    src, _ = dirs
    monkeypatch.setenv("WHATSAPP_STAGE_MAX_MB", "0.001")
    f = src / "big.bin"
    f.write_bytes(b"0" * 4096)
    ok, msg, _ = whatsapp.stage_file(str(f))
    assert not ok and "limit" in msg
