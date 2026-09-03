CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE accounts (
    account_id text PRIMARY KEY,
    name text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE devices (
    device_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id),
    name text NOT NULL,
    public_key text NOT NULL,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX devices_account_idx ON devices(account_id);

CREATE TABLE tasks (
    task_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id),
    title text NOT NULL,
    objective text NOT NULL,
    status text NOT NULL,
    origin_device text,
    head_event_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata bytea,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX tasks_account_activity_idx ON tasks(account_id, updated_at DESC);

CREATE TABLE permissions (
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    permission text NOT NULL CHECK (permission IN ('read','write','admin')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, account_id)
);

CREATE TABLE events (
    server_sequence bigserial PRIMARY KEY,
    event_id text NOT NULL UNIQUE,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    branch_id text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(account_id),
    participant_id text,
    agent_id text,
    provider_id text,
    device_id text NOT NULL REFERENCES devices(device_id),
    session_ref text,
    parent_event_ids jsonb NOT NULL,
    logical_time jsonb NOT NULL,
    device_sequence bigint NOT NULL CHECK (device_sequence > 0),
    event_type text NOT NULL,
    schema_version text NOT NULL,
    content_type text NOT NULL,
    content_digest text NOT NULL,
    payload bytea,
    artifact_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at_client timestamptz NOT NULL,
    received_at_server timestamptz NOT NULL,
    signature text NOT NULL,
    idempotency_key text NOT NULL,
    extensions bytea,
    UNIQUE (task_id, device_id, device_sequence),
    UNIQUE (task_id, device_id, idempotency_key)
);
CREATE INDEX events_task_sequence_idx ON events(task_id, server_sequence);
CREATE INDEX events_task_branch_sequence_idx ON events(task_id, branch_id, server_sequence);

CREATE TABLE conflicts (
    conflict_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    event_id text NOT NULL REFERENCES events(event_id) ON DELETE CASCADE,
    kind text NOT NULL,
    strategy text NOT NULL,
    details bytea,
    created_at timestamptz NOT NULL,
    resolved boolean NOT NULL DEFAULT false
);
CREATE INDEX conflicts_task_unresolved_idx ON conflicts(task_id, resolved, created_at);

CREATE TABLE artifacts (
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    artifact_id text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(account_id),
    version_id text NOT NULL,
    parent_version_id text,
    content_type text NOT NULL,
    filename text NOT NULL,
    size bigint NOT NULL CHECK (size >= 0),
    content_hash text NOT NULL,
    producer_type text,
    producer_id text,
    device_id text,
    storage_reference text NOT NULL,
    metadata bytea,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (task_id, artifact_id)
);
CREATE INDEX artifacts_hash_idx ON artifacts(content_hash);

CREATE TABLE invitations (
    invitation_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    inviter_account_id text NOT NULL REFERENCES accounts(account_id),
    permission text NOT NULL CHECK (permission IN ('read','write','admin')),
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    accepted_by text REFERENCES accounts(account_id),
    accepted_at timestamptz,
    revoked_at timestamptz
);
CREATE INDEX invitations_expiry_idx ON invitations(expires_at) WHERE accepted_at IS NULL AND revoked_at IS NULL;

INSERT INTO schema_migrations(version) VALUES ('001_initial') ON CONFLICT DO NOTHING;
