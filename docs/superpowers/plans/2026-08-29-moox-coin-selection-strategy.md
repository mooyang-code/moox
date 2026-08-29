# MooX 选币策略执行框架实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将当前 Python `run-once + quantity` Strategy V1 直接替换为由 `ViewFactorPeriodReady` 驱动、按全市场当期截面求值并向 Trade 输出 FULL 目标权重的声明式选币策略框架。

**Architecture:** 发布阶段将 `moox.strategy/v2` YAML 严格编译为不可变 `StrategyDef.compiled_json`，冻结 Source View、Factor Binding、Factor Result View 和列依赖；运行阶段动态解析 InstrumentPool、读取当期 Panel、使用纯 Go evaluator 生成规范权重。Strategy 通过 inbox/outbox 和单 SQLite 事务保证事件幂等，Trade 在消费 `LogicalAccountTargetRequested` 时同步冻结权益、参考价格和 raw quantity，再复用现有 TargetExecutor 做账户选择、步长量化和下单。

**Tech Stack:** Go 1.25、tRPC-Go、Protobuf、SQLite/GORM、NATS JetStream、Storage Metadata/DataView、FactorMgr、Trade LogicalAccount、Vue 3/Pinia/Arco Design、Vitest/Playwright。

---

## 实施基线

本计划只以 [选币策略执行框架设计](../../选币策略执行框架设计.md) 为目标基线，原计划内容不再继承。

- Factor 保持现有 `FactorDef + FactorBinding` 两层模型；不增加 `FactorInstance`，不修改 Factor 计算和完成事件协议。
- 一个 Runner 只有一个 Strategy、一个 UTC 日程、一份 InstrumentPoolRule 和最多一个 LogicalAccount。
- 不建设 UniverseSnapshot、MarketFrame 制品、readiness 进度表、Slot、WeightMerger、Python 策略入口或 Backtest。
- Strategy 只读取当期一行；历史窗口和 rolling 计算由 Factor 负责。
- 一期 readiness 只有 `strict`；缺依赖、缺行或缺列时不写 Result、不改 sequence、不写 outbox。
- Strategy 到 Trade 的跨模块目标只包含 `target_weight`；quantity 只在 Trade 内部同步换算后出现。
- 项目未上线，不保留 V1 schema、RPC、Proto 字段、Python SDK 或兼容适配层。
- 12 个 XBX 因子重写与导入属于独立交付，不进入本 Strategy 计划；本计划只依赖已启用的 `factor_id + Binding`。

## 实施口径补充

设计文档未完全展开、但编码前必须固定的口径如下：

1. 一期 DSL 只写 `factor_id`，因此被引用 Factor 必须只有一个 output；多 output Factor 在发布时拒绝。
2. 配置和目标权重最多 18 位小数。归一化和等权使用固定 18 位整数单位及稳定余数分配，余数按配置顺序或 `instrument_id` 升序分配。
3. 同一 instrument 同时入选 long/short 时求和净额；净额为零则省略，最终目标中 instrument 唯一。
4. 混合市场时 rank 仍在完整 Pool 上计算；short 候选只允许非 Spot。纯 Spot 池配置 short 在编译期失败。
5. `exchanges` 的配置顺序也是行情路由优先级；同一 instrument 多 venue 可用时选择第一个历史适格 venue，并写入 `debug_info`。
6. Runner 尚无 `last_result_id` 时，即使求值为空也生成一次 `rebalance`，向 Trade 明确发送 FULL 清仓目标。
7. 每次成功求值都保存当期 Result。权重与 current targets 相同则为 `hold`：更新最近结果/成功时间，但不改 targets、不增 sequence、不写 outbox。
8. Result 逻辑键为 `(runner_id,strategy_id,period_time)`。同 hash 返回现有行；不同 hash 重新求值并 UPSERT 最新行。只有 `rebalance` 增加 sequence 和重发目标。
9. 早于 Runner 最近成功周期的事件只记 inbox 后 ACK，防止旧信号倒灌；同周期允许 Factor latest-wins 重算。
10. Trade 增加同步账本 `t_logical_account_target_resolutions`，永久复用已接受 `target_id` 的首次权益、价格和 quantity；它不是异步 Resolver，不增加 PENDING/CAS/SUPERSEDED 状态机。
11. Trade 报价按 LogicalAccount member 的 `(priority,trading_account_id)` 选择第一个 ready member；TargetExecutor 仍负责最终账户选择、contract value、step、minimum 和容量。
12. 一期每个 Result 逻辑键只保留 latest-wins 当前行，不增加 revision/history 表；ready event ID 只放 `debug_info.source_event_ids`，不参与 input hash。

## 目标文件图

```text
modules/strategy/
  internal/config/{spec.go,parse.go,validate.go}
  internal/quant/{decimal.go,normalize.go}
  internal/compiler/{compiler.go,types.go,validate_dependencies.go}
  internal/factorio/client.go
  internal/storageio/{catalog.go,view.go,auth.go}
  internal/input/{types.go,pool.go,panel.go,hash.go}
  internal/selection/{rank.go,filter.go,select.go,weight.go,evaluator.go}
  internal/trigger/{schedule.go,processor.go,consumer.go,jetstream.go}
  internal/{domain,registry,store,rpc,bootstrap}/...
  proto/strategy.proto
  schema/strategy.sql

modules/trade/
  internal/application/equity/service.go
  internal/application/target/{weight_resolver.go,price.go,executor.go}
  internal/eventconsumer/target.go
  internal/infra/store/{target.go,target_resolution.go,equity_point.go}
  schema/logical_account.sql

modules/collector/internal/sources/binance/{symbol.go,symbol_test.go}
packages/tradeeventpb/trade_events.proto
packages/events/{validation.go,logical_account_target_test.go}
web/src/api/{strategy-types.ts,strategy.ts,strategy.test.ts}
web/src/store/modules/strategy.ts
web/src/views/strategy/...
```

删除：

```text
modules/strategy/internal/action/
modules/strategy/internal/engine/
modules/strategy/pysdk/
modules/strategy/pyworker/
modules/strategy/strategies/
```

## 依赖顺序

```text
Task 1 -> Task 2
Task 1 -> Task 4 -> Task 5 -> Task 6
Task 3 -> Task 4
Task 7 -> Task 8
Task 2 + Task 5 + Task 6 + Task 8 -> Task 9
Task 9 -> Task 10 -> Task 11 -> Task 12
```

Task 8 是 Trade event wire contract 与 Trade consumer 的原子切换；Task 9 是 Strategy Proto、持久化、RPC 与既有调用方的原子切换。两者都禁止中间提交 quantity/weight 双写适配。

### Task 1: 建立严格 DSL 与确定性数值内核

**Files:**
- Create: `modules/strategy/internal/config/spec.go`
- Create: `modules/strategy/internal/config/parse.go`
- Create: `modules/strategy/internal/config/validate.go`
- Create: `modules/strategy/internal/config/parse_test.go`
- Create: `modules/strategy/internal/quant/decimal.go`
- Create: `modules/strategy/internal/quant/normalize.go`
- Create: `modules/strategy/internal/quant/normalize_test.go`
- Modify: `modules/strategy/go.mod`
- Modify: `modules/strategy/go.sum`

- [ ] **Step 1: 写 DSL 解析失败测试**

覆盖未知字段、错误 api version/kind、空 source view、非法 frequency/every、`every` 非 data frequency 整数倍、空/重复 factor、非法 market/operator/value type、非法 count/fraction、负权重、纯 Spot short、readiness 非 strict。

```go
func TestParseRejectsUnknownAndUnsupportedFields(t *testing.T) {
    cases := []string{
        "api_version: moox.strategy/v2\nkind: coin_selection\nunknown: true\n",
        "api_version: moox.strategy/v1\nkind: coin_selection\n",
        "api_version: moox.strategy/v2\nkind: python\n",
    }
    for _, raw := range cases {
        _, err := config.Parse([]byte(raw))
        require.Error(t, err)
    }
}

func TestValidateRejectsPureSpotShort(t *testing.T) {
    spec := validSpec()
    spec.InstrumentPool.Markets = []string{"spot"}
    spec.Short = &config.SideSpec{SideWeight: "1", Scores: validScores(), Selection: count(1)}
    require.ErrorContains(t, config.Validate(spec), "spot-only instrument_pool cannot enable short")
}
```

- [ ] **Step 2: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/config ./internal/quant -count=1
```

Expected: FAIL，`Parse/Validate` 和 quant 包尚不存在。

- [ ] **Step 3: 定义版本化配置类型**

```go
type Spec struct {
    APIVersion     string             `yaml:"api_version"`
    Kind           string             `yaml:"kind"`
    Input          InputSpec          `yaml:"input"`
    InstrumentPool InstrumentPoolRule `yaml:"instrument_pool"`
    Schedule       ScheduleSpec       `yaml:"schedule"`
    Readiness      ReadinessSpec      `yaml:"readiness"`
    Long           *SideSpec          `yaml:"long,omitempty"`
    Short          *SideSpec          `yaml:"short,omitempty"`
}

type InputSpec struct {
    SourceViewID  string      `yaml:"source_view_id"`
    DataFrequency string      `yaml:"data_frequency"`
    Factors       []FactorRef `yaml:"factors"`
}

type InstrumentPoolRule struct {
    Exchanges         []string `yaml:"exchanges"`
    Markets           []string `yaml:"markets"`
    QuoteAssets       []string `yaml:"quote_assets"`
    Include           []string `yaml:"include"`
    Exclude           []string `yaml:"exclude"`
    MinHistoryPeriods int      `yaml:"min_history_periods"`
}

type ScoreSpec struct {
    FactorID string `yaml:"factor_id"`
    Order    string `yaml:"order"`
    Weight   string `yaml:"weight"`
}

type FilterSpec struct {
    FactorID string      `yaml:"factor_id"`
    Operator string      `yaml:"operator"`
    Value    interface{} `yaml:"value"`
}

type SelectionSpec struct {
    Count    *int    `yaml:"count,omitempty"`
    Fraction *string `yaml:"fraction,omitempty"`
}

type SideSpec struct {
    SideWeight  string        `yaml:"side_weight"`
    Scores      []ScoreSpec   `yaml:"scores"`
    PreFilters  []FilterSpec  `yaml:"pre_filters"`
    Selection   SelectionSpec `yaml:"selection"`
    PostFilters []FilterSpec  `yaml:"post_filters"`
}
```

`parse.go` 使用 `yaml.Decoder.KnownFields(true)` 并拒绝第二份 YAML document。

- [ ] **Step 4: 实现固定 18 位权重数值**

`quant.Decimal` 内部保存 `big.Int` 的 `1e-18` 单位，只接受规范十进制；不接受指数、NaN、Inf、前导正号或超过 18 位小数。

```go
func Parse(string) (Decimal, error)
func Zero() Decimal
func One() Decimal
func (Decimal) Add(Decimal) Decimal
func (Decimal) Sub(Decimal) Decimal
func (Decimal) Neg() Decimal
func (Decimal) Cmp(Decimal) int
func (Decimal) String() string
func NormalizeStable(values []Decimal) ([]Decimal, error)
func DivideStable(total Decimal, orderedKeys []string) map[string]Decimal
```

`NormalizeStable` 用最大余数法；相同余数按配置顺序决胜。`DivideStable` 先排序 key，再逐单位分配余数，确保和严格等于 total。

- [ ] **Step 5: 实现校验和默认值**

固定规则：`api_version=moox.strategy/v2`、`kind=coin_selection`、`schedule.every` 默认 data frequency、readiness 默认且只接受 strict、include/exclude 规范化为完整大写 ID、至少启用一侧、启用侧必须有 score/selection/正权重、count 为正整数、fraction 在 `(0,1]`。

frequency 复用 `packages/report.NormalizeDatasetFrequency` 和 `ParseDatasetFrequency`。

- [ ] **Step 6: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/config ./internal/quant -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/strategy/internal/config modules/strategy/internal/quant modules/strategy/go.mod modules/strategy/go.sum
git commit -m "feat(strategy): define coin selection manifest"
```

Expected: PASS 后提交。

### Task 2: 编译 Factor 与 View 依赖

**Files:**
- Create: `modules/strategy/internal/compiler/types.go`
- Create: `modules/strategy/internal/compiler/compiler.go`
- Create: `modules/strategy/internal/compiler/validate_dependencies.go`
- Create: `modules/strategy/internal/compiler/compiler_test.go`
- Create: `modules/strategy/internal/factorio/client.go`
- Create: `modules/strategy/internal/factorio/client_test.go`
- Create: `modules/strategy/internal/storageio/catalog.go`
- Create: `modules/strategy/internal/storageio/auth.go`
- Create: `modules/strategy/internal/storageio/catalog_test.go`
- Modify: `modules/strategy/go.mod`
- Modify: `modules/strategy/go.sum`

- [ ] **Step 1: 写编译失败矩阵**

覆盖 Factor 不存在/disabled、多 output、缺匹配 Binding、Binding 非 enabled 或 space/source/frequency 不匹配、Source/Result View 非 active、Factor output 列找不到、Source primary dataset 不支持 frequency。

```go
func TestCompileFreezesSingleOutputFactorBinding(t *testing.T) {
    compiled, err := compiler.Compile(context.Background(), validSpec(), "crypto", completeFakeDeps())
    require.NoError(t, err)
    require.Equal(t, "factor_bias.bias_20", compiled.Factors[0].ColumnName)
    require.Equal(t, []string{"factor_bias_view"}, compiled.Dependencies.FactorResultViewIDs)
}
```

- [ ] **Step 2: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/compiler ./internal/factorio ./internal/storageio -count=1
```

Expected: FAIL，编译器和依赖客户端尚不存在。

- [ ] **Step 3: 定义 compiled JSON 契约**

```go
type CompiledStrategy struct {
    APIVersion string
    Kind string
    SpaceID string
    SourceView CompiledView
    InstrumentPool config.InstrumentPoolRule
    Schedule CompiledSchedule
    Readiness string
    Factors []CompiledFactor
    Long *CompiledSide
    Short *CompiledSide
    Dependencies Dependencies
}

type CompiledFactor struct {
    FactorID string
    SourceHash string
    BindingID string
    ResultDatasetID string
    ResultViewID string
    Output string
    ColumnName string
}
```

Factors 按 factor_id 排序，Result View IDs 去重排序，使用显式 JSON tag 和 `json.Marshal` 生成确定性 compiled bytes/hash。

- [ ] **Step 4: 实现 FactorMgr 与 Storage catalog 客户端**

Factor 客户端只封装 `GetFactor/ListBindings` 并读完分页；Storage 客户端封装 `GetView/ListViewColumns/GetDataset/ListDatasetSubjects/GetSubject`。Factor 列必须同时匹配 ViewColumn attributes：

```text
origin_factor_id == factor_id
factor_output == FactorDef.outputs[0]
```

运行期使用冻结的实际 `column_name`，不猜列名、不访问 Factor SQLite。

- [ ] **Step 5: 实现 Compile 和 Enable 复核**

顺序：解析配置 -> Source View/Dataset -> 收集 factor_id -> enabled Factor/Binding -> Result ViewColumn -> 归一化 score/side weight -> compiled JSON/hash。

```go
func ValidateFrozenDependencies(
    ctx context.Context,
    compiled CompiledStrategy,
    factors factorio.Catalog,
    storage storageio.Catalog,
) error
```

Enable 时只复核冻结 ID/status/schema，不重新选择其他 Binding。

- [ ] **Step 6: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/compiler ./internal/factorio ./internal/storageio -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/strategy/internal/compiler modules/strategy/internal/factorio modules/strategy/internal/storageio modules/strategy/go.mod modules/strategy/go.sum
git commit -m "feat(strategy): compile factor and view dependencies"
```

Expected: PASS 后提交。

### Task 3: 统一 Storage 与 Trade 的 canonical instrument_id

**Files:**
- Modify: `modules/collector/internal/sources/binance/symbol.go`
- Modify: `modules/collector/internal/sources/binance/symbol_test.go`
- Modify: `modules/collector/internal/sources/binance/storage_config_test.go`
- Modify: `modules/collector/internal/jobs/kline/handler_test.go`
- Modify: `modules/collector/internal/jobs/kline/planner_test.go`
- Modify: `modules/collector/internal/planner/storagesource/source_test.go`

- [ ] **Step 1: 写现货与永续不冲突测试**

```go
func TestBuildSymbolRegisterRequestUsesMarketQualifiedSubjectID(t *testing.T) {
    symbol := &exchange.SymbolInfo{BaseAsset: "BTC", QuoteAsset: "USDT", Status: "TRADING"}
    spot := buildSymbolRegisterRequest(symbol, "crypto", "spot_dataset", spotBinding())
    swap := buildSymbolRegisterRequest(symbol, "crypto", "swap_dataset", swapBinding())
    require.Equal(t, "BTC-USDT-SPOT", spot.GetSubject().GetSubjectId())
    require.Equal(t, "BTC-USDT-SWAP", swap.GetSubject().GetSubjectId())
    require.Equal(t, "binance", spot.GetSubject().GetAttributes()["exchange"])
}
```

- [ ] **Step 2: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector
go test ./internal/sources/binance -run 'SubjectID|SymbolRegister' -count=1
```

Expected: FAIL，当前 subject ID 为 `BTC-USDT`。

- [ ] **Step 3: 改为完整 instrument identity**

`normalizedSubjectID` 同时接收 StorageBinding，返回 `BASE-QUOTE-SPOT|SWAP`。所有 symbol rows、Subject、DatasetSubject 使用完整 ID；Subject attributes 写 `instrument_id/exchange/market_type/base_asset/quote_asset/external_symbol`。SubjectSymbol 继续保存交易所原始 symbol。

所有测试 fixture 改为完整 ID，不保留旧 ID 兼容。部署后重新采集/导入本地历史数据，不写迁移脚本。

- [ ] **Step 4: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector
go test ./internal/sources/binance ./internal/jobs/kline ./internal/planner/storagesource -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/collector/internal
git commit -m "refactor(collector): qualify crypto instrument ids by market"
```

Expected: PASS 后提交。

### Task 4: 解析 InstrumentPool、读取当期 Panel 并生成 input hash

**Files:**
- Create: `modules/strategy/internal/input/types.go`
- Create: `modules/strategy/internal/input/pool.go`
- Create: `modules/strategy/internal/input/pool_test.go`
- Create: `modules/strategy/internal/input/panel.go`
- Create: `modules/strategy/internal/input/panel_test.go`
- Create: `modules/strategy/internal/input/hash.go`
- Create: `modules/strategy/internal/input/hash_test.go`
- Create: `modules/strategy/internal/storageio/view.go`
- Create: `modules/strategy/internal/storageio/view_test.go`

- [ ] **Step 1: 写 Pool 与 strict Panel 测试**

覆盖 exchange/market/quote/include/exclude、inactive subject、distinct history、venue 优先级、新上市排除、稳定排序；Panel 覆盖 `[period,period+1ns)`、完整 selector、分页、重复行、缺行/列、非数值、NaN/Inf、`complete=false`。

```go
func TestLoadPanelRejectsMissingPoolRowWithoutShrinkingCrossSection(t *testing.T) {
    _, err := loader.Load(ctx, compiled, pool("BTC-USDT-SPOT", "ETH-USDT-SPOT"), period)
    require.ErrorContains(t, err, "missing current rows: ETH-USDT-SPOT")
}
```

- [ ] **Step 2: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/input ./internal/storageio -count=1
```

Expected: FAIL，输入构造器尚不存在。

- [ ] **Step 3: 实现候选和历史适格检查**

```go
type PoolItem struct {
    InstrumentID string
    SubjectID string
    Exchange string
    Market string
    QuoteAsset string
    SeriesTag string
}
```

候选来自 Source View primary dataset 的 active DatasetSubject/Subject。历史查询窗口：

```go
start := period.Add(-time.Duration(minHistory-1) * dataFrequency)
end := period.Add(time.Nanosecond)
```

每个 `(subject_id,series_tag)` 只计 distinct data_time。多 venue 都适格时按配置顺序选第一个；历史不足写 `Ineligible` 而非运行错误。

- [ ] **Step 4: 实现当期多 View Panel 合并**

Source View 和每个 Factor Result View 分别查询，selector 必须包含 `space_id/dataset_id/subject_id/freq/series_tag`，时间窗统一为单周期。以 instrument_id 左连接，Factor 列按 compiled column_name 映射回 factor_id。

```go
type PanelRow struct {
    Instrument PoolItem
    Values map[string]*big.Rat
}
type Panel struct { Period time.Time; Rows []PanelRow }
```

TypedValue 只接受 int/double/string decimal；double 先拒绝 NaN/Inf，再用 `strconv.FormatFloat` 规范化。

- [ ] **Step 5: 实现规范 input hash**

SHA-256 输入只含 source hash、compiled hash、period、排序后 pool 和排序后 panel values；不含 ready event ID、ready_at、查询耗时或分页游标。

- [ ] **Step 6: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/input ./internal/storageio -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/strategy/internal/input modules/strategy/internal/storageio
git commit -m "feat(strategy): resolve instrument pool and current panel"
```

Expected: PASS 后提交。

### Task 5: 实现纯 Go 横截面选币 evaluator

**Files:**
- Create: `modules/strategy/internal/selection/rank.go`
- Create: `modules/strategy/internal/selection/filter.go`
- Create: `modules/strategy/internal/selection/select.go`
- Create: `modules/strategy/internal/selection/weight.go`
- Create: `modules/strategy/internal/selection/evaluator.go`
- Create: `modules/strategy/internal/selection/evaluator_test.go`
- Create: `modules/strategy/internal/selection/golden_test.go`

- [ ] **Step 1: 写排名和流水线顺序测试**

```go
func TestEvaluatorRanksFullPoolBeforePreFilter(t *testing.T) {
    panel := panelWithValues(
        row("A-USDT-SPOT", "score", "1", "liquidity", "1"),
        row("B-USDT-SPOT", "score", "2", "liquidity", "100"),
        row("C-USDT-SPOT", "score", "2", "liquidity", "100"),
    )
    got, err := selection.Evaluate(compiledWithPreFilter(), panel)
    require.NoError(t, err)
    require.Equal(t, []int{1, 2, 2}, got.Debug.ScoreRanks["score"])
}
```

测试锁定 `method=min`，不能先用 instrument_id 拆开并列 rank；最终截取才用 `(score,instrument_id)`。

- [ ] **Step 2: 写过滤、选币和权重矩阵**

覆盖 ascending/descending、多 score 归一化、pre/post AND、value/percentile、所有比较符、count/fraction、post 不回补、short 非 Spot 候选、long/short 等权、重叠净额、空结果、gross/net。

percentile 固定为 `method=min rank / pool_size`，并在完整未过滤 Pool 上预计算。

- [ ] **Step 3: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/selection -count=1
```

Expected: FAIL，evaluator 尚不存在。

- [ ] **Step 4: 实现无状态流水线**

执行顺序固定：score rank -> pre filters -> stable select -> post filters -> side weight。两个 Side 独立评分，但所有 rank/percentile 的 scope 都是完整 Panel。

```go
type TargetWeight struct {
    InstrumentID string `json:"instrument_id"`
    TargetWeight string `json:"target_weight"`
}
type Evaluation struct {
    Targets []TargetWeight
    DebugInfo DebugInfo
}
```

使用 Task 1 的 `DivideStable` 分配方向内权重；short 取负；按 instrument 求和并删除零。DebugInfo 至少保存 pool、venue route、history ineligible、各阶段数量、long/short 入选、gross、net。

- [ ] **Step 5: 加入稳定 golden fixture**

使用 6 个 instrument、并列值、pre/post filter 和 long/short；随机打乱输入行 100 次，targets/debug JSON 必须逐字节相同。

- [ ] **Step 6: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/selection -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/strategy/internal/selection
git commit -m "feat(strategy): evaluate cross sectional coin selection"
```

Expected: PASS 后提交。

### Task 6: 实现调度、Runner 串行和 ready 处理器内核

**Files:**
- Create: `modules/strategy/internal/trigger/schedule.go`
- Create: `modules/strategy/internal/trigger/schedule_test.go`
- Create: `modules/strategy/internal/trigger/processor.go`
- Create: `modules/strategy/internal/trigger/processor_test.go`
- Create: `modules/strategy/internal/trigger/consumer.go`
- Create: `modules/strategy/internal/trigger/consumer_test.go`
- Create: `modules/strategy/internal/trigger/jetstream.go`
- Create: `modules/strategy/internal/trigger/jetstream_test.go`

- [ ] **Step 1: 写 UTC 对齐和状态矩阵测试**

```go
func TestAlignedUsesUnixEpochAsUTCAnchor(t *testing.T) {
    require.True(t, trigger.Aligned(time.Unix(48*3600, 0), 24*time.Hour))
    require.False(t, trigger.Aligned(time.Unix(48*3600+3600, 0), 24*time.Hour))
}
```

再覆盖重复 message、非 complete、无匹配 Runner、未对齐、旧周期、依赖未齐、strict 缺数、same/changed hash、周期 N 不齐但 N+1 成功、同 Runner 串行、不同 Runner 并行。

- [ ] **Step 2: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/trigger -count=1
```

Expected: FAIL，trigger 包尚不存在。

- [ ] **Step 3: 实现 Processor 边界**

```go
type Repository interface {
    InboxContains(context.Context, string) (bool, error)
    ListEnabledForFactorView(context.Context, string, string, string) ([]RunnerContext, error)
    LatestSuccessfulPeriod(context.Context, string) (time.Time, bool, error)
    CommitEvaluation(context.Context, CommitRequest) (CommitOutcome, error)
    RecordRunnerFailure(context.Context, string, error, time.Time) error
    MarkInbox(context.Context, InboxMessage) error
}
```

Processor 使用 `sync.Map[runner_id]*sync.Mutex` 串行单 Runner，不等待未齐依赖，不创建 run 状态。协议/格式错误 TERM；Storage/Factor/SQLite/JetStream 暂时错误 RETRY；业务 no-op 和 strict skip ACK。

- [ ] **Step 4: 实现 message 级 inbox 时机**

先查 inbox；处理该 message 匹配的全部 Runner。只有全部 Runner 都得到 ACK/业务 no-op 后才写 inbox 并 ACK broker。任一 Runner RETRY 时不写 inbox；崩溃重放由 Result input hash 吸收已提交 Runner。

- [ ] **Step 5: 实现 JetStream consumer**

复用 Factor pull/ACK 模式，durable 固定为 `strategy_factor_ready_v1`，使用现有 `events.DecodeViewFactorPeriodReadyWithContentType`，不新增 Factor/Storage 事件协议。

- [ ] **Step 6: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./internal/trigger -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/strategy/internal/trigger
git commit -m "feat(strategy): process factor view ready events"
```

Expected: PASS 后提交。

### Task 7: 建立 Trade 同步权重换算与永久幂等账本

**Files:**
- Create: `modules/trade/internal/application/target/weight_resolver.go`
- Create: `modules/trade/internal/application/target/weight_resolver_test.go`
- Create: `modules/trade/internal/infra/store/target_resolution.go`
- Create: `modules/trade/internal/infra/store/target_resolution_test.go`
- Modify: `modules/trade/internal/application/equity/service.go`
- Modify: `modules/trade/internal/application/equity/service_test.go`
- Modify: `modules/trade/internal/infra/store/equity_point.go`
- Modify: `modules/trade/schema/logical_account.sql`
- Modify: `modules/trade/schema/schema_test.go`

- [ ] **Step 1: 写实时权益、换算和账本测试**

从 `sampleLogicalAccounts` 抽出：

```go
func (s *Service) ResolveLogicalAccountEquity(
    ctx context.Context,
    spaceID string,
    logicalAccountID string,
) (store.EquityPointRecord, error)
```

覆盖 live/paper、Spot 估值、多 member 汇总、disabled/unready、过期 snapshot。WeightResolver 覆盖正/负权重、Spot 负权重拒绝、空 FULL 无需行情、确定报价 member、过期价格、无支持 instrument。

```go
func TestResolveWeightsFreezesEquityPriceAndRawBaseQuantity(t *testing.T) {
    got, err := resolver.Resolve(ctx, request("10000", "BTC-USDT-SPOT", "0.25", "20000"))
    require.NoError(t, err)
    require.Equal(t, "0.125", got.Targets[0].Quantity)
}
```

账本覆盖同 target 同 payload复用、同 target 不同 payload conflict、旧 target 被覆盖后重投仍读首次结果、账本/current target 事务回滚。

- [ ] **Step 2: 运行测试并确认失败**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade
go test ./internal/application/equity ./internal/application/target ./internal/infra/store -run 'Equity|Weight|Resolution' -count=1
```

Expected: FAIL，resolver 和 resolution store 尚不存在。

- [ ] **Step 3: 增加同步账本 schema**

```sql
CREATE TABLE IF NOT EXISTS t_logical_account_target_resolutions (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT,
    c_equity_source_time INTEGER,
    c_reference_prices_json TEXT NOT NULL DEFAULT '[]',
    c_targets_json TEXT NOT NULL,
    c_priced_at INTEGER NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_target_id),
    UNIQUE (c_space_id, c_logical_account_id, c_command_sequence),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
);
```

weights、prices、targets 均按 instrument_id 排序并保存 canonical decimal。

- [ ] **Step 4: 实现实时权益、报价路由和 raw quantity**

权益复用现有 snapshot freshness/Spot valuation，不读分钟曲线。报价候选只取 enabled/ready member 和 TRADING instrument，按 priority/id 排序。

公式：

```text
notional = equity * target_weight
raw_base_quantity = notional / reference_price
```

这里只规范化 decimal 和符号；不做 floor、contract multiplier、minimum notional 或 capacity。

- [ ] **Step 5: 实现原子 AcceptResolvedTarget**

在 `LockLogicalAccount` 内：查 resolution -> 查 current sequence -> 首次请求同步 resolve -> transaction 插入 resolution 并调用 `Tx.AcceptLogicalAccountTarget`。事务内再次核验 sequence，避免 resolve 期间新 sequence 抢先落库。

- [ ] **Step 6: 运行测试、schema 校验并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade
go test ./internal/application/equity ./internal/application/target ./internal/infra/store ./schema -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git diff --check
git add modules/trade/internal/application modules/trade/internal/infra/store modules/trade/schema
git commit -m "feat(trade): resolve target weights synchronously"
```

Expected: PASS 后提交。

### Task 8: 原子切换权重 wire contract 与 Trade consumer

**Files:**
- Modify: `packages/tradeeventpb/trade_events.proto`
- Modify: `packages/tradeeventpb/trade_events.pb.go` (generated)
- Modify: `packages/events/validation.go`
- Modify: `packages/events/validation_test.go`
- Modify: `packages/events/logical_account_target_test.go`
- Modify: `modules/trade/internal/eventconsumer/target.go`
- Modify: `modules/trade/internal/eventconsumer/target_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap_test.go`

- [ ] **Step 1: 写新 descriptor 和校验测试**

```go
func TestLogicalAccountTargetUsesWeightOnly(t *testing.T) {
    fields := (&tradeeventpb.InstrumentTarget{}).ProtoReflect().Descriptor().Fields()
    require.NotNil(t, fields.ByName("target_weight"))
    require.Nil(t, fields.ByName("quantity"))
}
```

校验 target_weight 为有限规范十进制、instrument 唯一、空 targets 合法、logical account 与 EventMessage subject 一致。

- [ ] **Step 2: 修改 Proto 并生成**

```protobuf
message InstrumentTarget {
  string instrument_id = 1;
  string target_weight = 2;
}
```

不保留 deprecated quantity 或 reserved 声明；事件名保持 `LogicalAccountTargetRequested` / `trade.target.requested`。

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/tradeeventpb
make all
```

- [ ] **Step 3: 改写 Trade consumer**

顺序：decode -> Gate -> 查 resolution -> current sequence 预检 -> `WeightResolver.Resolve` -> `AcceptResolvedTarget` -> Wake -> ACK。

错误分类：

```text
协议/decimal/owner/Spot负权重/同target不同payload -> TERM
无ready member、权益/价格暂不可用、数据库或adapter暂时失败 -> RETRY
已接受target、stale sequence -> ACK，不重新定价、不回写旧current target
```

Bootstrap 复用已有 equity service、ExchangePriceSource、Store 和 adapter registry，不创建第二套连接。

- [ ] **Step 4: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events
go test ./... -run LogicalAccountTarget -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade
go test ./internal/eventconsumer ./internal/bootstrap -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add packages/tradeeventpb packages/events modules/trade/internal/eventconsumer modules/trade/internal/bootstrap
git commit -m "refactor(events): replace trade quantity target with weight"
```

Expected: PASS 后提交。

### Task 9: 原子切换 Strategy V2 控制面、持久化和自动运行

**Files:**
- Modify: `modules/strategy/proto/strategy.proto`
- Modify: `modules/strategy/proto/strategygen/strategy.pb.go` (generated)
- Modify: `modules/strategy/proto/strategygen/strategy.trpc.go` (generated)
- Modify: `modules/strategy/proto/strategygen/validation.go`
- Modify: `modules/strategy/proto/strategy_contract_test.go`
- Modify: `modules/strategy/internal/domain/types.go`
- Modify: `modules/strategy/internal/domain/types_test.go`
- Modify: `modules/strategy/schema/strategy.sql`
- Modify: `modules/strategy/schema/schema_test.go`
- Modify: `modules/strategy/internal/store/database.go`
- Modify: `modules/strategy/internal/store/database_test.go`
- Modify: `modules/strategy/internal/store/store.go`
- Modify: `modules/strategy/internal/store/strategies.go`
- Modify: `modules/strategy/internal/store/runners.go`
- Modify: `modules/strategy/internal/store/runner_queries.go`
- Modify: `modules/strategy/internal/store/results.go`
- Modify: `modules/strategy/internal/store/result_commit_test.go`
- Create: `modules/strategy/internal/store/inbox.go`
- Create: `modules/strategy/internal/store/inbox_test.go`
- Modify: `modules/strategy/internal/registry/service.go`
- Modify: `modules/strategy/internal/registry/service_test.go`
- Modify: `modules/strategy/internal/rpc/service.go`
- Modify: `modules/strategy/internal/rpc/service_test.go`
- Modify: `modules/strategy/internal/rpc/frontend_service.go`
- Modify: `modules/strategy/internal/rpc/frontend_service_test.go`
- Modify: `modules/strategy/internal/bootstrap/config.go`
- Modify: `modules/strategy/internal/bootstrap/config_test.go`
- Modify: `modules/strategy/internal/bootstrap/bootstrap.go`
- Modify: `modules/strategy/internal/bootstrap/health_test.go`
- Modify: `modules/strategy/internal/bootstrap/metrics_reporter.go`
- Modify: `modules/strategy/config/app.yaml`
- Modify: `modules/strategy/config/trpc_go.yaml`
- Modify: `modules/strategy/cmd/cli/main.go`
- Modify: `modules/strategy/cmd/cli/main_test.go`
- Delete: `modules/strategy/internal/action/`
- Delete: `modules/strategy/internal/engine/`
- Delete: `modules/strategy/pysdk/`
- Delete: `modules/strategy/pyworker/`
- Delete: `modules/strategy/strategies/`
- Modify: `modules/strategy/go.mod`
- Modify: `modules/strategy/go.sum`

- [ ] **Step 1: 写 V2 Proto、schema 和 Commit 状态矩阵**

Proto 目标：

```protobuf
message InstrumentTarget { string instrument_id = 1; string target_weight = 2; }
message Strategy {
  string strategy_id = 1;
  string name = 2;
  string kind = 3;
  string manifest_yaml = 4;
  string compiled_json = 5;
  string source_hash = 6;
  string created_at = 7;
}
message StrategyRunner {
  string runner_id = 1;
  string strategy_id = 2;
  string space_id = 3;
  string source_view_id = 4;
  string frequency = 5;
  string logical_account_id = 6;
  string status = 7;
  repeated InstrumentTarget current_targets = 8;
  int64 command_sequence = 9;
  string last_result_id = 10;
  string last_success_at = 11;
  string last_error = 12;
}
```

Result 使用 `period_time/targets/debug_info_json/input_hash/action/optional command_sequence`；删除 namespace/trigger_bar_time/output_json。服务删除 RunOnce/GetEngineStatus。

Schema 只允许 `t_strategies/t_strategy_runners/t_strategy_results/t_strategy_outbox/t_strategy_inbox`；禁止 source_code、params_json、namespace、quantity、snapshot、slot、lease。Result unique 为 `(runner_id,strategy_id,period_time)`。

Commit 测试覆盖首次空目标 rebalance、same hash、changed hash + same targets 的 hold、changed targets、观察 Runner、空 FULL 清仓和事务回滚。

- [ ] **Step 2: 生成 Proto**

CreateStrategyReq 只接受 `strategy_id/name/manifest_yaml`；CreateRunnerReq 只接受 `runner_id/strategy_id/space_id/logical_account_id`。source view/frequency 由 compiled Strategy 派生。UpdateRunnerReq 只允许 DISABLED Runner 更换 strategy/account。

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy/proto
make all
```

Expected: generated service 不含 RunOnce/GetEngineStatus。

- [ ] **Step 3: 重写 domain、schema 和 store**

删除 ExecutionRequest、Python Output、Quantity、Namespace。Strategy 保存 compiled JSON；Runner 保存权重 current targets；Result 保存 latest row。

事务顺序：锁定 Runner -> 读取同逻辑 Result -> same hash 返回 -> canonical 比较 targets -> UPSERT Result -> 更新 last result/success/error -> rebalance 时更新 targets/sequence -> 执行 Runner 写 outbox。`target_id=result_id`；观察 Runner不发 outbox。

`t_strategy_inbox.c_message_id` 为主键，并保存 event_id/result_view_id/space/frequency/period/processed_at 供排障。

- [ ] **Step 4: 重写 Registry 和 Runner 生命周期**

CreateStrategy 调用 Task 2 compiler，`source_hash=sha256(manifest_yaml bytes)`，保存 immutable StrategyDef。重复 strategy_id 仅在所有字段一致时幂等，否则 conflict。

Enable 顺序：Strategy -> frozen dependencies -> source/frequency -> 至少一个 metadata candidate -> validate/claim LogicalAccount -> SQLite enable。任一步失败保持 DISABLED；claim 成功但 DB 失败时补偿 release。

- [ ] **Step 5: 删除 Python V1**

删除目录和 pyruntime 依赖。CLI 改为 `strategy validate <manifest.yaml> --space-id <id>` 并通过配置的 Factor/Storage 目标真实 compile；删除 run-once。Bootstrap 不再检查 Python binary/worker，不再暴露 worker health。

- [ ] **Step 6: 接入输入、evaluator、trigger 和配置**

启动顺序：DB/schema -> Factor/Storage clients -> outbox runtime -> ready consumer -> RPC/health；关闭反向。

```yaml
factor:
  target: ip://127.0.0.1:11201
  timeout: 3s
storage:
  gateway_target: ip://127.0.0.1:11003
  gateway_node_id: storage-gateway
  key_id: strategy
  hmac_key_file: ~/.config/moox/storage/strategy.key
  timeout: 5s
eventbus:
  consumer_name: strategy_factor_ready_v1
  fetch_max_wait: 1s
  execution_timeout: 30s
```

Storage AuthInfo 复用 `requestauth` HMAC，不把 key 写入日志或 compiled JSON。

- [ ] **Step 7: 实现旧周期保护、指标和 health**

`LatestSuccessfulPeriod` 通过 Runner.last_result_id 查询 Result.period_time；同周期允许重算，较旧周期只记 inbox。strict input error 只 `RecordRunnerFailure`。

指标：`strategy_runs_total{status}`、run duration、pool size、selected by side、gross、outbox pending。日志含 space/runner/period/result/trace。Health 使用 database/eventbus/ready consumer/outbox 字段，删除 Python 字段。

- [ ] **Step 8: 运行 Strategy 定向和全模块测试**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./proto/... ./schema ./internal/domain ./internal/config ./internal/compiler ./internal/input ./internal/selection ./internal/trigger ./internal/store ./internal/registry ./internal/rpc ./internal/bootstrap -count=1
go test ./... -count=1
```

Expected: PASS。

- [ ] **Step 9: 搜索 V1 残留并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n 'RunOnce|GetEngineStatus|source_code|python_worker|target_quantity|"quantity"|FactorInstance|UniverseSnapshot|WeightMerger|RebalanceSlot' modules/strategy
git add modules/strategy
git commit -m "refactor(strategy): replace python runner with weighted coin selection"
```

Expected: 业务代码无命中，提交成功。

### Task 10: 完成 EventBus ACL、Storage 鉴权和默认部署

**Files:**
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `examples/setup/default/service-deployments.yaml`
- Modify: `scripts/test-eventbus-topology.sh`
- Modify: `scripts/test-strategy-deploy.sh`
- Modify: `scripts/test-strategy-deploy-e2e.sh`

- [ ] **Step 1: 写 ACL 与 Storage caller contract**

Strategy credential 必须允许发布 `moox.trade.target.requested.v1.>`，并只对 `strategy_factor_ready_v1` durable 拥有 create/info/fetch/ack；subscribe 允许 `_INBOX.>`。

ACL 精确包含：

```text
$JS.API.STREAM.NAMES
$JS.API.CONSUMER.INFO.*.strategy_factor_ready_v1
$JS.API.CONSUMER.CREATE.MOOX_STORAGE.strategy_factor_ready_v1
$JS.API.CONSUMER.CREATE.MOOX_STORAGE.strategy_factor_ready_v1.>
$JS.API.CONSUMER.DURABLE.CREATE.MOOX_STORAGE.strategy_factor_ready_v1
$JS.API.CONSUMER.MSG.NEXT.MOOX_STORAGE.strategy_factor_ready_v1
$JS.ACK.MOOX_STORAGE.strategy_factor_ready_v1.>
```

默认 Storage gateway/DataView caller 列表包含独立 app_id/key_id `strategy`，Strategy deployment 挂载独立 HMAC key。

- [ ] **Step 2: 更新部署脚本**

删除 Python/worker 探测；增加 Strategy 到 FactorMgr、Storage Gateway/DataView、JetStream consumer 的配置检查；health 必须看到 ready consumer connected。

- [ ] **Step 3: 运行测试并提交**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/admin
go test ./cmd/cli ./internal/service/sysdeploy -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
bash scripts/test-eventbus-topology.sh
bash scripts/test-strategy-deploy.sh
git add modules/admin examples/setup/default/service-deployments.yaml scripts/test-eventbus-topology.sh scripts/test-strategy-deploy.sh scripts/test-strategy-deploy-e2e.sh
git commit -m "feat(deploy): authorize strategy ready consumer"
```

Expected: PASS 后提交。

### Task 11: 更新管理台为声明式权重策略

**Files:**
- Modify: `web/src/api/strategy-types.ts`
- Modify: `web/src/api/strategy.ts`
- Modify: `web/src/api/strategy.test.ts`
- Modify: `web/src/store/modules/strategy.ts`
- Modify: `web/src/views/strategy/overview/index.vue`
- Modify: `web/src/views/strategy/overview/strategy-create-defaults.test.ts`
- Modify: `web/src/views/strategy/running/index.vue`
- Modify: `web/src/views/strategy/detail/index.vue`
- Modify: `web/src/views/strategy/components/strategy-target-table.vue`
- Modify: `web/src/views/strategy/components/strategy-run-timeline.vue`
- Modify: `web/src/views/strategy/components/strategy-operation-panel.vue`
- Modify: `web/src/views/strategy/components/strategy-operation-panel.test.ts`
- Modify: `web/tests/strategy-console.spec.ts`

- [ ] **Step 1: 写前端 contract 失败测试**

断言类型只有 target_weight；Strategy 有 kind/compiled_json、无 source_code；Runner 有 source_view_id、无 params_json；Result 有 period_time、无 namespace；API 无 runOnce/getEngineStatus。

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm test -- src/api/strategy.test.ts src/views/strategy/overview/strategy-create-defaults.test.ts src/views/strategy/components/strategy-operation-panel.test.ts
```

Expected: FAIL，仍存在 V1 contract。

- [ ] **Step 2: 更新 API、Store 和发布界面**

删除 EngineStatus、runOnce/getEngineStatus 和 engineReady。CreateStrategy 只发 id/name/manifest；CreateRunner 只发 id/strategy/space/account。

移除 Worker tag 与 Python textarea；默认 YAML 使用 `moox.strategy/v2` 示例。定义 drawer 展示 manifest、compiled JSON、source hash 和冻结依赖。

- [ ] **Step 3: 更新 Runner 和结果界面**

Runner 创建时 source view/frequency 从所选 Strategy compiled JSON 只读展示。LogicalAccount 可空并显示观察模式。Targets 表显示目标权重；详情展示 gross/net、pool size、long/short、period、input hash、strict last_error。删除 Python、Run Once、namespace、quantity 文案。

- [ ] **Step 4: 更新 Playwright 并验证**

覆盖发布声明式 Strategy、创建观察/执行 Runner、启停、查看 weight targets、查看 hold/rebalance Result。

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/web
pnpm test -- src/api/strategy.test.ts src/views/strategy
pnpm exec playwright test tests/strategy-console.spec.ts
pnpm build:prod
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add web/src/api web/src/store/modules/strategy.ts web/src/views/strategy web/tests/strategy-console.spec.ts
git commit -m "feat(web): manage declarative weighted strategies"
```

Expected: PASS 后提交。

### Task 12: 完成跨模块 E2E、文档和最终验收

**Files:**
- Modify: `modules/strategy/test/e2e_test.go`
- Modify: `modules/strategy/test/frontend_e2e_test.go`
- Modify: `modules/strategy/test/outbox_jetstream_e2e_test.go`
- Modify: `modules/strategy/test/strategy_trade_external_e2e_test.go`
- Create: `modules/strategy/test/view_factor_ready_e2e_test.go`
- Modify: `modules/strategy/docs/frontend-verification.md`
- Create: `modules/strategy/docs/coin-selection-runtime.md`
- Modify: `docs/选币策略执行框架设计.md`

- [ ] **Step 1: 写 Strategy 完整链路测试**

两个 ready event 乱序到达：第一个依赖不齐只记 inbox，第二个补齐后完成：

```text
ViewFactorPeriodReady
-> inbox/message routing
-> ResolveInstrumentPool
-> LoadCurrentPanel
-> Evaluate
-> Commit StrategyResult/current targets/outbox
```

断言 result、debug、input hash、sequence 和 outbox target_weight。

- [ ] **Step 2: 写 Strategy -> Trade E2E**

使用内嵌 NATS/JetStream、两个 SQLite 和 fake equity/price adapter，验证 outbox publish -> Trade consumer -> resolution ledger -> current quantity target PENDING -> TargetWorker wake。

重复同 target、重启 Trade、再投旧 target，resolution 的 equity/price/quantity 字节必须不变。

- [ ] **Step 3: 写关键故障矩阵**

覆盖 strict 缺行无 Result/sequence/outbox、下一周期恢复、同周期 Factor 重算、相同目标 hold、旧周期晚到不倒灌、outbox 重投、事务回滚、一个 LogicalAccount 不能启用两个 owner Runner。

- [ ] **Step 4: 更新运行文档和设计补充**

`coin-selection-runtime.md` 写发布/启用、ready 链、strict 排障、debug_info、hold/rebalance、Trade resolution、指标和日志。frontend verification 删除 quantity/Python。

设计文档只补充本计划固定的四个行为：hold 同周期重算、多空重叠净额、旧周期忽略、Trade resolution 账本；不扩大一期范围。

- [ ] **Step 5: 运行模块级测试**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test ./... -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/trade
go test ./internal/eventconsumer ./internal/application/equity ./internal/application/target ./internal/infra/store ./test -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector
go test ./internal/sources/binance ./internal/jobs/kline ./internal/planner/storagesource -count=1
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/packages/events
go test ./... -count=1
```

Expected: PASS。

- [ ] **Step 6: 运行外部 E2E**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/strategy
go test -tags=e2e_external ./test -run 'ViewFactorReady|StrategyTrade' -count=1 -v
```

Expected: 环境具备 NATS/Storage/Factor/Trade 时 PASS；环境不具备时记录明确未运行原因，不能用本地单测代替外部验收。

- [ ] **Step 7: 提交 E2E 和文档**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git add modules/strategy/test modules/strategy/docs docs/选币策略执行框架设计.md
git commit -m "test(strategy): verify weighted selection end to end"
```

- [ ] **Step 8: 在干净的实施改动上运行仓库门禁**

先确认本计划产生的文件已全部提交；用户在实施前已有的无关改动不得暂存、回滚或带入本计划 commit。

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git diff --check
make verify-pr
```

Expected: PASS，`make proto` 不产生 generated diff。

- [ ] **Step 9: 检查并推送**

```bash
git status --short
git log --oneline --max-count=12
git push
```

Expected: 各 Task commit 已推送；status 只允许显示实施前已存在且与本计划无关的用户改动。

## 验收矩阵

| 能力 | 必须证明的结果 |
| --- | --- |
| 配置发布 | 未知字段、非法 decimal/operator/frequency、disabled Factor/Binding、多 output Factor、Spot short 均拒绝；compiled JSON/hash 稳定 |
| Runner 生命周期 | 观察 Runner 可无账户；执行 Runner 独占 owner；仅 DISABLED 可换 Strategy/account；Enable 复核冻结依赖 |
| InstrumentPool | metadata/include/exclude/market/quote/venue 生效；历史不足只排除；venue 选择稳定；完整 ID 与 Trade 一致 |
| Panel | 每个 pool instrument 恰好一行；缺行/列/非数值严格跳过整期；不缩小横截面 |
| Evaluator | method=min、先排名后过滤、AND、percentile、count/fraction、post 不回补、稳定 tie break、多空等权和净额确定 |
| Trigger | ready 乱序、重复、未对齐、依赖不齐、同周期重算、重启、旧周期晚到均符合口径 |
| Result/Outbox | same hash 零副作用；hold 写 Result 不发目标；rebalance 原子写 Result/Runner/outbox；空 FULL 清仓 |
| Trade | target_weight 是唯一 wire 字段；首次权益/价格/quantity 永久冻结；重投不重估；Executor 继续负责量化和容量 |
| Live/Paper | 相同 compiled config 和 Panel 生成逐字节相同权重；差异只来自账户和执行环境 |
| 可观测性 | runs、耗时、pool、selected、gross、outbox 指标存在；日志包含 space/runner/period/result/trace |

## 完成标准

- `ViewFactorPeriodReady -> Strategy inbox -> InstrumentPool/Panel -> evaluator -> StrategyResult/outbox -> Trade resolution -> quantity TargetExecutor` 全链路通过。
- Strategy 代码和协议中不再存在 Python entrypoint、RunOnce、EngineStatus、quantity output、namespace、FactorInstance、Snapshot、Slot 或 WeightMerger。
- Factor 协议和 FactorDef/Binding schema 未被 Strategy 改造。
- strict 缺数周期无 Result、无 outbox、无 sequence 变化，且下一周期不受影响。
- 同 message、同 input hash、同 target_id 重投均无重复副作用；同周期更新 latest-wins；相同规范权重只产生 hold。
- Trade resolution 在进程重启和当前 target 更新后仍能复用旧 target_id 的首次换算事实。
- 管理台只展示声明式 manifest、编译依赖、权重目标、Result 和 debug，不再暴露 Python worker 或绝对 quantity。
- 模块测试、Web 测试/构建、外部 E2E（具备环境时）和 `make verify-pr` 达到计划声明结果。
