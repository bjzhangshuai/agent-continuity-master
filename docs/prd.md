# Product Requirements

## Product promise

A user can continue a long-lived Task across time, devices, agents, models and collaborators without copying a transcript, losing files, hiding conflicts or weakening access control. Native vendor resume is preferred only when compatibility is proven; otherwise a complete portable Continuity Package rehydrates another Agent.

## Required journeys

1. A new device sees the lightweight Task index immediately, then lazily pulls selected Events and Artifact blobs.
2. An offline Agent appends signed work to a crash-safe Outbox; after reconnect, duplicate/reordered delivery converges through cursor sync and reconciliation.
3. Two Agents fork concurrent work, preserve both branches, compare provenance and explicitly merge or resolve conflicts.
4. A device uploads an interrupted, encrypted Artifact and another device resumes/downloads it with matching hashes.
5. A user previews a cross-Agent package containing objective, conversation/tool/output Events, decisions/progress/issues, DAG, bindings and Artifact manifests, then Native Resumes or Rehydrates.
6. User A creates a signed QR invitation; User/Agent B joins with scoped access, collaborates through presence/notifications, and loses access immediately when permission/device is revoked.
7. An enterprise provisions users/teams through OIDC/SCIM, applies RBAC/ABAC/residency/key policy, audits actions and meters usage.

## Acceptance invariants

- No core state uses timestamp last-write-wins; valid concurrent work is retained with provenance.
- Duplicate delivery applies exactly once; causal heads and snapshots rebuild deterministically.
- Task indexes and Artifact manifests never embed blobs; payload fetch is explicit.
- Artifact/chunk/plaintext/ciphertext hashes match before publication.
- E2EE private keys remain on devices; the server stores only wrapped Task keys and ciphertext.
- Read grants cannot write; Artifact ACL can narrow Task access; owner ACL cannot be removed.
- Invitations are high entropy, short-lived, one-time, revocable and cannot grant admin.
- Realtime loss is repaired by signed hello and cursor pull.
- Compose and Kubernetes execute the same release smoke journey; backup restore is proven, not assumed.

## Experience and surfaces

The responsive Web console and CLI/local daemon are the implemented desktop experience. The Web information architecture covers Home, Tasks, Agents, Devices, Collaborations/Contacts, Organizations, Artifacts, Notifications, Security, Billing, Developer and Marketplace. Task Detail covers overview/metadata, timeline/activity/conversation, Artifacts/versions, Agents/participants, branches/conflicts, permissions/audit and resume/share previews. Mobile uses the same REST/Connect protocol and QR deep-link contract; a native mobile binary is outside MVP implementation scope but no protocol assumes desktop filesystem access.

## Success and commercial metrics

North Star: Successfully Continued Tasks. Supporting signals are resume/cross-agent success, weekly continued Tasks, invitation conversion, collaboration, active devices/Agents, Task completion/retention, Artifact volume, conflict and auto-resolution rates, latency/error SLOs and unit economics. Pricing values are hypotheses until real willingness-to-pay tests are complete.
