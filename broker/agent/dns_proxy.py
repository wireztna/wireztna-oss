"""Per-client DNS proxy for WireZTNA.

Listens on the broker's overlay IP (10.200.0.1:53) and routes DNS queries
to the correct publisher's DNS server based on:
  1. The client's overlay IP (source of the query)
  2. The zone being queried
  3. Which publishers that client can access

When a query is resolved via a publisher, the proxy records a "DNS routing hint"
mapping the resolved IP → publisher_index for that client. This hint is used by
the reconciler to create /32 nftables rules that ensure traffic to that IP goes
through the correct publisher — solving CIDR overlap when two publishers expose
the same network range but with different DNS zones.

IMPORTANT: The hint publisher_index is determined by which publisher's exposed_cidrs
CONTAIN the resolved IP, NOT by which publisher's DNS server resolved the query.
This handles CNAME chains that cross publishers (e.g., portal.company.internal
resolves via publisher A's DNS but the resulting IP belongs to publisher B's network).

Configuration is read from /etc/wireztna/dns-proxy.json, refreshed by the reconciler.

Config format:
{
  "clients": {
    "10.200.1.0": {
      "zones": {
        "compute.internal": {"publisher_index": 1, "proxy_port": 5301},
        "office.internal": {"publisher_index": 2, "proxy_port": 5302}
      }
    }
  },
  "publishers": {
    "1": {"exposed_cidrs": ["10.50.0.0/16"]},
    "2": {"exposed_cidrs": ["172.21.232.0/21", "10.1.0.0/19"]}
  },
  "default_dns": ["8.8.8.8", "1.1.1.1"]
}

Dependencies: dnslib (pip install dnslib)
"""

import ipaddress
import json
import logging
import os
import socket
import struct
import threading
import time
from pathlib import Path
from typing import Dict, List, Optional, Tuple

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")
logger = logging.getLogger("dns-proxy")

# ─── Configuration ───
LISTEN_IP = os.environ.get("DNS_LISTEN_IP", "10.200.0.1")
LISTEN_PORT = int(os.environ.get("DNS_LISTEN_PORT", "53"))
CONFIG_FILE = Path(os.environ.get("DNS_CONFIG_FILE", "/etc/wireztna/dns-proxy.json"))
CONTROL_PLANE_URL = os.environ.get("CONTROL_PLANE_URL", "http://localhost:8443")
BROKER_ID = os.environ.get("BROKER_ID", "broker-01")
BROKER_API_KEY = os.environ.get("BROKER_API_KEY", "")
HINTS_REPORT_INTERVAL = int(os.environ.get("HINTS_REPORT_INTERVAL", "10"))  # seconds
DEFAULT_DNS = ["8.8.8.8", "1.1.1.1"]
DNS_TIMEOUT = 3  # seconds for upstream queries
HINT_TTL = 300  # seconds before a DNS hint expires


# ─── DNS Routing Hints ───
# Accumulates resolved IP → publisher_index mappings per client
# Periodically reported to the control plane API
_dns_hints: Dict[str, List[dict]] = {}  # client_ip → [{ip, publisher_index, ttl, timestamp}]
_hints_lock = threading.Lock()


def record_dns_hint(client_ip: str, resolved_ip: str, publisher_index: int):
    """Record that a DNS resolution for client_ip returned resolved_ip via publisher_index."""
    with _hints_lock:
        if client_ip not in _dns_hints:
            _dns_hints[client_ip] = []

        # Update existing or add new
        hints = _dns_hints[client_ip]
        for hint in hints:
            if hint["ip"] == resolved_ip:
                hint["publisher_index"] = publisher_index
                hint["timestamp"] = time.time()
                return

        hints.append({
            "ip": resolved_ip,
            "publisher_index": publisher_index,
            "timestamp": time.time(),
        })


def get_and_clear_hints() -> Dict[str, List[dict]]:
    """Get all accumulated hints and clear the buffer."""
    with _hints_lock:
        hints = {}
        now = time.time()
        for client_ip, client_hints in _dns_hints.items():
            # Filter out expired hints
            active = [h for h in client_hints if (now - h["timestamp"]) < HINT_TTL]
            if active:
                hints[client_ip] = [
                    {"ip": h["ip"], "publisher_index": h["publisher_index"]}
                    for h in active
                ]
        # Keep only non-expired hints in the buffer
        for client_ip in list(_dns_hints.keys()):
            _dns_hints[client_ip] = [
                h for h in _dns_hints[client_ip]
                if (now - h["timestamp"]) < HINT_TTL
            ]
            if not _dns_hints[client_ip]:
                del _dns_hints[client_ip]
        return hints


def report_hints_loop():
    """Background thread: periodically report DNS hints to the control plane."""
    import httpx

    while True:
        time.sleep(HINTS_REPORT_INTERVAL)
        hints = get_and_clear_hints()
        if not hints:
            continue

        try:
            httpx.post(
                f"{CONTROL_PLANE_URL}/api/v1/brokers/{BROKER_ID}/dns-hints",
                headers={"X-API-Key": BROKER_API_KEY},
                json={"hints_by_client": hints},
                timeout=5,
            )
            total = sum(len(v) for v in hints.values())
            logger.debug(f"Reported {total} DNS hints for {len(hints)} clients")
        except Exception as e:
            logger.warning(f"Failed to report DNS hints: {e}")


# ─── Configuration Loading ───
_config: dict = {"clients": {}, "default_dns": DEFAULT_DNS}
_config_mtime: float = 0


def load_config():
    """Reload config from disk if changed."""
    global _config, _config_mtime

    if not CONFIG_FILE.exists():
        return

    try:
        mtime = CONFIG_FILE.stat().st_mtime
        if mtime == _config_mtime:
            return
        _config = json.loads(CONFIG_FILE.read_text())
        _config_mtime = mtime
        clients_count = len(_config.get("clients", {}))
        zones_count = sum(
            len(c.get("zones", {}))
            for c in _config.get("clients", {}).values()
        )
        logger.info(f"Config reloaded: {clients_count} clients, {zones_count} zone mappings")
    except (json.JSONDecodeError, OSError) as e:
        logger.error(f"Failed to load config: {e}")


def find_zone_for_query(client_ip: str, qname: str) -> Optional[dict]:
    """Find which publisher zone matches this query for this client.

    Matches the longest zone suffix. E.g., for query "db1.compute.internal":
      - "compute.internal" matches
      - "internal" would also match but is shorter → less specific

    Returns {"publisher_index": N, "proxy_port": P} or None.
    """
    client_config = _config.get("clients", {}).get(client_ip)
    if not client_config:
        return None

    zones = client_config.get("zones", {})
    if not zones:
        return None

    # Normalize qname (remove trailing dot if present)
    qname_lower = qname.rstrip(".").lower()

    best_match = None
    best_len = 0

    for zone, zone_config in zones.items():
        zone_lower = zone.rstrip(".").lower()
        # Check if qname ends with the zone (or equals it)
        if qname_lower == zone_lower or qname_lower.endswith("." + zone_lower):
            if len(zone_lower) > best_len:
                best_match = zone_config
                best_len = len(zone_lower)

    return best_match


def find_publisher_for_ip(resolved_ip: str, fallback_index: int) -> int:
    """Find which publisher owns the resolved IP based on exposed_cidrs.

    When a DNS query resolves to an IP (possibly via CNAME chain), the hint should
    point to the publisher whose exposed_cidrs CONTAIN that IP — not necessarily
    the publisher whose DNS server resolved the query.

    This handles the case where:
      - Publisher A (index=6) exposes 172.21.232.0/21 and has zone "company.internal"
      - Publisher B (index=4) exposes 10.0.0.0/16 and has zone "compute.internal"
      - Query: portal.company.internal → CNAME → ip-172-21-233-180.eu-central-1.compute.internal → 172.21.233.180
      - The CNAME resolution goes through publisher B (compute.internal zone)
      - But 172.21.233.180 is in 172.21.232.0/21 → publisher A owns it

    Uses longest prefix match when multiple publishers' CIDRs contain the IP.

    Falls back to fallback_index (the DNS-resolving publisher) if no CIDR match found.
    """
    publishers = _config.get("publishers", {})
    if not publishers:
        return fallback_index

    try:
        ip_addr = ipaddress.ip_address(resolved_ip)
    except ValueError:
        return fallback_index

    best_index = None
    best_prefix_len = -1

    for pub_index_str, pub_config in publishers.items():
        cidrs = pub_config.get("exposed_cidrs", [])
        for cidr_str in cidrs:
            try:
                network = ipaddress.ip_network(cidr_str, strict=False)
                if ip_addr in network:
                    if network.prefixlen > best_prefix_len:
                        best_prefix_len = network.prefixlen
                        best_index = int(pub_index_str)
            except (ValueError, TypeError):
                continue

    if best_index is not None:
        if best_index != fallback_index:
            logger.debug(
                f"  Hint correction: {resolved_ip} belongs to publisher {best_index} "
                f"(not {fallback_index} which resolved the DNS)"
            )
        return best_index

    return fallback_index


# ─── DNS Packet Handling (minimal, no dnslib dependency) ───
# We implement a simple UDP DNS forwarder that doesn't parse the full DNS packet,
# just extracts the question name and forwards the raw packet to the correct upstream.
# This avoids the need for dnslib as a dependency.

def extract_qname(data: bytes) -> str:
    """Extract the first question name from a raw DNS packet."""
    # Skip header (12 bytes)
    offset = 12
    labels = []
    while offset < len(data):
        length = data[offset]
        if length == 0:
            break
        offset += 1
        labels.append(data[offset:offset + length].decode("ascii", errors="replace"))
        offset += length
    return ".".join(labels)


def extract_answer_ips(data: bytes) -> List[str]:
    """Extract A record IPs from DNS response (simple parser)."""
    ips = []
    if len(data) < 12:
        return ips

    # Parse header
    flags = struct.unpack("!H", data[2:4])[0]
    qdcount = struct.unpack("!H", data[4:6])[0]
    ancount = struct.unpack("!H", data[6:8])[0]

    if ancount == 0:
        return ips

    # Skip header
    offset = 12

    # Skip questions
    for _ in range(qdcount):
        while offset < len(data):
            length = data[offset]
            if length == 0:
                offset += 1
                break
            if (length & 0xC0) == 0xC0:  # Pointer
                offset += 2
                break
            offset += 1 + length
        offset += 4  # QTYPE + QCLASS

    # Parse answers
    for _ in range(ancount):
        if offset >= len(data):
            break
        # Skip name (may be pointer)
        if (data[offset] & 0xC0) == 0xC0:
            offset += 2
        else:
            while offset < len(data) and data[offset] != 0:
                offset += 1 + data[offset]
            offset += 1

        if offset + 10 > len(data):
            break

        rtype = struct.unpack("!H", data[offset:offset + 2])[0]
        offset += 2  # TYPE
        offset += 2  # CLASS
        offset += 4  # TTL
        rdlength = struct.unpack("!H", data[offset:offset + 2])[0]
        offset += 2

        if rtype == 1 and rdlength == 4:  # A record
            ip = ".".join(str(b) for b in data[offset:offset + 4])
            ips.append(ip)

        offset += rdlength

    return ips


def forward_query(data: bytes, upstream_ip: str, upstream_port: int = 53) -> Optional[bytes]:
    """Forward a DNS query to an upstream server and return the response."""
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        sock.settimeout(DNS_TIMEOUT)
        sock.sendto(data, (upstream_ip, upstream_port))
        response, _ = sock.recvfrom(4096)
        sock.close()
        return response
    except (socket.timeout, OSError) as e:
        logger.debug(f"Upstream {upstream_ip}:{upstream_port} failed: {e}")
        return None


def handle_query(data: bytes, client_addr: tuple) -> Optional[bytes]:
    """Process a DNS query: route to correct publisher or default DNS."""
    client_ip = client_addr[0]
    qname = extract_qname(data)

    if not qname:
        # Can't parse — forward to default
        return forward_to_default(data)

    # Reload config if changed
    load_config()

    # Find matching zone for this client + query
    zone_match = find_zone_for_query(client_ip, qname)

    if zone_match:
        proxy_port = zone_match["proxy_port"]
        publisher_index = zone_match["publisher_index"]

        logger.debug(f"{client_ip} → {qname} → publisher {publisher_index} (port {proxy_port})")

        # Forward to the socat proxy for this publisher's namespace
        response = forward_query(data, "127.0.0.1", proxy_port)

        if response:
            # Extract resolved IPs and record as hints
            # IMPORTANT: attribute the hint to the publisher whose exposed_cidrs
            # contain the resolved IP, NOT the publisher whose DNS resolved it.
            # This handles CNAME chains that cross publishers.
            resolved_ips = extract_answer_ips(response)
            for ip in resolved_ips:
                correct_index = find_publisher_for_ip(ip, publisher_index)
                record_dns_hint(client_ip, ip, correct_index)

            if resolved_ips:
                logger.debug(f"  Resolved: {qname} → {resolved_ips}")

            return response
        else:
            logger.warning(f"Publisher DNS proxy port {proxy_port} unreachable for {qname}")
            # Fall through to default DNS

    # No zone match or publisher DNS failed → forward to public DNS
    return forward_to_default(data)


def forward_to_default(data: bytes) -> Optional[bytes]:
    """Forward to default public DNS servers."""
    dns_servers = _config.get("default_dns", DEFAULT_DNS)
    for server in dns_servers:
        response = forward_query(data, server)
        if response:
            return response
    return None


# ─── UDP Server ───

def run_server():
    """Main DNS proxy UDP server."""
    load_config()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)

    try:
        sock.bind((LISTEN_IP, LISTEN_PORT))
    except OSError as e:
        logger.error(f"Cannot bind to {LISTEN_IP}:{LISTEN_PORT}: {e}")
        logger.info("Hint: ensure the overlay interface (wg-clients) is up and has this IP")
        raise

    logger.info(f"DNS proxy listening on {LISTEN_IP}:{LISTEN_PORT}")
    logger.info(f"Config file: {CONFIG_FILE}")
    logger.info(f"Hints report interval: {HINTS_REPORT_INTERVAL}s")

    # Start hints reporter thread
    hints_thread = threading.Thread(target=report_hints_loop, daemon=True)
    hints_thread.start()

    while True:
        try:
            data, client_addr = sock.recvfrom(4096)
            # Handle in a thread to avoid blocking
            threading.Thread(
                target=_handle_and_reply,
                args=(sock, data, client_addr),
                daemon=True,
            ).start()
        except OSError as e:
            logger.error(f"Socket error: {e}")
            time.sleep(1)


def _handle_and_reply(sock: socket.socket, data: bytes, client_addr: tuple):
    """Handle a query and send the reply."""
    try:
        response = handle_query(data, client_addr)
        if response:
            sock.sendto(response, client_addr)
    except Exception as e:
        logger.error(f"Error handling query from {client_addr}: {e}")


if __name__ == "__main__":
    run_server()
