# Operations Runbook

| Symptom | Diagnose | Safe response | Recovery proof |
|---|---|---|---|
| Sync backlog increasing | readiness, DB latency/pool, retry-after, daemon doctor | preserve Outboxes; fix dependency; scale API only with DB headroom | backlog drains, cursor/head convergence and no Event loss |
| Reconciliation stuck | missing parents, branch heads, open conflict type | repull cursor; use explicit resolution/merge, never edit Event rows | snapshot digest verifies and open conflict closes |
| Database unavailable | readiness, connection/DNS/TLS, managed status | stop rollout; devices continue local Outbox; restore connectivity/PITR | migrations current, push replay idempotent, smoke green |
| Object storage unavailable | S3 health/DNS/egress/credentials | keep manifests; pause completion/download retry; do not publish partial data | resumed chunks assemble and final hash matches |
| Event bus/notifier unavailable | WebSocket delivery and process health | treat as latency issue; clients use cursor pull; rebuild notification from DB Event Log | missed client catches up from cursor |
| Abnormal conflict spike | adapter/device version, clock skew, partitions | disable faulty binding/device, retain all branches | conflict rate normal and sampled provenance intact |
| Key rotation failure | recipient device state/envelope version/audit | stop new sensitive writes if policy requires; revoke bad recipient; retry full active-device set | every active device unwraps new version; revoked cannot fetch |
| Device compromise | audit scope and last key/session use | revoke device/API sessions, remove Task grants, rotate affected Task keys | new Events rejected and unaffected devices resume |
| High API latency | HTTP percentiles, DB plans, CPU/memory, S3 I/O | shed/rate-limit abuse; tune query/storage; scale within PDB/HPA | P95/P99 and error alerts clear |
| Artifact hash mismatch | manifest, object version, local cache | quarantine/delete corrupt cache only; restore immutable version by digest | independent download hash and E2E pass |

Every incident records start/end, affected accounts/Tasks/Artifacts, causal evidence, actions, data-loss assessment, key/session changes, recovery evidence and follow-up owner. Never run destructive down migrations or delete retained storage during incident pressure.
