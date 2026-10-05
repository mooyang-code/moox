# 因子前端重设计：以因子集为中心的因子工作台

日期：2026-10-04

状态：设计草案，待确认后实施。后端基线见
[因子计算模块重构设计](./2026-10-04-factor-dataset-period-pipeline-design.md)。

> **修订说明（2026-10-04）：** 本文的信息架构（以因子集为中心的单一工作台、因子属于因子集）已被
> [多页面前端设计](./2026-10-04-factor-multi-page-frontend-design.md) 与
> [因子定义与因子集成员关系设计](./2026-10-04-factor-definition-set-membership-design.md) 取代：
> 因子定义无状态、不带 `set_id`，启停属于因子集成员，页面拆为 3 个菜单页。下文 §5.4、§7、§8
> 中凡涉及因子自带状态、`SetFactorStatus` 或按因子集过滤因子的描述，以新设计为准，仅作历史参考。

## 1. 背景与目标

后端已重构为“数据集驱动的周期流水线”：一个因子集（FactorSet）绑定一个源数据集和一个频率，
对应一个结果数据集和一个自动创建的默认 View；因子（FactorDef）属于因子集，启用即加列并自动补算，
停用保留列并停止写入；每个周期有完成标记与逐因子状态。

现有前端已拆成“因子集 / 因子定义 / 计算任务 / 因子结果”四个独立页面，类型与 `factor.proto`
基本一致，但页面组织和交互没有体现这些后端语义。本设计目标：

1. 前端信息架构与后端对象模型一致：一切围绕因子集组织。
2. 把后端已有的状态（周期健康、逐因子状态、lane、回填任务）如实展示，不再用内存假状态。
3. 让用户不会违反后端的约束（输入列必须是源数据集列、输出列不得冲突、先停用才能改）。
4. 去掉四份重复的加载与防抖代码，沉淀共享层。

## 2. 现状评估

| 编号 | 现状 | 与后端语义的差距 |
|---|---|---|
| F1 | 四个页面各自拉全部因子集、各自选因子集 | 后端所有操作以因子集为单位；跨页不保留当前因子集，加载逻辑重复四份 |
| F2 | 因子集状态类型只有 `pending/enabled/disabled` | 后端还有 `deleting`；`pending` 是可重试续跑的中间态，UI 无说明 |
| F3 | 因子集页只有新建、启停 | 后端有 `UpdateFactorSet`（改对象范围）、`DeleteFactorSet(purge)`，前端无入口 |
| F4 | 因子集列表只显示最近周期和延迟 | 看不出 `degraded`、因子数、lane 排队；`GetStatus` 的 `lanes` 未被使用 |
| F5 | 因子“启用”只是改状态按钮 | 后端启用 = 加列 + 自动补算；UI 不提示回填，也看不到对应任务 |
| F6 | “编辑”仅在停用时可点，无引导 | 后端流程是“停用 → 修改 → 启用”，用户不知道为何不能编辑 |
| F7 | 输入列是自由输入标签 | 后端要求输入列 ⊆ 源数据集列；输出列不得与源列、同因子集其他因子输出冲突，均要等提交后才报错 |
| F8 | `allow_partial_universe` 对所有类型显示；表单有永久禁用的“状态”项 | 仅截面因子有意义；状态由启停操作决定，不应出现在表单 |
| F9 | 补算任务列表只存在内存 | 后端无 `ListRecalcJobs`；刷新即丢；启用触发的回填任务根本不可见 |
| F10 | 补算成功任务的 `error` 显示在“错误”列 | 后端把分块降级写成 `degraded:` 前缀说明，成功任务被误读为失败 |
| F11 | 结果页内嵌 View 浏览 + 几个状态标签 | 周期完成标记里有逐因子 `complete/degraded/skipped` 与失败对象，页面未展示，用户看不出某因子本周期是否整列为 NULL |
| F12 | 菜单“计算任务”，页面标题“因子补算” | 命名不一致 |

内置 XBX 因子的批量导入目前只能用 `moox-factor-cli import-catalog`（读取服务器文件系统），不属于
本次前端范围，空态给出命令提示即可。

## 3. 设计原则

1. **因子集是唯一的上下文**：URL 即状态，切换页签不丢当前因子集。
2. **展示后端事实**：状态来自 RPC，不在前端推断或缓存假数据；刷新页面状态不丢。
3. **约束前置**：能在前端判断的后端校验（列合法性、冲突、先停用）在表单里提前提示。
4. **危险操作显式**：删除（含 purge）、停用因子集说明影响范围。
5. 沿用现有技术栈：Vue 3、Arco Design Vue、Pinia、`callControl("factormgr", …)`；复用
   `ViewBrowse`、`CodeBlock` 组件。

## 4. 信息架构

菜单“因子计算”下只保留一个入口“因子工作台”，路由：

```text
/factor/workbench?set=<set_id>&tab=<tab>        tab ∈ overview | factors | recalc | results
```

- 选中的因子集与页签放在 query（`set`、`tab`）而不是路径参数：与现有 `/collector/tasks?tab=` 约定一致，且菜单、标签页缓存都按路由 name 工作，无需特殊处理动态路由。
- 无 `set` 时自动选中当前空间的第一个因子集；无因子集时显示空态引导“新建因子集”。
- 旧路由 `/factor/sets`、`/factor/definitions`、`/factor/tasks`、`/factor/results` 删除（新项目无需兼容），
  旧的 `?set_id=` 查询参数一并删除。补算页签额外支持 `job=<job_id>` 高亮启用回填产生的任务。
- 切换空间时清空当前因子集并重新选择。

页面布局（桌面端左右两栏，窄屏左栏折叠为顶部下拉）：

```text
┌──────────────┬──────────────────────────────────────────────┐
│ 因子集列表    │ 头部：source_dataset · freq   [状态] [操作 ▾] │
│ [+ 新建]      ├──────────────────────────────────────────────┤
│ ● set A  OK  │ 概览 │ 因子 │ 补算 │ 结果                    │
│ ● set B  降级│                                              │
│ ○ set C  停用│            当前页签内容                         │
└──────────────┴──────────────────────────────────────────────┘
```

## 5. 各区域设计

### 5.1 左栏：因子集列表

- 每项显示：源数据集名称与频率、状态点、最近周期时间、健康标签。
- 健康标签由 `last_run` 推导，规则集中在 `health.ts`：
  - 无 `last_run`：`暂无周期`（灰）；
  - `last_status = degraded`：`降级`（橙）；
  - `lag_seconds > 2 × 频率时长`：`滞后`（红）；
  - 其余：`正常`（绿）。
- 状态：`pending` 显示“创建中，可重试”提示；`deleting` 显示“清理中”并禁用其余操作。
- “新建因子集”弹窗：源数据集（仅 active 的时序数据集，且排除 `factor_result` 角色）、频率（取数据集
  支持的频率）、对象范围（全部 / 指定）。创建失败后列表里会出现 `pending` 项，头部提供“重试激活”
  （再次调用 `CreateFactorSet`，后端按 set_id 续跑）。

### 5.2 头部操作

- 启用 / 停用因子集（`SetFactorSetStatus`），停用时说明“不再接收周期事件，结果数据集和 View 保留”。
- 修改对象范围（`UpdateFactorSet`）：弹窗编辑 `subject_mode / subjects`，说明“从下一个周期生效，
  历史需手动补算”。
- 删除（`DeleteFactorSet`）：确认框二选一——“仅删除因子集（保留结果数据集）”或“同时清理结果数据集
  （purge）”，purge 需要输入 set_id 二次确认。

### 5.3 概览页签

- 基本信息：空间、源数据集、频率、结果数据集、默认 View（来自 `listViews`）、对象范围、创建/更新时间。
- 运行状态：`consumer_running`、Python worker 总数/忙碌数、本因子集 lane 的 `queued/active`
  （来自 `GetStatus`，5 秒轮询，离开页面停止）。
- **最近周期**：周期时间、整体状态、延迟，下方为逐因子状态表（见 §7 的 `last_run.factors`）：
  因子 ID、状态标签（`complete/degraded/skipped`）、失败对象数（可展开列表）、源码 hash 前 8 位。
  解释文案：`skipped` = 截面因子在上游缺失对象且不允许部分参与时跳过；`degraded` = 部分对象计算失败，
  对应列在这些对象上为 NULL。

### 5.4 因子页签

> 已被 2026-10-04 因子定义解耦设计修订：因子状态属于因子集成员，`SetFactorStatus` 已由 `SetFactorMemberStatus` 取代（返回 `member` 与 `backfill_job`）；本节仅作历史参考。

列表列：因子 ID、模块名、类型、输入列、输出列、回看周期、状态、最近周期状态（来自 `last_run.factors`）、操作。

操作规则（与后端一致，按钮禁用时用 tooltip 说明原因）：

| 操作 | 条件 | 说明 |
|---|---|---|
| 详情 | 始终 | 抽屉：定义、参数、源码（`CodeBlock`）、hash |
| 启用 | `disabled` | 确认框说明：将加列并回填保留期内历史；成功后提示并跳到“补算”页签定位回填任务 |
| 停用 | `enabled` | 确认框说明：保留列和历史值，停止写入新周期，View 不重建 |
| 编辑 | `disabled` | `enabled` 时按钮可点，点击弹出引导“需先停用，停用后可修改并重新启用”，不再是单纯置灰 |
| 删除 | `disabled` | 说明“已写入的结果列保留” |

创建/编辑表单：

- 因子 ID（`[A-Za-z_][A-Za-z0-9_]*`）、模块名、类型。
- **输入列**：下拉多选，选项来自源数据集的 active 列（`listDatasetColumns`），剔除保留列
  `subject_id/freq/data_time/series_tag` 与本因子集已有因子输出列。
- **输出列**：标签输入，前端即时校验：合法列名、非保留列、不与源数据集列重名、不与同因子集其他因子
  输出重名（编辑时排除自身）。
- 回看周期数（≥1）。
- `allow_partial_universe` 仅在类型为截面因子时显示，并附说明。
- 参数 JSON：必须为 JSON object（沿用 `definitions/factor-form.ts` 中已有的 `validateFactorParamsJSON`，迁入新的 `factor-form.ts`）。
- 源码：等宽文本框 + 模板；提交前提示“保存后源码按 hash 不可变，修改会产生新版本”。
- 删除表单里的“状态”项；新建因子固定为 `disabled`，提交成功后提示“已创建为停用状态，启用后开始计算并回填”。

### 5.5 补算页签

- 提交表单：因子（多选，留空 = 全部启用因子）、对象（留空 = 因子集范围）、时间范围（默认最近 100 个
  周期，对齐频率）、请求 ID（留空自动生成，重复提交同一 request_id 幂等）。
- **任务列表来自后端 `ListRecalcJobs(set_id)`**，进入页签即加载，活动任务 3 秒轮询 `GetRecalcJob`，
  无活动任务时停止轮询。
- 区分任务来源：`request_id` 前缀 `factor-enable-`（启用因子时后端自动生成）的显示“启用回填”，其余显示
  “手动补算”。
- 状态展示：`accepted/running/succeeded/failed/cancelled`。`error` 以 `degraded:` 开头且任务 `succeeded`
  时，显示橙色“部分降级”标签，tooltip 展示说明，不放进“错误”列；`failed` 才展示为错误。
- 取消仅对 `accepted/running` 开放。

### 5.6 结果页签

- 顶部状态条：结果数据集、结果 View、最近周期与延迟、各因子最近周期状态汇总（`3 个正常 / 1 个降级`）。
- 主体保留 `ViewBrowse`（`view-roles=['factor_result']`），固定该因子集的默认 View。
- View 尚未创建时显示明确空态：“Storage 在结果数据集激活后自动创建默认 View，因子集处于 pending
  或刚创建时稍候刷新”，并提供刷新按钮。

## 6. 前端共享层

```text
web/src/api/factor/            索引、类型（扩展 §7 的新增字段与方法）
web/src/store/modules/factor.ts   Pinia：当前空间的因子集列表、当前 setId、加载与失效
web/src/views/factor/workbench/
  index.vue                    布局 + 路由同步
  set-list.vue                 左栏
  set-header.vue               头部与操作、对话框
  tabs/overview.vue
  tabs/factors.vue
  tabs/recalc.vue
  tabs/results.vue
  health.ts                    因子集健康推导（纯函数，带单测）
  factor-form.ts               表单校验（输出列冲突等，纯函数，带单测）
  status.ts                    状态/任务文案与颜色映射
```

- `factor` store 负责分页拉全量因子集（一处实现）、按空间过滤、`setId` 失效保护和请求序号防抖；
  各页签只读取 store，不再各自拉取。
- 轮询统一封装为 `usePolling(fn, intervalMs, active)`：页面隐藏或离开时停止。
- 类型：`FactorSetStatus` 增加 `deleting`；新增 `FactorPeriodState`、`ListRecalcJobs*` 等。

## 7. 后端配套变更（FactorMgr）

> 已被 2026-10-04 因子定义解耦设计修订：因子状态属于因子集成员，`SetFactorStatus` 已由 `SetFactorMemberStatus` 取代（返回 `member` 与 `backfill_job`）；本节仅作历史参考。

前端要如实展示状态，需要后端补三处，均在 `factor.proto`：

1. **`ListRecalcJobs`**
   ```protobuf
   message ListRecalcJobsReq { string set_id = 1; repeated string statuses = 2; common.Page page = 3; }
   message ListRecalcJobsRsp { common.RetInfo ret_info = 1; repeated RecalcJob jobs = 2; common.PageResult page_result = 3; }
   ```
   `store.ListRecalcJobs` 已存在，补按 `set_id` 过滤；按创建时间倒序，默认返回最近 50 条。
2. **`SetFactorStatusRsp` 增加 `RecalcJob backfill_job`**：启用因子并成功提交回填时返回该任务，前端据此
   定位任务；停用或幂等重复启用时为空。
3. **`SetRunSummary` 增加逐因子状态**
   ```protobuf
   message FactorPeriodState {
     string factor_id = 1;
     string status = 2;                    // complete | degraded | skipped
     repeated string failed_subjects = 3;
     string source_hash = 4;
   }
   // SetRunSummary 新增
   repeated FactorPeriodState factors = 5;
   repeated string failed_subjects = 6;
   ```
   数据来源是 `bootstrap.runTracker`：记录最近周期时一并保存 `outcome.Factors` 与整体失败对象，
   `LatestRun` / `GetStatus.recent_runs` 返回。该状态是进程内“最近一次”，服务重启后在下一个周期完成前
   为空，前端按“暂无周期”展示；持久的完成标记仍在 Storage 中，不为展示重复落库。

网关放行：`ListRecalcJobs` 是读操作，加入 `config/setup/service-deployments.yaml` 与
`sysdeploy/defaults.go` 中 FactorMgr **读路由**的 `gateway_methods`（与 `GetFactorSet`、`ListFactorSets`
同组，调用方 `admin-gateway`、`moox-cli`、`strategy`）；`sysdeploy` 的 `defaults_test.go` / `routes_test.go` 补对应断言，
防止默认值与 YAML 漂移。

## 8. 状态与文案映射

> 已被 2026-10-04 因子定义解耦设计修订：因子状态属于因子集成员，`SetFactorStatus` 已由 `SetFactorMemberStatus` 取代（返回 `member` 与 `backfill_job`）；本节仅作历史参考。

| 对象 | 值 | 文案 | 颜色 |
|---|---|---|---|
| 因子集 | pending | 创建中 | 蓝 |
| | enabled | 运行中 | 绿 |
| | disabled | 已停用 | 橙 |
| | deleting | 清理中 | 灰 |
| 因子 | enabled / disabled | 已启用 / 已停用 | 绿 / 橙 |
| 周期因子状态 | complete / degraded / skipped | 正常 / 降级 / 已跳过 | 绿 / 橙 / 灰 |
| 补算任务 | accepted / running / succeeded / failed / cancelled | 已受理 / 执行中 / 已完成 / 失败 / 已取消 | 橙 / 蓝 / 绿 / 红 / 灰 |

## 9. 错误、空态与加载

- 所有 RPC 失败统一 `Message.error(后端 ret_info.msg)`；列表加载失败在区域内显示可重试的错误态，而不是
  清空数据。
- 并发保护：每类请求带序号，空间或因子集切换后丢弃过期响应（集中在 store 与 `usePolling`）。
- 空态文案：无空间 → “请先在顶部选择空间”；无因子集 → “新建因子集”；无因子 → 提示可新建，或使用
  CLI `moox-factor-cli import-catalog` 导入内置因子。

## 10. 测试策略

- 纯函数单测：`health.ts`（正常/降级/滞后/无周期）、`factor-form.ts`（保留列、源列冲突、同集输出冲突、
  JSON object）、`status.ts`。
- store 单测：空间切换、过期响应丢弃、`setId` 失效回落。
- 组件契约测试（vitest）：因子操作按钮的启停条件；补算页对 `degraded:` 前缀的展示；`deleting` 禁用操作。
- 后端：`ListRecalcJobs` 按 set 过滤与分页；`SetFactorStatus` 返回回填任务；`runTracker` 保存逐因子状态；
  网关契约测试。
- 现有 `factor-contract.spec.ts` 中读取旧页面源码的断言随页面删除一并重写。
- 验收：`cd web && npx vitest run`、`npx vue-tsc --noEmit`、`go test ./...`（factor、admin）。

## 11. 删除项

- 页面：`views/factor/{sets,definitions,tasks,results}`；路由 `factor-sets/definitions/tasks/results`；
  `lang` 中对应键，新增 `factor-workbench`。
- 首页（`views/home/home.vue`）中指向 `/factor/definitions`、`/factor/sets`、`/factor/results` 的跳转改为
  `/factor/workbench`。
- 不保留旧 URL 兼容。

## 12. 不做的事

- 内置因子 catalog 的 Web 导入（依赖服务器文件系统，保留 CLI）。
- 因子源码在线调试/回测、结果可视化图表（后续单独设计）。
- A 股空间（后端当前仅支持加密货币空间，见后端设计 §20）。
- 跨因子集的全局补算任务总览（任务始终归属某个因子集）。

## 13. 实施顺序

1. 后端：`ListRecalcJobs`、`SetFactorStatusRsp.backfill_job`、`SetRunSummary.factors`、网关放行与测试。
2. 前端共享层：类型、`api/factor`、`factor` store、`usePolling`、纯函数及单测。
3. 工作台骨架：路由、左栏、头部、概览页签。
4. 因子页签与表单，补算页签，结果页签。
5. 删除旧页面与路由，更新首页与语言包，重写契约测试，全量验证后提交。
