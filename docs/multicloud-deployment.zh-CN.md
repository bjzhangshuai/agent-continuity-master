# 多云 Helm 交付

这是一套共享应用/依赖 Chart 加云环境覆盖配置。当前已有本地 Rancher/MinIO 验收；四云覆盖文件通过离线渲染，尚未使用真实云账号上线。它不自动购买云资源。

| 环境 | 应用覆盖 | 数据库 | 共享暂存 | 归档 |
|---|---|---|---|---|
| AWS EKS | `deploy/clouds/aws/values.yaml` | RDS PostgreSQL/PITR | EFS CSI/NFS | S3 |
| Alibaba ACK | `deploy/clouds/alicloud/values.yaml` | RDS PostgreSQL/PITR | NAS CSI/NFS | OSS |
| Tencent TKE | `deploy/clouds/tencentcloud/values.yaml` | TencentDB PostgreSQL | CFS CSI/NFS | COS |
| Azure AKS | `deploy/clouds/azure/values.yaml` | Azure Database for PostgreSQL | Azure Files CSI/NFS | Blob |

共享卷是 API 与异步 Worker 的暂存/回源缓存，不是最终归档库。必须支持跨节点 POSIX 文件锁；不能只看到 RWX 就假定缓存驱逐/回源互斥正确，也不能用对象存储 FUSE 替代。官方能力参考：[EKS/EFS](https://docs.aws.amazon.com/eks/latest/userguide/efs-csi.html)、[ACK/NAS](https://www.alibabacloud.com/help/en/ack/ack-managed-and-ack-dedicated/user-guide/nas-volume-overview-2/)、[TKE/CFS](https://cloud.tencent.com/document/product/457/44234)、[AKS/CSI](https://learn.microsoft.com/en-us/azure/aks/csi-storage-drivers)。具体区域、驱动和锁语义需要在目标集群验证。

## 依赖准备

1. 同区域 Kubernetes/外部 PostgreSQL、PITR 与恢复目标、数据库 CA；不使用开发内置数据库。
2. JetStream 3 节点、每节点独立持久卷、TLS/auth、故障域隔离；使用 `agent-continuity-infra` NATS 子 Chart 或已有兼容集群。启用官方 Argo EventBus 时复用其可达 NATS；不要部署两套重复 Broker。
3. 独立 RWX 暂存 PVC、备份 PVC、私有对象存储 Bucket/Container；按照 `deploy/examples/archive-clouds.example.json` 选择目标。当前对象存储凭据使用挂载文件，尚未实现所有云的无密钥 workload identity。
4. Core Secret、DB TLS Secret、archive config/credential Secret、NATS auth Secret；支付可独立后接，不把商户私钥写入 values/Git。
5. 对应 Ingress/Gateway、证书和 DNS；如启用 ServiceMonitor 则先安装 Prometheus Operator CRDs；KEDA 仅队列扩容模式需要。OTel/Tempo 是可选组件。
6. 将数据库、对象存储私网地址、Broker、DNS、支付 API 出口放入准确的 NetworkPolicy；静态 CIDR 要随服务端点变化维护。

## 操作顺序

```sh
# 先验证覆盖配置能渲染，无集群访问
python scripts/preflight-helm.py --offline --namespace agent-continuity-production \
  -f deploy/environments/production/values.yaml -f deploy/clouds/aws/values.yaml

# site-values.yaml 属于目标环境；补齐真实镜像版本、Ingress/TLS、PVC、Secret 名称、私网 peers
# 带 --production 时，未替换的模板会失败，这是预期行为
python scripts/preflight-helm.py --offline --production --namespace agent-continuity-production \
  -f deploy/environments/production/values.yaml -f deploy/clouds/aws/values.yaml -f site-values.yaml

# 只读验证目标集群的 Secret 键、PVC 状态/模式和 CRDs
python scripts/preflight-helm.py --context YOUR_CONTEXT --production --namespace agent-continuity-production \
  -f deploy/environments/production/values.yaml -f deploy/clouds/aws/values.yaml -f site-values.yaml

# 完成目标云连接、对象存储 probe、备份恢复演练后，在该环境执行部署
helm upgrade --install agent-continuity deploy/helm/agent-continuity \
  --kube-context YOUR_CONTEXT --namespace agent-continuity-production \
  -f deploy/environments/production/values.yaml -f deploy/clouds/aws/values.yaml -f site-values.yaml \
  --rollback-on-failure --wait --timeout 10m
```

上述命令没有创建 namespace/Secret/云资源；这些需按组织的基础设施流程预备。离线 PASS 不代表云可用，集群预检 PASS 也不代表真实商户结算、吞吐、跨区域恢复已通过。

GitHub Deploy workflow 现在要求受保护环境变量 `DEPLOY_VALUES_YAML`，内容是已经合并云覆盖的非敏感现场 values；商户/数据库凭据仍只通过现有 Secret 引用。先预检再 canary，canary 不创建公开 Ingress 或第二个备份 CronJob。镜像 tag 经格式检查，作为 YAML 数据写入，避免直接插入 shell。该 workflow 本次仅做本地结构/脚本检查，未触发远端生产发布。

扩展新的云：增加 Provider 测试和 `deploy/clouds/catalog.json` 条目/覆盖文件，复用同一数据库/消息协议；新增支付渠道：实现支付和退款接口、签名/回调/金额币种/幂等/未知结果查询测试，然后提供运营对账与沙箱验收。现有接口扩展点不等于任意渠道零代码接入。
