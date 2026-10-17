# Storage 读接口收敛与数据归档执行计划

状态：执行计划 v3（2026-10-17），尚未编码。v2：读接口收敛设计 v4（`ReadRows`、DataView 清理与改名）、归档设计 v8（按区间读取），新增发布 C（Storage 去业务耦合）。v3（吸收 Codex 第二轮评审）：对齐收敛设计 v5、归档设计 v9、去耦合设计 v2；发布 A 加入 Strategy 与方法改名；删除归档中 N 的残留；发布 C 改为有状态切换。

依据（下称「设计」）：

- [Storage 读接口收敛设计](Storage读接口收敛设计.md) v4（下称「收敛设计」）；
- [数据归档设计](数据归档设计.md) v8（下称「归档设计」）；
- [Storage 去业务耦合设计](Storage去业务耦合设计.md) v1（下称「去耦合设计」）；
- [Storage 读写路径审查](../审查/2026-10-16-Storage读写路径审查.md) S-1～S-12（下称「审查」）。

设计中已经说明的理由不再重复，本文只写：做什么、改哪里、怎么验收、出问题怎么退。

## 0. 约定

- **分支**：在 `feature/mooyang` 上按阶段提交，每个阶段至少一个独立提交，提交信息写明阶段号（如「阶段 1」），中文。
  Collector 计划时间线的修复（`59e076e6`）已在分支上，阶段 4 的 Collector 改动基于它进行。
- **兼容**：项目未上线，不考虑兼容。协议字段直接删除，不保留 `reserved`；接口、表、配置按设计重写；旧实现直接删除。
- **构建**：
  - 先把 Go 1.26.9 工具链放到 PATH 最前面（`/Users/mooyang/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin`），
    构建与 `go test` 时去掉 `HTTP_PROXY` / `HTTPS_PROXY`；`git push` 保留代理；
  - 纯 Go 组件用 `TARGET_GOOS=linux TARGET_GOARCH=amd64 scripts/build/build.sh <目标>` 交叉编译；
  - Storage 三个角色和 archive-worker 依赖 DuckDB（CGO），Linux 版用 `scripts/build/build-storage-linux.sh` 在编译机远程构建。
- **协议生成**：`make -C modules/storage/proto`；新增的 `modules/archive/proto` 同样用 `trpc-open`（参照 `modules/collector/proto/Makefile`）。
  生成文件随源文件一起提交。
- **测试**：
  - 每个阶段跑涉及模块的 `go test -count=1 ./...`，并发相关的包加 `-race`；
  - 改动 `modules/*/schema/*.sql` 时，按 AGENTS.md 的格式规范逐个载入空 SQLite，并执行 `git diff --check`；
  - 前端单测和构建在编译机上跑（本机 Node 版本过低），流程见阶段 8。
- **审查**：每个阶段编码完成后，用 Codex 只读审查本阶段的改动（只报实质问题），逐条核实后再修，修完补测试。
- **生产**：发布之前对生产只做只读查询。发布按既有流程执行：备份到 `backups/<名称>/old/` → 替换二进制或配置 → `restart.sh -components <组件>` →
  用 `status.sh` 核对 ready 与 `binary_sha256`。
- **文档**：每个阶段完成时同步更新涉及的模块文档；发布后在本文第 15 节记录与计划的差异。

## 1. 阶段总览

| 阶段 | 内容 | 依赖 | 发布 |
| --- | --- | --- | --- |
| 1 | Storage 协议与读路径：统一读事实、游标、不指定标的、属性、计数、序列索引区间读、`ReadRows`、DataView 清理与改名、S-1、S-2，删除分流、`ListHistorySubjects` 与 `ReadRecordRows` | 无 | 发布 A |
| 2 | Storage 资源：S-3 元数据快照、S-4 Outbox、S-8 过期清理 | 无 | 发布 A |
| 3 | 网关客户端：S-9 配置 gzip 后总是压缩 | 无 | 发布 A |
| 4 | 调用方迁移与清理：View 重建、因子、Collector、Monitor、Strategy、CLI；删除旧归档实现；服务目录与 ACL 改名 | 1 | 发布 A |
| 5 | 发布 A 与线上验收 | 1～4 | — |
| 6 | archive-mgr：表、协议、执行计划与全局触发器、运行与租约、接口、维护；服务目录、Access 白名单、部署单元 | 4 | 发布 B |
| 7 | archive-worker：按区间读取、DuckDB 暂存、导出与定稿、节奏与并发、维护、CLI、部署脚本 | 5、6 | 发布 B |
| 8 | 前端：数据归档页 | 6 | 发布 B |
| 9 | Monitor 告警与文档 | 6、7 | 发布 B |
| 10 | 发布 B 与线上验收 | 5～9 | — |
| 11 | Storage 去业务耦合：写入方与元数据权限、幂等写入、周期完成、就绪事件、数据集列来源、View 资源与投递、元数据校验、保留策略包 | 5 | 发布 C |
| 12 | 上层模块随去耦合迁移：factor-mgr、因子引擎、Collector、Strategy、Monitor、归档、`packages/events`、`packages/factorpb` | 11 | 发布 C |
| 13 | 发布 C 与线上验收 | 11、12 | — |

阶段 1、2、3 可以并行开发；阶段 4 依赖阶段 1 的协议；阶段 6、8 可以与阶段 4、5 并行；阶段 7 要用到发布 A 之后的线上 Storage 做联调。
发布 C 与发布 B 互不依赖，可以在发布 A 之后任选先后；两者都改服务目录，各自做一次协调发布。

## 2. 阶段 1：Storage 协议与读路径

对应收敛设计第 2 节、审查 S-1、S-2、S-7、S-10、S-11、S-13。

### 2.1 协议（`modules/storage/proto`）

| 文件 | 改动 |
| --- | --- |
| `rows.proto` | `ReadTimeSeriesRowsReq`：删除 `keys`、`order`、`after_key`；新增 `attribute_keys`（`repeated string`）；`latest_per_series` 的注释改为新语义（可不指定标的、按页返回）。`page` 只用 `size` 与 `cursor` |
| `rows.proto` | `ReadTimeSeriesRowsRsp`：删除 `served_indexed_from`、`served_indexed_to`、`complete` |
| `rows.proto` | `TimeSeriesRow.attributes` 改为 `map<string, TypedValue>`（DataView 的查询响应复用这个消息，一起变化） |
| `rows.proto` | 删除 `ListHistorySubjectsReq` / `ListHistorySubjectsRsp`、`ReadRecordRowsReq` / `ReadRecordRowsRsp` |
| `primary_store.proto` | 删除 `rpc ListHistorySubjects`、`rpc ReadRecordRows`；`rpc ReadFields` 改名为 `ReadRows`（请求、响应消息同步改名），响应删除 `existing_keys` |
| `data_node.proto` | 删除 `rpc ListHistorySubjects`；`rpc ReadFields` 改名为 `ReadRows`，响应删除 `existing_keys`；新增 `rpc CountTimeSeriesRows`（Primary 转给 DataNode）；新增内部消息 `RowsCursor`（区间读取：最后一行的键；最近 N 根：标的、频率、序列、续读时间、该序列已返回根数），只用于编码游标，不出现在公开请求中 |
| `view.proto` | `QueryTimeSeriesRowsReq`：删除 `rows_per_series`、`expected_input_contract_version`；`expected_active_index_id/revision` 合为 `pin`（新消息 `IndexVersion { index_id, revision }`）。`QueryTimeSeriesRowsRsp`：`served_indexed_from/to` 改名为 `min_data_time` / `max_data_time`（`string`，两端包含，不复用 `TimeRange`），`served_active_index_id/revision` 合为 `index`（`IndexVersion`），`served_series_bars` 改为 `retained_rows_per_series`，删除 `served_input_contract_version`；`SearchRecordRowsRsp` 的覆盖字段同样改为 `min_data_time` / `max_data_time` |
| 配置 | `[storage_retention] view_bars` 改名为 `view_rows_per_series`：`packages/storagepolicy`、`moox.toml.example`、CLI 的策略渲染与 Storage 配置加载同步 |

生成：`make -C modules/storage/proto`，提交 `storagegen` 的变化。

### 2.2 PrimaryStore（`internal/service/primarystore`）

- `rows_read.go`：
  - `ReadTimeSeriesRows` 全部转给 DataNode；精确读取由 `ReadRows`（原 `ReadFields`）负责，只返回存在的行；
    删除 `storage-view` 和因子身份的分流、`isFactorHistoryRead` / `validateFactorHistoryRead`、转给 View 的 `readTimeSeriesView`；
  - 新增统一校验 `validateTimeSeriesRead`：`column_names` 必填；`time_range` / `latest_per_series` 二选一；selector 0–100 个，
    每个带 `subject_id` 与等于数据集频率的 `freq`；区间必须有起止；区间页大小 1–2000、最近 N 根页大小 1–20000；N 为 1–10000；拒绝页码；
  - `CountTimeSeriesRows` 改为转给 DataNode，不再依赖 View；
  - 删除 `ListHistorySubjects`、`ReadRecordRows`（含转给 View 的记录读取）；`readHistoricalTimeSeriesRows` 改名为 `readDataNodeRows`。
- `service.go`：删除 `isFactorAppID` 中只为读取分流存在的用法（写入归属校验仍用它，保留）。
- PrimaryStore 的 View 解析器（`Options.View`）在删除范围读取、计数与 `ReadRecordRows` 的 View 路径后如果没有其他用途，一并删除，`cmd/server/main.go` 同步去掉注入。

### 2.3 DataNode（`internal/service/datanode`、`pebble/`）

- 文件与命名：`pebble/history.go` 改名为 `pebble/rows_read.go`；内部标识符去掉 history 说法：时间索引（`0x03`）、序列索引（`0x04`），
  例如 `historySelector` → `seriesSelector`、`pageHistory` → `readTimeRange`；物理键布局与命名空间字节不变。
- **区间读取**：
  - 不带 selector：沿用时间索引顺序扫描；
  - 带 selector（S-10）：对每个序列在序列索引上定位到 `max(区间起点, 游标位置)`，用最小堆按 `data_time, subject_id, series_tag` 归并输出，
    凑满一页即停，游标为最后一行的键。指定标的但不指定 `series_tag` 时，先列出该标的的序列前缀再逐个定位；
- **最近 N 根**：不带 selector 时从游标位置开始在序列索引上逐个标的、逐个序列读取；带 selector 时只读所选标的。
  一页不超过 `page.size` 行，也不超过 20 万个值（行数 × (列数 + 属性数)），先到为止；同一标的可以跨页，续读位置写入 `RowsCursor`；
- **计数**：在序列索引上按标的扫描键，同一时间点多个 `series_tag` 只计一次（同一标的的各序列按时间归并去重），不读值；
- **字段与属性补全（S-1、S-2、S-11）**：
  - `ReadFieldsWithPresence` 与 `ReadFields` 合并重写为 `ReadRows`：整个请求用一个快照、一个迭代器；键按物理编码排序后逐行 `SeekGE` 行前缀，
    顺序 `Next` 取字段并得到存在性；请求了 `attribute_keys` 或这一行没有字段时，再定位属性命名空间；只返回存在的行，按请求顺序；
  - 分块大小改为 `min(10000, 100000 / (字段数 + 属性数))`；
  - 组装响应时带上有类型的属性；
- **删除**：`ListHistorySubjects`、`ensureHistoryIndex`、`backfillHistoryIndex`、`backfillHistoryIndexSync`、`historyMaterializedMarker` 与
  `historyBackfillMu`（索引在 10-04、10-08 引入，线上 10-14 清空后重新积累，现存数据都带索引）；
- `data_time` 统一用 `time.RFC3339Nano` 输出（无小数秒时不带小数）；
- `datanode/service.go`：对应删除与新增 RPC 处理。

### 2.4 View（`internal/service/view`、`viewindex`）

- `view/query.go`：行属性不再转成字符串（删除 `rowAttributesToStrings`），直接返回有类型的属性；响应字段按 2.1 改名。
- 保留行数（收敛设计 2.4）：DuckDB 索引的 `view_meta` 增加 `rows_per_series`，建立索引时写入本次使用的保留行数；打开旧索引时按现有方式
  （`index_manager.go` 中给 `view_meta` 补列的逻辑）补列，默认 0 表示未知。`retained_rows_per_series` 返回 `min(索引的保留行数, 当前配置)`，
  索引为 0 时返回 0；删除 `SetSeriesBars` / `servedSeriesBars` 中直接返回配置的做法。不改元数据库的 schema。
- 删除 `view/series_window_query.go`（`rows_per_series` 模式）及其测试、`viewindex` 中只为它服务的部分。
- `view/event_apply.go`：改调 `ReadRows`，按「返回即存在」判断行是否存在，删除对 `existing_keys` 的处理。
- View 重建的 Primary 读取点（`build.go`、`primary_retry.go`、`maintenance.go`）改用统一接口，逐个点核对：
  - 按标的的最近 N 根：改为不带 selector、带列和 `attribute_keys`（`factorAttributeKeys` 的结果）的最近 N 根，按 `next_cursor` 逐页读取，
    一次拿到值和属性；删除 `listBackfillSubjects` 中仅为 `ListHistorySubjects` 服务的部分、`enrichBackfillRows` 的补字段步骤、
    `marshalTimeSeriesHistoryCursor`；
  - `build.go` 中以倒序区间加 `after_key` 读取 Primary 的路径：改为最近 N 根或升序区间加游标（按该路径的实际用途选择，实施时确认）；
  - 单个标的读取超时后跳过该标的的现有行为，改为按页：一页超时则整次重建按现有失败处理重试，不再静默跳过标的。

### 2.5 测试

- PrimaryStore：任何身份的区间读取、最近 N 根、计数都到达 DataNode；不合规请求被拒绝（无列、区间缺起止、页大小超限、
  selector 超过 100、N 超过 10000、多种形态同时出现、使用页码）；`ReadRecordRows`、`ListHistorySubjects` 不再注册。
- DataNode：
  - 区间读取用游标逐页读完，与一次性读取结果一致，无重复、无遗漏；带 selector（序列索引归并）与不带 selector 再过滤的结果逐行一致，
    包括多个 `series_tag`、区间边界、游标恰好落在页尾；
  - 最近 N 根逐页读完覆盖全部有数据的标的；多个 `series_tag` 每个序列各取 N 根；一个标的跨页时无重复、无遗漏；页大小与值数量上限都生效；
  - 计数与「按行读取后去重计数」一致；
  - 读取 1 万行以上、只请求少量列时成功（S-1）；
  - 前缀扫描与原逐字段点查结果逐行一致，包括只有属性没有字段的行、记录型数据集取最大版本的行（S-2）；属性类型不丢失（S-11）；
- View：重建出的索引与改造前逐行一致（同一份 Pebble 数据分别用新旧代码重建后比较）；因子来源属性仍在。
  - 覆盖范围：只有一行的索引 `min_data_time = max_data_time` 且查询该行 `complete = true`；查询最后一根；A 股日线的 `max_data_time` 为上海零点；
  - 保留行数：调大配置、索引未重建时返回旧值，重建切换后返回新值；调小配置立即返回新值；旧索引补列后返回 0。
- benchmark（`pebble` 包）：构造 500 个标的 × 3000 根 × 8 列的数据，比较改造前后「最近 N 根（不指定标的）」「50 个标的的一天区间」
  「单标的窗口」三种读取的耗时与分配。

### 2.6 验收

- `go test -count=1 ./modules/storage/...` 全部通过；benchmark 结果记入第 15 节。
- 本阶段不单独发布，随发布 A 一起上线。

## 3. 阶段 2：Storage 资源

对应审查 S-3、S-4、S-8。

### 3.1 元数据快照（S-3）

- `metadata/cache/store.go`：
  - 新增「当前代」：原子指针指向不可变的一代，内含条目按 `(kind, space, id)` 与 `(space, dataset)` 建好的索引、解码后的数据集 / DataNode / 列，
    以及每个数据集「列名 → 列」的映射；
  - `RequestSnapshot()` 读 `cache.Status().ContentHash`，与当前代相同就返回同一个对象；不同则构建一次新一代并替换，并发请求只构建一次；
  - 删除 `Snapshot()` 的深拷贝与 `MetadataSnapshot` 的线性扫描实现。
- `primarystore/metadata_validator.go`：`validateRow` 直接用当前代里的列映射，删除逐行分页读取列清单。
- 只读约束：逐个检查 `RequestSnapshot` 的调用点（`dataset_admin.go`、`service.go`、`factor_rows.go`、`marker.go`、`period.go`、
  `metadata_validator.go`、`cmd/server/main.go` 的 DataNode 解析），确认不修改返回对象；需要修改的地方先 `proto.Clone`。
- 测试：元数据未变化时连续请求拿到同一代；变化后拿到新一代；校验结果与改造前一致；`-race` 下并发读写通过。
- benchmark：按线上规模（5000 个标的、标签成员、因子数据集数百列）构造元数据，比较单次写入请求（500 行）的耗时与分配。

### 3.2 Outbox 中继（S-4）

- `pebble/store.go` 的 `PublishOutboxPublication`：用 `ListOutbox` 读到的数据确定数据集范围；加锁后只做一次 `Get` 比较字节，确认这一条仍在；
  删除两次重复的 `PrepareOutboxPublication` 完整校验（写入时已校验）。
- `outbox/relay.go`：把列表读到的数据传给发布函数。
- 测试：现有 Outbox 发布、清除、隔离无效事件的用例全部通过；新增「发布前该条已被清除」的用例。

### 3.3 过期清理（S-8）

- `pebble/cleanup.go`：
  - 由 `datasetWriteMu` 独占锁改为数据集级互斥（同一数据集的写入与清理互斥，不同数据集互不影响）；
  - 每个数据集记住上次清理到的截止桶，截止桶没有推进时直接返回；
- `pebble/store.go` 的 `compactAsync`：改为单个后台 worker 串行执行 compaction 请求，相同范围排队时合并。
- 测试：清理期间其他数据集的写入不被阻塞（并发用例）；截止桶未推进时不发起删除与 compaction；清理与同一数据集写入的互斥仍然成立。

### 3.4 验收

- `go test -count=1 -race ./modules/storage/...` 通过；benchmark 结果记入第 15 节。随发布 A 一起上线。

## 4. 阶段 3：网关客户端压缩

对应审查 S-9。

- `packages/gatewayclient/trpc.go`：配置 `compression: gzip` 后总是设置 gzip，删除 `minCompressBytes`。
- 测试：`trpc_test.go` 增加「小请求、大响应」用例，确认响应在链路上被压缩；Access 与主机网关的解压验签用例不变。
- 影响：所有配置了 gzip 的调用方随各自重新构建生效，随发布 A 上线。

## 5. 阶段 4：调用方迁移与清理

### 5.1 调用方

| 调用方 | 文件 | 改动 |
| --- | --- | --- |
| 因子引擎、factor-mgr | `modules/factor/internal/storageio/read.go` 等 | 区间读取改用 `page.cursor` / `next_cursor`，删除自行编码 `after_key`；预热与补算的读取一起改 |
| Collector | `modules/collector/internal/klinecheck/check.go`、`history.go`，`storageio/gateway.go` | 读行改用游标翻页；计数接口不变（改由 DataNode 回答）；历史扫描在 `59e076e6` 之后的代码上验证 |
| Monitor 指标历史 | `modules/monitor/internal/metrics/query.go` 的 `QueryService.History`、`storage.go` 的 `QueryHistorySelectors` | 无生产调用方，删除这条读取链以及只为它存在的类型、依赖和测试（S-12） |
| Monitor 主机指标 | `modules/monitor/internal/hostmetrics/storage_reader.go` | 改为区间升序读取、游标顺序翻页，在内存中保留区间内最后 `limit` 条，返回顺序与现在一致 |
| CLI | `modules/cli/internal/command/data.go`、`data_kline.go`、`data_remote.go` 及测试 | 删除对 `complete` 的判断；游标翻页；导出达到 `--page-size` 上限时停止并提示 |
| 主机网关 e2e 辅助程序 | `modules/hostgateway/cmd/e2e-helper/main.go` | 按新请求形态调整 |
| `ReadFields` 的调用方 | Collector `resample/storage.go`、`marketstorage/storage.go`；Monitor `storagegateway/client.go`、`watchdog/market_canary.go`（原 `keys` 形态）；CLI `setup_storage.go` | 改调 `ReadRows`；按键匹配结果，不依赖位置；不再读 `existing_keys` |
| Strategy | `modules/strategy/internal/input/client.go`、`depth.go`、`resolve.go`、`range.go` | DataView 字段改名：`min_data_time` / `max_data_time`（含义不变，最后行键的判断不变）、`index`、`retained_rows_per_series`（为 0 时跳过「永远无法满足」的预检，与现在一致）；多页读取用 `pin` |
| 前端 | `web/src/api/storage/types.ts` 及使用行属性、DataView 覆盖字段的页面（如 `views/factor/results/results-model.ts`） | 行属性改为有类型的值，展示时按类型转成文本；覆盖字段改名；放在阶段 8 一起构建 |

### 5.2 删除旧归档实现

旧归档读取 `complete` 等已删除字段，无法通过编译，在本阶段一并删除：

- `modules/archive` 下的全部旧代码（journal、eventconsumer、parquetio、cosstore、registry、partitionlock、writer、backfill、旧的 `cmd/server`、`cmd/cli`、
  配置与测试）；`scripts/build/build.sh` 中的 `archive` 目标暂时移除，阶段 6 换成新目标；
- Storage：`metadata.proto` 的 `RegisterArchiveFile` / `ListArchiveFiles`，`metadata` 服务、`cache/store.go`、`sqlite/crud_store.go`、
  `catalog/metadata_infra.go` 中的实现，`schema/metadata.sql` 的 `t_archive_files` 表与索引、触发器（同步核对 `metadata_schema_version_test`）；
  schema 版本号不变（只删表，不改其他表），线上库里留下的这张空表不再被引用，下次重置元数据库时自然消失；
- Admin：`console/policy.go` 的 `storageGlobalMethods` 去掉两个归档方法；`archive-eventbus` 凭据角色从 `eventbus-credentials` 中删除；
- 部署与安装：`modules/cli/internal/setup` 中对旧 `archive` 组件的引用（`unitbootstrap/request.go`、`unitinstall/eventbus.go`、`unitinstall/prepare.go`、
  `unitpackage/package.go`、`command/setup_storage.go`），`setup/deploy/deploy.go` 的 `--no-archive`，`scripts/deploy/deploy-moox.sh` 及测试脚本中的引用；
- 文档：`skills/moox/references/eventbus-credentials.md` 等提到 `archive-eventbus` 的地方。

### 5.3 服务目录（`packages/servicecatalog/catalog.yaml`）

- PrimaryStore：`ReadFields` 改名为 `ReadRows`，删除 `ListHistorySubjects`、`ReadRecordRows`；`methods`、`read_only_methods` 和每一条 `acl`
  （moox-cli、collector、factor 等内部调用方，以及外部身份白名单）都要改；
- 按字符串调用方法名的客户端同步改：Monitor `storagegateway/client.go`、Collector `storageio/gateway.go`、CLI `setup_storage.go`；
- Metadata：删除两个归档方法；
- 删除组件 `archive`；
- `catalog_test.go`、`packages/gatewayroute` 的相关测试同步调整。

### 5.4 文档

更新 `docs/模块/存储.md`（第 6.1 节重建方式、第 7 节查询）、`外部接入.md`、`采集.md`、`监控.md`、`因子.md`、`命令行工具.md`、`事件总线.md`、
`部署与运维.md`，以及 `modules/README.md` 中归档一行（改为「建设中」，阶段 9 再改成定稿描述）。

### 5.5 验收

- 全仓 `go build ./...` 与各模块 `go test -count=1 ./...` 通过；`rg 'ListHistorySubjects|ReadRecordRows|"ReadFields"|- ReadFields|after_key|AfterKey|GetComplete|ServedIndexed|ServedSeriesBars|archive-eventbus|RegisterArchiveFile'`
  在代码与服务目录中没有残留（生成代码以外）。

## 6. 阶段 5：发布 A 与线上验收

### 6.1 构建

同一个提交构建：Storage 三个角色（CGO，`build-storage-linux.sh`）、`moox-storage-cli`、Admin、Monitor、host-gateway、Access、Collector、
factor-mgr、Strategy、因子引擎、`moox-cli`。Strategy 依赖 DataView 改名后的响应，旧二进制不能继续用。前端（行属性类型变化）随阶段 8 的前端一起构建；如果发布 A 先于阶段 8，本次单独构建一次前端和 web-host。

### 6.2 顺序（整点后 :05～:20、分钟中段）

1. storage 主机：storage-node → storage-primary → 至少等 30 秒 → storage-view；
2. control 主机：Admin → control 的 host-gateway → Monitor（服务目录变化，三者紧接着换）→ Collector → factor-mgr → Strategy；
3. 其他主机的 host-gateway 与 Access；
4. 编译机：因子引擎（`systemctl --user restart moox-factor-engine`）；
5. 本机重新构建 `moox-cli`。

### 6.3 验收

- 各组件 `status.sh` 为 ready，`binary_sha256` 与构建一致；Monitor 库 `t_monitor_gateway_observations` 中各主机的 route_count 与 expected/applied hash 一致。
- 因子：发布后的 1m、1h 周期正常产出，运行摘要无连续失败；必要时用 `recalc` 补发布期间失败的几期。
- View：各 View 水位跟上；挑一个小 View 执行一次 `repair-view`，重建成功、行数与重建前一致、因子来源属性仍在。
- Collector：运行状态页正常；对一个数据集执行一次 `CheckKline`。
- `ReadRows`：经 host-gateway 的调用（Collector 重采样或 Monitor 行情巡检）与经 Access 的调用（`moox-cli` 远程读）都成功，网关观测里没有方法未注册的拒绝。
- Strategy：发布后各运行中实例按期触发；一个需要多页读取的实例跨页时 `index` 不变；覆盖与回溯深度判断与发布前一致。
- CLI：`moox-cli data rows export` 导出一段早于 View 覆盖范围、仍在保留期内的数据，返回完整行（发布 A 晚于 10-18 时可以直接验证）。
- 压缩：Access 指标或抓包确认最近 N 根等「小请求、大响应」的调用已压缩。
- 资源：观察 storage-node CPU（因子预热、View 重建期间）与过期清理日志；对比发布前的基线。

### 6.4 回退

所有改动同一提交构建，回退即把发布 A 涉及的组件全部换回备份并按同样顺序重启。没有数据迁移：物理键布局不变，删除的 `t_archive_files` 表线上本来就没有数据。

## 7. 阶段 6：archive-mgr

对应归档设计第 2、3、6.3、9、10、11 节。

### 7.1 目录结构（`modules/archive`）

```text
cmd/mgr/                 moox-archive-mgr
cmd/worker/              moox-archive-worker（阶段 7）
cmd/cli/                 moox-archive-cli
proto/archive.proto      ArchiveMgr、ArchiveWorker；Makefile 与 archivegen
schema/archive.sql       mgr 的 SQLite；schema.go 内嵌、schema_test.go 校验载入与格式
config/mgr/              app.yaml、trpc_go.yaml
config/worker/           app.yaml、trpc_go.yaml（阶段 7）
internal/mgr/
  config/                配置加载与校验
  store/                 任务、运行、分区、worker 的 SQLite 读写
  schedule/              执行计划解析（复用 robfig/cron v1 的 6 段带秒解析）、时区、最小间隔、预览
  trigger/               每秒一次的全局触发器
  runs/                  领取、租约、进度、完成、取消
  service/               ArchiveMgr 接口
  workerapi/             ArchiveWorker 接口
  maintain/              回收过期租约、刷新数据集信息、清理运行记录、incremental vacuum
  health/                任务健康等级、总览
  metrics/               /metrics
```

### 7.2 表与协议

- `schema/archive.sql`：按归档设计第 10 节建表（`t_archive_tasks`、`t_archive_runs`、`t_archive_partitions`、`t_archive_workers`），
  库以 `auto_vacuum = INCREMENTAL` 创建。
- `proto/archive.proto`：`ArchiveMgr` 的 11 个方法、`ArchiveWorker` 的 4 个方法（归档设计第 11 节）。下发给 worker 的运行参数包含：
  数据集、频率、列（发布 B 只有字段列）、执行间隔、最少回看时长、空间时区与本地收盘时刻、暂存保留期、补拉区间、回补起点。
  读取水位 `W` 与分区状态由 worker 在暂存库中维护，不下发。

### 7.3 关键逻辑

- **执行计划**：保存时解析 cron 与时区，计算相邻计划时刻的最小间隔（展开未来 14 天的计划时刻取最小差值），小于 300 秒拒绝；
  `PreviewArchiveSchedule` 返回接下来 5 次时刻、执行间隔、每次读取区间的长度（执行间隔 + L + 一根 K 线的最大跨度）、分区预计导出时间、落在避让区间的提示。
- **全局触发器**：`trpc.moox.archive.mgr.trigger.timer`（`* * * * * *`，超时 900ms，`timerjob` 防重叠）。每次查询到点的启用任务，
  逐个在事务内：有排队中或运行中的运行则累加连续跳过次数，否则生成 `scheduled` 运行并清零；把 `c_next_run_at` 推进到当前时刻之后的下一个计划时刻。
- **立即开始**：保存（或重新启用）的同一事务内生成 `manual` 运行。
- **领取与租约**：worker 租约（心跳 10 秒，45 秒无心跳可被接管，同 `worker_id` 的第二个实例返回冲突）；`PullArchiveRun` 在事务内
  按计划时刻选出不超过 DataNode 并发上限的运行，写运行租约（10 分钟）；已有分区的任务只派给分区登记的那个 `worker_id`。
- **完成**：一个事务内更新运行、分区登记（open / exported / final 与文件信息）、任务的最近成功时间与定稿边界。
- **维护**：`trpc.moox.archive.mgr.maintain.timer`（`13 * * * * *`）：回收过期运行租约；每小时调用 `Metadata.GetDataset` 刷新保留期与 DataNode；
  按保留期清理运行记录（成功的定时运行 30 天，其余 90 天）；每天一次 `PRAGMA incremental_vacuum`。
- **健康等级**：按归档设计第 13 节计算，列表与总览共用。

### 7.4 接入

- 服务目录：新增组件 `archive-mgr`（scope any、replicas single、端口 11406 / 11408、健康 11416）；
  `ArchiveMgr`（`console_name: archive`，调用方 console、moox-cli；只读方法另外允许 monitor）；`ArchiveWorker`（`acl: []`）；
  外部身份 `archive-worker`（PrimaryStore `ReadTimeSeriesRows`；Metadata `GetDataset`、`ListDatasetColumns`；ArchiveWorker 四个方法）；
  Metadata 的 `GetDataset` 对内部调用方 `archive-mgr` 放行。
- Admin：`console/policy.go` 的 `requiresSpaceHeader` 增加 `archive`；`unitbootstrap` 为内部调用方 `archive-mgr` 生成签名密钥。
- 部署单元：`unitinstall/state.go` 的组件顺序加入 `archive-mgr`（放在 factor-mgr 之后）；`unitpackage`、`unitbootstrap`、`unitruntime` 按 factor-mgr 的方式登记；
  `moox.toml.example` 与 `configinit/moox.toml.example` 的 control 放置加入 `archive-mgr`；`scripts/build/build.sh` 增加 `archive-mgr`、`archive-cli` 目标（CGO 关闭）。

### 7.5 测试

- schema：载入空 SQLite、格式校验、约束（执行间隔下限、补拉区间必须有起止、有分区的任务不能删除）。
- 执行计划：最小间隔计算（不均匀 cron、跨夏令时时区）、预览（读取区间长度、分区预计导出时间按空间时区与本地收盘时刻计算）。
- 触发器：到点生成、撞上未结束运行时累加跳过、错过的多个计划时刻合并、立即开始、停用后不触发。
- 租约：单一活动 worker、45 秒接管、重复实例冲突、运行租约过期回收、取消。
- 领取：DataNode 并发上限、按计划时刻排序、分区归属 worker 不同时不派发。
- 完成：分区状态流转与文件登记、定稿边界、健康等级。
- 接口：各方法的校验与空间作用域。

### 7.6 验收

`go test -count=1 -race ./modules/archive/...` 通过；本机起 mgr（SQLite 临时目录），用 `moox-archive-cli` 或测试客户端走通「建任务 → 触发 → 领取 → 上报完成」。

## 8. 阶段 7：archive-worker

对应归档设计第 4、6、7、8、9 节。

### 8.1 结构

```text
internal/worker/
  config/        配置加载与校验（worker、staging、pacing）
  client/        经 Access 的网关客户端（caller archive-worker）、Storage 角色认证（AppId archive）
  lease/         心跳、worker 租约、领取运行、进度与完成上报
  pacing/        避让窗口（每分钟 25–55 秒、整点 :58～:04）、请求间隔、全局带宽令牌桶、每 DataNode 并发
  reader/        读取区间的计算（[W − L − S, 本次运行开始)）、区间读取（游标翻页）、保留期截断
  staging/       DuckDB 暂存：建表与增列、批量比对写入、task_state / partition_state
  export/        按空间时区切分区、分区收盘时间（packages/frequency 的 BarEnd 与本地收盘时刻）、导出、重写、定稿、已定稿分区读回、_dataset.json、预览
  backfill/      首次回补（按分区推进）
  maintain/      每天的维护：CHECKPOINT、重建 DuckDB 文件、清理已删除任务、临时目录
  disk/          可用空间检查
cmd/cli/         export、verify、reindex、status
```

### 8.2 关键逻辑与对应的设计条款

| 逻辑 | 要点 | 设计 |
| --- | --- | --- |
| 读取区间 | 起点 = `W − L − S`（`S`：1mo 31 天、1w 7 天，其余为频率时长），不需要市场对齐；首次运行取回补起点或「本次开始 − 执行间隔 − L − S」；终点 = 本次运行开始；保留期截止处截断并上报无法补齐 | 归档设计第 4 节第 1 步 |
| 写入暂存 | Appender 写临时表 → 与暂存表按主键比对（`IS DISTINCT FROM`）→ 只写新增和变化的行 → 同一事务更新 `last_changed_at` | 第 8.2 节 |
| 导出与重写 | 运行开始 ≥ 分区收盘时间 + L，且「到时间未导出」或 `last_changed_at > exported_at`；`COPY ... TO '<文件>.partial'` → fsync → rename → fsync 目录 → sha256 | 第 8.3 节 |
| 定稿与水位 | 运行开始 ≥ 分区收盘时间 + L + 暂存保留期，且无待导出变化；删除暂存行与把 `W` 推进到本次运行开始在**同一个 DuckDB 事务**里提交（定时或立即运行；补拉区间不推进） | 第 4 节第 4 步、第 8.3 节 |
| 写入已定稿分区 | 任何一行要写入 `final` 分区时，先用 `read_csv(..., union_by_name = true, allow_quoted_nulls = false)` 把原文件读回暂存表、标回 `open`，再写入 | 第 4 节 |
| 分区时区 | 按空间时区的本地日 / 月切分；有交易时段的空间，日分区的收盘取本地收盘时刻（夏令时由时区库换算） | 第 8.1、8.3 节 |
| 节奏 | 请求只在允许的时间窗发起；全部并发运行共享 512KB/s 令牌桶；同一运行请求间隔 ≥ 200ms；每 DataNode ≤ 2 个运行 | 第 6 节 |
| 磁盘保护 | 可用空间低于 5GB 停止领取，心跳上报 | 第 6.1 节 |

### 8.3 构建与部署脚本

- `scripts/build/build-storage-linux.sh` 增加 `MOOX_LINUX_CGO_TARGET=archive-worker`；`build.sh` 增加本机构建目标（macOS 用）。
- 新增 `scripts/deploy/deploy-archive-worker.sh`，参照 `deploy-factor-engine.sh`：参数 `--dir`、`--root-dir`、`--worker-id`、`--access-address`、
  `--access-id`、`--access-key-id`、`--skip-build`、`--no-start`；检查 `secrets/` 下的 `access-archive-worker.key` 与 `storage-primary-auth.secret`
  为 0600 普通文件；渲染配置；Linux 用 systemd 用户服务、macOS 用 launchd 托管；等待 `/readyz`。安装目录不能放在 `~/Documents` 下。

### 8.4 测试

- 读取区间：评审中的两个场景必须有用例：
  - 5 分钟任务，一次运行因整点避让推迟到 :04，推迟期间补写的行仍在下一次的区间内被读到；
  - worker 停机 4 小时期间新增标的，恢复后的区间覆盖它的早期数据；
  - 另有：每根 K 线最后一次被读到时距收盘至少 L（随机生成计划时刻与推迟、失败，逐根验证）；中途失败后从同一起点重读且不重复写入；
    首次运行有 / 无回补起点；停更标的不再被读到；超出保留期截断；
  - 区间起点 `W − L − S` 覆盖所有收盘晚于 `W − L` 的 K 线：1h、1d（含 A 股日线上海零点、日内运行）、1w、1mo（含 2 月与 31 天的月）的边界。
- 导出与定稿：8 月最后一根周线（08-31 开盘）不会在 09-07 之前定稿；导出前失败留下的变化在下一次运行导出；
  首次回补遇到月分区时，同一个月的多天不会互相覆盖；当前分区不会被提前定稿；
  - 定稿与推进 `W` 的事务失败（注入错误）后，暂存行与 `W` 都没变，重跑后导出的文件与一次成功运行的结果逐字节一致；
  - 晚到的行落进已定稿分区时，先读回原文件再写入，重新导出的文件包含原有的全部行；1mo 区间起点落进上一个已定稿的月时不产生写入、不重新打开分区。
- 分区时区：A 股日线中每月第一个交易日落在当月文件；stockcn 1m 按上海本地日成文件；stockus 跨夏令时切换的那天收盘时刻正确；crypto 仍按 UTC 日。
- CSV：导出 → 读回 → 再导出逐字节一致（含默认序列的 `""`、缺失值、增列后的旧文件）。
- 节奏：用可控时钟验证请求只在允许的时间窗发起；令牌桶在并发运行下不超限。
- 暂存：值没变不写入；增列 `ALTER TABLE`；每天维护的重建在没有运行时执行、执行期间不领取。
- 端到端：用内存实现的 Storage 与 mgr 测试服务跑完整运行，检查文件、分区登记与运行统计。

### 8.5 验收

- `go test -count=1 -race ./modules/archive/...` 通过（DuckDB 用例需要 CGO，本机 macOS 可跑）。
- 在编译机上用测试配置连真实 Access，对一个 crypto 1m 数据集做一次手动运行，写到临时目录，核对文件行数与 DataNode 读取一致；
  这一步只读线上 Storage，不经过 mgr 生产实例（mgr 用本机测试实例）。

## 9. 阶段 8：前端

对应归档设计第 12 节。

- API：新增 `web/src/api/archive/index.ts`（`callControl("archive", ...)`，类型与 `archive.proto` 一致）。
- 页面：`web/src/views/collector/archive/` 下：
  - `index.vue`（两个 Tab：归档任务、运行记录）；
  - `task-dialog.vue`（新建 / 编辑：采集任务选择、执行计划预设与 cron、开始时间、回补起点；预览中显示每次读取区间的长度）；
  - `task-detail-drawer.vue`（汇总、月历、分区详情、运行记录）；
  - `archive-model.ts` 与单测。
- 复用：`views/collector/runtime-status/runtime-status-model.ts` 的 `cronText` 与 `views/data/subjects/tag-form.ts` 的 `nextRuns` 移到
  `web/src/utils/cron.ts` 共用，原调用点改为引用；执行计划的即时预览用 `nextRuns`，保存前以 `PreviewArchiveSchedule` 的结果为准。
- 菜单：`static-menu.ts` 在数据采集下增加「数据归档」（排在采集任务之后）；`route.ts`、`lang/modules/zhCN.ts` / `enUS.ts`、
  `scripts/check-menu-structure.mjs` 同步。
- 行属性类型变化（阶段 4 的前端部分）：`api/storage/types.ts` 与使用行属性的页面。
- 构建与测试（编译机）：`rsync -a --delete web/src/ 192.168.0.103:moox-web/src/`，远端执行 `npx vue-tsc`、`npx vitest run`、
  `npx vite build --mode production`，把 `dist` 拉回 `web/dist`，再 `cd web-host && make statik`，提交 `statik.go`。
- 验收：本机用 vite 预览壳（保留自动导入插件）查看页面布局；单测通过；菜单校验脚本通过。

## 10. 阶段 9：Monitor 告警与文档

- Monitor：
  - 新增 `internal/bootstrap/archive_health.go`：每分钟调用 `ArchiveMgr.GetArchiveOverview` 与 `ListArchiveTasks`，生成外部检查
    `archive:worker`（有到期运行时活动 worker 离线超过 30 分钟）与 `archive:task:<空间>/<数据集>`（任务健康等级）；
  - `default_alerts.go`：任务「异常」为 critical，「需关注」与 worker 离线为 warning；`alerttext` 增加对应文案；
  - 服务目录允许 monitor 调用 ArchiveMgr 的只读方法（阶段 6 已加）。
- 测试：检查生成、等级映射、恢复。
- 文档：重写 `docs/模块/归档.md`；更新 `modules/README.md`、`docs/部署与运维.md`（worker 安装、数据保留表中的归档一行改为 CSV）、
  `docs/模块/外部接入.md`（`archive-worker` 身份）、`管理后台.md`（控制台路径）、`前端.md`、`命令行工具.md`、`监控.md`、`docs/实现状态.md`。

## 11. 阶段 10：发布 B 与线上验收

### 11.1 准备

- 在隔离环境（`moox-e2e` 与 `moox.sandbox.toml`）演练一次：control 单元新增 `archive-mgr` 组件、服务目录更新、Access 校验文件重新导出。
- 编译机：确认归档目录所在磁盘的容量（`df -h`），选定 `root_dir`。
- 凭据：`moox-admin-cli keys export --caller archive-worker` 生成外部身份密钥；`keys export-access` 重新导出 Access 校验文件。

### 11.2 顺序

1. control：Admin → control 的 host-gateway → Monitor（服务目录变化）→ 安装并启动 archive-mgr → web-host（前端）；
2. 其他主机的 host-gateway；所有 Access 实例更新校验文件后重启；
3. 编译机：`scripts/deploy/deploy-archive-worker.sh --dir ~/moox/archive-worker --root-dir <归档目录> ...`；
4. 本机重新构建 `moox-cli`。

### 11.3 验收

- 控制台「数据采集 → 数据归档」可见，概况条显示 worker 在线、归档目录与磁盘信息。
- 建 crypto 现货 1m 每小时任务（立即开始）：
  - 首次运行完成，分区登记与暂存行数正确；
  - Access 日志中归档请求的时间分布：没有落在每分钟第 0–25 秒和整点 :58～:04 的请求；
  - 读取速率不超过 512KB/s，响应已压缩；
  - 过 UTC 0 点后，下一次运行（次日 00:34 UTC）导出前一天的文件；文件行数与 DataNode 读取一致；3 天后定稿。
- 建一个每 5 分钟的任务：计划时刻误差在几秒内；撞上整点避让而推迟的那次运行之后，下一次运行的读取区间仍从推迟那次的开始时间往前「15 分钟 + 一根」起（看运行记录的读取区间）。
- 停 worker 超过一个执行间隔后恢复：恢复后第一次运行的读取区间从停机前最后一次成功运行算起，中间的数据全部读到。
- stockcn 1m 每天任务：17:04 运行，当天文件约 17:25 可用，文件名是上海本地日期；stockcn 1d 任务：每月第一个交易日在当月文件里。
- Monitor：停 worker 30 分钟，`archive:worker` 告警触发；恢复后自动恢复。

### 11.4 回退

归档是新增模块：出问题时先停用任务、停掉 worker，不影响其他模块。需要撤下 archive-mgr 时，服务目录要用回退后的同一提交重新发布
Admin、Monitor、各 host-gateway 与 Access。归档目录与暂存库保留，不删除数据。

## 12. 阶段 11～13：发布 C（Storage 去业务耦合）

对应去耦合设计第 2～6 节。

### 12.1 阶段 11：Storage

| 范围 | 文件 | 改动 |
| --- | --- | --- |
| 写入方 | `primarystore/service.go`、`period.go`、`marker.go` | 删除 `isFactorAppID`、`isOwnedAppID`、`collectorWritableDataset`、`validateCollectorPeriodDataset`、`rejectCollectorPeriodWriteCredential` 与按 `dataset_role` / `owner_module` 的分支；统一为「调用方 AppId ∈ 数据集 `write_owner`」；强制校验由配置开关控制（发布 C 第 4 步才打开） |
| 元数据权限 | `metadata/dataset_attributes.go`、`catalog/metadata_catalog.go` | 删除业务属性的不可变约束；`write_owner` 只能由当前所有者或全局管理员修改；`CreateDataset` 中 `data_source_id` 对所有数据集可选（填写时校验引用）；`DeleteDataset`、删除 / 恢复行改为通用的 `write_owner` 授权 |
| 幂等写入 | `primary_store.proto`、`data_node.proto`、`pebble/commit_receipts.go`、`pebble/factor_rows.go`、`pebble/store.go`、`primarystore/service.go` | `UpsertFields` 收为单数据集请求；`UpsertFields`、`CommitTimeSeriesBatch` 增加 `commit_id`；回执重做：键为 `(space_id, dataset_id, commit_id)`，指纹含操作类型与完整载荷，记录写入时间、7 天后清理，与写入、outbox 同一批次提交；删除 `WriteFactorRows`、`source_event_id` 与已处理来源事件标记、`write_kind` |
| 周期完成 | `primary_store.proto`、`data_node.proto`、`dataset_markers.proto`、`packages/storagepb`、`pebble/marker_*.go`、`datanode/marker.go`、`primarystore/marker.go`、`cmd/cli` | 新增 `ReportDatasetPeriodCompleted`、`GetDatasetPeriodCompleted` 与事件 `DatasetPeriodCompleted`；账本判定改发新事件（`source = ledger`）；删除两种旧事件与五个旧接口；`moox-storage-cli` 增加按时间清除周期标记与 outbox 记录的维护命令（发布 C 回退用） |
| 就绪事件 | `view/period_event_apply.go`、`view/event_apply.go`、`view/data_ready.go` | `ViewDataReady` 的 `completion_kind` 改为 `source`，`factors[]` 改为透传 `detail_type` / `detail` |
| 数据集列来源 | `common.proto`、`metadata.proto`、`schema/metadata.sql`、`metadata/sqlite`、`bootstrap/metadata/seed.go`、`catalog/validate.go`、`catalog/metadata_catalog.go`、`view/build.go`、`view/event_apply.go`、`cmd/cli` | `DatasetColumnOriginType` 的 `FACTOR` 改名为 `CUSTOM`、新增 `ATTRIBUTE`（`origin_id` 为属性键）；`t_dataset_columns` 的 `CHECK` 改为 `('field', 'custom', 'attribute', 'system')`，schema 升到 v14（同步 `metadata_schema_version_test`、`TestMetadataSchemaVersionIsExact`）；`moox-storage-cli` 增加一次性的 v13 → v14 迁移命令（事务内重建该表、`factor` 改为 `custom`、写版本号），发布 C 后删除；删除因子输出列的专门校验；View 重建与事件应用按数据集列的来源读取值或属性；删除 `factorAttributeKeys` |
| View 资源 | `catalog/factor_result_view.go`、`catalog/metadata_catalog.go`、`metadata/sqlite/crud_dataset.go`、`metadata/sqlite/crud_view_index.go`、`view/maintenance_schema_extension.go`、`view/event_apply.go` | 删除托管默认 View 与删除数据集时自动删 View；数据集加列在同一元数据事务里更新所有 `follow_dataset_columns = true` 的 View 的投影与期望版本；扩展提交条件（`crud_view_index.go:279`）与消费屏障（`validateFactorResultEventColumns`）改为按 `follow_dataset_columns` 判断 |
| 投递 | `view/inventory_reconciler.go`、`view/eventconsumer/delivery_policy.go`、`config/loader.go`、`config/storage.yaml` | 按数据集属性 `view_delivery` 选择 `parallel` / `serial` 模板；删除 `dataset_factor_` 前缀规则；分区名改为 `parallel`、`serial`、`metrics` |
| 元数据校验 | `catalog/validate.go` | 删除 `dataset_role` 取值、`dataset_factor_` 前缀、`factor_output` 显示名规则；新增 `write_owner` 非空与 `view_delivery`、`follow_dataset_columns` 取值校验 |
| 保留策略包 | `packages/storagepolicy/policy.go`、`moox.toml.example`、`configinit/moox.toml.example`、CLI 策略渲染 | 各市场默认保留期移到部署配置；删除按市场交易分钟数估算根数的校验 |
| 初始化配置 | `config/setup/metadata.yaml` | 五个系统数据集补 `write_owner = moox-monitor` |
| 数据规范 | `primarystore/service.go` | 删除 `normalizeStockCNSeriesTags` |
| 其他 | `observability/view_metrics.go`、`cmd/cli` | 标签与维护命令去掉业务命名和分支 |

测试见去耦合设计第 5 节；另外全仓 `rg -i 'factor|collector|stockcn|raw_collection|kline'` 在 `modules/storage` 的非测试代码中只剩通用含义（如注释中的举例），逐条核对。

### 12.2 阶段 12：上层模块

| 模块 | 文件（主要） | 改动 |
| --- | --- | --- |
| `packages/factorpb`（新建） | `period_detail.proto` | `PeriodDetail`（原 `FactorPeriodState` 列表）与校验，从 `packages/events` 移来 |
| `packages/events` | 事件注册表 | 换成 `DatasetPeriodCompleted`；删除因子状态校验 |
| factor-mgr | 数据集与 View 维护、`internal/storageio/metadata.go`、`internal/catalog` | 因子输出列的来源改为 `CUSTOM`（`ColumnOriginFactor` 改名）；`write_owner = moox-factor`、`view_delivery = serial`；来源哈希等定义为来源为属性的数据集列；自己建、改、删默认 View（`follow_dataset_columns = true`）；删除因子结果数据集：检查额外引用 → 删默认 View → 清理行 → 删数据集，可重试；启动对账补齐已有数据集与 View |
| 因子引擎 | `internal/storageio`、触发与上报 | `UpsertFields(commit_id)`；`ReportDatasetPeriodCompleted` 带 `PeriodDetail`；`GetDatasetPeriodCompleted`；订阅 `DatasetPeriodCompleted`，按因子集绑定的源数据集与频率筛选，`ledger`、`producer` 都接受 |
| Collector | 任务结果、`marketstorage`、`resample`、SCF 写入 | `write_owner = moox-collector`；`UpsertFields` 按数据集拆分请求；重采样报告改为 `ReportDatasetPeriodCompleted`；A 股默认 `series_tag` 在写入方填写；去重用 `commit_id` |
| Strategy | `internal/trigger`、`internal/readiness`、`internal/input` | 按绑定的 View 与数据集判断就绪事件是否相关，不按来源区分；`detail_type` 为因子明细时解码；删除按数据集角色选择完成类型的逻辑及 `TestResolveCompletionKindByDatasetRole` |
| Monitor | `hostmetrics/storage_writer.go`、系统数据集对账 | 去重用 `commit_id`；启动对账为系统数据集补齐 `write_owner = moox-monitor` |
| 归档 | `internal/worker/reader`、`export` | 发布 B 已上线时：来源为属性的数据集列作为 `attribute_keys` 读取并按列导出；新列出现时按增列处理 |
| 服务目录 | `catalog.yaml` | PrimaryStore 方法改名与删除；`factor-engine`、`scf-collector` 等外部身份白名单同步 |

### 12.3 阶段 13：发布 C 与验收

发布 C 改元数据属性，旧事件也持久化在 DataNode（周期标记与 outbox，`pebble/marker_store.go`）和 View（待发布就绪队列，`view/data_ready.go`）里，
是一次**有状态切换**（去耦合设计第 6 节），按停写、切换、恢复执行：

1. 演练：先在隔离环境（`moox-e2e` 与 `moox.sandbox.toml`）完整走一遍下面的步骤和回退；
2. 停写：暂停 Collector 的计划对账与补采、停因子引擎与 Strategy；确认 DataNode outbox 排空、各 View 水位追平、待发布就绪队列为空；
3. 备份：Storage 元数据库、Collector 与 factor-mgr 的库，以及各组件二进制；
4. 同一提交构建、发布：storage-node → 停 storage-primary、用新版 `moox-storage-cli` 把元数据库迁移到 v14、启动新的 storage-primary → 至少等 30 秒 → storage-view → Admin → control 的 host-gateway → Monitor → Access 与其他主机的 host-gateway
   → Collector（SCF 代码包随之滚动升级）→ factor-mgr → 因子引擎 → Strategy；此时 `write_owner` 强制校验关闭；
5. 各业务模块启动对账补齐属性与默认 View 后，核对每个数据集的 `write_owner`，再打开强制校验；清除 EventBus 中两种旧周期事件的主题，
   View 消费者按新事件重建（参照 `reset-view-consumers`，必要时 `repair-view`）；
6. 恢复 Collector 的计划对账与补采、因子引擎、Strategy。

验收：采集周期按期完成（`source = ledger`）；重采样结果（`source = producer`）触发因子，因子按期产出并报告；View 就绪事件正常，Strategy 按期触发；
factor-mgr 接管的默认 View 加列后原地扩展、新列不丢；删除一个测试用因子结果数据集成功；非 `write_owner` 的写入被拒。

回退：先停写并等 outbox 排空；用新版 `moox-storage-cli` 清除 DataNode 中切换后写入的新格式周期标记与 outbox 记录（阶段 11 提供这个维护命令，
旧代码读不了新格式），用 `purge-dataset-events` 清除新事件的主题；然后换回全部组件的旧二进制、恢复第 3 步备份的数据库，View 重建，再恢复各模块。
不能只换回二进制。

## 13. 风险与检查点

| 风险 | 检查点 |
| --- | --- |
| 统一读事实后，原来由 View 回答的读取（Collector 读行、计数、CLI）改由 DataNode 回答，增加 DataNode 负载 | 阶段 1 的 S-2、S-10 benchmark；发布 A 后观察 storage-node CPU 与读取耗时；Collector 本来就是先计数、只读缺口标的 |
| View 重建改为按页读取后行为变化（超时处理、属性） | 阶段 1 的新旧重建逐行比较；发布 A 后执行一次 `repair-view` |
| S-3 共享快照被调用方意外修改 | 逐个调用点检查；`-race` 用例；必要处 `proto.Clone` |
| 发布 A 期间旧因子引擎的翻页不可用 | 编译机引擎紧随 Storage 发布；发布后核对运行摘要，必要时 `recalc` |
| DuckDB 资源占用（暂存库、临时目录） | 专用临时目录与内存上限；心跳上报暂存库大小；每天维护重建 |
| worker 时钟偏差导致避让失效 | 部署脚本检查 NTP 同步状态；发布 B 验收看 Access 日志时间分布 |
| 编译机断电断网超过线上保留期 | 「接近保留期」告警（任务异常）；crypto 1m 保留 7 天，提前 2 天告警 |
| 服务目录三次协调发布（A、B、C） | 每次都按 Admin → host-gateway → Monitor 的顺序紧接着换，并核对网关观测中的 hash |
| 去耦合后事件协议整体替换 | 发布 C 前在隔离环境演练「停写 → 切换 → 清除旧主题 → View 按新事件重建 → 因子与策略按期运行」以及回退 |
| 发布 C 是有状态切换（元数据属性、持久化的旧事件） | 停写后再切换；先备份数据库；`write_owner` 强制校验在属性补齐、核对后才打开；回退同时恢复数据库与事件状态 |
| 元数据 schema 从 v13 迁移到 v14（重建 `t_dataset_columns`） | 迁移命令在事务内完成，失败时库保持 v13；隔离环境先演练；迁移前备份；迁移后核对表结构与新建库一致 |
| 重采样数据集的完成事件是 `producer`，下游误按来源过滤 | 因子与 Strategy 按绑定的数据集与 View 判断相关性；「重采样 → 因子 → Strategy」端到端用例 |
| factor-mgr 接管默认 View 时与 Storage 原有托管状态冲突 | 对账逻辑先读取现有 View 属性再改写；发布 C 的验收中检查每个因子结果数据集的默认 View |

## 14. 交付物清单

- 代码：阶段 1～12 各自的提交；生成代码；schema 变更通过格式校验。
- 测试与基线：阶段 1、2 的 benchmark 结果（改造前后对比）记入第 15 节。
- 文档：第 5.4 节与第 10 节列出的模块文档；设计文档的状态改为「已实施」并补充与设计的差异。
- 运维：`deploy-archive-worker.sh`；发布 A、B 的备份目录与 sha256 记录。

## 15. 实施记录

（按阶段记录与计划的差异、benchmark 结果和线上验收结果。）
