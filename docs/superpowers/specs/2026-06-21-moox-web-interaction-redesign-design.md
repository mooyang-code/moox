# MooX Web 交互再设计 · 设计文档

> 状态：设计稿（待用户确认）。本设计只涉及**前端交互与信息架构**，不改动后端 API、proto 或存储逻辑；非存储模块（采集/策略/交易/运维）仅做菜单分组迁移，不改功能。

## 1. 目标

降低用户理解与使用成本：

1. 收敛「数据资产」下平铺的 9 个子菜单，按语义分组。
2. 重构「数据列表」——不再让用户手选"主存/视图 × 时序/记录"四层 tab。
3. 顶部空间选择器增加"新建空间"入口。
4. 全程不向用户暴露底层协议词汇（如"主存"、`RFC3339`、`SORT_ORDER_*`）。

## 2. 已确认决策

- 菜单结构采用 **X 方案（3 层）**：「数据资产」为唯一一级父菜单，下设 3 个二级分组，叶子页为三级。
- 三个二级分组：**数据建模 / 查询视图 / 数据管理**（"查询视图"独立成组）。
- 「数据来源」更名为 **「数据源」**。
- 第三组命名为 **「数据管理」**。
- URL **重命名**：`/data/list` → `/data/browse`，`/data/sync` → `/data/import`。
- 「数据浏览」来源切换用 **「数据集 / 视图」** 二选一，**默认「数据集」**；**不出现"主存"字样**。
- 点击一级「数据资产」落地到「数据概览」。

## 3. 最终信息架构

```
数据资产（一级，redirect → /data/overview）
├── 数据建模（二级目录）
│    ├── 数据源        /data/sources
│    ├── 数据对象      /data/subjects
│    ├── 数据集        /data/datasets
│    ├── 字段          /data/fields
│    └── 因子          /data/factors
├── 查询视图（二级·单页） /data/views
└── 数据管理（二级目录）
     ├── 数据概览      /data/overview
     ├── 数据浏览      /data/browse   （原 /data/list 重构）
     └── 数据导入      /data/import   （原 /data/sync 改名）
```

其余一级菜单（首页、计算与采集、策略管理、交易管理、资源与运维、系统设置）保持不变。

## 4. 页面交互规格

### 4.1 数据浏览（/data/browse）核心重构

用一次"选对象"替代四层 tab。表单分三步：

1. **选来源 + 选对象**
   - 二选一开关：`数据集` ｜ `视图`（默认 `数据集`）。
   - 紧随一个对象下拉（带名称搜索）：选「数据集」时列出本空间 Dataset；选「视图」时列出本空间 View。
2. **系统自动推断（对用户透明）**
   - 选「数据集」→ 读原始数据：按 `Dataset.data_kind` 自动决定调用 `ReadTimeSeriesRows`（时序）或 `ReadRecordRows`（记录）。
   - 选「视图」→ 查加速视图：按视图类型自动决定 `QueryTimeSeriesRows`（DuckDB 时序）或 `SearchRecordRows`（Bleve 记录）。
   - 推断结果以一行浅色提示展示（如"该数据集为时序数据"），不暴露接口名/“主存”。
3. **按类型显示筛选项**
   - 时序：对象(subject_id)、周期(freq)、开始/结束时间（日期时间选择器，提交时转 RFC3339Nano）、列名、排序（中文下拉 升序/降序）、页大小。
   - 记录：record_id、开始/结束版本、列名、排序、页大小。
   - 视图搜索额外：文本检索、过滤 JSON、排序 JSON（高级区，默认折叠）。
   - 不显示与当前类型无关的字段。
4. **查询与结果**
   - 一键查询；结果表根据返回列自适应渲染；分页用 keyset/page。

校验：时间字段在前端用 `timeSeriesValidator` 校验为 RFC3339/RFC3339Nano 后再提交。

### 4.2 顶部空间选择器 + 新建空间

- `layout/layout-head/index.vue` 的空间下拉底部增加「＋ 新建空间」项；点击弹出创建表单（space_id、name、description、owner、market、timezone），调用 `@/api/control/spaces:createSpace`，成功后刷新列表并自动 `setSelectedSpace` 到新空间。
- 保留右侧齿轮进入 `/settings/spaces`。
- 无空间时下拉显示空态提示 + 直接引导新建。

### 4.3 其余数据页

- 数据源/数据对象/数据集/字段/因子/查询视图/数据概览：**功能与现状一致**，仅菜单归类与「数据源」改名；不重写。
- 数据导入（原 sync）：仅路由与菜单文案改名为「数据导入」，页面功能不变。

## 5. 涉及改动的文件（前端）

- `web/src/mock/_data/system_menu.ts`：重排为 X 三层结构（新增"数据建模""数据管理"二级目录项，"数据源"文案，浏览/导入改名）。
- `web/src/router/route.ts`：`/data/list`→`/data/browse`、`/data/sync`→`/data/import`；目录与组件路径相应调整。
- `web/src/router/route-output.ts`：如有路由名引用同步更新。
- `web/src/lang/modules/{zhCN,enUS}.ts`：菜单 i18n 文案（数据源、数据建模、数据管理、数据浏览、数据导入等）。
- `web/src/views/data/browse/index.vue`：由 `data/list/index.vue` 重构而来（来源/对象选择 + 自动推断 + 分类型表单 + 中文枚举/日期选择器）。
- `web/src/views/data/import/index.vue`：由 `data/sync/index.vue` 改名迁移。
- `web/src/layout/layout-head/index.vue`：空间下拉新增"新建空间"入口与创建弹窗（可抽到子组件）。
- 旧 `data/list`、`data/sync` 目录在迁移后删除。

不改：`web/src/api/**`（API 层不变）、后端任何模块、非存储页面业务逻辑。

## 6. 非目标（YAGNI）

- 不调整后端 proto / 接口 / 存储。
- 不为非存储模块新增 Space 过滤逻辑（沿用现状）。
- 不引入新的可视化图表或权限模型。
- 不清理与本次无关的脚手架 Demo 页面（可后续单独处理）。

## 7. 验收标准

- 「数据资产」下仅见 数据建模 / 查询视图 / 数据管理 三个二级分组，无"建模元素""主存"等字样。
- 「数据浏览」无四层 tab；选对象后表单按类型自动切换、可查到数据。
- 顶部下拉可直接新建空间并自动切换。
- `pnpm vue-tsc --noEmit` 与 `pnpm build:prod` 通过；无遗留 `/data/list`、`/data/sync` 引用。
