"""Side-effect-free helpers for MCP server configuration env vars."""

# Accepted WHATSAPP_MCP_TRANSPORT values mapped to FastMCP transport names.
# "http" is a friendly alias for the spec's current "streamable-http" transport.
TRANSPORT_ALIASES = {
    "stdio": "stdio",
    "http": "streamable-http",
    "streamable-http": "streamable-http",
    "streamable_http": "streamable-http",
    "sse": "sse",
}
DEFAULT_MCP_HOST = "127.0.0.1"
DEFAULT_MCP_PORT = 8000


def resolve_transport(value: str | None) -> str:
    """Map a WHATSAPP_MCP_TRANSPORT value to a FastMCP transport name.

    Unset or whitespace-only values default to "stdio".
    Raises ValueError for unrecognized values.
    """
    normalized = (value or "").strip().lower() or "stdio"
    try:
        return TRANSPORT_ALIASES[normalized]
    except KeyError:
        accepted = ", ".join(sorted(TRANSPORT_ALIASES))
        raise ValueError(
            f"Invalid WHATSAPP_MCP_TRANSPORT={value!r}; recommended values: stdio, http, sse "
            f"(http maps to the spec's streamable-http transport; all accepted inputs: {accepted})"
        ) from None


def resolve_host(value: str | None) -> str:
    """Parse WHATSAPP_MCP_HOST, defaulting to DEFAULT_MCP_HOST."""
    return (value or "").strip() or DEFAULT_MCP_HOST


def resolve_port(value: str | None) -> int:
    """Parse WHATSAPP_MCP_PORT, defaulting to DEFAULT_MCP_PORT.

    Unset or whitespace-only values default to DEFAULT_MCP_PORT.
    Raises ValueError for non-integer or out-of-range values.
    """
    value = (value or "").strip()
    if not value:
        return DEFAULT_MCP_PORT
    try:
        port = int(value)
    except ValueError:
        raise ValueError(f"Invalid WHATSAPP_MCP_PORT={value!r}; must be an integer") from None
    if not 1 <= port <= 65535:
        raise ValueError(f"Invalid WHATSAPP_MCP_PORT={value!r}; must be between 1 and 65535") from None
    return port


# Host header values the MCP SDK accepts by default when bound to loopback.
# Mirrors FastMCP's own construction-time allow-list so that enabling extra
# hosts never locks out local clients.
LOOPBACK_HOSTS = ("127.0.0.1", "localhost", "[::1]")
LOOPBACK_BIND_ADDRESSES = ("127.0.0.1", "localhost", "::1")
# Disables DNS-rebinding protection entirely (allow any Host header).
ALLOW_ANY_HOST = "*"


def _split_csv(value: str | None) -> list[str]:
    return [item.strip() for item in (value or "").split(",") if item.strip()]


def _expand_host_patterns(hosts: list[str]) -> list[str]:
    """Return SDK-style Host patterns for the given entries.

    The SDK matches either an exact ``host[:port]`` string or a ``host:*``
    wildcard. A bare hostname is expanded to both forms so that
    ``WHATSAPP_MCP_ALLOWED_HOSTS=mcp.example.ts.net`` matches requests that
    arrive with or without an explicit port (a TLS terminator such as
    Tailscale Serve/Funnel forwards ``Host: mcp.example.ts.net``, while a
    direct call carries ``Host: mcp.example.ts.net:8000``).
    """
    expanded: list[str] = []
    for host in hosts:
        candidates = [host]
        if not host.endswith(":*") and not _has_explicit_port(host):
            candidates.append(f"{host}:*")
        for candidate in candidates:
            if candidate not in expanded:
                expanded.append(candidate)
    return expanded


def _has_explicit_port(host: str) -> bool:
    if host.startswith("["):
        return "]:" in host
    return host.count(":") == 1


def _origins_for_hosts(hosts: list[str]) -> list[str]:
    origins: list[str] = []
    for host in hosts:
        for scheme in ("http", "https"):
            origin = f"{scheme}://{host}"
            if origin not in origins:
                origins.append(origin)
    return origins


def resolve_allowed_hosts(value: str | None) -> list[str] | None:
    """Parse WHATSAPP_MCP_ALLOWED_HOSTS.

    Returns None when unset, an empty list when set to ``*`` (allow any Host),
    otherwise the configured entries expanded into SDK ``host`` / ``host:*``
    patterns, always including the loopback spellings.
    """
    entries = _split_csv(value)
    if not entries:
        return None
    if ALLOW_ANY_HOST in entries:
        return []
    return _expand_host_patterns(list(LOOPBACK_HOSTS) + entries)


def build_transport_security(host: str, allowed_hosts_env: str | None, allowed_origins_env: str | None = None):
    """Derive FastMCP transport-security settings for the network transports.

    FastMCP decides its DNS-rebinding allow-list when it is constructed (with the
    default loopback host), so a server later re-pointed at ``0.0.0.0`` still
    rejects every non-loopback ``Host`` header with 421. This recomputes the
    policy once the real bind address and the operator's allow-list are known.

    Returns None when the SDK's construction-time default (loopback only) should
    be kept, otherwise a ``TransportSecuritySettings`` instance.
    """
    from mcp.server.transport_security import TransportSecuritySettings

    allowed_hosts = resolve_allowed_hosts(allowed_hosts_env)
    if allowed_hosts is None:
        if host in LOOPBACK_BIND_ADDRESSES:
            return None
        # Bound to a non-loopback address without an allow-list: any Host is
        # accepted. main.py warns about this on stderr.
        return TransportSecuritySettings(enable_dns_rebinding_protection=False)
    if not allowed_hosts:
        return TransportSecuritySettings(enable_dns_rebinding_protection=False)

    origins = _origins_for_hosts(allowed_hosts) + _split_csv(allowed_origins_env)
    return TransportSecuritySettings(
        enable_dns_rebinding_protection=True,
        allowed_hosts=allowed_hosts,
        allowed_origins=list(dict.fromkeys(origins)),
    )
