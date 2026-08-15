# moox-monitor

`moox-monitor` 是 MooX 独立健康检查模块。它聚合进程探测、Reporter、主机资源和
实时 Dataset 健康事实，不把这些检查称为处理流水线。

Monitor 还消费 `moox.metrics.snapshot.reported.v1`，将每个已注册 tRPC
实例的有界 Prometheus snapshot 写入 Storage 时序历史，并提供 metric
catalog/latest/history API、看板和扁平 AND/OR 规则。Monitor 不抓取服务的
`/metrics`，也不依赖 Prometheus Server 或 Pushgateway；控制面与 Storage 部署完成后，
由 `moox-cli setup init` 注册并校验 Storage 元数据。

模块健康检查清单由 `packages/report` 代码注册表维护；实时 Dataset 阈值由
`examples/setup/default/dataset-health-policy.yaml` 维护，只有 Monitor 读取。
服务部署位置和健康 URL 来自 Admin SysDeploy，不进入 Dataset 健康策略。

## 持久化边界

`internal/store` 负责 Monitor 控制面数据（检查、结果和告警）。
`internal/metrics` 是独立的指标持久化 bounded context，负责指标目录、去重、最新值和规则状态；两者共享同一个 SQLite 文件，但通过明确的 Store 类型隔离职责。

## 历史清理与容量

- 检查结果和告警事件默认保留 14 天。`trpc.moox.monitor.data_cleanup.timer` 启动时执行一次，之后每 6 小时清理一次，单次超时 120 秒。
- 指标消息去重记录默认有效 7 天，由同一 Timer 删除到期记录。结果、告警和去重三项会全部尝试，一个步骤失败不会阻断其他步骤，最终返回汇总错误。
- 服务指标和主机指标的时序历史位于 Storage，不存放在 Monitor SQLite。服务指标 Dataset `moox_service_metrics` 默认保留 24 小时；四个主机指标 Dataset 默认保留 48 小时；Monitor 不删除任何 Storage 事实。
- SQLite 异常增长通常意味着清理任务失败、事件数量异常或高基数 catalog 增长，不能只靠缩短 Storage 历史窗口解决。

完整的数据保留矩阵、永久数据和磁盘巡检命令见[数据保留与磁盘空间](../../docs/运维/数据保留与磁盘空间.md)。

## 端口

- `:11410`: tRPC/HTTP 管理 API `trpc.moox.monitor.MonitorMgr`
- `:11409`: tRPC `http_no_protocol` 健康接口（`/healthz`、`/readyz`、`/metrics`）

普通部署应将健康接口绑定在 loopback。control profile 为 SCF Sentinel 将
`11409` 绑定到公网网卡；该端口只接受独立 health HMAC 签名，不经 Caddy，
也不暴露 Monitor 管理 API。

## 定时任务

检查调度和指标规则分别由 `check_schedule.timer`（30 秒）和 `metric_rule.timer`（60 秒）执行，均在启动时立即运行。所有维护 Handler 都同步返回错误、带显式超时并跳过同进程重入。Monitor 是单实例服务，不做跨实例 Owner 选举。

## 配置

核心配置在 `config/app.yaml`：

```yaml
database:
  path: ./data/monitor/monitor.db
health:
  addr: ":11409"
instance:
  instance_id: monitor-a
```

## 运行

```bash
../../scripts/build.sh monitor
./bin/moox-monitor-cli init --db-path ./data/monitor/monitor.db
./bin/moox-monitor -conf=config/trpc_go.yaml
```

Admin 只作为控制面和 SysDeploy 注册中心。Monitor 会有界分页读取 SysDeploy 的 active/disabled 部署，并按 Doctor Manifest 中的独立进程过滤，生成 `moox-system` 内置检查；探测时不依赖 Admin。

所有独立部署进程都通过 tRPC `http_no_protocol` 提供 `/healthz` 与 `/readyz`；Monitor 默认探测 `/readyz`。`moox_gateway` 使用自身 Reporter 和健康接口，不借用 Admin 的健康状态。

## 管理接口

管理 API 通过 `trpc.moox.monitor.MonitorMgr` 暴露，可由 Admin 网关转发：

```text
/api/admin/moox_monitor/ListChecks
/api/admin/moox_monitor/GetOverview
/api/admin/moox_monitor/RunCheckOnce
/api/admin/moox_monitor/GetDoctorContext
```

`GetDoctorContext` 只聚合 Manifest、SysDeploy、最新检查、Reporter/功能指标、主机资源和告警事实，组件最多 64 个、模块健康检查最多 32 个、响应最多 2 MiB。它不抓取 `/metrics`、不运行 Doctor Engine，也不执行恢复动作。Monitor V1 只能部署一个实例，不包含 Peer、Owner 或 Lease。

SysDeploy 同步可手动触发 `SyncSystemChecks`；同步后的内置检查使用 `source=sysdeploy` 和 `group_name=moox-system`。

## 验证

本模块的实现验证记录见：

```text
docs/superpowers/verification/2026-07-09-monitor-module.md
```

指标监控的部署、限额、告警和故障处理见
[`docs/运维/MooX指标监控.md`](../../docs/运维/MooX指标监控.md)。
