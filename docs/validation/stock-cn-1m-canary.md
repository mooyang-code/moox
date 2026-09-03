# stockcn 1m Canary

Status: `PENDING`. Formal SCF packages were built and the production CloudNode
fleet was exercised. The historical all-node egress results below are retained
for diagnosis only; egress is no longer a release gate. Current acceptance
still requires fresh canary, Timer/Rule readback, Monitor, and Storage evidence.

## Production Snapshot

- The current worktree was rebuilt as `stock-cn-1m-20260831-r46` for both the
  Darwin CLI and Linux/amd64 SCF binary. A follow-up formal publish submission
  produced the 10 MB package locally but the control-plane request remained
  established without a response and was interrupted; no r46 package ID or
  deployment result is claimed. The last complete production evidence remains
  r44 below.
- The current architecture correction was compiled as `stock-cn-1m-20260831-r48`
  for both the Darwin CLI and Linux/amd64 SCF binary. It separates the daily
  full-market Instrument snapshot into one dedicated Timer function and keeps
  the fixed-N periodic Kline Timer fleet separate. No r48 package upload,
  CloudNode deployment, Timer readback, or Storage E2E is claimed because the
  formal control-plane session is unavailable in this run.
- Package `stock-cn-1m-20260831-r44` was formally compiled and uploaded to the
  production control plane. Its real Singapore Invoke node passed both the
  bounded stock K-line canary (`600000.XSHG`, `1m`, explicit historical start)
  and the full stock Instrument canary. The latter uses Sina and EastMoney in
  parallel, accepts the verified Sina full catalogue when EastMoney is
  unavailable, and activates the Storage snapshot through the SCF path.
- The r44 publish deployed/confirmed Timer fleets for Singapore (36),
  Guangzhou (13), and Shanghai (38), then stopped at the Beijing Timer batch
  status readback. The control-plane request for job
  `node-batch-e762c83f-af21-4268-bb4c-838bc7138400` timed out after bounded
  retries. Beijing, Chengdu, and Tokyo therefore have no confirmed r44 fleet
  deployment; the release did not reach the egress gate or any Timer enable.
  The stock Rule and all Timer nodes remain disabled.
- The r24 full Instrument canary completed all 32 deterministic shards through
  SCF, Gateway, and Storage. Storage readback currently has 5,550 active
  subjects in both `dataset_stockcn_instruments` and `dataset_stockcn_equity_kline`; the
  `amount_quality` column is active.
- CloudNode readback confirms every configured stock Timer is disabled
  (`timer_actual_enabled=0`). A legacy Singapore Timer from an earlier package
  remains in the catalog but is also disabled.
- The r44 real bounded stock K-line canary for `600000.XSHG` returned success
  from SCF after the metadata schema migration, proving the SCF -> EventBus ->
  Storage write path. A direct durable time-series row query through the local
  Storage tunnel did not return, so this still does not close the independent
  Storage readback gate.
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
- A live Shanghai egress probe against 38 eligible Timer nodes returned 21
  non-empty/distinct public IPs and 17 `api.ipify.org` connection-refused
  failures; this is direct production evidence that the strict N=200 egress
  gate is still not satisfied.
- The current 170-node release was rechecked after changing the egress policy:
  all 170 nodes returned non-empty IPs without probe errors, and 158 IPs were
  distinct. This remains diagnostic evidence only; it does not determine
  Kline Timer or stock Rule activation.

## Required Evidence

| Check | Result | Evidence |
| --- | --- | --- |
| Provider probe: Sina/Tencent/EastMoney complete 1m OHLCV | `PARTIAL / NO-GO` | [stock-cn-provider-validation.md](stock-cn-provider-validation.md): this rerun saw Sina SH/SZ pass but XSHG timeout (an earlier run covered XSHG/XBSE); Tencent covers SH/SZ only; EastMoney timed out; Baidu protocol-invalid |
| Provider history probe with explicit start | `PARTIAL` | [stock-cn-provider-validation.md](stock-cn-provider-validation.md): Sina and Tencent SH history returned 240 bars; EastMoney/Baidu unavailable |
| Per-provider rate probe and 429 behavior | `PARTIAL` | [stock-cn-provider-validation.md](stock-cn-provider-validation.md): Sina/Tencent probes had no 429 at test concurrency; not a production quota approval |
| Full Instrument snapshot, SH/SZ/BSE | `PASS` for r24 and r44 canary | r24: 32/32 shards and Storage active set `5,550`; r44: one full-snapshot SCF canary passed |
| Published Timer functions | `PASS, disabled` | configured `N=200`; CloudNode readback 200 current-fleet Timer nodes |
| Distinct egress IPs | `DIAGNOSTIC ONLY` | Current N=170 probe: 170 results, 170 non-empty IPs, 158 distinct. Historical r24 equality result remains recorded above |
| Calendar and Timer trigger readback | `PARTIAL` | CloudNode readback confirms all Timer actual-enabled flags are `0` |
| Three-symbol 1m canary write | `PARTIAL` | r44 `600000.XSHG` bounded SCF canary succeeded; durable row query pending |
| Provider fallback / rate-limit drill | `PENDING` | batch ID and source_provider |
| Historical K-line query | `PENDING` | symbol, interval, row count, earliest/latest |
| Gap audit and rollback drill | `PENDING` | audit/rollback IDs |

## Commands

Run from the repository root with a populated local `moox.toml` and
short-lived credentials supplied through the process environment. Do not put
secrets in this document or in command output.

```bash
./bin/moox-cli collector function publish submit --file ./moox.toml --space-id stockcn --control-url "$MOOX_CONTROL_URL" > stock-cn-publish.json
./bin/moox-cli collector function probe-egress --control-url "$MOOX_CONTROL_URL" --space-id stockcn --file ./moox.toml --service-access-key "$MOOX_SERVICE_ACCESS_KEY" --service-secret-key "$MOOX_SERVICE_SECRET_KEY" > stock-cn-egress-probe.json
./bin/moox-cli data kline get --config ./config/data-access-stockcn.yaml --data-type stockcn --exchange stockcn --symbol 600000.XSHG --interval 1m --limit 10
```

The Rule and Timer must remain disabled if any required check is not backed by
the corresponding production response and Storage readback.

The provider probe was run on `2026-08-30` against representative SH, SZ and
BSE subjects. The redacted JSON evidence is
[stock-cn-provider-probe-20260830.json](stock-cn-provider-probe-20260830.json).
It remains an active-source diagnostic only: the current source route enables
Sina and EastMoney as complete Instrument candidates whose snapshots are
fetched in parallel and merged by canonical SubjectID. Tencent covers SH/SZ
K-line requests but still has no verified full Instrument pagination, while
Baidu remains protocol-invalid for this release.

The stock deployment canary sends a bounded historical `market_fetch` item. Its
initial start is kept below the active providers' 24-hour page capability; the
stock runtime then resolves the latest closed 1m session from the packaged
trading calendar and replays that exact bucket on weekends and holidays. This
calendar-aware replay is limited to the deployment canary and does not weaken
the normal `HistoryPolicy` fail-closed boundary. The Monitor canary also checks
the final 14:59 bucket during the short post-close grace window before treating
the market as idle.

The stock production Kline scheduler uses the configured fixed `N` Timer
fleet. The stock production Instrument path is a separate daily Timer with one
full-snapshot invocation, not a Kline shard. The r44 deployment canary also
uses one full snapshot because every provider pagination pass is expensive for
the public feed; it proves real provider merge, complete Sina count
validation, and Storage activation. The shared shard protocol remains covered
by collector and Storage tests for markets that use sharded snapshots.
