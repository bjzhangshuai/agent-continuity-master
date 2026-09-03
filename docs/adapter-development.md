# Adapter Development

Implement `pkg/adaptersdk.Adapter`, declare capabilities truthfully, capture native inputs/outputs/tool results as portable typed events, retain native session references only in scoped metadata, and implement Native Resume with compatibility checks or Rehydrate.

Add the adapter to `tests/compatibility`, then test discovery, capture, Artifact handling, session metadata, resume fallback and error behavior. Do not scrape UI as the primary integration. Redact credentials and require explicit permission before collecting files outside the Task workspace.

Built-in provider IDs are `codex`, `claude`, `codebuddy`, `gemini`, `cursor`, `opencode`, and `generic`. Codex supports compatible native resume. The other mainstream providers consume explicit JSONL exports or hook output and use portable Rehydrate; they do not scan vendor-private state directories or claim private session equivalence.
