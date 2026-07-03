# Protocol Notes

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
- PrimaryStoreNode
- PrimaryStoreRoute
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

`DataRef` describes a logical data location. It should include space, dataset, data source, subject, frequency, and dimensions. Physical path or table details belong to StorageRoute and storage engine config.

`DataView` is the query composition layer. It can include base fields, factor instances, expressions, and system columns. Query callers should not choose view policy details; the control plane should resolve the active view version and storage route.
