# Agent Continuity plugin

This is the installable Codex/ChatGPT plugin package for Agent Continuity. It uses the local `agentcloud mcp stdio` bridge and the credentials saved by `agentcloud login`.

Prerequisites:

```sh
make build
install -m 0755 bin/agentcloud /usr/local/bin/agentcloud
AGENTCLOUD_API_URL=http://127.0.0.1:18080 agentcloud login ACCOUNT_ID
```

The package is ready for local Codex installation. ChatGPT web/mobile cannot launch a process on your computer; publishing there requires registering the deployed HTTPS `/v1/mcp` endpoint as a remote MCP app and then adding the issued app ID to `.app.json`. No placeholder app ID is committed because it would not be installable.
