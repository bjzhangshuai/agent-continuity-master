# 对象归档验收记录 · 2026-09-03

## 本地交付

- 项目：`/Users/jansurezhang/go/src/agent-continuity-master`。
- 集群：`rancher-desktop`，命名空间 `agent-continuity-dev`。
- Helm：`agent-continuity` revision **23**，`deployed`。
- API、PostgreSQL、独立归档 Worker、独立 NATS JetStream 均为 Ready。
- NATS 镜像：`nats:2.12.15-alpine`；FileStorage、持久消费者 `archive-workers`、Stream `CONTINUITY_ARCHIVE`、`sync_always=true`。
- 本地一个 broker / 一个 Worker。没有修改既有 Argo Events、STAN、宿主机参数，也没有进行破坏性集群故障测试。
- Worker 在启动阶段因数据库暂未就绪重启一次，随后 Ready；PostgreSQL 的一次重启发生在本轮部署前。
- 最后检查节点 Ready，MemoryPressure / DiskPressure / PIDPressure 均为 False；CPU 47%、内存 37%（检查时快照，不是容量或吞吐承诺）。

本次采用 Argo Events 生态的 **NATS JetStream** 技术栈；本地独立实例并非由现有 Argo Events controller 管理的 EventBus。现有 controller 的既有故障未被修复。生产接入方法及 EventBus 清单见[配置指南](object-storage-archive.md)。

## 可复现 Demo

1. 打开 <http://127.0.0.1:18080/>。
2. 用本地演示账号 `tenant-demo.group` 登录；现有凭据保存在 `.data/tenant-demo.json`，不要提交或公开该文件。
3. 点击“对象存储与队列”，选择“对象存储 Demo · 对话、文件与图片”。
4. 查看 Markdown、PNG、文本、JSON、二进制附件与签名对话事件，共 **6 条 stored**，每条第一次处理成功。
5. 点击“从对象存储下载并校验”。目标显示 `local-demo-v1 · filesystem（本地演示，不是真实云）`。

已通过实际浏览器登录、栏目切换与渲染检查；页面展示 6 条已持久化回执。SDK E2E 下载六份内容并逐一校验 SHA-256 和字节数。总计 **1,588 bytes**，数据库归档用量 **6 条 / 1,588 bytes**。JetStream 检查为已投递 6、已 ACK 6、待 ACK 0、积压 0。

演示脚本：`python3 scripts/seed-archive-demo.py`，仅使用本地现有演示账号和合成内容，不输出密码，不访问真实云服务。稳定版本 ID 可重复运行；不会更改已有真实对话。

## 测试结果

| 检查 | 结果 / 覆盖边界 |
|---|---|
| Go 全量 `GOMAXPROCS=2 go test -p 2 ./...` | 通过；需外部服务的测试默认跳过，以下归档集成测试另行显式执行 |
| Go race 检查 | `internal/archive`、`internal/objectstore`、`internal/artifact` 通过；HTTP API race 检查亦通过 |
| AWS / S3-compatible / OSS / COS / Azure provider | 官方客户端本地 HTTP 合约测试通过，包含鉴权请求与二进制往返；不是实际云账号验收 |
| PostgreSQL + 真实 JetStream | 专用测试库；业务与 Outbox 原子入队、持久化后重启 broker、两个独立 Worker 并发消费、内容校验、缓存缺失回源均通过 |
| 重复、失败与权限 | 不可变版本、幂等归档用量、跨租户拒绝、过期租约 fencing、死信重投、队列满时业务与 Outbox 一同回滚通过 |
| TypeScript SDK | 构建与 3 项测试通过，含内容上传 / 状态查询 |
| Python SDK | 4 项测试通过；另通过本地部署端到端 Demo |
| Kotlin / .NET / Swift | Kotlin 测试、.NET self-test、Swift executable self-test 通过；Swift 项目无 XCTest target，实际执行 `swift run --skip-build AgentContinuitySelfTest` |
| Helm / 镜像 | 开发配置 lint、镜像构建、revision 23 滚动部署通过 |

集成测试仅允许 `continuity_archive_test_` 前缀的专用数据库。本轮使用 `continuity_archive_test_20260903`，未对业务库运行故障注入或测试清理。

迁移前保留了受限权限数据库备份 `.data/archive-backup-cUvnND/postgres.dump`。未删除现有 PVC、历史数据或云对象。

## 尚不能据此宣称的能力

- 未提供云账号凭据，因此没有进行真实 AWS / OSS / COS / Azure 联调。
- 没有在本地运行三节点高可用或生产规模高并发压测；生产多节点须使用共享 RWX 暂存、持久化配额、TLS 和独立故障域。
- 一致性是 PostgreSQL 事务 Outbox + 至少一次投递 + 幂等结果 / 用量 + 可恢复最终一致性，不是跨数据库、消息队列、对象存储的 XA 强事务。
- `storage.archive.bytes` 已计量，未新增自动收费价格；不等价于 GB-month、厂商实际账单或出口流量计费。
- 暂存数据当前不自动清理；容量达到上限时背压拒绝新上传。生产缓存回收、对象删除合规及云工作负载身份仍需后续配置或扩展。
