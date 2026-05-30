"""Tests for MCP transport selection."""

import os
import subprocess
import sys
from pathlib import Path

import pytest

import main
from main import resolve_port, resolve_transport

SERVER_DIR = Path(__file__).resolve().parents[1]
RUN_SERVER_WITH_FAKE_RUN = """
import main

calls = []


def fake_run(*, transport):
    calls.append(transport)


main.mcp.run = fake_run
main.run_mcp_server()
assert calls
"""


@pytest.fixture(autouse=True)
def reset_mcp_bind_settings(monkeypatch):
    monkeypatch.setattr(main.mcp.settings, "host", "127.0.0.1")
    monkeypatch.setattr(main.mcp.settings, "port", 8089)


class TestResolveTransport:
    """Tests for resolve_transport()."""

    def test_default_is_stdio(self):
        assert resolve_transport(None) == "stdio"
        assert resolve_transport("") == "stdio"

    def test_http_alias_maps_to_streamable_http(self):
        assert resolve_transport("http") == "streamable-http"
        assert resolve_transport("streamable-http") == "streamable-http"
        assert resolve_transport("streamable_http") == "streamable-http"

    def test_sse(self):
        assert resolve_transport("sse") == "sse"

    def test_case_and_whitespace_insensitive(self):
        assert resolve_transport("  STDIO ") == "stdio"
        assert resolve_transport("Http") == "streamable-http"

    def test_invalid_value_exits(self):
        with pytest.raises(SystemExit):
            resolve_transport("websocket")


class TestResolvePort:
    """Tests for resolve_port()."""

    def test_default(self):
        assert resolve_port(None) == 8089
        assert resolve_port("") == 8089

    def test_valid(self):
        assert resolve_port("9000") == 9000

    def test_invalid_exits(self):
        with pytest.raises(SystemExit):
            resolve_port("not-a-number")


def test_fastmcp_default_bind_matches_env_contract():
    assert main.mcp.settings.host == "127.0.0.1"
    assert main.mcp.settings.port == 8089


@pytest.mark.parametrize(
    ("env_value", "expected_transport"),
    [
        (None, "stdio"),
        ("stdio", "stdio"),
        ("http", "streamable-http"),
        ("sse", "sse"),
    ],
)
def test_run_mcp_server_uses_configured_transport(monkeypatch, capsys, env_value, expected_transport):
    calls = []
    monkeypatch.delenv("WHATSAPP_MCP_HOST", raising=False)
    monkeypatch.delenv("WHATSAPP_MCP_PORT", raising=False)

    if env_value is None:
        monkeypatch.delenv("WHATSAPP_MCP_TRANSPORT", raising=False)
    else:
        monkeypatch.setenv("WHATSAPP_MCP_TRANSPORT", env_value)

    def fake_run(*, transport):
        calls.append(transport)

    monkeypatch.setattr(main.mcp, "run", fake_run)

    main.run_mcp_server()

    assert calls == [expected_transport]
    captured = capsys.readouterr()
    if expected_transport == "stdio":
        assert captured.err == ""
    else:
        assert f"via {expected_transport}" in captured.err


def test_run_mcp_server_applies_remote_bind_settings(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_MCP_TRANSPORT", "http")
    monkeypatch.setenv("WHATSAPP_MCP_HOST", "0.0.0.0")
    monkeypatch.setenv("WHATSAPP_MCP_PORT", "9090")

    def fake_run(*, transport):
        calls.append(transport)

    monkeypatch.setattr(main.mcp, "run", fake_run)

    main.run_mcp_server()

    assert calls == ["streamable-http"]
    assert main.mcp.settings.host == "0.0.0.0"
    assert main.mcp.settings.port == 9090


def test_stdio_import_and_run_ignore_invalid_port_env():
    env = os.environ.copy()
    env["WHATSAPP_MCP_TRANSPORT"] = "stdio"
    env["WHATSAPP_MCP_PORT"] = "not-a-number"

    result = subprocess.run(
        [sys.executable, "-c", RUN_SERVER_WITH_FAKE_RUN],
        cwd=SERVER_DIR,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )

    assert result.returncode == 0
    assert "Invalid WHATSAPP_MCP_PORT" not in result.stderr


def test_invalid_transport_env_exits_nonzero():
    env = os.environ.copy()
    env["WHATSAPP_MCP_TRANSPORT"] = "websocket"

    result = subprocess.run(
        [sys.executable, "main.py"],
        cwd=SERVER_DIR,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )

    assert result.returncode != 0
    assert "Invalid WHATSAPP_MCP_TRANSPORT='websocket'" in result.stderr


@pytest.mark.parametrize("transport", ["http", "sse"])
def test_invalid_port_env_exits_nonzero_for_remote_transport(transport):
    env = os.environ.copy()
    env["WHATSAPP_MCP_TRANSPORT"] = transport
    env["WHATSAPP_MCP_PORT"] = "not-a-number"

    result = subprocess.run(
        [sys.executable, "-c", RUN_SERVER_WITH_FAKE_RUN],
        cwd=SERVER_DIR,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )

    assert result.returncode != 0
    assert "Invalid WHATSAPP_MCP_PORT='not-a-number'" in result.stderr
