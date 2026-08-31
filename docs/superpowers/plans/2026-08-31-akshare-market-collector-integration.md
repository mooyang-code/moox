# Market Data Collector Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 AkShare、QUANTAXIS 和 easy_tdx 中确认过的交易日历、TDX 协议及行情接口，以 Go 原生公共库和 Provider 的方式整合进 MooX Collector，覆盖 A 股、港股、美股，并为指数、可转债等资产建立独立且可扩展的分类、标准化和采集链路。

**Architecture:** 新增根级 `packages/marketcalendar`、`packages/routeprobe` 和 `packages/tdx` 公共库：`marketcalendar` 使用 `go:embed` 固化中国交易日历，`routeprobe` 抽象解析候选、实际执行环境探测、线路评分、快照和有限 fallback，`tdx` 以 Go 原生方式实现 easy_tdx 中已验证的 TDX TCP 帧、握手、压缩、编解码、普通行情和扩展行情，并提供 TDX 专用探针。Collector 新增强类型 `MarketProvider`、`KlineFetcher`、`InstrumentFetcher`、`KlineSpec` 和统一 `NormalizedKline`，Provider 按真实上游（TDX、东方财富、新浪、腾讯、中证、申万）归类，而不是把 AkShare、QUANTAXIS 或 Python 包作为运行时依赖。Provider 下的具体行情通道使用 `SourceID` 标识，例如 `tdx/normal_7709`、`tdx/ex_classic_7727` 和 `tdx/ex_mac_7727`；`SourceKey` 由 `ProviderID + SourceID` 组成。通用 KlinePipeline 负责日历/交易时段、标准化、完整性校验、线路路由、幂等 Storage 写入和来源追踪；Market Descriptor 负责区分 `stock_cn`、`stock_hk`、`stock_us` 以及 `equity`、`index`、`convertible_bond` 等资产类型。现有加密货币 HTTP 链路与 TDX 共用 `routeprobe`，但使用各自的 HTTP/TCP 协议探针。TDX 实时任务采用 SCF Timer 短时执行：函数内复用连接，任务结束后退出；线路选择按 `scf_region + provider + source + transport + host:port` 维护。SCF 不实现主动限频或全局配额，随机公网出口只是运行环境事实，不能作为规避上游限制的功能承诺。

**Tech Stack:** Go 1.25、多 module `go.work`、tRPC-Go、`net`、`encoding/binary`、`compress/zlib`、`httptest`、`testify`、现有 Collector `httpclient`、Storage PrimaryStore、SQLite/GORM、YAML、`go:embed`、TDX golden wire fixtures。

## 当前执行状态（2026-09-01）

本节是总计划的进度索引，不能替代后续任务中的真实验收证据。`[x]` 只表示代码和离线测试已落地；真实 SCF、上游线路和 Storage read-back 仍须按第 16 节独立验收。

- [x] 已建立 `packages/marketcalendar`：内嵌中国静态交易日历、`CivilDate`、三态覆盖查询、`valid_through` readiness、manifest/hash 校验和仓库校验脚本。
- [x] 已建立 `packages/routeprobe`：候选解析、协议无关探测协调、HTTP Host/SNI 探针、评分、快照 TTL、按 `scf_region + ProviderID + SourceID + Transport + host:port` 隔离以及有限 fallback；不包含主动限频或全局配额。
- [x] 已建立 `packages/marketmanifest` 和 Collector `MarketData` 契约：`ProviderID`、`SourceID`、`ProtocolVariant`、`SourceKey`、Kline/Instrument Spec、统一标准化 K 线和字段级 null 语义。
- [x] 已落地 Collector 的 Provider Registry、通用 Provider Router、KlinePipeline、A 股/港股/美股交易时段策略，以及东方财富 A 股、港股、美股、指数、可转债的 HTTP 基线适配器。
- [x] 已落地 TDX 普通 `normal_7709` 的 Go wire/transport/命令/Provider 基线，以及扩展 classic/MAC 的独立帧、目录、登录和协议探针代码；这些扩展 Source 尚未因离线布局而自动标记生产 enabled。
- [x] 已修复批量写入的来源事件幂等边界：同一批次内每个标的使用稳定且独立的 `SourceEventID`，避免 Storage 按 `source_event_id + dataset` 去重时误丢后续标的。
- [ ] 尚未完成完整 TDX Wire Spike：仍需在目标线路录制完整请求、16 字节响应头、压缩体、解压体、错误帧和人工对账结果，并分别确认 `normal_7709`、`ex_classic_7727`、`ex_mac_7727`。
- [ ] 尚未完成旧 Collector runtime 的通用 Executor/Job/SCF composition root 迁移、Metadata 初始化和正式 Storage 契约接入；当前新增 Pipeline 已用 fake Storage 完成端到端测试，但不等于正式环境验收。
- [ ] 尚未实现新浪、腾讯、中证、申万等 HTTP Provider；它们只保留在目录和配置中的 `catalog_only`，期货、期权、基金、REITs、外汇、黄金同样只登记不实现。
- [ ] 尚未完成正式 SCF 多地域出口、TDX/HTTP live probe、最优线路快照发布和 Storage read-back；本地通过不作为云端发布证明。

当前已经可以编译的通用 SCF 入口是 `modules/collector/cmd/scf/market_data`，它接受一批明确的 `MarketID/InstrumentType/SourceKey` 请求并调用通用 Pipeline；它不是旧 `crypto_market` 入口的兼容模式。TDX 扩展 Source 仍必须等待 Wire Spike 结论后才能接入该入口。

通用入口也已接入 `moox-cli collector function package --entrypoint market_data` 的本地打包路径；正式发布仍必须先补齐对应 `custom.toml` Space、Metadata/Timer 规则、Storage 凭据和 SCF 出口 read-back，不能把仅通过本地构建的 zip 视为正式上线。

命名约定：本计划不再使用 `Feed` 或 `CapabilityKey` 作为公共概念。需要区分同一 Provider 的不同访问通道时使用 `SourceID`，组合身份使用 `SourceKey{ProviderID, SourceID}`；`ProtocolVariant` 仅描述协议握手/帧/登录差异，能力范围由 Source 的 manifest 和 Spec 声明。

---

## 0. 范围与不变约束

本计划面向未上线的新项目，现有 [内置市场行情采集架构](../内置市场行情采集架构.md) 和 [2026-08-29 stock-cn 计划](2026-08-29-stock-cn-1m-multi-provider-scf.md) 仅作为设计参考。后者由本计划取代，不再与本计划并行执行。本计划是新的唯一实现依据，不重复建设另一套 Collector 框架，也不承诺旧代码、旧配置、旧 Dataset 或旧任务数据兼容。

本计划采用以下明确前提：

- AkShare 仅作为接口清单、参数语义和上游协议的参考；生产 Collector 使用 Go HTTP Provider，不在 SCF 中启动 Python，也不把 `packages/pyruntime` 扩展给行情采集。
- Tushare 明确排除：不实现需要 token/账户登录的 Tushare Provider，不增加 token、账户、积分或 Tushare API 配置；现有计划中的免费 HTTP Provider 和 TDX Provider 不依赖 Tushare。
- `SourceID` 表示一个 Provider 下的具体行情访问通道，不限定为主动推送数据流；例如 `tdx/normal_7709`、`tdx/ex_classic_7727`、`tdx/ex_mac_7727` 和 `binance/spot_http`。`ProviderID + SourceID` 组成唯一 `SourceKey`。
- `tool_trade_date_hist_sina()` 是动态源；MooX 运行时使用带版本和校验的静态日历，不在每次采集中请求新浪日历。
- QUANTAXIS 的 `QATdx.py`、`QAThs.py` 和 easy_tdx 仅用于提取接口、字段和协议语义；不复制 QUANTAXIS 的 Python 运行时。easy_tdx 当前为 Python 实现，Go 侧只移植已被 wire fixture 或实时探测确认的协议能力。
- `tdx-go` 的线路优选仅作为算法参考：`config.GetBestStockQuotesServer` 并发对候选 IP 做 5 次 ICMP 探测，以平均 RTT 排序，并淘汰丢包率超过 50% 的线路。它依赖特权 ICMP，且没有验证实际 TDX `host:port`、协议握手或首个有效响应；SCF 不原样移植这段逻辑，改为从实际 SCF 地域对候选 `host:port` 做 TCP/协议探测，并缓存按地域、Provider、Source 和 Transport 区分的线路快照。
- 当前 MooX 没有传统意义上的“DNS 代理”：现有链路是 Trade DNS Resolver/本地 DNS 解析产生候选 IP，`dnscache` 保存快照，`httpclient.GetWithIPs` 在保留 Host/SNI 的前提下按候选 IP 连接，失败后回退到域名解析。该能力目前主要服务 Binance HTTP；本计划不把它误称为全局 DNS 服务或公网转发代理，而是将 DNS 候选解析与实际线路探测抽象为公共 `packages/routeprobe`，供 Binance 等加密货币 HTTP Provider 和 TDX TCP Provider 复用。
- Trade Resolver 返回的 TCP 延迟是 Trade 计算节点到目标地址的观测值，不能直接当作 SCF 到目标地址的最优线路。`routeprobe` 将其作为候选初始排序/无 SCF 探针时的 fallback hint；生产启用前仍须从实际 SCF 地域使用对应协议探测并更新线路快照。
- TDX 不承担交易日历职责。普通 TDX 使用 TCP `7709` 握手协议，扩展 TDX 使用 TCP `7727` 扩展协议；两者都只负责行情/证券目录请求，交易日历继续由 `packages/marketcalendar` 提供。
- TDX SCF Timer 默认使用公网访问且不配置固定公网出口 IP；随机公网出口不纳入业务身份，也不作为规避频控的功能承诺。本阶段不实现 Provider 限频、全局配额、主动错峰或频控冷却；只保留连接超时、函数 deadline、单次批次边界和有限重试等运行可靠性约束。
- 中国交易日历第一阶段覆盖 A 股、指数和可转债；港股、美股不复用中国日历，使用各自的交易时段策略，并在加入对应假日数据后再启用严格的交易日缺口判断。
- 同一逻辑 K 线的 OHLCV 和 amount 必须来自同一个 Provider，不按字段拼接，不用 `close * volume` 猜造 amount。
- Provider 不选择 Dataset、不直接写 Storage、不管理 TaskInstance；这些职责属于 Market Module、KlinePipeline 和通用 Storage writer。
- 日线和分钟线按 `instrument_type` 拆分 Dataset，不把股票、指数和可转债混在同一个字段契约中。
- 第一批运行时交付为 TDX A 股股票/指数日线和分钟线、A 股可转债日线，以及 AkShare HTTP Provider 的 A 股、港股、美股日线/分钟线；港股/美股 TDX 扩展行情先完成动态市场、证券目录、字段和 SCF 出口验证后再启用。期货、期权、基金、REITs、外汇和黄金接口先完成统一目录与能力登记，再按同一契约分批接入，不在首批中伪装成已经可用。

### 执行顺序

当前文件是总执行计划；实际执行必须遵循以下顺序，不能按文档章节顺序直接并行实现：

1. 接口目录、Source 矩阵和完整 TDX Wire Spike。
2. 公共静态交易日历和 `CivilDate`/覆盖状态 API。
3. MarketData、Provider、SourceKey、Kline 和 Session 强类型契约。
4. 公共 `routeprobe` 与 Binance HTTP 路由迁移；该步骤完成后才能进入 TDX Go 协议实现。
5. TDX `normal_7709` 协议和 A 股 Provider。
6. TDX `ex_classic_7727`、`ex_mac_7727` 分别验证，再接入港股/美股扩展行情。
7. 东方财富、新浪、腾讯等 HTTP Provider，随后接入指数和可转债。
8. 通用 Executor、Job Registry、SCF Handler、Metadata 和 Storage 迁移；旧入口在迁移后删除。
9. 期货、期权、基金、REITs 等扩展接口只在独立契约完成后分批实现。
10. 最后进行实际 SCF 出口、线路选择、Provider 连通性和 Storage read-back 验收。

## 1. 目标接口与数据集矩阵

### 1.1 Provider 分类

| MooX Provider ID | Source/协议 | AkShare/QUANTAXIS/easy_tdx 参考接口 | 覆盖对象 | 首批状态 |
| --- | --- | --- | --- | --- |
| `tdx` | `normal_7709`，normal TCP `7709` | easy_tdx `TdxClient.get_security_bars`、`get_index_bars`；QUANTAXIS `QATdx.py`；tdx-go `config.GetBestStockQuotesServer` | A 股股票、指数、可转债及普通证券 | 实现 A 股日线/分钟线、指数日线/分钟线和线路优选 |
| `tdx` | `ex_classic_7727`，extended classic TCP `7727` | easy_tdx `ExTdxClient.get_markets`、`get_instrument_info`、`get_instrument_bars` | 港股、美股及扩展市场 | 先做协议验证和最小目录能力 |
| `tdx` | `ex_mac_7727`，extended mac TCP `7727` | easy_tdx `MacExClient` 及 MAC extended commands | 港股、美股及扩展市场 | 单独验证特殊帧和协议登录，未确认前不启用 |
| `eastmoney` | HTTP | `stock_zh_a_hist`、`stock_hk_hist`、`stock_us_hist`、指数/可转债 EM 接口 | A 股、港股、美股、指数、可转债 | 实现日线和可用分钟线 |
| `sina` | HTTP | `stock_zh_a_daily`、`stock_zh_a_minute`、`stock_hk_daily`、`stock_us_daily` | A 股、港股、美股及期权/期货扩展 | 实现股票日线/分钟线；扩展接口登记 |
| `tencent` | HTTP | `stock_zh_a_hist_tx`、`stock_zh_index_daily_tx`、`stock_zh_ah_daily` | A 股、A+H、指数 | 实现 A 股日线，其他能力登记 |
| `csindex` | HTTP | `index_hist_cni`、`stock_zh_index_hist_csindex` | 中证指数 | 实现指数日线 |
| `sw` | HTTP | `index_hist_sw`、`index_hist_fund_sw` | 申万指数、申万基金指数 | 实现指数日线；基金指数单独登记 |

Provider ID 代表真实上游，不代表 Python 包名；Source ID 代表 Provider 下的具体行情通道，`tdx/normal_7709`、`tdx/ex_classic_7727` 和 `tdx/ex_mac_7727` 不得混用端口、握手、登录方式和字段解码。Registry 使用 `SourceKey{ProviderID, SourceID}` 注册，拒绝重复 SourceKey。未来加入其他来源时，必须先通过 Source Spec 和 fixture/实时探测确认完整字段、历史范围、分页和错误语义。Tushare 不进入该矩阵。

### 1.2 MooX Market、Instrument 和 Dataset

| Market ID | Instrument Type | Dataset ID | 首批频率 | 日历/时区 |
| --- | --- | --- | --- | --- |
| `stock_cn` | `equity` | `stock_cn_kline` | `1m`、`1d`、`1w`、`1M` | 中国交易日历，`Asia/Shanghai` |
| `stock_cn` | `index` | `stock_cn_index_kline` | `1m`、`1d`、`1w`、`1M` | 中国交易日历，`Asia/Shanghai` |
| `stock_cn` | `convertible_bond` | `stock_cn_convertible_bond_kline` | `1m`、`1d` | 中国交易日历，`Asia/Shanghai` |
| `stock_hk` | `equity` | `stock_hk_kline` | `1m`、`1d`、`1w`、`1M` | 港股时段，`Asia/Hong_Kong` |
| `stock_us` | `equity` | `stock_us_kline` | `1m`、`1d`、`1w`、`1M` | 美股时段，交易所/统一时区策略 |

现有 `stock_kline`、`index_kline` 和旧 `crypto_market` 入口不构成兼容约束。Metadata、任务和代码直接迁移到本矩阵定义的 canonical 名称；不保留类型别名、兼容适配层、双写入口或旧资源保护逻辑。新资源仍需具备幂等初始化能力，但初始化目标只包含新契约。

## 2. 文件边界

### 2.1 新增文件

- `packages/marketcalendar/go.mod`：根级公共日历包 module。
- `packages/marketcalendar/calendar.go`：不可变交易日历、前后交易日和区间查询 API。
- `packages/marketcalendar/calendar_test.go`：排序、重复、边界、周末和已知节假日测试。
- `packages/marketcalendar/data/cn_trading_days.json`：从当前 AkShare `calendar.json` 导入并校验后的静态数据。
- `packages/marketcalendar/data/manifest.json`：日历 ID、来源、覆盖边界、版本和 SHA-256。
- `packages/routeprobe/go.mod`：公共解析候选、实际出口线路探测、评分和快照 module，不依赖具体 Provider。
- `packages/routeprobe/models.go`：候选线路、探针请求/结果、评分、健康状态和快照模型。
- `packages/routeprobe/resolver.go`：域名到候选地址的解析适配边界，支持消费现有 MooX DNS route snapshot。
- `packages/routeprobe/prober.go`：协议无关的探针接口、context deadline 和并发探测协调器。
- `packages/routeprobe/http.go`：保留 Host/SNI 的 HTTP/HTTPS 探针，支持 Provider endpoint 和期望状态/响应校验。
- `packages/routeprobe/selector.go`：失败线路降级、延迟/成功率/远端错误惩罚、EWMA/p95 评分、快照 TTL 和有限 fallback；不实现 Provider 限频或全局配额。
- `packages/routeprobe/snapshot.go`：按 `scf_region + provider + transport + host:port` 隔离的线路快照序列化和版本校验。
- `packages/routeprobe/*_test.go`：候选去重、并发探测、Host/SNI、评分、快照过期、fallback 和不支持协议测试。
- `packages/tdx/go.mod`：TDX 协议公共库 module，不引入 Python、Tushare 或 QUANTAXIS 运行时。
- `packages/tdx/models.go`：普通/扩展 TDX 的强类型 K 线、指数 K 线、市场和证券目录模型。
- `packages/tdx/frame.go`：16 字节 TDX 响应帧头、Body 长度、压缩标志和 zlib 解压边界。
- `packages/tdx/transport.go`：`7709` TCP 连接、握手、读写 deadline、连接复用和关闭。
- `packages/tdx/heartbeat.go`：普通 TDX 连接的可选心跳，不作为跨 SCF 调用的常驻机制。
- `packages/tdx/hosts.go`：普通/扩展 TDX 节点配置和 Source/端口兼容性校验；线路选择委托公共 `packages/routeprobe`。
- `packages/tdx/codec/price.go`、`volume.go`、`datetime.go`：TDX 价格、成交量和日期时间编解码。
- `packages/tdx/commands/security_bars.go`、`index_bars.go`、`security_list.go`、`security_quotes.go`：普通 TDX 请求和响应解析。
- `packages/tdx/ext/transport.go`、`markets.go`、`instrument_info.go`、`instrument_bars.go`、`history_bars_range.go`：`7727` 扩展行情和动态市场目录。
- `packages/tdx/testdata/normal-security-bars.bin`、`normal-index-bars.bin`、`extended-instrument-bars.bin`：由 easy_tdx fixture 整理并带来源说明的二进制协议样本。
- `packages/tdx/*_test.go`、`packages/tdx/codec/*_test.go`、`packages/tdx/ext/*_test.go`：帧、编解码、请求字节和响应解析测试。
- `modules/collector/internal/marketdata/provider.go`：Provider、Source 描述和统一错误。
- `modules/collector/internal/marketdata/spec.go`：`KlineSpec`、`InstrumentSpec`、请求边界和时间戳语义。
- `modules/collector/internal/marketdata/kline.go`：`KlineRequest`、`NormalizedKline` 和 K 线校验。
- `modules/collector/internal/marketdata/instrument.go`：统一标的、Provider symbol 和快照契约。
- `modules/collector/internal/marketdata/calendar.go`：Market Calendar Policy 与公共日历包的适配边界。
- `modules/collector/internal/marketdata/provider_test.go`、`kline_test.go`、`instrument_test.go`：契约测试。
- `modules/collector/internal/sources/stockcn/eastmoney/kline.go`、`parser.go`、`symbol.go`：A 股 EM 日线/分钟线和 symbol 转换。
- `modules/collector/internal/sources/stockcn/tdx/kline.go`、`parser.go`、`symbol.go`：A 股 TDX 普通行情适配，复用 `packages/tdx`，不重复实现 TCP 协议。
- `modules/collector/internal/sources/stockcn/sina/kline.go`、`parser.go`、`symbol.go`：A 股新浪日线/分钟线和 JSONP/JS 解码。
- `modules/collector/internal/sources/stockcn/tencent/kline.go`、`parser.go`、`symbol.go`：A 股腾讯日线和字段单位转换。
- `modules/collector/internal/sources/stockhk/eastmoney/kline.go`、`stockhk/eastmoney/parser.go`、`stockhk/sina/kline.go`、`stockhk/sina/parser.go`：港股适配器。
- `modules/collector/internal/sources/stockhk/tdx/kline.go`、`parser.go`、`symbol.go`：港股 TDX 扩展行情适配，使用动态市场 ID。
- `modules/collector/internal/sources/stockus/eastmoney/kline.go`、`stockus/eastmoney/parser.go`、`stockus/sina/kline.go`、`stockus/sina/parser.go`：美股适配器。
- `modules/collector/internal/sources/stockus/tdx/kline.go`、`parser.go`、`symbol.go`：美股 TDX 扩展行情适配，保留盘前/盘后和 regular-session 边界。
- `modules/collector/internal/sources/index/eastmoney/kline.go`、`index/eastmoney/parser.go`、`index/sina/kline.go`、`index/tencent/kline.go`、`index/csindex/kline.go`、`index/sw/kline.go`、`index/tdx/kline.go`、`index/tdx/parser.go`：指数适配器。
- `modules/collector/internal/sources/convertiblebond/eastmoney/kline.go`、`convertiblebond/eastmoney/parser.go`、`convertiblebond/sina/kline.go`：可转债适配器。
- `modules/collector/internal/sources/convertiblebond/tdx/kline.go`、`convertiblebond/tdx/parser.go`：TDX 可转债适配，独立于普通股票路由。
- `modules/collector/internal/markets/stockcn/calendar.go`、`sessions.go`：A 股、指数和可转债交易日/交易时段策略。
- `modules/collector/internal/markets/stockhk/sessions.go`：港股时段策略。
- `modules/collector/internal/markets/stockus/sessions.go`：美股时段策略。
- `modules/collector/internal/marketfetch/pipeline.go`：通用 KlinePipeline 的当前实现；InstrumentPipeline 仍是后续任务。
- `modules/collector/internal/marketfetch/provider_router.go`：Provider 候选链和能力路由。
- `modules/collector/internal/sources/binance/storage_rpc.go`：当前复用的窄 Storage writer 和通用 SCF 鉴权构造；完成 Storage 抽离后再迁移文件边界。
- `modules/collector/internal/marketfetch/route_policy.go`：把公共线路快照接入 Provider、Market、Source、SCF 地域和 Transport。
- `modules/collector/internal/marketfetch/route_policy_test.go`：验证 Binance HTTP 和 TDX TCP 共用路由策略时的隔离键、快照更新和 fallback。
- `modules/collector/internal/serverless/market_data/handler.go`、`handler_test.go`：通用短时 SCF HTTP/Provider Pipeline 入口和本地 fake Storage E2E。
- `modules/collector/cmd/scf/market_data/main.go`：通用市场行情 SCF 构建入口；不接受 `crypto_market`。
- `modules/cli/internal/command/collector.go`：允许显式选择 `crypto_market` 或 `market_data` SCF entrypoint，并复用统一打包校验。
- `modules/collector/config/markets/stock_cn/market.yaml`、`calendar.yaml`、`provider-validation.yaml`：A 股市场配置。
- `modules/collector/config/markets/stock_hk/market.yaml`、`provider-validation.yaml`：港股市场配置。
- `modules/collector/config/markets/stock_us/market.yaml`、`provider-validation.yaml`：美股市场配置。
- `docs/akshare-market-api-catalog.md`：AkShare 接口到 MooX Provider/Market/Dataset 的完整映射表。
- `docs/tdx-go-port.md`：easy_tdx/QUANTAXIS/tdx-go TDX 能力、协议差异、线路优选、字段确认状态和 Go 移植边界。
- `scripts/marketdata/validate-calendar.sh`：静态日历格式、排序、覆盖边界和哈希校验入口。

### 2.2 需要修改的文件

- `go.work`：Task 2 接入 `marketcalendar`，Task 5A 接入 `routeprobe`，Task 5B 接入 `tdx`；每个 module 创建后立即加入 workspace。
- `modules/collector/go.mod`：增加 `packages/marketcalendar`、`packages/routeprobe`、`packages/tdx` 本地依赖。
- `modules/collector/internal/sources/binance/storage_rpc.go`：补充通用市场行情 SCF 所需的环境鉴权 Storage writer 构造。
- `scripts/build.sh`、`scripts/build-collector-scf-package.sh`：增加 `market_data` SCF 编译和打包入口；只有 crypto 包继续执行 Binance 专用凭据渲染。
- `modules/collector/configs/scf/market_data/config.yaml`：通用市场行情 SCF 的最小运行时配置。
- `modules/collector/internal/sources/interface.go:13-113`：从 Binance-oriented `Collector` 直接迁移到 Market/Provider/Source/Frequency 语义，迁移完成后删除旧接口。
- `modules/collector/internal/sources/registry.go:9-129`：注册和查询 `ProviderDescriptor`、`SourceKey`、`MarketID`、`InstrumentType`，拒绝重复 SourceKey 或冲突的支持范围。
- `modules/collector/internal/sources/exchange/types.go:8-59`：直接迁移 `KlineRequest`/`Kline` 到 `marketdata`，删除旧类型和无调用者的转换函数。
- `modules/collector/internal/domain/collect_params.go:20-206`：补充 `market_id`、`instrument_type`、`exchange_id`、`provider_symbol`、`calendar_id`，并明确 Provider/Source 不进入逻辑 Task ID。
- `modules/collector/internal/domain/fetch_batch.go`、`task_instance.go`、`task_rule.go`：让任务和结果携带 Market/Instrument/Provider Attempt 信息，直接按新契约迁移现有 crypto 任务。
- `modules/collector/internal/jobs/registry.go:16-64`、`route.go:11-104`：从硬编码 Binance job route 改为基于 Market Registry 的通用 `kline`/`instrument` 任务路由。
- `modules/collector/internal/marketfetch/executor.go:17-360`：移除对 `sources/binance` 的静态依赖，注入通用 Kline/Instrument Pipeline。
- `modules/collector/internal/marketfetch/write_source.go:1`：保留必要的 Storage RPC 细节，改由通用 Pipeline 传入完整来源字段和稳定 `SourceEventID`；不受错误行号范围约束。
- `modules/collector/internal/sources/binance/kline.go:34-509`、`symbol.go` 及相关测试：实现现有 Binance 到通用契约的适配，保持 crypto 的 `series_tag` 语义。
- `modules/collector/internal/serverless`、`cmd/scf` 和 runtime 装配文件：按 Market manifest 选择 composition root，不复制一份 Binance Executor。
- `modules/collector/config/app.yaml`、`modules/collector/configs/config.yaml` 和 `modules/collector/configs/sources/market/binance.yaml`：加入 Provider host、TDX Source 节点、Market manifest、静态日历、连接超时和批次边界配置；禁止把 API 密钥写入配置文件。
- `examples/setup/default/metadata.yaml`、`examples/setup/default/collector-rules.yaml`：直接切换到新 Market/Dataset canonical 契约，删除不合理旧资源配置，不保留兼容示例。
- `docs/内置市场行情采集架构.md`、`docs/架构总览.md`、`docs/大仓架构.md`、`docs/architecture/scf-short-lived-market-fetch.md`：同步公共日历包、公共线路探测、Binance/TDX 复用、stock_hk、指数/可转债 Dataset、SCF TCP 出口边界，并将“DNS 代理”准确表述为解析候选快照与应用层线路优选。

## 3. Task 1：冻结 AkShare/QUANTAXIS/easy_tdx 接口目录和能力矩阵

**目的：** 把当前 AkShare 源码、QUANTAXIS 调用链和 easy_tdx TDX 参考实现中的能力清单转成可审计的 MooX 映射，避免实现过程中把“有函数名”或“能连上 TCP”误当成“有完整、字段已确认的 OHLCV 能力”。

**Files:**

- Create: `docs/akshare-market-api-catalog.md`
- Create: `docs/tdx-go-port.md`
- Create: `modules/collector/config/markets/stock_cn/provider-validation.yaml`
- Create: `modules/collector/config/markets/stock_hk/provider-validation.yaml`
- Create: `modules/collector/config/markets/stock_us/provider-validation.yaml`

- [ ] **Step 1: 记录接口来源、上游 URL 和响应字段**

为每个接口记录 AkShare 函数、源文件/函数、真实上游、请求参数、返回字段、时间粒度、复权选项、历史范围、是否支持分页、成交量/成交额单位和当前验证状态。至少覆盖：

```text
calendar:
  tool_trade_date_hist_sina
stock_cn:
  stock_zh_a_hist, stock_zh_a_hist_min_em
  stock_zh_a_daily, stock_zh_a_minute
  stock_zh_a_hist_tx
stock_hk:
  stock_hk_hist, stock_hk_hist_min_em, stock_hk_daily
stock_us:
  stock_us_hist, stock_us_hist_min_em, stock_us_daily
index:
  index_zh_a_hist, index_zh_a_hist_min_em
  stock_zh_index_daily, stock_zh_index_daily_tx, stock_zh_index_daily_em
  index_hist_cni, index_hist_sw, index_hist_fund_sw
convertible_bond:
  bond_zh_hs_cov_daily, bond_zh_hs_cov_min
extension:
  futures_*, option_*, fund_*, reits_*, forex_*, spot_*
tdx_reference:
  easy_tdx: TdxClient.get_security_bars, get_index_bars
             ExTdxClient.get_markets, get_instrument_info, get_instrument_bars
  quantaxis: QAFetch/QATdx.py 的股票、指数、债券、期货和分钟线调用链
```

- [ ] **Step 2: 为每个 Source 设置 `enabled`、`shadow` 或 `catalog_only`**

首批只允许完整 OHLCV、时间戳和单位均能验证的 Source 进入 `enabled`。只有实时价/均价而没有开盘价的接口，例如部分 option/REIT 分钟接口，标记为 `catalog_only`，不能伪造成 K 线。

- [ ] **Step 3: 固化 Source 矩阵的字段语义**

配置中明确 `complete_ohlcv`、`has_amount`、`volume_unit`、`amount_unit`、`timestamp_mode`、`supports_range`、`max_bars_per_request`、`supports_adjustment`、`request_timeout` 和 `history_start`。如需记录上游公开限频，仅保留说明性字段，不转化为 MooX 的主动限频配置。不支持 amount 的 Provider 不进入要求成交额的逻辑 Dataset。

- [ ] **Step 4: 校验目录没有遗漏或重复分类**

运行：

```bash
rg -n "^def (tool_trade_date_hist|stock_.*(hist|min|daily|minute)|index_.*hist|bond_.*(daily|min))" /Users/mooyang/Documents/go/src/github.com/akshare/akshare
```

预期：目录中的每个首批函数都能定位到一个上游 Provider、一个 Market、一个 Instrument Type 和一个 Dataset；扩展接口明确标注为 `catalog_only` 或单独实施批次。

- [ ] **Step 5: 冻结 TDX 参考实现和不可移植边界**

阅读 `/Users/mooyang/Documents/go/src/github.com/easy_tdx` 的 `transport`、`codec`、`commands`、`ex` 目录，以及 `/Users/mooyang/Documents/go/src/github.com/QUANTAXIS/QUANTAXIS/QAFetch/QATdx.py` 和 `/Users/mooyang/Documents/go/src/github.com/tdx-go/config/config.go`，把普通 `7709` 和扩展 `7727` 的请求字节、响应字段、分页上限、市场编号、时间戳、成交量/成交额单位、候选线路、测速方法和已知未知字段写入 `docs/tdx-go-port.md`。明确记录：easy_tdx 和 tdx-go 都是参考实现；Go 只移植协议和已验证字段，不移植 DataFrame、CLI、离线缓存、特权 ICMP 或任何 Python 运行时。Tushare 不建立目录项。

- [ ] **Step 6: 先完成完整 TDX Wire Spike，再允许实现协议库**

该步骤是 Task 5A（公共线路探测）和 Task 5B（TDX 协议实现）的前置门禁。针对 `normal_7709`、`ex_classic_7727`、`ex_mac_7727` 分别记录完整请求字节、连接/握手过程、16 字节响应头、压缩原始 Body、解压 Body、解析结果和人工对账结果。必须单独确认：classic extended 是否无普通握手、mac extended 的特殊帧和协议登录、周期编号、市场编号、分页上限、时间标签、成交量/成交额单位。当前仅有“已解压响应体”的离线 fixture 不能作为完整 wire 证据；任何未确认 Source 只能保持 `catalog_only`，不得进入 Go 协议实现或 canonical Dataset。

## 4. Task 2：建立公共静态交易日历库

**Files:**

- Create: `packages/marketcalendar/go.mod`
- Create: `packages/marketcalendar/calendar.go`
- Create: `packages/marketcalendar/calendar_test.go`
- Create: `packages/marketcalendar/data/cn_trading_days.json`
- Create: `packages/marketcalendar/data/manifest.json`
- Create: `scripts/marketdata/validate-calendar.sh`
- Modify: `go.work`
- Modify: `modules/collector/go.mod`

- [ ] **Step 1: 导入并校验 AkShare 静态日历**

从 `/Users/mooyang/Documents/go/src/github.com/akshare/akshare/file_fold/calendar.json` 导入日期，保留 `YYYY-MM-DD` 格式，生成 `cn_trading_days.json`。导入程序必须拒绝空日期、非法日期、重复日期和非严格升序数据，并把 `valid_from`、`valid_through`、更新来源、版本和 SHA-256 写入 `manifest.json`。日历接近 `valid_through` 时必须进入 readiness 告警；超过覆盖范围时必须 fail closed，不能把未知日期当作非交易日。

- [ ] **Step 2: 定义公共 API**

公共包提供不可变的 `CivilDate` 值对象和覆盖状态，至少包含以下方法：

```go
type CivilDate struct{}
type CoverageStatus int
const (
    TradingDay CoverageStatus = iota
    NonTradingDay
    OutOfCoverage
)

type TradingCalendar struct{}

func Load(id string) (TradingCalendar, error)
func (c TradingCalendar) ID() string
func (c TradingCalendar) FirstDate() CivilDate
func (c TradingCalendar) LastDate() CivilDate
func (c TradingCalendar) Status(date CivilDate) (CoverageStatus, error)
func (c TradingCalendar) PrevTradingDay(date CivilDate) (CivilDate, error)
func (c TradingCalendar) NextTradingDay(date CivilDate) (CivilDate, error)
func (c TradingCalendar) TradingDays(start, end CivilDate) ([]CivilDate, error)
```

`Load("cn_stock")` 使用嵌入数据；`CivilDate` 不携带时区和时分秒。未知日历 ID、超出覆盖范围和非法区间返回明确错误；`Status` 只对覆盖范围内日期返回交易日/非交易日，超出范围返回 `OutOfCoverage` 和错误。调用方不能取得内部 map/slice 并修改它。

- [ ] **Step 3: 增加确定性测试**

覆盖 `1992-05-04`、manifest 首尾日期、`valid_through`、周六/周日、已知工作日、越界 Status/Previous/Next、重复导入和返回切片防修改。测试不能调用外网，也不能依赖当前日期。

- [ ] **Step 4: 增加仓库校验入口**

`validate-calendar.sh` 校验 JSON、manifest 覆盖边界、SHA-256 和升序唯一性。运行：

```bash
(cd packages/marketcalendar && go test -count=1 ./...)
bash scripts/marketdata/validate-calendar.sh
```

预期：公共包测试通过，脚本报告日历 ID、首日、末日、总天数和哈希一致。

## 5. Task 3：建立 Market/Provider/Kline 强类型契约

**Files:**

- Create: `modules/collector/internal/marketdata/provider.go`
- Create: `modules/collector/internal/marketdata/spec.go`
- Create: `modules/collector/internal/marketdata/kline.go`
- Create: `modules/collector/internal/marketdata/instrument.go`
- Create: `modules/collector/internal/marketdata/calendar.go`
- Create: `modules/collector/internal/marketdata/provider_test.go`
- Create: `modules/collector/internal/marketdata/kline_test.go`
- Create: `modules/collector/internal/marketdata/instrument_test.go`
- Modify: `modules/collector/internal/sources/interface.go:13-113`
- Modify: `modules/collector/internal/sources/registry.go:9-129`
- Modify: `modules/collector/internal/sources/exchange/types.go:8-59`

- [ ] **Step 1: 定义统一身份字段和 SourceKey**

定义 `MarketID`、`ProviderID`、`SourceID`、`ExchangeID`、`ProductType`、`InstrumentType`、`CalendarID` 和 `Frequency`。`SourceID` 表示 Provider 下的具体行情通道，例如 `normal_7709`、`ex_classic_7727`、`ex_mac_7727` 或 `spot_http`；`SourceKey` 为 `ProviderID + SourceID` 的组合键。`provider_id` 和 `source_id` 表示本次 Attempt 使用的上游通道，不进入逻辑 Task ID/RowKey。

- [ ] **Step 2: 定义 Fetcher 和 Spec**

契约采用如下形状，字段以实际代码命名为准但不得退化为 `any`：

```go
type KlineFetcher interface {
    Descriptor() ProviderDescriptor
    KlineSpec() KlineSpec
    FetchKlines(context.Context, KlineRequest) ([]NormalizedKline, error)
}

type InstrumentFetcher interface {
    Descriptor() ProviderDescriptor
    InstrumentSpec() InstrumentSpec
    FetchInstrumentSnapshot(context.Context, InstrumentRequest) (InstrumentSnapshot, error)
}
```

`ProviderDescriptor` 必须包含 `ProviderID`、`SourceID`、`ProtocolVariant`、Market/Instrument 支持范围、连接协议和目标端口；Registry 以 `SourceKey` 注册，不能只以 Provider ID 注册。`KlineSpec` 声明支持的 Market、Instrument、Exchange、Frequency、完整 OHLCV、amount、单位、时间戳模式、历史范围、分页、连接协议、目标端口和请求超时。TDX Source 还必须分别声明 `normal_7709`、`ex_classic_7727` 或 `ex_mac_7727` 的握手/登录差异、`host:port` 和动态市场要求。上游公开限频只作为目录备注，不生成 MooX 主动限频配置。`InstrumentSpec` 声明全量快照、分页、状态和 symbol 转换能力。

- [ ] **Step 3: 定义 NormalizedKline 校验**

统一字段至少包含 `subject_id`、`provider_id`、`provider_symbol`、`frequency`、`bar_start`、`bar_end`、`open`、`high`、`low`、`close`、`volume`、可选 `amount`、单位、Provider 时间戳、抓取时间和请求 ID。

校验拒绝 NaN/Inf、负 volume/amount、`high/low` 关系非法、重复时间桶、非单调时间和缺失必填 OHLC。禁止把缺失 amount 替换为 `close * volume`。

- [ ] **Step 4: 统一错误分类并删除旧接口**

定义 `ErrTimeout`、`ErrRateLimited`、`ErrRemoteBusy`、`ErrTCP`、`ErrHTTPStatus`、`ErrProtocol`、`ErrNoClosedBar`、`ErrUnsupportedSymbol`、`ErrUnsupportedFrequency`。`ErrRateLimited` 仅用于透传上游返回的 429/频控错误，不代表 MooX 会主动限频。通用 Executor 切换到新契约后，直接删除 Binance-oriented 的旧 `sources.Collector`、旧类型和无调用者的转换函数，不保留兼容适配。

- [ ] **Step 5: 用契约测试锁定边界**

测试错误分类、Spec 不允许的频率、amount 缺失、时间戳模式、symbol 为空、重复 K 线和 Provider Registry 重复注册。运行：

```bash
(cd modules/collector && go test -count=1 ./internal/marketdata ./internal/sources)
```

## 6. Task 4：实现中国交易日历与交易时段策略

**Files:**

- Create: `modules/collector/internal/markets/stockcn/calendar.go`
- Create: `modules/collector/internal/markets/stockcn/sessions.go`
- Create: `modules/collector/internal/markets/stockcn/calendar_test.go`
- Create: `modules/collector/internal/markets/stockhk/sessions.go`
- Create: `modules/collector/internal/markets/stockus/sessions.go`
- Create: `modules/collector/internal/markets/stockhk/sessions_test.go`
- Create: `modules/collector/internal/markets/stockus/sessions_test.go`

- [ ] **Step 1: 将 `cn_stock` 适配为 Market Calendar Policy**

A 股、指数和可转债共用公共日历，但各自声明交易时段和午休规则。中国时区的来源时间先解析为 `Asia/Shanghai`，再转 UTC 存入 Storage。

- [ ] **Step 2: 实现分钟预期桶和闭合判断**

生成 `09:30-11:30`、`13:00-15:00` 的分钟桶；午休和非交易日不计为缺口。Bar `09:31` 表示 `09:30-09:31`，只在 `bar_end + settle_delay` 之后进入写入流程。

- [ ] **Step 3: 分离港股/美股时段策略**

港股和美股不能查询 `cn_stock` 日历。第一阶段只执行 Provider 返回数据的时间规范化和时段合法性检查；在对应假日表导入前，不对其做严格的交易日缺口推断。

- [ ] **Step 4: 测试跨时区和午休边界**

固定测试 09:29、09:30、11:30、13:00、15:00、周末和夏令时切换样例，确认输入时区变化不会改变逻辑 UTC 桶。

## 7. Task 5：实现 A 股 Provider

**Files:**

- Create: `modules/collector/internal/sources/stockcn/eastmoney/kline.go`
- Create: `modules/collector/internal/sources/stockcn/eastmoney/parser.go`
- Create: `modules/collector/internal/sources/stockcn/eastmoney/symbol.go`
- Create: `modules/collector/internal/sources/stockcn/eastmoney/kline_test.go`
- Create: `modules/collector/internal/sources/stockcn/sina/kline.go`
- Create: `modules/collector/internal/sources/stockcn/sina/parser.go`
- Create: `modules/collector/internal/sources/stockcn/sina/symbol.go`
- Create: `modules/collector/internal/sources/stockcn/sina/kline_test.go`
- Create: `modules/collector/internal/sources/stockcn/tencent/kline.go`
- Create: `modules/collector/internal/sources/stockcn/tencent/parser.go`
- Create: `modules/collector/internal/sources/stockcn/tencent/symbol.go`
- Create: `modules/collector/internal/sources/stockcn/tencent/kline_test.go`

- [ ] **Step 1: 实现东方财富日线/分钟线**

复用现有 `internal/httpclient` 的 timeout、DNS route 和 HTTP 错误处理。日线使用 `kline/get` 的 `klt=101/102/103`；分钟线按 `trends2/get` 或 `kline/get` 的实际能力选择，严格把 `1/5/15/30/60` 映射到 MooX frequency。

实现 A 股代码到 `secid` 的严格转换，覆盖沪、深、北交所；未知市场前缀直接返回 `ErrUnsupportedSymbol`，不能通过首位数字猜测。

- [ ] **Step 2: 实现新浪日线/分钟线**

解析新浪 JSONP/JS 编码响应，覆盖 `stock_zh_a_daily` 和 `stock_zh_a_minute` 的字段映射。复权参数只作为显式请求选项存在；逻辑 Canonical Dataset 默认拒绝复权结果，复权数据不进入不复权 K 线。

- [ ] **Step 3: 实现腾讯 A 股日线**

解析 `newfqkline/get` 返回的 `day/qfqday/hfqday`，实现源码中成交量、换手率和成交额单位转换。若某响应没有完整 amount，则 Spec 标记 `has_amount=false`，该 Source 不能进入要求 amount 的候选链。

- [ ] **Step 4: 用 HTTP fixture 测试正常和异常响应**

fixture 至少覆盖正常日线、正常分钟线、空数据、未闭合 bar、字段不足、错误 JSON、HTTP 429/500、重复 bar、乱序 bar、非法 OHLC 和单位转换。测试必须验证生成的 `NormalizedKline`，而不是只验证原始 DataFrame 等价物。

## 8. TDX Go 公共协议库和 Provider（执行批次 5B）

**目的：** 在 Task 5A 公共线路探测契约和前置 Wire Spike 通过后，将 easy_tdx 中已经确认的 TDX TCP 请求逻辑封装为可被 Collector 和未来其他模块复用的 Go 公共库；SCF 只负责短时执行和公网出口，不把连接管理或协议解析散落到各个 Market Handler。

**Files:**

- Create: `packages/tdx/go.mod`
- Create: `packages/tdx/models.go`
- Create: `packages/tdx/frame.go`
- Create: `packages/tdx/transport.go`
- Create: `packages/tdx/heartbeat.go`
- Create: `packages/tdx/hosts.go`
- Create: `packages/tdx/codec/price.go`
- Create: `packages/tdx/codec/volume.go`
- Create: `packages/tdx/codec/datetime.go`
- Create: `packages/tdx/commands/security_bars.go`
- Create: `packages/tdx/commands/index_bars.go`
- Create: `packages/tdx/commands/security_list.go`
- Create: `packages/tdx/commands/security_quotes.go`
- Create: `packages/tdx/ext/transport.go`
- Create: `packages/tdx/ext/markets.go`
- Create: `packages/tdx/ext/instrument_info.go`
- Create: `packages/tdx/ext/instrument_bars.go`
- Create: `packages/tdx/ext/history_bars_range.go`
- Create: `packages/tdx/NOTICE`
- Create: `packages/tdx/testdata/normal-security-bars.bin`
- Create: `packages/tdx/testdata/normal-index-bars.bin`
- Create: `packages/tdx/testdata/extended-instrument-bars.bin`
- Create: `packages/tdx/*_test.go`、`packages/tdx/codec/*_test.go`、`packages/tdx/ext/*_test.go`
- Create: `modules/collector/internal/sources/stockcn/tdx/kline.go`、`parser.go`、`symbol.go`
- Create: `modules/collector/internal/sources/index/tdx/kline.go`、`parser.go`、`kline_test.go`
- Create: `modules/collector/internal/sources/convertiblebond/tdx/kline.go`、`parser.go`、`kline_test.go`
- Create: `modules/collector/internal/sources/stockhk/tdx/kline.go`、`parser.go`、`symbol.go`
- Create: `modules/collector/internal/sources/stockus/tdx/kline.go`、`parser.go`、`symbol.go`
- Modify: `go.work`
- Modify: `modules/collector/go.mod`

- [ ] **Step 1: 固化协议 fixture 和许可边界**

从 `/Users/mooyang/Documents/go/src/github.com/easy_tdx/tests/unit/test_commands_offline.py` 选取普通股票、指数和分钟线样本，整理为 Go 可读的二进制 fixture，并在 `docs/tdx-go-port.md` 记录 fixture 来源、截取方式和字段确认状态。保留 easy_tdx、pytdx、xmtdx 的 MIT 许可和归属信息；不得将 Python 包、DataFrame 或离线缓存复制进 Go module。

- [ ] **Step 2: 实现普通 TDX `7709` 传输层**

使用 `net.Dialer`、`io.ReadFull` 和 context deadline 实现连接、发送、读取 16 字节响应帧、按压缩标记执行 zlib 解压和 Body 边界校验。普通连接建立后执行 easy_tdx 中的握手命令；一次 SCF 调用内复用同一 TCP 连接请求多个标的和分页，函数退出前关闭连接。心跳只允许用于连接仍在运行的单次任务，不得把 SCF warm instance 当作跨次调用的常驻连接。

- [ ] **Step 3: 实现 `7709` K 线和目录命令**

按 wire fixture 锁定 `GetSecurityBars`、指数 K 线、证券列表和快照请求字节。实现 1/3/5/15/30/60 分钟、日/周/月/季/年原生分类的解码，保留普通股票与指数额外字段的差异；分页严格遵守服务端单页上限。价格、成交量、成交额和时间字段必须先经过独立 codec，再转换为 `NormalizedKline`，不能用字符串切割或猜测单位。

- [ ] **Step 4: 实现扩展 TDX `7727` 传输层和动态目录**

扩展连接单独实现 `7727` 协议，不复用普通行情握手；先实现市场列表、证券数量、证券信息和扩展 K 线请求。港股/美股路由必须以服务端动态市场信息为准，`KNOWN_EX_MARKETS` 只能作为测试辅助，不能作为生产事实来源。扩展 K 线单页上限、日期范围请求和 `bar_time` 语义写入 `KlineSpec`。

- [ ] **Step 5: 隔离未确认扩展字段**

扩展 K 线中 `position`、`trade`、`settlement` 等字段只有在 fixture 和小规模实时探测均能确认含义、单位和稳定性后，才允许进入统一 Dataset。未确认字段保留在 TDX source-specific 扩展中，不映射为 canonical `amount` 或 `volume`；不能因为字段位置相同就直接复用普通 K 线语义。

- [ ] **Step 6: 实现 TDX 专用探针和节点兼容性校验**

TDX 公共库只负责 TDX 传输、Source/端口兼容性和协议探针：`normal_7709` 从实际 SCF 地域完成 TCP connect、`7709` 握手和最小合法请求，`ex_classic_7727` 和 `ex_mac_7727` 分别完成已确认的 `7727` 首包和最小合法请求。候选线路先校验 IP/域名、端口范围和 Source 兼容性，再按 `host:port + source` 去重；配置中的备用端口只有通过对应协议探测后才能启用，不能因为端口字段存在就视为可用。对 tdx-go `stock_ip.json` 中重复 endpoint、`ort` 拼写导致的零端口和缺少名称等问题，Go 配置加载必须显式报错或规范化，不能静默产生错误。具体的候选排序、失败线路降级、快照和 fallback 统一由 Task 5A 的公共 `packages/routeprobe` 完成，避免 Binance HTTP 和 TDX TCP 各自维护一套线路算法。

`packages/tdx` 通过 `routeprobe.Prober` 暴露 TDX 专用探针，不自行保存跨 Provider 的线路排名。公共线路快照按 `scf_region + provider + source + transport + host:port` 隔离；本项目不创建 Provider budget、全局配额或主动限频层。TDX 只声明连接超时、单次函数 deadline、单次请求页大小和有限重试等执行边界；上游返回的 429/远端忙作为错误结果和观测字段处理，不触发 MooX 主动限频。随机公网出口只作为 SCF 运行环境事实，不作为规避频控的承诺。

- [ ] **Step 6A: 固化线路快照和运行时降级**

控制面保存每个 SCF 地域、Provider、Transport 和 Source 的候选顺序、评分、采样时间、失败计数和配置版本；Timer 环境只携带经过校验的候选线路快照，不携带动态连接状态。运行时优先使用快照首线路，连接或协议失败时按快照顺序尝试有限的后备线路，并将失败原因写入结果和指标。当前函数内的 fallback 不得修改全局快照；快照更新由 Collector 的线路探测和配置协调完成。该机制只负责选择可用线路，不负责请求限频或主动冷却。

- [ ] **Step 7: 接入 TDX Market Provider 适配器**

`stockcn/tdx` 先接入沪深北普通股票、指数和可转债；`stockhk/tdx`、`stockus/tdx` 只在动态目录、交易时区、regular-session、成交量/成交额字段和实际出口验证通过后启用。所有适配器复用 `packages/tdx`，只负责 symbol、Market、Instrument、Calendar 和 `NormalizedKline` 映射，不直接写 Storage。

- [ ] **Step 8: 运行离线协议测试**

运行：

```bash
(cd packages/tdx && go test -count=1 ./...)
(cd modules/collector && go test -count=1 ./internal/sources/... ./internal/marketdata)
```

预期：请求字节、帧边界、压缩/解压、价格/成交量/时间编解码、普通/指数/扩展响应解析和字段拒绝测试全部通过；没有网络依赖。

## 8A. 公共线路探测与优选模块（执行批次 5A，必须先于 TDX）

**目的：** 把当前 MooX 的解析候选快照、实际出口探测、线路评分和有限 fallback 抽象成 Provider 无关的公共能力，让加密货币 HTTP 与 TDX TCP 共用选择框架，同时保留各协议独立的可达性验证。该任务必须先于 Task 5B 执行；Task 5B 只能引用本任务已经确定的 `SourceKey` 和 `routeprobe.Prober`。

**Files:**

- Create: `packages/routeprobe/go.mod`
- Create: `packages/routeprobe/models.go`
- Create: `packages/routeprobe/resolver.go`
- Create: `packages/routeprobe/prober.go`
- Create: `packages/routeprobe/http.go`
- Create: `packages/routeprobe/selector.go`
- Create: `packages/routeprobe/snapshot.go`
- Create: `packages/routeprobe/*_test.go`
- Create: `modules/collector/internal/marketfetch/route_policy.go`
- Create: `modules/collector/internal/marketfetch/route_policy_test.go`
- Modify: `go.work`
- Modify: `modules/collector/go.mod`
- Modify: `modules/collector/internal/marketfetch/egress_probe.go`
- Modify: `modules/collector/internal/dnsresolver/coordinator.go`
- Modify: `modules/collector/internal/dnscache/cache.go`
- Modify: `modules/collector/internal/httpclient/client.go`
- Modify: `modules/collector/internal/sources/binance/client/spot.go`
- Modify: `modules/collector/internal/sources/binance/client/swap.go`
- Modify: `modules/collector/internal/sources/interface.go`

- [ ] **Step 1: 定义与 Provider 无关的路由契约**

定义候选 endpoint、Provider/Transport/Source、SCF region、egress scope、探测结果、健康状态、评分和快照版本。公共模块只处理候选、探测编排、排序、TTL、失败线路降级和有限 fallback，不包含 Binance symbol、TDX command 或 Dataset 逻辑；域名解析仍是候选来源，不把公共模块实现成监听 `53` 端口的 DNS Server，也不实现 Provider 限频或全局配额。

- [ ] **Step 2: 接入协议特定探针**

HTTP/HTTPS 探针复用现有 Host/SNI 保留逻辑，并使用 Provider endpoint 的只读 ping 或最小合法请求验证 HTTP 状态和响应语义；TDX 探针由 `packages/tdx` 提供，normal/extended 分别验证 `7709/7727` 的对应协议。只完成 TCP connect 或只测 ICMP 的结果不能标记为可用线路。

- [ ] **Step 3: 实现并发探测和稳定评分**

借鉴 tdx-go 的并发探测、失败淘汰和延迟排序，但采用多次实际协议探测，综合连接延迟、首个有效响应延迟、成功率和远端错误，使用 EWMA/p95 与失败惩罚。探测只在显式的线路探测任务或 Invoke 中执行，不在每个标的或每次 K 线请求中重新测速；不依赖 Provider 配额或主动限频层。

- [ ] **Step 4: 复用现有 MooX DNS route snapshot**

保留 `dnsresolver`/`dnscache` 作为“域名到候选 IP”的解析与快照层；现有 Trade Resolver 的 `TcpConnectLatencyMs` 只作为初始排序 hint 或无实际 SCF 探针时的 fallback，不冒充 SCF 线路实测。`routeprobe` 在 Timer/Invoke 的实际 SCF 出口执行协议探测后，生成按 `scf_region + provider + transport + host:port` 隔离的快照。

- [ ] **Step 5: 让加密货币和 TDX 共用路由策略**

Binance 继续保留域名 Host/SNI 和 hostname fallback，但候选顺序、健康状态和快照由公共策略提供；TDX 使用同一选择器，替换为自身的 `normal_7709`、`ex_classic_7727` 和 `ex_mac_7727` 协议探针。HTTP 与 TDX 的 route key、端口、Source 和协议结果必须隔离，不能因为 Binance HTTP 可达就推断 TDX TCP 可达，反之亦然。

- [ ] **Step 6: 补齐离线测试并删除旧路由入口**

使用 httptest/fake dialer/fake prober 验证 Host/SNI、候选去重、探测并发边界、失败淘汰、稳定 tie-break、EWMA/p95 排序、快照 TTL、地域隔离、有限 fallback 和域名回退；将现有 `DNSRoutes`/`DNSIPs` 调用方直接迁移到新 Source/routeprobe 契约，随后删除旧路由入口，不保留兼容适配。

## 9. Task 6：实现港股和美股 Provider

**Files:**

- Create: `modules/collector/internal/sources/stockhk/eastmoney/kline.go`
- Create: `modules/collector/internal/sources/stockhk/eastmoney/parser.go`
- Create: `modules/collector/internal/sources/stockhk/eastmoney/kline_test.go`
- Create: `modules/collector/internal/sources/stockhk/sina/kline.go`
- Create: `modules/collector/internal/sources/stockhk/sina/parser.go`
- Create: `modules/collector/internal/sources/stockhk/sina/kline_test.go`
- Create: `modules/collector/internal/sources/stockus/eastmoney/kline.go`
- Create: `modules/collector/internal/sources/stockus/eastmoney/parser.go`
- Create: `modules/collector/internal/sources/stockus/eastmoney/kline_test.go`
- Create: `modules/collector/internal/sources/stockus/sina/kline.go`
- Create: `modules/collector/internal/sources/stockus/sina/parser.go`
- Create: `modules/collector/internal/sources/stockus/sina/kline_test.go`

- [ ] **Step 1: 固化港股 symbol 语义**

东方财富使用港股 `secid=116.<code>` 的协议；新浪使用其港股 symbol 规则。代码必须保留原始 Provider symbol 和统一 `subject_id`，并测试前导零、5 位/多位代码和特殊证券代码。

- [ ] **Step 2: 固化美股 symbol 和时间戳语义**

东方财富和新浪的美股代码、交易时区、盘前盘后字段必须分别映射；不把盘前/盘后数据默认并入 regular-session K 线。无法证明 consolidated source 的接口标记为单一 Provider Source，不宣称全市场合并行情。

- [ ] **Step 3: 完成日线和分钟线标准化**

按 Provider Spec 宣布支持的频率实现 `1d/1w/1M` 以及 `1/5/15/30/60m`。对于只返回近期分钟窗口的接口，将历史范围和最大 bars 写入 Spec，不能让 Backfill 误以为可无限回溯。

- [ ] **Step 4: 测试跨市场隔离**

测试必须证明 `stock_hk` 不会命中 `stock_cn` Provider 或日历，`stock_us` 不会使用中国交易时段，错误的 Market/Instrument/Provider 组合在 Registry 层失败。

## 10. Task 7：实现指数和可转债分类

**Files:**

- Create: `modules/collector/internal/sources/index/eastmoney/kline.go`
- Create: `modules/collector/internal/sources/index/eastmoney/parser.go`
- Create: `modules/collector/internal/sources/index/eastmoney/kline_test.go`
- Create: `modules/collector/internal/sources/index/sina/kline.go`
- Create: `modules/collector/internal/sources/index/sina/kline_test.go`
- Create: `modules/collector/internal/sources/index/tencent/kline.go`
- Create: `modules/collector/internal/sources/index/tencent/kline_test.go`
- Create: `modules/collector/internal/sources/index/csindex/kline.go`
- Create: `modules/collector/internal/sources/index/csindex/kline_test.go`
- Create: `modules/collector/internal/sources/index/sw/kline.go`
- Create: `modules/collector/internal/sources/index/sw/kline_test.go`
- Create: `modules/collector/internal/sources/convertiblebond/eastmoney/kline.go`
- Create: `modules/collector/internal/sources/convertiblebond/eastmoney/parser.go`
- Create: `modules/collector/internal/sources/convertiblebond/eastmoney/kline_test.go`
- Create: `modules/collector/internal/sources/convertiblebond/sina/kline.go`
- Create: `modules/collector/internal/sources/convertiblebond/sina/kline_test.go`
- Create: `modules/collector/internal/markets/stockcn/instrument_policy.go`
- Modify: `modules/collector/internal/marketdata/kline_test.go`

- [ ] **Step 1: 实现指数日线/分钟线**

接入东方财富、新浪、腾讯、中证和申万接口，保留 Provider 原始代码、指数名称和来源字段。对只有收盘价或没有完整 OHLCV 的 CSIndex 变体标记 `catalog_only`，不能直接进入 KlineFetcher。

- [ ] **Step 2: 实现可转债日线/分钟线**

接入 `bond_zh_hs_cov_daily` 和 `bond_zh_hs_cov_min` 的完整 OHLCV 路径，定义可转债独立 Dataset 字段契约。转换债券代码时保留交易所前缀，禁止与普通股票 Subject 混淆。

- [ ] **Step 3: 统一 CN Calendar Policy**

指数和可转债引用 `stockcn` 公共日历，但通过各自 `InstrumentType` 和 Dataset 路由；它们的停牌、成交额和成交量质量不能套用普通股票的默认值。

- [ ] **Step 4: 测试类型隔离和字段契约**

覆盖指数无 volume、可转债无 amount、债券代码前缀错误、普通股票误路由到债券 Fetcher、复权数据误进入 canonical Dataset 等失败场景。

## 11. Task 8：把现有 Binance 和新 Provider 接入通用 Collector

**Files:**

- Modify: `modules/collector/internal/marketfetch/pipeline.go`（当前通用 KlinePipeline；旧 `kline_pipeline.go` 不创建）
- Create: `modules/collector/internal/marketfetch/instrument_pipeline.go`
- Modify: `modules/collector/internal/marketfetch/provider_router.go`（当前已落地候选链和 fallback 语义）
- Modify: `modules/collector/internal/marketfetch/route_policy.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `modules/collector/internal/marketfetch/executor.go:17-360`
- Modify: `modules/collector/internal/marketfetch/write_source.go:1`
- Modify: `modules/collector/internal/sources/binance/kline.go:34-509`
- Modify: `modules/collector/internal/sources/binance/symbol.go`
- Modify: `modules/collector/internal/jobs/registry.go:16-64`
- Modify: `modules/collector/internal/jobs/route.go:11-104`
- Modify: `modules/collector/internal/serverless/crypto_market/handler.go`
- Modify: `modules/collector/internal/serverless/crypto_market/handler_test.go`
- Modify: `modules/collector/internal/marketfetch/egress_probe.go`
- Create: `modules/collector/internal/serverless/market_data/handler.go`
- Create: `modules/collector/internal/serverless/market_data/handler_test.go`
- Create: `modules/collector/cmd/scf/market_data/main.go`

- [ ] **Step 1: 建立 Provider Registry 和 Market Descriptor 装配**

启动时注册 Binance、TDX 的 `normal_7709`、`ex_classic_7727`、`ex_mac_7727`、EastMoney、Sina、Tencent、CSIndex 和 SW 的 descriptor；每个 descriptor 明确 Market、Instrument、Source、ProtocolVariant、目标端口和 Spec。Registry 以 `SourceKey{ProviderID, SourceID}` 注册，注册失败必须使启动失败，不能静默覆盖同一 SourceKey。三个 TDX Source 必须使用明确隔离的 transport composition root，不能仅靠配置切换后复用错误握手。

- [ ] **Step 2: 迁移 Binance 到同一 KlineFetcher**

保持 crypto 的 24x7 交易时段和 `venue:binance` `series_tag`；将请求/结果类型直接迁移到新契约并删除 Binance-oriented 旁路。Binance HTTP 候选线路改由公共 `route_policy`/`routeprobe` 提供，继续保留 Host/SNI、解析 route snapshot 和 hostname fallback。现有 Binance 单元测试必须先通过，再接入股票 Provider。

- [ ] **Step 3: 实现通用 Provider Router**

Router 按 `market_id + instrument_type + frequency + exchange_id + source` 查询候选链。主 Provider 遇到 timeout、429、远端忙、协议错误、空响应或无合法闭合 bar 时，最多尝试配置的下一个候选；本地参数错误、context 取消和 deadline 不足不得 fallback。TDX 的三个 Source 之间不得因一次失败自动互换，除非两个 Source 的字段和时间语义在 manifest 中明确相同。

- [ ] **Step 4: 实现通用 KlinePipeline**

流水线固定为：读取任务和 Subject 映射、选择 Calendar/Session、请求 Provider、标准化、完整性校验、过滤未闭合 bar、生成来源字段、构造 RowKey、批量幂等写 Storage、返回逐标的结果。Provider 不得直接调用 `UpsertFields`。

- [ ] **Step 5: 迁移 Executor 和 SCF Handler**

让 `marketfetch.Executor` 依赖 Pipeline 接口而不是 `sources/binance`，让 SCF 根据 `market_id + provider + source` 选择 composition root。HTTP Provider 和 TDX Provider 都从公共路由策略读取按地域/Provider/Transport/Source 隔离的快照；TDX Timer 运行时在单次函数内建立并复用 TCP 连接，按函数 deadline 完成请求、聚合和一次 Storage 写入后关闭连接；不得依赖 warm instance，也不得在 SCF 内无限重试。直接迁移并重建必要的 batch、Storage reserve、SourceEventID、结果上报和 Realtime/Catchup 语义；批次内每个标的必须使用独立且稳定的 `SourceEventID`，不能复用批次 ID 导致 Storage 去重丢行。不保留旧 Executor 兼容旁路。

- [ ] **Step 6: 删除静态 Binance job route 依赖**

Job Definition 保留通用 `kline`、`instrument` 数据类型；Provider/Market/Instrument/Source 支持范围来自 Registry 和 manifest。验证新 crypto、股票、指数和可转债规则均能规划并执行，不复制多套 Executor；旧 Binance-oriented job route 在迁移后删除。

## 12. Task 9：Metadata、配置和任务初始化

**Files:**

- Create: `modules/collector/config/markets/stock_cn/market.yaml`
- Create: `modules/collector/config/markets/stock_cn/calendar.yaml`
- Create: `modules/collector/config/markets/stock_hk/market.yaml`
- Create: `modules/collector/config/markets/stock_us/market.yaml`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/collector/configs/config.yaml`
- Modify: `examples/setup/default/metadata.yaml`
- Modify: `examples/setup/default/collector-rules.yaml`
- Modify: `modules/collector/cmd/cli/init_schema.go`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/cli/internal/command/metadata_spaces.go`
- Modify: `modules/cli/internal/command/setup_init.go`

- [ ] **Step 1: 为每个 Market 编写 manifest**

manifest 必须明确 Market ID、Instrument Type、Calendar ID、时区、Dataset ID、支持频率、Provider/Source 候选链、Spec 引用、HistoryPolicy、ProtocolVariant 和 `register_metadata`。TDX manifest 还必须声明 `normal_7709`、`ex_classic_7727` 或 `ex_mac_7727`、目标端口、节点池、单次页大小、连接超时和函数 deadline。Provider/Source 不由用户规则自由输入；规则引用 Market/Source，路由由内部 manifest 决定。

- [ ] **Step 2: 编写 Dataset/Field 契约**

Canonical K 线字段至少包含 `open`、`high`、`low`、`close`、`volume`、可选 `amount`、`trade_date`、`close_time`、`volume_unit`、`amount_unit`、`source_provider`、`provider_symbol`、`provider_timestamp` 和 `fetched_at`。所有股票类 canonical Dataset 默认使用不复权数据。

- [ ] **Step 3: 删除旧资源并初始化新契约**

直接删除不再使用的旧 Dataset、旧 View、旧规则和旧配置入口，再由 `moox-cli init` 按新 manifest 创建 canonical Space/Dataset/Field/Column/Rule。新资源初始化仍需幂等；契约冲突直接失败并输出差异，不提供旧名称兼容或双写。

- [ ] **Step 4: 增加最小规则示例**

示例至少包含 A 股 1m、港股 1d、美股 1d、中国指数 1d 和可转债 1d；每条规则包含 Market、Instrument、symbol source、target Dataset 和 frequency，不能把 Provider-specific URL 或 token 放进规则 JSON。

## 13. Task 10：Storage 行契约、幂等和来源质量

**Files:**

- Modify: `modules/collector/internal/marketfetch/storage.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go:132-318`
- Modify: `modules/collector/schema/collector.sql`
- Modify: `examples/setup/default/metadata.yaml`
- Modify: `modules/collector/internal/marketfetch/executor_test.go`
- Modify: `modules/collector/internal/marketfetch/write_source_test.go`
- Modify: `modules/collector/internal/sources/binance/kline_test.go`

- [ ] **Step 1: 固化逻辑 RowKey**

股票、指数和可转债的 canonical RowKey 使用 `subject_id + freq + data_time + series_tag`；第一阶段股票类 `series_tag` 为空，Provider 只写来源字段。Crypto 保留现有明确 venue tag。

- [ ] **Step 2: 固化单位字段和质量状态**

写入前把来源单位转换为 Dataset 契约单位，并同时写 `volume_unit`/`amount_unit`。质量状态至少区分 `primary`、`fallback`、`catalog_only` 拒绝和 `unavailable`；不合法或不完整行不写 Storage。

- [ ] **Step 3: 验证整行幂等**

同一 Subject、频率、时间桶在主 Provider 和 fallback Provider 之间只能形成一行；重试使用相同 RowKey 和确定 SourceEventID。不能以 Provider ID 进入 RowKey 来规避重复。

- [ ] **Step 4: 测试写入失败恢复**

使用 Storage fake 验证批量写入失败时结果标记、重试时重复 Upsert、完成标记顺序和来源字段与 OHLCV 同批写入。通过测试证明 Provider 不持有 Storage 引用。

## 14. Task 11：运行时边界、线路观测和安全门禁

**Files:**

- Modify: `modules/collector/internal/marketfetch/metrics.go`
- Modify: `modules/collector/internal/marketfetch/executor.go`
- Modify: `modules/collector/internal/marketfetch/route_policy.go`
- Modify: `modules/collector/internal/marketfetch/egress_probe.go`
- Modify: `modules/collector/internal/dnsresolver/coordinator.go`
- Modify: `modules/collector/internal/dnscache/cache.go`
- Modify: `modules/collector/internal/marketfetch/reconciler.go`
- Modify: `modules/collector/internal/marketfetch/assignment.go`
- Modify: `modules/collector/internal/health/server.go`
- Modify: `modules/collector/internal/health/state.go`
- Modify: `modules/collector/config/markets/stock_cn/provider-validation.yaml`
- Modify: `modules/collector/config/markets/stock_hk/provider-validation.yaml`
- Modify: `modules/collector/config/markets/stock_us/provider-validation.yaml`
- Modify: `docs/architecture/scf-short-lived-market-fetch.md`

- [ ] **Step 1: 删除主动限频和全局配额层**

不创建 `provider_budget.go`，不实现 Provider requests/sec、burst、全局配额、主动错峰或频控冷却。Source Spec 只声明请求页大小、连接超时、函数 deadline 和有限重试上限；上游返回的 429/远端忙只作为错误结果和观测字段处理。不得把 SCF 随机出口数量转换成 MooX 的请求预算。

- [ ] **Step 1A: 固化 SCF 非固定公网出口策略**

TDX Timer 默认使用 SCF 公网访问且 `fixed_public_ip=false`；`scf_public_pool` 只表示实际出口策略，不承诺每个函数拥有独立 IP。固定公网 IP不是本阶段必需能力。若 SCF 绑定 VPC，必须同时验证公网访问或 NAT 出口，否则不能访问公网 TDX 节点。Collector 记录观测到的出口 IP 仅用于诊断，不得写入 Task ID、RowKey 或业务身份。

- [ ] **Step 1B: 维护按地域、Provider、Source 和 Transport 隔离的最优线路快照**

线路探测从实际 SCF 地域执行，不能使用 Collector 本机 ICMP 延迟作为生产排序依据。Collector 通过公共 `routeprobe` 为每个 `scf_region + provider + source + transport + host:port` 保存候选线路顺序和版本；Timer 只读取快照并在当前调用内有限 fallback。HTTP Provider 使用 Host/SNI 保留的 HTTP 探针，TDX 使用 `7709/7727` 协议探针。使用协议成功率、p95 首个有效响应延迟和远端错误惩罚更新排序；若没有有效候选，Source 进入 `unavailable`，不得静默使用未经探测的线路。现有 Trade DNS 延迟只能作为 fallback hint，不能替代实际 SCF 探针。该模块只选择线路，不主动控制请求频率。

- [ ] **Step 2: 增加可观测字段**

CLS/Prometheus 至少记录 `market_id`、`instrument_type`、`provider_id`、`source_id`、`provider_symbol`、`frequency`、`source_kind`、`transport`、`remote_host`、`remote_port`、`scf_region`、`egress_scope`、观测到的 `egress_ip`、`connection_attempt`、`rows`、`unit`、`fallback_rank`、`error_kind`、`history_window` 和 `calendar_id`。日志不得包含密钥或完整请求头；出口 IP 只作为诊断标签，不作为稳定业务主键。

- [ ] **Step 3: 增加 readiness 门禁**

Provider/Market/Source 只有在 registry、manifest、calendar、Storage Dataset/Field 契约和 fixture contract tests 全部通过后才可标记 enabled。HTTP Provider 必须通过实际 SCF 出口的 Host/SNI/响应语义探针；TDX 还必须完成 `7709/7727` TCP 连接、对应握手/无握手分支、节点切换和有限 fallback 验证。实际出口样本只用于证明连通性和线路观测，不证明绕过上游限流。实时环境仍需独立完成 egress、Provider 连通性和 Storage read-back；本地测试通过不等于云端发布完成。

- [ ] **Step 4: 验证 SCF 包不携带 Python 行情运行时**

构建检查只允许 Go Collector 和已批准的运行时依赖进入 SCF 包；AkShare 源码路径只存在于开发目录和文档引用，不复制凭据、Python site-packages 或未审计响应缓存。

## 15. Task 12：扩展接口目录登记（暂不实现）

**Files:**

- Modify: `docs/akshare-market-api-catalog.md`
- Modify: `modules/collector/config/markets/stock_cn/market.yaml`
- Modify: `modules/collector/config/markets/stock_hk/market.yaml`
- Modify: `modules/collector/config/markets/stock_us/market.yaml`
- Modify: `examples/setup/default/metadata.yaml`

本任务只登记目录和能力，不创建期货、期权、基金、REITs、外汇或黄金的实现文件；后续每类接口必须另立子计划。

- [ ] **Step 1: 先完成能力登记**

将 `futures_zh_minute_sina`、`futures_hist_em`、交易所官方日行情、期权日/分钟、ETF/LOF、REITs、外汇和黄金接口按 `instrument_type` 分类，并记录是否为真正 OHLC、是否指定日期返回全合约、是否仅返回当前日或近期窗口。

- [ ] **Step 2: 记录未来 Dataset 边界**

期货、期权、基金、REITs、外汇和现货不得复用股票 Dataset。目录中只记录未来需要独立定义的 Subject、频率、单位、成交额可选性和交易时段，不创建实现文件或启用 Dataset。

- [ ] **Step 3: 标记后续实施前置条件**

官方交易所“指定日期全合约”接口可作为未来批量日行情 Fetcher，但不能假装成单标的连续 Kline。只有返回完整 OHLCV、稳定时间字段和可处理错误的接口才允许后续子计划进入 enabled；当前全部保持 `catalog_only`。

## 16. 验证顺序与完成标准

- [ ] **Step 1: 公共库验证**

```bash
(cd packages/marketcalendar && go test -count=1 ./...)
(cd packages/routeprobe && go test -count=1 ./...)
(cd packages/tdx && go test -count=1 ./...)
bash scripts/marketdata/validate-calendar.sh
```

预期：静态日历包、公共线路探测包、TDX 离线协议包和数据校验均通过，未调用外网。

- [ ] **Step 2: Collector 单元和契约测试**

```bash
(cd modules/collector && go test -count=1 ./internal/marketdata ./internal/markets/... ./internal/sources/... ./internal/marketfetch ./internal/jobs/...)
```

预期：Provider fixture、Calendar/Session、Registry、TDX 线路排序与 fallback、Pipeline、Storage fake 和现有 Binance 测试全部通过。

- [ ] **Step 3: Collector race/build 验证**

```bash
(cd modules/collector && go test -race -count=1 ./...)
(cd modules/collector && go build ./cmd/server ./cmd/cli ./cmd/scf/...)
git diff --check
```

预期：无数据竞争、无编译失败、无 whitespace 错误。环境限制导致的网络/IPv6 失败必须单独记录，不能伪装成 Provider 代码通过。

- [ ] **Step 4: Metadata dry-run**

使用本地 Metadata fake 或测试 Storage 执行 `moox-cli init` 的 create-or-verify，验证新 Space/Dataset/Field/Column/Rule 依赖顺序、重复执行的 `unchanged` 结果和冲突失败行为。

- [ ] **Step 5: Provider live probe（独立于默认测试）**

对每个 enabled HTTP Provider 做小规模、只读、单标的探测，记录 HTTP 状态、响应字段、历史边界、频率、上游错误状态和出口 IP，并由公共 `routeprobe` 记录候选线路评分和快照版本。对 TDX 的三个 Source 分别记录 TCP 连接、`7709/7727` 端口、握手/登录结果、响应帧、节点、字段单位、分页和错误分类。探测成功只证明接口当前可达，不替代多地域 SCF、Storage read-back 和生产发布门禁。

- [ ] **Step 6: SCF HTTP/TDX 出口、线路选择和 Storage 验收**

先使用每个启用地域的 Invoke 辅助函数，通过公共 `routeprobe` 对 Binance HTTP 和 TDX 三个 Source 分别完成协议探针，生成按 Provider/Source/Transport 隔离的候选线路排序；再让 Timer 函数真实触发 crypto 和 TDX 采集并完成 Storage read-back。记录每次 `scf_region`、观测到的 `egress_ip`、Provider/Source endpoint、线路评分、首线路命中率、fallback 次数、连接复用次数、成功/空响应、HTTP 429、远端忙/断连和有限重试结果。验证单函数和多地域调用下的出口可达性、线路选择、失败降级、数据完整性和 Storage 写入；不以请求速率、函数数量或更换出口后的偶然成功作为频控结论，也不设置基于频控的自动扩容门槛。

### 完成标准

1. `packages/marketcalendar` 可被 Collector 和未来其他模块直接导入，静态日历有 manifest/hash，运行时不依赖新浪网络。
2. AkShare 清单中的首批股票、指数和可转债接口都有明确的 Provider/Market/Instrument/Dataset 分类；无法提供完整 OHLCV 的接口被显式拒绝或标为 catalog-only。
3. A 股、港股、美股 Provider 使用统一强类型 Fetcher 和 KlinePipeline，现有 Binance 不再需要独立 Executor/Storage 写入旁路。
4. 交易日、时区、分钟桶、成交量/成交额单位、复权和时间戳语义均有源码 fixture 或确定性单元测试证明。
5. 逻辑 K 线满足整根来源一致、完整字段校验、固定 RowKey、SourceEventID 幂等和 fallback 证据要求。
6. `packages/tdx` 已完成普通/扩展协议的离线 fixture 验证；未确认的扩展字段没有进入 canonical Dataset，Tushare 没有运行时或配置依赖。
7. 默认 Go 测试、race、build、metadata dry-run 全部通过后，才进入 Provider live probe 和真实 SCF/Storage 验收。
8. 公共 `routeprobe` 已被 Binance HTTP 和 TDX TCP 共用，且通过协议特定探针、按地域/Provider/Source/Transport 的最优线路选择、有限 fallback 和快照隔离验证。
9. TDX 真实 SCF 验收证明了 TCP 出口、Timer 执行、连接复用、Source 选择、有限重试和 Storage read-back；未把随机公网 IP 宣称为可保证的独立 IP 或频控豁免。

## 17. 实施分批与提交边界

为降低风险，建议按以下独立批次执行，每批都能单独测试：

1. AkShare/QUANTAXIS/easy_tdx 目录 + 完整 TDX Wire Spike。
2. 公共静态交易日历 + `CivilDate`/覆盖状态 API。
3. MarketData 强类型契约 + Binance 直接迁移。
4. 公共 `routeprobe` + Binance HTTP Host/SNI 路由接入和离线测试。
5. TDX Go 公共协议库 + 普通 `7709` A 股/指数 fixture 和 Provider。
6. TDX `normal_7709`、`ex_classic_7727`、`ex_mac_7727` 协议探针、按地域/Provider/Source/Transport 的最优线路快照和 fallback 测试。
7. TDX 扩展 `7727` 动态目录、港股/美股能力验证和字段隔离。
8. A 股 EM/Sina/Tencent Provider + stock_cn pipeline。
9. 港股和美股 HTTP Provider + 独立 Market manifest。
10. 指数和可转债 Provider + 独立 Dataset metadata。
11. 通用 Executor/SCF/Job Registry 迁移、TDX Timer 接入和 crypto 新契约回归。
12. 期货、期权、基金、REITs、外汇等扩展接口目录登记；本批不创建实现文件。
13. HTTP/TDX live probe、SCF 非固定出口和最优线路验收、发布门禁和 Storage read-back。

每批只暂存该批 Files 列表，不使用 `git add -A`、`git add modules` 或宽范围回滚命令；不触碰其他任务的工作树修改。
