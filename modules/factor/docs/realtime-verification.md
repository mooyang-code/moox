# Factor Realtime Verification

## Preconditions

- Storage `eventbus.type` is `nats`.
- NATS is reachable from Storage and Factor, and stream `MOOX_STORAGE` includes `moox.storage.>`.
- `moox-factor` is running from `modules/factor` with access to `factors/`, `sections/`, and its SQLite DB.
- Enabled bindings exist for the source K-line dataset and frequency.

## Live Checks

1. Start Storage, NATS, Admin, and Factor.
2. Import or create factors, then create bindings for `binance_spot_kline` to `binance_spot_factor`.
3. Let collector write new 1m K-line rows.
4. Confirm Factor consumes `moox.storage.time_series.rows_changed.v1`.
5. Confirm Storage receives tail writes in `binance_spot_factor`.
6. Confirm Factor does not loop on its own writes because realtime trigger only whitelists source datasets from enabled bindings.

## Acceptance

- A 500-symbol event storm produces 500 event-batched tasks, not more.
- Scheduler drains deterministic 5ms tasks within one bar budget in test mode.
- Local service logs contain `factor_run_done` lines for success, failed, and superseded terminal states.
