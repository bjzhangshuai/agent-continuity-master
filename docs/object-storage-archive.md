# 多云内容归档与 Argo Events / JetStream

## 已实现的链路与边界

插件 / Go、TypeScript、Python、Swift、Kotlin、.NET SDK → API 鉴权 → 文件落到持久暂存盘（对话事件落 PostgreSQL）→ 与业务记录同事务写 Outbox → Relay 收到 JetStream PubAck → 独立 Worker 拉取 → 云对象存储 → 同事务更新归档回执和用量 → JetStream ACK。

消息仅包含归档任务 ID，不塞入图片、文件、对话或云密钥。新上传的 REST、Sync、MCP 对话事件和两种附件上传路径均由数据库触发器入 Outbox，避免某条传输路径漏发消息。历史内容不会启动即外传，需任务管理员显式补归档，每批最多 100 条。

Argo Events 生态的消息后端选用 **NATS JetStream**。Worker 使用共享持久化 pull consumer，可独立增加副本；不是为每条消息启动 Kubernetes Job。JetStream 使用 FileStorage、显式 ACK、拒收新消息而不丢旧消息、发布去重。Outbox 保留可恢复事实，30 秒后可重发未完成派发；租约、递增 fencing token、防重用量记录处理重复投递。默认指数退避，6 次失败转 dead，可授权重投。

**一致性不是跨云 XA/强原子事务。** PostgreSQL 本地事务保证业务、Outbox 与最终记账的原子边界，数据库与 JetStream/对象存储之间是至少一次投递、幂等对象 Key 和可恢复最终一致性。上传成功但数据库确认失败时会重新 PUT 同一个 Key；开启云端版本控制的桶可能产生多个物理版本，需配置生命周期，不应宣称“物理对象只写一次”。

`201` 表示平台已接收，不表示云端完成。只有 job 的 `state=stored` 才表示云 PUT 已成功并完成回执提交。下载经过任务及附件权限检查，从平台代理读取并校验 SHA-256，桶保持私有。E2EE 密文原样归档，服务端不会解密；SDK 的 uploadContent 本身不自动加密。

## 云配置

使用 `CONTINUITY_ARCHIVE_CONFIG_FILE=/run/archive/config.json`（或本地 `CONTINUITY_ARCHIVE_CONFIG`）。真实配置放 Kubernetes Secret，不放 ConfigMap / Git，不向插件下发。开启异步归档要求 `CONTINUITY_STORE=postgres` 与 `CONTINUITY_ARTIFACT_BACKEND=local`（local 在这里是持久暂存）。旧 `s3` 模式仍保留为独立同步模式，不能与新异步链路同时使用。

完整配置样例见 [archive-clouds.example.json](../deploy/examples/archive-clouds.example.json)。仅保留实际启用的 targets；样例五组密钥文件不存在时不能直接启动。

| provider | 驱动 | endpoint | 凭据 |
|---|---|---|---|
| aws | S3 SigV4 / minio-go | `https://s3.us-east-1.amazonaws.com` | Access Key、Secret Key，可选 STS token |
| alicloud | 阿里云 OSS Go SDK v2（原生 OSS API） | `https://oss-cn-hangzhou.aliyuncs.com` | AK/SK，可选 STS token |
| tencentcloud | 腾讯云 COS Go SDK v5（原生 COS API） | `https://bucket-appid.cos.ap-guangzhou.myqcloud.com` | SecretId/SecretKey，可选 session token |
| azure | Azure Blob Go SDK | `https://account.blob.core.windows.net` | account_name + Shared Key |
| s3 | S3-compatible / 自建 MinIO、Ceph 等 | 自建 HTTPS endpoint | AK/SK，按需 `path_style=true` |
| filesystem | 本地演示目标 | 独立绝对 root | 无，不代表真实云 |

本版凭据通过 Secret 文件加载，支持密钥轮换后滚动重启。尚未实现各云工作负载身份自动换取临时凭据或 Azure Entra ID；可在 `internal/objectstore.Provider` 接口扩展。TLS 使用系统信任链，不提供跳过证书验证。HTTP 仅允许显式启用的 loopback 测试服务。

运营方预建私有 bucket/container，授予最小 Put/Get 权限，不自动创建、清空桶或修改公共 ACL。云端加密、KMS、生命周期、备份、地域与租户合规策略由基础设施配置。

`organization_targets` 可精确指定租户，未指定使用 `default_target`。不会擅自把父租户路由继承给子租户。一个 target ID 的 provider/endpoint/bucket/prefix 等物理属性不可修改；切换桶时创建新 ID。队列记录绑定入队时的目标，旧目标配置应保留以继续读取历史内容。

## 本地部署与生产扩容

开发 values 启用项目自己的 JetStream（1 副本 / 1Gi PVC / 250m CPU / 128Mi 内存上限）、1 个 Worker（500m / 256Mi），对象写入 `/data/archive-demo`，并明确标记为本地演示。暂存上限 256Mi；不修改宿主机参数、其他命名空间或原有 Argo Events。

生产可用独立[基础设施 Chart](../deploy/helm/agent-continuity-infra/Chart.yaml)（固定官方 NATS 依赖，三副本 TLS）或健康的 Argo Events JetStream EventBus。样例见 [argo-eventbus.yaml](../deploy/examples/argo-eventbus.yaml)。使用平台独立 Stream `CONTINUITY_ARCHIVE`，不要直接混用 Argo 内部 CloudEvent 主题。

对照 EventBus `status.config.jetstream` 提供的实际鉴权方式配置：Token 用 `token_file`；Basic 用 `username` + `password_file`；NKey/JWT 用 `credentials_file`，三选一。CA 和客户端证书分别用 `ca_file`、`client_cert_file`、`client_key_file`。不要从密码字段推测用户名，不启用跳过 TLS 校验。

生产注意：

- JetStream 三副本需分布在不同故障域，流 `replicas=3`，至少三个服务端节点。新增 NATS 节点与提高 Stream 副本数是独立操作，不能只改 Deployment 数字。
- API/Worker 共用 **RWX 持久暂存卷**（如 EFS、Azure Files、NAS）并配置真实磁盘配额；本地 RWO 仅在同一节点演示。Chart 会拒绝 RWO 配置下的多 API、多 Worker 或自动扩容。
- `archive.workerReplicas` 控制手动扩容；CPU HPA 和可选 KEDA 积压扩容二选一；启用 KEDA 需实际安装其 CRD 并配置 Prometheus。参见[生产部署与依赖](archive-production.md)。
- Worker 每进程 `concurrency` 可设 1..32；应按文件大小和内存预算调节。拉取有界，消息/流大小有界，超限保留 Outbox 重试；无未经测量的吞吐量承诺。
- 暂存缓存现在支持显式开启的 TTL 回收，默认关闭；需共享 POSIX 内容锁、全部引用已 stored 及远端哈希校验。达到配额仍会拒绝新上传。归档副本删除、保留期、保留锁与审计已接入持久队列，详见[生产闭环文档](archive-production.md)。这不是原始对话或历史云版本完全擦除。
- 云端读取、数据库与共享暂存的网络权限需同时允许；跨 AZ 故障、网络隔离、灾难恢复和高并发压力测试需在生产等价环境验收。

本地既有 Argo Events controller 在本次工作前已因 `too many open files` 崩溃，本次不修复/替换它。隔离的 JetStream 不依赖该故障控制器；EventBus 样例不是“现有控制器已验收”的声明。

## API / SDK

- `POST /v1/tasks/{taskID}/contents`：统一小内容上传（最多 8MiB），JSON 包含 `artifact_id`、`version_id`、`filename`、`content_type`、`content_base64`，可选 `parent_version_id`。网络重试必须复用版本 ID。
- 大文件继续使用 `artifact-uploads` / `chunks` / `complete` 断点续传 API；合并完成后进入同一 Outbox。
- `GET /v1/tasks/{taskID}/storage-jobs?after=0`：最多 100 条，使用 `next_after` 分页。
- `GET /v1/tasks/{taskID}/storage-jobs/{id}`：`queued → processing → stored` 或 `retry → dead`。
- `GET /v1/tasks/{taskID}/storage-jobs/{id}/content`：读取云归档并校验；不公开云 URL。
- `POST /v1/tasks/{taskID}/storage-jobs/{id}/retry`：仅管理员可重投 dead job。
- `POST /v1/tasks/{taskID}/storage-backfill`：管理员显式补归档历史数据。
- `GET /v1/storage/config`：仅返回启用状态、驱动 ID 和非敏感限制，不返回密钥/endpoint。

Go `UploadContent / ListStorageJobs / GetStorageJob / WaitStorageJob / RetryStorageJob`；TypeScript/Swift/Kotlin 对应 lowerCamelCase；Python 对应 snake_case；.NET 对应 `*Async`。等待方法仅在 stored 时成功，dead/deleted 返回失败，不会自动替用户重投死信。调用方仍要持久保存版本 ID，并处理 401/503/507。

```python
from agent_continuity import Client
client = Client("http://127.0.0.1:18080", token)
client.upload_content(task_id, "notes", "version-001", "对话.md",
                      "text/markdown", "# 用户与助手的对话".encode())
page = client.list_storage_jobs(task_id)
# 找到 resource_id=notes / version_id=version-001；继续分页/轮询到 stored。
```

```typescript
await client.uploadContent(taskID, {
  artifact_id: "image", version_id: "version-001",
  filename: "截图.png", content_type: "image/png"
}, new Uint8Array(await file.arrayBuffer()));
const page = await client.listStorageJobs(taskID);
```

## 控制台、计量与验证

本地部署结果、6 条 Demo、测试范围与未验收边界见[2026-09-03 验收记录](object-storage-validation-2026-09-03.md)。

登录 [本地控制台](http://127.0.0.1:18080/) → **对象存储与队列** → 选择任务 → 查看消息状态、尝试次数、目标、内容类型和大小 → stored 后从对象存储下载。历史任务用“补归档本任务历史内容”；只有任务管理员可执行补归档/死信重投。

每条归档完成时原子记录 `storage.archive.bytes`；账期汇总 `storage.retained.byte_hours`；经校验的云回源计 `storage.download.bytes`。默认价目表为新增三项显式配置零价，自定义价目表缺指标会拒绝启用归档，避免账单在运行时无法生成。三项沿用租户账单、支付链接与回执流程；计费边界和生产价格配置见[生产闭环文档](archive-production.md#存储计费契约)。

测试分层：官方 Go SDK 的本地 HTTP 合约测试；PostgreSQL + 真实 JetStream 进程重启的集成测试；本地 K8s 开发链路。没有真实云凭据时，不声称 AWS/OSS/COS/Azure 商户账号已完成在线验收。

回退到不认识归档的旧镜像前，应先停用新入队（`archive_control.enabled=false`）并保留 Worker 排空队列；不要删除 Outbox 表、暂存 PVC、broker PVC 或云对象。迁移 013 为新增表/触发器，无历史数据删除。

## 官方接口参考

[Argo Events JetStream EventBus](https://argoproj.github.io/argo-events/eventbus/jetstream/)、[NATS durable consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)、[AWS PutObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html)、[阿里云 OSS Go v2](https://www.alibabacloud.com/help/en/oss/developer-reference/manual-for-go-sdk-v2/)、[腾讯 COS Go](https://github.com/tencentyun/cos-go-sdk-v5)、[Azure Blob Go](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-blob-go-get-started)。
