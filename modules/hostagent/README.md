# moox-host-agent

Linux 主机代理：每分钟采集 CPU、内存、文件系统、磁盘、网络和进程，经主机网关随 `MonitorMgr.SyncHostAgent` 上报主机快照给 Monitor 并拉取告警渠道存到本机（不连接 EventBus）；非控制机发现控制机或 Monitor 连不上时由本机直接发通知。

设计文档：[主机代理](../../docs/模块/主机代理.md)

## 构建与测试

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh hostagent
go test -count=1 ./modules/hostagent/...
```

## 配置

`config/app.yaml`（`host_id`（必填，Admin 登记的主机 ID）、`host_name`、`gateway_client`、`host_policy_path`，均由部署渲染）、`config/trpc_go.yaml`（服务端口、健康端口与采样定时器）。它是每台主机都有的主机级组件，随 `moox-cli setup deploy --host <主机>` 安装。

HostAgentMgr 管理接口使用 loopback 原生 tRPC 11426，经主机网关调用；健康端口 11425 只听 127.0.0.1。
