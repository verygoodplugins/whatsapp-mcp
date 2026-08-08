import time

from parent_watchdog import (
    DEFAULT_PARENT_WATCHDOG_S,
    parse_watchdog_interval_s,
    start_parent_watchdog,
)


def test_parse_defaults():
    assert parse_watchdog_interval_s(None) == DEFAULT_PARENT_WATCHDOG_S
    assert parse_watchdog_interval_s("") == DEFAULT_PARENT_WATCHDOG_S
    assert parse_watchdog_interval_s("nope") == DEFAULT_PARENT_WATCHDOG_S
    assert parse_watchdog_interval_s("0") == DEFAULT_PARENT_WATCHDOG_S


def test_parse_positive_floor():
    assert parse_watchdog_interval_s("2.5") == 2.5
    assert parse_watchdog_interval_s("0.01") == 0.1


def test_watchdog_fires_once():
    hits = []
    start_parent_watchdog(1, 0.05, lambda: hits.append(1), is_parent_gone=lambda _pid: True)
    time.sleep(0.2)
    assert hits == [1]
