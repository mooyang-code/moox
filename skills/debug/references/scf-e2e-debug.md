# SCF E2E Debug

Use this reference when a MooX collector cloud function does not publish, does not run on its Timer, or does not write K-line data to Storage.

## How Realtime Collection Works

1. Collector plans one Timer batch per period and function shard (`t_collector_timer_period_batches`).
2. CloudNode writes each function's shard assignment into its SCF environment and keeps its Timer trigger enabled.
3. At each Timer tick the function calls `ClaimTimerBatch` through Access (`11004`) as the `scf-collector` external identity (`MOOX_ACCESS_ADDRESS`, `MOOX_ACCESS_ID`, `MOOX_CALLER`, `MOOX_CALLER_KEY_ID`, `MOOX_CALLER_KEY` in the function environment; the address and instance identity are derived from the placement manifest) and receives its batch.
4. It fetches the providers concurrently and writes through the same Access instance: `EnsureDatasetPeriod`, `CommitTimeSeriesBatch`, `RecordDatasetPeriodFailures`.
5. It publishes `MarketFetchBatchCompleted` to EventBus; the Collector completion consumer updates the batch and schedules retries.
6. At the period deadline the Storage DataNode marks the period `complete` or `degraded` and emits `CollectorPeriodCompleted`.

## Inputs

Collect these before acting:

- Function name, namespace, region, Timer cron, and the CloudNode node ID.
- Cloud account ID, COS bucket/region, package ID and version.
- Space, collection task ID, result Dataset/View, frequency and the failing period time.
- CLS topic ID and a narrow time range around the failed Timer tick.

Never paste SecretKey, service auth secret, SSH password, signed headers, or EventBus tokens into notes or final answers.

## Fast Triage

1. No package: local build or COS upload failed.
2. No function or Timer disabled: CloudNode node batch or Tencent SCF API failed.
3. Timer fires but claims nothing: no planned batch for the shard, or the gateway runtime target/credentials are wrong.
4. Claim succeeds but no Storage rows: provider fetch or access write failed.
5. Rows written but the period stays waiting: completion not published or not consumed; check `MOOX_MARKET_FETCH` and the Collector completion consumer.
6. Preserve `git status --short` before touching code.

## Build And Package

```bash
SCF_SPACE_ID=crypto scripts/build/build-collector-scf-package.sh --eventbus-ca-file /path/to/eventbus-ca.pem
unzip -l release/scf/collector-scf-crypto-<version>.zip
```

Expected contents: `main`, `sources/market/*.yaml`, `certs/eventbus-ca.pem`, and for `stockcn` also `markets/stockcn/{calendar,route}.yaml`. The package carries no credentials; runtime addresses and secrets come from the function environment written by CloudNode.

## Publish And Inspect Functions

Prefer the CLI, which reads `moox.toml` and drives CloudNode:

```bash
bin/moox-cli collector function package ...
bin/moox-cli collector function publish ...
bin/moox-cli collector function status ...
bin/moox-cli collector function timer-inventory \
  --control-url http://127.0.0.1:11002 --file ./moox.toml \
  --space-id crypto --cloud-account-id <account-id> --namespace <namespace> --region <region>
```

`timer-inventory` is read-only and reports `complete=true` only when every Timer has a fresh provider readback with the expected cron, qualifier and message.

## Control Plane Checks

Useful log filters on the control host:

```bash
rg -n "ClaimTimerBatch|timer_period|market_fetch|completion|EnsureDatasetPeriod|CommitTimeSeriesBatch" /data/moox/prod/logs/collector/trpc.log
rg -n "SubmitUpdateNodeRuntimeConfigs|EnsureTimerTrigger|UpdateFunctionConfiguration" /data/moox/prod/logs/cloudnode/trpc.log
```

Check these boundaries:

- The node row has the expected function name, region, namespace, package and Timer state.
- The function environment carries the current assignment and binding hash.
- `t_collector_timer_period_batches` has a row for the shard and period, and it gets claimed.
- The Collector completion durable on `MOOX_MARKET_FETCH` is advancing.

## CLS Log Investigation

Use the `cls-query` skill when available. Query one Timer tick first, then widen. Look for the claim result, per-subject provider results, Storage commit acknowledgements, and the completion publish.

| Observation | Boundary |
| --- | --- |
| No invocation at the expected tick | Timer disabled, wrong cron, or function not deployed |
| Claim returns nothing | no planned batch, wrong group/binding hash, or runtime gateway auth |
| Provider errors for most subjects | egress IP blocked or provider down; run `moox-cli collector probe-egress` |
| Commit rejected | Storage period snapshot mismatch or access principal/method allowlist |
| Commit ok but no completion | EventBus TLS/credential (`market-fetch-publisher`) or CA mismatch |

## K-line Data Verification

1. Rows use the exchange bar time, never local time; only closed bars are written.
2. The result View for the task (`view_<task>_kline_<freq>`) shows the subject at the expected `data_time`.
3. If raw rows exist but the View is empty, inspect the View's consumer, rebuild state and `storage-view` logs.

## Common Root Causes

- Timer disabled or cron drift after a manual change in the Tencent console.
- Function environment not updated after a task or subject change (stale binding hash).
- access target or principal missing for the function's region.
- EventBus CA or `market-fetch-publisher` token rotated without republishing the functions.
- Provider throttling or egress IP blocked for the region.
