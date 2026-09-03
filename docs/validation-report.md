# Local Release Validation Report

Initial validation: 2026-08-30; latest incremental validation: 2026-09-01 (Asia/Shanghai)\
Workspace: `/Users/jansurezhang/go/src/agent-continuity-master`\
Go toolchain: `go1.26.7 darwin/amd64`

## Result

All software-controlled release gates in the master specification passed locally. The verified deployment is Helm release `agent-continuity`, revision 8, in Rancher Desktop namespace `agent-continuity-dev`. The API and PostgreSQL Pods are Ready with zero restarts, retained PVCs remain Bound, and the database has all 9 migrations through `009_enterprise_identity`.

## Full-platform SDK validation — 2026-09-01

Revision 8 adds the portable `ac1` signature format while preserving existing Go JSON signatures. Go verifies committed Python and TypeScript multilingual signing vectors. Swift for iOS/macOS, Kotlin for Android/JVM and .NET for Windows/Linux/macOS each generate Ed25519/X25519 device identities, sign Events and Sync Hello messages, persist offline state, register and bind Agents, retrieve Continuity Packages, and run cursor push/pull/ack.

- `go test ./...` and `go vet ./...` passed after the protocol and MCP bridge changes.
- `make test-sdk` passed Python unittest, TypeScript compiler/Node tests, Swift build/self-test, Kotlin Gradle tests and .NET build/self-test.
- The Codex/ChatGPT plugin passed the official local plugin validator; Claude, CodeBuddy, Cursor, Gemini CLI and OpenCode MCP templates use the tested `agentcloud mcp stdio` bridge without storing a Token in configuration.
- Rancher Desktop cross-device E2E passed: a Python device registered and pushed a Chinese event; a TypeScript tablet pulled it, bound a CodeBuddy Agent and pushed a causal continuation. The server stored two Events and MCP `tools/list` returned `continuity.get`.
- Standard Kubernetes `TestFullContinuityJourney` passed after deployment. Revision 8 API/PostgreSQL Pods are Ready with zero restarts, both 1 Gi PVCs remain Bound, and the default kube context remains `cls`.

## Incremental product validation — 2026-08-31

The bilingual operations and mainstream-Agent release is deployed as Helm revision 7. The console defaults to Chinese and switches to English without reload. Its operations overview reports real Task, Agent, device and notification data, and its integration catalog exposes `codex`, `claude`, `codebuddy`, `gemini`, `cursor`, and `opencode`.

- Full `go test ./... -count=1`, `go vet ./...`, JavaScript syntax, seed-script syntax and Helm lint/render passed.
- Relevant changed packages passed the race detector; the earlier release-wide race run also remains green.
- Isolated PostgreSQL/MinIO Compose E2E passed after adding a PostgreSQL assertion for Agent participants and Task bindings.
- Rancher Desktop Kubernetes smoke passed against revision 7; the new API Pod and retained PostgreSQL Pod are Ready with zero restarts and both PVCs remain Bound.
- Real browser validation passed for Chinese default rendering, English/Chinese switching, all six provider cards, 319-pixel responsive layout without page overflow, and zero console warnings/errors.
- `scripts/seed-local-demo.sh` was executed twice to prove repairable/idempotent behavior. The local demo account contains one Organization, six Agent profiles, five operations Tasks, one billing account and three published marketplace listings. Every demo Task has two Agent bindings, one verified Artifact, and five or six captured Events.

During live seeding, two environment-only gaps were found and fixed: PostgreSQL Agent participants now keep `account_id` null after ownership validation so they do not collide with the human Task owner, and CLI adapter source matching canonicalizes macOS `/var`/`/private/var` paths. Both fixes have regression coverage.

## Automated validation

| Gate | Result | Evidence |
|---|---|---|
| Format/static syntax | PASS | `gofmt -l`, `node --check`, JSON Schema via `jq`, source and rendered YAML parsing |
| Go static analysis | PASS | `go vet ./...` |
| Go test suite | PASS | `GOMAXPROCS=4 go test ./... -count=1`; 33 packages including unit, compatibility, chaos, performance, simulation, integration package and E2E package |
| Race detector | PASS | `GOMAXPROCS=2 go test -race -p=1 ./... -count=1` |
| SDK contracts | PASS | Go, Python, TypeScript, Swift, Kotlin/Android and .NET device identity, portable signing, offline queue, sync, Agent binding and auth/continuity contracts |
| OpenAPI | PASS | All 125 non-static HTTP operations registered by the server are documented; no stale operations |
| Reachable vulnerabilities | PASS | `govulncheck v1.7.0`: 0 reachable vulnerabilities; two dependency-tree findings are not called by this code |
| SAST | PASS | `gosec v2.22.8` run package-by-package over all 33 packages; 0 failures/timeouts |
| Dependency licenses | PASS | 44 actually imported third-party modules have discoverable LICENSE/COPYING/NOTICE files; project license is Apache-2.0 |
| Binary/container build | PASS | server, daemon and CLI binaries built; multi-stage non-root container image built in Rancher Desktop |
| Helm | PASS | dev, staging and production lint/render; rendered YAML parses; unsafe local-PVC multi-replica/HPA configuration fails closed; staging/production render without Artifact PVC; production renders migration Job/HPA/PDB/NetworkPolicy/ServiceMonitor |
| Compose E2E | PASS | Isolated PostgreSQL + MinIO + API stack; `TestFullContinuityJourney` passed and temporary project/volumes were removed |
| PostgreSQL integration | PASS | migrations are idempotent; transactional Events, deduplication, rotated refresh tokens, API keys, E2EE envelopes, branches/snapshots, chunk metadata, Task ACL, billing/marketplace and connection recovery passed |
| Backup/restore drill | PASS | checksummed PostgreSQL custom dump and Artifact archive restored into isolated targets; 9 migration rows, Task/Event rows and Artifact bytes matched |
| Kubernetes release smoke | PASS | New immutable local image ID forced a new Pod UID; standard journey and Python→TypeScript/CodeBuddy SDK E2E passed against revision 8 through loopback port-forwards |
| Browser UI | PASS | Deployed console loaded without console warnings/errors; 8 sections navigated; empty-auth guidance verified; 390×844 view had no page-level horizontal overflow |

The full E2E journey verifies two signed/encryption-capable devices, causal and duplicate Event handling, conflict resolution, Task-key envelopes, sync pull/ack/reconcile, out-of-order interrupted/resumed Artifact upload with hash verification and lazy download, signed one-time QR invitation, scoped access, invitation replay rejection, direct Task ACL revocation and device revocation.

## Kubernetes safety evidence

- Every mutation used `kubectl --context rancher-desktop` or Helm's explicit Rancher Desktop context.
- The script verified the API server was loopback and the node identity was Rancher Desktop before writing.
- The pre-existing namespace Secret and both retained PVCs were preserved; only the missing `master-key` field was added to the old Secret.
- The user's default `kubectl` context was `cls` before and after all operations and was never written to.
- Temporary Compose/PostgreSQL containers, test volumes, browser tabs and port-forwards were cleaned up.

## External release gates not claimed

These require customer infrastructure, credentials or operator/commercial authority and were not simulated as completed: real Cloud KMS/HSM/BYOK custody, enterprise IdP tenant interoperability, public DNS/TLS/WAF, managed PostgreSQL PITR/object lock/multi-region failover, payment settlement/tax handling, hosted GitHub Actions execution and production canary approval/compliance audit. Their code paths, configuration contracts and CI/CD gates are present, but a named external environment must pass a separately recorded live drill before being called production-qualified.
