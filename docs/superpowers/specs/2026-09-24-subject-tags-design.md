# 数据对象 Tag 化与标的采集器独立设计

## 背景与问题

当前数据对象（Subject）的维护与行情采集耦合在一起：

- 标的列表由 SCF 上的 `instrument` 类型采集任务（`builtin-binance-spot-symbols-1h`、`builtin-binance-swap-symbols-1h`、`builtin-stockcn-instrument-1d`）拉取，经 `InstrumentPipeline` 同时写入 `t_subjects`、`t_subject_symbols`、`*_symbols` / `*_instruments` record Dataset，并通过 `StageDatasetSubjectSet` + `ActivateDatasetSubjectSet` 原子替换 `t_dataset_subjects`。
- K 线任务通过 `symbol_dataset_id` 读取「标的 Dataset 的成员」来确定采集范围，即用 Dataset 成员关系隐式表达「标的分组」。
- 为支持 SCF 分片并发写入，Storage 引入了 staging 表、分片激活、generation 围栏、内容指纹等机制，`storagesource` 读取侧也积累了大量兜底分支与 `crypto` 特例。
- `t_subject_symbols` 缺少产品类型维度：币安现货与 U 本位永续共用 subject `BTC-USDT` 和外部代码 `BTCUSDT`，映射行被两者共享，`t_subjects.c_attrs_json.instrument_type` 按最后写入者在 spot / swap 之间翻转。

## 目标

1. 为 Subject 引入 tag 分组，替代「标的 Dataset 成员」这一隐式分组。
2. 标的维护由独立二进制 `moox-collector-subject` 在本地按配置定时完成，不再依赖 SCF。
3. 采集任务通过 tag 指定采集范围，Subject 维护与数据采集解耦。
4. 删除 `t_dataset_subjects` 及其全部配套机制，Dataset 的标的范围由其 tag 实时解析。

## 非目标

- 不引入 tag 交集、排除等复杂选择器。
- 不支持期货交割合约、期权等当前未使用的产品类型。
- 不改变 K 线等时序数据本身的写入与存储格式。

## 核心概念

| 概念 | 含义 |
| --- | --- |
| Subject | 规范标的身份，跨数据源唯一，如 `BTC-USDT`、`600000.SH` |
| Subject Symbol | Subject 在某数据源、某产品类型下的上市关系与外部代码，如 `binance / spot / BTCUSDT` |
| Tag | Subject 的分组标签，多对多；分为同步托管 tag 与手工 tag |
| 数据集标的范围 | Dataset 声明的 tag 列表，成员为这些 tag 的并集 |

前端不暴露 Dataset 概念：一个采集任务自然产生一个 Dataset，用户在采集任务表单中选择 tag，tag 实际存储在该 Dataset 上。

## 数据模型（`modules/storage/schema/metadata.sql`）

### `t_subjects`（保留）

一个规范身份一行，只存与数据源、产品类型无关的属性（名称、base / quote、币种、时区等）。`c_status` 收敛为 `active`、`disabled`，`disabled` 仅表示人工停用。

### `t_subject_symbols`（改造）

- 新增 `c_instrument_type`，取值：`spot`、`swap`、`equity`、`etf`、`index`、`convertible_bond`。
- 唯一键改为 `(c_space_id, c_data_source_id, c_instrument_type, c_external_symbol)`，并保证 `(c_space_id, c_subject_id, c_data_source_id, c_instrument_type)` 唯一。
- `c_status` 取值改为 `active`、`delisted`；新增 `c_delisted_at`，下架时由同步器写入，重新上市时清空并恢复 `active`。
- 上市维度明细（最小价格变动、下单精度、上市时间等）放入 `c_attrs_json`。

### `t_subject_tags`（新增）

```sql
CREATE TABLE IF NOT EXISTS t_subject_tags (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_tag TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_owner TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (c_space_id, c_subject_id) REFERENCES t_subjects (c_space_id, c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_tag, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_subject_tags_subject ON t_subject_tags (c_space_id, c_subject_id);
```

- `c_owner` 为 `sync:<tag>` 或 `manual`。同一 tag 的所有成员 owner 必须一致。
- 配置文件中声明的 tag 为托管 tag：只能由同步器写入，手工接口拒绝修改；同步器不触碰手工 tag。
- 标的下架不从 tag 中移除，下架状态体现在 `t_subject_symbols.c_status = 'delisted'`。

### `t_datasets`（改造）

新增 `c_subject_tags_json TEXT NOT NULL DEFAULT '[]'`，表示 Dataset 的标的范围，成员为这些 tag 的并集。record 类 Dataset（如财务数据）同样用 tag 声明范围。

### 删除

- `t_dataset_subjects`、`t_dataset_subject_set_staging` 及其索引、触发器。
- `dataset_binance_spot_symbols`、`dataset_binance_swap_symbols`、`dataset_stockcn_instruments` 三个 record Dataset 及其种子配置、字段定义。

## Storage 接口（`modules/storage/proto/metadata.proto`）

### 新增

- `SyncSubjectSnapshot(space_id, tag, data_source_id, instrument_type, items[])`：单事务完成——
  1. upsert 快照中的 Subject；
  2. upsert 该 `data_source_id + instrument_type` 下快照中的 Subject Symbol 并置为 `active`，快照中不存在且当前为 `active` 的置为 `delisted` 并写 `c_delisted_at`；
  3. 将快照中的 Subject 加入 tag（owner 为 `sync:<tag>`），已有成员保留。

  若 tag 已存在且 owner 不是 `sync:<tag>`，拒绝写入。
- `ResolveSubjects(space_id, subject_tags[], data_source_id, instrument_type, include_delisted)`：服务端 join tag 成员与 Subject Symbol，返回 `subject_id`、`external_symbol`、`status`，以及因缺少映射被跳过的数量。
- `SetSubjectTags` / `RemoveSubjectTags`：维护手工 tag，拒绝写托管 tag。

### 改造

- `ListSubjects` 增加 `subject_tags` 过滤，返回结果附带各 Subject 的 tag 与 Subject Symbol 状态。
- `ListDatasetSubjects` 保留接口名，改为按 Dataset 的 `subject_tags` 实时解析全部成员（含已下架），返回 Subject 列表。下游 merge、view 回补、archive、strategy、monitor 无需改变调用方式；strategy 中「绑定为空时回退到全量目录」的兜底分支删除。
- Dataset 的创建与更新接口支持 `subject_tags`。

### 删除

`RegisterDataSubject`、`BindDatasetSubject`、`StageDatasetSubjectSet`、`ActivateDatasetSubjectSet`，以及 `crud_subject_set.go` 与相关 accessproxy、gateway、CLI、前端调用。

## `moox-collector-subject`

### 位置与部署

- 代码：`modules/collector/cmd/subject`，产物 `moox-collector-subject`；配置：`modules/collector/config/subject.yaml`。
- 复用 `modules/collector/internal/marketdata` 下现有的币安、新浪、东方财富等标的抓取实现。
- 独立进程，与 `moox_collector` 无运行时依赖；部署在能直连交易所的实体机上，单实例运行；通过 service gateway 调用 Storage Metadata。

### 配置

```yaml
tags:
  - tag: binance_spot
    space: crypto
    data_source: binance
    instrument_type: spot
    every: 1h
  - tag: binance_swap
    space: crypto
    data_source: binance
    instrument_type: swap
    every: 1h
  - tag: cn_a_share
    space: stockcn
    data_source: stockcn
    providers: [sina, eastmoney]
    instrument_type: equity
    cron: "0 9 * * 1-5"
    timezone: Asia/Shanghai
    min_count: 4000
```

- `providers` 可选，缺省为 `[data_source]`；多个 provider 时沿用现有快照合并逻辑。
- `every` 与 `cron` 二选一。
- 保护参数 `min_count`（默认 1）、`max_drop_ratio`（默认 0.2）可按 tag 覆盖。

### 同步流程

1. 启动时按配置注册全部托管 tag，立即执行一轮，之后按调度执行。
2. 拉取 provider 全量快照并合并。
3. 保护校验：数量低于 `min_count`，或 active 数量较上次下降超过 `max_drop_ratio`，拒绝本次写入并告警，保留上次结果。
4. 调用 `SyncSubjectSnapshot` 一次写入。
5. 上报指标：每个 tag 的最近成功时间、active 数、delisted 数、失败次数。

## 采集侧

### 删除

- `instrument` 采集类型（`modules/collector/internal/jobs/symbol`）、`InstrumentPipeline`、SCF 标的路由。
- `config/setup/collection-tasks.yaml` 中三个标的采集内置任务。
- K 线参数 `symbol_source`、`symbol_dataset_id`；`storagesource` 中的多分页 merge、兜底分支与 `inferResampleSymbolSource`。
- `marketdata` 中与 `InstrumentType` 重复的 `ProductType`，统一使用 `InstrumentType`。

### 采集任务与标的范围

- 采集任务新增 `subject_tags` 参数，仅用于创建或更新任务时写入对应 Dataset 的 `c_subject_tags_json`，任务自身不保存副本。
- 创建任务时校验：tag 存在，且按任务的 `data_source + instrument_type` 解析结果非空。
- K 线规划器每个周期调用 `ResolveSubjects(dataset.subject_tags, task.data_source, task.instrument_type, include_delisted=false)` 生成原子任务；新增标的沿用现有 `history_policy` 回补，下架标的停止增量采集，已有数据保留。
- 运行时解析为空时保留上一轮任务集合并告警，不清空任务。
- 重采样任务生成的 Dataset 默认复制源 Dataset 的 `subject_tags`，用户可修改。

### 内置任务示例

```yaml
- space_id: crypto
  task_id: builtin-binance-spot-kline-1m
  task_name: Binance 现货 K 线 1m
  data_type: kline
  collect_params:
    provider: binance
    source_id: spot_http
    instrument_type: spot
    subject_tags: [binance_spot]
    frequency: 1m
```

## Monitor

`market_canary` 改为按托管 tag 检查：最近同步时间是否超时、active 数量是否低于阈值，不再依赖已删除的 record Dataset。

## 前端

- 数据对象页：增加 tag 列与 tag 筛选；手工 tag 可编辑，托管 tag 只读；Subject Symbol 列表展示 `instrument_type` 与状态，`delisted` 显示为「已下架」及下架时间；全部上市关系均下架的 Subject 显示「已失效」。
- 采集任务表单：增加 tag 多选，替换原「标的来源 Dataset」选择。
- 删除 Dataset 标的绑定面板（`dataset-subject-panel.vue`）。

## 测试

- Storage：`SyncSubjectSnapshot` 事务测试（新增、下架置 `delisted`、重新上市恢复、手工 tag 不受影响、owner 冲突拒绝）；`ResolveSubjects` 与 `ListDatasetSubjects` 解析测试；schema 逐文件载入空 SQLite 并执行 `git diff --check`。
- 同步器：保护规则、多 provider 合并、调度配置解析。
- 采集侧：K 线规划器按 tag 展开、空集保护、创建任务校验。
- 端到端：本地启动 Storage、`moox-collector-subject`、Collector，确认 K 线任务按 `binance_spot` 展开并写入。
