# MooX EventBus Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `moox-eventbus` as the single MooX-owned NATS JetStream service, define the common `MooxMessage` wire contract, provide a reusable JetStream client package, and migrate Storage, CloudNode, and existing consumers away from private embedded brokers.

**Architecture:** `modules/eventbus` embeds `nats-server/v2` and owns broker lifecycle, Stream/KV reconciliation, health, metrics, and a read-only tRPC management plane. Business processes connect directly to its NATS port through `packages/jetstream`; there is no tRPC/HTTP publish proxy. Every MooX application message is an individually acknowledged protobuf `MooxMessage`, while domain payloads remain opaque and versioned by Topic.

**Tech Stack:** Go 1.24, tRPC-Go, Protocol Buffers, `github.com/nats-io/nats-server/v2` v2.11.3, `github.com/nats-io/nats.go` v1.47.0, Prometheus client library, existing Admin gateway/SysDeploy conventions, and existing build/release scripts.

---

## Reading Summary

- `modules/storage` currently has a private transport abstraction, a JetStream producer/subscriber, optional embedded NATS startup, and row-change subjects under `moox.storage.>`.
- `modules/cloudnode` owns a second JetStream runtime and optional embedded server on a different port; it uses a WorkQueue Stream plus a JetStream KV bucket.
- `modules/factor` directly imports `nats.go` to subscribe to Storage messages.
- There is no public NATS package under `packages/`, so connection, Stream reconciliation, ACK, retry, and shutdown behavior are duplicated.
- `go.work` treats each module and shared package as a separate Go module. The new protocol, client package, EventBus service, and generated tRPC package must each be wired explicitly.
- The existing Storage write path commits PrimaryStore data before publishing its event. This plan replaces that non-atomic gap with a local durable outbox on each PrimaryStore shard and an asynchronous relay to the central EventBus.
- The existing `docs/superpowers/plans/2026-07-08-storage-view-write-journal-materialization.md` still governs View hot-path materialization. This plan owns the common message protocol, central JetStream topology, Storage outbox, and transport migration; it must preserve that plan's `RowsUpdated` payload semantics.

## Locked Decisions

| # | Decision |
|---|---|
| D1 | The internal protocol is solely **MooX Message Protocol v1**; core code, configuration, and documentation use MooX protocol terminology. |
| D2 | The single common protobuf type is `MooxMessage`; do not introduce an alternate wrapper type. |
| D3 | `MooxMessage` does not contain `resource_key`, `partition_key`, `correlation_id`, or `causation_id`. Business identifiers stay in domain payloads; transport ordering is a `PublishOption`. |
| D4 | `topic` is both the concrete NATS Subject and the versioned payload contract. One Topic major version maps to exactly one payload schema. |
| D5 | Services publish directly to the NATS listener embedded in `moox-eventbus`. There is no generic Publish RPC or HTTP endpoint. |
| D6 | `modules/eventbus` is the only production owner of `nats-server/v2`; Storage and CloudNode stop starting embedded servers. Tests may start an in-process NATS server. |
| D7 | `modules/eventbus` owns Stream and KV definitions. Business clients may publish and create/bind durable consumers but may not mutate Stream/KV retention configuration. |
| D8 | Delivery is at least once. Publisher retries reuse `message_id`; consumers remain idempotent even beyond the broker deduplication window. |
| D9 | Batch throughput uses asynchronous publication of multiple independent NATS messages. Do not combine unrelated logical messages into a transport-level batch with one ACK. |
| D10 | Callback subscriptions are not implemented in V1. `MooxMessage`, durable pull consumers, and the management model must allow a future callback-delivery worker without changing publishers. |
| D11 | The logical EventBus supports standalone and clustered deployment. Repository defaults use one node/one replica; production may run three `moox-eventbus` instances with three Stream replicas without changing client or message protocols. |

## Protocol Contract

```protobuf
syntax = "proto3";

package trpc.moox.message;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/mooyang-code/moox/packages/messagepb;messagepb";

enum MessageKind {
  MESSAGE_KIND_UNSPECIFIED = 0;
  MESSAGE_KIND_EVENT = 1;
  MESSAGE_KIND_COMMAND = 2;
  MESSAGE_KIND_SNAPSHOT = 3;
}

message Producer {
  string service_name = 1;
  string instance_id = 2;
  string node_id = 3;
  string boot_id = 4;
  string version = 5;
}

message TraceContext {
  string trace_id = 1;
  string request_id = 2;
}

message MooxMessage {
  uint32 protocol_version = 1;
  string message_id = 2;
  string topic = 3;
  MessageKind kind = 4;
  Producer producer = 5;
  string space_id = 6;
  uint64 sequence = 7;
  google.protobuf.Timestamp occurred_at = 8;
  google.protobuf.Timestamp published_at = 9;
  string content_type = 10;
  bytes payload = 11;
  TraceContext trace = 12;
  map<string, string> attributes = 13;
}
```

### Field Semantics

- `protocol_version` is the outer MooX Message protocol version and is `1` for this plan.
- `message_id` is globally unique, stable across retries, and copied to the `Nats-Msg-Id` header.
- `topic` must exactly equal the concrete NATS Subject used for publication.
- `content_type` describes `payload`, not the outer NATS body. Protobuf payloads use `application/x-protobuf; message=<fully-qualified-proto-name>`.
- The NATS `Content-Type` header describes the outer message and is fixed to `application/vnd.moox.message+protobuf`.
- `sequence` increases within `producer.service_name + producer.instance_id + producer.boot_id`; zero means the producer does not supply gap detection.
- `occurred_at` records the business occurrence. `published_at` records the first relay publication attempt and is not rewritten on retry.
- `attributes` is limited to routing-neutral, low-cardinality metadata. Secrets and complete business objects are forbidden.

### Topic Naming

```text
moox.<domain>.<entity>.<action>.v<major>
```

Initial Topic catalog:

```text
moox.storage.time_series.rows_updated.v1
moox.storage.record.rows_updated.v1
moox.metrics.snapshot.reported.v1
moox.cloudnode.job.requested.v1
moox.dlq.message.rejected.v1
```

Compatible protobuf field additions retain the Topic version. Field renumbering, meaning changes, or incompatible payload replacement require a new `.v2` Topic and a parallel migration period.

## JetStream Topology

| Stream | Subjects | Retention | Default limits |
|---|---|---|---|
| `MOOX_STORAGE` | `moox.storage.>` | Limits/File/DiscardOld | 72h, 20 GiB |
| `MOOX_METRICS` | `moox.metrics.>` | Limits/File/DiscardOld | 24h, 10 GiB |
| `MOOX_CLOUDNODE_EXEC` | `moox.cloudnode.job.requested.v1` | WorkQueue/File/DiscardOld | 72h, 10 GiB |
| `MOOX_DLQ` | `moox.dlq.>` | Limits/File/DiscardOld | 30d, 2 GiB |

`MOOX_CLOUDNODE_JOB_ACTIVE` remains a JetStream KV bucket with one value per key and a configurable 48h TTL. These defaults are explicit YAML values, not hidden constants, and production sizing is changed through EventBus configuration only.

## Target File Map

### Common Message Protocol

- Create `packages/messagepb/go.mod`, `Makefile`, `moox_message.proto`, generated `moox_message.pb.go`, and `contract_test.go`.
- Modify `go.work` and root `Makefile` to include protocol generation and tests.

### Shared JetStream Client

- Create `packages/jetstream/go.mod`.
- Create `packages/jetstream/config.go`, `client.go`, `publisher.go`, `consumer.go`, `delivery.go`, `codec.go`, `errors.go`, and focused tests.
- The package imports `nats.go` and `packages/messagepb`; it does not import `nats-server/v2`.

### EventBus Service

- Create `modules/eventbus/go.mod`, `cmd/server/main.go`, `config/app.yaml`, and `config/trpc_go.yaml`.
- Create `modules/eventbus/internal/config`, `broker`, `registry`, `management`, `health`, and `bootstrap` packages.
- Create `modules/eventbus/proto/eventbus.proto`, `Makefile`, and generated module `modules/eventbus/proto/eventbusgen`.
- Create `modules/eventbus/README.md`.

### Existing Module Migration

- Modify Storage's message proto, eventbus bootstrap, PrimaryStore write path, Pebble store, and split-role configuration.
- Modify CloudNode's JetStream bootstrap and queue to use `packages/jetstream` and the central endpoint.
- Modify Factor's Storage trigger consumer to use `packages/jetstream`.
- Remove private embedded NATS implementations and direct `nats.go` imports after migration tests pass.

### Product And Operations

- Modify `modules/admin/proto/sysdeploy_service.proto`, `modules/admin/internal/service/sysdeploy/defaults.go`, `modules/admin/internal/service/sysdeploy/defaults_test.go`, `scripts/build.sh`, `scripts/release.sh`, `scripts/deploy-moox.sh`, root `Makefile`, `go.work`, `modules/README.md`, and root `README.md`.
- Add deployment regression coverage for `moox-eventbus`, its data directory, and disabled-component preservation.

## Delivery Order

| Milestone | Tasks | Exit condition |
|---|---|---|
| M1 Protocol and client | 1-2 | A validated `MooxMessage` can be published, fetched, ACKed, retried, and deduplicated against a test JetStream. |
| M2 EventBus service | 3-6 | The service starts its broker, reconciles configured resources, reports readiness, and exposes read-only tRPC status. |
| M3 Packaging | 7 | `moox-eventbus` is built, released, deployed, registered in SysDeploy, and survives restart with persisted JetStream data. |
| M4 Storage migration | 8-9 | PrimaryStore commits data plus outbox atomically and relays versioned Storage messages asynchronously to the central EventBus. |
| M5 Remaining migration | 10-11 | CloudNode and Factor use the central EventBus; production modules contain no embedded NATS server. |
| M6 Verification | 12 | Failure, restart, load, dedupe, and deployment checks pass end to end. |

---

### Task 1: Add The MooX Message Protocol

**Files:**
- Create: `packages/messagepb/go.mod`
- Create: `packages/messagepb/Makefile`
- Create: `packages/messagepb/moox_message.proto`
- Generate: `packages/messagepb/moox_message.pb.go`
- Create: `packages/messagepb/contract_test.go`
- Modify: `go.work`
- Modify: `Makefile`

- [ ] **Step 1: Write the descriptor contract test**

Create a test that loads `(&messagepb.MooxMessage{}).ProtoReflect().Descriptor()` and asserts field numbers/names, `MessageKind` values, and the absence of `resource_key`, `partition_key`, `correlation_id`, and `causation_id`.

```go
func TestMooxMessageContract(t *testing.T) {
    d := (&MooxMessage{}).ProtoReflect().Descriptor()
    want := map[protoreflect.Name]protoreflect.FieldNumber{
        "protocol_version": 1, "message_id": 2, "topic": 3,
        "kind": 4, "producer": 5, "space_id": 6, "sequence": 7,
        "occurred_at": 8, "published_at": 9, "content_type": 10,
        "payload": 11, "trace": 12, "attributes": 13,
    }
    for name, number := range want {
        if field := d.Fields().ByName(name); field == nil || field.Number() != number {
            t.Fatalf("field %s number mismatch", name)
        }
    }
}
```

- [ ] **Step 2: Define the complete protobuf contract**

Write `moox_message.proto` exactly as shown in **Protocol Contract**. Do not add `google.protobuf.Any`; the EventBus treats domain payloads as opaque bytes.

- [ ] **Step 3: Add generation and workspace wiring**

Use the existing `packages/commonpb/Makefile` as the command pattern. Add `./packages/messagepb` to `go.work` and a root Makefile target that runs generation from the package directory.

- [ ] **Step 4: Generate and verify**

Run:

```bash
make -C packages/messagepb
go test -count=1 ./packages/messagepb
```

Expected: generated Go code compiles and `TestMooxMessageContract` passes.

- [ ] **Step 5: Commit**

```bash
git add go.work Makefile packages/messagepb
git commit -m "feat(eventbus): define moox message protocol"
```

---

### Task 2: Build The Shared JetStream Client

**Files:**
- Create: `packages/jetstream/go.mod`
- Create: `packages/jetstream/config.go`
- Create: `packages/jetstream/client.go`
- Create: `packages/jetstream/codec.go`
- Create: `packages/jetstream/publisher.go`
- Create: `packages/jetstream/consumer.go`
- Create: `packages/jetstream/delivery.go`
- Create: `packages/jetstream/errors.go`
- Create: `packages/jetstream/client_test.go`
- Create: `packages/jetstream/publisher_test.go`
- Create: `packages/jetstream/consumer_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write validation and codec tests**

Test rejection of protocol version zero, empty IDs, invalid Topics, Topic/Subject mismatch, missing producer identity, missing timestamps, missing payload content type, and oversized payloads. Test deterministic protobuf round trips and verify decoding copies byte slices before returning them.

- [ ] **Step 2: Define the public API**

```go
type Config struct {
    URLs           []string
    Name           string
    Username       string
    Password       string
    Credentials    string
    TLSCAFile      string
    TLSCertFile    string
    TLSKeyFile     string
    ConnectTimeout time.Duration
    ReconnectWait  time.Duration
    MaxReconnects  int
    MaxPayload     int
}

type PublishOption func(*publishOptions)

func WithOrderingKey(key string) PublishOption

type PublishAck struct {
    Stream    string
    Sequence  uint64
    Duplicate bool
}

func Connect(ctx context.Context, cfg Config) (*Client, error)
func (c *Client) Publish(ctx context.Context, msg *messagepb.MooxMessage, opts ...PublishOption) (*PublishAck, error)
func (c *Client) PublishBatch(ctx context.Context, messages []*messagepb.MooxMessage, opts ...PublishOption) []PublishResult
func (c *Client) NewPullConsumer(ctx context.Context, cfg ConsumerConfig) (*PullConsumer, error)
func (c *Client) Close() error
```

`PublishBatch` must issue independent async JetStream publications and return one result per input message in input order. It must not create a transport batch with one ACK.

- [ ] **Step 3: Implement message transport mapping**

Set:

```text
NATS Subject: msg.topic
Nats-Msg-Id: msg.message_id
Content-Type: application/vnd.moox.message+protobuf
Moox-Ordering-Key: PublishOption value, when non-empty
```

Validate before marshal, use JetStream PubAck, preserve the original `message_id` on retry, and return typed errors for validation, connection, publish timeout, and decode failures.

- [ ] **Step 4: Implement durable pull delivery**

Expose explicit delivery control:

```go
type Delivery struct {
    Message      *messagepb.MooxMessage
    Subject      string
    Stream       string
    Consumer     string
    StreamSeq    uint64
    ConsumerSeq  uint64
    DeliveryCount uint64
}

func (d *Delivery) Ack(ctx context.Context) error
func (d *Delivery) Nak(ctx context.Context, delay time.Duration) error
func (d *Delivery) InProgress(ctx context.Context) error
func (d *Delivery) Term(ctx context.Context) error
```

`Fetch` returns deliveries without auto-ACK. Domain code decides when durable work has completed.

- [ ] **Step 5: Add integration tests with a test-only embedded server**

Cover PubAck, `Nats-Msg-Id` duplicate detection, redelivery after NAK, redelivery after `AckWait`, `AckSync`, reconnect, malformed body rejection, and batch partial failure. The embedded NATS dependency belongs in test imports only.

- [ ] **Step 6: Run tests and commit**

```bash
go test -count=1 ./packages/messagepb ./packages/jetstream
git add go.work packages/jetstream
git commit -m "feat(eventbus): add shared jetstream client"
```

---

### Task 3: Define The EventBus Management Protocol

**Files:**
- Create: `modules/eventbus/proto/eventbus.proto`
- Create: `modules/eventbus/proto/Makefile`
- Create: `modules/eventbus/proto/eventbusgen/go.mod`
- Generate: `modules/eventbus/proto/eventbusgen/eventbus.pb.go`
- Generate: `modules/eventbus/proto/eventbusgen/eventbus.trpc.go`
- Create: `modules/eventbus/proto/eventbusgen/contract_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write a service descriptor test**

Lock the service name `trpc.moox.eventbus.EventBusMgr` and methods `GetOverview`, `ListTopics`, `ListStreams`, `ListConsumers`, and `GetConsumer`. Assert that no method named `Publish`, `Send`, or `Produce` exists.

- [ ] **Step 2: Define read-only management messages**

The proto must use `common.RetInfo` and `common.Page/PageResult`, and expose:

```protobuf
message TopicInfo { string topic = 1; string stream = 2; MessageKind kind = 3; string payload_content_type = 4; uint32 payload_version = 5; bool enabled = 6; }
message StreamInfo { string name = 1; repeated string subjects = 2; string retention = 3; string storage = 4; uint64 messages = 5; uint64 bytes = 6; string first_time = 7; string last_time = 8; }
message ConsumerInfo { string stream = 1; string name = 2; string filter_subject = 3; uint64 pending = 4; uint64 ack_pending = 5; uint64 redelivered = 6; string last_delivered_at = 7; }
message Overview { bool jetstream_ready = 1; uint32 connections = 2; uint32 streams = 3; uint32 consumers = 4; uint64 messages = 5; uint64 bytes = 6; uint64 total_pending = 7; }
```

Use `messagepb.MessageKind` rather than defining a second enum.

- [ ] **Step 3: Generate, test, and commit**

```bash
make -C modules/eventbus/proto
go test -count=1 ./modules/eventbus/proto/eventbusgen
git add go.work modules/eventbus/proto
git commit -m "feat(eventbus): define management protocol"
```

---

### Task 4: Add EventBus Configuration And Broker Lifecycle

**Files:**
- Create: `modules/eventbus/go.mod`
- Create: `modules/eventbus/config/app.yaml`
- Create: `modules/eventbus/config/trpc_go.yaml`
- Create: `modules/eventbus/internal/config/config.go`
- Create: `modules/eventbus/internal/config/config_test.go`
- Create: `modules/eventbus/internal/broker/server.go`
- Create: `modules/eventbus/internal/broker/server_test.go`
- Create: `modules/eventbus/cmd/server/main.go`
- Modify: `go.work`

- [ ] **Step 1: Write configuration tests**

Cover defaults, YAML overrides, environment overrides, duplicate Stream names, overlapping exact Topics, invalid Topic versions, Stream subject coverage, unsafe root StoreDir, incomplete TLS configuration, and authentication enabled without credentials.

- [ ] **Step 2: Add explicit default configuration**

```yaml
broker:
  host: 0.0.0.0
  port: 4222
  server_name: eventbus-dev-1
  store_dir: ./data/eventbus/jetstream
  startup_timeout: 10s
  max_payload_bytes: 8388608
  cluster:
    enabled: false
    name: MOOX_EVENTBUS
    host: 0.0.0.0
    port: 6222
    routes: []
  auth:
    enabled: false
    username: ""
    password: ""
  tls:
    enabled: false
    cert_file: ""
    key_file: ""
    ca_file: ""

health:
  addr: ":11419"

streams:
  - name: MOOX_STORAGE
    subjects: ["moox.storage.>"]
    retention: limits
    replicas: 1
    max_age: 72h
    max_bytes: 21474836480
  - name: MOOX_METRICS
    subjects: ["moox.metrics.>"]
    retention: limits
    replicas: 1
    max_age: 24h
    max_bytes: 10737418240
  - name: MOOX_CLOUDNODE_EXEC
    subjects: ["moox.cloudnode.job.requested.v1"]
    retention: work_queue
    replicas: 1
    max_age: 72h
    max_bytes: 10737418240
  - name: MOOX_DLQ
    subjects: ["moox.dlq.>"]
    retention: limits
    replicas: 1
    max_age: 720h
    max_bytes: 2147483648
```

Add the Topic registry and CloudNode KV bucket in the same file with explicit values.

- [ ] **Step 3: Implement broker ownership**

`broker.Server` creates `natsserver.Options`, starts the server, waits for `ReadyForConnections`, exposes a local client URL, and shuts down only after clients and management workers have drained. When clustering is enabled, require a unique `server_name`, configure cluster host/port/routes, and reject Stream replica counts greater than the reachable cluster size. Do not enable the NATS HTTP monitoring port; MooX exposes curated status through its own health/metrics server.

- [ ] **Step 4: Add tRPC service configuration**

Use admin port `11960`, service name `trpc.moox.eventbus.EventBusMgr`, service port `11420`, and HTTP protocol consistent with the Monitor module. The NATS listener remains configured in `app.yaml` rather than as a tRPC service.

- [ ] **Step 5: Verify lifecycle and commit**

```bash
go test -count=1 ./modules/eventbus/internal/config ./modules/eventbus/internal/broker
git add go.work modules/eventbus
git commit -m "feat(eventbus): own embedded jetstream lifecycle"
```

---

### Task 5: Reconcile Streams, KV Buckets, And Topics

**Files:**
- Create: `modules/eventbus/internal/registry/registry.go`
- Create: `modules/eventbus/internal/registry/stream.go`
- Create: `modules/eventbus/internal/registry/kv.go`
- Create: `modules/eventbus/internal/registry/topic.go`
- Create: `modules/eventbus/internal/registry/registry_test.go`

- [ ] **Step 1: Write reconciliation tests**

Start a test broker and verify create, no-op restart, safe limit updates, rejection of subject removal with stored messages, rejection of retention-policy changes, KV TTL reconciliation, Topic-to-Stream validation, and preservation of consumer state.

- [ ] **Step 2: Implement safe reconciliation**

Allow updates to `MaxAge`, `MaxBytes`, `MaxMsgs`, replicas, and description. Reject automatic changes to retention kind, storage kind, and subject removal when they can orphan existing messages. Return an error before readiness rather than silently accepting partial configuration.

- [ ] **Step 3: Make readiness depend on reconciliation**

The service reports ready only after all configured Streams and KV buckets exist and every enabled Topic is covered by exactly one Stream.

- [ ] **Step 4: Run and commit**

```bash
go test -count=1 ./modules/eventbus/internal/registry
git add modules/eventbus/internal/registry
git commit -m "feat(eventbus): reconcile stream and topic registry"
```

---

### Task 6: Add Management RPC, Health, And Metrics

**Files:**
- Create: `modules/eventbus/internal/management/service.go`
- Create: `modules/eventbus/internal/management/service_test.go`
- Create: `modules/eventbus/internal/health/server.go`
- Create: `modules/eventbus/internal/health/server_test.go`
- Create: `modules/eventbus/internal/bootstrap/bootstrap.go`
- Create: `modules/eventbus/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/eventbus/cmd/server/main.go`

- [ ] **Step 1: Write RPC and readiness tests**

Assert pagination, stable status conversion, consumer lag, missing resource errors, broker-not-ready behavior, and that no RPC can create/purge/delete a Stream or publish a message.

- [ ] **Step 2: Implement `EventBusMgr`**

Read broker and JetStream state through interfaces so unit tests do not require sockets. Sort Streams, Topics, and Consumers by stable names before pagination. Sanitize NATS errors into `common.RetInfo` without returning credentials or filesystem paths.

- [ ] **Step 3: Expose operational endpoints**

Implement:

```text
GET /healthz  process and broker liveness
GET /readyz   broker readiness plus successful resource reconciliation
GET /metrics  connection, stream bytes/messages, consumer pending/redelivery, publish advisories
```

Register bounded metric labels using Stream and Consumer names only.

- [ ] **Step 4: Implement ordered bootstrap and shutdown**

Startup order is config -> broker -> local JetStream client -> registry reconciliation -> management RPC -> health server. Shutdown order is health not-ready -> management stop -> client drain -> broker shutdown.

- [ ] **Step 5: Verify and commit**

```bash
go test -count=1 ./modules/eventbus/...
git add modules/eventbus
git commit -m "feat(eventbus): expose management and health surfaces"
```

---

### Task 7: Integrate Build, Release, Deploy, And SysDeploy

**Files:**
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/admin/proto/sysdeploy_service.proto`
- Regenerate: `modules/admin/proto/admingen`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/README.md`
- Modify: `README.md`
- Create: `scripts/test-deploy-moox-eventbus.sh`

- [ ] **Step 1: Add failing deploy-package assertions**

The script must assert the presence of `bin/moox-eventbus`, `eventbus/config/app.yaml`, `data/eventbus/jetstream`, `logs/eventbus`, start-before-Storage ordering, stop-after-consumers ordering, and preservation under `--no-eventbus`.

- [ ] **Step 2: Wire build and release**

Add `eventbus` to `scripts/build.sh`, release directories, binary copy, configuration copy, and the all-components build. Do not package JetStream runtime data.

- [ ] **Step 3: Wire deployment lifecycle**

Add `--no-eventbus`, stage path rewrites for `store_dir`, readiness waiting on `/readyz`, and start EventBus before Storage/CloudNode/Factor/Monitor. Stop publishers/consumers before stopping EventBus.

- [ ] **Step 4: Register the service in SysDeploy**

Add the deployment key `eventbus`, service name `trpc.moox.eventbus.EventBusMgr`, and Admin gateway route `/api/admin/eventbus/{method}` using the existing generated deployment model.

- [ ] **Step 5: Verify and commit**

```bash
./scripts/build.sh eventbus
./scripts/test-deploy-moox-eventbus.sh
go test -count=1 ./modules/admin/...
git add scripts modules/admin modules/README.md README.md
git commit -m "feat(eventbus): package and deploy eventbus service"
```

---

### Task 8: Migrate Storage Messages To `MooxMessage`

**Files:**
- Modify: `modules/storage/proto/message.proto`
- Modify: `modules/storage/proto/store.proto`
- Regenerate: `modules/storage/proto/gen`
- Modify: `modules/storage/internal/core/eventbus/bus.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory.go`
- Modify: `modules/storage/internal/services/access/data.go`
- Modify: `modules/storage/internal/services/primary/client.go`
- Modify: `modules/storage/internal/services/primary/local.go`
- Modify: `modules/storage/internal/services/primary/remote.go`
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/go.mod`
- Modify: `modules/storage/config/storage*.yaml`
- Test: focused Storage eventbus, access, primary, and proto tests

- [ ] **Step 1: Lock the Storage payload contracts**

Replace key-only `RowsChangedEvent` messages with the full-row `TimeSeriesRowsUpdated` and `RecordRowsUpdated` payloads defined by the existing write-journal materialization plan. Add descriptor tests for `message_id`, `written_at`, `space_id`, `dataset_id`, rows, and attributes.

- [ ] **Step 2: Extend the PrimaryStore internal write request**

Add `bytes outbox_message = 4` to `WritePrimaryRowsReq`. Access constructs one stable `MooxMessage` per routed PrimaryStore group, marshals it, and sends it with the rows. Local and remote clients preserve the same bytes across retries.

- [ ] **Step 3: Replace the private transport implementation**

Keep the domain `eventbus.Publisher/Subscriber` interfaces but implement them using `packages/jetstream`. Decode and validate `MooxMessage`, then unmarshal its domain payload. Remove Stream mutation from Storage startup because `moox-eventbus` owns `MOOX_STORAGE`.

- [ ] **Step 4: Change configuration to the central endpoint**

Replace `embedded` settings with shared client configuration:

```yaml
eventbus:
  type: jetstream
  urls: ["nats://127.0.0.1:4222"]
  stream_name: MOOX_STORAGE
  subject_prefix: moox.storage
  consumer_name: storage_view
```

Retain `type: memory` only for explicitly single-process tests; production split-role configs use JetStream.

- [ ] **Step 5: Verify message interoperability**

Publish through Storage Access, fetch through a durable test consumer, assert Topic/body/content type/producer identity, and confirm duplicate delivery does not duplicate View effects.

- [ ] **Step 6: Run and commit**

```bash
go test -count=1 ./modules/storage/internal/core/eventbus ./modules/storage/internal/infra/eventbus ./modules/storage/internal/services/access ./modules/storage/internal/services/primary
git add modules/storage
git commit -m "refactor(storage): publish storage updates through moox messages"
```

---

### Task 9: Add The PrimaryStore Durable Outbox And Relay

**Files:**
- Modify: `modules/storage/internal/infra/device/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Create: `modules/storage/internal/infra/device/pebble/outbox.go`
- Create: `modules/storage/internal/infra/device/pebble/outbox_test.go`
- Create: `modules/storage/internal/services/primary/outbox_relay.go`
- Create: `modules/storage/internal/services/primary/outbox_relay_test.go`
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage*.yaml`
- Modify: `modules/storage/cmd/server/main.go`

- [ ] **Step 1: Write atomicity tests**

Inject failures before and during Pebble batch commit. Assert that rows and outbox entry are both absent or both present. Assert that a committed entry survives Store close/reopen with unchanged message bytes and ID.

- [ ] **Step 2: Extend the fact store interface**

```go
type OutboxEntry struct {
    Sequence  uint64
    MessageID string
    Topic     string
    Data      []byte
    CreatedAt time.Time
}

type FactStore interface {
    WriteRowsWithOutbox(ctx context.Context, rows []*pb.PrimaryStoreRow, entry *OutboxEntry) error
    ListOutbox(ctx context.Context, after uint64, maxItems int, maxBytes int) ([]*OutboxEntry, error)
    DeleteOutbox(ctx context.Context, sequences []uint64) error
    // Existing read and scan methods remain unchanged.
}
```

Use a reserved Pebble key prefix that cannot collide with fact rows. Allocate `sequence` in the same Pebble batch as data and outbox bytes.

- [ ] **Step 3: Make PrimaryStore success mean data plus outbox durability**

Validate the supplied `MooxMessage`, require its producer identity to match the local PrimaryStore node, and call `WriteRowsWithOutbox`. Return success without waiting for EventBus PubAck.

- [ ] **Step 4: Implement asynchronous batched relay**

The relay reads up to the first reached limit of `flush_batch_size`, `flush_max_bytes`, or `flush_interval`, calls `packages/jetstream.PublishBatch`, and deletes only entries with successful PubAck. Failures remain in Pebble and retry with bounded exponential backoff plus jitter. Independent messages retain independent ACK results.

- [ ] **Step 5: Add backlog limits and backpressure**

Configure `max_rows`, `max_bytes`, and `max_age`. Reject new writes before PrimaryStore mutation when the durable outbox exceeds its hard limit; never delete undelivered Storage events to make room. Export depth, bytes, oldest age, publish failures, and last PubAck time.

- [ ] **Step 6: Test outage and restart**

Stop EventBus, commit writes, restart the PrimaryStore process, start EventBus, and assert every message is eventually published. Simulate crash after PubAck but before delete and assert duplicate publication is harmless.

- [ ] **Step 7: Run and commit**

```bash
go test -count=1 ./modules/storage/internal/infra/device/pebble ./modules/storage/internal/services/primary ./modules/storage/internal/services/access
git add modules/storage
git commit -m "feat(storage): relay durable outbox to eventbus"
```

---

### Task 10: Migrate CloudNode To The Central EventBus

**Files:**
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store.go`
- Delete: `modules/cloudnode/internal/jobqueue/embedded.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/go.mod`
- Test: CloudNode jobqueue and jobstate tests

- [ ] **Step 1: Write central-runtime compatibility tests**

Create `MOOX_CLOUDNODE_EXEC` and the active KV bucket through the EventBus registry fixture, then verify publish, poll, report/ACK, stale lease, redelivery, deduplication, and restart behavior through `packages/jetstream`.

- [ ] **Step 2: Wrap job payloads in `MooxMessage`**

Use Topic `moox.cloudnode.job.requested.v1`, Kind `COMMAND`, Job ID as the stable message ID, the existing `JobItem` protobuf as payload, and `space_id` from the job. Preserve current attempt/lease checks above the transport.

- [ ] **Step 3: Remove embedded-server ownership**

Replace `nats_url` plus `embedded` with a shared list of EventBus URLs and credentials/TLS fields. CloudNode may ensure/bind its durable consumer but must not create/update the Stream or KV bucket.

- [ ] **Step 4: Verify and commit**

```bash
go test -count=1 ./modules/cloudnode/internal/jobqueue ./modules/cloudnode/internal/jobstate ./modules/cloudnode/internal/bootstrap
git add modules/cloudnode
git commit -m "refactor(cloudnode): use central moox eventbus"
```

---

### Task 11: Migrate Factor And Remove Private NATS Implementations

**Files:**
- Modify: `modules/factor/internal/trigger/nats.go`
- Modify: `modules/factor/internal/config/config.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/go.mod`
- Delete: `modules/storage/internal/bootstrap/eventbus/embedded_nats.go`
- Delete: `modules/storage/internal/infra/transport/nats/producer.go`
- Delete: private transport tests superseded by `packages/jetstream` tests
- Modify: `modules/storage/go.mod`

- [ ] **Step 1: Migrate Factor's Storage consumer**

Bind a durable pull consumer to the relevant `moox.storage.>` Topics, decode `MooxMessage`, validate payload content type, and retain the current idempotent factor-run trigger behavior.

- [ ] **Step 2: Prove every production module uses the shared package**

Run:

```bash
rg 'nats-server/v2|github.com/nats-io/nats.go' modules packages \
  --glob '*.go' --glob 'go.mod'
```

Expected: `nats-server/v2` appears only in `modules/eventbus` and test files; `nats.go` appears only in `packages/jetstream`, `modules/eventbus`, and test fixtures.

- [ ] **Step 3: Delete superseded implementations and tidy modules**

Run `go mod tidy` in EventBus, Storage, CloudNode, Factor, `packages/messagepb`, and `packages/jetstream`. Confirm no production module can enable an embedded broker through YAML.

- [ ] **Step 4: Run and commit**

```bash
go test -count=1 ./modules/factor/... ./modules/storage/... ./modules/cloudnode/... ./packages/jetstream
git add modules packages
git commit -m "refactor(eventbus): remove module-private nats runtimes"
```

---

### Task 12: End-To-End Failure, Load, And Deployment Verification

**Files:**
- Create: `modules/eventbus/internal/integration/end_to_end_test.go`
- Create: `docs/运维/MooX-EventBus运维.md`
- Modify: `modules/eventbus/README.md`
- Modify: `README.md`

- [ ] **Step 1: Add end-to-end integration coverage**

Start EventBus with a temporary persistent StoreDir, publish Storage/metrics/CloudNode messages, bind independent durable consumers, restart EventBus, and verify Stream contents, consumer positions, KV state, PubAck, redelivery, and DLQ publication.

Start a three-node test cluster on loopback with three replicas, kill the current Stream leader, and verify publication, consumption, management status, and recovery continue without changing client configuration.

- [ ] **Step 2: Add bounded load checks**

Publish at least 100,000 independent 1 KiB messages through `PublishBatch`, maintain multiple durable consumers, and record throughput, p95 PubAck latency, process RSS, disk growth, pending counts, and recovery time after a 30-second broker outage. The test must enforce a temporary-directory byte cap and must not scan production data.

- [ ] **Step 3: Verify Storage outbox recovery**

Write while EventBus is unavailable, restart Storage, restore EventBus, and verify outbox depth returns to zero without missing logical IDs. Repeat a crash after PubAck and prove consumer idempotency.

- [ ] **Step 4: Verify packaged deployment**

Run:

```bash
./scripts/build.sh all
./scripts/release.sh
./scripts/test-deploy-moox-eventbus.sh
go test -count=1 ./modules/eventbus/... ./packages/messagepb ./packages/jetstream
```

Expected: all commands pass, the release contains no runtime JetStream data, and EventBus readiness succeeds before dependent services start.

- [ ] **Step 5: Document operations and commit**

Document backup/restore of `data/eventbus/jetstream`, capacity alerts, consumer-lag diagnosis, credential rotation, Stream change policy, and the rule that callbacks are asynchronous durable consumers rather than part of the publish path.

```bash
git add modules/eventbus docs/运维 README.md
git commit -m "docs(eventbus): add verification and operations guide"
```

---

## Final Acceptance Criteria

- All production MooX application messages use `MooxMessage` and a versioned Topic.
- `topic` exactly equals the NATS Subject; `Nats-Msg-Id` equals `message_id`.
- No production module except `moox-eventbus` embeds `nats-server/v2`.
- Storage writes wait only for data plus local outbox durability, not EventBus PubAck.
- Storage Relay batches independent async publications and deletes only PubAcked entries.
- EventBus owns all Stream/KV retention configuration and refuses unsafe automatic changes.
- The management plane is read-only and cannot publish messages.
- JetStream restart preserves messages, durable positions, and KV state.
- Consumers tolerate duplicate and redelivered messages.
- Build, release, deploy, unit, integration, outage, and bounded load checks pass.
