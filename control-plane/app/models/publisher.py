"""Publisher model — agents deployed in networks that expose resources."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, Integer, JSON, Table, Column, ForeignKey, select, func
from sqlalchemy.orm import Mapped, mapped_column, relationship
from sqlalchemy.ext.asyncio import AsyncSession

from app.database import Base

# Association: which groups can access which publishers
group_publishers = Table(
    "group_publishers",
    Base.metadata,
    Column("group_id", String(36), ForeignKey("groups.id", ondelete="CASCADE"), primary_key=True),
    Column("publisher_id", String(36), ForeignKey("publishers.id", ondelete="CASCADE"), primary_key=True),
)


class Publisher(Base):
    """A Publisher is an agent deployed inside a network that exposes resources.

    It creates a WireGuard tunnel to the broker, making its local network
    reachable by authorized clients. The publisher defines what it exposes
    (exposed_cidrs) and where it is (location).
    """

    __tablename__ = "publishers"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    name: Mapped[str] = mapped_column(String(100), nullable=False)
    location: Mapped[str | None] = mapped_column(String(200))  # e.g., "Frankfurt datacenter", "AWS eu-central-1"
    description: Mapped[str | None] = mapped_column(String(500))
    org_id: Mapped[str | None] = mapped_column(String(36))  # Organization (NULL = legacy/default org)

    # Namespace refactor: unique index and virtual CIDR for per-publisher namespaces
    publisher_index: Mapped[int | None] = mapped_column(Integer, unique=True)
    virtual_cidr: Mapped[str | None] = mapped_column(String(18))  # e.g., "10.252.1.0/24"

    # Network resources this publisher exposes (e.g., ["10.50.0.0/16", "192.168.1.0/24"])
    exposed_cidrs: Mapped[list | None] = mapped_column(JSON, default=list)

    # Published apps: fine-grained access rules with IP/FQDN + port + protocol
    # Each entry: {"target": "10.50.1.5" or "db1.internal", "port": 3389, "protocol": "tcp"}
    # ICMP example: {"target": "10.50.1.168/32", "port": 0, "protocol": "icmp"}
    # These take precedence over exposed_cidrs (more specific = higher priority in nftables)
    published_apps: Mapped[list | None] = mapped_column(JSON, default=list)

    # DNS (optional): internal DNS server + zones reachable through this publisher
    dns_server: Mapped[str | None] = mapped_column(String(45))
    dns_zones: Mapped[list | None] = mapped_column(JSON)

    # WireGuard tunnel info
    public_key: Mapped[str | None] = mapped_column(String(44))
    tunnel_ip: Mapped[str | None] = mapped_column(String(45))
    # Publisher's public endpoint (IP:port) derived from heartbeat source IP + listen port 51821
    # NOTE: requires ALTER TABLE publishers ADD COLUMN endpoint VARCHAR(60);
    endpoint: Mapped[str | None] = mapped_column(String(60))
    priority: Mapped[int] = mapped_column(Integer, default=100)

    # Broker namespace connection info (populated by reconciler after namespace creation)
    # The publisher polls GET /publishers/{id}/connection-info until these are set.
    broker_ns_public_key: Mapped[str | None] = mapped_column(String(44))  # Public key of the broker's namespace WG interface
    broker_ns_port: Mapped[int | None] = mapped_column(Integer)  # UDP port the namespace listens on
    broker_tunnel_ip: Mapped[str | None] = mapped_column(String(45))  # Broker-side tunnel IP (e.g., 10.100.4.1)

    # Publisher API key hash — authenticates heartbeat and connection-info calls.
    # NULL = legacy publisher enrolled before auth was added (grace period: allowed but warned).
    # Generated during enrollment or via admin rotate-key / publisher upgrade-key.
    api_key_hash: Mapped[str | None] = mapped_column(String(64))  # SHA-256 of wpk_... key

    # Version (reported via heartbeat — NULL for publishers that haven't reported yet)
    agent_version: Mapped[str | None] = mapped_column(String(50))

    # Exit node: if True, this publisher can be used as a VPN exit (routes 0.0.0.0/0)
    exit_node: Mapped[bool] = mapped_column(default=False)

    # Status
    status: Mapped[str] = mapped_column(String(20), default="pending")  # pending, online, offline
    enrolled_at: Mapped[datetime | None] = mapped_column(DateTime)
    last_heartbeat: Mapped[datetime | None] = mapped_column(DateTime)

    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)

    # Relationships
    groups = relationship("Group", secondary=group_publishers, back_populates="publishers")

    @staticmethod
    async def next_publisher_index(db: AsyncSession) -> int:
        """Return the next available publisher_index (max + 1, starting at 1)."""
        result = await db.execute(
            select(func.coalesce(func.max(Publisher.publisher_index), 0))
        )
        return result.scalar() + 1

    @staticmethod
    def compute_virtual_cidr(index: int) -> str:
        """Compute virtual CIDR from publisher index: 10.252.{index}.0/24."""
        return f"10.252.{index}.0/24"


class EnrollmentToken(Base):
    """One-time token for publisher self-enrollment."""

    __tablename__ = "enrollment_tokens"

    id: Mapped[str] = mapped_column(String(36), primary_key=True, default=lambda: str(uuid.uuid4()))
    token: Mapped[str] = mapped_column(String(64), unique=True, nullable=False)
    publisher_name: Mapped[str | None] = mapped_column(String(100))  # Pre-assigned name
    exposed_cidrs: Mapped[list | None] = mapped_column(JSON)  # Pre-configured CIDRs
    location: Mapped[str | None] = mapped_column(String(200))
    expires_at: Mapped[datetime | None] = mapped_column(DateTime)
    used_by: Mapped[str | None] = mapped_column(String(36))  # Publisher ID that used it
    created_by_user_id: Mapped[str | None] = mapped_column(String(36))  # User who created it (for auto-association)
    auto_group_id: Mapped[str | None] = mapped_column(String(36))  # Group to auto-associate publisher to
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
