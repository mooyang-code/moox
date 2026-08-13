# Trade DNS Resolver and Collector SCF Synchronization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `custom.toml` 指定的 Trade 节点 `compute-1 (43.132.204.177)` 上提供受鉴权的 `ResolveDomains` RPC，由 Collector 定时批量请求域名解析结果，并复用现有 CloudNode Environment Patch 将结果同步到所有 SCF Timer 函数。

**Architecture:** `custom.toml` 是唯一配置源，`moox-cli` 解析后把脱敏的 `dns_resolver` 运行配置写入 Trade 的 `config/app.yaml`，同时把 Collector 访问 Trade 的目标和刷新参数写入 Collector/SCF 运行配置。Trade 只负责在自身网络出口执行 DNS 查询并对每个候选 IP 做 TCP 探测，返回 `domain -> ordered IP + latency`；Collector 负责刷新周期、来源优先级、快照保留和 SCF 环境协调。SCF 继续使用现有 `MOOX_MARKET_FETCH_DNS_ROUTES_JSON`，保持域名作为 Host/SNI、IP 仅用于 TCP 拨号，解析服务不可用时保留上次成功快照并允许 SCF 回退系统 DNS。

**Configuration flow (fixed decision):** `custom.toml -> moox-cli snapshot -> rendered Trade/Collector runtime config`. `moox-cli` 是唯一解析 `custom.toml` 的入口；Trade 不读取完整 TOML，也不自己推导 `compute-1` 地址。CLI 在同一次解析中生成一个不可变配置快照，再分别渲染 Trade 与 Collector 的配置文件，避免两边各自解析或出现字段漂移。渲染完成并通过校验后，部署流程才允许重启服务。

**本轮讨论的最终取舍（必须按此实现）：**

- `custom.toml` 继续作为用户可编辑的唯一配置源；不在 Trade、Collector 或部署 Shell 中新增第二套 TOML 解析逻辑。
- `moox-cli` 负责一次性解析并校验 `dns_resolver`，从同一份快照提取两份运行时配置：Trade 只得到 Resolver 自身需要的脱敏字段，Collector 得到访问 Trade 的目标、节点和刷新参数。
- Trade 的目标地址只能由 `other_hosts[dns_resolver.trade_node]` 派生。当前配置为 `compute-1 -> 43.132.204.177`，该地址不得在 Trade 源码、Collector 源码或 SCF 包中重复硬编码。
- 渲染采用“合并现有 YAML、只替换 `dns_resolver` 节点、临时文件写入后原子 `rename`”的方式；必须保留目标配置中的 `database`、`admin`、`eventbus`、`runtime` 等无关节点及原文件权限。
- 禁用或删除 `dns_resolver` 时也要显式写入 `enabled: false`，防止旧部署中的启用配置残留；渲染失败必须在服务重启前终止部署。
- Trade 进程只能读取渲染后的 `trade/config/app.yaml`，不得读取完整 `custom.toml`，不得接触 SSH、云账号或 Gateway secret。

**Tech Stack:** Go 1.24, tRPC-Go, protobuf v3, Gateway HMAC/native tRPC, Tencent CloudNode SCF Environment Patch, Collector SQLite/runtime timers, `net.Resolver`。

## Global Constraints

- Trade Resolver 只部署并使用 `custom.toml` 中的 `compute-1`，地址为 `43.132.204.177`；不把 46 台机器作为解析节点集合。
- Collector 不直接访问 Trade 的业务端口，不绕过 Gateway，不新增公网未鉴权 HTTP 入口。
- `ResolveDomains` 支持一次请求多个域名；单域名请求等价于长度为 1 的批量请求。
- 只返回合法 IPv4 地址，单域名最多返回 4 个地址，结果必须去重，并按 Trade 节点 TCP 探测延迟排序；不在单域结果中传递完成时间。
- Trade 端限制可解析域名；默认允许 `fapi.binance.com`、`api.binance.com`、`data-api.binance.vision`，自定义域名必须通过配置显式加入。
- Collector 不因一次 Trade RPC 失败清空 SCF 中已有的 DNS 快照；只有新快照经过校验且内容 hash 变化时才提交 Environment Patch。
- 保留现有 `MOOX_MARKET_FETCH_DNS_ROUTES_JSON`、`MOOX_MARKET_FETCH_DNS_HASH`、`MOOX_MARKET_FETCH_DNS_UPDATED_AT` 协议，不新增第二套 SCF DNS 环境变量。
- DNS 解析成功不等于 Binance Futures HTTPS 可达；验收必须分别记录 DNS、TCP/443、HTTPS `/fapi/v1/ping` 三个阶段。
- Collector 仍是 SCF DNS 环境的唯一写入者；Trade 不直接修改 CloudNode 或 SCF 配置。
- `custom.toml` 不由 Trade 进程直接读取；`moox-cli` 只把 `dns_resolver` 的非敏感运行字段原子写入目标部署的 `trade/config/app.yaml`。Trade 不接触 SSH、云账号、Gateway secret 等无关配置。
- `DomainResolution` 不携带逐域 `resolved_at_unix_ms` 或逐域 `error`。Collector 以收到并校验完整响应的时间作为快照时间；未解析域名通过响应级 `unresolved_domains` 和日志/指标表达。
- 延迟字段表示 Trade 节点到目标 IP 的 TCP 探测耗时，不代表 SCF 到目标 IP 的耗时；Collector 只用它排序候选 IP，不把延迟写入 SCF 环境变量。
- 本计划不把 DNS 结果写入 Trade 业务 SQLite；Trade 只保留短 TTL 内存缓存，重启后允许重新解析。

## Current Code Map

当前代码已经具备 Collector -> CloudNode -> SCF 的 DNS 快照链路，本次只替换 DNS 结果来源并补齐远程 Resolver：

- Collector DNS 配置与启动：`modules/collector/internal/bootstrap/config.go`、`modules/collector/internal/bootstrap/bootstrap.go`
- 本地 DNS 快照：`modules/collector/internal/dnscache/cache.go`
- SCF 环境生成：`modules/collector/internal/marketfetch/environment.go`
- SCF 环境协调：`modules/collector/internal/marketfetch/reconciler.go`
- SCF 环境读取与请求构造：`modules/collector/internal/marketfetch/timer.go`
- IP 拨号并保留 TLS SNI：`modules/collector/internal/httpclient/client.go`
- Trade proto 与服务注册：`modules/trade/proto/trade_service.proto`、`modules/trade/internal/rpc/register.go`、`modules/trade/internal/bootstrap/bootstrap.go`
- Trade 业务配置加载：`modules/trade/internal/config/app.go`、`modules/trade/config/app.yaml`
- custom.toml 解析与严格字段校验：`modules/cli/internal/setup/config/config.go`
- custom.toml 到服务配置的渲染：`modules/cli/internal/command/setup.go`、`modules/cli/internal/setup/config/*`
- SCF/Trade/Collector 部署环境：`scripts/deploy-moox.sh`、`modules/cli/internal/command/collector.go`

## Protocol Design

在 `modules/trade/proto/trade_service.proto` 增加独立的 `TradeDNSResolverService`，不要把 DNS 方法加入账户或交易执行服务。

```proto
message ResolveDomainsReq {
  repeated string domains = 1;
  uint32 max_ips_per_domain = 2;
}

message ResolvedIP {
  string ip = 1;
  uint32 tcp_connect_latency_ms = 2;
}

message DomainResolution {
  string domain = 1;
  repeated ResolvedIP ips = 2;
}

message ResolveDomainsRsp {
  common.RetInfo ret_info = 1;
  repeated DomainResolution resolutions = 2;
  repeated string unresolved_domains = 3;
}

service TradeDNSResolverService {
  rpc ResolveDomains(ResolveDomainsReq) returns (ResolveDomainsRsp);
}
```

协议规则：

- `domains` 去除空白、统一小写、去掉末尾点后去重；空列表、超过 16 个域名、非法主机名直接返回 `INVALID_PARAM`。
- `max_ips_per_domain=0` 使用服务端默认 4；调用方不能要求超过 4。
- 每个 `ResolvedIP` 都必须是通过 Trade 节点 TCP 探测的可用候选；`tcp_connect_latency_ms` 是 Trade 节点到该 IP 的 TCP 建连耗时，单位毫秒。DNS 解析成功但所有候选探测失败时，该域名进入 `unresolved_domains`。
- 单个域名失败不影响同批其他域名；请求级鉴权、解析器不可用才返回非成功 `ret_info`。不再在每个域名中塞自由文本 `error`，具体失败原因写日志和指标。
- 响应不携带逐域完成时间。Collector 在收到并校验完整响应后生成一次快照时间，并继续写现有 `MOOX_MARKET_FETCH_DNS_UPDATED_AT`。
- 不返回 DNS 查询凭据、内网 nameserver 地址、完整错误堆栈或未通过 TCP 探测的 IP。

## Configuration Contract

在 `custom.toml` 增加明确的 Resolver 选择，不重复填写 `43.132.204.177`：

```toml
[dns_resolver]
enabled = true
trade_node = "compute-1"
refresh_interval_seconds = 300
request_timeout_ms = 3000
lookup_timeout_ms = 1500
probe_timeout_ms = 500
probe_port = 443
cache_ttl_seconds = 300
max_ips_per_domain = 4
domains = ["data-api.binance.vision", "api.binance.com", "fapi.binance.com"]
```

配置解析时通过 `trade_node` 在 `other_hosts` 中查找地址；当前 `compute-1` 必须解析为 `43.132.204.177`。其中 `refresh_interval_seconds`、`request_timeout_ms` 是 Collector 调用侧参数，`lookup_timeout_ms`、`probe_timeout_ms`、`probe_port`、`cache_ttl_seconds`、`max_ips_per_domain` 是 Trade Resolver 参数。`moox-cli` 不让 Trade 直接读取完整 `custom.toml`，而是在部署目标中把以下脱敏 YAML 合并写入 `trade/config/app.yaml`：

```yaml
dns_resolver:
  enabled: true
  domains: ["data-api.binance.vision", "api.binance.com", "fapi.binance.com"]
  lookup_timeout_ms: 1500
  probe_timeout_ms: 500
  probe_port: 443
  cache_ttl_seconds: 300
  max_ips_per_domain: 4
```

写入必须是临时文件 + `rename` 的原子更新，只替换 `dns_resolver` 节点，不覆盖 Trade 现有 `database`、`admin`、`eventbus`、`runtime` 配置；不把 `trade_node`、主机 SSH 凭据、云账号或 Gateway secret 写入 Trade 配置。Collector 运行时使用由同一份快照派生的：

```text
MOOX_COLLECTOR_DNS_RESOLVER_ENABLED
MOOX_COLLECTOR_DNS_RESOLVER_TARGET=ip://43.132.204.177:11003
MOOX_COLLECTOR_DNS_RESOLVER_NODE_ID=compute-1
MOOX_COLLECTOR_DNS_RESOLVER_TIMEOUT_MS=3000
```

Collector 仍通过已有 Service Gateway HMAC 凭据访问远程 Gateway；不新增 Trade API key。开发环境未配置 Resolver 时，继续使用本地 DNS。

### `moox-cli` 渲染边界

执行计划中的配置生成命令统一为：

```bash
moox-cli setup render-runtime-config \
  --file ./custom.toml \
  --trade-output <stage>/trade/config/app.yaml \
  --collector-output <stage>/collector/config/app.yaml
```

命令只读取一次 `custom.toml`，并使用同一份 `Manifest` 快照完成两份配置的合并写入：

- Trade 输出只包含 `dns_resolver.enabled`、`domains`、`lookup_timeout_ms`、`probe_timeout_ms`、`probe_port`、`cache_ttl_seconds`、`max_ips_per_domain`；不写 `trade_node`、主机地址、刷新周期、Gateway 节点、SSH/云凭据或任何 secret。
- Collector 输出包含 `enabled`、由 `other_hosts[trade_node].address` 派生的 `target`、`node_id`、`refresh_interval`、`request_timeout`、`cache_ttl` 与域名列表；Collector 仍从既有运行时凭据读取 Gateway HMAC secret。
- 两份输出均为“只替换 `dns_resolver` 节点”的原子 YAML 更新，保留目标文件的其他配置和权限；禁用或删除该段时必须显式写入 `enabled: false`，不能让旧的启用配置残留。
- 任何解析、主机映射、字段校验或写入失败都在服务重启前返回错误；部署脚本不得再独立解析 TOML，也不得把完整 `custom.toml` 拷贝到 Trade 或 Collector 运行目录。
- 本地开发可以直接使用 `trade/config/app.yaml` 与 `collector/config/app.yaml` 中的 disabled 默认值；只有显式执行该命令或部署流程调用该命令后才启用远程 Resolver。

---

### Task 1: Extend custom.toml Resolver Configuration

**Files:**
- Modify: `modules/cli/internal/setup/config/config.go`
- Modify: `custom.toml`
- Modify: `custom.toml.example`
- Test: `modules/cli/internal/setup/config/config_test.go`

**Interfaces:**
- Produces `Manifest.DNSResolver` with `Enabled`, `TradeNode`, `RefreshIntervalSeconds`, `RequestTimeoutMS`, `LookupTimeoutMS`, `ProbeTimeoutMS`, `ProbePort`, `CacheTTLSeconds`, `MaxIPsPerDomain`, and `Domains`.
- Validates that `TradeNode` matches one `other_hosts[].name`, the resolved host has a non-empty address, intervals are positive, and domains are syntactically valid and unique.

- [x] Add the `DNSResolver` manifest struct and `toml:"dns_resolver"` field.
- [x] Add the production entry selecting `compute-1`; do not duplicate its address outside `other_hosts`.
- [x] Add the same section to `custom.toml.example` with a safe example node name and comments explaining that Trade receives a rendered YAML subset, not the full TOML.
- [x] Reject an enabled resolver that points to a missing host or to the control host.
- [x] Validate `probe_port` is a valid TCP port, `max_ips_per_domain` is in `1..4`, and probe/lookup/cache intervals are positive.
- [x] Test enabled production config, missing `compute-1`, duplicate domains, invalid intervals, invalid probe port, invalid max-IP cap, disabled resolver, and preservation of existing custom.toml files without the new section.
- [x] Run `cd modules/cli && go test ./internal/setup/config -count=1` and expect PASS.

### Task 2: Add Trade ResolveDomains Protocol

**Files:**
- Modify: `modules/trade/proto/trade_service.proto`
- Regenerate: `modules/trade/proto/tradegen/*`
- Modify: `modules/trade/internal/rpc/register.go`
- Test: `modules/trade/internal/rpc/register_test.go`
- Test: `scripts/tests/contract/test-trade-dns-resolver-contract.sh`

**Interfaces:**
- Produces `TradeDNSResolverService.ResolveDomains` and generated client/server proxy types for Collector.

- [x] Add the messages and service exactly as defined above.
- [x] Regenerate protobuf/tRPC code with the repository proto command (`make proto` from the repository root); never hand-edit generated files.
- [x] Add `TradeDNSResolverServiceName = "trpc.moox.trade.TradeDNSResolverService"` and register it in `rpc.RegisterAll`.
- [x] Add a contract test that checks the service, method, repeated domain input, per-domain `ResolvedIP.ip` and `tcp_connect_latency_ms`, response-level `unresolved_domains`, and the absence of the removed per-domain timestamp/error fields.
- [ ] Run `make proto-check` and `cd modules/trade && go test ./internal/rpc ./proto/tradegen -count=1` (the generated files and module tests pass; `make proto-check` was also attempted from clean temporary worktrees and remains blocked while `trpc-open create moox_common.proto` hangs, so this repository-level check is not claimed).

### Task 3: Implement Trade DNS Resolver Core

**Files:**
- Create: `modules/trade/internal/resolver/resolver.go`
- Create: `modules/trade/internal/resolver/validation.go`
- Create: `modules/trade/internal/resolver/resolver_test.go`
- Modify: `modules/trade/internal/config/config.go`
- Modify: `modules/trade/config/app.yaml`

**Interfaces:**
- `type Resolver struct { ... }`
- `func New(cfg Config) *Resolver`
- `func (r *Resolver) Resolve(ctx context.Context, domains []string, maxIPs int) []Resolution`
- `Resolution` contains the normalized domain, ordered `[]ResolvedIP`, and an `Unresolved` flag/reason used only for internal metrics; the RPC response maps that state to `unresolved_domains` and never exposes a free-form per-domain error.
- `ResolvedIP` contains `IP string` and `TCPConnectLatencyMS uint32`.

- [x] Normalize and validate domains before calling `net.Resolver.LookupHost`.
- [x] Use a context deadline from Trade configuration; never allow an RPC request to block the Trade server beyond its request context.
- [x] Keep a small in-memory cache keyed by normalized domain. A fresh cached result may be returned; expired entries must be re-resolved.
- [x] Return only `net.ParseIP(value).To4() != nil`, remove duplicates, and cap at the configured maximum (default four). For each candidate, perform a bounded TCP connect to `ip:probe_port` from the Trade node, record `tcp_connect_latency_ms`, discard failed probes, and sort by latency then IP for deterministic ties.
- [x] Reject localhost, loopback, link-local, private, multicast, IP-literal, URL, port, path, and empty domain inputs. The configured allowlist remains the final domain boundary.
- [x] Make one bad domain an unresolved item rather than failing the whole batch; log/metric the error category without returning raw resolver text in the protobuf.
- [x] Add tests for cache hit/expiry, lookup timeout, probe timeout, invalid domain, disallowed domain, IPv4 filtering, duplicate/capped results, latency ordering, partial probe failure, all-probe failure, and context cancellation.
- [x] Do not add a background Trade timer in this first version; Collector owns the five-minute refresh cadence and Trade cache avoids repeated lookups within one cadence.

### Task 4: Register Trade RPC and Keep Health Semantics Independent

**Files:**
- Create: `modules/trade/internal/rpc/dns_resolver.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/internal/config/app.go`
- Modify: `modules/trade/config/app.yaml`
- Modify: `modules/trade/internal/rpc/register.go`
- Test: `modules/trade/internal/rpc/dns_resolver_test.go`
- Test: `modules/trade/internal/bootstrap/bootstrap_test.go`

**Interfaces:**
- `type DNSResolverServer struct { Resolver *resolver.Resolver }`
- Implements generated `TradeDNSResolverService` and maps validation failures to `common.RetInfo`.

- [x] Add `AppConfig.DNSResolver` and load the rendered `dns_resolver` block from `trade/config/app.yaml`; a missing/disabled block keeps the resolver capability disabled without changing trading startup.
- [x] Construct the resolver from the rendered Trade config in Trade bootstrap; do not make the Trade process locate or parse `custom.toml`.
- [x] Pass the resolver server to `rpc.RegisterAll`.
- [x] Preserve the existing Trade `/readyz` behavior when DNS resolution is temporarily unavailable; DNS is an auxiliary capability and must expose failure through metrics/logs rather than make the trading process unhealthy.
- [x] Log only domain, result count, probe latency summary, duration, and error category; never log account credentials or service secrets.
- [x] Add metrics for request count, failed request count, unresolved-domain count, probe-failure count, and lookup/probe latency.
- [x] Test RPC mapping, auth-independent service registration, partial failures, and shutdown without goroutine leaks.
- [x] Run `cd modules/trade && go test -race ./internal/resolver ./internal/rpc ./internal/bootstrap -count=1`.

### Task 5: Add Collector Trade Resolver Client

**Files:**
- Create: `modules/collector/internal/dnsresolver/trade_client.go`
- Create: `modules/collector/internal/dnsresolver/trade_client_test.go`
- Modify: `modules/collector/internal/bootstrap/discovery.go`
- Modify: `modules/collector/internal/bootstrap/config.go`

**Interfaces:**
- `type DomainResolver interface { ResolveDomains(context.Context, []string) (map[string]sources.DNSResolution, error) }`
- `type TradeClient struct { ... }`
- `func NewTradeClient(target, nodeID string, credentials gatewayauth.Credentials, timeout time.Duration) *TradeClient`
- `sources.DNSResolution` carries the ordered `IPs` and an optional per-IP `LatencyMS` map; snapshot receipt time is assigned by the Collector coordinator, not received from Trade.

- [x] Use the generated Trade DNS proxy through the native Gateway target `ip://43.132.204.177:11003` and target node `compute-1`.
- [x] Use the Collector caller credential loaded from the process environment (`gatewayauth.CredentialsFromEnv`) and `gatewayauth.NewTRPCClientOptions`; do not reuse SysDeploy's `moox-service` credential and do not call `43.132.204.177:11210` directly.
- [x] Submit all configured domains in one RPC, not one RPC per domain.
- [x] Convert each successful `DomainResolution` to `sources.DNSResolution{IPs, LatencyMS}`. Do not reconstruct a per-domain timestamp or free-form error from the response.
- [x] Return an error only for request-level failure. Keep per-domain successful results when another domain appears in `unresolved_domains`.
- [x] Reject malformed or non-public IPs before they reach `BuildManagedEnvironment`.
- [x] Add tests for target/node propagation, batch request shape, ret_info failure, unresolved-domain handling, latency ordering, timeout, and response normalization.

### Task 6: Make Collector DNS Refresh Prefer Trade Results

**Files:**
- Create: `modules/collector/internal/dnsresolver/coordinator.go`
- Modify: `modules/collector/internal/dnscache/cache.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/bootstrap/config.go`
- Test: `modules/collector/internal/dnsresolver/coordinator_test.go`
- Test: `modules/collector/internal/dnscache/cache_test.go`

**Interfaces:**
- `type Coordinator struct { Local *dnscache.Cache; Remote DomainResolver; ... }`
- `func (c *Coordinator) Refresh(ctx context.Context) error`
- `func (c *Coordinator) Snapshot() map[string]sources.DNSResolution`
- `func (c *Coordinator) Due(time.Time) bool`

- [x] Keep the current `dnsSnapshotter` interface used by the reconciler and scheduler; replace only the concrete cache dependency in bootstrap.
- [x] When `source=trade` or `source=hybrid`, call Trade once per refresh interval with `cfg.DNS.Domains`.
- [x] In `hybrid` mode use Trade results first, then local results only for domains missing from the Trade response; do not replace a valid Trade route with a local route merely because both exist.
- [x] Preserve the Trade-provided latency order when building the route list; use lexical order only for local-DNS results or equal-latency ties. Do not sort all routes again in a way that discards the probe ranking.
- [x] On Trade failure, retain the last known-good Trade snapshot; if no Trade snapshot exists, perform the existing local DNS refresh.
- [x] Never publish an empty `MOOX_MARKET_FETCH_DNS_ROUTES_JSON` solely because the Resolver request timed out.
- [x] Reject stale remote results older than `cache_ttl_seconds`; use the local resolver only for missing/expired domains.
- [x] Keep refresh serialized and context-bounded. A slow Trade request must not overlap the next refresh or block the market-fetch timer handler.
- [x] Store source, Collector receipt time, hash, per-IP latency, and last error category in memory for health details/metrics; do not put timestamps, latency, or diagnostic fields into the SCF route JSON.
- [x] Preserve `BuildManagedEnvironment`, `MOOX_MARKET_FETCH_DNS_HASH`, and CloudNode patch idempotency. A route hash change is the only reason to submit new environment values.
- [x] Test startup with Trade success, Trade timeout, local fallback, partial domains, stale response, empty response, unchanged hash, changed hash, concurrent timer calls, and cancellation.

### Task 7: Render custom.toml Resolver Settings into Trade and Collector Runtime

**Files:**
- Modify: `modules/cli/internal/setup/config/config.go`
- Create/Modify: `modules/cli/internal/setup/config/runtime_config.go`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/internal/command/collector.go`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/collector/config/app.yaml`
- Modify: `modules/collector/internal/bootstrap/config.go`
- Modify: `modules/trade/config/app.yaml`
- Modify: `modules/trade/internal/config/app.go`
- Test: `modules/cli/internal/command/collector_test.go`
- Test: `modules/cli/internal/setup/config/trade_runtime_test.go`
- Test: `scripts/tests/contract/test-deploy-moox-collector-dns-resolver.sh`

**Interfaces:**
- `moox-cli setup render-runtime-config` loads one `custom.toml` snapshot and invokes the Trade/Collector renderers; each renderer extracts only its owned resolver fields and merges them into the `dns_resolver` YAML node.
- `func RenderTradeDNSResolverConfig(snapshot *setupconfig.Snapshot, existing []byte) ([]byte, error)` extracts only the Trade-owned resolver fields and merges them into the `dns_resolver` YAML node.
- `func RenderCollectorDNSResolverConfig(snapshot *setupconfig.Snapshot, existing []byte) ([]byte, error)` extracts the Collector transport/refresh subset and derives the target from `other_hosts[trade_node]`.
- Deployment invokes the same CLI command rather than parsing TOML in Shell, and transports the resulting Trade/Collector files together. `MOOX_COLLECTOR_DNS_RESOLVER_*` is derived from the same `dns_resolver` manifest snapshot and the `other_hosts` record named `compute-1`.
- The Trade target receives a rendered `config/app.yaml`; it never reads `custom.toml` and never receives SSH/cloud/Gateway secrets through this path.

- [x] Derive `ip://43.132.204.177:11003` from the configured `compute-1` host address; do not hardcode the address in Go source or SCF package code.
- [x] Implement `moox-cli setup render-runtime-config` with required `--file` and optional `--trade-output`/`--collector-output` paths. It must load `custom.toml` once, render both outputs from the same snapshot, and reject a request that provides neither output.
- [x] During `moox-cli` setup/deploy, render the sanitized Trade `dns_resolver` block atomically into the target Trade `config/app.yaml`; preserve all unrelated YAML nodes and file permissions. Do not mutate the repository source file `modules/trade/config/app.yaml` as a side effect of parsing `custom.toml`.
- [x] Call the single render command from the existing setup/deploy stage after the `custom.toml` snapshot is loaded, and pass the same rendered bytes to local and remote deployment paths; do not add a second independent TOML parser in the shell script.
- [x] Render Trade fields `enabled`, `domains`, `lookup_timeout_ms`, `probe_timeout_ms`, `probe_port`, `cache_ttl_seconds`, and `max_ips_per_domain`; keep `trade_node`, `refresh_interval_seconds`, and Collector `request_timeout_ms` out of the Trade block because they belong to deployment/Collector orchestration.
- [x] Render the Collector subset from the same snapshot: `enabled`, `domains`, `target`, `node_id`, `refresh_interval`, `request_timeout`, and `cache_ttl`. Derive `target` with `net.JoinHostPort` from the selected `other_hosts` address and add the `ip://` scheme; never hardcode `43.132.204.177` in the renderer.
- [x] When the section is absent or disabled, render `enabled: false` (or remove only the generated resolver node) so a previous enabled resolver cannot survive an explicit disable. Never delete unrelated Trade configuration.
- [x] Pass `MOOX_COLLECTOR_DNS_RESOLVER_NODE_ID=compute-1` so Gateway HMAC `TargetNode` matches the remote Gateway identity.
- [x] Keep local development defaults disabled or local-DNS-only when no `dns_resolver` section is present.
- [x] Ensure remote deployment transports the rendered Trade app config and Collector resolver target/node/timeout/enabled values just like the existing Factor worker/read configuration; local and remote deployment must behave identically.
- [x] Ensure remote deployment transports the rendered Trade app config and Collector resolver environment together, before restarting Trade/Collector; a failed render must abort before any service restart.
- [x] Validate that resolver target is not loopback when enabled and that the target host matches the selected custom.toml host.
- [x] Add tests that parse `custom.toml`, render Trade YAML, preserve unrelated `database/admin/eventbus/runtime` nodes, replace stale resolver settings on re-render, and prove no SSH/cloud/Gateway credentials are present in the rendered Trade block.
- [x] Add a contract test checking the rendered Collector environment contains `compute-1` and `ip://43.132.204.177:11003`, and the staged Trade `config/app.yaml` contains the same domain list/timeouts while no credentials are printed or copied into SCF environments.
- [x] Add a preserve/overlay assertion that the Collector-only deployment does not rewrite Trade secrets or unrelated service environments.

### Task 8: Preserve SCF Runtime Semantics and Add Route-Source Diagnostics

**Files:**
- Modify: `modules/collector/internal/marketfetch/timer.go`
- Modify: `modules/collector/internal/httpclient/client.go`
- Modify: `modules/collector/internal/marketfetch/metrics.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Test: `modules/collector/internal/marketfetch/timer_test.go`
- Test: `modules/collector/internal/httpclient/client_test.go`
- Test: `modules/collector/internal/marketfetch/metrics_test.go`

- [x] Keep the URL hostname unchanged when dialing a returned IP, so HTTPS Host and TLS SNI remain `fapi.binance.com`.
- [x] Keep the current order: injected IPs first, one normal hostname fallback after all injected IPs fail.
- [x] Add structured logs/metrics for `dns_source`, `dns_hash`, `dns_route_age`, `resolved_ip_attempted`, and `resolved_ip_failed`; never log secrets.
- [x] Do not make malformed/empty DNS routes fatal to a Timer invocation; preserve the documented fallback behavior.
- [x] Add tests proving the injected IP path retains SNI/Host, falls back after all IPs fail, and records the route hash/source without changing the request protocol.

### Task 9: Update Architecture and Operational Documentation

**Files:**
- Modify: `docs/architecture/scf-short-lived-market-fetch.md`
- Modify: `docs/架构总览.md`
- Modify: `modules/collector/README.md`
- Modify: `modules/trade/README.md`

- [x] Replace the statement that Collector always resolves with the host resolver by the source model: Trade Resolver -> Collector snapshot, with local fallback.
- [x] Document that `compute-1 (43.132.204.177)` is the configured single Resolver node, not a 46-node Resolver fleet.
- [x] Document `ResolveDomains`, response-level unresolved domains, per-IP TCP probe latency, allowed domains, five-minute refresh, last-known-good behavior, four-IP cap, and Gateway authentication.
- [x] Document that `custom.toml` is parsed by `moox-cli`; Trade receives only the rendered `dns_resolver` YAML subset and must not read the full TOML directly.
- [x] Document that DNS success and HTTPS reachability are separate checks; a returned IP must still pass an SCF egress probe.
- [x] Document rollback: set resolver source to local, keep current SCF route snapshot, then redeploy Collector environment.
- [x] Remove or explicitly mark the old unused `dnsproxy` configuration in `modules/collector/configs/config.yaml`; there must be one effective DNS configuration path.

### Task 10: Verification and Production Rollout

**Files:**
- Test: `modules/trade/test/resolve_domains_e2e_test.go`
- Test: `modules/collector/test/trade_dns_scf_environment_e2e_test.go`
- Create: `scripts/tests/e2e/test-trade-dns-collector-scf-e2e.sh`

- [x] Run local protocol/core tests (all listed module tests pass; the repository-level `make proto-check` remains open because its generator hangs):

```bash
make proto-check
cd modules/trade && go test -race ./internal/resolver ./internal/rpc ./internal/bootstrap -count=1
cd modules/collector && go test -race ./internal/dnsresolver ./internal/dnscache ./internal/bootstrap ./internal/marketfetch ./internal/httpclient -count=1
```

- [x] Run contract checks:

```bash
bash scripts/tests/contract/test-trade-dns-resolver-contract.sh
bash scripts/tests/contract/test-deploy-moox-collector-dns-resolver.sh
git diff --check
```

- [x] Render and inspect the target Trade `config/app.yaml`; verify it contains the expected sanitized resolver fields and no custom.toml credentials. Deploy/restart Trade on `compute-1` at `43.132.204.177`; verify `ResolveDomains(["fapi.binance.com"])` returns at least one valid IP with a positive TCP probe latency when the endpoint is reachable.
- [x] From the control Collector, call the RPC through Gateway using target node `compute-1`; verify the request succeeds without exposing Trade's native port.
- [x] Trigger one Collector DNS refresh and inspect every active SCF Timer environment. Confirm the same route hash and `fapi.binance.com` IP list are present.
- [x] Run the real SCF egress probe and record separately: DNS route applied, TCP/443 connection, HTTPS `/fapi/v1/ping`, and K-line API response.
- [x] If Trade DNS succeeds but all Trade-side TCP probes or SCF TCP/443 still time out, stop the rollout and report the remaining network/egress restriction; do not mark the Resolver implementation as fixing Binance connectivity.
- [ ] After successful egress, run one perpetual K-line batch and verify Storage rows, `DatasetPeriodCollected`, and the next `perpetual_kline_1h` readiness state (the production Timer functions assigned in ap-shanghai/ap-guangzhou still time out at `fapi.binance.com`; a Singapore catch-up invocation succeeded at the function level but did not produce the required Timer/readiness evidence, so this item remains open).
- [x] Test failure recovery by stopping/unreachable Trade: Collector must retain the previous route hash, SCF must continue using the last snapshot, and no empty environment patch may be submitted.
- [x] Test rollback to local mode and verify the old Collector-only DNS path remains functional.

## Acceptance Criteria

## 执行记录（2026-08-11）

本计划的代码、协议、运行时配置、部署脚本、诊断信息和测试已按上述边界落地。以下是本次实际执行证据：

- Trade `ResolveDomains` 已在 `compute-1 (43.132.204.177)` 的 `127.0.0.1:11203` 启动，Gateway 在 `0.0.0.0:11003` 提供 `compute-1` 路由；Collector 使用 Gateway HMAC 调用，不直连 Trade 原生端口。
- Collector 已部署到 control，Trade/Gateway 已部署到 compute-1；重启后 control 全量服务、compute-1 Trade/Gateway 均保持运行。部署后二进制 hash 已与本地 Linux/amd64 构建产物核对。
- 本地验证通过：Trade、Collector、CLI、Admin 相关 race/focused tests，DNS resolver 两个合同脚本，`bash -n scripts/deploy-moox.sh` 和 `git diff --check`。
- 真实生产 E2E 已通过：`TestResolveDomainsProductionE2E`、`TestTradeDNSCollectorEnvironmentProductionE2E`。CloudNode 数据库回读显示 44 个 active timer 均有非空 DNS 路由；脚本允许短暂滚动窗口并轮询，最终要求 44 个 active timer 收敛到同一个 DNS hash，本次已收敛。
- 最近一次线上复验（当前 Collector 二进制 `0239f333589803f6a5cef407ff4405655f3a722661530886fad8b1183e7c45f0`）再次通过：Trade/Collector 两个 production E2E 均通过，重启 control Collector 后先从滚动中的多 hash 收敛到 `deployed=44 enabled=44 populated=44 distinct=1`，最终 live Collector health 的 managed hash 为 `086914537650d399`。脚本把本轮本地 RPC hash作为诊断，同时从重启后 Collector health 读取同一进程的 managed hash作为传播目标，并逐节点比较，避免 DNS 探测时延抖动导致假失败或旧统一 hash 假通过；health 还必须报告 `source=trade`、域名数量为3且无错误，确保验证的是 Trade 到 live Collector 链路；缺失/重发布中的运行时字段不会把 Timer 从验收分母中静默排除，只有明确 provider readback disabled 的节点才排除。由于 CloudNode 的 44 节点异步批量更新可能超过三分钟，回读窗口扩为最多六分钟。
- Collector health 现在同时暴露 coordinator 诊断 hash 与 CloudNode/SCF 使用的 `managed_hash`；生产脚本读取后者，避免把包含探测延迟元数据的内部 hash误当作环境变量 hash。
- 最新持久化版本 Collector 二进制 `0239f333589803f6a5cef407ff4405655f3a722661530886fad8b1183e7c45f0` 已部署到 control。动态 E2E 随后通过，44/44 Timer 收敛到 `086914537650d399`；Collector 重启后的生产健康检查也确认快照文件权限为 `0600`。
- 追加验收：生产 `ap-shanghai-9`（30 个 perpetual 标的）和 `ap-guangzhou-8`（14 个）直接 Timer 调用均返回 `fapi.binance.com` 请求超时；`perpetual_kline_1h` 的 `16:00Z` 与 `15:00Z` 周期仍为 `waiting`、284 个 subject pending。Singapore 函数的单标的 perpetual catch-up 调用返回 `succeeded`，但它不是 readiness 任务且未形成 `DatasetPeriodCollected`，不能替代完整 Timer 验收。为避免污染现网，临时 Singapore 环境 patch 已成功回滚，节点恢复原 spot assignment、Timer enabled 和 assignment hash。
- `make proto-check` 的阻塞已在干净临时 worktree 复现为 `trpc-open create moox_common.proto` 长时间无输出；进程已清理，未留下额外 worktree。该工具链阻塞与生成代码/模块测试结果分开记录。
- 2026-08-12 重新发布：本地构建 `linux/amd64` Trade、Collector、Admin server/CLI 二进制并通过 Trade/Collector/Admin focused race tests。Trade 已部署到 `43.132.204.177:/home/ubuntu/moox/trade-move`，Collector/Admin 已部署到 `106.53.107.122:/home/ubuntu/moox/prod`；旧二进制均保留为 `*.before-dns-20260812`，服务重启后 ready/status 均正常。
- 本次线上二进制摘要：Trade `6b7ee25480da93b1f1c4db4c279ce92a1749b570d016ecb1192d1accc405dd28`；Collector `76a2715951185feeadf118371eadfe9836215a1d3b9bd6cf0e62459c57ca1f8c`；Admin `151a095a62e4506cd5f34dd40e28110f25934ef40753029b6a93eb8081fafac3`。生产 Trade `ResolveDomains(fapi.binance.com)` 通过，Collector DNS 生产 E2E 通过，CloudNode 回读 `44/44` active Timer 已启用、有 DNS 路由且最终统一为 live managed hash `17f4770982a7572e`。
- 本轮补跑本地验证通过：Trade resolver/RPC/bootstrap/config、Collector dnsresolver/dnscache/bootstrap/marketfetch/httpclient（均 `go test -race`），CLI setup/command、Admin CLI/sysdeploy，两个 DNS 合同脚本、部署脚本语法检查和 `git diff --check` 均通过。`make proto-check` 的生成器在 dirty 与 clean 临时 worktree 都卡在 `trpc-open create moox_common.proto`，因此不误报为通过。
- 故障恢复复验（最新二进制）已完成：停止 compute-1 Trade 后重启 Collector，health 保留 `managed_hash=086914537650d399`、3 个路由并报告 `trade_rpc`，没有清空快照或提交空环境；恢复 Trade 后重启 Collector，health 回到 `source=trade`、`managed_hash=b8fdd6f477bbcb6f`、无错误。
- `perpetual_kline_1h` 的生产批次未标记为成功：Collector 数据库中对应任务的最近结果仍记录 `fapi.binance.com` 请求超时（`network_error`/`http_5xx`），当前周期 readiness 为 waiting/degraded。该结果说明部分 SCF 地域的 Binance 出口仍受限，不能把 DNS Resolver 链路验收误写成 K 线批次已恢复；待云端出口策略/地域权限处理后再补跑该项。
- 为验证该阻断，本轮通过 CloudNode 内部受控 `InvokeFunction` 手工触发了 `ap-shanghai-9` 和 `ap-guangzhou-8` 的永续 Timer 批次；两个批次均被 SCF 接受，但分别返回 30/14 个 `network_error`，错误均为 `https://fapi.binance.com/fapi/v1/klines` 请求超时。触发后 Collector readiness 最新的 `16:00Z`、`15:00Z` 周期仍为 `waiting`，每周期 284 个 item 全部 `pending`；未产生可核验的 Storage 行或 `DatasetPeriodCollected`，因此该验收项继续保持未完成。
- 生产 E2E 还覆盖了返回 IP 的公网校验、正 TCP 延迟、去重、最多四个候选和延迟升序；expected hash 测试输入与 live Collector 一样合并 Trade、legacy DNS 和 Collector resolver 三组域名，避免自定义域集合时产生假失败。
- compute-1 出口分层探测已通过：`fapi.binance.com` DNS 返回、TCP/443 建连、HTTPS `/fapi/v1/ping` 返回 `{}`、K-line API 返回最近一根数据；这只证明 compute-1 出口，不等同于所有 SCF 地域出口。
- SCF 出口探测需单独看待：已观测到部分地域（包括新加坡节点）可完成公网 IP、HTTPS 和 Binance 访问，但部分地域的公网探针连接被拒绝/重置；因此当前方案证明了 Trade Resolver、Collector 路由同步和 compute-1 出口，不把所有 SCF 地域的 Binance 可达性误写成已验证。
- 从本地直接读取腾讯 SCF 运行时环境仍需要单独的云端权限窗口；Collector local-source 回滚已按下述受控演练验证。
- 本轮已完成受控 local rollback 演练：临时将 control Collector 的 `dns_resolver.enabled` 设为 `false` 并重启，健康检查返回 `ready=true`、`dns_resolver.enabled=false`、`source=local`；随后自动恢复原配置并重启，健康检查恢复 `enabled=true`、`source=trade`、3 个路由且无错误。未留下临时配置。
- `make proto-check` 的 proto 生成命令在本地会卡在 `trpc-open create moox_common.proto`；生成文件和独立 Trade proto/module 测试已通过，但不能把仓库目标误报为通过。

### 独立 CodeCR 复核

独立 `codeCR` 已复核最新代码，未发现残留 P0/P1/P2。此前指出的协议边界（最大 IP 数、未知域名响应、响应接收时间、目标公网校验）均已补齐并通过 focused tests。剩余为非阻断验收缺口：生产 E2E 已强制 Collector/CloudNode SSH 和 CloudNode 元数据回读；真实腾讯 SCF 环境直读、local rollback 仍需线上运维窗口执行。

- 末次复核还确认 `trade_service.trpc.go`/`trade_service.pb.go` 与 proto 对齐，`ResolveDomainsReq.Validate` 已覆盖批量上限和空域名边界；Trade、Collector、Admin、CLI focused/race 测试、两个 DNS 合同、`bash -n` 与 `git diff --check` 均通过。
- `make proto` 已成功生成相关 Trade 文件；后续 `make proto-check` 在独立临时 worktree 仍卡在 `trpc-open create moox_common.proto`，因此不将仓库目标误报为通过。Trade `proto/tradegen` 安全测试已单独通过。

- `ResolveDomains` accepts one or more configured domains through the authenticated Trade RPC on `compute-1 (43.132.204.177)` and returns normalized IPv4 results with Trade-side TCP probe latency; unresolved domains are reported at response level.
- `moox-cli` parses `custom.toml` once and renders the sanitized Trade `dns_resolver` YAML block plus Collector transport/refresh settings; Trade never parses the full TOML.
- Collector requests the Resolver once per configured interval, not once per SCF function or per domain.
- The returned routes are propagated through the existing CloudNode Environment Patch and appear in all relevant SCF Timer environments under the existing DNS environment variables.
- A Trade RPC timeout, partial DNS failure, or Collector restart never erases the last good SCF route.
- SCF continues to preserve Host/SNI and falls back to hostname DNS when the injected route fails.
- Production verification distinguishes DNS resolution from TCP/HTTPS reachability and records the result for `fapi.binance.com`.
- Existing local DNS mode, Storage/Collector scheduling, and unrelated Trade trading behavior remain unchanged.

## Self-Review Checklist

- The single configured resolver host is `compute-1`, mapped from `custom.toml` to `43.132.204.177`; no 46-machine fan-out remains in the plan.
- Trade receives only the rendered resolver subset; `trade_node`, refresh orchestration, and Gateway transport remain deployment/Collector concerns.
- No per-domain `resolved_at_unix_ms` or `error` remains in the protocol; latency is an active TCP probe measurement, while Collector receipt time is the snapshot timestamp.
- The plan uses the existing DNS environment and CloudNode patch path rather than introducing a second SCF protocol.
- The plan does not claim that an IP returned by Trade guarantees SCF connectivity; real SCF egress remains an explicit gate.
- Every failure path has a safe behavior: retain last-known-good, local fallback, or stop rollout without clearing routes.
- Every new cross-module RPC has generated-code, auth, boundary, unit, contract, and production checks.
