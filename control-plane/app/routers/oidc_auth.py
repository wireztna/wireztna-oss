"""OIDC/SSO authentication endpoints — Microsoft Entra ID.

Provides:
- GET  /oidc/config     — Check if OIDC is enabled (public, no auth)
- GET  /oidc/login      — Redirect to Microsoft login page (Web UI)
- GET  /oidc/callback   — Handle redirect back from Microsoft (Web UI)
- POST /oidc/device     — Start device authorization (CLI)
- POST /oidc/device/poll — Poll for device auth completion (CLI)
"""

import secrets

from fastapi import APIRouter, Depends, HTTPException, Query, status
from fastapi.responses import RedirectResponse
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, or_

from app.database import get_db
from app.config import settings
from app.models.user import User
from app.models.audit import AccessLog
from app.services.auth_service import create_token
from app.services.oidc_service import (
    build_authorization_url,
    exchange_code_for_tokens,
    validate_id_token,
    extract_user_info,
    extract_groups,
    has_groups_overage,
    fetch_user_groups_from_graph,
    check_allowed_groups,
    start_device_authorization,
    poll_device_authorization,
    OIDCError,
)
from app.services.wireguard_service import OverlayIPAllocator
from app.schemas.schemas import (
    OIDCConfigResponse,
    OIDCCallbackResponse,
    OIDCDeviceStartResponse,
    OIDCDevicePollRequest,
    OIDCDevicePollResponse,
)

router = APIRouter()
ip_allocator = OverlayIPAllocator()

# Simple in-memory state store for CSRF protection on authorization code flow.
# In production with multiple workers, use Redis or DB. Entries expire after 10 min.
_pending_states: dict[str, float] = {}


def _require_oidc_enabled():
    """Guard: raise 404 if OIDC is not configured."""
    if not settings.oidc_enabled:
        raise HTTPException(status_code=404, detail="OIDC is not enabled")
    if not settings.oidc_client_id or not settings.oidc_tenant_id:
        raise HTTPException(status_code=503, detail="OIDC is not fully configured")


# ─── Public: check OIDC availability ───


@router.get("/oidc/config", response_model=OIDCConfigResponse)
async def get_oidc_config(db: AsyncSession = Depends(get_db)):
    """Report auth availability to the UI login page and CLI.

    Beyond OIDC/SSO status this exposes:
    - email_enabled / otp_enabled: whether SMTP is configured (email OTP + onboarding work).
      In CE with no SMTP both are False and the UI falls back to password-first login.
    - setup_required: whether no users exist yet, signalling the first-run create-admin screen.
    """
    email_enabled = bool(settings.smtp_host)

    # First-run signal: True when there are zero users (bootstrap via POST /auth/setup).
    users_exist = (await db.execute(select(User).limit(1))).first() is not None
    setup_required = not users_exist

    if not settings.oidc_enabled:
        return OIDCConfigResponse(
            enabled=False,
            email_enabled=email_enabled,
            otp_enabled=email_enabled,
            setup_required=setup_required,
        )
    return OIDCConfigResponse(
        enabled=True,
        provider_url=settings.oidc_provider_url or None,
        login_url="/api/v1/auth/oidc/login",
        email_enabled=email_enabled,
        otp_enabled=email_enabled,
        setup_required=setup_required,
    )


# ─── Web UI: Authorization Code Flow ───


@router.get("/oidc/login")
async def oidc_login():
    """Redirect to Microsoft Entra ID login page.

    The Web UI navigates here; Microsoft shows its login, then redirects back
    to /oidc/callback with an authorization code.
    """
    _require_oidc_enabled()

    state = secrets.token_urlsafe(32)
    import time
    _pending_states[state] = time.time()
    # Clean up expired states (>10 min old)
    now = time.time()
    expired = [k for k, v in _pending_states.items() if now - v > 600]
    for k in expired:
        _pending_states.pop(k, None)

    url = build_authorization_url(state=state)
    return RedirectResponse(url=url, status_code=302)


@router.get("/oidc/callback")
async def oidc_callback(
    code: str = Query(...),
    state: str = Query(...),
    db: AsyncSession = Depends(get_db),
):
    """Handle Microsoft redirect after user authenticates.

    Validates the authorization code, exchanges it for tokens, validates the
    id_token, then either finds an existing user or auto-provisions one
    (if allowed by group membership).

    On success, redirects to the Web UI login page with ?token=<jwt> so the
    frontend can capture it and store the session.
    """
    _require_oidc_enabled()

    # Verify state (CSRF protection)
    import time
    if state not in _pending_states:
        raise HTTPException(status_code=400, detail="Invalid state parameter (possible CSRF)")
    state_time = _pending_states.pop(state)
    if time.time() - state_time > 600:
        raise HTTPException(status_code=400, detail="State expired — please try again")

    # Exchange code for tokens
    try:
        tokens = await exchange_code_for_tokens(code)
    except OIDCError as e:
        raise HTTPException(status_code=401, detail=str(e))

    id_token_raw = tokens.get("id_token")
    access_token = tokens.get("access_token")
    if not id_token_raw:
        raise HTTPException(status_code=401, detail="No id_token in response")

    # Validate id_token signature + claims
    try:
        claims = await validate_id_token(id_token_raw)
    except OIDCError as e:
        raise HTTPException(status_code=401, detail=str(e))

    # Extract user info
    user_info = extract_user_info(claims)
    oidc_sub = user_info["sub"]
    email = user_info["email"]

    if not oidc_sub:
        raise HTTPException(status_code=401, detail="Missing 'sub' claim in token")

    # Find or provision user
    user, is_new = await _find_or_provision_user(
        oidc_sub=oidc_sub,
        email=email,
        name=user_info["name"],
        preferred_username=user_info["preferred_username"],
        claims=claims,
        access_token=access_token,
        db=db,
    )

    # Issue internal JWT
    token = create_token({"sub": user.id, "username": user.username, "is_admin": user.is_admin})

    # Audit
    db.add(AccessLog(
        user_id=user.id,
        action="oidc_login",
        detail=f"SSO login via Entra ID{' (new user provisioned)' if is_new else ''}",
    ))
    await db.commit()

    # Redirect to the Web UI with the token so it can store the session.
    # The UI login page reads ?token= from the URL on mount.
    ui_base = settings.oidc_redirect_uri.rsplit("/api/", 1)[0] if "/api/" in settings.oidc_redirect_uri else ""
    redirect_url = f"{ui_base}/login?token={token}"
    return RedirectResponse(url=redirect_url, status_code=302)


# ─── CLI: Device Authorization Grant (RFC 8628) ───


@router.post("/oidc/device", response_model=OIDCDeviceStartResponse)
async def oidc_device_start():
    """Start device authorization flow for CLI.

    Returns a user_code and verification_uri. The CLI displays these,
    then polls /oidc/device/poll until the user completes auth in their browser.
    """
    _require_oidc_enabled()

    try:
        result = await start_device_authorization()
    except OIDCError as e:
        raise HTTPException(status_code=503, detail=str(e))

    return OIDCDeviceStartResponse(
        device_code=result["device_code"],
        user_code=result["user_code"],
        verification_uri=result["verification_uri"],
        verification_uri_complete=result.get("verification_uri_complete"),
        expires_in=result.get("expires_in", 900),
        interval=result.get("interval", 5),
        message=result.get("message", f"Go to {result['verification_uri']} and enter code: {result['user_code']}"),
    )


@router.post("/oidc/device/poll", response_model=OIDCDevicePollResponse)
async def oidc_device_poll(
    body: OIDCDevicePollRequest,
    db: AsyncSession = Depends(get_db),
):
    """Poll for device authorization completion.

    The CLI calls this every `interval` seconds. Returns:
    - status="pending" while waiting
    - status="completed" + access_token when user finishes auth
    - status="expired" or "denied" on terminal errors
    """
    _require_oidc_enabled()

    try:
        tokens = await poll_device_authorization(body.device_code)
    except OIDCError as e:
        # Terminal error (expired, denied)
        error_msg = str(e)
        if "expired" in error_msg.lower():
            return OIDCDevicePollResponse(status="expired")
        elif "declined" in error_msg.lower():
            return OIDCDevicePollResponse(status="denied")
        raise HTTPException(status_code=401, detail=error_msg)

    if tokens is None:
        return OIDCDevicePollResponse(status="pending")

    # User completed auth — validate and find/provision user
    id_token_raw = tokens.get("id_token")
    access_token = tokens.get("access_token")
    if not id_token_raw:
        raise HTTPException(status_code=401, detail="No id_token in device auth response")

    try:
        claims = await validate_id_token(id_token_raw)
    except OIDCError as e:
        raise HTTPException(status_code=401, detail=str(e))

    user_info = extract_user_info(claims)
    oidc_sub = user_info["sub"]
    email = user_info["email"]

    if not oidc_sub:
        raise HTTPException(status_code=401, detail="Missing 'sub' claim in token")

    user, is_new = await _find_or_provision_user(
        oidc_sub=oidc_sub,
        email=email,
        name=user_info["name"],
        preferred_username=user_info["preferred_username"],
        claims=claims,
        access_token=access_token,
        db=db,
    )

    # Issue internal JWT
    token = create_token({"sub": user.id, "username": user.username, "is_admin": user.is_admin})

    # Audit
    db.add(AccessLog(
        user_id=user.id,
        action="oidc_device_login",
        detail=f"SSO login via device auth{' (new user provisioned)' if is_new else ''}",
    ))
    await db.commit()

    return OIDCDevicePollResponse(
        status="completed",
        access_token=token,
        username=user.username,
    )


# ─── Internal: find existing user or auto-provision ───


async def _find_or_provision_user(
    oidc_sub: str,
    email: str,
    name: str,
    preferred_username: str,
    claims: dict,
    access_token: str | None,
    db: AsyncSession,
) -> tuple[User, bool]:
    """Find an existing user by oidc_subject or email, or auto-provision if allowed.

    Returns (user, is_new_user).
    Raises HTTPException if user not found and auto-provisioning not allowed.
    """
    # First: try to find by oidc_subject (stable identifier, survives email changes)
    result = await db.execute(select(User).where(User.oidc_subject == oidc_sub))
    user = result.scalar_one_or_none()

    if user:
        if user.status != "active":
            raise HTTPException(status_code=403, detail="Account is disabled")
        if user.auth_provider not in ("oidc", "both"):
            raise HTTPException(status_code=403, detail="This account is not configured for SSO")
        return user, False

    # Second: try to find by email (for pre-created users that haven't linked yet)
    if email:
        result = await db.execute(select(User).where(User.email == email))
        user = result.scalar_one_or_none()

        if user:
            if user.status != "active":
                raise HTTPException(status_code=403, detail="Account is disabled")
            if user.auth_provider not in ("oidc", "both"):
                raise HTTPException(
                    status_code=403,
                    detail="Account exists but is not configured for SSO. Contact your admin.",
                )
            # Link the oidc_subject for future lookups
            user.oidc_subject = oidc_sub
            return user, False

    # Third: auto-provision if enabled and user belongs to allowed groups
    if not settings.oidc_auto_provision:
        raise HTTPException(
            status_code=403,
            detail="Account not provisioned. Contact your administrator.",
        )

    # Get user's Entra groups
    user_groups_list = extract_groups(claims)
    if has_groups_overage(claims) and access_token:
        # Too many groups in token — fetch from Graph API
        try:
            user_groups_list = await fetch_user_groups_from_graph(access_token)
        except OIDCError:
            # Can't verify groups — deny
            raise HTTPException(
                status_code=403,
                detail="Cannot verify group membership. Contact your administrator.",
            )

    # Check against allowed groups
    if not check_allowed_groups(user_groups_list):
        raise HTTPException(
            status_code=403,
            detail="Your organization group is not authorized for access. Contact your administrator.",
        )

    # Auto-provision the user
    username = _derive_username(preferred_username, email, name)

    # Ensure username uniqueness
    existing = await db.execute(select(User).where(User.username == username))
    if existing.scalar_one_or_none():
        # Append a short random suffix
        username = f"{username}_{secrets.token_hex(3)}"

    overlay_ip = await ip_allocator.allocate(db)

    new_user = User(
        username=username,
        email=email,
        password_hash=None,  # No local password — SSO only
        overlay_ip=overlay_ip,
        status="active",
        is_admin=False,
        auth_provider="oidc",
        oidc_subject=oidc_sub,
    )
    db.add(new_user)
    await db.flush()

    # Assign to default group if configured
    if settings.oidc_default_group:
        from app.models.group import Group
        result = await db.execute(select(Group).where(Group.id == settings.oidc_default_group))
        default_group = result.scalar_one_or_none()
        if default_group:
            from sqlalchemy import insert
            from app.models.group import user_groups as ug_table
            await db.execute(insert(ug_table).values(user_id=new_user.id, group_id=default_group.id))

    await db.flush()
    await db.refresh(new_user)
    return new_user, True


def _derive_username(preferred_username: str, email: str, name: str) -> str:
    """Derive a WireZTNA username from Entra claims.

    Priority: preferred_username (usually user@domain) → email prefix → name → fallback.
    Strips the @domain part for cleaner usernames.
    """
    # preferred_username is usually "user@company.com"
    if preferred_username and "@" in preferred_username:
        return preferred_username.split("@")[0].lower()
    if email and "@" in email:
        return email.split("@")[0].lower()
    if name:
        return name.lower().replace(" ", ".")
    return f"sso_user_{secrets.token_hex(4)}"
