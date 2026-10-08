"""Free tier cleanup service — deactivates inactive free tier users.

Runs as a background loop (similar to _expire_access_passes_loop).
Checks every N hours (configurable) for free tier users whose last_activity
is older than the inactivity threshold (default: 90 days).

Cleanup actions:
1. Set user status to "inactive"
2. Revoke active API keys
3. Remove WireGuard peer assignments (overlay_ip, public_key)
4. Write audit log entry
5. Optionally send notification email before deactivation

The user's data is NOT deleted — they can re-activate by contacting support
or re-registering (their canonical_email slot is freed when status changes).
"""

import logging
from datetime import datetime, timedelta

from sqlalchemy import select, update, and_
from sqlalchemy.ext.asyncio import AsyncSession

from app.config import settings
from app.models.user import User

logger = logging.getLogger(__name__)


async def cleanup_inactive_freetier_users(db: AsyncSession) -> int:
    """Find and deactivate inactive free tier users.

    Returns the number of users deactivated.
    """
    if not settings.freetier_cleanup_enabled:
        return 0

    cutoff = datetime.utcnow() - timedelta(days=settings.freetier_inactivity_days)

    # Find free tier users who are active but haven't been seen since cutoff
    # Users with NULL last_activity: use created_at as fallback
    result = await db.execute(
        select(User).where(
            User.plan == "free",
            User.status == "active",
            # Inactive if: last_activity < cutoff OR (last_activity is NULL AND created_at < cutoff)
            (
                (User.last_activity != None) & (User.last_activity < cutoff)
            ) | (
                (User.last_activity == None) & (User.created_at < cutoff)
            ),
        )
    )
    inactive_users = list(result.scalars().all())

    if not inactive_users:
        return 0

    deactivated_count = 0

    for user in inactive_users:
        # Deactivate user
        user.status = "inactive"
        # Clear WireGuard assignments (frees overlay IP)
        user.public_key = None
        user.overlay_ip = None

        # Revoke API keys
        from app.models.api_key import ApiKey
        await db.execute(
            update(ApiKey).where(
                ApiKey.user_id == user.id,
                ApiKey.status == "active",
            ).values(status="revoked")
        )

        # Audit log
        from app.models.audit import AccessLog
        days_inactive = (datetime.utcnow() - (user.last_activity or user.created_at)).days
        db.add(AccessLog(
            user_id=user.id,
            action="freetier_cleanup",
            detail=f"Free tier user '{user.email}' deactivated after {days_inactive} days of inactivity",
        ))

        deactivated_count += 1
        logger.info(f"Deactivated inactive free tier user: {user.email} (inactive {days_inactive}d)")

    await db.commit()
    return deactivated_count


async def update_user_activity(user_id: str, db: AsyncSession) -> None:
    """Update the last_activity timestamp for a user.

    Call this from login endpoints, API key usage, and session creation
    to track when a user was last active.
    """
    await db.execute(
        update(User).where(User.id == user_id).values(last_activity=datetime.utcnow())
    )
    # Note: caller is responsible for commit (usually part of a larger transaction)
