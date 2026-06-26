---
name: moox
description: Use when working in the MooX monorepo or operating moox-cli, including quant storage, collector cloud functions, deployment, Tencent Cloud Lighthouse firewall changes, or control-plane maintenance.
---

# MooX Quant Data System

Use this skill when working inside the MooX monorepo, especially for quant data storage, protocol changes, collector integration, factor data, release, or deployment.

## What MooX Is

MooX is the unified repository for a personal quant data platform. It groups storage, collection, factor calculation, account, order, and control-plane modules in one Go workspace.

Core concepts:

- Space: an isolated user or strategy domain.
- DataSource: a concrete upstream source, such as BINANCE spot, OKX spot, HKEX, NASDAQ, or a custom feed.
- Subject: the business object stored in a Space, such as APT-USDT, a stock, a ranking item, or a document subject.
- DataSet: a source-bound logical kind of data, such as kline, ticker, company profile, ranking, event, or factor values.
- Field and Factor: Space-scoped reusable column definitions selected by DataSet columns.
- View: a query-facing, asynchronously built wide view over one primary DataSet and selected columns from related DataSets.
- StorageRoute: a policy that maps online primary facts to PrimaryStore nodes. DuckDB views, Bleve search, and Parquet archive are derived asynchronously from primary fact changes.

## Repository Layout

- `modules/cli`: `moox-cli`.
- `modules/control`: control plane service and metadata orchestration.
- `modules/storage`: storage service, protocol, access, primary store, view, search, archive, and device drivers.
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

## MooX CLI Operations

Prefer bundled scripts in this skill when a workflow needs deterministic parsing or repeated `moox-cli` argument assembly.

### Tencent Lighthouse Firewall

When the user provides a Tencent Cloud Lighthouse instance detail URL, use the bundled script to parse the instance ID and call `moox-cli`:

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add \
  --detail-url 'https://console.cloud.tencent.com/lighthouse/instance/detail?searchParams=rid%3D5&rid=1&id=lhins-a7yikq89' \
  --ports 19104,19101,19105,20103,20180 \
  --dry-run
```

After the dry run is correct, remove `--dry-run` to create the firewall rule. The script defaults to the MooX service ports `19104,19101,19105,20103,20180`, `TCP`, `0.0.0.0/0`, and `ap-guangzhou` when the console URL does not provide a region.

Useful safe checks:

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py parse --detail-url '<console-detail-url>'
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add --detail-url '<console-detail-url>' --print-command
```

The script calls `bin/moox-cli` from the repository when present, or `moox-cli` from `PATH`. Tencent credentials should be supplied through `TENCENTCLOUD_SECRET_ID` and `TENCENTCLOUD_SECRET_KEY`; do not echo secrets in final responses or logs.

CSV acceptance import:

```bash
bin/moox-cli data csv import \
  --storage-url http://127.0.0.1:19104 \
  --space default \
  --data-source BINANCE \
  --dataset binance_spot_kline_1m \
  --subject APT-USDT \
  --freq 1m \
  --file ~/Downloads/APT-USDT.csv
```

## Development Rules

- Prefer the new protocol under `modules/storage/proto/*.proto` and `modules/control/proto/*.proto`.
- Keep legacy proto files under `proto/legacy` until their old call paths are deleted.
- Do not reintroduce `object_id` into public APIs. Use Space, DataSource, Subject, DataSet, View, Field, and Factor.
- Use `subject_id` for normalized subject identity and `SubjectSymbol.external_symbol` for source-specific symbols.
- Use `start_time`, `end_time`, and `snapshot_time`; avoid suffixes such as `_ms`.
- Keep `dimensions` as user-defined partition/query dimensions. Do not expose storage-level partition keys to callers.
- Treat Pebble-backed PrimaryStore as the online ordered fact store. Treat DuckDB as analytical query and versioned wide view storage. Treat Parquet as cold archive. Treat Bleve as text search.

See `references/` for more detailed notes.
