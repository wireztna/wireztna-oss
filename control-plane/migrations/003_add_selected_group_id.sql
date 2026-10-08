-- Migration: Add selected_group_id to client_sessions
-- Purpose: CIDR overlap resolution — allows client to select which group/project to connect to
-- Run: sqlite3 /opt/wireztna/data/wireztna.db < 003_add_selected_group_id.sql

ALTER TABLE client_sessions ADD COLUMN selected_group_id VARCHAR(36);
