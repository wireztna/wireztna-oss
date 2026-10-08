#!/usr/bin/env python3
"""
Migration: Add 'api_key_hash' column to publishers table.

Adds: publishers.api_key_hash VARCHAR(64) — stores SHA-256 hash of wpk_... publisher API key.

Existing publishers will have NULL (grace period: heartbeats allowed without key but logged).
New publishers enrolled after this migration receive a key during enrollment.

This migration is idempotent — safe to run multiple times.

Rollback:
    sqlite3 /opt/wireztna/data/wireztna.db "ALTER TABLE publishers DROP COLUMN api_key_hash"
    (SQLite 3.35+ supports DROP COLUMN)
"""

import sqlite3
import sys
import shutil
from pathlib import Path

DB_PATH = sys.argv[1] if len(sys.argv) > 1 else "/opt/wireztna/data/wireztna.db"


def main():
    db = Path(DB_PATH)
    if not db.exists():
        print(f"ERROR: Database not found at {DB_PATH}")
        sys.exit(1)

    # Backup
    backup_path = f"{DB_PATH}.bak-pre-publisher-apikey"
    shutil.copy2(DB_PATH, backup_path)
    print(f"[+] Backup created: {backup_path}")

    conn = sqlite3.connect(DB_PATH)
    cur = conn.cursor()

    # Check if column already exists
    cur.execute("PRAGMA table_info(publishers)")
    columns = [row[1] for row in cur.fetchall()]

    if "api_key_hash" in columns:
        print("[+] Column 'api_key_hash' already exists — skipping")
    else:
        print("[*] Adding 'api_key_hash' column to publishers table...")
        cur.execute("ALTER TABLE publishers ADD COLUMN api_key_hash VARCHAR(64)")
        print("[+] Column added")

    conn.commit()

    # Summary
    cur.execute("SELECT id, name, api_key_hash IS NOT NULL as has_key FROM publishers ORDER BY name")
    rows = cur.fetchall()
    print(f"\n[+] Publishers ({len(rows)} total):")
    print(f"    {'Name':<30} {'Has API Key'}")
    print(f"    {'─' * 30} {'─' * 11}")
    for row in rows:
        print(f"    {row[1]:<30} {'Yes' if row[2] else 'No (legacy)'}")

    conn.close()
    print(f"\n[+] Migration complete. Backup at: {backup_path}")
    print("[!] Legacy publishers need key assignment via admin rotate-key or publisher auto-upgrade.")


if __name__ == "__main__":
    main()
