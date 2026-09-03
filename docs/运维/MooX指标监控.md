# MooX 指标监控运维

## 架构和依赖

每个 tRPC 服务通过专用 health listener 提供带 HMAC 的 `/metrics`，同时由自身的 tRPC timer 每 30 秒从 Prometheus 默认 registry 生成有界快照，压缩后发布到 `MOOX_METRICS`。Monitor 不抓取 HTTP `/metrics`，也不需要部署 Prometheus Server、Pushgateway 或手工维护 target：

```text
服务 registry -> 本地 timer -> MetricSnapshot -> moox-eventbus
    -> Monitor Consumer -> Storage 历史 + SQLite 最新值
    -> 健康事实聚合 -> 健康监控页面 / 全局通知通道
```

`/metrics` 仅保留作带 health HMAC 的本机调试入口，不再存在独立的未鉴权 Prometheus plugin listener。共享 `requestmetrics` filter 继续采集 tRPC 客户端/服务端请求量和耗时，写入同一个默认 registry。EventBus 不可用时业务服务继续启动，timer 记录错误并等待下一次快照；Monitor 消费失败时消息按至少一次语义重投递。

## 数据保留

- `MOOX_METRICS` 消息最多保留 24 小时或占用 512 MiB，达到任一边界后由 JetStream 淘汰最旧消息。
- Monitor SQLite 中的检查结果和告警事件默认保留 14 天，由 `data_cleanup.timer` 启动时清理一次，之后每 6 小时清理。
- 指标消息去重记录默认保留 7 天，由同一 Timer 删除过期记录；最新值仅供内部健康聚合使用。
- 服务指标历史写入 Storage 内部 Dataset `dataset_mooxsys_service_metrics`，默认保留 24 小时；高基数指标应优先降低 label cardinality，不能仅依赖延长历史窗口。
- Storage 对四个主机指标 Dataset 默认保留 48 小时，由每小时执行的 `host_metrics_cleanup.timer` 进行有界 10 批清理；Monitor 不删除 Storage 事实。
- Archive Parquet 和通用行情/因子事实不受指标历史清理影响。

全系统的当前值、永久数据和磁盘巡检命令见[数据保留与磁盘空间](数据保留与磁盘空间.md)。

## 元数据初始化

历史数据使用 Storage 的内部 Space `mooxsys` 和 Dataset `dataset_mooxsys_service_metrics`，Space 以 `moox_`
前缀标记 MooX 管理范围。Monitor 和部署脚本都不会创建或修改 Storage 元数据。完成 Control 与
Storage 部署后，统一执行：

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

`setup init` 内部执行严格 create-or-verify：缺失资源按依赖顺序创建，已有资源只有在类型、频率、
字段和直接 DataNode 绑定契约一致时才报告 unchanged；不兼容会终止初始化而不是覆盖数据。部署流程
负责注册 DataNode，初始化命令检查激活条件后激活 Dataset，成功后绑定不可变。

```bash
export MOOX_METRICS_STORAGE_METADATA_URL=http://storage-metadata:20200
```

外部 Storage 只需从默认 metadata 中导入 `mooxsys`。每个指标 Dataset 在 seed 中声明
`data_node_id` 和 `keep_duration`；服务指标 Dataset `dataset_mooxsys_service_metrics` 默认使用 24 小时
保留窗口。`series_id` 直接作为 `subject_id`，不为每条动态时序创建独立元数据对象。

## Topic、payload 和生产者配置

服务指标事件是 `moox.metrics.reported.v1.<space>.<subject>`，主机指标事件是 `moox.metrics.host.reported.v1.<space>.<subject>`，两者均使用固定媒体类型 `application/vnd.moox.event+protobuf` 和 `EventMessage` 外层契约。服务指标 payload 包含服务身份、boot_id、序列和指标快照；每个样本最多 20 个 label，默认单次解压 4 MiB、压缩 1 MiB、2,000 个 metric family 和 20,000 个 samples；超限快照整体拒绝，不截断半个 family。

默认 reporter 配置：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| timer interval | 30s | 由 tRPC timer YAML 所有实例执行 |
| max_uncompressed_bytes | 4 MiB | 解压后上限 |
| max_compressed_bytes | 1 MiB | gzip payload 上限 |
| max_metric_families | 2,000 | family 数量上限 |
| max_samples | 20,000 | 展平 histogram/summary 后上限 |
| max_labels_per_sample | 20 | 包括生成的 `le`/`quantile` |

服务可以通过 `MOOX_METRICS_*` 环境变量覆盖大小、family、sample、label 和 gzip 限制；业务指标白名单由代码维护，用户不能通过 include/exclude 环境变量重新引入 Go、进程、tRPC 或 HTTP 技术指标。timer 周期仍以配置文件的 30 秒为准。Reporter 失败会记录日志并增加本地错误计数，不维护业务 outbox。

发布使用一个共享 `metrics-publisher` 发布角色和一个独立 `monitor-metrics-consumer` 消费角色。Publisher 只能发布 metrics snapshot；Monitor consumer 只订阅固定 metrics/host topic 和 durable。Monitor 为单实例，不能复用 Publisher 凭据消费，也不通过多实例抢占 durable。

Collector、CloudNode、Factor、Strategy、Trade、Archive 使用固定低基数
`moox_<module>_*` 指标和代码内置的模块健康检查注册表。只有具备同一业务时间域的权威输入、输出时间时
才生成水位健康检查；Collector、Factor 的实时连续性按启用中的 Dataset + Frequency 独立判断，
不把多个时序汇总成一个模块水位。穿过 Storage 的功能检查当前显式延期，不从相邻模块水位
推断 Storage 已正确处理。

View 驱动因子链路额外暴露以下固定低基数指标（不包含 subject、period 或 row key）：

| 指标 | 标签 | 含义 |
| --- | --- | --- |
| `moox_collector_period_pending_total` | `dataset,frequency` | 等待上报 Dataset 周期完成事件的数量 |
| `moox_collector_period_report_retry_total` | `dataset,frequency` | 周期完成事件上报或落库失败次数 |
| `moox_storage_view_period_waiting_datasets` | `view,frequency` | Source View 当前尚未收齐的 Dataset 数量 |
| `moox_storage_view_ready_publish_retry_total` | `view,event` | Source/Result ready 发布失败后重试次数 |
| `moox_storage_view_restore_duration_seconds` | 无 | 最近一次 View 索引恢复耗时 |
| `moox_storage_view_restore_ready` | 无 | 最近一次 View 索引恢复是否完成 |
| `moox_storage_view_restore_failures_total` | 无 | View 索引恢复失败累计次数 |
| `moox_factor_period_running` | `source_view,frequency` | 正在执行的因子周期数量 |
| `moox_factor_period_degraded_total` | `source_view,frequency` | 输入缺失或因子执行失败而降级的周期数量 |
| `moox_factor_batch_running` | `source_view,frequency` | 当前 View-ready 运行中尚未完成的标的批次数（包含排队批次） |
| `moox_factor_batch_total` | `source_view,frequency,status` | 标的批次完成数量 |
| `moox_factor_batch_factor_total` | `source_view,frequency` | 批次内处理的因子成员数量 |
| `moox_factor_batch_elapsed_seconds` | `source_view,frequency` | View-ready 本轮总耗时（按标的批次记录，非单批精确耗时） |
| `moox_factor_manifest_clear_total` | `binding` | 因跳过/失败 subject 清理结果 manifest 的次数 |
| `moox_factor_source_ready_lag_seconds` | `source_view,frequency` | Factor 开始执行时 Source ready 的滞后秒数 |

这些指标只用于运行观测，不改变完成事件的 payload，也不作为是否发布 ready 的判定条件。

## 健康页面和通知

健康页面只展示中文的当前告警、行情采集、因子计算、交易与余额和核心服务状态，不展示原始 metric、labels、实例 JSON 或阈值编辑器。系统检查、业务新鲜度和主机阈值由代码和运行时事实维护。

Monitor 将所有告警发送到一个全局通知通道，支持企业微信或飞书二选一。通道 URL 为空时停止站外发送，但告警状态和事件仍会保存；页面只返回通道类型、配置状态和掩码 URL。

## 故障处理

| 现象 | 检查 |
| --- | --- |
| 所有服务无最新值 | `moox-eventbus /readyz`、`MOOX_METRICS` consumer pending、服务 reporter error counter |
| Monitor 启动但没有 ingest | `monitor` 日志中的 metadata schema status；确认 Space/Dataset/columns/DataNode 绑定已通过 `metadata apply` 并完成激活 |
| 单个服务 stale | 使用 health HMAC 请求服务 `/metrics`、检查 timer 日志、`MOOX_BOOT_ID`、EventBus 连接和 producer 注册 |
| Consumer terminated 增长 | Monitor structured error log、原始 `event_id`、producer identity 和 payload schema |
| 看板历史缺口 | Storage Dataset/DataNode 状态、`service_target`、UpsertFields 错误、series_id 与 Attribute 身份是否完整 |

Malformed envelope、gzip bomb、未知 producer、错误 content type 和不兼容 snapshot 不会影响
Monitor 原有 HTTP/TCP 可用性检查；这些消息记录结构化错误并 TERM。重复 `event_id` 在
SQLite dedupe 后 ACK，不重复写入 latest。

## 容量和压测

重点监控 Reporter error、EventBus consumer pending/redelivery、Monitor ingest 延迟、Storage 写入速率、SQLite 增长和规则评估时长。压测应明确测试时长和临时目录字节上限，使用 100 services x 10 instances x 100 series 的模型，不得扫描生产数据。超过 label、sample、family 或 history 查询上限时返回有界错误，优先降低 cardinality 或调整 reporter include/exclude。

## 验证命令

```bash
go test -count=1 ./modules/monitor/... ./packages/metricspb
./scripts/test/contract/test-deploy-moox-eventbus.sh
cd web && node scripts/check-health-monitor.mjs && pnpm build:prod
```

初始部署后用 `moox-cli doctor bootstrap --format json` 验证 inventory、身份、路径和 Reporter 覆盖；故障定位用 `moox-cli doctor diagnose --format json`。操作边界和恢复动作见 [MooX Doctor 运维](MooX-Doctor运维.md)。

发布和部署完成后，先确认 EventBus ready、Storage Metadata HTTP 可访问且两个 metrics seed apply 成功，再观察两个 timer 周期，最后检查看板 latest/history 和一条测试规则的 firing/resolved 状态。
