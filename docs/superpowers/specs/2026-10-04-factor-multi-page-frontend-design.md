# 因子计算前端多页面重设计

- 日期：2026-10-04
- 状态：设计已确认（v3.2；v3.1 经用户回复「ok」确认，v3.2 为对照代码勘察后的实现细节修订；代码尚未修改，执行计划见 [`../plans/2026-10-04-factor-definition-membership-and-multipage-frontend.md`](../plans/2026-10-04-factor-definition-membership-and-multipage-frontend.md)）
- 交互原型：<https://claude.ai/artifact/QZm1nnnEHcXAnVDNnMZxzZ>（7 个画板：总览 / 因子定义 / 新建因子 / 计算任务 Tab ×3 / 计算任务详情抽屉，示例数据）
- 关系：
  - **取代** [`2026-10-04-factor-workbench-frontend-design.md`](./2026-10-04-factor-workbench-frontend-design.md) 中的信息架构部分（单入口「因子工作台」、左侧因子集列表 + 四个内部 Tab）；该文档中的状态派生规则、补算语义**继续有效**，本文只在需要时引用，不重复。
  - **依赖** [`2026-10-04-factor-definition-set-membership-design.md`](./2026-10-04-factor-definition-set-membership-design.md)（因子定义与因子集解耦的后端规格）。本文的因子定义页、编辑器、计算任务「因子」成员区都按该规格的接口与规则设计，后端未落地前前端无法实施（见 §11）。
- 参照物：数据采集模块（`/data/*`、`/collector/tasks`）的页面设计与组织，尤其是「采集任务」菜单页：一个菜单页 + 顶部标题 Tab（采集任务 / 任务实例 / 执行器 / 采集结果）。

## 0. 修订记录与评审决议

| 版本 | 内容 |
| --- | --- |
| v1 | 5 个菜单页：总览 / 因子集 / 因子定义 / 补算任务 / 计算结果；新建因子需选择「因子集」。 |
| v2 | 收为 3 个菜单页；「因子集」更名「计算任务」；计算任务 / 计算结果 / 补算合并为标题 Tab；新建因子改选「源数据集 + 频率」，因子集在保存时隐含创建。 |
| v3 | **因子定义与计算任务彻底解耦**（定义不再绑定计算任务、不再有启停状态）；计算结果 Tab 对齐采集结果页；各 Tab 去掉说明行，说明收进 ⓘ 悬浮提示。 |
| v3.1 | 新建 / 编辑因子的源码输入由 `a-textarea` 改为**深色代码编辑器**（Python 语法高亮），并抽成可复用组件；需要新增前端依赖 CodeMirror 6（见 §6）。 |
| v3.2 | 对照现有代码修订实现细节：`ViewBrowse` 已有 `status-extra` 插槽无需新增；`meta.activeMenu` 要在 `Menu` 等三处直接读路由而不是 store；`.page-head` / `.result-toolbar` 是各页 scoped 样式；`use-factor-scope` 不得每次挂载都重置 store；补充 strategy 前端、首页等溢出点与全局契约测试（见 §6、§8、§9、§10）。 |

v3 评审决议：

| # | 评审意见 | 决议 |
| --- | --- | --- |
| 1 | 因子定义不应在定义时绑定「所属计算任务」，定义只存储因子本身 | **彻底解耦**：新增成员关系 `FactorSetMember(set_id, factor_id, status)`，`FactorDef` 去掉 `set_id` 与 `status`。后端规格单独成文（见上方关系）。前端随之：因子定义页只管定义，编辑器没有源数据集 / 频率，因子的「加入 / 启用 / 停用 / 移除」全部移到「计算任务」详情抽屉的「因子」成员区。**v2 的「保存时隐含创建计算任务」流程整体作废。** |
| 2 | 「计算结果」页参考采集结果页：tab 下不要解释行，搜索与列表样式尽量一致 | 重做为采集结果页同构：顶部 `.result-toolbar`（「共 N 个计算任务；状态读取：…」+「刷新结果」）→ 圆角 Tab → 内嵌 `ViewBrowse`（状态行、查询面板、小号带边框表格、圆形翻页）。不再使用 `status-strip`。 |
| 3 | 「补算」页 tab 下也不要解释行，解释信息收进 info 小图标，hover 再显示 | 「计算任务」「补算」Tab 的说明、因子定义页标题的说明，统一改为 ⓘ 图标 + hover 浮层；Tab 下方只保留计数 / 工具栏。 |

v2 评审决议（保留）：

| # | 评审意见 | 决议 |
| --- | --- | --- |
| 1 | 因子定义放在因子总览下面；补算放最后 | 菜单顺序：因子总览 → 因子定义 → 计算任务；补算在「计算任务」页的 Tab 中排最后。 |
| 2 | 「因子集」换名；与计算结果、补算合并成子 Tab，参考「采集任务」 | 界面上「因子集」更名为**计算任务**；「计算任务 / 计算结果 / 补算」合并为一个菜单页「计算任务」，用标题 Tab 组织。菜单由 5 项收为 3 项。 |

### 术语对照（界面文案 ↔ 代码 / 接口 / 后端文档）

| 界面 | 代码与后端 | 说明 |
| --- | --- | --- |
| 计算任务 | `FactorSet`、`set_id`、`ListFactorSets` … | 一个 `(源数据集 × 频率)`，持续计算一组因子，写入一个结果数据集和默认视图 |
| 因子定义 / 因子 | `FactorDef`、`factor_id` | 一个 Python 算法 + 静态参数 + 显式输入输出声明；**与计算任务无关，无状态**，可被多个计算任务引用 |
| 任务里的因子（成员） | `FactorSetMember`、`FactorMember` | 某个因子定义在某个计算任务中的运行实例，状态「已启用 / 已停用」 |
| 补算 | `RecalcJob`、`RecalcFactors` | 对历史范围的一次性重算；`factor-enable-` 前缀的是启用回填 |
| 计算结果 | 结果 View（`view_role === "factor_result"`） | 计算任务唯一的输出数据集的默认视图 |

只改界面文案，**代码标识符、接口名、数据库、后端设计文档继续使用 FactorSet / `set_id`**，避免无谓的大范围改名。界面里原有的「因子集」字样统一替换为「计算任务」（含测试断言）；后端返回的错误消息不在此列。

## 1. 背景与问题

因子计算目前只有一个菜单页「因子工作台」（`/factor/workbench`），所有能力都挤在这一页里：

1. **入口单调**：侧栏「因子计算」下只有一项，用户看不出模块里有哪些对象和能力。
2. **页内结构复杂**：左侧因子集列表 + 因子集头部 + 概览/因子/补算/结果四个 Tab + 因子编辑抽屉，两层导航叠在一起。
3. **与数据采集不一致**：数据采集按对象拆成多个菜单页，其中「采集任务」用标题 Tab 把任务、实例、执行器、结果收在一页；因子计算没有沿用这套组织方式。
4. **因子集选择是全局前置条件**：不管想看因子定义、补算还是结果，都必须先在左栏选因子集，跨因子集的横向视图无从谈起。
5. **因子定义被绑死在因子集上**：现状里 `FactorDef` 自带 `set_id` 和启停状态，新建因子必须先理解并选择因子集，也无法把同一个算法用到多个计算任务；定义（算法本身）与使用（在某个任务里运行）混为一谈。

## 2. 目标与非目标

目标：

- 因子计算拆成 3 个菜单页，组织方式与数据采集一致；
- 每个页面只回答一个问题，页内导航只有一层（标题 Tab）；
- **定义与使用分离**：因子定义页 / 编辑器只处理算法本身，「让它在某个计算任务里跑起来」在计算任务里完成；
- 复杂的因子编辑从抽屉升级为独立整页；
- 计算任务（原因子集）不再是左栏前置选择，而是各页面的**筛选条件**（`?set=`）；
- 计算结果、补算 Tab 的版式与采集模块一致，说明文字不占版面（ⓘ 悬浮）。

非目标：

- 不新增图表依赖，不做因子绩效分析（IC、分层收益等）页面；
- 不保留 `/factor/workbench` 兼容跳转（仓库约定：新项目无需向后兼容，旧入口直接删除）；
- 不在本文重复后端解耦的数据模型与校验细节，统一引用后端规格。

> 相对 v2 的变化：v2 声明「不改后端」，v3 不再成立——解耦必须先有后端的成员关系，前后端按 §11 的顺序分别落地。

## 3. 设计原则

继承 10-04 工作台文档的原则，并补充：

1. **后端是事实来源**：状态、健康、回填、降级一律来自接口返回，前端只做展示派生（`health.ts` / `status.ts` 逻辑不变）。
2. **启停只经 `SetFactorMemberStatus`**（原 `SetFactorStatus`）：状态属于「计算任务里的成员」而不是定义；启用会增加结果列并自动触发回填，需把 `backfill_job` 呈现给用户。
3. **降级不是错误**：`succeeded` 且 error 以 `degraded:` 开头的补算任务展示为「部分降级」，与失败分开（`splitJobNote`）。
4. **一页一主题**：总览看全局，因子定义管算法，计算任务页管「计算任务（含其因子成员）/ 计算结果 / 补算」。
5. **定义与使用分离**：因子定义页没有任何「启用 / 停用」，也不显示某个任务的运行状态；定义只展示「使用情况」（被哪些任务使用）。运行相关的操作一律出现在计算任务的成员区。
6. **跨页跳转带上下文**：用 `?set=<set_id>` 传递计算任务，`?tab=` 选择 Tab，`?detail=<set_id>` 打开详情抽屉，可选 `?job=<job_id>` 高亮补算任务；URL 即状态，刷新 / 分享不丢。
7. **复用而非重造**：页头、表格、抽屉、`PageTitleTabs`、`ViewBrowse` 均复用采集模块已有写法（`.moox-page > .moox-inner` 结构、绿色 `type="primary" status="success"` 新建按钮；创建按钮样式有全局契约测试约束）。注意 `.page-head`、`.result-toolbar` 在采集页里是**各页自带的 scoped 样式**而不是全局类，新页面按同样的结构与数值自备，重复部分抽到 `factor/shared/factor-page.scss` 共用，不改动采集页。
8. **说明不占版面**：页面 / Tab 的解释文字放进 ⓘ 图标的 hover 浮层（20px 图标按钮，`aria-label="说明"`，hover / focus 显示），不在标题或 Tab 下方单独占一行。

## 4. 信息架构

### 4.1 菜单与路由

侧栏「因子计算」（目录 id `0240`，图标 `experiment`，位置不变）下 3 个菜单页：

| 排序 | 菜单 id | 路由 path | 路由 name / 菜单名 | 组件 | 中文名 |
| --- | --- | --- | --- | --- | --- |
| 1 | 024001 | `/factor/overview` | `factor-overview` | `factor/overview/index` | 因子总览 |
| 2 | 024002 | `/factor/definitions` | `factor-definitions` | `factor/definitions/index` | 因子定义 |
| 3 | 024003 | `/factor/tasks` | `factor-tasks` | `factor/task-management/index` | 计算任务 |

目录项 `factor-compute` 的 path 由 `/factor/workbench` 改为 `/factor/overview`（点击目录默认进入总览）。

「计算任务」页是一个 **Tab 宿主**，与 `collector/task-management/index.vue` 同构：`PageTitleTabs` + `<keep-alive><component :is="activeComponent" /></keep-alive>`，Tab 由 `?tab=` 控制，默认 Tab 不写入 URL：

| Tab | `?tab=` | 标题 | 内容 | 对象 |
| --- | --- | --- | --- | --- |
| 1（默认） | 省略 | 计算任务 | 计算任务列表、详情抽屉（含因子成员管理）、新建 / 启停 / 删除 | FactorSet + FactorSetMember |
| 2 | `results` | 计算结果 | 按计算任务切换浏览结果 View | 结果 View |
| 3 | `recalc` | 补算 | 补算任务列表、新建补算 | RecalcJob |

隐藏页（`meta.hide: true`，不进侧栏，约定同 `strategy-definition-new/edit`）：

| 路由 path | name | 组件 | 中文名 |
| --- | --- | --- | --- |
| `/factor/definitions/new` | `factor-definition-new` | `factor/editor/index.vue` | 新建因子 |
| `/factor/definitions/:factorId/edit` | `factor-definition-edit` | `factor/editor/index.vue` | 编辑因子 |

`factor_id` 全局唯一，`getFactor(factor_id)` 即可定位。新建页不再接受任何预填参数（v2 的 `?dataset=&freq=` 删除）。

语言包（`lang/modules/zhCN.ts` / `enUS.ts`）：删除 `factor-workbench`，新增

| key | zh-CN | en-US |
| --- | --- | --- |
| `factor-overview` | 因子总览 | factor overview |
| `factor-definitions` | 因子定义 | factor definitions |
| `factor-tasks` | 计算任务 | compute tasks |
| `factor-definition-new` | 新建因子 | new factor |
| `factor-definition-edit` | 编辑因子 | edit factor |

### 4.2 对象关系与跳转

```
因子定义 ──(被引用)──▶ 计算任务里的因子成员 ──(启用后写入)──▶ 计算结果

因子总览 ──卡片名称────────▶ 计算任务?detail=<set>      （打开详情抽屉）
         ──卡片「因子」────▶ 计算任务?detail=<set>      （抽屉里的「因子」成员区）
         ──卡片「结果」────▶ 计算任务?tab=results&set=
         ──卡片「补算」────▶ 计算任务?tab=recalc&set=
         ──待处理项────────▶ 对应 Tab / 计算任务详情
因子定义 ──新增 / 编辑─────▶ 因子编辑器 ──保存 / 取消──▶ 因子定义
         ──「使用情况」chip─▶ 计算任务?detail=<set>
计算任务 ──抽屉「添加因子」─▶ 从因子定义库中选择（弹窗）
         ──成员「编辑定义」─▶ 因子编辑器
         ──成员启用后 backfill_job──▶ 补算 Tab ?set=&job=
         ──抽屉「前往」────▶ 因子定义 / 计算结果 Tab / 补算 Tab（后两者带 set）
```

### 4.3 计算任务上下文规则（`?set=`）

- **必选计算任务的 Tab**（计算结果、补算）：优先取 `?set=`，其次取 store 里的 `currentSetId`（跨 Tab / 跨页最近使用），再次取第一个；确定后用 `router.replace` 把 `set` 写回 URL。**切换标题 Tab 时保留 `set`**，因此在「计算结果」里选了 1m，切到「补算」仍是 1m。
- **「计算任务」Tab 与总览**不使用 `set` 作筛选；详情抽屉用独立的 `?detail=<set_id>` 表示，避免与跨 Tab 共享的 `set` 混淆。
- **因子定义页**不再有计算任务筛选（定义与任务解耦，一个定义可被多个任务使用）；改为「使用情况」筛选（全部 / 使用中 / 未使用），见 §5.2。
- 计算任务不存在（被删除、切换空间）时回落到第一个；空间切换时 store 重置并重新加载（行为同现有工作台）。

## 5. 页面设计

所有页面共同约定：根节点 `.moox-page > .moox-inner`；未选择空间时显示 `a-alert` 提示「请先在顶部选择空间」；加载失败显示带「重试」的 `a-alert`。

### 5.1 因子总览（`/factor/overview`）

只读 + 跳转，不放任何写操作。回答「现在整体健康吗、有什么要处理」。

- **页头**：`h2` 因子总览 + 刷新。
- **统计条**（4 格）：实时消费（`consumer_running`）、Python Worker（`python_busy/python_workers`）、计算队列（`lanes` 汇总）、进行中补算。
- **左列：计算任务卡片**，每个计算任务一张：标题 = 源数据集名称 · 频率；健康标签（`setHealth`：正常 / 落后 / 降级 / 失败 / 已停用 / 创建中 / 删除中）；该任务下每个**成员因子**一个状态 chip，来自 `last_run.factors[]`（complete / degraded / skipped；降级附「n 个对象失败」）；底部三个链接「结果 / 补算 / 因子」（跳转见 §4.2，「因子」进入该任务详情抽屉的成员区）。
- **右列：待处理**，纯前端由 `sets` 与补算任务派生，规则：
  - `failed`（最近周期失败）→ 计算任务详情；
  - `degraded` → 计算结果，文案带失败对象数；
  - `lagging`（落后超过 2 个频率周期）→ 计算结果；
  - `pending` 创建中 / 激活失败 → 计算任务（重试激活）；
  - 最近失败的补算 → 补算 Tab 并过滤「失败」。
- **右列：进行中的补算**，列出 `accepted/running` 任务（因子 · 进度条），点击进补算 Tab 并高亮。
- **数据来源**：`useFactorStore`（`sets` 已含 `members` 与 `last_run`）、`getFactorStatus`；进行中补算需要对每个 `enabled` 计算任务调用 `listRecalcJobs({ set_id, statuses: ["accepted","running"] })`（接口要求 `set_id`），见 §10 风险 1。
- **轮询**：10 秒，`usePolling`（KeepAlive 感知、页面不可见暂停）。

### 5.2 因子定义（`/factor/definitions`）

管理 FactorDef——**算法的库**。回答「有哪些因子、被谁用着、能不能改」。

- **页头**：`h2` 因子定义 + ⓘ（hover：「因子定义只描述算法本身：代码、参数、输入输出列。它不属于任何计算任务；要让因子运行，请到「计算任务」里添加并启用。」）+ 右侧「新增因子」（绿色主按钮，跳 `/factor/definitions/new`，无预填）。
- **筛选条**：类型下拉（全部类型 / 时序 / 横截面）、使用情况分段（全部 n / 使用中 n / 未使用 n；「使用中」= 至少被一个计算任务引用）、搜索（因子 ID / 模块名）。
- **数据来源**：`listFactors()`，`set_id` 缺省即返回全部定义，每个定义带 `usages: [{ set_id, status }]`（后端规格 §7）。计算任务名称由 `store.sets` + `datasetNames` 在前端拼出。筛选、分页（条数很少，前端分页）全在前端。
- **表格列**：因子 ID（`factor_id` + 次要文本「模块 xxx」）、类型、输入列、输出列、回看周期、**使用情况**、操作。**没有「状态」「最近周期」「所属计算任务」列。**
  - 使用情况：每个引用一个 chip「数据集名 · 频率」+ 状态点（绿 = 已启用，灰 = 已停用）；点击 chip 跳转到该计算任务详情抽屉；没有引用显示「未被使用」。
- **行操作**（没有启用 / 停用，那是成员的事）：
  - 详情（抽屉：基本属性、输入输出、参数 JSON、只读源码（用 §5.3 的代码编辑器组件，只读模式，同样深色高亮）、完整使用情况）；
  - 编辑：**被任一已启用成员引用时置灰**，tooltip「被启用中的计算任务使用，需先停用」（后端规则 D20）；否则进入编辑器；
  - 删除：**仍被任一计算任务引用时置灰**，tooltip「仍被计算任务使用，需先从计算任务中移除」（D21）；确认后 `deleteFactor`。

### 5.3 因子编辑器（隐藏页，新建 / 编辑）

独立整页，取代原先的编辑抽屉与 `factor-editor.vue`。页头为「← 返回因子定义 · 新建因子 / 编辑因子 `<factor_id>`」+ 右侧「取消」「保存因子」；副标题固定为「因子定义只描述算法本身（代码、参数、输入输出），不属于任何计算任务；有未保存的修改时，离开页面会二次确认。」左右两栏：

- **左栏表单**（**没有「因子集」「源数据集」「频率」，也没有「对应计算任务」提示**）：
  - 因子 ID（全局唯一；编辑时只读）、模块名；
  - 类型分段：时序 `timeseries` / 横截面 `cross_section`；选横截面时才出现「允许部分对象」`allow_partial_universe` 复选框；
  - 回看周期 `lookback_periods`；
  - **输入列**：自由输入的列名，以 chips 展示；帮助文字「只需写列名；加入计算任务时，会按该任务的源数据集校验这些列是否存在」。
    - **参考数据集**（可选，**仅用于提示可选列，不保存**）：下拉选一个数据集后，下方列出该数据集的列（`listDatasetColumns`），点击即可加入输入列 chips；不选也不影响保存。只列 `dataset_role !== "factor_result"` 的数据集。
  - **输出列**：chips，帮助文字「不得与系统保留列重名；加入计算任务时，还会检查与该任务源数据集列、其他因子输出是否重名」；
  - 参数：JSON 文本框，必须是对象（`params_json`）。
- **右栏**：Python **代码编辑器**（深色主题、Python 语法高亮、行号、当前行高亮、缩进辅助，见下）+ 「保存前检查」清单。清单项对应后端规格 §6.1 的**静态校验**：因子 ID 合法且全局唯一；输入列非空、名称合法、无重复；输出列非空、不含系统保留列（`subject_id`、`freq`、`data_time`、`series_tag`）、无重复；参数是合法 JSON object；回看周期合法；仅横截面允许 `allow_partial_universe`。逐项显示通过 / 未通过，未全部通过时「保存因子」禁用并说明原因。清单末尾一行信息提示：「保存后因子定义不属于任何计算任务；到「计算任务」里添加并启用。」
  - 「输入列 ⊆ 源数据集列」「输出列不与源数据集 / 其他因子冲突」这两类**依赖数据集的校验不在编辑器里做**，推迟到「添加到计算任务」与「启用」时由后端校验（D19），前端在添加弹窗里做预检（§5.4.1）。
- **源码编辑器（v3.1 新增）**：不再使用 `a-textarea`，改用真正的代码编辑器组件，原型按此绘制（深色底 `#1e1e1e`、VS Code Dark+ 配色）：
  - 外观：顶部标签条（文件名 `<factor_id>.py` + 「Python · UTF-8 · 缩进 4 空格」）、行号槽、当前行高亮、底部状态栏（行 / 列 + 「已通过语法检查」，后者来自保存前检查的源码 LOAD 结果）；
  - 行为：Python 语法高亮（关键字 / 函数名 / 参数 / 字符串 / 数字 / 注释分色）、Tab 缩进 4 空格、自动缩进、括号 / 引号自动配对、基本撤销重做与查找；编辑中不做语法检查请求，保存前由后端 §6.1 的源码试 LOAD 校验；
  - 组件：新增通用组件 `web/src/components/code-editor/index.vue`（`v-model`、`language`、`readOnly`、`minLines / maxLines`、`theme` 属性），**因子编辑器、因子定义详情抽屉（只读）共用**；选型与取舍见 §6「代码编辑器选型」；
  - 「插入模板」按钮继续保留：把 `compute(df, params, context)` 的骨架插入编辑器；已有内容时先二次确认；
  - 只读模式（编辑已启用引用的定义时，以及详情抽屉）使用同一组件的 `readOnly`，样式一致，不再另写一套 `<pre>`。
- **保存流程**：
  - 新建：`createFactor(form)`（不带 `set_id` / `status`），成功后返回 `/factor/definitions` 并 Toast「因子已创建，到计算任务里添加并启用」。**没有任何隐含的副作用请求**，不再创建计算任务或数据集。
  - 编辑：`updateFactor(form)`；因子 ID 不可改。若该定义被**已停用**成员引用，后端会对所有引用它的计算任务重新校验并整体拒绝（D20），前端把后端返回的「计算任务 + 原因」原样展示在表单顶部 `a-alert` 中。
- **编辑已启用引用的定义**：进入**只读模式**：顶部警告「该因子在 N 个计算任务中处于启用状态，需要先停用后才能编辑」并列出这些任务（chip 链到计算任务详情），表单全部禁用，提供「去计算任务」按钮。
- **状态规则**：没有 status 字段，也没有「启用」按钮——新建后的定义处于「未被使用」。
- **离开保护**：脏数据离开（返回、切菜单、关标签）弹确认，写法同策略编辑器（`onBeforeRouteLeave` + 保存成功后标记已提交）。
- **菜单高亮**：见 §10 待确认 1。

### 5.4 计算任务（`/factor/tasks`，Tab 宿主）

页面骨架同「采集任务」：`.moox-page > .moox-inner` 内先放 `PageTitleTabs`（`aria-label="计算任务"`），Tab 内容放在下方的 `<keep-alive>` 区域。Tab 顺序固定为 计算任务 → 计算结果 → 补算。**标题 Tab 下方不再放说明行**，各 Tab 的工具栏第一项是「共 N 个…」计数 + ⓘ。

#### 5.4.1 Tab「计算任务」（默认）

管理 FactorSet 生命周期与它的因子成员。回答「有哪些计算任务、各自什么状态、里面跑哪些因子」。

- **工具栏**：左侧「共 N 个计算任务」+ ⓘ（hover：「一个计算任务 = 一个源数据集 × 一个频率，持续计算一组因子，写入一个结果数据集和默认视图。」）；右侧搜索（计算任务或源数据集）、状态筛选、「新建计算任务」（绿色主按钮，打开现有 `SetCreateModal`，源数据集 / 频率 / 对象范围表单不变）。
- **表格列**：计算任务（名称 + `set_id` 次要文本）、源数据集、频率、对象范围（全部 / 指定 n 个）、因子（启用数 / 总数，数据来自 `members[]`）、状态、周期健康（`setHealth` 标签）、最近周期（时间 + `lag_seconds`）、操作。
- **行操作**：详情 / 停用 | 启用 | 重试激活（按状态切换）/ 删除。动作、确认文案与现 `set-header.vue` 完全一致：`setFactorSetStatus`、`updateFactorSet`（改对象范围）、`deleteFactorSet(set_id, purge)`，删除弹窗保留「同时清理结果数据」选项与说明；**删除的前置条件改为「无成员」**（D23），有成员时按钮置灰并提示先移除成员。
- **详情抽屉**（右侧，700px，`a-drawer`，`?detail=` 驱动），自上而下：
  1. **基本信息**：`set_id`、源数据集、频率、对象范围（可在此编辑）、结果数据集、结果视图、创建 / 更新时间；
  2. **因子（启用 n / 共 m）**——**成员区，因子的运行管理都在这里**。标题右侧「添加因子」（绿色小按钮）。每个成员一行：因子 ID + 类型 tag + 输出列；成员状态 tag（已启用 / 已停用）；最近周期 tag（来自 `last_run.factors[]`：正常 / 降级 / 跳过 / —）；行操作：
     - 编辑定义：跳编辑器；**已启用置灰**；
     - 启用 / 停用（`setFactorMemberStatus`）：启用后若返回 `backfill_job`，Toast 提示并给出「查看回填」链接（→ 补算 Tab `?set=&job=`）；
     - 移除（`removeFactorFromSet`）：**已启用置灰**，需先停用；移除保留结果列（D21 / D12）。
     
     区底的灰字说明：「启用时加列并自动回填历史；停用保留结果列。已启用的因子需先停用才能编辑定义或移除。」没有成员时显示「还没有因子。点击「添加因子」，从「因子定义」中选择。」；其下一行显示最近周期与滞后。
  3. **前往**：因子定义库 / 计算结果 / 补算（后两者带 `set`）；
  4. **危险区**：「删除计算任务…」+ purge 说明。
- **「添加因子」弹窗**（`a-modal`，标题「添加因子到「<计算任务名>」」）：
  - 候选 = `listFactors()` 的全部定义，多选，右上搜索；
  - 每行：复选框 + 因子 ID + 类型 tag + 「输入 … · 输出 …」+ 右侧 note：
    - 可添加（绿）；
    - 已在该计算任务中（灰，禁用）；
    - 不可添加的原因（红，禁用），如「输入列 funding_rate 不在源数据集中」「输出列与已有因子重名」——前端用 `listDatasetColumns(source_dataset_id)` 与成员输出做**预检**，以提示为目的；**权威校验仍是后端 `AddFactorToSet`**，提交失败时按行展示后端原因；
  - 底部「取消」「添加（n）」：对每个选中项调用 `addFactorToSet`，添加后成员为**停用**状态（不加列、不补算），启用在成员行里单独做。
- 数量很少（store 已全量拉取），不做分页与服务端筛选。**轮询**：10 秒（状态会从 pending 迁移到 enabled）。

#### 5.4.2 Tab「计算结果」（对齐采集结果页）

浏览结果 View。回答「算出来的数据长什么样」。版式照搬采集模块「采集结果」（`collector/task-results` + 内嵌 `ViewBrowse`）：

- **顶部工具栏**（`.result-toolbar`，同采集结果）：左侧「共 N 个计算任务；状态读取：HH:mm:ss」，右侧「刷新结果」（`a-button`，带刷新图标）。**Tab 与工具栏之间没有说明行。**
- **计算任务圆角 Tab**（`a-tabs type="rounded"`，同采集结果按任务切换的写法），每个计算任务一个 Tab，选中项与 `?set=` 同步；`pending` 的不出现，`disabled` 的显示「已停用」标记。
- **数据区**：内嵌现有 `ViewBrowse`（`:embedded="true"`），版式与采集结果一致：
  - **状态行**（`view-status-line`）：View 名、Dataset、「时序」「已构建」tag、「日志」「重建视图」入口，加因子计算特有的「最近周期」与「正常 / 降级」tag——这些放进 `ViewBrowse` **已有**的 `status-extra` 插槽（`views/data/view-browse/index.vue`，无需改动 `ViewBrowse`），**取代 v2 在 `ViewBrowse` 之上另放的 `status-strip`**；
  - **查询面板**：数据 ID（模糊）、时间范围、因子列条件（带运算符按钮）、「查询 / K 线 / 清空」；
  - **表格**：小号带边框，列 = 序号、数据 ID、频率、序列标签、时间、各因子输出列，表头可排序（▲▼），每行「查看」；
  - **翻页**：圆形上 / 下一页按钮。
  - View 由 `view.attributes.view_role === "factor_result"` 且 `dataset_id === result_dataset_id` 选出，列、分页、排序、筛选能力完全复用，不新增表格实现。
- 没有结果 View 时显示 `a-empty`，并给出「前往计算任务」链接。

#### 5.4.3 Tab「补算」

管理 RecalcJob。回答「补算跑到哪了、为什么失败」。

- **工具栏**：左侧「共 N 条补算任务」+ ⓘ（hover：「启用因子时的历史回填与手动补算都在这里；有进行中的任务时每 3 秒自动刷新。」）；右侧计算任务选择（必选，§4.3 规则）、刷新、「新建补算」（绿色主按钮，打开抽屉）。**Tab 下方没有说明行。**
- **筛选**：状态分段 全部 / 进行中（accepted+running）/ 已完成（succeeded）/ 失败·取消（failed+cancelled）。
- **表格列**：任务（`job_id` + 来源 tag：`factor-enable-` 前缀显示「启用回填」，其余「手动」）、因子、时间范围、状态（`succeeded` 且 error 以 `degraded:` 开头显示「部分降级」+ 说明；失败显示错误原因）、进度（`progress_time` 相对时间范围的进度条）、创建时间、操作（进行中可取消 `cancelRecalcJob`）。`?job=` 命中的行高亮并滚动到可视区。
- **新建补算抽屉**（取代原 Tab 内联表单）：因子多选（候选为该计算任务的**已启用成员**，默认全选；与后端 `RecalcFactors` 的约束一致）、对象范围（全部 / 指定）、时间范围（开始 / 结束）+ 快捷 chip（最近 100 个周期 / 1 天 / 7 天）、汇总行「将处理 N 个周期 × M 个因子 × 对象范围」（按频率与时间范围前端估算）、请求 ID（占位文案「留空自动生成」）、「提交补算」按钮；提交 `recalcFactors`，成功后关闭抽屉、刷新列表并高亮新任务（状态「已受理」）。
- **轮询**：3–5 秒，仅当存在进行中任务时继续（沿用 `recalc.vue` 现有间隔）；`listRecalcJobs` 为服务端分页，翻页保持现状。

## 6. 共享层与文件结构

```
web/src/views/factor/
├── overview/index.vue
├── definitions/
│   ├── index.vue
│   └── factor-detail-drawer.vue
├── editor/
│   ├── index.vue                    ← 取代 workbench/factor-editor.vue
│   └── factor-form.ts               ← 迁移自 workbench/，只保留静态校验并产出 checklist
├── task-management/index.vue        ← Tab 宿主（对标 collector/task-management）
├── compute-tasks/
│   ├── index.vue                    ← Tab「计算任务」
│   ├── task-detail-drawer.vue       ← 取自 set-header.vue 的动作 + 因子成员区
│   ├── add-factor-modal.vue         ← 添加因子弹窗（候选 + 预检）
│   ├── set-create-modal.vue         ← 迁移自 workbench/
│   └── subject-scope-fields.vue     ← 迁移自 workbench/
├── results/index.vue                ← Tab「计算结果」
├── recalc/
│   ├── index.vue                    ← Tab「补算」
│   └── recalc-create-drawer.vue
├── shared/
│   ├── health.ts                    ← 迁移自 workbench/
│   ├── status.ts                    ← 迁移自 workbench/（含 splitJobNote）
│   ├── use-factor-scope.ts          ← 空间切换加载 + ?set= ↔ store 同步 + 轮询
│   ├── set-select.vue               ← 计算任务下拉（名称 · 频率 + 状态点）
│   ├── health-tag.vue               ← setHealth 标签
│   └── info-tip.vue                 ← ⓘ 悬浮说明（`aria-label="说明"`，hover / focus 显示）
└── __tests__/factor-contract.spec.ts

web/src/components/code-editor/      ← 通用代码编辑器（CodeMirror 6，因子编辑器与详情抽屉共用，见下文「代码编辑器选型」）
├── index.vue
└── theme.ts                         ← Dark+ 配色的 EditorView.theme + HighlightStyle
```

`store/modules/factor.ts` 保持职责不变（`sets`、`engine`、`load/reload/select/refreshEngine/reset`），调整三点：

1. `currentSetId` 的语义改为「跨页最近使用的计算任务」，仅在必选计算任务的 Tab 作为默认值；
2. 新增 `datasetNames`（`ListDatasets` 的 `dataset_id → name` 映射，按空间加载一次），用于把计算任务显示为「数据集名称 · 频率」，`set_id` 退为次要文本；
3. `sets[].members` 取代 `sets[].factors`（后端规格 §7.2）；成员状态变更、添加、移除后调用 `reload()`。

`use-factor-scope.ts` 封装各页重复的样板：监听 `spaceStore.selectedSpaceId` → `store.reset()` + `store.load()`、`?set=` 与 store 同步（`requireSet` 参数区分 §4.3 的两类场景）、`usePolling` 接入。各页只关心自己的表格与动作。

两点来自对 `Main/index.vue` 的核对：主区域是 `keep-alive` 且 `:key="route.fullPath"`，**query 一变（切 Tab、切计算任务）页面组件就会重新挂载**。因此 `use-factor-scope` ①**不得在每次挂载时 `reset()`**，只在空间 id **变化**时 reset + load（旧工作台是 `watch(spaceId, { immediate: true })` 里无条件 reset，新写法要先判断 store 是否已加载同一空间）；②标题 Tab 切换用 `router.replace` 更新 `tab`，由于会重新挂载，Tab 内容的筛选状态要么进 URL，要么存 store，不能只放组件内部。

**API 层（`api/factor/*`）随后端解耦调整**（v2 的「API 层不变」作废）：

| 变更 | 说明 |
| --- | --- |
| `FactorDef` 类型 | 去掉 `set_id`、`status` |
| `FactorInfo` / `FactorUsage` / `FactorMember` 类型 | 新增（后端规格 §7.2） |
| `FactorSetInfo` | `factors` → `members`（`FactorMember[]`） |
| `listFactors` | `set_id` 改为可选；返回 `FactorInfo[]` |
| `createFactor` | 请求去掉 `set_id`、`status` |
| `addFactorToSet` / `removeFactorFromSet` / `setFactorMemberStatus` | 新增；`setFactorStatus` 删除 |
| `recalcFactors` | 参数不变，语义上 `factor_ids` 必须是已启用成员 |

### 代码编辑器选型（v3.1）

现状：`web/package.json` 里**没有任何代码编辑 / 高亮库**，策略编辑器（`views/strategy/editor/index.vue`）的 DSL YAML 也是 `a-textarea.code-input`。要做到「深色 + 语法高亮」，必须新增依赖，候选如下：

| 方案 | 体积 / 复杂度 | 评价 |
| --- | --- | --- |
| **CodeMirror 6**（`codemirror` + `@codemirror/lang-python` + `@codemirror/theme-one-dark` 或自定义 Dark+ 主题） | 模块化、按需引入，gzip 后约一二百 KB；无 worker，Vite 零配置 | **推荐**：支持编辑、高亮、行号、缩进、撤销、查找、只读模式；同一组件还能用 `@codemirror/lang-yaml` 给策略 DSL 编辑器复用 |
| Monaco Editor | 体积在 MB 级，需配置 Vite worker | 对一段几十行的因子源码过重 |
| `textarea` + 叠加高亮层（highlight.js / Prism） | 体积小 | 光标、选区、滚动、IME 输入容易错位，维护成本高，不建议用于「可编辑」场景 |
| 只读高亮（shiki / highlight.js） | 小 | 只能用于详情抽屉，编辑器仍缺 |

决定（已确认，§10 待确认 3）：采用 **CodeMirror 6**，封装为 `web/src/components/code-editor/index.vue`：

- 属性：`v-model`、`language`（首期仅 `python`，预留 `yaml`）、`readOnly`、`minLines`、`maxLines`、`theme`（默认深色）、`placeholder`；
- **按需加载**：用 `defineAsyncComponent` + 动态 `import()`，只有进入因子编辑器 / 详情抽屉才加载，不增加首屏体积；
- 主题：自定义一套 Dark+ 配色的 `EditorView.theme` + `HighlightStyle`，与原型一致（背景 `#1e1e1e`、关键字 `#569cd6` / `#c586c0`、函数 `#dcdcaa`、字符串 `#ce9178`、注释 `#6a9955`），不依赖 Arco 的主题变量；容器圆角 / 边框沿用全局 token；
- 可访问性：编辑区的文本框语义由 CodeMirror 默认提供，顶部标签条提供可见的标题 / 文件名；Tab 键用于缩进时，按 CodeMirror 的无障碍建议保证键盘仍可离开编辑器（如 `Esc` 之后再按 `Tab`），实施时按所用版本的 API 实现；
- 首期**只用于因子源码**（编辑 + 只读）；策略编辑器的 DSL YAML 是否顺带切换，作为后续独立改动，不在本次范围。

## 7. 状态、轮询与缓存

- 计算任务与引擎状态是跨页共享的 store 数据；谁在前台谁轮询（`usePolling` 在 KeepAlive 失活或标签页不可见时暂停，恢复时立即补一次）。
- 页面级筛选条件全部进 URL query（`tab`、`set`、`detail`、`job`，以及因子定义页的 `type`、`usage`、`q` 视需要），不依赖组件内部状态。
- Tab 宿主沿用采集任务的 `activeTab` + `normalizeTab` + `router.replace` 写法；切换 Tab 时保留 `set`，清掉与目标 Tab 无关的 `detail` / `job`。
- 各页 `defineOptions({ name })` 与 keepAlive 配置对齐采集模块页面的做法，保证从侧栏切换回来时筛选与滚动位置保留。
- 因子定义页的 `listFactors()` 与计算任务页的成员操作是两份数据：成员添加 / 移除 / 启停后，除了 `store.reload()`，还需让因子定义页的使用情况失效（用 store 的一个 `definitionsVersion` 计数触发重新拉取，或页面 `onActivated` 时重取，实施时二选一，倾向后者，更简单）。

## 8. 迁移与删除清单

删除：

- `web/src/views/factor/workbench/` 整个目录（`index.vue`、`set-list.vue`、`set-header.vue`、`factor-editor.vue`、`tabs/*`，其余文件迁移见 §6）；
- `static-menu.ts` 中的 `factor-workbench` 菜单、`route.ts` 中的 `/factor/workbench` 路由、两个语言包中的 `factor-workbench`；不保留任何旧路由重定向；
- API 层的 `setFactorStatus` 及其类型。

修改：

- `static-menu.ts`：目录项 path 改 `/factor/overview`，新增 3 个菜单项；
- `router/route.ts`：新增 3 个页面路由 + 2 个隐藏编辑器路由；
- `lang/modules/zhCN.ts`、`enUS.ts`：见 §4.1；
- `views/home/home.vue`：首页入口 —— 「因子定义」→ `/factor/definitions`；原「因子集」入口改名「计算任务」→ `/factor/tasks`；结果类入口（约 387、503 行）→ `/factor/tasks?tab=results`；
- 全局检索界面文案中的「因子集」，统一为「计算任务」（组件、提示、确认弹窗、测试断言）；
- `Menu/index.vue`（`selectedKeys` 改为优先取 `route.meta.activeMenu`，直接读 `useRoute()`）、`layout-head/index.vue`、`layout-mixing/index.vue` 的同类选中逻辑、`typings/global.d.ts` 的 `MetaType.activeMenu`（见 §10 待确认 1）；
- `vitest.config.ts`：`include` 里写死的 `src/views/factor/__tests__/factor-contract.spec.ts` 随测试文件位置同步；`scripts/check-detail-page-style.mjs` 里已失效的 `factor/sets|definitions|tasks|results` 引用改为新页面（该脚本目前就会 ENOENT）；
- strategy 与首页的溢出点：`views/strategy/components/strategy-instance-create.vue`（按 `factor.status === "enabled"` 过滤因子）、`views/strategy/bindings.ts` 及 `bindings.test.ts`、`views/home/home.vue` 约 626–629 行（消费 `FactorSetInfo.factors`）——全部改为基于成员（`members[].status`）；
- `scripts/check-menu-structure.mjs`：见 §9。

## 9. 测试方案

- `scripts/check-menu-structure.mjs`：
  - zh-CN 标签断言由「因子工作台」改为 3 个新标签（因子总览 / 因子定义 / 计算任务）；
  - 断言 `factor-compute` 下恰有 3 个菜单，顺序为 overview → definitions → tasks，路由均存在；
  - 断言 2 个编辑器路由存在且 `meta.hide: true`、不在菜单；
  - 原先「`factor-sets / factor-definitions / factor-tasks / factor-results` 已并入工作台」的退役断言删除，改为退役 `factor-workbench`、`factor-sets`、`factor-results`、`factor-recalc`（不再是独立菜单或路由）；`factor-bindings / factor-datasets / factor-construct` 保持退役。
- `views/factor/__tests__/factor-contract.spec.ts`：「API 契约」一组按 §6 的 API 变更更新（`FactorDef` 不含 `set_id` / `status`、`listFactors` 的 `set_id` 可选、新增三个成员接口、删除 `setFactorStatus`）；「工作台契约」一组按页面拆开，`?raw` 引用改到新文件，原断言随功能迁移：
  - 总览：`route.query.set`、`usePolling`；
  - 任务宿主：`route.query.tab`、`PageTitleTabs`、三个 Tab 的 key；
  - 计算任务 Tab：`updateFactorSet`、`deleteFactorSet(…, purge.value)`、`setFactorSetStatus`；**新增**：抽屉成员区调用 `setFactorMemberStatus(…)`、`removeFactorFromSet`、`backfill_job`、「需要先停用」，添加弹窗调用 `addFactorToSet` 与 `listDatasetColumns`；
  - 因子定义：**新增**不含 `setFactorMemberStatus`、不含「启用」「停用」操作列、含「使用情况」、编辑 / 删除的置灰规则（`usages`）；
  - 编辑器：`form.factor_type === 'cross_section'`、`timeseries` / `cross_section` 选项、不含 `v-model="form.status"`、`onBeforeRouteLeave`；**新增**：不含「因子集」「源数据集」选择项、不调用 `createFactorSet`、`createFactor` 请求不含 `set_id`；
  - 补算：`listRecalcJobs`、`cancelRecalcJob`、`splitJobNote`、「部分降级」、不含 `getRecalcJob`；
  - 结果：`view.attributes?.view_role === "factor_result"`、`result_dataset_id`、不含 `ViewDefinitions`、**使用 `result-toolbar` 与 `ViewBrowse` 的 `status-extra` 插槽、不含 `status-strip`**。
- `tests/page-layout-standard-contract.test.ts`：把「工作台集合列表 + Tab 同一布局」「因子集结果」两处引用改到新页面；保留「结果 View」、`:embedded="true"` 断言，**`status-strip` 断言替换为 `result-toolbar` + `status-extra`**；新增「任务宿主使用 `PageTitleTabs` 与 `keep-alive`」断言；新增「各 Tab 标题下不单独放说明行，使用 `info-tip`」的静态断言。
- `tests/factor-dataset-workflow.spec.ts`（Playwright e2e，mock 网关；需补 `AddFactorToSet`、`SetFactorMemberStatus`、`RemoveFactorFromSet` 的 mock，`ListFactors` 返回 `FactorInfo`）：改为按页面走一遍
  1. `/#/factor/tasks` 可见计算任务名称与 `set_id`；点开详情抽屉可见「因子」成员区与 `Bias`；
  2. `/#/factor/definitions` 可见 `Bias` 及其「使用情况」chip；已启用引用的因子「编辑」置灰；
  3. `/#/factor/tasks?tab=results&set=…` 可见「共 N 个计算任务；状态读取」与「结果 View view_factor_binance_kline_1m」；
  4. `/#/factor/tasks?tab=recalc&set=…` 新建补算，填「留空自动生成」→「提交补算」→ 可见「已受理」；
  5. 新增：`/#/factor/definitions/new` 填写并保存，断言只调用 `CreateFactor`（不含 `set_id`）、回到列表后「未被使用」；离开确认；
  6. 新增：计算任务抽屉 →「添加因子」→ 勾选 → 添加（调用 `AddFactorToSet`）→ 成员为「已停用」→ 启用（调用 `SetFactorMemberStatus`，可见回填提示）；
  7. 新增：`/#/factor/overview` 可见计算任务卡片与状态条。
- 代码编辑器（新增）：
  - `components/code-editor/index.test.ts`：`v-model` 双向同步（外部改值后编辑器内容更新、编辑后触发 `update:modelValue`）、`readOnly` 时不可编辑、卸载时销毁 `EditorView`；jsdom 下 CodeMirror 的布局测量不可用，只断言文档内容与事件，不断言像素；
  - 契约测试：因子编辑器 / 详情抽屉引用 `code-editor`，且不再出现源码用的 `a-textarea`；
  - e2e：源码输入改为定位 `.cm-content` 后键入（contenteditable，不能再对 `textarea` 用 `fill`），断言保存请求体里的 `source_code`。
- 验证命令：`pnpm vitest run`、`pnpm vue-tsc --noEmit`（或仓库现有 `check` 脚本）、`pnpm check:menu`、`pnpm playwright test tests/factor-dataset-workflow.spec.ts`。

## 10. 风险与待确认

风险：

1. **总览的「进行中补算」需要按计算任务扇出**：`ListRecalcJobs` 必须带 `set_id`，N 个计算任务 = 每 10 秒 N 次请求。当前规模（个位数）可接受；缓解办法：只查 `enabled` 的、总览页失活时不轮询。若数量上来，建议后端提供不带 `set_id` 的聚合查询，前端再切换。
2. **前端依赖后端解耦先落地**：成员关系、`ListFactors` 的 `usages`、三个成员 RPC 都是后端规格新增项；前端实现前必须先合入后端，或先约定好 mock。见 §11 实施顺序。
3. **被多个任务引用的定义怎么改**：定义不可变更新只靠「被任一已启用成员引用时拒绝编辑」兜底，改动会同时影响所有引用它的任务（D20 要求对全部引用重新校验）。编辑器顶部需要把「N 个计算任务引用该因子」说清楚；若要对不同任务用不同参数，仍需注册为不同的 `factor_id`（D22）。
4. **预检与权威校验不一致**：添加弹窗里的前端预检（`listDatasetColumns` 对输入列、成员输出做比对）可能与后端 `AddFactorToSet` 结果不一致（例如源数据集列刚变化）。约定以后端为准，预检仅用于提前置灰与提示；后端拒绝时在对应行展示原因，不吞掉。
5. **名称显示**：计算任务显示为「数据集名称 · 频率」，`set_id` 退为次要文本；依赖 `ListDatasets` 的名称，取不到时回落为 `source_dataset_id`。e2e 断言需同步调整。
6. **「补算」Tab 与「计算任务」命名**：补算任务本身也是「任务」，为避免与「计算任务」混淆，Tab 只叫「补算」，列表与抽屉里再用「补算任务」「新建补算」。
7. **隐藏页的菜单高亮**：现有 `Menu` 的 `selectedKeys` 取 `routeStore.currentRoute.name`；而 `currentRoute` 只在 `route-output.ts` 里对**有权限的菜单路由**写入，隐藏路由会提前 return，所以进入隐藏页后高亮不会更新（保持上一个菜单的高亮，或没有高亮）。这意味着 `meta.activeMenu` **不能走 store**，必须在 `Menu` 里直接读 `useRoute().meta.activeMenu`（见待确认 1）。
8. **`ListFactorSets` 载荷变大**：成员里带完整 `FactorDef`（含源码），一次拉全量会随成员数增长。后端规格 §10 已记录，必要时让 `ListFactorSets` 返回成员摘要（不含 `source_code`），详情再查 `GetFactor`。
9. **新增 CodeMirror 依赖的代价**：构建体积增加（用动态加载隔离到编辑器 / 详情抽屉两处），jsdom 单测无法覆盖布局行为（见 §9），e2e 的源码输入方式要改；深色编辑器嵌在浅色页面里是刻意的视觉选择，如与全局暗色主题联动，`theme` 属性再扩展，本次不做。

待确认事项的处理（用户回复「ok」后，下列事项按文中推荐 / 默认方案执行，未列为阻塞项）：

1. 编辑器隐藏页的侧栏高亮（**已采纳推荐方案**）：路由 `meta.activeMenu` 存在时，用它作为选中项（因子编辑器指向 `factor-definitions`，策略编辑器可顺带受益）。核对代码后改动面比最初估计的大：`Menu/index.vue` 的 `selectedKeys` 要直接读 `useRoute()`（见风险 7）；`layout-head/index.vue`、`layout-mixing/index.vue` 有同类选中逻辑要一并改；`typings/global.d.ts` 的 `MetaType` 加 `activeMenu?: string`；共约 4 个文件、十来行，并补一个小的单元测试。
2. 总览「待处理」的规则是否够用，或需要增加（例如引擎消费停止、Python Worker 全忙）。
3. **新增前端依赖 CodeMirror 6**（`codemirror`、`@codemirror/lang-python`，主题自定义；按需动态加载）。这是「源码要语法高亮 + 深色」的必要代价（现有依赖里没有任何代码编辑 / 高亮库，见 §6）；已确认，写入 `package.json` / `pnpm-lock.yaml`。策略编辑器的 DSL YAML 是否顺带换成同一组件（`@codemirror/lang-yaml`）：本次不做，留作后续独立改动。
4. 原型中的数据集名称（如「现货K线」）和示例因子为示意；真实环境以接口返回为准。
5. 「添加因子」弹窗的候选是否需要隐藏「不可添加」的定义（当前设计是显示并置灰 + 原因，利于排查）。

## 11. 实施顺序

1. **后端先落地**：按后端规格实现 `FactorSetMember`、`ListFactors` 的 `usages`、`AddFactorToSet` / `RemoveFactorFromSet` / `SetFactorMemberStatus` 与迁移（前后端合并在同一份执行计划里分阶段落地，后端阶段先行）；前端并行只做不依赖接口的部分（步骤 3、4 的壳子）；
2. 共享层：新增 `components/code-editor/`（CodeMirror 6，见 §6「代码编辑器选型」）；`shared/*`（含 `info-tip.vue`）、`use-factor-scope.ts`、store 调整、API 类型与函数调整，迁移 `health.ts`、`status.ts`、`factor-form.ts`（删去依赖数据集的校验）、`set-create-modal.vue`、`subject-scope-fields.vue`；
3. 菜单 / 路由 / 语言包 / 首页链接，一并更新 `check-menu-structure.mjs`；
4. 页面：任务宿主 + 计算任务 Tab（含详情抽屉成员区与添加因子弹窗）→ 因子定义 → 因子编辑器 → 补算 Tab → 计算结果 Tab（用 `ViewBrowse` 已有的 `status-extra` 插槽）→ 因子总览（先管理类页面，总览最后基于它们的数据派生）；
5. 删除 `views/factor/workbench/`，清理死代码与未使用的 store 字段，替换残留的「因子集」文案；
6. 更新 / 新增测试，跑 §9 的验证命令；
7. 按仓库约定提交全部变更并 `git push`。
