# Built-in Market Data Collector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建成四个内置 Market Module 的统一契约，在相应 gate 通过的 capability 上激活 `stock_cn`、`crypto_binance` 和 `crypto_okx` 采集闭环，并将 `stock_us` 安全注册为受 US-1 授权门禁约束的 `not_ready` 模块；用户只表达市场与 K 线需求，Collector 在内部完成 Provider 路由、来源写入、质量裁决、统一数据生成、完整性修复和 SCF 有界执行。

**Architecture:** Collector 控制面持有 Market Registry、任务规划、Provider 配额、覆盖率巡检和统一裁决租约；SCF 只执行一个有时间与数据量上限的 JobItem。Provider 返回强类型来源事实，`KlinePipeline` 先写 `provider_data` 来源数据集，再由 `QualityResolver` 选择整根 K 线写入 `unified_data` 统一数据集；K 线和 coverage 状态都落 Storage，Market API 不依赖外部 Provider 或 Collector SQLite 完成读取。

**Tech Stack:** Go 1.24、tRPC-Go、SQLite/GORM、Storage Metadata/Access RPC、CloudNode JobItem、腾讯云 SCF、YAML market manifest、Vue 3/TypeScript、HTTP REST、TDX TCP。

---

## Design Source And Scope

本计划以 [内置市场行情采集架构](../../内置市场行情采集架构.md) 为唯一采集设计基线，并结合当前 `modules/collector`、`modules/storage`、`modules/cloudnode`、`modules/cli` 和 `web` 实现拆解任务。

本计划覆盖：

1. Market Module、Provider、KlinePipeline、Quality Resolver 和 Coverage Reconciler。
2. `provider_data`、`unified_data`、`quality_event` 和 coverage 状态的 Storage 契约。
3. 内置 Market manifest 与 `moox-cli init --markets`。
4. Market-oriented TaskRule、TaskInstance、Attempt、Provider runtime 和裁决租约。
5. `crypto_binance` 现有能力迁移、`stock_cn` 多 Provider、`crypto_okx` 激活，以及 `stock_us` 逻辑注册、资格门禁与具体 addendum。
6. 同一代码包、每个 Space 一个 SCF 函数的部署模式。
7. Market 查询与 refresh API、Collector 管理台和端到端验收。

本计划不覆盖：

- TuShare Pro。
- 实时快照、Tick、盘口和逐笔数据。
- 复权 K 线生成；首版只保存不复权行情。
- 跨 Binance 和 OKX 合并加密货币 K 线。
- [行情数据归档模块](../../行情数据归档模块设计.md) 的实现；归档只消费统一数据写入后由 Storage 发布的事件，Collector 不双写归档文件。

## Locked Implementation Decisions

1. **不兼容现有 `crypto` Space 和 Binance-oriented Task schema。** 项目允许直接替换为四个内置 Space 和 Market-oriented schema。
2. **Provider 不写 Storage。** `ProviderDatasetWriter`、`UnifiedDatasetWriter` 和 `QualityEventWriter` 是唯一事实写入入口。
3. **来源数据先提交。** 来源数据集写入失败时禁止写统一数据；统一数据失败时重试从已持久化来源数据重新裁决。
4. **一根 K 线只选一个 Provider。** 不平均价格，不按字段拼接来源。
5. **价格和数量采用双列契约。** `open/high/low/close/volume/amount` 保持有限 `DOUBLE` 以兼容现有 Factor/Trade/Web；同一行增加 `*_exact` `STRING` 保存 Provider 十进制文本。Collector 只用精确列做质量比较，Writer 从同一个 Decimal 同时生成两种表示并验证一致性。
6. **`data_time` 是 UTC 表示的统一桶起始时间。** `trade_date` 保存 Exchange 本地交易日期，Provider 原始时间另存 `provider_timestamp`。
7. **来源时序行保存该 Provider 对某个桶的最新标准化事实。** 冲突、胜出变化和修订原因写入追加式 `kline_quality_event`。
8. **来源与统一时序行使用 Storage `REPLACE` 写入。** 新增通用 `ROW_WRITE_MODE_REPLACE`，原子替换同一 key 与 `data_time` 的整行，防止切换 Provider 后残留旧的可选列；现有 `MERGE` 保持默认行为。
9. **首版不修改 Storage 为 CAS。** Collector/CloudNode 用确定性的 logical shard 和统一裁决租约保证单写；允许重叠写入前必须另行增加 CAS 或按 Subject 分区的单写者队列。
10. **一个 JobItem 拥有一个 logical shard、一个 Provider 配额租约和一组按 Subject/固定时间片的裁决租约。** Provider 失败后的 fallback、质量抽样和重复验证由后续 JobItem 完成；Quality Resolver 从已落盘来源行读取其他候选。Provider 不进入 TaskInstance 稳定 ID。
11. **SCF 每轮默认领取一个 JobItem。** 目标执行 20 到 30 秒，至少预留 10 秒写 Storage 和上报；超限返回 continuation cursor，不在本地保存续跑状态。
12. **同一 SCF zip 可复用到四个函数。** 每个 runtime-enabled 函数通过 `MOOX_SPACE_ID` 固定 Space；未通过门禁的 Space 不部署函数，运行时也不再从 Binance binding 推断 Space。
13. **Metadata 注册与 Runtime 激活是两个字段。** 四个 manifest 都设置 `register_metadata=true`；只有 readiness gate 通过的 capability 才允许 `runtime_enabled=true`。`stock_us` 未通过 US-1 时保持 `not_ready`，但 `moox-cli init` 仍注册其内置 Space 与逻辑契约。
14. **所有 Record Dataset 使用业务确定性的 `(record_id, version)`。** Instrument/Calendar 的 source 与 unified 记录统一使用控制面分配的 `universe_generation`/`calendar_generation` RFC3339 时间，Provider effective time 只作为列；quality/coverage 使用各自确定性业务版本。重试不得使用新的 `time.Now()`，current-state 读取显式取最新版本。
15. **首版 Collector 控制面单活。** SQLite lease、token bucket、planner 和 outbox 只允许一个 active control-plane 实例；第二实例只能健康检查和待命，不得发 permit 或发布 JobItem。多主部署必须先迁移到共享协调存储。
16. **单位和时间桶属于 capability 契约。** 每个 `(Market, product_type, instrument_type, frequency)` 显式声明 canonical `volume_unit`、`amount_unit`、timezone 和 `bucket_alignment`；Provider 在写来源前完成换算，Quality Resolver 不比较单位未知或桶锚点不同的候选。
17. **配额按 Provider 官方计费模型执行。** Manifest 以 endpoint class 声明 IP/credential/Provider scope、多个滑动窗口、request weight/cost、daily reset timezone 和并发上限；每次实际请求前由单活控制面原子扣减所有适用窗口。
18. **Collector 控制库显式切换到 V2。** 不迁移旧 Binance-oriented 表；新控制面使用 `moox_collector_market_v2.db`，上线前备份旧 DB、停发/清理旧 `crypto` JobItems 并下线旧函数。Storage 中旧 `crypto` 行情不删除，新四个 Space 独立写入，回滚只切换控制面/函数。

## External Validation Gates

| Gate | 必须取得的证据 | 未通过时的行为 |
| --- | --- | --- |
| CN-1 | 对每个拟启用的 `(equity/ETF/index, 日/分钟频率)` 单元验证主/备 K-line Provider，并验证至少一个可维护完整上市标的的 Instrument Provider；记录分页、历史深度、超时、限频、`adjustment=none` 和除权日前后样例 | 只启用证据完整且能证明不复权口径的 capability 单元；无完整 Instrument universe 时 `stock_cn` 不标记 production-ready |
| CN-2 | 目标腾讯云 SCF VPC/公网可以连接候选 TDX `7709` 节点，并在 5 秒内完成握手与一页 K 线 | 本计划将 TDX capability 保持禁用；常驻 Worker 另写设计与执行计划，不在本计划中暗含实现 |
| US-1 | 选定至少一个主 Provider 和一个 fallback，确认 equity/ETF/index、日 K、分钟 K、历史深度、授权和配额 | `stock_us` 只初始化 metadata，不发布采集 JobItem |
| SCF-1 | 所有已启用 Market 函数可使用同一 zip、不同 `MOOX_SPACE_ID` 和不同节点标签完成 Poll/Report | 不扩大到多 Space，只保留本地/测试执行；`stock_us` 未通过 US-1 时不计入已启用函数 |

Gate 输出统一保存到 `modules/collector/config/markets/<market_id>/provider-validation.yaml`。文件必须记录探测时间、`valid_until`、目标环境、Provider/endpoint fingerprint、通过的 capability、批量/点数限制、配额 scope/window/weight/reset timezone、调整口径、证据摘要和 `capability_enabled`；不得保存 token、cookie 或 Secret。

## Delivery Milestones

| Milestone | Tasks | Exit condition |
| --- | --- | --- |
| M0 Contracts | 1-3 | 精确 K 线、Market manifest 和 Provider 强类型契约通过单元测试。 |
| M1 Metadata And Storage | 4-8 | 四个内置 Space 可 create-or-verify，批量标的注册和完整行替换通过测试。 |
| M2 Pipeline | 9-11 | 来源写入、质量裁决、统一写入和 bounded pipeline 形成闭环。 |
| M3 Control Plane | 12-15 | Market-oriented schema、路由、Attempt、租约、cursor 续跑和 Binance vertical slice 完成。 |
| M4 Completeness | 16-19 | `stock_cn` 标的、日历、多源 K 线和补洞通过验收。 |
| M5 Expansion | 20-22 | OKX 激活、US 资格与具体 addendum 完成、Market API 和管理台使用逻辑市场语义。 |
| M6 Production | 23 | 全量测试、按已启用 Space 部署的 SCF、远程数据和缺口修复完成验证。 |

## Target File Map

```text
modules/collector/
  config/markets/
    stock_cn/{market.yaml,metadata.seed.yaml,calendar.yaml,provider-validation.yaml}
    stock_us/{market.yaml,metadata.seed.yaml,provider-validation.yaml}
    crypto_binance/{market.yaml,metadata.seed.yaml,provider-validation.yaml}
    crypto_okx/{market.yaml,metadata.seed.yaml,provider-validation.yaml}
  internal/marketdata/          # 精确 Decimal、Kline、Frequency、Subject 等值对象
  internal/builtin/             # 控制面与 SCF 共享的显式 Market/Provider factory catalog
  internal/readiness/           # manifest/evidence/factory readiness lock 校验
  internal/markets/             # Descriptor、manifest loader、registry、通用策略接口
    stockcn/                    # A 股 universe/calendar/symbol/quality/coverage 策略
    stockus/                    # 美股 descriptor 与授权后的市场策略
    crypto/                     # Binance/OKX 复用的 crypto Market Module
  internal/providers/           # 强类型 Provider 契约、错误分类、contract tests
    binance/
    okx/
    tdx/
    tencent/
    ifeng/
  internal/pipeline/            # Kline/Instrument pipeline、Quality/Universe resolver、budget、cursor
  internal/storageio/           # Metadata/Access client、来源/统一/coverage/事件 writer 与 reader
  internal/routing/             # weighted rendezvous、health、quota、candidate chain
  internal/coverage/            # 预期桶、watermark、近期/历史巡检
  internal/jobs/{instrument,calendar,kline,coverage}/
  internal/domain/              # Market-oriented rule/instance/attempt/lease model
  internal/store/               # SQLite 连接和上述控制面状态的 repository
  proto/collector.proto         # Market/Query/Refresh/Attempt/Continuation RPC
  schema/collector.sql

packages/marketmanifest/        # Collector 与 moox-cli 共享的中立 manifest 契约

modules/cli/cmd/
  init.go                       # moox-cli init --markets
  market.go                     # 逻辑 Market 查询与 refresh 探针
  collector.go                  # per-Market SCF publish

modules/storage/
  proto/{common,access,store,metadata}.proto
  internal/services/access/metadata_builtin.go
  internal/infra/device/pebble/store.go
  internal/infra/metadata/sqlite/crud.go

web/src/views/collector/
  collector-rules/collector-rules.vue
  task-instances/task-instances.vue
  market-status/market-status.vue
```

旧 `modules/collector/internal/sources` 和 `internal/executor` 在 Binance vertical slice 通过后删除；迁移期间不继续向其中增加 Provider。

---

## Phase One: Contracts And Built-in Metadata

### Task 1: Define Exact Market Data Value Contracts

**Files:**
- Create: `modules/collector/internal/marketdata/decimal.go`
- Create: `modules/collector/internal/marketdata/decimal_test.go`
- Create: `modules/collector/internal/marketdata/identifiers.go`
- Create: `modules/collector/internal/marketdata/kline.go`
- Create: `modules/collector/internal/marketdata/kline_test.go`
- Create: `modules/collector/internal/marketdata/result.go`

- [ ] **Step 1: Write exact-decimal contract tests.** Cover valid canonical decimals, invalid exponent notation、NaN、Infinity、leading plus、negative volume、scale limit and zero-value behavior. Verify `0.10` and `0.1` compare equal while serialization returns a stable base-10 string.
- [ ] **Step 2: Run the focused test and verify it fails because `marketdata.Decimal` does not exist.**

```bash
go test -count=1 ./modules/collector/internal/marketdata -run 'TestDecimal' -v
```

- [ ] **Step 3: Implement `Decimal` with `math/big.Rat`, not `float64`.** Expose `ParseDecimal`、`MustDecimal`、`String`、`Cmp`、`IsNegative` and `Validate(maxScale, allowNegative)`; keep the zero value equal to numeric zero.
- [ ] **Step 4: Define stable identifiers and frequencies.** Add typed `MarketID`、`ProviderID`、`ExchangeID`、`ProductType`、`InstrumentType` and `Frequency`; normalize aliases such as `1H -> 1h`, but reject unknown values instead of accepting arbitrary strings.
- [ ] **Step 5: Define the provider-neutral K-line types with the following required shape.**

```go
type ProviderKline struct {
    SubjectID        string
    ProviderID       ProviderID
    ProviderSymbol   string
    Frequency        Frequency
    DataTime         time.Time
    CloseTime        time.Time
    TradeDate        string
    FeedScope        string
    VolumeUnit       string
    AmountUnit       string
    Open              Decimal
    High              Decimal
    Low               Decimal
    Close             Decimal
    Volume            *Decimal
    Amount            *Decimal
    ProviderTimestamp time.Time
    FetchedAt         time.Time
    RequestID         string
    Closed            bool
}

type ResolvedKline struct {
    ProviderKline
    SourceDatasetID string
    QualityStatus  string
    Revision       int64
    ResolvedAt     time.Time
}
```

- [ ] **Step 6: Add validation tests.** Reject missing Subject/Provider/Frequency/unit、non-UTC or misaligned bucket time、invalid `YYYY-MM-DD` trade date、unclosed rows、negative present volume、`high < max(open, close)`、`low > min(open, close)` and `high < low`. Required fields come from the exact capability/Dataset policy: equity/ETF may require volume while index volume can be absent; never convert absence to numeric zero.
- [ ] **Step 7: Run the package tests and verify PASS.**

```bash
go test -count=1 ./modules/collector/internal/marketdata -v
```

- [ ] **Step 8: Commit.**

```bash
git add modules/collector/internal/marketdata
git commit -m "feat(collector): define exact market data contracts"
```

### Task 2: Create The Shared Market Manifest Contract And Registry

**Files:**
- Create: `packages/marketmanifest/go.mod`
- Create: `packages/marketmanifest/go.sum`
- Create: `packages/marketmanifest/manifest.go`
- Create: `packages/marketmanifest/load.go`
- Create: `packages/marketmanifest/validate.go`
- Create: `packages/marketmanifest/manifest_test.go`
- Modify: `go.work`
- Modify: `modules/collector/go.mod`
- Modify: `modules/cli/go.mod`
- Create: `modules/collector/internal/markets/contract.go`
- Create: `modules/collector/internal/markets/registry.go`
- Create: `modules/collector/internal/markets/registry_test.go`
- Create: `modules/collector/config/markets/stock_cn/market.yaml`
- Create: `modules/collector/config/markets/stock_us/market.yaml`
- Create: `modules/collector/config/markets/crypto_binance/market.yaml`
- Create: `modules/collector/config/markets/crypto_okx/market.yaml`
- Create: `modules/collector/config/markets/stock_cn/provider-validation.yaml`
- Create: `modules/collector/config/markets/stock_us/provider-validation.yaml`
- Create: `modules/collector/config/markets/crypto_binance/provider-validation.yaml`
- Create: `modules/collector/config/markets/crypto_okx/provider-validation.yaml`

- [ ] **Step 1: Write strict manifest loader tests.** Unknown YAML fields、directory/Market/Space mismatch、duplicate Provider ID、unknown Dataset binding、negative quota、execution budget over 30 seconds and `timeout_seconds < budget + reserve` must fail before any registry mutation.
- [ ] **Step 2: Define the neutral manifest shape.** It must contain `schema_version`、`market_id`、`space_id`、`register_metadata`、`runtime_enabled`、readiness matrix、asset class、timezone、Exchange/Product/Instrument types、Feed descriptors、per-capability `volume_unit/amount_unit/bucket_alignment`、Provider capabilities and quotas、dataset bindings、routing、quality、coverage and SCF settings. Reject the old ambiguous `enabled` field.
- [ ] **Step 3: Reject embedded secrets.** Values or keys named `token`、`secret`、`password`、`api_key` or `cookie` are invalid. Manifests may declare only environment-variable names such as `credential_env: MOOX_OKX_API_KEY`.
- [ ] **Step 4: Implement `markets.Module` and the registry.**

```go
type Module interface {
    Descriptor() Descriptor
    Universe() UniversePolicy
    Calendar() CalendarPolicy
    Symbols() SymbolPolicy
    Routing() RoutingPolicy
    Quality() QualityPolicy
    Coverage() CoveragePolicy
}
```

Registry lookup is exact by Market ID. No code may infer semantics by splitting `stock_cn` or `crypto_binance`.
- [ ] **Step 5: Add the four production `market.yaml` files in a fail-closed state.** Set `register_metadata=true` and `runtime_enabled=false` for all four initially. Tasks 15、19 and 20 may activate Binance、validated stock_cn capability cells and OKX only after their live gates pass; stock_us stays false until its provider-specific addendum is implemented and US-1 passes. Set SCF timeout to 60 seconds、job budget to 30000 ms、report reserve to 10000 ms and explicit function names from the design.
- [ ] **Step 6: Add and validate four evidence templates.** `provider-validation.yaml` uses a strict schema with `probed_at`、`valid_until`、environment、Provider/endpoint fingerprint、endpoint class、frequencies、batch/point limits、quota scopes/windows/weights/reset timezone、network result、adjustment semantics、evidence summary、gate IDs and `capability_enabled`; unknown fields and secret-like keys fail tests.
- [ ] **Step 7: Verify all manifests load and no TuShare Pro ID appears.**

```bash
(cd packages/marketmanifest && GOWORK=off go mod tidy)
(cd packages/marketmanifest && GOWORK=off go test -count=1 ./...)
go test -count=1 ./modules/collector/internal/markets -v
! rg -n "tushare_pro|api\.tushare\.pro" modules/collector/config/markets packages/marketmanifest modules/collector/internal/markets
```

- [ ] **Step 8: Commit.**

```bash
git add go.work packages/marketmanifest modules/collector/go.mod modules/cli/go.mod modules/collector/internal/markets modules/collector/config/markets/*/market.yaml modules/collector/config/markets/*/provider-validation.yaml
git commit -m "feat(collector): add built-in market manifests"
```

### Task 3: Define Strongly Typed Provider Contracts

**Files:**
- Create: `modules/collector/internal/providers/contract.go`
- Create: `modules/collector/internal/providers/errors.go`
- Create: `modules/collector/internal/providers/request_gate.go`
- Create: `modules/collector/internal/providers/registry.go`
- Create: `modules/collector/internal/providers/registry_test.go`
- Create: `modules/collector/internal/providers/fake/provider.go`
- Create: `modules/collector/internal/providers/contracttest/kline.go`
- Create: `modules/collector/internal/providers/contracttest/kline_test.go`

- [ ] **Step 1: Write registry and capability tests.** Duplicate registration must fail; lookup filters by Instrument Type、Frequency、date range and product type; missing capabilities return a typed unsupported result.
- [ ] **Step 2: Define `KlineProvider`、`InstrumentProvider` and optional `CalendarProvider`.** Every method accepts `context.Context` and a `RequestGate`; every physical HTTP/TCP request must call the gate immediately before network I/O, including failover and Provider-internal retries. The K-line contract is:

```go
type FetchKlinesRequest struct {
    MarketID      marketdata.MarketID
    ExchangeID    marketdata.ExchangeID
    ProductType   marketdata.ProductType
    InstrumentType marketdata.InstrumentType
    Frequency     marketdata.Frequency
    Subjects      []ProviderSubject
    StartTime     time.Time
    EndTime       time.Time
    Limit         int
    Cursor        string
}

type FetchKlinesResult struct {
    Rows           []marketdata.ProviderKline
    SubjectResults []SubjectResult
    NextCursor     string
    Complete       bool
    RequestCount   int
    Latency        time.Duration
}

type KlineProvider interface {
    FetchKlines(context.Context, RequestGate, FetchKlinesRequest) (FetchKlinesResult, error)
}

type InstrumentProvider interface {
    FetchInstruments(context.Context, RequestGate, FetchInstrumentsRequest) (FetchInstrumentsResult, error)
}

type FetchInstrumentsRequest struct {
    MarketID       marketdata.MarketID
    ExchangeID     marketdata.ExchangeID
    InstrumentTypes []marketdata.InstrumentType
    SnapshotAt     time.Time
    Limit          int
    Cursor         string
}

type FetchInstrumentsResult struct {
    Instruments  []ProviderInstrument
    NextCursor   string
    Complete     bool
    RequestCount int
}

type ProviderInstrument struct {
    ProviderID     marketdata.ProviderID
    ProviderSymbol string
    ExchangeID     marketdata.ExchangeID
    ProductType    marketdata.ProductType
    InstrumentType marketdata.InstrumentType
    Name           string
    Currency       string
    ListingDate    string
    DelistingDate  string
    Status         string
    EffectiveAt    time.Time
    FetchedAt      time.Time
    RequestID      string
}

type CalendarProvider interface {
    FetchCalendar(context.Context, RequestGate, FetchCalendarRequest) (FetchCalendarResult, error)
}

type FetchCalendarRequest struct {
    MarketID   marketdata.MarketID
    ExchangeID marketdata.ExchangeID
    StartDate  string
    EndDate    string
    Limit      int
    Cursor     string
}

type FetchCalendarResult struct {
    Sessions     []ProviderSession
    NextCursor   string
    Complete     bool
    RequestCount int
}

type ProviderSession struct {
    TradeDate  string
    OpenTime   time.Time
    CloseTime  time.Time
    Status     string
    EffectiveAt time.Time
}

type RequestGate interface {
    BeforeRequest(context.Context, RequestMeta) (RequestPermit, error)
}

type RequestMeta struct {
    ProviderID    marketdata.ProviderID
    JobItemID     string
    AttemptNo     int
    ExecutionNonce string
    RequestIndex  int
    EndpointClass string
    QuotaScopeKey string
    RequestCost   int64
}

type RequestPermit struct {
    PermitID    string
    LeaseEpoch  int64
    Allowed     bool
    NotBefore   time.Time
    ExpiresAt   time.Time
    DenialReason string
}
```

- [ ] **Step 3: Implement typed errors.** Support `rate_limited`、`unauthorized`、`temporarily_unavailable`、`unsupported` and `parse_failed`, with retryability and optional `RetryAfter`. Pipeline and routing code must use `errors.As`, never substring matching.
- [ ] **Step 4: Add a fake Provider and RequestGate with scripted pages, permits and errors.** They record calls, support a fixed clock, enforce response-size/deadline limits and refuse Storage dependencies. Tests prove `Allowed=false` or a `NotBefore` beyond budget produces zero network calls.
- [ ] **Step 5: Add contract-test helpers that every concrete Provider will reuse.** Verify canonical units、normalized UTC bucket alignment、Exchange-local trade date、declared feed scope、stable request count、cursor monotonicity、no duplicate business key and complete source metadata. Include session-open CN minutes、crypto UTC daily/minute anchors and a reserved US DST case.
- [ ] **Step 6: Run boundary and package tests.**

```bash
go test -count=1 ./modules/collector/internal/providers/... -v
./scripts/check-module-boundaries.sh
```

- [ ] **Step 7: Commit.**

```bash
git add modules/collector/internal/providers
git commit -m "feat(collector): define provider contracts"
```

### Task 4: Harden Metadata Seed Validation And Create-Or-Verify

**Files:**
- Modify: `modules/cli/cmd/metadata.go`
- Modify: `modules/cli/cmd/metadata_test.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Create: `modules/storage/internal/bootstrap/metadata/seed_test.go`

- [ ] **Step 1: Add failing tests for strict YAML parsing and complete contract comparison.** A typo in `attributes`、changed Dataset role、changed DataSource kind、changed Field type、changed required flag or changed DatasetColumn origin must fail instead of returning `unchanged`.
- [ ] **Step 2: Enable YAML known-fields decoding in both metadata seed entry points.** The same seed applied through `moox-cli metadata apply` or Storage `import-seed` must produce the same protobuf attributes.
- [ ] **Step 3: Make `verifyMetadataResource` compare the complete logical contract.** Ignore server-owned timestamps, but compare names、descriptions、status、attributes、data kind、frequencies、origin、value type、required and identity references.
- [ ] **Step 4: Add market-seed validation.** Enforce:
  - K-line `provider_data` and `unified_data` use `time_series`.
  - Instrument `provider_data`/`unified_data`、`quality_event` and `coverage_state` use `record`.
  - Logical `calendar` uses `unified_data + feed=calendar + kind=internal + record`; a physical Calendar Provider, when present, writes a separate `provider_data + feed=calendar + record` Dataset.
  - `provider_data` binds a physical Provider DataSource.
  - `unified_data`、`quality_event` and `coverage_state` bind `kind=internal`.
  - Every Dataset declares both `dataset_role` and `feed` attributes; unsupported role/feed/data-kind combinations fail validation.
  - Dataset IDs are lower snake case and no longer than 20 characters.
  - Market seeds contain no Subject、DatasetSubject、PrimaryStoreNode、Device or PrimaryStoreRoute.
- [ ] **Step 5: Run both CLI and Storage seed tests.**

```bash
go test -count=1 ./modules/cli/cmd -run 'Metadata|Seed' -v
go test -count=1 ./modules/storage/internal/bootstrap/metadata -v
```

- [ ] **Step 6: Commit.**

```bash
git add modules/cli/cmd/metadata.go modules/cli/cmd/metadata_test.go modules/storage/internal/bootstrap/metadata
git commit -m "feat(metadata): verify complete seed contracts"
```

### Task 5: Add Built-in Market Metadata Seeds

**Files:**
- Create: `modules/collector/config/markets/stock_cn/metadata.seed.yaml`
- Create: `modules/collector/config/markets/stock_us/metadata.seed.yaml`
- Create: `modules/collector/config/markets/crypto_binance/metadata.seed.yaml`
- Create: `modules/collector/config/markets/crypto_okx/metadata.seed.yaml`
- Create: `modules/cli/cmd/market_seed_test.go`
- Create: `examples/metadata-market-local-routes.seed.yaml`
- Modify: `scripts/release.sh`

- [ ] **Step 1: Write a test that loads all four real seeds with the strict loader.** Assert all IDs are unique within a Space, every Dataset references a DataSource, every column references a Field and all Dataset IDs fit Storage limits.
- [ ] **Step 2: Define shared field contracts.** Register business OHLC、volume and amount as finite `DOUBLE` and parallel `open_exact/high_exact/low_exact/close_exact/volume_exact/amount_exact` as `STRING`; register `trade_date`、`feed_scope`、`volume_unit` and `amount_unit` as `STRING`; register `revision` as `INT`、`close_time`、`provider_timestamp`、`fetched_at`、`source_fetched_at` and `resolved_at` as `TIME`、`is_closed` as `BOOL` and quality reasons as `JSON`. DatasetColumn `required` is capability-specific, so index volume/amount may be optional while equity OHLCV remains required. Add instrument identity/listing fields and coverage range/status/retry fields.
- [ ] **Step 3: Define the `stock_cn` datasets.** Include Provider-specific equity/ETF/index K-line datasets、Provider-specific instrument Record datasets、unified `equity_kline`、`etf_kline`、`index_kline`、`instruments`、`calendar`、`market_coverage` and `kline_quality_event`. Instrument source datasets use `provider_data`; `instruments` uses `unified_data`; `market_coverage` uses `coverage_state`.
- [ ] **Step 4: Define the crypto datasets with both layers.** For example:

```text
crypto_binance/binance_spot_kline  dataset_role=provider_data
crypto_binance/spot_kline          dataset_role=unified_data
crypto_binance/binance_swap_kline  dataset_role=provider_data
crypto_binance/swap_kline          dataset_role=unified_data
```

Use the corresponding OKX IDs in `crypto_okx`. Do not collapse source and unified data even when only one Provider is enabled.
- [ ] **Step 5: Add instrument and control datasets to both crypto Spaces.** Create `binance_instruments`/`okx_instruments` as Provider records, plus unified `instruments`、`calendar`、`market_coverage` and `kline_quality_event` bound to the internal DataSource.
- [ ] **Step 6: Define `stock_us` logical metadata without claiming a live Provider.** Create its Space、internal DataSource、unified K-line/instrument/calendar datasets、`market_coverage` and quality dataset; Provider datasets are added only for Provider IDs accepted by US-1.
- [ ] **Step 7: Add a deployment-only wildcard route seed.** Keep PrimaryStore topology out of Market metadata manifests; cover all four Spaces through the existing local PrimaryStore node.
- [ ] **Step 8: Add release dry-run checks.**

```bash
go test -count=1 ./modules/cli/cmd -run 'MarketSeed' -v
for seed in modules/collector/config/markets/*/metadata.seed.yaml; do
  go run ./modules/cli/cmd/moox-cli metadata apply --file "$seed" --dry-run >/dev/null
done
```

- [ ] **Step 9: Commit.**

```bash
git add modules/collector/config/markets/*/metadata.seed.yaml modules/cli/cmd/market_seed_test.go examples/metadata-market-local-routes.seed.yaml scripts/release.sh
git commit -m "feat(collector): seed built-in market metadata"
```

### Task 6: Implement `moox-cli init --markets`

**Files:**
- Create: `modules/cli/cmd/init.go`
- Create: `modules/cli/cmd/init_test.go`
- Modify: `modules/cli/cmd/root.go`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/release.sh`

- [ ] **Step 1: Write httptest-backed command tests.** Cover dry-run without writes、first run applied、second run unchanged、one-market selection、global duplicate preflight and contract conflict without mutation.
- [ ] **Step 2: Add the exact command surface.**

```text
moox-cli init \
  --manifest-dir <deploy-root>/collector/config/markets \
  --markets all|stock_cn,stock_us,crypto_binance,crypto_okx \
  --metadata-url http://127.0.0.1:20200 \
  [--dry-run]
```

Resolve manifest dir in this order: flag、`MOOX_MARKET_MANIFEST_DIR`、`./collector/config/markets`.
- [ ] **Step 3: Preflight all selected `register_metadata=true` manifests before the first RPC.** Runtime readiness does not suppress built-in metadata registration. Merge seed calls and apply strictly in Space -> DataSource -> Field -> Dataset -> DatasetColumn order.
- [ ] **Step 4: Emit stable JSON.** Include total and per-market `planned`、`applied`、`unchanged` and `failed`; any failed resource returns a nonzero exit status.
- [ ] **Step 5: Insert init into deployment after Storage Metadata becomes ready and before Collector starts.** Set `MOOX_INIT_MARKETS=all` by default and copy `collector/config/markets` into release/stage directories.
- [ ] **Step 6: Run tests and a real dry run.**

```bash
go test -count=1 ./modules/cli/cmd -run 'Init|MetadataApply' -v
go run ./modules/cli/cmd/moox-cli init --manifest-dir ./modules/collector/config/markets --markets all --dry-run
```

Expected: JSON with `failed: 0`; no Storage service is required in dry-run mode.
- [ ] **Step 7: Commit.**

```bash
git add modules/cli/cmd/init.go modules/cli/cmd/init_test.go modules/cli/cmd/root.go scripts/deploy-moox.sh scripts/release.sh
git commit -m "feat(cli): initialize built-in markets"
```

## Phase Two: Storage Write Semantics And Resolution

### Task 7: Protect Built-in Metadata And Add Batch Subject Registration

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/gen/metadata.pb.go`
- Regenerate: `modules/storage/proto/gen/metadata.trpc.go`
- Modify: `modules/storage/internal/core/metadata/store.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/internal/services/access/metadata_catalog.go`
- Modify: `modules/storage/internal/services/access/metadata_space_view.go`
- Create: `modules/storage/internal/services/access/metadata_builtin.go`
- Create: `modules/storage/internal/services/access/metadata_builtin_test.go`
- Modify: `modules/storage/internal/services/access/metadata_cache_test.go`

- [ ] **Step 1: Add failing built-in protection tests for the whole owned graph.** Once a Space has `scope=builtin, owner_module=collector, managed_by=moox-cli`, ordinary Update/Delete RPCs and cascading deletes must reject identity or contract changes to its Space、DataSource、Field、Dataset and DatasetColumn resources, including rename、binding、data kind、frequency、role and schema changes. Identical create-or-verify operations remain successful.
- [ ] **Step 2: Define batch Subject registration.**

```proto
message DataSubjectRegistration {
  Subject subject = 1;
  repeated SubjectSymbol subject_symbols = 2;
  repeated DatasetSubject dataset_subjects = 3;
}

message RegisterDataSubjectsReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  repeated DataSubjectRegistration registrations = 3;
}
```

Add `RegisterDataSubjects` to Metadata and keep the existing single-item RPC as a compatibility wrapper during the same commit.
- [ ] **Step 3: Implement one SQLite transaction per batch.** Validate all references first; write up to 500 Subjects、all Provider symbols and all Dataset bindings; refresh the metadata snapshot once after commit.
- [ ] **Step 4: Reject symbol stealing.** An active `(space_id, data_source_id, external_symbol)` already bound to another active Subject returns conflict and rolls back the entire batch.
- [ ] **Step 5: Permit dynamic instrument maintenance without weakening built-in contracts.** Subject、SubjectSymbol and DatasetSubject remain upsertable by the Instrument Feed; immutable Space/DataSource/Field/Dataset/Column contracts cannot be renamed, mutated, deleted or removed through a parent cascade.
- [ ] **Step 6: Regenerate proto and run fresh tests.**

```bash
make -C modules/storage proto
go test -count=1 ./modules/storage/internal/infra/metadata/sqlite ./modules/storage/internal/services/access -run 'Builtin|RegisterDataSubjects|SubjectSymbol' -v
```

- [ ] **Step 7: Commit generated and handwritten files together.**

```bash
git add modules/storage/proto modules/storage/internal/core/metadata modules/storage/internal/infra/metadata/sqlite modules/storage/internal/services/access
git commit -m "feat(storage): protect built-in metadata and batch subjects"
```

### Task 8: Add Atomic Full-Row Replacement To Storage

**Files:**
- Modify: `modules/storage/proto/common.proto`
- Modify: `modules/storage/proto/access.proto`
- Modify: `modules/storage/proto/store.proto`
- Regenerate: `modules/storage/proto/gen/*`
- Modify: `modules/storage/internal/core/schema/validator.go`
- Modify: `modules/storage/internal/core/schema/validator_test.go`
- Modify: `modules/storage/internal/services/access/data.go`
- Create: `modules/storage/internal/services/access/write_mode_test.go`
- Modify: `modules/storage/internal/services/primary/client.go`
- Modify: `modules/storage/internal/services/primary/local.go`
- Modify: `modules/storage/internal/services/primary/remote.go`
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/internal/infra/device/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store_test.go`
- Modify: `modules/storage/internal/infra/device/pebble/outbox.go`

- [ ] **Step 1: Add TimeSeries and Record tests that demonstrate the stale-column problem.** For each row kind, MERGE `{open, amount}` followed by REPLACE `{open}` removes `amount`; duplicate TimeSeries keys or duplicate `(record_id, version)` keys in one REPLACE request fail deterministically.
- [ ] **Step 2: Add the generic write mode without changing defaults.**

```proto
enum RowWriteMode {
  ROW_WRITE_MODE_UNSPECIFIED = 0;
  ROW_WRITE_MODE_MERGE = 1;
  ROW_WRITE_MODE_REPLACE = 2;
}
```

Add `write_mode` to Access and PrimaryStore write requests. `UNSPECIFIED` behaves as current MERGE.
- [ ] **Step 3: Validate REPLACE rows.** Reject duplicate column names、duplicate row keys and missing/null active `required=true` columns before routing any write.
- [ ] **Step 4: Implement Pebble replacement atomically for both row families.** REPLACE overwrites the complete TimeSeries or Record row, including attributes; it must not call Delete followed by Write. Keep the row and corresponding outbox event in one Pebble batch.
- [ ] **Step 5: Pass write mode through local and remote PrimaryStore clients.** Add contract tests proving both `WriteTimeSeriesRows` and `WriteRecordRows` preserve mode through Access -> remote PrimaryStore.
- [ ] **Step 6: Prove MERGE behavior is unchanged and REPLACE clears stale Provider/Record fields.** Include Record outbox replay and latest-version reads.

```bash
make -C modules/storage proto
go test -count=1 ./modules/storage/internal/core/schema ./modules/storage/internal/services/access ./modules/storage/internal/services/primary ./modules/storage/internal/infra/device/pebble -run 'WriteMode|Replace|Merge' -v
```

- [ ] **Step 7: Commit.**

```bash
git add modules/storage/proto modules/storage/internal/core/schema modules/storage/internal/services/access modules/storage/internal/services/primary modules/storage/internal/infra/device
git commit -m "feat(storage): support atomic row replacement"
```

### Task 9: Implement Collector StorageIO Adapters

**Files:**
- Create: `modules/collector/internal/storageio/client.go`
- Create: `modules/collector/internal/storageio/binding.go`
- Create: `modules/collector/internal/storageio/metadata.go`
- Create: `modules/collector/internal/storageio/provider_writer.go`
- Create: `modules/collector/internal/storageio/unified_writer.go`
- Create: `modules/collector/internal/storageio/unified_reader.go`
- Create: `modules/collector/internal/storageio/candidate_reader.go`
- Create: `modules/collector/internal/storageio/instrument_writer.go`
- Create: `modules/collector/internal/storageio/universe_reader.go`
- Create: `modules/collector/internal/storageio/coverage.go`
- Create: `modules/collector/internal/storageio/quality_event_writer.go`
- Create: `modules/collector/internal/storageio/storageio_test.go`

- [ ] **Step 1: Write fake-RPC tests for role enforcement.** Provider writers reject non-`provider_data`; unified writers reject non-`unified_data`; coverage/event writers reject non-`coverage_state`/`quality_event`; cache refresh handles metadata schema-version change.
- [ ] **Step 2: Implement immutable Dataset bindings from Market Descriptor and Metadata.** A binding contains Space、Dataset、DataSource、role、Instrument Type、Product Type、column contract and schema version.
- [ ] **Step 3: Implement `ProviderDatasetWriter` using `WriteTimeSeriesRows(REPLACE)`.** Use canonical Subject ID in the key; validate the row's units/alignment/required fields against the Dataset capability, derive finite numeric values and `*_exact` strings from each present `marketdata.Decimal`, then write both representations plus `trade_date`、`close_time`、`feed_scope`、units、`provider_symbol`、`provider_timestamp`、`fetched_at`、`request_id` and `is_closed`. Omit absent optional volume/amount so REPLACE clears stale values; reject unknown units、misaligned buckets and non-finite/out-of-range conversion.
- [ ] **Step 4: Implement exact source and current-unified reads.** For each affected Subject/Frequency/Bucket, `CandidateReader` builds exact keys for all runtime-enabled source datasets and `UnifiedReader` reads the exact current unified key. Never issue a whole-Dataset scan. Test missing unified、existing revision and retry after an already committed unified row.
- [ ] **Step 5: Implement `UnifiedDatasetWriter` using REPLACE.** Copy both numeric and exact OHLCV representations from one winning source row, plus `trade_date`、`close_time`、`feed_scope` and `source_provider`、`source_dataset_id`、`source_fetched_at`、`quality_status`、`revision`、`resolved_at`.
- [ ] **Step 6: Implement deterministic quality-event writes with `WriteRecordRows`.**

```text
record_id = sha256(unified_dataset_id | subject_id | freq | dimensions | data_time | revision | decision_hash)
version   = resolved_at
```

An identical retry must ensure the event exists without creating another logical event.
`decision_hash` includes canonical candidate identities、exact business values、winner and reason codes, but excludes request ID、fetch/retry time and map iteration order.
- [ ] **Step 7: Implement source-first instrument adapters with exact RecordKeys and shared generations.** Planner assigns one RFC3339 `universe_generation` to all Provider snapshots in a logical cycle. Provider source uses `record_id=sha256(provider_id|external_symbol)` and `version=universe_generation`; Provider effective time stays in a column. Fetch writes source only. Resolve acquires a Subject-scoped monotonic lease, verifies its generation is still the Market/Exchange's active generation, then writes unified `record_id=sha256(subject_id), version=universe_generation` and Metadata. Old-generation resolve/omission may leave immutable source evidence but must not update unified current state or non-versioned Metadata. Complete markers authorize bounded omission resolution only while their generation remains active.
- [ ] **Step 8: Implement `CoverageStateStore` on `market_coverage`.** Partition coverage into deterministic non-overlapping windows (for example, one trade day for minute data and one calendar month for daily data). Use `record_id=sha256(unified_dataset_id|subject_id|frequency|coverage_partition_id)` and `version=partition_start` in RFC3339, then REPLACE the record's present/missing/terminal/quality-review subranges、attempt summary、last error、`next_retry_at` and resolution time. Expose latest partition reads for Market API; Collector SQLite is not the source returned to users.
- [ ] **Step 9: Add recovery tests for partial-write boundaries.** Prove source success plus unified failure replays from persisted source rows; prove unified success plus quality-event failure re-ensures the same deterministic event on retry; prove instrument source/unified/metadata replay is idempotent; prove unified K-line values never contain columns from two Provider rows.
- [ ] **Step 10: Run tests.**

```bash
go test -count=1 ./modules/collector/internal/storageio -v
```

- [ ] **Step 11: Commit.**

```bash
git add modules/collector/internal/storageio
git commit -m "feat(collector): add market storage adapters"
```

### Task 10: Implement Deterministic Kline Quality And Universe Resolution

**Files:**
- Create: `modules/collector/internal/pipeline/quality.go`
- Create: `modules/collector/internal/pipeline/quality_test.go`
- Create: `modules/collector/internal/pipeline/quality_event.go`
- Create: `modules/collector/internal/pipeline/universe.go`
- Create: `modules/collector/internal/pipeline/universe_test.go`

- [ ] **Step 1: Write table-driven resolver tests before implementation.** Cover illegal OHLC、negative present volume、optional index volume absence、missing capability-required values、wrong bucket、main Provider win、fallback/non-authoritative single-source provisional、policy-declared authoritative single-source confirmed、two-source agreement confirmation and tolerance breach conflict.
- [ ] **Step 2: Define the decision input and output.** Input contains Market quality policy、all candidate source rows and the existing unified row. Output contains either no-write reason or exactly one complete `ResolvedKline` plus zero or more deterministic quality events.
- [ ] **Step 3: Implement deterministic Provider ordering.** Sort by configured priority, candidate-chain position, `fetched_at` and Provider ID; never depend on Go map order or request completion order.
- [ ] **Step 4: Implement exact-decimal tolerances.** Compare price and volume with `marketdata.Decimal`; do not convert to float. Store the tolerance rule and candidate differences in `reasons_json`.
- [ ] **Step 5: Implement revision and event semantics.** Winner、normalized business values or quality status/reason changes increment revision once; request ID、fetch time and retry time alone do not. An unchanged decision retains `revision` and `resolved_at` but still returns the current deterministic event for ensure-write, so a retry after event-write failure repairs the missing event without creating another revision.
- [ ] **Step 6: Verify atomic-source behavior.** Tests must fail if the resolver combines OHLC from one candidate with amount/volume from another.
- [ ] **Step 7: Write Universe Resolver tests.** Cover canonical identity agreement、Provider symbol disagreement、one-source omission、explicit listing/delisting、conflicting status、delisting grace period and idempotent replay.
- [ ] **Step 8: Implement deterministic universe rules.** Merge membership across enabled Instrument Providers through `UniversePolicy`; retain every Provider symbol as evidence, choose canonical metadata by declared authority, and require an explicit authoritative delisting or configured quorum plus grace period. Never infer delisting or `no_trade` from one empty page.
- [ ] **Step 9: Keep first-stage halt semantics honest.** Persist listing/active/delisted states; only persist suspension when an authoritative Instrument/Calendar feed explicitly supplies a bounded period. Real-time halt/snapshot inference remains outside scope.
- [ ] **Step 10: Run tests.**

```bash
go test -count=1 ./modules/collector/internal/pipeline -run 'Quality|Resolve|Universe' -v
```

- [ ] **Step 11: Commit.**

```bash
git add modules/collector/internal/pipeline/quality.go modules/collector/internal/pipeline/quality_test.go modules/collector/internal/pipeline/quality_event.go modules/collector/internal/pipeline/universe.go modules/collector/internal/pipeline/universe_test.go
git commit -m "feat(collector): resolve market facts"
```

## Phase Three: Generic Pipeline And Control Plane

### Task 11: Implement Bounded Kline And Instrument Pipelines

**Files:**
- Create: `modules/collector/internal/pipeline/kline.go`
- Create: `modules/collector/internal/pipeline/kline_test.go`
- Create: `modules/collector/internal/pipeline/instrument.go`
- Create: `modules/collector/internal/pipeline/instrument_test.go`
- Create: `modules/collector/internal/pipeline/budget.go`
- Create: `modules/collector/internal/pipeline/budget_test.go`
- Create: `modules/collector/internal/pipeline/pagination.go`
- Create: `modules/collector/internal/pipeline/cursor.go`
- Create: `modules/collector/internal/pipeline/cursor_test.go`

- [ ] **Step 1: Write orchestration tests with Fake Provider and StorageIO fakes.** K-line order is fetch -> filter closed -> source REPLACE -> source candidates plus current unified exact read -> resolve -> conditional unified REPLACE -> ensure quality events. Instrument fetch phase is marker in-progress -> page source REPLACE -> durable-key result -> final complete marker, with no unified/metadata writes. Resolve phase owns per-Subject leases and performs same-generation candidate read -> Universe resolve -> unified/metadata writes; omission resolve is a separately bounded phase.
- [ ] **Step 2: Define one opaque cursor envelope for both feeds.** Canonical JSON contains version、`plan_id`、sorted TaskInstance IDs hash、schedule window、feed/phase、Market/Provider/source/unified Dataset IDs、frequency、Subject batch hash、shard ID、original start/end or snapshot generation、pagination direction、Provider/omission cursor、Subject/page offsets、last committed key、cumulative request/page/row counts and manifest schema version. Compute `cursor_scope_hash` from all immutable scope fields and append a payload SHA-256 integrity hash. Reject cross-plan/task/schedule/Provider/Dataset cursors. On an unsupported manifest version, discard the cursor and re-plan from persisted coverage/watermark rather than guessing a conversion.
- [ ] **Step 3: Implement explicit sub-budgets.** Pipeline uses `min(parent deadline, now + execution_budget_ms)` and reserves bounded windows for Provider fetch、source write、candidate read、unified/event write、CollectMgr finalize and CloudNode report. Provider response bytes and every Storage RPC's rows/bytes/deadline come from validated manifest limits; do not start a fetch or write chunk that cannot finish before its reserve boundary.
- [ ] **Step 4: Enforce phase-specific ownership and monotonic generations.** K-line fetch/resolve owns one Provider lease and per-Subject window leases. Instrument or external-Calendar fetch owns a Provider lease and writes source; resolve-only owns Subject/date leases. Built-in CalendarPolicy uses `materialize_policy`, no Provider/source/quota lease, and directly writes logical calendar under per-date resolution leases. Every resolve/materialize lease key excludes generation but carries active generation/fencing epoch; reject older generations before unified or Metadata writes.
- [ ] **Step 5: Stop normally at `max_requests`、`max_pages`、`max_rows` or deadline reserve.** Commit only bounded source/unified chunks that fit the remaining write budget, then return `complete=false` and a cursor whose `last_committed_key` resumes after the durable boundary; do not map planned partial completion to an error.
- [ ] **Step 6: Implement the failure matrix and resume stage.** Source write failure skips resolution; unified/event or unified-instrument/metadata failure returns `resume_stage=resolve_from_source` with durable keys so the next JobItem performs no Provider request. Permanent unsupported is reported per Subject; rate limit includes retry-after; an unclosed newest candle is skipped without failing valid historical rows.
- [ ] **Step 7: Return a complete structured summary.** Include feed/phase、snapshot generation、fetched/source/unified/metadata row counts、request cost/count、quality counts、Subject results、latency、stop reason、cursor and completion flag.
- [ ] **Step 8: Run focused tests, including slow Provider、slow Storage and a paginated full-universe feed under a 30-second budget.**

```bash
go test -count=1 ./modules/collector/internal/pipeline -run 'Kline|Instrument|Universe|Budget|Cursor' -v
```

Expected: each fake workload returns a cursor before budget expiry, preserves the last durable boundary and is not classified as failed.
- [ ] **Step 9: Commit.**

```bash
git add modules/collector/internal/pipeline
git commit -m "feat(collector): add bounded market pipelines"
```

### Task 12: Replace Collector Persistence With Market-Oriented Tasks

**Files:**
- Replace: `modules/collector/schema/collector.sql`
- Create: `modules/collector/schema/schema_test.go`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/collector/cmd/cli/init_schema.go`
- Modify: `modules/collector/cmd/cli/init_schema_test.go`
- Modify: `modules/collector/internal/app/control/config.go`
- Modify: `modules/collector/internal/store/database.go`
- Modify: `modules/collector/internal/app/control/database_test.go`
- Replace: `modules/collector/internal/domain/collect_params.go`
- Replace: `modules/collector/internal/domain/task_rule.go`
- Replace: `modules/collector/internal/domain/task_instance.go`
- Create: `modules/collector/internal/domain/task_binding.go`
- Create: `modules/collector/internal/domain/task_attempt.go`
- Create: `modules/collector/internal/domain/provider_runtime.go`
- Create: `modules/collector/internal/domain/lease.go`
- Create: `modules/collector/internal/domain/generation.go`
- Create: `modules/collector/internal/domain/attempt_outbox.go`
- Replace: `modules/collector/internal/store/task_rule.go`
- Replace: `modules/collector/internal/store/task_instance.go`
- Create: `modules/collector/internal/store/task_binding.go`
- Create: `modules/collector/internal/store/task_attempt.go`
- Create: `modules/collector/internal/store/provider_runtime.go`
- Create: `modules/collector/internal/store/lease.go`
- Create: `modules/collector/internal/store/generation.go`
- Create: `modules/collector/internal/store/attempt_outbox.go`
- Create: `modules/collector/internal/store/repository_test.go`
- Modify: `modules/collector/proto/collector.proto`
- Regenerate: `modules/collector/proto/collectorgen/*`
- Modify: `modules/collector/internal/rpc/convert.go`
- Modify: `modules/collector/internal/jobs/jobdef/definition.go`
- Modify: `modules/collector/internal/jobs/kline/definition.go`
- Modify: `modules/collector/internal/jobs/kline/planner.go`
- Modify: `modules/collector/internal/jobs/symbol/definition.go`
- Modify: `modules/collector/internal/jobs/symbol/planner.go`
- Modify: `modules/collector/internal/jobs/registry.go`
- Modify: `modules/collector/internal/jobs/registry_test.go`
- Modify: `modules/collector/internal/planner/kline.go`
- Modify: `modules/collector/internal/planner/task_builder.go`
- Modify: `modules/collector/internal/rpc/schedule.go`
- Modify: `modules/collector/internal/rpc/schedule_test.go`
- Modify: `modules/collector/internal/rpc/data_type_config_test.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/taskpublisher/client.go`
- Modify: `modules/collector/internal/taskpublisher/client_test.go`
- Modify: `modules/collector/internal/executor/executor.go`
- Modify: `modules/collector/internal/sources/interface.go`
- Modify: `modules/collector/internal/sources/binance/kline.go`
- Modify: `modules/collector/internal/sources/binance/symbol.go`

- [ ] **Step 1: Write schema and module-default V2-path tests.** Cover all new tables/unique indexes and change Collector module defaults to `data/collector/moox_collector_market_v2.db`; Task 23 switches deployment paths during the explicit cutover. Tests initialize a fresh V2 database and reject opening an old-schema DB at the V2 path; no implicit ALTER/guessing migration is allowed.
- [ ] **Step 2: Replace TaskRule fields with logical demand.** Persist Market ID、Feed、Instrument Types、Frequencies、history range、schedule and optional Subject/Exchange filters. Provider choice/policy comes only from the system-owned Market manifest, never from ordinary user input. Remove `c_exchange` as a Provider alias.
- [ ] **Step 3: Replace TaskInstance identity with feed-specific stable keys.** Use:

```text
kline     = sha256(market_id | kline | unified_dataset_id | subject_id | frequency)
instrument= sha256(market_id | instrument | exchange_id | product_type | unified_instruments_dataset_id)
calendar  = sha256(market_id | calendar | exchange_id | unified_calendar_dataset_id)
coverage  = sha256(market_id | coverage | unified_dataset_id | subject_id | frequency | coverage_partition_id)
```

Do not include Rule ID or Provider ID. Include Exchange only where it is part of the logical Instrument/Calendar scope. Add collision/stability tests across all four feed keys.
- [ ] **Step 4: Add `t_collector_rule_task_bindings` and deterministic demand aggregation.** Multiple rules may demand the same logical task; binding rows carry each rule's history window、schedule and enabled state. Effective history is the union/earliest start, schedule triggers are unioned then deduplicated by logical window, and removing/disabling a binding recomputes demand without deleting stored data. Stop a TaskInstance only when enabled binding refcount reaches zero. Test 5y+1y、different cadences、disable/re-enable and last-reference removal.
- [ ] **Step 5: Add `t_collector_task_attempts` and per-Subject outcomes.** Record plan/attempt/job IDs、Provider、Exchange、Product Type、time window、shard、candidate chain index、quota lease、cursor、row/request counts、error classification and timestamps. Child rows link every batched Subject to its own stable TaskInstance ID and track `success`、`empty`、`rate_limited`、`temporary`、`unsupported` or `permanent` plus next candidate index, so fallback/finalization updates each logical task and never restarts blindly at the same Provider.
- [ ] **Step 6: Add runtime/generation/lease/outbox tables.** Create Provider quota/health state、monotonic active Universe/Calendar generations、Provider leases、Subject/date resolution leases、coverage scan cursors and an attempt-finalization outbox. Generation advance and resolve-lease grant are transactional; an older generation cannot regain current status. `FinalizeMarketAttempt` stores outcome/summary、settles old leases、updates tasks and inserts zero to many unique grouped outbox rows. Duplicate finalization returns the stored receipt and inserts nothing new.
- [ ] **Step 7: Update protobuf messages and RPC conversions.** API fields use `market_id`、`exchange_id`、`product_type`、`instrument_type`、`frequency`、`unified_dataset_id`; add `AcquireProviderPermit`、`GetMarketAttemptReceipt` and `FinalizeMarketAttempt`. A finalized receipt contains the persisted terminal summary so a redelivery can skip Pipeline. Remove ambiguous `exchange/market/interval` semantics.
- [ ] **Step 8: Update every compile-time consumer in the same commit.** Job definitions、legacy planner/executor/source adapters、scheduler and publisher must use the new fields immediately; do not leave aliases for removed struct fields. Some behavior is replaced in Tasks 13-15, but every intermediate commit must compile.
- [ ] **Step 9: Regenerate and run tests, including a full Collector compile gate.**

```bash
make -C modules/collector/proto all
go test -count=1 ./modules/collector/schema ./modules/collector/internal/domain ./modules/collector/internal/store ./modules/collector/internal/rpc -v
(cd modules/collector && go test -count=1 ./...)
```

- [ ] **Step 10: Commit.**

```bash
git add modules/collector/schema modules/collector/config/app.yaml modules/collector/cmd/cli modules/collector/internal/app/control modules/collector/internal/domain modules/collector/internal/store modules/collector/proto modules/collector/internal/jobs modules/collector/internal/planner modules/collector/internal/rpc modules/collector/internal/taskpublisher modules/collector/internal/executor modules/collector/internal/sources
git commit -m "feat(collector): adopt logical market tasks"
```

### Task 13: Implement Market Planning, Routing, Quotas And Health

**Files:**
- Create: `modules/collector/internal/planner/market.go`
- Create: `modules/collector/internal/planner/kline_shards.go`
- Create: `modules/collector/internal/planner/kline_shards_test.go`
- Create: `modules/collector/internal/routing/router.go`
- Create: `modules/collector/internal/routing/router_test.go`
- Create: `modules/collector/internal/routing/budget.go`
- Create: `modules/collector/internal/routing/budget_test.go`
- Create: `modules/collector/internal/routing/permit.go`
- Create: `modules/collector/internal/routing/permit_test.go`
- Create: `modules/collector/internal/routing/health.go`
- Create: `modules/collector/internal/rpc/provider_permit.go`
- Create: `modules/collector/internal/rpc/provider_permit_test.go`
- Modify: `modules/collector/internal/planner/kline.go`
- Create: `modules/collector/internal/app/control/leader.go`
- Create: `modules/collector/internal/app/control/leader_test.go`
- Modify: `modules/collector/internal/planner/storagesource/source.go`
- Modify: `modules/collector/internal/planner/task_builder.go`
- Modify: `modules/collector/internal/app/control/bootstrap.go`

- [ ] **Step 1: Enforce one active control plane.** Acquire a DB-backed leadership row plus process lock before starting planner、permit service or outbox publisher; renew with a fencing epoch. A standby reports ready=false for scheduling and all mutation RPCs fail closed until leadership is held.
- [ ] **Step 2: Change planning input from one external symbol to all Provider mappings.** Load `ProviderSymbols map[provider_id]external_symbol` for each Subject from Storage Metadata.
- [ ] **Step 3: Implement weighted rendezvous hashing and capability readiness.** Filter by exact `(feed, instrument_type, frequency, date_range, feed_scope)` capability and health first, then produce a deterministic primary and ordered candidate chain. Readiness is a matrix per capability, not one Market-wide boolean; unsupported ETF/minute/scope cells stay disabled instead of inheriting another cell's healthy status.
- [ ] **Step 4: Split logical work and allocate generations.** Respect Provider symbol/point/date/row/budget limits. K-line shards own one Provider and normalized window. Instrument/Calendar planner transactionally advances one shared Market/Exchange generation for the cycle and gives it to all external source fetches or the Provider-free `materialize_policy` job; older incomplete cycles cannot later mutate current state.
- [ ] **Step 5: Reserve concurrency before publishing, not RPS tokens.** Transactionally enforce Provider max concurrency、SCF max concurrency and pending-job cap. Params carry `quota_lease_id`、lease epoch and maximum physical request count; daily/RPS accounting happens at actual execution time.
- [ ] **Step 6: Implement execution-time request permits.** Every physical HTTP/TCP request calls `AcquireProviderPermit` through Task 3's RequestGate with a unique execution nonce、request index、endpoint class、scope key and cost. The transaction validates control-plane leadership/lease epoch and atomically consumes every applicable IP/credential/Provider sliding window plus the timezone-aware daily counter. It returns permit/`not_before`/denial. A crash after permit may overcount but can never undercount or burst; if `not_before` exceeds the remaining budget, SCF returns a cursor without making the request.
- [ ] **Step 7: Make lease expiry safe without Storage CAS.** Provider/resolution leases outlive SCF hard timeout plus maximum Storage RPC deadline and clock skew. Pipeline checks context and lease epoch before every source/unified write and every request; an expired lease enters a quarantine interval before reassignment. Add fixed-clock tests for queued start delay、late SCF source write、expired epoch、unused reservation、weighted multi-window permits、daily reset timezone and denial without network I/O.
- [ ] **Step 8: Implement circuit state.** Closed -> open after configured consecutive temporary failures; cooldown -> half-open with one probe; success closes. Use manifest policy and a fixed clock.
- [ ] **Step 9: Plan deterministic quality samples.** Select `sample_ratio` Subjects by stable hash per Market/day, enqueue a lower-priority second-Provider JobItem only after the primary attempt finalizes, charge normal Provider permits and reacquire per-Subject resolution leases. Sampling never reduces ordinary coverage below its reserved quota budget.
- [ ] **Step 10: Create deterministic per-Subject resolution keys and JobItem IDs.**

```text
resolution_key = market_id | unified_dataset_id | subject_id | frequency | fixed_window_id
instrument_key = market_id | instruments_dataset_id | subject_id
calendar_key   = market_id | calendar_dataset_id | exchange_id | trade_date
plan_id        = market_id | feed | unified_dataset_id | frequency | schedule_window
job_item_id    = plan_id | sorted_task_ids_hash | provider_id | fixed_window_id | cursor_hash
```

Normalize every request into fixed, non-overlapping windows. Acquire all per-Subject leases for a batch in one transaction before submit; split out busy Subjects instead of publishing partial ownership. Repeated planner runs and differently sized Provider batches must not create overlapping unified writers.
- [ ] **Step 11: Run leadership/routing/planner/permit tests.**

```bash
go test -count=1 ./modules/collector/internal/app/control ./modules/collector/internal/planner ./modules/collector/internal/routing ./modules/collector/internal/rpc -run 'Leader|Plan|Route|Quota|Permit|Circuit|Sample' -v
```

- [ ] **Step 12: Commit.**

```bash
git add modules/collector/internal/planner modules/collector/internal/routing modules/collector/internal/rpc/provider_permit.go modules/collector/internal/rpc/provider_permit_test.go modules/collector/internal/app/control
git commit -m "feat(collector): route market data jobs"
```

### Task 14: Make Market Feed Job Handlers The SCF Execution Boundary

**Files:**
- Create: `modules/collector/internal/jobs/instrument/definition.go`
- Create: `modules/collector/internal/jobs/instrument/params.go`
- Create: `modules/collector/internal/jobs/instrument/result.go`
- Create: `modules/collector/internal/jobs/instrument/planner.go`
- Create: `modules/collector/internal/jobs/instrument/handler.go`
- Create: `modules/collector/internal/jobs/instrument/handler_test.go`
- Create: `modules/collector/internal/jobs/calendar/definition.go`
- Create: `modules/collector/internal/jobs/calendar/params.go`
- Create: `modules/collector/internal/jobs/calendar/result.go`
- Create: `modules/collector/internal/jobs/calendar/planner.go`
- Create: `modules/collector/internal/jobs/calendar/handler.go`
- Create: `modules/collector/internal/jobs/calendar/handler_test.go`
- Replace: `modules/collector/internal/jobs/kline/params.go`
- Replace: `modules/collector/internal/jobs/kline/result.go`
- Replace: `modules/collector/internal/jobs/kline/handler.go`
- Create: `modules/collector/internal/jobs/kline/handler_test.go`
- Modify: `modules/collector/internal/jobs/kline/planner.go`
- Modify: `modules/collector/internal/jobs/kline/definition.go`
- Modify: `modules/collector/internal/jobs/jobdef/definition.go`
- Modify: `modules/collector/internal/jobs/registry.go`
- Modify: `modules/collector/internal/jobs/registry_test.go`
- Modify: `modules/collector/internal/taskrunner/poller.go`
- Modify: `modules/collector/internal/taskrunner/poller_test.go`
- Modify: `modules/collector/internal/reporter/task_status.go`
- Modify: `modules/collector/internal/reporter/task_status_test.go`
- Modify: `modules/collector/internal/taskpublisher/client.go`
- Modify: `modules/collector/internal/taskpublisher/client_test.go`
- Create: `modules/collector/internal/taskpublisher/outbox.go`
- Create: `modules/collector/internal/taskpublisher/outbox_test.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Modify: `modules/collector/internal/rpc/schedule.go`
- Create: `modules/collector/internal/builtin/catalog.go`
- Create: `modules/collector/internal/builtin/catalog_test.go`
- Modify: `modules/collector/internal/app/control/bootstrap.go`
- Modify: `modules/collector/internal/app/runtimeboot/services.go`
- Modify: `packages/cloudruntime/runtime.go`
- Modify: `packages/cloudruntime/runtime_test.go`
- Modify: `modules/collector/internal/serverless/handler_test.go`
- Delete after migration: `modules/collector/internal/jobs/symbol/*`

- [ ] **Step 1: Define strict phase-aware JobItem params.** Params include `phase=fetch|resolve|reconcile_omissions|materialize_policy`、Market/Space/Exchange、stable generation/window、limits、sub-budgets and cursor. Fetch carries Provider/source Dataset/quota lease; resolve carries durable keys/unified Dataset/resolution leases; `materialize_policy` carries CalendarPolicy ID、unified calendar Dataset and per-date leases but no Provider/source/quota lease. K-line additionally carries Product/Instrument/Frequency/Subject/time window.
- [ ] **Step 2: Add finalized-receipt preflight, then wire handlers to pipelines.** Before any Provider/Storage work, call `GetMarketAttemptReceipt(job_item_id, attempt_no)`. If finalized, skip Pipeline and return the persisted summary solely for CloudNode terminal-report replay. Otherwise `jobs/kline.Handler` calls KlinePipeline directly; do not convert through `TaskExecuteEvent` or `executor.ExecuteTaskImmediately`.
- [ ] **Step 3: Replace the symbol job with bounded Instrument and Calendar jobs.** Instrument/external-Calendar fetch writes Provider source with planner generation, then Finalize creates resolve-only groups. Resolve handlers acquire subject/date leases and write same-generation unified records. When no CalendarProvider is configured, `materialize_policy` pages the checked-in or 24x7 CalendarPolicy directly into logical calendar records using `calendar_generation` and date leases. Tests cover Binance/OKX 24x7、stock_cn YAML、optional Provider and out-of-order Provider timestamps.
- [ ] **Step 4: Register feed handlers and one explicit built-in factory catalog.** Registry keys are `collect.instrument`、`collect.calendar` and `collect.kline` with `runtime_class=scf`. `internal/builtin` explicitly imports concrete Market/Provider packages and maps IDs to constructors without `init()` side effects; both control-plane bootstrap and SCF runtimeboot consume this same catalog. Startup validates every runtime-enabled capability has both factories, and tests assert control/SCF factory ID sets are identical.
- [ ] **Step 5: Set Collector polling limit to one.** Delete the Binance Space fallback; missing or unregistered `MOOX_SPACE_ID` fails runtime initialization. Reject a polled item whose Space differs from runtime config.
- [ ] **Step 6: Make CollectMgr the only business-retry authority.** Handler calls `FinalizeMarketAttempt` first. That one transaction stores results, updates every attempt-subject's linked TaskInstance, settles old leases and writes zero to many grouped continuation/fallback/resolve/sample outbox rows; the publisher submits them afterward. Provider/Storage/quality outcomes are then reported as terminal success to CloudNode with the structured summary, so CloudNode never races a business fallback.
- [ ] **Step 7: Reserve CloudNode retry for control-plane delivery failure.** If `FinalizeMarketAttempt` cannot be acknowledged, return retryable so CloudNode redelivers the same JobItem. If CollectMgr finalized but its ACK or the later CloudNode report was lost, receipt preflight skips Pipeline and returns the same summary; no lease validation or external request occurs. Outbox publish and status-report order must tolerate a crash after either side.
- [ ] **Step 8: Implement grouped follow-up publishing and fresh lease acquisition.** Cursor is normal completion for the current JobItem. Group mixed batch outcomes by deterministic continuation、fallback Provider/candidate、resolve-only durable keys and sample scope. Before publishing each outbox row, idempotently acquire fresh leases: continuation/fallback/sample require one Provider concurrency lease plus affected per-Subject resolution leases; resolve-only requires only resolution leases and consumes no Provider concurrency/request permit. Busy groups stay pending with `next_attempt_at`; publish ACK loss is resolved by deterministic JobItem ID lookup, not duplicate submission.
- [ ] **Step 9: Preserve report budget.** Pipeline work ends by 30 seconds; CollectMgr finalize has a total 5-second budget; CloudRuntime still reports/ACKs before the 45-second keepalive window ends. Slow finalize returns a delivery error, not a second business decision.
- [ ] **Step 10: Test feed handlers, cursor, receipt, outbox and lease lifecycle.** Cover paginated instrument registration、calendar RecordKey replay、G2 completing before late G1 resolve/omission、stable continuation ID、mixed batch producing multiple grouped outbox rows、busy follow-up lease、resolve-only without Provider lease、finalized redelivery preflight、duplicate/single-sided reports、crash after finalize、stale attempt、expired/late writer、cross-Space item、cross-plan cursor rejection、per-Subject fallback and cursor finalization. Assert late G1 cannot regress unified rows or Metadata.
- [ ] **Step 11: Run tests.**

```bash
go test -count=1 ./packages/cloudruntime -v
go test -count=1 ./modules/collector/internal/builtin ./modules/collector/internal/app/control ./modules/collector/internal/app/runtimeboot ./modules/collector/internal/jobs ./modules/collector/internal/jobs/instrument ./modules/collector/internal/jobs/calendar ./modules/collector/internal/jobs/kline ./modules/collector/internal/taskrunner ./modules/collector/internal/reporter ./modules/collector/internal/taskpublisher ./modules/collector/internal/rpc ./modules/collector/internal/serverless -v
```

- [ ] **Step 12: Commit.**

```bash
git add packages/cloudruntime modules/collector/internal/jobs modules/collector/internal/taskrunner modules/collector/internal/reporter modules/collector/internal/taskpublisher modules/collector/internal/rpc modules/collector/internal/serverless modules/collector/internal/builtin modules/collector/internal/app/control/bootstrap.go modules/collector/internal/app/runtimeboot/services.go
git commit -m "feat(collector): execute bounded market jobs on SCF"
```

### Task 15: Migrate Binance Into The Generic Pipeline

**Files:**
- Create: `modules/collector/internal/providers/binance/config.go`
- Create: `modules/collector/internal/providers/binance/client/client.go`
- Create: `modules/collector/internal/providers/binance/client/spot.go`
- Create: `modules/collector/internal/providers/binance/client/swap.go`
- Create: `modules/collector/internal/providers/binance/client/symbol.go`
- Create: `modules/collector/internal/providers/binance/client/types.go`
- Create: `modules/collector/internal/providers/binance/kline.go`
- Create: `modules/collector/internal/providers/binance/instrument.go`
- Create: `modules/collector/internal/providers/binance/provider_test.go`
- Create: `modules/collector/internal/providers/binance/live_test.go`
- Create: `modules/collector/internal/pipeline/binance_integration_test.go`
- Create: `modules/collector/internal/markets/crypto/module.go`
- Create: `modules/collector/internal/markets/crypto/module_test.go`
- Modify: `modules/collector/internal/app/runtimeboot/services.go`
- Modify: `modules/collector/internal/builtin/catalog.go`
- Modify: `modules/collector/internal/builtin/catalog_test.go`
- Modify: `modules/collector/internal/app/runtime/local_config.go`
- Modify: `modules/collector/internal/reporter/heartbeat.go`
- Modify: `modules/collector/configs/config.yaml`
- Modify: `modules/collector/config/markets/crypto_binance/market.yaml`
- Modify: `modules/collector/config/markets/crypto_binance/provider-validation.yaml`
- Modify: `modules/factor/internal/storageio/dataframe.go`
- Modify: `modules/factor/internal/storageio/storageio_test.go`
- Delete: `modules/collector/configs/sources/market/binance.yaml`
- Delete: `modules/collector/internal/sources/*`
- Delete: `modules/collector/internal/executor/*`
- Delete: `modules/collector/internal/model/common/*`
- Delete: `modules/collector/internal/model/market/*`

- [ ] **Step 1: Move the existing HTTP clients without behavior changes and preserve their httptest fixtures.** No new package may import Storage protobuf.
- [ ] **Step 2: Implement Binance `KlineProvider`.** Support start/end/limit/cursor、spot/swap URLs、closed-candle metadata and typed error mapping. Preserve exact API text, normalize every enabled product to its manifest unit/alignment and test spot base-asset volume/quote-asset amount separately from each enabled swap contract type; disable a product whose official fields cannot be converted unambiguously.
- [ ] **Step 3: Implement Binance `InstrumentProvider`.** Feed paginated results through InstrumentPipeline; source/unified instrument records and batch SubjectSymbol/DatasetSubject bindings must replay idempotently.
- [ ] **Step 4: Implement and register the reusable crypto Market Module.** Instantiate `crypto_binance` from Descriptor and add its Market/Binance Provider constructors to the shared built-in catalog. `crypto_okx` later reuses the same 24x7 Calendar、symbol、unit/alignment、quality and coverage policies with a different Exchange and Provider. CalendarPipeline materializes deterministic daily session records instead of assuming absence of a calendar Dataset.
- [ ] **Step 5: Remove the entire legacy source/executor path.** Delete old source registry、exchange types、Binance Storage writer、executor and obsolete market/common models. Remove legacy source YAML/runtime config and derive heartbeat capabilities from the Market/Job registry. `rg` must find no import of `internal/sources` or `internal/executor`.
- [ ] **Step 6: Add the vertical-slice integration test.**

```text
Binance fixture
-> binance_spot_kline (provider_data, REPLACE)
-> QualityResolver
-> spot_kline (unified_data, REPLACE)
-> exact Storage read
```

Assert values match the existing collector output and source metadata/revision are present.
- [ ] **Step 7: Migrate Factor's canonical input contract.** Keep numeric `open/high/low/close/volume/amount` columns for calculation, ignore parallel `*_exact` and provenance columns unless requested, and replace Binance-specific `quote_volume/trade_num`. Test that a unified market row becomes the expected numeric DataFrame without nil/string regressions.
- [ ] **Step 8: Run offline, full-Collector and consumer tests.**

```bash
go test -count=1 ./modules/collector/internal/providers/binance ./modules/collector/internal/markets/crypto ./modules/collector/internal/pipeline -v
! rg -n "WriteTimeSeriesRows|WriteRecordRows" modules/collector/internal/providers/binance
(cd modules/collector && go test -count=1 ./...)
go test -count=1 ./modules/factor/internal/storageio -v
! rg -n 'internal/(sources|executor)' modules/collector --glob '*.go'
```

- [ ] **Step 9: Run the opt-in Binance live probe and record evidence.** Verify instrument pagination、spot/swap daily and minute candles、closed-candle handling、units/alignment、response limits and quota weights/windows; update `provider-validation.yaml` without secrets. Set only passing capability cells and then `runtime_enabled=true`; otherwise keep the module fail-closed.

```bash
MOOX_LIVE_PROVIDER_TEST=1 go test -count=1 ./modules/collector/internal/providers/binance -run TestLive -v
```

- [ ] **Step 10: Commit.**

```bash
git add modules/collector/internal/providers/binance modules/collector/internal/markets/crypto modules/collector/internal/pipeline/binance_integration_test.go modules/collector/internal/builtin modules/collector/internal/app/runtimeboot modules/collector/internal/app/runtime modules/collector/internal/reporter modules/collector/configs modules/collector/config/markets/crypto_binance modules/collector/internal/sources modules/collector/internal/executor modules/collector/internal/model/common modules/collector/internal/model/market modules/factor/internal/storageio
git commit -m "feat(collector): migrate binance to market pipeline"
```

## Phase Four: Coverage And Additional Markets

### Task 16: Implement Coverage Reconciliation And Repair Planning

**Files:**
- Create: `modules/collector/internal/coverage/expected.go`
- Create: `modules/collector/internal/coverage/expected_test.go`
- Create: `modules/collector/internal/coverage/reconciler.go`
- Create: `modules/collector/internal/coverage/reconciler_test.go`
- Create: `modules/collector/internal/domain/coverage.go`
- Create: `modules/collector/internal/store/coverage_cursor.go`
- Create: `modules/collector/internal/store/coverage_cursor_test.go`
- Create: `modules/collector/internal/jobs/coverage/definition.go`
- Create: `modules/collector/internal/jobs/coverage/handler.go`
- Create: `modules/collector/internal/jobs/coverage/handler_test.go`
- Modify: `modules/collector/internal/jobs/registry.go`
- Modify: `modules/collector/internal/jobs/registry_test.go`
- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/collector/internal/rpc/schedule.go`

- [ ] **Step 1: Write expected-bucket tests against fake calendars.** Cover weekends、holidays、midday breaks、listing/delisting boundaries、24x7 crypto and daily close semantics.
- [ ] **Step 2: Make Storage the coverage source of truth.** Persist normalized range state through Task 9's `CoverageStateStore` in each Space's `market_coverage` Record Dataset. Collector SQLite stores only scan cursors、leases and scheduler checkpoints, never the `coverage_status` returned by Market API.
- [ ] **Step 3: Implement watermarks from unified Storage rows.** Read latest unified bucket per Subject/Frequency, then apply a configurable overlap window. Normalize overlap into Task 9's coverage partitions and Task 13's fixed lease windows so correction scans cannot create overlapping state or unified writers.
- [ ] **Step 4: Implement three planning modes.** Incremental collection、recent internal-gap scan and low-frequency historical scan. All generate normalized missing ranges, then reuse routing/quota logic from Task 13.
- [ ] **Step 5: Record terminal absence only from authoritative evidence.** `not_listed`/`delisted` come from resolved Universe state; `no_trade` comes from Calendar or an explicit authoritative session status. Provider empty/unsupported responses stay `unavailable` and advance the candidate chain; never synthesize zero-volume candles.
- [ ] **Step 6: Close the quality loop as well as the gap loop.** Coverage records distinguish presence from `provisional/confirmed/conflict`. Schedule Task 13's deterministic second-Provider samples, retry unresolved provisional/conflict ranges within a separate quality budget and never report full quality when only a row's existence is known.
- [ ] **Step 7: Add an independent Collector timer.** Register `coverage.reconcile` with `runtime_class=control`; it executes only under the active Collector leader. SCF receives only bounded `collect.kline` repair/sample shards and must not advertise or poll the coverage job type.
- [ ] **Step 8: Run tests.**

```bash
go test -count=1 ./modules/collector/internal/coverage ./modules/collector/internal/store ./modules/collector/internal/jobs ./modules/collector/internal/jobs/coverage ./modules/collector/internal/rpc -run 'Coverage|ExpectedBucket|Repair|Registry' -v
```

- [ ] **Step 9: Commit.**

```bash
git add modules/collector/internal/coverage modules/collector/internal/domain/coverage.go modules/collector/internal/store/coverage_cursor* modules/collector/internal/jobs/coverage modules/collector/internal/jobs/registry.go modules/collector/internal/jobs/registry_test.go modules/collector/config/trpc_go.yaml modules/collector/internal/rpc/schedule.go
git commit -m "feat(collector): reconcile market coverage"
```

### Task 17: Implement The `stock_cn` Market Module And Calendar

**Files:**
- Create: `modules/collector/internal/markets/stockcn/module.go`
- Create: `modules/collector/internal/markets/stockcn/module_test.go`
- Create: `modules/collector/internal/markets/stockcn/universe.go`
- Create: `modules/collector/internal/markets/stockcn/universe_test.go`
- Create: `modules/collector/internal/markets/stockcn/calendar.go`
- Create: `modules/collector/internal/markets/stockcn/calendar_test.go`
- Create: `modules/collector/internal/markets/stockcn/symbols.go`
- Create: `modules/collector/internal/markets/stockcn/symbols_test.go`
- Create: `modules/collector/internal/markets/stockcn/quality.go`
- Create: `modules/collector/internal/markets/stockcn/coverage.go`
- Modify: `modules/collector/internal/builtin/catalog.go`
- Modify: `modules/collector/internal/builtin/catalog_test.go`
- Create: `modules/collector/config/markets/stock_cn/calendar.yaml`
- Create: `docs/collector/calendar-maintenance.md`

- [ ] **Step 1: Define the calendar file contract.** Include source/provenance、license note、retrieved/version time、coverage start/end、timezone、regular sessions、closed dates and exceptional open dates. Fail closed outside the declared range so missing holiday data is never treated as a market gap.
- [ ] **Step 2: Implement SSE/SZSE/BSE sessions.** Tests must cover `09:30-11:30`、`13:00-15:00`, the midday break, a known holiday and an exceptional open day from the checked-in fixture.
- [ ] **Step 3: Implement stable Subject IDs and Provider symbol mappings.** Use `XSHG`、`XSHE` and `XBSE` suffixes; keep each Provider's external symbol in SubjectSymbol, not in Subject ID. Tests derive TDX、Tencent and iFeng mappings from `600000.XSHG`, persist their mapping source and prove the planner can route a TDX-discovered Subject to Tencent/iFeng.
- [ ] **Step 4: Implement `UniversePolicy`.** Merge instrument candidates by canonical Exchange/security code, preserve all Provider symbols, declare authority order and require explicit delisting or quorum plus grace. One Provider's missing page cannot remove a Subject.
- [ ] **Step 5: Keep equity、ETF and index policies separate inside the same module.** Resolve distinct unified/source datasets、canonical share/index volume units、CNY amount semantics、session/daily bucket anchors、quality tolerances、capability-readiness cells and routing candidates.
- [ ] **Step 6: Implement listing-state rules.** Instrument metadata carries listing date、delisting date、status、currency、Exchange and Instrument Type; Coverage excludes out-of-life buckets. Provider empty results never imply halt or `no_trade`.
- [ ] **Step 7: Add horizon checks and maintenance workflow.** CI requires at least 180 future days; under 90 days lowers Calendar readiness and emits an operator alert. Document source refresh、diff review、fixture tests、rollback and the rule that an expired calendar stops new repair planning rather than inventing weekdays.
- [ ] **Step 8: Run tests and register the module from manifest.**

```bash
go test -count=1 ./modules/collector/internal/markets/stockcn ./modules/collector/internal/builtin -v
```

- [ ] **Step 9: Commit.**

```bash
git add modules/collector/internal/markets/stockcn modules/collector/internal/builtin modules/collector/config/markets/stock_cn/calendar.yaml docs/collector/calendar-maintenance.md
git commit -m "feat(collector): add China stock market policies"
```

### Task 18: Implement The TDX Provider

**Files:**
- Create: `modules/collector/internal/providers/tdx/config.go`
- Create: `modules/collector/internal/providers/tdx/protocol.go`
- Create: `modules/collector/internal/providers/tdx/client.go`
- Create: `modules/collector/internal/providers/tdx/servers.go`
- Create: `modules/collector/internal/providers/tdx/parser.go`
- Create: `modules/collector/internal/providers/tdx/kline.go`
- Create: `modules/collector/internal/providers/tdx/instrument.go`
- Create: `modules/collector/internal/providers/tdx/provider_test.go`
- Create: `modules/collector/internal/providers/tdx/live_test.go`
- Modify: `modules/collector/internal/builtin/catalog.go`
- Modify: `modules/collector/internal/builtin/catalog_test.go`
- Modify: `modules/collector/go.mod`
- Modify: `modules/collector/go.sum`
- Modify: `modules/collector/config/markets/stock_cn/market.yaml`
- Modify: `modules/collector/config/markets/stock_cn/provider-validation.yaml`

- [ ] **Step 1: Add captured packet/response fixtures and parser tests first.** Cover server handshake、security-list page、bar page、market code、category mapping、GBK names、empty page and malformed packet.
- [ ] **Step 2: Implement only the required TDX subset.** Support security list and K-line bars; do not implement snapshot、Tick、quotes or extended instruments.
- [ ] **Step 3: Add server selection with bounded connect/read deadlines.** Keep a manifest-provided server list, record failures and choose the next healthy server. One JobItem connects and closes; no heartbeat or cross-invocation connection reuse.
- [ ] **Step 4: Map TDX pages into exact ProviderKline rows.** Respect its page maximum, oldest/newest direction and inclusive boundaries; cursor must prevent duplicates across pages.
- [ ] **Step 5: Implement Instrument Feed pages without Storage imports.** Classify equity/ETF/index and return normalized `ProviderInstrument` values. InstrumentPipeline applies Universe/Symbol policies and calls `RegisterDataSubjects` in batches no larger than 500; TDX Provider never invokes Metadata or Storage RPC itself.
- [ ] **Step 6: Add an opt-in live test and explicit runtime gate.**

```bash
MOOX_LIVE_PROVIDER_TEST=1 go test -count=1 ./modules/collector/internal/providers/tdx -run TestLive -v
```

Record CN-1/CN-2 evidence in `provider-validation.yaml`; never commit endpoint credentials. Enable TDX only when CN-2 passes. If it fails, commit the tested Provider as disabled and keep `stock_cn` not ready unless another qualified Instrument Provider satisfies the capability matrix; the Worker alternative requires a separate approved plan.
- [ ] **Step 7: Register the TDX factory, tidy dependencies and run offline tests/contract suite.** The shared catalog must resolve it even when its capability remains disabled by CN-2.

```bash
(cd modules/collector && go mod tidy)
go test -count=1 ./modules/collector/internal/providers/tdx ./modules/collector/internal/builtin -v
```

- [ ] **Step 8: Commit with the validated enabled/disabled state.**

```bash
git add modules/collector/internal/providers/tdx modules/collector/internal/builtin modules/collector/config/markets/stock_cn modules/collector/go.mod modules/collector/go.sum
git commit -m "feat(collector): add TDX market provider"
```

### Task 19: Add Tencent And iFeng Providers And Prove Multi-Provider Resolution

**Files:**
- Create: `modules/collector/internal/providers/tencent/config.go`
- Create: `modules/collector/internal/providers/tencent/kline.go`
- Create: `modules/collector/internal/providers/tencent/parser.go`
- Create: `modules/collector/internal/providers/tencent/provider_test.go`
- Create: `modules/collector/internal/providers/tencent/live_test.go`
- Create: `modules/collector/internal/providers/ifeng/config.go`
- Create: `modules/collector/internal/providers/ifeng/kline.go`
- Create: `modules/collector/internal/providers/ifeng/parser.go`
- Create: `modules/collector/internal/providers/ifeng/provider_test.go`
- Create: `modules/collector/internal/providers/ifeng/live_test.go`
- Create: `modules/collector/internal/pipeline/stockcn_integration_test.go`
- Modify: `modules/collector/internal/builtin/catalog.go`
- Modify: `modules/collector/internal/builtin/catalog_test.go`
- Modify: `modules/collector/config/markets/stock_cn/market.yaml`
- Modify: `modules/collector/config/markets/stock_cn/provider-validation.yaml`

- [ ] **Step 1: Capture response fixtures from the endpoints analyzed in the legacy TuShare repository.** Record URL form、encoding、frequency、pagination、field order、empty/error response、adjustment parameter/default and observed limits. Include a split/dividend boundary sample proving `adjustment=none`; tests use fixtures and never require the internet.
- [ ] **Step 2: Implement Tencent daily/weekly/monthly and supported minute K-line parsing.** Convert symbols through stock_cn SymbolPolicy and classify HTTP/parse/rate errors.
- [ ] **Step 3: Implement iFeng daily and supported minute K-line parsing.** Do not claim a frequency until its live test and fixture both pass.
- [ ] **Step 4: Update the readiness matrix from evidence, not old documentation alone.** Each `(instrument_type, frequency)` cell records its usable primary/fallback and Instrument-universe source; cells without validated coverage remain disabled. Set `stock_cn.runtime_enabled=true` only when CN-1 is green for at least one cell and an enabled complete Universe source exists; readiness remains per cell, never because two Providers passed an unrelated daily-equity probe.
- [ ] **Step 5: Add multi-Provider integration tests for production planners.** Split a Subject set across Providers, fill a primary gap from the persisted next candidate, verify Task 13 creates and later runs a sample JobItem, produce `confirmed` agreement and one deterministic conflict event when tolerance is exceeded.
- [ ] **Step 6: Prove all rows still follow source-first ordering and whole-row selection.** Simulate source write failure、unified failure and retry after a fallback result. An empty Provider response stays `unavailable` until Calendar/Universe evidence proves `no_trade`.
- [ ] **Step 7: Register Tencent/iFeng factories and run offline/optional live tests.** The shared catalog test must construct each enabled candidate from the stock_cn manifest.

```bash
go test -count=1 ./modules/collector/internal/providers/tencent ./modules/collector/internal/providers/ifeng ./modules/collector/internal/pipeline ./modules/collector/internal/builtin -run 'Tencent|IFeng|StockCN|MultiProvider|Catalog' -v
MOOX_LIVE_PROVIDER_TEST=1 go test -count=1 ./modules/collector/internal/providers/tencent ./modules/collector/internal/providers/ifeng -run TestLive -v
```

- [ ] **Step 8: Commit.**

```bash
git add modules/collector/internal/providers/tencent modules/collector/internal/providers/ifeng modules/collector/internal/pipeline/stockcn_integration_test.go modules/collector/internal/builtin modules/collector/config/markets/stock_cn
git commit -m "feat(collector): aggregate China stock providers"
```

### Task 20: Implement OKX And Activate `crypto_okx`

**Files:**
- Create: `modules/collector/internal/providers/okx/config.go`
- Create: `modules/collector/internal/providers/okx/client.go`
- Create: `modules/collector/internal/providers/okx/kline.go`
- Create: `modules/collector/internal/providers/okx/instrument.go`
- Create: `modules/collector/internal/providers/okx/parser.go`
- Create: `modules/collector/internal/providers/okx/provider_test.go`
- Create: `modules/collector/internal/providers/okx/live_test.go`
- Modify: `modules/collector/internal/builtin/catalog.go`
- Modify: `modules/collector/internal/builtin/catalog_test.go`
- Modify: `modules/collector/config/markets/crypto_okx/market.yaml`
- Modify: `modules/collector/config/markets/crypto_okx/provider-validation.yaml`
- Create: `modules/collector/internal/markets/crypto/okx_integration_test.go`

- [ ] **Step 1: Add fixture tests for official V5 public instruments and history-candles responses.** Cover spot、swap、pagination cursor、confirmed candle flag、rate limit and API error codes.
- [ ] **Step 2: Implement and register OKX.** Preserve OKX instrument IDs as Provider symbols; map canonical Subjects through crypto SymbolPolicy. Convert `vol/volCcy/volCcyQuote` according to contract metadata into declared base/quote units and prove its UTC bucket anchor. Add both the `crypto_okx` Market constructor and OKX Provider constructor to the shared built-in catalog before enabling any capability.
- [ ] **Step 3: Keep Exchange boundaries strict.** `crypto_okx` source/unified datasets never read or fill from `crypto_binance`, even for the same BTC-USDT name.
- [ ] **Step 4: Run source-first vertical-slice tests for spot and swap.** Verify `okx_spot_kline -> spot_kline` and `okx_swap_kline -> swap_kline` in the `crypto_okx` Space.
- [ ] **Step 5: Run tests and update validation evidence.** Record OKX quota windows/weights、unit/alignment and passing capability cells. Set `crypto_okx.runtime_enabled=true` only after its Instrument plus at least one K-line cell pass; otherwise leave it false.

```bash
go test -count=1 ./modules/collector/internal/providers/okx ./modules/collector/internal/markets/crypto ./modules/collector/internal/builtin -run 'OKX|Catalog' -v
MOOX_LIVE_PROVIDER_TEST=1 go test -count=1 ./modules/collector/internal/providers/okx -run TestLive -v
```

- [ ] **Step 6: Commit.**

```bash
git add modules/collector/internal/providers/okx modules/collector/internal/markets/crypto/okx_integration_test.go modules/collector/internal/builtin modules/collector/config/markets/crypto_okx
git commit -m "feat(collector): add OKX market module"
```

### Task 21: Complete US-1 Before Activating `stock_us`

**Files:**
- Create: `docs/collector/stock-us-provider-qualification.md`
- Modify: `modules/collector/config/markets/stock_us/provider-validation.yaml`
- Create: `docs/superpowers/plans/2026-07-11-stock-us-provider-addendum.md`

- [ ] **Step 1: Run a written Provider qualification instead of choosing an unofficial API silently.** Evaluate at least two candidates for equity/ETF/index、daily/minute frequencies、historical depth、adjustment semantics、consolidated vs single-venue scope、license、credential storage、RPS/daily quota and SCF network access.
- [ ] **Step 2: Record exact sample evidence and a go/no-go result.** The decision document must name the primary and fallback Provider IDs, official documentation, required credentials, data scope and unsupported cases. Tokens and response bodies containing account data stay out of Git.
- [ ] **Step 3: Update validation evidence after a Provider passes US-1.** Record the chosen Provider capabilities、quotas、`feed_scope` and gate status in `provider-validation.yaml`; do not enable the runtime manifest in this task.
- [ ] **Step 4: Write the provider-specific addendum at the exact path above.** The addendum must name every `market.yaml`、metadata seed、`internal/markets/stockus` and `internal/providers/<provider>` file to create or modify, plus exact API endpoints、fixtures、credential names、feed scope、volume/amount units、US/Eastern daily/minute anchors、DST cases、live probes、tests and activation commit. No `stock_us` Provider code may be written before that addendum is reviewed.
- [ ] **Step 5: Keep the module disabled.** Until the approved addendum has been executed, registry status is `not_ready`, Task planner publishes no US JobItems and `moox-cli init` remains safe to run.
- [ ] **Step 6: Commit the qualification separately.**

```bash
git add docs/collector/stock-us-provider-qualification.md docs/superpowers/plans/2026-07-11-stock-us-provider-addendum.md modules/collector/config/markets/stock_us/provider-validation.yaml
git commit -m "docs(collector): qualify US market providers"
```

This is an intentional hard gate, not an optional follow-up. The overall feature is not production-complete while `stock_us` remains `not_ready`.

## Phase Five: Product API, UI And Production Rollout

### Task 22: Expose Market Query/Refresh APIs And Update Collector UI

**Files:**
- Modify: `modules/collector/proto/collector.proto`
- Regenerate: `modules/collector/proto/collectorgen/*`
- Create: `modules/collector/internal/rpc/market.go`
- Create: `modules/collector/internal/rpc/market_test.go`
- Create: `modules/collector/internal/rpc/market_cursor.go`
- Create: `modules/collector/internal/rpc/market_cursor_test.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Create: `modules/cli/cmd/market.go`
- Create: `modules/cli/cmd/market_test.go`
- Create: `web/src/api/collector-market.ts`
- Modify: `web/src/views/collector/collector-rules/collector-rules.vue`
- Modify: `web/src/views/collector/task-instances/task-instances.vue`
- Create: `web/src/views/collector/market-status/market-status.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/api/modules/system/static-menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: Add API contract tests first.** Query resolves logical Market/Instrument/Feed to unified Dataset IDs, reads K lines plus `market_coverage` only through Storage Access, and never calls a Provider or Collector SQLite. Merge and dedupe on the complete stable tuple `(data_time, instrument_type, subject_id, frequency, canonical_dimensions)` with requested asc/desc order, not `data_time` alone.
- [ ] **Step 2: Add explicit RPCs.**

```text
ListMarketModules
GetMarketStatus
QueryMarketKlines
RefreshMarketKlines
ListTaskAttempts
```

`QueryMarketKlines` accepts Market ID、Instrument Types、Subjects、Frequency、time range、order、page size and an opaque composite cursor. The cursor stores each underlying Dataset's Storage cursor plus the last emitted full sort tuple, so k-way merge resumes without loss or duplication. It does not accept Provider/source Dataset/unified Dataset IDs.
- [ ] **Step 3: Add explicit mutable-data detection.** First page fixes `query_as_of` and records the last emitted row's revision/provenance hash. Every next page exact-reads that boundary before continuing; a changed boundary or any candidate with `resolved_at > query_as_of` returns `data_changed_restart_query` rather than silently drifting. Storage remains current-state; this API does not pretend it has an unavailable historical snapshot.
- [ ] **Step 4: Return stable query metadata and values.** Include `freshness`、`coverage_status`、`quality_status`、`missing_ranges` and rows. RPC OHLCV fields use the `*_exact` decimal strings as canonical values while numeric Storage columns remain available to Factor/Trade/Web adapters. If data is stale, incomplete or provisional, return existing Storage rows plus state; never fetch in the read request.
- [ ] **Step 5: Implement async refresh.** `RefreshMarketKlines` creates or wakes logical tasks and returns task IDs. Completion requires a new query; Provider responses are never returned as query results.
- [ ] **Step 6: Add scriptable CLI probes.** Implement `moox-cli market status`、`market kline query` and `market kline refresh` with `--control-url` and JSON stdout. Query exposes logical Market/Subject/Frequency/time/cursor flags only and is used by Task 23 remote verification.
- [ ] **Step 7: Replace Provider-oriented rule form fields.** The UI asks for Market、Feed、Instrument Types、Frequency、history and optional Exchange/Subject filters. Remove inferred `${exchange}_${market}_${data_type}` Dataset IDs.
- [ ] **Step 8: Update task views.** TaskInstance displays Market/Instrument/unified Dataset/Frequency; Provider/attempt/candidate/error appear only in attempt details.
- [ ] **Step 9: Add a restrained Market status view.** Show readiness per `(feed, instrument_type, frequency)` cell、runtime-enabled Provider count、watermark、coverage/quality、open circuit and last repair. Do not expose raw source datasets as normal query targets.
- [ ] **Step 10: Test pagination boundaries.** Cover multiple Subjects at the same timestamp、mixed Instrument Types、asc/desc、page size one、empty Dataset、uneven exhaustion and a row inserted/revised after page one; cursor must fail with `data_changed_restart_query`, never drift silently.
- [ ] **Step 11: Run protocol, RPC, CLI and frontend checks.**

```bash
make -C modules/collector/proto all
go test -count=1 ./modules/collector/internal/rpc -run 'Market|Query|Refresh|Attempt' -v
go test -count=1 ./modules/cli/cmd -run 'Market' -v
pnpm --dir web build:prod
```

- [ ] **Step 12: Commit.**

```bash
git add modules/collector/proto modules/collector/internal/rpc modules/cli/cmd/market.go modules/cli/cmd/market_test.go web/src/api/collector-market.ts web/src/views/collector web/src/router/route.ts web/src/api/modules/system/static-menu.ts web/src/lang/modules
git commit -m "feat(collector): expose logical market workflows"
```

### Task 23: Package, Publish And Verify Per-Space SCF Functions

**Files:**
- Modify: `modules/cli/cmd/collector.go`
- Modify: `modules/cli/cmd/collector_test.go`
- Modify: `modules/cli/internal/adminclient/cloudnode.go`
- Modify: `modules/cli/internal/adminclient/client_test.go`
- Modify: `modules/cloudnode/internal/providers/tencent-scf/client.go`
- Modify: `modules/cloudnode/internal/providers/tencent-scf/client_test.go`
- Modify: `modules/cloudnode/internal/rpc/node.go`
- Modify: `modules/cloudnode/internal/rpc/node_scf_test.go`
- Modify: `modules/cloudnode/internal/rpc/job_item_test.go`
- Create: `modules/collector/internal/readiness/lock.go`
- Create: `modules/collector/internal/readiness/lock_test.go`
- Modify: `modules/collector/internal/app/control/bootstrap.go`
- Modify: `modules/collector/internal/app/runtimeboot/services.go`
- Modify: `modules/collector/cmd/cli/main.go`
- Create: `modules/collector/cmd/cli/readiness_lock.go`
- Create: `modules/collector/cmd/cli/readiness_lock_test.go`
- Modify: `scripts/build-collector-scf-package.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/collector/internal/deploy/deploy_moox_test.go`
- Create: `scripts/test-market-manifest-release.sh`
- Create: `scripts/verify-market-remote.sh`
- Create: `scripts/test-verify-market-remote.sh`
- Modify: `modules/collector/README.md`
- Modify: `docs/采集任务管理.md`
- Modify: `docs/内置市场行情采集架构.md`

- [ ] **Step 1: Add publish and legacy-cutover commands.** `collector function publish-markets` requires `--manifest-dir`、`--environment`、`--control-url`、`--cloud-account-id`、`--region` and accepts prebuilt `--zip`. Before upload it validates readiness evidence. Add typed adminclient wrappers for node list/update、JobItem list/cancel and function deployment; `collector legacy-cutover --mode preflight|drain|rollback --legacy-space crypto` uses those wrappers to handle old nodes/items/functions without raw HTTP calls or Storage deletion.
- [ ] **Step 2: Use exact function identities and environments.**

```text
moox-collector-stock-cn-scf        MOOX_SPACE_ID=stock_cn
moox-collector-stock-us-scf        MOOX_SPACE_ID=stock_us
moox-collector-crypto-binance-scf  MOOX_SPACE_ID=crypto_binance
moox-collector-crypto-okx-scf      MOOX_SPACE_ID=crypto_okx
```

Skip `stock_us` deployment while `runtime_enabled=false`; deploy its fourth function only after the reviewed US addendum has been executed and US-1 is green.
- [ ] **Step 3: Reconcile runtime configuration, not only code.** Existing functions must receive updated timeout、memory、environment and reserved concurrency when manifest settings change. Provider quota max concurrency cannot exceed SCF max concurrency.
- [ ] **Step 4: Package only runtime inputs and a generated readiness lock.** Add `moox-collector-cli readiness-lock` so the build invokes the same compiled built-in catalog and writes `readiness.lock.json` with environment plus manifest/evidence/factory IDs and hashes. Put that file in both service release config and SCF zip; exclude raw seeds/evidence/secrets from SCF. Remote publish revalidates evidence, while active Collector and SCF reject lock/manifest/factory mismatches before planning or polling.
- [ ] **Step 5: Add release/stage and V2 cutover assertions.** Release archives contain all logical manifests for `moox-cli init`; SCF zip contains runtime manifests plus generated readiness lock only. Deployment order is legacy preflight/drain -> backup `moox_collector.db` -> initialize fresh `moox_collector_market_v2.db` -> Storage ready -> Market init -> V2 CloudNode/Collector -> runtime-enabled functions. A failed step restores the old config/function state and never resets Storage.
- [ ] **Step 6: Add executable remote-verification scripts.** `verify-market-remote.sh` requires `MOOX_DEV_SSH_TARGET` and `MOOX_DEPLOY_DIR`, uses the logical Market CLI, and verifies V2 DB selection/old DB backup、no legacy pending jobs、init idempotency、function isolation、continuation、quota/fallback、single-active Collector、source/unified equality and controlled-gap repair. Its shell contract test uses fake `ssh`/CLI and asserts no Storage reset, secret argument or deletion of the old DB/Space occurs.
- [ ] **Step 7: Run the full local gate with fresh, uncached Go tests.**

```bash
set -euo pipefail
(cd packages/marketmanifest && GOWORK=off go test -count=1 ./...)
(cd packages/cloudruntime && go test -count=1 ./...)
(cd modules/storage && go test -count=1 ./...)
(cd modules/cli && go test -count=1 ./...)
(cd modules/cloudnode && go test -count=1 ./...)
(cd modules/collector && go test -count=1 ./...)
(cd modules/factor && go test -count=1 ./...)
(cd modules/trade && go test -count=1 ./...)
./scripts/check-module-boundaries.sh
bash scripts/test-market-manifest-release.sh
bash scripts/test-verify-market-remote.sh
for script in scripts/build-collector-scf-package.sh scripts/release.sh scripts/deploy-moox.sh scripts/test-market-manifest-release.sh scripts/verify-market-remote.sh scripts/test-verify-market-remote.sh; do bash -n "$script"; done
pnpm --dir web build:prod
./scripts/build.sh collector
./scripts/build.sh collector-scf
./scripts/build.sh cli
```

- [ ] **Step 8: Set executable bits and inspect the SCF package.**

```bash
set -euo pipefail
chmod +x scripts/test-market-manifest-release.sh scripts/verify-market-remote.sh scripts/test-verify-market-remote.sh
MOOX_MARKET_ENVIRONMENT=development OUT_PATH=/tmp/moox-collector-market.zip VERSION=plan ./scripts/build-collector-scf-package.sh
unzip -Z1 /tmp/moox-collector-market.zip | sort
test "$(unzip -Z1 /tmp/moox-collector-market.zip | grep -c '^main$')" -eq 1
test "$(unzip -Z1 /tmp/moox-collector-market.zip | grep -c 'readiness\.lock\.json$')" -eq 1
! unzip -Z1 /tmp/moox-collector-market.zip | grep -E 'metadata\.seed|provider-validation|secret|token'
```

- [ ] **Step 9: Stage and deploy the explicit Collector V2 cutover without resetting Storage.** The target must provide a mode-0600 `${MOOX_DEPLOY_DIR}/env.sh`; deploy sources it remotely and fails before stopping anything if service/cloud auth is missing. It performs legacy drain and DB backup before switching config, and aborts if old running items remain or the V2 DB path already contains an incompatible schema.

```bash
set -euo pipefail
: "${MOOX_DEV_SSH_TARGET:?set user@host or an SSH config alias}"
export MOOX_DEPLOY_DIR="${MOOX_DEPLOY_DIR:-/home/ubuntu/moox/prod}"
MOOX_COLLECTOR_MARKET_V2_CUTOVER=1 ./scripts/deploy-moox.sh --target "${MOOX_DEV_SSH_TARGET}" --dir "${MOOX_DEPLOY_DIR}" --goos linux --goarch amd64
scp /tmp/moox-collector-market.zip "${MOOX_DEV_SSH_TARGET}:${MOOX_DEPLOY_DIR}/collector/moox-collector-market.zip"
```

- [ ] **Step 10: Run idempotent init and publish runtime-enabled functions.** The first init may apply; the second must report total `applied=0, failed=0`. Cloud credentials remain in the remote environment, not shell arguments.

```bash
set -euo pipefail
: "${MOOX_DEV_SSH_TARGET:?set user@host or an SSH config alias}"
MOOX_DEPLOY_DIR="${MOOX_DEPLOY_DIR:-/home/ubuntu/moox/prod}"
ssh "${MOOX_DEV_SSH_TARGET}" "cd '${MOOX_DEPLOY_DIR}' && test -r ./env.sh && set -a && . ./env.sh && set +a && ./bin/moox-cli init --manifest-dir ./collector/config/markets --markets all --metadata-url http://127.0.0.1:20200" | tee /tmp/moox-market-init-1.json
ssh "${MOOX_DEV_SSH_TARGET}" "cd '${MOOX_DEPLOY_DIR}' && test -r ./env.sh && set -a && . ./env.sh && set +a && ./bin/moox-cli init --manifest-dir ./collector/config/markets --markets all --metadata-url http://127.0.0.1:20200" | tee /tmp/moox-market-init-2.json
python3 -c 'import json; d=json.load(open("/tmp/moox-market-init-2.json")); assert d["total"]["applied"] == 0 and d["total"]["failed"] == 0'
ssh "${MOOX_DEV_SSH_TARGET}" "cd '${MOOX_DEPLOY_DIR}' && test -r ./env.sh && set -a && . ./env.sh && set +a && : \"\${MOOX_CLOUD_ACCOUNT_ID:?}\" && : \"\${TENCENTCLOUD_REGION:?}\" && : \"\${MOOX_SERVICE_AUTH_ACCESS_KEY:?}\" && : \"\${MOOX_SERVICE_AUTH_SECRET_KEY:?}\" && ./bin/moox-cli collector function publish-markets --manifest-dir ./collector/config/markets --environment development --zip ./collector/moox-collector-market.zip --control-url http://127.0.0.1:11000 --cloud-account-id \"\${MOOX_CLOUD_ACCOUNT_ID}\" --region \"\${TENCENTCLOUD_REGION}\""
```

- [ ] **Step 11: Run the remote verification suite.** It creates a bounded dev-only rule with `max_pages=1`, uses a local stub for one retryable/429 candidate, queries a narrow logical Market range, and creates a controlled missing bucket in a disposable verification Subject/range. It must restore the rule/stub/fault settings on exit and never delete production rows.

```bash
set -euo pipefail
: "${MOOX_DEV_SSH_TARGET:?set user@host or an SSH config alias}"
MOOX_DEPLOY_DIR="${MOOX_DEPLOY_DIR:-/home/ubuntu/moox/prod}"
MOOX_DEV_SSH_TARGET="${MOOX_DEV_SSH_TARGET}" MOOX_DEPLOY_DIR="${MOOX_DEPLOY_DIR}" ./scripts/verify-market-remote.sh
```

- [ ] **Step 12: Assert function and control-plane isolation.** Every runtime-enabled node reports its expected Space and the same zip hash; cross-Space poll returns zero. Start a second Collector against the same control DB and prove it remains standby and cannot issue permits or publish JobItems.
- [ ] **Step 13: Assert bounded continuation and unique retry authority.** Every JobItem finishes pipeline work within 30 seconds, reports/ACKs within 45 seconds and uses cursor continuation. One-sided CollectMgr/CloudNode report failures and a 429 must create no concurrent CloudNode retry plus Collector fallback, and no duplicate row/event/outbox item.
- [ ] **Step 14: Assert data and coverage correctness.** `moox-cli market kline query` must return exact strings whose numeric counterparts and provenance match one source row. The controlled missing range moves from incomplete to complete after exactly one repair; provisional/conflict status remains visible until a sample resolves it.
- [ ] **Step 15: Update docs from observed behavior and commit.**

```bash
git add modules/cli/cmd/collector* modules/cli/internal/adminclient modules/cloudnode scripts modules/collector/cmd/cli modules/collector/internal/readiness modules/collector/internal/app/control/bootstrap.go modules/collector/internal/app/runtimeboot/services.go modules/collector/internal/deploy modules/collector/README.md docs/采集任务管理.md docs/内置市场行情采集架构.md
git commit -m "feat(collector): deploy built-in market functions"
```

## Verification Matrix

| Requirement | Automated evidence | Remote evidence |
| --- | --- | --- |
| Provider cannot write Storage | module-boundary scan and Provider package tests | source rows appear only through Pipeline JobItems |
| Source-first ordering | KlinePipeline call-order tests | induced source-write failure creates no unified row |
| Whole-row Provider selection | Quality Resolver atomic-source tests | unified OHLCV equals one source row exactly |
| Idempotent retries | REPLACE、event-key and cursor dedupe tests | repeated JobItem/report does not increase logical row/event count |
| Event recovery | unified-success/event-failure replay test | retry restores the deterministic event without changing revision |
| Record key stability | instrument/calendar/coverage retry tests with fixed RFC3339 versions | repeated feed pages replace the same logical records and latest reads are deterministic |
| No stale optional columns | Storage REPLACE test | switch winning Provider and verify removed field stays absent |
| Global quotas | execution-time permit、late lease and single-leader tests | observed physical requests/concurrency stay under manifest and standby issues none |
| Unique retry authority | finalize/outbox and one-sided report failure tests | Provider failure creates one later fallback and no CloudNode race |
| SCF bounded execution | slow Provider/Storage/finalize and one-item poll tests | duration under 30 seconds, keepalive work under 45 seconds |
| Query isolation | Market RPC fake Provider panic test | Provider outage does not break stored-data query |
| Query merge correctness | full-key k-way merge/composite cursor tests | same-time multi-Subject pages have no loss, duplicate or drift |
| Universe correctness | multi-source omission/delisting grace tests | full-market Subject count and Provider symbols remain stable across partial pages |
| Coverage correctness | Storage range、calendar/expected-bucket and sample tests | controlled gap is repaired once; provisional/conflict remains visible until resolved |
| Numeric consumer compatibility | dual-column writer and Factor DataFrame tests | unified numeric OHLCV feeds Factor while API returns exact strings |
| Built-in metadata protection | Metadata update/delete/cascade rejection tests | conflicting second init fails without mutation |
| US gate | disabled readiness/planner tests | US metadata exists but no function/JobItem is published before US-1 |
| Readiness lock | manifest/evidence expiry/fingerprint/factory mismatch tests | publish/runtime reject stale or unregistered capabilities |
| Per-Space isolation | CloudNode node/job tests | runtime-enabled functions cannot consume another Space's jobs |

## Plan Self-Review Checklist

- [x] Every first-stage requirement in the design maps to at least one task.
- [x] No Task asks a Provider to import Storage or control-plane packages.
- [x] All Provider/API-dependent claims have an explicit live-validation gate and offline fixture test.
- [x] `stock_us` is not presented as ready before US-1 and its provider-specific addendum.
- [x] Storage REPLACE and batch Subject registration land before Instrument Feed and multi-Provider writes.
- [x] Universe/Calendar fetch、policy materialization and resolve phases have stable generations and monotonic fencing.
- [x] Rule-to-task binding aggregates multiple schedules/history windows without overwriting one logical TaskInstance.
- [x] Cursor handling includes immutable scope、durable boundary and the control-plane continuation loop.
- [x] Execution-time weighted permits、single-active control and grouped retry outbox close the SCF concurrency path.
- [x] Source/unified/coverage/event roles and deterministic RecordKeys remain separate in every Market.
- [x] V2 Collector DB cutover preserves Storage data and has an explicit legacy drain/rollback boundary.
- [x] Readiness evidence、factory catalog and runtime lock are enforced before planning/polling.
- [x] Archive、snapshot、Tick and adjusted K lines remain outside scope.
- [x] Every phase ends with focused tests and a reviewable commit.

## Execution Order

Execute Tasks 1-15 in order to establish the generic framework and `crypto_binance` vertical slice. Tasks 16-20 may proceed only after their preceding generic contracts pass; real Provider commits require their validation gate. Task 21 is a hard stop for `stock_us`, not permission to invent an API. Complete Tasks 22-23 only after at least `crypto_binance` and `stock_cn` pass end-to-end tests.

Do not combine all tasks into one commit. After every milestone, run `git diff --check`, the milestone's fresh tests and `./scripts/check-module-boundaries.sh`, then request code review before starting the next milestone.
