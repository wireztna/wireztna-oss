"""Access Pass model for delegated access."""

from datetime import datetime

from sqlalchemy import String, DateTime, Integer, Text, ForeignKey
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class AccessPass(Base):
    __tablename__ = "access_passes"

    id: Mapped[str] = mapped_column(String(12), primary_key=True)  # "dap_XXXXXX"
    created_by_user_id: Mapped[str] = mapped_column(String(36), ForeignKey("users.id", ondelete="CASCADE"), nullable=False)
    label: Mapped[str] = mapped_column(String(200), nullable=False)
    scope_json: Mapped[str] = mapped_column(Text, nullable=False)
    ttl_seconds: Mapped[int] = mapped_column(Integer, nullable=False)
    status: Mapped[str] = mapped_column(String(20), nullable=False, default="active")
    connection_url: Mapped[str] = mapped_column(String(500), nullable=False)
    bytes_uploaded: Mapped[int] = mapped_column(Integer, default=0)
    bytes_downloaded: Mapped[int] = mapped_column(Integer, default=0)
    connections_count: Mapped[int] = mapped_column(Integer, default=0)
    last_activity_at: Mapped[datetime | None] = mapped_column(DateTime)
    metadata_json: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime, nullable=False, default=datetime.utcnow)
    expires_at: Mapped[datetime] = mapped_column(DateTime, nullable=False)
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime)
    max_bytes: Mapped[int | None] = mapped_column(Integer)
    max_connections: Mapped[int] = mapped_column(Integer, default=100)
