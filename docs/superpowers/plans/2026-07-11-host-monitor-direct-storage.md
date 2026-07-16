# Host Monitor Direct Storage Implementation Plan

> **状态：已完成。** 本计划定义的 direct-storage 链路已经成为当前实现；稳定架构说明见 [主机监控架构设计](../../主机监控架构设计.md)。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将主机监控历史样本从 Monitor SQLite 迁移到 MooX Storage 时序数据，Monitor 只负责消费、实时快照、告警和 API，主机历史最多保留 3 天。

**Architecture:** Host Agent 继续通过 EventBus 发布 `HostMetric`。Monitor durable consumer 校验消息后直接写入四个 Host Dataset；Storage 写成功后才 ACK，失败允许有限重投后丢弃。Monitor 不再保存主机样本 inbox/history/latest 表，实时 latest 保留在内存，历史通过 Monitor RPC 查询 Storage。告警在 Monitor 消费链路中基于原始 HostMetric 计算，Storage 的 `rows_updated` 事件只作为其他消费者和后续恢复用途。

**Tech Stack:** Go 1.24、tRPC-Go、Protocol Buffers、NATS JetStream、MooX Storage Access RPC、Pebble PrimaryStore、Monitor SQLite（仅控制面）、Vue 3、Arco Design。

**Updated:** 2026-07-11。告警规则缓存使用现有 `github.com/mooyang-code/snapshotcache`；规则 CRUD 不主动刷新，统一由周期刷新发现变更。

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

- [x] 写失败测试：默认值为 `moox_system`、`1m`、72h、四个 Dataset；缺失 target/Dataset ID 失败；Host retention 不覆盖普通 Dataset。
- [x] 增加 Host Storage 配置并校验 1h 到 72h retention。
- [x] 四个 Dataset、columns 已固化到逻辑 metadata seed；active wildcard routes 单独放在 local-route seed，避免集群部署误写 `node_id=local`。
- [x] EventBus durable `MaxDeliver=3` 与 Monitor consumer 一致；配置和单元测试通过。
- [x] Direct-storage contract 已提交并纳入 release/deploy 校验。

### Task 2: 实现 HostSnapshot 到 Storage rows 的转换

**Files:** `modules/monitor/internal/hostmetrics/storage_writer.go`、`storage_writer_test.go`、`hostmetrics.go`。

- [x] minute bucket、四类 row、维度、rate unavailable、确定性排序已由 Writer/E2E 测试覆盖。
- [x] `StorageWriter.WriteSnapshot` 按 Dataset 分组调用 Storage Access，使用幂等时序 key。
- [x] 每个 row 使用固定 space/subject/frequency/bucket，attributes 写入 `message_id` 和 `agent_id`。
- [x] TypedValue 和 `rate_available=false` 规则已由 Writer 测试覆盖。
- [x] `go test -count=1 ./modules/monitor/internal/hostmetrics -run TestHostStorageWriter` 已通过。

### Task 3: 改造 Monitor consumer 为 direct write + ACK

**Files:** `modules/monitor/internal/hostmetrics/hostmetrics.go`、`modules/monitor/internal/bootstrap/bootstrap.go`、`modules/eventbus/config/app.yaml`、`consumer_test.go`。

- [x] fake writer/Store 测试覆盖校验失败、Storage 失败和 latest 更新顺序；Consumer 对 Storage 暂时错误 NAK、毒消息 DLQ/Term。
- [x] Store 改为内存 registry + StorageWriter，不再读写 Host SQLite 样本表。
- [x] Bootstrap 注入 Storage Access/Metadata client，并周期执行只读 StorageGate 校验四个 Dataset 和 active wildcard route。
- [x] Consumer 固定 `MaxDeliver=3`，顺序为 `Validate -> WriteSnapshot -> registry.Update -> AlertEvaluate -> Ack`；Storage gate 失败时不 fetch。
- [x] Monitor hostmetrics/bootstrap race tests 已通过。

### Task 4: 从 Storage 实现实时/历史查询

**Files:** `storage_reader.go`、`storage_reader_test.go`、`modules/monitor/internal/rpc/host.go`、`modules/monitor/proto/monitor.proto` 和 generated files。

- [x] fake Access E2E 覆盖四 Dataset 扫描、时间范围和合并；Reader 从 key dimensions 重建实体。
- [x] Reader 按 `subject_id=agent_id` 做分页扫描四个 Dataset，底层 PrimaryStore 使用 subject/freq key-prefix，按 agent/dimension/time 归并，避免 dataset-wide 10,000 行保护阈值。
- [x] `ListHostAgents` 读取内存 latest；历史查询默认 1h、最大 3d/500 点，并返回 `storage_available/data_gap`。
- [x] Monitor proto 已重新生成，Reader/RPC race tests 已通过。

### Task 5: 移除 HostMetric SQLite 样本依赖

**Files:** `modules/monitor/schema/monitor.sql`、HostMetric bootstrap/RPC、`bootstrap_test.go`。

- [x] schema 测试断言新 DB 不创建 Host sample/inbox/history/outbox 表，普通 checks/alert tables 仍存在。
- [x] 旧 DB 中的 Host sample 表不自动 DROP；新增显式 `monitor-cli cleanup-host-sample-tables --confirm`。
- [x] Bootstrap 不再初始化 Host sample schema；仅保留通用 schema、Storage gate、consumer、内存 latest 和告警 worker。
- [x] `go test -count=1 ./modules/monitor/...` 已通过。

### Task 6: 抽象 DB 快照缓存并实现 Host 告警

**Files:** `modules/monitor/internal/hostmetrics/alerts.go`、`alerts_test.go`、`rule_cache.go`、`rule_cache_test.go`、existing alert repository、Host RPC/proto、`modules/monitor/go.mod`、`modules/monitor/go.sum`；复用外部模块 `github.com/mooyang-code/snapshotcache`。

- [x] Host rule 固定 `moox_system`，key 为 `host:<agent_id>:<metric>`；支持 CPU、memory、filesystem、disk、network，rate unavailable 不触发。
- [x] 已完成旧 dbcache 评估，运行时使用 snapshotcache，不增加旧兼容 API。
- [x] 复用已有 `github.com/mooyang-code/snapshotcache`，提供泛型快照、索引、原子发布、Start/Stop 和刷新失败保留旧快照。
- [x] `RuleCache` 在 Bootstrap 启动并仅由周期 source 刷新；消费路径只读缓存，规则 CRUD 不主动失效。
- [x] snapshotcache 以 30s 周期刷新；失败保留旧快照，CRUD 不主动刷新。
- [x] 首次无快照时只跳过告警，不阻塞 Storage/ACK；去重缓存有上限。
- [x] RuleCache、告警聚合、rate unavailable、消息去重和刷新失败测试已覆盖。
- [x] Storage 成功后从缓存读取规则执行 Host evaluator，firing/resolved 写现有 Monitor SQLite alert state/event；告警错误不回滚 Storage/ACK。
- [x] Host rule create/list/update 对 host key 固定 `moox_system`，普通业务规则保持 space 隔离。
- [x] Hostmetrics/repository/rpc race tests 已通过。

### Task 7: Storage raw time-series 3 天 retention

**Files:** Storage access proto/generated、Access service、PrimaryStore client/local/service、device store/Pebble、Storage config、delete/retention tests。

- [x] Delete RPC 校验 space/dataset/time range、time-series 类型、1000 行上限，并使用严格 `< now-72h` 截止点。
- [x] 增加受限 `DeleteTimeSeriesRows` RPC，按 route 扫描并限制单批 1000 行，不发布 rows-updated 事件。
- [x] PrimaryStore/Pebble 支持完整 PrimaryStoreKey 批量删除和 context cancel。
- [x] Storage access role 每小时维护四个 Host Dataset，每轮每个 Dataset 最多 1000 行，失败下轮重试；PrimaryStore 支持远程删除 RPC。
- [x] Storage access/primary/Pebble retention tests 已通过；Monitor 不再启动重复 retention worker，Storage maintenance 配置是唯一保留策略来源。

### Task 8: 前端、发布和文档迁移

**Files:** Host monitor API/page、release/deploy scripts、deployment test、monitoring docs、Host Agent skill reference。

- [x] 前端对 missing/rate unavailable 显示 `--`，RPC storage unavailable/data gap 状态被透传。
- [x] 页面历史选择最大 3d；实时卡片和历史均通过 Monitor API，RPC 增加 `storage_available/data_gap` 状态。
- [x] release/deploy 校验四个 Host Dataset、Host storage 72h retention 和 route seed，不初始化 Host SQLite sample schema。
- [x] EventBus deploy contract、前端 production build、Monitor/Storage 全量测试已通过。

### Task 9: 端到端验收

**Files:** `modules/monitor/internal/hostmetrics/direct_storage_e2e_test.go`、`scripts/test-deploy-moox-eventbus.sh`。

- [x] 模块根 `modules/monitor/test` 增加 direct-storage contract E2E：发送 snapshot，断言四 Dataset 行、历史合并和 72h retention；Storage Access/Primary service tests 覆盖真实删除边界。
- [x] E2E 验证重复同一分钟写入幂等，不同分钟产生新历史点。
- [x] Storage writer 失败路径验证不更新 latest，Consumer 对 Storage 未就绪不 fetch 并 NAK。
- [x] E2E 验证四 Dataset retention 删除旧行；Storage maintenance 使用受限删除 RPC 和配置执行。
- [x] 前端 production build 验证 realtime/history 状态渲染；RPC contract 覆盖 Storage unavailable/data gap。
- [x] 最终 race tests、模块边界、前端 build 和 `git diff --check` 已执行。

## 4. 不采用的方案

- 不让前端直接读取 SQLite 或 Storage。
- 不保留 HostMetric raw payload 的 Monitor SQLite inbox/history/latest 表。
- 不创建 Monitor SQLite host sample outbox。
- 不依赖 Storage `rows_updated` 作为唯一告警触发器，避免 Monitor 写入后再消费自己的消息形成环路。
- 不改变普通业务 Dataset retention、普通 Monitor Space 隔离或已有 Storage outbox。

## 5. 实施顺序

`Task 1 配置/metadata -> Task 2 row conversion -> Task 3 direct consumer/ACK -> Task 4 Storage query -> Task 5 删除 Host SQLite 样本路径 -> Task 6 host alert -> Task 7 Storage retention -> Task 8 UI/release -> Task 9 e2e`。

每个 Task 先写失败测试，再实现最小改动，运行 fresh `-count=1` 测试后独立提交。任何 Task 不得重新引入 Host Agent SQLite、Monitor host sample outbox、HTTP report 或每 Agent EventBus 用户。
