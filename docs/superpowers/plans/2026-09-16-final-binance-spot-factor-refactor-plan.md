# 最终改造执行计划：Dataset 周期能力与币安现货 1m 因子链路

> **For agentic workers:** 实施时使用 `subagent-driven-development` 或 `executing-plans` 逐任务推进；严格先做 T00，协议先落定再分工，最终代码审查必须使用新启动的 `codeCR`。本次仅更新计划，不授权本轮编码、服务启停或部署。

**Goal:** 在现有代码上补齐 Dataset 标的快照与周期完成、优化数据资产管理、重构因子计算输入输出，最终交付真实币安现货 1m 的时序因子、聚合和截面结果。

**Architecture:** Collector 同步 subject 快照；Storage KV 固定周期引用、完整提交时记成功并发布 DatasetPeriodCompleted。时序输入输出分离，聚合原始 K 线与时序结果形成 mdataset，截面直接读其 Primary；View 仅提供查询索引及 ViewDataReady，重建由用户手动触发。

**Tech Stack:** Go 多模块、tRPC、Protobuf、Pebble KV、JetStream、DuckDB、Python、Vue/TypeScript、Vitest/Playwright。

日期：2026-09-16。原始源码盘点基线为 `feature/mooyang@8afaaa60d724862cc652bb9491fc4c3257af838a`；当前隔离分支为 `feature/final-binance-spot-factor`。实现尝试所在提交为 `d5498de8`（父提交 `aadbc2db`）；当前 HEAD `54027eeb` 只更新了本计划文档，源码差异仍是 130 个已跟踪文件修改及 13 个未跟踪文件。它们包含本重构的未审查实现尝试，不能据此把任何任务标记为完成。执行前必须先完成 T00，逐文件识别、保全并验证这些改动，再决定如何继续；禁止 reset、checkout、clean、覆盖或重复实现。本轮仅按要求整理计划和进行只读审计，没有修改业务代码、启停服务或部署；下文记录了只读定向测试结果。“已有”只表示源码或工作树中可见，不表示通过 codeCR 或正式环境验收。

## 1. 唯一执行口径

已重新对照以下文档：
- ../specs/2026-09-13-factor-dataset-view-refactor-design.md
- 2026-09-13-factor-dataset-view-refactor-plan.md
- 2026-09-15-dataset-membership-factor-refactor-plan.md（含 9 月 16 日修订）

本计划作为最终实施入口。旧文档只提供背景，冲突时按下表执行，不同时实现两套方案：

| 议题 | 最终结论 |
|---|---|
| 术语 | subject、subject_id、subject_ids、subject_snapshot；items 仅指批量状态项 |
| 标的名单 | 全量快照按生效周期存储；无变化自动沿用；运行周期固定 snapshot_id |
| 快照写入 | Collector 定期获取有明确市场范围的全市场标的；派生生产者推导输出名单 |
| 状态权威 | 现有 Storage 磁盘 KV，不另建 SQLite 周期权威库 |
| 正常成功 | 完整数据、subject 成功状态、待发布记录同 KV 原子提交 |
| 失败上报 | ReportPeriod.items 仅 missing/failed，不再要求二次报告成功收据 |
| 周期事件 | Storage 发布 DatasetPeriodCompleted，代替生产方专用完成事件 |
| 时序触发 | Factor Engine 消费 Storage 的 DatasetRowsUpserted(input_commit)，不新增 ViewSourceSubjectReady，也不等周期全集 |
| 因子输出 | 写独立结果 Dataset，输入输出不同、依赖图无环 |
| 聚合 | 标准化各源周期预期 subject 后取交集，不取成功行交集 |
| 截面 | 消费 mdataset 的 DatasetPeriodCompleted，直接读 Primary |
| View | 每个 View 一个 Dataset，一个 Dataset 多个 View；不聚合、不驱动计算 |
| 重建 | 用户手动发起 A/B；保留追平、切换、恢复、读者排空 |
| 查询通知 | View 消费 DatasetPeriodCompleted，等相关写入位置应用后由 View 发布 ViewDataReady；Storage 完成与 View 可读是两个不同状态 |
| 缓存 | Dataset ID/schema 隔离，惰性回源，37m13s（2233 秒）检查，保留 N 行新文件重建 |
| 历史 | 不实现历史修正传播；重复投递、故障恢复、显式补算仍有边界 |
| 输入版本 | 不增加 input_contract_version；输入语义改变创建新 Dataset |

### RPC 与事件的对外语义

Storage 对外接口按短而稳定的业务名称收敛；以下是语义契约，最终字段按现有 RPC 风格落入 Protobuf：

```text
PutSubjectSnapshot(dataset_id, frequency, effective_period, request_id, subject_ids)
BeginDatasetPeriod(dataset_id, frequency, period)
GetPeriodSubjects(dataset_id, frequency, period, page_size, page_cursor)
CommitInput(commit_id, dataset_id, frequency, period, subject_id, complete_row)
ReportPeriod(dataset_id, frequency, period, request_id, items)
GetDatasetPeriod(dataset_id, frequency, period)
ListDatasetPeriodItems(dataset_id, frequency, period, state_filter, page_size, page_cursor)
```

`ReportPeriod.items` 每项只描述 `missing` 或 `failed` 的 subject 和原因；成功由 Storage 在完整 `CommitInput` 的数据写入批次内记录，不再接收冗长的成功项/提交收据参数。`GetDatasetPeriod` 只返回周期汇总；subject 状态通过分页 `ListDatasetPeriodItems` 查询。`DatasetPeriodCompleted` 只放周期状态/计数、snapshot_id 和写入位置，不塞入全量 subject 或失败明细，避免事件随市场扩大而超限。调用方不能自报 producer 身份或缩小必需字段集合。View 只有在索引实际追平后才发布 `ViewDataReady`。系统不新增 `MDatasetPeriodCompleted`、`ViewFactorPeriodReady` 或 `ViewSourceSubjectReady`，也不继续向外暴露旧 Collector/Merge/Factor 专用周期完成事件。

不因新项目允许重构而清理无关工作树内容。当前隔离 worktree 已有大量未提交修改；在证明文件归属及完整性前，禁止批量暂存、覆盖、回滚或删除。旧盘点还记录过 artifacts/storage-datanode-release-sha256.txt、modules/cli/tools/、web/test-results/ 等工作区改动，执行前重新核对，不能默认它们属于本任务或当前分支。

## 2. 代码盘点与改造落点

### 2.1 Storage、Collector 和 View

| 已核对入口 | 现状 | 实施决策 |
|---|---|---|
| modules/storage/internal/service/datanode/pebble/input_commit.go:63、70、98；modules/storage/internal/service/primarystore/input_commit.go:46 | CommitInput 有完整字段检查，数据、outbox、幂等收据同批写入；但必需字段由调用方 request 提供，尚不能作为可信成功判据 | 复用原 KV batch；Storage 改从已授权生产任务/schema 取 required fields，并同批写 subject success 与完成状态 |
| modules/collector/internal/marketfetch/period_readiness.go:95、186、217 | TaskInstance 形成名单，Collector 消费写事件确认成功 | 用 Storage 快照和提交成功状态替代完整性权威；保留采集任务诊断 |
| modules/collector/internal/store/period_readiness.go:26 | 每周期 SQLite 冻结名单 | 停止作为 Dataset 权威，不新建第二套相同账本 |
| modules/collector/internal/marketfetch/period_reporter.go:217 | 部分快照引用为占位字符串 | 替换为真实可解析的 KV snapshot_id |
| packages/storagepb/storage_events.proto:38、54、79 | Collector/Merge/Factor 专用完成事件仍活跃 | 原子切换生产者/消费者到 DatasetPeriodCompleted |
| modules/storage/proto/metadata.proto:75 | View 已为单 dataset_id | 复用模型，补一 Dataset 多 View 回归，不重复改协议 |
| modules/storage/internal/service/metadata/sqlite/crud_view_rebuild.go:17 | 已有手动请求 | 复用 UI/RPC 请求入口 |
| modules/storage/internal/service/view/maintenance.go:658、669、785 | coverage/schema/容量仍可自动申请重建 | 限制新任务准入，保留已授权任务恢复 |
| modules/storage/internal/service/view/data_ready.go:59、85；ready_fence.go:163 | 已有通用事件及持久 fence | 更换输入协议，复用等待与恢复，验证新周期提交位置 |
| modules/storage/internal/service/view/period_event_apply.go:130、177 | 仍接生产方专用完成 | 改为统一 Dataset 完成入口 |

在 `feature/mooyang` 源码基线中，PutSubjectSnapshot、GetPeriodSubjects、DatasetPeriodCompleted 尚未形成目标实现。隔离分支 `aadbc2db` 仅已有 Pebble 内部快照保存、按生效时间读取和固定周期引用；它没有对外 RPC、生产者授权、周期成功状态或完成事件。无论从哪个分支执行，T02 都必须先核对并复用这段实现，再补齐完整链路；不能把内部 KV 方法误认为已交付 Dataset 成员接口。

### 2.2 前端、默认配置与部署现状

| 已核对入口 | 现状 | 实施决策 |
|---|---|---|
| web/src/api/modules/system/static-menu.ts:54 | 数据采集与因子菜单已归并 | 保留结构，补功能，不重做搬菜单 |
| web/src/views/collector/datasets/index.vue:3 | 按 owner/role 过滤基础数据，复用浏览组件 | 保留，补快照和标准化导航 |
| web/src/views/data/datasets/index.vue:191 | 列、对象、索引页及默认 View 重试已有 | 增加 subject 快照与完整性，不重建详情框架 |
| web/src/api/storage/metadata.ts:234；views/data/views/index.vue:54 | 重建 RPC 已封装，操作列尚无重建按钮 | 接入确认、状态、日志和失败重试 |
| web/src/views/factor/bindings/index.vue:36；tasks/index.vue:21 | 仍以 source_view_id 为核心 | 改 Dataset 输入/独立输出/任务范围 |
| config/setup/collector-rules.yaml:33 | 默认现货 1m 写 dataset_binance_spot_kline_1m | 验收 RAW 优先引用该真实配置，不新建重复采集 |
| modules/cli/internal/command/setup_factors.go:56 | 默认因子仍绑定原始 View，无默认截面 | 与新任务配置一起更改 seed |
| modules/merge/config/merge-app.yaml:13；modules/factor/config/engine-app.yaml:37 | 默认是 spot+swap mdataset 链 | 本次新增明确现货原始+因子验收配置，不混用两套默认链 |
| scripts/build/build.sh:117；scripts/build/package-factor-engine.sh:49 | 独立 control/engine/merge 打包已有 | 复用并回归发布契约 |
| modules/factor/internal/integration/dataset_pipeline_test.go:25、82 | 有路由模拟测试，recordingRunner/假 Storage | 保留为快速测试，另补真实组件和数值 E2E |

View 的 index_build.snapshot_end 是索引构建进度，不是 Dataset subject_snapshot；前端不能把二者混为同一个“快照状态”。

### 2.3 因子引擎的复用与旧语义

| 入口 | 当前状态 | 计划处理 |
|---|---|---|
| modules/factor/internal/bootstrap/engine_runtime.go:29 | 独立 engine 已启动行变更和 ViewDataReady 消费 | 复用进程/资源边界，只替换目标消费协议 |
| modules/factor/internal/registry/metadata_sync.go:111；internal/rpc/service.go:829；internal/store/binding.go:183 | 绑定仍以 source_view_id 为入口并把输入/结果身份混用；result_dataset_id 虽已存在，尚未形成独立输入/输出契约 | T05 显式保存 input_dataset_id/output_dataset_id；View 只作为可选查询索引，不再替代 Dataset 身份 |
| modules/factor/internal/trigger/dataset_rows.go:87 | 已过滤 input_commit，factor_patch 不自触发 | 复用过滤和幂等，接入 RAW 完整提交 |
| modules/factor/internal/storageio/dataset_window.go:25 | 已有 Primary 精确键窗口读取 | 复用，补完整度/预算和输出 Dataset 场景 |
| modules/factor/internal/trigger/view_ready_runner.go:503、537 | 仍依赖 MergePeriodCompleted 类型的 ViewDataReady | T09 改 Dataset 完成及 Primary 面板，不另建平行截面引擎 |
| modules/factor/schema/factor.sql:181 | 本地 barrier/pairs 保存周期完成 | 保留执行明细，将 Dataset 完整性权威移到 KV |
| modules/factor/internal/bootstrap/engine_resources.go:86；modules/factor/internal/inputcache/ | Go-owned DuckDB read-through、代际切换及容量维护已有 | T07 先验证真实查询入口确实命中，再复用现有实现，不另造缓存模块 |
| modules/factor/internal/bootstrap/engine_runtime.go:47；modules/factor/config/engine-trpc.yaml:21 | `cache.enabled=true` 仍被启动校验拒绝，容量 Timer 配置仍关闭 | T07 修通注入和 37m13s Timer 后再启用；仅改默认 YAML 不算完成 |
| modules/factor/pyworker/worker.py:133；factors/catalog.json:1 | 三参数 ABI 已实现，内置 12 个因子皆时序 | 保留 ABI，新增明确截面 Rank 定义及测试 |
| modules/merge/internal/merge/assembler.go:149；consumer.go:60 | 多源前缀映射和 CommitInput 已实现，但 consumer 当前忽略 `write_kind=input_commit` | 复用 assembler/ledger；按显式输入 Dataset 允许 Collector/Factor 完整提交事件，防止 PANEL 自触发 |

仓库 setup 默认规则存在不等于实际环境已经启用；本轮没有读取线上生效配置。真实验收前必须查询规则状态和新数据，不能据示例配置宣称采集运行。

### 2.4 公共验证资产

- modules/factor/factors/Bias.py:1 已使用 compute(df, params, context)，计算的是 close/rolling_mean，不减一；min_periods=1。验收沿用其实际口径，不凭算法名称猜公式。
- modules/collector/internal/sources/binance/symbol_identity.go:9 的 ProviderSymbol 是交易所请求符号转换，不是通用跨 Dataset 标准化。不能直接拿去充当用户映射系统。
- modules/factor/test/storage_e2e_test.go 与 scripts/test/e2e/test-factor-storage-e2e.sh 属于合成数据/旧消费者链路；脚本要求部署服务且有可选重启，不覆盖真实 Binance Collector、独立 Engine、Merge 或目标 XS 链路。不得在盘点阶段执行，也不能把它们当成本次 E2E 证明。
- web/package.json 已有 test、check:menu、check:data-browse、build:prod；复用既有构建与测试体系。

### 2.5 计划刷新时点的代码审计结论

以下是对当前隔离 worktree（以 `d5498de8` 为代码差异基线，包括尚未提交的实现尝试）的只读核对结果。它们是开工前的风险清单，不代表当前实现已通过独立 codeCR 或正式环境验收；对应任务完成前必须加回归测试并由执行 Agent 重新核实：

| 优先级 | 现状与影响 | 执行要求 |
|---|---|---|
| P0 | `DatasetPeriodCompleted` 已注册并能由 Pebble 周期状态写入 outbox，但 View consumer 的 subject filter、订阅、dispatch、inventory reconcile 和 apply handler 尚未接入该事件。Storage 发出它不会自然产生 `ViewDataReady`。 | T01/T10 必须补齐从 Storage outbox 到 ViewDataReady 的完整链路；事件不携带整份大 subject 集合，只带 `snapshot_id`、汇总状态/计数与完成位置，View 用稳定分页接口恢复 subject IDs。 |
| P0 | `CommitInput` 只验证周期仍是 running，没有在 `deadline_unix` 到期时同步终结。deadline timer 延迟期间的最后一条迟到行仍可能把周期算成 complete。 | T03 在同一 `outboxMu` 临界区内比较 deadline；过期时原子登记剩余 missing、写 degraded 完成状态和 outbox，并拒绝新 commit。覆盖 deadline 前、恰好到期、到期后及与 timer 并发。 |
| P1 | Begin 周期目前冻结 snapshot、production task 和 mapping revision，但未冻结 owner、必需字段、字段类型/writer、schema revision/hash；Commit 仍可能读取可变元数据。 | T02/T03 冻结不可变 production contract 身份及其内容，不增加 `input_contract_version`；中途元数据变更只作用于未来周期，当前周期的授权与完整成功标准不变。 |
| P1 | `GetPeriodSubjects` RPC 目前一次返回整个 subject 列表，没有稳定分页。 | T01/T02 增加 snapshot 锚定、稳定排序的 opaque cursor/page size/next cursor；重试不得换快照，禁止以不稳定 offset 扫描动态名单。 |
| P1 | Pebble 周期记录把全部成功 subject、terminal items 和 report receipts 放在一个 JSON 记录；每次成功需重写增长中的周期对象，且快照/周期/receipt 尚无完整保留与 GC 设计。 | T03 按 subject 或固定块持久化状态并增量维护计数；定义幂等窗口、outbox、活动周期和可继承快照的保留边界，再实现有界 GC。不能为降写放大牺牲原子幂等。 |
| P0 | Merge 当前跳过 `write_kind=input_commit`，所以新的完整提交事件不能驱动其组装；名单还可能在首条到达时查询实时 active subjects，并仍消费旧 Collector 完成协议。 | T08 只接受配置的 RAW/TS 输入完整提交，按周期冻结的各源 snapshot 计算 canonical subject 交集；缺 snapshot 是等待/错误，不是空集。行到齐才一次 CommitInput，缺源到 deadline 则 missing，禁止残缺计算。 |
| P0 | Merge 没有为输出 Dataset 调用 PutSubjectSnapshot/BeginDatasetPeriod，真实 Storage CommitInput 会因周期未开始而拒绝；现有测试使用 fake committer，未覆盖这条真实链路。 | T08 在处理行之前先冻结 PANEL 预期名单并开始 Storage 周期；补真实 Storage RPC 集成测试，不能以 assembler 单测替代。 |
| P0 | Merge `Accepts` 不检查本地周期终态，现有测试 `TestMergePeriodLedgerAcceptsLateArrivalAfterCloseWithoutRewritingReport` 反而断言关闭后仍接受迟到行；`NoteCommit` 对终态静默返回，可能留下已发布 missing 但又写入数据的矛盾。 | T08 修改为终态后拒绝 arrival/commit，只发诊断；更正旧测试语义并覆盖 deadline 后事件、重启和并发迟到。 |
| P1 | Merge 来源完成账本键缺 frequency、snapshot/config identity；membership 首行路径读取实时 active subjects，缺 source snapshot 与合法空 snapshot 也可能混淆。默认 canonicalization 仍启发式删除 `-SPOT/-SWAP`。 | T08 使用源 Dataset + frequency + period + snapshot/config identity 的冻结输入；各源快照齐备后标准化求交集。映射缺失/冲突要显式失败，不用实时名单、成功行交集或后缀猜测。 |
| P1 | Merge custom 模式目前是 no-op，测试明确断言不提交；计划要求的自定义构造路径仍需遵守业务键、字段归属、幂等和终态报告。 | T08 定义 custom 程序的受控 Storage 提交与完成契约；system merge 未显式启用时不得静默 fallback。 |
| P1 | Collector 同名单刷新直接返回，且 request_id 由内容/生效周期确定，可能导致 Storage 的独立 freshness 不更新；标准化映射缺失时还会回退 provider subject。 | T04 分离不可变 snapshot 与同步 freshness 的写入语义；缺映射/冲突必须拒绝启用或报告，不能把 provider symbol 当 canonical ID。 |
| P1 | metadata `UpdateDataset` 将部分更新后的对象整体序列化，未保留新加的 production owner/task 属性；普通描述更新可能清空生产契约。列更新也没有冻结 WriterAppId。 | T01/T03 明确 metadata merge/update 语义；Begin 冻结 owner、required fields/types/writers 和 schema identity。增加部分更新不丢字段、周期中途变更只影响后续周期的测试。 |
| P1 | `DatasetPeriodCompleted` 当前 schema/validator 要求携带完整 missing/failed subject ID 列表，事件随市场规模增长；`GetDatasetPeriod` 也返回完整终态 ID，缺独立分页明细接口。 | T01 改成有界汇总事件和周期摘要；subject 明细通过稳定分页 API 查询。事件只带 snapshot 引用、计数/状态和所需提交位置，不携带全量 subject 或失败列表。 |
| P1 | Factor 输入/输出 Dataset RPC 改造尚未收敛：编译仍引用被删除的旧 View/Result 字段；trigger 测试夹具仍使用旧字段，事件到 task 的新映射没有通过回归。 | T05/T06 先统一 proto/domain/store/registry/RPC 的 input_dataset_id/output_dataset_id，再更新 fixtures。禁止为通过测试恢复旧字段或兼容别名。 |

周期状态的持久化结构还需要单独解决：当前一条 JSON KV 保存全部 succeeded IDs、terminal items 和 report receipts，每次 CommitInput 都重写扩大的周期记录并扫描终态；尚未发现 snapshot、period、request receipt 的完整保留/GC。T03 必须拆分 subject 状态或固定块并增量维护汇总，定义活动周期、幂等窗口、待发布 outbox、最新可继承快照及边界前驱的保留规则，再实现有界清理。原子幂等和恢复能力不能为降低写放大而牺牲。

只读验证记录（均为本地结果，不代表正式发布或端到端通过）：

- `packages/events`、`packages/storagepb` 全包测试通过；Storage DataNode/Pebble、PrimaryStore、SQLite metadata、View consumer/View、bootstrap 和 `cmd/server` 的定向测试通过。`modules/storage` 全模块结果仍需后续单独跑全量并解释基线差异。
- Collector 的 `internal/marketfetch`、`internal/marketstorage`、`internal/marketdata`、`internal/marketwiring` 定向测试通过；Merge `internal/merge` 测试通过，但其中迟到行测试通过的是与目标契约相反的行为，必须按上表修正测试和实现。
- Factor `env CGO_ENABLED=1 go test ./... -run '^$' -count=1` 当前不能编译：RPC converter 与 integration test 仍访问已移除的 SourceView/ResultView/ResultDataset 旧属性。`internal/trigger` 的 `TestDatasetRowsTriggerDedupesDuplicateSourceEvent` 与 `TestDatasetRowsTriggerDoesNotWaitForUniverse` 当前得到 0 个任务而非预期 1 个；需修正新 input Dataset 语义及测试夹具后重跑。
- Storage、Collector、Merge 的上述通过结果只覆盖定向包，不是所有模块回归。当前尚未完成独立 `codeCR`；此前因审查 agent 槽位已满未能启动。不得将这些检查记为最终审查。

T00 负责在实际编码开始前重新确认以上问题仍成立或已由现有未提交代码解决，并核对测试是否因后续变更而漂移。任何一项若已实现，必须以定向测试和 diff 证据标记为“已实现、待 review/验证”，不可静默删出执行清单；上述只读盘点不等同于 T00 的完整执行验收。

## 3. 可复现的验收拓扑

先使用专用验收配置，引用真实原始 Dataset，不改动其已有 ID。新增 ID 是建议值，创建前检查约束和冲突：

| 符号 | 建议资源 | 作用 |
|---|---|---|
| RAW | 从现有币安现货 1m 配置解析 | 原始已收盘 K 线 |
| TS | ds_binance_spot_bias_1m | 时序结果，输出 bias_20 |
| PANEL | mdataset_spot_bias_1m | RAW 与 TS 同键聚合 |
| CS | ds_binance_spot_rank_1m | 截面结果，输出 bias_rank |
| V_PANEL / V_CS | 所属 Dataset 的查询 View | 前端查询与可读验证 |

```text
RAW ──时序 Bias(20)──> TS
 │                     │
 └─────同周期聚合───────┘
            ↓
          PANEL ──截面 Rank──> CS ──View──> 前端
```

试运行选择 BTC、ETH、SOL 三个实际存在且已激活的资产做功能验收；启动前从实际源标的和标准化映射表解析 raw_subject_id/canonical_subject_id，不在计划里假定 `-SPOT/-SWAP` 后缀就是标准 ID。映射缺失或冲突时报清晰错误，不静默替换。基础 RAW 名单仍由 Collector 的完整市场快照管理，计算任务通过显式过滤得到这三个输出预期标的，不缩小全市场采集名单。

第一阶段三标的是端到端功能验收，不等同全市场吞吐验收；随后扩展到已激活现货范围，记录延迟与回源负载。

### 3.1 独立数值基准

```python
# 验收程序使用同一份已确认完整的原始 close 输入，独立计算预期值。
bias_20 = closes[-1] / (sum(closes[-20:]) / 20)
# 截面降序 dense rank：相同值同名次，下一个不同值名次加一。
levels = sorted(set(bias_by_subject.values()), reverse=True)
rank = {subject: levels.index(value) + 1
        for subject, value in bias_by_subject.items()}
```

要求每个标的有 20 根截至目标周期的完整、连续 1m K 线；这是验收预热要求，不擅自修改现有 Bias 的 min_periods 算法。时间统一为 UTC，只处理已收盘 K 线。当前 engine-app.yaml 的 period_boundary 为 close；T12 必须追踪 Collector 实际 data_time 和周期事件口径后固定一套 RAW/TS/PANEL/CS 共用规则，不能在本计划凭 K 线习惯擅改成 open。独立验算使用明确的目标 bar 与其前 19 根；series_tag 和计价范围固定。Bias 使用绝对/相对容差 1e-10，rank 整数精确比较；非法值、零均值或缺窗口不得写成功。

PANEL 的输入映射必须使用真实 RAW/TS ID 前缀，CS 将 TS 前缀的 bias_20 映射成算法输入列，不硬编码示例 Dataset ID。

## 4. 实施任务与依赖

每项遵循失败测试 → 最小实现 → 定向回归 → 提交；当前全部待验收。文件范围为仓库相对路径，新增路径存在时复用，禁止新建职责重复模块。

### T00. 现有工作树归属与实现盘点

依赖：无；这是所有编码任务的硬前置。范围：整个隔离 worktree，不改动文件。

- [ ] 记录 `git status --short --branch`、HEAD/上游提交、`git diff --stat`、未跟踪文件清单；将每个改动文件分类为本重构实现尝试、计划文档、生成代码或无关用户改动。
- [ ] 对本重构文件逐个阅读完整 diff，与当前提交和 9/15 方案对照；不要只看文件名或 diff stat。确认变更是否符合本计划、是否有未生成的 protobuf、是否存在测试失败或未接入调用路径。
- [ ] 对已有部分实现逐模块运行其最小定向测试并记录输出，标记“存在但未验证 / 局部验证通过 / 已由 codeCR 审查”；本任务起始的改动不得直接标记完成。
- [ ] 保留全部现有工作树内容。不使用 `git reset`、`checkout`、`clean` 或宽泛暂存。默认在 `feature/final-binance-spot-factor` 上增量继续；若必须切换基线，先逐文件保存和核验当前差异，确保不产生第二份 Storage/Collector/Merge 实现。
- [ ] 建立新的进度记录，逐项映射旧进度文档、本计划 T00-T13、当前 diff、测试和剩余工作。只有有测试及代码路径证据时才复用现成实现。

### T01. 固定协议和权威边界

依赖：T00。文件：packages/storagepb/storage_events.proto、packages/events/registry.go、validation.go；modules/storage/proto/metadata.proto、dataset_markers.proto、primary_store.proto、data_node.proto。

- [ ] 定义 PutSubjectSnapshot、BeginDatasetPeriod、GetPeriodSubjects、CommitInput、ReportPeriod、GetDatasetPeriod、ListDatasetPeriodItems；扩展 Dataset/生产任务元数据，保存唯一 production owner、频率、完整提交所需字段及字段所有权。Storage 将已认证 principal 映射到该 owner，不接受请求自报 producer 字符串；`data_node_id` 仅是存储路由，不得误当业务生产者身份。当前 Dataset 有单一 data_node_id owner，第一版以所属 DataNode KV 作为该 Dataset 周期状态的唯一权威，不先造跨 DataNode 汇总层。
- [ ] PutSubjectSnapshot 包含 dataset_id、frequency、生效周期、request_id、subject_ids 及标准化映射引用；BeginDatasetPeriod 原子冻结当期适用 snapshot_id 且重试幂等；GetPeriodSubjects 只查询，不隐式创建/冻结周期。GetDatasetPeriod 返回周期汇总，ListDatasetPeriodItems 分页返回 subject 终态和原因。
- [ ] `DatasetPeriodCompleted` 是 Storage 对任意 Dataset 的周期终态事件，携带真实 snapshot_id、周期、状态/计数及成功写入位置；它不是 View 已可读事件，也不携带全量 subject 或失败明细。View 消费它并确认索引追平后，由 View 模块发布 `ViewDataReady(completion_event_id, view_id, dataset_id, scope)`。因子截面直接消费输入 mdataset 的 `DatasetPeriodCompleted`，策略若查 View 则消费对应 `ViewDataReady`，两种就绪语义不得混用。
- [ ] View 的事件消费链路必须完整接入该事件：Storage subject filter、consumer subscription、delivery dispatch、handler、inventory reconciliation、fence 持久化及恢复。以 `snapshot_id` 分页恢复冻结 subject 列表，避免把大量 subject 放入事件；事件所带每个相关有序写入位置必须分别连续应用后，才能发布 ViewDataReady。
- [ ] 测试旧事件无注册、缺字段拒绝、不同 Dataset/周期不误关联、重复发布 ID 稳定；更新生成代码和全部调用方。
- [ ] 删除伪 scope 引用、input_contract_version 和旧专用完成协议，不保留兼容别名。

### T02. KV 快照与周期冻结

依赖 T01。主要落点：modules/storage/internal/service/datanode/pebble/subject_snapshot.go、subject_snapshot_test.go、period_state.go、period_state_test.go，以及现有 DataNode/PrimaryStore RPC 路由。先在当前分支及其 worktree diff 中复核已有 `PutSubjectSnapshot`、`GetPeriodSubjects`、`FreezePeriodSubjects`，验证可复用部分后只补缺口。`feature/mooyang` 仅作旧实现参考；无论源码入口如何，都必须补齐 RPC、认证授权和调用链，不能重复造第二套快照实现。

- [ ] 保存全量快照、生效索引、周期固定引用；同频率按 effective_period 查最近适用快照。同一 Dataset 最近快照的规范化 subject 集合未变化时复用现有不可变 snapshot_id，不为每个周期创建重复全量记录；将最近同步成功时间写入独立可变 freshness 状态，不改快照内容。request_id 重放返回同一结果。
- [ ] 无变化不写逐周期 UNCHANGED；BeginDatasetPeriod 内部操作串行固定引用，查询只读。业务进程必须在首行写入前显式开始周期，不能把第一条数据的到达时间当作成员冻结时间。
- [ ] 未有快照阻塞、合法空快照可完成；更新不改变已运行周期，迟到生效记录不回写固定引用。
- [ ] `GetPeriodSubjects` 分页锚定已解析的 `snapshot_id`，subject ID 稳定排序，opaque cursor 不暴露内部 KV key；同一 cursor 重放返回同一页，下一页不得切换快照。合法空快照返回 `found=true` 和空页，未找到快照不能与空快照混同。
- [ ] 测试关库重开、稳定分页、同请求重试、并发 Put/Freeze、同名单不重复存储、不同名单创建新快照、跨频率隔离。
- [ ] GC 同时保护周期引用、活动任务引用、最新可继承快照和保留范围边界前驱。

### T03. 原子成功状态与增量完成

依赖 T02。修改 input_commit.go、现有 outbox writer；新增 subject_state.go、period_complete.go 及测试。

- [ ] 扩展现有同 KV batch：数据 + subject 成功状态 + 待发布记录；不订阅自身字段广播，不要求成功二次 RPC。
- [ ] 从 CommitInput RPC 移除调用方声明的 required_fields，必需字段改由受 Storage 管理的 Dataset schema + producer task 字段所有权推导；不能由客户端提交缩减字段集伪造完整成功。RAW、TS、PANEL、CS 都走同一受控完整提交路径。
- [ ] Begin 时冻结 production contract：认证 owner/producer task、输出字段 ID/类型/writer、完整成功条件、schema revision 或 content hash。`input_contract_version` 不增加；后续 CommitInput 按周期冻结的 contract 校验，当前元数据变更仅影响后续周期。
- [ ] 按 subject 去重更新 succeeded/missing/failed；串行化或事务保护读旧状态到写新状态，原子 batch 本身不是并发计数锁。
- [ ] ReportPeriod 仅报告 missing/failed；禁止覆盖已成功或重开终态周期。
- [ ] terminal period 的迟到新 CommitInput 只记诊断并拒绝状态改写；deadline 前已成功的相同 `commit_id` 在响应丢失后重放仍返回原 receipt。新计算/显式补算必须有独立运行身份，不重开实时 period。
- [ ] 最后一项终态触发 CompletePeriod，周期状态与完成事件原子保存；timer 处理截止及重试，不每次全量扫数据。每次 CommitInput 都在同一 `outboxMu` 临界区比较当前时刻与周期 deadline；达到 deadline 时先原子将未终态 subject 标为 missing、完成 degraded 并写 outbox，然后拒绝该迟到 commit。不能依赖 timer 准时运行来保证截止语义。
- [ ] 分开配置成员快照等待超时、输入数据截止和计算执行超时；无适用 snapshot 时只记录等待/告警，不把 period 伪造成空集合 complete。
- [ ] Dataset 的 `data_node_id` 是周期状态权威归属；Primary 必须将成员、CommitInput 与 ReportPeriod 路由到同一 owner，错误 owner 拒绝。当前不设计跨 DataNode 成功汇总；只有 Dataset 改为跨节点分片时才扩展此协议。
- [ ] 将成功 subject、终态 subject 和 report 幂等收据按 subject key 或固定大小分块保存，周期摘要只保存计数/状态，避免每次成功重写整个增长中的 JSON 数组。定义 period、snapshot request、receipt、outbox 和快照的 retention/GC；必须保护活动周期、幂等窗口、待发布事件、最新可继承快照与边界前驱。
- [ ] 测试响应丢失、重复/并发写、恰好 deadline、deadline 后迟到、CommitInput 与 finalizer 竞争、数据缺列、contract 中途变更、owner 路由错误、状态已提交但发布失败；View 侧对每条相关有序流分别等待连续应用位置，不能比较不同流的单个最大序号。

### T04. Collector 和标准化

依赖 T02/T03。修改 modules/collector/internal/marketfetch/{instrument_pipeline.go,period_readiness.go,period_reporter.go,kline_pipeline.go}、internal/marketstorage/storage.go、internal/marketdata/subject.go；在 modules/storage/internal/service/metadata/sqlite/crud_subject.go 及对应 proto/API 增加通用标准化映射管理；增加 Storage 成员 RPC client 和领域测试。

- [ ] 市场列表定时获取；变化写全量，未变化复用原 snapshot 并更新同步成功时间，失败保留旧快照并告警。
- [ ] 每个 1m 周期在任何行写入前调用 BeginDatasetPeriod 固定 RAW snapshot；将 K 线从普通 UpsertFields 改为 Storage 授权的完整 CommitInput。Collector 不再消费自己的 DatasetRowsUpserted 事件来推导成功或提交位置；SQLite 仅保留采集调度/诊断所需状态，不再作为 Dataset 完整性权威。
- [ ] source_scope + raw_subject_id → canonical_subject_id，保留类型、交易所、计价信息；启用前预览冲突。
- [ ] 测试冻结前退市排除、冻结后缺数据记 missing、全市场 API 失败不写空列表。
- [ ] 同一源多个品种归一冲突拒绝；当前 ProviderSymbol 继续仅服务交易所请求。

### T05. 任务配置与输出隔离

依赖 T01/T02。修改 modules/factor/internal/domain/、store/、proto/、catalogsync/、registry/metadata_sync.go、schema/factor.sql。

- [ ] 在现有 factor binding/schema/proto 中显式分离 input_dataset_id、output_dataset_id、因子绑定、固定 subject 过滤、频率和配置快照；具体入口包括 modules/factor/internal/rpc/service.go、internal/store/binding.go、internal/registry/metadata_sync.go、schema/factor.sql、proto/factor.proto。
- [ ] 因子定义保存 `factor_type`（timeseries/cross_section）、输入字段、输出字段和 Python 入口名；类型由控制面目录与引擎读取，不塞入 `compute` 函数名或调用参数。因子结果 Dataset 明确字段 schema、subject 规则、所含因子定义及其输入基础/聚合 Dataset；View 仍是一 Dataset 的字段/数据范围子索引，不是因子 Dataset 的多源面板。
- [ ] 输入输出不同，依赖图无环；第一版一个输出 Dataset 一个生产任务，任务内多个因子。
- [ ] 启用后输入语义不可原地改，改变来源/行键/映射创建新 Dataset；配置更新不能让旧任务覆盖新结果。
- [ ] 持久化 task generation/config snapshot，使输入 Dataset、字段映射、因子参数或输出列变化后旧执行无法写入新输出；实时周期终态不可被重新打开。
- [ ] 在输出数据到来前建立输出周期名单：TS 从输入 Dataset 冻结快照应用任务过滤，Merge 从各源快照标准化后取交集，XS 从 PANEL 快照派生；先 PutSubjectSnapshot + BeginDatasetPeriod，再接受该周期结果行。成功行不能决定预期名单。
- [ ] 测试多任务一引擎、创建失败恢复、孤立资源重试、绑定启停及越权写入。

### T05b. 控制面与计算引擎部署职责

依赖 T05。文件范围：modules/factor/cmd/server/main.go、cmd/engine/main.go、internal/bootstrap/{control_resources.go,engine_resources.go,engine_runtime.go}、config/{app.yaml,engine-app.yaml,trpc_go.yaml,engine-trpc.yaml}；scripts/build/build.sh、scripts/build/package-factor-engine.sh、scripts/deploy/factor-engine/。

- [ ] 先复用并验证当前已存在的 `moox-factor` 与 `moox-factor-engine` 双入口，不复制第二个 Factor 服务或目录数据库。
- [ ] 控制面测试证明它无需 Python worker、不订阅实时计算 durable，仅维护定义/绑定/任务受理、目录发布和状态查询。
- [ ] 引擎测试证明其不注册 FactorMgr，只按已授权任务同步目录并订阅输入 Dataset；引擎→EventBus/Storage/控制面的连接采用内网出站，正式配置不得要求外网 Gateway 回连内网。
- [ ] 验证 durable consumer 的 stream/filter/durable 名称和首次启动策略；重启从持久 ack 位置恢复，不使用 DeliverNew 重放/跳过历史。
- [ ] 分别构建/打包 control、engine；配置、Python runtime、缓存目录和密钥隔离。密钥只由部署环境注入，不进入仓库或发布包。
- [ ] 该任务是对既有进程拆分的边界补齐，不代表外网控制面、内网引擎已部署或在线。

### T06. 时序路径改造

依赖 T03/T05/T05b。修改 modules/factor/internal/bootstrap/subject.go、internal/trigger/{dataset_rows.go,subject_tasks.go}、internal/taskrunner/、internal/storageio/{dataset_window.go,writeback.go}、engine/；复用已有入口和微批。

- [ ] 输入普通 RAW 的完整记录可触发，不仅限 merge 专属 Dataset；Collector 写入也采用受验证完整提交。
- [ ] consumer FilterSubjects 和 binding 选择统一按 input_dataset_id，而非现有 ResultDatasetID；只接受所属任务的完整基础输入事件，输出写 TS，不回写 RAW。周期调度器应可在首条输入行到达前根据输入 snapshot 开始 TS 输出周期；不能因整周期暂时没有行而漏掉超时终态。
- [ ] lookback 读 Primary，微批按 Dataset/频率/窗口组合，限制对象数、行数、字段数和并发。
- [ ] Python 保留 compute(df, params, context)，factor_type 在定义中；保留现有 Bias 数学口径。
- [ ] 把完整因子结果行作为 output Dataset 的受控 CommitInput 提交，由 Storage 从任务契约推导必需字段并原子记成功；不能沿用旧同 Dataset PatchFactor 作为新输出主链。缺历史和算法失败通过 ReportPeriod 报终态，不能丢 subject。
- [ ] 测试重复事件、早于周期完成的行、无未来数据、输出不回环、崩溃重试和完整字段要求。

### T07. 复用缓存并修正身份

依赖 T06。修改 modules/factor/internal/inputcache/、storageio/、bootstrap/cache.go。

- [ ] 复用既有代际/文件锁/重建实现，验证实际读数入口已接缓存，而非仅有单测工具。
- [ ] 按输入 Dataset ID/schema 隔离缓存，按 Dataset 元数据创建完整基础字段投影；新增因子依赖已有 Dataset 字段不改表。Dataset schema 改变才废弃旧代际并新建空 DuckDB；不对旧库在线 ALTER、不主动回填，也不把 View revision 当缓存身份。
- [ ] 冷缓存不预填，schema 变更新空库，完整性不足回 Primary，源确认缺失不无限循环。
- [ ] 真实 read-through 与故障降级测试通过后移除 engine_runtime.go 的 cache enabled 拒绝条件；默认开启与否不代替能力验收。
- [ ] `check_interval=37m13s`（2233 秒）、`rebuild_keep_rows=N`（N 从引擎配置读取）及容量上限均可配置；使用 tRPC Timer，启动后延迟首次检查。超限时在新 DuckDB 文件复制 `cache_updated_at DESC, stable_business_key` 最近 N 行，原子切换并等旧读者释放后删除旧代际。预算统计活动库、退役库、临时文件和 WAL；空间不足或新库仍超限时停止缓存写入并回 Primary。
- [ ] 测试大整数/JSON/null、读者排空、跨 Dataset 隔离、cache disabled、重建失败和目录残留。

### T08. 聚合原始与时序结果

依赖 T04/T05/T06。复用 modules/merge 独立 Go 模块、cmd/server 和 internal/merge/{assembler,consumer,periods,subjects,reporter}.go，替换名单来源；禁止在 modules/factor 下新建 cmd/merge。

- [ ] 聚合任务输入 RAW + TS。周期启动时读取每个输入 Dataset 在相同频率/period 上固定的 `snapshot_id`，按标准化规则求交集；只允许所有源快照均已解析后固定 PANEL expected subject 集合。不查询 live active subjects，不以成功行求交集，也不等待旧 `CollectorPeriodCompleted` 才确定名单。缺快照表示等待/告警，显式空快照才表示空集合。consumer 当前忽略 `write_kind=input_commit`，需按已配置输入 Dataset 放行 RAW/TS 完整提交，同时拒绝无关 Dataset 并防止读取 PANEL 自身输出形成回环。
- [ ] 对确定的 PANEL expected subjects 先 PutSubjectSnapshot + BeginDatasetPeriod，再接收行事件；周期状态固定所有输入 snapshot/config/mapping 身份。所有输入源同一个 canonical subject、frequency、period、series_tag 的必需字段齐全后才一次 CommitInput(PANEL)。任一源终态缺失或到 deadline，ReportPeriod 标 missing，不用残缺输入计算；membership 来自预期快照，不随缺行缩小。
- [ ] RAW 全市场与 TS 三标的相交只得三标的；其中 TS 某 subject 失败不从交集消失。
- [ ] 一 subject 同周期所有源完整后一次 CommitInput(PANEL)；持久化部分到达状态。交集以快照为准而非成功行：双上市对象即使 TS 或某源失败仍留在 expected 中并最终 missing；只在单边上市的对象不在交集中。
- [ ] 默认 system merge 仅在聚合 Dataset 明确选择系统聚合时启用：按 canonical subject + period 合并源行，输出字段名用来源 Dataset ID 前缀（`{source_dataset_id}_{field_name}`）；配置自定义构造时不暗中 fallback 到系统规则。进程内部可继续叫 Merge，控制台/API/事件对外统一称“聚合 Dataset”，不暴露 `MergePeriodCompleted`。
- [ ] 周期/对象账本所有复合键包含 Dataset、frequency、period、snapshot/config identity；频率错配显式拒绝。终态后的迟到输入只记录诊断，不保存 arrival、不调用 CommitInput、不更改完成事件。测试缺快照与空快照差异、同源冲突、先 TS 后 RAW、重复、重启、错频率、终态迟到和截止缺失。

### T09. 截面直接 Dataset 驱动

依赖 T03/T08。修改 modules/factor/internal/bootstrap/view_ready.go、internal/trigger/view_ready_runner.go、internal/taskrunner/、internal/storageio/；新增 modules/factor/factors/Rank.py，并更新 factors/catalog.json、pyworker/worker.py 与 factors/test_cross_section_factors.py。

- [ ] 仅目标 PANEL 的 DatasetPeriodCompleted 触发，默认 complete；degraded 按明确策略终止/报告，不静默缩小集合。
- [ ] task builder/storage reader 只接收 PANEL 的 input_dataset_id；删除把 source_view_id 误当 dataset_id 传给 Primary 的路径。CS 输出 View 为可选查询目标，不是截面输入。
- [ ] Primary 按固定周期和冻结 snapshot 批量读完整面板，验证对象范围、字段与 period boundary；优先复用现有 ReadPeriodChunks 类批量接口，不逐 subject 打回源 RPC；不等源 View。
- [ ] 因子 Python ABI 仍为 `compute(df, params, context)`；`factor_type=cross_section` 由定义表/registry 决定传入多 subject 面板。Rank 使用降序 dense rank，相同值同名次；输出唯一 subject 键到 CS，不在 Python 函数名或参数中重复声明类型。
- [ ] CS 预期名单固定继承目标面板规则；全部输出提交后由 Storage 发布完成。
- [ ] 测试无关 Dataset 事件、重复完成、并列/非有限值、输出越界、结果不自触发。

### T10. View 手动 A/B 与通用可读

依赖 T03。修改 maintenance.go、data_ready.go、ready_fence.go、period_event_apply.go；复用 rebuild RPC。

- [ ] 区分显式创建索引、用户请求新重建、已授权重建恢复；关闭 schema/coverage/容量自动新建 B。
- [ ] 实时索引写入继续；受影响字段删改仅标记差异，不静默修复或返回虚假成功通知。
- [ ] 未选择的新字段不算失配；失败保留 A，用户手动重试，切换后旧读者退出再删 A。
- [ ] 消费统一 Dataset 完成，WaitApplied 等所有相关有序流的连续应用进度；不能比较一个跨流最大序号。
- [ ] 测试 View 重建不影响时序/截面，旧 schema 不声称新字段可读，多 View 独立范围。

### T11. 前端数据资产归并

依赖 T04/T05/T10。修改既有 static-menu、Dataset/subject/因子页面与 API，不另建平行管理站点。

- [ ] 数据采集下整合数据源、采集标的及标准化、基础字段、采集任务、基础 Dataset。
- [ ] 因子模块管理任务与派生 Dataset，Dataset 详情提供上下游链接，不强制新增图形 DAG。
- [ ] 详情呈现数据、字段、标的快照、周期完整性、失败 subject、View 索引；“沿用快照”“无快照”“合法空”可区分。
- [ ] 配置输入输出/参数/映射，创建默认 View 复用幂等资源流程；保存失败给出明确原因。
- [ ] 重建按钮先展示差异和操作范围，再触发已有 A/B；状态、失败、重试和轮询完善。
- [ ] 新增页面/路由 Vitest 与 Playwright：直达刷新、空/加载/错误态、桌面与移动、权限和跨 space。
- [ ] 不机械删除原资产页面全部代码，复用已实现的管理组件和功能。

### T12. 预热与启用边界

依赖 T06/T08/T09。修改 setup_factors.go、engine-app 配置/验收 manifest、Merge 专用验收配置和 fixture；不改动或覆盖现有默认 spot+swap 示例配置，不直接在现网执行本步骤。

- [ ] 使用现有数据浏览/API 确认 RAW ID、字段、UTC 周期、subject、series_tag 和已有历史完整度。
- [ ] 新增独立验收配置，明确 RAW spot 输入、TS 结果、PANEL=RAW+TS 聚合、CS Rank 输出及三个 subject 的任务过滤；避免把当前 `mdataset_binance_kline_1m` spot+swap 默认链误当成目标配置。
- [ ] 选择 T0 为下一个明确 UTC 分钟，T0 前准备每个标的至少 19 根前序完整 K 线；T0 到来后总计 20 根。
- [ ] 优先等待自然采集积累；缺历史需显式授权走已有历史导入/补齐工具，历史导入不伪造实时完成事件。
- [ ] TS/PANEL/CS 从 T0 建立周期引用并准入；不要求先补算全部历史因子才能跑当前截面。
- [ ] 显式历史因子补算复用现有 Recalc 受理入口，但锁定独立 run identity 和结果 Dataset/版本，不覆写已完成实时周期；历史修改传播不在范围内。
- [ ] consumer 首次起点和 T0 协调；重启沿用 durable 积压，不重新 DeliverNew。

### T13. 构建、真实 E2E 和交付

依赖全部任务。

- [ ] 分别构建 control/engine/merge，验证 CGO、Python 依赖及独立配置/凭据/目录。
- [ ] 先运行本地真实 KV + NATS + Python 集成测试，再进入授权的外网控制面与内网运行部署。
- [ ] 新增 scripts/test/e2e/test-binance-spot-factor-pipeline.sh：默认只读预检和结果验证，必须显式指定部署根/环境；不隐式重启服务、补历史、创建/删除资源或改采集规则。缺依赖失败退出，不能将 SKIP 算通过；写入/部署操作由单独明确步骤执行。
- [ ] 记录连续至少 3 个 T0 之后的新分钟，证明 RAW→TS→PANEL→CS→View 可读；仅历史回算不算通过。
- [ ] 每周期核对三标的 snapshot、结果值、失败集合、关联完成事件和 ViewDataReady，不凭 UI 行数断言完整。
- [ ] 3 标的通过后扩大实际激活现货范围，记录耗时和失败，不以局部验收冒充全市场性能。
- [ ] 新启动 codeCR 最终审查，主 Agent 复现发现并修复重测；旧审查不代替本次最终审查。
- [ ] 生成结果报告和进度清单，记录实际 SHA、部署配置摘要及命令输出，隐藏秘密。
- [ ] 提交/推送本次范围；不能覆盖未授权文件或启动旧单体计算消费者。

## 5. 执行顺序、依赖与分工

推荐顺序：

1. T00 盘点当前分支和未提交 diff，复核本计划列出的审计问题，标记哪些实现可安全复用；未经此步骤不开始编码。
2. T01 固定 Storage RPC、事件字段、生产者身份和必需字段来源；先生成协议代码并冻结接口。
3. T02 完成 Pebble 快照 API 与 Primary/DataNode 鉴权路由；随后 T03 将完整行、subject 成功、事件 outbox 和 Dataset 周期状态打通。
4. T04 接入 Collector 快照同步及完整 K 线提交；T05/T05b 完成输入输出 Dataset 模型和进程授权边界。
5. T06 时序计算输出独立 Dataset；T07 启用并验证本地 DuckDB；T08 把 RAW 与 TS 聚合为 PANEL；T09 由 PANEL 完成事件触发 XS 并写独立 CS。
6. T10 改造 View 手动 A/B 与可读 fence；T11 优化数据资产 UI；T12 准备验收配置和历史窗口；T13 本地/正式环境验证、独立 codeCR 与交付。

共享代码所有权：Storage proto 与 `packages/storagepb` 由单一实现者先完成；Pebble 快照与周期状态由同一 Storage 实现者连续修改；Collector、Factor、Merge 只能在协议固定后并行，且不能同时修改共享生成代码。Merge 必须继续位于 `modules/merge` 独立模块，不要新建 `modules/factor/internal/merge`。View/UI 可在协议冻结后并行，但手动重建 RPC 的语义由 Storage owner 定义。

执行分支默认是当前 `feature/final-binance-spot-factor` 隔离 worktree。其 HEAD `d5498de8` 的父提交 `aadbc2db` 已含 Pebble 快照基础实现，而 worktree 又含大量未提交实现尝试；T00 必须先审阅这些差异并原地增量修正。`feature/mooyang@8afaaa60` 只作为旧源码盘点参考，不要再从它复制第二套实现。需要另开 worktree 时，先完整保全并清点现有 diff，再明确只由一个分支持有 Storage/Collector/Merge 改动。

阻断条件：找不到有效历史 snapshot、生产任务与 principal 无法建立唯一授权映射、Storage 无法从受管理 schema 推导必需字段、事件位置无法可靠对应 Dataset owner、部署版本/有效配置不能确定时，先补设计或部署调查，不以空集合、调用方自报字段或 `SKIP` 假装成功。

范围外：不交易、不下单、不改策略资金逻辑；策略只验证 DatasetPeriodCompleted/ViewDataReady 消费契约。第一版不做分布式调度平台、不支持历史数据修正传播、不自动回填本地 DuckDB；View 重建仍须用户手动触发，已授权任务可自动恢复。

## 6. 自动化验证命令

协议生成在仓库根目录执行；只运行受本次 Protobuf 修改影响的生成器：

```bash
make -C packages/storagepb generate
make -C modules/storage/proto all
make -C modules/factor/proto all
git diff --check
```

Go 是多模块仓库。每个模块分别进入目录验证，不能用根目录 `go test ./...` 代替：

```bash
cd packages/events && go test ./...
cd packages/storagepb && go test ./...
cd modules/storage && env CGO_ENABLED=1 go test ./...
cd modules/collector && env CGO_ENABLED=1 go test ./...
cd modules/factor && env CGO_ENABLED=1 go test ./...
cd modules/merge && env CGO_ENABLED=1 go test ./...
cd modules/strategy && go test ./...
```

持久化、并发、outbox 和消费者恢复测试至少对以下模块运行 race：

```bash
cd modules/storage && env CGO_ENABLED=1 go test -race ./internal/service/datanode/pebble ./internal/service/primarystore ./internal/service/view -count=1
cd modules/collector && env CGO_ENABLED=1 go test -race ./internal/marketfetch ./internal/store -count=1
cd modules/factor && env CGO_ENABLED=1 go test -race ./internal/trigger ./internal/taskrunner ./internal/storageio ./internal/inputcache -count=1
cd modules/merge && env CGO_ENABLED=1 go test -race ./internal/merge -count=1
```

复用现有模拟管线作为快速回归，但不能把它们称为真实 Binance E2E：

```bash
cd modules/factor && env CGO_ENABLED=1 go test ./internal/integration -run '^TestDatasetPipeline$' -count=1
cd modules/merge && env CGO_ENABLED=1 go test ./internal/merge -run '^TestDatasetPipeline$' -count=1
cd modules/factor/factors && python3 -m unittest discover -p 'test_*.py'
```

前端改动的最小验证：

```bash
cd web && pnpm test
cd web && pnpm run check:menu
cd web && pnpm run check:data-browse
cd web && pnpm run build:prod
cd web && pnpm exec playwright test tests/dataset-subject-snapshot.spec.ts tests/factor-pipeline.spec.ts tests/view-manual-rebuild.spec.ts
```

构建/发布契约验证：

```bash
bash scripts/build/build.sh factor
bash scripts/build/build.sh factor-engine
bash scripts/build/build.sh merge
bash scripts/build/build.sh web-host
bash scripts/test/contract/test-release-contract.sh
```

测试记录必须区分：定向单测、模块回归/模拟组件 E2E、正式部署健康、真实新周期数值 E2E。工具缺失、未执行、被跳过或只返回 UI 行数都不是通过证据；开始编码前记录已知基线失败，不把旧失败归咎于本次改动，也不因旧失败跳过新增测试。

## 7. 正式部署与真实数据 E2E

本计划不在本轮启动服务、创建线上资源或发起部署。所有正式操作放在实现、模块回归和独立 codeCR 通过之后，由实施阶段执行并逐项记录变更对象。不要直接运行旧 `test-factor-storage-e2e.sh` 作为本验收，它会访问部署服务且具备可选重启行为。

### 7.1 只读预检

- [ ] 记录目标环境当前服务版本、Storage DataNode/Gateway、EventBus Stream、有效 Collector rule、当前消费者 durable 和部署根；配置值可记摘要，凭据只记 secret 名称，不输出 secret。
- [ ] 通过正式环境 API/CLI 确认 RAW 的 Dataset ID、字段 schema、1m period boundary、series_tag、实际激活现货 subject 和至少 20 根连续 K 线。仓库 `collector-rules.yaml` 模板不能证明线上已启用。
- [ ] 确认 RAW 当前生产者 principal 与目标 Dataset owner；确认 event subject/ACL 放行 Collector、Merge、Factor Engine 各自所需操作。
- [ ] 查询 TS/PANEL/CS、View 和任务 ID 是否冲突；存在同名但 schema/owner 不一致的资源时停止，不覆盖、不删除。
- [ ] 预检输出缺少服务、权限、名单或事件时判失败并说明缺项；不得返回成功 `SKIP`，不得在预检自动补历史、改规则或重启服务。

### 7.2 资源与拓扑

以 BTC、ETH、SOL 三个资产做功能验收。预检必须从实际 Binance spot active 列表及映射配置解析每个 raw_subject_id 和 canonical_subject_id；若某项未激活、无映射或有歧义，就停止并明确列出，不能静默换 symbol 或靠删 `-SPOT/-SWAP` 后缀猜 ID。RAW 仍保持完整现货市场成员快照，三标过滤只作用于验收计算任务。

```text
Collector → RAW spot Dataset ──DatasetRowsUpserted──> 内网 Factor Engine
                    │                                  │
                    │                                  └→ TS result Dataset
                    └──────────── moox-merge ←──────────┘
                                      │
                                  PANEL mdataset
                                      │ DatasetPeriodCompleted
                                      ▼
                           内网 Factor Engine (cross-section)
                                      │
                                  CS result Dataset → CS View
```

`moox-factor` 控制面部署在外网控制节点；`moox-factor-engine` 在指定内网高性能机本地运行。引擎只发起到 EventBus、Storage 与控制面目录接口的必要出站连接；不为 Gateway 开放回连内网的入口。`moox-merge` 是独立进程，按实际 NATS/Storage 可达性部署，可与内网 Engine 同机但配置、账本、权限独立。部署前确认消费者 durable 不与旧版本冲突；不启动旧版计算 consumer 与新 Engine 双重消费同一任务。

资源 ID 建议（实际创建前以正式元数据查询结果为准）：RAW 使用现存 Collector Dataset；TS `ds_binance_spot_bias_1m`；PANEL `mdataset_spot_bias_1m`；CS `ds_binance_spot_rank_1m`；V_PANEL/V_CS 各自只关联一个 Dataset。创建过程必须幂等；不清理无关字段、Dataset、View 或历史数据。

### 7.3 周期级验证

- [ ] T0 选下一个明确 UTC 已收盘分钟；T0 之前确认每个验收 subject 有 19 根连续、完整历史，T0 到达后共 20 根。优先等真实采集；若需导入历史，执行阶段明确展示导入范围并取得授权，导入不能伪造实时完成事件。
- [ ] 核验该周期 RAW 的固定 snapshot_id、三个测试 subject 属于 RAW 期望全集，以及 Collector 每条 K 线经受控完整行提交写入 Storage Primary。
- [ ] 每个新 RAW input commit 触发且仅触发相应 TS 计算；从 Primary/DuckDB 读取不晚于目标周期的 20 根 close，计算 Bias，写到 TS 的同业务键；TS CommitInput 不再次触发自己的输入任务。
- [ ] Merge 根据 RAW 与 TS 的固定周期 snapshot 取标准 subject 交集。某个 TS 输入尚未到齐时保留 PANEL 的 expected subject，不按成功行缩小；所有必需源都齐后才一次写完整 PANEL 行。超时则 missing，不提交残缺 PANEL 行。
- [ ] 只有 Storage 发布 PANEL 的 DatasetPeriodCompleted 后，XS 才读取完整 Primary 面板；若状态 degraded，默认不对缩小面板计算。Rank 写 CS 独立 Dataset，不能写回 PANEL。
- [ ] 查询 CS View 并核对相应 ViewDataReady.completion_event_id；有任一相关提交位置未应用时，View 不得报告该周期可读。因子引擎本身不得依赖 View 就绪。
- [ ] 连续记录至少 3 个自然新周期，逐周期核对 snapshot ID、expected/success/missing、DatasetPeriodCompleted ID/status、各层业务键/列/值和最终 ViewDataReady。只用历史补算得到的行不计入这 3 个实时周期。
- [ ] 3 个 subject 通过后，扩大到实际 active spot subject 集合，记录时序触发到结果提交延迟、Merge 延迟、XS 执行时间、缓存命中/回源、Storage/NATS 错误和失败 subject；三标功能通过不等于全市场性能通过。

数值验收继续使用现存 Bias.py 口径：`bias_20 = close / mean(last 20 closes)`，并使用独立脚本从已确认的 20 个原始 close 计算，绝对/相对误差不超过 `1e-10`。截面 `bias_rank` 使用降序 dense rank：相同值同名次，下一个不同值名次加一；rank 整数精确比较。零均值、非有限值、窗口缺失和重复业务键应失败并留下终态原因，不能写成功结果。

### 7.4 交付证据

- [ ] 本地 Linux 目标构建与包校验通过，记录 `moox-factor`、`moox-factor-engine`、`moox-merge`、web-host 产物 SHA 和 Python runtime 版本。
- [ ] 正式部署后核对进程角色、版本 SHA、生效配置摘要、NATS durable ACK/lag、Storage 数据和服务健康；engine 进程在内网机本地运行。
- [ ] 新启动 codeCR subagent 对完整差异做最终只读审查；主 Agent 核实每个发现，修复后重跑受影响测试和发布包校验。
- [ ] 交付报告写明实际执行的命令、测试级别、3 个周期的证据索引、性能结果、未覆盖范围及风险；不包含 secret。

## 8. 故障与一致性验收矩阵

| 场景 | 必须结果 |
|---|---|
| Collector 市场列表获取失败 | 保留最后有效快照并告警；不能写空快照 |
| 成员集合未变化 | 沿用已有快照 ID；不生成重复全量快照 |
| 无适用历史快照 | 阻止启动该周期并给出可观测原因；不当作空集合 |
| 合法空快照 | 明确固定空快照，并可产生空集 complete 终态 |
| 快照写成功但响应丢失 | 同 request_id 重试返回同快照，不重复创建 |
| 同频率并发 FreezePeriod | 返回同一冻结引用；更新的快照不改已固定周期 |
| CommitInput required_fields 被调用方缩减 | Storage 忽略调用方声明并依据生产契约验证；不完整行拒绝成功 |
| 成功行提交后进程崩溃 | 行、subject success 与 outbox 一致恢复；重复投递不重复计数 |
| 并发同 subject 完整提交 | 每个业务键只产生一次终态转换和增量计数 |
| 周期终态后迟到数据 | 不重开实时周期、不改完成事件或输出；诊断记录迟到提交 |
| Collector 完成事件先于数据到达 | 不再依赖此旧事件；以行提交和 Storage 状态机完成周期 |
| 一源缺数据/超时 | expected 不缩小；上报 missing；不提交残缺 Merge 行 |
| 两源标的映射碰撞 | 映射启用失败并指出来源，不任意挑一行 |
| 双上市对象仅一源到达 | 继续等待另一源；到期进入 missing，不提前提交 |
| 聚合/Factor Engine 中途重启 | 持久 ledger/durable 恢复；不重复提交或遗漏终态 |
| 因子计算失败一个对象 | 输出 Dataset expected 不缩小，该对象是 failed |
| PANEL 周期 degraded | 默认 XS 不以残缺 subject 集合运行，不伪造 complete |
| DatasetPeriodCompleted 发布失败 | 持久 outbox 重试，事件身份稳定且下游幂等 |
| 无关 DatasetRowsUpserted / 完成事件 | 任务按 input Dataset/owner/filter 丢弃，不启动计算 |
| Factor 输出进入 Merge 输入 | 仅配置的 TS Dataset 被接收；panel 写回不触发自身回环 |
| DuckDB 文件损坏、满盘或超限 | 缓存弃用/停止填充并回源；不阻塞权威计算结果 |
| DuckDB schema 改变 | 新空代际；旧读者退出后清理；不在线 ALTER 或主动回填 |
| View 检测到 schema/coverage/容量变化 | 只报告差异/告警，不自动创建 B；用户请求后执行 A/B |
| View 一条相关输入流尚未追平 | 不发对应 ViewDataReady；不能拿不同流的最大序号互相比较 |
| Collector task/映射配置在周期中改变 | 只影响后续未冻结周期；本周期快照和输出保持原样 |

## 9. 执行产物与完成定义

编码阶段创建 `docs/superpowers/plans/2026-09-16-binance-spot-factor-progress.md`，逐任务记录复用路径、改动文件、红/绿测试命令、审查结论、提交 SHA、部署证据和剩余风险。完成定义不是“代码合并”或“文档复选框全勾”：Storage 通用周期能力、输入输出分离、真实 Collector→TS→PANEL→XS→View 链路、独立 codeCR、正式发布以及 3 个新分钟数值核对都需分别有证据。

本次计划刷新只修改本计划文档；编辑前隔离 worktree 有 130 个已跟踪源码文件修改及 13 个未跟踪文件，均未在本轮回滚或覆盖。完成只读审计和文中列出的定向本地测试，但未修改业务代码、未运行正式环境操作、未创建线上资源、未部署，也未完成独立 codeCR。计划完成只代表执行拆解清楚，不代表任何目标功能已实现或正式环境已验收。
