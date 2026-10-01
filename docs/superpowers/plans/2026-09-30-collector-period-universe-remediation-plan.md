# Collector 周期标的池快照与降级链路补齐执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal：** 统一周期标的池相关术语，补齐真实 Primary/Gateway、StockCN Timer、失败回执与终态清理链路，使不可变周期快照、最多三次重试和硬 deadline 降级在生产调用路径中成立。

**Architecture：** Storage 是周期成功 bitmap、失败 bitmap、deadline 和终态 marker 的唯一权威。Collector 保存不可变 `PeriodSeriesSnapshot`、持久批次和逐目标失败回执；StockCN 保留 Timer 触发，但通过独立 Collector runtime RPC 领取持久批次，并沿用 EventBus Completion 和 Invoke 重试。所有终态清理必须有匹配的 Storage 终态证据，不通过本地时间或缺少 readiness 推断。

**Tech Stack：** Go workspace / 多模块、tRPC-Go、Protobuf、Collector SQLite/GORM、Storage Pebble、Node Service Gateway、NATS JetStream、Tencent SCF Timer/Invoke。

---

## 1. 范围与基线

工作目录为仓库根目录 `moox`；下文命令均在该目录执行，文件清单使用仓库相对路径。业务代码审计基线为 `593a51cab886ef6f9362765ac7730470cd0f29d2`；本轮复核开始时主工作区为 `feature/mooyang`，HEAD 为 `06972d14`，工作区干净。业务基线之后的三次已提交变更 `80291a16`、`e8c5b4d2`、`06972d14` 均只修改计划文档，未改变该业务实现。

**本轮交付边界：** 只完善执行计划与审查依据，不继续业务编码、不部署、不重置。独立工作区中已有部分实现，Task 1 与门禁前置改动已有分项独立审查结论，但未提交、未合入，也未完成整条链路的审查及验收，不视为主工作区已完成。实施前先核验实际 diff 和验证证据，禁止依据改名后的文件存在或子任务的本地测试回报就勾选整项任务。

**实施授权更新（2026-09-30）：** 用户随后明确要求按本计划编码、编码完成后新起 Agent 审查、编译发布正式环境并做线上端到端验证。因此最终交付包括正式发布及真实 1m/1h 周期验证，不以本地门禁或运行手册代替。上述“只写计划”仅描述文档编写阶段；线上数据清理/重建仍须列出精确范围、备份并另行取得确认，不因发布授权自动执行。

本计划补充 `docs/superpowers/plans/2026-09-29-collector-subject-period-failure-plan.md`。旧文档保留为历史记录；其中已勾选的任务不能作为本次补齐项已完成的证据，旧“Storage 先升级、旧周期继续收尾”的发布假设由本计划替代。

已有周期快照表、canonical series hash、dense index、并发首建、Invoke 重试、Pebble 失败 bitmap 和 marker/outbox 主体逻辑予以复用，不重新实现整套状态机。

| 已确认事项 | 当前证据位置与符号 | 对应任务 |
| --- | --- | --- |
| 完整快照使用 roster，容易与量化标的池及单条 series 混淆 | `modules/collector/internal/domain/task_period_series.go:5`；Storage `DatasetPeriodExpectation.roster` | Task 1 |
| 生产 DataNode adapter 未实现失败 RPC，影响三个 period RPC 的接口断言 | `modules/storage/cmd/server/main.go:1011` 的 `dataNodeProxyAdapter`；`primarystore/period.go:12` 的 `periodDataNodeClient` | Task 3 |
| Gateway 部署默认值漏掉失败 RPC | `config/setup/service-deployments.yaml:117`；`modules/admin/internal/service/sysdeploy/defaults.go` 的 `storagePrimaryGatewayMethods` | Task 3 |
| Ensure 缺少写权限及 Dataset 所有权保护 | `modules/storage/internal/service/primarystore/period.go:18` 的 `EnsureDatasetPeriod` | Task 3 |
| 失败 RPC 缺硬 deadline fence，终态 no-op 被误判为上报成功 | `period_progress.go:288` 的 `RecordDatasetPeriodFailures`；Collector `storage.go:136`、`scheduler.go:1361` | Task 2、5 |
| Commit 的终态 no-op 同样返回 nil，worker 不能据此宣称首次写入成功 | `period_progress.go:211` 的终态/截止分支；DataNode `period.go` 返回请求 keys；Collector writer 丢弃结果 | Task 2、4、7 |
| 快照清理把无 readiness 记录当作可删除条件 | `modules/collector/internal/store/task_period_series.go:119`；启动清理早于 Storage 依赖初始化 | Task 4 |
| StockCN Timer 未创建持久批次、缺 period identity、不发布 Completion | `scheduler.go:313`、`timer.go:83`、`handler.go:88` | Task 6、7 |
| StockCN Storage 包装器未转发 period 接口 | `modules/collector/internal/marketfetch/handler.go:313` 的 `reservedDeadlineStorage` | Task 7 |
| 非交易时段返回零写入 success，且无 Invoke 节点时跳过超时恢复 | `kline_pipeline.go:93`；`scheduler.go:1156` 的 `recoverDue` | Task 6、7 |
| 旧协议、旧 Pebble expectation 与新快照不具备无缝升级条件 | 旧计划“发布顺序”；当前 Ensure 快照校验 | Task 9 |
| 原验收没有证明实际 resolver + Gateway + Timer 全链路 | 当前 Primary 测试直接注入 DataNode Service；Timer 测试采用无 Completion 语义 | Task 8 |
| 两个 SCF 打包入口仍将 Storage HMAC 写入 ZIP，不能只检查环境注入 | `scripts/build/build-collector-scf-package.sh:55` 的 `render_storage_auth`；`modules/cli/internal/collectorpackager/scf.go:89` | Task 7 |
| period index 只绑定 Subject，不能区别同 Subject 的两个 storage series | `modules/storage/proto/data_node.proto:13`；`period_progress.go:620` 的 `validatePeriodRow` | Task 2、8 |
| 通用 write-owner helper 实际只保护 Factor，不能代替 Collector 周期授权 | `modules/storage/internal/service/primarystore/service.go:249` 的 `validateDatasetWriteOwner` | Task 3 |
| 已有 Admin deployment 的 operator-owned route 不自动合并新默认路由 | `modules/admin/internal/service/sysdeploy/dao.go:282` 的 `mergeDefaultGatewayRoutes` | Task 9 |

### 1.1 原审查建议的处理决定

| 审查建议 | 核验结论与本次处理 |
| --- | --- |
| 生产 Primary 代理缺失败 RPC | 成立；补转发、生成接口编译断言，以及使用实际 resolver 的测试，不再只注入 DataNode Service |
| StockCN Timer 绕过周期状态机 | 成立；保留 Timer 触发，改为领取持久批次，接入 period Commit、Completion 和最多 3 次 Invoke 重试 |
| Ensure 缺写权限保护 | 成立；初始化不可变快照属于写操作。统一周期 RPC 的 Collector 身份与 Dataset owner 校验，不能仅复用实际只保护 Factor 的 helper |
| 旧 waiting period 与新快照冲突 | 冲突风险成立，但不按旧计划增加历史兼容；依仓库原则采用受控维护窗口、收尾和另行授权的运行态重建 |
| 截止后失败上报可能漏进 marker | 成立；不能修改终态 marker 补救，新增逐 index 权威回执，区分已收录、已成功和未赶上截止，传输未确认保持 pending |
| 同 Dataset 被多任务按不同标签采集 | 不实现动态合并；当前 RPC 创建任务专属结果 Dataset，更新禁止更换结果身份。补 API 和 Store 回归断言，发现绕过约束的输入则拒绝，不复用首个任务的快照 |
| 清理缺 Storage 终态依据 | 成立；新增只读查询与权威状态记录，未知、waiting、查询错误和未决工作均禁止删除 |
| 原测试通过不能证明完整验收 | 成立；新增真实进程、实际 Gateway/adapter、Timer/Completion 的端到端门禁，分别记录本地与上线证据 |

### 1.2 实施状态与接续入口

以下状态仅用于避免重复劳动，不替代任务清单和验收。表格首列记录计划建立时主工作区的状态；本次实施及最终门禁进度以右侧记录为准。独立工作区为 `.worktrees/collector-period-universe-remediation`，实施分支为 `codex/collector-period-universe-remediation`。

| 范围 | 主工作区状态 | 独立工作区已有内容 | 下一步 |
| --- | --- | --- | --- |
| Task 1 命名与完整快照对象 | 待合入 | 已完成并提交 `34da1414`，规格/质量审查及主 Agent 七包复跑、三个入口 build 通过 | 本实施分支 Task 1 已勾选；最终合入前随完整链路再验证 |
| Task 3 Gateway 默认路由 | 计划建立时待实施/合入 | 四方法 Collector-only 路由、生产 adapter、服务鉴权和 resolver/Gateway 集成测试均已实现；独立审查及主 Agent 复跑通过 | 已完成；最终门禁仍按 Task 9 执行 |
| Task 9 门禁前置项 | 计划建立时待完成 | 文档模块清单、事件名、部署契约和部署脚本缺陷已修复；部分门禁已复跑通过 | 完成剩余 `make verify-pr` 和最终复核 |
| Task 2 | 完成并提交 `caea6e1c` | 后续增加了 deadline finalizer 公平轮转和失败游标重启恢复；Storage codeCR 复审及 race 测试通过 | 已完成；最终 proto-check 纳入 Task 9 |
| Task 4、5、6 | 计划建立时待实施 | 持久 Storage 状态、逐目标失败回执、Timer manifest/Claim 与 Completion 生命周期均已实现并有回归测试 | 已完成；以 Task 8 真实进程验收和最终门禁为准 |
| Task 7、8 | 计划建立时待实施 | SCF Timer Claim/period Commit/Completion、凭据与公开 CA 契约已实现；真实 Collector SQLite、Gateway、Storage 和 EventBus E2E 已运行通过 | 已完成本地实现及 E2E；不等于正式环境发布或验收 |

若独立工作区已被修改，先重新读取本表涉及文件并按实际 diff 更新执行记录；不撤销已有工作，也不把未完成改动直接发布。每项任务完成必须同时具备实现、针对性测试、审查闭环和提交记录。

**不在本次范围：**

- 不实现历史协议双读、旧 roster 回填、数据库自动迁移或旧版本兼容分支。
- 不实现多个任务共享 Dataset 的快照合并。当前任务 RPC 创建独立 Dataset，更新不允许更换目标 Dataset；保持这一业务约束并补回归断言。
- 不重写 subject-sync：外部列表刷新继续只由 `moox-collector-subject` 发起。
- 不改写已终结 marker，不延长已固定 deadline，不以失败上报触发提前终结。
- 不新建另一套 Completion RPC、消息中间件或前端页面，不全仓替换无关领域的 Universe/Series 名称。
- 计划编写本身不授权线上操作；本次实施已另获正式发布及线上验证授权。数据清理/重建不在自动授权范围，须按第 1 节的精确范围确认执行。

## 2. 统一契约

### 2.1 术语与身份

| 名称 | 中文 | 精确含义 |
| --- | --- | --- |
| `CollectionUniverse` | 采集标的池 | 当前标签解析得到的 Subject 集合，允许随成功的标签快照刷新变化 |
| `PeriodUniverseSnapshot` | 周期标的池快照 | 一个周期固定的、按 Subject 去重的集合；用于业务描述及 marker 的 `universe_subject_ids`，不新增一张重复存储表 |
| `PeriodSeriesSnapshot` | 周期采集序列快照 | Subject 经 Provider/Source 展开后的完整有序集合，携带固定 index/hash/count |
| `PeriodSeriesSnapshotEntry` | 周期采集序列快照条目 | 上述集合中的一条持久化 series row，不把单行模型命名为整个 snapshot |

周期主键保持 `(space_id, dataset_id, frequency, period_time)`。canonical series key 的组成、排序、hash 算法、index 从零连续的规则保持不变；一个 Subject 对应多个 Provider/Source 时计多个 series，但 marker 按 Subject 去重。

Storage 的可观测序列身份为 `(subject_id, series_tag)`。完整 Ensure 快照必须固定每个 index 的该身份；同一物理序列不能占两个 index。Collector 用现有 `collectionItemSeriesTag` 生成与写入相同的 storage tag，StockCN fallback Provider 链仍是一条 `default` 逻辑序列，不因为实际选中的备用 Provider 另增一个期望 bit。

### 2.2 硬截止与可保证边界

1. deadline 以 Storage 首次成功 Ensure 持久化的值为准。重复 Ensure、重试及后续 tick 不修改它。
2. `now < deadline` 才接收新的失败或成功证据；`now >= deadline` 必须走同一硬截止 fence。失败仅设置失败 bitmap，不提前 finalize。
3. `recorded` 表示该 index 的失败已持久化；`already_succeeded` 表示已有成功 bitmap，失败无需重复计入；`missed_deadline` 表示 Storage 未收录该失败，且已不可补写。
4. 已终态的失败请求不是一律 no-op：按现有持久 bitmap 返回真实结果。截止前已接收但 ACK 丢失的失败，截止后重放仍返回 `recorded`；若之后成功清除 failure bit，则返回 `already_succeeded`。
5. RPC 超时、断网、响应不完整及 Collector 本地时间过 deadline 都不能证明 Storage 未接受。此类报告保持 `pending`，记录逾期诊断并继续有界重放。
6. marker 只包含截止前 Storage 接受且最终仍未成功的失败 Subject。网络隔离导致未及时送达的失败，不能保证进入不可变 marker；必须在 Collector 保留可观测的 `pending` 或 Storage 确认的 `missed_deadline`，不得显示成已上报。

### 2.3 Timer 与运行身份

Timer 只决定何时运行，不再自己决定该周期的标的集合。Collector 固定有效目标周期、owning Run、Instance/WriteTarget、Provider/Source 与 shard；SCF 只领取和执行持久请求。

独立 service 为 `trpc.moox.collector.MarketFetchRuntime`，方法为 `ClaimTimerBatch`，Gateway alias 为 `collector-market-runtime`，仅 `caller=collector`。不把该方法加到已有 wildcard 的 `CollectMgr`，不复用 Storage 的 target node 作为 Collector node。

现有 Collector service credential 由 Gateway 验证并证明调用方服务身份；runtime listener 仅接收可信本机转发，BFF 禁止转发到该 service。Claim 不增加一套独立 `AuthInfo` 密钥或接收客户端自报的 caller；运行时 function name/request ID 与持久 shard 绑定用于一致性校验，不声称共享服务凭证能提供独立的云函数身份认证。

## 3. 任务顺序

依赖顺序：Task 1 -> Task 2 -> Task 3 -> Task 4 -> Task 5 -> Task 6 -> Task 7 -> Task 8 -> Task 9。各任务先写精确失败测试，再实现和验证；涉及同一 `scheduler.go`、Proto 或 Schema 的任务不得由不同 worker 同时修改。

每个任务验证通过后提交该任务相关文件，使用 Conventional Commit；不通过测试不得标记完成。全部任务结束后按仓库 `AGENTS.md` 提交本次实施所有变更并推送。执行阶段的代码审查使用 `codeCR` subAgent，所有审查完成后由主 Agent 独立核验。

| 阶段 | 任务 | 可验收的交付结果 |
| --- | --- | --- |
| 基础契约 | Task 1 | Universe 与 Series 含义分离，canonical key/hash/index 不变，完整快照仓储可恢复 |
| Storage 正确性与授权 | Task 2、3 | 四个真实 period RPC、逐 index 权威回执、硬 deadline、series 绑定和 Collector-only 权限 |
| Collector 状态与恢复 | Task 4、5 | 权威终态清理、逐 WriteTarget 持久回执、独立且有界的公平上报循环 |
| StockCN Timer 接入 | Task 6、7 | 持久 initial batch、Claim CAS、period Commit、Completion 和最多 3 次 Invoke retry |
| 完整验证与交付准备 | Task 8、9 | 真实进程 E2E、全量门禁、新 Agent 审查及可审计的协调发布手册 |

### Task 1：统一快照命名，不改变集合算法

**Files：**
- Rename: `modules/collector/internal/domain/task_period_series.go` -> `modules/collector/internal/domain/period_series_snapshot.go`
- Rename: `modules/collector/internal/store/task_period_series.go` -> `modules/collector/internal/store/period_series_snapshot.go`
- Rename: `modules/collector/internal/store/task_period_series_test.go` -> `modules/collector/internal/store/period_series_snapshot_test.go`
- Modify: `modules/collector/internal/store/database.go`、`modules/collector/internal/store/database_test.go`
- Modify: `modules/collector/internal/store/task_series_test.go`，更新旧条目类型和 accessor 的直接引用
- Modify: `modules/collector/internal/marketfetch/scheduler.go`、`modules/collector/internal/marketfetch/scheduler_test.go`、`modules/collector/internal/marketfetch/period_failure_reporter_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/rpc/service_validation_test.go`，固定任务专属结果 Dataset 的业务约束
- Modify: `modules/storage/proto/data_node.proto`、`modules/storage/proto/primary_store.proto`、`modules/storage/proto/storagegen/`
- Modify: `modules/storage/internal/service/datanode/period.go`、`modules/storage/internal/service/datanode/pebble/period_progress.go`、`modules/storage/internal/service/datanode/pebble/period_progress_test.go`
- Modify: `modules/collector/internal/marketstorage/storage.go` 及编译暴露的同协议调用方/测试

- [x] **1.1 固定重命名前后的等价性测试。** 使用现有两个 Subject、其中一个 Subject 含两个 Provider/Source 的 fixture，断言 series count=3、Universe count=2；固定 canonical key、hash 与 index 的预期值。已有并发首建、重启读取和冲突测试保留，禁止因改名删除测试。
- [x] **1.2 执行基线测试并记录 PASS。** `go test -count=1 ./modules/collector/internal/store ./modules/collector/internal/marketfetch ./modules/storage/internal/service/datanode/pebble`。命名任务不伪造行为失败；新模型/接口引用的编译失败应随重命名一次性消除。
- [x] **1.3 更新模型和仓储契约。** 单行仍保留当前所有 GORM 字段；保持 `TableName()` 为 `t_collector_task_period_series`，不为命名进行无收益的表重建。完整快照对象与仓储签名为：

```go
type PeriodSeriesSnapshot struct {
    Key           PeriodKey
    SeriesHash    string
    ExpectedCount uint32
    Entries       []PeriodSeriesSnapshotEntry
}

func (*PeriodSeriesSnapshotEntry) TableName() string {
    return "t_collector_task_period_series"
}

func (*PeriodSeriesSnapshotRepository) GetPeriodSeriesSnapshot(context.Context, domain.PeriodKey) (domain.PeriodSeriesSnapshot, bool, error)
func (*PeriodSeriesSnapshotRepository) CreatePeriodSeriesSnapshotIfAbsent(context.Context, domain.PeriodSeriesSnapshot) (domain.PeriodSeriesSnapshot, bool, error)
```

继续使用现有具体仓储依赖，不为改名新增 consumer interface；Get 仅在周期不存在时返回 `found=false`，损坏、不完整或身份冲突返回 error。补任务创建拒绝调用方指定结果 Dataset、不同 TaskID 生成不同结果身份、任务更新拒绝更换 Dataset 的回归测试。Store 对相同周期不同快照返回冲突，不做标签合并或静默覆盖。

- [x] **1.4 更新跨进程和 Pebble 名称。** 保留 Proto 字段编号 8，将 `repeated DatasetPeriodSeries roster = 8` 改为 `repeated DatasetPeriodSeries series_snapshot = 8`；消息 `DatasetPeriodSeries` 已表达 series 条目，继续复用。Pebble 的结构字段/JSON 名称统一为 `SeriesSnapshot` / `series_snapshot`，不加入旧名称双读。Commit/Record 的轻量 expectation 不重复携带整份快照。
- [x] **1.5 更新局部变量、错误文本、构造函数与引用。** 完整集合用 `snapshot` / `seriesSnapshot`，Subject 去重集合用 `universe`；更新 `Store.PeriodSeriesSnapshot()` 和 Scheduler 依赖，删除本次替换后无调用方的旧接口，不全仓盲替换历史文档。
- [x] **1.6 重新生成并验证。** 执行以下命令，预期全部 PASS，canonical fixture 未变；提交 `refactor(collector): unify period series snapshot terminology`。

```bash
make -C modules/storage/proto all
go test -count=1 ./modules/collector/internal/domain ./modules/collector/internal/store ./modules/collector/internal/marketfetch ./modules/collector/internal/rpc ./modules/storage/internal/service/datanode/pebble
go build ./modules/collector/cmd/subject ./modules/collector/cmd/server ./modules/storage/cmd/server
git diff --check
```

### Task 2：Storage 精确失败回执、硬截止与只读状态查询

**Files：**
- Modify: `modules/storage/proto/data_node.proto`、`modules/storage/proto/primary_store.proto`、`modules/storage/proto/storagegen/`
- Modify: `modules/storage/internal/service/datanode/period.go`、`modules/storage/internal/service/datanode/service.go`、`modules/storage/internal/service/datanode/service_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/period_progress.go`、`modules/storage/internal/service/datanode/pebble/period_progress_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store.go`，用现有 Options 注入统一周期时钟，默认生产时间不变
- Modify: `modules/storage/internal/service/primarystore/period.go`、`modules/storage/internal/service/primarystore/service_test.go`
- Modify: `modules/collector/internal/marketfetch/scheduler.go`、`modules/collector/internal/marketfetch/scheduler_test.go`，在 Ensure 中携带最终 storage-series tag
- Modify: `modules/storage/cmd/server/main.go` 中新增查询转发，使本任务协议可编译；完整代理契约在 Task 3 验证

- [x] **2.1 先补 Store 层失败测试。** 新增 `TestPeriodFailureDeadlineFence`、`TestPeriodFailureReplayAfterFinalization`、`TestPeriodFailureTerminalValidatesIndexes`、`TestPeriodCommitAcceptedIndexes`、`TestPeriodCommitRejectsWrongSeriesBinding`、`TestPeriodSeriesSnapshotStorageIdentity`、`TestPeriodStatusQueryDoesNotCreate`。Commit/Record/finalizer 共用可注入时钟，分别测试 deadline 前一刻、恰好 deadline、之后；测试 Record/finalizer 两种锁顺序，不能靠 sleep 控制竞态。
- [x] **2.2 运行失败测试。** `go test -count=1 ./modules/storage/internal/service/datanode/pebble -run 'TestPeriod(Failure|CommitAccepted|CommitRejects|SeriesSnapshot|StatusQuery)'`。当前代码应出现终态错误 ACK、截止后接收失败、无写入却宣称 Commit 成功、错绑 storage series 或缺查询方法的失败；记录具体断言。
- [x] **2.3 定义逐 index 回执。** 两层 Record 响应在字段 3 增加相同结果列表；仅返回请求中去重后的 index，稳定按 index 排序。成功响应必须完整覆盖请求，不能用 `UNSPECIFIED` 表示成功。

本任务同时补全每个 index 的行身份。保留已有字段编号，`DatasetPeriodSeries` 新增 `series_tag = 3`；Pebble snapshot 条目同步持久化。Ensure 校验 index 连续及 `(subject_id, series_tag)` 唯一，重复 Ensure 同时匹配 tag，不能仅匹配 Subject。Collector 从同一 `collectionItemSeriesTag` 生成 tag；写入校验使用实际 RowKey 的完整 identity，空 tag 只表示真实默认序列，不是通配符。

```protobuf
message DatasetPeriodSeries {
  uint32 series_index = 1;
  string subject_id = 2;
  string series_tag = 3;
}
```

```protobuf
enum PeriodFailureDisposition {
  PERIOD_FAILURE_DISPOSITION_UNSPECIFIED = 0;
  PERIOD_FAILURE_DISPOSITION_RECORDED = 1;
  PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED = 2;
  PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE = 3;
}

message DatasetPeriodFailureResult {
  uint32 series_index = 1;
  PeriodFailureDisposition disposition = 2;
}

message RecordDatasetPeriodFailuresRsp {
  common.RetInfo ret_info = 1;
  string period_status = 2;
  repeated DatasetPeriodFailureResult results = 3;
}

message PrimaryRecordDatasetPeriodFailuresRsp {
  common.RetInfo ret_info = 1;
  string period_status = 2;
  repeated DatasetPeriodFailureResult results = 3;
}
```

- [x] **2.4 在持有 `periodMu` 时实现统一 fence。** 先校验完整 identity 和所有 index，再读取 bitmap；到截止点先按既有 bitmap finalize，再分类，绝不把本次新失败补入 marker。抽取锁内 finalize helper，维持现有 datasetWriteMu/periodMu 顺序，禁止嵌套重入锁。分类规则为：

```text
success[index]                       => already_succeeded
failure[index]                       => recorded
status == waiting && now < deadline  => 持久化 failure[index] 后 recorded
其他                                 => missed_deadline
```

- [x] **2.5 维持原有原子性并返回真实 Commit 证据。** status、marker、outbox 继续同一个 Pebble batch 提交；新 failure 变更持久化成功才返回 `recorded`。任一非法 index 或错绑 series 使整个请求失败，不能先写合法部分。迟到成功在截止前清除对应 failure，终态 replay 返回 `already_succeeded`，不需要额外“曾失败”历史 bitmap。Commit 两层响应在字段 4 增加 `repeated uint32 accepted_series_indexes`：只返回请求中目标成功 bit 已持久化的去重 index；截止后首次未写入的 index 不返回，已成功的幂等重放可返回。Store 在同一锁内校验请求 index/Subject/series_tag/目标周期绑定并生成回执，DataNode 不再仅把请求 keys 当接受证明。补 `TestPeriodCommitRejectsWrongSeriesBinding`：同 Subject 的 Binance row 冒用 OKX index、同一 row 冒用两个 index，以及终态错绑重放均拒绝且零副作用；合法两个 tag 分别推进各自 bit。
- [x] **2.6 返回首次真实 deadline。** `EnsureDatasetPeriodRsp`、`PrimaryEnsureDatasetPeriodRsp` 在字段 3 增加 `int64 deadline_at`；新建或重复 Ensure 都返回已保存 deadline，Primary 原样转发，不返回本次请求的 deadline hint。
- [x] **2.7 新增只读查询 RPC。** DataNode request 字段为 `auth_info=1, node_id=2, expectation=3`，Primary request 为 `auth_info=1, expectation=2`；两层 response 为 `ret_info=1, status=2, series_hash=3, expected_count=4, deadline_at=5`。在 `DataNodePeriodRuntime` 与 `PrimaryStore` 增加 `GetDatasetPeriodStatus`。查询 expectation 只携带 key/hash/count，不要求整份 snapshot；锁内一致读取并校验 hash/count，不创建 period、不改 deadline、不调用 Ensure。不存在返回 `NOT_FOUND`，不冒充 waiting 或终态。
- [x] **2.8 补全服务映射测试。** DataNode/Primary 均验证逐 index 回执和 deadline 未丢失；查询合法等待/完整/降级、hash/count 冲突、NOT_FOUND。截止后两种锁顺序的 status、event ID、payload 必须相同；marker/outbox 数量保持 1。
- [x] **2.9 生成、验证、提交。** 预期以下全部 PASS；提交 `fix(storage): acknowledge period failures with deadline fencing`。
- [x] **2.10 补齐 finalizer 重试公平性。** deadline 批次因 marker 大小失败后降低限制并重启，仍能重试唯一 cursor 项；持久游标回绕不饿死该项或后续周期。`TestDatasetPeriodFinalizerRetriesOnlyFailedCursorAfterRestart`、轮转测试及 Pebble race focused 测试通过。

```bash
make -C modules/storage/proto all
go test -count=1 ./modules/storage/internal/service/datanode/pebble ./modules/storage/internal/service/datanode ./modules/storage/internal/service/primarystore ./modules/collector/internal/marketfetch
go test -race -count=1 ./modules/storage/internal/service/datanode/pebble -run 'TestPeriod(Failure|CommitAccepted|CommitRejects|SeriesSnapshot|StatusQuery)'
go build ./modules/storage/cmd/server
git diff --check
```

### Task 3：补齐生产代理、Gateway 路由和写鉴权

**Files：**
- Modify: `modules/storage/cmd/server/main.go`、`modules/storage/cmd/server/main_test.go`
- Modify: `modules/storage/internal/service/primarystore/period.go`、`modules/storage/internal/service/primarystore/service_test.go`
- Modify: `config/setup/service-deployments.yaml`
- Modify: `config/setup/metadata.yaml`，给仍用于 Collector period 的内置市场 Dataset 明确 `owner_module: collector` 和 `dataset_role: raw_collection`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`、`modules/admin/internal/service/sysdeploy/defaults_test.go`、`modules/admin/internal/service/sysdeploy/routes_test.go`
- Create: `modules/admin/internal/service/sysdeploy/period_gateway_contract_test.go`，从实际 YAML 与 DefaultDeployments 派生路由并验证四方法 ACL
- Modify: `modules/gateway/internal/router/router_test.go`
- Modify: `scripts/test/contract/test-deploy-moox-gateway.sh`

- [x] **3.1 先补实际 resolver 回归测试。** 新增 `TestDataNodeResolverPeriodRPCContract`，通过生产 `newDataNodeResolver` 获取默认 `dataNodeProxyAdapter`，分别执行 Ensure/Commit/Record/GetStatus 的生成客户端转发；禁止直接注入 DataNode Service 替代 adapter。修复前 Record 及整组接口断言应失败。
- [x] **3.2 增加不可遗漏的编译期契约。** adapter 转发 Record 和 GetStatus，透传 context、auth、expectation、indexes、响应和错误，不构造假的成功返回：

```go
var _ pb.DataNodePeriodRuntimeService = (*dataNodeProxyAdapter)(nil)

func (a *dataNodeProxyAdapter) RecordDatasetPeriodFailures(ctx context.Context, req *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
    return a.periodProxy.RecordDatasetPeriodFailures(ctx, req)
}

func (a *dataNodeProxyAdapter) GetDatasetPeriodStatus(ctx context.Context, req *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error) {
    return a.periodProxy.GetDatasetPeriodStatus(ctx, req)
}
```

- [x] **3.3 先补四个 period RPC 的 Collector 授权测试。** 新增 `TestEnsureDatasetPeriodWriteAuthorization` 和 Commit/Record/GetStatus 对应表驱动测试，覆盖 `moox-skill`、`scf-market-canary`、Factor/其他模块、Collector 请求非 Collector Dataset、未知 Dataset、缺 Metadata snapshot、合法 Collector owner；非法情况拒绝且 resolver/Store 无副作用。增加窄 helper `validateCollectorPeriodDataset`：通用 HMAC 验证后要求 `isOwnedAppID(appID, "collector")`，从 request snapshot 精确读取 Space/Dataset，并要求 `owner_module=collector`、`dataset_role=raw_collection`、time-series kind 及频率有效。Ensure/Commit/Record 与 GetStatus 均使用该 helper，写接口再保留只读拒绝；不扩大非 period Upsert 的授权改动。内置市场 Dataset 在 seed 明确归属，任务结果复用已有 owner 属性；无属性不默认为 Collector。共享服务凭据不提供独立 TaskID 权限，不宣称 AuthInfo 已含 Space/Dataset scope。
- [x] **3.4 修改两份生产路由来源。** YAML 与 `sysdeploy/defaults.go` 同步建立四方法 `EnsureDatasetPeriod / CommitTimeSeriesBatch / RecordDatasetPeriodFailures / GetDatasetPeriodStatus` 的独立 Primary route，显式 `gateway_callers: [collector]`；从旧宽 caller route 删除 Ensure/Commit，避免绕过窄路由。当前真实 period 调用方只有 Collector，不为无调用方保留宽权限；服务层授权仍不可依赖 Gateway 替代。
- [x] **3.5 用生产默认值生成测试路由。** `defaults_test.go` / `routes_test.go` 检查 YAML 与 DefaultDeployments 的方法/caller 等价；Gateway 通过生成的 route snapshot 验证合法 Collector 四个 RPC 可达，非法 caller 四个 period RPC 均不到上游。不能手写一个包含缺失方法的测试路由后宣称 seed 已覆盖。补内置 period Dataset 与任务生成 Dataset 的 owner metadata fixture，避免只 mock 一个“已允许”的 helper。
- [x] **3.6 验证并提交。** 实际提交：`396683ae`（Collector-only Gateway routes）、`b31b4169`（Collector market Dataset ownership）、`b058c9e9`（Primary auth/proxy contract）、`3c3fc602`（real Gateway ACL integration test）。

```bash
go test -count=1 ./modules/storage/cmd/server ./modules/storage/internal/service/primarystore ./modules/admin/internal/service/sysdeploy ./modules/gateway/internal/router
make test-gateway-deploy
go build ./modules/storage/cmd/server ./modules/gateway/cmd/server
git diff --check
```

Task 3 验证记录：四包单测、`make test-gateway-deploy`、Storage/Gateway build、Storage metadata seed/Collector taskresult/rpc 测试、resolver/auth focused race（`-count=3`）、Gateway route race（`-count=3`）和 `git diff --check` 均通过。两阶段独立审查：规格审查发现并要求补齐 Gateway 实际调用/零上游测试，复审 PASS；code quality review PASS。主 Agent 在审查后独立重跑上述门禁。Task 9 的存量 Admin active route/metadata 受控应用与部署态 E2E 尚未执行。

### Task 4：持久 Storage 权威状态并据此清理快照

**Files：**
- Create: `modules/collector/internal/domain/period_storage_state.go`
- Create: `modules/collector/internal/store/period_storage_state.go`、`modules/collector/internal/store/period_storage_state_test.go`
- Modify: `modules/collector/internal/store/period_series_snapshot.go`、`modules/collector/internal/store/period_series_snapshot_test.go`
- Modify: `modules/collector/internal/store/database.go`、`modules/collector/internal/store/database_test.go`、`modules/collector/schema/collector.sql`
- Modify: `modules/collector/internal/marketstorage/storage.go`
- Create: `modules/collector/internal/marketstorage/storage_period_test.go`
- Modify: `modules/collector/internal/marketfetch/contracts.go`、`modules/collector/internal/marketfetch/scheduler.go`、`modules/collector/internal/marketfetch/scheduler_test.go`
- Create: `modules/collector/internal/marketfetch/period_storage_reconciler.go`、`modules/collector/internal/marketfetch/period_storage_reconciler_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`

- [x] **4.1 先补清理拒绝测试。** 覆盖“无 readiness”“30 天以上”“本地无活动批次”但 Storage waiting/unknown/NOT_FOUND/error 的情况，均不删除；只在 key/hash/count 匹配且 Storage complete/degraded 时允许删除。另测仍有 planned/dispatched batch、未结束 retry 或失败回执 pending 的周期保留。
- [x] **4.2 执行失败测试。** `go test -count=1 ./modules/collector/internal/store -run 'TestPeriodSeriesSnapshotCleanup'`。旧无 readiness 分支应违反保留断言；删除原来固化错误删除语义的断言，替换为上述明确场景。
- [x] **4.3 新建一张权威状态表，兼作首次 deadline 及终态确认。** 不同时新增另一张重复 terminal receipt 表。模型 `PeriodStorageState` 保留 key/hash/count、`DeadlineAt`、`Status`、`ConfirmedAt`，每行只由成功 Ensure/GetStatus 的匹配响应更新。

```sql
CREATE TABLE IF NOT EXISTS t_collector_period_storage_states (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time DATETIME NOT NULL,
    c_series_hash TEXT NOT NULL,
    c_expected_count INTEGER NOT NULL,
    c_deadline_at DATETIME NOT NULL,
    c_status TEXT NOT NULL,
    c_confirmed_at DATETIME NOT NULL,
    CHECK (c_expected_count > 0),
    CHECK (c_status IN ('waiting', 'complete', 'degraded')),
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency, c_period_time)
);
```

- [x] **4.4 更新 Collector Storage 接口。** Ensure 返回权威 `PeriodStorageState`，GetStatus 返回同类型；校验响应状态及 hash/count。首次 Ensure ACK 丢失时可以用只读 GetStatus 恢复，不用后续 tick 重算 deadline。仓储方法为 `ObservePeriodStorageState(ctx, state)` 和 `GetPeriodStorageState(ctx, key)`；不同 hash/count/deadline 拒绝，旧 waiting 响应不能覆盖已确认 complete/degraded。Commit 保持返回 error 的调用契约，但必须校验 `accepted_series_indexes` 恰好覆盖本次请求去重后的全部目标 index，缺项/额外项/重复项均报错；终态 nil-error 不再冒充实际写入成功。补截止后未接受失败、已成功重放通过的 adapter 测试。
- [x] **4.5 移除启动前置清理和错误推断。** 清理在 Storage client 可用后执行，按 Space 以 `(period_time,dataset_id,frequency)` 稳定 keyset 分页探测最多 1000 个过期周期，超时/error 留待下一轮。候选 API 显式接受/返回 cursor，reconciler 为每 Space 保留跨轮游标，到尾部回绕，不新增游标表；失败候选也推进游标。新增最老 1000 条持续 NOT_FOUND/error、第 1001 条合法终态仍能在下一轮清理的测试。把 `CleanupReportedBefore` 改为 `CleanupTerminalBefore(ctx, spaceID, before, limit)`，SQL 只 JOIN 匹配终态 state；删除 `OR NOT EXISTS readiness` 条件。保持 30 天保留期及活动工作保护，不把 local missed、年龄或 readiness 当 Storage 终态。
- [x] **4.6 保持探测与删除身份一致。** 探测用快照自身 key/hash/count；删除事务再次检查相同 hash/count、Storage state 终态及未决工作，再删除候选快照/state，不能跨 Space 扫全表。Timer manifest 尚未在本任务创建，其关联删除在 Task 6 加入同一清理事务。
- [x] **4.7 验证恢复与 schema。** 增加 Ensure 返回原始 deadline、重启读取 state、并发 waiting/terminal 响应不倒退测试；更新 fresh-schema 必需表/列校验。Task 5 变更回执字段后再次扩展 pending 保护，不能提前依赖尚不存在的列。
- [x] **4.8 验证并提交。** 实际提交：`cc9a1737`（Storage 终态清理、Collector 状态持久化与 reconciler）、`eed2a4ef`（处理 Primary `INNER_ERR` unknown outcome）。

```bash
go test -count=1 ./modules/collector/internal/store ./modules/collector/internal/marketstorage ./modules/collector/internal/marketfetch ./modules/collector/internal/bootstrap
sqlite3 :memory: '.read modules/collector/schema/collector.sql'
git diff --check
```

Task 4 验证记录：四包 fresh tests、四包 race、SQLite schema load、`gofmt` 和 `git diff --check` 均通过。首次清理拒绝测试在旧实现上按预期 RED；一次 Store 测试挂起定位为事务内从主 DB 二次取单连接，改为复用 transaction 后通过。规格审查 PASS；codeCR 发现 Primary 内部 DataNode 错误会以 nil-Go-error `INNER_ERR` 返回，导致 Ensure 未做状态恢复；新增回归先 RED 后以只读 GetStatus 恢复，`INNER_ERR+NOT_FOUND` 有界重试，参数/权限/冲突仍立即拒绝；修复复审 PASS。主 Agent 独立重跑四包 fresh/race 与 SQLite schema load。非阻断余项：未跑全 workspace gate；没有 bootstrap Storage-client-unavailable 级测试，也没有双 DB 连接并发插入 batch/retry 竞争测试。

### Task 5：逐目标持久失败回执与独立上报循环

**Files：**
- Modify: `modules/collector/internal/domain/fetch_batch.go`
- Create: `modules/collector/internal/domain/period_failure_report_test.go`
- Modify: `modules/collector/schema/collector.sql`、`modules/collector/internal/store/database.go`、`modules/collector/internal/store/database_test.go`
- Modify: `modules/collector/internal/store/fetch_retry.go`
- Create: `modules/collector/internal/store/fetch_retry_report_test.go`
- Modify: `modules/collector/internal/marketstorage/storage.go`、`modules/collector/internal/marketstorage/storage_period_test.go`
- Create: `modules/collector/internal/marketfetch/period_failure_reporter.go`
- Modify: `modules/collector/internal/marketfetch/period_failure_reporter_test.go`、`modules/collector/internal/marketfetch/scheduler.go`、`modules/collector/internal/marketfetch/contracts.go`、`modules/collector/internal/marketfetch/metrics.go`、`modules/collector/internal/marketfetch/metrics_test.go`
- Modify: `modules/collector/internal/store/period_series_snapshot.go`、`modules/collector/internal/store/period_series_snapshot_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`

- [x] **5.1 先补错误 ACK 与恢复测试。** 覆盖终态未录入失败、截止前录入但 ACK 丢失、成功清除 failure、缺项/重复/额外 index/未知枚举响应、多 WriteTarget 一个 accepted 一个 missed、部分 RPC 错误、重启继续报告。任一未决目标时聚合状态必须仍为 pending。
- [x] **5.2 执行失败测试。** `go test -count=1 ./modules/collector/internal/marketfetch -run 'TestPeriodFailureReport'`；旧 boolean + nil-error 判成功的实现应失败。
- [x] **5.3 替换 boolean，不保留兼容分支。** `FetchRetryItem.PeriodFailureReported` 改成 `PeriodFailureReportState`，状态值 `pending / acknowledged / missed_deadline`；schema 替换 `c_period_failure_reported`，新增 `c_period_failure_results_json`、`c_period_failure_last_error`、`c_period_failure_deadline_exceeded_at`，更新 CHECK、pending 索引和启动校验。

```go
type PeriodFailureReportState string

const (
    PeriodFailureReportPending        PeriodFailureReportState = "pending"
    PeriodFailureReportAcknowledged   PeriodFailureReportState = "acknowledged"
    PeriodFailureReportMissedDeadline PeriodFailureReportState = "missed_deadline"
)

type PeriodFailureTargetResult struct {
    WriteTargetID string    `json:"write_target_id"`
    SpaceID       string    `json:"space_id"`
    DatasetID     string    `json:"dataset_id"`
    Frequency     string    `json:"frequency"`
    PeriodTime    time.Time `json:"period_time"`
    SeriesHash    string    `json:"series_hash"`
    ExpectedCount uint32    `json:"expected_count"`
    SeriesIndex   uint32    `json:"series_index"`
    Disposition   string    `json:"disposition"`
    ObservedAt    time.Time `json:"observed_at"`
}
```

- [x] **5.4 适配器返回并验证逐 index 结果。** `RecordDatasetPeriodFailures` 返回 `[]*storagepb.DatasetPeriodFailureResult, error`；RetInfo 非成功、status 非法或结果集合不等于请求去重集合时返回错误，不能当作已上报。按 `(period identity, series_index)` 映射回原 WriteTarget，重复 index 的多个目标均保留各自结果，不用 `succeededGroups` 布尔值代替。
- [x] **5.5 原子合并目标结果并计算聚合状态。** 仓储方法 `ApplyPeriodFailureReportResults(ctx, spaceID, retryKey, results, lastError)` 在事务内读取已有结果、校验目标属于持久 FailureTargets、合并而非覆盖。规则：任何目标缺权威结果 => pending；全部 recorded/already_succeeded => acknowledged；无未决且至少一个 missed_deadline => missed_deadline。已接受结果不因后续网络错误倒退；missed 只有 Storage 明确返回才能写入。

聚合时按全部持久 FailureTargets 构造 dispositions，未确认目标填空值，不能只把本次成功 RPC 的目标传入。纯函数及最小表驱动测试为：

```go
func AggregatePeriodFailureReportState(dispositions []string) PeriodFailureReportState {
    if len(dispositions) == 0 {
        return PeriodFailureReportPending
    }
    missed := false
    for _, disposition := range dispositions {
        switch disposition {
        case "recorded", "already_succeeded":
        case "missed_deadline":
            missed = true
        default:
            return PeriodFailureReportPending
        }
    }
    if missed {
        return PeriodFailureReportMissedDeadline
    }
    return PeriodFailureReportAcknowledged
}
```

```go
func TestAggregatePeriodFailureReportState(t *testing.T) {
    cases := []struct {
        name   string
        values []string
        want   PeriodFailureReportState
    }{
        {"empty", nil, PeriodFailureReportPending},
        {"accepted", []string{"recorded", "already_succeeded"}, PeriodFailureReportAcknowledged},
        {"partial_missed", []string{"missed_deadline", ""}, PeriodFailureReportPending},
        {"all_confirmed_with_missed", []string{"recorded", "missed_deadline"}, PeriodFailureReportMissedDeadline},
        {"unknown", []string{"recorded", "unknown"}, PeriodFailureReportPending},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := AggregatePeriodFailureReportState(tc.values); got != tc.want {
                t.Fatalf("got %q, want %q", got, tc.want)
            }
        })
    }
}
```
- [x] **5.6 将报告从 Tick 拆到独立循环。** `PeriodFailureReporter.RunOnce(ctx, spaceID)` 复用永久失败 retry 行作为持久 outbox，按 retry key 分页、单轮最多 1000 条、整轮共用 5 秒 context 预算，单个 RPC 的现有超时不得越过该预算；bootstrap 启动每秒触发、无重叠、支持 cancel 的循环。每次已尝试的行推进分页游标，到尾部回绕；失败仍 pending，但不能让最前面的失败行永久饿死后面的报告。Completion 永久失败落库后唤醒循环；CloudNode 列表/规划失败不能阻断报告。没有另建 broker 或 in-memory-only 队列。新增超过一页、首行持续超时而后页可达，以及单轮预算到期退出的测试。
- [x] **5.7 逾期保持可恢复。** 对 canonical deadline 已到但传输未确认的目标记录一次 `deadline_exceeded_at` 与最后错误，仍 pending 并重放；不能按本地钟永久停止。恢复后 Storage 的 recorded/already_succeeded 可将其收敛为 acknowledged，未接受则收敛为 missed_deadline。清理保护 pending 行直到拿到权威结论。
- [x] **5.8 补可观测性。** 在现有 metrics 增加 pending、missed-deadline 与上报重试计数，新增指标的 label 仅使用已配置 Space、有效 frequency 和有限枚举 outcome；Dataset 是按 Task 生成的身份，不能把它误当作 bounded label。subject、dataset、taskID、retryKey 均只放结构化日志和持久 JSON，不进入新增指标标签。新增任务增删测试，断言不同 Dataset 不产生新的指标标签组合，并通过已有 metrics registry 验证各 outcome 计数；不借此重写无关监控。日志携带 period 和 outcome，不打印凭证，无需新前端页面。
- [x] **5.9 验证并提交。** 预期全部 PASS，包括报告无节点可用时仍运行、schema 空库及 race；提交 `fix(collector): persist authoritative period failure acknowledgements`。

```bash
go test -count=1 ./modules/collector/internal/domain ./modules/collector/internal/store ./modules/collector/internal/marketstorage ./modules/collector/internal/marketfetch ./modules/collector/internal/bootstrap
go test -race -count=1 ./modules/collector/internal/marketfetch -run 'TestPeriodFailureReport'
sqlite3 :memory: '.read modules/collector/schema/collector.sql'
git diff --check
```

Task 5 完成记录：提交 `1894a96e`。首轮与二轮独立审查发现并修正 pending outbox 被 Task 删除/disabled-target prune 丢弃、同目标权威结果覆盖、metrics 在预算耗尽后不刷新、日志缺 period/outcome、重复 index 多目标无回归测试等问题。第三轮复审另发现 round context 外增加 metrics 时间会突破 5 秒总预算，以及预算中断的短页误清游标导致前缀重试饥饿；实现已将 metrics 预留在同一 round deadline 内，并在中断时保留游标，测试先复现饥饿再通过。

主 Agent 最终独立验证通过：五包 fresh tests、五包完整 race、五包 `go vet`、计划指定的 failure-report race、SQLite 空库 schema load、Collector scoped `git diff --check`。规格审查与最终 codeCR 复审均 PASS。Strategy/Trade/proto/ops 文档的既有未提交改动未纳入 Task 5 提交。

### Task 6：持久 Timer 周期批次与独立 Claim RPC

**Files：**
- Create: `modules/collector/internal/domain/timer_period_batch.go`
- Create: `modules/collector/internal/store/timer_period_batch.go`、`modules/collector/internal/store/timer_period_batch_test.go`
- Create: `modules/collector/internal/marketfetch/timer_period_planner.go`、`modules/collector/internal/marketfetch/timer_period_planner_test.go`
- Create: `modules/collector/internal/marketfetch/timer_batch_claimer.go`、`modules/collector/internal/marketfetch/timer_batch_claimer_test.go`
- Create: `modules/collector/internal/rpc/market_fetch_runtime.go`、`modules/collector/internal/rpc/market_fetch_runtime_test.go`
- Modify: `modules/collector/proto/collector.proto`、`modules/collector/proto/collectorgen/`
- Modify: `modules/collector/schema/collector.sql`、`modules/collector/internal/store/database.go`、`modules/collector/internal/store/database_test.go`、`modules/collector/internal/store/fetch_batch.go`
- Modify: `modules/collector/internal/store/period_series_snapshot.go`、`modules/collector/internal/store/period_series_snapshot_test.go`，在 manifest 表创建后扩展清理事务
- Modify: `modules/collector/internal/marketfetch/scheduler.go`、`modules/collector/internal/marketfetch/scheduler_test.go`、`modules/collector/internal/marketfetch/reconciler.go`、`modules/collector/internal/marketfetch/reconciler_test.go`、`modules/collector/internal/marketfetch/completion.go`、`modules/collector/internal/marketfetch/completion_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`、`modules/collector/config/trpc_go.yaml`
- Modify: `modules/collector/internal/bootstrap/config.go`、`modules/collector/internal/bootstrap/discovery.go` 及现有配置/发现测试，显式解析 Collector runtime Gateway endpoint 与 target node
- Modify: `config/setup/service-deployments.yaml`、`modules/admin/internal/service/sysdeploy/defaults.go`、`modules/admin/internal/service/sysdeploy/defaults_test.go`、`modules/admin/internal/service/sysdeploy/routes.go`、`modules/admin/internal/service/sysdeploy/routes_test.go`
- Modify: `modules/admin/internal/gateway/gateway.go`、`modules/admin/internal/gateway/gateway_test.go`

- [x] **6.1 先补计划与 CAS 测试。** 同周期两个 tick、Collector 重启、午休/节假日都只能保留一个 initial manifest、同一 owning Run/BatchItems；首次 Ensure 失败零 batch。单个候选时两个不同 requestID 并发 Claim 只有一个首次领取成功；相同 requestID 丢响应重放返回相同请求，terminal batch 重放 no_work。另建同 function/group/binding 的两个有效周期，断言先领旧周期、再领新周期；重启、持续新周期入队和并发 Claim 不能饿死旧周期。同 requestID 并发遇到两个候选仍只绑定同一个 batch，不因 CAS 失败领取下一批。
- [x] **6.2 执行失败测试。** `go test -count=1 ./modules/collector/internal/marketfetch ./modules/collector/internal/store -run 'TestTimer(Period|BatchClaim)'`。旧 Timer 分支缺批次/Claim，应编译或断言失败。
- [x] **6.3 定义持久 Timer manifest。** 表名 `t_collector_timer_period_batches`；模型 `TimerPeriodBatch` 字段为 key、TaskID、FirstRunID、SeriesHash、ExpectedCount、GroupID、GroupCount、ShardIndex、BindingHash、RouteVersion、BatchID、FunctionName、NodeID、Region、ClaimRequestID、ClaimedAt、CreateTime。唯一约束 `(space_id,dataset_id,frequency,period_time,shard_index)` 和 `(space_id,batch_id)`；另加 `(space_id,function_name,claim_request_id)` 的部分唯一索引，仅索引非空 claim request ID，防止同一次运行绑定两个 batch。ClaimRequestID/ClaimedAt 在 CAS 成功时写入，其余身份字段不可更新。
- [x] **6.4 冻结分组而非每轮重算。** 使用 snapshot 内的 series、现有 Provider/Source route 及 rendezvous/stagger 规则生成 shard，单 batch 不跨 Provider/Source、遵守请求 item 上限。GroupCount、route version 与静态 binding hash 首次固定；binding hash 不包含当前标签成员或当前 tick。进行中的 manifest 不因 Reconciler 当前标签或新 Run 追加 Instance。
- [x] **6.5 固定首次持久化顺序。** 先创建/读取 snapshot，再用完整 snapshot Ensure Storage，保存 canonical state，成功后一个 SQLite 事务创建 owning Instance/WriteTarget、manifest、batch、batch items、RequestJSON。首次 batch ID 为 `stableID(spaceID,datasetID,frequency,periodUTC,shardIndex,"timer-initial")`，不含 RunID/requestID/tick/动态 assignment hash；sync point ID 同样稳定。已有 manifest 只读复用，不能执行现有 duplicate insert 后仍追加 BatchItems 的分支。
- [x] **6.6 拒绝重复/过期首次计划。** 初次创建前按市场 Calendar 确认有效 TargetDataTime；终态或 canonical deadline 已过的周期不新建 initial batch。标签变化只影响下一周期。Task 修改/停用时，未领取批次按现有任务控制语义取消；已持久 owning 关系不静默替换为新 Run。
- [x] **6.7 定义独立运行协议。** 不接受客户端提交标的列表、Items 或周期快照；`request_json` 为有界持久 `Request` 的 JSON，worker 使用现有结构化 JSON decoder，而不是重新展开标签。所需字段为：

```protobuf
message ClaimTimerBatchReq {
  string space_id = 1;
  string function_name = 2;
  string request_id = 3;
  uint32 group_id = 4;
  uint32 group_count = 5;
  string binding_hash = 6;
  int64 tick_time = 7;
}

message ClaimTimerBatchRsp {
  common.RetInfo ret_info = 1;
  bool claimed = 2;
  bytes request_json = 3;
  int64 period_deadline_at = 4;
}

service MarketFetchRuntime {
  rpc ClaimTimerBatch(ClaimTimerBatchReq) returns (ClaimTimerBatchRsp);
}
```

- [x] **6.8 实现领取事务。** Gateway 负责验证既有 Collector credential 与 caller，runtime 在可信 loopback 边界内验证 Space、function/group/binding 与 manifest 一致、目标仍启用，然后 CAS `planned -> dispatched`，同事务保存 claim requestID/node/region 与完成超时。返回已冻结 RequestJSON，其中每项含 InstanceID、TargetDataTime、SeriesIndex/Hash/Count，每个 WriteTarget 携带自己的 period identity；request ID 从本次已持久 claim 填入，不重建 Items/Targets。无可领取批次返回 `claimed=false`、空 payload；超过请求体/响应 item 限制拒绝而非拆出未持久的新批次。已领取请求的同 requestID 重放是幂等交付，不承诺网络层 exactly-once 执行。

领取顺序必须确定：先查 `(space_id,function_name,request_id)` 的已持久 claim，核验本次 group/count/binding 与原 claim 一致；非终态返回原 payload，终态返回 no_work，均不改领另一个周期。没有已持久 claim 时，只选择匹配静态身份、任务仍启用、尚未过 canonical deadline 的 planned manifest，按 `(period_time,dataset_id,frequency,shard_index)` 稳定 oldest-first；不把 tick_time 当成“只查最新周期”的过滤条件。CAS 竞争失败先重新查本 requestID，再有界重选下一候选，受本次 RPC context 约束，不无限自旋。唯一冲突回查原 claim，不静默覆盖。一次 Claim 最多返回一个已持久 batch；已过期 planned manifest 由 6.11 的超时恢复收尾，不能永远占据活动工作保护。
- [x] **6.9 接入 server 与 Gateway。** 增加独立 runtime listener，HTTP 为 loopback `127.0.0.1:11418`、native tRPC 为 loopback `127.0.0.1:11422`；实施时先确认配置和运行端口无占用，冲突时停止并调整配置与测试，不抢占已有服务。注册同一 runtime handler，Gateway 显式 alias/method/caller；`sysdeploy/routes.go` 的 native 端口映射为 `collector-market-runtime -> 11422`，不能把 tRPC 发往 deployment 的 HTTP 11418。BFF 同时拒绝 runtime alias 及解析后的 service path，避免通过别名绕过机器 ACL；保留旧 CollectMgr 管理接口，不扩大 wildcard。

新增部署条目是 Collector 进程内 endpoint，不是第二个独立进程；YAML 与 DefaultDeployments 同步：

```yaml
- name: collector_market_runtime
  kind: collector_runtime
  deployment_mode: endpoint
  protocol: http
  host: 127.0.0.1
  port: 11418
  gateway_path: trpc.moox.collector.MarketFetchRuntime
  gateway_service_id: collector-market-runtime
  gateway_enabled: true
  scope: internal
  status: active
  extra_config:
    gateway_methods: [ClaimTimerBatch]
    gateway_callers: [collector]
```

`Dependencies.CollectorRuntimeGatewayTarget` 与 `CollectorRuntimeGatewayNodeID` 从 Collector 所在节点的 native service gateway 解析，支持显式配置；缺值时 Claim 配置失败，不能回退 Storage 的节点。测试包含 Collector/Storage 不同节点的路由，避免仅单机通过。
- [x] **6.10 校验 Timer Completion 身份。** initial Timer batch 在既有 Completion 原子事务前验证持久 claim 的 Batch/Node/Function/RequestID/Region，错误事件零副作用。Invoke retry 仍沿用现有 failover identity 规则，不能把 Timer 的固定 node 校验套到合法重试。继续使用 Batch terminal CAS + 所有 ownership 校验 + effects 的事务，不新增 Complete RPC。
- [x] **6.11 拆开超时恢复与节点派发。** `recoverDue` 无 Invoke node 也要持久 timed_out 与 retry effects；同时扫描已到 canonical deadline 的未领取 planned manifest，使其按有界 timeout/retry 规则收尾，不能因 Claim 排除过期候选而永久保持 planned。`dispatchDueRetries` 无容量时保留队列并报警，不重新创建 Timer initial；失败报告的 pending/missed 仍由 Task 5 的权威回执决定，不由本地过期推断。StockCN 重试节点纳入可 Invoke 的 Timer fleet/实际可用 catalog，发布前验证该容量；不因为保留 Timer 初始触发就把重试也交给“下次 Timer”。同时扩展 Task 4 清理事务：仅已确认终态且无未决工作时删除同周期 manifest，再删除 snapshot/state；未领取 planned manifest 也属于活动工作保护。
- [x] **6.12 生成、验证、提交。** 预期全部 PASS；提交 `feat(collector): claim durable timer period batches`。

```bash
make -C modules/collector/proto all
go test -count=1 ./modules/collector/internal/store ./modules/collector/internal/marketfetch ./modules/collector/internal/rpc ./modules/collector/internal/bootstrap ./modules/admin/internal/service/sysdeploy ./modules/admin/internal/gateway
go test -race -count=1 ./modules/collector/internal/marketfetch -run 'TestTimer(Period|BatchClaim|Completion)'
sqlite3 :memory: '.read modules/collector/schema/collector.sql'
git diff --check
```

Task 6 完成记录：提交 `82ef1df2`。最终 codeCR 与规格复审均 PASS。复审补正了按周期区分 write-target retry identity、按同一目标与周期边界 supersede 旧重试，并新增跨周期重试和 sibling target 保留的回归测试。主 Agent 最终验证通过：计划指定六包测试矩阵、Timer Completion race、retry/store race、proto 生成、SQLite schema load 与 `git diff --check`。

### Task 7：SCF Timer 消费 Claim、提交 period 并发布 Completion

**Files：**
- Create: `modules/collector/internal/marketfetch/timer_runtime_client.go`、`modules/collector/internal/marketfetch/timer_runtime_client_test.go`
- Modify: `modules/collector/internal/marketfetch/timer.go`、`modules/collector/internal/marketfetch/timer_test.go`、`modules/collector/internal/marketfetch/contracts.go`
- Modify: `modules/collector/internal/marketfetch/handler.go`、`modules/collector/internal/marketfetch/handler_test.go`、`modules/collector/internal/marketfetch/kline_pipeline.go`、`modules/collector/internal/marketfetch/kline_pipeline_test.go`
- Modify: `modules/collector/internal/serverless/market_data/handler.go`、`modules/collector/internal/serverless/market_data/handler_test.go`
- Modify: `modules/collector/internal/marketfetch/reconciler.go`、`modules/collector/internal/marketfetch/reconciler_test.go`
- Modify: `modules/collector/internal/marketfetch/environment.go`、`modules/collector/internal/marketfetch/environment_test.go`
- Modify: `modules/cli/internal/command/collector.go`、`modules/cli/internal/command/collector_test.go`
- Modify: `modules/cli/internal/setup/config/config.go`、`modules/cli/internal/setup/config/config_test.go`、`modules/cli/internal/setup/config/runtime_config.go`、`modules/cli/internal/setup/config/runtime_config_test.go`，只调整既有 SCF 执行预算与 runtime endpoint 配置
- Modify: `modules/cloudnode/internal/rpc/runtime_config.go`、`modules/cloudnode/internal/rpc/runtime_config_test.go`、`modules/cloudnode/internal/rpc/node.go`、`modules/cloudnode/internal/rpc/node_test.go`，复用最终合并环境校验
- Modify: `scripts/build/build-collector-scf-package.sh`、`scripts/build/build-collector-scf-package_test.sh`
- Modify: `modules/cli/internal/collectorpackager/scf.go`、`modules/cli/internal/collectorpackager/scf_test.go`，同步 Go 打包入口和外部 ZIP 校验
- Modify: `modules/collector/internal/marketstorage/storage.go`、`modules/collector/internal/marketstorage/storage_period_test.go`，在运行时解析受管理的 Storage 凭据，缺失时拒绝写入
- Create: `modules/collector/internal/marketstorage/storage_auth.go`、`modules/collector/internal/marketstorage/storage_auth_test.go`，严格解析 app-key 对象并拒绝重复键
- Modify: `modules/collector/configs/scf/market_data/sources/market/binance.yaml`、`modules/collector/configs/scf/stockcn/sources/market/binance.yaml`，SCF binding 只保留 app ID，app key 为空
- Modify: `modules/collector/internal/subjectsync/storage_client_test.go` 及现有 marketwiring Storage 测试，验证 host 凭据路径及错误传播不被 SCF 契约破坏

- [x] **7.1-7.8b Timer、period 写入、Completion 与凭据链路。** Claim fail-closed、period-only Commit、Storage wrapper、历史目标 bar、最多三次 Invoke retry、持久 Completion、环境预算和运行时受管凭据解析均已实现；Collector/CLI/CloudNode 测试、SCF package contract 与本地 E2E 已覆盖。针对性 race 测试及全 Workspace 测试通过。

运行时 `storageAuthInfo(binding)` 改为 `(*storagepb.AuthInfo, error)`：通过 `os.LookupEnv` 判断 JSON 是否存在，存在但为空也必须报错，不能回退。JSON 一旦存在就是权威来源，使用 `json.Decoder.Token` 先检查顶层对象及重复 app ID，再构造 `map[string]string`；禁止直接 `json.Unmarshal` 到 map 后声称已检测重复键。按 `binding.AuthInfo.AppID` 精确取值；非法 JSON、重复 app ID、缺项、非字符串/非 64 位 hex、顶层 null/数组或尾随第二段 JSON 均报错且不回退，错误不携带凭据值。JSON 不存在时保留 host 的现有主密钥派生路径；SCF binding 的 key 为空，因此 SCF 缺受管 JSON 时 fail closed。错误从 `NewBatchStorageWithWriteSource`、`NewResampleMetadataClient`、`NewResampleStorage`、`ResolveStorageAuthInfo` 传播，验证 subject-sync 的 host 用法仍正常。所有 ZIP 上传入口，包括 `publish --zip` 和 `function deploy --zip`，均先执行相同的无凭据校验，再执行现有 CLS 校验；不能因外部 ZIP 跳过门禁。

严格解析器的实现与重复键回归为：

```go
package marketstorage

import (
    "encoding/hex"
    "encoding/json"
    "errors"
    "io"
    "strings"
)

func parseStoragePrimaryAppKeys(raw string) (map[string]string, error) {
    decoder := json.NewDecoder(strings.NewReader(raw))
    token, err := decoder.Token()
    if err != nil || token != json.Delim('{') {
        return nil, errors.New("storage app keys must be a JSON object")
    }
    keys := make(map[string]string)
    for decoder.More() {
        token, err := decoder.Token()
        appID, ok := token.(string)
        if err != nil || !ok || strings.TrimSpace(appID) == "" {
            return nil, errors.New("invalid storage app ID")
        }
        if _, duplicate := keys[appID]; duplicate {
            return nil, errors.New("duplicate storage app ID")
        }
        var appKey string
        if err := decoder.Decode(&appKey); err != nil || len(appKey) != 64 {
            return nil, errors.New("storage app key must be 64 hex characters")
        }
        if _, err := hex.DecodeString(appKey); err != nil {
            return nil, errors.New("storage app key must be 64 hex characters")
        }
        keys[appID] = appKey
    }
    if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
        return nil, errors.New("invalid storage app-key object")
    }
    if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
        return nil, errors.New("trailing storage app-key JSON")
    }
    return keys, nil
}
```

```go
package marketstorage

import (
    "strings"
    "testing"
)

func TestStoragePrimaryAppKeysRejectDuplicateAppID(t *testing.T) {
    raw := `{"moox-collector":"` + strings.Repeat("a", 64) +
        `","moox-collector":"` + strings.Repeat("b", 64) + `"}`
    keys, err := parseStoragePrimaryAppKeys(raw)
    if err == nil || keys != nil {
        t.Fatal("duplicate app ID must fail closed")
    }
}
```

在 writer 构造测试中设置上述重复对象、一个看似合法的 host 主密钥以及计数型 fake client，断言构造失败、没有回退且 RPC 次数为 0；对空值、null、数组、尾随 JSON、非字符串值和缺少 binding app ID 执行相同零 RPC 断言。这里只使用测试凭据，不把真实主密钥写入 fixture。
- [x] **7.8c 固定公开 CA 的打包数据路径。** 用户选择按计划将公开 CA 打包。Go/shell packager 只接收并校验公开证书，固定写入 `certs/eventbus-ca.pem`；运行时使用该路径，私钥及凭据不会入包。外部 ZIP 和 CA/凭据负例已覆盖。
- [x] **7.9 验证实现。** SCF package contract、CLI 全量测试、Collector/Storage/CloudNode 相关测试、完整 Workspace 门禁和 build 通过；`make proto` 已执行且无额外生成漂移。Task 9 的 `make verify-pr` 最终门禁待提交后执行。

```bash
go test -count=1 ./modules/collector/internal/marketfetch ./modules/collector/internal/marketstorage ./modules/collector/internal/marketwiring ./modules/collector/internal/subjectsync ./modules/collector/internal/serverless/market_data ./modules/cli/internal/collectorpackager ./modules/cli/internal/command ./modules/cli/internal/setup/config ./modules/cloudnode/internal/rpc
make test-collector-scf-package-contract
go build ./modules/collector/cmd/scf/market_data ./modules/collector/cmd/server
git diff --check
```

### Task 8：真实进程边界端到端验收

**Files：**
- Create: `modules/storage/cmd/server/period_process_helper_test.go`
- Modify: `modules/gateway/cmd/e2e-helper/main.go`
- Create: `modules/collector/internal/marketfetch/period_storage_rpc_e2e_test.go`
- Create: `scripts/test/e2e/test-collector-period-universe-e2e.sh`
- Modify: `Makefile`，新增本地 `test-collector-period-universe-e2e` 验收入口

- [x] **8.1 先建立不会绕过生产 adapter 的测试进程。** Storage helper 通过 `go test -c` 编译，`TestPeriodNativeProcessHelper` 仅在 `MOOX_PERIOD_E2E_HELPER=1` 时启动，普通测试显式 Skip；启动真实 Pebble DataNode native listener、Primary、Metadata，并复用实际 resolver/adapter。不跨模块 import Storage internal，不直接 `Options.Node = datanode.Service`。readiness、端口、临时目录、退出信号及日志均由 test harness 控制。实现提交 `b4a65388`；主 Agent Storage server 测试与 race 生命周期测试通过；codeCR 未发现阻断项。受控时钟已验证 deadline 后状态降级，SIGINT/SIGTERM 与缺失时钟 fail-closed 已验证。
- [x] **8.2 接入真实 Gateway 路由。** 既有 e2e-helper 新增 `collector-period-native` 模式，读取部署 YAML 派生路由，保留方法/caller 白名单，只替换临时 listener 地址。Collector 使用真实 marketstorage/gatewayauth client；缺 route、上游错误或 helper 启动失败必须 FAIL，不能降级为直接 RPC 或测试 Skip。实现提交 `e4f6c5d3`；独立 codeCR 复审及主 Agent focused/race 测试通过。真实 Collector client 跨 Gateway RPC 将由 8.3/8.8 端到端测试完成。
- [x] **8.3-8.7 真实进程端到端场景。** `period_storage_rpc_e2e_test.go` 使用真实 Collector SQLite/Runtime、嵌入式 EventBus、Gateway 路由和 Storage Pebble helper；覆盖快照冻结/下周期更新、Subject 去重、错误绑定、failure ACK 与 deadline、Timer Claim/CAS/retry/Completion、清理与 marker 不变。
- [x] **8.8 本地 E2E 门禁。** `bash scripts/test/e2e/test-collector-period-universe-e2e.sh` 成功，实际执行并报告全部场景；Collector Timer/reporter focused race 测试 `-count=5` 通过。此证据仅为本地真实进程 E2E，不替代正式环境 1m/1h 验收。

2026-10-01 最终复核补充：周期 E2E 经 loopback-only test endpoint 检查真实 Pebble marker/outbox 持久化、payload 与唯一性；它没有启动生产 Storage Relay 并等待 JetStream subscriber ACK，因此不单独证明 `CollectorPeriodCompleted` 已经从 Relay 发布并被订阅。Storage outbox Relay 单测与 Storage cgo E2E 另行通过，但仍不覆盖该 marker 的实际 JetStream 投递；此项由编码后 `codeCR` 评为非阻断 P3 测试证据缺口，线上验收记录也必须区分 outbox 入队与 EventBus 投递。

```bash
bash scripts/test/e2e/test-collector-period-universe-e2e.sh
go test -race -count=5 ./modules/collector/internal/marketfetch -run 'TestTimer(BatchClaim|Completion)|TestPeriodFailureReport'
go test -race -count=5 ./modules/storage/internal/service/datanode/pebble -run 'TestPeriod(Failure|CommitAccepted|CommitRejects|SeriesSnapshot|StatusQuery)'
git diff --check
```

### Task 9：活跃文档、完整门禁与协调发布运行手册

**Files：**
- Modify: `docs/内置市场行情采集架构.md`、`docs/采集任务管理.md`、`docs/architecture/collector-task-result.md`
- Modify: `modules/collector/README.md`、`modules/storage/README.md`
- Modify: `docs/架构总览.md`、`scripts/test/contract/test-docs-architecture.sh`，只修复已核验的 Workspace 模块清单及其数量断言漂移
- Modify: `docs/策略模块架构设计.md`，仅补回当前发布事件 `LogicalAccountTargetWeightRequested` 的精确名称，不改 Strategy/Trade 行为
- Modify: `scripts/deploy/deploy-moox.sh`、`scripts/test/contract/test-deploy-moox-gateway.sh`，修复合法空可选 Trade placement 导致 overlay/Admin 启动 `read` EOF 退出；非法配置仍须拒绝
- Modify: `scripts/test/contract/test-storage-datanode-management-contract.sh`，将过期的 Storage Schema v10 断言同步到当前 v12，并校验远端 DataNode fixture binding、默认业务 Fields 以及 Storage lifecycle E2E 覆盖
- Modify: `scripts/test/contract/test-build-storage-linux-contract.sh`，将 GOTMPDIR 远端命令断言同步到当前实际生成的 assignment 语法
- Modify: `modules/cli/internal/command/collector_task_purge.go`；Create: `collector_task_purge_proxy_test.go`，修复 Workspace vet 暴露的 protobuf 锁浅拷贝及嵌套 Page 共享，不执行 purge
- Modify: `modules/storage/cmd/cli/main.go`、`modules/storage/README.md`；Create: `modules/storage/cmd/cli/period_inventory_test.go`、`modules/storage/internal/service/datanode/pebble/period_inventory.go`、`period_inventory_test.go`，提供 fail-closed Pebble 只读周期盘点
- Modify: `packages/tradeeventpb/trade_events.proto` 及生成文件、直接依赖的测试 fixture（包含 `reporter_test.go`、`target_timeout_test.go` 的 helper 调用），移除无生产调用的历史兼容消息/字段，不改 Trade store 业务字段；新增 descriptor 回归，不放宽 greenfield 门禁
- Modify: `modules/strategy/internal/trigger/modern_test.go`，仅将已过 7 天去重 TTL 的固定旧日期 fixture 改为当前时钟的相同 17 分钟过期距离，不改生产 TTL 或拒绝/确认断言
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`，修复 Workspace vet 发现的 runtime range 值拷贝 atomic.Bool，沿用相邻调度循环的索引取指针方式
- Modify: `web/src/views/collector/task-instances/task-instances.vue` 及测试、`scripts/check/check-collector-planned-node-removal.mjs`、`web/scripts/check-collector-task-style.mjs`，以实际 11 列合计 1600 校准 scroll 与两份断言，保留全部禁用 token 检查
- Create: `docs/ops/collector-period-universe-release.md`
- Modify: 本计划，逐项附实际执行证据，不提前勾选

- [x] **9.1 更新活跃文档。** Collector/Storage README、采集架构、任务管理和 task result 架构文档现已描述周期标的池快照、Timer Claim、Completion、Invoke retry、权威回执与 deadline；明确首次失败可能 missed 且不假 ACK。
- [x] **9.2 复核唯一列表刷新入口。** 搜索调用点确认外部列表刷新只在 subject-sync runners；Scheduler/reconciler 使用 Metadata `ResolveSubjects`，没有残留刷新调用。
- [x] **9.3 执行模块级和跨模块门禁。** 原生 macOS arm64 + CGO 环境下全量 `make verify-pr` 成功，包含 proto clean-tree 检查、event/greenfield 合同、EventBus、Storage view capacity、Factor View-ready、DataNode management 和 SCF package gates。`CGO_ENABLED=1 bash scripts/test/contract/test-go-workspace.sh` 全 Workspace 测试与 vet、module boundaries、architecture/Gateway/SCF 文档与包门禁、空库 Schema 加载、Collector period process E2E、五个入口 build、focused race 测试及 `git diff --check` 均通过；过程中的三处旧合同/本机架构假失败已按当前实现修正后复跑。最终 Storage 浏览器 fixture/默认 Space opt-in 补强提交 `22ba03cd` 后，再次运行 `env GOARCH=arm64 CGO_ENABLED=1 make verify-pr` 成功；其 CLI 命令单测、Storage contract 和 Playwright `--list`（3 项）亦通过。增量 Storage resolver/deadline 修复提交 `51209c7a` 后，`env GOARCH=arm64 CGO_ENABLED=1 make verify-pr` 再次通过，随后通过正式 `moox-cli setup build-linux --module storage` 在 compile host 为同一 commit 构建并校验下载 Storage Linux 二进制。结果仅证明本地门禁和构建，不代表正式发布。

本轮文档检查已发现既存前置失败：`make test-docs-architecture` 报 `docs/架构总览.md missing go.work module: tools/moox-mcp`；当前 `go.work` 实际含 55 个 module，脚本仍要求 54 个。实施时先同步真实模块清单和严格数量断言，保留逐模块覆盖检查；不得删除检查、跳过该门禁或把当前结果记为 PASS。

同一门禁还要求 Strategy 文档出现 `LogicalAccountTargetWeightRequested`，当前文档仅写“完整目标权重事件”；该名称由 `packages/tradeeventpb/trade_events.proto`、`packages/events/registry.go` 和 `modules/strategy/internal/trigger/processor.go` 的实际发布路径共同确认。只补文档名称，不改交易协议。Gateway 部署门禁的固定健康地址断言与实际 `gateway_health_addr` 已漂移，gateway-only fixture 也必须显式关闭独立的 Storage Access、HostAgent；修复测试 fixture 后重新执行完整脚本。不得放宽部署脚本的凭据、组件依赖、健康检查或回滚保护。

独立工作区的实跑还定位到两处真实部署缺陷：`patch_configs` 和生成的 `start_admin` 在合法空可选 `trade-gateway.json` 时，Python 不输出、`read` EOF 返回 1，触发 `set -e`。按现有 `import_trade_owner_route` 的模式捕获 producer 结果与退出码，仅合法空配置跳过 `read`；半缺失字段、非法 JSON/URL/node 仍 fail closed。用生成 Admin placement block 与真实 overlay 的空/合法/非法 fixture 做红绿回归，不能填假 Trade node 掩盖缺陷；这是门禁前置修复，不改变 Strategy/Trade 业务规则。

2026-10-01 最终 `make verify-pr` 执行中发现本机登录 Shell 默认 `GOARCH=amd64, CGO_ENABLED=0`，Factor E2E 的 macOS arm64 DuckDB bindings 因交叉目标选择失败；以 `GOARCH=arm64 CGO_ENABLED=1` 单独重跑该脚本通过。DataNode management contract 仍要求旧 Schema v10 和已从 remote UI 场景删除的 Dataset 页面断言，且 Storage Linux build contract 将无语义差异的 GOTMPDIR 赋值要求成带引号文本；三项断言已按当前 schema、UI 场景及 shell 实际参数更新，各自目标通过。随后 `env GOARCH=arm64 CGO_ENABLED=1 make verify-pr` 完整成功，`proto-check` 确认生成无漂移且工作树干净。

```bash
go -C modules/collector test -count=1 ./...
go -C modules/storage test -count=1 ./...
go -C modules/gateway test -count=1 ./...
go -C modules/admin test -count=1 ./...
go -C modules/cli test -count=1 ./...
go -C modules/cloudnode test -count=1 ./...
bash scripts/check/check-module-boundaries.sh
bash scripts/test/contract/test-go-workspace.sh
make verify-pr
make test-greenfield-contract test-gateway-deploy test-collector-scf-package-contract test-docs-architecture
make test-collector-period-universe-e2e
go build ./modules/collector/cmd/subject ./modules/collector/cmd/server ./modules/collector/cmd/scf/market_data ./modules/storage/cmd/server ./modules/gateway/cmd/server
git diff --check
```

- [x] **9.4 验证所有 Schema 可载入空库。** 全部 `modules/**/schema/*.sql` 经 sqlite3 空库加载成功；Collector 初始化/必需 schema 校验测试通过。没有增加启动时自动迁移。

```bash
while IFS= read -r schema; do
    sqlite3 :memory: ".read $schema" || exit 1
done < <(rg --files modules | rg '/schema/[^/]+\.sql$')
```
- [x] **9.5 执行独立代码审查及 proto-check。** 两个独立 `codeCR` Agent 分别复审 Storage finalizer 和 Collector/SCF 最近修改；另有 `codeCR` 独立复审 Storage browser fixture/env contract follow-up。主 Agent 核对文件/符号/测试证据，修复 Storage contract 覆盖断言与继承环境变量两项 P2，最终复审无 P1/P2。已在提交后执行 `make proto-check`，生成无漂移且工作树干净。2026-10-01 增量复核中，Storage codeCR 对生产 resolver 绑定 DataNode ID 与正 deadline 校验未发现 P1/P2；Collector/SCF codeCR 对 Timer/Claim/Completion/retry/回执/清理链路未发现 P1/P2。新增差异已由主 Agent复跑 Storage 四包 fresh/race 与 30 场景 period E2E；仅保留 8.8 所述 marker JetStream 投递 P3 证据边界，不作为本地实现阻断项。
- [x] **9.6a 编写协调发布运行手册。** `docs/ops/collector-period-universe-release.md` 已列明无兼容协议的维护窗口、暂停/排空门禁、构建与服务包、Gateway/operator-owned route 有界更新、SCF CA/凭据预算、停止条件、回滚与分层线上证据。需要由获准生产 inventory 填写的值保持空白，不伪造目标范围。
- [x] **9.6c 实现 Storage 只读 period inventory。** `moox-storage-cli period-inventory` 验证 Pebble layout、持久化 DataNode/store identity 与调用者 node ID，限定 Space/Dataset/frequency/period_time 闭区间；DataNode 持锁时拒绝读取；只输出周期元数据、快照规模、索引/marker/outbox 关系和全局扫描计数，不导出行情行或 Subject 明细，也不写业务数据。旧 `roster`、双 snapshot key、非 canonical/错误/重复/孤儿索引、不可发布 outbox key、marker payload/event ID/timestamp/bitmap 不一致及非法 degraded 状态均 fail closed，且校验错误脱敏。全局 link 命名空间离线流式扫描，周期结果上限 100,000 条。Pebble fixture、live-lock、离线只读不变性、CLI 测试通过；Storage 全量测试/build、inventory race、架构文档 contract、`git diff --check` 通过；新 `codeCR` 复审无 P1/P2，并修复其发现的重复诊断 P3。
- [ ] **9.6b 在全部前置条件获批后执行正式发布。** 用户已确认公开 CA 按计划打包，SCF 固定包路径为 `certs/eventbus-ca.pem`；本地实现、审查、门禁和 Storage Linux 构建已通过，增量实现提交 `51209c7a` 已推送。生产尚未发布。仍须取得受影响运行态表与 Pebble period key 的只读 inventory，并单独确认明确 Space/Dataset/时间边界、可验证备份与保留行情/任务对象的有界 schema 切换方案；未获该项确认前不暂停生产者、不修改生产配置或数据、不上线。
- [ ] **9.7 明确 Schema/Pebble 切换前置条件。** 即使没有活跃周期，旧 Collector 表结构和 Pebble snapshot JSON 名称仍不能自动兼容。运行手册列出受影响 Collector DB 与 Storage period key 的范围、备份、受影响运行态表按新 Schema 重建及有界旧 period state 清理方案；保存并核对任务定义、结果 Dataset/View 引用与已有行情行数据。任何清理/重建必须另行授权，不清空整个 Storage 行数据，也不把运行手册中的一次性受授权重建变成产品兼容迁移。当前 `collector task purge` 会调用 DeleteView/DeleteDatasetRows/DeleteDataset，不可作为本次“保留行情与任务”的切换工具；`reset-view-consumers` 也不能证明 period ledger 已处理。若没有可审计、受授权的切换方案就停止，不能以“已 drain”替代 schema 处理。
- [ ] **9.8 固定实际发布顺序及停止条件。** 停止旧生产者后，以同一构建版本准备 Storage/Collector/SCF/Gateway route 与受管理环境；验证新的四个 Storage RPC 和 Claim、caller ACL、Timer EventBus、总环境 <=4096、Invoke retry 容量后才恢复 Timer/规划。禁止“先升级 Storage、旧 Collector 继续运行”。所有节点/proto/SCF package 不一致、pending 漏报不可解释或新 Schema 初始化失败立即停止恢复采集。
- [ ] **9.8a 明确存量部署配置的受控应用与回滚。** Gateway 的真实来源是 Admin 中 active deployment，不是工作区 YAML；非空 `gateway_routes` 为 operator-owned，不会自动合并新默认值。运行手册要求先导出并备份各目标 node 的 `storage-primary`、Collector runtime deployment 及受影响市场 Dataset metadata，生成逐行差异，只修改本次 period/Claim route 和 owner 属性，保留其余 operator 配置。使用现有 service-deployment scoped import/update 应用，注意 import 会覆盖整份 `extra_config`，不得直接拿全量默认 seed 覆盖所有生产行。明确失败时恢复原行的命令及停采条件；等待 Gateway 实际 applied route hash 与 Admin snapshot 一致，正向验证四个 RPC + Claim、反向验证非 Collector 零上游调用，再恢复生产者。上线授权前仅准备命令和脱敏差异，不执行更新。
- [ ] **9.9 分层记录并完成线上验收。** 分别记录本地单测、race、真实进程 E2E、构建/打包、独立审查；正式发布后的运行记录另列机器/Gateway/包版本及 1m、1h 周期事件。当前代码完成不等于已部署，1m 本地 E2E 不等于真实市场 1h 已验收。本次最终完成条件是代码与全部门禁通过、编码后新 Agent 审查闭环、正式版本实际生效及真实 1m/1h 周期端到端证据齐全。缺任一证据都不宣称完成。
- [x] **9.10 提交、推送并核验范围。** 本轮 Storage resolver/deadline 及复核记录提交为 `51209c7a`，已推送到 `origin/codex/collector-period-universe-remediation`。提交后 `make verify-pr` 的 proto-check 确认生成代码无漂移、工作树干净；Storage Linux binary 由 compile host 为同一 commit 构建并校验下载。临时 `moox.toml` hardlink 已移除，提交不含凭证、数据库、临时 helper binary 或测试日志。此前实施提交仍保留在同一已推送分支。

## 4. 最终验收矩阵

| 场景 | 必须观察到的结果 | 主要验收入口 |
| --- | --- | --- |
| 同周期标签减少、下一周期变化 | 当前 snapshot/hash/index/count 不变，下一周期采用新成员 | Task 1、8 |
| 同 Subject 多 Provider/Source | expected_count 按 series 计，Universe/failed_subjects 按 Subject 去重 | Task 1、2、8 |
| 同 Subject 错绑 storage series/index | Subject/tag/周期完整匹配；错绑及同 row 冒用两个 index 均拒绝且零 bit 副作用 | Task 2、8 |
| 生产 resolver 与默认 Gateway | Ensure/Commit/Record/GetStatus 均可达，不靠直接 DataNode 注入 | Task 3、8 |
| 只读/非 owner Ensure | 拒绝、零 period 初始化副作用 | Task 3 |
| 非 Collector period 调用或未知归属 Dataset | 四个 RPC 的 Gateway ACL 与服务授权均拒绝，不因 helper 缺规则放行 | Task 3 |
| 初始请求及 3 次重试失败 | permanent_failed，不改标签、不提前 finalize，其他标的继续 | Task 5、7、8 |
| 截止前失败、截止前迟到成功 | failure 清除、成功 bitmap 生效，可 complete | Task 2、8 |
| 恰好截止及之后首次失败 | missed_deadline；无锁顺序导致的 marker 差异 | Task 2、8 |
| 接受后 ACK 丢失、截止后恢复 | recorded/already_succeeded，可收敛 acknowledged | Task 2、5、8 |
| Commit 截止后首次写入/已成功重放 | 前者不能形成 worker success，后者按持久成功 bit 幂等通过 | Task 2、4、7、8 |
| 网络持续不可用 | pending + 逾期诊断，绝不假 ACK、不声称保证 marker 收录 | Task 5、8 |
| 部分 WriteTarget missed，其他未决 | 保留逐目标事实，聚合 pending；全确认后才最终归类 | Task 5、8 |
| Timer 双 tick/重启/午休/周末 | 同有效周期一个 initial manifest，无新 Run 追加 BatchItems | Task 6、8 |
| Timer 多周期积压与同 requestID 并发 | 有效旧周期优先、同 requestID 只绑定一个 batch；过期 planned 不永久悬挂 | Task 6、8 |
| Claim 不可达/no_work/身份非法 | 零抓取、零普通 Upsert；已有合法 Claim 可独立完成写入 | Task 6、7、8 |
| Timer Completion 身份非法 | Batch/Instance/Target/Retry 全部零副作用 | Task 6、8 |
| wrapped Storage/收盘后有效目标 | 真 period Commit；空 bars 不成功，当前非交易时刻不吞掉重试 | Task 7、8 |
| EventBus 发布失败、无可用 Invoke node | 先持久超时/重试，等待容量，不重新创建 initial 或无限 Timer 重试 | Task 6、7、8 |
| 30 天旧快照且 Storage 未确认终态 | 保留；只有匹配终态且无未决工作才有界删除 | Task 4、5、8 |
| deadline 后任意重复请求 | marker event ID/payload 不变，逻辑 marker/outbox 各一条 | Task 2、8 |
| SCF 实际合并环境 | 含 EventBus/Gateway/CLS/provider 的最终总字节 <=4096，凭证不入包 | Task 7 |
| SCF 凭据 JSON 重复键/空值/非法对象 | writer 构造失败、零 RPC、不回退主密钥或占位 app key | Task 7 |
| 任务持续增删 | 新增失败回执指标不含 Dataset/Task/Subject/retryKey 标签，诊断细节仍持久化 | Task 5 |
| 无兼容协调切换 | 旧生产者停止、状态和 Schema 前置条件确认，新版本全链路就绪后恢复 | Task 9；需单独上线授权 |
| 已有 Admin deployment 配置 | 有界差异更新、保留 operator 配置、实际 applied route hash 和正反向 RPC 验证通过 | Task 9；需单独上线授权 |

## 5. 交付证据要求

计划编写阶段在主 checkout 的完成清单全部未勾选；接续实施只在本实施分支按实际证据勾选 Task 1，其提交与独立审查见第 1.2 节和执行记录。其余中间实现不构成整体验收。既存门禁失败和独立工作区的部分验证分开记录，不引用它们证明本次全部验收通过。执行者对每个完成任务记录：实际修改文件、失败测试观察、修复后命令结果、独立审查结果、commit；不能只勾选步骤或引用旧测试成功。

本轮计划文档检查：`git diff --check` 为 PASS；`make test-docs-architecture` 为 FAIL，首个错误是 `docs/架构总览.md missing go.work module: tools/moox-mcp`，其修复与完整复跑已列入 Task 9。这不是产品功能测试，也不是上线验收。

本地验收报告至少列出 snapshot 身份、三个失败回执分类、Timer first batch 与 retry 次数、Storage 终态和 marker 载荷、清理前后记录数、真实 Gateway/adapter 经过证明。日志必须脱敏。

上线观察与本地执行报告分开保存；没有运行证据的门禁保持未勾选并说明阻断原因，不以构建成功推断生产链路达标。

本轮计划核验已分别由两位独立 `codeCR` Agent 检查 Storage 和 Collector 路径；主 Agent 随后核验了 RowKey/index 绑定、Collector owner 属性、operator-owned route 更新、两套 packager 与 runtime 凭据/CA 消费入口，修订已纳入相应任务。这是计划审查，不替代 Task 9.5 要求的编码完成后新 Agent 代码审查。

本轮最终计划复核还指出 Timer 积压领取顺序、普通 JSON map 无法查重、Dataset 标签无界三项；主 Agent 已核验 assignment、Storage 凭据入口、任务结果身份和标准 JSON decoder 的实际行为，分别补入 Task 6、7、5 及验收矩阵。没有增加另一个运行协议、迁移体系或监控子系统。

本次接续更新在主工作区 `06972d14` 上再次由 Storage、Collector 两路 `codeCR` 分别只读复核，均为 PASS、无新增阻断项；主 Agent 独立核验了生产 adapter、Ensure 授权、终态 Record 分支、Timer 请求与 Completion、Storage 包装器、失败适配器、清理 SQL 和任务结果身份约束。仅更新文档基线、分项实施状态与门禁前置文件清单，未继续业务编码；全部实施复选框仍未勾选。上述计划复核不替代编码完成后的独立审查或端到端验收。
