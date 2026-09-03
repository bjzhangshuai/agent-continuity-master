# Architecture

The server is a modular monolith because Event acceptance, permission evaluation, idempotency and causal-head updates require one transactional boundary. Modules communicate through storage, Artifact, policy, identity, webhook and observability interfaces, so measured scaling pressure can extract workers without changing the public protocol.

```mermaid
flowchart LR
  AD[Codex / Generic Adapter] --> LD[Local Daemon]
  CLI[CLI / SDK / Web] --> API[REST + Connect + MCP + A2A]
  LD --> API
  API --> IAM[Auth / SSO / SCIM / Policy]
  API --> CORE[Task / Event / DAG / Reconcile]
  API --> COL[Collaboration / Realtime]
  API --> ART[Artifact / E2EE]
  API --> DEV[OAuth / Webhook / Billing / Marketplace]
  IAM --> PG[(PostgreSQL)]
  CORE --> PG
  COL --> PG
  DEV --> PG
  ART --> OBJ[(Local CAS or S3)]
```

```mermaid
flowchart TB
  subgraph Device
    A[Adapter SDK] --> O[Crash-safe Outbox]
    O --> S[Sync Engine]
    S --> I[bbolt Inbox / Task Index / Cursor]
    I --> C[Artifact Cache + Keyring]
  end
  subgraph Cloud
    H[Versioned HTTP Boundary] --> M[Domain Modules]
    M --> ST[Storage Interface]
    M --> AS[Artifact Interface]
    M --> RT[WebSocket Wake-up]
    ST --> P[(PostgreSQL)]
    AS --> B[(S3/CAS)]
  end
  S --> H
```

PostgreSQL is the authority for identity, permissions, metadata, Events, cursors, conflicts and durable webhook delivery state. The database Event Log is also the reconstructable durable source behind notifications, so an external event bus is optional rather than a correctness dependency. Artifact bytes remain outside relational rows.

S3 mode stores resumable chunks as immutable hash-addressed temporary objects, allowing any API replica to continue or assemble an upload. Per-pod disk is only an expendable cache; local mode uses a retained PVC. Realtime is never authoritative. E2EE payloads and Artifact ciphertext are opaque to the server.

Production uses external PostgreSQL/PITR, versioned object storage, TLS ingress, external secrets, three or more stateless API replicas, HPA/PDB/topology controls, explicit egress policy and managed telemetry. The in-chart PostgreSQL exists only for local/dev acceptance.
