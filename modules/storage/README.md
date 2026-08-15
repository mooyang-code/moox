# MooX Storage

> scalar `series_tag` 和 Schema v6 已完成代码切换；生产发布与真实跨模块 E2E
> 验收仍按[实施计划](../../docs/superpowers/plans/2026-07-29-factor-runtime-correctness-hardening.md)
> 执行。

MooX Storage 是字段级事实存储和可重建 View 服务。当前实现只有三种进程角色：

| 角色 | 职责 |
| --- | --- |
| `primary` | Metadata SQLite v6、snapshotcache、PrimaryStore 校验与 Dataset 路由 |
| `node` | 单个 DataNode 的 Pebble 字段存储、原子 Outbox、过期时间桶清理 |
| `view` | 单 JetStream Consumer、DuckDB/Bleve ViewIndex、A/B 重建与 DataView 查询 |

Dataset 创建时直接绑定不可修改的 `data_node_id`。

## 数据模型

TimeSeries RowKey：

```text
space_id + dataset_id + subject_id + freq + data_time + series_tag
```

Record RowKey：

```text
space_id + dataset_id + record_id + version
```

- `series_tag` 是一个可选、不透明的标量字符串，空字符串表示默认序列。
- 推荐使用 `venue:binance`、`device:sdb` 等约定，但 Storage 不解析冒号。
- 不支持通用键值 Map、多 tag、额外标签名称元数据或允许值注册；业务属性继续使用
  Field/Attribute。
- 精确 Key 的空 tag 只匹配默认序列；范围 selector 未设置 tag 才表示全部序列。
- `data_time` 接受 RFC3339/RFC3339Nano，服务端统一保存为 UTC 固定 9 位纳秒。
- Record 写入必须提供非空 `version`。
- Record 读取时 version 为空表示读取 UTF-8 字节顺序最大的版本。
- Field 和 Attribute 分别使用 Pebble `0x01`、`0x02` 命名空间。
- 写入是字段级 Upsert，可以新增、覆盖或补写历史 RowKey 的字段。

## 物理存储

### Pebble

TimeSeries 物理键按以下顺序编码：

```text
value_kind | time_series | space | dataset | bucket_start | subject | freq | data_time | series_tag | field
```

Tuple codec 保持 UTF-8 字节顺序并支持 NUL。过期清理以
`(space_id, dataset_id, bucket_start)` 为边界执行 `DeleteRange`，随后异步
Compact。Record 不做自动清理。

一次 DataNode 写入在同一个 Pebble Batch 中提交：

```text
Field / Attribute keys
__outbox/<20 位补零 ID>
__meta/next_outbox_id
```

Outbox ID 使用定长二进制保存。Relay 按 ID 同步发布，失败后停止，不允许后续
消息越过。

### ViewIndex

- TimeSeries View 使用真实 DuckDB 文件。
- 系统列 `series_tag VARCHAR NOT NULL` 参与
  `(subject_id, freq, data_time, series_tag)` 主键和稳定排序。
- Record View 使用真实 Bleve 索引并支持全文搜索。
- 每个 View 使用 A/B 两个独立索引。
- 重建期间实时消息同时写 Active 和 New。
- Backfill 每批最多 10000 行，只填充缺失值，不覆盖实时写。
- DuckDB 默认内存上限为 `256MB`；View 文件大小上限默认为 `1GiB`，需要重建较大 View 时可通过
  `MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT` 调整。
- 每个 DuckDB View 最多打开 4 个连接、保留 1 个空闲连接，允许因子读取与实时写入并行，
  避免读取窗口把结果写入长期堵在单连接队列后。
- View 角色按 `rebuild_check_interval`（重建检查周期）检查 DuckDB 文件大小；超过
  `max_view_file_bytes` 且 View 有限 `keep_duration` 时启动 A/B 重建。新索引只
  backfill 保留窗口，切换完成后按固定 grace 删除旧 DuckDB 文件，不在 active 索引
  上逐行删除。配置统一使用：
  `storage.view.rebuild_check_interval`、`storage.view.rebuild_lookback` 和
  `storage.view.max_view_file_bytes`。每一种重建在激活前都必须覆盖至少
  `rebuild_lookback` 的墙上时钟历史；不足时保持构建中，不发布不完整 View。
- `moox-cli storage force-rebuild-view` 可在确认后删除 View 的 A/B 物理索引、清理
  durable consumer、period/sync checkpoint 状态并从 `DeliverAll` 事件重新建 View。该命令要求显式指定
  `--lookback`，用于一次性覆盖配置；旧 View 数据不可恢复，执行前必须确认 Source
  事件仍在 JetStream 保留窗口内。
- 仅由文件大小触发的容量整理会等待 View consumer 已恢复、总积压不超过
  `storage.view.rebuild_max_pending`（默认 `32`），并连续满足
  `storage.view.rebuild_idle_checks`（默认 `3`）次检查；同一进程同时只运行一个
  容量重建。缺失/损坏 Active、revision 或覆盖范围修复等必要重建不受该门禁阻塞。
- 每次实际重建和容量门禁跳过都会写入 Metadata 的 `t_view_rebuild_logs`。该表是
  重建历史的权威来源，`t_view_index_builds` 只表示当前 CAS 构建状态；相同 View、
  原因和阻塞原因连续跳过时会聚合 `skip_count`，不会按检查周期无限新增行。
- 文件超限重建失败后使用固定 30 分钟的“超限重建重试间隔”，只抑制重复的全量回填尝试；
  Active View 继续提供查询和实时写入，desired revision 变化时立即允许新重建。
- Desired Revision、关联 Dataset 或超过 `2 * keep_duration` 会触发 Reconcile。
- Metadata 激活后，DataView 原子切换 Active 索引；宽限期后删除 OldView。

## EventBus

每个 Dataset 使用一个 Subject：

```text
moox.storage.dataset.rows.upserted.v2.<space-token>.<dataset-token>
```

Token 是可逆的小写无 Padding Base32。View 使用唯一 Consumer
`storage_view`，默认 `Fetch(8)`、`MaxAckPending=8`。View 启动时创建并持有该
Consumer；同一 Dataset 的 rows、Marker 和 SyncPoint 进入同一个 Dataset 队列（队列键为
`space_id + dataset_id`，全文统一称为“Dataset 队列键”，
不同 Dataset 可并行。Active 写失败时保持当前 Delivery 并本地退避；无法恢复的
Subject/Proto/Payload 错误执行 `Term`，避免毒消息永久阻塞全部 Dataset。

## 认证

三个角色都不允许空 secret 或以公开 node ID 作为 secret：

```text
MOOX_STORAGE_NODE_AUTH_SECRET
MOOX_STORAGE_PRIMARY_AUTH_SECRET
MOOX_STORAGE_VIEW_AUTH_SECRET
```

Primary 到 DataNode、View 到 Primary 以及 ViewIndex/DataView RPC 都校验
HMAC。生产部署必须通过敏感配置注入这些值。

## 启动

初始化 Metadata：

```bash
go run ./cmd/cli init \
  --storage-conf config/storage.primary.yaml \
  --schema-path schema/metadata.sql
```

默认业务元数据不由 Storage 部署过程隐式导入。部署并注册 DataNode 后，在仓库根目录使用
`moox-cli setup init --config-dir ./examples/setup/default --storage-host <custom.toml 中主机名>`
同步 Admin 空间、Storage 元数据并显式激活 Dataset。

启动角色时设置：

```bash
MOOX_STORAGE_ROLE=primary
MOOX_STORAGE_ROLE=node
MOOX_STORAGE_ROLE=view
```

当前 Schema v6 中，Primary 只从同一份 Metadata Snapshot 解析
`Dataset.data_node_id -> DataNode.service_target`，不读取路由表、节点 attributes
或环境变量兜底。Dataset 创建后默认为 disabled/unlocked；Doctor 只读检查就绪，
部署或管理员随后显式激活，激活成功后绑定永久锁定。

```text
service_target: ip://127.0.0.1:20107
```

View 使用：

```text
MOOX_STORAGE_METADATA_TARGET=ip://127.0.0.1:20100
MOOX_STORAGE_PRIMARY_TARGET=ip://127.0.0.1:20101
MOOX_STORAGE_EVENTBUS_URL=nats://127.0.0.1:4222
```

## 验证

```bash
cd modules/storage
go test ./...
CGO_ENABLED=1 go test ./...

cd ../..
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
```

DuckDB 运行和测试需要 CGO。no-CGO 构建可以编译 Node/Primary 和 Bleve，但启动
DuckDB View 会返回明确错误。
