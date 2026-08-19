# Storage View 消费队列分区实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task.

> **执行状态（2026-08-17）：** Task 1-7 已完成并通过本地单测/合同测试；真实部署/线上验收仍待执行。后续复核补充了 Primary 历史索引、游标分页、coverage repair、reset readiness 鉴权、拓扑前置校验和失败留停策略。项目明确不做旧数据兼容；旧 DataNode layout 需要 reset-all。Task 8 仅保留为正式环境操作手册，未在本次本地变更中执行。

**目标：** 将 K 线 View 与系统指标及其他 Dataset 的 Storage View 消费队列彻底隔离，避免某一类 Dataset 的积压、坏消息、慢查询或重建争用占满同一个 JetStream durable 的 ACK/Fetch 配额，导致 `binance_spot_kline_1m` 停滞。

**约束：** 项目尚未上线，可以删除现有 View 数据、EventBus 历史消息和旧 durable consumer；不做旧 durable、旧配置或旧磁盘索引的兼容迁移。Primary 事实数据默认保留，以便新 View 按 `rebuild_lookback_periods` 回溯构建；若执行全量初始化，可显式一并清空 Primary。

**架构：** 保留一个 `MOOX_STORAGE` Stream，但将 Storage View 从单一 `storage_view_period_v1` 改为显式的消费分区。每个分区拥有独立 durable、Fetch/ACK/worker 限额和状态；分区只接收其配置 Dataset 的四类 View 事件（rows、period、factor-period、sync-point）的精确 subject。K 线分区只包含 `crypto_market/binance_spot_kline_1m`，系统指标分区只包含 `moox_system/moox_service_metrics`，其余当前 View 的 source Dataset 进入显式 `other` 分区。任何 Dataset 必须且只能出现在一个分区，不能使用会与 K 线重叠的 catch-all wildcard。

**关键不变量：**

1. 同一 Dataset 的 rows、period marker、factor-period、sync-point 必须进入同一个 durable，保持 rows-before-marker 和 Dataset 内串行语义。
2. K 线分区的 pending/ack-pending、worker、重试和慢处理不能消耗系统指标或其他分区的配额；反向同理。
3. View 重建门禁只读取该 View 所依赖分区的 backlog，不再用全局 `storage_view_period_v1` 状态阻塞无关 View。
4. 分区启动、重连、停止、readyz、指标和审计日志都必须按 partition_id 可观察。
5. 新建或重建时序 View 必须按 `rebuild_lookback_periods` 覆盖每个 subject/frequency 配置的最少已完成根数，未达到回溯水位不得激活；默认所有频率统一为 `1000` 根，可由根目录 `custom.toml` 的 `[storage_view] rebuild_lookback_periods` 覆盖。无 frequency 的旧 View 才使用 `rebuild_lookback` 兜底。

**当前根因证据：** `modules/storage/internal/service/view/consume.go` 只启动一个 `storage_view_period_v1`；`eventconsumer/consumer.go` 使用 Dataset 全局事件族过滤，`MaxAckPending/FetchBatch` 属于同一个 durable；`subject_dispatcher.go` 的 Dataset lane 只能约束已拉取的消息，不能阻止 JetStream 全局 pending/Fetch 配额被其他 Dataset 占满。因此仅增加 worker 或 lane 数不能消除队头阻塞。

---

## Task 1：定义分区配置与当前 Dataset 清单

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `examples/setup/default/metadata.yaml`（仅用于核对当前 source Dataset 清单，不改变业务定义）
- Create/Modify: `packages/events/storage_consumers.go`（只放稳定 durable 名称和 topology 常量）

- [x] 在 `StorageView` 下新增 `consumer_partitions` 配置。每项至少包含：`id`、`durable`、`space_id`、`dataset_ids`、`fetch_batch`、`max_workers`、`max_ack_pending`、`ack_wait_ms`、`deliver_policy`、`max_retry_attempts`。
- [x] 为当前环境建立三类分区：
  - `kline`：`crypto_market/binance_spot_kline_1m`，durable 使用稳定的新名称，例如 `storage_view_kline_v2`。
  - `system_metrics`：`moox_system/moox_service_metrics`，durable 使用 `storage_view_metrics_v2`。
  - `other`：从 `examples/setup/default/metadata.yaml` 及实际 View 元数据盘点出的其余 source Dataset，使用 `storage_view_other_v2`；不得用 `>` wildcard 覆盖前两个分区。
- [x] 为 K 线设置保守的独立资源上限（初始建议 `max_ack_pending=16`、`fetch_batch=4`、`max_workers=2`）；系统指标和 other 使用更小或独立可调的默认值，禁止再依赖单个全局 `eventbus.max_ack_pending`。
- [x] 校验规则：partition_id/durable 唯一、Dataset 不重复、`space_id`/`dataset_ids` 非空、durable 名称符合 JetStream 规则、`fetch_batch <= max_ack_pending`、重试和 ACK 参数合法。
- [x] Storage View 启动前从 Metadata 读取所有 managed View 的 source Dataset，验证每个 Dataset 恰好映射到一个 partition；缺失、重复时 fail-fast，不启动部分消费者。配置可包含尚未创建的未来 Dataset（例如 Factor 结果），避免默认部署顺序被反向依赖。
- [x] 迁移默认值时删除 `StorageViewConsumer = storage_view_period_v1` 的业务默认依赖；旧 durable 只保留在一次性 reset 清单中，不再出现在运行时配置、ACL 或 watchdog 中。
- [x] 测试省略配置、重复 Dataset、K 线与 metrics 重叠、未知 Dataset、非法资源参数和完整覆盖校验。

## Task 2：让事件层支持精确 Dataset subject 过滤

**Files:**
- Modify: `packages/events/consumer.go`
- Modify: `packages/events/consumer_test.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer_test.go`
- Possibly modify: `packages/events/registry.go`（复用现有 `RenderSubject`，不重复定义 subject 模板）

- [x] 在 `packages/events` 增加受控的 exact subject filter 能力：由 `Event`/`Events` 指定 Stream，由 `RenderSubject` 生成四个 `space_id + dataset_id` 精确 subject，再交给 `jetstream.ConsumerConfig.FilterSubject(s)`；禁止业务层拼接裸 NATS subject。
- [x] 明确 exact filter 与现有 event-family filter 互斥；排序、去重、Stream 一致性和 subject token 编码由 `packages/events` 统一校验。
- [x] 在 `eventconsumer.Config` 增加 partition identity 与 `SpaceID/DatasetIDs`，bind 时为每个 Dataset 生成四类事件的精确过滤列表。
- [x] 保留现有 Dataset queue key、heartbeat、永久重试和 rows-before-marker 处理；过滤改造不得把四类事件拆到不同 durable。
- [x] 测试确认 Kline consumer 的 `FilterSubjects` 只包含 Kline 四类精确 subject，metrics consumer 不会收到 Kline，其他 Dataset 也不会落入 Kline；测试非法 token、重复 filter 和跨 Stream 配置。

## Task 3：重构 Storage View 为多消费者生命周期

**Files:**
- Modify: `modules/storage/internal/service/view/consume.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/internal/observability/view_metrics.go`
- Modify: `modules/storage/internal/health/server.go`
- Tests: `modules/storage/internal/service/view/*_test.go`, `modules/storage/cmd/server/main_test.go`, `modules/storage/internal/health/server_test.go`

- [x] 将 `StartEventConsumer` 改为按配置创建多个 `eventconsumer.Consumer`，所有 consumer 共享 View handler，但各自拥有独立 durable、dispatcher、pending semaphore、worker 和 reconnect loop。
- [x] 每个分区使用独立 JetStream/NATS 连接；单分区坏订阅触发 reconnect 时不得关闭其他分区的连接。
- [x] stop 函数必须按相反顺序停止全部分区，任一分区 bind/start 失败都关闭已启动分区并返回错误，不能留下半启动状态。
- [x] 将单一 `consumerState/consumerBound` 改为 partition 状态集合：记录 durable、filters、bound、pending、ack_pending、last_error、last_delivery；整体 ready 只有全部 required partition bound 才为 true。
- [x] 让 `consumerBacklog` 接受 View/source Dataset，聚合该 View 涉及的 partition 状态；Kline View 的容量重建只看 Kline partition，不被 metrics/other backlog 阻塞。多 Dataset View 要求所有依赖 partition 可用，并记录具体 blocker。
- [x] readiness/metrics 按 partition 输出，至少包含 `partition_id`、durable、bound、pending、ack_pending、oldest_pending_age、last_error；保留总览字段供旧监控读取，但不再把总 pending 作为 Kline 的门禁。
- [x] 每个 partition 的 dispatcher queue key 仍使用 `space + dataset`，坏消息只能阻塞自己的 Dataset 和所在 partition；记录 partition 标签，避免日志无法定位来源。
- [x] 测试：部分 partition bind 失败回滚、单 partition 重连、某 partition 停止不影响其他 partition、readyz 细节、聚合 backlog 和 View 依赖映射。

## Task 4：按分区重建门禁与回溯水位

**Files:**
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Modify: `modules/storage/internal/service/view/build.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/reconcile_test.go`
- Modify: `modules/storage/internal/service/view/build_test.go`

- [x] 把 View 的 source Dataset 到 partition 的映射注入 reconciler，所有 backlog/idle-check 查询使用相关 partition，而不是全局 durable 名称。
- [x] 对 size-limit/可选容量重建，只允许相关 partition bound、pending/ack_pending 低于该 partition 阈值，并连续通过 idle checks；其他 partition 的积压不应阻塞 Kline。
- [x] 对初次构建、active 缺失/损坏、schema/revision/coverage 修复和用户手动重建，仍走必要修复路径；时序 View 从 Primary 按 subject/frequency 倒序读取至少配置根数，周末/节假日不消耗根数；无 frequency 的旧 View 才按 `rebuild_lookback` 验证时间水位后 READY/Activate。
- [x] 记录 lookback 目标、实际覆盖起止时间、缺口和阻塞原因；回溯不足继续保持 build pending/failed，不得对外宣告构建完成。
- [x] 在测试中模拟 metrics partition 长期 backlog，同时 Kline partition idle，确认 Kline 可完成构建和消费；反向验证同 partition rows/marker 仍严格有序。

## Task 5：更新 EventBus ACL、部署和启动模板

**Files:**
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/eventbus/config/app.yaml`
- Modify: `modules/eventbus/internal/config/*`（如 topology 校验需要）
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/moox-storage-view-watchdog.sh`
- Modify: `deploy/systemd/system/moox-storage-view-watchdog.service`
- Modify: `scripts/tests/contract/test-storage-consistency-contract.sh`
- Modify: `scripts/tests/contract/test-deploy-moox-storage-view.sh`
- Modify: `modules/eventbus/test/storage_consumers_e2e_test.go`

- [x] 用共享 topology 常量替换 `storage_view_period_v1` 的 ACL 字符串，storage-eventbus 只允许创建/绑定新的 partition durables及其精确 subject；删除旧 durable 的运行时权限和部署引用。
- [x] 若部署仍由 Storage 动态创建 consumer，明确 EventBus 只负责 Stream，不在 registry 中隐式创建旧 consumer；启动模板必须注入同一份 partition 配置，避免二进制和 ACL 漂移。
- [x] 更新 storage profile、watchdog、start/stop/restart/status/healthcheck 中的 consumer 名称和状态判断；watchdog 按全部 partition 的 liveness 处理，不以某一个旧 durable 的 backlog 作为重启依据。
- [x] 部署合同断言：旧 `storage_view_period_v1` 不再出现；Kline/metrics/other 三个 durable、精确 filters、deliver policy、ACL 权限和配置文件一致。
- [x] 补充 clean package、local overlay、remote package 三条路径的配置完整性测试。

## Task 6：增加一次性全量清理与重新初始化命令

**Files:**
- Modify: `modules/storage/cmd/cli/main.go`
- Create: `modules/storage/cmd/cli/reset_view_consumers.go`
- Create: `modules/storage/cmd/cli/reset_view_consumers_test.go`
- Modify: `modules/storage/cmd/cli/repair_view.go`（复用安全校验和生命周期 helper）
- Modify: `modules/cli/internal/command/storage.go`、`modules/cli/internal/command/storage_test.go`
- Modify: `modules/storage/README.md`、`modules/cli/README.md`

- [x] 增加显式破坏性命令，例如：

  ```bash
  moox-cli storage reset-view-consumers \
    --storage-conf /home/ubuntu/moox/storage/config/storage.yaml \
    --stream MOOX_STORAGE \
    --yes
  ```

- [x] 命令执行前检查 `--yes`、目标 Stream、凭据和 storage-view 生命周期；输出将删除的 old/partition durables、消息数量、View index 路径和预计影响范围。
- [x] 在维护锁内停止整个 Storage 生命周期，删除旧 `storage_view`/`storage_view_period_v1` 及所有 partition durables，按配置 Dataset 的精确 subjects 清理 `MOOX_STORAGE` 历史消息，清理所有 View（包括 Record/Bleve）A/B 物理索引和未完成 build；保留 View 定义和 Primary 事实，供时序 View 按 lookback backfill 使用。
- [x] 提供 `--reset-all-storage-data` 选项，在用户明确确认时额外删除 Primary Pebble/outbox；默认不误删 Primary，避免无法满足回溯要求。
- [x] 清理成功后按新配置启动 EventBus/Storage View，创建 partition consumers，等待所有 partition bound 和 View lookback ready；任一步失败返回非零并显示恢复步骤。
- [x] dry-run 不产生 mutation；测试命令确认旧 durable 删除、精确 subject purge、索引清理、启动顺序、重复执行幂等和中途失败不留下半启动消费者。

## Task 7：端到端隔离与恢复验证

**Files:**
- Modify/Create: `modules/storage/internal/service/e2e/view_consumer_partition_e2e_test.go`
- Modify: `modules/eventbus/test/storage_consumers_e2e_test.go`
- Create: `scripts/tests/e2e/test-storage-view-consumer-partitions.sh`
- Modify: `Makefile` / `scripts/test-go-workspace.sh`（按现有入口接入）

- [x] 使用嵌入 NATS JetStream 验证三个 durable 的 filter、pending 和 ACK 独立性。
- [x] 注入一个永远失败或延迟很长的 metrics delivery，再发布 Kline rows + period marker + sync point；Kline View 必须继续推进、ACK 和查询到最新数据，metrics 只能在自己的 durable 中积压。
- [x] 验证 Kline rows 在 marker 前到达同一 consumer，marker 不得越过未完成 rows；同时确认 metrics/other 事件不会被 Kline durable 拉取。
- [x] 重启 EventBus/Storage View，验证每个 durable 独立 rebind，pending 只在所属 partition 恢复，Kline 不重复消费其他 partition。
- [x] 验证 `rebuild_lookback_periods`：Primary 中存在目标频率的足够历史根数、EventBus 从空开始，View 初建仍覆盖最低根数；不足回溯时构建不激活并暴露明确原因。
- [ ] 线上验收指标：Kline durable pending/ack_pending 连续下降、Kline View 最新时间追平 Primary/Collector；metrics backlog 增长时 Kline p95 读取/写入延迟不显著恶化；readyz 展示三个 partition 的独立状态（待真实环境执行）。

## Task 8：数据清理、发布与验收顺序

1. 在正式环境停止 Collector、Factor、Storage View 及其他会向 `MOOX_STORAGE` 写入或消费的服务，确认无写入窗口。
2. 先发布包含新 partition 配置、ACL 和 reset CLI 的 EventBus/Admin/Storage 二进制；不启动旧 `storage_view_period_v1`。
3. 执行 `reset-view-consumers --dry-run`，核对 durable、Stream purge 和 View index 路径；确认后加 `--yes` 执行。
4. 启动 EventBus，再启动 Storage Primary/Node，最后启动 Storage View；Storage View 必须等待所有 partition bound 后才对外 ready。
5. 保持 Primary 事实数据，等待或验证每个频率至少配置根数的历史；执行初次 View 构建，检查回溯水位达标后再启动 Collector/Factor。
6. 按 Task 7 的脚本观察 Kline、metrics、other 三条链路；记录每个 durable 的 pending、ack_pending、last delivery、View latest timestamp 和 rebuild lookback。
7. 只有隔离验证通过后再启用系统指标高频上报和其他 Dataset；若 Kline 仍停滞，优先查看 Kline partition 自身状态，不再清空无关 Dataset 队列。

## 完成标准

- [x] 运行时不再依赖或创建 `storage_view_period_v1`；reset CLI 仅保留旧 durable 名称用于一次性清理。
- [x] Kline、system metrics、other 分区的 filters/durable/资源配额/状态可独立观察。
- [x] metrics 或 other backlog、坏消息、重建不会阻塞 Kline View 的 rows/marker 消费（本地嵌入 JetStream E2E）。
- [x] 新 View/任何时序重建实际覆盖 `rebuild_lookback_periods` 后才能激活，日志包含目标/实际覆盖和阻塞原因。
- [x] reset CLI 可 dry-run、需要显式确认、幂等清理旧队列和 View 索引，并能从空队列重新启动。
- [ ] Storage、EventBus、Admin、部署合同、Go race、CGO View E2E 和真实部署验收全部通过。

> 本地 Storage/EventBus/Admin、部署合同、Go race、CGO View E2E 均已通过；最后一项保留未勾选，直到按 Task 8 在真实环境完成发布和线上指标验收。

## 非目标

- 不在本次改造中把 Factor 计算合并进 K 线采集或共享同一个 Dataset 写入程序。
- 不保留旧 durable consumer、旧 View index 或旧配置的兼容迁移路径。
- 不通过单纯增加全局 worker、FetchBatch 或 MaxAckPending 来掩盖分区设计问题。
