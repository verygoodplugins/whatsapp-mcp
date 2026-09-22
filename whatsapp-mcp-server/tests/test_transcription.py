"""Tests for local voice-note transcription.

Two failure modes here are worth guarding permanently. Hand-quoted SQL broke on
apostrophes and skipped exactly the rows whose transcript contained one, and a
database write that failed silently left the text on disk but invisible to
clients — which is indistinguishable from "this note was never transcribed".
"""

import sqlite3
import subprocess
from unittest.mock import Mock

import pytest

import transcription


@pytest.fixture(autouse=True)
def clean_transcription_config(monkeypatch):
    for name in ("PROVIDER", "URL", "MODEL", "LANGUAGE", "API_KEY"):
        monkeypatch.delenv(f"WHATSAPP_TRANSCRIPTION_{name}", raising=False)
    monkeypatch.delenv("WHISPER_LANGUAGE", raising=False)


def _make_db(path):
    connection = sqlite3.connect(path)
    connection.executescript(
        """
        CREATE TABLE messages (
            id TEXT,
            chat_jid TEXT,
            content TEXT,
            media_type TEXT,
            PRIMARY KEY (id, chat_jid)
        );
        """
    )
    connection.executemany(
        "INSERT INTO messages (id, chat_jid, content, media_type) VALUES (?, ?, ?, ?)",
        [
            ("voice1", "chat@g.us", "", "audio"),
            ("voice1", "other@g.us", "", "audio"),
            ("voice2", "chat@g.us", None, "audio"),
            ("text1", "chat@g.us", "a message someone typed", ""),
            ("voice3", "chat@g.us", "a caption a human wrote", "audio"),
        ],
    )
    connection.commit()
    connection.close()
    return str(path)


@pytest.fixture
def db(tmp_path):
    return _make_db(tmp_path / "messages.db")


def _content(db_path, message_id, chat_jid="chat@g.us"):
    connection = sqlite3.connect(db_path)
    try:
        return connection.execute(
            "SELECT content FROM messages WHERE id = ? AND chat_jid = ?", (message_id, chat_jid)
        ).fetchone()[0]
    finally:
        connection.close()


def test_empty_content_is_filled(db):
    assert transcription.store_transcript(db, "voice1", "chat@g.us", "hello there", "ggml-small.bin")
    assert _content(db, "voice1").endswith("hello there")
    assert _content(db, "voice1").startswith(transcription.TRANSCRIPT_MARKER)


def test_null_content_is_filled(db):
    assert transcription.store_transcript(db, "voice2", "chat@g.us", "hello", "ggml-small.bin")
    assert _content(db, "voice2").endswith("hello")


def test_apostrophes_survive(db):
    """The regression that motivated parameter binding."""
    text = "Hey, ich hoffe, dir geht's gut — we can't skip this one"
    assert transcription.store_transcript(db, "voice1", "chat@g.us", text, "ggml-small.bin")
    assert _content(db, "voice1").endswith(text)


def test_backslashes_and_quotes_survive(db):
    text = "a \\ backslash, a \"quote\" and a 'single' one"
    assert transcription.store_transcript(db, "voice1", "chat@g.us", text, "ggml-small.bin")
    assert _content(db, "voice1").endswith(text)


def test_an_earlier_transcript_is_refreshed(db):
    transcription.store_transcript(db, "voice1", "chat@g.us", "first pass", "ggml-small.bin")
    transcription.store_transcript(db, "voice1", "chat@g.us", "better pass", "ggml-large-v3-turbo.bin")
    stored = _content(db, "voice1")
    assert stored.endswith("better pass")
    assert "large-v3-turbo" in stored


def test_a_human_written_message_is_never_overwritten(db):
    assert not transcription.store_transcript(db, "voice3", "chat@g.us", "machine text", "ggml-small.bin")
    assert _content(db, "voice3") == "a caption a human wrote"


def test_non_audio_rows_are_left_alone(db):
    assert not transcription.store_transcript(db, "text1", "chat@g.us", "machine text", "ggml-small.bin")
    assert _content(db, "text1") == "a message someone typed"


def test_a_failed_write_is_reported_rather_than_assumed(db, monkeypatch):
    def fail(*_args, **_kwargs):
        raise sqlite3.OperationalError("database is locked")

    monkeypatch.setattr(transcription.sqlite3, "connect", fail)
    monkeypatch.setattr(transcription.time, "sleep", lambda _s: None)
    assert not transcription.store_transcript(db, "voice1", "chat@g.us", "text", "m.bin", attempts=2)


def test_stored_transcript_only_reports_its_own_output(db):
    assert transcription.stored_transcript(db, "voice1", "chat@g.us") is None
    transcription.store_transcript(db, "voice1", "chat@g.us", "words", "m.bin")
    assert transcription.stored_transcript(db, "voice1", "chat@g.us").endswith("words")
    assert transcription.stored_transcript(db, "voice3", "chat@g.us") is None


def test_duplicate_ids_are_isolated_by_chat(db):
    assert transcription.store_transcript(db, "voice1", "other@g.us", "other words", "m.bin")
    assert transcription.stored_transcript(db, "voice1", "chat@g.us") is None
    assert _content(db, "voice1") == ""
    assert transcription.store_transcript(db, "voice1", "chat@g.us", "target words", "m.bin")
    assert transcription.stored_transcript(db, "voice1", "chat@g.us").endswith("target words")
    assert transcription.stored_transcript(db, "voice1", "other@g.us").endswith("other words")
    assert not transcription.store_transcript(db, "voice1", "absent@g.us", "wrong", "m.bin")


def test_missing_model_configuration_says_what_to_set(monkeypatch):
    monkeypatch.delenv("WHISPER_MODEL", raising=False)
    with pytest.raises(transcription.TranscriptionError) as excinfo:
        transcription.model_path()
    assert "WHISPER_MODEL" in str(excinfo.value)


def test_model_pointing_nowhere_is_reported(monkeypatch, tmp_path):
    monkeypatch.setenv("WHISPER_MODEL", str(tmp_path / "absent.bin"))
    with pytest.raises(transcription.TranscriptionError):
        transcription.model_path()


def test_output_is_collapsed_to_one_line():
    assert transcription.normalise(" a\n b \n\n c \n") == "a b c"


def test_decode_targets_mono_16k_pcm(tmp_path):
    command = transcription.decode_command(tmp_path / "a.ogg", tmp_path / "a.wav")
    assert command[command.index("-ar") + 1] == transcription.TARGET_SAMPLE_RATE
    assert command[command.index("-ac") + 1] == "1"


def test_whisper_runs_without_timestamps(tmp_path):
    command = transcription.whisper_command("m.bin", tmp_path / "a.wav", "de")
    assert "-nt" in command
    assert command[command.index("-l") + 1] == "de"


def test_transcribe_file_returns_recognised_text(tmp_path, monkeypatch):
    audio = tmp_path / "audio_1.ogg"
    audio.write_bytes(b"opus")
    monkeypatch.setattr(transcription.shutil, "which", lambda _tool: "/usr/bin/tool")

    def fake_run(command, **_kwargs):
        if command[0] == "ffmpeg":
            transcription.Path(command[-1]).write_bytes(b"wav")
            return subprocess.CompletedProcess(command, 0, b"", b"")
        return subprocess.CompletedProcess(command, 0, b"  recognised\n  words \n", b"")

    monkeypatch.setattr(transcription.subprocess, "run", fake_run)
    assert transcription.transcribe_file(audio, tmp_path, model="m.bin") == "recognised words"


def test_transcribe_file_surfaces_a_decode_failure(tmp_path, monkeypatch):
    audio = tmp_path / "audio_1.ogg"
    audio.write_bytes(b"broken")
    monkeypatch.setattr(transcription.shutil, "which", lambda _tool: "/usr/bin/tool")
    monkeypatch.setattr(
        transcription.subprocess,
        "run",
        lambda command, **_k: subprocess.CompletedProcess(command, 1, b"", b"Invalid data"),
    )
    with pytest.raises(transcription.TranscriptionError) as excinfo:
        transcription.transcribe_file(audio, tmp_path, model="m.bin")
    assert "Invalid data" in str(excinfo.value)


def test_missing_whisper_binary_says_what_to_install(tmp_path, monkeypatch):
    audio = tmp_path / "audio_1.ogg"
    audio.write_bytes(b"opus")
    monkeypatch.setattr(
        transcription.shutil,
        "which",
        lambda tool: "/usr/bin/ffmpeg" if tool == "ffmpeg" else None,
    )
    with pytest.raises(transcription.TranscriptionError) as excinfo:
        transcription.transcribe_file(audio, tmp_path, model="m.bin")
    assert "whisper" in str(excinfo.value).lower()


@pytest.fixture
def http_provider(tmp_path, monkeypatch):
    audio = tmp_path / "voice.ogg"
    audio.write_bytes(b"test opus")
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_PROVIDER", "openai_compatible")
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_URL", "http://127.0.0.1:8178/v1/audio/transcriptions")
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_MODEL", "parakeet")
    response = Mock(status_code=200)
    response.json.return_value = {"text": " recognised\nwords "}
    response.__enter__ = Mock(return_value=response)
    response.__exit__ = Mock(return_value=False)
    post = Mock(return_value=response)
    monkeypatch.setattr(transcription.requests.Session, "post", post)
    monkeypatch.setattr(transcription.shutil, "which", lambda _tool: None)
    monkeypatch.delenv("WHISPER_MODEL", raising=False)
    return audio, post, response


@pytest.mark.parametrize("language", ["auto", "de"])
def test_http_upload_contract_without_local_whisper(http_provider, monkeypatch, language):
    audio, post, response = http_provider
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_LANGUAGE", language)
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_API_KEY", "test-only-key")

    def upload(url, **kwargs):
        assert url.endswith("/v1/audio/transcriptions")
        assert kwargs["files"]["file"][1].read() == b"test opus"
        assert kwargs["data"] == {
            "model": "parakeet",
            "response_format": "json",
            **({"language": "de"} if language == "de" else {}),
        }
        assert kwargs["headers"] == {"Authorization": "Bearer test-only-key"}
        assert kwargs["allow_redirects"] is False
        assert kwargs["timeout"][1] == transcription.TRANSCRIPTION_TIMEOUT
        return response

    post.side_effect = upload
    assert transcription.transcribe_file(audio, audio.parent, provider="openai_compatible") == "recognised words"


@pytest.mark.parametrize("failure", ["timeout", "unauthorized", "redirect", "invalid_json", "empty", "wrong_type"])
def test_http_failures_are_actionable(http_provider, failure):
    audio, post, response = http_provider
    if failure == "timeout":
        post.side_effect = transcription.requests.Timeout("secret URL and key")
    elif failure in {"unauthorized", "redirect"}:
        response.status_code = 401 if failure == "unauthorized" else 307
    elif failure == "invalid_json":
        response.json.side_effect = ValueError("private response")
    else:
        response.json.return_value = {"text": " " if failure == "empty" else 123}
    with pytest.raises(transcription.TranscriptionError) as excinfo:
        transcription.transcribe_file(audio, audio.parent, provider="openai_compatible")
    assert "secret" not in str(excinfo.value) and "private" not in str(excinfo.value)
    assert post.call_count == 1


@pytest.mark.parametrize("missing", ["URL", "MODEL"])
def test_http_requires_explicit_configuration(http_provider, monkeypatch, missing):
    audio, post, _response = http_provider
    monkeypatch.delenv(f"WHATSAPP_TRANSCRIPTION_{missing}")
    with pytest.raises(transcription.TranscriptionError, match=f"WHATSAPP_TRANSCRIPTION_{missing}"):
        transcription.transcribe_file(audio, audio.parent, provider="openai_compatible")
    post.assert_not_called()


def test_provider_defaults_and_rejects_typos(monkeypatch):
    assert transcription.provider_name() == "whisper_cpp"
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_PROVIDER", "typo")
    with pytest.raises(transcription.TranscriptionError, match="PROVIDER"):
        transcription.provider_name()


def test_tool_dispatches_stores_and_reuses_chat_scoped_transcript(db, http_provider, monkeypatch):
    import main

    audio, post, _response = http_provider
    monkeypatch.setattr(main, "MESSAGES_DB_PATH", db)
    download = Mock(return_value=str(audio))
    monkeypatch.setattr(main, "whatsapp_download_media", download)
    transcription.store_transcript(db, "voice1", "other@g.us", "wrong chat", "m.bin")
    result = main.transcribe_audio("voice1", "chat@g.us")
    assert result["success"] and result["transcript"] == "[transcript (openai_compatible parakeet)] recognised words"
    assert _content(db, "voice1") == result["transcript"]
    assert _content(db, "voice1", "other@g.us").endswith("wrong chat")
    assert post.call_args.kwargs["headers"] == {}
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_PROVIDER", "unavailable")
    assert main.transcribe_audio("voice1", "chat@g.us")["transcript"] == result["transcript"]
    download.assert_called_once_with("voice1", "chat@g.us")
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_PROVIDER", "openai_compatible")
    assert main.transcribe_audio("voice1", "chat@g.us", force=True)["success"]
    assert post.call_count == 2


def test_subprocess_timeout_becomes_tool_error(monkeypatch):
    monkeypatch.setattr(
        transcription.subprocess, "run", Mock(side_effect=subprocess.TimeoutExpired("whisper-cli", 300))
    )
    with pytest.raises(transcription.TranscriptionError, match="TimeoutExpired"):
        transcription.run_command(["whisper-cli"], timeout=300)


def test_http_ignores_ambient_credentials_and_proxies(tmp_path, monkeypatch):
    audio = tmp_path / "voice.ogg"
    audio.write_bytes(b"test opus")
    netrc = tmp_path / "netrc"
    netrc.write_text("machine 127.0.0.1 login unrelated password test-only\n")
    monkeypatch.setenv("NETRC", str(netrc))
    monkeypatch.setenv("HTTP_PROXY", "http://unwanted-proxy.invalid:8000")
    monkeypatch.setenv("NO_PROXY", "")
    monkeypatch.setenv("WHATSAPP_TRANSCRIPTION_URL", "http://127.0.0.1:8178/v1/audio/transcriptions")

    def send(_session, request, **kwargs):
        assert kwargs["proxies"] == {}
        assert "Authorization" not in request.headers
        assert request.headers["Content-Type"].startswith("multipart/form-data;")
        assert b'name="model"\r\n\r\nparakeet' in request.body
        assert b'name="file"; filename="voice.ogg"' in request.body
        assert b"test opus" in request.body
        response = transcription.requests.Response()
        response.status_code = 200
        response._content = b'{"text":"hello"}'
        return response

    monkeypatch.setattr(transcription.requests.Session, "send", send)
    assert transcription.transcribe_http(audio, "parakeet") == "hello"
