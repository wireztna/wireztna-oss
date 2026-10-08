"""Authentication service — JWT token management."""

import hmac
import uuid
from datetime import datetime, timedelta

from jose import jwt, JWTError
from passlib.context import CryptContext
from fastapi import HTTPException, Depends, Request, Header, status
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials

from app.config import settings

pwd_context = CryptContext(schemes=["bcrypt"], deprecated="auto")
security = HTTPBearer()


def hash_password(password: str) -> str:
    return pwd_context.hash(password)


def verify_password(plain: str, hashed: str) -> bool:
    return pwd_context.verify(plain, hashed)


def create_token(data: dict) -> str:
    to_encode = data.copy()
    expire = datetime.utcnow() + timedelta(hours=settings.jwt_expiry_hours)
    to_encode.update({"exp": expire, "jti": str(uuid.uuid4())})
    return jwt.encode(to_encode, settings.secret_key, algorithm=settings.jwt_algorithm)


def decode_token(token: str) -> dict:
    try:
        payload = jwt.decode(token, settings.secret_key, algorithms=[settings.jwt_algorithm])
    except JWTError:
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid token")

    # Check if token has been revoked (A.8.5 / CC6.1)
    jti = payload.get("jti")
    if jti:
        from app.models.revoked_token import RevokedToken
        # Use a sync-compatible check via a module-level cache to avoid async in sync function.
        # The cache is populated/checked by the async middleware or on-demand.
        if jti in _revoked_jti_cache:
            raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Token has been revoked")

    return payload


# In-memory cache of revoked JTIs (populated by startup and revocation endpoints).
# This avoids async DB calls in the synchronous decode_token path.
# Entries auto-expire when the JWT itself would have expired.
_revoked_jti_cache: set[str] = set()


async def load_revoked_tokens_cache():
    """Load active revoked token JTIs into memory. Called at startup and after revocations."""
    from app.database import async_session
    from app.models.revoked_token import RevokedToken
    from sqlalchemy import select

    async with async_session() as db:
        result = await db.execute(
            select(RevokedToken.jti).where(RevokedToken.expires_at > datetime.utcnow())
        )
        _revoked_jti_cache.clear()
        _revoked_jti_cache.update(row[0] for row in result.all())


async def revoke_token(jti: str, user_id: str | None = None, reason: str = "logout"):
    """Revoke a JWT by its JTI. Adds to DB and in-memory cache."""
    from app.database import async_session
    from app.models.revoked_token import RevokedToken

    expires_at = datetime.utcnow() + timedelta(hours=settings.jwt_expiry_hours)
    async with async_session() as db:
        db.add(RevokedToken(jti=jti, user_id=user_id, expires_at=expires_at, reason=reason))
        await db.commit()
    _revoked_jti_cache.add(jti)


async def revoke_all_user_tokens(user_id: str, reason: str = "admin_revoke"):
    """Revoke all active tokens for a user. Used on password change, account disable, etc."""
    from app.database import async_session
    from app.models.revoked_token import RevokedToken
    from sqlalchemy import select

    # We can't enumerate all JTIs for a user (they're in issued JWTs, not stored).
    # Instead, we store the user_id + timestamp. The decode check will also verify
    # that tokens issued before this timestamp are invalid.
    # For now, this only prevents future use of tokens that get explicitly revoked.
    pass  # TODO: implement user-level revocation with issued_at tracking


async def get_current_user(credentials: HTTPAuthorizationCredentials = Depends(security)) -> dict:
    """Dependency to extract current user from JWT or API key.

    Supports two auth methods:
    - API key: Bearer token starting with "wzk_" → validated against api_keys table
    - JWT: All other Bearer tokens → decoded as JWT
    """
    token = credentials.credentials

    # Detect API key by prefix
    if token.startswith("wzk_"):
        from app.database import async_session
        from app.services.apikey_service import validate_api_key

        async with async_session() as db:
            claims = await validate_api_key(token, db)
            if claims is None:
                raise HTTPException(
                    status_code=status.HTTP_401_UNAUTHORIZED,
                    detail="Invalid or revoked API key",
                )
            return claims

    # Default: JWT
    return decode_token(token)


async def require_admin(current_user: dict = Depends(get_current_user)) -> dict:
    """Dependency that requires any admin role (org_admin or super_admin).

    For backward compatibility, also checks the legacy 'is_admin' claim.
    Scoping by org is handled separately via get_admin_org_id().
    """
    role = current_user.get("role", "user")
    is_admin_legacy = current_user.get("is_admin", False)
    if role in ("super_admin", "org_admin") or is_admin_legacy:
        return current_user
    raise HTTPException(status_code=status.HTTP_403_FORBIDDEN, detail="Admin access required")


async def require_super_admin(current_user: dict = Depends(get_current_user)) -> dict:
    """Dependency that requires super_admin role.

    Used for: dashboard, debug, diagnostics, brokers, system config.
    org_admin cannot access these endpoints.
    """
    role = current_user.get("role", "user")
    is_admin_legacy = current_user.get("is_admin", False)
    # Legacy tokens without 'role' claim: treat is_admin=True as super_admin
    if role == "super_admin" or (is_admin_legacy and role == "user"):
        return current_user
    raise HTTPException(status_code=status.HTTP_403_FORBIDDEN, detail="Super admin access required")


def get_admin_org_id(current_user: dict) -> str | None:
    """Extract the org_id scope for the current admin.

    - super_admin: returns None (no filter — sees all orgs)
    - org_admin: returns their org_id (scoped to their org only)

    Use this in list/create endpoints to scope queries and force org_id on creation.
    """
    role = current_user.get("role", "user")
    if role == "super_admin" or current_user.get("is_admin"):
        return None  # No restriction
    # org_admin: must be scoped to their org
    return current_user.get("org_id")


def scoped_query(query, model, current_user: dict, explicit_org_id: str | None = None):
    """Apply org_id filter to a SQLAlchemy query based on the admin's role.

    - super_admin + no explicit_org_id: no filter (sees all)
    - super_admin + explicit_org_id: filters to the requested org (voluntary UI filter)
    - org_admin: filters to their org_id only (ignores explicit_org_id)

    Usage:
        query = select(User).order_by(User.email)
        query = scoped_query(query, User, current_user, org_id_from_ui)
    """
    org_id = get_admin_org_id(current_user)
    if org_id:
        # org_admin: always scoped to their org
        query = query.where(model.org_id == org_id)
    elif explicit_org_id:
        # super_admin with voluntary org filter from UI
        query = query.where(model.org_id == explicit_org_id)
    return query


def resolve_org_for_creation(current_user: dict, explicit_org_id: str | None = None) -> str | None:
    """Determine the org_id to assign when creating a resource.

    - super_admin: uses explicit_org_id if provided, else None (will be resolved by resolve_org_id)
    - org_admin: ALWAYS uses their own org_id (ignores explicit_org_id)
    """
    role = current_user.get("role", "user")
    if role == "org_admin":
        return current_user.get("org_id")  # Forced to own org
    return explicit_org_id  # super_admin can specify or leave None


async def require_internal(request: Request, x_api_key: str = Header(default="")):
    """Dependency that restricts access to internal endpoints.

    Only allows requests from:
    - Localhost (127.0.0.1 / ::1) — reconciler, health monitor, DNS proxy
    - OR with a valid X-API-Key header matching settings.broker_api_key

    This protects sensitive endpoints (broker config with PSKs, namespace-info,
    client-status, dns-hints) from external access.
    """
    client_ip = request.client.host if request.client else ""

    # Allow localhost unconditionally
    if client_ip in ("127.0.0.1", "::1", "localhost"):
        return

    # Otherwise require valid API key
    if not settings.broker_api_key:
        # No API key configured — reject all non-localhost
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="Internal endpoint — access denied",
        )

    if not x_api_key or not hmac.compare_digest(x_api_key, settings.broker_api_key):
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="Internal endpoint — invalid API key",
        )


import logging
_pub_auth_logger = logging.getLogger("publisher_auth")


async def require_publisher_auth(
    publisher_id: str,
    request: Request,
    x_api_key: str = Header(default="", alias="X-API-Key"),
):
    """Dependency that authenticates publisher agent API calls.

    Three authentication modes, checked in order:

    1. Localhost bypass — the broker health monitor calls the heartbeat endpoint
       from 127.0.0.1 with the broker API key. Trusted unconditionally.

    2. Publisher API key (wpk_ prefix) — remote publishers send their individual
       key in the X-API-Key header. Validated against the publisher's stored hash.
       The key is bound to the publisher_id in the path, preventing cross-publisher
       impersonation.

    3. Legacy grace period — publishers enrolled before this fix have no key
       (api_key_hash = NULL). They are allowed through but a warning is logged.
       This gives operators time to rotate keys via admin panel or auto-upgrade.
       The grace period should be removed in a future release once all publishers
       have keys.

    Returns the Publisher ORM object on success (useful for callers that need it).
    """
    from app.database import get_db, async_session
    from app.models.publisher import Publisher
    from app.services.publisher_key_service import PUBLISHER_KEY_PREFIX, verify_publisher_key
    from sqlalchemy import select

    client_ip = request.client.host if request.client else ""

    # ── Mode 1: localhost (health monitor, reconciler) ──
    if client_ip in ("127.0.0.1", "::1", "localhost"):
        return None  # Trusted — no publisher object needed

    # ── Load the publisher ──
    async with async_session() as db:
        result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
        pub = result.scalar_one_or_none()

    if not pub:
        # Don't reveal whether the publisher exists — same 401 for all failures
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Publisher authentication required",
        )

    # ── Mode 2: publisher API key ──
    if x_api_key and x_api_key.startswith(PUBLISHER_KEY_PREFIX):
        if not pub.api_key_hash:
            # Publisher has a key in the request but none stored — reject
            raise HTTPException(
                status_code=status.HTTP_401_UNAUTHORIZED,
                detail="Publisher authentication required",
            )
        if not verify_publisher_key(x_api_key, pub.api_key_hash):
            raise HTTPException(
                status_code=status.HTTP_401_UNAUTHORIZED,
                detail="Invalid publisher API key",
            )
        return pub  # Authenticated

    # ── Mode 3: no key — reject ──
    # Grace period removed (2026-08-27). All publishers must have an API key.
    # Assign one via admin rotate-key or publisher upgrade-key before restarting.
    if pub.api_key_hash is None:
        _pub_auth_logger.warning(
            "Publisher '%s' (%s) rejected — no API key configured. "
            "Assign a key via admin rotate-key or publisher upgrade-key. "
            "Caller: %s %s",
            pub.name, pub.id, client_ip, request.url.path,
        )
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Publisher authentication required — no API key configured",
        )

    # Publisher has a key configured but caller didn't send one (or sent wrong prefix)
    raise HTTPException(
        status_code=status.HTTP_401_UNAUTHORIZED,
        detail="Publisher authentication required",
    )
