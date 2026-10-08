"""Authentication endpoints."""

import secrets
from datetime import datetime, timedelta

from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, or_

from app.config import settings
from app.database import get_db
from app.models.user import User
from app.models.otp_code import OTPCode
from app.schemas.schemas import (
    LoginRequest, TokenResponse,
    OTPRequestSchema, OTPRequestResponse, OTPVerifySchema,
)
from app.services.auth_service import verify_password, create_token, hash_password
from app.services.email_service import send_otp_email

router = APIRouter()


async def _find_user_by_identifier(identifier: str, db: AsyncSession) -> User | None:
    """Dual lookup: try email first (case-insensitive), fallback to username.

    This provides backward compatibility for old clients that send username
    while new clients send email.
    """
    if not identifier:
        return None

    # Try email first (primary login path)
    result = await db.execute(
        select(User).where(User.email == identifier)
    )
    user = result.scalar_one_or_none()
    if user:
        return user

    # Case-insensitive email lookup (user might type Email with different case)
    result = await db.execute(
        select(User).where(User.email.ilike(identifier))
    )
    user = result.scalar_one_or_none()
    if user:
        return user

    # Fallback: try username (backward compat for old clients)
    result = await db.execute(
        select(User).where(User.username == identifier)
    )
    user = result.scalar_one_or_none()
    return user


def _mask_email(email: str) -> str:
    """Mask an email for display: s***o@company.com"""
    if "@" not in email:
        return email[:1] + "***"
    local, domain = email.split("@", 1)
    if len(local) <= 2:
        masked = local[0] + "***"
    else:
        masked = local[0] + "***" + local[-1]
    return f"{masked}@{domain}"


def _generate_otp() -> str:
    """Generate a cryptographically secure numeric OTP code."""
    return "".join([str(secrets.randbelow(10)) for _ in range(settings.otp_length)])


def _build_token_claims(user: User, **extra) -> dict:
    """Build JWT claims with both email and username for backward compat."""
    claims = {
        "sub": user.id,
        "email": user.email,
        "username": user.username,  # Kept for backward compat with old clients reading JWT
        "is_admin": user.is_admin,
        "role": getattr(user, "role", None) or ("super_admin" if user.is_admin else "user"),
        "org_id": user.org_id,
        "plan": getattr(user, "plan", "pro"),
    }
    claims.update(extra)
    return claims


@router.post("/otp/request", response_model=OTPRequestResponse)
async def request_otp(request: OTPRequestSchema, db: AsyncSession = Depends(get_db)):
    """Request a one-time code sent to the user's registered email.

    Accepts email (preferred) or username (backward compat).
    Always returns success-like response to prevent user enumeration.
    """
    identifier = request.get_login_identifier()
    user = await _find_user_by_identifier(identifier, db)

    # Always return the same response shape to prevent enumeration
    # Generate a consistent fake hint even when user doesn't exist (pentest VULN-6)
    fake_hint = _mask_email(identifier) if "@" in identifier else f"{identifier[0]}***@***.com" if identifier else None
    default_response = OTPRequestResponse(email_hint=fake_hint, expires_in=settings.otp_ttl_seconds)

    if not user or not user.email:
        return default_response

    if user.status != "active":
        return default_response

    # Invalidate any existing unused codes for this user
    existing = await db.execute(
        select(OTPCode).where(
            OTPCode.user_id == user.id,
            OTPCode.used == False,
            OTPCode.expires_at > datetime.utcnow(),
        )
    )
    for old_code in existing.scalars().all():
        old_code.used = True

    # Generate and store new OTP
    code = _generate_otp()
    otp = OTPCode(
        user_id=user.id,
        code=code,
        expires_at=datetime.utcnow() + timedelta(seconds=settings.otp_ttl_seconds),
    )
    db.add(otp)
    await db.commit()

    # Send email (non-blocking failure — don't expose SMTP errors to client)
    send_otp_email(to_email=user.email, code=code, username=user.username)

    return OTPRequestResponse(
        email_hint=_mask_email(user.email),
        expires_in=settings.otp_ttl_seconds,
    )


@router.post("/otp/verify")
async def verify_otp(request: OTPVerifySchema, db: AsyncSession = Depends(get_db)):
    """Verify a one-time code and return a JWT token.

    Accepts email (preferred) or username (backward compat) to identify the user.
    """
    identifier = request.get_login_identifier()
    user = await _find_user_by_identifier(identifier, db)

    if not user:
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid code")

    # Find the latest valid (non-expired, non-used) OTP for this user
    otp_result = await db.execute(
        select(OTPCode).where(
            OTPCode.user_id == user.id,
            OTPCode.used == False,
            OTPCode.expires_at > datetime.utcnow(),
        ).order_by(OTPCode.created_at.desc()).limit(1)
    )
    otp = otp_result.scalar_one_or_none()

    if not otp:
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid or expired code")

    # Check max attempts
    if otp.attempts >= settings.otp_max_attempts:
        otp.used = True  # Invalidate
        await db.commit()
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Too many attempts — request a new code")

    # Verify the code (constant-time comparison)
    if not secrets.compare_digest(otp.code, request.code.strip()):
        otp.attempts += 1
        await db.commit()
        remaining = settings.otp_max_attempts - otp.attempts
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail=f"Invalid code ({remaining} attempts remaining)",
        )

    # Success — mark code as used and issue JWT (or partial token if MFA enabled)
    otp.used = True
    await db.commit()

    # Audit log
    from app.services.audit_service import log_action
    await log_action(db, "portal_login", detail=f"{user.email} logged in via OTP email", user_id=user.id)

    # Update last_activity for inactivity tracking
    from app.services.freetier_cleanup import update_user_activity
    await update_user_activity(user.id, db)

    await db.commit()

    # Check if user has TOTP MFA enabled
    if user.totp_enabled and user.totp_secret:
        mfa_token = create_token(_build_token_claims(user, mfa_pending=True))
        return {"access_token": mfa_token, "token_type": "bearer", "mfa_required": True, "password_change_required": False}

    token = create_token(_build_token_claims(user))
    return {"access_token": token, "token_type": "bearer", "mfa_required": False, "password_change_required": bool(user.must_change_password)}


@router.post("/login")
async def login(request: LoginRequest, db: AsyncSession = Depends(get_db)):
    """Authenticate with email (or username) + password and receive JWT.

    Accepts email (preferred) or username (backward compat for old clients).
    Respects the user's auth_provider setting:
    - "local" or "both": password login allowed
    - "oidc": password login rejected with helpful message
    """
    identifier = request.get_login_identifier()
    user = await _find_user_by_identifier(identifier, db)

    if not user or not user.password_hash:
        # Generic error to avoid user enumeration
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid credentials")

    # Check if this user is SSO-only
    if user.auth_provider == "oidc":
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="This account uses SSO. Please sign in with Microsoft.",
        )

    if not verify_password(request.password, user.password_hash):
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid credentials")

    # Audit log
    from app.services.audit_service import log_action
    await log_action(db, "portal_login", detail=f"{user.email} logged in via password", user_id=user.id)

    # Update last_activity for inactivity tracking
    from app.services.freetier_cleanup import update_user_activity
    await update_user_activity(user.id, db)

    await db.commit()

    # Check if user has TOTP MFA enabled
    if user.totp_enabled and user.totp_secret:
        mfa_token = create_token(_build_token_claims(user, mfa_pending=True))
        return {"access_token": mfa_token, "token_type": "bearer", "mfa_required": True, "password_change_required": False}

    token = create_token(_build_token_claims(user))
    return {"access_token": token, "token_type": "bearer", "mfa_required": False, "password_change_required": bool(user.must_change_password)}


@router.post("/setup", response_model=TokenResponse)
async def initial_setup(request: LoginRequest, db: AsyncSession = Depends(get_db)):
    """Create the first admin user. Only works if no users exist."""
    result = await db.execute(select(User))
    if result.first() is not None:
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail="Setup already completed")

    identifier = request.get_login_identifier()
    # For initial setup, use the identifier as both email and username
    email = identifier if "@" in identifier else f"{identifier}@local"
    username = identifier.split("@")[0] if "@" in identifier else identifier

    # Validate password complexity (A.5.17 / CC6.1)
    from app.services.password_validation import validate_password
    is_valid, error_msg = validate_password(request.password)
    if not is_valid:
        raise HTTPException(status_code=400, detail=error_msg)

    admin = User(
        username=username,
        email=email,
        password_hash=hash_password(request.password),
        is_admin=True,
        role="super_admin",
        status="active",
    )
    db.add(admin)
    await db.flush()

    token = create_token(_build_token_claims(admin))
    return TokenResponse(access_token=token)


# ─── Self-Service Registration (Free Tier) ───

from pydantic import BaseModel as _BaseModel, Field as _Field


class RegisterRequest(_BaseModel):
    """Request registration — sends OTP to email."""
    email: str = _Field(..., min_length=5, max_length=255)


class RegisterVerifyRequest(_BaseModel):
    """Verify OTP and complete registration."""
    email: str = _Field(..., min_length=5, max_length=255)
    code: str = _Field(..., min_length=4, max_length=10)


class RegisterResponse(_BaseModel):
    """Successful registration response."""
    api_key: str
    user_id: str
    plan: str


@router.post("/register")
async def register_request_otp(body: RegisterRequest, db: AsyncSession = Depends(get_db)):
    """Request a verification code for self-service registration.

    Public endpoint — no auth required.
    If email is already registered, returns same response (anti-enumeration).
    Rejects emails with +alias syntax to prevent free tier abuse.
    """
    from app.services.email_normalization import validate_email_for_registration, normalize_email

    email = body.email.strip().lower()

    # Reject +alias emails for free tier registration
    is_valid, error_msg = validate_email_for_registration(email)
    if not is_valid:
        raise HTTPException(status_code=400, detail=error_msg)

    # Check canonical email — block attempts using dot variations (Gmail) etc.
    canonical = normalize_email(email)
    canonical_result = await db.execute(
        select(User).where(User.canonical_email == canonical, User.plan == "free")
    )
    if canonical_result.scalar_one_or_none():
        # An account with the same canonical email exists — return same response
        # (anti-enumeration: don't reveal the specific reason)
        return {"message": "Verification code sent", "expires_in": settings.otp_ttl_seconds}

    # Check if already registered
    result = await db.execute(select(User).where(User.email == email))
    existing_user = result.scalar_one_or_none()

    if existing_user:
        # Don't reveal that the account exists — return same response
        # But still send OTP (they can use OTP login instead)
        return {"message": "Verification code sent", "expires_in": settings.otp_ttl_seconds}

    # Generate OTP and store with a placeholder user_id (email hash)
    # We use a synthetic user_id for pending registrations: "pending:<email>"
    import hashlib
    pending_id = f"pending:{hashlib.sha256(email.encode()).hexdigest()[:16]}"

    # Invalidate any existing pending OTPs for this email
    existing_otps = await db.execute(
        select(OTPCode).where(
            OTPCode.user_id == pending_id,
            OTPCode.used == False,
            OTPCode.expires_at > datetime.utcnow(),
        )
    )
    for old_code in existing_otps.scalars().all():
        old_code.used = True

    # Generate and store new OTP
    code = _generate_otp()
    otp = OTPCode(
        user_id=pending_id,
        code=code,
        expires_at=datetime.utcnow() + timedelta(seconds=settings.otp_ttl_seconds),
    )
    db.add(otp)
    await db.commit()

    # Send email
    from app.services.email_service import send_otp_email
    send_otp_email(to_email=email, code=code, username=email.split("@")[0])

    return {"message": "Verification code sent", "expires_in": settings.otp_ttl_seconds}


@router.post("/register/verify", response_model=RegisterResponse, status_code=201)
async def register_verify(body: RegisterVerifyRequest, db: AsyncSession = Depends(get_db)):
    """Verify OTP and create a free-tier user account.

    On success:
    1. Creates user (plan=free)
    2. Creates personal group
    3. Generates API key
    4. Returns the plaintext API key (shown only once)
    """
    import hashlib
    import uuid
    from app.services.email_normalization import validate_email_for_registration, normalize_email

    email = body.email.strip().lower()

    # Re-validate email (defense in depth — /register already checks this)
    is_valid, error_msg = validate_email_for_registration(email)
    if not is_valid:
        raise HTTPException(status_code=400, detail=error_msg)

    # Check canonical email uniqueness — prevent alias-based duplicates
    canonical = normalize_email(email)
    canonical_result = await db.execute(
        select(User).where(User.canonical_email == canonical, User.plan == "free")
    )
    if canonical_result.scalar_one_or_none():
        raise HTTPException(status_code=409, detail="An account with this email already exists")

    # Check if already registered
    result = await db.execute(select(User).where(User.email == email))
    if result.scalar_one_or_none():
        raise HTTPException(status_code=409, detail="Email already registered")

    # Verify OTP
    pending_id = f"pending:{hashlib.sha256(email.encode()).hexdigest()[:16]}"

    otp_result = await db.execute(
        select(OTPCode).where(
            OTPCode.user_id == pending_id,
            OTPCode.used == False,
            OTPCode.expires_at > datetime.utcnow(),
        ).order_by(OTPCode.created_at.desc()).limit(1)
    )
    otp = otp_result.scalar_one_or_none()

    if not otp:
        raise HTTPException(status_code=401, detail="Invalid or expired code")

    if otp.attempts >= settings.otp_max_attempts:
        otp.used = True
        await db.commit()
        raise HTTPException(status_code=401, detail="Too many attempts — request a new code")

    if not secrets.compare_digest(otp.code, body.code.strip()):
        otp.attempts += 1
        await db.commit()
        remaining = settings.otp_max_attempts - otp.attempts
        raise HTTPException(status_code=401, detail=f"Invalid code ({remaining} attempts remaining)")

    # OTP valid — mark as used
    otp.used = True

    # ─── Resolve or create "freetier" organization ───
    from app.models.organization import Organization
    from sqlalchemy import insert

    org_result = await db.execute(
        select(Organization).where(Organization.slug == "freetier")
    )
    freetier_org = org_result.scalar_one_or_none()

    if not freetier_org:
        freetier_org_id = str(uuid.uuid4())
        freetier_org = Organization(
            id=freetier_org_id,
            name="Free Tier",
            slug="freetier",
            description="Self-registered free tier users",
        )
        db.add(freetier_org)
        await db.flush()
    else:
        freetier_org_id = freetier_org.id

    # ─── Create user ───
    username = email.split("@")[0]
    user_id = str(uuid.uuid4())

    user = User(
        id=user_id,
        username=username,
        email=email,
        canonical_email=canonical,
        last_activity=datetime.utcnow(),
        status="active",
        role="user",
        plan="free",
        auth_provider="local",
        org_id=freetier_org_id,
        max_enrollment_tokens=3,  # Free tier: generous tokens, limit is 1 online publisher
    )
    db.add(user)

    # ─── Create personal group (named {username}-freetier) ───
    from app.models.group import Group, user_groups

    group_id = str(uuid.uuid4())
    group = Group(
        id=group_id,
        name=f"{username}-freetier",
        description=f"Personal group for {email}",
        org_id=freetier_org_id,
    )
    db.add(group)
    await db.flush()

    # Associate user to group
    await db.execute(
        insert(user_groups).values(user_id=user_id, group_id=group_id)
    )

    # ─── Generate API key ───
    from app.services.apikey_service import create_api_key_for_user
    plaintext_key = await create_api_key_for_user(user_id, db, label="registration")

    # ─── Audit log ───
    from app.models.audit import AccessLog
    db.add(AccessLog(
        user_id=user_id,
        action="self_registration",
        detail=f"Free tier user registered: {email}",
    ))

    await db.commit()

    return RegisterResponse(
        api_key=plaintext_key,
        user_id=user_id,
        plan="free",
    )
