# 采集结果新鲜度与容量治理实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 防止 Collector 运行记录持续增长拖慢任务页面，消除任务结果列表对 Storage 的同步扇出，并让动态 K 线结果在采集、Primary、View、API 和监控中保持可验证的新鲜度；时序 View 默认保留最近 5,000 根 K 线，任一序列超过 6,000 根时触发安全重建，容量扫描改为每小时一次并随机错峰。

**Architecture:** Pebble Primary 继续作为在线事实数据权威，DuckDB View 作为可重建的查询投影。Collector 对终态运行明细实行有界保留，清理移出 Scheduler 前台路径并集中限速；任务列表直接返回本地轻量状态，只有用户查看的任务才刷新 Storage 结果元数据。View 沿用已有 A/B 构建与索引退役清理机制，只调整容量策略并补足端到端验收、动态结果新鲜度监控和发布门禁。

**Tech Stack:** Go、SQLite/GORM、Pebble、DuckDB、tRPC/Protobuf、Vue 3、Vitest、部署与契约测试脚本。

---

## 当前基线与执行约束

- 计划基于 `feature/mooyang` 的 `39cb7d3b` 编写；更新本计划时该分支工作区仍有大量未提交变更，其中 View 容量调度、Collector 维护 runner、period 清理、执行明细回收等已有部分实现。执行前必须逐文件审阅 `git status` 与 `git diff`，把已有修改视为用户工作；不得覆盖、回滚或假设它们已经提交。以下任务描述的是目标合同，现有未提交代码只有经过测试和独立审查后才能勾选完成。
- 按用户确认的新项目策略，不承担旧版数据兼容；可重新部署，但本计划执行时仍须保护正在执行、待重试或尚未获 Storage 明确终态确认的数据。计划编写阶段没有清空远端数据或部署服务；实现与发布必须分别记录代码验证和真实链路证据，任何手工数据清理仍要限定目标并先做 inventory/dry-run。
- View 的 A/B 重建、容量触发、索引退役清理以及 Collector period bitmap/Storage 状态链路已经有实现；本计划不是重写这些机制，而是调整默认容量并补足有证据的生命周期、性能和发布门禁。
- 当前 `maintenance_check_interval=1m` 驱动整个 View Maintainer，不只是容量查询。计划保留每分钟的轻量维护/修复循环，不把它整体放慢到 1 小时；每分钟循环只判断容量扫描是否到期，真正昂贵的序列行数统计按每个 Storage View 进程中的 `(View, active index)` 独立限频为每小时一次。随机偏移只用于容量扫描，不改变其它维护任务的频率。
- 本计划不把 Collector/Storage SQLite 或 Pebble 全库定期重建作为容量方案。删除 Collector 执行历史不删除 Primary K 线事实；View 重建也不删除 Primary 历史。

## 已确认决策与保留建议

1. 时序 View 每个 `(subject_id, frequency, series_tag)` 重建后保留最近 **5,000 根完整 K 线**。
2. 任一序列物理行数 **超过 6,000 根**（即至少 6,001 根）即可触发容量重建；1 GiB View 文件硬上限仍独立生效。
3. 用户查询的逻辑 View ID 不变。新索引先在 inactive slot 构建并追平，CAS 激活后旧索引按现有无引用清理机制删除；不允许先删旧 View 再建新 View。
4. 5,000/6,000 是全局时序 View 默认。`mooxsys` 和单 View override 必须解析为 `max_periods_per_series > rebuild_lookback_periods`；若不再需要旧的 1,000/2,000 显式覆盖，应更新或移除，避免遮蔽新默认。
5. **（2026-10-03 已确认）** 容量行数检查独立设置为每个 `(Storage View 进程, View, active index)` 每 **1 小时**至多一次；这里的“每小时”指昂贵的容量统计，不是把整个 View Maintainer 改为每小时运行。首次容量扫描不立即执行，而是在进程启动/active index 首次可见后均匀随机延迟 `[0, 1h)`；之后固定按最近一次扫描**开始**时间加 1 小时调度，不按整点对齐，也不在每次扫描后再额外叠加 jitter。重启或 active index 切换后重新抽取首次偏移。保持 `capacity_check_jitter="1h"`。同一维护 loop 内的 View 扫描串行，同一个 active index 不得并发扫描。失败/超时也占用本次扫描周期，只能在下一次到期扫描重试，不能被每分钟的通用维护循环立即重试。容量扫描的实际启动时刻可能比计划到期时刻晚最多一个 `maintenance_check_interval`（默认 1 分钟）；独立随机初始相位用于降低多个 View/Storage 进程同时扫描的概率，不承诺跨 Storage 副本全局去重。

   | 调度项 | 配置 | 作用 |
   |---|---|---|
   | 通用 View Maintainer | `maintenance_check_interval="1m"` | 继续每分钟处理轻量维护；未到容量扫描期限时只检查到期条件，不调用序列容量统计 RPC。 |
   | 容量统计 | `capacity_check_interval="1h"` | 对每个 active index 至多每小时执行一次昂贵的逐序列行数检查。 |
   | 首次错峰 | `capacity_check_jitter="1h"` | 每个进程/View/index 在首次可见时独立抽取 `[0, 1h)` 初始延迟；后续按固定一小时周期，不逐轮重新随机。 |

Collector 数据保留默认值定为：已被更新结果替代的终态 scheduled 执行明细保留 24 小时、scheduled Run 汇总保留 30 天、terminal retry 保留 7 天、period snapshot 保留 30 天；维护 worker 默认每分钟运行一次，timeout 45 秒，单轮总删除预算默认 50,000 行。预算包含 BatchItem 12,000、terminal Batch 500、terminal Retry 10,000、已替代 WriteTarget 12,000、per-Run TaskInstance 10,000、scheduled Run 汇总 4,000、terminal period snapshot 500、Timer manifest 1,000；按 Space 轮转公平分配，不能把每个 Space 都当成完整全局预算。期限、worker interval/timeout 和总预算都要进入 `[collector_retention]` 配置并有上下界校验；总预算最小 8 行（覆盖八类清理），最大 50,000 行。任何仍被活动 Batch、pending/dispatched Retry、未终态 Timer manifest 或 Storage waiting period 使用的记录不得因年龄到期而清除；对 K 线 WriteTarget，只有找到 Storage 明确的同 Dataset/frequency/period `complete` 或 `degraded` 状态才允许删除，缺少状态行和 `waiting` 均必须保留。若未来需要超过现有分项上限，必须同步提高对应 Repository 的硬上限和配置上限，不能只提高 `max_rows_per_pass`。

**重要数据模型约束：** market-fetch 的 `TaskInstance` 是一个 Collector Run 内的一次 Provider 请求执行，生产调度的 identity 包含 `run_id`；一个 instance 可由多个 CollectionTask 共享，具体任务/Dataset 输出关系由 `WriteTarget` 表示。因此它们不是稳定的任务定义或跨 Run 身份。24 小时回收只针对终态 scheduled Run 中已被同一启用任务、Dataset、subject、frequency 的更新 WriteTarget 替代的旧执行明细；保留每个启用目标的最新结果，保留活动 Run/Batch、Retry、Timer manifest 及所有 period 未获 Storage 明确终态确认的数据。`kline_resample`、manual/backfill 和无 Run 的维护身份不纳入这条回收规则。scheduled Run 汇总按独立的 30 天策略清理；任务定义与 Series/Dataset 配置不属于执行明细。禁止把“本地无 readiness/Storage 状态行”推断为 Storage 已终态，也禁止利用级联删除绕过上述保护。

## 文件责任图

### View 容量默认

- 修改 `moox.toml`、`moox.toml.example`：实际部署清单与新安装默认值。
- 修改 `modules/cli/internal/setup/config/config.go`、`config_test.go`、`modules/cli/README.md`：CLI 配置默认、优先级解析、范围校验与用户说明。
- 修改 `modules/storage/internal/config/loader.go` 及对应测试、`modules/storage/cmd/server/main.go`：Storage 直接启动的默认值与解析行为。
- 修改 `modules/storage/config/storage_view/maintenance.json`、`trpc_go.yaml`、`storage.yaml` 中的对应默认项：各启动 profile 保持一致。
- 修改 `modules/storage/internal/service/view/maintenance.go`、`maintenance_test.go`、`maintenance_capacity_cgo_test.go`：独立容量扫描周期、随机偏移、阈值、保留根数和 A/B 激活语义。

### Collector 生命周期与请求延迟

- 修改 `modules/collector/internal/store/run.go`，新增 `modules/collector/internal/store/run_retention.go`、`execution_retention.go` 和对应测试：scheduled Run 汇总与被新结果替代的 per-Run 执行明细安全保留。
- 修改 `modules/collector/internal/store/fetch_batch.go`、`fetch_retry.go`、`task_instance.go`、`write_target.go` 及对应测试：以事务、外键和活动引用为边界进行有界清理。
- 修改 `modules/collector/schema/collector.sql`、`modules/collector/internal/store/database.go` 及数据库测试：清理所需索引、迁移和空库初始化。
- 修改 `moox.toml`、`moox.toml.example`、`modules/cli/internal/setup/config/config.go`、`runtime_config.go` 及相邻测试：声明并渲染 Collector retention 策略；在生成的 Collector YAML 中使用 `collector_retention` 区块。
- 修改 `modules/collector/internal/bootstrap/config.go`、`config_test.go`、`modules/collector/internal/bootstrap/bootstrap.go`：解析 TTL 和 worker budget，并只启动一个维护 worker；先构造全部 runtime 的 Storage 状态客户端与 reconciler，再启动 runner，禁止 runner 并发读取仍在初始化写入的 runtime 字段。
- 修改 `modules/collector/internal/marketfetch/scheduler.go`、新增或扩展 Collector process-level maintenance runner，并在 `modules/collector/internal/bootstrap/bootstrap.go` 只注册一个维护 worker：Scheduler Tick 只做规划与 dispatch，不同步清理大表。
- 修改 `modules/collector/internal/rpc/service.go`、`modules/collector/internal/store/task_instance.go`、`modules/collector/internal/rpc/service_test.go`、`modules/collector/internal/store/task_instance_test.go`：任务列表不逐任务同步访问 Storage；任务实例过滤匹配准确 task ID 时使用等值查询及对应索引。
- 修改 `web/src/views/collector/task-results/index.vue` 和相关 API composable/test：列表先显示本地轻量摘要，选中任务再获取该任务当前结果；避免以 1,000 项结果页触发全任务 Storage 检查。

### Period 清理、可观测性与发布验证

- 修改 `modules/collector/internal/marketfetch/period_storage_reconciler.go` 及其测试：增加有界分页和受限并发的 Storage 状态探测，确认清理吞吐长期大于过期 period 产生速率，并且仅凭 Storage 明确的 complete/degraded 状态清理快照。
- 修改 `modules/collector/internal/marketfetch/metrics.go`、`metrics_runtime.go` 及 bootstrap wiring，覆盖数据库增长、清理积压、最老活动队列和最近成功时间；短时积压不得令 readiness 抖动。
- 修改 `modules/cli/internal/command/collector.go` 与对应测试、`scripts/test/contract/` 中 Storage Gateway/AccessProxy 合同脚本：发布 canary 必须检查 SCF 业务结果并验证目标 View 数据，不以进程存活或 HTTP 200 代替。
- 扩展 `modules/collector/internal/marketfetch/period_storage_rpc_e2e_test.go`、Storage Primary/DataNode/AccessProxy 测试与发布验收脚本：覆盖实际生产代理路径、Timer 和 Invoke period binding、Primary 写入及 View 可读结果。
- 修改 `modules/monitor/internal/metrics/kline_freshness.go`、`kline_freshness_test.go`、`modules/monitor/internal/config/config.go`、配置测试及 `modules/monitor/config/app.yaml`：动态 task-owned View 纳入新鲜度告警，不再只盯固定的 `view_binance_spot_kline_1m`。

## Task 1：统一 View 默认容量为 5,000/6,000

**Files:** `modules/storage/internal/service/view/{maintenance.go,maintenance_test.go,maintenance_capacity_cgo_test.go}`、`modules/storage/internal/config/loader.go` 及测试、`modules/storage/cmd/server/main.go`、`modules/storage/config/storage_view/{maintenance.json,trpc_go.yaml,storage.yaml}`、`modules/cli/internal/setup/config/{config.go,config_test.go}`、`moox.toml`、`moox.toml.example`。

- [ ] **Step 1：先增加配置合同测试。** 覆盖全局默认 `rebuild_lookback_periods=5000`、`max_periods_per_series=6000`、`max_view_file_bytes=1073741824`、`capacity_check_interval="1h"`、`capacity_check_jitter="1h"`；覆盖 `mooxsys` 与 `view_binance_spot_kline_1m` 的解析后值；覆盖非法的 `max <= lookback`、jitter 大于 interval、非正值和超过 24 小时的间隔仍被拒绝。
- [ ] **Step 2：运行配置测试确认失败。**

```bash
cd modules/cli
go test ./internal/setup/config -run 'Test.*StorageView|Test.*ViewPolicy' -count=1
cd ../storage
go test ./internal/config ./internal/service/view -run 'Test.*View.*(Policy|Capacity|Rebuild)' -count=1
```

- [ ] **Step 3：同步所有运行默认与 override。** 更新 CLI 默认、Storage loader/server 默认、`moox.toml`、示例和 Storage profile。所有生效配置保持 lookback 5,000、单序列阈值 6,000、容量扫描周期 1 小时、jitter 上限 1 小时；通用 `maintenance_check_interval` 仍为 1 分钟，不改 1 GiB 文件硬上限；删除或同步旧 1,000/2,000 覆盖，禁止只改示例而遗漏运行配置。
- [ ] **Step 4：锁定容量扫描和重建行为。** 注入时钟与 jitter source，验证启动时通用 View Maintainer 仍立即完成首轮维护、容量扫描按 `(进程, View, active index)` 的随机相位错开、每个 active index 每个 1 小时窗口最多执行一次 `SeriesCapacity`，失败/超时不会被每分钟的通用循环热重试而会在下一次到期扫描重试；同一 active index 的扫描不得并发，单个 maintainer loop 内的 View 扫描保持串行。用固定 jitter source 模拟多个 View 和多个 Storage View 实例，验证首轮扫描按各自相位落在 `[0, 1h)`，而非启动即同时扫描或统一对齐整点；这是降低集中请求概率的错峰，不承诺跨进程绝无时间碰撞。维护循环仍可每分钟执行其它必要检查，扫描启动比到期时刻晚最多 1 分钟。测试 6,000 根不因序列行数触发，6,001 根触发；重建后每个序列最多保留最新 5,000 根；文件大小超过 1 GiB 仍可独立触发；inactive 构建失败时 active slot 不变；激活后旧物理索引只有在无引用后才被现有 Cleanup Timer 回收。
- [ ] **Step 5：运行配置、Storage View 和 CLI 定向测试。**

```bash
cd modules/cli
go test ./internal/setup/config ./internal/command -run 'Test.*(StorageView|ViewPolicy|ViewCapacity)' -count=1
cd ../storage
go test ./internal/config ./internal/service/view ./internal/service/catalog ./cmd/server -count=1
```

- [ ] **Step 6：检查所有 profile 的最终有效值。** 新增配置合同测试解析 `moox.toml.example` 及所有 Storage profile；断言全局、系统监控、显式 View override 均满足严格不等式，并且用户目标 K 线 View 的有效值是 5,000/6,000；容量检查 1 小时及 jitter 生效，而其它 View Maintainer 检查仍为 1 分钟。

**验收：** 新建部署、CLI 安装和直接启动 Storage 得到同一默认策略；默认每个序列容量查询每小时至多执行一次且相位分散；单个序列超过 6,000 根时安全切换到保留最近 5,000 根的索引；Primary 历史不被删除。

## Task 2：给 Collector 执行历史设置安全、有界的生命周期

**Files:** `moox.toml`、`moox.toml.example`、`modules/cli/internal/setup/config/{config.go,runtime_config.go}` 及测试、`modules/collector/internal/bootstrap/{config.go,bootstrap.go}` 及测试、`modules/collector/internal/store/{run.go,run_retention.go,fetch_batch.go,fetch_retry.go,task_instance.go,write_target.go}` 及相邻测试、`modules/collector/schema/collector.sql`、`modules/collector/internal/store/database.go`。

- [ ] **Step 1：定义 retention 配置和保护规则测试。** 在 `[collector_retention]` 中设置 `maintenance_interval="1m"`、`maintenance_timeout="45s"`、`max_rows_per_pass=50000`、`execution_detail_retention="24h"`、`scheduled_run_summary_retention="720h"`、`terminal_retry_retention="168h"`、`period_snapshot_retention="720h"`。拒绝非正值、timeout 大于 interval、超过 365 天的期限和每轮删除上限小于 8 行或超过 50,000 行。fixture 覆盖 planned/active Run、活动 Batch、pending/dispatched Retry、未终态 Timer manifest、Storage waiting/状态缺失、已 complete/degraded period、被新结果替代的旧执行记录、每个启用目标的最新结果、disabled task、manual/backfill 与 `kline_resample`；确认活动工作和未获 Storage 明确终态确认的数据不被误删。
- [ ] **Step 2：先运行新测试确认清理 API 不存在或行为不符。**

```bash
cd modules/collector
go test ./internal/store -run 'Test.*(Retention|Cleanup|PreserveActive)' -count=1
```

- [ ] **Step 3：实现限量分批、space-scoped 清理。** 每轮全局总删除预算最多 50,000 行，短事务并按老记录优先分配：BatchItem 12,000、terminal Batch 500、terminal Retry 10,000、已替代的 WriteTarget 12,000、对应 per-Run TaskInstance 10,000、scheduled Run 汇总 4,000、terminal period snapshot 500、terminal Timer manifest 1,000；分项总和不得超过全局预算。清理超过 24 小时且已被更新结果替代的终态 scheduled 执行明细、30 天前的 terminal scheduled Run 汇总、7 天前且不再被活动工作引用的 terminal Retry、30 天前且 Storage 已确认终态的 period snapshot。仅当 K 线 period 有 Storage 明确的同键 `complete`/`degraded` 状态时，才能删除其 WriteTarget；状态缺失或 `waiting` 必须保留。最新目标结果、活动 Run/Batch、pending/dispatched Retry、未终态 Timer manifest、manual/backfill 与 `kline_resample` 不受此回收规则影响。WriteTarget 删除后，只能删除已无目标和其它活动引用的对应 per-Run TaskInstance；任务定义不属于这条 TTL。
- [ ] **Step 4：增加查询索引并验证初始化。** 对 space/status/terminal timestamp/primary key 清理条件新增可复用索引；新项目只要求当前 schema 可从空库正确初始化并可重复启动，不要求兼容历史 schema 或旧版 Collector 运行记录。测试 `EXPLAIN QUERY PLAN` 命中清理索引，避免全表排序或扫描。
- [ ] **Step 5：复用并协调现有 Batch/Retry 清理。** 将终态 Batch/BatchItem TTL 固定为 24 小时、terminal Retry TTL 固定为 7 天；测试证明清理不会删除每个启用目标的最新结果、Storage 状态缺失/等待的 K 线 WriteTarget 或 pending/dispatched Retry 依赖的目标。terminal retry 的 7 天诊断记录独立保留；只有符合 per-Run 明细回收条件且引用关系安全时，才回收对应实例/目标。
- [ ] **Step 6：测试幂等、故障恢复和并发。** 对清理前后重跑、删除中断、并发 completion、Batch 晚到回调、Retry 重新 dispatch 进行测试；清理失败不得推进水位或误删下一页数据。

**验收：** 数据量持续增长时可清理的终态 Run、Batch/BatchItem、Retry 与已被替代的 per-Run 执行明细保持在配置保留窗内；最新目标结果、活动/可恢复工作及 Storage 未明确终态确认的 K 线数据不被清理；每轮全局清理不超过 50,000 行且按 Space 公平分配，重复执行安全；scheduled Run 汇总与执行明细的保留策略彼此独立。

## Task 3：将维护清理移出 Scheduler 前台并确保 period backlog 可收敛

**Files:** `modules/collector/internal/marketfetch/scheduler.go` 及测试、process-level maintenance runner、`modules/collector/internal/bootstrap/bootstrap.go`、`modules/collector/internal/marketfetch/period_storage_reconciler.go` 及测试、相关配置。

- [ ] **Step 1：增加 Scheduler 不等待清理的回归测试。** 用阻塞 cleanup fake 证明 Tick 在其自身计划和 SCF dispatch 预算内返回；多个 Space 共用一个进程级维护队列，不能每个 Scheduler 独立启动相同的全库清理。
- [ ] **Step 2：把 Batch、Retry、Run 和执行明细维护接入唯一 worker。** worker 使用一个串行/coalescing 队列、45 秒超时和每轮 50,000 行全局预算；Scheduler Tick 只发起非阻塞唤醒，不在 `return` 前做清理；按 Space 轮转并分摊各类额度；SQLite 连接数不因并发 worker 增加。scheduled Run 仅清理 30 天前、终态且不再被最新目标执行或 Timer manifest 引用的汇总。所有 Storage status client/reconciler 必须在 `MaintenanceRunner.Start` 前构造完毕，启动后 runtime 字段只读。
- [ ] **Step 3：为 period snapshot 设置可证明的清理吞吐。** 保持先分页探测 Storage 状态、仅确认 complete/degraded 后回收的规则；按最老候选 cursor 轮转多个 Dataset/frequency，每 Space 每轮最多读取 500 个 snapshot 候选、探测 500 个 waiting state、删除 500 个已确认终态 snapshot；Storage RPC 全进程并发最多 10，单轮仍受 45 秒全局 timeout 限制。一个失败 probe 不得卡住整页或永久钉住 cursor。以每分钟新增 300 个过期候选的持续模拟验证 backlog 收敛，并记录 RPC 数、清理耗时和最老过期 snapshot 年龄；若实测生产新增速率超过该验收负载，必须提高受配置约束的预算后再发布。
- [ ] **Step 4：复核 Timer 与 Invoke 两条周期链。** 测试 Timer 请求带正确 period、series index/hash/count，Timer manifest 绑定并终态清理；Invoke 重试耗尽会经生产代理上报失败；Primary/DataNode 拒绝错误凭据或不匹配身份；late report 的终态响应不能被 Collector 当成成功确认。
- [ ] **Step 5：运行 Collector/Storage 定向集成测试。**

```bash
cd modules/collector
go test ./internal/marketfetch ./internal/store ./internal/bootstrap -run 'Test.*(Period|Timer|Cleanup|Maintenance|Retry)' -count=1
cd ../storage
go test ./internal/accessproxy ./internal/service/primarystore ./internal/service/datanode/... ./cmd/server -run 'Test.*(Period|Proxy|Failure|Auth)' -count=1
```

**验收：** cleanup/Storage probe 不再占用计划与 SCF dispatch 前台预算；period snapshot 清理有可验证的终态 Storage 依据和足够吞吐；Timer 与 Invoke 均不能绕过 period 状态契约。

## Task 4：去除任务列表的 Storage 扇出并修正任务实例过滤

**Files:** `modules/collector/internal/rpc/service.go` 及其测试、`modules/collector/internal/planner/taskresult/result.go` 及测试、`modules/collector/internal/store/task_instance.go` 及测试、相关 Vue task-results API/component/tests。

- [ ] **Step 1：给列表 RPC 增加零 Storage 调用测试。** 注入可计数 Metadata fake，`GetTaskList` 返回 1,000 个 task 时 `GetDataset/GetView` 调用数为 0；`GetTaskDetail` 或用户选中一个 task 时仍检查该任务的 Dataset/View 所有权和当前结果状态。
- [ ] **Step 2：运行测试确认当前列表扇出暴露问题。**

```bash
cd modules/collector
go test ./internal/rpc ./internal/planner/taskresult -run 'Test.*(TaskList|TaskDetail|ResultInspect|List.*Metadata)' -count=1
```

- [ ] **Step 3：将 task list 转为本地轻量读取。** `toPBTaskList` 只由 Collector 数据库中的任务配置、持久化 Result ID 和缓存/最近一次已确认状态组成，不调用 `InspectIDs`。保留单任务 detail/显式刷新路径上的 Storage 所有权检查；Storage 不可用时，列表照常返回并把状态表示为 stale/unknown，而不是整页超时或伪报 error。
- [ ] **Step 4：调整结果页按需加载与错误状态。** 任务列表和结果页初次响应使用有限分页（默认不超过 100 条）；只对当前选中任务请求一次 task detail/result metadata，再加载该任务 View 数据。Storage 暂时不可用时保留旧数据并标 stale/unknown；永久 ownership/contract 错误必须返回 error 和 last_error，不能作为 ready。刷新结果时保留已展示数据并显示更新时间，不并发请求所有任务的 View。
- [ ] **Step 5：让结果页深链接失败时不回退到其他任务。** `resultTask` 指向不存在、无权限或 detail 读取失败的任务时，结果页不得静默展示列表第一项任务的数据；显示该 task 的明确错误或修正 URL 到明确选中的任务。覆盖无效 query、任务被删除、ownership error、刷新后恢复及有效 query 直接打开的组件/router 测试。
- [ ] **Step 6：按完整 Task ID 使用等值过滤。** 将 `CollectionTaskID` 的 `LIKE '%id%'` 改为 `targets.c_task_id = ?`，复用 WriteTarget 上已有的唯一复合索引 `(c_space_id,c_instance_id,c_task_id)` 与 TaskInstance 的 `(c_space_id,c_id DESC)` 列表索引；先用 `EXPLAIN QUERY PLAN` 验证，不再叠加同列冗余索引。测试查询计划和跨 task ID 子串误匹配。`InstanceID`/函数名模糊搜索语义保持独立，不借用 TaskID 字段。
- [ ] **Step 7：运行 RPC、Store 和前端测试。**

```bash
cd modules/collector
go test ./internal/rpc ./internal/planner/taskresult ./internal/store -count=1
cd ../../web
pnpm test -- task-results task-instances
pnpm exec vue-tsc --noEmit
```

**验收：** task list 的 Storage RPC 扇出为零；selected task 的当前结果仍通过 Storage 验权并可显示真实 last_data_time；ownership/contract 错误、瞬态 Storage 错误和无效深链接状态明确且不会串显别的任务；实例页完整 task ID 过滤走索引且不做全表 COUNT。

## Task 5：让动态 K 线新鲜度、数据库压力和发布结果可见

**Files:** Collector/Storage 当前 metrics 实现、`modules/monitor/config/app.yaml` 和对应测试、`modules/cli/internal/command/collector.go` 及测试、`scripts/test/contract/`、period RPC E2E 测试。

> **当前安全状态（2026-10-03）：** 独立 period/Primary/View 正向证明合同尚未实现；CLI 当前必须对 manifest 与 ad-hoc 发布、Timer 与 Invoke 发布、空区域计划一律 fail closed，并在账号注册、上传包和节点/Timer 变更前退出。这只是防止无证明发布的临时安全门，不代表 canary 或 Task 5 已完成；Step 3 必须保持未勾选，直到可绑定同一目标 period/序列/View 并通过发布级验证。

- [ ] **Step 1：确定并测试关键运行指标。** 暴露 Collector DB/WAL bytes、Runs/Instances/WriteTargets/Batches/Retry/period snapshot 行数、每轮删除行数和耗时、最老 pending/dispatched Retry、最老 period waiting/snapshot 清理候选、Scheduler planning/dispatch 最近成功时间。标签只使用有限基数维度（Space、状态、频率），不把 subject/task ID 写成 Prometheus label。
- [ ] **Step 2：增加动态 task-owned View freshness 信号。** 先补齐并测试一个 authoritative、分页且有总量上限的 active task-result inventory 合同，至少返回经过 ownership 校验的 `space/task/dataset/view/frequency/market-calendar`、最近完成 period、View 最近数据时间及观测时间，并能表达 task 禁用/删除和结果身份变更；Monitor 当前静态配置和 Storage metrics 不足以推断这些身份，不得反向扫描无界 metrics 或把 task/subject ID 加为 Prometheus label。再复用 `moox_storage_view_dataset_output_last_data_time_seconds`，按该 inventory 汇总最近完成周期与最近 View 数据时间，提供 stale age，并在有界刷新 TTL 内处理禁用、删除和 View 变更。监控配置不再只绑定静态 Binance View 名称。短暂任务 backlog 不直接令服务 readiness 失败。
- [ ] **Step 3：加强发布 canary。** SCF canary 同时要求函数返回 `success=true`、目标 period 状态为 `complete`、Primary 存在目标 K 线行、View 对同一 subject/frequency 查询到新 period；`success=false` 即使 RPC transport 无 error 也算失败。必须在任何账号注册、SCF 包上传或节点/Timer 变更前确认目标周期/序列/View 身份已绑定且三路独立读校验可用；校验合同未实现时，所有公开发布入口（manifest/ad-hoc、Timer/Invoke、空区域计划）都必须在预检阶段拒绝，Timer-only 不得绕过验证要求；不能在部署 Invoke 节点后才报 canary 失败。`degraded` 只由独立故障场景测试验证，不作为正常发布 canary 通过条件。
- [ ] **Step 4：增加网关合同回归。** 从同一测试合同验证 AccessProxy、Admin Gateway 默认白名单包含 period Ensure/Commit/Failure/Status RPC；通过生产 `dataNodeProxyAdapter` 测试请求 node_id 被正确绑定和调用被转发，覆盖 read-only/invalid auth 拒绝写操作。
- [ ] **Step 5：运行 CLI 和合同脚本测试。**

```bash
cd modules/cli
go test ./internal/command -run 'Test.*(Collector|Canary|Storage.*RPC)' -count=1
cd ../..
scripts/test/contract/test-deploy-moox-control-profile.sh
```

**验收：** 运维能从指标区分“没有执行”“SCF 成功但没写 Primary”“Primary 有数据但 View 未追平”“结果 API 过期”；发布 canary 会因业务返回失败或目标 View 缺新 K 线而失败。

## Task 6：构建、独立审查与真实链路发布验收

**Files:** 本计划前述所有涉及文件；发布后将版本、时间、View 与数据时间记录到本计划末尾的验收记录。

- [ ] **Step 1：复查目标分支和工作区变更。** 在当前 `feature/mooyang` 工作区确认基线仍包含本计划所需代码；将此前未提交的用户修改逐项纳入或明确保持未触碰，禁止用清理/重置命令消除它们。当前工作区已有与本计划相关的未提交改动，实施时必须基于这些改动增量推进，不假设可用干净 worktree 代替。
- [ ] **Step 2：运行 Go/前端验证和静态检查。**

```bash
cd modules/collector
go test ./... -count=1
go build ./...
cd ../storage
go test ./... -count=1
go build ./...
CGO_ENABLED=1 go test ./internal/service/view -run 'Test.*(Capacity|Rebuild|Maintenance)' -count=1
cd ../cli
go test ./... -count=1
go build ./...
cd ../../web
pnpm test
pnpm exec vue-tsc --noEmit
pnpm run build:prod
cd ..
git diff --check
```

- [ ] **Step 3：执行独立 code review。** 使用 `codeCR` subAgent 审查本计划实际变更，重点检查 retention 是否误删活动关联、SQLite 锁/清理预算、View 新旧 slot 原子切换、结果列表刷新/错误呈现、Gateway allowlist 和 canary 是否检查业务数据。主 Agent 按文件/符号/行号核验每条发现；未关闭的 P1/P2 不得发布。
- [ ] **Step 4：部署前 dry-run。** 在目标环境确认 Collector DB、Storage Pebble、View active index、SCF 函数/触发器/节点数量和当前最新 K 线；清单写明本次要删的 Collector 终态记录行数和时间范围。保留 Primary 行；不重建全库，除非单独确认明确目标和恢复边界。
- [ ] **Step 5：按依赖顺序发布。** 先发布 Storage/AccessProxy 和配置，再发布 Collector/CLI/Web/Monitor；验证服务 readiness 后启用/恢复调度。发布成功只说明二进制已启动，不作为业务验收。
- [ ] **Step 6：用真实任务完成一轮 E2E。** 等待一个新目标周期；记录 Collector Run/SCF invocation/Batch/WriteTarget 状态和 period status；分别查询 Primary 与目标 View 的 subject、frequency、data_time；检查 Collector 结果 API 与页面显示相同的最近 K 线时间；确认下一根 K 线继续到达且 View 数据不回退。至少覆盖一个 Invoke 任务和一个 Timer-owned 任务。
- [ ] **Step 7：压测页面和保留策略。** 用生产量级 task/instance/page fixture 对任务、实例、结果列表记录 P50/P95 与 Storage RPC 次数；模拟连续调度 48 小时等效记录，验证 Collector 明细数在 TTL 窗口内有界、清理 backlog 归零或下降、活跃 retry/period 未丢失；对 View 单序列写入 6,001 根并确认最终保留最新 5,000 根。

**最终发布门禁：** 任一 Go/前端测试、独立审查 P1/P2、Storage period RPC 合同、SCF 业务 canary、Primary/View 最新 K 线对账、页面 P95 或数据有界性验证失败，不得宣称完成。

## 验收记录

- View 全局有效 lookback/trigger/file cap（实施时记录数值与配置来源）。
- 单序列 6,000/6,001 边界、A/B 激活、旧索引回收、Primary 历史保留（实施时记录测试结果）。
- Collector 明细/Run/Retry TTL、保留行数、清理耗时和活动引用保护（实施时记录测量结果）。
- Scheduler Tick P95 与 cleanup 独立 worker/backlog（实施时记录压测结果）。
- Period snapshot oldest age、清理吞吐与终态依据（实施时记录测量结果）。
- 1,000 task list 的 Storage RPC 次数及任务/实例/结果页 P50/P95（实施时记录基线和改后值）。
- Invoke/Timer 端到端 Run ID、period status、Primary latest data_time、View latest data_time、Task API/UI last_data_time（验收时逐项记录实测值）。
- 独立 codeCR 结论、修复项及复验（验收时记录审查结论和行号）。
- 生产发布时间、版本/SHA256、目标 View、发布前后数据时间与下一周期连续采集结果（发布时记录实测值）。
