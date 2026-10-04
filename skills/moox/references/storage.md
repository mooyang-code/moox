# Storage Responsibilities

Storage components have different jobs:

- Pebble: online, ordered, low-latency time-series writes and range reads.
- DuckDB: analytical query, dynamic factor exploration, versioned wide table materialization.
- Parquet: cold archive, offline export, and disaster recovery data.
- Bleve: text search for documents, announcements, news, notes, and metadata.

Time-series and factor-result rows share one logical identity:

- space
- dataset
- subject
- frequency
- time
- optional scalar `series_tag`, such as `venue:binance`

Factor outputs are dataset fields. Factor definition identity and source hash
belong in write attributes, not in the time-series primary key.

DuckDB can materialize a DataView into versioned wide tables instead of altering an existing wide table in place. A new view version can add dynamic factor columns without blocking readers of the previous version.
