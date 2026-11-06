# 支付、消费明细、账单与回执

2026-11-06 更新：已增加原路退款申请、运营审批、部分/全额退款、恢复查单及非税务贷项凭证。最新操作和渠道模拟验收见 [退款与交付说明](refunds-and-delivery.zh-CN.md)。

## 交付范围与真实收款边界

本模块提供通用 Provider 接口、微信支付 Native APIv3、支付宝/Alipay 当面付 RSA2、PayPal Orders v2、Stripe Checkout，以及独立的 `demo` 模拟渠道。支付宝与 Alipay 是同一渠道，不能配置成两个重复收款入口。

已实现：全窗口用量聚合、服务端价格表、消费报价、不可变账单快照、幂等支付单、限时/可撤销付款链接、独立收银台、二维码、渠道签名验证、PayPal capture、订单查询、有限自动补偿、回执及 CSV/HTML 下载、账户隔离和支付审计。

本地环境只启用独立模拟渠道，不会接触真实资金。网络适配通过模拟 HTTP 协议服务测试；未提供真实商户凭据之前，不声称完成商户沙箱或生产验收。

新增独立的[收款与提现模块](treasury.zh-CN.md)：完整模拟付款回执 → 渠道结算 → 本人账户 → 冻结/审批 → 转出/到账/退回，并提供默认关闭的 Stripe 银行 Payout 协议适配。微信、支付宝、PayPal 的真实提现仍走官方商户后台，不声称全部自动转出已完成。

以下不是现有接口的承诺：微信 JSAPI/小程序/APP 支付、支付宝 APP/WAP、订阅自动续费/代扣、所有渠道自动打款、拒付管理、渠道日结文件/银行到账对账、税务发票、外汇换汇，以及市场佣金分账。支付查单发现 REFUND 进入“需核查”；退款完成以独立退款单的渠道查询为准。市场交易账本尚未绑定本模块的账单和收款单，不能将其当成已结算资金。

## 领域模型与可靠性

`UsageRecord → BillingLine/Invoice → Payment → verified provider result → PaymentReceipt`

- 金额统一为币种最小单位的 int64，不使用浮点。当前仅支持 CNY/USD/EUR 两位小数币种；不隐式换汇，未来新增 JPY/KWD 等须先提供币种精度策略。
- 计量按 `[period_start, period_end)`、付款账户及组织作用域聚合，使用数据库 SUM/COUNT，不受页面 1,000 条明细上限影响。
- 每个账单冻结价格版本、数量、单位、单价、计量笔数与行金额。按每个计量指标的 `ceil(quantity * unit_price_minor / unit_size)` 收费，最后累加。未知指标、单位不匹配、负值、溢出、超过上限均拒绝出账。
- 默认演示价格：每次 API 变更 1 分，每 1,024 上传字节 1 分，按上述公式取整。不是正式产品定价，也不是存储容量月租或第三方 AI Token 成本。
- 测试账单不能使用 live 渠道付款；正式账单不能使用模拟或沙箱渠道。正式价格表必须由运营方批准，服务器配置 `test_mode:false`。
- 出账使用账户级事务 advisory lock，阻止同账户、同组织、同测试/正式环境的有效账期重叠，防止同一用量重复收费。迟到用量不会改变已出账快照；需独立差额账单/调账模块承接，不得修改原账单。
- 一个账单是一个事务聚合：PostgreSQL `billing_documents` 保存账单、支付状态、回执、审计，`version` CAS 原子更新。`billing_payment_routes` 提供支付单/渠道订单索引和渠道流水全局唯一约束。不同账单可以并发处理，不使用全局支付锁。
- 每次账单最多 10 次支付尝试，同一时刻最多一个未确定/待付/已授权支付。超时不会直接创建另一笔扣款。重试使用原持久化 payment_id 作为支付网关幂等键；过期的未确定请求只能查单，不能重新下单。
- 回调和主动查单都校验订单号、商户、金额、币种。重复成功通知只产生一份回执；晚到 pending/expired 不能覆盖成功。不同尝试的额外成功扣款会保留回执并将账单置为 disputed，要求人工退款核查。
- 自动补偿每分钟扫描，每笔至少间隔两分钟，单轮最多 10 笔、串行调用、单笔超时 20 秒；20 次后停止自动查询，保留人工查单入口。模拟渠道不产生外网访问。多副本通过 CAS 认领并使用网关幂等请求防止重复 capture。
- 付款链接是独立 HMAC 能力凭证，有效期 35 分钟，可撤销；不包含工作区 JWT。撤销链接不会取消已经创建的渠道订单，收银台明确区分这一点。商户签名/回调密钥不通过 API/UI 返回。
- 回执是历史支付事实，不是税务发票、银行流水或到账证明。零金额账单无需支付，也不会生成虚假回执。

## 渠道实现矩阵

| 渠道 | 收银方式 | 创建/查单 | 成功依据 | 所需配置 |
|---|---|---|---|---|
| 微信支付 | Native 扫码 | APIv3 Native / out-trade-no | RSA-SHA256 验签 + AES-256-GCM 解密；商户/App 校验 | app_id、merchant_id、商户证书序列号、私钥、APIv3 key、平台公钥 ID→PEM |
| 支付宝/Alipay | 当面付扫码 | trade.precreate / trade.query | RSA2 请求/响应与异步通知验签；app/seller 校验 | app_id、seller_id、应用私钥、支付宝公钥；sandbox 开关 |
| PayPal | 官方托管 Checkout | Orders v2 create/query/capture | 商户校验；CAPTURE COMPLETED；官方 verify-webhook-signature | client_id、client_secret、merchant_id、webhook_id；sandbox 开关 |
| Stripe | 官方托管 Checkout | Checkout Session create/query | Stripe-Signature HMAC；payment_status=paid；livemode 匹配 | secret_key、webhook_secret；sandbox 开关 |
| demo | 本地独立模拟页 | 本地状态转换 | 显式模拟按钮，不接触资金 | demo_enabled:true，仅测试账单 |

微信只接 APIv3 正式域名，不能将模拟器标为微信沙箱。PayPal capture 只在服务端查到 APPROVED 或验签后的授权通知触发，不信任浏览器 success 参数。Stripe 只确认实际 paid，不能把 Checkout completed/pending 当成收款成功。

## 安装与配置

环境变量支持内容或文件二选一：

- `CONTINUITY_PAYMENT_CONFIG` / `CONTINUITY_PAYMENT_CONFIG_FILE`
- `CONTINUITY_BILLING_PRICE_BOOK` / `CONTINUITY_BILLING_PRICE_BOOK_FILE`

默认无渠道。开发 Helm values 只启用 `demo`，PublicBaseURL 为 `http://127.0.0.1:18080`。网络渠道必须使用可访问的 HTTPS 回调域名，HTTP loopback 仅允许模拟模式。

配置示例（占位符不可直接上线，真实密钥不要粘贴到任务对话或提交 Git）：

```json
{
  "public_base_url": "https://billing.example.com",
  "allow_live": false,
  "demo_enabled": false,
  "alipay": {
    "app_id": "SANDBOX_APP_ID", "seller_id": "SANDBOX_SELLER_ID",
    "private_key_file": "/run/payments/alipay-app-private.pem",
    "public_key_file": "/run/payments/alipay-public.pem", "sandbox": true
  },
  "paypal": {
    "client_id": "SANDBOX_CLIENT_ID", "client_secret": "SANDBOX_SECRET",
    "merchant_id": "SANDBOX_MERCHANT_ID", "webhook_id": "SANDBOX_WEBHOOK_ID", "sandbox": true
  },
  "stripe": {"secret_key": "sk_test_REPLACE", "webhook_secret": "whsec_REPLACE", "sandbox": true}
}
```

微信配置需要在正式收款审批后设置 `allow_live:true`，并添加：

```json
{"wechat":{"app_id":"REPLACE","merchant_id":"REPLACE","certificate_serial":"REPLACE","private_key_file":"/run/payments/wechat-private.pem","api_v3_key":"REPLACE_WITH_EXACTLY_32_BYTES_KEY","platform_key_files":{"PUB_KEY_ID_REPLACE":"/run/payments/wechat-platform.pem"}}}
```

`platform_key_files` 支持同时保留多个平台公钥/证书 ID 以便轮换。私钥必须是 RSA 2048 位以上 PEM；生产优先使用独立密钥托管/签名服务扩展，不通过前端输入私钥。

价格表格式（这里只示意格式；单价需运营批准）：

```json
{"version":"approved-price-version","test_mode":true,"currencies":{"CNY":[{"metric":"api.mutation","description":"API 变更操作","unit":"operation","unit_size":1,"unit_price_minor":1},{"metric":"artifact.bytes","description":"产物上传字节","unit":"byte","unit_size":1024,"unit_price_minor":1}]}}
```

Kubernetes：创建专用 Secret，包含 `config.json`、`price-book.json`、所引用的 PEM 文件，然后设置 `payments.existingSecret` 为其名称。Chart 将其只读挂载到 `/run/payments`，文件权限 0440。Chart 不创建商户凭据、不修改 Rancher Desktop 配置，不将私钥放入 ConfigMap。Secret 更新后按受控流程滚动应用；数据库无需重启。

## 回调与 API

| 方法与路径 | 用途 |
|---|---|
| GET `/v1/billing/providers` | 非敏感渠道状态、支持币种、能力、价格表 |
| POST `/v1/billing/quote` | `{billing_account_id,period_start,period_end}` 消费预览 |
| POST `/v1/billing/invoices` | 同上；要求 `Idempotency-Key`，出账 |
| GET `/v1/billing/invoices?limit=200` | 最近账单；按账户隔离 |
| GET `/v1/billing/invoices/{id}` | 行明细、支付单、回执、审计、有效分享链接 |
| GET `/v1/billing/invoices/{id}/export` | CSV 消费快照，防表格公式注入 |
| POST `/v1/billing/invoices/{id}/void` | 仅允许无支付尝试的未付账单作废 |
| POST `/v1/billing/invoices/{id}/payments` | `{provider}` + `Idempotency-Key`；返回 payment/payment_link |
| POST `/v1/billing/invoices/{id}/payments/{pid}/reconcile` | 主动查单；PayPal APPROVED 时幂等 capture |
| POST `/v1/billing/invoices/{id}/payments/{pid}/revoke-link` | 撤销链接，不取消渠道订单 |
| POST `/v1/billing/invoices/{id}/payments/{pid}/simulate` | 仅 demo：`{outcome:succeeded/failed/canceled}` |
| GET `/v1/billing/invoices/{id}/receipts/{rid}?format=html` | 受鉴权的可打印付款回执；默认 JSON |
| GET `/pay/{pid}?token=...` | 独立支付能力链接，不使用账户 JWT |
| POST `/v1/payments/webhooks/{wechat,alipay,paypal,stripe}` | 原始请求体验签，无 JWT；上限 256 KiB |

账单读接口要求 `billing:read`，报价/出账/支付操作要求 `billing:write`。只有付款账户可访问。通知只接受配置渠道的合法签名；Alipay 成功应答 `success`，其他渠道 204。未经验证的结果不产生回执。

运营处理：成功以回执和渠道流水为准；pending/creating 先查单，不能让用户重复下单；disputed/重复扣款保留所有证据。退款必须经授权申请、审批和明确提交；自动补偿仅恢复已提交的原退款单，不自动批准新的退款。

## 扩展新渠道

实现 `internal/payments.Provider` 的 `Info/Create/Query/Capture/VerifyNotification`，启动时 `Registry.Register`，不会改动核心账单金额逻辑。`Capture` 不适用时返回 `ErrUnsupported`。能力矩阵必须只声明实际支持的能力，`Result` 统一映射状态、金额、币种、商户订单/渠道订单/交易流水；未知状态 fail closed。渠道专有密钥从 Secret 加载，端点固定在适配器中，不接受用户输入的任意网关 URL。

新币种须增加币种精度/边界测试；新支付方式（微信 JSAPI、APP 等）需要扩展客户端动作结构与商户授权，不能复用 Native 二维码冒充原生 App SDK。退款使用独立 Refund/CreditNote 聚合及审批权限、幂等退款单和到账状态；原金额和原付款回执保留。

## 不同设备与 Agent 的统一接入

服务端 Go SDK 位于 `pkg/client/billing.go`，提供 `QuoteInvoice`、`IssueInvoice`、`ListInvoices`、`GetInvoice`、`CreatePayment`、`ReconcilePayment`。其他语言可按 OpenAPI 的同一套 HTTP 接口接入；本次不声称给每款原生 App 提供了独立支付插件。

桌面端、移动端与 Agent 客户端只选择已存在账单和已配置渠道，并为一次逻辑支付持久保存幂等键；服务端确定金额和环境。客户端使用返回的 `payment_link` 打开独立收银台，返回后查询账单状态。不能信任客户端的“付款成功”参数，不能向客户端下发商户私钥，也不能在网络超时后自动更换幂等键重复创建支付。

当前 Native/当面付是扫码流程；PayPal/Stripe 是托管收银台流程。App 内购、微信小程序/JSAPI 和原生支付 SDK 不是同一种集成，需要各平台独立能力和验收。面向不同商户主体的多租户代收/分账也尚未实现，不应把账户隔离等同于支付机构的商户资金隔离。

## 验证

```sh
GOMAXPROCS=4 go test ./internal/payments ./internal/billing ./internal/httpapi
GOMAXPROCS=4 go test -race ./internal/payments ./internal/billing
./scripts/smoke-k8s.sh
sh scripts/seed-billing-demo.sh
```

测试覆盖协议请求、RSA2/APIv3/HMAC 验签、AES-GCM 解密、PayPal capture/官方验签、金额舍入/溢出、完整用量汇总、账期重叠、并发重放只生成一份回执、跨账户越权、过期/撤销能力链接、CSV/回执导出及 K8s PostgreSQL 持久化。Demo 脚本生成已付/待付/失败三种可交互状态，并保留幂等键。

真实渠道上线前仍须：商户准入与产品签约、地域/币种资格确认、正式价格批准、密钥托管与轮换、HTTPS 回调可达性、各商户沙箱小额完整链路验证、退款/拒付/对账运营流程和明确生产收款授权。

本地执行结果、部署版本和演示账单见 [2026-09-03 验收记录](payment-validation-2026-09-03.md)。

## 官方协议依据（核对日期：2026-09-03）

- [微信 Native 开发指引](https://pay.wechatpay.cn/doc/v3/merchant/4012791891)
- [微信 APIv3 签名/验签](https://pay.wechatpay.cn/doc/v3/merchant/4012365352)
- [支付宝 trade.precreate 官方接口](https://developer.alibaba.com/docs/api.htm?apiId=862&docType=4)
- [支付宝异步通知验签](https://aipay.alipay.com/docs/ai-web-app-payment-qianyi/api-list/async-notify-verify.html)
- [PayPal Orders v2](https://developer.paypal.com/api/rest/integration/orders-api)
- [PayPal Webhook 验证](https://developer.paypal.com/api/webhooks/v1/verify-webhook-signature-post/)
- [Stripe Checkout Sessions](https://docs.stripe.com/api/checkout/sessions)
- [Stripe Webhook 签名](https://docs.stripe.com/webhooks)
