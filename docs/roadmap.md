# Roadmap and Delivery Status

The causal/offline core, collaboration data model, adapters/SDKs, local archival, billing and simulated payment/refund journeys are implemented. This is not completion of every P0/P1/P2 commercial requirement. The [2026-11-07 review](product-architecture-review-2026-11-07.zh-CN.md) replaces the previous all-complete claim.

Remaining engineering work, ordered by release risk:

- implement/test current A2A and MCP wire contracts using independent clients; the existing legacy A2A gateway is custom;
- connect marketplace orders to verified collection/refund/seller settlement, or keep paid marketplace operation disabled;
- add real tax invoicing, cloud KMS/workload identity, and transactional audit/non-event accounting where promised;
- measure client-confirmed successful continuation and tenant unit economics rather than demo metadata;
- add an explicit execution/lease/budget layer if autonomous multi-agent orchestration is a product promise;

- increase Adapter coverage only when vendors expose stable supported APIs;
- calibrate SLOs using production-like load and partition Event storage when measurements require it;
- add managed multi-region placement and event-bus extraction without changing cursor/Event semantics;
- obtain independent security/compliance review and exercise customer KMS/HSM, IdP and private-cloud environments;
- validate pricing, conversion and marketplace liquidity with real users before changing commercial limits.

External validation is deliberately not represented as code completion. Public DNS/TLS, managed PostgreSQL/PITR, Cloud KMS/HSM custody, enterprise OIDC/SCIM tenants, payment settlement, app-store policy and production canary infrastructure require operator-provided accounts and change authority.
