DROP TABLE IF EXISTS artifact_chunks;
DROP TABLE IF EXISTS artifact_upload_sessions;
DROP TABLE IF EXISTS artifact_versions;
ALTER TABLE artifacts DROP COLUMN IF EXISTS encryption_metadata;
ALTER TABLE artifacts DROP COLUMN IF EXISTS chunk_manifest_hash;
