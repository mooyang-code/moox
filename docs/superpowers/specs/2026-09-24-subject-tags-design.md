# 数据对象标签化与标的采集器独立设计

## 背景与问题

当前数据对象（Subject）的维护与行情采集耦合在一起：

- 标的列表由 SCF 上的 `instrument` 类型采集任务（`builtin-binance-spot-symbols-1h`、`builtin-binance-swap-symbols-1h`、`builtin-stockcn-instrument-1d`）拉取，经 `InstrumentPipeline` 同时写入 `t_subjects`、`t_subject_symbols`、`*_symbols` / `*_instruments` record Dataset，并通过 `StageDatasetSubjectSet` + `ActivateDatasetSubjectSet` 原子替换 `t_dataset_subjects`。
- K 线任务通过 `symbol_dataset_id` 读取「标的 Dataset 的成员」确定采集范围，即用 Dataset 成员关系隐式表达标的分组。
- 为支持 SCF 分片并发写入，Storage 引入了 staging 表、分片激活、generation 围栏、内容指纹等机制，`storagesource` 读取侧也积累了大量兜底分支与 `crypto` 特例。
- 各抓取源的标的代码一部分存于 `t_subject_symbols`，一部分由适配器运行时换算：A 股在聚合源 `stockcn` 下只存了新浪格式代码，东方财富等源在代码中换算；币安现货与永续共用 `(crypto, binance, BTCUSDT)` 一行映射，`t_subjects.c_attrs_json.instrument_type` 按最后写入者翻转。

## 目标

1. 用标签（tag）对 Subject 分组，替代「标的 Dataset 成员」这一隐式分组。
2. 标签成员由独立二进制 `moox-collector-subject` 在本地按 cron 定时维护，不再依赖 SCF。
3. 采集任务通过标签指定采集范围，Subject 维护与数据采集解耦。
4. 删除 `t_subject_symbols`、`t_dataset_subjects` 及其全部配套机制，数据模型只保留标的、标签、标签成员三个概念。

## 非目标

- 不支持标签交集、排除等复杂选择器；需要时新建手工标签表达。
- 不做跨标签的有效性过滤；每个标签的成员有效性由该标签自身维护。
- 不在标签成员失效时通知标签维护人。
- 不支持期货交割合约、期权等当前未使用的产品类型。
- 不改变 K 线等时序数据本身的写入与存储格式。

## 核心概念

- **标的（Subject）**：跨数据源唯一的身份，如 `BTC-USDT`、`600000.XSHG`。
- **标签（Tag）**：Subject 的分组，与 Subject 多对多，按 Space 隔离。
- **标签模式**：
  - `auto`（自动）：成员跟随数据源标的列表，由程序增删，页面只读；
  - `manual`（手工）：成员由人维护；可选开启「自动探测有效性」，由程序标记成员有效或失效。
- **成员状态**：`active`（有效）、`inactive`（失效）。标的下架、停止交易或在探测源中不存在时置为 `inactive`，重新出现时恢复 `active`；失效成员不从标签中移除，以保证历史数据仍可查询。
- **数据集标的范围**：前端不暴露 Dataset 概念，一个采集任务自然产生一个 Dataset。用户在采集任务表单中选择标签，标签存储在该 Dataset 上；采集范围为所选标签中 `active` 成员的并集。

## 标的 ID 与抓取源代码

数据库不再存储各抓取源的标的代码，由各抓取源适配器自行换算：

- `ToSymbol(subject_id)`：K 线等请求前将标的 ID 换算为该源代码；该源不支持的标的直接返回 `ErrUnsupportedSymbol`，候选链跳到下一个源。
- `ToSubjectID(item)`：仅提供标的列表的源实现，由列表项的结构化字段生成标的 ID，不做字符串猜测。

标的 ID 规则集中定义，函数统一命名为 `SubjectID`（去掉 `Canonical` 前缀）：

| 空间 | 规则 | 示例 |
| --- | --- | --- |
| `crypto` | `crypto.SubjectID(base, quote)`，取交易所返回的 `baseAsset` / `quoteAsset` | `BTC-USDT`、`1000PEPE-USDT` |
| `stockcn` | `stockcn.SubjectID(code)`，按代码首位确定交易所 | `600000.XSHG`、`000001.XSHE`、`920000.XBSE` |

换算示例：`600000.XSHG` → 新浪 `sh600000`、东方财富 `1.600000`；`BTC-USDT` → 币安 `BTCUSDT`；OKX 现货 `BTC-USDT`、永续 `BTC-USDT-SWAP`。个别不规则代码在适配器内维护覆盖表。

`ToSubjectID` 失败（格式无法识别、缺少 base / quote）时跳过该项并计数告警，不得回退为以原始代码作为标的 ID；同一次列表中两项生成同一标的 ID 时，两项均拒绝。

## 数据模型（`modules/storage/schema/metadata.sql`）

### `t_subjects`（保留）

一个标的一行，只存与数据源、产品类型无关的属性（名称、类型、市场、币种、时区）。`c_status` 收敛为 `active`、`disabled`，`disabled` 仅表示人工停用。

### `t_tags`（新增）

```sql
CREATE TABLE IF NOT EXISTS t_tags (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_tag TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_mode TEXT NOT NULL,
    c_builtin INTEGER NOT NULL DEFAULT 0,
    c_sources_json TEXT NOT NULL DEFAULT '[]',
    c_instrument_type TEXT NOT NULL DEFAULT '',
    c_cron TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT 'UTC',
    c_min_count INTEGER NOT NULL DEFAULT 1,
    c_max_drop_ratio REAL NOT NULL DEFAULT 0.2,
    c_run_requested_at DATETIME NOT NULL DEFAULT '',
    c_last_run_at DATETIME NOT NULL DEFAULT '',
    c_last_status TEXT NOT NULL DEFAULT '',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_mode IN ('auto', 'manual')),
    CHECK (c_builtin IN (0, 1)),
    CHECK (c_last_status IN ('', 'success', 'failed')),
    CHECK (c_min_count >= 0),
    CHECK (c_max_drop_ratio >= 0 AND c_max_drop_ratio <= 1),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_tag)
);
```

- `c_sources_json` 为提供标的列表的抓取源，多个源时列表取并集并沿用现有的多源合并校验。
- `auto` 模式必须填写 `c_sources_json`、`c_instrument_type`、`c_cron`；`manual` 模式三者同时填写表示开启探测，同时为空表示不探测。该约束由 Storage 接口校验。
- `c_cron` 为标准 5 段 cron 语法，按 `c_timezone` 解释。
- `c_run_requested_at` 由页面「立即运行」写入，同步器下次轮询时执行并清空。
- 最近运行信息（`c_last_run_at`、`c_last_status`、`c_last_error`）由同步器回写；有效 / 失效成员数由查询实时统计，不落库。

### `t_subject_tags`（新增）

```sql
CREATE TABLE IF NOT EXISTS t_subject_tags (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_tag TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_inactive_at DATETIME NOT NULL DEFAULT '',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'inactive')),
    FOREIGN KEY (c_space_id, c_tag) REFERENCES t_tags (c_space_id, c_tag) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_subject_id) REFERENCES t_subjects (c_space_id, c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_tag, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_subject_tags_status ON t_subject_tags (c_space_id, c_tag, c_status);
CREATE INDEX IF NOT EXISTS idx_t_subject_tags_subject ON t_subject_tags (c_space_id, c_subject_id);
```

- `c_attrs_json` 存放该成员在探测源中的明细（最小价格变动、下单精度、上市时间等），由同步器写入。
- `t_tags` 与 `t_subject_tags` 均按 schema 规范补充 `c_mtime` 触发器。

### `t_datasets`（改造）

新增 `c_subject_tags_json TEXT NOT NULL DEFAULT '[]'`，表示 Dataset 的标的范围。record 类 Dataset（如财务数据）同样用标签声明范围。

### 删除

- `t_subject_symbols`、`t_dataset_subjects`、`t_dataset_subject_set_staging` 及其索引、触发器。
- `dataset_binance_spot_symbols`、`dataset_binance_swap_symbols`、`dataset_stockcn_instruments` 三个 record Dataset 及其种子配置、字段定义（含 `modules/cli/config/fields.yaml`、`modules/storage/config` 中的引用）。

### 内置标签种子

内置标签定义在 `config/setup/subject-tags.yaml`，由 Storage metadata 初始化流程在系统初始化时写入 `t_tags`，`c_builtin = 1`；记录已存在时不覆盖，保留用户在页面上的修改。

```yaml
tags:
  - space_id: crypto
    tag: binance_spot
    description: 币安现货全部标的
    mode: auto
    sources: [binance]
    instrument_type: spot
    cron: "0 * * * *"
    timezone: UTC
  - space_id: crypto
    tag: binance_swap
    description: 币安 U 本位永续全部标的
    mode: auto
    sources: [binance]
    instrument_type: swap
    cron: "0 * * * *"
    timezone: UTC
  - space_id: stockcn
    tag: cn_a_share
    description: A 股全部股票
    mode: auto
    sources: [sina, eastmoney]
    instrument_type: equity
    cron: "0 9 * * 1-5"
    timezone: Asia/Shanghai
    min_count: 4000
```

`instrument_type` 取值：`spot`、`swap`、`equity`、`etf`、`index`、`convertible_bond`。

## Storage 接口（`modules/storage/proto/metadata.proto`）

### 标签定义

- `UpsertTag`：创建或更新标签，校验模式与 `sources` / `instrument_type` / `cron` 的组合、cron 语法、时区。内置标签可编辑，`c_builtin` 不可修改。
- `GetTag` / `ListTags`：返回定义、最近运行信息及有效 / 失效成员数。
- `DeleteTag`：内置标签禁止删除；被任一 Dataset 的 `c_subject_tags_json` 引用时禁止删除，并返回引用方（Dataset ID 与对应采集任务名称）。
- `RequestTagRun`：写入 `c_run_requested_at`。

### 标签成员

- `ListTagMembers(space_id, tag, status, keyword, page)`：返回成员及 Subject 信息；`tag` 为空时列出全部 Subject 及其所属标签。
- `AddTagMembers` / `RemoveTagMembers` / `SetTagMemberStatus`：仅允许 `manual` 标签，`auto` 标签拒绝。
- `ApplyTagSnapshot(space_id, tag, run_at, items[])`：同步器调用，单事务完成：
  - `auto`：upsert 快照中的 Subject；新标的加入标签；快照中存在的成员置 `active` 并更新明细；快照中不存在的 `active` 成员置 `inactive` 并写 `c_inactive_at`；
  - `manual`：不新增成员，只按快照将现有成员置 `active` 或 `inactive`；
  - 回写 `c_last_run_at`、`c_last_status = 'success'`，清空 `c_last_error` 与 `c_run_requested_at`。
- `ReportTagRunFailure(space_id, tag, run_at, error)`：回写失败状态与错误信息，不改动成员。

### 标的解析

- `ResolveSubjects(space_id, tags[])`：返回所选标签 `active` 成员的并集，供采集规划使用。
- `ListDatasetSubjects`：保留接口名，改为按 Dataset 的 `c_subject_tags_json` 解析全部成员（含 `inactive`），供 merge、view 回补、archive、strategy、monitor 使用；strategy 中「绑定为空时回退到全量目录」的兜底分支删除。
- Dataset 创建与更新接口支持 `subject_tags`。

### 删除

`RegisterDataSubject`、`UpsertSubjectSymbol`、`ListSubjectSymbols`、`BindDatasetSubject`、`StageDatasetSubjectSet`、`ActivateDatasetSubjectSet`，以及 `crud_subject_set.go` 和 accessproxy、gateway、admin BFF、CLI、前端中的相关调用。

## `moox-collector-subject`

### 位置与部署

- 代码：`modules/collector/cmd/subject`，产物 `moox-collector-subject`，复用 `modules/collector/internal` 下各抓取源的标的列表实现。
- 运行配置：`modules/collector/config/subject.yaml`，只包含 Storage 地址、鉴权、轮询间隔、抓取超时等运行参数；标签定义以 `t_tags` 为准。
- 独立进程，与 `moox_collector` 无运行时依赖；部署在能直连交易所的实体机上，单实例运行；通过 service gateway 调用 Storage Metadata。

### 支持的抓取源登记

启动时将自身支持标的列表的抓取源及各源支持的 `instrument_type` 写入 `t_data_sources.c_attrs_json.subject_listing`，前端标签表单的数据源、产品类型下拉框据此展示。

### 运行流程

1. 每个轮询周期（默认 1 分钟）读取全部需要运行的标签：`auto` 标签与开启探测的 `manual` 标签。
2. 按 `c_cron` + `c_timezone` 判断是否到期，或 `c_run_requested_at` 非空；到期标签串行执行。
3. 拉取 `sources` 的标的列表，经 `ToSubjectID` 生成标的 ID 并多源合并。
4. 保护校验：列表数量低于 `c_min_count`，或较上次成功运行时的有效成员数下降超过 `c_max_drop_ratio`，判定失败。
5. 成功调用 `ApplyTagSnapshot`；失败调用 `ReportTagRunFailure`，成员保持不变。
6. 上报指标：每个标签的最近成功时间、有效 / 失效成员数、失败次数。

## 采集侧

### 删除

- `instrument` 采集类型（`modules/collector/internal/jobs/symbol`）、`InstrumentPipeline`、SCF 标的路由。
- `config/setup/collection-tasks.yaml` 中三个标的采集内置任务。
- K 线参数 `symbol_source`、`symbol_dataset_id`；`storagesource` 中的代码映射合并、兜底分支与 `inferResampleSymbolSource`。
- 适配器中依赖已存映射的参数（如 `binance.ProviderSymbol` 的 `configured`）及以原始代码作为标的 ID 的回退逻辑。
- `marketdata` 中与 `InstrumentType` 重复的 `ProductType`，统一使用 `InstrumentType`。

### 采集任务与标的范围

- 采集任务新增 `subject_tags` 参数，仅用于创建或更新任务时写入对应 Dataset 的 `c_subject_tags_json`，任务自身不保存副本。
- 创建任务时校验标签存在且 `ResolveSubjects` 结果非空。
- 规划器每个周期调用 `ResolveSubjects(dataset.subject_tags)`，对每个标的由候选链中各源的 `ToSymbol` 换算代码并生成原子任务；新增标的沿用现有 `history_policy` 回补，失效标的停止增量采集，已有数据保留。
- 不做标签与任务数据源的一致性校验；选错标签导致的请求失败按单标的失败跳过。
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

`market_canary` 改为按内置标签检查：最近成功运行时间是否超过 cron 周期的若干倍、`c_last_status` 是否失败、有效成员数是否低于 `c_min_count`，不再依赖已删除的 record Dataset。

## 前端

### 数据对象页（`/#/data/subjects`）

页面分为两个子 tab，删除原「外部符号」抽屉。

**标签**

- 列表列：标签名（内置显示「内置」角标）、描述、模式（自动 / 手工）、数据源与产品类型（未探测显示「—」）、cron 与时区、有效 / 失效成员数、最近运行时间与结果（失败时悬停显示错误）。
- 操作：
  - 成员：切换到「标签成员」tab 并选中该标签；
  - 编辑；
  - 立即运行：调用 `RequestTagRun`，同步器下一次轮询时执行；
  - 删除：内置标签不可删；被采集任务引用时不可删，并列出引用的采集任务。
- 新建 / 编辑弹窗：标签名、描述、模式（自动 / 手工）；手工模式下提供「自动探测有效性」开关；自动模式或开启探测时填写数据源、产品类型、cron 与时区，并预览接下来 3 次运行时间；自动模式的高级选项（默认折叠）包括最少成员数、最大下降比例。

**标签成员**

- 顶部：标签选择（含「全部标的」）、状态筛选（有效 / 失效 / 全部）、关键字搜索。
- 表格列：对象 ID、名称、类型、市场、所属标签、当前标签中的状态、失效时间；展开行查看成员明细。
- 选中手工标签时：支持批量加入（粘贴对象 ID，或在「全部标的」中勾选后加入）、移出标签、将失效成员手动恢复为有效（开启探测时以下一次探测结果为准）。
- 选中自动标签时：成员只读，顶部提示「成员由数据源自动同步」。
- 选中「全部标的」时：显示全部 Subject，隐藏状态列，保留新增与编辑对象入口。

### 采集任务表单

「标的来源 Dataset」选择替换为标签多选，数据来自 `ListTags`。删除 Dataset 标的绑定面板（`dataset-subject-panel.vue`）。

## 测试

- Storage：
  - `ApplyTagSnapshot` 事务测试：`auto` 新增、失效、恢复与明细更新；`manual` 只改状态不增成员；
  - 手工成员接口拒绝 `auto` 标签；`DeleteTag` 的内置与引用保护；
  - `ResolveSubjects` 只返回有效成员并集，`ListDatasetSubjects` 返回全部成员；
  - schema 逐文件载入空 SQLite 并执行 `git diff --check`。
- 同步器：cron 到期判断与 `c_run_requested_at`、保护校验、多源合并、`ToSubjectID` 失败与冲突处理。
- 适配器：各源 `ToSymbol` / `ToSubjectID` 的换算用例（含北交所、`1000PEPE-USDT`、OKX 永续）。
- 采集侧：规划器按标签展开、空集保护、创建任务校验。
- 前端：两个子 tab 的权限与交互（自动标签只读、内置与引用标签不可删）。
- 端到端：本地启动 Storage、`moox-collector-subject`、Collector，确认内置标签完成首轮同步，K 线任务按 `binance_spot` 展开并写入。
