# MooX Node Gateway

`moox-gateway` is the node-local service gateway. Each server runs one process,
which accepts authenticated `/api/service/{service}/{method}` requests and
forwards them only to loopback services listed by the Admin control plane.

## Runtime

- Service endpoint: `127.0.0.1:11002`
- Local diagnostics: `127.0.0.1:11012`
- Control-plane refresh: every 15 seconds by default
- Route cache: `<store.path>/routes.json`
- Persistent replay store: `<store.path>/nonces`

Startup loads the last valid route cache before pulling the current snapshot
from Admin. A node without a valid cache must complete its initial pull. Once a
valid snapshot has been applied, later control-plane failures keep the Gateway
ready and leave the last route table active.

The service and control-plane HMAC secrets must be separate owner-only (`0600`)
files. Do not put either secret in YAML or source control. The store directory
and cache are also owner-only.

## Configuration

Start from `config/app.yaml`. The node ID must match the Admin gateway-node
record. Admin may use HTTPS, or plaintext HTTP only on a loopback address. The
service and health listeners are deliberately fixed to loopback.

```bash
go run ./cmd/server --config config/app.yaml
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
go run ./cmd/cli health --url http://127.0.0.1:11012/readyz
```

The commands return a nonzero exit code for invalid configuration, an unreadable
or hash-invalid cache, and a non-ready health response.
