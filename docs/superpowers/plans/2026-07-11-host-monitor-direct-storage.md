# Host Monitor Direct Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将主机监控历史样本从 Monitor SQLite 迁移到 MooX Storage 时序数据，Monitor 只负责消费、实时快照、告警和 API，主机历史最多保留 3 天。

**Architecture:** Host Agent 继续通过 EventBus 发布 `HostMetric`。Monitor durable consumer 校验消息后直接写入四个 Host Dataset；Storage 写成功后才 ACK，失败允许有限重投后丢弃。Monitor 不再保存主机样本 inbox/history/latest 表，实时 latest 保留在内存，历史通过 Monitor RPC 查询 Storage。告警在 Monitor 消费链路中基于原始 HostMetric 计算，Storage 的 `rows_updated` 事件只作为其他消费者和后续恢复用途。

**Tech Stack:** Go 1.24、tRPC-Go、Protocol Buffers、NATS JetStream、MooX Storage Access RPC、Pebble PrimaryStore、Monitor SQLite（仅控制面）、Vue 3、Arco Design。

**Updated:** 2026-07-11。告警规则缓存改用现有 `github.com/mooyang-code/snapshotcache`；规则 CRUD 不主动刷新，统一由周期刷新发现变更。旧 `go-commlib/dbcache` 不作为运行时依赖。

---

## 1. 锁定决策

- Host Agent 不使用 SQLite、outbox、磁盘重试队列或旧样本补发。
- Monitor 不保存 HostMetric 原始 payload、inbox、history、latest 样本表；旧表不再读写。
- Monitor SQLite 仅保留普通检查、告警规则、告警状态和告警事件等控制面数据。
- Storage Dataset 固定为 `host_resource_v1`、`host_fs_v1`、`host_disk_v1`、`host_net_v1`，统一 `space_id=moox_system`、`freq=1m`、`subject_id=agent_id`。
- Storage 主机历史最多保留 3 天，过期数据由 Storage maintenance 分批删除，允许历史缺口。
- Storage 写入成功后 ACK；失败按 `1s/5s/15s` NAK，最多交付 3 次后允许丢弃。
- 同一 `dataset + subject_id + freq + dimensions + data_time` 重写必须幂等。
- 浏览器始终访问 Monitor API，不直接访问 SQLite 或 Storage。
- `ListHostAgents` 返回 Monitor 内存中的最近 agent；Monitor 重启后下一条合法样本到达时重新注册。
- `QueryHostMetricHistory` 扫描 Storage 四个 Dataset，按 `data_time` 合并成 HostSnapshot；Storage 不可用时返回明确错误，不返回伪造零值。
- 告警在 Monitor 消费 HostMetric 时计算，Storage `rows_updated` 不作为唯一告警触发器。
- 告警规则启动时加载到现有的 `github.com/mooyang-code/snapshotcache` 快照缓存；消费路径只读内存缓存，禁止每条 HostMetric 查询 SQLite。
- 规则新增、修改、删除只写 SQLite，不主动刷缓存；统一由定时刷新发现变更。刷新失败保留上一份有效快照。

## 2. 文件边界

### Monitor

- Modify: `modules/monitor/internal/config/config.go`、`modules/monitor/config/app.yaml`：Host Storage target、Dataset、`1m`、72h retention、超时和查询上限。
- Modify: `modules/monitor/internal/hostmetrics/hostmetrics.go`：移除 GORM 样本写入，保留校验、内存 registry、consumer 和 DLQ。
- Create: `modules/monitor/internal/hostmetrics/storage_writer.go`：HostSnapshot 到四类 `storagepb.TimeSeriesRow` 的转换和批量 RPC。
- Create: `modules/monitor/internal/hostmetrics/storage_reader.go`：Storage scan、分页、四类 row 合并和历史转换。
- Create: `modules/monitor/internal/hostmetrics/alerts.go`：Host 阈值计算、firing/resolved 和告警去重。
- Create: `modules/monitor/internal/hostmetrics/rule_cache.go`：基于 `github.com/mooyang-code/snapshotcache` 的 Host 规则快照和刷新统计。
- Create: `modules/monitor/internal/hostmetrics/storage_writer_test.go`、`storage_reader_test.go`、`alerts_test.go`。
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`、`modules/monitor/internal/rpc/host.go`。
- Modify: `modules/monitor/schema/monitor.sql`：不再创建 Host sample inbox/latest/history/outbox 表，保留告警控制表。
- Modify: `modules/monitor/go.mod`、`modules/monitor/go.sum`：引入已经被 Storage 使用的 `github.com/mooyang-code/snapshotcache`。
- Modify/Regenerate: `modules/monitor/proto/monitor.proto`、`modules/monitor/proto/monitorgen/*`：增加 `storage_available`、`data_gap`、`rate_available` 状态。

### Storage

- Modify/Regenerate: `modules/storage/proto/access.proto`、`modules/storage/proto/gen/*`：增加受限 `DeleteTimeSeriesRows` maintenance RPC。
- Modify: `modules/storage/internal/services/access/data.go`：校验删除范围、最大批量和 Dataset/Space 边界。
- Modify: `modules/storage/internal/services/primary/client.go`、`local.go`、`service.go`：按版本范围分页删除的 PrimaryStore 接口。
- Modify: `modules/storage/internal/infra/device/store.go`、`pebble/store.go`：Pebble 批量删除。
- Modify: `modules/storage/internal/services/view/maintenance.go`：raw time-series retention pass。
- Modify: `modules/storage/internal/config/loader.go`、`modules/storage/config/storage.yaml`：四个 Host Dataset 使用 3d retention。
- Create: `modules/storage/internal/services/access/delete_test.go`、`modules/storage/internal/services/primary/retention_test.go`。

### Metadata、发布和前端

- Modify: `examples/metadata-monitor-host.seed.yaml`、`scripts/release.sh`、`scripts/deploy-moox.sh`、`scripts/test-deploy-moox-eventbus.sh`。
- Modify: `web/src/api/modules/host-monitor.ts`、`web/src/views/container/resource-monitor/resource-monitor.vue`。
- Modify: `docs/监控配置.md`、`skills/moox/references/host-agent.md`。

## 3. 执行任务

### Task 1: 固化配置和 metadata contract

**Files:** Monitor config、Storage config、Host metadata seed、EventBus config。

- [ ] 写失败测试：默认值为 `moox_system`、`1m`、72h、四个 Dataset；缺失 target/Dataset ID 失败；Host retention 不覆盖普通 Dataset。
- [ ] 增加 `HostMetricsStorageConfig`，字段为 `access_target`、`metadata_target`、`frequency`、`retention`、`write_timeout`、`read_limit`；retention 只允许 1h 到 72h。
- [ ] 为四个 Dataset 增加 `freq=1m`、完整 columns 和 3d retention dry-run 校验。
- [ ] 运行 `go run ./modules/cli/cmd/moox-cli metadata apply --file examples/metadata-monitor-host.seed.yaml --dry-run` 和 `go test -count=1 ./modules/monitor/internal/config ./modules/storage/internal/config`。
- [ ] 提交 `feat(monitor): define direct host storage contract`。

### Task 2: 实现 HostSnapshot 到 Storage rows 的转换

**Files:** `modules/monitor/internal/hostmetrics/storage_writer.go`、`storage_writer_test.go`、`hostmetrics.go`。

- [ ] 先写失败测试：minute bucket、CPU/memory 行、filesystem `device+mountpoint` 维度、disk/network `device` 维度、重复 entity、rate unavailable、确定性排序。
- [ ] 定义 `HostStorageWriter.WriteSnapshot(ctx, snapshot, agentID, observedAt, messageID) error`，按 Dataset 分组调用 Storage Access。
- [ ] 每个 row 使用 `space_id=moox_system`、`subject_id=agentID`、`freq=1m`、`data_time=bucketStart`；attributes 写入 `message_id` 和 `agent_id`。
- [ ] TypedValue 规则固定：整数 int64、percent/rate/IOPS double、可用性/只读 bool、entity string；`rate_available=false` 时不写 rate 数值列。
- [ ] 运行 `go test -count=1 ./modules/monitor/internal/hostmetrics -run TestHostStorageWriter`，提交 `feat(monitor): map host snapshots to storage rows`。

### Task 3: 改造 Monitor consumer 为 direct write + ACK

**Files:** `modules/monitor/internal/hostmetrics/hostmetrics.go`、`modules/monitor/internal/bootstrap/bootstrap.go`、`modules/eventbus/config/app.yaml`、`consumer_test.go`。

- [ ] 写 fake delivery/writer 测试：校验失败 Term；Storage 成功后更新内存 latest 再 ACK；Storage 失败 Nak；告警失败不影响 ACK。
- [ ] 将 Store 改为内存 registry + HostStorageWriter，删除 EnsureSchema、GORM transaction、inbox/history/latest payload 写入。
- [ ] 复用 Storage Access/Metadata client 和 target 规范化；启动时只读校验四个 Dataset、columns、active route。
- [ ] Consumer 固定 `MaxDeliver=3`，顺序为 `Validate -> WriteSnapshot -> registry.Update -> AlertEvaluate -> Ack`；Storage gate 失败时不 fetch。
- [ ] 运行 `go test -race -count=1 ./modules/monitor/internal/hostmetrics ./modules/monitor/internal/bootstrap`，提交 `feat(monitor): write host metrics directly to storage`。

### Task 4: 从 Storage 实现实时/历史查询

**Files:** `storage_reader.go`、`storage_reader_test.go`、`modules/monitor/internal/rpc/host.go`、`modules/monitor/proto/monitor.proto` 和 generated files。

- [ ] 写 fake Access 测试：空 subject/freq/dimensions dataset scan、time range、分页、四 Dataset 合并和缺列 data-gap。
- [ ] Reader 扫描四个 Dataset，按 agent/dimension/time 归并；禁止使用数组下标充当 dimension。
- [ ] `ListHostAgents` 读取内存 latest；`QueryHostMetricHistory` 固定 `moox_system`，默认 1h，最大 3d/500 点；Storage 错误返回 `storage_available=false`。
- [ ] 运行 `make -C modules/monitor/proto all` 和 `go test -race -count=1 ./modules/monitor/internal/hostmetrics ./modules/monitor/internal/rpc`，提交 `feat(monitor): query host history from storage`。

### Task 5: 移除 HostMetric SQLite 样本依赖

**Files:** `modules/monitor/schema/monitor.sql`、HostMetric bootstrap/RPC、`bootstrap_test.go`。

- [ ] schema 测试断言新 DB 不创建 `t_monitor_host_inbox`、`t_monitor_host_latest`、`t_monitor_host_history`、history outbox；普通 checks/alert tables 仍存在。
- [ ] 旧 DB 中的 Host sample 表默认不自动 DROP，代码不再读写；增加显式 `monitor-cli cleanup-host-sample-tables`，由用户确认后删除旧表。
- [ ] 删除 bootstrap Host schema 初始化和 history cleaner，Monitor 只初始化通用 schema、Storage gate、consumer 和告警 worker。
- [ ] 运行 `go test -count=1 ./modules/monitor/...`，提交 `refactor(monitor): remove host sample sqlite path`。

### Task 6: 抽象 DB 快照缓存并实现 Host 告警

**Files:** `modules/monitor/internal/hostmetrics/alerts.go`、`alerts_test.go`、`rule_cache.go`、`rule_cache_test.go`、existing alert repository、Host RPC/proto、`modules/monitor/go.mod`、`modules/monitor/go.sum`；复用外部模块 `github.com/mooyang-code/snapshotcache`，不修改旧 `go-commlib/dbcache`。

- [ ] 规则固定 `space_id=moox_system`，rule key 为 `host:<agent_id>:<metric>`；支持 CPU、memory、filesystem usage、disk utilization、network errors；无 baseline 时为 unavailable。
- [ ] 评估旧 `/Users/mooyang/Documents/go/src/github.com/mooyang-code/go-commlib/dbcache`：只借鉴表注册、首次加载、周期刷新、过滤器和索引的思路；不继续维护其 GORM v1/MySQL/连接发现耦合、永久 goroutine、全局 logger、反射字段写入和逐条可变更新。暂不在该模块继续堆兼容 API。
- [ ] 复用已有 `github.com/mooyang-code/snapshotcache`：它已被 Storage 使用，提供泛型 `Cache[T]`、Source、索引、原子快照发布、刷新并发保护、`Start/Stop` 生命周期、刷新状态和失败保留旧快照能力。
- [ ] `RuleCache` 在 Monitor bootstrap 首次加载 enabled Host rules；消费路径只执行 `cache.Get`，不允许调用 repository/SQLite。缓存值使用不可变深拷贝，避免消费 goroutine 修改共享切片。
- [ ] 统一由 `snapshotcache` 的 `RefreshInterval=30s` 调用 source；刷新成功原子替换快照，失败保留上一份有效快照并增加低基数失败计数。规则 CRUD 成功后不得调用 Refresh/Invalidate，最迟一个刷新周期生效。
- [ ] 启动首次加载失败时告警 evaluator degraded，但不得阻塞 HostMetric 写 Storage/ACK；没有成功快照时只跳过告警计算，不伪造 resolved。
- [ ] 测试 threshold firing、连续样本恢复、重复 message redelivery、rate unavailable、offline 状态、`message_id + rule_id` 去重，以及连续多条样本只产生一次 DB load；额外测试 SnapshotCache 的并发 Get、刷新失败保留旧快照和 Stop 不泄漏 goroutine。
- [ ] Storage 成功后从缓存读取规则并执行 evaluator，firing/resolved 写现有 Monitor SQLite alert state/event；通知失败不回滚 Storage/ACK。
- [ ] Host alert API 忽略请求 Space 并固定 `moox_system`，普通业务监控 API 保持原隔离。
- [ ] 运行 `go test -race -count=1 ./modules/storage/internal/infra/metadata/cache ./modules/monitor/internal/hostmetrics ./modules/monitor/internal/repository ./modules/monitor/internal/rpc`，提交 `feat(monitor): cache host alert rules during ingest`。

### Task 7: Storage raw time-series 3 天 retention

**Files:** Storage access proto/generated、Access service、PrimaryStore client/local/service、device store/Pebble、View maintenance、Storage config、delete/retention tests。

- [ ] 删除测试断言：必须提供 `space_id + dataset_id + time_range`，只能删除 time-series，单批最多 1000 行，只删除 `data_time < now-72h`。
- [ ] 增加内部 `DeleteTimeSeriesRows`，按 route 分页执行；不发布 rows-updated 事件，不开放给 Monitor/前端。
- [ ] PrimaryStore/Pebble 使用完整 PrimaryStoreKey 批量删除，批次提交后释放锁，支持 context cancel。
- [ ] Storage maintenance 每小时扫描四个 Host Dataset 的 72h 之前数据，每轮最多 1000 行；失败下轮重试，不能影响正常写入/View builder。
- [ ] 运行 Storage access/primary/Pebble/View 的 race tests，提交 `feat(storage): expire host time series after three days`。

### Task 8: 前端、发布和文档迁移

**Files:** Host monitor API/page、release/deploy scripts、deployment test、monitoring docs、Host Agent skill reference。

- [ ] mapping 测试覆盖 missing column -> unavailable、Storage unavailable、data gap；不得把缺失转换为 0。
- [ ] 页面历史选择最大 3d，显示 rate unavailable、Storage 错误和历史缺口；实时卡片仍使用 Monitor API。
- [ ] release/deploy 校验四个 Host Dataset、`freq=1m`、retention=3d，不再调用 Host SQLite 初始化。
- [ ] 运行 `./scripts/test-deploy-moox-eventbus.sh`、`pnpm -C web build:prod`、`go test -count=1 ./modules/monitor/... ./modules/storage/...`，提交 `feat(deploy): publish direct-storage host monitoring`。

### Task 9: 端到端验收

**Files:** `modules/monitor/internal/hostmetrics/direct_storage_e2e_test.go`、`scripts/test-deploy-moox-eventbus.sh`。

- [ ] fresh Storage 导入 logical/route seed，发送 HostMetric，断言四 Dataset 分钟行出现且 consumer ACK。
- [ ] 重复、同一分钟和乱序消息验证 Storage key 幂等、历史点数量稳定。
- [ ] Storage 停止时验证 NAK/最终丢弃；恢复后新样本继续写入，旧样本不承诺补齐。
- [ ] 插入超过 72h 的 Host rows，执行 maintenance，断言旧行删除、72h 内行保留、其他 Dataset 不受影响。
- [ ] 浏览器验收 realtime latest、Storage history、Storage unavailable、data gap、告警 firing/resolved、`moox_system` 跨 Space 可见。
- [ ] 最终运行 `go test -race -count=1 ./packages/hostmetricpb ./packages/jetstream ./modules/eventbus/... ./modules/hostagent/... ./modules/monitor/... ./modules/storage/...`、`./scripts/check-module-boundaries.sh`、`pnpm -C web build:prod`、`git diff --check`。

## 4. 不采用的方案

- 不让前端直接读取 SQLite 或 Storage。
- 不保留 HostMetric raw payload 的 Monitor SQLite inbox/history/latest 表。
- 不创建 Monitor SQLite host sample outbox。
- 不依赖 Storage `rows_updated` 作为唯一告警触发器，避免 Monitor 写入后再消费自己的消息形成环路。
- 不改变普通业务 Dataset retention、普通 Monitor Space 隔离或已有 Storage outbox。

## 5. 实施顺序

`Task 1 配置/metadata -> Task 2 row conversion -> Task 3 direct consumer/ACK -> Task 4 Storage query -> Task 5 删除 Host SQLite 样本路径 -> Task 6 host alert -> Task 7 Storage retention -> Task 8 UI/release -> Task 9 e2e`。

每个 Task 先写失败测试，再实现最小改动，运行 fresh `-count=1` 测试后独立提交。任何 Task 不得重新引入 Host Agent SQLite、Monitor host sample outbox、HTTP report 或每 Agent EventBus 用户。
