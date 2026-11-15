import json
import os
import signal
import subprocess
import sys
from pathlib import Path

import pytest

import main as mcp_main

SERVER_DIR = Path(__file__).resolve().parent.parent


def test_shutdown_handler_exits_without_finalization(monkeypatch):
    calls = []
    monkeypatch.setattr(mcp_main.os, "_exit", lambda code: calls.append(code))

    mcp_main.shutdown_handler(signal.SIGTERM, None)

    assert calls == [0]


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX signals")
def test_sigterm_with_open_stdin_exits_cleanly(tmp_path):
    """Regression: SIGTERM while the stdio reader thread holds the stdin lock
    used to abort in interpreter finalization (SIGABRT, _enter_buffered_busy)."""
    env = dict(
        os.environ,
        WHATSAPP_DB_PATH=str(tmp_path / "messages.db"),
        WHATSMEOW_DB_PATH=str(tmp_path / "whatsapp.db"),
    )
    proc = subprocess.Popen(
        [sys.executable, "main.py"],
        cwd=SERVER_DIR,
        env=env,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        init = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "test", "version": "0"},
            },
        }
        proc.stdin.write((json.dumps(init) + "\n").encode())
        proc.stdin.flush()
        assert proc.stdout.readline()  # stdio loop is up and reading stdin

        # Two signals, as a client escalating its shutdown would send.
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.send_signal(signal.SIGTERM)

        returncode = proc.wait(timeout=10)
        stderr = proc.stderr.read()
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()

    assert b"Fatal Python error" not in stderr
    assert returncode == 0
