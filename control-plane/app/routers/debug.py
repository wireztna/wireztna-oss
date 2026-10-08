"""Debug panel — real-time broker state, logs, and network diagnostics.

All endpoints require admin authentication. Executes commands on the broker
host and returns structured results for the UI debug panel.
"""

import subprocess
import re
import time
import json
from pathlib import Path
from typing import Optional

from fastapi import APIRouter, Depends, Query
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.database import get_db
from app.services.auth_service import require_super_admin
from app.models.user import User
from app.models.publisher import Publisher

router = APIRouter()


def _run(cmd: list[str], timeout: int = 10) -> str:
    """Execute a command and return stdout. Returns error message on failure."""
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return result.stdout.strip() if result.returncode == 0 else f"ERROR: {result.stderr.strip()}"
    except subprocess.TimeoutExpired:
        return "ERROR: Command timed out"
    except FileNotFoundError:
        return f"ERROR: Command not found: {cmd[0]}"


# ─── Firewall Rules ───

@router.get("/nftables")
async def get_nftables(_=Depends(require_super_admin)):
    """Get current nftables rules applied by the reconciler.

    Returns the full wireztna table with mangle (routing marks) and forward (firewall) chains.
    Optionally filter by a client overlay IP.
    """
    raw = _run(["nft", "list", "table", "inet", "wireztna"])
    if raw.startswith("ERROR"):
        return {"raw": raw, "mangle_rules": [], "forward_rules": []}

    # Parse into structured rules
    mangle_rules = []
    forward_rules = []
    current_chain = None

    for line in raw.split("\n"):
        stripped = line.strip()
        if "chain prerouting" in stripped:
            current_chain = "mangle"
        elif "chain forward" in stripped:
            current_chain = "forward"
        elif stripped.startswith("}"):
            current_chain = None
        elif current_chain and stripped and not stripped.startswith("#") and not stripped.startswith("type ") and not stripped.startswith("policy "):
            # Extract client IP if present
            client_ip = ""
            dst = ""
            mark = ""
            action = ""

            saddr_match = re.search(r'ip saddr ([\d.]+)', stripped)
            daddr_match = re.search(r'ip daddr ([\d./]+)', stripped)
            mark_set_match = re.search(r'meta mark set (?:0x0*)?(\d+)', stripped)
            mark_match = re.search(r'meta mark (?:0x0*)?(\d+)', stripped)

            if saddr_match:
                client_ip = saddr_match.group(1)
            if daddr_match:
                dst = daddr_match.group(1)
            if mark_set_match:
                mark = mark_set_match.group(1)
            if mark_match and not mark_set_match:
                mark = mark_match.group(1)

            if "accept" in stripped:
                action = "accept"
            elif "drop" in stripped:
                action = "drop"
            elif "meta mark set" in stripped:
                action = f"mark → {mark}"

            rule = {
                "raw": stripped,
                "client_ip": client_ip,
                "destination": dst,
                "mark": mark,
                "action": action,
            }

            if current_chain == "mangle":
                mangle_rules.append(rule)
            elif current_chain == "forward":
                forward_rules.append(rule)

    return {
        "raw": raw,
        "mangle_rules": mangle_rules,
        "forward_rules": forward_rules,
        "total_mangle": len(mangle_rules),
        "total_forward": len(forward_rules),
    }


# ─── WireGuard Peers ───

@router.get("/wireguard")
async def get_wireguard_status(_=Depends(require_super_admin)):
    """Get WireGuard peer status for wg-clients and all publisher namespaces."""

    # Client peers
    clients_raw = _run(["wg", "show", "wg-clients", "dump"])
    client_peers = []
    if not clients_raw.startswith("ERROR"):
        for line in clients_raw.strip().split("\n")[1:]:  # Skip header
            parts = line.split("\t")
            if len(parts) >= 8:
                handshake_ts = int(parts[4]) if parts[4] != "0" else 0
                handshake_age = int(time.time()) - handshake_ts if handshake_ts else None
                client_peers.append({
                    "public_key": parts[0][:12] + "...",
                    "endpoint": parts[2] if parts[2] != "(none)" else None,
                    "allowed_ips": parts[3],
                    "handshake_age_seconds": handshake_age,
                    "rx_bytes": int(parts[5]),
                    "tx_bytes": int(parts[6]),
                    "healthy": handshake_age is not None and handshake_age < 180,
                })

    # Publisher namespaces
    namespaces_raw = _run(["ip", "netns", "list"])
    publisher_peers = []
    if not namespaces_raw.startswith("ERROR"):
        for line in namespaces_raw.strip().split("\n"):
            ns_name = line.split()[0] if line.strip() else ""
            if not ns_name.startswith("ns-"):
                continue
            wg_iface = f"wg-{ns_name[3:9]}"
            ns_wg = _run(["ip", "netns", "exec", ns_name, "wg", "show", wg_iface, "dump"])
            if ns_wg.startswith("ERROR"):
                publisher_peers.append({"namespace": ns_name, "interface": wg_iface, "error": ns_wg})
                continue
            for peer_line in ns_wg.strip().split("\n")[1:]:
                parts = peer_line.split("\t")
                if len(parts) >= 8:
                    handshake_ts = int(parts[4]) if parts[4] != "0" else 0
                    handshake_age = int(time.time()) - handshake_ts if handshake_ts else None
                    publisher_peers.append({
                        "namespace": ns_name,
                        "interface": wg_iface,
                        "peer_key": parts[0][:12] + "...",
                        "endpoint": parts[2] if parts[2] != "(none)" else None,
                        "allowed_ips": parts[3],
                        "handshake_age_seconds": handshake_age,
                        "rx_bytes": int(parts[5]),
                        "tx_bytes": int(parts[6]),
                        "healthy": handshake_age is not None and handshake_age < 180,
                    })

    return {
        "client_peers": client_peers,
        "publisher_peers": publisher_peers,
        "total_clients": len(client_peers),
        "total_publishers": len(publisher_peers),
    }


# ─── Enriched Tunnels (combined WireGuard + Namespaces with name mapping) ───

@router.get("/tunnels")
async def get_tunnels_enriched(
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """Combined WireGuard + Namespace view with names mapped from public keys.

    Returns client peers with username/overlay IP and publisher tunnels with
    publisher name, namespace status, routes, and WG state — all in one call.
    """

    # ─── Build lookup maps from DB ───
    # public_key → {username, overlay_ip}
    users_result = await db.execute(
        select(User.public_key, User.username, User.overlay_ip).where(User.public_key.isnot(None))
    )
    key_to_user = {}
    for row in users_result.all():
        if row.public_key:
            key_to_user[row.public_key] = {"username": row.username, "overlay_ip": row.overlay_ip}

    # publisher_id → {name, status, exposed_cidrs, exit_node, location}
    pubs_result = await db.execute(
        select(Publisher.id, Publisher.name, Publisher.status, Publisher.exposed_cidrs,
               Publisher.exit_node, Publisher.location, Publisher.publisher_index, Publisher.endpoint)
    )
    id_to_pub = {}
    for row in pubs_result.all():
        id_to_pub[row.id] = {
            "name": row.name,
            "status": row.status,
            "exposed_cidrs": row.exposed_cidrs or [],
            "exit_node": row.exit_node,
            "location": row.location,
            "publisher_index": row.publisher_index,
            "endpoint": row.endpoint,
        }

    # ─── Client peers (wg-clients interface) ───
    clients_raw = _run(["wg", "show", "wg-clients", "dump"])
    client_peers = []
    if not clients_raw.startswith("ERROR"):
        for line in clients_raw.strip().split("\n")[1:]:
            parts = line.split("\t")
            if len(parts) >= 8:
                full_key = parts[0]
                handshake_ts = int(parts[4]) if parts[4] != "0" else 0
                handshake_age = int(time.time()) - handshake_ts if handshake_ts else None

                # Map key to user
                user_info = key_to_user.get(full_key, {})

                client_peers.append({
                    "username": user_info.get("username"),
                    "overlay_ip": user_info.get("overlay_ip") or parts[3],
                    "public_key_short": full_key[:12] + "...",
                    "endpoint": parts[2] if parts[2] != "(none)" else None,
                    "handshake_age_seconds": handshake_age,
                    "rx_bytes": int(parts[5]),
                    "tx_bytes": int(parts[6]),
                    "healthy": handshake_age is not None and handshake_age < 180,
                })

    # ─── Publisher tunnels (namespaces) ───
    ns_map_file = Path("/etc/wireztna/namespace-map.json")
    ns_map = {}
    if ns_map_file.exists():
        try:
            ns_map = json.loads(ns_map_file.read_text())
        except Exception:
            pass

    namespaces_raw = _run(["ip", "netns", "list"])
    publisher_tunnels = []
    if not namespaces_raw.startswith("ERROR"):
        for line in namespaces_raw.strip().split("\n"):
            ns_name = line.split()[0] if line.strip() else ""
            if not ns_name.startswith("ns-"):
                continue

            ns_info = ns_map.get(ns_name, {})
            pub_id = ns_info.get("publisher_id", "")
            pub_info = id_to_pub.get(pub_id, {})
            wg_iface = f"wg-{ns_name[3:9]}"
            veth_host = f"v{ns_name[3:9]}-h"

            # WG status in namespace
            ns_wg = _run(["ip", "netns", "exec", ns_name, "wg", "show", wg_iface, "dump"])
            peer_data = None
            if not ns_wg.startswith("ERROR"):
                for peer_line in ns_wg.strip().split("\n")[1:]:
                    parts = peer_line.split("\t")
                    if len(parts) >= 8:
                        handshake_ts = int(parts[4]) if parts[4] != "0" else 0
                        handshake_age = int(time.time()) - handshake_ts if handshake_ts else None
                        peer_data = {
                            "endpoint": parts[2] if parts[2] != "(none)" else None,
                            "handshake_age_seconds": handshake_age,
                            "rx_bytes": int(parts[5]),
                            "tx_bytes": int(parts[6]),
                            "healthy": handshake_age is not None and handshake_age < 150,
                        }
                        break  # One peer per namespace

            # Routes in namespace
            routes_raw = _run(["ip", "netns", "exec", ns_name, "ip", "route"])
            routes = [r.strip() for r in routes_raw.split("\n") if r.strip()] if not routes_raw.startswith("ERROR") else []

            # Listen port
            port_file = Path(f"/etc/wireguard/{wg_iface}.port")
            listen_port = None
            if port_file.exists():
                try:
                    listen_port = int(port_file.read_text().strip())
                except Exception:
                    pass

            # Veth active check
            veth_check = _run(["ip", "link", "show", veth_host])
            veth_active = not veth_check.startswith("ERROR")

            publisher_tunnels.append({
                "publisher_name": pub_info.get("name") or f"unknown ({pub_id[:8]}...)" if pub_id else "unknown",
                "publisher_id": pub_id,
                "publisher_status": pub_info.get("status"),
                "publisher_index": pub_info.get("publisher_index") or ns_info.get("publisher_index"),
                "exit_node": pub_info.get("exit_node", False),
                "location": pub_info.get("location"),
                "exposed_cidrs": pub_info.get("exposed_cidrs", []),
                "public_endpoint": pub_info.get("endpoint"),
                "namespace": ns_name,
                "wg_interface": wg_iface,
                "listen_port": listen_port,
                "veth_host": veth_host,
                "veth_active": veth_active,
                "config_hash": ns_info.get("config_hash", ""),
                "peer": peer_data,
                "routes": routes,
            })

    return {
        "client_peers": client_peers,
        "publisher_tunnels": publisher_tunnels,
        "total_clients": len(client_peers),
        "total_publishers": len(publisher_tunnels),
    }


# ─── Namespaces ───

@router.get("/namespaces")
async def get_namespaces(_=Depends(require_super_admin)):
    """List all publisher network namespaces with their WG interface status."""

    namespaces_raw = _run(["ip", "netns", "list"])
    ns_map_file = Path("/etc/wireztna/namespace-map.json")
    ns_map = {}
    if ns_map_file.exists():
        try:
            ns_map = json.loads(ns_map_file.read_text())
        except Exception:
            pass

    namespaces = []
    for line in (namespaces_raw or "").strip().split("\n"):
        if not line.strip():
            continue
        ns_name = line.split()[0]
        if not ns_name.startswith("ns-"):
            continue

        info = ns_map.get(ns_name, {})
        wg_iface = f"wg-{ns_name[3:9]}"

        # Get WG summary
        wg_info = _run(["ip", "netns", "exec", ns_name, "wg", "show", wg_iface])
        listen_port = ""
        pub_key = ""
        for wg_line in wg_info.split("\n"):
            if "listening port:" in wg_line:
                listen_port = wg_line.split(":")[-1].strip()
            if "public key:" in wg_line:
                pub_key = wg_line.split(":")[-1].strip()[:16] + "..."

        # Check veth
        veth_name = f"v{ns_name[3:9]}-h"
        veth_exists = _run(["ip", "link", "show", veth_name])

        namespaces.append({
            "name": ns_name,
            "publisher_id": info.get("publisher_id", "unknown"),
            "publisher_index": info.get("publisher_index"),
            "config_hash": info.get("config_hash", ""),
            "wg_interface": wg_iface,
            "wg_public_key": pub_key,
            "listen_port": listen_port,
            "veth_active": not veth_exists.startswith("ERROR"),
        })

    return {"namespaces": namespaces, "total": len(namespaces)}


# ─── Policy Routing ───

@router.get("/routing")
async def get_routing(_=Depends(require_super_admin)):
    """Get policy routing rules and tables used by WireZTNA."""
    ip_rules = _run(["ip", "rule", "list"])
    wireztna_rules = [l for l in ip_rules.split("\n") if "fwmark" in l]

    tables = []
    for rule in wireztna_rules:
        # Extract table number
        table_match = re.search(r'lookup (\d+)', rule)
        mark_match = re.search(r'fwmark (?:0x)?(\w+)', rule)
        if table_match:
            table_id = table_match.group(1)
            route = _run(["ip", "route", "show", "table", table_id])
            tables.append({
                "rule": rule.strip(),
                "table_id": table_id,
                "fwmark": mark_match.group(1) if mark_match else "",
                "routes": route.strip(),
            })

    return {"rules": wireztna_rules, "tables": tables, "total_rules": len(wireztna_rules)}


# ─── Logs ───

@router.get("/logs")
async def get_service_logs(
    service: str = Query(default="wireztna-reconciler", description="Service name"),
    lines: int = Query(default=50, le=200),
    _=Depends(require_super_admin),
):
    """Get recent logs from a WireZTNA systemd service.

    Allowed services: wireztna-api, wireztna-reconciler, wireztna-health, wireztna-dns, wireztna-ui, nginx
    """
    allowed_services = {"wireztna-api", "wireztna-reconciler", "wireztna-health", "wireztna-dns", "wireztna-ui", "nginx"}
    if service not in allowed_services:
        return {"error": f"Service '{service}' not allowed. Use one of: {sorted(allowed_services)}"}

    raw = _run(["journalctl", "-u", service, "-n", str(lines), "--no-pager", "--output", "short-iso"])

    log_lines = []
    for line in raw.split("\n"):
        if not line.strip():
            continue
        # Parse journalctl short-iso format: "2026-07-20T12:00:00+0000 host service[pid]: message"
        level = "info"
        if "[ERROR]" in line or "ERROR" in line:
            level = "error"
        elif "[WARNING]" in line or "[WARN]" in line:
            level = "warning"
        elif "[DEBUG]" in line:
            level = "debug"
        log_lines.append({"line": line, "level": level})

    return {
        "service": service,
        "lines": log_lines,
        "total": len(log_lines),
    }


# ─── DNS Proxy Status ───

@router.get("/dns-proxy")
async def get_dns_proxy_status(_=Depends(require_super_admin)):
    """Get the current DNS proxy configuration and socat proxy status."""

    config_file = Path("/etc/wireztna/dns-proxy.json")
    config = {}
    if config_file.exists():
        try:
            config = json.loads(config_file.read_text())
        except Exception:
            pass

    # Check socat processes
    socat_status = _run(["pgrep", "-a", "socat"])
    socat_procs = [l for l in socat_status.split("\n") if l.strip()] if not socat_status.startswith("ERROR") else []

    return {
        "config": config,
        "socat_processes": len(socat_procs),
        "socat_details": socat_procs[:10],
    }


# ─── System Metrics ───

@router.get("/system-metrics")
async def get_system_metrics(_=Depends(require_super_admin)):
    """Get broker host resource metrics: CPU, RAM, disk, conntrack, WG peers, services.

    Returns a comprehensive view of the broker's resource usage and capacity.
    Used by the Dashboard health bar and the Debug → System tab.
    """
    from app.services.metrics_service import collect_system_metrics
    return collect_system_metrics()
