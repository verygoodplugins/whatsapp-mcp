"""Parent-liveness watchdog for stdio MCP servers.

When an intermediate wrapper keeps stdin open after the client dies, the
Python MCP stdio loop never sees EOF and the process leaks forever (swap
thrash). This watchdog exits once os.getppid() changes from the original
parent (POSIX reparenting).

Soft stdin EOF is intentional: do NOT hard-exit on EOF alone. One-shot
pipe clients must be able to finish in-flight handlers. Hard exit is
reserved for parent-watchdog reparent (and OS signals installed by the
host process).
"""

from __future__ import annotations

import os
import threading
import time
from collections.abc import Callable

DEFAULT_PARENT_WATCHDOG_S = 30.0
MIN_PARENT_WATCHDOG_S = 0.1


def parse_watchdog_interval_s(raw: str | None) -> float:
    if raw is None or raw.strip() == "":
        return DEFAULT_PARENT_WATCHDOG_S
    try:
        value = float(raw)
    except ValueError:
        return DEFAULT_PARENT_WATCHDOG_S
    if value <= 0:
        return DEFAULT_PARENT_WATCHDOG_S
    return max(value, MIN_PARENT_WATCHDOG_S)


def start_parent_watchdog(
    parent_pid: int,
    interval_s: float,
    on_dead: Callable[[], None],
    *,
    is_parent_gone: Callable[[int], bool] | None = None,
) -> threading.Thread:
    probe = is_parent_gone or (lambda pid: os.getppid() != pid)
    fired = {"value": False}

    def _loop() -> None:
        while not fired["value"]:
            if probe(parent_pid):
                fired["value"] = True
                on_dead()
                return
            time.sleep(interval_s)

    thread = threading.Thread(target=_loop, name="mcp-parent-watchdog", daemon=True)
    thread.start()
    return thread


def install_stdio_parent_watchdog(
    env_name: str,
    *,
    parent_pid: int | None = None,
) -> None:
    # Prefer a pid captured before any await in main — getppid() is dynamic.
    pinned = os.getppid() if parent_pid is None else parent_pid
    interval = parse_watchdog_interval_s(os.environ.get(env_name))

    def _exit() -> None:
        os._exit(0)

    start_parent_watchdog(pinned, interval, _exit)
