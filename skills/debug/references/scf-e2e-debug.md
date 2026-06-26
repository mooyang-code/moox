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
   - No function: create-node job or Tencent SCF API failed.
   - Function offline: keepalive or heartbeat failed.
   - Task pending: task planner, heartbeat response, or collector local task cache failed.
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
  --out /tmp/collector-scf-vYYYYMMDDHHMM.zip \
  --set control.server_url=http://<control-host>:20103 \
  --set storage.server_url=http://<storage-host>:19104
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
ssh ubuntu@<remote-ip> 'ss -lntp | egrep ":(20103|19104|19105|19101|18201)"'
```

If SCF cannot reach the host, open Tencent Lighthouse firewall ports with the MooX skill script:

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add \
  --detail-url '<tencent-lighthouse-instance-detail-url>' \
  --ports 20103,19104,19105,19101,18201 \
  --dry-run
```

Remove `--dry-run` only after the parsed instance, region, and ports are correct.

## Publish Or Recreate SCF

One-step package upload and function creation:

```bash
bin/moox-cli collector function publish \
  --control-url http://<control-host>:20103 \
  --cloud-account-id <cloud-account-id> \
  --region ap-guangzhou \
  --runtime CustomRuntime \
  --handler main \
  --version vYYYYMMDDHHMM \
  --package-name data-collector \
  --package-type data_collector \
  --biz-type data_collector \
  --node-type scf-event \
  --set control.server_url=http://<control-host>:20103 \
  --set storage.server_url=http://<storage-host>:19104 \
  --function-config timeout=60 \
  --env MOOX_ENV=prod
```

If a zip already exists, pass `--zip /path/to/package.zip`.

Watch the returned `upload_job_id`, `package_id`, and `create_job_id`. If `package_id` is missing after polling, inspect control-plane async task logs and `t_function_packages`.

## Control Plane Checks

Use `/api/control` for management calls and `/api/service` for service-to-service callbacks.

Useful log filters:

```bash
rg -n "UploadPackage|CreateNode|CreateFunction|UpdateFunction|Invoke keepalive|ReportHeartbeat|TaskPlanner|ReportTaskStatus|storage" ~/moox/log/trpc.log
```

Check these boundaries:

- Cloud account still exists and is linked to node/package.
- Package row has COS bucket, COS region, COS path, version, runtime, package type, and status.
- Node row has node ID/function name, package ID, region, namespace, runtime, handler, node type, biz type, supported collectors, and probe enabled.
- Keepalive probe invokes the same function name shown in Tencent SCF.
- Heartbeat store marks the node online after keepalive success.
- Task planner has already run at least once after rule/node changes.

## CLS Log Investigation

Use the `cls-query` skill when available. Query a narrow time range first, then widen only if needed.

Search patterns:

- Function entry: `handleKeepalive`, `ProcessProbe`, `ReportHeartbeat`.
- Task download: `收到任务实例更新`, `Task[`, `parse_task_params_success`.
- Due-task decision: `client_task_fetch`, `client_task_detail`, `shouldExecute`, `Will execute`.
- Collector execution: `执行采集`, `采集成功`, `采集失败`.
- Storage write: `WriteRecordRows`, `UpsertSubject`, `BindDatasetSubject`, `key.data_time`.
- Status callback: `ReportTaskStatus`, `任务状态上报成功`, `status=2`.

Evidence table:

| Observation | Boundary |
| --- | --- |
| No `handleKeepalive` | control did not invoke SCF, or SCF trigger/API failed |
| `ProcessProbe` ok but no `ReportHeartbeat` | SCF cannot reach control `/api/service/cloudnode/ReportHeartbeatInner` |
| Heartbeat ok but no task list | planner/store/node assignment issue |
| Task list ok but `shouldExecute=false` | interval/time scheduling issue |
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

- SCF package contains stale YAML pointing at local endpoints.
- Remote firewall allows storage port but not control gateway port.
- Management API path was used by SCF callback, causing login-state auth failure.
- Task planner store was empty and treated as `initializing`, leaving old collector task cache intact.
- Rule or node lacks matching `space_id`, `biz_type`, supported collector type, or online heartbeat status.
- Fixed node assignment points at an offline or incompatible node.
- Function runtime/handler differs from the zip layout.
- CLS topic/region is for another function or namespace.
