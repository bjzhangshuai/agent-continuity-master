# 可复现的本地 S3 测试镜像

2026-11-07 的 CI 已验证旧 `minio/minio` Docker Hub 地址无法拉取，Quay 同标签的 manifest 也不存在。本项目不依赖第三方重打包镜像，改从两个上游仓库的完整提交构建：

| 镜像 | 上游源码 |
|---|---|
| `agent-continuity-minio:source-7aac2a2c5b7c` | [minio/minio 7aac2a2c5b7c882e68c1ce017d8256be2feea27f](https://github.com/minio/minio/tree/7aac2a2c5b7c882e68c1ce017d8256be2feea27f) |
| `agent-continuity-mc:source-77f82e18b540` | [minio/mc 77f82e18b5401a65958f1619df6ebb994634bd88](https://github.com/minio/mc/tree/77f82e18b5401a65958f1619df6ebb994634bd88) |

源码获取使用完整提交并校验 checkout SHA。构建限制 `GOMAXPROCS=2`、`go build -p 2`，使用 Go 1.27.3，运行用户为 1000:1000。镜像内 `/usr/share/minio` 或 `/usr/share/mc` 保存上游源码快照与 AGPL 许可证；OCI labels 标注来源和完整修订。MinIO 入口兼容 vendored Helm chart 的 `/usr/bin/docker-entrypoint.sh minio server ...`。

这两个上游项目已归档，只用于隔离的 S3 兼容测试，不作为受维护的生产对象存储承诺。四云生产 profile 默认不安装 MinIO，继续连接外部对象存储。

在目标机器选择 Docker context 后显式构建：

```sh
CONTINUITY_DOCKER_CONTEXT=rancher-desktop ./scripts/build-objectstore-images.sh
```

脚本只构建两个指定镜像，不启动容器、不操作集群、不清理其他镜像。`scripts/deploy-rancher-desktop.sh` 的容量检查通过后会调用它；本次交付没有执行该安装脚本。Compose 的 MinIO service 带 `build.target=minio`，`up --build` 自动构建。Compose E2E 额外构建 mc 并实际进行建桶、上传、下载比对。

多节点 Kubernetes 需要把这些镜像推送到自己的 registry，再覆盖 infra values 的 `minio.image`、`minio.mcImage` 和 `minioBootstrap.image`，或者将镜像导入每个测试节点。构建需要访问 Go 镜像、Alpine 包、GitHub 上游源码和 Go 模块代理；发布压缩包不是离线镜像包。已有本地 PVC/服务在本轮保持原样。
