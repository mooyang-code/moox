# SCF 短时行情采集执行计划

> **执行要求：** 实现本计划时使用 `superpowers:executing-plans` 或 `superpowers:subagent-driven-development`，逐任务执行并在每个阶段完成测试、独立代码审查和提交。

**目标：** 将当前常驻 Collector SCF 改造为 10 秒内完成的按需行情采集函数，在保留多地域公网出口能力的同时，显著降低函数运行时长和资源使用费用。

**架构：** Collector 是唯一调度控制面，按启用规则生成稳定的采集周期和短时批次；CloudNode 负责按地域异步调用 SCF；SCF 只抓取当前批次、批量写入 Storage Primary 并发布一个受治理的批次完成事件。Collector 消费完成事件、持久化失败项并负责超时回收和受控重试，Monitor 只观察批次、Dataset 和部署状态，不参与调度。

**技术栈：** Go、tRPC-Go Timer、腾讯云 SCF 事件函数、SQLite/GORM、Storage Primary、NATS JetStream EventBus、`packages/events`、`packages/report`。

---

## 1. 背景、目标与非目标

当前 SCF 使用常驻 Worker、JetStream Consumer 和 Keepalive。函数被调用后长时间等待任务，导致函数运行时长和资源使用费用偏高。

SCF 仍需保留，因为行情 API 请求必须从 SCF 发出，以使用不同地域的公网出口，降低单一出口集中请求带来的频控风险。本计划仅设计 Binance REST 行情采集，不引入 WebSocket。

### 1.1 目标

1. 每分钟处理所有启用中的实时 TimeSeries Dataset + Frequency。
2. 支持约 1000 个标的的实时 K 线采集。
3. SCF 单次执行超时固定为 10 秒；函数不常驻、不使用 Keepalive。
4. 函数内部并发由环境变量配置，并在 CloudNode 数据库记录期望值。
5. 实时采集只抓取最近少量已收盘 K 线，不在实时批次内追赶大量历史数据。
6. SCF 在内存中聚合结果后，只通过 Storage Primary 批量写入真值数据。
7. 临时失败通过 EventBus 批次完成事件回传，由 Collector 落库并重新调度。
8. 支持多地域部署，但第一版先用少量函数验证出口 IP、429、冷启动和成本。
9. Monitor 能展示采集周期完成度、Dataset freshness、重试积压和 SCF 部署状态。

### 1.2 非目标

- 不实现 WebSocket 行情连接。
- 不实现预置并发。
- 不实现跨地域一致性、分布式锁、Saga 或全局 exactly-once。
- 不在 SCF 内进行多轮 HTTP 重试或长时间退避。
- 不让 Monitor 参与任务生成或 SCF 调用。
- 不保证不同函数一定具有不同公网出口 IP；该能力以真实探针结果为准。
- 不在第一版实现复杂错峰；同一采集周期的批次可以同时触发。
- 不把 SCF Sentinel、Watchdog 等外部探测逻辑塞进行情 Fetcher；需要时由独立的短时探测调用承担。

## 2. 已确定的核心决策

| 事项 | 第一版决策 |
| --- | --- |
| 调度所有者 | 仅 Collector |
| SCF 运行模式 | 异步事件调用，单次批次，10 秒超时 |
| SCF 内存 | 64MB |
| 实时批次初始大小 | 10 个 CollectionItem |
| 函数内 HTTP 并发 | 5 |
| 单次 HTTP 超时 | 2000ms |
| 函数内 HTTP 尝试次数 | 1 次，不在函数内重试 |
| 实时 K 线窗口 | 最近 3 根已收盘 K 线 |
| 历史缺口 | 独立 CatchupBatch，每批 1 个标的、最多 1000 根 |
| 数据真值写入 | 仅 Storage Primary |
| EventBus 作用 | 批次完成回执、失败明细和异步解耦 |
| 重试状态真值 | Collector SQLite |
| 运行时函数配置真值 | CloudNode `t_cloud_nodes.c_metadata` 和部署环境变量 |
| `custom.toml` 作用 | 新系统初始化种子，不是运行时第二份真值 |
| 初始地域规模 | 每个启用地域 1 个函数 |
| 扩展到 50 个函数 | 出口 IP 与 429 实测证明有效后再执行 |
| 上线方式 | 全新初始化；不兼容、不迁移、不恢复旧常驻链路 |

## 3. 官方约束与成本依据

腾讯云 SCF 资源使用量按“配置内存 × 实际运行时长”计算，因此从常驻改为短时调用可以直接减少 GBs 使用量：[计费项介绍](https://cloud.tencent.com/document/product/583/63306)。

地域间账号级并发额度相互独立；广州、上海、北京、成都和中国香港默认总函数并发配额为 128000MB。每个命名空间默认最多 50 个函数：[并发管理体系](https://cloud.tencent.com/document/product/583/49313)、[配额限制说明](https://cloud.tencent.com/document/product/583/11637)。

SCF 当前函数运行环境最小内存为 64MB，异步请求事件大小上限为 128KB，函数超时支持 1～900 秒。本计划固定使用 64MB 和 10 秒，不尝试 32MB。

SCF 官方没有承诺固定冷启动耗时。冷启动必须通过 `Init Report` 或 `Provisioned Report` 中的 `Coldstart`、`PullCode`、`InitRuntime` 和 `InitFunction` 实测：[工作原理](https://cloud.tencent.com/document/product/583/9694)。

因此：

- 不在容量计算中假定冷启动固定为 1～2 秒；
- 事件载荷只携带最多 10 个轻量 CollectionItem，不携带行情结果；
- 正常 Handler 在 8 秒前停止新请求，预留约 2 秒提交 Storage 和发布完成事件；
- 是否扩大批次、并发或函数数量，以真实 P95/P99 和 429 数据决定。

## 4. 名词与采集规则

### 4.1 名词收敛

| 名称 | 含义 | 持久化策略 |
| --- | --- | --- |
| TaskRule | 用户配置的采集规则 | 长期保存 |
| TaskInstance | 规则展开后的稳定业务任务，例如某 Dataset 的某 Symbol + Frequency | 长期保存，每个组合仅一条 |
| CollectionItem | 某次 SCF 请求中的单个标的采集项 | 不逐周期单独落库 |
| BatchInvocation | 一次实际 SCF 调用，包含最多 10 个 CollectionItem | 成功保留 48 小时，失败保留 7 天 |
| RetryItem | 需要重试的单个 CollectionItem | 仅失败时保存，成功或过期后清理 |
| CatchupBatch | 独立的历史缺口补采批次 | 按 BatchInvocation 保存 |

不再使用 `SymbolTask` 表示逐标的运行单元，因为当前 `data_type=symbol` 已表示“交易所标的元数据同步”。运行时逐标的单元统一命名为 `CollectionItem`。

### 4.2 Symbol Dataset 规则

Symbol 规则负责生成或刷新 RECORD 类型的 Symbol Dataset。用户可以手动指定关注的标的，同时必须提供数据源、市场类型和目标 Dataset：

```json
{
  "data_type": "symbol",
  "provider": "binance",
  "market_type": "spot",
  "symbol_source": "manual",
  "symbols": ["BTC-USDT", "ETH-USDT"],
  "target_dataset_id": "binance_spot_symbols"
}
```

执行语义：

1. Collector 生成只包含一个规则项的 `symbol_snapshot` BatchInvocation。
2. SCF 调用 Binance exchangeInfo 获取一次市场标的快照；行情数据源 API 不从 Collector 本机调用。
3. SCF 仅保留用户 `symbols` 中存在且处于 active 状态的标的。
4. 将规范化 Symbol 批量写入 `target_dataset_id`。
5. 用户配置但 Binance 不存在的 Symbol 标记为规则错误，不进入 K 线任务。
6. 现货与合约必须使用不同 Symbol Dataset，例如 `binance_spot_symbols` 和 `binance_futures_symbols`。

### 4.3 普通行情采集规则

K 线等普通行情规则必须关联 Symbol Dataset，不直接保存另一份 Symbol 列表：

```json
{
  "data_type": "kline",
  "provider": "binance",
  "market_type": "spot",
  "symbol_source": "dataset",
  "symbol_dataset_id": "binance_spot_symbols",
  "target_dataset_id": "binance_spot_kline_1m",
  "frequency": "1m"
}
```

规则校验必须保证：

- `provider`、`market_type` 与 Symbol Dataset 一致；
- `symbol_dataset_id` 是 RECORD Dataset；
- `target_dataset_id` 是启用中的 TimeSeries Dataset；
- `frequency` 已在目标 Dataset 中启用；
- `symbol_source` 对普通行情只能是 `dataset`；
- `symbol_source=manual` 只用于 Symbol Dataset 规则。
- `symbols` 必须去重、规范化且数量不超过 1000；最终 SCF 事件仍必须通过 128KB 大小校验。

### 4.4 稳定标识

稳定 TaskInstance ID：

```text
sha256(space_id + rule_id + provider + market_type + dataset_id + subject_id + frequency)
```

TaskInstance ID 不包含 `scheduled_at`，避免每分钟为 1000 个标的创建 1000 条长期记录。

采集周期 ID：

```text
schedule_id = space_id + rule_id + frequency + target_data_time
```

物理批次 ID：

```text
batch_id = sha256(schedule_id + batch_kind + shard_index + attempt)
```

地域和函数是批次的执行位置，不参与 batch_id。这样同一周期内节点可用列表发生变化时，也不会为同一 shard 创建第二个批次。Planner 必须按 `region、function_name` 排序后做 round-robin，并将第一次选择结果随 BatchInvocation 一起持久化。

Storage 行幂等键直接采用规范 RowKey：

```text
space_id + dataset_id + subject_id + freq + data_time + series_tag
```

Binance K 线的 `data_time` 使用 K 线开盘时间，`series_tag` 使用 `venue:binance`。不另造基于 `close_time` 的数据幂等键。

## 5. 总体架构与状态闭环

```text
Collector Timer
  │
  ├─ 扫描启用规则和到期 Frequency
  ├─ 从 Symbol Dataset 展开 active subjects
  ├─ 在 SQLite 先保存 planned BatchInvocation
  └─ 通过 CloudNode 异步调用 SCF
         │
         ▼
SCF Fetcher（最长 10 秒）
  ├─ 受限并发请求 Binance REST
  ├─ 聚合每个 CollectionItem 的结果
  ├─ 成功数据一次批量写入 Storage Primary
  ├─ 发布 MarketFetchBatchCompleted 事件
  └─ 返回
         │
         ▼
EventBus MOOX_MARKET_FETCH
  │
  ▼
Collector Completion Consumer
  ├─ 事务更新 BatchInvocation
  ├─ 将临时失败落为 RetryItem
  ├─ 终结永久失败
  └─ ACK
         │
         ▼
Collector Timer
  ├─ 重投到期 RetryItem
  └─ 回收无完成事件的超时 BatchInvocation
```

### 5.1 BatchInvocation 状态

| 状态 | 含义 |
| --- | --- |
| `planned` | 批次已持久化，CloudNode 调用尚未确认；该状态也覆盖网络调用中的短暂窗口 |
| `dispatched` | CloudNode 已接受异步调用，记录了腾讯云 request_id |
| `succeeded` | 所有 CollectionItem 成功写入 Storage |
| `partial_failed` | 部分成功，部分进入 RetryItem 或永久失败 |
| `failed` | 没有成功项且已经终结 |
| `timed_out` | SCF 10 秒上限后又超过完成回执宽限期，仍未收到完成事件 |

强制顺序：

1. 在 SQLite 事务中插入 `planned` 批次及完整 `request_json`，同时设置 `deadline_at=planned_at+30s`。
2. 调用 CloudNode `InvokeFunction(Event)`。
3. 调用成功后使用条件更新将 `planned` 转为 `dispatched`，写入 `request_id`、`dispatched_at`，并将 `deadline_at` 更新为 `dispatched_at+20s`。
4. 如果完成事件先于 CloudNode 响应到达，调用返回后的更新只能补充 `request_id`，不得把 terminal 状态回退为 `dispatched`。
5. 调用同步失败时，不删除批次；仅当批次仍为 `planned` 时将全部 CollectionItem 写入 RetryItem。
6. Completion Consumer 收到完成事件后，事务更新批次和 RetryItem，再 ACK。
7. Timer 回收超过 `deadline_at` 的 `planned` 或 `dispatched` 批次，标记为 `timed_out`，并为尚未确认成功的 CollectionItem 创建 RetryItem。

Collector 在 CloudNode 接受调用后崩溃、SCF 超时、SCF 进程退出或 EventBus 发布失败时，即使没有完成回执，Collector 重启后仍能通过 `deadline_at` 回收整批。由此产生的少量重复调用由 Storage RowKey 幂等吸收。

### 5.2 晚到完成事件

完成事件晚于 `timed_out` 到达时：

1. Consumer 仍处理事件，并设置 `late_completion=true`。
2. 对事件中已经成功的 CollectionItem，取消尚未重新投递的 RetryItem。
3. 已经投递的新批次不强制取消；Storage RowKey Upsert 保证少量重复写不会产生重复 K 线。
4. 不因为晚到事件再次创建相同 RetryItem。

### 5.3 SCF 调用载荷

CloudNode 异步调用的 JSON 使用固定 `action=market_fetch`：

```json
{
  "action": "market_fetch",
  "request_id": "batch-id",
  "data": {
    "batch_id": "batch-id",
    "schedule_id": "crypto/rule/1m/2026-08-01T12:00:00Z",
    "batch_kind": "realtime",
    "space_id": "crypto",
    "rule_id": "rule-binance-spot-kline-1m",
    "provider": "binance",
    "market_type": "spot",
    "dataset_id": "binance_spot_kline_1m",
    "frequency": "1m",
    "items": [
      {
        "task_id": "stable-task-id",
        "subject_id": "BTC-USDT",
        "external_symbol": "BTCUSDT",
        "target_data_time": "2026-08-01T11:59:00Z",
        "bar_limit": 3
      }
    ]
  }
}
```

`batch_kind` 允许：

- `realtime`：最多 10 个 items，每项 `bar_limit=3`；
- `catchup`：恰好 1 个 item，必须携带 `start_time`，`bar_limit<=1000`；
- `symbol_snapshot`：恰好 1 个规则项，携带去重后的 `manual_symbols`，调用一次 exchangeInfo。

Handler 必须拒绝未知字段组合、空 Dataset、重复 task_id、跨 Dataset items 和超过配置上限的载荷。序列化后的异步事件必须小于 128KB；部署前测试以最大长度 Symbol 和 10 个 realtime items 验证该边界。

## 6. 实时采集与历史补采分离

### 6.1 实时批次

实时 K 线批次不再逐标的读取 Storage 水位，也不执行最多 5000 根的历史追赶。

每个 CollectionItem：

1. 请求 Binance 最近 3 根 K 线。
2. 过滤尚未收盘的 K 线。
3. 将已收盘 K 线转换为 Storage RowKey。
4. 依靠 Storage Upsert 覆盖重复行。

选择最近 3 根的原因：

- 正常周期只新增 1 根；
- 某一分钟调用失败时，下一周期可以自动补回短缺口；
- 固定小窗口能控制 HTTP 响应大小和 10 秒预算；
- 不需要在 SCF 内先读取 Storage 水位。

### 6.2 Gap Audit 与 CatchupBatch

历史缺口不进入每分钟实时批次。Collector 在现有 Timer 中每 10 分钟执行一次轻量 Gap Audit：

1. 先筛选最近成功时间已经落后 3 个 Frequency 的 TaskInstance，只对这些候选项读取 Storage 最新水位。
2. 若水位落后目标时间超过实时窗口覆盖范围，生成 CatchupBatch。
3. 一个 CatchupBatch 只包含 1 个 Symbol。
4. 单次 Binance 请求最多拉取 1000 根；函数内不翻页。
5. 如果仍有缺口，下一个 Gap Audit 继续生成下一段 CatchupBatch。
6. CatchupBatch 与实时批次使用不同 `batch_kind`，且每分钟最多投递 5 个，避免挤占实时调用。

短时停采由最近 3 根自动恢复；长时间停采由 CatchupBatch 分段恢复。实时批次不再承担长历史补采。

## 7. SCF 执行预算与环境变量

### 7.1 固定函数配置

```toml
memory_size = 64
timeout_seconds = 10
```

不配置预置并发。部署器必须将腾讯云异步自动重试次数设为 0，避免平台自动重试和 Collector 重试叠加。

### 7.2 首版运行参数

```text
MOOX_FETCH_MAX_INFLIGHT_REQUESTS=5
MOOX_FETCH_REQUEST_TIMEOUT_MS=2000
MOOX_FETCH_HTTP_MAX_ATTEMPTS=1
MOOX_FETCH_STORAGE_MAX_ATTEMPTS=1
MOOX_FETCH_REALTIME_BATCH_SIZE=10
MOOX_FETCH_REALTIME_BAR_LIMIT=3
MOOX_FETCH_CATCHUP_BATCH_SIZE=1
MOOX_FETCH_CATCHUP_BAR_LIMIT=1000
MOOX_FETCH_COMMIT_RESERVE_MS=2000
```

`MOOX_FETCH_HTTP_MAX_ATTEMPTS=1` 表示 Binance 总共只发起一次 HTTP 请求，不是“失败后再重试一次”。`MOOX_FETCH_STORAGE_MAX_ATTEMPTS=1` 同样禁止沿用当前 Storage Client 的三次内层重试；Storage 临时失败交给 Collector RetryItem。

### 7.3 Deadline 规则

1. 从 SCF `context.Deadline()` 读取平台 deadline；若运行库没有提供，则以 Handler 开始时间加 10 秒作为保守 fallback。
2. 计算 `work_deadline = platform_deadline - commit_reserve`。
3. 到达 `work_deadline` 后不再启动新的 Binance 请求。
4. 已经运行的请求使用子 Context 取消。
5. 未启动和被取消的 CollectionItem 标记为 `deadline_exhausted`，由 Collector 重试。
6. 最后约 2 秒只用于 Storage 批量提交和完成事件发布。

首版验收要求：

```text
正常实时批次 P99 Handler Duration < 8s
Storage commit P99 < 1s
Completion publish P99 < 500ms
ResourceLimitReached = 0
SCF timeout = 0
```

批次大小只有在以上门槛连续满足后，才允许从 10 调到 20、30 或 50。

## 8. custom.toml 与配置真值

### 8.1 初始化示例

`[tencent_cloud].region` 继续作为控制面默认地域。Fetcher 地域池使用独立配置：

```toml
[scf_fetcher]
enabled = true
namespace = "default"
runtime = "Go1"
function_prefix = "moox-fetcher"
memory_size = 64
timeout_seconds = 10
realtime_batch_size = 10
realtime_bar_limit = 3
catchup_batch_size = 1
catchup_bar_limit = 1000
max_inflight_requests = 5
request_timeout_ms = 2000
http_max_attempts = 1
storage_max_attempts = 1
commit_reserve_ms = 2000
max_retry_attempts = 3
retry_delays = ["5s", "30s", "2m"]
stagger_enabled = false

[[scf_fetcher.regions]]
region = "ap-guangzhou"
display_name = "华南地区（广州）"
enabled = true
function_count = 1

[[scf_fetcher.regions]]
region = "ap-shanghai"
display_name = "华东地区（上海）"
enabled = true
function_count = 1

[[scf_fetcher.regions]]
region = "ap-beijing"
display_name = "华北地区（北京）"
enabled = true
function_count = 1

[[scf_fetcher.regions]]
region = "ap-chengdu"
display_name = "西南地区（成都）"
enabled = true
function_count = 1

[[scf_fetcher.regions]]
region = "ap-hongkong"
display_name = "港澳台地区（中国香港）"
enabled = true
function_count = 1
```

配置校验：

- `memory_size` 必须等于 64；若真实压测出现 OOM，停止扩量并重新评审内存与成本，不静默改成更大值；
- timeout 必须为 10；
- `request_timeout_ms + commit_reserve_ms < 10000`；
- `max_inflight_requests > 0`；
- `realtime_batch_size` 初始不得超过 10；
- `http_max_attempts` 必须为 1；
- `storage_max_attempts` 必须为 1；
- 地域不能重复；
- 每个启用地域 `function_count >= 1`；
- 所有函数的环境变量总大小不得超过腾讯云限制；
- 未配置 `[scf_fetcher]` 时不创建 Fetcher，不影响不使用 SCF 的用户。

### 8.2 真值来源

不新增独立 `t_scf_fetcher_configs` 表。

配置流向：

```text
custom.toml（用户初始化输入）
  → moox-cli setup validate
  → CloudNode NodeCreateItem / NodeDeployItem
  → t_cloud_nodes.c_metadata（期望配置）
  → 腾讯云函数配置和环境变量（运行配置）
```

CloudNode `c_metadata` 至少保存：

```json
{
  "biz_type": "market_fetcher",
  "supported_workloads": ["symbol_snapshot", "kline_realtime", "kline_catchup"],
  "memory_size": 64,
  "timeout_seconds": 10,
  "max_inflight_requests": 5,
  "realtime_batch_size": 10,
  "request_timeout_ms": 2000,
  "http_max_attempts": 1,
  "storage_max_attempts": 1
}
```

`biz_type=market_fetcher` 是节点的业务身份，不是兼容旧执行模式的开关。Collector 只读取该业务类型且部署状态为 Active 的节点，不维护第二份函数配置表，也不保留 `execution_mode`、旧 Worker 模式或双模式分支。

## 9. EventBus 完成事件与重试

### 9.1 只定义一个业务事件

第一版不再定义独立的裸 `moox.market.collect.retry.v1` Subject。SCF 只发布一个受治理事件：

```text
event name: market.fetch.batch.completed
version: 1
stream: MOOX_MARKET_FETCH
owner: collector
subject: moox.market.fetch.batch.completed.v1.<space>.<dataset>
```

失败明细包含在批次完成事件中。Collector 消费事件后，把可重试失败写入 SQLite RetryItem。这样可以同时完成“执行回执”和“失败投递”，减少 SCF 的 EventBus 发布次数。

### 9.2 Proto 契约

新增 `packages/marketfetchpb/market_fetch_events.proto`：

```protobuf
syntax = "proto3";

package trpc.moox.marketfetch;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/mooyang-code/moox/packages/marketfetchpb;marketfetchpb";

enum FetchItemStatus {
  FETCH_ITEM_STATUS_UNSPECIFIED = 0;
  FETCH_ITEM_STATUS_SUCCESS = 1;
  FETCH_ITEM_STATUS_RETRYABLE_FAILURE = 2;
  FETCH_ITEM_STATUS_PERMANENT_FAILURE = 3;
}

message FetchItemResult {
  string task_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string frequency = 4;
  string target_data_time = 5;
  FetchItemStatus status = 6;
  uint64 rows_written = 7;
  string error_type = 8;
  string error_summary = 9;
  int64 retry_after_ms = 10;
}

message MarketFetchBatchCompleted {
  string batch_id = 1;
  string schedule_id = 2;
  string batch_kind = 3;
  string node_id = 4;
  string function_name = 5;
  string region = 6;
  uint32 planned_count = 7;
  repeated FetchItemResult results = 8;
  int64 duration_ms = 9;
  int64 commit_duration_ms = 10;
  google.protobuf.Timestamp completed_at = 11;
}
```

事件约束：

- EventMessage `event_id == batch_id`；
- EventMessage `space_id` 与批次一致；
- EventMessage `subject_id == dataset_id`；
- `planned_count == len(results)`；
- 每个结果的 Dataset 必须与事件 Dataset 一致；
- `error_summary` 最长 256 字节，详细堆栈只写 CLS；
- 一个 BatchInvocation 只能包含一个目标 Dataset，保证一次 Storage Primary 调用只有一个路由组。

### 9.3 EventBus 拓扑

在 `modules/eventbus/config/app.yaml` 新增：

```yaml
- name: MOOX_MARKET_FETCH
  subjects: ["moox.market.fetch.batch.completed.v1.>"]
  retention: work_queue
  discard: old
  storage: file
  replicas: 1
  max_age: 24h
  duplicates: 10m
  max_bytes: 268435456
```

Collector Consumer：

```text
durable: collector-market-fetch-completion-v1
ack_wait: 30s
max_deliver: 5
max_ack_pending: 128
deliver_policy: all
```

处理规则：

1. 解码或契约校验失败：TERM，并记录中文诊断日志。
2. SQLite 临时失败：NAK 5 秒。
3. 批次不存在：TERM，并告警 `unknown_batch_completion`。
4. 批次结果事务保存成功：ACK。
5. 重复 `event_id`：幂等 ACK，不重复创建 RetryItem。

### 9.4 EventBus 凭据

新增两种最小权限角色，凭据由现有 Admin EventBus credential 流程生成，不写入 `custom.toml`：

| 角色 | 运行位置 | 权限 |
| --- | --- | --- |
| `market-fetch-publisher` | SCF | 只允许发布 `moox.market.fetch.batch.completed.v1.>` 和订阅自身 `_INBOX.>` |
| `collector-market-fetch-consumer` | Collector 控制节点 | 只允许创建、绑定、拉取和 ACK `MOOX_MARKET_FETCH/collector-market-fetch-completion-v1` |

`market-fetch-publisher` 不允许创建 Consumer、读取其他 Stream 或发布 Observability 指标。`collector-market-fetch-consumer` 不允许发布业务事件。发布包只注入 publisher 凭据内容和 CA，不复制仓库 `custom.toml`。

### 9.5 可重试和永久错误

可重试：

```text
network_timeout
connection_reset
dns_temporary_failure
http_429
http_500
http_502
http_503
http_504
storage_unavailable
event_deadline_exhausted
scf_completion_timeout
```

永久失败：

```text
invalid_symbol
invalid_frequency
invalid_market_type
http_400
authentication_failed
permission_denied
invalid_rule_config
```

HTTP 429 优先采用 `Retry-After`；没有该字段时使用 5 秒、30 秒、2 分钟。最多重试 3 次。

当后续实时批次已经成功写入同一 Dataset、Subject、Frequency 且 `target_data_time` 不早于 RetryItem 目标时间时，旧 RetryItem 标记为 `superseded`，不再浪费 SCF 调用。

## 10. Collector 数据库设计

数据库按全新项目直接重建，不提供迁移脚本：

- `t_collector_task_rules` 使用 `c_provider` 和 `c_market_type`，删除 `c_exchange`；
- `t_collector_task_instances` 使用 `c_provider`、`c_market_type` 和 `c_frequency`，统一用 `c_subject_id` 表示标的，删除 `c_cloud_job_item_id`、`c_exchange`、`c_market`、`c_interval` 和重复的 `c_symbol`；
- Repository、Proto 和 JSON 字段统一使用 `provider`、`market_type`、`frequency`，不接受旧字段别名；
- Schema 测试直接从空 SQLite 创建数据库并断言旧列不存在；
- 实施与部署均不编写 ALTER、COPY、backfill 或双读逻辑。

### 10.1 BatchInvocation

在 `modules/collector/schema/collector.sql` 直接定义：

```sql
CREATE TABLE IF NOT EXISTS t_collector_fetch_batches (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_batch_id TEXT NOT NULL,
    c_parent_batch_id TEXT NOT NULL DEFAULT '',
    c_schedule_id TEXT NOT NULL,
    c_batch_kind TEXT NOT NULL,
    c_shard_index INTEGER NOT NULL,
    c_rule_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_region TEXT NOT NULL,
    c_node_id TEXT NOT NULL,
    c_function_name TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_attempt INTEGER NOT NULL DEFAULT 1,
    c_request_id TEXT NOT NULL DEFAULT '',
    c_request_json TEXT NOT NULL,
    c_planned_count INTEGER NOT NULL,
    c_success_count INTEGER NOT NULL DEFAULT 0,
    c_retry_count INTEGER NOT NULL DEFAULT 0,
    c_permanent_failed_count INTEGER NOT NULL DEFAULT 0,
    c_error_summary TEXT NOT NULL DEFAULT '',
    c_late_completion INTEGER NOT NULL DEFAULT 0,
    c_dispatched_at DATETIME,
    c_deadline_at DATETIME,
    c_completed_at DATETIME,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_batch
ON t_collector_fetch_batches (c_space_id, c_batch_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_schedule_shard
ON t_collector_fetch_batches
(c_space_id, c_schedule_id, c_batch_kind, c_shard_index, c_attempt);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_deadline
ON t_collector_fetch_batches (c_status, c_deadline_at);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_schedule
ON t_collector_fetch_batches (c_space_id, c_schedule_id);
```

### 10.2 RetryItem

```sql
CREATE TABLE IF NOT EXISTS t_collector_fetch_retry_items (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_retry_key TEXT NOT NULL,
    c_source_batch_id TEXT NOT NULL,
    c_rule_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_target_data_time DATETIME NOT NULL,
    c_task_json TEXT NOT NULL,
    c_attempt INTEGER NOT NULL,
    c_status TEXT NOT NULL,
    c_next_retry_at DATETIME,
    c_last_error_type TEXT NOT NULL,
    c_last_error_summary TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_fetch_retry
ON t_collector_fetch_retry_items (c_space_id, c_retry_key);

CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_due
ON t_collector_fetch_retry_items (c_status, c_next_retry_at);
```

`retry_key`：

```text
sha256(space_id + rule_id + dataset_id + subject_id + frequency + target_data_time)
```

### 10.3 保留策略

- `succeeded`、`partial_failed` 批次保留 48 小时，覆盖 EventBus 24 小时消息窗口；
- `failed`、`timed_out` 批次保留 7 天；
- `resolved`、`superseded` RetryItem 保留 7 天；
- 每小时由 Collector Timer 执行一次清理；
- 稳定 TaskInstance 不按周期删除；
- 不创建每个 Symbol、每分钟一行的长期历史表。

## 11. Storage 写入边界

SCF 行情结果只写 Storage Primary：

```text
SCF
  → PrimaryStore.UpsertFields(source_event_id=batch_id)
  → DataNode 原子写入 Row + source marker + outbox
  → Storage Outbox 发布 DatasetRowsUpserted
  → Storage View / Factor 等下游消费
```

禁止以下路径：

- SCF 直接发布 `DatasetRowsUpserted` 冒充已经提交的数据；
- 同一批次跨多个 Dataset；
- Storage 写失败但将 CollectionItem 标记为成功；
- 先发布完成成功事件，再写 Storage。

正确顺序：

1. 聚合所有成功 API 响应。
2. 构造同一 Dataset 的 Storage Rows。
3. 一次调用 Primary `UpsertFields`，`source_event_id=batch_id`。
4. Storage 成功后，将相应 CollectionItem 标记为成功。
5. Storage 失败时，将这些 CollectionItem 统一标为 `storage_unavailable` 可重试。
6. 最后发布 `MarketFetchBatchCompleted`。

## 12. Collector Timer 与分片

复用 `trpc.moox.collector.schedule.timer`：

```yaml
- name: trpc.moox.collector.schedule.timer
  port: 11403
  network: "*/10 * * * * *?scheduler=collectorSchedule&startAtOnce=1"
  protocol: timer
  timeout: 30000
```

注册时必须用 `packages/timerjob.Job` 包装，设置 25 秒内部超时并启用本地防重入。若上一轮尚未结束，下一次触发记录 `skipped_overlap`，不并发扫描。

Dispatcher 对 CloudNode 调用使用最多 20 个并发；这是控制面请求的有界并发，不是地域错峰。100 个批次必须在 Timer 的 25 秒内部预算内完成“持久化 + 调用提交”，Timer 不等待 SCF 执行结果。

每轮执行顺序：

1. 回收超过 `deadline_at` 的 `dispatched` 批次。
2. 查询并聚合到期 RetryItem。
3. 查询启用中的 TaskRule。
4. 判断各 Frequency 是否到期。
5. Symbol 规则执行一次元数据同步规划。
6. K 线规则读取关联 Symbol Dataset 的 active subjects。
7. Upsert 稳定 TaskInstance。
8. 以 Dataset 为边界生成 CollectionItem。
9. 以初始 batch size 10 分组。
10. 按启用地域和函数 round-robin 分配批次。
11. 先保存 `planned`，再异步调用 CloudNode。
12. 每 10 分钟执行 Gap Audit；每小时执行历史状态清理。

确定性的 `batch_id` 和 `space_id + schedule_id + batch_kind + shard_index + attempt` 唯一索引，保证 Timer 每 10 秒扫描时不会重复创建同一周期的同一分片；`schedule_id` 用于聚合一个周期内的多个批次，不要求单列唯一。

## 13. 多地域和函数数量

### 13.1 第一版

第一版按以下方式运行：

```text
广州 1 个函数
上海 1 个函数
北京 1 个函数
成都 1 个函数
中国香港 1 个函数
```

1000 个 Symbol、batch size 10 时每分钟约 100 次调用，按 5 个地域 round-robin 后每地域约 20 次。一个函数名可以由 SCF 平台按并发自动扩出多个实例，不需要预先创建 10 个同地域函数。

### 13.2 一次性出口探针

出口探针不是腾讯云系统函数，也不是常驻 Watchdog。它是同一个行情 Fetcher Handler 的一次性诊断动作，只在函数部署完成后、Collector Scheduler 启用前或人工排障时同步调用：

```json
{
  "action": "egress_probe",
  "provider": "binance",
  "market_type": "spot"
}
```

执行流程保持简单：

1. `moox-cli collector function probe-egress --file ./custom.toml` 从 CloudNode 枚举所有 `biz_type=market_fetcher` 且部署状态为 Active 的节点。
2. CLI 以最多 5 个并发，通过 CloudNode `InvokeFunction(RequestResponse)` 同步调用每个节点一次。
3. SCF 使用 2 秒超时请求 `https://api.ipify.org?format=json`，解析本次调用的公网出口 IP。
4. SCF 再使用 2 秒超时请求当前 `provider + market_type` 对应的轻量 `time` 或 `ping` 接口；必须使用真实采集配置中的 Base URL，不能另写一个只供探针使用的 Binance 地址。
5. Handler 将结果直接返回给 CLI；不写 Storage、不发布 EventBus、不更新业务指标。
6. CLI 将 CloudNode 已知字段和 Handler 返回字段合并，输出逐节点表格和 `distinct_outbound_ips` 汇总。

单节点结果固定为：

```json
{
  "node_id": "moox-fetcher-ap-guangzhou-0",
  "region": "ap-guangzhou",
  "function_name": "moox-fetcher-ap-guangzhou-0",
  "outbound_ip": "203.0.113.10",
  "provider_status": 200,
  "latency_ms": 86,
  "checked_at": "2026-08-01T12:00:00Z",
  "error": ""
}
```

失败语义：

- 公网 IP 服务失败时，当前节点结果失败并记录错误，不把空 IP 当作有效出口；
- Binance 探测失败时，当前节点不通过部署验收；
- 一个节点失败不阻止 CLI 完成其他节点探测，CLI 最终以非零退出码结束并打印全部失败节点；
- `coldstart`、`http_429` 和资源使用量不由出口探针判断，分别从腾讯云日志、真实采集完成事件和账单中观察。

### 13.3 扩容门槛

只有同时满足以下条件，才扩大函数数量：

1. 多地域或多函数确实观察到更多有效出口 IP；
2. 429 数量相对单地域明显下降；
3. P99 Duration 小于 8 秒；
4. ResourceLimitReached 为 0；
5. 增加函数后的 GBs 和调用成本仍可接受。

用户确实要求 50 个函数时，再按 5 个地域各 10 个部署；分片仍只执行一次，不能在 5 个地域复制采集同一批数据。

## 14. 监控与告警

### 14.1 指标来源

短时 SCF 不启动后台 metrics reporter。SCF 通过批次完成事件传递结果，Collector Completion Consumer 更新内存指标，再由 Collector 现有 `packages/report` 定时统一上报 Observability EventBus。

冷启动精确数据第一版保留在腾讯云 CLS 和 SCF 控制台，不新增 CLS 抓取服务。Collector 只报告批次、采集项、批次总耗时、重试积压和最后成功时间；HTTP 请求耗时与 Storage 提交耗时进入结构化日志，不单独生成指标。

### 14.2 指标命名

统一使用 `moox_collector_market_fetch_` 前缀，duration 使用秒：

```text
moox_collector_market_fetch_batches_total
moox_collector_market_fetch_batch_duration_seconds
moox_collector_market_fetch_items_total
moox_collector_market_fetch_retry_pending
moox_collector_market_fetch_last_success_timestamp_seconds
```

每个指标只允许以下标签：

```text
batches_total:
  space_id, dataset_id, frequency, region, batch_kind, outcome

batch_duration_seconds:
  space_id, dataset_id, frequency, region, batch_kind

items_total:
  space_id, dataset_id, frequency, outcome

retry_pending:
  space_id, dataset_id, frequency

last_success_timestamp_seconds:
  space_id, dataset_id, frequency
```

`outcome` 按指标使用固定枚举：

```text
batches_total:
  success, partial_failed, failed, timeout

items_total:
  success, http_429, http_5xx, network_error, storage_error, invalid_request
```

不再单独定义 retry、completion timeout、HTTP 429 和 HTTP 5xx counter：

- 重试原因从 `items_total{outcome=~"http_429|http_5xx|network_error"}` 得到；
- Completion 超时从 `batches_total{outcome="timeout"}` 得到；
- 429 和 5xx 分别从 `items_total` 对应 outcome 得到；
- 详细请求耗时、Storage 提交耗时、原始错误和 `Retry-After` 写结构化日志。

`provider` 和 `market_type` 可由 `dataset_id` 关联配置获得，不重复放进指标标签。`symbol`、`batch_id`、`function_name`、`node_id` 和 SCF 实例 ID 只进入日志或 SQLite，禁止作为指标标签。

### 14.3 Monitor 检查

Monitor 为每个启用 Dataset + Frequency 检查：

- 最近一次成功周期时间；
- Dataset 最新数据时间；
- 周期 planned/success/retry/permanent_failed 数量；
- 批次完成率；
- Completion 超时数量；
- RetryItem 积压和最老等待时间；
- 连续失败周期数；
- CloudNode Fetcher 部署是否 Active。

短时 SCF 不再使用 `scf:heartbeat` 判断函数在线。未被调用的备用函数没有心跳是正常状态。

### 14.4 中文告警

示例：

```text
[严重] 行情采集批次未完成：binance_spot_kline_1m/1m
异常原因：计划调用 100 个批次，20 秒内仅收到 97 个完成回执
建议处理：检查缺失批次对应地域的 SCF 日志、CloudNode request_id 和 EventBus 连通性
诊断信息：schedule_id=... missing_batch_count=3 oldest_deadline=...
```

告警诊断至少包含 `schedule_id`、`batch_id`、`region`、`node_id`、`request_id` 和最后错误摘要。

## 15. 全新初始化与删除范围

本项目尚未上线，本方案只实现一种 Collector SCF 执行模型：短时按需行情 Fetcher。实现时不提供旧常驻 Worker 的兼容开关，不迁移旧任务和运行状态，不做双读、双写或双 Scheduler，也不保留旧函数作为回滚链路。

### 15.1 直接删除的旧路径

- 删除 Collector SCF 的常驻 TaskRunner、`select {}`、Keepalive Handler 和后台 observability timer；
- 删除 Collector 通过旧 CloudNode JobItem work queue 投递行情采集的生产路径；
- 从 TaskInstance Schema 和模型删除 `c_cloud_job_item_id` 和重复的 `c_symbol`，统一使用 `subject_id`；
- 将 `exchange` 直接重命名为 `provider`，将 `market` 直接收敛为 `market_type`，不保留别名字段或兼容解析；
- 删除专门服务旧常驻 Collector 的配置、环境变量、测试和文档；
- 删除 Monitor 针对行情 Fetcher 的 `scf:heartbeat` 检查，改为 deployment、batch completion、retry pending 和 Dataset freshness；
- CloudNode 若仍为其他业务节点保留 Heartbeat Maintainer，必须根据 `biz_type` 排除 `market_fetcher`，不能再向它发送 Keepalive。

CloudNode 的通用 JobItem 能力只有在其他模块仍有明确引用时才保留；Collector 不得为兼容旧行情链路继续依赖它。

### 15.2 数据与 EventBus 初始化

直接重写 `modules/collector/schema/collector.sql`，新 Schema 不包含旧兼容列。部署时按以下顺序进行一次性初始化：

1. 停止 Collector、CloudNode 和 Monitor，确保不会生成新任务或告警。
2. 使用 `moox-cli collector function delete --confirm` 提交异步删除任务，并等待所有旧 Collector SCF 节点删除完成。
3. 删除 Collector SQLite `./data/moox_collector.db`、CloudNode SQLite `./data/moox_cloudnode.db`、Monitor SQLite `./data/monitor/monitor.db` 和 `job_item.history_dir` 指向的旧 JobItem 历史目录，再通过 `custom.toml` 重新初始化所需配置。
4. 删除旧 Collector JetStream stream、consumer 和 KV 数据，按本计划重新创建 `MOOX_MARKET_FETCH` 及 Completion durable。
5. 使用全新 Schema 启动 Collector、CloudNode 和 Monitor；禁止编写 ALTER、数据回填或旧字段读取代码。
6. 发布新的短时 Fetcher，回读验证 64MB、10 秒和环境变量。
7. 执行 `probe-egress`，全部启用节点通过后再创建 Symbol 与 Kline 规则。
8. 先启用 10 个 Symbol 验证真实 Storage 数据，再逐步扩大到 100 和 1000 个 Symbol。

### 15.3 发布失败处理

发布失败时只回退代码，不恢复旧常驻链路：

1. 禁用 Collector Scheduler，停止创建 BatchInvocation。
2. 删除本轮新建的 Fetcher 函数。
3. 修复代码并重新构建；需要清理状态时，直接删除新建的 batch、retry 和 EventBus 数据。
4. 重新发布后，从出口探针和 10 个 Symbol 验收重新开始。

Git 可以回退到上一个可构建提交，但旧常驻 Worker、Keepalive 和旧 JobItem 行情链路不作为可恢复方案。

## 16. 文件改造地图

### 新增

- `packages/marketfetchpb/market_fetch_events.proto`：批次完成事件 Proto。
- `packages/marketfetchpb/Makefile`、`go.mod`、`go.sum`：独立 Proto module。
- `modules/collector/internal/domain/fetch_batch.go`：BatchInvocation 和 RetryItem 领域模型。
- `modules/collector/internal/store/fetch_batch.go`：批次 Repository 和状态转换。
- `modules/collector/internal/store/fetch_retry.go`：RetryItem Repository。
- `modules/collector/internal/planner/fetch_batch_builder.go`：CollectionItem、分片和地域分配。
- `modules/collector/internal/scfinvoker/client.go`：查询短时 Fetcher 节点并调用 CloudNode `InvokeFunction(Event)`。
- `modules/collector/internal/marketfetch/dispatcher.go`：先持久化批次，再异步调用并条件更新状态。
- `modules/collector/internal/marketfetch/executor.go`：10 秒批次执行器。
- `modules/collector/internal/marketfetch/storage.go`：单 Dataset Storage Primary 批量写入。
- `modules/collector/internal/marketfetch/completion.go`：完成事件构造和发布。
- `modules/collector/internal/marketfetch/egress_probe.go`：一次性公网出口和数据源连通性探测。
- `modules/collector/internal/marketfetch/metrics.go`：五个低基数行情采集指标。
- `modules/collector/internal/marketfetch/consumer.go`：Collector Completion Consumer。
- `modules/collector/internal/marketfetch/recovery.go`：超时回收、RetryItem 和 Gap Audit。
- `modules/collector/test/short_lived_market_fetch_e2e_test.go`：本地完整链路 E2E。

### 修改

- `go.work`、根 `Makefile`：加入和生成 `marketfetchpb`。
- `packages/events/registry.go`、`validation.go`、对应测试：注册完成事件。
- `packages/events/go.mod`、`go.sum`：依赖 `marketfetchpb`。
- `modules/collector/go.mod`、`go.sum`：依赖 `marketfetchpb`。
- `modules/admin/cmd/cli/eventbus_credentials.go`、对应测试：生成 Fetcher publisher 和 Collector consumer 最小权限凭据。
- `scripts/verify-event-contracts.sh`：锁定两个新角色的 Subject ACL。
- `modules/eventbus/config/app.yaml`、配置测试：增加 `MOOX_MARKET_FETCH`。
- `modules/collector/schema/collector.sql`：直接重建规则、实例、batch 和 retry 表，不保留旧列。
- `modules/collector/internal/store/database.go`：暴露新 Repository。
- `modules/collector/proto/collector.proto`：`exchange` 收敛为 `provider`。
- `modules/collector/internal/domain/task_rule.go`、`task_instance.go`：同步命名和稳定任务语义。
- `modules/collector/internal/jobs/symbol/*`：手动 Symbol allowlist 和目标 Dataset 校验。
- `modules/collector/internal/jobs/kline/*`：只允许 Dataset Symbol 来源。
- `modules/collector/internal/sources/binance/kline.go`：拆分实时小窗口和 Catchup 单页路径。
- `modules/collector/internal/sources/binance/kline_cursor.go`：移除实时路径的 5000 根循环。
- `modules/collector/internal/sources/binance/storage_rpc.go`：支持 `source_event_id`，短时路径只尝试一次 Storage 写入。
- `modules/collector/internal/rpc/service.go`、`schedule.go`：调用新 Scheduler。
- `modules/collector/internal/bootstrap/bootstrap.go`：注册防重入 Timer 和 Completion Consumer。
- `modules/collector/config/trpc_go.yaml`：10 秒扫描 Timer。
- `modules/collector/internal/model/types.go`：增加 `market_fetch` 事件输入。
- `modules/collector/internal/serverless/handler.go`：实现单次批量 Handler，删除 Keepalive 分支。
- `modules/collector/cmd/scf/main.go`、`observability.go`：删除常驻 Runner 和后台 reporter。
- `modules/cloudnode/internal/rpc/heartbeat_maintainer.go`：按 `biz_type` 排除行情 Fetcher。
- `modules/cloudnode/internal/store/node.go`、`internal/rpc/node.go`：短时函数部署状态语义。
- `modules/cli/internal/setup/config/config.go`、测试：支持严格解析 `[scf_fetcher]`。
- `custom.toml.example`：加入初始化示例。
- `modules/cli/internal/command/collector.go`、测试：支持多地域函数池发布、异步删除和同步出口探针。
- `modules/monitor/internal/observability/overview.go`：展示部署和批次状态。
- `modules/monitor/internal/bootstrap/business_freshness.go`、`default_alerts.go`：替换 heartbeat 检查。

## 17. 实施任务

### Task 1：建立完成事件契约和 EventBus 拓扑

**文件：**

- Create: `packages/marketfetchpb/*`
- Modify: `go.work`、`Makefile`
- Modify: `packages/events/registry.go`、`validation.go`、`validation_test.go`
- Modify: `modules/eventbus/config/app.yaml`、`internal/config/config_test.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`、`eventbus_credentials_test.go`
- Modify: `scripts/verify-event-contracts.sh`

- [ ] 先写事件身份、planned_count、Dataset 一致性和错误摘要长度测试。
- [ ] 运行 `make proto && make test-event-contracts && make test-eventbus-topology`，确认新测试在注册前失败。
- [ ] 生成 `marketfetchpb`，注册 `market.fetch.batch.completed@1` 和 `MOOX_MARKET_FETCH`。
- [ ] 生成 `market-fetch-publisher.yaml` 和 `collector-market-fetch-consumer.yaml`，测试两者都无法访问契约外 Subject。
- [ ] 验证 Subject 为 `moox.market.fetch.batch.completed.v1.crypto.binance_spot_kline_1m`。
- [ ] 重新运行上述测试，预期全部通过。
- [ ] 提交：`feat(events): add market fetch completion contract`。

### Task 2：扩展 strict custom.toml 和 CloudNode 期望配置

**文件：**

- Modify: `modules/cli/internal/setup/config/config.go`、`config_test.go`
- Modify: `custom.toml.example`
- Modify: `modules/cli/internal/command/collector.go` 及测试
- Modify: `modules/cloudnode/internal/rpc/node.go` 及测试

- [ ] 写合法五地域配置、重复地域、batch size 超限、timeout 非 10、HTTP attempts 非 1 的失败测试。
- [ ] 运行 `cd modules/cli && go test -count=1 ./internal/setup/config ./internal/command`，确认新字段当前被 strict loader 拒绝。
- [ ] 增加 `SCFFetcher`、`SCFFetcherRegion` typed config 和本计划中的默认值。
- [ ] 将 `collector function publish submit` 改为接受 `--file ./custom.toml`，从 `scf_fetcher.regions` 展开函数池；Fetcher 模式不再使用单一 `--region` 和 `--node-count` 作为真值。
- [ ] 将配置转换为 NodeCreateItem metadata、config 和 environment；metadata 固定使用 `biz_type=market_fetcher`，不得生成 `execution_mode`。
- [ ] 发布后通过 DescribeFunction 回读 memory、timeout 和环境变量；任一不一致则发布任务失败。
- [ ] 运行 `make verify-custom-setup`。
- [ ] 提交：`feat(setup): add short-lived scf fetcher config`。

### Task 3：增加 BatchInvocation 和 RetryItem 持久状态

**文件：**

- Create: `modules/collector/internal/domain/fetch_batch.go`
- Create: `modules/collector/internal/store/fetch_batch.go`、`fetch_retry.go`
- Modify: `modules/collector/schema/collector.sql`
- Modify: `modules/collector/internal/store/database.go`
- Test: 对应 `*_test.go`

- [ ] 写 planned→dispatched→terminal、重复完成、晚到完成、到期查询和 RetryItem 幂等测试。
- [ ] 写“CloudNode 已接受但 Collector 在状态更新前退出”时 stale planned 能被回收的测试。
- [ ] 写完成事件先到达、CloudNode 响应后到达时 terminal 状态不会回退的测试。
- [ ] 运行 `cd modules/collector && go test -count=1 ./internal/store`，确认缺少 Repository 时失败。
- [ ] 直接重写空库 Schema 和 Repository，删除旧列并实现状态转换与保留清理；不编写迁移、回填或旧字段读取代码。
- [ ] 写 Schema 测试，断言 `c_cloud_job_item_id`、`c_exchange`、`c_market`、`c_interval` 和 `c_symbol` 均不存在。
- [ ] 验证同一 `space_id + batch_id` 不能重复插入。
- [ ] 验证 Completion Consumer 重放不会重复增加 attempt。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/store`。
- [ ] 提交：`feat(collector): persist market fetch batches and retries`。

### Task 4：收敛规则、稳定 TaskInstance 和 CollectionItem 规划

**文件：**

- Modify: `modules/collector/proto/collector.proto`
- Modify: `modules/collector/internal/domain/task_rule.go`、`task_instance.go`
- Modify: `modules/collector/internal/jobs/symbol/*`、`jobs/kline/*`
- Create: `modules/collector/internal/planner/fetch_batch_builder.go`
- Test: `internal/jobs/*/*_test.go`、`internal/planner/fetch_batch_builder_test.go`

- [ ] 写 Symbol manual allowlist、`symbol_snapshot` 只生成一个规则项、Kline 必须引用 Dataset、现货/合约 Dataset 不匹配的测试。
- [ ] 写 1000 个 active subjects 生成 100 个 size=10 批次的测试。
- [ ] 写连续两次相同 schedule 生成相同 batch_id、节点列表变化也不重复建 shard、不同 schedule 仍复用稳定 TaskInstance 的测试。
- [ ] 写 Proto、JSON 和数据库只接受 `provider`、`market_type`、`frequency`，拒绝旧字段别名的测试。
- [ ] 串行运行 `make proto`，不要与 Go 测试并发。
- [ ] 实现 `provider` 命名、规则校验、稳定 TaskInstance 和 round-robin 地域分配。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/jobs/... ./internal/planner/... ./internal/domain/...`。
- [ ] 提交：`refactor(collector): plan stable short-lived market batches`。

### Task 5：实现实时小窗口和独立 Catchup

**文件：**

- Modify: `modules/collector/internal/sources/binance/kline.go`
- Modify: `modules/collector/internal/sources/binance/kline_cursor.go`
- Test: `kline_test.go`、`kline_cursor_test.go`

- [ ] 写实时路径不调用 `LatestTimeSeriesTime`、只请求最近 3 根的测试。
- [ ] 写 Catchup 一次只请求一个 Symbol、一页最多 1000 根且不循环翻页的测试。
- [ ] 写未收盘 K 线被过滤、RowKey 使用 open_time 和 `venue:binance` 的测试。
- [ ] 运行目标测试，确认当前 5000 根循环使新测试失败。
- [ ] 拆分 `FetchRealtime` 与 `FetchCatchupPage`，移除实时路径水位读取。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/sources/binance`。
- [ ] 提交：`refactor(collector): bound realtime kline fetch window`。

### Task 6：实现 10 秒 Market Fetch Executor 和 SCF Handler

**文件：**

- Create: `modules/collector/internal/marketfetch/executor.go`、`storage.go`、`completion.go`
- Modify: `modules/collector/internal/model/types.go`
- Modify: `modules/collector/internal/serverless/handler.go`
- Modify: `modules/collector/cmd/scf/main.go`
- Test: `internal/marketfetch/*_test.go`、`internal/serverless/handler_test.go`、`cmd/scf/main_test.go`

- [ ] 写并发永不超过 5、realtime batch 超过 10 被拒绝、8 秒停止新请求的测试。
- [ ] 写 `symbol_snapshot` 由 SCF 调用 exchangeInfo、按 allowlist 过滤并批量写 RECORD Dataset 的测试。
- [ ] 写 9 个慢请求加 1 个成功请求的 deadline 测试。
- [ ] 写 Storage 失败使已抓取项转为 retryable、Storage 成功后才发布成功事件的测试。
- [ ] 写短时路径 Storage 只调用一次，并为 Primary 请求传入 `source_event_id=batch_id` 的测试。
- [ ] 写事件发布失败时 Handler 返回错误且依赖 Collector 超时回收的测试。
- [ ] 写 `action=egress_probe` 依次探测公网 IP 和当前数据源轻量接口、任一失败返回明确错误且不调用 Storage/EventBus 的测试。
- [ ] 实现 `egress_probe.go`：两个外部请求各自最多 2 秒，返回 `outbound_ip`、`provider_status`、`latency_ms`、`checked_at` 和 `error`。
- [ ] 删除生产启动中的常驻 Runner、`select {}`、Keepalive 和后台 observability timer。
- [ ] 让 `cloudfunction.Start` 直接阻塞运行短时 Handler。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/marketfetch ./internal/serverless ./cmd/scf`。
- [ ] 运行 `make test-collector-scf-package-contract`。
- [ ] 提交：`feat(collector): run bounded short-lived scf market fetch`。

### Task 7：实现 Completion Consumer、重试和超时回收

**文件：**

- Create: `modules/collector/internal/marketfetch/consumer.go`、`recovery.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/rpc/service.go`
- Test: `internal/marketfetch/consumer_test.go`、`recovery_test.go`

- [ ] 写 Consumer “事务成功后 ACK、SQLite 失败 NAK、非法事件 TERM”的测试。
- [ ] 写 429 Retry-After、5s/30s/2m、最多 3 次和永久错误不重试测试。
- [ ] 写 planned 超过 30 秒、dispatched 超过 20 秒无完成事件后创建 RetryItem 的测试。
- [ ] 写晚到成功事件取消未投递 RetryItem、已经投递允许幂等重复的测试。
- [ ] 写后续实时成功覆盖旧 target_data_time 后 RetryItem 变为 superseded 的测试。
- [ ] 实现 governed Consumer 和恢复扫描。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/marketfetch ./internal/bootstrap ./internal/rpc`。
- [ ] 提交：`feat(collector): close async scf completion and retry loop`。

### Task 8：改造 Timer 并增加防重入

**文件：**

- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/rpc/schedule.go`
- Create: `modules/collector/internal/scfinvoker/client.go`、`client_test.go`
- Create: `modules/collector/internal/marketfetch/dispatcher.go`、`dispatcher_test.go`
- Test: `internal/bootstrap/bootstrap_test.go`、`internal/rpc/schedule_test.go`

- [ ] 写两次并发触发只执行一次扫描的测试。
- [ ] 写每 10 秒扫描但同一 schedule 只创建一次批次的测试。
- [ ] 写 100 个批次提交时 CloudNode 并发不超过 20，Timer 不等待 SCF 完成的测试。
- [ ] 写 Dispatcher 固定使用 Event 调用、保存腾讯云 request_id，并用 CAS 防止 terminal 状态回退的测试。
- [ ] 写 Fetcher 节点只选择 `biz_type=market_fetcher` 且部署状态 Active 的测试。
- [ ] 写 Gap Audit 只在 10 分钟边界运行、Catchup 每分钟最多 5 个的测试。
- [ ] 使用 `timerjob.New("collector_schedule", 25*time.Second, ...)` 包装 Handler。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/bootstrap ./internal/rpc`。
- [ ] 提交：`feat(collector): guard short-lived market scheduler`。

### Task 9：删除旧健康语义并建立精简指标和告警

**文件：**

- Modify: `modules/cloudnode/internal/rpc/heartbeat_maintainer.go`、`internal/store/node.go`
- Modify: `modules/cloudnode/internal/observability/scf_metrics.go`
- Create: `modules/collector/internal/marketfetch/metrics.go`、`metrics_test.go`
- Modify: `modules/monitor/internal/observability/overview.go`
- Modify: `modules/monitor/internal/bootstrap/business_freshness.go`、`default_alerts.go`
- Test: 对应 `*_test.go`

- [ ] 写 `biz_type=market_fetcher` 节点不会收到 Keepalive 的测试。
- [ ] 写该节点不因 90 秒无心跳被标为 timeout 的测试。
- [ ] 写 Monitor 不再生成 `scf:heartbeat`，而生成 deployment 和 batch freshness 检查的测试。
- [ ] 写中文批次超时、重试积压和恢复通知测试。
- [ ] 写仅注册五个 `moox_collector_market_fetch_` 指标、标签集合和每类 outcome 枚举严格匹配 14.2、非法 outcome 被拒绝的测试。
- [ ] 删除旧 keepalive 指标和默认告警；删除独立 retries、completion timeout、429、5xx、request duration 和 storage commit duration 指标。
- [ ] 将请求耗时、Storage 提交耗时、原始错误和 `Retry-After` 写入结构化日志，保留 deployment、batch、retry pending 和 last success 指标。
- [ ] 运行 `cd modules/cloudnode && go test -race -count=1 ./internal/...`。
- [ ] 运行 `cd modules/collector && go test -race -count=1 ./internal/marketfetch`。
- [ ] 运行 `cd modules/monitor && go test -race -count=1 ./internal/...`。
- [ ] 提交：`refactor(monitor): observe on-demand scf by batch freshness`。

### Task 10：本地 E2E、全新初始化工具和真实发布

**文件：**

- Create: `modules/collector/test/short_lived_market_fetch_e2e_test.go`
- Modify: `modules/cli/internal/command/collector.go` 及测试
- Modify: `docs/云节点管理.md`、`docs/采集任务管理.md`

- [ ] 使用嵌入式 JetStream、假 Binance、真实 Collector SQLite 和假 Storage Primary 验证 planned→dispatched→completed→ACK。
- [ ] 验证 429 进入 RetryItem，重试成功后写入 Storage。
- [ ] 验证 SCF 无完成事件时 20 秒后自动回收。
- [ ] 验证 Storage 重复 Upsert 不产生重复 RowKey。
- [ ] 给 moox-cli 增加 `collector function probe-egress --file ./custom.toml`：最多并发 5、逐节点同步 RequestResponse、打印完整结果、任一节点失败时最终返回非零退出码。
- [ ] 写出口 IP 服务失败、Binance 探测失败、部分节点失败仍继续和 `distinct_outbound_ips` 汇总测试。
- [ ] 增加全新初始化检查：旧 Collector 函数已删除、Fetcher metadata 无 `execution_mode`、新 Completion durable 可用；不实现旧 JobItem drain 或旧链路恢复命令。
- [ ] 运行 `make proto` 后执行 `./scripts/test-go-workspace.sh`，两者不得并行。
- [ ] 运行 `make verify-pr` 和 `make verify-custom-setup`。
- [ ] 请求独立 `codeCR` 审查，修复所有 P0-P2 后重新运行验证。
- [ ] 提交：`test(collector): prove short-lived market fetch end to end`。

## 18. 真实环境验收

### 18.1 启用调度前

先完成配置校验、构建和函数发布；发布任务显示全部节点 Active 后，再执行出口探针：

```bash
./bin/moox-cli setup validate --file ./custom.toml
make build
make test-collector-scf-package-contract
# 执行 Task 2 定义的 publish submit/status，等待全部函数 Active
./bin/moox-cli collector function probe-egress --file ./custom.toml
```

检查：

- `custom.toml` 权限仍为 0600；
- EventBus TLS 地址可从 SCF 访问；
- Storage Primary tRPC 地址可从 SCF 访问；
- CloudNode 中 5 个初始函数均为 Active；
- 函数配置为 64MB、10 秒、异步自动重试 0；
- 环境变量与 CloudNode metadata 一致；
- 出口探针逐节点成功，Binance 轻量接口状态为 200，汇总中至少有一个有效公网 IP。

### 18.2 分阶段真实数据验证

#### 阶段 A：10 个 Symbol、广州 1 个函数

- 连续运行 30 分钟；
- Storage Primary 和 View 均能看到真实 K 线；
- 每分钟批次全部有 Completion；
- 无 Keepalive 调用；
- 无 SCF timeout；
- 保存出口探针结果；从腾讯云 CLS 或控制台记录 Coldstart，不从探针结果推断冷启动。

#### 阶段 B：100 个 Symbol、2 个地域

- 连续运行 2 小时；
- 验证 region round-robin；
- 验证 429、5xx 和网络超时能进入 RetryItem 并恢复；
- 验证 Gap Audit 不影响实时批次。

#### 阶段 C：1000 个 Symbol、5 个地域

- 连续运行至少 24 小时；
- 检查每个启用 Dataset + Frequency 的最新数据时间；
- 检查每分钟约 100 个 batch 的完成率；
- 检查 SQLite 批次清理和 RetryItem 数量；
- 检查 Monitor 中文告警和恢复通知；
- 与此前常驻 SCF 的账单基线比较资源使用趋势。

### 18.3 成本记录

每个阶段记录：

```text
invocation_count
configured_memory_mb
bill_duration_ms_sum
resource_usage_gbs
external_egress_gb
cls_ingestion_gb
coldstart_count
p50_duration_ms
p95_duration_ms
p99_duration_ms
http_429_count
resource_limit_count
```

估算公式：

```text
resource_usage_gbs =
  sum(configured_memory_mb / 1024 * bill_duration_ms / 1000)
```

不能只比较“函数个数”。函数未调用时不会产生执行 GBs；真正需要比较的是总调用数、每次计费时长、内存和外网/CLS 附加费用。

## 19. 完成标准

以下条件全部满足才算完成：

1. Collector 是唯一 Scheduler，Monitor 不生成任务。
2. BatchInvocation 在异步调用前持久化，SCF 超时或事件丢失能够回收。
3. EventBus Registry、Proto、Stream 和 durable consumer 契约完整。
4. SCF 只写 Storage Primary，不直接发布 Storage 已提交事实。
5. Storage RowKey 和 `source_event_id` 保证重复执行不产生重复 K 线。
6. 实时路径只抓最近 3 根，不读取水位、不循环追赶 5000 根。
7. 长历史缺口通过受限 CatchupBatch 分段恢复。
8. 函数配置固定为 64MB、10 秒，正常实时批次 P99 小于 8 秒。
9. 函数内并发、batch size 和 timeout 环境变量与 CloudNode DB 一致。
10. 429、网络超时和 5xx 能通过完成事件落为 RetryItem，并在最多 3 次内终结。
11. Stable TaskInstance 不按分钟膨胀；成功批次 48 小时后自动清理。
12. `manual` Symbol 规则和 `dataset` 行情规则均能正确工作。
13. 短时 Fetcher 不运行 Keepalive，也不会触发 `scf:heartbeat` 告警。
14. 初始 5 个地域函数通过真实出口 IP 和 API 连通性探针。
15. 1000 个标的连续运行 24 小时，完成率、Dataset freshness 和 RetryItem 积压符合预期。
16. SCF 总运行时长和 GBs 资源使用量显著低于旧常驻模型。
17. 本地 race/E2E、`make verify-pr`、独立 codeCR、真实 SCF 和真实 Storage 数据验证全部通过。

## 20. 最终设计结论

本方案保持简单的单控制面模型：

- Collector 规划、持久化、回收和重试；
- CloudNode 管理函数和发起调用；
- SCF 只做 10 秒内的行情请求、Storage 提交和完成上报；
- Storage 是数据真值；
- EventBus 传递受治理的完成事实；
- Monitor 观察批次和 Dataset，而不是用心跳假设按需函数必须常驻。

该边界能够满足个人量化场景需要，同时避免静默丢批、双重写入、重试风暴、任务表爆炸和 Keepalive 误告警。
