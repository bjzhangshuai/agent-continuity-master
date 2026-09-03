# 主流 AI Agent 集成

所有桌面 Agent 共用 `agentcloud mcp stdio`，它从本机 `0600` 权限的凭据文件读取短期 Token 并自动刷新，配置文件中不保存 Token。

| Agent | 放置位置 | 模板 |
|---|---|---|
| Claude Code / Claude Desktop | 项目 `.mcp.json` 或 Claude MCP 设置 | `claude-code.mcp.json` |
| CodeBuddy IDE / CLI | 项目 MCP 自定义服务器设置 | `codebuddy.mcp.json` |
| Cursor | 项目 `.cursor/mcp.json` | `cursor.mcp.json` |
| Gemini CLI | 项目 `.gemini/settings.json` | `gemini.settings.json` |
| OpenCode | 项目 `opencode.json` 的 `mcp` 节点 | `opencode.json` |
| Codex Desktop | 安装 `plugins/agent-continuity` | 插件内 `.mcp.json` |

先构建并登录：

```sh
make build
install -m 0755 bin/agentcloud /usr/local/bin/agentcloud
AGENTCLOUD_API_URL=http://127.0.0.1:18080 agentcloud login ACCOUNT_ID
```

然后把对应模板合并到现有配置；不要直接覆盖已有 `mcpServers` 或 `mcp`。手机端 ChatGPT、Claude、Gemini App 不能执行本机 stdio 子进程，必须使用部署后的 HTTPS 远程 MCP 地址，并在各厂商控制台完成 OAuth/应用审核。移动 App 自身集成则使用 `sdk/swift` 或 `sdk/kotlin`，不依赖聊天产品是否允许插件。
