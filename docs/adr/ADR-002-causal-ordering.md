# ADR-002: Causal Ordering

Status: Accepted.

Use per-device monotonic sequence, causal parent DAG, Hybrid Logical Clock and accepted server sequence. No signal alone is global truth. This detects replay, reconstructs causality, provides deterministic display and supports stable cursor pulls. Last-write-wins is forbidden for Task work.
