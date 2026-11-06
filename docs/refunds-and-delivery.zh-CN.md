# 原路退款、票据和客户端交付

本次代码在原支付账单上增加独立退款单，现有账单金额和支付回执不修改。票据包括消费账单、付款回执、退款贷项凭证以及合并可打印 HTML；均明确标注非税务发票。正式税务开票须选定开票服务商、提供主体资质并独立接入，不能由本地模拟生成合法税票。

## 页面验收

1. 租户登录 → 租户费用中心 → 已付款账单 → 退款与票据。
2. 填写金额（以分为单位）和原因 → 申请原路退款。可申请部分退款，剩余额度扣除待审批/处理中退款。
3. 运营方登录 → 收款与提现 → 客户退款处理 → 选择账单 → 填写审批意见 → 批准或驳回。
4. 批准后点击提交原路退款。Demo 进入处理中，可选择模拟成功/失败；不会转账。真实渠道以原订单查单结果确认，不能指定其他收款人。
5. 返回租户账单 → 查询退款结果 → 下载账单与退款凭证。部分退款保留 paid；全部实际收款退完后变为 refunded。每笔成功退款只有一份贷项凭证。
6. 模拟商户资金展示已退款和退款占用；这些金额从可用/待结算资金中扣除，原手续费保留。退款后存在资金缺口时可用额可以为负并阻止新增模拟提现。

## 一致性与失败处理

- 退款单与金额、币种、原支付单、申请人和幂等键绑定。相同键改变内容会被拒绝。申请、状态和贷项与账单 CAS 一起提交。
- 租户只能申请和查看/查单。批准、提交、模拟结果需要指定商户账号和独立 treasury 权限；正式退款申请人和批准人必须不同。
- submitting 状态在网络请求之前持久化。未知结果继续占额度，不能换单重试。Stripe/PayPal 缺渠道退款号时仅在首次提交后一小时内重试相同幂等请求；超过窗口保留人工核查。
- 每分钟扫描非 demo 未完成退款；至少间隔两分钟、每轮最多十笔、每笔最多二十秒、自动尝试有上限。主动查单仍可恢复结果。退款查询是结果确认路径，当前没有独立退款 webhook 接收器；微信商户须在商户平台配置退款通知地址。
- 成功终态不会被迟到 pending/failed 覆盖。未知/异常状态进入 review，金额继续占用；不将受理成功当作最终退款成功。
- PostgreSQL 退款路由有渠道退款号唯一约束。商户资金提交时锁定关联账单版本，拒绝使用退款前的旧余额提交提现预约。
- 迁移 015 为增量表和索引。回滚应用保留退款、原账单和审计数据。

## 渠道协议验收

本地 HTTP 替身使用各渠道的正式请求结构：微信 RSA APIv3 请求及签名响应、支付宝 RSA2、PayPal OAuth/幂等请求、Stripe PaymentIntent/幂等键。没有真实商户密钥，也没有实款交易。

官方接口依据：

- [微信退款开发指引](https://pay.wechatpay.cn/doc/v3/merchant/4013071031)
- [支付宝统一收单退款](https://aipay.alipay.com/docs/subscribe/api-list/other-basic-interfaces/alipay-trade-refund.html)
- [PayPal Capture refund](https://developer.paypal.com/api/payments/v2/captures-refund)
- [Stripe refund](https://docs.stripe.com/api/refunds/create)

## 跨操作系统安装与新增 Agent

Go、Python、TypeScript、Swift、Kotlin、.NET 客户端均提供账单预览/生成/查询、创建付费链接、支付查单、申请退款、退款查单和 HTML 凭证下载。付款与退款需要由调用方持久化幂等键，网络重试复用原键；SDK 不自动批准退款或自动打开收银台。运营审批保持在后台权限边界内。

例如 Python：`client.create_payment(invoice_id, "demo", key)` 返回 `payment_link`，由宿主在系统浏览器打开；付款后 `reconcile_payment` 查单；`request_refund(invoice_id, payment_id, 35, "未使用部分", refund_key)` 申请退 35 分，后台审批后用 `reconcile_refund` 查询，`invoice_statement` 下载非税务票据。Swift/Kotlin 等保持同样的服务端语义。

`scripts/agent-install.py detect` 读取操作系统、PATH 和 macOS 应用目录，报告 Agent 安装路径。`install --workspace PATH --agent claude|codebuddy|cursor|gemini|opencode --executable ABSOLUTE_AGENTCLOUD` 默认仅展示计划；加 `--apply` 后合并项目 MCP 配置。`--agent all` 可批量配置。

安装保留其他服务器和用户设置，不在配置中保存访问 Token。相同配置重复执行不变；冲突配置拒绝覆盖。备份位于工作区 `.continuity-install/`，`uninstall ... --apply` 仅在配置未被后续修改时恢复备份。JSONC 或不匹配结构会报错，避免丢失注释或已有设置。

安装器中的 `claude` 指 Claude Code，`codebuddy` 指 CodeBuddy Code CLI，两者共享根目录 `.mcp.json` 和同一份恢复记录。检测到 `Claude.app` 或 `CodeBuddy CN.app` 会单列为桌面应用，不能据此认定 CLI 已安装。CodeBuddy IDE 使用独立的 VSIX/Hook 安装流程；Claude Desktop 需使用其官方扩展配置入口。宿主仍可能要求用户确认项目工具信任。

配置位置依据：[Claude Code](https://code.claude.com/docs/en/mcp)、[CodeBuddy CLI](https://www.codebuddy.ai/docs/cli/mcp)、[CodeBuddy IDE](https://www.codebuddy.cn/docs/ide/User-guide/MCP)、[Cursor](https://cursor.com/docs/mcp)、[Gemini CLI](https://geminicli.com/docs/tools/mcp-server/)、[OpenCode 稳定版](https://opencode.ai/docs/mcp-servers/)。安装器拒绝直接改写 JSONC；OpenCode v2 预览配置与稳定版不同，需新增独立目录项后接入。

配置 MCP 提供工具调用入口，不代表厂商会自动把所有对话发给 MCP。CodeBuddy 项目 Hook 与 VSIX 已另行提供自动采集；其他 Agent 通过明确的导出/Hook 输出和 adapter capture 接入。Codex 使用现有 `plugins/agent-continuity` 原生包及宿主的安装入口。iOS/Android 自研 App 嵌入 Swift/Kotlin SDK；封闭第三方 App 需要官方 Remote MCP/OAuth 支持或用户导出。

增加新 Agent：实现 `pkg/adaptersdk.Adapter`，声明 Detect/Discover/Capture/Resume 能力，在 CLI 注册适配器并添加兼容性测试；MCP 型接入在 `integrations/agent-catalog.json` 增加条目和配置模板即可复用安装器。统一事件、任务、附件、签名、队列归档与计量接口保持不变。恢复分为兼容原生会话恢复和 portable rehydrate，不能把上下文注入宣传为跨厂商私有会话数据库克隆。

## 部署包

先运行 `scripts/package-codebuddy-extension.sh`，再运行 `python3 scripts/package-release.py --version VERSION`。包包含源码快照、应用与依赖 Chart、迁移、环境配置模板、六种桌面平台客户端（macOS/Linux/Windows × amd64/arm64）、CodeBuddy VSIX、五套语言 SDK 源码及 Go SDK、安装器、运行手册和 SHA256SUMS。RELEASE.json 标识 Git commit、是否含未提交变更和目标平台。

这是联网安装包。容器镜像通过 Dockerfile 构建或从镜像仓库拉取；没有把商户密钥、云密钥、本地数据库或用户对话打入包。离线交付还需按目标架构导出应用和全部依赖镜像。在开发机使用 dev profile；生产 profile 的三节点、RWX、证书、备份和外部账号仍需在实际环境准备。

本机部署：`make deploy-dev`。部署前备份数据库；应用升级运行增量迁移；失败可回滚 Helm，保留数据库和 PVC。扩容和监控依赖的详细安装次序见 `archive-production.md`。
