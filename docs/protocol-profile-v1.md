# Agent Continuity Profile 1

Status: project-owned integration contract, revision 2026-11-07. This is not an industry standard or certification. Changes preserve AC-EVENT-1 / AC-SYNC-HELLO-1 signatures and existing protocol 1.0 wire messages.

## Discovery and contracts

`GET /.well-known/agent-continuity.json` is public and contains no tenant data or credentials. Follow its same-origin `schemas` and `openapi` paths. Unknown schema names return 404. Task content, usage, bills and audit still require their normal scopes/tenant permissions.

The package schema accepts historical `null` encodings for empty collections where documented. Consumers treat these as empty collections. Do not normalize signed legacy Event bytes before verifying their documented signature encoding; portable AC-EVENT-1 is preferred for cross-language producers.

## Normative integration rules

| Area | Required behavior |
|---|---|
| Identity | Authenticate account; register device; keep private keys local; check account/task/org authorization on every operation |
| Capture | Obtain explicit capture consent; map conversation, tools, decisions and artifacts to Task events; exclude private reasoning and credentials |
| Ordering | Preserve parent IDs and branch IDs; HLC is display/tie-break information; server sequence is a cursor, not causal ancestry |
| Retry | Persist event ID, idempotency key and device sequence before sending; replay the same signed event; never create a new ID for a transport retry |
| Sync | Signed hello/version/capability intersection; durable local inbox before ack; websocket loss repaired by cursor pull |
| Artifacts | Upload bounded/chunked content; verify digest; preserve manifest and archive receipt; do not call queued content archived until job success |
| Collaboration | Scoped invitation and participants; preserve conflicts and provenance; do not silently overwrite another branch |
| Resume | Export complete readable context or selected manifests; advertise native resume only after compatible runtime detection; otherwise rehydrate |
| Usage | Server records one accepted event as one `api.mutation` operation in the event transaction; duplicate/failed transaction adds zero; payer is Task owner or organization, actor retained in metadata |
| Audit | Signed Event history is device provenance. HTTP AuditEvent includes actor/action/resource/time/request correlation, but currently its write is best-effort after business state. Do not claim lossless financial compliance audit |
| Finance | Integer minor units; use stable idempotency keys for issue/pay/refund; only verified provider success changes payment/refund state; original invoice/receipt immutable; credit note is not a tax invoice |
| Compatibility | Additive fields only within a compatible version; preserve unknown extensions; unsupported required behavior must be surfaced to the host, not silently assumed |

Event metering starts with this release. Previously accepted events are not retroactively charged or reassigned; issued invoices remain immutable. Non-event mutation accounting and artifact upload metering still have separate post-commit paths; do not extrapolate the event atomicity guarantee to every meter. This platform does not verify a model vendor's token consumption simply because an SDK supplies metadata.

## Relationship to other protocols

| Layer | Existing ecosystem | Current implementation |
|---|---|---|
| Agent tools | MCP | HTTP/stdio tool subset pinned to 2025-06-18; current vendor revisions need separate conformance work |
| Agent delegation | A2A | `/v1/a2a` legacy custom signed-event gateway; not A2A 1.0 compatible |
| Durable offline task continuity | This profile | Task/Event/DAG/Artifact/device identity/sync/rehydration |
| Telemetry | OpenTelemetry | OTLP/W3C tracing and metrics; full GenAI semantic convention mapping not claimed |
| Consumption and cash | This profile + provider APIs | Usage/invoice/payment/refund/credit-note contracts; no generic cross-merchant settlement protocol |

Official references: [A2A 1.0 specification](https://a2a-protocol.org/v1.0.0/specification/), [MCP versioning](https://modelcontextprotocol.io/specification/versioning), [OpenTelemetry conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/). A future A2A bridge should use standard Message/Task/Artifact and versioned extensions for signed Continuity envelopes, preserve auth/cancellation/errors, and pass independent client tests. Renaming an existing endpoint is not conformance.

## Conformance and evolution

Run `python scripts/test-protocol-contracts.py` with jsonschema 4.26.0. It exercises REST, sync, MCP and legacy JSON-RPC against the server, validates emitted packages/usage/audit, and rejects invalid envelopes. It is a Continuity contract suite, not the MCP/A2A industry's certification suite.

SDK authors also run portable signature vectors in `pkg/protocol/portable_test.go` and each SDK's tests. New Agent adapters implement Detect/Discover/Capture/Resume and pass the shared adapter compatibility suite; add real-app version evidence before listing native support. Release contract changes with a changelog, pinned schemas, cross-language tests and backward-compatible migration. Independent vendors can propose RFCs and conformance fixtures; ecosystem adoption and governance remain future work.
