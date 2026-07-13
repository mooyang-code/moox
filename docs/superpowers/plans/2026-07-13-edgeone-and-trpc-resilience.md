# EdgeOne Perimeter And tRPC Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put MooX public browser and service edges behind Tencent EdgeOne, keep all origin services private, and establish a tRPC baseline for recovery, validation, metrics, and production logging.

**Architecture:** EdgeOne provides public DNS/CDN/WAF/CC protection. Managed Caddy remains the only public-origin process and proxies only to loopback web-host and Admin listeners. The tRPC rollout standardizes the mature plugins already adopted by MooX; retries, masking, metadata barriers, and tracing are method-scoped pilots, never global switches.

**Tech Stack:** Tencent EdgeOne, Tencent Cloud security groups, managed Caddy v2.11.4, Go 1.24, tRPC-Go v1.0.3, recovery v1.0.0, validation v1.0.1, Prometheus v1.0.0, CLS v1.0.0, Go tests, and Bash contract tests.

---

## Locked Decisions

| ID | Decision |
|---|---|
| D1 | EdgeOne fronts the browser edge on port 9527 and service edge on port 11001. web-host, Admin, health, metrics, and module RPC ports are private. |
| D2 | The first rollout uses CNAME access, limiting DNS impact to selected hosts and enabling rollback by restoring the recorded prior DNS record. |
| D3 | EdgeOne uses HTTPS origin pull to the existing Caddy ports. First rollout accepts the default origin-certificate behavior; a paid-tier follow-up imports a dedicated CA and enables origin validation. |
| D4 | Origin security groups permit only EdgeOne origin-pull IP ranges. Do not trust forwarded client-IP headers for authorization until Caddy trusts only those ranges. |
| D5 | The required tRPC baseline is recovery, existing validation where supported, Prometheus, and console/CLS logging. |
| D6 | Prometheus stays under `plugins.metrics.prometheus` with `enablepush: false`. No Pushgateway or central Prometheus server is introduced. |
| D7 | Slime is allowed only for explicitly idempotent Storage reads after an injected failure test. It is forbidden for trade writes, task creation, timers, event publication, and acknowledgments. |
| D8 | The current MooX JWT/HMAC gateway filters remain authoritative. The generic JWT plugin cannot preserve MooX claim checks, metadata injection, request signatures, and no-auth rules. |
| D9 | OpenTelemetry requires a separate collector compatibility and privacy pilot. Do not deploy Jaeger in parallel. |

## Preconditions

1. Complete [the managed Caddy plan](/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/superpowers/plans/2026-07-13-admin-https-request-signing-health-auth.md) first.
2. Obtain an ICP-filed production domain for China Mainland/global acceleration. Record DNS TTL, existing record, origin IP, security-group ID, and rollback owner.
3. Create EdgeOne budget and usage alerts before enforcement.
4. Use an isolated worktree. The current main checkout has unrelated uncommitted deployment and authentication work.

## Target Flow

```text
browser or service client
  -> EdgeOne: TLS, WAF, CC/Bot, rate rules, cache
  -> Caddy browser edge or service edge
  -> loopback web-host | admin-control | admin-service
  -> MooX module RPC

module RPC
  -> recovery -> validation -> business handler -> prometheus
  -> console / CLS structured logging
```

## File Map

| Path | Responsibility |
|---|---|
| `docs/运维/EdgeOne接入与应急回滚.md` | Console onboarding, rules, firewall, probes, rollback, and cost control. |
| `deploy/caddy/Caddyfile` | EdgeOne-compatible origin routing and trusted-proxy policy. |
| `scripts/test-edgeone-origin-contract.sh` | Caddy routing and security contract. |
| `web-host/main.go` | Timeouts, method/body bounds, and safe origin logging. |
| `web-host/health_test.go` | Static origin regressions. |
| `modules/*/config/trpc_go.yaml` | Uniform filters and exporter wiring. |
| `modules/*/cmd/server/main.go` | Anonymous plugin registration imports. |
| `scripts/test-trpc-plugin-config.sh` | Config/import/dependency consistency. |
| `packages/trpcplugintest/recovery_test.go` | Real recovery plugin integration test. |
| `docs/运维/tRPC插件运行基线.md` | Rollout matrix and advanced-pilot boundaries. |

## Task 1: Capture The EdgeOne Operating Contract

**Files:**
- Create: `docs/运维/EdgeOne接入与应急回滚.md`
- Create: `scripts/test-edgeone-origin-contract.sh`
- Test: `scripts/test-edgeone-origin-contract.sh`

- [ ] **Step 1: Write the operator runbook**

Document CNAME onboarding for browser and service hosts, HTTPS origin pull, EdgeOne certificate issuance, origin firewall allowlisting, monitoring, and DNS rollback.

Create the following rules in observation mode and promote them independently after one day of valid traffic:

| Rule | Match | Initial action |
|---|---|---|
| Login | `/api/admin/auth/login` | 10 requests/minute/client IP, JavaScript challenge |
| Admin API | `/api/admin/*`, excluding login | 120 requests/minute/client IP, log then block |
| Service API | `/api/service/*` | 120 requests/minute/client IP, log then block |
| Static assets | JS/CSS/fonts/images | edge cache, no restrictive application rate |
| Diagnostics | health, ready, metrics paths | block |

- [ ] **Step 2: Implement a local origin contract test**

Create a Bash test with these assertions:

```bash
grep -Fq 'admin 127.0.0.1:2019' deploy/caddy/Caddyfile
grep -Fq 'reverse_proxy 127.0.0.1:9528' deploy/caddy/Caddyfile
grep -Fq 'reverse_proxy 127.0.0.1:11000' deploy/caddy/Caddyfile
grep -Fq 'reverse_proxy 127.0.0.1:11002' deploy/caddy/Caddyfile
grep -Fq 'handle /api/admin/*' deploy/caddy/Caddyfile
grep -Fq 'handle /api/service/*' deploy/caddy/Caddyfile
grep -Fq 'Content-Security-Policy' deploy/caddy/Caddyfile
```

When `CADDY_BIN` exists, also run Caddy validation; otherwise print one SKIP line while retaining static assertions.

- [ ] **Step 3: Verify and commit**

Run: `bash scripts/test-edgeone-origin-contract.sh`

Expected: all routing/security assertions pass and Caddy validation either passes or emits the single documented SKIP line.

```bash
git add docs/运维/EdgeOne接入与应急回滚.md scripts/test-edgeone-origin-contract.sh
git commit -m "docs: add edgeone origin protection runbook"
```

## Task 2: Enforce The Private-Origin Invariant

**Files:**
- Modify: `deploy/caddy/Caddyfile`
- Modify: `scripts/deploy-moox.sh`
- Modify: `scripts/test-caddy-config.sh`
- Modify: `scripts/test-edgeone-origin-contract.sh`

- [ ] **Step 1: Add failing listener-contract checks**

The test must assert that Caddy admin stays at loopback and browser and service edges stay separate. Check the configured browser port 9527 and service port 11001 exactly once each.

- [ ] **Step 2: Configure proxy trust safely**

Add Caddy trusted-proxy configuration generated from a reviewed EdgeOne origin-pull IP range artifact. Do not hard-code a copied range list in the Caddyfile. Before this artifact is operational, Caddy must not use forwarded headers for authorization or client-IP rate limiting.

- [ ] **Step 3: Reject public upstream listeners**

Make `scripts/deploy-moox.sh` reject all effective values except the explicit `127.0.0.1:port` form for:

```text
MOOX_WEB_HOST_ADDR
MOOX_WEB_HOST_HEALTH_ADDR
MOOX_ADMIN_CONTROL_ADDR
MOOX_ADMIN_SERVICE_ADDR
```

Do not accept wildcard addresses, IPv6 wildcard addresses, hostnames, or arbitrary interfaces.

- [ ] **Step 4: Verify and commit**

```bash
bash scripts/test-caddy-config.sh
bash scripts/test-edgeone-origin-contract.sh
bash scripts/test-deploy-moox-https.sh
git add deploy/caddy/Caddyfile scripts/deploy-moox.sh scripts/test-caddy-config.sh scripts/test-edgeone-origin-contract.sh
git commit -m "feat: prepare caddy origin for edgeone"
```

Expected: only the Caddy browser/service listeners can face the network; all upstream and diagnostic listeners are loopback-only.

## Task 3: Bound web-host At The Origin

**Files:**
- Modify: `web-host/main.go`
- Modify: `web-host/health_test.go`

- [ ] **Step 1: Add failing method and timeout tests**

```go
func TestStaticHandlerRejectsNonReadMethods(t *testing.T) {
	h := newStaticHandler(unavailableFS{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodTrace} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, "/", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, rr.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestHTTPServerConfigHasFiniteTimeouts(t *testing.T) {
	s := newHTTPServer("127.0.0.1:9528", newStaticHandler(unavailableFS{}))
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Fatalf("unsafe server limits: %+v", s)
	}
}
```

- [ ] **Step 2: Implement the bounded server**

Create `newHTTPServer` and use it for static and health listeners:

```go
return &http.Server{
	Addr:              addr,
	Handler:           handler,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       15 * time.Second,
	WriteTimeout:      30 * time.Second,
	IdleTimeout:       60 * time.Second,
	MaxHeaderBytes:    16 << 10,
}
```

In the static handler, allow only GET and HEAD; otherwise return 405 with `Allow: GET, HEAD`. Reject a non-empty request body with 413. Preserve 404 behavior for APIs, diagnostics, and missing assets.

Remove per-request success logging. Error logs may contain method, normalized path, status, and error category only. Never log headers, cookies, query strings, credentials, or bodies.

- [ ] **Step 3: Verify and commit**

```bash
go test -count=1 ./web-host
go vet ./web-host
git add web-host/main.go web-host/health_test.go
git commit -m "feat: harden web host origin limits"
```

## Task 4: Normalize Prometheus And Validation Wiring

**Files:**
- Modify: `modules/cloudnode/config/trpc_go.yaml`
- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/factor/config/trpc_go.yaml`
- Modify: `modules/eventbus/config/trpc_go.yaml`
- Modify: `modules/monitor/config/trpc_go.yaml`
- Modify: `modules/hostagent/config/trpc_go.yaml`
- Modify: `modules/strategy/config/trpc_go.yaml`
- Modify: `modules/archive/config/trpc_go.yaml`
- Create: `scripts/test-trpc-plugin-config.sh`

- [ ] **Step 1: Create a failing plugin-matrix test**

Enumerate these production modules:

```text
admin cloudnode collector eventbus factor hostagent monitor storage strategy trade archive
```

For every configured filter, assert the server binary contains the matching anonymous plugin import. For every Prometheus exporter, assert:

```yaml
plugins:
  metrics:
    prometheus:
      enablepush: false
```

Reject the invalid top-level shape:

```bash
rg -n '^  prometheus:$' modules/*/config/trpc_go.yaml && exit 1
```

- [ ] **Step 2: Repair known exporter defects**

Move cloudnode, collector, and factor from `plugins.prometheus` to `plugins.metrics.prometheus`, retaining their existing address, port, path, and disabled Pushgateway value.

For eventbus, monitor, hostagent, strategy, and archive choose and encode one exact state:

```text
managed exporter: import + prometheus filter + valid exporter config
not an exporter: no import, no filter, only the existing health surface
```

No module may remain half-configured.

- [ ] **Step 3: Establish filter order**

```yaml
server:
  filter:
    - recovery
    - validation
    - cors
    - spacectx
    - prometheus
```

Omit validation where there is no supported protobuf validation surface, omit cors except admin/storage/trade, omit spacectx except trade, and omit prometheus only for deliberately unmanaged exporters.

- [ ] **Step 4: Verify and commit**

```bash
bash scripts/test-trpc-plugin-config.sh
go test -count=1 ./modules/admin/... ./modules/cloudnode/... ./modules/collector/... ./modules/factor/...
go test -count=1 ./modules/eventbus/... ./modules/monitor/... ./modules/storage/... ./modules/trade/...
git add modules/*/config/trpc_go.yaml scripts/test-trpc-plugin-config.sh
git commit -m "fix: standardize trpc metrics configuration"
```

## Task 5: Add tRPC Recovery To Every Production Server

**Files:**
- Modify: `modules/{admin,archive,cloudnode,collector,eventbus,factor,hostagent,monitor,storage,strategy,trade}/go.mod`
- Modify: matching server entrypoints and tRPC configs
- Create: `packages/trpcplugintest/recovery_test.go`
- Modify: `scripts/test-trpc-plugin-config.sh`

- [ ] **Step 1: Write an actual recovery-filter integration test**

Create an in-process tRPC server with a handler that panics `recovery-test`. Enable the recovery plugin, issue a request, and assert:

```text
the call returns a controlled tRPC error instead of panicking the test process
a later normal request succeeds
the response includes neither a stack trace nor the panic text
```

Do not put a local defer/recover in the test handler.

- [ ] **Step 2: Add dependency, import, and filter**

Add this dependency to all listed modules:

```text
trpc.group/trpc-go/trpc-filter/recovery v1.0.0
```

Add exactly one anonymous import to each server entrypoint:

```go
_ "trpc.group/trpc-go/trpc-filter/recovery"
```

Place `recovery` first in every server filter list. Modules without a list receive recovery plus only filters intentionally supported by that module.

- [ ] **Step 3: Preserve non-RPC recovery**

Do not remove panic recovery from `packages/cloudruntime` or workflow executors. Those protect asynchronous work and are not duplicates of the RPC boundary filter.

- [ ] **Step 4: Verify and commit**

```bash
go test -count=1 ./packages/trpcplugintest
bash scripts/test-trpc-plugin-config.sh
go test -count=1 ./modules/admin/... ./modules/storage/... ./modules/trade/...
git add modules/*/go.mod modules/*/go.sum modules/*/cmd/server/main.go modules/*/config/trpc_go.yaml packages/trpcplugintest/recovery_test.go scripts/test-trpc-plugin-config.sh
git commit -m "feat: recover panics at trpc server boundaries"
```

## Task 6: Make CLS And Sensitive-Data Rules Explicit

**Files:**
- Modify: `modules/admin/config/trpc_go.yaml`
- Modify: `modules/cloudnode/config/trpc_go.yaml`
- Modify: `modules/collector/config/trpc_go.yaml`
- Modify: `modules/factor/config/trpc_go.yaml`
- Create: `docs/运维/tRPC插件运行基线.md`
- Modify: `scripts/test-trpc-plugin-config.sh`

- [ ] **Step 1: Document log routing**

| Environment | Writers | Required fields | Forbidden content |
|---|---|---|---|
| local/test | console | module, service, method, trace ID, code, duration | credentials, raw bodies, JWT, HMAC, API secret |
| production | console plus CLS at warn/error | same plus deployment version | same forbidden content |
| incident debug | time-bounded, method-specific | trace ID and approved identifiers | raw sensitive fields without redaction tests |

- [ ] **Step 2: Add committed-secret scanning**

Fail the script when committed configuration has non-empty `secret_id`, `secret_key`, authorization headers, or admin JWT secret values. Permit only unexpanded environment references and never write expanded values into the release tree.

- [ ] **Step 3: Configure CLS as a production overlay**

Keep console logging. Add CLS writers only through production overlays or runtime-generated configuration, at warn/error initially. Never add debuglog. Do not add masking until protobuf annotations and client compatibility tests prove response rewriting is safe.

- [ ] **Step 4: Verify and commit**

```bash
bash scripts/test-trpc-plugin-config.sh
git add docs/运维/tRPC插件运行基线.md modules/admin/config/trpc_go.yaml modules/cloudnode/config/trpc_go.yaml modules/collector/config/trpc_go.yaml modules/factor/config/trpc_go.yaml scripts/test-trpc-plugin-config.sh
git commit -m "docs: define trpc production logging baseline"
```

## Task 7: Gate Advanced Plugins Behind Focused Pilots

**Files:**
- Modify: `docs/运维/tRPC插件运行基线.md`
- Create: `docs/superpowers/specs/2026-07-13-trpc-advanced-plugin-pilots.md`

- [ ] **Step 1: Record admission criteria**

| Plugin | First scope | Required proof | Exclusion |
|---|---|---|---|
| slime | one Storage read RPC | injected timeout proves bounded retry and no duplicate write | trade, timers, event publishing, all writes |
| transinfo-blocker | one Admin-to-Storage route | trace/space pass; JWT, auth, HMAC do not | global blacklist without inventory |
| masking | new non-critical protobuf response field | generated proto/client compatibility tests | API keys, secrets, write fields |
| filterextensions | one costly read method | ordering test | broad service changes |
| OpenTelemetry | Admin-to-Storage trace | collector compatibility, sampling, retention, body suppression | global rollout and Jaeger dual stack |
| degrade/hystrix | none | load test, SLO, approved fallback | treating it as a WAF substitute |

- [ ] **Step 2: Define the OTel spike**

Before importing `trpc-opentelemetry` v1.0.2, prove collector address and transport compatibility, sampling, body suppression, allowed attributes, retention, access control, cost ownership, and one end-to-end trace. Do not infer generic OTLP compatibility from the package name.

- [ ] **Step 3: Commit the boundaries**

```bash
git add docs/运维/tRPC插件运行基线.md docs/superpowers/specs/2026-07-13-trpc-advanced-plugin-pilots.md
git commit -m "docs: scope advanced trpc plugin pilots"
```

## Task 8: Stage, Verify, And Release

**Files:**
- Modify: `README.md`
- Modify: `docs/架构总览.md`
- Modify: both operational runbooks

- [ ] **Step 1: Use the mandatory rollout order**

```text
1. Caddy, origin-contract, and deployment tests pass locally.
2. Staging CNAME points to EdgeOne and edge certificate is ready.
3. External probes prove every upstream and diagnostic port is unreachable.
4. Browser, signed admin API, and signed service API work through EdgeOne.
5. WAF and CC rules observe for 24 hours.
6. Enforce login, then admin, then service limits in separate windows.
7. Roll out recovery/config module by module: admin, storage, trade, collector/factor/cloudnode, then monitor/eventbus/hostagent/strategy/archive.
```

- [ ] **Step 2: Execute external acceptance**

```bash
curl -fsS https://$MOOX_PUBLIC_HOST:9527/ -o /dev/null
curl -fsS http://$MOOX_PUBLIC_HOST:9528/ && exit 1 || true
curl -fsS http://$MOOX_PUBLIC_HOST:11000/healthz && exit 1 || true
```

Use valid signed requests for protected API acceptance. Record authentication result separately from network routing result.

- [ ] **Step 3: Final verification and push**

```bash
bash scripts/test-edgeone-origin-contract.sh
bash scripts/test-caddy-config.sh
bash scripts/test-trpc-plugin-config.sh
go test -count=1 ./web-host ./packages/trpcplugintest
go test -count=1 ./modules/admin/... ./modules/storage/... ./modules/trade/...
git diff --check
git status --short --branch
git push origin main
git status --short --branch
```

Expected: all focused tests pass, advanced plugins remain off pending pilots, and main is aligned with origin.

## Acceptance Criteria

1. EdgeOne fronts browser and service endpoints and CNAME rollback is tested inside the recorded TTL.
2. Direct public access to web-host, Admin, health, metrics, and module RPC ports fails.
3. WAF, adaptive CC, and rate rules create observable security events without blocking valid browser, CLI, or SCF requests.
4. Every production tRPC server has recovery and survives a handler panic.
5. Every Prometheus exporter uses `plugins.metrics.prometheus`, has a matching import, and keeps Pushgateway disabled.
6. MooX JWT/HMAC and business idempotency semantics do not change.
7. No credential, token, raw request body, API secret, or HMAC value is committed or emitted by the logging baseline.
8. Slime, masking, transinfo-blocker, filterextensions, OpenTelemetry, degrade, and hystrix stay disabled until their pilot proof passes.

