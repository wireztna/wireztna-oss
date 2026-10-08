"""OTP code model for email-based authentication."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, Integer, Boolean
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class OTPCode(Base):
    __tablename__ = "otp_codes"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    user_id: Mapped[str] = mapped_column(String(36), nullable=False, index=True)
    code: Mapped[str] = mapped_column(String(10), nullable=False)  # The OTP digits
    attempts: Mapped[int] = mapped_column(Integer, default=0)  # Failed verification attempts
    used: Mapped[bool] = mapped_column(Boolean, default=False)  # Whether the code was consumed
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    expires_at: Mapped[datetime] = mapped_column(DateTime, nullable=False)
