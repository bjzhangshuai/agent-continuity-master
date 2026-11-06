# 归档闭环验收 — 2026-09-04（北京时间）

## 本地交付

- 仓库：`/Users/jansurezhang/go/src/agent-continuity-master`。
- Helm release `agent-continuity`，namespace `agent-continuity-dev`，context `rancher-desktop`，revision **24**。
- 本次运行镜像 ID：`sha256:b1a463b0fe225c9cfc7d1b65f7e62b74e0e4ac12127d1711d737fa7f2786f4f6`。
- 迁移 `014_archive_lifecycle` 已应用；API、Worker、JetStream、PostgreSQL 均 Ready，节点 Ready，积压/死信均 0。
- Worker 启动时遇到一次 PostgreSQL 连接拒绝后重启恢复；升级后检查无持续崩溃。未修改 Rancher Desktop 配置、其它命名空间或既有 Argo Events 组件。
- 升级前备份：`.data/closure-backup-20lmu0/postgres.dump` 与 `artifacts-and-archive.tgz`（目录 0700，文件 0600）。未删除原业务数据。
- 前端地址：`http://127.0.0.1:18080/`。已用获授权的本地演示账号验收“对象存储与队列”：六条原样例均 stored，并可见保留期/保留锁/归档时间、校验下载入口。暂存回收和归档删除仍默认关闭。

## 测试结果

1. `GOMAXPROCS=2 go test -p 2 ./...`、`go vet -p 2 ./...`、`git diff --check` 通过。需要专用环境的测试默认跳过，不能用这条命令冒充已跑真实云。
2. 专用测试数据库 `continuity_archive_test_20260903` + 真实嵌入式 NATS JetStream，`go test -race ... -run TestArchiveJetStreamTransaction` 通过：签名事件/SDK 内容上传、重复版本幂等、重启后持久恢复、双消费者、校验下载、租户隔离、租约 fencing、死信重投、源事务/Outbox 回滚、缓存回收后回源、保留锁/保留期/最后副本删除拦截、删除审计、存储计费及模拟回执、全新空 broker 数据目录后的 Outbox 重投恢复。
3. 四家云原生驱动及自建 S3 的本地 HTTP 协议测试通过：认证、二进制回读、删除幂等、诊断 probe；不是云账户在线验收。
4. 六套 SDK 回归通过：Go、Python、TypeScript、Swift、Kotlin、.NET；增加 durable receipt 等待及终态失败用例。手机实体设备/封闭第三方 App 的安装授权不由这些自测替代。
5. Helm dev/staging/production + 独立官方 NATS dependency 的 lint/渲染契约通过；RWO 多 Worker 被拒绝；Secret/config 滚动、Worker 探针/指标、KEDA 配置、TLS/三副本/PVC/反亲和均检查。生产配置未在本机实际安装。
6. 本机 Chart 只读预检通过：引用的 Secret/key、PVC、配置及依赖。新增 CI 专用 PostgreSQL/JetStream 集成 job，远端 CI 尚未运行。
7. 已对 **本机部署服务** 跑 HTTP E2E：`TestBillingPaymentJourney`（新内容 → stored → 校验回读 → 三项存储计量进入账单 → 支付链接 → 模拟成功回调并发重放 → 唯一回执）与 `TestFullContinuityJourney` 通过。第一次付款链路测试因客户端账期结束时间被判超前失败，测试改用服务端 HTTP Date 后通过，没有放松服务端账期校验。Portal/treasury 的独立演示角色 E2E 本轮未注入其专用环境变量，明确跳过。

## 尚需真实环境验收的项目

真实 AWS/OSS/COS/Azure 私有账户、银行卡/商户收款、厂商账单成本核对、三节点故障域切换、跨节点 RWX 锁、KEDA 实际扩容、生产负载/灾备及云工作负载身份换证未在本轮虚构验收。

入口、Secret/Chart 安装次序、生命周期与计费边界、`archive-probe --write-probe` 联调方法均见 [archive-production.md](archive-production.md)。当前闭环事务语义是 Outbox + 至少一次 + 幂等最终一致，不是跨云 XA。
