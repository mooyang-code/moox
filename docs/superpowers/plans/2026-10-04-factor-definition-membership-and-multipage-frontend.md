# 因子定义与因子集解耦 + 因子计算前端多页面重构：执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把因子计算改成「定义与使用分离」：`FactorDef` 只描述算法，不再带 `set_id` 与启停状态；因子在计算任务（`FactorSet`）里的运行实例用新的成员关系 `FactorSetMember` 表达，启用 / 停用 / 回填都发生在成员上。前端把单页「因子工作台」拆成 3 个菜单页（因子总览 / 因子定义 / 计算任务，后者含 计算任务 · 计算结果 · 补算 三个标题 Tab），因子编辑改为独立整页，源码使用深色 Python 代码编辑器。

**Architecture:** 后端沿用已落地的单进程 `moox-factor` 周期流水线，只改定义 / 成员层：SQLite 新增 `t_factor_set_members`，`EnabledSetByDataset` 的签名保持不变（内部改为 join enabled 成员），因此 trigger / pipeline 的热路径几乎不动；校验拆成「定义静态校验」与「加入因子集时的数据集校验」；锁序固定为「定义锁 → 因子集锁」。下游（网关白名单、strategy、`moox` CLI、web）同一批发布，不保留兼容层。前端按对象拆页，复用采集模块的 Tab 宿主与结果浏览写法，新增通用 `code-editor` 组件（CodeMirror 6，按需加载）。

**Tech Stack:** Go 1.2x、tRPC-Go、SQLite/GORM、protobuf（`modules/factor/proto`）、Python worker；Vue 3 + TypeScript + Arco Design Vue + Pinia + vue-router(hash) + Vitest + Playwright + vue-tsc、CodeMirror 6。

**设计基线**（本计划中的「后端规格 §N」「前端设计 §N」均指下列文档）：

- 后端规格：[因子定义与因子集解耦设计](../specs/2026-10-04-factor-definition-set-membership-design.md)（决策 D16–D28）
- 前端设计：[因子计算前端多页面重设计](../specs/2026-10-04-factor-multi-page-frontend-design.md)（v3.2）
- 交互原型：<https://claude.ai/artifact/QZm1nnnEHcXAnVDNnMZxzZ>（7 个画板：总览 / 因子定义 / 新建因子 / 计算任务三个 Tab / 详情抽屉）
- 前序实现：[因子计算模块重构设计](../specs/2026-10-04-factor-dataset-period-pipeline-design.md) 与 [执行计划](./2026-10-04-factor-dataset-period-pipeline.md)；本计划**取代**该计划的 Task 23（Web 因子页面），并修订 Task 9 / 10 / 12 / 13 / 20 / 22 / 24 / 25 / 27 中与「因子属于单一因子集」相关的部分（见 Task 0 与 Task B4）。
- 取代：[工作台前端设计](../specs/2026-10-04-factor-workbench-frontend-design.md) 中的信息架构部分。

**状态：** 设计已于 2026-10-04 获用户确认（回复「ok」，含新增 CodeMirror 依赖）。**实施状态（2026-10-05）：阶段 A、B、C 代码已全部实现并提交；迁移按用户决策改为「删库重启」，A2 迁移与 D3 删除迁移步骤已省略；阶段 D 的正式环境发布与线上验证待执行（本机沙箱无法 SSH 直连部署主机）。前端 Playwright e2e 因无浏览器尚未运行。**

---

## 当前基线与执行约束

- **工作区基线未核对**：本计划编写时无法在本机执行 `git status`。执行第一步必须 `git status` / `git log -5` 确认基线；若存在与本计划无关的未提交改动，视为用户工作，不得覆盖、回滚或混入本计划的提交。`modules/factor` 的流水线重构代码在工作区里已存在（文件时间戳显示），不要重做。
- 新项目原则（`AGENTS.md`）：不兼容历史；protobuf 里的 `reserved` 直接删除；清理死代码；文档用简体中文；SQL schema 遵守 `AGENTS.md` 的排版规范（关键字大写、4 空格缩进、表级约束在后、触发器分行）；**每次会话结束把所有改动提交并 `git push`**。
- 每个后端 Task 结束要满足：该 Task 涉及的包 `go build` / `go vet` / `go test` 通过；改 schema 时逐个载入空 SQLite；`git diff --check` 无告警；`bash scripts/check/check-gofmt.sh` 通过。**阶段 A 的中间 Task 允许同模块内其他包暂时编译失败**（与前序计划 Task 9 的做法一致，用 `go test ./internal/<pkg>/` 单包验证），阶段 A 的收口 Task A8 要求整个 `modules/factor` 全绿。
- 每个前端 Task 结束要满足：相关 vitest 通过、`pnpm exec vue-tsc --noEmit` 无错误、`pnpm lint:eslint:check`（`--max-warnings 0`）与 `pnpm lint:prettier:check`（printWidth 130）通过；仅用 `pnpm`（`preinstall` 有 `only-allow pnpm`）。
- 提交信息用 Conventional Commit（`feat(factor): …`、`refactor(web): …`、`docs: …`），每个 Task 单独提交，只 `git add` 本 Task 涉及的文件。
- **同批发布**（后端规格 D28）：`factor`、`strategy`、`moox` CLI、网关种子配置、`web` 必须一起发布，不做滚动兼容；`FactorSetInfo.members` 直接占用 `factors` 原来的字段号 2。
- **阶段依赖**：阶段 A（后端）→ 阶段 B（下游与运维）→ 阶段 D（联调与部署）；阶段 C（前端）中 C1（代码编辑器）、C3（菜单 / 路由壳）、C4 的 store 骨架与后端无关，可与阶段 A 并行；C2（API 层）按后端规格的 proto 手写类型即可并行，但 C5–C10 的联调要等阶段 A 完成。
- **全局契约测试会卡新代码**（前端）：`tests/create-button-style-contract.test.ts`（创建按钮必须是静态 `type="primary" status="success"`）、`tests/frontend-network-contract.test.ts`（禁止出现 `11000`）、`tests/space-scoped-request-contract.test.ts`；每个页面 Task 完成后跑一遍 `pnpm test:unit`。

## 已确认的实现取舍（相对设计文档）

以下是勘察代码后落定、但设计文档里只写了方向的取舍，执行时按此办理：

1. **ViewBrowse 已有 `status-extra` 插槽**（`web/src/views/data/view-browse/index.vue`），计算结果 Tab 直接用，不改 `ViewBrowse`。
2. **`meta.activeMenu` 要直接读路由**：`Menu/index.vue`、`layout-head/index.vue`、`layout-mixing/index.vue` 三处的选中逻辑一起改，`src/typings/global.d.ts` 的 `MetaType` 加 `activeMenu`。
3. **`use-factor-scope` 不能每次挂载都 reset**：主区域是 `keep-alive` + `:key="route.fullPath"`，query 变化会重挂载；只在空间 id 变化时 reset + load。
4. **`.page-head` / `.result-toolbar` 是各页 scoped 样式**，不是全局类；新页面自备，重复部分抽到 `factor/shared/factor-page.scss`。
5. **列表类响应默认不带 `source_code`**（D24），`ListFactorsReq.include_source` 显式开启，`GetFactor` 始终带；strategy 热路径与前端 10 秒轮询都受益。
6. **后端迁移**：保留后端规格的一次性迁移（`store.ApplySchema` 开头、校验之前），并在 Task D3 删除。理由：现有库里已有 enabled 因子，直接重建库会让每个成员重新触发全窗口回填。若确认部署环境没有需要保留的数据，可跳过 Task A2 的迁移步骤与 D3 的删除步骤，改为「删库重启 + `moox` CLI 重新导入」。
7. **不改策略编辑器的 DSL 输入**：`code-editor` 先只用于因子源码（编辑 + 只读），策略 DSL 切换留作后续独立改动。

## 文件责任图

### 后端 `modules/factor`（阶段 A）

- 修改 `schema/factor.sql`、`schema/schema_test.go`：去掉 `t_factor_defs.c_set_id / c_status` 与相关索引、外键；新增 `t_factor_set_members` 与 mtime 触发器。
- 修改 `internal/domain/{types.go,validate.go,validation.go}` 及测试：`FactorDef` 去字段；新增 `FactorSetMember`、`SetMember`、`FactorUsage`、`FactorInfo`；`ValidateFactor` 拆为 `ValidateDefinition` + `ValidateMembership`。
- 修改 `internal/store/{database.go,defs.go,sets.go,recalc_jobs.go}`，新增 `members.go`、`migrate.go` 及测试。
- 修改 `proto/factor.proto`，重新生成 `proto/factorgen/`。
- 修改 `internal/catalog/{service.go,reconcile.go,locks.go}` 及测试。
- 修改 `internal/pipeline/{plan.go,compute.go}`、`internal/recalc/{service.go,worker.go}`、`internal/trigger/**` 的测试，`internal/rpc/{service.go,convert.go}`，`internal/bootstrap/bootstrap.go`，`cmd/cli/{commands.go,main.go}`，`README.md`。

### 下游与运维（阶段 B）

- 网关种子：`config/setup/service-deployments.yaml`、`modules/admin/internal/service/sysdeploy/{defaults.go,routes.go}`、`modules/admin/cmd/cli/service_deployments_test.go` 及各自 `*_test.go`、`period_gateway_contract_test.go`。
- strategy：`modules/strategy/internal/factorio/{client.go,client_test.go}`；`internal/compiler/verify_dependencies.go` 与 `modules/strategy/docs/coin-selection-runtime.md` 仅注释 / 措辞。
- `moox` CLI：`modules/cli/internal/command/{setup_factors.go,setup_init.go,setup.go}`、`modules/cli/internal/setup/config/config.go` 及测试、`moox.toml.example`、本机 `moox.toml`（已被 gitignore，手工迁移）。
- 文档与技能：见 Task B4。

### 前端 `web/`（阶段 C）

- 新增 `src/components/code-editor/{index.vue,theme.ts,index.test.ts}`；`package.json`、`pnpm-lock.yaml`。
- 重写 `src/api/factor/{types.ts,index.ts,index.test.ts}`、`src/store/modules/factor.ts`（及测试）。
- 新增 `src/views/factor/{overview,definitions,editor,task-management,compute-tasks,results,recalc,shared}/**`；迁移 `health.ts`、`status.ts`、`factor-form.ts`、`set-create-modal.vue`、`subject-scope-fields.vue`。
- 修改 `src/router/route.ts`、`src/api/modules/system/static-menu.ts`、`src/lang/modules/{zhCN,enUS}.ts`、`src/layout/components/Menu/index.vue`、`src/layout/layout-head/index.vue`、`src/layout/layout-mixing/index.vue`、`src/typings/global.d.ts`、`src/views/home/home.vue`、`src/views/strategy/components/strategy-instance-create.vue`、`src/views/strategy/bindings.ts`（及 `bindings.test.ts`）、`vitest.config.ts`、`scripts/check-menu-structure.mjs`、`scripts/check-detail-page-style.mjs`。
- 删除 `src/views/factor/workbench/`（含 `components/code-block` 若无其他引用）。
- 测试：`src/views/factor/__tests__/factor-contract.spec.ts`、`tests/page-layout-standard-contract.test.ts`、`tests/factor-dataset-workflow.spec.ts` 及各新页面的 model 测试。

## 接口约定

以下签名在多个 Task 中引用，实现时保持一致；字段可增，名称与语义不得改变。

```go
// modules/factor/internal/domain
const (
	MemberStatusEnabled  = "enabled"  // 取代 FactorStatusEnabled
	MemberStatusDisabled = "disabled" // 取代 FactorStatusDisabled
)

type FactorDef struct { // 去掉 SetID、Status，其余字段不变
	FactorID, Name, FactorType, SourceCode, SourceHash string
	InputColumns, Outputs                               []string
	ParamsJSON                                          string
	LookbackPeriods                                     int
	AllowPartialUniverse                                bool
	CreatedAt, UpdatedAt                                time.Time
}

type FactorSetMember struct { // 表 t_factor_set_members
	SetID, FactorID, Status string
	CreatedAt, UpdatedAt    time.Time
}

type SetMember struct { // 因子集视角：成员 + 定义
	FactorSetMember
	Factor FactorDef
}

type FactorUsage struct{ SetID, Status string }
type FactorInfo struct { // 定义视角：定义 + 被谁使用
	Factor FactorDef
	Usages []FactorUsage
}

// 校验（后端规格 §6）
func ValidateDefinition(def FactorDef) error // §6.1：静态，不依赖数据集
func ValidateMembership(set FactorSet, def FactorDef, sourceColumns []string,
	siblings []FactorDef, resultColumnOrigins map[string]string) error // §6.2 / §6.3 / D25
```

```go
// modules/factor/internal/store
func (s *Store) AddMember(ctx context.Context, setID, factorID string) (domain.FactorSetMember, error)
func (s *Store) RemoveMember(ctx context.Context, setID, factorID string) error // 仅 disabled，否则 ErrConflict
func (s *Store) SetMemberStatus(ctx context.Context, setID, factorID, expectedStatus, status string) error
func (s *Store) GetMember(ctx context.Context, setID, factorID string) (domain.FactorSetMember, error)
func (s *Store) ListMembers(ctx context.Context, setID, status string) ([]domain.SetMember, error) // join 定义，按 factor_id 排序
func (s *Store) ListUsages(ctx context.Context, factorIDs ...string) (map[string][]domain.FactorUsage, error)
func (s *Store) CountMembers(ctx context.Context, setID string) (int, error)
func (s *Store) ListFactors(ctx context.Context) ([]domain.FactorDef, error) // 全局列表，不再按 set / status 过滤
func (s *Store) EnableMemberWithRecalcJob(ctx context.Context, setID, factorID, expectedStatus string, job RecalcJob) (RecalcJob, error)
// EnabledSetByDataset 签名不变，内部改为 join enabled 成员
```

```go
// modules/factor/internal/catalog：CatalogAPI 中与定义 / 成员相关的方法
CreateFactor(ctx, in domain.FactorDef) (domain.FactorDef, error)                 // 静态校验 + 源码试 LOAD
UpdateFactor(ctx, in domain.FactorDef) (domain.FactorDef, error)                 // D20
DeleteFactor(ctx, factorID string) error                                         // D21
GetFactor(ctx, factorID string) (domain.FactorInfo, error)
ListFactors(ctx, setID, status string) ([]domain.FactorInfo, error)              // setID 可选
AddFactorToSet(ctx, setID, factorID string) (domain.SetMember, error)            // §6.2，写 disabled 成员，不加列
RemoveFactorFromSet(ctx, setID, factorID string) error
SetFactorMemberStatus(ctx, setID, factorID, status string) (domain.SetMember, string, error) // 返回 backfill job id（启用时）
GetSet(ctx, setID string) (domain.FactorSet, []domain.SetMember, error)
```

```go
// modules/factor/internal/catalog/locks.go
func (l *Locks) LockFactorContext(ctx context.Context, factorID string) (func(), error) // 键 "factor:"+id，与 fset_* 键空间隔离
// 锁序：先 LockFactorContext，再 LockContext(setID)；跨多个因子集时按 set_id 升序
```

```ts
// web/src/api/factor/types.ts
export type MemberStatus = "enabled" | "disabled";
export interface FactorDef {
  factor_id: string; name: string; factor_type: "timeseries" | "cross_section";
  source_code?: string; // 列表类响应默认缺省（D24）
  source_hash: string; input_columns: string[]; outputs: string[]; params_json: string;
  lookback_periods: number; allow_partial_universe: boolean; created_at: string; updated_at: string;
}
export interface FactorUsage { set_id: string; status: MemberStatus }
export interface FactorInfo { factor: FactorDef; usages: FactorUsage[] }
export interface FactorMember { set_id: string; factor_id: string; status: MemberStatus; factor: FactorDef; created_at: string; updated_at: string }
export interface FactorSetInfo { factor_set: FactorSet; members: FactorMember[]; last_run?: SetRunSummary }
// web/src/api/factor/index.ts 新增：addFactorToSet / removeFactorFromSet / setFactorMemberStatus；删除 setFactorStatus
// listFactors(req?: { set_id?: string; status?: MemberStatus; include_source?: boolean }): Promise<{ factors: FactorInfo[] }>
// getFactor(factor_id): Promise<{ factor: FactorDef; usages: FactorUsage[] }>
```

---

## 阶段 0：文档（本会话已完成）

### Task 0：设计与计划文档定稿

**Files:**
- Create/Modify: `docs/superpowers/specs/2026-10-04-factor-definition-set-membership-design.md`（补 D24–D28、下游调用方、迁移细节）
- Modify: `docs/superpowers/specs/2026-10-04-factor-multi-page-frontend-design.md`（v3.2）
- Create: `docs/superpowers/plans/2026-10-04-factor-definition-membership-and-multipage-frontend.md`（本文）
- Modify: `docs/superpowers/plans/2026-10-04-factor-dataset-period-pipeline.md`（顶部加「已被取代 / 修订」说明，Task 23 标注被取代）

- [x] **Step 1：** 后端规格补充 D24–D28、§9 下游调用方清单、§10 迁移与风险。
- [x] **Step 2：** 前端设计升到 v3.2（`status-extra` 已有、`activeMenu` 改三处、`use-factor-scope` 语义、scoped 样式、溢出点）。
- [x] **Step 3：** 写本执行计划。
- [x] **Step 4：** 在旧流水线计划顶部加修订说明（Task 9 / 10 / 12 / 13 / 20 / 22 / 24 / 25 / 27 按说明视为已被修订），并把 Task 23 标注为「已被本计划取代」；Task B4 再逐个 Task 做细节标注与其余文档同步。
- [ ] **Step 5：** 提交 `docs: plan factor definition membership split and multi-page frontend`（连同两份设计文档的更新）。

---

## 阶段 A：后端（`modules/factor`）

### Task A1：domain 实体与校验拆分

**Files:**
- Modify: `modules/factor/internal/domain/{types.go,validate.go,validation.go}`
- Modify: `modules/factor/internal/domain/{factor_set_test.go,validation_test.go}`

- [ ] **Step 1：写失败测试**（表驱动，`domain` 包）：
  - `TestValidateDefinitionRejectsReservedOutput`（`series_tag`、`data_time`、`subject_id`、`freq`）；
  - `TestValidateDefinitionRejectsDuplicateInputsAndOutputs`、`TestValidateDefinitionRequiresNonEmptyInputsAndOutputs`；
  - `TestValidateDefinitionParamsMustBeJSONObject`、`TestValidateDefinitionLookbackAtLeastOne`；
  - `TestValidateDefinitionPartialUniverseOnlyForCrossSection`；
  - `TestValidateDefinitionDoesNotNeedSourceColumns`（传入任意输入列名也通过）；
  - `TestValidateMembershipRejectsUnknownInput`（输入列不在源数据集）；
  - `TestValidateMembershipRejectsOutputCollidingWithSourceColumn`、`TestValidateMembershipRejectsDuplicateOutputInSetExcludingSelf`；
  - `TestValidateMembershipRejectsOutputOwnedByOtherFactor`（`resultColumnOrigins["bias_5"]="Other"`）与 `TestValidateMembershipAllowsReaddOfSameFactor`（origin 相同则放行，D25）；
  - `TestNormalizeFactorDefinitionIgnoresStatusAndSet`（`FactorDef` 不再有这两个字段，规范化只处理名称、列、参数、回看）。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/domain/ -count=1`。
- [ ] **Step 3：实现。** `types.go`：删除 `FactorDef.SetID / Status` 与 `FactorStatus*` 常量，新增 `MemberStatus*` 与接口约定里的类型（`FactorSetMember` 带 `TableName() "t_factor_set_members"`）。`validate.go`：把现有 `ValidateFactor` 拆成两个函数，**静态规则**（名称格式、类型、回看、输入输出非空且无重复、保留列、参数 JSON object、仅截面允许 partial）进 `ValidateDefinition`；**依赖数据集 / 因子集的规则**（输入列 ⊆ 源列、输出与源列 / 保留列 / 同集成员 / 结果数据集既有列归属冲突、`AllowPartialUniverse` 与所属因子集 `subject_mode` 的关系——先核对现有 `ValidateFactor` 里该条件的写法再分配）进 `ValidateMembership`。`validation.go`：`NormalizeFactorDefinition` 去掉 Status 处理。
- [ ] **Step 4：验证并提交**：`cd modules/factor && go test ./internal/domain/ -count=1 && go vet ./internal/domain/`。提交 `refactor(factor): split definition validation from set membership validation`。

### Task A2：schema、store 与一次性迁移

**Files:**
- Modify: `modules/factor/schema/factor.sql`、`modules/factor/schema/schema_test.go`
- Modify: `modules/factor/internal/store/{database.go,defs.go,sets.go,recalc_jobs.go}`
- Create: `modules/factor/internal/store/{members.go,migrate.go,members_test.go,migrate_test.go}`
- Modify: `modules/factor/internal/store/{sets_test.go,recalc_jobs_test.go,database_test.go}`

- [ ] **Step 1：schema。** 按后端规格 §5 重写 `t_factor_defs`（去掉 `c_set_id`、`c_status`、对应索引与外键）并新增 `t_factor_set_members`（`PRIMARY KEY (c_set_id, c_factor_id)`，`FOREIGN KEY … REFERENCES t_factor_sets (c_set_id)`，`FOREIGN KEY … REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT`，索引 `idx_t_factor_set_members_set (c_set_id, c_status)`、`idx_t_factor_set_members_factor (c_factor_id)`），**为 `t_factor_set_members` 补 mtime 触发器**，并核对现有 `t_factor_defs` 触发器（约 76–82 行）没有引用被删列。排版严格遵守 `AGENTS.md` 的 schema 规范。校验：

```bash
sqlite3 :memory: < modules/factor/schema/factor.sql && echo OK
git diff --check
```

- [ ] **Step 2：写失败测试**（`store/*_test.go`，临时文件 SQLite）：
  - `TestMemberUniqueAndForeignKeys`：同一 `(set, factor)` 第二次 `AddMember` 返回 `ErrConflict`；定义或因子集不存在返回 `ErrNotFound`；
  - `TestSameDefinitionInTwoSetsHasIndependentStatus`：同一定义加入两个因子集，启用其一，另一个保持 disabled；
  - `TestRemoveMemberRequiresDisabled`、`TestDeleteSetFailsWhenMembersExist`、`TestDeleteFactorRejectedWhileReferenced`（`ON DELETE RESTRICT` 映射为 `ErrConflict`）；
  - `TestUpdateFactorRejectedWhileEnabledMemberExists`、`TestUpdateFactorAllowedWhenOnlyDisabledMembers`（事务内 `NOT EXISTS`，D20/D26）；
  - `TestListUsagesAggregatesAcrossSets`、`TestListMembersJoinsDefinitionsSortedByFactorID`；
  - `TestEnabledSetByDatasetReturnsOnlyEnabledMembers`（由原 `TestEnabledSetByDatasetReturnsOnlyEnabledFactors` 改写）；
  - `TestEnableMemberWithRecalcJobIsAtomicAndIdempotent`（由 `TestEnableFactorWithRecalcJobIsAtomicAndIdempotent` 改写：回算 job 入库与成员状态翻转同事务，重复启用幂等，`request_id` 冲突返回 `ErrConflict`）；
  - 改写 `TestSetCRUDAndFactorCRUD`、`TestLifecycleStatusWritesUseExpectedStatus`、`TestUpdateRecalcProgressAndCancel`、`TestListRecalcJobsFiltersSetAndStatuses` 中对 `SetID` / `Status` 的使用；
  - `TestApplySchemaRejectsObsoleteDefsColumns`（旧 `t_factor_defs` 带 `c_set_id` 且迁移被关闭时仍报「create a fresh database」）。
- [ ] **Step 3：运行确认失败**：`cd modules/factor && go test ./internal/store/ -count=1`。
- [ ] **Step 4：实现 store。**
  - `database.go`：`validateSchemaTables` 的 `expected` 去掉 `c_set_id`、`c_status`，新增 `t_factor_set_members` 的列清单；表数量判断由 3 改为 4。
  - `members.go`：接口约定里的成员方法；`AddMember` 默认 `disabled`；`RemoveMember` 用 `DELETE … WHERE c_status='disabled'`，`RowsAffected==0` 时区分不存在与状态不符；`SetMemberStatus` CAS。
  - `defs.go`：`insertFactor` 去掉 set / status；删除 `SetFactorStatus`；`UpdateFactor` 的 SQL 把原来的 `AND c_status='disabled'` 换成 `AND NOT EXISTS (SELECT 1 FROM t_factor_set_members WHERE c_factor_id = ? AND c_status = 'enabled')`；`DeleteFactor` 依赖外键并映射错误；`ListFactors(ctx)` 变成全局列表，原 `ListFactors(setID, status)` 的用法改走 `ListMembers`。
  - `sets.go`：`EnabledSetByDataset` 内部改为 `ListMembers(setID, enabled)` 后取 `.Factor`；`DeleteSet` 把外键失败映射为 `ErrConflict`。
  - `recalc_jobs.go`：`EnableFactorWithRecalcJob` → `EnableMemberWithRecalcJob(ctx, setID, factorID, expectedStatus, job)`，状态翻转改为 `UPDATE t_factor_set_members … WHERE c_set_id=? AND c_factor_id=? AND c_status=?`，其余原子性与幂等语义保持不变。
- [ ] **Step 5：写迁移的失败测试**（`migrate_test.go`，用测试内嵌的**旧版 DDL** 建库并写入数据）：
  - `TestMigrateLegacyDefsMovesSetIDAndStatusIntoMembers`：2 个因子集、3 个因子（含 enabled / disabled）迁移后，成员数 = 3，状态一一对应，定义行内容不变（`source_hash`、`outputs` 等）；
  - `TestMigrateLegacyDefsRebuildsIndexesAndTriggers`（新表的 mtime 触发器可用、旧索引不存在）；
  - `TestMigrateIsNoopOnNewSchema`、`TestMigrateIsAtomicOnFailure`（注入失败后旧表保持原样、可重试）；
  - `TestMigratePreservesRecalcJobs`（`t_factor_recalc_jobs` 不变）；
  - `TestMigrateCheckForeignKeys`（迁移后 `PRAGMA foreign_key_check` 为空）。
- [ ] **Step 6：实现迁移**（`migrate.go`，在 `ApplySchema` 开头、校验之前调用）：用 `pragma_table_info('t_factor_defs')` 判断是否含 `c_set_id`；是则在**一个事务**内：建 `t_factor_set_members`（若不存在）→ `INSERT … SELECT c_set_id, c_factor_id, c_status FROM t_factor_defs` → 以「建新表 → 拷贝 → 删旧 → 改名」重建 `t_factor_defs`（SQLite 里 `PRAGMA foreign_keys` 须在事务外切换，实现时先关闭、事务后恢复并跑 `PRAGMA foreign_key_check`）→ 重建索引与触发器。迁移函数文件头注释写明「一次性迁移，部署完成后由 Task D3 删除」。
- [ ] **Step 7：验证并提交**：`cd modules/factor && go test ./internal/store/ ./schema/ -count=1`，`sqlite3 :memory: < schema/factor.sql`。提交 `refactor(factor): store factor set membership and migrate legacy definitions`。

### Task A3：factor.proto 与生成代码

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Regenerate: `modules/factor/proto/factorgen/{factor.pb.go,factor.trpc.go,validation.go}`

- [ ] **Step 1：编辑 proto**（后端规格 §7.2 + 补充）：
  - `FactorDef`：删除 `set_id`、`status` 两个字段（不留 `reserved`）；
  - 新增 `FactorUsage{set_id,status}`、`FactorInfo{factor,usages}`、`FactorMember{set_id,factor_id,status,factor,created_at,updated_at}`；
  - `FactorSetInfo.members`（`repeated FactorMember`，占用原 `factors` 的字段号 2）；`GetFactorSetRsp` 同样改为 `repeated FactorMember members`；
  - `ListFactorsReq`：`set_id` 变可选（空 = 全部）、保留 `status`（按成员状态，仅指定 `set_id` 时有意义）、新增 `bool include_source`；`ListFactorsRsp.factors` 改为 `repeated FactorInfo`；
  - `GetFactorRsp`：保留 `FactorDef factor`，新增 `repeated FactorUsage usages`；
  - `CreateFactorReq`：随 `FactorDef` 去掉 `set_id` / `status`；
  - 新增 `AddFactorToSet`、`RemoveFactorFromSet`、`SetFactorMemberStatus` 的请求 / 响应（`SetFactorMemberStatusRsp` 带 `FactorMember member` 与 `RecalcJob backfill_job`）；删除 `SetFactorStatusReq/Rsp` 与 service 里的 `SetFactorStatus`。
- [ ] **Step 2：生成并检查**：`make -C modules/factor/proto`，再 `make proto-check`。预期 `modules/factor` 其他包此时编译失败（`SetId`、`Status`、`Factors` 等引用），由 A4–A7 逐包修复。
- [ ] **Step 3：提交** `refactor(factor): reshape FactorMgr proto around definitions and set members`。

### Task A4：catalog（定义级 + 成员级生命周期）

**Files:**
- Modify: `modules/factor/internal/catalog/{service.go,reconcile.go,locks.go}`
- Modify: `modules/factor/internal/catalog/{service_test.go,locks_test.go}`

- [ ] **Step 1：写失败测试**（fake Metadata + 真实 SQLite store）：
  - 定义级：`TestCreateFactorDoesNotNeedSet`、`TestCreateFactorRunsStaticValidationAndLoadsSource`（`SourceChecker` 失败即拒绝）、`TestUpdateFactorRejectedWhenAnyEnabledMember`、`TestUpdateFactorRevalidatesAllReferencingSetsAndNamesTheFailingSet`、`TestDeleteFactorRejectedWhileReferenced`；
  - 成员级：`TestAddFactorToSetValidatesAgainstSourceColumns`、`TestAddFactorToSetRejectsPendingOrDeletingSet`、`TestAddFactorToSetRejectsOutputOwnedByOtherFactor`、`TestAddFactorToSetDoesNotAddColumnsOrSubmitRecalc`、`TestReaddAfterRemoveReusesRetainedColumns`；
  - 启停：`TestEnableMemberRevalidatesThenAddsColumnsThenSubmitsRecalc`（顺序为 `UpsertColumns` → 成员 enabled → `RecalcSubmitter.Submit(set, [factor], …)`，D27）、`TestDisableMemberKeepsColumns`、`TestRemoveRequiresDisabledMember`；
  - 多因子集：`TestSameDefinitionEnablesIndependentlyInTwoSets`；
  - 因子集：`TestDeleteSetRejectedWhileMembersExist`；
  - 锁与并发：`TestFactorLockPrecedesSetLock`（持有 `LockFactorContext(f)` 时 Add / Update / SetMemberStatus 阻塞）、`TestAddAndUpdateDoNotInterleave`（Add 在事务内重读定义）、`TestFactorLockKeysDoNotCollideWithSetKeys`（`factor:` 前缀）；
  - 通知与恢复：`TestMemberStatusChangesNotifyTrigger`、`TestReconcileUsesEnabledMembers`、`TestReconcileRejectsSourceColumnCollisionWithEnabledMember`、`TestReconcileContinuesAfterOneSetFailure`、`TestReconcileResumesDurableResultDatasetPurge`（保持）。
  - 改写：原 `TestEnableFactor*`、`TestDisableFactorKeepsColumns`、`TestDeleteFactorRequiresDisabled`、`TestUpdateFactorRequiresDisabled`、`TestFactorStatusChangesNotifyTrigger`、`TestDeletingSetRejectsLifecycleMutation`、`TestArtifactsMaterializeImmutableSource`、`TestLifecycleOpsSerializeWithPeriodLock`。
- [ ] **Step 2：运行确认失败**：`cd modules/factor && go test ./internal/catalog/ -count=1`。
- [ ] **Step 3：实现。**
  - `locks.go`：新增 `LockFactorContext`，文件锁键用 `factor:` 前缀并做文件名安全转换，与 `fset_*` 隔离；在文件头注释写明锁序（定义锁 → 因子集锁，多个因子集按 `set_id` 升序）。
  - `service.go`：`CreateFactor` 只做 `ValidateDefinition` + `loadSource`；`UpdateFactor` 取定义锁，若存在 enabled 成员直接拒绝，否则对**所有引用它的因子集**调用 `ValidateMembership`（需要各因子集的源列与同集成员），任一失败整体拒绝并在错误里带 `set_id` 与原因；`DeleteFactor` 要求无成员；新增 `AddFactorToSet`（取定义锁 → 因子集锁 → 事务内重读定义 → `ValidateMembership` → `store.AddMember`，不加列、不补算）、`RemoveFactorFromSet`、`SetFactorMemberStatus`（启用 = 重新校验 → `ensureResultColumns` → `EnableMemberWithRecalcJob` → 通知 trigger；停用 = CAS + 通知，保留列）；`SetFactorStatus` 删除；`GetSet` 返回成员；`GetFactor` / `ListFactors` 返回 `FactorInfo`（`usages` 来自 `ListUsages`）；`DeleteSet` 前置「无成员」。
  - `reconcile.go`：列对齐与启动恢复改为遍历因子集的 enabled 成员定义；`ensureResultColumns(ctx, set, source, sourceColumns, factors ...FactorDef)` 的入参保持，调用点改为成员定义；核对 `compatibleResultColumn` 对「同 origin 重复列」的幂等行为（D25 / 后端规格 §10.4）。
  - `artifacts.go` 不动（按 `source_hash` 内容寻址，与因子集无关）。
- [ ] **Step 3b：** 同步 `CatalogAPI` 接口（`rpc` 包里的接口定义）与测试 fake，保证 `rpc` 包后续可编译。
- [ ] **Step 4：验证并提交**：`cd modules/factor && go test ./internal/catalog/ -count=1`。提交 `feat(factor): manage definitions and set members with definition-first locking`。

### Task A5：recalc、pipeline、trigger 适配

**Files:**
- Modify: `modules/factor/internal/pipeline/{plan.go,compute.go}`（`plan.go:45`、`compute.go:104` 对 `Status` 的过滤）
- Modify: `modules/factor/internal/recalc/{service.go,worker.go}`
- Modify tests: `pipeline/{plan_test,period_e2e_test,output_test,load_test,compute_test,recalc_compute_test}.go`、`recalc/service_test.go`、`trigger/eventconsumer/handler_test.go`、`trigger/lanes_test.go`

- [ ] **Step 1：写失败测试：**
  - `TestSameDefinitionInTwoSetsComputesIntoEachResultDataset`（`period_e2e_test.go`：同一定义分别加入 1m 与 1h 两个因子集，各自写入自己的结果数据集，`CommitID` 不串）；
  - `TestRecalcRejectsFactorThatIsNotAMember`、`TestRecalcRejectsDisabledMember`、`TestRecalcDefaultsToAllEnabledMembers`；
  - 改写：构造 `FactorDef{Status: …}` 的所有用例（`plan_test`、`output_test`、`load_test`、`compute_test`）改为通过成员表达启用；`TestEnableBackfillAcceptsDisabledFactorWithAtomicVisibility`、`TestEnableBackfillWithEmptyDatasetCompletesAsNoop`、`TestRunningJobsResumeAfterRestart`、`TestRecalcDoesNotReportMarker`；`TestFilterSubjectsFollowEnabledSets`。
- [ ] **Step 2：运行确认失败**：`go test ./internal/pipeline/ ./internal/recalc/ ./internal/trigger/... -count=1`。
- [ ] **Step 3：实现。** `pipeline/plan.go`、`compute.go`：删除对 `FactorDef.Status` 的过滤（`EnabledSetByDataset` 已只返回 enabled 成员的定义），`factorCall` 与 `write.go` 的 `CommitID`（用 `SetID`）不变；`recalc/service.go`：`factor_ids` 校验改为「必须是该因子集的 enabled 成员」（`ListMembers(setID, enabled)`），`PrepareEnableBackfill` 入参改为成员；`worker.go` 的 `runChunk` 逻辑不变；`trigger/locator.go`、`eventconsumer/handler.go`、`lanes.go` 只需确认编译与测试通过。
- [ ] **Step 4：验证并提交**：`cd modules/factor && go test ./internal/pipeline/ ./internal/recalc/ ./internal/trigger/... -count=1`。提交 `refactor(factor): drive pipeline and recalc from enabled set members`。

### Task A6：rpc 层

**Files:**
- Modify: `modules/factor/internal/rpc/{service.go,convert.go,service_test.go,convert_test.go}`

- [ ] **Step 1：写失败测试：**
  - `TestCreateFactorWithoutSetSucceeds`（原 `TestCreateFactorRequiresSetID` 语义反转）、`TestUpdateFactorMapsEnabledMemberConflict`、`TestDeleteFactorMapsReferencedConflict`；
  - `TestListFactorsWithoutSetReturnsAllWithUsages`、`TestListFactorsWithSetFiltersByMemberStatus`、`TestListFactorsOmitsSourceByDefault`、`TestListFactorsIncludeSourceWhenRequested`、`TestGetFactorAlwaysIncludesSourceAndUsages`、`TestListFactorSetsMembersOmitSource`（D24）；
  - `TestAddFactorToSetMapsPendingSetToFailedPrecondition`（沿用现有错误码映射，核对 `rpc/service.go` 已有约定）、`TestRemoveFactorFromSetRequiresDisabled`；
  - `TestSetFactorMemberStatusReturnsBackfillJobOnEnable`（由 `TestSetFactorStatusReturnsBackfillJobOnEnable` 改写）；
  - `TestFactorMgrExposesOnlyNewContract`（服务描述里没有 `SetFactorStatus`，有三个新方法）；
  - `TestConvertFactorDefRoundTrip`（不再含 `set_id` / `status`，`allow_partial_universe` 保留）、`TestConvertMemberRoundTrip`。
- [ ] **Step 2：运行确认失败**，然后实现 handlers 与转换：成员转换函数 `memberToPB`，列表类转换有 `includeSource bool` 参数，默认 false 时清空 `source_code`（保留 `source_hash`）。
- [ ] **Step 3：验证并提交**：`cd modules/factor && go test ./internal/rpc/ -count=1`。提交 `feat(factor): expose set member RPCs and omit source in list responses`。

### Task A7：bootstrap 与 factor CLI

**Files:**
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`（`reconcileAtStartup` 约 251–270 行遍历因子集再遍历成员；装配处约 125–132、168 行确认接口适配）
- Modify: `modules/factor/cmd/cli/{commands.go,main.go,main_test.go}`、`modules/factor/README.md`

- [ ] **Step 1：写失败测试**（`cmd/cli/main_test.go`）：`TestParseImportDefinitionsAndMembers`（导入格式支持「定义 + 因子集 + 成员」三段，原 `TestParseImport*` 改写）、`TestImportCatalogCreatesDefinitionsOnly`（`factors/catalog.json` 无 set 字段，只建定义）、`TestInitCreatesSetThenDefinitionsThenMembers`。
- [ ] **Step 2：实现。** `import` / `import-catalog` / `init`：先建定义，再对指定因子集 `AddFactorToSet`，目标为 enabled 的再 `SetFactorMemberStatus`；`README.md` 的 24–50 行用法同步。
- [ ] **Step 3：验证并提交**：`cd modules/factor && go test ./internal/bootstrap/ ./cmd/... -count=1`。提交 `refactor(factor): bootstrap and CLI work with definitions and members`。

### Task A8：阶段 A 收口

**Files:** 无新增；只验证。

- [ ] **Step 1：** `cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1`，全部通过（含 `schema`、`pyworker` 之外的所有 Go 包）。
- [ ] **Step 2：** `cd modules/factor/pyworker && python -m pytest -q`（协议无 `set_id`，预期不受影响，作回归）。
- [ ] **Step 3：** 仓库级：`bash scripts/check/check-gofmt.sh`、`git diff --check`、`make proto-check`、`make test-greenfield-contract`（确认没有残留 `reserved`）、`make test-event-contracts`。
- [ ] **Step 4：** 对 `modules/factor/schema/factor.sql` 逐个载入空 SQLite；用旧版 DDL 造一个含 3 个因子的库，经 `store.Open` → `ApplySchema` 迁移后核对成员数与状态（迁移测试已覆盖，此处人工抽查一次）。
- [ ] **Step 5：** 若某包仍有遗漏编译错误，在本 Task 内修复并以 `fix(factor): …` 提交；全部通过后在计划里勾选 A1–A7。

---

## 阶段 B：下游与运维

> 前提：阶段 A 的 proto 已定稿并重新生成（A3）。B1–B3 之间无依赖，可并行；B4 在 B1–B3 之后。

### Task B1：网关白名单

**Files:**
- Modify: `config/setup/service-deployments.yaml`（写 ACL 约 285 行、读路由约 290 行）
- Modify: `modules/admin/internal/service/sysdeploy/{defaults.go,routes.go}`（`defaults.go` 约 45 行）
- Modify tests: `modules/admin/internal/service/sysdeploy/{defaults_test.go,routes_test.go}`、`modules/admin/cmd/cli/service_deployments_test.go`、`period_gateway_contract_test.go`

- [ ] **Step 1：写失败测试：** 在上述测试里把期望的因子方法集合改为「删除 `SetFactorStatus`，新增 `AddFactorToSet`、`RemoveFactorFromSet`、`SetFactorMemberStatus`（均为写方法）」；`period_gateway_contract_test.go` 要求 YAML 与 `defaults.go` 一致，两边断言同时更新。
- [ ] **Step 2：运行确认失败**：`cd modules/admin && go test ./internal/service/sysdeploy/ ./cmd/cli/ -count=1`。
- [ ] **Step 3：实现。** YAML 与 `defaults.go` 同步修改；`ListFactors`、`GetFactor` 等读方法保持读路由。
- [ ] **Step 4：验证并提交**：同上命令通过，`git diff --check`。提交 `refactor(admin): route factor member RPCs through the gateway`。
- [ ] **部署注意（写入 Task D2）：** 已部署环境必须重新种子网关配置，否则新方法会 403。

### Task B2：strategy 适配

**Files:**
- Modify: `modules/strategy/internal/factorio/{client.go,client_test.go}`（`ListFactorsReq{SetId}` 约 71 行，读取 `GetSetId()` / `GetStatus()` 约 81–90 行）
- Modify（仅注释 / 措辞）: `modules/strategy/internal/compiler/verify_dependencies.go`（29–30 行）、`modules/strategy/docs/coin-selection-runtime.md`（11–13、46 行）

- [ ] **Step 1：写失败测试**（`client_test.go`，fake 返回 `FactorInfo`）：
  - `TestListFactorsUsesUsageStatusForRequestedSet`：`SetID` 取请求值，`Status` 取该 `set_id` 对应的 `usage.status`，`Outputs` 等取 `factor`；
  - `TestListFactorsIgnoresDefinitionsWithoutUsageInRequestedSet`（防御：后端按 `set_id` 过滤后不应出现，出现则跳过）；
  - `TestListFactorsDoesNotRequestSource`（请求里 `include_source` 为 false，策略不需要源码）。
- [ ] **Step 2：运行确认失败**：`cd modules/strategy && go test ./internal/factorio/ -count=1`。
- [ ] **Step 3：实现。** `ListFactors` 遍历 `FactorInfo`；`ListFactorSets` 只用 `GetFactorSet()`，不改。`compiler/verify_dependencies.go` 与 `trigger/processor.go` 逻辑不变，仅 `FactorDescriptor` 来源变化。
- [ ] **Step 4：核对冻结绑定**（后端规格 §10.8）：阅读 strategy 实例冻结的 `input_bindings_json` 内容，确认是否包含 `source_hash`。若包含，在 `coin-selection-runtime.md` 写明「编辑因子（`source_hash` 变化）后，依赖它的策略实例需要重新校验」，并补一条说明；若不包含，在计划里勾掉并记录结论。
- [ ] **Step 5：验证并提交**：`cd modules/strategy && go build ./... && go vet ./... && go test ./internal/factorio/ ./internal/compiler/ ./internal/trigger/ -count=1`。提交 `refactor(strategy): read factor usage status for the bound set`。

### Task B3：`moox` CLI setup 与配置

**Files:**
- Modify: `modules/cli/internal/setup/config/config.go`（`FactorSetup*` 结构约 643–677 行、`validateFactorSetup` 约 1511–1593 行、`factorSetupSetKey` 约 1595 行、默认值约 966 / 1022 行、汇总计数器约 44–51 行）
- Modify: `modules/cli/internal/command/{setup_factors.go,setup_init.go,setup.go}`（`setupFactorItem` 约 25–42 行、`factorAPIResponse` 约 324–353 行、`Apply` 约 376–460 行、`sameFactorContract` 约 467–487 行、`defaultSetupFactorItems` 约 59–86 行）
- Modify tests: `config_test.go`（约 1072–1151 行）、`setup_factors_test.go`、`setup_init_test.go`
- Modify: `moox.toml.example`（71–134 行）、`modules/cli/README.md`、`config/setup/README.md`；本机 `moox.toml`（已被 gitignore，11 条 items，手工迁移）

- [ ] **Step 1：配置形态**（`moox.toml`）：

```toml
[[factors.sets]]
source_dataset_id = "dataset_binance_kline_1m"
freq = "1m"
subject_mode = "all"

[[factors.definitions]]       # 只含定义字段，不再有 source_dataset_id / freq / status
factor_id = "Bias"
name = "Bias"
factor_type = "timeseries"
input_columns = ["close"]
outputs = ["bias_5", "bias_10", "bias_20"]
lookback_periods = 20
params_json = '{"windows":[5,10,20]}'

[[factors.members]]           # 成员：定义在某个因子集里的运行实例
source_dataset_id = "dataset_binance_kline_1m"
freq = "1m"
factor_id = "Bias"
status = "enabled"
```

- [ ] **Step 2：写失败测试：**
  - `config_test.go`：`TestFactorSetupParsesDefinitionsAndMembers`、`TestFactorSetupRejectsMemberWithUnknownFactor`、`TestFactorSetupRejectsMemberWithUnknownSet`、`TestFactorSetupRejectsLegacyItemsKeyWithMigrationHint`（旧 `[[factors.items]]` 由 `md.Undecoded()` 报错，错误信息要指出改为 `definitions` + `members`）；
  - `setup_factors_test.go`：`TestApplyCreatesSetsThenDefinitionsThenMembers`（顺序：`CreateFactorSet` → `GetFactor` 比对后 `CreateFactor` → 读 `ListFactors(set_id)` 的 `usages` 补 `AddFactorToSet` → 对目标为 enabled 的 `SetFactorMemberStatus`）、`TestApplyIsIdempotentWhenNothingChanged`、`TestApplySkipsIdenticalDefinition`、`TestApplyDoesNotPruneExtraMembers`（只补不减）、`TestSameFactorContractIgnoresSetAndStatus`；
  - **契约测试**：`TestFactorAPIResponseMatchesFactorProtoJSON`——CLI 用原始 JSON 结构体解析响应、不引用 `factorgen`，字段变化编译不会报错，要用真实 proto 的 JSON 样例固定 `factorAPIResponse` 的字段，防止静默漂移。
- [ ] **Step 3：运行确认失败**：`cd modules/cli && go test ./internal/setup/config/ ./internal/command/ -count=1`。
- [ ] **Step 4：实现。** `FactorSetupItem` 拆成 `FactorSetupDefinition` 与 `FactorSetupMember`；`validateFactorSetup` 校验成员引用的因子集与定义必须存在、同一 `(set, factor)` 不重复；`setup_factors.go` 的 `Apply` 按 Step 2 的顺序；`defaultSetupFactorItems`、`moox.toml.example`、本机 `moox.toml` 三份清单**同时**改（11 条）；汇总计数改为「定义 n / 成员 m」。
- [ ] **Step 5：验证并提交**：`cd modules/cli && go build ./... && go vet ./... && go test ./... -count=1`。提交 `refactor(cli): set up factor definitions and set members separately`。

### Task B4：文档与技能同步

**Files:**
- Modify: `docs/superpowers/specs/2026-10-04-factor-dataset-period-pipeline-design.md`（§2、§6、§7、§8.1、§9、§10、§13、§14、§19；§7 里旧 DDL 已过期，缺 `c_factors_omitted`、`c_subjects_omitted`，一并核对更新）
- Modify: `docs/superpowers/plans/2026-10-04-factor-dataset-period-pipeline.md`（顶部加修订说明；接口约定、Task 9 / 10 / 12 / 13 / 20 / 22 / 24 / 25 / 27 加「已被 2026-10-04 因子定义解耦计划修订」；Task 23 标注「已被本计划取代」）
- Modify: `docs/superpowers/specs/2026-10-04-factor-workbench-frontend-design.md`（顶部标注信息架构部分已被多页面设计取代；§5.4、§7、§8 同步）
- Modify: `docs/因子计算模块设计.md`（19–29、44、53、76、92–98 行）、`modules/factor/README.md`（24–50 行）、`docs/SUMMARY.md`（52–58 行）、`docs/元数据命名规范.md`（88–100、146–169 行）、`docs/架构总览.md`（271、594、747 行）、`docs/策略执行框架设计.md`（157 行）、`skills/moox/references/cli-operations.md`（15–30 行）、`modules/cli/README.md`（281–287、297 行）、`config/setup/README.md`（4、58–63 行）、`modules/strategy/docs/coin-selection-runtime.md`（11–13、46 行）

- [ ] **Step 1：** 逐个文档按「因子定义无状态 / 无 `set_id`，成员关系承载启停」改写，章节行号以执行时为准（本清单来自勘察，行号可能已漂移，用 `rg -n "SetFactorStatus|因子属于|set_id.*factor|factors.items" docs modules skills config` 复核）。
- [ ] **Step 2：** 在流水线计划与设计文档顶部加一句话修订说明并互链；Task 23 的步骤保留作历史，但明确指向本计划的 C 阶段。
- [ ] **Step 3：验证：** `make test-skill-contracts`；`make test-docs-architecture`（**注意：该检查在本改造之前就已失败，不要把它算到本计划头上**，只确认失败项没有新增）。
- [ ] **Step 4：提交** `docs: sync factor docs with definition and membership split`。

---

## 阶段 C：前端（`web/`）

> 页面原型在交互原型的 7 个画板里，验收时逐板对照。每个页面 Task 采用「纯 model 文件 + 薄页面」的写法（参考 `views/collector/task-results/task-results-model.ts`）：把筛选、派生、禁用规则写成纯函数，用 vitest 直接测；页面组件只做装配。契约测试沿用 `?raw` 读源码做字符串断言的约定（仅 `factor-contract.spec.ts` 用 `?raw`，其余用 `fs.readFileSync` + `normalizeSource`）。

### Task C1：`code-editor` 通用组件与依赖

**Files:**
- Modify: `web/package.json`、`web/pnpm-lock.yaml`
- Create: `web/src/components/code-editor/{index.vue,theme.ts,index.test.ts}`

- [ ] **Step 1：安装依赖**（版本以安装时最新 6.x 为准，写入 lock）：

```bash
cd web
pnpm add codemirror @codemirror/lang-python @codemirror/language @codemirror/state @codemirror/view @codemirror/commands @lezer/highlight
```

- [ ] **Step 2：写失败测试**（`index.test.ts`，jsdom 下只断言文档内容与事件，不断言像素 / 布局）：
  - `TestCodeEditorRendersModelValue`：挂载后 `EditorView` 文档内容 = `modelValue`；
  - `TestCodeEditorEmitsUpdateOnEdit`：派发一次文本事务后触发 `update:modelValue`；
  - `TestCodeEditorSyncsExternalValue`：外部改 `modelValue` 后文档更新且不回触发 `update:modelValue`（防循环）；
  - `TestCodeEditorReadOnly`：`readOnly` 时事务被拒绝（`EditorState.readOnly`）；
  - `TestCodeEditorDestroysViewOnUnmount`；
  - `TestCodeEditorHonorsMinMaxLines`（通过容器样式变量断言行数范围）。
- [ ] **Step 3：运行确认失败**：`cd web && pnpm exec vitest run src/components/code-editor`。
- [ ] **Step 4：实现。**
  - `theme.ts`：自定义 Dark+ 配色的 `EditorView.theme({...}, { dark: true })` 与 `HighlightStyle`（背景 `#1e1e1e`、文字 `#d4d4d4`、关键字 `#569cd6` / `#c586c0`、函数 `#dcdcaa`、字符串 `#ce9178`、数字 `#b5cea8`、注释 `#6a9955` 斜体；行号槽 `#858585`、当前行 `#2a2d2e`），不依赖 Arco 主题变量；
  - `index.vue`：props `modelValue`、`language`（首期仅 `python`，预留 `yaml`）、`readOnly`、`minLines`、`maxLines`、`placeholder`、`ariaLabel`；扩展 = 行号槽、当前行高亮、`history`、`indentWithTab`、缩进 4 空格、括号配对、`python()`、主题；键盘可离开编辑器（`Esc` 后 `Tab`，按所用版本 API 实现）；`defineExpose({ view })` 便于测试与「插入模板」；
  - **按需加载**：对外提供 `defineAsyncComponent(() => import("./index.vue"))` 的异步入口（例如 `async.ts`），消费者只用异步入口，保证 CodeMirror 单独成 chunk。
- [ ] **Step 5：验证并提交**：`pnpm exec vitest run src/components/code-editor && pnpm exec vue-tsc --noEmit && pnpm lint:eslint:check && pnpm lint:prettier:check`；再 `pnpm build:prod`，确认 `code-editor` 相关代码在独立 chunk 中、首屏入口 chunk 体积无明显增长（记录前后差值到提交说明）。提交 `feat(web): add lazily loaded CodeMirror code editor component`。

### Task C2：因子 API 层与外溢调用点

**Files:**
- Modify: `web/src/api/factor/{types.ts,index.ts,index.test.ts}`
- Modify: `web/src/views/strategy/components/strategy-instance-create.vue`（约 242 行按 `factor.status === "enabled"` 过滤）、`web/src/views/strategy/bindings.ts`（约 1 / 5 / 43–77 行）与 `bindings.test.ts`（约 7–19 行）、`web/src/views/home/home.vue`（约 626–629 行消费 `FactorSetInfo.factors`）

- [ ] **Step 1：写失败测试：**
  - `index.test.ts`：`listFactors without set_id requests all definitions`、`listFactors can request source explicitly`、`getFactor returns usages`、`addFactorToSet posts set_id and factor_id`、`removeFactorFromSet`、`setFactorMemberStatus returns the backfill job`、`createFactor payload has no set_id or status`；
  - `bindings.test.ts`：绑定因子列表来自「该因子集的 enabled 成员」；
  - `strategy-instance-create` 相关断言（若有）改为基于成员状态。
- [ ] **Step 2：运行确认失败**：`cd web && pnpm exec vitest run src/api/factor src/views/strategy`。
- [ ] **Step 3：实现。** 按「接口约定」重写类型；`index.ts` 新增三个成员接口，删除 `setFactorStatus`；`listFactors` 的 `set_id` 可选；`getFactorSet` 若无调用方则删除（勘察显示它是死 API，`getRecalcJob` 同理，先 `rg` 复核再删）。`strategy-instance-create.vue` 与 `bindings.ts` 改用 `listFactors({ set_id, status: "enabled" })`，读 `usages` 中对应因子集的状态；`home.vue` 用 `members.filter(m => m.status === "enabled").length` 计数——**勘察提示这些地方有多处可选链计数，类型一改会静默变成 0，务必用 `vue-tsc` 与测试各兜一次**。
- [ ] **Step 4：验证并提交**：`pnpm exec vitest run src/api/factor src/views/strategy && pnpm exec vue-tsc --noEmit`。提交 `refactor(web): model factor definitions and set members in the API layer`。

### Task C3：菜单、路由、语言包与侧栏高亮

**Files:**
- Modify: `web/src/api/modules/system/static-menu.ts`（约 63–64 行）、`web/src/router/route.ts`（约 59–64 行）、`web/src/lang/modules/{zhCN,enUS}.ts`（zh 约 31–32 / 37–38 行，en 约 33–34 / 39–40 行）
- Modify: `web/src/layout/components/Menu/index.vue`（约 32–35 行）、`web/src/layout/layout-head/index.vue`（约 12 行）、`web/src/layout/layout-mixing/index.vue`（约 20、74–77 行）、`web/src/typings/global.d.ts`（`MetaType`）
- Modify: `web/scripts/check-menu-structure.mjs`（retired 列表约 28–37、68、95–118、160–169 行）、`web/scripts/check-detail-page-style.mjs`（20–23 行，已失效引用）、`web/vitest.config.ts`（`include` 里写死的 factor spec 路径）
- Modify: `web/src/views/home/home.vue`（约 263、337–338、350–351、387、503 行的 `/factor/workbench` 入口）
- Create: `web/src/layout/components/Menu/menu-selected.ts`、`menu-selected.test.ts`

- [ ] **Step 1：写失败测试：**
  - `menu-selected.test.ts`：`resolveSelectedKeys`（纯函数）——`meta.activeMenu` 存在时返回它，否则回落到 `route.name`；
  - `check-menu-structure.mjs` 的新断言先写好（运行会失败）：`factor-compute` 目录下恰有 3 个菜单，顺序 overview → definitions → tasks；两个编辑器路由存在且 `meta.hide: true`、不在菜单；退役 `factor-workbench`、`factor-sets`、`factor-results`、`factor-recalc`；**把 `factor-definitions`、`factor-tasks` 从退役名单里移除**（它们现在是真实路由），`factor-bindings / factor-datasets / factor-construct` 保持退役；
  - zh-CN 标签断言：因子总览 / 因子定义 / 计算任务。
- [ ] **Step 2：运行确认失败**：`pnpm check:menu`、`pnpm exec vitest run src/layout`。
- [ ] **Step 3：实现。**
  - 路由（`route.ts`）：

| path | name | 组件 | meta |
| --- | --- | --- | --- |
| `/factor/overview` | `factor-overview` | `@/views/factor/overview/index.vue` | `{ title: "factor-overview" }` |
| `/factor/definitions` | `factor-definitions` | `@/views/factor/definitions/index.vue` | `{ title: "factor-definitions" }` |
| `/factor/tasks` | `factor-tasks` | `@/views/factor/task-management/index.vue` | `{ title: "factor-tasks" }` |
| `/factor/definitions/new` | `factor-definition-new` | `@/views/factor/editor/index.vue` | `{ title: "factor-definition-new", hide: true, activeMenu: "factor-definitions" }` |
| `/factor/definitions/:factorId/edit` | `factor-definition-edit` | 同上 | `{ title: "factor-definition-edit", hide: true, activeMenu: "factor-definitions" }` |

  - `static-menu.ts`：目录 `0240`（path 改 `/factor/overview`）下三个菜单 `024001 / 024002 / 024003`（排序 1 / 2 / 3，组件 `factor/overview/index`、`factor/definitions/index`、`factor/task-management/index`）；
  - 语言包：删除 `factor-workbench`，新增前端设计 §4.1 的 5 个 key；
  - `Menu/index.vue`、`layout-head`、`layout-mixing`：选中逻辑改为 `resolveSelectedKeys(useRoute())`（**直接读路由，不走 store**，因为隐藏路由不会写入 `currentRoute`）；`MetaType` 增加 `activeMenu?: string`；
  - `home.vue`：入口改到 `/factor/definitions`（因子定义）、`/factor/tasks`（计算任务）、`/factor/tasks?tab=results`（结果类入口）；
  - 脚本与配置：`check-detail-page-style.mjs` 里已不存在的 `factor/sets|definitions|tasks|results` 改为新页面路径；`vitest.config.ts` 的 `include` 先保持原路径（C12 再随测试文件迁移）。
  - 页面组件此时还不存在，路由用 `() => import(...)` 懒加载会在构建期报错：本 Task 先为五个路由放置**最小占位组件**（只渲染标题），后续 Task 逐个替换；占位组件在 C11 前必须全部被真实实现替换，C11 的自检里 `rg "占位"` 为空。
- [ ] **Step 4：验证并提交**：`pnpm check:menu && pnpm exec vitest run src/layout && pnpm exec vue-tsc --noEmit`。提交 `refactor(web): split factor menu into overview, definitions and tasks`。

### Task C4：store 与共享层

**Files:**
- Modify: `web/src/store/modules/factor.ts`（及 `factor.test.ts`）
- Create: `web/src/views/factor/shared/{use-factor-scope.ts,use-factor-scope.test.ts,set-select.vue,health-tag.vue,info-tip.vue,factor-page.scss}`
- Move（`git mv`）: `views/factor/workbench/{health.ts,health.test.ts,status.ts,status.test.ts}` → `views/factor/shared/`

- [ ] **Step 1：写失败测试：**
  - `factor.test.ts`：`datasetNames maps dataset id to name per space`、`setLabel falls back to source_dataset_id`、`reload keeps currentSetId when still present`、`members replace factors on FactorSetInfo`；
  - `use-factor-scope.test.ts`（`vi.hoisted` + `vi.mock` 路由 / space store / API）：`does not reset the store when remounted for the same space`、`resets and reloads when the space id changes`、`requireSet resolves ?set= then currentSetId then first`、`writes the resolved set back with router.replace`、`optional scope leaves ?set= untouched`、`tab switch keeps set and drops unrelated detail/job`、`polling pauses when deactivated`（沿用 `usePolling` 的 KeepAlive 感知）。
- [ ] **Step 2：实现。**
  - store：`sets[].members` 取代 `factors`；新增 `datasetNames`（`ListDatasets` 的 `dataset_id → name`，每个空间加载一次）与 `setLabel(set)`（「数据集名称 · 频率」，取不到名称时回落 `source_dataset_id`）；`currentSetId` 语义改为「跨页最近使用的计算任务」；保留 `RequestGate` 过期响应保护；
  - `use-factor-scope.ts`：见「已确认的实现取舍」第 3 条——**只在空间 id 变化时 reset + load**，首次挂载若 store 已加载同一空间则直接使用；`?set=` 与 store 同步由 `requireSet` 区分两类场景（前端设计 §4.3）；接入 `usePolling`；
  - `info-tip.vue`：20px 图标按钮，`aria-label="说明"`，hover / focus 显示浮层（样式参照 `views/data/datasets/index.vue` 里已有的 ⓘ 写法，约 8–22、833–847 行）；
  - `factor-page.scss`：`.page-head`、`.result-toolbar` 等采集页 scoped 样式的等价写法（同结构同数值），供各因子页 `@use`。
- [ ] **Step 3：验证并提交**：`pnpm exec vitest run src/store/modules src/views/factor/shared && pnpm exec vue-tsc --noEmit`。提交 `refactor(web): rebuild factor store and shared page helpers`。

### Task C5：任务宿主与「计算任务」Tab

**Files:**
- Create: `web/src/views/factor/task-management/index.vue`（Tab 宿主）
- Create: `web/src/views/factor/compute-tasks/{index.vue,compute-tasks-model.ts,compute-tasks-model.test.ts,task-detail-drawer.vue,member-section.vue,add-factor-modal.vue,add-factor-precheck.ts,add-factor-precheck.test.ts}`
- Move（`git mv` 后调整）: `workbench/{set-create-modal.vue,subject-scope-fields.vue}` → `compute-tasks/`；`set-header.vue` 的生命周期动作（`deleteFactorSet`、`setFactorSetStatus`、重试激活、`updateFactorSet`）并入 `task-detail-drawer.vue`

- [ ] **Step 1：写失败测试：**
  - `compute-tasks-model.test.ts`：`filterSets by keyword and status`、`factor counts are enabled over total from members`、`setHealth label per status`、`delete disabled while members exist and explains why`、`member actions: edit and remove disabled when enabled`、`toggle label follows member status`、`add factor disabled for pending or deleting sets`；
  - `add-factor-precheck.test.ts`：`candidate already in set is disabled with reason`、`missing input column reports the column name`、`output colliding with an existing member output is rejected`、`output colliding with source column is rejected`、`valid candidate is selectable`（输入为定义、源数据集列、成员定义；纯函数）；
  - 宿主：`task host normalizes ?tab=, keeps ?set= when switching tabs, drops detail/job` 与 `uses PageTitleTabs with keep-alive`（`fs.readFileSync` 断言，对标 `collector/task-management/index.vue` 的 `activeTab` / `normalizeTab` / `router.replace` 写法）。
- [ ] **Step 2：运行确认失败**：`pnpm exec vitest run src/views/factor/task-management src/views/factor/compute-tasks`。
- [ ] **Step 3：实现。**
  - 宿主：`PageTitleTabs`（`aria-label="计算任务"`）+ `<keep-alive><component :is /></keep-alive>`；Tab = 计算任务 | 计算结果 | 补算；默认 Tab 不写 URL；**标题 Tab 下不放说明行**；
  - 「计算任务」Tab：工具栏左侧「共 N 个计算任务」+ `info-tip`（文案见前端设计 §5.4.1）；右侧搜索、状态筛选、「新建计算任务」（绿色 `type="primary" status="success"`，打开 `SetCreateModal`）；表格列、行操作、轮询 10 秒按前端设计 §5.4.1；
  - 详情抽屉（700px，`?detail=<set_id>` 驱动）：基本信息 → **因子（启用 n / 共 m）成员区** → 前往 → 危险区；成员行：因子 ID + 类型 tag + 输出列、成员状态 tag、最近周期 tag、`编辑定义`（已启用置灰）/ `启用·停用`（`setFactorMemberStatus`，启用后若返回 `backfill_job` 弹 Toast + 「查看回填」链到 `?tab=recalc&set=&job=`）/ `移除`（`removeFactorFromSet`，已启用置灰）；
  - 添加因子弹窗：候选 = `listFactors()` 全部定义（不带源码），多选 + 搜索，行内 note 按 precheck 结果显示（可添加 / 已在该计算任务中 / 具体原因），「添加（n）」对每个选中项调用 `addFactorToSet`，失败时按行展示后端原因；预检仅用于提示，**以后端校验为准**（后端规格 §10）；
  - 数据来源：`store.sets`（`members`、`last_run`）；成员变更后 `store.reload()`。
- [ ] **Step 4：验证并提交**：`pnpm exec vitest run src/views/factor && pnpm exec vue-tsc --noEmit && pnpm lint:eslint:check`；对照原型画板 3a、3d。提交 `feat(web): add compute tasks tab with member management`。

### Task C6：因子定义页

**Files:**
- Create: `web/src/views/factor/definitions/{index.vue,definitions-model.ts,definitions-model.test.ts,factor-detail-drawer.vue}`

- [ ] **Step 1：写失败测试**（`definitions-model.test.ts`）：`usage filter: all/using/idle counts`、`type filter`、`search by factor_id or module name`、`usage chips carry dataset name · freq and status dot`、`edit locked when any enabled usage, with reason`、`delete locked when any usage, with reason`、`unused definition can be edited and deleted`；契约：`definitions page has no enable/disable action and no status column`。
- [ ] **Step 2：实现。** 页头 `h2` 因子定义 + `info-tip` + 绿色「新增因子」；筛选条（类型下拉 / 使用情况分段 / 搜索）；表格列：因子 ID（+「模块 xxx」）、类型、输入列、输出列、回看周期、使用情况（chip 点击跳 `?detail=<set>` 的计算任务抽屉）、操作；数据来自 `listFactors()` + `store.sets` 拼名称；详情抽屉：基本属性、输入输出、参数 JSON、**只读源码用 `code-editor`（`readOnly`）**，源码通过 `getFactor` 取（列表不带源码）、完整使用情况；`onActivated` 时重新拉取（成员变更后使用情况会变化）。
- [ ] **Step 3：验证并提交**：`pnpm exec vitest run src/views/factor/definitions && pnpm exec vue-tsc --noEmit`；对照原型画板 2。提交 `feat(web): add factor definitions page with usage view`。

### Task C7：因子编辑器（隐藏页）

**Files:**
- Create: `web/src/views/factor/editor/{index.vue,factor-form.ts,factor-form.test.ts,editor-template.ts}`（`factor-form.ts` / 测试由 `workbench/` `git mv` 迁入）

- [ ] **Step 1：写失败测试：**
  - `factor-form.test.ts`（迁移并改写）：保存前检查产出 checklist（对应后端规格 §6.1）：`id valid and not empty`、`inputs non-empty without duplicates`、`outputs non-empty, no reserved columns, no duplicates`、`params must be a JSON object`、`lookback at least 1`、`allow_partial_universe only for cross_section`；**不再有**「输入列 ⊆ 源数据集列」；`submit payload has no set_id or status`；
  - 契约：`editor has no 因子集 / 源数据集 / 频率 selector`、`editor never calls createFactorSet`、`editor registers onBeforeRouteLeave`、`editor uses code-editor and no a-textarea for source`、`reference dataset is optional and not part of the payload`。
- [ ] **Step 2：实现。** 页头「← 返回因子定义 · 新建因子 / 编辑因子 `<factor_id>`」+「取消」「保存因子」；左栏表单：因子 ID、模块名、类型分段（选截面才出现 `allow_partial_universe`）、回看周期、输入列 chips（自由输入）+ 可选**参考数据集**（`listDatasets` 只列 `dataset_role !== "factor_result"`，选后 `listDatasetColumns` 列出可点选的列，**仅提示不保存**）、输出列 chips、参数 JSON；右栏：`code-editor`（Python、深色，文件名标签 `<factor_id>.py`，底部状态栏行列 + 语法检查状态来自保存前检查的试 LOAD 结果）+「保存前检查」清单 +「插入模板」（`editor-template.ts`，已有内容时二次确认）；保存：`createFactor` / `updateFactor`，成功返回 `/factor/definitions` 并 Toast「因子已创建，到计算任务里添加并启用」；**编辑**时若后端因 D20 拒绝，把「计算任务 + 原因」原样展示在表单顶部 `a-alert`；定义被已启用成员引用时进入**只读模式**（顶部警告 + 引用它的计算任务 chip + 「去计算任务」）；离开保护同策略编辑器（`onBeforeRouteLeave` + 保存成功后标记已提交）；侧栏高亮由 C3 的 `activeMenu` 提供。
- [ ] **Step 3：验证并提交**：`pnpm exec vitest run src/views/factor/editor && pnpm exec vue-tsc --noEmit`；对照原型画板 2b（含深色编辑器外观）。提交 `feat(web): add full-page factor editor with code editor`。

### Task C8：「补算」Tab

**Files:**
- Create: `web/src/views/factor/recalc/{index.vue,recalc-model.ts,recalc-model.test.ts,recalc-create-drawer.vue}`（逻辑由 `workbench/tabs/recalc.vue` 迁移）

- [ ] **Step 1：写失败测试：**`recalc-model.test.ts`：`status segment filters (all / running / succeeded / failed+cancelled)`、`source tag: factor-enable- prefix means 启用回填`、`degraded note renders as 部分降级 not error`（沿用 `splitJobNote`）、`progress percent from progress_time within range`、`estimate periods x factors`、`default factor selection is all enabled members`；契约：`recalc tab has no explanation row, uses info-tip`、`uses listRecalcJobs and cancelRecalcJob, not getRecalcJob`。
- [ ] **Step 2：实现。** 工具栏左侧「共 N 条补算任务」+ `info-tip`（文案见前端设计 §5.4.3）；右侧计算任务选择（必选，`requireSet`）、刷新、「新建补算」；表格与状态筛选按前端设计 §5.4.3；`?job=` 命中行高亮并滚动到可视区；新建补算抽屉：因子多选（候选为该计算任务的**已启用成员**，默认全选）、对象范围、时间范围 + 快捷 chip、汇总行、请求 ID（占位「留空自动生成」）、「提交补算」→ `recalcFactors`；轮询 3–5 秒，仅有进行中任务时继续。
- [ ] **Step 3：验证并提交**：`pnpm exec vitest run src/views/factor/recalc`；对照原型画板 3c。提交 `feat(web): move recalc into a task tab with create drawer`。

### Task C9：「计算结果」Tab

**Files:**
- Create: `web/src/views/factor/results/{index.vue,results-model.ts,results-model.test.ts}`（逻辑由 `workbench/tabs/results.vue` 迁移）

- [ ] **Step 1：写失败测试：**`results-model.test.ts`：`picks the factor_result view of the result dataset`（`view.attributes?.view_role === "factor_result"` 且 `dataset_id === result_dataset_id`）、`pending sets are excluded, disabled sets are labeled`、`status line extras: last period and normal/degraded counts`；契约：`uses result-toolbar and ViewBrowse status-extra slot, not status-strip`、`no explanation row under the tabs`、`no ViewDefinitions`。
- [ ] **Step 2：实现。** 版式照搬采集结果页（`views/collector/task-results/index.vue` 的结构）：`.result-toolbar`「共 N 个计算任务；状态读取：HH:mm:ss」+「刷新结果」→ 圆角 `a-tabs`（每个计算任务一个，选中项与 `?set=` 同步）→ 内嵌 `ViewBrowse`（`:embedded="true"`），「最近周期」「正常 / 降级」tag 放进 `ViewBrowse` **已有**的 `status-extra` 插槽；没有结果 View 时 `a-empty` + 「前往计算任务」。**注意**：`ViewBrowse` 嵌入模式会调 `getView` 与 `getDataset`，e2e mock 要补这两个接口。
- [ ] **Step 3：验证并提交**：`pnpm exec vitest run src/views/factor/results`；对照原型画板 3b。提交 `feat(web): rebuild factor results tab on the collector results layout`。

### Task C10：因子总览

**Files:**
- Create: `web/src/views/factor/overview/{index.vue,overview-model.ts,overview-model.test.ts}`

- [ ] **Step 1：写失败测试：**`overview-model.test.ts`：`stats strip from engine status`（实时消费、Python Worker、计算队列汇总、进行中补算）、`task cards list member chips from last_run.factors`、`pending items derivation`（failed → 计算任务详情；degraded → 计算结果；lagging 超过 2 个频率周期 → 计算结果；pending / 激活失败 → 计算任务重试激活；最近失败的补算 → 补算 Tab 过滤失败）、`running recalc only queries enabled sets`、`links target the right tab and carry ?set= / ?detail=`；契约：`overview has no write action`。
- [ ] **Step 2：实现。** 只读 + 跳转：页头 + 刷新、4 格统计条、左列计算任务卡片（标题「数据集名称 · 频率」、`setHealth` 标签、成员因子状态 chip、底部「结果 / 补算 / 因子」链接）、右列「待处理」「进行中的补算」；进行中补算只对 `enabled` 的计算任务扇出 `listRecalcJobs({ set_id, statuses: ["accepted","running"] })`，页面失活不轮询；轮询 10 秒（`usePolling`）。
- [ ] **Step 3：验证并提交**：`pnpm exec vitest run src/views/factor/overview`；对照原型画板 1。提交 `feat(web): add factor overview page`。

### Task C11：删除旧工作台与文案清理

**Files:**
- Delete: `web/src/views/factor/workbench/`（`index.vue`、`set-list.vue`、`set-header.vue`、`factor-editor.vue`、`tabs/*`；已迁移的文件此时应已不在原处）
- Delete（确认无其他引用后）: `web/src/components/code-block`
- Modify: 全部残留的「因子集」界面文案 → 「计算任务」（组件、提示、确认弹窗、测试断言；后端返回的错误消息除外）

- [ ] **Step 1：** `git rm -r web/src/views/factor/workbench`；`rg -n "factor/workbench|factor-workbench|FactorWorkbench" web/src web/tests web/scripts` 必须为空。
- [ ] **Step 2：** `rg -n "code-block" web/src`，仅被已删除页面使用则删除该组件目录；`rg -n "因子集" web/src` 逐个替换为「计算任务」（保留后端错误透传与代码标识符）。
- [ ] **Step 3：** 清理 C3 放置的占位组件：`rg -n "占位" web/src/views/factor` 必须为空；清理 store 里未使用字段与未使用 API（`getFactorSet` / `getRecalcJob` 若仍无调用方）。
- [ ] **Step 4：验证并提交**：`pnpm exec vue-tsc --noEmit && pnpm lint:eslint:check && pnpm exec vitest run`。提交 `refactor(web): remove the factor workbench`。

### Task C12：契约测试、e2e 与全量验证

**Files:**
- Modify/Move: `web/src/views/factor/__tests__/factor-contract.spec.ts`（`vitest.config.ts` 的 `include` 同步）
- Modify: `web/tests/page-layout-standard-contract.test.ts`（约 85–95、147–150、206–209 行）、`web/tests/factor-dataset-workflow.spec.ts`

- [ ] **Step 1：`factor-contract.spec.ts`。** 「API 契约」三条：`uses explicit generic time-series fields` 重写（`FactorDef` 无 `set_id` / `status`，`FactorMember` / `FactorInfo` 存在）；`accepts set-scoped async recalc jobs with a request id` 与 `exposes per-factor period states and the deleting set status` 保留。「工作台契约」六条按页面拆开，`?raw` 引用改到新文件：
  - `keeps the selected set and tab in the URL query` → 任务宿主与 `use-factor-scope`（`route.query.tab`、`route.query.set`、`usePolling`）；
  - `changes status only through SetFactorStatus…` → 重写为「成员状态只经 `setFactorMemberStatus`，启用后呈现 `backfill_job`，且不含 `v-model="form.status"`」；
  - `limits editor inputs to source dataset columns…` → 重写为「编辑器输入列自由输入，参考数据集仅提示、不进 payload」；
  - `loads recalc jobs from the backend and renders degraded notes separately…` → 补算 Tab（保留 `splitJobNote`、「部分降级」、不含 `getRecalcJob`）；
  - `offers lifecycle actions for sets including purge delete` → 计算任务 Tab（`updateFactorSet`、`deleteFactorSet(…, purge.value)`、`setFactorSetStatus`）；
  - `scopes results to the factor result view of the selected set` → 结果 Tab（`view_role === "factor_result"`、`result_dataset_id`、不含 `ViewDefinitions`、使用 `result-toolbar` 与 `status-extra`、不含 `status-strip`）。
- [ ] **Step 2：布局契约。** `page-layout-standard-contract.test.ts`：把「工作台集合列表 + Tab 同一布局」「因子集结果」两处引用改到新页面；保留「结果 View」、`:embedded="true"`；`status-strip` 断言替换为 `result-toolbar` + `status-extra`；新增「任务宿主使用 `PageTitleTabs` 与 `keep-alive`」「各 Tab 标题下不单独放说明行、使用 `info-tip`」。
- [ ] **Step 3：e2e 重写**（`factor-dataset-workflow.spec.ts`，mock 网关；补 `AddFactorToSet`、`RemoveFactorFromSet`、`SetFactorMemberStatus`、`GetView`、`GetDataset` 的 mock，`ListFactors` 返回 `FactorInfo`，`ListFactorSets` 返回 `members`）：
  1. `/#/factor/tasks`：可见计算任务名称与 `set_id`；点开详情抽屉可见「因子」成员区与 `Bias`；
  2. `/#/factor/definitions`：可见 `Bias` 与「使用情况」chip；被已启用成员引用的「编辑」「删除」置灰；
  3. `/#/factor/tasks?tab=results&set=…`：可见「共 N 个计算任务；状态读取」与「结果 View view_factor_binance_kline_1m」；
  4. `/#/factor/tasks?tab=recalc&set=…`：新建补算，填「留空自动生成」→「提交补算」→ 可见「已受理」；
  5. `/#/factor/definitions/new`：在 `.cm-content` 里键入源码并保存，断言只调用 `CreateFactor`（请求体无 `set_id`）、回到列表后「未被使用」；脏数据离开弹确认；
  6. 计算任务抽屉 →「添加因子」→ 勾选 → 添加（`AddFactorToSet`）→ 成员「已停用」→ 启用（`SetFactorMemberStatus`，可见回填提示）；
  7. `/#/factor/overview`：可见计算任务卡片与统计条；
  8. 侧栏高亮：进入 `/#/factor/definitions/new` 时「因子定义」处于选中态。
- [ ] **Step 4：全量验证：**

```bash
cd web
pnpm test:unit
pnpm exec vue-tsc --noEmit
pnpm lint:eslint:check
pnpm lint:prettier:check
pnpm check:menu
pnpm check:detail-pages
pnpm check:collector-task-style   # 采集页未改，作回归
pnpm exec playwright test tests/factor-dataset-workflow.spec.ts
pnpm build:prod
cd .. && make test-web check-format check-lint
```

- [ ] **Step 5：提交** `test(web): rewrite factor contracts and e2e for the multi-page layout`。

---

## 阶段 D：联调、部署与收尾

### Task D1：本地联调验收

**Files:** 无新增；执行时在本计划末尾追加「验收记录」小节，记录各项验收结果。

- [ ] **Step 1：** 起一个**全新**的 factor SQLite、真实 Storage / Gateway（或测试环境），用 B3 的 `moox` CLI setup 导入 11 个定义与成员（先 `disabled`，再逐个启用），确认每个启用都产生 `factor-enable-` 回填任务。
- [ ] **Step 2：** `pnpm dev` 对着真实网关，逐板对照原型验收：总览（卡片 / 待处理 / 进行中的补算）、因子定义（使用情况 chip、置灰规则）、编辑器（深色 Python 高亮、静态检查、参考数据集、只读模式、离开确认）、计算任务 Tab（成员区、添加因子预检与后端拒绝原因展示）、计算结果 Tab（与采集结果页并排对比版式）、补算 Tab、侧栏高亮。
- [ ] **Step 3：多因子集复用：** 同一个定义（如 `Bias`）加入 1m 与 1h 两个计算任务，分别启用，确认各自结果数据集有数据、互不影响；编辑该定义时被已启用成员拦截，停用后可编辑，保存时对两个因子集重新校验。
- [ ] **Step 4：迁移演练：** 用部署环境 SQLite 的**副本**启动新版 factor，确认迁移后成员数 = 旧因子数、状态一致、`last_run` 与补算历史仍在；原库不动。
- [ ] **Step 5：** 发现的问题在对应阶段的 Task 内修复并以 `fix(...)` 提交，不在本 Task 里夹带无关改动。

### Task D2：部署切换手册

**Files:** 手册内容追加到本计划末尾的「验收记录」小节；不新增部署脚本，除非 D1 暴露出必要性。

- [ ] **Step 1：发布前：** 备份 factor SQLite 文件；记录当前各因子集的 enabled 因子清单（`ListFactorSets` 导出）；通知暂停手工补算；确认没有进行中的补算任务（`ListRecalcJobs` 各因子集 `accepted/running` 为空）。
- [ ] **Step 2：发布顺序（同批，一次完成）：** ① 重新种子网关配置（B1，否则新方法 403）→ ② 发布 `factor`（启动时自动迁移，日志确认「迁移完成：N 个成员」）→ ③ 发布 `strategy`（B2）→ ④ 发布 `web`（C 阶段）→ ⑤ 在运维机更新 `moox.toml` 并重新执行 `moox` CLI setup（B3，预期零差异）。
- [ ] **Step 3：发布后核对：** 各因子集 enabled 成员数与发布前清单一致；`GetStatus` 实时消费正常；等一个周期，确认每个因子集产出 `FactorPeriodComputed` 且 `last_run` 前进；策略实例的依赖校验通过；前端 3 个菜单页可用。
- [ ] **Step 4：回滚方案：** 迁移不可逆，回滚 = 停服务 → 恢复备份 SQLite → 回退 `factor` / `strategy` / `web` / 网关种子到上一版本（同批回退）。因此 Step 1 的备份是硬前置。

### Task D3：收尾与提交

**Files:**
- Modify: `modules/factor/internal/store/{migrate.go,migrate_test.go}`（删除一次性迁移）、本计划（勾选与验收记录）、三份设计 / 计划文档的状态字段

- [ ] **Step 1：** 部署验证通过后，删除一次性迁移代码与测试（后端规格要求「迁移后删除，不保留兼容读取路径」），`ApplySchema` 恢复为只校验新结构；提交 `refactor(factor): drop one-off legacy definition migration`。**若按「已确认的实现取舍」第 6 条选择了跳过迁移，则本步骤与 A2 的迁移步骤一并省略。**
- [ ] **Step 2：** 把两份设计文档与本计划的状态更新为「已实施」，在流水线计划 Task 23 处补指向本计划完成情况的一句话。
- [ ] **Step 3：** `git status` 确认本会话涉及的文件全部已提交（`AGENTS.md`：会话结束提交并 `git push`）；`git push`。

---

## 全局验收清单

- [ ] `cd modules/factor && go build ./... && go vet ./... && go test ./... -count=1`；`modules/admin`、`modules/strategy`、`modules/cli` 对应包 `go build` / `go vet` / `go test` 全绿。
- [ ] `make proto-check`、`make test-greenfield-contract`、`make test-event-contracts`、`make test-skill-contracts` 通过；`make test-docs-architecture` 没有**新增**失败项（它在本计划之前就已失败）。
- [ ] `sqlite3 :memory: < modules/factor/schema/factor.sql` 通过，`git diff --check` 无告警，`bash scripts/check/check-gofmt.sh` 通过。
- [ ] `FactorDef` 无 `set_id` / `status`；`SetFactorStatus` 在 proto、网关种子、strategy、CLI、web 中零残留：`rg -n "SetFactorStatus|setFactorStatus" modules web/src config docs` 仅剩历史文档中的说明性文字。
- [ ] web：`pnpm test:unit`、`vue-tsc`、ESLint（`--max-warnings 0`）、Prettier、`pnpm check:menu`、`pnpm check:detail-pages`、Playwright e2e、`pnpm build:prod` 全绿；`rg -n "factor-workbench|factor/workbench|占位" web/src` 为空。
- [ ] 7 个原型画板逐板验收完成；深色代码编辑器在因子编辑器与定义详情抽屉中一致；侧栏在编辑器页高亮「因子定义」。
- [ ] 部署演练（D1 Step 4）与回滚手册（D2 Step 4）已在副本上验证。

## 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| 同批发布失败 / 局部回滚造成前后端不一致 | 迁移不可逆，必须先备份；回退时 `factor`、`strategy`、`web`、网关种子同批回退（D28） |
| 网关种子未重新写入，新 RPC 403 | D2 把「重新种子网关」放在第一步，并在发布后用 `AddFactorToSet` 做一次探测 |
| CLI 用原始 JSON 解析响应，字段漂移编译不报错 | B3 增加 `TestFactorAPIResponseMatchesFactorProtoJSON` 契约测试 |
| strategy 热路径载荷变大 | D24：列表默认省略 `source_code`，策略不请求源码 |
| 编辑被多处引用的定义影响面大 | D20：任一 enabled 成员引用即拒绝；停用后编辑时对所有引用因子集重新校验并指出失败的因子集 |
| 添加 / 编辑并发交错 | D26：定义锁 → 因子集锁；Add 在事务内重读定义 |
| CodeMirror 增加构建体积、jsdom 无法测布局 | 动态加载；单测只断言内容与事件，布局由 e2e 与人工验收覆盖 |
| 在途补算遇到成员停用 | 沿用现状（任务以「not enabled in set」失败），D2 发布前要求无进行中任务 |
| `test-docs-architecture` 本来就在失败 | 只核对失败项不新增，不在本计划内修复 |

## 自检记录

- **覆盖检查：** 设计文档的每个决策都有落点——D16–D23 → A1–A6；D24 → A6 / B2 / C2；D25 → A1 / A4；D26 → A4；D27 → A4；D28 → 「执行约束」与 D2；前端设计 §4–§6 → C3–C10；§8 迁移与删除清单 → C3 / C11；§9 测试方案 → C12；§10 风险 → 「风险与回滚」。
- **顺序检查：** 后端 A1（domain）→ A2（store）→ A3（proto）→ A4（catalog）→ A5（recalc / pipeline / trigger）→ A6（rpc）→ A7（bootstrap / CLI）自底向上，每个 Task 只依赖前面已完成的包；下游 B 依赖 A3；前端 C1 / C3 / C4 与后端并行，C5–C10 联调依赖阶段 A。
- **类型一致性：** `FactorUsage`、`FactorInfo`、`FactorMember` 在 Go domain、proto、TypeScript 三处的字段名一致（接口约定一节为唯一来源）；`EnabledSetByDataset` 签名保持不变，所以 trigger / pipeline 热路径只需改测试与两处 `Status` 过滤。
- **已知不确定项（执行时先核对）：** 工作区 git 基线；各文件行号来自勘察，可能已漂移；`compatibleResultColumn` 对重复列的幂等行为；strategy 冻结绑定是否含 `source_hash`；`ValidateFactor` 里 `AllowPartialUniverse` 的现有条件写法；`test-docs-architecture` 的既有失败项清单。
