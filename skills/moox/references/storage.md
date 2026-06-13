# Storage Responsibilities

Storage components have different jobs:

- Pebble: online, ordered, low-latency time-series writes and range reads.
- DuckDB: analytical query, dynamic factor exploration, versioned wide table materialization.
- CSV: cold backup, offline export, and disaster recovery data.
- Bleve: text search for documents, announcements, news, notes, and metadata.
- File-backed quant store: development and acceptance implementation for v2 protocol paths.

Factor values should be stored as long-form records keyed by:

- workspace
- exchange
- instrument
- dataset
- time
- factor instance
- dimensions

DuckDB can materialize a DataView into versioned wide tables instead of altering an existing wide table in place. A new view version can add dynamic factor columns without blocking readers of the previous version.
