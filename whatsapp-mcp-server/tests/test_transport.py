"""Tests for MCP transport selection."""

import pytest

from mcp_config import (
    build_transport_security,
    resolve_allowed_hosts,
    resolve_host,
    resolve_port,
    resolve_transport,
)


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


class TestResolveAllowedHosts:
    """Tests for resolve_allowed_hosts()."""

    @pytest.mark.parametrize("value", [None, "", "   ", ", ,"])
    def test_unset_returns_none(self, value):
        assert resolve_allowed_hosts(value) is None

    def test_star_disables_protection(self):
        assert resolve_allowed_hosts("*") == []
        assert resolve_allowed_hosts("mcp.example.ts.net, *") == []

    def test_bare_hostname_expands_to_exact_and_wildcard_port(self):
        hosts = set(resolve_allowed_hosts("mcp.example.ts.net"))
        # Set comparisons rather than `"host" in list` membership: CodeQL reads the
        # latter as URL-substring sanitisation and flags the tests.
        assert {"mcp.example.ts.net", "mcp.example.ts.net:*"} <= hosts
        # Loopback spellings are always kept so local clients keep working.
        assert {"127.0.0.1:*", "localhost:*", "[::1]:*"} <= hosts

    def test_explicit_port_is_kept_verbatim(self):
        hosts = set(resolve_allowed_hosts("whatsapp-mcp:8000, 10.0.0.5:*"))
        assert {"whatsapp-mcp:8000", "10.0.0.5:*"} <= hosts
        assert hosts.isdisjoint({"whatsapp-mcp:*"})


class TestBuildTransportSecurity:
    """Tests for build_transport_security()."""

    def test_loopback_without_allowlist_keeps_sdk_default(self):
        assert build_transport_security("127.0.0.1", None) is None

    def test_non_loopback_without_allowlist_disables_protection(self):
        security = build_transport_security("0.0.0.0", None)
        assert security is not None
        assert security.enable_dns_rebinding_protection is False

    def test_star_disables_protection(self):
        security = build_transport_security("127.0.0.1", "*")
        assert security.enable_dns_rebinding_protection is False

    def test_allowlist_enables_protection_with_origins(self):
        security = build_transport_security("0.0.0.0", "mcp.example.ts.net", "https://app.example.com")
        assert security.enable_dns_rebinding_protection is True
        assert {"mcp.example.ts.net", "mcp.example.ts.net:*"} <= set(security.allowed_hosts)
        expected_origins = {"https://mcp.example.ts.net", "http://localhost:*", "https://app.example.com"}
        assert expected_origins <= set(security.allowed_origins)


def _streamable_http_app(host: str, allowed_hosts: str | None):
    """Build a FastMCP streamable-http app the way main.py configures it at runtime."""
    from mcp.server.fastmcp import FastMCP

    server = FastMCP("test")  # constructed with the default loopback host, like main.py
    server.settings.host = host
    security = build_transport_security(host, allowed_hosts)
    if security is not None:
        server.settings.transport_security = security
    return server.streamable_http_app()


class TestStreamableHttpHostHeader:
    """Regression tests for 421 Misdirected Request on non-loopback Host headers."""

    FUNNEL_HOST = "mcp.example.ts.net"

    def _get(self, app, host_header: str) -> int:
        from starlette.testclient import TestClient

        with TestClient(app) as client:
            response = client.get(
                "/mcp",
                headers={"Host": host_header, "Accept": "application/json, text/event-stream"},
            )
        return response.status_code

    def test_sdk_default_rejects_non_loopback_host(self):
        # Documents the failure mode being fixed: loopback bind + loopback-only policy.
        assert self._get(_streamable_http_app("127.0.0.1", None), self.FUNNEL_HOST) == 421

    def test_allowlisted_host_is_accepted(self):
        status = self._get(_streamable_http_app("0.0.0.0", self.FUNNEL_HOST), self.FUNNEL_HOST)
        assert status != 421

    def test_allowlisted_host_with_port_is_accepted(self):
        status = self._get(_streamable_http_app("0.0.0.0", self.FUNNEL_HOST), f"{self.FUNNEL_HOST}:8000")
        assert status != 421

    def test_loopback_still_accepted_with_allowlist(self):
        status = self._get(_streamable_http_app("0.0.0.0", self.FUNNEL_HOST), "localhost:8000")
        assert status != 421

    def test_unlisted_host_still_rejected(self):
        status = self._get(_streamable_http_app("0.0.0.0", self.FUNNEL_HOST), "evil.example.com")
        assert status == 421

    def test_non_loopback_bind_without_allowlist_accepts_any_host(self):
        status = self._get(_streamable_http_app("0.0.0.0", None), "whatsapp-mcp:8000")
        assert status != 421
