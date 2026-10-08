"""Broker configuration endpoint — provides desired state to the broker agent."""

import ipaddress
from datetime import datetime, timedelta

from fastapi import APIRouter, Depends, Request
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select

from app.database import get_db
from app.models.publisher import Publisher
from app.models.user import User
from app.models.group import Group
from app.models.session import ClientSession
from app.models.client_status import ClientPeerStatus
from app.models.access_policy import GroupPublisherPolicy, AccessRule
from app.schemas.schemas import (
    BrokerConfigResponse, BrokerPublisherConfig, BrokerClientConfig,
    ClientStatusReport, ClientFlowsReport, RoutingRule, DnsHintsReport,
)
from app.services.auth_service import require_internal
from app.config import settings

router = APIRouter()

# In-memory store for client flows (refreshed every health check cycle)
# Key: overlay_ip, Value: {"flows": [...], "updated_at": datetime}
_client_flows_cache: dict[str, dict] = {}


def get_flows_for_ip(overlay_ip: str) -> list[dict]:
    """Get cached flows for a specific overlay IP."""
    entry = _client_flows_cache.get(overlay_ip)
    if not entry:
        return []
    # Flows older than 60s are stale
    age = (datetime.utcnow() - entry["updated_at"]).total_seconds()
    if age > 60:
        return []
    return entry["flows"]


@router.get("/{broker_id}/config", response_model=BrokerConfigResponse)
async def get_broker_config(
    broker_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_internal),
):
    """Returns the full desired state for a broker to reconcile against.

    The broker uses this to:
    1. Configure publisher WireGuard peers
    2. Configure client WireGuard peers (with PSK)
    3. Apply nftables rules for access control (client → publisher)
    """

    # ─── Publishers ───
    pub_result = await db.execute(
        select(Publisher).where(Publisher.public_key.isnot(None), Publisher.status != "pending")
    )
    publishers = pub_result.scalars().all()

    publisher_configs = [
        BrokerPublisherConfig(
            id=p.id,
            name=p.name,
            public_key=p.public_key,
            tunnel_ip=p.tunnel_ip,
            exposed_cidrs=p.exposed_cidrs or [],
            published_apps=p.published_apps or [],
            dns_server=p.dns_server,
            dns_zones=p.dns_zones,
            publisher_index=p.publisher_index,
            virtual_cidr=p.virtual_cidr,
            endpoint=p.endpoint,
            status=p.status,
            priority=p.priority,
            exit_node=p.exit_node,
        )
        for p in publishers
    ]

    # ─── Clients (only with active session) ───
    users_result = await db.execute(
        select(User).where(User.public_key.isnot(None), User.status == "active")
    )
    users = users_result.scalars().all()

    client_configs = []
    for user in users:
        if not user.public_key or not user.overlay_ip:
            continue

        # Check for active session
        session_result = await db.execute(
            select(ClientSession)
            .where(
                ClientSession.user_id == user.id,
                ClientSession.is_active == True,
                ClientSession.expires_at > datetime.utcnow(),
            )
            .order_by(ClientSession.created_at.desc())
            .limit(1)
        )
        active_session = session_result.scalar_one_or_none()
        if not active_session:
            continue

        # Get selected group filter (for CIDR overlap resolution)
        selected_group_id = active_session.selected_group_id

        # Derive access: User → Groups → Publishers
        await db.refresh(user, ["groups"])
        allowed_publisher_ids = set()
        allowed_cidrs = set()

        for group in user.groups:
            # If a specific group was selected (CIDR overlap), only include that group's publishers
            if selected_group_id and group.id != selected_group_id:
                continue
            await db.refresh(group, ["publishers"])
            for publisher in group.publishers:
                allowed_publisher_ids.add(publisher.id)
                for cidr in (publisher.exposed_cidrs or []):
                    allowed_cidrs.add(cidr)

        # Build publisher_id → cidrs mapping for policy routing (backward compat)
        publisher_cidrs = {}
        for group in user.groups:
            # If a specific group was selected (CIDR overlap), only include that group's publishers
            if selected_group_id and group.id != selected_group_id:
                continue
            for publisher in group.publishers:
                if publisher.status in ("disabled", "pending"):
                    continue
                if publisher.id in allowed_publisher_ids and publisher.exposed_cidrs:
                    publisher_cidrs[publisher.id] = publisher.exposed_cidrs

        # ─── Build routing_rules with overlap resolution ───
        # Rules are ordered so nftables evaluates them correctly:
        # 1. App rules (IP+port) — most specific, evaluated first in nftables
        # 2. CIDR rules — ordered by prefix length descending (longest prefix match)
        # Ties broken by publisher priority (lower = wins = placed later in nftables)

        routing_rules: list[RoutingRule] = []

        for group in user.groups:
            # If a specific group was selected (CIDR overlap), only include that group's publishers
            if selected_group_id and group.id != selected_group_id:
                continue
            for publisher in group.publishers:
                if publisher.publisher_index is None:
                    continue
                # Skip publishers that are not actively routing traffic
                if publisher.status in ("disabled", "pending"):
                    continue

                # App rules (port-specific)
                for app in (publisher.published_apps or []):
                    target = app.get("target", "") if isinstance(app, dict) else app.target
                    port = app.get("port", 0) if isinstance(app, dict) else app.port
                    protocol = app.get("protocol", "tcp") if isinstance(app, dict) else app.protocol

                    # Determine prefix_len: /32 for bare IPs, actual prefix for CIDRs
                    try:
                        net = ipaddress.ip_network(target, strict=False)
                        prefix_len = net.prefixlen
                        resolved_target = str(net)
                    except ValueError:
                        # FQDN — treat as /32 equivalent specificity, target stays as-is
                        # DNS resolution happens on the broker at runtime
                        resolved_target = target
                        prefix_len = 32

                    routing_rules.append(RoutingRule(
                        type="app",
                        target=resolved_target,
                        port=port,
                        protocol=protocol,
                        publisher_id=publisher.id,
                        publisher_index=publisher.publisher_index,
                        prefix_len=prefix_len,
                    ))

                # CIDR rules (broad network access)
                for cidr in (publisher.exposed_cidrs or []):
                    try:
                        net = ipaddress.ip_network(cidr, strict=False)
                        prefix_len = net.prefixlen
                    except ValueError:
                        prefix_len = 0

                    routing_rules.append(RoutingRule(
                        type="cidr",
                        target=cidr,
                        port=0,
                        protocol="any",
                        publisher_id=publisher.id,
                        publisher_index=publisher.publisher_index,
                        prefix_len=prefix_len,
                    ))

        # Sort routing_rules for nftables evaluation order:
        # nftables evaluates sequentially — LAST matching mangle rule wins (mark overwrite)
        # So we want: least specific first → most specific last
        # Within same specificity: higher priority number first, lower priority last (lower wins)
        #
        # Sort key: (type_order ASC, prefix_len ASC, priority DESC)
        # - type_order: cidr=0, app=1 (apps always after cidrs = more specific)
        # - prefix_len ASC: /16 before /24 before /32
        # - priority DESC: 200 before 100 (so 100 is placed last = wins)

        # Build publisher priority lookup
        pub_priority = {p.id: p.priority for p in publishers}

        routing_rules.sort(key=lambda r: (
            0 if r.type == "cidr" else 1,  # CIDRs first (less specific)
            r.prefix_len,                   # Shorter prefix first (/16 < /24 < /32)
            -pub_priority.get(r.publisher_id, 100),  # Higher priority number first (lower wins last)
        ))

        # ─── Access restrictions (per group↔publisher) ───
        # If any group↔publisher pair has access_policy="restricted", only those
        # specific apps are allowed in the forward chain (instead of full CIDR).
        # This is loaded per-publisher for this client.
        access_restrictions: dict[str, list[dict]] = {}

        for group in user.groups:
            if selected_group_id and group.id != selected_group_id:
                continue
            for publisher in group.publishers:
                if publisher.id not in allowed_publisher_ids:
                    continue
                if publisher.status in ("disabled", "pending"):
                    continue

                # Check if this group↔publisher has a restricted policy
                policy_result = await db.execute(
                    select(GroupPublisherPolicy).where(
                        GroupPublisherPolicy.group_id == group.id,
                        GroupPublisherPolicy.publisher_id == publisher.id,
                        GroupPublisherPolicy.access_policy == "restricted",
                    )
                )
                policy = policy_result.scalar_one_or_none()
                if policy:
                    # Load the rules for this policy
                    rules_result = await db.execute(
                        select(AccessRule).where(AccessRule.policy_id == policy.id)
                    )
                    rules = rules_result.scalars().all()
                    if rules:
                        access_restrictions[publisher.id] = [
                            {"target": r.target, "port": r.port, "protocol": r.protocol}
                            for r in rules
                        ]

        client_configs.append(BrokerClientConfig(
            public_key=user.public_key,
            overlay_ip=user.overlay_ip,
            allowed_publishers=sorted(allowed_publisher_ids),
            allowed_cidrs=sorted(allowed_cidrs),
            publisher_cidrs=publisher_cidrs,
            routing_rules=routing_rules if routing_rules else None,
            preshared_key=active_session.preshared_key,
            access_restrictions=access_restrictions if access_restrictions else None,
            exit_node_publisher_id=active_session.exit_node_publisher_id,
        ))

    return BrokerConfigResponse(
        broker_id=broker_id,
        version="1",
        publishers=publisher_configs,
        clients=client_configs,
        dns_hints=get_dns_hints() or None,
        overlay_ip=settings.broker_overlay_ip,
        overlay_network=settings.broker_overlay_network,
        wg_port=settings.broker_wg_port,
    )


@router.post("/{broker_id}/client-status", status_code=204)
async def report_client_status(
    broker_id: str,
    report: ClientStatusReport,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_internal),
):
    """Receives client peer status from the broker health monitor.

    Peers present in the report get their status updated.
    Peers previously reported by this broker but ABSENT from the current report
    are marked as disconnected (their WG peer was removed or they disappeared).
    """
    now = datetime.utcnow()

    reported_keys = set()

    for peer in report.peers:
        reported_keys.add(peer.public_key)

        result = await db.execute(
            select(ClientPeerStatus).where(ClientPeerStatus.public_key == peer.public_key)
        )
        existing = result.scalar_one_or_none()

        last_handshake = None
        if peer.last_handshake_at:
            try:
                last_handshake = datetime.fromisoformat(peer.last_handshake_at)
            except (ValueError, TypeError):
                pass

        if existing:
            existing.endpoint_ip = peer.endpoint_ip
            existing.endpoint_port = peer.endpoint_port
            existing.last_handshake_at = last_handshake
            existing.is_connected = peer.is_connected
            existing.rx_bytes = peer.rx_bytes
            existing.tx_bytes = peer.tx_bytes
            existing.last_reported_at = now
            existing.broker_id = broker_id
        else:
            new_status = ClientPeerStatus(
                public_key=peer.public_key,
                endpoint_ip=peer.endpoint_ip,
                endpoint_port=peer.endpoint_port,
                last_handshake_at=last_handshake,
                is_connected=peer.is_connected,
                rx_bytes=peer.rx_bytes,
                tx_bytes=peer.tx_bytes,
                last_reported_at=now,
                broker_id=broker_id,
            )
            db.add(new_status)

    # Mark peers from this broker that are no longer in the report as disconnected.
    # This handles the case where a client's WG peer was removed (session expired,
    # client disconnected cleanly, reconciler removed the peer).
    if reported_keys:
        stale_result = await db.execute(
            select(ClientPeerStatus).where(
                ClientPeerStatus.broker_id == broker_id,
                ClientPeerStatus.is_connected == True,
                ClientPeerStatus.public_key.notin_(reported_keys),
            )
        )
    else:
        # No peers reported at all — mark ALL peers from this broker as disconnected
        stale_result = await db.execute(
            select(ClientPeerStatus).where(
                ClientPeerStatus.broker_id == broker_id,
                ClientPeerStatus.is_connected == True,
            )
        )

    stale_peers = stale_result.scalars().all()
    for stale in stale_peers:
        stale.is_connected = False
        stale.last_reported_at = now

    # ─── Auto-extend sessions for connected clients ───
    # If a client has a fresh handshake (is_connected=True) and their session
    # expires within the next hour, extend it by session_ttl_hours.
    # This allows tunnels to stay alive indefinitely without client-side renewal.
    extend_threshold = now + timedelta(hours=1)
    extend_duration = timedelta(hours=settings.session_ttl_hours)

    connected_keys = [p.public_key for p in report.peers if p.is_connected]
    if connected_keys:
        # Find users with these public keys
        users_result = await db.execute(
            select(User.id, User.public_key).where(User.public_key.in_(connected_keys))
        )
        user_map = {row.public_key: row.id for row in users_result.all()}

        if user_map:
            # Find active sessions expiring soon for these users
            sessions_result = await db.execute(
                select(ClientSession).where(
                    ClientSession.user_id.in_(user_map.values()),
                    ClientSession.is_active == True,
                    ClientSession.expires_at < extend_threshold,
                    ClientSession.expires_at > now,  # Not already expired
                )
            )
            expiring_sessions = sessions_result.scalars().all()
            for session in expiring_sessions:
                session.expires_at = now + extend_duration


@router.post("/{broker_id}/client-flows", status_code=204)
async def report_client_flows(
    broker_id: str,
    report: ClientFlowsReport,
    _=Depends(require_internal),
):
    """Receives active conntrack flows from the broker health monitor.

    Stored in-memory (ephemeral) since flows change every health check cycle.
    Replaces the entire cache on each report — IPs not present are cleared.
    """
    now = datetime.utcnow()

    # Update flows for IPs in the report
    reported_ips = set(report.flows_by_ip.keys())
    for overlay_ip, flows in report.flows_by_ip.items():
        _client_flows_cache[overlay_ip] = {
            "flows": [f.model_dump() for f in flows],
            "updated_at": now,
        }

    # Clear IPs that are no longer in the report (connection ended)
    stale_ips = set(_client_flows_cache.keys()) - reported_ips
    for ip in stale_ips:
        del _client_flows_cache[ip]


# ─── DNS Routing Hints ───
# In-memory store for DNS-based routing hints.
# Key: overlay_ip, Value: [{ip: str, publisher_index: int}]
# These create /32 nftables rules that override broad CIDR rules when
# two publishers expose the same network range.
_dns_hints_cache: dict[str, list[dict]] = {}


def get_dns_hints() -> dict[str, list[dict]]:
    """Get current DNS routing hints (consumed by reconciler via broker config)."""
    return _dns_hints_cache.copy()


@router.post("/{broker_id}/dns-hints", status_code=204)
async def report_dns_hints(
    broker_id: str,
    report: DnsHintsReport,
    _=Depends(require_internal),
):
    """Receives DNS routing hints from the DNS proxy.

    When a client resolves a domain via a publisher's DNS server, the proxy
    records the resolved IP → publisher_index mapping. These hints create /32
    nftables rules ensuring traffic goes to the correct publisher even when
    multiple publishers expose the same CIDR range.
    """
    for client_ip, hints in report.hints_by_client.items():
        _dns_hints_cache[client_ip] = [
            {"ip": h.ip, "publisher_index": h.publisher_index}
            for h in hints
        ]
