# 因子本地 K 线存储与回放设计

2026-10-21 · v3（已定稿） · 基于 `feat/ws-market-stream`（3ec89780）

## 0. 已确认的决定

| # | 决定 |
| --- | --- |
| 1 | 因子机本地用 Pebble 持久化 K 线；实时计算、水位追赶、补算、重算都从本地读，Storage 只用于补增量 |
| 2 | 本地数据**永不删除**；不做每日核对；不抽公共 Pebble 包 |
| 3 | 不做应用层压缩，开启 Pebble 自带的块压缩 |
| 4 | 内存保留按列存储的**滑动窗口**；初始化时估算窗口内存给用户参考 |
| 5 | 历史数据通过 **CSV 导入**，同时写本地存储和 Storage；文件先放到因子机指定目录（用户自己放，或由 Claude 上传），导入时给路径 |
| 6 | CSV 列名与系统里注册的字段名一致 |
| 7 | **逐期回放本期要做**：回放不产生实际下单，只走纸上交易；消息体加回放标记 |
| 8 | `compute.max_memory` 按初始化时探测到的因子机内存自适应 |
| 9 | 5 万根主要用于 1m；View 默认保留根数从 5000 改为 **1 万**，初始化时可设置 |
| 10 | 所有时间统一用 UTC：块边界、CSV 时间列、命令参数、日志、回放时钟 |
| 11 | 回放默认按下一根 K 线开盘价成交（可选本期收盘价） |
| 12 | 回放不走生产事件总线：引擎直接调用策略回放求值接口，只在发往 Trade 的目标权重消息里带 `replay_id`、回放时钟与回放报价 |

## 1. 背景

- 预期规模：每个标的回看最多约 5 万根 1m K 线，1000 多个标的，单次计算约 5000 万行。
- 现状：因子引擎每期从 Storage 经网络读取，只在进程内存里缓存实时窗口（`pipeline/window_cache.go`）。
  - 缓存用 `[][]any` 按行存储，每格约 48 字节（`pipeline/memory.go:16-18`），这个规模下放不下；
  - 重启要整窗口重新预热；
  - 补算、重算（`ModeRecalc`）每次从 Storage 读整窗口（`pipeline/load.go:48-56`）。

## 2. 总体结构

```
 CSV（因子机磁盘）                         Storage（权威）
     │ moox-cli factor kline import            ▲    │ 增量 / 补齐重拉
     ▼                                         │    ▼
 ┌─────────────── 因子引擎进程 ───────────────────────────────┐
 │ 导入器 ──► klinestore（Pebble，磁盘，永久） ◄── klinesync    │
 │   └─后台分批写 Storage        │ 启动加载 / 每期追加           │
 │                               ▼                             │
 │                  klinewindow（内存列式滑动窗口）              │
 │                               │                             │
 │        pipeline（实时 / 追赶 / 补算 / 重算）   回放驱动        │
 └───────────────────────────────┼───────────────────┼─────────┘
                                 ▼                   ▼ 回放帧（replay_id）
                         Storage 结果写回      策略回放求值 ─► Trade PAPER
```

`modules/factor/internal/` 下新增 5 个组件：

| 组件 | 职责 |
| --- | --- |
| `klinestore` | 本地磁盘存储，Pebble 只出现在这里 |
| `klinewindow` | 内存列式滑动窗口 |
| `klinesync` | 从 Storage 增量拉取、补齐后重拉 |
| `klineimport` | CSV 导入 |
| `replay` | 逐期回放驱动 |

Pebble 同一目录只允许一个进程打开，所以导入和回放都在引擎进程内执行，命令行只负责下发任务、查询进度。

## 3. 本地磁盘存储 klinestore

### 3.1 接口

```go
type Store interface {
    // ReadRange 返回 [start, end) 内各标的的列式数据，按 data_time 排序。
    ReadRange(ctx context.Context, src Source, subjects []string, start, end time.Time) (map[string]*Columns, error)
    // Upsert 用 rows 替换标的在 [from, to) 内的数据。新 K 线的区间在本地最新一根之后，效果就是追加。
    Upsert(ctx context.Context, src Source, subject string, from, to time.Time, rows *Columns) error
    // Latest / Earliest 返回标的本地最新、最早一根的时间。
    Latest(src Source, subject string) (time.Time, bool, error)
    Earliest(src Source, subject string) (time.Time, bool, error)
}

type Source struct{ SpaceID, DatasetID, Freq string }
// Columns: Times []int64（UTC 秒）、SeriesTags、按字段名的数值列 []float64 / []int64 + 空值位图，其他类型按字节存。
```

不提供删除接口：本地永不删除，补齐后的修正用 `Upsert` 按区间替换完成。

### 3.2 数据布局

- **key**：`k/{space}/{dataset}/{freq}/{subject}/{blockStart 大端 int64 UTC 秒}`。各段是系统内标识符，写入前做字符校验，不需要转义编码。
- **块跨度**按频率固定，每块几十到几百行：

  | 频率 | 块跨度（UTC 对齐） | 每块行数 |
  | --- | --- | --- |
  | 1m | 1 小时 | 60 |
  | 3m / 5m | 6 小时 | 72–120 |
  | 15m / 30m | 1 天 | 48–96 |
  | 1h | 1 周 | 168 |
  | 4h | 1 个月 | ~180 |
  | 1d / 1w / 1mo | 1 年 | ≤ 366 |

- **块内按列编码**：
  - 头部：行数、列名表（数据集全部字段）；
  - 时间列：int64 UTC 秒；
  - `series_tag`：块内字典；
  - 数值列：float64 / int64，每列带空值位图；
  - 其他类型（字符串、TIME、JSON）：长度前缀字节。
  - 不做应用层压缩，Pebble 用默认块压缩。
- **存数据集全部字段**，同一数据集的多个因子集合共用一份。数据集加字段后，新块带新字段，旧块读出为空；要补齐旧数据就重新导入或重拉对应区间。
- **元数据 key**：
  - `m/layout`：布局版本，不一致时拒绝启动；
  - `m/latest/…`、`m/earliest/…`：每个标的本地最新、最早时间；
  - `m/import/{taskID}`：导入断点；
  - `m/replay/{replayID}`：回放进度。

### 3.3 读写实测（2026-10-21）

环境：云端 4 核容器，Pebble v1.1.5，1000 标的 × 5 万根 1m，7 个数值字段。测试代码在会话临时目录，可按需放进仓库的 `tools/`。

| 项 | 块方案（采用） | 对照：每根一个 KV |
| --- | --- | --- |
| 全量导入 5000 万行 | 34s，约 145 万行/秒 | 64s，约 78 万行/秒 |
| 每期追加 1000 根（一批，同步落盘） | 平均 33ms，最慢 104ms | 平均 15ms |
| 读 1000 × 1 万根 | 1.4–2.9s | 2.0–3.2s |
| 读 1000 × 5 万根 | 5.6–6.0s | 7.9–8.1s |
| 磁盘 | 2.8GB | 2.8GB |

结论：

- 追加的读改写每期只要几十毫秒，不是瓶颈。实时写入量是每分钟约 2000 根，离上限差三四个数量级。现有的 WS 采集写主存也是"每个数据集每期一次批量提交"（`collector/internal/stream/commit.go`），是同一种模式。
- **瓶颈是每期整窗口重读**（5 万根约 6 秒），所以引入第 4 节的内存滑动窗口。磁盘存储只在启动、回放开始、补齐重拉和超出窗口的补算时读取。

### 3.4 容量

- 本地磁盘：实测约 56 字节/行。crypto 1m 现货加合约（约 2000 个序列）存 5 万根约 5.6GB。因子机剩余 1.7T，不需要清理。
- Pebble 块缓存 512MB，可配置。

## 4. 内存滑动窗口 klinewindow

### 4.1 结构

- **单位**：每个「数据源（space + dataset + freq）」一份窗口，被多个因子集合共用。
- **长度**：使用该数据源的启用集合的最大回看根数。
- **存储**：每个标的每个字段一个定长环形数组（float64 或 int64），另有一个时间数组。
  - 每期追加一根，最旧的一根自然被覆盖；
  - 计算时直接以列的方式交给表达式执行器，不转换成 `[][]any`。
- **生命周期**：
  - 引擎启动时从 `klinestore` 加载（5 万根约 6 秒），加载完成前集合状态为"窗口加载中"；
  - 每个实时周期由同步器写盘后追加；
  - 补齐通知重拉后，覆盖窗口里对应的区间；
  - 回放用独立的窗口（第 6 节），不影响实时窗口。

### 4.2 内存估算

公式：**标的数 × 窗口根数 × (数值字段数 + 1) × 8 字节 × 1.1**。crypto K 线 7 个字段，约每行 70 字节：

| 序列数 | 1 万根 | 5 万根 |
| --- | --- | --- |
| 1000（如现货 1m） | ~0.7GB | ~3.5GB |
| 2000（现货 + 合约 1m） | ~1.4GB | ~7GB |
| 再加 1h 两个数据集 | +~1.4GB | +~7GB |

- **初始化时**：
  - 探测因子机内存（已有，`cli/internal/configinit/probe.go:142`）；
  - 按"计划启用的数据集 × 预计标的数 × 窗口根数（默认 1 万）"给出窗口内存估算，和建议的 `max_memory` 一起展示；
  - 两者之和超过总内存的 60% 时提示调小窗口根数。
- **运行时**：
  - 状态页显示每个数据源窗口的实际占用；
  - 新启用的集合如果会让窗口内存超过上限（`window.max_memory`，初始化时写入），就拒绝启用，并给出明确原因。

### 4.3 对计算侧的影响

窗口是列式的，pipeline 的读取和表达式执行器的输入要随之改成列式，跳过 `storageio.Frame`（`[][]any`）。这原本是 v2 里"单独评审"的第 7 步，现在成为本期必做：每期把 5000 万格转成 `[][]any`，本身就要十几 GB 内存和数秒 CPU。

- **数值字段**：直接从窗口切片构建面板。
- **携带列（非数值）**：只取目标周期的那几行，仍按行处理。
- **内存预算**（`pipeline/memory.go`）：去掉"读取帧"那一项，窗口内存单独计入第 4.2 节的估算。

## 5. 同步与导入

### 5.1 同步器 klinesync

| 时机 | 动作 |
| --- | --- |
| 每个实时周期，计算前 | 对本期标的，从 `Latest` 拉到本期末，`Upsert` 写盘，然后追加到窗口。正常每个标的只拉 1 根 |
| 引擎启动 | 对所有启用集合的数据源，每个标的从 `Latest` 拉到当前，补齐停机缺口，再加载窗口 |
| 收到补齐通知 `PeriodsFilled`（`engine/catchup.go:45`） | 重拉 `[from, to)`，`Upsert` 替换并覆盖窗口；完成后再唤醒水位推进和补算（调整 `onFilled` 的顺序） |
| 本地没有某个标的（新上市、新数据集） | 从 Storage 拉"最大回看根数"的窗口，一次性完成 |

同一数据源的同步、导入、补齐重拉按数据源串行执行。

### 5.2 CSV 导入

**文件准备**：CSV 放到因子机的指定目录（例如 `/data/moox/import/`），用户自己放，或由 Claude 用 scp 上传。导入命令只传路径，文件内容不再经过网络。

**格式**：

- 一个文件对应一个标的，文件名即标的 ID（也可以用 `--subject` 指定）；
- 列名与数据集注册的字段名一致，外加时间列 `data_time`；
- 时间按 UTC 解析：没有时区的时间视为 UTC，带时区的统一转换为 UTC；
- 可选 `series_tag` 列，或用 `--series-tag` 统一指定。

**命令**：

```
moox-cli factor kline import --space crypto --dataset <dataset> --freq 1m \
  --dir /data/moox/import/spot_1m/ [--storage=true] [--recalc]
```

**引擎处理**：

1. 解析并校验。CSV 读取和字段校验复用现有 `moox-cli storage import` 的代码（`cli/internal/command/storage_import.go`），抽成公共包 `packages/klinecsv`。
2. 按块 `Upsert` 写本地。
3. `--storage=true`（默认）时，把同样的行后台分批写入 Storage，并绑定标的（同现有 storage import）。本地写完即可开始计算；Storage 的写入进度单独显示。
4. 断点记录在 `m/import/{taskID}`，重跑时幂等。
5. 带 `--recalc` 时，导入完成后对相关集合按导入区间发起区间重算，得到历史因子值。

### 5.3 本地数据未就绪时

集合的数据源在本地没有覆盖最大回看窗口时，集合状态为"本地数据未就绪"：不计算、不跳期，连续水位停住。导入或同步完成后，水位从停住的地方自动追赶（`engine/catchup.go`）。

## 6. 逐期回放

### 6.1 目标与边界

- 按时间顺序逐期推进历史 K 线，每期走实时计算路径算出因子，交给策略求值，再由 Trade 在 **PAPER 账户**里撮合，得到接近实盘时序的模拟结果。
- **回放绝不产生实际下单**。
- 回放不进入生产事件总线，不影响连续水位、监控新鲜度和实盘策略实例。

### 6.2 流程

```
moox-cli factor replay start --set <因子集合> --instance <策略实例> \
  --from 2026-01-01T00:00:00Z --to 2026-02-01T00:00:00Z [--write-factors]
```

1. 校验：
   - 策略实例绑定的逻辑账户必须全部是 PAPER 账户，否则拒绝；
   - 区间内的 K 线在本地已就绪（先导入）。
2. 引擎建立回放会话（`replay_id`）和独立的回放窗口：从 `klinestore` 加载截至 `from` 的回看窗口。
3. 回放驱动逐期推进，对周期 t：
   1. 窗口追加 t 这一根，计算因子；
   2. 调用策略的回放求值接口，传入 `replay_id`、t 和本期因子帧，等待返回；
   3. 策略复用现有求值引擎，产出目标权重，向 Trade 发出带 `replay_id` 的 `LogicalAccountTargetWeightRequested`，同时附带本期回放报价；
   4. Trade 在 PAPER 账户按回放报价和回放时钟撮合，完成后确认；
   5. 收到确认后再推进下一期，保证严格按时序。
4. 带 `--write-factors` 时，同时把回放算出的因子结果写入结果数据集（与区间重算的写法相同，不发周期完成事件）。
5. 进度、当前周期、每期耗时记在 `m/replay/{replayID}`，支持暂停、继续、取消。

### 6.3 消息与接口改动

| 位置 | 改动 |
| --- | --- |
| `tradeeventpb.LogicalAccountTargetWeightRequested` | 新增 `replay_id`、`replay_time`（回放时钟，UTC）、`replay_quotes`（本期各标的的回放报价） |
| 策略 RPC | 新增回放求值接口：入参是 `replay_id`、实例、周期和因子帧，复用现有求值引擎（`strategy/internal/engine`） |
| Trade 入口 | `replay_id` 不为空时，目标账户必须是 PAPER，否则拒绝并告警。这是双重保险：策略侧已校验一次 |
| Trade PAPER 撮合 | 现在用实时报价并校验新鲜度（`trade/internal/execution/paper/pricing.go`）。回放订单改用 `replay_quotes`，以 `replay_time` 作为当前时间 |
| Trade 订单、成交 | 记录 `replay_id`，便于查询与清理 |
| 因子集合事件、水位 | 不变。回放不走 `FactorPeriodComputed` 等生产事件，不需要所有消费者都去过滤回放标记 |

回放报价默认取下一根 K 线的开盘价，模拟"本期收盘决策、下期开盘成交"；可选本期收盘价。

### 6.4 与区间重算、策略研究回放的关系

| 需求 | 用法 |
| --- | --- |
| 只要历史因子值 | CSV 导入 + 区间重算（`--recalc`）。按分块共享窗口，比逐期回放快几个数量级 |
| 要按实盘时序验证策略与成交 | 逐期回放 |
| 快速比较策略版本 | 现有策略研究回放（`strategy/internal/replay`，读取 View，区间受 View 保留根数限制） |

### 6.5 速度估算

内存窗口下每期的读取成本接近零，耗时主要在计算和撮合往返。如果每期 0.1–1 秒，回放一个月的 1m（约 4.3 万期）需要 1–12 小时。实施时按真实因子集合实测，再决定是否在回放里批量预计算因子、只逐期执行策略和撮合。

## 7. max_memory 自适应

- 初始化探测到因子机总内存 M、估算出窗口内存 W 之后，建议 `compute.max_memory = max(1GiB, (M − W) × 50%)`，写入 `engine.yaml`，用户可以修改。
- 初始化界面同时展示 M、W 和建议值。M × 60% 小于 W 加 1GiB 时给出警告。

## 8. View 保留根数与 Storage 容量

### 8.1 改动

- `packages/storagepolicy/policy.go:32` 的 `DefaultViewBars` 从 5000 改为 10000；初始化表单已有这一项（`cli/internal/configinit/editform.go:48`）。
- 默认保留期要能装下 1 万根，否则配置校验报错（`storagepolicy.Coverage`）：

  | 频率 | 现在 | 改为 |
  | --- | --- | --- |
  | 1m | 7d | 8d |
  | 5m | 30d | 35d |
  | 15m | 90d | 105d |
  | 30m | 180d | 210d |
  | 1h | 365d | 420d |
  | 4h | 1095d | 1700d |

  有交易时段的市场只会给出警告，按需调整。同步更新 `moox.toml.example`，以及 `docs/计划/策略模块评审与设计.md` 4.5 节里"默认 5000 根"的描述。

### 8.2 实测与估算（2026-10-21，Storage 机）

| 位置 | 实测 | 每行 |
| --- | --- | --- |
| crypto 1m K 线 View（单个数据集） | ~546MB | ~190–210 B |
| crypto 1m 因子结果 View（单个数据集） | ~665MB | ~230 B |
| 主存 Pebble | 4.3GB，512 个 SST | ~200 B |
| 磁盘 | Storage 机 98G 剩 75G；控制机 39G 剩 17G；因子机 1.8T 剩 1.7T | — |

crypto 现有 8 个数据集（K 线和因子结果，1m、1h，现货和合约，每个约 520 个序列）：

| 每序列根数 | View | 主存 | Storage 机合计 |
| --- | --- | --- | --- |
| 1 万（默认） | ~8.5GB | ~8.5GB | **~17–20GB**，够用 |
| 5 万 | ~43GB | ~43GB | **~85–100GB**，需要先扩盘 |

## 9. 一致性与故障处理

| 情况 | 处理 |
| --- | --- |
| 本地目录损坏、布局版本不符 | 拒绝启动。人工删目录后重启，然后重新导入，或由同步器按回看窗口从 Storage 拉取 |
| 同步器拉取失败 | 本期按现有规则：读不到的标的算失败，进入跳过账本，之后补算 |
| 导入时本地已完成、Storage 未写完 | 计算照常进行；Storage 写入续传，状态显示未完成 |
| 本地与 Storage 不一致 | 由补齐通知重拉修正，补算重算受影响的周期 |
| 回放中引擎重启 | 回放会话暂停，按 `m/replay` 记录的进度继续 |
| 回放订单指向非 PAPER 账户 | 策略和 Trade 两处都拒绝，并告警 |

## 10. 配置

```yaml
klinestore:
  dir: ./data/klinestore
  block_cache: 512MiB
window:
  max_memory: 4GiB              # 初始化按探测结果写入
compute:
  max_memory: 1GiB              # 初始化按 (总内存 − 窗口) × 50% 写入
import:
  dir: /data/moox/import        # CSV 放置目录
  storage_batch_rows: 1000
  storage_workers: 2
```

## 11. 实施阶段

| # | 阶段 | 内容 |
| --- | --- | --- |
| 1 | klinestore | 块编码与解码、`ReadRange` / `Upsert` / `Latest` / `Earliest`、单元测试 |
| 2 | klinewindow 与列式计算 | 环形窗口；pipeline 与表达式执行器改为列式输入；删除 `liveWindowCache` 与后台预热；调整内存预算 |
| 3 | klinesync | 实时增量、启动补缺、新标的拉取、补齐重拉（调整 `onFilled` 顺序） |
| 4 | CSV 导入 | 抽出 `packages/klinecsv`；引擎导入任务；`moox-cli factor kline import`；`--recalc` |
| 5 | 初始化与配置 | 窗口内存估算与 `max_memory` 自适应；`DefaultViewBars` 改为 1 万及默认保留期；相关文档同步 |
| 6 | 逐期回放 | 回放驱动；策略回放求值接口；消息加 `replay_id` 与回放报价；Trade 的 PAPER 校验、回放撮合与时钟 |
| 7 | 部署验证 | 导入真实 CSV；测 1m 实时周期耗时、窗口内存、磁盘；与 Storage 抽样比对；一次一个月的 1m 回放 |

阶段 1–5 是本地存储本身，可以先上线；阶段 6 依赖 1–4。

## 12. 待确认

无。第 0 节第 11、12 条已于 2026-10-21 确认。
