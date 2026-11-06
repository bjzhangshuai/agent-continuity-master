# Agent Continuity for CodeBuddy

This extension is paired with project-local CodeBuddy hooks. Capture is disabled
unless `.continuity/config.json` contains `capture_consent: true`. It captures
only visible user/assistant text for sessions that start in that opted-in
workspace; it does not scan existing conversations or copy private reasoning.

Commands expose sync status, task resume, explicit file upload, billing, local
simulated checkout, and the management console. `rehydrate` creates portable
context in a new CodeBuddy session; it does not overwrite CodeBuddy's private
native session database.
