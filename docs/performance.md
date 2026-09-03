# Performance and SLO

The first calibrated local targets are safety gates, not claims about Internet-scale capacity. `go test -v ./tests/performance` records the observed rates on each release candidate.

| Signal | Initial target | Measurement |
|---|---:|---|
| Task index API P95 | < 250 ms | bounded HTTP load test with payloads excluded |
| Event push P95 / P99 | < 300 / 750 ms | Compose and Kubernetes smoke telemetry |
| Event notification P95 | < 500 ms | WebSocket wake-up, with cursor pull as authority |
| 1,000-event backlog replay | < 10 s | deterministic in-memory performance gate plus PostgreSQL E2E |
| Reconciliation of 1,000 events | < 2 s | `tests/performance` |
| Local Artifact write | > 1.6 MiB/s floor | 16 MiB CAS write and SHA-256 verification |
| Resume API success | >= 99.9% when the Task is readable | `continuity_task_resume_requests_total` and HTTP errors |
| Cross-Agent rehydrate success | >= 99% | adapter compatibility and journey E2E |
| Sync success rate | >= 99.9% excluding client cancellation | Prometheus recording/alert rules |

Capacity changes must preserve append-only partitionability, cursor pagination, content-addressed object storage, and stateless API replicas. The 10M-user/1B-Task/trillion-event figures in the product direction are evolution constraints, not an MVP load claim.
