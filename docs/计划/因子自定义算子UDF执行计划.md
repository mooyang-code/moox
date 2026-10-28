# 因子自定义算子（wasm UDF）执行计划

> 状态：执行计划 v2.1（2026-10-28，尚未编码；已吸收两轮 Codex 审查，处置见第 14 节）。设计依据见[因子自定义算子（wasm UDF）设计](因子自定义算子UDF设计.md)（下称“设计”）。
> 约束：单用户、不考虑历史兼容、可全新部署、不过度设计；UDF 与用户因子的源码不进本仓库，仓库里只有 SDK、模板、示例与测试样本。

## 1. 总体顺序

```text
阶段 0 引擎暂停机制修复（独立，可先合入发布）──────────────────────────────┐
阶段 1 packages/factorudf（宿主侧：元数据、模块检查、加载调用、一致性测试）─┐   │
阶段 2 Rust SDK、模板、示例与测试模块 ─────────────────────────────────────┤   │
                                                                         ▼   │
阶段 3 factorexpr 接入（udf. 语法、回看、规范文本、错误通道）               │
                                                                         ▼   ▼
阶段 4 管理端（表、绑定、锁、发布、切换、补算绑定持久化）──► 阶段 5 引擎（模块就绪、实例池、执行、回放）
            │                                                          │
            ├──► 阶段 6 命令行（udf new/build/publish/preview/activate）│
            └──► 阶段 7 控制台                                        │
                                                                       ▼
                                   阶段 8 文档与 skill ──► 阶段 9 发布、验收与演练
```

- **阶段 0 不依赖 UDF**，修的是现有暂停恢复的缺陷（设计 10.3 前置改动），可以先单独合入、发布、演练。
- 阶段 1、2 互相独立，可以并行；阶段 1 的测试使用阶段 2 产出的测试模块（T2.4），两边按 T2.4 的约定对齐。
- 阶段 3～5 改动 factorexpr 与因子模块，**一起合入**：绑定、规范文本、目录快照与补算作业的格式同时变化，管理端与引擎要同版本发布。
- 阶段 6、7 依赖阶段 4 的接口，可以与阶段 5 并行。
- 每个阶段完成时：相关 `go test -count=1` 通过、`git diff --check` 干净、`make check-boundaries` 通过；涉及 Rust 时 `make test-rust` 通过；按仓库规范带路径提交。

## 2. 阶段 0：引擎暂停机制修复（设计 10.3 前置改动）

这些缺陷对普通的暂停恢复同样存在，与 UDF 无关，先修。因子引擎有**四条**写结果的路径：实时计算、补算作业、追赶推进、**回放写回**（回放会话的 `write_factors`，`engine/replay.go`）。设计 v2.3 只列了前三条，本计划补上第四条，设计同步修订。

| 任务 | 文件 | 改动 |
| --- | --- | --- |
| T0.1 收敛证据 | `packages/lifecycle/lifecycle.go`、`command.go`、`query.go`（对象转换） | 对象状态增加 `QuiescedAt`：`Execute` 在 `Quiesce` 成功返回后写入，进入其他阶段（恢复、追赶、重置）时清空；只有 `QuiescedAt` 不早于本次暂停开始（`Pause.Since`）才算本次已收敛。没有 `Quiesce` 钩子的对象暂停即视为收敛。持久化到现有状态文件 |
| T0.2 在途登记协议 | `modules/factor/internal/engine/lifecycle.go`、`runtime.go`；实时计算（`trigger/eventconsumer/handler.go` 的 `Advance` 及其任务队列）、补算执行（`recalcexec/executor.go`）、追赶推进（`catchup.go`）、回放写回（`replay.go`）的调用处 | 每次写结果的操作按“**先登记在途、再检查门禁**”执行：登记后门禁已关就注销并拒绝；进入任务串行队列的闭包在**真正执行前再检查一次门禁**；`Quiesce` 等待在途计数归零或超时。这样不存在“已通过门禁但尚未登记”的空隙 |
| T0.3 模块暂停时不领取、不续跑补算 | `modules/factor/internal/engine/recalcloop.go`（`runWhenOpen`）、`runtime.go`、`lifecycle.go` | 新增 `leaseAllowed()`：模块级生命周期允许写时才领取；在途的领取请求计入 T0.2 的在途计数。不修改共用的 `active()`（它还服务于消费者、预载与数据同步）。引入**暂停代次**（模块每次暂停加一）：补算的执行上下文记录领取时的代次，代次变化后不再写任何分块；`runWhenOpen` 遇到模块暂停时立即放弃当前作业、不在门禁重开后继续（现在它会在等待时限内持有原作业重试），作业只能在放行后重新领取，已取消的作业不会再被领取。任务级暂停仍按现有方式由分块门禁处理 |
| T0.4 回放写回并入门禁 | `modules/factor/internal/engine/replay.go` | 回放写回受模块与任务门禁约束并登记在途；暂停期间拒绝写回 |
| T0.5 追赶前置条件 | `modules/factor/internal/engine/catchup.go`、`catchup_life.go`、`lifecycle.go`（`catchupGate`、`writeGate`） | 追赶在推进水位、后台补算写结果之前调用“追赶前置条件”，不满足时本轮不写、记录原因；本阶段只接“本次恢复之后至少成功同步过一次目录”：恢复时清除同步标记并立即触发一次同步，同步返回“已应用”或“未变化”才置位。阶段 5 再加“引用模块全部就绪” |
| T0.6 上报 | `modules/factor/internal/domain/`（`EngineStatus`）、`modules/factor/internal/engine/runtime.go`（构造心跳）、`modules/factor/internal/factorwire/convert.go`、`modules/factor/proto/factor.proto`（`EngineRuntimeStatus`、`GetStatusRsp`，重新生成 `factorgen`）、`modules/factor/internal/enginehub/hub.go`、`modules/factor/internal/rpc/service.go`（`GetStatus` 组装） | 心跳上报模块生命周期阶段、本次暂停开始时间、`QuiescedAt`、追赶前置条件未满足的原因；`GetStatus` 展示 |

**测试**
- lifecycle：`Quiesce` 成功才写 `QuiescedAt`、超时不写、恢复后清空、重启后恢复；上一轮暂停的 `QuiescedAt` 不能证明本轮暂停已收敛；没有钩子的对象暂停即收敛；`query.go` 转换带出新字段。
- 在途协议：分别构造“已登记但正在计算”“已通过门禁但尚未登记”（应不可能出现）、“排队中尚未执行”“领取请求尚未返回”“回放正在写回”，暂停后 `Quiesce` 等它们结束或它们在执行前被拒绝；暂停期间不领取补算、拒绝回放写回。
- 补算代次：作业在 `runWhenOpen` 中被暂停挡住 → 管理端取消该作业 → 恢复，确认旧执行上下文不再写任何分块、已取消的作业不再被领取。
- 追赶：恢复后、同步完成之前没有任何写入；管理端不可达时追赶停住并上报原因。
- 上报往返：引擎心跳 → 管理端 → `GetStatus` 的字段完整。
- 现有暂停恢复用例（`catchup_life_test.go`、`lifecycle_test.go`）全部通过。

**验收**：`go test -count=1 ./packages/lifecycle/... ./modules/factor/...` 通过；proto 生成检查通过；线上演练见 11.3 第 1 项。

## 3. 阶段 1：`packages/factorudf`

新建共享包（加入 `go.work`），命令行、FactorMgr、FactorEngine 共用。依赖 wazero（版本锁定，写进 `go.mod`）。

| 任务 | 文件 | 内容 |
| --- | --- | --- |
| T1.1 元数据 | `manifest.go` | 自定义段 `moox.udf` 的结构与校验（设计 7.2）：名称正则、`abi`、`inputs`、`params`（int / float、范围）、`lookback`、`nan`、`min_valid`；`manifest.json` = 语义字段 + `doc`、`example`、仓库与提交号 |
| T1.2 回看公式 | `lookback.go` | 只解析常量或 `a*<整数参数>+b`（设计 4.2）；端点求值、`L ≥ 1`、不超过上限、溢出检查；返回 `Back = L − 1` |
| T1.3 模块检查 | `check.go` | 设计 8.2：用 wazero（只开 `CoreFeaturesV2`、打开自定义段）编译；无导入、无 start、恰好一个非共享内存、初始页数与表大小上限、导出签名逐一比对、ABI 版本、大小 ≤ 1.5 MiB；自定义段与 `manifest.json` 的语义字段一致 |
| T1.4 加载与调用 | `module.go`、`pool.go`、`call.go` | 编译缓存（`wazero.NewCompilationCacheWithDir`）；按模块哈希的实例池（借还、上限、空闲关闭）；实例化与每次 guest 调用都带截止时间（`WithCloseOnContextDone`）与实例内存上限；`reserve` / `eval` 的 ABI 封装（设计 7.3：对齐、偏移、参数按元数据顺序、每次调用后重新取内存视图、返回码）；失败统一返回带原因的错误（trap、超时、内存、返回码），出错的实例丢弃；输出 NaN 规范化 |
| T1.5 预算准入 | `pool.go` | 实例创建与线性内存增长前向调用方提供的预算接口申请（引擎接 `memadmit`，管理端接试算与发布各自的预算），超预算拒绝并返回错误；实例关闭时释放。不只登记 |
| T1.6 分批 | `batch.go` | 按 L 分组、按字节预算拆批（设计 11.2 的公式，溢出检查，结果为 0 报错） |
| T1.7 一致性测试 | `conformance.go` | 设计 8.3 全部测试：固定种子的合成数据、参数取端点与中间值；分块与只依赖窗口检查、批次组合与换序、未来数据扰动、回看外垃圾、空值（严格 / relaxed 共同有效段）、有限性、两实例确定性、fixtures 对拍；整套测试有总时限与内存上限；报告包含全部测试输出的规范化字节（小端 f64、NaN 已规范化）的 sha256、每输出点平均耗时与实例峰值内存 |
| T1.8 包格式 | `pkg.go` | `.udfpkg`（tar）读写：`module.wasm`、`manifest.json`、`fixtures/`（≤ 256 KiB）、`report.json`；整包 ≤ 2 MiB |

**测试**（测试模块来自 T2.4，放在 `packages/factorudf/testdata/`）
- 合格模块全部检查通过、一致性测试通过；每一类不合格模块被准确拒绝并给出原因：有导入、有 start 函数、用了线程特性、共享内存、导出签名不对、ABI 版本不对、回看公式不合规、自定义段与 manifest 不一致、超大。
- 执行时限制：死循环模块在截止时间后被中断、实例被丢弃、下一次调用用新实例；内存失控模块触发上限；panic 模块返回 trap 错误。
- 预算：多个模块的实例并发创建与复用时，总量不超过预算；关闭后释放。
- 一致性测试能抓到问题：带全局状态的模块（只依赖窗口的检查失败）、读窗口外的模块（回看失败）。
- 分批：L 不同不混批；预算边界；单条放不下报错。
- 确定性：同一个 wasm 的测试输出摘要在 CI（amd64）与开发机（arm64）上相同（作为回归检查，见 T2.5）。

**验收**：`go test -count=1 ./packages/factorudf/...` 通过；`make check-boundaries` 允许 `packages/factorudf` 被 `modules/factor`、`modules/cli` 引用。

## 4. 阶段 2：Rust SDK、模板、示例与测试模块

| 任务 | 位置 | 内容 |
| --- | --- | --- |
| T2.1 SDK 运行时 | `sdk/rust/moox-udf/` | `Window`（越界 panic）、空值规则（严格 / relaxed 共同有效段、`min_valid`）、有限性转换、`reserve` / `eval` 的实现、`testing` 模块（原生跑 fixtures） |
| T2.2 过程宏 | `sdk/rust/moox-udf-macros/` | `#[udf(...)]`：校验名称、参数、回看公式形式；生成参数结构体、逐点循环与导出；写自定义段 `moox.udf` |
| T2.3 模板 | `modules/cli/internal/command/udftemplate/`（权威位置，供 `go:embed`；`go:embed` 不能引用 Go 包目录之外的文件） | 设计 6.1 的骨架：`rust-toolchain.toml`（锁定版本与 wasm 目标）、`clippy.toml`（设计 6.3 的禁用清单）、workspace `Cargo.toml`（release 配置）、`AGENTS.md`、一个 crate 的样板与 fixtures 样板。SDK 依赖写成 git 依赖：仓库地址 + 标签（`udf-sdk-v<版本>`）+ 对应提交，SDK 发布时打标签 |
| T2.4 示例与测试模块 | `sdk/rust/examples/`、`packages/factorudf/testdata/` | 合格示例：用 UDF 重新实现 `ts_mean` 与截断 EMA（与 factorexpr 逐位一致，作为验收基准）、一个两输入 relaxed 示例、一个“输入等于约定哨兵值时才死循环 / panic / 申请大内存”的故障示例（能通过门禁，用于线上故障演练）；不合格样本：死循环、内存失控、panic、全局状态、读窗口外；需要绕过 SDK 的（有导入、有 start、线程特性、错误签名、错误 ABI）直接用 Rust 不经 SDK 写成。`make udf-testdata` 重新构建并复制到 testdata |
| T2.5 构建与 CI | `Makefile`、`.github/workflows/ci.yml` | `make test-rust`：`cargo clippy --locked -- -D warnings`、`cargo test`（SDK）、构建示例、`make udf-testdata` 后 `git diff --exit-code packages/factorudf/testdata`、`packages/factorudf` 的跨架构摘要检查；并入 `make test` / `make verify`。CI 新增 `verify-rust` 任务（安装锁定版本的工具链与 wasm 目标），加入 `verify-pr` 的必过依赖；所有非纯文档变更都运行，不另维护路径清单。命令行的 `new → build` 集成测试（T6.8）也在这个任务里跑 |

**测试**
- 宏的编译期错误（`trybuild`）：名称不合法、回看公式不合规、参数名不一致。
- 原生测试：空值规则（严格、relaxed、多输入不同前缀、全空、`min_valid`）、`Window` 越界 panic、有限性转换。
- 示例 UDF 的 fixtures 在原生与 wasm（经阶段 1 的宿主）下都通过。

**验收**：`make test-rust` 与 CI 的 `verify-rust` 通过；testdata 与源码一致。

## 5. 阶段 3：factorexpr 接入

| 任务 | 文件 | 改动 |
| --- | --- | --- |
| T3.1 语法 | `packages/factorexpr/syntax.go`、`check.go`（`lowerCall`） | 语法白名单与 `lowerCall` 额外接受“调用对象是成员访问、左侧标识符 `udf`、右侧名称”（设计 4.1）；其余成员访问仍拒绝，报错给出可用 UDF |
| T3.2 解析环境 | `registry.go`、`program.go`、新增 `udf.go` | 编译入口增加 UDF 解析环境（名称 → `{哈希, 元数据, 说明}`），由调用方传入，factorexpr 不自行查找；没有环境时 `udf.` 调用报错；`FunctionDocs()` 可附带环境中的 UDF（类别 `timeseries`，签名带 `udf.` 前缀） |
| T3.3 节点与回看 | `ir.go`、`check.go`（`worstBackShift`）、`program.go` | 新节点类型 `udf`：序列参数在前、参数按元数据声明顺序映射到 `Ints` / `Floats` 并记录映射；`Back = L − 1`、`Shift = 0`，最坏情况按参数端点；参数槽照常支持 |
| T3.4 规范文本与去重 | `check.go`（`CanonicalText`）、`ir.go`（`writeCanonical`）、`program.go`（`programKey`）、`exec/run.go`（`runtimeKey`） | `writeCanonical` 把 UDF 节点写成 `udf.<名称>@<哈希>(...)`（`definition_hash` 用的就是这份文本）；编译期去重与运行时去重的键包含哈希 |
| T3.5 执行与错误通道 | `exec/run.go`、`ops/ops.go`、新增 `exec/udf.go` | UDF 节点使用新的内核类型（可返回错误），通过宿主接口调用（由 `modules/factor` 注入 `packages/factorudf` 的实现，factorexpr 不依赖 wazero）；内核出错时 `Run` 返回错误，不写 NaN（设计 11.4）；内置内核不变 |
| T3.6 内存估算 | `exec` 的内存估算 | 计入每次调用的拷贝缓冲（设计 11.3） |

**测试**
- 语法：`udf.x(...)` 接受，`a.b`、`udf.x.y`、`foo.x(...)` 拒绝。
- 回看：`udf` 节点叠加在输入表达式回看之上；参数槽取不同值时需求区间正确；与 `actualExtent` 一致。
- 规范文本、定义哈希、编译期去重、运行时去重**分别**测试：同一 spec 在不同绑定下规范文本不同，四者都随哈希区分。
- 执行：用 T2.4 的 `ts_mean` / EMA 示例，在面板上与内置 `ts_mean` / `ema` **逐位一致**（含开头 NaN、分块、多 worker）；故障示例让 `Run` 返回错误而不是 NaN；`fill_nan(udf...)` 与 `cs_rank(udf...)` 在 UDF 失败时整体返回错误，不被吞成空值。

**验收**：`go test -count=1 ./packages/factorexpr/...` 通过。

## 6. 阶段 4：管理端

| 任务 | 文件 | 改动 |
| --- | --- | --- |
| T4.1 表 | `modules/factor/schema/factor.sql`、`modules/factor/internal/store/database.go`（严格表结构校验的期望表与列） | 设计 10.1 的两张表；新表 `t_factor_recalc_job_bindings`（`c_job_id` 主键、外键指向 `t_factor_recalc_jobs`，`c_bindings` 为首次领取时固化的绑定 JSON）。**不改动任何已有表**：现有的 `ApplySchema` 会用 `CREATE TABLE IF NOT EXISTS` 补建新表，但要求已有表的结构完全一致，所以加列会让新 FactorMgr 在现有库上启动失败。按仓库 schema 格式规范；schema 文件能载入空 SQLite；新表加入严格校验的期望列表 |
| T4.2 重置分类 | `modules/factor/internal/bootstrap/reset.go`、`reset_test.go` | 两张 UDF 表归入配置表（`mgrConfigTables`）：`data` 重置保留，`all` 重置清除；删除顺序先版本表、后名称表（外键）。`t_factor_recalc_job_bindings` 归入数据表（`mgrDataTables`），随补算作业一起清除，先于 `t_factor_recalc_jobs` 删除 |
| T4.3 存储 | `modules/factor/internal/store/`（新增 `udfs.go`） | 名称与版本的读写；`c_active_hash` 的事务内校验；被引用时不可清空 |
| T4.4 绑定与编译顺序 | `modules/factor/internal/store/defs.go`、`members.go`（读取时编译的地方）、`modules/factor/internal/domain/types.go`、`naming.go`、`validation.go`、`runtime.go` | 读取定义的顺序改为：读原文 → 按启用版本解析绑定（同一事务读 `t_factor_udfs`）→ 带绑定编译。`FactorDef` 增加 `UdfBindings`；编译结果记录所用绑定，`CompileDefinition` 的“已编译就直接返回”改为“已编译且绑定相同才返回”；`DefinitionHash` 的备用值含绑定（设计 4.3、4.4） |
| T4.5 流水线编译 | `modules/factor/internal/pipeline/compute.go` | 同样的提前返回改为比较绑定；缓存键加上绑定 |
| T4.6 引用关系 | `modules/factor/internal/store/`、`catalog/service.go` | 定义 → UDF 名称的引用（由编译结果给出）；`ListUdfs` / `GetUdf` 的引用关系 |
| T4.7 锁与发布 | `modules/factor/internal/catalog/`（新增 `udfs.go`）、`service.go` | 全局 UDF 锁（设计 10.3）：`PublishUdf`、`ActivateUdf` 以及新旧原文任一方引用 UDF 的 `CreateFactor`、`UpdateFactor` 先取它，再按现有顺序取定义锁、任务锁；`PublishUdf` 调 `factorudf` 复核模块、重跑一致性测试并比对摘要（发布测试串行执行，独立的时限与内存上限），重复哈希只更新说明 |
| T4.8 切换 | `catalog/udfs.go` | 设计 10.3 的步骤：影响清单；前提一（因子引擎模块已暂停且本次暂停已收敛，读 T0.6 的上报，提交前复查）；前提二（受影响任务没有已领取且未结束的补算作业）；用新版本重新编译引用定义并按 `UpdateFactor` 规则做成员校验；报告新旧 `definition_hash`、回看、输入列、保留期检查与引擎侧配置提示；`dry_run` |
| T4.9 补算绑定固化 | `modules/factor/internal/store/recalc_jobs.go`（`PullRecalcJob` 事务）、`modules/factor/internal/enginehub/jobs.go`（`Hub.Pull`） | 首次领取时在同一事务里把当时的绑定写入 `t_factor_recalc_job_bindings`；租约过期重新领取、引擎或管理端重启后续跑，都用已固化的绑定编译下发的定义，不再按当前启用版本解析 |
| T4.10 试算与校验 | `modules/factor/internal/rpc/expr.go`、`modules/factor/internal/preview/` | `ValidateFactorSpec`、`PreviewFactor` 接受 `udf_versions`（请求级覆盖）；试算在 mgr 内用 `factorudf` 执行，沿用现有 256 MiB 上限并接入 T1.5 的预算；`ListFactorFunctions` 经 `FunctionDocs()` 附带已启用 UDF 与说明 |
| T4.11 接口 | `modules/factor/proto/factor.proto`（重新生成 `factorgen`）、`modules/factor/internal/rpc/` | 设计 10.2：`PublishUdf`、`ListUdfs`、`GetUdf`、`ActivateUdf`、`GetUdfModule`；`ValidateFactorSpec` / `PreviewFactor` 的 `udf_versions`；`EngineRuntimeStatus` 的模块状态 |
| T4.12 目录下发 | `modules/factor/internal/enginehub/snapshot.go`、`modules/factor/internal/factorwire/convert.go` | 目录快照携带绑定与被引用模块的元数据，`CatalogHash` 包含它们；补算领取下发的定义携带固化的绑定；`factorwire` 双向转换 |
| T4.13 服务目录 | `packages/servicecatalog/catalog.yaml` | 新方法加入方法列表、只读方法列表、控制台路由、管理接口的调用方 ACL；`GetUdfModule` 加入 `principals.factor-engine.allow` |

**测试**
- schema 与严格校验：新表与新列在期望列表里；schema 载入空库；重置分类测试覆盖两张新表（`data` 保留、`all` 清除、删除顺序）。
- 绑定：同一定义在不同启用版本下 `definition_hash` 不同；编译失败时备用哈希不同；**已编译的定义复制后只替换绑定**会重新编译（domain 与流水线两处）；请求级覆盖不影响其他请求。
- 锁：并发的 `UpdateFactor`（新增对 UDF 的引用）与 `ActivateUdf` 串行化，切换看到的引用清单在提交前不变。
- 发布：合格包入库、不合格包拒绝；摘要不一致拒绝；重复哈希只更新说明；包与请求大小上限；发布测试超时与内存上限生效。
- 切换：引擎未暂停、未收敛或收敛证据属于上一轮暂停时拒绝；有已领取的补算时拒绝；新版本编译失败或成员参数越界时拒绝；`dry_run` 不改数据；切换后目录快照哈希变化。
- 补算绑定：首次领取固化；租约过期重领、领取响应丢失后重领、管理端重启后续跑都用固化的绑定；切换后新提交的作业领取时固化新绑定。
- 服务目录测试、Access 与网关的 ACL 测试通过。

**验收**：`go test -count=1 ./modules/factor/... ./packages/servicecatalog/... ./modules/access/...` 通过；proto 生成检查通过。

## 7. 阶段 5：引擎

| 任务 | 文件 | 改动 |
| --- | --- | --- |
| T5.1 模块存储 | 新增 `modules/factor/internal/engine/udfmodules.go`、`managerclient.go` | 按内容寻址的本地目录 `<数据目录>/udf/<哈希>.wasm`；`GetUdfModule` 拉取、校验 sha256；编译缓存目录 `<数据目录>/udf/cache/`；模块状态（就绪 / 拉取失败 / 编译失败及原因） |
| T5.2 三个入口 | `catalog.go`（`Apply`、`Load`）、`synctimer.go`、`recalcloop.go` | 共用“按绑定准备模块”（设计 11.1）：目录同步时全部就绪才应用、失败不应用并退避重试；启动恢复时缺失模块按退避拉取，未就绪的任务记为不可执行；补算领取后先准备，失败则作业报告失败、不写分块 |
| T5.3 追赶前置条件 | `catchup.go`、`lifecycle.go` | 在 T0.5 的条件上加“引用模块全部就绪”：本次恢复后同步成功且模块就绪，追赶才写 |
| T5.4 回放会话 | `replay.go` | 会话建立时记录所用绑定；写回时若与引擎当前目录的绑定不同则拒绝，要求重新打开会话 |
| T5.5 执行接入 | `pipeline/compute.go`、`recalcexec/`、引擎装配 | 把 `factorudf` 的调用实现注入 factorexpr（T3.5）；实例池按模块哈希、上限 `compute.workers`、空闲关闭；UDF 调用失败按本次运行全部成员失败处理（现有路径） |
| T5.6 配置与预算 | `config.go`、`memplan.go`、`modules/factor/internal/memadmit/`、`localdata/` | 新增可选配置段 `udf`（调用截止时间、实例内存上限、批字节上限、空闲关闭时间，均有默认值）；实例创建与内存增长经 `memadmit` 准入（超预算拒绝，不只登记），关闭时释放；调用缓冲计入 `compute.max_memory` |
| T5.7 上报与指标 | `heartbeat.go`、`observability/` | 心跳上报各模块哈希的状态；指标 `factor_udf_call_seconds`、`factor_udf_failures_total`、`factor_udf_copy_bytes_total`、`factor_udf_module_state` |

**测试**
- 模块存储：拉取、校验失败（哈希不符）、本地命中、编译缓存命中。
- 目录：模块拉取失败时不应用新快照、哈希不记、下次同步重试；就绪后应用；启动恢复时模块缺失的任务不可执行、就绪后恢复。
- 补算：领取后模块准备失败，作业失败且没有写分块；续跑使用固化的绑定。
- 追赶：暂停 → 切换 → 立即恢复，在定时同步尚未运行、管理端暂时不可达、模块尚未就绪三种情况下**都没有任何旧版本写入**；就绪后用新版本补算暂停期间的周期再放行。
- 回放：跨“暂停、切换、恢复”的旧会话写回被拒绝；重新打开后可写。
- 执行：示例 UDF 的因子与内置等价因子逐位一致；故障示例让本次运行失败、引擎进程不受影响、其他任务照常；实例池上限、空闲关闭、多模块并发下的预算准入。

**验收**：`go test -count=1 ./modules/factor/...` 通过；`run-once` 能计算引用示例 UDF 的因子。

## 8. 阶段 6：命令行

| 任务 | 文件 | 内容 |
| --- | --- | --- |
| T6.1 `udf new` | `modules/cli/internal/command/udf.go`（新增），模板见 T2.3 | 空目录生成 workspace，已有 workspace 只添加 crate；生成后执行 `cargo generate-lockfile` 得到 `Cargo.lock`（含锁定版本的 SDK） |
| T6.2 `udf build` | 同上 | 执行 `cargo clippy --locked -- -D warnings` 与 `cargo build --release --locked --target wasm32-unknown-unknown`；调用 `factorudf` 做模块检查与一致性测试；合成 `manifest.json`（自定义段 + `udf.toml` + 仓库与提交号）；打包 `dist/<名称>-<sha256>.udfpkg`；输出机器可读的报告 |
| T6.3 `udf publish` | 同上，经 `gateway_call.go` 调用 | 要求私有仓库工作区干净；检查完整序列化后的请求体大小；调 `PublishUdf` |
| T6.4 `udf preview` | 同上 | 调 `PreviewFactor`（带 `udf_versions`） |
| T6.5 `udf activate` | 同上 | `--dry-run` 展示影响清单与前提检查；正式执行前提示 11.3 第 4 项的步骤 |
| T6.6 `udf list` / `show` | 同上 | 版本、启用状态、元数据、服务端报告、提交号、引用关系、各引擎的模块状态 |
| T6.7 根命令登记 | `root.go` | 登记 `udf` 子命令组 |
| T6.8 集成测试 | `modules/cli/` 下的集成测试，在 `verify-rust` 任务中运行 | 在独立的空目录（不依赖 moox 源码树）执行 `udf new → build`，产出的包能通过 `factorudf` 的检查 |

**测试**：publish / preview / activate 用模拟网关验证请求体；工作区不干净时拒绝；超大请求在本地拒绝；T6.8。

**验收**：`go test -count=1 ./modules/cli/...` 通过；`verify-rust` 中的 T6.8 通过。

## 9. 阶段 7：控制台

| 文件 | 内容 |
| --- | --- |
| `web/src/api/factor/` | UDF 接口封装 |
| `web/src/views/factor/udfs/`（新增） | 列表（名称、启用版本、更新时间、被引用次数）；详情（元数据、版本、服务端报告、提交号、引用关系、各引擎模块状态）；切换版本（先 `dry_run` 展示影响清单与前提检查结果） |
| `web/src/router/`、`static-menu.ts`、中英文标题资源 | 路由与菜单（页面 meta 写在 `static-menu.ts`，菜单路由会覆盖 `route.ts` 的 meta） |
| `web/src/views/factor/editor/` | 第一期沿用现有的“函数参考列表 + 点击插入”：确认 UDF 以 `timeseries` 类别、带 `udf.` 的签名出现在列表里（分组目前固定为三个类别，按需调整）；不做输入时的自动补全 |

**测试**：组件与接口的单元测试；类型检查与 lint 通过。按现有方式在编译机构建前端。

## 10. 阶段 8：文档与 skill

- [因子](../模块/因子.md)：UDF 的写法、语义、发布与切换步骤、失败语义、配置项；
- [命令行工具](../模块/命令行工具.md)：`moox-cli udf` 子命令；
- `skills/moox/references/` 新增 UDF 一节（发布流程、切换步骤、给 Agent 的编写规范摘要），`factor-dsl.md` 补 `udf.` 调用；
- 设计文档状态改为“已实现”，记录与设计的差异；`docs/计划/README.md` 更新。

## 11. 阶段 9：发布、验收与演练

### 11.1 发布范围

- **阶段 0**：只发布行为变化的组件——因子引擎（门禁、收敛、上报）与 FactorMgr（`GetStatus` 展示新字段）。`packages/lifecycle` 是静态编入的共享包，其他模块不改跨模块协议，不必随之发布，但它们的现有暂停恢复测试必须通过；
- **阶段 1～8 一起发布**：服务目录变化，内嵌目录的组件必须用同一提交构建并一起发布：Admin、control 主机网关、Monitor、Access，以及 FactorMgr、FactorEngine、命令行、web。

### 11.2 发布顺序（停机发布）

FactorMgr 的表结构校验是严格的（遇到不认识的表或列拒绝启动），引擎配置也是严格模式（`KnownFields(true)`），所以新旧版本不能混跑：

1. 暂停因子引擎模块并确认本次暂停已收敛，然后停止 FactorEngine 与 FactorMgr；
2. 停止之后再做发布前快照：`factor.db` 用 `sqlite3 factor.db ".backup <备份文件>"` 生成一致副本（数据库是 WAL 模式，直接复制主文件可能漏掉已提交的数据），连同 `engine.yaml` 与引擎本地状态目录一起保存，并在另一处试恢复一次确认可用；
3. 按服务目录的同发要求替换全部组件的二进制；
4. 启动新 FactorMgr：现有的 `ApplySchema` 补建三张新表（已有表不变）；
5. 启动新 FactorEngine（不加 `udf` 配置段也能运行，需要调整默认值时再加）；
6. 恢复因子引擎模块；检查 `GetStatus` 中引擎的生命周期与模块状态字段正常、现有因子任务照常出期。

### 11.3 验收与演练

1. **暂停机制（阶段 0 发布后即做）**：模块级暂停 → 心跳出现本次的 `QuiescedAt` → 暂停期间不领取补算、拒绝回放写回、不写结果 → 恢复后同步完成之前追赶不写 → 放行；构造一个长补算分块与一次正在进行的实时计算，确认被等待结束。
2. **逐位一致**：用 T2.4 的 `ts_mean`、EMA 示例 UDF 建两个因子定义，与内置 `ts_mean`、`ema` 的等价定义放进同一个测试任务，在真实数据上连续运行至少 100 期，结果逐位一致。
3. **发布链路**：在空目录 `udf new → build → publish → preview → activate`（首次启用）走通；不合格样本在 `build` 与 `publish` 都被拒绝。
4. **切换演练**：发布示例 UDF 的第二个版本 → `activate --dry-run` → 暂停因子引擎模块（等收敛）→ 取消列出的已领取补算 → `activate` → 恢复（追赶：同步、准备模块、用新版本补算暂停期间的周期、放行）→ 相关策略实例进入 `factor_changed`、重新启用后正常；再切回旧版本（回滚）。
5. **模块准备失败**：切换后让引擎无法拉取模块（例如临时屏蔽 `GetUdfModule`），确认引擎停在追赶、不写任何结果、上报原因；恢复后自动完成。
6. **故障隔离**：把 T2.4 的故障示例（能通过门禁、遇到哨兵值才出错）接入一个测试任务，向其输入注入哨兵值，确认只有该任务这一期运行失败、失败状态如实上报、没有写出 NaN 结果、引擎进程与其他任务不受影响。
7. **跨架构**：本地（arm64）`build` 的包在服务端（amd64）发布时摘要一致。

### 11.4 回滚

- **UDF 版本问题**：`activate` 指回旧版本（按 11.3 第 4 项的步骤）；
- **系统问题**：停机，用 11.2 第 2 步的快照替换 `factor.db`（同时删除旧的 `-wal`、`-shm` 文件）、`engine.yaml` 与引擎本地状态目录，回退同一批组件的二进制。旧 FactorMgr 会拒绝新表，所以必须恢复快照，不能只回退二进制。不做兼容层；按项目原则也可以直接全新部署；
- 阶段 0 的回滚独立进行：回退因子引擎与 FactorMgr 的二进制；生命周期状态文件里新增的字段，旧版本按 JSON 的默认行为忽略（T0.1 的测试覆盖）。

## 12. 风险与应对

| 风险 | 应对 |
| --- | --- |
| wazero 编译模式在某个架构上的行为差异 | 锁定 wazero 版本；跨架构摘要比对（T1.7、11.3 第 7 项）；升级 wazero 时用全部已发布 UDF 的一致性测试与示例对拍做回归，不必视为 ABI 变化 |
| Rust 工具链升级导致同一源码产出不同 wasm | 工具链版本锁在模板里；升级后重新 `build` 得到新版本，按切换步骤启用 |
| 实例常驻内存挤占引擎预算 | 实例创建经 `memadmit` 准入、空闲关闭、池上限；`udf` 配置段可调 |
| 长回看 UDF 的拷贝开销 | 第一期接受；回看上限与批字节上限控制；需要时再做历史常驻（设计第 14 节） |
| 切换步骤较多、容易漏步 | `activate --dry-run` 给出前提检查结果；skill 写明步骤；是否做一键选项见设计第 15 节 |
| 阶段 0 改动 `packages/lifecycle` 影响多个模块 | 只增加字段与等待，不改变现有阶段转换；各模块现有暂停恢复测试全部通过后再合入 |

## 13. 进度

| 阶段 | 状态 |
| --- | --- |
| 0 引擎暂停机制修复 | 未开始 |
| 1 `packages/factorudf` | 未开始 |
| 2 Rust SDK、模板、示例与测试模块 | 未开始 |
| 3 factorexpr 接入 | 未开始 |
| 4 管理端 | 未开始 |
| 5 引擎 | 未开始 |
| 6 命令行 | 未开始 |
| 7 控制台 | 未开始 |
| 8 文档与 skill | 未开始 |
| 9 发布、验收与演练 | 未开始 |

## 14. 审查记录

**第一轮 Codex 审查（2026-10-28）**，共 16 条，处置如下：

| 序号 | 意见 | 处置 |
| --- | --- | --- |
| 1 | 遗漏回放写回路径，回放会话可用建立时的定义直接写回 | 采纳：第四条写结果路径并入门禁与在途收敛（T0.2、T0.4），会话绑定与当前目录不一致时拒绝写回（T5.4）；设计同步修订 |
| 2 | 只规定等待计数归零，缺少防止新操作进入的并发协议 | 采纳：“先登记在途、再检查门禁”，排队闭包执行前再检查（T0.2） |
| 3 | `CatalogInSync` 不是引擎现有状态，且需要本次恢复后的新鲜确认 | 采纳：恢复时清除同步标记并立即同步，本次同步成功才允许追赶写（T0.5、T5.3）；设计同步修订 |
| 4 | 补算绑定只下发、不持久化，重领与重启续跑没有落点 | 采纳：首次领取固化、重领复用（T4.1、T4.9；第二轮改为放在新表 `t_factor_recalc_job_bindings`） |
| 5 | 新表与新配置段让旧版本无法启动，回滚步骤错误 | 采纳：停机发布、由新 FactorMgr 建表，回滚恢复发布前快照（11.2、11.4） |
| 6 | 阶段 0 的状态链路文件不全，协议类型名写错 | 采纳：补 `query.go`、`domain`、`runtime.go`、`GetStatus` 组装、`EngineRuntimeStatus`、`GetStatusRsp` 与 proto 生成；测试“上一轮收敛不能证明本轮”（T0.1、T0.6） |
| 7 | 领取前无法判断任务门禁，`active()` 是共用的 | 采纳：缩小为模块级暂停时不领取，新增 `leaseAllowed()`，不改 `active()`（T0.3） |
| 8 | 管理端绑定解析与编译缓存失效的落点不准 | 采纳：改 store 读取路径的顺序，“已编译且绑定相同才复用”，补“只替换绑定”的测试（T4.4、T4.5） |
| 9 | 规范文本在 `check.go` / `ir.go`，函数文档入口是 `FunctionDocs()` | 采纳：修正文件与符号，四种身份分别测试（T3.2、T3.4） |
| 10 | 新表遗漏重置分类 | 采纳：T4.2 |
| 11 | `go:embed` 不能引用包外模板；clippy 命令缺 `--`；SDK 来源与锁文件未安排 | 采纳：模板放在命令行包内，SDK 用 git 标签依赖，`udf new` 生成锁文件，修正命令，空目录集成测试（T2.3、T6.1、T6.2、T6.8） |
| 12 | Rust 测试没有成为必过门禁 | 采纳：`make test-rust` 并入 `make test` / `verify`，`verify-rust` 进入 `verify-pr` 必过依赖（T2.5） |
| 13 | 资源缺少总量限制，`SetResident` 只登记不拒绝 | 采纳：预算准入与释放、发布测试独立限额、资源报告（T1.5、T1.7、T4.7、T5.6） |
| 14 | 故障演练与发布门禁冲突；缺少 `fill_nan` / `cs_rank` 的失败传播测试 | 采纳：用“遇到哨兵值才出错”的故障示例（T2.4、11.3 第 6 项）；补传播测试（阶段 3） |
| 15 | 依赖 NaN 位模式的样本不一定在两种架构上表现不同 | 采纳：去掉这个反例；跨架构摘要比对只作回归检查，输出按规范字节计算（T1.7） |
| 16 | 编辑器没有函数补全，“无需特殊处理”不成立 | 采纳：第一期沿用参考列表加点击插入，确认类别与签名；菜单补中英文标题（阶段 7） |

可删减项全部采纳：阶段 0 只发布行为变化的组件；去掉手工建表步骤；wazero 升级不视为 ABI 变化；Rust 检查对所有非文档变更运行，不维护路径清单。

**第二轮 Codex 复审（2026-10-28）**，提出 3 条：

| 序号 | 意见 | 处置 |
| --- | --- | --- |
| 1 | `runWhenOpen` 被门禁挡住时会持有原作业重试，取消状态要到分块上报才检查，旧作业可能在恢复后用旧绑定继续写 | 采纳：暂停代次使旧执行上下文失效，模块暂停时立即放弃作业、放行后只能重新领取（T0.3） |
| 2 | 现有 `ApplySchema` 只补建新表、不给已有表加列，新列会让新 FactorMgr 在现有库上启动失败 | 采纳：补算绑定改放新表 `t_factor_recalc_job_bindings`，不改已有表（T4.1、T4.2、T4.9、11.2） |
| 3 | 备份在停机之前做，且数据库是 WAL 模式，备份可能不一致 | 采纳：先暂停收敛、停止进程，再用 `sqlite3 .backup` 做一致副本并试恢复；回滚时连同 `-wal`、`-shm` 一起处理（11.2、11.4） |
