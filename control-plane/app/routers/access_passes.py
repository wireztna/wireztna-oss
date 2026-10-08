"""Access Passes router — CRUD for delegated access passes.

Enables authenticated users to create time-limited, scope-restricted
tokens that grant third-party agents network access to internal resources.
"""

import json
from datetime import datetime, timedelta

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy import select, func
from sqlalchemy.ext.asyncio import AsyncSession

from app.database import get_db
from app.models.access_pass import AccessPass
from app.models.audit import AccessLog
from app.services.auth_service import get_current_user, require_admin
from app.services.pass_service import generate_pass_id
from app.services.scope_service import validate_scope
from app.services.url_builder import external_ws_base_url
from app.schemas.schemas import (
    AccessPassCreate,
    AccessPassResponse,
    AccessPassListItem,
)

router = APIRouter()

MAX_ACTIVE_PASSES = 5
MAX_TTL_SECONDS = 7200

# Plan-based limits
PLAN_LIMITS = {
    "free": {"max_active": 2, "max_ttl": 3600, "max_passes_month": 25, "max_bytes": 500 * 1024 * 1024},
    "pro":  {"max_active": 5, "max_ttl": 7200, "max_passes_month": None, "max_bytes": None},
}

# Module-level dict tracking active WebSocket connections per pass_id.
# The tunnel.py router (task 5.2) will import and use this dict to register
# connections when agents connect, and this router uses it to close connections
# on revocation.
_active_tunnels: dict[str, list] = {}


def _build_scope_summary(scope: dict) -> str:
    """Build a human-readable scope summary string.

    Examples:
    - "K8s API (10.50.1.200:6443) via publisher-main"
    - "10.50.0.0/16:5432 via publisher-main"
    """
    parts = []

    # Use app name if available
    if scope.get("apps"):
        parts.append(scope["apps"][0])

    # Add CIDR:port info
    cidrs = scope.get("cidrs", [])
    ports = scope.get("ports", [])
    if cidrs:
        target = cidrs[0]
        if ports:
            target = f"{target}:{ports[0]}"
        if parts:
            parts[0] = f"{parts[0]} ({target})"
        else:
            parts.append(target)

    # Add publisher context
    publishers = scope.get("publishers", [])
    if publishers:
        parts.append(f"via {publishers[0]}")

    return " ".join(parts) if parts else "scoped access"


@router.post("", response_model=AccessPassResponse, status_code=201)
async def create_access_pass(
    body: AccessPassCreate,
    current_user: dict = Depends(get_current_user),
    db: AsyncSession = Depends(get_db),
):
    """Create a new delegated access pass.

    Validates that the requested scope is within the user's current access,
    enforces TTL cap and active pass limit, then generates a pass with
    a unique ID and connection URL.

    Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 7.1
    """
    user_id = current_user["sub"]

    # Determine user plan and limits
    user_plan = current_user.get("plan", "pro")
    limits = PLAN_LIMITS.get(user_plan, PLAN_LIMITS["pro"])
    max_ttl = limits["max_ttl"]
    max_active = limits["max_active"]
    max_passes_month = limits["max_passes_month"]
    max_bytes = limits["max_bytes"]

    # Check TTL cap (plan-based)
    if body.ttl_seconds > max_ttl:
        raise HTTPException(
            status_code=400,
            detail=f"TTL exceeds maximum of {max_ttl} seconds for your plan",
        )

    # Check active pass count limit (plan-based)
    active_count_result = await db.execute(
        select(func.count())
        .select_from(AccessPass)
        .where(
            AccessPass.created_by_user_id == user_id,
            AccessPass.status == "active",
        )
    )
    active_count = active_count_result.scalar() or 0
    if active_count >= max_active:
        raise HTTPException(
            status_code=429,
            detail=f"Maximum {max_active} active passes reached for your plan",
        )

    # Check monthly pass creation limit (free tier only)
    if max_passes_month is not None:
        from app.models.pass_usage import PassUsageMonthly
        current_month = datetime.utcnow().strftime("%Y-%m")
        usage_result = await db.execute(
            select(PassUsageMonthly).where(
                PassUsageMonthly.user_id == user_id,
                PassUsageMonthly.month == current_month,
            )
        )
        usage = usage_result.scalar_one_or_none()
        if usage and usage.passes_created >= max_passes_month:
            raise HTTPException(
                status_code=429,
                detail=f"Monthly pass limit ({max_passes_month}) reached. Resets next month.",
            )

    # Validate scope — raises HTTPException(403) if scope exceeds user's access
    await validate_scope(user_id, body.scope, db)

    # Generate pass ID and compute expiration
    pass_id = generate_pass_id()
    now = datetime.utcnow()
    expires_at = now + timedelta(seconds=body.ttl_seconds)

    # Build connection URL from the configured WS base (ws/wss per scheme)
    connection_url = f"{external_ws_base_url()}/api/v1/tunnel/{pass_id}"

    # Build scope summary
    scope_dict = body.scope.model_dump()
    scope_summary = _build_scope_summary(scope_dict)

    # Create the access pass record
    access_pass = AccessPass(
        id=pass_id,
        created_by_user_id=user_id,
        label=body.label,
        scope_json=json.dumps(scope_dict),
        ttl_seconds=body.ttl_seconds,
        status="active",
        connection_url=connection_url,
        metadata_json=json.dumps(body.metadata) if body.metadata else None,
        max_bytes=max_bytes,  # Plan-based traffic limit (None = unlimited)
        created_at=now,
        expires_at=expires_at,
    )
    db.add(access_pass)

    # Write audit log
    db.add(AccessLog(
        user_id=user_id,
        action="access_pass_created",
        detail=f"Pass {pass_id}: {scope_summary} (TTL {body.ttl_seconds}s)",
    ))

    # Track monthly usage (for free tier rate limiting)
    if max_passes_month is not None:
        from app.models.pass_usage import PassUsageMonthly
        import uuid as _uuid
        current_month = datetime.utcnow().strftime("%Y-%m")
        usage_result = await db.execute(
            select(PassUsageMonthly).where(
                PassUsageMonthly.user_id == user_id,
                PassUsageMonthly.month == current_month,
            )
        )
        usage = usage_result.scalar_one_or_none()
        if usage:
            usage.passes_created += 1
        else:
            db.add(PassUsageMonthly(
                id=str(_uuid.uuid4()),
                user_id=user_id,
                month=current_month,
                passes_created=1,
            ))

    await db.commit()

    # Compute time remaining
    time_remaining = max(0, int((expires_at - datetime.utcnow()).total_seconds()))

    return AccessPassResponse(
        pass_id=pass_id,
        label=body.label,
        status="active",
        scope_summary=scope_summary,
        connection_url=connection_url,
        created_by_user_id=user_id,
        created_at=now,
        expires_at=expires_at,
        bytes_uploaded=0,
        bytes_downloaded=0,
        connections_count=0,
        last_activity_at=None,
        time_remaining_seconds=time_remaining,
    )


@router.get("", response_model=list[AccessPassListItem])
async def list_access_passes(
    current_user: dict = Depends(get_current_user),
    db: AsyncSession = Depends(get_db),
):
    """List all access passes created by the current user.

    Returns passes ordered by creation time descending, including
    status, scope summary, traffic counters, and timestamps.

    Requirements: 2.1, 2.2
    """
    user_id = current_user["sub"]

    result = await db.execute(
        select(AccessPass)
        .where(AccessPass.created_by_user_id == user_id)
        .order_by(AccessPass.created_at.desc())
    )
    passes = result.scalars().all()

    return [
        AccessPassListItem(
            pass_id=p.id,
            label=p.label,
            status=p.status,
            scope_summary=_build_scope_summary(json.loads(p.scope_json)),
            created_at=p.created_at,
            expires_at=p.expires_at,
            bytes_uploaded=p.bytes_uploaded,
            bytes_downloaded=p.bytes_downloaded,
            connections_count=p.connections_count,
        )
        for p in passes
    ]


@router.get("/{pass_id}", response_model=AccessPassResponse)
async def get_access_pass(
    pass_id: str,
    current_user: dict = Depends(get_current_user),
    db: AsyncSession = Depends(get_db),
):
    """Get detailed status of a specific access pass.

    Verifies ownership before returning the pass details, including
    computed time_remaining_seconds based on current time vs expires_at.

    Requirements: 3.1, 3.2
    """
    user_id = current_user["sub"]

    result = await db.execute(
        select(AccessPass).where(AccessPass.id == pass_id)
    )
    access_pass = result.scalar_one_or_none()

    if not access_pass or access_pass.created_by_user_id != user_id:
        raise HTTPException(status_code=404, detail="Pass not found")

    # Compute time remaining
    now = datetime.utcnow()
    time_remaining = max(0, int((access_pass.expires_at - now).total_seconds()))

    # Build scope summary from stored JSON
    scope_summary = _build_scope_summary(json.loads(access_pass.scope_json))

    return AccessPassResponse(
        pass_id=access_pass.id,
        label=access_pass.label,
        status=access_pass.status,
        scope_summary=scope_summary,
        connection_url=access_pass.connection_url,
        created_by_user_id=access_pass.created_by_user_id,
        created_at=access_pass.created_at,
        expires_at=access_pass.expires_at,
        bytes_uploaded=access_pass.bytes_uploaded,
        bytes_downloaded=access_pass.bytes_downloaded,
        connections_count=access_pass.connections_count,
        last_activity_at=access_pass.last_activity_at,
        time_remaining_seconds=time_remaining,
    )


@router.delete("/{pass_id}")
async def revoke_access_pass(
    pass_id: str,
    current_user: dict = Depends(get_current_user),
    db: AsyncSession = Depends(get_db),
):
    """Revoke an active access pass immediately.

    Sets status to 'revoked', closes any active WebSocket tunnel connections
    for this pass, and records an audit log entry with bytes transferred.

    Requirements: 4.1, 4.2, 4.3, 7.2
    """
    # 1. Look up pass by pass_id
    result = await db.execute(
        select(AccessPass).where(AccessPass.id == pass_id)
    )
    access_pass = result.scalar_one_or_none()

    # 2. Verify ownership (else 404)
    if not access_pass or access_pass.created_by_user_id != current_user["sub"]:
        raise HTTPException(
            status_code=404,
            detail="Pass not found",
        )

    # 3. Check status == "active" (else 409)
    if access_pass.status != "active":
        raise HTTPException(
            status_code=409,
            detail=f"Pass is not active (status: {access_pass.status})",
        )

    # 4. Set status = "revoked", revoked_at = now()
    access_pass.status = "revoked"
    access_pass.revoked_at = datetime.utcnow()

    # 5. Close any active WebSocket connections for this pass
    active_connections = _active_tunnels.get(pass_id, [])
    for websocket in active_connections:
        try:
            await websocket.close(code=4002, reason="Pass revoked")
        except Exception:
            pass  # Connection may already be closed
    # Clear the entry
    _active_tunnels.pop(pass_id, None)

    # 6. Write audit log entry
    bytes_transferred = (access_pass.bytes_uploaded or 0) + (access_pass.bytes_downloaded or 0)
    db.add(AccessLog(
        user_id=current_user["sub"],
        resource_id=pass_id,
        action="access_pass_revoked",
        detail=f"Revoked pass '{access_pass.label}' — {bytes_transferred} bytes transferred",
        bytes_transferred=bytes_transferred,
    ))

    # 7. Commit and return
    await db.commit()

    return {"status": "revoked", "pass_id": pass_id}


# ─── Admin endpoints ───


@router.get("/admin/by-user/{user_id}", response_model=list[AccessPassListItem])
async def admin_list_user_passes(
    user_id: str,
    current_user: dict = Depends(require_admin),
    db: AsyncSession = Depends(get_db),
):
    """Admin: list all access passes created by a specific user."""
    result = await db.execute(
        select(AccessPass)
        .where(AccessPass.created_by_user_id == user_id)
        .order_by(AccessPass.created_at.desc())
    )
    passes = result.scalars().all()

    return [
        AccessPassListItem(
            pass_id=p.id,
            label=p.label,
            status=p.status,
            scope_summary=_build_scope_summary(json.loads(p.scope_json)),
            created_at=p.created_at,
            expires_at=p.expires_at,
            bytes_uploaded=p.bytes_uploaded or 0,
            bytes_downloaded=p.bytes_downloaded or 0,
            connections_count=p.connections_count or 0,
        )
        for p in passes
    ]


@router.delete("/admin/{pass_id}")
async def admin_revoke_pass(
    pass_id: str,
    current_user: dict = Depends(require_admin),
    db: AsyncSession = Depends(get_db),
):
    """Admin: revoke any access pass regardless of ownership."""
    result = await db.execute(
        select(AccessPass).where(AccessPass.id == pass_id)
    )
    access_pass = result.scalar_one_or_none()

    if not access_pass:
        raise HTTPException(status_code=404, detail="Pass not found")

    if access_pass.status != "active":
        raise HTTPException(
            status_code=409,
            detail=f"Pass is not active (status: {access_pass.status})",
        )

    access_pass.status = "revoked"
    access_pass.revoked_at = datetime.utcnow()

    # Close active connections
    active_connections = _active_tunnels.get(pass_id, [])
    for websocket in active_connections:
        try:
            await websocket.close(code=4002, reason="Pass revoked by admin")
        except Exception:
            pass
    _active_tunnels.pop(pass_id, None)

    # Audit log
    db.add(AccessLog(
        user_id=current_user["sub"],
        resource_id=pass_id,
        action="access_pass_revoked_admin",
        detail=f"Admin revoked pass '{access_pass.label}' (owner: {access_pass.created_by_user_id})",
    ))

    await db.commit()
    return {"status": "revoked", "pass_id": pass_id}
