"""Client session model — ephemeral PSK sessions for Zero Trust access."""

import uuid
from datetime import datetime

from sqlalchemy import String, DateTime, Boolean, ForeignKey
from sqlalchemy.orm import Mapped, mapped_column, relationship

from app.database import Base


class ClientSession(Base):
    """Represents an active WireGuard session with a rotatable PSK.

    Each session binds a user to a preshared key that the broker requires
    for the WireGuard handshake to succeed. When the session expires, the
    broker removes the PSK and the tunnel dies — forcing re-authentication.

    Future extensions:
        - OIDC/IdP integration: validate id_token before issuing sessions
        - MFA enforcement: require MFA claim in JWT before renewal
        - Device posture: gate renewal on posture check results
        - Policy evaluation: OPA/Rego policies evaluated at renewal time
    """

    __tablename__ = "client_sessions"

    id: Mapped[str] = mapped_column(
        String(36), primary_key=True, default=lambda: str(uuid.uuid4())
    )
    user_id: Mapped[str] = mapped_column(
        String(36), ForeignKey("users.id", ondelete="CASCADE"), nullable=False, index=True
    )
    preshared_key: Mapped[str] = mapped_column(
        String(64), nullable=False
    )  # Base64-encoded 256-bit key
    expires_at: Mapped[datetime] = mapped_column(DateTime, nullable=False)
    created_at: Mapped[datetime] = mapped_column(DateTime, default=datetime.utcnow)
    client_ip: Mapped[str | None] = mapped_column(String(45))  # IP from which session was created
    selected_group_id: Mapped[str | None] = mapped_column(String(36))  # Group selected by client (for CIDR overlap resolution)
    exit_node_publisher_id: Mapped[str | None] = mapped_column(String(36))  # Publisher used as exit node (VPN mode full tunnel)
    is_active: Mapped[bool] = mapped_column(Boolean, default=True)

    # Relationships
    user = relationship("User", backref="sessions")
