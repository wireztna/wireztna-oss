"""Access log model for audit trail."""

from datetime import datetime

from sqlalchemy import String, DateTime, Integer, BigInteger
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class AccessLog(Base):
    __tablename__ = "access_logs"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    timestamp: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    user_id: Mapped[str | None] = mapped_column(String(36))
    resource_id: Mapped[str | None] = mapped_column(String(36))
    publisher_id: Mapped[str | None] = mapped_column(String(36))
    action: Mapped[str] = mapped_column(String(50))  # connect, disconnect, denied, admin_action
    detail: Mapped[str | None] = mapped_column(String(1000))
    client_ip: Mapped[str | None] = mapped_column(String(45))
    duration_seconds: Mapped[int | None] = mapped_column(Integer)
    bytes_transferred: Mapped[int | None] = mapped_column(BigInteger)
