# moox-host-agent

Linux 主机代理：每分钟采集 CPU、内存、文件系统、磁盘和网络，发布主机快照到 EventBus。

设计文档：[主机代理](../../docs/模块/主机代理.md)

## 构建与测试

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh hostagent
go test -count=1 ./modules/hostagent/...
```

## 配置

`config/app.yaml`（身份文件、EventBus 凭据、健康端口、`host_name`）、`config/trpc_go.yaml`。它是每台主机都有的主机级组件，随 `moox-cli setup deploy --host <主机>` 安装，EventBus 凭据在部署时从 Admin 导出，写到 `<部署根>/secrets/eventbus/`。

HostAgentMgr 管理接口使用 loopback 原生 tRPC 11426，经共享网关调用；健康检查继续使用原有独立 HTTP 端点。
