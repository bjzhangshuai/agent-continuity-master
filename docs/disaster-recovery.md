# Disaster Recovery

If cloud services fail, local adapters continue enqueueing signed events in the fsynced Outbox. The daemon pauses destructive retries, applies bounded exponential backoff, and resumes at-least-once delivery after health returns. Duplicate acknowledgements are success.

Recovery order is identity/keys, PostgreSQL, Artifact storage, API, then notification optimizations. Compare cursors and causal heads, retain missing-parent conflicts, verify Artifact hashes, and never repair with last-write-wins. A region-loss design requires external PostgreSQL/object replication and is not provided by the single-node dev chart.

The automated outage test stops the API transport while local daemon writes continue into bbolt/outbox, restarts cloud dependencies, then runs push, pull, staged cryptographic verification, reconciliation, and cursor acknowledgement. S3 version recovery is selected by content digest; database recovery selects a PITR timestamp before the incident. Declare recovery only after RPO/RTO evidence and audit continuity are recorded.
