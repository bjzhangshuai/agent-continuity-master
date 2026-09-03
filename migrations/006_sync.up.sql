CREATE TABLE sync_nonces (
    device_id text NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    nonce_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (device_id, nonce_hash)
);
CREATE INDEX sync_nonces_expiry_idx ON sync_nonces(expires_at);

CREATE TABLE sync_cursors (
    device_id text NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    server_sequence bigint NOT NULL CHECK (server_sequence >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (device_id, task_id)
);

INSERT INTO schema_migrations(version) VALUES ('006_sync') ON CONFLICT DO NOTHING;
