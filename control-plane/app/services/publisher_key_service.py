"""Publisher API Key service — generation, hashing, and validation.

Publisher keys use the 'wpk_' prefix (WireZTNA Publisher Key) to distinguish
them from user API keys ('wzk_' prefix). They authenticate publisher agents
calling the heartbeat and connection-info endpoints.

Follows the same pattern as apikey_service.py for users.
"""

import hashlib
import hmac
import secrets
import logging

from sqlalchemy.ext.asyncio import AsyncSession

from app.models.publisher import Publisher

logger = logging.getLogger(__name__)

# Prefix for publisher API keys — enables detection in auth middleware
PUBLISHER_KEY_PREFIX = "wpk_"


def generate_publisher_key() -> str:
    """Generate a new publisher API key: wpk_ + 32 hex chars (128 bits entropy)."""
    raw = secrets.token_hex(16)
    return f"{PUBLISHER_KEY_PREFIX}{raw}"


def hash_publisher_key(plaintext_key: str) -> str:
    """SHA-256 hash of the full key for storage."""
    return hashlib.sha256(plaintext_key.encode()).hexdigest()


def verify_publisher_key(plaintext_key: str, stored_hash: str) -> bool:
    """Constant-time comparison of a plaintext key against a stored hash."""
    computed = hash_publisher_key(plaintext_key)
    return hmac.compare_digest(computed, stored_hash)


async def assign_publisher_key(publisher: Publisher, db: AsyncSession) -> str:
    """Generate a new API key for a publisher, hash and store it.

    Returns the plaintext key (shown only once — caller must deliver it).
    Overwrites any existing key (rotation).
    """
    plaintext = generate_publisher_key()
    publisher.api_key_hash = hash_publisher_key(plaintext)
    await db.flush()
    return plaintext


async def revoke_publisher_key(publisher: Publisher, db: AsyncSession) -> None:
    """Remove a publisher's API key (sets hash to NULL)."""
    publisher.api_key_hash = None
    await db.flush()
