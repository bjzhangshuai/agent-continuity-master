# Observability

The server emits structured JSON logs with request ID, W3C trace ID, status, and latency. Incoming `traceparent`/`baggage` is propagated and spans are exported over OTLP/HTTP when `CONTINUITY_OTLP_TRACES_ENDPOINT` is configured. `/metrics` exposes HTTP, sync success/failure and latency, event duplicate/accept, reconciliation, conflict, artifact throughput, invitation, and resume counters. Health endpoints separate liveness from persistence readiness.

The Compose stack includes an OpenTelemetry Collector, Tempo, Prometheus, Grafana, native alert rules, and short local retention. Kubernetes includes probes, an optional ServiceMonitor and PrometheusRule. Alerts cover readiness, 5xx rate, sync failure ratio, duplicate spikes, and slow reconciliation. Runbooks define operator response; tokens, event payloads, task keys, webhook secrets, and credentials must never be logged.

Local-daemon metrics such as outbox backlog and offline replay rate are emitted in daemon status and are assessed by the scale/chaos suites. Production should route OTLP and Prometheus data to durable managed backends and set retention and tenant isolation according to policy.
