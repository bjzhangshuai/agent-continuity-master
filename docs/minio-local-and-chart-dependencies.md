# MinIO 本地测试与完整 Chart 依赖

本次实际部署与测试结果见 [2026-09-04 验收记录](minio-validation-2026-09-04.md)。

## 实际安装边界

应用 Chart 与基础设施 Chart 分开升级，不迁移已有 PostgreSQL、NATS 或暂存 PVC 的 Helm 所有权。

| 组件 | Chart 所属 / 锁定版本 | 本机默认 |
|---|---|---|
| API、归档 Worker、迁移 Job | agent-continuity | 开启 |
| PostgreSQL | 应用内置 StatefulSet，postgres:16-alpine | 复用原实例 |
| NATS JetStream（Argo Events 生态） | 开发：应用内置；生产：infra 的官方 nats 2.14.6 | 复用原单节点 Broker，不再创建一套 |
| MinIO | infra 的本地修订 minio 5.4.0-continuity.1；服务端固定源码 7aac2a2c5b7c；mc 固定源码 77f82e18b540 | 新增一个副本 |
| Prometheus Operator、Prometheus、Grafana、kube-state-metrics | infra 的 kube-prometheus-stack 88.6.3 | 关闭，可选安装 |
| KEDA | infra 的 keda 2.20.2 | 关闭，可选安装 |
| OpenTelemetry Collector | infra 的 opentelemetry-collector 0.172.0 | 关闭，可选安装 |
| Tempo | infra 的 tempo 1.24.4 | 关闭，可选安装 |

依赖包已随项目放在 `deploy/helm/agent-continuity-infra/charts`，版本及依赖摘要由 Chart.lock 锁定。普通安装无需 `helm repo update`，不接触用户配置的其它 Helm 仓库。需要重新打包依赖时：

```sh
helm dependency build deploy/helm/agent-continuity-infra --skip-refresh --repository-config /dev/null
```

默认基础设施 values 是三节点生产 NATS 配置；**本机必须传 dev/infra-values.yaml**。不能把它与开发内置 broker 同时部署。Ingress Controller、StorageClass/CSI、metrics-server 是集群级前置条件，不会为了本应用替换或接管已有安装。此应用没有 Redis、Kafka、RabbitMQ 运行时依赖；生产数据库、KMS、公有云账号和真实支付商户仍由运维提供，不能通过装 Chart 自动开通。

## 一键本地部署

在仓库根目录运行：

```sh
./scripts/deploy-rancher-desktop.sh
kubectl --context rancher-desktop -n agent-continuity-dev port-forward \
  svc/agent-continuity 18080:8080 --address 127.0.0.1
```

脚本检查 Rancher Desktop 上下文、回环 API 地址与健康节点；镜像编译限制并行度。安装顺序：infra 只启用 MinIO → 私有桶/权限初始化 → Helm TLS 读写测试 → 应用只读预检 → API/Worker 升级。中间任一步失败都不会继续切换业务目标；Helm 回滚不执行数据库降级或清空原卷。

MinIO 上限 CPU 500m、内存 512Mi；新增 PVC 2Gi，测试桶硬配额 256MiB。初始化/测试容器上限 250m/128Mi，仅短时运行；不安装监控或扩容控制器。`local-path` 的 PVC 请求并非宿主机硬磁盘配额，仍需监控磁盘；MinIO 桶配额用于限制本演示的业务对象，单机目录/PVC 不能冒充跨节点 HA 或备份。

## 安全与配置

- API：`https://continuity-minio:9000`，仅 ClusterIP；控制台：`continuity-minio-console:9001`，无 Ingress/NodePort，默认不公开。
- 桶：`continuity-archive`，私有、256MiB 配额；归档对象只能访问 `continuity/*` 前缀。
- `continuity-minio-root`：仅 MinIO 和初始化 Job 可读。`continuity-minio-client`：归档专用 access-key/secret-key/ca.crt，API/Worker 只挂载此 Secret。
- TLS 使用随机生成的本地 CA 和带服务 DNS SAN 的证书，不使用 `--insecure` 或 `InsecureSkipVerify`。`ca_file` 为 S3-compatible provider 增加私有 CA，仍校验主机名。以后自建 S3 换 endpoint/CA/凭据即可。
- Helm 只在 Secret 不存在时生成值，后续 upgrade 复用；证书和私钥不写 Git、不安装到 macOS 系统信任库。GitOps/生产禁用 `minioBootstrap.manageSecrets`，由外部 Secret 管理系统预置三份 Secret。
- 本地服务证书有效期一年，CA 十年；不会自动续期。到期前轮换 `continuity-minio-tls` 的 public.crt/private.key/ca.crt 及 client Secret 的 ca.crt，重启 MinIO 并更新应用 `secretRevision` 后升级。不要删除根账号 Secret 后重新随机生成，尤其不能丢失数据卷对应的密钥与目标配置。
- MinIO PVC 与三份 Secret 标记 `helm.sh/resource-policy: keep`。卸载测试 release 不自动销毁数据；清理必须另行核对确切资源。它们的备份仍需另做，keep 不是备份。

MinIO 社区仓库已于 2026-04-25 归档，当前测试镜像从锁定的历史源码构建，构建方式见 [镜像说明](../deploy/images/objectstore/README.md)。这里只作为隔离的本地 S3 协议测试；生产选型应评估受维护版本、漏洞更新和许可要求。服务端为 AGPLv3；本地 Chart 修订与原始来源见 `deploy/helm/vendor/minio/CONTINUITY-PATCHES.md`。[上游仓库](https://github.com/minio/minio)、[上游 Chart](https://github.com/minio/minio/tree/master/helm/minio)。

## 如何看完整演示

打开 http://127.0.0.1:18080/，使用现有本地演示账号登录，进入「对象存储与队列」。

```sh
# 创建/复用 5 个类型附件 + 1 条签名对话事件，并逐一从对象存储回读校验
python3 scripts/seed-archive-demo.py --target minio-dev-v1
# 只读取旧演示，证明切换后历史 filesystem 归档仍可用
python3 scripts/seed-archive-demo.py --target local-demo-v1 --verify-only
```

选「MinIO Demo · 对话、文件与图片」，检查目标 `minio-dev-v1`、状态「归档已持久化」、尝试次数、回执时间；点击「从对象存储下载并校验」。文件类型为 markdown、PNG、文字、JSON、二进制，另有完整签名事件 JSON。旧「对象存储 Demo」仍对应 `local-demo-v1`，不会通过改目标 ID 覆盖旧回执。

这是实际 SDK → 暂存/Outbox → JetStream → Worker → MinIO HTTPS PUT → PostgreSQL 回执 → GET/SHA256 → 存储用量的链路，**不是用本地文件系统假扮 S3**。示例内容是假数据，支付仍是 demo 渠道，不会转移真实款项。

```sh
helm test agent-continuity-infra --kube-context rancher-desktop -n agent-continuity-dev --logs --timeout 120s
kubectl --context rancher-desktop -n agent-continuity-dev exec deployment/agent-continuity-archive-worker -- \
  /app/archive-probe --target minio-dev-v1 --write-probe
```

两个测试只创建并删除随机 diagnostics 测试对象，不列举/清空用户对象。Helm test 还检查专用账号不能执行管理员查询。重复运行不会删除任何业务归档。

## 可选监控、追踪与自动扩容

`deploy/environments/production/infra-observability-values.yaml` 开启 Prometheus/Grafana/KEDA/OTel/Tempo；这是资源有界的配置起点，未在本机安装，不代表生产 HA、QPS 或扩缩容已实测。

1. 确认没有需要复用的 Prometheus/KEDA 控制器。已存在时关闭对应 dependency，配置已有服务地址。不要同时注册第二套 KEDA external.metrics API。
2. 在预定应用命名空间准备 NATS TLS/auth Secret、`continuity-grafana-admin`（admin-user/admin-password），并加标签 `continuity.agent-continuity.io/monitor=true`。不要使用 Chart 默认弱密码。
3. 执行 `scripts/preflight-infra.py --context CONTEXT --namespace NAMESPACE -f deploy/environments/production/infra-observability-values.yaml`。它检查节点/压力、NATS 副本、Secret/PVC 和控制器归属，不会安装或接管已有控制器。
4. 显式安装 infra 的生产 overlay。Prometheus 按 namespace 标签发现本应用 ServiceMonitor/PrometheusRule；不要省略该标签，否则面板和扩容指标为空。
5. 应用使用 production/values.yaml，加 production/app-observability-values.yaml 和自己的数据库、RWX PVC、云目标及网络配置。OTLP 发向 `continuity-otel:4318`，Tempo 接收 4317；Grafana 数据源连接 Prometheus 和 Tempo。若修改 release/namespace，同步修改 endpoint 和 NetworkPolicy 选择器。
6. 应用预检通过后安装。KEDA 的 serverAddress 为 `http://continuity-monitoring-prometheus:9090`，按积压 max 值扩容；与 CPU HPA 互斥。至少两个 Worker 必须有经验证支持 flock 的 RWX 暂存，不能在本机 RWO 上强开。

数据库与队列可与应用分批升级；依赖控制器的 CRD 升级/回退应遵循各自官方说明，不依赖 Helm rollback 自动降级 CRD。持续监控磁盘、证书期限、队列死信与归档积压，并在生产等价环境做故障和备份恢复演练。
