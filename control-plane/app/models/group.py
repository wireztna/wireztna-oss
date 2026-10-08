"""Group model — organizes users and controls access to publishers."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, Table, Column, ForeignKey
from sqlalchemy.orm import Mapped, mapped_column, relationship

from app.database import Base

# Association: which users belong to which groups
user_groups = Table(
    "user_groups",
    Base.metadata,
    Column("user_id", String(36), ForeignKey("users.id", ondelete="CASCADE"), primary_key=True),
    Column("group_id", String(36), ForeignKey("groups.id", ondelete="CASCADE"), primary_key=True),
)


class Group(Base):
    """A Group contains users and has access to publishers.

    The access model is: User → Group → Publisher.
    If a user is in a group that has access to a publisher, the user can
    reach the networks exposed by that publisher.
    """

    __tablename__ = "groups"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    name: Mapped[str] = mapped_column(String(100), nullable=False)  # Unique within org, not globally
    description: Mapped[str | None] = mapped_column(String(500))
    org_id: Mapped[str | None] = mapped_column(String(36))  # Organization (NULL = legacy/default org)
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)

    # Relationships
    users = relationship("User", secondary=user_groups, back_populates="groups")
    publishers = relationship("Publisher", secondary="group_publishers", back_populates="groups")
