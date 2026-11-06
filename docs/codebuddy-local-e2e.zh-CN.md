# CodeBuddy 本地闭环实验与页面查看

## 本次验收对象

- CodeBuddy CN 4.10.3，扩展 `agent-continuity.agent-continuity-codebuddy@0.1.0`
- Device A：`MacBook · CodeBuddy 本机`
- Device B：`模拟第二台设备 · CodeBuddy`
- 连续性任务：`task_8065b3dbc838c2fd1f9ff43294d8f750`
- 模拟账单：`inv_fe90cc5d4e3255474d117520`
- 本地服务：<http://127.0.0.1:18080/>

标识用于定位本机演示记录，不是访问凭据。插件 API Key 和设备私钥位于
`.data/codebuddy-local/`，权限为 `0600`，不会出现在项目配置或页面 URL 中，
并在 24 小时后到期。

## 页面查看顺序

1. 打开管理页并使用本地演示账号登录。
2. 点击“任务”，选择 `CodeBuddy · codebuddy-device-a · 2e03244c`。详情应显示
   设备名、4 个事件、1 个产物、完整的两轮用户/助手公开对话、Agent 绑定、
   DAG、参与者和审计轨迹。
3. 点击“对象存储与队列”，在任务下拉框选择相同 CodeBuddy 任务。应显示
   4 个对话事件和 1 个 Markdown 附件，全部为“归档已持久化”，目标是
   `minio-dev-v1`。点击“从对象存储下载并校验”可做单项回读。
4. 点击“Agent 与工作区”，查看 CodeBuddy Agent 以及两台独立设备。
5. 点击“租户费用中心”，找到金额 `CNY 0.06` 的已支付模拟账单；点击
   “账单 / 付款 / 回执”查看五类计费明细、支付审计和唯一回执。
6. “个人用量与原账单”可看原始用量；平台管理员的“收款与提现”页面可看
   已收款、结算、提现目的地、模拟提现和退款审计。

## CodeBuddy 内查看

CodeBuddy 命令面板中搜索 `Agent Continuity`，可使用：查看同步状态、从其他
设备接续、上传明确选择的工作区文件、查看账单与模拟支付、打开本地管理页。

项目 Hook 与 MCP 配置位于实验工作区的 `.codebuddy/`。跨设备恢复会创建一份
可审阅 Markdown，并在新会话启动时注入历史上下文；不会覆盖 CodeBuddy 私有
会话数据库，也不声称等同于原生会话克隆。

## 已验证的链路

真实 CodeBuddy 新会话触发了 SessionStart/Stop Hook；第二轮消息验证了 Stop
返回后再读取最终回复的时序处理。4 条可见消息均已设备签名、服务端接收，
经 PostgreSQL Outbox、NATS JetStream 和归档 Worker 写入本地 MinIO。5 个对象
均以内容哈希回读校验通过，测试字段中的 `never-upload-this` 未进入事件。

第二设备使用独立身份和本地状态恢复同一任务，生成并注入了 portable
rehydrate 上下文。支付采用 `demo` provider 模拟微信交互：页面明确标注不扣款，
成功回调后账单变为 paid 并生成一张回执，重复回调未重复入账。真实微信商户、
银行结算和真实提现没有在本地伪造为已接入。
