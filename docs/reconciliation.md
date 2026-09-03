# Reconciliation

Reconciliation is a dedicated module and store transaction, not timestamp overwrite logic. It classifies duplicates, sequence replay, missing parent, non-conflicting concurrency, branch conflict, permission conflict, Artifact version conflict, and delete-versus-update.

```mermaid
flowchart TD
  O[Offline outbox] --> P[At-least-once push]
  P --> D{Duplicate?}
  D -- yes --> ACK[Return original ack]
  D -- no --> C{Parents known?}
  C -- no --> MP[Store + missing-parent conflict]
  C -- yes --> H{Consumes a head?}
  H -- no --> BR[Store + create branch]
  H -- yes --> AP[Store + advance heads]
  MP --> R[Parent later arrives]
  R --> FIX[Recompute causal head]
```

No valid signed work is silently discarded. Auto-merge is limited to structurally non-conflicting events. Semantic and Artifact conflicts require an explicit merge/resolution event; provenance remains queryable.
