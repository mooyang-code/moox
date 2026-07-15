# Node Service Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an independent per-server `moox-gateway`, move `/api/service/*` out of Admin, manage node-local routes from Service Management, and make the two Monitor instances inspect each other through the gateways.

**Architecture:** Admin remains the browser control plane and stores gateway nodes plus node-scoped service deployments. Each Gateway pulls a full route snapshot for its `node_id`, verifies and atomically caches it, then accepts HMAC-authenticated `/api/service/{service}/{method}` requests and forwards only to loopback tRPC HTTP services. The implementation is a clean replacement for an unreleased personal project: no legacy gateway compatibility, route history, staged cutover, or SSH-tunnel fallback.

**Tech Stack:** Go 1.24, tRPC-Go HTTP/no-protocol servers, GORM with SQLite, Badger nonce storage, Vue 3, TypeScript, Arco Design, Vitest, Caddy, Bash deployment scripts.

---

## Delivery Rules

- Work in `/Users/mooyang/.config/superpowers/worktrees/moox/frontend-service-host-workbench` on `feature/frontend-service-host-workbench`.
- Follow TDD inside each task and use `go test -count=1`; do not accept cached test results as proof.
- Regenerate protobuf output with the repository Makefiles; never edit generated `*.pb.go` or `*.trpc.go` manually.
- This is a breaking replacement. Delete old code instead of adding compatibility branches.
- Before the remote schema reset, back up `admin.db`; do not build a schema migration framework.
- Use an independent implementation pass and review pass before remote deployment.

## File Map

New shared packages:

- `packages/gatewayauth`: final `moox-gateway-auth-v1` HMAC request contract and secure HTTP client.
- `packages/gatewayproxy`: route snapshot types, validation, deterministic hash, atomic route table, forwarding header rules.

New module:

- `modules/gateway/cmd/server`: process entry point.
- `modules/gateway/cmd/cli`: `check-config`, `routes`, and `health` diagnostics.
- `modules/gateway/internal/config`: YAML and environment configuration.
- `modules/gateway/internal/controlplane`: route pull and status reporting.
- `modules/gateway/internal/router`: `/api/service/*` and local health muxes.
- `modules/gateway/internal/store`: atomic `routes.json` and persistent nonce store.
- `modules/gateway/internal/health`: readiness state and metrics.
- `modules/gateway/internal/bootstrap`: lifecycle wiring.

Admin changes:

- `modules/admin/schema/admin.sql`: final gateway node and node-scoped deployment schema.
- `modules/admin/proto/sysdeploy_service.proto`: gateway node management and node-aware deployment API.
- `modules/admin/internal/service/sysdeploy/*`: DAO, route compiler, status updates, validation, defaults.
- `modules/admin/internal/gateway/*`: keep `/api/admin/*`, add `/api/gateway-control/*`, remove `/api/service/*`.
- `modules/admin/config/trpc_go.yaml`: remove Admin `11002` listener.

Monitor changes:

- `modules/monitor/proto/monitor.proto`: add `GetPeerSnapshot`.
- `modules/monitor/internal/rpc/*`: build snapshot through the formal RPC.
- `modules/monitor/internal/peer/*`: call peer Gateway using the shared authenticated client.
- `modules/monitor/internal/bootstrap/bootstrap.go`: remove the internal snapshot HTTP handler.

Frontend and delivery changes:

- `web/src/views/ops/service-management/index.vue`: add “网关节点” and “服务实例” tabs.
- `web/src/views/ops/service-management/gateway-nodes.vue`: node table and editor.
- `web/src/views/settings/service-deployments/index.vue`: node-aware service table and editor.
- `web/src/api/admin/{types,sysdeploy}.ts`: new API types and methods.
- `scripts/{build,release,deploy-moox}.sh`, `deploy/caddy/Caddyfile`: build, package, start, verify, and route the new service.

---

### Task 1: Final Gateway Authentication Package

**Files:**
- Create: `packages/gatewayauth/go.mod`
- Create: `packages/gatewayauth/auth.go`
- Create: `packages/gatewayauth/client.go`
- Create: `packages/gatewayauth/auth_test.go`
- Create: `packages/gatewayauth/client_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write failing contract tests**

Define tests for a valid request, wrong target node, expired timestamp, changed body, changed path, and a non-loopback plaintext URL. The public contract is:

```go
const Version = "moox-gateway-auth-v1"

type Credentials struct {
    KeyID, Secret string
    Expire, ClockSkew time.Duration
}

type Request struct {
    Method, Path, TargetNode string
    Body []byte
}

type Claims struct {
    KeyID, Nonce, TargetNode string
    Timestamp int64
    TTL time.Duration
}

func Sign(c Credentials, req Request, now time.Time) (http.Header, error)
func Verify(c Credentials, req Request, header http.Header, now time.Time) (Claims, error)
type ClientOptions struct { Timeout time.Duration; CAFile string }
func NewHTTPClient(ClientOptions) (*http.Client, error)
```

- [ ] **Step 2: Run tests and confirm the package is missing**

Run: `go test -count=1 ./packages/gatewayauth/...`

Expected: FAIL because `packages/gatewayauth` has not been implemented.

- [ ] **Step 3: Implement the canonical HMAC material and secure client**

Use `packages/requestauth` for nonce generation and HMAC. Sign exactly these newline-separated values:

```text
moox-gateway-auth-v1
<METHOD>
<ESCAPED_PATH>
<SHA256_BODY_HEX>
<UNIX_TIMESTAMP>
<NONCE>
<TARGET_NODE>
```

Write `X-Moox-Key-Id`, `X-Moox-Timestamp`, `X-Moox-Nonce`, `X-Moox-Target-Node`, and `X-Moox-Signature`. `NewHTTPClient` must allow HTTPS everywhere, add certificates from `CAFile` to the system pool, and allow HTTP only for loopback hosts.

- [ ] **Step 4: Add the module to the workspace and verify**

Run: `go work use ./packages/gatewayauth && go test -count=1 ./packages/gatewayauth/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.work packages/gatewayauth
git commit -m "feat(gateway): add node-targeted request authentication"
```

### Task 2: Route Snapshot and Proxy Package

**Files:**
- Create: `packages/gatewayproxy/go.mod`
- Create: `packages/gatewayproxy/route.go`
- Create: `packages/gatewayproxy/table.go`
- Create: `packages/gatewayproxy/forward.go`
- Create: `packages/gatewayproxy/route_test.go`
- Create: `packages/gatewayproxy/table_test.go`
- Create: `packages/gatewayproxy/forward_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write failing route validation and hashing tests**

Use these final types:

```go
type Route struct {
    ServiceID string `json:"service_id,omitempty"`
    Address string `json:"address,omitempty"`
    ServicePath string `json:"service_path,omitempty"`
    TimeoutMS int64 `json:"timeout_ms,omitempty"`
    MaxBodyBytes int64 `json:"max_body_bytes,omitempty"`
}

type Snapshot struct {
    NodeID, RouteHash string `json:"node_id,omitempty"`
    GeneratedAt time.Time `json:"generated_at,omitempty"`
    Disabled bool `json:"disabled,omitempty"`
    Routes []Route `json:"routes,omitempty"`
}

func ValidateRoute(Route) error
func NormalizeAndHash(nodeID string, routes []Route) (Snapshot, error)
```

Assert that only `127.0.0.1` and `::1` are accepted, service IDs are lowercase URL segments, service paths have no leading slash, routes sort by service ID, and repeated input produces the same SHA-256 hash.

- [ ] **Step 2: Write failing atomic table and forwarding tests**

Test `Table.Replace`, `Table.Resolve`, whole-snapshot rejection, 4 MiB body limit, header allowlist, gzip, trace propagation, and tRPC error header preservation.

- [ ] **Step 3: Implement the minimal package**

Expose:

```go
type Table struct { current atomic.Pointer[Snapshot] }
func (t *Table) Replace(Snapshot) error
func (t *Table) Resolve(serviceID string) (Route, bool)
func Forward(ctx context.Context, client *http.Client, route Route, method string, body []byte, headers http.Header) (*Response, error)
```

`Forward` must POST to `http://<route.Address>/<route.ServicePath>/<method>` and forward only `Content-Type`, `Accept-Encoding`, `X-Trace-Id`, and `X-Space-Id`.

- [ ] **Step 4: Verify both shared packages**

Run: `go work use ./packages/gatewayproxy && go test -count=1 ./packages/gatewayauth/... ./packages/gatewayproxy/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.work packages/gatewayproxy
git commit -m "feat(gateway): add route snapshot and loopback proxy core"
```

### Task 3: Final Admin Schema and Node-Aware SysDeploy Contract

**Files:**
- Modify: `modules/admin/schema/admin.sql`
- Modify: `modules/admin/schema/schema_test.go`
- Modify: `modules/admin/cmd/cli/init_schema.go`
- Modify: `modules/admin/cmd/cli/init_schema_test.go`
- Modify: `modules/admin/proto/sysdeploy_service.proto`
- Regenerate: `modules/admin/proto/admingen/sysdeploy_service.pb.go`
- Regenerate: `modules/admin/proto/admingen/sysdeploy_service.trpc.go`

- [ ] **Step 1: Write schema tests for final tables and constraints**

Require `t_gateway_nodes`, nullable foreign key `c_host_id`, unique `c_node_id`, deployment columns `c_node_id`, `c_gateway_service_id`, `c_gateway_enabled`, unique `(c_node_id,c_service_name)`, and unique enabled `(c_node_id,c_gateway_service_id)`. A node may be bootstrapped before SSH hosts are restored, but final UI validation must require an associated host before the node is considered fully configured.

- [ ] **Step 2: Remove legacy table rebuilding and write the final schema**

Delete `migrateLegacyServiceDeployments`. Define the node table and deployment columns directly in `admin.sql`. `init` applies the final schema to a fresh database; deployment will back up and reset the unreleased database.

- [ ] **Step 3: Extend the protobuf contract**

Add fields to `ServiceDeployment` and key all get/update/delete requests by `node_id + service_name`. Add:

```protobuf
message GatewayNode {
  string node_id = 1;
  int64 host_id = 2;
  string name = 3;
  string public_address = 4;
  string status = 5;
  string route_hash = 6;
  string applied_route_hash = 7;
  int32 route_count = 8;
  string last_seen_at = 9;
  string last_error = 10;
  string created_at = 11;
  string updated_at = 12;
}

rpc ListGatewayNodes(ListGatewayNodesReq) returns (ListGatewayNodesRsp);
rpc CreateGatewayNode(CreateGatewayNodeReq) returns (CreateGatewayNodeRsp);
rpc UpdateGatewayNode(UpdateGatewayNodeReq) returns (UpdateGatewayNodeRsp);
rpc DeleteGatewayNode(DeleteGatewayNodeReq) returns (DeleteGatewayNodeRsp);
rpc GetGatewayNodeRoutes(GetGatewayNodeRoutesReq) returns (GetGatewayNodeRoutesRsp);
```

- [ ] **Step 4: Regenerate and test**

Run: `make -C modules/admin/proto && go test -count=1 ./modules/admin/schema ./modules/admin/cmd/cli`

Expected: PASS; generated interfaces contain the five gateway-node RPC methods.

- [ ] **Step 5: Commit**

```bash
git add modules/admin/schema modules/admin/cmd/cli modules/admin/proto
git commit -m "feat(admin): define gateway nodes and node-scoped deployments"
```

### Task 4: SysDeploy DAO, Route Compiler, and Management RPCs

**Files:**
- Create: `modules/admin/internal/service/sysdeploy/gateway_node.go`
- Create: `modules/admin/internal/service/sysdeploy/routes.go`
- Create: `modules/admin/internal/service/sysdeploy/routes_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/model.go`
- Modify: `modules/admin/internal/service/sysdeploy/dao.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Modify: `modules/admin/internal/service/sysdeploy/service_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/rpc/service.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/go.mod`

- [ ] **Step 1: Write failing DAO and route compiler tests**

Cover two `moox_monitor` rows on different nodes, duplicate service ID rejection within one node, same service ID allowed across nodes, non-loopback gateway-enabled rejection, disabled service exclusion, deterministic route hash, node disabled response, and status heartbeat update.

- [ ] **Step 2: Add final models and composite keys**

```go
type GatewayNode struct {
    NodeID string `gorm:"column:c_node_id;primaryKey"`
    HostID *int64 `gorm:"column:c_host_id"`
    Name, PublicAddress, Status string
    RouteHash, AppliedRouteHash string
    RouteCount int
    LastSeenAt *time.Time
    LastError string
}

type Deployment struct {
    // existing fields
    NodeID string `gorm:"column:c_node_id;not null;uniqueIndex:idx_service_deployments_node_name"`
    GatewayServiceID string `gorm:"column:c_gateway_service_id;not null;default:''"`
    GatewayEnabled bool `gorm:"column:c_gateway_enabled;not null;default:false"`
}
```

Update DAO signatures to `Get(ctx,nodeID,serviceName)`, `Update(ctx,nodeID,serviceName,item)`, and `Delete(ctx,nodeID,serviceName)`.

- [ ] **Step 3: Implement `CompileGatewaySnapshot` and status update**

`CompileGatewaySnapshot(ctx,nodeID)` loads the enabled node, selects active gateway-enabled deployments, maps them to `gatewayproxy.Route`, validates the entire list, computes `route_hash`, and persists that hash on the node record. `ReportGatewayStatus` updates applied hash, heartbeat, and error.

- [ ] **Step 4: Implement management RPC adapters and defaults**

Add the node CRUD methods and node-aware deployment methods to `Service` and `rpc.Service`. Ensure the local control-plane node from `MOOX_ADMIN_NODE_ID` during Admin bootstrap; attach its SSH host automatically when the public address matches, otherwise leave `host_id` null for later UI association. Seed local service deployments with that node ID and explicit gateway service IDs; remove `service_gateway` and `service_gateway_internal` deployment rows.

- [ ] **Step 5: Verify Admin SysDeploy**

Run: `go test -count=1 ./modules/admin/internal/service/sysdeploy/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/admin/internal/service/sysdeploy modules/admin/go.mod modules/admin/go.sum
git commit -m "feat(admin): compile node-local gateway routes"
```

### Task 5: Gateway Control Endpoints in Admin

**Files:**
- Create: `modules/admin/internal/gateway/gateway_control.go`
- Create: `modules/admin/internal/gateway/gateway_control_test.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/request_auth.go`
- Modify: `modules/admin/internal/bootstrap/services.go`
- Modify: `modules/admin/internal/bootstrap/trpc.go`
- Modify: `modules/admin/config/gateway.yaml`

- [ ] **Step 1: Write failing HTTP handler tests**

Test authenticated `GET /api/gateway-control/routes?node_id=gateway-gz-122`, authenticated `POST /api/gateway-control/status`, missing signature 401, replayed nonce 401, query/header node mismatch 401, and unknown node 404.

- [ ] **Step 2: Define a narrow provider interface**

```go
type GatewayControlProvider interface {
    CompileGatewaySnapshot(context.Context, string) (gatewayproxy.Snapshot, error)
    ReportGatewayStatus(context.Context, GatewayStatusReport) error
}

type GatewayStatusReport struct {
    NodeID, AppliedRouteHash, LastError string
    RouteCount int
}
```

Pass this provider into Gateway initialization from `bootstrap.RegisterTRPCServices`; do not add a package global.

- [ ] **Step 3: Implement control authentication and handlers**

Use `gatewayauth.Verify` with `MOOX_GATEWAY_CONTROL_KEY_ID` and `MOOX_GATEWAY_CONTROL_SECRET_KEY`. Consume control nonces through Admin's existing persistent request-auth store. JSON responses use HTTP status codes because these are Gateway control endpoints, not browser tRPC responses.

- [ ] **Step 4: Register the control path on Admin `11000`**

The Admin router must contain:

```go
router.HandleFunc("/api/gateway-control/routes", control.handleRoutes).Methods(http.MethodGet)
router.HandleFunc("/api/gateway-control/status", control.handleStatus).Methods(http.MethodPost)
router.HandleFunc("/api/admin/{service}/{method}", hr.handleControlRequest)
```

- [ ] **Step 5: Verify**

Run: `go test -count=1 ./modules/admin/internal/gateway ./modules/admin/internal/bootstrap`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/admin/internal/gateway modules/admin/internal/bootstrap modules/admin/config/gateway.yaml
git commit -m "feat(admin): expose authenticated gateway control endpoints"
```

### Task 6: Independent Gateway Module Core

**Files:**
- Create: `modules/gateway/go.mod`
- Create: `modules/gateway/config/app.yaml`
- Create: `modules/gateway/config/trpc_go.yaml`
- Create: `modules/gateway/internal/config/config.go`
- Create: `modules/gateway/internal/config/config_test.go`
- Create: `modules/gateway/internal/store/routes.go`
- Create: `modules/gateway/internal/store/nonce.go`
- Create: `modules/gateway/internal/store/store_test.go`
- Create: `modules/gateway/internal/controlplane/client.go`
- Create: `modules/gateway/internal/controlplane/client_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write failing configuration and store tests**

Require node ID, Admin base URL, control key file, service key file, service address `127.0.0.1:11002`, health address `127.0.0.1:11012`, 15-second refresh default, atomic `routes.json`, and persistent duplicate nonce rejection.

- [ ] **Step 2: Implement configuration loading**

```go
type Config struct {
    Node struct{ ID string `yaml:"id"` } `yaml:"node"`
    Server struct{ ServiceAddr, HealthAddr string } `yaml:"server"`
    ControlPlane struct{ BaseURL string; RefreshInterval time.Duration; HMACKeyFile, CAFile string } `yaml:"control_plane"`
    Auth struct{ HMACKeyFile string } `yaml:"auth"`
    Store struct{ Path string } `yaml:"store"`
    Proxy struct{ MaxBodyBytes int64 } `yaml:"proxy"`
}
```

Reject wildcard listen addresses and key files whose permission contains group/world bits.

- [ ] **Step 3: Implement route and nonce persistence**

Write routes through a same-directory temporary file, `Sync`, rename, and directory `Sync`. Store nonces in Badger with TTL and a transaction that returns `false` when the key already exists.

- [ ] **Step 4: Implement the control-plane client**

`Pull(ctx,currentHash)` signs a GET for the configured node. `Report(ctx,appliedHash,routeCount,lastError)` signs the JSON POST. A pull failure returns an error without modifying disk or memory.

- [ ] **Step 5: Verify module foundations**

Run: `go work use ./modules/gateway && go test -count=1 ./modules/gateway/internal/config ./modules/gateway/internal/store ./modules/gateway/internal/controlplane`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.work modules/gateway
git commit -m "feat(gateway): add configuration control client and state store"
```

### Task 7: Gateway HTTP Server, Health, Metrics, and CLI

**Files:**
- Create: `modules/gateway/internal/router/router.go`
- Create: `modules/gateway/internal/router/router_test.go`
- Create: `modules/gateway/internal/health/state.go`
- Create: `modules/gateway/internal/bootstrap/bootstrap.go`
- Create: `modules/gateway/internal/bootstrap/bootstrap_test.go`
- Create: `modules/gateway/cmd/server/main.go`
- Create: `modules/gateway/cmd/cli/main.go`
- Create: `modules/gateway/README.md`

- [ ] **Step 1: Write failing service-router tests**

Cover valid proxying, method/path rejection, HMAC verification, wrong target node, duplicate nonce, body too large 413, missing route 404, disabled node 503, unavailable upstream 502, and trace/tRPC header preservation.

- [ ] **Step 2: Implement the service router**

Register only:

```go
service.HandleFunc("/api/service/{service}/{method}", h.HandleService)
health.Handle("/healthz", liveness)
health.Handle("/readyz", readiness)
health.Handle("/metrics", metrics)
```

Read the bounded body before authentication, verify with `gatewayauth`, atomically consume the nonce, resolve the route, and call `gatewayproxy.Forward`.

- [ ] **Step 3: Implement lifecycle and stale-control behavior**

Bootstrap loads cached routes first, performs an initial pull, starts the HTTP services, and then refreshes every 15 seconds. No cache plus failed initial pull is fatal. A later pull failure retains readiness and increments `gateway_route_sync_errors_total`.

- [ ] **Step 4: Implement diagnostics CLI**

Commands:

```text
moox-gateway-cli check-config --config config/app.yaml
moox-gateway-cli routes --config config/app.yaml
moox-gateway-cli health --url http://127.0.0.1:11012/readyz
```

Exit nonzero for invalid configuration, unreadable cache, hash mismatch, or non-ready health.

- [ ] **Step 5: Verify the full module**

Run: `go test -count=1 ./modules/gateway/... && (cd modules/gateway && go build ./cmd/server ./cmd/cli)`

Expected: tests and both direct builds PASS. Repository build targets are added in Task 12.

- [ ] **Step 6: Commit**

```bash
git add modules/gateway
git commit -m "feat(gateway): serve authenticated node-local routes"
```

### Task 8: Remove the Admin Machine Gateway

**Files:**
- Modify: `modules/admin/config/trpc_go.yaml`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/forward.go`
- Modify: `modules/admin/internal/gateway/resolver.go`
- Delete: `modules/admin/internal/gateway/service_auth.go`
- Delete: `modules/admin/internal/gateway/service_auth_test.go`
- Modify: `modules/admin/internal/gateway/gateway_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Modify: `modules/admin/internal/bootstrap/services.go`
- Modify: `modules/admin/config/gateway.yaml`

- [ ] **Step 1: Change tests to require an Admin-only router**

Assert `/api/admin/auth/GetLoginSalt` remains registered, `/api/gateway-control/routes` remains registered, and `/api/service/monitor/GetPeerSnapshot` returns 404 on Admin `11000`.

- [ ] **Step 2: Remove the `trpc.moox.gateway.service` listener and router**

Delete port `11002` from Admin `trpc_go.yaml`, `buildServiceRouter`, `handleServiceRequest`, all service-auth configuration, and Admin service nonce consumption.

- [ ] **Step 3: Keep browser control routing node-local**

Replace the old global resolver with `ResolveAdminServiceDetail(ctx, adminNodeID, serviceID)`. Read `MOOX_ADMIN_NODE_ID`, require a matching active deployment, and keep browser/raw SSH behavior unchanged.

- [ ] **Step 4: Verify Admin has no machine gateway surface**

Run: `go test -count=1 ./modules/admin/... && ! rg -n "trpc\.moox\.gateway\.service|buildServiceRouter|legacy_service" modules/admin`

Expected: tests PASS and `rg` finds nothing.

- [ ] **Step 5: Commit**

```bash
git add -A modules/admin
git commit -m "refactor(admin): remove embedded machine gateway"
```

### Task 9: Update All Machine Callers to the Final Auth Contract

**Files:**
- Modify: `modules/collector/internal/app/runtime/auth.go`
- Modify: `modules/collector/internal/bootstrap/discovery.go`
- Modify: `modules/collector/internal/httpclient/fetch.go`
- Modify: `modules/collector/internal/reporter/heartbeat.go`
- Modify: `modules/collector/internal/reporter/task_status.go`
- Modify: `modules/collector/internal/taskpublisher/client.go`
- Modify: `modules/cli/internal/adminclient/client.go`
- Modify: `modules/cli/internal/adminclient/service_auth.go`
- Modify: `modules/trade/internal/secretclient/client.go`
- Modify: `packages/cloudruntime/auth.go`
- Modify: `packages/cloudruntime/runtime.go`
- Modify: affected `go.mod`, `go.sum`, and tests
- Delete: `packages/servicegateway/`
- Modify: `go.work`

- [ ] **Step 1: Update caller tests first**

Every request builder must accept `targetNode` and assert `X-Moox-Target-Node` is signed. Configuration uses `MOOX_GATEWAY_NODE_ID`, `MOOX_GATEWAY_SERVICE_KEY_ID`, and `MOOX_GATEWAY_SERVICE_SECRET_KEY`.

- [ ] **Step 2: Replace `servicegateway` imports**

Use:

```go
headers, err := gatewayauth.Sign(creds, gatewayauth.Request{
    Method: http.MethodPost,
    Path: req.URL.EscapedPath(),
    Body: body,
    TargetNode: cfg.TargetNode,
}, time.Now())
```

Copy the returned headers to the outbound request. Use `gatewayauth.NewHTTPClient` with the configured peer CA bundle for HTTPS/loopback enforcement.

- [ ] **Step 3: Remove the obsolete module**

Delete `packages/servicegateway`, its `go.work` entry, and all require/replace directives. No adapter package remains.

- [ ] **Step 4: Verify every affected module**

Run:

```bash
go test -count=1 ./packages/gatewayauth/... ./packages/cloudruntime/...
go test -count=1 ./modules/collector/... ./modules/cli/... ./modules/trade/...
! rg -n "packages/servicegateway|moox-auth-v2|X-Moox-Service-Auth" --glob '*.go' --glob 'go.mod'
```

Expected: all tests PASS and the old contract is absent.

- [ ] **Step 5: Commit**

```bash
git add -A go.work packages modules/collector modules/cli modules/trade
git commit -m "refactor(gateway): switch machine callers to node auth"
```

### Task 10: Move Monitor Peer Synchronization to Gateway RPC

**Files:**
- Modify: `modules/monitor/proto/monitor.proto`
- Regenerate: `modules/monitor/proto/monitorgen/monitor.pb.go`
- Regenerate: `modules/monitor/proto/monitorgen/monitor.trpc.go`
- Modify: `modules/monitor/internal/rpc/service.go`
- Modify: `modules/monitor/internal/rpc/service_test.go`
- Modify: `modules/monitor/internal/peer/puller.go`
- Modify: `modules/monitor/internal/peer/http_test.go`
- Delete: `modules/monitor/internal/peer/http.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap.go`
- Modify: `modules/monitor/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/monitor/internal/config/config.go`
- Modify: `modules/monitor/config/app.yaml`
- Modify: `modules/monitor/go.mod`

- [ ] **Step 1: Add failing RPC and peer-client tests**

Add protobuf messages for instance ID, base URL, observed time, check statuses, and alert events. Test that `GetPeerSnapshot` returns repository data and that `Puller` posts through `/api/service/monitor/GetPeerSnapshot` with target-node HMAC.

- [ ] **Step 2: Add and regenerate the RPC**

```protobuf
message GetPeerSnapshotReq {}
message PeerCheckSnapshot { string check_id = 1; string status = 2; }
message PeerAlertEventSnapshot { string event_id = 1; string event_type = 2; string created_at = 3; }
message GetPeerSnapshotRsp {
  common.RetInfo ret_info = 1;
  string instance_id = 2;
  string base_url = 3;
  string observed_at = 4;
  repeated PeerCheckSnapshot checks = 5;
  repeated PeerAlertEventSnapshot alert_events = 6;
}
rpc GetPeerSnapshot(GetPeerSnapshotReq) returns (GetPeerSnapshotRsp);
```

Run: `make -C modules/monitor/proto`.

- [ ] **Step 3: Implement server and Gateway client**

Move `monitorSnapshot` logic into `rpc.Service.GetPeerSnapshot`. Change `peer.Remote` to `{InstanceID, GatewayURL, NodeID}` and sign the JSON POST with `gatewayauth`.

- [ ] **Step 4: Delete the old peer token surface**

Remove `/internal/monitor/v1/snapshot`, `X-MooX-Monitor-Peer-Token`, `peer.token`, per-peer tokens, and health-server snapshot registration. Health keeps only `/healthz`, `/readyz`, and `/metrics`.

- [ ] **Step 5: Verify Monitor**

Run: `go test -count=1 ./modules/monitor/... && ! rg -n "internal/monitor/v1/snapshot|Monitor-Peer-Token|peer\.token" modules/monitor`

Expected: PASS and old peer surface absent.

- [ ] **Step 6: Commit**

```bash
git add -A modules/monitor
git commit -m "feat(monitor): synchronize peers through node gateways"
```

### Task 11: Service Management UI for Gateway Nodes and Instances

**Files:**
- Modify: `web/src/api/admin/types.ts`
- Modify: `web/src/api/admin/sysdeploy.ts`
- Create: `web/src/views/ops/service-management/gateway-nodes.vue`
- Create: `web/src/views/ops/service-management/gateway-nodes.test.ts`
- Modify: `web/src/views/ops/service-management/index.vue`
- Modify: `web/src/views/settings/service-deployments/index.vue`
- Modify: `web/src/views/settings/service-deployments/health.ts`
- Modify: `web/scripts/check-service-management-contract.mjs`

- [ ] **Step 1: Write failing API and component tests**

Require four tabs in order: `网关节点`, `服务实例`, `可用性监控`, `应用指标`. Test node status labels, hash mismatch warning, node filter propagation, loopback validation, `gateway_enabled` toggle, and composite row key `${node_id}:${service_name}`.

- [ ] **Step 2: Add TypeScript contracts and API methods**

Add `GatewayNode`, node-aware `ServiceDeployment`, node CRUD methods, and `getGatewayNodeRoutes`. Update deployment update/delete methods to send both node ID and service name.

- [ ] **Step 3: Build the Gateway Nodes view**

Use the existing `moox-page`/`moox-inner` spacing and compact Arco table style. Columns: node, host, public address, online state, last seen, route count, expected/applied hash, error, actions. Use icon buttons with tooltips for refresh/view-routes/edit; do not add nested cards or imply that Admin can push a sync to a node.

- [ ] **Step 4: Convert Service Deployments into the Service Instances view**

Add node selector, Gateway service ID, and Gateway enabled switch. When enabled, only accept `127.0.0.1` or `::1` and a nonempty tRPC path. Remove horizontal scrolling where the current viewport can fit by hiding lower-priority description/extra JSON columns in the table and keeping them in the editor.

- [ ] **Step 5: Verify frontend behavior and style**

Run:

```bash
cd web
CI=true pnpm test -- gateway-nodes service-deployments
pnpm check:service-management
pnpm build:prod
```

Expected: tests, contract check, and production build PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/api/admin web/src/views/ops/service-management web/src/views/settings/service-deployments web/scripts/check-service-management-contract.mjs
git commit -m "feat(web): manage gateway nodes and service instances"
```

### Task 12: Build, Package, Caddy, and Deployment Integration

**Files:**
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `deploy/caddy/Caddyfile`
- Modify: `scripts/test-caddy-config.sh`
- Create: `scripts/test-deploy-moox-gateway.sh`
- Modify: `Makefile`
- Modify: `README.md`

- [ ] **Step 1: Write failing shell contract tests**

Require a `gateway` build target, packaged `gateway/config`, start/stop/status support, loopback listeners `11002/11012`, secret files with mode `0600`, `/api/service/* -> 127.0.0.1:11002`, and central-only `/api/gateway-control/* -> 127.0.0.1:11000`.

- [ ] **Step 2: Add build and release entries**

`scripts/build.sh gateway` builds both gateway binaries; `all` includes them. `release.sh` copies Gateway config and binaries into the archive. Add deploy options `--node-id`, `--gateway-control-url`, `--gateway-ca-bundle`, `--gateway-control-key-file`, `--gateway-service-key-file`, and `--no-admin`; the latter packages a data-plane node without Admin, browser assets, Admin schema initialization, or Admin credentials.

- [ ] **Step 3: Update Caddy**

On the browser site add:

```caddyfile
handle /api/gateway-control/* {
    reverse_proxy 127.0.0.1:11000
}
```

The service HTTPS site continues to proxy `/api/service/*` to `127.0.0.1:11002`, now owned only by `moox-gateway`.

- [ ] **Step 4: Integrate deployment lifecycle**

Package Gateway by default, install the supplied cluster control/service keys into separate `gateway-control.env` and `gateway-service.env`, start Gateway after Admin on the control-plane node and before Monitor, add Gateway health to status/healthcheck, and include Gateway in stop/restart. Both nodes must receive the same service key; Admin and both Gateways must receive the same control key. Set `MOOX_ADMIN_NODE_ID` only where Admin runs and `MOOX_GATEWAY_NODE_ID` on every target. Install the public CA bundle at `certs/gateway/peers.pem` with both nodes' Caddy roots; never copy either Caddy CA private key. In `--no-admin` mode, Caddy must omit the browser site and `/api/gateway-control/*` route while retaining the service HTTPS site.

- [ ] **Step 5: Verify scripts**

Run:

```bash
bash scripts/test-deploy-moox-gateway.sh
bash scripts/test-caddy-config.sh
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh gateway
make verify
```

Expected: all checks PASS and Linux Gateway binaries exist.

- [ ] **Step 6: Commit**

```bash
git add scripts deploy/caddy Makefile README.md
git commit -m "build(gateway): package and supervise node gateways"
```

### Task 13: Integrated E2E, Review, and Two-Server Acceptance

**Files:**
- Create: `modules/gateway/test/e2e_test.go`
- Modify: `modules/admin/test/e2e_test.go`
- Modify: `modules/monitor/test/e2e_test.go`
- Create: `docs/ops/node-gateway.md`

- [ ] **Step 1: Add local integrated E2E coverage**

Start a fake Admin control endpoint, Gateway, and fake loopback tRPC upstream. Prove route pull, authenticated forwarding, wrong-node rejection, atomic route replacement, cached restart, disabled node behavior, and Admin outage behavior.

- [ ] **Step 2: Add Monitor two-node E2E coverage**

Run two Gateway/Monitor pairs on separate loopback ports. Prove each Monitor stores the other snapshot; stop one Monitor and assert the surviving instance marks the peer stale and emits the expected alert transition.

- [ ] **Step 3: Run the full local verification suite**

```bash
go test -count=1 ./packages/gatewayauth/... ./packages/gatewayproxy/...
go test -count=1 ./modules/gateway/... ./modules/admin/... ./modules/monitor/...
cd web && CI=true pnpm test && pnpm build:prod
cd .. && make verify
```

Expected: all commands PASS.

- [ ] **Step 4: Request an independent code review and fix findings**

Review specifically for remote proxy bypass, HMAC material mismatches, nonce races, route-cache corruption, Admin `/api/admin/*` regression, secret logging, deployment ordering, and frontend overflow. Re-run Step 3 after every fix and commit review fixes separately.

- [ ] **Step 5: Back up and reset the unreleased Admin database**

```bash
ssh ubuntu@106.53.107.122 'cp /home/ubuntu/moox/prod/data/admin.db /home/ubuntu/moox/prod/data/admin.db.pre-gateway-$(date +%Y%m%d%H%M%S)'
ssh ubuntu@106.53.107.122 'sqlite3 /home/ubuntu/moox/prod/data/admin.db ".mode insert t_ssh_host" "select * from t_ssh_host;"' > /tmp/moox-ssh-hosts.sql
scp ubuntu@106.53.107.122:/home/ubuntu/moox/prod/certs/caddy/root.crt /tmp/moox-gateway-gz-root.crt
scp ubuntu@43.132.204.177:/home/ubuntu/moox/prod/certs/caddy/root.crt /tmp/moox-gateway-hk-root.crt
cat /tmp/moox-gateway-gz-root.crt /tmp/moox-gateway-hk-root.crt > /tmp/moox-gateway-peers.pem
read -r -s -p 'Initial Admin password: ' MOOX_ADMIN_PASSWORD; printf '\n'
printf '%s\n' "${MOOX_ADMIN_PASSWORD}" > /tmp/moox-admin-password
chmod 0600 /tmp/moox-admin-password
openssl rand -hex 32 > /tmp/moox-gateway-control.key
openssl rand -hex 32 > /tmp/moox-gateway-service.key
chmod 0600 /tmp/moox-gateway-control.key /tmp/moox-gateway-service.key
```

Verify the CA bundle contains two certificates with `grep -c 'BEGIN CERTIFICATE' /tmp/moox-gateway-peers.pem`. Expected: `2`. Then deploy with the repository's explicit reset-data option. Do not run an old-schema migration.

- [ ] **Step 6: Deploy the Guangzhou node**

```bash
./scripts/deploy-moox.sh --target ubuntu@106.53.107.122 --dir /home/ubuntu/moox/prod --public-host 106.53.107.122 --service-https-port 443 --node-id gateway-gz-122 --gateway-control-url https://106.53.107.122:9527 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key --admin-password-file /tmp/moox-admin-password --reset-data
ssh ubuntu@106.53.107.122 'sqlite3 /home/ubuntu/moox/prod/data/admin.db' < /tmp/moox-ssh-hosts.sql
```

Associate the bootstrapped `gateway-gz-122` with `腾讯云-122` and verify its local service instances. Before deploying Hong Kong, create `gateway-hk-177`, associate it with `腾讯云-香港`, and add its `moox_monitor` route. Verify signed health and confirm only Caddy owns public ports.

- [ ] **Step 7: Deploy the Hong Kong node**

```bash
./scripts/deploy-moox.sh --target ubuntu@43.132.204.177 --dir /home/ubuntu/moox/prod --public-host 43.132.204.177 --service-https-port 443 --node-id gateway-hk-177 --gateway-control-url https://106.53.107.122:9527 --gateway-ca-bundle /tmp/moox-gateway-peers.pem --gateway-control-key-file /tmp/moox-gateway-control.key --gateway-service-key-file /tmp/moox-gateway-service.key --no-admin --no-web-host --no-storage --no-archive --no-eventbus --no-cloudnode --no-collector --no-factor
```

Create `gateway-hk-177`, associate it with `腾讯云-香港`, and register its local Monitor and Host Agent services.

- [ ] **Step 8: Verify live Gateway and Monitor behavior**

Use signed requests to verify both `/api/service/monitor/GetPeerSnapshot` endpoints, confirm the Admin UI shows matching route hashes and fresh heartbeats, stop Monitor on each server in turn, and prove the other server marks it down and generates an alert. Confirm ports `11409`, `11410`, and other business ports are not publicly reachable.

- [ ] **Step 9: Remove the obsolete SSH tunnel**

Disable and delete `moox-monitor-tunnel.service`, its scripts, environment files, and SSH reverse-forward configuration on both servers. Repeat Step 8 after removal.

- [ ] **Step 10: Commit operational documentation and push**

```bash
git add modules/gateway/test modules/admin/test modules/monitor/test docs/ops/node-gateway.md
git commit -m "test(gateway): verify two-node gateway operation"
git push origin feature/frontend-service-host-workbench
```

Document exact health commands, node/service registration, route inspection, secret replacement, restart order, and peer-failure drill results in `docs/ops/node-gateway.md`.

---

## Completion Checklist

- [ ] `modules/gateway` is independently buildable, testable, packaged, and supervised.
- [ ] Admin has no `/api/service/*` listener or machine-auth compatibility code.
- [ ] Gateway upstreams are restricted to loopback.
- [ ] Admin manages two gateway nodes and node-scoped service instances.
- [ ] Route updates become visible within 15 seconds without a publish action.
- [ ] Gateway continues using cached routes during an Admin outage.
- [ ] All machine callers sign the final node-targeted HMAC contract.
- [ ] Monitor peer synchronization uses `GetPeerSnapshot` through Gateway.
- [ ] The old peer token handler and SSH reverse tunnel are absent.
- [ ] Service Management displays live node heartbeat and matching route hashes.
- [ ] Full Go, frontend, script, E2E, and live two-server checks pass.
