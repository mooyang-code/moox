# Protocol Notes

Storage 当前使用 Schema v5 的直接 DataNode 绑定模型：Dataset 直接保存
`data_node_id`，Metadata 保存 DataNode 的一等字段 `service_target`。部署负责注册
DataNode；Doctor 只读检查激活条件，调用方必须显式激活 Dataset，激活后绑定不可变。
管理台在 DataNode 页面展示其全部 Dataset 标签，在 Dataset 页面执行检查、激活和首次
激活前的解绑。当前项目不做旧拓扑迁移。

Use these names in new APIs:

- Space
- DataSource
- Subject
- SubjectSymbol
- Dataset
- DatasetSubject
- Field
- Factor
- View
- StorageDevice
- DataNode
- DataNodeRuntime
- ArchiveFile

Avoid these public API concepts:

- Project
- Workspace as a business domain alias
- Exchange as a public data-source concept
- Instrument as a public subject concept
- object aliases outside SubjectSymbol
- object_id
- partition_key
- DataAddress
- metric as a synonym for data field
- `_time_ms` suffixes

Time fields should be named by meaning:

- `start_time`
- `end_time`
- `snapshot_time`
- `observed_time`
- `updated_time`

`DataRef` describes a logical data location. For time-series data it should include space, dataset, subject, frequency, and the optional scalar `series_tag`. Physical path or table details belong to StorageRoute and storage engine config.

`DataView` is the query composition layer. It can include base fields, factor instances, expressions, and system columns. Query callers should not choose view policy details; the control plane should resolve the active view version and Storage metadata.
