-- Migration: Free Tier Phase 1
-- Purpose: Add plan field to users, create api_keys and pass_usage_monthly tables
-- Run: sqlite3 /opt/wireztna/data/wireztna.db < 004_free_tier.sql

-- 1. Add plan column to users (existing users get 'pro')
ALTER TABLE users ADD COLUMN plan VARCHAR(20) DEFAULT 'pro';

-- 2. API keys table
CREATE TABLE IF NOT EXISTS api_keys (
    id VARCHAR(36) PRIMARY KEY,
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_hash VARCHAR(64) NOT NULL,
    key_prefix VARCHAR(8) NOT NULL,
    label VARCHAR(100) NOT NULL DEFAULT 'default',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    last_used_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at DATETIME
);
CREATE INDEX IF NOT EXISTS ix_api_keys_key_hash ON api_keys(key_hash);
CREATE INDEX IF NOT EXISTS ix_api_keys_user_id ON api_keys(user_id);

-- 3. Monthly pass usage tracking (for free tier rate limiting)
CREATE TABLE IF NOT EXISTS pass_usage_monthly (
    id VARCHAR(36) PRIMARY KEY,
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    month VARCHAR(7) NOT NULL,
    passes_created INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, month)
);

-- 4. Add self-service fields to enrollment_tokens (publisher tokens)
ALTER TABLE enrollment_tokens ADD COLUMN created_by_user_id VARCHAR(36);
ALTER TABLE enrollment_tokens ADD COLUMN auto_group_id VARCHAR(36);
