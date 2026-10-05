# MooX Factor

`moox-factor` manages factor definitions, factor sets (计算任务) and their members, and runs the dataset-driven factor period
pipeline in one process. It consumes `CollectorPeriodCompleted`, reads source rows through
Storage PrimaryStore, writes carried source values plus factor outputs to a `factor_result`
Dataset, and reports `FactorPeriodComputed`. Storage maintains the default result View;
View is a query index, not an input to Factor computation.

The architecture and RPC/data contracts are documented in
[`docs/因子计算模块设计.md`](../../docs/因子计算模块设计.md) and the canonical
[design](../../docs/superpowers/specs/2026-10-04-factor-dataset-period-pipeline-design.md).

## Build And Run

```bash
./scripts/build/build.sh factor
./bin/moox-factor
```

The service reads `./config/app.yaml`. The config sections are `database`, `storage`,
`eventbus`, `python`, `pipeline`, and `recalc`. Python defaults are in
`modules/factor/config/app.yaml`; deployment supplies Storage gateway and EventBus credentials.

## CLI

```bash
./bin/moox-factor-cli init --db ./data/factor/factor.db
./bin/moox-factor-cli import \
  --config ./config/app.yaml \
  --file ./factors/Bias.py --factor-id bias \
  --inputs close --outputs bias_20 --params '{"window":20}' --lookback 20
./bin/moox-factor-cli import-catalog --dir ./factors
./bin/moox-factor-cli recalc --set fset_binance_kline_1m \
  --start 2026-10-04T00:00:00Z --end 2026-10-04T01:00:00Z
./bin/moox-factor-cli run-once --set fset_binance_kline_1m \
  --period 2026-10-04T00:10:00Z
./bin/moox-factor-cli status --target ip://127.0.0.1:11004
```

`import` creates a standalone definition: the algorithm, parameters and input/output column
names only, with no factor set and no run state. Adding `--set <set_id>` also attaches it to that
factor set as a disabled member after validating it against the set's source dataset. Enabling a
member (which adds the result columns and submits the backfill) is done via FactorMgr
`SetFactorMemberStatus`. `recalc` submits an asynchronous range job;
`run-once` invokes one period directly for local diagnosis. Supported commands are `init`,
`import`, `import-catalog`, `recalc`, `run-once`, and `status`.

## XBX Factor Catalog

The catalog manifest and ordinary Python factors live under `modules/factor/factors/`.
`import-catalog` imports the definitions only (add `--set` to also attach them as disabled
members); a member runs only after it is explicitly enabled in a factor set. Every definition states its complete input columns, outputs, params, and maximum
lookback. Factor does not infer OHLCV dependencies or depend on another Factor's output.

## Operational Signals

The JetStream durable consumer is `factor_collector_period_v1`. Its lag represents unprocessed
Collector periods; restart recovery is provided by JetStream redelivery. Inspect Factor's
Prometheus metrics: `factor_period_total`, `factor_period_duration_seconds`,
`factor_period_lag_seconds`, `factor_failures_total`, `factor_lane_backlog`,
`factor_python_busy`, and `factor_last_period_time`.

Do not clear a Factor consumer to recover it. Diagnose durable lag, set-lane backlog, Storage
errors, and Python worker saturation; use an explicit Recalc job for historical correction.
