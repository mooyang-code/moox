# A 股采集、因子、策略链路设计与执行计划

状态：设计稿 v5（2026-11-03），尚未编码。v1～v4 以日线为主频率，经三轮独立审查；v5 按用户决定把主频率改为 5m，与加密货币空间同样走“采集 → 因子 → 策略”，修订点见第 8 节。项目尚未上线，接口、表结构和数据约定直接修改，不写迁移，也不设计回退；上线时 stockcn 部分全新部署。

## 0. 已拍板的范围

| 项 | 决定 |
| --- | --- |
| 频率 | 默认 5m：SCF 直接采 5m K 线，因子和策略都按 5m 计算，其他做法与加密货币空间相同 |
| 数据源与复权 | 东方财富为主、通达信为备，存不复权 OHLCV（两家口径一致，见 3.2.1） |
| 标的范围 | 沪深 300 成分股：自动标签 `cn_hs300`，由 Collector 按接口定时同步成分（3.2.5），跑通后再扩到全 A |
| 因子与策略 | 一个 5m 因子任务，只放双均线用到的两个成员 `ma__5`、`ma__10`；一个 5m 双均线示例策略。EMA 等长回看因子先不算，初次体验不需要拉几万根历史 K 线 |
| 链路终点 | 策略实例产出结果为止：实例不绑定交易账户，不发目标给 Trade，不做纸面交易 |
| 交易日历 | 不在本方案里续期 2027 年，到时按年度日历更新处理；本方案只把两份日历统一成一份 |

不在本方案内：
- 1m、日线及其他周期的采集，以及 K 线合成；
- A 股交易执行和纸面交易；
- 指数与可转债（数据源目前是占位实现）；
- 全 A 规模（扩容条件见第 6 节）。

## 1. 现状与缺口

crypto 的链路如下。其中对账、计划、快照登记、Storage 周期裁决和两种事件都与市场无关，stockcn 可以直接复用。

1. Collector 按空间对账，生成采集计划。
2. 执行端写入 Storage：crypto 用 WS 推送，其他空间默认用 SCF 定时器。
3. DataNode 裁决周期，发出 `DatasetPeriodCompleted`。
4. 因子引擎计算，写结果并上报周期。
5. View 发出 `ViewDataReady`。
6. 策略求值。

| 环节 | stockcn 已具备 | 缺口 |
| --- | --- | --- |
| 交易日历 | `packages/marketcalendar` 内嵌日历（1990～2026）；`packages/barclock` 已登记 stockcn 交易时段（`market.go:26`）；Monitor 已按 barclock 计算交易时段市场的日内周期（`monitor/internal/metrics/market_clock.go:241`） | 两份日历各自维护：内嵌日历和 collector 的 `config/markets/stockcn/calendar.yaml` |
| 采集 | SCF 定时器对账与打包；交易分钟过滤（`marketwiring/handler.go:57`）；标的同步（东方财富）；任务结果数据集从标签写 `market_type`（`taskresult/result.go:540-546`）；通达信已声明 5m（`tdx/provider.go:86`，回看 365 天） | **只能采 1m。** 写死 1m 的位置：<br>• `marketwiring/handler.go:66`<br>• `marketfetch/kline_pipeline.go:65-68`、`:767`<br>• `jobs/kline/definition.go:55`<br>• 路由 `route.yaml` 与 `stock_route.go:77`<br>• 完整性检查 `bootstrap/fetch_plan.go:298`<br>东方财富只请求 `klt=1`、拒绝非 1m 请求（`eastmoney/provider.go:114,137`）；分钟线归一化写死 1 分钟（`stockcn/common.go:116-131`）。结果数据集不写 `calendar`。stockcn 函数从未上线 |
| 因子 | 事件消费、计算、表达式、结果上报都与市场无关 | `periodclock.ForSpace` 只认 crypto（`periodclock/clock.go:23`），建任务即拒绝（`catalog/service.go:93`）；本地存储是锚定纪元的定长网格（`seriesstore/grid.go`），A 股的午休、隔夜、周末都会变成填充行，MA20 会跨休市取到填充值；写死 `Continuous{}` 的有 8 处，引擎整个进程只有一个时钟（`engine/runtime.go:135`） |
| 策略 | `cn_stock` 1d 的周期、有效期、触发和日历到期检查 | `market_type` 只接受 spot/swap（`input/resolve.go:321,335`），现货判定只认 `spot`（`:84`）；cn_stock 只支持 1d（`calendar.go:400-413`）；A 股专用的上海日期换算散在 `depth.go`、`range.go`、`calendar.go` |
| 交易 | — | 只有 Binance、OKX、Paper。本方案不涉及 |

## 2. 总体做法

1. **只用一份交易日历。** `packages/marketcalendar` 是唯一来源，collector 自带的 `calendar.yaml` 删掉。
2. **只用一套分桶规则。** A 股的周期由 `barclock` 统一计算（桶的起点、终点、交易序号），采集过滤、完整性检查、因子、策略都调用它，不再各写一份。
3. **A 股 5m 桶本身对齐纪元。** 两个交易时段的开盘 09:30、13:00（北京时间）是 01:30Z、05:00Z，都是 5 分钟的整数倍；每根 5m 的终点就是“开始 + 5 分钟”。因此纪元算法下的这些环节都不用改：采集触发（`frequencyDue` 按 5 分钟取整）、K 线校验、Storage 周期与截止时间、快照生效点、补采扫描上界。采集侧只需判断某根桶是不是交易时段内的桶。
4. **因子使用交易序号时间轴（3.3）。** 真实时间只在因子引擎的三层边界上换算，本地存储、窗口、填充、长缺口阈值和连续水位都原样复用。
5. **策略的周期推进与 BarIndex 改用 barclock**，与因子共用同一份交易序号。

## 3. 设计

### 3.1 共用基础：日历与 barclock

- **日历续期不在本方案内。** 内嵌日历现在覆盖到 2026-12-31，2027 年按年度日历更新处理：更新 `packages/marketcalendar/data/cn_trading_days.json` 与 `manifest.json` 的 `valid_through`，再重新构建各组件和 SCF 代码包。统一成一份日历后，每年只更新这一处。现有预检要求覆盖期至少还剩 14 天（`bootstrap/fetch_plan.go:235`），所以年度更新须在 12 月中旬前完成。
- **日历接口。** marketcalendar 已有 `TradingDayIndex`（`calendar.go:230`），新增反向接口 `TradingDayAt(index)`。
- **barclock 新增两个接口**（现有接口保持原名、原签名；日历 ID 直接用 `Market.CalendarID` 字段）：
  - `Ordinal(b Bucket) (int64, error)`：桶的交易序号，等于“交易日序号 × 每日根数 + 日内序号”。A 股没有半日市，每个交易日根数固定（5m 为 48 根），所以是 O(1) 运算；
  - `BucketAtOrdinal(n int64) (Bucket, error)`：由交易序号反查桶；
  - 两者超出日历覆盖范围时都返回 `ErrOutOfCoverage`。
- **collector 改用这份日历。**
  - 删除 `modules/collector/config/markets/stockcn/calendar.yaml`，以及 `markets/stockcn/calendar.go` 里按 yaml 判断交易日的逻辑；交易分钟、交易桶的判断改由 barclock 计算。
  - SCF 打包脚本与 `collectorpackager/scf.go` 不再打入 calendar.yaml。
- **年度运维。** 日历更新写进 skill 的运维条目，到期告警保留现有的 60 天提前量。

### 3.2 采集

#### 3.2.1 数据源与复权

- **主备关系。** 5m 以东方财富（`klt=5`）为主、通达信为备，存不复权 OHLCV。
- **为什么用不复权。** 通达信请求写死不复权（`packages/tdx/transport.go:355`），东方财富也取不复权（`fqt=0`），主备切换时口径不会混。
- **已知限制。** 除权日前后，跨越除权的窗口里因子会有一次跳变。这是此前定下的“日内不复权”口径，单用户可以接受。
- **东方财富 provider 的改动：**
  - 放开频率限制（`:114`），`KlineSpec.Frequencies` 加入 5m（`:82`），否则候选链检查直接判为不支持（`feed_guard.go:383`）。
  - 请求按频率选 `klt`：1m 用 `klt=1`，5m 用 `klt=5`。
  - `NormalizeMinuteKline`（`stockcn/common.go:116`）把写死的 1 分钟改为按请求频率取周期，`Frequency` 也写请求频率。
  - **时间标签与单位要实测，两家都要。** 编码时用真实返回逐项确认，并录成测试样例：
    - 东方财富 5m 的时间标签代表开盘时刻还是收盘时刻；现有代码按开盘时刻处理（`TimestampModeOpen`）。
    - 第一根是否包含 09:25 的集合竞价。
    - 通达信 5m 的时刻代表 K 线的开始还是结束。现有代码直接当开盘时刻用（`tdx/provider.go:203,223`），协议层只解码到时分（`packages/tdx/parse.go:129-138`），现有测试用的是人造时间。
    - 成交量单位：东方财富乘 100 换算成股（`eastmoney/provider.go:202`），通达信直接取原值，两家是否一致。

    标签一旦错位，整条序列会错一根；单位不一致，主备切换时成交量会跳变。
  - 金丝雀请求的覆盖区间（`kline_pipeline.go:202-208`）现在用 `LatestClosedMinute` 计算，5m 请求会拿到 1m 的区间，要改为按频率取 barclock 桶。
- **回看能力按频率声明。** 现在回看窗口按数据源声明，东方财富只有一份 24 小时（`:89`），候选链也按这一份判断（`feed_guard.go:383,395`、`kline_pipeline.go:499`）。改为按频率给出：东方财富 5m 的回看按实测的单页上限声明；通达信 5m 保持 365 天。超出东方财富回看窗口的补采自动落到通达信，两家口径一致。

#### 3.2.2 路由与流水线

- **路由。**
  - `route.yaml` 改为按频率分路由：1m 维持现状；5m 为东方财富主、通达信备。
  - `StockCNRouteID` 不再是常量，改为按频率生成。
  - `stock_route.go:77` 的 1m 校验和 `metrics.go:401` 的路由白名单同步修改。
- **流水线。**
  - 去掉 `kline_pipeline.go:65-68` 的 1m 限制和 `:767` 写死的频率。
  - `:96` 的 `stockCNShouldCollectMinute` 只用于 1m 实时批，5m 由下面的周期过滤负责，不改。
- **周期过滤。** `stockCNPlanPeriodValid`（`marketwiring/handler.go:57-79`）现在只放行 1m，改为对 5m 用 barclock 判断：周期开始时刻必须是交易时段内某根 5m 桶的键。
- **任务能力表。** `jobs/kline/definition.go:55` 增加 `eastmoney equity 5m`。
- **完整性检查与补采。** 完整性检查的日历过滤（`bootstrap/fetch_plan.go:285-305`）现在遇到非 1m 就报错，改为对 5m 用 barclock 过滤交易桶。补采扫描的上界公式是纪元算法，对 5m 本来就正确，只需让应有时间点经过同一个过滤。

#### 3.2.3 触发

- **cron 不用改。** `planCron`（`plan_desired.go:432`）对最小周期 5m 生成 `0 */5 * * * * *`，每天触发 288 次，其中 48 次落在交易时段；其余次数函数经周期过滤后返回 `no_work`，不建周期。
- **到期判断不用改。** `frequencyDue`（`marketfetch/plan.go:283`）按 5 分钟取整，返回刚收盘那一根：
  - 11:30 触发采 11:25 那根，15:00 触发采 14:55 那根；
  - 13:00 触发对应的 12:55 不是交易桶，被过滤掉。
- **迟到时间**沿用 stockcn 的默认值 90 秒（`domain/collect_params.go:295`）；结算延迟和错峰窗口沿用现有配置。

#### 3.2.4 结果数据集属性

- `taskresult.Manager.Ensure`（`planner/taskresult/result.go:521`）补写 `calendar` 和 `alignment`，取值来自 `barclock.MarketFor(space)`，stockcn 分别为 `cn_stock`、`session`。
- `market_type` 已经从标签写入，不用改。
- 策略按 `calendar` 选日历（`input/resolve.go:55-71`），没有这个属性会退回 `crypto_24x7`。

#### 3.2.5 沪深 300 成分标签

新增自动标签 `cn_hs300`，由 Collector 的标签同步按接口定时拉取沪深 300 成分股，不手工维护名单。

**现状**
- 标签同步（`subjectsync/tag_runner.go`）按标签属性 `sync.source` 和 `market_type` 找 lister。lister 的 `List(ctx)` 不带参数，只能返回某个数据源的全部标的。
- 写入用 `ApplyTagSnapshot`。标的不存在时会先建标的（`metadata/sqlite/crud_tag_member.go:234-240`），所以这个标签不依赖 `cn_a_share` 先同步。
- 拉取失败会重试；都失败就不应用快照，保留上一次的成员（`tag_runner.go:165-196`）。

**标签属性**

```yaml
- space_id: stockcn
  tag_id: cn_hs300
  tag_name: 沪深300
  mode: auto
  builtin: true
  attributes:
    market_type: equity
    sync.source: eastmoney
    sync.index: "000300"        # 新增：有这个属性时，同步该指数的成分股，而不是数据源的全部标的
    sync.cron: "40 8 * * *"     # 每天开盘前同步一次
    sync.timezone: Asia/Shanghai
```

**改动**
- `marketdata` 新增接口 `IndexConstituentFetcher`：`FetchIndexConstituents(ctx, indexCode) (InstrumentSnapshot, error)`。
- 东方财富 provider 实现这个接口：复用现有的 `clist` 标的列表请求，把筛选条件换成该指数的成分板块。
  - 指数代码到板块的对照只登记 `000300` 一项。端点和对照在编码时实测确认，并把一次真实返回录成测试样例。
  - 标的 ID 的生成与 `cn_a_share` 共用同一个函数（`stockcn/common.go`）。
- 返回条数必须等于接口自己报的总数。翻页不完整就报错、不应用快照；返回为空按现有的 `ErrEmptySnapshot` 处理。这样代码里不用写死 300。
- `tagattr` 增加 `Index(tag)`。
- **成分 lister 单独成表，不放进现有的 `Listers`。** `ListerKey` 只有 `{Source, InstrumentType}`（`lister.go:18-21`），而 `Listers` 还有三处把它当全量列表用：属性同步（`attr_runner.go:92`）、管理台“新建自动标签”的数据源选项（`rpc/subject_sync.go:89-101`）、启动时的数据源检查（`bootstrap/subject_sync.go:43`）。
  - 所以 `TagRunner` 新增一张 `IndexListers`，按 `(source, index)` 索引，由 `subject_listers.go` 装配。
  - `runTag` 看到 `sync.index` 时从这张表取，`FetchSnapshot` 和 `Listers` 都不改。

**调样**
- 沪深 300 每年 6 月、12 月各调整一次样本。每天 08:40 的同步拿到新名单后，计划 hash 随之变化，下一轮对账生成新计划，从新的生效期起按新名单采集（5m 的生效期就是几分钟之后）。
- 调出的股票不再采集。因子侧这些标的不再有新数据，超过长缺口阈值后标记为过期。
- 策略的 universe 以 View 的活跃序列为准。编码时核对调出的股票多久离开 universe，并写进验收。

#### 3.2.6 Monitor 休市时的停滞判断

“5m 桶对齐纪元”只在采集和 Storage 两侧成立，Monitor 的运行停滞判断不成立：

- **怎么判的。** 条件是 `now - LastRunAt > run_missed_intervals × 间隔 + 30s`（`monitor/internal/observability/overview.go:949,991-1001`）。`run_missed_intervals` 为 2（`config/setup/dataset-health-policy.yaml:4`），所以 5m 的阈值是 10 分 30 秒。
- **为什么会误报。** 采集侧（`collector/internal/observability/realtime_inventory.go:138-158`）和因子侧（`factor/internal/bootstrap/dataset_observer.go:104-110`）上报的间隔都是连续时间。每天午休、收盘后和周末，stockcn 的采集与因子数据集都会被判为停滞，并发出告警（`monitor/internal/bootstrap/business_freshness.go:105`）。
- **改法。** 只改 Monitor 一处：`datasetStatus` 在时钟是交易所日历、且当前没有正在等待的 K 线（`bars.waiting == 0`）时，跳过运行与成功两项停滞判断。
- **不受影响的部分。** K 线新鲜度已经按交易时段切桶（`market_clock.go:177-186,241-251`），休市时不会判逾期。

#### 3.2.7 SCF 部署

- 在 `[[scf_plan.spaces]]` 增加 stockcn。
  - 地域取存储机所在的南京，与带 access 组件的存储机同地域，走私网。
  - 代码包为 `moox-collector-stockcn-plan`。
- 300 只按每函数 40 只分配，共 8 个函数，每 5 分钟触发一次，不涉及命名空间和并发配额。
- 验收覆盖开盘、午休、收盘和非交易日。

### 3.3 因子：交易序号时间轴

#### 3.3.1 轴的定义

在轴上，A 股的交易 K 线排成一条没有休市的连续序列。以下现有机制都不用改：

- 本地存储的网格与块布局；
- 整段替换与填充；
- 内存窗口的“最近 N 根”；
- 长缺口阈值；
- 连续水位；
- 跳过账本。

**定义**
- 轴上时间 = 纪元 + barclock 交易序号 × 周期。crypto 的轴就是真实时间本身（恒等）。
- `Session` 时钟按 barclock 实现，能处理 1m、5m、15m、30m、1h、1d，以及其他能整除两个交易时段的周期；4h 这类不能整除的拒绝。本方案的测试和验收只覆盖 5m。

**含义变化**
- 长缺口阈值（`localdata.go:971`）原样按根数使用。5m 是 289 根，在轴上约等于 6 个交易日，不再是“24 小时”。
- 轴上时间直接打进日志会显示成 1970～1990 年代。日志和告警里的周期一律先换回真实时间再输出。

**表达式不受影响。** 表达式不读取时刻；`cs_rank` 按 `data_time` 分组，而轴上时间与真实时间一一对应。

#### 3.3.2 时钟接口

`periodclock.Clock` 新增三个方法：

```go
// ToAxis 把真实的桶起点换成轴上时间；t 不是该频率的桶起点时返回错误。
ToAxis(t time.Time, freq string) (time.Time, error)
// FromAxis 把轴上时间换回真实的桶起点。
FromAxis(a time.Time, freq string) (time.Time, error)
// AxisEnd 返回真实时刻 t 在轴上的排他终点：收盘不晚于 t 的最新一根的轴上时间 + 周期。
// 用于“当前时刻”和各种排他终点（区间终点、末根 + 周期）。
AxisEnd(t time.Time, freq string) (time.Time, error)
```

**`Continuous` 的行为**
- 三个方法都是恒等换算，其中 `AxisEnd` 返回 t 向下对齐后的值。
- 这与现有的取整类用法等价：
  - `localdata.go:376` 的 `now.Truncate(step)`；
  - `catchup_life.go:100` 的 `now.Add(-grace).Truncate(step)`，即 `AxisEnd(now-grace) - step`；
  - `localdata.go:1004` 的整数根数。
- 不等价的是连续秒数类用法，例如 `runner.go:66` 的 `time.Since(periodTime)`。这类地方不用 `AxisEnd`，见 3.3.3 的“滞后”一条。

**`Session` 基于 barclock 实现。**

**时钟按空间取，不再是整个进程一个。**
- 引擎运行时（`engine/runtime.go:135`）注入的不再是单个 `Clock`，而是 `periodclock.ForSpace`。
- localdata、pipeline、runonce、reconcile 都改为按任务所在空间取时钟，这样同一台引擎可以同时跑 crypto 和 stockcn。
- `ForSpace` 对未登记的空间仍然报错。

#### 3.3.3 换算边界

换算收拢到三层。层内一律用轴上时间，业务代码里不手写序号运算。

| 层 | 位置 | 做什么 |
| --- | --- | --- |
| Storage 客户端 | `storageio` 的读行、`UpsertFields`、周期上报、完成标记查询（`marker.go:57,67`）；`pipeline/storagedata.go` 直接读窗口；`seriessync` 同步 | 读进来 `ToAxis`，写出去 `FromAxis`；区间终点用 `AxisEnd` |
| 事件入口 | 周期完成事件（`trigger/eventconsumer/handler.go`、`continuity.go`）、补采通知 `NotifyPeriodsFilled` | 周期 `ToAxis`，区间终点 `AxisEnd` |
| 引擎对外接口 | 心跳和 RecentRuns（`engine/runner.go:66-69`）；引擎 HTTP `/v1/series/write\|subject\|bounds`；重算 RPC（`rpc/service.go:448`）与 `recalcexec`；导入（`localdata`、`packages/seriesimport/runner`）；回放（`engine/replay.go:259`、`packages/seriesreplay`）；预览；CLI | 入参 `ToAxis`/`AxisEnd`，出参与上报 `FromAxis` |

**引擎内和“当前时刻”比较的地方**，统一改为 `AxisEnd(now)`，不再直接拿 `now` 与轴上时间相减：
- 长缺口判“标的已经很久没有新数据”（`localdata.go:1000-1006`）；
- 追赶目标（`engine/catchup.go`、`catchup_life.go`）；
- 重算循环（`engine/recalcloop.go:244`）。

**另外三处单独处理：**
- **滞后。** 引擎在心跳里新增“落后根数”字段，定义为 `(AxisEnd(now) - 最后一期轴上时间) / 周期 - 1`：算完最新已收盘的一期时为 0，收盘后、结果写出前为 1。
  - 管理端 `enginehub/hub.go:174` 改为由这个值换算 `LagSeconds`（根数 × 周期），前端（`web/src/api/factor/types.ts:192`）继续按秒显示，接口字段不变。
  - 管理端不再自己用 `now` 减最后一期的开始时间。按真实时间算，A 股午休时滞后会涨到约 90 分钟，周末约 2.5 天。
  - `runner.go:66` 的 `PeriodLag` 秒数指标保留真实时间语义，先 `FromAxis` 再计算。
- **参数生效比较**（`engine/params.go:146`）：比较的两边是管理端存的真实时间和本期周期。先把本期周期 `FromAxis` 换回真实时间，再比较。
- **冻结区间清理点**（`catalog/categories.go:72`）：位于管理端，用真实时间。改为用 `Clock.Window` 按交易 K 线往回数 N 根。

**8 处写死的 `Continuous{}`** 全部换成按空间取时钟：

- `engine/runtime.go:135`
- `engine/runonce.go:71`
- `engine/reconcile.go:66,124`
- `preview/preview.go:87`
- `pipeline/storagedata.go:125`
- `bootstrap/dataset_observer.go:76`
- `cmd/cli/commands.go:353`

**管理端与引擎的分工。**
- 管理端（catalog、recalc 服务、观察器）面对的是用户输入和 Storage 里的真实时间，只用 `Align`、`Window`、`Duration` 这些真实时间语义。
- 引擎内部只用轴上时间。
- 两侧的函数不混用。

**编码时的约束。**
- 先在 `modules/factor` 里 grep `time.Now`、`.Sub(`、`Truncate(`、`seriesstore.Step`，逐一标注它属于哪一层。
- 编码完成时，三层之外不允许再有真实时间与轴上时间的混算。
- 上表是审查时找到的位置，最终以 grep 结果为准。

#### 3.3.4 保留期与历史

- 因子任务只有 `ma__5`、`ma__10` 两个成员，最大回看 10 根，即 50 分钟交易时间。上线后第一个交易日的上午就能出结果，不需要导入历史。
- 不加 EMA 等长回看因子。默认 EMA 最大回看 49995 根，按 A 股 5m 每天 48 根算要 4 年多的历史，而且要把保留期改成 `forever`；以后需要时再加。
- 保留期沿用现在的 `storage_retention.spaces.stockcn."5m" = "300d"`，装得下。
- `setup factors` 会核对保留期能否装下最大回看（`cli/internal/command/setup_retention.go:58-60` 的 `policy.RetentionBars`），这里只差 10 根，不会触发。`RetentionBars` 对 stockcn 是否按交易时段折算，留到以后加长回看因子时再核对。

#### 3.3.5 不采用的做法

不把约 45 处 `t.Add(step)`、`(b-a)/step` 改成 `clock.Next`、`clock.Count`，也不改写 `seriesstore` 的块布局去直接存真实时间。那样存储、窗口、填充和长缺口逻辑内部都要感知日历，crypto 的热路径也得跟着改，改动面和出错面都比只改边界大。

### 3.4 策略

**市场类型**
- `sourceMarketType`（`input/resolve.go:321,335`）接受 `equity`。
- 现货判定（`:84`）改为 `marketType == "spot" || marketType == "equity"`，A 股同样受现货的三条限制：不许 tranche、杠杆不大于 1、不许做空。

**账户**
- `tradeowner.checkMarket` 不改：A 股实例绑定账户时，它已经会报“市场 equity 不受支持”。
- 不绑账户的实例照常求值、记录结果，不发目标（`trigger/handler.go:515` 已经如此）。
- 前端账户下拉框（`logical-account-options.ts:3`）遇到 equity 显示为空，并提示“A 股暂无交易账户”。

**日历与周期**
- `CheckCalendar` 对 cn_stock 支持 barclock 允许的日内周期（本方案用 5m）。
- **cn_stock 1d 的现有代码原样保留。** 不删，也不换成 barclock：barclock 的 1d 键是 UTC 零点（`packages/barclock/clock.go:6`），策略要求上海零点（`calendar.go:369`），两者不一致。
- **新增一条“cn_stock 日内走 barclock”的分支**，各周期函数都加上。下面这些函数现在只有“cn_stock 且 1d”和 crypto 两条分支，其余情况一律报 `ErrUnsupportedCalendar`：
  - `FromStorageStart`（`calendar.go:118-155`）：触发入口（`trigger/handler.go:239`）调用它，不加分支的话 5m 每一期都会被丢弃；
  - `FromBarEnd`（`:158-187`）；
  - `AdvanceBarEnd`（`:218-258`）：现在只要不是 crypto 就按交易日推进，5m 会得到次日 15:00，而且不报错；
  - `PreviousStorageStart`（`:260`）；
  - `HistoryStart`（`:269-306`）；
  - `ClosedPeriod`；
  - `FloorStorageStart`、`CeilStorageStart`（`:308,340`）。
- `depth.go:61,87,104`（写死的 `+15h`）和 `range.go:22-36,68-82,148-165` 同样只加日内分支，1d 代码不动。
- 有效期例子：有效期是 `AdvanceBarEnd(BarEnd, 2)`（`ValidBars = 2`，`trigger/handler.go:33,247`）。11:25 那根的 BarEnd 是 11:30，往后推两根是 13:10。
- 5m 的行键是真实的开盘时刻（UTC），不经过 `checkStockRowKey`。
- 策略观察器的 cn_stock 动态间隔（`strategy/internal/bootstrap/observer.go:305-330`）基于 `AdvanceBarEnd`，补上日内分支后，5m 也自然正确。

**BarIndex**
- 持有期、冷却期按 BarIndex 计数（`engine/frame.go:26`）。cn_stock 的 BarIndex 改为 barclock 交易序号，与因子的轴一致，午休、隔夜不计入根数。

**保留期与年化**
- 保留根数（`resolve.go:359-360`）按 barclock 的每日根数折算，不再一律按日线算。
- 回放年化（`replay/metrics.go:52-55,151-154`）改为 244 × 每日根数，5m 即 244 × 48。

**回放**
- 逐期推进在 `packages/seriesreplay/runner.go:127-145`，归因子步骤（T2），按 barclock 的桶推进。
- 策略侧只改 `replay/metrics.go` 的年化和 BarIndex。

### 3.5 配置、seed 与一键编排

**`config/setup/metadata.yaml`**
- 新增自动标签 `cn_hs300`，属性见 3.2.5。
- 删除没有写入方的种子数据集：
  - `dataset_stockcn_equity_kline_1m`（结果数据集由任务派生）；
  - 指数 1d、可转债 1m 的占位数据集。

**`config/setup/collection-tasks.yaml`**
- stockcn 只留一条任务“A 股 K 线 5m”，绑定 `cn_hs300`，`collect_params` 设 `frequency: 5m`。
- 删除原来的“A 股 K 线 1m”任务。

**`moox.toml.example`**
- 增加 stockcn 因子任务 `ftask_cn_5m`，只有两个固定成员：`{ factor = "ma", params = { window = 5 } }` 和 `{ factor = "ma", params = { window = 10 } }`。
- 增加 `[[scf_plan.spaces]]` 的 stockcn 示例。
- 保留期不改（3.3.4）。

**示例策略与一键编排**
- `examples/strategies/` 新增 `dual_ma_cn_5m.yaml`：照 `dual_ma_swap_1m.yaml` 改为 A 股 5m 现货口径，规则仍是 `ma__5 > ma__10` 的等权持有，不绑账户。这是 stockcn 唯一的示例策略。
- `setup start --space-id stockcn`：`setup_start.go:24-28` 的默认策略改为按空间选择。流程与 crypto 相同：
  1. 启用任务；
  2. 立即同步一次 `cn_hs300`（与 `moox-cli collector subjects sync` 同一入口），确认成员数大于 0；
  3. 等待计划；
  4. 导入因子任务；
  5. 创建并启用策略实例。

**skill（`skills/moox`）**
- 补充 stockcn 空间的启用步骤，以及日历的年度更新步骤。

## 4. 执行计划

| 步骤 | 内容 | 依赖 | 完成标准 |
| --- | --- | --- | --- |
| T0 | 日历统一；barclock 和 marketcalendar 新接口（3.1） | — | collector 不再读 calendar.yaml；`Ordinal` 与 `BucketAtOrdinal` 能往返，覆盖跨午休、周末、国庆和日历首尾；现有测试通过 |
| T1 | 采集：东方财富 5m、路由、周期过滤、完整性检查、数据集属性、沪深 300 标签；Monitor 休市时跳过停滞判断（3.2） | T0 | 东方财富 5m 与通达信 5m 在样例上逐根一致，时间标签与成交量单位已实测；午休、隔夜、周末 Monitor 不报停滞；交易时段每 5 分钟一期，午休、收盘后、非交易日返回 `no_work`；完整性检查与自动补采支持 5m；结果数据集带 `calendar`、`alignment`；`cn_hs300` 同步出与接口总数一致的成员，拉取失败时保留旧成员 |
| T2 | 因子交易序号时间轴，含因子回放（3.3） | T0 | crypto 全部测试不变；stockcn 5m 的 `ma__10` 等于最近 10 根交易 K 线的均值，窗口跨午休、隔夜、周末时没有填充行；心跳落后根数与长缺口判断正确；三层之外没有真实时间与轴上时间的混算 |
| T3 | 策略：equity 按现货、cn_stock 日内分支（3.4 列出的全部周期函数）、BarIndex、保留期与年化、前端账户下拉框（3.4） | T0 | stockcn 5m 实例能启用；周期边界、有效期、BarIndex 与 barclock 一致，跨午休推进正确（11:25 那根的有效期到 13:10）；cn_stock 1d 现有测试不变；回放与实时结果一致 |
| T4 | seed、配置样例、示例策略、`setup start`、skill（3.5） | T1～T3 | `setup start --space-id stockcn` 一次跑完 |
| T5 | 上线与验收 | T4 | 见第 5 节 |

- **可以并行。** T0 完成后，T1（collector）、T2（factor、`packages/seriesreplay`、`packages/seriesimport`）、T3（strategy、web）改的文件不重叠，可以并行。共用的 barclock、marketcalendar 接口在 T0 完成，T1～T3 只调用、不修改。
- **每一步的流程**按项目惯例：编码 → 独立审查 → 本地测试与 CI → 合入。

## 5. 上线与验收

**上线方式**
- stockcn 相关组件换新版本：Collector、因子引擎、Strategy、Monitor、web-host。Storage 不需要改。
- 上传 stockcn 代码包。
- `moox.toml` 增加 stockcn 的 `scf_plan`、因子任务和保留期。
- 执行 `setup start --space-id stockcn`。
- stockcn 没有旧数据，不做迁移。

**验收**：在两个连续交易日加一个非交易日内完成，逐项核对。

1. **标签**
   - `cn_hs300` 成员数与接口报的总数一致（正常为 300）；与 `cn_a_share` 重合的股票，标的 ID 一致（`cn_a_share` 目前不含科创板，见第 6 节）。
   - 每天 08:40 同步一次。人为让接口失败时，成员保持不变。
2. **5m 采集**
   - 每个交易日 48 期：09:30～11:25、13:00～14:55，齐全率不低于 99%。
   - 午休、收盘后、非交易日函数返回 `no_work`，不建周期；这些时段 Monitor 不报采集、因子数据集停滞。
   - 抽 3 只股票，与东方财富网页的 5m K 线逐根一致，包括第一根和 11:25、14:55 两根。
3. **因子**
   - 在源周期完成后 1 分钟内写出结果。
   - 抽 3 只股票，用相同 K 线离线计算 `ma__5`、`ma__10`，结果逐位一致。
   - 窗口跨午休、隔夜、周末时没有填充行（`is_filled=0`）。
   - 心跳上报的落后根数不超过 1，午休、隔夜、非交易日也一样。
4. **策略**
   - 示例实例每个交易日产出 48 期结果；11:25 那期之后的下一期是 13:00 那期。
   - 回放结果与实时结果一致。
5. **停牌**
   - 停牌股的检查报缺，补采不反复重试。
   - 因子按填充规则计算。

## 6. 扩到全 A 的前置条件

不在第一版范围内，扩容前必须完成：

- 完成 SCF 执行计划第 31 条延后项：单个周期大面积缺失时，按标的分批入队补采。
- 核对南京地域的函数数、命名空间与并发配额：按每函数 40 只计算，全 A 约 130 个函数，每 5 分钟触发一次。
- 复测通达信容量（每函数 40 只是旧的实测值）。
- `cn_a_share` 的列表请求补上科创板。它现在只取深主板、创业板、沪主板（`eastmoney/provider.go:242`），换标签后沪深 300 里的科创板股票会掉出采集范围。
- 任务的标签从 `cn_hs300` 换成 `cn_a_share`。

## 7. 已定事项

| 事项 | 决定 |
| --- | --- |
| 频率 | 默认 5m，其他与加密货币空间相同（v5） |
| 复权 | 日内不复权（5m 东方财富与通达信口径一致） |
| 标的范围 | 沪深 300 成分股，自动标签，按接口定时同步 |
| 2027 年日历 | 不在本方案内，到时按年度日历更新处理 |
| 日线、1m、K 线合成 | 先不做 |
| 链路终点 | 策略结果，不绑账户 |
| 因子与示例 | 只算 `ma__5`、`ma__10`，只给双均线示例；EMA 等长回看因子先不算 |

## 8. 修订记录

**v4 → v5**

主频率由 1d 改为 5m。A 股 5m 的桶对齐纪元（见第 2 节第 3 条），v4 里专为日线准备的下列内容都不再需要，已删去：

- 日线时间键方案 B 的实施；
- 采集侧按 barclock 判断收盘；
- Storage 的 `period_end_at` 与 1800 秒迟到时间；
- 每天一次的 cron；
- 日线自动补采的上界改造；
- 后复权日线。

新增或改动的内容：

- 东方财富 5m 与通达信 5m 互为主备，均不复权；
- 时间标签实测；
- 策略要支持 cn_stock 日内周期、BarIndex 和年化；
- 因子任务只放 `ma__5`、`ma__10`，示例只有双均线策略；不算 EMA 等长回看因子，保留期不改（用户决定，避免初次体验时卡在拉取历史上）。
- v5 复审补充：
  - Monitor 休市时跳过停滞判断（3.2.6）；
  - 策略侧 cn_stock 日内分支补全周期函数清单，1d 代码不动（3.4）；
  - 时间标签和成交量单位的实测扩到通达信；
  - 金丝雀区间按频率计算；
  - 有效期例子更正为 13:10。

沪深 300 标签、因子交易序号时间轴、按空间注入时钟沿用 v4。

**v1 → v4**

- 三轮独立审查。仍然有效的结论已并入正文：
  - 换算收拢到三层；
  - `AxisEnd` 的等价范围；
  - 8 处 `Continuous{}`；
  - 滞后定义；
  - equity 按现货；
  - 成分 lister 单独成表；
  - `cn_a_share` 缺科创板。
- 只针对日线的结论随 v5 删去。
