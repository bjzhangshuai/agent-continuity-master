# Roadmap and Delivery Status

All software-controlled P0, P1 and P2 capabilities named by the master specification are implemented in this repository and gated by the requirement matrix. This includes the causal/offline core and three demos, DAG/conflict/snapshot UI, presence and collaboration, Organizations/Teams/RBAC/ABAC, advanced E2EE key sharing, metering, agent/capability/task marketplace contracts, Agent-to-Agent transaction ledger, enterprise identity/private-deployment controls and direct P2P Artifact transfer.

The next engineering work is evolution, not deferred core functionality:

- increase Adapter coverage only when vendors expose stable supported APIs;
- calibrate SLOs using production-like load and partition Event storage when measurements require it;
- add managed multi-region placement and event-bus extraction without changing cursor/Event semantics;
- obtain independent security/compliance review and exercise customer KMS/HSM, IdP and private-cloud environments;
- validate pricing, conversion and marketplace liquidity with real users before changing commercial limits.

External validation is deliberately not represented as code completion. Public DNS/TLS, managed PostgreSQL/PITR, Cloud KMS/HSM custody, enterprise OIDC/SCIM tenants, payment settlement, app-store policy and production canary infrastructure require operator-provided accounts and change authority.
