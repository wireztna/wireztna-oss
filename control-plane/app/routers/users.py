"""Users CRUD endpoints."""

import secrets
from datetime import datetime, timedelta

from fastapi import APIRouter, Depends, HTTPException, Request, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select

from app.database import get_db
from app.models.user import User
from app.models.client_enrollment import ClientEnrollmentToken
from app.schemas.schemas import (
    UserCreate, UserUpdate, UserResponse, AdminSetPasswordRequest,
    ClientEnrollmentTokenCreate, ClientEnrollmentTokenResponse,
)
from app.services.auth_service import require_admin, get_current_user, hash_password, scoped_query, resolve_org_for_creation
from app.services.wireguard_service import OverlayIPAllocator
from app.services.org_service import resolve_org_id
from app.services.url_builder import external_base_url
from app.config import settings

router = APIRouter()
ip_allocator = OverlayIPAllocator()


@router.get("", response_model=list[UserResponse])
async def list_users(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
    org_id: str | None = None,
):
    query = select(User).order_by(User.email)
    query = scoped_query(query, User, current_user, org_id)
    result = await db.execute(query)
    return result.scalars().all()


@router.post("", response_model=UserResponse, status_code=status.HTTP_201_CREATED)
async def create_user(
    user: UserCreate,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
    org_id: str | None = None,
):
    # Check email uniqueness (primary identifier)
    result = await db.execute(select(User).where(User.email == user.email))
    if result.scalar_one_or_none():
        raise HTTPException(status_code=409, detail="Email already exists")

    # Validate: local/both users must have a password
    if user.auth_provider in ("local", "both") and not user.password:
        raise HTTPException(status_code=400, detail="Password is required for local auth users")

    # Allocate overlay IP
    overlay_ip = await ip_allocator.allocate(db)

    # Username defaults to email local part if not provided
    username = user.username or user.email.split("@")[0]

    # Resolve org_id: org_admin forced to their own org, super_admin uses explicit or default
    effective_org_id = resolve_org_for_creation(current_user, org_id)
    if not effective_org_id:
        effective_org_id = await resolve_org_id(None, db)

    # Sync is_admin from role for backward compat
    role = user.role if hasattr(user, 'role') else "user"
    is_admin = role in ("super_admin", "org_admin")

    new_user = User(
        username=username,
        email=user.email,
        password_hash=hash_password(user.password) if user.password else None,
        overlay_ip=overlay_ip,
        is_admin=is_admin,
        role=role,
        auth_provider=user.auth_provider,
        org_id=effective_org_id,
    )
    db.add(new_user)
    await db.flush()
    await db.refresh(new_user)

    from app.services.audit_service import log_action
    await log_action(db, "admin_user_created", detail=f"Created user {username} ({user.email}) role={role}", user_id=current_user.get("sub"), resource_id=new_user.id)

    return new_user


@router.get("/{user_id}", response_model=UserResponse)
async def get_user(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    return user


@router.put("/{user_id}", response_model=UserResponse)
async def update_user(user_id: str, update: UserUpdate, db: AsyncSession = Depends(get_db), current_user: dict = Depends(require_admin)):
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Role change authorization:
    # - super_admin can set any role (super_admin, org_admin, user)
    # - org_admin can only set org_admin or user (within their org)
    # - org_admin cannot promote to super_admin
    if update.role is not None:
        caller_role = current_user.get("role", "user")
        if caller_role == "org_admin":
            if update.role == "super_admin":
                raise HTTPException(status_code=403, detail="Only a super_admin can assign the super_admin role")
            # org_admin can only change roles within their own org
            caller_org = current_user.get("org_id")
            if caller_org and user.org_id != caller_org:
                raise HTTPException(status_code=403, detail="Cannot change role of users outside your organization")

    # If email is being changed, check uniqueness
    if update.email and update.email != user.email:
        existing = await db.execute(select(User).where(User.email == update.email))
        if existing.scalar_one_or_none():
            raise HTTPException(status_code=409, detail="Email already in use by another user")

    for field, value in update.model_dump(exclude_unset=True).items():
        if field == "password":
            user.password_hash = hash_password(value)
        elif field == "role":
            user.role = value
            user.is_admin = value in ("super_admin", "org_admin")
        elif field == "is_admin":
            # Legacy: if is_admin changes, sync role
            user.is_admin = value
            if value and user.role == "user":
                user.role = "org_admin"
            elif not value and user.role in ("super_admin", "org_admin"):
                user.role = "user"
        else:
            setattr(user, field, value)

    await db.flush()
    await db.refresh(user)

    from app.services.audit_service import log_action
    changed = [f for f in update.model_dump(exclude_unset=True).keys()]
    await log_action(db, "admin_user_updated", detail=f"Updated user {user.username}: fields={changed}", user_id=current_user.get("sub"), resource_id=user_id)

    return user


@router.delete("/{user_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_user(user_id: str, db: AsyncSession = Depends(get_db), current_user: dict = Depends(require_admin)):
    from app.models.session import ClientSession
    from app.models.access_pass import AccessPass
    from sqlalchemy import delete as sql_delete

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Delete related records that have FK to users.id without CASCADE
    await db.execute(sql_delete(ClientSession).where(ClientSession.user_id == user_id))
    await db.execute(sql_delete(ClientEnrollmentToken).where(ClientEnrollmentToken.user_id == user_id))
    await db.execute(sql_delete(AccessPass).where(AccessPass.created_by_user_id == user_id))

    await db.delete(user)
    await db.flush()  # Surface FK errors before response is sent

    from app.services.audit_service import log_action
    await log_action(db, "admin_user_deleted", detail=f"Deleted user {user.username} ({user.email})", user_id=current_user.get("sub"), resource_id=user_id)


@router.post("/{user_id}/disable", status_code=status.HTTP_204_NO_CONTENT)
async def disable_user(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Disable a user — their tunnel will stop working at next PSK expiry.

    The user remains in the system but cannot renew sessions.
    """
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    user.status = "disabled"
    await db.flush()


@router.post("/{user_id}/enable", status_code=status.HTTP_204_NO_CONTENT)
async def enable_user(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Re-enable a disabled user."""
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    user.status = "active"
    await db.flush()


@router.post("/{user_id}/unenroll", status_code=status.HTTP_204_NO_CONTENT)
async def unenroll_user(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Unenroll a client — removes their public key and revokes active sessions.

    The user account remains but they must re-enroll to connect again.
    Use this when a device is lost/stolen or the user changes machines.
    """
    from app.models.session import ClientSession
    from sqlalchemy import update

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Remove public key (breaks WG handshake immediately on next reconciler cycle)
    user.public_key = None

    # Revoke all active sessions
    await db.execute(
        update(ClientSession)
        .where(ClientSession.user_id == user_id, ClientSession.is_active == True)
        .values(is_active=False)
    )

    await db.flush()


# ─── Reset Enrollment Tokens ───

@router.post("/{user_id}/reset-tokens", status_code=status.HTTP_200_OK)
async def reset_enrollment_tokens(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Reset a user's enrollment tokens — deletes all used/expired/revoked tokens.

    This frees up the user's quota so they can generate new tokens from their portal.
    """
    from app.models.client_enrollment import ClientEnrollmentToken
    from sqlalchemy import delete as sql_delete

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Delete all tokens for this user (used, expired, revoked, and pending)
    deleted = await db.execute(
        sql_delete(ClientEnrollmentToken).where(
            ClientEnrollmentToken.user_id == user_id,
        )
    )

    # Audit
    from app.services.audit_service import log_action
    await log_action(db, "admin_tokens_reset", detail=f"Admin reset enrollment tokens for {user.username}", user_id=user_id)
    await db.commit()

    return {"message": f"Tokens reset — {user.username} can now generate {user.max_enrollment_tokens} new tokens"}


# ─── Onboarding Email ───

@router.post("/{user_id}/send-onboarding", status_code=status.HTTP_200_OK)
async def send_onboarding(user_id: str, db: AsyncSession = Depends(get_db), _=Depends(require_admin)):
    """Send a professional onboarding email to a user with getting started instructions.

    Generates a random temporary password, sets it on the user, and includes it in the email.
    The user should change it on first login via Portal > Security.
    """
    import secrets
    import string
    from app.services.email_service import send_onboarding_email

    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    if not user.email:
        raise HTTPException(status_code=400, detail="User has no email address configured")

    # Generate a random temporary password (12 chars, mix of letters + digits)
    alphabet = string.ascii_letters + string.digits
    temp_password = ''.join(secrets.choice(alphabet) for _ in range(12))

    # Set it on the user
    user.password_hash = hash_password(temp_password)
    user.must_change_password = True
    await db.commit()

    # Determine portal URL — prefer ui_public_domain, else the configured base URL.
    if settings.ui_public_domain:
        portal_url = f"{settings.broker_public_scheme}://{settings.ui_public_domain}"
    else:
        portal_url = external_base_url()

    success = send_onboarding_email(
        to_email=user.email,
        username=user.username,
        portal_url=portal_url,
        temp_password=temp_password,
    )

    if not success:
        raise HTTPException(status_code=500, detail="Failed to send email — check SMTP configuration")

    # Audit
    from app.services.audit_service import log_action
    await log_action(db, "admin_onboarding_sent", detail=f"Onboarding email sent to {user.username} ({user.email})", user_id=user.id)
    await db.commit()

    return {"message": f"Onboarding email sent to {user.email} (temporary password included)"}


# ─── Admin Set / Reset Password (SMTP-independent) ───

@router.post("/{user_id}/password", status_code=status.HTTP_200_OK)
async def admin_set_password(
    user_id: str,
    body: AdminSetPasswordRequest,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
):
    """Set or reset a user's password directly, without sending any email.

    This is the email-less path for CE deployments with no SMTP configured:
    an admin assigns a password and shares it out of band. Password complexity
    is enforced the same way as initial setup.
    """
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    from app.services.password_validation import validate_password
    is_valid, error_msg = validate_password(body.password)
    if not is_valid:
        raise HTTPException(status_code=400, detail=error_msg)

    user.password_hash = hash_password(body.password)
    user.must_change_password = body.must_change_password
    await db.flush()

    from app.services.audit_service import log_action
    await log_action(
        db,
        "admin_password_set",
        detail=f"Admin set password for {user.username} ({user.email})",
        user_id=current_user.get("sub"),
        resource_id=user_id,
    )
    await db.commit()

    return {"message": f"Password updated for {user.username}"}


# ─── Client Enrollment Tokens ───

def _build_enroll_url(token: str) -> str:
    """Build the enrollment URL that users pass to `wireztna enroll`.

    Uses port 80 (nginx) since port 8443 is not externally accessible.
    """
    if settings.ui_public_domain:
        base = f"{settings.broker_public_scheme}://{settings.ui_public_domain}"
    else:
        base = external_base_url()
    return f"{base}/api/v1/clients/enroll?token={token}"


def _token_to_response(token_obj: ClientEnrollmentToken, username: str) -> ClientEnrollmentTokenResponse:
    """Convert DB model to response schema."""
    return ClientEnrollmentTokenResponse(
        id=token_obj.id,
        token=token_obj.token,
        user_id=token_obj.user_id,
        username=username,
        expires_at=token_obj.expires_at,
        used_at=token_obj.used_at,
        used_from_ip=token_obj.used_from_ip,
        revoked=token_obj.revoked,
        created_by=token_obj.created_by,
        created_at=token_obj.created_at,
        note=token_obj.note,
        enroll_url=_build_enroll_url(token_obj.token),
        device_name=token_obj.device_name,
        device_platform=token_obj.device_platform,
    )


@router.post(
    "/{user_id}/enrollment-token",
    response_model=ClientEnrollmentTokenResponse,
    status_code=status.HTTP_201_CREATED,
)
async def create_enrollment_token(
    user_id: str,
    req: ClientEnrollmentTokenCreate,
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(require_admin),
):
    """Generate a one-time enrollment token for a user.

    The admin creates the token and sends the resulting enroll_url to the user.
    The user runs `wireztna enroll <url>` which registers their public key
    and returns full client configuration.
    """
    # Validate user exists
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    # Check user has an overlay IP (needed for enrollment)
    if not user.overlay_ip:
        raise HTTPException(
            status_code=400,
            detail="User has no overlay IP assigned — cannot generate enrollment token",
        )

    # Check user has a password set (needed for login after enrollment)
    if not user.password_hash:
        raise HTTPException(
            status_code=400,
            detail="User has no password set — set a password before generating enrollment token",
        )

    # Create token
    token_value = secrets.token_urlsafe(32)
    enrollment_token = ClientEnrollmentToken(
        token=token_value,
        user_id=user_id,
        expires_at=datetime.utcnow() + timedelta(hours=req.expires_in_hours),
        created_by=current_user.get("sub"),
        note=req.note,
    )
    db.add(enrollment_token)
    await db.flush()
    await db.refresh(enrollment_token)

    return _token_to_response(enrollment_token, user.username)


@router.get("/{user_id}/enrollment-tokens", response_model=list[ClientEnrollmentTokenResponse])
async def list_enrollment_tokens(
    user_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """List all enrollment tokens for a user (including used/expired/revoked)."""
    # Validate user exists
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    tokens_result = await db.execute(
        select(ClientEnrollmentToken)
        .where(ClientEnrollmentToken.user_id == user_id)
        .order_by(ClientEnrollmentToken.created_at.desc())
    )
    tokens = tokens_result.scalars().all()

    return [_token_to_response(t, user.username) for t in tokens]


@router.delete("/{user_id}/enrollment-tokens/{token_id}", status_code=status.HTTP_204_NO_CONTENT)
async def revoke_enrollment_token(
    user_id: str,
    token_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_admin),
):
    """Revoke an enrollment token (prevents future use)."""
    result = await db.execute(
        select(ClientEnrollmentToken).where(
            ClientEnrollmentToken.id == token_id,
            ClientEnrollmentToken.user_id == user_id,
        )
    )
    token = result.scalar_one_or_none()
    if not token:
        raise HTTPException(status_code=404, detail="Token not found")

    if token.used_at:
        raise HTTPException(status_code=400, detail="Token already used — cannot revoke")

    token.revoked = True
    await db.flush()
