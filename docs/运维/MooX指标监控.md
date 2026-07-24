# MooX 指标监控运维

## 架构和依赖

每个 tRPC 服务通过专用 health listener 提供带 HMAC 的 `/metrics`，同时由自身的 tRPC timer 每 30 秒从 Prometheus 默认 registry 生成有界快照，压缩后发布到 `MOOX_METRICS`。Monitor 不抓取 HTTP `/metrics`，也不需要部署 Prometheus Server、Pushgateway 或手工维护 target：

```text
服务 registry -> 本地 timer -> MetricSnapshot -> moox-eventbus
    -> Monitor durable consumer -> Storage 历史 + SQLite catalog/latest
    -> 看板查询 -> 结构化 AND/OR 规则 -> webhook
```

`/metrics` 仅保留作带 health HMAC 的本机调试入口，不再存在独立的未鉴权 Prometheus plugin listener。共享 `requestmetrics` filter 继续采集 tRPC 客户端/服务端请求量和耗时，写入同一个默认 registry。EventBus 不可用时业务服务继续启动，timer 记录错误并等待下一次快照；Monitor 消费失败时消息按至少一次语义重投递。

## 数据保留

- `MOOX_METRICS` 消息最多保留 24 小时或占用 512 MiB，达到任一边界后由 JetStream 淘汰最旧消息。
- Monitor SQLite 中的检查结果和告警事件默认保留 14 天，由 `data_cleanup.timer` 启动时清理一次，之后每 6 小时清理。
- 指标消息去重记录默认保留 7 天，由同一 Timer 删除过期记录；catalog、latest、规则和规则状态不随去重回执删除。
- Storage 对四个主机指标 Dataset 默认保留 48 小时，由每小时执行的 `host_metrics_cleanup.timer` 进行有界 10 批清理；Monitor 不删除 Storage 事实。
- Archive Parquet 和通用行情/因子事实不受指标历史清理影响。

全系统的当前值、永久数据和磁盘巡检命令见[数据保留与磁盘空间](数据保留与磁盘空间.md)。

## 元数据预检和启动顺序

历史数据使用 Storage 的内部 Space `moox_system` 和 Dataset `moox_service_metrics`，Space 以 `moox_` 前缀标记 MooX 管理范围。Monitor 不会在运行时创建或修改 Storage 元数据。部署脚本在启动 Monitor 前执行：

```bash
moox-cli metadata apply --file examples/platform-local.seed.yaml \
  --metadata-url http://127.0.0.1:20200
moox-cli metadata apply --file examples/metadata-monitor-metrics.seed.yaml \
  --metadata-url http://127.0.0.1:20200
```

`metadata apply` 是 create-or-verify：缺失资源按依赖顺序创建，已有资源只有在类型、频率、字段和直接
DataNode 绑定契约兼容时才报告 unchanged；不兼容会终止 Monitor 启动而不是覆盖数据。部署流程
负责注册 DataNode。Dataset 初始为 disabled，Doctor 只读检查激活条件，部署或管理员随后显式
激活，激活成功后绑定不可变。外部或多机 Storage 只需提供 Metadata 地址：

```bash
export MOOX_METRICS_STORAGE_METADATA_URL=http://storage-metadata:20200
```

外部 Storage 不得应用 `platform-local.seed.yaml`。每个指标 Dataset 在 seed 中声明
`data_node_id` 和 `keep_duration`，`series_id` 直接作为 `subject_id`，不为每条动态时序创建
独立元数据对象。

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

服务可以通过 `MOOX_METRICS_*` 环境变量覆盖大小、family、sample、label、gzip、include/exclude 限制；timer 周期仍以配置文件的 30 秒为准。Reporter 失败会记录日志并增加本地错误计数，不维护业务 outbox。

发布使用一个共享 `metrics-publisher` 发布角色和一个独立 `monitor-metrics-consumer` 消费角色。Publisher 只能发布 metrics snapshot；Monitor consumer 只订阅固定 metrics/host topic 和 durable。Monitor 为单实例，不能复用 Publisher 凭据消费，也不通过多实例抢占 durable。

Collector、CloudNode、Factor、Strategy、Trade、Archive 使用固定低基数 `moox_module_*` 指标和 `config/monitor-pipelines.yaml` 白名单水位。穿过 Storage 的功能 pipeline 当前显式延期，不从相邻模块水位推断 Storage 已正确处理。

## 看板和规则

看板从 Monitor catalog 选择已发现的 service、instance、metric 和 labels，查询 bounded history 后绘制趋势。不存在“新增监控目标”入口，服务发现来自 SysDeploy 注册表；未知 producer、缺失 schema、无 route 的消息会被拒绝。

规则是 1-8 条条件的扁平结构，条件可命名 A-H，整组使用 AND 或 OR。每个条件依次选择：series selector、时间 reducer（CURRENT/AVG/MIN/MAX/SUM/RATE/INCREASE）、series reducer（AVG/MIN/MAX/SUM）、比较符和阈值。`MAX > 100` 表示任一匹配 series 超过阈值，`MIN > 100` 表示所有匹配 series 都超过阈值。每条条件独立设置 no-data 策略，规则设置连续触发/恢复次数和 evaluation interval；不使用自由文本 PromQL，也不支持嵌套表达式。

Counter 保留绝对值，RATE/INCREASE 在 Monitor 使用按时间排序的历史点计算并处理 counter reset。通知状态按 rule、事件和 notification key 去重，网络失败会保留重试状态。历史数据在 Storage，catalog/latest、dedupe、rule state 和 evaluation 在 Monitor SQLite。

## 故障处理

| 现象 | 检查 |
| --- | --- |
| 所有服务无最新值 | `moox-eventbus /readyz`、`MOOX_METRICS` consumer pending、服务 reporter error counter |
| Monitor 启动但没有 ingest | `monitor` 日志中的 metadata schema status；确认 Space/Dataset/columns/DataNode 绑定已通过 `metadata apply` 并完成激活 |
| 单个服务 stale | 使用 health HMAC 请求服务 `/metrics`、检查 timer 日志、`MOOX_BOOT_ID`、EventBus 连接和 producer 注册 |
| DLQ 增长 | `moox.dlq.message.rejected.v1` consumer、原始 message_id、rejection_reason；修复 schema 或 producer 后重新发布新 message_id |
| 看板历史缺口 | Storage Dataset/DataNode 状态、`service_target`、WriteFields 错误、series_id 与 Attribute 身份是否完整 |

Malformed envelope、gzip bomb、未知 producer、错误 content type 和不兼容 snapshot 不会影响 Monitor 原有 HTTP/TCP 可用性检查；这些消息写入 DLQ 并终止原 delivery。重复 message_id 在 SQLite dedupe 后 ACK，不重复写入 latest。

## 容量和压测

重点监控 Reporter error、EventBus consumer pending/redelivery、Monitor ingest 延迟、Storage 写入速率、SQLite 增长和规则评估时长。压测应明确测试时长和临时目录字节上限，使用 100 services x 10 instances x 100 series 的模型，不得扫描生产数据。超过 label、sample、family 或 history 查询上限时返回有界错误，优先降低 cardinality 或调整 reporter include/exclude。

## 验证命令

```bash
go test -count=1 ./modules/monitor/... ./packages/metricspb
./scripts/test-deploy-moox-eventbus.sh
cd web && node scripts/check-metric-monitor.mjs && pnpm build:prod
```

初始部署后用 `moox-cli doctor bootstrap --format json` 验证 inventory、身份、路径和 Reporter 覆盖；故障定位用 `moox-cli doctor diagnose --format json`。操作边界和恢复动作见 [MooX Doctor 运维](MooX-Doctor运维.md)。

发布和部署完成后，先确认 EventBus ready、Storage Metadata HTTP 可访问且两个 metrics seed apply 成功，再观察两个 timer 周期，最后检查看板 latest/history 和一条测试规则的 firing/resolved 状态。
