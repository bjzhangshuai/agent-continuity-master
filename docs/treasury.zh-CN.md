# 收款、结算与转入自己的账户

现有「用量与计费」及四种收款适配保留。新增独立的「收款与提现」页面，区分付款人账单和运营方资金，不能将付费用户自动当成有提现权限的商户。

## 交付与边界

本地可完整操作：发起账单支付 → 模拟付款 → 导入对应付款回执 → 待渠道结算 → 结算净额可用 → 登记自己的虚拟银行/支付宝/PayPal 账户 → 模拟归属验证 → 申请提现冻结余额 → 审批 → 提交 → 处理中 → 模拟到账/失败 → 回单、CSV 与审计。到账后退回会恢复可用余额，并保留历史回单及最新退回状态。

这是一套可持久化的资金工作流，不是持牌资金托管钱包。Demo 不转账、不收集真实卡号，不等于银行开户、KYC 或商户验收。币种只支持 CNY/USD/EUR，不换汇。

真实渠道支持分层：

| 渠道 | 当前转出方式 | 未完成的外部门槛 |
|---|---|---|
| Stripe | 可选银行 Payout API：查询自己商户的余额与默认银行卡、审批、提交、原单重试、查单、到账后退回 | 商户与地区准入、自己绑定的银行卡、manual payout schedule、权限与正式密钥、商户沙箱/实款验收 |
| 微信支付 | 官方商户后台的结算/提现入口 | 普通商户与服务商能力不同；没有冒用特约商户专用提现 API |
| 支付宝 | 官方商家后台，商家账户余额提现至绑定银行卡 | 未实现自动转账/银行提现 API；需按自己的签约产品申请能力 |
| PayPal | 官方余额/提现入口，按账户地区规则提现到银行 | Payouts 向另一钱包付款不等于本人的银行提现，当前不自动调用 |

因此，完整模拟已实现，并有 Stripe 银行转出的协议适配；不是已经替你开通四家真实自动提现。真实网络适配只以本地协议测试验证，未发生任何实款交易。不支持任意收款人、跨钱包互转、多商户代收分账或平台代管用户资金。

## 看 UI 的顺序

1. 在「用量与计费」完成一张模拟账单，保留原有收银台和付款回执。
2. 切换「收款与提现」，选择该已付款账单，点击「导入已验签模拟回执」。金额取自服务端回执，不能输入一个余额直接充值。
3. 在收款记录点「模拟渠道结算」。演示手续费为 0.6%，按分向下取整，不代表任何真实渠道费率。
4. 在「自己的收款账户」登记展示名称、类型、币种，再点「模拟本人账户验证」。不要填写真实账号；此步骤只演示验证状态。
5. 选择已验证的同币种账户，输入金额，勾选本人账户确认，点击「提交审批」。可用余额相应冻结。
6. 在提现记录中点「批准提现」再「提交转出」，状态变为「渠道处理中」。申请可驳回或在提交前取消。
7. 点「模拟到账」查看回单；也可演示「失败退回」。已到账记录可演示「到账后退回」。不要把提交受理当成到账。
8. 查看每笔「全过程审计」，下载转出回单及资金流水 CSV。回单不是税票，也不是独立银行到账证明。

`sh scripts/seed-treasury-demo.sh` 使用已有已付模拟回执生成五种场景：银行到账、支付宝账户处理中、银行失败、到账后退回、待审批。不会清空既有账单或支付记录；重复执行保留已有场景。

## 授权与安全

- 明确设置 `owner_account_id`，其他付款账户即使持有自己的通配凭据也得到 403。
- 独立权限：`treasury:read`、`treasury:write`、`treasury:approve`；只有普通计费权限不能操作资金中心。
- 正式提现的申请 subject 和审批 subject 必须不同。由运营方正确分发身份，管理员不能把同一主体伪装成两个人来替代真实双人审批。
- 金额以整数最小币种单位存储；目的账户、金额、币种在申请后冻结；币种不匹配和未验证账户拒绝。
- 申请时冻结余额；账户级 PostgreSQL CAS 防止并发超额预约。实款使用 Stripe 官方余额，模拟账本不能充值到实款余额。
- Stripe 的可申请额度采用保守策略：官方余额减本地未终结申请；处理中可能重复预留，宁可暂缓新申请，也不冒险透支。渠道可能另收费用，最终可转出金额以渠道实际受理为准。
- 提交前先持久化提交状态，使用原 payout_id 作为渠道幂等键。超时状态不释放资金、不换单；缺少渠道单号时只允许 23 小时内原键重试，之后需到官方后台核查。
- 只允许服务端配置商户对应的默认银行账户。更换默认卡不能将已审批转账重定向到新卡；接口不接受银行卡明文。
- 银行回单以渠道确认时间记录，不声称独立核验银行入账。后续退回更新当前状态，历史回单不删除。
- 演示账本总额核对：实付−手续费＝待结算＋可提现＋冻结＋已转出净额；历史退回仅作累计展示，不重复计入余额。
- 当前为有界运营账本：每运营方最多 5,000 笔收款、5,000 笔转出、50,000 条审计，避免本地无界内存/JSON 放大。长期大规模资金账务需要归档、分区明细表及独立结算对账系统；本版本不自动删历史。

## 配置（默认关闭实款）

`CONTINUITY_TREASURY_CONFIG` 与 `CONTINUITY_TREASURY_CONFIG_FILE` 二选一。未配置运营方时所有资金 API 拒绝操作，不影响原支付功能。

```json
{"owner_account_id":"YOUR_OPERATOR_ACCOUNT_ID","demo_enabled":true,"allow_live":false}
```

Stripe 沙箱配置示意：

```json
{
  "owner_account_id":"YOUR_OPERATOR_ACCOUNT_ID",
  "demo_enabled":false,
  "allow_live":false,
  "stripe":{
    "account_id":"acct_YOUR_OWN_MERCHANT",
    "sandbox":true,
    "secret_key_file":"/run/treasury/stripe-key"
  }
}
```

在 Stripe 官方后台绑定本人/商户银行并设置手动打款。API 每次按密钥查 `/v1/account`，验证 merchant ID；只从渠道返回的默认银行选择收款方，读取 `/v1/balance`。真实启用必须另行授权，将 `sandbox:false`、`allow_live:true` 与正式密钥配套配置；不能将本地演示账本当作正式余额。

Helm：`treasury.ownerAccountID` 与 `treasury.demoEnabled` 控制本地模拟；正式/沙箱密钥使用 `treasury.existingSecret`，内含 `config.json` 和 `stripe-key`，挂载 `/run/treasury`，权限 0440。不要提交真实密钥到 Git 或聊天中。部署脚本沿用当前 release 已配置的 operator，也可显式传 `CONTINUITY_TREASURY_OWNER_ACCOUNT_ID`。

新增迁移 `011_treasury` 仅添加独立表，不删除、不回写原账单。该迁移无自动 down，以免删除财务审计历史。

## SDK 与接口

Go SDK：`pkg/client/treasury.go`，包含查询资金中心、导入演示收款、申请/审批/提交/查询转出。其他设备可通过同一 REST API 接入，银行明文及服务端密钥不能下发客户端。所有路径见 `api/openapi.yaml` 的 `/v1/treasury`。

验证：`GOMAXPROCS=2 go test -race -p 2 ./internal/treasury ./internal/httpapi ./pkg/client`。生产商户验收和真实到账验证必须单独进行，不能用 Demo 通过代替。

## 官方能力依据（2026-09-03）

- [Stripe 自己银行账户 Payout](https://docs.stripe.com/api/payouts/create)
- [Stripe 余额查询](https://docs.stripe.com/api/balance/balance_retrieve)
- [Stripe Payout 查询](https://docs.stripe.com/api/payouts/retrieve)
- [微信普通商户/服务商资金路径](https://pay.wechatpay.cn/doc/v3/partner/4012081931)
- [支付宝提现](https://help.alipay.com/support/help_detail.htm?help_id=397752)
- [PayPal 余额提现说明](https://www.paypal.com/c2/cshelp/article/how-do-i-withdraw-money-from-my-paypal-account-help394)
