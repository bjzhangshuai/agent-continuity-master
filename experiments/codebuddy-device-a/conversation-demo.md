# CodeBuddy 本地闭环实验

1. 新会话的可见对话由项目 Hook 采集并签名上传。
2. 服务端通过 PostgreSQL Outbox 和 NATS JetStream 交给归档 Worker。
3. 对话事件与显式选择的文件落入本地 MinIO，并可由第二设备恢复。
