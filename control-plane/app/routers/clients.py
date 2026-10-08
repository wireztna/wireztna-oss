"""Client configuration generation and observability endpoints."""

import io
import qrcode

from datetime import datetime
from fastapi import APIRouter, Depends, HTTPException, Query, Request, status
from fastapi.responses import StreamingResponse
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, desc

from app.database import get_db
from app.models.user import User
from app.models.group import Group
from app.models.publisher import Publisher
from app.models.session import ClientSession
from app.models.client_status import ClientPeerStatus
from app.models.client_enrollment import ClientEnrollmentToken
from app.models.access_policy import GroupPublisherPolicy, AccessRule
from app.models.audit import AccessLog
from app.services.auth_service import require_admin
from app.services.wireguard_service import generate_keypair, build_client_config
from app.schemas.schemas import (
    ConnectedClientResponse, ClientConnectionHistory,
    ClientDebugResponse, ClientDebugPublisher, ClientDebugSession,
    ClientFlowResponse, ClientFlowsResponse,
    ClientEnrollRequest, ClientEnrollResponse,
)
from app.config import settings

router = APIRouter()


# ─── Client Self-Enrollment ───

@router.post("/enroll", response_model=ClientEnrollResponse, status_code=status.HTTP_201_CREATED)
async def enroll_client(req: ClientEnrollRequest, request: Request, db: AsyncSession = Depends(get_db)):
    """Client self-enrollment using a one-time token.

    Flow:
        1. Client generates WireGuard keypair locally
        2. Client sends public_key + token to this endpoint
        3. API validates token (exists, not used, not expired, not revoked)
        4. API registers client's public_key on the user
        5. API returns full client configuration (broker info, overlay IP, allowed IPs)
        6. Token is marked as used

    No authentication required — the token IS the authorization.
    """
    # Find the token and linked user before deciding whether this is a first
    # use or an idempotent recovery of the same locally persisted key.
    result = await db.execute(
        select(ClientEnrollmentToken).where(ClientEnrollmentToken.token == req.token)
    )
    token = result.scalar_one_or_none()

    if not token:
        raise HTTPException(status_code=401, detail="Invalid enrollment token")
    if token.revoked:
        raise HTTPException(status_code=401, detail="Enrollment token has been revoked")

    user_result = await db.execute(select(User).where(User.id == token.user_id))
    user = user_result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=500, detail="Token linked to non-existent user")
    if user.status != "active":
        raise HTTPException(status_code=403, detail=f"User account is '{user.status}' — must be active")

    if token.used_at:
        # The client persists its private key before the one-shot POST. Returning
        # the same public configuration for that exact key lets it recover from
        # a crash or disk error after the server committed but before config.yaml
        # was durably replaced. A different key remains a rejected token reuse.
        if user.public_key != req.public_key:
            raise HTTPException(status_code=401, detail="Enrollment token already used")
    else:
        if token.expires_at < datetime.utcnow():
            raise HTTPException(status_code=401, detail="Enrollment token expired")
        user.public_key = req.public_key
        await db.flush()

        client_ip = request.client.host if request.client else None
        token.used_at = datetime.utcnow()
        token.used_from_ip = client_ip
        token.device_name = req.device_name
        token.device_platform = req.platform
        await db.flush()

    allowed_ips = await _get_user_allowed_ips(user.id, db)
    broker_pubkey = settings.broker_public_key or ""
    broker_endpoint = (
        f"{settings.broker_public_endpoint}:{settings.broker_wg_port}"
        if settings.broker_public_endpoint
        else f"broker.example.com:{settings.broker_wg_port}"
    )

    return ClientEnrollResponse(
        email=user.email,
        username=user.username,
        overlay_ip=user.overlay_ip,
        broker_public_key=broker_pubkey,
        broker_endpoint=broker_endpoint,
        broker_overlay_ip=settings.broker_overlay_ip,
        allowed_ips=allowed_ips,
        dns_server=settings.broker_overlay_ip,
        enrolled_at=token.used_at,
    )


async def _get_user_allowed_ips(user_id: str, db: AsyncSession) -> list[str]:
    """Calculate AllowedIPs for a user based on User → Group → Publisher → exposed_cidrs."""
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    await db.refresh(user, ["groups"])

    allowed_ips = set()
    allowed_ips.add(f"{settings.broker_overlay_ip}/32")  # Always include broker

    for group in user.groups:
        await db.refresh(group, ["publishers"])
        for publisher in group.publishers:
            for cidr in (publisher.exposed_cidrs or []):
                allowed_ips.add(cidr)

    return sorted(allowed_ips)


@router.get("/{user_id}/config")
async def get_client_config(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Generate and return WireGuard .conf for a user."""
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Generate keypair for client
    private_key, public_key = generate_keypair()

    # Store public key on user (private key delivered only now, never stored)
    user.public_key = public_key
    await db.flush()

    # Calculate allowed IPs
    allowed_ips = await _get_user_allowed_ips(user_id, db)

    # Broker connection info
    broker_pubkey = settings.broker_public_key or "BROKER_PUBLIC_KEY_NOT_CONFIGURED"
    broker_endpoint = f"{settings.broker_public_endpoint}:{settings.broker_wg_port}" if settings.broker_public_endpoint else f"broker.example.com:{settings.broker_wg_port}"

    config = build_client_config(
        private_key=private_key,
        overlay_ip=user.overlay_ip,
        broker_public_key=broker_pubkey,
        broker_endpoint=broker_endpoint,
        allowed_ips=allowed_ips,
    )

    return StreamingResponse(
        io.BytesIO(config.encode()),
        media_type="application/octet-stream",
        headers={"Content-Disposition": f"attachment; filename=wireztna-{user.username}.conf"},
    )


@router.get("/{user_id}/qr")
async def get_client_qr(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Generate QR code of WireGuard config for mobile clients."""
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Generate keypair
    private_key, public_key = generate_keypair()
    user.public_key = public_key
    await db.flush()

    allowed_ips = await _get_user_allowed_ips(user_id, db)

    broker_pubkey = settings.broker_public_key or "BROKER_PUBLIC_KEY_NOT_CONFIGURED"
    broker_endpoint = f"{settings.broker_public_endpoint}:{settings.broker_wg_port}" if settings.broker_public_endpoint else f"broker.example.com:{settings.broker_wg_port}"

    config = build_client_config(
        private_key=private_key,
        overlay_ip=user.overlay_ip,
        broker_public_key=broker_pubkey,
        broker_endpoint=broker_endpoint,
        allowed_ips=allowed_ips,
    )

    # Generate QR
    qr = qrcode.QRCode(version=1, box_size=10, border=4)
    qr.add_data(config)
    qr.make(fit=True)
    img = qr.make_image(fill_color="black", back_color="white")

    buf = io.BytesIO()
    img.save(buf, format="PNG")
    buf.seek(0)

    return StreamingResponse(buf, media_type="image/png")


# ─── Observability endpoints ───

@router.get("/connected", response_model=list[ConnectedClientResponse])
async def list_connected_clients(db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """List all clients with their real-time connection status."""
    users_result = await db.execute(
        select(User).where(User.public_key.isnot(None)).order_by(User.username)
    )
    users = users_result.scalars().all()

    clients = []
    for user in users:
        if not user.public_key:
            continue

        status_result = await db.execute(
            select(ClientPeerStatus).where(ClientPeerStatus.public_key == user.public_key)
        )
        peer_status = status_result.scalar_one_or_none()

        session_result = await db.execute(
            select(ClientSession).where(
                ClientSession.user_id == user.id,
                ClientSession.is_active == True,
                ClientSession.expires_at > datetime.utcnow(),
            )
        )
        active_session = session_result.scalar_one_or_none()

        # Determine connection status: use broker-reported value, but treat
        # stale reports (>60s old) as disconnected — covers health monitor downtime.
        is_connected = False
        if peer_status and peer_status.is_connected:
            report_age = (datetime.utcnow() - peer_status.last_reported_at).total_seconds() if peer_status.last_reported_at else 9999
            is_connected = report_age < 60  # 4x health check interval (15s)

        clients.append(ConnectedClientResponse(
            username=user.username,
            user_id=user.id,
            overlay_ip=user.overlay_ip,
            endpoint_ip=peer_status.endpoint_ip if peer_status else None,
            endpoint_port=peer_status.endpoint_port if peer_status else None,
            is_connected=is_connected,
            last_handshake_at=peer_status.last_handshake_at if peer_status else None,
            rx_bytes=peer_status.rx_bytes if peer_status else 0,
            tx_bytes=peer_status.tx_bytes if peer_status else 0,
            session_expires_at=active_session.expires_at if active_session else None,
            session_group_id=active_session.selected_group_id if active_session else None,
            session_group_name=None,  # Resolved below if needed
            client_version=peer_status.client_version if peer_status else None,
            wg_version=peer_status.wg_version if peer_status else None,
            platform=peer_status.platform if peer_status else None,
            last_reported_at=peer_status.last_reported_at if peer_status else None,
        ))

    # Resolve group names for sessions that have a selected group
    group_ids_needed = {c.session_group_id for c in clients if c.session_group_id}
    if group_ids_needed:
        group_result = await db.execute(
            select(Group).where(Group.id.in_(group_ids_needed))
        )
        group_name_map = {g.id: g.name for g in group_result.scalars().all()}
        for client in clients:
            if client.session_group_id:
                client.session_group_name = group_name_map.get(client.session_group_id)

    return clients


@router.get("/{user_id}/history", response_model=list[ClientConnectionHistory])
async def get_client_history(
    user_id: str,
    limit: int = Query(default=50, le=200),
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Get session and connection history for a specific client."""
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    history = []

    # Session history
    sessions_result = await db.execute(
        select(ClientSession)
        .where(ClientSession.user_id == user_id)
        .order_by(desc(ClientSession.created_at))
        .limit(limit)
    )
    sessions = sessions_result.scalars().all()

    for session in sessions:
        history.append(ClientConnectionHistory(
            event="session_renew",
            timestamp=session.created_at,
            detail=f"Session created (expires: {session.expires_at.strftime('%H:%M:%S')})",
            client_ip=session.client_ip,
        ))

    # Audit log entries
    audit_result = await db.execute(
        select(AccessLog)
        .where(AccessLog.user_id == user_id)
        .order_by(desc(AccessLog.timestamp))
        .limit(limit)
    )
    audit_entries = audit_result.scalars().all()

    for entry in audit_entries:
        history.append(ClientConnectionHistory(
            event=entry.action,
            timestamp=entry.timestamp,
            detail=entry.detail,
            client_ip=entry.client_ip,
        ))

    history.sort(key=lambda h: h.timestamp, reverse=True)
    return history[:limit]


# ─── Connection Debug ───

@router.get("/{user_id}/debug", response_model=ClientDebugResponse)
async def get_client_debug(
    user_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Detailed connection debug info for a client.

    Shows session state, tunnel health, accessible publishers,
    and auto-detected issues that may explain connectivity problems.
    """
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    issues: list[str] = []

    # ─── Peer status from broker ───
    peer_status = None
    if user.public_key:
        status_result = await db.execute(
            select(ClientPeerStatus).where(ClientPeerStatus.public_key == user.public_key)
        )
        peer_status = status_result.scalar_one_or_none()

    is_connected = False
    if peer_status and peer_status.is_connected:
        report_age = (datetime.utcnow() - peer_status.last_reported_at).total_seconds() if peer_status.last_reported_at else 9999
        is_connected = report_age < 60  # 4x health check interval (15s)
    last_handshake = peer_status.last_handshake_at if peer_status else None
    handshake_age = None
    if last_handshake:
        handshake_age = int((datetime.utcnow() - last_handshake).total_seconds())
        if handshake_age > 180:
            issues.append(
                f"Last WireGuard handshake was {handshake_age}s ago (>3min) — "
                f"tunnel may be stale or client is offline"
            )

    if not user.public_key:
        issues.append("No public key set — client config has not been generated yet")

    # ─── Active session ───
    session_result = await db.execute(
        select(ClientSession).where(
            ClientSession.user_id == user_id,
            ClientSession.is_active == True,
            ClientSession.expires_at > datetime.utcnow(),
        )
    )
    active_session_obj = session_result.scalar_one_or_none()

    active_session = None
    if active_session_obj:
        remaining = (active_session_obj.expires_at - datetime.utcnow()).total_seconds()

        # Resolve selected group name
        selected_group_name = None
        if active_session_obj.selected_group_id:
            group_name_result = await db.execute(
                select(Group).where(Group.id == active_session_obj.selected_group_id)
            )
            group_obj = group_name_result.scalar_one_or_none()
            if group_obj:
                selected_group_name = group_obj.name

        # Resolve exit node publisher name
        exit_node_name = None
        tunnel_mode = "split"
        if active_session_obj.exit_node_publisher_id:
            tunnel_mode = "vpn"
            from app.models.publisher import Publisher
            pub_result = await db.execute(
                select(Publisher).where(Publisher.id == active_session_obj.exit_node_publisher_id)
            )
            pub_obj = pub_result.scalar_one_or_none()
            if pub_obj:
                exit_node_name = pub_obj.name

        active_session = ClientDebugSession(
            session_id=active_session_obj.id,
            is_active=True,
            created_at=active_session_obj.created_at,
            expires_at=active_session_obj.expires_at,
            ttl_remaining_seconds=int(max(remaining, 0)),
            client_ip=active_session_obj.client_ip,
            selected_group_id=active_session_obj.selected_group_id,
            selected_group_name=selected_group_name,
            exit_node_publisher_id=active_session_obj.exit_node_publisher_id,
            exit_node_publisher_name=exit_node_name,
            tunnel_mode=tunnel_mode,
        )
        if remaining < 300:
            issues.append(
                f"Session expires in {int(remaining)}s — client should renew soon "
                f"or tunnel will die"
            )
    else:
        issues.append(
            "No active session — WireGuard handshake will fail (no PSK). "
            "Client must call POST /sessions/renew"
        )

    # Total session count
    total_sessions_result = await db.execute(
        select(ClientSession).where(ClientSession.user_id == user_id)
    )
    total_sessions = len(total_sessions_result.scalars().all())

    # ─── Accessible publishers (User → Groups → Publishers) ───
    await db.refresh(user, ["groups"])

    accessible_publishers: list[ClientDebugPublisher] = []
    all_cidrs: set[str] = set()
    publisher_seen: set[str] = set()

    for group in user.groups:
        await db.refresh(group, ["publishers"])
        for publisher in group.publishers:
            for cidr in (publisher.exposed_cidrs or []):
                all_cidrs.add(cidr)

            if publisher.id not in publisher_seen:
                publisher_seen.add(publisher.id)

                # Load access policy for this group↔publisher
                policy_result = await db.execute(
                    select(GroupPublisherPolicy).where(
                        GroupPublisherPolicy.group_id == group.id,
                        GroupPublisherPolicy.publisher_id == publisher.id,
                    )
                )
                policy = policy_result.scalar_one_or_none()
                access_policy = policy.access_policy if policy else "unrestricted"
                allowed_apps = None

                if policy and policy.access_policy == "restricted":
                    rules_result = await db.execute(
                        select(AccessRule).where(AccessRule.policy_id == policy.id)
                    )
                    rules = rules_result.scalars().all()
                    allowed_apps = [
                        {"target": r.target, "port": r.port, "protocol": r.protocol, "name": r.name}
                        for r in rules
                    ]

                accessible_publishers.append(ClientDebugPublisher(
                    publisher_id=publisher.id,
                    publisher_name=publisher.name,
                    status=publisher.status,
                    exposed_cidrs=publisher.exposed_cidrs or [],
                    endpoint=publisher.endpoint,
                    last_heartbeat=publisher.last_heartbeat,
                    via_groups=[group.name],
                    access_policy=access_policy,
                    allowed_apps=allowed_apps,
                ))
            else:
                # Publisher accessible via multiple groups — add group name
                for ap in accessible_publishers:
                    if ap.publisher_id == publisher.id:
                        ap.via_groups.append(group.name)
                        break

    # Publisher-level diagnostics
    for pub in accessible_publishers:
        if pub.status == "disabled":
            issues.append(f"Publisher '{pub.publisher_name}' is disabled — traffic won't route")
        elif pub.status == "pending":
            issues.append(f"Publisher '{pub.publisher_name}' is pending — not yet connected")
        elif pub.status == "offline":
            issues.append(f"Publisher '{pub.publisher_name}' is offline — tunnel down")
        elif pub.last_heartbeat:
            hb_age = (datetime.utcnow() - pub.last_heartbeat).total_seconds()
            if hb_age > 60:
                issues.append(
                    f"Publisher '{pub.publisher_name}' last heartbeat was {int(hb_age)}s ago "
                    f"— may be unreachable"
                )

    if not accessible_publishers:
        issues.append("No publishers accessible — user is not in any group with publishers")

    if user.status != "active":
        issues.append(f"User status is '{user.status}' — must be 'active' for access")

    return ClientDebugResponse(
        user_id=user.id,
        username=user.username,
        overlay_ip=user.overlay_ip,
        status=user.status,
        public_key_set=user.public_key is not None,
        is_connected=is_connected,
        endpoint_ip=peer_status.endpoint_ip if peer_status else None,
        endpoint_port=peer_status.endpoint_port if peer_status else None,
        last_handshake_at=last_handshake,
        handshake_age_seconds=handshake_age,
        rx_bytes=peer_status.rx_bytes if peer_status else 0,
        tx_bytes=peer_status.tx_bytes if peer_status else 0,
        last_reported_at=peer_status.last_reported_at if peer_status else None,
        client_version=peer_status.client_version if peer_status else None,
        wg_version=peer_status.wg_version if peer_status else None,
        platform=peer_status.platform if peer_status else None,
        active_session=active_session,
        total_sessions=total_sessions,
        accessible_publishers=accessible_publishers,
        total_allowed_cidrs=sorted(all_cidrs),
        issues=issues,
    )


# ─── Active Flows (conntrack) ───

# Well-known port → service name mapping
_PORT_SERVICES = {
    22: "SSH", 80: "HTTP", 443: "HTTPS", 3389: "RDP", 3306: "MySQL",
    5432: "PostgreSQL", 6379: "Redis", 8080: "HTTP-Alt", 8443: "HTTPS-Alt",
    53: "DNS", 25: "SMTP", 587: "SMTP", 993: "IMAPS", 445: "SMB",
    139: "NetBIOS", 5900: "VNC", 27017: "MongoDB", 9200: "Elasticsearch",
    6443: "K8s-API", 2379: "etcd", 5672: "AMQP", 1433: "MSSQL",
}


def _resolve_service(port: int) -> str | None:
    """Resolve a port number to a human-readable service name."""
    return _PORT_SERVICES.get(port)


@router.get("/{user_id}/flows", response_model=ClientFlowsResponse)
async def get_client_flows(
    user_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Get active network flows (connections) for a specific client.

    Data comes from conntrack on the broker, reported every ~15s.
    Shows what the client is actively connecting to through the tunnel.
    """
    from app.routers.brokers import get_flows_for_ip

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    if not user.overlay_ip:
        return ClientFlowsResponse(
            overlay_ip=None, total_flows=0, flows=[], stale=True
        )

    raw_flows = get_flows_for_ip(user.overlay_ip)

    # Resolve publisher names for destination IPs
    # Uses longest-prefix-match across all accessible publishers (filtered by session group)
    import ipaddress
    publisher_networks: list[tuple[ipaddress.IPv4Network, str, int]] = []  # (network, name, prefix_len)

    # Get active session to determine selected group
    session_result = await db.execute(
        select(ClientSession).where(
            ClientSession.user_id == user_id,
            ClientSession.is_active == True,
            ClientSession.expires_at > datetime.utcnow(),
        )
    )
    active_session = session_result.scalar_one_or_none()
    selected_group_id = active_session.selected_group_id if active_session else None

    await db.refresh(user, ["groups"])
    for group in user.groups:
        # If a specific group is selected, only show publishers from that group
        if selected_group_id and group.id != selected_group_id:
            continue
        await db.refresh(group, ["publishers"])
        for publisher in group.publishers:
            for cidr in (publisher.exposed_cidrs or []):
                try:
                    net = ipaddress.IPv4Network(cidr, strict=False)
                    publisher_networks.append((net, publisher.name, net.prefixlen))
                except ValueError:
                    pass

    # Sort by prefix length descending (most specific first) for longest-prefix-match
    publisher_networks.sort(key=lambda x: x[2], reverse=True)

    def _find_publisher(dst_ip: str) -> str | None:
        """Find publisher using longest-prefix-match (most specific CIDR wins)."""
        try:
            addr = ipaddress.IPv4Address(dst_ip)
            for net, name, _ in publisher_networks:
                if addr in net:
                    return name
        except ValueError:
            pass
        return None

    def _is_reachable(state: str, bytes_in: int, protocol: str, packets_out: int, packets_in: int) -> bool | None:
        """Determine if destination is responding based on conntrack state and packet counts.

        Returns:
            True: destination is confirmed responding
            False: destination is NOT responding (no reply, or reply rate critically low)
            None: unknown / can't determine yet
        """
        # TCP states are reliable indicators
        if state in ("ESTABLISHED", "TIME_WAIT", "FIN_WAIT", "CLOSE_WAIT", "LAST_ACK", "CLOSE"):
            return True
        if state == "UNREPLIED":
            return False
        if state == "SYN_SENT":
            return False

        # For ICMP/UDP: use packet counts to determine delivery
        if packets_out > 0 and packets_in == 0:
            return False  # Sent packets but received nothing back

        if packets_out > 0 and packets_in > 0:
            # Calculate reply rate
            reply_rate = packets_in / packets_out
            if reply_rate < 0.1:
                return False  # Less than 10% reply rate — effectively unreachable
            return True  # Getting replies

        # Fallback: check bytes for conntrack without packet counters
        if state == "ACTIVE":
            if bytes_in > 0:
                return True
            if packets_out > 3:
                return False  # Sent multiple packets with 0 bytes back
            return None  # Too early to tell

        return None

    flows = []
    for raw in raw_flows:
        dst_port = raw.get("dst_port", 0)
        state = raw.get("state", "UNKNOWN")
        bytes_in = raw.get("bytes_in", 0)
        protocol = raw.get("protocol", "unknown")
        packets_out = raw.get("packets_out", 0)
        packets_in = raw.get("packets_in", 0)
        flows.append(ClientFlowResponse(
            protocol=protocol,
            dst_ip=raw.get("dst_ip", ""),
            dst_port=dst_port,
            src_port=raw.get("src_port", 0),
            state=state,
            bytes_in=bytes_in,
            bytes_out=raw.get("bytes_out", 0),
            service=_resolve_service(dst_port),
            publisher_name=_find_publisher(raw.get("dst_ip", "")),
            reachable=_is_reachable(state, bytes_in, protocol, packets_out, packets_in),
        ))

    # Sort by bytes descending (most active flows first)
    flows.sort(key=lambda f: f.bytes_in + f.bytes_out, reverse=True)

    return ClientFlowsResponse(
        overlay_ip=user.overlay_ip,
        total_flows=len(flows),
        flows=flows,
        stale=len(raw_flows) == 0,
    )
