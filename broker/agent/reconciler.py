"""Broker Agent — Reconciliation daemon.

Polls the control plane for desired state and reconciles:
- Client peers on wg-clients (with PSK from active sessions)
- Per-publisher network namespaces with isolated WireGuard tunnels
- nftables + policy routing for transparent per-client access (fwmark-based)

Access model: User → Group → Publisher (exposed_cidrs)
Traffic flow:
  Client → real IP (e.g., 10.50.1.168) → wg-clients → nftables marks fwmark
  → policy routing (ip rule fwmark N → table 100+N) → veth into namespace
  → WG tunnel to publisher → publisher delivers to real IP on its LAN
  → reply via conntrack (same path back)

No DNAT needed — clients use real CIDRs, broker just routes to the correct namespace.
"""

import hashlib
import json
import logging
import os
import subprocess
import tempfile
import time
from pathlib import Path

import httpx

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("broker-agent")

CONTROL_PLANE_URL = os.environ.get("CONTROL_PLANE_URL", "http://localhost:8443")
BROKER_ID = os.environ.get("BROKER_ID", "broker-01")
BROKER_API_KEY = os.environ.get("BROKER_API_KEY", "")
POLL_INTERVAL = int(os.environ.get("BROKER_POLL_INTERVAL", "10"))
WG_CLIENTS_IF = "wg-clients"
CONFIG_DIR = Path("/etc/wireztna")
SCRIPTS_DIR = Path(os.environ.get("SCRIPTS_DIR", "/opt/wireztna/scripts"))
NAMESPACE_MAP_FILE = CONFIG_DIR / "namespace-map.json"


# ─── Utility ───

def run_cmd(cmd: list[str], check=False) -> subprocess.CompletedProcess:
    result = subprocess.run(cmd, capture_output=True, text=True)
    if check and result.returncode != 0:
        logger.error(f"Command failed: {' '.join(cmd)}\n  {result.stderr.strip()}")
    return result


def wg_set_peer_psk(interface: str, public_key: str, psk: str, allowed_ips: str) -> bool:
    with tempfile.NamedTemporaryFile(mode='w', suffix='.psk', delete=False) as f:
        f.write(psk)
        psk_file = f.name
    try:
        result = run_cmd(["wg", "set", interface, "peer", public_key,
                          "preshared-key", psk_file, "allowed-ips", allowed_ips], check=True)
        return result.returncode == 0
    finally:
        os.unlink(psk_file)


def get_current_peers(interface: str) -> dict:
    result = run_cmd(["wg", "show", interface, "dump"])
    peers = {}
    if result.returncode != 0 or not result.stdout.strip():
        return peers
    for line in result.stdout.strip().split("\n")[1:]:
        parts = line.split("\t")
        if len(parts) >= 8:
            peers[parts[0]] = {
                "psk": parts[1] if parts[1] != "(none)" else "",
                "endpoint": parts[2] if parts[2] != "(none)" else "",
                "allowed_ips": parts[3],
                "latest_handshake": int(parts[4]) if parts[4] != "0" else 0,
            }
    return peers


def interface_exists(name: str) -> bool:
    return run_cmd(["ip", "link", "show", name]).returncode == 0


# ─── Namespace map persistence ───

def load_namespace_map() -> dict:
    """Load the namespace-to-publisher mapping from disk.

    Returns dict mapping namespace name (e.g., 'ns-a1b2c3d4') to
    {'publisher_id': ..., 'publisher_index': ...}
    """
    if NAMESPACE_MAP_FILE.exists():
        try:
            return json.loads(NAMESPACE_MAP_FILE.read_text())
        except (json.JSONDecodeError, OSError) as e:
            logger.warning(f"Failed to load namespace map: {e}")
    return {}


def save_namespace_map(ns_map: dict):
    """Persist the namespace-to-publisher mapping to disk."""
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    NAMESPACE_MAP_FILE.write_text(json.dumps(ns_map, indent=2))


def get_existing_namespaces() -> set:
    """Get set of existing publisher namespaces (ns-* prefixed) from the system."""
    result = run_cmd(["ip", "netns", "list"])
    namespaces = set()
    if result.returncode == 0 and result.stdout.strip():
        for line in result.stdout.strip().split("\n"):
            # ip netns list output can include IDs like "ns-a1b2c3d4 (id: 0)"
            ns_name = line.split()[0] if line.strip() else ""
            if ns_name.startswith("ns-"):
                namespaces.add(ns_name)
    return namespaces


# ─── DNS / Corefile management ───

def _regenerate_corefile(publishers: list):
    """Regenerate CoreDNS Corefile with split DNS zones for publishers.

    Pipes publisher JSON (with dns_zones/dns_server) to generate-corefile.sh,
    which produces a Corefile forwarding zone queries to local proxy ports.
    Then signals CoreDNS to reload.
    """
    # Only include publishers that have both dns_zones and a dns_server reported by the publisher
    dns_publishers = [
        p for p in publishers
        if p.get("dns_zones") and p.get("dns_server") and p.get("publisher_index") is not None
    ]

    # Always regenerate (even with no DNS publishers — produces a clean default Corefile)
    publishers_json = json.dumps(publishers)

    result = subprocess.run(
        [str(SCRIPTS_DIR / "generate-corefile.sh")],
        input=publishers_json,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        logger.error(f"Failed to generate Corefile: {result.stderr}")
        return

    if dns_publishers:
        logger.info(
            f"Corefile regenerated with {len(dns_publishers)} publisher DNS zone(s)"
        )
    else:
        logger.debug("Corefile regenerated (no publisher DNS zones)")

    # Reload CoreDNS — send SIGUSR1 to trigger config reload
    # CoreDNS watches the Corefile if 'reload' plugin is configured, but we also
    # try explicit signal for immediate effect.
    _reload_coredns()


def _reload_coredns():
    """Signal CoreDNS to reload its configuration."""
    # Try to find CoreDNS PID and send SIGUSR1
    result = run_cmd(["pidof", "coredns"])
    if result.returncode == 0 and result.stdout.strip():
        pids = result.stdout.strip().split()
        for pid in pids:
            run_cmd(["kill", "-SIGUSR1", pid])
        logger.debug(f"Sent SIGUSR1 to CoreDNS (PIDs: {', '.join(pids)})")
    else:
        # CoreDNS not running as a direct process — might be in a container
        # The 'reload' plugin in the Corefile handles this automatically
        logger.debug("CoreDNS PID not found; relying on 'reload' plugin for config refresh")


# ─── Publisher reconciliation ───


def _report_namespace_info(pub_id: str, ns_name: str, publisher_index: int, broker_tunnel_ip: str):
    """Read the namespace WG public key and port, then report to the control plane.

    This allows the publisher to poll /connection-info and get the real
    broker namespace key/port for its WireGuard config.
    """
    # Get the WG interface name (first 6 chars of pub_id with wg- prefix)
    id_short = pub_id[:6]
    wg_iface = f"wg-{id_short}"

    # Read public key from the namespace
    result = run_cmd(["ip", "netns", "exec", ns_name, "wg", "show", wg_iface, "public-key"])
    if result.returncode != 0 or not result.stdout.strip():
        logger.warning(f"Cannot read public key from {ns_name}/{wg_iface}")
        return
    ns_public_key = result.stdout.strip()

    # Read listen port
    result = run_cmd(["ip", "netns", "exec", ns_name, "wg", "show", wg_iface, "listen-port"])
    if result.returncode != 0 or not result.stdout.strip():
        logger.warning(f"Cannot read listen port from {ns_name}/{wg_iface}")
        return
    ns_port = int(result.stdout.strip())

    # Report to control plane
    url = f"{CONTROL_PLANE_URL}/api/v1/publishers/{pub_id}/namespace-info"
    headers = {"X-API-Key": BROKER_API_KEY, "Content-Type": "application/json"}
    payload = {
        "broker_ns_public_key": ns_public_key,
        "broker_ns_port": ns_port,
        "broker_tunnel_ip": broker_tunnel_ip,
    }

    try:
        response = httpx.post(url, headers=headers, json=payload, timeout=10)
        if response.status_code == 204:
            logger.info(f"Reported namespace info for {ns_name}: key={ns_public_key[:12]}..., port={ns_port}")
        else:
            logger.warning(f"Failed to report namespace info: HTTP {response.status_code}")
    except httpx.RequestError as e:
        logger.warning(f"Failed to report namespace info: {e}")


def _publisher_config_hash(pub: dict) -> str:
    """Compute a hash of the publisher config fields that affect the namespace setup.

    NOTE: exit_node is intentionally EXCLUDED from this hash. Toggling exit_node
    should NOT trigger WG interface recreation — it only affects client-side routing
    (nftables rules) and the session's AllowedIPs response. The namespace WG always
    uses 0.0.0.0/0 as AllowedIPs so it can handle both split and full-tunnel traffic
    without reconfiguration.
    """
    relevant = {
        "public_key": pub.get("public_key", ""),
        "exposed_cidrs": sorted(pub.get("exposed_cidrs", [])),
        "endpoint": pub.get("endpoint", ""),
        "dns_server": pub.get("dns_server", ""),
        "dns_zones": sorted(pub.get("dns_zones") or []),
    }
    return hashlib.sha256(json.dumps(relevant, sort_keys=True).encode()).hexdigest()[:12]


def reconcile_publishers(desired_publishers: list):
    """Reconcile per-publisher network namespaces.

    Each publisher with a public_key and status != 'pending' gets its own
    Linux network namespace containing:
    - A veth pair connecting to the host
    - A WireGuard interface for the publisher tunnel
    - Routes for exposed CIDRs via the WG interface

    No NAT needed — traffic arrives with real destination IP and is routed
    to the publisher via WG tunnel. Policy routing on the host (fwmark) directs
    packets to the correct namespace veth.
    """

    # Load current namespace map from disk
    ns_map = load_namespace_map()

    # Get existing namespaces from the system
    existing_namespaces = get_existing_namespaces()

    # Build desired namespace set from publishers
    desired_ns_map = {}
    for pub in desired_publishers:
        pub_id = pub.get("id", "")
        pub_key = pub.get("public_key", "")
        status = pub.get("status", "pending")
        publisher_index = pub.get("publisher_index")

        # Skip publishers without a key or that are still pending
        if not pub_key or status == "pending" or not pub_id or publisher_index is None:
            continue

        id_short = pub_id[:8]
        ns_name = f"ns-{id_short}"
        desired_ns_map[ns_name] = pub

    # ─── Create/update namespaces for desired publishers ───
    for ns_name, pub in desired_ns_map.items():
        pub_id = pub["id"]
        publisher_index = pub["publisher_index"]
        pub_key = pub["public_key"]
        exposed_cidrs = pub.get("exposed_cidrs", [])

        # Always use 0.0.0.0/0 as WG AllowedIPs in the namespace.
        # This allows the namespace to forward ANY traffic to the publisher,
        # whether it's split-tunnel (specific CIDRs) or full-tunnel (exit node).
        # The actual access control is enforced by nftables on the host — only
        # authorized traffic gets marked and forwarded to this namespace's veth.
        # This approach means toggling exit_node doesn't require WG recreation.
        wg_cidrs = ["0.0.0.0/0"]

        exposed_cidrs_csv = ",".join(wg_cidrs)

        # Tunnel IPs: broker side 10.100.{index}.1, publisher side 10.100.{index}.2
        tunnel_ip_broker = f"10.100.{publisher_index}.1"
        tunnel_ip_publisher = f"10.100.{publisher_index}.2"

        # Compute config hash to detect changes
        current_hash = _publisher_config_hash(pub)
        previous_hash = ns_map.get(ns_name, {}).get("config_hash", "")
        config_changed = current_hash != previous_hash

        # Determine if we need to setup WG/DNS
        need_wg_setup = False

        if ns_name not in existing_namespaces:
            # Create the namespace with veth pair (NEW namespace)
            logger.info(f"Creating namespace for publisher '{pub.get('name', pub_id)}': {ns_name}")
            result = run_cmd([
                str(SCRIPTS_DIR / "create-publisher-ns.sh"),
                pub_id,
                str(publisher_index),
            ], check=True)
            if result.returncode != 0:
                logger.error(f"Failed to create namespace {ns_name}: {result.stderr}")
                continue
            need_wg_setup = True
        elif config_changed:
            # Namespace exists but config changed — re-apply WG/DNS
            logger.info(f"Config changed for publisher '{pub.get('name', pub_id)}' in {ns_name} — updating")
            need_wg_setup = True
        else:
            logger.debug(f"Namespace {ns_name} unchanged — skipping")

        # Don't run WG/DNS setup for disabled publishers (namespace kept for quick re-enable)
        if pub.get("status") == "disabled":
            ns_map[ns_name] = {
                "publisher_id": pub_id,
                "publisher_index": publisher_index,
                "publisher_name": pub.get("name", pub_id[:8]),
                "config_hash": current_hash,
            }
            logger.debug(f"Publisher '{pub.get('name', pub_id)}' is disabled — skipping WG/DNS setup")
            continue

        if need_wg_setup:
            # Create/recreate WireGuard interface
            logger.info(f"Setting up WireGuard in {ns_name} for publisher '{pub.get('name', pub_id)}'")
            wg_args = [
                str(SCRIPTS_DIR / "create-ns-wg.sh"),
                pub_id,
                str(publisher_index),
                pub_key,
                tunnel_ip_broker,
                tunnel_ip_publisher,
                exposed_cidrs_csv,
            ]
            endpoint = pub.get("endpoint", "")
            if endpoint:
                wg_args.append(endpoint)

            result = run_cmd(wg_args, check=True)
            if result.returncode != 0:
                logger.error(f"Failed to create WG in {ns_name}: {result.stderr}")
                # If this was a config-change re-apply (namespace existed), don't skip
                # saving the hash — the existing WG setup may already be correct.
                if ns_name not in existing_namespaces:
                    continue

            # Report namespace connection info to the API so publisher can poll it
            _report_namespace_info(pub_id, ns_name, publisher_index, tunnel_ip_broker)

            # Setup DNS proxy if publisher has DNS zones and a known DNS server
            dns_zones = pub.get("dns_zones") or []
            dns_server = pub.get("dns_server", "")
            if dns_zones and dns_server:
                logger.info(f"Setting up DNS proxy for {ns_name}: port {5300 + publisher_index} → {dns_server}:53")
                result = run_cmd([
                    str(SCRIPTS_DIR / "setup-ns-dns.sh"),
                    pub_id,
                    str(publisher_index),
                    dns_server,
                ], check=True)
                if result.returncode != 0:
                    logger.error(f"Failed to setup DNS proxy for {ns_name}: {result.stderr}")
        else:
            # Namespace unchanged — but ensure DNS socat proxy is still alive
            dns_zones = pub.get("dns_zones") or []
            dns_server = pub.get("dns_server", "")
            if dns_zones and dns_server:
                dns_pid_file = Path(f"/var/run/wireztna/dns-proxy-{publisher_index}.pid")
                socat_alive = False
                if dns_pid_file.exists():
                    try:
                        pid = int(dns_pid_file.read_text().strip())
                        os.kill(pid, 0)  # Check if process exists
                        socat_alive = True
                    except (OSError, ValueError):
                        socat_alive = False
                if not socat_alive:
                    logger.warning(f"DNS proxy for {ns_name} (port {5300 + publisher_index}) not running — restarting")
                    result = run_cmd([
                        str(SCRIPTS_DIR / "setup-ns-dns.sh"),
                        pub_id,
                        str(publisher_index),
                        dns_server,
                    ], check=True)
                    if result.returncode != 0:
                        logger.error(f"Failed to restart DNS proxy for {ns_name}: {result.stderr}")

        # Update namespace map with current hash
        ns_map[ns_name] = {
            "publisher_id": pub_id,
            "publisher_index": publisher_index,
            "publisher_name": pub.get("name", pub_id[:8]),
            "config_hash": current_hash,
        }

    # ─── Remove stale namespaces ───
    desired_ns_names = set(desired_ns_map.keys())
    stale_namespaces = existing_namespaces - desired_ns_names

    for ns_name in stale_namespaces:
        # Look up publisher info from the namespace map
        ns_info = ns_map.get(ns_name)
        if ns_info:
            pub_id = ns_info["publisher_id"]
            publisher_index = ns_info["publisher_index"]
        else:
            # Cannot determine publisher_id/index — skip with warning
            logger.warning(
                f"Stale namespace '{ns_name}' not in namespace map, cannot destroy safely"
            )
            continue

        logger.info(f"Destroying stale namespace: {ns_name} (publisher {pub_id[:8]}...)")

        # Kill DNS proxy for this publisher if running
        dns_pid_file = Path(f"/var/run/wireztna/dns-proxy-{publisher_index}.pid")
        if dns_pid_file.exists():
            try:
                pid = dns_pid_file.read_text().strip()
                run_cmd(["kill", pid])
                dns_pid_file.unlink(missing_ok=True)
                logger.debug(f"Stopped DNS proxy for stale publisher (PID {pid})")
            except (OSError, ValueError):
                pass

        result = run_cmd([
            str(SCRIPTS_DIR / "destroy-publisher-ns.sh"),
            pub_id,
            str(publisher_index),
        ], check=True)
        if result.returncode != 0:
            logger.error(f"Failed to destroy namespace {ns_name}: {result.stderr}")
        else:
            # Remove from map
            ns_map.pop(ns_name, None)

    # Persist updated namespace map
    save_namespace_map(ns_map)

    # ─── Regenerate CoreDNS Corefile for split DNS ───
    _regenerate_corefile(desired_publishers)


# ─── Client reconciliation ───

def reconcile_clients(desired_clients: list) -> bool:
    """Reconcile client peers on wg-clients and report whether every mutation succeeded."""

    if not interface_exists(WG_CLIENTS_IF):
        logger.error(f"{WG_CLIENTS_IF} does not exist")
        return False

    success = True
    current = get_current_peers(WG_CLIENTS_IF)
    desired_map = {c["public_key"]: c for c in desired_clients}

    for pub_key, client in desired_map.items():
        psk = client.get("preshared_key", "")
        allowed_ips = f"{client['overlay_ip']}/32"

        if pub_key not in current:
            logger.info(f"Adding client peer: {client['overlay_ip']}")
            if psk:
                success = wg_set_peer_psk(WG_CLIENTS_IF, pub_key, psk, allowed_ips) and success
            else:
                result = run_cmd(["wg", "set", WG_CLIENTS_IF, "peer", pub_key,
                                  "allowed-ips", allowed_ips], check=True)
                success = result.returncode == 0 and success
        else:
            current_psk = current[pub_key].get("psk", "")
            current_allowed_ips = current[pub_key].get("allowed_ips", "")
            if psk and (psk != current_psk or allowed_ips != current_allowed_ips):
                logger.info(f"Reconciling PSK or AllowedIPs for client: {client['overlay_ip']}")
                success = wg_set_peer_psk(WG_CLIENTS_IF, pub_key, psk, allowed_ips) and success
            elif not psk and allowed_ips != current_allowed_ips:
                result = run_cmd(["wg", "set", WG_CLIENTS_IF, "peer", pub_key,
                                  "allowed-ips", allowed_ips], check=True)
                success = result.returncode == 0 and success

    # Remove stale peers. Every unchanged poll runs this function again, so a
    # transient wg failure cannot be latched behind the desired-config hash.
    for pub_key in current:
        if pub_key not in desired_map:
            logger.info(f"Removing stale client: {pub_key[:8]}...")
            result = run_cmd(["wg", "set", WG_CLIENTS_IF, "peer", pub_key, "remove"], check=True)
            success = result.returncode == 0 and success

    return success


# ─── Firewall enforcement ───

def _resolve_target(target: str, publisher_index: int | None = None) -> str:
    """Resolve a target (IP, CIDR, or FQDN) to an IP/CIDR for nftables.

    - If target is already an IP or CIDR, return as-is
    - If target is an FQDN, resolve via the publisher's DNS proxy (127.0.0.1:5300+index)
    - Falls back to system DNS if publisher_index is not available
    - Returns None if resolution fails (caller should skip the rule)
    """
    import ipaddress as _ipaddress
    import socket as _socket
    import struct as _struct

    # Check if it's already an IP or CIDR
    try:
        _ipaddress.ip_network(target, strict=False)
        return target
    except ValueError:
        pass

    try:
        _ipaddress.ip_address(target)
        return target
    except ValueError:
        pass

    # It's an FQDN — resolve via publisher DNS proxy
    resolved = None
    if publisher_index is not None:
        resolved = _dns_query_via_proxy(target, publisher_index)

    # Fallback to system DNS
    if not resolved:
        try:
            resolved = _socket.gethostbyname(target)
        except _socket.gaierror:
            pass

    if resolved:
        logger.debug(f"Resolved FQDN '{target}' → {resolved} (via publisher index {publisher_index})")
        return resolved
    else:
        logger.warning(f"Cannot resolve FQDN '{target}' for access rule (publisher_index={publisher_index}) — skipping")
        return ""


def _dns_query_via_proxy(fqdn: str, publisher_index: int) -> str | None:
    """Resolve an FQDN using the publisher's socat DNS proxy on 127.0.0.1:5300+index.

    Sends a raw UDP DNS query (A record) and parses the response.
    Returns the first A record IP, or None on failure.
    """
    import socket as _socket
    import struct as _struct

    port = 5300 + publisher_index

    # Build a minimal DNS query for A record
    # Transaction ID
    tx_id = 0x1234
    # Flags: standard query, recursion desired
    flags = 0x0100
    # Questions: 1, Answers: 0, Authority: 0, Additional: 0
    header = _struct.pack("!HHHHHH", tx_id, flags, 1, 0, 0, 0)

    # Encode FQDN as DNS name (length-prefixed labels)
    question = b""
    for label in fqdn.rstrip(".").split("."):
        question += _struct.pack("!B", len(label)) + label.encode()
    question += b"\x00"  # Terminating null
    # QTYPE=A (1), QCLASS=IN (1)
    question += _struct.pack("!HH", 1, 1)

    packet = header + question

    try:
        sock = _socket.socket(_socket.AF_INET, _socket.SOCK_DGRAM)
        sock.settimeout(3)
        sock.sendto(packet, ("127.0.0.1", port))
        response, _ = sock.recvfrom(512)
        sock.close()
    except (_socket.timeout, OSError) as e:
        logger.debug(f"DNS proxy query to port {port} failed for '{fqdn}': {e}")
        return None

    # Parse response — look for A record in answers
    if len(response) < 12:
        return None

    # Check response code (lower 4 bits of flags)
    resp_flags = _struct.unpack("!H", response[2:4])[0]
    rcode = resp_flags & 0x0F
    if rcode != 0:
        return None

    # Number of answers
    ancount = _struct.unpack("!H", response[6:8])[0]
    if ancount == 0:
        return None

    # Skip question section
    offset = 12
    # Skip QNAME
    while offset < len(response):
        length = response[offset]
        if length == 0:
            offset += 1
            break
        if length >= 0xC0:  # Pointer
            offset += 2
            break
        offset += 1 + length
    # Skip QTYPE and QCLASS
    offset += 4

    # Parse answer records
    for _ in range(ancount):
        if offset >= len(response):
            break
        # Skip NAME (may be pointer)
        if response[offset] >= 0xC0:
            offset += 2
        else:
            while offset < len(response) and response[offset] != 0:
                offset += 1 + response[offset]
            offset += 1

        if offset + 10 > len(response):
            break

        rtype, rclass, rttl, rdlength = _struct.unpack("!HHIH", response[offset:offset + 10])
        offset += 10

        if rtype == 1 and rdlength == 4:  # A record
            ip_bytes = response[offset:offset + 4]
            return _socket.inet_ntoa(ip_bytes)

        offset += rdlength

    return None


def reconcile_firewall(desired_clients: list, desired_publishers: list, dns_hints: dict | None = None):
    """Apply nftables + policy routing for transparent per-client access.

    Traffic flow:
    1. Client sends to real IP (e.g., 10.50.1.168)
    2. nftables mangle marks packet with publisher_index based on client→publisher assignment
    3. Policy routing (ip rule fwmark N → table 100+N) routes to correct veth
    4. Namespace forwards to publisher via WG tunnel
    5. Return traffic handled by conntrack

    Rule ordering (longest prefix match + app specificity + DNS hints):
    - Rules arrive pre-sorted from the control plane (least specific → most specific)
    - DNS hints add /32 rules at the very end (most specific, always win)
    - nftables evaluates sequentially; last matching mangle rule wins (mark overwrite)
    - So the most specific rule (DNS hint /32, then app IP+port, then longest prefix) wins
    """
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)

    # Build publisher_id → publisher_index mapping
    pub_index_map = {}  # publisher_id → publisher_index
    for pub in desired_publishers:
        pub_id = pub.get("id", "")
        idx = pub.get("publisher_index")
        status = pub.get("status", "")
        if pub_id and idx is not None and status not in ("disabled", "pending"):
            pub_index_map[pub_id] = idx

    # ─── Policy routing: ip rules + routing tables ───
    # Clean old wireztna rules (fwmark 1-255 → tables 101-355)
    for i in range(1, 256):
        run_cmd(["ip", "rule", "del", "fwmark", str(i), "table", str(100 + i)])

    # Add rules for each active publisher
    for pub_id, idx in pub_index_map.items():
        veth_ns_ip = f"10.252.{idx}.2"
        table_id = 100 + idx
        # Add ip rule: packets with fwmark=idx use table (100+idx)
        run_cmd(["ip", "rule", "add", "fwmark", str(idx), "table", str(table_id)])
        # Add route in that table: default via namespace veth IP
        run_cmd(["ip", "route", "replace", "default", "via", veth_ns_ip, "table", str(table_id)])

    # ─── nftables: mangle + forward ───
    mangle_rules = []
    forward_rules = []

    for client in desired_clients:
        overlay_ip = client["overlay_ip"]
        routing_rules = client.get("routing_rules")
        access_restrictions = client.get("access_restrictions") or {}
        exit_node_pub_id = client.get("exit_node_publisher_id")

        # ─── Exit node mode: full tunnel ───
        # When a client has selected an exit node, ALL traffic routes to that publisher.
        # No per-CIDR rules needed — one mangle + one forward rule covers everything.
        if exit_node_pub_id:
            exit_idx = pub_index_map.get(exit_node_pub_id)
            if exit_idx is not None:
                # Allow DNS to broker overlay (still needed for split DNS)
                forward_rules.append(
                    f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                    f'ip daddr 10.200.0.1 meta l4proto udp th dport 53 accept'
                )
                # Mark ALL traffic from this client with the exit node publisher index
                mangle_rules.append(
                    f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                    f'meta mark set {exit_idx}'
                )
                # Accept ALL forwarded traffic from this client with the exit mark
                forward_rules.append(
                    f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                    f'meta mark {exit_idx} accept'
                )
                continue  # Skip normal per-CIDR rule generation for this client

        # Always allow DNS to broker overlay (needed for split DNS to work)
        # This is implicit and always present regardless of restrictions
        forward_rules.append(
            f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
            f'ip daddr 10.200.0.1 meta l4proto udp th dport 53 accept'
        )

        # Track which publishers are restricted (for forward rule generation)
        restricted_publishers = set(access_restrictions.keys())

        if routing_rules:
            # New path: pre-computed routing_rules with overlap resolution
            # Rules arrive sorted least-specific → most-specific
            # nftables evaluates top-to-bottom, last match wins for mangle (mark overwrite)
            for rule in routing_rules:
                rule_type = rule.get("type", "cidr")
                target = rule.get("target", "")
                port = rule.get("port", 0)
                protocol = rule.get("protocol", "any")
                idx = rule.get("publisher_index")
                publisher_id = rule.get("publisher_id", "")

                if idx is None or not target:
                    continue

                # MANGLE rules always apply (routing is independent of restrictions)
                if rule_type == "app":
                    if protocol == "icmp":
                        mangle_rules.append(
                            f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                            f'ip daddr {target} ip protocol icmp meta mark set {idx}'
                        )
                    elif port > 0:
                        proto_match = ""
                        if protocol in ("tcp", "udp"):
                            proto_match = f"meta l4proto {protocol} "
                        mangle_rules.append(
                            f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                            f'ip daddr {target} {proto_match}th dport {port} meta mark set {idx}'
                        )
                    else:
                        mangle_rules.append(
                            f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                            f'ip daddr {target} meta mark set {idx}'
                        )
                else:
                    # CIDR mangle rule
                    mangle_rules.append(
                        f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                        f'ip daddr {target} meta mark set {idx}'
                    )

                # FORWARD rules: depend on whether this publisher is restricted
                if publisher_id in restricted_publishers:
                    # Restricted publisher: DON'T add broad CIDR accept here.
                    # Per-app forward rules are added below from access_restrictions.
                    pass
                else:
                    # Unrestricted: add forward accept scoped by mark (prevents overlap issues)
                    if rule_type == "app":
                        if protocol == "icmp":
                            forward_rules.append(
                                f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                                f'meta mark {idx} ip daddr {target} ip protocol icmp accept'
                            )
                        elif port > 0:
                            proto_match = ""
                            if protocol in ("tcp", "udp"):
                                proto_match = f"meta l4proto {protocol} "
                            forward_rules.append(
                                f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                                f'meta mark {idx} ip daddr {target} {proto_match}th dport {port} accept'
                            )
                        else:
                            forward_rules.append(
                                f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                                f'meta mark {idx} ip daddr {target} accept'
                            )
                    else:
                        forward_rules.append(
                            f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                            f'meta mark {idx} ip daddr {target} accept'
                        )

        elif client.get("publisher_cidrs"):
            # Legacy fallback: publisher_cidrs without ordering
            publisher_cidrs = client["publisher_cidrs"]
            for pub_id, cidrs in publisher_cidrs.items():
                idx = pub_index_map.get(pub_id)
                if idx is None:
                    continue
                for cidr in cidrs:
                    mangle_rules.append(
                        f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                        f'ip daddr {cidr} meta mark set {idx}'
                    )
                    if pub_id not in restricted_publishers:
                        forward_rules.append(
                            f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                            f'meta mark {idx} ip daddr {cidr} accept'
                        )
        else:
            # No routing info — allow all allowed_cidrs (no marking)
            for cidr in client.get("allowed_cidrs", []):
                forward_rules.append(
                    f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                    f'ip daddr {cidr} accept'
                )

        # ─── Restricted publisher forward rules (per-app) ───
        # For publishers with access_restrictions, add explicit per-app accept rules
        # Scoped by meta mark to prevent overlap with other publishers sharing same CIDR
        for pub_id, rules in access_restrictions.items():
            idx = pub_index_map.get(pub_id)
            if idx is None:
                continue
            for app_rule in rules:
                target = app_rule.get("target", "")
                port = app_rule.get("port", "0")
                protocol = app_rule.get("protocol", "tcp")

                if not target:
                    continue

                # Resolve FQDN targets to IP using publisher's DNS proxy
                resolved_target = _resolve_target(target, publisher_index=idx)

                if not resolved_target:
                    continue  # Skip unresolvable targets
                if protocol == "icmp":
                    forward_rules.append(
                        f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                        f'meta mark {idx} ip daddr {resolved_target} ip protocol icmp accept'
                    )
                elif port and port != "0":
                    proto_match = ""
                    if protocol in ("tcp", "udp"):
                        proto_match = f"meta l4proto {protocol} "
                    forward_rules.append(
                        f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                        f'meta mark {idx} ip daddr {resolved_target} {proto_match}th dport {port} accept'
                    )
                else:
                    # Port 0 = any port (all traffic to target)
                    forward_rules.append(
                        f'        iifname "{WG_CLIENTS_IF}" ip saddr {overlay_ip} '
                        f'meta mark {idx} ip daddr {resolved_target} accept'
                    )

    # ─── DNS routing hints: /32 rules (most specific, always win) ───
    # These are generated when the DNS proxy resolves a domain through a specific
    # publisher, recording which publisher "owns" a resolved IP for a given client.
    # NOTE: For restricted publishers, DNS hints only create mangle (routing) rules,
    # NOT forward (firewall) rules — forward is handled by the per-app access_restrictions.
    if dns_hints:
        # Build lookup: client_ip → set of restricted publisher indices
        restricted_idx_by_client: dict[str, set[int]] = {}
        for client in desired_clients:
            restrictions = client.get("access_restrictions") or {}
            if restrictions:
                client_ip = client["overlay_ip"]
                restricted_idx_by_client[client_ip] = set()
                for pub_id in restrictions:
                    idx = pub_index_map.get(pub_id)
                    if idx is not None:
                        restricted_idx_by_client[client_ip].add(idx)

        for client_ip, hints in dns_hints.items():
            restricted_indices = restricted_idx_by_client.get(client_ip, set())
            for hint in hints:
                ip = hint.get("ip", "")
                idx = hint.get("publisher_index")
                if not ip or idx is None:
                    continue
                # Mangle (routing) always applies — traffic needs to reach the right namespace
                mangle_rules.append(
                    f'        iifname "{WG_CLIENTS_IF}" ip saddr {client_ip} '
                    f'ip daddr {ip}/32 meta mark set {idx}'
                )
                # Forward (firewall) only for unrestricted publishers
                # Restricted publishers have explicit per-app rules already
                if idx not in restricted_indices:
                    forward_rules.append(
                        f'        iifname "{WG_CLIENTS_IF}" ip saddr {client_ip} '
                        f'meta mark {idx} ip daddr {ip}/32 accept'
                    )

    mangle_block = "\n".join(mangle_rules) if mangle_rules else "        # No mangle rules"
    forward_block = "\n".join(forward_rules) if forward_rules else "        # No client policies defined"

    nft_rules = f"""table inet wireztna {{
    chain prerouting {{
        type filter hook prerouting priority mangle; policy accept;

        # Mark packets for policy routing to correct publisher namespace
        # Rules ordered: least specific first → most specific last (last match wins)
{mangle_block}
    }}

    chain forward {{
        type filter hook forward priority filter; policy drop;

        # Allow established/related (handles return traffic)
        ct state established,related accept

        # Per-client access rules (client → publisher real CIDRs)
{forward_block}

        # Return traffic from publisher namespaces to clients
        iifname "v*-h" oifname "{WG_CLIENTS_IF}" accept

        # Log and drop everything else
        log prefix "wireztna-deny: " drop
    }}
}}
"""

    rules_file = CONFIG_DIR / "nftables-wireztna.nft"
    rules_file.write_text(nft_rules)

    # Apply atomically: delete old table then load new ruleset
    # Only apply if rules actually changed (avoids micro-cuts on every cycle)
    rules_hash = hashlib.md5(nft_rules.encode()).hexdigest()
    rules_hash_file = CONFIG_DIR / ".nftables-hash"
    previous_rules_hash = ""
    if rules_hash_file.exists():
        try:
            previous_rules_hash = rules_hash_file.read_text().strip()
        except OSError:
            pass

    if rules_hash == previous_rules_hash:
        return  # Rules unchanged — skip nft apply

    run_cmd(["nft", "delete", "table", "inet", "wireztna"])
    result = run_cmd(["nft", "-f", str(rules_file)], check=True)
    if result.returncode == 0:
        rules_hash_file.write_text(rules_hash)
        logger.info(f"nftables applied: {len(mangle_rules)} mangle + {len(forward_rules)} forward rules")
    else:
        logger.error(f"nftables failed: {result.stderr}")


# ─── DNS Proxy Configuration ───

DNS_PROXY_CONFIG_FILE = CONFIG_DIR / "dns-proxy.json"


def _write_dns_proxy_config(desired_clients: list, desired_publishers: list):
    """Generate the DNS proxy config file mapping clients → zones → publishers.

    The DNS proxy reads this to know, for each client (by overlay IP), which
    DNS zones should be forwarded to which publisher's socat proxy port.

    Output format:
    {
      "clients": {
        "10.200.1.0": {
          "zones": {
            "compute.internal": {"publisher_index": 1, "proxy_port": 5301},
            "office.internal": {"publisher_index": 2, "proxy_port": 5302}
          }
        }
      },
      "default_dns": ["8.8.8.8", "1.1.1.1"]
    }
    """
    # Build publisher_id → {dns_zones, publisher_index} lookup
    pub_dns_map = {}  # publisher_id → {zones: [...], index: N}
    for pub in desired_publishers:
        pub_id = pub.get("id", "")
        dns_zones = pub.get("dns_zones") or []
        dns_server = pub.get("dns_server", "")
        publisher_index = pub.get("publisher_index")

        if pub_id and dns_zones and dns_server and publisher_index is not None:
            pub_dns_map[pub_id] = {
                "zones": dns_zones,
                "index": publisher_index,
            }

    if not pub_dns_map:
        # No publishers with DNS configured — write minimal config
        config = {"clients": {}, "default_dns": ["8.8.8.8", "1.1.1.1"]}
        _write_config_if_changed(config)
        return

    # Build per-client zone mapping
    clients_config = {}

    for client in desired_clients:
        overlay_ip = client.get("overlay_ip", "")
        allowed_publishers = client.get("allowed_publishers", [])

        if not overlay_ip:
            continue

        # Collect all DNS zones this client can access
        client_zones = {}
        for pub_id in allowed_publishers:
            pub_dns = pub_dns_map.get(pub_id)
            if not pub_dns:
                continue
            for zone in pub_dns["zones"]:
                # If multiple publishers serve the same zone for this client,
                # the first one wins (could later use priority)
                if zone not in client_zones:
                    client_zones[zone] = {
                        "publisher_index": pub_dns["index"],
                        "proxy_port": 5300 + pub_dns["index"],
                    }

        if client_zones:
            clients_config[overlay_ip] = {"zones": client_zones}

    # Build publishers section: publisher_index → exposed_cidrs
    # Used by DNS proxy to attribute resolved IPs to the correct publisher
    # (solves CNAME chains crossing publishers with overlapping zones like compute.internal)
    publishers_config = {}
    for pub in desired_publishers:
        pub_index = pub.get("publisher_index")
        exposed_cidrs = pub.get("exposed_cidrs") or []
        if pub_index is not None and exposed_cidrs:
            publishers_config[str(pub_index)] = {
                "exposed_cidrs": exposed_cidrs,
            }

    config = {
        "clients": clients_config,
        "publishers": publishers_config,
        "default_dns": ["8.8.8.8", "1.1.1.1"],
    }

    _write_config_if_changed(config)


def _write_config_if_changed(config: dict):
    """Write DNS proxy config only if it changed (avoids unnecessary disk writes)."""
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    new_content = json.dumps(config, indent=2, sort_keys=True)

    if DNS_PROXY_CONFIG_FILE.exists():
        existing = DNS_PROXY_CONFIG_FILE.read_text()
        if existing == new_content:
            return

    DNS_PROXY_CONFIG_FILE.write_text(new_content)
    clients_count = len(config.get("clients", {}))
    zones_count = sum(
        len(c.get("zones", {}))
        for c in config.get("clients", {}).values()
    )
    logger.info(f"DNS proxy config written: {clients_count} clients, {zones_count} zone mappings")


# ─── Main ───

def fetch_desired_config() -> dict | None:
    url = f"{CONTROL_PLANE_URL}/api/v1/brokers/{BROKER_ID}/config"
    headers = {"X-API-Key": BROKER_API_KEY}
    try:
        response = httpx.get(url, headers=headers, timeout=10)
        if response.status_code == 200:
            return response.json()
        else:
            logger.error(f"Control plane returned {response.status_code}: {response.text}")
            return None
    except httpx.RequestError as e:
        logger.error(f"Cannot reach control plane: {e}")
        return None


def reconcile(config: dict) -> bool:
    desired_publishers = config.get("publishers", [])
    desired_clients = config.get("clients", [])
    dns_hints = config.get("dns_hints")

    reconcile_publishers(desired_publishers)
    clients_reconciled = reconcile_clients(desired_clients)
    reconcile_firewall(desired_clients, desired_publishers, dns_hints)
    _write_dns_proxy_config(desired_clients, desired_publishers)

    if clients_reconciled:
        logger.info("Reconciliation complete")
    else:
        logger.warning("Reconciliation incomplete; client peers will be retried")
    return clients_reconciled


def ensure_dns_proxies_alive(config: dict):
    """Check that socat DNS proxies are running for all publishers with DNS zones.

    This runs on every poll cycle (even without config changes) to recover
    from socat crashes or OOM kills without waiting for a config change.
    """
    publishers = config.get("publishers", [])
    ns_map = load_namespace_map()

    for pub in publishers:
        if pub.get("status") == "disabled":
            continue
        dns_zones = pub.get("dns_zones") or []
        dns_server = pub.get("dns_server", "")
        if not dns_zones or not dns_server:
            continue

        pub_id = pub["id"]
        publisher_index = pub["publisher_index"]
        id_short = pub_id[:8]
        ns_name = f"ns-{id_short}"

        # Only check if namespace exists in our map (meaning it was successfully created)
        if ns_name not in ns_map:
            continue

        dns_pid_file = Path(f"/var/run/wireztna/dns-proxy-{publisher_index}.pid")
        socat_alive = False
        if dns_pid_file.exists():
            try:
                pid = int(dns_pid_file.read_text().strip())
                os.kill(pid, 0)
                socat_alive = True
            except (OSError, ValueError):
                socat_alive = False

        if not socat_alive:
            logger.warning(f"DNS proxy for {ns_name} (port {5300 + publisher_index}) not running — restarting")
            result = run_cmd([
                str(SCRIPTS_DIR / "setup-ns-dns.sh"),
                pub_id,
                str(publisher_index),
                dns_server,
            ], check=True)
            if result.returncode != 0:
                logger.error(f"Failed to restart DNS proxy for {ns_name}: {result.stderr}")


def main():
    logger.info("WireZTNA Broker Agent starting")
    logger.info(f"  Control Plane: {CONTROL_PLANE_URL}")
    logger.info(f"  Broker ID: {BROKER_ID}")
    logger.info(f"  Poll Interval: {POLL_INTERVAL}s")

    last_config_hash = None

    while True:
        config = fetch_desired_config()
        if config:
            config_hash = hash(json.dumps(config, sort_keys=True))
            if config_hash != last_config_hash:
                logger.info("Configuration changed, reconciling...")
                if reconcile(config):
                    last_config_hash = config_hash
            else:
                # Peer convergence is checked on every cycle. A transient wg set
                # failure must never be hidden by an unchanged desired-state hash.
                desired_publishers = config.get("publishers", [])
                desired_clients = config.get("clients", [])
                dns_hints = config.get("dns_hints")
                reconcile_clients(desired_clients)
                reconcile_firewall(desired_clients, desired_publishers, dns_hints)

                # Also check DNS proxy health
                ensure_dns_proxies_alive(config)

        time.sleep(POLL_INTERVAL)


if __name__ == "__main__":
    main()
