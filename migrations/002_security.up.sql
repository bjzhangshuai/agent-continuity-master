ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS encryption_public_key text NOT NULL DEFAULT '';

CREATE TABLE auth_sessions (
    session_id text PRIMARY KEY,
    family_id text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    subject text NOT NULL,
    refresh_token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    revoked_at timestamptz,
    replaced_by text
);
CREATE INDEX auth_sessions_account_idx ON auth_sessions(account_id, created_at DESC);
CREATE INDEX auth_sessions_family_idx ON auth_sessions(family_id);

CREATE TABLE api_keys (
    api_key_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    name text NOT NULL,
    prefix text NOT NULL,
    secret_hash text NOT NULL UNIQUE,
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL,
    expires_at timestamptz,
    last_used_at timestamptz,
    revoked_at timestamptz
);
CREATE INDEX api_keys_account_idx ON api_keys(account_id, created_at DESC);

CREATE TABLE audit_events (
    audit_event_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    request_id text,
    source_hash text,
    details bytea,
    created_at timestamptz NOT NULL
);
CREATE INDEX audit_events_account_time_idx ON audit_events(account_id, created_at DESC);

CREATE TABLE task_key_sets (
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    key_version integer NOT NULL CHECK (key_version > 0),
    algorithm text NOT NULL,
    created_by text NOT NULL REFERENCES devices(device_id),
    wrapped_keys jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (task_id, key_version)
);

INSERT INTO schema_migrations(version) VALUES ('002_security') ON CONFLICT DO NOTHING;
