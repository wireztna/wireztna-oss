"""Pydantic schemas for all API endpoints."""

from datetime import datetime
from pydantic import BaseModel, Field


# ─── Auth ───
class LoginRequest(BaseModel):
    """Login with email (preferred) or username (backward compat for old clients)."""
    email: str | None = None
    username: str | None = None  # Backward compat — old clients send username
    password: str

    def get_login_identifier(self) -> str:
        """Return the identifier to look up (email takes priority)."""
        return self.email or self.username or ""


class TokenResponse(BaseModel):
    access_token: str
    token_type: str = "bearer"


# ─── OTP (One-Time Password) ───

class OTPRequestSchema(BaseModel):
    """Request an OTP code — email (preferred) or username (backward compat)."""
    email: str | None = None
    username: str | None = None  # Backward compat — old clients send username

    def get_login_identifier(self) -> str:
        """Return the identifier to look up (email takes priority)."""
        return self.email or self.username or ""


class OTPRequestResponse(BaseModel):
    """Response after requesting an OTP code."""
    message: str = "If the account exists, a code has been sent to the registered email"
    email_hint: str | None = None  # Masked email hint, e.g., "s***o@company.com"
    expires_in: int = 300  # Seconds until code expires


class OTPVerifySchema(BaseModel):
    """Verify an OTP code to complete authentication."""
    email: str | None = None
    username: str | None = None  # Backward compat — old clients send username
    code: str = Field(..., min_length=4, max_length=10)

    def get_login_identifier(self) -> str:
        """Return the identifier to look up (email takes priority)."""
        return self.email or self.username or ""


# ─── OIDC / SSO ───

class OIDCConfigResponse(BaseModel):
    """Public OIDC configuration (returned to UI/CLI to know if SSO is available)."""
    enabled: bool
    provider_url: str | None = None
    login_url: str | None = None  # Full /auth/oidc/login URL for the UI redirect
    email_enabled: bool = False  # True when SMTP is configured (OTP + onboarding email work)
    otp_enabled: bool = False  # True when SMTP is configured (email OTP login available)
    setup_required: bool = False  # True when no users exist yet (first-run create-admin)


class OIDCCallbackResponse(BaseModel):
    """Returned after successful OIDC callback."""
    access_token: str
    token_type: str = "bearer"
    username: str
    is_new_user: bool = False  # True if auto-provisioned on this login


class OIDCDeviceStartResponse(BaseModel):
    """Returned when CLI starts device authorization flow."""
    device_code: str
    user_code: str
    verification_uri: str
    verification_uri_complete: str | None = None  # URI with code pre-filled
    expires_in: int  # Seconds until device_code expires
    interval: int  # Polling interval in seconds
    message: str | None = None  # Human-readable instruction


class OIDCDevicePollRequest(BaseModel):
    """CLI polls with device_code until user completes auth."""
    device_code: str


class OIDCDevicePollResponse(BaseModel):
    """Poll result — either pending or completed with token."""
    status: str  # "pending", "completed", "expired", "denied"
    access_token: str | None = None
    token_type: str = "bearer"
    username: str | None = None


# ─── Publishers ───

class PublishedApp(BaseModel):
    """A fine-grained access rule exposing a specific service through a publisher.

    target: IP address, CIDR, or FQDN (e.g., "10.50.1.5", "10.50.1.0/24", "db1.internal")
    port: destination port (e.g., 3389, 443, 22). Use 0 for ICMP (no port concept).
    protocol: "tcp", "udp", or "icmp" (default: "tcp")
    name: optional human-readable label (e.g., "RDP to DB server")
    """
    target: str = Field(..., min_length=1)
    port: int = Field(default=0, ge=0, le=65535)
    protocol: str = Field(default="tcp", pattern="^(tcp|udp|icmp)$")
    name: str | None = None


class PublisherCreate(BaseModel):
    name: str = Field(..., min_length=1, max_length=100)
    location: str | None = None
    description: str | None = None
    exposed_cidrs: list[str] | None = None  # ["10.50.0.0/16", "192.168.1.0/24"]
    published_apps: list[PublishedApp] | None = None
    dns_server: str | None = None
    dns_zones: list[str] | None = None


class PublisherUpdate(BaseModel):
    name: str | None = None
    location: str | None = None
    description: str | None = None
    exposed_cidrs: list[str] | None = None
    published_apps: list[PublishedApp] | None = None
    dns_server: str | None = None
    dns_zones: list[str] | None = None
    public_key: str | None = None
    tunnel_ip: str | None = None
    status: str | None = None
    exit_node: bool | None = None


class PublisherResponse(BaseModel):
    id: str
    name: str
    location: str | None
    description: str | None
    exposed_cidrs: list[str] | None
    published_apps: list[PublishedApp] | None
    publisher_index: int | None
    virtual_cidr: str | None
    dns_server: str | None
    dns_zones: list[str] | None
    public_key: str | None
    tunnel_ip: str | None
    endpoint: str | None
    status: str
    priority: int
    agent_version: str | None = None
    exit_node: bool = False
    org_id: str | None = None
    enrolled_at: datetime | None
    last_heartbeat: datetime | None
    created_at: datetime

    class Config:
        from_attributes = True


class PublisherEnrollRequest(BaseModel):
    token: str
    public_key: str
    name: str
    local_dns: str | None = None
    exposed_cidrs: list[str] | None = None


class PublisherEnrollResponse(BaseModel):
    publisher_id: str
    publisher_api_key: str  # wpk_... — shown only once, publisher must store it
    broker_public_key: str  # Deprecated — use connection-info endpoint for real namespace key
    broker_endpoint: str  # Deprecated — use connection-info endpoint for real namespace port
    tunnel_ip: str
    broker_tunnel_ip: str
    allowed_ips: str
    # The publisher should poll GET /publishers/{id}/connection-info until
    # the namespace is ready with the real broker key and port.
    connection_info_url: str = ""
    poll_for_connection: bool = True


class PublisherHeartbeat(BaseModel):
    publisher_id: str
    timestamp: str
    uptime_seconds: int
    handshake_age_seconds: int | None = None
    rx_bytes: int = 0
    tx_bytes: int = 0
    peer_count: int = 0
    status: str = Field(default="online", pattern="^(online|offline)$")
    local_dns: str | None = Field(default=None, pattern=r"^(\d{1,3}\.){3}\d{1,3}$")
    agent_version: str | None = Field(default=None, max_length=50)
    update_last_seen: bool | None = None  # When False, don't update last_heartbeat timestamp


class NamespaceInfoUpdate(BaseModel):
    """Reconciler reports namespace WG details after creation."""
    broker_ns_public_key: str
    broker_ns_port: int
    broker_tunnel_ip: str  # Publisher agent version (optional, backwards-compatible)


# ─── Enrollment Tokens ───
class EnrollmentTokenCreate(BaseModel):
    publisher_name: str | None = None
    exposed_cidrs: list[str] | None = None
    location: str | None = None
    expires_in_hours: int = Field(default=24, ge=1, le=720)  # 1h to 30 days


class EnrollmentTokenResponse(BaseModel):
    id: str
    token: str
    publisher_name: str | None
    exposed_cidrs: list[str] | None
    location: str | None
    expires_at: datetime | None
    used_by: str | None
    created_at: datetime

    class Config:
        from_attributes = True


# ─── Users ───
class UserCreate(BaseModel):
    email: str = Field(..., min_length=3, max_length=255)  # Primary login identifier (required)
    username: str | None = Field(default=None, max_length=100)  # Display name (optional — defaults to email local part)
    password: str | None = Field(default=None, min_length=6, max_length=128)
    is_admin: bool = False
    role: str = Field(default="user", pattern="^(super_admin|org_admin|user)$")
    auth_provider: str = Field(default="local", pattern="^(local|oidc|both)$")
    vpn_mode: bool = False


class UserUpdate(BaseModel):
    username: str | None = None
    email: str | None = None
    status: str | None = None
    is_admin: bool | None = None
    role: str | None = Field(default=None, pattern="^(super_admin|org_admin|user)$")
    public_key: str | None = None
    password: str | None = Field(default=None, min_length=6, max_length=128)
    auth_provider: str | None = Field(default=None, pattern="^(local|oidc|both)$")
    vpn_mode: bool | None = None
    max_enrollment_tokens: int | None = Field(default=None, ge=0, le=50)


class AdminSetPasswordRequest(BaseModel):
    """Admin set/reset a user's password without SMTP (email-less CE flow)."""
    password: str = Field(..., min_length=6, max_length=128)
    must_change_password: bool = False  # Force a change on the user's next login


class UserResponse(BaseModel):
    id: str
    username: str
    email: str
    overlay_ip: str | None
    status: str
    is_admin: bool
    role: str = "user"
    auth_provider: str = "local"
    vpn_mode: bool = False
    max_enrollment_tokens: int = 3
    org_id: str | None = None
    created_at: datetime

    class Config:
        from_attributes = True


# ─── Groups ───
class GroupCreate(BaseModel):
    name: str = Field(..., min_length=1, max_length=100)
    description: str | None = None


class GroupUpdate(BaseModel):
    name: str | None = None
    description: str | None = None


class GroupResponse(BaseModel):
    id: str
    name: str
    description: str | None
    org_id: str | None = None
    created_at: datetime

    class Config:
        from_attributes = True


class GroupMemberAdd(BaseModel):
    user_ids: list[str]


class GroupPublisherAdd(BaseModel):
    publisher_ids: list[str]


# ─── Access Policies (restrictive app rules per group↔publisher) ───

class AccessRuleCreate(BaseModel):
    """Create an access rule for a restricted group↔publisher pair."""
    target: str = Field(..., min_length=1)  # IP, CIDR, or FQDN
    port: str = Field(default="0")  # "443", "8080-8090", "0" (any)
    protocol: str = Field(default="tcp", pattern="^(tcp|udp|icmp|any)$")
    name: str | None = None  # Human-readable label (e.g., "Web App")


class AccessRuleResponse(BaseModel):
    id: str
    target: str
    port: str
    protocol: str
    name: str | None
    created_at: datetime

    class Config:
        from_attributes = True


class AccessPolicyUpdate(BaseModel):
    """Set the access policy for a group↔publisher pair."""
    access_policy: str = Field(..., pattern="^(unrestricted|restricted)$")


class AccessPolicyResponse(BaseModel):
    id: str
    group_id: str
    publisher_id: str
    access_policy: str
    rules: list[AccessRuleResponse] = []
    created_at: datetime

    class Config:
        from_attributes = True


# ─── Broker Config (what the reconciler consumes) ───
class BrokerPublisherConfig(BaseModel):
    id: str
    name: str
    public_key: str | None
    tunnel_ip: str | None
    exposed_cidrs: list[str]
    published_apps: list[dict] | None = None  # [{target, port, protocol, name}]
    dns_server: str | None = None
    dns_zones: list[str] | None = None
    publisher_index: int | None = None
    virtual_cidr: str | None = None
    endpoint: str | None = None  # Publisher's public IP:port for WG
    status: str
    priority: int = 100
    exit_node: bool = False


class RoutingRule(BaseModel):
    """A single routing/firewall rule for the reconciler, pre-ordered by specificity.

    type: "app" (IP+port or IP+protocol) or "cidr" (network prefix)
    target: IP address or CIDR (FQDNs resolved before reaching here)
    port: destination port (0 = any/not applicable, used for TCP/UDP type="app")
    protocol: "tcp", "udp", or "icmp" (for type="app")
    publisher_id: which publisher handles this traffic
    publisher_index: the fwmark/table index for routing
    prefix_len: CIDR prefix length for ordering (32 for host, 0-31 for networks)
    """
    type: str  # "app" or "cidr"
    target: str  # IP or CIDR
    port: int = 0  # 0 = any/not applicable (ICMP has no port)
    protocol: str = "tcp"  # "tcp", "udp", "icmp", or "any"
    publisher_id: str
    publisher_index: int
    prefix_len: int = 0  # For ordering: higher = more specific


class BrokerClientConfig(BaseModel):
    public_key: str
    overlay_ip: str
    allowed_publishers: list[str]  # Publisher IDs this client can access
    allowed_cidrs: list[str]  # Real exposed CIDRs (for client AllowedIPs + firewall rules)
    publisher_cidrs: dict[str, list[str]] | None = None  # publisher_id → [cidrs] mapping for policy routing
    routing_rules: list[RoutingRule] | None = None  # Pre-computed ordered rules for nftables
    preshared_key: str | None = None
    # Access restrictions per publisher (only present if restricted)
    access_restrictions: dict[str, list[dict]] | None = None  # publisher_id → [{target, port, protocol}]
    # Exit node: if set, ALL traffic from this client routes to this publisher (VPN full tunnel)
    exit_node_publisher_id: str | None = None


class BrokerConfigResponse(BaseModel):
    broker_id: str
    version: str
    publishers: list[BrokerPublisherConfig]
    clients: list[BrokerClientConfig]
    dns_hints: dict[str, list[dict]] | None = None  # overlay_ip → [{ip, publisher_index}]
    overlay_ip: str
    overlay_network: str
    wg_port: int


# ─── Sessions (ephemeral PSK) ───
class SessionRenewRequest(BaseModel):
    """Optional body sent by clients on session renew. All fields optional for backwards-compat."""
    client_version: str | None = None  # e.g. "0.3.1"
    wg_version: str | None = None  # e.g. "1.0.20210914"
    platform: str | None = None  # e.g. "darwin/arm64", "windows/amd64", "linux/amd64"


class SessionRenewResponse(BaseModel):
    session_id: str
    preshared_key: str
    expires_at: datetime
    ttl_seconds: int
    allowed_ips: list[str] = []  # Current CIDRs the client can reach (dynamic, updated each renew)
    is_exit_node: bool = False  # True when connected via an exit node (full tunnel)
    broker_endpoint: str | None = None  # Broker WG endpoint — client must exclude from tunnel routes


class SessionInfoResponse(BaseModel):
    session_id: str
    expires_at: datetime
    ttl_remaining_seconds: int
    client_ip: str | None
    selected_group_id: str | None = None
    selected_group_name: str | None = None


# ─── Client Observability ───
class ClientPeerStatusEntry(BaseModel):
    public_key: str
    endpoint_ip: str | None = None
    endpoint_port: int | None = None
    last_handshake_at: str | None = None
    is_connected: bool = False
    rx_bytes: int = 0
    tx_bytes: int = 0
    client_version: str | None = None
    wg_version: str | None = None


class ClientStatusReport(BaseModel):
    peers: list[ClientPeerStatusEntry]


# ─── Client Flows (conntrack) ───
class ClientFlowEntry(BaseModel):
    protocol: str
    dst_ip: str
    dst_port: int
    src_port: int = 0
    state: str
    bytes_in: int = 0
    bytes_out: int = 0
    packets_in: int = 0
    packets_out: int = 0


class ClientFlowsReport(BaseModel):
    flows_by_ip: dict[str, list[ClientFlowEntry]]


# ─── DNS Routing Hints ───
class DnsHintEntry(BaseModel):
    ip: str  # Resolved IP address
    publisher_index: int  # Which publisher resolved this


class DnsHintsReport(BaseModel):
    """DNS routing hints from the DNS proxy.

    When a client resolves a domain via a publisher's DNS, the proxy records
    the resolved IP → publisher_index mapping. This creates /32 nftables rules
    ensuring traffic to that IP goes through the correct publisher, resolving
    CIDR overlap for identical ranges with different DNS zones.
    """
    hints_by_client: dict[str, list[DnsHintEntry]]  # overlay_ip → hints


class ClientFlowResponse(BaseModel):
    protocol: str
    dst_ip: str
    dst_port: int
    src_port: int = 0
    state: str
    bytes_in: int = 0
    bytes_out: int = 0
    service: str | None = None  # Resolved service name (ssh, https, etc.)
    publisher_name: str | None = None  # Which publisher owns this destination
    reachable: bool | None = None  # Whether the destination is responding (based on conntrack state)


class ClientFlowsResponse(BaseModel):
    overlay_ip: str | None
    total_flows: int
    flows: list[ClientFlowResponse]
    stale: bool = False  # True if data is older than expected


class ConnectedClientResponse(BaseModel):
    username: str
    user_id: str
    overlay_ip: str | None
    endpoint_ip: str | None
    endpoint_port: int | None
    is_connected: bool
    last_handshake_at: datetime | None
    rx_bytes: int
    tx_bytes: int
    session_expires_at: datetime | None
    session_group_id: str | None = None
    session_group_name: str | None = None
    client_version: str | None = None
    wg_version: str | None = None
    platform: str | None = None  # e.g. "darwin/arm64"
    last_reported_at: datetime | None

    class Config:
        from_attributes = True


class ClientConnectionHistory(BaseModel):
    event: str
    timestamp: datetime
    detail: str | None = None
    client_ip: str | None = None


# ─── Client Connection Debug ───
class ClientDebugPublisher(BaseModel):
    publisher_id: str
    publisher_name: str
    status: str
    exposed_cidrs: list[str]
    endpoint: str | None
    last_heartbeat: datetime | None
    via_groups: list[str]  # Group names granting access
    access_policy: str = "unrestricted"  # "unrestricted" or "restricted"
    allowed_apps: list[dict] | None = None  # [{target, port, protocol, name}] if restricted


class ClientDebugSession(BaseModel):
    session_id: str
    is_active: bool
    created_at: datetime
    expires_at: datetime
    ttl_remaining_seconds: int
    client_ip: str | None
    selected_group_id: str | None = None
    selected_group_name: str | None = None
    exit_node_publisher_id: str | None = None
    exit_node_publisher_name: str | None = None
    tunnel_mode: str = "split"  # "split" or "vpn"


class ClientDebugResponse(BaseModel):
    user_id: str
    username: str
    overlay_ip: str | None
    status: str
    public_key_set: bool

    # Connection state (from broker reports)
    is_connected: bool
    endpoint_ip: str | None
    endpoint_port: int | None
    last_handshake_at: datetime | None
    handshake_age_seconds: int | None  # Seconds since last successful handshake
    rx_bytes: int
    tx_bytes: int
    last_reported_at: datetime | None

    # Client version info (reported by client on session renew — None if not reported)
    client_version: str | None = None
    wg_version: str | None = None
    platform: str | None = None  # e.g. "darwin/arm64"

    # Session state
    active_session: ClientDebugSession | None
    total_sessions: int

    # Access topology: which publishers are reachable and why
    accessible_publishers: list[ClientDebugPublisher]
    total_allowed_cidrs: list[str]

    # Diagnostics
    issues: list[str]  # Human-readable list of detected problems


# ─── Audit ───
class AccessLogEntry(BaseModel):
    id: int
    timestamp: datetime
    user_id: str | None
    resource_id: str | None
    action: str
    detail: str | None
    client_ip: str | None

    class Config:
        from_attributes = True


class AccessLogCreate(BaseModel):
    user_id: str | None = None
    resource_id: str | None = None
    publisher_id: str | None = None
    action: str
    detail: str | None = None
    client_ip: str | None = None
    duration_seconds: int | None = None
    bytes_transferred: int | None = None


# ─── Client Enrollment Tokens ───

class ClientEnrollmentTokenCreate(BaseModel):
    """Admin creates an enrollment token for a user."""
    expires_in_hours: int = Field(default=24, ge=1, le=720)  # 1h to 30 days
    note: str | None = None  # Optional note (e.g., "For Sergio's laptop")


class ClientEnrollmentTokenResponse(BaseModel):
    """Returned when admin generates a token."""
    id: str
    token: str
    user_id: str
    username: str  # Convenience: resolved from user_id
    expires_at: datetime
    used_at: datetime | None
    used_from_ip: str | None
    revoked: bool
    created_by: str | None
    created_at: datetime
    note: str | None
    enroll_url: str  # Full URL the user can pass to `wireztna enroll`
    device_name: str | None = None  # Hostname of the enrolled device
    device_platform: str | None = None  # e.g. "darwin/arm64"

    class Config:
        from_attributes = True


class ClientEnrollRequest(BaseModel):
    """User consumes enrollment token with their generated public key."""
    token: str
    public_key: str = Field(..., min_length=44, max_length=44)
    device_name: str | None = None  # e.g. "MacBook-Sergio", "DESKTOP-F4TZK"
    platform: str | None = None  # e.g. "darwin/arm64", "windows/amd64"


class ClientEnrollResponse(BaseModel):
    """Full client config returned after successful enrollment."""
    # Identity
    email: str  # Primary login identifier
    username: str  # Display name (backward compat)
    overlay_ip: str

    # Broker connection
    broker_public_key: str
    broker_endpoint: str  # "203.0.113.10:51820"
    broker_overlay_ip: str  # "10.200.0.1"

    # Tunnel
    allowed_ips: list[str]  # ["10.200.0.1/32", "10.50.0.0/16", ...]
    dns_server: str  # "10.200.0.1"

    # Metadata
    enrolled_at: datetime


# ─── Access Passes (delegated access for agents) ───

class AccessPassScope(BaseModel):
    """Defines the scope of resources an access pass grants."""
    publishers: list[str] = []         # Publisher IDs
    cidrs: list[str] = []              # Target CIDRs (e.g., "10.50.1.200/32")
    ports: list[int] = []              # Target ports
    apps: list[str] = []               # Published app names


class AccessPassCreate(BaseModel):
    """Request body for creating a new access pass."""
    label: str = Field(..., min_length=1, max_length=200)
    scope: AccessPassScope
    ttl_seconds: int = Field(..., ge=60, le=7200)
    metadata: dict | None = None


class AccessPassResponse(BaseModel):
    """Full access pass details returned on create/get."""
    pass_id: str
    label: str
    status: str
    scope_summary: str
    connection_url: str
    created_by_user_id: str
    created_at: datetime
    expires_at: datetime
    bytes_uploaded: int
    bytes_downloaded: int
    connections_count: int
    last_activity_at: datetime | None
    time_remaining_seconds: int


class AccessPassListItem(BaseModel):
    """Summary of an access pass for list endpoints."""
    pass_id: str
    label: str
    status: str
    scope_summary: str
    created_at: datetime
    expires_at: datetime
    bytes_uploaded: int
    bytes_downloaded: int
    connections_count: int
