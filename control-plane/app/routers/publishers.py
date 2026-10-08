"""Publishers — CRUD, enrollment, and heartbeat."""

import secrets
import subprocess
import time
import json
from datetime import datetime, timedelta
from pathlib import Path

from fastapi import APIRouter, Depends, HTTPException, Header, Request, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, func

from app.database import get_db
from app.models.publisher import Publisher, EnrollmentToken
from app.schemas.schemas import (
    PublisherCreate, PublisherUpdate, PublisherResponse,
    PublisherEnrollRequest, PublisherEnrollResponse,
    PublisherHeartbeat, EnrollmentTokenCreate, EnrollmentTokenResponse,
    NamespaceInfoUpdate,
)
from app.services.auth_service import require_admin, require_internal, require_publisher_auth, scoped_query, resolve_org_for_creation
from app.services.wireguard_service import TunnelIPAllocator
from app.services.org_service import resolve_org_id
from app.services.url_builder import external_base_url
from app.config import settings

router = APIRouter()


# ─── CRUD ───

@router.get("", response_model=list[PublisherResponse])
async def list_publishers(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
    org_id: str | None = None,
):
    query = select(Publisher).order_by(Publisher.name)
    query = scoped_query(query, Publisher, current_user, org_id)
    result = await db.execute(query)
    return result.scalars().all()


@router.get("/handshakes")
async def get_publisher_handshakes(_=Depends(require_admin)):
    """Return real-time WG handshake age for each publisher namespace.

    Reads directly from the kernel via `ip netns exec ns-X wg show` —
    this is the ground truth of tunnel liveness, not a cached DB value.
    Returns dict of publisher_id → handshake_age_seconds (null if no handshake).
    """
    ns_map_file = Path("/etc/wireztna/namespace-map.json")
    if not ns_map_file.exists():
        return {}

    try:
        ns_map = json.loads(ns_map_file.read_text())
    except (json.JSONDecodeError, OSError):
        return {}

    result = {}
    for ns_name, ns_info in ns_map.items():
        pub_id = ns_info.get("publisher_id")
        if not pub_id:
            continue

        # Get WG interface name inside namespace
        try:
            iface_result = subprocess.run(
                ["ip", "netns", "exec", ns_name, "wg", "show", "interfaces"],
                capture_output=True, text=True, timeout=5,
            )
            if iface_result.returncode != 0 or not iface_result.stdout.strip():
                result[pub_id] = None
                continue

            wg_if = iface_result.stdout.strip().split()[0]

            # Get peer dump
            dump_result = subprocess.run(
                ["ip", "netns", "exec", ns_name, "wg", "show", wg_if, "dump"],
                capture_output=True, text=True, timeout=5,
            )
            if dump_result.returncode != 0:
                result[pub_id] = None
                continue

            # Parse handshake from first peer (publisher has one peer = the publisher itself)
            lines = dump_result.stdout.strip().split("\n")
            if len(lines) < 2:
                result[pub_id] = None
                continue

            parts = lines[1].split("\t")
            if len(parts) >= 5:
                latest_handshake = int(parts[4]) if parts[4] != "0" else 0
                if latest_handshake > 0:
                    result[pub_id] = int(time.time()) - latest_handshake
                else:
                    result[pub_id] = None
            else:
                result[pub_id] = None

        except (subprocess.TimeoutExpired, OSError):
            result[pub_id] = None

    return result


@router.post("", response_model=PublisherResponse, status_code=status.HTTP_201_CREATED)
async def create_publisher(pub: PublisherCreate, db: AsyncSession = Depends(get_db), current_user: dict = Depends(require_admin), org_id: str | None = None):
    """Create a publisher entry (before enrollment). The publisher will self-enroll later."""
    # Auto-assign publisher_index and virtual_cidr
    index = await Publisher.next_publisher_index(db)
    virtual_cidr = Publisher.compute_virtual_cidr(index)

    # Resolve org_id: org_admin is forced to their own org, super_admin uses explicit or default
    effective_org_id = resolve_org_for_creation(current_user, org_id)
    if not effective_org_id:
        effective_org_id = await resolve_org_id(None, db)

    new_pub = Publisher(
        name=pub.name,
        location=pub.location,
        description=pub.description,
        exposed_cidrs=pub.exposed_cidrs or [],
        dns_server=pub.dns_server,
        dns_zones=pub.dns_zones,
        publisher_index=index,
        virtual_cidr=virtual_cidr,
        org_id=effective_org_id,
    )
    db.add(new_pub)
    await db.flush()
    await db.refresh(new_pub)
    return new_pub


# ─── Publisher install script and assets (MUST be before /{publisher_id} routes) ───

# Publisher version — update when deploying new publisher scripts/images
PUBLISHER_VERSION = "0.4.1"


@router.get("/install.sh")
async def get_install_script(request: Request):
    """Serves the auto-installer script for publishers.

    Usage: curl -sf http://<broker>:8443/api/v1/publishers/install.sh | ENROLLMENT_TOKEN=xxx bash
    No auth required — the token is the authorization.
    """
    from fastapi.responses import PlainTextResponse
    from pathlib import Path

    script_path = Path(__file__).resolve().parent.parent.parent / "static" / "install-publisher.sh"
    if not script_path.exists():
        raise HTTPException(status_code=404, detail="Install script not found")

    content = script_path.read_text()

    # The CONTROL_PLANE_URL embedded in the script is used by the publisher container
    # for enrollment, heartbeats, and script downloads. It must be directly reachable.
    # Derived from the configured scheme + public endpoint; falls back to localhost dev.
    if settings.broker_public_endpoint:
        broker_url = external_base_url()
    else:
        broker_url = "http://localhost:8443"

    content = content.replace("__CONTROL_PLANE_URL__", broker_url)
    content = content.replace("__AGENT_VERSION__", PUBLISHER_VERSION)

    return PlainTextResponse(content, media_type="text/plain")


@router.get("/upgrade.sh")
async def get_upgrade_script(request: Request):
    """Serves the upgrade script for existing Docker publishers.

    Usage: curl -sf http://<broker>/api/v1/publishers/upgrade.sh | sudo bash

    No auth required — the script needs to run on the publisher host where
    Docker access is the authorization. It reads config from the existing
    container, rebuilds the image with fresh scripts, and restarts.
    """
    from fastapi.responses import PlainTextResponse
    from pathlib import Path

    script_path = Path(__file__).resolve().parent.parent.parent / "static" / "upgrade-publisher.sh"
    if not script_path.exists():
        raise HTTPException(status_code=404, detail="Upgrade script not found")

    content = script_path.read_text()

    if settings.broker_public_endpoint:
        broker_url = external_base_url()
    else:
        broker_url = "http://localhost:8443"

    content = content.replace("__CONTROL_PLANE_URL__", broker_url)
    content = content.replace("__PUBLISHER_VERSION__", PUBLISHER_VERSION)

    return PlainTextResponse(content, media_type="text/plain")


@router.get("/scripts/{script_name}")
async def get_publisher_script(script_name: str):
    """Serves individual publisher scripts (used by the installer).

    No auth required — scripts are useless without a valid enrollment token.
    """
    from fastapi.responses import PlainTextResponse
    from pathlib import Path

    allowed = {"entrypoint.sh", "enroll.sh", "heartbeat.sh"}
    if script_name not in allowed:
        raise HTTPException(status_code=404, detail="Script not found")

    scripts_dir = Path(__file__).resolve().parent.parent.parent / "publisher" / "scripts"
    script_path = scripts_dir / script_name
    if not script_path.exists():
        script_path = Path("/opt/wireztna/publisher/scripts") / script_name
    if not script_path.exists():
        raise HTTPException(status_code=404, detail=f"Script '{script_name}' not found")

    return PlainTextResponse(script_path.read_text(), media_type="text/plain")


@router.get("/{publisher_id}", response_model=PublisherResponse)
async def get_publisher(publisher_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")
    return pub


@router.put("/{publisher_id}", response_model=PublisherResponse)
async def update_publisher(publisher_id: str, update: PublisherUpdate, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")

    for field, value in update.model_dump(exclude_unset=True).items():
        setattr(pub, field, value)

    await db.flush()
    await db.refresh(pub)
    return pub


@router.delete("/{publisher_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_publisher(publisher_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")
    await db.delete(pub)


# ─── Enrollment tokens ───

@router.post("/enrollment-tokens", response_model=EnrollmentTokenResponse, status_code=status.HTTP_201_CREATED)
async def create_enrollment_token(req: EnrollmentTokenCreate, db: AsyncSession = Depends(get_db), current_user: dict = Depends(require_admin)):
    """Generate a one-time enrollment token for a new publisher."""
    token = EnrollmentToken(
        token=secrets.token_urlsafe(32),
        publisher_name=req.publisher_name,
        exposed_cidrs=req.exposed_cidrs,
        location=req.location,
        expires_at=datetime.utcnow() + timedelta(hours=req.expires_in_hours),
        created_by_user_id=current_user.get("sub"),
    )
    db.add(token)
    await db.flush()
    await db.refresh(token)
    return token


# ─── Publisher self-enrollment ───

@router.post("/enroll", response_model=PublisherEnrollResponse, status_code=status.HTTP_201_CREATED)
async def enroll_publisher(req: PublisherEnrollRequest, db: AsyncSession = Depends(get_db)):
    """Publisher calls this on first boot with its enrollment token."""
    # Validate token
    result = await db.execute(
        select(EnrollmentToken).where(
            EnrollmentToken.token == req.token,
            EnrollmentToken.used_by.is_(None),
        )
    )
    token = result.scalar_one_or_none()
    if not token:
        raise HTTPException(status_code=401, detail="Invalid or already used token")
    if token.expires_at < datetime.utcnow():
        raise HTTPException(status_code=401, detail="Token expired")

    # Count existing publishers for tunnel IP allocation
    result = await db.execute(select(func.count()).select_from(Publisher))
    pub_index = result.scalar()

    # Allocate tunnel IPs (site_id_num=1 for now — single-tier)
    broker_tunnel_ip, publisher_tunnel_ip = TunnelIPAllocator.get_tunnel_ips(1, pub_index)

    # Auto-assign publisher_index and virtual_cidr
    publisher_index = await Publisher.next_publisher_index(db)
    virtual_cidr = Publisher.compute_virtual_cidr(publisher_index)

    # Create publisher
    publisher = Publisher(
        name=token.publisher_name or req.name,
        location=token.location,
        exposed_cidrs=token.exposed_cidrs or req.exposed_cidrs or [],
        public_key=req.public_key,
        tunnel_ip=publisher_tunnel_ip,
        status="online",
        enrolled_at=datetime.utcnow(),
        last_heartbeat=datetime.utcnow(),
        publisher_index=publisher_index,
        virtual_cidr=virtual_cidr,
    )

    # Inherit org_id from the user who created the token (free tier auto-association)
    if token.created_by_user_id:
        from app.models.user import User
        user_result = await db.execute(
            select(User.org_id).where(User.id == token.created_by_user_id)
        )
        user_org = user_result.scalar_one_or_none()
        if user_org:
            publisher.org_id = user_org

    db.add(publisher)
    await db.flush()

    # Mark token as used
    token.used_by = publisher.id

    # Auto-associate publisher to group if token specifies one (free tier self-service)
    if token.auto_group_id:
        from app.models.publisher import group_publishers
        from sqlalchemy import insert
        await db.execute(
            insert(group_publishers).values(
                group_id=token.auto_group_id,
                publisher_id=publisher.id,
            )
        )

    # Update DNS if publisher provides it
    if req.local_dns and not publisher.dns_server:
        publisher.dns_server = req.local_dns

    await db.flush()
    await db.refresh(publisher)

    # Compute correct tunnel IPs based on publisher_index
    publisher_tunnel_ip = f"10.100.{publisher_index}.2"
    broker_tunnel_ip_computed = f"10.100.{publisher_index}.1"

    # Update tunnel_ip in DB with the correct value
    publisher.tunnel_ip = publisher_tunnel_ip
    await db.flush()

    # Generate publisher API key for authenticating heartbeat/connection-info calls
    from app.services.publisher_key_service import assign_publisher_key
    publisher_api_key = await assign_publisher_key(publisher, db)

    # Audit log: publisher enrolled with API key
    from app.models.audit import AccessLog
    db.add(AccessLog(
        publisher_id=publisher.id,
        action="publisher_enrolled",
        detail=f"Publisher '{publisher.name}' enrolled with API key (wpk_...{publisher_api_key[-6:]})",
    ))

    # Broker connection info (placeholder — real values come from /connection-info after reconciler creates namespace)
    broker_pubkey = settings.broker_public_key or "PENDING"
    broker_endpoint = f"{settings.broker_public_endpoint}:{settings.broker_wg_port}" if settings.broker_public_endpoint else f"127.0.0.1:{settings.broker_wg_port}"

    # Build connection-info URL for polling (port 80, nginx proxies to internal 8443)
    base_url = external_base_url() if settings.broker_public_endpoint else "http://127.0.0.1:8443"
    connection_info_url = f"{base_url}/api/v1/publishers/{publisher.id}/connection-info"

    return PublisherEnrollResponse(
        publisher_id=publisher.id,
        publisher_api_key=publisher_api_key,
        broker_public_key=broker_pubkey,
        broker_endpoint=broker_endpoint,
        tunnel_ip=publisher_tunnel_ip,
        broker_tunnel_ip=broker_tunnel_ip_computed,
        allowed_ips="10.200.0.0/16,10.100.0.0/16",
        connection_info_url=connection_info_url,
        poll_for_connection=True,
    )


# ─── Publisher connection info (polled after enrollment) ───

@router.get("/{publisher_id}/connection-info")
async def get_connection_info(
    publisher_id: str,
    db: AsyncSession = Depends(get_db),
    _auth=Depends(require_publisher_auth),
):
    """Returns the broker namespace connection details for a publisher.

    The publisher polls this after enrollment until the reconciler creates
    the namespace and populates the connection fields.

    Returns 200 with ready=true when namespace is ready.
    Returns 200 with ready=false if still waiting for reconciler.
    """
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    publisher = result.scalar_one_or_none()
    if not publisher:
        raise HTTPException(status_code=404, detail="Publisher not found")

    # Check if namespace connection info is available
    if publisher.broker_ns_public_key and publisher.broker_ns_port and publisher.broker_tunnel_ip:
        broker_endpoint = (
            f"{settings.broker_public_endpoint}:{publisher.broker_ns_port}"
            if settings.broker_public_endpoint
            else f"127.0.0.1:{publisher.broker_ns_port}"
        )
        return {
            "ready": True,
            "broker_public_key": publisher.broker_ns_public_key,
            "broker_endpoint": broker_endpoint,
            "broker_tunnel_ip": publisher.broker_tunnel_ip,
            "tunnel_ip": publisher.tunnel_ip,
            "publisher_index": publisher.publisher_index,
            "allowed_ips": "10.200.0.0/16,10.100.0.0/16",
        }
    else:
        return {
            "ready": False,
            "message": "Waiting for broker to create namespace. Retry in 5 seconds.",
            "retry_after": 5,
        }


@router.post("/{publisher_id}/namespace-info", status_code=status.HTTP_204_NO_CONTENT)
async def update_namespace_info(
    publisher_id: str,
    info: NamespaceInfoUpdate,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_internal),
):
    """Internal endpoint: reconciler reports namespace connection details after creation.

    Called by the broker reconciler after it creates a publisher namespace and
    its WireGuard interface. Stores the namespace public key, listen port,
    and broker-side tunnel IP so the publisher can retrieve them via /connection-info.
    """
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    publisher = result.scalar_one_or_none()
    if not publisher:
        raise HTTPException(status_code=404, detail="Publisher not found")

    publisher.broker_ns_public_key = info.broker_ns_public_key
    publisher.broker_ns_port = info.broker_ns_port
    publisher.broker_tunnel_ip = info.broker_tunnel_ip
    await db.flush()


# ─── Actions (reset, disable, enable) ───

@router.post("/{publisher_id}/reset", status_code=status.HTTP_204_NO_CONTENT)
async def reset_publisher(publisher_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Force a publisher to reconnect by resetting its status to pending.

    The reconciler will destroy the stale namespace on the next cycle,
    and the publisher's persistent-keepalive will re-initiate a handshake.
    Once the publisher sends a heartbeat again, it will automatically
    transition from pending → online (unlike disable, which requires
    explicit /enable).
    """
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")

    pub.endpoint = None
    pub.status = "pending"

    from app.models.audit import AccessLog
    db.add(AccessLog(
        publisher_id=publisher_id,
        action="publisher_reset",
        detail=f"Publisher '{pub.name}' reset requested — will force reconnection. "
               f"Publisher will auto-recover on next heartbeat.",
    ))


@router.post("/{publisher_id}/disable", status_code=status.HTTP_204_NO_CONTENT)
async def disable_publisher(publisher_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Disable a publisher — stops routing traffic to it."""
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")
    pub.status = "disabled"

    from app.models.audit import AccessLog
    db.add(AccessLog(
        publisher_id=publisher_id,
        action="publisher_disabled",
        detail=f"Publisher '{pub.name}' disabled",
    ))


@router.post("/{publisher_id}/enable", status_code=status.HTTP_204_NO_CONTENT)
async def enable_publisher(publisher_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Re-enable a disabled publisher."""
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")
    pub.status = "online"

    from app.models.audit import AccessLog
    db.add(AccessLog(
        publisher_id=publisher_id,
        action="publisher_enabled",
        detail=f"Publisher '{pub.name}' re-enabled",
    ))


# ─── API Key Management ───

@router.post("/{publisher_id}/rotate-key")
async def rotate_publisher_key(
    publisher_id: str,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
):
    """Admin: generate a new API key for a publisher (rotation or first-time assignment).

    Returns the plaintext key exactly once. The admin must deliver it to the
    publisher operator (update env var / config file, then restart the publisher).

    Use cases:
    - Assign a key to a legacy publisher that was enrolled before auth was added.
    - Rotate a compromised key.
    - Re-key after an operator leaves.
    """
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")

    had_key = pub.api_key_hash is not None

    from app.services.publisher_key_service import assign_publisher_key
    plaintext_key = await assign_publisher_key(pub, db)

    from app.models.audit import AccessLog
    action = "publisher_key_rotated" if had_key else "publisher_key_assigned"
    db.add(AccessLog(
        publisher_id=publisher_id,
        action=action,
        detail=(
            f"Publisher '{pub.name}' API key {'rotated' if had_key else 'assigned'} "
            f"by {current_user.get('email', current_user.get('sub', 'unknown'))} "
            f"(wpk_...{plaintext_key[-6:]})"
        ),
    ))

    return {
        "publisher_id": pub.id,
        "publisher_name": pub.name,
        "publisher_api_key": plaintext_key,
        "rotated": had_key,
        "message": "Store this key securely — it will not be shown again.",
    }


@router.post("/{publisher_id}/upgrade-key")
async def upgrade_publisher_key(
    publisher_id: str,
    request: Request,
    db: AsyncSession = Depends(get_db),
):
    """Publisher self-service: obtain an API key without admin intervention.

    This enables a zero-downtime upgrade path for legacy publishers:
    1. Deploy new publisher code that calls this endpoint on startup.
    2. If the publisher has no key yet, it gets one automatically.
    3. Publisher stores the key and uses it for subsequent heartbeats.

    Security constraints:
    - Only works if the publisher does NOT already have a key (one-time upgrade).
    - Caller must be from the publisher's registered endpoint IP or localhost.
      This ensures only the actual publisher (which has a WG tunnel to prove
      its identity) can claim its key.
    """
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")

    # Only allow if no key is configured yet (one-time upgrade, not rotation)
    if pub.api_key_hash is not None:
        raise HTTPException(
            status_code=status.HTTP_409_CONFLICT,
            detail="Publisher already has an API key. Use admin rotate-key to replace it.",
        )

    # Verify caller identity via source IP
    client_ip = request.client.host if request.client else ""
    allowed = False

    # Localhost is always trusted (publisher running on same host as broker)
    if client_ip in ("127.0.0.1", "::1", "localhost"):
        allowed = True

    # Match against the publisher's registered endpoint IP
    # (set during previous heartbeats from the real publisher)
    if pub.endpoint:
        registered_ip = pub.endpoint.split(":")[0]  # strip port
        if client_ip == registered_ip:
            allowed = True

    if not allowed:
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="upgrade-key must be called from the publisher's registered IP or localhost.",
        )

    from app.services.publisher_key_service import assign_publisher_key
    plaintext_key = await assign_publisher_key(pub, db)

    from app.models.audit import AccessLog
    db.add(AccessLog(
        publisher_id=publisher_id,
        action="publisher_key_self_upgrade",
        detail=(
            f"Publisher '{pub.name}' self-upgraded to API key "
            f"(wpk_...{plaintext_key[-6:]}) from {client_ip}"
        ),
    ))

    return {
        "publisher_id": pub.id,
        "publisher_api_key": plaintext_key,
        "message": "Store this key securely — it will not be shown again. "
                   "Set it as PUBLISHER_API_KEY and restart the heartbeat.",
    }


# ─── Heartbeat ───

@router.post("/{publisher_id}/heartbeat", status_code=status.HTTP_204_NO_CONTENT)
async def publisher_heartbeat(
    publisher_id: str,
    heartbeat: PublisherHeartbeat,
    request: Request,
    db: AsyncSession = Depends(get_db),
    _auth=Depends(require_publisher_auth),
):
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        raise HTTPException(status_code=404, detail="Publisher not found")

    # Only update last_heartbeat (last seen alive) when the publisher is confirmed
    # healthy via WG handshake. The health monitor sets update_last_seen=True only
    # when the handshake is fresh. Direct publisher heartbeats always update it.
    update_last_seen = heartbeat.update_last_seen if heartbeat.update_last_seen is not None else True
    if update_last_seen:
        pub.last_heartbeat = datetime.utcnow()

    # Only update status from heartbeat if the publisher is NOT in an
    # administrative state. "disabled" is set by an admin and must only
    # be cleared via the /enable endpoint. "pending" after a reset is
    # allowed to transition back to online once the publisher reconnects.
    if pub.status == "disabled":
        # Disabled by admin — heartbeat keeps it alive but cannot change state
        pass
    elif pub.status == "pending":
        # After a reset: the publisher reconnecting means it's healthy again
        pub.status = heartbeat.status
    else:
        # Normal operation: trust the publisher's reported status
        pub.status = heartbeat.status

    # Track publisher's public endpoint from heartbeat source IP
    # The publisher listens on port 51821 for WireGuard connections
    # Only update if the request comes from a non-loopback IP (i.e., directly from the publisher)
    # The broker health monitor reports heartbeats from localhost — those should NOT
    # overwrite the real publisher endpoint.
    if request.client and request.client.host:
        source_ip = request.client.host
        if source_ip not in ("127.0.0.1", "::1", "localhost"):
            pub_endpoint = f"{source_ip}:51821"
            if pub.endpoint != pub_endpoint:
                pub.endpoint = pub_endpoint

    # Update DNS server if publisher reports it via auto-discovery
    if heartbeat.local_dns:
        pub.dns_server = heartbeat.local_dns

    # Update agent version if reported (backwards-compatible: old publishers won't send this)
    if heartbeat.agent_version:
        pub.agent_version = heartbeat.agent_version
