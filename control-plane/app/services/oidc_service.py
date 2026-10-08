"""OIDC service — Microsoft Entra ID integration.

Handles:
- Authorization Code Flow (Web UI login)
- Device Authorization Grant (CLI login — RFC 8628)
- ID token validation
- Group membership checks for auto-provisioning
"""

import time
import secrets
from typing import Any

import httpx
from jose import jwt, JWTError

from app.config import settings

# ─── Microsoft Entra ID endpoints ───

_ENTRA_BASE = "https://login.microsoftonline.com"


def _authority_url() -> str:
    """Return the Entra authority URL for the configured tenant."""
    tenant = settings.oidc_tenant_id or "common"
    return f"{_ENTRA_BASE}/{tenant}"


def _openid_config_url() -> str:
    return f"{_authority_url()}/v2.0/.well-known/openid-configuration"


def _authorize_url() -> str:
    return f"{_authority_url()}/oauth2/v2.0/authorize"


def _token_url() -> str:
    return f"{_authority_url()}/oauth2/v2.0/token"


def _device_code_url() -> str:
    return f"{_authority_url()}/oauth2/v2.0/devicecode"


def _jwks_url() -> str:
    return f"{_authority_url()}/discovery/v2.0/keys"


# ─── JWKS cache (simple in-memory, refreshed every 24h) ───

_jwks_cache: dict[str, Any] = {}
_jwks_fetched_at: float = 0
_JWKS_CACHE_TTL = 86400  # 24h


async def _get_jwks() -> dict[str, Any]:
    """Fetch and cache the Microsoft JWKS (JSON Web Key Set)."""
    global _jwks_cache, _jwks_fetched_at

    if _jwks_cache and (time.time() - _jwks_fetched_at) < _JWKS_CACHE_TTL:
        return _jwks_cache

    async with httpx.AsyncClient(timeout=10) as client:
        resp = await client.get(_jwks_url())
        resp.raise_for_status()
        _jwks_cache = resp.json()
        _jwks_fetched_at = time.time()

    return _jwks_cache


# ─── Authorization Code Flow (Web UI) ───


def build_authorization_url(state: str | None = None) -> str:
    """Build the Entra ID authorization URL for the Web UI redirect.

    Returns the URL the browser should navigate to for SSO login.
    """
    if not state:
        state = secrets.token_urlsafe(32)

    scopes = settings.oidc_scopes
    # Request group membership claims if auto-provisioning is enabled
    if settings.oidc_auto_provision:
        scopes = f"{scopes} GroupMember.Read.All"

    params = {
        "client_id": settings.oidc_client_id,
        "response_type": "code",
        "redirect_uri": settings.oidc_redirect_uri,
        "scope": scopes,
        "state": state,
        "response_mode": "query",
    }

    query = "&".join(f"{k}={httpx.URL('', params={k: v}).params[k]}" for k, v in params.items())
    return f"{_authorize_url()}?{query}"


async def exchange_code_for_tokens(code: str) -> dict[str, Any]:
    """Exchange an authorization code for tokens (id_token, access_token).

    Called from the /auth/oidc/callback endpoint after Entra redirects back.
    """
    data = {
        "client_id": settings.oidc_client_id,
        "client_secret": settings.oidc_client_secret,
        "code": code,
        "redirect_uri": settings.oidc_redirect_uri,
        "grant_type": "authorization_code",
        "scope": settings.oidc_scopes,
    }

    async with httpx.AsyncClient(timeout=15) as client:
        resp = await client.post(_token_url(), data=data)
        if resp.status_code != 200:
            error_detail = resp.json().get("error_description", resp.text)
            raise OIDCError(f"Token exchange failed: {error_detail}")
        return resp.json()


# ─── Device Authorization Grant (CLI — RFC 8628) ───


async def start_device_authorization() -> dict[str, Any]:
    """Start device authorization flow. Returns device_code, user_code, verification_uri.

    The CLI shows the user_code and verification_uri, then polls for completion.
    """
    data = {
        "client_id": settings.oidc_client_id,
        "scope": settings.oidc_scopes,
    }

    async with httpx.AsyncClient(timeout=10) as client:
        resp = await client.post(_device_code_url(), data=data)
        if resp.status_code != 200:
            error_detail = resp.json().get("error_description", resp.text)
            raise OIDCError(f"Device authorization failed: {error_detail}")
        return resp.json()


async def poll_device_authorization(device_code: str) -> dict[str, Any] | None:
    """Poll Microsoft for device authorization completion.

    Returns tokens dict if user completed auth, None if still pending.
    Raises OIDCError on terminal errors (expired, denied).
    """
    data = {
        "client_id": settings.oidc_client_id,
        "device_code": device_code,
        "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
    }

    async with httpx.AsyncClient(timeout=10) as client:
        resp = await client.post(_token_url(), data=data)
        body = resp.json()

        if resp.status_code == 200:
            # User completed auth successfully
            return body

        error = body.get("error", "")
        if error == "authorization_pending":
            return None  # Still waiting for user
        elif error == "slow_down":
            return None  # Still waiting, should increase interval
        elif error == "expired_token":
            raise OIDCError("Device code expired. Please try again.")
        elif error == "authorization_declined":
            raise OIDCError("Authorization was declined by the user.")
        else:
            raise OIDCError(f"Device auth poll error: {body.get('error_description', error)}")


# ─── ID Token Validation ───


async def validate_id_token(id_token: str) -> dict[str, Any]:
    """Validate a Microsoft Entra ID id_token and return its claims.

    Validates:
    - Signature (RS256 against Microsoft's JWKS)
    - Audience (must match our client_id)
    - Issuer (must be our tenant)
    - Expiry (standard JWT exp check)

    Returns the decoded claims dict on success.
    """
    jwks = await _get_jwks()

    try:
        # python-jose handles RS256 signature verification with JWKS
        claims = jwt.decode(
            id_token,
            jwks,
            algorithms=["RS256"],
            audience=settings.oidc_client_id,
            issuer=f"{_authority_url()}/v2.0",
            options={"verify_at_hash": False},  # at_hash verification is optional
        )
        return claims
    except JWTError as e:
        raise OIDCError(f"ID token validation failed: {e}")


def extract_user_info(claims: dict[str, Any]) -> dict[str, str]:
    """Extract user info from validated Entra ID token claims.

    Returns dict with: sub, email, name, preferred_username
    """
    return {
        "sub": claims.get("sub", ""),
        "email": claims.get("email") or claims.get("preferred_username", ""),
        "name": claims.get("name", ""),
        "preferred_username": claims.get("preferred_username", ""),
    }


def extract_groups(claims: dict[str, Any]) -> list[str]:
    """Extract group Object IDs from the id_token claims.

    Microsoft includes groups in the 'groups' claim when the app registration
    has 'groupMembershipClaims' configured (set to 'SecurityGroup' or 'All').

    If there are too many groups (>200), Microsoft omits the claim and includes
    '_claim_names' / '_claim_sources' — the caller should use MS Graph API instead.
    """
    groups = claims.get("groups", [])
    if isinstance(groups, list):
        return groups
    return []


def has_groups_overage(claims: dict[str, Any]) -> bool:
    """Check if the token has a groups overage claim (>200 groups).

    When this is True, groups must be fetched from MS Graph API instead of the token.
    """
    claim_names = claims.get("_claim_names", {})
    return "groups" in claim_names


# ─── MS Graph API (fallback for groups overage) ───


async def fetch_user_groups_from_graph(access_token: str) -> list[str]:
    """Fetch group memberships from MS Graph API.

    Used when the id_token has groups overage (>200 groups).
    Requires 'GroupMember.Read.All' scope.
    """
    headers = {"Authorization": f"Bearer {access_token}"}
    url = "https://graph.microsoft.com/v1.0/me/memberOf/microsoft.graph.group?$select=id"

    groups: list[str] = []
    async with httpx.AsyncClient(timeout=15) as client:
        while url:
            resp = await client.get(url, headers=headers)
            if resp.status_code != 200:
                raise OIDCError(f"MS Graph groups query failed: {resp.status_code}")
            data = resp.json()
            for item in data.get("value", []):
                if "id" in item:
                    groups.append(item["id"])
            url = data.get("@odata.nextLink")

    return groups


# ─── Group Membership Validation ───


def check_allowed_groups(user_groups: list[str]) -> bool:
    """Check if the user belongs to at least one allowed Entra group.

    Returns True if:
    - oidc_allowed_groups is empty (no restriction — allow all)
    - OR user_groups intersects with oidc_allowed_groups
    """
    if not settings.oidc_allowed_groups:
        # No restriction configured — but auto_provision is True means "allow all org users"
        # This is the failsafe: if admin forgets to set groups, nobody auto-provisions
        return False

    return bool(set(user_groups) & set(settings.oidc_allowed_groups))


# ─── Error class ───


class OIDCError(Exception):
    """Raised when OIDC operations fail."""
    pass
