CREATE TABLE IF NOT EXISTS billing_refund_routes (
 refund_id text PRIMARY KEY,
 invoice_id text NOT NULL REFERENCES billing_documents(invoice_id),
 provider text NOT NULL,
 provider_refund_id text,
 status text NOT NULL,
 last_checked_at timestamptz,
 check_attempts integer NOT NULL DEFAULT 0,
 UNIQUE(provider, provider_refund_id)
);
CREATE INDEX IF NOT EXISTS billing_refund_recovery ON billing_refund_routes(last_checked_at)
 WHERE status IN ('submitting','pending','review');
