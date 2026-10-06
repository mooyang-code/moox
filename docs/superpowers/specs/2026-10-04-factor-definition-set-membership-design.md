# 因子定义与因子集解耦设计：FactorDef / FactorSetMember

- 日期：2026-10-04
- 状态：已实施；部署形态已被 [`2026-10-05-factor-manager-engine-split-design.md`](./2026-10-05-factor-manager-engine-split-design.md) 取代（管理端 + 本机计算引擎），2026-10-06 按该设计完成正式环境发布与验收
- 性质：对 [`2026-10-04-factor-dataset-period-pipeline-design.md`](./2026-10-04-factor-dataset-period-pipeline-design.md)（下称「流水线设计」）的**增量修订**。评审通过后，把第 8 节列出的修订点合并回流水线设计与执行计划，本文随之归档。
- 前端配套：[`2026-10-04-factor-multi-page-frontend-design.md`](./2026-10-04-factor-multi-page-frontend-design.md)（界面上「因子集」叫「计算任务」，本文沿用后端名称 FactorSet / `set_id`）。

## 1. 背景与问题

流水线设计把因子定义建模为「每个因子只属于一个因子集」：`t_factor_defs` 带 `c_set_id` 与 `c_status`，创建因子必须指定 `set_id`，输入列按该因子集的源数据集校验，输出列在因子集内查重，启停在因子上。

这个模型在使用中暴露了几个问题：

1. **定义和运行位置绑死**：因子定义本来只是「一个 Python 算法 + 参数 + 输入输出声明」，却必须先有因子集才能创建，且创建后不能换集合。
2. **同一算法无法复用**：同一个算法、同一套参数想在 1m 和 1h 两个因子集上都算，只能注册成两个因子（例如 `Bias` / `Bias1h`），源码、参数各存一份，改一处要改多处。
3. **定义和运行状态混在一起**：`enabled / disabled` 描述的是「这个因子在某个因子集里是否在跑」，却挂在定义上；一个定义要想同时在两个集合里一个跑一个停，现有模型表达不了。
4. **前端无法给出「因子库」视图**：因子定义页只能是「某个因子集下的因子列表」，不能是独立的定义管理。

## 2. 目标与非目标

目标：

- 因子定义只描述算法本身（代码、参数、类型、回看周期、输入输出列名），**不含运行位置、不含运行状态**；
- 一个定义可以被多个因子集引用，运行状态属于「因子集 × 因子」这一成员关系；
- 校验分层：创建定义只做与数据集无关的静态校验，与具体源数据集相关的校验推迟到「加入因子集」时。

非目标：

- 不改周期流水线、结果数据集、Python 计算契约、补算执行、失败语义（流水线设计第 4、5、10–12、14、15 节）；
- 不引入因子之间的依赖（因子独立性约束不变）；
- 不做定义版本管理与成员级参数覆盖（见第 10 节取舍）。

## 3. 概念变更

| 概念 | 现状 | 本设计 |
| --- | --- | --- |
| 因子定义 FactorDef | 算法 + 参数 + 输入输出，**属于一个因子集，带 enabled/disabled** | 算法 + 参数 + 输入输出声明，**与因子集无关，无状态**；`factor_id` 仍全局唯一 |
| 因子集 FactorSet | 绑定 `(space, 源数据集, 频率)` 的一组因子 | 不变 |
| 成员 FactorSetMember | 无 | `(set_id, factor_id)`：定义在某个因子集中的运行实例，状态 `enabled / disabled` |
| 结果列 | 因子集内因子的输出 | 因子集内 **enabled 成员** 的输出（加列时机不变：启用时） |

## 4. 已确认决策（增量，编号接流水线设计 D1–D15）

| 编号 | 决策 |
| --- | --- |
| D16 | `FactorDef` 不含 `set_id`、不含 `status`。 |
| D17 | 成员关系 `(set_id, factor_id)` 唯一；一个定义可加入多个因子集；同一因子集内同一定义最多一次。 |
| D18 | 启用、停用、回填都发生在成员上：启用成员 = 加列 → enabled → 自动补算（仅该因子、仅该因子集），语义同流水线设计 §13「启用因子」。 |
| D19 | 与源数据集相关的校验在 `AddFactorToSet` 完成：`input_columns ⊆ 源数据集列`，输出列不与源数据集业务列、保留列、同因子集其他成员的输出重名。创建定义只做静态校验（见 §6.1）。 |
| D20 | 编辑规则：定义被任一 enabled 成员引用时拒绝编辑（沿用「先停用再修改」）；否则允许，且编辑时对**所有引用它的因子集**重新执行 D19 校验，任一失败整体拒绝并指出因子集与原因。 |
| D21 | 删除规则：只有没有任何成员引用的定义可删除；`RemoveFactorFromSet` 只允许移除 disabled 成员；移除成员保留结果列（沿 D12）。 |
| D22 | 同一定义的源码不可变（`source_hash`），所有引用它的因子集共用同一份；需要不同参数或不同回看周期，仍注册为不同的 `factor_id`（沿用「同一 Python 模块配不同参数可注册为多个因子」）。 |
| D23 | 删除因子集的前置条件由「无因子」改为「无成员」；`purge=true` 的 `deleting` 流程不变。 |
| D24 | 列表类响应不带源码：`FactorMember.factor` 在 `GetFactorSet` / `ListFactorSets` 中、`FactorInfo.factor` 在 `ListFactors` 中默认**省略 `source_code`**（`source_hash` 保留）；`ListFactorsReq.include_source`（默认 false）可显式要求带出；`GetFactor` 始终带 `source_code`。原因：前端 10 秒轮询 `ListFactorSets`、strategy 每个实例每个事件都会调用 `ListFactors`，载荷不应随因子数与源码长度增长。 |
| D25 | `AddFactorToSet` 追加一条校验：输出列不得与结果数据集中**已存在且 `origin_factor_id` 不同**的列重名；成员被移除后重新加入同一定义时，复用已保留的同名列（`origin_factor_id` 相同则视为同一归属，不报冲突）。 |
| D26 | 锁序固定为「定义级锁 → 因子集锁」，定义级锁的键加 `factor:` 前缀，与 `fset_*` 键空间隔离。`AddFactorToSet`、`SetFactorMemberStatus`、`RemoveFactorFromSet`、`UpdateFactor`、`DeleteFactor` 都先取定义级锁（`Add` 校验通过后在事务内再读一次定义，避免与并发 `Update` 交错）；`UpdateFactor` 对所有引用因子集的重新校验在持有定义锁的状态下完成；「无 enabled 成员」仍由事务内 `NOT EXISTS` 兜底。 |
| D27 | 启用成员时**重新执行** §6.2 的全部校验（定义可能在成员停用期间被编辑，源数据集列也可能变化），不信任加入时的结果。 |
| D28 | 不做滚动兼容：`factor`、`strategy`、`moox` CLI 与 `web` 同一批发布；`FactorSetInfo.members` 直接使用 `factors` 原来的字段号 2，不保留 `reserved`。 |

## 5. 数据模型（SQLite）

`t_factor_sets`、`t_factor_recalc_jobs` 不变。`t_factor_defs` 去掉 `c_set_id`、`c_status` 与对应索引；新增成员表：

```sql
-- 因子定义：与因子集无关，无状态
CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_factor_type TEXT NOT NULL,
    c_source_code TEXT NOT NULL,
    c_source_hash TEXT NOT NULL,
    c_input_columns_json TEXT NOT NULL,
    c_outputs_json TEXT NOT NULL,
    c_params_json TEXT NOT NULL DEFAULT '{}',
    c_lookback_periods INTEGER NOT NULL,
    c_allow_partial_universe INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_factor_type IN ('timeseries', 'cross_section')),
    CHECK (c_lookback_periods >= 1),
    CHECK (c_allow_partial_universe IN (0, 1))
);

-- 成员：因子在某个因子集中的运行实例
CREATE TABLE IF NOT EXISTS t_factor_set_members (
    c_set_id TEXT NOT NULL,
    c_factor_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_set_id, c_factor_id),
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_set_id) REFERENCES t_factor_sets (c_set_id),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_t_factor_set_members_set ON t_factor_set_members (c_set_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_factor_set_members_factor ON t_factor_set_members (c_factor_id);
```

两张表都补 `c_mtime` 触发器（同现有规范）。

迁移（一次性，不保留双读）：重建 `t_factor_defs`；对旧表每一行生成一条成员 `(c_set_id, c_factor_id, c_status)`。仓库约定新项目无需向后兼容，已部署环境按此迁移一次后删除迁移代码；执行位置与步骤见第 10 节第 6 条。

## 6. 校验分层

### 6.1 创建 / 编辑定义（静态，不依赖数据集）

- `factor_id` 匹配 `[A-Za-z_][A-Za-z0-9_]*`，全局唯一；
- `factor_type ∈ {timeseries, cross_section}`；`lookback_periods ≥ 1`；`allow_partial_universe` 仅截面因子可为真；
- `input_columns` 非空、无重复；`outputs` 非空、无重复，且不含系统保留列（`subject_id`、`freq`、`data_time`、`series_tag`）；
- `params_json` 是 JSON object；
- 源码试 LOAD 成功（现有 `loadSource`）。

### 6.2 加入因子集（`AddFactorToSet`，依赖数据集）

- 因子集状态不是 `pending` / `deleting`；
- `input_columns ⊆ 该因子集源数据集的业务列`；
- 输出列不与源数据集业务列、保留列、同因子集**其他成员**的输出重名；
- 输出列不与结果数据集中已存在且 `origin_factor_id` 不同的列重名（D25，同一定义重新加入时复用原列）；
- 通过后写入一条 `disabled` 成员，不加列、不补算。

### 6.3 启用成员

重复 6.2 的全部校验（D27；定义可能被编辑、源数据集列可能已变化），再执行 D18 的「加列 → enabled → 自动补算」；加列和回填任务入库在同一个 SQLite 事务中完成（沿用现有 `EnableFactorWithRecalcJob` 的原子性）。

## 7. 协议

### 7.1 RPC 变更

| 分组 | RPC | 变更 |
| --- | --- | --- |
| 因子集 | `CreateFactorSet` / `UpdateFactorSet` / `SetFactorSetStatus` | 不变 |
| | `DeleteFactorSet` | 前置条件改为「无成员」（D23） |
| | `GetFactorSet` / `ListFactorSets` | 返回因子集、**成员**（含定义摘要与成员状态）、最近周期运行摘要 |
| 因子定义 | `CreateFactor` | **去掉 `set_id` 与 `status`**；只做 §6.1 校验 |
| | `UpdateFactor` | 规则 D20 |
| | `DeleteFactor` | 规则 D21（无成员引用） |
| | `GetFactor` | 返回定义 + 引用列表（`usages`） |
| | `ListFactors` | `set_id` 变为**可选**：缺省返回全部定义；指定时只返回该因子集的成员定义；`status` 过滤改为按成员状态（仅在指定 `set_id` 时有意义）；返回每个定义的 `usages`；新增 `include_source`（默认 false，D24） |
| 成员 | `AddFactorToSet` | 新增，§6.2 |
| | `RemoveFactorFromSet` | 新增，只允许 disabled 成员 |
| | `SetFactorMemberStatus` | 取代 `SetFactorStatus`：启用 → 返回自动提交的 `backfill_job`；停用 → 停止写入、保留列 |
| 补算 | `RecalcFactors` | `factor_ids` 必须是该因子集的 enabled 成员；缺省为全部 enabled 成员 |
| 运行 | `GetStatus` | 不变 |

### 7.2 proto 变更

```protobuf
message FactorDef {            // 去掉 set_id(2)、status(12)
  string factor_id = 1;
  string name = 3;
  string factor_type = 4;
  string source_code = 5;
  string source_hash = 6;
  repeated string input_columns = 7;
  repeated string outputs = 8;
  string params_json = 9;
  int32 lookback_periods = 10;
  bool allow_partial_universe = 11;
  string created_at = 13;
  string updated_at = 14;
}

message FactorUsage {          // 某个定义在某个因子集中的使用情况
  string set_id = 1;
  string status = 2;           // enabled | disabled
}

message FactorInfo {
  FactorDef factor = 1;
  repeated FactorUsage usages = 2;
}

message FactorMember {         // 因子集视角：成员 + 定义
  string set_id = 1;
  string factor_id = 2;
  string status = 3;           // enabled | disabled
  FactorDef factor = 4;
  string created_at = 5;
  string updated_at = 6;
}

message FactorSetInfo {
  FactorSet factor_set = 1;
  repeated FactorMember members = 2;     // 取代 repeated FactorDef factors
  SetRunSummary last_run = 3;
}

message AddFactorToSetReq { string set_id = 1; string factor_id = 2; }
message AddFactorToSetRsp { common.RetInfo ret_info = 1; FactorMember member = 2; }

message RemoveFactorFromSetReq { string set_id = 1; string factor_id = 2; }
message RemoveFactorFromSetRsp { common.RetInfo ret_info = 1; }

message SetFactorMemberStatusReq { string set_id = 1; string factor_id = 2; string status = 3; }
message SetFactorMemberStatusRsp {
  common.RetInfo ret_info = 1;
  FactorMember member = 2;
  RecalcJob backfill_job = 3;
}
```

`GetFactorSetRsp.factors` 同样改为 `repeated FactorMember members`；`ListFactorsRsp.factors` 改为 `repeated FactorInfo`；`SetFactorStatusReq/Rsp` 删除。

补充：`ListFactorsReq` 增加 `bool include_source`（D24）；`GetFactorRsp` 保持 `FactorDef factor`（始终带 `source_code`），新增 `repeated FactorUsage usages`，CLI 的内容比对与前端详情抽屉都据此取定义与使用情况；`CreateFactorReq` 去掉 `set_id`、`status`。

## 8. 对流水线设计的修订点

| 流水线设计章节 | 修订 |
| --- | --- |
| §2 已确认决策 | 增加 D16–D28 |
| §6 概念与命名 | 「因子定义」「因子集」描述按第 3 节改写；「因子输出在因子集内唯一」改为「在因子集内 enabled/已加入成员之间唯一」 |
| §7 数据模型 | `t_factor_defs` 与新增 `t_factor_set_members` 按第 5 节替换；删除「每个因子只属于一个因子集」注释 |
| §8.1 RPC 与 proto | 按第 7 节替换 |
| §10.2 规划 | 「开始时刻的 enabled 因子」→「enabled 成员对应的定义」；其余不变 |
| §13 生命周期 | 因子状态图改为成员状态图：`(无) ─Add→ disabled ─启用→ enabled ─停用→ disabled ─Remove→ (无)`；定义「无状态，无引用才能删除」；「创建因子 / 启用因子 / 修改因子」表格按 §6、D20 重写 |
| §14 补算 | 「默认全部 enabled」→「默认全部 enabled 成员」 |
| §19 Web / CLI | 因子页面改为「因子总览 / 因子定义 / 计算任务（计算任务 · 计算结果 · 补算）」；`moox.toml` 因子配置改为「因子定义」「因子集」「成员」三段 |

## 9. 实现影响（文件级，实施前对照代码核对）

| 位置 | 影响 |
| --- | --- |
| `modules/factor/schema/factor.sql` | 表结构按第 5 节；补迁移脚本 |
| `modules/factor/proto/factor.proto`、`factorgen/` | 按第 7.2 节修改并重新生成 |
| `internal/domain` | `FactorDef` 去掉 `SetID`、`Status`；新增 `FactorSetMember`；`ValidateFactor` 拆为 `ValidateDefinition`（静态）与 `ValidateMembership`（对数据集列与同集成员） |
| `internal/store` | `defs.go` 去掉 set 过滤与状态；新增 `members.go`（成员 CRUD、按因子集列 enabled 成员、按定义列引用、引用计数）；`sets.go` 的聚合查询带出成员 |
| `internal/catalog/service.go` | `CreateFactor` / `UpdateFactor` / `DeleteFactor` 改为定义级（见 D20、D21）；`SetFactorStatus` 拆成 `AddFactorToSet` / `RemoveFactorFromSet` / `SetFactorMemberStatus`；`ensureResultColumns` 的入参改为 enabled 成员的定义；`reconcile.go` 的列对齐与启动恢复按成员遍历 |
| 锁 | 成员操作仍取因子集级锁；`UpdateFactor` / `DeleteFactor` 跨因子集：先取定义级锁，再按 `set_id` 升序依次取所有引用因子集的锁，避免死锁 |
| `internal/pipeline`（plan） | 规划阶段读取因子集的 enabled 成员及其定义，其余计算逻辑不变；Python 请求帧、`source_hash` 路径不变（同一定义在多个因子集共用同一份物化源码） |
| `internal/recalc` | 启用回填入口 `PrepareEnableBackfill` 的入参从「因子」改为「成员」；`factor_ids` 校验改为 enabled 成员 |
| `internal/rpc` | 新增三个成员 RPC，调整 `ListFactors` 等的请求与响应 |
| `cmd/cli` | `init / import / import-catalog` 的导入格式支持「定义 + 因子集 + 成员」 |
| `modules/strategy/internal/factorio/client.go`（及 `client_test.go`） | 唯一直接读取 factor pb 字段的外部调用方：`ListFactors{SetId}` 的返回改为 `FactorInfo`，按请求的 `set_id` 取对应 `usage` 的 `status`，`SetID` 取请求值，其余取 `factor`；`verify_dependencies.go`、`trigger/processor.go` 等逻辑不变，仅 `FactorDescriptor` 来源变化（注释与 `modules/strategy/docs/coin-selection-runtime.md` 同步） |
| `modules/cli`（`internal/command/setup_factors.go`、`internal/setup/config/config.go`、`setup_init.go`、`setup.go` 及测试，`moox.toml.example`） | CLI 以原始 JSON 结构体解析响应、不引用 `factorgen`，字段变化**编译不会报错**，需要契约测试兜底。配置由 `[[factors.sets]]` + `[[factors.items]]`（items 带 `source_dataset_id / freq / status`）改为 `[[factors.sets]]` + `[[factors.definitions]]`（只含定义字段）+ `[[factors.members]]`（`source_dataset_id / freq / factor_id / status`）；Apply 流程改为：建因子集 → 建定义（`GetFactor` 比对内容，一致则跳过）→ 读 `ListFactors(set_id)` 的 usages，补 `AddFactorToSet` → 对目标为 enabled 的成员 `SetFactorMemberStatus`；旧键由 `md.Undecoded()` 报错，需给迁移提示；本机 `moox.toml`（已被 gitignore，11 条）手工迁移 |
| 网关白名单：`config/setup/service-deployments.yaml`（写 ACL 约 285 行、读路由约 290 行）、`modules/admin/internal/service/sysdeploy/defaults.go`（约 45 行）、`routes.go`、各自测试与 `period_gateway_contract_test.go` | 删除 `SetFactorStatus`，新增 `AddFactorToSet`（写）、`RemoveFactorFromSet`（写）、`SetFactorMemberStatus`（写）；YAML 与 `defaults.go` 必须同时改，已部署环境需重新种子，否则新方法 403 |
| `web/src/api/factor` 及页面层 | 类型与调用同步（前端设计文档第 6、8 节）；溢出点：`views/strategy/components/strategy-instance-create.vue`、`views/strategy/bindings.ts`、`views/home/home.vue` |
| 文档 | `docs/因子计算模块设计.md`、`modules/factor/README.md`、`docs/SUMMARY.md`、`docs/元数据命名规范.md`、`docs/架构总览.md`、`docs/策略执行框架设计.md`、`skills/moox/references/cli-operations.md`、`modules/cli/README.md`、`config/setup/README.md`、`modules/strategy/docs/coin-selection-runtime.md`；流水线设计 / 计划里已过期的 DDL 与 Task（见执行计划的文档任务） |

## 10. 取舍与风险

1. **编辑被多处引用的定义**：采用「所有引用先停用」。代价是要到各个因子集逐个停用；好处是不会在不知情的情况下改变正在运行的计算。备选是对定义做版本化、成员固定到某个 `source_hash`，复杂度明显更高，暂不做。
2. **同算法不同参数 / 不同回看周期仍要注册多个因子（D22）**：本设计解决的是「**同算法同参数**跨因子集复用」。若要「同一定义在 1m 用 200、在 1h 用 50」，需要成员级参数覆盖，会让定义的含义变得模糊，**推荐不做**；可在评审时再确认。
3. **输入列语义由使用者保证**：同名列在不同源数据集里含义可能不同（例如现货与永续的 `close`），加入因子集时只校验列存在。
4. **结果列保留与重新加入**：成员移除后结果列保留（D12）。已由 D25 解决：加入时校验与已存在列的归属，同一定义重新加入复用列；实施时仍需核对 `ensureResultColumns` 与 Storage `UpsertDatasetColumn` 对重复列的幂等行为。
5. **列表载荷**：已由 D24 解决（列表默认不带 `source_code`）。
6. **迁移**：一次性迁移，单库、数据量小；迁移后删除，不保留兼容读取路径。现状没有任何版本号或 ALTER 机制（`store.ApplySchema` 对列做精确校验，列不符直接失败），所以迁移必须在 `ApplySchema` 之前、事务内完成：检测到旧 `t_factor_defs` 仍有 `c_set_id` 时，先建 `t_factor_set_members` 并 `INSERT … SELECT c_set_id, c_factor_id, c_status`，再用「新表 → 拷贝 → 删旧 → 改名」重建 `t_factor_defs`（含索引与 mtime 触发器）。`factor_id` 原本全局唯一，不会产生冲突；执行前须停服务并备份 SQLite 文件；在途补算任务的 `factor_ids` 不受影响。若确认部署环境没有需要保留的数据，可以跳过迁移、直接重建库并用 CLI 重新导入。
7. **在途补算与成员变化**：已提交的补算任务在成员被停用后会因「not enabled in set」失败，沿用现状；`RemoveFactorFromSet` 只允许 disabled 成员，不额外检查在途任务。
8. **编辑定义使 `source_hash` 变化**：已冻结 `input_bindings_json` 的策略实例若引用了旧 hash，可能失效；实施 strategy 适配时核对冻结内容，若包含 `source_hash` 则在文档里写明「编辑因子需要重新校验依赖它的策略」。
9. **因子 ID 与物化文件名**：`factor_id` 与 Python 模块名（物化目录 `<factors_dir>/<name>/<source_hash>.py` 的 `name`）的关系在 D22 下需保持：`name` 仍取定义的模块名，不随成员变化；同一 `source_hash` 在多个因子集共用同一份物化文件。

## 11. 测试

- `domain`：`ValidateDefinition`（静态规则、保留列、重复、截面才允许 partial）与 `ValidateMembership`（输入列不在源数据集、输出与源列 / 同集成员重名）；
- `store`：成员唯一约束、`ON DELETE RESTRICT`、按定义列引用、按因子集列 enabled 成员、迁移脚本；
- `catalog`：创建定义无需因子集；加入 / 启用 / 停用 / 移除的状态机与错误；编辑被 enabled 成员引用时被拒、编辑时对所有引用因子集重新校验、删除有引用的定义被拒；启用时加列与回填任务原子入库；
- `pipeline`：同一定义同时加入 1m 与 1h 两个因子集，各自写入自己的结果数据集、互不影响；
- `recalc`：`factor_ids` 只接受 enabled 成员；
- `rpc` 契约与前端 `factor-contract.spec.ts`；
- e2e：新建定义 → 加入两个计算任务 → 分别启用并确认回填任务 → 停用 / 移除 / 删除。
