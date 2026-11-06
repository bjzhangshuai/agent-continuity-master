# Full Requirements Traceability

This is an implementation evidence inventory, not a declaration of full commercial or standards readiness. The 2026-11-07 [product/architecture audit](product-architecture-review-2026-11-07.zh-CN.md) supersedes earlier completion claims. Local code/tests, real-app acceptance, production acceptance and commercial validation are separate gates.

Known code gaps include conformant current A2A/MCP gateways, automatic multi-agent execution orchestration, marketplace payment/settlement integration, tax invoicing, cloud KMS/workload identity integration and fully transactional audit/non-event metering. These are not merely missing credentials. Cross-agent rehydrate is not native runtime cloning.

| Area | State | Implementation and executable evidence |
|---|---|---|
| Universal Task State | LOCAL IMPLEMENTATION | Versioned Go/JSON Schema/Protobuf; Task, typed Events, Artifacts, branches, participants, Agent bindings, conflicts and snapshots |
| Ordering and delivery | LOCAL IMPLEMENTATION | Device sequence + causal DAG + HLC + server cursor; at-least-once, duplicate/idempotency/replay tests |
| Offline daemon | LOCAL IMPLEMENTATION | Crash-safe Outbox, bbolt Inbox/index/cursors, reconnect/backoff/jitter, replay, throttle, pause/resume, revoke response and doctor |
| Sync protocol | LOCAL IMPLEMENTATION | Signed hello/nonce, negotiation, head/cursor exchange, push/pull/ack, missing discovery, manifests and reconcile; unit/E2E |
| Reconciliation | LOCAL IMPLEMENTATION | Dedicated classifier for required conflict families, explicit strategies/resolution provenance, unit/E2E |
| DAG/fork/merge | LOCAL IMPLEMENTATION | Branch persistence/API/UI, merge parents/decision/provenance and snapshot projection tests |
| Snapshots | LOCAL IMPLEMENTATION | Event-log rebuild, topological projection, progress/decision/Artifact/issues and digest verification |
| Artifact model | LOCAL IMPLEMENTATION | Typed/versioned manifest, SHA-256 CAS, MIME, lazy manifest/blob split, Range and integrity tests |
| Resumable/S3 transfer | LOCAL IMPLEMENTATION | Out-of-order chunk sessions, missing status, parent concurrency, replica-safe S3 chunks, MinIO Compose E2E |
| Artifact E2EE/P2P | LOCAL IMPLEMENTATION | Per-chunk AES-GCM metadata/key versions plus authenticated resumable direct transfer and tamper cleanup tests |
| Identity/session | LOCAL IMPLEMENTATION | Account, Ed25519/X25519 device, revoke, short JWT, rotated refresh family, API key/scopes, OIDC/JWKS and SCIM tests |
| E2EE key hierarchy | LOCAL IMPLEMENTATION | Random Task keys, X25519 per-device wrapping, AES-256-GCM payload/Artifact encryption, rotation and revoked-recipient checks |
| Authorization | LOCAL IMPLEMENTATION | Task ACL administration, Artifact ACL, Organization/Team RBAC, ABAC deny precedence, capability/scope checks and cross-tenant tests |
| Audit/rate/abuse | LOCAL IMPLEMENTATION | Audit API/provenance, request IDs, scoped limits, bounded bodies, webhook SSRF defense and security tests |
| Invitations/QR | LOCAL IMPLEMENTATION | 256-bit one-time token, digest-at-rest, expiry/revoke/scope/nonce/signature/key reference, SVG renderer, replay tests and CLI QR output |
| Collaboration/social | LOCAL IMPLEMENTATION | Human/Agent participants, mentions, notifications, Task room identity, scoped contact graph, presence, leave/revoke and UI |
| Realtime | LOCAL IMPLEMENTATION | WebSocket notifications/presence/sync wake-up with cursor fallback and reconnect tests |
| Adapters/resume | LOCAL IMPLEMENTATION | Generic, Codex, Claude, CodeBuddy, Gemini, Cursor and OpenCode JSONL adapter contracts; Artifact capture, native Codex resume, provider-specific privacy filtering and vendor-neutral Rehydrate |
| Organizations/Teams | LOCAL IMPLEMENTATION | Membership/team lifecycle, workspace Tasks, RBAC/ABAC, audit, residency and enterprise plan/key policy |
| Developer platform | LOCAL IMPLEMENTATION | Go/Python/TypeScript/Swift/Kotlin/.NET SDKs, portable cross-language signatures, device runtime/offline sync, CLI stdio bridge, Codex plugin, Claude/CodeBuddy/Gemini/Cursor/OpenCode MCP templates, REST/OpenAPI, Connect/Protobuf, WebSocket, MCP tool subset, legacy custom JSON-RPC, OAuth and signed/retried webhooks |
| Billing/usage | LOCAL IMPLEMENTATION | Idempotent usage ledger, plan contracts, complete-window consumption pricing, immutable invoices, payment links, receipts and CSV/HTML exports |
| Payment adapters | LOCAL IMPLEMENTATION / EXTERNAL ACCEPTANCE REQUIRED | WeChat Native APIv3, Alipay RSA2 QR, PayPal Orders v2/capture, Stripe Checkout; signed callbacks, bounded order reconciliation, invoice CAS and replay safety; simulator and protocol HTTP tests. No real merchant credentials or real-money validation. See payments.zh-CN.md |
| Marketplace/P2 | LOCAL IMPLEMENTATION | Agent/capability/Task listings, installs, simulated buyer/seller transaction state/commission ledger; no verified collection or seller settlement |
| Web product | LOCAL IMPLEMENTATION | Chinese/English operations overview, Tasks, task Continuity detail, conversation, Artifacts/versions, DAG/conflicts, Agent integration catalog/participants, permissions, audit, Workspace, Security, Billing, Developer, Marketplace, notifications/offline state |
| Observability | LOCAL IMPLEMENTATION | Structured logs, W3C/OTLP traces, metrics, audit, Prometheus/Tempo/Grafana stack, ServiceMonitor and actionable alerts/runbooks |
| Four deployment modes | LOCAL IMPLEMENTATION | Memory dev, isolated Compose, Kubernetes dev/staging and HA production Helm values |
| Kubernetes production controls | LOCAL IMPLEMENTATION | Deployment/Service/Ingress/ConfigMap/Secret refs/SA/RBAC/HPA/PDB/NetworkPolicy/ServiceMonitor/migration/backup jobs, probes and graceful stop |
| Migrations/upgrades | LOCAL IMPLEMENTATION | Versions 001–015, advisory locking, idempotent up, rollback strategy and expand/contract docs/tests; payment downgrade preserves financial history |
| Backup/DR | LOCAL IMPLEMENTATION | Atomic checksummed DB+Artifact bundle, isolated restore guard/drill, PITR/versioning design and cloud-outage replay tests |
| CI/CD/supply chain | LOCAL IMPLEMENTATION | Go/Python/TypeScript CI plus schema/cloud/Helm/isolated PostgreSQL/JetStream gates; Swift/Kotlin/.NET have local self-tests. Workflow definitions exist for scans/SBOM/signing; remote green status must be checked per commit |
| Scale/chaos/security | LOCAL IMPLEMENTATION | 2/10/100 devices, offline durations/backlog, loss/duplicate/reorder/partition/crash/restart/storage timeout/skew/auth/tamper suites |
| Product/business/operations | LOCAL IMPLEMENTATION | Product/PRD/architecture/ADR, SLO, GTM, pricing hypothesis, metrics, risk register and required runbooks |

## External release gates

In addition to the code gaps above, these cannot be declared validated by a local workstation alone:

- customer Cloud KMS/HSM/BYOK custody and key-escrow ceremony;
- real enterprise OIDC/SCIM tenant interoperability and IdP lifecycle ownership;
- public DNS, TLS 1.3 ingress, WAF and externally reachable webhook delivery;
- managed PostgreSQL PITR, object-lock/versioning and multi-region failover;
- payment-provider authorization/settlement/tax flows;
- production canary approval, compliance audit and market/pricing validation.

Passing local gates qualifies the tested local scope only. External gates need a named target environment and recorded acceptance; industry conformance requires independent protocol clients/tests. See the dated audit for current status and priorities.
