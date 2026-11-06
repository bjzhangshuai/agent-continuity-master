# MinIO chart local test fork

Source: https://charts.min.io/helm-releases/minio-5.4.0.tgz
Upstream archive SHA256: `25fa2740480d1ebc9e64340854a6c42d3a7bc39c2a77378da91b21f144faa9af`.

Version `5.4.0-continuity.1` changes only the standalone Deployment: startup,
readiness and liveness health probes; disable automatic Kubernetes API token mount.
The enclosing infrastructure chart disables default users, bounds CPU/memory/PVC,
enables TLS, provisions private buckets and restricts the application's identity.
Do not use upstream distributed defaults as a production deployment profile.

The upstream community repository is archived (2026-04-25). This pinned artifact is
for isolated local S3 tests; evaluate a maintained object-store distribution and
its security updates before production. S3-compatible endpoint configuration is
not tied to MinIO. MinIO server is licensed under AGPLv3.
