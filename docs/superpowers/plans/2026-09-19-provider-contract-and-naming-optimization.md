# Provider 契约与数据命名优化执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不改变 moox 的 Storage、Strategy、Trade 核心职责的前提下，统一数据命名语义，强化 Provider/Fetcher 契约、能力描述、错误分类与测试门禁，并吸收 OpenBB 的可插拔数据接入设计。

**Architecture:** 保留 moox 当前的状态化量化运行时边界：Provider/Collector 负责接入与采集，Storage 负责事实数据，View/Factor 负责派生查询，Strategy/Trade 负责决策与执行。采集链路明确为“Provider 响应解码 → Normalize → Validate → Pipeline 写入”，其中 NormalizedKline 继续作为 Collector 内部模型；Provider 能力通过显式 Manifest 描述，由静态 allowlist 注册，不引入任意动态插件加载。查询协议继续与 redesign-quant-data-protocols OpenSpec 变更协同，不创建第二套 QueryFrame/DataView 协议。

**Tech Stack:** Go multi-module workspace、tRPC-Go、Protobuf、SQLite/Pebble/DuckDB、NATS JetStream、Go test、Vue/TypeScript（仅在查询契约落地时同步客户端类型）。

---

## 现状、命名决策与边界

### 现状证据

- modules/collector/internal/marketdata/kline.go:10 定义 NormalizedKline，包含 Provider/source provenance、Provider symbol、请求元数据和统一 OHLCV/时间字段。
- modules/collector/internal/marketdata/types.go:158-411 已有 KlineSpec、InstrumentSpec、ProviderDescriptor、MarketProvider、KlineFetcher 和 InstrumentFetcher。
- modules/collector/internal/marketwiring/market_runtime.go:82-149 当前使用大段 if/switch 将 market、instrument、provider、source 组合映射到构造器。
- modules/collector/internal/marketdata/registry.go 已提供强类型 Fetcher 查找，但尚未把 Provider 能力聚合为可检查的 Manifest。
- modules/storage/proto/view.proto 已有包含列定义、行、分页、active index 和 contract version 的查询响应；OpenSpec 变更计划中的 QueryFrame、DataViewColumn、ExplainQuery 应在同一查询演进线上实现。

### 采用的词汇规则

| 语义 | 统一词汇 | 使用范围 | 例子 |
| --- | --- | --- | --- |
| 外部数据转内部字段、单位、时间和别名 | Normalize / Normalized / 规范化 | Provider adapter、Collector 内部模型 | NormalizedKline、NormalizeCryptoSubjectID |
| 确定性序列化、签名材料、哈希输入 | Stable / 稳定 | JSON、签名、快照 hash、可复现字节串 | StableJSON、StableRequestMaterial |
| 唯一业务身份 | Subject、InstrumentID、InstrumentSymbol、Primary | 数据主键、内部身份、主存储 | SubjectID、InstrumentSymbol |
| 正式对外发布的跨模块协议 | Standard / 标准 | 经过 PB/API 评审的公共契约 | 不用于当前内部 NormalizedKline |

### 明确不做的替换

1. 不把 NormalizedKline 改为 StandardKline。Standard 表示已经发布、跨模块稳定承诺的公共协议，而当前类型是 Collector 内部 Provider 输出模型。
2. 不全局把所有英文 canonical 替换为 normalized。身份语义改用 Subject/Instrument，确定性字节语义改用 Stable，只有数据别名和格式转换才改用 Normalize。
3. 不把 DataFrame 引入 Storage 事实数据层，不用 OpenBB 的弱类型动态字段模型替代 moox 当前强类型 Provider/Storage 契约。
4. 不采用任意目录扫描或未授权的动态插件加载。Provider 必须经过编译期注册、Manifest 校验和能力测试。
5. 不复制一套新的 QueryFrame/DataView PB；查询协议以 /Users/mooyang/Documents/go/src/github.com/mooyang-code/openspec/changes/redesign-quant-data-protocols 为唯一演进来源。

## Task 1: 固化命名规则并建立迁移清单

**Files:**
- Create: docs/architecture/data-naming-conventions.md
- Test: modules/collector/internal/marketdata/subject_test.go
- Test: modules/collector/internal/domain/collect_params_test.go
- Test: packages/requestauth/requestauth_test.go

- [ ] **Step 1: 写入命名规范文档**

在 docs/architecture/data-naming-conventions.md 固化四类词汇，并加入决策表：CanonicalCryptoSubjectID → NormalizeCryptoSubjectID；CanonicalizeCryptoInstrument → NormalizeCryptoInstrument；CanonicalJSON → StableJSON；requestauth.Canonical → StableRequestMaterial；CanonicalSymbol → InstrumentSymbol；NormalizedKline 保持不变。明确变量名可以使用 normalized，不得新增 canonical；历史协议、外部厂商字段和不表示上述语义的文档名单独处理。

- [ ] **Step 2: 建立全量符号清单并分类**

执行：

    rg -n "Canonical|canonical|NormalizedKline|StandardKline" modules packages docs > /tmp/moox-naming-inventory.txt

把结果按“规范化、稳定序列化/签名、唯一身份、历史/协议措辞”分类；迁移范围至少覆盖 subject.go、kline_resample.go、requestauth.go、dataset_metrics.go、instrument_pipeline.go 和对应测试。

- [ ] **Step 3: 先补充行为锁定测试**

将以下断言加入 subject_test.go，改名后保持结果不变：

    func TestCryptoSubjectNormalizationRemovesVenueSuffix(t *testing.T) {
        cases := map[string]string{
            "0G-USDT-SPOT": "0G-USDT",
            "BTC-USDT-SWAP": "BTC-USDT",
            "sol-usdt-spot": "SOL-USDT",
        }
        for input, want := range cases {
            if got := NormalizeCryptoSubjectID(input); got != want {
                t.Fatalf("NormalizeCryptoSubjectID(%q) = %q, want %q", input, got, want)
            }
        }
    }

- [ ] **Step 4: 运行基线测试并提交**

    (cd modules/collector && go test ./internal/marketdata ./internal/domain)
    (cd packages/requestauth && go test ./...)
    git add docs/architecture/data-naming-conventions.md modules/collector/internal/marketdata/subject_test.go modules/collector/internal/domain/collect_params_test.go packages/requestauth/requestauth_test.go
    git commit -m "docs: define moox data naming conventions"

## Task 2: 完成身份与采集数据命名迁移

**Files:**
- Modify: modules/collector/internal/marketdata/subject.go
- Modify: modules/collector/internal/marketdata/types.go:413-424
- Modify: modules/collector/internal/marketdata/kline.go:10-22
- Modify: modules/collector/internal/marketfetch/instrument_pipeline.go
- Modify: modules/collector/internal/marketfetch/scheduler.go
- Modify: modules/collector/internal/marketfetch/period_readiness.go
- Modify: modules/collector/internal/marketfetch/kline_pipeline.go
- Modify: modules/collector/internal/sources/binance/marketdata_adapter.go
- Modify: modules/collector/internal/sources/binance/marketdata_adapter_test.go
- Modify: modules/collector/internal/marketfetch/collector_completed_test.go
- Modify: modules/collector/internal/marketfetch/instrument_pipeline_test.go

- [ ] **Step 1: 重命名身份规范化函数**

将 subject.go 的 API 改为 NormalizeCryptoSubjectID 和 NormalizeCryptoInstrument：

    func NormalizeCryptoSubjectID(id string) string {
        value := strings.ToUpper(strings.TrimSpace(id))
        for _, suffix := range []string{"-SPOT", "-SWAP"} {
            value = strings.TrimSuffix(value, suffix)
        }
        return value
    }

    func NormalizeCryptoInstrument(instrument Instrument) Instrument {
        instrument.SubjectID = NormalizeCryptoSubjectID(instrument.SubjectID)
        return instrument
    }

删除旧 CanonicalCryptoSubjectID 和 CanonicalizeCryptoInstrument，不保留兼容 wrapper。

- [ ] **Step 2: 把 CanonicalSymbol 改成 InstrumentSymbol**

在 marketdata.Instrument 中把字段改成 InstrumentSymbol，并同步替换 Binance adapter、instrument pipeline 的合并/校验/日志和测试 fixture。InstrumentSymbol 是业务身份符号，不要改成 NormalizedSymbol。

- [ ] **Step 3: 明确 NormalizedKline 的内部边界**

在 kline.go 中加入注释：NormalizedKline 是 Provider 响应解码、时间/单位规范化并通过 OHLCV 校验后的 Collector 内部结果，不是公共跨模块 StandardKline。保留 ValidateNormalizedKline 名称和现有校验逻辑。

- [ ] **Step 4: 扫描、测试、提交**

    rg -n "CanonicalCryptoSubjectID|CanonicalizeCryptoInstrument|CanonicalSymbol" modules/collector
    (cd modules/collector && go test ./internal/marketdata ./internal/marketfetch ./internal/sources/binance)
    git add modules/collector/internal/marketdata modules/collector/internal/marketfetch modules/collector/internal/sources/binance
    git commit -m "refactor(collector): use normalized market identities"

预期：旧符号在 Collector 代码中为零，SubjectID、instrument 合并、Binance adapter 和 Kline 校验测试全部通过。

## Task 3: 将确定性序列化语义改为 Stable

**Files:**
- Modify: modules/collector/internal/domain/kline_resample.go:47-55
- Modify: modules/collector/internal/domain/collect_params.go:318-352
- Modify: modules/collector/internal/rpc/service.go:599-617
- Modify: modules/collector/internal/domain/collect_params_test.go
- Modify: packages/requestauth/requestauth.go:1-110
- Modify: packages/requestauth/requestauth_test.go
- Modify: packages/gatewayproxy/route.go:348-362
- Modify: packages/gatewayproxy/route_test.go
- Modify: packages/report/dataset_metrics.go:96-260
- Modify: packages/report/dataset_metrics_test.go

- [ ] **Step 1: 重命名 CollectParams 稳定 JSON API**

把 (*CollectParams).CanonicalJSON() 改为 StableJSON()，把 formatDurationCanonical 改为 formatDurationStable，把 RPC 局部变量 canonical 改为 stableJSON；字段过滤、JSON 编码和输出字节必须保持不变。测试改名为 TestKlineResampleStableJSONOmitsRuntimeRepairPolicy。

- [ ] **Step 2: 重命名 requestauth 签名材料 API**

把 requestauth.Canonical 改为 StableRequestMaterial，把 canonicalHeaders 改为 stableHeaders，把 Sign 中的局部变量 canonical 改为 stableMaterial。版本、字段顺序、header 规则、body hash 和 HMAC 结果必须保持不变，只改变 Go 符号和注释。

- [ ] **Step 3: 重命名 hash 和 Dataset key 的内部变量**

在 packages/gatewayproxy/route.go 中把 hash 输入结构变量改为 stableSnapshot，错误文本改为 marshal stable routes，测试改名为 TestStableSnapshotHashExcludesGeneratedAt。在 packages/report/dataset_metrics.go 中把 canonicalDatasetKey 改为 normalizedDatasetKey，把局部变量改为 normalized；NormalizeDatasetFrequency 保持函数名但更新注释。

- [ ] **Step 4: 扫描、测试、提交**

    rg -n "CanonicalJSON|Canonical\\(|canonicalHeaders|canonicalDatasetKey|canonicalSnapshot|CanonicalCryptoSubjectID|CanonicalizeCryptoInstrument|CanonicalSymbol" modules packages
    (cd modules/collector && go test ./internal/domain ./internal/rpc)
    (cd packages/requestauth && go test ./...)
    (cd packages/gatewayproxy && go test ./...)
    (cd packages/report && go test ./...)
    git add modules/collector packages/requestauth packages/gatewayproxy packages/report
    git commit -m "refactor: replace canonical serialization names with stable"

代码结果应为零；签名向量和稳定 JSON 输出与改名前完全一致。

## Task 4: 聚合 Provider 能力为可校验 Manifest

**Files:**
- Create: modules/collector/internal/marketdata/manifest.go
- Create: modules/collector/internal/marketdata/manifest_test.go
- Modify: modules/collector/internal/marketdata/registry.go
- Modify: modules/collector/internal/marketdata/registry_test.go
- Modify: modules/collector/internal/marketdata/types.go:292-411

- [ ] **Step 1: 定义 ProviderCapability 和 ProviderManifest**

新增：

    type ProviderCapability string
    const (
        CapabilityKline ProviderCapability = "kline"
        CapabilityInstrument ProviderCapability = "instrument"
    )

    type ProviderManifest struct {
        Descriptor ProviderDescriptor
        Capabilities []ProviderCapability
        Kline *KlineSpec
        Instrument *InstrumentSpec
    }

    func BuildProviderManifest(provider MarketProvider) (ProviderManifest, error)
    func (m ProviderManifest) Validate() error
    func (m ProviderManifest) SupportsKline(req KlineRequest) bool

BuildProviderManifest 从现有 KlineFetcher/InstrumentFetcher 接口读取能力；Validate 调用已有 descriptor/spec 校验，并拒绝 capability 与 spec 不一致。

- [ ] **Step 2: 让 Registry 保存 Manifest**

Registry 新增 manifests map[string]ProviderManifest；Register 按“非空 → Build → Validate → 重复 key 检查 → 保存 Provider 和 Manifest”执行。新增 Manifest(SourceKey) 和 Manifests()；后者按 ProviderID、SourceID 排序并返回拷贝。

- [ ] **Step 3: 写测试并提交**

测试普通 Provider、Kline Provider、Instrument Provider、无效 spec、重复 source key 和稳定排序：

    (cd modules/collector && go test ./internal/marketdata -run 'TestProviderRegistry|TestProviderManifest')
    git add modules/collector/internal/marketdata
    git commit -m "feat(collector): expose validated provider manifests"

## Task 5: 用显式 Provider Catalog 收敛 Market Wiring

**Files:**
- Create: modules/collector/internal/marketwiring/provider_catalog.go
- Create: modules/collector/internal/marketwiring/provider_catalog_test.go
- Modify: modules/collector/internal/marketwiring/market_runtime.go:82-149
- Modify: modules/collector/internal/marketwiring/crypto_runtime_test.go
- Modify: modules/collector/internal/marketwiring/stock_runtime_test.go
- Modify: modules/collector/internal/jobs/registry.go
- Modify: modules/collector/internal/jobs/route.go
- Modify: modules/collector/internal/jobs/registry_test.go

- [ ] **Step 1: 定义显式 key 和 factory**

新增 ProviderCatalogKey、ProviderFactory 和 ProviderCatalog：

    type ProviderCatalogKey struct {
        MarketID string
        InstrumentType marketdata.InstrumentType
        ProviderID string
        SourceID string
    }
    type ProviderFactory func() (marketdata.MarketProvider, error)
    func NewProviderCatalog() *ProviderCatalog
    func (c *ProviderCatalog) Register(key ProviderCatalogKey, factory ProviderFactory) error
    func (c *ProviderCatalog) Build(key ProviderCatalogKey) (marketdata.MarketProvider, error)
    func (c *ProviderCatalog) Keys() []ProviderCatalogKey

key 的四个字段均需规范化；重复 key、空 key、nil factory 和 factory 返回非法 Provider 必须失败。

- [ ] **Step 2: 迁移现有 switch**

将 Binance、stockcn index/bond、stockhk、stockus 构造器拆成显式 factory，在 provider_catalog.go 登记。Binance spot/swap 的 product type 固定在各自 factory 中。NewMarketKlinePipeline 保留 dataset、calendar、product type 和 fallback 逻辑，只把 newMarketProvider 替换为 catalog.Build。

- [ ] **Step 3: 校验 Catalog 覆盖并对齐 Job route**

测试每个 key 都能构造 Provider 且生成合法 Manifest；不支持组合返回当前等价的 not found 错误。所有会被 jobs.JobRouteFor 选中的组合都必须有 Catalog factory；kline_resample 继续保持本地执行，不创建云队列 route。

- [ ] **Step 4: 测试并提交**

    (cd modules/collector && go test ./internal/marketwiring ./internal/jobs)
    (cd modules/collector && go test ./internal/marketfetch -run 'Test.*Runtime|Test.*Pipeline|Test.*Provider')
    git add modules/collector/internal/marketwiring modules/collector/internal/jobs
    git commit -m "refactor(collector): register providers through explicit catalog"

## Task 6: 建立通用 Provider 契约测试和错误输出边界

**Files:**
- Create: modules/collector/internal/marketdata/provider_contract_test.go
- Modify: modules/collector/internal/marketdata/errors.go
- Modify: modules/collector/internal/marketdata/errors_test.go
- Modify: modules/collector/internal/marketfetch/kline_pipeline.go
- Modify: modules/collector/internal/marketfetch/instrument_pipeline.go
- Modify: modules/collector/internal/rpc/service.go
- Modify: modules/cli/internal/command/data_kline.go
- Modify: modules/cli/internal/command/data_kline_test.go

- [ ] **Step 1: 固化 Kline Fetcher 通用契约**

所有 Catalog Provider 使用同一测试 helper，断言 Descriptor.Validate、KlineSpec.Validate 和每条返回行的 ValidateNormalizedKline。HTTP Provider 使用已有 transport fake/fixture，不依赖公网；覆盖当前登记的 Binance、EastMoney、Sina、Tencent Kline adapter。

- [ ] **Step 2: 固化 Instrument Fetcher 通用契约**

断言 InstrumentSpec.Validate、snapshot ID/source provider 非空、FullSnapshot 结果满足完整快照校验、每个 instrument 的 SubjectID 和 InstrumentSymbol 非空、ProviderSymbol 保留外部值。

- [ ] **Step 3: 稳定化 RPC/CLI 错误输出**

保留 marketdata.ErrorKind 作为内部分类，在边界输出 code、retryable、provider_id、source_id、request_id、message。CanFallback 仍只由 ErrorKind 决定；CLI 文本、JSON 和 Python message 从同一结果对象渲染，不根据上游错误字符串猜测 fallback。

- [ ] **Step 4: 测试并提交**

覆盖 timeout、TCP、rate limited、unsupported symbol、invalid request、history coverage、context canceled，断言 ErrorKind、fallback 和 CLI code/retryable：

    (cd modules/collector && go test ./internal/marketdata ./internal/marketfetch ./internal/rpc)
    (cd modules/cli && go test ./internal/command -run 'Test.*Kline|Test.*Error')
    git add modules/collector modules/cli/internal/command
    git commit -m "test(collector): enforce provider and error contracts"

## Task 7: 将查询契约与 OpenSpec 变更对齐

**Files:**
- Create: docs/architecture/query-contract-migration.md
- Modify: modules/storage/proto/view.proto
- Modify: modules/storage/internal/service/view/query.go
- Modify: modules/storage/internal/service/view/series_window_query.go
- Modify: modules/storage/internal/service/view/query_test.go
- Modify: modules/cli/internal/command/data_kline.go
- Modify: modules/cli/internal/command/data_kline_test.go
- Modify: packages/pyruntime/protocol/message.go
- Modify: packages/pyruntime/python/moox_pyruntime/protocol.py
- Reference: /Users/mooyang/Documents/go/src/github.com/mooyang-code/openspec/changes/redesign-quant-data-protocols/specs/quant-query-service/spec.md

- [ ] **Step 1: 先完成协议映射表**

在 query-contract-migration.md 记录：space_id → workspace_id；view_id → data_view_id；column_names → select_columns/DataViewColumn；selectors + time_range → instrument_ids + QueryTime；served_active_index_* → execution metadata；ret_info → 统一状态 envelope。明确调用方不传 universe、fallback policy 或 execution policy。

- [ ] **Step 2: 增加当前查询响应契约测试**

成功响应必须包含 ret_info、列定义、active index revision；查询期间 active index 变化返回 VIEW_NOT_READY；参数错误不返回部分 rows；rows_per_series 拒绝分页、排序和 exact total 组合。

- [ ] **Step 3: 让 CLI 和 Python 只消费结果 envelope**

data_kline.go 和 Python protocol.py 只读取状态、columns、rows、page/complete、served contract metadata；文本、JSON 和 Python message 由同一内部结果对象渲染。

- [ ] **Step 4: 对接 OpenSpec QueryFrame/ExplainQuery**

OpenSpec 的 quant-query-service PB/handler 落地后，补 snapshot_time、time_range、多因子过滤、DataView policy 服务端选择、TextSearch 独立路由和 ExplainQuery planner 路径测试；不为旧 canonical 术语创建兼容协议。

- [ ] **Step 5: 测试并提交**

    (cd modules/storage && go test ./internal/service/view ./internal/service/primarystore)
    (cd modules/cli && go test ./internal/command -run 'Test.*Kline|Test.*Query')
    (cd packages/pyruntime && go test ./...)
    git add docs/architecture/query-contract-migration.md modules/storage/proto modules/storage/internal/service/view modules/cli/internal/command packages/pyruntime
    git commit -m "refactor: align query result contracts with quant protocol"

## Task 8: 文档、门禁和完整验证

**Files:**
- Create: docs/architecture/provider-contract.md
- Modify: docs/内置市场行情采集架构.md
- Modify: docs/行情数据归档模块设计.md
- Modify: docs/运维/MooX-Doctor运维.md
- Modify: Makefile

- [ ] **Step 1: 写 Provider 契约文档并更新术语**

provider-contract.md 说明 ProviderDescriptor、ProviderManifest、KlineSpec、InstrumentSpec、NormalizedKline、ErrorKind、Registry、Catalog 的边界，并规定新 Provider 必须提交 factory、Manifest 校验、Fetcher 契约测试和错误分类测试。现有采集架构把“标准化”写为 NormalizedKline，把主身份写为 primary identity，把确定性 JSON/签名/hash 写为 Stable；不修改外部协议字段名和历史事件名称。

- [ ] **Step 2: 增加 Makefile 验证目标**

新增：

    test-provider-contract:
        cd modules/collector && go test ./internal/marketdata ./internal/marketfetch ./internal/marketwiring ./internal/jobs

    test-query-contract:
        cd modules/storage && go test ./internal/service/view ./internal/service/primarystore

    verify-architecture: test-provider-contract test-query-contract
        git diff --check

目标只能运行测试和 diff 检查，不启动公网请求、删除数据库或改写用户配置。

- [ ] **Step 3: 执行全量验证**

    make verify-architecture
    go test ./...
    pnpm exec vitest run --config vitest.config.ts
    pnpm build:prod
    git diff --check

若 go test ./... 因 Go workspace 外部模块不支持而失败，按各 modules/*/go.mod 和 packages/*/go.mod 逐模块执行同等测试，并记录具体失败模块和原因，不把已有工作区失败误报为通过。

- [ ] **Step 4: 只提交本计划产生的文件并推送**

先执行 git status --short 和 git diff --name-only；仅暂存本任务文件，不要把 artifacts/、既有运维文档、监控代码或其他并行任务文件一并提交。完成后执行：

    git diff --cached --check
    git commit -m "docs: add provider contract optimization plan"
    git push

## 完成判定

1. 新代码不再新增 Canonical；采集规范化使用 Normalize/Normalized，确定性序列化使用 Stable，正式公共协议才使用 Standard。
2. NormalizedKline 保持为 Collector 内部模型，并有明确边界注释和校验测试。
3. Registry 能返回经过校验的 Manifest，Market wiring 通过显式 Catalog 构造 Provider，不使用任意动态插件发现。
4. 已登记 Provider 均通过统一 Fetcher 契约测试，错误分类和 fallback 语义由内部 ErrorKind 驱动。
5. Storage 查询结果保留列、行、分页、active index 和 contract metadata；查询协议与 OpenSpec 的 QueryFrame/DataView/ExplainQuery 演进保持单一来源。
6. 相关 Go 单测、查询契约测试、前端回归和构建命令均有可重复执行入口。
