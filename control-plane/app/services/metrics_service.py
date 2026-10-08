"""Broker host metrics collection — CPU, RAM, disk, conntrack, WG peers, services.

All data comes from standard Linux /proc filesystem and subprocess calls.
No external dependencies required. Works on Amazon Linux, Ubuntu, Debian.
"""

import os
import subprocess
import time
from datetime import datetime
from pathlib import Path
from typing import Optional


def _run(cmd: list[str], timeout: int = 5) -> Optional[str]:
    """Execute a command, return stdout or None on failure."""
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        return result.stdout.strip() if result.returncode == 0 else None
    except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
        return None


def get_cpu_metrics() -> dict:
    """Get CPU load averages and core count from /proc."""
    load_avg = {"load_1": 0.0, "load_5": 0.0, "load_15": 0.0, "cores": 1, "usage_percent": 0.0}

    try:
        with open("/proc/loadavg") as f:
            parts = f.read().split()
            load_avg["load_1"] = float(parts[0])
            load_avg["load_5"] = float(parts[1])
            load_avg["load_15"] = float(parts[2])
    except (OSError, IndexError, ValueError):
        pass

    try:
        with open("/proc/cpuinfo") as f:
            cores = sum(1 for line in f if line.startswith("processor"))
            load_avg["cores"] = max(cores, 1)
    except OSError:
        pass

    # Estimate usage as load_1 / cores * 100 (capped at 100)
    load_avg["usage_percent"] = round(min(load_avg["load_1"] / load_avg["cores"] * 100, 100), 1)

    return load_avg


def get_memory_metrics() -> dict:
    """Get memory stats from /proc/meminfo."""
    mem = {
        "total_mb": 0, "used_mb": 0, "available_mb": 0,
        "usage_percent": 0.0, "swap_total_mb": 0, "swap_used_mb": 0,
    }

    try:
        info = {}
        with open("/proc/meminfo") as f:
            for line in f:
                parts = line.split()
                if len(parts) >= 2:
                    key = parts[0].rstrip(":")
                    info[key] = int(parts[1])  # kB

        total = info.get("MemTotal", 0)
        available = info.get("MemAvailable", 0)
        swap_total = info.get("SwapTotal", 0)
        swap_free = info.get("SwapFree", 0)

        mem["total_mb"] = round(total / 1024)
        mem["available_mb"] = round(available / 1024)
        mem["used_mb"] = mem["total_mb"] - mem["available_mb"]
        mem["usage_percent"] = round((1 - available / total) * 100, 1) if total > 0 else 0.0
        mem["swap_total_mb"] = round(swap_total / 1024)
        mem["swap_used_mb"] = round((swap_total - swap_free) / 1024)
    except (OSError, KeyError, ValueError, ZeroDivisionError):
        pass

    return mem


def get_disk_metrics() -> dict:
    """Get disk usage for root and DB file."""
    disk = {"root_total_gb": 0, "root_used_gb": 0, "root_usage_percent": 0.0, "db_size_mb": 0.0}

    try:
        stat = os.statvfs("/")
        total = stat.f_blocks * stat.f_frsize
        used = (stat.f_blocks - stat.f_bfree) * stat.f_frsize
        disk["root_total_gb"] = round(total / (1024**3), 1)
        disk["root_used_gb"] = round(used / (1024**3), 1)
        disk["root_usage_percent"] = round(used / total * 100, 1) if total > 0 else 0.0
    except OSError:
        pass

    # DB size
    db_paths = [
        Path("/opt/wireztna/data/wireztna.db"),
        Path("data/wireztna.db"),
    ]
    for db_path in db_paths:
        if db_path.exists():
            disk["db_size_mb"] = round(db_path.stat().st_size / (1024**2), 2)
            break

    return disk


def get_conntrack_metrics() -> dict:
    """Get conntrack table usage from /proc/sys."""
    ct = {"current": 0, "max": 65536, "usage_percent": 0.0}

    try:
        count_path = Path("/proc/sys/net/netfilter/nf_conntrack_count")
        max_path = Path("/proc/sys/net/netfilter/nf_conntrack_max")

        if count_path.exists():
            ct["current"] = int(count_path.read_text().strip())
        if max_path.exists():
            ct["max"] = int(max_path.read_text().strip())

        ct["usage_percent"] = round(ct["current"] / ct["max"] * 100, 1) if ct["max"] > 0 else 0.0
    except (OSError, ValueError):
        pass

    return ct


def get_wireguard_metrics() -> dict:
    """Get WireGuard peer counts."""
    wg = {
        "client_peers": 0, "publisher_namespaces": 0,
        "publisher_peers_total": 0,
        "wg_clients_rx_bytes": 0, "wg_clients_tx_bytes": 0,
    }

    # Client peers on wg-clients
    dump = _run(["wg", "show", "wg-clients", "dump"])
    if dump:
        lines = dump.strip().split("\n")[1:]  # Skip interface line
        wg["client_peers"] = len(lines)
        for line in lines:
            parts = line.split("\t")
            if len(parts) >= 7:
                wg["wg_clients_rx_bytes"] += int(parts[5])
                wg["wg_clients_tx_bytes"] += int(parts[6])

    # Publisher namespaces
    ns_list = _run(["ip", "netns", "list"])
    if ns_list:
        ns_names = [l.split()[0] for l in ns_list.split("\n") if l.strip() and l.split()[0].startswith("ns-")]
        wg["publisher_namespaces"] = len(ns_names)

        for ns_name in ns_names:
            ns_dump = _run(["ip", "netns", "exec", ns_name, "wg", "show", "interfaces"])
            if ns_dump:
                wg["publisher_peers_total"] += 1

    return wg


def get_services_status() -> dict:
    """Get systemd service statuses."""
    services = {}
    service_names = [
        "wireztna-api", "wireztna-reconciler", "wireztna-health",
        "wireztna-dns", "wireztna-ui",
    ]

    for name in service_names:
        result = _run(["systemctl", "is-active", name])
        services[name] = result if result else "unknown"

    return services


def get_uptime() -> dict:
    """Get host uptime from /proc/uptime."""
    info = {"uptime_seconds": 0, "hostname": "unknown", "kernel": "unknown"}

    try:
        with open("/proc/uptime") as f:
            info["uptime_seconds"] = int(float(f.read().split()[0]))
    except (OSError, ValueError, IndexError):
        pass

    hostname = _run(["hostname"])
    if hostname:
        info["hostname"] = hostname

    uname = _run(["uname", "-r"])
    if uname:
        info["kernel"] = uname

    return info


def compute_capacity(cpu: dict, memory: dict, conntrack: dict, wg: dict) -> dict:
    """Compute capacity assessment based on current metrics."""
    # Estimate max users based on instance size (heuristic from B5 analysis)
    total_ram_gb = memory["total_mb"] / 1024
    if total_ram_gb >= 8:
        estimated_max = 300
    elif total_ram_gb >= 4:
        estimated_max = 200
    elif total_ram_gb >= 2:
        estimated_max = 100
    else:
        estimated_max = 50

    current_users = wg["client_peers"]
    headroom = round((1 - current_users / estimated_max) * 100, 1) if estimated_max > 0 else 0.0

    warnings = []
    bottleneck = None

    if conntrack["usage_percent"] > 70:
        warnings.append("Conntrack table nearing capacity")
        bottleneck = "conntrack"
    if memory["available_mb"] < 500:
        warnings.append("Low memory — consider instance upgrade")
        if not bottleneck:
            bottleneck = "memory"
    if cpu["usage_percent"] > 80:
        warnings.append("CPU saturated")
        if not bottleneck:
            bottleneck = "cpu"
    if current_users > estimated_max * 0.8:
        warnings.append(f"Approaching recommended peer limit ({current_users}/{estimated_max})")
        if not bottleneck:
            bottleneck = "peers"

    return {
        "estimated_max_users": estimated_max,
        "current_users": current_users,
        "headroom_percent": headroom,
        "bottleneck": bottleneck,
        "warnings": warnings,
    }


def collect_system_metrics() -> dict:
    """Collect all system metrics in a single call."""
    cpu = get_cpu_metrics()
    memory = get_memory_metrics()
    disk = get_disk_metrics()
    conntrack = get_conntrack_metrics()
    wg = get_wireguard_metrics()
    services = get_services_status()
    host = get_uptime()
    capacity = compute_capacity(cpu, memory, conntrack, wg)

    return {
        "timestamp": datetime.utcnow().isoformat() + "Z",
        "host": host,
        "cpu": cpu,
        "memory": memory,
        "disk": disk,
        "conntrack": conntrack,
        "wireguard": wg,
        "services": services,
        "capacity": capacity,
    }
