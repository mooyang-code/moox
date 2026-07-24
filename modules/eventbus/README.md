# MooX EventBus

`moox-eventbus` is the single production owner of the embedded NATS JetStream broker. Services connect directly to the configured NATS listener and publish governed protobuf `EventMessage` values; the EventBus management plane is intentionally read-only and has no publish proxy.

## Endpoints

- NATS: `nats://127.0.0.1:4222`
- tRPC/HTTP: `trpc.moox.eventbus.EventBusMgr` on `:11420`
- Liveness: `GET /healthz` on `:11419`
- Readiness: `GET /readyz` on `:11419`
- Metrics: `GET /metrics` on `:11419`

## Configuration

`config/app.yaml` declares the broker, stream and consumer topology, and KV buckets. Event names, versions, payloads, subjects, and owning streams come exclusively from `packages/events/registry/events.yaml`. Stream/KV reconciliation is completed before `/readyz` becomes successful. Retention and storage changes are rejected automatically; only bounded limits and descriptions may be changed in place.

The `consumers` section is also the single source of truth for durable consumer topology. Storage View, Factor, and Archive use `packages/jetstream.BindManagedPullConsumer` with only their Stream, Durable, fetch wait, and poison-decode choice; they do not create, update, or delete consumers and must not duplicate filter or delivery limits in service configuration.

The `store_dir` is runtime state and is intentionally not included in release archives. Deployment rewrites it to `../data/eventbus/jetstream`.

## Disk Bounds

EventBus uses file-backed JetStream with `DiscardOld`; reaching either `max_age` or `max_bytes` removes the oldest eligible messages. Current defaults are:

| Stream/KV | Time bound | Byte bound |
| --- | --- | --- |
| `MOOX_STORAGE` | 72 hours | 2 GiB |
| `MOOX_METRICS` | 24 hours | 512 MiB |
| `MOOX_CLOUDNODE_EXEC` | 72 hours | 512 MiB |
| `MOOX_TRADE` | 7 days | 512 MiB |
| `MOOX_STRATEGY` | 7 days | 512 MiB |
| `MOOX_CLOUDNODE_JOB_ACTIVE` KV | 48 hours | no separate byte limit |

These bounds protect the broker directory; they do not delete facts already committed to Storage, Trade, Strategy, or Archive. A durable consumer that remains offline beyond Stream retention can have an unrecoverable event gap, so increasing retention trades recovery time for disk capacity. See [data retention and disk space](../../docs/运维/数据保留与磁盘空间.md) before changing production limits.

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
