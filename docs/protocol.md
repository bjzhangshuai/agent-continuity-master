# Protocol Overview

Protocol `1.0` is a project-owned transport-neutral contract comprising Universal Task State JSON Schema/Protobuf, signed Event and sync envelopes, causal reconciliation, Artifact separation, E2EE keys, collaboration and Adapter resume semantics. REST/Connect, an MCP tool subset and a legacy custom JSON-RPC gateway share the Task/permission model. This does not establish full current MCP/A2A compatibility. See [Profile 1 and conformance boundaries](protocol-profile-v1.md).

Clients negotiate versions/capabilities, preserve namespaced extensions, apply Events idempotently, stage pulls durably, verify producer signatures/digests and repair missed realtime notifications with cursor pull. Task/Artifact IDs, Event causality, branch/merge provenance, key versions and ACLs cannot be reinterpreted by a transport Adapter.

Continuity Packages carry the complete readable Task Event history plus capabilities, Artifact manifests filtered by ACL, branches, participants, Agent bindings, conflicts and current digest-verified snapshots. An Adapter uses a compatible native session only after capability detection; otherwise it Rehydrates this portable package without claiming private runtime equivalence.

Concrete contracts are in `api/openapi.yaml`, `api/schemas/task-state-v1.schema.json`, `proto/task_state.proto`, `pkg/protocol`, `docs/event-protocol.md`, `docs/sync-protocol.md`, `docs/artifact-protocol.md` and `docs/adapter-protocol.md`.
