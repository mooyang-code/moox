# 协议与命名边界

Storage 当前元数据 Schema 为 **v15**，由 `modules/storage/schema/metadata.sql` 和
`internal/service/metadata/sqlite/store.go` 严格校验。Dataset 直接保存 `data_node_id`，
Metadata 保存 DataNode 的 `service_target`。部署负责注册 DataNode；激活条件由服务端校验，
Doctor 只读诊断。首次激活前可解绑，激活后绑定不可变；不自动迁移旧 Schema。

## 当前模型

- 元数据：Space、DataSource、Subject、Tag、TagMember、Dataset、Field、View、DataNode。
- 因子：全局 Factor 定义；Space 内的 FactorTask、任务成员与结果 Dataset。成员键、结果列名和任务相关 ID 由 `packages/factorname` 推导。
- 归档：ArchiveTask、ArchiveRun、ArchivePartition，由 `modules/archive/proto` 定义。不要恢复旧的 `ArchiveFile` 或事件式归档接口。
- 事实写入使用 `RowKey`：`space_id`、`dataset_id`，以及时序键（`subject_id`、`freq`、`data_time`、`series_tag`）或记录键（`record_id`、`version`）。时序键仍携带 `freq`，其值必须与 Dataset 的固定频率一致。
- 时序读取使用 `TimeSeriesSelector`、`TimeSeriesKey` 等实际协议类型；其中 `freq` 必须与 Dataset 一致。`TimeSeriesSelector.series_tag` 是 optional：省略表示全部序列，显式空值表示默认序列；`TimeSeriesKey.series_tag` 不是 optional，空值就是默认序列。

不要把设计稿中的抽象 `DataRef`、已删除的 `StorageDevice`、`DataNodeRuntime` 或 `ArchiveFile`
当作当前可调用的 Protobuf 类型。确切字段、可选性与分页契约以 `modules/storage/proto/*.proto` 为准。

## 读写职责

`PrimaryStore.ReadRows` 按精确键读取事实；`ReadTimeSeriesRows`、`CountTimeSeriesRows` 面向时序事实，
由 DataNode 的 Pebble 回答，调用方身份不会把读取切换成 View。区间读取使用 `[start_time, end_time)`
和 `page.size` / `page.cursor`，不使用旧 `after_key`。

`DataView` 查询可重建的 View 索引。一个 View 只绑定一个 Dataset；列来源是 Dataset 列、表达式或系统列，
不跨 Dataset 拼宽表，也不直接引用旧因子实例。因子值先作为任务结果写入 Dataset，再由 View 索引。
查询方使用当前激活的索引与列快照，不把待构建定义当作已激活版本。

## 命名

新 API 使用业务域的正式名称，避免 `Project` / `Workspace` 作为 Space 的别名、
`Instrument` 作为 Subject 的别名，以及 `object_id`、`partition_key`、`DataAddress` 等旧概念。

- 标的身份使用 `subject_id`，来源适配层负责转换外部 symbol。
- `series_tag` 是一个可选字符串，不是 map 式维度。
- 时间字段按含义命名（如 `start_time`、`end_time`、`snapshot_time`）；类型和单位以各协议注释为准。
- Factor ID、参数名、多输出名使用小写 `snake_case`；双下划线保留给成员键与列名分段。

完整规则见 [元数据命名规范](../../../docs/元数据命名规范.md)，公开读写契约见 [存储](../../../docs/模块/存储.md)。
