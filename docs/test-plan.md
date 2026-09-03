# Test Plan

Release qualification runs `go test ./...`, `go vet ./...`, `go test -race ./...`, Go/Python/TypeScript/Swift/Kotlin/.NET SDK contract tests, cross-language signature conformance, cross-device SDK E2E, OpenAPI/JSON validation, three-environment Helm lint/render, Compose E2E, PostgreSQL integration, backup/restore drill and Rancher Desktop Kubernetes smoke. CI additionally runs reachable-vulnerability, package-isolated SAST, imported-module license inventory, SBOM and container scans.

Coverage by layer:

- Unit: Event validation/signature/digest, sequence/HLC/DAG, idempotency, policy, E2EE primitives and rotation, snapshot projector, merge/conflict rules, Artifact manifests/chunks/cache/tamper, QR rendering, OIDC, secrets and webhook SSRF/retry.
- Integration: migrations through the current version, PostgreSQL transactional Event acceptance, Organization/policy, SCIM identity, billing/marketplace, MinIO/S3 Artifact upload/download and backup restore.
- Adapter contract: Generic and real Codex JSONL discovery/capture/input/output/tool/Artifact/native-resume/rehydrate/error behavior.
- Distributed/offline: 2, 10 and 100 devices; 1 minute, 1 hour, 1 day and long offline backlog; packet loss, duplicate, reorder and partition healing.
- Chaos/security: process crash between Inbox stage/projection, queue retry/restart, object timeout, clock skew, token/invitation replay, device spoof/signature failure, task enumeration, cross-tenant/privilege checks and Artifact tamper.
- Performance: 1,000-Event replay/reconciliation and 16 MiB verified Artifact throughput against documented local safety budgets.
- E2E/smoke: two encryption-capable devices, reversed causal delivery, duplicate apply, conflict resolution, Task key exchange, push/pull/ack/reconcile, interrupted resumable Artifact transfer, manifest-only lazy fetch, signed QR invite, scoped read, ACL revoke and device revoke.

Test infrastructure is isolated by random Compose project/database/ports and cleanup traps. Kubernetes writes always specify `--context rancher-desktop` and the dev namespace. No test may infer or mutate the user's default remote cluster context.
