# SCF E2E Debug

Use this reference when a MooX collector cloud function does not publish, does not come online, does not receive tasks, or does not write K-line data to storage.

## Inputs

Collect these before acting:

- Function name, namespace, region, runtime, handler, and node type.
- Cloud account ID, COS bucket, COS region, package ID, package version, and package zip path.
- Control URL, storage URL, remote host, and deploy directory.
- Task ID, rule ID, symbol, interval, data source, data type, inst type, space ID, dataset ID, and view ID.
- CLS topic ID and a narrow time range around the failed invocation.

Never paste SecretKey, service auth secret, SSH password, signed `Auth` headers, or complete encrypted config values into notes or final answers.

## Fast Triage

1. Confirm whether the failure is before or after SCF invocation.
   - No package: local build or package upload failed.
   - No function: CloudNode batch_change or Tencent SCF API failed.
   - Function offline: keepalive or heartbeat failed.
   - Task pending: planner submission, JetStream delivery, or collector task status report failed.
   - Task success but no data: collector execution or storage write failed.
2. Preserve `git status --short` before touching code.
3. Use narrow commands and logs; avoid broad log dumps.

## Local Build And Package

Prefer `moox-cli` because it uses the same package path as one-click publish:

```bash
scripts/build.sh cli collector
bin/moox-cli collector function package \
  --collector-root modules/collector \
  --version vYYYYMMDDHHMM \
  --out /tmp/collector-scf-vYYYYMMDDHHMM.zip
```

Alternative module-local build:

```bash
cd modules/collector
make build-scf vYYYYMMDDHHMM
```

Inspect the zip before upload:

```bash
unzip -l /tmp/collector-scf-vYYYYMMDDHHMM.zip
unzip -p /tmp/collector-scf-vYYYYMMDDHHMM.zip config.yaml | sed -n '1,180p'
```

Expected contents:

- `main` at zip root.
- Collector YAML files at zip root.
- Embedded version matches the package version.
- Control and storage endpoints point to the reachable remote services, not localhost unless SCF can reach localhost.

## Deploy MooX To Remote Host

Deploy control, storage, collector, CLI, web-host assets, scripts, docs, and skills through the root deploy script:

```bash
scripts/deploy-moox.sh \
  --target ubuntu@<remote-ip> \
  --dir ~/moox \
  --goos linux \
  --goarch amd64 \
  --build-web-assets
```

For package-only rollout:

```bash
scripts/deploy-moox.sh --target ubuntu@<remote-ip> --dir ~/moox --goos linux --goarch amd64 --no-start
```

Remote checks:

```bash
ssh ubuntu@<remote-ip> '~/moox/status.sh'
ssh ubuntu@<remote-ip> 'tail -n 200 ~/moox/log/trpc.log'
ssh ubuntu@<remote-ip> 'ss -lntp | egrep ":(11000|20201|20202|20200|20102)"'
```

If SCF cannot reach the host, open Tencent Lighthouse firewall ports with the MooX skill script:

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add \
  --detail-url '<tencent-lighthouse-instance-detail-url>' \
  --ports 11000,20201,20202,20200,20102 \
  --dry-run
```

Remove `--dry-run` only after the parsed instance, region, and ports are correct.

## Publish Or Recreate SCF

One-step package upload and function creation:

```bash
bin/moox-cli collector function publish \
  --control-url http://<control-host>:11000 \
  --cloud-account-id <cloud-account-id> \
  --region ap-guangzhou \
  --runtime CustomRuntime \
  --handler main \
  --version vYYYYMMDDHHMM \
  --package-name moox-collector \
  --package-type data_collector \
  --biz-type data_collector \
  --node-type scf-event \
  --function-config timeout=120 \
  --env MOOX_ENV=prod
```

If a zip already exists, pass `--zip /path/to/package.zip`.
`--function-config` and `--env` are submitted to cloudnode as node runtime metadata; they do not mutate the SCF zip `config.yaml`.

Storage/Admin runtime addresses are no longer injected at package time. They come from control-plane keepalive payload `service_deployments`; update `t_service_deployments` through the management console before probing SCF nodes.

Watch the returned `package_id` and create-node `batch_id`. If `package_id` is missing, inspect `moox-cloudnode` logs and `t_cloud_function_packages`.

## Control Plane Checks

Use `/api/admin` for management calls and `/api/service` for service-to-service callbacks.

Useful log filters:

```bash
rg -n "InitPackageUpload|CompletePackageUpload|CreateNode|CreateFunction|UpdateFunction|Invoke keepalive|ReportHeartbeat|SubmitJobItems|ReportJobItemStatus|ScheduleTasks|ReportTaskStatus|storage" ~/moox/log/trpc.log
```

Check these boundaries:

- Cloud account still exists and is linked to node/package.
- Package row has COS bucket, COS region, COS path, version, runtime, package type, and status.
- Node row has node ID/function name, package ID, region, namespace, runtime, handler, node type, biz type, and supported collectors.
- Keepalive probe invokes the same function name shown in Tencent SCF.
- Heartbeat store marks the node online after keepalive success.
- `ScheduleTasks` submitted only the next JobItem after the rule changed.
- The runtime has one NATS connection and one resident taskrunner bound to all supported durables.
- Heartbeat scheduling is independent of the resident taskrunner; SCF execution needs no scheduler invocation.
- New compatible SCF instances join the same `space_id + job_type` durable automatically.

## CLS Log Investigation

Use the `cls-query` skill when available. Query a narrow time range first, then widen only if needed.

Search by `job_item_id` across the complete lifecycle:

- Function entry and heartbeat: `handleKeepalive`, `ProcessProbe`, `ReportHeartbeat`.
- Delivery lifecycle: `collector_job_received`, validation, deferral, execution start, and `collector_job_done`.
- Status callbacks: `collector_job_instance_reported`, `collector_job_cloudnode_reported`,
  `ReportTaskStatus`, and `ReportJobItemStatus`.
- Delivery action: actual ACK, NAK, or TERM result.
- Storage write: `WriteRecordRows`, `UpsertSubject`, `BindDatasetSubject`, `key.data_time`.

Evidence table:

| Observation | Boundary |
| --- | --- |
| No `handleKeepalive` | control did not invoke SCF, or SCF trigger/API failed |
| `ProcessProbe` ok but no `ReportHeartbeat` | SCF cannot reach control `/api/service/cloudnode/ReportHeartbeat` |
| Heartbeat ok but no `collector_job_received` | submission, stream, durable identity, or NATS connectivity issue |
| Received then deferred | future `execute_at`; NAK delay must equal `execute_at - now` |
| `采集成功` but status callback fails | service auth/path/firewall issue |
| Status success but no view rows | storage write, subject binding, dataset, freq, or view materialization issue |

## K-line Data Verification

Verify the task payload first:

- `data_type` should be `kline`.
- `data_source` should match the collector registry, for example `binance`.
- `inst_type` should match the dataset, for example `SPOT`.
- `symbol` should be the source symbol used by storage subject binding.
- `intervals` should contain one interval for atomic K-line tasks, for example `["1m"]`.

Then verify storage:

1. Subject exists under the expected `space_id`.
2. Dataset binding exists and is idempotent.
3. K-line rows use exchange K-line close/open time from upstream data; do not use local machine time as candle time.
4. `key.data_time` represents the K-line time.
5. Only closed candles are written. If upstream returns an open candle, collector should retry rather than write it.
6. View query includes the same `space_id`, dataset, symbol, and `freq`.

If the view is empty but raw records exist, inspect view definition, freq selection, materialization status, and storage-view logs.

## Common Root Causes

- Keepalive did not include active `service_deployments`, or the SCF runtime did not apply them before callback/storage writes.
- Remote firewall allows storage port but not control gateway port.
- Management API path was used by SCF callback, causing login-state auth failure.
- Collector did not submit the next JobItem for the expected dataset subjects.
- Publisher and SCF use different `space_id + job_type` durable identities.
- SCF metadata omits a supported workload, so its resident taskrunner did not bind that durable.
- Function runtime/handler differs from the zip layout.
- CLS topic/region is for another function or namespace.
- Binance TLS verification being disabled is accepted here; do not diagnose that configured state as the failure by itself.
