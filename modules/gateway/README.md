# MooX Node Gateway

`moox-gateway` is the node-local service gateway. Each server runs one process,
which accepts authenticated `/api/service/{service}/{method}` requests and
forwards them only to loopback services listed by the Admin control plane.

The architecture deliberately separates two planes:

- Admin owns the browser and route control plane: `/api/admin/*` and
  `/api/gateway-control/*`.
- Node Gateway owns the machine service data plane: `/api/service/*`.

Admin stores gateway nodes and node-scoped service deployments. A Gateway pulls
only the full snapshot for its stable `node_id`; it never reads the Admin
database or proxies to another machine. See the canonical
[节点服务网关架构](../../docs/节点服务网关架构.md).

## Runtime

- Service endpoint: `127.0.0.1:11002` (HTTP `/api/service/*`)
- Native tRPC endpoint: `127.0.0.1:11003` by default; control deployments bind
  `0.0.0.0:11003` for signed SCF-to-Storage calls.
- Local diagnostics: `127.0.0.1:11012`
- Control-plane refresh: every 15 seconds through `trpc.moox.gateway.route_refresh.timer`
- Route cache: `<store.path>/routes.json`
- Persistent replay store: `<store.path>/nonces`

Startup loads the last valid route cache before pulling the current snapshot
from Admin. A node without a valid cache must complete its initial pull. Once a
valid snapshot has been applied, later control-plane failures keep the Gateway
ready and leave the last route table active.

The periodic `trpc.moox.gateway.route_refresh.timer` has a 10-second execution timeout, waits for `Refresh` to finish, and skips an overlapping local invocation. It deliberately omits `startAtOnce`: the cache-aware synchronous initialization above is the only startup pull. The 15-second frequency is static deployment configuration and does not hot reload. `DefaultScheduler` provides no cross-process exclusion, so each Gateway process refreshes only its own in-memory route table.

An invalid new snapshot is rejected as a whole. Route changes are made in
Service Management, not by editing `routes.json`; the cache exists only for
recovery when Admin is temporarily unavailable.

The service and control-plane HMAC secrets must be separate owner-only (`0600`)
files. Do not put either secret in YAML or source control. The store directory
and cache are also owner-only.

The HMAC signature binds the method, path, body hash, timestamp, nonce, and
target node. Nonces are persisted so a process restart does not reopen the
replay window. HTTPS control-plane connections must validate the configured CA
bundle.

## Configuration

Start from `config/app.yaml` and `config/trpc_go.yaml`. The node ID must match the Admin gateway-node
record. Admin may use HTTPS, or plaintext HTTP only on a loopback address. The
service and health listeners are deliberately fixed to loopback.

```bash
go run ./cmd/server -config=config/app.yaml -conf=config/trpc_go.yaml
```

Only these local diagnostic routes are served:

- `GET /healthz`: process liveness
- `GET /readyz`: a valid route table has been applied
- `GET /metrics`: Prometheus text metrics, including
  `gateway_route_sync_errors_total`

## Diagnostics CLI

```bash
go run ./cmd/cli check-config --config config/app.yaml
go run ./cmd/cli routes --config config/app.yaml
set -a; source ../../secrets/health-auth.env; set +a
go run ./cmd/cli health --url http://127.0.0.1:11012/readyz
```

The commands return a nonzero exit code for invalid configuration, an unreadable
or hash-invalid cache, and a non-ready health response.

## Operations

For deployment flags, route checks, key replacement, signed requests, and the
two-node Monitor failure drill, see the
[Node Gateway 运维手册](../../docs/ops/node-gateway.md).
