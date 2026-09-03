# 多租户、分级后台与账号付款

代码根目录：`/Users/jansurezhang/go/src/agent-continuity-master`。本地控制台：http://127.0.0.1:18080/。

## 已实现的边界

- `Organization` 作为唯一租户标识，新增只在创建时确定的 `parent_id`，最多 8 层。现有组织仍是根租户；不改写历史数据，不提供任意移动子树的接口。
- 租户成员直接绑定账号：owner、admin、billing、member、viewer。账号可以加入多个租户；父子关系本身不授予内容访问或付款权。owner 才能授予、调整、撤销 billing 付款权限。
- 平台超级管理员由部署配置 `CONTINUITY_PLATFORM_SUPER_ADMINS` 显式指定。普通 JWT 的 `*`、租户 owner、API Key 都不因此成为超级管理员。
- 后台 grant 独立于租户成员：可以按“全平台 → 集团子树 → 区域子树”分级委派。下级不得扩大范围；撤销上级授权会使所有下级授权失效。撤权后新请求按当前成员/授权判断，实时连接发送任务消息前也重新校验。
- 后台 admin：范围内元数据、委派、创建子租户；operations：范围内元数据和名称维护；finance：范围内账单金额汇总；support / auditor：范围内元数据和控制授权审计。以上权限均不等于租户任务正文权限、付款权或商户提现权。
- 浏览器账号登录使用 bcrypt 密码哈希、随机 HttpOnly / SameSite=Strict 会话 Cookie、同源校验及 CSRF 请求头。会话空闲 30 分钟失效，最长 8 小时，刷新页面可以恢复；退出会注销服务器会话。原来的 JWT / SDK 刷新机制保留。

## 付款责任模型

1. 用量记录同时标识产生用量的账号与服务租户。租户新账单汇总该租户所有成员的用量，而不是仅统计账单创建者。
2. 默认独立结算。子租户 owner 申请上级租户代付，付款集团 owner 接受后才生效；每个服务租户最多一个 pending / active 授权。
3. 出账冻结服务租户、付款责任租户、代付授权、账期、定价及创建者。数据库提交时再次验证授权，避免用已撤销授权出账。
4. 集团费用中心聚合自己和明确承接的子租户账单，不再给同一用量出第二份“集团账单”。重复/重叠出账按租户而非个人隔离；不同币种、测试/正式环境分别汇总。
5. 服务租户或冻结责任租户的 owner / billing 可以付款；admin 仅财务只读，不获得付款链接。成员被移除后，即使是旧账单或任务创建者，也不能凭历史个人键绕过权限。
6. 创建支付单 → 打开渠道收银台 → 渠道可信确认 / 服务端查单 → 幂等更新 paid → 生成支付回执。支付单记录真实发起账号 `initiated_by`，不冒充原始账单创建者。
7. 平台运营方在独立“收款与提现”页导入已确认的模拟回执，查看结算、收款账户和提现状态。专用商户只读入口可以查看租户原付款账单、下载 CSV 和回执，但不会赋予运营方租户付款权限，也不会返回收银台链接。租户登录账号不是资金钱包，也不是商户银行账号。

撤销代付授权只影响新账单；已出账责任和历史回执不变。需要纠正历史责任的商业流程，应另行设计有审计的作废、退款或贷项，而非直接修改归属。

## 主要接口

| 接口 | 用途 / 权限 |
| --- | --- |
| POST `/v1/auth/login` | username/password 登录；同源 `X-Portal-Request: 1` |
| GET `/v1/portal/session` | 当前账号、直接租户成员、可管理租户范围、授权、代付申请 |
| POST `/v1/auth/session/logout` | 撤销当前 Cookie 会话 |
| POST `/v1/portal/users` | 超管创建独立账号，或 owner 创建并加入本租户；self 为已有全权限交互账号开通首次密码 |
| POST `/v1/portal/tenants` | 创建根租户或子租户；parent_id 不可后改 |
| POST `/v1/portal/grants` | 授予后台角色、tenant_id 子树范围及 parent_id 上级授权 |
| POST `/v1/portal/grants/{id}/revoke` | 撤销后台授权链 |
| PATCH `/v1/portal/tenants/{id}/metadata` | 在后台管理范围内维护名称 |
| GET `/v1/portal/tenants/{id}/finance-summary` | 平台财务只读汇总，不含支付链接 |
| GET `/v1/portal/tenants/{id}/billing` | 租户财务账单、集团代付账单及分币种欠款汇总，最近 200 条 |
| POST `/v1/portal/tenants/{id}/billing-account` | owner 开通独立计费账户 |
| POST `/v1/portal/tenants/{id}/quote`、`/invoices` | 预览 / 正式出账；出账须 Idempotency-Key |
| POST `/v1/portal/mandates` | 子租户 owner 申请祖先租户代付 |
| POST `/v1/portal/mandates/{id}/accept`、`/revoke` | 双边确认或撤销未来代付 |
| 原 `/v1/billing/invoices/{id}/...` | 继续用于账单、付款、查单、CSV 和回执，新增租户角色校验 |
| GET `/v1/treasury/receivables`、`/{invoiceID}`、`/{invoiceID}/export`、`/{invoiceID}/receipts/{receiptID}` | 指定商户运营账号只读查询已付账单候选、原账单及财务凭证；无租户付款权限 |

前端的“租户费用中心”选择器只限定该页的费用和成员管理。原任务、Agent、工作区等栏目仍展示当前账号获准访问的资源合集，不伪装成全站租户筛选。

## 演示数据与操作

`cmd/tenant-demo` 只允许 localhost / 127.0.0.1，且必须启用测试价格表和独立 demo 支付渠道。它不删除旧记录，不启用真实支付。演示状态和随机密码只写入权限 0600 的 `.data/tenant-demo.json`，不进入文档或 Docker 镜像。

```sh
AGENTCLOUD_TOKEN='' AGENTCLOUD_API_URL=http://127.0.0.1:18080 \
  AGENTCLOUD_STATE_DIR='/Users/jansurezhang/Library/Application Support/agent-continuity' \
  ./bin/agentcloud status
GOMAXPROCS=2 go run ./cmd/tenant-demo \
  -credentials '/Users/jansurezhang/Library/Application Support/agent-continuity/credentials.json'
```

- `tenant-demo.platform`：平台超级管理员。看 5 个新增租户、3 层后台委派与原有商户收款后台。
- `tenant-demo.hq` / `tenant-demo.region` / `tenant-demo.support`：总部、华东区域、杭州客服。逐次登录可观察可管理范围缩小。
- `tenant-demo.group`：集团 owner，查看并确认代付责任。
- `tenant-demo.finance`：集团 billing，可支付集团及授权承接的华东账单；同时是华南 viewer，切换后不能查看费用。
- `tenant-demo.east` / `tenant-demo.lab` / `tenant-demo.west` / `tenant-demo.independent`：各自独立 owner。
- `tenant-demo.viewer`：华东 viewer，不显示费用明细，后端直接访问也拒绝。

付款演示：登录 finance → 租户费用中心选“星海集团” → 找华东待付账单 → 账单 / 付款 / 回执 → 发起支付 → 打开安全收银台 → 点击“模拟支付成功” → 返回控制台点击“主动查单” → 账单 paid → 下载回执。也可以使用控制台的“模拟完成付款（不扣款）”。已有华东代付及集团自付两笔 paid 样例，另保留华东待付账单供交互。

平台收款演示：切换 platform → 收款与提现 → 导入指定租户的已付账单号 → 模拟结算 → 使用原有虚拟本人收款账户提现流程。所有金额都是模拟资金，不是银行实收。

## 尚不包含 / 上线前事项

这次完成的是多租户账号、授权和账单付款改造，不等同于所有商业 SaaS 功能都已完成。尚无客户余额充值钱包、父子租户自动分账、合并税务发票、真实商户开户、银行绑卡验证、退款/贷项审批、支付后套餐自动续订、密码找回与 MFA。已有 OIDC/SCIM 后端未新增完整浏览器 SSO 跳转页。

真实收款仍需要商户资质和密钥、HTTPS、可信回调域名、正式价格表及显式 live 开关；此次不进行真实扣款或转账。多副本部署还需共享登录限流、完善身份恢复与运维审计。后台授权使用 PostgreSQL CAS 控制文档（最多 5000 grants、5000 mandates，50000 事件后停止新增但仍允许撤权）；超大规模需迁移分页关系模型。租户列表和财务汇总当前是有界管理界面，不是海量分析仓库。

新增迁移 012 为增量迁移。上线回滚应用可保留新增表和列；不要为回滚而删除真实登录/授权数据。部署脚本维持原 namespace、PVC、Secret 和资源限制，不修改 Rancher 全局配置。
