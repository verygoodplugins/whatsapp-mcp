"""Auto-start management for the Go WhatsApp bridge.

The MCP server reads message history straight from SQLite, but sends
(messages, reactions, media) and downloads go through the bridge's REST
API. Historically the bridge had to be started by hand before any of
those tools worked; if it wasn't, every call surfaced a connection error
in the MCP client. ensure_bridge_running() closes that gap: when the
bridge is unreachable on a loopback address it launches the bridge
binary as a detached background process (building it once with
`go build` when missing) and waits for the REST API to come up.

Two constraints from the bridge itself shape this module:

- The bridge resolves store/ (databases, token, media) relative to its
  working directory, so the spawn must set cwd to the bridge directory.
- The REST port only opens after WhatsApp authentication succeeds, so
  "listening" implies "connected". First-time QR pairing cannot happen
  headlessly (the QR code would only land in the log file); that first
  run still needs a terminal.
"""

import os
import shutil
import subprocess
import sys
import threading
import time
from typing import TextIO
from urllib.parse import urlsplit

import requests

try:  # POSIX-only; used to serialize spawns across MCP server processes
    import fcntl
except ImportError:  # pragma: no cover - Windows
    fcntl = None

_DEFAULT_BRIDGE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "whatsapp-bridge")
_BINARY_NAME = "whatsapp-bridge.exe" if os.name == "nt" else "whatsapp-bridge"
_LOCAL_HOSTNAMES = {"localhost", "127.0.0.1", "::1"}
_GO_BUILD_TIMEOUT_SECONDS = 300

# Serializes ensure_bridge_running() across threads in this process; the
# flock on store/.bridge-autostart.lock serializes it across processes
# (e.g. Claude Desktop and Cursor launching their MCP servers at the same
# moment). Two concurrent bridges would fight over the one WhatsApp
# session (StreamReplaced loops), so both layers matter.
_thread_lock = threading.Lock()


def _api_base_url() -> str:
    return os.getenv("WHATSAPP_API_URL", "http://localhost:8080/api").rstrip("/")


def _autostart_enabled() -> bool:
    return os.getenv("WHATSAPP_BRIDGE_AUTOSTART", "true").strip().lower() in ("1", "true", "yes", "on")


def _startup_timeout() -> float:
    raw = os.getenv("WHATSAPP_BRIDGE_STARTUP_TIMEOUT", "60")
    try:
        return max(1.0, float(raw))
    except ValueError:
        return 60.0


def _bridge_dir() -> str:
    return os.path.abspath(os.getenv("WHATSAPP_BRIDGE_DIR") or _DEFAULT_BRIDGE_DIR)


def _store_dir() -> str:
    return os.path.join(_bridge_dir(), "store")


def _log_path() -> str:
    return os.path.join(_store_dir(), "bridge.log")


def _api_host_port() -> tuple[str | None, int]:
    parts = urlsplit(_api_base_url())
    port = parts.port or (443 if parts.scheme == "https" else 80)
    return parts.hostname, port


def _is_listening() -> bool:
    """True if anything answers HTTP at the bridge API URL.

    Auth is deliberately ignored: a 401/403/503 still proves the bridge
    process is up and serving, which is all autostart needs to know.
    """
    try:
        requests.get(f"{_api_base_url()}/health", timeout=2)
        return True
    except requests.RequestException:
        return False


def _find_or_build_binary(log_fh: TextIO) -> tuple[str | None, str]:
    """Locate the bridge binary, building it with `go build` when missing.

    Returns (path, "") on success or (None, reason) on failure.
    """
    override = os.getenv("WHATSAPP_BRIDGE_BINARY", "").strip()
    if override:
        if os.path.isfile(override) and os.access(override, os.X_OK):
            return override, ""
        return None, f"WHATSAPP_BRIDGE_BINARY={override} is not an executable file"

    binary = os.path.join(_bridge_dir(), _BINARY_NAME)
    if os.path.isfile(binary) and os.access(binary, os.X_OK):
        return binary, ""

    go = shutil.which("go")
    if not go:
        return None, (
            f"bridge binary not found at {binary} and no Go toolchain on PATH; "
            f"build it once with `cd whatsapp-bridge && go build -o {_BINARY_NAME} .` "
            "or point WHATSAPP_BRIDGE_BINARY at an existing build"
        )

    log_fh.write(
        f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] autostart: {go} build -o {_BINARY_NAME} . (cwd={_bridge_dir()})\n"
    )
    log_fh.flush()
    try:
        result = subprocess.run(
            [go, "build", "-o", _BINARY_NAME, "."],
            cwd=_bridge_dir(),
            stdout=log_fh,
            stderr=subprocess.STDOUT,
            timeout=_GO_BUILD_TIMEOUT_SECONDS,
            check=False,
        )
    except subprocess.TimeoutExpired:
        return None, f"`go build` timed out after {_GO_BUILD_TIMEOUT_SECONDS}s; see {_log_path()}"
    if result.returncode != 0:
        return None, f"`go build` failed with exit code {result.returncode}; see {_log_path()}"
    if not (os.path.isfile(binary) and os.access(binary, os.X_OK)):
        return None, f"`go build` reported success but {binary} is missing"
    return binary, ""


def _spawn_bridge(binary: str, log_fh: TextIO) -> subprocess.Popen:
    env = os.environ.copy()
    _, port = _api_host_port()
    # Make the spawned bridge bind the port the MCP server will call. An
    # explicit WHATSAPP_BRIDGE_PORT in the environment still wins.
    env.setdefault("WHATSAPP_BRIDGE_PORT", str(port))

    detach: dict = {}
    if os.name == "posix":
        # New session so the bridge outlives the MCP server (Claude Desktop
        # kills the MCP process group when it quits or reloads).
        detach["start_new_session"] = True
    elif os.name == "nt":  # pragma: no cover - Windows
        detach["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.DETACHED_PROCESS

    log_fh.write(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] autostart: launching {binary}\n")
    log_fh.flush()
    return subprocess.Popen(
        [binary],
        cwd=_bridge_dir(),
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=log_fh,
        stderr=subprocess.STDOUT,
        **detach,
    )


def _wait_until_listening(proc: subprocess.Popen, deadline: float) -> tuple[bool, str]:
    while True:
        if _is_listening():
            return True, f"bridge started (pid {proc.pid}, log: {_log_path()})"
        code = proc.poll()
        if code is not None:
            return False, f"bridge exited with code {code} during startup; see {_log_path()}"
        if time.monotonic() >= deadline:
            # Leave the process running: it may still be syncing, or waiting
            # for a first-time QR scan that only a terminal run can show.
            return False, (
                f"bridge started (pid {proc.pid}) but {_api_base_url()}/health is not answering yet; "
                f"if this is the first run it is waiting for QR pairing — run the bridge in a terminal "
                f"to scan the code, or check {_log_path()}"
            )
        time.sleep(0.5)


def _try_lock(fh: TextIO) -> bool:
    if fcntl is None:  # pragma: no cover - Windows falls back to the thread lock only
        return True
    try:
        fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        return True
    except OSError:
        return False


def ensure_bridge_running() -> tuple[bool, str]:
    """Make sure the bridge REST API is reachable, starting the bridge if needed.

    Returns (ok, detail). ok=True means the API is answering (possibly because
    we just started the bridge); ok=False explains why it is unreachable and
    what to do about it.
    """
    if _is_listening():
        return True, "bridge already running"

    if not _autostart_enabled():
        return False, (
            f"bridge is not reachable at {_api_base_url()} and WHATSAPP_BRIDGE_AUTOSTART "
            "is disabled — start whatsapp-bridge manually"
        )

    host, _ = _api_host_port()
    if host not in _LOCAL_HOSTNAMES:
        return False, (
            f"bridge is not reachable at {_api_base_url()}, which is not a loopback address — "
            "autostart only manages a local bridge; start it on the remote host"
        )

    deadline = time.monotonic() + _startup_timeout()
    with _thread_lock:
        os.makedirs(_store_dir(), exist_ok=True)
        lock_fh = open(os.path.join(_store_dir(), ".bridge-autostart.lock"), "a")
        try:
            while not _try_lock(lock_fh):
                # Another MCP server process is starting the bridge right now;
                # wait for it instead of racing it.
                if _is_listening():
                    return True, "bridge already running (started by another process)"
                if time.monotonic() >= deadline:
                    return False, (
                        f"another process is starting the bridge but it has not come up "
                        f"within {int(_startup_timeout())}s; see {_log_path()}"
                    )
                time.sleep(0.5)

            if _is_listening():  # raced: it came up while we waited for the lock
                return True, "bridge already running"

            with open(_log_path(), "a", encoding="utf-8") as log_fh:
                binary, reason = _find_or_build_binary(log_fh)
                if binary is None:
                    return False, reason
                proc = _spawn_bridge(binary, log_fh)
            return _wait_until_listening(proc, deadline)
        finally:
            lock_fh.close()


def start_background_autostart() -> threading.Thread:
    """Run ensure_bridge_running() on a daemon thread (MCP server startup path).

    Detached so a slow bridge start — or a one-time `go build` — never delays
    the MCP stdio handshake. The outcome goes to stderr because stdout carries
    the MCP protocol.
    """

    def run() -> None:
        try:
            _, detail = ensure_bridge_running()
            print(f"whatsapp-bridge autostart: {detail}", file=sys.stderr)
        except Exception as exc:  # never take down the MCP server from here
            print(f"whatsapp-bridge autostart error: {exc}", file=sys.stderr)

    thread = threading.Thread(target=run, name="whatsapp-bridge-autostart", daemon=True)
    thread.start()
    return thread
