# moox-monitor

`moox-monitor` 是 MooX 独立服务可用性监控模块。V1 只覆盖 HTTP 和 TCP 探测，不迁移 Admin 内现有的主机资源监控。

## 端口

- `:11410`: tRPC/HTTP 管理 API `trpc.moox.monitor.MonitorMgr`
- `:11409`: 原生 HTTP `/healthz` 与 peer snapshot API

## 配置

核心配置在 `config/app.yaml`：

```yaml
database:
  path: ./data/monitor/monitor.db
health:
  addr: ":11409"
instance:
  instance_id: monitor-a
  base_url: http://127.0.0.1:11409
peer:
  enabled: true
  token: "change-me"
  peers:
    - instance_id: monitor-b
      base_url: http://10.0.0.2:11409
      token: "change-me"
```

## 运行

```bash
../../scripts/build.sh monitor
./bin/moox-monitor-cli init --db-path ./data/monitor/monitor.db
./bin/moox-monitor -conf=config/trpc_go.yaml
```

Admin 只作为网关和 SysDeploy 注册中心。Monitor 会周期性读取 SysDeploy active 部署，生成 `moox-system` 内置检查；探测时不依赖 Admin。

所有独立部署进程都提供 `/healthz`，monitor 自己也提供，可用于多实例互相监控。

## 管理接口

管理 API 通过 `trpc.moox.monitor.MonitorMgr` 暴露，可由 Admin 网关转发：

```text
/api/admin/moox_monitor/ListChecks
/api/admin/moox_monitor/GetOverview
/api/admin/moox_monitor/RunCheckOnce
```

SysDeploy 同步可手动触发 `SyncSystemChecks`；同步后的内置检查使用 `source=sysdeploy` 和 `group_name=moox-system`。

## 验证

本模块的实现验证记录见：

```text
docs/superpowers/verification/2026-07-09-monitor-module.md
```
