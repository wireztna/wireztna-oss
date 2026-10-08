-- Migration: Add ON DELETE CASCADE to FK references to users.id
-- Purpose: Fix user deletion bug — without CASCADE, deleting a user with active
--          sessions or access passes caused a silent FK constraint violation and rollback.
-- Run: sqlite3 /opt/wireztna/data/wireztna.db < 006_cascade_user_fk.sql
--
-- NOTE: SQLite doesn't support ALTER CONSTRAINT. We must recreate the tables.
--       This migration preserves all existing data.

PRAGMA foreign_keys = OFF;

BEGIN TRANSACTION;

-- ─── 1. client_sessions: add ON DELETE CASCADE to user_id FK ───

CREATE TABLE client_sessions_new (
    id VARCHAR(36) PRIMARY KEY,
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    preshared_key VARCHAR(64) NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    client_ip VARCHAR(45),
    selected_group_id VARCHAR(36),
    exit_node_publisher_id VARCHAR(36),
    is_active BOOLEAN DEFAULT 1
);

INSERT INTO client_sessions_new
    SELECT id, user_id, preshared_key, expires_at, created_at,
           client_ip, selected_group_id, exit_node_publisher_id, is_active
    FROM client_sessions;

DROP TABLE client_sessions;
ALTER TABLE client_sessions_new RENAME TO client_sessions;

CREATE INDEX IF NOT EXISTS ix_client_sessions_user_id ON client_sessions(user_id);

-- ─── 2. access_passes: add ON DELETE CASCADE to created_by_user_id FK ───

CREATE TABLE access_passes_new (
    id VARCHAR(12) PRIMARY KEY,
    created_by_user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label VARCHAR(200) NOT NULL,
    scope_json TEXT NOT NULL,
    ttl_seconds INTEGER NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    connection_url VARCHAR(500) NOT NULL,
    bytes_uploaded INTEGER DEFAULT 0,
    bytes_downloaded INTEGER DEFAULT 0,
    connections_count INTEGER DEFAULT 0,
    last_activity_at DATETIME,
    metadata_json TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    revoked_at DATETIME,
    max_bytes INTEGER
);

INSERT INTO access_passes_new
    SELECT id, created_by_user_id, label, scope_json, ttl_seconds, status,
           connection_url, bytes_uploaded, bytes_downloaded, connections_count,
           last_activity_at, metadata_json, created_at, expires_at, revoked_at, max_bytes
    FROM access_passes;

DROP TABLE access_passes;
ALTER TABLE access_passes_new RENAME TO access_passes;

COMMIT;

PRAGMA foreign_keys = ON;
