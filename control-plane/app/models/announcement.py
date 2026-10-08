"""System announcements model."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, Text, Boolean
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class SystemAnnouncement(Base):
    __tablename__ = "system_announcements"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    title: Mapped[str] = mapped_column(String(200), nullable=False)
    message: Mapped[str] = mapped_column(Text, nullable=False)
    type: Mapped[str] = mapped_column(String(20), default="info")  # "info", "warning", "success", "update"
    active: Mapped[bool] = mapped_column(Boolean, default=True)
    created_by: Mapped[str | None] = mapped_column(String(36))  # admin user_id
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
