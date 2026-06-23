# MooX Storage

MooX Storage 是面向量化金融场景的统一数据存储服务。它在**同一套元数据和同一个访问入口**下，承接时序行情、静态资料、因子结果、文本与冷归档等多种数据形态，对外只暴露 Space / Dataset / Subject / View 等业务概念，屏蔽底层物理表、分片与存储引擎细节。

> 架构细节见 [`docs/architecture.md`](docs/architecture.md)；概念与协议设计见仓库根目录 `docs/storage-*.md`。

## 能力一览

- **统一写入与读取**：所有数据访问都经由 Access 入口；时序数据使用 `TimeSeriesKey + TimeRange`，记录数据使用 `RecordKey + VersionRange`，写入按事实键做列级更新。
- **多形态数据**：时序（K 线/tick/快照）、记录（公司/交易对资料）、事件、文档、通用表格，以及参数化因子结果。
- **Record 全文检索**：登记 Record View 后由 Bleve 维护版本化索引，支持 `text_query` 与结构化过滤。
- **TimeSeries 物化视图**：登记 TimeSeries View 后由 DuckDB 维护版本化结果，`QueryTimeSeriesRows` 直接读取 active 版本。
- **冷归档**：定时把在线主存数据归档为 Parquet 并登记归档文件。
- **可水平扩展**：通过路由规则把事实数据分片到多个在线主存节点；主存可同进程内嵌，也可独立部署。

### 存储引擎分工

| 引擎 | 角色 |
| --- | --- |
| SQLite | 元数据控制面 |
| Pebble | 在线事实主存（低延迟写入/读取） |
| DuckDB | TimeSeries View 版本化物化结果（OLAP） |
| Bleve | Record View 版本化全文索引 |
| Parquet | 事实冷归档 |

### 事实数据版本语义

Storage 的事实主存统一按 `key + version` 定位一行数据，Access 对外拆成两套更贴近业务的接口：

- **TimeSeries**：`TimeSeriesKey` 由 `space_id + dataset_id + subject_id + freq + dimensions + data_time` 组成，其中 `data_time` 就是版本时间，必须使用 RFC3339/RFC3339Nano。K 线、tick 等有固定 Subject 和固定频率的数据应使用这一类。
- **Record**：`RecordKey` 由 `space_id + dataset_id + record_id + version` 组成。`version` 允许调用方传入；为空时由 Access 使用当前 UTC 时间生成默认版本，写入响应会返回最终 `RecordKey`。
- **列级更新**：同一个 key+version 再次写入时，只更新本次携带的列；未携带列保留旧值。携带 `NULL` 值不会覆盖已有非空值，便于多批因子或资料字段逐步补齐。
- **绑定关系**：TimeSeries 写入不强制校验 `DatasetSubject`，Record 写入也不自动维护对象绑定；这些关系由应用层通过 Metadata 独立登记，便于管理台展示和治理。

### View 版本与切换

View 是用户可查询的派生读模型：TimeSeries View 由 DuckDB 维护版本化结果表，Record View 由 Bleve 维护版本化索引。只要 View 定义变化，例如新增查询列，`view_version` 就会递增；后台构建新版本，成功后再把读取切到新 active 版本。

TimeSeries View 的 DuckDB 结果表按 `ViewColumn` 展开为真实物理列，不再以 `row_json` 作为查询主路径。物理表名以 `view_{view_id}` 开头；视图字段统一使用 `dataset_id.column_name`，并为 `(subject_id, freq, data_time)`、内部行键与每个视图字段创建索引；`QueryTimeSeriesRows` 会把 `subject_id`、`freq`、`time_range`、结构化 filter、sort 和分页尽量下推到 DuckDB SQL 执行。

创建或更新 View 时，主数据集决定 View 的引擎和粒度：时序主数据集对应 DuckDB View，记录主数据集对应 Bleve View。包含数据集只要求已在同一空间注册，不能与主数据集重复；实际物化时是否能对齐取决于 View 字段能否按主数据集粒度聚合。`dataset_id` 必须是 lower_snake_case 且最长 20 字符，`view_id` 必须是 lower_snake_case 且最长 30 字符。

| 字段 | 含义 |
| --- | --- |
| `view_version` | 当前 View 定义版本，新增列或构建形态变化时递增 |
| `active_view_version` | 当前线上读取的 View 版本 |
| `active_result` | 当前线上读取的结果表 / 索引标识 |
| `building_view_version` | 正在后台构建的目标版本 |
| `building_result` | 正在后台构建的新结果标识 |
| `build_status` | 构建状态：`pending` / `building` / `active` / `failed` |
| `build_error` | 最近一次失败原因 |
| `build_started_at` / `build_finished_at` | 最近一次构建开始 / 结束时间 |

构建期间的新增写入会先落主存，再由 View 消费者同步到相关 active/building 结果；重建也会在完成前补刷构建期间的增量，切换后查询只读 `active_result`。

## 环境要求

- Go **1.24+**
- **CGO 开启**（`CGO_ENABLED=1`）并具备 C 编译器（gcc/clang）—— DuckDB 视图存储依赖真实磁盘 DuckDB，no-cgo 构建会在启动 ViewBuilder 时失败。
- 支持 macOS / Linux；Windows 需自备 CGO 工具链。

## 安装与构建

```bash
cd modules/storage

make deps         # 下载依赖
make build        # 构建当前平台二进制到 ./bin/moox-storage

# 交叉/发布构建
make build-linux  # Linux amd64 -> ./release/linux
make build-darwin # macOS       -> ./release/darwin
make release      # 按当前平台打包（含 bin/config/schema/start.sh/stop.sh）
```

也可直接用 go：

```bash
CGO_ENABLED=1 go build -o bin/moox-storage ./cmd/moox-storage
```

## 配置文件

框架配置与业务配置分开，均在 `config/` 下：

| 文件 | 作用 |
| --- | --- |
| `config/trpc_go.yaml` | tRPC server/client/plugin/timer 等**框架配置**（端口、计时器、日志） |
| `config/storage.yaml` | 存储**业务配置**（数据目录、设备路径、主存模式、事件总线） |
| `config/metadata.seed.yaml` | **元数据种子文件**（Space/Dataset/Subject…），通过 CLI 导入 |

### `config/storage.yaml` 各字段含义

```yaml
storage:
  root: ./var/storage                                  # 数据根目录
  roles:                                              # 本进程承担的运行角色
    - access
    - deriver
  metadata:
    path: ./var/storage/metadata/storage_metadata.db   # 元数据 SQLite 文件
  devices:
    pebble_path:  ./var/storage/pebble                 # 在线主存目录
    duckdb_path:  ./var/storage/duckdb/views.duckdb    # 视图物化库
    bleve_path:   ./var/storage/bleve                  # 全文索引目录
    parquet_path: ./var/storage/archive                # 归档目录
  primary:
    service_name: ""        # 留空=同进程内嵌主存；填服务名=走远程 PrimaryStore（分布式）
  eventbus:
    type: nats              # 默认 nats；memory 只用于单进程开发/测试，仍是异步总线
    nats_url: nats://127.0.0.1:4222
    stream_name: MOOX_STORAGE
    subject_prefix: moox.storage
    consumer_name: storage_deriver
  deriver:
    access_service_name: trpc.storage.access.AccessService  # 留空=同进程本地 Access reader
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

### Runtime Roles

`moox-storage` 通过 `storage.roles` 决定同一个二进制在本进程启用哪些运行角色：

| 角色 | 职责 |
| --- | --- |
| `access` | 面向用户的写入和权威读取入口；校验列契约、解析路由、写 PrimaryStore，并发布行变更事件。 |
| `primary` | 拥有 Pebble PrimaryStore RPC；可按路由和配置部署多个 primary 服务。 |
| `deriver` | 消费行变更事件、批量聚合 key、通过 AccessService 回读当前行；TimeSeries View 写入 DuckDB，Record View 写入 Bleve。 |

默认运行角色是 `access + deriver`，不包含显式 `primary`。当 `access` 的 `primary.service_name` 为空时，进程会同时暴露本地 `PrimaryStoreService`，保持单进程/本地主存部署可用；当 `primary.service_name` 非空时，Access 走远程 PrimaryStore，除非显式加入 `primary` 角色。

默认事件总线是 NATS。`memory` 只适合单进程开发和测试；它仍然异步投递事件，不提供写后立即可查派生结果的契约。NATS 行变更 subject 使用 `eventbus.subject_prefix` 拼接：

- `${prefix}.time_series.rows_changed.v1`
- `${prefix}.record.rows_changed.v1`

NATS transport 会为两个 subject 派生不同 durable consumer，避免 TimeSeries 与 Record 消费者冲突。Deriver 事件 handler 只有在派生写入成功后才返回 success；失败会向上传递错误，让 NATS `Nak` 并重试。Deriver 批处理参数为 `deriver.access_service_name`、`deriver.batch_size`、`deriver.batch_wait_ms`、`deriver.max_workers`。

### `config/trpc_go.yaml` 默认服务端口

| 服务 | tRPC | HTTP | 说明 |
| --- | --- | --- | --- |
| MetadataService | 18001 | 19101 | 元数据控制面 |
| PrimaryStoreService | 18101 | - | 在线主存（内部服务） |
| AccessService | 18201 | 19104 | 事实数据读写 |
| ViewService | 18202 | 19105 | 视图/检索查询 |
| admin | 9000 | - | tRPC 管理端口（健康检查） |

后台计时器（在 `config/trpc_go.yaml` 的 `service` 段，通过 cron 串里的 `?disable=1` 开关、`*/30 * * * * *` 调频）：

| 计时器服务 | 作用 | 默认 |
| --- | --- | --- |
| `trpc.storage.view.timer` | 物化视图构建 | 开（每 30s） |
| `trpc.storage.view.cleanup.timer` | 清理旧物化结果表 | 开（每小时） |
| `trpc.storage.view.retry_failed.timer` | 重试失败视图 | 关 |
| `trpc.storage.archive.timer` | Parquet 冷归档 | 关 |

配置路径可被命令行参数或环境变量覆盖：

- 框架配置：`-conf=<file>`，或 `STORAGE_CONFIG_FILE` / `STORAGE_CONFIG_PATH`。
- 业务配置：`-storage-conf=<file>`，或 `MOOX_STORAGE_CONFIG` / `STORAGE_APP_CONFIG`；未指定时默认读取框架配置同目录下的 `storage.yaml`。
- 数据根目录：`MOOX_STORAGE_HOME`。

## 元数据概念

写入任何事实数据之前，必须先把元数据登记好。元数据是控制面，描述"数据长什么样、归谁、放哪、怎么查"。各概念及依赖关系如下（父在前、子在后）：

| 概念 | 含义 | 归属 |
| --- | --- | --- |
| **Space** | 业务命名空间 / 工作区，是一切元数据的根 | — |
| **DataSource** | 数据来源（交易所、数据供应商），如 `binance` | Space |
| **Subject** | 业务对象 / 标的（交易对、股票、公司），**不归属 DataSource** | Space |
| **SubjectSymbol** | Subject 在某来源侧的外部代码映射，如 `AR-USDT → ARUSDT` | Space + Subject + DataSource |
| **Dataset** | 可写事实数据集，**绑定唯一 DataSource**，声明 `data_kind` 与 `freqs` | Space + DataSource |
| **DatasetSubject** | Dataset 与 Subject 的应用层绑定关系，数据写入链路不强制校验 | Dataset + Subject |
| **Field** | Space 级字段字典（`open`/`close`/`symbol`…），声明值类型、单位、示例 | Space |
| **Factor** | 参数化因子定义（算法 + 参数），结果可作为列来源 | Space |
| **DatasetColumn** | Dataset 的列契约，`origin_type` 指向 Field/Factor/System；可声明 required 等写入约束 | Dataset |
| **View** | 查询入口，必须指定 `primary_dataset_id`；TimeSeries View 物化到 DuckDB，Record View 索引到 Bleve | Space |
| **ViewColumn** | View 对外暴露的列 | View |
| **PrimaryStoreNode** | 在线事实主存节点（Pebble），`endpoint` 决定访问地址（`local`=同进程） | — |
| **Device** | 节点上的设备，`engine` ∈ `pebble`/`duckdb`/`bleve`/`parquet_archive`；路由只用 `pebble` 设备 | PrimaryStoreNode |
| **PrimaryStoreRoute** | 把 `(space, dataset[, subject/pattern])` 路由到 PrimaryStoreNode，`hash_rule` 决定分片 | Space + Dataset + PrimaryStoreNode |

> Space 边界说明：Storage 中的 `space_id` 是存储隔离标签和元数据根键，不主动向 Control/Admin 校验 Space 是否存在，也不建立跨服务外键。管理台顶部 Space 选择器的数据来源是 Control/Admin；Storage 的 `ListSpaces` / `GetSpace` 只返回 Storage 自身登记的元数据。写错 `space_id` 可能产生孤立的 Storage 元数据或事实数据，接入方应统一使用 Control/Admin 选中的 `space_id`，或通过初始化脚本显式同步 Storage 侧 Space 元数据。

> 命名注记：`PrimaryStoreRoute` / `PrimaryStoreNode` 为历史命名，语义已收窄为"仅路由 Pebble 在线主存切分"，不路由 DuckDB/Bleve/Parquet 派生设备。

枚举取值（在 seed 文件里用短名书写）：

- `data_kind`：`time_series` / `record` / `snapshot` / `event` / `document` / `table`
- `value_type`：`string` / `int` / `double` / `bool` / `time` / `json` / `bytes`
- `dataset_columns.origin_type`：`field` / `factor` / `system`
- `view_columns.origin_type`：`dataset_column` / `expression` / `system`
- `engine`（设备）：`pebble` / `duckdb` / `bleve` / `parquet_archive`

## 配置与导入元数据

### 1. 编辑种子文件 `config/metadata.seed.yaml`

该文件是**领域型**配置，按上面的概念分块组织。完整示例见 `config/metadata.seed.yaml`，下面摘取关键片段说明各字段：

```yaml
# 业务空间：一切的根
spaces:
  - space_id: crypto                 # 唯一标识（后续所有实体都引用它）
    name: Crypto Market Data
    owner: default
    status: active                   # active 才会被读路径采用

# 数据来源
data_sources:
  - space_id: crypto
    data_source_id: binance
    kind: exchange                   # 自定义分类
    market: crypto
    timezone: UTC
    status: active

# 业务对象 / 标的（交易对）
subjects:
  - space_id: crypto
    subject_id: AR-USDT
    subject_type: crypto_pair
    currency: USDT
    status: active

# Subject 在来源侧的外部代码：AR-USDT 在 binance 叫 ARUSDT
subject_symbols:
  - space_id: crypto
    subject_id: AR-USDT
    data_source_id: binance
    external_symbol: ARUSDT
    status: active

# 事实数据集：绑定 binance，时序型，支持 1m/1h/1d 频率
datasets:
  - space_id: crypto
    dataset_id: binance_spot_kline
    data_source_id: binance
    data_kind: time_series
    freqs: ["1m", "1h", "1d"]
    status: active

# Dataset 与 Subject 绑定（应用层关系；Access 写入链路不强制校验）
dataset_subjects:
  - space_id: crypto
    dataset_id: binance_spot_kline
    subject_id: AR-USDT
    subject_role: normal
    status: active

# 字段字典
fields:
  - space_id: crypto
    field_id: close
    name: Close Price
    value_type: double
    unit: USDT
    write_example: "7.1500"
    status: active

# Dataset 列契约：列 close 来自 Field close，必填
dataset_columns:
  - space_id: crypto
    dataset_id: binance_spot_kline
    column_name: close
    origin_type: field
    origin_id: close
    value_type: double
    required: true
    status: active

# 物化视图：以 binance_spot_kline 为主集，按 subject_id/freq/data_time 物化
views:
  - space_id: crypto
    view_id: spot_kline_close_view
    primary_dataset_id: binance_spot_kline
    dataset_ids: ["binance_spot_kline"]
    grain_keys: ["subject_id", "freq", "data_time"]
    engine: duckdb
    query_window: 30d               # 物化时间窗口
    build_status: pending           # 初始 pending，由 view.timer 物化
    status: active

view_columns:
  - space_id: crypto
    view_id: spot_kline_close_view
    column_name: binance_spot_kline.close
    origin_type: dataset_column
    origin_id: binance_spot_kline.close   # DatasetColumn 来源：column_name 与 origin_id 保持一致
    value_type: double
    sort_order: 1

# 在线主存节点。endpoint=local 表示同进程内嵌（单机）
primary_store_nodes:
  - node_id: local
    endpoint: local
    weight: 100
    status: active

# 节点上的设备
devices:
  - device_id: pebble-local
    node_id: local
    engine: pebble                  # 路由只认 pebble 设备
    endpoint: ./var/storage/pebble
    status: active

# 路由：crypto/binance_spot_kline 的所有 subject 都落到 local 节点
primary_store_routes:
  - space_id: crypto
    route_id: route-binance-spot-kline
    dataset_id: binance_spot_kline
    subject_pattern: "*"            # 也可写具体 subject_id，或 glob 模式
    hash_rule: subject_id           # 分片键
    node_id: local
    priority: 100
    status: active
```

> 路由匹配优先级：精确 `subject_id` > 具体 `subject_pattern`（glob）> `*` 通配；同级按 `priority` 升序选择。

### 2. 用 CLI 二进制导入

种子文件**不应手工写库**，而是通过 CLI 导入（内部走元数据控制面，按依赖顺序 Upsert，可重复执行、幂等）：

```bash
# 一条命令完成：确保 schema 存在 + 导入 metadata.seed.yaml
./bin/moox-storage -import-metadata \
  -conf=config/trpc_go.yaml \
  -storage-conf=config/storage.yaml

# 指定自定义种子文件（默认取业务配置同目录下的 metadata.seed.yaml）
./bin/moox-storage -import-metadata -storage-conf=config/storage.yaml -seed=/path/to/my.seed.yaml
```

导入成功会打印各类实体数量，例如：

```text
metadata seed 导入完成 (config/metadata.seed.yaml): spaces=1 data_sources=1 subjects=2 ... primary_store_routes=2
```

种子文件路径解析顺序：`-seed=<file>` → 环境变量 `STORAGE_SEED_FILE` → 业务配置同目录下的 `metadata.seed.yaml` → `config/metadata.seed.yaml`。

> 若只想初始化空 schema 而不导入数据，用 `./bin/moox-storage -init-metadata ...`。`-import-metadata` 已隐含 schema 初始化。

## 部署

### 单进程开发/测试部署

单进程开发/测试模式下，显式启用 `access + primary + deriver`，`primary.service_name` 留空，`eventbus.type` 设为 `memory`，`deriver.access_service_name` 留空。这样所有服务、派生消费者和计时器都跑在一个进程里，且不需要本地 NATS。

仓库自带的 `config/storage.yaml` 使用 NATS，适合默认/分布式运行；如果本机没有 NATS，请新建一份本地配置，例如：

```bash
cat > config/storage.local.yaml <<'YAML'
storage:
  root: ./var/storage
  roles: [access, primary, deriver]
  metadata:
    path: ./var/storage/metadata/storage_metadata.db
  devices:
    pebble_path: ./var/storage/pebble
    duckdb_path: ./var/storage/duckdb/views.duckdb
    bleve_path: ./var/storage/bleve
    parquet_path: ./var/storage/archive
  primary:
    service_name: ""
  eventbus:
    type: memory
  deriver:
    access_service_name: ""
    batch_size: 100
    batch_wait_ms: 50
    max_workers: 1
YAML
```

然后用这份本地配置启动：

```bash
# 1. 初始化 schema 并导入元数据
./bin/moox-storage -import-metadata -conf=config/trpc_go.yaml -storage-conf=config/storage.local.yaml

# 2. 启动服务
./bin/moox-storage -conf=config/trpc_go.yaml -storage-conf=config/storage.local.yaml
```

`metadata.seed.yaml` 中 PrimaryStoreNode 仍应使用 `endpoint: local`。

`make release` 产物自带 `start.sh` / `stop.sh`，会自动执行 schema 初始化并以 nohup 后台启动。

### 分布式部署

把在线主存（Pebble 分片）与 Access/Query/物化等角色拆到不同机器。两点前提：

1. **事件总线必须用 NATS**（`eventbus.type: nats`，配 `nats_url`），否则行变更事件无法跨进程传播到派生存储（全文索引、视图）。
2. **主存走远程**：Access 节点的 `storage.primary.service_name` 必须非空，并在元数据里把 PrimaryStoreNode 的 `endpoint` 指向真实主存地址。

#### 角色拆分示例（2 层）

**主存节点（可多台，承载 Pebble 分片）** —— `storage.yaml`：

```yaml
storage:
  roles:
    - primary
  primary:
    service_name: ""        # 本机就是主存，内嵌 Pebble
  eventbus:
    type: nats
    nats_url: nats://10.0.0.9:4222
```

只需对外暴露 `PrimaryStoreService`（`config/trpc_go.yaml` 里该 service 的 `network` 设为 `tcp`、`ip` 设为可被访问的地址）。这些节点上可关闭 view/archive 计时器。

**Access 节点（Metadata/Data/Query + 物化/归档）** —— `storage.yaml`：

```yaml
storage:
  roles:
    - access
    - deriver
  primary:
    service_name: trpc.storage.store.PrimaryStoreService   # 走远程主存
  eventbus:
    type: nats
    nats_url: nats://10.0.0.9:4222
  deriver:
    access_service_name: trpc.storage.access.AccessService
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

并在元数据种子里把每个分片节点写成真实地址、用路由把 subject 分到不同节点：

```yaml
primary_store_nodes:
  - node_id: shard-a
    endpoint: ip://10.0.0.11:18101    # 主存节点 A 的 PrimaryStoreService 地址
    status: active
  - node_id: shard-b
    endpoint: ip://10.0.0.12:18101    # 主存节点 B
    status: active

devices:
  - device_id: pebble-a
    node_id: shard-a
    engine: pebble
    status: active
  - device_id: pebble-b
    node_id: shard-b
    engine: pebble
    status: active

primary_store_routes:
  - space_id: crypto
    route_id: route-shard-a
    dataset_id: binance_spot_kline
    subject_pattern: "A*"      # A 开头的标的 -> 节点 A
    hash_rule: subject_id
    node_id: shard-a
    priority: 100
    status: active
  - space_id: crypto
    route_id: route-shard-b
    dataset_id: binance_spot_kline
    subject_pattern: "*"       # 其余 -> 节点 B
    hash_rule: subject_id
    node_id: shard-b
    priority: 200
    status: active
```

> PrimaryStoreNode `endpoint` 支持 `ip://host:port`、`host:port`（自动补 `ip://`）或 tRPC 服务名（走服务发现）；`local`/留空表示同进程。

#### 服务的分散运行

`moox-storage` 二进制只注册 `storage.roles` 启用的服务。常见拆分：

- **物化/归档节点**：让该节点的 `view.timer` / `archive.timer` 在 `trpc_go.yaml` 中启用（去掉 `?disable=1`），其它节点把这些计时器 `disable`，避免重复物化。
- **派生节点**：启用 `deriver` 角色，消费 NATS 行变更事件并写 DuckDB/Bleve；多副本时用独立 durable consumer 名或隔离结果目录。
- 元数据 SQLite 是控制面单点，建议集中在 Access/控制节点；各数据面节点通过元数据快照缓存（默认 10s 刷新）读取路由与列契约。

### 启动验证

无论单机还是分布式，按以下步骤确认正确启动：

1. **进程/端口**：服务日志出现 tRPC 启动信息，且端口在监听：

```bash
lsof -i :18001 -i :18101 -i :18201 -i :18202   # 各服务端口
curl -s http://127.0.0.1:9000/cmds             # admin 管理端口，返回命令列表即存活
```

2. **元数据已导入**：重跑一次导入命令，数量应与种子文件一致（幂等校验）：

```bash
./bin/moox-storage -import-metadata -storage-conf=config/storage.yaml
# -> spaces=1 ... primary_store_routes=2
```

   或经 HTTP 调 MetadataService（端口 19101）确认能列出 Space：

```bash
curl -s -XPOST http://127.0.0.1:19101/trpc.storage.metadata.MetadataService/ListSpaces \
  -H 'Content-Type: application/json' -d '{}'
```

3. **读写链路**：写一行再读回，或直接跑端到端测试（最稳妥）：

```bash
make e2e   # 本地拉起整套服务并用 K 线数据驱动写入/读取/搜索/视图
```

4. **分布式额外检查**：Access 节点日志无"primary store"连接错误；主存节点 `PrimaryStoreService` 端口可被 Access 节点 `telnet`/`nc` 通；NATS 上能看到 `moox.storage.time_series.rows_changed.v1` / `moox.storage.record.rows_changed.v1` 主题有消息。

## 提供的接口

均为 tRPC 服务（同时提供 HTTP 端口），协议定义见 `proto/`，生成代码见 `proto/gen/`。

### MetadataService — 元数据控制面（端口 18001 / HTTP 19101）

对以下实体提供 Create/Update/Get/List（部分为 Upsert）：
Space、View（+ViewColumn）、DataSource、Subject（+SubjectSymbol）、Dataset（+DatasetSubject 绑定）、Field、Factor、DatasetColumn、PrimaryStoreNode、Device、PrimaryStoreRoute、ArchiveFile。

> CLI `-import-metadata` 即是对这些接口的批量封装。

### AccessService — 事实数据读写（端口 18201 / HTTP 19104）

| RPC | 说明 |
| --- | --- |
| `WriteTimeSeriesRows` / `ReadTimeSeriesRows` | 写入/读取固定 `subject_id + freq` 下按 `data_time` 演进的时序数据 |
| `WriteRecordRows` / `ReadRecordRows` | 写入/读取记录数据，按 `record_id + version` 定位 |

### ViewService — 用户侧查询（端口 18202 / HTTP 19105）

| RPC | 说明 |
| --- | --- |
| `QueryTimeSeriesRows` | 查询 TimeSeries + DuckDB 派生 View；不存在返回 `VIEW_NOT_FOUND` |
| `SearchRecordRows` | 搜索 Record + Bleve 派生 View，支持全文 + 结构化过滤 |
| `RebuildTimeSeriesView` | 异步重建 TimeSeries 派生 View，返回 `rebuild_id` |
| `RebuildRecordView` | 异步重建全文索引，返回 `rebuild_id` |

### PrimaryStoreService — 在线主存（端口 18101，内部服务）

`WritePrimaryRows` / `ReadPrimaryRows`，通常由 Access 内部调用；仅在主存独立部署时对外。

### 常见返回错误码

`SUCCESS` / `INVALID_PARAM` / `ROUTE_NOT_FOUND` / `SPACE_NOT_FOUND` / `DATASET_NOT_FOUND` / `SUBJECT_NOT_FOUND` / `VIEW_NOT_FOUND` / `ENGINE_CAPABILITY_UNSUPPORTED` / `INNER_ERR`。

## 测试

```bash
make test    # 单元测试（CGO 开启）
make e2e     # 端到端测试：本地部署整套服务并用 K 线数据驱动各接口
```

端到端测试详见 [`tests/README.md`](tests/README.md)。

## 目录结构

```text
cmd/moox-storage/   服务入口（含 -init-metadata / -import-metadata）
config/             部署配置 + 元数据种子文件
schema/             元数据 SQL 表定义
proto/              协议定义与生成代码
internal/
  bootstrap/        启动期装配（schema 初始化、seed 导入、eventbus 工厂）
  config/           运行配置加载
  core/             领域抽象（eventbus/metadata/router/schema/factvalue/response）
  infra/            底层实现（device/metadata/eventbus/transport）
  services/         access / primary / deriver / search / view / archive
tests/e2e/          端到端测试
docs/               架构与设计文档
```
