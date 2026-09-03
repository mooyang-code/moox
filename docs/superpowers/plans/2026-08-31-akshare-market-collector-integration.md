# Market Data Collector Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 AkShare、QUANTAXIS 和 easy_tdx 中确认过的交易日历、TDX 协议及行情接口，以 Go 原生公共库和 Provider 的方式整合进 MooX Collector，覆盖 A 股、港股、美股，并为指数、可转债等资产建立独立且可扩展的分类、标准化和采集链路。

**Architecture:** 新增根级 `packages/marketcalendar`、`packages/routeprobe` 和 `packages/tdx` 公共库：`marketcalendar` 使用 `go:embed` 固化中国交易日历，`routeprobe` 抽象解析候选、实际执行环境探测、线路评分、快照和有限 fallback，`tdx` 按 P0 Wire Spike 逐步以 Go 原生方式实现 easy_tdx 中已确认的 TDX TCP 帧、握手、压缩、编解码、普通行情和扩展行情，并提供 TDX 专用探针。Collector 新增强类型 `MarketProvider`、`KlineFetcher`、`InstrumentFetcher`、`KlineSpec` 和统一 `NormalizedKline`，Provider 按真实上游（TDX、东方财富、新浪、腾讯、中证、申万）归类，而不是把 AkShare、QUANTAXIS 或 Python 包作为运行时依赖。Provider 下的具体行情通道使用 `SourceID` 标识，例如 `tdx/normal_7709`、`tdx/ex_classic_7727` 和 `tdx/ex_mac_7727`；`SourceKey` 由 `ProviderID + SourceID` 组成。通用 KlinePipeline 负责日历/交易时段、标准化、完整性校验、线路路由、幂等 Storage 写入和来源追踪；Market Descriptor 负责区分 `stockcn`、`stockhk`、`stockus` 以及 `equity`、`index`、`convertible_bond` 等资产类型。现有加密货币 HTTP 链路与 TDX 共用 `routeprobe`，但使用各自的 HTTP/TCP 协议探针。TDX 实时任务采用 SCF Timer 短时执行：函数内复用连接，任务结束后退出；线路选择按 `scf_region + provider + source + transport + host:port` 维护。SCF 不实现主动限频或全局配额，随机公网出口只是运行环境事实，不能作为规避上游限制的功能承诺。

**Tech Stack:** Go 1.25、多 module `go.work`、tRPC-Go、`net`、`encoding/binary`、`compress/zlib`、`httptest`、`testify`、现有 Collector `httpclient`、Storage PrimaryStore、SQLite/GORM、YAML、`go:embed`、TDX golden wire fixtures。

## 本轮确认的设计决策

- **不用 `Feed`。** 本项目的行情接口是主动拉取，不是消息推送流；`Source` 表示某个 Provider 下的一条可独立连接、探测和故障切换的访问通道，例如 `tdx/normal_7709`、`tdx/ex_classic_7727`、`tdx/ex_mac_7727` 或 `binance/spot_http`。`SourceKey{ProviderID, SourceID}` 是唯一注册身份。
- **不用 `CapabilityKey`。** 原建议中的这个字段只是“该来源支持什么”的抽象，不是业务数据。计划改用 `SourceSpec`、`Market/Instrument/Frequency` 支持矩阵和 manifest 表达支持范围；`ProtocolVariant` 只表达握手、帧和登录差异，不能代替支持矩阵。
- **SCF 不做主动限频。** 不创建 Provider budget、全局配额、主动错峰、频控冷却或基于随机出口 IP 的请求预算。连接超时、函数 deadline、单批次边界和有限故障 fallback 属于可靠性控制，不是限频；上游 429/远端忙只作为错误和观测结果处理。
- **按新项目直接迁移。** 不保留类型别名、兼容适配层、旧 Dataset、旧入口、双写或旧资源保护。现有 Binance/crypto 业务能力迁移到统一 `market_data` composition root 后，删除旧 `crypto` Handler、SCF entrypoint 和 Binance-oriented 旁路；“crypto”保留为业务 Market/Source 语义，不保留旧入口名称。
- **TDX 扩展先证据后启用。** `ex_classic_7727` 和 `ex_mac_7727` 即使已有离线解析代码，也必须分别完成完整 wire spike、动态市场/证券目录、字段和真实 SCF 出口验证；未完成前只能是 `catalog_only` 或 `shadow`，不得写入 canonical Dataset。

## 当前执行状态（2026-09-01）

本节是总计划的进度索引；每个正式验收项都必须同时记录目标项目、节点、请求 ID、读回路径和限制条件。`[x]` 不表示所有 Provider 都已生产启用，未通过 Wire/Field、覆盖范围或 Storage View 门禁的来源仍保持 `catalog_only`。

本轮先尝试使用 Colima 的 `moox-cross-builder`，但该 profile 因 stale attach-disk 状态无法启动；随后改用宿主机 `/usr/local/go/bin/go` 完成可复现的离线验证：Collector `./...` 与 `./test` 全量测试、全量 `-race`、`go vet`、`packages/marketcalendar`/`routeprobe`/`tdx`/`marketmanifest` 全量测试和 race/vet、Storage `./...` 全量测试、CLI `./...` 及 `-race`/vet 测试，以及 Collector 的 `server`、`cli`、`scf/...` 构建均通过；其中也覆盖 `stockcn/tencent`、腾讯 JSONP 经通用 Pipeline 的 SCF Handler E2E、统一 `market_data` Handler 到 completion consumer 的 SQLite E2E、`markets`、`marketmanifest`、`marketcalendar` 和 `routeprobe`。本轮新增的 `cni`、`sw` 日线适配器、A 股新浪分钟 JSONP 适配器、公共 `BarDefinition/SessionSpec/TradabilityPolicy/SourceSpec` 及其 Registry/manifest/Pipeline catalog-only 门禁测试也通过了 Collector 全量测试和 targeted race；同时修正并核对了三个 Market 的 `provider-validation.yaml` 与 manifest 的 SourceID/状态登记。SCF 打包脚本同时通过公开 symlink 和实现文件两种调用路径的测试，最新 Linux/amd64 包 `/tmp/moox-market-data-final-20260901.zip` 包含 `main`、`config.yaml` 和 `sources/market/binance.yaml`，未包含 Python/AkShare 运行时，SHA-256 为 `c11b4eb555686d25af44d4ee96130649215d4f187050c6d99d533a838b99c431`。最近的 Pipeline/Handler/TDX 修订另行通过受影响包 targeted race/vet 和 `packages/tdx` race；新增了 `bar_end + settle_delay` 闭合边界、畸形 OHLCV 先验校验、TDX 串行批次 deadline 预算、wire/protocol 错误分类重连和 IPv6 endpoint 格式化；另将美股月线 Timer 调整为纽约月末收盘后的北京时间 08:00，并补充 Collector/CloudNode 测试。CLI 的 K 线完整性断言已改为结构化 protojson 校验，避免依赖空格格式。最新受影响包的 targeted race 以及 Collector/CLI 全量 race 也已通过。

另以构建脚本实际生成 `/tmp/moox-market-data-final-20260901.zip`；包内包含通用入口、默认配置和 Binance Provider 配置，不包含 Python/AkShare 运行时，SHA-256 为 `c11b4eb555686d25af44d4ee96130649215d4f187050c6d99d533a838b99c431`。该包已经被新项目 `crypto` 的四个 Timer 节点成功部署，但仍须结合下面的 Invoke、Provider 和 Storage 证据阅读。

- [x] 已建立 `packages/marketcalendar`：内嵌中国静态交易日历、`CivilDate`、三态覆盖查询、`valid_through` readiness、manifest/hash 校验和仓库校验脚本。
- [x] 已建立 `packages/routeprobe`：候选解析、协议无关探测协调、HTTP Host/SNI 探针、评分、快照 TTL、按 `scf_region + ProviderID + SourceID + Transport + host:port` 隔离以及有限 fallback；不包含主动限频或全局配额。
- [x] 已建立 `packages/marketmanifest` 和 Collector `MarketData` 契约：`ProviderID`、`SourceID`、`ProtocolVariant`、`SourceKey`、`SourceStatus`、Kline/Instrument Spec、`SourceSpec`、统一标准化 K 线和字段级 null 语义；Pipeline 会拒绝 `shadow/catalog_only` 来源写入 canonical Dataset。
- [x] 已落地 Collector 的 Provider Registry、通用 Provider Router、KlinePipeline、A 股/港股/美股交易时段策略，以及东方财富 A 股、港股、美股、指数、可转债的 HTTP 基线适配器。
- [x] 已落地 TDX 普通 `normal_7709` 的 Go wire/transport/命令/Provider 基线，并保留扩展 classic/MAC 的 Source、请求模型和探针边界；扩展完整帧、目录、登录和响应语义仍受 P0 Wire Spike 门禁，不能因已有模型或离线解析代码而标记生产 enabled。
- [x] 已把协议感知的最优线路选择接入通用 SCF Handler：支持按 `SCFRegion + SourceKey + TCP + host:port` 隔离的 fresh route snapshot，未命中快照时对候选 IP 执行 TDX 协议探测并按响应延迟/成功率排序；CLI 可将 TDX host、候选 IP、地域和快照注入新项目函数环境。
- [x] 已将相同的公共选择器接入现有 Binance 短时 HTTP K 线/标的采集：SCF DNS 候选先通过 Binance `/time` 协议探测，再按端点缓存本次调用的排序结果；没有 DNS 快照时仍走正常系统解析。
- [x] 已修复批量写入和 completion-loss retry 的来源事件幂等边界：Scheduler 在首次规划时为每个标的冻结稳定且独立的 `SourceEventID`，重试沿用原 `RetryKey`，同时用新的 `BatchID` 关联本次完成事件；避免 Storage 按 `source_event_id + dataset` 去重时误丢后续标的或重复写入同一逻辑 payload。
- [x] 本轮补齐并迁移了公共 `BarDefinition`、`SessionSpec`、`TradabilityPolicy` 和明确错误分类；标准化时间统一为 `[bar_start, bar_end)`，月末按自然月裁剪，交易日历越界不会再被当作普通非交易日；Pipeline 现在拒绝 Provider 返回的 subject/symbol/frequency 与请求不一致的 bar，并覆盖了对应契约测试。
- [ ] 尚未完成 TDX Wire/Field Acceptance：本轮已在 `jstdx.gtjas.com:7709` 本机单线路成功录制普通三条 setup command、证券数量请求及四个完整响应帧，并成功解码一次日线 K 线；分钟 K 线返回了超出本次探测日期的未来时间标签，字段/时间语义仍未通过人工对账。抓包哈希和响应头已记录在 `docs/tdx-go-port.md`；其他四条 `7709` 线路超时，后续重试还观测到 `jstdx.gtjas.com:7709` 的 HTTP 403 和 `jstdx.gtjas.com:7727` 的 HTTP 502，均不是 TDX 帧；`jstdx.gtjas.com:7727`/`shtdx.gtjas.com:7727` 的先前 classic/MAC 尝试也以非完整 body 结束。仍需在目标 SCF 地域录制并人工对账 `normal_7709`，并分别确认 `ex_classic_7727`、`ex_mac_7727` 的完整请求、16 字节响应头、压缩体、解压体、错误帧、登录和字段语义，详见 `docs/tdx-go-port.md`。TDX 普通传输现已把 transport/protocol 错误与数据、Storage 错误分开，重连预算覆盖串行 items 和已选线路的重连/setup；非 TDX HTTP 响应也会归类为 `ErrProtocol`，这只改善故障处理和诊断，不替代目标地域的 Wire/Field Acceptance。
- [x] 已把通用 `market_data` Timer 的静态 assignment 接到 SCF 运行时契约：Reconciler 将 Market/Instrument/Source 身份写入 assignment，CLI 将 Storage 应用身份写入函数环境，Timer 事件可从静态环境构造有界请求；当前测试仍使用 fake Storage，不等于正式 Storage 契约验收。
- [x] 新浪 HTTP Provider 基线已实现：A 股 `stock_zh_a_minute` 的 Go JSONP 分钟适配器覆盖 1/5/15/30/60 分钟并保持 `catalog_only`；A 股、港股、美股日线已实现共享 K2 压缩 JS 解码和按市场绑定的 HTTP 客户端，A 股完整 6379 行、港股 5462 行、美股 10022 行临时实盘响应已与原始 AkShare 解码结果对账，US amount 按 AkShare 语义不写入。由于尚未完成目标 SCF 地域的实盘覆盖、单位/时段和闭合 bar 门禁，新浪日线、分钟线仍保持 `catalog_only`；同花顺、国证（CNI）和申万（SW）已补齐 Go 日线适配器，CNI 原始单位明确保留为 `10k_share`/`100m_cny`，但来源仍待实盘覆盖核验；腾讯 A 股已实现并标记为 enabled，仅覆盖不复权日线。期货、期权、基金、REITs、外汇、黄金同样只登记不实现。
- [x] 已完成新项目 `crypto` 的 Binance 1m 正式 PrimaryStore 写入和精确读回：`dataset_binance_spot_kline_1m` 已激活并处于 `binding_locked/ready`，四个 `scf-event` Timer 节点 `ap-beijing`、`ap-shanghai`、`ap-singapore`、`ap-guangzhou` 均成功部署 `c11b4eb5...` 包；Timer 节点的同步 Invoke 在 `2026-09-01T14:02:00Z` 返回 `success=true`、`rows_written=2`、`request_id=9ffa5041-6618-47b7-910f-2c5fc177a441`。随后对 `PrimaryStore/ReadTimeSeriesRows` 做精确 key read-back，`2026-09-01T14:00:00Z` 和 `14:01:00Z` 两行均返回 `ret_info.code=0`，并核对了 OHLCV 字段。Storage View 进程当前未运行，范围查询返回 `dataset ... has no active view`，因此本项只证明 PrimaryStore 写入/精确读回，不宣称 View/range read-back 已通过；旧 `crypto` 云资源未被本次新项目验收复用或删除。
- [x] 已完成四地域 SCF 出口和 Binance HTTP live probe：当前项目节点的 r7 部署 job 分别为 `node-batch-418c9c7c-01fc-430a-8f8c-20d165b6c81c`（北京）、`node-batch-03c45fc6-5d71-441a-9ef9-4b2f7ef2af9d`（上海）、`node-batch-1bafeeb3-2dad-41ca-93a9-b0a6ae7fae32`（新加坡）和 `node-batch-ab698c92-9bdd-4f40-bdc0-198961173f2a`（广州）；四个节点的 Provider probe 均返回 HTTP `200`。本次只观测到新加坡出口 `43.134.111.139`，其余三个节点的 `api.ipify.org` 反射请求被拒绝；这不构成多 IP 证明，更不构成绕过频控的承诺。探针只验证实际节点到 Binance 的协议连通性和延迟观测。
- [x] 已完成一次独立 `codeCR` 审查：报告未发现 P0，提出的 3 个 P1 和 5 个 P2 已逐项修复并由主 Agent 重新运行受影响的单测/全量回归；后续复审 Agent 未在等待窗口内返回，因此不能把复审超时视为“无问题”。
- [x] 本轮 CNI/SW、manifest/config、SourceStatus 和公共时间契约修订后已完成一次独立 `codeCR`；首轮发现 1 个 P1（Pipeline 返回 bar 身份未与请求绑定）和 3 个 P2（月末 BarEnd、日历越界、SourceSpec 能力丢失），均已修复；同一独立 Agent 对修复做了定向复核，未发现 P0/P1/P2，并由主 Agent 运行 targeted/full/race tests 验证。此前其他复审 Agent 的超时仍不视为“无问题”。
- [x] 本轮新浪分钟 Provider 另由独立 `codeCR` 审查：发现新浪分钟时间戳为 end-label 的 P1 和 `NaN/Inf` 校验缺口 P2，均已先补失败测试再修复；追加 `ProviderTime` 原始标签断言后复核未发现 P0/P1/P2。剩余缺口是其他周期的 end-label、未闭合 bar 和各数值字段的逐字段测试，不影响当前 `catalog_only` 状态。
- [x] 最近几次独立 `codeCR` 复核 Pipeline、Handler、TDX transport、`NormalizedKline`、Scheduler retry 和 CloudNode cron：累计未发现 P0；提出的多标的 deadline、settle/畸形 bar、重连原子性与错误分类、IPv6、重连耗时、retry key、TDX Timer 单标的分配、`1w/1M` cron 白名单、symbol deadline、stale-row source event 和多地域发布补偿问题均已修复，并由主 Agent 运行受影响包测试、targeted race、全量 Collector 测试和 vet 验证。最新复核发现美股 `1M` Timer 使用北京时间零点会早于纽约月末收盘，已改为 `stockus + equity + 1M` 使用 `0 0 8 1 * * *`，并同步加入 CloudNode cron 白名单和测试；该时间在纽约夏令时和标准时均位于前一交易日收盘之后。剩余缺口是完整 SCF deadline、真实 TDX 节点和 Storage read-back，不能以本地测试代替。
- [x] 最后一次独立 `codeCR` 针对美股月线时区修复做了有效只读复核：检查 `BuildAssignments -> CronForMarketFrequency -> Reconciler -> CloudNode preflight/EnsureTimerTrigger` 调用链、`stockus` manifest 以及对应单测，未发现可复现的 P0/P1/P2。该复核未修改文件或发布资源；随后补充了 `TestReconcilerUsesMarketAwareMonthlyCronForUSRule`，覆盖 Reconciler 生成最终 Timer patch 的调用链；腾讯云实际 cron 时区解释仍需在正式环境验证。
- [x] 该集成测试和当前架构文档追加一次独立 `codeCR` 后，修正了两个 P2 文档问题：Invoke 函数名补充强制的 `<space>` 标识，SCF 验收步骤改为按各 Market/Source 的 Timer fleet 分别验证，避免把不同 Dataset、频率或 Market 误写为单函数承载。复核未发现 P0/P1；代码和文档修订均未发布云端资源。
- [x] 最新独立 `codeCR` 发现 TDX transport 的两个 P2：底层 Dial/读写错误丢失原始 `errors.Is` cause，以及完整帧后的 `SecurityBars` payload 解析错误仍复用连接；已改为保留底层错误链、解析错误后关闭连接，并增加对应 race 测试。该轮复核未发现 P0/P1；扩展 TDX 的 Wire/Field Acceptance 仍未通过。
- [x] 随后一次独立 `codeCR` 针对 Handler/TDX transport/route selector 发现 2 个 P1（重复 Item `SourceEventID` 会被 Storage 去重、TDX 协议错误被字符串规则误判为永久 invalid）和 3 个 P2（symbol 多 Item 静默只取第一项、TDX 阻塞 I/O 不响应主动取消、显式零值配置被默认值覆盖）。已增加请求级来源事件去重和 symbol 单 Item 校验、按 `errors.Is` 分类 TDX protocol/transport、用 `context.AfterFunc` 关闭当前连接响应取消，并使用可保留显式零值的环境解析；对应失败测试先行补齐，targeted test/race 已通过。复审 Agent 在终止前未发现新的 P0/P1/P2，但未完成全量动态复核，因此不将其“clean”作为正式审查结论。
- [x] 本轮最终本地回归已通过：Collector 全量 `go test -race`、`go vet`、server/cli/SCF build，CloudNode 全量 test/vet，CLI 全量 test/vet，`marketcalendar`/`routeprobe`/`marketmanifest`/`tdx` race/vet，以及日历校验脚本均通过；Metadata `apply --dry-run` 规划 438 个资源调用，A/H/美股筛选规划 153 个资源调用。最新 Linux/amd64 `market_data` SCF 包已重新构建并核对入口和配置清单。
- [x] 新项目正式发布前置条件已按独立环境补齐并完成 Binance 路径验收：使用 `crypto` Space、新的 Metadata/Dataset、Storage 应用签名和网关 service auth，未复用旧 `moox.toml` 的旧 Space/入口；四地域 fleet 的创建/部署 job 均以 `success` 完成。新增的 `collector function invoke` 也已通过真实 SCF request-response 调用验证通用入口；一次非 Timer Invoke 返回 `rows_written=1`，`request_id=033264c6-5b8d-420f-9f99-373bb0e93217`。正式验收不等价于所有来源启用：TDX Wire/Field、Sina/EastMoney 等股票来源、Storage View/range query 仍按各自门禁保持未完成。

当前已经可以编译的目标 SCF 入口是 `modules/collector/cmd/scf/market_data`，它接受一批明确的 `MarketID/InstrumentType/SourceKey` 请求并调用通用 KlinePipeline；Kline 的非 Timer Invoke 也会按批次契约发布 completion。Binance symbol snapshot 同样从该入口进入，并由通用 InstrumentPipeline 完成一次性全量快照、记录写入和 Metadata membership 维护；多调用分片在缺少持久化 barrier 时会被拒绝。它不是旧 `crypto` 入口的兼容模式；旧入口已经删除。TDX 扩展 Source 仍必须等待 Wire Spike 结论后才能接入该入口。

通用入口也已接入 `moox-cli collector function package --entrypoint market_data` 的本地打包路径，且 `collector function publish` 已能按 `market_data` 选择不携带 CLS/EventBus 控制面材料、生成 canonical Source 身份和 Storage 应用环境；正式发布仍必须先补齐新项目 Space、Metadata/Timer 规则、Storage 凭据和 SCF 出口 read-back，不能把仅通过本地构建的 zip 视为正式上线。发布切换完成后不再提供 `--entrypoint crypto`。

命名约定：本计划不再使用 `Feed` 或 `CapabilityKey` 作为公共概念。需要区分同一 Provider 的不同访问通道时使用 `SourceID`，组合身份使用 `SourceKey{ProviderID, SourceID}`；`ProtocolVariant` 仅描述协议握手/帧/登录差异，支持范围由 `SourceSpec`、Market/Instrument/Frequency 矩阵和 manifest 声明。

正式发布前必须单独准备一份新项目环境配置：包含新 Market/Source manifest、Storage 应用身份、网关目标和 Timer/Rule 资源；配置就绪后先以 disabled 状态发布并回读函数、环境、Timer、Rule 和 assignment，再执行实际 SCF 地域的 live probe 与 Storage read-back。任何随机公网出口只记录为观测值，不构成独立 IP 或频控豁免。

---

## 0. 范围与不变约束

本计划面向未上线的新项目，现有 [内置市场行情采集架构](../内置市场行情采集架构.md) 和 [2026-08-29 stock-cn 计划](2026-08-29-stock-cn-1m-multi-provider-scf.md) 仅作为设计参考。后者由本计划取代，不再与本计划并行执行。本计划是新的唯一实现依据，不重复建设另一套 Collector 框架，也不承诺旧代码、旧配置、旧 Dataset 或旧任务数据兼容。

本计划采用以下明确前提：

- AkShare 仅作为接口清单、参数语义和上游协议的参考；生产 Collector 使用 Go HTTP Provider，不在 SCF 中启动 Python，也不把 `packages/pyruntime` 扩展给行情采集。
- Tushare 明确排除：本项目不实现任何 Tushare Provider，不增加 token、账户、积分或 Tushare API 配置；HTTP Provider、TDX Provider 和静态日历均不依赖 Tushare。
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

### 执行顺序与拆分索引

当前文件是总设计与拆分索引，不是要求一次性实现的单个大任务。实际执行按下表推进；每个阶段完成自己的离线测试和审查后，才能进入后续阶段。文档章节保留完整上下文，但不得按章节位置直接并行编码。

| 阶段 | 独立交付物 | 前置条件 | 对应章节 |
| --- | --- | --- | --- |
| P0 | AkShare/QUANTAXIS/easy_tdx 接口目录、Source 矩阵、`docs/tdx-go-port.md` 和完整 TDX Wire Spike | 无 | Task 1 |
| P1 | `marketcalendar`、`CivilDate`、覆盖状态和静态日历校验 | P0 的日历字段确认 | Task 2 |
| P2 | MarketData、SourceKey、SourceSpec、Bar/Session/Tradability 契约和 manifest | P0、P1 | Task 3、Task 9 Step 1-2 |
| P3 | `routeprobe` 公共库和 Binance HTTP 路由迁移 | P2；TDX 只消费其已冻结接口 | Task 8A |
| P4 | TDX `normal_7709`、A 股股票/指数/可转债 Provider | P0、P2、P3 | TDX section 8（normal 部分） |
| P5 | 东方财富、腾讯等已确认 HTTP Provider，以及 A 股/港股/美股/指数/可转债分类 | P1、P2 | Task 5、Task 6、Task 7 |
| P6 | `ex_classic_7727`、`ex_mac_7727` 的独立协议验证、动态目录和扩展 Provider | P0、P2、P3；每个 Source 单独过 wire gate | TDX section 8（extended 部分）、Task 6 |
| P7 | 通用 Pipeline/Job Registry、唯一 `market_data` SCF 入口、Metadata、Storage 和旧入口删除 | P2-P6 | Task 8、Task 9、Task 10 |
| P8 | 期货、期权、基金、REITs、外汇、黄金等只做目录登记 | P0 | Task 12；不创建实现文件 |
| P9 | 新项目 SCF 配置、live probe、线路快照、Timer/Rule 和 Storage read-back | P7；P4-P6 的 enabled Source 已过门禁 | Task 11、Task 16 |

依赖关系固定为 `P0 -> P1 -> P2`，然后 `P2 -> {P3, P5}`，`{P0, P2, P3} -> {P4, P6}`，最后 `{P4, P5, P6} -> P7 -> P9`；P8 可在 P0 后独立完成，但不会阻塞首批运行时。完整 Wire Spike 是 TDX 协议实现的前置证据，不要求先创建 `routeprobe`；TDX 公共库只有在 P3 的 `routeprobe.Prober` 契约存在后才能接入它。未来若把阶段拆成独立子计划，子计划必须沿用上述阶段编号和依赖，不得重新定义身份、限频或兼容语义。

## 1. 目标接口与数据集矩阵

### 1.1 Provider 分类

| MooX Provider ID | Source/协议 | AkShare/QUANTAXIS/easy_tdx 参考接口 | 覆盖对象 | 首批状态 |
| --- | --- | --- | --- | --- |
| `tdx` | `normal_7709`，normal TCP `7709` | easy_tdx `TdxClient.get_security_bars`、`get_index_bars`；QUANTAXIS `QATdx.py`；tdx-go `config.GetBestStockQuotesServer` | A 股股票、指数、可转债及普通证券 | 实现 A 股日线/分钟线、指数日线/分钟线和线路优选 |
| `tdx` | `ex_classic_7727`，extended classic TCP `7727` | easy_tdx `ExTdxClient.get_markets`、`get_instrument_info`、`get_instrument_bars` | 港股、美股及扩展市场 | 先做协议验证和最小目录能力 |
| `tdx` | `ex_mac_7727`，extended mac TCP `7727` | easy_tdx `MacExClient` 及 MAC extended commands | 港股、美股及扩展市场 | 单独验证特殊帧和协议登录，未确认前不启用 |
| `eastmoney` | HTTP | `stock_zh_a_hist`、`stockhk_hist`、`stockus_hist`、指数/可转债 EM 接口 | A 股、港股、美股、指数、可转债 | 实现日线和可用分钟线 |
| `sina` | HTTP | `stock_zh_a_daily`、`stock_zh_a_minute`、`stockhk_daily`、`stockus_daily` | A 股、港股、美股及期权/期货扩展 | A 股分钟 JSONP 已有 catalog-only Go 适配器；日线压缩 JS 解码和港美股接口待单独验证 |
| `ths` | `daily_http` | QUANTAXIS `QAThs.py` 的 `QA_fetch_get_stock_day_in_year`、`QA_fetch_get_stock_day`；`d.10jqka.com.cn/v2/line/hs_{code}/{if_fq}/{year}.js` | A 股股票日线、板块元数据 | `catalog_only`，先验证年度切片、复权 `if_fq`、`date/open/high/low/close/volume/amount/factor` 单位和上游可用性 |
| `tencent` | HTTP | `stock_zh_a_hist_tx`、`stock_zh_index_daily_tx`、`stock_zh_ah_daily` | A 股、A+H、指数 | 实现 A 股日线，其他能力登记 |
| `cni` | HTTP | `index_hist_cni` | 国证指数 | Go 日线适配器已实现，待单位和覆盖范围实盘核验 |
| `csindex` | HTTP | `stock_zh_index_hist_csindex` | 中证指数 | 仅收盘价，保持 catalog_only |
| `sw` | HTTP | `index_hist_sw`、`index_hist_fund_sw` | 申万指数、申万基金指数 | 实现指数日线；基金指数单独登记 |

Provider ID 代表真实上游，不代表 Python 包名；Source ID 代表 Provider 下的具体行情通道，`tdx/normal_7709`、`tdx/ex_classic_7727` 和 `tdx/ex_mac_7727` 不得混用端口、握手、登录方式和字段解码。Registry 使用 `SourceKey{ProviderID, SourceID}` 注册，拒绝重复 SourceKey。`ths` 的 `stock_block` 只能作为标的/板块元数据候选，不能当作 Kline；`QA_fetch_get_stock_highlimit_reason` 在参考代码中没有实现，不建立运行时接口。未来加入其他来源时，必须先通过 Source Spec 和 fixture/实时探测确认完整字段、历史范围、分页和错误语义。Tushare 不进入该矩阵。

### 1.2 MooX Market、Instrument 和 Dataset

| Market ID | Instrument Type | Dataset ID | 首批频率 | 日历/时区 |
| --- | --- | --- | --- | --- |
| `stockcn` | `equity` | `dataset_stockcn_equity_kline` | `1m`、`1d`、`1w`、`1M` | 中国交易日历，`Asia/Shanghai` |
| `stockcn` | `index` | `dataset_stockcn_index_kline` | `1m`、`1d`、`1w`、`1M` | 中国交易日历，`Asia/Shanghai` |
| `stockcn` | `convertible_bond` | `dataset_stockcn_bond_kline` | `1m`、`1d` | 中国交易日历，`Asia/Shanghai` |
| `stockhk` | `equity` | `dataset_stockhk_equity_kline` | `1m`、`1d`、`1w`、`1M` | 港股时段，`Asia/Hong_Kong` |
| `stockus` | `equity` | `dataset_stockus_equity_kline` | `1m`、`1d`、`1w`、`1M` | 美股时段；`1M` Timer 为北京时间每月 1 日 08:00，确保纽约前一月已收盘 |

现有 `stock_kline`、`index_kline` 和旧 `crypto` SCF 入口不构成兼容约束。Metadata、任务和代码直接迁移到本矩阵定义的 canonical 名称；不保留类型别名、兼容适配层、双写入口或旧资源保护逻辑。已有 crypto 采集能力迁移为统一 MarketData Provider/Job，但旧 Handler、旧 SCF entrypoint 和旧 Binance-oriented route 在迁移完成后删除。新资源仍需具备幂等初始化能力，但初始化目标只包含新契约。

## 2. 文件边界

### 2.1 新增文件

- `packages/marketcalendar/go.mod`：根级公共日历包 module。
- `packages/marketcalendar/calendar.go`：不可变交易日历、前后交易日和区间查询 API。
- `packages/marketcalendar/calendar_test.go`：排序、重复、边界、周末和已知节假日测试。
- `packages/marketcalendar/data/cn_trading_days.json`：从当前 AkShare `calendar.json` 导入并校验后的静态数据。
- `packages/marketcalendar/data/manifest.json`：日历 ID、来源、覆盖边界、版本和 SHA-256。
- `scripts/marketdata/update-calendar.sh`：离线输入/人工审核后的年度日历更新入口，不由 Collector 运行时调用网络。
- `packages/routeprobe/go.mod`：公共解析候选、实际出口线路探测、评分和快照 module，不依赖具体 Provider。
- `packages/routeprobe/models.go`：候选线路、探针请求/结果、评分、健康状态和快照模型。
- `packages/routeprobe/resolver.go`：域名到候选地址的解析适配边界，支持消费现有 MooX DNS route snapshot。
- `packages/routeprobe/prober.go`：协议无关的探针接口、context deadline 和并发探测协调器。
- `packages/routeprobe/http.go`：保留 Host/SNI 的 HTTP/HTTPS 探针，支持 Provider endpoint 和期望状态/响应校验。
- `packages/routeprobe/selector.go`：失败线路降级、延迟/成功率/远端错误惩罚、EWMA/p95 评分、快照 TTL 和有限 fallback；不实现 Provider 限频或全局配额。
- `packages/routeprobe/snapshot.go`：按 `scf_region + provider + transport + host:port` 隔离的线路快照序列化和版本校验。
- `packages/routeprobe/*_test.go`：候选去重、并发探测、Host/SNI、评分、快照过期、fallback 和不支持协议测试。
- `packages/marketmanifest/go.mod`、`manifest.go`、`manifest_test.go`：CLI、Collector 和 SCF 共用的 Market/Instrument/Source manifest 契约；不得由 CLI 依赖 Collector `internal` 包。
- `packages/tdx/go.mod`：TDX 协议公共库 module，不引入 Python、Tushare 或 QUANTAXIS 运行时。
- `packages/tdx/models.go`：普通/扩展 TDX 的强类型 K 线、指数 K 线、市场和证券目录模型。
- `packages/tdx/frame.go`：16 字节 TDX 响应帧头、Body 长度、压缩标志和 zlib 解压边界。
- `packages/tdx/transport.go`：`7709` TCP 连接、握手、读写 deadline、连接复用和关闭。
- `packages/tdx/heartbeat.go`：普通 TDX 连接的可选心跳，不作为跨 SCF 调用的常驻机制。
- `packages/tdx/hosts.go`：普通/扩展 TDX 节点配置和 Source/端口匹配校验；线路选择委托公共 `packages/routeprobe`。
- `packages/tdx/codec/price.go`、`volume.go`、`datetime.go`：TDX 价格、成交量和日期时间编解码。
- `packages/tdx/commands/security_bars.go`、`index_bars.go`、`security_list.go`、`security_quotes.go`：普通 TDX 请求和响应解析。
- `packages/tdx/ext/transport.go`、`markets.go`、`instrument_info.go`、`instrument_bars.go`、`history_bars_range.go`：`7727` 扩展行情和动态市场目录。
- `packages/tdx/cmd/wire-spike/main.go`：单线路、单会话的完整 TDX 请求/响应字节流采集工具；不遍历节点，不把采集结果当作协议验收。
- `packages/tdx/testdata/normal-security-bars.bin`、`normal-index-bars.bin`、`extended-instrument-bars.bin`：由 easy_tdx fixture 整理并带来源说明的二进制协议样本。
- `packages/tdx/*_test.go`、`packages/tdx/codec/*_test.go`、`packages/tdx/ext/*_test.go`：帧、编解码、请求字节和响应解析测试。
- `modules/collector/internal/marketdata/provider.go`：Provider、Source 描述和统一错误。
- `modules/collector/internal/marketdata/spec.go`：`KlineSpec`、`InstrumentSpec`、请求边界和时间戳语义。
- `modules/collector/internal/marketdata/kline.go`：`KlineRequest`、`NormalizedKline` 和 K 线校验。
- `modules/collector/internal/marketdata/bar.go`、`session.go`、`tradability.go`：已冻结 `BarDefinition`、`SessionSpec`、`TradabilityPolicy`，解决各 Provider 的时间标签、集合竞价、停牌和无成交分钟语义；当前 KlinePipeline 执行 OHLCV 校验、`bar_end + settle_delay` 闭合过滤和基本时间区间校验，但不启用严格缺口修复。
- `modules/collector/internal/marketdata/instrument.go`：统一标的、Provider symbol 和快照契约。
- `modules/collector/internal/marketdata/calendar.go`：Market Calendar Policy 与公共日历包的适配边界。
- `modules/collector/internal/marketdata/provider_test.go`、`kline_test.go`、`instrument_test.go`：契约测试。
- `modules/collector/internal/sources/stockcn/eastmoney/kline.go`、`parser.go`、`symbol.go`：A 股 EM 日线/分钟线和 symbol 转换。
- `modules/collector/internal/sources/markethttp/eastmoney/client.go`：共享 JSON 解码 Getter 与保留原始响应流的 `RawGetter` 边界。
- `modules/collector/internal/sources/stockcn/tdx/kline.go`、`parser.go`、`symbol.go`：A 股 TDX 普通行情适配，复用 `packages/tdx`，不重复实现 TCP 协议。
- `modules/collector/internal/sources/stockcn/sina/kline.go`、`daily.go`、`daily_codec.go`、相关测试：A 股新浪分钟 JSONP 和 A/H/US 日线 K2 解码基线；由于单位、交易时段、闭合 bar 和目标 SCF 出口门禁未完成，运行时仍保持 `catalog_only`。
- `modules/collector/internal/sources/stockcn/tencent/kline.go`、`kline_test.go`：腾讯 A 股日线 JSONP 解码、按年请求、代码归一化和字段单位转换；当前实现集中在一个文件，不另建 parser/symbol 层。
- `modules/collector/internal/sources/stockhk/eastmoney/kline.go`、`stockhk/eastmoney/parser.go`、`stockhk/sina/kline.go`、`stockhk/sina/parser.go`：港股适配器。
- `modules/collector/internal/sources/stockhk/tdx/kline.go`、`parser.go`、`symbol.go`：港股 TDX 扩展行情适配，使用动态市场 ID。
- `modules/collector/internal/sources/stockus/eastmoney/kline.go`、`stockus/eastmoney/parser.go`、`stockus/sina/kline.go`、`stockus/sina/parser.go`：美股适配器。
- `modules/collector/internal/sources/stockus/tdx/kline.go`、`parser.go`、`symbol.go`：美股 TDX 扩展行情适配，保留盘前/盘后和 regular-session 边界。
- `modules/collector/internal/sources/index/eastmoney/kline.go`、`index/eastmoney/parser.go`、`index/cni/kline.go`、`index/sw/kline.go`、`index/sina/kline.go`、`index/tencent/kline.go`、`index/csindex/kline.go`、`index/tdx/kline.go`、`index/tdx/parser.go`：指数适配器；CNI/SW 已有 catalog-only Go 日线基线，CSIndex 只有收盘价时不得伪装成完整 K 线。
- `modules/collector/internal/sources/bond/eastmoney/kline.go`、`bond/eastmoney/parser.go`、`bond/sina/kline.go`：可转债适配器。
- `modules/collector/internal/sources/bond/tdx/kline.go`、`bond/tdx/parser.go`：TDX 可转债适配，独立于普通股票路由。
- `modules/collector/internal/markets/stockcn/calendar.go`、`sessions.go`：A 股、指数和可转债交易日/交易时段策略。
- `modules/collector/internal/markets/stockhk/sessions.go`：港股时段策略。
- `modules/collector/internal/markets/stockus/sessions.go`：美股时段策略。
- `modules/collector/internal/marketfetch/pipeline.go`：通用 KlinePipeline 的当前实现。
- `modules/collector/internal/marketfetch/instrument_pipeline.go`、`instrument_pipeline_test.go`：完整标的快照、单次激活、Metadata 注册和过期 membership 停用；缺少持久化 barrier 时拒绝多调用分片。
- `modules/collector/internal/marketfetch/request.go`：Scheduler/重试/完成消费共享的批次请求与 Storage 边界；不再包含 Binance Executor。
- `modules/collector/internal/marketfetch/completion_publisher.go`、`dns_routes.go`、`runtime_config.go`：完成事件发布、DNS 候选解析和运行时配置小工具。
- 已删除 `modules/collector/internal/marketfetch/executor.go`、`handler.go`、`timer.go` 及其旧测试；生产入口统一使用 `serverless/market_data`。
- `modules/collector/internal/marketfetch/provider_router.go`：Provider 候选链和能力路由。
- `modules/collector/internal/sources/binance/storage_rpc.go`：当前复用的窄 Storage writer 和通用 SCF 鉴权构造；完成 Storage 抽离后再迁移文件边界。
- `modules/collector/internal/marketfetch/route_policy.go`：把公共线路快照接入 Provider、Market、Source、SCF 地域和 Transport。
- `modules/collector/internal/marketfetch/route_selection.go`、`route_selection_test.go`：供 Binance HTTP、TDX TCP 复用的协议感知候选线路选择入口；不实现主动限频、全局配额或冷却。
- `modules/collector/internal/marketfetch/http_route_provider.go`、`http_route_provider_test.go`：Binance spot/swap HTTP health probe 和单次 SCF 调用内的端点选择缓存。
- `modules/collector/internal/marketfetch/route_policy_test.go`：验证 Binance HTTP 和 TDX TCP 共用路由策略时的隔离键、快照更新和 fallback。
- `modules/collector/internal/serverless/market_data/handler.go`、`handler_test.go`：通用短时 SCF HTTP/Provider Pipeline 入口和本地 fake Storage E2E。
- `modules/collector/cmd/scf/market_data/main.go`：唯一通用市场行情 SCF 构建入口；通过 Market manifest 覆盖股票和 crypto，不接受 `crypto` 入口名。
- 已删除 `modules/collector/cmd/scf/crypto/main.go`、`modules/collector/internal/serverless/crypto/handler.go` 及其测试和旧配置；新项目不保留兼容壳。
- `modules/cli/internal/command/collector.go`：只保留 `market_data` entrypoint 选择和统一打包校验，删除 `crypto` 分支。
- `modules/collector/config/markets/stockcn/market.yaml`、`calendar.yaml`、`provider-validation.yaml`：A 股市场配置。
- `modules/collector/config/markets/stockhk/market.yaml`、`provider-validation.yaml`：港股市场配置。
- `modules/collector/config/markets/stockus/market.yaml`、`provider-validation.yaml`：美股市场配置。
- `docs/akshare-market-api-catalog.md`：AkShare 接口到 MooX Provider/Market/Dataset 的完整映射表。
- `docs/tdx-go-port.md`：easy_tdx/QUANTAXIS/tdx-go TDX 能力、协议差异、线路优选、字段确认状态和 Go 移植边界。
- `scripts/marketdata/validate-calendar.sh`：静态日历格式、排序、覆盖边界和哈希校验入口。

### 2.2 需要修改的文件

- `go.work`：按 P1/P2/P3/P4 依次接入 `marketcalendar`、`marketmanifest`、`routeprobe`、`tdx`；每个 module 创建后立即加入 workspace，TDX 不得在 `routeprobe` module 创建前引用它，CLI 和 Collector 均通过 `marketmanifest` 公共包共享契约。
- `modules/collector/go.mod`：增加 `packages/marketcalendar`、`packages/routeprobe`、`packages/marketmanifest`、`packages/tdx` 本地依赖。
- `modules/collector/internal/sources/binance/storage_rpc.go`：补充通用市场行情 SCF 所需的环境鉴权 Storage writer 构造。
- `scripts/build/build.sh`、`scripts/build/build-collector-scf-package.sh`：增加并最终只保留 `market_data` SCF 编译和打包入口；删除旧 `crypto` 分支，行情包不携带 Binance 专用凭据渲染。
- `modules/collector/configs/scf/market_data/config.yaml`：通用市场行情 SCF 的最小运行时配置。
- `modules/collector/internal/sources/interface.go:13-113`：从 Binance-oriented `Collector` 直接迁移到 Market/Provider/Source/Frequency 语义，迁移完成后删除旧接口。
- `modules/collector/internal/sources/registry.go:9-129`：注册和查询 `ProviderDescriptor`、`SourceKey`、`MarketID`、`InstrumentType`，拒绝重复 SourceKey 或冲突的支持范围。
- `modules/collector/internal/sources/exchange/types.go:8-59`：直接迁移 `KlineRequest`/`Kline` 到 `marketdata`，删除旧类型和无调用者的转换函数。
- `modules/collector/internal/domain/collect_params.go:20-206`：补充 `market_id`、`instrument_type`、`exchange_id`、`provider_symbol`、`calendar_id`，并明确 Provider/Source 不进入逻辑 Task ID。
- `modules/collector/internal/domain/fetch_batch.go`、`task_instance.go`、`task_rule.go`：让任务和结果携带 Market/Instrument/Provider Attempt 信息，直接按新契约迁移现有 crypto 任务。
- `modules/collector/internal/jobs/registry.go:16-64`、`route.go:11-104`：从硬编码 Binance job route 改为基于 Market Registry 的通用 `kline`/`instrument` 任务路由。
- `modules/collector/internal/marketfetch/request.go`、`completion_publisher.go`：保留 Scheduler/重试所需的批次边界和 completion publisher；旧 `executor.go` 不再存在。
- `modules/collector/internal/marketfetch/pipeline.go`、`modules/collector/internal/sources/binance/storage_rpc.go`：由通用 Pipeline 传入完整来源字段和稳定 `SourceEventID`，Storage writer 保留必要的 RPC/auth 细节；按当前实际文件边界修改，不引用不存在的 `write_source.go`。
- `modules/collector/internal/sources/binance/kline.go:34-509`、`symbol.go` 及相关测试：实现现有 Binance 到通用契约的适配，保持 crypto 的 `series_tag` 语义。
- `modules/collector/internal/serverless`、`cmd/scf` 和 runtime 装配文件：按 Market manifest 选择唯一 composition root，不复制一份 Binance Executor；统一入口验证后删除旧 `crypto` runtime 文件。
- `modules/collector/configs/config.yaml` 和 `modules/collector/configs/sources/market/binance.yaml`：加入 Provider host、TDX Source 节点、Market manifest、静态日历、连接超时和批次边界配置；禁止把 API 密钥写入配置文件。
- `config/setup/metadata.yaml`、`config/setup/collector-rules.yaml`：直接切换到新 Market/Dataset canonical 契约，删除不合理旧资源配置，不保留兼容示例。
- `docs/内置市场行情采集架构.md`、`docs/架构总览.md`、`docs/大仓架构.md`、`docs/architecture/scf-short-lived-market-fetch.md`：同步公共日历包、公共线路探测、Binance/TDX 复用、stockhk、指数/可转债 Dataset、SCF TCP 出口边界，并将“DNS 代理”准确表述为解析候选快照与应用层线路优选。

## 3. Task 1：冻结 AkShare/QUANTAXIS/easy_tdx 接口目录和能力矩阵

**目的：** 把当前 AkShare 源码、QUANTAXIS 调用链和 easy_tdx TDX 参考实现中的能力清单转成可审计的 MooX 映射，避免实现过程中把“有函数名”或“能连上 TCP”误当成“有完整、字段已确认的 OHLCV 能力”。

**Files:**

- Create: `docs/akshare-market-api-catalog.md`
- Create: `docs/tdx-go-port.md`
- Create: `modules/collector/config/markets/stockcn/provider-validation.yaml`
- Create: `modules/collector/config/markets/stockhk/provider-validation.yaml`
- Create: `modules/collector/config/markets/stockus/provider-validation.yaml`

- [x] **Step 1: 记录接口来源、上游 URL 和响应字段**

为每个接口记录 AkShare 函数、源文件/函数、真实上游、请求参数、返回字段、时间粒度、复权选项、历史范围、是否支持分页、成交量/成交额单位和当前验证状态。至少覆盖：

```text
calendar:
  tool_trade_date_hist_sina
stockcn:
  stock_zh_a_hist, stock_zh_a_hist_min_em
  stock_zh_a_daily, stock_zh_a_minute
  stock_zh_a_hist_tx
stockhk:
  stockhk_hist, stockhk_hist_min_em, stockhk_daily
stockus:
  stockus_hist, stockus_hist_min_em, stockus_daily
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
  quantaxis:
    QAFetch/QATdx.py 的股票、指数、债券、期货和分钟线调用链
    QAFetch/QAThs.py 的 QA_fetch_get_stock_day_in_year、QA_fetch_get_stock_day、
    QA_fetch_get_stock_block、QA_fetch_get_stock_highlimit_reason
```

- [x] **Step 2: 为每个 Source 设置 `enabled`、`shadow` 或 `catalog_only`**

首批只允许完整 OHLCV、时间戳和单位均能验证的 Source 进入 `enabled`。只有实时价/均价而没有开盘价的接口，例如部分 option/REIT 分钟接口，标记为 `catalog_only`，不能伪造成 K 线。

- [x] **Step 3: 固化 Source 矩阵的字段语义**

配置中明确 `complete_ohlcv`、`has_amount`、`volume_unit`、`amount_unit`、`timestamp_mode`、`supports_range`、`max_bars_per_request`、`supports_adjustment`、`request_timeout` 和 `history_start`。如需记录上游公开限频，仅保留说明性字段，不转化为 MooX 的主动限频配置。不支持 amount 的 Provider 不进入要求成交额的逻辑 Dataset。

- [x] **Step 4: 校验目录没有遗漏或重复分类**

运行：

```bash
rg -n "^def (tool_trade_date_hist|stock_.*(hist|min|daily|minute)|index_.*hist|bond_.*(daily|min))" /Users/mooyang/Documents/go/src/github.com/akshare
```

预期：目录中的每个首批函数都能定位到一个上游 Provider、一个 Market、一个 Instrument Type 和一个 Dataset；扩展接口明确标注为 `catalog_only` 或单独实施批次。

- [x] **Step 5: 冻结 TDX 参考实现和不可移植边界**

阅读 `/Users/mooyang/Documents/go/src/github.com/easy_tdx` 的 `transport`、`codec`、`commands`、`ex` 目录，以及 `/Users/mooyang/Documents/go/src/github.com/QUANTAXIS/QUANTAXIS/QAFetch/QATdx.py` 和 `/Users/mooyang/Documents/go/src/github.com/tdx-go/config/config.go`，把普通 `7709` 和扩展 `7727` 的请求字节、响应字段、分页上限、市场编号、时间戳、成交量/成交额单位、候选线路、测速方法和已知未知字段写入 `docs/tdx-go-port.md`。明确记录：easy_tdx 和 tdx-go 都是参考实现；Go 只移植协议和已验证字段，不移植 DataFrame、CLI、离线缓存、特权 ICMP 或任何 Python 运行时。Tushare 不建立目录项。

- [ ] **Step 6: 先完成完整 TDX Wire Spike，再允许实现协议库**

这是 P0 的独立证据门禁，先于 P4/P6 的 TDX 协议实现；原始 wire 采集不依赖尚未创建的 `routeprobe`。针对 `normal_7709`、`ex_classic_7727`、`ex_mac_7727` 分别记录完整请求字节、连接/握手过程、16 字节响应头、压缩原始 Body、解压 Body、解析结果和人工对账结果。必须单独确认：classic extended 是否无普通握手、mac extended 的特殊帧和协议登录、周期编号、市场编号、分页上限、时间标签、成交量/成交额单位。当前仅有“已解压响应体”的离线 fixture 不能作为完整 wire 证据；任何未确认 Source 只能保持 `catalog_only`，不得进入 Go 协议实现或 canonical Dataset。

## 4. Task 2：建立公共静态交易日历库

**Files:**

- Create: `packages/marketcalendar/go.mod`
- Create: `packages/marketcalendar/calendar.go`
- Create: `packages/marketcalendar/calendar_test.go`
- Create: `packages/marketcalendar/data/cn_trading_days.json`
- Create: `packages/marketcalendar/data/manifest.json`
- Create: `scripts/marketdata/validate-calendar.sh`
- Create: `scripts/marketdata/update-calendar.sh`
- Modify: `go.work`
- Modify: `modules/collector/go.mod`

- [x] **Step 1: 导入并校验 AkShare 静态日历**

从 `/Users/mooyang/Documents/go/src/github.com/akshare/akshare/file_fold/calendar.json` 导入日期，保留 `YYYY-MM-DD` 格式，生成 `cn_trading_days.json`。导入程序必须拒绝空日期、非法日期、重复日期和非严格升序数据，并把 `valid_from`、`valid_through`、更新来源、版本和 SHA-256 写入 `manifest.json`。每年由维护者从参考源生成一次更新 PR，先运行 `update-calendar.sh` 和全量校验，再人工确认新增节假日/调休日；不得在 Collector 运行时请求新浪或自动改写嵌入数据。日历接近 `valid_through` 时必须进入 readiness 告警并阻止新增回溯任务；超过覆盖范围时必须 fail closed，不能把未知日期当作非交易日。

- [x] **Step 2: 定义公共 API**

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

- [x] **Step 3: 增加确定性测试**

覆盖 `1992-05-04`、manifest 首尾日期、`valid_through`、周六/周日、已知工作日、越界 Status/Previous/Next、重复导入和返回切片防修改。测试不能调用外网，也不能依赖当前日期。

- [x] **Step 4: 增加仓库校验入口**

`validate-calendar.sh` 校验 JSON、manifest 覆盖边界、SHA-256 和升序唯一性。运行：

```bash
(cd packages/marketcalendar && go test -count=1 ./...)
bash scripts/marketdata/validate-calendar.sh
```

预期：公共包测试通过，脚本报告日历 ID、首日、末日、总天数和哈希一致。

- [x] **Step 5: 固化年度更新和到期门禁**

使用 `scripts/marketdata/update-calendar.sh --source /Users/mooyang/Documents/go/src/github.com/akshare/akshare/file_fold/calendar.json --valid-through YYYY-MM-DD` 生成候选数据；脚本只修改日历 JSON 和 manifest，随后必须由人工审核 diff 并运行 `go test`、`validate-calendar.sh`。Collector readiness 在 `today > valid_through` 或 manifest/hash 不一致时失败；在 `today` 距 `valid_through` 小于 90 个 civil days 时告警并拒绝新建依赖未来日期的任务。运行时查询统一返回 `TradingDay`、`NonTradingDay` 或 `OutOfCoverage` 三态，不能把越界折叠成 `false`。

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
- Modify: `packages/marketmanifest/manifest.go`
- Modify: `packages/marketmanifest/manifest_test.go`
- Modify: `modules/collector/internal/sources/interface.go:13-113`
- Modify: `modules/collector/internal/sources/registry.go:9-129`
- Modify: `modules/collector/internal/sources/exchange/types.go:8-59`

- [x] **Step 1: 定义统一身份字段和 SourceKey**

定义 `MarketID`、`ProviderID`、`SourceID`、`ExchangeID`、`ProductType`、`InstrumentType`、`CalendarID` 和 `Frequency`。`SourceID` 表示 Provider 下的具体行情通道，例如 `normal_7709`、`ex_classic_7727`、`ex_mac_7727` 或 `spot_http`；`SourceKey` 为 `ProviderID + SourceID` 的组合键。`provider_id` 和 `source_id` 表示本次 Attempt 使用的上游通道，不进入逻辑 Task ID/RowKey。不要引入 `FeedID` 或 `CapabilityKey`；需要表达“支持什么”时使用 `SourceSpec` 和显式支持矩阵。

- [x] **Step 2: 定义 Fetcher 和 Spec**

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

`SourceSpec` 是来源的静态声明，不是运行时状态或限频器，至少包含以下字段：

```go
type SourceStatus string
const (
    SourceEnabled SourceStatus = "enabled"
    SourceShadow SourceStatus = "shadow"
    SourceCatalogOnly SourceStatus = "catalog_only"
)

type SourceSpec struct {
    Key              SourceKey
    Status           SourceStatus // enabled, shadow, catalog_only
    ProtocolVariant  string
    Transport        string       // http, https, tcp
    Host             string
    Port             uint16
    Markets          []MarketID
    Instruments      []InstrumentType
    Frequencies      []Frequency
    TimestampMode    string       // start_label or end_label
    CompleteOHLCV    bool
    HasAmount        bool
}
```

`ProviderDescriptor` 必须包含 `ProviderID`、`SourceID`、`ProtocolVariant`、Market/Instrument 支持范围、连接协议和目标端口；Registry 以 `SourceKey` 注册，不能只以 Provider ID 注册。`KlineSpec` 声明支持的 Market、Instrument、Exchange、Frequency、完整 OHLCV、amount、单位、时间戳模式、历史范围、分页、连接协议、目标端口和请求超时。TDX Source 还必须分别声明 `normal_7709`、`ex_classic_7727` 或 `ex_mac_7727` 的握手/登录差异、`host:port` 和动态市场要求。上游公开限频只作为目录备注，不生成 MooX 主动限频配置。`InstrumentSpec` 声明全量快照、分页、状态和 symbol 转换能力。

- [x] **Step 3: 定义 NormalizedKline 校验**

统一字段至少包含 `subject_id`、`provider_id`、`provider_symbol`、`frequency`、`bar_start`、`bar_end`、`open`、`high`、`low`、`close`、`volume`、可选 `amount`、单位、Provider 时间戳、抓取时间和请求 ID。增加三项显式语义：`BarDefinition` 规定逻辑 `data_time=bar_start`，`bar_end` 由频率和 Market 时区计算；`SessionSpec` 规定集合竞价、午休、盘前盘后和 DST；`TradabilityPolicy` 区分停牌/临时停牌、无成交分钟和真实缺口。Provider 的 `start_label`/`end_label` 先在标准化层转换，不能在下游猜测。

标准化层不自动补齐停牌、临时停牌或没有成交的分钟，也不把集合竞价行默认并入连续交易时段。只有 `TradabilityPolicy` 明确该时段应有数据且 Provider 返回的 bar 已闭合时，才允许执行缺口检查；严格缺口修复要等各市场 Session 和 Provider 时间语义冻结后启用。

校验拒绝 NaN/Inf、负 volume/amount、`high/low` 关系非法、重复时间桶、非单调时间和缺失必填 OHLC。禁止把缺失 amount 替换为 `close * volume`。

- [x] **Step 4: 统一错误分类并删除旧接口**

定义 `ErrTimeout`、`ErrRateLimited`、`ErrRemoteBusy`、`ErrTCP`、`ErrHTTPStatus`、`ErrProtocol`、`ErrNoClosedBar`、`ErrUnsupportedSymbol`、`ErrUnsupportedFrequency`。`ErrRateLimited` 仅用于透传上游返回的 429/频控错误，不代表 MooX 会主动限频。通用 Pipeline 切换到新契约后，直接删除 Binance-oriented 的旧 `sources.Collector`、旧类型和无调用者的转换函数，不保留兼容适配。

- [x] **Step 5: 用契约测试锁定边界**

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

- [x] **Step 1: 将 `cn_stock` 适配为 Market Calendar Policy**

A 股、指数和可转债共用公共日历，但各自声明交易时段和午休规则。中国时区的来源时间先解析为 `Asia/Shanghai`，再转 UTC 存入 Storage。

- [x] **Step 2: 实现分钟预期桶和闭合判断**

生成 `09:30-11:30`、`13:00-15:00` 的分钟桶；午休和非交易日不计为缺口。Bar `09:31` 表示 `09:30-09:31`，只在 `bar_end + settle_delay` 之后进入写入流程。

- [x] **Step 3: 分离港股/美股时段策略**

港股和美股不能查询 `cn_stock` 日历。第一阶段只执行 Provider 返回数据的时间规范化和时段合法性检查；在对应假日表导入前，不对其做严格的交易日缺口推断。

- [x] **Step 4: 测试跨时区和午休边界**

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

- [x] **Step 1: 实现东方财富日线/分钟线**

复用现有 `internal/httpclient` 的 timeout、DNS route 和 HTTP 错误处理。日线使用 `kline/get` 的 `klt=101/102/103`；分钟线按 `trends2/get` 或 `kline/get` 的实际能力选择，严格把 `1/5/15/30/60` 映射到 MooX frequency。

实现 A 股代码到 `secid` 的严格转换，覆盖沪、深、北交所；未知市场前缀直接返回 `ErrUnsupportedSymbol`，不能通过首位数字猜测。

- [x] **Step 2: 实现新浪日线/分钟线基线**

解析新浪 JSONP/JS 编码响应，覆盖 `stock_zh_a_daily` 和 `stock_zh_a_minute` 的字段映射。复权参数只作为显式请求选项存在；逻辑 Canonical Dataset 默认拒绝复权结果，复权数据不进入不复权 K 线。

- [x] **Step 2A: 先实现 A 股分钟 JSONP 基线**

已实现 `sina/stockcn_minute_http`，覆盖 `1m`、`5m`、`15m`、`30m`、`60m` 请求、JSONP 提取、字段校验、时间区间过滤和标准化 K 线；由于上游只返回固定近期窗口，适配器不宣称任意历史范围；尚未完成新浪分钟覆盖、单位、闭合 bar 和实盘响应核验，仍保持 `catalog_only`。

- [x] **Step 2B: 完成新浪压缩 JS 日线解码基线**

不得把 `stock_zh_a_daily`、`stockhk_daily`、`stockus_daily` 的压缩字符串直接当作 JSON；Go K2 解码器已通过零行确定性 fixture，并以 A/H/US 临时实盘响应完成行数、首尾日期和 AkShare 原始解码值对账。成交额/复权因子、港美股时区与交易时段语义以及目标 SCF 地域验证仍未完成，因此这些 Source 继续保持 `catalog_only`。

- [ ] **Step 2C: 完成新浪 Provider 实盘门禁**

在目标 SCF 地域补充固定 fixture、字段逐项对账、半日市/DST/盘前盘后语义、闭合 bar 和出口验证；门禁通过前不得把新浪日线或分钟线提升为 `enabled`。

- [x] **Step 3: 实现腾讯 A 股日线**

解析 `newfqkline/get` 返回的 `day/qfqday/hfqday`，实现源码中成交量和成交额单位转换，并通过 `RawGetter` 保留 JSONP 原文。当前请求固定为不复权日线；缺少 amount 或字段不完整时直接失败，不进入要求 amount 的候选链。腾讯源在 manifest/config 中仅声明 `1d`，不能被误选为分钟线。

- [ ] **Step 4: 用 HTTP fixture 测试正常和异常响应**

fixture 至少覆盖正常日线、正常分钟线、空数据、未闭合 bar、字段不足、错误 JSON、HTTP 429/500、重复 bar、乱序 bar、非法 OHLC 和单位转换。测试必须验证生成的 `NormalizedKline`，而不是只验证原始 DataFrame 等价物。

## 8. TDX Go 公共协议库和 Provider（执行阶段 P4/P6，原批次 5B）

**目的：** 在 P0 Wire Spike、P2 强类型契约和 P3 公共线路探测契约通过后，将 easy_tdx 中已经确认的 TDX TCP 请求逻辑封装为可被 Collector 和未来其他模块复用的 Go 公共库；SCF 只负责短时执行和公网出口，不把连接管理或协议解析散落到各个 Market Handler。P4 先实现 `normal_7709`；P6 再分别实现经过证据确认的 `ex_classic_7727` 和 `ex_mac_7727`，不得因为目录文件已存在就宣称扩展协议完整。

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
- Create: `modules/collector/internal/sources/bond/tdx/kline.go`、`parser.go`、`kline_test.go`
- Create: `modules/collector/internal/sources/stockhk/tdx/kline.go`、`parser.go`、`symbol.go`
- Create: `modules/collector/internal/sources/stockus/tdx/kline.go`、`parser.go`、`symbol.go`
- Modify: `go.work`
- Modify: `modules/collector/go.mod`
- Modify: `docs/tdx-go-port.md`

- [ ] **Step 1: 固化协议 fixture 和许可边界**

从 `/Users/mooyang/Documents/go/src/github.com/easy_tdx/tests/unit/test_commands_offline.py` 选取普通股票、指数和分钟线样本，整理为 Go 可读的二进制 fixture，并在 `docs/tdx-go-port.md` 记录 fixture 来源、截取方式和字段确认状态。保留 easy_tdx、pytdx、xmtdx 的 MIT 许可和归属信息；不得将 Python 包、DataFrame 或离线缓存复制进 Go module。

- [ ] **Step 2: 实现普通 TDX `7709` 传输层**

使用 `net.Dialer`、`io.ReadFull` 和 context deadline 实现连接、发送、读取 16 字节响应帧、按压缩标记执行 zlib 解压和 Body 边界校验。普通连接建立后执行 easy_tdx 中的握手命令；一次 SCF 调用内复用同一 TCP 连接请求多个标的和分页，函数退出前关闭连接。心跳只允许用于连接仍在运行的单次任务，不得把 SCF warm instance 当作跨次调用的常驻连接。

- [ ] **Step 3: 实现 `7709` K 线和目录命令**

按 wire fixture 锁定 `GetSecurityBars`、指数 K 线、证券列表和快照请求字节。实现 1/3/5/15/30/60 分钟、日/周/月/季/年原生分类的解码，保留普通股票与指数额外字段的差异；分页严格遵守服务端单页上限。价格、成交量、成交额和时间字段必须先经过独立 codec，再转换为 `NormalizedKline`，不能用字符串切割或猜测单位。

- [ ] **Step 4: 在对应 Wire Spike 通过后实现扩展 TDX `7727` 传输层和动态目录**

扩展连接单独实现 `7727` 协议，不复用普通行情握手；先实现市场列表、证券数量、证券信息和扩展 K 线请求。`ex_classic_7727` 与 `ex_mac_7727` 必须使用独立 transport composition root，不能只靠配置切换帧格式；MAC 登录和 classic 首包分别按 wire 证据实现。港股/美股路由必须以服务端动态市场信息为准，`KNOWN_EX_MARKETS` 只能作为测试辅助，不能作为生产事实来源。扩展 K 线单页上限、日期范围请求和 `bar_time` 语义写入 `KlineSpec`。

- [ ] **Step 5: 隔离未确认扩展字段**

扩展 K 线中 `position`、`trade`、`settlement` 等字段只有在 fixture 和小规模实时探测均能确认含义、单位和稳定性后，才允许进入统一 Dataset。未确认字段保留在 TDX source-specific 扩展中，不映射为 canonical `amount` 或 `volume`；不能因为字段位置相同就直接复用普通 K 线语义。

- [ ] **Step 6: 实现 TDX 专用探针和节点兼容性校验**

TDX 公共库只负责 TDX 传输、Source/端口匹配和协议探针：`normal_7709` 从实际 SCF 地域完成 TCP connect、`7709` 握手和最小合法请求，`ex_classic_7727` 和 `ex_mac_7727` 分别完成已确认的 `7727` 首包和最小合法请求。候选线路先校验 IP/域名、端口范围和 Source 匹配，再按 `host:port + source` 去重；配置中的备用端口只有通过对应协议探测后才能启用，不能因为端口字段存在就视为可用。对 tdx-go `stock_ip.json` 中重复 endpoint、`ort` 拼写导致的零端口和缺少名称等问题，Go 配置加载必须显式报错或规范化，不能静默产生错误。具体的候选排序、失败线路降级、快照和 fallback 统一由 P3/Task 8A 的公共 `packages/routeprobe` 完成，避免 Binance HTTP 和 TDX TCP 各自维护一套线路算法。

`packages/tdx` 通过 P3 已定义的 `routeprobe.Prober` 暴露 TDX 专用探针，不自行保存跨 Provider 的线路排名。公共线路快照按 `scf_region + provider + source + transport + host:port` 隔离；本项目不创建 Provider budget、全局配额或主动限频层。TDX 只声明连接超时、单次函数 deadline、单次请求页大小和有限重试等执行边界；上游返回的 429/远端忙作为错误结果和观测字段处理，不触发 MooX 主动限频。随机公网出口只作为 SCF 运行环境事实，不作为规避频控的承诺。

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

## 8A. 公共线路探测与优选模块（执行阶段 P3，原批次 5A，必须先于 TDX）

**目的：** 把当前 MooX 的解析候选快照、实际出口探测、线路评分和有限 fallback 抽象成 Provider 无关的公共能力，让加密货币 HTTP 与 TDX TCP 共用选择框架，同时保留各协议独立的可达性验证。该任务必须先于 TDX section 8/P4 执行；TDX 只能引用本任务已经确定的 `SourceKey` 和 `routeprobe.Prober`。P3 不负责完成 TDX wire spike，P0 的原始协议证据仍是 TDX 实现的独立前置条件。

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

- [x] **Step 1: 定义与 Provider 无关的路由契约**

定义候选 endpoint、Provider/Transport/Source、SCF region、egress scope、探测结果、健康状态、评分和快照版本。公共模块只处理候选、探测编排、排序、TTL、失败线路降级和有限 fallback，不包含 Binance symbol、TDX command 或 Dataset 逻辑；域名解析仍是候选来源，不把公共模块实现成监听 `53` 端口的 DNS Server，也不实现 Provider 限频或全局配额。

- [x] **Step 2: 接入协议特定探针契约**

HTTP/HTTPS 探针复用现有 Host/SNI 保留逻辑，并使用 Provider endpoint 的只读 ping 或最小合法请求验证 HTTP 状态和响应语义；P3 只定义 `ProtocolProbe` 注入边界，并用 fake TDX probe 验证 `7709/7727` 结果如何进入选择器，不在 `routeprobe` module 中引用尚未创建的 `packages/tdx`。P4/P6 创建 TDX 协议库后，再把 normal/extended 的真实探针接入该契约。只完成 TCP connect 或只测 ICMP 的结果不能标记为可用线路。

- [x] **Step 3: 实现并发探测和稳定评分**

借鉴 tdx-go 的并发探测、失败淘汰和延迟排序，但采用多次实际协议探测，综合连接延迟、首个有效响应延迟、成功率和远端错误，使用 EWMA/p95 与失败惩罚。探测只在显式的线路探测任务或 Invoke 中执行，不在每个标的或每次 K 线请求中重新测速；不依赖 Provider 配额或主动限频层。

- [x] **Step 4: 复用现有 MooX DNS route snapshot**

保留 `dnsresolver`/`dnscache` 作为“域名到候选 IP”的解析与快照层；现有 Trade Resolver 的 `TcpConnectLatencyMs` 只作为初始排序 hint 或无实际 SCF 探针时的 fallback，不冒充 SCF 线路实测。`routeprobe` 在 Timer/Invoke 的实际 SCF 出口执行协议探测后，生成按 `scf_region + provider + transport + host:port` 隔离的快照。

- [x] **Step 5: 让加密货币和 TDX 共用路由策略**

Binance 继续保留域名 Host/SNI 和 hostname fallback，但候选顺序、健康状态和快照由公共策略提供；TDX 在 P4/P6 接入同一选择器，替换为自身的 `normal_7709`、`ex_classic_7727` 和 `ex_mac_7727` 协议探针，P3 只用 fake probe 锁定选择器契约。HTTP 与 TDX 的 route key、端口、Source 和协议结果必须隔离，不能因为 Binance HTTP 可达就推断 TDX TCP 可达，反之亦然。

- [x] **Step 6: 补齐离线测试并删除旧路由入口**

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

测试必须证明 `stockhk` 不会命中 `stockcn` Provider 或日历，`stockus` 不会使用中国交易时段，错误的 Market/Instrument/Provider 组合在 Registry 层失败。

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
- Create: `modules/collector/internal/sources/bond/eastmoney/kline.go`
- Create: `modules/collector/internal/sources/bond/eastmoney/parser.go`
- Create: `modules/collector/internal/sources/bond/eastmoney/kline_test.go`
- Create: `modules/collector/internal/sources/bond/sina/kline.go`
- Create: `modules/collector/internal/sources/bond/sina/kline_test.go`
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
- Created: `modules/collector/internal/marketfetch/instrument_pipeline.go`、`modules/collector/internal/marketfetch/instrument_pipeline_test.go`
- Created: `modules/collector/internal/sources/binance/instrument.go`（Binance exchange-info 到通用 InstrumentFetcher 的适配）
- Modify: `modules/collector/internal/marketfetch/provider_router.go`（当前已落地候选链和 fallback 语义）
- Modify: `modules/collector/internal/marketfetch/route_policy.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Deleted: `modules/collector/internal/marketfetch/executor.go`、`handler.go`、`timer.go` 及对应旧测试；由通用 `market_data` Handler 直接调用 Kline/Instrument Pipeline
- Modify: `modules/collector/internal/marketfetch/storage_write_consumer.go`
- Modify: `modules/collector/internal/sources/binance/kline.go:34-509`
- Modify: `modules/collector/internal/sources/binance/symbol.go`
- Modify: `modules/collector/internal/jobs/registry.go:16-64`
- Modify: `modules/collector/internal/jobs/route.go:11-104`
- Deleted: `modules/collector/internal/serverless/crypto/handler.go`
- Deleted: `modules/collector/internal/serverless/crypto/handler_test.go`
- Modify: `modules/collector/internal/marketfetch/egress_probe.go`
- Created: `modules/collector/internal/serverless/market_data/handler.go`
- Created: `modules/collector/internal/serverless/market_data/handler_test.go`
- Created: `modules/collector/cmd/scf/market_data/main.go`

- [x] **Step 1: 建立 Provider Registry 和 Market Descriptor 装配**

启动时注册 Binance、TDX `normal_7709`、EastMoney、Sina、Tencent、CNI 和 SW 的已实现 descriptor；TDX `ex_classic_7727`、`ex_mac_7727`、CSIndex 等未完成协议或字段门禁的 Source 只在 manifest 中登记，不能伪装成运行时可调用的实现。每个运行时 descriptor 明确 Market、Instrument、Source、ProtocolVariant、目标端口、Spec 和 `enabled/shadow/catalog_only` 状态。Registry 以 `SourceKey{ProviderID, SourceID}` 注册，注册失败必须使启动失败，不能静默覆盖同一 SourceKey。未来接入两个 TDX 扩展 Source 时必须使用独立 transport composition root，不能仅靠配置切换帧格式；未通过 P0/P3/P6 门禁的 Source 只能登记，不能进入 enabled 候选链。

- [x] **Step 2: 迁移 Binance 到同一 KlineFetcher**

保持 crypto 的 24x7 交易时段和 `venue:binance` `series_tag`；将请求/结果类型直接迁移到新契约并删除 Binance-oriented 旁路。Binance HTTP 候选线路改由公共 `route_policy`/`routeprobe` 提供，继续保留 Host/SNI、解析 route snapshot 和 hostname fallback。现有 Binance 单元测试必须先通过，再接入股票 Provider。

- [x] **Step 3: 实现通用 Provider Router**

Router 按 `market_id + instrument_type + frequency + exchange_id + source` 查询候选链。主 Provider 遇到 timeout、429、远端忙、协议错误、空响应或无合法闭合 bar 时，最多尝试配置的下一个候选；本地参数错误、context 取消和 deadline 不足不得 fallback。TDX 的三个 Source 之间不得因一次失败自动互换，除非两个 Source 的字段和时间语义在 manifest 中明确相同。

- [x] **Step 4: 实现通用 KlinePipeline**

流水线固定为：读取任务和 Subject 映射、选择 Calendar/Session、请求 Provider、标准化、完整性校验、过滤未闭合 bar、生成来源字段、构造 RowKey、批量幂等写 Storage、返回逐标的结果。Provider 不得直接调用 `UpsertFields`。

- [x] **Step 5: 收敛通用 SCF Handler 和批次边界**

唯一的 `market_data` SCF 入口根据 `market_id + provider + source` 选择 composition root，直接调用通用 Kline/Instrument Pipeline。HTTP Provider 和 TDX Provider 都从公共路由策略读取按地域/Provider/Transport/Source 隔离的快照；TDX Timer 运行时在单次函数内建立并复用 TCP 连接，按函数 deadline 完成请求、聚合和一次 Storage 写入后关闭连接；不得依赖 warm instance，也不得在 SCF 内无限重试。批次/重试仅保留 Scheduler 和 completion consumer 所需的数据结构；批次内每个标的必须使用独立且稳定的 `SourceEventID`，不能复用批次 ID 导致 Storage 去重丢行。统一入口回归通过后删除旧 `crypto` Handler/entrypoint 及 Binance-oriented Executor 旁路，不保留兼容壳。当前旧旁路已删除，剩余门禁是正式 Storage 契约和云端 read-back。

- [x] **Step 6: 删除静态 Binance job route 依赖**

Job Definition 保留通用 `kline`、`instrument` 数据类型；Provider/Market/Instrument/Source 支持范围来自 Registry 和 manifest。验证新 crypto、股票、指数和可转债规则均能规划并执行，不复制多套 Executor；旧 Binance-oriented job route 在迁移后删除。

## 12. Task 9：Metadata、配置和任务初始化

**Files:**

- Create: `modules/collector/config/markets/stockcn/market.yaml`
- Create: `modules/collector/config/markets/stockcn/calendar.yaml`
- Create: `modules/collector/config/markets/stockhk/market.yaml`
- Create: `modules/collector/config/markets/stockus/market.yaml`
- Modify: `modules/collector/configs/config.yaml`
- Modify: `config/setup/metadata.yaml`
- Modify: `config/setup/collector-rules.yaml`
- Modify: `modules/collector/cmd/cli/init_schema.go`
- Modify: `modules/cli/internal/command/metadata_types.go`
- Modify: `modules/cli/internal/command/metadata_spaces.go`
- Modify: `modules/cli/internal/command/setup_init.go`

- [x] **Step 1: 为每个 Market 编写 manifest**

manifest 必须明确 Market ID、Instrument Type、Calendar ID、时区、Dataset ID、支持频率、Provider/Source 候选链、Spec 引用、HistoryPolicy、ProtocolVariant 和 `register_metadata`。TDX manifest 还必须声明 `normal_7709`、`ex_classic_7727` 或 `ex_mac_7727`、目标端口、节点池、单次页大小、连接超时和函数 deadline。Provider/Source 不由用户规则自由输入；规则引用 Market/Source，路由由内部 manifest 决定。

- [x] **Step 2: 编写 Dataset/Field 契约**

Canonical K 线字段至少包含 `open`、`high`、`low`、`close`、`volume`、可选 `amount`、`trade_date`、`close_time`、`volume_unit`、`amount_unit`、`source_provider`、`provider_symbol`、`provider_timestamp` 和 `fetched_at`。所有股票类 canonical Dataset 默认使用不复权数据。

- [ ] **Step 3: 删除旧资源并初始化新契约**

直接删除不再使用的旧 Dataset、旧 View、旧规则和旧配置入口，再由 `moox-cli init` 按新 manifest 创建 canonical Space/Dataset/Field/Column/Rule。新资源初始化仍需幂等；契约冲突直接失败并输出差异，不提供旧名称兼容或双写。

- [x] **Step 4: 增加最小规则示例**

示例至少包含 A 股 1m、港股 1d、美股 1d、中国指数 1d 和可转债 1d；每条规则包含 Market、Instrument、symbol source、target Dataset 和 frequency，不能把 Provider-specific URL 或 token 放进规则 JSON。

## 13. Task 10：Storage 行契约、幂等和来源质量

**Files:**

- Modify: `modules/collector/internal/marketfetch/storage_write_consumer.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go:132-318`
- Modify: `modules/collector/schema/collector.sql`
- Modify: `config/setup/metadata.yaml`
- Modify: `modules/collector/internal/marketfetch/pipeline_test.go`、`instrument_pipeline_test.go`、`modules/collector/test/short_lived_market_fetch_e2e_test.go`
- Modify: `modules/collector/internal/marketfetch/storage_write_consumer_test.go`
- Modify: `modules/collector/internal/sources/binance/kline_test.go`

- [x] **Step 1: 固化逻辑 RowKey**

股票、指数和可转债的 canonical RowKey 使用 `subject_id + freq + data_time + series_tag`；第一阶段股票类 `dataset_stockcn_equity_kline` 使用 `series_tag=default`，Provider 只写来源字段。Crypto 保留现有明确 venue tag。

- [x] **Step 2: 固化单位字段和质量状态**

写入前把来源单位转换为 Dataset 契约单位，并同时写 `volume_unit`/`amount_unit`。质量状态至少区分 `primary`、`fallback`、`catalog_only` 拒绝和 `unavailable`；不合法或不完整行不写 Storage。Provider 本次没有提供的可选字段必须显式写 null 并同步清理旧值，不能依赖 Storage 字段级 merge 留下上一来源的 `amount` 或质量状态。

- [ ] **Step 3: 验证整行幂等**

同一 Subject、频率、时间桶在主 Provider 和 fallback Provider 之间只能形成一行；重试同一份 payload 使用相同的 `SourceEventID`，但合法的上游修订或新 payload 必须产生新的 `SourceEventID`，不能永久把 `SourceEventID` 等同于逻辑 RowKey。Primary/fallback 的覆盖优先级必须由 manifest 明确，并由单写者租约或等价的串行提交规则保护；不能以 Provider ID 进入 RowKey 来规避重复。

- [x] **Step 4: 测试写入失败恢复**

使用 Storage fake 验证批量写入失败时结果标记、重试时重复 Upsert、完成标记顺序和来源字段与 OHLCV 同批写入。通过测试证明 Provider 不持有 Storage 引用。

## 14. Task 11：运行时边界、线路观测和安全门禁

**Files:**

- Modify: `modules/collector/internal/marketfetch/metrics.go`
- Modify: `modules/collector/internal/marketfetch/route_policy.go`
- Modify: `modules/collector/internal/marketfetch/egress_probe.go`
- Modify: `modules/collector/internal/dnsresolver/coordinator.go`
- Modify: `modules/collector/internal/dnscache/cache.go`
- Modify: `modules/collector/internal/marketfetch/reconciler.go`
- Modify: `modules/collector/internal/marketfetch/assignment.go`
- Modify: `modules/collector/internal/health/server.go`
- Modify: `modules/collector/internal/health/state.go`
- Modify: `modules/collector/config/markets/stockcn/provider-validation.yaml`
- Modify: `modules/collector/config/markets/stockhk/provider-validation.yaml`
- Modify: `modules/collector/config/markets/stockus/provider-validation.yaml`
- Modify: `docs/architecture/scf-short-lived-market-fetch.md`

- [x] **Step 1: 固化“不主动限频”边界**

本项目不创建 `provider_budget.go`，不实现 Provider requests/sec、burst、全局配额、主动错峰或频控冷却。Source Spec 只声明请求页大小、连接超时、函数 deadline 和必要的连接/协议重试上限；上游返回的 429/远端忙只作为错误结果和观测字段处理，不触发 sleep、降速或自动换出口策略。不得把 SCF 随机出口数量转换成 MooX 的请求预算。

- [x] **Step 1A: 固化 SCF 非固定公网出口策略**

TDX Timer 默认使用 SCF 公网访问且 `fixed_public_ip=false`；`scf_public_pool` 只表示实际出口策略，不承诺每个函数拥有独立 IP。固定公网 IP不是本阶段必需能力。若 SCF 绑定 VPC，必须同时验证公网访问或 NAT 出口，否则不能访问公网 TDX 节点。Collector 记录观测到的出口 IP 仅用于诊断，不得写入 Task ID、RowKey 或业务身份。

- [x] **Step 1B: 维护按地域、Provider、Source 和 Transport 隔离的最优线路快照**

线路探测从实际 SCF 地域执行，不能使用 Collector 本机 ICMP 延迟作为生产排序依据。Collector 通过公共 `routeprobe` 为每个 `scf_region + provider + source + transport + host:port` 保存候选线路顺序和版本；Timer 只读取快照并在当前调用内有限 fallback。HTTP Provider 使用 Host/SNI 保留的 HTTP 探针，TDX 使用 `7709/7727` 协议探针。使用协议成功率、p95 首个有效响应延迟和远端错误惩罚更新排序；若没有有效候选，Source 进入 `unavailable`，不得静默使用未经探测的线路。现有 Trade DNS 延迟只能作为 fallback hint，不能替代实际 SCF 探针。该模块只选择线路，不主动控制请求频率。

- [ ] **Step 2: 增加可观测字段**

CLS/Prometheus 至少记录 `market_id`、`instrument_type`、`provider_id`、`source_id`、`provider_symbol`、`frequency`、`source_kind`、`transport`、`remote_host`、`remote_port`、`scf_region`、`egress_scope`、观测到的 `egress_ip`、`connection_attempt`、`rows`、`unit`、`fallback_rank`、`error_kind`、`history_window` 和 `calendar_id`。日志不得包含密钥或完整请求头；出口 IP 只作为诊断标签，不作为稳定业务主键。

- [ ] **Step 3: 增加 readiness 门禁**

Provider/Market/Source 只有在 registry、manifest、calendar、Storage Dataset/Field 契约和 fixture contract tests 全部通过后才可标记 enabled。HTTP Provider 必须通过实际 SCF 出口的 Host/SNI/响应语义探针；TDX 还必须完成 `7709/7727` TCP 连接、对应握手/无握手分支、节点切换和有限 fallback 验证。实际出口样本只用于证明连通性和线路观测，不证明绕过上游限流。实时环境仍需独立完成 egress、Provider 连通性和 Storage read-back；本地测试通过不等于云端发布完成。

- [x] **Step 4: 验证 SCF 包不携带 Python 行情运行时**

构建检查只允许 Go Collector 和已批准的运行时依赖进入 SCF 包；AkShare 源码路径只存在于开发目录和文档引用，不复制凭据、Python site-packages 或未审计响应缓存。

## 15. Task 12：扩展接口目录登记（暂不实现）

**Files:**

- Modify: `docs/akshare-market-api-catalog.md`
- Modify: `modules/collector/config/markets/stockcn/market.yaml`
- Modify: `modules/collector/config/markets/stockhk/market.yaml`
- Modify: `modules/collector/config/markets/stockus/market.yaml`
- Modify: `config/setup/metadata.yaml`

本任务只登记目录和能力，不创建期货、期权、基金、REITs、外汇或黄金的实现文件；后续每类接口必须另立子计划。

- [x] **Step 1: 先完成能力登记**

将 `futures_zh_minute_sina`、`futures_hist_em`、交易所官方日行情、期权日/分钟、ETF/LOF、REITs、外汇和黄金接口按 `instrument_type` 分类，并记录是否为真正 OHLC、是否指定日期返回全合约、是否仅返回当前日或近期窗口。

- [x] **Step 2: 记录未来 Dataset 边界**

期货、期权、基金、REITs、外汇和现货不得复用股票 Dataset。目录中只记录未来需要独立定义的 Subject、频率、单位、成交额可选性和交易时段，不创建实现文件或启用 Dataset。

- [x] **Step 3: 标记后续实施前置条件**

官方交易所“指定日期全合约”接口可作为未来批量日行情 Fetcher，但不能假装成单标的连续 Kline。只有返回完整 OHLCV、稳定时间字段和可处理错误的接口才允许后续子计划进入 enabled；当前全部保持 `catalog_only`。

## 16. 验证顺序与完成标准

- [x] **Step 1: 公共库验证**

```bash
(cd packages/marketcalendar && go test -count=1 ./...)
(cd packages/routeprobe && go test -count=1 ./...)
(cd packages/tdx && go test -count=1 ./...)
bash scripts/marketdata/validate-calendar.sh
```

预期：静态日历包、公共线路探测包、TDX 离线协议包和数据校验均通过，未调用外网。

- [x] **Step 2: Collector 单元和契约测试**

```bash
(cd modules/collector && go test -count=1 ./internal/marketdata ./internal/markets/... ./internal/sources/... ./internal/marketfetch ./internal/jobs/...)
```

预期：Provider fixture、Calendar/Session、Registry、TDX 线路排序与 fallback、Pipeline、Storage fake 和现有 Binance 测试全部通过。

- [x] **Step 3: Collector race/build 验证**

```bash
(cd modules/collector && go test -race -count=1 ./...)
(cd modules/collector && go build ./cmd/server ./cmd/cli ./cmd/scf/...)
git diff --check
```

预期：无数据竞争、无编译失败、无 whitespace 错误。环境限制导致的网络/IPv6 失败必须单独记录，不能伪装成 Provider 代码通过。

- [x] **Step 4: Metadata dry-run**

使用本地 CLI 执行 `metadata apply --dry-run` 解析默认新项目 seed，验证新 Space/Dataset/Field/Column/Rule 的调用计划可生成且不发送 RPC；默认全量计划为 438 个资源调用，筛选 `stockcn,stockhk,stockus` 为 153 个资源调用。真实 Storage create-or-verify、重复执行的 `unchanged` 结果和冲突失败行为仍属于正式 Storage 契约门禁，未因 dry-run 通过而提前标记完成。

- [ ] **Step 5: Provider live probe（独立于默认测试）**

对每个 enabled HTTP Provider 做小规模、只读、单标的探测，记录 HTTP 状态、响应字段、历史边界、频率、上游错误状态和出口 IP，并由公共 `routeprobe` 记录候选线路评分和快照版本。对 TDX 的三个 Source 分别记录 TCP 连接、`7709/7727` 端口、握手/登录结果、响应帧、节点、字段单位、分页和错误分类。探测成功只证明接口当前可达，不替代多地域 SCF、Storage read-back 和生产发布门禁。

- [ ] **Step 6: SCF HTTP/TDX 出口、线路选择和 Storage 验收**

先使用每个启用地域的 Invoke 辅助函数，通过公共 `routeprobe` 对 Binance HTTP 和 TDX 三个 Source 分别完成协议探针，生成按 Provider/Source/Transport 隔离的候选线路排序；再让各目标 Market/Source 的 `market_data` Timer fleet 分别真实触发 crypto 和 TDX 采集并完成 Storage read-back，不能用单个 Timer 函数同时承载不同 Dataset、频率或 Market。记录每次 `scf_region`、观测到的 `egress_ip`、Provider/Source endpoint、线路评分、首线路命中率、fallback 次数、连接复用次数、成功/空响应、HTTP 429、远端忙/断连和有限重试结果。验证单函数和多地域调用下的出口可达性、线路选择、失败降级、数据完整性和 Storage 写入；不以请求速率、函数数量或更换出口后的偶然成功作为频控结论，也不设置基于频控的自动扩容门槛。

#### 2026-09-01 已完成的正式验收子项

- [x] **Step 6A：Binance/crypto 独立验收**：在新项目 `crypto` 中启用 `dataset_binance_spot_symbols` 和 `dataset_binance_spot_kline_1m`，四地域 `scf-event` Timer fleet 均已成功部署包 `c11b4eb555686d25af44d4ee96130649215d4f187050c6d99d533a838b99c431`。四个节点的 Binance Provider probe 均返回 HTTP `200`；只观测到一个公网反射地址 `43.134.111.139`，另外三个节点的 `api.ipify.org` 请求被拒绝，因此没有据此推导多 IP 或频控结论。
- [x] **Step 6B：Timer 节点 Invoke 与 PrimaryStore 精确读回**：向广州 Timer 节点发送真实 SCF Timer 事件 `Type=Timer/TriggerName=moox-market-fetch-timer/Message=market_fetch_timer_v1`，返回 `success=true`、`rows_written=2`、`request_id=9ffa5041-6618-47b7-910f-2c5fc177a441`。随后使用 `PrimaryStore/ReadTimeSeriesRows` 的精确 keys 读回 `BTC-USDT` 在 `2026-09-01T14:00:00Z` 和 `14:01:00Z` 的 OHLCV，两行 `ret_info.code=0`。另一次非 Timer 通用入口 Invoke 写入 1 行，request ID 为 `033264c6-5b8d-420f-9f99-373bb0e93217`。
- [ ] **Step 6C：Storage View/range 和其他 Provider**：正式环境的 `storage-view` 进程当前未运行，范围查询返回 `dataset ... has no active view`，所以不能把 range read-back 标为通过。TDX 三个 Source 的完整 Wire/Field Acceptance，以及 A/H/美股其他 Provider 的目标 SCF 覆盖、时段和字段门禁也尚未完成。

### 完成标准

1. `packages/marketcalendar` 可被 Collector 和未来其他模块直接导入，静态日历有 manifest/hash，运行时不依赖新浪网络。
2. AkShare 清单中的首批股票、指数和可转债接口都有明确的 Provider/Market/Instrument/Dataset 分类；无法提供完整 OHLCV 的接口被显式拒绝或标为 catalog-only。
3. A 股、港股、美股 Provider 使用统一强类型 Fetcher 和 KlinePipeline，现有 Binance 不再需要独立 Executor/Storage 写入旁路。
4. 交易日、时区、分钟桶、成交量/成交额单位、复权和时间戳语义均有源码 fixture 或确定性单元测试证明。
5. 逻辑 K 线满足整根来源一致、完整字段校验、固定 RowKey、SourceEventID 幂等和 fallback 证据要求。
6. `packages/tdx` 的 `normal_7709` 具备 wire/framing、codec 和 parser fixture；`ex_classic_7727`、`ex_mac_7727` 只有在各自 Wire Spike 后才能生成对应 fixture、实现和 enabled Source，Tushare 没有运行时或配置依赖。
7. 默认 Go 测试、race、build、metadata dry-run 全部通过后，才进入 Provider live probe 和真实 SCF/Storage 验收。
8. 公共 `routeprobe` 已被 Binance HTTP 和 TDX TCP 共用，且通过协议特定探针、按地域/Provider/Source/Transport 的最优线路选择、有限 fallback 和快照隔离验证。
9. 正式验收完成后必须证明 TDX TCP 出口、Timer 执行、连接复用、Source 选择、有限重试和 Storage read-back；在此之前不得把本地编译、离线 fixture 或随机公网 IP 宣称为正式 SCF 交付或频控豁免。
10. 静态日历具备版本、哈希、`valid_through` 和年度更新流程；到期、越界或校验失败时 readiness fail closed。

## 17. 实施分批与提交边界

为降低风险，按 P0-P9 独立提交，每批都必须有明确测试和完成条件：

1. P0：接口目录、Source 矩阵、`docs/tdx-go-port.md` 和三类 TDX 完整 Wire Spike；只产出参考记录、fixture 证据和门禁结论。
2. P1：公共静态日历、`CivilDate`、三态覆盖查询、manifest/hash 和年度更新脚本。
3. P2：MarketData、SourceKey、SourceSpec、Bar/Session/Tradability 契约与 Market manifest。
4. P3：公共 `routeprobe`、DNS 候选适配、Binance HTTP Host/SNI 路由和协议无关探针测试；不实现 TDX 具体帧。
5. P4：TDX `normal_7709` Go 协议库、A 股股票/指数/可转债 Provider 和普通线路探针。
6. P5：东方财富、腾讯以及经证据确认的其他 HTTP Provider，完成 A 股/港股/美股/指数/可转债的独立 Dataset 分类。
7. P6：`ex_classic_7727`、`ex_mac_7727` 分别实现独立 transport、动态目录、字段隔离和港股/美股扩展 Provider；任何未过 Source gate 的实现保持 `shadow/catalog_only`。
8. P7：通用 Pipeline/Job Registry、唯一 `market_data` SCF 入口、Metadata、Storage、幂等/覆盖规则回归，并删除旧 `crypto` entrypoint、旧 Executor/Handler/Timer 旁路和旧路由。
9. P8：期货、期权、基金、REITs、外汇、黄金等只登记目录和未来 Dataset 边界，不创建实现文件、不启用规则。
10. P9：新项目配置、disabled 发布回读、SCF 多地域 live probe、线路快照、Timer/Rule 和 Storage read-back；正式验收不包含主动限频结论。

每批只暂存该批 Files 列表，不使用 `git add -A`、`git add modules` 或宽范围回滚命令；不触碰其他任务的工作树修改。
