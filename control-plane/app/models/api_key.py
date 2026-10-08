"""API Key model for programmatic authentication (free tier + CLI)."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, ForeignKey, Index
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class ApiKey(Base):
    __tablename__ = "api_keys"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    user_id: Mapped[str] = mapped_column(String(36), ForeignKey("users.id", ondelete="CASCADE"), nullable=False)
    key_hash: Mapped[str] = mapped_column(String(64), nullable=False)  # SHA-256 hash
    key_prefix: Mapped[str] = mapped_column(String(8), nullable=False)  # e.g., "wzk_a8f3"
    label: Mapped[str] = mapped_column(String(100), nullable=False, default="default")
    status: Mapped[str] = mapped_column(String(20), nullable=False, default="active")  # "active", "revoked"
    last_used_at: Mapped[datetime | None] = mapped_column(DateTime)
    created_at: Mapped[datetime] = mapped_column(DateTime, nullable=False, default=datetime.utcnow)
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime)

    __table_args__ = (
        Index("ix_api_keys_key_hash", "key_hash"),
        Index("ix_api_keys_user_id", "user_id"),
    )
