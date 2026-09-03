# 支付模块本地验收记录 — 2026-09-03

## 结论与范围

账单、支付单、独立付费链接、模拟收银台、回执、查单和审计已在本地 Rancher Desktop Kubernetes 完成部署与验收。微信 Native、支付宝/Alipay 当面付、PayPal Orders v2、Stripe Checkout 的适配器通过本地协议测试。

**这不是商户沙箱或生产收款验收。** 本地只启用 `demo`，`allow_live:false`，价格表为测试价格。未加载真实商户密钥、未公开 HTTPS 回调域名、未发起真实扣款或退款。支付宝与 Alipay 是同一个渠道。

## 最终部署

- Kubernetes context：`rancher-desktop`
- Namespace：`agent-continuity-dev`
- Helm release：`agent-continuity`，revision **16**，状态 `deployed`
- 构建镜像标识：`sha256:5b2360c23b4780327dfbfafded451903a0ae19c30103d90d02a65e8fb04f11c4`
- 应用 Pod：`agent-continuity-5df848449-5s2ht`，Ready 1/1，0 次重启
- PostgreSQL：`agent-continuity-postgres-0`，Ready 1/1；检查前后均为 1 次历史重启，本次没有新增重启
- 原 PostgreSQL 与 Artifact PVC 均保持原卷并为 Bound；没有删除/重建数据卷，没有修改 Rancher Desktop 全局设置
- 数据库迁移：`001_initial` 至 `010_payments`，新支付模块使用新增表
- 本机入口：`http://127.0.0.1:18080/` → **用量与计费**
- Readiness：HTTP 200，`{"status":"ready"}`

## 数据保护

迁移前生成 PostgreSQL 自包含备份，且使用 `pg_restore --list` 验证可读取目录：

`/Users/jansurezhang/go/src/agent-continuity-master/.data/payment-backup-xqLtLc/postgres.dump`

SHA-256：`c91fb558f664451e78b12844b24facba94117d82885592a3acedfc8cd07799c7`。

此次没有执行数据库恢复演练或破坏性回滚；备份目录检查不能冒充恢复演练。原任务、设备、Agent 和用量数据保留。

## 自动化测试

| 验证 | 结果与边界 |
|---|---|
| `GOMAXPROCS=4 go test -p 4 ./...` | PASS；依赖外部环境的测试按自身环境条件跳过 |
| `GOMAXPROCS=4 go test -race -p 2 ./internal/payments ./internal/billing ./internal/httpapi` | PASS |
| `GOMAXPROCS=4 go vet -p 4 ./...` | PASS |
| `GOMAXPROCS=4 ./scripts/smoke-k8s.sh` | revision 15 与最终 revision 16 上 PASS：`TestBillingPaymentJourney`、`TestFullContinuityJourney` |
| 支付协议测试 | RSA2/APIv3/HMAC 签名、AES-GCM 解密、PayPal capture/官方验签调用、金额与商户校验；使用本地 HTTP 协议替身，不代表真实商户联调 |
| 业务与安全测试 | 超过 1,000 条的全窗口聚合、金额舍入/溢出、重复账期拦截、测试/正式隔离、并发重复成功仅一份回执、越权/链接过期/撤销、失败单重试 |
| JS / shell / Chart / OpenAPI | JS 语法与脚本语法检查通过；Helm lint、可选 Secret 挂载渲染通过；OpenAPI YAML 解析通过 |

K8s 支付 E2E 使用独立测试账户，验证 PostgreSQL 持久化的出账、幂等下单、收银台、四个并发成功通知、唯一回执、CSV/HTML 导出、跨账户拒绝访问与链接撤销。不使用真实网络支付渠道。

## 浏览器与演示数据

经用户明确许可，将本地演示账号的现有凭据用于本机管理页登录。凭据未写入报告、代码或浏览器持久存储。

- 中文计费页加载：888 条当前账户可见用量、2 个计费账户、4 个待配置网络渠道与 1 个模拟渠道。
- `sh scripts/seed-billing-demo.sh` 运行成功；再次运行不重复出账或重复生成回执。
- 演示账单以已有用量的三个非重叠时间窗口出账，价格明确为测试价格。

| 账单末尾标识 | 测试金额 | 验收后状态 |
|---|---:|---|
| `F9A71CB96A2DCF2FCE4742C8` | CNY 2.72 | 已支付；1 次支付、1 份回执 |
| `A9179824DA6ADD4D70391E08` | CNY 2.65 | 待支付；1 次待付款尝试 |
| `C291D9884B8FA65FE2989F8F` | CNY 2.67 | 账单待支付；保留 3 次失败支付尝试，支持再次发起支付 |

浏览器实测：查看消费快照；失败单主动查单仍保持失败；生成新付款链接；打开独立收银台并模拟失败；刷新管理页同步状态；失败后再次生成不同支付单；展开审计；下载消费 CSV 和可打印 HTML 回执。下载文件存在且内容与账单金额一致。中英文切换后仍停留于计费页并重新加载数据。检查浏览器错误/警告日志为空。

窄屏截图检查发现长账单号挤压状态标签，已增加安全换行并在 revision 16 重新加载验证：账单号和已支付标签完整显示。消费四列表格保留横向滚动。页面最终停留在中文已支付账单详情。

## 必须由外部条件完成的上线门禁

1. 商户准入、相关支付产品签约、地域/币种资格和正式收款授权。
2. 商户密钥与证书通过专用 Secret 配置；不要在任务对话或 Git 中提供密钥。
3. 可达的 HTTPS 域名、签名回调、证书轮换与各渠道实际联调。
4. 业务负责人批准正式定价；默认演示价格不是产品报价或 AI Token 成本。
5. 退款审批、拒付、银行到账/渠道日结对账、税务发票、自动续费、市场分账仍需独立实现和验收，不能用当前回执或订单查单冒充。

接口、配置、扩展方式和官方协议链接见 [支付接入文档](payments.zh-CN.md)。
