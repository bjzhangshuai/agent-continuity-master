# Agent Continuity

本地 CodeBuddy 插件、MinIO、跨设备和支付演示的查看步骤见
[CodeBuddy 本地闭环实验](docs/codebuddy-local-e2e.zh-CN.md)。

原路退款、票据、跨平台安装器与发行包见 [退款与交付](docs/refunds-and-delivery.zh-CN.md)。

Agent Continuity is a vendor-neutral persistent Task State and collaboration platform. A Task—not a chat transcript—is the durable unit: identity, objective, signed event DAG, snapshots, artifacts, participants, agents, permissions, audit and security context survive device, model, agent and network changes.

## Delivered capabilities

- Multi-cloud archival: transactional Outbox → NATS JetStream (Argo Events ecosystem) → independent workers → AWS S3 / Alibaba OSS / Tencent COS / Azure Blob / self-hosted S3. Includes status, retry/dead-letter replay, tenant-attributed idempotent archive metering and verified private downloads. See [configuration, SDKs, operational limits and cloud acceptance status](docs/object-storage-archive.md).
- Archive closure: [production Charts, preflight, lifecycle, storage billing, KEDA and opt-in cloud probes](docs/archive-production.md). Local demonstration is not live cloud or production HA acceptance.
- Local S3 testing: [TLS MinIO, safe local deployment, demo walkthrough and optional Chart dependencies](docs/minio-local-and-chart-dependencies.md). Existing filesystem archives remain readable.

- Causal Task/Event core: Ed25519 device identity, per-device sequence, HLC, causal parents, server cursor, append-only provenance, idempotency and replay protection.
- Offline sync: signed hello/capability negotiation, crash-safe Outbox and bbolt Inbox/index/cursors, push/pull/ack, retry with jitter, bandwidth limits, pause/resume and diagnostics.
- Reconciliation: missing-parent, concurrency, semantic, Artifact, permission, delete/update and branch conflict classification; explicit resolution, fork/merge and verifiable snapshot projection.
- Artifact plane: manifest-only indexes, SHA-256 CAS, MIME metadata, versions, resumable chunks, Range/lazy fetch, local/S3 storage, E2EE and direct authenticated P2P transfer.
- Security and enterprise: short access tokens, rotated refresh sessions, API keys/scopes, task and Artifact ACLs, RBAC/ABAC, audit, rate limits, X25519/AES-256-GCM E2EE, QR invitations, SSO/OIDC, SCIM, BYOK/KMS/HSM policy contracts and data residency.
- Collaboration and platform: agents, participants, mentions, notifications, presence, WebSocket wake-up, Organizations/Teams, OAuth clients, webhooks, MCP, A2A, Connect, Go/Python/TypeScript/Swift/Kotlin/.NET SDKs, usage/billing and marketplace transaction ledger.
- Product and operations: CLI, local daemon, Codex, Claude, CodeBuddy, Gemini, Cursor, OpenCode and Generic adapters, bilingual Chinese/English operations console, PostgreSQL migrations, Docker Compose, Helm, observability stack, backup/restore drill, CI/CD contracts and release smoke tests.
- Finance: existing consumption, invoices, payment links and verified receipts are retained. An operator-only collections/payouts workflow adds settlement, own-account destinations, reservations, approval, payout results, returns and exports. Local flows are simulated; optional Stripe bank-payout protocol support is disabled by default and has not undergone merchant/live acceptance. See [payment collection](docs/payments.zh-CN.md) and [treasury capabilities and limits](docs/treasury.zh-CN.md).
- Multi-tenancy: password/session login, tenant hierarchy, separate scoped platform delegation and configured superadmins, explicit bilateral group-payment mandates, tenant-wide usage invoices and account-authorized checkout. Management hierarchy does not imply tenant data or payment access. See the [Chinese integration and demo guide](docs/multitenancy.zh-CN.md) and [local validation record](docs/multitenancy-validation-2026-09-03.md). This is not a prepaid wallet, tax-invoicing system, or merchant onboarding service.

## Verification

```sh
make bootstrap
make lint
make test
make test-race
make test-sdk
make test-device-sdk
make test-e2e
make helm-lint
```

For local Kubernetes, both scripts are hard-guarded to the loopback Rancher Desktop cluster and never change the default `kubectl` context:

```sh
make deploy-dev
make smoke-test
```

The deployment creates or upgrades only `agent-continuity-dev`, preserves the existing Secret and retained PVCs, waits for migrations and rollout, then executes the full continuity journey.

Full iPhone/iPad/macOS, Android/JVM, Web/Node, Python, Go, and Windows/.NET integration guidance is in [`docs/device-agent-sdk.zh-CN.md`](docs/device-agent-sdk.zh-CN.md). Codex/ChatGPT plugin packaging and Claude, CodeBuddy, Cursor, Gemini CLI, and OpenCode MCP templates live under `plugins/` and `integrations/`.

Seed five non-sensitive management and operations scenarios into the local release with:

```sh
./scripts/seed-local-demo.sh
```

The script is guarded to the loopback Rancher Desktop cluster, reuses records marked with its demo seed, and creates a local console credential under the normal `agentcloud` state directory.

Start with [product definition](docs/product-definition.md), [architecture](docs/architecture.md), [protocol](docs/protocol.md), [security](docs/security.md), [requirements traceability](docs/requirements-traceability.md), [the executed validation report](docs/validation-report.md), [development](docs/development.md) and the [OpenAPI contract](api/openapi.yaml).
