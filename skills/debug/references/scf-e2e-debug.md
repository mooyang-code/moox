# SCF E2E Debug

Use this reference when a MooX collector cloud function does not run on its Timer, or does not write K-line data to Storage.

## How Realtime Collection Works

1. Collector (one reconciler per space) computes the fetch plan, registers series snapshots in Storage and writes the plan into each function's SCF environment (`MOOX_*` variables). Functions keep their Timer trigger enabled.
2. At each Timer tick the function derives the (dataset, subject) work from the plan and the event time, fetches the providers concurrently, and writes through Access (`11004`) as the `scf-collector` external identity: `CommitTimeSeriesBatch` (and `UpsertFields` for backfill rows). The Access address and instance identity are derived from the deployment manifest.
3. The default plan/backfill path does not call Collector or require EventBus; an explicitly configured metrics Reporter can publish to EventBus. Provider requests can retry or switch endpoints within the invocation budget. Final failures are logged to CLS (`market_fetch_item_failure`, `market_fetch_batch`); Collector repairs remaining gaps through backfill instead of re-dispatching an old completion batch.
4. Gaps are found by `moox-cli collector kline check` and repaired by `kline backfill` or the Collector's automatic backfill.
5. Storage lazily creates a period on its first valid commit, completes it as soon as all expected series arrive, or finalizes it as degraded at the deadline. A period with zero commits has no ledger or completion event. Crypto defaults to WS/local; use this Timer workflow only for datasets actually assigned to SCF.

## Inputs

Collect these before acting:

- Function name, namespace, region, Timer cron, and the FunctionInstance node ID.
- Cloud account ID, COS bucket/region, package ID and version.
- Space, collection task ID, result Dataset/View, frequency and the failing period time.
- CLS topic ID and a narrow time range around the failed Timer tick (CLS timestamps are in milliseconds).

Never paste SecretKey, service auth secret, SSH password or signed headers into notes or final answers.

## Fast Triage

1. No package: local build or COS upload failed.
2. No function or Timer disabled: FunctionInstance node batch or Tencent SCF API failed.
3. Timer fires but nothing is fetched: the plan in the function environment has no entry for this period (check `moox-cli collector fetch-plan`), or the event time is past the lateness deadline.
4. Fetch succeeds but no Storage rows: Access write failed (principal/method allowlist, wrong Storage application key).
5. Rows written but the View is empty: inspect the View consumer and `storage-view` logs. For historical backfills, first check `[storage_view.max_age]` and `moox_storage_view_expired_rows_dropped_total{space,freq}`: expired rows are deliberately dropped at View write time, including factor results, while processing still succeeds. Primary retention is independent (default crypto 1m: Primary 35d, View 7d).
6. Preserve `git status --short` before touching code.

## Build And Package

```bash
moox-cli collector function upload --package-name moox-collector-crypto-plan --version <version> --collector-root modules/collector
```

The package carries no credentials; runtime addresses and secrets come from the function environment written by Collector and FunctionInstance. Uploading a new version under the same package name makes Collector roll the functions forward (resolution happens during reconciliation/backfill preparation and successful results are cached for at least 30 seconds; normal reconciliation runs once per minute).

## Inspect Functions

```bash
bin/moox-cli collector fetch-plan --file ./moox.toml --space-id crypto
bin/moox-cli collector function timer-inventory \
  --file ./moox.toml \
  --space-id crypto --cloud-account-id <account-id> --namespace <namespace> --region <region>
```

`timer-inventory` is read-only and reports `complete=true` only when every Timer has a fresh provider readback with the expected cron, qualifier and message.

## Control Plane Checks

```bash
rg -n "fetch plan|backfill|readback" /data/moox/run/logs/collector.log
rg -n "SubmitUpdateFunctionInstanceRuntimeConfigs|EnsureTimerTrigger|UpdateFunctionConfiguration" /data/moox/run/logs/cloudfunction.log
```

Check these boundaries:

- The node row has the expected function name, region, namespace, package and Timer state.
- The applied plan (`fetch-plan`) lists the dataset hashes you expect and no `last_error`.

## CLS Log Investigation

Use the `cls-query` skill when available. Query one Timer tick first, then widen.

| Observation | Boundary |
| --- | --- |
| No invocation at the expected tick | Timer disabled, wrong cron, or function not deployed |
| Provider errors for most subjects | egress IP blocked or provider down; run `moox-cli collector function probe-egress` |
| Commit rejected | series snapshot mismatch (plan in the environment is stale) or Access principal/method allowlist |

## K-line Data Verification

1. Rows use the exchange bar time, never local time; only closed bars are written.
2. `moox-cli collector kline check --space-id <space> --dataset-id <dataset> --from <RFC3339> --to <RFC3339>` lists the missing bars.
3. The result View for the task (`view_<task>_kline_<freq>`) shows the subject at the expected `data_time`.

## Common Root Causes

- Timer disabled or cron drift after a manual change in the Tencent console.
- Function environment not updated after a task or subject change (stale plan).
- Access target or principal missing for the function's region.
- Provider throttling or egress IP blocked for the region.
