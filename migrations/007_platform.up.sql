CREATE TABLE organizations (
    organization_id text PRIMARY KEY,
    name text NOT NULL,
    slug text NOT NULL UNIQUE,
    owner_account_id text NOT NULL REFERENCES accounts(account_id),
    plan text NOT NULL CHECK (plan IN ('free','pro','team','enterprise')),
    data_residency text,
    key_provider text NOT NULL CHECK (key_provider IN ('platform','byok','kms','hsm')),
    settings bytea,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE organization_members (
    organization_id text NOT NULL REFERENCES organizations(organization_id) ON DELETE CASCADE,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('owner','admin','member','viewer','billing')),
    status text NOT NULL CHECK (status IN ('active','removed')),
    joined_at timestamptz NOT NULL,
    removed_at timestamptz,
    PRIMARY KEY (organization_id, account_id)
);
CREATE INDEX organization_members_account_idx ON organization_members(account_id,status);

CREATE TABLE teams (
    team_id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES organizations(organization_id) ON DELETE CASCADE,
    name text NOT NULL,
    slug text NOT NULL,
    settings bytea,
    created_at timestamptz NOT NULL,
    UNIQUE (organization_id, slug)
);

CREATE TABLE team_members (
    team_id text NOT NULL REFERENCES teams(team_id) ON DELETE CASCADE,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('lead','member','viewer')),
    status text NOT NULL CHECK (status IN ('active','removed')),
    joined_at timestamptz NOT NULL,
    removed_at timestamptz,
    PRIMARY KEY (team_id, account_id)
);

CREATE TABLE access_policies (
    policy_id text PRIMARY KEY,
    organization_id text NOT NULL REFERENCES organizations(organization_id) ON DELETE CASCADE,
    name text NOT NULL,
    effect text NOT NULL CHECK (effect IN ('allow','deny')),
    subjects jsonb NOT NULL,
    actions jsonb NOT NULL,
    resource_type text NOT NULL,
    conditions jsonb NOT NULL DEFAULT '[]'::jsonb,
    priority integer NOT NULL DEFAULT 0,
    created_by text NOT NULL REFERENCES accounts(account_id),
    created_at timestamptz NOT NULL
);
CREATE INDEX access_policies_org_idx ON access_policies(organization_id,priority DESC);

ALTER TABLE tasks ADD COLUMN organization_id text REFERENCES organizations(organization_id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN team_id text REFERENCES teams(team_id) ON DELETE SET NULL;
CREATE INDEX tasks_organization_idx ON tasks(organization_id,updated_at DESC);
CREATE INDEX tasks_team_idx ON tasks(team_id,updated_at DESC);

CREATE TABLE artifact_permissions (
    task_id text NOT NULL,
    artifact_id text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    permission text NOT NULL CHECK (permission IN ('read','write','admin')),
    granted_by text NOT NULL REFERENCES accounts(account_id),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (task_id,artifact_id,account_id),
    FOREIGN KEY (task_id,artifact_id) REFERENCES artifacts(task_id,artifact_id) ON DELETE CASCADE
);

ALTER TABLE invitations ADD COLUMN inviter_device_id text REFERENCES devices(device_id) ON DELETE SET NULL;
ALTER TABLE invitations ADD COLUMN nonce text;
ALTER TABLE invitations ADD COLUMN token_digest text;
ALTER TABLE invitations ADD COLUMN key_exchange_reference bytea;
ALTER TABLE invitations ADD COLUMN signature text;
CREATE UNIQUE INDEX invitations_device_nonce_unique ON invitations(inviter_device_id,nonce) WHERE nonce IS NOT NULL;

CREATE TABLE oauth_clients (
    client_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    name text NOT NULL,
    secret_hash text NOT NULL UNIQUE,
    scopes jsonb NOT NULL,
    redirect_uris jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE TABLE webhook_endpoints (
    webhook_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    url text NOT NULL,
    events jsonb NOT NULL,
    secret_hash text NOT NULL,
    secret_ciphertext bytea NOT NULL,
    created_at timestamptz NOT NULL,
    disabled_at timestamptz
);

CREATE TABLE webhook_deliveries (
    delivery_id text PRIMARY KEY,
    webhook_id text NOT NULL REFERENCES webhook_endpoints(webhook_id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload bytea NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt timestamptz NOT NULL,
    last_status integer,
    last_error text,
    created_at timestamptz NOT NULL,
    delivered_at timestamptz,
    dead_lettered_at timestamptz
);
CREATE INDEX webhook_deliveries_ready_idx ON webhook_deliveries(delivered_at,dead_lettered_at,next_attempt);

CREATE TABLE usage_records (
    usage_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    organization_id text REFERENCES organizations(organization_id) ON DELETE SET NULL,
    metric text NOT NULL,
    quantity bigint NOT NULL CHECK (quantity >= 0),
    unit text NOT NULL,
    resource_id text,
    idempotency_key text NOT NULL,
    metadata bytea,
    recorded_at timestamptz NOT NULL,
    UNIQUE (account_id,metric,idempotency_key)
);
CREATE INDEX usage_records_account_time_idx ON usage_records(account_id,recorded_at DESC);

CREATE TABLE marketplace_listings (
    listing_id text PRIMARY KEY,
    publisher_account_id text NOT NULL REFERENCES accounts(account_id),
    kind text NOT NULL CHECK (kind IN ('agent','capability','adapter','task')),
    name text NOT NULL,
    description text NOT NULL,
    version text NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    manifest bytea NOT NULL,
    status text NOT NULL CHECK (status IN ('draft','published','suspended')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX marketplace_listings_search_idx ON marketplace_listings(kind,status,updated_at DESC);

CREATE TABLE marketplace_installations (
    installation_id text PRIMARY KEY,
    listing_id text NOT NULL REFERENCES marketplace_listings(listing_id),
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    version text NOT NULL,
    status text NOT NULL CHECK (status IN ('active','removed')),
    installed_at timestamptz NOT NULL,
    UNIQUE (listing_id,account_id)
);

INSERT INTO schema_migrations(version) VALUES ('007_platform') ON CONFLICT DO NOTHING;
