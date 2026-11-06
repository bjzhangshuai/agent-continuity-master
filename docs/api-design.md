# API Design

`api/openapi.yaml` is the REST contract. `proto/task_state.proto` defines the Connect/gRPC shape. `/v1/mcp` exposes a pinned tool subset; `/v1/a2a` and its Agent Card are a legacy custom gateway, not current A2A conformance. Discover the explicit [project contract](protocol-profile-v1.md) at `/.well-known/agent-continuity.json`. Authenticated routes enforce token scope and resource permission.

The API groups are auth/session, API keys/audit/notifications, devices/agents, Tasks/events/DAG/conflicts/snapshots, collaboration/presence/realtime/E2EE keys, Artifacts/chunks/versions/ACL, invitations/QR, Organizations/Teams/policy, OAuth/webhooks, usage/billing/marketplace, sync/reconciliation, SSO and SCIM. Error envelopes are `{error:{code,message}}`; SCIM uses RFC-style SCIM error shapes and Connect uses Connect-compatible codes.

Important semantics:

- `server_sequence` is a stable cursor, never a replacement for causal parents.
- Duplicate Event IDs or idempotency keys return the original result only when task/account/device/branch/type/digest match; cross-tenant collisions and content substitutions are rejected without disclosing the prior payload.
- Task indexes, sync without payloads and Artifact lists contain metadata/manifests only. Blobs are fetched explicitly and support `Range`.
- Continuity Packages internally read all Event pages into one response, apply Artifact ACLs and include branches/participants/bindings/conflicts/snapshots. Public package pagination is still a scalability gap; it is not the same as cursor-paginated Event sync.
- WebSocket messages advertise availability/presence. Cursor pull remains authoritative after disconnect or missed notification.
- QR creation accepts a device-signed short-lived reference and returns a self-contained SVG; the QR contains no Task plaintext or long-lived credential.

Backward-compatible additions are optional fields, routes and event types. Required-field changes, signature canonicalization changes or semantic reinterpretation require a new major protocol version.
