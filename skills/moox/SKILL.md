# MooX Quant Data System

Use this skill when working inside the MooX monorepo, especially for quant data storage, protocol changes, collector integration, factor data, release, or deployment.

## What MooX Is

MooX is the unified repository for a personal quant data platform. It groups storage, collection, factor calculation, account, order, and control-plane modules in one Go workspace.

Core concepts:

- Workspace: an isolated user or strategy domain.
- Exchange: a trading venue or market data venue, such as BINANCE, HKEX, NASDAQ, or OKX.
- Instrument: the internal normalized tradable object.
- InstrumentAlias: external symbols from data sources and exchanges.
- DataSet: a logical kind of data, such as kline, ticker, company profile, ranking, event, or factor values.
- DataView: a query-facing view that can combine base fields, factor instances, expressions, and system columns.
- StorageRoute: a policy that maps a DataSet/DataView to Pebble, DuckDB, CSV, Bleve, or the file-backed acceptance store.

## Repository Layout

- `modules/cli`: `moox-cli`.
- `modules/control`: control plane service and metadata orchestration.
- `modules/storage`: storage service, protocol, adapters, and file-backed quant store.
- `modules/collector`: market data collection. The former miner responsibility is folded into collector discovery/source/scheduler packages.
- `modules/factor`: factor calculation module.
- `modules/order`: order module.
- `modules/account`: account module.
- `docs`: architecture, concept, and protocol documents.
- `scripts`: root build, release, deploy, acceptance, and node_exporter operation scripts.

## Common Commands

From the repository root:

```bash
make test
make build
make acceptance
make release
make deploy
```

Protocol generation:

```bash
make proto
```

CSV acceptance import:

```bash
bin/moox-cli data csv import \
  --storage-root var/storage/acceptance \
  --workspace default \
  --exchange BINANCE \
  --dataset binance_spot_kline_1m \
  --instrument APT-USDT \
  --freq 1m \
  --file ~/Downloads/APT-USDT.csv
```

## Development Rules

- Prefer the new v2 protocol under `modules/storage/proto/*.proto` and `modules/control/proto/*.proto`.
- Keep legacy proto files under `proto/legacy` until their old call paths are deleted.
- Do not reintroduce `object_id` into public APIs. Use Workspace, Exchange, Instrument, DataSet, DataView, Field, FactorDef, and FactorInstance.
- Use `internal_symbol` for normalized symbols and `InstrumentAlias.external_symbol` for source-specific symbols.
- Use `start_time`, `end_time`, and `snapshot_time`; avoid suffixes such as `_ms`.
- Keep `dimensions` as user-defined partition/query dimensions. Do not expose storage-level partition keys to callers.
- Treat Pebble as the online ordered time-series write/read engine. Treat DuckDB as analytical query and versioned wide table materialization. Treat CSV as cold backup/export. Treat Bleve as text search.

See `references/` for more detailed notes.
