"""Audit logging service — records user and system actions."""

from datetime import datetime

from sqlalchemy.ext.asyncio import AsyncSession

from app.models.audit import AccessLog


async def log_action(
    db: AsyncSession,
    action: str,
    detail: str | None = None,
    user_id: str | None = None,
    client_ip: str | None = None,
    resource_id: str | None = None,
    publisher_id: str | None = None,
):
    """Record an action in the audit log.

    Common actions for portal self-service:
    - portal_login: User logged in via portal
    - portal_email_changed: User updated their email
    - portal_password_changed: User changed their password
    - portal_mfa_enabled: User activated TOTP MFA
    - portal_mfa_disabled: User deactivated MFA
    - portal_token_generated: User generated an enrollment token
    - portal_accessed: User accessed the portal
    - admin_onboarding_sent: Admin sent onboarding email
    - admin_tokens_reset: Admin reset enrollment tokens
    """
    log = AccessLog(
        timestamp=datetime.utcnow(),
        user_id=user_id,
        resource_id=resource_id,
        publisher_id=publisher_id,
        action=action,
        detail=detail,
        client_ip=client_ip,
    )
    db.add(log)
    # Don't commit here — let the caller's transaction handle it
