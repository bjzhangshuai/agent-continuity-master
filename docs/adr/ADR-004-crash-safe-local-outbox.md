# ADR-004: Crash-safe Local Outbox

Status: Accepted.

The first daemon uses one immutable JSON event file per sequence, mode 0600, fsync and atomic rename. Acknowledgement removes only after server acceptance and syncs the directory. This avoids a heavy embedded driver while preserving crash safety; migrate to SQLite when queries, compaction or multi-process coordination require it.
