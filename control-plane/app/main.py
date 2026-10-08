"""WireZTNA Control Plane — FastAPI Application"""

import asyncio
import logging
from datetime import datetime

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from sqlalchemy import select

from app.config import settings
from app.database import engine, Base, async_session
from app.models.access_pass import AccessPass
from app.models.audit import AccessLog as AccessLogModel
from app.routers import auth, publishers, users, groups, clients, brokers, audit, sessions, diagnostics, me, downloads, debug, oidc_auth, announcements, mfa, organizations, access_passes, tunnel
from app.models.revoked_token import RevokedToken  # Ensure table is created on startup

app = FastAPI(
    title="WireZTNA Control Plane",
    description="Zero Trust Network Access — User → Group → Publisher",
    version="1.0.6",
    redirect_slashes=False,  # Prevent 307 TLS downgrade (pentest VULN-7)
)

# CORS — restrict to known origins
allowed_origins = [
    "http://localhost:3000",
    "http://127.0.0.1:3000",
]

# Add broker endpoint origins (domain or IP)
if settings.broker_public_endpoint:
    ep = settings.broker_public_endpoint
    allowed_origins.extend([
        f"http://{ep}:3000",
        f"http://{ep}:8443",
        f"https://{ep}",
        f"http://{ep}",
    ])
    # If endpoint is wg-xxx.domain.com, also allow the UI domain (without wg- prefix)
    if ep.startswith('wg-') and '.' in ep:
        ui_domain = ep[3:]  # Remove "wg-" prefix
        allowed_origins.extend([
            f"https://{ui_domain}",
            f"http://{ui_domain}",
        ])

# Add UI public domain if configured (primary user-facing domain)
if settings.ui_public_domain:
    allowed_origins.extend([
        f"https://{settings.ui_public_domain}",
        f"http://{settings.ui_public_domain}",
    ])

# Extra origins from configuration (comma-separated), if you front the UI elsewhere
if settings.cors_extra_origins:
    allowed_origins.extend(
        o.strip() for o in settings.cors_extra_origins.split(",") if o.strip()
    )

# Filter out empty/malformed entries
allowed_origins = list(set(o for o in allowed_origins if "://" in o and "://:" not in o))

app.add_middleware(
    CORSMiddleware,
    allow_origins=allowed_origins,
    allow_credentials=True,
    allow_methods=["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"],
    allow_headers=["Authorization", "Content-Type", "X-API-Key", "X-Requested-With"],
)

# Include routers
app.include_router(auth.router, prefix="/api/v1/auth", tags=["Authentication"])
app.include_router(oidc_auth.router, prefix="/api/v1/auth", tags=["Authentication (OIDC)"])
app.include_router(publishers.router, prefix="/api/v1/publishers", tags=["Publishers"])
app.include_router(users.router, prefix="/api/v1/users", tags=["Users"])
app.include_router(groups.router, prefix="/api/v1/groups", tags=["Groups"])
app.include_router(clients.router, prefix="/api/v1/clients", tags=["Clients"])
app.include_router(brokers.router, prefix="/api/v1/brokers", tags=["Brokers"])
app.include_router(audit.router, prefix="/api/v1/audit", tags=["Audit"])
app.include_router(sessions.router, prefix="/api/v1/sessions", tags=["Sessions"])
app.include_router(diagnostics.router, prefix="/api/v1/diagnostics", tags=["Diagnostics"])
app.include_router(me.router, prefix="/api/v1/me", tags=["User Portal"])
app.include_router(downloads.router, prefix="/api/v1/downloads", tags=["Downloads"])
app.include_router(debug.router, prefix="/api/v1/debug", tags=["Debug"])
app.include_router(announcements.router, prefix="/api/v1/announcements", tags=["Announcements"])
app.include_router(mfa.router, prefix="/api/v1", tags=["MFA"])
app.include_router(organizations.router, prefix="/api/v1/organizations", tags=["Organizations"])
app.include_router(access_passes.router, prefix="/api/v1/access-passes", tags=["Access Passes"])
app.include_router(tunnel.router, tags=["Tunnel"])

# Register WebSocket route directly on app (some FastAPI versions don't propagate
# WebSocket routes from included routers correctly)
app.add_api_websocket_route("/api/v1/tunnel/{pass_id}", tunnel.tunnel_endpoint)


# Security headers middleware (pentest VULN-3 — HSTS, X-Content-Type-Options, X-Frame-Options)
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request as StarletteRequest


class SecurityHeadersMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request: StarletteRequest, call_next):
        response = await call_next(request)
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["X-Frame-Options"] = "DENY"
        response.headers["Strict-Transport-Security"] = "max-age=31536000; includeSubDomains"
        response.headers["Referrer-Policy"] = "strict-origin-when-cross-origin"
        response.headers["X-XSS-Protection"] = "1; mode=block"
        return response


app.add_middleware(SecurityHeadersMiddleware)


@app.on_event("startup")
async def startup():
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)

    # Auto-migration: ensure org_id columns exist and default org is created
    await _ensure_organizations()

    # Auto-migration: ensure freetier columns exist (last_activity, canonical_email)
    await _ensure_freetier_columns()

    # Auto-migration: ensure publisher api_key_hash column exists (CVE fix)
    await _ensure_publisher_api_key_column()

    # Load revoked tokens cache for JWT revocation (A.8.5 / CC6.1)
    from app.services.auth_service import load_revoked_tokens_cache
    await load_revoked_tokens_cache()

    # Auto-migration: ensure compliance columns exist (MFA backup codes, org mfa_required)
    await _ensure_compliance_columns()

    # Start background task: expire stale access passes
    asyncio.create_task(_expire_access_passes_loop())

    # Start background task: cleanup inactive free tier users
    asyncio.create_task(_freetier_cleanup_loop())


@app.get("/health")
async def health_check():
    """Comprehensive health check for uptime monitoring (BetterStack, UptimeRobot, etc.).

    Validates:
    - API process is responding (implicit — if you get a response, it's alive)
    - Database is reachable and queryable
    - WireGuard overlay interface exists and is up
    - Critical systemd services are running (reconciler, health monitor, DNS proxy)
    - Publisher connectivity (at least one publisher online if any exist)

    Returns HTTP 200 if healthy, HTTP 503 if degraded/critical.
    No authentication required — designed for external monitoring probes.
    """
    import subprocess
    from pathlib import Path
    from app.database import async_session
    from sqlalchemy import text

    checks = {}
    issues = []

    # ─── 1. Database check ───
    try:
        async with async_session() as db:
            await db.execute(text("SELECT 1"))
            checks["database"] = {"status": "ok"}
    except Exception as e:
        checks["database"] = {"status": "error"}
        issues.append("Database unreachable or corrupted")

    # ─── 2. WireGuard overlay interface ───
    try:
        result = subprocess.run(
            ["ip", "link", "show", "wg-clients"],
            capture_output=True, text=True, timeout=5
        )
        if result.returncode == 0 and "UP" in result.stdout.upper():
            checks["wireguard"] = {"status": "ok"}
        else:
            checks["wireguard"] = {"status": "error"}
            issues.append("WireGuard overlay interface (wg-clients) is down")
    except FileNotFoundError:
        checks["wireguard"] = {"status": "skip"}
    except Exception as e:
        checks["wireguard"] = {"status": "error"}
        issues.append("Cannot check WireGuard interface")

    # ─── 3. Systemd services ───
    critical_services = ["wireztna-reconciler", "wireztna-health", "wireztna-dns"]
    services_status = {}
    try:
        for svc in critical_services:
            result = subprocess.run(
                ["systemctl", "is-active", svc],
                capture_output=True, text=True, timeout=5
            )
            svc_status = result.stdout.strip()
            services_status[svc] = "ok" if svc_status == "active" else "error"
            if svc_status != "active":
                issues.append(f"Service {svc} is inactive")
        checks["services"] = {"status": "ok" if all(v == "ok" for v in services_status.values()) else "degraded", "detail": services_status}
    except FileNotFoundError:
        checks["services"] = {"status": "skip"}
    except Exception as e:
        checks["services"] = {"status": "error"}

    # ─── 4. Publishers (at least one online, if any configured) ───
    try:
        async with async_session() as db:
            total_result = await db.execute(text(
                "SELECT COUNT(*) FROM publishers WHERE status != 'pending'"
            ))
            total_pubs = total_result.scalar() or 0

            online_result = await db.execute(text(
                "SELECT COUNT(*) FROM publishers WHERE status = 'online'"
            ))
            online_pubs = online_result.scalar() or 0

            if total_pubs == 0 or online_pubs > 0:
                checks["publishers"] = {"status": "ok"}
            else:
                checks["publishers"] = {"status": "error"}
                issues.append("All publishers are offline")
    except Exception as e:
        checks["publishers"] = {"status": "error"}

    # ─── 5. Disk space (database partition) ───
    try:
        db_path = Path("/opt/wireztna/data/wireztna.db")
        if db_path.exists():
            import shutil
            usage = shutil.disk_usage(db_path.parent)
            usage_pct = (usage.used / usage.total) * 100
            if usage_pct < 90:
                checks["disk"] = {"status": "ok"}
            else:
                checks["disk"] = {"status": "warning"}
                issues.append("Disk usage critical")
        else:
            checks["disk"] = {"status": "skip"}
    except Exception as e:
        checks["disk"] = {"status": "error"}

    # ─── Result ───
    overall = "healthy" if not issues else ("degraded" if len(issues) <= 2 else "critical")

    from fastapi.responses import JSONResponse

    response = {
        "status": overall,
        "checks": checks,
        "issues": issues,
    }

    status_code = 200 if overall == "healthy" else 503
    return JSONResponse(content=response, status_code=status_code)


async def _expire_access_passes_loop():
    """Background task: expire stale access passes every 60 seconds.

    Queries for passes where expires_at < now() AND status = 'active',
    transitions them to 'expired', closes any tracked WebSocket connections,
    and writes audit log entries.

    Requirements: 6.1, 6.2, 6.3, 7.3
    """
    logger = logging.getLogger(__name__)
    from app.routers.access_passes import _active_tunnels

    while True:
        try:
            await asyncio.sleep(15)  # Check every 15 seconds

            async with async_session() as db:
                now = datetime.utcnow()
                result = await db.execute(
                    select(AccessPass).where(
                        AccessPass.status == "active",
                        AccessPass.expires_at < now,
                    )
                )
                expired_passes = list(result.scalars().all())

                for ap in expired_passes:
                    ap.status = "expired"

                    # Close any active WebSocket connections
                    active_connections = _active_tunnels.get(ap.id, [])
                    for ws in active_connections:
                        try:
                            await ws.close(code=4001, reason="Pass expired")
                        except Exception:
                            pass
                    _active_tunnels.pop(ap.id, None)

                    # Write audit log
                    bytes_total = (ap.bytes_uploaded or 0) + (ap.bytes_downloaded or 0)
                    db.add(AccessLogModel(
                        user_id=ap.created_by_user_id,
                        resource_id=ap.id,
                        action="access_pass_expired",
                        detail=f"Pass '{ap.label}' expired — {bytes_total} bytes transferred, {ap.connections_count or 0} connections",
                    ))

                if expired_passes:
                    await db.commit()
                    logger.info(f"Expired {len(expired_passes)} access passes")

        except Exception as e:
            logger.error(f"Access pass expiration task error: {e}")
            # Don't crash — retry next cycle


async def _freetier_cleanup_loop():
    """Background task: deactivate inactive free tier users.

    Runs every N hours (configured by freetier_cleanup_interval_hours).
    Deactivates free tier users who haven't been active for 90+ days.
    """
    from app.services.freetier_cleanup import cleanup_inactive_freetier_users

    # Initial delay — wait 60s after startup before first check
    await asyncio.sleep(60)

    interval_seconds = settings.freetier_cleanup_interval_hours * 3600

    while True:
        try:
            if settings.freetier_cleanup_enabled:
                async with async_session() as db:
                    count = await cleanup_inactive_freetier_users(db)
                    if count:
                        logger.info(f"Free tier cleanup: deactivated {count} inactive users")
        except Exception as e:
            logger.error(f"Free tier cleanup task error: {e}")
            # Don't crash — retry next cycle

        await asyncio.sleep(interval_seconds)


async def _ensure_organizations():
    """Auto-migration: create default organization and assign existing resources.

    This runs on every startup and is idempotent. It handles:
    1. Adding org_id column to users/groups/publishers (if missing — SQLite ALTER TABLE)
    2. Creating a 'Default' organization if none exist
    3. Assigning all resources with NULL org_id to the default org
    """
    from sqlalchemy import text
    from app.database import async_session
    from app.models.organization import Organization

    async with async_session() as db:
        # Add org_id columns if they don't exist (SQLite-safe)
        for table in ["users", "groups", "publishers"]:
            try:
                await db.execute(text(f"SELECT org_id FROM {table} LIMIT 1"))
            except Exception:
                await db.execute(text(f"ALTER TABLE {table} ADD COLUMN org_id VARCHAR(36)"))
                await db.commit()

        # Create default org if no orgs exist
        result = await db.execute(text("SELECT id FROM organizations LIMIT 1"))
        default_org = result.first()

        if not default_org:
            import uuid
            default_id = str(uuid.uuid4())
            await db.execute(text(
                "INSERT INTO organizations (id, name, slug, description, created_at) "
                "VALUES (:id, :name, :slug, :desc, datetime('now'))"
            ), {"id": default_id, "name": "Default", "slug": "default", "desc": "Default organization"})
            await db.commit()
        else:
            default_id = default_org[0]

        # Assign all resources with NULL org_id to the default org
        for table in ["users", "groups", "publishers"]:
            await db.execute(text(
                f"UPDATE {table} SET org_id = :org_id WHERE org_id IS NULL"
            ), {"org_id": default_id})
        await db.commit()


async def _ensure_freetier_columns():
    """Auto-migration: add last_activity and canonical_email columns if missing.

    Runs on every startup, idempotent. Also backfills last_activity for existing
    free tier users (set to created_at) so they aren't immediately flagged.
    """
    from sqlalchemy import text

    async with async_session() as db:
        # Add last_activity column if not present
        try:
            await db.execute(text("SELECT last_activity FROM users LIMIT 1"))
        except Exception:
            await db.execute(text("ALTER TABLE users ADD COLUMN last_activity DATETIME"))
            await db.commit()
            # Backfill: existing free tier users get created_at as last_activity
            await db.execute(text(
                "UPDATE users SET last_activity = created_at WHERE plan = 'free' AND last_activity IS NULL"
            ))
            await db.commit()

        # Add canonical_email column if not present
        try:
            await db.execute(text("SELECT canonical_email FROM users LIMIT 1"))
        except Exception:
            await db.execute(text("ALTER TABLE users ADD COLUMN canonical_email VARCHAR(255)"))
            await db.commit()


async def _ensure_publisher_api_key_column():
    """Auto-migration: add api_key_hash column to publishers table if missing.

    Part of the CVE fix for unauthenticated publisher heartbeat.
    Runs on every startup, idempotent.
    """
    from sqlalchemy import text

    async with async_session() as db:
        try:
            await db.execute(text("SELECT api_key_hash FROM publishers LIMIT 1"))
        except Exception:
            await db.execute(text("ALTER TABLE publishers ADD COLUMN api_key_hash VARCHAR(64)"))
            await db.commit()


async def _ensure_compliance_columns():
    """Auto-migration: add compliance-related columns if missing.

    Sprint 2 compliance: MFA backup codes on users, mfa_required on organizations,
    audit_retention_days config. Runs on every startup, idempotent.
    """
    from sqlalchemy import text

    async with async_session() as db:
        # Add mfa_backup_codes to users (JSON string of hashed backup codes)
        try:
            await db.execute(text("SELECT mfa_backup_codes FROM users LIMIT 1"))
        except Exception:
            await db.execute(text("ALTER TABLE users ADD COLUMN mfa_backup_codes TEXT"))
            await db.commit()

        # Add mfa_required to organizations
        try:
            await db.execute(text("SELECT mfa_required FROM organizations LIMIT 1"))
        except Exception:
            await db.execute(text("ALTER TABLE organizations ADD COLUMN mfa_required BOOLEAN DEFAULT 0"))
            await db.commit()
