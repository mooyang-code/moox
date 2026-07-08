# Monitor Module Verification

Date: 2026-07-09
Branch: `feature/monitor-module`

## Local Checks

```bash
go test ./packages/healthz/... ./modules/monitor/... -count=1
```

Result: PASS.

```bash
go test ./modules/admin/internal/gateway ./modules/admin/internal/service/sysdeploy ./modules/cloudnode/internal/config ./modules/collector/internal/app/control ./modules/factor/internal/app/control -count=1
```

Result: PASS.

```bash
bash -n scripts/build.sh scripts/release.sh scripts/deploy-moox.sh
./scripts/build.sh monitor
```

Result: PASS. Built `moox-monitor` and `moox-monitor-cli`.

```bash
./scripts/deploy-moox.sh \
  --target localhost \
  --dir /tmp/moox-deploy-noweb \
  --stage /tmp/moox-stage-noweb \
  --skip-build \
  --no-start \
  --no-storage \
  --no-cloudnode \
  --no-collector \
  --no-factor \
  --no-monitor \
  --no-web-host
```

Result: PASS. Generated runtime scripts with `MOOX_WITH_WEB_HOST=0`, preserved an existing `bin/moox-web-host`, and `status.sh` only reported `admin`.

```bash
cd web
pnpm exec vue-tsc --noEmit
pnpm exec eslint --ext .js,.ts,.vue ./src
pnpm run build:prod
```

Result: PASS. ESLint reported pre-existing warnings in `cloud-account-manage.vue` and `function-package-manage.vue`; no errors.

## Local End-To-End

Ports used:

- `18080`: local HTTP server for HTTP and TCP success probes.
- `18081`: webhook receiver for alert delivery.
- `18082`: webhook receiver for peer ownership failover.
- `21409/21410`, `21429/21430`, `21439/21440`, `21449/21450`: temporary monitor health/RPC ports.

Verified:

- `moox-monitor-cli init --db-path <tmp>/monitor.db` initialized schema successfully.
- Direct monitor RPC over HTTP created an HTTP check, ran it successfully, and `GetOverview` reported healthy checks.
- Direct monitor RPC created a TCP check against the same local server and confirmed `connected=true`.
- A failing HTTP check with `failure_threshold=1` sent a webhook payload containing `dedupe_key`; `ListAlertEvents` returned the alert event.
- Two monitor instances exchanged peer snapshots; running the same failing check/rule on both emitted exactly one webhook from the deterministic owner. After stopping the owner and waiting for stale timeout, the surviving instance emitted the next alert.

## Remote Deployment

Target: `ubuntu@106.53.107.122`
Deploy dir: `/home/ubuntu/moox/prod`

Final successful deploy for admin/monitor/web-host:

```bash
./scripts/deploy-moox.sh \
  --target ubuntu@106.53.107.122 \
  --dir /home/ubuntu/moox/prod \
  --goos linux \
  --goarch amd64 \
  --no-storage \
  --no-cloudnode \
  --no-collector \
  --no-factor
```

Result: PASS. Started `admin`, `monitor`, and `web-host`; previously running `cloudnode`, `collector`, and `factor` remained up.

Storage was then rebuilt on the Linux target because macOS cross-compiling the CGO DuckDB binary is not reliable:

```bash
ssh ubuntu@106.53.107.122 '$HOME/.local/go1.24.7/bin/go version'
rsync -az --delete ... /tmp/moox-build/
ssh ubuntu@106.53.107.122 'cd /tmp/moox-build && PATH=$HOME/.local/go1.24.7/bin:$PATH GOFLAGS=-buildvcs=false ./scripts/build.sh storage'
rsync -avP ubuntu@106.53.107.122:/tmp/moox-build/bin/moox-storage \
  ubuntu@106.53.107.122:/tmp/moox-build/bin/moox-storage-cli ./bin/
./scripts/deploy-moox.sh \
  --target ubuntu@106.53.107.122 \
  --dir /home/ubuntu/moox/prod \
  --skip-build \
  --goos linux \
  --goarch amd64 \
  --no-cloudnode \
  --no-collector \
  --no-factor \
  --reuse-web-assets
```

Result: PASS. Started `storage`, `admin`, `monitor`, and `web-host`; previously running `cloudnode`, `collector`, and `factor` remained up.

Remote health checks:

```bash
ssh ubuntu@106.53.107.122 '/home/ubuntu/moox/prod/status.sh'
curl -fsS http://106.53.107.122:11000/healthz
curl -fsS http://106.53.107.122:9527/healthz
ssh ubuntu@106.53.107.122 'curl -fsS http://127.0.0.1:11409/healthz'
```

Result: PASS. `storage`, `admin`, `monitor`, and `web-host` were running and healthy. Additional checks confirmed `cloudnode`, `collector`, and `factor` health endpoints were healthy.

Remote monitor SysDeploy sync:

```bash
ssh ubuntu@106.53.107.122 '
  curl -fsS -H "Content-Type: application/json" -d "{}" \
    http://127.0.0.1:11410/trpc.moox.monitor.MonitorMgr/SyncSystemChecks
  curl -fsS -H "Content-Type: application/json" -d "{\"source\":\"sysdeploy\",\"page\":{\"page\":1,\"size\":20}}" \
    http://127.0.0.1:11410/trpc.moox.monitor.MonitorMgr/ListChecks
  curl -fsS -H "Content-Type: application/json" -d "{}" \
    http://127.0.0.1:11410/trpc.moox.monitor.MonitorMgr/GetOverview
'
```

Result: PASS. Sync returned `synced=26`; `ListChecks` returned `moox-system` checks including `moox_monitor` and `storage_metadata` health URLs; overview returned `total_checks=26`, `healthy_checks=26`, `down_checks=0`, `active_instances=1`.

Frontend availability:

```bash
curl -fsS -I http://106.53.107.122:9527/#/ops/service-monitor
```

Result: PASS. Returned HTTP 200 and the SPA shell.

## Issues Found During Verification

- First remote monitor start failed because `modules/monitor/config/trpc_go.yaml` referenced the validation plugin without importing/registering it. Fixed by removing the unused validation filter/plugin config.
- First remote SysDeploy sync timed out because monitor's SysDeploy client used the default protocol against Admin's HTTP protocol service. Fixed by adding `client.WithProtocol("http")` and `client.WithNetwork("tcp")`.
- Peer stale detection initially marked fresh peers down due SQLite DATETIME comparison behavior. Fixed by comparing `LastSeenAt` in Go and added a regression assertion.
- Running deploy with `--no-web-host` still stopped the existing remote web-host and then could not restart it because the binary was excluded. Fixed by propagating `WITH_WEB_HOST` through generated runtime scripts and local/remote sync cleanup.
- SysDeploy sync originally only upserted current deployments. Fixed by disabling stale `source=sysdeploy` checks after a successful sync, including deployments removed from SysDeploy or marked `monitor_enabled=false`.
- Peer ownership could include stale instances when peer pulling was disabled or partially failing. Fixed by filtering owners by fresh `LastSeenAt`, continuing after individual peer pull failures, and marking stale peers even when the configured peer list is empty.
- Webhook template rendering originally did raw string replacement. Fixed by JSON-escaping string placeholder values before replacement.
- `result_retention_days` was loaded but unused. Fixed by adding result and alert-event pruning.
- Per-check alert rule listing originally hid disabled rules. Fixed so the management API returns all non-deleted rules for that check while alert evaluation still uses only enabled rules.
- Existing Admin DB rows did not automatically receive newly added default health metadata. Fixed by making SysDeploy default seeding backfill missing `extra_config` keys without overwriting explicit user values.
- The existing remote `trade_account` row had been backfilled with a local `127.0.0.1:11210/healthz` URL even though trade is not deployed on this host. Fixed by removing the default trade health backfill and clearing the remote row back to TCP probing.

## Residual Risks

- Browser-level visual verification of the authenticated service monitor page was not completed because the remote Admin UI requires login credentials in the browser session. Frontend compile, lint, production build, and remote SPA availability passed.
