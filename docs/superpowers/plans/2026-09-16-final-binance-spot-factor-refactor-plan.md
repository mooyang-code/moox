# 最终改造执行计划：Dataset 周期能力与币安现货 1m 因子链路

> **For agentic workers:** 使用 executing-plans 逐任务实施；共享协议先落定再分工，代码审查使用 codeCR。本次交付仅为计划，不授权编码、服务启停或部署。

**Goal:** 在现有代码上补齐 Dataset 标的快照与周期完成、优化数据资产管理、重构因子计算输入输出，最终交付真实币安现货 1m 的时序因子、聚合和截面结果。

**Architecture:** Collector 同步 subject 快照；Storage KV 固定周期引用、完整提交时记成功并发布 DatasetPeriodCompleted。时序输入输出分离，聚合原始 K 线与时序结果形成 mdataset，截面直接读其 Primary；View 仅提供查询索引及 ViewDataReady，重建由用户手动触发。

**Tech Stack:** Go 多模块、tRPC、Protobuf、Pebble KV、JetStream、DuckDB、Python、Vue/TypeScript、Vitest/Playwright。

日期：2026-09-16。只读盘点基线：`8afaaa60d724862cc652bb9491fc4c3257af838a`。本轮未运行功能测试、未检查线上版本；“已有”仅表示源码存在，不表示已验证可上线。

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
| 因子输出 | 写独立结果 Dataset，输入输出不同、依赖图无环 |
| 聚合 | 标准化各源周期预期 subject 后取交集，不取成功行交集 |
| 截面 | 消费 mdataset 的 DatasetPeriodCompleted，直接读 Primary |
| View | 每个 View 一个 Dataset，一个 Dataset 多个 View；不聚合、不驱动计算 |
| 重建 | 用户手动发起 A/B；保留追平、切换、恢复、读者排空 |
| 查询通知 | ViewDataReady 关联 Dataset 完成事件，证明指定范围可读 |
| 缓存 | Dataset ID/schema 隔离，惰性回源，2233s 检查，保留 N 行新文件重建 |
| 历史 | 不实现历史修正传播；重复投递、故障恢复、显式补算仍有边界 |
| 输入版本 | 不增加 input_contract_version；输入语义改变创建新 Dataset |

不因新项目允许重构而清理无关工作树内容。盘点时已有 artifacts/storage-datanode-release-sha256.txt、modules/cli/tools/、web/test-results/ 改动，实施前重新检查，禁止批量覆盖或暂存。

## 2. 代码盘点与改造落点

### 2.1 Storage、Collector 和 View

| 已核对入口 | 现状 | 实施决策 |
|---|---|---|
| modules/storage/internal/service/datanode/pebble/input_commit.go:63、98 | CommitInput 校验必需字段，数据、outbox、幂等收据同批写入 | 扩展原 batch callback 写 subject 状态；不要重写提交框架 |
| modules/collector/internal/marketfetch/period_readiness.go:95、186、217 | TaskInstance 形成名单，Collector 消费写事件确认成功 | 用 Storage 快照和提交成功状态替代完整性权威；保留采集任务诊断 |
| modules/collector/internal/store/period_readiness.go:26 | 每周期 SQLite 冻结名单 | 停止作为 Dataset 权威，不新建第二套相同账本 |
| modules/collector/internal/marketfetch/period_reporter.go:217 | 部分快照引用为占位字符串 | 替换为真实可解析的 KV snapshot_id |
| packages/storagepb/storage_events.proto:38、54、79 | Collector/Merge/Factor 专用完成事件仍活跃 | 原子切换生产者/消费者到 DatasetPeriodCompleted |
| modules/storage/proto/metadata.proto:75 | View 已为单 dataset_id | 复用模型，补一 Dataset 多 View 回归，不重复改协议 |
| modules/storage/internal/service/metadata/sqlite/crud_view_rebuild.go:17 | 已有手动请求 | 复用 UI/RPC 请求入口 |
| modules/storage/internal/service/view/maintenance.go:658、669、785 | coverage/schema/容量仍可自动申请重建 | 限制新任务准入，保留已授权任务恢复 |
| modules/storage/internal/service/view/data_ready.go:59、85；ready_fence.go:163 | 已有通用事件及持久 fence | 更换输入协议，复用等待与恢复，验证新周期提交位置 |
| modules/storage/internal/service/view/period_event_apply.go:130、177 | 仍接生产方专用完成 | 改为统一 Dataset 完成入口 |

按当前符号搜索，PutSubjectSnapshot、GetPeriodSubjects、DatasetPeriodCompleted 尚未形成目标实现；不能将文档接口当作已有 API。

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
| modules/factor/internal/registry/metadata_sync.go:111 | 权威绑定将结果定位回源 View 的 Dataset | T05 首先解除同 Dataset 限制，再调整写回和 ACL |
| modules/factor/internal/trigger/dataset_rows.go:87 | 已过滤 input_commit，factor_patch 不自触发 | 复用过滤和幂等，接入 RAW 完整提交 |
| modules/factor/internal/storageio/dataset_window.go:25 | 已有 Primary 精确键窗口读取 | 复用，补完整度/预算和输出 Dataset 场景 |
| modules/factor/internal/trigger/view_ready_runner.go:503、537 | 仍依赖 MergePeriodCompleted 类型的 ViewDataReady | T09 改 Dataset 完成及 Primary 面板，不另建平行截面引擎 |
| modules/factor/schema/factor.sql:181 | 本地 barrier/pairs 保存周期完成 | 保留执行明细，将 Dataset 完整性权威移到 KV |
| modules/factor/internal/bootstrap/engine_resources.go:86 | 已创建并挂载缓存 read-through | T07 复用，不重复造缓存 |
| modules/factor/internal/bootstrap/engine_runtime.go:47 | cache.enabled=true 仍被运行校验拒绝 | T07 验证真实路径后删除临时禁用，不能仅改默认配置 |
| modules/factor/pyworker/worker.py:133；factors/catalog.json:1 | 三参数 ABI 已实现，内置 12 个因子皆时序 | 保留 ABI，新增明确截面 Rank 定义及测试 |
| modules/merge/internal/merge/assembler.go:149 | 多源前缀映射和 CommitInput 已实现 | 更换快照来源及验收输入，不重写 assembler |

仓库 setup 默认规则存在不等于实际环境已经启用；本轮没有读取线上生效配置。真实验收前必须查询规则状态和新数据，不能据示例配置宣称采集运行。

### 2.4 公共验证资产

- modules/factor/factors/Bias.py:1 已使用 compute(df, params, context)，计算的是 close/rolling_mean，不减一；min_periods=1。验收沿用其实际口径，不凭算法名称猜公式。
- modules/collector/internal/sources/binance/symbol_identity.go:9 的 ProviderSymbol 是交易所请求符号转换，不是通用跨 Dataset 标准化。不能直接拿去充当用户映射系统。
- scripts/test/e2e/test-factor-storage-e2e.sh 会读取部署目录、要求服务运行，且存在可选重启；不得在盘点阶段执行，也不能把它当成新链路已覆盖的证明。
- web/package.json 已有 test、check:menu、check:data-browse、build:prod；复用既有构建与测试体系。

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

试运行先采用 BTC-USDT-SPOT、ETH-USDT-SPOT、SOL-USDT-SPOT 三个实际存在且已激活标的（启动前核查，缺少则报清晰错误，不静默替换）。基础 RAW 名单仍由 Collector 的完整市场快照管理，计算任务通过显式过滤得到这三个输出预期标的，不缩小全市场采集名单。

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

- [ ] 定义 PutSubjectSnapshot、GetPeriodSubjects、ReportPeriod、GetDatasetPeriod；producer 来自认证及任务归属，不接受任意身份字符串。
- [ ] PutSubjectSnapshot 包含 dataset_id、frequency、生效周期、request_id、subject_ids 及标准化映射引用；GetPeriodSubjects 只查询，不隐式启动任务。
- [ ] DatasetPeriodCompleted 携带真实 snapshot_id、周期、状态、失败集合和所需提交位置；ViewDataReady 关联 completion_event_id、view_id、范围。
- [ ] 测试旧事件无注册、缺字段拒绝、不同 Dataset/周期不误关联、重复发布 ID 稳定；更新生成代码和全部调用方。
- [ ] 删除伪 scope 引用、input_contract_version 和旧专用完成协议，不保留兼容别名。

### T02. KV 快照与周期冻结

依赖 T01。新增：modules/storage/internal/service/datanode/pebble/subject_snapshot.go、subject_snapshot_test.go、period_state.go、period_state_test.go。RPC 路由复用现有 Primary/DataNode 归属机制。

- [ ] 保存全量快照、生效索引、周期固定引用；同频率按 effective_period 查最近适用快照。
- [ ] 无变化不写逐周期 UNCHANGED；周期 StartPeriod 内部操作串行固定引用，查询只读。
- [ ] 未有快照阻塞、合法空快照可完成；更新不改变已运行周期，迟到生效记录不回写固定引用。
- [ ] 测试关库重开、分页、同请求重试、并发冻结、跨频率隔离。
- [ ] GC 同时保护周期引用、活动任务引用、最新可继承快照和保留范围边界前驱。

### T03. 原子成功状态与增量完成

依赖 T02。修改 input_commit.go、现有 outbox writer；新增 subject_state.go、period_complete.go 及测试。

- [ ] 扩展现有同 KV batch：数据 + subject 成功状态 + 待发布记录；不订阅自身字段广播，不要求成功二次 RPC。
- [ ] 必需字段从受管理配置取得，不能由客户端提交一个缩减字段集伪造完整成功；结果 Dataset 同样纳入完整提交路径。
- [ ] 按 subject 去重更新 succeeded/missing/failed；串行化或事务保护读旧状态到写新状态，原子 batch 本身不是并发计数锁。
- [ ] ReportPeriod 仅报告 missing/failed；禁止覆盖已成功或重开终态周期。
- [ ] 最后一项终态触发 CompletePeriod，周期状态与完成事件原子保存；timer 处理截止及重试，不每次全量扫数据。
- [ ] 多节点时本地可靠状态记录驱动指定归属节点幂等汇总，不承诺跨 KV 事务。
- [ ] 测试响应丢失、重复/并发写、数据缺列、跨节点乱序、状态已提交但发布失败。

### T04. Collector 和标准化

依赖 T02/T03。修改 modules/collector/internal/marketfetch/period_readiness.go、period_reporter.go、marketstorage/storage.go、sources/binance/symbol.go；增加对象映射 API/领域测试。

- [ ] 市场列表定时获取；变化写全量，未变化更新同步时间，失败保留旧快照并告警。
- [ ] 周期启动先固定 RAW snapshot，再写数据；Collector 不再消费写事件充当 Dataset 成功权威。
- [ ] source_scope + raw_subject_id → canonical_subject_id，保留类型、交易所、计价信息；启用前预览冲突。
- [ ] 测试冻结前退市排除、冻结后缺数据记 missing、全市场 API 失败不写空列表。
- [ ] 同一源多个品种归一冲突拒绝；当前 ProviderSymbol 继续仅服务交易所请求。

### T05. 任务配置与输出隔离

依赖 T01/T02。修改 modules/factor/internal/domain/、store/、proto/、catalogsync/、registry/metadata_sync.go、schema/factor.sql。

- [ ] 输入 Dataset、输出 Dataset、因子绑定、固定 subject 过滤、频率和配置快照显式定义。
- [ ] 输入输出不同，依赖图无环；第一版一个输出 Dataset 一个生产任务，任务内多个因子。
- [ ] 启用后输入语义不可原地改，改变来源/行键/映射创建新 Dataset；配置更新不能让旧任务覆盖新结果。
- [ ] 在输出数据到来前建立输出周期名单，成功行不能决定预期名单。
- [ ] 测试多任务一引擎、创建失败恢复、孤立资源重试、绑定启停及越权写入。

### T06. 时序路径改造

依赖 T03/T05。修改 modules/factor/internal/trigger/、taskrunner/、storageio/、engine/；复用已有入口和微批。

- [ ] 输入普通 RAW 的完整记录可触发，不仅限 merge 专属 Dataset；Collector 写入也采用受验证完整提交。
- [ ] 只接受所属任务的完整基础输入事件，输出写 TS，不回写 RAW。
- [ ] lookback 读 Primary，微批按 Dataset/频率/窗口组合，限制对象数、行数、字段数和并发。
- [ ] Python 保留 compute(df, params, context)，factor_type 在定义中；保留现有 Bias 数学口径。
- [ ] 结果提交驱动 Storage 成功；缺历史和算法失败报告终态，不能丢 subject。
- [ ] 测试重复事件、早于周期完成的行、无未来数据、输出不回环、崩溃重试和完整字段要求。

### T07. 复用缓存并修正身份

依赖 T06。修改 modules/factor/internal/inputcache/、storageio/、bootstrap/cache.go。

- [ ] 复用既有代际/文件锁/重建实现，验证实际读数入口已接缓存，而非仅有单测工具。
- [ ] Dataset ID/schema 隔离，全量基础投影命中；不使用 View revision。
- [ ] 冷缓存不预填，schema 变更新空库，完整性不足回 Primary，源确认缺失不无限循环。
- [ ] 真实 read-through 与故障降级测试通过后移除 engine_runtime.go 的 cache enabled 拒绝条件；默认开启与否不代替能力验收。
- [ ] 2233s 延迟首次检查、N 行按本地写入时间、总目录字节预算；空间不足停填充回源。
- [ ] 测试大整数/JSON/null、读者排空、跨 Dataset 隔离、cache disabled、重建失败和目录残留。

### T08. 聚合原始与时序结果

依赖 T04/T05/T06。复用 modules/merge 独立 Go 模块、cmd/server 和 internal/merge/{assembler,consumer,periods,subjects,reporter}.go，替换名单来源；禁止在 modules/factor 下新建 cmd/merge。

- [ ] 聚合任务输入 RAW + TS，GetPeriodSubjects 标准化后求交集；不再等 CollectorPeriodCompleted 冻结。
- [ ] RAW 全市场与 TS 三标的相交只得三标的；其中 TS 某 subject 失败不从交集消失。
- [ ] 一 subject 同周期所有源完整后一次 CommitInput(PANEL)；持久化部分到达状态。
- [ ] system/custom 仍可选，外部使用“聚合数据集”，不暴露 MergePeriodCompleted。
- [ ] 测试缺快照与空快照差异、同源冲突、先 TS 后 RAW、重复、重启和截止缺失。

### T09. 截面直接 Dataset 驱动

依赖 T03/T08。修改 factor trigger 的周期入口及 Primary 面板读取；新增明确的截面 rank 因子与 Python 测试。

- [ ] 仅目标 PANEL 的 DatasetPeriodCompleted 触发，默认 complete；degraded 按明确策略终止/报告，不静默缩小集合。
- [ ] Primary 读固定周期面板，验证 snapshot 范围和必需字段；不等源 View。
- [ ] 降序 dense rank，相同值同名次；输出唯一 subject 键到 CS。
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

依赖 T06/T08/T09。修改默认配置/CLI 和验收 fixture，不直接在现网执行本步骤。

- [ ] 使用现有数据浏览/API 确认 RAW ID、字段、UTC 周期、subject、series_tag 和已有历史完整度。
- [ ] 选择 T0 为下一个明确 UTC 分钟，T0 前准备每个标的至少 19 根前序完整 K 线；T0 到来后总计 20 根。
- [ ] 优先等待自然采集积累；缺历史需显式授权走已有历史导入/补齐工具，历史导入不伪造实时完成事件。
- [ ] TS/PANEL/CS 从 T0 建立周期引用并准入；不要求先补算全部历史因子才能跑当前截面。
- [ ] 显式历史因子补算使用独立输出 Dataset/任务，不覆写已完成实时周期。
- [ ] consumer 首次起点和 T0 协调；重启沿用 durable 积压，不重新 DeliverNew。

### T13. 构建、真实 E2E 和交付

依赖全部任务。

- [ ] 分别构建 control/engine/merge，验证 CGO、Python 依赖及独立配置/凭据/目录。
- [ ] 先运行本地真实 KV + NATS + Python 集成测试，再进入授权的外网控制面与内网运行部署。
- [ ] 记录连续至少 3 个 T0 之后的新分钟，证明 RAW→TS→PANEL→CS→View 可读；仅历史回算不算通过。
- [ ] 每周期核对三标的 snapshot、结果值、失败集合、关联完成事件和 ViewDataReady，不凭 UI 行数断言完整。
- [ ] 3 标的通过后扩大实际激活现货范围，记录耗时和失败，不以局部验收冒充全市场性能。
- [ ] 新启动 codeCR 最终审查，主 Agent 复现发现并修复重测；旧审查不代替本次最终审查。
- [ ] 生成结果报告和进度清单，记录实际 SHA、部署配置摘要及命令输出，隐藏秘密。
- [ ] 提交/推送本次范围；不能覆盖未授权文件或启动旧单体计算消费者。

## 5. 必跑命令与证据等级

仓库根：
```bash
make -C packages/storagepb generate
make -C modules/storage/proto all
git diff --check
```

各模块单独执行，不用根 go test 代替嵌套模块：
```bash
# packages/events、packages/storagepb 各自目录
go test ./...
# modules/storage、modules/factor、modules/collector、modules/merge 各自目录
env CGO_ENABLED=1 go test ./...
env CGO_ENABLED=1 go test -race ./internal/... -count=1
# modules/factor/factors
python3 -m unittest discover -p 'test_*.py'
# web
pnpm test
pnpm run check:menu
pnpm run check:data-browse
pnpm run build:prod
```

已有定向测试先记录基线：
```bash
# modules/factor
env CGO_ENABLED=1 go test ./internal/integration -run '^TestDatasetPipelinescripts/test/e2e/test-binance-spot-factor-pipeline.sh。必须只读预检在先，要求显式部署根与资源清单；缺少服务/权限/事件不可返回 SKIP 成功。若操作会创建资源、补历史或重启服务，必须显示范围并由执行阶段授权，不复制旧脚本隐式副作用。

新增前端测试：web/tests/dataset-subject-snapshot.spec.ts、factor-pipeline.spec.ts、view-manual-rebuild.spec.ts。使用现有 Playwright 配置启动本地服务，验收浏览器请求与最终数据，不只断言按钮存在。前端改动发布时重建 web-host 嵌入产物并验证生效 SHA。

证据分四级：定向单测、模块回归/模拟 E2E、实际部署、真实新周期。没有运行的级别明确标记未验证；不存在完整测试通过声明的自动继承。

## 6. 故障与一致性验收矩阵

| 场景 | 必须结果 |
|---|---|
| 标的列表获取失败 | 沿用旧快照、同步失败告警，不生成空快照 |
| 无本周期更新 | 解析最近生效快照；启动后引用固定 |
| 成功写入响应丢失 | 原子状态存在，重复不加计数 |
| 同 subject 并发完整提交 | 一次有效终态，不能靠无保护读改写 |
| 一源缺数据 | mdataset 交集仍保留该 subject，截止 missing |
| 聚合中途重启 | 部分到达可恢复，不提交半行 |
| 某因子失败 | 输出名单不缩小，Dataset degraded |
| 完成事件发布失败 | KV 完成状态和 outbox 可恢复重试 |
| 缓存满/删除 | 回 Primary 继续计算，完整性账本不受影响 |
| View 自动检测到字段差异 | 不启动 B，提示差异；手动请求后才重建 |
| 一个提交流未应用 | 不发布对应 ViewDataReady |
| 规则中途变化 | 新周期生效，旧周期范围与结果不变 |
| 快照无人引用但仍是最新生效 | 不被 GC 删除，未来周期仍能沿用 |

## 7. 实施顺序与范围控制

主链：T01→T02→T03→T04/T05→T06→T08→T09→T12→T13。缓存 T07 依赖正确的直读路径；View T10 和前端 T11 在协议固定后推进。共享 proto 与 KV 状态文件明确单负责人，禁止两个任务同时改同一权威协议。

本轮不交易、不下单、不改策略资金逻辑；策略侧只验证通用可读消费契约。默认单引擎/单聚合实例，内部受控并发；不趁机实现全分布式调度平台。多节点 Storage 的可靠汇总不能用“第一版单实例引擎”掩盖。

进度文档建议：2026-09-16-binance-spot-factor-progress.md，执行时创建。每任务记录已复用代码、修改文件、红灯/绿灯命令、审查结论、提交号、未完成项；本计划中的复选框不是现有代码完成证明。

 -count=1
# modules/merge
go test ./internal/merge -run '^TestDatasetPipelinescripts/test/e2e/test-binance-spot-factor-pipeline.sh。必须只读预检在先，要求显式部署根与资源清单；缺少服务/权限/事件不可返回 SKIP 成功。若操作会创建资源、补历史或重启服务，必须显示范围并由执行阶段授权，不复制旧脚本隐式副作用。

新增前端测试：web/tests/dataset-subject-snapshot.spec.ts、factor-pipeline.spec.ts、view-manual-rebuild.spec.ts。使用现有 Playwright 配置启动本地服务，验收浏览器请求与最终数据，不只断言按钮存在。前端改动发布时重建 web-host 嵌入产物并验证生效 SHA。

证据分四级：定向单测、模块回归/模拟 E2E、实际部署、真实新周期。没有运行的级别明确标记未验证；不存在完整测试通过声明的自动继承。

## 6. 故障与一致性验收矩阵

| 场景 | 必须结果 |
|---|---|
| 标的列表获取失败 | 沿用旧快照、同步失败告警，不生成空快照 |
| 无本周期更新 | 解析最近生效快照；启动后引用固定 |
| 成功写入响应丢失 | 原子状态存在，重复不加计数 |
| 同 subject 并发完整提交 | 一次有效终态，不能靠无保护读改写 |
| 一源缺数据 | mdataset 交集仍保留该 subject，截止 missing |
| 聚合中途重启 | 部分到达可恢复，不提交半行 |
| 某因子失败 | 输出名单不缩小，Dataset degraded |
| 完成事件发布失败 | KV 完成状态和 outbox 可恢复重试 |
| 缓存满/删除 | 回 Primary 继续计算，完整性账本不受影响 |
| View 自动检测到字段差异 | 不启动 B，提示差异；手动请求后才重建 |
| 一个提交流未应用 | 不发布对应 ViewDataReady |
| 规则中途变化 | 新周期生效，旧周期范围与结果不变 |
| 快照无人引用但仍是最新生效 | 不被 GC 删除，未来周期仍能沿用 |

## 7. 实施顺序与范围控制

主链：T01→T02→T03→T04/T05→T06→T08→T09→T12→T13。缓存 T07 依赖正确的直读路径；View T10 和前端 T11 在协议固定后推进。共享 proto 与 KV 状态文件明确单负责人，禁止两个任务同时改同一权威协议。

本轮不交易、不下单、不改策略资金逻辑；策略侧只验证通用可读消费契约。默认单引擎/单聚合实例，内部受控并发；不趁机实现全分布式调度平台。多节点 Storage 的可靠汇总不能用“第一版单实例引擎”掩盖。

进度文档建议：2026-09-16-binance-spot-factor-progress.md，执行时创建。每任务记录已复用代码、修改文件、红灯/绿灯命令、审查结论、提交号、未完成项；本计划中的复选框不是现有代码完成证明。

 -count=1
# 仓库根
bash scripts/test/contract/test-release-contract.sh
```

新增脚本：scripts/test/e2e/test-binance-spot-factor-pipeline.sh。必须只读预检在先，要求显式部署根与资源清单；缺少服务/权限/事件不可返回 SKIP 成功。若操作会创建资源、补历史或重启服务，必须显示范围并由执行阶段授权，不复制旧脚本隐式副作用。

新增前端测试：web/tests/dataset-subject-snapshot.spec.ts、factor-pipeline.spec.ts、view-manual-rebuild.spec.ts。使用现有 Playwright 配置启动本地服务，验收浏览器请求与最终数据，不只断言按钮存在。前端改动发布时重建 web-host 嵌入产物并验证生效 SHA。

证据分四级：定向单测、模块回归/模拟 E2E、实际部署、真实新周期。没有运行的级别明确标记未验证；不存在完整测试通过声明的自动继承。

## 6. 故障与一致性验收矩阵

| 场景 | 必须结果 |
|---|---|
| 标的列表获取失败 | 沿用旧快照、同步失败告警，不生成空快照 |
| 无本周期更新 | 解析最近生效快照；启动后引用固定 |
| 成功写入响应丢失 | 原子状态存在，重复不加计数 |
| 同 subject 并发完整提交 | 一次有效终态，不能靠无保护读改写 |
| 一源缺数据 | mdataset 交集仍保留该 subject，截止 missing |
| 聚合中途重启 | 部分到达可恢复，不提交半行 |
| 某因子失败 | 输出名单不缩小，Dataset degraded |
| 完成事件发布失败 | KV 完成状态和 outbox 可恢复重试 |
| 缓存满/删除 | 回 Primary 继续计算，完整性账本不受影响 |
| View 自动检测到字段差异 | 不启动 B，提示差异；手动请求后才重建 |
| 一个提交流未应用 | 不发布对应 ViewDataReady |
| 规则中途变化 | 新周期生效，旧周期范围与结果不变 |
| 快照无人引用但仍是最新生效 | 不被 GC 删除，未来周期仍能沿用 |

## 7. 实施顺序与范围控制

主链：T01→T02→T03→T04/T05→T06→T08→T09→T12→T13。缓存 T07 依赖正确的直读路径；View T10 和前端 T11 在协议固定后推进。共享 proto 与 KV 状态文件明确单负责人，禁止两个任务同时改同一权威协议。

本轮不交易、不下单、不改策略资金逻辑；策略侧只验证通用可读消费契约。默认单引擎/单聚合实例，内部受控并发；不趁机实现全分布式调度平台。多节点 Storage 的可靠汇总不能用“第一版单实例引擎”掩盖。

进度文档建议：2026-09-16-binance-spot-factor-progress.md，执行时创建。每任务记录已复用代码、修改文件、红灯/绿灯命令、审查结论、提交号、未完成项；本计划中的复选框不是现有代码完成证明。
