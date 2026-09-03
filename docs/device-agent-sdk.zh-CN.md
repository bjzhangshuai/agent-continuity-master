# 全平台设备与 AI Agent App 集成 SDK

## 交付范围

本仓库提供同一份 `AC-EVENT-1` / `AC-SYNC-HELLO-1` 协议在六套运行时中的实现。它们生成 Ed25519 签名身份和 X25519 加密身份，注册设备，创建签名事件，在断网时写入 Outbox，恢复网络后执行 push/hello/pull/ack，并保存 Inbox、Task Heads 与游标。服务端兼容原 Go JSON 签名和新的 `ac1.` 跨语言签名。

| 设备/App | SDK | 本地验证命令 | 安全存储 |
|---|---|---|---|
| 浏览器、PWA、Electron、Node.js | `sdk/typescript` | `npm test` | Web 传入 IndexedDB/平台安全存储适配器；示例含 LocalStorage 与内存实现 |
| Python Agent、Linux/macOS/Windows 服务 | `sdk/python` | `python3 -m unittest` | `FileStore` 使用原子写；身份密钥应接 OS keyring |
| Go CLI、Daemon、服务器 Agent | `pkg/client`、`internal/daemon`、`pkg/adaptersdk` | `go test ./...` | 本地状态 `0600`、崩溃安全 Outbox、Task Keyring |
| iPhone、iPad、macOS App | `sdk/swift` | `swift run AgentContinuitySelfTest` | `KeychainIdentityStore`，`ThisDeviceOnly`；文件状态使用 Data Protection |
| Android 手机、平板、车机；JVM Agent | `sdk/kotlin` | `./gradlew test` | `AndroidKeystoreIdentityVault` 用不可导出 AES Key 包装身份；Host 提供 SharedPreferences/DataStore/Room BlobStore |
| Windows 桌面、Linux/macOS .NET App | `sdk/dotnet` | `dotnet run --project tests/...` | `IRuntimeStore`；生产 App 应把身份保存到 DPAPI/Keychain/libsecret，状态使用原子文件或数据库 |

Flutter 没有被伪装成“已原生实现”：Flutter App 应通过 Platform Channel 调用 Swift/Kotlin SDK。React Native 可调用 TypeScript API，但生产密钥必须通过 Native Module 放入 Keychain/Android Keystore，不能保存在 AsyncStorage。

## 五步接入任何设备 App

1. 用户登录后取得短期 Access Token 与可轮换 Refresh Token；移动端生产环境使用 OIDC/SSO，不内置 Bootstrap Token。
2. 首次启动生成 `DeviceIdentity`，私钥只进本机安全存储；调用 `register` 上传两个公钥。
3. 将 Agent 会话注册为 Agent Profile，并用 `bindTaskAgent` 保存 `native_session_ref`、`resume_mode` 与能力。
4. 用户输入、Agent 输出、工具调用、决策、进度、错误和 Artifact 变化都调用 `queueEvent`。该调用先落本机队列再返回，不要求在线。
5. App 前台、后台调度或网络恢复时调用 `syncOnce`。实时 WebSocket 只负责唤醒，游标 pull 才是权威状态。

### Swift（iPhone/iPad/macOS）

在 Xcode 中添加本地或 Git Swift Package `sdk/swift`：

```swift
let identity = try KeychainIdentityStore().load()
    ?? DeviceIdentity.generate(name: UIDevice.current.name)
try KeychainIdentityStore().save(identity)

let api = AgentContinuityClient(baseURL: URL(string: "https://continuity.example.com")!, token: accessToken)
let store = try FileRuntimeStore(directory: appSupportDirectory)
let runtime = DeviceRuntime(client: api, identity: identity, accountID: accountID, store: store)
try await runtime.register()
try await runtime.queueEvent(taskID: taskID, eventType: "human.message.created", payload: .object(["text": .string(message)]))
try await runtime.syncOnce()
```

给 App 打开 Keychain Sharing 仅在同一开发者 Team 的 App/Extension 需要共享身份时使用；不要启用 iCloud Keychain 同步设备私钥。后台同步使用 `BGAppRefreshTask`，每次执行单个有超时的 `syncOnce`。

### Kotlin（Android/JVM）

发布前可先从本地 Maven 使用 `./gradlew publishToMavenLocal`，Android App 引入 `io.agentcontinuity:agent-continuity-kotlin:0.1.0`：

```kotlin
val identity = vault.load() ?: DeviceIdentity.generate(Build.MODEL).also(vault::save)
val api = AgentContinuityClient("https://continuity.example.com", accessToken)
val runtime = DeviceRuntime(api, identity, accountId, roomBackedRuntimeStore)
runtime.register()
runtime.queueEvent(taskId, "human.message.created", buildJsonObject { put("text", message) })
runtime.syncOnce()
```

`vault` 使用 `AndroidKeystoreIdentityVault`，并由 App 以 DataStore/Room 实现 `SecureBlobStore`。周期同步用 WorkManager，网络约束设为 `CONNECTED`；不要在主线程运行网络同步。

### TypeScript（Web/PWA/Electron/Node）

```ts
const identity = await generateDeviceIdentity("Chrome on tablet");
const runtime = new DeviceRuntime(new Client(apiURL, accessToken), identity, accountID, indexedDBStore);
await runtime.register();
await runtime.queueEvent(taskID, "agent.message.created", {text: output}, {agentID, providerID: "codex"});
await runtime.syncOnce();
```

浏览器端生产实现应将 `KeyValueStore` 接到 IndexedDB，并由 WebAuthn/平台密钥包装私钥。PWA 后台同步能力受浏览器限制，回到前台必须补做 `syncOnce`。

### Python

```python
identity = generate_device_identity("Linux Agent")
runtime = DeviceRuntime(Client(api_url, access_token), identity, account_id, FileStore(state_dir))
runtime.register()
runtime.queue_event(task_id, "tool.completed", {"tool": "build", "result": "ok"}, agent_id=agent_id)
runtime.sync_once()
```

### .NET（Windows 桌面）

```csharp
var runtime = new DeviceRuntime(new AgentContinuityClient(http, apiUrl, accessToken), identity, accountId, sqliteStore);
await runtime.RegisterAsync();
await runtime.QueueEventAsync(taskId, "progress.updated", new JsonObject { ["percent"] = 60 });
await runtime.SyncOnceAsync();
```

Windows 将 `DeviceIdentity` 放入 DPAPI 或 Windows Credential Manager；不要使用普通 JSON 文件保存私钥。

## 集成不同 AI Agent

桌面 Agent 优先使用 MCP：先构建并登录 `agentcloud`，再把 `integrations/` 下对应模板合并到项目配置。当前提供 Codex/ChatGPT 插件、Claude、CodeBuddy、Cursor、Gemini CLI 和 OpenCode 模板。它们都启动 `agentcloud mcp stdio`，所以配置中没有长期 Token。

运行中的 Agent App 若要主动写事件，使用设备 SDK；MCP 主要读取 Task、Continuation Package 与调用受控工具。Provider 绑定规则如下：

| Provider | `provider` | 推荐恢复方式 |
|---|---|---|
| Codex | `codex` | 有可验证 session ref 时 `native`，否则 `rehydrate` |
| Claude | `claude` | `native` 仅用于已验证 Claude session；默认 `rehydrate` |
| CodeBuddy | `codebuddy` | `rehydrate` |
| Gemini | `gemini` | `rehydrate` |
| Cursor | `cursor` | `rehydrate` |
| OpenCode | `opencode` | `rehydrate` |
| 其他 MCP/A2A Agent | `generic` | `rehydrate` |

不要把“生成了一段 Prompt”当成完整恢复。`continuityPackage` 包含 Task、因果 Event DAG、Heads、参与者、Agent Binding、冲突和 Artifact manifests；目标 Agent 必须先处理冲突和缺失 Artifact，再继续执行。

## ChatGPT/Claude/Gemini 手机 App 的边界

第三方手机 App 不能启动你电脑上的 `agentcloud`。要让 ChatGPT iPhone/Android、Claude Mobile 等直接看到管理工具，需要把 `/v1/mcp` 部署为公网 HTTPS Remote MCP，配置 OAuth、隐私政策和各厂商应用审核。Codex 本地插件已在 `plugins/agent-continuity`；ChatGPT Remote App 的 `.app.json` 必须使用平台注册后签发的真实 App ID，因此仓库不会提交无效占位 ID。

如果你开发的是自己的 AIAgent App，则不受该限制，直接嵌入 Swift/Kotlin/TypeScript/.NET SDK 即可。

## 验证

```sh
make test-sdk                 # 六套运行时构建/单测
make test-device-sdk          # 在 Rancher Desktop K8s 上跑 Python -> TypeScript 跨设备 + MCP E2E
go test ./pkg/protocol        # Python/TypeScript 固定签名向量由 Go 服务端验签
```

跨设备 E2E 只创建新的测试 Account、Device、Task 与 Agent，不删除命名空间、PVC、Secret 或 Rancher Desktop 资源。
