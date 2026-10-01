"""The converted .ogg must live next to the input: the bridge rejects media
outside WHATSAPP_MEDIA_ROOTS, and the system temp dir is never one of them."""

import os

import pytest

import audio


def test_converted_temp_file_is_created_next_to_the_input(tmp_path, monkeypatch):
    src = tmp_path / "voice.mp3"
    src.write_bytes(b"not really mp3")

    def fake_convert(input_file, output_file, bitrate, sample_rate):
        return output_file

    monkeypatch.setattr(audio, "convert_to_opus_ogg", fake_convert)

    converted = audio.convert_to_opus_ogg_temp(str(src))

    assert os.path.dirname(converted) == str(tmp_path)
    assert converted.endswith(".ogg")
    os.unlink(converted)


def test_converted_temp_file_is_removed_when_conversion_fails(tmp_path, monkeypatch):
    src = tmp_path / "voice.mp3"
    src.write_bytes(b"not really mp3")

    def failing_convert(*args):
        raise RuntimeError("ffmpeg missing")

    monkeypatch.setattr(audio, "convert_to_opus_ogg", failing_convert)

    with pytest.raises(RuntimeError):
        audio.convert_to_opus_ogg_temp(str(src))
    assert os.listdir(tmp_path) == ["voice.mp3"]
