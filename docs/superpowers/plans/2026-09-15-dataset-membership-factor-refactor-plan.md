# Dataset 成员快照与因子计算链路重构执行计划

> **执行要求：** 使用 executing-plans 逐项执行。代码审查使用 codeCR，审查后由主 Agent 独立核验。本文只生成计划，不授权本轮实施或部署。

**目标：** 将成员快照、数据完整性和周期完成提升为 Storage Dataset 通用能力，统一对象身份，分离时序与截面输出，保留手动触发的 View A/B 重建。

**架构：** 生产者在周期开始登记成员，Storage 保存不可变全量快照与显式周期引用并发布 DatasetPeriodCompleted；时序读取输入 Dataset 写独立结果，聚合按源周期成员交集构造 mdataset，截面按其完成事件读取 Primary。View 只作为单 Dataset 的查询子索引。

**技术栈：** Go 多模块、tRPC、Protobuf、NATS JetStream、Storage Primary、SQLite 元数据/运行账本、DuckDB 本机缓存、Python、Vue/TypeScript、Vitest/Playwright。

**日期：** 2026-09-15。状态：待执行；未验收任何代码任务。

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
| 每周期存全量成员或直接向前猜测继承 | 每周期显式引用不可变全量快照；不变时复用，有增删时新全量 |

新项目无需兼容旧协议和数据。但不得删除与本任务无关的用户改动或线上资源。实施开始重新检查工作树、分支与实际入口；当前存在的缓存、引擎、周期账本优先验证复用，不按文件名盲目新建第二套实现。

## 2. 最终设计约束

### 2.1 Dataset 成员模型

每个 Dataset 有成员存储能力。时序 Dataset 每个频率、每个业务周期必须显式登记成员快照。非时序 Dataset 可使用已激活成员版本；参与周期聚合时必须固定其对应版本，不能边计算边读最新成员。

```text
09:00  FULL       snapshot=S1  [BTC, ETH]
09:01  UNCHANGED  snapshot=S1
09:02  UNCHANGED  snapshot=S1
09:03  FULL       snapshot=S2  [BTC, ETH, SOL]
09:04  UNCHANGED  snapshot=S2
```

UNCHANGED 是明确引用，不是缺记录时默认继承。查询直接解析 snapshot_id，不逐周期回溯链；首个周期必须登记全量。即使空集合也有合法快照。全量存储没有增删差量链，有变化就生成新的完整集合。

快照不可变；同周期相同提交幂等，不同名单拒绝。已结束周期不能受迟到成员、后续退市或映射规则变化影响。快照清理按引用关系执行，不能删掉仍被后续周期使用的早期全量。

### 2.2 生产责任和完成判断

基础 Dataset 由 Collector 登记；派生 Dataset 由对应计算或聚合程序登记。Storage 提供基础接口、授权、冻结、状态校验、持久化和可靠发布，不自行推导业务名单。

每个输出 Dataset 绑定一个明确生产任务；一个任务可包含多个因子，一个引擎进程可执行多个任务。Storage 判断完成依据为冻结成员及有效对象终态，不是 COUNT(*)，更不能用成功对象交集缩小名单。

```text
未登记成员 → 成员已冻结 → 对象处理中 → 全部终态
                                      → complete / degraded
                                      → DatasetPeriodCompleted
```

成员缺失不能解释为空集合。未登记超时属于阻塞/错误，不发伪造的完整周期事件。零成员已登记可明确完成。成功状态必须证明对应必需字段已经提交；缺失和失败保留在原始预期名单中。

### 2.3 通用接口草案

以下为实施时需映射到现有 Protobuf/RPC 的语义接口，不表示当前已存在：

```text
RegisterPeriodMembers(dataset, frequency, period, producer,
                      request_id, full_members | reuse_snapshot_id)
GetPeriodMembers(dataset, frequency, period, page_cursor)
ReportPeriodItems(dataset, frequency, period, producer,
                  request_id, terminal_items_with_commit_receipts)
GetDatasetPeriod(dataset, frequency, period)
```

成员登记返回 snapshot_id 与固定周期身份；沿用必须引用同 Dataset、兼容频率及成员语义的已存在快照。周期配置固定必需输出字段及截止策略。状态报告必须验证对象属于快照、生产者权限和提交收据；不允许调用者用一个 all_done=true 绕过校验。

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

**文件范围：** modules/storage/schema/metadata.sql；新增 modules/storage/internal/service/metadata/sqlite/crud_dataset_membership.go、crud_dataset_membership_test.go。

- [ ] 添加定向失败测试：首周期不变但无快照拒绝；空集合可冻结；相同名单可复用；不同频率不误引用；已冻结周期冲突提交失败。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：建立快照头、成员明细、周期引用。成员排序去重并计算集合摘要；原始对象映射信息和标准化规则身份纳入快照语义，不能只对标准 ID 做 hash。频率独立，保存全量或引用已有全量。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 03. 周期成员基础接口与授权

**依赖：** 02

**文件范围：** modules/storage/internal/service/catalog/；modules/storage/internal/service/metadata/；新增 membership API 与测试文件。

- [ ] 添加定向失败测试：无权调用拒绝；未登记不自动继承；大名单分页完整且顺序稳定；两生产者并发冲突只有一个成功；未来周期补录不改变旧周期。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：实现分页读取完整成员、登记全量、显式沿用、查询未登记/已冻结；只有 Dataset 授权生产者可以登记。快照与周期引用原子创建，响应丢失重试返回同一记录。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 04. 完整行提交与对象终态

**依赖：** 01、03

**文件范围：** modules/storage/internal/service/primarystore/；modules/storage/internal/service/datanode/pebble/；modules/storage/proto/rows.proto。

- [ ] 添加定向失败测试：仅行数相等不能成功；越界对象拒绝；半字段不算完整；写成功响应丢失幂等恢复；不同因子列互不覆盖。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：验证成员范围、必需字段及生产任务身份；完整行提交返回可靠收据。生产者报告成功时必须关联真实提交；缺失/失败无需伪造空行。允许一个任务内多个因子列按补丁写入，但只有全部必需输出完成才确认对象成功。
- [ ] 重跑定向测试和触及模块回归；持久化/并发任务增加进程退出、重放及 race 测试。
- [ ] 检查协议与调用方一致，记录验收证据；执行暂存差异检查，独立提交本任务范围。

### 05. Dataset 周期完成状态机

**依赖：** 04

**文件范围：** 新增 modules/storage/internal/service/catalog/dataset_period.go；modules/storage/internal/service/metadata/sqlite/ 周期账本；Storage outbox 与测试。

- [ ] 添加定向失败测试：对象终态早到与晚到都正确；完成事件先于数据提交不可能；发布失败重启重试；零成员明确 complete；全部失败仍带原名单；无成员快照超时返回明确阻塞而非伪造完整事件。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：Storage 汇总冻结成员与对象终态，持久化完成状态和可靠发布意图；全部成功 complete，有缺失/失败 degraded。截止策略由登记配置明确，超时扫描不能把未登记名单当空名单。成功提交位置收集齐才发布 DatasetPeriodCompleted。
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

### 07. Collector 提前登记成员

**依赖：** 03、05、06

**文件范围：** modules/collector/internal/marketfetch/period_readiness.go、period_reporter.go；modules/collector/internal/marketstorage/storage.go；对应测试。

- [ ] 添加定向失败测试：冻结前退市不入名单；冻结后退市记缺失；单纯采集失败不得减少成员；漏一个周期登记不能自动继承；重启沿用相同快照。
- [ ] 运行该包测试，确认失败断言确实对应缺失行为，记录红灯结果；编译工具缺失不算有效红灯。
- [ ] 实现：在周期任务启动前按生效成员登记全量或显式沿用，采集成功/失败调用 Storage 通用接口；去掉 Collector 自有完成事件权威。保留运行明细和采集监控，不复制 Dataset 周期账本。
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
- [ ] 实现：通过 Storage 周期成员接口读取各源同周期快照，标准化后取交集，登记 mdataset 周期成员。不再等待采集完成事件确定名单；metadata 尚无周期快照时重试。原 collector/membership 配置适配成成员登记来源，不让两个运行协议并存。
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
register_period(P, Full(M)):
    normalize_and_validate(M)
    transaction:
        existing = lookup_period(P)
        if existing: require_same_content_and_producer(existing)
        else:
            S = create_or_reuse_immutable_snapshot(M)
            insert_period(P, snapshot=S, frozen=true)

register_period(P, Reuse(S)):
    require(S exists and belongs_to_same_scope)
    atomically_insert_or_verify_identical_period_reference(P, S)
```

不允许用“查询最近快照失败则空集合”或“周期未登记则沿用”替代显式写入。

### 聚合

```text
snapshots = get_each_source_period_members(P)
if any snapshot missing:
    wait_with_deadline_and_error_state()
else:
    expected = intersection(normalize_each_source(snapshots))
    register_output_period(expected)
    for subject in expected:
        if all_required_source_rows_complete(subject):
            commit_complete_output_once(subject)
        elif deadline_reached:
            report_missing(subject)
```

### Dataset 完成

```text
require(period_members_frozen)
require(all expected members have valid terminal states)
require(all success states reference committed required output)
persist_completion_and_reliable_publish_intent_atomically()
publish DatasetPeriodCompleted with stable identity
```

### View 可读

```text
on DatasetPeriodCompleted:
    persist_pending_receipt()
    wait_until_all_required_positions_applied_in_active_view()
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
| 一源周期快照缺失 | 等待/超时错误，不能当空集合 |
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
- 启用生产任务时先登记其将处理的周期成员，再准入该周期数据；先到事件可靠暂存或延迟重试，禁止 ACK 后遗忘。
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
