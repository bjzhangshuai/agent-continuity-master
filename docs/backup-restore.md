# Backup and Restore

Development backup consists of a PostgreSQL custom-format dump, local Artifact snapshot, checksums, schema-version inventory, and a machine-readable manifest. Run `make backup` with explicit `CONTINUITY_BACKUP_DATABASE_URL`, `CONTINUITY_BACKUP_DIR`, and `CONTINUITY_ARTIFACT_DIR`. The script writes to a temporary directory, verifies command success, and atomically publishes the bundle.

`make restore-drill` requires an isolated database URL and Artifact target containing `restore_drill`/`restore-drill`; it refuses non-isolated names and non-empty Artifact targets. It verifies bundle checksums before `pg_restore`, restores Artifacts, and reports schema/Task/Event counts. The release drill then runs Artifact hash sampling and the E2E smoke journey.

Production uses continuous WAL archiving/PITR, encrypted base backups, S3 object versioning and lifecycle, cross-account immutable copies, and a separately governed key escrow. Service-managed master keys and customer E2EE keys are never placed in the ordinary data backup bundle. The optional Helm CronJob creates bounded logical dumps on a dedicated backup PVC; managed database and object-store backups remain the production source of truth.

Target objectives are RPO ≤5 minutes and RTO ≤60 minutes, calibrated by quarterly restore drills. A drill is successful only after schema validation, sampled object digest verification, Task snapshot replay, offline outbox replay/reconciliation, and the full release smoke test.
