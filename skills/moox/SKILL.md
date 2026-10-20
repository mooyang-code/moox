---
name: moox
description: Use when working in the MooX monorepo, releasing or deploying MooX binaries to the production hosts (发布, 部署, 上线, rollout, rollback, 回滚, native release, service catalog rollout, host-gateway/Admin/Monitor/Storage/factor-engine release), operating moox-cli, or querying MooX采集数据 such as BTC-USDT crypto market queries and K-line/K线行情. Also covers quant storage, collector cloud functions, Linux amd64/arm64 Host Agent monitoring, rootless deployment, EventBus credentials, EventBus rotate, Authorization Violation, FIN-WAIT-2, certificate signature failure, View watermark catch-up, repair-view, Factor period diagnostics and Recalc, factor expression DSL (因子表达式, 因子 DSL), converting legacy Python factors (Python 因子转换, pandas 因子迁移) with offline check/eval/compare and writing them into the system, Tencent Cloud firewall changes, CCN/云联网, SCF 公网, SCF VPC, 内网组网, private-network, MOOX_ACCESS_ADDRESS, and control-plane maintenance.
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
- DataNode: the Storage node a fact Dataset is bound to at activation; it persists field-level facts in Pebble and publishes row changes through its outbox. DuckDB/Bleve views and Parquet archive are derived asynchronously from those changes.

Host Agent and EventBus credentials:

- Host Agent ships inside every host's `host` unit together with Host Gateway (`setup deploy-host`); see [`references/host-agent.md`](references/host-agent.md).
- Provision and rotate EventBus credentials only through `skills/moox/scripts/eventbus-credentials.sh`; release archives and checked-in YAML must remain credential-free.
- After EventBus CA or role-token rotation, fan-out to every client before treating deploy as done. Follow [`references/eventbus-credentials.md`](references/eventbus-credentials.md). Control `export` does not update Host Agents on other machines.

## Deployment Status

线上运行的是**原生部署**（`/data/moox/<单元>/current`，`moox-runtime` 管理进程；控制机 `prod`、Storage 主机 `storage`、每台主机 `host`）。
**发布、更新线上二进制时读 [`references/native-release.md`](references/native-release.md)**，并用 `skills/moox/scripts/native-release.sh`
（构建、安装、回滚、状态、台账、服务目录变更检测）；服务目录（`catalog.yaml`）变化必须整体发布，配置文件不随二进制更新，文档里有顺序和陷阱。
只有用户明确要求“发布/上线”时才发布，并且只发布他点名的组件。

## Repository Layout

- `modules/cli`: `moox-cli` (setup and native unit deployment, SCF publish, data import, doctor).
- `modules/admin`: control plane (auth, spaces, component catalog and host placements, gateway control, PKI and keys, SSH, setup) and the browser API gateway.
- `modules/hostgateway`: per-host service gateway (signed native tRPC: loopback `11002`, cross-host TLS `11003`); the only gateway.
- `modules/access`: signed entry for external callers (SCF, factor engine, Skill) on `11004`, with per-caller method allowlists from `packages/servicecatalog`.
- `modules/egressproxy`: restricted HTTPS egress and DNS resolution for Collector.
- `modules/consoleproxy`: embedded-Caddy HTTPS proxy for the console (`9527`).
- `modules/eventbus`: embedded NATS JetStream broker.
- `modules/storage`: metadata (primary), field-level facts and outbox (node), rebuildable views (view).
- `modules/collector`: collection tasks, period batch planning, SCF Timer runtime and reconciliation.
- `modules/cloudnode`: cloud accounts, SCF nodes, code packages and function publishing.
- `modules/factor`: factor manager and the expression factor engine (`packages/factorexpr`).
- `modules/strategy`, `modules/trade`, `modules/monitor`, `modules/hostagent`, `modules/archive`.
- `docs`: 总体设计、部署与运维和 `docs/模块/` 下的模块设计文档.
- `scripts`: build, quality checks, and contract/E2E test scripts.

## Common Commands

From the repository root:

```bash
make build
make check-boundaries
make verify-pr
```

发布或替换线上已部署的二进制时，用上面“Deployment Status”里的原生发布流程（[`references/native-release.md`](references/native-release.md)）。

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
`moox-cli data skill kline get`.

For View recovery, read
[`references/cli-operations.md`](references/cli-operations.md) before operating. It documents
the safe dry-run-first workflow and Storage `repair-view`; Factor recovery uses durable lag diagnosis and explicit Recalc, not consumer deletion,
Storage `force-rebuild-view`, durable names, defaults, credential lookup, backups, the
View bar budget (`[storage_retention] view_rows_per_series`), and the high-risk full index reset.
Never delete a durable consumer or a View index by hand when the corresponding `moox-cli`
operation is available.

`moox.toml` changes (for example `[storage_retention]` or `[storage_view]`) reach a native unit
only through a new installation of that unit (`setup release build` plus the unit's setup command
with `--release`); binary-only releases keep the installed configuration. A retention shrink deletes
rows at the next Storage cleanup, so confirm it with the user first. Do not edit host config files by hand.

When crypto K-line or Factor periods stall, read
[`references/view-catchup.md`](references/view-catchup.md) first. Measure Primary vs View vs
Factor separately. Collector completion events drive Factor reads from Storage PrimaryStore;
the Factor durable is `factor_collector_period_v1`. Use set-labeled period and lane metrics to
distinguish event backlog, Storage failures, and compute errors (`factor_compile_errors_total`).

### Factor Expressions And Python Factor Conversion

因子用 YAML 表达式（spec）定义，由 Go 表达式引擎计算；旧的 Python 因子（`compute(df, params, context)`）已退役。
用户要写新因子、或拿原来的 Python 因子来转换时，先读 [`references/factor-dsl.md`](references/factor-dsl.md)，按其中的流程执行：
对照 pandas → DSL 映射转写，`scripts/factor-expr.sh check` 编译检查，准备真实与合成 K 线，
`scripts/factor_py_check.py run-python` 运行原因子、`scripts/factor-expr.sh eval` 试算 DSL，`factor_py_check.py compare` 按验收规则逐值比较，
向用户汇报差异并在用户确认后写入（`moox-cli setup factors` 或控制台），最后做写入后验证。
不要把不支持的写法悄悄换成近似公式，也不要“修正”原公式；输出列名必须保持不变。

### Tencent Cloud Firewall

Open the ports MooX needs on every Tencent host with the CLI; it derives the ports from the
topology in `moox.toml` and calls the Tencent Cloud API (Lighthouse firewall or CVM security group):

```bash
./bin/moox-cli setup firewall --file ./moox.toml
```

`setup deploy-host` also confirms the control host's relay and EventBus ports before deploying an
outbound-only host. Tencent credentials come only from `moox.toml`; never echo them.

### Tencent Private Network

SCF functions run inside each region's VPC and reach MooX only through the same-region
Access host's private address (`11004`); they never connect to Storage or EventBus directly.
Every SCF region in `[scf_plan]` needs an Access host with `region` and `private_address`.
Do not create CCN. Keep `public_net_status=ENABLE` when the function must reach public market
providers. Follow [`references/private-network.md`](references/private-network.md) for the
configuration rules and evidence requirements.

Runtime data can be deleted and rebuilt from `examples/` and service flows. Do not reintroduce standalone acceptance CSV scripts.

## Development Rules

- Prefer the new protocol under `modules/storage/proto/*.proto`, `modules/admin/proto/*.proto`, `modules/collector/proto/*.proto`, and `modules/cloudnode/proto/*.proto`; shared response/auth/page types live in `packages/commonpb`.
- Do not add new compatibility proto packages for deleted call paths; update callers to the current module proto instead.
- Do not reintroduce `object_id` into public APIs. Use Space, DataSource, Subject, DataSet, View, Field, and Factor.
- Use `subject_id` for normalized subject identity. Source-specific symbols are derived by each collector adapter from the canonical subject ID; do not persist source symbol mappings in metadata.
- Use `start_time`, `end_time`, and `snapshot_time`; avoid suffixes such as `_ms`.
- Keep `series_tag` as one optional scalar user label and part of time-series identity, for example `venue:binance`. Do not reintroduce map-style dimensions.
- Treat Pebble-backed PrimaryStore as the online ordered fact store. Treat DuckDB as analytical query and versioned wide view storage. Treat Parquet as cold archive. Treat Bleve as text search.

See `references/` for more detailed notes.

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
creates repository-root `moox.toml` before deployment (preferably by running
`moox-cli config init` and filling the local form in their own browser; the
Agent never fills or reads it). The file is stored encrypted (key in the OS
keychain); when the user wants to view or change it, run `moox-cli config edit`
so they can do it on the local config page, and use `moox-cli config encrypt`
for a hand-written plaintext file. After the initial deployment run
`moox-cli config backup` once (encrypted with the admin password, stored on the
user's own Admin); on a new computer run `moox-cli config restore`. The Agent may test only
whether it exists and must never read, parse, print, copy, or source it outside
`moox-cli setup`.

Run `setup validate`, `setup bootstrap --stage core`, `setup deploy-host` for every
other host, `setup deploy-unit` for the Storage/Access/egress-proxy/Trade placements,
`setup bootstrap --stage control`, and finally `setup init --config-dir
./config/setup --storage-host <host-name>` to create or verify the default Admin
spaces and Storage metadata, activate Datasets, and verify the result. Keep
`moox.toml` secrets unchanged and never parse a generated filtered seed in Agent
context. When SCF is enabled, configure the same-region Access hosts described in
[`references/private-network.md`](references/private-network.md).

`t_hosts` and `t_placements` are the source of truth for service placement;
SysDeploy v1 and its deployment seed have been removed. CLI deployment
registration uses `SyncHostPlacements` with a full business component list;
Admin adds host components and preserves persisted enablement state.
`/#/ops/storage/nodes` remains the separate PrimaryStore topology and is never
silently synchronized. SCF receives active service deployments from the control
plane rather than embedding addresses at package time.
Host Agent server-resource monitoring is supported for Linux amd64/arm64 and
ships inside every host's `host` unit. EventBus
credentials are provisioned, exported, and rotated only through Admin using
`scripts/eventbus-credentials.sh`; do not put tokens or private keys in a
release archive, command line, or checked-in config. After CA or role-token
rotation, follow [`references/eventbus-credentials.md`](references/eventbus-credentials.md)
and fan-out to every EventBus client: control services, Storage, every Host Agent
host, and SCF. Control `export` does not update Host Agents on other machines.
