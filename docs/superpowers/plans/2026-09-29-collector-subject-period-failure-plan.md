# Collector 标签标的与周期降级优化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal：** 让 `moox-collector-subject` 成为外部标的列表的唯一刷新者；Collector 只读取 Storage 标签成员生成采集任务；SCF 标的请求在初次请求及最多 3 次重试后终态失败，但不提前结束周期，周期到 deadline 后发布 `degraded` 事件并列出失败标的。

**Architecture：** 标签成员以 Storage 中最近一次成功的 subject snapshot 为准，Collector 不调用行情源标的列表接口。Collector 按数据集、频率和周期持久化不可变标的快照；同一周期后续调度始终使用该快照，下一周期才采用最新标签成员。Storage 保留 period expectation 和 deadline finalizer；Collector 将重试耗尽的标的失败记录到该周期，Storage 到期时发布包含 `universe_subject_ids`、`failed_subjects` 的 `degraded` marker。

**Tech Stack：** Go 多模块、tRPC-Go、Protobuf、Collector SQLite（GORM/SQLite）、Storage DataNode Pebble、SCF Invoke。

---

## 设计约定

- `moox-collector-subject` 是外部标的列表的唯一刷新进程；标签快照成功才替换成员，失败时保留上一份成员快照。
- Collector 的 `ResolveSubjects` 是 Storage 元数据读取，不是行情源列表刷新。保留该读取能力，但只在创建一个新周期快照时解析标签成员；同周期不再次解析并改写成员集合。
- `t_collector_task_series` 可继续表示任务当前成员集合和当前 hash，但不能再作为已开始周期的动态成员真相。增加按 `(space_id, dataset_id, frequency, period_time)` 固定的 period series 快照，供同周期的所有 Run、重试及迟到回调复用。
- 初次请求加最多 3 次重试后，失败标记只影响该标的及其所属周期，不修改 `t_tags` / `t_subject_tags`，不阻断其它标的调度，也不提前发布周期事件。下一周期是否采集该标的由届时的 Storage 标签快照决定。
- 周期到 deadline 时，如果仍有未成功标的则发布一次 `degraded`；`failed_subjects` 记录重试耗尽的标的，未完成但没有明确 SCF 终态错误的标的仍反映在 Universe 与完成状态中。若所有标的在 deadline 前成功，则按现有语义发布 `complete`。
- 本计划不部署线上服务、不重置线上数据；上线另行安排。

## 当前代码边界

| 路径 | 作用 | 计划变更 |
| --- | --- | --- |
| `modules/collector/cmd/subject/main.go` | 启动 subject-sync 独立进程 | 保留为唯一外部标的快照拉取入口；检查并删除仍有调用方的旧 instrument 列表采集路径 |
| `modules/collector/internal/subjectsync/tag_runner.go` | 按标签 cron 拉取 source snapshot 并调用 `ApplyTagSnapshot` | 保留快照成功/失败语义；仅在验收发现重复列表请求时调整 |
| `modules/collector/internal/marketfetch/scheduler.go` | 展开标签成员、创建实例和 write target、初始化 Storage period | 新周期创建 roster snapshot；已有周期加载快照；耗尽重试时上报该周期标的失败 |
| `modules/collector/internal/domain/task_series.go`、`modules/collector/internal/store/task_series.go` | 规范化当前任务成员与索引/hash | 保留当前任务集合能力；新增 period roster 模型和仓储，不用动态 task series 覆盖进行中的周期 |
| `modules/collector/schema/collector.sql`、`modules/collector/internal/store/database.go` | Collector SQLite Schema、启动校验 | 新增 period roster 表、索引、启动 Schema 检查和清理入口 |
| `modules/collector/internal/marketstorage/storage.go` | Collector 到 Storage 的周期写入适配 | 增加幂等的周期失败标记调用 |
| `modules/storage/proto/data_node.proto`、`modules/storage/proto/primary_store.proto` | DataNode / Primary period RPC | expectation 携带有序 universe 标的；新增周期标的失败记录 RPC；重新生成 Go protobuf |
| `modules/storage/internal/service/primarystore/period.go`、`modules/storage/internal/service/datanode/period.go` | period RPC 路由与鉴权 | 校验周期身份并路由失败标记请求 |
| `modules/storage/internal/service/datanode/pebble/period_progress.go` | period expectation、bitmap、deadline finalizer | 固化 universe 与失败标的；deadline marker 带 `universe_subject_ids` / `failed_subjects`；迟到成功清除对应失败标记 |

## 实施任务

### Task 1：锁定周期成员快照契约

**Files：**
- Modify: `modules/collector/internal/marketfetch/scheduler_test.go`
- Modify: `modules/collector/internal/store/task_series_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/period_progress_test.go`

- [x] 增加验证场景：Storage 标签成员在周期中从 `{BTC, ETH}` 变为 `{BTC}` 后，已存在的该周期 roster、series hash、index 和 expected count 不变。
- [x] 增加验证场景：下一个周期首次创建 roster 时读取更新后的 `{BTC}` 集合。
- [x] 增加验证场景：同一周期并发创建只保留一个 roster；重复创建相同 roster 幂等；不同 roster 不得静默覆盖已创建周期。
- [x] 增加验证场景：SCF 重试耗尽的标的被记录为失败，但 period 状态在 deadline 前仍是 `waiting`；到 deadline 发出且仅发出一次 `degraded` marker。
- [x] 增加验证场景：失败标的在 deadline 前迟到成功后，从失败集合清除；若所有 series 成功，周期以 `complete` 结束。

### Task 2：持久化 Collector 周期 roster

**Files：**
- Create: `modules/collector/internal/domain/task_period_series.go`
- Create: `modules/collector/internal/store/task_period_series.go`
- Modify: `modules/collector/schema/collector.sql`
- Inspect: `modules/collector/schema/schema.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/store/database.go`
- Modify: `modules/collector/internal/store/database_test.go`

- [x] 新增 period series row，字段至少包括 `space_id`、`dataset_id`、`frequency`、`period_time`、`series_index`、`series_key`、`subject_id`、`provider`、`source_id`、`market_type`、`provider_symbol`、`series_tag`、`series_hash`、`expected_count` 和创建时间。
- [x] 为 `(space_id, dataset_id, frequency, period_time, series_index)` 建唯一约束，并为同一 period 的 `series_key` 建唯一约束；确保 index 从 0 开始连续，hash 由规范排序后的完整 series key 集合计算。
- [x] 在仓储中实现 `GetPeriodSeries`、`CreatePeriodSeriesIfAbsent` 和有界过期清理。并发首建采用事务及唯一约束仲裁；已有周期只返回已保存 roster，不覆盖它。
- [x] 将新表加入 Collector Schema 加载、完整性校验和测试数据库列表。清理只删除已越过保留期的已终态周期，保留时间覆盖 SCF 回调、重试和 period 上报窗口。

### Task 3：调度只消费标签快照并复用周期 roster

**Files：**
- Modify: `modules/collector/internal/marketfetch/scheduler.go`
- Inspect: `modules/collector/internal/planner/storagesource/source.go`
- Modify: `modules/collector/internal/marketfetch/scheduler_test.go`

- [x] 保留 `DatasetSource.ResolveSubjects` 对 Storage 的只读请求；确认 Scheduler 和 SCF worker 没有调用 `SubjectLister` / 外部行情源标的列表 API。
- [x] 在每个 Dataset/frequency/period 首次 planning 时，从 Storage 解析标签成员、展开 Provider symbol、规范化并创建 period roster，然后用该 roster 生成该周期的实例、write target、`series_index`、`series_hash` 和 `expected_count`。
- [x] 同周期后续 planning、retry 和重启恢复必须读取 period roster；不得再次用当前标签成员替换 roster，也不得因为 subject-sync 刷新造成同周期 expectation conflict。
- [x] 新周期重新读取 Storage 当前 active tag members，使成功的 subject snapshot 变更从下一周期生效。
- [x] 任务没有可采集成员或解析失败时，按现有任务错误路径记录原因，不创建伪造的空 roster；该错误不能中断同一调度轮次中的其它任务。

### Task 4：将重试耗尽记录为周期失败，不提前 finalize

**Files：**
- Modify: `modules/collector/internal/marketfetch/scheduler.go`
- Modify: `modules/collector/internal/marketstorage/storage.go`
- Inspect: `modules/collector/internal/domain/fetch_batch.go`
- Modify: `modules/collector/internal/marketfetch/scheduler_test.go`

- [x] 明确重试次数语义为初始请求后最多再重试 3 次；重试计数达到上限后，将 instance/retry 标记为 `permanent_failed`，保存最后错误类型和摘要，不再重新排队。
- [x] 将终态失败关联到其固定的 dataset/frequency/period 和 subject ID，并通过 Storage 适配器提交幂等失败标记；相同 subject 重复上报不重复计数。
- [x] 失败标记不得调用 Ensure 时改写 expected count，不得设置成功 bitmap，不得触发立即 finalize，也不得改动标签成员；其它待执行标的继续 dispatch。
- [x] 记录 late callback 的收敛规则：period 尚在 waiting 且写入成功时成功 bitmap 生效并撤销该 subject 的失败标记；period 已终结时遵循 Storage 既有终态规则，不重发或改写 marker。

### Task 5：Storage 保存 period universe / 失败标的并到期发布

**Files：**
- Modify: `modules/storage/proto/data_node.proto`
- Modify: `modules/storage/proto/primary_store.proto`
- Regenerate: `modules/storage/proto/storagegen/`
- Modify: `modules/storage/internal/service/primarystore/period.go`
- Modify: `modules/storage/internal/service/datanode/period.go`
- Modify: `modules/storage/internal/service/datanode/pebble/period_progress.go`
- Modify: `modules/storage/internal/service/datanode/pebble/period_progress_test.go`

- [x] `DatasetPeriodExpectation` 接收按 `series_index` 排序的 roster（每项包含 `series_index` 与 `subject_id`）；校验 roster 项数等于 `expected_count`、索引连续、subject ID 非空。marker 的 `universe_subject_ids` 按 Subject 去重，因为一个 Subject 可对应多个 Provider series。重复 Ensure 必须同时匹配已保存 roster，重试不得用不同 roster 覆盖已初始化周期；SCF commit 仍使用现有轻量 identity（不重复携带整份 roster）。
- [x] 新增 DataNode / Primary 的 `RecordDatasetPeriodFailures` 请求；请求携带同一 expectation identity 和失败 `series_index`。Storage 只接受 roster 中存在且仍处于 waiting 的 series，重复请求幂等，并将失败 index 持久化在 period state 中。
- [x] 成功 commit 对应的 `series_index` 时，清除该 index 的失败状态并推进既有 bitmap；marker 汇总 `failed_subjects` 时将未成功的失败 index 映射到 Subject ID 后去重；不得因失败记录让 period 提前终结。
- [x] deadline finalizer 继续以 bitmap 完整性决定 `complete` / `degraded`：未完成的 period 到 deadline 进入 `degraded`，marker 带完整 universe 和显式失败 subject IDs；只有所有 bit 成功时进入 `complete`。
- [x] 确保 marker/outbox 的 event ID 与 payload 幂等；同一 period 的重试不能发布冲突 payload 或重复事件。

### Task 6：确认唯一标的列表刷新链路与验收

**Files：**
- Inspect: `modules/collector/cmd/subject/main.go`
- Inspect: `modules/collector/internal/subjectsync/service.go`
- Inspect: `modules/collector/internal/subjectsync/tag_runner.go`
- Inspect: `config/setup/metadata.yaml`
- Modify only if a remaining legacy list refresh is found: related seed/config and its obsolete collector listing code

- [x] 用全仓搜索确认行情源 instrument listing fetch 只由 `moox-collector-subject` 调用；Collector Scheduler 对标签的读取只到 Storage Metadata 的 `ResolveSubjects`。
- [x] 若仍有旧 instrument 类型实时采集任务或另一处列表抓取入口，删除其默认任务/触发配置和无调用方代码；不得删除 K 线采集任务或 `ResolveSubjects` 只读接口。
- [x] 执行针对性 Go 测试：`go test ./modules/collector/internal/store ./modules/collector/internal/marketfetch ./modules/storage/internal/service/datanode/pebble ./modules/storage/internal/service/primarystore`。
- [x] 执行构建验证：`go build ./modules/collector/cmd/subject ./modules/collector/cmd/server ./modules/storage/cmd/server`。
- [x] 做本地端到端验证：先以 `{BTC, ETH}` 创建 1m 周期 roster；刷新 Storage 标签为 `{BTC}`；同一 1m 周期仍沿用 `{BTC, ETH}`；下一周期只含 `{BTC}`；让 ETH 的 SCF 请求耗尽 3 次重试，确认其它标的正常写入、周期在 deadline 前仍 waiting、deadline 后只有一条 `degraded` marker 且 `failed_subjects` 包含 ETH。
- [x] 执行 `git diff --check`，并检查 `rg -n "FetchSnapshot|NewSubjectListers|ResolveSubjects" modules/collector/internal` 的调用边界，确认源列表抓取未泄漏到 Scheduler。

## 发布顺序

1. 先发布 Storage Proto / Primary / DataNode，确认新增 period failure RPC 可用。
2. 再发布 Collector：新版本只在新周期创建 roster；对尚未终结的旧周期继续按原 Storage expectation 和原 retry state 收尾，不重写旧周期身份。
3. 确认 `moox-collector-subject` 是唯一启用的外部列表刷新服务；标签刷新失败时保留现有 active roster。
4. 观察至少覆盖一个 1m 和一个 1h 周期的完成事件：成功周期为 `complete`；有终态 SCF 标的失败的周期在 deadline 后为 `degraded`，且失败标的可见；同周期成员 hash 不再变化。

## 验收标准

- 外部行情源的标的列表刷新只由 `moox-collector-subject` 发起；Collector 只读 Storage 标签成员。
- 标签成员快照变化不修改已开始周期的 universe、series hash、index 或 expected count；只影响随后新建的周期。
- SCF 标的请求最多重试 3 次后终态失败，失败只记在对应 subject/period；不阻断其它标的，不修改标签成员，不提前发布 period event。
- 未完成周期严格等到 deadline 后发布一条 `degraded` marker，且 marker 能指出重试耗尽的 subject；所有预期标的成功时为 `complete`。
- Collector 重启、重复调度、Storage RPC 重试和 SCF 迟到成功不会重建或改写已固定的周期 roster，也不会产生重复/冲突的 period marker。
