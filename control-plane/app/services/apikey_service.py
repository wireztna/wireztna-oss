"""API Key generation and validation service."""

import hashlib
import secrets
import uuid
from datetime import datetime

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.models.api_key import ApiKey


# Prefix for all API keys — enables detection in auth middleware
API_KEY_PREFIX = "wzk_"


def generate_api_key() -> str:
    """Generate a new API key: wzk_ + 32 hex chars (128 bits entropy)."""
    raw = secrets.token_hex(16)  # 32 hex chars
    return f"{API_KEY_PREFIX}{raw}"


def hash_api_key(plaintext_key: str) -> str:
    """SHA-256 hash of the full key for storage."""
    return hashlib.sha256(plaintext_key.encode()).hexdigest()


def key_prefix(plaintext_key: str) -> str:
    """Extract the first 8 chars for display/identification."""
    return plaintext_key[:8]


async def create_api_key_for_user(
    user_id: str,
    db: AsyncSession,
    label: str = "default",
) -> str:
    """Create a new API key for a user and persist its hash.

    Returns the plaintext key (shown only once).
    """
    plaintext = generate_api_key()

    api_key = ApiKey(
        id=str(uuid.uuid4()),
        user_id=user_id,
        key_hash=hash_api_key(plaintext),
        key_prefix=key_prefix(plaintext),
        label=label,
        status="active",
        created_at=datetime.utcnow(),
    )
    db.add(api_key)

    return plaintext


async def validate_api_key(plaintext_key: str, db: AsyncSession) -> dict | None:
    """Validate an API key and return user claims if valid.

    Returns a dict matching the shape of JWT claims, or None if invalid/revoked.
    Also updates last_used_at.
    """
    from app.models.user import User

    key_hash_val = hash_api_key(plaintext_key)

    result = await db.execute(
        select(ApiKey).where(
            ApiKey.key_hash == key_hash_val,
            ApiKey.status == "active",
        )
    )
    api_key = result.scalar_one_or_none()

    if not api_key:
        return None

    # Load the associated user
    user_result = await db.execute(
        select(User).where(User.id == api_key.user_id)
    )
    user = user_result.scalar_one_or_none()

    if not user or user.status != "active":
        return None

    # Update last_used_at
    api_key.last_used_at = datetime.utcnow()

    # Update user's last_activity (for inactivity cleanup)
    user.last_activity = datetime.utcnow()

    await db.commit()

    # Return claims matching JWT shape
    return {
        "sub": user.id,
        "email": user.email,
        "username": user.username,
        "is_admin": user.is_admin,
        "role": getattr(user, "role", None) or ("super_admin" if user.is_admin else "user"),
        "org_id": user.org_id,
        "plan": getattr(user, "plan", "pro"),
        "auth_method": "api_key",
    }


async def revoke_api_key(key_id: str, user_id: str, db: AsyncSession) -> bool:
    """Revoke an API key. Returns True if found and revoked."""
    result = await db.execute(
        select(ApiKey).where(
            ApiKey.id == key_id,
            ApiKey.user_id == user_id,
            ApiKey.status == "active",
        )
    )
    api_key = result.scalar_one_or_none()

    if not api_key:
        return False

    api_key.status = "revoked"
    api_key.revoked_at = datetime.utcnow()
    await db.commit()
    return True


async def revoke_all_keys(user_id: str, db: AsyncSession) -> int:
    """Revoke all active API keys for a user. Returns count revoked."""
    result = await db.execute(
        select(ApiKey).where(
            ApiKey.user_id == user_id,
            ApiKey.status == "active",
        )
    )
    keys = list(result.scalars().all())

    now = datetime.utcnow()
    for key in keys:
        key.status = "revoked"
        key.revoked_at = now

    if keys:
        await db.commit()

    return len(keys)
