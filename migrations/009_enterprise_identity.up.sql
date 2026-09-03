CREATE TABLE external_identities (
    external_identity_id text PRIMARY KEY,
    provider text NOT NULL,
    subject text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    email text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    UNIQUE (provider, subject),
    UNIQUE (provider, account_id)
);
CREATE INDEX external_identities_account_idx ON external_identities(account_id);

INSERT INTO schema_migrations(version) VALUES ('009_enterprise_identity') ON CONFLICT DO NOTHING;
