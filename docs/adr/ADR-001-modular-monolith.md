# ADR-001: Modular Monolith

Status: Accepted.

The P0 server is a Go modular monolith. Event acceptance, permissions, idempotency and head updates require one transaction and the local workload does not justify distributed queues. Modules depend on storage and Artifact interfaces, preserving later extraction. Revisit when independent scaling or team ownership demonstrably outweighs distributed consistency cost.
