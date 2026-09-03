# Universal Task State Schema

Schema and protocol version `1.0` are defined in `api/schemas/task-state-v1.schema.json`, `proto/task_state.proto` and `pkg/protocol`. The aggregate includes capability negotiation, Task identity/organization/team/objective/status/metadata, complete typed Event history, Artifact manifests, branches, participants, Agent bindings, conflicts and digest-verified snapshots.

Events carry producer/account/participant/Agent/provider/device/session provenance, branch and parent DAG, HLC, device and server sequence, type/schema/content/digest, payload or references, Artifact refs, client/server times, Ed25519 signature, idempotency key and extension namespace. Conversation, inputs/outputs, tool calls/results, decisions, constraints, progress and unresolved issues are event families projected into snapshots rather than lossy mutable columns.

Compatibility rules:

- Additive optional fields, capabilities and Event types are minor-compatible.
- Clients preserve unknown namespaced extensions and ignore capabilities they did not negotiate.
- Stored Events remain immutable with their original schema version.
- Required-field removal, semantic reinterpretation, canonical-signature changes or digest algorithm changes require a new major version.
- Snapshots accelerate resume/read paths and are rebuildable from the Event Log; their state digest and Event count are verified before use.

Vendor metadata belongs in `extensions` or binding metadata and may never alter portable core semantics. Continuity Packages advertise included capabilities so an adapter can choose Native Resume, portable Rehydrate or an explicit unsupported result.
