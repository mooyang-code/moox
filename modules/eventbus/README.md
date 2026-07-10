# MooX EventBus

`moox-eventbus` is the single production owner of the embedded NATS JetStream broker. Services connect directly to the configured NATS listener and publish the protobuf `MooxMessage`; the EventBus management plane is intentionally read-only and has no publish proxy.

## Endpoints

- NATS: `nats://127.0.0.1:4222`
- tRPC/HTTP: `trpc.moox.eventbus.EventBusMgr` on `:11420`
- Liveness: `GET /healthz` on `:11419`
- Readiness: `GET /readyz` on `:11419`
- Metrics: `GET /metrics` on `:11419`

## Configuration

`config/app.yaml` declares the broker, Streams, Topics, and KV buckets. Stream/KV reconciliation is completed before `/readyz` becomes successful. Retention and storage changes are rejected automatically; only bounded limits and descriptions may be changed in place.

The `store_dir` is runtime state and is intentionally not included in release archives. Deployment rewrites it to `../data/eventbus/jetstream`.

## Operations

Start EventBus before Storage and Monitor. The generated deployment waits for
`/readyz`, keeps JetStream data under `data/eventbus/jetstream`, and never
packages that runtime directory. Stream/KV retention is owned by EventBus;
clients only publish messages and bind durable consumers. See
[`docs/运维/MooX-EventBus运维.md`](../../docs/运维/MooX-EventBus运维.md) for
backup/restore, lag diagnosis, capacity, credentials, and clustered operation.

## Build

```bash
./scripts/build.sh eventbus
go test -count=1 ./modules/eventbus/...
```
