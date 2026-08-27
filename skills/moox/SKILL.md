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
- `scripts`: root build, release, deploy, collector SCF package, storage helper, quality checks, and contract/E2E test scripts.

## Common Commands

From the repository root:

```bash
make build
make check-boundaries
make release
make deploy
```

单独发布或替换已部署的二进制服务时，使用
[`references/service-release.md`](references/service-release.md) 中的服务包发布流程；先用
`scripts/package-service.sh` 生成 ZIP，再通过通用入口 `moox-cli setup deploy-service` 发布。
服务包包含二进制、配置和生命周期脚本，由 CLI 通过已核验的 SSH 主机指纹完成上传、解压、
回滚和健康检查；不要在命令行中拼接密码。

敏感信息扫描已接入 CNB 和 GitHub CI 的 Pull Request 流程：`.cnb.yml` 的 `pull_request`
流水线和 `.github/workflows/ci.yml` 的 `Secret scan` 任务都会扫描本次 PR 的提交范围，
命中密码、令牌或密钥时以非零状态失败。要真正阻止合并，还需要在两个平台的 `main` 分支
保护规则中将 `Secret scan` 和常规验证任务设置为必需状态检查；CNB 同时启用“需要通过状态检查”。

Protocol generation:

```bash
make proto
```

## MooX CLI Operations

Prefer bundled scripts in this skill when a workflow needs deterministic parsing or repeated `moox-cli` argument assembly.

For requests to fetch collected market data, such as “获取 BTC-USDT 的 1m K 线”, read
[`references/data-query.md`](references/data-query.md). It defines the natural-language mapping,
catalog constraints, packaged credential handling, and result summary contract for
`moox-cli data kline get`.

For queue backlog and View recovery, read
[`references/cli-operations.md`](references/cli-operations.md) before operating. It documents
the safe dry-run-first workflow, the Factor `clear-queue` command, Storage `repair-view`,
Storage `force-rebuild-view`, durable names, defaults, credential lookup, backups, the
`storage.view.rebuild_lookback` coverage gate, and the high-risk full index reset.
Never delete a durable consumer or a View index by hand when the corresponding `moox-cli`
operation is available.

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

### Tencent CLS Before Deployment

When CLS is enabled, prepare it before syncing the release archive or stopping
any existing service. On remote targets this may upload a temporary,
architecture-matched `moox-cli` helper solely for preflight; the helper is
removed immediately and is not the release archive. The Admin and CloudNode
control plane must already be running and reachable through the
service-authenticated gateway, and the selected account must be a Tencent
cloud account:

```bash
skills/moox/scripts/cls-bootstrap.sh \
  --target user@host \
  --deploy-dir /home/user/moox \
  --stage-dir release/deploy-stage/moox \
  --admin-url http://127.0.0.1:11002
```

The script selects the first Tencent cloud account unless
`--cloud-account-id` is set. It queries the fixed `ap-guangzhou` Tencent Cloud
region for Logset `moox` and Topic `moox-application`. Missing
resources are created; existing resources are reused. The resolved non-secret
metadata is written to the generated `config/resources.env`, while staged
`trpc_go*.yaml` files reference `${MOOX_CLS_TOPIC_ID}`. Writer credentials are
installed only in the target's `secrets/cls.env`, with mode `0600`.

`custom.toml` contains only Tencent Cloud input credentials and region. The
generated `config/resources.env` is the canonical runtime source for the
selected cloud account, resolved Logset/Topic IDs, and CLS endpoint; it is non-secret, mode `0644`, and
is atomically replaced with the release stage. Never add resource IDs or
credentials to tracked examples, staged YAML, or command output.

## Development Rules

- Prefer the new protocol under `modules/storage/proto/*.proto`, `modules/admin/proto/*.proto`, `modules/collector/proto/*.proto`, and `modules/cloudnode/proto/*.proto`; shared response/auth/page types live in `packages/commonpb`.
- Do not add new compatibility proto packages for deleted call paths; update callers to the current module proto instead.
- Do not reintroduce `object_id` into public APIs. Use Space, DataSource, Subject, DataSet, View, Field, and Factor.
- Use `subject_id` for normalized subject identity and `SubjectSymbol.external_symbol` for source-specific symbols.
- Use `start_time`, `end_time`, and `snapshot_time`; avoid suffixes such as `_ms`.
- Keep `series_tag` as one optional scalar user label and part of time-series identity, for example `venue:binance`. Do not reintroduce map-style dimensions.
- Treat Pebble-backed PrimaryStore as the online ordered fact store. Treat DuckDB as analytical query and versioned wide view storage. Treat Parquet as cold archive. Treat Bleve as text search.

See `references/` for more detailed notes.

### moox-storage 远端 CGO 编译（内部）

维护者专用，见 [skills/dev-helper/SKILL.md](../dev-helper/SKILL.md)。未配置 SSH 目标时脚本会交互提示；Agent 应在对话中向用户询问后以 `--target` 传入，勿写入仓库。

```bash
# 先配置 MOOX_DEV_SSH_TARGET（勿提交密码）
./skills/dev-helper/scripts/storage-remote-build.sh \
  --deploy-dir /data/moox \
  --deploy
```

## System Initialization Deployment Flow

Use `moox-cli setup deploy-control` for the first control-plane deployment. It
owns the Caddy prerequisite, certificate selection, HTTPS acceptance, and the
healthcheck/renewal scheduler; do not ask the user to run a separate Caddy
script during initialization:

```bash
./bin/moox-cli setup validate --file ./custom.toml
./bin/moox-cli setup deploy-control --file ./custom.toml
```

The command installs and verifies pinned Caddy `v2.11.4`, uploads the
candidate Caddyfile, rejects non-loopback upstreams, starts or reloads only the
MooX-owned edge, and performs HTTPS acceptance. Its sanitized JSON includes a
`certificate` summary (`mode`, `issuer`, and `automatic_renewal`) so the Skill
can prove which trust model was selected without reading keys.

For internal CA deployments, `moox-cli` also checks and installs the Caddy root
in the operator machine's browser trust store during control initialization.
The same check runs before publishing `admin` or `web-host` packages. If a
previous run was interrupted, repair it with:

```bash
./bin/moox-cli setup trust-browser --file ./custom.toml
```

Automatic mode selects Let's Encrypt public certificates for public IP/DNS
hosts and Caddy internal CA for private, loopback, and `.localhost` hosts.
Public mode requires TCP 80 to remain reachable for HTTP-01 issuance and
renewal. Caddy remains running, renews from ACME ARI automatically, and is
restarted by the generated healthcheck after failure or reboot. Browsers and
backend clients use their operating-system trust store in public mode; no MooX
root certificate is installed.

### Internal 模式根证书

Only explicit `--tls-mode internal` creates `certs/caddy/root.crt`. Fetch and install it with the Caddy CA helper, then verify trust by fingerprint:

```bash
skills/moox/scripts/caddy-ca.sh fetch --target user@host --deploy-dir /home/user/moox \
  --output ~/.moox/certs/moox-caddy-root-<host>.crt
skills/moox/scripts/caddy-ca.sh install --ca-file ~/.moox/certs/moox-caddy-root-<host>.crt
skills/moox/scripts/caddy-ca.sh status --ca-file "$CA_FILE"
```

Internal-mode backend processes use `MOOX_SERVICE_GATEWAY_CA_FILE`; SCF uses `MOOX_SERVICE_GATEWAY_CA_PEM_B64`. Public mode must not set either service-edge CA variable. The separate `MOOX_GATEWAY_CA_FILE` peer bundle remains required in both modes. Never disable TLS verification.

For a CLS-enabled release, `scripts/deploy-moox.sh --enable-cls` runs the CLS
predeploy check after stage creation and before release archive sync or service
shutdown.
That check requires the already-running Admin/CloudNode control plane and at
least one Tencent cloud account; a failed check must not stop the current
deployment.
The managed Factor configuration uses an `info` CLS writer so
`factor_view_read_done`, `factor_task_done`, and `factor_view_ready_done` can
be used to diagnose calculation freshness; other services keep the lower
volume `warn` policy.

### Tencent CLS Log Query And Verification

When the user asks to query, verify, or troubleshoot CLS logs, use the bundled
`skills/moox/scripts/cls_search.py` script. Read
[`references/cls-query.md`](references/cls-query.md) for the query workflow and
[`references/cls-api.md`](references/cls-api.md) for the signed API contract.
The script supports CQL, relative time ranges, raw/JSON output, field
selection, and pagination. It uses `cls.internal.tencentcloudapi.com` by
default and loads credentials only from CLI arguments, `CLS_*` environment
variables, or an untracked `.env` file.
If the bundled script reports a missing SDK, install `tencentcloud-sdk-python-cls`
in the active Python environment; do not add Tencent credentials to the repo.

For a MooX CLS check, first query `--query '*'` with a narrow time range and
inspect the returned structured records. Verify `service_name` in the record
before trying a CQL predicate on that field; the fixed Topic may not have a
`service_name` index yet, in which case the API returns an explicit
`field ... is not indexed` error and client-side filtering is required. Report
the Topic ID, time range, result count, RequestId, service name values, and any
indexing or delivery errors, but never print CLS credentials.

When initializing a fresh MooX system, follow
[`references/custom-setup.md`](references/custom-setup.md) exactly. The user
creates repository-root `custom.toml` before deployment. The Agent may test only
whether it exists and must never read, parse, print, copy, or source it outside
`moox-cli setup`.

Run `validate`, `deploy-control`, `apply`, and `status` in that order. Do not
ask about Storage until setup is complete and the public login API is verified.
Then use `setup hosts`, ask the user in natural language for exactly one Storage
host, and run `setup deploy-storage`; the four initial Storage processes stay on
that machine. After Storage is ready, run `setup init --config-dir
./examples/setup/default --storage-host <host-name>` to create or verify the
default Admin spaces and Storage metadata, activate Datasets, and verify the
result. Keep `custom.toml` unchanged and never parse either its secrets or a
generated filtered seed in Agent context.

`t_service_deployments` remains the source of truth for service addresses.
`/#/ops/storage/nodes` remains the separate PrimaryStore topology and is never
silently synchronized. SCF receives active service deployments from the control
plane rather than embedding addresses at package time.
Host Agent deployment and server-resource monitoring are supported for Linux
amd64/arm64. Use `scripts/hostagent-release.sh` and
`scripts/hostagent-deploy.sh` for rootless user-systemd deployment. EventBus
credentials are provisioned, exported, and rotated only through Admin using
`scripts/eventbus-credentials.sh`; do not put tokens or private keys in a
release archive, command line, or checked-in config.
