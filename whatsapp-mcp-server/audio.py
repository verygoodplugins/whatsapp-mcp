import os
import shutil
import subprocess
import tempfile


def _resolve_ffmpeg() -> str:
    """Return an absolute path to the ffmpeg binary.

    Resolving via shutil.which() at call time (and rejecting a missing binary)
    means we surface a clear error instead of silently invoking an unexpected
    "ffmpeg" found earlier on PATH. Setting FFMPEG_PATH lets users pin a
    known-good absolute path (e.g. /opt/homebrew/bin/ffmpeg) and bypass PATH
    lookup entirely.
    """
    pinned = os.environ.get("FFMPEG_PATH", "").strip()
    if pinned:
        if not os.path.isabs(pinned):
            raise RuntimeError(f"FFMPEG_PATH must be an absolute path, got: {pinned!r}")
        if not os.path.isfile(pinned) or not os.access(pinned, os.X_OK):
            raise RuntimeError(f"FFMPEG_PATH does not point to an executable file: {pinned}")
        return pinned

    found = shutil.which("ffmpeg")
    if not found:
        raise RuntimeError("ffmpeg not found on PATH. Install ffmpeg or set FFMPEG_PATH to an absolute binary path.")
    return found


def convert_to_opus_ogg(input_file, output_file=None, bitrate="32k", sample_rate=24000):
    """
    Convert an audio file to Opus format in an Ogg container.

    Args:
        input_file (str): Path to the input audio file
        output_file (str, optional): Path to save the output file. If None, replaces the
                                    extension of input_file with .ogg
        bitrate (str, optional): Target bitrate for Opus encoding (default: "32k")
        sample_rate (int, optional): Sample rate for output (default: 24000)

    Returns:
        str: Path to the converted file

    Raises:
        FileNotFoundError: If the input file doesn't exist
        RuntimeError: If the ffmpeg conversion fails
    """
    if not os.path.isfile(input_file):
        raise FileNotFoundError(f"Input file not found: {input_file}")

    # If no output file is specified, replace the extension with .ogg
    if output_file is None:
        output_file = os.path.splitext(input_file)[0] + ".ogg"

    # Ensure the output directory exists
    output_dir = os.path.dirname(output_file)
    if output_dir and not os.path.exists(output_dir):
        os.makedirs(output_dir)

    # Build the ffmpeg command. Resolve to an absolute binary path so we don't
    # silently pick up an unintended "ffmpeg" earlier on PATH.
    ffmpeg_bin = _resolve_ffmpeg()
    cmd = [
        ffmpeg_bin,
        "-i",
        input_file,
        "-c:a",
        "libopus",
        "-b:a",
        bitrate,
        "-ar",
        str(sample_rate),
        "-application",
        "voip",  # Optimize for voice
        "-vbr",
        "on",  # Variable bitrate
        "-compression_level",
        "10",  # Maximum compression
        "-frame_duration",
        "60",  # 60ms frames (good for voice)
        "-y",  # Overwrite output file if it exists
        output_file,
    ]

    try:
        # Run the ffmpeg command and capture output
        subprocess.run(cmd, capture_output=True, text=True, check=True)
        return output_file
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"Failed to convert audio. You likely need to install ffmpeg {e.stderr}")


def convert_to_opus_ogg_temp(input_file, bitrate="32k", sample_rate=24000):
    """
    Convert an audio file to Opus format in an Ogg container and store in a temporary file.

    Args:
        input_file (str): Path to the input audio file
        bitrate (str, optional): Target bitrate for Opus encoding (default: "32k")
        sample_rate (int, optional): Sample rate for output (default: 24000)

    Returns:
        str: Path to the temporary file with the converted audio

    Raises:
        FileNotFoundError: If the input file doesn't exist
        RuntimeError: If the ffmpeg conversion fails
    """
    # Create a temporary file with .ogg extension
    temp_file = tempfile.NamedTemporaryFile(suffix=".ogg", delete=False)
    temp_file.close()

    try:
        # Convert the audio
        convert_to_opus_ogg(input_file, temp_file.name, bitrate, sample_rate)
        return temp_file.name
    except Exception as e:
        # Clean up the temporary file if conversion fails
        if os.path.exists(temp_file.name):
            os.unlink(temp_file.name)
        raise e


if __name__ == "__main__":
    # Example usage
    import sys

    if len(sys.argv) < 2:
        print("Usage: python audio.py input_file [output_file]")
        sys.exit(1)

    input_file = sys.argv[1]

    try:
        result = convert_to_opus_ogg_temp(input_file)
        print(f"Successfully converted to: {result}")
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)
