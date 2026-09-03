# Threat Model

| Threat | Asset / attack path | Impact / likelihood | Preventive control | Detection and recovery |
|---|---|---|---|---|
| Malicious/stolen device | Valid device keys append or unwrap | High / medium | device ACL, per-device envelopes, revoke/rotate | audit, signature/revoke metrics; remove grant and rotate Task key |
| Tampered Adapter | captures/changes native state | High / medium | adapter isolation, signed daemon envelope, explicit roots, contract tests | provenance/version anomalies; disable binding and replay trusted Events |
| MITM | steals tokens or alters traffic | Critical / low with TLS | TLS 1.3 ingress, signatures, AEAD and digests | TLS/WAF alerts; rotate sessions/keys and verify audit |
| Token leakage/replay | bearer reused | High / medium | short JWT, scopes, rotated refresh family, hashed opaque tokens, rate limit | auth/replay audit; revoke family/API key and investigate access |
| Event forgery/truncation | fake signature or omitted history | High / medium | Ed25519, causal parents, digest, cursor/head reconciliation | signature/missing-parent/conflict metrics; repull/reconcile from durable log |
| Artifact tamper | storage or transfer substitutes bytes | High / medium | CAS, per-chunk/final SHA-256, AEAD, atomic publish | hash alerts; quarantine cache and restore object version by digest |
| Task enumeration | guessed opaque ID | High / medium | resource permission before existence details, random IDs | 403/rate anomalies; block source and review tenant audit |
| Cross-tenant access | broken ACL/policy | Critical / low | scope + Task/Artifact ACL + RBAC/ABAC, deny precedence | authorization matrix and audit; revoke credentials and incident response |
| Privilege/tool escalation | user/Agent exceeds role | High / medium | explicit actions/resources, Agent capability subsets, no QR admin | denied-action metrics; remove policy/binding and preserve evidence |
| Invitation hijack/forward | QR or token copied | High / medium | 256-bit one-time reference, TTL, digest at rest, signed nonce/scope | accept/replay audit; revoke Task ACL and rotate key if disclosed |
| Device spoofing | fake device/sync hello | High / medium | registered Ed25519 proof, nonce/timestamp replay window | invalid-proof metrics; block/revoke identity |
| Malicious Agent/prompt injection | coerces exfiltration/tool misuse | High / high | least-privilege Task/Artifact grants, preview, explicit share, provenance | unusual access/audit; revoke binding/device and rotate affected keys |
| Server compromise | DB/object/ciphertext exposure | Critical / medium | E2EE, external secrets, non-root/read-only, NetworkPolicy, audit export | runtime/SIEM alert; isolate, restore, rotate service and Task keys |
| Webhook SSRF | endpoint reaches private metadata | High / medium | HTTPS/public-address validation, DNS/IP recheck, bounded body/retry | delivery errors/audit; disable endpoint and inspect network logs |
| Dependency/supply chain | compromised module/image/action | Critical / medium | pinned modules, CI SAST/vuln/license, SBOM, provenance and signature | scan/signature gate; rebuild from trusted revision and roll back |
| Backup/key loss | unavailable or exposed recovery data | Critical / low | encrypted PITR/object versions, checksum drill, separate key escrow | restore drill/backup alerts; invoke governed escrow and incident plan |

Residual facts are explicit: cryptographic revocation cannot erase data already decrypted by a once-authorized endpoint; global protection against prompt exfiltration requires Agent/tool sandboxing outside this service; Cloud KMS/HSM and public TLS controls are only proven in the operator's real environment.
