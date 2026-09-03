-- Additive only: historical invoices, organizations and credentials stay intact.
BEGIN;
ALTER TABLE organizations ADD COLUMN parent_id text REFERENCES organizations(organization_id) ON DELETE RESTRICT;
ALTER TABLE organizations ADD CONSTRAINT organization_not_own_parent CHECK (parent_id IS NULL OR parent_id <> organization_id);
CREATE INDEX organizations_parent_idx ON organizations(parent_id);
CREATE TABLE tenancy_control (id boolean PRIMARY KEY DEFAULT true CHECK(id), version bigint NOT NULL, document jsonb NOT NULL);
INSERT INTO tenancy_control(id,version,document) VALUES(true,0,'{"version":0,"grants":[],"mandates":[],"events":[]}');
CREATE TABLE portal_logins (
 username text PRIMARY KEY, account_id text NOT NULL UNIQUE REFERENCES accounts(account_id),
 display_name text NOT NULL, password_hash text NOT NULL
);
CREATE TABLE portal_sessions (
 token_hash text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(account_id), username text NOT NULL,
 created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL, last_seen timestamptz NOT NULL
);
CREATE INDEX portal_sessions_expiry_idx ON portal_sessions(expires_at);
CREATE INDEX billing_tenant_payer_idx ON billing_documents((document->'invoice'->>'payer_organization_id'));
INSERT INTO schema_migrations(version) VALUES ('012_tenant_portal') ON CONFLICT DO NOTHING;
COMMIT;
