# ADR-003: PostgreSQL and Content-Addressed Artifact Storage

Status: Accepted.

PostgreSQL is transactional metadata/event truth. Artifact bytes remain outside rows and are addressed by verified SHA-256. Local dev uses a PVC filesystem; production replaces it with versioned S3-compatible storage without changing manifest semantics. Redis/NATS are omitted until a measured need exists.
