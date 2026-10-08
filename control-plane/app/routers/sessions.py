"""Session management — ephemeral PSK renewal for Zero Trust client access.

This router handles the session lifecycle for WireGuard clients:
- Renew: Authenticate via JWT, receive a fresh PSK with a TTL.
- Revoke: Admin can kill a session immediately.

The PSK is the cryptographic "session token" — without a valid one,
the WireGuard handshake fails and the tunnel is dead.

Future extension points (marked with # FUTURE):
- OIDC/IdP token validation before issuing PSK
- MFA claim verification in JWT
- OPA/Rego policy evaluation at renewal time
- Device posture gate
"""

from datetime import datetime, timedelta
import json

from fastapi import APIRouter, Depends, HTTPException, Request, status, Query as FastAPIQuery
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, update

from app.database import get_db
from app.models.session import ClientSession
from app.models.user import User
from app.models.audit import AccessLog
from app.models.client_status import ClientPeerStatus
from app.services.auth_service import get_current_user, require_admin
from app.services.wireguard_service import generate_preshared_key
from app.schemas.schemas import SessionRenewRequest, SessionRenewResponse, SessionInfoResponse
from app.config import settings

router = APIRouter()


def _broker_wireguard_endpoint() -> str:
    """Return the canonical host:port endpoint consumed by WireGuard clients."""
    host = settings.broker_public_endpoint or settings.broker_overlay_ip
    return f"{host}:{settings.broker_wg_port}"


async def _get_user_allowed_ips(user: "User", selected_group_id: str | None, db: AsyncSession) -> list[str]:
    """Calculate the AllowedIPs for a user based on their group→publisher→CIDRs.

    Always includes the broker overlay IP. If a group_id is specified,
    only CIDRs from that group's publishers are included.
    """
    allowed_ips = set()
    allowed_ips.add(f"{settings.broker_overlay_ip}/32")

    await db.refresh(user, ["groups"])
    for group in user.groups:
        if selected_group_id and group.id != selected_group_id:
            continue
        await db.refresh(group, ["publishers"])
        for publisher in group.publishers:
            if publisher.status in ("disabled", "pending"):
                continue
            for cidr in (publisher.exposed_cidrs or []):
                allowed_ips.add(cidr)

    return sorted(allowed_ips)


def _full_tunnel_allowed_ips() -> list[str]:
    """Return a platform-neutral full-tunnel prefix set.

    The /1 pair is equivalent to 0.0.0.0/0 without triggering wg-quick's
    implicit default-route and endpoint-route ownership on macOS.
    """
    return ["0.0.0.0/1", "128.0.0.0/1", f"{settings.broker_overlay_ip}/32"]


async def _validated_exit_node_id(
    user: "User",
    selected_group_id: str | None,
    exit_node_id: str | None,
    db: AsyncSession,
) -> str | None:
    """Validate the complete User → Group → Publisher selection before persistence."""
    await db.refresh(user, ["groups"])
    selected_groups = [
        group for group in user.groups
        if not selected_group_id or group.id == selected_group_id
    ]
    if selected_group_id and not selected_groups:
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="Selected group is not accessible to this user",
        )

    if not exit_node_id:
        return None
    if not user.vpn_mode:
        raise HTTPException(status_code=status.HTTP_403_FORBIDDEN, detail="VPN mode is not enabled for this user")

    from app.models.publisher import Publisher

    result = await db.execute(select(Publisher).where(Publisher.id == exit_node_id))
    publisher = result.scalar_one_or_none()
    if not publisher:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Exit node not found")
    if not publisher.exit_node or publisher.status != "online":
        raise HTTPException(status_code=status.HTTP_409_CONFLICT, detail="Exit node is not available")

    for group in selected_groups:
        await db.refresh(group, ["publishers"])
        if any(candidate.id == exit_node_id for candidate in group.publishers):
            return exit_node_id

    raise HTTPException(status_code=status.HTTP_403_FORBIDDEN, detail="Exit node is not accessible to this user")


@router.post("/renew", response_model=SessionRenewResponse)
async def renew_session(
    request: Request,
    body: SessionRenewRequest | None = None,
    group_id: str | None = FastAPIQuery(default=None, description="Selected group for CIDR overlap resolution"),
    exit_node_id: str | None = FastAPIQuery(default=None, description="Publisher ID to use as VPN exit node (full tunnel)"),
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Authenticate and obtain or reuse an active WireGuard PSK session.

    Group and VPN policy changes reuse a live PSK so the API and broker never
    expose a transient cryptographic mismatch during the reconciler poll window.
    """
    user_id = current_user.get("sub")
    if not user_id:
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid token payload")

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="User not found")
    if user.status != "active":
        raise HTTPException(status_code=status.HTTP_403_FORBIDDEN, detail="User account is not active")

    # FUTURE: Validate OIDC/MFA claims and evaluate device posture policy.
    requested_exit_node = await _validated_exit_node_id(user, group_id, exit_node_id, db)

    # Reuse a live PSK across group/mode selection changes. Routing policy can
    # change independently; rotating the cryptographic session here creates a
    # polling window where client and broker hold different PSKs.
    result = await db.execute(
        select(ClientSession)
        .where(
            ClientSession.user_id == user_id,
            ClientSession.is_active == True,
            ClientSession.expires_at > datetime.utcnow(),
        )
        .order_by(ClientSession.created_at.desc())
        .limit(1)
    )
    existing_session = result.scalar_one_or_none()

    if existing_session:
        selection_changed = (
            existing_session.exit_node_publisher_id != requested_exit_node
            or existing_session.selected_group_id != group_id
        )
        if selection_changed:
            old_group_id = existing_session.selected_group_id
            old_exit_node_id = existing_session.exit_node_publisher_id
            existing_session.exit_node_publisher_id = requested_exit_node
            existing_session.selected_group_id = group_id
            if user.overlay_ip:
                from app.routers.brokers import _dns_hints_cache
                _dns_hints_cache.pop(user.overlay_ip, None)
            client_ip = request.client.host if request.client else None
            db.add(AccessLog(
                user_id=user_id,
                resource_id=existing_session.id,
                publisher_id=requested_exit_node,
                action="session_selection_changed",
                detail=json.dumps({
                    "old": {
                        "group_id": old_group_id,
                        "exit_node_id": old_exit_node_id,
                        "mode": "vpn" if old_exit_node_id else "split",
                    },
                    "new": {
                        "group_id": group_id,
                        "exit_node_id": requested_exit_node,
                        "mode": "vpn" if requested_exit_node else "split",
                    },
                }, separators=(",", ":"), sort_keys=True),
                client_ip=client_ip,
            ))
            # Publish policy and audit atomically while retaining the installed
            # PSK so WireGuard remains cryptographically live.
            await db.commit()

        ttl = max(0, int((existing_session.expires_at - datetime.utcnow()).total_seconds()))
        is_exit = existing_session.exit_node_publisher_id is not None
        allowed_ips = _full_tunnel_allowed_ips() if is_exit else await _get_user_allowed_ips(user, group_id, db)

        return SessionRenewResponse(
            preshared_key=existing_session.preshared_key,
            session_id=existing_session.id,
            expires_at=existing_session.expires_at,
            ttl_seconds=ttl,
            allowed_ips=allowed_ips,
            is_exit_node=is_exit,
            broker_endpoint=_broker_wireguard_endpoint() if is_exit else None,
        )

    # No live session exists. Retire expired or otherwise leftover active rows
    # before creating the sole active session with a fresh PSK.
    await db.execute(
        update(ClientSession)
        .where(ClientSession.user_id == user_id, ClientSession.is_active == True)
        .values(is_active=False)
    )

    preshared_key = generate_preshared_key()
    expires_at = datetime.utcnow() + timedelta(hours=settings.session_ttl_hours)
    client_ip = request.client.host if request.client else None

    # Stale hints from a previous group would create more-specific /32 rules
    # pointing to the wrong publisher.
    if user.overlay_ip:
        from app.routers.brokers import _dns_hints_cache
        _dns_hints_cache.pop(user.overlay_ip, None)

    session = ClientSession(
        user_id=user_id,
        preshared_key=preshared_key,
        expires_at=expires_at,
        client_ip=client_ip,
        is_active=True,
        selected_group_id=group_id,
        exit_node_publisher_id=requested_exit_node,
    )
    db.add(session)
    await db.flush()

    tunnel_mode = "vpn" if requested_exit_node else "split"
    exit_detail = f", exit_node={requested_exit_node[:8]}..." if requested_exit_node else ""
    group_detail = f", group={group_id[:8]}..." if group_id else ""
    db.add(AccessLog(
        user_id=user_id,
        action="session_renew",
        detail=f"Session {session.id[:8]}... created (TTL: {settings.session_ttl_hours}h, mode={tunnel_mode}{group_detail}{exit_detail})",
        client_ip=client_ip,
    ))

    # Persist client version info if reported (backwards-compatible: old clients send no body).
    if body and (body.client_version or body.wg_version or body.platform) and user.public_key:
        peer_status = await db.get(ClientPeerStatus, user.public_key)
        if peer_status:
            if body.client_version:
                peer_status.client_version = body.client_version
            if body.wg_version:
                peer_status.wg_version = body.wg_version
            if body.platform:
                peer_status.platform = body.platform
        else:
            db.add(ClientPeerStatus(
                public_key=user.public_key,
                client_version=body.client_version,
                wg_version=body.wg_version,
                platform=body.platform,
            ))

    return await _build_renew_response(
        user=user,
        session=session,
        preshared_key=preshared_key,
        expires_at=expires_at,
        group_id=group_id,
        exit_node_id=requested_exit_node,
        db=db,
    )


async def _build_renew_response(
    user, session, preshared_key, expires_at, group_id, exit_node_id, db
) -> SessionRenewResponse:
    """Build a response from an already validated and persisted selection."""
    is_exit_node = exit_node_id is not None
    return SessionRenewResponse(
        session_id=session.id,
        preshared_key=preshared_key,
        expires_at=expires_at,
        ttl_seconds=max(0, int((expires_at - datetime.utcnow()).total_seconds())),
        allowed_ips=(
            _full_tunnel_allowed_ips()
            if is_exit_node
            else await _get_user_allowed_ips(user, group_id, db)
        ),
        is_exit_node=is_exit_node,
        broker_endpoint=_broker_wireguard_endpoint() if is_exit_node else None,
    )


@router.get("/dns-zones")
async def get_dns_zones(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Return the DNS zones the authenticated user can reach through the tunnel.

    Used by the client script to configure split DNS on macOS — only these
    zones are routed through the tunnel DNS, preserving local resolution.
    """
    from app.models.publisher import Publisher, group_publishers
    from app.models.group import Group, user_groups

    user_id = current_user.get("sub")

    # Find all publishers accessible to this user via group membership
    result = await db.execute(
        select(Publisher.dns_zones)
        .join(group_publishers, group_publishers.c.publisher_id == Publisher.id)
        .join(Group, Group.id == group_publishers.c.group_id)
        .join(user_groups, user_groups.c.group_id == Group.id)
        .where(
            user_groups.c.user_id == user_id,
            Publisher.status == "online",
            Publisher.dns_zones.isnot(None),
        )
    )

    # Flatten all zones from all accessible publishers
    zones = []
    for (dns_zones,) in result.all():
        if dns_zones:
            zones.extend(dns_zones)

    # Deduplicate
    zones = sorted(set(zones))

    return {"zones": zones}


@router.get("/me", response_model=SessionInfoResponse)
async def get_my_session(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Get current active session info for the authenticated user."""
    user_id = current_user.get("sub")

    result = await db.execute(
        select(ClientSession).where(
            ClientSession.user_id == user_id,
            ClientSession.is_active == True,
            ClientSession.expires_at > datetime.utcnow(),
        )
    )
    session = result.scalar_one_or_none()
    if not session:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail="No active session — call POST /sessions/renew",
        )

    remaining = (session.expires_at - datetime.utcnow()).total_seconds()

    # Resolve group name if a group was selected
    selected_group_name = None
    if session.selected_group_id:
        from app.models.group import Group
        group_result = await db.execute(
            select(Group.name).where(Group.id == session.selected_group_id)
        )
        group_row = group_result.first()
        if group_row:
            selected_group_name = group_row[0]

    return SessionInfoResponse(
        session_id=session.id,
        expires_at=session.expires_at,
        ttl_remaining_seconds=int(max(remaining, 0)),
        client_ip=session.client_ip,
        selected_group_id=session.selected_group_id,
        selected_group_name=selected_group_name,
    )


@router.get("/available-groups")
async def get_available_groups(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Return the groups available to the user, with overlap detection.

    Used by the client script to determine if group selection is needed.
    If no overlapping CIDRs exist between groups, selection is not required.
    """
    from app.models.publisher import Publisher, group_publishers
    from app.models.group import Group, user_groups

    user_id = current_user.get("sub")

    # Get all groups for this user with their publishers and CIDRs
    result = await db.execute(
        select(Group)
        .join(user_groups, user_groups.c.group_id == Group.id)
        .where(user_groups.c.user_id == user_id)
    )
    groups = result.scalars().all()

    group_info = []
    group_cidrs = {}  # group_id → set of CIDRs

    for group in groups:
        await db.refresh(group, ["publishers"])
        publishers_info = []
        cidrs = set()
        for pub in group.publishers:
            if pub.status in ("disabled", "pending"):
                continue
            publishers_info.append({
                "id": pub.id,
                "name": pub.name,
                "status": pub.status,
                "exposed_cidrs": pub.exposed_cidrs or [],
            })
            for cidr in (pub.exposed_cidrs or []):
                cidrs.add(cidr)

        online_count = sum(1 for p in publishers_info if p["status"] == "online")

        group_cidrs[group.id] = cidrs
        group_info.append({
            "id": group.id,
            "name": group.name,
            "description": group.description,
            "publishers": publishers_info,
            "online_publishers": online_count,
            "cidrs": sorted(cidrs),
        })

    # Detect CIDR overlaps between groups
    has_overlap = False
    overlap_details = []
    group_ids = list(group_cidrs.keys())
    for i in range(len(group_ids)):
        for j in range(i + 1, len(group_ids)):
            g1, g2 = group_ids[i], group_ids[j]
            shared = group_cidrs[g1] & group_cidrs[g2]
            if shared:
                has_overlap = True
                g1_name = next(g["name"] for g in group_info if g["id"] == g1)
                g2_name = next(g["name"] for g in group_info if g["id"] == g2)
                overlap_details.append({
                    "groups": [g1_name, g2_name],
                    "overlapping_cidrs": sorted(shared),
                })

    # Check if user has vpn_mode enabled and collect exit nodes
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    vpn_mode_enabled = user.vpn_mode if user else False

    exit_nodes = []
    if vpn_mode_enabled:
        # A publisher may be shared by several groups. Keep it in every group,
        # but expose it only once in the global exit-node selector.
        unique_exit_nodes = {}
        for group in groups:
            for pub in group.publishers:
                if pub.status in ("disabled", "pending"):
                    continue
                if getattr(pub, "exit_node", False):
                    unique_exit_nodes[pub.id] = pub

        ordered_exit_nodes = sorted(
            unique_exit_nodes.values(),
            key=lambda pub: (
                pub.publisher_index is None,
                pub.publisher_index if pub.publisher_index is not None else 0,
                pub.name.casefold(),
                pub.id,
            ),
        )
        exit_nodes = [{
            "id": pub.id,
            "name": pub.name,
            "location": pub.location,
            "status": pub.status,
            "publisher_index": pub.publisher_index,
        } for pub in ordered_exit_nodes]

    return {
        "groups": group_info,
        "has_overlap": has_overlap,
        "overlap_details": overlap_details,
        "selection_recommended": len(group_info) > 1,  # Always offer selection when multiple groups
        "allow_all": True,
        "exit_nodes": exit_nodes,
        "vpn_mode": vpn_mode_enabled,
    }


@router.delete("/{session_id}", status_code=status.HTTP_204_NO_CONTENT)
async def revoke_session(
    session_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Admin endpoint: immediately revoke a client session.

    The broker will remove the PSK on its next reconciliation cycle,
    causing the client's tunnel to die within seconds.
    """
    result = await db.execute(
        select(ClientSession).where(ClientSession.id == session_id)
    )
    session = result.scalar_one_or_none()
    if not session:
        raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="Session not found")

    session.is_active = False

    # Audit log: session revoked
    db.add(AccessLog(
        user_id=session.user_id,
        action="session_revoke",
        detail=f"Session {session.id[:8]}... revoked by admin",
    ))
