"""Monthly pass usage tracking for free tier rate limiting."""

import uuid
from datetime import datetime

from sqlalchemy import String, Integer, ForeignKey, UniqueConstraint
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class PassUsageMonthly(Base):
    __tablename__ = "pass_usage_monthly"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    user_id: Mapped[str] = mapped_column(String(36), ForeignKey("users.id", ondelete="CASCADE"), nullable=False)
    month: Mapped[str] = mapped_column(String(7), nullable=False)  # "2026-07"
    passes_created: Mapped[int] = mapped_column(Integer, nullable=False, default=0)

    __table_args__ = (
        UniqueConstraint("user_id", "month", name="uq_pass_usage_user_month"),
    )
