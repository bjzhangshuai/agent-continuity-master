BEGIN;
CREATE TABLE IF NOT EXISTS billing_documents (
    invoice_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id),
    organization_id text NOT NULL DEFAULT '',
    billing_account_id text NOT NULL REFERENCES billing_accounts(billing_account_id),
    idempotency_key text NOT NULL,
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL CHECK (period_end > period_start),
    test_mode boolean NOT NULL,
    status text NOT NULL CHECK (status IN ('open','paid','void','refunded','disputed')),
    version bigint NOT NULL CHECK (version > 0),
    document jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE(account_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS billing_documents_account_idx ON billing_documents(account_id,created_at DESC);
CREATE INDEX IF NOT EXISTS billing_documents_period_idx ON billing_documents(account_id,organization_id,test_mode,period_start,period_end);
CREATE TABLE IF NOT EXISTS billing_payment_routes (
    payment_id text PRIMARY KEY,
    invoice_id text NOT NULL REFERENCES billing_documents(invoice_id),
    provider text NOT NULL,
    provider_order_id text,
    provider_transaction_id text,
    status text NOT NULL,
    environment text NOT NULL,
    last_checked_at timestamptz,
    reconcile_attempts integer NOT NULL DEFAULT 0,
    UNIQUE(provider,provider_order_id),
    UNIQUE(provider,provider_transaction_id)
);
CREATE INDEX IF NOT EXISTS billing_payment_routes_invoice_idx ON billing_payment_routes(invoice_id);
CREATE INDEX IF NOT EXISTS billing_payment_routes_due_idx ON billing_payment_routes(last_checked_at) WHERE environment <> 'simulation' AND status IN ('creating','pending','approved') AND reconcile_attempts < 20;
INSERT INTO schema_migrations(version) VALUES ('010_payments') ON CONFLICT DO NOTHING;
COMMIT;
