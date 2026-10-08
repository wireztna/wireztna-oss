-- Migration: Free Tier Cleanup & Anti-Alias
-- Purpose: Add last_activity and canonical_email columns for inactivity tracking
--          and email alias abuse prevention.
-- Run: sqlite3 /opt/wireztna/data/wireztna.db < 005_freetier_cleanup.sql

-- 1. Add last_activity column (tracks last login/API usage for inactivity detection)
ALTER TABLE users ADD COLUMN last_activity DATETIME;

-- 2. Add canonical_email column (normalized email to detect alias duplicates)
ALTER TABLE users ADD COLUMN canonical_email VARCHAR(255);

-- 3. Index for efficient cleanup queries (free tier + active + last_activity)
CREATE INDEX IF NOT EXISTS ix_users_freetier_activity
    ON users(plan, status, last_activity)
    WHERE plan = 'free';

-- 4. Index for canonical email uniqueness checks during registration
CREATE INDEX IF NOT EXISTS ix_users_canonical_email
    ON users(canonical_email)
    WHERE canonical_email IS NOT NULL;

-- 5. Backfill: set last_activity = created_at for existing free tier users
--    (so they aren't immediately flagged as inactive)
UPDATE users SET last_activity = created_at WHERE plan = 'free' AND last_activity IS NULL;

-- 6. Backfill: compute canonical_email for existing free tier users
--    NOTE: SQLite doesn't support functions for this. Run the Python backfill script
--    after applying this migration:
--      python -m migrations.backfill_canonical_email
