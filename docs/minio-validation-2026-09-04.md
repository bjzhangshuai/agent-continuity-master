# MinIO 与 Helm 依赖验收记录

时间：2026-09-04 00:40（Asia/Shanghai）；只在 `rancher-desktop / agent-continuity-dev` 操作。

## 部署结果

- 应用 release `agent-continuity`：revision **25**，deployed。
- 基础设施 release `agent-continuity-infra`：revision **3**，deployed；Helm test **Succeeded**。
- 应用镜像 build ID：`sha256:4f95991eda894c927ede68251331b76a4045134a272f2f3cc9890569a9eb2c9f`。
- API、Worker、原 PostgreSQL、原单节点 JetStream、新 MinIO 全部 Ready；MinIO 重建 Pod 后仍 Ready。
- 仅新增 MinIO 2Gi PVC，原三个 PVC 名称、UID/卷绑定保持不变；MinIO 上限 500m/512Mi，桶配额 256MiB。
- 快照时 MinIO 占用约 86Mi（重启前曾 239Mi），应用相关五个运行 Pod 合计约 225Mi；节点 Ready，约 50% CPU / 40% 内存，无内存/磁盘/PID 压力。这里不是长期资源或性能承诺。
- PostgreSQL 的旧重启记录仍保留；新 Worker 启动阶段遇到一次数据库连接拒绝，自动重启后稳定。未发生 OOM，未修改现有 Argo Events 或其它命名空间。

## 已通过的测试

1. `GOMAXPROCS=2 go test -p 2 ./...`、`go vet -p 2 ./...`。
2. objectstore/archive 包 race 测试。S3 自定义 CA 成功；不可信 CA、主机名不匹配、无效 CA 文件拒绝；CA 轮换不改变目标物理身份。
3. 三个应用环境与 infra 默认/dev/可选完整依赖的 Helm lint、离线契约检查及依赖包 SHA256 检查。dev 无 CRD、ClusterRole、第二套 NATS 或监控组件；完整可选依赖可渲染 Prometheus/KEDA CRD、Grafana、OTel、Tempo；运行容器均配置资源上限。
4. 应用和 infra 的在线只读 preflight。生产三节点配置在本机被预检明确拒绝，未实际安装。
5. MinIO 初始化：私有桶、256MiB 配额、独立归档账号，仅允许专用前缀 Put/Get/Delete 及桶地域查询。根账号未交给 API/Worker。三份 Secret 升级前后内容摘要一致。
6. `helm test`：HTTPS PUT → GET → SHA256 → DELETE；归档账号不能执行管理员查询。对已有演示对象的无凭据 GET 返回 **403**。
7. Worker 内 `/app/archive-probe --target minio-dev-v1 --write-probe`：真实 S3 PUT/GET/SHA256/DELETE 成功，只创建/删除随机 diagnostics key。
8. Python SDK 创建「MinIO Demo · 对话、文件与图片」：5 个附件 + 1 条签名事件，全部 `stored`，6 次 API 回源下载大小/哈希校验通过。
9. `TestBillingPaymentJourney`、`TestFullContinuityJourney` 在已部署服务上通过。设置 `CONTINUITY_E2E_ARCHIVE_TARGET=minio-dev-v1`，防止误把 filesystem 当成 S3 验收。账单包含 archive/retained/download 三类指标；模拟支付成功且仅有一个支付回执。没有执行真实支付。
10. 重启 **仅 MinIO Deployment**，不删 PVC；随后 6 个 MinIO 样例再次回读校验通过。重复执行同一组 SDK 上传后仍只有 6 个归档 Job、6 条归档计量记录，共 1588 字节。
11. 历史 filesystem Demo 的 6 个对象切换前后、MinIO 重启后均可回读；没有迁移或删除旧对象。
12. 本地控制台使用已授权的演示账号登录，打开「对象存储与队列」；能看到 MinIO Demo 六条“归档已持久化”、目标、类型、尝试次数和回执时间。

测试期间发现并修复了初始化 Job 的非 root 临时目录权限问题、测试 Pod 启动网络规则尚未收敛的短暂连接拒绝，以及测试脚本重复清理同一已删除对象的问题。首次失败的安装自动回滚，MinIO PVC/三份 Secret 成功保留；业务应用在 infra 测试通过前一直保持旧版本。测试重试有次数上限，没有降低 TLS 校验或扩大账号权限。

## 数据证据

| 目标 | stored Job | 合计字节 |
|---|---:|---:|
| local-demo-v1（原目标） | 13 | 4676 |
| minio-dev-v1（新目标） | 11 | 4455 |

全局 pending/retry/dead/deleted 均为 0；Worker 指标 `continuity_archive_broker_connected=1`。MinIO 共 11 条包括 6 条可见演示及 Go E2E 内容，不全属于演示账号。

- 新演示任务：`task_1591fdac9c8c39755d02e68392b6923f`。
- 历史演示任务：`task_24453dcfad93bb4cb7faed685e287745`。
- 此次支付 E2E 账单：`inv_e4ef475501c79038ead6ea78`，paid，receipts=1。
- 升级前备份：`.data/minio-backup-lJqJdu/postgres.dump`（607KiB）及 `artifacts-and-archive.tgz`（16KiB），目录/文件权限 0700/0600。备份未删除，不含新建 MinIO 数据；MinIO 新数据保留在 PVC。

## 未冒充完成的生产验证

可选 Prometheus/Grafana、KEDA、OTel、Tempo 仅做 Chart 渲染/配置验收，**未安装到本机**。本机是单节点 MinIO、单节点 NATS、RWO 暂存；没有执行多节点 HA、生产压测、真实公有云账户/真实付款验收。业务归档删除和自动缓存清理仍关闭。MinIO 社区上游已归档，历史固定镜像只用于隔离测试；生产选型和安全更新见 [部署说明](minio-local-and-chart-dependencies.md)。

复现命令、依赖安装顺序、配置及 UI 查看过程见 [MinIO 本地测试与 Chart 依赖](minio-local-and-chart-dependencies.md)。此前 revision 24 的历史验收记录仍保留，不能将其 filesystem 结果当成本次 MinIO 结果。
