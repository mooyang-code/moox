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
`--cloud-account-id` is set. It always queries the fixed `ap-guangzhou` CLS
resources: Logset `moox` and Topic `moox-application`. Missing resources are
created; existing resources are reused. The verified Topic ID is written only
to staged `trpc_go*.yaml` files. Writer credentials are installed only in the
target's `secrets/cls.env`, with mode `0600`, and remain placeholders in the
staged configuration.

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
  --deploy-dir /home/ubuntu/moox \
  --deploy
```

## System Initialization Deployment Flow

Before uploading or starting web-host, run the automatic managed edge prerequisite:

```bash
skills/moox/scripts/caddy-prerequisite.sh ensure --target user@host --deploy-dir /home/user/moox
scripts/deploy-moox.sh --target user@host --dir /home/user/moox --public-host host.example
```

The prerequisite command installs and verifies Caddy `v2.11.4`; on a clean target it intentionally waits because the Caddyfile and loopback upstreams do not exist yet. The following deployment command uploads the candidate Caddyfile, rejects non-loopback upstream addresses, starts loopback upstreams, atomically starts or reloads only the MooX-owned Caddy process, persists its CA, attempts target-host trust installation, configures backend trust, and performs HTTPS acceptance. See `references/caddy-https.md` for CA retrieval, browser trust, rotation, and conflict recovery.

### 本机浏览器 Caddy 根证书

当浏览器打开 `https://<public-host>:9527` 报 `ERR_CERT_AUTHORITY_INVALID` 时，不要关闭 TLS 校验。先按当前操作系统选择本机证书路径并获取目标 CA：

```bash
case "$(uname -s)" in
  Darwin*) PLATFORM=macos ;;
  Linux*) PLATFORM=linux ;;
  MINGW*|MSYS*|CYGWIN*) PLATFORM=windows ;;
  *) echo "unsupported OS" >&2; exit 2 ;;
esac

PUBLIC_HOST=106.53.107.122
CA_FILE="$HOME/.moox/certs/moox-caddy-root-${PUBLIC_HOST}.crt"
mkdir -p "$(dirname "$CA_FILE")"
skills/moox/scripts/caddy-ca.sh fetch \
  --target ubuntu@106.53.107.122 \
  --deploy-dir /home/ubuntu/moox/prod \
  --output "$CA_FILE"
skills/moox/scripts/caddy-ca.sh inspect --ca-file "$CA_FILE"
```

根据 `PLATFORM` 安装到浏览器实际使用的系统信任库：

```bash
case "$PLATFORM" in
  macos)
    sudo security add-trusted-cert -d -r trustRoot -p ssl \
      -k /Library/Keychains/System.keychain "$CA_FILE"
    ;;
  linux)
    if command -v update-ca-certificates >/dev/null 2>&1; then
      sudo cp "$CA_FILE" /usr/local/share/ca-certificates/moox-caddy-root.crt
      sudo update-ca-certificates
    elif command -v update-ca-trust >/dev/null 2>&1; then
      sudo cp "$CA_FILE" /etc/pki/ca-trust/source/anchors/moox-caddy-root.crt
      sudo update-ca-trust
    else
      echo "no supported Linux trust-store command" >&2; exit 1
    fi
    ;;
  windows)
    powershell.exe -NoProfile -NonInteractive -Command \
      'Import-Certificate -FilePath $args[0] -CertStoreLocation Cert:\\CurrentUser\\Root | Out-Null' \
      "$CA_FILE"
    ;;
esac
```

安装后必须按指纹检查，而不是只检查 `.crt` 文件是否存在：

```bash
skills/moox/scripts/caddy-ca.sh status --ca-file "$CA_FILE"
```

结果应包含 `"valid_ca":true,"trusted":true`。macOS Chrome/Safari 使用系统钥匙串；因此仅放入登录钥匙串不作为浏览器信任成功的依据。完全退出并重新打开浏览器后再访问页面。公开部署默认执行同样的检查和安装；只有明确不需要浏览器访问时才使用 `--local-ca skip`。

For a CLS-enabled release, `scripts/deploy-moox.sh --enable-cls` runs the CLS
predeploy check after stage creation and before release archive sync or service
shutdown.
That check requires the already-running Admin/CloudNode control plane and at
least one Tencent cloud account; a failed check must not stop the current
deployment.

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
that machine. Only after Storage is ready, list `metadata spaces`, ask which
business spaces to import (including all or none), and invoke
`setup metadata-import` with stable Space IDs. Keep `custom.toml` unchanged and
never parse either its secrets or a generated filtered seed in Agent context.

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
