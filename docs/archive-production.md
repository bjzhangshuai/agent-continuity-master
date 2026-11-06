# 对象归档闭环与生产交付

## 已实现的链路与边界

插件/SDK 提交签名事件或附件 → 持久暂存 + PostgreSQL 同事务 Outbox → NATS JetStream FileStore/PubAck → 独立 Worker → AWS S3 / OSS / COS / Azure Blob / 自建 S3 → 回执和幂等逻辑归档用量 → 账单 → 现有支付渠道 → 支付回执。

`201` 仅为接收成功，`storage-job.state=stored` 才是云写入和数据库回执完成。六种 SDK 新增 `WaitStorageJob`（语言命名遵循各 SDK），调用方先按 resource_id/version_id 从分页列表获取 job ID。等待超时不代表上传失败，继续查询原 ID，不能生成新版本重复上传。Go/.NET 支持上下文取消；Python/TS/Swift/Kotlin 的轮询预算之外还受单次 HTTP 超时约束，Kotlin 要在后台线程调用。小内容方法最多 8 MiB，大文件使用已有分片/恢复/完成 API，完成后走相同 Outbox；不自动解密或加密内容。

事务语义是**本地事务 + Outbox + 至少一次投递 + 幂等/补偿的最终一致性**，不是跨 PostgreSQL、NATS、四家云的 XA/2PC。消息只含 job ID，不携带对话正文或密钥。旧同步 S3 模式仍保留，但与新异步模式互斥。

本地是独立单节点 JetStream + TLS MinIO（S3 协议），新上传默认 `minio-dev-v1`；历史 filesystem 目标 `local-demo-v1` 保留并可读取。这不表示已连上真实公有云账号。部署与依赖见 [MinIO 与依赖安装](minio-local-and-chart-dependencies.md)。封闭的第三方手机 Agent App 仍需官方扩展入口或用户导出，不会后台抓取其它 App 私有聊天内容。

## Chart 与依赖安装顺序

1. 准备至少三个 Kubernetes 节点、CSI 存储类、私有对象桶，以及外部 PostgreSQL、备份目的卷。API/Worker 暂存 PVC 必须 RWX，并实测跨节点 POSIX flock；Rancher Desktop 的 local-path 只支持本地单节点演示。暂存容量配置必须小于 PVC 可用空间，完整上传期间还要容纳分片和合并副本。
2. 在应用命名空间创建 `continuity-nats-auth`（token）和 `continuity-nats-tls`（tls.crt/tls.key/ca.crt）。证书包含 `continuity-nats` 服务及 headless/各 Pod DNS SAN，兼容 server/client EKU。不要把真实值放 Git。
3. 独立基础设施 Chart 固定官方 NATS **2.14.6**，依赖归档与 Chart.lock 已入仓库。执行 `helm dependency build --skip-refresh deploy/helm/agent-continuity-infra` 后用独立 release `agent-continuity-infra` 安装。其三副本、PVC、路由 TLS/mTLS、客户端 TLS + token、反亲和及 PDB 与应用独立升级。默认需要三个节点，**不要用生产 profile 安装到本机**。
4. 也可以使用已健康运行的 Argo Events JetStream EventBus；此时不安装 infra Chart，改为 EventBus 的实际 TLS 地址、认证文件和网络选择器。不得混用 Argo 内部 CloudEvent stream/subject。现有本机旧 STAN/故障 Argo controller 不在此次改动范围。
5. 创建核心 Secret `agent-continuity-secrets`、归档 Secret `agent-continuity-archive`、数据库 CA Secret `continuity-database-tls`。核心 `database-url` 使用 `sslmode=verify-full&sslrootcert=/run/database-tls/ca.crt`。该 CA 挂载到 API/init、Worker、迁移 Job 和备份 Job。
6. 归档 Secret 的 config.json 设 `enabled=true`，仅保留实际启用的 targets。复制 `deploy/examples/archive-clouds.example.json` 到安全目录，修改租户路由、桶和凭据路径；使用本仓库 infra 时 broker URL 为 `tls://continuity-nats:4222`，认证改为 `token_file=/run/archive-broker/token`，CA 可放归档 Secret：`ca_file=/run/archive/ca.crt`。客户端 Secret 与服务端必须使用同一 token；不要同时配置 credentials_file。
7. 准备实际 values 覆盖文件：不可变镜像 tag、imagePullSecrets、已 Bound 的 RWX claim、外部数据库 `networkPolicy.databasePeers`（TCP/5432）、brokerPeers（TCP/4222）、云/KMS/监控外连 CIDR、域名/TLS，以及 `backup.enabled=true` 和专用 backup.existingPVC。示例生产 values 故意不伪造你的数据库地址/卷/证书，因此需要补齐才可部署。
8. 若 `serviceMonitor.enabled=true`，先安装 Prometheus Operator CRD 并使实例的 selector 选中本 Chart 的 ServiceMonitor/PrometheusRule。infra Chart 已锁定可选 Prometheus/Grafana、KEDA、OTel、Tempo 依赖，使用 `deploy/environments/production/infra-observability-values.yaml` 显式开启；已有实例应复用，不重复安装控制器。`monitoringNamespaceLabels` 必须与实际监控命名空间匹配；没有 CRD 时不能直接安装生产示例。
9. 执行只读预检，再安装应用：

```sh
python3 scripts/preflight-helm.py --context YOUR_CONTEXT --namespace YOUR_NAMESPACE \
  -f deploy/environments/production/values.yaml -f /secure/actual-values.yaml --production
helm upgrade --install agent-continuity deploy/helm/agent-continuity \
  --kube-context YOUR_CONTEXT -n YOUR_NAMESPACE \
  -f deploy/environments/production/values.yaml -f /secure/actual-values.yaml --wait --timeout 5m
```

预检验证渲染、Secret 必需 key、PVC/accessMode、CRD 和必要外连配置，不会替代真实网络、TLS、云权限测试。不会自动扩容本机、安装云基础设施或修改其它命名空间。

## 扩容、轮换与可观测性

- Worker `GET /live` 检查进程，`GET /ready` 检查数据库/迁移及 broker 连接；不会因为云端短故障而无限重启进程。`GET /metrics` 输出状态数量、积压、最老积压时长、归档保留字节和 broker 连通性，不输出租户 ID/内容。
- CPU HPA 与 `archive.queueAutoscaling`（KEDA Prometheus scaler）二选一。KEDA 需要集群安装、可达的 Prometheus serverAddress、正确认证及 scrape；按积压扩容使用 `max(continuity_archive_pending_jobs)`，不能把多个 Worker 导出的全局计数相加。API/Worker 多副本必须 RWX。
- KEDA、Prometheus 与三节点容灾在生产等价环境执行验收，本机仅做 Chart 渲染/契约检查，不代表已跑生产压测。当前无未经实测的 QPS/SLA 承诺。
- ConfigMap 内容变化和 Helm 可读取的 Secret 内容变化都会更新 Pod checksum。外部 Secret 控制器/GitOps 轮换后须更新 `secretRevision` 并 Helm upgrade；仅编辑 Secret 不保证进程刷新内存中的凭据。NATS 独立 release 轮换时更新 `nats.podTemplate.merge.metadata.annotations.continuity-secret-revision`，分批滚动并保持法定多数。
- 不要修改已有 target ID 对应的 endpoint/bucket/prefix。新物理目的地使用新 ID，旧配置保留到旧对象迁移/删除完成。云 provider 实现 `Put/Get/Delete` 接口，可继续增加新厂商。

## 存储计费契约

| 指标 | 计量点 | 默认新增费用 |
|---|---|---|
| storage.archive.bytes | PUT 成功后与回执同事务写入；按 job ID 幂等 | 0 |
| storage.retained.byte_hours | stored_at 至 deleted_at 与账期的交集，字节 × 小时；生成账单时固定快照 | 0 |
| storage.download.bytes | 完整回源且 SHA256 校验通过、向客户端返回前记账 | 0 |

已有 artifact.bytes 仍是上传量，并非 GB-month。默认零价避免重复收费，但存储指标必须有显式价目；开启归档时自定义价目表缺任一指标会拒绝启动。新增计量按任务原始租户归属，沿用多租户付款授权和原有微信/支付宝/PayPal 等账单支付流程。生产价格由运营方审核配置，不从浏览器决定。

保留时长按所有 job 的逻辑副本计费，不是物理去重后用量；byte_hours 按账期合计后取整。默认计价基数 `GiB*730` 只是可配置单位，不能冒称某云的真实月租。回源量不是浏览器实收字节：Range/取消请求也可能已读取整对象；校验失败、内部维护 GET 不计入此指标。它不等于厂商出站账单、API 请求费或跨区费，真实供应商成本对账仍需导入厂商账单。

## 生命周期与安全边界

`lifecycle.allow_delete=false`、`cache_ttl_seconds=0` 为默认值；不会删除已有内容或改变真实收费。

开启缓存回收时 TTL 至少 3600 秒，Worker 每分钟扫描有界批次。API 文件发布到元数据提交期间持有跨进程内容锁；回收同时检查所有租户/版本引用均已 stored，先持久记录本地副本可回收，再从归档校验 SHA256，最后仅移除精确 CAS 文件。失败保留云端/元数据供重试。锁文件不删除，防止 inode 竞争。不能借此清除原始对话事件、分片目录或整卷；未完成/孤立上传空间仍要监控，达到配额拒绝新上传。

删除当前归档副本需要 task/attachment admin、显式哈希确认与原因、保留期到期、无 legal_hold，且本地持久副本未交给缓存回收。删除请求经持久队列消费，失败重试/死信，完成后保留 deleted_at 与审计。不允许删掉缓存回收后的最后持久归档副本。此接口**不是原始对话完全擦除/账号注销/GDPR purge**。版本桶、快照、云备份中的非当前版本，由运营者配置生命周期与合规保留。

设置保留锁需要任务/附件管理权限；解锁还需平台超级管理员。删除已入队后不能再变更锁，必须在请求前设定保留政策。审计保存在 archive_action_audit。云对象锁应在云侧配置，本应用锁不能阻止管理员绕过平台直接删除桶对象。

## 真实云联调（显式选择才写入）

```sh
# 默认仅解析/初始化配置，不发测试请求
go run ./cmd/archive-probe --config /secure/archive-config.json
# 显式授权：每个启用 target 创建一个随机 diagnostics/<id>/roundtrip.txt，回读校验后删该 key
go run ./cmd/archive-probe --config /secure/archive-config.json --write-probe
```

镜像含 `/app/archive-probe`，可在已挂载归档 Secret、已有云外连权限的 Worker Pod 内运行同样命令。配置来自 Secret，不输出凭据；失败只报告自己创建的诊断 key，便于按精确 key 清理。需要 Put/Get/Delete 权限；建议使用专用测试桶和短期最小权限凭据。版本桶可能保留测试历史版本且存在厂商请求费用。不会列举、清空、删除现有业务对象。

四家原生协议均有本地 HTTP 合约测试，但未提供真实云凭据前不声称真实账户验收成功。目前仍使用 Secret 文件凭据（Azure Shared Key），不包含各云工作负载身份/Entra 自动换证。

## 恢复与演练

- 数据库、暂存和云对象都要备份；数据库 CronJob 只负责 pg_dump，完成前使用 partial 后缀，成功 pg_restore --list 验证后才发布。备份 PVC 不能与业务/暂存卷混用。
- 暂停业务写入或使用一致性快照协调 DB 与暂存备份。云对象开启版本/备份，并备份加密密钥与 target 配置（密钥与内容备份分开管理）。不能靠 pg_dump 恢复未归档附件的原始字节。
- JetStream 三副本不是备份。Broker 丢失后新 FileStore/stream 可由 Outbox 在重投周期后重建尚未完成任务，测试覆盖此路径；死信仍需管理员显式重投。PostgreSQL 丢失则必须先恢复数据库。
- 恢复演练使用隔离数据库、暂存目录与专用测试目标，沿用 scripts/restore-drill.sh。**不要让旧数据库备份中的 pending/delete 任务直接指向真实生产桶运行**；先核对现有云回执/保留锁/删除记录与目标身份，避免旧任务复活或误删。
- 回滚只回退应用镜像/values，不自动 DROP 表或删除 PVC；014 是新增生命周期迁移。已使用新计费/删除状态后优先修复前进，避免旧版本丢失新语义。

参考：[NATS 官方 Chart](https://github.com/nats-io/k8s/tree/main/helm/charts/nats)、[Argo Events JetStream](https://argoproj.github.io/argo-events/eventbus/jetstream/)、[KEDA Prometheus scaler](https://keda.sh/docs/latest/scalers/prometheus/)。
