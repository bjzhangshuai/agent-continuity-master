# Full Requirements Traceability

This is the completion gate for the master specification. `DONE` means the software-controlled behavior has code, automated tests and an executable deployment or operational path. Environment-owned services are listed separately and may not be called validated without real credentials and change authority.

| Area | State | Implementation and executable evidence |
|---|---|---|
| Universal Task State | DONE | Versioned Go/JSON Schema/Protobuf; Task, typed Events, Artifacts, branches, participants, Agent bindings, conflicts and snapshots |
| Ordering and delivery | DONE | Device sequence + causal DAG + HLC + server cursor; at-least-once, duplicate/idempotency/replay tests |
| Offline daemon | DONE | Crash-safe Outbox, bbolt Inbox/index/cursors, reconnect/backoff/jitter, replay, throttle, pause/resume, revoke response and doctor |
| Sync protocol | DONE | Signed hello/nonce, negotiation, head/cursor exchange, push/pull/ack, missing discovery, manifests and reconcile; unit/E2E |
| Reconciliation | DONE | Dedicated classifier for required conflict families, explicit strategies/resolution provenance, unit/E2E |
| DAG/fork/merge | DONE | Branch persistence/API/UI, merge parents/decision/provenance and snapshot projection tests |
| Snapshots | DONE | Event-log rebuild, topological projection, progress/decision/Artifact/issues and digest verification |
| Artifact model | DONE | Typed/versioned manifest, SHA-256 CAS, MIME, lazy manifest/blob split, Range and integrity tests |
| Resumable/S3 transfer | DONE | Out-of-order chunk sessions, missing status, parent concurrency, replica-safe S3 chunks, MinIO Compose E2E |
| Artifact E2EE/P2P | DONE | Per-chunk AES-GCM metadata/key versions plus authenticated resumable direct transfer and tamper cleanup tests |
| Identity/session | DONE | Account, Ed25519/X25519 device, revoke, short JWT, rotated refresh family, API key/scopes, OIDC/JWKS and SCIM tests |
| E2EE key hierarchy | DONE | Random Task keys, X25519 per-device wrapping, AES-256-GCM payload/Artifact encryption, rotation and revoked-recipient checks |
| Authorization | DONE | Task ACL administration, Artifact ACL, Organization/Team RBAC, ABAC deny precedence, capability/scope checks and cross-tenant tests |
| Audit/rate/abuse | DONE | Audit API/provenance, request IDs, scoped limits, bounded bodies, webhook SSRF defense and security tests |
| Invitations/QR | DONE | 256-bit one-time token, digest-at-rest, expiry/revoke/scope/nonce/signature/key reference, SVG renderer, replay tests and CLI QR output |
| Collaboration/social | DONE | Human/Agent participants, mentions, notifications, Task room identity, scoped contact graph, presence, leave/revoke and UI |
| Realtime | DONE | WebSocket notifications/presence/sync wake-up with cursor fallback and reconnect tests |
| Adapters/resume | DONE | Generic, Codex, Claude, CodeBuddy, Gemini, Cursor and OpenCode JSONL adapter contracts; Artifact capture, native Codex resume, provider-specific privacy filtering and vendor-neutral Rehydrate |
| Organizations/Teams | DONE | Membership/team lifecycle, workspace Tasks, RBAC/ABAC, audit, residency and enterprise plan/key policy |
| Developer platform | DONE | Go/Python/TypeScript/Swift/Kotlin/.NET SDKs, portable cross-language signatures, device runtime/offline sync, CLI stdio bridge, Codex plugin, Claude/CodeBuddy/Gemini/Cursor/OpenCode MCP templates, REST/OpenAPI, Connect/Protobuf, WebSocket, MCP, A2A, OAuth and signed/retried webhooks |
| Billing/usage | DONE | Idempotent usage ledger, plan contracts, complete-window consumption pricing, immutable invoices, payment links, receipts and CSV/HTML exports |
| Payment adapters | LOCAL IMPLEMENTATION / EXTERNAL ACCEPTANCE REQUIRED | WeChat Native APIv3, Alipay RSA2 QR, PayPal Orders v2/capture, Stripe Checkout; signed callbacks, bounded order reconciliation, invoice CAS and replay safety; simulator and protocol HTTP tests. No real merchant credentials or real-money validation. See payments.zh-CN.md |
| Marketplace/P2 | DONE | Agent/capability/Task listings, installs, paid gating, A2A buyer/seller transaction state/commission ledger |
| Web product | DONE | Chinese/English operations overview, Tasks, task Continuity detail, conversation, Artifacts/versions, DAG/conflicts, Agent integration catalog/participants, permissions, audit, Workspace, Security, Billing, Developer, Marketplace, notifications/offline state |
| Observability | DONE | Structured logs, W3C/OTLP traces, metrics, audit, Prometheus/Tempo/Grafana stack, ServiceMonitor and actionable alerts/runbooks |
| Four deployment modes | DONE | Memory dev, isolated Compose, Kubernetes dev/staging and HA production Helm values |
| Kubernetes production controls | DONE | Deployment/Service/Ingress/ConfigMap/Secret refs/SA/RBAC/HPA/PDB/NetworkPolicy/ServiceMonitor/migration/backup jobs, probes and graceful stop |
| Migrations/upgrades | DONE | Versions 001–010, advisory locking, idempotent up, rollback strategy and expand/contract docs/tests; payment downgrade preserves financial history |
| Backup/DR | DONE | Atomic checksummed DB+Artifact bundle, isolated restore guard/drill, PITR/versioning design and cloud-outage replay tests |
| CI/CD/supply chain | DONE | Format/vet/unit/race/integration/E2E/SDK, vuln/SAST/license, build/SBOM/container scan, provenance/signing and Helm release artifact |
| Scale/chaos/security | DONE | 2/10/100 devices, offline durations/backlog, loss/duplicate/reorder/partition/crash/restart/storage timeout/skew/auth/tamper suites |
| Product/business/operations | DONE | Product/PRD/architecture/ADR, SLO, GTM, pricing hypothesis, metrics, risk register and required runbooks |

## External release gates

These are not source-code gaps, but cannot honestly be declared validated by a local workstation:

- customer Cloud KMS/HSM/BYOK custody and key-escrow ceremony;
- real enterprise OIDC/SCIM tenant interoperability and IdP lifecycle ownership;
- public DNS, TLS 1.3 ingress, WAF and externally reachable webhook delivery;
- managed PostgreSQL PITR, object-lock/versioning and multi-region failover;
- payment-provider authorization/settlement/tax flows;
- production canary approval, compliance audit and market/pricing validation.

The current release is complete when the local validation report records green full/race/SDK/Compose/PostgreSQL/restore/Helm/Kubernetes gates. External gates require a named target environment, credentials and explicit authorization.
