CREATE TABLE billing_accounts (
    billing_account_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    organization_id text REFERENCES organizations(organization_id) ON DELETE CASCADE,
    plan text NOT NULL CHECK (plan IN ('free','pro','team','enterprise','developer')),
    status text NOT NULL CHECK (status IN ('active','past_due','suspended','closed')),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    provider_customer_reference text,
    limits bytea NOT NULL,
    current_period_start timestamptz NOT NULL,
    current_period_end timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX billing_accounts_personal_unique ON billing_accounts(account_id) WHERE organization_id IS NULL;
CREATE UNIQUE INDEX billing_accounts_org_unique ON billing_accounts(organization_id) WHERE organization_id IS NOT NULL;

ALTER TABLE marketplace_listings ADD COLUMN price_minor bigint NOT NULL DEFAULT 0 CHECK (price_minor >= 0);
ALTER TABLE marketplace_listings ADD COLUMN currency text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$');
ALTER TABLE marketplace_listings ADD COLUMN commission_basis_points integer NOT NULL DEFAULT 0 CHECK (commission_basis_points BETWEEN 0 AND 3000);

CREATE TABLE marketplace_transactions (
    transaction_id text PRIMARY KEY,
    listing_id text NOT NULL REFERENCES marketplace_listings(listing_id),
    buyer_account_id text NOT NULL REFERENCES accounts(account_id),
    seller_account_id text NOT NULL REFERENCES accounts(account_id),
    buyer_agent_id text REFERENCES agents(agent_id),
    seller_agent_id text REFERENCES agents(agent_id),
    task_id text REFERENCES tasks(task_id),
    quantity bigint NOT NULL CHECK (quantity > 0),
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    commission_minor bigint NOT NULL CHECK (commission_minor >= 0 AND commission_minor <= amount_minor),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status text NOT NULL CHECK (status IN ('pending','authorized','completed','cancelled')),
    idempotency_key text NOT NULL,
    metadata bytea,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (buyer_account_id,idempotency_key)
);
CREATE INDEX marketplace_transactions_party_idx ON marketplace_transactions(buyer_account_id,seller_account_id,updated_at DESC);
CREATE INDEX marketplace_transactions_completed_idx ON marketplace_transactions(listing_id,buyer_account_id,status);

INSERT INTO schema_migrations(version) VALUES ('008_billing_marketplace') ON CONFLICT DO NOTHING;
