# ADR-006: Replica-safe resumable S3 chunks

Status: Accepted.

Production resumable chunks are stored immediately as immutable keys containing upload ID, chunk index and content digest. PostgreSQL stores the accepted manifest reference; assembly may run on any API replica and validates the final content hash. Per-Pod disk is staging/cache only, so S3 mode uses `emptyDir` rather than a shared RWO PVC. Local development retains filesystem chunks and a PVC. This avoids sticky-session correctness and permits rolling/HA replicas without changing the client protocol.
