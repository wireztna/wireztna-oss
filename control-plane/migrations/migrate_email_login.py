"""Migration: Make email the primary login identifier.

This script:
1. Finds users with duplicate emails and adds +N suffix to resolve conflicts
2. For users without email, copies username to email field
3. Makes email column unique and non-nullable (via table rebuild for SQLite)

Run manually ONCE before deploying the new code:
    cd /opt/wireztna/control-plane
    python -m migrations.migrate_email_login

Safe to run multiple times (idempotent — skips if already migrated).
"""

import sqlite3
import sys
from collections import Counter
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
                # sqlite+aiosqlite:///path or sqlite+aiosqlite:////absolute/path
                if ":///" in url:
                    db_path = url.split(":///", 1)[1]
                break

    return db_path


def migrate(db_path: str):
    """Run the email login migration."""
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    print(f"[*] Database: {db_path}")

    # Check if email already has a unique index (already migrated)
    cursor.execute("SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='users'")
    indexes = cursor.fetchall()
    for idx in indexes:
        if idx["sql"] and "email" in idx["sql"].lower() and "unique" in idx["sql"].lower():
            print("[*] Email column already has unique index — migration already applied. Skipping.")
            conn.close()
            return

    # Step 1: Find all users
    cursor.execute("SELECT id, username, email FROM users ORDER BY created_at ASC")
    users = cursor.fetchall()
    print(f"[*] Found {len(users)} users")

    # Step 2: Fill empty emails with username (if username looks like email) or username@local
    updates = []
    for user in users:
        if not user["email"] or user["email"].strip() == "":
            email = user["username"] if "@" in user["username"] else f"{user['username']}@local"
            updates.append((email, user["id"]))
            print(f"    [fill] {user['username']} -> email: {email}")

    if updates:
        cursor.executemany("UPDATE users SET email = ? WHERE id = ?", updates)
        conn.commit()
        print(f"[*] Filled {len(updates)} empty email fields")

    # Reload after filling
    cursor.execute("SELECT id, username, email FROM users ORDER BY created_at ASC")
    users = cursor.fetchall()

    # Step 3: Find and resolve duplicates with +N suffix
    email_counter = Counter(user["email"].lower() for user in users)
    duplicates = {email: count for email, count in email_counter.items() if count > 1}

    if duplicates:
        print(f"[*] Found {len(duplicates)} duplicate email(s):")
        for email, count in duplicates.items():
            print(f"    {email} ({count} users)")

        # For each duplicate group, keep the first (oldest by order) as-is, suffix the rest
        seen = {}
        for user in users:
            email_lower = user["email"].lower()
            if email_lower in duplicates:
                if email_lower not in seen:
                    seen[email_lower] = 0
                    # First occurrence — keep as-is
                else:
                    seen[email_lower] += 1
                    n = seen[email_lower]
                    # Add +N before the @
                    local, domain = user["email"].rsplit("@", 1)
                    new_email = f"{local}+{n}@{domain}"
                    cursor.execute("UPDATE users SET email = ? WHERE id = ?", (new_email, user["id"]))
                    print(f"    [dedup] {user['username']}: {user['email']} -> {new_email}")

        conn.commit()
        print(f"[*] Resolved all duplicate emails")
    else:
        print("[*] No duplicate emails found")

    # Step 4: Make email unique and non-nullable (SQLite requires table rebuild)
    print("[*] Rebuilding table with email as UNIQUE NOT NULL...")

    # Get current table schema to preserve all columns
    cursor.execute("PRAGMA table_info(users)")
    columns = cursor.fetchall()
    col_names = [c["name"] for c in columns]

    # Create new table with email constraints
    # We keep username but remove its UNIQUE constraint (email is now the unique login identifier)
    cursor.executescript("""
        -- Create new table with correct constraints
        CREATE TABLE users_new (
            id VARCHAR(36) PRIMARY KEY,
            username VARCHAR(100) NOT NULL,
            email VARCHAR(255) NOT NULL UNIQUE,
            password_hash VARCHAR(255),
            public_key VARCHAR(44),
            overlay_ip VARCHAR(45),
            status VARCHAR(20) DEFAULT 'active',
            is_admin BOOLEAN DEFAULT 0,
            auth_provider VARCHAR(20) DEFAULT 'local',
            oidc_subject VARCHAR(255),
            vpn_mode BOOLEAN DEFAULT 0,
            max_enrollment_tokens INTEGER DEFAULT 3,
            totp_secret VARCHAR(32),
            totp_enabled BOOLEAN DEFAULT 0,
            must_change_password BOOLEAN DEFAULT 0,
            created_at DATETIME
        );

        -- Copy data
        INSERT INTO users_new SELECT
            id, username, email, password_hash, public_key, overlay_ip,
            status, is_admin, auth_provider, oidc_subject, vpn_mode,
            max_enrollment_tokens, totp_secret, totp_enabled, must_change_password, created_at
        FROM users;

        -- Swap tables
        DROP TABLE users;
        ALTER TABLE users_new RENAME TO users;

        -- Recreate index on username (not unique anymore, but still indexed for backward compat lookups)
        CREATE INDEX ix_users_username ON users(username);
    """)
    conn.commit()
    print("[*] Table rebuilt successfully")

    # Verify
    cursor.execute("SELECT count(*) as cnt FROM users")
    count = cursor.fetchone()["cnt"]
    print(f"[*] Verification: {count} users in migrated table")

    cursor.execute("SELECT id, username, email FROM users LIMIT 5")
    sample = cursor.fetchall()
    for u in sample:
        print(f"    {u['username']} -> {u['email']}")

    conn.close()
    print("[+] Migration complete! Email is now the primary login identifier.")
    print("[+] Username remains as display name (backward compatible for old clients).")


if __name__ == "__main__":
    db_path = get_db_path()

    if not Path(db_path).exists():
        print(f"[!] Database not found at {db_path}")
        sys.exit(1)

    # Backup first
    import shutil
    backup_path = db_path + ".bak-pre-email-migration"
    if not Path(backup_path).exists():
        shutil.copy2(db_path, backup_path)
        print(f"[*] Backup created: {backup_path}")

    migrate(db_path)
