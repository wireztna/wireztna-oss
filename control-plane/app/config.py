"""Application configuration via environment variables."""

from pathlib import Path

from pydantic_settings import BaseSettings

# Default DB path: relative ./data/wireztna.db for dev, override via DATABASE_URL env for production
_default_db_path = Path(__file__).resolve().parent.parent / "data" / "wireztna.db"
_default_db_url = f"sqlite+aiosqlite:///{_default_db_path}"


class Settings(BaseSettings):
    # Database (use 4 slashes for absolute paths: sqlite+aiosqlite:////absolute/path/db)
    database_url: str = _default_db_url

    # Security
    secret_key: str = "change-me-to-a-random-string"
    jwt_algorithm: str = "HS256"
    jwt_expiry_hours: int = 168  # 7 days

    # OIDC
    oidc_enabled: bool = False
    oidc_provider_url: str = ""  # e.g., "https://login.microsoftonline.com/{tenant_id}/v2.0"
    oidc_client_id: str = ""
    oidc_client_secret: str = ""
    oidc_redirect_uri: str = ""  # e.g., "https://broker.example.com/api/v1/auth/oidc/callback"
    oidc_tenant_id: str = ""  # Microsoft Entra tenant ID
    oidc_auto_provision: bool = False  # If True, create users on first SSO login (restricted by allowed groups)
    oidc_allowed_groups: list[str] = []  # Entra group Object IDs that permit auto-provisioning
    oidc_default_group: str = ""  # WireZTNA group ID to assign auto-provisioned users to
    oidc_scopes: str = "openid email profile"  # Space-separated OIDC scopes

    # Session (ephemeral PSK)
    session_ttl_hours: int = 8  # How long a PSK session is valid
    max_active_sessions: int = 1  # Max concurrent sessions per user

    # Broker defaults
    broker_public_endpoint: str = ""  # Public IP/hostname for WG connections (e.g., "broker.example.com")
    # External scheme for server-generated HTTP/WS URLs. CE default is http (no TLS).
    broker_public_scheme: str = "http"  # "http" -> http:// & ws://; "https" -> https:// & wss://
    # Optional explicit base URL override; if set, takes precedence over scheme+endpoint.
    public_base_url: str = ""  # e.g., "http://203.0.113.10"
    # Extra CORS origins (comma-separated), appended to the derived allow-list.
    cors_extra_origins: str = ""
    broker_public_key: str = ""  # WireGuard public key of the broker
    broker_api_key: str = ""  # API key for internal broker→API communication
    broker_overlay_network: str = "10.200.0.0/16"
    broker_overlay_ip: str = "10.200.0.1"
    broker_tunnel_network: str = "10.100.0.0/16"
    broker_wg_port: int = 51820

    # UI public domain — user-facing URL for portal links, emails, enrollment instructions
    # If set, used instead of deriving from broker_public_endpoint.
    # Example: "broker.example.com"
    ui_public_domain: str = ""

    # Broker diagnostic server (internal, used by control-plane to trigger on-demand diagnostics)
    broker_diag_url: str = "http://localhost:8080"  # Where the broker's diag server listens

    # OTP settings
    otp_length: int = 6  # Number of digits in the OTP code
    otp_ttl_seconds: int = 300  # 5 minutes validity
    otp_max_attempts: int = 3  # Max verification attempts before code is invalidated

    # SMTP settings for OTP email delivery
    smtp_host: str = ""  # e.g., "smtp.gmail.com", "email-smtp.eu-central-1.amazonaws.com"
    smtp_port: int = 587  # 587 for STARTTLS, 465 for SSL
    smtp_user: str = ""
    smtp_password: str = ""
    smtp_from: str = ""  # e.g., "noreply@wireztna.io"
    smtp_from_name: str = "WireZTNA"
    smtp_use_tls: bool = True

    # Access-pass plan limits (CE default: unlimited on self-host; opt in to enforce)
    plan_limits_enabled: bool = False

    # Free tier policies
    freetier_inactivity_days: int = 90  # Days of inactivity before free tier user is deactivated
    freetier_cleanup_interval_hours: int = 24  # How often to run the cleanup check (hours)
    freetier_cleanup_enabled: bool = True  # Enable/disable automatic free tier cleanup

    class Config:
        env_file = ".env"
        extra = "ignore"


settings = Settings()


# ── Startup validation: fail-fast on insecure defaults (compliance C4 / A.8.24) ──
_INSECURE_SECRET_DEFAULTS = {"change-me-to-a-random-string", "changeme", "secret", ""}

import os as _os  # noqa: E402

# Only enforce in production-like environments (not during pytest / alembic / migrations)
if _os.environ.get("WIREZTNA_SKIP_SECURITY_CHECK") != "1":
    if settings.secret_key in _INSECURE_SECRET_DEFAULTS:
        import sys
        print(
            "\n"
            "╔══════════════════════════════════════════════════════════════╗\n"
            "║  FATAL: SECRET_KEY is set to an insecure default value.    ║\n"
            "║  Set a strong random SECRET_KEY in .env before starting.   ║\n"
            "║  Generate one with: python -c 'import secrets;             ║\n"
            "║    print(secrets.token_urlsafe(48))'                       ║\n"
            "╚══════════════════════════════════════════════════════════════╝\n",
            file=sys.stderr,
        )
        sys.exit(1)
