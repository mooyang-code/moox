# WS 行情推送采集设计

> 设计稿 v1（2026-10-20，尚未编码）。依据是同日在香港、南京两台主机上的 WebSocket 实测、线上 Storage 数据核对，以及当前 Collector／Storage 代码。
> 实现后以[采集](../模块/采集.md)为准；执行顺序见[执行计划](WS行情推送采集执行计划.md)。

## 0. 结论

- 加密货币空间改为**常驻进程接收交易所 WebSocket 推送的收盘 K 线**，按周期提交 Storage；某个标的到兜底时刻还没收到定稿，就用现有的 REST 取数补齐。SCF 定时拉取的代码和函数保留，默认不启用，可以用命令手动切回。
- 写成一个**通用的 WS K 线接收框架**：连接管理、订阅、周期闸门、兜底、提交、计划同步、观测都与交易所无关；币安是第一个数据源适配器，以后接 OKX 等只需新增适配器。
- 同时把"一个行情源能做什么"收进**统一的行情源目录**：每个数据源×市场注册一次，按实现的接口声明能力——全市场标的列表、REST K 线、WS K 线（第 5.9 节）。标的同步、SCF 取数、stream 接收与兜底都从目录取，不再各自写装配开关。stream 进程本身不拉标的列表，标的与 SCF 一样来自 Collector 的采集计划。
- 进程是服务目录里的新组件 `collector-stream`，放在海外机（香港 compute-1）的部署单元里，由 `moox-runtime` 托管。单点可接受，故障时手动切回 SCF。
- 合约必须订阅**连续合约 K 线**（`<pair>_perpetual@continuousKline_<周期>`），不能用普通 K 线（`<symbol>@kline_<周期>`）：普通 K 线冷门标的要等下一笔成交或 3 秒才收盘，连续合约 K 线固定在收盘后约 0.1 秒收盘，数值一致。
- 实测发现现行 SCF 合约数据**有相当一部分不是定稿值**（成交量、成交额、笔数偏少，收盘价不对）。这个问题与 WS 改造无关，单独先修，见第 8 节。

## 1. 背景与实测

### 1.1 现状

实时采集是"腾讯云定时器每分钟触发 42 个 SCF 函数（现货、合约各 21 个，每个约 25 个标的），函数调 REST 取刚收盘的 K 线，经 Access 用 `CommitTimeSeriesBatch` 提交"。整点币安 REST 接口变慢（约一半请求停住），整点现货 1m 写入完成要 6～7 秒，见[整点取数变慢排查](采集整点取数变慢排查与优化.md)。

### 1.2 推送与拉取的耗时（2026-10-20，香港，15:52～16:08 UTC，含 16:00 整点）

时间都是相对收盘的毫秒数。WS 是全部标的（现货 511、合约 524）收盘 K 线到达本机的时刻，SCF 是写入 Storage 完成的时刻（CLS `market_fetch_commit.since_close_done_ms`）。

| | 现货 WS | 现货 SCF | 合约 WS（普通 K 线） | 合约 SCF |
| --- | --- | --- | --- | --- |
| 普通分钟，中位标的 | 104 | 约 650 | 704 | 约 620 |
| 普通分钟，最后一个标的 | 约 150（最大 282） | 约 750～1400 | 约 3036 | 约 780～1500 |
| 16:00 整点，中位 | 193 | 6184 | 551 | 2855 |
| 16:00 整点，最后一个标的 | 290 | 6919 | 3043 | 4216 |

连续合约 K 线（第 1.3 节）全量验证（16:59～18:14 UTC，全部 524 个合约标的，1m 73 个周期、1h 2 个周期，含 17:00、18:00 两个整点）：每个周期 524 个标的全部收到定稿（含没有成交的分钟），服务端收盘时间中位 102 毫秒、最大 118 毫秒，本机到达每周期最后一个标的中位 152 毫秒、最大 191 毫秒；1m 38252 根、1h 1048 根与稳定基准 100% 一致。

资源：全量 1m＋1h 订阅（4 条连接、2070 个流）应用层报文平均 1.7 Mbps、峰值 5.9 Mbps，进程 CPU 约 6%（2 核机器）。整机网卡入站平均 4 Mbps，下载测试 160 Mbps 以上，入站不是瓶颈。

### 1.3 币安推送的特性（实测＋官方文档）

| 项目 | 现货 | 合约（USDⓈ-M） |
| --- | --- | --- |
| 地址 | `wss://stream.binance.com:9443/stream`；`data-stream.binance.vision` 只有行情 | `wss://fstream.binance.com/market/stream`（K 线只在 `/market` 路由，`/public` 只有盘口；旧地址 2026-04-23 已停） |
| 南京（国内）能否连上 | 只有 `data-stream.binance.vision` 能连上（其余被 SNI 拦截） | 不能 |
| 收盘推送（`x=true`）的服务端时间 | 全部在收盘后 250 毫秒内 | 普通 K 线：新一分钟有成交的标的，在第一笔成交时收盘（差 0～1 毫秒）；3 秒内没有成交的固定在 3000 毫秒收盘，约 27% 的推送落在这里。连续合约 K 线：全部在约 103 毫秒 |
| 网络传输（收到 − 服务端时间） | 中位 28 毫秒 | 中位 33 毫秒 |
| 每条连接的流数上限 | 1024 | 1024（2025-07 从 200 提高） |
| 入站消息限速（订阅、pong 等） | 每秒 5 条；每 IP 每 5 分钟最多建连 300 次 | 按同样的限额设计（合约的具体值在阶段 1 对照文档核实） |
| 服务端 ping | 每 20 秒 | 每 3 分钟，10 分钟不回 pong 断开 |
| 连接寿命 | 24 小时强制断开 | 24 小时强制断开 |
| 每根 K 线有没有"全部标的推完"的汇总消息 | 没有，只能按标的逐个判断 | 没有 |

### 1.4 定稿：哪些值可以进周期提交

以收盘 1 分钟后再取的 REST 历史 K 线为基准（已稳定），16:33～16:36 共 180 个标的的比对：

| 来源 | 现货 | 合约-活跃 | 合约-冷门 |
| --- | --- | --- | --- |
| WS 普通 K 线 `x=true` | 100% | 100% | 100% |
| WS 连续合约 K 线 `x=true` | — | 100% | 100% |
| REST 收盘后 0.6 秒 | 99.2% | 13% | 65% |
| REST 收盘后 1.5 秒 | 99.6% | 31.5% | 73% |
| REST 收盘后 5 秒 | 100% | 86% | 99% |
| 收盘前最后一条未收盘推送 | 91% | 25% | 48% |
| 用逐笔成交（`aggTrade`）自己聚合 | — | 98.3%（跨分钟的成交归属与 K 线不一致） | |

结论：

- 现货与合约的 WS 收盘推送都是定稿值。
- **合约 REST 在收盘后几秒内返回的是未定稿的 K 线**：成交量只会偏少、不会偏多，收盘价也可能不是最后一笔。现货 REST 0.6 秒就基本定稿。
- 收盘前的快照、自己聚合逐笔成交都对不上，不能用来替代收盘推送。

补测（16:56～17:10 UTC，含 17:00 整点，同一批标的，同样以稳定后的历史 K 线为基准）：

| REST 取数时刻（收盘后） | 现货 | 合约-活跃 | 合约-冷门 |
| --- | --- | --- | --- |
| 3 秒 | 100% | 59.3% | 93.9% |
| 8 秒 | 100% | 100% | 100% |
| 12／20／30 秒 | 100% | 100% | 100% |

合约 REST 要到收盘后 5～8 秒之间才定稿（5 秒时活跃标的 86%，8 秒起全部一致；6、7 秒未测），整点与普通分钟一样。

### 1.5 线上 Storage 数据核对（只读，View 副本）

把 Storage View 的 DuckDB 文件复制到临时目录导出，与币安稳定后的历史 K 线逐条比对（15:00～16:50 UTC，每天 110 分钟）：

| 数据集 | 日期 | 与基准不一致 | 说明 |
| --- | --- | --- | --- |
| 合约 1m | 10-18 | 26.0% | 全部是成交量偏少（中位少 2.9%，p90 少 25%，个别整根为 0），其中七成收盘价也不对 |
| 合约 1m | 10-19 | 35.7% | 同上 |
| 合约 1m | 10-20 | 55.0% | 当天把错峰降到 1 毫秒并复用连接，取数更早，问题更重；524 个标的全部受影响 |
| 合约 1h | 10-19 17:00～10-20 15:00 | 11.8% | 偏少幅度小（中位 0.02%）：1h 排在 1m 之后取，晚几百毫秒 |
| 现货 1m | 10-20 | 2.4% | 集中在活跃标的（USDC、ETH、BNB 等） |
| 现货 1h | 同上 | 0.02% | 基本正确 |

整点那一分钟的不一致率反而低（合约 1m 约 15～19%），因为整点取数慢、取得晚。

## 2. 目标与非目标

目标：

1. 加密货币空间 1m、1h 的实时 K 线由 WS 推送采集，现货整点写入完成从 6～7 秒降到 1 秒以内，合约在 1 秒以内；写入的都是定稿值。
2. 框架与交易所无关：新增数据源只写适配器（第 5.2 节的接口），不改连接管理、周期闸门、提交与 Collector。
3. 复用现有采集计划（数据集、标的快照、序号、生效周期）与 Storage 周期协议，不改 Storage。
4. SCF 实时采集保留为备用，系统初始化时默认关闭；加密空间默认 WS；可以用命令按生效周期在 WS 与 SCF 之间切换。

非目标：

- 不做 WS 进程的多活或自动切换（单点可接受）。
- 不改补采：补采仍走 SCF 补采函数（REST 历史 K 线已稳定，没有定稿问题）。
- 不覆盖 A 股（stockcn 仍是 SCF 定时拉取）。
- 不新增数据列；行情来源（WS 或 REST 兜底）只记在写入来源与指标里。

## 3. 硬约束（来自 Storage 周期协议，不能绕开）

1. **进入周期提交的只能是定稿值。** DataNode 在一个周期的全部序列都提交成功的那一刻就把周期定为 `complete`（`period_progress.go` 的 `finalizePeriodBaseLocked`），之后对该周期的 `CommitTimeSeriesBatch` 只返回回执、**不写行**；过了截止时间也一样。下游（因子、策略）在周期完成事件到达时就读了数据。所以"先写临时值、以后补取覆盖"在周期提交里做不到。事后修正只能走普通写入（`UpsertFields`，与补采同一路径），而且下游已经算过的结果不会自动重算。
2. **截止时间是收盘＋`lateness_s`。** 线上 crypto 的 1m、1h 数据集都是 15 秒。截止前没提交的序列在周期标记里记为失败，由自动补采补齐。
3. **同一周期只能有一个写入方。** 两个写入方写同一周期，先写全的那个让周期完成，后到的被静默丢弃；如果都只写了一部分，结果就混在一起。WS 与 SCF 之间的切换必须按生效周期交接（第 6.3 节）。
4. **序列身份不能变。** 行键里的 `series_tag` 是 `venue:binance|market:spot|source:spot_http`（合约为 `swap`／`swap_http`）。WS 写入沿用同一个 `series_tag` 与 `source_id`，否则就是新序列，历史窗口断开。`source_id` 里的 `http` 是历史命名，表示"币安现货 K 线"这个来源，不表示传输方式。

## 4. 总体架构

```text
control 主机
  Collector
    ├─ 计划对账（每空间每分钟）：任务与标签 → 数据集标的快照 → 生效周期 P → Storage 登记快照
    │     ├─ ingest=scf    → 下发 SCF 函数环境变量（现状）
    │     └─ ingest=stream → 写入 stream 计划版本，等 collector-stream 心跳确认
    ├─ StreamCollector 服务（新）：心跳、拉计划、上报周期统计
    └─ 自动补采（不变，仍调 SCF 补采函数）

海外机 compute-1（香港，overseas 单元）
  collector-stream（新组件，moox-runtime 托管）
    ├─ 计划同步：每 10 秒心跳，计划版本变化时拉取
    ├─ 数据源适配器：binance（现货、合约），以后 okx …
    ├─ 连接管理：分片、订阅、保活、重连、24 小时前主动轮换
    ├─ 周期闸门：按（数据集, 频率, 周期）收集收盘推送；齐了立即提交，不齐到兜底时刻用 REST 补
    ├─ 兜底取数：复用 marketdata 注册表里的 KlineFetcher（币安 REST）
    └─ 提交：复用 KlinePipeline 的行构造与周期提交 → host-gateway → Storage PrimaryStore
```

与 SCF 相比：取数从"每分钟 42 个函数各自发 25 个 REST 请求"变成"4 条常驻连接被动接收"；写入方从 42 个函数变成 1 个进程；计划、快照、周期协议、补采不变。

## 5. 通用框架（`modules/collector/internal/stream`）

### 5.1 分层

| 层 | 职责 | 与交易所有关吗 |
| --- | --- | --- |
| 数据源适配器 `Source` | 连接地址、流命名、订阅／退订报文、解码、保活、限额、兜底来源 | 是，每个交易所×市场一个 |
| 连接管理 `ConnManager` | 按流数分片、拨号、订阅限速、重连退避、主动轮换、差量订阅 | 否 |
| 周期闸门 `PeriodGate` | 按周期收集定稿、判断齐备、触发兜底与提交、处理迟到 | 否 |
| 兜底 `Fallback` | 对缺的标的调 REST（`marketdata.KlineFetcher`） | 否（来源由适配器声明） |
| 提交 `Committer` | 行构造、周期提交、失败上报 | 否（复用 `marketfetch`） |
| 计划客户端 `PlanClient` | 心跳、拉取计划、上报统计 | 否 |
| 观测 | 指标、日志、健康检查 | 否 |

### 5.2 数据源适配器接口

```go
// Source 是一个交易所的一个市场（例如币安现货、币安 U 本位永续）的 WS K 线来源。
type Source interface {
    // Key 与 marketdata 注册表里的 SourceKey 一致（provider + source_id），用来找兜底的 KlineFetcher 和构造 series_tag。
    Key() marketdata.SourceKey
    MarketType() string
    // Endpoints 按优先级返回连接地址；连接管理按顺序尝试，失败后退避换下一个。
    Endpoints() []string
    // Limits 是连接与订阅的限额。
    Limits() Limits
    // Stream 返回一个（交易所 symbol, 频率）对应的流名；不支持的频率返回 false。
    Stream(symbol string, frequency marketdata.Frequency) (string, bool)
    // Subscribe / Unsubscribe 构造订阅报文；一条报文最多 Limits.StreamsPerMessage 个流。
    Subscribe(streams []string, id int) ([]byte, error)
    Unsubscribe(streams []string, id int) ([]byte, error)
    // Decode 把一条原始消息解成零到多个事件；订阅回执、心跳等控制消息返回 Control 事件。
    Decode(raw []byte, receivedAt time.Time) ([]Event, error)
    // Keepalive 说明保活方式：服务端 ping（回 pong）或客户端定时发送的报文。
    Keepalive() Keepalive
    // Fallback 说明 REST 兜底：最早在收盘后多久取到的值才可视为定稿。
    Fallback() FallbackPolicy
}

type Limits struct {
    StreamsPerConn    int           // 币安 1024，按 1000 用
    StreamsPerMessage int           // 一条订阅报文的流数，币安 100
    MessagesPerSecond int           // 入站限速，币安 5
    MaxConnAge        time.Duration // 交易所强制断开的寿命，币安 24h
    ConnectsPerWindow int           // 建连限速（币安每 IP 5 分钟 300 次），只做保护
}

type Keepalive struct {
    ServerPing  bool          // 收到 ping 回 pong（币安）
    ClientPing  []byte        // 客户端定时发送的报文（例如某些交易所要求发文本 "ping"）
    Interval    time.Duration // 客户端发送间隔
    ReadTimeout time.Duration // 这么久没有任何消息就判定连接失效并重连
}

type FallbackPolicy struct {
    StableAfter time.Duration // 收盘后这么久 REST 才是定稿；币安现货 3 秒、合约 8 秒，见 5.6
}

// Event 是解码后的一条消息。只有 Final 为真的 K 线才进周期闸门。
type Event struct {
    Kind    EventKind // KlineUpdate / KlineFinal / Control / Error
    Symbol  string    // 交易所 symbol（大写）
    Kline   marketdata.NormalizedKline // 已填 ProviderID、SourceID、Frequency、BarStart/BarEnd、OHLCV、成交额、笔数
    EventAt time.Time // 交易所事件时间，用来算服务端延迟
}
```

要点：

- **定稿标志由适配器显式给出**（币安是 `k.x == true`）。闸门只接受 `KlineFinal`，`KlineUpdate` 只用于观测（可选）。
- 适配器只负责"协议"，不做重连、不持有状态（除订阅 ID 生成外），便于用录制的报文做单元测试。
- 一个交易所的不同市场是不同 `Source`（现货、合约分别实现，或同一类型不同参数）。
- 交易所 symbol 与 `subject_id` 的映射沿用采集计划的 `ExchangeSymbol`（例外表优先，否则 crypto 去掉连字符）。

### 5.3 连接管理

- **分片**：一个 `Source` 的全部流（标的 × 频率）按 `StreamsPerConn` 切成若干连接；1m 与 1h 分开连接（1h 的流整点才有定稿，分开便于观察）。币安现货 1m、现货 1h、合约 1m、合约 1h 各一条，共 4 条。
- **订阅**：连接建立后按 `StreamsPerMessage` 分批发送订阅，批间间隔满足 `MessagesPerSecond`；校验每条回执，回执报错的流记入事件并在下次重连时重试。
- **差量订阅**：计划变化时对比新旧流集合，只发退订与新增订阅；某条连接超过上限时新开连接。
- **保活与失效判定**：按 `Keepalive` 回 pong 或定时发报文；`ReadTimeout`（默认 60 秒）内收不到任何消息就关闭重连。
- **重连**：指数退避加抖动（1 秒起，最长 30 秒），按 `Endpoints` 轮换；重连后重新订阅全部流。
- **主动轮换**：连接寿命到 `MaxConnAge − 30 分钟`时，在非整点、分钟第 20～40 秒之间新建一条连接、订阅完同一组流、确认收到消息后再关闭旧连接；轮换期间两条连接的事件都交给闸门，闸门按（周期, 标的）去重。这样 24 小时强制断开不会造成缺数。
- **断线期间缺的定稿**：由周期闸门的兜底补齐（第 5.4 节），不在连接层补。

### 5.4 周期闸门

闸门按（数据集, 频率, 周期）维护一个"待提交周期"，周期的期望标的集合来自计划里该周期生效的那份（`PlanDataset.SelectCopy`，与 SCF 相同）。

```text
收盘时刻 C 到达
  ├─ 收到定稿：记入（同一标的重复的定稿忽略，数值不同时记告警）
  ├─ 期望标的全部到齐 → 立即提交（通常 C + 0.1～0.3 秒）
  ├─ 兜底时刻 F = C + max(fallback_after, StableAfter) 仍有缺 → 对缺的标的发 REST，结果并入
  ├─ 提交时刻：全部到齐或兜底结束，或到达提交截止 D = C + lateness − 提交预留（默认 3 秒）
  │     → 一次 CommitTimeSeriesBatch（每数据集一次，见 5.5）
  │     → 仍缺的标的不另行上报：截止时 DataNode 把没有提交成功的序列记为失败（`periodMarkerSubjects`），自动补采之后会补
  └─ 提交之后才到的定稿：只计数（late_after_commit），不再写
```

- `fallback_after` 按市场配置，默认 800 毫秒（现货 WS 的 p99 约 290 毫秒，连续合约 K 线最晚约 106 毫秒，很少触发）；兜底取到的 REST 必须晚于 `StableAfter` 才可信，所以实际兜底时刻由 `StableAfter` 决定（现货 3 秒、合约 8 秒）。
- **写权限**：每次提交（含重试）前检查本实例的写权限是否在有效期内（第 6.4 节），过期就取消在途提交、不再写。
- 兜底必须在提交截止前完成，否则放弃兜底，缺的标的留给截止判定与补采。
- 一个周期一行都没提交成功时 DataNode 不会建立这个周期（周期在第一次提交时才建立），也就没有完成标记；与 SCF 全部函数失败时相同，由自动补采的缺口扫描补齐。
- 1h 与 1m 独立闸门；整点同时有 1m、1h 两个周期。
- 闸门只按本机时钟判断时刻，部署机器必须开启 NTP；适配器给出的交易所事件时间只用于观测。
- 进程重启时丢弃内存里的待提交周期；重启后第一个完整周期从下一个收盘开始，期间的缺口由自动补采补齐。

### 5.5 提交

- **复用 SCF 的行构造与周期绑定**：把 `KlinePipeline.Execute` 里"由标准化 K 线构造行（`rowFor`）→ 按数据集分组 → `periodCommitForDataset` → `CommitTimeSeriesBatch`"这一段抽成独立的 `KlineCommitter`，SCF 与 stream 共用，保证两边写出的行逐字节一致（字段、`series_tag`、`source_id`、写入 ID 规则）。
- **请求的构造**：stream 进程用计划构造与 SCF 相同的 `PlannedRequest`（`Plan.BuildRequests` 的同一套目标、序号、hash、总数），只是 `Items` 的数据来自推送与兜底，而不是 REST。
- **一个周期一个数据集一次提交**：现货 1m 511 行、合约 1m 524 行，每行约 290 字节（压缩前），一次约 150KB。若单次请求体或耗时不合适，按序号范围分成固定的几片（例如每片 128 个标的）提交，片之间不需要错峰（同一进程串行发出）。
- **写入 ID**：沿用 `WriteIDByContent`，同一内容重试复用同一 ID。
- **写入来源**：`writeSource` 取 `collector-stream`，便于在 Storage 与 Monitor 区分写入方。
- **失败**：提交失败在截止前重试（间隔 500 毫秒，最多到 D）；最终失败的标的留给截止判定与补采。

### 5.6 兜底与 REST 定稿时间

- 兜底取数复用 `marketdata` 注册表里该 `Source.Key()` 的 `KlineFetcher`（币安 REST 适配器，含端点、限流、备援请求），每个缺的标的取 1 根。
- **只有晚于 `StableAfter` 的 REST 结果才能进提交。** 现货 `StableAfter` 取 3 秒（0.6 秒 99.3%、1.5 秒 99.9%，3 秒起 100%；一旦把未定稿的值提交，周期就完成了，之后到的正确推送也改不了，所以不能取 1 秒）。合约取 8 秒（第 1.4 节：5 秒时活跃标的 86%，8 秒起 100%）；8 秒加一次 REST（约 0.1～0.7 秒）加提交，仍在 `C + 15 秒 − 3 秒提交预留` 之内。兜底时刻之后到达的推送仍优先于 REST 结果（推送是定稿，REST 可能更晚才返回）。
- 兜底触发率是健康指标：正常时应接近 0；持续升高说明推送有问题。

### 5.7 币安适配器

| | 现货 | 合约 |
| --- | --- | --- |
| `Key` | `binance` / `spot_http` | `binance` / `swap_http` |
| 地址 | `wss://stream.binance.com:9443/stream`，备选 `wss://stream.binance.com:443/stream` | `wss://fstream.binance.com/market/stream` |
| 流名 | `<symbol 小写>@kline_<周期>` | `<pair 小写>_perpetual@continuousKline_<周期>` |
| 解码 | `data.k`：`t` 开盘、`T` 收盘、`o h l c v`、`q` 成交额、`n` 笔数、`x` 定稿 | 同左，标的在 `data.ps`（不是 `s`） |
| 保活 | 服务端 ping（20 秒） | 服务端 ping（3 分钟） |
| 限额 | 1024 流／连接，订阅 5 条／秒，24 小时 | 同左 |
| 兜底 `StableAfter` | 3 秒 | 8 秒 |

- 合约用连续合约 K 线的前提是标的都是 USDT 本位永续、`pair` 与 `symbol` 相同；适配器在订阅前校验，不满足的标的改用普通 K 线并记告警。
- 解码注意：`data` 里同时有大小写不同的键（`e`／`E`、`t`／`T`、`v`／`V`、`q`／`Q`、`l`／`L`），Go 的 `encoding/json` 对结构体字段大小写不敏感，必须为每个大小写形式都声明字段，否则数值会被串（调研工具踩过）。

### 5.8 接入新数据源（以 OKX 为例）的清单

新数据源只实现 `Source`，并在 `marketdata` 注册表里有对应的 REST `KlineFetcher` 作兜底。实现前要对照该交易所文档核实以下各项（OKX 的具体协议**未核实**，下面只列需要确认的问题）：

1. 公共行情的 WS 地址，K 线频道属于哪个路由；
2. 订阅报文格式、每条报文与每条连接的频道上限、订阅限速、建连限速；
3. 保活方式（客户端是否要定时发送报文、间隔多少）；
4. K 线推送里**有没有显式的"已收盘"标志**；没有的话怎样判断定稿（例如收到下一根的第一条更新）——这决定能否用于周期提交；
5. 收盘推送相对收盘的延迟分布、冷门标的是否像币安合约那样延迟；
6. REST K 线在收盘后多久定稿（`StableAfter`），按第 1.4 节的方法实测；
7. 连接寿命与强制断开规则；
8. 标的命名与 `subject_id` 的映射。

上线前用与本次相同的方法（全量订阅、与稳定后的 REST 基准逐条比对、覆盖整点）验收。

### 5.9 统一的行情源目录（标的列表、REST K 线、WS K 线）

**现状。** 能力接口已经统一在 `marketdata`：`MarketProvider`（描述：provider、source_id、域名、状态）、`InstrumentFetcher`（全市场标的快照 `FetchInstrumentSnapshot`）、`KlineFetcher`（REST K 线 `FetchKlines`），币安的 `MarketDataAdapter` 一个类型同时实现后两者，A 股各数据源也按同样接口实现。不统一的是**装配**——"有哪些数据源、怎样构造"写死在三处：

| 用途 | 位置 | 做法 |
| --- | --- | --- |
| 标的同步（自动标签成员、属性维护） | `marketwiring/subject_listers.go` 的 `NewSubjectListers` | 手写币安现货、合约两项，再按 A 股路由配置逐个构造数据源，包成 `subjectsync.Lister` |
| SCF 取数（加密、可转债、指数） | `marketwiring/market_runtime.go` 的 `newMarketProvider` | 按 `marketID`／`instrumentType`／`providerID`／`sourceID` 的 `if/switch` 构造，每条流水线新建一个只有一个数据源的 `Registry` |
| SCF 取数（A 股） | `marketwiring/stock_runtime.go` 的 `newStockCNProviderForSource` | 另一套按 provider 名称的 `switch` |

结果是：同一个数据源在三处各构造一次（HTTP 客户端、配置来源也各不相同：标的同步走出口代理客户端，SCF 走函数内的共享客户端）；新增一个交易所要改三处开关，WS 如果再加一处就是第四处。目前实现了标的快照的只有币安与 A 股的新浪、东方财富、百度（指数、可转债、腾讯、通达信只有 K 线），目录能直接看出每个数据源有哪些能力。

**方案：一个目录，按能力取用。**

```go
// Factory 构造一个数据源实例；Deps 由使用方注入（HTTP 客户端、DNS 路由、配置目录等），
// 同一个数据源在 Collector（标的同步）、SCF（取数）、collector-stream（接收与兜底）里用同一个工厂。
type Factory func(Deps) (MarketProvider, error)

type CatalogEntry struct {
    Key        SourceKey      // provider + source_id，例如 binance/swap_http
    MarketID   MarketID       // crypto / stockcn …
    Instrument InstrumentType // spot / swap / equity …
    New        Factory
}

// Catalog 是全部数据源的登记表；每个数据源包在 init 或显式 Register 里登记自己。
type Catalog struct{ entries []CatalogEntry }

func (c *Catalog) Listers(deps Deps) (map[ListerKey]InstrumentFetcher, error) // 实现了 InstrumentFetcher 的
func (c *Catalog) Kline(key SourceKey, deps Deps) (KlineFetcher, error)      // 实现了 KlineFetcher 的
// stream 包里：provider.(stream.Provider) 取得 WS 适配器（stream 依赖 marketdata，反过来不依赖）
```

- **能力由接口决定**：数据源实现了 `InstrumentFetcher` 就能用于标的同步，实现了 `KlineFetcher` 就能用于 SCF 取数和 stream 兜底，实现了 `stream.Provider`（返回第 5.2 节的 `Source`）就能用于 WS 接收。不需要另写能力清单。
- **一个数据源一处构造**：`NewSubjectListers`、`newMarketProvider`、`newStockCNProviderForSource` 改为从目录取；A 股按路由配置决定启用哪些数据源，仍在工厂里读配置。
- **标的身份一致**：`subject_id` 与交易所 symbol 的映射只在数据源的标的快照里产生（`Instrument.SubjectID`／`ProviderSymbol`），经标签成员进入采集计划（`ExchangeSymbol`、符号例外）；SCF 取数、WS 订阅、REST 兜底都用计划里的同一个映射，WS 不另写一套。
- **合约的连续合约 K 线需要 `pair`**：币安合约标的快照已经只保留 `TRADING` 且 `PERPETUAL` 的合约（`binance/client/swap.go`），目前 USDT 永续的 `pair` 与 `symbol` 相同。`Instrument` 增加可选的 `Pair` 字段，由标的快照带出；WS 适配器订阅前校验，`pair` 与 `symbol` 不同的标的按 `pair` 订阅、按 `symbol` 写入。
- **新增交易所（例如 OKX）只加一个包**：实现 `MarketProvider` + `InstrumentFetcher` + `KlineFetcher` + `stream.Provider`，在目录里登记，标的同步、SCF、stream 都能用；不改 `marketwiring`、`subjectsync`、`stream` 框架。

这一步是纯重构，行为不变，放在 stream 框架之前做（执行计划阶段 2 第一项）。

## 6. Collector 侧

### 6.1 采集方式

- 每个空间一个采集方式 `ingest`：`scf`（SCF 定时拉取）或 `stream`（WS 推送）。配置在 Collector `app.yaml` 的 `scf_plan.spaces.<空间>.ingest`（与现有按空间覆盖项同一处），运行时的当前值保存在 Collector 库，由命令切换（第 6.3 节），配置只决定首次启动时的初值。
- **系统初始化**（`moox setup init` / `setup deploy` 渲染 Collector 配置时）：加密空间默认 `stream`，A 股默认 `scf`；`moox.toml` 的 `[[scf_plan.spaces]]` 可写 `ingest = "scf"` 覆盖。`collector-stream` 默认放进海外机的放置清单。SCF 补采仍需要云账号与代码包，所以 `[scf_plan]` 的云账号与地域配置照旧。

### 6.2 计划

- **期望状态与快照不变**：仍由任务与标签成员算出每个数据集的标的集合、hash、序号与生效周期 P，并在 Storage 登记快照。
- **分配不同**：`ingest=stream` 时，一个空间的每个市场只有一个"stream 分配"，包含该市场全部数据集与全部标的（不受每函数 25 个、每函数 200 个、环境变量 4KB 的限制）；仍用 `Plan` 结构（`market`、`datasets[].copies[]` 的 `from`/`hash`/`count`/`subjects[].i`），只是不经过环境变量编码。
- **stream 分配是计划内容里的一种执行端**：在 `FetchPlanContent.Functions` 里与 SCF 函数并列，带执行端类型（`scf`／`stream`），名称如 `stream-crypto-swap`；过渡计划、最终计划、各份 copy、符号例外都按现有结构记录。这样计划记录仍是"谁在什么时候负责哪些标的"的唯一来源：补采按历史计划解析市场与交易所符号（`klinecheck/backfill.go` 的 `datasetMarket` 遍历 `Functions`）不需要另查别处，只需按执行端类型选用补采函数的市场与来源。
- **下发与确认**：`collector-stream` 用 `PullStreamPlan` 从计划记录里取自己那几项的计划（过渡或最终），心跳带上已应用的计划记录 ID 与阶段；Collector 看到确认才算这一项下发完成，替代 SCF 的"推环境变量 → 读回核对"。
- 对账器内部把"下发到执行端"抽成接口（SCF 舰队与 stream 各一个实现），计划生成、快照登记、生效周期选择、活动日志共用。

### 6.3 切换与交接

- 命令：`moox-cli collector ingest-mode get --space-id crypto`、`moox-cli collector ingest-mode set --space-id crypto --mode scf|stream [--yes]`（RPC `SetIngestMode`，只允许 `moox-cli`）。
- **按生效周期交接**：切换时对账器生成一份新计划，按频率各选生效周期 P（与现在选 P 的规则相同；P 是 K 线开始时间），对每个数据集：旧执行端的最后一份 copy 从 P 起没有标的，新执行端的 copy 从 P 起包含全部标的。标的集合没变，hash、总数、序号都不变，**不需要重新登记快照**（Storage 只核对该周期应生效的 hash，不关心是哪个执行端写的）。P 之前的周期由旧执行端写，P 及以后由新执行端写。
- **交接状态机**（现有"过渡 → 等旧周期结束 → 最终"的两段式不保证两端同时就绪，交接要更严格）：
  1. `prepare`：生成计划，选 P，留足提前量（P 距现在至少 3 分钟）。
  2. `release`：先给**旧执行端**下发"P 起为空"的过渡计划，等确认。确认之前新执行端不拿到含 P 的 copy。
  3. `grant`：再给**新执行端**下发"P 起全部标的"的计划，等确认。
  4. **只能在 `grant` 开始下发之前撤回**：截至 `P − 60 秒` `release` 还没全部确认，就停止交接，给旧执行端补发原计划（新执行端从未被授予，不会双写），下一轮重新选 P。`grant` 一旦开始下发，云端或进程可能已经部分生效而结果未知，就**不再撤回旧端**，只继续重试 `grant` 直到确认；P 到来时仍未确认的那部分标的在这些周期无人写，由补采补齐。顺序保证最坏情况是"P 起部分周期无人写"，不会出现两个写入方。
  5. `drain`：旧执行端继续负责 P 之前的周期，直到各频率的旧周期都过了提交截止（现有 `WindowEnd` 加收尾余量，1h 要等到 P 之后那个整点 + 迟到窗口），才下发最终计划；SCF 是旧执行端时，这之后才停用定时器（函数保留，代码包仍随发布升级）。
  - Collector 在任一步重启，按计划记录里的阶段续跑；`release` 已确认、`grant` 未确认时重启，同样按第 4 步的时限决定授予还是撤回。
- 切到 `stream`：要求 `collector-stream` 在线且持有实例租约（处于观察状态即可，正式写授权由 `grant` 从 P 起给出，第 6.4 节），否则拒绝。
- 切回 `scf`：对称（旧执行端是 stream）。`collector-stream` 离线时，Collector 无法确认它已停写，所以 P 必须晚于它写权限的到期时间加一个提交截止窗口（`P ≥ 最后一次续期的到期时间 + lateness`），这样即使它还在用缓存的计划，也已经因写权限过期而停写。
- **交接与标的变更分开**：交接计划不包含标的变更。对账发现标的集合有变化时，先按现有规则完成那份计划（登记新快照、生效、收尾），再开始交接；交接进行中出现的标的变化等交接完成后再处理。否则撤回交接时，Storage 里已为原 P 登记的新 hash 撤不掉（同一 P 不能改成另一个 hash），旧执行端恢复旧计划后提交会被拒绝，整周期缺数。

### 6.4 `StreamCollector` 服务（新）

| 方法 | 调用方 | 作用 |
| --- | --- | --- |
| `StreamHeartbeat` | `collector-stream` | 上报进程实例、已应用的计划版本、各连接状态（流数、最近消息时间、重连次数）；返回最新版本号 |
| `PullStreamPlan` | `collector-stream` | 拉取指定空间与市场的计划 |
| `ReportStreamPeriods` | `collector-stream` | 批量上报每个周期的统计：推送到达数、兜底数、兜底失败数、提交耗时、提交完成相对收盘的时间、迟到数 |

**实例租约与写授权**是两件事：实例租约决定哪个进程是"这个空间的 stream 实例"（可以拉计划、观察）；写授权是计划里按 P 授予的"从哪个周期起可以提交哪些标的"，只由交接的 `grant` 给出。

**写权限到期**：同一空间同一时刻只允许一个实例写入。心跳带实例 ID，Collector 给持有租约的实例返回写权限的到期时间（心跳时刻 + 90 秒，每 10 秒续期）；stream 进程在每次提交与重试前检查本地记录的到期时间（按本机时钟，留 5 秒余量），过期就停写、取消在途提交，只继续接收与观测。另一个实例只有在原实例写权限到期之后才能接管。Storage 的提交协议里没有写入方身份，所以"停写"只能由进程自己保证，这也是切回 SCF 时 P 要晚于写权限到期的原因。

**观察模式**：`ingest=scf` 时，Collector 也可以给 stream 下发"观察计划"（全部标的、不授予写权限），用于影子运行（执行计划阶段 5）。心跳明确报告当前是观察还是可写；正式写权限只随交接状态机的 `grant` 从 P 起生效。

### 6.5 运行状态与监控

- 运行状态页"函数"区按采集方式显示：`stream` 时显示 collector-stream 实例、连接、最近周期的到达／兜底／失败、提交完成时间。
- 指标（Collector 汇总后上报）：`moox_collector_stream_online`、`..._period_final_arrivals_total`、`..._period_fallback_total`、`..._period_missed_total`、`..._commit_done_seconds`（相对收盘）、`..._reconnects_total`。
- Monitor 告警：`collector-stream` 心跳中断超过 3 分钟；连续 5 个周期兜底率超过 10%；连续 3 个周期有失败标的。

## 7. `collector-stream` 组件

### 7.1 部署形态：服务目录组件（选定）

| 方案 | 先例 | 取舍 |
| --- | --- | --- |
| **服务目录组件，放在海外机单元，`moox-runtime` 托管（选定）** | trade、egress-proxy | 和其他组件一样由主机守护拉起、`status.sh` 可见、随发布与回滚；内部身份经本机 host-gateway 调 Collector 与 Storage，不需要 Access 签名和外部密钥 |
| 只出站的外部进程，经 Access 外部身份调用 | archive-worker、factor-engine | 适合放在不受部署管理的机器上；需要单独的部署脚本与 systemd 服务、外部身份密钥，与本场景（固定放在已部署的海外机）不匹配 |

两种都要改服务目录（新组件或外部身份、新服务与权限），**Admin、Monitor、各主机 host-gateway、Access 必须用同一提交一起发布**（见[部署与运维](../部署与运维.md)）。

### 7.2 身份与权限

- 组件 ID `collector-stream`，二进制 `moox-collector-stream`（`modules/collector/cmd/stream`，纯 Go）。
- 调用：Collector 的新服务 `StreamCollector` 三个方法（`acl` 只放 `collector-stream`）；Storage `PrimaryStore.CommitTimeSeriesBatch`（不需要 `RecordDatasetPeriodFailures`，它没有对外 RPC，截止判定已覆盖失败）。
- Storage 写入凭据与 Collector 相同：请求体里的应用密钥由 `MOOX_STORAGE_PRIMARY_AUTH_SECRET`（Storage 主密钥）按 AppId 派生（`marketstorage.storageAuthInfo`）。这个环境变量由部署端按组件白名单注入（`modules/cli/internal/setup/unitdeploy/unit.go` 的 `unitEnvironment`，目前只给 control 上的几个组件），服务目录没有密钥投影配置；要把 `collector-stream` 加进白名单，并确认海外机单元的安装材料里带有主密钥（现在只有 control 的组件需要它）。
- 首次安装：部署单元的组件与资源清单（`modules/cli/internal/setup/unitpackage`）要加入 `collector-stream`（二进制、配置模板、运行清单）；`native-release.sh install` 只能替换已有组件的二进制，不能新增组件，首次安装要走部署工具（需要支持向已部署的单元新增组件，或一次单元重新部署）。
- 只监听本机健康检查端口（`/healthz`、`/readyz`、`/metrics`），不对外提供业务端口。

### 7.3 资源与带宽

- 入站：全量 4 条连接约 1.7 Mbps，峰值约 6 Mbps；海外机入站实测 160 Mbps 以上。
- 出站：提交的数据量与现在的 SCF 相同（SCF 也经这台机器的 Access 转发到存储机）。实测每分钟前 10 秒的出站比平时多约 183KB（中位），整点约 664KB；海外机出带宽标称 1 Mbps，实测秒级峰值可到约 3.6 Mbps，现在 SCF 提交耗时 100～200 毫秒。stream 是同一进程在收盘后集中提交，阶段 0 要在海外机上实测一次整点 4 个数据集同时提交的耗时；超过 2 秒就按 5.5 分片并在 1m 与 1h 之间串行。
- CPU 与内存：调研工具约 6% CPU；进程常驻内存预计 100MB 以内。

### 7.4 单点与恢复

- 进程退出：`moox-runtime` 拉起，重启后从下一个收盘开始收，期间缺口由自动补采补齐（自动补采每 5 分钟一个槽）。
- `collector-stream` 进程或 WS 连接长时间失败：Monitor 告警后，操作员执行 `moox-cli collector ingest-mode set --space-id crypto --mode scf` 切回 SCF。
- 币安推送整体异常但 REST 正常：兜底会接住（触发率告警），必要时同样切回 SCF。
- **海外机整机故障时切回 SCF 也没用**：SCF 函数的 Storage 入口是同地域唯一的 Access，也在这台机器上（`modules/cli/internal/setup/config/scf_plan.go` 选入口、写进函数环境）。整机故障期间两条路都断，只能等机器恢复后由补采补齐；要覆盖这种情况需要另一台香港主机的 Access 与函数入口切换，不在本次范围。

## 8. 现行 SCF 合约数据不准的处理（独立于 WS，先做）

1. **立即缓解**：取数推迟到 REST 定稿之后——合约 1m、1h 收盘后约 8 秒才开始取，现货收盘后约 3 秒（第 1.4 节；现货 0.6 秒时 99.3%、线上现货 1m 不一致 2.4%，同样是早取造成的）。现有的 `stagger_base_ms` 不合适：它从函数入口算起（云定时器本身晚 0.4～1 秒），而且按空间生效（现货、合约在同一空间）。所以新增两项：按市场的拉取参数覆盖（`scf_plan.spaces.<空间>.markets.<市场>.fetch`），以及相对收盘的最早取数时刻 `not_before_ms`，合约设 8000、现货设 3000。8 秒起取数，加整点可能的 2～4 秒停住，接近 15 秒的截止时间：合约函数的单项重试预算要相应压缩，来不及的标的按失败上报、由补采补齐（补采读的是已稳定的历史 K 线）。代价是写入完成推迟：合约从约 0.6 秒到约 9 秒，现货非整点从约 0.7 秒到约 3.5 秒（整点本来就在 6 秒以上，影响小），策略产出延迟相应增加；正确性优先。WS 上线后这项设置只在切回 SCF 时生效，所以保留，切回 SCF 时数据同样是定稿值。
2. **历史数据修正**：对合约 1m、1h 从最早生效点（10-16）到修复生效时刻做一次 `force` 补采（`moox-cli collector kline backfill --force`），REST 历史 K 线已稳定，重拉即正确；补采走普通写入，不受周期完成的限制。现货 1m 不一致率 2.4%，同样可以一起重拉。
3. **下游**：因子与策略的历史结果是用不准的成交量算的。重拉之后，因子按需提交重算（因子模块的补算任务）；策略的历史结果不回改，只在说明里注明。
4. 修复后用本文第 1.5 节的方法复核一次（Storage View 副本 vs 币安基准）。

## 9. 延迟预算（目标）

| 段 | 现货 | 合约 |
| --- | --- | --- |
| 收盘到推送到达 | 0.1～0.3 秒 | 约 0.1～0.15 秒（连续合约 K 线） |
| 等齐全部标的 | 最后一个约 0.3 秒 | 约 0.15 秒 |
| 提交 Storage | 约 0.1～0.3 秒（阶段 0 实测） | 同左 |
| 写入完成相对收盘 | 约 0.5 秒，整点同样 | 约 0.5 秒 |
| 兜底时（少数标的） | 3 秒＋REST 耗时 | 8 秒＋REST 耗时 |

现货整点写入完成从 6～7 秒降到约 0.5 秒，策略整点产出延迟预计相应减少 5 秒以上。

## 10. 风险与未决

| 项 | 说明 | 处理 |
| --- | --- | --- |
| 连续合约 K 线的全量验证 | 2026-10-20 已通过：524 个标的、75 分钟、1m 与 1h、两个整点，每周期全部到齐，最晚 191 毫秒，与基准 100% 一致（第 1.2 节） | 上线前影子运行（执行计划阶段 5）再覆盖 24 小时 |
| 合约 REST 定稿时间 | 已测 8 秒（5 秒 86%，6～7 秒未测），样本只有约 15 分钟、一个整点 | 阶段 0 再跑一次覆盖多个整点；若要压到 8 秒以内再补测 6、7 秒 |
| 出站带宽 | 集中提交可能超过 1 Mbps 标称值 | 阶段 0 实测，必要时分片 |
| 单点 | 进程或 WS 故障靠手动切回 SCF；海外机整机故障时 SCF 也断（同一个 Access），只能靠恢复后补采 | 已接受 |
| 币安推送行为变化 | 例如连续合约 K 线的收盘时机改变 | 指标里的到达分布与兜底率告警 |
| `source_id` 命名 | `spot_http`／`swap_http` 名不副实 | 本次不改（改了就是新序列）；以后若要改，需要整体重新登记并迁移历史 |

## 11. 取舍记录

- **为什么不用逐笔成交自己聚合 K 线**：实测与官方 K 线的成交量、笔数只有 98.3% 一致（成交跨分钟的归属规则不同），而且冷门标的仍然无法判断"这一分钟的成交已经全部到达"。
- **为什么不用收盘前最后一条快照**：合约只有 25%～48% 等于定稿。
- **为什么不在 WS 里先写临时值再修正**：周期一旦完成就锁定，下游当时就读走了，事后修正改不了已算出的因子与策略结果（第 3 节）。
- **为什么不放南京**：南京只能连上现货的 `data-stream.binance.vision`，合约连不上。
- **为什么补采不改走 stream 进程**：补采读历史 K 线，没有定稿问题，SCF 补采函数可以并发、按需扩展，没有必要改。
