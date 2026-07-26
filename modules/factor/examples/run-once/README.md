# Factor Run-Once Verification

Storage Gateway、source Dataset 与 Factor target Dataset 需要已经可用。Python 环境需安装
`pyworker/requirements.txt`。

```bash
cd modules/factor
go run ./cmd/cli init --db ./data/factor/factor.db
go run ./cmd/cli import \
  --db ./data/factor/factor.db \
  --factors-dir ./factors \
  --default-periods 20,96
go run ./cmd/cli run-once \
  --space crypto \
  --dataset binance_spot_kline \
  --subject BTC-USDT \
  --freq 1m \
  --start-time 2026-07-26T00:00:00Z \
  --end-time 2026-07-27T00:00:00Z
```

命令同步完成整个 `[start_time,end_time)` 范围；超过 2000 个目标 bar 自动分块。
terminal JSON 的 `elapsed_ms` 是完整读取、计算和写回耗时。
