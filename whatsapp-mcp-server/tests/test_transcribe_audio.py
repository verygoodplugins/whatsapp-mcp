import os

import whatsapp


class DummyCompleted:
    def __init__(self, returncode=0, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def test_transcribe_audio_whisper_missing(monkeypatch):
    monkeypatch.setattr(whatsapp.shutil, "which", lambda _bin: None)

    result = whatsapp.transcribe_audio("MSGID", "123@s.whatsapp.net")

    assert result["success"] is False
    assert "not found on PATH" in result["message"]


def test_transcribe_audio_download_failure(monkeypatch):
    monkeypatch.setattr(whatsapp.shutil, "which", lambda _bin: "/usr/bin/whisper")
    monkeypatch.setattr(whatsapp, "download_media", lambda _mid, _jid: None)

    result = whatsapp.transcribe_audio("MSGID", "123@s.whatsapp.net")

    assert result["success"] is False
    assert "download" in result["message"].lower()


def test_transcribe_audio_whisper_error(monkeypatch, tmp_path):
    audio_file = tmp_path / "note.ogg"
    audio_file.write_bytes(b"fake")
    monkeypatch.setattr(whatsapp.shutil, "which", lambda _bin: "/usr/bin/whisper")
    monkeypatch.setattr(whatsapp, "download_media", lambda _mid, _jid: str(audio_file))
    monkeypatch.setattr(
        whatsapp.subprocess,
        "run",
        lambda *a, **k: DummyCompleted(returncode=1, stderr="boom"),
    )

    result = whatsapp.transcribe_audio("MSGID", "123@s.whatsapp.net")

    assert result["success"] is False
    assert "boom" in result["message"]


def test_transcribe_audio_success(monkeypatch, tmp_path):
    audio_file = tmp_path / "note.ogg"
    audio_file.write_bytes(b"fake")
    monkeypatch.setattr(whatsapp.shutil, "which", lambda _bin: "/usr/bin/whisper")
    monkeypatch.setattr(whatsapp, "download_media", lambda _mid, _jid: str(audio_file))

    def fake_run(cmd, capture_output=True, text=True):
        # Emulate the Whisper CLI writing "<base>.txt" into --output_dir.
        out_dir = cmd[cmd.index("--output_dir") + 1]
        base = os.path.splitext(os.path.basename(cmd[1]))[0]
        with open(os.path.join(out_dir, base + ".txt"), "w", encoding="utf-8") as fh:
            fh.write("  hello world  \n")
        return DummyCompleted(returncode=0)

    monkeypatch.setattr(whatsapp.subprocess, "run", fake_run)

    result = whatsapp.transcribe_audio("MSGID", "123@s.whatsapp.net", language="pt", model="small")

    assert result["success"] is True
    assert result["text"] == "hello world"
    assert result["model"] == "small"
    assert result["language"] == "pt"
    assert result["file_path"] == str(audio_file)
