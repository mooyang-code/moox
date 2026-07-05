# Trade Sync Examples

## Timer Service

`modules/trade/config/trpc_go.yaml` registers:

```yaml
- name: trpc.moox.trade.sync.timer
  port: 11209
  network: "0 */5 * * * *?scheduler=tradeSyncSchedule&startAtOnce=1&params=space_id=crypto;sections=balances,positions,orders,trades;window_hours=24;page_size=500;max_symbols=10"
  protocol: timer
  timeout: 60000
```

## Verify Local SQLite

```bash
python3 - <<'PY'
import sqlite3

conn = sqlite3.connect("/home/ubuntu/moox-cloud/trade/data/moox_trade.db")
cur = conn.cursor()
for table in [
    "t_accounts",
    "t_account_balances",
    "t_positions",
    "t_orders",
    "t_trades",
    "t_trade_sync_cursors",
]:
    cur.execute(f"select count(*) from {table}")
    print(table, cur.fetchone()[0])
PY
```

## Expected Semantics

- admin-console and backend API trading requests use MooX trade APIs as the primary write path;
- the timer is reconciliation/compensation, not "exchange-only now, local write later";
- balances and positions are snapshots;
- orders are upserted by deterministic MooX order id;
- trades are append-only and deduplicated by `c_trade_id`;
- cursors let the next timer run continue from the previous successful end time.
