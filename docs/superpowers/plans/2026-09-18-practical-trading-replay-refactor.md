# MooX 个人量化系统改造执行计划：交易规则、决策证据与轻量回放

> **For agentic workers:** 实施时使用 `superpowers:executing-plans` 按任务推进；存在独立任务时可使用 `superpowers:subagent-driven-development`。代码审查必须使用 `codeCR`，等待所有审查结束后由主 Agent 独立核验。任务使用 `- [ ]` 跟踪。本次仅交付计划文档，不授权编码、部署、服务启停或交易。

**Goal:** 在不增加常驻服务、不替换现有架构的前提下，统一交易规则与快照契约，让一次策略决策可解释、可重放，并为受限的离线 Bar 级成交模拟建立最小基础。

**Architecture:** 保留 Go 多模块、SQLite、JetStream、Storage/Factor/Strategy/Trade 分工。优先完成 Trade 局部规则收敛和 Strategy 决策工件；离线计算通过现有模块 CLI 与文件衔接，不新建通用 Kernel、Actor 框架或分布式回测平台。

**Tech Stack:** Go、现有精确 Decimal、SQLite/GORM、标准库 JSON/gzip/SHA-256、既有策略 DSL 求值器、Archive Parquet、现有 Paper adapter/Reducer。

---

## 1. 执行边界与结论

### 1.1 本文状态

- 日期：2026-09-18。
- MooX 阅读基线：`feature/mooyang@17fdbc3714`。
- 参考项目：`/Users/mooyang/Documents/go/src/github.com/nautilus_trader`，`develop@f6ff54ffe3`，Rust-native v2。
- 本轮只有计划文档变更；文中的新增文件、接口、命令和测试均为实施目标，不代表已经存在或已经通过。
- 所有实施复选框保持未勾选。批准文档不等于批准生产交易；生产验证必须沿用真实环境的独立授权边界。
- 计划采用分阶段验收，不将全部阶段打包成一次大重构。完成 A、B 即可形成一个有实际价值的交付版本。

### 1.2 个人量化约束

1. 只解决已识别的规则重复、接口歧义和决策不可复现问题，不为可能的未来需求建设平台。
2. 单用户、单实例；不增加 HA、分布式锁、Saga、DLQ、全局 exactly-once 或持久化任务调度器。
3. 不改写为 Rust，不嵌入 Nautilus，不复制其完整 Instrument 类型树、事件溯源、Actor 或 MessageBus。
4. 复用 `Strategy → FULL target → Trade`，策略不感知订单、成交和实际账户仓位。
5. 不混淆决策重放、历史成交模拟、真实行情模拟盘、交易所 Testnet 和生产实盘验收。
6. 新系统按合理契约直接改造，不建设旧版本兼容框架；但任何已有真实数据都不得未经备份和明确授权删除。
7. 每阶段都有独立收益和停止点。没有数据支撑时，不增加更精细的撮合模型。

### 1.3 阶段裁剪

| 阶段 | 内容 | 本计划定位 | 完成后可停止 |
| --- | --- | --- | --- |
| A | InstrumentRules、REST 快照完整性 | 第一批必做，小范围交易正确性改进 | 是 |
| B | 决策输入工件、单次及连续决策重放 | 第二批必做，解决实盘决策解释与回归 | 是，推荐首个版本终点 |
| C | Archive 受限导出、离线 Bar 级成交模拟 | 后续独立批次，须先通过 A/B 验收并再次确认启动 | 是 |
| D | 部分成交、复杂延迟、TWAP | 本轮明确不实施，仅列启动条件 | 不计入 A/B/C 完成标准 |

不把新增风控引擎、速率保险丝、Factor warmup 改造、前端大屏或因子重算引入主线。现有 Primary 路径已经有 `RequireDatasetLookback`，不能仅凭某个 Python 因子的 `min_periods=1` 认定缺少冷启动保护。

## 2. 现状与参考证据

本节路径相对 MooX 仓库根目录；参考路径相对 Nautilus 仓库根目录。实施前重新核对符号，不能用旧行号覆盖当前代码。

| 主题 | MooX 当前入口与已具备能力 | 借鉴对象与本次增量 |
| --- | --- | --- |
| 数量规则 | `modules/trade/internal/domain/shared/decimal.go` 已有精确数值；`application/target/executor.go:baseQuantityRules` 与 `application/operator/flatten.go:baseQuantityRules` 重复换算 | Nautilus `crates/model/src/instruments/mod.rs:Instrument`；收敛规则而非替换 Decimal |
| 快照 | `modules/trade/internal/execution/adapter.go:ExecutionAdapter`；`application/accountsync/service.go:applySnapshot`；`infra/store/fact.go:ReplacePositionsForAccount` | Nautilus `crates/model/src/reports/mass_status.rs:ExecutionMassStatus`；明确完整性、时间边界和覆盖范围 |
| 共用执行 | `modules/trade/internal/execution/factory.go:Factory.Bind`；`test/live_paper_parity_e2e_test.go` 已覆盖共用 OrderService/Reducer | 保留 paper/live 共用路径，只增加离线装配，不复制第二套订单账本 |
| 策略求值 | `modules/strategy/internal/selection/evaluator.go:Evaluate`；`selection/state.go`；`trigger/processor.go:handleInstances` | 保留纯求值和理论 RuleState，不转换为持有交易状态的 Actor |
| 结果提交 | `modules/strategy/internal/store/definitions.go:CommitResult` 已有 session、有效期、CAS 与发布状态原子提交 | 在同一 SQLite 事务中附加输入工件，不引入对象存储事务编排 |
| 输入冻结 | `modules/strategy/internal/input/types.go:EvaluationInput`；`input/hash.go:Hash`；Processor 的 snapshot JSON | 已冻结 DSL/绑定/index revision，但还需要实际输入、前态、已解析规则和日历上下文 |
| 历史数据 | `modules/archive/internal/domain/row.go:Manifest/MergePatch`；`writer/writer.go` 已有 Parquet、generation、SHA-256，但物化会覆盖旧字段值 | Nautilus `crates/backtest/src/data_iterator.rs:replay_key`；借鉴有序回放，不把当前 Parquet 宣称为历史 as-of 数据库 |
| 时钟 | Processor、WeightResolver、TargetExecutor、Paper Decider 已有部分 `Now` 注入 | Nautilus `crates/system/src/clock_factory.rs`；只统一离线确定性路径需要的业务时间 |
| 模拟成交 | `modules/trade/internal/execution/paper/decider.go` 当前整单生成 Fill；`paper/matcher.go` 持久化匹配 | Nautilus `crates/execution/src/models/fill.rs`；第一版仍整单成交，不顺手升级部分成交 |

这些是静态源码结论，不表示已确认现网存在残缺快照、错误成交或前视收益。

## 3. 总体设计与不变量

### 3.1 保持在线主链

```text
Collector / Merge / Factor
    → Storage 与现有就绪契约
    → Strategy 读取并冻结 EvaluationInput
    → 现有 selection.Evaluate
    → 同一事务提交结果、RuleState、发布数据和决策工件
    → 现有 JetStream FULL target
    → Trade 目标接受、权重换算、目标收敛
    → OrderService → live 或 paper adapter → 现有事实 Reducer
```

本计划不重新定义 Dataset/View 就绪事件，也不覆盖正在独立演进的数据平台计划。B 阶段在求值边界截获已经读取的输入，因此不要求重构上游协议。

### 3.2 离线优先采用两段式工具

```text
B：冻结的决策工件 → strategy replay → 目标与规则状态对比报告

C：连续决策工件 → strategy replay-series → targets.jsonl
   固定的行情与标的规则文件 + targets.jsonl
       → trade replay → 独立 trade.db + orders/fills/equity/report
```

理由：当前 Strategy 的 RuleState 是理论状态，不依赖成交反馈，可以先算目标序列，再模拟交易。每个 CLI 只调用自身模块的 `internal` 包，避免违反 Go internal 边界。只有 C 的文件协议确实被两个模块共同消费时，才增加一个无业务引擎的共享协议 package。

### 3.3 永不破坏的业务约束

- FULL 目标仍是完整组合快照，空 targets 表示归零；缺失输入、捕获失败、文件缺口都不是空 targets。
- session 授权、目标时效、旧周期拒绝、请求幂等、UNKNOWN 先查询而非盲目重发保持不变。
- 资金预留、订单状态、成交去重、持仓和余额都通过现有 Trade 路径更新。
- 市场/合约单位只转换一次；普通领域数量继续使用基础资产数量。
- 回放目录、账户、数据库和 owner/session 与生产隔离，不允许离线命令加载 live 凭据。
- 行情时间、业务决策时间、真实系统时间分开；不得用历史钟签名 HTTP 请求或决定真实网络超时。

## 4. A 阶段：交易规则与快照契约

### A1. 收敛 InstrumentRules

**改动文件**

- 新增：`modules/trade/internal/domain/instrument/rules.go`、`rules_test.go`。
- 修改：`modules/trade/internal/application/order/validator.go`。
- 修改：`modules/trade/internal/application/target/executor.go`。
- 修改：`modules/trade/internal/application/operator/flatten.go`。
- 按调用链修改：`modules/trade/internal/exchange/binance/`、`modules/trade/internal/exchange/okx/` 中实际拥有交易所单位转换的文件，保留 HTTP/WS DTO 转换职责。
- 测试：上述调用点现有测试、`modules/trade/test/live_paper_parity_e2e_test.go`。

**职责**

`InstrumentRules` 是普通值对象，不持有 Store、RPC client、时钟或锁。构造参数从已有 Instrument 元数据映射；领域包不反向依赖 `infra/store` 或 `application`。现有元数据没有独立的原生数量单位字段，本轮不为此改 schema：在 `rules.go` 中以一个纯函数把受支持的 exchange/market 映射为 `NativeBase` 或 `NativeContracts`，构造时显式传入；Target、Flatten、validator 和 adapter 共用这一个映射，不各自推断。

| 方法职责 | 输入/输出语义 |
| --- | --- |
| 构造与校验 | tick/step 为正；最小量非负；有合约换算时乘数为正；禁止不支持的结算公式被默认当成线性 |
| `BaseStep` / `MinBaseQty` | 把规则转换为领域基础资产单位，供 Target、Flatten 和 validator 共同使用 |
| `FloorBaseQty` | 对非负绝对数量按实际交易步长向下取整；方向由外层保留，不能改变正负持仓目标 |
| `ValidatePrice` | 非法价格和 tick 不对齐返回明确错误；不得静默四舍五入公共限价订单 |
| `ValidateBaseQty` | 同时检查正数、步长和最小量；不替代资金、杠杆或 owner 校验 |
| `BaseToVenueQty` / `VenueToBaseQty` | 明确该 venue API 使用基础资产数量还是合约张数，禁止仅凭 `MarketType=SWAP` 推断 API 单位 |
| 名义金额 | 仅覆盖目前支持的 SPOT/线性 SWAP 公式；不新增反向合约、quanto 或期权支持 |

当前单位基线及第一版映射：Binance SPOT/SWAP、OKX SPOT 为 `NativeBase`；OKX SWAP 为 `NativeContracts`；未支持的 exchange/market 返回错误。Binance 现有适配器将 ContractValue 设为 1；OKX SWAP 依据 ctVal/lotSz/minSz 使用合约张数。OKX SWAP 只有 live adapter 出站执行 base/ctVal，订单、Fill、Position 和私有持仓入站执行 native*ctVal；Paper 始终直接使用基础资产数量，不做 native conversion。主要核对文件为 `exchange/binance/binance.go`、`exchange/okx/okx.go`、`exchange/okx/account_events.go`。

资金、杠杆、最大子单金额、账户 Ready 属于现有业务风控，不塞进 InstrumentRules。Spot 最小名义金额和 reduce-only 特例保持当前业务语义，改变语义必须另列用例与说明。

**步骤**

- [ ] A1.1 先建立字段/单位对照表，分别记录 Binance SPOT、Binance SWAP、OKX SPOT、OKX SWAP 的 API quantity、step、contract value 含义；以适配器源码和已有测试为准。
- [ ] A1.2 新增表驱动 RED 测试，覆盖四种 exchange/market 单位映射、未知组合拒绝、非十进制整步长、最小量、合约乘数、零/非法规则、买卖方向及错误精度。
- [ ] A1.3 实现值对象，直接使用现有 Decimal 运算，不经 float64，也不通过字符串截位实现取整。
- [ ] A1.4 分别替换 Target、Flatten、Order validator 的重复规则，并删除已无调用的局部 helper；不重排无关大文件。
- [ ] A1.5 只在原有 adapter 边界替换相同语义的换算，保留公共 OrderRequest 基础资产单位，检查没有二次除/乘合约值。
- [ ] A1.6 跑定向测试、Trade 全模块和 race，独立 codeCR 后提交这一逻辑单元。

**必须通过的数值用例**

| 用例 | 期望 |
| --- | --- |
| 基础 step=0.005，数量=0.012 | floor=0.010，原始公共数量校验拒绝 0.012 |
| 合约张数 step=1，合约值=0.01 基础资产，基础数量=0.025 | floor=0.02，发送 2 张，不发送 0.02 张 |
| 某 SWAP venue 已直接使用基础资产 quantity | 不因市场类型再乘/除合约值 |
| tick=0.05，价格=100.03 | 拒绝，不自动改为 100.00 |
| 同一个标的走 Target、人工下单和 Flatten | 使用一致的数量规则；允许的业务特例有明确测试 |
| metadata 缺乘数、step=0、未支持的反向结算 | 返回显式错误，禁止除零或按 1 静默补值 |

运行目录为 `modules/trade`：

```bash
go test -count=1 ./internal/domain/instrument ./internal/application/order ./internal/application/target ./internal/application/operator ./internal/exchange/...
go test -count=1 ./...
go test -race -count=1 ./...
```

验收：没有新增常驻组件；原有有效交易场景行为不变；数量公式有一个明确归属；source 与 paper/live 测试均能证明单位只转换一次。

### A2. 为 REST 批量事实增加完整性契约

**改动文件**

- 新增：`modules/trade/internal/execution/snapshot.go`、`snapshot_test.go`。
- 修改：`modules/trade/internal/execution/adapter.go`、`execution/paper/adapter.go`。
- 修改：`modules/trade/internal/exchange/binance/`、`modules/trade/internal/exchange/okx/` 批量查询实现与测试。
- 修改：`modules/trade/internal/runtime/session.go`、`internal/application/accountsync/service.go`。
- 修改：`modules/trade/internal/infra/store/fact.go` 中接收完整快照的调用契约与对应测试。
- 同步：`modules/trade/test/` 中所有实现 ExecutionAdapter 的 fake，不允许 fake 默认绕过完整性要求。

**接口语义**

positions、open orders、recent fills 的结果各自附带：

```text
coverage:
  scope: account 或明确的 symbol 集合
  started_at: 本次查询开始时间
  completed_at: 本次查询完成时间
  complete: 所需分页/分片是否全部成功，默认 false
  window_start/window_end: 历史 fills 查询的覆盖窗口，半开区间
items: 规范化事实
next_cursor: 仅 fills 使用，必须属于已完整读取并成功应用的范围
```

这是本地 Go port 契约，不增加对外 tRPC 服务或 JetStream 事件。现有 AccountSnapshot 的字段 presence 继续表达“该字段是否返回”，不得用 `complete` 代替，也不得把查询 `completed_at` 冒充交易所事实时间。

**采用简单的整批拒绝策略**

1. 所需 REST 数据完整采集后才调用 `applySnapshot`。
2. 任一必需结果 incomplete、scope 不足或查询失败，本轮 REST batch 不写入，不推进 fills cursor，不执行缺失删除，账户不进入 Ready。
3. 已经由私有流成功写入的事实继续保留；不回滚真实私有事件。已有流程中任何中途失败后的 fill 重投仍必须幂等。
4. 完整且为空的账户级 positions 才表示该范围确实没有持仓；完整的单 symbol 结果不能替换整个账户。
5. `ReplacePositionsForAccount` 保留对更新较新的 WS 事实的时间保护；coverage 证明范围完整，不能替代时间先后保护。
6. 初次启动和重连继续使用既有私有事件缓冲/激活机制。查询失败时不自动恢复下单；撤单和查询仍可执行，清仓必须先取得可信持仓。
7. 保留 AccountSync 现有 membership/execution/trading-account 锁顺序；REST 采集、coverage 校验与应用仍在现有账户锁保护内，私有 Position 更新使用同一把账户锁。不为缩短临界区把采集移出锁，不新增水位或接收序号；Session 保留 opMu 和启动缓冲，仅在完整快照后激活。
8. 不新增 partial merge 模式、快照重试队列或新的同步状态机。临时失败走现有 session 重连/周期同步。

**步骤与测试**

- [ ] A2.1 为 complete=false、完整空集、错误 scope、分页中断分别增加失败测试。
- [ ] A2.2 替换本地 adapter 返回类型；Binance/OKX 只有在端点或完整分页确实覆盖请求范围时才能设置 complete=true。
- [ ] A2.3 Paper 从同一隔离账户读取完整本地事实，返回明确 account scope；当前 recent fills 有 Limit=1000 且无有效分页游标，必须补齐分页或明确 incomplete，不能把截断列表标成 full；与真实 adapter 共用契约测试。
- [ ] A2.4 Session 和 AccountSync 在应用前统一验证 coverage；错误必须可从账户诊断读出，而不是仅打印日志。
- [ ] A2.5 检查 Operator Flatten 等其他快照调用方，禁止旁路继续把裸 slice 当全量账户事实。
- [ ] A2.6 验证部分分页不删仓、不推进 cursor；完整空集才清理旧仓；并发私有更新按现有锁顺序在快照前或后应用且较新事实不丢失；不完整启动不激活缓冲、不 Ready；恢复后的重复 fill 不重复计账；Paper 超过 1000 fills 不漏页。
- [ ] A2.7 跑 Trade 全模块/race 和 paper/live parity，codeCR 重点审查“缺失”和“零”的区别后提交。

运行目录为 `modules/trade`：

```bash
go test -count=1 ./internal/execution/... ./internal/runtime ./internal/application/accountsync ./internal/application/operator ./internal/infra/store
go test -count=1 ./test -run 'TestLiveAndPaper|Snapshot|Reconcile|Cursor|Unknown'
go test -race -count=1 ./...
```

验收不要求制造真实资金风险。真实交易所只做只读分页/范围抽样，确认本地 complete 映射的依据；接口测试通过不能替代该事实核对。

## 5. B 阶段：决策工件与策略重放

### B1. 明确三种不同的时间和证明能力

| 字段/能力 | 定义与禁止事项 |
| --- | --- |
| `bar_end_time` | 策略依据的已闭合周期时间，保持现有 FULL 合同 |
| `evaluated_at` | 本次在线求值完成并形成结果的本地时间，复用同一次采样的 StrategyResult.CreatedAt 和事件 OccurredAt，不另采三个时间，也不得回填为 bar_end |
| `captured_at` | 完整输入工件形成的本地时间，不能宣称为每条源数据第一次到达时间 |
| 行级 `available_at` | 第一阶段不伪造。上游未提供可靠历史可得性时，保留 unknown；已有 source position 可原样记录 |
| 决策重放 | 证明“给定当时实际读取的输入和前态，同一求值器产生相同目标与后态” |
| 全历史 PIT 回测 | 还要求历史标的池、修订、因子版本、可得性和成交规则；不属于 B 的交付承诺 |

Nautilus 的 ts_event/ts_init 提醒我们显式表达时间，不意味着只增加一个时间字段就获得 point-in-time 正确性。

### B2. 定义可序列化 DecisionArtifact

**改动文件**

- 新增：`modules/strategy/internal/decisionartifact/types.go`、`codec.go`、`codec_test.go`。
- 修改：`modules/strategy/internal/trigger/processor.go`，只在现有求值边界组装工件。
- 修改：`modules/strategy/internal/selection/evaluator.go` 及测试，复用现有 definitionFromDSL 转换，向 Processor 暴露同一准备入口，不复制 DSL 转换实现。
- 复用：`modules/strategy/internal/input/hash.go`、`selection/state.go`、`input/calendar.go`。

**工件字段与内容**

| 分组 | 必须冻结的内容 |
| --- | --- |
| 身份 | format_version=1、result/instance/session/strategy ID、space ID、previous_result_id |
| 执行版本 | MooX commit/build identifier、dirty 状态、Go 版本和架构、DSL hash、日历规则版本；不是自动下载执行代码的地址 |
| 时间 | bar_end_time、evaluated_at、captured_at、BarIndex、BarDuration、BarEndAt 是否存在及实际调用的相对 offset→bar_end 映射 |
| 规则 | 原始 DSL 仅供审计；计算冻结实际 evaluator-facing selection.Definition，保留 PoolSet 与具体 Pool 的区别，以及每条规则的 pool 解析/未使用状态 |
| 输入 | 所有 Items、Values、PreviousValues、ScopedFields、ScopedFieldsReady、Ineligible；Decimal 使用可精确往返的字符串 |
| 前态 | Evaluate 前的完整 RuleState 快照，而不是刚产生的后态 |
| 来源 | source/result index ID/revision、实际绑定信息、已有 factor source_hash；无法获得的来源信息显式为空，不补造 |
| 预期 | 当前 Evaluate 产生的排序后 targets、规范化 RuleStates；必要 debug 只用于解释 |
| 发布 | 绑定交易账户的结果原样保留 StrategyResult.EventData 中完整 EventMessage，作为 C 的身份与时间证据；观察型结果标记无执行信封，不影响 B 重放 |
| 校验 | canonical input hash、完整 payload SHA-256；完整 hash 不包含自身 hash 字段 |

`EvaluationInput.BarEndAt` 是函数，不能直接 JSON marshal。参数是相对当前 period 的 offset，不是绝对 BarIndex。非 nil 时用记录型 wrapper 委托原函数，记录 Evaluate 实际查询的 offset 与返回值；重放只查捕获映射。请求未捕获 offset 必须报不支持，不能回退当前日历或 time.Now；wrapper 错误在 Evaluate 返回后统一检查，不把零时间当合法值。原函数为 nil 时保留 nil 及 BarDuration，不能注入 wrapper 改变求值器原有分支。

holding 非建仓周期可以直接复用旧 RuleState，现有 inputDSLForPeriod 因而不读取该规则的输入或调用 Pool UDF。这是合法成功决策，不能为了捕获而补调 UDF。先用 selection 内唯一的准备函数生成 Definition，再将同一个值交给 Evaluate 和工件编码；参与本期输入读取的动态 pool 固定为实际名单，未使用的规则标记 unused/unresolved 并保留真实 Definition，不伪造空 pool 解析结果。跨版本比较若需要原来未读取的数据，应明确输入不足，而非重新查线上数据。

规范化要求：map key 和语义无序集合排序；RuleState 用现有 NormalizeRuleState；保留 Decimal 原有精确值；不得把所有 nil/空集合无差别归一化。`input.Hash` 是输入校验工具，但不能代替包含前态、日历与 Definition 的完整工件 hash。

**步骤与测试**

- [ ] B2.1 写 codec RED 测试：map 顺序不同 hash 相同、ScopedFieldsReady 改变必须可见、前态改变完整 hash 不同、Decimal 精度保真。
- [ ] B2.2 定义无运行时函数的 DTO 和 canonical encoding；拒绝未知 format_version、重复 instrument、非法 decimal、损坏/超限 gzip。
- [ ] B2.3 在本期所需输入已就绪、Evaluate 真正执行的位置捕获输入/前态和日历调用；不再次读取 Storage 或重新调用 Pool UDF。
- [ ] B2.4 让线上求值与捕获使用同一个准备后的 Definition；冻结完整 EventData，复用 result.CreatedAt 作为 evaluated_at；避免离线重查标的目录或重新拼生产身份。
- [ ] B2.5 测试 holding/offset、BarIndex=0、CN-stock 周末/节假日日历、nil 日历、bars[-1]、动态 pool、空合法 pool、subject-scoped 缺列的 round-trip；增加 dynamic-pool+holding 非建仓周期用例，证明捕获不补调 UDF；不要求 Factor 重算。

示例测试结构，接口在 B2 定义后实现，不是新增生产代码：

```go
func TestArtifactRoundTripPreservesDecision(t *testing.T) {
    original := decisionFixtureWithHoldingAndScopedFields(t)
    blob, err := Encode(original)
    require.NoError(t, err)
    decoded, err := Decode(blob)
    require.NoError(t, err)
    require.Equal(t, original.ExpectedTargets, decoded.ExpectedTargets)
    require.Equal(t, original.PreviousRuleStates, decoded.PreviousRuleStates)
    require.Equal(t, original.CalendarCalls, decoded.CalendarCalls)
}
```

fixture 必须使用真实 DSL/Decimal/RuleState 构造，不 mock JSON 解码器或 selection.Evaluate。

### B3. 工件随结果在同一 SQLite 事务提交

**改动文件**

- 修改：`modules/strategy/schema/strategy.sql`、`schema/schema_test.go`。
- 修改：`modules/strategy/internal/store/definitions.go:CommitResult`、`definitions_test.go`。
- 新增：`modules/strategy/internal/store/decision_artifacts.go`、`decision_artifacts_test.go`。
- 修改：`modules/strategy/internal/bootstrap/config.go`、`bootstrap.go`、`modules/strategy/config/app.yaml` 及配置测试。
- 修改：`modules/strategy/internal/trigger/processor.go`。
- 同步说明：`docs/策略模块架构设计.md`，新增 `docs/运维/MooX-Strategy决策重放.md` 并更新相关文档契约，区分现有三张业务表与新增工件辅助表，不让文档继续声称只有三张物理表。

采用一张本地辅助表 `t_strategy_decision_artifacts`，以 result_id 为主键，保存 capture_status、format_version、payload_sha256、未压缩长度、可为空的 gzip BLOB、captured_at。每个新结果保留一条捕获元数据；disabled/oversize 没有 BLOB，purged 保留原 hash 和长度。第一版不做跨结果去重、不建立对象存储、不引入文件+数据库双写协议。旧结果没有元数据时明确为 legacy-unavailable，不尝试用当前 View 补出“历史输入”。

**持久化规则**

- 扩展现有 CommitResultRequest 携带捕获元数据和可选 payload；它们与新结果在同一事务提交，结果 CAS/session/过期检查原样执行。
- 同一 session/bar 命中已有结果时，返回已有结果及已有工件，不用新输入覆盖旧工件。
- capture_status 仅放辅助表，状态为 `captured`、`disabled`、`oversize`、`purged`；StrategyResult 的 snapshot_json 最多记录不可变 artifact hash，不写需要随清理改变的状态，继续遵守结果插入后仅 PublishStatus 可变的契约。
- 默认启用捕获，默认保留 7 天，单份未压缩工件上限 8 MiB；这些是可配置的初始默认值，不作为吞吐承诺。
- disabled/oversize 可继续原有交易流程，但必须留下状态与计数；编码错误、数据契约错误或 SQLite 写入失败不能假装 captured，走现有失败处理。
- gzip 解压同样限制输出 8 MiB，防止损坏文件无限分配；数值和集合顺序在压缩前固定。
- 每日复用既有进程定时机制，分批清理过期且 publish_status 非 pending 的已捕获 payload；同一事务把辅助表 payload 设为 NULL、capture_status 改为 purged，保留元数据。不修改结果 snapshot/targets/RuleStates/交易记录，不每次清理做 VACUUM。
- 不承诺数据库硬容量上限：持续 pending 会阻止对应工件清理。复用磁盘告警并暴露 artifact 数量/体积/跳过清理数量，不建设新的存储调度器。

**步骤与测试**

- [ ] B3.1 先测试事务回滚、CAS 输家不留工件、重复结果不覆盖、旧结果无工件可读。
- [ ] B3.2 增加表和 Store 方法，遵循 SQLite schema 风格，逐个 schema 载入空库验证。
- [ ] B3.3 接入 Processor 的同一提交请求；测试 pending result 和工件要么一起提交要么一起回滚。
- [ ] B3.4 接入配置、oversize 状态与计数；测试超限不会生成假工件，也不会把空 targets 当降级结果。
- [ ] B3.5 增加有界清理测试：7 天边界、pending 保留、purged 后重放返回明确原因、原 hash/长度保留、结果 snapshot/目标/规则状态逐字节不变；清 payload 与改状态之间注入失败必须一起回滚。
- [ ] B3.6 运行 Strategy 全模块/race、真实 SQLite 流程与 schema 测试，codeCR 后单独提交。

运行目录为 `modules/strategy`：

```bash
go test -count=1 ./internal/decisionartifact ./internal/input ./internal/selection ./internal/store ./internal/trigger ./schema
go test -count=1 ./...
go test -race -count=1 ./...
```

### B4. 提供只读导出与离线决策重放 CLI

**改动文件**

- 修改：`modules/strategy/cmd/cli/main.go`、`main_test.go`，保留现有 validate 命令。
- 新增：`modules/strategy/cmd/cli/replay.go`、`replay_test.go`。
- 新增：`modules/strategy/internal/replay/decision.go`、`decision_test.go`、`series.go`、`series_test.go`。
- 新增：`modules/strategy/internal/replay/testdata/` 下的脱敏工件。

**目标命令**

```bash
moox-strategy-cli decision-export --db /data/moox/prod/data/strategy.db --result-id result-example --out /tmp/moox-decision.json.gz
moox-strategy-cli replay --input /tmp/moox-decision.json.gz --out /tmp/moox-decision-report.json
moox-strategy-cli replay-series --input-dir /tmp/moox-decisions --out-dir /tmp/moox-series
```

上述是未来 CLI 合同。decision-export 使用 SQLite 只读连接和一致读事务，不通过会自动 ApplySchema 的启动函数打开在线数据库，不复制正在写入的裸 db 文件以忽略 WAL。输出路径已存在时失败，禁止默认覆盖。replay 系列只读本地工件，不加载生产服务配置、不访问网络、不启动 outbox。

**重放方式**

- 单次：解码工件 → 重建实际 Definition、EvaluationInput、日历查表和前态 → 直接调用现有 Evaluate → 比较排序后的 targets 和 RuleStates。
- 连续：按 session 与 bar_end 排序，首条使用工件记录的初始前态；后续使用上一次重放后态，而不是逐条注入记录前态掩盖漂移。
- 通过 previous_result_id 检查链是否连续；首条允许明确作为切片起点，后续缺口、跨 session、同周期冲突或前态不一致直接失败。
- 不要求每个自然分钟都有结果：合法无结果周期不等于数据丢失，以结果链和记录的日历为准。
- 默认严格检查引擎 commit/build identifier 与求值环境；dirty/unknown 制品不能冒充严格原环境重放。不同版本或环境必须显式指定 compare-current，此时报告标记为跨版本回归比较，不称为原环境重放。
- 比较业务结果，不把随机 UUID、输出目录、运行耗时放进结果一致性判断。

**退出与报告合同**

| 退出码 | 含义 |
| --- | --- |
| 0 | 所请求工件全部成功重放且业务结果一致 |
| 1 | 已完成比较但出现 targets/RuleState 差异 |
| 2 | 参数、版本、完整性、工件缺失、链缺口或数据契约错误 |

报告包含 source commit/current commit、artifact hashes、result/session/bar、目标和后态是否一致、结构化差异、capture_status、输入来源及运行限制。stdout 只输出摘要 JSON，诊断到 stderr，完整报告落指定文件。

**步骤与测试**

- [ ] B4.1 用真实 Evaluate 建立失败测试，覆盖持仓周期跨 bar、空 FULL、动态 pool 冻结、上一根字段、缺少日历映射和损坏 hash。
- [ ] B4.2 实现只读导出；测试 WAL 中已提交数据可读、打开数据库不运行 schema、不写生产目录。
- [ ] B4.3 实现单次 replay、显式跨版本比较和结构化 diff，不复制 selection 算法。
- [ ] B4.4 实现连续前态衔接和结果链校验，证明丢一份中间工件不会默默算完。
- [ ] B4.5 使用录制的真实脱敏工件做 E2E；限制网络拨号为失败，重复运行两次业务输出必须一致。
- [ ] B4.6 在个人使用说明中明确：这验证策略求值，不验证 Factor、消息时序、成交价格或收益。

运行目录为 `modules/strategy`：

```bash
go test -count=1 ./internal/replay ./cmd/cli
go test -count=1 ./...
go test -race -count=1 ./...
```

### B 阶段验收门

- 至少包含一个无状态策略、一个 holding/offset 策略及一个动态 pool 策略的真实输入录制。
- 同 commit 的单次与连续重放全部一致；修改一个输入、前态或 DSL 后能给出可解释差异。
- 进程重启后仍可导出保留期内工件；过期/oversize/disabled 的结果明确不可重放。
- 线上 Strategy→Trade FULL 行为没有因捕获改变；原有 CAS、session、expiry、空目标测试仍通过。
- A/B 到此可以发布并停止。C 不作为本阶段完成的附加条件。

## 6. C 阶段：受限离线 Bar 成交模拟

**范围澄清（2026-09-18）：** 本节仅指“已录制决策的成交模拟”，不支持直接拿新策略重新计算历史因子，因此不是完整历史回测。新增的加密现货历史回测设计见 [加密现货历史回测第一版设计](../specs/2026-09-18-crypto-spot-backtest-design.md)。两者可复用基础代码，但历史回测不以完成 C 或已有在线工件为前提；本节不扩张为回测实施授权。

本节使用 `recorded_time_strict_next_open`：按实际 evaluated_at 进入执行，开盘必须严格晚于提交时间。历史回测另用 `bar_close_next_open_zero_latency`：允许同时间戳按 CLOSE→DECIDE→SUBMIT→OPEN 的事件顺序成交。不得把后者用于真实录制决策而提前成交，也不得把本节“仅回放输入、不重算 Factor”限制套到历史回测。最终文档中 `next_open_full_fill` 只表示成交形态，报告还必须记录上述独立 timing_model。

本阶段只有在 A/B 已交付且确有历史模拟需求时启动。它不是多市场通用回测引擎。

### C0. 第一版固定范围

| 项目 | 第一版边界 |
| --- | --- |
| 市场 | Crypto SPOT、单 venue、一个 LogicalAccount 和一个 Paper TradingAccount、固定结算币 |
| 数据 | 固定一组标的，UTC 24x7，固定 bar 频率，已录制的连续决策工件与本地行情 |
| 策略 | 重放已录制策略输入，不从当前 View 重建旧动态 pool，不在工具中重新计算 Factor |
| 订单 | Target 当前使用的 MARKET；整单成交；固定手续费和固定 BPS 滑点 |
| 初始化 | 显式初始结算币现金、无初始订单/持仓；允许切片初始理论 RuleState，但报告说明不是实盘账户恢复 |
| 时钟 | 单线程手动推进业务钟，不启动 cron、重连循环和后台 paper matcher worker |
| 历史版本 | 行情文件与 Instrument 规则固定到运行目录；标的规则不变是显式模拟假设，不称为历史规则还原 |
| 输出 | 订单、成交、费用、持仓、权益、目标拒绝/过期原因及确定性摘要 |
| 不支持 | SWAP/资金费率/强平、股票交易日历、LIMIT、部分成交、盘口队列、跨 venue 路由、实盘资金恢复 |

### C1. 在 Archive 上增加受限 coverage/export

**文件**

- 新增：`modules/archive/internal/replayexport/export.go`、`coverage.go`、对应测试。
- 修改：`modules/archive/cmd/cli/main.go`、`main_test.go`。
- 复用：`modules/archive/internal/parquetio/codec.go`、`domain/row.go:Manifest`。
- 新增：`packages/replayartifact/go.mod`、`manifest.go`、`targets.go`、`bars.go`、`codec.go` 及测试；仅保存标准化文件格式，不依赖业务模块。
- 修改：`go.work` 及 Archive/Strategy/Trade 的 `go.mod`，按已有本地 replace 规则接入共享格式；同步 `docs/架构总览.md` 的模块清单及 `scripts/test/contract/test-docs-architecture.sh` 的模块数量断言，不删除边界检查。

不新增 Archive daemon、SQL 查询服务、S3 catalog 或历史修订旁路账本。CLI 只接收结构化 space/dataset/subject/freq/series_tag、半开时间范围及列集合，不接受任意 SQL。

```bash
moox-archive-cli coverage --manifest-list /tmp/replay-input/manifests.json --subjects BTC-USDT-SPOT,ETH-USDT-SPOT --freq 1m --start 2026-09-01T00:00:00Z --end 2026-09-02T00:00:00Z
moox-archive-cli replay-export --manifest-list /tmp/replay-input/manifests.json --subjects BTC-USDT-SPOT,ETH-USDT-SPOT --freq 1m --start 2026-09-01T00:00:00Z --end 2026-09-02T00:00:00Z --out-dir /tmp/replay-bars
```

manifest-list 必须列出完整 partition 身份、文件路径、generation、SHA-256、行数和时间范围。先把文件复制到新运行目录的 staging 区，核对复制后的 SHA-256，再导出；源文件被并发重写导致 hash 不匹配时失败，不能改用最新文件继续。manifest 中路径必须解析到允许的本地数据根目录。

**coverage 规则**

- 逐标的按实际时间键检查期望 bar 集合；只有 min/max 命中不代表完整。
- 缺 bar、重复冲突行、缺列、错误 frequency/series_tag 或非法 OHLC 值直接报告；不前向填充，不把没有记录解释成价格不变。
- 第一版要求固定标的在请求区间全覆盖；上市/退市导致的边界缺口由缩小区间或名单处理，不自动引入历史 universe 服务。
- Parquet 当前版本标记 `data_semantics=latest_materialized_snapshot`。它可作为确定性模拟输入，但不能冒充 as-of 行情；B 的已录制决策输入与 C 的行情假设分别展示。
- 导出 `bars.jsonl`，包括 instrument、bar_start/bar_end、OHLCV、源 manifest hash；按时间与 instrument 稳定排序。

**步骤与测试**

- [ ] C1.1 建立跨月、缺分钟、同时间重复冲突、series_tag 隔离、列类型错误的 Parquet fixture。
- [ ] C1.2 实现范围和实际覆盖验证；用现有 Parquet 库，不另引数据库。
- [ ] C1.3 实现复制后 hash 验证和稳定 JSONL 导出；任何错误不生成 completed manifest。
- [ ] C1.4 对两个相同输入目录导出两次，验证内容 hash 一致，读取过程不修改原 Archive。

运行目录为 `modules/archive`：

```bash
go test -count=1 ./internal/replayexport ./internal/parquetio ./cmd/cli
go test -count=1 ./...
```

### C2. 将策略重放结果导出为目标文件

**文件**

- 修改：`modules/strategy/internal/replay/series.go`、`cmd/cli/replay.go` 及测试。
- 使用：`packages/replayartifact/targets.go`，不让 Trade 解析 Strategy internal 类型。

`targets.jsonl` 每条包含完整源 EventMessage 溯源和本次计算产生的完整目标命令：event/target/result ID、space、subject/logical account、instance/session/strategy ID、bar_end_time、effective_at、valid_until、occurred_at/evaluated_at、完整 target weights、决策工件 hash，以及现有 registry 要求的事件类型/版本。保持 `event_id=target_id=result_id`、`subject_id=logical_account_id`、`effective_at=bar_end_time`、`occurred_at=evaluated_at=CreatedAt`。

目标权重取自本次 Evaluate 输出，不复制 expected targets 作为“计算结果”；其他字段使用捕获的执行信封，经 manifest 显式、确定性的源→离线身份映射后生成，不能临时补造 session、时效或时间。源信封与映射后命令都用现有 events registry 校验，任何映射导致的身份/时间不一致直接失败。空目标保留为空数组；未发生决策的时点不补造空目标。第一版 C 明确拒绝缺少执行信封的观察型工件，它们仍可做 B 决策重放；不为其新增虚拟绑定模式。

- [ ] C2.1 扩展 replay-series 输出文件协议，先通过 B 的前态与结果链检查。
- [ ] C2.2 测试完整信封往返与身份映射、空 FULL、同时间冲突、过期目标、不连续 session、缺信封和 event/target/subject 不一致；结构/合同非法文件在创建 Trade 数据库前拒绝，运行时已到期目标保留同线上拒绝语义。
- [ ] C2.3 manifest 固定源/当前代码 commit、工件 hash 列表、数据语义和规则假设。

### C3. 提取离线装配需要的最小业务入口

**文件**

- 新增：`modules/trade/internal/replay/clock.go`、`marketdata.go`、`next_open.go`、`runtime.go`、对应测试。
- 修改：`modules/trade/internal/infra/store/target_receipt.go`、`target.go` 及 Store/Tx 时钟装配；两层目标过期校验当前直接用 time.Now，需共用实例级业务钟，线上默认真实时间、离线注入历史钟；不得从事件输入接收 now 或删除最终校验。增加历史窗口接受、到期边界拒绝、线上默认不变的 SQLite 测试。
- 新增：`modules/trade/internal/application/target/accept.go`、`accept_test.go`，从实际 eventconsumer 中提取可复用目标接受逻辑。
- 修改：`modules/trade/internal/eventconsumer/target.go`，保留事件解码、鉴权/信封校验和 ACK/NAK/TERM 映射。
- 按需要修改：`internal/application/target/weight_resolver.go`、`executor.go`、`internal/application/order/service.go`、`internal/execution/paper/decider.go`，统一注入已有 Now 及必要的 ID source。
- 新增：`modules/trade/cmd/cli/replay.go`、`replay_test.go`；修改 CLI 分派。

**抽取边界**

- `AcceptTarget` 仍检查同一 logical account 的 owner/session、时效、bar 顺序、request hash 和已有 receipt；离线不得直接写目标表跳过校验。
- JetStream handler 与离线入口复用应用层接受逻辑；服务身份校验仍留在在线传输边界，离线只处理自身新库的本地账户。
- 输入不能是未经验证的裸 protobuf。构造包含 SpaceID/EventID/SubjectID/OccurredAt/payload 的命令，复用 `packages/events/validation.go` 对 event_id=target_id、subject_id=logical_account_id、时间、重复标的和权重限制的验证；在线传输认证与该业务数据校验是两回事。
- 提取时保留 control mode、logical account 锁、成员 Ready、Instrument 支持、WeightResolver 有界调用和 receipt 证据；Store 的最终 CAS/session/有效期校验不得因上层已有检查而删除。
- Acceptor 只在提交后返回 accepted；线上调用方保留定向 wake，幂等重投 accepted=false 不重复 wake；离线由运行器显式推进，不把下单塞进 Acceptor。
- 权益取自离线账户事实，价格来自手动推进的本地行情；WeightResolver、TargetExecutor、OrderService 和 Reducer 复用原实现。
- 使用已有 `Converge`、`Matcher.Scan`/单订单入口做同步推进；不启动 Manager.Run、后台 ticker、生产 bootstrap、Gateway、NATS 或 Secret client。
- 现有 Now 注入能满足的地方不再增加接口；只有需要推进/校验单调性时由 replay 包提供 ManualClock，不创建全仓 Clock package。
- 确定性 ID 优先复用 OrderService 已有 NewOrderID 注入，仅为 TargetExecutor 的 childClientOrderID 增加必要注入。Paper exchange order/trade ID 已从账户与 client order ID 派生，不另造全局 ID 框架；生产默认保持原 xid 等生成器，不通过环境变量开启测试分支。
- 装配时逐个检查 WeightResolver、Executor、OrderService、Validator、Paper Adapter、Decider、Matcher、Reducer、Equity、AccountSync 和 LogicalAccount Service 的业务时间依赖，已有 Now 必须指向同一 ManualClock；网络 deadline 和锁等待仍用真实运行时间。

**独立库的装配顺序**

1. 使用当前 `store.Open` 打开运行目录内新库，复用 WAL、foreign key、busy timeout 和完整 schema 初始化，不直接 gorm.Open 或拼局部 DDL。
2. 通过应用/Store 正式方法创建 PAPER TradingAccount 和初始现金快照；不设置 live environment/credential。
3. 创建 STRATEGY control 的 LogicalAccount，保持 PAUSED；添加同质 LogicalAccountMember 和固定 Instrument。当前模型是 TradingAccount/LogicalAccount/Member，不恢复旧 ExecutionBinding/ExchangeAccount。
4. 通过 `logicalaccount.Service.ClaimSession` 和 AuthFence CAS 认领离线 session，不直接 SQL 写 owner 字段。
5. 按 Paper 当前规则提供 PRODUCTION 命名空间的本地 Instrument 记录；它只是元数据命名空间，不代表创建生产连接。
6. 通过现有 equity service 采样实际离线账户权益点，再将自动执行设为 ACTIVE。非空目标的 WeightResolver 读的是权益服务的采样点，不是随意拼接的账户 snapshot；空 FULL 仍保持无需报价/权益即可归零的合同。
7. 随每个行情/成交推进更新行情和账户事实，并在接受新非空目标前刷新权益采样；报价/权益时间必须真实对应模拟时点，不能把旧值重新打上新时间绕过 freshness。

**离线安全默认值**

1. 必须指定新的空 `--out-dir`，所有可写文件都位于该目录；不接受任意 `--db` 路径，不覆盖既有运行。
2. 只构造 Paper adapter，拒绝 live/account secret 字段，不读取生产配置文件或凭据环境变量。
3. 输入中任何未支持的市场、订单类型或规则直接失败，不回退为实盘查询。
4. 测试使用拒绝网络的 transport/dialer 验证没有外联，不能仅用“没有配置 key”证明隔离。
5. 每次运行从初始工件与初始现金重来；不做中断续跑、持久化 job 状态和跨机器调度。

### C4. 固定 Bar 级事件顺序与成交假设

第一版采用保守的 `next_open` 模型，不利用当前 bar 的 high/low 猜测路径。

`bars.jsonl` 虽包含完整 OHLCV，运行器必须拆成两个可见性投影：`BarOpened` 在 bar_start 只暴露 open；`BarClosed` 在 bar_end 才暴露 high/low/close/volume。完整行只归事件源持有，WeightResolver、equity 和 matcher 不能直接遍历文件读未来字段；local marketdata 只返回 ManualClock 当前已发布的投影。共用边界先关闭旧 bar 再打开新 bar，价格引用以该时点最新已知投影为准。

```text
按 (timestamp, event_priority, instrument_id, source_sequence) 排序
同一 timestamp：
  1. 应用旧 bar 的 BarClosed，再应用新 bar 的 BarOpened；同类事件按 instrument 排序
  2. 撮合此前已提交且当前仍有效的订单
  3. 应用新的策略目标，按当前离线权益/已知参考价换算数量
  4. 通过 Converge 生成新子单，但该新单不能在本 timestamp 再成交
  5. 记录权益、订单状态及本时点诊断
```

具体规则：

- 决策在 evaluated_at 才进入离线 Trade，不在 bar_end_time 提前进入。evaluated_at 必须晚于或等于源 bar_end_time。
- 下单后在该 instrument 第一个严格晚于提交时间的 bar open 成交；10:00:02 提交的订单不能使用 10:00:00 的开盘价。
- 在接受目标时，权重换算使用截至当前时间可得的参考价格；不得为提前换算而读取下一根 open。缺参考价时保持现有可重试/拒绝语义，不用未来价格补齐。
- 目标到期后禁止创建新子单；已经提交的订单仍按现有 Trade 有效性规则处理，不自行发明到期自动撤单或强制平仓。
- 固定滑点对 BUY/SELL 方向不利，手续费按现有结算币规则计算；下一 open 时资金不足走明确取消/阻塞，不伪造负余额。
- 同一时间不无限循环下单和成交，避免一个 open 被无限复用；默认每个逻辑账户每次同步推进最多生成一个新子单。
- 无下一根 open 的尾部未成交订单保留并报告；不在区间末尾强行以最后 close 清仓。
- 权益按截至当前时间已知价格估值；缺估值价报告不可估值，不能当成零价。

**与现有 Paper 的最小衔接**

当前 PaperReservationPolicy/OrderService 会固化 MARKET 的 PaperExecutionPrice，Decider 又优先使用该价格。因此仅把 Matcher.Scan 延后，仍会按提交时价格成交，不能称为 next_open。

在 replay/next_open.go 定义一个窄的 `NextOpenDecider`，挂到已有 Matcher.DecideContext：非本标的 open 阶段或提交时间不严格早于本 open 时返回 Rest；符合资格时只复制传入的 OrderRecord，清空副本的 PaperExecutionPrice，再调用生产 paper.Decider.Decide。持久化订单和提交时资金预留不改。此时本地行情只暴露当前已知 open，令 Bid=Ask=Last=open，并保留真实 open SourceTime；由已有 Decider 应用一次不利滑点、手续费与资金检查，Matcher/Reducer 原样记账。无 bid/ask spread 是明确模拟假设，不虚构盘口。

不得用“前一 close 与下一 open 同一时间”绕过严格晚于提交的规则；以实际 evaluated_at/SubmittedAt 为准，同刻提交要等再下一次 open。新旧报价过期判断沿用现有配置，不给旧价格重打时间戳；bar 内决策因报价过期被拒绝/阻塞也须如实报告。

不能把当前整单 Matcher 的终态逻辑简单改成部分成交。第一版继续整单；next_open 只改变成交资格和价格来源，不把订单、成交或权益计算复制到 replay 包。

**任务步骤**

- [ ] C3/C4.1 先写时序 RED 用例，固定 3-5 根 bar 和一个简单目标；断言下单与成交严格前后有序。
- [ ] C3/C4.2 提取 AcceptTarget，在线事件入口的所有原有幂等/session/expiry 测试先保持通过。
- [ ] C3/C4.3 建立只含 Paper 的离线装配，新建独立 SQLite；使用现有账户、目标和订单应用服务。
- [ ] C3/C4.4 接入 ManualClock/ID source/local marketdata，不依赖后台业务 ticker/worker 推进，不限制 Go runtime 或数据库驱动的内部 goroutine。
- [ ] C3/C4.5 实现稳定事件排序和 NextOpenDecider；测试提交参考价=100、下一 open=110 时按 110 加一次滑点成交而非 100，且副本改动不提前改写订单；校验没有读取未来 OHLC。
- [ ] C3/C4.6 接入导出订单/成交/权益报告，业务数据排序稳定，运行耗时等非确定性字段单独存放。
- [ ] C3/C4.7 运行真实 SQLite+生产 Paper adapter+生产 Reducer 的端到端测试，codeCR 后才运行录制行情样本。

目标命令：

```bash
moox-trade-cli replay --manifest /tmp/replay-run-input/manifest.json --initial-cash 10000 --out-dir /tmp/moox-offline-run
```

### C5. 验收用例与输出

| 用例 | 必须验证的结果 |
| --- | --- |
| 同输入运行两次 | 规范化 targets/orders/fills/positions/equity/report 的业务 hash 一致 |
| 决策晚于 bar open | 不成交于决策前的 open，不能使用未来 close |
| 决策发生在 bar 内 | 即使文件已含完整 OHLCV，WeightResolver/equity 查询也拿不到该 bar 的 high/low/close/volume |
| 旧 bar 关闭与新 bar 开始同刻 | 先发布旧 close 再发布新 open，撮合旧单后才接受新目标，新单不在同刻成交 |
| 买入后空 FULL | 由原 TargetExecutor 产生减仓，最终资金/持仓/费用账一致 |
| 同目标重投 | 不重新生成第二份 receipt 或重复成交 |
| 旧 session、旧 bar、已过期目标 | 复用线上相同拒绝语义，不直接跳过 |
| 跳空后资金不足 | 明确取消/阻塞，不超额透支 |
| 提交价与下一 open 不同 | 不复用固化 PaperExecutionPrice，滑点只计一次，成交仍走生产 Decider/Matcher/Reducer |
| 缺行情、坏 manifest、未知 Instrument | 在开跑或对应步骤失败，有明确错误，不自动下载补齐 |
| 末尾没有下一 open | 保留未成交状态，报告包含 pending/residual |
| 阻断所有网络 | 仍能完成合法运行；任何拨号尝试使测试失败 |
| 相同业务 ID 多次 Fill | 现有 Reducer 幂等，不能重复增加仓位 |

输出目录固定为 `manifest.json`、`trade.db`、`orders.jsonl`、`fills.jsonl`、`equity.jsonl`、`report.json`。报告必须带 `simulation_model=next_open_full_fill`、费用/滑点、Instrument 常量假设、行情 latest_snapshot 语义、输入版本、未成交和不可估值项。第一版不做年化收益/Sharpe 展示，避免把数据口径和短样本统计包装成策略表现。

运行目录为 `modules/trade`：

```bash
go test -count=1 ./internal/replay ./internal/application/target ./internal/execution/paper ./cmd/cli
go test -count=1 ./test -run 'Replay|LiveAndPaper|Target|Paper'
go test -race -count=1 ./...
```

C 的验证证明离线模拟在已声明假设下可重复，不证明成交逼真，也不替代 live Testnet 或生产验收。

## 7. D 阶段：本轮明确不做

| 能力 | 启动条件 | 届时必须处理的边界 |
| --- | --- | --- |
| 部分成交/有限流动性 | 已有可用成交量/盘口数据，且整单模型确实影响判断 | 分笔 fill ID、剩余量、手续费分摊、Matcher 终态与重启幂等，不能只改 Decider |
| 延迟分布/成交概率 | 有实盘延迟与成交统计可校准 | 固定随机种子、可追溯参数、解释模型偏差 |
| TWAP/ExecutionPolicy | 真实订单规模或执行成本证明立即成交不够 | 旧目标替换、到期、Pause/Flatten、子单归属、数量守恒；仍放 Trade 内 |
| 全历史 PIT | 明确需要研究新策略且有历史修订/可得性数据 | Archive append-only revision、历史 universe、factor/source 版本；单独出计划 |
| 新单速率保险丝 | 存在交易所限速/异常循环的实际证据 | 限额范围、新增风险与减仓撤单豁免、现有重试联动 |
| 指标 initialized | 现有 lookback 校验无法表达具体因子成熟条件 | 先证明缺口再改契约，不把数据已就绪与统计成熟一概等同 |

这些能力不进入 A/B/C 的任务列表、schema 或空接口。没有实际调用方时不预留插件注册表。

## 8. 实施顺序、分工与提交

```text
A1 InstrumentRules ─────────────┐
A2 Snapshot coverage ──────────┤→ A 验收，可独立发布
B2 Artifact → B3 原子捕获 → B4 决策重放 → B 验收，推荐首版结束
                                  │
        再次确认 C 的必要性与数据   ↓
C1 Archive 导出 → C2 目标文件 → C3 最小离线入口 → C4/C5 模拟与验收
```

- A1/A2 可独立调查；都涉及 adapter 和 Trade 测试，编码时不得无边界并行编辑同一文件。
- A 与 B 可以独立开发，各模块指定唯一写入负责人；共享代码和协议先定稿再分工。
- 每个阶段使用新工作树和明确分支，不在多个工作树复制同一未完成实现。实施前检查已有改动和相关正在执行的计划。
- 建议逻辑提交：规则收敛、快照契约、工件契约及持久化、重放 CLI、Archive 导出、离线模拟；不要把全部变更混成一个提交。
- 每个提交只包含本任务文件；提交信息按 `moo-git-commit`。不混入格式清理、生成物漂移或无关重构。
- codeCR 结论要有文件/符号依据；超时、未完成不能视为通过。主 Agent 对关键业务不变量独立核验。

## 9. 验证、发布与回退

### 9.1 分层验证

| 层级 | 内容 | 能证明什么 |
| --- | --- | --- |
| 单元/契约 | 数量规则、快照 coverage、artifact codec、时间顺序 | 局部语义正确 |
| 模块/race | Trade、Strategy；C 再加 Archive 与共享格式 | 模块集成与被覆盖的并发路径 |
| 本地 E2E | 真 SQLite、真 Evaluate、真 Paper adapter/Reducer、录制输入 | 本地端到端链路，不是实盘 |
| 真实行情 Paper | 经正式部署后在线捕获/导出/重放，真实行情但模拟交易 | 真实数据与部署接线有效 |
| 交易所只读核对 | 快照端点范围、分页和 complete 依据 | adapter 假设有现场依据 |
| Testnet/实盘 | 仅在后续明确授权时执行 | 不能以本计划或 Paper 结果代替 |

仓库级命令在根目录运行；MooX 是多模块仓库，不以根目录 `go test ./...` 代替 workspace 验证。

```bash
git diff --check
make check-boundaries
make verify-pr
make verify
```

按影响面补充现有 `scripts/test/e2e/test-strategy-trade-event-e2e.sh`、`test-strategy-trade-logical-account-e2e.sh`。执行前阅读脚本配置，使用隔离测试环境，禁止默认接入生产。仅文档阶段不运行上述业务验证或脚本。

### 9.2 Schema 与数据保护

- A 预期不需更换业务数据库；coverage 是进程内契约，不为它新建持久化表。
- B 新增工件表，不删除旧结果。用现有 schema 初始化机制加入新表；不用 CREATE IF NOT EXISTS 掩盖不兼容列变更。
- 不做通用迁移框架。确需不兼容结构变更时，在该提交中列出备份、停止写入和转换步骤；未经确认不得清库。
- SQLite 在线备份用 backup API/命令或停写后的完整副本，包含一致性验证；不拷贝正在写入的单个 db 文件当备份。
- C 只创建临时离线库，不接受生产数据库路径，因此不需要生产回放数据迁移。

### 9.3 发布门

- A：Trade 本地回归、完整性 fake 契约、只读端点抽样、codeCR 完成后才能替换正式二进制；现网首轮保持原有 live 开关，不创建新交易行为。
- B：Strategy 正式制品、配置默认值、schema 和捕获计数一起验证；至少观察一个完整决策周期并成功离线重放，不能只检查进程 health。
- C：作为现有 CLI 的离线能力发布，不增加 SystemDeploy 常驻服务或管理台页面。
- 每次部署单独记录源码 SHA、制品 SHA-256、目标版本、重启状态、业务证据。源码测试通过不等于已部署。
- 失败回退使用验证过的二进制/配置与兼容数据库备份；先停相关自动执行，不在运行中交叉使用新旧 snapshot adapter 契约。

## 10. 完成清单与复杂度检查

### A/B 首版完成清单

- [ ] 数量规则唯一归属、无双重合约换算，原有 live/paper 行为保持。
- [ ] 不完整 REST 快照不会删除持仓、推进游标或将账户标记 Ready。
- [ ] 工件包含真实输入、实际 Definition、前态、日历调用和来源；未使用 pool 明确标记，不补读数据，不只保存 index 指针。
- [ ] 工件与结果原子提交；重投不覆盖；限额、保留期和不可重放原因可见。
- [ ] 单次与连续重放使用真实求值器，缺口/版本/损坏问题明确失败。
- [ ] codeCR 完成，主 Agent 已核验；模块、race、workspace/契约和相关 E2E 证据齐全。
- [ ] 发布与真实行情 Paper 验证单列，不把未执行部分写成已通过。
- [ ] 已更新模块说明与运维文档，用户能导出一个结果并读懂重放报告。

### 每次评审都问

1. 是否新增了本可以用普通函数完成的框架或常驻服务？若是，删回最小实现。
2. 是否因为“以后可能用”增加了 schema、插件接口或事件？若是，本轮移除。
3. 是否复制了 Strategy 求值、Trade 记账或 Quantity 规则？若是，回到现有唯一实现。
4. 是否把未记录的历史信息猜成已知事实？若是，明确 unsupported/assumption，不输出伪精确结果。
5. 是否能在当前阶段停止并获得实际收益？若不能，继续拆小。

**建议执行起点：先 A1、A2，再 B2-B4；完成 A/B 后使用一段时间，再决定是否启动 C。** 第一版的核心目标是“规则一致、事实可信、决策可重跑”，不是建造一个功能齐全的交易引擎。
