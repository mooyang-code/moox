# moox-monitor

`moox-monitor` 是 MooX 独立服务可用性监控模块。V1 只覆盖 HTTP 和 TCP 探测，不迁移 Admin 内现有的主机资源监控。

Monitor 还消费 `moox.metrics.snapshot.reported.v1`，将每个已注册 tRPC
实例的有界 Prometheus snapshot 写入 Storage 时序历史，并提供 metric
catalog/latest/history API、看板和扁平 AND/OR 规则。Monitor 不抓取服务的
`/metrics`，也不依赖 Prometheus Server 或 Pushgateway；Storage 元数据由
部署前的 `moox-cli metadata apply` 注册和校验。

## 持久化边界

`internal/store` 负责 Monitor 控制面数据（检查、结果、告警和实例）。
`internal/metrics` 是独立的指标持久化 bounded context，负责指标目录、去重、最新值和规则状态；两者共享同一个 SQLite 文件，但通过明确的 Store 类型隔离职责。

## 历史清理与容量

- 检查结果和告警事件默认保留 14 天。`trpc.moox.monitor.data_cleanup.timer` 启动时执行一次，之后每 6 小时清理一次，单次超时 120 秒。
- 指标消息去重记录默认有效 7 天，由同一 Timer 删除到期记录。结果、告警和去重三项会全部尝试，一个步骤失败不会阻断其他步骤，最终返回汇总错误。
- 服务指标和主机指标的时序历史位于 Storage，不存放在 Monitor SQLite。Storage 对四个主机指标 Dataset 默认保留 48 小时；Monitor 不删除任何 Storage 事实。
- SQLite 异常增长通常意味着清理任务失败、事件数量异常或高基数 catalog 增长，不能只靠缩短 Storage 历史窗口解决。

完整的数据保留矩阵、永久数据和磁盘巡检命令见[数据保留与磁盘空间](../../docs/运维/数据保留与磁盘空间.md)。

## 端口

- `:11410`: tRPC/HTTP 管理 API `trpc.moox.monitor.MonitorMgr`
- `:11409`: tRPC `http_no_protocol` 健康接口（`/healthz`、`/readyz`、`/metrics`）

## 定时任务

检查调度、指标规则和 peer 同步分别由 `check_schedule.timer`（30 秒）、`metric_rule.timer`（60 秒）和 `peer_sync.timer`（10 秒）执行，均在启动时立即运行。所有维护 Handler 都同步返回错误、带显式超时并跳过同进程重入；`DefaultScheduler` 不提供跨进程互斥。

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
  service_auth:
    key_id: moox-service
    secret_key: "从受限环境文件注入"
    ca_file: ./certs/gateway-peers.pem
  peers:
    - instance_id: monitor-b
      gateway_url: https://peer.example
      node_id: gateway-peer-b
```

## 运行

```bash
../../scripts/build.sh monitor
./bin/moox-monitor-cli init --db-path ./data/monitor/monitor.db
./bin/moox-monitor -conf=config/trpc_go.yaml
```

Admin 只作为控制面和 SysDeploy 注册中心。Monitor 会周期性读取 SysDeploy active 部署，生成 `moox-system` 内置检查；探测时不依赖 Admin。

所有独立部署进程都通过 tRPC `http_no_protocol` 提供 `/healthz` 与 `/readyz`；Monitor 默认探测 `/readyz`。多实例之间通过目标节点 Gateway 的 `GetPeerSnapshot` RPC 同步，并使用统一的节点 HMAC 服务密钥。

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

指标监控的部署、限额、告警和故障处理见
[`docs/运维/MooX指标监控.md`](../../docs/运维/MooX指标监控.md)。
