"""Organization model — logical grouping for multi-client isolation."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, false
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class Organization(Base):
    """An Organization is a logical tenant within a single broker.

    All users, groups, and publishers belong to exactly one organization.
    Admins can see and manage all organizations. Regular users only see
    resources within their own organization.

    This provides multi-client isolation at the API/UI layer without
    any changes to the underlying network infrastructure (broker, WG,
    nftables, namespaces all remain shared).
    """

    __tablename__ = "organizations"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    name: Mapped[str] = mapped_column(String(100), unique=True, nullable=False)
    slug: Mapped[str] = mapped_column(String(50), unique=True, nullable=False)  # URL-friendly identifier
    description: Mapped[str | None] = mapped_column(String(500))
    # server_default ensures the NOT NULL column has a DB-level default so that
    # create_all DDL and raw-SQL inserts (bootstrap default org) don't violate
    # the NOT NULL constraint on a fresh database.
    mfa_required: Mapped[bool] = mapped_column(default=False, server_default=false())  # If True, all admins in this org must enable MFA
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
