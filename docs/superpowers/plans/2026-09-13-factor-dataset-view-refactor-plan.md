# 复合因子数据集与单数据集 View 重构执行计划

> **执行说明：** 实施时使用 writing-plans 对应的 executing-plans 技能逐项推进；需要分工时使用 subagent-driven-development。代码审查使用 codeCR，等待审查完成后主 Agent 独立核验。本文仅为计划，不授权本轮编码或部署。

**目标：** 将基础采集、复合构造、因子计算、索引查询分离，交付 Dataset 驱动的时序计算、通用 ViewDataReady 和独立部署程序。

**架构：** Collector 写基础 Dataset，Merge 将完整基础行写入 mdataset，时序直接消费 Storage 变更，截面消费关联输入完成的 ViewDataReady。因子以字段补丁写回 mdataset，View 只维护所属唯一 Dataset 的子索引。

**技术栈：** Go 多模块、tRPC、Protobuf、NATS JetStream、Storage Primary、SQLite 运行账本、DuckDB 缓存、Python、Vue/TypeScript、Vitest、Playwright。

**设计基线：** [重构设计文档](../specs/2026-09-13-factor-dataset-view-refactor-design.md)，包含 2026-09-13 最新事件和版本精简结论。

**当前状态：** 所有实施项均未在本计划中验收。代码中已经存在独立引擎入口、缓存和任务账本等基础，不应重复建设；必须先测试确认再复用。此处文件路径相对仓库根目录。

---

## 1. 执行边界与前置步骤

- [ ] 记录执行分支、HEAD、工作树状态及各模块基线失败；已有改动不得覆盖或回滚。
- [ ] 阅读仓库 AGENTS.md、设计全文及本计划。实现基线是执行时工作树，不机械套用旧拆分计划。
- [ ] 先查看现有实现再修改。任务中“新增”的路径若已存在，改为复用并补测试，禁止创建职责重复的第二套实现。
- [ ] 每项按“失败测试 → 最小实现 → 定向测试 → 回归 → 独立提交”执行；测试失败必须由目标行为缺失导致，不能用编译环境缺失充当红灯证据。
- [ ] 每个任务完成后在进度文档记录文件、命令、结果、提交号和尚未解决的依赖；没有完成的项不勾选。
- [ ] 数据模型/协议变更是原子交付组：生成代码、生产者、消费者、配置和测试同步修改，不要求组内每个中间编辑都能独立运行。
- [ ] 不保留旧字段、旧事件别名或数据迁移适配。删除旧能力须同时删除调用方，不能只删定义让编译失败。
- [ ] 本轮不执行服务启停、正式数据清理或部署；后续实施阶段部署前确认目标环境和操作范围。

## 2. 文件与责任分组

| 责任 | 现有入口/目标位置 | 写入边界 |
|---|---|---|
| 事件协议 | packages/storagepb、packages/events、modules/storage/proto | 统一协议与生成产物 |
| Storage/View | modules/storage/internal/service、schema/metadata.sql | 原子输入、列补丁、单 Dataset 索引、可读进度 |
| Collector | modules/collector/internal/marketfetch、marketstorage、store | 采集名单及周期终态 |
| 因子控制面 | modules/factor/cmd/server、internal/domain、store、catalogsync | 定义、不可变快照、异步任务 |
| Merge | modules/factor/cmd/merge、internal/merge（新增） | 只写基础输入与构造账本 |
| 引擎 | modules/factor/cmd/engine、internal/trigger、taskrunner、engine | 只写绑定拥有的因子列 |
| 缓存 | modules/factor/internal/inputcache、storageio | 可丢弃本机缓存，不保存权威任务状态 |
| 前端 | web/src/api、router、现有采集/存储/因子页面 | 基础与派生资产分域导航 |
| 交付 | 模块 Makefile、配置模板、web-host、进度文档 | 独立构建与分层验证 |

建议按协议、Storage、Merge、引擎、前端分别形成小提交组；共享 proto/schema 文件同一时刻只由一个负责人编辑。新 Merge 独立进程放在 factor Go 模块内复用现有授权、配置与存储客户端，不要求新增一套 Go 模块。

## 3. 固定接口不变量

以下为实现与测试的行为约束，伪代码不是已经存在的 API：

```text
RowKey = (dataset_id, subject_id, frequency, period_time, series_tag)
InputCommit = (stable_commit_id, RowKey, complete_base_fields, input_ready)
TaskKey = (dataset_id, RowKey, config_snapshot_id, binding_version)
Completion = (event_id, batch_id, dataset_id, config_snapshot_id,
              expected_scope_ref, terminal_status, failures, committed_positions[])
ViewReady = (event_id, view_id, view_config_id, completion_event_id,
             dataset_id, visible_scope, status)
```

- 不新增 input_contract_version；mdataset 输入语义启用后不可变，输入变更创建新 Dataset。
- Storage schema 标识只证明结构，不代替配置快照；相同 schema 的不同 Dataset 不共享缓存。
- 完成消息所引用的对象名单必须不可变且可读取，不能只是没有解析来源的 hash。
- 写入位置按真实提交有序域区分；单一全局数字不能代替多个独立节点/分区。
- ViewReady 只证明其 visible_scope，不能代表任意因子列、任意对象都已可读。
- 空值与缺字段不同；缺字段不能通过置零/置 null 伪装成完整输入。
- 同 Dataset 的因子写入不得覆盖基础字段或其他绑定输出。
- 普通实时数据不修正历史；显式 Recalc 使用独立任务批次，不静默改变旧终态。

## 4. 任务依赖

主链为协议 → Storage 单数据集与写入保障 → Merge → 时序/截面 → 完成汇总 → 前端/交付。控制面拆分在 mdataset 定义后推进；缓存必须先有可独立正确运行的 Primary 读路径，再作为优化接入。

任务 01—05 为基础协议组，06—08 为构造组，09—16 为计算组，17 为界面组，18 为最终交付组。任何阶段均不得以临时“全部已完成”事件越过尚未实现的屏障。

## 5. 详细任务

### 任务 01：协议与状态身份

**依赖：** 无。
**文件：** 修改 packages/storagepb/storage_events.proto、packages/events/registry.go、packages/events/validation.go、modules/storage/proto/dataset_markers.proto；新增 packages/events/view_data_ready_test.go。

- [ ] **先写失败用例：** 旧事件注册失败；缺关联事件或写入位置的可读声明校验失败；重复消息保持稳定事件 ID；degraded 不转换成 complete。
- [ ] **运行红灯测试：** 在 `packages/events` 执行 `go test ./... -run 'TestViewDataReadyContract' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 定义 CollectorPeriodCompleted、MergePeriodCompleted、ViewDataReady；删除旧 subject-ready 和专用 View 周期协议，不留别名。统一完成标记包含 batch_id、config_snapshot_id、dataset_id、对象清单引用、状态和提交位置集合；ViewDataReady 关联 completion_event_id、view_id、View 配置身份及数据范围。

```text
decode(ready); require(completion_event_id != ""); require(dataset_id != ""); require(view_id != "")
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 02：Storage 单 Dataset View 模型

**依赖：** 01。
**文件：** 修改 modules/storage/proto/metadata.proto、modules/storage/proto/view.proto、modules/storage/schema/metadata.sql、modules/storage/internal/service/view/period_event_apply.go、modules/storage/internal/service/view/build.go；新增 modules/storage/internal/service/view/single_dataset_test.go。

- [ ] **先写失败用例：** 同 Dataset 创建两个不同投影 View 均成功；提交多个来源失败；重建后过滤和字段投影保持一致；删除一个 View 不删除 Dataset 或另一个 View。
- [ ] **运行红灯测试：** 在 `modules/storage` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestSingleDatasetView' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 将 View 的多 Dataset/primary Dataset 关系收敛为唯一 dataset_id；保留一对多索引、字段投影、对象过滤和历史范围；移除 JOIN/多源 enrich。重建和恢复仅按所属 Dataset 工作，禁止旧列表字段绕过校验。

```text
create_view(dataset=A, columns=[close]); create_view(dataset=A, columns=[volume]); assert(view_count(A) == 2)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 03：基础输入提交和字段写权限

**依赖：** 01。
**文件：** 修改 modules/storage/proto/rows.proto、modules/storage/internal/service/primarystore/、modules/storage/internal/service/datanode/pebble/；新增 modules/storage/internal/service/datanode/pebble/input_commit_test.go。

- [ ] **先写失败用例：** 普通客户端伪造 factor/merge 来源不能越权；并发因子列更新互不覆盖；相同提交重试幂等；基础字段不完整不产生 ready；成功写入但响应丢失后可查询原提交收据。
- [ ] **运行红灯测试：** 在 `modules/storage` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestInputCommit' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 提供受权限约束的完整输入提交与因子字段补丁两类操作；完整输入与 ready 元数据原子提交。返回可靠写入位置，以 Storage 验证的操作类别生成事件。检查字段归属、绑定版本和对象范围，冲突重试不能更改已提交基础内容。

```text
commit_input(key, all_required_fields, id); patch_factor(key, owned_columns, binding_version); assert(base_fields_unchanged)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 04：通用 ViewDataReady 应用屏障

**依赖：** 02、03。
**文件：** 修改 modules/storage/internal/service/view/period_event_apply.go、modules/storage/internal/service/view/eventconsumer/、modules/storage/internal/service/view/build.go；新增 modules/storage/internal/service/view/data_ready.go、data_ready_test.go。

- [ ] **先写失败用例：** 完成标记先到而行未应用不发；两个分区只完成一个不发；过滤范围合法时生成自己的范围通知；重建/重启后不误发；同一完成事件的发布重试保持幂等。
- [ ] **运行红灯测试：** 在 `modules/storage` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestViewDataReadyFence' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 持久化待确认完成标记及活动索引应用进度。逐数据节点/store/分区验证所有所需提交位置已应用；缺位置不发布。进度只用于可读证明，不恢复历史修正体系。重建期间绑定索引代际，切换后重新确认；发布失败可靠重试。

```text
ready = all(required_positions[p] <= applied_positions[p] for p in required_positions); publish_only_if(ready && active_index_readable)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 05：Collector 周期事件改名

**依赖：** 01、03。
**文件：** 修改 modules/collector/internal/marketfetch/period_reporter.go、modules/collector/internal/marketstorage/storage.go、modules/collector/internal/store/period_readiness.go；修改 Storage marker RPC 与事件消费者；新增 modules/collector/internal/marketfetch/collector_completed_test.go。

- [ ] **先写失败用例：** 全成功为 complete；有超时为 degraded 且 SubjectIds 不缩小；重启不改变冻结名单；重复上报不重复创建批次。
- [ ] **运行红灯测试：** 在 `modules/collector` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestCollectorCompleted' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 将逻辑名称和协议统一为 CollectorPeriodCompleted；保留冻结对象集合和成功/超时语义。所有生产者、订阅配置、CLI、测试与文档同时更新，不只修改事件常量。

```text
assert(completed.expected_subjects == frozen_subjects); assert(status == complete only_if all_success)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 06：mdataset 配置与权威边界

**依赖：** 02、03。
**文件：** 修改 modules/factor/schema/factor.sql、modules/factor/internal/domain/、modules/factor/internal/store/；新增 modules/factor/internal/domain/merged_dataset.go、merged_dataset_test.go、modules/factor/internal/store/merged_dataset.go。

- [ ] **先写失败用例：** 不同频率、时间边界不一致、字段前缀冲突拒绝；启用后修改来源拒绝；不同 Dataset 身份不会复用缓存；Storage 创建成功响应丢失后不重复建资源。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestMergedDatasetDefinition' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 保存来源、键规则、对象集、merge_mode、字段映射及不可变配置快照；实际 schema 从 Storage 读取。不增加 input_contract_version。启用后拒绝原地改变基础输入语义；输出绑定更新采用新配置快照。为创建失败记录可重试状态，避免跨库伪事务。

```text
if enabled && input_semantics_changed: reject(); else persist_snapshot(); reconcile_storage_resources_idempotently()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 07：独立 Merge 程序和持久化行聚合

**依赖：** 05、06。
**文件：** 新增 modules/factor/cmd/merge/main.go、modules/factor/internal/merge/assembler.go、store.go、consumer.go、assembler_test.go；修改 modules/factor/schema/factor.sql。Merge 使用单独运行库，不与控制面共享数据库文件。

- [ ] **先写失败用例：** A 到达不写可计算行，B 到达仅写一次；另一对象缺源不阻塞当前对象；重启恢复 A；重复 B 幂等；不完整字段不计入到齐；custom 模式系统零写入。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestMergeAssembler' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 默认以 dataset、配置快照、对象、频率、周期、series_tag 聚合；完整源字段使用源DatasetID__字段名。持久化来源到达状态；全部源到齐后原子提交输入；成功后 ACK。custom 模式不注册系统写入任务。

```text
persist_arrival(key, source, fields); if all_required_sources_complete(key): commit_input_once(key)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 08：Merge 周期账本和超时

**依赖：** 07。
**文件：** 新增 modules/factor/internal/merge/periods.go、periods_test.go、reporter.go；修改 modules/factor/schema/factor.sql。

- [ ] **先写失败用例：** 498 成功和 2 超时保留 500 预期对象；关闭后迟到不改写；标记上报失败可恢复；零对象、全部缺失可明确结束且不伪造输入；多源 Collector 结束不代表 Merge 写入结束。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestMergePeriodLedger' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 周期开始冻结 mdataset 对象名单和快照；按成功/缺失汇总，截止后固定 degraded，不写空数据行。完整输入提交收据持久化后才报告 MergePeriodCompleted。迟到实时输入不改变终态，明确记录迟到原因；自定义程序复用同一终态接口。

```text
freeze(expected); finalize_at_deadline(success, missing); publish_after_all_success_receipts_persisted()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 09：控制面与运行资源彻底隔离

**依赖：** 06。
**文件：** 修改 modules/factor/cmd/server/main.go、modules/factor/cmd/engine/main.go、modules/factor/internal/bootstrap/、modules/factor/internal/catalogsync/；新增 modules/factor/internal/bootstrap/role_boundary_test.go。

- [ ] **先写失败用例：** 控制面不依赖 Python 也能启动；控制面重启不影响引擎已有任务；漏配置事件可恢复；无授权节点启动失败；关闭过程不会提前关闭仍使用的数据库。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestRoleBoundary' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 复用已存在的 control/engine 资源和配置快照实现；移除控制面旧实时消费者及 Python 初始化。Merge 与 engine 独立目录、凭据和生命周期；引擎启动快照同步成功后再消费。配置通知丢失由快照对账恢复，关闭时先停准入再排空资源。

```text
open_control_without_python(); start_engine_after_snapshot(); stop_admission(); drain(); close_resources()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 10：Dataset 驱动时序触发

**依赖：** 03、07、09。
**文件：** 修改 modules/factor/internal/trigger/subject_tasks.go、subject_runner.go、subject_batcher.go、modules/factor/internal/taskrunner/；新增 modules/factor/internal/trigger/dataset_rows_test.go。

- [ ] **先写失败用例：** 因子回写事件不触发；重复源事件仅一个有效任务；两个对象到达不等全集；旧绑定结果提交被拒绝；首次 durable 起点明确，重启恢复积压而非重新 DeliverNew。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestDatasetRowsTrigger' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 用 mdataset 完整基础输入事件替换 subject-ready 入口；删除 ActiveIndex/InputContract 等旧 View 依赖。只消费已绑定 Dataset，验证操作类型和 ready；短窗口微批，持久化任务身份和终态；任务结果可能先于周期标记到达。

```text
if event.kind != input_commit || !event.input_ready: ignore(); enqueue_once(dataset, row_key, snapshot, binding)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 11：Primary 历史读取与统一 Python ABI

**依赖：** 10。
**文件：** 修改 modules/factor/internal/storageio/、modules/factor/internal/taskrunner/read_pipeline.go、modules/factor/internal/engine/；新增 modules/factor/internal/storageio/dataset_window_test.go。

- [ ] **先写失败用例：** 窗口没有未来行；缺历史明确跳过或失败；JSON/null/整数精度不丢；空对象集合不退化成全量读；Python 错误不写成功输出；截面输出重复键或越界对象拒绝。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestDatasetWindow' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 改为读取 mdataset Primary 的单对象/多对象窗口；按行数、字段数和响应字节预算分批，限制回源并发。复用 compute(df, params, context)，引擎从定义读取 factor_type。校验类型、唯一对象键、当前周期输出和最小样本。

```text
window = read_primary(subjects, end=period, limit=lookback); assert(all(row.time <= period)); compute(df, params, context)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 12：缓存 Dataset 化和惰性回源

**依赖：** 06、11。
**文件：** 修改 modules/factor/internal/inputcache/manager.go、query.go、modules/factor/internal/storageio/；新增 modules/factor/internal/storageio/dataset_cache_test.go。

- [ ] **先写失败用例：** 冷缓存回源一次后命中；删除缓存仍正确；新增输出列新建空代际；两 Dataset 相同 schema 不混用；源确认缺失不会无限重试；半字段缓存不能命中完整输入。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestDatasetCache' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 复用已有 DuckDB 管理器，缓存身份改为 Dataset 与 schema 标识；全 schema 建表、只以完整基础字段判定命中。缺键/缺窗口回源；只接受 ready 行。新增 schema 建空库，输出列写入不反复失效基础输入缓存。

```text
cache_key=(dataset_id,schema_id); if !covers_required_window: fetch_primary(); fill_complete_base_rows_only()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 13：缓存容量维护

**依赖：** 12。
**文件：** 修改 modules/factor/internal/inputcache/config.go、runtime.go、rebuild.go、manager.go、modules/factor/internal/bootstrap/cache.go；新增 modules/factor/internal/inputcache/capacity_policy_test.go。

- [ ] **先写失败用例：** 模拟时间验证首次不立即清理；多个 Dataset 共用总预算；N 行仍超字节限制退出并降级；旧读者不报文件关闭；构建途中退出后恢复；读取不更新淘汰时间。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestCacheCapacityPolicy' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 默认 timer 2233s，首次延后、不重入；N 和目录总字节上限可配置。按本地写入时间和业务键保留 N 行，在新文件重建；代际切换后等旧读者释放才删文件。计算 WAL/临时/退役目录，空间不足停填充回源，不无限重建。

```text
if bytes > max_bytes: rebuild_new_file(newest_n); swap(); drain_old_readers(); delete_old(); recheck_or_disable_fill()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 14：截面计算与通用可读事件

**依赖：** 04、08、09、11。
**文件：** 修改 modules/factor/internal/trigger/view_ready_runner.go、modules/factor/internal/engine/；新增 modules/factor/internal/trigger/cross_section_test.go。

- [ ] **先写失败用例：** FactorPeriodComputed 对应 Ready 不触发；其他 View/周期不触发；缺少依赖字段拒绝绑定；残缺面板默认跳过；允许降级时失败集合传入上下文且结果带状态。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestCrossSectionReady' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 仅关联 MergePeriodCompleted 的 ViewDataReady 触发截面；锁定 View、配置批次和对象范围。默认拒绝 degraded，只有显式策略才允许缺失对象处理。读取基础字段面板，输出 patch 写回同一 mdataset。

```text
if completion.kind != MergePeriodCompleted: ignore(); panel=read_fixed_view_scope(); validate_universe(); compute(panel,params,context)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 15：因子周期汇总与策略订阅

**依赖：** 08、10、14。
**文件：** 新增 modules/factor/internal/trigger/period_barrier.go、period_barrier_test.go；修改 modules/factor/internal/store/、modules/strategy/ 中实际事件消费入口。

- [ ] **先写失败用例：** 先任务后名单仍正确汇总；绑定启停不改变已有周期；缺源不会永远等任务；View 未应用结果时策略不运行；多 View 独立可读；零绑定明确终态；输出提交失败不报 complete。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestFactorPeriodBarrier' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 固定绑定和对象集合；容纳先到的时序终态、后到的 Merge 终态；汇总成功/失败/缺输入/跳过。输出收据全部确认后发布 FactorPeriodComputed；策略读 View 等匹配的 ViewDataReady，读 Primary 才可直接用计算完成。按保留期清理终态账本但不得早于幂等重放窗口。

```text
terminal = expected_binding_subject_pairs - accounted_pairs == empty; publish_when(terminal && all_output_receipts_confirmed)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 16：补算、启停与状态接口

**依赖：** 09、11、15。
**文件：** 修改 modules/factor/proto/、modules/factor/internal/bootstrap/、modules/factor/cmd/cli/；新增 modules/factor/internal/trigger/recalc_test.go。

- [ ] **先写失败用例：** 重复补算请求幂等；取消不产生虚假成功；绑定停用后旧任务不能污染新输出；引擎离线返回受理状态而非本地计算；状态接口区分输入缺失、算法失败和 View 等待。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestRecalcTask' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 控制面仅受理异步任务；明确 Dataset、对象、周期范围、绑定快照和任务 ID。实时终态不被补算静默覆盖，补算结果有独立批次与完成通知。记录 desired/applied 配置与引擎心跳；停止新准入不强行删除正在读取的资源。

```text
accept_job(id, frozen_scope); engine_execute(job); publish_separate_completion(job); never_execute_python_in_control()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 17：前端导航和基础资产归并

**依赖：** 02、06、16。
**文件：** 修改 web/src/api/modules/system/static-menu.ts、web/src/router/、web/src/api/storage/、web/src/api/factor/；新增 web/tests/data-collection-navigation.spec.ts、factor-dataset-workflow.spec.ts；页面从现有路由映射定位后迁移。

- [ ] **先写失败用例：** 刷新/直达路由可用；基础字段列表不混入因子字段；两个 View 可独立创建删除；system/custom 构造状态清楚；错误/空/加载态完整；桌面与移动无重叠。
- [ ] **运行红灯测试：** 在 `web` 执行 `pnpm exec playwright test tests/data-collection-navigation.spec.ts`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 删除数据资产顶层菜单；基础资源归数据采集，派生资源归因子/策略。数据集详情索引页支持一 Dataset 多 View，去掉多来源选择器；因子页面展示构造配置、基础/输出字段归属、任务和补算。自动创建默认 View，跨接口部分失败可重试恢复。

```text
open_collection(); assert(no_top_level_assets); open_dataset(); create_two_views(); assert(each_view_has_one_dataset)
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

### 任务 18：清理、端到端与交付

**依赖：** 01—17。
**文件：** 修改相关模块 README、部署模板、modules/factor/Makefile、web-host/Makefile；新增 modules/factor/internal/integration/dataset_pipeline_test.go、docs/superpowers/plans/2026-09-13-factor-dataset-view-refactor-progress.md。

- [ ] **先写失败用例：** 两来源两对象全链路成功；一个源超时 degraded；杀进程恢复；缓存满退化回源；View 重建期间时序继续；策略不会因结果 Ready 回环触发；所有新程序可独立启动和关闭。
- [ ] **运行红灯测试：** 在 `modules/factor` 执行 `env CGO_ENABLED=1 go test ./internal/... -run 'TestDatasetPipeline' -count=1`；预期上述目标行为至少一项失败，并记录失败断言。新增测试名称以该筛选前缀开头，不能接受 “no tests to run”。
- [ ] **实现目标行为：** 移除旧事件、旧 View 多源 UI、废弃配置和协议适配；独立构建 control/engine/merge。执行模拟 Storage+NATS+Python 完整链路及故障注入；最后启动新的 codeCR 审查，处理发现后重跑。部署按用户授权单独执行并记录真实新周期证据。

```text
collect(); merge(); timeseries(); view_input_ready(); cross_section(); factor_complete(); view_results_ready(); strategy_once()
```

- [ ] **验证与回归：** 重跑同一命令，预期 PASS；Go 并发/存储任务再使用 `-race -count=3`；前端使用本节末尾的完整校验命令。扩大到所有触及包，记录基线失败与新失败的区别。
- [ ] **提交与证据：** 只暂存本任务及必要协议联动文件，执行 `git diff --cached --check`，使用 `feat/refactor/test(<责任模块>): <本任务行为>` 提交；将提交号、断言与结果写入进度文档。

## 6. 协议生成与模块验证命令

所有命令均在对应目录执行，不用根目录 go test ./... 代替各 Go 模块验证。

```bash
# 仓库根目录：事件消息和 Storage RPC 生成
make -C packages/storagepb generate
make -C modules/storage/proto all
git diff --check
```

变更 Factor RPC 时先读取 modules/factor/proto/Makefile 并使用其既有生成目标；不手工改生成代码，不借生成过程删除手写 storagegen 辅助文件。

```bash
# packages/events 和 packages/storagepb 分别执行
go test ./...

# modules/storage、modules/collector、modules/factor 分别执行
env CGO_ENABLED=1 go test ./...
env CGO_ENABLED=1 go test -race ./internal/... -count=1

# modules/strategy 中执行，验证事件消费更新
go test ./...

# web 中执行
pnpm test
pnpm run check:menu
pnpm run check:data-browse
pnpm exec playwright test tests/data-collection-navigation.spec.ts tests/factor-dataset-workflow.spec.ts
pnpm run build:prod
```

预期结果：生成后消费者全部可编译，目标新增测试实际运行并通过，前端构建成功。缺 protoc/trpc-open/Python/浏览器运行库属于环境阻塞，应修复环境后重跑，不能标记通过。Go 根模块、独立 proto 模块和嵌套模块分别验证；代码生成导致的依赖变动必须列入结果。

新增 schema 按仓库规范加载到空 SQLite 校验；没有历史迁移测试要求，但仍须验证唯一键、外键和字段写入约束。DuckDB 测试保持 CGO 启用，数字精度、SQL NULL/JSON null 与空字节值必须覆盖。

## 7. 故障注入验收矩阵

| 注入点 | 必须观察到的结果 |
|---|---|
| 源 A 到达后杀 Merge | 重启保留 A；B 到达后完整输入只生效一次 |
| Storage 写成功但响应丢失 | 重试获得原收据，不产生不同的输入身份 |
| 因子字段更新再次进入 NATS | 引擎忽略，不触发循环 |
| 结果提交后任务账本尚未更新即退出 | 重启通过幂等提交/收据恢复，不覆盖新版本 |
| 时序完成早于 Merge 周期完成 | 早到结果被保留，拿到冻结名单后正确闭合 |
| View 一个分区落后 | 不发对应 ViewDataReady；追平后可重试发布 |
| View 重建时因子继续写 | 时序不受源 View 依赖影响；结果 Ready 等新索引真实应用 |
| 截面收到结果 Ready | 不触发新截面任务 |
| 缓存重建期间读请求未结束 | 老文件等读者退出后删除；新请求走新代际 |
| 缓存文件写失败或无足够重建空间 | 停止填充并回源，计算正确性不依赖缓存 |
| 源缺失直到截止 | 不产生半成品行；degraded 含完整失败对象集合 |
| 同 schema 更换输入来源 | 必须创建新 mdataset，不能复用旧缓存和任务身份 |

## 8. 发布与真实验收门槛

- [ ] 完成所有实现后启动一次新的 codeCR 审查，重点检查原子提交、授权列归属、事件关联、终态冻结、缓存文件生命周期和多分区可读屏障。
- [ ] 主 Agent 独立复现审查发现，修复后补测试；审查无问题也记录剩余测试覆盖边界。
- [ ] 分别构建控制面、Merge、engine，验证 Linux CGO/Python 运行依赖。部署脚本不能默认连带启动旧单体消费者。
- [ ] 部署配置继续引用既有主机/凭据管理，不复制生产秘密到文档、测试或新模板。
- [ ] 根据届时授权执行外网控制面和内网运行进程部署；未获部署范围确认时只交付构建产物与操作清单。
- [ ] 用真实新周期记录：基础写入 → Merge 完整输入 → 时序结果 → 截面输入可读 → 截面结果 → FactorPeriodComputed → ViewDataReady → 策略读取。
- [ ] 用两对象两来源构造可核算样本，将输出与直接 Python 结果比较，记录周期、对象、绑定快照、事件 ID 和结果字段，隐藏凭据。
- [ ] 区分本地单测、模拟 E2E、实际部署和真实数据验收，不将任何一级替代另一级。
- [ ] 更新进度文档，提交本次范围变更并按仓库要求推送；列明未完成项，不用“已完成”掩盖缺失验收。

## 9. 设计覆盖对照

| 设计章节 | 执行任务 |
|---|---|
| 目标、旧方案替代、总体架构 | 01、02、09、18 |
| 数据模型与不可变输入定义 | 02、03、06 |
| 默认/自定义 Merge、周期对象集合 | 07、08 |
| 时序、回写防循环、Python、截面 | 10、11、14 |
| 完成事件、写入位置、策略可读 | 01、04、05、08、15 |
| 本地缓存与容量限制 | 12、13 |
| 控制面、任务、部署 | 09、16、18 |
| 前端归并、默认 View | 06、17 |
| 故障语义、观测与验收 | 各任务定向测试、18、故障矩阵 |

每个状态转换记录 dataset_id、batch_id、config_snapshot_id、对象或范围、关联事件和耗时。任务 07—16 同步增加相应指标：待聚合、缺源、计算成功/失败/跳过、回源量、缓存字节、View 滞后；日志不得包含凭据和完整敏感数据。

## 10. 计划使用说明

本文是可分阶段执行的重构计划，不是整套实现代码。任务内代码块描述必须满足的操作顺序和断言；具体函数签名沿用现有包接口，在对应任务先定义测试接口后实现，不能把伪代码名称误认为已存在符号。

实施不需要旧系统兼容，但仍需可靠交付和失败恢复。完成单个子系统后可以提交验证，不应提前打开依赖尚未实现的正式消费链路。
