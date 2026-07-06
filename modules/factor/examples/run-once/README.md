# Factor Run-Once Verification

## Prerequisites

- Storage is running and exposes Metadata tRPC on `127.0.0.1:20100`.
- Storage is running and exposes Access tRPC on `127.0.0.1:20102`.
- The source K-line Dataset exists and has rows for the requested symbol/frequency/bar time.
- The registry sync step in `run-once` can create the result Dataset and factor result columns before writeback.
- Python can import `pandas` and `numpy` for `pyworker/worker.py`.

## Commands

Run from the factor module directory:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/factor
go run ./cmd/cli init --db ./data/factor/factor.db
go run ./cmd/cli import --db ./data/factor/factor.db --factors-dir ./factors --default-params 20,96
go run ./cmd/cli run-once --space crypto --dataset binance_spot_kline --subject BTC-USDT --freq 1m --bar-time 2026-07-06T09:15:00Z
```

## Acceptance

- `t_factor_runs` contains one `succeeded` row.
- Storage `binance_spot_factor` has `Bias_20` and `Bias_96` values for the requested tail bars.
- After View columns are added, Storage View can join K-line rows with the factor result Dataset.
