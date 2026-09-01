# stock_cn Monitor Task12 validation

Date: 2026-08-30

Scope: source-level implementation, local tests, and a bounded production
process/readback check. This is not a production acceptance claim.

## Production Readback (2026-08-30)

- `moox-monitor` was running and its metrics endpoint returned HTTP 200.
- The endpoint exposed no `stock_cn`, `collector_market`, or `market_fetch`
  metric lines at readback time. No fresh SCF metrics-consumer observation was
  available, so the Monitor gate remains `PENDING`.
- Storage metadata readback showed 5,550 active subjects for both
  `stock_cn_instruments` and `stock_cn_kline`; CloudNode readback showed the
  stock Timer fleet disabled. These are supporting facts, not Monitor
  freshness evidence.

## Observable signal matrix

| Alert domain from Task12 | Current producer | Monitor consumer | Status |
| --- | --- | --- | --- |
| K-line no-data, stale closed buckets, missing source fields, invalid OHLCV, calendar warning/expiry | Storage `stock_cn_kline` rows | `watchdog.MarketCanary.runStockCN` | Closed for configured canary subjects. The check is session-aware, treats lunch/closed days as idle/healthy, requires the configured percentage of the latest closed bars, and emits explicit reasons. It is a canary, not proof of 99% coverage across every active A-share instrument. |
| Group/function count and Timer capacity | Collector `Reconciler` calls `ObserveConfiguredGroups`, `ObserveConfiguredGroupID`, `ObserveTimerCapacity`, assignment and trigger observers | `observability.Builder.buildMarketFetchCoordination`, then `buildBusinessFreshnessReporter` persists an external check/result and default alert rule | Closed for count/capacity/trigger facts. A `stock_cn` configured-group count or per-Group identity mismatch now produces a down result; Group IDs remain a bounded label. |
| Provider HTTP 429, system failure rate, local rate-limit deadline exhaustion, fallback/no-candidate | `moox_collector_market_feed_results_total` | SCF common Handler and KlinePipeline observe outcomes and perform an optional one-shot EventBus report; Monitor aggregates fresh observations by provider over the configured window | Closed in source. Production status remains pending until the SCF EventBus environment is configured and a real failed/fallback invocation is observed. |
| Instrument snapshot age, completeness, active count, exchange coverage | Instrument metric families | SCF InstrumentPipeline observes the complete/incomplete/stale result and reports it through the same one-shot metrics path; Monitor checks age, active lower bound and required exchanges | Closed in source. Production status remains pending until a real complete snapshot is reported and read back. |
| Egress result/non-empty/distinct IP count | `moox_collector_market_egress_functions` | CLI may publish the bounded probe result when metrics EventBus is configured; it is diagnostic only and is not consumed as a Timer/Rule health gate. |

The Monitor consumer deliberately does not synthesize success or failure from a missing metric family. A production deployment must explicitly configure `MOOX_SCF_METRICS_EVENTBUS_URL` (and, when required, `MOOX_SCF_METRICS_EVENTBUS_CREDENTIAL_FILE`) in the SCF publish configuration; CloudNode maps those scoped inputs to the function-only `MOOX_METRICS_EVENTBUS_URL` and `MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE`. Host process values are never inherited, and without the explicit SCF-scoped configuration the source-level reporter is intentionally disabled.

## Configured thresholds

- `market_canary.closed_bar_count: 3`
- `market_canary.closed_bar_min_coverage: 0.99`
- `market_canary.post_close_delay: 1m`，避开最晚 Group 的错峰触发和写入延迟；随后在 `freshness` 窗口内检查当日 14:59 桶
- `market_canary.calendar_warning_lead: 336h`
- `market_health.timer_coordination_stale_after: 15m`
- `market_health.timer_coordination_pending_grace: 5m`
- `market_health.low_capacity_headroom: 2`

The values are loaded and validated by `internal/config`; production Monitor builders receive the market-health thresholds rather than using fixed values.

## Local verification

```text
go test -race -count=1 ./internal/watchdog ./internal/config ./internal/observability ./internal/bootstrap
PASS
```

Production call-site audit:

```text
ObserveConfiguredGroups: called by modules/collector/internal/marketfetch/reconciler.go
ObserveFeedResult: `modules/collector/internal/marketfetch/kline_pipeline.go` and the common SCF Handler reporter
ObserveInstrumentSnapshot: `modules/collector/internal/marketfetch/instrument_pipeline.go` and both SCF composition roots
ObserveEgressDiagnostic: `modules/cli/internal/command/collector.go` optional probe metric
```
