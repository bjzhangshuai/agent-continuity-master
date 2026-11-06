# Production Deployment

The production values are a policy template, not a validated cloud deployment. Use a versioned signed image and verify its digest, external PostgreSQL/PITR, shared RWX/POSIX staging, TLS JetStream, versioned object storage, TLS ingress, external-secret ownership, explicit egress peers, audit export and durable metrics/traces. See [four-cloud delivery](multicloud-deployment.zh-CN.md).

Run at least three API replicas across failure domains with HPA, PDB, anti-affinity/topology spread and zero-unavailable rolling updates. Asynchronous archive mode must keep pending data in durable shared staging until confirmed archive receipt; it is not an expendable per-Pod cache. Direct S3 backend is a separate supported mode. PostgreSQL transaction locks remain the authority for Event heads, upload manifests, permissions and durable delivery records.

Release sequence:

1. verify checksummed DB/Artifact backup and isolated restore evidence;
2. run full/race/security/SDK/Compose gates and render all Helm environments;
3. execute additive migration under advisory lock;
4. deploy a compatible canary, run the complete smoke journey and inspect readiness, 5xx, sync lag, reconciliation/conflicts and Artifact hashes;
5. roll out gradually while retaining the previous image and compatible schema;
6. roll back application on failure—never automatically run destructive down SQL.

Cloud KMS/HSM, enterprise IdP, payment settlement, public DNS/TLS and multi-region failover require environment-specific credentials, operator approval and a recorded live drill before that environment is called production-qualified.
