"""Tests for MCP transport selection."""

import os
import subprocess
import sys
from pathlib import Path

import pytest
from starlette.responses import JSONResponse
from starlette.testclient import TestClient

import main
from main import MCPAuthMiddleware, resolve_port, resolve_transport

SERVER_DIR = Path(__file__).resolve().parents[1]
VALID_TOKEN = "0123456789abcdef0123456789abcdef"
RUN_SERVER_WITH_FAKE_RUN = """
import main

calls = []


def fake_stdio_run(*, transport):
    calls.append(("stdio", transport))


def fake_remote_run(transport, auth_enabled, token):
    calls.append(("remote", transport, auth_enabled, token))


main.mcp.run = fake_stdio_run
main.run_remote_mcp_app = fake_remote_run
main.run_mcp_server()
assert calls
"""


@pytest.fixture(autouse=True)
def reset_mcp_bind_settings(monkeypatch):
    monkeypatch.setattr(main.mcp.settings, "host", "127.0.0.1")
    monkeypatch.setattr(main.mcp.settings, "port", 8089)
    for var in (
        "WHATSAPP_MCP_TRANSPORT",
        "WHATSAPP_MCP_HOST",
        "WHATSAPP_MCP_PORT",
        "WHATSAPP_MCP_AUTH",
        "WHATSAPP_MCP_TOKEN",
    ):
        monkeypatch.delenv(var, raising=False)


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


async def ok_app(scope, receive, send):
    response = JSONResponse({"ok": True})
    await response(scope, receive, send)


@pytest.fixture
def auth_client():
    return TestClient(MCPAuthMiddleware(ok_app, VALID_TOKEN))


@pytest.mark.parametrize(
    "headers",
    [{}, {"Authorization": f"Bearer {VALID_TOKEN[:-1]}x"}, {"Authorization": f"Basic {VALID_TOKEN}"}],
)
def test_mcp_auth_middleware_rejects_missing_or_invalid_token(auth_client, headers):
    response = auth_client.get("/mcp", headers=headers)

    assert response.status_code == 401
    assert response.headers["www-authenticate"] == "Bearer"


@pytest.mark.parametrize(
    "headers",
    [
        {"Authorization": f"Bearer {VALID_TOKEN}"},
        {"Authorization": f"bearer {VALID_TOKEN}"},
        {"X-API-Key": VALID_TOKEN},
    ],
)
def test_mcp_auth_middleware_accepts_valid_bearer_or_api_key(auth_client, headers):
    response = auth_client.get("/mcp", headers=headers)

    assert response.status_code == 200
    assert response.json() == {"ok": True}


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

    if env_value is None:
        monkeypatch.delenv("WHATSAPP_MCP_TRANSPORT", raising=False)
    else:
        monkeypatch.setenv("WHATSAPP_MCP_TRANSPORT", env_value)

    def fake_run(*, transport):
        calls.append(transport)

    def fake_remote_run(transport, auth_enabled, token):
        calls.append(transport)
        assert auth_enabled is True
        assert token == VALID_TOKEN

    monkeypatch.setattr(main.mcp, "run", fake_run)
    monkeypatch.setattr(main, "run_remote_mcp_app", fake_remote_run)
    if expected_transport != "stdio":
        monkeypatch.setenv("WHATSAPP_MCP_TOKEN", VALID_TOKEN)

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
    monkeypatch.setenv("WHATSAPP_MCP_TOKEN", VALID_TOKEN)

    def fake_remote_run(transport, auth_enabled, token):
        calls.append((transport, auth_enabled, token))

    monkeypatch.setattr(main, "run_remote_mcp_app", fake_remote_run)

    main.run_mcp_server()

    assert calls == [("streamable-http", True, VALID_TOKEN)]
    assert main.mcp.settings.host == "0.0.0.0"
    assert main.mcp.settings.port == 9090


def run_server_subprocess(env_updates: dict[str, str | None]) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    for key, value in env_updates.items():
        if value is None:
            env.pop(key, None)
        else:
            env[key] = value

    return subprocess.run(
        [sys.executable, "-c", RUN_SERVER_WITH_FAKE_RUN],
        cwd=SERVER_DIR,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )


@pytest.mark.parametrize(
    ("env_updates", "expected_returncode", "stderr_parts"),
    [
        (
            {
                "WHATSAPP_MCP_TRANSPORT": "stdio",
                "WHATSAPP_MCP_PORT": "not-a-number",
                "WHATSAPP_MCP_HOST": "0.0.0.0",
                "WHATSAPP_MCP_AUTH": "off",
                "WHATSAPP_MCP_TOKEN": "test",
            },
            0,
            [],
        ),
        (
            {"WHATSAPP_MCP_TRANSPORT": "http", "WHATSAPP_MCP_HOST": "127.0.0.1", "WHATSAPP_MCP_AUTH": "off"},
            0,
            [],
        ),
        (
            {"WHATSAPP_MCP_TRANSPORT": "http", "WHATSAPP_MCP_HOST": "0.0.0.0", "WHATSAPP_MCP_AUTH": "off"},
            1,
            ["WHATSAPP_MCP_AUTH=off", "WHATSAPP_MCP_HOST"],
        ),
        (
            {"WHATSAPP_MCP_TRANSPORT": "http", "WHATSAPP_MCP_AUTH": "on", "WHATSAPP_MCP_TOKEN": None},
            1,
            ["WHATSAPP_MCP_TOKEN"],
        ),
        (
            {"WHATSAPP_MCP_TRANSPORT": "http", "WHATSAPP_MCP_AUTH": "on", "WHATSAPP_MCP_TOKEN": "short"},
            1,
            ["WHATSAPP_MCP_TOKEN"],
        ),
        (
            {"WHATSAPP_MCP_TRANSPORT": "http", "WHATSAPP_MCP_AUTH": "on", "WHATSAPP_MCP_TOKEN": "changeme" * 5},
            1,
            ["WHATSAPP_MCP_TOKEN"],
        ),
        (
            {
                "WHATSAPP_MCP_TRANSPORT": "sse",
                "WHATSAPP_MCP_HOST": "0.0.0.0",
                "WHATSAPP_MCP_AUTH": "on",
                "WHATSAPP_MCP_TOKEN": VALID_TOKEN,
            },
            0,
            [],
        ),
    ],
)
def test_remote_auth_startup_guard_exits_or_runs(env_updates, expected_returncode, stderr_parts):
    result = run_server_subprocess(
        {
            "WHATSAPP_MCP_TOKEN": None,
            **env_updates,
        }
    )

    assert (result.returncode == 0) is (expected_returncode == 0)
    for part in stderr_parts:
        assert part in result.stderr


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
