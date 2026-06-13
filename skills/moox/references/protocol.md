# Protocol Notes

Use these names in new APIs:

- Workspace
- Exchange
- Instrument
- InstrumentAlias
- DataSet
- Field
- FactorDef
- FactorInstance
- DataView
- StorageDevice
- StorageRoute
- CollectorDataSetBinding

Avoid these public API concepts:

- Project
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

`DataRef` describes a logical data location. It should include workspace, dataset, exchange, instrument, frequency, and dimensions. Physical path or table details belong to StorageRoute and storage engine config.

`DataView` is the query composition layer. It can include base fields, factor instances, expressions, and system columns. Query callers should not choose view policy details; the control plane should resolve the active view version and storage route.
