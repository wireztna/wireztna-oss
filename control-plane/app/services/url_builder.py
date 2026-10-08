"""Builders for server-generated, user-facing HTTP/WS URLs.

CE routes every externally-visible link (enroll, portal, install, access-pass)
through these helpers so the scheme and host are configured in one place. The
default scheme is http (bare-IP, no TLS); set BROKER_PUBLIC_SCHEME=https (or
PUBLIC_BASE_URL) when terminating TLS in front of the broker.

These do NOT cover the WireGuard Endpoint host:port strings (those have no
scheme and are built in routers/clients.py and routers/sessions.py).
"""

from app.config import settings


def _host() -> str:
    """Resolve the public host, falling back to localhost for dev."""
    return settings.broker_public_endpoint or "127.0.0.1"


def external_base_url() -> str:
    """Return the external HTTP(S) base URL for server-generated links.

    Precedence:
      1. settings.public_base_url, if set (used verbatim).
      2. f"{broker_public_scheme}://{broker_public_endpoint}".
      3. Dev fallback host 127.0.0.1 when no endpoint is configured.
    """
    if settings.public_base_url:
        return settings.public_base_url.rstrip("/")
    return f"{settings.broker_public_scheme}://{_host()}"


def external_ws_base_url() -> str:
    """Return the external WebSocket base URL (ws:// or wss://).

    Derives the ws scheme from the resolved HTTP scheme and reuses the same host.
    """
    base = external_base_url()
    if base.startswith("https://"):
        return "wss://" + base[len("https://"):]
    if base.startswith("http://"):
        return "ws://" + base[len("http://"):]
    # No recognizable scheme prefix — derive from configured scheme.
    scheme = "wss" if settings.broker_public_scheme == "https" else "ws"
    return f"{scheme}://{_host()}"
