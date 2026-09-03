DROP TABLE IF EXISTS sync_cursors;
DROP TABLE IF EXISTS sync_nonces;
DELETE FROM schema_migrations WHERE version='006_sync';
