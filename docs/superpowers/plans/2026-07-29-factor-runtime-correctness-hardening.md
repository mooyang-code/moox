# Time-Series Series Tag 与 Factor 正确性改造实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans` to
> implement this plan task-by-task. Use the checkboxes as the execution record.

**Goal:** 用一个可选、不透明的 `series_tag` 完整替换全链路 Dimensions Map，并让
Storage、View、Archive、Factor 和所有时序读写方在同一行身份、查询语义和
`lookback_periods` 上达成一致；同时完成此前确认的 Factor 实时正确性修复。

**Architecture:** TimeSeries 行身份固定为
`space + dataset + subject + freq + data_time + series_tag`。精确 Key 中空 tag 表示
默认序列；范围 selector 用 proto presence 区分“全部 tag”和“精确空 tag”。
Storage 不解析 `namespace:value`。Factor 的任务 scope 不包含 tag，每次在单 Dataset
内读取完整 tag cohort，每个任务只执行一个 Factor，Python 用带
`data_time + series_tag` 的 DataFrame 返回任意目标行。Archive 将 tag 纳入物理
PartitionKey，每个 Subject/tag/月独立物化一个 Parquet 文件。

**Tech Stack:** Go 1.25、Protocol Buffers、tRPC-Go、Pebble、DuckDB、NATS
JetStream、Parquet、GORM + SQLite、Python 3 + pandas、Vue 3 + TypeScript。

**Canonical design:**
[Time-Series `series_tag` 统一设计](../specs/2026-07-29-time-series-series-tag-design.md)

---

## 0. 实施原则与基线

本计划审查代码时的基线：

```text
repository: /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
branch:     feature/mooyang
code HEAD:  417fdf3d242d26e1865fc5e5b72cc894c0179bb1
date:       2026-07-29
```

实施必须遵守：

- 新项目不兼容旧 Proto、Pebble、DuckDB、Parquet、Factor SQLite 或 Event v1。
- 不实现 Dimensions Map、多 tag、tag registry、Factor DAG、持久化任务队列或
  Exactly-once。
- 不把 `series_tag` 拆成 name/value；Storage 只做形态校验与原样比较。
- 每个 Task 先写失败测试，再实现最小代码并运行可执行的模块级测试。
- Tasks 1-13 是一次 wire-breaking 原子切换：期间只在隔离 worktree 保存工作，不
  commit、不 push；Task 13 在 Proto、Storage、Archive、Monitor、Collector、CLI、
  Web、Factor 全部编译和语义测试通过后创建唯一 cutover commit。这样共享历史中
  不会出现新 Proto 配旧消费者的不可编译提交。Task 14 以后再按任务独立提交。
- 不在本改造中顺手解决 DataNode TTL 删除后 View 未同步删除的问题；另立 Storage
  生命周期任务。
- 任一 Proto 改动必须同一提交更新生成物和所有消费者，禁止中间状态进入共享分支。

### Task 0: 建立隔离工作区与基线

**Files:**
- No code changes

- [ ] **Step 1: 记录当前分支、HEAD 和工作区**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git status --short --branch
git rev-parse HEAD
git ls-files --others --exclude-standard
```

Expected: 本计划与 canonical spec 已经提交并可由 `git ls-files` 找到；没有未知业务
代码改动。若文档仍未提交，先完成文档提交，再创建 worktree。

- [ ] **Step 2: 创建实施 worktree**

Run:

```bash
plan_commit="$(git rev-parse HEAD)"
git worktree add .worktrees/time-series-tag \
  -b feature/time-series-tag "$plan_commit"
cd .worktrees/time-series-tag
```

- [ ] **Step 3: 跑修改前基线**

Run:

```bash
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
(cd modules/storage && go test ./... -count=1)
(cd modules/factor && go test ./... -count=1)
(cd modules/archive && go test ./... -count=1)
(cd modules/monitor && go test ./... -count=1)
(cd modules/collector && go test ./... -count=1)
```

Expected: 全部 PASS。若有既有失败，先记录命令、错误与基线 SHA，不把它归因于本次
改造。

---

## 1. Storage 协议与事实身份

### Task 1: 用 scalar `series_tag` 替换协议并升级事件到 v2

**Files:**
- Modify: `modules/storage/proto/rows.proto`
- Modify: `modules/storage/proto/view.proto`
- Modify: `packages/storagepb/storage_events.proto`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/validation.go`
- Regenerate: `modules/storage/proto/storagegen/*.pb.go`
- Regenerate: `packages/storagepb/storage_events.pb.go`
- Test: `packages/storagepb/storage_events_test.go`
- Test: `modules/storage/internal/eventmapper/rows_test.go`
- Test: `packages/events/validation_test.go`

- [ ] **Step 1: 先写协议往返与 presence 失败测试**

覆盖：

1. 本地 `TimeSeriesRowKey.series_tag="venue:okx"` 经 event mapper 往返后字节一致。
2. 精确 Key 的空 tag 往返仍是默认序列。
3. `TimeSeriesSelector` 未设置 tag、设置 `""`、设置 `venue:okx` 三种状态可区分。
4. v1 事件名不再被 v2 Consumer 接受。

- [ ] **Step 2: 修改 Proto**

目标模型：

```proto
message TimeSeriesRowKey {
  string subject_id = 1;
  string freq = 2;
  string data_time = 3;
  string series_tag = 4;
}

message TimeSeriesKey {
  string space_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string freq = 4;
  string data_time = 5;
  string series_tag = 6;
}

message TimeSeriesSelector {
  string space_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string freq = 4;
  optional string series_tag = 5;
}
```

`ReadTimeSeriesRowsReq` 和 `QueryTimeSeriesRowsReq` 范围查询改用
`repeated TimeSeriesSelector selectors`；点查只走 `ReadFields(RowKey)`。删除当前
未实现的 `page_token/next_page_token`，保留稳定排序的 offset page，明确并发写入时
不提供跨请求快照。

不要添加 `reserved` 或兼容字段；仓库 greenfield contract 会检查这些残留。

- [ ] **Step 3: 升级公共事件**

把 `storage.dataset.rows.upserted@1` 升为 `@2`，同步 Registry、Subject 和验证测试。
本地与公共 Proto 的字段名、类型、语义完全一致，让 protojson mapper 继续只做结构
复制。

- [ ] **Step 4: 重新生成并验证**

Run:

```bash
make proto
go test ./packages/storagepb ./packages/events -count=1
(cd modules/storage && go test ./internal/eventmapper -count=1)
! rg -n "dimensions|Dimensions" \
  modules/storage/proto packages/storagepb \
  --glob '*.proto' --glob '*.pb.go'
```

Expected: 测试 PASS；最后一个命令无输出。

- [ ] **Step 5: 保存原子切换工作状态，不提交**

```bash
git diff --check
git status --short
```

Expected: Proto 与生成物已更新；消费者迁移将在 Tasks 2-13 完成。此时禁止 commit
或 push。

### Task 2: 建立唯一 tag 校验与 Pebble v2 键布局

**Files:**
- Delete: `modules/storage/internal/rowidentity/dimensions.go`
- Delete: `modules/storage/internal/rowidentity/dimensions_test.go`
- Create: `modules/storage/internal/rowidentity/series_tag.go`
- Create: `modules/storage/internal/rowidentity/series_tag_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/key.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store.go`
- Modify: `modules/storage/internal/service/datanode/pebble/key_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store_test.go`
- Create: `modules/storage/internal/service/datanode/pebble/layout.go`
- Create: `modules/storage/internal/service/datanode/pebble/layout_test.go`

- [ ] **Step 1: 写 tag 校验测试**

`ValidateSeriesTag` 应接受空值和 `venue:binance`，拒绝：

- 非 UTF-8；
- 超过 128 bytes；
- NUL 或 ASCII 控制字符；
- 首尾空白。

校验函数不得 trim、lowercase、解析冒号或返回改写后的 tag。

- [ ] **Step 2: 写物理身份失败测试**

覆盖：

- 同一时间点的 `""`、`venue:binance`、`venue:okx` 生成不同 Pebble key；
- 同一个 tag 的 Field/Attribute 共用完整 RowKey；
- 读取一个 tag 不返回另一个 tag；
- tag 内合法 Unicode 和普通符号原样往返；
- bucket 清理前缀仍只按 Space/Dataset/bucket，不因 tag 改变。

- [ ] **Step 3: 实现 v2 键布局**

删除 JSON canonicalization。TimeSeries tuple 固定为：

```text
kind | space | dataset | bucket | subject | freq | data_time | series_tag
```

空 tag 也编码最后一个空 tuple component。`NormalizeRowKey` 只规范 UTC 时间，不改
tag。

- [ ] **Step 4: 增加 layout marker**

DataNode 根目录写入固定 `storage_layout_version=2`。打开无 marker、v1 marker 或含旧
键布局的数据目录时不能一概处理：

- 路径不存在或目录为空：原子创建 v2 marker，再初始化 Pebble；
- 非空目录没有 marker：返回明确的 `reset DataNode store` 错误；
- v1/未知 marker：拒绝并要求清理；
- 已有 v2 marker：正常重开。

四种情况都写单测；不能让首次启动因“无 marker”被误拒绝。

- [ ] **Step 5: 验证当前子系统**

```bash
(cd modules/storage && \
  go test ./internal/rowidentity ./internal/service/datanode/pebble -count=1)
```

Expected: 目标 package PASS；继续保留未提交改动。

### Task 3: 拆分精确 Key 与范围 Selector

**Files:**
- Modify: `modules/storage/internal/service/viewindex/model.go`
- Modify: `modules/storage/internal/service/view/query.go`
- Create: `modules/storage/internal/service/view/query_test.go`
- Modify: `modules/storage/internal/service/primarystore/rows_read.go`
- Modify: `modules/storage/internal/service/primarystore/service_test.go`

- [ ] **Step 1: 写 selector 语义测试**

同一 Subject/Frequency/Time 写三种 tag 后：

| selector | 期望 |
| --- | --- |
| 未设置 `series_tag` | 三行 |
| 设置 `series_tag=""` | 仅默认行 |
| 设置 `series_tag="venue:okx"` | 仅 OKX 行 |

同时断言返回 `TimeSeriesKey` 始终包含精确 tag。

- [ ] **Step 2: 修改内部 QuerySpec**

禁止把 selector 降级成 `RowKey`。内部结构显式保存：

```go
type TimeSeriesSelector struct {
    SpaceID, DatasetID, SubjectID, Freq string
    SeriesTag *string
}
```

`nil` 表示全部 tag，非 nil 指向空串表示默认序列。所有 Primary/View/Access 转换都
保留 presence。

- [ ] **Step 3: 透传 View 完整性**

在 `ReadTimeSeriesRowsRsp` 增加并由 PrimaryStore 从 DataView 原样复制：

```text
served_indexed_from
served_indexed_to
complete
```

空 rows 不能自动改成 `complete=true`。

- [ ] **Step 4: 验证当前子系统**

```bash
(cd modules/storage && \
  go test ./internal/service/primarystore ./internal/service/view -count=1)
```

### Task 4: 把 DuckDB View 切换为 scalar tag 与稳定全序

**Files:**
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`
- Modify: `modules/storage/internal/service/view/build.go`
- Create: `modules/storage/internal/service/view/build_test.go`
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Test: `modules/storage/internal/service/view/reconcile_test.go`
- Modify: `modules/storage/internal/service/viewindex/model.go`

- [ ] **Step 1: 写 DuckDB 失败测试**

覆盖：

- DDL 为 `series_tag VARCHAR NOT NULL`；
- PK 是 `(subject_id, freq, data_time, series_tag)`；
- 同时刻不同 tag 均存在；
- 同 tag upsert 只更新自身；
- absent/present-empty/present-value selector 正确；
- 默认与用户自定义排序最终都补齐唯一 tie-breaker；
- `SORT_ORDER_DESC` 将
  `(subject_id, freq, data_time, series_tag)` 四列整体设为 DESC；
- 同 timestamp 两个 tag、page size 1 时，ASC/DESC 两页都不重复、不漏行且顺序
  互为反向；
- Backfill cursor 在同一 timestamp 的多个 tag 间不跳行。

- [ ] **Step 2: 修改 DDL、读写和保留列**

删除 `parseDimensions`、JSON marshal 和所有 `dimensions_json` 分支。系统保留列为：

```text
subject_id, freq, data_time, series_tag
```

默认全序：

```text
subject_id ASC, freq ASC, data_time ASC, series_tag ASC
```

`SORT_ORDER_DESC` 使用四列整体 DESC。用户自定义 sorts 后按缺失顺序追加上述
ASC tie-breaker，保证每页确定性；不要把公开 DESC 路径与任意自定义 sort 混为一套
方向推导。

- [ ] **Step 3: 更新 Backfill keyset**

`build.go` 的 Grain、`QueryAfter` 和 cursor 全部加入 `series_tag`。实时双写与
Backfill 只按完整 PK 判断同一行。

- [ ] **Step 4: 处理旧 View**

`Open/Stat/Reconcile` 直接检查固定系统列和主键。发现旧 `dimensions_json`、缺少
`series_tag` 或主键不匹配时拒绝复用 Active/New index，并要求清理重建；不引入
通用 schema migration/epoch 框架，也不执行 `ALTER TABLE`。

- [ ] **Step 5: 验证当前子系统**

```bash
(cd modules/storage && CGO_ENABLED=1 \
  go test ./internal/service/viewindex/duckdb ./internal/service/view -count=1)
```

### Task 5: 更新 Metadata grain、系统列和初始化 seed

**Files:**
- Modify: `modules/storage/internal/service/catalog/validate.go`
- Modify: `modules/storage/internal/service/catalog/metadata_catalog_test.go`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store_test.go`
- Modify: `modules/storage/schema/metadata_schema_version_test.go`
- Modify: `scripts/test-storage-datanode-management-contract.sh`
- Modify: `examples/metadata-quant-initial.seed.yaml`
- Modify: `examples/metadata-monitor-host.seed.yaml`
- Modify: `examples/metadata-monitor-metrics.seed.yaml`
- Modify: `modules/cli/internal/command/metadata_quant_seed_test.go`

- [ ] **Step 1: 写 metadata 校验失败测试**

TimeSeries View 的完整 grain 必须是：

```text
subject_id, freq, data_time, series_tag
```

用户 Field/ViewColumn 不得与四个系统列重名。Dataset metadata 不新增
`series_tag_name`、allowed values 或 tag registry。

- [ ] **Step 2: 执行破坏性 Metadata v6**

把 metadata schema version 升到 v6；启动旧 v5 数据库时明确提示清理并重新
`init/import-seed`。不写迁移 SQL。

量化 seed 将同 Schema/频率/生命周期的 crypto venue 合并：

```text
crypto/spot_kline_1h
crypto/perpetual_kline_1h
```

venue 由行 `series_tag` 区分。Monitor seed 的 TimeSeries grain 同步加 tag。
共享 crypto Dataset 绑定一个逻辑聚合 `crypto_market` DataSource；物理 Binance/OKX
DataSource 继续用于 Provider 配置，不把一个 Dataset 改成多 DataSource 绑定。
共享 View ID 固定为 `spot_kline_1h_view` 和 `perpetual_kline_1h_view`，seed contract
测试同步删除四个 venue 专属 Dataset/View 断言。

- [ ] **Step 3: 验证当前子系统**

```bash
(cd modules/storage && \
  go test ./internal/service/catalog ./internal/service/metadata/sqlite ./schema -count=1)
tmp="$(mktemp -d /tmp/moox-storage-series-tag.XXXXXX)"
MOOX_STORAGE_HOME="$tmp" go run ./modules/storage/cmd/cli import-seed \
  --storage-conf modules/storage/config/storage.yaml \
  --seed examples/metadata-quant-initial.seed.yaml
status=$?
rm -rf "$tmp"
exit "$status"
```

### Task 6: 完成 Storage 事件、实时 View、回填和查询 E2E

**Files:**
- Modify: `modules/storage/internal/service/e2e/flow_test.go`
- Modify: `modules/storage/internal/service/e2e/view_query_consistency_e2e_test.go`
- Modify: `modules/storage/internal/service/e2e/view_consumer_concurrency_e2e_test.go`
- Modify: `modules/storage/internal/eventmapper/rows.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/internal/service/datanode/service.go`
- Modify: `modules/storage/test/storage_contract_test.go`
- Modify: `scripts/test-storage-boundary-contract.sh`
- Modify: `scripts/test-storage-consistency-contract.sh`

- [ ] **Step 1: 添加真实双 tag E2E**

同一 Dataset/Subject/Frequency/Time 写：

```text
""               close=100
venue:binance   close=101
venue:okx       close=102
```

验证 Pebble 精确读取、`DatasetRowsUpserted@2`、Active View、Backfill 后 New View、
selector 三态、稳定排序和字段 patch 均一致。

- [ ] **Step 2: 添加旧术语 contract**

active source tree 必须不存在：

```text
dimensions
Dimensions
dimensions_json
CanonicalDimensions
```

排除历史计划和第三方生成依赖，但不能排除当前 Proto、服务、模块 README 和 Web
代码。

- [ ] **Step 3: 运行 Storage 验收**

```bash
(cd modules/storage && go test ./... -count=1)
(cd modules/storage && CGO_ENABLED=1 go test ./... -count=1)
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
```

---

## 2. Archive 与内置生产者

### Task 7: 将 Archive journal 和 Parquet 升级为 `series_tag` v2

**Files:**
- Modify: `modules/archive/internal/domain/row.go`
- Modify: `modules/archive/internal/domain/identity.go`
- Modify: `modules/archive/internal/domain/identity_test.go`
- Modify: `modules/archive/internal/eventconsumer/decode.go`
- Modify: `modules/archive/internal/backfill/backfill.go`
- Modify: `modules/archive/internal/journal/store.go`
- Modify: `modules/archive/internal/writer/writer.go`
- Modify: `modules/archive/internal/parquetio/schema.go`
- Modify: `modules/archive/internal/parquetio/codec.go`
- Modify: `modules/archive/internal/parquetio/schema_test.go`
- Modify: `modules/archive/internal/parquetio/codec_test.go`
- Modify: `modules/archive/internal/eventconsumer/decode_test.go`
- Modify: `modules/archive/internal/backfill/backfill_test.go`
- Modify: `modules/archive/internal/journal/store_test.go`
- Modify: `modules/archive/internal/writer/writer_test.go`
- Modify: `modules/archive/internal/config/config.go`
- Modify: `modules/archive/internal/config/config_test.go`
- Modify: `modules/archive/config/app.yaml`
- Modify: `modules/archive/internal/registry/client.go`
- Modify: `modules/archive/internal/registry/client_test.go`
- Modify: `modules/archive/internal/cosstore/client.go`
- Modify: `modules/archive/internal/cosstore/client_test.go`
- Modify: `modules/archive/internal/cosstore/sync.go`
- Create: `modules/archive/internal/cosstore/sync_test.go`
- Modify: `modules/archive/cmd/cli/main.go`
- Modify: `modules/archive/cmd/cli/main_test.go`
- Modify: `modules/archive/test/archive_e2e_test.go`

- [ ] **Step 1: 写 Archive 双 tag 失败测试**

同一时间两种 tag 经事件与 Backfill 产生两个独立 Partition，每个 Partition 各有
一条 ArchiveRow；同 tag 修订只覆盖自己的月文件。测试必须抓住当前
`CanonicalStringMap(nil)` 静默丢身份的问题，并断言：

```text
.../BTC-USDT/series_tag=venue%3Abinance/
.../BTC-USDT/series_tag=venue%3Aokx/
.../BTC-USDT/series_tag=/
```

分别对应 Binance、OKX 和默认序列。文件名也必须携带相同
`series_tag={encoded_tag}` 字段；percent 编码可逆，字面 `%`、`/`、`..` 和空 tag
不会碰撞或越过归档根目录。

- [ ] **Step 2: 改领域模型和分区身份**

删除 `DimensionsJSON`，不要在 RowPatch 上再保存一份可能与路径不一致的 tag。
`PartitionKey` 增加 `SeriesTag string`，固定身份为：

```text
space_id + dataset_id + freq + subject_id + series_tag + YYYYMM
```

`PartitionID`、journal key、dirty 状态、generation、writer mutex 和
`ArchiveFileID` 全部加入 tag。一个 Partition 内只存在一个 tag，因此
`LogicalRowID` 只使用 UTC `data_time`；全局行身份由 PartitionKey 与 LogicalRowID
共同组成。事件和 Backfill 从 Storage key 原样复制 tag 到 PartitionKey。

- [ ] **Step 3: 改 Parquet v2**

删除 `dimensions_json`，增加必填 UTF-8 `series_tag`。每个文件内该列必须为常量，
并与目录及文件名解码出的 tag 一致。分区内排序和唯一键为：

```text
candle_begin_time ASC
```

本地相对路径与 COS object key 统一为：

```text
{space}/{dataset}/{freq}/{encoded_subject}/series_tag={encoded_tag}/
  {space}__{dataset}__{encoded_subject}__{freq}__series_tag={encoded_tag}__{YYYYMM}.parquet
```

`encoded_subject` 与 `encoded_tag` 复用 `domain.EncodeIdentity` 的可逆编码；空 tag
的目录固定为 `series_tag=`。`FileName/ParseFileName` 使用六个固定字段并校验
目录 tag、文件名 tag 和 Parquet 常量列完全一致。metadata 使用
`moox.archive.schema_version=2`。发现 v1 文件、旧 journal、旧列或不含 tag 的旧
路径时明确拒绝并提示使用新目录；不兼容读写。

- [ ] **Step 4: 更新 Archive 启动与登记配置**

默认 allowlist 使用 `crypto` 共享 Dataset，Consumer durable 使用
`moox_archive_kline_v2`，ArchiveFile metadata 写 `schema_version=2`。
`partition_key` 固定为
`freq/encoded_subject/series_tag=encoded_tag/YYYYMM`，稳定 ID 和 COS key 都包含
tag 身份：稳定 ID 的摘要输入使用原始 tag，COS key 使用可逆编码。Archive CLI
增加 presence-aware `--series-tag`：未出现表示全部 tag，显式空串表示默认序列，
非空值表示精确 tag。同步更新配置、CLI、COS 和 registry 单测，确保默认配置可以
启动且不会拒绝目标 Dataset。

- [ ] **Step 5: 验证当前子系统**

```bash
(cd modules/archive && go test ./... -count=1)
```

### Task 8: 修改 Monitor 多实体时序身份

**Files:**
- Modify: `modules/monitor/internal/hostmetrics/storage_writer.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_writer_test.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_reader.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_reader_test.go`
- Modify: `modules/monitor/internal/hostmetrics/storage_gate_test.go`
- Modify: `modules/monitor/test/host_monitor_direct_storage_e2e_test.go`
- Modify: `modules/monitor/internal/metrics/storage.go`
- Modify: `modules/monitor/internal/metrics/storage_test.go`
- Modify: `modules/monitor/internal/rpc/metrics_test.go`
- Modify: `modules/monitor/test/metrics_eventbus_e2e_test.go`
- Modify: `modules/monitor/test/watchdog_e2e_test.go`
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/internal/bootstrap/market_canary.go`
- Modify: `modules/monitor/internal/watchdog/market_canary.go`
- Modify: `modules/monitor/internal/watchdog/market_canary_test.go`

- [ ] **Step 1: 写无碰撞测试**

同一分钟：

- resource 使用空 tag；
- disk/network 使用 `device:<device>`；
- filesystem 使用生产者可逆编码的一个 `filesystem:<identity>`；
- device、mountpoint 等展示值仍是普通 Field；
- reader 未设置 selector tag，能重建全部实体。

- [ ] **Step 2: 实现 producer-owned tag helper**

helper 只负责稳定编码 Monitor 自己的业务身份。不要把解析器下沉到 Storage，也不要
增加 Map。编码必须覆盖分隔符转义，并有 round-trip 单测。

- [ ] **Step 3: 让 Market Canary 精确选择一条序列**

`MarketCanarySubject` 使用必填 presence 字段（例如 `*string SeriesTag`），从而区分
“配置了默认空 tag”和“漏配 tag”。Canary selector 必须精确设置该 tag；CheckID、
Target 和名称也包含完整 tag。双 tag 测试应证明 page size 2 返回同一 venue 的相邻
两个 `data_time`，不会把同 timestamp 的 Binance/OKX 当成相邻 bar。

`modules/monitor/config/app.yaml` 改为共享 `crypto/spot_kline_1h` 并显式设置
`series_tag: venue:binance`。

- [ ] **Step 4: 验证当前子系统**

```bash
(cd modules/monitor && \
  go test ./internal/hostmetrics ./test -count=1)
(cd modules/monitor && \
  go test ./internal/config ./internal/bootstrap ./internal/watchdog -count=1)
```

### Task 9: 更新 Collector、CLI、Web 和样本入口

**Files:**
- Modify: `modules/collector/internal/sources/binance/kline.go`
- Create: `modules/collector/internal/sources/binance/kline_test.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Modify: `modules/collector/internal/sources/binance/storage_config_test.go`
- Modify: `modules/collector/internal/serverless/market_canary.go`
- Modify: `modules/collector/internal/serverless/market_canary_test.go`
- Modify: `modules/collector/cmd/scf/observability.go`
- Modify: `modules/collector/cmd/scf/main_test.go`
- Modify: `modules/collector/configs/observability.env.example`
- Modify: `scripts/build-collector-scf-package_test.sh`
- Modify: `modules/cli/internal/command/storage_import.go`
- Modify: `modules/cli/internal/command/storage_import_test.go`
- Modify: `modules/cli/internal/command/data.go`
- Modify: `modules/cli/internal/command/data_remote.go`
- Modify: `modules/cli/internal/command/data_test.go`
- Modify: `modules/cli/internal/command/setup_storage.go`
- Modify: `modules/cli/internal/command/setup_storage_test.go`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/access.ts`
- Modify: `web/src/api/storage/view.ts`
- Modify: `web/src/views/data/import/index.vue`
- Modify: `web/src/views/data/browse/index.vue`
- Modify: `web/src/views/data/browse/browse-utils.ts`
- Create: `web/src/views/data/browse/browse-utils.test.ts`
- Modify: `web/src/views/data/view-browse/index.vue`
- Modify: `examples/e2e/collector-symbol-kline.mjs`
- Modify: `examples/e2e/collector-symbol-kline.test.mjs`
- Modify: `examples/e2e/run.sh`
- Modify: `examples/e2e/run-real-scf.sh`
- Modify: `examples/e2e/verify.mjs`
- Modify: `examples/data/kline/stock_cn/stock_kline_1d.csv`
- Modify: `examples/data/kline/stock_cn/stock_kline_1h.csv`
- Modify: `examples/data/kline/crypto/binance_spot_kline_1h.csv`
- Modify: `examples/data/kline/crypto/binance_perpetual_kline_1h.csv`
- Modify: `examples/data/kline/crypto/okx_spot_kline_1h.csv`

- [ ] **Step 1: 写入口失败测试**

覆盖：

- Binance 写入和 watermark 使用 `venue:binance`；
- CLI import 用单个 `--series-tag`，删除 `--dimension`；
- CLI 范围读取省略 tag 表示全部，显式 `--series-tag ""` 表示默认序列；
- Web 导入页接受普通字符串，不解析 JSON；
- browse row ID 包含 `series_tag`，同 timestamp 两行不会覆盖；
- View browse 将 tag 作为系统列展示、排序和精确筛选。
- Collector Market Canary 必须配置 `venue:binance` 并发出精确 selector；双 tag 测试
  证明它只比较同一 venue 的相邻时间点，Check Target 包含 tag。

- [ ] **Step 2: 修改实现和样本**

删除 Web/JS 中 `dimensions: {}` 归一化逻辑。Crypto CSV 使用共享 Dataset ID，
增加 `series_tag` 列；股票样本使用空 tag。
SCF canary 默认 Dataset 改为 `crypto/spot_kline_1h`，新增必填
`MOOX_SCF_CANARY_SERIES_TAG`，用 `os.LookupEnv` 区分“未配置”和“显式空 tag”，并
将 tag 纳入配置校验和 Target identity。

- [ ] **Step 3: 验证当前子系统**

```bash
(cd modules/collector && go test ./... -count=1)
bash scripts/build-collector-scf-package_test.sh
(cd modules/cli && go test ./... -count=1)
(cd web && pnpm test)
(cd web && pnpm exec vue-tsc --noEmit)
node --test examples/e2e/collector-symbol-kline.test.mjs
bash -n examples/e2e/run.sh examples/e2e/run-real-scf.sh
node --check examples/e2e/verify.mjs
```

---

## 3. Factor 跨 Tag 计算契约

### Task 10: 将 Factor schema 改为 `lookback_periods`

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Regenerate: `modules/factor/proto/factorgen/*.pb.go`
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/internal/domain/factor.go`
- Modify: `modules/factor/internal/domain/validation.go`
- Modify: `modules/factor/internal/store/database.go`
- Modify: `modules/factor/internal/store/factor.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/rpc/convert.go`
- Modify: `modules/factor/cmd/cli/main.go`
- Modify: `modules/factor/cmd/cli/import.go`
- Modify: `modules/factor/internal/domain/validation_test.go`
- Modify: `modules/factor/internal/store/database_test.go`
- Modify: `modules/factor/internal/store/factor_test.go`
- Modify: `modules/factor/internal/registry/service_test.go`
- Modify: `modules/factor/internal/registry/metadata_sync_test.go`
- Modify: `modules/factor/internal/rpc/convert_test.go`
- Modify: `modules/factor/cmd/cli/main_test.go`

- [ ] **Step 1: 写旧 schema 拒绝测试**

新数据库只有 `c_lookback_periods`；发现 `c_lookback_rows` 或旧 Factor schema 时
启动失败并提示删除重建。`lookback_periods < 1` 继续拒绝；`input_columns` 和
`outputs` 使用 `data_time` 或 `series_tag` 也必须拒绝。

- [ ] **Step 2: 完成无兼容重命名**

Proto、Go、SQL、CLI flag 和 metadata attributes 统一为：

```text
lookback_periods
LookbackPeriods
--lookback-periods
```

不保留 alias、双列或自动迁移。

- [ ] **Step 3: 验证当前子系统**

```bash
make proto
(cd modules/factor && \
  go test ./internal/domain ./internal/store ./internal/registry ./internal/rpc \
    ./cmd/cli -count=1)
```

### Task 11: 把 Python I/O 改为带行身份的 DataFrame

**Files:**
- Modify: `modules/factor/internal/engine/types.go`
- Modify: `modules/factor/internal/engine/json_codec.go`
- Modify: `modules/factor/internal/engine/json_codec_test.go`
- Modify: `modules/factor/internal/engine/executor.go`
- Create: `modules/factor/internal/engine/executor_test.go`
- Modify: `modules/factor/pyworker/codec.py`
- Modify: `modules/factor/pyworker/worker.py`
- Modify: `modules/factor/pyworker/test_worker.py`
- Modify: `modules/factor/internal/storageio/dataframe.go`
- Modify: `modules/factor/internal/storageio/dataframe_test.go`
- Modify: `modules/factor/internal/storageio/writeback.go`
- Create: `modules/factor/internal/storageio/writeback_test.go`
- Modify: `examples/factors/timeseries/*.py`

- [ ] **Step 1: 写输入顺序与结果身份失败测试**

输入必须按 `(data_time, series_tag)` 稳定排序。Python 输出测试覆盖：

- 输出 tag 与输入 tag 相同；
- 两个 venue pivot 后输出 `venue_pair:binance-okx`；
- 空 DataFrame；
- 重复 `(data_time, series_tag)`；
- 缺系统列、多余业务列、非法时间、非法 tag；
- `NaN`/无穷归一化为 null；
- 输出落在目标范围外时被裁掉。

- [ ] **Step 2: 简化任务为一个 Factor**

`FactorTask` 只携带一个 `FactorSpec`。删除为多个 Factor 合并 input/output row grain 的
路径；Scheduler 对每个 enabled Binding/Factor 建独立任务，同 scope 仍可在队列层
合并时间范围。

- [ ] **Step 3: 修改 JSON/Python 协议**

输入列固定为：

```text
data_time, series_tag, <input_columns...>
```

`compute(df, params)` 返回 pandas DataFrame。Go `FactorResult` 保存有序结果行：

```go
type FactorResultRow struct {
    DataTime time.Time
    SeriesTag string
    Values map[string]any
}
```

写回用结果行身份，不再用 `targetTimes[i]` 或假定结果与物理输入等长。属性至少记录
`factor.id`、`factor.source_hash`、`factor.parent_task_id` 和 `factor.computed_at`。

- [ ] **Step 4: 验证当前子系统**

```bash
(cd modules/factor && \
  go test ./internal/engine ./internal/storageio -count=1)
(cd modules/factor && \
  PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
    python -m pytest pyworker -q)
```

### Task 12: 按完整时间 cohort 分块和回看

**Files:**
- Modify: `modules/factor/internal/storageio/client.go`
- Modify: `modules/factor/internal/storageio/client_test.go`
- Modify: `modules/factor/internal/scheduler/task.go`
- Modify: `modules/factor/internal/scheduler/builder.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`

- [ ] **Step 1: 写 cohort 分块失败测试**

构造 2001 个 `data_time`，每个时间点有 Binance/OKX 两行。验证：

- 第一 chunk 是 2000 个完整时间点、4000 行；
- 第二 chunk 从第 2001 个时间点开始；
- 只有读完最后一个完整 cohort 后才能用“最后时间 + 1ns”推进，不能在 row limit
  截断时直接推进；
- `lookback_periods=3` 恰好补前两个完整时间点，而不是两条物理行；
- tag 数量变化不改变回看时间点数。

- [ ] **Step 2: 实现范围读取 helper**

`ReadRangeChunk` 使用无 tag selector，按稳定全序逐页读取，聚合不同
`data_time`，直到得到目标 period 数并读完最后一个 cohort。历史倒序读取同理：
拿满 `lookback_periods - 1` 个时间点后读完整边界 cohort，再按升序合并。

这仍是单实例 best-effort 读取；不新增 Snapshot/Cursor 服务。完整性依赖 View
`complete/indexed_to` 和稳定排序。

- [ ] **Step 3: 更新 Scheduler**

task key 继续是：

```text
space + source_dataset + target_dataset + subject + freq + factor_id
```

不能加入 tag。每个 Factor 单独执行，pending 同 key 任务只合并时间范围。

- [ ] **Step 4: 验证当前子系统**

```bash
(cd modules/factor && \
  go test ./internal/storageio ./internal/scheduler -count=1)
```

### Task 13: 修复 View 并行消费与历史修正范围

**Files:**
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/trigger/event_batcher.go`
- Modify: `modules/factor/internal/trigger/event_batcher_test.go`
- Modify: `modules/factor/internal/storageio/client.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Modify: `modules/factor/internal/observability/realtime_inventory.go`
- Modify: `modules/factor/internal/observability/realtime_inventory_test.go`

- [ ] **Step 1: 写 event-only retry 失败测试**

覆盖：

- event task 首次 empty 且 `complete=false`，settle 后成功；
- `complete=false` 有行也重读，不能算半截数据；
- manual Recalc 的合法空范围立即成功，不做事件重试；
- 重试耗尽记录低基数 outcome，不永久阻塞 scheduler；
- `complete=true` 仍不被解释为“特定历史事件一定已应用”。

- [ ] **Step 2: 增加简单配置**

只增加：

```yaml
view_settle_delay: 300ms
event_read_retry: 3
event_read_retry_interval: 500ms
```

不增加持久化队列、事件 barrier 或跨服务 sequence。

- [ ] **Step 3: 扩展历史修正**

EventBatcher 保留事件的最小/最大 `data_time`，不带 tag。构建任务时，从最后一个受影响
时间点向后读取 `lookback_periods - 1` 个实际时间点，并把任务 end 扩到最后一个影响
period 的下一纳秒。多 Factor 因已拆任务，各自按自己的窗口扩展。

- [ ] **Step 4: 验证整个 wire cutover**

```bash
(cd modules/factor && \
  go test ./internal/trigger ./internal/storageio ./internal/scheduler \
    ./internal/observability ./internal/bootstrap -count=1)
(cd modules/storage && CGO_ENABLED=1 go test ./... -count=1)
(cd modules/archive && go test ./... -count=1)
(cd modules/monitor && go test ./... -count=1)
(cd modules/collector && go test ./... -count=1)
(cd modules/cli && go test ./... -count=1)
(cd modules/factor && go test ./... -count=1)
(cd modules/factor && \
  PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
    python -m pytest pyworker -q)
(cd web && pnpm test && pnpm exec vue-tsc --noEmit)
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
bash scripts/test-storage-datanode-management-contract.sh
! rg -U -n 'ReadTimeSeriesRowsReq\{[\s\S]{0,800}?Keys:' \
  modules packages examples web
! rg -U -n 'QueryTimeSeriesRowsReq\{[\s\S]{0,800}?Keys:' \
  modules packages examples web
git diff --check
```

Expected: 两个 `rg` 命令均无输出，证明范围查询调用方已经全部切到 selector。

- [ ] **Step 5: 创建唯一原子 cutover commit**

```bash
git add modules/storage modules/archive modules/monitor modules/collector \
  modules/cli modules/factor packages/storagepb packages/events web examples scripts
git diff --cached --check
git status --short
git commit -m "refactor(timeseries): replace dimensions with series tags"
```

Expected: staged diff 同时包含 Proto、生成物和所有消费者；commit 后 worktree clean，
且不存在只含新协议或只含旧消费者的中间提交。

---

## 4. Factor 管理与 Python 进程边界

### Task 14: 在启用前验证 Binding，并禁止环和热更新

**Files:**
- Create: `modules/factor/internal/domain/frequency.go`
- Create: `modules/factor/internal/domain/frequency_test.go`
- Create: `modules/factor/internal/registry/binding_contract.go`
- Create: `modules/factor/internal/registry/binding_contract_test.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/cmd/cli/import.go`
- Create: `modules/factor/cmd/cli/import_test.go`
- Modify: `modules/factor/internal/store/database.go`
- Modify: `modules/factor/internal/store/database_test.go`

- [ ] **Step 1: 写启用合同测试**

enabled Binding 必须一次性验证：

- source Dataset 存在、active 且为 TimeSeries；
- active View 存在且投影全部 `input_columns`；
- frequency 可解析且大于零；
- `source_dataset != target_dataset`；
- source Dataset 不是 `dataset_role=factor_result`。

disabled 草稿不依赖远端 Storage。

- [ ] **Step 2: 写定义更新状态测试**

enabled Factor 的源码、input columns、params、`lookback_periods` 更新全部拒绝。唯一
流程是 `disable -> update/import -> enable -> RecalcFactor`。CLI import 只创建或
更新 disabled 定义，不同步运行期 metadata。

- [ ] **Step 3: 复用具体 registry service**

RPC 和 CLI 调用同一个已有 `registry.Service` 的具体方法，不新增通用 application
framework。`SetFactorStatus` 继续是唯一启用入口，远端 metadata 同步成功后才落
enabled。

- [ ] **Step 4: 全连接启用 SQLite 外键**

在 SQLite DSN 使用连接级参数，例如 `_foreign_keys=on`，并通过 `SetMaxOpenConns`
后的多连接测试证明每条连接都拒绝孤儿 Binding；不要只执行一次 `PRAGMA`。

- [ ] **Step 5: 验证并提交**

```bash
(cd modules/factor && \
  go test ./internal/domain ./internal/registry ./internal/rpc \
    ./internal/store ./cmd/cli -count=1)
git add modules/factor
git commit -m "fix(factor): validate bindings and freeze enabled definitions"
```

### Task 15: 删除 Python worker 启动预加载

**Files:**
- Modify: `modules/factor/pyworker/worker.py`
- Modify: `modules/factor/pyworker/test_worker.py`
- Modify: `packages/pyruntime/process/worker.go`
- Modify: `packages/pyruntime/process/worker_test.go`
- Modify: `packages/pyruntime/process/supervisor.go`
- Test: `packages/pyruntime/process/supervisor_test.go`

- [ ] **Step 1: 写草稿隔离失败测试**

在 factors 目录放置 disabled 草稿：

- import 时死循环；
- `os._exit(1)`；
- 顶层 `print()`；
- 语法错误。

启动 worker 的 HELLO 必须全部成功，因为启动不扫描该目录。只有显式 LOAD 对应文件
时才返回该文件的错误。

- [ ] **Step 2: 删除 glob/import preload**

worker 启动只完成协议初始化。任务按 immutable `SourcePath + SourceHash` LOAD。
导入阶段用 redirect 捕获 stdout/stderr，并把诊断放入结构化错误帧，不能污染 stdout
帧流。

- [ ] **Step 3: 验证 Supervisor 边界**

HELLO/LOAD/RUN/写阻塞/超时失败后仍由 Supervisor `Kill + Wait`、清空引用，并在后续
请求重建。不要在 Supervisor 增加业务任务重放；Scheduler 的有限 retry 是唯一任务
重试层。

- [ ] **Step 4: 验证并提交**

```bash
(cd modules/factor && \
  PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
    python -m pytest pyworker -q)
(cd packages/pyruntime && go test ./... -count=1)
(cd packages/pyruntime && go test -race ./... -count=1)
git add modules/factor/pyworker packages/pyruntime
git commit -m "fix(pyruntime): load only requested factor sources"
```

---

## 5. 跨模块验收、重建与合并

### Task 16: 更新活跃文档并清除旧术语

**Files:**
- Modify: `docs/superpowers/specs/2026-07-29-time-series-series-tag-design.md`
- Modify: `docs/存储层架构.md`
- Modify: `docs/存储概念与设计意图.md`
- Modify: `docs/协议设计.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/数据库管理.md`
- Modify: `docs/存储目标架构与元数据.md`
- Modify: `docs/量化金融数据概念.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`
- Modify: `docs/采集任务管理.md`
- Modify: `docs/量化初始元数据设计.md`
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/行情数据归档模块设计.md`
- Modify: `docs/主机监控架构设计.md`
- Modify: `docs/内置市场行情采集架构.md`
- Modify: `modules/storage/README.md`
- Modify: `modules/factor/README.md`
- Modify: `modules/cli/README.md`
- Modify: `modules/factor/examples/run-once/README.md`
- Modify: `examples/data/kline/README.md`
- Modify: `examples/README.md`
- Modify: `examples/e2e/README.md`

- [ ] **Step 1: 把“待实施”标记改为“已实现”**

只有对应 E2E 全部 PASS 后才能修改状态。文档必须一致说明：

- 单 scalar tag；
- empty exact 与 absent wildcard；
- Dataset 没有 `series_tag_name`；
- Archive Parquet v2；
- Factor 单任务单 Factor、跨 tag、`lookback_periods`；
- 旧数据全部清理重建。

- [ ] **Step 2: 扫描活跃树**

Run:

```bash
rg -n "dimensions|Dimensions|dimensions_json|dimension_name|dimension_value|lookback_rows|crypto_binance|crypto_okx|rows\\.upserted\\.v1|Schema v5|SQLite v5|(?i:dataset).*(binance|okx)_(spot|perpetual)_kline" \
  modules packages web examples \
  docs/存储层架构.md docs/存储概念与设计意图.md docs/存储目标架构与元数据.md \
  docs/协议设计.md docs/架构总览.md docs/数据库管理.md \
  docs/量化金融数据概念.md docs/量化初始元数据设计.md \
  docs/因子计算模块设计.md docs/行情数据归档模块设计.md \
  docs/主机监控架构设计.md docs/内置市场行情采集架构.md \
  docs/采集任务管理.md docs/运维/数据保留与磁盘空间.md \
  examples/e2e/README.md \
  --glob '!**/node_modules/**' \
  --glob '!**/dist/**'
```

Expected: 只允许文档中“明确删除/不兼容”的否定说明；代码、Proto、SQL、Web 类型和
活跃示例零残留。不要误删 `EventMessage.tags`，它是事件 envelope metadata，与
TimeSeries tag 无关。

- [ ] **Step 3: 提交**

```bash
git add docs modules/storage/README.md modules/factor/README.md modules/cli/README.md \
  modules/factor/examples/run-once/README.md examples/README.md \
  examples/data/kline/README.md examples/e2e/README.md
git commit -m "docs: document scalar time series tags"
```

### Task 17: 执行真实跨模块 E2E

**Files:**
- Create: `scripts/test-series-tag-e2e.sh`
- Modify: `modules/factor/test/storage_e2e_test.go`
- Modify: `modules/archive/test/archive_e2e_test.go`
- Modify: `modules/monitor/test/host_monitor_direct_storage_e2e_test.go`

- [ ] **Step 1: Storage + Factor + Python**

在真实 Storage Primary/View 写入同 Dataset 两个 venue tag。因子以：

```json
{
  "left_tag": "venue:binance",
  "right_tag": "venue:okx",
  "output_tag": "venue_pair:binance-okx"
}
```

计算价差，断言 target Dataset 的 tag、数值、null 清除、历史修正向后窗口和
`complete=false` 重读均正确。

- [ ] **Step 2: Storage + Archive**

消费真实 `DatasetRowsUpserted@2`，断言同一时间点两个 tag 生成两个独立 tag 目录和
两个 Parquet v2 月文件。用独立 reader 检查每个文件的 `series_tag` 为对应常量，
按 `candle_begin_time` 排序且时间唯一；ArchiveFile partition key、本地路径和 COS
object key 均包含同一可逆 tag 编码。

- [ ] **Step 3: Storage + Monitor**

写一个主机同分钟多个 filesystem/disk/network，查询后实体数和值完整，无 RowKey
覆盖。

- [ ] **Step 4: 运行脚本**

```bash
bash scripts/test-series-tag-e2e.sh
```

Expected: 所有真实 Storage/View/Python/Parquet 路径 PASS；脚本自行创建并清理临时
数据目录，不依赖开发机已有数据库。

### Task 18: 全量验证、独立 codeCR 与远端落地

**Files:**
- No planned source changes; only fixes required by verification/review

- [ ] **Step 1: 全量测试**

```bash
(cd modules/storage && go test ./... -count=1)
(cd modules/storage && CGO_ENABLED=1 go test ./... -count=1)
(cd modules/factor && go test ./... -count=1)
(cd modules/factor && \
  go test -race ./internal/trigger/... ./internal/storageio \
    ./internal/scheduler ./internal/rpc ./internal/registry ./internal/store \
    -count=1)
(cd modules/factor && \
  PYTHONPATH="$PWD/../../packages/pyruntime/python" \
  uv run --with-requirements pyworker/requirements.txt \
    python -m pytest pyworker -q)
(cd packages/pyruntime && go test -race ./... -count=1)
(cd modules/archive && go test ./... -count=1)
(cd modules/monitor && go test ./... -count=1)
(cd modules/collector && go test ./... -count=1)
(cd modules/cli && go test ./... -count=1)
(cd web && pnpm test && pnpm exec vue-tsc --noEmit)
bash scripts/test-go-workspace.sh
make verify-pr
```

- [ ] **Step 2: 用 codeCR 做独立审查**

审查重点：

1. selector presence 是否在任一转换层丢失；
2. 同 timestamp 多 tag 是否在排序、分页、Backfill、Archive、Factor chunk 中漏行；
3. 是否残留旧 Map/JSON schema 或兼容路径；
4. event v1/v2 是否可能混跑；
5. Factor 输出是否能伪造 scope 外 Subject/Dataset；
6. event retry 是否只用于 event task；
7. enabled 定义和 Binding 是否存在绕过校验的写入口。

处理全部 P0/P1 和与本计划相关的 P2，再重跑受影响测试。

- [ ] **Step 3: 执行破坏性重建演练**

在临时部署目录验证：

1. 旧 Metadata v5、Pebble v1、DuckDB dimensions schema、Archive v1、Factor
   `c_lookback_rows` 都明确拒绝启动；
2. 清理后可初始化 Metadata v6；
3. v2 事件、View、Archive 和 Factor 全链路恢复；
4. 文档列出的生产停机顺序可实际执行。

- [ ] **Step 4: 检查提交与工作区**

```bash
git status --short --branch
git log --oneline --decorate -20
git diff feature/mooyang...HEAD --stat
```

Expected: worktree clean；每个任务有独立、可回滚提交；diff 不含未知文件。

- [ ] **Step 5: 合并并推送**

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git status --short --branch
git merge --ff-only feature/time-series-tag
git push origin feature/mooyang
git rev-parse HEAD
git ls-remote --heads origin feature/mooyang
```

Expected: 本地 HEAD 与远端 `refs/heads/feature/mooyang` 完全相同。只有完成远端 SHA
核对，才可报告实施完成。

---

## 6. 最终验收清单

- [ ] TimeSeries 全链路只有一个 `series_tag`，无 Dimensions Map 或 tag 数组。
- [ ] 空 exact tag 与 absent selector 的语义有单测和 E2E。
- [ ] Pebble、DuckDB、事件、Parquet 和 Factor 写回使用同一完整行身份。
- [ ] View/Backfill/分页按包含 tag 的唯一全序，不漏同 timestamp 行。
- [ ] Archive v2 按 tag 拆分月目录和文件，路径、ArchiveFile、COS 与 Parquet 常量
      列中的 tag 一致，旧 schema 明确拒绝。
- [ ] Binance/OKX 同 Dataset 价差因子真实跑通。
- [ ] `lookback_periods` 按不同 `data_time` 计数。
- [ ] 历史修正扩展后续 period，View incomplete 有限重读。
- [ ] disabled Python 草稿不会在 worker 启动时 import。
- [ ] Binding 启用合同、环限制、enabled 热更新限制与 SQLite FK 均生效。
- [ ] 未引入 DAG、持久化调度、Exactly-once 或通用 tag registry。
- [ ] 模块测试、race、CGO DuckDB、真实 E2E、workspace verify 和 codeCR 全部通过。
- [ ] `feature/mooyang` 本地与远端 SHA 一致。
