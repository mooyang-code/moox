# moox-host-agent

Linux 主机代理：每分钟采集 CPU、内存、文件系统、磁盘和网络，发布主机快照到 EventBus。

设计文档：[主机代理](../../docs/模块/主机代理.md)

## 构建与测试

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh host-agent
go test -count=1 ./modules/hostagent/...
```

## 配置

`config/app.yaml`（身份文件、EventBus 凭据、健康端口、`host_name`）、`config/trpc_go.yaml`。主机采集器是每台主机自动部署的
主机组件，随 `moox-cli setup deploy-host` 一起部署。
