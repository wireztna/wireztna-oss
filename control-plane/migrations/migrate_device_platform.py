"""Migration: Add platform/device info columns to client_peer_status and client_enrollment_tokens.

New columns:
- client_peer_status.platform (VARCHAR 50) — e.g. "darwin/arm64", "windows/amd64"
- client_enrollment_tokens.device_name (VARCHAR 255) — e.g. "MacBook-Sergio"
- client_enrollment_tokens.device_platform (VARCHAR 50) — e.g. "darwin/arm64"

Run manually ONCE before deploying the new code:
    cd /opt/wireztna/control-plane
    python -m migrations.migrate_device_platform

Safe to run multiple times (idempotent — skips columns that already exist).
"""

import sqlite3
import sys
from pathlib import Path


def get_db_path() -> str:
    """Resolve the SQLite database path from .env or default."""
    env_file = Path(__file__).resolve().parent.parent / ".env"
    db_path = str(Path(__file__).resolve().parent.parent / "data" / "wireztna.db")

    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if line.startswith("DATABASE_URL="):
                url = line.split("=", 1)[1].strip().strip('"').strip("'")
                if ":///" in url:
                    db_path = url.split(":///", 1)[1]
                break

    return db_path


def column_exists(conn: sqlite3.Connection, table: str, column: str) -> bool:
    """Check if a column exists in a table."""
    cursor = conn.execute(f"PRAGMA table_info({table})")
    columns = [row[1] for row in cursor.fetchall()]
    return column in columns


def migrate(db_path: str):
    """Add platform and device info columns."""
    conn = sqlite3.connect(db_path)
    conn.execute("PRAGMA journal_mode=WAL")

    changes = []

    # 1. client_peer_status.platform
    if not column_exists(conn, "client_peer_status", "platform"):
        conn.execute("ALTER TABLE client_peer_status ADD COLUMN platform VARCHAR(50)")
        changes.append("client_peer_status.platform")

    # 2. client_enrollment_tokens.device_name
    if not column_exists(conn, "client_enrollment_tokens", "device_name"):
        conn.execute("ALTER TABLE client_enrollment_tokens ADD COLUMN device_name VARCHAR(255)")
        changes.append("client_enrollment_tokens.device_name")

    # 3. client_enrollment_tokens.device_platform
    if not column_exists(conn, "client_enrollment_tokens", "device_platform"):
        conn.execute("ALTER TABLE client_enrollment_tokens ADD COLUMN device_platform VARCHAR(50)")
        changes.append("client_enrollment_tokens.device_platform")

    conn.commit()
    conn.close()

    if changes:
        print(f"[+] Migration complete. Added columns: {', '.join(changes)}")
    else:
        print("[=] Already migrated — all columns exist. Nothing to do.")


if __name__ == "__main__":
    db_path = get_db_path()
    print(f"[*] Database: {db_path}")

    if not Path(db_path).exists():
        print(f"[!] Database file not found: {db_path}")
        print("    If this is a fresh install, the columns will be created by create_all().")
        sys.exit(0)

    migrate(db_path)
