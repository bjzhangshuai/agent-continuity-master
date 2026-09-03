# Upgrade

Use expand → migrate → deploy compatible application → backfill → observe → contract. The migration command obtains an advisory lock and is idempotent. Helm runs a bounded post-install/post-upgrade migration Job while dev also permits safe automatic migration.

Before upgrade, verify database and Artifact backups, render/lint the chart, run unit/race/E2E, record the current Helm revision and image digest, and confirm at least 30% free database/storage headroom. After upgrade, run smoke tests and inspect readiness, API errors, event lag, duplicate rate and conflicts.
