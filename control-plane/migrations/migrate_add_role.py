#!/usr/bin/env python3
"""
Migration: Add 'role' column to users table.

Adds: users.role VARCHAR(20) DEFAULT 'user'
Maps: is_admin=1 → role='super_admin', is_admin=0 → role='user'

This migration is idempotent — safe to run multiple times.

Rollback:
    sqlite3 /opt/wireztna/data/wireztna.db "ALTER TABLE users DROP COLUMN role"
    (SQLite 3.35+ supports DROP COLUMN)

    Or restore from backup:
    cp /opt/wireztna/data/wireztna.db.bak-pre-role-migration /opt/wireztna/data/wireztna.db
"""

import sqlite3
import sys
import shutil
from datetime import datetime
from pathlib import Path

DB_PATH = sys.argv[1] if len(sys.argv) > 1 else "/opt/wireztna/data/wireztna.db"


def main():
    db = Path(DB_PATH)
    if not db.exists():
        print(f"ERROR: Database not found at {DB_PATH}")
        sys.exit(1)

    # Backup
    backup_path = f"{DB_PATH}.bak-pre-role-migration"
    shutil.copy2(DB_PATH, backup_path)
    print(f"[+] Backup created: {backup_path}")

    conn = sqlite3.connect(DB_PATH)
    cur = conn.cursor()

    # Check if column already exists
    cur.execute("PRAGMA table_info(users)")
    columns = [row[1] for row in cur.fetchall()]

    if "role" in columns:
        print("[+] Column 'role' already exists — skipping ADD COLUMN")
    else:
        print("[*] Adding 'role' column to users table...")
        cur.execute("ALTER TABLE users ADD COLUMN role VARCHAR(20) DEFAULT 'user'")
        print("[+] Column added")

    # Migrate: is_admin=1 → role='super_admin'
    cur.execute("UPDATE users SET role = 'super_admin' WHERE is_admin = 1 AND (role IS NULL OR role = 'user')")
    migrated = cur.rowcount
    print(f"[+] Migrated {migrated} admin(s) to role='super_admin'")

    # Ensure all users have a role value
    cur.execute("UPDATE users SET role = 'user' WHERE role IS NULL")
    fixed = cur.rowcount
    if fixed > 0:
        print(f"[+] Set role='user' for {fixed} user(s) with NULL role")

    conn.commit()

    # Verify
    cur.execute("SELECT email, role, is_admin FROM users ORDER BY role DESC, email")
    print("\n[+] Current user roles:")
    print(f"    {'Email':<40} {'Role':<15} {'is_admin'}")
    print(f"    {'─' * 40} {'─' * 15} {'─' * 8}")
    for row in cur.fetchall():
        print(f"    {row[0]:<40} {row[1]:<15} {row[2]}")

    conn.close()
    print(f"\n[+] Migration complete. Backup at: {backup_path}")


if __name__ == "__main__":
    main()
