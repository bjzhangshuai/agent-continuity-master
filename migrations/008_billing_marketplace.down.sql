DROP TABLE IF EXISTS marketplace_transactions;
ALTER TABLE marketplace_listings DROP COLUMN IF EXISTS commission_basis_points;
ALTER TABLE marketplace_listings DROP COLUMN IF EXISTS currency;
ALTER TABLE marketplace_listings DROP COLUMN IF EXISTS price_minor;
DROP TABLE IF EXISTS billing_accounts;
DELETE FROM schema_migrations WHERE version='008_billing_marketplace';
