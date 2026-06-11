import os
import types

import pytest
import requests

import bridge
import whatsapp


@pytest.fixture(autouse=True)
def bridge_env(monkeypatch, tmp_path):
    """Isolate every test from the real environment and repo layout."""
    for var in (
        "WHATSAPP_BRIDGE_AUTOSTART",
        "WHATSAPP_BRIDGE_BINARY",
        "WHATSAPP_BRIDGE_DIR",
        "WHATSAPP_BRIDGE_PORT",
        "WHATSAPP_BRIDGE_STARTUP_TIMEOUT",
        "WHATSAPP_API_URL",
    ):
        monkeypatch.delenv(var, raising=False)
    bridge_dir = tmp_path / "whatsapp-bridge"
    bridge_dir.mkdir()
    monkeypatch.setenv("WHATSAPP_BRIDGE_DIR", str(bridge_dir))
    return bridge_dir


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload or {"success": True, "message": "sent"}
        self.text = text

    def json(self):
        return self._payload


class FakeProc:
    def __init__(self, pid=4242, exit_code=None):
        self.pid = pid
        self._exit_code = exit_code

    def poll(self):
        return self._exit_code


def forbid_spawn(*args, **kwargs):
    raise AssertionError("subprocess.Popen must not be called")


def listening_after(monkeypatch, failures):
    """Make bridge's health check fail `failures` times, then succeed."""
    calls = {"n": 0}

    def fake_get(url, timeout=None):
        calls["n"] += 1
        if calls["n"] <= failures:
            raise requests.ConnectionError("connection refused")
        return DummyResponse()

    monkeypatch.setattr(bridge.requests, "get", fake_get)
    return calls


def never_listening(monkeypatch):
    def fake_get(url, timeout=None):
        raise requests.ConnectionError("connection refused")

    monkeypatch.setattr(bridge.requests, "get", fake_get)


def make_fake_binary(bridge_dir, name="whatsapp-bridge"):
    binary = bridge_dir / name
    binary.write_bytes(b"#!/bin/sh\n")
    binary.chmod(0o755)
    return binary


def test_no_spawn_when_bridge_already_listening(monkeypatch):
    monkeypatch.setattr(bridge.requests, "get", lambda url, timeout=None: DummyResponse(200))
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is True
    assert "already running" in detail


def test_unauthorized_health_response_still_counts_as_listening(monkeypatch):
    """A 401 proves the bridge process is up; autostart must not double-start it."""
    monkeypatch.setattr(bridge.requests, "get", lambda url, timeout=None: DummyResponse(401))
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, _ = bridge.ensure_bridge_running()

    assert ok is True


def test_autostart_disabled_returns_actionable_error(monkeypatch):
    monkeypatch.setenv("WHATSAPP_BRIDGE_AUTOSTART", "false")
    never_listening(monkeypatch)
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "WHATSAPP_BRIDGE_AUTOSTART" in detail


def test_non_loopback_api_url_is_never_autostarted(monkeypatch):
    monkeypatch.setenv("WHATSAPP_API_URL", "http://192.168.7.20:8080/api")
    never_listening(monkeypatch)
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "loopback" in detail


def test_spawns_binary_detached_in_bridge_dir(monkeypatch, bridge_env):
    binary = make_fake_binary(bridge_env)
    listening_after(monkeypatch, failures=2)
    spawns = []

    def fake_popen(args, **kwargs):
        spawns.append((args, kwargs))
        return FakeProc()

    monkeypatch.setattr(bridge.subprocess, "Popen", fake_popen)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is True
    assert "pid 4242" in detail
    args, kwargs = spawns[0]
    assert args == [str(binary)]
    # cwd must be the bridge dir: the bridge resolves store/ relative to it.
    assert kwargs["cwd"] == str(bridge_env)
    # Spawned bridge binds the port derived from WHATSAPP_API_URL.
    assert kwargs["env"]["WHATSAPP_BRIDGE_PORT"] == "8080"
    if os.name == "posix":
        assert kwargs["start_new_session"] is True


def test_custom_api_port_is_passed_to_spawned_bridge(monkeypatch, bridge_env):
    monkeypatch.setenv("WHATSAPP_API_URL", "http://127.0.0.1:9111/api")
    make_fake_binary(bridge_env)
    listening_after(monkeypatch, failures=2)
    spawns = []

    def fake_popen(args, **kwargs):
        spawns.append(kwargs)
        return FakeProc()

    monkeypatch.setattr(bridge.subprocess, "Popen", fake_popen)

    ok, _ = bridge.ensure_bridge_running()

    assert ok is True
    assert spawns[0]["env"]["WHATSAPP_BRIDGE_PORT"] == "9111"


def test_missing_binary_without_go_explains_how_to_build(monkeypatch):
    never_listening(monkeypatch)
    monkeypatch.setattr(bridge.shutil, "which", lambda name: None)
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "go build" in detail
    assert "WHATSAPP_BRIDGE_BINARY" in detail


def test_invalid_binary_override_is_reported_not_ignored(monkeypatch):
    monkeypatch.setenv("WHATSAPP_BRIDGE_BINARY", "/nonexistent/whatsapp-bridge")
    never_listening(monkeypatch)
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "WHATSAPP_BRIDGE_BINARY" in detail


def test_builds_binary_with_go_when_missing(monkeypatch, bridge_env):
    monkeypatch.setattr(bridge.shutil, "which", lambda name: "/usr/local/bin/go")
    builds = []

    def fake_run(args, **kwargs):
        builds.append((args, kwargs))
        make_fake_binary(bridge_env)
        return types.SimpleNamespace(returncode=0)

    monkeypatch.setattr(bridge.subprocess, "run", fake_run)
    monkeypatch.setattr(bridge.subprocess, "Popen", lambda args, **kwargs: FakeProc())
    listening_after(monkeypatch, failures=2)

    ok, _ = bridge.ensure_bridge_running()

    assert ok is True
    args, kwargs = builds[0]
    assert args[:3] == ["/usr/local/bin/go", "build", "-o"]
    assert kwargs["cwd"] == str(bridge_env)


def test_failed_go_build_points_at_log(monkeypatch):
    never_listening(monkeypatch)
    monkeypatch.setattr(bridge.shutil, "which", lambda name: "/usr/local/bin/go")
    monkeypatch.setattr(bridge.subprocess, "run", lambda args, **kwargs: types.SimpleNamespace(returncode=2))
    monkeypatch.setattr(bridge.subprocess, "Popen", forbid_spawn)

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "go build" in detail
    assert "bridge.log" in detail


def test_bridge_exiting_during_startup_is_reported(monkeypatch, bridge_env):
    make_fake_binary(bridge_env)
    never_listening(monkeypatch)
    monkeypatch.setattr(bridge.subprocess, "Popen", lambda args, **kwargs: FakeProc(exit_code=3))

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "exited with code 3" in detail
    assert "bridge.log" in detail


def test_startup_timeout_mentions_qr_pairing(monkeypatch, bridge_env):
    make_fake_binary(bridge_env)
    never_listening(monkeypatch)
    monkeypatch.setenv("WHATSAPP_BRIDGE_STARTUP_TIMEOUT", "1")
    monkeypatch.setattr(bridge.time, "sleep", lambda seconds: None)
    monkeypatch.setattr(bridge.subprocess, "Popen", lambda args, **kwargs: FakeProc())

    ok, detail = bridge.ensure_bridge_running()

    assert ok is False
    assert "QR" in detail


def test_send_message_starts_bridge_and_retries(monkeypatch):
    calls = []

    def fake_post(url, json, headers=None):
        calls.append(url)
        if len(calls) == 1:
            raise requests.ConnectionError("connection refused")
        return DummyResponse()

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)
    ensures = []
    monkeypatch.setattr(
        whatsapp.bridge,
        "ensure_bridge_running",
        lambda: ensures.append(1) or (True, "bridge started"),
    )

    success, _ = whatsapp.send_message("12025551234", "hello")

    assert success is True
    assert len(calls) == 2
    assert ensures == [1]


def test_send_message_surfaces_autostart_failure_reason(monkeypatch):
    calls = []

    def fake_post(url, json, headers=None):
        calls.append(url)
        raise requests.ConnectionError("connection refused")

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)
    monkeypatch.setattr(
        whatsapp.bridge,
        "ensure_bridge_running",
        lambda: (False, "bridge binary not found at /x"),
    )

    success, message = whatsapp.send_message("12025551234", "hello")

    assert success is False
    assert "bridge binary not found at /x" in message
    assert len(calls) == 1


def test_download_media_retries_after_autostart(monkeypatch):
    calls = []

    def fake_post(url, json, headers=None):
        calls.append(url)
        if len(calls) == 1:
            raise requests.ConnectionError("connection refused")
        return DummyResponse(payload={"success": True, "path": "/tmp/media.jpg"})

    monkeypatch.setattr(whatsapp.requests, "post", fake_post)
    monkeypatch.setattr(whatsapp.bridge, "ensure_bridge_running", lambda: (True, "bridge started"))

    path = whatsapp.download_media("msg-id", "12025551234@s.whatsapp.net")

    assert path == "/tmp/media.jpg"
    assert len(calls) == 2


def test_start_background_autostart_logs_outcome_to_stderr(monkeypatch, capsys):
    monkeypatch.setattr(bridge, "ensure_bridge_running", lambda: (True, "bridge already running"))

    thread = bridge.start_background_autostart()
    thread.join(timeout=5)

    assert not thread.is_alive()
    assert "bridge already running" in capsys.readouterr().err
