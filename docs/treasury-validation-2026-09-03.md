# 收款与提现验收记录 · 2026-09-03

## 交付状态

本地完整模拟链路已实现并部署；原支付、消费、账单、支付链接和回执保留。不是全部渠道的真实自动提现已上线：Stripe 自有银行转出仅完成默认关闭的协议适配与本地假网关测试；微信/支付宝/PayPal 真实提现仍走官方后台。未进行商户沙箱或实款验收，未发送真实付款/转账。

- 本地项目：`/Users/jansurezhang/go/src/agent-continuity-master`
- 控制台：`http://127.0.0.1:18080/` →「收款与提现」。已用现有本地演示账户登录验收。
- Rancher context：`rancher-desktop`；namespace：`agent-continuity-dev`。
- 最终 Helm revision：**19**，应用 `agent-continuity-fb7f859d7-w2rh5`，1/1 Ready、0 次重启。
- PostgreSQL 未重启：原 `agent-continuity-postgres-0`，1/1 Ready；1 次旧重启计数未增加。
- PVC 两个均 Bound；节点 Ready，MemoryPressure/DiskPressure/PIDPressure 均 False。
- 只更新本项目应用；构建限制 GOMAXPROCS=2，没有调整 Rancher Desktop 资源、系统时钟或全局 Kubernetes context。
- 新增 `011_treasury` 独立表，旧账单无破坏性迁移；只配置演示运营方，`demo_enabled=true`、`stripe_api_enabled=false`。

## 已执行验证

| 验证 | 结果 |
|---|---|
| `GOMAXPROCS=2 go test -p 2 ./...` | 通过；外部环境测试在此命令下按配置跳过，另行执行下述本地 E2E |
| `GOMAXPROCS=2 go vet -p 2 ./...` | 通过 |
| `go test -race`：treasury/httpapi/billing/payments/client | 通过；最终回单格式修订后再次执行 treasury/httpapi race 测试通过 |
| JavaScript 语法、脚本 `sh -n` | 通过 |
| OpenAPI YAML 解析 | 通过，118 paths |
| Helm lint + 本地受限部署 | 通过 |
| 本地 Kubernetes 全套 E2E（显式配置 URL、bootstrap、operator token） | 三个测试全部通过，非 skip |
| 中英文 UI、状态筛选、付款账单回跳、完整模拟转出 | 已实际点击验证 |
| 319px 内置浏览器 | 页面宽度与 scrollWidth 均 319，无整页横向溢出；表单及审计可操作 |
| 页面 error/warn 日志 | 最终 UI 验收未发现 |
| 模拟种子重复执行 | 五个固定申请均沿用原 ID，无重复提现 |

集群 E2E：`TestBillingPaymentJourney`、`TestFullContinuityJourney`、`TestTreasuryLocalCompleteJourney`。完整套件最终成功用时约 0.85 秒（不含编译）；在 revision 18 执行，revision 19 仅修订回单的中文状态和空值/时间展示并复测。

资金安全用例包括重复回执导入、未结算不可提现、未验证/币种不匹配拒绝、非运营方/只读账号拒绝、同键不同金额冲突、20 个并发申请不超额冻结、未批准禁止提交、关闭渠道后禁止提交、实款禁止同 subject 自批、实款未配置拒绝、失败/退回释放、回单保留、CSV 公式注入防护、Stripe 原幂等键与金额/账户/环境核验、银行失效后仍可查询原提现。

## 实际验收流水

- 集群完整链路付款：`pay_b83d924f1730339c85c2e7f7`
- 对应收款：`col_5a64e9b8a5d9e71e579a0e87`
- 对应模拟提现：`po_484de4386d583f3497d1f674`（CNY 0.01，到账后退回）；4 个并发相同申请只保存 1 笔。
- UI 点击验收：`po_0121fdaedd0d30227317d6db`（CNY 0.02），申请→批准→提交→处理中→到账→退回。三位小数 0.001 被前端拒绝，历史全过程审计保留。
- 初始测试重复创建计费账户得到 409，改为复用既有账户；随后发现宿主机与 Rancher VM 约一秒时差使毫秒级客户端账期未包含计量，测试已改用数据库保存的服务端计量时间。
- 因上述测试产生的零金额账单 `AC-20260903-A0DDA546F4814BCBF7F8F56D` 保留，无支付、无回执、未计入资金；没有删除财务历史或伪造金额。
- 原四张账单（CNY 0.01、2.72 已付，2.65、2.67 待付）及支付尝试/回执保留。

截至验收：7 张账单、4 笔收款、4 个虚拟目的账户、8 笔模拟提现、54 条资金审计。五个固定场景为已到账、处理中、失败、到账后退回、待审批；额外流水来自 API/UI 验收。

资金核对（CNY）：`2.75 − 0.01 = 0.00 待结算 + 0.99 可提现 + 0.75 冻结 + 1.00 已转出净额`。历史退回累计 0.14，不重复计入余额。这些都是模拟数据，后续用户操作会改变状态。

## 备份与导出

迁移前 PostgreSQL 自定义格式备份：`.data/treasury-backup-1ovtBY/postgres.dump`。

SHA-256：`03854faf61f8f83e99ea1304b7de4858036ac7fa7075dd00f56fffc90e78f436`。已验证非空且 `pg_restore --list` 可读取目录；本轮没有在当前数据库执行恢复或回滚。

通过已授权本地 API 导出：

- `.data/treasury-demo-FOXzXx/treasury-ledger.csv`
- `.data/treasury-demo-FOXzXx/bank-payout-receipt.html`
- `.data/treasury-demo-FOXzXx/returned-payout-receipt.html`

回单显式标注模拟、非银行凭证，退回后仍保留原回单并显示最新退回状态。配置与真实能力边界见 [资金模块说明](treasury.zh-CN.md)，UI 路线见 [完整演示指南](demo-ui-walkthrough.zh-CN.md)。
