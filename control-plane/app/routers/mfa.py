"""MFA (TOTP authenticator app) endpoints."""

import pyotp
from fastapi import APIRouter, Depends, HTTPException, status
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select
from pydantic import BaseModel

from app.database import get_db
from app.models.user import User
from app.services.auth_service import get_current_user, create_token, decode_token

router = APIRouter()


# ─── Schemas ───

class MFASetupResponse(BaseModel):
    """Returned when user starts MFA enrollment."""
    secret: str  # Base32 secret (for manual entry)
    otpauth_uri: str  # URI for QR code generation (otpauth://totp/...)
    qr_data: str  # Same as otpauth_uri (client generates QR from this)


class MFAVerifyRequest(BaseModel):
    """Code from authenticator app to confirm setup or complete login."""
    code: str


class MFAStatusResponse(BaseModel):
    enabled: bool


class MFALoginRequest(BaseModel):
    """Complete login with TOTP code after receiving a partial token."""
    mfa_token: str  # The partial token from the first login step
    code: str  # 6-digit TOTP code from authenticator app


# ─── Self-Service: Setup / Disable (requires full auth) ───

@router.post("/me/mfa/setup", response_model=MFASetupResponse)
async def mfa_setup(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Generate a TOTP secret for the user to scan with their authenticator app.

    Does NOT enable MFA yet — user must call /me/mfa/verify with a valid code first.
    """
    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    if user.totp_enabled:
        raise HTTPException(status_code=400, detail="MFA is already enabled. Disable it first to re-enroll.")

    # Generate new secret
    secret = pyotp.random_base32()
    user.totp_secret = secret
    await db.commit()

    # Build otpauth URI for QR code
    totp = pyotp.TOTP(secret)
    otpauth_uri = totp.provisioning_uri(
        name=user.username,
        issuer_name="WireZTNA",
    )

    return MFASetupResponse(
        secret=secret,
        otpauth_uri=otpauth_uri,
        qr_data=otpauth_uri,
    )


@router.post("/me/mfa/verify", response_model=MFAStatusResponse)
async def mfa_verify_setup(
    data: MFAVerifyRequest,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Verify a TOTP code to confirm MFA enrollment. This activates MFA."""
    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    if not user.totp_secret:
        raise HTTPException(status_code=400, detail="Call /me/mfa/setup first")

    totp = pyotp.TOTP(user.totp_secret)
    if not totp.verify(data.code.strip(), valid_window=1):
        raise HTTPException(status_code=401, detail="Invalid code — check your authenticator app and try again")

    user.totp_enabled = True

    from app.services.audit_service import log_action
    await log_action(db, "portal_mfa_enabled", detail=f"{user.username} enabled TOTP MFA", user_id=user_id)
    await db.commit()

    return MFAStatusResponse(enabled=True)


@router.post("/me/mfa/disable", response_model=MFAStatusResponse)
async def mfa_disable(
    data: MFAVerifyRequest,
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Disable MFA (requires a valid TOTP code to confirm identity)."""
    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    if not user.totp_enabled:
        raise HTTPException(status_code=400, detail="MFA is not enabled")

    totp = pyotp.TOTP(user.totp_secret)
    if not totp.verify(data.code.strip(), valid_window=1):
        raise HTTPException(status_code=401, detail="Invalid code — cannot disable MFA without valid authenticator code")

    user.totp_enabled = False
    user.totp_secret = None

    from app.services.audit_service import log_action
    await log_action(db, "portal_mfa_disabled", detail=f"{user.username} disabled TOTP MFA", user_id=user_id)
    await db.commit()

    return MFAStatusResponse(enabled=False)


@router.get("/me/mfa/status", response_model=MFAStatusResponse)
async def mfa_status(
    db: AsyncSession = Depends(get_db),
    current_user: dict = Depends(get_current_user),
):
    """Check if MFA is enabled for the current user."""
    user_id = current_user.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=404, detail="User not found")

    return MFAStatusResponse(enabled=user.totp_enabled)


# ─── Login Step 2: Verify TOTP after primary auth ───

@router.post("/auth/mfa/verify")
async def mfa_login_verify(
    data: MFALoginRequest,
    db: AsyncSession = Depends(get_db),
):
    """Complete authentication by verifying TOTP code.

    Called after the primary login (OTP email or password) returns a partial mfa_token
    instead of a full access_token.
    """
    # Decode the partial MFA token
    try:
        payload = decode_token(data.mfa_token)
    except Exception:
        raise HTTPException(status_code=401, detail="Invalid or expired MFA token")

    # Verify it's a partial (MFA-pending) token
    if not payload.get("mfa_pending"):
        raise HTTPException(status_code=400, detail="This token does not require MFA verification")

    user_id = payload.get("sub")
    result = await db.execute(select(User).where(User.id == user_id))
    user = result.scalar_one_or_none()
    if not user:
        raise HTTPException(status_code=401, detail="User not found")

    if not user.totp_enabled or not user.totp_secret:
        raise HTTPException(status_code=400, detail="MFA is not configured for this user")

    totp = pyotp.TOTP(user.totp_secret)
    if not totp.verify(data.code.strip(), valid_window=1):
        raise HTTPException(status_code=401, detail="Invalid authenticator code")

    # Issue full access token
    role = getattr(user, "role", None) or ("super_admin" if user.is_admin else "user")
    full_token = create_token({
        "sub": user.id,
        "email": user.email,
        "username": user.username,
        "is_admin": user.is_admin,
        "role": role,
        "org_id": user.org_id,
    })
    return {"access_token": full_token, "token_type": "bearer", "password_change_required": bool(user.must_change_password)}
