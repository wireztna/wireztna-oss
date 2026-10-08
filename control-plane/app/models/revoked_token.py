"""Revoked JWT tokens — compliance control A.8.5 / CC6.1.

Stores JTI (JWT ID) of revoked tokens so they can be rejected
before expiration. Entries are cleaned up once past their original
expiry time.
"""

from datetime import datetime

from sqlalchemy import String, DateTime
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class RevokedToken(Base):
    __tablename__ = "revoked_tokens"

    jti: Mapped[str] = mapped_column(String(36), primary_key=True)  # JWT ID
    user_id: Mapped[str | None] = mapped_column(String(36))
    revoked_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    expires_at: Mapped[datetime] = mapped_column(DateTime)  # Original JWT expiry — for cleanup
    reason: Mapped[str | None] = mapped_column(String(200))  # "logout", "password_change", "admin_revoke"
