"""Backfill canonical_email for existing free tier users.

Run after applying 005_freetier_cleanup.sql:
    cd control-plane
    python -m migrations.backfill_canonical_email

This script computes the normalized canonical_email for all users
that don't have one set yet (primarily existing free tier users).
"""

import sqlite3
import sys
from pathlib import Path

# Add parent to path so we can import app modules
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from app.services.email_normalization import normalize_email

# Default DB path (production override via CLI arg)
DEFAULT_DB_PATH = "/opt/wireztna/data/wireztna.db"


def backfill(db_path: str) -> int:
    """Backfill canonical_email for all users missing it.

    Returns number of users updated.
    """
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cur = conn.cursor()

    # Find users without canonical_email
    cur.execute("SELECT id, email FROM users WHERE canonical_email IS NULL AND email IS NOT NULL")
    rows = cur.fetchall()

    if not rows:
        print("[*] All users already have canonical_email set. Nothing to do.")
        return 0

    updated = 0
    for row in rows:
        canonical = normalize_email(row["email"])
        cur.execute(
            "UPDATE users SET canonical_email = ? WHERE id = ?",
            (canonical, row["id"]),
        )
        updated += 1

    conn.commit()
    conn.close()

    print(f"[+] Backfilled canonical_email for {updated} users.")
    return updated


if __name__ == "__main__":
    db_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_DB_PATH

    if not Path(db_path).exists():
        # Try dev path
        dev_path = Path(__file__).resolve().parent.parent / "data" / "wireztna.db"
        if dev_path.exists():
            db_path = str(dev_path)
        else:
            print(f"[!] Database not found at {db_path}")
            print(f"    Usage: python -m migrations.backfill_canonical_email [/path/to/wireztna.db]")
            sys.exit(1)

    print(f"[*] Using database: {db_path}")
    backfill(db_path)
