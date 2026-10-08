"""User model."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime
from sqlalchemy.orm import Mapped, mapped_column, relationship

from app.database import Base


class User(Base):
    __tablename__ = "users"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    username: Mapped[str] = mapped_column(String(100), nullable=False)  # Display name (not unique — email is the login identifier)
    email: Mapped[str] = mapped_column(String(255), unique=True, nullable=False)  # Primary login identifier
    password_hash: Mapped[str | None] = mapped_column(String(255))  # For local auth (admin)
    public_key: Mapped[str | None] = mapped_column(String(44))  # WireGuard public key
    overlay_ip: Mapped[str | None] = mapped_column(String(45))  # Assigned overlay IP
    status: Mapped[str] = mapped_column(String(20), default="active")
    is_admin: Mapped[bool] = mapped_column(default=False)  # DEPRECATED — use 'role' field instead
    role: Mapped[str] = mapped_column(String(20), default="user")  # "super_admin", "org_admin", "user"
    org_id: Mapped[str | None] = mapped_column(String(36))  # Organization (NULL = legacy/default org)
    auth_provider: Mapped[str] = mapped_column(String(20), default="local")  # "local", "oidc", "both"
    oidc_subject: Mapped[str | None] = mapped_column(String(255))  # OIDC subject (sub) claim — stable user identifier
    vpn_mode: Mapped[bool] = mapped_column(default=False)  # If True, user can select exit nodes for full-tunnel VPN
    max_enrollment_tokens: Mapped[int] = mapped_column(default=3)  # Self-service enrollment token quota
    totp_secret: Mapped[str | None] = mapped_column(String(32))  # TOTP secret (base32 encoded)
    totp_enabled: Mapped[bool] = mapped_column(default=False)  # Whether MFA is active
    mfa_backup_codes: Mapped[str | None] = mapped_column()  # JSON array of hashed backup codes
    must_change_password: Mapped[bool] = mapped_column(default=False)  # Force password change on next login
    plan: Mapped[str] = mapped_column(String(20), default="pro")  # "free", "pro" — existing users default to pro
    canonical_email: Mapped[str | None] = mapped_column(String(255))  # Normalized email (no +alias, no dots for gmail)
    last_activity: Mapped[datetime | None] = mapped_column(DateTime)  # Last API/login activity (for inactivity cleanup)
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)

    # Relationships
    groups = relationship("Group", secondary="user_groups", back_populates="users")
