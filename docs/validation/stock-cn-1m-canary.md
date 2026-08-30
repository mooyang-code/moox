# stock_cn 1m Canary

Status: `NO-GO`. Formal SCF packages were built and the production CloudNode
fleet was exercised, but the all-node egress gate and Monitor runtime evidence
did not pass. The stock Rule and all Timer nodes remain disabled.

## Production Snapshot

- Package `stock-cn-1m-20260830-r30` was formally compiled into the SCF zip
  (`collector-scf-stock_cn-stock-cn-1m-20260830-r30.zip`). The final publish
  attempt was fail-closed during the full Instrument canary; only the
  Singapore Invoke node reached that package. The last completed fleet deploy
  was `stock-cn-1m-20260830-r24`, with 200 configured Timer nodes across six
  regions and one Invoke node per region.
- The r24 full Instrument canary completed all 32 deterministic shards through
  SCF, Gateway, and Storage. Storage readback currently has 5,550 active
  subjects in both `stock_cn_instruments` and `stock_cn_kline`; the
  `amount_quality` column is active.
- CloudNode readback confirms every configured stock Timer is disabled
  (`timer_actual_enabled=0`). A legacy Singapore Timer from an earlier package
  remains in the catalog but is also disabled.
- The real bounded stock K-line canary for `600000.XSHG` reached SCF, Gateway,
  and Storage successfully after the metadata schema migration. A durable
  time-series row query was not completed, so this does not close the Storage
  acceptance gate.
- The r24 all-node egress gate did not pass: Singapore had 2/36 successful
  strict probes, Guangzhou 0/13, Shanghai 0/38, Beijing 0/38, Chengdu 0/38,
  and Tokyo 36/37. Therefore `result_count`, non-empty IP count, and distinct
  IP count were not all equal to the configured `N=200`. The newer identity
  probe is in source, but no successful all-200 production run has been
  recorded.
- The completed r26/r27/r29 release attempts were fail-closed by, respectively,
  an EventBus completion timeout, a transient EastMoney instrument response,
  and a Sina instrument response timeout. No Timer or Rule was enabled by any
  attempt.

## Required Evidence

| Check | Result | Evidence |
| --- | --- | --- |
| Provider probe: Sina/Tencent/EastMoney complete 1m OHLCV | `PARTIAL / NO-GO` | [stock-cn-provider-validation.md](stock-cn-provider-validation.md): this rerun saw Sina SH/SZ pass but XSHG timeout (an earlier run covered XSHG/XBSE); Tencent covers SH/SZ only; EastMoney timed out; Baidu protocol-invalid |
| Provider history probe with explicit start | `PARTIAL` | [stock-cn-provider-validation.md](stock-cn-provider-validation.md): Sina and Tencent SH history returned 240 bars; EastMoney/Baidu unavailable |
| Per-provider rate probe and 429 behavior | `PARTIAL` | [stock-cn-provider-validation.md](stock-cn-provider-validation.md): Sina/Tencent probes had no 429 at test concurrency; not a production quota approval |
| Full Instrument snapshot, SH/SZ/BSE | `PASS` for r24 | 32/32 shards; Storage active set `5,550` |
| Published Timer functions | `PASS, disabled` | configured `N=200`; CloudNode readback 200 current-fleet Timer nodes |
| Distinct egress IPs | `NO-GO` | r24 strict gate had 162 failed probes; equality gate not met |
| Calendar and Timer trigger readback | `PARTIAL` | CloudNode readback confirms all Timer actual-enabled flags are `0` |
| Three-symbol 1m canary write | `PARTIAL` | `600000.XSHG` bounded SCF canary succeeded; durable row query pending |
| Provider fallback / rate-limit drill | `PENDING` | batch ID and source_provider |
| Historical K-line query | `PENDING` | symbol, interval, row count, earliest/latest |
| Gap audit and rollback drill | `PENDING` | audit/rollback IDs |

## Commands

Run from the repository root with a populated local `custom.toml` and
short-lived credentials supplied through the process environment. Do not put
secrets in this document or in command output.

```bash
./bin/moox-cli collector function publish submit --file ./custom.toml --space-id stock_cn --control-url "$MOOX_CONTROL_URL" > stock-cn-publish.json
./bin/moox-cli collector function probe-egress --control-url "$MOOX_CONTROL_URL" --space-id stock_cn --file ./custom.toml --service-access-key "$MOOX_SERVICE_ACCESS_KEY" --service-secret-key "$MOOX_SERVICE_SECRET_KEY" > stock-cn-egress-probe.json
./bin/moox-cli data kline get --config ./config/data-access-stock-cn.yaml --data-type stock_cn --exchange stock_cn --symbol 600000.XSHG --interval 1m --limit 10
```

The Rule and Timer must remain disabled if any required check is not backed by
the corresponding production response and Storage readback.

The provider probe was run on `2026-08-30` against representative SH, SZ and
BSE subjects. The redacted JSON evidence is
[stock-cn-provider-probe-20260830.json](stock-cn-provider-probe-20260830.json).
It remains an active-source diagnostic only: Sina is the only complete
Instrument source currently enabled in the release route, Tencent covers SH/SZ
K-line requests, EastMoney is unstable, and Baidu is protocol-invalid.

The stock deployment canary sends a bounded historical `market_fetch` item. Its
initial start is kept below the active providers' 24-hour page capability; the
stock runtime then resolves the latest closed 1m session from the packaged
trading calendar and replays that exact bucket on weekends and holidays. This
calendar-aware replay is limited to the deployment canary and does not weaken
the normal `HistoryPolicy` fail-closed boundary. The Monitor canary also checks
the final 14:59 bucket during the short post-close grace window before treating
the market as idle.
