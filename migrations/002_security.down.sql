DROP TABLE IF EXISTS task_key_sets;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS auth_sessions;
ALTER TABLE devices DROP COLUMN IF EXISTS encryption_public_key;
