# moox-host-agent

Linux 主机代理：每分钟采集 CPU、内存、文件系统、磁盘、网络和进程，发布主机快照到 EventBus；每分钟从 Monitor 拉取告警渠道存到本机，非控制机发现控制机或 Monitor 连不上时由本机直接发通知。

设计文档：[主机代理](../../docs/模块/主机代理.md)

## 构建与测试

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh hostagent
go test -count=1 ./modules/hostagent/...
```

## 配置

`config/app.yaml`（身份文件、EventBus 凭据文件、`host_id`、`host_name`、`gateway_client`、`host_policy_path`）、`config/trpc_go.yaml`（服务端口、健康端口与采样定时器）。它是每台主机都有的主机级组件，随 `moox-cli setup deploy --host <主机>` 安装，EventBus 凭据在部署时从 Admin 导出，写到 `<部署根>/secrets/eventbus/`。

HostAgentMgr 管理接口使用 loopback 原生 tRPC 11426，经主机网关调用；健康端口 11425 只听 127.0.0.1。
