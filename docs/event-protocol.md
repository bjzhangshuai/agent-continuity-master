# Event Protocol

Ordering combines four signals: a strictly unique `(task, device, device_sequence)`, causal `parent_event_ids`, a Hybrid Logical Clock for deterministic display, and an accepted `server_sequence` cursor. Server sequence does not rewrite causality.

Delivery is at least once. `(event_id)` and `(task, device, idempotency_key)` return the original accepted event; reusing a device sequence for a different event is a replay conflict. Payload SHA-256 is verified before the Ed25519 signature, and signature canonicalization excludes server-assigned fields.

Missing parents are accepted and recorded, not dropped. An event that does not consume an existing head creates another head and a branch conflict. A merge event names every merged head as a parent. Clients pull after a cursor and may request metadata without inline payloads.
