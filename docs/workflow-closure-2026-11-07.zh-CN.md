# 五项闭环交付与验收（2026-11-07）

本轮补齐 A2A/MCP、持久化多 Agent 编排、市场收款分账、票据和服务端云 KMS。对外接口可以模拟；模拟资金不进入真实银行账户，模拟票据不能用于报税报销。现有任务、签名事件、JetStream 归档、支付和退款接口继续保留。

## 范围与证据

| 能力 | 本轮实现 | 生产边界 |
|---|---|---|
| A2A | `/a2a/v1` JSON-RPC 1.0；Agent Card、SendMessage、GetTask、ListTasks、CancelTask；阻塞/立即返回、状态映射、历史长度、分页、错误、租户隔离 | 已用官方 `a2aproject/a2a-go/v2@v2.6.0` 客户端互操作；文本执行技能每个任务接收一条请求，后续任务用 contextId 分组；不声明流式、推送、扩展卡能力；未宣称官方认证 |
| MCP | 2026-07-28 discovery、逐请求版本/身份元数据、HTTP 镜像头校验、工具调用；兼容 2025-11-25/06-18/03-26；现有 stdio 桥同步保留 RPC 错误 | 工具能力配置，无 resources/prompts/sampling；不声明未实现能力 |
| 编排 | 已排序的 DAG、依赖输出传递、审批/取消、预算、有限重试、持久化租约与 CAS fencing、恢复；成功步骤和用量同一事务 | 每个 run 的步骤按拓扑顺序执行，多副本可并行执行不同 run；远程 Agent 须实现稳定 messageId 幂等。预算是请求内的执行单位上限，不是模型厂商 token 费用保证 |
| 跨设备接续 | 绑定 task_id 的编排输出进入 `execution_reports`；Go CLI、通用/主流 Agent/Codex 适配器及 CodeBuddy 恢复文本包含结果 | 结果注明服务端外部 Agent 来源，不伪造设备签名事件；有新执行结果时使用 portable rehydrate。接续包包含最近 5 个 run，每步骤最多 16 KiB UTF-8 输出并标注 output_truncated，避免大结果拖慢设备；完整历史经编排查询接口读取 |
| 收款/分账 | 市场交易 → 固定金额采购账单 → 原支付渠道 → 已验证回执 → 分账 → 卖方可提额 → 账户 token 验证 → 提现预留 → 渠道对账；退款冲回与欠款阻止超提 | 网关接口可替换；Demo 模式全程不动真实资金。正式分账需要收单平台/支付机构账户与商户资质，网关必须验证收款账户属于对应卖方。平台佣金在 split 请求中单独分配；不可把同笔市场订单再次导入平台全额收款台账 |
| 开票 | 已付款账单申请蓝字票据、购方/税号、持久化请求、幂等对账、下载；成功退款生成关联红字请求 | 服务商网关负责税务系统接口、签章、校验与法定格式；Demo 文档明确 `legally_valid_tax_invoice=false`，不是税务机关开具发票 |
| 云 KMS | AWS KMS SigV4、阿里云 KMS RPC 签名、腾讯云 TC3、Azure Key Vault RSA-OAEP-256；随机数据密钥信封加密、租户上下文、密钥版本、轮换/撤销；应用于 Webhook 和账号密钥库 | 四个云 REST 合同通过本地 HTTP mock 验证；未使用真实云账号。阿里云采用公共 KMS API，专属实例/国密接口需新增 Wrapper。Azure bearer/各云临时凭据由挂载文件提供并轮换；尚未内置云工作负载身份令牌获取 |

事务边界：数据库内部状态与完成计量原子提交。外部支付/分账/开票采用“持久化意图 + 稳定幂等号 + 对账恢复”；不宣称跨 PostgreSQL 和云服务的 XA 或绝对 exactly-once。远程 Agent 在超时后仍可能继续执行；本地取消确保旧结果无法提交，外部副作用仍须通过 Agent 的幂等/补偿契约管理。

## 本地验收方式

`python3 scripts/test-workflows-e2e.py` 仅接受环境变量 `CONTINUITY_WORKFLOW_TEST_DATABASE_URL` 指向 `continuity_test_*` 或 `continuity_archive_test_*` 测试库。它构建服务、监听 `127.0.0.1:18081`、创建独立演示收款账号、设置模拟支付/编排/KMS、执行 HTTP E2E，然后清理自己启动的服务。不会部署 Pod、删除数据库或修改 Rancher 配置；测试数据库保留作证据。端口被占用时直接退出。

默认执行新功能的两个 E2E。`CONTINUITY_WORKFLOW_TEST_PATTERN='.'` 执行全部 E2E；旧的门户角色测试还需测试库中有匹配的 `CONTINUITY_E2E_PORTAL_STATE` 演示夹具。每个 E2E 使用独立 HTTP 进程，保留正式认证限流规则。不要把生产数据库 URL 填入测试环境。

控制台新增 **编排、分账与开票**：创建需审批的计划 → 批准 → 刷新查看步骤及结果；查看分账可提额、验证服务商收款账户 token、申请提现；输入已付款 invoice_id 和购方资料申请开票、对账、下载蓝/红字票据；显示当前 KMS 模式。审批和财务操作均经服务端鉴权，不由页面自行修改状态。

## 接口顺序

1. `POST /v1/orchestration/runs`，带 `Idempotency-Key`，请求包含 `task_id`（可选）、`steps`、`budget`、`require_approval`。步骤提供 `id/agent/needs/input/max_attempts/cost_limit`；依赖须在前且不重复。`GET /v1/orchestration/agents` 查看管理员配置的执行器。
2. `POST /v1/orchestration/runs/{id}/approve` 或 `/cancel`；`GET /v1/orchestration/runs/{id}` 查询。`GET /v1/tasks/{id}/continuity-package` 拉取共享结果。发起绑定任务的执行需要任务所有者；共享读者可通过原任务 ACL 读取报告。
3. 创建/授权市场交易后，`POST /v1/marketplace/transactions/{id}/checkout`，body 为 `billing_account_id/provider`。返回 `bill/payment_id/payment_link`；集团代付的付款主体与授权在账单中固化，原租户财务与代付方财务沿用现有付款权限；无财务权限的管理员不能发起支付。通过现有支付链接/支付回调完成付款。未付款不能完成付费交易或安装。
4. `POST /v1/marketplace/transactions/{id}/settlement` 固化分账；`POST /v1/commerce/settlements/{id}/reconcile` 对账；`GET /v1/commerce/settlements` 返回最新退款投影、可用额和欠款。
5. `POST /v1/commerce/recipients`，`provider_account_token/kind`（bank/wechat/alipay/paypal），必要时 `/recipients/{id}/reconcile`。只能使用服务商已完成账户归属验证的 token，不能提交银行卡密码。`POST /settlements/{id}/withdraw` 带独立幂等号及 `destination/amount_minor`，预留后由 Worker/对账提交。
6. `POST /v1/billing/invoices/{id}/fiscal`，`buyer_name/tax_id`；`GET /v1/billing/invoices/{id}/fiscal` 按账单权限列出蓝/红票，支持授权租户财务；`POST /v1/commerce/fiscal/{id}/reconcile`；`GET /v1/commerce/fiscal/{id}/document`。退款沿原申请/审批/提交/对账接口执行；已成功退款自动进入红字队列。
7. 账号密钥库：`PUT /v1/security/secrets/{name}`，`value/expected_version`；`GET` 读取；`POST .../rewrap` 重新包裹数据密钥。专用 `secrets:read/secrets:write` 权限，不向仅 tasks:read 的 Agent 开放。访问不记录明文；GET 禁止缓存。

Go/Python/TypeScript/Swift/Kotlin/.NET SDK 新增 createRun/getRun/controlRun/issueFiscal/getSettlement 对应方法，完整参数还可通过已有通用请求方法扩展。幂等号由调用者保存并在重试时复用。

## 部署和扩容

新增迁移 `016_operations` 保存编排、分账、票据和加密密钥库，带到期索引、run 时间游标及任务查询索引。Helm 应用和基础设施 chart 版本均为 `0.3.0`，无需新增数据库、队列或 Operator：现有 PostgreSQL 保存业务状态，已有 NATS JetStream 继续承担附件归档队列。Worker 随 API 副本运行；CAS/租约防止多个副本提交同一步骤。编排调度持久化扫描不是替换上传链路的 JetStream。

开发叠加 `deploy/environments/dev/workflows.yaml` 即启用模拟执行器和模拟 KMS；**本轮没有把这个 overlay 发布到现有本地 release**。生产 profile 引用 `continuity-workflows` 和 `continuity-kms` Secret，分别挂载 `/run/workflows`、`/run/kms`，只读 `0440`，纳入 Secret rollout checksum。Secret 需有 `config.json` 和其中引用的凭据文件。预检验证文件映射并拒绝生产模拟模式。

- 编排/商务配置示例：`deploy/clouds/workflow-config.example.json`。只接收管理员配置的 HTTPS A2A 地址；任务内容不能指定请求地址或运行宿主机命令。
- 四云 KMS：`deploy/clouds/kms-config.examples.json`。AWS/阿里/腾讯文件结构为 `access_key_id/secret_access_key/session_token`；Azure 为 `bearer_token`。实际凭据不写入 values、仓库或日志。
- `CONTINUITY_WORKFLOW_CONFIG[_FILE]`、`CONTINUITY_KMS_CONFIG[_FILE]` 可直接用于 VM/容器。配置启动时解析，凭据文件由云 Secret/CSI 更新，KMS 每次调用重新读取。
- KMS 轮换：先新增唯一版本 ID，保留旧 key，修改 current 或 `tenant_keys:{"acct_id":"new-version"}`，滚动服务；对存量密钥调用 rewrap；确认旧 key 不再被引用后在云 IAM 和配置中撤销。不得把旧版本 ID 指向另一把密钥。Webhook 密钥可轮换重建；存量 v1 本地主密钥密文仍能读取迁移。
- 备份必须包含 PostgreSQL、仍被引用的云 KMS key/本地主密钥、Secret 配置及对象存储。撤销旧密钥前先做恢复验证。

MinIO 与 mc 的旧公开镜像在本轮 CI 中已无法从 Docker Hub/Quay 拉取。测试部署改为上游固定提交源码构建，保留源码快照、许可证和 Helm 入口；Compose 会构建 MinIO，Rancher 安装脚本先构建两个指定镜像。配置与手动构建方式见 `deploy/images/objectstore/README.md`；本轮构建只在 GitHub Runner 执行，未替换本地 MinIO。

发布包包含两个 chart、已有六项可选基础依赖、迁移、六语言 SDK、客户端二进制、VSIX、协议与示例。镜像和真实云资源/服务商账号不在压缩包内；部署需构建或拉取镜像及准备外部服务。它不是离线镜像包。

## 商务网关扩展契约

`POST {commerce_url}/effects`，Bearer 服务凭据、`Idempotency-Key` 与 body.idempotency_key 相同。Effect 包含 `kind/account_id/currency/amount_minor/reference/destination/fields`；kind 为 `split/payout/reverse_split/verify_recipient/invoice/credit_invoice`。返回必须包含相同幂等号/金额/币种、`provider_id`、`status=pending|succeeded|failed`、`simulated`；出票成功另需 `document`。网关必须持久化幂等记录，同号异参拒绝；不明确的渠道响应保持 pending，重试必须查询或恢复原单。网关还应确保平台与卖方合计等于收款净额，退款冲回使用累计分配避免四舍五入漂移。

网关承接微信服务商分账、支付宝分账、Stripe Connect、PayPal 平台等各自不同的商户开户、收款人验证和监管要求；本轮没有把通用 JSON 直接冒充这些厂商的官方请求。原支付适配器仍负责支付网络签名/回调/原路退款；本模块处理已确认款项的运营与结算。

## 规范来源

- [A2A 1.0 规范](https://a2a-protocol.org/v1.0.0/specification/)；[官方 Go SDK](https://github.com/a2aproject/a2a-go)
- [MCP 2026-07-28 版本协商](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning)；[Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)
- [AWS Encrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Encrypt.html)、[阿里云 KMS 公共参数](https://www.alibabacloud.com/help/en/kms/key-management-service/common-parameters-1)、[腾讯云 Encrypt](https://cloud.tencent.cn/document/product/573/34420)、[Azure Key Vault Encrypt](https://learn.microsoft.com/en-us/rest/api/keyvault/keys/encrypt/encrypt?view=rest-keyvault-keys-2025-07-01)

## 本轮实际验收结果

- `GOMAXPROCS=2 go test -race -p 2 ./...` 和 `go vet -p 2 ./...` 通过；最后修改的 HTTP/SDK/适配器包再次通过 race 回归。环境变量门控的数据库/HTTP 测试另行启用执行，未把默认跳过当作验收。
- 独立数据库 `continuity_test_closure_20261107`：7 条 HTTP E2E 全部通过，覆盖旧支付/退款/门户/资金台账，以及新增标准协议和编排/分账/KMS 链路。新链路实际检查 100 元付款、10 元佣金、60 元提现成功、50 元退款后的 15 元待追缴、分账冲回、蓝字与红字模拟票据下载。
- Linux CI 反馈已修复：服务层统一账期微秒精度，归档测试结束恢复全局 admission 配置，更新可解析的 Trivy Action，并在干净 CI 环境安装 Python SDK 声明的依赖；同库运行三项 integration 后再跑新功能 E2E 的夹具已在全新专用数据库通过。
- 3 条真实 PostgreSQL/JetStream integration 通过：事件事务与断线恢复；签名对话与 SDK 内容 → Outbox → 持久 JetStream 重启 → 双消费者 → 对象下载/失败重放；编排恢复、多个存储连接抢占与用量事务回滚。测试只使用专用测试库与进程内临时 broker。
- 官方 A2A Go SDK `v2.6.0` 通过 Agent Card/SendMessage/GetTask/ListTasks；远程 A2A 模拟器验证“服务端已接收但客户端遇到 503”后沿用原 messageId 恢复。
- Go/Python/TypeScript/Swift/Kotlin/.NET SDK 测试与编译通过；CodeBuddy bridge 测试通过。VSIX 已重新打包，含本轮 Python SDK 和执行报告恢复逻辑。
- 项目工具链固定 Go 1.27.3（模块最低 Go 1.26.10），容器构建使用同版本；升级 x/net 至 0.62.0，同时包含 HTTP/2 与 HTML 解析/渲染安全修复。仅本项目配置与模块缓存变化，系统 Go 未升级。修复依据：[Go 证书信任公告](https://pkg.go.dev/vuln/GO-2026-6809)、[HTTP/2 公告](https://pkg.go.dev/vuln/GO-2026-6617)、[HTML 解析公告](https://pkg.go.dev/vuln/GO-2026-6860)、[HTML 渲染公告](https://pkg.go.dev/vuln/GO-2026-6866)。
- `gosec` 静态安全扫描、`go mod verify` 模块校验以及变更文件凭据特征扫描通过。
- GitHub Runner 已从固定源码构建 MinIO 与 mc，通过 S3 建桶、上传/下载内容比对以及真实 Compose 跨设备接续 E2E；未配置支付/财务夹具的 Compose 测试显式跳过，这些能力由上述独立 HTTP E2E 验收。
- dev/staging/production Helm 合同、四云 profile 渲染/生产危险配置拒绝、实际 HTTP 响应 JSON Schema 校验通过。NATS、MinIO、可选监控/伸缩依赖继续通过渲染检查。
- 临时内存服务 `127.0.0.1:18082` 的浏览器验收：账号登录、审批、三个 Agent 完成接力、分账展示、模拟开票下载；下载内容解析为 JSON 且 `legally_valid_tax_invoice=false`。验收服务已停止。
- Rancher 前后均 Ready，无 Memory/Disk/PIDPressure；96/110 活跃 Pod，0 个未分配 Pending。项目 API、归档 Worker、NATS、PostgreSQL、MinIO 的运行 Pod 重启次数未增加；没有发布 Helm release、重启集群、创建额外 Pod或改变其他项目。

本轮生产验收仍需真实商户/税务服务商账号与云 KMS 凭据，验证各渠道准入、对账/到账、真实票据格式、IAM/工作负载身份及运营容量。以上实现和模拟合同测试不能代替这些外部验收，也没有宣称全量 A2A/MCP 可选特性认证。

交付版本为 `0.3.0-closure-20261107`。发布包内 `RELEASE.json` 记录精确 Git 提交和工作区是否有未提交内容，`SHA256SUMS` 覆盖包内文件；外层 `.sha256` 校验压缩包。源码中包含 `scripts/test-workflows-e2e.py`、全部单测/集成测试及 CI 配置，可按上述隔离方式复验。
