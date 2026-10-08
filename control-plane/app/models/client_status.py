"""Client peer status — real-time connection state reported by the broker."""

from datetime import datetime

from sqlalchemy import String, DateTime, Integer, BigInteger, Boolean
from sqlalchemy.orm import Mapped, mapped_column

from app.database import Base


class ClientPeerStatus(Base):
    """Stores the latest connection state of each client peer as reported by the broker.

    Updated every health check cycle (~15s). Represents a point-in-time snapshot
    of the WireGuard peer on the broker's wg-clients interface.
    """

    __tablename__ = "client_peer_status"

    # Primary key is public_key (one entry per client peer)
    public_key: Mapped[str] = mapped_column(String(44), primary_key=True)

    # Connection state
    endpoint_ip: Mapped[str | None] = mapped_column(String(45))  # Client's public IP:port
    endpoint_port: Mapped[int | None] = mapped_column(Integer)
    last_handshake_at: Mapped[datetime | None] = mapped_column(DateTime)
    is_connected: Mapped[bool] = mapped_column(Boolean, default=False)

    # Traffic counters (cumulative since peer was added)
    rx_bytes: Mapped[int] = mapped_column(BigInteger, default=0)
    tx_bytes: Mapped[int] = mapped_column(BigInteger, default=0)

    # Client version info (reported via session renew — NULL for clients that haven't reported yet)
    client_version: Mapped[str | None] = mapped_column(String(50))
    wg_version: Mapped[str | None] = mapped_column(String(50))
    platform: Mapped[str | None] = mapped_column(String(50))  # e.g. "darwin/arm64", "windows/amd64"

    # Metadata
    last_reported_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    broker_id: Mapped[str | None] = mapped_column(String(36))
