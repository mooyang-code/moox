# SCF 定时触发采集改造执行计划

状态：经独立评审（codex，`gpt-6-astra`，三轮）结论“同意执行计划”（2026-10-11），尚未编码。依据 [SCF 定时触发采集改造设计](SCF定时触发采集改造设计.md) v3（下称“设计”），本文把设计拆成可逐步实施、逐步验证的阶段。设计中已经说明的理由不再重复，只写“做什么、改哪里、怎么验收、出问题怎么退”。

## 0. 约定

- **分支**：在 `feature/mooyang` 上按阶段提交，每个阶段至少一个独立提交，提交信息写明阶段号。
- **兼容**：项目未上线，不写长期兼容层；但**生产切换以阶段为单位**，阶段 1～5 期间旧的 Invoke 链路继续运行，阶段 6 切换，七天回退窗口结束后才删除旧代码。所以阶段 1～6 期间新代码必须与旧链路共存，下面的“临时共存措施”在阶段 6b 全部删除。
- **临时共存措施**（阶段 3 起生效，阶段 6b 删除）：
  1. **任务归属**：任务参数 `c_collect_params` 增加 `fetch_mode`（`invoke`，默认；或 `timer`）。旧调度只处理 `invoke` 任务（`scheduler.go:412` 读取启用任务处加过滤），新对账只处理 `timer` 任务；**例外**是正在交接的任务（见第 3 条），两边按周期边界各管一段。
  2. **节点归属**：新链路创建的实时函数与补采函数带 `fetch_mode=timer` 元数据和独立命名前缀；旧 Invoke 节点池（`scfinvoker/client.go:87` 选节点处）排除它们，新对账也只管理带该标记的函数。
  3. **交接边界**（都是 K 线开始时间，作用于旧调度的规划与重试）：
     - `invoke_until_period = T`：旧调度只处理目标周期 `< T`；新对账同时处理带此参数的 `invoke` 任务，只为它生成 `from = T` 的计划。
     - `invoke_from_period = T'`（回退用）：旧调度只处理目标周期 `≥ T'`。
- **构建与测试**：Go 构建前去掉 `HTTP(S)_PROXY`，使用固定的 Go 1.26.9 工具链；每个阶段跑涉及模块的 `go test ./...`。改动 `modules/*/schema/*.sql` 时按 AGENTS.md 的格式规范，逐个载入空 SQLite 并执行 `git diff --check`。
- **部署**：原生布局暂不支持就地更新单元配置，沿用“备份到 `state-imports/` → 替换二进制或配置 → `restart.sh -components <组件>`”的做法，每次替换记录 sha256。重启前确认没有持有 `/data/moox/run/maintenance.lock`。
- **文档**：每个阶段完成时同步更新对应模块文档（采集、存储、外部接入、云节点、命令行工具、事件总线、部署与运维）。

## 1. 阶段总览

| 阶段 | 内容 | 依赖 | 可否与旧链路共存 |
| --- | --- | --- | --- |
| 1 | Storage：快照、生效表、提交时建周期、状态查询、缺失列表 | 无 | 是（旧链路仍走 Ensure） |
| 2 | 函数：读计划环境、按周期选份、不连 EventBus、内部截止时间、补采动作 | 1 | 是（新函数独立部署） |
| 3 | Collector：每空间对账、采集计划、函数部署（VPC、cron、内存）、检查与补采；Admin 活动日志 | 1、2 | 是（新对账只管新函数组） |
| 4 | 前端与监控 | 3 | 是 |
| 5 | crypto canary（隔离数据集） | 1～4 | 是 |
| 6a | crypto 切换（按目标周期交接） | 5 | 旧链路停用但保留，七天内可回退 |
| 6b | 回退窗口结束后清理旧代码、接口、权限、事件流 | 6a + 7 天 | 删除旧链路 |
| 7 | stockcn | 6 | — |
| 8 | 去 TLS 与防火墙白名单 | 6b、7（所有函数已不连 EventBus） | — |

阶段 1、2 可以并行开发；阶段 3 依赖 1 的接口与 2 的环境格式。

## 2. 阶段 1：Storage

### 2.1 协议（`modules/storage/proto`）

PrimaryStore 新增（字段为示意，实施时与现有命名对齐）：

```protobuf
rpc RegisterSeriesSnapshot(PrimaryRegisterSeriesSnapshotReq) returns (PrimaryRegisterSeriesSnapshotRsp);

message PrimaryRegisterSeriesSnapshotReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string dataset_id = 3;
  string frequency = 4;
  repeated DatasetPeriodSeries series = 5;   // 完整标的列表，按规范排序
  int64 effective_from = 6;                  // 生效周期 P（K 线开始时间，Unix 秒）
}
message PrimaryRegisterSeriesSnapshotRsp {
  common.RetInfo ret_info = 1;
  string series_hash = 2;                    // Storage 自己计算
  uint32 expected_count = 3;
  bool already_registered = 4;               // 同 P 同 hash 的重放
}
```

- DataNode 侧增加对应的内部 RPC（`data_node.proto`），由 Primary 按 `space_id + dataset_id` 路由，与现有周期 RPC 一致（`primarystore/period.go`）。
- `GetDatasetPeriodStatus`：**保持旧请求形态可用**（旧客户端发送嵌套 `expectation`，并在 Ensure 结果不明时依赖它恢复，见 `marketstorage/storage.go:142`），只放宽为“也可以只带周期键”；响应增加 `found`、`series_hash`、`expected_count`、`successful_count`、`missing_series_indexes`（`include_progress=true` 时返回）。阶段 6b 再删除嵌套 `expectation` 形态。
- **允许迟到时间**：不新增接口，作为数据集属性 `allowed_lateness_seconds` 保存在元数据中，用现有数据集更新接口设置。Primary 处理 `CommitTimeSeriesBatch` 时从元数据缓存读取该属性，写入 DataNode 提交请求的新字段 `allowed_lateness_seconds`；属性缺失时按 60 秒处理并告警。
- `CommitTimeSeriesBatch` 请求结构不变：函数在 `expectation` 中只带 `series_hash`、`expected_count`、周期键，不带快照、不带截止时间（Storage 忽略请求中的截止时间）。

### 2.2 存储布局（Pebble，示意）

| 键 | 值 | 说明 |
| --- | --- | --- |
| 快照：`(space, dataset, freq, hash)` | 排序后的完整标的列表 | 只追加；v1 不清理 |
| 生效表：`(space, dataset, freq, P)` | hash | 只追加；按 P 有序，便于“取 ≤ 周期时间的最后一行” |
| 周期记录 | `series_hash`、`expected_count`、`deadline`、`status`、位图、waiting 与 deadline 索引 | **不再保存 `series_snapshot` 副本** |

键前缀沿用 `period_progress.go` 现有约定，实施时在同文件或新文件 `period_snapshot.go` 中集中定义。

### 2.3 逻辑

1. **共享 hash 计算**：规范序列键沿用现有定义 `CanonicalSeriesKey(provider, sourceID, marketType, subjectID, seriesTag)`（`collector/internal/domain/task_series.go:30`），排序后生成稠密序号与 `SeriesSetHash`。把这两个函数及排序规则抽到共享包（建议 `packages/seriesset`），Storage 与 Collector 共用。`RegisterSeriesSnapshot` 的快照条目因此必须带齐 provider、sourceID、marketType、subjectID、seriesTag（扩展 `DatasetPeriodSeries` 或单独定义条目消息），Storage 用同一函数重算 hash。补充“相同列表在两边得到相同 hash”的测试。
2. **`RegisterSeriesSnapshot`**（设计 5.1）：与 `CommitTimeSeriesBatch` 共用 DataNode 周期锁；锁内校验数据集存在、列表规范、规模上限；Storage 重算 hash；`(space, dataset, freq, P)` 唯一——同 P 同 hash 直接返回成功（即使 P 已过），同 P 不同 hash 拒绝；新增 P 必须严格晚于最后一行、对齐频率边界、晚于 Storage 当前时间，且 P 之后尚未建立周期；快照与生效记录同一批次写入。
3. **允许迟到时间**：Primary 从数据集属性读取并随提交请求带给 DataNode（必须为正、设上限，例如 ≤ 1 天；缺失按 60 秒）。属性修改只影响此后新建的周期。
4. **`CommitTimeSeriesBatch` 提交时建周期**（设计 5.2）：
   - 锁内先判断周期是否存在（不能沿用先读期望再判断的顺序，`period_progress.go:244`）；
   - 不存在时：按周期时间查生效表得到应生效 hash，与请求不一致则拒绝（错误码区分“hash 与生效表不符”“快照未登记”“已过截止时间”）；按 hash 加载快照；截止时间 = `frequency.BarEnd(P)` + 请求带来的允许迟到时间；`now >= deadline` 拒绝且不建周期；
   - 用快照构造期望并完成逐行身份校验（`period_progress.go:927`）；
   - 在首次行写入的同一批次保存周期记录、位图、waiting 与 deadline 索引、行与行变更事件；
   - 不在锁内调用 `EnsureDatasetPeriod`。
5. **缺失列表**：`periodMarkerSubjects`（`period_progress.go:1096`）改为“快照全集减成功位图”；`CollectorPeriodCompleted` 的缺失标的按 hash 查回快照生成。
6. **写入幂等**：`CommitTimeSeriesBatch` 继续按 `source_event_id` 去重（`store.go:260`），本阶段不改 Storage 去重逻辑，写入 ID 规则在函数侧实现（阶段 2）。
7. 旧接口 `EnsureDatasetPeriod`、`RecordDatasetPeriodFailures` 本阶段保留（旧链路在用），阶段 6b 删除。
8. **两种周期存储形式并存**：旧周期在记录中内嵌 `series_snapshot`（`period_progress.go:980`），新周期只存 hash 引用。读取周期期望、逐行校验、截止裁决、生成完成事件时都要同时支持两种形式（有内嵌快照就用内嵌，否则按 hash 查快照）。历史状态查询与缺失序列查询也依赖快照（`period_progress.go:841`），所以阶段 6b 删除旧读取分支前，先把仍保留的旧周期内嵌快照按原 hash **一次性迁入共享快照存储**，验证历史查询结果不变后再删除。

### 2.4 权限（`packages/servicecatalog/catalog.yaml`）

- `collector`：增加 `RegisterSeriesSnapshot`。
- `scf-collector`（经 Access）：保留 `CommitTimeSeriesBatch`；增加 `UpsertFields`（补采写行）。`EnsureDatasetPeriod`、`RecordDatasetPeriodFailures`、`ClaimTimerBatch` 在阶段 6b 移除。
- 更新路由快照测试（`packages/servicecatalog/routes_test.go`）。

### 2.5 测试

- 单元：登记幂等（同 P 同 hash 重放、P 已过的重放）、同 P 不同 hash 拒绝、P 插入中间拒绝、P 之后已有周期拒绝、P 未对齐拒绝。
- 单元：提交建周期——并发 N 个提交只建一次；hash 与生效表不符拒绝；快照未登记拒绝；过截止时间拒绝且无周期记录；跨生效边界（P−1 用旧 hash、P 用新 hash）；月线等 `BarEnd` 计算。
- 单元：截止 worker 能找到懒建立的周期并按时裁决；缺失列表包含“从未提交”的标的；完成事件正确。
- 现有 `period_receipts_test.go`、`period_progress` 相关测试全部通过（旧 Ensure 路径不受影响）。
- **共存测试**：用旧客户端的请求形态（嵌套 `expectation` 的状态查询、Ensure + Commit）、旧格式的已持久化周期数据，对新服务运行，结果与旧服务一致；不能只跑重新编译后的单测。
- 重启：建周期后重启 DataNode，周期、位图、索引完整。

### 2.6 验收与部署

- 部署 Storage（storage 主机）与 Admin/主机网关的服务目录更新（新方法授权）。
- 线上旧链路运行正常（Monitor K 线新鲜度不受影响）。
- 用 CLI 或临时工具对 canary 数据集登记快照、提交一条测试批次，验证周期懒建立与完成事件。

**回退**：替换回旧 Storage 二进制。注意新周期会进入旧 worker 也会扫描的周期索引，旧代码读不到其中的快照引用；所以阶段 1 只在 canary 前用测试数据集验证懒建周期，**回退前须确认生产数据集没有新形式的周期**，并演练“已有新形式周期时回退”的处理（终结或删除这些测试周期）。

## 3. 阶段 2：SCF 函数

### 3.1 计划环境变量 `MOOX_MARKET_FETCH_PLAN`

gzip 后 base64 的 JSON（示意）：

```json
{
  "v": 1,
  "market": {"provider": "binance", "market_type": "spot", "instrument_type": "spot", "source_id": "spot_http"},
  "fetch": {"concurrency": 10, "rps": 50, "burst": 50, "request_timeout_ms": 3000, "max_attempts": 4},
  "symbol_exceptions": {},
  "datasets": [
    {
      "dataset_id": "dataset_xxx",
      "frequency": "1m",
      "lateness_s": 15,
      "copies": [
        {"from": 1791700800, "hash": "H1", "count": 506, "subjects": [["BTC-USDT", 12], ["ETH-USDT", 57]]},
        {"from": 1792050720, "hash": "H2", "count": 507, "subjects": [["BTC-USDT", 12], ["ETH-USDT", 58]]}
      ]
    }
  ]
}
```

- `v` 只用于拒绝无法识别的格式，不做多版本兼容。
- `symbol_exceptions` 只放无法由“标的 ID 去连字符”推导出交易所 symbol 的标的，通常为空。
- 选份：对每个数据集取 `from ≤ 目标周期` 中最晚的一份，没有则跳过（设计 4.1）。
- 环境预算：复用 `packages/cloudprovider/tencent/scf_environment.go` 的大小校验，按变更期间两份的最终值计算。

### 3.2 改动

| 位置 | 改动 |
| --- | --- |
| `collector/internal/serverless/market_data/handler.go` | Timer 路径：严格解析事件 `Time`（缺失或非法直接拒绝，去掉 `timerNow()` 回退当前时间）；按计划执行；新增 `kline_backfill` 动作（补采，同步调用） |
| `collector/internal/marketfetch/environment.go`（或新文件） | 解析 `MOOX_MARKET_FETCH_PLAN`；计算到期频率与目标周期；选份；按“标的 + 频率”合并去重 |
| `collector/internal/marketfetch/kline_pipeline.go` | 只拉目标 bar；按数据集拆分提交，带所选份的 hash、总数、序号；不调用 `EnsureDatasetPeriod`；写入 ID = (周期、函数、数据集、提交内容摘要)，同一 RPC 重试复用、内容变化换 ID；保留 `AcceptedSeriesIndexes` 检查；内部截止 = 收盘 + 迟到 − 3 秒，到点停止 HTTP 重试并提交已得结果 |
| `collector/internal/marketfetch/handler.go` | 新的 Timer 路径不发布完成事件、不调用 `RecordDatasetPeriodFailures`、不领取批次；旧 Invoke 路径在阶段 6b 前保留 |
| `collector/internal/marketfetch/contracts.go`（`:175`、`:205` 等）及关联校验 | 请求模型拆分：**抓取身份**（市场、标的、频率）与**各目标的周期身份**（数据集、hash、序号、总数）分开；去掉“各目标 hash、序号必须与共享 item 相同”的约束（`:205`），允许同一标的写入多个各自编号的数据集；非 stockcn 实时 item 上限 40（`:175`）改为按数据源配置，默认允许 50 |
| `collector/internal/marketfetch/metrics_runtime.go` | 去掉函数指标的 EventBus reporter |
| `collector/internal/sources/binance/marketdata_adapter.go`、`marketdata/feed_guard.go` | 限流参数（rps、burst、并发、超时）从计划读取；流水线并发 `Request.Concurrency` 同步可配；遇到 429/418 按 `Retry-After` 退避 |
| 错峰 | 函数内按函数名哈希做 0～1.5 秒确定性延迟（crypto）；stockcn 沿用现有 settle delay 与窗口 |
| `packages/cloudprovider/tencent/scf_environment.go` | 去掉 EventBus 必需项与 CA 文件要求；增加计划变量校验 |
| `scripts/build/build-collector-scf-package.sh`、`cli/internal/collectorpackager` | 不再要求、不再打包 EventBus CA |

补采动作 `kline_backfill`：请求带 `[(标的, 频率, from, to, 目标数据集列表)]` 与写入 ID；按 1～1000 根拉取，经 `UpsertFields` 写行（`write_source=backfill`）；返回每项写入行数。不涉及周期状态。

### 3.3 测试

- 单元：选份边界（周期 = P、无份可选、两份）；1m 与 1h 同时到期；合并去重；写入 ID 规则；内部截止停止重试；非法 Timer 时间拒绝。
- 端到端：一个函数 50 个标的；同一标的写入两个 hash、序号不同的数据集。
- 单元：环境校验不再要求也不读取 EventBus 键；CloudNode 下发时把 EventBus 键从受管环境中移除（无兼容要求，新函数环境中不应出现）。
- 本地端到端（`period_storage_rpc_e2e_test.go` 风格）：函数 → 测试 Storage，懒建周期、完成事件、跨生效边界切换。

### 3.4 验收与部署

- 构建新代码包；**不更新线上现有 80 个函数**（它们仍是旧 Invoke 函数），新包只用于阶段 3 创建的新函数组。

**回退**：新包不部署到旧函数，无需回退。

## 4. 阶段 3：Collector

### 4.1 数据表（`modules/collector/schema/collector.sql`）

```sql
-- SCF 定时采集的采集计划：每个空间同一时间最多一份未生效的计划，最近一份已生效的计划即“已生效状态”
CREATE TABLE IF NOT EXISTS t_collector_fetch_plans (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_plan TEXT NOT NULL,
    c_progress TEXT NOT NULL DEFAULT '{}',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'applying', 'applied'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_t_collector_fetch_plans_open
ON t_collector_fetch_plans (c_space_id)
WHERE c_status <> 'applied';
CREATE INDEX IF NOT EXISTS idx_t_collector_fetch_plans_space ON t_collector_fetch_plans (c_space_id, c_id);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_fetch_plans_mtime
AFTER UPDATE ON t_collector_fetch_plans
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_fetch_plans SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- K 线补采作业与分块
CREATE TABLE IF NOT EXISTS t_collector_backfill_jobs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_source TEXT NOT NULL DEFAULT 'manual',
    c_request TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_progress TEXT NOT NULL DEFAULT '{}',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_source IN ('manual', 'auto')),
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_backfill_jobs_space ON t_collector_backfill_jobs (c_space_id, c_status);

CREATE TABLE IF NOT EXISTS t_collector_backfill_chunks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_write_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_rows INTEGER NOT NULL DEFAULT 0,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id, c_dataset_id, c_frequency, c_subject_id, c_from)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_backfill_chunks_job ON t_collector_backfill_chunks (c_job_id, c_status);

-- 自动补采扫描水位
CREATE TABLE IF NOT EXISTS t_collector_backfill_watermarks (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_scanned_until DATETIME NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency)
);
```

状态：`pending`（已保存，尚未登记快照）→ `applying`（已登记，正在下发与收尾）→ `applied`（全部完成，已生效）。`c_plan` 保存本次变更的完整内容（各数据集的 P、hash、完整标的列表、相对上一份计划新增与移除的标的、每个函数的完整目标环境、退役函数）；`c_progress` 保存登记、各函数下发、收尾、活动日志写入各步的确认状态。已生效的计划保留，用于检查时还原“某个时间段应有哪些标的”。旧表在阶段 6b 删除。

### 4.2 配置

- 任务参数 `c_collect_params` 增加 `symbols_per_function`（默认 50）。
- Collector `app.yaml` 增加按地域的 SCF 网络配置，由 `moox-cli setup` 从放置清单生成：

```yaml
scf_regions:
  ap-nanjing:
    access_address: 10.206.0.5:11004     # Access 私网地址
    access_id: access@storage
    vpc_id: vpc-qdi3ffwb
    subnet_id: subnet-gihzv7ty
  ap-hongkong:
    access_address: 172.19.32.13:11004
    access_id: access@compute-1
    vpc_id: vpc-qm346eik
    subnet_id: subnet-99ym7odh
scf_markets:                              # 每个空间、市场使用的地域白名单
  crypto:
    spot: [ap-nanjing]
    swap: [ap-hongkong]
  stockcn:
    a_share: [ap-nanjing]
```

  实施时：`scf_regions` 只放各地域的 VPC/子网；Access 私网入口放 `scf_plan.access_routes`（旧链路的 `scf_access_routes` 在 6b 之前继续服务旧 Invoke 函数）；其余部署参数在 `scf_plan`（云账号、代码包、命名空间、内存、基础环境文件、每函数标的数、拉取参数）。6b 时删除 `scf_access_routes`、`scf_region_blacklists`。`moox.toml` 的 `region_blacklist` 改为 `regions`，`timer_function_count` 删除（函数数由标的数与每函数标的数算出）。
- setup 生成配置时用现有 `privatenet` 主机查询拿到 VPC 与子网；地域没有同地域 Access 主机时报错。

### 4.3 计划计算（替换 `assignment.go` 中 crypto 与 stockcn 的分配）

输入：本空间启用的采集任务、各标签当前成员（Storage `ResolveSubjects`，每空间一次）、`scf_markets`、`symbols_per_function`。

1. 按任务得出每个数据集（含频率）的完整标的列表，用共享包计算 hash 与序号。
2. 按“市场 + 标的 + 频率”合并，按市场分组；每组标的按规范顺序切成每份 ≤ `symbols_per_function` 的分片；分片映射到函数名（`moox-fetch-plan-<space>-<market>-<region>-<NNN>`；名称含 `-plan-`，旧节点池据此排除；补采函数 `moox-fetch-plan-backfill-<space>-<market>-<region>`），数量变化时增删函数。
3. 生成每个函数的计划环境（只含本函数的标的及其在各数据集中的序号）与 cron（服务频率集合的并集；只有 1h 时用整点 cron）。
4. 容量校验（设计 8.1）：计划环境大小、函数数量配额、`symbols_per_function` 上限；不通过则本轮报错、不下发。

### 4.4 每空间对账协程（替换 `reconciler.go` 的 Timer 对账）

- 空间管理器每分钟检查空间列表，为每个启用的空间维持一个协程；空间停用或删除时先停用其全部函数定时器，确认后退出协程（设计 6.1）。
- 每分钟第 10 秒执行一轮，沿用 `timerjob` 防重叠；流程与约束完全按设计 6.1（先续跑未生效的计划、每小时全量读回放在提前返回之前、只有“无未生效计划且期望一致”才提前结束、先保存计划再登记、旧份保留到 P + 迟到、退役函数在收尾时停用）。
- **P 的重放与重选**：登记结果不明（超时、断连）时重放原请求；只有 Storage **明确拒绝**“P 已不在未来且该 P 尚未登记”时，才为该数据集重选 P 并更新采集计划（例如保存计划后 Collector 宕机到 P 之后才恢复）。已登记成功的 P 永不重选。
- 下发通过 CloudNode：新函数用 `SubmitCreateNodes`（VPC、公网访问、内存、异步配置）；环境与定时器用 `SubmitUpdateNodeRuntimeConfigs`；停用定时器同样走运行配置。等待批次结果（`GetNodeBatchChange`），按函数记录确认状态。
- **授权与发布租约**：服务目录为 `collector` 增加实际使用的 CloudNode 方法（至少 `SubmitCreateNodes`；当前只有 `GetNodeList`、`InvokeFunction`、`GetNodeBatchChange`、`SubmitUpdateNodeRuntimeConfigs`，见 `catalog.yaml:725`）。行情函数创建强制要求采集发布租约与 fencing token（`cloudnode/internal/rpc/node_batch.go:328`），对账复用现有 Timer 对账已有的租约获取、续期、释放与请求字段传递，并用真实网关授权测试覆盖。
- 全局并发：一个进程级信号量（默认 20）限制同时进行的 SCF 配置操作，各空间共用。
- Collector 重启后第一轮全量读回云端函数与定时器（CloudNode 节点列表 + 运行配置），与最近一份已生效的计划、未生效的计划比对。

### 4.5 CloudNode（`modules/cloudnode`）

| 位置 | 改动 |
| --- | --- |
| `internal/rpc/node.go:681` | 去掉行情 Timer 函数内存固定 64MB，使用请求中的内存（默认 64） |
| `internal/providers/tencentscf/client.go` | 创建函数时带 `VpcConfig`、`PublicNetConfig`（开启公网，不用 EIP）；创建后调用 `UpdateFunctionEventInvokeConfig` 把异步重试设为 0、消息保留期设为最小值 |
| `internal/rpc/runtime_config.go` | 受管环境键加入 `MOOX_MARKET_FETCH_PLAN`；去掉 EventBus 相关键；定时器 cron 与启停沿用现有逻辑 |
| 部署前检查 | 创建前确认“VPC + 公网访问”生效（读回函数配置）；不满足时报告错误，不自动创建 NAT |

### 4.6 Storage 客户端与快照登记

- `collector/internal/marketstorage`、`storageio` 增加 `RegisterSeriesSnapshot`、`GetDatasetPeriodStatus`（新形态）。
- 允许迟到时间：Collector 在创建或更新任务的结果数据集时设置数据集属性 `allowed_lateness_seconds`（crypto 15 秒，stockcn 90 秒，任务参数可改）。

### 4.7 检查与补采

**Collector RPC**（`CollectMgr`）：`CheckKline`、`SubmitKlineBackfill`、`GetKlineBackfill`、`CancelKlineBackfill`。服务目录授权 `moox-cli` 与 `console`。

**CLI**（`modules/cli/internal/command`）：

```text
moox-cli collector kline check    --space-id crypto [--dataset-id ...] --from ... --to ... [--subject ...] [--deep]
moox-cli collector kline backfill --space-id crypto [--dataset-id ...] --from ... --to ... [--subject ...] [--force]
moox-cli collector kline backfill-status --job-id ...
moox-cli collector kline backfill-cancel --job-id ...
```

**检查**：由已生效的采集计划，以及**已确认登记且 P 已生效的未生效计划**，还原每段时间应有的（数据集、标的），按频率（crypto 连续，stockcn 用交易日历）生成应有时间点；`ReadTimeSeriesRows` 读行比对；连续缺口合并输出；`--deep` 校验 OHLCV 字段齐全。

**补采执行**：
- 每个（地域、市场）一个专用补采函数（与实时函数同一代码包、无定时器），**函数级预留并发 5**，由平台硬性限制实际执行实例数；Collector 以同步调用执行 `kline_backfill`。并发 5 的范围是“每个补采函数”。
- 本地在途计数作为第二道限制：每次调用持久化开始时间与最迟结束时间（开始 + 函数超时 + 余量）；同步调用超时、断连或 Collector 重启时结果不明，名额保留到最迟结束时间后才释放。
- 作业拆成（标的、连续时间段）分块，每块 ≤ 1000 根。**分块 ID 稳定**，实际写入 ID = 分块 ID + 本次写入内容摘要：同一内容重试复用写入 ID，部分写入后重新抓取得到更多行时使用新写入 ID（Storage 对相同来源 ID 整组跳过，见 `store.go:260`）。完成后复查该块缺口；支持取消与重启续跑。
- 自动补采：同一个对账协程每小时一次，从水位起扫描已过截止时间的周期（保留 10 分钟重叠），每轮行数与请求数有上限；Collector 停机恢复后从水位追赶。

### 4.8 Admin 活动日志模块（全局）

全局、永久保存的展示日志，任何模块都可写入；第一个使用者是 Collector 的标的变更。

```sql
-- 活动日志：各模块写入的、需要展示给用户的事件；永久保存，不清理
CREATE TABLE IF NOT EXISTS t_activity_logs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_source TEXT NOT NULL,
    c_category TEXT NOT NULL,
    c_level TEXT NOT NULL DEFAULT 'info',
    c_title TEXT NOT NULL,
    c_detail TEXT NOT NULL DEFAULT '{}',
    c_ref_id TEXT NOT NULL DEFAULT '',
    c_dedup_key TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_level IN ('info', 'warning', 'error'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_t_activity_logs_dedup
ON t_activity_logs (c_dedup_key)
WHERE c_dedup_key <> '';
CREATE INDEX IF NOT EXISTS idx_t_activity_logs_space_time ON t_activity_logs (c_space_id, c_ctime);
CREATE INDEX IF NOT EXISTS idx_t_activity_logs_category_time ON t_activity_logs (c_category, c_ctime);
```

- 表放在 `modules/admin/schema/admin.sql`；`c_space_id` 为空表示全局事件。
- 接口（Admin 新服务，名称实施时与现有服务命名对齐）：
  - `AppendActivityLogs`：批量追加；`c_dedup_key` 非空时按它去重（重放返回成功、不重复写）。授权给内部组件（先给 `collector`）。
  - `ListActivityLogs`：按空间、来源、类别、关联对象 ID、时间范围过滤，按时间倒序分页。授权给 `console`、`moox-cli`；console 按用户的空间成员关系过滤。
- Collector 写入：
  - 新建采集计划且标的有变化时，类别 `collector.subject_change`，标题如“现货 1m：新增 3 个、移除 1 个标的，将于 2026-10-15 08:32 生效”，详情为各数据集的新增与移除标的、旧 hash、新 hash、生效周期，关联对象为计划 ID，幂等键 `collector.fetch_plan.<计划ID>.created`；
  - 计划生效时，类别 `collector.fetch_plan_applied`，幂等键 `collector.fetch_plan.<计划ID>.applied`；
  - 写入状态记在采集计划的 `c_progress` 中，失败时下一分钟随对账重试；日志写入失败不阻塞计划推进，但计划标记为已生效前须确认两条日志都已写入。
- CLI：`moox-cli activity list [--space-id] [--category] [--since]`。

### 4.9 测试

- 计划计算：合并去重、分片、序号、hash 与 Storage 一致、cron 生成、容量校验拒绝。
- 对账：无变化提前结束；变化时“先落记录 → 登记 → 下发 → 收尾”；下发部分失败后下一分钟只重试失败项；Collector 在各步骤崩溃后重启续跑且不重选 P；共享函数不被旧记录覆盖；空间停用流程；退役函数在收尾时停用。
- 补采：分块、写入 ID（部分写入后再补）、响应丢失、取消、重启续跑、并发上限、复查。
- 恢复与检查：落记录后宕机到 P 之后恢复（重选 P）；登记成功但下发未完成时的检查覆盖新增标的。
- 共存：同一空间同时有 `invoke` 与 `timer` 任务时，旧调度与新对账互不处理对方任务，旧节点池不选新函数。
- 活动日志：幂等追加、分页查询、按空间过滤；Collector 新建与生效计划各写一条，Admin 不可用时下一分钟补写，不重复。
- 用 CloudNode 与 Storage 的测试替身覆盖，不依赖真实云资源。

### 4.10 验收与部署

- 部署 Collector 与 CloudNode；新对账只处理 `fetch_mode=timer` 的任务与带新标记的函数组，**不触碰现有 80 个 Invoke 函数**；旧调度只处理 `invoke` 任务，照常运行。
- 部署后在 canary 数据集（阶段 5）上验证。

**回退**：替换回旧二进制；新表不影响旧逻辑；新函数组由 CloudNode 删除。

## 5. 阶段 4：前端与监控

| 位置 | 改动 |
| --- | --- |
| `web/src/views/collector/task-instances/task-instances.vue` 及首页统计卡片 | 改为三块：分配（函数、地域、标的、数据集 hash、最近下发时间与结果、最近执行观测时间）、标的变更日志（活动日志按类别过滤）、补采作业；不展示周期列表；接口封装与页面测试 |
| 全局活动日志页（新） | 按空间、来源、类别、时间范围分页查看活动日志；采集任务页复用同一组件 |
| Collector `GetTaskResultInventory`（`internal/rpc/task_result_inventory.go:372`） | 直接行情任务不再填 `latest_completed_period`、`latest_completed_status`（不查 Storage，也不再读 `t_collector_period_storage_states`）；条目增加 `allowed_lateness_seconds`；重采样分支仍从本地 readiness 取最近完成周期，不变 |
| Monitor `metrics/kline_freshness.go:105` | 最近已终结周期为空的条目，只按“View 最新数据时间 ≥ 当前时间 − 一个周期 − 允许迟到时间”判断新鲜，告警文案相应调整；有该字段的条目（重采样）逻辑不变 |
| Monitor `metrics/task_result_inventory.go:309` | 最后一份有效的任务结果清单持久化到 Monitor 数据库；Collector 不可用或缓存过期时继续用它检查，并单独告警“控制面数据过期”；两者同时重启后仍可用 |
| Collector `period_storage_reconciler.go` | 新链路不再需要，阶段 6b 随旧代码删除，不做替换 |
| Monitor `observability/overview.go:888` | stockcn feed 健康检查不再依赖函数经 EventBus 上报的指标 |
| Monitor 新规则 | 周期无任何提交、对账下发失败、因迟到被拒、hash 不一致被拒、补采积压、429、函数内存峰值（来自 SCF 指标与 CLS） |

验收：停掉 Collector，Monitor 的 K 线新鲜度检查继续运行并产生“控制面数据过期”告警；页面三块数据正确。

## 6. 阶段 5：crypto canary

1. 新建**隔离的 canary 采集任务**（独立数据集，不与生产数据集共享周期期望）：现货 1m、现货 1h、合约 1m、合约 1h，各 50 个标的（每个市场一个函数，同一函数同时服务 1m 与 1h），全部 `fetch_mode=timer`。
2. 对账自动登记快照与生效表，结果数据集带允许迟到时间属性（15 秒），创建南京现货、香港合约各一个函数（VPC + 公网、每函数 50 标的、并发 50、rps/burst 50），以及两个补采函数。
3. 连续运行至少 24 小时，覆盖多次整点（1m 与 1h 同时到期）。
4. 验证项：
   - 每分钟、每小时周期 complete；因迟到被拒 ≤ 0.1%；无 hash 不一致被拒；
   - 收盘到 Storage 接受 p99 ≤ 8 秒（周期口径与序列口径），同时记录冷启动样本；
   - 峰值内存、429 次数、compute-1 出口带宽、计划环境实际大小；
   - 停掉 Collector 10 分钟，canary 采集不受影响；
   - 修改 canary 任务的标的（增删一个），观察生效周期 P 前后切换正确、旧份收尾；
   - 人为删除一段数据，`kline check` 发现、`kline backfill` 补齐、复查通过；
   - **交接演练**：再建一个 canary 任务，先以 `fetch_mode=invoke` 运行，按阶段 6a 的规则在整点 T 交接给 timer，确认 `< T` 的 1m 与 1h 周期由旧链路完成、`≥ T` 由新链路完成，无双采、无空窗；再按回退规则交回 invoke 演练一次。
5. 结果写入设计文档第 14.2 节。

## 7. 阶段 6：crypto 切换与清理

### 7.1 阶段 6a：切换（按目标周期交接）

交接边界 T 是 **K 线开始时间**（整点），按目标周期划分：旧链路负责 `< T` 的周期，新链路负责 `≥ T` 的周期。例如 T = 13:00：13:00 那次触发要采的是 12:59 的 1m 与 12:00 的 1h，仍属于旧链路；新函数第一次提交的是 13:00 开始的 1m（13:01 触发）与 13:00 开始的 1h（14:00 触发）。

1. 提前设置生产 crypto 任务的 `invoke_until_period = T`：旧调度只规划和重试目标周期 `< T` 的批次。
2. 新对账识别到这些待交接任务，为它们生成计划（计划份 `from = T`）、登记快照（生效周期 = T）、创建新函数组（约 11 个现货 + 11 个合约 + 补采函数）。
3. 到 T 之后，旧链路完成最后一轮 `< T` 的批次并处理完在途任务、重试（等待旧调度的最大截止时间），再把这些任务的 `fetch_mode` 改为 `timer`、清除 `invoke_until_period`，旧 Invoke 函数停止被调用（函数本身保留）。
4. 观察 T 之后至少 30 分钟与第一个整点：周期 complete、延迟、Monitor 新鲜度。

**回退**（七天回退窗口内）：同样按目标周期交接，不新增环境字段。

1. 选新的边界 T'（整点），给任务设置 `invoke_from_period = T'`，旧调度只规划和重试目标周期 `≥ T'`。
2. 新函数照常运行到负责 `< T'` 的最后一轮（1m 为 T' 那次触发、1h 为 T' 整点触发），确认这些周期完成后停用新函数定时器。
3. 把任务 `fetch_mode` 改回 `invoke`，旧链路从 `≥ T'` 起接管；回退稳定后清除 `invoke_from_period`。

新链路已写入的数据保留，不覆盖。旧链路的接口、权限、EventBus 流与旧函数在 6b 之前都保留，回退只需切换任务归属与对应二进制。

### 7.2 阶段 6b：回退窗口结束后清理

七天内无回退需要后执行。**会破坏回退能力的清理都放在这里**，不在 6a 中做。

| 范围 | 删除内容 |
| --- | --- |
| 临时共存措施 | `fetch_mode`、`invoke_until_period`、`invoke_from_period` 参数与过滤逻辑；旧节点池的排除逻辑；`GetDatasetPeriodStatus` 的嵌套 `expectation` 形态 |
| Collector | 调度器的规划、派发与重试（`scheduler.go` 中直接行情部分）、`timer_period_planner.go`、`timer_batch_claimer.go`、`timer_runtime.go`、`completion.go`、`period_storage_reconciler.go`、`period_failure_reporter.go`、`storage_write_consumer.go` 中直接行情路径、旧 `reconciler.go` 与 `assignment.go` 的旧分配；`MarketFetchRuntime` 服务与 `ClaimTimerBatch`（proto、生成代码、注册、tRPC 配置） |
| Collector 表 | `t_collector_timer_period_batches`、`t_collector_fetch_batches`、`t_collector_fetch_batch_items`、`t_collector_fetch_retry_items`、`t_collector_task_period_series`、`t_collector_period_storage_states`；`t_collector_task_series` 实施时核对是否仍有用途。**保留**重采样使用的 `t_collector_task_instances`、`t_collector_instance_write_targets`、`t_collector_runs`、`t_period_readiness`、`t_period_readiness_items` |
| Storage | `EnsureDatasetPeriod`、`RecordDatasetPeriodFailures`（确认无调用方后）；旧周期内嵌快照按原 hash 一次性迁入共享快照存储并验证历史查询后，删除对内嵌 `series_snapshot` 的读取 |
| 事件 | `MarketFetchBatchCompleted` 事件与 `marketfetchpb` 中相关消息；EventBus 的 `MOOX_MARKET_FETCH` 流。EventBus 启动时会拒绝与受管流 subject 重叠的未声明流（`eventbus/internal/registry/registry.go:57`），所以先删除磁盘上的该流，再从配置中移除，避免残留流影响启动 |
| 服务目录 | `scf-collector` 的 `EnsureDatasetPeriod`、`RecordDatasetPeriodFailures`、`ClaimTimerBatch` 授权；`market-fetch-publisher` 角色 |
| 旧函数 | 删除旧的 80 个 Invoke 函数 |
| CLI | `collector function publish` 中 Invoke fleet、EventBus 凭据、`canary_task_id` 相关逻辑；`timer_function_count` 等旧配置 |
| 文档 | 采集、存储、外部接入、云节点、事件总线、命令行工具文档按新链路重写 |

验收：全量 crypto 连续 24 小时满足阶段 5 的指标；Collector SQLite 每分钟写入量降到对账与补采的量级。

### 7.3 交接与回退的实施约束

1. `fetch_mode` 与交接边界字段（`invoke_until_period`、`invoke_from_period`）在同一个数据库事务中更新，避免短暂出现没有边界的旧调度。
2. 回退停用新函数定时器时，防止对账或在途的配置作业把它重新启用；必须在第一次可能采集 `≥ T'` 的触发之前确认停用，失败则重新选择交接边界。
3. 清除 `invoke_from_period` 前，确认低于边界的遗留重试都已终结；否则保留下界。
4. 旧周期内嵌快照的迁移保持原 hash、序号和总数，可幂等续跑；直接写入快照存储，**不调用**会追加生效表的 `RegisterSeriesSnapshot`；历史查询验证通过后再删除旧读取分支。

## 8. 阶段 7：stockcn

- 计划计算与函数代码已统一；补充：交易日历判断（函数按日历跳过非交易周期）、settle delay 与错峰窗口、TDX 每函数 40 个标的、迟到 90 秒（按实测调整）。
- 部署到 `scf_markets.stockcn` 配置的地域（默认南京）；核对命名空间与并发配额。
- 公共日历覆盖期（当前至 2026-12-31）加入部署检查与告警。
- 验收：覆盖开盘、午休、收盘与非交易日，周期 complete，非交易时段无周期记录、无告警。

## 9. 阶段 8：去 TLS 与防火墙

前提：所有 SCF 函数已不连 EventBus（阶段 6b、7 完成）。

1. 按设计 7.3 的影响面表改造：`gatewayclient`、hostgateway listener 与控制面连接池、unitinstall/unitbundle/unitbootstrap、Admin PKI（只删自建内部 PKI，保留签名密钥与 Console 证书）、EventBus 服务与 jetstream 客户端、HostAgent、脚本与示例配置。
2. 防火墙（设计 7.4）：扩展 `moox-cli setup firewall` 为按来源对账，收窄宽规则（含安全组重叠规则、端口范围、IPv6），输出差异；`setup init` 中已调用，确保 partial 时失败；更新 `skills/moox` 的 SKILL.md 指引。
3. 切换顺序：项目无兼容要求，不做“同时接受 TLS 与明文”的过渡版本，改为**一次性停机切换**。维护窗口内**先**执行防火墙对账，把 11003、4222 收窄为主机白名单并验证生效；**白名单验证失败则不启动明文服务**，保持旧发布。白名单生效后，再按主机顺序（Storage 主机、compute-1、控制机）替换全部单元并重启。
4. 验收：全部链路在无 TLS 下正常（Console 9527 仍为 HTTPS）；防火墙差异输出符合预期；Monitor 全部检查恢复。

**回退**：维护窗口内保留各主机旧发布目录与证书材料，失败时整体回切旧发布。

## 10. 风险与检查点

| 风险 | 检查点 |
| --- | --- |
| Storage 懒建周期与现有周期逻辑耦合复杂 | 阶段 1 单独上线并在旧链路下稳定运行后再进入阶段 3 |
| 计划环境超过 4KB | 阶段 3 的容量校验；canary 记录实际大小 |
| SCF 配置更新批量下发被云 API 限频 | 全局并发信号量；canary 变更测试中观察 |
| 因迟到被拒比例高 | canary 统计，必要时把 crypto 迟到调到 30 秒 |
| 新旧链路同时采集同一数据集或交接空窗 | `fetch_mode` 互斥归属；按目标周期交接（旧 `< T`、新 `≥ T`）；canary 中演练交接与回退 |
| 清理后无法回退 | 破坏回退能力的清理全部放到阶段 6b（七天后） |
| EventBus 残留流影响启动 | 阶段 6b 按“先删磁盘流、再改配置”的顺序 |
| 去 TLS 一次性切换失败或中途暴露明文端口 | 阶段 8 先收窄并验证白名单再启动明文服务；保留旧发布目录整体回切 |

## 11. 交付物清单

- 代码：阶段 1～8 各自的提交；schema 变更与格式校验通过。
- 文档：设计文档第 14.2 节补充 canary 结果；各模块文档同步更新。
- 运维：新的 setup 配置（`scf_regions`、`scf_markets`）、CLI 命令说明、回滚包（旧代码包、函数与触发器清单、数据库快照）。


## 12. 实施记录（与原计划的差异和补充）

1. **分配算法**：同一市场的全部标的按规范顺序“稳定分配”——标的尽量留在原函数，新增标的填入有空位的函数，超出容量的移走；任何数据集在某函数上的内容（hash、总数、序号、标的集）变化都会被识别并按两份过渡。同一空间内市场类型相同的不同数据源会让函数名冲突，对账直接报错。
2. **P 的提前量**：2 分钟；计划里含需要新建的函数时为 6 分钟。首次登记且来自旧链路交接边界的数据集，P 直接取边界（边界已过则顺延并告警，期间缺口由自动补采补回）。
3. **回退**：任务保持 `timer` 并设置 `invoke_from_period=T'` 时，数据集按“不再采集”处理，新份起点取 T'（空标的），旧份服务到 T'；之后再 `--mode invoke`。CLI 在回退到 invoke 时强制要求下界，在 T 未过时拒绝清除 `invoke_until_period`。
4. **停用定时器**：CloudNode 预检要求 cron 非空，停用时用占位 cron 并设 `enabled=false`。
5. **空间管理**：对账管理器每分钟取 Admin 中启用的空间（配置的空间 ∩ 启用空间），新增则启动协程；停用或删除则先停用该空间全部函数定时器，确认后退出协程。
6. **读回**：每小时同步读回云端（CloudNode 列表请求带 `refresh_readback`，逐个读取 SCF 函数与定时器的真实状态，并记录云端实际生效的分配 hash 与 Access 入口）；读回时间必须是刚做的，读回失败的函数不算核对完，几分钟后重试；云端已不存在的函数先走受租约保护的删除清掉目录记录，再按已生效计划重建；Access 入口与调用方密钥纳入漂移判断；逐项结果，部分失败不推进读回时间。未生效计划存在期间，不受该计划影响的函数照常读回与修复。
7. **补采**：函数名含 `-plan-` 以便旧节点池排除；创建补采函数用独立的发布租约并串行，补采函数暂不可用不算分块失败；自动补采的水位只在作业全部成功后推进，窗口内分块过多时对半缩小；检查与补采的范围按（标的×时间点）总数设上限。
8. **新增命令**：`collector fetch-plan`、`collector task fetch-mode`（交接/回退）、`collector function upload`（只上传代码包）。函数代码包不再强制携带 EventBus CA。
9. **Storage**：登记快照的 P 上界为 7 天；日、周、月频率至少要求整分钟；同一 (subject_id, series_tag) 不能重复；缺少数据集允许迟到属性时打告警。
10. **未做/延后**：阶段 6b（需七天回退窗口）；Monitor 中依赖 Storage 计数器的新规则（因迟到被拒、hash 不一致被拒、429、内存峰值）延后到 canary 观察后补；新形式周期缺快照时仍宽松读取。
11. **函数代码包升级**：`scf_plan.package_id`（或空间覆盖项）换成新版本后，对账器每两分钟检查一次，把代码包落后的计划函数（和补采函数）通过受租约保护的部署作业升级到新版本，只换代码，环境与定时器不动。CloudNode 部署时保留已存在的定时器状态（此前每次部署都会把定时器重置为停用）。服务目录授权 Collector 调用 `SubmitDeployNodes`、`SubmitDeleteNodes`。
12. **创建失败的遗留预留**：创建失败后 CloudNode 目录里会留下“未就绪”的预留，导致同名函数无法重建。对账器识别超过 15 分钟仍未就绪的预留，经受租约保护的删除作业清掉后再创建；补采函数同理。
13. **补采**：调用补采函数使用独立事件来源（不发布完成事件，函数不连 EventBus）；分块的名额有效期在真正调用前续期，完成与续期都带尝试号，旧尝试不能覆盖新尝试；函数暂不可用时分块退回待认领并退回这次尝试；按数据集、频率、标的与时间定位负责该标的的函数计划份（例外符号只保存在该份里）；完整性检查接入 stockcn 交易日历，缺口不跨休市区间，月线按日历月计数；点数超限当作可缩窗错误处理。
14. **多空间部署**：部署参数按空间生成，空间身份由对账器强制写入，`scf_plan.spaces.<space>` 可覆盖代码包、命名空间与基础环境文件；同时管理多个空间时每个空间必须有自己的代码包。Collector 停机期间被停用的空间，启动时先做一次停用收尾。
15. **取数窗口**：crypto 每函数并发提高到与标的数相同（50）；canary 发现整五分钟前后行情源（Binance 现货）出现延迟尖峰，单次请求要几百毫秒到数秒，原先把剩余预算平均切给 7 个备用端点（每个只有约 430 毫秒）会把慢响应全部掐断。现在单个端点至少 1.2 秒、单次尝试至少一个请求超时（3 秒）；南京出口对 `api*.binance.com` 的 DNS 被污染（解析到不可达地址），现货函数改到香港。函数运行结果带上失败原因归并与相对收盘的起止时间，便于定位。
16. **授权收紧**：Storage 对 Collector 身份的时序行写入只放行自有的采集与重采样数据集；首次登记快照的 P 也必须晚于 Storage 当前时间。
17. **第二轮审查后的处理**：补采后仍缺的 K 线只在单根分块时按“行情源没有”记忆（键含空间），多根分块仍按失败重试；stockcn 日历不可用时检查直接报错且不缓存失败；补采清理预留用自己的租约与在途集合；小时重建先清理遗留预留、代码升级失败不阻断重建；触发器缺失按元数据修复；停用空间按 100 个一批；同截止时间的多频率按周期短的先执行；部分失败保留 `partial_failed`。
18. **第三轮审查后的处理**：升级、清理、停用与漂移修复的云作业超时后记入辅助在途集合（`plan_aux.go`），结束前不对同一批函数重复提交，在途期间不释放发布租约；清理遗留预留后不再提前释放租约；代码升级失败只记日志且有独立时间预算；停用确认用云端新鲜读回；小时校验补查定时器 Message/Qualifier；单根补采遇到“行情源空结果”按行情源缺失处理；stockcn 检查日历超出覆盖期或计算出错时报错（不再当作休市），日历每小时重新加载；按时间选计划份时，新旧两份符号映射不一致则拒绝补采。
19. **第四轮审查后的处理**：按时间选计划份时，标的符号映射按原始新旧计划与生效时间 P 判定（P 之前用旧映射、P 及之后用新映射），不再比较已合并的派生计划；在途判定统一为“辅助作业 ∪ 计划作业”，所有变更入口（升级、清理、修复、计划下发、停用）都排除在途函数；停用收尾时结算已终态的计划作业，仍有在途作业则不宣布完成；辅助作业每轮入口刷新、运行中续租、无在途时释放租约，超过 20 分钟（大于 CloudNode 单项 16 分钟上限）才凭年龄解除并告警；代码升级的等待预算不超过整轮剩余时间的三分之一，`verifyFleet` 的升级入口同样受限。
20. **第五轮审查后的处理**：停用收尾先结算在途作业、再做云端新鲜读回，避免读回之后启用作业完成而误判停用完成；停用确认后作废未完成计划里的下发与收尾确认（原 P 与登记结果不变），并让已生效函数在重新启用后立即校验，重新启用时按原计划重推；在途作业（辅助作业与计划作业）不再凭年龄丢弃，超过 20 分钟（计划作业 10 分钟）只告警，必须等到终态才解除互斥；纯符号映射变更（标的集合不变、没有数据集 P）没有明确生效边界，补采拒绝该变更时间之前的历史数据。
21. **第六轮审查后的处理**：纯符号映射变更没有明确生效边界，所在计划未生效时补采一律拒绝，已生效后以其最终更新时间为保守上界（此前的历史数据拒绝，此后的照常）；停用前先持久化作废未完成计划的下发确认，再提交停用（部分停用后崩溃，重启重新启用也会重推）；计划作业超龄同样只告警一次。永不到终态的云作业会持续阻止停用、推进与升级（结果不明时保留互斥）；CloudNode 目前没有取消批量作业的接口，需人工恢复流程：先恢复 CloudNode 或云 API 让原作业到终态，无法恢复时先阻止其继续领取与恢复执行、确认云端结束并读回状态，再终结作业，不能只清空 Collector 的在途集合；自动取消接口留作后续。
22. **补采吞吐**：E2E 发现补采只在每分钟的调度节拍上认领分块（并发上限 5），765 块的窗口要排两个多小时，老窗口还会堵住新缺口。现改为分块执行完（成功或失败）立刻补位认领下一块（`Backfiller.Refill`，生产开启，认领串行化不超并发上限）；退回名额、结果不明的分块不触发补位，避免函数未就绪时空转。
23. **任务更新校验**：交接演练发现 `UpdateTask` 把 `fetch_mode`、`invoke_until_period/from_period`、`symbols_per_function`、`allowed_lateness_seconds` 当成不可变执行契约而拒绝（`task collect_params cannot change`），旧链路到 timer 的交接与回退因此无法通过 `collector task fetch-mode` 完成。这些是可变的运维归属属性，更新校验不再比较；频率等序列范围仍不可改。
24. **函数日志上报 CLS**：SCF 函数日志不会自动投递，原先 `Handler.Reporter` 虽然声明、环境里也有 `MOOX_CLS_*`，但没有任何地方创建与调用，CLS 主题长期为空。现接入 `packages/clsreporter`：函数每次调用按环境变量创建上报器，取数管线上报两类记录——`market_fetch_item_failure`（每个失败项：函数、数据集、频率、标的、交易所符号、结果类别、错误类型与全文、尝试次数、取数耗时、窗口）与 `market_fetch_batch`（每批：条数、失败数、取数耗时、剩余窗口）；调用结束时同步一次性 Flush（有界超时，失败只记日志）。CLS 主题索引已补充这些字段（保留原 `SCF_*` 索引），字段检索与 SQL 统计可用；索引只对之后写入的日志生效。
25. **CLS 自动开通与下发**：初始化与函数创建都自动完成，用户无感知。`setup init` 在登记云账号后幂等开通 CLS（服务、日志集、主题）并补齐采集日志字段索引（`CLSAPI.EnsureIndexFields`，保留主题原有索引），失败不阻断、记入摘要；CloudNode 新增 `cls:` 配置（默认开启）与 `clsprovision`，创建函数与运行时配置更新时给采集函数补齐 `MOOX_CLS_*` 环境（首次使用同样幂等开通，缓存与失败退避，已声明的保持不动，失败只记日志）。字段清单统一在 `cloudprovider/tencent/cls_fields.go`，collector 测试保证上报字段都有索引。现有生产函数的环境本来就已声明，不受影响。
26. **Timer 模式纳入初始化部署**：`moox.toml` 新增 `[scf_plan]`（空间×市场→地域、地域 VPC 与子网、可选云账号、内存与每函数标的数）。`setup deploy` 渲染 Collector 的 `scf_regions/scf_markets/scf_plan`（Access 私网入口由同地域带 access 组件主机的 `private_address` 推导；关闭时移除这三块）；`setup init` 登记云账号、开通 CLS，并为每个空间保证有可用代码包（已有则沿用，没有则构建上传，失败不阻断）。Collector 侧：代码包改为按名称解析最新可用版本（`scf_plan.package_name`，每轮至多每 30 秒查一次 CloudNode，失败沿用上次结果，从未成功则拒绝创建函数；`package_id` 仍可固定版本，二选一），上传新版本后函数自动升级；函数基础环境由 Collector 生成（空间身份与由 Storage 主密钥派生的应用密钥），`base_environment_file` 变为可选的额外环境；服务目录新增 Collector 调用 `GetPackageList`（只读）。
27. **仍延后的审查项**：自动补采遇到单个周期大面积缺失（超过单次入队上限）时按标的分批入队——只影响 stockcn 全量恢复，限量 crypto 不受影响，stockcn 切换前必须完成；1h 与 1m 同截止时间的取数预算隔离（需有限并行），整点按频率分别统计缺失与超时，不达指标不通过验收。
