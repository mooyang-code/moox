# Storage 职责与边界

当前 `modules/storage` 有 primary、node、view 三个角色；外部 RPC 入口是独立的 `modules/access`。

- SQLite：primary 保存空间、标的、标签、数据集、字段和 View 元数据。
- Pebble：node 保存字段级时序/记录事实、Outbox、采集周期账本与行变更日志。
- DuckDB：view 保存可重建的时序宽表；每个 View 一个物理索引：首次从 Primary 回填建成，之后原地加列、定时删除最旧数据。
- Bleve：view 为记录数据提供全文索引。

归档由独立的 `modules/archive` 定时从 Primary 读取，worker 用 SQLite 暂存并导出 CSV（默认）或 CSV.gz。当前运行时不生成 Parquet；不能把 Parquet 当作已实现的归档或恢复入口。

时序行情与因子结果共享逻辑行身份：`space + dataset + subject + freq + data_time + series_tag`。
`series_tag` 是可选的不透明字符串。因子输出是结果数据集的列；定义 ID、成员键、定义哈希属于列元数据与周期标记，
实际参数保存在行内 `__params` JSON 列，不属于时序主键。当前因子结果写入不携带行属性。

Primary 的保留期按空间、频率解析；时序 View 默认以每序列最近 10000 根为整理目标，超过 12000 根触发整理，容量阈值默认 4GiB。View 另按 `[storage_view.max_age]` 过滤过旧的写入：默认 1m 为 7d（Primary 为 35d），5m 为 35d；股票空间等有覆盖值，完整默认值见[存储文档](../../../docs/模块/存储.md#51-行变更日志)。配置统一生成到 Storage policy。

View 在实时写入、补采、导入及重建回填时丢弃 `data_time < 处理时刻 − max_age` 的行，等于边界保留；整批被丢弃仍返回成功，不影响 Primary 按自身策略保存事实。已有索引不按年龄主动删除或在查询时过滤，重建后实际根数也可能少于配置；`retained_rows_per_series` 是根数配置，不是实际行数保证。年龄过滤计入 `moox_storage_view_expired_rows_dropped_total{space,freq}`。

调整因子回看、Primary 保留期、View 根数和年龄上限时要同时核对。因子常驻引擎从本地时序副本计算，Primary 负责增量同步、历史核对与结果写入；View 不参与因子计算输入读取，其年龄上限也不裁剪因子本地历史。
