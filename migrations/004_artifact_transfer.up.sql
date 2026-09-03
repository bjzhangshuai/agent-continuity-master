ALTER TABLE artifacts
    ADD COLUMN IF NOT EXISTS chunk_manifest_hash text,
    ADD COLUMN IF NOT EXISTS encryption_metadata bytea;

CREATE TABLE artifact_versions (
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    artifact_id text NOT NULL,
    version_id text NOT NULL,
    parent_version_id text,
    account_id text NOT NULL REFERENCES accounts(account_id),
    content_type text NOT NULL,
    filename text NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    content_hash text NOT NULL,
    chunk_manifest_hash text,
    producer_type text,
    producer_id text,
    device_id text,
    storage_reference text NOT NULL,
    metadata bytea,
    encryption_metadata bytea,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (task_id, artifact_id, version_id)
);
CREATE INDEX artifact_versions_history_idx ON artifact_versions(task_id, artifact_id, created_at DESC);

INSERT INTO artifact_versions(task_id,artifact_id,version_id,parent_version_id,account_id,content_type,filename,size,content_hash,producer_type,producer_id,device_id,storage_reference,metadata,created_at)
SELECT task_id,artifact_id,version_id,parent_version_id,account_id,content_type,filename,size,content_hash,producer_type,producer_id,device_id,storage_reference,metadata,created_at FROM artifacts
ON CONFLICT DO NOTHING;

CREATE TABLE artifact_upload_sessions (
    upload_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    artifact_id text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(account_id),
    version_id text NOT NULL,
    parent_version_id text,
    filename text NOT NULL,
    content_type text NOT NULL,
    total_size bigint NOT NULL CHECK (total_size >= 0),
    chunk_size bigint NOT NULL CHECK (chunk_size > 0),
    total_chunks integer NOT NULL CHECK (total_chunks > 0),
    content_hash text NOT NULL,
    encryption_metadata bytea,
    status text NOT NULL CHECK (status IN ('open','completed','expired','cancelled')),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    completed_at timestamptz
);
CREATE INDEX artifact_upload_sessions_expiry_idx ON artifact_upload_sessions(status, expires_at);

CREATE TABLE artifact_chunks (
    upload_id text NOT NULL REFERENCES artifact_upload_sessions(upload_id) ON DELETE CASCADE,
    chunk_index integer NOT NULL CHECK (chunk_index >= 0),
    byte_offset bigint NOT NULL CHECK (byte_offset >= 0),
    size bigint NOT NULL CHECK (size >= 0),
    content_hash text NOT NULL,
    storage_reference text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (upload_id, chunk_index)
);

INSERT INTO schema_migrations(version) VALUES ('004_artifact_transfer') ON CONFLICT DO NOTHING;
