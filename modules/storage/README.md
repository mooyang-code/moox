# MooX Storage

MooX Storage 是字段级事实存储和可重建 View 服务。当前实现只有三种进程角色：

| 角色 | 职责 |
| --- | --- |
| `primary` | Metadata SQLite v4、snapshotcache、PrimaryStore 校验与 Dataset 路由 |
| `node` | 单个 DataNode 的 Pebble 字段存储、原子 Outbox、过期时间桶清理 |
| `view` | 单 JetStream Consumer、DuckDB/Bleve ViewIndex、A/B 重建与 DataView 查询 |

旧的分片执行链、RowsCommitted、Sequence Progress 和
`legacy_storage` 构建已经删除。Dataset 创建时直接绑定不可修改的
`data_node_id`。

## 数据模型

TimeSeries RowKey：

```text
space_id + dataset_id + subject_id + freq + data_time
```

Record RowKey：

```text
space_id + dataset_id + record_id + version
```

- TimeSeries 不包含 Dimensions。业务维度应建模为 Field 或 Attribute。
- `data_time` 接受 RFC3339/RFC3339Nano，服务端统一保存为 UTC 固定 9 位纳秒。
- Record 写入必须提供非空 `version`。
- Record 读取时 version 为空表示读取 UTF-8 字节顺序最大的版本。
- Field 和 Attribute 分别使用 Pebble `0x01`、`0x02` 命名空间。
- 写入是字段级 Upsert，可以新增、覆盖或补写历史 RowKey 的字段。

## 物理存储

### Pebble

TimeSeries 物理键按以下顺序编码：

```text
value_kind | time_series | space | dataset | bucket_start | subject | freq | data_time | field
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
- Record View 使用真实 Bleve 索引并支持全文搜索。
- 每个 View 使用 A/B 两个独立索引。
- 重建期间实时消息同时写 Active 和 New。
- Backfill 每批最多 100 行，只填充缺失值，不覆盖实时写。
- Desired Revision、关联 Dataset 或超过 `2 * keep_duration` 会触发 Reconcile。
- Metadata 激活后，DataView 原子切换 Active 索引；宽限期后删除 OldView。

## EventBus

每个 Dataset 使用一个 Subject：

```text
moox.storage.fields_changed.v1.<space-token>.<dataset-token>
```

Token 是可逆的小写无 Padding Base32。View 使用唯一 Durable
`storage_view`，`Fetch(1)`、`MaxAckPending=1`。Active 写失败时保持当前
Delivery 并本地退避；无法恢复的 Subject/Proto/Payload 错误执行 `Term`，避免
毒消息永久阻塞全部 Dataset。

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

go run ./cmd/cli import-seed \
  --storage-conf config/storage.primary.yaml \
  --schema-path schema/metadata.sql \
  --seed config/metadata.seed.yaml
```

启动角色时设置：

```bash
MOOX_STORAGE_ROLE=primary
MOOX_STORAGE_ROLE=node
MOOX_STORAGE_ROLE=view
```

Primary 从 Metadata Catalog 的 `PrimaryStoreNode.attributes.service_target` 路由到节点：

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
go build -tags legacy_storage ./...

cd ../..
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
```

DuckDB 运行和测试需要 CGO。no-CGO 构建可以编译 Node/Primary 和 Bleve，但启动
DuckDB View 会返回明确错误。
