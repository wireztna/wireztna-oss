"""User self-service endpoints — read-only access to own connection data.

Non-admin users can see their own status, session, flows, and accessible publishers.
No write operations are exposed here.
"""

import ipaddress
from datetime import datetime

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select

from app.database import get_db
from app.models.user import User
from app.models.group import Group
from app.models.session import ClientSession
from app.models.client_status import ClientPeerStatus
from app.models.access_policy import GroupPublisherPolicy, AccessRule
from app.services.auth_service import get_current_user
from app.services.url_builder import external_base_url
from app.config import settings

router = APIRouter()


# ─── Schemas (inline, specific to this portal) ───

from pydantic import BaseModel


class PortalPublisher(BaseModel):
    id: str
    name: str
    status: str
    exposed_cidrs: list[str]
    via_groups: list[str]
    access_policy: str = "unrestricted"  # "unrestricted" or "restricted"
    allowed_apps: list[dict] | None = None  # [{target, port, protocol, name}] if restricted


class PortalSession(BaseModel):
    session_id: str
    expires_at: datetime
    ttl_remaining_seconds: int
    selected_group_name: str | None = None


class PortalFlowEntry(BaseModel):
    protocol: str
    dst_ip: str
    dst_port: int
    state: str
    bytes_in: int = 0
    bytes_out: int = 0
    service: str | None = None
    publisher_name: str | None = None
    reachable: bool | None = None


class PortalStatusResponse(BaseModel):
    """Everything a non-admin user needs to see about their own connection."""
    username: str
    overlay_ip: str | None
    status: str

    # Connection state
    is_connected: bool
    endpoint_ip: str | None = None
    last_handshake_at: datetime | None = None
    handshake_age_seconds: int | None = None
    rx_bytes: int = 0
    tx_bytes: int = 0

    # Session
    active_session: PortalSession | None = None

    # Access topology
    accessible_publishers: list[PortalPublisher]
    allowed_cidrs: list[str]

    # Issues (user-facing, simplified)
    issues: list[str]


class PortalFlowsResponse(BaseModel):
    overlay_ip: str | None
    total_flows: int
    flows: list[PortalFlowEntry]


# ─── Endpoints ───

@router.get("", response_model=PortalStatusResponse)
async def get_my_status(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Get your own connection status, session, and accessible publishers."""
    user_id = current_user.get("sub")

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    issues: list[str] = []

    # ─── Peer status ───
    peer_status = None
    if user.public_key:
        status_result = await db.execute(
            select(ClientPeerStatus).where(ClientPeerStatus.public_key == user.public_key)
        )
        peer_status = status_result.scalar_one_or_none()

    is_connected = peer_status.is_connected if peer_status else False
    last_handshake = peer_status.last_handshake_at if peer_status else None
    handshake_age = None
    if last_handshake:
        handshake_age = int((datetime.utcnow() - last_handshake).total_seconds())
        if handshake_age > 180:
            issues.append("Tunnel may be stale — last handshake was over 3 minutes ago")

    if not user.public_key:
        issues.append("Device not enrolled — run 'wireztna enroll' with your enrollment token")

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

        selected_group_name = None
        if active_session_obj.selected_group_id:
            group_result = await db.execute(
                select(Group.name).where(Group.id == active_session_obj.selected_group_id)
            )
            group_row = group_result.first()
            if group_row:
                selected_group_name = group_row[0]

        active_session = PortalSession(
            session_id=active_session_obj.id,
            expires_at=active_session_obj.expires_at,
            ttl_remaining_seconds=int(max(remaining, 0)),
            selected_group_name=selected_group_name,
        )

        if remaining < 300:
            issues.append("Session expires soon — your client should auto-renew")
    else:
        if user.public_key:
            issues.append("No active session — run 'wireztna connect' to establish tunnel")

    # ─── Accessible publishers ───
    await db.refresh(user, ["groups"])

    accessible_publishers: list[PortalPublisher] = []
    all_cidrs: set[str] = set()
    publisher_seen: dict[str, PortalPublisher] = {}

    for group in user.groups:
        await db.refresh(group, ["publishers"])
        for publisher in group.publishers:
            if publisher.status in ("disabled", "pending"):
                continue
            for cidr in (publisher.exposed_cidrs or []):
                all_cidrs.add(cidr)

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

            if publisher.id not in publisher_seen:
                pub = PortalPublisher(
                    id=publisher.id,
                    name=publisher.name,
                    status=publisher.status,
                    exposed_cidrs=publisher.exposed_cidrs or [],
                    via_groups=[group.name],
                    access_policy=access_policy,
                    allowed_apps=allowed_apps,
                )
                publisher_seen[publisher.id] = pub
                accessible_publishers.append(pub)
            else:
                publisher_seen[publisher.id].via_groups.append(group.name)

    if not accessible_publishers:
        issues.append("No accessible networks — contact your administrator")

    # Check for offline publishers
    offline_pubs = [p.name for p in accessible_publishers if p.status == "offline"]
    if offline_pubs:
        issues.append(f"Some networks may be unreachable (publishers offline: {', '.join(offline_pubs)})")

    return PortalStatusResponse(
        username=user.username,
        overlay_ip=user.overlay_ip,
        status=user.status,
        is_connected=is_connected,
        endpoint_ip=peer_status.endpoint_ip if peer_status else None,
        last_handshake_at=last_handshake,
        handshake_age_seconds=handshake_age,
        rx_bytes=peer_status.rx_bytes if peer_status else 0,
        tx_bytes=peer_status.tx_bytes if peer_status else 0,
        active_session=active_session,
        accessible_publishers=accessible_publishers,
        allowed_cidrs=sorted(all_cidrs),
        issues=issues,
    )


@router.get("/flows", response_model=PortalFlowsResponse)
async def get_my_flows(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Get your own active network flows."""
    from app.routers.brokers import get_flows_for_ip

    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    if not user.overlay_ip:
        return PortalFlowsResponse(overlay_ip=None, total_flows=0, flows=[])

    raw_flows = get_flows_for_ip(user.overlay_ip)

    # Build publisher lookup (filtered by session group)
    publisher_networks: list[tuple[ipaddress.IPv4Network, str, int]] = []

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

    publisher_networks.sort(key=lambda x: x[2], reverse=True)

    def _find_publisher(dst_ip: str) -> str | None:
        try:
            addr = ipaddress.IPv4Address(dst_ip)
            for net, name, _ in publisher_networks:
                if addr in net:
                    return name
        except ValueError:
            pass
        return None

    # Well-known ports
    _PORT_SERVICES = {
        22: "SSH", 80: "HTTP", 443: "HTTPS", 3389: "RDP", 3306: "MySQL",
        5432: "PostgreSQL", 6379: "Redis", 8080: "HTTP-Alt", 53: "DNS",
    }

    def _is_reachable(state: str, bytes_in: int, packets_out: int, packets_in: int) -> bool | None:
        if state in ("ESTABLISHED", "TIME_WAIT", "FIN_WAIT", "CLOSE_WAIT", "LAST_ACK", "CLOSE"):
            return True
        if state in ("UNREPLIED", "SYN_SENT"):
            return False
        if packets_out > 0 and packets_in == 0:
            return False
        if packets_out > 0 and packets_in > 0:
            return (packets_in / packets_out) >= 0.1
        if state == "ACTIVE":
            if bytes_in > 0:
                return True
            if packets_out > 3:
                return False
        return None

    flows = []
    for raw in raw_flows:
        dst_port = raw.get("dst_port", 0)
        state = raw.get("state", "UNKNOWN")
        bytes_in = raw.get("bytes_in", 0)
        packets_out = raw.get("packets_out", 0)
        packets_in = raw.get("packets_in", 0)
        flows.append(PortalFlowEntry(
            protocol=raw.get("protocol", "unknown"),
            dst_ip=raw.get("dst_ip", ""),
            dst_port=dst_port,
            state=state,
            bytes_in=bytes_in,
            bytes_out=raw.get("bytes_out", 0),
            service=_PORT_SERVICES.get(dst_port),
            publisher_name=_find_publisher(raw.get("dst_ip", "")),
            reachable=_is_reachable(state, bytes_in, packets_out, packets_in),
        ))

    flows.sort(key=lambda f: f.bytes_in + f.bytes_out, reverse=True)

    return PortalFlowsResponse(
        overlay_ip=user.overlay_ip,
        total_flows=len(flows),
        flows=flows,
    )


# ─── Self-Service Schemas ───

class UpdateEmailRequest(BaseModel):
    email: str
    current_password: str


class ChangePasswordRequest(BaseModel):
    current_password: str
    new_password: str


class MyProfileResponse(BaseModel):
    id: str
    username: str
    email: str
    overlay_ip: str | None
    status: str
    is_admin: bool
    vpn_mode: bool
    max_enrollment_tokens: int
    must_change_password: bool = False
    created_at: datetime

    class Config:
        from_attributes = True


class MyTokenResponse(BaseModel):
    id: str
    token: str | None = None  # Only shown on creation
    status: str  # "pending", "used", "expired", "revoked"
    note: str | None = None
    created_at: datetime
    expires_at: datetime | None = None
    used_at: datetime | None = None
    device_name: str | None = None  # Hostname of enrolled device
    device_platform: str | None = None  # e.g. "darwin/arm64"

    class Config:
        from_attributes = True


class MyAccessGroup(BaseModel):
    id: str
    name: str
    description: str | None
    publishers: list[PortalPublisher]


class MyAccessResponse(BaseModel):
    groups: list[MyAccessGroup]
    total_cidrs: list[str]
    dns_zones: list[str]


# ─── Self-Service Endpoints ───

@router.get("/profile", response_model=MyProfileResponse)
async def get_my_profile(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Get own profile data."""
    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    return user


@router.put("/email")
async def update_my_email(
    data: UpdateEmailRequest,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Update own email address.

    Requires current password for re-authentication (step-up).
    OIDC-only users cannot change email via this endpoint.
    Validates email format and normalizes canonical_email for dedup.
    """
    from app.services.auth_service import verify_password
    from app.services.audit_service import log_action
    from app.services.email_normalization import validate_email_for_registration, normalize_email

    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # OIDC-only users must change email through their identity provider
    if user.auth_provider == "oidc":
        raise HTTPException(
            status_code=400,
            detail="Email is managed by your identity provider — change it there instead",
        )

    # Step-up: require current password (mirrors PUT /password behaviour)
    if not user.password_hash:
        raise HTTPException(status_code=400, detail="No password set — contact admin")
    if not verify_password(data.current_password, user.password_hash):
        raise HTTPException(status_code=401, detail="Current password is incorrect")

    # Validate email format (reuse registration-grade validation)
    new_email = data.email.strip().lower()
    is_valid, err_msg = validate_email_for_registration(new_email)
    if not is_valid:
        raise HTTPException(status_code=422, detail=err_msg)

    # Case-insensitive uniqueness check
    if new_email != user.email.lower():
        existing = await db.execute(
            select(User).where(User.email.ilike(new_email))
        )
        if existing.scalar_one_or_none():
            raise HTTPException(status_code=409, detail="Email already in use by another account")

    old_email = user.email
    user.email = new_email
    user.canonical_email = normalize_email(new_email)
    await log_action(
        db, "portal_email_changed",
        detail=f"{user.username} changed email from {old_email} to {user.email}",
        user_id=user_id,
    )
    await db.commit()
    return {"message": "Email updated", "email": user.email}


@router.put("/password")
async def change_my_password(
    data: ChangePasswordRequest,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Change own password. Skips current password verification during forced change (onboarding)."""
    from app.services.auth_service import verify_password, hash_password
    from app.services.audit_service import log_action

    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Skip current password check if user must change password (onboarding flow)
    if not user.must_change_password:
        # Normal flow: verify current password
        if not user.password_hash:
            raise HTTPException(status_code=400, detail="No password set — contact admin")
        if not verify_password(data.current_password, user.password_hash):
            raise HTTPException(status_code=401, detail="Current password is incorrect")

    if len(data.new_password) < 6:
        raise HTTPException(status_code=400, detail="New password must be at least 6 characters")

    # Validate password complexity (A.5.17 / CC6.1)
    from app.services.password_validation import validate_password
    pw_valid, pw_error = validate_password(data.new_password)
    if not pw_valid:
        raise HTTPException(status_code=400, detail=pw_error)

    user.password_hash = hash_password(data.new_password)
    user.must_change_password = False
    await log_action(db, "portal_password_changed", detail=f"{user.username} changed their password", user_id=user_id)
    await db.commit()
    return {"message": "Password changed successfully"}


@router.get("/tokens", response_model=list[MyTokenResponse])
async def list_my_tokens(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """List own enrollment tokens (used and pending)."""
    from app.models.client_enrollment import ClientEnrollmentToken

    user_id = current_user.get("sub")
    result = await db.execute(
        select(ClientEnrollmentToken)
        .where(ClientEnrollmentToken.user_id == user_id)
        .order_by(ClientEnrollmentToken.created_at.desc())
    )
    tokens = result.scalars().all()

    response = []
    for t in tokens:
        if t.used_at:
            status = "used"
        elif t.revoked:
            status = "revoked"
        elif t.expires_at and t.expires_at < datetime.utcnow():
            status = "expired"
        else:
            status = "pending"

        response.append(MyTokenResponse(
            id=t.id,
            token=None,  # Never show the actual token after creation
            status=status,
            note=t.note,
            created_at=t.created_at,
            expires_at=t.expires_at,
            used_at=t.used_at,
            device_name=t.device_name,
            device_platform=t.device_platform,
        ))

    return response


@router.post("/tokens", response_model=MyTokenResponse, status_code=201)
async def generate_my_token(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Generate a self-service enrollment token (within quota)."""
    import secrets
    from app.models.client_enrollment import ClientEnrollmentToken
    from datetime import timedelta

    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Count non-revoked tokens (used + pending + expired all count against quota)
    token_result = await db.execute(
        select(ClientEnrollmentToken).where(
            ClientEnrollmentToken.user_id == user_id,
            ClientEnrollmentToken.revoked == False,
        )
    )
    existing_count = len(token_result.scalars().all())

    if existing_count >= user.max_enrollment_tokens:
        raise HTTPException(
            status_code=403,
            detail=f"Token quota reached ({user.max_enrollment_tokens}). Contact your admin for more.",
        )

    # Generate token
    token_secret = secrets.token_urlsafe(32)
    new_token = ClientEnrollmentToken(
        user_id=user_id,
        token=token_secret,
        expires_at=datetime.utcnow() + timedelta(hours=24),
        note="Self-service enrollment",
    )
    db.add(new_token)
    await db.flush()
    await db.refresh(new_token)

    # Audit
    from app.services.audit_service import log_action
    await log_action(db, "portal_token_generated", detail=f"{user.username} generated a self-service enrollment token", user_id=user_id)
    await db.commit()

    # Build a GUI-compatible user-facing enrollment URL.
    if settings.ui_public_domain:
        base_url = f"{settings.broker_public_scheme}://{settings.ui_public_domain}"
    else:
        base_url = external_base_url()
    enroll_url = f"{base_url}/api/v1/clients/enroll?token={token_secret}"

    return MyTokenResponse(
        id=new_token.id,
        token=enroll_url,  # Show full URL only on creation
        status="pending",
        note=new_token.note,
        created_at=new_token.created_at,
        expires_at=new_token.expires_at,
        used_at=None,
    )


@router.get("/access", response_model=MyAccessResponse)
async def get_my_access(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Get detailed view of groups, publishers, CIDRs, and DNS zones accessible."""
    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    await db.refresh(user, ["groups"])

    groups_data: list[MyAccessGroup] = []
    all_cidrs: set[str] = set()
    all_dns_zones: set[str] = set()

    for group in user.groups:
        await db.refresh(group, ["publishers"])
        publishers: list[PortalPublisher] = []

        for publisher in group.publishers:
            if publisher.status in ("disabled", "pending"):
                continue

            for cidr in (publisher.exposed_cidrs or []):
                all_cidrs.add(cidr)
            for zone in (publisher.dns_zones or []):
                all_dns_zones.add(zone)

            # Load access policy
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

            publishers.append(PortalPublisher(
                id=publisher.id,
                name=publisher.name,
                status=publisher.status,
                exposed_cidrs=publisher.exposed_cidrs or [],
                via_groups=[group.name],
                access_policy=access_policy,
                allowed_apps=allowed_apps,
            ))

        groups_data.append(MyAccessGroup(
            id=group.id,
            name=group.name,
            description=group.description,
            publishers=publishers,
        ))

    return MyAccessResponse(
        groups=groups_data,
        total_cidrs=sorted(all_cidrs),
        dns_zones=sorted(all_dns_zones),
    )


# ─── API Key Management ───

class ApiKeyResponse(BaseModel):
    id: str
    key_prefix: str
    label: str
    status: str
    last_used_at: datetime | None
    created_at: datetime

    class Config:
        from_attributes = True


class ApiKeyCreatedResponse(BaseModel):
    api_key: str  # Plaintext — shown only once
    id: str


@router.get("/api-keys", response_model=list[ApiKeyResponse])
async def list_my_api_keys(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """List all API keys for the current user (metadata only, not the key itself)."""
    from app.models.api_key import ApiKey

    user_id = current_user.get("sub")
    result = await db.execute(
        select(ApiKey)
        .where(ApiKey.user_id == user_id)
        .order_by(ApiKey.created_at.desc())
    )
    keys = result.scalars().all()
    return keys


@router.delete("/api-keys/{key_id}")
async def revoke_my_api_key(
    key_id: str,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Revoke a specific API key."""
    from app.services.apikey_service import revoke_api_key

    user_id = current_user.get("sub")
    success = await revoke_api_key(key_id, user_id, db)
    if not success:
        raise HTTPException(status_code=404, detail="API key not found or already revoked")
    return {"message": "API key revoked"}


@router.post("/api-keys/rotate", response_model=ApiKeyCreatedResponse, status_code=201)
async def rotate_my_api_keys(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Revoke all existing API keys and create a new one."""
    from app.services.apikey_service import revoke_all_keys, create_api_key_for_user

    user_id = current_user.get("sub")
    await revoke_all_keys(user_id, db)
    plaintext = await create_api_key_for_user(user_id, db, label="rotated")
    await db.commit()

    # Get the new key's ID
    from app.models.api_key import ApiKey
    from app.services.apikey_service import hash_api_key
    result = await db.execute(
        select(ApiKey.id).where(ApiKey.key_hash == hash_api_key(plaintext))
    )
    key_id = result.scalar_one()

    return ApiKeyCreatedResponse(api_key=plaintext, id=key_id)


# ─── Publisher Enrollment Token (self-service for free tier) ───

class PublisherTokenRequest(BaseModel):
    name: str | None = None  # Optional publisher name (defaults to hostname at enrollment)
    exposed_cidrs: list[str] | None = None  # Optional pre-configured CIDRs


class PublisherTokenResponse(BaseModel):
    token_url: str  # Full enrollment URL ready to paste


@router.post("/publisher-tokens", response_model=PublisherTokenResponse, status_code=201)
async def create_publisher_enrollment_token(
    body: PublisherTokenRequest | None = None,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Generate a publisher enrollment token (self-service).

    The enrolled publisher will be automatically associated to the user's
    first group. For free tier users, this is their personal group.
    """
    import secrets
    from datetime import timedelta
    from app.models.publisher import EnrollmentToken
    from app.models.group import user_groups

    user_id = current_user.get("sub")

    # Find user's first group (for auto-association)
    from sqlalchemy import select as sa_select
    group_result = await db.execute(
        sa_select(user_groups.c.group_id).where(user_groups.c.user_id == user_id).limit(1)
    )
    group_row = group_result.first()
    if not group_row:
        raise HTTPException(status_code=400, detail="No group found — cannot create publisher token")
    auto_group_id = group_row[0]

    # Create publisher enrollment token
    token_value = secrets.token_urlsafe(32)
    token = EnrollmentToken(
        token=token_value,
        publisher_name=body.name if body else None,
        exposed_cidrs=body.exposed_cidrs if body else None,
        expires_at=datetime.utcnow() + timedelta(hours=24),
        created_by_user_id=user_id,
        auto_group_id=auto_group_id,
    )
    db.add(token)
    await db.commit()

    # Build enrollment URL
    token_url = f"{external_base_url()}/api/v1/publishers/enroll?token={token_value}"

    return PublisherTokenResponse(token_url=token_url)
