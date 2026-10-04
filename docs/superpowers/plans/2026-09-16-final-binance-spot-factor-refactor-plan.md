# 最终改造执行计划：Dataset 周期能力与币安现货 1m 因子链路

> **For agentic workers:** 使用 executing-plans 逐任务实施；共享协议先落定再分工，代码审查使用 codeCR。本次交付仅为计划，不授权编码、服务启停或部署。

**Goal:** 在现有代码上补齐 Dataset 标的快照与周期完成、优化数据资产管理、重构因子计算输入输出，最终交付真实币安现货 1m 的时序因子、聚合和截面结果。

**Architecture:** Collector 同步 subject 快照；Storage KV 固定周期引用、完整提交时记成功并发布 DatasetPeriodCompleted。时序输入输出分离，聚合原始 K 线与时序结果形成 mdataset，截面直接读其 Primary；View 仅提供查询索引及 ViewDataReady，重建由用户手动触发。

**Tech Stack:** Go 多模块、tRPC、Protobuf、Pebble KV、JetStream、DuckDB、Python、Vue/TypeScript、Vitest/Playwright。

## Global Constraints

- 新系统无需历史兼容；协议、数据结构和代码可按本计划直接重构，不保留旧事件别名或兼容分支。
- 业务对象统一使用 `subject` / `subject_id` / `subject_ids`；生产任务由 Storage 元数据和已认证 principal 授权。
- Storage 磁盘 KV 是 Dataset 快照、周期状态和可靠发布记录的权威存储；不能用内存或另一份 SQLite 账本替代。
- 每个 View 只索引一个 Dataset；Dataset 可有多个 View。View 不聚合数据、不触发因子计算；重建由用户手动发起 A/B。
- 时序因子按输入 Dataset 完整行变更触发并写独立结果 Dataset；截面因子等待 PANEL Dataset 周期完成再批量计算。
- 因子输入缓存按 Dataset 身份和 schema 隔离；DuckDB 容量受限，37m13s 定时检查并以新文件保留配置的最近 N 行，不在线 ALTER 或主动回填。
- 计划编写阶段只允许修改计划文档和只读核对；只有在确有必要校准现状时才运行既有本地定向测试。本次交付不包含编码、服务启停、线上资源变更或部署。

日期：2026-09-16。主工作树代码盘点基线为 `feature/mooyang@6adfae2b`；其后的 `c7e72bad`、`d8d4a38c`、`6ade83ee` 为计划文档提交，未改变源码基线。实施候选工作树为 `.worktrees/final-binance-spot-factor`，分支 `feature/final-binance-spot-factor@d5498de8`；其中包含大量未提交的 Storage、Proto、events、Collector、Merge 改动，以及本轮之前开始的 Factor 局部改造。Factor 的 binding domain/schema/proto/store/registry 等已有一部分改为 `input_dataset_id` / `output_dataset_id`，但跨层转换、任务过滤、写回、截面消费仍未闭环；例如 `internal/bootstrap/subject.go` 当前优先用输出 Dataset 构造输入事件过滤，`internal/rpc/convert.go` 仍引用旧 View/Result 字段，`internal/storageio/writeback.go` 仍走 `PatchFactor`，引擎仍有 `ViewDataReady` 计算 consumer。候选分支不得被当成可直接验收的实现：未来先审查所有未提交差异、运行 Factor 定向编译/测试、修正残留契约，再在该分支增量推进，不得从主工作树复制第二份实现。Web 没有候选分支改动。当前用户明确要求先不编码；本轮只做只读盘点和计划文档修改，没有修改候选源码、运行测试、启停服务或部署。

候选工作树现有实现已超出前一版盘点：快照内容复用、请求幂等冲突、freshness、周期冻结和查询；`GetPeriodSubjects` 可区分“无快照”(成功响应、`found=false`) 与“合法空快照”( `found=true` 且 subject 列表为空)；`ReportPeriod`、成功状态、`DatasetPeriodCompleted` outbox；Primary 根据 request-scoped Metadata snapshot 验证 Dataset 生产 owner/task、字段注册及字段写入权，并从 active required columns 推导 required fields；Primary 公共 RPC 已移除调用方自报的 `required_fields`，DataNode 内部 `CommitInput` 仅接受 `storage-primary`；Merge 调用方已同步移除该参数。周期 deadline 已进入 BeginDatasetPeriod 契约，Pebble deadline index、到期 missing 收敛及 tRPC timer 已加入。Collector 局部输入提交已有 `TimerRequestFromEnv` 生成实时目标 `target_data_time`、realtime 请求校验、仅对 `data_time` 精确匹配目标周期的行调用 `CommitInput`，其他返回 bars 仍走 bulk Upsert；`storageWriter` 已增加带认证信息的 DatasetPeriod RPC adapter，并保留 `CommitInput` 能力穿过 reserved deadline wrapper。此前 `TestRealtimeKlinePipelineCommitsOnlyTheTargetBar`、`TestRequestValidateRequiresTargetDataTimeForRealtimeKlines`、adapter 认证测试和 Collector 三个定向 package 曾通过，但这些结果早于本次候选工作树新增的 Reconciler 周期冻结 hook，不能作为当前候选分支回归通过证明。

当前候选工作树已有 `Reconciler.Reconcile` 在 Timer 环境分片前调用 `freezeDatasetPeriods` 的接线，以及 `period_snapshot.go`。最新未提交改动已按 `{target Dataset, frequency}` 合并多个 `TaskGroup` 的 subject union，只向 Storage 冻结一次，再把冻结名单与各组可执行 ProviderSymbol 求交集；`groups()` 对已启用规则的解析、Dataset/subject 查询错误改为失败关闭，无法解析或反解的 active subject 不再静默缩小名单。相应新增测试覆盖同目标跨组 union、名单错误失败关闭和 ProviderSymbol 缺失；`crypto_subject_normalization.go` 复用现有 source-scoped `SubjectSymbol` 映射，把 ProviderSymbol 映射到 canonical subject，未映射时仅 trim/uppercase，不再剥离 `-SPOT`/`-SWAP`，并拒绝歧义映射。Collector 的 `marketfetch`、`marketstorage`、`marketwiring` 定向测试以及 Collector 全模块测试曾在该候选快照通过。该代码仍未提交、未独立审查；测试通过只证明局部约束，不能视为完整 T04 验收。下一步应先复核差异及测试断言，再验证各组 assignment 不会收到其他 Provider 的 subject、当前/预备周期边界、退市变化与失败场景；不要重写已有 Storage RPC，也不要把“Reconciler 已调用”视为 T04 完成。Factor schema/proto 已出现 input/output Dataset 新契约的部分改动，但跨层转换、输入过滤、写回与截面触发仍未闭环；Web 无候选分支改动。Merge/Storage 当前未提交改动仍需独立审查。本轮只允许文档更新，没有继续修改源码、测试、启停服务或部署。

候选工作树最近已记录的验证为：`modules/collector` 的 `go test ./...` 全模块通过；此前 `marketfetch`、`marketstorage`、`marketwiring` 定向测试通过；`modules/storage` 的 Pebble、DataNode、PrimaryStore、bootstrap 定向包通过；`packages/events`、`packages/storagepb` 和 `modules/merge` 部分定向测试通过。Factor input/output schema RED 测试属于早期状态，候选 schema 现已改变，不能再将那次失败当作当前结果；当前大规模 Factor 改动没有本轮回归证据，应在执行计划开工阶段先跑编译与定向包测试并记录实际失败。此前还记录过 `modules/storage/internal/service/metadata/sqlite` 的 `TestDatasetProducerContractAndColumnOwnershipRoundTrip` 通过。Storage 全模块测试曾观察到两处失败：`internal/bootstrap/metadata/TestDefaultViewInventory` 的 View 清单预期差异，以及 `internal/service/e2e/TestSeriesTagPrimaryEventActiveViewAndBackfillFlow` 的空 selector 返回行数差异；尚未在干净基线复现，不能定性为本次回归或既有失败。Storage 全模块、完整 race、Merge/Storage 全模块回归、Factor 全模块、前端测试与构建仍未完成。以上均是候选工作树的局部验证，不等于独立审查或整体通过；本轮没有运行测试。

最近还修正了 Primary 周期路由的 principal 别名判断：`collector` 与 Metadata owner `moox-collector` 应按既有 `sameApplicationPrincipal` 规则视为同一应用主体。先将测试 fixture 改为不同别名并确认 RED，再接入别名比较后目标测试 GREEN。该修复后的 `primarystore` 全包与 race 尚未重跑，因此后续执行从重跑开始，不把单个目标测试通过扩写成全包通过。

这些源码和定向测试只说明局部路径存在，不代表完整任务验收。Collector、Factor Engine 与 Merge 的 Dataset 快照/周期生产链路仍未端到端接通；Storage 原子状态、required-field 契约固定、deadline 竞争/恢复、View 连续应用位置及跨模块消费者语义仍需独立审查。全模块验证、前端验证、独立 codeCR、正式部署和真实 Binance E2E 均未完成。主工作树存在用户未提交文件 `artifacts/storage-datanode-release-sha256.txt`、`modules/cli/tools/`、`web/test-results/`，后续不得覆盖或暂存它们。本轮只更新计划并做只读源码审计，没有改实现代码、运行测试、启停服务或部署。文中所有任务复选框表示尚未完成端到端验收，不因局部代码存在而勾选。

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

不因新项目允许重构而清理无关工作树内容。盘点时已有 artifacts/storage-datanode-release-sha256.txt、modules/cli/tools/、web/test-results/ 改动，实施前重新检查，禁止批量覆盖或暂存。

## 2. 代码盘点与改造落点

### 2.1 Storage、Collector 和 View

| 已核对入口 | 现状 | 实施决策 |
|---|---|---|
| modules/storage/internal/service/datanode/pebble/input_commit.go；modules/storage/internal/service/primarystore/input_commit.go | 候选工作树已有完整字段检查、KV 状态/outbox 路径；Primary 已改为从 request-scoped Metadata snapshot 验证生产 owner、字段注册/写入权并推导 required fields，公共 RPC 不再接受调用方 required_fields | 审查 Metadata snapshot 时效/分页、字段 alias、空值及并发配置变化；证明数据、成功状态、周期完成与 outbox 原子边界，并覆盖所有生产者调用方 |
| modules/collector/internal/marketfetch/period_readiness.go:95、186、217 | TaskInstance 形成名单，Collector 消费写事件确认成功 | 用 Storage 快照和提交成功状态替代完整性权威；保留采集任务诊断 |
| modules/collector/internal/store/period_readiness.go:26 | 每周期 SQLite 冻结名单 | 停止作为 Dataset 权威，不新建第二套相同账本 |
| modules/collector/internal/marketfetch/period_reporter.go:217 | 部分快照引用为占位字符串 | 替换为真实可解析的 KV snapshot_id |
| packages/storagepb/storage_events.proto:38、54、79 | Collector/Merge/Factor 专用完成事件仍活跃 | 原子切换生产者/消费者到 DatasetPeriodCompleted |
| modules/storage/proto/metadata.proto:75 | View 已为单 dataset_id | 复用模型，补一 Dataset 多 View 回归，不重复改协议 |
| modules/storage/internal/service/metadata/sqlite/crud_view_rebuild.go:17 | 已有手动请求 | 复用 UI/RPC 请求入口 |
| modules/storage/internal/service/view/maintenance.go:658、669、785 | coverage/schema/容量仍可自动申请重建 | 限制新任务准入，保留已授权任务恢复 |
| modules/storage/internal/service/view/data_ready.go:28、59、85；ready_fence.go:41、74、94、163 | 已有通用事件及持久 fence；当前 fence 记录每条流的最大 sequence 并以 `applied >= required` 判定，尚未证明连续前缀已应用 | 更换输入协议；先确认底层每条流严格有序且无洞，否则改为 contiguous watermark/洞集合；再验证新周期提交位置 |
| modules/storage/internal/service/view/period_event_apply.go:130、177 | 仍接生产方专用完成 | 改为统一 Dataset 完成入口 |

在 `feature/mooyang` 源码基线中，PutSubjectSnapshot、GetPeriodSubjects、DatasetPeriodCompleted 尚未形成目标实现；候选工作树已有 Pebble/DataNode/Primary 快照和周期 RPC、required-field 权威推导、deadline finalizer、部分原子状态/outbox，并且 Collector 已在 Reconciler 分片前接入 `freezeDatasetPeriods`、调用 Storage 快照/Begin RPC。T01/T02/T03/T04 均不得从头重写：先审查现有未提交差异和生成代码，再以失败测试补齐协议语义、权限边界、deadline、并发/重启行为及真实 Reconciler 边界。周期 deadline timer 与成功提交/ReportPeriod 的竞争、重启恢复、空周期和异常索引仍需验证。当前 `BeginDatasetPeriod` 固定 subject snapshot/task/deadline，但尚未固定用于整周期的生产字段契约；Primary `CommitInput` 每次基于当前 Metadata columns 推导 required fields，因此必须增加不可变 contract revision 或等价快照，避免同一周期前后采用不同 required-field 判据。Collector hook 已接入不代表退市边界、定时器分片、故障关闭、目标 bar 和生产 ACL 已端到端通过。Factor 已开始 binding 输入/输出 Dataset 改造，但仍有旧 DTO/转换、错误输入过滤、PatchFactor 写回和 ViewDataReady 截面消费；Merge 尚未切换成 Storage snapshot intersection / DatasetPeriodCompleted 权威，并保留了周期终态后接受迟到提交的旧行为。T03 还需解决候选实现每次完整重写周期 JSON 的全量状态更新成本；不能把单点 Primary CommitInput 或 Storage 局部测试通过误认为 Dataset 周期能力已端到端交付。

### 2.2 前端、默认配置与部署现状

| 已核对入口 | 现状 | 实施决策 |
|---|---|---|
| web/src/api/modules/system/static-menu.ts:51-79 | 没有“数据资产”顶层菜单；数据源、数据对象、基础字段、采集任务、基础 Dataset 已在“数据采集”下，因子仍是独立菜单 | 保留现有入口，统一“采集对象/字段管理”用语；把缺失能力与数据边界补齐，不重做搬菜单 |
| web/src/views/collector/datasets/index.vue:3 | 按 owner/role 过滤基础数据，复用浏览组件 | 保留，补快照和标准化导航 |
| web/src/views/data/datasets/index.vue:191 | 列、对象、索引页及默认 View 重试已有 | 增加 subject 快照与完整性，不重建详情框架 |
| web/src/api/storage/metadata.ts:234；views/data/views/index.vue:54 | 重建 RPC 已封装，操作列尚无重建按钮 | 接入确认、状态、日志和失败重试 |
| web/src/views/factor/bindings/index.vue:7-18,35-68,82-120,170-245；web/src/views/factor/tasks/index.vue:21-43,77-115 | 绑定和补算仍以 Source View 选择任务范围；结果与策略也保留 View 耦合 | 同步改为 input Dataset/output Factor Dataset；View 只作为独立 Dataset 查询索引，策略明确引用输入/结果 Dataset 或其 View |
| config/setup/collector-rules.yaml:33 | 默认现货 1m 写 dataset_binance_spot_kline_1m | 验收 RAW 优先引用该真实配置，不新建重复采集 |
| modules/cli/internal/command/setup_factors.go:56 | 默认因子仍绑定原始 View，无默认截面 | 与新任务配置一起更改 seed |
| modules/merge/config/merge-app.yaml:13；modules/factor/config/engine-app.yaml:37 | 默认是 spot+swap mdataset 链 | 本次新增明确现货原始+因子验收配置，不混用两套默认链 |
| scripts/build/build.sh:117；scripts/build/package-factor-engine.sh:49 | 独立 control/engine/merge 打包已有 | 复用并回归发布契约 |
| modules/factor/internal/integration/dataset_pipeline_test.go:25、82 | 有路由模拟测试，recordingRunner/假 Storage | 保留为快速测试，另补真实组件和数值 E2E |

前端“数据资产”大部分已在“数据采集”内，不应再做一次机械迁移。尚需明确的边界：`web/src/views/data/datasets/components/dataset-column-panel.vue:65` 允许 FIELD/FACTOR/SYSTEM 列来源，基础采集 Dataset 必须限制为允许的基础字段；`web/src/views/data/import/index.vue:172,338-376` 是不在菜单中的直达 CSV 导入页，目前不按 owner/role 限定 Dataset 并直接 `UpsertFields`，必须纳入采集工作台或明确限制为基础 Dataset/字段并衔接完整行提交契约；Dataset owner/role 主要在前端基于 attributes 过滤，列表 API 缺少服务端筛选，不能把 UI 过滤当作写入授权；`module-attribution.ts:78` 对未知归属默认显示基础字段，应改为显式 unknown/不可编辑；`metadata.ts` 与 browse helper 还接受 `primary_dataset_id` 旧别名，目标统一为单一 `dataset_id` 后删除别名。已有 `data-collection-navigation.spec.ts`、`data-management.test.ts`、`factor-dataset-workflow.spec.ts` 应扩展“基础页不编辑因子输出”“CSV 不写因子 Dataset”与 API payload 契约断言。View 的 `index_build.snapshot_end` 是索引构建进度，不是 Dataset subject_snapshot；前端不能把二者混为同一个“快照状态”。

### 2.3 因子引擎的复用与旧语义

| 入口 | 当前状态 | 计划处理 |
|---|---|---|
| modules/factor/cmd/server/main.go:29-47；internal/bootstrap/control.go:61-140；cmd/engine/main.go:24-42；internal/bootstrap/engine_runtime.go:29-63,65-164 | 控制面已不跑 Python；Engine 有独立入口并持有 Storage/Python，但仍同时启动 DatasetRows 和 ViewDataReady consumer，缓存 enabled 仍被启动校验拒绝 | 复用双进程，不重做拆分；切开目录/任务受理和内网计算职责，替换错误 consumer/配置边界 |
| modules/factor/internal/registry/metadata_sync.go；internal/rpc/{service.go,convert.go,recalc.go}；internal/store/binding.go；schema/factor.sql；proto/factor.proto | 主工作树基线仍是 View/Result 身份；候选分支已将部分 domain/schema/proto/store/registry 改为 input/output Dataset，但 RPC convert/recalc、部分测试与 Catalog/绑定管理仍引用旧字段；Upsert 仍可能从输入推导并覆盖输出 ID；Control Registry 仅接受 `cross_section` | T05 先完成候选半成品的全调用链检查与定向测试；显式 `output_dataset_id` 是权威绑定值，不可从 input ID 推导覆盖；Control 接受并同步 `timeseries` 与 `cross_section`；Factor 输出为独立 Factor-owned Dataset，View 仅为可选查询索引 |
| modules/factor/internal/trigger/dataset_rows.go；internal/bootstrap/subject.go | 候选代码虽已改名为 input/output Dataset，但 `boundDatasetRowFilters` 和 `bindingDatasetID` 仍优先选择 `OutputDatasetID`，输入事件因此可能订阅/匹配错误 Dataset；task 构造及旧 View-ready 路径也未完全拆开 | T06 所有 DatasetRows consumer filter、binding 选择、读数和 task identity 必须统一使用 `input_dataset_id`；写回只使用 `output_dataset_id`；增加 RAW→TS 正反向过滤及不回环测试 |
| modules/factor/internal/storageio/writeback.go:95-118,152-247,293-333,363-395 | 主结果写回仍使用 PatchFactor/PrimaryPatchFactor，未通过 Storage 所有的完整输出行提交 | T06 改为输出 Dataset 的受控完整 `CommitInput`，required fields 由 Storage metadata 契约推导 |
| modules/factor/internal/storageio/dataset_window.go:25；internal/taskrunner/read_pipeline.go:407-430；internal/storageio/client.go:155-187 | 有 Primary 窗口读取，但 TaskRunner 在 Dataset 与 View 查询之间按旧 input contract 分流 | 复用 Primary window reader；TS 读单 subject history，XS 直接批读 PANEL/mdataset Primary，不经过 View |
| modules/factor/internal/trigger/view_ready_runner.go:206-230,248-315,317-420,503,537 | 截面任务仍由 ViewDataReady 选 binding 并读取 View/period marker | T09 仅用 Storage `DatasetPeriodCompleted` 驱动目标 PANEL；ViewDataReady 只通知索引可读 |
| modules/factor/schema/factor.sql:181 | 本地 barrier/pairs 保存周期完成 | 保留执行明细，将 Dataset 完整性权威移到 KV |
| modules/factor/internal/bootstrap/engine_resources.go:86；internal/inputcache/{manager.go,config.go,capacity.go,rebuild.go,generation.go}；internal/storageio/dataset_cache.go:40-53,121-183 | 已有按 Space+Dataset 隔离、schema 变更新代、DuckDB 读穿、容量计量、按更新时间保留 N 行与原子代际切换设施，但生产读取没有 `RegisterSchema` 调用，当前没证明查询会命中缓存 | T07 把受管理 Dataset schema/generation 接进生产 reader，先证明冷缓存回源及随后命中，再解除 runtime 拒绝 |
| modules/factor/internal/bootstrap/engine_runtime.go:47-53；modules/factor/config/engine-app.yaml:33-36；engine-trpc.yaml:21-27；internal/inputcache/config.go:11-30,54-82 | `cache.enabled=true` 仍被启动校验拒绝、Timer 配置关闭；默认参数已有 37m13s、keep 100k、容量 20GiB/5GiB headroom，首次检查延迟一个 interval | T07 保留确定的 37m13s 周期，确认 prime 秒级错峰和首次运行行为后接入 Timer；只改 YAML 不算完成 |
| modules/factor/internal/domain/factor.go:11-33；pyworker/worker.py:133-215；factors/catalog.json:1 | 定义已有 factor type/inputs/outputs/params/lookback；Python ABI 已是 `compute(df, params, context)`，但截面缺新定义/完整测试 | 保留 ABI，factor_type 留在定义/registry，由引擎选择单 subject 或多 subject 面板 |
| modules/factor/internal/rpc/recalc.go:19-117；internal/bootstrap/recalc.go:13-42,44-120；internal/store/subject_runs.go:43-196 | Control SQLite 已持久受理补算，Engine claim/report 并有 subject run ledger；补算请求仍按 Source View 过滤 | 复用现有账本与 claim/report；改为 Dataset 输入输出和独立 run identity，不得重开实时 Dataset period |
| modules/merge/internal/merge/assembler.go:149；consumer.go:60 | 多源前缀映射和 CommitInput 已实现，但 consumer 当前忽略 `write_kind=input_commit` | 复用 assembler/ledger；按显式输入 Dataset 允许 Collector/Factor 完整提交事件，防止 PANEL 自触发 |

仓库 setup 默认规则存在不等于实际环境已经启用；本轮没有读取线上生效配置。真实验收前必须查询规则状态和新数据，不能据示例配置宣称采集运行。

### 2.4 公共验证资产

- modules/factor/factors/Bias.py:1 已使用 compute(df, params, context)，计算的是 close/rolling_mean，不减一；min_periods=1。验收沿用其实际口径，不凭算法名称猜公式。
- modules/collector/internal/sources/binance/symbol_identity.go:9 的 ProviderSymbol 是交易所请求符号转换，不是通用跨 Dataset 标准化。不能直接拿去充当用户映射系统。
- modules/factor/test/storage_e2e_test.go 与 scripts/test/e2e/test-factor-storage-e2e.sh 属于合成数据/旧消费者链路；脚本要求部署服务且有可选重启，不覆盖真实 Binance Collector、独立 Engine、Merge 或目标 XS 链路。不得在盘点阶段执行，也不能把它们当成本次 E2E 证明。
- web/package.json 已有 test、check:menu、check:data-browse、build:prod；复用既有构建与测试体系。

### 2.5 Collector 周期快照的真实接入点

这部分是执行时容易接错的边界，按当前候选源码固定：

- 币安实时 Timer 的完整 Kline universe 在 `modules/collector/internal/marketfetch/reconciler.go:153` 的 `Reconciler.Reconcile` 中通过 `groups()` 构造；`groups()` 从启用的采集规则读取源 symbol Dataset 的 active subject，再按目标 Kline Dataset 与 frequency 归并、去重，见 `reconciler.go:872-941,967-1006`。周期快照应写到规则的目标 Kline Dataset，而不是 symbol/source Dataset。
- 必须在 Reconciler 得到完整目标 Dataset/frequency subject 集之后、`splitGroupsForEnvironment` 和 `BuildAssignments` 产生分片之前，提交全量 `PutSubjectSnapshot` 并 `BeginDatasetPeriod`。这保证每个周期只冻结一次全市场范围；snapshot 与 Begin 重试必须幂等，Storage 暂不可用时不得继续发布一个未冻结的实时分配。
- 不要把这两个调用放进单个 Timer SCF。Timer 环境变量 `MOOX_MARKET_FETCH_SUBJECTS` 只包含分给该实例的 shard；在 SCF 内首次 Begin 会把局部 shard 错误冻结成全市场宇宙。Collector 当前 Scheduler 配置为 `InvokeNonRealtimeOnly: true`（`bootstrap/bootstrap.go:395-406`），且 Reconciler 由协调 timer 周期调用（`bootstrap/bootstrap.go:508-519`）；实时接入优先落在 Reconciler，而不是误把 invoke Scheduler 当成 Binance 全市场实时入口。
- `KlinePipeline.Execute` 可为一个 subject 拉回多根 bar，候选工作树已有“仅目标 `data_time` 行调用 `CommitInput`、其他 bars bulk Upsert”的局部实现（`kline_pipeline.go`、`contracts.go`）；成功仍依赖 Storage 已 Begin 的目标周期、完整 required fields 和 authenticated principal。最新候选测试已覆盖分片前跨组 union、错误名单失败关闭和 ProviderSymbol 反解失败，相关三个 Collector package 定向测试通过；但这些未提交改动仍须审查。下一步验证各 Timer assignment 仍只包含自身可执行子集、当前/预备周期边界稳定，并测试退市发生在冻结前/后、目标行缺失、响应只有邻近历史 bar、认证失败与缺字段都不会误记成功。不得将 lookback/gap-repair bar 错记为当前周期成功。
- 采集重试中的临时失败不是周期终态。只在现有 completion flow 判定重试耗尽/永久失败后调用 `ReportPeriod(failed)`；没有成功提交且没有终态失败报告的 subject 由 Storage deadline finalizer 归为 `missing`。不可在每次 transient fetch failure 时提前报告 failed。
- Collector 旧 SQLite `PeriodReadinessService`、`PeriodReporter` 和 `CollectorPeriodCompleted` 是另一套完整性账本/通知。只有确认 View、Merge、Factor/策略消费者都已切换到 Storage 的 `DatasetPeriodCompleted` 后，才能停掉旧账本的完整性裁决和旧完成事件；采集 TaskInstance、`MarketFetchBatchCompleted`、重试及运行诊断继续保留，它们不属于 Dataset 完整性权威。
- 新测试落点至少包含：`modules/collector/internal/marketfetch/reconciler_test.go` 与 `period_snapshot_reconciler_test.go`（多规则/TaskGroup 同一 Dataset/frequency 只冻结一次全量并集、Timer 分片只保留对应子集、重复 Reconcile 幂等、名单源错误或单个 active subject 无法映射时不发布部分配置）、`kline_pipeline_test.go`（精确目标行、响应只有邻近历史行、批量历史仍保留）、`completion_test.go`（只有终态失败上报）及 `marketwiring/handler_integration_test.go`（实际 Binance spot 组合路径）。

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

### T01. 固定协议和权威边界

依赖：无。文件：packages/storagepb/storage_events.proto、packages/events/registry.go、validation.go；modules/storage/proto/metadata.proto、dataset_markers.proto、primary_store.proto、data_node.proto。

- [ ] 定义 PutSubjectSnapshot、BeginDatasetPeriod、GetPeriodSubjects、ReportPeriod、GetDatasetPeriod；扩展 Dataset/生产任务元数据，保存唯一 production owner、频率、完整提交所需字段及字段所有权。Storage 将已认证 principal 映射到该 owner，不接受请求自报 producer 字符串；`data_node_id` 仅是存储路由，不得误当业务生产者身份。当前 Dataset 有单一 data_node_id owner，第一版以所属 DataNode KV 作为该 Dataset 周期状态的唯一权威，不先造跨 DataNode 汇总层。
- [ ] PutSubjectSnapshot 包含 dataset_id、frequency、生效周期、request_id、subject_ids 及标准化映射引用；BeginDatasetPeriod 原子冻结当期适用 snapshot_id 且重试幂等；GetPeriodSubjects 只查询，不隐式创建/冻结周期。
- [ ] DatasetPeriodCompleted 携带真实 snapshot_id、周期、状态、失败集合和所需提交位置；ViewDataReady 关联 completion_event_id、view_id、范围。
- [ ] 测试旧事件无注册、缺字段拒绝、不同 Dataset/周期不误关联、重复发布 ID 稳定；更新生成代码和全部调用方。
- [ ] 删除伪 scope 引用、input_contract_version 和旧专用完成协议，不保留兼容别名。

### T02. KV 快照与周期冻结

依赖 T01。主要落点：modules/storage/internal/service/datanode/pebble/subject_snapshot.go、subject_snapshot_test.go、period_state.go、period_state_test.go，以及现有 DataNode/PrimaryStore RPC 路由。当前唯一实施分支已有 `PutSubjectSnapshot`、`GetPeriodSubjects`、`BeginDatasetPeriod`、`ReportPeriod`、`GetDatasetPeriod` 的 Pebble 与 RPC 基础实现，以及 Primary owner 检查/路由；实施时先确认这些代码和生成文件，再补充缺失语义与测试。不得重建第二套快照 API，也不得因当前候选分支已有部分代码而跳过 schema/权限/跨模块验收。

- [ ] 保存全量快照、生效索引、周期固定引用；同频率按 effective_period 查最近适用快照。同一 Dataset 最近快照的规范化 subject 集合未变化时复用现有不可变 snapshot_id，不为每个周期创建重复全量记录；将最近同步成功时间写入独立可变 freshness 状态，不改快照内容。request_id 重放返回同一结果。
- [ ] `GetPeriodSubjects` 必须显式区分不存在与空集合：无适用快照时 `found=false` 且不返回虚构的空宇宙；合法空快照时 `found=true`、返回稳定 `snapshot_id` 且 `subject_ids=[]`。查询不创建快照、不 Begin period。
- [ ] 无变化不写逐周期 UNCHANGED；BeginDatasetPeriod 内部操作串行固定引用，查询只读。业务进程必须在首行写入前显式开始周期，不能把第一条数据的到达时间当作成员冻结时间。
- [ ] 未有快照阻塞、合法空快照可完成；更新不改变已运行周期，迟到生效记录不回写固定引用。
- [ ] 测试关库重开、分页、同请求重试、并发 Put/Freeze、同名单不重复存储、不同名单创建新快照、跨频率隔离。
- [ ] GC 同时保护周期引用、活动任务引用、最新可继承快照和保留范围边界前驱。

### T03. 原子成功状态与增量完成

依赖 T02。修改 `modules/storage/internal/service/datanode/pebble/input_commit.go`、候选工作树已有的 `period_state.go`、`period_deadline.go`、现有 outbox writer 及对应测试；不要再新建一套重复的周期状态模块，只有确认职责已过载后才拆分。候选工作树已实现/接入部分原子 `CommitInput`、生产者 owner/schema 字段权威校验、`ReportPeriod` missing/failed 基础能力、deadline 索引和超时 finalizer。新增 `TestFinalizeDueDatasetPeriodsMarksUnfinishedSubjectsMissing`、Storage Primary 身份校验及 Metadata 持久化测试已通过；这些未提交实现仍不等于本任务完成，终态并发/重启验证、完成事件位置与 View 连续应用 fence、全部生产者接入及全模块测试仍待完成。

- [ ] 扩展现有同 KV batch：数据 + subject 成功状态 + 待发布记录；不订阅自身字段广播，不要求成功二次 RPC。
- [ ] （候选分支已移除 Primary 公共 RPC 的 required_fields，并从 Metadata snapshot 验证 Dataset owner/task、列注册和 writer_app_id，推导 active required columns；）确认 RAW、TS、PANEL、CS 的正式 Metadata 都配置完整生产 owner/task、required 列和 writer 权限，并通过各自生产者做端到端拒绝/成功验证。
- [ ] 在 `BeginDatasetPeriod` 固定不可变的生产契约 revision（或内容寻址契约快照），至少覆盖生产 task、必需字段及 writer 所有权；Primary 不能每次按可能变化的当前 Metadata 重新推导同一周期的 required fields。Metadata 契约变更只能影响后续周期；测试周期中新增/停用 required 列、变更 writer 和生产 task 的行为。
- [ ] 将 subject 终态改为按 `{space,dataset,freq,period,subject}` 独立 KV key 存储，并维护可原子更新的 complete/missing/failed 计数；CommitInput 不得每行 append/sort 全量 succeeded 列表或遍历所有 TerminalItems 后重写整份周期 JSON。Report receipts 与 deadline 索引也应有独立索引/键。对实际全市场 subject 数量做并发和写放大/延迟基准，证明最后终态只触发一次周期关闭。
- [ ] ReportPeriod 仅报告 missing/failed；禁止覆盖已成功或重开终态周期。
- [ ] terminal period 的迟到 CommitInput 只记诊断并拒绝状态改写；新计算/显式补算必须有独立运行身份，不重开实时 period。
- [ ] 最后一项终态触发 CompletePeriod，周期终态计数与完成事件 outbox 原子保存；候选分支已有按 deadline index 扫描到期项的 5 秒 timer 和超时 missing finalizer，仍需验证成功/失败/超时并发竞争、重启持久恢复、批量上限和 timer 错误可观测性，不每次全量扫描 Dataset。完成事件不得因完整失败列表导致超过 NATS 消息上限；必要时事件携带失败清单引用/摘要，消费者再从 Storage 查询。
- [ ] 分开配置 subject 快照等待超时、输入数据截止和计算执行超时；无适用 snapshot 时只记录等待/告警，不把 period 伪造成空集合 complete。
- [ ] Dataset 的 `data_node_id` 是周期状态权威归属；Primary 必须将 subject 状态、CommitInput 与 ReportPeriod 路由到同一 owner，错误 owner 拒绝。当前不设计跨 DataNode 成功汇总；只有 Dataset 改为跨节点分片时才扩展此协议。
- [ ] 测试响应丢失、重复/并发写、数据缺列、owner 路由错误、状态已提交但发布失败；View 侧对每条相关有序流分别等待连续应用位置，不能比较不同流的单个最大序号。

### T04. Collector 和标准化

依赖 T02/T03。修改 `modules/collector/internal/marketfetch/{instrument_pipeline.go,reconciler.go,reconciler_test.go,period_readiness.go,period_reporter.go,kline_pipeline.go,kline_pipeline_test.go,completion.go,completion_test.go}`、`modules/collector/internal/marketstorage/storage.go`、`modules/collector/internal/marketdata/subject.go`；在 `modules/storage/internal/service/metadata/sqlite/crud_subject.go` 及对应 proto/API 增加通用标准化映射管理；增加 Storage snapshot/period RPC client 和领域测试。按 §2.5 的 Reconciler→SCF→Storage 实际链路接入，不在单个 Timer shard 中冻结全集。

- [ ] 先审查候选工作树未提交的 `freezeDatasetPeriods` 与现有 RED/GREEN 测试，确认其已按 `{target Dataset, frequency}` 聚合所有规则/TaskGroup 的完整 subject union，并只对目标 Dataset/frequency 写入一个快照、为目标周期 Begin 一次；若断言有缺口，先补失败测试再修实现。将冻结 subject 集与 Provider/市场组分开：Storage 固定的是 Dataset 完整宇宙，每个 Timer assignment 仍只能拿到其 provider/source 可执行的交集；不得把 union 广播成某个 Provider 的任务名单。Storage 调用或名单校验失败时不发布任何不完整配置。
- [ ] `groups()` 不能在启用规则解析失败、source Dataset/ListSubjects 查询失败或 active subject 标准化失败时静默跳过并缩小全市场快照。区分“没有启用的 Kline 生产规则”与“无法完整读取已启用规则名单”；后者 fail closed，保留当前 Timer fleet 并告警。冻结名单中的每个 canonical subject 都必须可解析为需要执行该任务的 ProviderSymbol，否则失败，不允许 `symbolsForFrozenSubjects` 静默省略。
- [ ] 市场/交易品种列表定时获取并写入 symbol source Dataset；失败保留最后有效 source 集合并告警，不能写空集合。Reconciler 从活跃 source subject 解析 canonical subject，按目标基础 Kline Dataset + frequency 生成全量 `subject_ids`；快照写入目标 Kline Dataset。集合变化写新全量快照，不变时沿用 snapshot_id 并更新同步 freshness。
- [ ] 在任何实时目标行写入、Timer shard 发布之前，按 `{target Dataset, frequency}` 调用 `PutSubjectSnapshot` 与 `BeginDatasetPeriod` 冻结完整 RAW snapshot。目标周期必须使用全链路统一的 UTC period boundary；不因 Reconcile 重复、Collector 重启或部署重试而重新冻结不同名单。对同一个目标 Dataset/frequency 的多个 Group 只允许一次全量冻结；当前周期与预备周期的 Begin 语义、deadline 和变化生效边界必须有测试证明。
- [ ] Kline Pipeline 明确区分实时目标周期提交与历史/补洞批量写入：仅当返回数据包含目标 `data_time` 的完整行时，对该 subject 调用 Storage 授权的 `CommitInput`；漏掉目标行即不能以其他历史 bar 宣告本周期成功。额外历史 bar 可走既有批量 Upsert，不写入当前周期成功状态。
- [ ] Collector 不再消费自己的 `DatasetRowsUpserted` 事件推导成功或提交位置。`ReportPeriod` 只接收重试耗尽/永久终态的 failed subject；暂时错误保留重试，deadline 到期由 Storage 生成 missing。SQLite 仅保留采集调度、TaskInstance 与诊断所需状态，不再作为 Dataset 完整性权威。
- [ ] 完成消费者切换后停用本地 `PeriodReadinessService` / `PeriodReporter` 的 Dataset 完整性裁决及旧 `CollectorPeriodCompleted` 发布；先确认 View/Merge/Factor/策略不再依赖旧事件。采集批次完成、重试、指标和 TaskInstance 仍继续工作。
- [ ] 优先复用 Storage 已有 `SubjectSymbol(space_id, subject_id, data_source_id, external_symbol)` 做 source-scoped 显式标准化，不另造重复映射表/API；只有确认现有 CRUD/UI 无法表达“源范围 + 原始符号 → canonical subject”时才扩展。移除 `CanonicalCryptoSubjectID` 等通过剥离 `-SPOT`/`-SWAP` 猜测身份的逻辑；无映射时不同原始产品标识保持不同，显式映射冲突或一个输入无法唯一归一时失败关闭。启用前提供冲突预览，并保留交易所 `ProviderSymbol` 仅用于请求路由。
- [ ] 测试冻结前退市排除、冻结后缺数据记 missing、全市场 API 失败不写空列表。
- [ ] 测试 Reconcile 分片前快照完整性、SCF 不自行 Begin、同周期重试幂等、target period 精确行提交、仅终态失败上报，以及历史 Upsert 不污染当前周期终态。
- [ ] 同一源多个品种归一冲突拒绝；当前 ProviderSymbol 继续仅服务交易所请求。

### T05. 任务配置与输出隔离

依赖 T01/T02。修改 modules/factor/internal/domain/、store/、proto/、catalogsync/、registry/metadata_sync.go、schema/factor.sql。

- [ ] 在现有 factor binding/schema/proto 中显式分离 input_dataset_id、output_dataset_id、因子绑定、固定 subject 过滤、频率和配置快照；具体入口包括 modules/factor/internal/rpc/service.go、internal/store/binding.go、internal/registry/metadata_sync.go、schema/factor.sql、proto/factor.proto。
- [ ] 从候选工作树当前的半成品继续：先运行 Factor 编译与 `internal/store`、`internal/registry`、`internal/rpc` 定向测试，修复旧字段引用后再扩展逻辑。完整修改 `internal/rpc/convert.go`、`internal/rpc/recalc.go` 及相应 schema/tests、catalog payload 和生成代码；不得仅凭 proto/schema 已出现新字段就认为契约迁移完成。显式 `output_dataset_id` 是绑定权威值，不能再由 input Dataset 自动推导、解析或覆盖；加入 RPC round-trip 断言，证明不同的 RAW→TS 与 PANEL→CS input/output IDs 原样持久化、同步和回读。
- [ ] 将 Factor Dataset 建模为 Storage 的普通 Dataset：由一个 Factor production task 独占写入，字段来自该任务包含的 factor definitions，成员来自任务固定的输入快照/显式过滤；一个输出 Dataset 可承载多个因子列。Dataset 关联与计算任务关联分开存储，不能再用 Source View 表达输入数据源。
- [ ] 多基础 Dataset 的组合归属 MDataset 定义而非 Factor Dataset：TS 第一版使用一个 RAW 输入 Dataset 和一个独立 TS 输出 Dataset；XS 使用一个已构造好的 PANEL/MDataset 输入 Dataset 与一个独立 CS 输出 Dataset。多个基础来源的对齐、交集和列前缀由 MDataset producer 完成，不在 Factor Python 中隐式 Join。
- [ ] 输入输出不同，依赖图无环；第一版一个输出 Dataset 一个生产任务，任务内多个因子。
- [ ] 启用后输入语义不可原地改，改变来源/行键/映射创建新 Dataset；配置更新不能让旧任务覆盖新结果。
- [ ] 持久化 task generation/config snapshot，使输入 Dataset、字段映射、因子参数或输出列变化后旧执行无法写入新输出；实时周期终态不可被重新打开。
- [ ] 在输出数据到来前建立输出周期名单：TS 从输入 Dataset 冻结快照应用任务过滤，Merge 从各源快照标准化后取交集，XS 从 PANEL 快照派生；先 PutSubjectSnapshot + BeginDatasetPeriod，再接受该周期结果行。成功行不能决定预期名单。
- [ ] 测试多任务一引擎、创建失败恢复、孤立资源重试、绑定启停及越权写入。
- [ ] Control Registry/FactorMgr 接受并能创建、更新、目录同步 `timeseries` 与 `cross_section` 两类定义；Engine 根据 `factor_type` 分派执行，不让控制面只收录截面因子。
- [ ] Control/Engine RPC、catalog payload、Web DTO、Strategy 绑定和 Recalc 请求一并去除 SourceViewID 作为计算身份的用法；View ID 仅保留在查询/浏览配置。为 RAW→TS、PANEL→CS 两条关系加序列化/回读测试，验证旧输入快照和旧 generation 的运行不能写新 Dataset。

### T05b. 控制面与计算引擎部署职责

依赖 T05。文件范围：modules/factor/cmd/server/main.go、cmd/engine/main.go、internal/bootstrap/{control_resources.go,engine_resources.go,engine_runtime.go}、config/{app.yaml,engine-app.yaml,trpc_go.yaml,engine-trpc.yaml}；scripts/build/build.sh、scripts/build/package-factor-engine.sh、scripts/deploy/factor-engine/。

- [ ] 先复用并验证当前已存在的 `moox-factor` 与 `moox-factor-engine` 双入口。控制面 SQLite 是定义/绑定目录权威源；Engine 可保留本机 Store 作为目录缓存、运行状态和任务明细，但不能成为第二权威目录，也不能向 FactorMgr 写回控制面定义。
- [ ] 控制面测试证明它无需 Python worker、不订阅实时计算 durable，仅维护定义/绑定/任务受理、目录发布和状态查询。
- [ ] 引擎测试证明其不注册 FactorMgr，只按已授权任务同步目录并订阅输入 Dataset；引擎→EventBus/Storage/控制面的连接采用内网出站，正式配置不得要求外网 Gateway 回连内网。
- [ ] 验证 durable consumer 的 stream/filter/durable 名称和首次启动策略。新 durable 必须由受控启用流程显式设定消费起点（与 T12 的 T0/快照冻结协调），不能用 `DeliverNew` 隐式跳过已进入 Stream 的目标事件，也不能无界重放全部历史；durable 创建后重启只沿用持久 ack 位置。
- [ ] 明确控制面/引擎对 catalog 的权威与同步方向：Control 持有定义/绑定并发布 revision；Engine 启动先同步目录再启消费，revision 落后或配置缺失时不得以旧任务输出；队列事件只携带任务变更/调度信号，不复制第二份权威目录数据库。
- [ ] Engine 对 EventBus、Storage、Control 的网络连接由内网发起；Control Gateway 只连接外网本机 control RPC，不要求从外网入站访问 Engine。验证非必要监听端口关闭、凭据按服务角色最小化。
- [ ] 分别构建/打包 control、engine；配置、Python runtime、缓存目录和密钥隔离。密钥只由部署环境注入，不进入仓库或发布包。
- [ ] 该任务是对既有进程拆分的边界补齐，不代表外网控制面、内网引擎已部署或在线。

### T06. 时序路径改造

依赖 T03/T05/T05b。修改 modules/factor/internal/bootstrap/subject.go、internal/trigger/{dataset_rows.go,subject_tasks.go}、internal/taskrunner/、internal/storageio/{dataset_window.go,writeback.go}、engine/；复用已有入口和微批。

- [ ] 输入普通 RAW 的完整记录可触发，不仅限 merge 专属 Dataset；Collector 写入也采用受验证完整提交。
- [ ] consumer FilterSubjects 和 binding 选择统一按 input_dataset_id。候选 `boundDatasetRowFilters` 和 `bindingDatasetID` 当前仍优先使用 `OutputDatasetID`，需改成只匹配输入 Dataset；写回目标单独使用 `output_dataset_id`。只接受所属任务的完整基础输入事件，输出写 TS，不回写 RAW。周期调度器应可在首条输入行到达前根据输入 snapshot 开始 TS 输出周期；不能因整周期暂时没有行而漏掉超时终态。
- [ ] lookback 读 Primary，微批按 Dataset/频率/窗口组合，限制对象数、行数、字段数和并发。
- [ ] Python 保留 compute(df, params, context)，factor_type 在定义中；保留现有 Bias 数学口径。
- [ ] 同一 output Dataset/task/subject/period 下的所有 required 因子先聚合成一条完整结果行，再由该 Factor production task 调用受控 CommitInput 一次提交；单因子失败时整条 subject 结果不提交、以 failed 终态报告，不能写部分 required 因子列。Storage 使用周期固定的生产契约推导必需字段并原子记成功；不能沿用旧同 Dataset PatchFactor 作为新输出主链。缺历史和算法失败通过 ReportPeriod 报终态，不能丢 subject。
- [ ] 全量 binding/consumer 切到独立输出 Dataset 后，删除同 Dataset `PatchFactor` 计算写回和对应的 `factor_patch` 输入分支，避免留下第二种结果权威或兼容路径；仅保留确实仍由非因子管理用途调用的通用 Storage patch API。
- [ ] 测试重复事件、早于周期完成的行、无未来数据、输出不回环、崩溃重试和完整字段要求。
- [ ] 保留既有 subject run ledger 作为执行明细/重试协助，不让它取代 Storage Dataset period 权威；移除或降级本地 `PeriodBarrier` / `FactorPeriodComputed` 的结果就绪权威，策略及下游统一使用输出 Dataset 的 `DatasetPeriodCompleted`，View 查询再等其关联 `ViewDataReady`。时序 binding 不得同时被 DatasetRows 与旧 ViewDataReady consumer 双触发。补算用独立 run identity，不能重开实时周期或覆写不同 task generation 的最新输出。

### T07. 复用缓存并修正身份

依赖 T06。修改 modules/factor/internal/inputcache/、storageio/、bootstrap/cache.go。

- [ ] 复用既有代际/文件锁/重建实现，验证实际读数入口已接缓存，而非仅有单测工具。
- [ ] 从 Storage metadata 获取任意已绑定输入 Dataset（包括普通 RAW，而不只是 Merge catalog 中的 Dataset）的字段类型/基础字段投影/schema generation，并在 Engine 启动和目录 revision 更新时为生产 `DatasetCache` 调用 `RegisterSchema`；证明 RAW 冷缓存回源后能命中。schema generation 变更即废弃旧缓存、创建结构匹配的新空 DuckDB，不在线 ALTER。因子新增只依赖已有字段时不改表结构。
- [ ] Dataset ID/schema 隔离，全量基础投影命中；不使用 View revision 或 Factor binding 数量决定缓存列集合。
- [ ] 冷缓存不预填，schema 变更新空库，完整性不足回 Primary，源确认缺失不无限循环。
- [ ] 真实 read-through 与故障降级测试通过后移除 engine_runtime.go 的 cache enabled 拒绝条件；默认开启与否不代替能力验收。
- [ ] Timer interval 固定为 37m13s（2233 秒）且启动后延迟首次检查；超限时用新 DuckDB 文件保留 `cache_updated_at DESC, stable_business_key` 最近 N 行，原子切换并等旧读者释放后删除旧代际。预算统计活动库、退役库、临时文件和 WAL；空间不足或新库仍超限时停止缓存写入并回 Primary。
- [ ] 配置 `rebuild_keep_rows=N`；重建 N 行仅保留最近缓存，不声明完整覆盖区间。覆盖不足、过期、缺窗口或容量退化均必须回源 Primary 补齐，不能返回静默截断窗口。
- [ ] 测试大整数/JSON/null、读者排空、跨 Dataset 隔离、cache disabled、重建失败和目录残留。

### T08. 聚合原始与时序结果

依赖 T04/T05/T06。复用 modules/merge 独立 Go 模块、cmd/server 和 internal/merge/{assembler,consumer,periods,subjects,reporter}.go，替换名单来源；禁止在 modules/factor 下新建 cmd/merge。

- [ ] 聚合任务输入 RAW + TS，GetPeriodSubjects 标准化后求交集；按 1m 周期调度器在行事件到来前 BeginDatasetPeriod 冻结 PANEL 宇宙，不再等 CollectorPeriodCompleted。consumer 当前忽略 `write_kind=input_commit`，需按已配置输入 Dataset 放行 RAW/TS 完整提交，同时拒绝无关 Dataset 并防止读取 PANEL 自身输出形成回环。
- [ ] MDataset 定义必须声明多个输入 Dataset、统一 frequency/business key、字段映射和 universe 来源；系统 merge 开启时按标准 subject_id 自动匹配，多个来源字段名用原 Dataset ID 前缀（例如 `raw_dataset_id__close`、`factor_dataset_id__bias_20`）避免碰撞；未配置系统 merge 时转给用户自定义 processor，不能假装已自动聚合。
- [ ] 配置校验拒绝不同 frequency、period boundary 或 business key 的输入；映射后同一标准字段多源冲突必须显式配置优先级/拒绝，不采用最后写入覆盖。
- [ ] RAW 全市场与 TS 三标的相交只得三标的；其中 TS 某 subject 失败不从交集消失。
- [ ] 一 subject 同周期所有源完整后一次 CommitInput(PANEL)；持久化部分到达状态。交集以快照为准而非成功行：双上市对象即使 TS 或某源失败仍留在 expected 中并最终 missing；只在单边上市的对象不在交集中。
- [ ] PANEL 字段与对象快照在建任务/启用时可预览：字段由输入 Dataset 声明列及前缀映射确定；对象宇宙为每个输入 period snapshot 的 canonical subject 交集，绝不从已到达成功行反推或删减。
- [ ] system/custom 仍可选，外部使用“聚合数据集”，不暴露 MergePeriodCompleted。Merge reporter 必须改用 Storage `ReportPeriod` 上报 missing/failed，由 Storage 统一发布 PANEL 的 `DatasetPeriodCompleted`；删除 Merge 专用报告 RPC、marker 和完成事件作为权威的路径。
- [ ] 周期一旦由 Storage 终态关闭，Merge 必须停止接受该周期的新 CommitInput；替换当前 `PeriodLedger.Accepts` 对 reported period 仍返回 true 的行为，以及 `TestMergePeriodLedgerAcceptsLateArrivalAfterCloseWithoutRewritingReport` / 对应 pipeline 断言。迟到输入应记录可观测原因并 ACK/终止重试，不能无限重试必然被 Storage 拒绝的写入，也不能重开或改写完成结果。为常态延迟配置足够的 deadline；超过截止的恢复仅允许走显式、独立 run identity 的补算。
- [ ] 测试缺快照与空快照差异、同源冲突、先 TS 后 RAW、重复、重启、截止缺失、Storage 已终态后迟到事件 ACK/drop，以及输入事件/完成报告重放不会双计。

### T09. 截面直接 Dataset 驱动

依赖 T03/T08。修改 modules/factor/internal/bootstrap/view_ready.go、internal/trigger/view_ready_runner.go、internal/taskrunner/、internal/storageio/；新增 modules/factor/factors/Rank.py，并更新 factors/catalog.json、pyworker/worker.py 与 factors/test_cross_section_factors.py。

- [ ] 仅目标 PANEL 的 DatasetPeriodCompleted 触发，默认 complete；degraded 按明确策略终止/报告，不静默缩小集合。
- [ ] task builder/storage reader 只接收 PANEL 的 input_dataset_id；删除把 source_view_id 误当 dataset_id 传给 Primary 的路径。CS 输出 View 为可选查询目标，不是截面输入。
- [ ] Primary 按固定周期和冻结 snapshot 批量读完整面板，验证对象范围、字段与 period boundary；优先复用现有 ReadPeriodChunks 类批量接口，不逐 subject 打回源 RPC；不等源 View。
- [ ] 因子 Python ABI 仍为 `compute(df, params, context)`；`factor_type=cross_section` 由定义表/registry 决定传入多 subject 面板。Rank 使用降序 dense rank，相同值同名次；输出唯一 subject 键到 CS，不在 Python 函数名或参数中重复声明类型。
- [ ] 同一 CS output Dataset/task/subject/period 下的所有 required 截面因子先合并成完整结果行，再一次 CommitInput；任一 required 因子失败则该 subject 不写部分行并报告 failed。CS 预期名单固定继承目标面板规则，全部 subject 到终态后由 Storage 发布输出 Dataset 完成事件。
- [ ] 所有截面任务切到 DatasetPeriodCompleted 后删除 `factor_view_ready_v1` 对计算调度的消费及其旧 ViewDataReady→binding 路径；ViewDataReady 仍由 View 发布给结果查询/策略读者，不再是计算引擎触发器。
- [ ] 测试无关 Dataset 事件、重复完成、并列/非有限值、输出越界、结果不自触发。

### T10. View 手动 A/B 与通用可读

依赖 T03。修改 maintenance.go、data_ready.go、ready_fence.go、period_event_apply.go；复用 rebuild RPC。

- [ ] 区分显式创建索引、用户请求新重建、已授权重建恢复；关闭 schema/coverage/容量自动新建 B。
- [ ] 实时索引写入继续；受影响字段删改仅标记差异，不静默修复或返回虚假成功通知。
- [ ] 未选择的新字段不算失配；失败保留 A，用户手动重试，切换后旧读者退出再删 A。
- [ ] 消费统一 Dataset 完成，WaitApplied 等所有相关有序流的连续应用进度；不能比较一个跨流最大序号。
- [ ] 不能把“已见最大 sequence”直接当“连续已应用”：先查明每个 `(space, view, index, node, store)` sequence 的生成与投递是否保证严格连续有序；无此保证时改为连续水位或持久化洞集合，加入先收到 N+1、后收到 N 的恢复测试。
- [ ] 测试 View 重建不影响时序/截面，旧 schema 不声称新字段可读，多 View 独立范围。
- [ ] 新增 `web/tests/view-manual-rebuild.spec.ts`，覆盖 schema/coverage 差异只提示不自动重建、手动确认后进入 A/B、失败保留 A、可重试和旧读者排空后清理。

### T11. 数据采集资产与因子工作台整理

依赖 T04/T05/T10。修改既有 static-menu、Dataset/subject/因子页面与 API，不另建平行管理站点。

- [ ] “数据资产”内容不重新机械搬迁：数据源、数据对象、基础字段、采集任务、基础 Dataset 现已在“数据采集”菜单。将对象/字段文案统一为“采集对象/字段管理”，确认空间切换和菜单可见性；保留独立“因子”区用于派生任务与结果。`/data/import` 虽不在菜单但能直达，必须明确归属与权限，不留下采集体系外的基础数据写入入口。
- [ ] 因子模块管理时序/截面任务、Factor result Dataset 和聚合 Dataset；增加可操作的聚合 Dataset 配置流程：选择 2 个以上输入 Dataset、系统/自定义构造方式、frequency/key contract、必需源、字段前缀映射、subject universe 来源与交集预览；提交后展示生成的 Dataset schema、生产任务和默认 View。配置版本启用后不可原地改输入语义。Dataset 详情提供上下游链接，不强制新增图形 DAG。
- [ ] 详情呈现数据、字段、标的快照、周期完整性、失败 subject、View 索引；“沿用快照”“无快照”“合法空”可区分。
- [ ] 配置输入输出/参数/映射，创建默认 View 复用幂等资源流程；保存失败给出明确原因。
- [ ] 在采集对象/符号映射管理中明确展示 source、external symbol、canonical subject；提供冲突/歧义预览，未经确认或不唯一映射不得启用，不通过剥除 `-SPOT`/`-SWAP` 自动猜测标准标的。
- [ ] 重建按钮先展示差异和操作范围，再触发已有 A/B；状态、失败、重试和轮询完善。
- [ ] 基础 Dataset 的列编辑禁止增加 Factor/System 输出列；归属未知的字段显示 unknown 并只读，不默认伪装为基础字段。CSV import 入口按 RAW/import role 与 FIELD 来源过滤候选 Dataset/字段，并拒绝通过 Gateway API 直接写 Factor-owned Dataset；如它属于基础导入路径，写入必须接 Storage 完整提交/period contract，否则禁用此入口，不保留绕过路径。
- [ ] 增加服务端 owner_module/dataset_role 查询过滤或后端写入鉴权；现有前端 attributes 过滤只用于显示，不能视为权限边界。Storage service-level 负向测试由 T02/T03 验证错误 principal/owner、非授权字段和跨 Dataset 写入确实被拒绝；本任务的 API payload 测试验证浏览器请求契约，Playwright 验证错误被正确呈现，不能将前端测试当作授权证明。
- [ ] 因子绑定、补算和策略引用一起从 Source View 驱动改为 input/output Dataset 驱动；View 只作为属于单个 Dataset 的浏览索引，默认 View 创建继续幂等。
- [ ] 新增 `web/tests/dataset-subject-snapshot.spec.ts` 与 `web/tests/factor-pipeline.spec.ts`，覆盖快照三态、映射冲突预览、聚合 Dataset 创建和 RAW→TS/PANEL→CS 工作流；Vitest/Playwright 覆盖直达刷新、空/加载/错误态、桌面与移动、权限和跨 space。
- [ ] 扩展已有 `data-collection-navigation.spec.ts`、`data-management.test.ts`、`factor-dataset-workflow.spec.ts`：断言没有“数据资产”顶层入口、数据采集子页稳定可达、基础字段页不暴露因子列、CSV 不可写 Factor Dataset、Factor binding 显示 RAW→TS/PANEL→CS 明确输入输出和各自 View。
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

1. T01 固定 Storage RPC、事件字段、生产者身份和必需字段来源；先生成协议代码并冻结接口。
2. T02 完成 Pebble 快照 API 与 Primary/DataNode 鉴权路由；随后 T03 将完整行、subject 成功、事件 outbox 和 Dataset 周期状态打通。
3. T04 接入 Collector 快照同步及完整 K 线提交；T05/T05b 完成输入输出 Dataset 模型和进程授权边界。
4. T06 时序计算输出独立 Dataset；T07 启用并验证本地 DuckDB；T08 把 RAW 与 TS 聚合为 PANEL；T09 由 PANEL 完成事件触发 XS 并写独立 CS。
5. T10 改造 View 手动 A/B 与可读 fence；T11 补齐数据采集资产边界并迁移 Factor 工作台契约；T12 准备验收配置和历史窗口；T13 本地/正式环境验证、独立 codeCR 与交付。

共享代码所有权：Storage proto 与 `packages/storagepb` 由单一实现者先完成；Pebble 快照与周期状态由同一 Storage 实现者连续修改；Collector、Factor、Merge 只能在协议固定后并行，且不能同时修改共享生成代码。Merge 必须继续位于 `modules/merge` 独立模块，不要新建 `modules/factor/internal/merge`。View/UI 可在协议冻结后并行，但手动重建 RPC 的语义由 Storage owner 定义。

执行分支起点已指定为 `.worktrees/final-binance-spot-factor` 的 `feature/final-binance-spot-factor@d5498de8`。开工第一步不是重写 T01/T02，而是对该工作树现存未提交的 KV、Proto 和生成代码做独立核对，确认工作区文件清单及定向测试结果，再在这一处增量实现。主工作树 `feature/mooyang` 只作为源代码基线，不再平行实现同一 Storage 功能；禁止 cherry-pick/复制另一份未经审查的相同实现。当前用户要求先不编码，因此上述分支选择只固定未来执行入口，不授权本轮继续实现。

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
cd web && pnpm exec playwright test tests/data-collection-navigation.spec.ts tests/factor-dataset-workflow.spec.ts tests/field-management-workbench.e2e.spec.ts tests/dataset-subject-snapshot.spec.ts tests/factor-pipeline.spec.ts tests/view-manual-rebuild.spec.ts
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
- [ ] Admin 的 `ListServiceDeployments` 只证明部署登记，不证明进程存活；在确认目标主机和部署根后，使用该机 `healthcheck.sh factor-engine`（本机 `/readyz`）验证 Engine。健康检查前不得把未知 IP 当作目标，也不得用 `setup status` 代替实时状态。
- [ ] 通过正式环境 API/CLI 确认 RAW 的 Dataset ID、字段 schema、1m period boundary、series_tag、实际激活现货 subject 和至少 20 根连续 K 线。仓库 `collector-rules.yaml` 模板不能证明线上已启用。
- [ ] 确认 RAW 当前生产者 principal 与目标 Dataset owner；确认 event subject/ACL 放行 Collector、Merge、Factor Engine 各自所需操作。
- [ ] 查询 TS/PANEL/CS、View 和任务 ID 是否冲突；存在同名但 schema/owner 不一致的资源时停止，不覆盖、不删除。
- [ ] 预检输出缺少服务、权限、名单或事件时判失败并说明缺项；不得返回成功 `SKIP`，不得在预检自动补历史、改规则或重启服务。

### 7.2 资源与拓扑

以 BTC、ETH、SOL 三个资产做功能验收。预检必须从实际 Binance spot active 列表及映射配置解析每个 raw_subject_id 和 canonical_subject_id；若某项未激活、无映射或有歧义，就停止并明确列出，不能静默换 symbol 或靠删 `-SPOT/-SWAP` 后缀猜 ID。RAW 仍保持完整现货 subject 快照，三标过滤只作用于验收计算任务。

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
- [ ] 3 个 subject 通过后，扩大到实际 active spot universe，记录时序触发到结果提交延迟、Merge 延迟、XS 执行时间、缓存命中/回源、Storage/NATS 错误和失败 subject；三标功能通过不等于全市场性能通过。

数值验收继续使用现存 Bias.py 口径：`bias_20 = close / mean(last 20 closes)`，并使用独立脚本从已确认的 20 个原始 close 计算，绝对/相对误差不超过 `1e-10`。截面 `bias_rank` 使用降序 dense rank：相同值同名次，下一个不同值名次加一；rank 整数精确比较。零均值、非有限值、窗口缺失和重复业务键应失败并留下终态原因，不能写成功结果。

### 7.4 交付证据

- [ ] 本地 Linux 目标构建与包校验通过，记录 `moox-factor`、`moox-factor-engine`、`moox-merge`、web-host 产物 SHA 和 Python runtime 版本。
- [ ] 正式部署后核对进程角色、版本 SHA、生效配置摘要、NATS durable ACK/lag、Storage 数据和服务健康；engine 进程在内网机本地运行。
- [ ] 只读预检与数据验收脚本不得调用部署脚本；`scripts/deploy/factor-engine/start.sh` 含 stop/kill/mkdir/launch 等副作用，不可作为健康探测手段。
- [ ] 新启动 codeCR subagent 对完整差异做最终只读审查；主 Agent 核实每个发现，修复后重跑受影响测试和发布包校验。
- [ ] 交付报告写明实际执行的命令、测试级别、3 个周期的证据索引、性能结果、未覆盖范围及风险；不包含 secret。

## 8. 故障与一致性验收矩阵

| 场景 | 必须结果 |
|---|---|
| Collector 市场列表获取失败 | 保留最后有效快照并告警；不能写空快照 |
| subject 集合未变化 | 沿用已有快照 ID；不生成重复全量快照 |
| 无适用历史快照 | 阻止启动该周期并给出可观测原因；不当作空集合 |
| 合法空快照 | 明确固定空快照，并可产生空集 complete 终态 |
| 查询周期成员时无适用快照 | `GetPeriodSubjects` 成功返回 `found=false`；调用方阻止计算，不隐式创建空 snapshot |
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
| PANEL 周期 degraded | 默认 XS 不以残缺对象宇宙运行，不伪造 complete |
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

计划生成阶段重跑了 Collector 三个定向 package 测试并据此整理盘点；本次续接遵照“先不编码”，只校对并交付计划，没有修改源代码、运行测试、启停服务、创建线上资源或部署。候选工作树中原有未提交改动保持原状。计划完成仅代表执行拆解完成，不代表任何目标功能已实现或正式环境已验收。
