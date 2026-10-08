"""Client enrollment token model — one-time tokens for client onboarding."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, Boolean
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class ClientEnrollmentToken(Base):
    """One-time token for client self-enrollment.

    Flow:
        1. Admin creates a token linked to a pre-created user (POST /users/{id}/enrollment-token)
        2. Admin sends the token URL to the user
        3. User runs `wireztna enroll <token_url>`
        4. Client generates WG keypair, sends public_key + token to POST /clients/enroll
        5. API validates token, registers public_key on user, returns full client config
        6. Token is marked as used (one-time only)
    """

    __tablename__ = "client_enrollment_tokens"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    token: Mapped[str] = mapped_column(String(64), unique=True, nullable=False)

    # Pre-linked to an existing user
    user_id: Mapped[str] = mapped_column(String(36), nullable=False)

    # Expiration and usage tracking
    expires_at: Mapped[datetime] = mapped_column(DateTime, nullable=False)
    used_at: Mapped[datetime | None] = mapped_column(DateTime)
    used_from_ip: Mapped[str | None] = mapped_column(String(45))

    # Admin can revoke before use
    revoked: Mapped[bool] = mapped_column(Boolean, default=False)

    # Metadata
    created_by: Mapped[str | None] = mapped_column(String(36))  # Admin user_id who generated it
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    note: Mapped[str | None] = mapped_column(String(500))  # Optional note from admin

    # Device info (populated when token is consumed by the client)
    device_name: Mapped[str | None] = mapped_column(String(255))  # e.g. "MacBook-Sergio"
    device_platform: Mapped[str | None] = mapped_column(String(50))  # e.g. "darwin/arm64"
