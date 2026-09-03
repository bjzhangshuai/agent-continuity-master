# Configuration

The server fails fast on missing or inconsistent critical configuration. Sensitive values may be supplied directly or through the corresponding `*_FILE` path, but never both. File content is trimmed and intended for Kubernetes projected Secrets, External Secrets or Vault agents.

| Area | Variables |
|---|---|
| Core | `CONTINUITY_LISTEN_ADDRESS`, `CONTINUITY_STORE`, `CONTINUITY_DATABASE_URL[_FILE]`, `CONTINUITY_AUTO_MIGRATE`, `CONTINUITY_MAX_BODY_BYTES`, `CONTINUITY_SHUTDOWN_GRACE` |
| Secrets | `CONTINUITY_JWT_SECRET[_FILE]` (≥32), `CONTINUITY_BOOTSTRAP_TOKEN[_FILE]` (≥24), `CONTINUITY_MASTER_KEY[_FILE]` (base64url 32 bytes) |
| Artifact | `CONTINUITY_ARTIFACT_DIR`, `CONTINUITY_ARTIFACT_BACKEND=local|s3`, S3 endpoint/bucket/region/access/secret/session-token file variants and controlled auto-create |
| Enterprise | `CONTINUITY_DATA_RESIDENCY`, OIDC issuer/audience/JWKS file/Organization/auto-provision, SCIM bearer-token file/Organization |
| Observability | `CONTINUITY_SERVICE_NAME`, full OTLP/HTTP traces URL in `CONTINUITY_OTLP_TRACES_ENDPOINT` |

OIDC issuer must be absolute HTTPS and issuer/audience/JWKS are configured together. SCIM requires a 32+ character bearer and Organization. S3 requires an HTTP(S) origin, bucket and credentials. A configured deployment residency is immutable for Organization placement in that deployment.

CLI/daemon variables are `AGENTCLOUD_API_URL`, `AGENTCLOUD_TOKEN` or local rotated credentials, `AGENTCLOUD_ACCOUNT_ID`, `AGENTCLOUD_STATE_DIR` and `AGENTCLOUD_SYNC_INTERVAL`. Device signing/encryption keys, Task keyring, local database/cache and credentials are stored under a mode-0700 state root with mode-0600 sensitive files.

Chart defaults contain no real secret. Production supplies an existing Secret with database URL, JWT/bootstrap/master keys and optional S3/OIDC/SCIM material; external-secret controllers can own that Secret without chart changes.
