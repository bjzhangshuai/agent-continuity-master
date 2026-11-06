# 租户完整链路交付记录

范围：支付与原路退款、账单与非税务票据、跨 Agent 接续、跨平台接入安装、扩展协议和可重建部署包。

## 已完成的代码与验收

- [x] 退款持久化、申请/审批、部分/多次退款额度、幂等、未知结果恢复、贷项凭证。并发超额退款、重复成功、驳回释放、请求超时后原单恢复、迟到状态回退均有测试。
- [x] 微信/支付宝/PayPal/Stripe 的退款与查询协议，通过本地 HTTP 替身验证签名、身份、币种、金额和幂等键。
- [x] 租户申请、运营审批与提交、查询/模拟结果、票据下载页面；Go/Python/TypeScript/Swift/Kotlin/.NET SDK 和 OpenAPI 串联。
- [x] 跨设备 Python 发出、TypeScript 接收并继续任务，MCP stdio 可用；归档使用 MinIO，接续携带对话和附件。跨厂商采用 portable rehydrate；原生恢复需要兼容源会话。
- [x] 五类 Agent 工作区 MCP 配置检测、安装计划、原子合并、备份恢复、重复安装保护；Claude Code / CodeBuddy CLI 共享配置也有测试。
- [x] 应用、依赖 Chart、客户端、配置模板、校验和的打包脚本；六平台客户端已交叉构建。
- [x] Go 单测、关键包 race、五套语言 SDK 测试、Helm lint/渲染/依赖检查、本地集群 e2e 和页面交互验收。

约束：保护当前未提交工作及现有本地数据；限制测试与构建并发；不执行真实扣款或退款。正式税务开票必须接入合规开票服务，普通账单/收据明确标注非税票。封闭 App 无官方扩展入口时不声明可自动安装。

## 可复查结果

- Rancher Desktop：应用 Helm revision **29**，基础设施 revision **6**。API、归档 Worker、NATS、PostgreSQL、MinIO 均 Ready；节点 Memory/Disk/PID Pressure 均 False。升级过程中 Worker 等待重建后恢复，没有重启 Rancher 或集群，没有删 PVC。
- PostgreSQL 升级前已有本机备份，迁移 015 增加退款路由与唯一约束。原账单及回执保留。
- 五条实际服务 e2e 在最终部署第 29 版全通过（3.433 秒，无跳过）：PaymentJourney、FullContinuityJourney、PortalLocalRolesAndGroupPayment、RefundPaymentJourney、TreasuryLocalCompleteJourney。通用 `go test ./...` 的环境依赖测试会跳过；这里的结果来自显式配置本机集群的运行。完整结果保存在本机 `.data/closure-final-e2e.log`。
- 全额退款样例：账单 `inv_78ef3a68f50189ca8c0976d8`，支付 `pay_ed3ca3671c8af0dbe7a6dac9`，两次部分退款后全额退完，2 张贷项凭证、1 份原回执。
- 页面实际交互：集团财务申请 1 分 → 平台运营审批 → 提交 → 模拟成功。账单 `inv_e378dc30044e494eb18bbe76`，退款 `rf_b1262b2c7f04c32a24705b32`，贷项 `cn_rf_b1262b2c7f04c32a24705b32`；原 4 分回执保留，可退余额为 3 分。HTML 下载同时含原回执和退款凭证。
- 模拟收款/结算/提现/退回：支付 `pay_12c33c7b969c7418c9929373`、收款 `col_e7358f11eb1bbb9a3622e643`、提现 `po_3162afd591c671236dcba285`，旧 8 张账单未修改。
- 跨设备 SDK：任务 `task_e0320ca00ec28c4317617100c67ef59b`，独立 Python 与 TypeScript 设备各贡献 1 条事件，后者读取前者上下文并绑定 CodeBuddy Agent；MCP tools/list 通过。这是 SDK 设备仿真，真实 CodeBuddy Hook 的历史验收见 `codebuddy-local-e2e.zh-CN.md`。
- 自动检测区分 CLI 路径与桌面 App，不将本机 Claude Desktop 当作 Claude Code。工作区安装、重复安装和恢复在独立临时目录验证。
- 额外修复：退出登录独立限流，登录限流耗尽不再阻止退出；正常退出仍受 CSRF 和自身请求上限保护。
- 页面成功退款后再次验收，旧资金守恒断言暴露出漏算退款的问题；测试已按「原收款 − 手续费 − 已退款 − 退款占用 = 待结算 + 可用 + 提现冻结 + 已转出」更新，实际服务复测通过。

## 如何查看

打开 http://127.0.0.1:18080/ 。现有本地演示账号资料仍在 `.data/tenant-demo.json`（不进入部署包）。财务账号选择“星海集团”查看账单尾号 `E378DC30044E494EB18BBE76`；平台运营账号在“收款与提现 → 客户退款处理”查看同一笔退款与凭证。所有金额均属模拟。

操作步骤、SDK 调用、安装范围和发布包构成见 [退款与交付说明](refunds-and-delivery.zh-CN.md)。

## 对外运营仍需外部条件

1. 真实微信/支付宝/PayPal/Stripe 商户产品、凭据、回调及真实渠道验收。本轮仅协议模拟，不证明实款到账；微信/支付宝/PayPal 提现由渠道商户后台完成。
2. 当前发行非税务账单、付款收据和退款贷项。合法税票需要开票服务商及主体资质，当前没有税务开票服务集成。
3. 自动安装仅覆盖目录声明的项目配置。CodeBuddy IDE 有 VSIX + Hook；其他产品不会因为安装 MCP 就自动开放全部历史对话。移动第三方封闭 App 不支持任意注入；自研 App 可用 Swift/Kotlin SDK。
4. macOS/Linux/Windows × amd64/arm64 是六种客户端构建产物，不代表六种真实设备上的厂商 App 均已验收。
5. 发布物为联网部署包，依赖 Chart 已锁定并随包携带，容器镜像需要构建/拉取。生产多节点、共享存储、备份恢复、正式密钥和对外域名需部署方准备；本机测试不替代生产容量验证。
