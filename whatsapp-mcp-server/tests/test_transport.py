"""Tests for MCP transport selection."""

import pytest

from mcp_config import resolve_host, resolve_port, resolve_transport


class TestResolveTransport:
    """Tests for resolve_transport()."""

    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            (None, "stdio"),
            ("", "stdio"),
            ("   ", "stdio"),
            ("\t\n", "stdio"),
            ("  STDIO ", "stdio"),
            ("http", "streamable-http"),
            ("Http", "streamable-http"),
            ("streamable-http", "streamable-http"),
            ("streamable_http", "streamable-http"),
            ("sse", "sse"),
        ],
    )
    def test_valid_values(self, value, expected):
        assert resolve_transport(value) == expected

    def test_invalid_value_raises(self):
        with pytest.raises(ValueError, match="Invalid WHATSAPP_MCP_TRANSPORT"):
            resolve_transport("websocket")


class TestResolveHost:
    """Tests for resolve_host()."""

    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            (None, "127.0.0.1"),
            ("", "127.0.0.1"),
            ("   ", "127.0.0.1"),
            ("\t\n", "127.0.0.1"),
            (" 127.0.0.1 ", "127.0.0.1"),
            ("0.0.0.0", "0.0.0.0"),
        ],
    )
    def test_values(self, value, expected):
        assert resolve_host(value) == expected


class TestResolvePort:
    """Tests for resolve_port()."""

    @pytest.mark.parametrize(
        ("value", "expected"),
        [
            (None, 8000),
            ("", 8000),
            ("   ", 8000),
            ("\t\n", 8000),
            ("9000", 9000),
            (" 9000 ", 9000),
            ("1", 1),
            ("65535", 65535),
        ],
    )
    def test_valid_values(self, value, expected):
        assert resolve_port(value) == expected

    def test_non_integer_raises(self):
        with pytest.raises(ValueError, match="Invalid WHATSAPP_MCP_PORT"):
            resolve_port("not-a-number")

    def test_out_of_range_raises(self):
        for value in ("0", "-1", "65536"):
            with pytest.raises(ValueError, match="Invalid WHATSAPP_MCP_PORT"):
                resolve_port(value)
