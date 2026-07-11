# Stock US Provider Addendum

> Status: blocked by US-1 account, licensing and SCF evidence. Review this addendum before Provider code is added.

## Selected IDs And Secrets

- Primary Provider ID: `alpaca_sip`
- Fallback Provider ID: `massive_sip`
- Alpaca credentials: `MOOX_ALPACA_API_KEY_ID`, `MOOX_ALPACA_API_SECRET_KEY`
- Massive credential: `MOOX_MASSIVE_API_KEY`
- Secrets are resolved at runtime and must never appear in manifests, readiness locks, logs or JobItem params.

## Exact Files

Modify:

- `modules/collector/config/markets/stock_us/market.yaml`
- `modules/collector/config/markets/stock_us/metadata.seed.yaml`
- `modules/collector/config/markets/stock_us/provider-validation.yaml`
- `modules/collector/internal/builtin/catalog.go`
- `modules/collector/internal/markets/stockus/module.go`
- `modules/collector/internal/markets/stockus/module_test.go`

Create after approval:

- `modules/collector/internal/providers/alpaca/provider.go`
- `modules/collector/internal/providers/alpaca/provider_test.go`
- `modules/collector/internal/providers/alpaca/testdata/*.json`
- `modules/collector/internal/providers/massive/provider.go`
- `modules/collector/internal/providers/massive/provider_test.go`
- `modules/collector/internal/providers/massive/testdata/*.json`
- `modules/collector/internal/markets/stockus/integration_test.go`
- `modules/collector/test/stock_us_market_e2e_test.go`

## Endpoints

Alpaca:

- `GET https://data.alpaca.markets/v2/stocks/bars`
- `GET https://data.alpaca.markets/v2/stocks/{symbol}/bars`
- Approved reference-asset endpoint selected during US-1; do not infer the universe from observed bars.
- Every request explicitly sends `feed=sip`, bounded `limit`, RFC3339 start/end and consumes `next_page_token`.

Massive:

- `GET https://api.massive.com/v3/reference/tickers`
- `GET https://api.massive.com/v2/aggs/ticker/{ticker}/range/{multiplier}/{timespan}/{from}/{to}`
- Every aggregate request sends `adjusted=false`, `sort=asc`, bounded `limit` and follows `next_url` without copying credentials from one URL to another.

## Dataset And Capability Contract

Add source datasets `alpaca_sip_kline`, `alpaca_sip_instruments`, `massive_sip_kline` and `massive_sip_instruments`. Unified datasets remain `equity_kline`, `etf_kline`, `index_kline`, `instruments`, `calendar`, `market_coverage` and `kline_quality_event`.

- Equity/ETF price currency: USD.
- Volume unit: shares.
- Amount is absent unless the Provider returns a documented notional value; never synthesize amount from VWAP or close.
- `feed_scope` is `sip_consolidated` only when the response was actually requested and entitled as SIP.
- Daily `data_time` is America/New_York local midnight represented in UTC; close time follows the registered session calendar.
- Minute buckets use Provider timestamps but must align to America/New_York session buckets across EST and EDT.
- No adjusted prices enter first-stage datasets.
- Index remains disabled until a separately licensed primary and fallback both pass US-1.

## Fixtures And Tests

Fixture tests must cover:

1. Empty page, partial page, final page and repeated pagination token.
2. 401/403 as permanent credential/entitlement errors; 429 with retry metadata; 5xx/timeout as temporary.
3. Whole-row decimal normalization and absent amount.
4. Split-date samples proving unadjusted behavior.
5. 2026-03-08 and 2026-11-01 DST boundaries, regular sessions and extended-hours exclusion.
6. Same-timestamp multiple Subjects and deterministic request IDs.
7. RequestGate immediately before every physical request.
8. Provider packages importing neither Storage nor control-plane packages.

The module E2E test must use actual Pebble through `modules/storage/testkit`, write source before unified, persist quality events and coverage, exercise fallback, and prove idempotent retry.

## Activation Sequence

1. Approve provider/license choice and this addendum.
2. Implement fixture-driven Providers while `runtime_enabled=false`.
3. Run live probes from target SCF and write sanitized evidence with a bounded `valid_until`.
4. Enable only the proven equity/ETF frequency cells; leave index disabled if unresolved.
5. Generate a new readiness lock and SCF package.
6. Run `scripts/test-market-manifest-release.sh` and the full local matrix.
7. Publish `moox-collector-stock-us-scf` only through `collector function publish-markets`.
8. Run `scripts/verify-market-remote.sh` on disposable ranges and retain the JSON evidence.
9. Commit activation separately as `feat(collector): activate qualified US market providers`.

Any failed entitlement, license, adjustment, quota, DST or SCF probe returns the module to `not_ready`; metadata registration remains enabled.
