# K 线合成设计

状态：设计稿 v2（2026-10-22），尚未编码。项目尚未上线，接口与数据约定直接修改，不考虑兼容。
v2 在 v1（支持 45m 等非标准周期）的基础上，补充了触发与执行方式的重写：以事件为主路径、按根数做兜底检查、新增补采完成事件、
提供历史合成 CLI。实现后以[采集](../模块/采集.md)第 7 节为准。

## 结论

- **合成功能已经存在，继续作为采集模块的子模块 `collector/internal/resample`**，不放进 `collector-stream`，也不拆成独立模块（第 2 节）。
  合成的输入输出都是 Storage 里的事实数据，与采集方式无关。
- **支持 45m 的关键在频率体系**：`packages/frequency` 从写死的枚举改为“`<N>m` / `<N>h`、整除一天、唯一规范形式”的语法，
  原来的 10 个值保留为标准档（第 3 节）。创建任务时校验：目标能被源整除；A 股等交易时段市场按交易日历分桶，只允许能整除每个交易时段的周期，除不尽的（如 A 股 45m）直接拒绝（第 4 节）。
- **触发与执行重写**（第 5 节）：
  - **事件是主路径**：订阅源数据集的 `CollectorPeriodCompleted`。收到桶内最后一个源周期的完成事件后，按“数据集 × 桶”一次批量合成全部标的。
  - **兜底检查**：每分钟第 5 秒运行，只看最近 N 根已收盘的目标 K 线，按 Storage 里的目标数据找出缺的标的并补上；自身不保存状态。
  - **补采完成事件**：补采分块确认后由 Collector 经本地 outbox 发出 `event.collector.kline.backfilled`，不带行情数据，带数据集同步点。
    合成订阅它，重算受影响的桶；因子也可以订阅它。
  - **历史合成**：按任务提交作业，`moox-cli collector resample` 发起、查看、取消；补采完成事件也提交同一种作业。
  - **控制台**：合成任务留在“采集任务” tab；新建时选源采集任务，源周期和序列标签由后端推导并带出；行上可“新建合成”；历史合成用抽屉；结果同样展示在“采集结果” tab（第 6 节）。
  - **不加锁**：各路径都可重复执行，写入幂等；源数据被改写时一定会触发事后重算，结果最终一致。
  - **删掉的部分**：逐标的的 TaskInstance 状态机（认领、租约、CAS、`waiting_source`）、WriteTarget、Collector 本地的 PeriodReadiness，
    以及基于 `source_hash` 的修复扫描。目标数据集的周期由 Storage 跟踪，为此要放开 Storage 周期接口的准入（只改准入判断）。
- **时延**：WS 源数据下，45m 收盘后约 1～3 秒写入。现在要 60 秒左右：定时器在整分钟触发，而认领条件要求过了 10 秒结算延迟，每个桶都得等到下一分钟。
- **顺带发现一个现有 bug，要先修**：合成任务在创建时和 Collector 启动时用的是两套校验，后者要求结果数据集 ID 以 `_<频率>` 结尾，
  任务 ID 不带这个后缀时 Collector 会起不来（第 8 节）。

## 1. 现状与问题

```text
CollectionTask(data_type=kline_resample)
  │  collect_params: source_dataset_id / source_frequency / source_series_tag / target_frequency / alignment=epoch_utc
  ▼
Preparer（异步）      建目标 Dataset 与默认 View（Catalog.PrepareTarget），prepare_state → ready
  ▼
Runner.Tick（cron "0 * * * * *"）
  ├─ PlanTask：源数据集每个标的一份 TaskInstance + WriteTarget
  ├─ ensureReadiness：为目标周期建 PeriodReadiness
  ├─ 认领 realtime → repair → backfill（每轮最多 100 个“标的 × 桶”，2 个并发）
  └─ processClaim：读一个标的的源窗口 → resample.Bars → 写一个标的 → 标记就绪
```

| 问题 | 原因 |
| --- | --- |
| 时延固定在 60 秒左右 | 定时器在第 0 秒触发，认领条件要求 `收盘 + 10 秒结算延迟`，收盘那一刻的扫描永远认领不到 |
| 标的多时要排好几分钟 | 执行单位是“一个标的的一个桶”，每个都单独读一次、写一次；每轮上限 100 个 |
| 补采改了源数据不会重算 | 补采走普通写入，不发周期完成事件；只有扫描最近 3 个桶的修复流程能碰巧发现 |
| 只支持标准档周期 | 频率枚举写死（第 3 节） |

## 2. 模块归属

| 选项 | 结论 | 理由 |
| --- | --- | --- |
| `collector/internal/resample` | **保留** | 复用 CollectionTask、Catalog 与“一任务一结果”约定；补采作业也在 Collector 内，补采完成事件由这里发出；控制台与 CLI 的任务管理已接通 |
| 放进 `collector-stream` | 不采用 | 合成的输入是 Storage 里的事实数据，与采集方式（SCF 拉取、WS 推送、本地拉取、补采）无关；stream 只接收 crypto，部署在海外机，没有 Catalog 与补采 |
| 拆成独立模块 | 不采用 | 依赖的任务、补采状态都在 Collector 库里，拆开只多一层 RPC 和一套部署 |

## 3. 频率体系改造（`packages/frequency`）

### 3.1 规范语法

| 类别 | 规范写法 | 约束 |
| --- | --- | --- |
| 秒级 | `30s` | 只此一个，不在本次范围 |
| 分钟级 | `<N>m` | 1 ≤ N < 1440，`1440 % N == 0`，且 `N % 60 != 0` |
| 小时级 | `<H>h` | 1 ≤ H < 24，`24 % H == 0` |
| 日、周、月 | `1d`、`1w`、`1mo` | 不变 |

- **唯一规范形式**：能写成整小时的一律写成小时。`60m → 1h`、`120m → 2h`、`240m → 4h`、`1440m → 1d`、`24h → 1d`。
  这些写法只作为输入别名，由 `Parse` 归一，`IsCanonical` 对它们返回假。否则 `60m` 和 `1h` 会在 Storage 里成为两条序列，悄悄分叉。
- **数字不带前导零**：`05m` 拒绝。
- **大小写**：规范形式只用小写 `m`、`h`。`<N>H` 作为小时的输入别名接受，与现有的 `1H`、`4H` 一致。
  大写 `M` 只表示月（`1M`），`45M` 直接拒绝。现有别名 `60M` 删除，消除歧义。
- 一天能被整除的日内周期是有限集（1440 的 35 个真因子）：
  `1 2 3 4 5 6 8 9 10 12 15 16 18 20 24 30 32 36 40 45 48 72 80 90 96 144 160 288` 分钟，加上 `1h 2h 3h 4h 6h 8h 12h`。
  不能整除一天的周期（例如 `7m`、`50m`、`5h`）会让桶边界逐日漂移，一律拒绝。

### 3.2 接口含义

| 函数 | 改造后含义 |
| --- | --- |
| `Parse(raw)` | 按 3.1 的语法与别名解析，返回规范值 |
| `IsCanonical(raw)` | `raw` 符合语法且已是规范形式 |
| `Duration()` | 由规范值直接计算（不再查表）；`1mo` 仍返回 0 |
| `Standard()` / `StandardStrings()` | 原来的 10 个值，用于下拉候选、保留期表格、错误提示。由 `All()` / `Strings()` **改名**而来，避免被误读成“全部合法值” |

现有测试 `TestParseRejectsUnknownFrequencies` 断言 `2h`、`7m` 会被拒绝，要改成：`2h` 接受，`7m` 拒绝。

## 4. 合成规则校验

规则只写在 `resample.ValidateTaskParams`（及其调用的 `ValidateResamplePair`）一处，所有入口都调用它（见第 8 节），
在**创建、更新任务时**校验，不留到运行时：

1. 源、目标都是 1m～1d 的定长周期（现状）。
2. `target > source`（现状）。
3. **`target % source == 0`**（新增）。例如 30m → 45m 拒绝，5m / 15m → 45m 接受。
   `ValidateResamplePair` 里“更大的目标一定是源的整数倍”这条注释在新语法下不再成立，要一起删掉。
4. **日内目标能整除 1d**：由 3.1 的语法保证，不另写检查。45m 一天 32 根，每天 00:00 UTC 重新起桶。
5. **交易时段市场：日内源周期和目标周期都必须整除每个交易时段的长度**（新增）。除不尽的周期在提交任务时直接拒绝，
   不做跨午休或截短的桶。A 股两个时段各 120 分钟，日内目标只能是 120 的约数：`1m 2m 3m 4m 5m 6m 8m 10m 12m 15m 20m 24m 30m 40m 1h 2h`；
   45m、4h 拒绝，提示“45m 不能整除交易时段（09:30–11:30、13:00–15:00，各 120 分钟）”。`1d` 不受这条限制，按交易日一天一根。
6. **市场必须有交易日历**（新增）。交易时段市场的分桶依赖交易日历与交易时段，没有的在创建时拒绝。目前只内嵌了 A 股 `cn_stock`；
   港股、美股要先补日历。这是数据前提，不按市场类型限制。

源窗口大小不需要新上限：目标最大 1d、源最小 1m，一个桶最多 1440 根，低于现有 `maxSourceBars`（10080）。

### 4.1 分桶规则

分桶方式由源数据集所在市场自动决定，写入任务参数 `alignment`，用户不选：

| 市场 | `alignment` | 日内桶 | `1d` 桶 |
| --- | --- | --- | --- |
| 24 小时连续市场（crypto） | `epoch_utc` | 从 Unix 零点起按目标时长切分 | UTC 自然日 |
| 交易时段市场（A 股等） | `session` | 每个交易日的每个交易时段从开盘起按目标时长切分；第 5 条保证正好切完，不跨午休，不截短 | 交易日，一天一根 |

- **数据键**：`data_time` 取桶的开盘时刻（UTC），与 A 股 1m 的约定一致。A 股 1h 为 09:30、10:30、13:00、14:00（北京时间），与主流行情软件相同。
  `1d` 的键沿用《[日线时间键约定](日线时间键约定.md)》的结论。
- **应有的源 K 线**：桶内按交易时段逐根列出源 K 线的开盘时刻，不含午休和非交易日。
  行情源确认没有的 K 线（Collector 的 `SourceAbsence`，例如无成交的分钟）不计入应有数量；整个桶都没有源数据（例如停牌）时不产出。
- **共享分桶包**：新增 `packages/barclock`。输入市场的交易日历、时区、交易时段与周期，提供“某时刻所在的桶”“某个桶之后的下一个桶”
  “最近 N 个已收盘的桶”“桶内应有的源 K 线”。合成与 Monitor 共用；交易时段的来源与 Monitor 交易所时钟相同，即任务清单声明的日历、时区和时段。
  `epoch_utc` 也由它实现，合成代码不再直接做 epoch 取整。

## 5. 触发与执行

### 5.1 总览

```text
                      ┌───────────────────────────────────────────────────────────────┐
源数据集周期完成       │ 事件主路径：桶内最后一个源周期完成 → 按数据集批量合成            │
CollectorPeriodCompleted ─▶│                                                               │
                      │ 兜底检查：每分钟第 5 秒，最近 N 根目标 K 线，补缺的标的          ├─▶ 目标数据集
  (cron "5 * * * * *") ─▶│                                                               │   （周期提交 / 普通写入）
                      │ 历史合成作业：CLI / 控制台提交，或由补采完成事件提交             │
补采完成事件 ─────────▶│                                                               │
event.collector.kline.backfilled └───────────────────────────────────────────────────────────────┘
```

| 场景 | 由谁处理 |
| --- | --- |
| 源数据集周期完成（实时） | 事件主路径（5.4） |
| 漏掉的事件、degraded 后晚到的源数据 | 兜底检查（5.5） |
| 源数据被补采改写 | 补采完成事件 → 历史合成作业（5.6、5.7） |
| 指定时间段的历史数据 | CLI / 控制台 → 历史合成作业（5.7） |
| 超出兜底窗口、源数据一直缺 | 历史扫描发现，补采源数据后自动重算，或用 CLI 重跑 |

### 5.2 执行单位：数据集 × 桶

三条路径共用一个执行函数：输入为（任务，目标桶，标的列表），输出为写入的行数和跳过的标的。

1. 对全部标的一次批量读出源窗口 `[bucket, bucket + target)` 的行（`ReadRows`，读 Primary）。键数超过 DataNode 单次点读上限（1 万个键）时分片读取。
2. 对每个标的调用 `resample.Bars`。**源 K 线必须一根不少**，不完整的标的直接跳过，不写、不等待、不重试，由调用方决定后续。
3. 合成结果批量写入目标数据集（5.3）。

聚合规则沿用 `resample/bar.go`：

| 字段 | 规则 |
| --- | --- |
| open | 窗口内第一根的 open |
| high / low | 窗口内最大 high / 最小 low |
| close | 窗口内最后一根的 close |
| volume / quote_volume / trade_num | 求和（带溢出与有限值检查） |
| data_time | 目标桶的开盘时刻（按 4.1 的分桶规则） |

### 5.3 写入目标：由 Storage 跟踪周期

目标数据集改用和 K 线采集相同的周期机制，由 Storage 判断一个目标桶是否写满，并发出目标数据集的 `CollectorPeriodCompleted`。
这样下游因子可以直接按事件消费 45m，Collector 不再自己汇总就绪状态。

- **Storage 准入（要改 Storage）**：周期提交与快照登记现在只接受 `dataset_role=raw_collection` 的数据集
  （`primarystore/period.go:172`），合成目标是 `kline_resample_result`，会被直接拒绝。准入要放开到合成目标。
  如果《Storage 去业务耦合设计》先落地，就按它“由数据集属性声明”的方式判断，不再枚举角色。这是第 10 节第 4 步的前置工作。
- **登记标的快照**：Storage 只给登记过标的快照的数据集跟踪周期，现在只有采集计划对账器（`plan_reconciler.go`）会登记。
  Storage 对生效点有两条约束（`pebble/period_snapshot.go:157`）：必须晚于当前时间，且生效点及以后还没有建过周期。所以分两种情况：
  - **首次登记**（合成任务就绪时）：生效点取“当前时间之后的第一个目标桶边界”。更早的桶不跟踪周期，一律普通写入。
  - **跟随源数据集变化**：对账器（同一进程）为源数据集登记新快照时，生效点 P 本来就在未来；为目标登记同一份标的集合，
    生效点取 P 之后的第一个目标桶开盘时刻（按 4.1 分桶）。目标的周期在桶收盘后才会建立，所以这个生效点之后不会已有周期。过渡桶以目标快照为准，只写快照里的标的。
- **截止时间**：目标桶收盘后 2 分钟（`kline_resample.target_lateness`，沿用现在 PeriodReadiness 的取值）。
  到截止时没写满，Storage 按 degraded 发出完成事件，并附上缺失的标的。整桶一行都没写的，Storage 不会建立这个周期，也不发完成事件；
  之后兜底或历史作业补上时按“补写”处理，由补采完成事件通知下游。
- **写法由调用方事先决定**：周期还不存在时，Storage 会尝试惰性建周期，没有生效快照或已过截止都直接报错，不会返回“已结束”回执
  （`period_snapshot.go:280`、`297`），所以不能靠试写再回退。规则如下：
  - 桶在首次生效点之后、且当前时间早于截止：用周期提交（`CommitTimeSeriesBatch`），序号与快照 hash 复用采集的构造方式；
  - 其余情况（历史作业、截止后的兜底、截止后才回放到的事件）：用普通写入（`UpsertFields`），并记为“补写”（5.6，`origin=resample`）；
  - 周期提交仍然返回“已结束”回执（`period_progress.go:294`，这时 Storage 没有写入这些行）、或返回 `SeriesSnapshotMissing`、`PeriodDeadlinePassed`
    时（时钟误差或快照变化造成），同样改用普通写入。
- **幂等**：周期提交用位图合并记录到达，同一标的写两次结果不变；普通写入写的是同一个值。

### 5.4 事件主路径

- Collector 新增 JetStream 持久消费者，订阅 `CollectorPeriodCompleted`，按“已就绪合成任务的源数据集”过滤主题。
  做法与因子的 `trigger/eventconsumer` 相同，任务变化时更新过滤条件。
- 收到数据集 D、周期 P 的事件时，找出以 D 为源、`source_frequency` 等于事件频率的合成任务；
  若源周期 P 的收盘时刻（`P + source`）恰好是某个目标桶的收盘时刻（由 `packages/barclock` 按 4.1 计算），就对该桶执行 5.2。
  交易时段市场的桶不跨午休，所以上午最后一根源 K 线的事件触发上午最后一个目标桶。
  - 标的列表取事件里冻结的 `universe_subject_ids`，去掉 `failed_subjects`。
  - 不核对桶内前面几个源周期是否完成：读到的源窗口不完整的标的会被跳过，由兜底检查补上。
- 执行成功后才确认（ACK）事件；失败时 NAK 并退避。重复投递的结果相同。
- Collector 停机期间积压的事件，重启后由 JetStream 重新投递，所以兜底检查只处理零星漏掉的情况。

### 5.5 兜底检查

- **触发**：tRPC 定时器 `cron "5 * * * * *"`（每分钟第 5 秒）。沿用 `TryLock` 防重叠，上一次没跑完就跳过本次。
- **检查范围**：每个已就绪的合成任务取最近 `N` 根已收盘的目标 K 线（`kline_resample.fallback_lookback_bars`，默认 3；由 `packages/barclock` 计算，交易时段市场自动跳过午休、收盘后和非交易日），
  只看收盘时刻不晚于 `now − 60 秒` 的桶，给事件主路径留出至少约 1 分钟。这条规则只为减少重复劳动，不是正确性的前提。
- **找缺口**：标的集合取源数据集当前的标的；对“标的 × 桶”批量读目标数据集（只读一个字段），没有行的就是缺口。
- **补缺口**：对缺口按桶执行 5.2。源数据仍不完整就跳过，下一分钟再查。超出 N 根后不再管，缺口由历史扫描发现。
- **无状态**：兜底检查不保存进度，判断“该合成没合成”只看 Storage 里的目标数据。
- **开销**：每次每个任务读“标的数 × N”个键，500 个标的、N = 3 时是 1500 个键。

### 5.6 补采完成事件

补采走普通写入，Storage 只发出 `DatasetRowsUpserted`。View 会消费它，补采的行最终在 View 里可见；
但对应周期当初已按 degraded 发过完成事件，之后不会再发，按事件驱动的下游不知道数据已经补上。
为此新增一个只带元数据的事件，由 Collector 发布：

```text
event.collector.kline.backfilled
  space_id, dataset_id, frequency
  subject_ids               // 本批确认的标的
  start, end                // 本批覆盖区间的并集 [start, end)
  origin                    // auto / manual / force（补采）；resample（合成在周期结束后的补写）
  job_id                    // 补采作业或历史合成作业
  sync_request_id           // 数据集同步点；读 View 的消费者先等它
  confirmed_at
```

**由谁发、何时发**

- 补采分块读回确认成功时（`klinecheck/backfill.go` 里 `FinishChunk(BackfillSucceeded)`），在**同一个 SQLite 事务**里写一条补写通知：数据集、标的、区间、来源、作业。
  合成在周期结束后的补写（5.3）同样写补写通知。
- 发布协程每分钟按（空间，数据集，来源，作业）合并待发通知；作业结束时立即冲刷剩下的。每一批：
  1. 对该数据集追加一个同步点（`AppendDatasetSyncPoint`，`request_id` 取批次 ID，可重放）；
  2. 在一个事务里把通知标为已发，并写入 Collector 本地 outbox；
  3. 推送到 JetStream，`Msg-Id` 取批次 ID 去重。进程崩溃后从 outbox 重新推送。

  outbox 的做法参考策略模块的 `strategy/internal/outbox/publisher.go`。

**为什么要带同步点**

补采分块是 Collector 直接从 Primary 读回确认的，而 View 异步消费行写入事件，事件发出时 View 可能还没写入这些行。
同步点和行写在同一个数据集的有序 outbox 里，View 处理到同步点，就说明它之前的补采行都已写入索引。

| 消费者 | 读哪里 | 对同步点的处理 |
| --- | --- | --- |
| 合成 | Primary | 忽略，写入后立即可读 |
| 因子 | View | 先 `WaitViewSyncPoint` 等自己的 View 追上，再读 |

现有合成回填结束时等待 View 同步点就是这个用法。不由 Collector 等 View 追上再发事件，是因为 Collector 不知道下游各自读哪些 View。

**谁订阅**

- **合成**：把 `[start, end)` 向外对齐到目标桶（按 4.1 分桶），即覆盖这段时间的全部目标桶，对以该数据集为源的每个合成任务提交历史合成作业（`origin=source_backfill`，标的取事件里的 `subject_ids`，最早开始时间 `confirmed_at + 2T`，见 5.8）。
- **因子**：收到后对受影响的标的和区间提交重算。现在因子补洞只靠定期扫描自己的账本，而且只填原来跳过的键；`force` 补采改写了已有数据时，因子发现不了。
  因子侧怎么接，在因子文档里另行设计，本文只定事件契约。

### 5.7 历史合成

**作业模型**：新表 `t_collector_resample_jobs`，替换现在逐标的实例上的回填游标。

| 字段 | 说明 |
| --- | --- |
| `c_job_id`、`c_space_id`、`c_task_id` | 作业身份 |
| `c_request_id` | 同一任务内唯一，CLI 重试时幂等 |
| `c_origin` | `manual`（CLI / 控制台）/ `source_backfill`（补采完成事件） |
| `c_subjects` | JSON 标的列表，空表示全部 |
| `c_start`、`c_end`、`c_next_bucket` | 范围 `[start, end)` 与游标 |
| `c_not_before` | 最早开始时间：`source_backfill` 为 `confirmed_at + 2T`（5.8），`manual` 为提交时刻 |
| `c_status` | `queued` / `running` / `syncing` / `succeeded` / `failed` / `cancelled` |
| `c_written`、`c_skipped`、`c_skipped_samples` | 写入数、跳过数、前若干个跳过的（标的，桶），供查看 |

**执行**

- 范围按 UTC 解析，`start` 向下、`end` 向上对齐到目标桶（按 4.1 分桶，交易时段市场跳过非交易时间），`end` 截到最近一根已收盘的桶；单个作业最多 `MaxResampleBackfillBuckets`（10080）个桶。
- 独立的后台协程每轮从游标处理有限个桶（`kline_resample.history_buckets_per_round`），每个桶执行 5.2，写入后推进游标；进程重启后从游标继续。
  不占用事件主路径和兜底检查的时间。
- 同一任务同一时间只运行一个作业，其余排队；作业到了 `c_not_before` 才能开始。排队中的 `source_backfill` 作业，若范围重叠或相邻，
  就合并（标的取并集，最早开始时间取较晚的一个）。
- 源数据不完整的标的跳过并计数，不等待。`status` 里能看到跳过了哪些，补采这些源数据后，补采完成事件会再次触发；也可以对同一范围再跑一次。
- 作业写入的行都是“补写”：结束时冲刷它的补写通知（5.6，`origin=resample`），等目标数据集默认 View 追上同步点后标记 `succeeded`。
  这保留了现在回填的语义：作业成功即 View 可读。
  实现上拆成两个函数，替换现有的 `completeBackfillWithViewFence`：`waitViewSync(ctx, spaceID, viewID, requestID)` 只负责等 View 追上同步点，可以复用；
  `finishJob(ctx, job)` 依次冲刷补写通知、调用 `waitViewSync`、把作业标为成功。

**接口与 CLI**

用新接口替换 `StartKlineResampleBackfill`、`GetKlineResampleBackfill`、`CancelKlineResampleBackfill`，控制台的回填页面随之改造：

| 接口 | 作用 |
| --- | --- |
| `SubmitKlineResampleJob` | 提交作业：任务、范围、可选标的、`request_id` |
| `ListKlineResampleJobs` / `GetKlineResampleJob` | 按任务、状态列出，或查看单个作业 |
| `CancelKlineResampleJob` | 取消排队或运行中的作业（已写入的保留） |

```text
moox-cli collector resample backfill --space <空间> --task <任务> --start <时间> --end <时间> [--subjects a,b] [--wait]
moox-cli collector resample jobs     --space <空间> --task <任务> [--status running]
moox-cli collector resample cancel   --space <空间> --job <作业>
```

`--wait` 轮询作业状态直到结束，输出写入数、跳过数和跳过样例。

### 5.8 并发与幂等：为什么不加锁

事件主路径、兜底检查、历史合成可能同时处理同一个（标的，桶），这里不加锁：

- **读到同一份源数据时，结果完全相同**：写两次是同一个值，周期提交的位图合并也是幂等的。
- **兜底不覆盖已有结果**：它只补目标里缺的行。
- **源数据在两次合成之间被改写**：执行 A 读到旧源数据，补采随后改写源数据并发出事件，补采触发的作业 B 写入新结果，
  A 最后才写，就会用旧结果覆盖新结果。用两条规则排除这种情况：
  - 每次执行（读源到写完）都有硬超时 `T`（`kline_resample.exec_timeout`，默认 30 秒），写请求带同一个截止时间，超时即放弃；
  - 补采触发的作业（`origin=source_backfill`）设最早开始时间 `confirmed_at + 2T`。到那时，所有在补采写入之前读过源数据的执行，要么已经写完，要么已经放弃。

  所以 B 的写入一定晚于任何读到旧数据的执行，结果最终一致。这条论证的前提是：源数据只会被补采改写。
- **重复执行的代价**：只是多一次批量读写。兜底只看收盘 1 分钟以前的桶，与事件主路径撞车很少。

### 5.9 删除的部分

| 删除 | 替代 |
| --- | --- |
| 合成的 TaskInstance、WriteTarget 及其状态机（`store/task_instance_resample.go` 的认领、租约、CAS、`waiting_source`） | 5.2 的批量执行 + 5.4～5.7 的触发 |
| Collector 本地 PeriodReadiness（`t_period_readiness`、`t_period_readiness_items`）与 `marketfetch/period_reporter.go` | Storage 跟踪目标周期（5.3）。实现时确认没有其他调用方，再连表一起删除 |
| repair 扫描与 `source_hash` 比对 | 补采完成事件触发重算（5.6） |
| `kline_resample` 配置里的 `worker_concurrency`、`max_claims_per_tick`、`stale_running_after`、`repair_lookback_buckets` 等 | `fallback_lookback_bars`、`history_buckets_per_round`、`target_lateness`、`exec_timeout` |
| 任务参数 `settle_delay_ms`（控制台的“收盘等待”） | 事件触发后不再需要，参数与 `SettleDelay` / `SettleDelayOr` 一起删除 |

## 6. 控制台

合成任务**留在“采集任务”页的“采集任务” tab 里**，不另开子页面。合成任务和采集任务是同一个对象：同一张任务表，“一任务一结果”，
启停、删除和结果管理一样，“采集结果” tab 也能直接看它的产出。单独做一个页面，列表、详情、删除、结果管理都要再做一遍，用户还得在两处找任务。

现在的入口是新建任务时“执行任务类型”选 K 线合成，表单出现源行情、源周期、序列标签、收盘等待四项；合成任务的行上有“回填”按钮
（`web/src/views/collector/collection-tasks/collection-tasks.vue`）。改造如下。

### 6.1 新建合成任务的表单

| 项 | 现在 | 改为 |
| --- | --- | --- |
| 源 | “源行情”下拉（`resampleSourceOptions`） | **选源采集任务**：列出同一空间、已启用的 `kline` 任务和合成任务（允许 5m → 15m → 45m 逐级合成），显示“任务名 · 频率 · 市场”。不满足第 4 节规则的（频率不能整除目标、所在市场没有交易日历）置灰并说明原因 |
| 源周期 | 手填 | 选好源任务后**自动带出**，只读 |
| 序列标签 | 手填（例如 `venue:binance`） | 选好源任务后**自动带出**，只读 |
| 目标频率 | 按标准档下拉 | 标准档快捷项加自由输入（例如 `45m`），按第 3、4 节规则即时提示：语法不对、不能被源周期整除、不能整除一天；交易时段市场还提示不能整除交易时段，并列出可选周期 |
| 采集标的 | 显示一个禁用的标签选择框 | 不再显示选择框，只读显示“继承源任务，共 N 个标的” |
| 收盘等待 | 可填 `settle_delay_ms` | **删除**（5.9） |

**源参数由后端推导**：任务参数新增 `source_task_id`。创建时 Collector 由源任务推导并写入
`source_dataset_id`（源任务的结果数据集）、`source_frequency`（数据集频率）、`source_series_tag`（与采集写入用同一个函数 `marketfetch.PlanSeriesTag`），
前端提交时只传 `source_task_id` 和目标频率。这样手填错误的序列标签从根上就不会出现，规则也只在后端一处。
表单上的只读展示来自一个新的预览接口 `PreviewKlineResampleSource`（输入源任务 ID，返回数据集、频率、序列标签、标的数、市场类型，以及第 4 节的校验结果），
提交前后用的是同一套推导。源任务被删除或停用时，已有合成任务照常读源数据集；源数据集被删除时，合成任务进入 `prepare_state=error`，与现状一致。

### 6.2 任务列表

- **“新建合成”行操作**：`kline` 任务和合成任务的行上新增，打开 6.1 的表单，源任务已经选好。这是建立绑定关系最直观的入口；工具栏的“新建任务”保留，选类型后同样能填。
- **新增一列“合成来源”**：合成任务显示“源任务名 → 目标频率”，点源任务名定位到该任务；其余类型为空。数据类型筛选里已有合成，不另加筛选。
- 任务详情抽屉里，合成任务显示源任务、源周期、序列标签、目标频率，以及“依赖它的合成任务”列表（反向关系，供删除源任务前查看）。

### 6.3 历史合成抽屉

合成任务行上的“回填”弹窗（`resample-backfill.vue`）改为“历史合成”抽屉，对应 5.7 的作业接口：

- **上半部分提交作业**：时间范围（UTC）、可选标的（默认全部），提交时生成 `request_id`，重复点击不会重复建作业。
- **下半部分是这个任务的作业列表**：来源（手动 / 补采触发）、范围、状态、进度（游标 / 总桶数）、写入数、跳过数；
  展开可看跳过样例（标的，桶），运行中或排队的作业可以取消。
- 补采触发的作业也显示在这里，用户能看到“源数据补采后自动重算了哪些桶”。
- “补采” tab 只管采集补采，不混入合成作业。

### 6.4 采集结果

合成任务的结果**和采集任务一样，展示在“采集结果” tab**，不单独做结果页。现在已经能显示，重写时保持以下约定不变：

- 结果页用 `GetTaskList` 列出全部任务，不按类型过滤；合成任务同样出现在任务切换 tab 里。
- 合成目标的默认 View 保持 `owner_module=collector`、`view_role=collection_browse`（`resample/catalog.go:179`），结果页的 View 浏览组件只认这两个属性。
  5.9 删除旧结构时 Catalog 不动，这一点不受影响。
- 结果状态、最新数据时间、覆盖范围由 `enrichPBTaskResult` → `InspectIDs` 给出，它不区分任务类型，合成任务照常可用。

补几处合成任务专属的展示：

- 任务切换 tab 的标题后加“合成”标记；结果区顶部显示“源任务 → 目标频率”，点源任务名切到源任务的结果。
- 目标数据集还没有数据时，空状态写明原因：`prepare_state` 未就绪时显示“正在准备结果数据集”；已就绪时显示“等待第一个目标桶收盘（预计 HH:MM）”，时间按市场时区显示（A 股为北京时间），交易时段市场遇到休市会顺延到下一个交易日。
  1d 这类长周期首个桶可能要等将近一天，不说明会被当成故障。
- 有历史合成作业在跑时，显示“历史合成进行中（游标 / 总桶数）”，点击打开 6.3 的抽屉。

## 7. 受影响的模块

| 位置 | 现状 | 改动 |
| --- | --- | --- |
| `packages/frequency` | 写死 10 个值 | 第 3 节 |
| Storage 元数据 `crud_dataset.go:448`、`primarystore/period.go:179`、`pebble/period_snapshot.go` | 用 `IsCanonical` 校验频率 | 频率校验不改代码，随频率包放开 |
| Storage 周期准入 `primarystore/period.go:172` | 周期提交、快照登记只接受 `raw_collection` | 放开到合成目标 `kline_resample_result`（5.3） |
| `packages/storagepolicy`（保留期） | `defaults` 必须覆盖 `Strings()` 里的每个频率，遇到未配置的频率 `Resolve` 直接报错 | 配置键接受任意规范频率；`Resolve` 依次查：空间里的精确值 → 默认里的精确值 → 空间里的“回落标准档” → 默认里的“回落标准档”。回落标准档是时长不超过它的最大标准档（45m → 30m，2h → 1h，12h → 4h）。“defaults 必须覆盖每个标准档”的校验保留 |
| `modules/cli/internal/configinit/editform.go` | 保留期表格写死 10 行 | 改用 `StandardStrings()`；非标准档在配置文件里手写 |
| Collector `resample` | 第 1 节 | 第 5 节重写；`resample.Bars` 与 Catalog 保留 |
| Collector 补采 `klinecheck/backfill.go`、`store/backfill.go` | 分块确认后只更新状态 | 同一事务写补写通知（5.6） |
| Collector 事件发布 | 没有 | 新增本地 outbox、发布协程、两个 JetStream 消费者（源周期完成、补采完成）；事件注册表新增 `event.collector.kline.backfilled` |
| Collector 计划对账 `plan_reconciler.go` | 只为采集数据集登记快照 | 源数据集登记新快照时，同步登记依赖它的合成目标快照（5.3） |
| `moox-cli` | 没有合成命令 | 新增 `collector resample backfill / jobs / cancel` |
| 因子：`factor/internal/domain/frequency.go`、`periodclock/continuous.go` | 各写死一份 `1m…1d` 列表 | 改成从 `packages/frequency` 派生：1m～1d 的定长周期。不改的话，合成出来的 45m 用不了因子 |
| 因子：补采完成事件 | 没有 | 订阅后提交重算，另行设计（5.6） |
| 策略 DSL `strategy/internal/dsl/validate.go` | `supportedBars()` 遍历 `All()` | `supportedBar` 改成按语法判断（1m～1d 的定长周期），提示列表用 `Standard()` |
| Monitor `market_clock.go`、`kline_freshness.go`、`dataset_health_policy.go` | 连续市场按 `Duration()` 计算；交易所市场的 5m～4h 返回“周期对齐未确认” | 连续市场不改。交易所市场的日内周期改用 `packages/barclock` 的 `session` 分桶，补上“对齐未确认”的空缺，这样 A 股合成结果的新鲜度能正常判断 |
| 新增 `packages/barclock` | 没有 | 4.1：`epoch_utc` 与 `session` 两种分桶，合成与 Monitor 共用 |
| Storage 快照对齐 `pebble/period_snapshot.go:77`（`periodAligned`） | 日内周期要求生效点按 epoch 对齐 | 数据集声明 `alignment=session` 时只要求整分钟（A 股 1h 的 09:30 开盘不是整点 epoch 边界）；合成目标数据集已经带 `alignment` 属性 |
| 归档 `archive/internal/schedule` | 日内按天分区，`BarSpan` 取 `Duration()` | 不改代码：可整除一天，因此不会跨天分区 |
| 采集直连 `marketdata.klineFrequencies`、币安 `binanceIntervals` | 白名单 | **保持不变**：45m 不能直接采集。币安的 `binanceIntervals` 在新语法下能解析 `2h` 等值，但白名单仍会拦住 |
| 前端 `web/src/utils/frequency.ts` | `FREQUENCIES` 写死 | 与 Go 同步：`STANDARD_FREQUENCIES` 加语法版 `isCanonicalFrequency` / `normalizeFrequency` |
| 前端采集任务页（`collection-tasks.vue`、`collection-task-params.ts`、`resample-backfill.vue`） | 合成表单手填源周期、序列标签；单个回填弹窗 | 第 6 节：选源采集任务、自动带出、删收盘等待、“新建合成”行操作、“合成来源”列、历史合成抽屉 |
| 前端采集结果页（`task-results/index.vue`、`task-results-model.ts`） | 已能显示合成任务结果 | 6.4：合成标记、“源任务 → 目标频率”、空状态说明、历史合成进度 |
| Collector 任务接口 | 合成参数由前端提交 `source_dataset_id` 等三项 | 任务参数新增 `source_task_id`，三项由后端推导；新增 `PreviewKlineResampleSource`（6.1） |
| 文档 | — | [采集](../模块/采集.md)第 7 节写明可合成的周期、校验规则、触发方式、补采完成事件与 CLI；[存储](../模块/存储.md)写明频率语法与保留期回落 |

## 8. 先修：合成任务的两套校验不一致（现有 bug）

合成任务的参数现在有两套校验：

| 入口 | 校验函数 | 要求目标 Dataset ID 以 `_<频率>` 结尾 |
| --- | --- | --- |
| 控制台 / CLI 创建、更新（`rpc/service.go:1267`） | `resample.ValidateTaskParams` | 否 |
| Collector 启动时刷新实时清单（`observability/realtime_inventory.go:104`） | `CollectParams.Validate` → `ValidateKlineResample` | **是** |
| 内置任务导入（`ruleseed/seed.go:146`） | 同上 | **是** |

结果 Dataset ID 由任务 ID 派生（`taskresult.resultIDs`：`dataset_<task>`），不带频率后缀。所以只要任务 ID 不恰好以 `_<目标频率>` 结尾，
清单刷新就会报错；刷新排在“合成功能是否启用”的判断之前，`bootstrap.go:109` 又把首次刷新失败当作启动错误，**Collector 会起不来**。
这与是否加 45m 无关，要先修：

- 三个入口统一调用 `resample.ValidateTaskParams`（它已覆盖 `ValidateKlineResample` 的其余检查，第 4 节的新增规则也只写在这一处）；
- 删除 `domain.ValidateKlineResample` 与 `validateResampleTargetDatasetID`，`CollectParams.Validate` 的 `kline_resample` 分支改为交给调用方；
  domain 包不能反向依赖 resample 包，所以由入口直接调用；
- 删除 `resample.ValidateTargetDatasetID` 里注释写着“故意忽略”的可变参数；
- 补测试：任务 ID 不带频率后缀的合成任务，清单刷新和种子导入都能通过。

## 9. 不在本次范围

- **不能整除交易时段的周期**（如 A 股 45m、4h）：需要跨午休或截短的桶，第 4 节第 5 条在提交时拒绝，不支持。
- **港股、美股的合成**：分桶规则已覆盖，缺交易日历，补上日历即可使用。
- **A 股合成结果用于因子**：因子模块目前只支持 crypto 空间（`periodclock.ForSpace`），需要因子侧另做。
- **未收盘的实时合成**（45m 收盘前就展示进行中的那一根）：现有链路只处理已收盘的桶，需要时另做只读计算，不写 Storage。
- **秒级与跨日周期**（`2d`、`3d`）：没有业务需求，不支持。
- **因子订阅补采完成事件后的重算细节**：本文只定事件契约。

## 10. 实施顺序与验收

0. **统一合成校验**（第 8 节，现有 bug，可单独先发）：三个入口统一用 `resample.ValidateTaskParams`，删除后缀检查。
1. **频率包**：实现语法、规范化与 `Standard()`；改名并修好全部调用方，更新测试（含 `60m → 1h`、`45M` 拒绝、`7m` 拒绝、`05m` 拒绝）。
2. **保留策略回落**：`storagepolicy.Resolve` 加回落规则并补单测；CLI 表格改用 `StandardStrings()`。
3. **分桶与合成规则**（依赖第 0 步）：实现 `packages/barclock`（`epoch_utc`、`session`）；`ValidateResamplePair` 增加整除检查，交易时段市场增加“整除每个交易时段”与“必须有交易日历”的检查；
   Storage 快照对齐按 `alignment=session` 放宽。单测覆盖 30m → 45m 拒绝、crypto 5m → 45m 接受、A 股 1m → 45m 拒绝、A 股 1m → 1h 接受且桶为 09:30 / 10:30 / 13:00 / 14:00、跨长假的交易日推进。
4. **批量执行与目标周期**（5.2、5.3）：先放开 Storage 周期准入（`period.go:172`）；再做执行函数、目标快照登记（首次登记与跟随源变化两种）、
   按“首次生效点与截止”事先选写法；单测覆盖不完整标的跳过、首次生效点之前的桶走普通写入、截止后走普通写入、
   周期提交返回已结束回执或 `SeriesSnapshotMissing` / `PeriodDeadlinePassed` 时改写法。
5. **兜底检查**（5.5）：cron 改为第 5 秒；单测覆盖“只看最近 N 根、收盘 1 分钟以前”、只补缺失的行。到这一步可以先上线，由兜底单独承担实时合成，时延约 1 分钟，比现在没有变差。
6. **事件主路径**（5.4）：JetStream 消费者与过滤条件维护；测试覆盖重复投递、degraded 事件跳过失败标的。
7. **补采完成事件**（5.6）：补写通知、本地 outbox、同步点、发布；测试覆盖进程在追加同步点后、写 outbox 前崩溃时能重放。
8. **历史合成作业与 CLI**（5.7）：作业表、执行协程、合并规则、新接口、CLI（控制台抽屉在第 11 步）。
9. **删除旧结构**（5.9）：TaskInstance、WriteTarget、PeriodReadiness 与 repair 扫描。
10. **因子与策略、前端频率**：频率改从共享包派生；前端频率工具。
11. **控制台**（第 6 节，依赖第 3、8 步）：`source_task_id` 与预览接口、合成表单改造、“新建合成”行操作、“合成来源”列、历史合成抽屉、采集结果页的合成展示；
    前端单测覆盖源任务置灰规则、目标频率即时提示、只读带出项。
12. **端到端**：`modules/collector/test/kline_resample_e2e_test.go` 增加 5m → 45m 用例，覆盖事件触发、跨天边界（23:15–00:00 UTC 的最后一桶）、
    兜底补缺、补采源数据后自动重算、历史作业。
13. **文档**：第 7 节列出的文档更新。

验收：

- crypto 空间新建 `5m → 45m` 合成任务。45m 收盘后 5 秒内，目标数据集写满并发出 complete 的 `CollectorPeriodCompleted`。
- 停掉事件消费者，兜底检查在约 1 分钟内补齐。
- 删除一个标的的一根 5m 源 K 线，再对其补采，对应 45m 自动重算，因子收到补采完成事件。
- 用 CLI 回填最近 3 天：每个标的每天正好 32 根，`data_time` 都是 45 分钟的整数倍；随机抽 3 根，与 5m 原始数据手算的 OHLCV 一致。
- 在 45m 数据集上建一个因子，能正常出值。
- stockcn 空间：`1m → 45m` 提交即被拒绝并给出可选周期；`1m → 1h` 合成任务每个交易日正好 4 根（09:30、10:30、13:00、14:00 北京时间开盘），
  午休与非交易日没有桶，Monitor 能正常判断它的新鲜度。
