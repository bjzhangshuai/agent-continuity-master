# ADR-005: Client-side Task and Artifact E2EE

Status: Accepted.

Devices use Ed25519 for authenticity and X25519 ephemeral agreement to wrap random versioned Task keys. Event payloads and Artifact chunks use AES-256-GCM with identity-bound associated data and plaintext/ciphertext digests. The server stores ciphertext and one envelope per authorized active device; it never receives device private keys. Rotation produces a new Task-key version and excludes revoked devices. This preserves portable collaboration while making server compromise insufficient to read protected content.
