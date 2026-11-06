# CodeBuddy integration

The integration has three cooperating surfaces:

- CodeBuddy project hooks call `bridge.py` on session start, stop, end and compaction.
- A project MCP server exposes read-only status, tasks, billing and portable resume tools.
- The installed VSIX provides explicit status, file upload, resume and simulated-checkout commands.

Capture requires `capture_consent: true` in the project-local configuration. A
session is eligible only after its `SessionStart` hook was observed in that
workspace. The bridge reads only the `transcript_path` capability supplied by
CodeBuddy, validates the session/path relationship and an allowlisted root, and
copies visible user/assistant text only. It does not discover older sessions,
private reasoning, tool secrets or arbitrary attachment paths.

`Stop` happens before CodeBuddy commits its final response, so the hook starts a
bounded detached read after returning. Records and signed events are durable in
SQLite before transmission and are idempotent on retry.

See [the local walkthrough](../../docs/codebuddy-local-e2e.zh-CN.md).
