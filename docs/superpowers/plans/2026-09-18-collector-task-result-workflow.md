# Collector Task Result Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Collector 从“采集规则 + 数据集管理”重构为“采集任务 + 采集结果”，让用户以任务为唯一入口查看真实数据和 K 线，同时将 Dataset 保留为 Storage 内部技术对象。

**Architecture:** Collector 的主领域对象改名为 `CollectionTask`，每个任务恰好拥有一个结果承接对象和一个默认浏览 View。前端在“采集任务”模块下提供“采集任务、任务实例、执行器、采集结果”四个并列子 Tab；“采集结果”内部按任务展示少量动态子 Tab，并复用现有真实数据浏览和 K 线弹窗。由于项目未上线，不保留旧 Rule API、旧 Collector 数据集页面或历史数据兼容层；Storage 模块内部继续使用 `dataset_id`。

**Tech Stack:** Go、tRPC/Protobuf、GORM、SQLite、Vue 3、TypeScript、Arco Design、Vitest、Vite、现有 Storage Metadata/View RPC。

---

## 1. 已确认的产品决策

1. Collector 顶层导航为：

   ```text
   采集任务
   ├── 采集任务
   ├── 任务实例
   ├── 执行器
   └── 采集结果
   ```

2. “采集规则”统一改名为“采集任务”。
3. 新建任务必须填写“任务名称”；系统自动生成内部 `task_id`，普通用户不需要管理该 ID。
4. 一个采集任务对应一个采集结果；采集结果下按任务名称展示动态子 Tab。
5. 采集任务较少，因此直接使用横向动态子 Tab；不增加复杂的结果选择器。
6. 采集结果页面直接进入真实数据表格，并保留当前的筛选、排序、行详情和 K 线弹窗。
7. Dataset、DataNode、保留时长等内容移动到新建/编辑任务的“高级设置”；不提供复用已有结果的入口。
8. 所有任务结果都由任务自动独占创建。删除任务时必须弹窗让用户选择“保留结果数据”或“同时物理删除结果数据和数据视图”。
9. 不做旧接口别名、旧表迁移或历史数据包装。旧 Collector 数据和无主结果按清理计划删除。
10. Storage 的 `dataset_id`、`t_datasets`、Metadata RPC 和 Storage 内部模型不改名，只通过 Collector 对外接口隐藏。

## 2. 当前代码边界与基线

当前 Collector 的核心接口和表仍使用 Rule 语义：

- [modules/collector/proto/collector.proto](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/proto/collector.proto) 提供 `TaskRule`、`GetTaskRuleList`、`CreateTaskRule` 等 RPC。
- [modules/collector/schema/collector.sql](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/schema/collector.sql) 使用 `t_collector_task_rules.c_rule_id`。
- [modules/collector/internal/domain/task_rule.go](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/domain/task_rule.go) 和 [modules/collector/internal/store/task_rule.go](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/store/task_rule.go) 仍以 `TaskRule`/`RuleID` 为核心。
- [web/src/views/collector/task-management/index.vue](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/collector/task-management/index.vue) 当前的第一个 Tab 仍导入 `collector-rules.vue`。
- [web/src/views/collector/data-management/index.vue](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/collector/data-management/index.vue) 和 [web/src/views/collector/datasets/index.vue](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/collector/datasets/index.vue) 仍提供独立的数据集管理入口。
- [web/src/views/data/view-browse/index.vue](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web/src/views/data/view-browse/index.vue) 已具备真实数据、View 切换、筛选、排序、行详情和 K 线弹窗能力，应作为结果浏览的复用组件。

已知检查基线：

- `pnpm test` 当前通过；重构后必须保持全量通过。
- `web/scripts/check-menu-structure.mjs` 仍断言旧名称“基础数据集”，需要随导航重构更新。
- `web/scripts/check-detail-page-style.mjs` 当前引用不存在的旧路径 `web/src/views/ops/storage/routes.vue`，应在本次前端检查整理中修正，而不是把该失败留给发布阶段。

## 3. 文件变更地图

### 3.1 Collector 后端

修改或重命名以下文件：

- `modules/collector/proto/collector.proto`
- `modules/collector/proto/collectorgen/collector.pb.go`
- `modules/collector/proto/collectorgen/collector.trpc.go`
- `modules/collector/proto/collectorgen/validation.go`
- `modules/collector/schema/collector.sql`
- `modules/collector/internal/domain/task_rule.go` → `task.go`
- `modules/collector/internal/domain/task_rule_test.go` → `task_test.go`
- `modules/collector/internal/domain/task_instance.go`
- `modules/collector/internal/domain/fetch_batch.go`
- `modules/collector/internal/store/task_rule.go` → `task.go`
- `modules/collector/internal/store/task_rule_test.go` → `task_test.go`
- `modules/collector/internal/store/task_instance.go`
- `modules/collector/internal/store/task_instance_resample.go`
- `modules/collector/internal/store/database.go`
- `modules/collector/internal/store/database_test.go`
- `modules/collector/internal/rpc/convert.go`
- `modules/collector/internal/rpc/convert_test.go`
- `modules/collector/internal/rpc/service.go`
- `modules/collector/internal/rpc/service_validation_test.go`
- `modules/collector/internal/bootstrap/bootstrap.go`
- `modules/collector/internal/observability/realtime_inventory.go`
- `modules/collector/internal/marketfetch/*.go` 中使用父任务 `RuleID` 的文件
- `modules/collector/internal/resample/*.go` 中使用 `RuleID`/`resample_rule_id` 的文件
- `modules/collector/internal/ruleseed/seed.go` 及对应测试
- `modules/collector/cmd/cli/init_schema.go` 及对应测试
- `modules/cli/internal/adminclient/cloudnode.go`
- `modules/cli/internal/adminclient/cloudnode_test.go`
- `modules/cli/internal/command/collector.go`
- `modules/cli/internal/command/collector_test.go`
- `modules/collector/README.md`

### 3.2 前端

创建或修改：

- `web/src/views/collector/task-management/index.vue`
- `web/src/views/collector/collection-tasks/index.vue`
- `web/src/views/collector/collection-tasks/collection-task-params.ts`
- `web/src/views/collector/collection-tasks/collection-tasks.test.ts`
- `web/src/views/collector/task-results/index.vue`
- `web/src/views/collector/task-results/task-results-model.ts`
- `web/src/views/collector/task-results/task-results.test.ts`
- `web/src/views/collector/task-instances/task-instances.vue`
- `web/src/views/data/view-browse/index.vue`
- `web/src/views/data/view-browse/view-browse-utils.ts`
- `web/src/api/collector/index.ts`
- `web/src/api/modules/system/static-menu.ts`
- `web/src/router/route.ts`
- `web/src/lang/modules/zhCN.ts`
- `web/src/lang/modules/enUS.ts`
- `web/scripts/check-menu-structure.mjs`
- `web/scripts/check-detail-page-style.mjs`

删除或停止引用：

- `web/src/views/collector/collector-rules/collector-rules.vue`
- `web/src/views/collector/collector-rules/collector-rule-params.ts`
- `web/src/views/collector/collector-rules/collector-rules.test.ts`
- `web/src/views/collector/data-management/index.vue`
- `web/src/views/collector/data-management/data-management.test.ts`
- `web/src/views/collector/datasets/index.vue`

保留不动：

- `web/src/views/data/datasets/index.vue`，它仍供 Factor 数据集定义等内部模块使用。
- `web/src/views/factor/datasets/index.vue`、`web/src/views/factor/results/index.vue` 及 Storage 通用 Metadata API。

### 3.3 设计与运维文档

创建：

- `docs/architecture/collector-task-result.md`
- `docs/operations/collector-task-result-reset.md`

修改：

- `modules/collector/README.md`
- `docs/大仓架构.md` 中涉及 Collector protobuf 包或导航边界的说明。

## 4. 任务 1：建立新领域模型和失败测试

**Files:**

- Modify: `modules/collector/internal/domain/task_rule_test.go` → `modules/collector/internal/domain/task_test.go`
- Modify: `modules/collector/internal/rpc/service_validation_test.go`
- Create: `web/src/views/collector/collection-tasks/collection-tasks.test.ts`
- Create: `web/src/views/collector/task-results/task-results.test.ts`

- [ ] **Step 1: 为任务名称、任务 ID 和结果配置写失败测试。**

  后端测试必须覆盖：

  - 空 `task_name` 被拒绝。
  - 同一 `space_id` 下重复任务名称被拒绝。
  - `task_id` 缺失时由服务生成稳定的内部 ID。
  - 创建任务时自动产生结果引用。
  - 任务更新允许修改名称、描述、启用状态，不允许改变结果身份或数据类型。

  前端测试必须覆盖：

  - 新建任务表单显示“任务名称”且为必填。
  - 普通表单不显示 `Dataset`、`数据集 ID`、`DataNode` 字样。
  - 展开“高级设置”后才显示结果存储配置。
  - 结果页按任务数量创建动态子 Tab，而不是按 Dataset ID 创建。

- [ ] **Step 2: 运行针对性测试，确认测试先失败。**

  ```bash
  go test ./modules/collector/internal/domain ./modules/collector/internal/rpc
  cd web && pnpm vitest run src/views/collector/collection-tasks/collection-tasks.test.ts src/views/collector/task-results/task-results.test.ts
  ```

  Expected: 新测试因 `CollectionTask`、`task_name`、结果 Tab 组件尚未存在而失败。

- [ ] **Step 3: 先提交测试契约。**

  ```bash
  git add modules/collector/internal/domain/task_test.go modules/collector/internal/rpc/service_validation_test.go web/src/views/collector/collection-tasks/collection-tasks.test.ts web/src/views/collector/task-results/task-results.test.ts
  git commit -m "test: define collector task result contracts"
  ```

## 5. 任务 2：重构 Collector Schema、领域对象和 Repository

**Files:**

- Modify: `modules/collector/schema/collector.sql`
- Rename: `modules/collector/internal/domain/task_rule.go` → `modules/collector/internal/domain/task.go`
- Rename: `modules/collector/internal/store/task_rule.go` → `modules/collector/internal/store/task.go`
- Modify: `modules/collector/internal/domain/task_instance.go`
- Modify: `modules/collector/internal/domain/fetch_batch.go`
- Modify: `modules/collector/internal/store/task_instance.go`
- Modify: `modules/collector/internal/store/task_instance_resample.go`
- Modify: `modules/collector/internal/store/database.go`
- Modify: `modules/collector/internal/store/database_test.go`

- [ ] **Step 1: 定义新的 Collector 表结构。**

  将主表改为 `t_collector_tasks`，字段至少包括：

  ```sql
  c_task_id TEXT NOT NULL,
  c_task_name TEXT NOT NULL,
  c_description TEXT NOT NULL DEFAULT '',
  c_result_dataset_id TEXT NOT NULL DEFAULT '',
  c_result_view_id TEXT NOT NULL DEFAULT '',
  ```

  保留现有采集配置字段 `c_data_type`、`c_provider`、`c_market_type`、`c_collect_params`、`c_enabled`、准备状态和覆盖起始时间。新增唯一索引 `(c_space_id, c_task_id)` 和 `(c_space_id, c_task_name)`。

- [ ] **Step 2: 消除 TaskInstance 中两个不同“Task ID”的歧义。**

  当前 `TaskInstance.task_id` 是单个标的执行实例 ID，而 `RuleID` 是父规则 ID。重构为：

  - `instance_id`：单个标的执行实例的稳定 ID。
  - `task_id`：父 CollectionTask ID。
  - 数据库列 `c_task_id`：父任务 ID。
  - 数据库列 `c_instance_id`：原来的单实例 `c_task_id`。

  同步修改 fetch batch、retry item、period readiness、resample 规划和查询过滤字段，禁止在同一个结构中再次出现两个含义不同的 `task_id`。

- [ ] **Step 3: 将 Repository 改名并替换查询条件。**

  将 `TaskRuleRepository` 改为 `TaskRepository`，公开方法改为 `List`、`GetByTaskID`、`Create`、`UpdateByTaskID`、`SetEnabled`、`SetPrepareState`。所有 SQL 条件从 `c_rule_id` 改为 `c_task_id`，过滤器从 `RuleID` 改为 `TaskID`。

- [ ] **Step 4: 移除旧 Schema 的渐进式迁移代码。**

  由于不保留旧数据库，`database.go` 不再执行 `ALTER TABLE t_collector_task_rules`。启动时检测到旧表时返回明确错误：`collector schema reset required: legacy task rule table found`。新数据库只通过 `collector.sql` 建立新表。

- [ ] **Step 5: 运行 Store 测试和 Schema 测试。**

  ```bash
  go test ./modules/collector/internal/store ./modules/collector/internal/domain
  go test ./modules/collector/schema
  ```

  Expected: 新表、任务名称唯一性、任务/实例关联和准备状态测试全部通过。

- [ ] **Step 6: 提交 Repository 和 Schema 重构。**

  ```bash
  git add modules/collector/schema modules/collector/internal/domain modules/collector/internal/store
  git commit -m "refactor: model collector rules as tasks"
  ```

## 6. 任务 3：重写 Collector Protobuf/RPC 契约

**Files:**

- Modify: `modules/collector/proto/collector.proto`
- Regenerate: `modules/collector/proto/collectorgen/collector.pb.go`
- Regenerate: `modules/collector/proto/collectorgen/collector.trpc.go`
- Modify: `modules/collector/proto/collectorgen/validation.go`
- Modify: `modules/collector/internal/rpc/convert.go`
- Modify: `modules/collector/internal/rpc/convert_test.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/rpc/service_validation_test.go`

- [ ] **Step 1: 在 proto 中定义 CollectionTask 和 TaskResult。**

  `CollectionTask` 对外字段包括 `space_id`、`task_id`、`task_name`、`description`、采集配置、启用状态、创建修改时间、准备状态和 `TaskResult result`。`TaskResult` 包含 `result_name`、`view_id`、`status`、`last_data_time`、`data_kind`。

  `CreateTaskReq` 的高级结果配置只允许传入 DataNode、保留时长等存储参数；服务端自动生成独占结果。正常列表响应不返回底层 Dataset ID，只返回浏览所需的 `view_id` 和用户可读结果信息。

  RPC 改为：

  ```text
  GetTaskList
  GetTaskDetail
  CreateTask
  UpdateTask
  DisableTask
  DeleteTask
  ```

  `DeleteTaskReq` 必须包含 `space_id`、`task_id` 和 `delete_result_data`；服务端不根据默认值推断用户的删除选择。

  任务实例接口的父关联字段统一为 `task_id`，单个执行实例字段改为 `instance_id`。不保留 `GetTaskRuleList` 等旧别名。

- [ ] **Step 2: 重新生成 Go RPC 代码。**

  ```bash
  make -C modules/collector/proto all
  ```

  检查生成代码中不存在 `TaskRule`、`RuleId`、`CreateTaskRule`、`GetTaskRuleList` 等旧符号。

- [ ] **Step 3: 实现新的参数校验。**

  校验规则：

  - `space_id`、`task_name`、数据类型、Provider、市场类型必须存在。
  - 任务名称在同一空间内唯一，去除首尾空格后比较，长度限制为 1–80 个 Unicode 字符。
  - `task_id` 由服务生成时使用 `task_<uuid>`，不可由编辑请求修改。
  - 自动创建结果时必须能生成合法内部结果 ID。
  - 创建任务请求不得携带已有 Dataset ID。

  - [ ] **Step 4: 更新 RPC 转换和 Service 方法。**

  将 `toPBRule/fromPBRule` 改为 `toPBTask/fromPBTask`，将 `normalizeTaskRule`、`validateTaskRule`、`canonicalizeTaskRule` 等函数改为 Task 语义。错误信息统一使用“任务”，不再向前端返回“规则”。

- [ ] **Step 5: 运行 RPC 契约测试。**

  ```bash
  go test ./modules/collector/internal/rpc ./modules/collector/proto/collectorgen
  ```

- [ ] **Step 6: 提交 Protobuf 和 RPC 契约。**

  ```bash
  git add modules/collector/proto modules/collector/internal/rpc
  git commit -m "refactor: expose collector task RPCs"
  ```

## 7. 任务 4：实现“一任务一结果”的结果生命周期

**Files:**

- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/rpc/convert.go`
- Modify: `modules/collector/internal/planner/storagesource/source.go`
- Create: `modules/collector/internal/planner/taskresult/result.go`
- Create: `modules/collector/internal/planner/taskresult/result_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`

- [ ] **Step 1: 定义结果生命周期接口。**

  新增内部接口：

  ```go
  type TaskResultManager interface {
      Ensure(ctx context.Context, task domain.CollectionTask, config domain.ResultConfig) (domain.TaskResult, error)
      Inspect(ctx context.Context, spaceID, taskID string) (domain.TaskResult, error)
      Delete(ctx context.Context, result domain.TaskResult, deleteData bool) error
  }
  ```

  `Ensure` 必须在任务写入前完成结果创建；任何失败都不得留下无任务的结果。

- [ ] **Step 2: 实现自动结果命名和 Collector 标记。**

  自动创建结果时生成确定性内部 ID：

  ```text
  suffix = first 16 hex chars of SHA-256(space_id + "\0" + task_id)
  dataset_id = dataset_collector_<suffix>
  view_id = view_collector_<suffix>
  ```

  Dataset 写入 `owner_module=collector`、`dataset_role=raw_collection`、`collector_task_id=<task_id>`；View 写入 `owner_module=collector`、`view_role=collection_browse`、`collector_task_id=<task_id>`。用户界面只显示 `task_name`/`result_name`。

- [ ] **Step 3: 实现高级结果存储配置。**

  高级设置只允许配置结果的 DataNode、保留时长和结果描述，不允许选择已有结果。任务创建服务自动创建 Dataset 和默认 View，并把 `c_result_dataset_id`、`c_result_view_id` 写入任务记录。

- [ ] **Step 4: 实现自动结果删除和补偿。**

  删除任务请求增加 `delete_result_data` 布尔字段，前端确认弹窗必须显式传入该值。按以下顺序执行：停止任务调度 → 删除 Collector 实例/批次/重试记录 → 删除任务记录 → 当 `delete_result_data=true` 时删除当前任务的 View 和 Dataset。`false` 时保留结果数据但不再展示在采集结果列表中。任一步失败都返回明确错误；创建过程中失败时按已创建资源倒序补偿。

- [ ] **Step 5: 测试结果生命周期。**

  `result_test.go` 至少覆盖：自动创建、重复请求幂等、结果配置校验、选择物理删除时删除 View/Dataset、选择保留时不删除 View/Dataset、创建 View 失败时补偿删除 Dataset。

- [ ] **Step 6: 运行 Collector 结果相关测试。**

  ```bash
  go test ./modules/collector/internal/planner/taskresult ./modules/collector/internal/rpc ./modules/collector/internal/resample
  ```

- [ ] **Step 7: 提交结果生命周期。**

  ```bash
  git add modules/collector/internal/planner/taskresult modules/collector/internal/rpc modules/collector/internal/planner/storagesource modules/collector/internal/bootstrap
  git commit -m "feat: bind one collector task to one result"
  ```

## 8. 任务 5：替换 Collector 运行时的 Rule 关联

**Files:**

- Modify: `modules/collector/internal/marketfetch/*.go`
- Modify: `modules/collector/internal/resample/*.go`
- Modify: `modules/collector/internal/jobs/**/*.go`
- Modify: `modules/collector/internal/observability/realtime_inventory.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/domain/fetch_batch.go`
- Modify: `modules/collector/internal/store/task_instance*.go`
- Modify: all corresponding `*_test.go` files found by the verification search below

- [ ] **Step 1: 替换父任务符号。**

  将 Collector 内部所有父规则关联替换为 `CollectionTaskID` 或 `TaskID`；将执行实例的 `TaskID` 改为 `InstanceID`。日志从 `rule=%s` 改为 `task=%s`，指标标签从 `rule_id` 改为 `task_id`。

- [ ] **Step 2: 重命名 Resample 领域对象。**

  `RuleSpec` 改为 `TaskSpec`，`RuleID` 改为 `TaskID`，Storage attributes 中的 `resample_rule_id` 改为 `resample_task_id`。回填请求和状态查询统一使用 `task_id`。

- [ ] **Step 3: 重命名 Scheduler/Reconciler 的状态键。**

  稳定键由 `space_id + rule_id + frequency` 改为 `space_id + task_id + frequency`；SCF 请求、批次、retry item、period readiness 和实例撤销查询全部使用新的父任务 ID。

- [ ] **Step 4: 执行旧符号扫描。**

  ```bash
  rg -n --glob '!*.pb.go' --glob '!*.trpc.go' 'TaskRule|RuleID|rule_id|CreateTaskRule|GetTaskRuleList|UpdateTaskRule|DisableTaskRule' modules/collector modules/cli
  ```

  输出中允许保留与 Monitor 无关的 `monitor rule_id`；Collector 代码、Collector CLI、Collector 文档和 Collector 配置不得再出现旧 Rule 术语。

- [ ] **Step 5: 运行全部 Collector Go 测试。**

  ```bash
  go test ./modules/collector/...
  go test ./modules/cli/...
  ```

- [ ] **Step 6: 提交运行时关联重构。**

  ```bash
  git add modules/collector modules/cli
  git commit -m "refactor: use collection task identity at runtime"
  ```

## 9. 任务 6：更新种子配置、CLI 和开发文档

**Files:**

- Modify: `modules/collector/internal/ruleseed/seed.go`
- Modify: `modules/collector/internal/ruleseed/seed_test.go`
- Modify: `modules/collector/cmd/cli/init_schema.go`
- Modify: `modules/collector/cmd/cli/init_schema_test.go`
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/internal/adminclient/cloudnode_test.go`
- Modify: `modules/cli/internal/command/collector.go`
- Modify: `modules/collector/README.md`
- Modify: `docs/大仓架构.md`

- [ ] **Step 1: 更新种子文件格式。**

  内置 YAML 从 `rule_id` 改为 `task_id`，增加必填 `task_name`，将错误信息和 seed summary 从 rule 改为 task。初始化命令输出 `tasks_created`、`tasks_unchanged`。

- [ ] **Step 2: 更新 Admin CLI 客户端。**

  `CreateTaskRule`、`UpdateTaskRule`、`DisableTaskRule`、`ListTaskRules` 改成 Task API 和字段名；所有 HTTP 路径改为 `/api/admin/collectmgr/CreateTask` 等新路径。删除旧路径测试，不新增兼容转发。

- [ ] **Step 3: 更新 Collector CLI 帮助和输出。**

  命令帮助使用“采集任务”“任务实例”“采集结果”；JSON 输出使用 `task_id`、`task_name`、`result`。SCF 发布命令内部仍可读取结果的 Storage ID，但不得把它作为普通 CLI 输出主字段。

- [ ] **Step 4: 更新文档并做术语扫描。**

  `modules/collector/README.md` 明确说明一任务一结果、Storage Dataset 为内部对象和任务删除时的结果保留/物理删除规则。执行：

  ```bash
  rg -n '采集规则|TaskRule|rule_id|数据集管理|基础数据集' modules/collector modules/cli docs/大仓架构.md
  ```

  只允许出现 Monitor、Storage 或历史变更说明中的技术术语；Collector 用户流程文档不得出现“采集规则”。

- [ ] **Step 5: 提交 CLI、种子和文档更新。**

  ```bash
  git add modules/collector/internal/ruleseed modules/collector/cmd modules/cli/internal/adminclient modules/cli/internal/command modules/collector/README.md docs/大仓架构.md
  git commit -m "docs: describe collector tasks and results"
  ```

## 10. 任务 7：重构前端导航和任务表单

**Files:**

- Rename: `web/src/views/collector/collector-rules/collector-rules.vue` → `web/src/views/collector/collection-tasks/index.vue`
- Rename: `web/src/views/collector/collector-rules/collector-rule-params.ts` → `web/src/views/collector/collection-tasks/collection-task-params.ts`
- Rename: `web/src/views/collector/collector-rules/resample-backfill.vue` → `web/src/views/collector/collection-tasks/resample-backfill.vue`
- Rename corresponding `collector-rules.test.ts` to `collection-tasks.test.ts`
- Modify: `web/src/views/collector/task-management/index.vue`
- Modify: `web/src/api/collector/index.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: 把第一个主 Tab 改为“采集任务”。**

  表格列改为：任务名称、数据类型、数据源、市场、频率、结果状态、最近数据时间、启用状态、操作。`rule_id` 不再作为首列；任务 ID 只在技术详情抽屉中显示。

- [ ] **Step 2: 重写新建/编辑表单的普通区。**

  普通区顺序固定为：任务名称 → 描述 → 数据类型 → 数据源 → 市场类型 → 采集频率 → 标的来源 → 启用状态。按钮使用“新建采集任务”“保存任务”。

- [ ] **Step 3: 增加高级设置折叠区。**

  高级区只提供结果存储设置，包括 DataNode、保留时长和结果描述；结果始终由任务自动独占创建，字段文案不得出现 Dataset ID。

- [ ] **Step 4: 更新编辑限制和删除交互。**

  编辑时任务名称和描述可变；数据类型、市场、结果身份和结果形态变更必须提示“请创建新任务”。新增“删除任务”操作，确认框使用二选一：`删除任务，保留结果数据`、`删除任务并物理删除结果数据和数据视图`，默认选中保留结果。

- [ ] **Step 5: 更新路由和菜单。**

  将 `/collector/rules` 改为 `/collector/tasks`，菜单 key 改为 `collector-tasks`，删除 `/collector/data-management` 和 `/collector/datasets` 的 Collector 菜单条目。Factor 和 Storage 的数据集路由保持不变。

- [ ] **Step 6: 运行前端任务测试。**

  ```bash
  cd web
  pnpm vitest run src/views/collector/collection-tasks/collection-tasks.test.ts src/views/collector/task-management/task-management.test.ts
  ```

- [ ] **Step 7: 提交前端任务入口。**

  ```bash
  git add web/src/views/collector/collection-tasks web/src/views/collector/task-management web/src/api/collector web/src/api/modules/system/static-menu.ts web/src/router/route.ts web/src/lang/modules
  git commit -m "feat: expose collector tasks instead of rules"
  ```

## 11. 任务 8：实现“采集结果”动态子 Tab和真实数据浏览

**Files:**

- Create: `web/src/views/collector/task-results/index.vue`
- Create: `web/src/views/collector/task-results/task-results-model.ts`
- Create: `web/src/views/collector/task-results/task-results.test.ts`
- Modify: `web/src/views/collector/task-management/index.vue`
- Modify: `web/src/views/data/view-browse/index.vue`
- Modify: `web/src/views/data/view-browse/view-browse-utils.ts`
- Modify: `web/src/api/collector/index.ts`
- Modify: `web/src/views/collector/task-instances/task-instances.vue`

- [ ] **Step 1: 增加第四个主 Tab“采集结果”。**

  `task-management/index.vue` 的 Tab 顺序固定为：`tasks`、`instances`、`executors`、`results`。结果页使用独立 query key，例如 `?tab=results&resultTask=<task_id>`；刷新后保持选中的任务结果。

- [ ] **Step 2: 实现结果子 Tab模型。**

  结果页调用 `GetTaskList` 获取当前空间任务，过滤出具备 `result.view_id` 的任务，按任务创建时间排序。每个子 Tab 标题使用 `task_name`，不使用 Dataset ID。Tab 内容显示：任务状态、结果状态、最近数据时间、数据覆盖范围和真实数据浏览区。

- [ ] **Step 3: 扩展 ViewBrowse 的受控范围。**

  为 `ViewBrowse` 增加可选 props：

  ```ts
  viewIds?: string[];
  activeViewId?: string;
  hideTechnicalIdentity?: boolean;
  emptyDescription?: string;
  ```

  Collector 结果页传入当前任务的 View ID，并设置 `hideTechnicalIdentity=true`，因此页面不显示 `Dataset:` 和内部 View ID；Factor/Storage 页面继续使用原有默认行为。只有当前结果存在多个 View 时才显示内部 View 切换。

- [ ] **Step 4: 保留真实数据和 K 线交互。**

  复用 `ViewBrowse` 当前的 `queryTimeSeriesRows`、筛选、排序、行详情和 `openKlineModal` 流程。时序结果仍显示“K线”按钮；非时序结果显示记录查询。结果加载中、未建 View、暂无数据、查询失败分别显示明确状态，不回退到假数据或空白表格。

- [ ] **Step 5: 实现结果 Tab 的空态和失效态。**

  - 没有任务：显示“暂无采集任务”和“新建采集任务”。
  - 有任务但没有结果 View：显示“结果准备中”，提供跳转任务详情。
  - 有 View 但无数据：显示“任务已准备，尚未产生数据”。
  - 任务禁用：保留结果 Tab，显示“任务已停用”。

- [ ] **Step 6: 运行结果页测试。**

  ```bash
  cd web
  pnpm vitest run src/views/collector/task-results/task-results.test.ts tests/storage-view-browse.spec.ts src/views/data/datasets/default-view.test.ts
  pnpm run check:data-browse
  ```

- [ ] **Step 7: 提交结果浏览页面。**

  ```bash
  git add web/src/views/collector/task-results web/src/views/collector/task-management web/src/views/data/view-browse web/src/api/collector web/src/views/collector/task-instances
  git commit -m "feat: browse collector results by task"
  ```

## 12. 任务 9：移除 Collector 数据集管理并更新检查器

**Files:**

- Delete: `web/src/views/collector/data-management/index.vue`
- Delete: `web/src/views/collector/data-management/data-management.test.ts`
- Delete: `web/src/views/collector/datasets/index.vue`
- Delete: obsolete Collector rule files after Task 7 rename
- Modify: `web/scripts/check-menu-structure.mjs`
- Modify: `web/scripts/check-detail-page-style.mjs`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/router/route.ts`

- [ ] **Step 1: 删除 Collector 专属数据集路由和菜单断言。**

  检查器必须断言：

  - 存在 `/collector/tasks`。
  - 四个任务模块子 Tab 顺序正确。
  - 存在“采集结果”入口。
  - 不存在 `/collector/data-management`、`/collector/datasets`。
  - Collector 用户界面普通路径不出现“数据集管理”“集合定义”“基础数据集”。

- [ ] **Step 2: 修复详情页检查器的实际路径。**

  从 `check-detail-page-style.mjs` 删除不存在的 `web/src/views/ops/storage/routes.vue`，改为检查现存 Storage 页面，并增加对 `task-results/index.vue` 和 `view-browse/index.vue` 的结构断言。

- [ ] **Step 3: 运行前端静态检查。**

  ```bash
  cd web
  pnpm run check:menu
  pnpm run check:detail-pages
  pnpm run check:data-browse
  pnpm run lint:eslint:check
  pnpm run lint:prettier:check
  ```

- [ ] **Step 4: 提交 Collector 数据集入口移除。**

  ```bash
  git add web/src web/scripts
  git commit -m "refactor: hide collector dataset management"
  ```

## 13. 任务 10：清理旧 Collector 数据和部署前状态

**Files:**

- Create: `docs/operations/collector-task-result-reset.md`
- Modify: `modules/cli/internal/command/collector.go`
- Modify: `modules/cli/internal/command/collector_test.go`
- Modify: `modules/collector/cmd/cli/init_schema.go`
- Modify: `modules/collector/cmd/cli/init_schema_test.go`

- [ ] **Step 1: 增加清理命令的 dry-run。**

  增加 `moox-cli collector task purge`，默认只输出清单，不删除。清单必须包含：空间、任务表行数、实例/批次/retry 行数、Collector-owned View ID、Collector-owned Dataset ID，以及没有任务关联的保留结果数量。

  ```bash
  go run ./modules/cli/cmd/moox-cli collector task purge --file ./moox.toml --space-id crypto --dry-run
  ```

- [ ] **Step 2: 实现明确确认的 apply 模式。**

  `--apply --confirm` 才允许执行，顺序为：停止 Collector 写入 → 删除 Collector 任务运行记录 → 删除 Collector-owned View → 删除 Collector-owned Dataset → 删除旧 Collector 数据库并用新 Schema 初始化。Storage 中 Factor/Strategy/手工数据集不在清理范围内。

- [ ] **Step 3: 测试清理命令。**

  测试必须证明：默认 dry-run 不产生删除；没有 `--confirm` 时 apply 被拒绝；任务删除选择保留时不删除结果；选择物理删除时删除对应 View/Dataset；清理失败会停止并输出已完成阶段。

- [ ] **Step 4: 编写远端清理 Runbook。**

  `docs/operations/collector-task-result-reset.md` 必须包含：

  1. 发布前备份 Collector DB 和 Storage Metadata manifest。
  2. 运行 dry-run 并人工核对空间和数量。
  3. 停止 Collector、执行 `--apply --confirm`。
  4. 初始化新 Schema 和内置任务种子。
  5. 启动服务并验证任务列表、结果列表、真实数据和 K 线。
  6. 失败时恢复备份并回滚前端/Collector 二进制。

- [ ] **Step 5: 提交数据清理和 Runbook。**

  ```bash
  git add modules/cli/internal/command/collector.go modules/cli/internal/command/collector_test.go modules/collector/cmd/cli docs/operations/collector-task-result-reset.md
  git commit -m "ops: reset collector data for task result model"
  ```

## 14. 任务 11：全量验证和发布

**Files:**

- Verify all files from Tasks 2–10.
- Modify: `docs/architecture/collector-task-result.md`

- [ ] **Step 1: 运行 Go 格式化和构建。**

  ```bash
  gofmt -w modules/collector modules/cli/internal/adminclient modules/cli/internal/command
  go test ./modules/collector/...
  go test ./modules/cli/...
  TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh collector
  TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host
  ```

- [ ] **Step 2: 运行前端全量验证。**

  ```bash
  cd web
  CI=true pnpm install --frozen-lockfile --config.confirmModulesPurge=false
  pnpm test
  pnpm run check:data-browse
  pnpm run check:detail-pages
  pnpm run check:menu
  pnpm run lint:eslint:check
  pnpm run lint:prettier:check
  pnpm run build:prod
  cd ../web-host
  go run github.com/rakyll/statik@v0.1.7 -src=../web/dist -dest=./internal
  cd ..
  TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh web-host
  ```

- [ ] **Step 3: 执行术语和路由扫描。**

  ```bash
  rg -n --glob '!*.pb.go' --glob '!*.trpc.go' '采集规则|数据集管理|集合定义|基础数据集|TaskRule|CreateTaskRule|GetTaskRuleList|c_rule_id|rule_id' web/src modules/collector modules/cli docs
  ```

  允许的残留只包括 Storage/Factor/Monitor 的独立技术语义；Collector 对外页面、Collector RPC、Collector Schema 和 Collector 运维文档不得残留旧命名。

- [ ] **Step 4: 本地运行端到端验收。**

  验收顺序：

  1. 新建一个“Binance 现货 K 线 · 1 小时”任务。
  2. 确认任务表显示任务名称而不是规则 ID。
  3. 确认“采集结果”出现同名子 Tab。
  4. 进入结果后看到真实数据表格。
  5. 点击“K线”能打开 K 线弹窗。
  6. 刷新页面后仍保持当前结果 Tab。
  7. 禁用任务后结果仍可查看并显示停用状态。
  8. 删除任务并选择保留结果后确认 View/Dataset 仍存在但不再出现在采集结果列表。
  9. 删除任务并选择物理删除结果后确认对应 View/Dataset 和任务实例都消失。

- [ ] **Step 5: 发布前检查远端状态。**

  在 `106.53.107.122` 发布前记录 `/data/moox/prod` 下 Collector DB、当前 `moox-collector`、`moox-web-host` 的 SHA-256 和服务状态。先发布 Collector/Storage 控制面，再发布前端嵌入资源；不得在未完成 Schema reset 的情况下启动新 Collector。

- [ ] **Step 6: 远端发布和验收。**

  发布完成后检查：

  ```bash
  curl -kfsS 'https://106.53.107.122:9527/'
  curl -kfsS 'https://106.53.107.122:9527/collector/tasks'
  ssh ubuntu@106.53.107.122 '/data/moox/prod/status.sh'
  ```

  浏览器验收必须确认 Collector 页面不再出现“数据集管理”，并能从“采集结果”进入真实数据和 K 线弹窗。其他已运行的 SCF、CloudNode、Monitor 和 Storage 服务不因前端发布被不必要地重启。

- [ ] **Step 7: 更新架构文档并完成最终提交。**

  `docs/architecture/collector-task-result.md` 记录领域模型、RPC、数据库字段、结果创建、删除选择和前端导航。完成 `git diff --check`、全量测试和 `git status` 审核后，按提交序列合并到 `feature/mooyang`。

## 15. 回滚策略

1. 前端问题：恢复上一个 `moox-web-host` 二进制和前端静态资源，不回滚 Storage 数据。
2. Collector RPC/Schema 问题：停止新 Collector，恢复 Collector 二进制和数据库备份；由于新版本不读旧表，回滚必须同时恢复旧 Collector DB。
3. 结果创建补偿失败：停止任务调度，依据发布前 manifest 删除孤儿 Collector-owned View/Dataset，再重试初始化。
4. 远端清理命令只在 `--dry-run` 清单与人工确认一致后执行；清理后的 Collector 数据不可恢复，Storage Factor/Strategy 数据不在清理范围内。

## 16. 完成标准

- 用户只通过“采集任务”和“采集结果”完成采集配置与数据查看。
- 新建任务必须有任务名称，一个任务在结果页恰好对应一个结果子 Tab。
- 普通 UI 不出现 Dataset、数据集管理、集合定义或规则术语。
- Collector API、领域模型、数据库表和运行时关联统一使用 Task 语义。
- Storage 内部 `dataset_id` 保持不变，只作为隐藏技术对象。
- 真实数据表格、筛选、排序、详情和 K 线弹窗全部可用。
- Collector、CLI、前端全量测试和静态检查通过。
- 远端部署后新页面可访问，旧 Collector 数据集入口不可访问，其他模块功能不受影响。
