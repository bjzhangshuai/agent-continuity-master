# 多租户账号与付款改造：本地验收记录

验收日期：2026-09-03（Asia/Shanghai）。代码目录：`/Users/jansurezhang/go/src/agent-continuity-master`。

## 部署结果

- 地址：http://127.0.0.1:18080/。仅本机回环端口转发，不对公网开放。
- Kubernetes context：`rancher-desktop`；namespace：`agent-continuity-dev`。
- Helm release：`agent-continuity`，revision **22**，状态 `deployed`。
- 部署镜像标识：`sha256:278b39f5bb6853afccc620f78483b2ba8e1d50fdae0fa52a86fda95630e6f603`。
- 应用 Pod：`agent-continuity-d5795654f-vpbts`，1/1 Ready，0 次重启。
- PostgreSQL：`agent-continuity-postgres-0`，1/1 Ready；原有一次重启计数未增加。
- 两个原有 1Gi PVC 均 Bound，未替换 Secret、PVC 或数据库，未修改 Rancher 全局配置及当前 kubectl context。
- 节点 Ready；MemoryPressure、DiskPressure、PIDPressure 均 False。
- 一次采样：应用 13m CPU / 7Mi 内存，PostgreSQL 16m / 50Mi。采样不是长期负载承诺；保留应用 500m/256Mi、数据库 500m/512Mi 限制，单副本。
- `/v1/health/ready` 返回 `{"status":"ready"}`。

迁移 012 前已备份 PostgreSQL：`.data/tenant-backup-CABF0J/postgres.dump`，文件权限 0600。
SHA256：`4cf0246d6574b0f40ebdc012be67717acf2fd251ea28e81d37d9ef074cb1f454`。
此次没有为了验收而覆盖恢复数据库；回滚应用时也不应删除新增登录/授权表。

## 自动测试

以下检查通过，限制 Go 并行度为 2：

```sh
GOMAXPROCS=2 go test -p 2 ./...
GOMAXPROCS=2 go vet -p 2 ./...
GOMAXPROCS=2 go test -race -p 2 \
  ./internal/httpapi ./internal/billing ./internal/tenancy \
  ./internal/storage/memory ./internal/treasury
node --check internal/httpapi/static/portal.js
node --check internal/httpapi/static/treasury.js
node --check internal/httpapi/static/app.js
```

新增 `api/openapi.yaml` 登录、租户、委派、代付、商户只读凭证接口与字段；YAML 可解析，内部 `$ref` 均可解析。这不是所有外部支付商生产环境的认证测试。

真实本地 PostgreSQL / K8s 服务上执行：

```sh
CONTINUITY_E2E_URL=http://127.0.0.1:18080 \
CONTINUITY_E2E_PORTAL_STATE=/Users/jansurezhang/go/src/agent-continuity-master/.data/tenant-demo.json \
GOMAXPROCS=2 go test -p 2 ./tests/e2e \
  -run TestPortalLocalRolesAndGroupPayment -count=1 -v
```

revision 22 通过（0.843s）。覆盖匿名拒绝、Cookie 登录、四类账号、区域管理范围、viewer 财务隔离、集团冻结付款责任、商户只读原始凭证、CSV/HTML 归属、重复确认幂等、CSRF 和退出失效。E2E 只重放已成功的模拟确认，不创建新订单，也不操作真实支付。

单元/接口测试额外覆盖：委派撤销链、父租户无隐式内容权限、已移除成员无法沿旧任务 ACL 访问、实时连接重校验、租户全员用量汇总、重叠账期拒绝、出账提交时重验代付授权、只读角色无付款链接、密码哈希和会话空闲失效。

## 浏览器验收与保留样例

11 个账号、5 个新增租户、6 张新增租户账单已持久化。旧任务、设备、个人账单及收款/提现记录保留。

已通过真实浏览器 UI 验证：

1. `tenant-demo.platform` 显示独立客户、集团、华东、三级研发中心、华南及旧演示组织；总部 → 华东区域 → 客服三层后台授权可见。
2. `tenant-demo.finance` 默认选择有付款权限的集团；切换其仅为 viewer 的华南租户后不显示费用。平台分级管理、商户收款栏目对该账号隐藏。
3. 账单列表显示服务租户名称与付款责任租户，而不是只显示裸 ID；代付授权显示双方名称。
4. 打开集团 CNY 0.04 账单、生成收银台、在独立收银台点击“模拟支付成功”、返回“主动查单”，账单变为已付并出现回执。
5. 平台运营账号导入该笔已确认模拟回执，出现“待渠道结算”，并可只读打开原账单。没有把收款成功伪装成银行到账。
6. `tenant-demo.viewer` 仅看到华东直接成员身份，明确提示“费用数据不可见”，无平台管理或商户收款入口。
7. 部署重启后浏览器会话自动恢复；窄屏长 ID 自动折行，选择框不越界，账单表在自己的容器内横向滚动。

| 场景 | 账单 | 当前状态 |
| --- | --- | --- |
| 华东用量、集团财务代付 | `AC-20260903-AFE6E3A0158A3827119A9837` | CNY 0.06 已付，有回执，模拟收款待结算 |
| 集团自身用量、集团财务付款 | `AC-20260903-E378DC30044E494EB18BBE76` | CNY 0.04 已付，有回执，模拟收款待结算 |
| 华东另一账期、等待集团代付 | `AC-20260903-B2A3A4C97CA9D6A57566A6AF` | CNY 0.04 待付，留给用户交互 |
| 三级研发中心独立结算 | `AC-20260903-89CF082E927E1BF2658D95FD` | CNY 0.03 待付 |
| 华南独立结算 | `AC-20260903-ABF34A4C93257385082A4195` | CNY 0.05 待付 |
| 独立客户 | `AC-20260903-42287E93975F5849D56E8EB8` | CNY 0.04 待付 |

集团实操付款 ID：`pay_aaff5365b0d942886be45e5e`；回执：`rcpt_76b75fc5fe66f816e33b2103`。回执记录实际发起财务账号，不冒充租户 owner。

导入后模拟资金一次快照：实付 CNY 2.85 − 手续费 0.01 = 待结算 0.10 + 可提现 0.99 + 冻结 0.75 + 已转出净额 1.00。此等式使用既有演示资金，**全部为模拟，不是真实到账**。

## 用户查看过程

1. 打开 http://127.0.0.1:18080/，当前保留集团财务演示会话。
2. 点“租户费用中心”，查看华东服务租户与集团付款责任；点待付账单的“账单 / 付款 / 回执”继续模拟付款，也可打开已付账单下载回执。
3. 点“退出 / 切换账号”，登录 `tenant-demo.platform`，看“平台分级管理”与“收款与提现”。其他演示身份和初始随机密码在 `.data/tenant-demo.json` 的 `users` 中，文件权限 0600；不需要复制过期 Bearer Token。
4. 查看受限后台可登录 `tenant-demo.region`；查看无财务权限可登录 `tenant-demo.viewer`。全部账号清单见 `docs/multitenancy.zh-CN.md`。
5. 新建实际租户的操作顺序：超管创建独立登录账号 → 创建租户并指定该账号为 owner → owner 创建/邀请财务成员 → 开通租户计费账户 → 产生用量、预览出账 → owner/billing 发起付款。集团代付需子租户 owner 申请、集团 owner 接受；租户层级本身不授权代付。

收银台按钮与 API 的模拟结果可重复演示，但已出账账期不会再次出账。若需要新的待付样例，应产生新的用量与不重叠账期，不改写历史付款记录。

## 范围与未完成的商业能力

本轮完成的是账号、多租户分级、独立后台授权与账单付款闭环，不是全部商业 SaaS 能力。未新增余额充值钱包、父子租户自动分账、税务发票、真实商户开户、银行卡验证、退款/贷项审批、套餐自动续费、密码找回、MFA 或完整浏览器 SSO 跳转页。详细权限、容量上限和上线事项见 `docs/multitenancy.zh-CN.md`。

没有启用真实支付密钥、真实转账或对外发布。真实收款需要用户自己的商户资质、配置、HTTPS/回调域名和单独验收；微信、支付宝、PayPal 的银行提现仍按既有渠道能力边界处理，不能用本次模拟结果宣称生产收款完成。
