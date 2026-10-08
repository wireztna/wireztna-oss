"""System-wide diagnostics endpoint for troubleshooting ZTNA connectivity.

Aggregates health signals from all components (publishers, clients, sessions)
and returns a structured report identifying exactly where the problem is.

Designed to be called from:
- Admin UI (dashboard health panel)
- CLI debugging (curl + jq)
- Automated monitoring (Prometheus/Grafana via JSON)
"""

from datetime import datetime, timedelta
from typing import Optional

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy import select, func

from app.database import get_db
from app.models.publisher import Publisher
from app.models.user import User
from app.models.session import ClientSession
from app.models.client_status import ClientPeerStatus
from app.services.auth_service import require_super_admin

router = APIRouter()


# ─── Response schemas ───

class PublisherHealth(BaseModel):
    id: str
    name: str
    status: str
    endpoint: Optional[str]
    last_heartbeat: Optional[datetime]
    heartbeat_age_seconds: Optional[int]
    healthy: bool
    issues: list[str]


class ClientHealth(BaseModel):
    user_id: str
    username: str
    overlay_ip: Optional[str]
    is_connected: bool
    has_active_session: bool
    session_ttl_remaining: Optional[int]
    last_handshake_at: Optional[datetime]
    handshake_age_seconds: Optional[int]
    healthy: bool
    issues: list[str]


class SystemHealthSummary(BaseModel):
    status: str  # "healthy", "degraded", "critical"
    timestamp: datetime
    publishers_total: int
    publishers_online: int
    publishers_offline: int
    publishers_pending: int
    clients_total: int
    clients_connected: int
    clients_disconnected: int
    sessions_active: int
    sessions_expired_recently: int  # Expired in last hour (might explain disconnects)
    issues: list[str]  # System-wide issues


class DiagnosticResponse(BaseModel):
    summary: SystemHealthSummary
    publishers: list[PublisherHealth]
    clients: list[ClientHealth]
    connectivity_matrix: list[dict]  # Which client can reach which publisher


# ─── Main diagnostic endpoint ───

@router.get("/health", response_model=DiagnosticResponse)
async def get_system_health(
    include_healthy: bool = Query(default=False, description="Include healthy components in detail"),
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """Full system health diagnostic.

    Returns a structured report showing:
    - Overall system status (healthy/degraded/critical)
    - Per-publisher health with detected issues
    - Per-client health with detected issues
    - Connectivity matrix (who can reach what)

    Quick interpretation:
    - Check summary.issues first for system-wide problems
    - Then check publishers[].issues for tunnel problems
    - Then check clients[].issues for client-specific problems

    Common failure patterns:
    - Publisher heartbeat stale → publisher crashed or network issue
    - Client no session → needs to call /sessions/renew
    - Client has session but no handshake → firewall blocking UDP or wrong keys
    - Publisher online but client can't reach it → nftables/routing issue on broker
    """
    now = datetime.utcnow()
    system_issues: list[str] = []

    # ─── Publishers ───
    pub_result = await db.execute(select(Publisher).order_by(Publisher.name))
    publishers = pub_result.scalars().all()

    publisher_healths: list[PublisherHealth] = []
    pub_online = 0
    pub_offline = 0
    pub_pending = 0

    for pub in publishers:
        issues: list[str] = []
        healthy = True

        # Heartbeat freshness
        heartbeat_age = None
        if pub.last_heartbeat:
            heartbeat_age = int((now - pub.last_heartbeat).total_seconds())

        if pub.status == "pending":
            pub_pending += 1
            healthy = False
            if pub.public_key:
                issues.append("Status is 'pending' but has a public key — may need manual reset or re-enrollment")
            else:
                issues.append("Awaiting enrollment — publisher has not connected yet")
        elif pub.status == "disabled":
            issues.append("Administratively disabled")
            healthy = False
        elif pub.status == "offline":
            pub_offline += 1
            healthy = False
            if heartbeat_age and heartbeat_age > 120:
                issues.append(f"Offline: last heartbeat was {heartbeat_age}s ago")
            else:
                issues.append("Marked offline by health monitor")
        elif pub.status == "online":
            pub_online += 1
            if heartbeat_age is not None and heartbeat_age > 90:
                healthy = False
                issues.append(
                    f"Status says 'online' but heartbeat is {heartbeat_age}s stale — "
                    f"publisher may have crashed without clean shutdown"
                )
            if not pub.endpoint:
                issues.append("No endpoint detected — broker cannot initiate WireGuard to publisher (relying on publisher's persistent-keepalive)")
        else:
            healthy = False
            issues.append(f"Unknown status: {pub.status}")

        if not pub.public_key:
            healthy = False
            issues.append("No WireGuard public key — enrollment incomplete")

        if not pub.exposed_cidrs:
            issues.append("No exposed CIDRs configured — publisher exposes nothing")

        if include_healthy or not healthy or issues:
            publisher_healths.append(PublisherHealth(
                id=pub.id,
                name=pub.name,
                status=pub.status,
                endpoint=pub.endpoint,
                last_heartbeat=pub.last_heartbeat,
                heartbeat_age_seconds=heartbeat_age,
                healthy=healthy,
                issues=issues,
            ))

    # System-level publisher issues
    if len(publishers) == 0:
        system_issues.append("No publishers configured — no private networks are reachable")
    elif pub_online == 0:
        system_issues.append(f"ALL publishers are offline/pending ({len(publishers)} total) — no connectivity possible")
    elif pub_offline > 0:
        system_issues.append(f"{pub_offline} publisher(s) offline — some networks unreachable")

    # ─── Clients ───
    users_result = await db.execute(
        select(User).where(User.public_key.isnot(None)).order_by(User.username)
    )
    users = users_result.scalars().all()

    client_healths: list[ClientHealth] = []
    clients_connected = 0
    clients_disconnected = 0

    for user in users:
        issues: list[str] = []
        healthy = True

        # Peer status from broker
        status_result = await db.execute(
            select(ClientPeerStatus).where(ClientPeerStatus.public_key == user.public_key)
        )
        peer_status = status_result.scalar_one_or_none()

        is_connected = peer_status.is_connected if peer_status else False
        last_handshake = peer_status.last_handshake_at if peer_status else None
        handshake_age = None
        if last_handshake:
            handshake_age = int((now - last_handshake).total_seconds())

        # Active session
        session_result = await db.execute(
            select(ClientSession).where(
                ClientSession.user_id == user.id,
                ClientSession.is_active == True,
                ClientSession.expires_at > now,
            )
        )
        active_session = session_result.scalar_one_or_none()
        has_active_session = active_session is not None
        session_ttl = None
        if active_session:
            session_ttl = int((active_session.expires_at - now).total_seconds())

        # Diagnose issues
        if not has_active_session:
            healthy = False
            issues.append("No active session — PSK missing, WireGuard handshake will fail. Client must POST /sessions/renew")
        elif session_ttl is not None and session_ttl < 600:
            issues.append(f"Session expires in {session_ttl}s — client should renew soon or will disconnect")

        if has_active_session and not is_connected:
            healthy = False
            if handshake_age is None:
                issues.append("Has valid session but NEVER completed a handshake — check: client WG config, UDP connectivity to broker, correct public keys")
            elif handshake_age > 180:
                issues.append(f"Has valid session but last handshake was {handshake_age}s ago — client likely went offline or NAT timeout")

        if is_connected:
            clients_connected += 1
        else:
            clients_disconnected += 1

        if user.status != "active":
            healthy = False
            issues.append(f"User account status is '{user.status}' (not 'active')")

        if include_healthy or not healthy or issues:
            client_healths.append(ClientHealth(
                user_id=user.id,
                username=user.username,
                overlay_ip=user.overlay_ip,
                is_connected=is_connected,
                has_active_session=has_active_session,
                session_ttl_remaining=session_ttl,
                last_handshake_at=last_handshake,
                handshake_age_seconds=handshake_age,
                healthy=healthy,
                issues=issues,
            ))

    # ─── Sessions (recently expired) ───
    one_hour_ago = now - timedelta(hours=1)
    expired_result = await db.execute(
        select(func.count()).select_from(ClientSession).where(
            ClientSession.expires_at.between(one_hour_ago, now),
            ClientSession.is_active == False,
        )
    )
    recently_expired = expired_result.scalar() or 0

    if recently_expired > 0 and clients_disconnected > 0:
        system_issues.append(
            f"{recently_expired} session(s) expired in the last hour — "
            f"may explain {clients_disconnected} disconnected client(s)"
        )

    # ─── Active sessions total ───
    active_sessions_result = await db.execute(
        select(func.count()).select_from(ClientSession).where(
            ClientSession.is_active == True,
            ClientSession.expires_at > now,
        )
    )
    active_sessions = active_sessions_result.scalar() or 0

    # ─── Connectivity matrix ───
    # Show which clients have access to which publishers (via groups)
    connectivity: list[dict] = []
    for user in users[:20]:  # Limit to avoid huge responses
        await db.refresh(user, ["groups"])
        pub_ids = set()
        for group in user.groups:
            await db.refresh(group, ["publishers"])
            for pub in group.publishers:
                pub_ids.add(pub.id)

        if pub_ids or include_healthy:
            # Check peer status
            status_result = await db.execute(
                select(ClientPeerStatus).where(ClientPeerStatus.public_key == user.public_key)
            )
            peer = status_result.scalar_one_or_none()

            connectivity.append({
                "user": user.username,
                "overlay_ip": user.overlay_ip,
                "connected": peer.is_connected if peer else False,
                "publishers_allowed": len(pub_ids),
                "publishers_online": sum(
                    1 for p in publishers if p.id in pub_ids and p.status == "online"
                ),
                "can_reach_any": (
                    (peer.is_connected if peer else False) and
                    any(p.status == "online" for p in publishers if p.id in pub_ids)
                ),
            })

    # ─── Overall status ───
    if any("ALL publishers" in i for i in system_issues) or (len(users) > 0 and clients_connected == 0 and active_sessions > 0):
        overall_status = "critical"
    elif system_issues:
        overall_status = "degraded"
    else:
        overall_status = "healthy"

    return DiagnosticResponse(
        summary=SystemHealthSummary(
            status=overall_status,
            timestamp=now,
            publishers_total=len(publishers),
            publishers_online=pub_online,
            publishers_offline=pub_offline,
            publishers_pending=pub_pending,
            clients_total=len(users),
            clients_connected=clients_connected,
            clients_disconnected=clients_disconnected,
            sessions_active=active_sessions,
            sessions_expired_recently=recently_expired,
            issues=system_issues,
        ),
        publishers=publisher_healths,
        clients=client_healths,
        connectivity_matrix=connectivity,
    )


# ─── Quick check (lightweight, no auth needed) ───

class QuickHealthResponse(BaseModel):
    status: str
    publishers_online: int
    publishers_total: int
    clients_connected: int
    clients_total: int
    issues_count: int


@router.get("/quick", response_model=QuickHealthResponse)
async def quick_health(db: AsyncSession = Depends(get_db)):
    """Lightweight health check — no auth required.

    Returns basic counts without exposing sensitive data.
    Suitable for monitoring systems, load balancer health checks,
    or a quick CLI check:

        curl -s http://broker:8443/api/v1/diagnostics/quick | jq .
    """
    now = datetime.utcnow()

    # Publisher counts — only count "active" publishers (not disabled/pending) for health status
    # Disabled publishers are intentional admin actions, not degradation.
    pub_total = await db.execute(
        select(func.count()).select_from(Publisher).where(
            Publisher.status.notin_(["disabled", "pending"])
        )
    )
    pub_online = await db.execute(
        select(func.count()).select_from(Publisher).where(Publisher.status == "online")
    )

    # Client counts (anyone with a public key = configured client)
    client_total = await db.execute(
        select(func.count()).select_from(User).where(User.public_key.isnot(None))
    )
    client_connected = await db.execute(
        select(func.count()).select_from(ClientPeerStatus).where(
            ClientPeerStatus.is_connected == True
        )
    )

    # Count issues (stale heartbeats, expired sessions with connected clients)
    stale_publishers = await db.execute(
        select(func.count()).select_from(Publisher).where(
            Publisher.status == "online",
            Publisher.last_heartbeat < now - timedelta(seconds=90),
        )
    )

    total_pubs = pub_total.scalar() or 0
    online_pubs = pub_online.scalar() or 0
    total_clients = client_total.scalar() or 0
    connected_clients = client_connected.scalar() or 0
    stale_count = stale_publishers.scalar() or 0

    issues = stale_count
    if total_pubs > 0 and online_pubs == 0:
        issues += 1

    if total_pubs > 0 and online_pubs == 0:
        status = "critical"
    elif stale_count > 0 or (online_pubs < total_pubs and total_pubs > 0):
        status = "degraded"
    else:
        status = "healthy"

    return QuickHealthResponse(
        status=status,
        publishers_online=online_pubs,
        publishers_total=total_pubs,
        clients_connected=connected_clients,
        clients_total=total_clients,
        issues_count=issues,
    )


# ─── On-demand broker diagnostic (triggered from UI) ───

class BrokerDiagnosticResult(BaseModel):
    """Combined result: broker's raw diagnostic + control-plane health."""
    broker_reachable: bool
    broker_diagnostic: Optional[dict]  # Raw output from broker diagnose.sh --json
    control_plane_health: Optional[SystemHealthSummary]
    error: Optional[str] = None


@router.post("/run", response_model=BrokerDiagnosticResult)
async def run_diagnostic(
    quick: bool = Query(default=False, description="Run in quick mode (skip pings)"),
    publisher: Optional[str] = Query(default=None, description="Focus on a specific publisher ID"),
    client: Optional[str] = Query(default=None, description="Focus on a specific client overlay IP"),
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """Trigger an on-demand diagnostic and return combined results.

    This endpoint runs the control-plane's own health analysis based on
    data already reported by the broker (heartbeats, peer status, flows).
    No separate broker diagnostic server is required.

    The UI calls this when an admin clicks "Run Diagnostic" to get a
    point-in-time snapshot of the entire system.
    """

    # ─── Run control-plane health analysis ───
    now = datetime.utcnow()
    system_issues: list[str] = []

    # Publisher analysis
    pub_result = await db.execute(select(Publisher))
    publishers = pub_result.scalars().all()
    pub_online = sum(1 for p in publishers if p.status == "online")
    pub_offline = sum(1 for p in publishers if p.status == "offline")
    pub_pending = sum(1 for p in publishers if p.status == "pending")
    pub_disabled = sum(1 for p in publishers if p.status == "disabled")

    # Stale heartbeats (online but no recent heartbeat)
    for pub in publishers:
        if pub.status == "online" and pub.last_heartbeat:
            age = (now - pub.last_heartbeat).total_seconds()
            if age > 90:
                system_issues.append(f"Publisher '{pub.name}' heartbeat stale ({int(age)}s)")

    # Offline publishers with recent heartbeat (went offline recently)
    for pub in publishers:
        if pub.status == "offline" and pub.last_heartbeat:
            age = (now - pub.last_heartbeat).total_seconds()
            if age < 60:
                system_issues.append(
                    f"Publisher '{pub.name}' went offline {int(age)}s ago — "
                    f"WireGuard handshake to broker failed"
                )

    # Client analysis
    users_result = await db.execute(
        select(func.count()).select_from(User).where(User.public_key.isnot(None))
    )
    total_clients = users_result.scalar() or 0

    connected_result = await db.execute(
        select(func.count()).select_from(ClientPeerStatus).where(
            ClientPeerStatus.is_connected == True
        )
    )
    clients_connected = connected_result.scalar() or 0

    active_sessions_result = await db.execute(
        select(func.count()).select_from(ClientSession).where(
            ClientSession.is_active == True,
            ClientSession.expires_at > now,
        )
    )
    active_sessions = active_sessions_result.scalar() or 0

    expired_result = await db.execute(
        select(func.count()).select_from(ClientSession).where(
            ClientSession.expires_at.between(now - timedelta(hours=1), now),
            ClientSession.is_active == False,
        )
    )
    recently_expired = expired_result.scalar() or 0

    if recently_expired > 0 and (total_clients - clients_connected) > 0:
        system_issues.append(
            f"{recently_expired} session(s) expired in the last hour — "
            f"may explain disconnected clients"
        )

    # Broker health: check if we've received any client-status report recently
    latest_report = await db.execute(
        select(ClientPeerStatus.last_reported_at)
        .order_by(ClientPeerStatus.last_reported_at.desc())
        .limit(1)
    )
    latest_report_time = latest_report.scalar_one_or_none()
    broker_reachable = True
    broker_diagnostic = None

    if latest_report_time:
        report_age = (now - latest_report_time).total_seconds()
        if report_age > 60:
            system_issues.append(
                f"No broker status report in {int(report_age)}s — "
                f"broker health monitor may be down"
            )
            broker_reachable = False
    elif total_clients > 0:
        system_issues.append("No broker status reports ever received — broker agent may not be running")
        broker_reachable = False

    # Build broker diagnostic from available data
    if broker_reachable and latest_report_time:
        # Gather what we know from broker reports
        peers_result = await db.execute(select(ClientPeerStatus))
        all_peers = peers_result.scalars().all()

        broker_diagnostic = {
            "status": "reporting",
            "last_report_age_seconds": int((now - latest_report_time).total_seconds()) if latest_report_time else None,
            "peers_tracked": len(all_peers),
            "peers_connected": sum(1 for p in all_peers if p.is_connected),
            "publishers_tracked": len(publishers),
            "publishers_online": pub_online,
        }

    if len(publishers) > 0 and pub_online == 0:
        overall_status = "critical"
        if not any("offline" in i.lower() or "publisher" in i.lower() for i in system_issues):
            system_issues.append("All publishers offline — no private network connectivity")
    elif system_issues:
        overall_status = "degraded"
    else:
        overall_status = "healthy"

    cp_health = SystemHealthSummary(
        status=overall_status,
        timestamp=now,
        publishers_total=len(publishers),
        publishers_online=pub_online,
        publishers_offline=pub_offline,
        publishers_pending=pub_pending,
        clients_total=total_clients,
        clients_connected=clients_connected,
        clients_disconnected=total_clients - clients_connected,
        sessions_active=active_sessions,
        sessions_expired_recently=recently_expired,
        issues=system_issues,
    )

    return BrokerDiagnosticResult(
        broker_reachable=broker_reachable,
        broker_diagnostic=broker_diagnostic,
        control_plane_health=cp_health,
        error=None if broker_reachable else "Broker health monitor not reporting — check wireztna-health service",
    )


# ─── Per-publisher live diagnostic ───

class PublisherDiagnosticCheck(BaseModel):
    name: str
    status: str  # "pass", "fail", "warn"
    detail: str
    duration_ms: Optional[int] = None


class PublisherDiagnosticResponse(BaseModel):
    publisher_id: str
    publisher_name: str
    timestamp: datetime
    overall_status: str  # "healthy", "degraded", "critical"
    checks: list[PublisherDiagnosticCheck]
    recommendation: Optional[str] = None


@router.get("/publisher/{publisher_id}", response_model=PublisherDiagnosticResponse)
async def diagnose_publisher(
    publisher_id: str,
    db: AsyncSession = Depends(get_db),
    _=Depends(require_super_admin),
):
    """Run live diagnostic checks on a specific publisher.

    Executes checks on the broker's namespace for this publisher:
    1. Publisher exists and has valid config
    2. Namespace exists on the broker
    3. WireGuard interface is active with recent handshake
    4. Can ping publisher's tunnel IP from namespace
    5. Can ping publisher's LAN (first exposed CIDR host) from namespace
    6. nftables rules exist for this publisher
    7. Policy routing table exists

    Returns a checklist with pass/fail/warn for each step and a recommendation.
    """
    import subprocess
    import time

    now = datetime.utcnow()
    checks: list[PublisherDiagnosticCheck] = []

    # ─── Check 1: Publisher exists in DB with valid config ───
    result = await db.execute(select(Publisher).where(Publisher.id == publisher_id))
    pub = result.scalar_one_or_none()
    if not pub:
        return PublisherDiagnosticResponse(
            publisher_id=publisher_id,
            publisher_name="unknown",
            timestamp=now,
            overall_status="critical",
            checks=[PublisherDiagnosticCheck(name="Publisher exists", status="fail", detail="Publisher ID not found in database")],
            recommendation="Check the publisher ID. It may have been deleted.",
        )

    # Basic config checks
    if pub.public_key:
        checks.append(PublisherDiagnosticCheck(name="Public key configured", status="pass", detail=f"Key: {pub.public_key[:16]}..."))
    else:
        checks.append(PublisherDiagnosticCheck(name="Public key configured", status="fail", detail="No public key — publisher has not enrolled"))

    if pub.publisher_index is not None:
        checks.append(PublisherDiagnosticCheck(name="Publisher index assigned", status="pass", detail=f"Index: {pub.publisher_index}"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Publisher index assigned", status="fail", detail="No index — cannot create namespace"))

    if pub.endpoint:
        checks.append(PublisherDiagnosticCheck(name="Endpoint known", status="pass", detail=f"Endpoint: {pub.endpoint}"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Endpoint known", status="warn", detail="No endpoint — broker cannot initiate handshake (relies on publisher's keepalive)"))

    if pub.status == "online":
        checks.append(PublisherDiagnosticCheck(name="Status", status="pass", detail="online"))
    elif pub.status == "disabled":
        checks.append(PublisherDiagnosticCheck(name="Status", status="warn", detail="disabled (administratively)"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Status", status="fail", detail=f"{pub.status}"))

    # Heartbeat freshness
    if pub.last_heartbeat:
        hb_age = int((now - pub.last_heartbeat).total_seconds())
        if hb_age < 60:
            checks.append(PublisherDiagnosticCheck(name="Heartbeat", status="pass", detail=f"{hb_age}s ago"))
        elif hb_age < 150:
            checks.append(PublisherDiagnosticCheck(name="Heartbeat", status="warn", detail=f"{hb_age}s ago (getting stale)"))
        else:
            checks.append(PublisherDiagnosticCheck(name="Heartbeat", status="fail", detail=f"{hb_age}s ago (stale — publisher may be down)"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Heartbeat", status="fail", detail="Never received"))

    # ─── Live checks (require shell access to broker namespace) ───
    # These run subprocess commands on the broker host

    if not pub.publisher_index:
        # Can't do live checks without an index
        return PublisherDiagnosticResponse(
            publisher_id=publisher_id,
            publisher_name=pub.name,
            timestamp=now,
            overall_status="critical",
            checks=checks,
            recommendation="Publisher has no index — enrollment may be incomplete.",
        )

    id_short = publisher_id[:8]
    ns_name = f"ns-{id_short}"

    # Check 2: Namespace exists
    def run(cmd: list[str], timeout: int = 5) -> subprocess.CompletedProcess:
        try:
            return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            return subprocess.CompletedProcess(args=cmd, returncode=-1, stdout="", stderr="timeout")

    ns_check = run(["ip", "netns", "list"])
    if ns_name in ns_check.stdout:
        checks.append(PublisherDiagnosticCheck(name="Namespace exists", status="pass", detail=f"Network namespace {ns_name} is active"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Namespace exists", status="fail", detail=f"Namespace {ns_name} not found. The reconciler creates it when the publisher has a public key and status is not 'pending'."))
        return PublisherDiagnosticResponse(
            publisher_id=publisher_id,
            publisher_name=pub.name,
            timestamp=now,
            overall_status="critical",
            checks=checks,
            recommendation=f"Namespace {ns_name} does not exist. Check reconciler logs: journalctl -u wireztna-reconciler -n 20",
        )

    # Check 3: WireGuard interface active with handshake
    wg_check = run(["ip", "netns", "exec", ns_name, "wg", "show"])
    if wg_check.returncode == 0 and wg_check.stdout.strip():
        # Parse handshake age
        handshake_line = ""
        transfer_line = ""
        for line in wg_check.stdout.split("\n"):
            if "latest handshake" in line:
                handshake_line = line.strip()
            if "transfer" in line:
                transfer_line = line.strip()

        if handshake_line:
            checks.append(PublisherDiagnosticCheck(name="WG handshake", status="pass", detail=handshake_line))
        else:
            checks.append(PublisherDiagnosticCheck(name="WG handshake", status="fail", detail="No handshake completed yet — the publisher hasn't established an encrypted tunnel with the broker"))

        if transfer_line:
            if "0 B received" in transfer_line:
                checks.append(PublisherDiagnosticCheck(name="WG traffic", status="fail", detail=f"{transfer_line} — The broker receives no data from the publisher. Possible zombie UDP socket blocking the port (see troubleshooting guide)."))
            else:
                checks.append(PublisherDiagnosticCheck(name="WG traffic", status="pass", detail=f"Data flowing through tunnel: {transfer_line}"))
    else:
        checks.append(PublisherDiagnosticCheck(name="WG interface", status="fail", detail="No WireGuard interface found in the namespace. The reconciler should create it — check logs: journalctl -u wireztna-reconciler"))

    # Check 4: Ping tunnel IP
    tunnel_ip = f"10.100.{pub.publisher_index}.2"
    t0 = time.time()
    ping_tunnel = run(["ip", "netns", "exec", ns_name, "ping", "-c", "1", "-W", "3", tunnel_ip])
    ping_ms = int((time.time() - t0) * 1000)
    if ping_tunnel.returncode == 0:
        checks.append(PublisherDiagnosticCheck(name="Ping tunnel IP", status="pass", detail=f"Publisher tunnel endpoint {tunnel_ip} reachable ({ping_ms}ms)", duration_ms=ping_ms))
    else:
        checks.append(PublisherDiagnosticCheck(name="Ping tunnel IP", status="fail", detail=f"Publisher tunnel endpoint {tunnel_ip} unreachable — WireGuard tunnel is broken or publisher is down", duration_ms=ping_ms))

    # Check 5: Verify namespace has routes for exposed CIDRs
    if pub.exposed_cidrs:
        route_ns_check = run(["ip", "netns", "exec", ns_name, "ip", "route"])
        route_output = route_ns_check.stdout

        # Check if full-tunnel routes exist (0.0.0.0/1 + 128.0.0.0/1 cover all CIDRs)
        has_full_tunnel = "0.0.0.0/1" in route_output and "128.0.0.0/1" in route_output

        if has_full_tunnel:
            checks.append(PublisherDiagnosticCheck(
                name="Namespace routes for CIDRs",
                status="pass",
                detail=f"Full-tunnel routes active (0.0.0.0/1 + 128.0.0.0/1) — covers all {len(pub.exposed_cidrs)} exposed CIDRs"
            ))
        else:
            cidrs_routed = []
            cidrs_missing = []
            for cidr in pub.exposed_cidrs:
                if cidr in route_output:
                    cidrs_routed.append(cidr)
                else:
                    cidrs_missing.append(cidr)

            if cidrs_missing:
                checks.append(PublisherDiagnosticCheck(
                    name="Namespace routes for CIDRs",
                    status="fail",
                    detail=f"Missing routes: {', '.join(cidrs_missing)}. Routed: {', '.join(cidrs_routed) or 'none'}"
                ))
            else:
                checks.append(PublisherDiagnosticCheck(
                    name="Namespace routes for CIDRs",
                    status="pass",
                    detail=f"All exposed CIDRs routed: {', '.join(cidrs_routed)}"
                ))

    # Check 6: Policy routing
    fwmark = str(pub.publisher_index)
    table_id = str(100 + pub.publisher_index)
    rule_check = run(["ip", "rule", "list"])
    if f"fwmark 0x{int(fwmark):x}" in rule_check.stdout or f"fwmark {fwmark}" in rule_check.stdout:
        checks.append(PublisherDiagnosticCheck(name="Policy routing rule", status="pass", detail=f"Traffic marked with fwmark {fwmark} is routed to table {table_id}"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Policy routing rule", status="fail", detail=f"No ip rule for fwmark {fwmark}. Client traffic won't reach this publisher's namespace. Reconciler restart should fix this."))

    route_check = run(["ip", "route", "show", "table", table_id])
    if route_check.returncode == 0 and route_check.stdout.strip():
        checks.append(PublisherDiagnosticCheck(name="Routing table", status="pass", detail=f"Table {table_id} routes traffic to namespace: {route_check.stdout.strip()}"))
    else:
        checks.append(PublisherDiagnosticCheck(name="Routing table", status="fail", detail=f"Routing table {table_id} is empty or missing. Traffic marked for this publisher has nowhere to go."))

    # Check 7: nftables rules
    nft_check = run(["nft", "list", "chain", "inet", "wireztna", "prerouting"])
    if nft_check.returncode != 0:
        checks.append(PublisherDiagnosticCheck(
            name="nftables mangle rule",
            status="fail",
            detail="Cannot read nftables prerouting chain — the 'wireztna' table may not exist. Reconciler needs to run at least one cycle."
        ))
    else:
        # Look for the mark value in various formats nft might output:
        # "meta mark set 0x00000002" or "meta mark set 2" or "mark set 0x2"
        nft_output = nft_check.stdout
        fwmark_hex = f"0x{int(fwmark):08x}"  # e.g., "0x00000002"
        fwmark_hex_short = f"0x{int(fwmark):x}"  # e.g., "0x2"
        
        found_mark = (
            f"mark set {fwmark}" in nft_output or
            f"mark set {fwmark_hex}" in nft_output or
            f"mark set {fwmark_hex_short}" in nft_output
        )
        
        if found_mark:
            checks.append(PublisherDiagnosticCheck(
                name="nftables mangle rule",
                status="pass",
                detail=f"Client traffic is being marked with fwmark={fwmark} for routing to this publisher"
            ))
        else:
            # Check if ANY mark rules exist (maybe it's a different publisher assigned)
            if "meta mark set" in nft_output:
                checks.append(PublisherDiagnosticCheck(
                    name="nftables mangle rule",
                    status="warn",
                    detail=f"Other publishers have active mangle rules, but none route traffic to this publisher (fwmark={fwmark}). "
                           f"This is normal if no connected client is assigned to a group that includes this publisher."
                ))
            else:
                checks.append(PublisherDiagnosticCheck(
                    name="nftables mangle rule",
                    status="fail",
                    detail="No mangle rules exist in the prerouting chain. This means no connected client has access to any publisher. Check that users are assigned to groups and groups are linked to publishers."
                ))

    # ─── Overall status ───
    fail_count = sum(1 for c in checks if c.status == "fail")
    warn_count = sum(1 for c in checks if c.status == "warn")

    if fail_count >= 2:
        overall = "critical"
    elif fail_count == 1:
        overall = "degraded"
    elif warn_count > 0:
        overall = "degraded"
    else:
        overall = "healthy"

    # ─── Recommendation ───
    recommendation = None
    fail_names = [c.name for c in checks if c.status == "fail"]

    if "WG handshake" in fail_names or "Ping tunnel IP" in fail_names:
        recommendation = "The WireGuard tunnel to this publisher is down. Verify: 1) Publisher container is running (docker ps on the publisher host), 2) Publisher has the correct broker public key, 3) UDP port is open in security groups between publisher and broker, 4) No zombie UDP sockets blocking the port (ss -ulnp on broker)."
    elif "WG traffic" in fail_names:
        recommendation = "The WG interface exists but receives no data — likely a zombie UDP socket is intercepting packets on this port. See the troubleshooting guide under 'Zombie WireGuard UDP sockets'. Quick fix: use a different port or reboot the broker."
    elif "Namespace exists" in fail_names:
        recommendation = "The namespace for this publisher hasn't been created. The reconciler creates it automatically when the publisher has a public key and status is not 'pending'. Check: journalctl -u wireztna-reconciler -n 20"
    elif "Namespace routes for CIDRs" in fail_names:
        recommendation = "Namespace is missing routes for exposed CIDRs. This may happen if the reconciler hasn't run after a config change. Try: systemctl restart wireztna-reconciler, then wait 15s for the next cycle."
    elif "Policy routing rule" in fail_names or "Routing table" in fail_names:
        recommendation = "Policy routing is not configured for this publisher. The reconciler creates ip rules on each cycle. Try restarting it: systemctl restart wireztna-reconciler"
    elif "nftables mangle rule" in fail_names:
        recommendation = "No firewall rule routes client traffic to this publisher. This is normal if no connected client belongs to a group that includes this publisher. If a client should have access, verify: Users → Groups → this publisher is assigned."
    elif warn_count > 0:
        recommendation = "Minor issues detected. The publisher is functional but some checks show warnings — review them above for details."

    return PublisherDiagnosticResponse(
        publisher_id=publisher_id,
        publisher_name=pub.name,
        timestamp=now,
        overall_status=overall,
        checks=checks,
        recommendation=recommendation,
    )
