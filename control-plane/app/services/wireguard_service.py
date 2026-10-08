"""WireGuard key generation and configuration building."""

import subprocess
import ipaddress
from typing import Iterator

from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, func

from app.models.user import User
from app.config import settings


def generate_keypair() -> tuple[str, str]:
    """Generate a WireGuard private/public keypair."""
    private = subprocess.run(["wg", "genkey"], capture_output=True, text=True, check=True).stdout.strip()
    public = subprocess.run(["wg", "pubkey"], input=private, capture_output=True, text=True, check=True).stdout.strip()
    return private, public


def generate_preshared_key() -> str:
    """Generate a WireGuard preshared key."""
    return subprocess.run(["wg", "genpsk"], capture_output=True, text=True, check=True).stdout.strip()


class OverlayIPAllocator:
    """Allocates IPs from the overlay network (10.200.0.0/16).
    
    Reserved:
        10.200.0.1 — Broker
        10.200.0.2-10.200.0.255 — Reserved for infrastructure
        10.200.1.0-10.200.255.254 — Client pool
    """

    def __init__(self, network: str = "10.200.0.0/16"):
        self.network = ipaddress.IPv4Network(network)
        # Client pool starts at .1.0
        self.pool_start = ipaddress.IPv4Address("10.200.1.0")
        self.pool_end = ipaddress.IPv4Address("10.200.255.254")

    def _pool(self) -> Iterator[ipaddress.IPv4Address]:
        current = self.pool_start
        while current <= self.pool_end:
            yield current
            current += 1

    async def allocate(self, db: AsyncSession) -> str:
        """Find the next available overlay IP."""
        # Get all currently assigned IPs
        result = await db.execute(select(User.overlay_ip).where(User.overlay_ip.isnot(None)))
        used_ips = {row[0] for row in result.fetchall()}

        for ip in self._pool():
            ip_str = str(ip)
            if ip_str not in used_ips:
                return ip_str

        raise RuntimeError("Overlay IP pool exhausted")


class TunnelIPAllocator:
    """Allocates tunnel IPs for broker↔publisher links (10.100.0.0/16).
    
    Each publisher gets a /30: broker side = .1, publisher side = .2
    Site ID determines the third octet: 10.100.SITE_ID.X
    """

    @staticmethod
    def get_tunnel_ips(site_id_num: int, publisher_index: int = 0) -> tuple[str, str]:
        """Return (broker_ip, publisher_ip) for a tunnel."""
        # Each publisher in a site gets a /30 block
        base = publisher_index * 4
        broker_ip = f"10.100.{site_id_num}.{base + 1}"
        publisher_ip = f"10.100.{site_id_num}.{base + 2}"
        return broker_ip, publisher_ip


def build_client_config(
    private_key: str,
    overlay_ip: str,
    broker_public_key: str,
    broker_endpoint: str,
    allowed_ips: list[str],
    dns_ip: str = "10.200.0.1",
    preshared_key: str | None = None,
) -> str:
    """Generate a WireGuard .conf file for a client."""
    allowed_ips_str = ", ".join(allowed_ips)

    psk_line = f"\nPresharedKey = {preshared_key}" if preshared_key else ""

    return f"""[Interface]
PrivateKey = {private_key}
Address = {overlay_ip}/32
DNS = {dns_ip}

[Peer]
PublicKey = {broker_public_key}
Endpoint = {broker_endpoint}
AllowedIPs = {allowed_ips_str}{psk_line}
PersistentKeepalive = 25
"""
