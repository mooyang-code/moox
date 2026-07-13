---
name: moox
description: Use when working in the MooX monorepo or operating moox-cli, including quant storage, collector cloud functions, Linux amd64/arm64 Host Agent server-resource monitoring, rootless deployment, EventBus credential provision/rotate, Tencent Cloud Lighthouse firewall changes, or control-plane maintenance.
---

# MooX Quant Data System

Use this skill when working inside the MooX monorepo, especially for quant data storage, protocol changes, collector integration, factor data, release, or deployment.

## What MooX Is

MooX is the unified repository for a personal quant data platform. It groups storage, collection, cloud node execution, factor calculation, trade, and control-plane modules in one Go workspace.

Core concepts:

- Space: an isolated user or strategy domain.
- DataSource: a concrete upstream source, such as BINANCE spot, OKX spot, HKEX, NASDAQ, or a custom feed.
- Subject: the business object stored in a Space, such as APT-USDT, a stock, a ranking item, or a document subject.
- DataSet: a source-bound logical kind of data, such as kline, ticker, company profile, ranking, event, or factor values.
- Field and Factor: Space-scoped reusable column definitions selected by DataSet columns.
- View: a query-facing, asynchronously built wide view over one primary DataSet and selected columns from related DataSets.
- StorageRoute: a policy that maps online primary facts to PrimaryStore nodes. DuckDB views, Bleve search, and Parquet archive are derived asynchronously from primary fact changes.

Host Agent deployment:

- Build `skills/moox/scripts/hostagent-release.sh` for Linux `amd64` or `arm64`.
- Deploy with `skills/moox/scripts/hostagent-deploy.sh`; it uses a normal user and `systemctl --user`, never sudo.
- Provision and rotate EventBus credentials only through `skills/moox/scripts/eventbus-credentials.sh`; release archives and checked-in YAML must remain credential-free.

## Repository Layout

- `modules/cli`: `moox-cli`.
- `modules/admin`: control plane service and metadata orchestration.
- `modules/storage`: storage service, protocol, access, primary store, view, search, archive, and device drivers.
- `modules/collector`: independent collection service with CollectMgr APIs, planner, executor, cloudruntime, source adapters, and SCF runtime entrypoints.
- `modules/cloudnode`: cloud node, cloud account, SCF package, async work_item, and sync invocation service.
- `modules/factor`: factor calculation module.
- `modules/trade`: trade module.
- `docs`: architecture, concept, and protocol documents.
- `scripts`: root build, release, deploy, collector SCF package, storage helper, and node_exporter operation scripts.

## Common Commands

From the repository root:

```bash
make build
make check-boundaries
make release
make deploy
```

Protocol generation:

```bash
make proto
```

## MooX CLI Operations

Prefer bundled scripts in this skill when a workflow needs deterministic parsing or repeated `moox-cli` argument assembly.

### Tencent Lighthouse Firewall

When the user provides a Tencent Cloud Lighthouse instance detail URL, use the bundled script to parse the instance ID and call `moox-cli`:

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add \
  --detail-url 'https://console.cloud.tencent.com/lighthouse/instance/detail?searchParams=rid%3D5&rid=1&id=lhins-a7yikq89' \
  --ports 20201,20200,20202,11000 \
  --dry-run
```

After the dry run is correct, remove `--dry-run` to create the firewall rule. The script defaults to the MooX service ports `20201,20200,20202,11000`, `TCP`, `0.0.0.0/0`, and `ap-guangzhou` when the console URL does not provide a region.

Useful safe checks:

```bash
python3 skills/moox/scripts/tencent_lighthouse_firewall.py parse --detail-url '<console-detail-url>'
python3 skills/moox/scripts/tencent_lighthouse_firewall.py add --detail-url '<console-detail-url>' --print-command
```

The script calls `bin/moox-cli` from the repository when present, or `moox-cli` from `PATH`. Tencent credentials should be supplied through `TENCENTCLOUD_SECRET_ID` and `TENCENTCLOUD_SECRET_KEY`; do not echo secrets in final responses or logs.

Runtime data can be deleted and rebuilt from `examples/` and service flows. Do not reintroduce standalone acceptance CSV scripts.

## Development Rules

- Prefer the new protocol under `modules/storage/proto/*.proto`, `modules/admin/proto/*.proto`, `modules/collector/proto/*.proto`, and `modules/cloudnode/proto/*.proto`; shared response/auth/page types live in `packages/commonpb`.
- Do not add new compatibility proto packages for deleted call paths; update callers to the current module proto instead.
- Do not reintroduce `object_id` into public APIs. Use Space, DataSource, Subject, DataSet, View, Field, and Factor.
- Use `subject_id` for normalized subject identity and `SubjectSymbol.external_symbol` for source-specific symbols.
- Use `start_time`, `end_time`, and `snapshot_time`; avoid suffixes such as `_ms`.
- Keep `dimensions` as user-defined partition/query dimensions. Do not expose storage-level partition keys to callers.
- Treat Pebble-backed PrimaryStore as the online ordered fact store. Treat DuckDB as analytical query and versioned wide view storage. Treat Parquet as cold archive. Treat Bleve as text search.

See `references/` for more detailed notes.

### moox-storage 远端 CGO 编译（内部）

维护者专用，见 [skills/dev-helper/SKILL.md](../dev-helper/SKILL.md)。未配置 SSH 目标时脚本会交互提示；Agent 应在对话中向用户询问后以 `--target` 传入，勿写入仓库。

```bash
# 先配置 MOOX_DEV_SSH_TARGET（勿提交密码）
./skills/dev-helper/scripts/storage-remote-build.sh \
  --deploy-dir /home/ubuntu/moox \
  --deploy
```

## System Initialization Deployment Flow

Before uploading or starting web-host, run the automatic managed edge prerequisite:

```bash
skills/moox/scripts/caddy-prerequisite.sh ensure --target user@host --deploy-dir /home/user/moox
scripts/deploy-moox.sh --target user@host --dir /home/user/moox --public-host host.example
```

The prerequisite command installs and verifies Caddy `v2.11.4`; on a clean target it intentionally waits because the Caddyfile and loopback upstreams do not exist yet. The following deployment command uploads the candidate Caddyfile, starts loopback upstreams, atomically starts or reloads only the MooX-owned Caddy process, persists its CA, configures backend trust, and performs HTTPS acceptance. See `references/caddy-https.md` for CA retrieval, browser trust, rotation, and conflict recovery.

When initializing a fresh MooX system, use a strict two-stage deployment flow. Do not ask for every service placement up front.

1. Stage 1 starts with admin only.
   Ask the user where to deploy the admin management plane. Collect only the target SSH host, admin gateway public IP, fixed admin gateway port, and admin frontend/web-host port needed to publish the management console.

2. Deploy the admin backend and frontend before anything else.
   Build and deploy the Admin loopback upstreams and frontend/web-host before starting or reloading managed Caddy. Browser calls use same-origin `/api/admin/*` through Caddy `:9527`; web-host serves static files at `127.0.0.1:9528` and does not proxy API traffic. Backend `/api/service/*` calls use the separate Caddy `:11001` edge.

3. Stop if the admin management plane is not reachable.
   Confirm that the admin page can be opened and that the gateway health endpoint answers from the expected gateway port. If either check fails, fix admin deployment first and do not ask about other services yet.

4. Stage 2 starts only after admin succeeds.
   After the admin backend and frontend are deployed and reachable, ask where to deploy the remaining services. Cover storage metadata/access/view, service gateway exposure, collector-related services, trade services, and any internal admin RPC ports that differ from defaults. Prefer explicit IP and port for every service.

5. Write service deployment information through SysDeploy.
   Use `t_service_deployments` via the admin `sysdeploy` API as the single source of truth for service IP/port/base URL. Do not derive deployment topology from local files.

6. Preserve the storage topology boundary.
   `/#/ops/storage/nodes` remains the PrimaryStore topology. If a storage service deployment address changes, remind the user to inspect and update the storage topology endpoint separately; do not silently synchronize it.

7. SCF runtime address propagation.
   Do not inject storage/admin addresses while building SCF packages. The control plane keepalive probe must pass active `service_deployments` to SCF, and SCF should use `storage_access` directly for storage writes while using `/api/service/*` for backend gateway calls.
Host Agent deployment and server-resource monitoring are supported for Linux
amd64/arm64. Use `scripts/hostagent-release.sh` and
`scripts/hostagent-deploy.sh` for rootless user-systemd deployment. EventBus
credentials are provisioned, exported, and rotated only through Admin using
`scripts/eventbus-credentials.sh`; do not put tokens or private keys in a
release archive, command line, or checked-in config.
