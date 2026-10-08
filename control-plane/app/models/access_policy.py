"""Access policy model — restrictive app rules per group↔publisher pair.

When a group↔publisher pair has an access policy set to "restricted",
clients in that group can only reach the specific apps (target+port+protocol)
defined in the rules — NOT the full exposed_cidrs of the publisher.

If no policy exists (or policy is "unrestricted"), the group has full access
to all exposed_cidrs — backward compatible with the existing behavior.
"""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, ForeignKey, UniqueConstraint
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class GroupPublisherPolicy(Base):
    """Access policy for a specific group↔publisher relationship.

    access_policy:
        - "unrestricted" (default, explicit) — full CIDR access, same as no policy
        - "restricted" — only allowed_apps rules are permitted (+ DNS always allowed)

    If no row exists for a group↔publisher pair, it's treated as unrestricted.
    """

    __tablename__ = "group_publisher_policies"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    group_id: Mapped[str] = mapped_column(String(36), ForeignKey("groups.id", ondelete="CASCADE"), nullable=False)
    publisher_id: Mapped[str] = mapped_column(String(36), ForeignKey("publishers.id", ondelete="CASCADE"), nullable=False)
    access_policy: Mapped[str] = mapped_column(String(20), default="unrestricted")  # "unrestricted" | "restricted"
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)

    __table_args__ = (
        UniqueConstraint("group_id", "publisher_id", name="uq_group_publisher_policy"),
    )


class AccessRule(Base):
    """A single access rule within a restricted group↔publisher policy.

    Defines one allowed app (target + port + protocol) that clients in the
    group can reach through the publisher.

    Examples:
        target="10.50.1.5", port="443", protocol="tcp"       → HTTPS to specific IP
        target="10.50.1.0/24", port="8080-8090", protocol="tcp" → port range to subnet
        target="app.internal", port="443", protocol="tcp"     → FQDN (resolved via DNS hints)
        target="10.50.1.10", port="3306", protocol="tcp"      → MySQL

    Port supports:
        - Single port: "443"
        - Range: "8080-8090"
        - Any: "0" or empty (all ports — use sparingly)

    DNS (53/udp to broker overlay) is always allowed implicitly and does not
    need a rule.
    """

    __tablename__ = "access_rules"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    policy_id: Mapped[str] = mapped_column(String(36), ForeignKey("group_publisher_policies.id", ondelete="CASCADE"), nullable=False)
    target: Mapped[str] = mapped_column(String(255), nullable=False)  # IP, CIDR, or FQDN
    port: Mapped[str] = mapped_column(String(20), default="0")  # "443", "8080-8090", "0" (any)
    protocol: Mapped[str] = mapped_column(String(10), default="tcp")  # "tcp", "udp", "icmp", "any"
    name: Mapped[str | None] = mapped_column(String(200))  # Human-readable label
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
