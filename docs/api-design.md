# API Design

`api/openapi.yaml` is the versioned REST contract. `proto/task_state.proto` defines the compatible Connect/gRPC shape; `/v1/mcp`, `/v1/a2a` and the Agent Card expose standard agent integration surfaces. All authenticated routes enforce both token scope and resource permission.

The API groups are auth/session, API keys/audit/notifications, devices/agents, Tasks/events/DAG/conflicts/snapshots, collaboration/presence/realtime/E2EE keys, Artifacts/chunks/versions/ACL, invitations/QR, Organizations/Teams/policy, OAuth/webhooks, usage/billing/marketplace, sync/reconciliation, SSO and SCIM. Error envelopes are `{error:{code,message}}`; SCIM uses RFC-style SCIM error shapes and Connect uses Connect-compatible codes.

Important semantics:

- `server_sequence` is a stable cursor, never a replacement for causal parents.
- Duplicate Event IDs or idempotency keys return the original acceptance result; sequence reuse with different work is rejected.
- Task indexes, sync without payloads and Artifact lists contain metadata/manifests only. Blobs are fetched explicitly and support `Range`.
- Continuity Packages paginate the complete Event Log, apply Artifact ACLs and include capabilities, branches, participants, Agent bindings, conflicts and verified projected snapshots.
- WebSocket messages advertise availability/presence. Cursor pull remains authoritative after disconnect or missed notification.
- QR creation accepts a device-signed short-lived reference and returns a self-contained SVG; the QR contains no Task plaintext or long-lived credential.

Backward-compatible additions are optional fields, routes and event types. Required-field changes, signature canonicalization changes or semantic reinterpretation require a new major protocol version.
