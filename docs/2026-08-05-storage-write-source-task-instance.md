# Storage 写入源与 SCF 任务实例

## 背景与命名

行情采集改为腾讯云 SCF Timer 触发后，SCF 不再向 Collector 请求任务，也不依赖常驻心跳或 EventBus 消费来完成采集。每个 SCF 的标的、周期、数据集和 DNS 快照写入自身环境变量；Collector 只负责刷新任务分配和环境变量。这样可以缩短函数运行时间并避免为常驻进程支付资源使用费。

这里的字段统一使用英文 `write_source`，前端显示为“写入源”，不使用 `source_function_name` 或“请求源”：它描述的是**实际把数据写入 Storage 的上游执行者**，而不是一次 HTTP 请求的调用方。当前 SCF 值为 `scf:<function-name>`，例如 `scf:moox-fetcher-crypto-market-ap-shanghai-5`。

## 写入链路

1. Collector 根据启用的 K 线规则和全量 Symbol 快照，把每个任务组按最多 30 个标的分配给一个 Timer SCF。
2. 分配结果写入 `TaskInstance.c_function_name`，同时提交该 SCF 的环境变量。稳定的 `task_id` 不随 SCF 迁移变化。
3. SCF Timer 读取本地环境变量，调用行情接口，聚合结果后通过 Storage Primary 的 `UpsertFields` 写入 `write_source=scf:<function-name>`。
4. PrimaryStore、DataNode 和 Pebble outbox 原样传递该字段，发布 `storage.dataset.rows.upserted@2`。字段位于事件顶层，不放到业务行 attributes 中。
5. Collector 以空间为范围启动持久 NATS JetStream consumer，消费 Storage 变更事件。对每个已写入的时序行，用事件 envelope 的 `occurred_at` 更新匹配任务实例的 `last_exec_status=SUCCESS` 和 `last_exec_time`。

## 一致性规则

- 消费者只接受 `scf:` 写入源；空写入源不影响任务新鲜度。
- 更新条件包含 `space_id + dataset_id + subject_id + frequency + function_name`。SCF 重新分配后，旧函数迟到的事件不会覆盖新函数的执行时间。
- `last_exec_node` 仍表示逻辑执行节点；SCF 函数名单独存放在 `c_function_name`，前端“写入源”列展示该值。
- Storage 事件重复投递是正常情况；更新时间只在事件时间更新时才前进，重复事件不会回退水位。
- Timer 路径不发布行情完成事件；Storage 变更是成功写入的事实来源，CLS 仅用于单标的耗时和结果诊断。

## 失败与恢复

- 环境变量更新失败时，Collector 保留旧配置并由 Monitor 告警；不会先清空任务实例的执行历史。
- JetStream 消费失败会 NAK，解码错误会 TERM，服务重启后从 durable consumer 继续消费。
- 任务分配每次先清理当前受管 SCF 的旧绑定，再写入本轮确定性绑定；执行历史列保持不变。
- Collector 重启或 DNS 首次刷新失败时不发送空 DNS 快照，避免覆盖 SCF 上一次可用值；行情请求仍可回退系统 DNS。

## 维护提示

修改 Storage 写入 RPC 或事件 payload 时，必须同时检查 PrimaryStore、DataNode、outbox、`packages/storagepb` 和 Collector 的 Storage consumer。新增数据源时只需沿用 `write_source` 规范，不要重新引入 `source_function_name` 等同义字段。
