# 外部接入

`moox-access` 是独立的外部签名 tRPC 入口，由 Storage 的外部代理迁出。它校验调用方签名、持久化 nonce 防重放、限制可调用方法，再重新签名转发。进程不依赖 Storage 的内部实现，使用共享健康检查包和纯 Go SQLite，可在本机关闭 CGO 构建 Linux 制品。

当前 E1 只完成模块拆分与命名调整，原有 `collector`、`factor-engine` 主体、方法白名单及转发行为保留。统一的 `access@<主机>` 身份、组件目录 ACL、共享上游客户端和全部外部调用方的同步切换在 E2 完成。

```bash
CGO_ENABLED=0 go test ./modules/access/...
TARGET_GOOS=linux TARGET_GOARCH=amd64 bash scripts/build/build.sh access
```

配置为 `config/trpc_go.yaml`，入站原生 tRPC 监听 `0.0.0.0:11004`，健康检查监听 loopback `11014`。运行目录包含 `access/config`、`data/access` 与私密 `secrets` 文件。`config/principals.example.yaml` 说明主体与密钥文件格式；实际密钥由部署流程提供，不放入发布包或输出。

运行环境统一使用 `MOOX_ACCESS_` 前缀：`PRINCIPALS_FILE`、`TARGET_NODE`、`UPSTREAM_TARGET_NODE`、`UPSTREAM_TARGET`、`NONCE_PATH`、`NONCE_NAMESPACE`、`MAX_BODY_BYTES`、`TIMEOUT`、`READY_CHECK_INTERVAL`、`READY_CHECK_TIMEOUT`。独立部署 profile 为 `access`，启停和打包使用同一组件名。
