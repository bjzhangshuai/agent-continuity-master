# Risk Register

| Risk | Probability | Impact | Early signal | Mitigation | Contingency |
|---|---:|---:|---|---|---|
| Closed/changing Agent APIs | High | High | contract failures/vendor notices | capability contract, structured supported hooks | portable Rehydrate; disable unsafe Native Resume |
| Vendor ships native continuity | High | Medium | overlap/adoption decline | own cross-vendor Task/Artifact/collaboration layer | focus enterprise trust and interoperability |
| MCP/A2A standard drift | Medium | Medium | conformance failures | version adapters, keep core transport-neutral | pin compatible surface; dual-version gateway |
| Frequent Adapter breakage | High | High | capture warnings/resume errors | compatibility CI and explicit capability detection | quarantine version and fall back to Generic |
| Cross-Agent state loss | Medium | High | low resume/completion rate | complete package preview, snapshots, provenance | return to source Agent/branch and human review |
| Security/data incident | Medium | Critical | auth/hash/audit anomalies | E2EE, least privilege, scans, response drills | isolate, revoke/rotate, restore and notify |
| E2EE collaboration friction | High | Medium | failed invites/key unwrap | device envelopes, post-accept key exchange, diagnostics | explicit plaintext policy only for approved Task |
| Storage/bandwidth cost | Medium | High | egress/object growth slope | manifests/lazy fetch/dedup/cache/lifecycle | tighten plan retention and move cold versions |
| Event growth | High | High | cursor lag/index/storage slope | snapshots, pagination, partition-ready schema | partition/archive with verified restore |
| Cold start/network effect | High | High | low weekly continued Tasks/invites | coding-Agent wedge, repeatable demos, SDKs | narrow segment and prioritize direct utility |
| Local installation resistance | Medium | High | daemon abandonment/doctor errors | passive sync, small binary, diagnostics | browser/SDK-only mode with reduced capture |
| Large platform copying | Medium | High | bundled competing feature | open protocol/adapter ecosystem and trust graph | partner/integrate rather than runtime competition |
| App Store/enterprise endpoint limits | Medium | High | rejected package/MDM denial | CLI/daemon/Web options, documented permissions | server-side SDK mode or approved enterprise package |
| Market/pricing invalid | High | High | weak conversion/negative margin | instrument north-star/cost and run experiments | change limits/segment; do not depend on marketplace |

Owners review this register at each release. A risk closes only when early-signal data and a tested contingency exist; code completion alone does not close market, vendor or compliance risks.
