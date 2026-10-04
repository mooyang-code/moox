# Dataset 成员快照与因子计算链路重构执行计划

> **执行要求：** 使用 executing-plans 逐项执行。代码审查使用 codeCR，审查后由主 Agent 独立核验。本文只生成计划，不授权本轮实施或部署。

**目标：** 将成员快照、数据完整性和周期完成提升为 Storage Dataset 通用能力，统一对象身份，分离时序与截面输出，保留手动触发的 View A/B 重建。

**架构：** 生产者定期同步标的列表，有变化时保存生效全量快照，无变化时沿用。Storage 在周期启动时固定快照引用，在完整数据提交时直接持久化标的成功状态，并发布 DatasetPeriodCompleted；时序读取输入 Dataset 写独立结果，聚合按源周期标的交集构造 mdataset，截面按其完成事件读取 Primary。View 只作为单 Dataset 的查询子索引。

**技术栈：** Go 多模块、tRPC、Protobuf、NATS JetStream、现有 Storage 磁盘 KV、DuckDB 本机缓存、Python、Vue/TypeScript、Vitest/Playwright。标的快照、周期和标的状态复用现有 KV，不额外引入 SQLite；其他模块既有数据库不因此无关重写。

**日期：** 2026-09-15 创建，2026-09-16 按最新讨论修订。状态：待执行；未验收任何代码任务。

## 1. 基线与替代关系

本计划以本次对话最终结论为准，取代 2026-09-13 设计与执行计划中冲突的部分。旧文档保留讨论历史，不再作为以下行为的实施依据：

| 旧内容 | 当前结论 |
|---|---|
| 因子回写输入 mdataset | 时序、截面分别写独立结果 Dataset |
| 截面等 View 输入可读 | 截面等输入 DatasetPeriodCompleted，直接读 Primary |
| Collector/Merge/Factor 各发布数据周期完成 | 生产者通过 Storage 接口登记与报告，Dataset 层统一发布 |
| Merge 等 Collector 完成事件确定交集 | 周期开始读取源 Dataset 的已冻结成员快照确定交集 |
| 去掉 -SPOT/-SWAP 作为统一标准 | 用户配置标准对象映射，保留原始身份并校验冲突 |
| schema 变化自动重建 View | 仅提示受影响字段差异，用户手动触发现有 A/B 重建 |
| 每周期强制写不变标记 | 有变化写生效全量，未更新周期自动沿用最近生效快照；实际运行周期固定引用 |
| 成功写入后再带收据上报成功 | Storage 在完整提交路径内直接记录成功，额外接口仅报告缺失/失败 |

新项目无需兼容旧协议和数据。但不得删除与本任务无关的用户改动或线上资源。实施开始重新检查工作树、分支与实际入口；当前存在的缓存、引擎、周期账本优先验证复用，不按文件名盲目新建第二套实现。

## 2. 最终设计约束

### 2.1 Dataset 成员模型

每个 Dataset 有标的存储能力。协议统一使用 subject、subject_id、subject_ids、subject_snapshot 和 snapshot_id；不以 members 或 universe 表示同一业务集合。下文“成员快照”是历史中文称呼，均指 subject_snapshot；items 仅指批量结果项。

Collector 定期获取明确范围的全市场标的列表（交易所、现货/合约、市场状态等），有变化才保存新全量快照；不变只更新同步成功时间。时序周期未写新快照时，解析生效时间不晚于该周期的最近快照。周期开始处理时持久化 snapshot_id，后续更新不改变该周期。非时序 Dataset 使用已激活版本，参与聚合时也固定版本。

```text
09:00  FULL       snapshot=S1  [BTC, ETH]
09:01  无新快照，沿用 S1
09:02  无新快照，沿用 S1
09:03  FULL       snapshot=S2  [BTC, ETH, SOL]
09:04  无新快照，沿用 S2
```

不要求每周期写 UNCHANGED 标记。未运行周期通过生效时间索引查找最近快照，已运行周期直接使用固定引用，不逐周期回溯链。没有任何适用历史快照时阻塞，不能当空集合；获取列表失败保留旧快照并告警，不能把接口失败变成空列表。记录最近同步成功时间，对持续过期告警。显式空集合仍是合法快照。

快照不可变；已冻结周期相同引用幂等，冲突拒绝。快照清理保护周期引用，也保护仍能被后续周期沿用的最新生效快照及保留范围所需前驱；不能仅因暂时没有周期引用就删除。后续退市或映射变化不改变已启动周期。

### 2.2 生产责任和完成判断

基础 Dataset 由 Collector 登记；派生 Dataset 由对应计算或聚合程序登记。Storage 提供基础接口、授权、冻结、状态校验、持久化和可靠发布，不自行推导业务名单。

每个输出 Dataset 绑定一个明确生产任务；一个任务可包含多个因子，一个引擎进程可执行多个任务。Storage 判断完成依据为冻结成员及有效对象终态，不是 COUNT(*)，更不能用成功对象交集缩小名单。

```text
未登记成员 → 成员已冻结 → 对象处理中 → 全部终态
                                      → complete / degraded
                                      → DatasetPeriodCompleted
```

没有适用历史快照时阻塞，超时不发伪造完整事件；仅本周期没有更新应正常沿用。合法空快照可明确完成。成功状态由完整提交路径确认；缺失和失败保留在原始预期名单中。

### 2.3 通用接口草案

以下为实施时需映射到现有 Protobuf/RPC 的语义接口，不表示当前已存在：

```text
PutSubjectSnapshot(dataset_id, frequency, effective_period, request_id, subject_ids)
GetPeriodSubjects(dataset_id, frequency, period, page_cursor)
ReportPeriod(dataset_id, frequency, period, request_id, items)
GetDatasetPeriod(dataset, frequency, period)
```

PutSubjectSnapshot 返回 snapshot_id；GetPeriodSubjects 只解析或读取，不暗中启动所有被查询的周期。任务启动路径负责原子固定周期引用。沿用限定同 Dataset、频率及兼容语义。producer 从认证身份与 Dataset 生产任务配置确定，不由调用方任意指定。

ReportPeriod.items 每项仅包含 subject_id、status（missing 或 failed）及 reason。正常成功写入不再二次上报，也不要求对外暴露 commit_id。Storage 在完整提交时检查冻结成员和必需字段，直接记录成功；多次部分字段更新须直到全部必需输出完成才记录成功，不能收到任意字段变化就计数。周期固定所需输出和截止策略。

### 2.3.1 磁盘 KV 与增量汇总

逻辑键包括 subject_snapshot/{dataset}/{snapshot_id}、period/{dataset}/{freq}/{period}、subject_state/{dataset}/{freq}/{period}/{subject_id}，实际编码沿用现有 KV 规范。快照内容与生效索引、周期引用、状态及发布记录均持久化，内存只作加速。

同一个 KV 实例内，将完整业务数据、标的成功状态及待发布记录放入一个原子写批次。Storage 不订阅自己的 NATS 字段变更来确认成功。不跨 Primary 和另一 SQLite 假装原子事务，也不承诺跨节点原子写。

周期增量维护 expected、succeeded、missing、failed。更新状态时按完整业务键去重；在串行化或事务保护下仅首次终态转换增加计数，单独使用原子批量写不能防止并发重复计数。全部预期标的终态时调用 CompletePeriod，原子保存完成状态和待发布事件。每次只处理本次标的，不全量扫描周期数据；timer 处理截止、恢复和重试。

若 Dataset 跨 KV 实例或节点，本地提交同时保存可重放的内部状态记录，由周期归属节点幂等汇总；不是让 Storage 自订阅外部字段广播。汇总按各自有序域收集成功提交位置，View 的 WaitApplied 必须等所有相关流连续应用，不能用跨流单个最大序号判断。重复状态或乱序报告不得把成功改成失败，也不能重新打开已终态周期。

### 2.4 对象映射与交集

源范围 + 原始对象 ID 映射为标准对象 ID，保留原始交易类型、交易所、计价和品种信息。两个不同源的现货与合约可归一；同一源多行归一后冲突必须拒绝或先按用户规则加工，不静默选一条。

mdataset 预期成员为各源同周期快照标准化后的交集，不是成功行交集。快照缺失则等待；空快照则是真实空集合。双上市对象缺数据属于 missing_input；只在一个源上市的对象不在预期交集，不算缺失。

### 2.5 计算、缓存与 View

时序消费输入 Dataset 完整行事件，不等全集；截面消费目标 mdataset 的 DatasetPeriodCompleted，不因任意一行变更立即计算。两者都读 Primary，不依赖 View。

历史数据不修正；首次完整提交、重试和显式补算仍需定义清楚。缓存按 Dataset 身份与 schema 隔离，缺失回源，2233 秒检查总容量，保留最近配置 N 行重建新文件。缓存重建可自动，View 重建必须手动，两者不能混为一谈。

View 一对一关联 Dataset，Dataset 可有多个 View。字段差异只检测影响所选字段的变化。手动开始后沿用现有 A/B 构建、追平、原子切换、失败保留旧 A；已授权任务的自动恢复不等于自动触发新重建。

## 3. 工作方式与验收规则

- [ ] 记录 HEAD、分支、现有改动和模块基线；所有未完成项保持未勾选。
- [ ] 按任务依赖执行，先写失败测试，再最小实现，再回归和提交。
- [ ] 协议修改联动生产者、消费者、生成代码、ACL、配置和前端；不保留旧事件别名作为兼容。
- [ ] 表结构加载空 SQLite 验证；并发与持久化任务运行 race 和重启测试。
- [ ] 更新进度文档：每项记录文件、命令、测试结果、提交号、剩余风险。
- [ ] 部署、正式数据删除与实时验证在后续明确授权的阶段执行；本次不执行。

代码路径相对仓库根目录。标为新增的文件若已有对应实现，复用现有文件并补测试。宽目录范围表示需按实际接口定位，不能整目录无关重写。

## 4. 任务清单

### 01. 统一数据与事件协议

**依赖：** 无

**文件范围：** packages/storagepb/storage_events.proto；packages/events/registry.go、validation.go；modules/storage/proto/metadata.proto、dataset_markers.proto、primary_store.proto。

- [ ] 添加定向失败测试：事件缺 dataset_id、周期身份或关联完成 ID 必须拒绝；degraded 保留失败集合；重复完成报告稳定幂等。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：定义 DatasetPeriodCompleted、通用 ViewDataReady、周期成员登记/解析、对象终态报告、周期查询接口；明确写入位置、生产任务身份、周期身份和快照引用。删除旧生产方专用完成协议，生成所有调用方代码。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 02. 不可变全量成员快照存储

**依赖：** 01

**文件范围：** modules/storage/internal/service/datanode/pebble/；新增 subject_snapshot.go、subject_snapshot_test.go 及周期键编码文件，复用现有 KV 路由与持久化接口。

- [ ] 添加定向失败测试：无生效快照时阻塞；未更新周期沿用最近快照；空集合合法；不同频率不误引用；已固定周期不受后续更新影响；关库重开恢复内容与引用。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：在现有 KV 建立快照头、标的明细、生效时间索引和周期引用。排序去重并计算摘要，原始映射与规则身份纳入语义；有变化保存全量，无变化无需周期标记，不新增 SQLite 存储。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 03. 周期成员基础接口与授权

**依赖：** 02

**文件范围：** modules/storage/internal/service/catalog/；modules/storage/internal/service/metadata/；新增 membership API 与测试文件。

- [ ] 添加定向失败测试：无权调用拒绝；有适用快照可继承、无快照阻塞；分页稳定；并发固定周期引用一致；补录或未来快照不改变已启动周期；普通查询不启动周期。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：实现 PutSubjectSnapshot、GetPeriodSubjects 和周期状态查询；认证映射生产身份。任务启动显式固定解析出的引用，响应丢失重试恢复原记录。RPC 使用现有 KV 归属路由，不另存一份成员权威库。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 04. 完整行提交与对象终态

**依赖：** 01、03

**文件范围：** modules/storage/internal/service/primarystore/；modules/storage/internal/service/datanode/pebble/；modules/storage/proto/rows.proto。

- [ ] 添加定向失败测试：仅行数相等不能成功；越界对象拒绝；半字段不算完整；写成功响应丢失幂等恢复；不同因子列互不覆盖。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：验证标的范围、必需字段和认证生产身份；完整数据、subject 成功状态、待发布记录在同 KV 原子提交，不需要成功二次 RPC 或自订阅。多因子补丁只在必需输出齐全后记成功；ReportPeriod 仅受理 missing/failed。以串行化或事务保护并发状态转换，重复写入不重复计数。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 05. Dataset 周期完成状态机

**依赖：** 04

**文件范围：** 新增 modules/storage/internal/service/catalog/dataset_period.go；modules/storage/internal/service/datanode/pebble/ 的 period_state.go、period_state_test.go；Storage outbox 与测试。

- [ ] 添加定向失败测试：对象终态早到与晚到都正确；完成事件先于数据提交不可能；发布失败重启重试；零成员明确 complete；全部失败仍带原名单；无成员快照超时返回明确阻塞而非伪造完整事件。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：KV 增量记录标的终态和周期计数，最后一个终态到达时尝试 CompletePeriod；全部成功 complete，否则 degraded。成功位置由写入路径内部记录，不接受伪造成功上报。多节点采用本地可靠状态记录、固定汇总归属和去重应用；完成及发布意图在汇总 KV 原子写。timer 只处理超时/恢复，不重复扫全周期行。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 06. 对象标准化公共能力

**依赖：** 03

**文件范围：** modules/storage/internal/service/metadata/sqlite/crud_subject.go；新增对象映射领域/API/测试；web/src/views/data/subjects/index.vue。

- [ ] 添加定向失败测试：BTC-SPOT/BTC-SWAP 跨源映射相同 ID；同一源两个合约映射同 ID 时拒绝歧义，要求过滤或自定义聚合；改规则不改变已有周期；不能硬编码去后缀覆盖所有市场。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：提供 source_scope + raw_subject_id 到 canonical_subject_id 的显式映射和规则预览；保留现货/合约/交易所/计价等原始属性。映射快照不可变，周期固定引用。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 07. Collector 定期同步全市场标的

**依赖：** 03、05、06

**文件范围：** modules/collector/internal/marketfetch/period_readiness.go、period_reporter.go；modules/collector/internal/marketstorage/storage.go；对应测试。

- [ ] 添加定向失败测试：冻结前退市不入名单；冻结后退市记缺失；单纯采集失败不得减少成员；漏一个周期登记不能自动继承；重启沿用相同快照。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：提供指定市场范围的全量标的获取能力，定时比较并通过 PutSubjectSnapshot 写变化全量，不变只记录同步成功时间。周期启动解析并固定快照，采集完整写入自动记成功，仅缺失/失败调用 ReportPeriod。获取失败保留旧快照并告警；去掉 Collector 自有数据完整性权威。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 08. 派生 Dataset 配置与无环校验

**依赖：** 03、06

**文件范围：** modules/factor/internal/domain/、internal/store/、schema/factor.sql、proto/；新增 dataset_job.go 与测试。

- [ ] 添加定向失败测试：输入输出相同拒绝；A→B→A 拒绝；新增输出列合法；修改已启用基础映射拒绝；任务快照锁定后绑定更新不污染旧结果。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：定义计算任务输入/输出 Dataset、绑定、固定对象过滤和快照；输出不得等于输入。mdataset 输入来源和键规则启用后不可原地变更；校验完整依赖图无环。第一版一个输出 Dataset 一个明确生产任务，不限制一个引擎运行多个任务。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 09. 聚合周期交集登记

**依赖：** 06、07、08

**文件范围：** 新增或复用 modules/factor/internal/merge/ 的 membership.go、periods.go 与测试；独立 cmd/merge。

- [ ] 添加定向失败测试：只有现货/只有合约不入交集且不记 missing；双上市预期成员采集失败仍入交集并最终缺失；一个快照未登记不是空交集；交集为空可明确完成；规则/快照固定。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：通过 GetPeriodSubjects 解析各源生效快照，标准化后取交集，必要时 PutSubjectSnapshot 保存 mdataset 新全量并固定输出周期引用。不等采集完成；仅无适用历史快照才等待，同周期没更新可正常沿用。原 collector/membership 配置仅作为上游快照来源，不保留两套完整性协议。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 10. 聚合完整输入提交

**依赖：** 04、05、09

**文件范围：** modules/factor/internal/merge/assembler.go、consumer.go、store.go、对应测试；独立运行账本和配置。

- [ ] 添加定向失败测试：A 到达不写半行，B 到达一次提交；重复消息幂等；重启不丢 A；截止后迟到不静默改写；custom 模式零自动写入。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：等待同一标准对象所有必需源完整行；源字段用源DatasetID__字段名前缀。持久化待聚合输入，全部到齐才一次 CommitInput；向 Storage 报告对象终态。custom 模式不启动系统写入，用户程序遵守相同接口。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 11. 控制面和引擎独立部署边界

**依赖：** 08

**文件范围：** modules/factor/cmd/server/main.go、cmd/engine/main.go、internal/bootstrap/、catalogsync/。

- [ ] 添加定向失败测试：控制面无 Python 可启动；一个引擎执行多个 Dataset 任务；漏配置通知可恢复；重启不是重新 DeliverNew；引擎无权写其他任务 Dataset。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：控制面管理任务、异步补算和状态，不启动 Python/实时消费；引擎按任务配置动态订阅数据源。快照对账、durable 恢复、关闭排空和独立目录复用现有实现，不另建重复框架。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 12. 时序独立结果 Dataset

**依赖：** 04、05、08、11

**文件范围：** modules/factor/internal/trigger/subject_tasks.go、subject_runner.go、taskrunner/、storageio/、engine/。

- [ ] 添加定向失败测试：一对象先到即计算不等全集；结果事件不触发自身；窗口不含未来；空历史明确失败或跳过；失败对象仍在输出预期集合；旧任务不能覆盖新版本。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：只消费任务输入 Dataset 完整行，读取 Primary 历史窗口，compute(df, params, context) 后写另一结果 Dataset。输出成员由输入快照与固定任务过滤推导，在处理开始前登记；缺输入和计算失败报告为输出缺失/失败，不能删成员。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 13. Dataset DuckDB 缓存

**依赖：** 12

**文件范围：** modules/factor/internal/inputcache/、storageio/、bootstrap/cache.go；缓存回源和容量测试。

- [ ] 添加定向失败测试：冷启动不预填；同 schema 不同 Dataset 不混用；读不更新淘汰时间；N 行仍超限停止填充并回源；WAL/临时文件计入；删除缓存仍正确；不承诺定时软限是硬磁盘上限。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：Dataset ID + schema 标识隔离；完整 schema 建表、基础输入命中；缺数据回 Primary。schema 变化新建空代际不 ALTER；timer 2233s、保留最近 N 行、目录总预算、新文件 A/B 切换和旧读者排空。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 14. 截面由 Dataset 完成触发

**依赖：** 05、10、12

**文件范围：** modules/factor/internal/trigger/view_ready_runner.go、period_barrier.go、engine/；新增 dataset_period_runner_test.go。

- [ ] 添加定向失败测试：其他 Dataset 完成不触发；degraded 默认拒绝；显式降级策略不默默缩小预期集合；输入完成不等输出；输出重复键/越界拒绝；输出 Dataset 事件不会回环。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：删除截面 View 依赖，消费目标 mdataset 的 DatasetPeriodCompleted，读取 Primary 固定周期面板；默认只接受 complete。输出到独立截面结果 Dataset，按配置登记预期对象，报告任务输出终态，Storage 发布完成。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 15. 补算与周期身份隔离

**依赖：** 05、11、12、14

**文件范围：** modules/factor/cmd/cli/、internal/store/、internal/trigger/；控制面 RPC。

- [ ] 添加定向失败测试：补算不覆盖旧实时结果；重复请求返回原任务；取消不报 complete；生产者离线只受理；可解释成功/缺输入/失败/取消。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：实时周期冻结后不可改成员和完成状态。显式补算使用独立运行批次，必须锁定输出身份与版本：第一版输出到新的结果 Dataset，避免与已冻结实时周期共享覆盖权。控制面只受理，引擎执行，Storage 使用相同完成协议。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 16. View 单 Dataset 和手动 A/B

**依赖：** 01、05

**文件范围：** modules/storage/internal/service/view/build.go、maintenance.go、period_event_apply.go；metadata/sqlite/crud_view_rebuild.go；相关测试。

- [ ] 添加定向失败测试：字段改变只标记差异不启动 B；用户点击才启动；失败保留 A；重启恢复已提交重建；Dataset 新增未选列不判失效；删除/类型改变选中列不继续宣称正常。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：保留 A/B 构建、追平、验证、原子切换、旧读者排空；禁用自动启动重建的 schema/容量/周期维护入口，容量压力报警或明确退化而非偷偷重建。已由用户启动的任务可自动恢复和完成；首次创建索引是显式创建操作，不是后台 schema 自愈。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 17. 通用 ViewDataReady 和策略

**依赖：** 05、16

**文件范围：** modules/storage/internal/service/view/eventconsumer/、period_event_apply.go；modules/strategy/ 实际消费入口。

- [ ] 添加定向失败测试：跨分区未追平不发；手动重建后重新验证位置；结果无关的 View 不冒称可读；重复 Ready 策略幂等；生产者/计算角色不决定 View 业务逻辑。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：Dataset 完成携带成功数据的所有提交位置；View 按自己的配置范围和活动索引确认已应用，再发关联 completion_event_id 的 ViewDataReady。缺字段/旧定义不发布受影响成功通知。策略读 Primary 用 DatasetPeriodCompleted，读 View 用匹配的 ViewDataReady。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 18. 前端管理和诊断面板

**依赖：** 06、08、11、16

**文件范围：** web/src/api/modules/system/static-menu.ts、api/storage/、api/factor/、views/data/、现有采集与因子页面；新增导航和重建 Playwright 用例。

- [ ] 添加定向失败测试：一 Dataset 多 View 可管理；派生字段不混入基础字段；成员未登记与空集合不同显示；重建前展示变更范围；桌面/移动无重叠；刷新直达路由可用。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：基础资产归数据采集：数据源、采集对象/标准化、基础字段、采集任务、基础 Dataset。派生 Dataset/任务归因子；详情增加周期成员、差异、完成状态、源快照引用和 View 索引页。重建按钮展示差异、进度、失败和重试。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 19. 快照清理与运行可观测性

**依赖：** 02—18

**文件范围：** Storage 快照引用清理、各模块 metrics、配置模板、README；新增 GC/告警测试。

- [ ] 添加定向失败测试：09:00 快照仍被 10:00 引用时不能删；并发登记与 GC 不悬空；未登记名单超时报警；规则冲突可定位；日志不包含凭据。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：先删除过期周期引用，再删除无人引用且无运行任务租用的快照；快照全量不与产生周期捆绑清理。记录等待名单、待源输入、失败对象、周期状态、缓存容量、索引差异和重建原因。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 20. 完整验证与交付

**依赖：** 01—19

**文件范围：** 新增 modules/factor/internal/integration/dataset_membership_pipeline_test.go；部署构建文件；新的进度文档。

- [ ] 添加定向失败测试：两源双上市交集正确；退市生效边界正确；崩溃/重投无漏任务；缓存删除仍计算；View 手动重建不阻塞时序或截面；真实新周期结果可核算。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：覆盖 Collector→基础 Dataset→时序结果→交集 mdataset→截面结果→View→策略链路，故障注入后新启动 codeCR 审查，主 Agent 独立核验。部署属于后续获授权执行阶段；不以文档完成代替实现。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

## 5. 状态与算法验收伪代码

### 成员快照复用

```text
PutSubjectSnapshot(scope, effective_period, subjects):
    validate(subjects)
    kv_batch: save immutable snapshot and effective index

StartPeriod(scope, period):
    if frozen reference exists: return it
    snapshot = latest effective snapshot at or before period
    require(snapshot exists)
    serialize: persist snapshot reference unless already frozen
```

允许未更新周期沿用最近生效快照，不允许把查询失败当空集合。StartPeriod 是内部周期准入操作的建议名称，不要求新增外部 RPC；普通 GetPeriodSubjects 查询不启动周期。

### 聚合

```text
snapshots = GetPeriodSubjects(each_source, P)
if any snapshot missing:
    RetryOrTimeout()
else:
    expected = intersection(normalize_each_source(snapshots))
    PutSubjectSnapshot(output, P, expected) if changed
    StartPeriod(output, P)
    for subject in expected:
        if all_required_source_rows_complete(subject):
            CommitInput(subject)
        elif deadline_reached:
            ReportPeriod(items=[{subject_id, status: missing}])
```

### Dataset 完成

```text
CommitInput(subject):
    serialize state transition
    kv_batch: data + success state + pending events
    advance local counters or durable cross-node aggregation

CompletePeriod():
    require(frozen subjects and all terminal)
    kv_batch: final period state + completion event
    publish DatasetPeriodCompleted with stable identity
```

### View 可读

```text
on DatasetPeriodCompleted:
    SavePending()
    WaitApplied()
    require(selected_fields_compatible && relevant_scope_readable)
    publish ViewDataReady(completion_event_id, view_id, scope)
```

## 6. 测试命令与生成入口

在仓库根目录执行事件和 RPC 生成；只对真实发生协议变更的模块运行生成，检查生成器未覆盖手写辅助文件：

```bash
make -C packages/storagepb generate
make -C modules/storage/proto all
git diff --check
```

在以下各自模块目录执行，不用仓库根目录 go test ./... 替代多模块验证：

```bash
# packages/events、packages/storagepb
go test ./...

# modules/storage、modules/collector、modules/factor
env CGO_ENABLED=1 go test ./...
env CGO_ENABLED=1 go test -race ./internal/... -count=1

# modules/strategy
go test ./...

# web
pnpm test
pnpm run check:menu
pnpm run check:data-browse
pnpm run build:prod
```

前端新增 tests/dataset-membership.spec.ts、tests/view-manual-rebuild.spec.ts、tests/factor-dataset-pipeline.spec.ts，并在 web 下执行：

```bash
pnpm exec playwright test tests/dataset-membership.spec.ts tests/view-manual-rebuild.spec.ts tests/factor-dataset-pipeline.spec.ts
```

预期：测试实际运行并通过，不能把 no tests to run 当验收；缺少工具、Python、浏览器或 CGO 是环境阻塞而非功能通过。已有基线失败单独记录，不掩盖新增回归。

## 7. 故障注入矩阵

| 场景 | 预期 |
|---|---|
| 成员写入成功、响应丢失 | 同 request_id 恢复同快照与周期引用 |
| 周期中途退市 | 当前冻结成员不删除，缺失明确终态 |
| 新周期退市已生效 | 新快照删除该对象，交集重新计算 |
| 一源本周期未更新快照 | 沿用最近生效快照；无适用历史快照才等待/超时，不能当空集合 |
| 同一源映射碰撞 | 明确拒绝，不覆盖 |
| 所有源有快照但交集为空 | 登记合法空快照，明确完成 |
| 聚合只到一个源后重启 | 恢复部分到达状态，不提交半行 |
| 因子失败一个对象 | 输出名单不缩小，Dataset degraded |
| Storage 完成状态提交后发布失败 | outbox 或等价可靠恢复重试 |
| View 字段类型变化 | 不自动重建，展示受影响差异，禁止受影响的成功可读声明 |
| 用户启动 B 构建后进程退出 | 恢复这次任务，不新建未经授权的重建 |
| 缓存超限且磁盘不足 | 停填充、回源、告警，不影响权威状态 |
| 清理快照时新周期引用旧快照 | 事务/引用保护避免悬空 |
| 下游任务尚在运行但周期数据到期 | 运行引用保护名单和完成收据 |

## 8. 落地前需要固定的实现参数

以下为本计划建议的第一版落地规则，不新增通用复杂框架：

- 成员未就绪的等待截止、输入行截止及任务执行超时分别配置，不共用一个含混 timeout。
- 启用周期任务时先解析最近生效快照并固定引用，再准入数据；无需每周期重复登记。先到事件可靠暂存或延迟重试，禁止 ACK 后遗忘。
- 实时与补算隔离：第一版显式补算写新结果 Dataset，避免改写已经完成的实时周期。
- 已终态周期迟到数据记录诊断，不自动重开；历史修正不在范围内。
- 一输出 Dataset 一生产任务，成员接口授权由 Storage 强制验证。
- 手动 View 重建覆盖 schema 差异及现有自动维护重建入口；不停止普通实时索引更新。
- 快照读取先采用查询和可靠重试，不新增成员快照就绪事件；已有 DatasetPeriodCompleted 和 ViewDataReady 足够表达完成/可读。
- 生产任务配置、映射快照可版本化；不重新引入独立 input_contract_version。

## 9. 最终交付检查

- [ ] 基础采集、时序结果、mdataset、截面结果均可查询周期成员快照及完成状态。
- [ ] 时序、截面均不读 View，输入输出分离且依赖图无环。
- [ ] 所有外部数据完整性消费统一 DatasetPeriodCompleted，旧生产方专用事件无活跃调用。
- [ ] ViewDataReady 只表达指定完成批次在指定索引可读。
- [ ] 数据采集前端包含基础资源和对象标准化；因子页面包含任务、派生数据和运行诊断。
- [ ] 新启动 codeCR 审查，主 Agent 核验并修复发现；记录残余风险。
- [ ] 按授权部署独立控制面、聚合程序、引擎，验证网络、凭据和进程职责。
- [ ] 记录真实新周期的对象名单、快照 ID、标准映射、输入提交、输出结果、完成事件和可读事件；结果与直接计算对照。
- [ ] 区分本地测试、模拟 E2E、实际部署和真实数据验收，不互相替代。
- [ ] 提交本次实施范围并推送，保留无关用户改动；未验收项不标完成。

本计划完成仅代表任务拆解完成，不代表上述功能已实现。后续执行需逐项回填证据。
