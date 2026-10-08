"""Gateway health monitoring and failover management.

Monitors WireGuard handshake timestamps for all publisher peers and manages
failover when publishers become unreachable.

Also monitors client peer connections on wg-clients interface and reports
status to the control plane for observability.
"""

import logging
import os
import subprocess
import time
from datetime import datetime

import httpx

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("health-monitor")

CONTROL_PLANE_URL = os.environ.get("CONTROL_PLANE_URL", "http://localhost:8443")
BROKER_ID = os.environ.get("BROKER_ID", "broker-01")
BROKER_API_KEY = os.environ.get("BROKER_API_KEY", "")
CHECK_INTERVAL = int(os.environ.get("HEALTH_CHECK_INTERVAL", "15"))
HANDSHAKE_TIMEOUT = int(os.environ.get("HANDSHAKE_TIMEOUT", "180"))  # seconds
CLIENT_HANDSHAKE_TIMEOUT = int(os.environ.get("CLIENT_HANDSHAKE_TIMEOUT", "180"))  # seconds
STALE_FLOW_CYCLES = int(os.environ.get("STALE_FLOW_CYCLES", "2"))  # cycles with no traffic before dropping


def get_namespace_wg_status(namespace: str) -> list[dict]:
    """Get WireGuard peer status within a namespace."""
    # Find the WG interface inside the namespace dynamically (named wg-XXXXXX)
    result = subprocess.run(
        ["ip", "netns", "exec", namespace, "wg", "show", "interfaces"],
        capture_output=True, text=True,
    )
    if result.returncode != 0 or not result.stdout.strip():
        return []

    wg_if = result.stdout.strip().split()[0]

    result = subprocess.run(
        ["ip", "netns", "exec", namespace, "wg", "show", wg_if, "dump"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        return []

    peers = []
    for line in result.stdout.strip().split("\n")[1:]:  # Skip interface line
        parts = line.split("\t")
        if len(parts) >= 5:
            public_key = parts[0]
            endpoint = parts[2] if parts[2] != "(none)" else None
            latest_handshake = int(parts[4]) if parts[4] != "0" else 0
            rx_bytes = int(parts[5]) if len(parts) > 5 else 0
            tx_bytes = int(parts[6]) if len(parts) > 6 else 0

            handshake_age = None
            if latest_handshake > 0:
                handshake_age = int(time.time()) - latest_handshake

            peers.append({
                "public_key": public_key,
                "endpoint": endpoint,
                "latest_handshake": latest_handshake,
                "handshake_age_seconds": handshake_age,
                "rx_bytes": rx_bytes,
                "tx_bytes": tx_bytes,
                "healthy": handshake_age is not None and handshake_age < HANDSHAKE_TIMEOUT,
            })

    return peers


def get_all_namespaces() -> list[str]:
    """List all publisher namespaces (ns-* prefixed)."""
    result = subprocess.run(["ip", "netns", "list"], capture_output=True, text=True)
    namespaces = []
    for line in result.stdout.strip().split("\n"):
        ns = line.split()[0] if line.strip() else ""
        if ns.startswith("ns-"):
            namespaces.append(ns)
    return namespaces


def report_health_status(publisher_statuses: list[dict]):
    """Report publisher health changes to control plane."""
    import json
    from pathlib import Path

    # Load namespace map to resolve publisher names
    ns_map = {}
    ns_map_file = Path("/etc/wireztna/namespace-map.json")
    if ns_map_file.exists():
        try:
            ns_map = json.loads(ns_map_file.read_text())
        except (json.JSONDecodeError, OSError):
            pass

    for status in publisher_statuses:
        if status.get("status_changed"):
            ns_name = status.get("namespace", "")
            # Resolve publisher name from namespace map
            ns_info = ns_map.get(ns_name, {})
            pub_name = ns_info.get("publisher_name", status['public_key'][:8] + "...")

            state_str = "online" if status["healthy"] else "offline"
            try:
                httpx.post(
                    f"{CONTROL_PLANE_URL}/api/v1/audit/logs",
                    headers={"X-API-Key": BROKER_API_KEY},
                    json={
                        "action": "publisher_health_change",
                        "detail": f"Publisher '{pub_name}' is now {'healthy' if status['healthy'] else 'OFFLINE'}",
                    },
                    timeout=5,
                )
            except httpx.RequestError:
                logger.warning("Could not report health status to control plane")


def report_publisher_health(publisher_statuses: list[dict]):
    """Update publisher status based on WG handshake freshness.

    Only sends a heartbeat (updating last_heartbeat) when the publisher's
    WG handshake is fresh — making last_heartbeat mean "last seen alive".
    When unhealthy, reports status=offline without touching last_heartbeat timestamp.
    """
    import json
    from pathlib import Path

    ns_map_file = Path("/etc/wireztna/namespace-map.json")
    if not ns_map_file.exists():
        return

    try:
        ns_map = json.loads(ns_map_file.read_text())
    except (json.JSONDecodeError, OSError):
        return

    # Group statuses by namespace — a namespace is healthy if any peer is healthy
    ns_health: dict[str, bool] = {}
    for status in publisher_statuses:
        ns_name = status.get("namespace", "")
        if ns_name:
            ns_health[ns_name] = ns_health.get(ns_name, False) or status.get("healthy", False)

    for ns_name, healthy in ns_health.items():
        ns_info = ns_map.get(ns_name)
        if not ns_info:
            continue
        pub_id = ns_info.get("publisher_id")
        if not pub_id:
            continue

        try:
            httpx.post(
                f"{CONTROL_PLANE_URL}/api/v1/publishers/{pub_id}/heartbeat",
                headers={"X-API-Key": BROKER_API_KEY},
                json={
                    "publisher_id": pub_id,
                    "timestamp": datetime.utcnow().isoformat(),
                    "uptime_seconds": 0,
                    "status": "online" if healthy else "offline",
                    "update_last_seen": healthy,  # Only update last_heartbeat when alive
                },
                timeout=5,
            )
        except httpx.RequestError as e:
            logger.warning(f"Could not report heartbeat for publisher {pub_id}: {e}")


def check_all_publishers() -> list[dict]:
    """Check health of all publishers across all namespaces."""
    namespaces = get_all_namespaces()
    all_statuses = []

    for ns in namespaces:
        peers = get_namespace_wg_status(ns)
        for peer in peers:
            peer["namespace"] = ns
            all_statuses.append(peer)

    return all_statuses


# Track previous states for change detection
_previous_states: dict[str, bool] = {}


def get_client_peers_status() -> list[dict]:
    """Get status of all client peers on the wg-clients interface."""
    result = subprocess.run(
        ["wg", "show", "wg-clients", "dump"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        return []

    peers = []
    lines = result.stdout.strip().split("\n")
    for line in lines[1:]:  # Skip interface line
        parts = line.split("\t")
        if len(parts) >= 7:
            public_key = parts[0]
            # parts[1] = preshared_key (or (none))
            endpoint = parts[2] if parts[2] != "(none)" else None
            # parts[3] = allowed_ips
            latest_handshake = int(parts[4]) if parts[4] != "0" else 0
            rx_bytes = int(parts[5])
            tx_bytes = int(parts[6])

            # Parse endpoint into ip:port
            endpoint_ip = None
            endpoint_port = None
            if endpoint:
                ep_parts = endpoint.rsplit(":", 1)
                endpoint_ip = ep_parts[0]
                endpoint_port = int(ep_parts[1]) if len(ep_parts) > 1 else None

            handshake_age = None
            if latest_handshake > 0:
                handshake_age = int(time.time()) - latest_handshake

            is_connected = handshake_age is not None and handshake_age < CLIENT_HANDSHAKE_TIMEOUT

            peers.append({
                "public_key": public_key,
                "endpoint_ip": endpoint_ip,
                "endpoint_port": endpoint_port,
                "last_handshake_at": datetime.utcfromtimestamp(latest_handshake).isoformat() if latest_handshake > 0 else None,
                "is_connected": is_connected,
                "rx_bytes": rx_bytes,
                "tx_bytes": tx_bytes,
            })

    return peers


def report_client_status(client_peers: list[dict]):
    """Report client peer status to the control plane."""
    if not client_peers:
        return

    try:
        httpx.post(
            f"{CONTROL_PLANE_URL}/api/v1/brokers/{BROKER_ID}/client-status",
            headers={"X-API-Key": BROKER_API_KEY},
            json={"peers": client_peers},
            timeout=5,
        )
    except httpx.RequestError as e:
        logger.warning(f"Could not report client status to control plane: {e}")


# ─── Conntrack flow collection ───

def get_client_flows() -> dict[str, list[dict]]:
    """Collect active conntrack flows grouped by client overlay IP.

    Runs conntrack -L and parses entries involving IPs in the overlay
    network (10.200.0.0/16). Returns a dict mapping overlay_ip to flows.
    """
    try:
        result = subprocess.run(
            ["conntrack", "-L", "-f", "ipv4"],
            capture_output=True, text=True,
        )
    except FileNotFoundError:
        # conntrack tool not installed — skip silently
        logger.debug("conntrack not found, skipping flow collection")
        return {}

    if result.returncode != 0:
        logger.debug(f"conntrack -L failed: {result.stderr.strip()}")
        return {}

    flows_by_ip: dict[str, list[dict]] = {}

    for line in result.stdout.strip().split("\n"):
        if not line.strip():
            continue

        # Only interested in flows from overlay network (10.200.x.x)
        # conntrack output format (default, no -o extended):
        # tcp  6 431990 ESTABLISHED src=10.200.0.2 dst=10.50.1.168 sport=48832 dport=22 ...
        # ICMP format:
        # icmp  1 29 src=10.200.1.0 dst=10.50.1.168 type=8 code=0 id=22072 ...
        parts = line.split()
        if len(parts) < 6:
            continue

        try:
            protocol = parts[0]

            # Skip the "ipv4  2" prefix if present (extended format)
            if protocol == "ipv4" or protocol == "ipv6":
                parts = parts[2:]
                if len(parts) < 6:
                    continue
                protocol = parts[0]

            state = ""
            src_ip = ""
            dst_ip = ""
            sport = 0
            dport = 0
            bytes_in = 0
            bytes_out = 0
            packets_out = 0
            packets_in = 0

            for part in parts:
                if part.startswith("src=") and not src_ip:
                    src_ip = part[4:]
                elif part.startswith("dst=") and not dst_ip:
                    dst_ip = part[4:]
                elif part.startswith("sport=") and not sport:
                    sport = int(part[6:])
                elif part.startswith("dport=") and not dport:
                    dport = int(part[6:])
                elif part.startswith("packets="):
                    if not packets_out:
                        packets_out = int(part[8:])
                    else:
                        packets_in = int(part[8:])
                elif part.startswith("bytes="):
                    if not bytes_out:
                        bytes_out = int(part[6:])
                    else:
                        bytes_in = int(part[6:])
                elif part in ("ESTABLISHED", "TIME_WAIT", "SYN_SENT",
                              "SYN_RECV", "FIN_WAIT", "CLOSE_WAIT",
                              "LAST_ACK", "CLOSE", "UNREPLIED"):
                    state = part
                elif part == "ASSURED":
                    pass  # Skip — not a connection state, just a conntrack flag

            # Only track flows from overlay clients (10.200.x.x)
            if not src_ip.startswith("10.200."):
                continue

            # Skip flows to the broker itself
            if dst_ip.startswith("10.200."):
                continue

            # State fallback for UDP/ICMP
            if not state:
                state = "ACTIVE"

            flow = {
                "protocol": protocol,
                "dst_ip": dst_ip,
                "dst_port": dport,
                "src_port": sport,
                "state": state,
                "bytes_out": bytes_out,
                "bytes_in": bytes_in,
                "packets_out": packets_out,
                "packets_in": packets_in,
            }

            if src_ip not in flows_by_ip:
                flows_by_ip[src_ip] = []
            flows_by_ip[src_ip].append(flow)

        except (ValueError, IndexError):
            continue

    return flows_by_ip


def report_client_flows(flows_by_ip: dict[str, list[dict]]):
    """Report active client flows to the control plane."""
    try:
        httpx.post(
            f"{CONTROL_PLANE_URL}/api/v1/brokers/{BROKER_ID}/client-flows",
            headers={"X-API-Key": BROKER_API_KEY},
            json={"flows_by_ip": flows_by_ip},
            timeout=5,
        )
    except httpx.RequestError as e:
        logger.debug(f"Could not report client flows: {e}")


# ─── Stale flow detection ───
# Tracks bytes per flow across cycles. If bytes don't change for STALE_FLOW_CYCLES
# consecutive checks, the flow is considered dead (connection interrupted without
# proper TCP teardown) and gets filtered out.
# Key: (src_ip, protocol, dst_ip, dst_port, src_port) → {"bytes": total, "stale_count": int}
_flow_byte_tracker: dict[tuple, dict] = {}


def _flow_key(src_ip: str, flow: dict) -> tuple:
    """Create a unique key for a flow entry."""
    return (src_ip, flow["protocol"], flow["dst_ip"], flow["dst_port"], flow["src_port"])


def filter_stale_flows(flows_by_ip: dict[str, list[dict]]) -> dict[str, list[dict]]:
    """Remove flows that haven't seen any new bytes since the last cycle.

    A flow is considered stale if its total bytes (in + out) haven't changed
    for STALE_FLOW_CYCLES consecutive health check cycles (~30s by default).
    This catches connections that were interrupted without a clean TCP close.
    """
    global _flow_byte_tracker

    seen_keys: set[tuple] = set()
    filtered: dict[str, list[dict]] = {}

    for src_ip, flows in flows_by_ip.items():
        active_flows = []
        for flow in flows:
            key = _flow_key(src_ip, flow)
            seen_keys.add(key)
            total_bytes = flow.get("bytes_in", 0) + flow.get("bytes_out", 0)

            prev = _flow_byte_tracker.get(key)
            if prev is None:
                # New flow — always include
                _flow_byte_tracker[key] = {"bytes": total_bytes, "stale_count": 0}
                active_flows.append(flow)
            elif total_bytes != prev["bytes"]:
                # Traffic changed — reset stale counter
                _flow_byte_tracker[key] = {"bytes": total_bytes, "stale_count": 0}
                active_flows.append(flow)
            else:
                # No change — increment stale counter
                prev["stale_count"] += 1
                if prev["stale_count"] < STALE_FLOW_CYCLES:
                    active_flows.append(flow)
                # else: drop — flow is stale

        if active_flows:
            filtered[src_ip] = active_flows

    # Clean up tracker entries for flows that no longer exist in conntrack
    stale_keys = set(_flow_byte_tracker.keys()) - seen_keys
    for key in stale_keys:
        del _flow_byte_tracker[key]

    return filtered


def monitor_loop():
    """Main health monitoring loop."""
    global _previous_states

    logger.info("WireZTNA Health Monitor starting")
    logger.info(f"  Check interval: {CHECK_INTERVAL}s")
    logger.info(f"  Handshake timeout (publishers): {HANDSHAKE_TIMEOUT}s")
    logger.info(f"  Handshake timeout (clients): {CLIENT_HANDSHAKE_TIMEOUT}s")

    while True:
        # ─── Publisher health checks ───
        statuses = check_all_publishers()

        changes = []
        for status in statuses:
            key = f"{status['namespace']}:{status['public_key']}"
            prev_healthy = _previous_states.get(key)
            current_healthy = status["healthy"]

            if prev_healthy is not None and prev_healthy != current_healthy:
                status["status_changed"] = True
                state_str = "ONLINE" if current_healthy else "OFFLINE"
                logger.warning(
                    f"Publisher state change in {status['namespace']}: "
                    f"{status['public_key'][:8]}... → {state_str} "
                    f"(handshake age: {status['handshake_age_seconds']}s)"
                )
                changes.append(status)
            else:
                status["status_changed"] = False

            _previous_states[key] = current_healthy

        if changes:
            report_health_status(changes)

        # Report publisher heartbeat status to control plane
        if statuses:
            report_publisher_health(statuses)

        # Log publisher summary
        total = len(statuses)
        healthy = sum(1 for s in statuses if s["healthy"])
        if total > 0:
            logger.info(f"Health check: {healthy}/{total} publishers healthy")

        # ─── Client peer status reporting ───
        client_peers = get_client_peers_status()
        if client_peers:
            connected = sum(1 for p in client_peers if p["is_connected"])
            logger.info(f"Client peers: {connected}/{len(client_peers)} connected")
            report_client_status(client_peers)

        # ─── Client flow collection (conntrack) ───
        flows_by_ip = get_client_flows()
        if flows_by_ip:
            flows_by_ip = filter_stale_flows(flows_by_ip)
            total_flows = sum(len(f) for f in flows_by_ip.values())
            logger.info(f"Active flows: {total_flows} across {len(flows_by_ip)} clients")
            report_client_flows(flows_by_ip)
        else:
            # No flows at all — report empty to clear the cache
            report_client_flows({})

        time.sleep(CHECK_INTERVAL)


if __name__ == "__main__":
    monitor_loop()
