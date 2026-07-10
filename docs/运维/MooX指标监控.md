# MooX 指标监控运维

## 架构和依赖

每个 tRPC 服务继续提供本地 `/metrics`，同时由自身的 tRPC timer 每 30 秒从 Prometheus 默认 registry 生成有界快照，压缩后发布到 `MOOX_METRICS`。Monitor 不抓取 HTTP `/metrics`，也不需要部署 Prometheus Server、Pushgateway 或手工维护 target：

```text
服务 registry -> 本地 timer -> MetricSnapshot -> moox-eventbus
    -> Monitor durable consumer -> Storage 历史 + SQLite catalog/latest
    -> 看板查询 -> 结构化 AND/OR 规则 -> webhook
```

`/metrics` 仅保留作本机调试和兼容入口。EventBus 不可用时业务服务继续启动，timer 记录错误并等待下一次快照；Monitor 消费失败时消息按至少一次语义重投递。

## 元数据预检和启动顺序

历史数据使用 Storage 的内部 Space `moox_system` 和 Dataset `moox_service_metrics`，Space 以 `moox_` 前缀标记 MooX 管理范围。Monitor 不会在运行时创建或修改 Storage 元数据。部署脚本在启动 Monitor 前执行：

```bash
moox-cli metadata apply --file examples/platform-local.seed.yaml \
  --metadata-url http://127.0.0.1:20200
moox-cli metadata apply --file examples/metadata-monitor-metrics.seed.yaml \
  --metadata-url http://127.0.0.1:20200
moox-cli metadata apply --file examples/metadata-monitor-metrics-local-route.seed.yaml \
  --metadata-url http://127.0.0.1:20200
```

`metadata apply` 是 create-or-verify：缺失资源按依赖顺序创建，已有资源只有在类型、频率、字段、路由等契约兼容时才报告 unchanged；不兼容会终止 Monitor 启动而不是覆盖数据。单机 Storage 使用内置 local route seed。外部或多机 Storage 必须设置：

```bash
export MOOX_METRICS_STORAGE_METADATA_URL=http://storage-metadata:20200
export MOOX_METRICS_STORAGE_ROUTE_SEED=/etc/moox/metadata/metrics-route-prod.yaml
```

外部 Storage 不得应用 `platform-local.seed.yaml`。路由 seed 应为每个 PrimaryStore 节点声明同优先级的 `subject_id` 通配路由；series_id 直接作为 subject_id，不为每条动态时序创建 Subject。

## Topic、payload 和生产者配置

快照 Topic 是 `moox.metrics.snapshot.reported.v1`，payload content type 是 `application/vnd.moox.metrics.snapshot+protobuf`。外层身份、时间、序列、boot_id 和 `space_id` 由 `MooxMessage` 提供。每个样本最多 20 个 label，默认单次解压 4 MiB、压缩 1 MiB、2,000 个 metric family 和 20,000 个 samples；超限快照整体拒绝，不截断半个 family。

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

## 看板和规则

看板从 Monitor catalog 选择已发现的 service、instance、metric 和 labels，查询 bounded history 后绘制趋势。不存在“新增监控目标”入口，服务发现来自 SysDeploy 注册表；未知 producer、缺失 schema、无 route 的消息会被拒绝。

规则是 1-8 条条件的扁平结构，条件可命名 A-H，整组使用 AND 或 OR。每个条件依次选择：series selector、时间 reducer（CURRENT/AVG/MIN/MAX/SUM/RATE/INCREASE）、series reducer（AVG/MIN/MAX/SUM）、比较符和阈值。`MAX > 100` 表示任一匹配 series 超过阈值，`MIN > 100` 表示所有匹配 series 都超过阈值。每条条件独立设置 no-data 策略，规则设置连续触发/恢复次数和 evaluation interval；不使用自由文本 PromQL，也不支持嵌套表达式。

Counter 保留绝对值，RATE/INCREASE 在 Monitor 使用按时间排序的历史点计算并处理 counter reset。通知状态按 rule、事件和 notification key 去重，网络失败会保留重试状态。历史数据在 Storage，catalog/latest、dedupe、rule state 和 evaluation 在 Monitor SQLite。

## 故障处理

| 现象 | 检查 |
| --- | --- |
| 所有服务无最新值 | `moox-eventbus /readyz`、`MOOX_METRICS` consumer pending、服务 reporter error counter |
| Monitor 启动但没有 ingest | `monitor` 日志中的 metadata schema status；确认 Space/Dataset/columns/route 已通过 `metadata apply` |
| 单个服务 stale | 服务 `/metrics`、timer 日志、`MOOX_BOOT_ID`、EventBus 连接和 producer 注册 |
| DLQ 增长 | `moox.dlq.message.rejected.v1` consumer、原始 message_id、rejection_reason；修复 schema 或 producer 后重新发布新 message_id |
| 看板历史缺口 | Storage PrimaryStore 路由、WriteTimeSeriesRows 错误、series dimensions 是否完整；不要只按 subject_id 查询 |

Malformed envelope、gzip bomb、未知 producer、错误 content type 和不兼容 snapshot 不会影响 Monitor 原有 HTTP/TCP 可用性检查；这些消息写入 DLQ 并终止原 delivery。重复 message_id 在 SQLite dedupe 后 ACK，不重复写入 latest。

## 容量和压测

重点监控 Reporter error、EventBus consumer pending/redelivery、Monitor ingest 延迟、Storage 写入速率、SQLite 增长和规则评估时长。压测应明确测试时长和临时目录字节上限，使用 100 services x 10 instances x 100 series 的模型，不得扫描生产数据。超过 label、sample、family 或 history 查询上限时返回有界错误，优先降低 cardinality 或调整 reporter include/exclude。

## 验证命令

```bash
go test -count=1 ./modules/monitor/... ./packages/metricspb
./scripts/test-deploy-moox-eventbus.sh
cd web && node scripts/check-metric-monitor.mjs && pnpm build:prod
```

发布和部署完成后，先确认 EventBus ready、Storage Metadata HTTP 可访问且两个 metrics seed apply 成功，再观察两个 timer 周期，最后检查看板 latest/history 和一条测试规则的 firing/resolved 状态。
