import signal

import pytest

import main as mcp_main


class _ExitCalledError(Exception):
    def __init__(self, code: int) -> None:
        self.code = code


@pytest.fixture
def fake_os_exit(monkeypatch):
    def _fake(code: int) -> None:
        raise _ExitCalledError(code)

    monkeypatch.setattr(mcp_main.os, "_exit", _fake)


@pytest.mark.parametrize("signum", [signal.SIGTERM, signal.SIGINT])
def test_shutdown_handler_skips_interpreter_finalization(fake_os_exit, signum):
    # sys.exit would run finalization, which aborts on Python 3.11 when a
    # stdio worker thread still holds the stdin/stdout buffer lock.
    with pytest.raises(_ExitCalledError) as exc:
        mcp_main.shutdown_handler(signum, None)
    assert exc.value.code == 0


def test_exit_now_survives_broken_stderr(fake_os_exit, monkeypatch):
    class _BrokenStream:
        def flush(self) -> None:
            raise BrokenPipeError

    monkeypatch.setattr(mcp_main.sys, "stderr", _BrokenStream())
    with pytest.raises(_ExitCalledError) as exc:
        mcp_main._exit_now(3)
    assert exc.value.code == 3
