BEGIN;
CREATE TABLE IF NOT EXISTS treasury_documents (
  owner_account_id text PRIMARY KEY REFERENCES accounts(account_id),
  version bigint NOT NULL CHECK (version > 0),
  document jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations(version) VALUES ('011_treasury') ON CONFLICT DO NOTHING;
COMMIT;
