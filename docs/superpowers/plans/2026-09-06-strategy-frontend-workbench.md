# 策略前端工作台重构执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不新增后台能力的前提下，重构策略前端，使策略定义、实例配置、启停、目标权重及历史结果的交互与当前后台实现一致。

**Architecture:** 保留 MooX 现有应用外壳，以运行实例为日常入口，策略定义独立管理，实例详情下钻。仅调用现代 StrategyInstance 接口，DSL 是唯一策略定义来源，数据绑定从现有 Storage、Factor 和 Trade 元数据生成。前端只展示后台可证实的状态，不计算策略、不推断成交、不增加通用工作流框架。

**Tech Stack:** Vue 3、TypeScript、Pinia、Vue Router、Arco Design Vue、Vitest、Playwright、现有 `callControl` 网关客户端；DSL 摘要使用结构化 YAML 解析。

---

## 1. 执行边界与事实基线

- 文档日期：2026-09-06；源码基线：`7a4e9491`，分支 `feature/mooyang`。执行前重新检查源码变化。
- 当前授权仅为生成计划文档。所有任务复选框保持未完成，不运行构建、不改业务代码、不部署、不启停策略。
- 当前线上入口：`https://106.53.107.122:9527/#/strategy/overview`。本地原型和开发服务器不等于线上更新。
- 已观察到线上 `ListStrategies` 返回 EOF。该现象只证明当时加载失败，不证明策略列表为空，也不能据此断言错误根因或线上后台版本。
- 本计划以当前本地后台源码为契约。真实联调前核验远端版本及路由；不把线上连通性修复夹带到前端重构中。
- 不修改 `modules/strategy`、`modules/trade` 的业务逻辑、Proto、数据库结构或运行配置；不增加删除、回测、立即运行、独立校验等 RPC。
- 本次已有无关改动包括 gateway 配置、Trade 文档、部署脚本；执行时保留他人改动，只提交本任务文件。
- 先前浏览器原型仅供布局讨论，示例字段、状态和模板必须重新按本计划核对，不能直接当成生产实现。

### 1.1 后台依据

以下路径均相对仓库根目录，行号是计划生成时的定位辅助，符号是执行时的主要定位依据。

| 依据 | 已核实的约束 |
|---|---|
| `modules/strategy/proto/strategy.proto:217`，StrategyMgr | 现代实例仅有创建、查询、列表和启停接口 |
| `modules/strategy/internal/rpc/service.go:314`，CreateStrategy / UpdateStrategy | 保存 DSL；名称取 `name`；完整绑定依赖编译留到启用 |
| `modules/strategy/internal/store/definitions.go`，UpdateStrategyDefinition | 有启用实例引用时禁止更新定义 |
| `modules/strategy/internal/rpc/service.go:763`，CreateStrategyInstance | 账户可选；创建并启用可能在落库后失败 |
| `modules/strategy/internal/rpc/service.go:900`，SetStrategyInstanceEnabled | 启用校验和账户认领；停用可能部分成功；会话由服务端维护 |
| `modules/strategy/internal/compiler/bindings.go:78`，CompileWithBindings | 频率一致、单结果 View、因子事件约束、字段别名冲突约束 |
| `modules/strategy/internal/compiler/verify_dependencies.go:150`，findFactorColumn | 用列属性 `origin_factor_id` 和 `factor_output` 对应因子输出 |
| `modules/strategy/internal/config/manifest.go:302`，validateRule / validateWeightBudget | `top/tail`、`weight/weight_each`、`signals/holding` 等 DSL 约束 |
| `modules/strategy/internal/selection/evaluator.go:252`，evaluateRule | 分配权重后进行后置过滤，不补位、不重分配；信号和批次有状态 |
| `modules/strategy/internal/trigger/processor.go:541`，handleInstances | 有因子的实例不能由不带就绪来源信息的定时事件直接计算 |
| `modules/strategy/internal/rpc/service.go:722`，ListStrategyTargets | 返回当前会话最新目标，但不替前端判断启用状态和过期时间 |
| `modules/strategy/internal/rpc/convert.go:39`，modernResultProto | 现代结果返回目标、时间、投递状态、规则状态，不返回完整调试过程或输入快照 |
| `modules/strategy/internal/store/definitions.go:487`，PreparePendingResult | 投递前检查启用、会话、有效期和最新结果；失效待投递结果转 cancelled |

## 2. 已确认的交互设计

### 2.1 页面与路由

保留现有 URL，调整名称与排序，不建立新旧双套页面。

| 路径 | 页面名称 | 职责 |
|---|---|---|
| `/strategy/running` | 运行实例 | 策略管理的首个菜单项；当前空间实例列表、筛选、分页、创建、启停 |
| `/strategy/overview` | 策略定义 | 定义列表、创建入口、编辑入口；不再称运行概览 |
| `/strategy/definitions/new` | 新建策略 | 独立 DSL 编辑页 |
| `/strategy/definitions/:strategyId/edit` | 编辑策略 | 读取并保存单一 DSL，保留未保存草稿 |
| `/strategy/detail/:instanceId` | 实例详情 | 最新目标、历史结果、数据绑定；只接收 instanceId |

- 继续使用当前空间选择器、导航、标签页和已有 Arco 图标；不重做全站外壳。
- 页面采用紧凑标题、工具栏、表格和无装饰分区；不设置宣传式首屏、嵌套卡片或大型统计卡。
- 运行列表字段：实例 ID、策略名称、观察/交易模式、关联账户、启用状态、更新时间、操作。session_id 放到详情，不占主表列。
- 列表只展示能按现有接口准确查询的筛选：策略、启用状态。不把当前分页内搜索伪装成全量搜索，不显示从当前页推算的全局统计。
- 名称或账户元数据加载失败时回退到真实 ID，不伪造名称，不阻断已有实例的查看。
- 刷新使用图标按钮和提示，启停使用明确命令按钮和确认框；长 ID 可换行或省略并查看完整值。

### 2.2 定义编辑

- DSL 是唯一可编辑定义，不增加独立可编辑的策略名称、触发配置或权重表单，以免双向同步失真。
- 创建输入 `strategy_id` 与 `dsl_yaml`；编辑时 ID 只读。名称从 YAML 的 `name` 读取，服务端响应为最终结果。
- 编辑器使用等宽字体、明确错误行列、稳定高度；先沿用 Arco 文本编辑控件，不引入大型 IDE 框架。
- 增加只读摘要：名称、K 线周期、交易日历、触发方式、规则名称和规则配置。解析失败显示定位错误，不擦除草稿。
- 使用 YAML 解析器读取单文档，拒绝重复键；不使用正则提取 YAML，不在前端实现 Expr 求值或另一套策略引擎。
- 前端语法检查通过只能称“YAML 语法通过”；保存成功只能称“策略已保存”，不能称“运行校验通过”。
- 已知存在启用实例时展示编辑限制；当前空间看不到的其他引用仍以服务端保存检查为准，不能宣称前端已检查所有空间。
- 保存被拒绝保留草稿，不自动停用实例；切换模板、离开编辑页、切换空间前对未保存内容确认。

### 2.3 创建实例

使用三步抽屉，关闭或重开不影响列表筛选。

1. 选择策略与输入实例 ID，显示 DSL 只读摘要；空间取当前选择，不再另填。
2. 选择源 View、因子绑定和输出；生成只读绑定 JSON 预览。无因子策略不显示必填因子项。
3. 选择“仅计算”或“发送给交易模块”；后者选择现有逻辑账户。确认后仅调用创建接口，固定 `enabled: false`，跳转实例详情。

- 源 View 列表来自 `listViews/getView`，频率按 View 的主 Dataset 元数据核对，不能假定 View 存在直接 `frequency` 字段。
- 因子定义、绑定、输出来自 `listFactorDefs/listFactorBindings`；列映射来自结果 View 的列元数据。
- 使用 `origin_factor_id + factor_output` 找真实 `column_name`，不默认列名等于 output，不允许任意填写 DSL 别名。
- 绑定必须属于当前空间和所选源 View；频率匹配 DSL；所选因子共享一个不同于源 View 的结果 View。
- 必填身份字段与元数据不完整时阻止完成绑定；未知或加载失败展示原因和重试，不猜测默认 source_hash、列名或范围。
- 元数据下拉按各 API 实际分页结构逐页读取，不能仅加载第一页。绑定/输出选择变化后清理失效的关联值。
- 有因子且触发配置不兼容时指出需在 DSL 中配置 `factor.ready`，提供返回编辑入口，不静默改 DSL。
- 账户选择不是下单；未关联账户表示仅计算而非 Paper 仿真。账户的实际可认领性仍由启用 RPC 最终确认。
- 请求超时或错误后保留表单，并按实例 ID 查询是否已创建，避免重复提交相同 ID；查询失败显示结果未知。

### 2.4 状态、结果和职责

| 维度 | 展示规则 |
|---|---|
| 启用状态 | 只根据实例 enabled 显示，不推导服务健康、数据已就绪或计算成功 |
| 控制操作未完成 | 停用且仍有 session_id 时显示会话尚未清理，不擅自断定是认领失败还是释放失败 |
| 无当前目标 | 无 session 或当前会话没有带时间的目标结果；不是清仓成功 |
| 零仓位目标 | 当前会话有明确结果时间、未过期且 targets 为空；不是查询失败 |
| 已过期目标 | 根据后台 valid_until 判断，只展示历史参考，不自行延长有效期 |
| 状态未知 | 时间缺失/无效、会话不匹配或请求失败；不能按有效目标处理 |
| 投递状态 | none=无需投递，pending=待投递，sent=已发送，cancelled=已取消投递；未知值保留为未知 |

- 最新目标必须调用 `ListStrategyTargets` 并保留 session_id、bar_end_time、valid_until；不能拿历史表首行替代。
- 历史结果的周期来自 RPC `period_time`，前端可统一命名 `bar_end_time`；与目标接口的同名字段做明确映射，不保留旧 Runner 时间字段回退链。
- 目标以权重百分比展示，正负权重分别标注多/空；不展示伪造数量、成交价格、收益率或剩余现金。
- 停用后旧目标只可作为历史参考。会话不匹配时重新读取实例和目标，仍不匹配则显示未知。
- 历史结果分页，筛选“本次启用”或“全部历史”。无 session 时本次结果为空，不能把空 session 参数传成查询全部历史。
- 结果详情展示该结果目标、时间、投递状态、只读规则状态；不把当前 DSL 伪装为历史结果生成时的 DSL。
- 规则状态不是实际交易持仓。首版保留折叠 JSON，不绘制接口没有返回的逐阶段打分和过滤漏斗。
- sent 不等于成交，cancelled 不等于撤单，过期不等于清仓。实际账户、订单和成交从关联账户入口进入交易模块。
- 停用确认明确“不执行清仓”。启停失败后重新读取实例和目标，不乐观回滚开关，不自动重试交易控制操作。

### 2.5 刷新与错误

- 列表默认手动刷新；实例详情提供可关闭的 10 秒自动刷新，仅页面可见时运行，离开或切换空间后停止。
- 请求按空间、实例、会话、筛选和页码隔离；旧请求晚返回必须丢弃，刷新不能覆盖编辑草稿。
- 首次加载、后台刷新、操作提交分别维护状态。后台刷新失败保留旧数据并标注更新时间和失败提示，不展示成实时有效状态。
- 首次请求失败使用错误态与重试入口，不能同时显示“暂无数据”；原始 RPC 错误放在可展开详情中。
- 不新增通用查询框架、复杂全局缓存或 WebSocket 推送，优先使用现有 Pinia 和组件生命周期。

## 3. 文件与责任划分

以下是计划触及的文件，执行前搜索调用方，仅清理策略相关兼容代码。

| 文件 | 处理方式与职责 |
|---|---|
| `web/src/api/strategy-types.ts`、`web/src/api/strategy.ts` | 精简为现代契约；保留结果与目标元数据；移除旧 Runner 客户端 |
| `web/src/store/modules/strategy.ts` | 列表/详情状态隔离、刷新和操作后对账；移除旧 Runner 状态 |
| `web/src/views/strategy/model.ts` | 新建：目标展示状态与频率匹配纯函数，不引入持久化模型 |
| `web/src/views/strategy/dsl.ts` | 新建：YAML 解析、只读摘要、内置模板 |
| `web/src/views/strategy/bindings.ts` | 新建：元数据筛选、列映射和绑定对象生成 |
| `web/src/views/strategy/overview/index.vue` | 重写为策略定义列表 |
| `web/src/views/strategy/editor/index.vue` | 新建完整 DSL 编辑页 |
| `web/src/views/strategy/running/index.vue` | 重写实例列表 |
| `web/src/views/strategy/components/strategy-instance-create.vue` | 新建三步创建抽屉 |
| `web/src/views/strategy/detail/index.vue` | 重写实例详情，删除 Runner 回退 |
| `web/src/views/strategy/components/strategy-operation-panel.vue` | 启停确认与状态刷新 |
| `web/src/views/strategy/components/strategy-target-table.vue` | 权重展示与空/过期状态 |
| `web/src/views/strategy/components/strategy-run-timeline.vue` | 改名为 `strategy-result-table.vue`，负责历史结果分页与详情入口 |
| `web/src/views/strategy/components/strategy-status-badge.vue` | 对齐真实状态，无调用方则删除 |
| `web/src/router/route.ts`、`web/src/api/modules/system/static-menu.ts` | 增加编辑路由，调整菜单顺序，保留现有 URL |
| `web/src/lang/modules/zhCN.ts`、`web/src/lang/modules/enUS.ts` | 对齐路由标题与菜单名称 |
| `web/package.json`、`web/pnpm-lock.yaml` | 仅将现有锁文件中的 YAML 库显式声明为运行依赖，不顺带升级其他依赖 |
| `web/src/api/strategy.test.ts`、`web/src/store/modules/strategy.test.ts` | 新建 API 契约和异步状态测试 |
| `web/src/views/strategy/model.test.ts`、`dsl.test.ts`、`bindings.test.ts` | 新建纯函数和约束测试 |
| `web/src/views/strategy/editor/index.test.ts`、`components/strategy-instance-create.test.ts` | 新建表单行为测试 |
| `web/src/views/strategy/components/strategy-operation-panel.test.ts` | 重写现代启停交互测试 |
| `web/src/views/strategy/overview/strategy-create-defaults.test.ts` | 替换旧默认字符串测试，断言来自实际模板模块 |
| `web/tests/strategy-console.spec.ts` | 更新现代实例 E2E；保留相邻 Trade 账户测试 |
| `web/playwright.strategy.config.ts` | 新建独立 mock 浏览器测试配置，避免复用已有未知服务器 |
| `modules/strategy/docs/frontend-verification.md` | 更新验收方式，分别记录 mock、本地与远端证据 |

## 4. 分阶段任务

各任务执行顺序为 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8。后续执行可对独立测试调查并行，但不得让多个执行者同时改同一文件。实现结束必须使用 codeCR 子 Agent 审查，主 Agent 独立核验结论。

### 任务 1：收敛现代 API 契约

**文件：** `web/src/api/strategy-types.ts`、`web/src/api/strategy.ts`、`web/src/api/strategy.test.ts`。

- [ ] 搜索所有策略 API 调用方，记录旧 Runner 引用，只在策略前端范围移除，不改 Trade 自己的所有权字段。
- [ ] 编写失败测试：createInstance 永远发送 enabled=false；结果请求只发 instance_id/session_id；目标响应保留全部时间及会话字段；业务错误不能被转换为空列表。
- [ ] 执行 `pnpm vitest run src/api/strategy.test.ts`，确认新增断言在旧实现失败。
- [ ] 采用以下明确契约，更新调用方，不用空 runner_id 占位参数：

```ts
export interface StrategyTargetSnapshot {
  targets: InstrumentTarget[];
  session_id: string;
  bar_end_time: string;
  valid_until: string;
}

// 实现时沿用 PageRequest、PageResult 和 callControl。
// listStrategyResults(instance_id, { session_id?, page?, page_size? })
// listStrategyTargets(instance_id): Promise<StrategyTargetSnapshot>
// getStrategyResult(result_id): 返回单条现代 StrategyResult
```

- [ ] 删除不再使用的 manifest_yaml、compiled_json、source_hash、command_sequence 及旧 Runner 客户端适配；定义返回字段使用 strategy_name、dsl_yaml、created_at，不虚构 updated_at。
- [ ] 重跑上述测试，确认请求字段与 Proto 一致；提交 `refactor(web): align strategy client with instance APIs`。

### 任务 2：目标状态与请求隔离

**文件：** `model.ts`、`model.test.ts`、策略 Store 及其测试，路径见第 3 节。

- [ ] 编写状态表驱动测试，覆盖停用残留 session、空目标、有结果时间的零仓位、过期、时间损坏、会话不匹配。
- [ ] 编写异步测试：实例 A 的慢响应不能覆盖 B；切换空间后旧响应丢弃；刷新失败保留旧数据；无 session 的本次历史不发起全历史查询。
- [ ] 执行 `pnpm vitest run src/views/strategy/model.test.ts src/store/modules/strategy.test.ts`，确认失败。
- [ ] 将展示判定提取为 `deriveTargetState(instance, snapshot, nowMs)`，输入实例 enabled/session_id、目标快照和显式 nowMs；返回 `inactive | unknown | empty | expired | zero | valid`，中文标签仅在展示层映射。实现以下优先级：

```text
停用 -> 不作为当前有效目标
启用且无 session -> 状态未知
没有快照时间且 targets=[] -> 尚无结果
session 不匹配、时间无法解析或元数据不完整 -> 状态未知
valid_until <= now -> 已过期
targets=[] -> 零仓位目标
其他 -> 有效目标
请求失败/旧缓存 -> 单独覆盖为加载失败或数据未刷新，不宣称实时有效
```

最小回归测试内容如下，保存到 `web/src/views/strategy/model.test.ts`，其余组合继续使用同一接口：

```ts
import { expect, it } from "vitest";
import { deriveTargetState } from "./model";

it("distinguishes a zero target from no evaluation", () => {
  const instance = { enabled: true, session_id: "s1" };
  const now = Date.parse("2026-09-06T01:30:00Z");
  expect(deriveTargetState(instance, {
    targets: [], session_id: "", bar_end_time: "", valid_until: ""
  }, now)).toBe("empty");
  expect(deriveTargetState(instance, {
    targets: [], session_id: "s1",
    bar_end_time: "2026-09-06T01:00:00Z",
    valid_until: "2026-09-06T03:00:00Z"
  }, now)).toBe("zero");
});

it("never presents a disabled session target as active", () => {
  expect(deriveTargetState({ enabled: false, session_id: "s1" }, {
    targets: [{ instrument_id: "BTC-USDT-SPOT", target_weight: "0.6" }],
    session_id: "s1", bar_end_time: "2026-09-06T01:00:00Z",
    valid_until: "2026-09-06T03:00:00Z"
  }, Date.parse("2026-09-06T01:30:00Z"))).toBe("inactive");
});
```

- [ ] Store 对实例、目标、结果分别记录 loading/error，使用递增请求标识忽略晚到响应；轮询由详情生命周期启动/停止，不轮询定义编辑页。
- [ ] 通过断言后提交 `fix(web): separate strategy target and control states`。

### 任务 3：DSL 编辑与定义列表

**文件：** `dsl.ts` 及测试、overview、editor 及测试、路由、菜单、语言文件、package/lock。

- [ ] 以真实 DSL 编写模板解析测试，覆盖重复键、多文档、缺失 name 和损坏 YAML；读取下方模板，不恢复 api_version/kind。

```yaml
name: 收盘价排序示例
triggers:
  event: {name: source.ready}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  rank:
    pool: [BTC-USDT-SPOT, ETH-USDT-SPOT]
    filter_before: "close > 0"
    score: "close"
    select: {top: 1}
    weight: 0.60
```

- [ ] 增加时序模板：固定标的池、`factor.ready`、`bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close` 作为 entry，反向穿越作为 exit，`weight_each: 0.10`；标明模板依赖名为 ma20 的实际因子字段，不把它当内置均线函数。
- [ ] 编写编辑测试：新建调用 CreateStrategy、编辑调用 UpdateStrategy；保存失败/编辑限制保留草稿；模板切换和离开页面需要确认；保存不启用实例。
- [ ] 执行 `pnpm vitest run src/views/strategy/dsl.test.ts src/views/strategy/editor/index.test.ts src/views/strategy/overview/strategy-create-defaults.test.ts`，确认新断言失败。
- [ ] 使用显式 YAML 依赖实现摘要，完整页面编辑，替换旧新增弹窗；定义列表按服务端分页，点击名称进入编辑/查看页。
- [ ] 添加第 2.1 节路由和标题，实例菜单排第一。保留现有 overview/running URL 语义，不增加旧路由兼容页。
- [ ] 重跑测试和 `pnpm check:menu`；提交 `feat(web): add full-page strategy DSL editor`。

### 任务 4：基于真实元数据生成实例绑定

**文件：** `bindings.ts` 及测试、`strategy-instance-create.vue` 及测试；复用现有 Factor、Storage、Trade API。

- [ ] 编写映射测试：跨空间/错源 View/错周期被排除；多结果 View 被拒绝；列名从属性映射；第二页元数据可选择；元数据失败不自动填默认值。
- [ ] 编写抽屉测试：无因子可创建；选择交易模式必须选账户；账户选择不发送认领请求；最终请求为 enabled=false；超时后保留表单并查实例。
- [ ] 执行 `pnpm vitest run src/views/strategy/bindings.test.ts src/views/strategy/components/strategy-instance-create.test.ts`，确认失败。
- [ ] 实现列映射，以服务端规则为准，核心匹配断言如下：

```ts
const matched = columns.find(column =>
  column.attributes?.origin_factor_id === factor.factor_id &&
  column.attributes?.factor_output === output
);
// matched 不存在则阻止选定该输出，不以 output 代替 column_name。
```

- [ ] 构造对象后 JSON.stringify，字段来源固定：

| 绑定字段 | 来源 |
|---|---|
| source_view_id、frequency | 所选源 View、DSL 周期的规范化值 |
| factor_id、source_hash、input_columns、params_json、lookback_periods | FactorDef |
| binding_id、result_dataset_id、result_view_id、subject_mode、subjects_json | FactorBinding |
| output | 用户从 FactorDef.outputs 选择的值 |
| column_name | 结果 View 的匹配列 |
| 因子 frequency | FactorBinding.freq，规范化后与 DSL 对照 |

- [ ] 周期规范化保留 `m` 为分钟、`M` 为月的区别；按后台当前支持范围拒绝月/周周期，不做不加区分的 toLowerCase。
- [ ] 创建流程按第 2.3 节完成，绑定预览只读；测试通过后提交 `feat(web): create strategy instances from metadata selections`。

### 任务 5：运行实例列表与启停

**文件：** running 页面、operation-panel 及其测试、Store。

- [ ] 写失败测试：取消确认不请求；双击只提交一次；停用 RPC 错误但重读 enabled=false 时展示已停用；仍有 session 时显示控制未完成；停止轮询后不再请求。
- [ ] 执行 `pnpm vitest run src/views/strategy/components/strategy-operation-panel.test.ts src/store/modules/strategy.test.ts`。
- [ ] 实现列表列、服务端筛选、分页和创建入口；策略名称通过真实定义 ID 关联，不为每行轮询目标接口。
- [ ] 操作流程固定为“确认 → 禁止重复提交 → 调用启停 → 无论成功失败都重新查询 → 展示返回的实际状态”；重读失败显示结果未知，不能恢复原状态假装无事发生。
- [ ] 不提供清仓、立即运行、实例编辑、删除和 session 手动修改。无账户叫仅计算，不叫仿真盘；交易模式按账户关联展示，不承诺会成交。
- [ ] 通过测试后提交 `feat(web): rebuild strategy instance controls`。

### 任务 6：最新目标与历史结果

**文件：** detail 页面、target-table、result-table、status-badge、`web/tests/strategy-console.spec.ts`、`web/playwright.strategy.config.ts`。

- [ ] 新建仅匹配 strategy-console.spec.ts 的 mock Playwright 配置；baseURL 固定本地 19527，webServer 使用 `pnpm dev --host 127.0.0.1 --port 19527 --strictPort`，reuseExistingServer=false，独立测试输出目录。
- [ ] 端口被占用时停止测试并选择另一个空闲端口，同时更新该测试运行的配置；不得终止未知进程或误连已有服务器。
- [ ] mock Strategy、Storage、Factor、Trade 和会话接口，拦截未列入 fixture 的 `/api/admin/**` 请求并使测试失败，确保零真实控制副作用。
- [ ] 新增 mock E2E：最新目标接口和历史第一行不同，页面仍以最新目标接口为准；零仓位、过期、session 残留分别呈现。
- [ ] 新增历史分页断言：第二页请求携带 page；全部历史不带 session_id；本次启用使用当前 session；停止后不悄悄显示全部历史为当前结果。
- [ ] 新增结果详情断言：sent 显示已发送而非已成交；规则状态折叠只读；不展示不存在的 quantity、收益或历史 DSL 快照。
- [ ] 重写 detail 只读取现代实例。页面主区域展示最新目标，标签切换历史结果和输入配置，账户入口使用现有真实 Trade 路由。
- [ ] 将 timeline 组件改为结果表组件并删除旧引用；权重显示带方向的百分比，数据错误不按 0% 展示。
- [ ] 使用 `pnpm exec playwright test --config playwright.strategy.config.ts` 运行对应用例；先确认新断言在旧页面失败，再确认实现后通过，提交 `feat(web): align strategy targets and result history`。

### 任务 7：端到端与界面验收

**文件：** `web/playwright.strategy.config.ts`、`web/tests/strategy-console.spec.ts`、相关单测。

- [ ] 复用任务 6 建立的独立 mock 配置，检查未注册请求兜底拦截确实生效，没有访问实际控制接口。
- [ ] 替换旧 immutable/Runner fixture，保留文件中相邻 Trade 账户暂停与清仓结果测试；按需更新其独立 fixture，不删除其断言。
- [ ] 测试实例创建完整流程、DSL 保存拒绝、元数据第二页、启停部分成功、列表 EOF、迟到响应、目标状态和历史分页。
- [ ] 用 Playwright 检查 1440×900、1024×768、390×844 视口：列表、DSL 编辑、三步抽屉、详情、错误态。表格可横向滚动，页面本身不溢出；确认框按钮可见，长 ID 不遮挡操作。
- [ ] 检查键盘 Tab 焦点、抽屉/弹窗关闭后焦点返回、必填字段标签、图标提示、禁用态、刷新不跳动及无控制台错误。
- [ ] 从 `web` 目录运行，所有命令必须实际通过后才能标记验收通过：

```bash
pnpm vitest run src/api/strategy.test.ts src/store/modules/strategy.test.ts src/views/strategy
pnpm exec playwright test --config playwright.strategy.config.ts
pnpm test
pnpm build:prod
pnpm check:menu
pnpm check:detail-pages
```

- [ ] 从仓库根目录运行 `git diff --check`。如全量检查存在原有失败，记录原有/新增失败证据，不能把未通过记为通过。
- [ ] 提交 `test(web): cover strategy workbench workflows`。

### 任务 8：独立审查、文档与交付

**文件：** 本计划、`modules/strategy/docs/frontend-verification.md`；修复只触及审查确认的本任务问题。

- [ ] 请求 codeCR 子 Agent 审查契约一致性、控制操作部分成功、空间/会话隔离、元数据映射、轮询竞态及测试缺口，要求文件和符号依据。
- [ ] 等待审查完成，主 Agent 独立核验结论，修复确认问题并重跑相关测试；未解决问题如实列出，不宣称全部完成。
- [ ] 更新验收文档，分别记录源码 SHA、单测、mock E2E、桌面/移动截图、本地构建结果；未执行远端联调明确记为未执行。
- [ ] 执行 `git diff --stat` 和 `git status --short`，逐文件暂存本次修改，排除任务开始前已有的无关改动。
- [ ] 提交后执行 `git push`，确认远端分支 SHA 与本地提交一致；推送失败单独报告，不改写远端历史。
- [ ] 向用户提供本地预览 URL 与实施结果，并明确尚未发布到 `106.53.107.122:9527`。

## 5. 后续远端验收门槛（不自动执行）

本节不是本次文档生成或未来本地编码完成后的自动发布授权。用户另行批准发布后才执行。

1. 只读确认目标主机的实际服务角色、web-host 制品版本、Gateway 到 Strategy 路由及健康状态；先解决/单独报告 EOF，不能用空 fixture 掩盖真实故障。
2. 按执行时仓库发布流程构建前端、重新生成 web-host 嵌入静态资源，构建并替换目标 web-host。参考入口为 `make -C web-host statik` 和 `scripts/build/build.sh web-host`，执行前核对脚本当前参数；不假定 strategy_host 就是前端主机。
3. 不顺带重启 Strategy、Trade 或其他服务。配置与凭据不写入文档、截图、日志或提交。
4. 健康检查后，在用户给出的 HTTPS 页面确认实际静态资源版本、菜单、错误态、定义列表和实例详情；本地构建通过不等于线上通过。
5. 远端默认只读验收。任何创建/编辑策略、启停实例测试均需用户确认具体测试对象；优先仅计算实例，不使用真实交易账户进行冒烟操作。

## 6. 完成标准

- [ ] 页面只使用当前现代实例契约，旧 Runner 策略前端代码和测试夹具清理完成。
- [ ] DSL 可创建/编辑且保护草稿，后台拒绝原因可见；没有伪独立校验按钮。
- [ ] 实例可通过元数据选择创建，无需手写绑定 JSON；提交后默认停用。
- [ ] 启停部分成功能正确对账，停用不显示清仓成功。
- [ ] 无结果、零仓位、过期、会话不匹配、接口失败互不混淆。
- [ ] 目标权重、消息投递与交易成交明确分离；没有接口不支持的状态或数据面板。
- [ ] 分页、空间切换、迟到响应、移动布局和相邻 Trade 测试均有验证。
- [ ] 单测、mock E2E、构建、独立审查、文档与 Git 推送分别有证据；远端发布状态单独报告。

## 执行结果（2026-09-07）

本计划已按现代 StrategyInstance 契约执行，相关前端代码、单测、mock Playwright、生产构建和
web-host 制品已完成，并由独立 codeCR Agent 复审。实现提交为 `6aaefffb`、`e45679ee`，
已推送到 `feature/mooyang`；详细命令、远端制品 SHA 和已知环境问题见
`modules/strategy/docs/frontend-verification.md`。

正式环境静态前端已发布并验证登录、菜单、路由和错误态；远端 `ListStrategies` 当前仍因
Strategy 服务 `127.0.0.1:11430` 返回 EOF，策略数据读取未通过。该问题单独记录，不在本次
前端重构中修改后台业务或运行配置。
