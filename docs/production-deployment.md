# Production Deployment

The production values are an executable policy profile, not credentials. Replace the example image/tag and endpoints with an immutable signed digest, external PostgreSQL/PITR, versioned/object-locked S3, TLS 1.3 ingress, external-secret ownership, explicit egress CIDRs, audit export and durable metrics/traces.

Run at least three API replicas across failure domains with HPA, PDB, anti-affinity/topology spread and zero-unavailable rolling updates. S3 mode keeps resumable chunks in hash-addressed objects, so requests may land on any replica; Pod disk is an expendable cache. PostgreSQL transaction locks remain the authority for Event heads, upload manifests, permissions and webhook delivery.

Release sequence:

1. verify checksummed DB/Artifact backup and isolated restore evidence;
2. run full/race/security/SDK/Compose gates and render all Helm environments;
3. execute additive migration under advisory lock;
4. deploy a compatible canary, run the complete smoke journey and inspect readiness, 5xx, sync lag, reconciliation/conflicts and Artifact hashes;
5. roll out gradually while retaining the previous image and compatible schema;
6. roll back application on failure—never automatically run destructive down SQL.

Cloud KMS/HSM, enterprise IdP, payment settlement, public DNS/TLS and multi-region failover require environment-specific credentials, operator approval and a recorded live drill before that environment is called production-qualified.
