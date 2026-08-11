"""Regression tests for the signal shutdown handler in main.py.

Guards against the interpreter-shutdown SIGABRT: on SIGINT/SIGTERM the handler
must terminate via os._exit() (which skips interpreter finalization) rather than
sys.exit(). sys.exit() raises SystemExit and runs full finalization; if a daemon
thread spawned by the MCP/anyio stdio transport is mid-write to stdout/stderr,
CPython aborts with "_enter_buffered_busy: could not acquire lock for
<_io.BufferedWriter> at interpreter shutdown, possibly due to daemon threads".
"""

import signal

import main as mcp_main


class _FakeStream:
    def __init__(self):
        self.flush_calls = 0

    def flush(self):
        self.flush_calls += 1


def test_shutdown_handler_uses_os_exit_and_flushes(monkeypatch):
    fake_out = _FakeStream()
    fake_err = _FakeStream()
    monkeypatch.setattr(mcp_main.sys, "stdout", fake_out)
    monkeypatch.setattr(mcp_main.sys, "stderr", fake_err)

    exit_calls = []
    monkeypatch.setattr(mcp_main.os, "_exit", lambda code: exit_calls.append(code))

    # sys.exit() is exactly the bug — raising SystemExit runs finalization.
    # Make it fail loudly so a regression trips this test instead of exiting.
    def _forbidden(*_args, **_kwargs):
        raise AssertionError("shutdown_handler must not call sys.exit()")

    monkeypatch.setattr(mcp_main.sys, "exit", _forbidden)

    mcp_main.shutdown_handler(signal.SIGTERM, None)

    assert exit_calls == [0]
    assert fake_out.flush_calls == 1
    assert fake_err.flush_calls == 1


def test_shutdown_handler_survives_flush_errors(monkeypatch):
    class _BrokenStream:
        def flush(self):
            raise ValueError("stream already closed")

    monkeypatch.setattr(mcp_main.sys, "stdout", _BrokenStream())
    monkeypatch.setattr(mcp_main.sys, "stderr", _BrokenStream())

    exit_calls = []
    monkeypatch.setattr(mcp_main.os, "_exit", lambda code: exit_calls.append(code))

    # A flush that raises must not prevent the process from exiting.
    mcp_main.shutdown_handler(signal.SIGINT, None)

    assert exit_calls == [0]
