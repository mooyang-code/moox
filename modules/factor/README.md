# MooX Factor

The factor module builds two binaries from one Go module:

| Binary | Runs on | Responsibility |
| --- | --- | --- |
| `moox-factor-mgr` | control host, next to Admin | FactorMgr RPC (definitions, factor sets 计算任务, members, recalc jobs, status), the factor SQLite catalog, result-dataset reconciliation, and the internal FactorEngine RPC the engine pulls from |
| `moox-factor-engine` | operator machine (outbound only) | consumes `CollectorPeriodCompleted`, runs the dataset-driven period pipeline and recalc jobs with a Python worker pool, writes carried source values plus factor outputs to the `factor_result` Dataset and reports `FactorPeriodComputed` |

The engine keeps no business state: it pulls a hashed catalog snapshot from the manager on a
UTC-aligned timer (default every minute at second 45), saves it to `data/engine/catalog.json`
and keeps computing with the last snapshot while the manager is unreachable. One engine holds
the manager's lease at a time (heartbeat every 10s). Recalc jobs are pulled with a job lease and
report every chunk. See
[`docs/superpowers/specs/2026-10-05-factor-manager-engine-split-design.md`](../../docs/superpowers/specs/2026-10-05-factor-manager-engine-split-design.md)
and [`docs/因子计算模块设计.md`](../../docs/因子计算模块设计.md).

## Network

| From | To | Protocol |
| --- | --- | --- |
| engine | manager `FactorEngine` via `https://<control>:11001/api/service/factormgr/<Method>` | HTTP + gateway HMAC (caller `factor-engine`) |
| engine | EventBus `tls://<control>:4222` | NATS + factor role credential |
| engine | storage-access `ip://<storage>:11004` | tRPC + storage-access HMAC (principal `factor-engine`) |
| manager | local Storage gateway `ip://127.0.0.1:11003` | tRPC + gateway HMAC (caller `factor`) |

Ports: manager 11403 (FactorMgr tRPC), 11404 (FactorMgr HTTP), 11405 (FactorEngine HTTP), 11414
(health); engine 11417 (health), 11945 (admin), 12945 (metrics).

## Build And Run

```bash
./scripts/build/build.sh factor-mgr      # moox-factor-mgr, moox-factor-mgr-cli
./scripts/build/build.sh factor-engine   # moox-factor-engine (CGO-free)
```

`moox-factor-mgr` reads `config/app.yaml` (`database`, `storage`, `python`, `engine`). `python.bin`
only test-loads factor sources when a definition is created or edited and must import pandas and
numpy. It is deployed by `scripts/deploy/deploy-moox.sh --profile control`.

`moox-factor-engine` reads `config/engine.yaml` (`engine`, `manager`, `catalog_sync`, `storage`,
`eventbus`, `python`, `pipeline`, `recalc`). Install it with
`scripts/deploy/deploy-factor-engine.sh`; see [`docs/ops/factor-engine.md`](../../docs/ops/factor-engine.md).

```bash
moox-factor-engine serve -config config/engine.yaml -conf config/trpc_go.engine.yaml
moox-factor-engine run-once --set fset_dasftksvjhj2jom4vhd0_1m --period 2026-10-04T00:10:00Z
moox-factor-engine health            # signed /readyz with MOOX_HEALTH_AUTH_*
```

## Manager CLI

```bash
./bin/moox-factor-mgr-cli init --db ./data/factor/factor.db
./bin/moox-factor-mgr-cli import \
  --config ./config/app.yaml \
  --file ./factors/Bias.py --factor-id bias \
  --inputs close --outputs bias_20 --params '{"window":20}' --lookback 20
./bin/moox-factor-mgr-cli import-catalog --dir ./factors
./bin/moox-factor-mgr-cli recalc --set fset_dasftksvjhj2jom4vhd0_1m \
  --start 2026-10-04T00:00:00Z --end 2026-10-04T01:00:00Z
./bin/moox-factor-mgr-cli status --target ip://127.0.0.1:11403
```

The CLI edits the manager's SQLite directly on the control host and shares its per-set file locks.
`import` creates a standalone definition; `--set <set_id>` also attaches it as a disabled member.
Enabling a member (adding result columns and submitting the backfill) goes through FactorMgr
`SetFactorMemberStatus`. `recalc` only submits a job; the engine pulls and runs it.

## XBX Factor Catalog

The catalog manifest and ordinary Python factors live under `modules/factor/factors/`.
`import-catalog` imports the definitions only (add `--set` to also attach them as disabled
members); a member runs only after it is explicitly enabled. Every definition states its complete
input columns, outputs, params, and maximum lookback.

## Operational Signals

The engine's JetStream durable consumer is `factor_collector_period_v1`; restart recovery is
JetStream redelivery. Engine metrics: `factor_period_total`, `factor_period_duration_seconds`,
`factor_period_lag_seconds`, `factor_failures_total`, `factor_lane_backlog`, `factor_python_busy`,
`factor_last_period_time`. The manager's `GetStatus.engine` shows whether the engine is online and
whether its catalog matches the manager's.

Do not clear the consumer to recover it. Diagnose durable lag, lane backlog, Storage errors and
Python saturation; use an explicit Recalc job for historical correction.
